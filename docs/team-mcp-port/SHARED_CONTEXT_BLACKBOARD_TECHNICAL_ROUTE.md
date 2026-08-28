# 共享上下文黑板技术路线

> 状态：§1 协议/存储模型、§2 并发一致性、§3 token 节约、§4 绑定解绑、§6 隔离安全已实现（Go 内核）；§5 durable command/report bus 服务端已实现（Python MCP server）；P6.1 薄 blackboard CLI 已实现（2026-08-24）；P7 MCP 接线已实现（Python 走 Go CLI，SQLite 主写 + JSONL 双写，2026-08-24）；P2.5 durable inbox/controller bridge 接线已实现（Python 走 Go serve /inbox/items 入 member Controller，watermark envelope + tmux 降级标记，2026-08-25）。
> 适用范围：`Agent设计` 团队成员之间的共享结论/进度/证据，以及 leader 与成员之间的指令/回报管道。
>
> 非目标：共享成员完整思考过程、复制私有 transcript、把 leader 变成普通黑板作者、提供 exactly-once 的跨进程语义。

## 0. 设计决策

系统分成两个平面：

| 平面 | 写入者 | 读取者 | 内容 |
|---|---|---|---|
| Shared board | 成员（服务端盖章） | 成员、leader（只读） | 结论、进度、阻塞、检查点、证据指针 |
| Command/report bus | leader -> member；member -> leader | 对端及服务端恢复器 | 派单、确认、取消、完成回报 |

黑板是事实日志；物化视图是可丢弃缓存；游标是增量读取协议。消息管道是调度协议，不能用 TUI `send-keys` 文本注入替代持久化事实。

推荐落地顺序：

1. P1：协议字段、`seq`、`kind`、`digest`、`after_seq` 兼容层。
2. P2：SQLite WAL 主存储、幂等键、事务游标、CAS epoch、权限边界。
3. P3：L0-L3 物化视图、checkpoint、token 预算、摘要缓存。
4. P4：binding generation、durable command/report bus、旧 JSONL 迁移。
5. P5：归档、压缩审计、跨进程/跨主机恢复和完整验收门禁。

## 1. 黑板协议与存储模型

> 实现状态（P1/P2 已落地，2026-08-24）：
> - 文件：`internal/team/blackboard.go`（BoardEvent/AppendInput/Identity/Filter/Page/CursorUpdate/Conclusion/ViewSpec/错误类型 + BoardStore 接口）、`internal/team/blackboard_sqlite.go`（SQLiteStore：WAL、IMMEDIATE 事务、幂等、CAS、游标、ACL、归档）。
> - 测试：`internal/team/blackboard_sqlite_test.go`（`go test -race ./internal/team/ -run Blackboard`，9 个测试覆盖 §2.4 全部验收点）。
> - 偏差：事件信封的 `ArtifactRefs` 序列化存入 `payload_ref` 列（schema 未加列）；`ReadView` 为结论表快照的最小实现，L0-L3 预算与缓存属 P3；旧 `BlackboardDoc`（rev-N.json，§2.8）原样保留，未迁移。

### 1.1 事件信封

共享黑板只接受结构化事件，成员不能自报身份或序号：

```go
type BoardEvent struct {
    SchemaVersion uint16         `json:"schema_version"`
    BoardID       string         `json:"board_id"`
    Seq           int64          `json:"seq"`          // 服务端分配
    EventID       string         `json:"event_id"`
    ClientMsgID   string         `json:"client_msg_id"` // 幂等键
    Kind          EventKind      `json:"kind"`
    TaskID        string         `json:"task_id"`
    MemberID      string         `json:"member_id"`     // 服务端盖章
    Role          string         `json:"role"`          // 服务端盖章
    Agent         string         `json:"agent"`         // 服务端盖章
    Generation    uint64         `json:"generation"`    // 服务端盖章
    CreatedAt     time.Time      `json:"created_at"`
    Digest        string         `json:"digest"`
    Summary       string         `json:"summary"`
    ArtifactRefs  []ArtifactRef  `json:"artifact_refs,omitempty"`
    Supersedes    []int64        `json:"supersedes,omitempty"`
}

type EventKind string

const (
    EventReport      EventKind = "report"
    EventConclusion  EventKind = "conclusion"
    EventCheckpoint  EventKind = "checkpoint"
    EventEvidence    EventKind = "evidence"
    EventAssignment  EventKind = "assignment"
    EventSupersede   EventKind = "supersede"
)
```

`summary` 是共享上下文的默认载荷；大文件、补丁和日志只能通过 `ArtifactRef` 引用。`Supersedes` 保留审计链，禁止原地覆盖旧事件。

### 1.2 Store 接口

```go
type BoardStore interface {
    Append(ctx context.Context, in AppendInput) (BoardEvent, error)
    AppendBatch(ctx context.Context, in []AppendInput) ([]BoardEvent, error)
    ReadAfter(ctx context.Context, boardID string, afterSeq int64, f Filter) (Page, error)
    ReadView(ctx context.Context, boardID string, view ViewSpec) (MaterializedView, error)
    AdvanceCursor(ctx context.Context, cursor CursorUpdate) error
    Supersede(ctx context.Context, boardID string, ids []int64, replacement AppendInput) (BoardEvent, error)
    ArchiveBefore(ctx context.Context, boardID string, seq int64) error
}
```

`AppendInput` 不含 `Seq/MemberID/Role/Agent/Generation`；这些字段由当前窗口绑定解析并盖章。`ReadAfter` 返回 `next_seq`、`has_more` 和 `need_resync`，避免归档后静默漏读。

### 1.3 SQLite WAL schema

P2 默认使用 SQLite WAL；`results.jsonl` 保留为导出/审计视图。MQ 不作为单机主存储，避免额外运维和双重投递语义。

```sql
CREATE TABLE board_events (
  board_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  event_id TEXT NOT NULL,
  client_msg_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  task_id TEXT NOT NULL,
  member_id TEXT NOT NULL,
  role TEXT NOT NULL,
  agent TEXT NOT NULL,
  generation INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  digest TEXT NOT NULL,
  summary TEXT NOT NULL,
  payload_ref TEXT,
  supersedes_json TEXT,
  archived INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (board_id, seq),
  UNIQUE (board_id, client_msg_id)
);

CREATE TABLE board_cursors (
  board_id TEXT NOT NULL,
  consumer_id TEXT NOT NULL,
  generation INTEGER NOT NULL,
  last_seq INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (board_id, consumer_id)
);

CREATE TABLE board_conclusions (
  board_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  topic TEXT NOT NULL,
  epoch INTEGER NOT NULL,
  event_seq INTEGER NOT NULL,
  digest TEXT NOT NULL,
  summary TEXT NOT NULL,
  PRIMARY KEY (board_id, task_id, topic)
);

CREATE TABLE board_bindings (
  member_id TEXT NOT NULL,
  leader_id TEXT NOT NULL,
  generation INTEGER NOT NULL,
  status TEXT NOT NULL,
  task_id TEXT NOT NULL,
  bound_at TEXT NOT NULL,
  PRIMARY KEY (member_id)
);
```

事件写入、物化结论更新和游标推进必须在同一事务中完成，避免“消息已落盘但游标已前进”或反向半写。

### 1.4 兼容与迁移

现有 `results.jsonl` 字段（`timestamp/member/result/artifact_path/compressed_context_path/report_id`）映射为 `EventReport`。迁移分三步：

1. 导入：按文件顺序生成 `seq`，旧 `member` 标记 `identity_status=unverified`，不伪造新 generation。
2. 双写：新事件写 SQLite，并异步导出 JSONL；每日比较事件数、digest 和 task 关联。
3. 切读：默认从 SQLite 读取；旧 JSONL 只读归档，校验不一致则阻断归档。

旧 MCP 工具保持签名兼容：`member_report_result` 写 `Append`，`member_read_shared` 转为 `ReadView`，返回格式保留摘要字段并附 `next_seq`。

> 实现状态（P1 JSONL 导出器，2026-08-25，plugin-engineer-claude）：SQLite/WAL 为唯一事实源；
> `internal/team/blackboard_export.go` 新增只读 checkpoint-consistent JSONL snapshot exporter
> （`ExportSnapshot`）：单个只读事务内按 (board_id, seq) 顺序导出，并发 append 不撕裂（同一 WAL 快照），
> 重复导出逐字节相同；行格式为 legacy results.jsonl 兼容超集（`timestamp/member/result/artifact_path/report_id`
> 别名 + 全事件字段，别名取自盖章事件非客户端字段）；batch digest 与 `PlanMigration` 同算法
> （每行+换行 sha256），供 §4 步骤 2 每日对账；导出是派生快照——不参与写入路径、不双写分叉、
> 无第二个事实源。CLI 新增 `op=export`（响应携带 `snapshot` 摘要 + 整块 `jsonl`）。
> 测试：`blackboard_export_test.go`（golden 字节级、幂等、并发 append 无撕裂、中断后 reopen 恢复、SinceSeq/归档过滤）+ `protocol_test.go` `TestCLIExportSnapshot`，全部 `-race` 绿。

## 2. 高并发读写与一致性

> 实现状态（P1/P2 已落地，2026-08-24）：SQLiteStore 写路径为 BEGIN IMMEDIATE 事务（写锁前置，busy_timeout 5000 排队）；幂等按 `(board_id, client_msg_id)` 先查后写，重放返回原事件不产生新 seq；结论 CAS 按 `base_epoch` 匹配，冲突返回 `ErrConflict{CurrentEpoch, CurrentSeq}` 且整批回滚；游标按 consumer 独立行存储，单调推进 + generation 门控（`ErrStaleGeneration`/`ErrCursorBackwards`）；归档只置 `archived=1` 不重排 seq，落在归档洞内的游标读返回 `NeedResync`。

### 2.1 写入路径

```text
窗口请求
  -> 服务端解析绑定与 generation
  -> 校验 schema/ACL/redaction/client_msg_id
  -> BEGIN IMMEDIATE
  -> 分配 board seq
  -> 写 board_events + 更新物化视图
  -> COMMIT
  -> 返回 seq/digest
```

WAL 允许并发读，写事务保持短小；批量事件使用 `AppendBatch`，禁止调用方直接 append 文件。多进程部署以单写连接或短事务 CAS 为边界，暂不引入无界后台队列。

### 2.2 幂等、CAS 和冲突

- 相同 `(board_id, client_msg_id)` 重放只返回原事件，不产生新 `seq`。
- 结论修订使用 `base_epoch`；匹配时 `epoch + 1`，不匹配返回 `ErrConflict{CurrentEpoch, CurrentSeq}`，调用方重新读取后发布 `supersede`。
- 两个 leader/成员同时更新同一 topic 时最多一个 CAS 成功，另一方必须显式合并，不能静默覆盖。
- 读游标只允许单调前进；旧 generation 的 cursor 更新返回 `ErrStaleGeneration`。

### 2.3 读取与恢复

`ReadAfter` 的游标语义：

| 返回 | 语义 | 调用方动作 |
|---|---|---|
| `next_seq=0` | 首次读取 | 请求 L0 快照 |
| `next_seq>0` | 正常增量 | 保存 next_seq |
| `need_resync=true` | 游标落在归档洞内或 schema 不兼容 | 丢弃本地缓存，重新拉 L0/L1 |

进程崩溃后 SQLite 自动回滚未提交事务；已提交事件和游标同时存在。归档只设置 `archived=1` 或移动到只读分片，不重排逻辑 `seq`。

### 2.4 并发验收

- 10 个成员各写 100 条事件，`seq` 无重复、无撕裂、总数正确。
- 10 个消费者独立推进 cursor，不互相覆盖。
- CAS 冲突单赢，失败方得到当前 epoch。
- kill -9 后 WAL 重开，已提交不丢、未提交不出现。
- 同 `client_msg_id` 重放 N 次，最终只存在一条事件。

## 3. Token 节约与物化上下文

> 实现状态（P3 已落地，2026-08-24；游标持久化 P6.2）：
> - 文件：`internal/team/blackboard_view.go`（ViewLevel/BoardView/ViewBuilder：L0 索引+动作优先级、L1 增量、L2 详情；预算截断 truncated=true；CheckpointSummary/foldCheckpoints 折叠+supersede 审计链；contentDigest 字节稳定）、`internal/team/context_view.go`（BoardCursor/CursorStore 成员侧游标；ViewCache 缓存键 (board,member,level,epoch,source_seq)、epoch 整键失效、Last 视图 stale 回退；Assembler.Advance 游标三态与 ReadAfter 直连）、`internal/team/blackboard_cursor.go`（`SQLiteCursorStore`（P6.2）：成员游标直存 board_cursors 表，LoadCursor 对 generation 失配返回零值=首次读，SaveCursor 容忍 ErrStaleGeneration/ErrCursorBackwards；`MemoryCursorStore` 保留供测试/无 store 场景）。
> - 消费契约：直接实现 `BoardStore`（§1.2），无自造适配层；`ReadAfter` 空页 `NextSeq=0` 时游标保持原位不倒退（首次/空板不可分，见 §1.2 注意点 1）；`need_resync` 按 §2.3 丢弃该成员本地缓存后从 0 重建。
> - 测试：`internal/team/blackboard_view_test.go` + `context_view_test.go` + `blackboard_persist_test.go`（P6.2），`go test -race ./internal/team/` 全绿（预算有界/优先级顺序/增量与历史解耦/L2 checkpoint 折叠等价/游标三态/epoch 整键失效/无重复消费/stale 不空窗/并发消费者 -race/重启续读不重放/绑定与游标跨 reopen 恢复）。
> - 偏差与依赖：L2 为 delta 作用域（每轮新事件折叠），任务切换/重绑的"全板 L2"待 P2 `ReadView`/`board_conclusions` 增量投影接入（seam 为 `CheckpointSummary` 映射，需与 BoardStore 作者协调私有板签名）；`advanceMirror` 与 `SQLiteCursorStore.SaveCursor` 同为镜像写入（容忍 ErrStaleGeneration/ErrCursorBackwards，镜像仅恢复辅助）。

### 3.1 L0-L3 视图

| 层级 | 内容 | 默认上限 | 使用时机 |
|---|---|---:|---|
| L0 | 团队/task 索引、当前阻塞、最新 seq | 2 KB | 每轮注入 |
| L1 | 自上次 cursor 的摘要增量 | 4 KB | 有新事件时 |
| L2 | 最新结论、assignment、checkpoint、证据索引 | 8 KB | 任务切换/重绑 |
| L3 | 原始事件和完整 artifact | 不进入常规上下文 | 显式追溯 |

L0/L1/L2 是可重建物化视图，不是第二事实源；每个视图带 `source_seq`、`epoch` 和 `digest`。

### 3.2 增量、压缩和预算

- 每个成员维护 `board_cursor` 和 `command_cursor`，只读取 `after_seq`。
- 每 N 条事件或达到大小阈值生成 `CheckpointSummary`；旧事件通过 `supersedes` 保留审计引用。
- 使用 `client_msg_id`、内容 digest 和 supersede 链去重；摘要窗口采用“保留阻塞/最新结论，淘汰旧 delta”。
- 每个成员设置 token budget；优先级为阻塞 > 指令 > 最新结论 > 验收证据 > 历史报告。
- 缓存键为 `(board_id, member_id, view_level, epoch, source_seq)`；epoch 变化整键失效，禁止部分复用。
- 缓存不可用或摘要器失败时返回上一个视图并标记 `stale=true`，不返回空上下文。

### 3.3 防重复注入

消息注入只发送短提示和文件指针，载荷超过 2 KB 必须写入 inbox 文件。成员侧在 LLM 入口按 `msg_id` 去重；TUI seen-set 只用于显示幂等，不能替代服务端持久幂等。

### 3.4 Token 验收

- 事件数量增加时，L1 token 与增量成正比而非与历史总量成正比。
- 所有视图不超过预算；截断结果包含 `truncated=true` 和 `source_seq`。
- checkpoint 前后 L2 语义等价，digest 可追溯。
- 游标为空、正常、越界三态均能恢复。
- 同一事件重复注入只产生一次 LLM 消费。

## 4. 成员绑定/解绑与 session 生命周期

> ✅ 实现（2026-08-24，tui-researcher-claude）：`internal/team/blackboard_binding.go`（`BindingRegistry`：Bind/Unbind/GetBind/All，generation 门控，5s transitioning 超时回滚，handoff 校验）+ `blackboard_binding_test.go`（11 用例含并发单赢、幂等重放、失败不残留半绑定态）。
> ✅ P6.2 持久化（2026-08-24，plugin-engineer-claude）：`board_bindings` 表 + `SaveBinding`/`LoadBindings` + `NewBindingRegistryWithPersister`/`Restore`（write-before-commit：落盘失败保持原状态，重启不 bump generation，transitioning 不恢复）；`blackboard_persist_test.go` 验证跨 reopen 恢复。
> ✅ P6.3 TUI 接缝（2026-08-24，tui-researcher-claude）：`internal/cli/chat_tui_team_binding_calibration.go`——`calibrateTeamSessionBinding` 以服务端持久化 BindRecord 校准本地 session 绑定态（unbound/记录缺失→退出绑定态；generation 提升→刷新 L0/L2 并清 seen-set；transitioning 不动，由服务端定夺）+ 6 用例测试。
> ✅ P6.4 TUI 接线（2026-08-24，tui-researcher-claude）：l 改 assign-only（调 `assignFocusedLeader`，已有 leader 拒绝并显示持有人 id，leader 只经 k 下位）；k 走 `stepDownLeader`（architecture `chat_tui_team_leader_lifecycle.go`：stop backends → 清 leader 上下文 → 写 flag，任一步失败不写）；成员编辑器 role/Leader 只读展示（assignment 经 roster l / 底层 redefine API，API 保留）；x 退出全部团队会话（关 session 回管理页 + 清持久化 selection + 下次 [TEAM] 停管理页，t 显式进入恢复；不清 leader 身份）；`toggleRosterLeader` 移除。
> ✅ P6.5 可复用选项弹出列表（2026-08-25，tui-researcher-claude）：`internal/cli/option_list.go` 纯状态组件（`optionList`：`setOptions`/`handleKey`(consumed+Commit/Cancel action)/`wheel`/`resize`/`view`/`choice(s)`，单选 enter 提交光标项、多选空格 toggle + enter 提交集合；up/down/left/right/k/j 导航、home/end/pgup/pgdown 翻页、esc/ctrl+c 取消、禁用项跳过、offset 跟随、视图按窗口 1/4 自适应高度、行宽 ANSI 截断）。接入：member 编辑器 status/proxy/agent 与 pool provider 字段由行内循环 picker 迁移为弹窗；Role 现支持自由文本动态编辑，保存后清理成员上下文并在下次绑定时按新 Role 重建 system prompt；legacy provider 值作为列表首项 "legacy: X" 标记、确认写回原值；picker 激活期宿主层独占键，字母/粘贴惰性不泄漏）；`memberEditState` 保留 role 输入缓冲，`poolState` 删 provSel（struct-state 净减）。
> ✅ P6.5.1 弹窗布局修复（2026-08-25，tui-researcher-claude）：`option_list.view` 三处几何修正——(1) 内容行由 `padColumn`（至少补 1 空格语义）改为按 `width-4` 精确 pad，右边界框不再左移 1 列；(2) `start` clamp 到 `[0, len-rows]`，窗口缩小滚动后变大不再顶部空白 + 底部假 "no options" 填充；(3) 帮助行补齐到 width（截断后仍整宽）。新增 `option_list_layout_test.go` 7 个几何断言（边框逐行对齐、窗口增长满行、空态、一行窗口光标可见、多选标记不撑宽、窄窗口帮助行、滚动窗口连续真实行）。
> ✅ P0 Ctrl+T 持久退出（2026-08-25，tui-researcher-claude）：`exitTeam()` 对齐 x 键——补 `WriteSelection(Team only)`（清空持久化 selection）+ `teamSuppressAutoSession=true`（下次 [TEAM] 点击停 ModeTeams 管理页，space 才进成员管理，t 显式恢复 auto-session）；Ctrl+T 优先级不变（`handleTeamKey` 最前拦截，先于一切状态 owner 与 composer）。`restoreSession()` 兑现持久化读侧：`onTeamButtonClick` 在未抑制时读 `ReadSelection`，MemberID 在册且 `IsLeader()` 才恢复该成员窗口，否则（absent/非 leader/stale）回落 `firstLeader`——会话入口的 leader 门控与 t 键镜像；`p.sessions == nil` 防护。`openLeaderSession` 重构为 `openSession(initial)`。
> ✅ TUI leader 入口与排序（2026-08-25，tui-researcher-claude）：进入路径自动纠正——t 键从非 leader 成员按 `firstLeader`（registry 文档序）自动选择现存 leader 进入 session，`FocusMember(leader)` 同步 roster 高亮，不再拒绝；仅团队无 leader 才拒绝。新增 `teamPicker.refusal` 轻量拒绝横幅（区别于 errMsg 错误页：refusal 是管理页上方的横幅行，列表保持可见可操作，errMsg 渲染替代管理页），[TEAM] 点击（未 suspend）与 t 键两条路径无 leader 时写入，文案保持 "Only the leader can start a team session"；reload（含 l 分配 leader 成功）自动清除；Ctrl+T suspend 的团队 [TEAM] 点击静默停管理页（用户偏好优先）。成员列表 leader 稳定置顶：`tui.Model.sortedMembers` 排序键改为 leader > stateRank > ID（SliceStable）——leader 恒在一切非 leader 之前（无论 state），多 leader 间沿用 state→ID tie-break；`FocusMember(id)` 按身份聚焦（Reload 已按 ID 找回 focus，排序变化不漂移选择态）。
>
> ✅ TUI leader 入口与排序回归测试（2026-08-25，test-engineer-claude；只改测试与文档，未触碰实现）：`internal/cli/chat_tui_team_leader_entry_test.go` 6 用例——普通成员 t 自动选 leader 进入（session 绑定 lead、无拒绝）；多 leader 取 registry 文档序第一个（lead1）；无 leader t 拒绝不进入；[TEAM] 点击无 leader 拒绝但管理页列表保持可见（refusal 横幅语义，非 errMsg 错误页）；suspend 团队 [TEAM] 点击静默停管理页不报错；roster 打开首焦点为置顶 leader（focusMember helper 按 ID 定位，不依赖排序方向）。`internal/team/tui/model_leader_pin_test.go` 6 用例——leader 恒在非 leader 前（状态优先级失效）；多 leader 按 state→ID；同 state leader 按 ID tie-break；非 leader 子序不重排；FocusMember 按身份聚焦/未知 id 不漂移；Reload 排序变化焦点按 ID 找回。验证：cli 全量 exit=0（含 tui-researcher 更新的 6 个既有契约测试）+ tui 全量 9 用例绿 + repolint clean 1269 baselined 零新增（未用 -update）。
>
> ✅ 持久化与重启恢复（P6.2，2026-08-24，plugin-engineer-claude）：`board_bindings` 表（§1.3 schema）+ `BindingPersister`（`SaveBinding`/`LoadBindings`，`blackboard_binding_sqlite.go`）；`BindingRegistry` 增 `NewBindingRegistryWithPersister`/`Restore`，Bind/Unbind 先持久化后提交内存（失败不残留半绑定态）；**服务端重启不递增 generation**——`Restore` 原样恢复持久化记录，存活窗口不受重启影响；unbound 记录同样持久化，跨重启幂等 replay 成立；transitioning 不落库（随进程消亡）。kill/reopen 单测：`blackboard_persist_test.go`（绑定/游标/事件同库跨重启恢复，`TestCursorSurvivesReopen` 验证重启后只渲染新事件不重放）。

### 4.1 稳定身份与 generation

`member_id` 在团队内稳定，和 agent 类型、终端进程、leader 关系解耦。窗口启动、换号或重新拉起时，服务端递增 `generation`，旧窗口进入 `DRAINING`。

```go
type BindRecord struct {
    MemberID   string
    LeaderID   string
    Generation uint64
    Status     string // unbound|bound|transitioning
    TaskID     string
    BoundAt    time.Time
}
```

### 4.2 状态机

```text
unbound --Bind(task_brief)--> bound
bound   --Unbind(handoff)--> unbound
任一状态 --并发操作--> transitioning --成功--> 目标状态
                                      --5s 超时--> 回滚原状态
```

不变量：`bound` 必须对应恰好一个有效 session；TUI 的 `teamSessionBound()`、overlay 和后端绑定走同一生命周期路径。

### 4.3 绑定、解绑和重绑

绑定只导入 `task_brief` 摘要和 artifact 指针，不复制私有 transcript。解绑必须先写 checkpoint/handoff；写失败则保持 `bound`，禁止半解绑。

成员崩溃后以新 generation 自动重绑，使用原 `task_id` 幂等重放。leader 崩溃时成员保留私有上下文，新 leader 依据 `BindRecord + checkpoint` 接管。旧 generation 的命令、回报和 cursor 默认拒绝。

```go
type Handoff struct {
    TaskID        string
    Digest        string
    ArtifactRefs  []ArtifactRef
    Pending       string
}
```

TUI 接缝：Bind 事件进入绑定态；Unbind 统一走 `exitTeam()`；generation 变化清空 seen-set 并重新拉 L0/L2；事件乱序时以服务端 `BindRecord` 为准校准，而不是本地猜测。x（退出全部）只关 session 窗口并抑制下次自动进入，不动 leader 身份与绑定记录；leader 下位只经 k（`stepDownLeader`）。

### 4.4 绑定验收

- Bind/Unbind 同 task 重放结果一致。
- 旧 generation 的写入被拒绝，新 generation 可继续。
- 双 leader 并发 Bind 只有一个成功。
- 解绑失败不留下半绑定态。
- 解绑/重绑 10 次，私有上下文大小不增长，overlay/picker/session 无泄漏。
- kill/rebind 后 task_id、checkpoint 和 artifact 指针仍可恢复。

### 4.5 TEAM session 思考态成员切换（实现中，2026-08-25，tui-researcher-claude）

> 目标（leader 分配）：runtimeSwitchBusy 不再因 Running 阻止切换；被拦截的
> PendingPrompt/审批/chooser 状态按 member 保存、切回该成员时恢复；Running 成员
> 后台事件 unread/transcript 语义不变；禁止破坏已有 TEAM leader 选择/置顶行为。
>
> 根因（integration-tester 分析）：`switchTeamMember`（chat_tui_team_switch.go:80）
> 的 `runtimeSwitchBusy()` 门（chat_tui.go:466-472：`Running || PendingPrompt ||
> BackgroundJobs>0 || pendingApproval!=nil || chooser!=nil`）在思考态（controller
> `c.running` 覆盖 spawnGuardedTurn→finishGuardedTurn 全程）拒绝切换；门理由
> （「swapping m.ctrl under a running turn 事件错位」）在 §11.5 单 pump + member-tag
> 分流下不成立——`handleMemberEvent` 按 `boundMember()` 分流：非绑定成员
> TurnDone/Message→unread badge、流式 delta 丢弃；backend 不重建（registry 复用），
> turn 后台继续，`History()` 持续 append，切回 `bindBackend` 从 History 快照重建
> transcript。
>
> 实现（最终方案，architecture 审核修正后）：
> - **窄门独立**：共享 `runtimeSwitchBusy()` **保留全门不动**（model.go:35 /
>   effort.go:44 / skill_hooks.go:128,196 / chat_tui_web.go:31 / runtime_rebuild.go:40,158
>   与 rebindMemberAgentUser 共用——那些是真实重建/拆毁，Running 仍拒）。
>   `switchTeamMember` 单独用新窄门 `memberSwitchBusy()`：`status.PendingPrompt ||
>   m.pendingApproval != nil || m.chooser != nil`——Running/BackgroundJobs 放开（后台
>   job 不阻塞事件流，TurnDone/Message 有 unread 兜底）。PendingPrompt 保留是竞态
>   保险：approval 注册（backend goroutine）与 TUI ingest（Update）之间有时序窗口，
>   用户按键可能先到（m.pendingApproval==nil 但 backend.PendingPrompt==true）。
> - **恢复走 backend 层重放（非 TUI per-member 缓存）**：`switchTeamMember` bind 成功
>   后调 `backend.ReplayPendingPrompts()`（control.SessionAPI 内嵌 Approvals 端口，
>   controller.go:2595，SSE 交接同款链路）——重放**当前仍阻塞**的 ApprovalRequest/
>   AskRequest 事件经 member backend 自己的 sink（共享 memberEvents 泵）流出，后续
>   Update 的 handleMemberEvent 归因（此时已 bound，switchTeamMember 先设
>   session.current 再 bindBackend，时序无竞态）→ ingest → 恢复 m.pendingApproval/
>   m.chooser。无挂起则无事件，幂等；ApprovalTimeout 超时后无事件可重放，天然无
>   stale。esc 出 session（不经窄门）时 bindBackend 清显示状态无妨——backend 层
>   approvalManager 队列仍挂着，切回重放补上。handleMemberEvent/closeSession/
>   bindBackend 零改动（unbound ApprovalRequest/AskRequest 照旧不 ingest、不 badge，
>   切回重放正好补上被丢弃的 prompt 事件）。
> - **后台事件语义不变**：TurnDone/Message unread badge 与流式丢弃逻辑零改动。
> - errMsg 文案随窄门更新为 "Answer the pending approval before switching member"
>   （窄门拒绝仅剩真实模态）。
> - 测试（test-engineer 并行，chat_tui_team_switch_busy_test.go，基于 replays 计数
>   断言新契约）：Running 中切换成功（直接 + ctrl+down 键路径）、BackgroundJobs 放行、
>   Running 后台 TurnDone unread badge + 流式不 badge + 切回清 badge、切回 History
>   重建、PendingPrompt/pendingApproval/chooser 三拒绝 + 保持绑定、esc 下卡片清除 +
>   切回 replays+1、idle 重放无卡片、unbound Approval/Ask 不 ingest 不 badge 不渲染、
>   rebind Running 仍拒（宽门）。stubBackend 加 `replays *int` 计数 +
>   `ReplayPendingPrompts()` 最小实现（switch_test.go）；可注入 RuntimeStatus。
>   实现侧适配：TestEscUnderApprovalClearsCardReplayRestores 改走 handleMemberEvent
>   不经完整 Update 渲染（stub 不实现状态行子端口）。
> - 门禁：cli 全量 PASS + -race 子集 PASS + repolint clean（零新增，不用 -update）
>   + vet/build/gofmt。
>
> 测试收口（2026-08-25，test-engineer-claude；只改测试与文档，未触碰实现）：
> `internal/cli/chat_tui_team_switch_busy_test.go` 13 用例全绿——Running 切换成功
> （直接 switchTeamMember + ctrl+down 键路径）、BackgroundJobs 放行、Running 成员切走后
> TurnDone→unread badge/流式不 badge/切回清 badge、切回 History 完整重建（对方上下文
> 不残留）、PendingPrompt/pendingApproval/chooser 三拒绝且保持绑定、esc 下卡片清出窗口
> + 切回 replays+1 + 重放事件恢复卡片、idle 重放无卡片（幂等）、unbound Approval/Ask
> 不 ingest 不 badge 不渲染、rebind Running 仍拒（memberRebuildBusy 宽门）。
> 验证：cli 全量 exit=0（16.8s，含既有共享门契约测试复绿）、13/13 -race 绿、tui 9/9、
> repolint clean（1269 baselined 零新增，未用 -update）、gofmt/vet clean。
> stubBackend 扩展（switch_test.go，与 tui-researcher 共建）：可注入 RuntimeStatus
> + `replays *int` 计数 + `ReplayPendingPrompts()` 最小实现。
>
> 收口终验（2026-08-25，integration-tester-claude，只读）：五个验收点独立复验全绿。
> ①门：`switchTeamMember` 用窄门 `memberSwitchBusy()`（chat_tui_team_switch.go:80，
> 仅 `PendingPrompt || pendingApproval!=nil || chooser!=nil`），Running/BackgroundJobs
> 放开；共享 `runtimeSwitchBusy()` 恢复全门（diff 净 0），model/effort/skill/web 与
> rebind（`memberRebuildBusy` = 全门 + Running/BackgroundJobs）仍拒——
> `TestRuntimeSwitchesRejectRunningBackgroundJobs` 6 子项全 PASS。②恢复：
> `backend.ReplayPendingPrompts()` 于 bind 成功后调用（switch.go:113，SSE 同款链路），
> 重放仍阻塞的 Approval/Ask 事件经共享泵归因已 bound 成员，恢复卡片；无 TUI 缓存，
> idle 重放无卡片（幂等，TestPendingInteractionNotRestoredWhenStale 语义）。③后台语义
> 零改动：TurnDone→unread badge、流式不 badge、切回清 badge、切回 History 重建无残留、
> unbound Approval/Ask 不 ingest 不 badge 不渲染。④TEAM leader 选择/置顶未破坏：
> entry 6 + gate 2 + tui leader_pin 6 全 PASS。⑤覆盖：busy 测试 13 用例全 PASS。
> 独立验证：cli 全量 11.8s ok、team/tui ok、vet/build/gofmt clean、
> repolint clean（1269 baselined 零新增，未用 -update）。
> 遗留（非本任务，待 leader）：unbound 成员后台卡审批无 badge 提示，用户切回才见。
>
> P0 架构审核（2026-08-25，architecture-analyst-claude，只读；对象：指纹化缓存
> team_backends.go + rebindTeamBackend 落地实现）：
> - **A（P0，阻断）rebind 失败路径窗口死**：`rebindTeamBackend` 目前 release（Close 旧）
>   先于 bind（组装新）。组装失败（错 key/错 provider 是 rebind 常见失败）→ 旧 backend
>   已 Close、m.ctrl 死绑 dead backend，窗口不可用。违反代码库既有约定：modelSwitchMsg
>   失败语义是「Build failed — the kept controller still serves」（chat_tui.go:1935-1938，
>   预组装 + 成功后才换绑、旧 controller 延迟 Close）。修复：bind 内原子替换——指纹
>   失效时先组装新 backend，成功后才 retire 旧并替换 live/fps；组装失败保留旧 backend
>   返回 err。bind 的 fpErr 分支（AgentUser 瞬时读取失败）同修。
> - **B（P0，阻断）指纹失效 retire 绕过 Running 保护**：bind 的指纹失效路径由
>   switchTeamMember（窄门 memberSwitchBusy，不挡 Running）触发。场景：A 在 Running，
>   用户切 B、改 pool、切回 A → bind(A) 指纹不符 → retire(A) Close 正在跑的 backend →
>   静默杀 turn，违反 §4.5「切换不中断 turn」。修复：指纹失效时读旧 backend
>   RuntimeStatus（Running/BackgroundJobs）——忙则保守复用旧 backend（下轮空闲再重建），
>   闲才 retire 重建。（evictOverCap 同类缺口属既有行为，不在本次 P0，另记。）
> - **C（P1）指纹维度遗漏**：指纹只 hash pool 条目身份（ref/provider/baseURL/model/
>   APIKey），baked 的 b.Role（memberSystemPromptIdentity）、b.Proxy（memberProxySpec）、
>   u.Effort（DefaultEffort）变更不触发重建 → 角色/代理/effort 编辑后 backend stale。
>   指纹函数签名是 func(team.MemberBinding)，应并入 b.Role + b.Proxy + user.Effort。
> - **D（确认通过）**：指纹 hash APIKey 不落明文（fps 内存 map 存 hash）；fps 随 retire
>   清理（team_backends.go:156）；rebind 成功路径立即切换 m.ctrl/m.modelRef（bindBackend
>   全套 + ReplayPendingPrompts）；宽门保护 rebind（Running/PendingPrompt/BackgroundJobs/
>   pendingApproval/chooser 全挡，无审批迁移丢失窗口——rebind 时旧 backend 无挂起）；
>   BindAgentUser→bind 同 Update 循环同步执行无输入竞态；pool 编辑路径被指纹校验覆盖
>   （下次 bind 重建；「保存后立即重建」属 P1 体验优化）。
> - **测试建议（转 test-engineer）**：① rebind 组装失败 → 窗口仍 bound 旧 backend 且可
>   继续 Submit（当前实现失败，预期暴露 A）；② Running 成员指纹失效切回 → backend 不被
>   Close、turn 不中断（暴露 B）；③ 指纹含 Role/Effort 变更 → 重建（暴露 C）；④ 指纹命中
>   → 复用（fps 生效）；⑤ rebind 成功 → modelRef 立即切换 + ReplayPendingPrompts 调用。
>
> P0 落地复核（2026-08-25，cli-researcher-claude；对象：审核 A/B/C 三阻断项）：
> - **A（原子重建）已落地**：`rebindTeamBackend` 不再 release-then-bind，改为直接 bind——绑定
>   ref 变更必然触发指纹失效，bind 内**先组装新 backend，成功后才 retire 旧**并替换 live/fps；
>   组装失败保留旧 backend 继续服务并返回 err（errMsg 可见），窗口永不死绑。对齐既有
>   modelSwitchMsg 失败语义（chat_tui.go:1935「Build failed — the kept controller still
>   serves」）。测试 `TestRebindMemberAgentUserFailedBuildKeepsServing`：失败后 closed=0、
>   bound 仍 true、成员仍绑定、错误可见。
> - **B（忙态保护）已落地**：bind 指纹失效路径读旧 backend RuntimeStatus——Running/
>   PendingPrompt/BackgroundJobs 任一忙态或 fpErr 不可解析 → 保守复用旧 backend（touch+return，
>   不 retire 不重建），空闲才 retire+重建。测试 `TestBindKeepsRunningBackendOnFingerprintChange`
>   （Running 保留 + idle 后重建）+ `TestBindKeepsPendingPromptBackendOnFingerprintChange`
>   （PendingPrompt 保留）。
> - **C（指纹维度）已落地**：`memberAgentUserFingerprint` 并入 user.Effort；`newMemberBackendFingerprint`
>   追加 b.Role + b.Proxy（proxyFingerprint 编码 enabled+address）——角色/代理/effort 编辑后
>   指纹变化触发重建，backend 不再 stale。测试 `TestMemberAgentUserFingerprintSensitiveToIdentity`
>   （provider/baseURL/model/apiKey/Effort 逐项敏感、key 不落明文）+ `TestMemberBackendFingerprintResolvesFromPool`
>   （ref→pool 解析 + dangling ref err）。
> - 测试建议 ①-⑤ 全部落地：①rebind 失败保留窗口 ✓ ②Running 切回不杀 turn ✓ ③Role/Effort
>   变更重建 ✓ ④指纹命中复用 ✓（`TestTeamBackendsFingerprintKeepsReuse`）⑤rebind 成功 modelRef
>   立即切换 + ReplayPendingPrompts ✓（`TestRebindMemberAgentUserUpdatesModelRef` 断言 modelRef
>   pool-a/gpt-5.6 → pool-b/deepseek-v4，rebindTeamBackend 保留 ReplayPendingPrompts）。
> 门禁：cli 全量 13.4s ok、gofmt/vet/repolint clean（零新增，未用 -update）；未触碰全局默认
> 模型语义（config 层零改动）。

> P0 阻断回归测试收口（2026-08-25，test-engineer-claude；只改测试与文档，未触碰实现）：
> 新增 `internal/cli/chat_tui_team_model_rebind_test.go`，6 用例对齐 A/B/C 落地语义：
> - **openai 直连路由**（`TestMemberProviderResolverRoutesOpenAI`）：Provider="openai" 无 baseURL →
>   kind "openai" @ https://api.openai.com；有 baseURL → 路由 entry 端点；model 进 ref（u1/gpt-5.6）
>   与 descriptor/dial 配置（补 TestMemberProviderResolverServesThePoolEntry 未覆盖的 openai 直连）。
> - **指纹真实链路**（`TestBindRebuildsOnPoolEntryChange`）：newMemberBackendFingerprint（真实 pool
>   lookup）+ teamBackends.bind 端到端——entry 不变复用（builds=1）、Model 变更 retire 旧 + 重建
>   （builds=2、closed=1）、重建后复用。
> - **A 失败路径**（`TestBindKeepsOldBackendWhenRebuildFails`）：指纹失效 + 组装失败 → 返回 err、
>   旧 backend 继续服务（bound 仍 true、closed=0）；修复后重建成功 retire 旧（closed=1）。
> - **B 忙态三态**（`TestBindKeepsBusyBackendOnFingerprintChange`）：Running / PendingPrompt /
>   BackgroundJobs 任一忙态指纹变化 → 保守复用（builds=1、closed=0），table 覆盖实现者单态测试
>   （TestBindKeepsRunningBackendOnFingerprintChange / TestBindKeepsPendingPromptBackendOnFingerprintChange）
>   未覆盖的 BackgroundJobs。
> - **C 指纹维度**（`TestFingerprintSensitiveToRoleProxyEffort`）：Effort 变更（memberAgentUserFingerprint
>   含 Effort）与 Role/Proxy 变更（newMemberBackendFingerprint 追加 role+proxyFingerprint）均改指纹。
> - **rebind 成功路径**（`TestRebindMemberAgentUserUpdatesModelRef`）：真实 store + 真实 fingerprint +
>   modelRef 跟随 entry（pool-a/gpt-5.6 → pool-b/deepseek-v4）+ replay 断言（bind replays=1、
>   rebind 后 replays=2），与 cli-researcher ⑤ 复核互相印证。
> 验证：6/6 用例 -race 全绿；cli 全量 exit=0（12.4s）；gofmt/vet/repolint clean（1269 baselined
> 零新增，未用 -update）。协作说明：cli-researcher P0 落地期间曾出现实现/测试中间态（指纹失效
> 语义演进：error 保守复用、忙态保留、Effort 并入指纹、rebind 原子重建），最终实现与测试一致全绿；
> 曾误判的间歇 FAIL（TestRebindMemberAgentUserRebuildsAndRebindsImmediately 单跑绿/全量红）系实现者
> 改名+改断言中间态，实现稳定后全量 exit=0。
>
> 实现协同确认（2026-08-25，tui-researcher-claude）：A/B/C 的实现主体为
> `team_backends.go` bind 原子替换（指纹失效/组装失败保留旧 backend、busy 或 fpErr
> 保守复用，切换时 `RuntimeStatus` 忙态判定含 Running/PendingPrompt/BackgroundJobs）
> + `team_backend_build.go` 指纹补全（user.Effort、b.Role、b.Proxy/proxyFingerprint），
> 与 cli-researcher 的 `rebindTeamBackend`（bind→bindBackend→ReplayPendingPrompts，
> 宽门保护，失败保留窗口）协同；test-engineer 测试收口为权威版本（表驱动
> `TestBindKeepsBusyBackendOnFingerprintChange` 覆盖 busy 三态、
> `TestFingerprintSensitiveToRoleProxyEffort` 覆盖 C 维度、`TestBindKeepsOldBackendWhenRebuildFails`
> 覆盖失败保留），重复用例已去重；并行编辑冲突收口（import 清理、注释限长、
> `TestMemberBackendFingerprintResolvesFromPool` 断言改 HasPrefix——指纹现含 role/proxy 后缀）。
> 终验：cli 全量 12.3s PASS、cli -race 39.5s PASS、control/team/boot PASS、
> repolint clean（1269 baselined 零新增，未用 -update）。

> **收口终验（2026-08-25，integration-tester-claude，只读）**：P0/P1 收口后独立复验全绿。
> - **原子重建（A）**：`rebindTeamBackend` 无 release-then-bind，bind 内先组装新 backend 成功才
>   retire 旧并替换 live/fps；组装失败保留旧 backend 服务并返回 err（对齐 modelSwitchMsg 失败
>   语义），窗口永不死绑——`TestRebindMemberAgentUserFailedBuildKeepsServing` /
>   `TestBindKeepsOldBackendWhenRebuildFails` 锚定。
> - **忙态保护（B）**：bind 指纹失效路径读旧 backend `RuntimeStatus`，Running/PendingPrompt/
>   BackgroundJobs 或 fpErr → 保守复用不重建不杀 turn，空闲才 retire+重建——表驱动
>   `TestBindKeepsBusyBackendOnFingerprintChange`（busy 三态）+ `TestBindKeepsRunningBackendOnFingerprintChange` /
>   `TestBindKeepsPendingPromptBackendOnFingerprintChange`。
> - **指纹维度（C）**：Effort/Role/Proxy 并入指纹（key 仅 hash 不落明文）——
>   `TestFingerprintSensitiveToRoleProxyEffort`。
> - **错误提示不混淆**：全局默认 DeepSeek 缺 key 走 boot notice（boot.go:535-536），Detail 指向
>   entry.APIKeyEnv（DEEPSEEK_API_KEY），`TestBuildNoticesMissingAPIKey` 参数化 keyEnv 锚定提示
>   不含其他 env；团队 OpenAI key 经 `memberProviderResolver` 直传 `provider.New`（合成 entry
>   零值、`RequiresAPIKey()=false`，不触发全局 key 提示、不误报 DEEPSEEK_API_KEY），
>   `TestMemberProviderResolverRoutesOpenAI` 锚定独立路由；keyless fallback 语义未被破坏
>   （`TestBuildKeylessDefaultFallsBackToConfiguredProvider` / `TestBuildExplicitKeylessModelStillFails`）。
> 独立门禁：cli 全量多次 ok（12.1–18.0s，曾 1 次瞬时 FAIL 复跑未复现、磁盘无变化，系协作期
> 并行编辑中间态）、cli -race 40.5s ok、team 全家 ok、vet/build/gofmt clean、
> repolint clean（1269 baselined 零新增，未用 -update）。
> 遗留：boot.go:417 新会话 keyless fallback 仍无 notice（静默切换无可观测信号，全局会话侧
> 产品缺口，超本轮团队侧范围，待 leader 拍板）；工作树改动未提交。

> **P1 落地：pool 组合校验 + 成员缺 key 来源明确（2026-08-25，cli-researcher-claude）**：
> - **provider/model 组合校验**（`agentuser_validate.go` `validateProviderModel`）：entry 解析到
>   anthropic 路由（provider=anthropic，或 deepseek 官方端点/含 /anthropic 的 base_url）时拒绝
>   gpt 前缀模型——首次请求必以认证形状错误失败，读起来像 key 问题；OpenAI 路由（openai
>   provider，或 deepseek + OpenAI 兼容 base_url）保持开放，自定义 OpenAI 模型（gpt-5.6-luna 等）
>   合法。空 provider/model 是表单中间态不拒；legacy provider 由 allow-legacy 门放行后不二次
>   拒绝（消费时判定）。落在 ValidateAgentUser/validateAgentUserAllowLegacy 共用字段校验尾部，
>   AddAgentUser/UpdateAgentUser 都过。测试 `TestValidateAgentUserRefusesGptOnAnthropicRoute`
>   （3 拒 6 放矩阵，含自定义 OpenAI 端点）+ `TestValidateAgentUserAllowLegacySkipsModelCombo`。
>   既有 pool picker 测试（anthropic/deepseek 提交 gpt-5 的 fixture）按新语义改 model 为合法值。
> - **成员缺 key 来源明确**（`team_backend_build.go` `memberCredentialError`）：成员装配预检——
>   AgentUser 无 APIKey 且无 SecretRef 时返回 `member %q: agent user %q has no API key
>   configured`，错误带成员与 entry 身份，直接进 session errMsg（member unavailable: ...），
>   与 integration-tester 收口终验互补：那边锚定"成员 key 正常时不误报 ambient"，这边锚定
>   "成员确实缺 key 时报错点名来源"，绝不读成 ambient 的 DEEPSEEK_API_KEY 提示；SecretRef 非空
>   视为已声明凭证来源不误报。测试 `TestMemberCredentialErrorNamesSource` +
>   `TestMemberBuilderMissingKeyFailsBeforeAssembly`（装配前失败，不触发 boot 重路径）。
> 门禁：cli 全量 12.2s ok、team 全量 ok、gofmt/vet/build clean、repolint clean（1269 baselined
> 零新增，未用 -update）；全局默认模型语义零改动（config/boot 层未触碰）。

> P1 校验测试收口（2026-08-25，test-engineer-claude；只改测试与文档）：leader 5 项场景
> 覆盖核对——①合法 openai/gpt ②非法 deepseek/gpt ③非法 anthropic/gpt ④合法自定义 openai
> 模型：实现者 `TestValidateAgentUserRefusesGptOnAnthropicRoute` 的 3 拒 6 放矩阵已全部覆盖
> （au-1/au-3 拒、au-4/au-5 放，含 deepseek+OpenAI 兼容 base_url 变体），我初写的
> `agentuser_model_compat_test.go` 与其语义完全重复，已删除避免双份维护；⑤团队 key 错误提示
> 来源：与实现者 `memberCredentialError`（装配预检点名成员/entry 来源，SecretRef 非空不误报）
> 互补，新增 `internal/boot/team_resolver_key_source_test.go`
> （`TestMemberResolverEntryNeverTriggersGlobalKeyNotice`）：成员 resolver 合成 entry 零
> api_key_env、`RequiresAPIKey()=false` → boot.go:535 全局缺 key notice 永不因成员条目触发、
> detail 不误报 DEEPSEEK_API_KEY；对照组（官方 host + api_key_env 配置条目）仍要求 key，
> 证明差异来自 resolver seam 而非 RequiresAPIKey 失效。
> 验证：boot targeted+race PASS、team 全量 2.5s ok、cli 全量 12.2s ok、gofmt/vet clean、
> repolint clean（1269 baselined 零新增，未用 -update）。纯测试新增（1 文件），无实现改动。

## 5. Leader/成员指令与回报管道

> 实现状态（2026-08-24，cli-researcher-claude）：服务端 bus 落地于 MCP server（`/home/zwc/mult_agent_mcp`）。
> - `common/durable_bus.py`：envelope 字段级校验、ACK 状态机（received/accepted/rejected/executed，幂等不重执行）、cancel（queued 终态 / in-flight best-effort 标记）、kind=command per-member ≤5 pending 背压、ack 游标单调不倒退、after_seq 过滤（legacy 无 seq 跳过）、>2KB 指针注入文本。
> - `mult_agent_mcp.py` 接线：outbox 条目 envelope 字段（env_id/task_id/reply_to/severity/digest/generation/ttl）、>2KB 载荷落盘 `members/<name>/inbox/<id>.md` + 只注入指针行、新工具 `member_bus_ack`/`leader_bus_cancel`/`leader_bus_ack_report`、`member_report_result(reply_to=)` 回报闭环（指令 closed(executed)）、`member_read_shared(after_seq=)` 增量游标（缺省=最近 10 条兼容）、results.jsonl 全写路径（member 回报/leader 完成/复活事件）seq 服务端单调盖章。
> - 验收：`tests/test_pipeline_durable_bus.py` 16 用例全绿；既有 outbox 回归（test_ack_batch_outbox / test_task1_batch_ack_granularity）不受影响。
> - P7 MCP 接线（2026-08-24，cli-researcher-claude）：新增 `common/blackboard_client.py` 薄 client（一次性 stdio JSON 调用 Go `reasonix-blackboard` CLI，op=append/read-after/read-view/bind/cursor；业务拒绝编码进响应 JSON，非零退出仅服务失败，超时/缺二进制 → BridgeError）。`member_report_result` 主路径走 bridge：report_id→client_msg_id（Go UNIQUE 幂等，重放返回原事件不产生新 seq）、seq 由 Go store 事务内分配、identity（member/role/agent/generation）由 Python 服务端从团队数据解析盖章、results.jsonl 双写（带 board_seq/client_msg_id，seq=Go seq）；`member_read_shared(after_seq>0)` 走 bridge read-after（SQLite 事实源，返回文本附 next_seq 供游标推进；空页 NextSeq=0 不倒退），缺省"最近 10 条"保持 JSONL 路径（SQLite 无 last-N 语义，全量快照读归 P8 切读）；桥不可用（二进制缺失/超时/非零退出）自动降级纯 JSONL（Python seq 盖章兜底，提示可见）——回报绝不因桥丢失；Python bus ACK/retry/backpressure 原样保留未动。
> - P2.5 durable inbox / controller bridge（2026-08-25，cli-researcher-claude）：新增 `common/controller_bridge.py`——`POST /inbox/items` → control.EnqueueInbox → sessioninbox.Store（input=指针行+digest、幂等键=env_id、409/413→BridgeRejectedError、连接/超时/非 JSON→BridgeUnavailableError；`REASONIX_SERVE_URL` 配置，未配置=桥不可用走降级）。投递链统一：指令事实**总是**落盘 durable inbox `members/<name>/inbox/<env_id>.md`（≤2KB 也写，机器字段只存在于文件），bridge 可用时经它进入 member Controller（controller 自动 RunInboxTurn 消费，不再 send-keys 注入第二份文本），tmux 仅降级并盖章 `delivery_channel=go-bridge|tmux-fallback`（降级原因 `bridge_error` 可观测）；默认环境（未配置 REASONIX_SERVE_URL）行为零变化。watermark envelope：per-member 指令流服务端盖章单调 +1（跨 kind 共享、dup/拒绝不消耗序号）、`acked_watermark` 回报/ACK 确认点单调不倒退。接线：`leader_assign_subtask` 改走 outbox（kind=command，task_id/generation/watermark 盖章 + 立即 flush 保持同步语义，busy 显式拒绝，恢复由投递链 `_recover_and_send` 兜底）、`member_send_message` 走 outbox（kind=message 不受 command 背压；leader 目标保留原 `_send_context_to_member` 确认路径）、`member_report_result` 回报推进成员 acked_watermark（确认到流头）、`_notify_leader_of_report` 收敛为纯唤醒信号（正文已落盘不搬运，digest≤120 + artifact 指针）。离线/重复/过期幂等沿用既有冷却/pending_reports/leader_activate 闭环。测试：`tests/test_pipeline_inbox_bridge.py` 16 用例（watermark 盖章/文件落盘/bridge 双路径/assignment·send_message 接线/acked_watermark 单调/唤醒信号收敛）+ 既有套件回归 199 passed 7 skipped；顺带修复 P7 遗留双 start mock 泄漏（`test_seq_stamped` 对已 start mock 重复 start 导致 `_send_keys` 替换层未撤销，跨文件污染 24 个测试）。
> - P0 leader_checkpoint 显式清空（2026-08-25，cli-researcher-claude）：`leader_checkpoint_set` 参数默认改为 None（未提供=不改动该字段），显式空字符串=清空该字段（goal/deadline→""，boundaries/decisions/plan/dependencies/remaining/next_actions→[]）——总任务完成后的合法收口 = `leader_checkpoint_set(goal="", remaining="", next_actions="")`：goal 清空后不再与 leader_last_task 构成 HIGH drift，drift gate 放行分配/广播（此前 goal 一经设置永不可清，任务收口必然触发"checkpoint.goal 已记录但 leader_last_task 为空"硬门）。旧调用（省略字段）行为不变（None 默认等价旧 "" 忽略）；清空也是写入，epoch 单调 +1；旧 epoch CAS 拒绝语义不变。测试：`tests/test_leader_checkpoint.py` 新增 4 用例（显式清空字段/未提供不改动/清空 goal 后 drift gate 放行/set-assign-report epoch 单调），38 用例全绿；相关回归 228 passed（mult_agent_mcp + ack_batch + report 套件 + checkpoint 套件）。
> - 收口复核 report→leader loop 唤醒接线（2026-08-25，cli-researcher-claude）：`_record_report_and_notify_leader` 链路核对——S1 原子完成标记（完成标记+pending append 同锁）、S2 幂等（last_report_key + report_id 双层跳过，重复回报不增行不倒退 acked_watermark）、S3 delivered（注入成功 mark_pending_reports_delivered，未 ACK 报告不再被巡检重放）、成员回报推进 acked_watermark 到流头；`_notify_leader_of_report` 门控核对——tmux leader 存活+空闲+冷却通过才注入纯唤醒信号（digest≤120+artifact 指针，正文不搬运），冷却内被跳过回报先入 pending 不丢，dead 窗口不注入（revival 闭环独立），direct leader 不注入（pending 持久化 + leader_activate drain 消费）；`_retry_deferred_report_injection` 巡检兜底补投。幂等闭环测试覆盖：`test_leader_wakeup_injection.py`（c1-c5 冷却节流/ts 异常放行、b1-b6 巡检补投不双投、v/k 门控、d 系列 codex 确认提交）+ `test_report_reliable_delivery.py`（activate drain 不误清并发、离线重启存活、幂等/原子/可见性）+ `test_pipeline_inbox_bridge.py` WakeupSignalTests（信号不含正文、离线持久化幂等）。**修复 P7 隔离缺口**：`test_leader_wakeup_injection.py` fixture 未覆盖 `_BLACKBOARD_BIN/_BLACKBOARD_DB/_SERVE_URL` 与 `REASONIX_BOARD_BIN/DB/REASONIX_SERVE_URL` env——端到端用例（test_c4 等走 `_record_report_and_notify_leader`）受宿主 PATH 影响：blackboard 二进制缺失 → P7 降级 note 污染 write_error 断言（跨环境 flaky，2 用例失败）。修复对齐 P7 套件模式：确定性 `_BLACKBOARD_BIN="blackboard-not-installed"` + env pop + old_globals/old_env 恢复；test_c4 断言语义对齐 P7（write_error 现承载"JSONL 写失败（真错误）"与"blackboard 降级提示（预期内，JSONL 双写兜底）"两类，精确化为 JSONL 失败为空 + 降级提示可见）。验证：唤醒/report/桥四套件 92 passed 全绿。

### 5.1 两条 durable 通道

- 指令：leader -> member，outbox 持久化，成员离线时排队。
- 回报：member -> leader，写入结果日志/黑板后可确认，leader 离线不阻塞成员回报。

> 生产者单一性决策（2026-08-25，plugin-engineer-claude）：成员指令的**唯一生产者**是 Python
> 控制面（sessioninbox 链：outbox→POST /inbox/items→controller RunInboxTurn；tmux send-keys
> 仅降级并盖章 `delivery_channel=go-bridge|tmux-fallback`）。board EventCommand 链
> （`EventCommand` kind + `BoardInbox.Fetch`）是**预留 seam**：消费端就位、生产端刻意不接线——
> 全仓生产代码零 `Append(kind=command)` 调用点（blackboard.go 定义 + inbox.go 消费仅两处），
> 测试内写入均为合成（agentruntime/chat_tui_team_inbox 测试直接 append 模拟 leader 指令）。
> 激活门槛：出现 Go 宿主 leader 且 sessioninbox 链已收口/迁移；在此之前任何实现不得激活
> 第二条活跃命令链（integration「命令链单一性」门：无同一事件双消费）。

```go
type Envelope struct {
    Version    uint16
    Kind       string // command|report|ack|cancel|ping
    ID         string
    TaskID     string
    ReplyTo    string
    From       string // 服务端盖章
    To         string
    Generation uint64
    Severity   string // info|normal|destructive
    TTL        time.Duration
    Inline     string
    PayloadRef string
    Digest     string
}
```

`Inline` 上限 2 KB；大载荷写 `inbox/<id>.md` 或 artifact 并只注入指针。机器字段不通过 send-keys 文本传递。

### 5.2 ACK 状态机

```text
queued -> delivering -> received -> accepted -> completed -> closed
                     \-> failed -> retry(backoff) -> escalate
                     \-> canceled -> closed
```

`received` 只表示到达，`accepted` 表示成员接受，`completed` 必须由回报回指 `reply_to`。同 `id` 重试只重发 ACK，不重复执行。取消保证 queued 不再投递；in-flight 取消是 best-effort，最终必须回报 `canceled` 或 `completed`。

### 5.3 背压、离线和兼容

- 每成员 outbox 有界（默认 5 条 pending）；满时返回 `busy`，leader 合并或等待，不丢弃旧指令。
- 终端死亡时 outbox 保留，指数退避重试（30s、5min、最多 3 次）后 escalate。
- leader 离线时回报继续落盘；`leader_activate` 读取 pending reports 并推进 ack cursor。
- 保留 `leader_assign_task_to_relevant`、`member_report_result`、`member_send_message` 的外部签名；内部统一映射到 envelope。

### 5.4 管道验收

- kill 成员终端后同 command 只执行一次，恢复后 ACK 可补发。
- leader 离线期间回报不丢，恢复后 cursor 单调前进。
- destructive 指令无二次确认不执行。
- queued cancel 不投递，in-flight 返回 canceled。
- 所有 report 的 `reply_to` 都能解析到 command，关联率 100%。

## 6. 共享/私有隔离、安全和迁移

### 6.1 Namespace

```text
<state_root>/blackboard/shared/       # 事件、摘要、证据索引
<state_root>/blackboard/private/<m>/  # 成员私有 notes/drafts/cursor
<state_root>/blackboard/inbox/<m>/    # leader 指令载荷
<state_root>/blackboard/outbox/<m>/   # 待提交/待确认消息
<state_root>/blackboard/meta/         # bind、generation、全局 cursor
```

共享文件由服务端持有并以 0640 暴露；私有文件 0600。所有读写禁止 `..` 穿越和符号链接逃逸，路径由服务端解析，客户端不能传权限位。

### 6.2 ACL 与身份盖章

身份 `{member_id, role, agent, generation, session_id}` 从当前窗口绑定解析，工具参数中的同名字段全部拒绝或忽略并审计。文本中的 `[member=...]` 只是显示标签，不是安全边界。

共享区允许成员提交自己的摘要，leader 只读；私有区只允许属主读写；跨成员读取统一返回 403 且不泄露目标是否存在。删除和归档只允许 leader/服务端管理通道。

### 6.3 三层脱敏

1. 落盘前：API key、token、cookie、私钥、认证头等 deny-list 脱敏。
2. 注入前：再次扫描，必要时只发送 artifact 指针。
3. 摘要前：摘要器不得还原被替换内容；命中写审计记录。

误报风险通过白名单和人工审计处理，不能关闭默认脱敏。

### 6.4 旧数据迁移

`results.jsonl` 采用导入、双写、切读、归档四步。旧身份标记 `unverified`，须在首次成员回报或 leader 复核时确认；旧数据不自动获得当前 generation。迁移工具必须支持 dry-run、digest 比对和失败回滚。

### 6.5 隔离验收

- 伪造 member/role/generation 被拒。
- 私有文件不出共享列表，跨成员读取不泄露存在性。
- 共享事件身份字段覆盖率 100%。
- secrets 在落盘、注入、摘要三个边界均被脱敏。
- 双写窗口不一致时阻断切读和归档。

> 实现状态（2026-08-24）：服务层隔离与脱敏落地于 `internal/team/blackboard_security.go`
> （`ValidBoardID`/`CheckBoardAccess`/`RequireManagement`/`Stamp`/`Redactor`；默认 deny-list
> 四类模式，落盘/注入/摘要三层边界复用同一 `Redact` 调用，标记不再被后续模式命中）；
> 旧数据迁移校验落地于 `internal/team/blackboard_migration.go`（`ParseLegacyLine`/
> `PlanMigration`/`VerifyDualWrite`，四步迁移门，不一致返回 `ErrMigrationMismatch` 阻断
> 切读与归档）。单元测试 `blackboard_security_unit_test.go`/`blackboard_migration_test.go`
> 全绿；store 级集成测试在 `blackboard_security_test.go`（并行成员）。
> 归档缺口已修复：`ArchiveBefore` 接口加 `Identity` 参数并接入 `RequireManagement`
> （管理通道门控，route §6.2），`TestBlackboardNonOwnerArchiveDenied` 转绿。

## 7. 上下文组装合同

成员每轮上下文固定为：

```text
系统规则
-> 私有 task context
-> 当前 generation 的 command inbox
-> 相关 task 的 L0/L1/L2 board view
-> 当前用户输入
```

leader 每轮只组装：当前团队 L0、待处理 report、阻塞和需要决策的证据。完整成员 transcript 永不自动复制到 leader。

共享事件必须带结构化身份、`task_id` 和 `source_seq`；显示层可加身份标签帮助阅读，但标签不能覆盖系统规则，也不能改变 ACL 或 generation 过滤。

> P2.5/P3/P4 cli 接线（2026-08-25，tui-researcher-claude）：chat TUI 的 durable
> command chain 落地（`internal/cli/chat_tui_team_inbox.go`）：
> - `teamInboxWire`：overlay 打开时经 `openTeamInbox` 打开 board store
>   （`.reasonix/team/board.db`，WAL 多进程安全）；缺失/不可读 → nil，团队 UI 零依赖
>   （优雅降级，绝不阻塞）。
> - member 侧 turn-tail 注入（§7 顺序：inbox → 用户输入）：`injectTeamTurn` 挂在
>   提交路径唯一闭包（`startTurnWithRaw` 的 SendWithRaw 与 slash 的 SubmitDisplay 两处），
>   bound member 时 `BoardInbox.Fetch`（watermark + generation）→ 注入块（格式与
>   `agentruntime.InjectTask` 的 inbox link 一致）→ write-before-commit `Ack`（失败留
>   watermark 下轮重放）；每轮上限 8 条；generation 从服务端持久化 `LoadBindings` 的
>   BindRecord 取——无绑定成员不注入（服务端窗口是门，旧窗口不越权 drain）；
>   composer 文本/事件流/unread 零触碰。
> - leader wakeup 消费（§5.1）：`consumeWakeups` 用 leader 自身的 board cursor 增量读
>   `EventWakeup`，`[TEAM]` 打开时以 notice 呈现；无游标 leader 首次静默建位（历史不重放）。
> - 生命周期：board 随 overlay（`exitTeam` 关闭；重复打开先关旧板）。
> - 测试 `internal/cli/chat_tui_team_inbox_test.go`：注入+ack 幂等、stale generation
>   跳过、无绑定直通、wakeup 只出现一次、提交路径端到端（capture backend 断言
>   最终边界：模型输入带注入、raw/display 仍是用户文本）。
> - 门禁：全量 cli 11.7s PASS、-race 子集 PASS、vet/build/gofmt/repolint clean
>   （1269 baselined 零新增，未用 -update）。

## 8. 实施分期与回滚

| 阶段 | 交付 | 回滚点 |
|---|---|---|
| P1 | schema_version/kind/seq/digest/after_seq，JSONL 兼容读；checkpoint-consistent JSONL 快照导出（2026-08-25，§1.4） | 保留旧 `results.jsonl` 读路径 |
| P2 | SQLite WAL、幂等、CAS、事务 cursor、ACL | 双写但继续从 JSONL 切读 |
| P3 | L0-L3、checkpoint、budget、cache epoch | 视图失败回退旧摘要 |
| P4 | binding generation、durable bus、ACK/cancel | 保留旧 send/report 适配器 |
| P5 | 归档、redaction、迁移切读、完整门禁 | 双写窗口回退并阻断归档 |
| P6 | 薄 blackboard CLI（subprocess JSON 契约，按需调用） | Python 直连或 CLI 双通道，互不影响 |

> P6.3 实现状态（2026-08-24，tui-researcher-claude）：持久化加载接线（`NewBindingRegistryWithPersister`/`Restore`，§4）与 TUI 校准接缝（`internal/cli/chat_tui_team_binding_calibration.go` + 6 用例）已就位，`go test ./internal/cli/` 绿。已闭环（2026-08-24，architecture-analyst-claude）：`cmd/reasonix-blackboard/main.go` 改用 `NewBindingRegistryWithPersister(store)` + 启动 `Restore(LoadBindings(...))`——每个 subprocess 先恢复上次进程的绑定再服务请求，bind/rebind/unbind 跨进程持久化（跨进程测试 `TestBlackboardBindPersistsAcrossProcesses` 绿）。

任何阶段都不得删除旧事实源，直到下一阶段完成对账和恢复演练。

> P5 实现状态（2026-08-24）：redaction（`Redactor` 三层边界）与迁移校验
> （`PlanMigration`/`VerifyDualWrite` 双写门）已落地且单测全绿；归档 ACL 已修复
> （`ArchiveBefore` 接入 `RequireManagement`，管理通道门控）。

> P6.1 实现状态（2026-08-24）：
> - 文件：`cmd/reasonix-blackboard/`（main.go 入口 + protocol.go 转译层）、`internal/team/blackboard_cursor.go`（`GetCursor` 读接口 + `SQLiteCursorStore` 持久化半环）。
> - 协议：stdin JSON 请求 → stdout JSON 响应；op = append / read-after / read-view / bind / cursor；业务拒绝编码为 `{"ok":false,"error":{"kind","message","detail"}}`（conflict 带 current_epoch）；进程非零退出仅当请求无法处理（坏 JSON/未知 op/DB 打不开）。
> - 测试：`cmd/reasonix-blackboard/protocol_test.go` + `blackboard_cross_process_test.go`（`go test ./cmd/reasonix-blackboard/`，含 P8 跨进程：kill 重开、双写门、游标跨进程续读、stale-generation 拒绝）。
> - 偏差：bind/rebind/unbind 经 `board_bindings` 表跨 subprocess 持久化（`NewBindingRegistryWithPersister` + 启动 `Restore(LoadBindings)`，见 P6.3 状态）——每次调用的新进程先恢复上次进程的绑定再服务请求；CLI 透传 stamped 身份并执行 `CheckBoardAccess` 服务层 ACL + `CheckGeneration` 服务层 generation 门控（绑定的最大 generation 与事件历史最大 generation 取 max，stale 写入拒绝，未绑定无历史者放行）——所有写入经 CLI 时该门控单点生效，Python subprocess 调用自动继承。

## 9. 验收矩阵

| 类别 | 最低验收 |
|---|---|
| 并发 | 10×100 写入无撕裂；seq 单调；CAS 单赢；cursor 不倒退 |
| 幂等 | 重复 event/command/report 只产生一次业务副作用 |
| 恢复 | kill -9、WAL 重开、leader/member 离线后均可续跑 |
| generation | 旧窗口写入/回报/游标被拒，新窗口正常 |
| 迁移 | JSONL 导入行数、digest、task 关联率 100%；双写不一致阻断 |
| token | L0/L1/L2 有界；增量与历史解耦；checkpoint 前后语义等价 |
| 安全 | 伪造身份、跨私有区访问、路径穿越、secret 泄漏全部拒绝/脱敏 |
| 生命周期 | bind/unbind/rebind 10 次无上下文复制、session、picker 或订阅泄漏 |
| 管道 | ACK 状态机、重试、取消、背压、指令回指和离线恢复全覆盖 |

测试落点：`internal/team/blackboard` 单元测试、SQLite effect 测试、`internal/cli` TUI 接缝测试、跨进程 integration 测试；并行测试使用 deterministic seed，`-race` 覆盖 store、cursor、inbox/outbox 和 generation 门控。

### 9.1 验收测试状态（P1-P3 实现后）

已落地测试文件（`internal/team/`，包内测试，全部通过 `-race`）：

| 文件 | 覆盖类别 |
|---|---|
| `blackboard_sqlite_test.go`（实现成员） | 并发无撕裂、分页、幂等重放、CAS 单赢+epoch、游标隔离/单调、WAL 恢复、私有 ACL、归档 resync、supersede 审计链、批回滚 |
| `blackboard_binding_test.go`（实现成员） | bind/unbind 状态机、generation 门控、handoff、transition 回滚、并发单赢 |
| `blackboard_view_test.go`、`context_view_test.go`（实现成员） | L0/L1/L2 预算、增量比例、checkpoint 等价、并发消费 |
| `blackboard_persist_test.go`（P6.2，实现成员） | kill/reopen：绑定跨重启原样恢复（generation 不递增）、unbound 持久化+幂等 replay、persist 失败不残留半绑定态、游标跨重启续读不重放、generation 失配重置首次读、无行=首次读、倒退镜像容忍 |
| `blackboard_export_test.go`（P1 导出器，实现成员，2026-08-25） | 导出 golden 字节级固定（legacy 别名+全事件字段形状）、重复导出逐字节相同、并发 append 导出无撕裂（行数=某个中间态、无重复 seq、每行完整 JSON）、导出中途失败后 reopen 全量一致、SinceSeq/归档过滤与 Archived 计数 |
| `blackboard_concurrency_test.go`（验收） | 读写并发隔离（无撕裂/无幻影）、同 msg_id 并发单赢、多 consumer 游标独立、同 cursor 并发单调（`ErrCursorBackwards` 语义）、supersede×append 并发审计链完整 |
| `blackboard_idempotency_test.go`（验收） | 批重放同 seq、重放不改结论、supersede 重放不双链 |
| `blackboard_token_test.go`（验收） | digest 恒有、分页增量无重复无遗漏（120 条 limit 10）、游标推进后不重放已见 |
| `blackboard_generation_test.go`（验收） | cursor 旧 gen 拒绝+高 gen 接管、盖章身份持久化不变 |
| `blackboard_security_test.go`（验收） | 私有板跨成员读写拒（fail-closed 不泄存在性）、非 owner supersede 拒、非 owner archive 拒、共享板空身份拒、`PrivateBoard` 前缀分类边界 |
| `blackboard_cli_contract_test.go`、`blackboard_cursor_after_test.go`（验收，P8） | CLI JSON 契约：错误 kind 全映射（forbidden/invalid-request/not-found/cursor-not-found）、CAS conflict detail（current_epoch/current_seq）、cursor-backwards、全字段往返（refs/supersedes/conclusion/identity）、report_id（=client_msg_id）幂等重放同 seq/digest/event_id、created_at 空=now UTC、每 op 恰一响应负载；after_seq 增量页不重放、边界值（负/超大）、cursor 跨 reopen 持久 |
| `blackboard_digest_test.go`（验收，P8） | 跨实现 digest 字节契约：**BoardEvent 无 json tag → digest 输入为 Go 字段名（PascalCase），非 wire snake_case**；Python 等价重算一致（python3 缺失 skip）、任一 client 字段变更 digest 变化 |
| `chat_tui_team_test.go`（P6.4，tui-researcher） | l assign 门控（无 leader 分配 / 已有 leader 拒绝显示持有人 / 自身幂等）、role/Leader 只读（渲染只读行、enter 打开首个可编辑字段、s 不写 role/leader）、x 退出全部（session 关 + selection 清 + 下次 [TEAM] 停管理页 + t 恢复自动进入 + leader 标记保留） |
| `option_list_test.go`（P6.5，tui-researcher 基底 + test-engineer 增量） | 组件：单选光标/提交/取消、多选 toggle 幂等与集合序、禁用跳过（含全禁用/端部）、home/end/pgup/pgdown 与 offset 跟随、视图高度自适应（空态/行数/截断）、滚轮、高度预算；接入（integration 侧 option_list_integration_test.go）：取消零写盘、宿主层键路由惰性不泄漏 composer、member/pool 字段弹窗、粘贴惰性 |
| `option_list_layout_test.go`（P6.5.1，tui-researcher，2026-08-25） | 弹窗几何：每行恰为请求宽度（边框/内容/帮助行对齐）、窗口增长后无空白头与假 "no options" 尾（offset clamp）、空态几何、一行窗口光标恒可见、多选 ✓ 标记不撑宽、窄窗口帮助行截断、滚动窗口内容为连续真实行 |
| `chat_tui_team_ctrlt_test.go`（P0，test-engineer，2026-08-25） | Ctrl+T 持久退出：7 个测试（-race 全绿），各深度/状态 exit 语义 |
| `chat_tui_team_inbox_test.go`（P2.5/P3/P4，tui-researcher，2026-08-25） | durable command chain：bound member 注入+ack 幂等、stale generation 跳过、无绑定直通、wakeup 只出现一次、提交路径端到端（capture backend 断言模型输入带注入、raw/display 不变） |
| `chat_tui_team_exit_test.go` 增量 `TestTeamExitDropsSelectionAndParksNextClick`（P0，tui-researcher） | Ctrl+T 清空持久化 selection + 设 suppress → 下次 [TEAM] 落 ModeTeams 管理页，t 显式恢复 auto-session |
| `chat_tui_team_session_test.go` 增量 `TestTeamRestoreResumesPersistedLeaderSelection`（P0，tui-researcher） | restoreSession 门控：absent/非 leader/stale selection 回落 firstLeader，在册 leader 恢复其窗口 |
| `chat_tui_team_leader_entry_test.go`（2026-08-25，test-engineer） | leader-first 入口回归：普通成员 t 自动选 leader、多 leader 取文档序第一个、无 leader t 拒绝、无 leader [TEAM] 点击 refusal 横幅、suspend 静默边界、roster 首焦点 leader |
| `internal/team/tui/model_leader_pin_test.go`（2026-08-25，test-engineer） | leader 置顶排序：leader 优先于 state、多 leader state→ID、同 state ID tie-break、leader 插入不重排非 leader 子序、FocusMember 按身份聚焦、Reload 焦点保持 |

验收回归结果（最终状态，2026-08-24 复核）：`go test ./internal/team/ -count=1 -race` 全绿。

最终门禁证据（2026-08-24，实现侧复核一致）：

- `go test ./internal/team/...` PASS
- `go test -race ./internal/team/...` PASS（P1 导出器 2026-08-25 复核一致：`blackboard_export_test.go` 5 用例 + `TestCLIExportSnapshot` 全绿）
- `go test ./internal/cli/` PASS（P6.4 TUI 接线全量，含 l assign 门控 / role·Leader 只读 / x 退出全部）
- `go test ./internal/cli/` PASS（P6.5 组件全量，2026-08-25：optionList 组件测试 + member/pool picker 迁移回归 + -race 相关子集；repolint clean 1271 baselined 未用 -update）
- `go test ./internal/cli/` PASS（P6.5.1 布局修复，2026-08-25：view 行 pad 到 width-4 使边框对齐、start clamp 到 [0, len-rows] 消除窗口增长后的顶部空白与假 "no options" 尾、帮助行补齐到 width；新增 option_list_layout_test.go 7 个几何断言；全量 9s + vet/build/repolint clean）
- `go test ./internal/cli/` PASS（P0 Ctrl+T 持久退出，2026-08-25：exitTeam 对齐 x 补 selection 清空 + suppress；restoreSession 读侧 + leader 门控；新增 TestTeamExitDropsSelectionAndParksNextClick / TestTeamRestoreResumesPersistedLeaderSelection，TestTeamReentryStartsFreshOnLeader 改为「exit 后落管理页 + t fresh on leader」语义；test-engineer chat_tui_team_ctrlt_test.go 7 测试 -race 全绿；全量 9.5s + -race 子集 + vet/build + repolint clean 1271 baselined 未用 -update）
- `go test ./internal/cli/` PASS（P2.5/P3/P4 durable command chain 接线，2026-08-25：teamInboxWire 打开 board + bound member turn-tail 注入 + write-before-commit ack + leader wakeup 增量消费（leader cursor）+ exitTeam 关板；提交路径 3 处挂 injectTeamTurn（SendWithRaw + 2× SubmitDisplay input）；新增 chat_tui_team_inbox_test.go 5 测试；全量 11.7s + -race 子集 + vet/build/gofmt + repolint clean 1269 baselined 零新增未用 -update）
- `go vet ./...` PASS
- `CGO_ENABLED=0 go build ./...` PASS
- 服务端 `pytest tests/test_pipeline_durable_bus.py` 16 PASS
- `go run ./tools/repolint` PASS（clean，1271 baselined findings）：生产代码 ratchet drift 已由架构侧拆分收敛归零（2026-08-24，architecture-analyst-claude，见下）；`chat_tui_test.go` 与 repo total `test-file-size` 已由测试预算收敛清零（2026-08-24，test-engineer）。新增 blackboard 文件无单文件违规。全程未用 `-update` 扩基线。

> 测试预算收敛状态（2026-08-24，test-engineer-claude）：
> ratchet drift 最小拆分（test-engineer 部分）完成：只改 `internal/cli/chat_tui_test.go` 单文件，
> 压缩 9 处 ≥4 行 restatement 型测试注释至 2 行（保留测试名 + 核心 why），净减 20 行（4398→4378，
> 目标 ≥8）；断言与测试逻辑零删除。per-file `test-file-size`（+6）与 repo total `test-file-size`
> （+6）为同一来源（该文件是当时唯一 per-file 违规文件），一次修复双门禁清零。
> 验证：`go test ./internal/cli/ -count=1 -race` PASS（33.9s）、gofmt 干净、`git diff --check` 通过、
> repolint 中 chat_tui_test 与 repo total 两行消失。未用 `-update`、未碰 baseline。
> 生产代码 drift 收敛状态（2026-08-24，architecture-analyst-claude）：
> 只改三个生产文件 + 同包非测试拆分落点，全量归零（不用 `-update`、不碰 Python bus、不碰测试文件）：
> - `internal/boot/boot.go`（file-size +10 / function-size +6 / complexity +1）：把 build() 内
>   vision 闭包（37 行）提取为包级 `resolveVisionProvider`/`selectVisionModel`，落点
>   `internal/boot/assembly_reuse.go`（103 行，无 budget 记录），一次动作三指标全过。
> - `internal/cli/chat_tui.go`（file-size +17 / function-size +2）：team overlay 接线最小收敛——
>   MouseClick 分支合并为 `teamStatusClick(msg)`（落点 chat_tui_team.go）、`handleTeamKey` 调用处
>   去冗余外层 if（内部已判 teamPick）、View 两处 MouseMode 分支统一为 `overlayMouseMode()`
>   （落点 chat_tui_team_render.go）、字段/注释压缩、行尾文档注释移除。
> - `internal/cli/shell_completion.go`（function-size +1）：`permissionMode` 定义两行合一。
> 验证：`go run ./tools/repolint` clean；`go test ./internal/boot/` PASS（9.6s）；
> `go test ./internal/cli/` PASS（8.7s）；`go vet`/`go build ./...`/gofmt 干净。
> 语义等价确认：`overlayMouseMode()` 在 themeSweep 渲染路径中 teamOverlayModal() 恒 false，
> 与原分支等价；`handleTeamKey` 空 overlay 时返回 consumed=false，与原外层 if 等价。
> 只读门禁复核（2026-08-24，integration-tester-claude，独立重跑非转录）：
> - `go run ./tools/repolint` clean（1271 baselined findings，exit 0）——直接运行 + 后台轮询
>   双重确认；`baseline.json` 零 diff（未用 `-update`，基线未扩大，git diff 证实）。
> - `go vet ./...`、`CGO_ENABLED=0 go build ./...`、gofmt（4 目标文件）全 PASS。
> - `go test ./cmd/reasonix-blackboard/ -count=1 -race` PASS（P8 跨进程 5 用例不受影响）；
>   `go test ./internal/team/ -count=1 -race` PASS（blackboard 全套）。
> - test-engineer 约束复核：chat_tui_test.go +18/-38 净减 20 行（目标 ≥8），删减全部为注释
>   （restatement 型测试注释压缩），断言与测试逻辑零删除（diff 抽查）。
> - 修改统计：boot.go/chat_tui.go/chat_tui_test.go/shell_completion.go 共 4 文件，+32/-101；
>   新增 blackboard 文件无单文件违规，Python bus 未触碰。

> TEAM leader 生命周期接缝（2026-08-24，architecture-analyst-claude）：
> 新增 `internal/cli/chat_tui_team_leader_lifecycle.go`（领域/生命周期 helper，TUI 键路由
> 经此调用，不改 chat_tui_team.go）：
> - `assignFocusedLeader() error`——l 键 assign-only 门控：已有 leader（非焦点成员）拒绝并
>   指名持有者（"already has a leader %q"）；已是 leader 幂等 no-op；成功已 reload。
> - `stepDownLeader(teamName, memberID) error`——k 注销领域原语，严格顺序
>   （write-before-commit，任一步失败即中止、不写 flag）：① `stopTeamBackends`
>   （releaseTeam + liveTeamCount 残留校验，stop 失败不清上下文）→ ② `clearTeamHistories`
>   （leader 私有 session 文件 + 团队共享 context 树，trash 暂存 §6.6）→ ③
>   `SetMemberLeader(false)` → ④ WriteSelection 清空 → ⑤ reload → ⑥ closeTeamSessions。
>   含 IsLeader 重查；**黑板不触碰**（board SQLite events/bindings/cursors 与 member
>   identity 是跨成员共享数据，不在清除范围——`TestStepDownLeaderStrictOrder` 断言
>   board.db 存活）。
> - `closeTeamSessions()`——p 级纯复位（sessionState{}，回 roster 渲染）；m 级 unbind
>   （窗口还给 chat 后端）由 TUI 在 k 成功分支执行（stop 已退休全部 backend）。
> - `team_backends.go` 增 `liveTeamCount`；`chat_tui_team_reset.go` 的 `executeLeaderReset`
>   改为委托 `stepDownLeader`（k 状态机行为不变，单一真相）。
> 测试 `internal/cli/chat_tui_team_leader_lifecycle_test.go`：assign 成功/拒绝/幂等、
> step-down 成功（flag off + context 清 + session 关 + board 存活）、非 leader 拒绝、
> **clear 失败（.trash 位置放文件使 sweep 失败）→ flag 不写且 context 未动**（write-
> before-commit）、liveTeamCount 计数。验证：`go test ./internal/cli/` PASS（8.9s，
> 含既有 reset 端到端）、`go vet`/`go build ./...`/gofmt 干净、repolint clean。

> TEAM 生命周期/会话退出最终验收（2026-08-24，integration-tester-claude，独立重跑非转录）：
> - 实现审计：`chat_tui_team_leader_lifecycle.go`（`assignFocusedLeader` l 键 assign-only /
>   `stepDownLeader` 严格顺序 / `closeTeamSessions`）、`team_backends.liveTeamCount` 生存校验、
>   reset 委托单一真相、`toggleRosterLeader` 删除；`x` 键 `exitAllTeamSessions` +
>   `teamSuppressAutoSession`（退出全部会话，下次 `[TEAM]` 落管理页，t 清除恢复 auto-session）；
>   `memberEditFields` 移除 role/leader（只读行）。
> - 验收点：**k 失败不清上下文**（stop 存活校验、clear 失败 flag 不写——两个失败测试双证）；
>   **退出快捷键不清 leader**（backends 与 leader 属性不动，`TestTeamExitKeepsLeaderContext`）；
>   **黑板 member identity 保留**（stepDownLeader 不触碰 board SQLite，`TestStepDownLeaderStrictOrder`
>   断言 board.db 存活）；**TEAM 不恢复旧 session**（suppress 后 `[TEAM]` 落管理页，
>   `TestTeamReentryStartsFreshOnLeader`）。
> - 门禁（最终独立重跑）：`go run ./tools/repolint` clean（1271 baselined findings，exit 0，
>   baseline 零 diff 未用 `-update`）；`go vet ./...`、`CGO_ENABLED=0 go build ./...`、gofmt 全 PASS；
>   `go test -race ./internal/cli/`（35.3s）/ `./internal/team/`（8.2s）/
>   `./cmd/reasonix-blackboard/`（3.1s）全 PASS；旧测试适配 6 处（paste/session/write）后无红。
> - 范围确认：Python durable bus 零改动、AGENTS.md 未提交，改动全在 Go 实现/测试/路线文档内。

> TEAM runtime 阶段：真实调度执行、durable inbox 与 consumer watermark（2026-08-25，architecture-analyst-claude）：
> 从 §3.7 占位（仅记账、ErrRuntimePending）推进为真实执行——scheduler 仍是策略层，
> 执行归 `internal/team/agentruntime`（新包，实现 scheduler.Executor）：
> - **真实 scheduler**（`internal/team/scheduler/runtime.go`）：`Executor` 窄接口
>   （Start/Cancel/Resume，由 agentruntime 实现）+ `RuntimeScheduler`——`Assign` 策略 pick 后
>   **真启动**（executor.Start 失败返回 `ErrStartFailed` 且不记 ledger，绝不伪称 runtime-pending），
>   `Cancel`、`Restore`（重启恢复：持久 running/assigned 任务对成员仍在 fleet 者 Resume、
>   成员消失者标 failed）；占位 `PlaceholderScheduler` 原样保留（P6 占位语义测试不动）。
> - **agentruntime**（`internal/team/agentruntime/`）：`AgentAPI` 窄接口（Submit/SubmitUserTurn/
>   Cancel/Running/Turn/Compose/Close，宿主把 control.SessionAPI 适配进来，runtime 零 control 依赖）；
>   `Runtime` 实现 Executor——Start 注入后 SubmitUserTurn、Cancel 停后端、Resume 带 `[resumed]`
>   标记、`Complete` 是回报路径**唯一**迁移点（running→reported）；`Drain` 消费循环
>   （fetch→逐条处理→整批成功才 Ack，write-before-commit：中途失败不推进 watermark，重读重放幂等）。
> - **durable inbox + 注入适配**（inbox.go/inject.go）：`BoardInbox` 以黑板事件日志为持久指令
>   通道（kind=command，leader 直接指令；调度指派不经 inbox 由 Runtime 直推）——`Fetch`
>   从 watermark 起读、generation 不匹配事件跳过不消费（旧窗口不吞当前窗口指令）、`Ack` 推进
>   游标（stale generation 由 store 拒绝）；`InjectTask` 按 §7 链组装（task context → command
>   inbox → board view，每环节可审计 Link.Ref），`[task: <id>]`/`[command inbox] (generation <n>)`
>   结构化标签注入——文本是显示提示，ACL 边界是服务端盖章身份（§6.2）。
> - **consumer watermark**（watermark.go）：`Watermark` 接口（Load/Commit）+ `CursorWatermark`
>   包 BoardStore 的 board_cursors 行（GetCursor 无行=0 首读，AdvanceCursor 单调+generation 门控）。
> - **leader wakeup**（wakeup.go）：`WakeFunc` 信号接口（宿主注入真实投递：终端注入/bus；
>   失败不 wedge 任务收尾）+ `NewBoardWake` 工厂（wakeup 事件落黑板，无活 leader 窗口也可观察）；
>   Runtime 在 cancel/report 终态触发（`AddWakeup` 注册）。
> - **统一状态迁移**：`team.TaskStatus` 增 running/failed/canceled，`team.TransitionTask` 单一
>   迁移图（created→assigned→running→reported→archived；running→failed/canceled；
>   failed→assigned 重派；同态幂等），scheduler/runtime/回报路径共用，任何路径无法发明新迁移。
> - 测试：`scheduler/runtime_test.go`（Assign 真启动/无 executor/无匹配成员/启动失败/
>   Cancel/Restore 恢复与成员消失标 failed）、`agentruntime/agentruntime_test.go`（注入链顺序与
>   标签、Start 提交注入+黑板 running 事件、busy member 拒绝、非法状态拒绝、Cancel 停后端+
>   wakeup+条目清除、Complete 唯一迁移点+黑板 report 事件+wakeup、Resume 标记、Drain 失败不
>   推进 watermark 重放成功、inbox 跳过其他 generation、watermark 首读 0 与提交持久）、
>   `team/task_transition_test.go`（全合法边+非法边）。
> - 验证：`go build ./...` 干净、`go vet ./internal/team/...` 干净、gofmt 干净、
>   `go test ./internal/team/... ./internal/cli/` 全 PASS、`go run ./tools/repolint` clean
>   （1269 baselined，零 diff 未用 -update）。EventKind 增 command/wakeup 两 kind（append-only）。

> TEAM runtime 收口：宿主接入与任务持久化恢复（2026-08-25，architecture-analyst-claude）：
> - **宿主 seam 适配**（`internal/boot/team_runtime_host.go`，不改 internal/cli TUI）：`ControllerAsAgent`
>   把共享 `control.Controller` 适配为 `agentruntime.AgentAPI`——controller 的驾驶面
>   （Submit/SubmitUserTurn/Cancel/Running/Turn/Compose/Close）就是成员 agent 后端契约，
>   **无需包装**；编译期断言 `var _ agentruntime.AgentAPI = (*control.Controller)(nil)` 在
>   boot（唯一允许同时 import control 与 team 的 frontend 层）钉住两个表面永不漂移。
>   agentruntime 保持零 control 依赖（分层倒置，宿主适配）。
> - **durable task store**（`internal/team/taskstore.go` + `team_tasks` 表进 boardSchema，
>   与黑板同库）：`TaskStore` 接口（SaveTask 幂等 upsert / LoadTask / **LoadLiveTasks**——
>   kill/reopen 恢复集，只捞 assigned|running，终态永不再驱动）+ SQLiteStore 实现
>   （`var _ TaskStore = (*SQLiteStore)(nil)`，同库跨重启恢复与 cursor 一致）。
> - **写点收敛在 agentruntime.Runtime**（`SetTaskStore` 注入）：Start/Resume 落 running、
>   Cancel 落 canceled、Complete 落 reported——每处**先落库后副作用**（write-before-commit：
>   落库拒绝则 abort，绝无半启动 agent/半取消后端；Complete 落库失败重试经同态幂等）；
>   所有落库状态先过 `team.TransitionTask` 单一迁移图。Cancel 补显式 TransitionTask 校验。
> - **scheduler 恢复闭环**（`RuntimeScheduler.SetTaskStore`）：`Restore` 失败分支（成员消失/
>   resume 失败）经迁移图落库——running→failed；**assigned→canceled**（迁移图无
>   assigned→failed 边，非法迁移直接拒写）；best-effort（Assignment.Note 已记录失败，
>   库保持 live 待下次尝试）。宿主恢复循环：`LoadLiveTasks → scheduler.Restore →
>   成功者 executor 内落 running`，失败者不再被下次重启重驱动。
> - 测试：`team/taskstore_test.go`（Save/Load 幂等 upsert、未知 id 报错、LoadLiveTasks
>   只捞两态、**TestTaskStoreSurvivesReopen** 同库跨重启返回上进程 live 任务）、
>   `agentruntime/runtime_store_test.go`（Start/Cancel/Complete/Resume 四写点落库断言、
>   落库拒绝时 agent 零提交——store 是 gate 不是 log）、`scheduler/runtime_test.go`
>   （Restore 成员消失落 failed、assigned 消失落 canceled、库内 live 集清零）。
> - 验证：`go build ./internal/team/... ./internal/boot/` 干净（断言编译通过）、
>   `go vet`/gofmt 干净、`go test -race ./internal/team/agentruntime/ ./internal/team/scheduler/
>   ./internal/team/` 全 PASS、`go run ./tools/repolint` clean（1269 baselined，零新增）。
> - 统一性：task_id 贯穿 Task→黑板事件→task 表（EventID 复用幂等键）；generation 门控在
>   inbox/watermark 既有；所有状态迁移单一 TransitionTask。风险：scheduler 恢复失败落库
>   best-effort（store 自身故障时靠 Assignment 表达）；宿主导入 controller 池（每成员一窗
>   口）由后续 cli/desktop 接线承担，本节点只钉契约。

> 执行选项弹出列表（agent-user pool 编辑器）集成验收（2026-08-25，integration-tester-claude，独立重跑非转录）：
> - 实现审计（真实 TEAM/member/pool 路由）：`u` 键 → `enterTeamPool()`（chat_tui_team.go:533）；
>   TEAM 键路由 `handleTeamPickerKey` 以 `p.pool.active` 门转交 `handlePoolKey`（poolInputNone/
>   Delete/Edit/EditField 四态，§6.2 键域隔离，编辑态内 a/d/e 为普通字符）；`e`/`a` 经
>   `armPoolEditor` 进入字段列表（`poolEditFields` id/identity/provider/base_url/api_key/model/
>   effort），`enter` 打开字段（id 行对已发布条目不可改），provider 为 picker（`handlePoolProviderKey`，
>   legacy 值 -1 展示不覆写）；`s` 单写 `savePoolEdit`（Add/UpdateAgentUser CAS + 写后读回 §8.3），
>   拒绝定位字段（`locatePoolEditField`）；api key 按用户明文契约渲染（K2/K3 仍治理日志/回报），
>   存储 raw 不 trim；渲染 `renderPoolList/Detail/Edit` 与 `boundMembers` 成员引用展示；
>   `teamPasteTarget` 支持非 provider 行粘贴。pool 完全 cli-owned，chat_tui.go 零引用。
> - 测试覆盖（对照 pool-editor-test-plan.md 10 项全对齐）：33 个 pool 测试
>   （chat_tui_team_pool_test.go 28 + provider_test.go 5）——enter detail / e 编辑预填 /
>   字段环绕 / 单字段提交合并（其余字段保留、APIKey 不清空）/ 坏 base_url 拒绝不泄漏 /
>   esc 零写入 / 键域隔离 / 明文 key（detail+edit 双断言）/ key 原样保存 / 整条目校验定位字段。
> - 门禁（独立重跑）：gofmt 干净；`go vet ./...` PASS；`CGO_ENABLED=0 go build ./...` PASS；
>   `go run ./tools/repolint` clean（1271 baselined findings，baseline 零 diff 未用 `-update`）；
>   `go test ./internal/cli/... -count=1 -race` PASS（34.3s，含 pool 全量与团队/生命周期回归）。
> - 风险登记：`chat_tui_team_binding_calibration.go`（`calibrateTeamSessionBinding`，6 测试）
>   为 §4.3「以服务端 BindRecord 校准」的预留 seam——纯函数与测试已落地但零生产调用
>   （`openLeaderSession` 仍信任本地 leader 标记）；server 侧 `GetBind`/`LoadBindings` 查询
>   API 齐备，接线待事件源路由推进，已登记的未跟踪产物（二进制/package.json）提交前排除。

> optionList 弹窗视觉修复验收（2026-08-25，integration-tester-claude，独立重跑非转录）：
> - 实现审计（`internal/cli/option_list.go` 可复用弹窗：single/multi、offset 滚动窗口、
>   wheel、disabled 跳过、resize 幂等）——修复后 `view`：行内容区 `cell = width-4` +
>   手动 pad（`cell-visibleWidth`，弃 padColumn 至少-1-空格保证），**每行恒等于窗口宽，
>   边框四角闭合**；`start = min(offset, len-rows)` clamp，**收缩窗口无空白头部/伪造
>   "no options" 尾部**；help 截断后补全到满宽。宿主 `renderTeamPool`/`renderMemberEdit`
>   经 `optionListHeight(h)`（h/4 有界 +3）派生弹窗高度，`resize(listH-3)` 后 `view(w,listH)`；
>   宽度宿主保护 `w = max(width,10)`（两宿主一致）。
> - 测试覆盖：新增 `option_list_layout_test.go` 8 项视觉断言（边框全行对齐+角位置/
>   窗口增长无空白头与 fake tail/空态几何/单行窗口 cursor 可见/多选 ✓ 不溢出/
>   help 截断满宽/滚动窗口连续行）；既有 `option_list_test.go` 8 项行为（滚动 offset
>   跟随/pgdn-home-end/长标签截断不超宽/高度自适应收缩/空态/cursor 兜底/wheel 惰性/
>   高度预算）；`option_list_integration_test.go` 5 项宿主路由（字母惰性不泄漏 composer/
>   wheel 移动+提交落地/esc 零写入/archived 持久化/pool provider wheel）。
> - 独立复核（非转录，临时审计测试已删）：多宽度×高度扫描（w∈{10,12,15,20,40,60} ×
>   h∈{6,8,13}）每行可见宽 == 边框宽，顶/底/行三向闭合；长标签（legacy URL、中文宽
>   字符）零溢出；滚动至底 cursor 可见且唯一；行数 = min(h-3, len)+3 自适应收缩。
> - 门禁（独立重跑）：gofmt/vet/build 干净；`go run ./tools/repolint` clean（1271
>   baselined findings，baseline 零 diff 未用 `-update`）；`go test ./internal/cli/...`
>   `-race` PASS（含 optionList 16 + 集成 5 + pool/团队/生命周期回归全量）。
> - 风险登记：视觉修复在共享工作树未提交（option_list.go 09:38 落盘、layout 测试
>   09:38:29 创建于实现微调之前，终版已重跑全绿）；极小窗口（高≤5/宽<5）弹窗可超窗，
>   高度端由 optionListHeight 下界 6 与 TUI 最小窗高兜底，宽度端宿主已保护。

> P0 Ctrl+T 持久退出集成验收（2026-08-25，integration-tester-claude，独立重跑非转录）：
> - 实现审计（真实入口状态链）：**层级** ModeTeams（团队管理）→space→ModeList（成员
>   花名册）→e/enter→ModeContext（字段编辑）；sessionState 覆盖 roster（bound 时
>   composer 共享键盘）；ModeQuit 挂任意层。**Ctrl+T**（`handleTeamKey` 最前拦截，先于
>   一切 state owner，chat_tui_team_switch.go:212）→ `exitTeam()` = closeSession（unbind
>   ambient）+ quickPick(member-agent-user) 清理 + closeTeamOverlay + **WriteSelection
>   清空 + `teamSuppressAutoSession=true`**（对齐 x 语义，注释明示「x 停管理页、Ctrl+T
>   关 overlay」为唯一差异）+ teamPick=nil。**restoreSession**（`onTeamButtonClick` 未抑制
>   时调用）读 `ReadSelection`：MemberID 在册且 `IsLeader()` 才恢复其窗口，absent/非
>   leader/stale 回落 `firstLeader`，`p.sessions==nil` 防护；`openLeaderSession` 重构为
>   `openSession(initial)`，t 键门禁（leader-only）不变。**9 条入口链逐一核验**：首次
>   [TEAM] 无 selection→firstLeader；ctrl+up/down→stepSession persist；esc（导航）保留
>   selection/suppress 不动；Ctrl+T（任意深度）清 selection+suppress+关 overlay；x（管理
>   页）清 selection+suppress 留管理页；suppress 后 [TEAM] 停 ModeTeams；t 显式恢复（清
>   suppress）；k step-down 清 selection（stop→clear→flag→selection 顺序不变）；esc
>   管理页/q 确认退出与 Ctrl+T 同路径（closed→exitTeam）。
> - 测试覆盖（独立重跑全绿）：`chat_tui_team_ctrlt_test.go` 7 项（bound session 清
>   selection+suppress/重入停管理页+space 进 roster 无会话复活/roster 深度清 stale
>   selection/composer draft 丢弃+backend 恢复/二次 Ctrl+T 幂等/无 leader+sessions nil
>   不 panic/t 显式恢复开在 leader）；`chat_tui_team_exit_test.go` 增量
>   `TestTeamExitDropsSelectionAndParksNextClick`；`chat_tui_team_session_test.go` 增量
>   `TestTeamRestoreResumesPersistedLeaderSelection` 4 用例（absent→lead1/在册
>   leader→恢复其窗/非 leader→回落/stale→回落）；既有 10 屏退出+unbind+picker+广告惰性
>   保持。targeted 27 项 + 全量 `go test ./internal/cli/` 9.45s PASS。
> - 门禁（独立重跑）：gofmt/vet 干净；`CGO_ENABLED=0 go build ./...` PASS；
>   `go run ./tools/repolint` clean（1271 baselined findings，baseline 零 diff 未用
>   `-update`）；Ctrl+T/exit/restore/session 子集 `-race` PASS。
> - 风险登记：修复未提交（工作树含 exitTeam/restoreSession/openSession 与 9 个新测试，
>   提交时需含）；`teamSuppressAutoSession` 为内存标志，x/Ctrl+T 的「下次 [TEAM] 停管理
>   页」承诺跨进程重启失效（重启后 [TEAM] 仍自动进 leader 会话，selection 持久化仅服务
>   会话内重开）；无阻塞。

历史追踪（均已修复转绿）：

1. P3 视图层 4 项（`TestConcurrentConsumersRace`、`TestNoDoubleConsumption`、`TestL2OnDemand`、`TestL2BoundedAndSections`）——由 P3 节点修复，`-race` 全包转绿。
2. **P2 安全缺口（验收测试新暴露）**：`ArchiveBefore` 无 `checkAccess`——非 owner 可归档任意私有板（`TestBlackboardNonOwnerArchiveDenied` 红）。修复：接口加 `Identity` 参数并接入 `RequireManagement`（管理通道门控，route §6.2），转绿。
3. **P8 跨进程缺口（验收测试新暴露，已修复转绿）**：`TestBlackboardStaleGenerationAppendDenied` 初版红——`handleAppend` 只走 `CheckBoardAccess` 服务层 ACL，无 per-member generation 门控，且 CLI 每次进程重建 `BindingRegistry`，未接 P6.2 持久化绑定。修复：`handleAppend` 接入 `store.CheckGeneration`（绑定持久化 max generation 与事件历史 max generation 取 max，stale 写入拒绝，未绑定无历史者放行，同 `AdvanceCursor` 语义 route §4.3）；CLI 启动 `Restore(LoadBindings)` 从 `board_bindings` 表恢复持久化绑定（P6.3），本测试转绿。

管道（bus）验收：§5 服务端实现已落地（Python MCP server，`tests/test_pipeline_durable_bus.py` 16 用例：幂等/背压/cancel/重试/注入边界/seq 游标/reply_to 关联，2026-08-24）；P7 接线（2026-08-24）：`member_report_result`/`member_read_shared` 走 Go `reasonix-blackboard` CLI（SQLite 事实源，report_id→client_msg_id 幂等、Go 分配 seq、返回 next_seq、JSONL 双写 + 桥降级回退），`tests/test_pipeline_blackboard_client.py` 16 用例全绿（mock 协议单测 + 真二进制端到端：幂等/私有板 ACL/盖章持久化/重启续读/双写/降级；bus 32 用例 + outbox/回报回归 88 全绿）。Go 内核接入 bus 后补 `blackboard_bus_test.go`（msg_id 幂等注入单次消费、重试耗尽可观测、ack 游标单调、背压拒绝）。

> P8 跨进程验收状态（2026-08-24，integration-tester-claude）：
> 新增 `cmd/reasonix-blackboard/blackboard_cross_process_test.go`（TestMain 编译 CLI，
> 每用例走真实子进程边界 + SIGKILL helper；`go test ./cmd/reasonix-blackboard/ -race` 全绿）：
> - `TestBlackboardKillReopen`：SIGKILL（无 Close/无 defer sync）后 WAL 重开，已提交 3 事件不丢（§2.4）。
> - `TestBlackboardDualWriteDigestSeq`：Python 侧双写 legacy results.jsonl 行，`VerifyDualWrite`
>   门（count+digest+盖章双向覆盖）通过，seq 连续 1..3（§1.4/§6.4）。
> - `TestBlackboardCursorZeroReplay`：进程 A 推进游标后，进程 B 增量读零重放，游标行跨进程持久（§2.3/§3.4）。
> - `TestBlackboardLegacyCompat`：真实 legacy 行（含 artifact_path/compressed_context_path/report_id）
>   解析→CLI 双写→`VerifyDualWrite` 通过（§1.4）。
> - `TestBlackboardStaleGenerationAppendDenied`：转绿（历史追踪 3）——`handleAppend` 接入
>   `store.CheckGeneration`（持久化绑定 max gen vs 事件历史 max gen），CLI `Restore(LoadBindings)`
>   恢复跨进程绑定后旧 generation 窗口回报被拒。
>
> 与 P6.2 `blackboard_persist_test.go` 互补：彼为进程内 close/reopen 语义（绑定恢复/游标续读），
> 此为真实进程边界（独立 CLI 二进制 + SIGKILL + JSON 协议），覆盖 P6.1 subprocess 契约本身。

> P8 契约测试状态（2026-08-24，test-engineer-claude）：
> 新增 `cmd/reasonix-blackboard/blackboard_cli_contract_test.go`、`blackboard_cursor_after_test.go`、
> `internal/team/blackboard_digest_test.go`；`go test ./cmd/reasonix-blackboard/ ./internal/team/ -race` 全绿，vet/gofmt 干净，未触碰实现文件。
> - CLI JSON 契约：错误 kind 全映射（forbidden/invalid-request/cursor-not-found/not-found）、CAS conflict 带 detail、
>   cursor-backwards、全字段往返（refs/supersedes/conclusion/identity 读回一致）、report_id 幂等重放、created_at 空=now UTC、每 op 恰一响应负载。
> - cursor after_seq：50 事件逐页（limit 10）覆盖全量零重放；advance 后只回新事件；after_seq=-1 等价首读、超大值空页；cursor get 跨 store reopen 持久（P6.2 接线层）。
> - 跨实现 digest：**关键契约事实——BoardEvent 无 json tag，digest 输入是 Go 字段名（PascalCase）而非 wire snake_case**；
>   字节级编码（struct 字段序、全字段输出含 null、`<>&` 转义、尾随换行）由 Python 等价重算锁定一致（python3 缺失自动 skip）；
>   任一 client 字段变更 digest 变化。⚠️ Python gateway 若按 wire 格式（snake_case）重算 digest 将失配——实现侧须以 store 输出为准透传。

> P1 集成验收：SQLite/WAL 唯一事实源 + JSONL 兼容导出（2026-08-25，integration-tester-claude，独立重跑非转录）：
> - 实现审计：`ExportSnapshot`（blackboard_export.go）为单只读事务（同一 WAL 快照），行序
>   `(board_id, seq)` 主键 → 重复导出字节一致；纯读派生视图，无任何写路径——Go 侧 team 包内
>   grep 确认无第二 JSONL 写链（其余 `.jsonl` 为会话文件，与黑板无关）；`handleExport`（protocol.go
>   export op）是管理读，快照+digest+行数经 JSON gateway 返回，消费方按 digest 对账。
> - 双写分叉核对：Python 侧 results.jsonl 写为 legacy 兼容层（P8 `TestBlackboardDualWriteDigestSeq`
>   `VerifyDualWrite` 门 count+digest 双向覆盖已锁定），P1 导出器与其 digest 算法一致
>   （逐行+换行，匹配 PlanMigration）；无分叉路径。
> - 测试覆盖（独立重跑全绿）：`blackboard_export_test.go` 5 项——golden-file / 重复导出幂等
>   / 并发 append 无撕裂（单快照中间态、seq 无重复）/ kill-reopen（errWriter 中断导出 → reopen
>   后 101 事件全量、重复导出字节相同）/ SinceSeq+Archived 过滤（live-only 默认、Archived 计数）。
>   `cmd/reasonix-blackboard/` 跨进程 8 项全绿（真实子进程 + SIGKILL）：KillReopen WAL 重开不丢、
>   DualWriteDigestSeq、StaleGenerationAppendDenied、CursorZeroReplay、BindPersists、LegacyCompat、
>   AfterSeq 增量零重放。`internal/team` 导出+TaskTransition `-race` PASS。
> - 风险/未完成：导出器与 CLI 改动在共享工作树未提交；Python 侧 results.jsonl 写保留为 legacy
>   （不迁移 bus 约束，P8 门继续守护）。

> P2-P5 集成验收：durable inbox / 唤醒 / event-to-input / scheduler（2026-08-25，integration-tester-claude，独立重跑非转录）：
> - P2 inbox：`BoardInbox` 以 board 事件日志为持久存储，watermark 落 `board_cursors` 行（跨进程
>   续读由 P8 `TestBlackboardCursorZeroReplay` 覆盖）；`Drain` 为 write-before-commit（全部处理成功
>   才 Ack，中途失败重放幂等，at-least-once）；stale generation 事件**跳过不消费**（`TestBoardInboxSkipsOtherGeneration`）；
>   注入文本经 `InjectTask` §7 链（task→inbox→board 固定序），task id/generation 为结构化标签，
>   ACL 边界是 stamped identity 而非文本。
> - P3 唤醒：`WakeFunc` 失败非致命（wakeAll 忽略错误，完成不被楔住）；`boardWake` 追加 `EventWakeup`
>   事件到 board（durable——leader 离线时唤醒丢失但事件在板上，报告事实本身经 `Complete` record
>   事件落板，唤醒是通知不是投递）；stale 窗口事件由 store 层 generation 门控（P8 已验证）。
> - P4 event-to-input：`CursorWatermark` 缺行读 0（首读）、Commit=AdvanceCursor（store 层
>   `ErrStaleGeneration` 拒绝旧窗口推进，watermark 与窗口同代际）；无第二消费链——命令流仅
>   `BoardInbox` 一条（scheduler 直接推任务，不读 EventCommand；`member_read_shared` 为既有 P7
>   消费方），`TestCursorWatermarkFirstReadZero` 锁定。
> - P5 scheduler：`RuntimeScheduler.Assign/Cancel/Restore` 经 `Executor` 真实执行（agentruntime
>   `Runtime` 实现）；所有状态迁移过 `TransitionTask` 单一迁移表（Start/Resume/Cancel/Complete，
>   非法迁移拒绝 `TestRuntimeStartRefusesIllegalState`）；Restore 无 fake pending（Assigned/Running
>   任务成员在舰队→Resume，否则 failed）；Cancel 未知任务报错非静默；busy member 拒绝二次启动。
> - 契约漂移核对（无 task/generation/watermark 漂移）：task_id 贯穿 EventCommand→InboxItem→
>   InjectTask 标签→scheduler Assignment→runtime record 事件（event_id=task-{id}-{status}-{nano}
>   双关幂等键）；generation 同一把尺——store 盖章 Identity.Generation + CheckGeneration/
>   `ErrStaleGeneration`（inbox 过滤、watermark 推进、CLI handleAppend P8 测试三方一致）；watermark
>   单一（board_cursors，CursorWatermark 唯一实现）。
> - 测试覆盖（独立重跑全绿）：agentruntime 10 项（InjectTask 链序/Start 注入/busy 拒绝/非法迁移/
>   Cancel 停止+wake/Complete 回报+wake/Resume 标记/Drain 写前提交/stale 跳过/watermark 首读），
>   `-race` PASS；scheduler 9 项 + TransitionTask 2 项 `-race` PASS；team 包 P1 组 `-race` PASS。
> - 未完成项（host 接线未落地，登记待后续）：Go 侧无 `EventCommand` 生产者（Python 命令投递仍走
>   legacy durable_bus envelope——保留未迁移，不构成双通道竞态）；`RuntimeScheduler.Restore` 无
>   host 调用者；`SetInjector` 完整 §7 链（inbox+board view）未接（默认 task-only）；Python
>   blackboard_client 未消费 Go 侧命令事件。三处均为接口/实现就绪、host 接线待做，非漂移。
> - 风险：上述改动全部未提交（工作树）；scheduler 双提交幂等依赖调用方（事件层 client_msg_id
>   幂等 + 状态机非法迁移拒绝，执行层无独立 task 表）。

> P2-P5 补充验收（host 接线并行落地后复验，2026-08-25，integration-tester-claude，独立重跑）：
> - P2/P4 host 接线已落地：`internal/cli/chat_tui_team_inbox.go`（openTeamInbox/teamInboxWire/
>   injectTeamTurn/consumeWakeups——BoardInbox 按成员接入 + wakeup 单次消费），
>   `chat_tui_team_inbox_test.go` 5 项全绿（bound member 命令注入/stale generation 跳过/
>   未绑定透传/唤醒消费一次/turn 注入 at submit）；`internal/boot/team_runtime_host.go`
>   `ControllerAsAgent` compile-time 证明 `*control.Controller` 满足 AgentAPI（签名漂移在
>   装配处构建失败）。此前登记的「SetInjector 完整链未接」随之解除（TUI 侧已接 inbox+wakeup；
>   Python EventCommand 生产者仍未接）。
> - P5 task 幂等锚点已落地：`internal/team/taskstore.go`（`team_tasks` 表 task_id PRIMARY KEY，
>   SaveTask `ON CONFLICT(task_id) DO UPDATE` 幂等 upsert——重放恢复循环不可能重复任务；
>   LoadLiveTasks 只回 assigned/running 恢复集；「store 是任务真相，黑板记录是可观测性」），
>   taskstore_test.go 4 项（save/load 往返、live 集过滤、kill/reopen 恢复、幂等重写）全绿——
>   补上了 P5 之前登记的执行层 task 表缺口。
> - 复验门禁（独立重跑非转录）：`go test ./internal/team/` 全量 PASS（3.4s）+ `-race` PASS
>   （8.2s，含 taskstore/export/task_transition）；`./internal/cli/` 全量 `-race` PASS（39.5s，
>   含 inbox 5 项）；`go vet ./...` 干净；`CGO_ENABLED=0 go build ./...` PASS；repolint clean
>   （1269 baselined，未用 -update）；`cmd/reasonix-blackboard` TestCLIExport* effect 7 项
>   （empty-board 稳定 digest/直连 store 一致/两次幂等/append 中导出 checkpoint/since 增量/
>   digest 可重算/archived 排除）PASS。
> - 未完成项（更新）：Go 侧无 `EventCommand` 生产者（Python 命令投递仍走 legacy durable_bus，
>   保留未迁移）；`RuntimeScheduler.Restore` 的 host 调度循环调用未落地（TUI inbox 已接、
>   boot 适配已备）；`make lint` 本机无法运行（golangci-lint 未安装，repolint 已过）。

> 五项闭环最终门禁（2026-08-25，integration-tester-claude，leader checkpoint epoch=115 后全量独立重跑非转录）：
> - Go targeted + race：P1 export 5 项 + TestCLIExport* effect 7 项（真实 stdin→stdout seam）
>   `-race` PASS；P2/P3 agentruntime 10 项（inbox 注入/stale 跳过/Drain 写前提交/wakeup 单次/
>   runtime start/cancel/complete/resume）`-race` PASS；P4 TUI inbox/watermark 5 项 + cli 全量
>   `-race` 39.5s PASS；P5 taskstore 4 项（save/load 往返、live 集过滤、kill/reopen 恢复、幂等
>   重写）+ scheduler 9 项 + TransitionTask 2 项，team 全量 3.4s + `-race` 8.2s PASS。
> - 门禁：`go vet ./...` 干净；`CGO_ENABLED=0 go build ./...` PASS；repolint clean（1269
>   baselined，未用 -update）；`make lint` 无法运行（golangci-lint 未安装，环境缺失，repolint
>   已代跑）。
> - Python pytest pipeline（/home/zwc/mult_agent_mcp，REASONIX_BOARD_BIN 指向工作树二进制）：
>   全量 2853 passed + 3 skipped + 228 subtests；1 个顺序依赖 flaky（D8
>   `test_d8_wakeup_action_codex_confirm_fail_keeps_pending`，单独重跑 3/3 绿，mock 泄漏类）。
>   首次全量曾见 69 failed 为状态污染（前置测试 mock/环境残留），干净重跑收敛为 1 flaky——后续
>   跑全量建议 `--lf` 前清 .pytest_cache 或隔离文件。两仓边界：Python 经 reasonix-blackboard
>   JSON gateway 对接 Go 事实源（bridge 降级仅写 results.jsonl），legacy durable_bus 保留未迁移。
> - 契约终核（无漂移）：无 JSONL 双写分叉（Go 单写者 SQLite、导出为只读派生视图、Python legacy
>   写有 VerifyDualWrite 门）；无第二消费链（命令流仅 BoardInbox，scheduler 直推不走 inbox）；
>   task/generation/watermark 无漂移（task_id 贯穿 + TransitionTask 单迁移表 + taskstore
>   `ON CONFLICT(task_id)` 幂等；generation 同一把尺 store 层 CheckGeneration/ErrStaleGeneration；
>   watermark 单一 CursorWatermark 落 board_cursors）。

> 五项闭环测试矩阵（2026-08-25，test-engineer-claude；只改测试与文档，未触碰实现）：
>
> **统一 fixture**：cmd 网关层 `boardReq/genID/appendBoard/exportCLI`——task_id/generation/watermark(seq) 三件套
> 跨 append/export/cursor 共用；agentruntime 层沿用 `newTestBoard/newTestRuntime` + reopen 路径 helper。
>
> **已落地且已测**（本次新增）：
> - `cmd/reasonix-blackboard/blackboard_export_effect_test.go` 7 项（真实 stdin→stdout seam）：空板零行稳定 digest；
>   网关 export 与 store 直连 ExportSnapshot 行数+digest+字节一致（wire 不加行）；二次导出字节幂等；
>   checkpoint：导出后 append 旧快照不变、下次导出见新行；since-seq 增量（含地板语义 seq>=floor，与 cursor 同）；
>   外部对账 digest=sha256(逐行+换行) 可重算；archived 默认排除+计数、include_archived 恢复全量（archived 经 store
>   ArchiveBefore 预置——网关无 archive op，导出本身走真实 seam）。
> - `internal/team/agentruntime/agentruntime_effect_test.go` 3 项（真实 SQLite board + AgentAPI stub seam）：
>   wakeup 失败（leader offline）不 wedge 完成路径——reported 事件仍落黑板的 durable 通知；
>   **inbox 至少一次跨 reopen**：ack 批次重开后零重放、handler 失败的批次在重开 store 上恰好重放一次（三窗口链）；
>   cancel 释放成员槽位——同 member 可再 Start，不撞 ErrMemberBusy。
>
> **既有覆盖对照**（无须重复）：team 层 export 5 项（golden/kill-reopen/since+archived/并发无撕裂/幂等）、
> cmd 跨进程 8 项（KillReopen/DualWrite/StaleGeneration/CursorZeroReplay/BindPersists/LegacyCompat）、
> sessioninbox 至少一次（IdempotentEnqueue/ReceiptSurvivesAckAndReopen/RecoverOrphanedInFlight/CrashAfterBlob）、
> agentruntime 既有 10 项（Start/Cancel/Complete/Resume/Drain 写前提交/BoardInbox generation 门控/Watermark 首读零）、
> scheduler runtime 7 项（Assign/Cancel/Restore）+ task_transition 边表 2 项。
>
> **待实现节点（实现落地后补测，验收三验：kill/reopen 续跑、同指令不重执行、旧 generation 拒绝）**：
> 1. **Go Controller 自动注入**：agentruntime 有 SetInjector seam，但 `control.Controller` 侧每轮拉取
>    BoardInbox+Drain 的接线未落地（bus 侧 Python 已实现）——落地后测：注入链全序
>    （task→inbox→board view）进真实 provider 请求、Drain 后 watermark 前进恰好一次。
> 2. **report 自动唤醒 leader loop**：runtime wakeAll 已可注册（离线不阻塞已测），但 Python bus→tmux 注入
>    的真实唤醒接线未落地——落地后测：同一 reported 事件不重复唤醒（runtime 已保证一次，接线侧再锁去重）、
>    leader offline 期回报不丢（board 事件是持久通知，恢复后补读）。**【已落地，2026-08-25，
>    cli-researcher-claude，见下方收口记录】**——三验对应：S2 report_id 双层跳过（同事件不重复唤醒，重复
>    回报不增行不倒退）、S3 delivered 标记（未 ACK 报告不被巡检重放）、离线不丢（pending 持久化 +
>    leader_activate drain）；专项测试 92 passed 全绿。
> 3. **scheduler 持久化任务恢复**：RuntimeScheduler.Restore 已有，但调度层 task 持久化（崩溃后重放未完成
>    assigned/running 任务）未落地——落地后测：Restore 幂等（同任务两次恢复不双执行）、
>    member 消失回落其他成员、generation 换代后旧调度提交拒绝。

> 五项闭环门禁收口：scheduler 宿主恢复循环（2026-08-25，architecture-analyst-claude）：
> - **宿主恢复循环落地**（`internal/boot/team_runtime_host.go` 增 `RecoverTeamRuntime`）：LoadLiveTasks
>   （durable store 恢复集）→ `scheduler.Restore` → 成员在舰队者 Resume 落 running、失败者经迁移图落库
>   （running→failed / assigned→canceled）——第二次重启不再重驱动；接线内统一 `SetTaskStore`
>   （失败恢复必须 durable，否则 kill/reopen 重放失败任务）；store 读失败 abort 且 executor 零调用
>   （store 是 gate 不是 log）。恢复循环为宿主一次性入口：装配 executor 后启动时调用。
> - **状态边界核查**（无漂移）：TransitionTask 单一迁移图与 persistRestoreFailure 合法边一致
>   （assigned→canceled，迁移图无 assigned→failed 边）；LoadLiveTasks 只捞 assigned|running 与
>   Restore 分支一致；写点收敛（Start/Resume/Cancel/Complete 先落库后副作用，落库拒绝 abort）。
> - 测试：`internal/boot/team_runtime_host_test.go` 5 项（resume 恰一次/空 store 零调用/store 错误
>   上浮且零 resume/成员消失 running→failed 落库后 live 清零/成员消失 assigned→canceled 落库）——
>   后两项走真实 SQLiteStore，闭 kill/reopen 重驱动环。
> - 门禁：`go test -race ./internal/boot/ ./internal/team/scheduler/ ./internal/team/agentruntime/
>   ./internal/team/` 全 PASS；`go vet ./...`、`CGO_ENABLED=0 go build ./...`、gofmt 干净
>   （protocol.go 除外，plugin 节点收口）；repolint clean（1269 baselined，未用 -update）。
> - 未完成项（更新）：此前登记的「RuntimeScheduler.Restore 无 host 调用者」已解除（本节点接线）；
>   test-engineer 矩阵「待实现节点 3：scheduler 持久化任务恢复」随之闭环；report 自动唤醒 leader loop
>   （Python bus→tmux 真实投递）亦已由 cli 节点收口（2026-08-25，见下方收口记录）；Go 侧
>   EventCommand 生产者已收口为预留 seam（plugin 节点，见下）；改动均未提交（工作树）。

> integration 跨组件最终复验（收口门禁，2026-08-25，integration-tester-claude）：
> - **P1 两仓 seam 复验**：cmd export 网关 7 项 TestCLIExport* + TestCLIExportSnapshot + 跨进程
>   TestBlackboardKillReopen（SIGKILL kill/reopen）独立 -race 全绿（3.4s）；Python
>   test_pipeline_blackboard_client 16 项覆盖双写门/幂等/降级/游标重启；导出单只读事务只读派生，
>   无 JSONL 双写分叉。
> - **P2/P3 bridge seam 复验**：Python controller_bridge.py（REASONIX_SERVE_URL）→ POST /inbox/items
>   （internal/serve/inbox.go 端点真实存在）→ sessioninbox 幂等存储（envelope_id 幂等键）；
>   serve 4.3s + sessioninbox 0.8s 回归全绿；tmux send-keys 仅降级并盖章
>   delivery_channel=go-bridge|tmux-fallback（mult_agent_mcp.py:878），幂等不双投。
> - **命令链单一性核验**：活跃链仅 sessioninbox（Python 指令→controller RunInboxTurn）；board
>   EventCommand 无生产者（blackboard.go 定义 + BoardInbox.Fetch 消费，无 Append 调用点）——
>   预留链空置，无同一事件双消费。
> - **P4/P5 复验**：TUI inbox 5 项（-race）+ team 全包 -race（team 8.5s/agentruntime 1.5s/
>   scheduler 1.1s）+ taskstore/export/transition 定向全绿；boot 全量 9.7s 全绿（含
>   RecoverTeamRuntime 5 项恢复接线，architecture 节点产物复验通过）。
> - **门禁复验**：go vet ./... 干净；CGO_ENABLED=0 go build ./... PASS；repolint clean
>   （1269 baselined 未用 -update）；gofmt 仅 protocol.go 遗留（plugin 职责未动）。
> - **pytest**：本次收口复验全量 **2878 passed + 3 skipped + 228 subtests，0 failed**
>   （601.66s，exit 0）——D8 顺序依赖 flaky 已收敛，全绿；历史基线 2853 passed + 1 flaky
>   在 mock 泄漏修复（P7 遗留）后同步提升。全量前置清 .pytest_cache 收敛成立。
> - **遗留（非阻塞）**：① protocol.go gofmt 已由 plugin 收口（见下节）；② make lint 环境缺
>   golangci-lint；③ Go Controller 自动注入（SetInjector 完整 §7 链）已由 architecture 收口
>   （见下节），report 自动唤醒 leader loop（Python bus→tmux 真实投递）已由 cli 节点收口
>   （2026-08-25，见下方收口记录）；
>   ④ D8 flaky 已收敛（本次全量 0 failed，全量前置清 .pytest_cache 成立）。

> 收口：EventCommand 生产者评估 + protocol.go gofmt（2026-08-25，plugin-engineer-claude）：
> - **决策（文档收口，不实现 Go 生产者）**：成员指令的唯一生产者为 Python 控制面
>   （sessioninbox 链 + tmux 降级盖章），Go 侧**不**新增 EventCommand 生产者。理由：
>   ① 无 Go 宿主 leader——Go 宿主（cli TUI/desktop/bot/acp）均为成员面，只消费命令
>   （RunInboxTurn 执行 Python 入队的成员回合）；生产命令是 leader 侧职责，现役 leader 是
>   Python tmux 会话；② 接线 Go 生产者即再造第二条活跃投递链，破坏 integration「命令链
>   单一性」门（无同一事件双消费），并放大 generation 竞态面与幂等键维护成本；③ board
>   EventCommand 链是预留 seam（§5.1 决策块）：消费端（BoardInbox）与测试内合成写入已就位，
>   生产端刻意空缺，供未来 Go 宿主 leader 启用（激活门槛见 §5.1）。
> - **证据**：grep 复核全仓——生产代码零 `Append(kind=command)` 调用点（EventCommand 仅
>   blackboard.go:39 定义 + inbox.go:71 消费），EventCommand 写入全部在测试内合成
>   （agentruntime 3 处 + chat_tui_team_inbox 2 处直接 append 模拟）；Python 控制面指令走
>   controller_bridge→POST /inbox/items（sessioninbox 链），board 链无任何外部生产者。
>   此前登记的「Go 侧 EventCommand 生产者仍无（plugin 节点核查中）」定性为**设计如此**
>   （预留 seam），与 P2-P5 验收「预留链空置」、integration「命令链单一性核验」一致。
> - **protocol.go gofmt**：wireSnapshot 字段对齐 + response 字面量对齐修复（gofmt -d 归零），
>   关闭 integration 遗留 ①（plugin 认领项）；该文件同时含 P1 export op（仅格式修复，
>   零行为变更）。
> - **门禁**：`gofmt -l` 全仓无输出；`go vet ./...` 干净；`go test ./internal/team/
>   ./internal/team/agentruntime/ ./internal/team/scheduler/ ./cmd/reasonix-blackboard/`
>   PASS + `-race` PASS；repolint clean（1269 baselined，未用 -update）。
> - 未变：sessioninbox 活跃链、tmux 降级盖章、BoardInbox 消费语义均零改动（本节点仅
>   文档 + 格式收口）。

> 收口：Go Controller 自动注入——SetInjector 完整 §7 链宿主接线（2026-08-25，architecture-analyst-claude）：
> - **宿主接线落地**（`internal/boot/team_runtime_host.go` 增 `WireTeamInjector`）：boot 宿主一次性
>   入口，`rt.SetInjector(...)` 安装完整 §7 链——按 task.AssignedMember 拉持久化绑定
>   （LoadBindings 取 generation，未绑定降级 task-only）、Fetch 未读命令、ReadView 物化结论作
>   board view，经 `InjectTask` 组装 task→inbox→board 固定序；命令仅在注入成功后 ack（写前
>   提交：ack 失败下轮重放，与 cli 侧 teamInboxWire.inject 同一语义）；board 失败降级 partial
>   链，不阻塞回合。
> - **seam 上下文修复**（`internal/team/agentruntime/runtime.go`）：`Start/Resume` 此前在
>   `r.inject(task)` 之后才设置 `task.AssignedMember`，注入器拿到空成员查不到绑定——赋值提前到
>   注入之前，注入上下文与落库/提交共享同一任务视图（默认 task-only 链零行为变化，agentruntime
>   既有 10 项测试零破坏）。
> - 测试：`internal/boot/team_injector_test.go` 3 项走真实 SQLiteStore（链全序 task→inbox→board
>   进 stub 成员提交、ack 恰好一次且成员 cursor 落 cmd seq、未绑定 task-only 降级）。
> - 门禁：`go test -race ./internal/boot/ ./internal/team/agentruntime/` PASS（49.7s/1.4s）；
>   `./internal/team/ ./internal/team/scheduler/ ./internal/cli/` 全绿；`go vet ./...`、
>   `CGO_ENABLED=0 go build ./...`、gofmt 全仓干净（含 protocol.go，plugin 节点已落地）；
>   repolint clean（1269 baselined，未用 -update）。
> - 未完成项（更新）：此前登记的「Go Controller 自动注入（SetInjector 完整 §7 链）」**解除**；
>   report 自动唤醒 leader loop（Python bus→tmux 真实投递）亦已由 cli 节点收口（见下方收口记录）；
>   改动均未提交（工作树）。

> 收口终验：跨组件最终复验（2026-08-25，integration-tester-claude）——所有收口节点产物独立复验通过，跨组件无漂移：
> - **SetInjector 宿主接线（architecture 节点）**：`WireTeamInjector` 3 项 effect 测试
>   （ChainOrder/AckOnce/UnboundTaskOnly，真 SQLiteStore + stub 成员提交路径）+ `RecoverTeamRuntime`
>   5 项全部 `-race` 全绿；与 cli 侧 teamInboxWire 语义逐点一致（Fetch→Ack→注入，Ack 失败留
>   watermark 下轮重放；board 故障降级 partial 链；未绑定 task-only）——at-least-once 一致。
>   boot 全量 10.6s + cli 全量 13.0s PASS。
> - **EventCommand 生产者决策（plugin 节点）**：复核全仓生产代码零 `Append(kind=command)`
>   （仅 blackboard.go:39 定义 + inbox.go:71 消费），Python 控制面（sessioninbox 链）唯一生产者
>   成立，board 链预留 seam 设计成立——命令链单一性门保持，无同一事件双消费。
> - **report→leader loop（cli 节点）**：`_notify_leader_of_report` 纯唤醒信号 + pending 幂等闭环
>   复核通过；测试隔离修复（test_leader_wakeup_injection.py fixture 对齐 P7）47 passed 全绿；
>   seam 套件复跑 blackboard_client 16 + inbox_bridge 16 + durable_bus 16 + report_reliable 6 =
>   54 passed 7 skipped。
> - **pytest 最终复验**：全量 **2878 passed + 3 skipped + 228 subtests，0 failed**
>   （601.66s，exit 0）——D8 顺序依赖 flaky 已收敛（全量前置清 .pytest_cache 成立）；历史基线
>   2853 + 1 flaky 在 P7 mock 泄漏修复后同步提升。注：全量窗口（~20:47-20:57）早于
>   test_leader_wakeup_injection.py 修复落盘（20:57:46），该文件修复后单独 47 passed 已补足。
> - **门禁终验**：`go vet ./...` 干净；`CGO_ENABLED=0 go build ./...` PASS；gofmt 全仓无输出；
>   repolint clean（1269 baselined，未用 -update）。pytest 全绿。
> - 未完成项（更新）：除「两仓全部改动未提交（工作树，提交时需含全部）」外，此前登记的遗留
>   ①②③④（protocol.go gofmt / make lint 环境 / SetInjector 接线 / D8 flaky）全部解除；
>   make lint 环境缺 golangci-lint 为环境项非代码缺口。

> 收口终验：TUI TEAM 入口自动选 leader + 成员列表 leader 置顶（2026-08-25，integration-tester-claude）——
> 集成验收全绿，实现与测试收敛无漂移：
> - **实现（tui-researcher 节点）**：① 进入路径——`enterTeamSession` 焦点非 leader 时经
>   `firstLeader()` 自动纠正到团队现存 leader 并 `FocusMember`（roster 高亮跟随，esc 落回绑定成员）；
>   仅全队无 leader 才拒绝，拒绝走独立 `refusal` banner（`chat_tui_team.go:125`，页面存活、`l`
>   任命后 `reload` 清除，非 `errMsg` 死路）；`restoreSession` 改返 `(member, suspended)`，
>   suspended（Ctrl+T 主动离开）静默优先于 leader 门——用户偏好 > 门禁；[TEAM] 点击 leaderless
>   团队落在管理页 + refusal banner 而非静默死路。② 成员列表——`tui/model.go sortedMembers`
>   改 leaders-first（`sort.SliceStable`，leader 之间相对顺序稳定）→ state 优先级 → ID ties；
>   `Reload` 按成员 ID 恢复焦点（`selectMember(prevMember)`），排序变化不破坏选择态；
>   新增 `FocusMember` 公开聚焦入口。firstLeader 语义=模板首个 leader 槽位（成员全来自模板，
>   「现存」=槽位 Leader 标记，多 leader 取首个、确定性同 [TEAM] 点击路径）。
> - **测试（test-engineer 节点）**：新增 8 项回归——`chat_tui_team_leader_entry_test.go` 6 项
>   （普通成员 t 入口自动选 leader / 多 leader 取首个 / 无 leader t 拒绝 / 无 leader [TEAM] 拒绝 /
>   suspended 无 leader 静默 / roster 打开 leader 置顶聚焦）+ `chat_tui_team_leader_gate_test.go`
>   2 项（refusal banner 生命周期：banner 下页面仍可达可写、l 任命清除、任命后 t 打开 session；
>   自动纠正后 roster 高亮跟随 + esc 落点）。既有旧契约测试（TestTeamSessionLeaderGate、
>   TestTeamRosterLeaderAssignAndSessionGate 等「非 leader 拒绝」断言）由 test-engineer 同步适配
>   为新契约（自动纠正），验收期间观察到的中间态失败收敛后不再复现。
> - **验收（integration 节点，只读）**：新测试 8/8 PASS（单独 + 全量）；`go test ./internal/cli/`
>   全量 13.85s PASS + `internal/team/tui` PASS（收敛后连续 4 次全量 0 failed）；
>   `-race` team 相关子集（cli 5.6s + tui 1.0s）全绿；门禁 vet 干净、`CGO_ENABLED=0 go build`
>   PASS、gofmt 全仓干净（验收修复 test-engineer 新测试文件 1 处注释对齐）、repolint clean
>   （1269 baselined，未用 -update）。
> - 未完成项：改动均在工作树未提交（Go 仓 20+ 文件 + Python 仓非 git）。

> 分析记录：leader session 思考中无法切换其他成员会话——根因定位（2026-08-25，integration-tester-claude，只读）：
> - **直接根因**：`switchTeamMember`（`internal/cli/chat_tui_team_switch.go:80`）的 `runtimeSwitchBusy()` 门
>   （`chat_tui.go:466-472`：`status.Running || PendingPrompt || BackgroundJobs>0 || pendingApproval!=nil ||
>   chooser!=nil`）在思考态拒绝切换。思考态=Running：`control/controller.go:2121-2136`，`c.running` 覆盖
>   spawnGuardedTurn→finishGuardedTurn 全程（含推理/流式输出）。键路由：`handleTeamKey` 中 ctrl+up/down
>   （`chat_tui_team_switch.go:220-224`）→ `stepSession`（`chat_tui_team_session.go:150-172`）→ 拒绝时
>   focus/current 保持不动、session.errMsg=「Finish or stop the current turn before switching member」。
>   t 键仅 roster 页有效（`chat_tui_team.go:418` enterTeamSession）；Tab 仅 completion 分支
>   （`chat_tui.go:1419`），session 中落 composer，均非切换路径。
> - **门理由已弱化**：switchTeamMember 注释「swapping m.ctrl under a running turn would leave its events
>   arriving for a backend the window no longer shows」在 §11.5 单 pump + member-tag 分流下不成立——
>   `handleMemberEvent`（switch.go:29-34）按 `boundMember` 分流：非绑定成员 TurnDone/Message→unread badge、
>   流式 delta→丢弃（`memberEventIsTerminal` 只认二者，switch.go:41-47）；backend 不重建（registry 复用
>   assembled backend），turn 后台继续，`History()` 持续 append（controller.go:4758 注释），切回时
>   `bindBackend` 从 History 快照重建 transcript（switch.go:150-153）。真实约束：①非绑定期间流式 delta
>   丢弃，切回只见完整结果不见中间输出；②`bindBackend` 清 `pendingApproval/chooser`（switch.go:140-141），
>   挂起 approval 的成员切走再切回 approval 模态丢失；③门为共享门（model.go:35 / effort.go:44 /
>   chat_tui_web.go:31 / runtime_rebuild.go:40,158 / skill_hooks.go:128,196），那些是真实重建、拒绝合理，
>   唯独 switchTeamMember 是纯 UI 绑定交换，门过严。
> - **测试矩阵缺口**：`stubBackend.RuntimeStatus()` 恒零（switch_test.go:54）→ busy 拒绝分支（switch.go:80
>   及 277 rebind 拒绝）零覆盖；「busy 中 stepSession 保持绑定+errMsg」「busy 中 rebind 拒绝」「切回
>   History 重建」均无测试。现有矩阵仅覆盖空闲切换/拒绝未知成员/事件分流/ctrl+down 空闲切换。
> - **建议修复方向（供实现节点）**：A（推荐）为 switchTeamMember 引入窄门——仅拒
>   `status.PendingPrompt || pendingApproval!=nil || chooser!=nil`，放开 `Running`/`BackgroundJobs`：
>   思考中可切、turn 后台继续、TurnDone 计 unread、切回 History 重建；需先验证 approval 事件在非绑定
>   成员时的归属（事件不 ingest 则 approval 交互悬空）。B 保守：仅放开 BackgroundJobs。C UX-only：
>   改 errMsg 文案（不解决无法切换本身）。共享门不要动（model/effort/web 是真实重建）。配套测试：
>   stubBackend 支持注入 RuntimeStatus{Running:true}，新增 busy 拒绝+保持绑定 / busy rebind 拒绝 /
>   切回 History 重建三组。

> 收口：report 自动唤醒 leader loop——Python bus→tmux 唤醒接线终态（2026-08-25，cli-researcher-claude）：
> - **状态更新**：此前登记的「report 自动唤醒仍待 cli 宿主接线」**解除**（上述 architecture/integration/
>   plugin 收口段的对应行已同步）；本节点为该链路唯一收口记录。
> - **链路复核（实现零改动，已闭环）**：`member_report_result` → `_record_report_and_notify_leader`
>   → `_notify_leader_of_report` → tmux 注入纯唤醒信号 → `leader_activate`/`_retry_deferred_report_injection`
>   巡检兜底。幂等链四重闸门：S1 原子（完成标记 + pending append 同锁 `_update_team_data`）、
>   S2 双层跳过（last_report_key + report_id，重复回报不增行不倒退）、S3 delivered 标记
>   （注入成功 mark_pending_reports_delivered，未 ACK 报告不被巡检重放）、冷却节流
>   （REPORT_WAKEUP_COOLDOWN_SECONDS + leader_last_wakeup_ts，时钟回拨放行）；信号收敛
>   （digest≤120 + artifact 指针，正文不搬运）；离线/direct leader 不注入，pending 持久化由
>   leader_activate drain；acked_watermark 随回报推进到 outbox 流头，单调不倒退。
> - **专项测试 92 passed 全绿**（`test_leader_wakeup_injection.py` + `test_report_reliable_delivery.py`
>   + `test_pipeline_inbox_bridge.py` + `test_pipeline_durable_bus.py`，修复前 2 failed——P7 隔离缺口
>   （fixture 未覆盖 `_BLACKBOARD_BIN`/`_SERVE_URL` + env pop）与 test_c4 断言语义（write_error 两类
>   提示：JSONL 写失败=真错误 vs blackboard 降级=预期内提示）已修复）；integration 复验口径
>   （wakeup 47 + seam 54 passed 7 skipped）与之一致。
> - **pytest 全量证据**：**2878 passed + 3 skipped + 228 subtests，0 failed**（601.66s，exit 0），
>   与 integration 收口终验记录保持一致（integration 注：全量窗口早于 wakeup 修复落盘，修复后单独
>   47 passed 已补足）。
> - 门禁：未触碰 Go 侧与 Python 生产代码（纯文档 + 既有测试修复，均在工作树未提交）。

## 10. 未决决策

1. SQLite 驱动和迁移工具是否引入新依赖，需单独审查 `go.mod` 体积和交叉编译影响。
2. 多主机部署时是否把 `BoardStore` 替换为 PostgreSQL；在此之前不提前引入 MQ。
3. 摘要模型的具体 provider 和成本预算由产品侧确认，协议只约束输入/输出 digest 与 token 上限。
4. `member_read_shared` 的兼容返回是否增加 `next_seq` 字段，建议采用向后兼容的可选字段。
5. [2026-08-27, architecture-analyst-claude] 参数预校验边界与兼容策略（背景：leader 调 `use_capability` → `tool:task` 报 "prompt is required"，授权已 allow_persistent 仍报错——参数错误在授权之后才暴露）。
   - 现状机制：`tool.Tool` 接口无 Validate 钩子（internal/tool/tool.go:21-35），必填校验全靠各工具 Execute 内手工检查（TaskTool.Execute task.go:638-640、RunProfileSpec task.go:756-758 双防线）；`parseUseCapabilityArgs` 仅对 `mcp-tool:` 前缀做 normalizeMCPToolArguments 对象归一化（usecapability_mcp_arguments.go:23-29），`tool:` 本地 capability 的 arguments 零结构校验；调用链为 ResolveCall（解析）→ 授权（allow_persistent → reasonix.toml）→ Execute（校验+执行），参数错误晚于授权暴露。
   - 预校验边界（建议）：`UseCapabilityTool.ResolveCall` → `resolveRegistryTool`（usecapability_registry.go:15）内、base.Target 绑定后、授权之前——校验失败走既有 ResolveCall 错误语义，授权流程（permission/approval/audit）不启动，不产生无意义授权记录。
   - API 建议（择一或叠加）：① `tool.Tool` 增加可选接口 `ArgsValidator { ValidateArgs(ctx, args) error }`（新文件 internal/tool/validator.go），resolveRegistryTool 对 `target.(tool.ArgsValidator)` 调用；TaskTool/fleet/parallel 实现（prompt 必填，错误文案沿用 "prompt is required" 保测试兼容）。② TaskTool.Schema() 声明 `"required":["prompt"]`——provider-visible 引导模型早生成正确参数，但属 cache-impact 变更，PR 需 Cache-impact 标注 + guard 测试。③ 通用兜底：resolveRegistryTool 对 schema 含 required 的本地工具做轻量必填检查（仅 required 字段存在性，不引入第三方依赖）。
   - 不动的边界：use_capability 自身 provider-visible schema 保持固定（usecapability.go:545-548 cache-stable 铁律）；Execute 内原校验保留作纵深防御；mcp-tool: 归一化不动。
   - 兼容策略：增量生效（仅实现 ValidateArgs / schema 声明 required 的工具获得预校验，其余零变化）；错误文案不变只提前时机；授权流程语义不变（预校验失败不触发授权）。
   - TUI 工具卡分类（配套）：cli/toolcard.go:102-109 `toolCategory` 仅 read/write/exec/proc 四类，task/use_capability/fleet/bgjobs/todo 等 agent 编排工具全落 default；建议增加 "agent" 类目（元工具+loop 工具同色），失败态工具卡可仿 shellFailureDetail 增加"缺必填参数"提示行。
   - 验证建议：预校验失败 → 断言 0 条授权记录（fake recorder）；带 prompt 调用 → 与现状一致；未实现接口的工具 → 无行为变化；schema 变更 → cache guard + boot effect test。

> **P1 落地：本地 registry-backed capability 参数 schema 预校验（2026-08-27，plugin-engineer-claude）**：
> - 对应第 10 节第 5 条"API 建议①"，实现 `tool.ArgsValidator { ValidateArgs(ctx, args) error }`
>   （新文件 internal/tool/validator.go，可选接口，type-assert 发现），并在
>   `UseCapabilityTool.resolveRegistryTool`（usecapability_registry.go）内、base.Target 绑定后、
>   授权之前调用：校验失败走既有 ResolveCall 错误语义（`fmt.Errorf("%s: %w", name, err)`），
>   权限提示/授权持久化/PreToolUse hooks/Execute 均不启动，不产生授权与 audit/ledger 记录。
> - 复用既有 validator：`provider.ValidateToolArgs`（新文件 schema_validate_args.go）用与
>   `ValidateToolSchema` 相同的沙箱编译器设置（UseLoader(nil)、draft-07 默认）做实例校验；
>   编译结果按 schema 字节串进程级缓存（sync.Map，工具 schema 注册后不变），每次调用只付
>   validate 不付 compile。错误文案带实例路径（如 `invalid arguments: ... at '/prompt'`）。
> - 实现范围（增量生效）：TaskTool / ReadOnlyTaskTool 实现（prompt 缺失或空白 →
>   "prompt is required" 沿用 Execute 文案；类型错误等其余约束落 schema）；
>   ParallelTasksTool / FleetTool 实现嵌套校验（tasks 每项 prompt 缺失/空白 →
>   "task N: prompt is required" 沿用 validateParallelTaskItems 文案，空数组 →
>   "at least one task is required"；嵌套结构/类型/约束落 schema，parallel/fleet 的
>   items.required=["prompt"] 已声明，无需改 Schema，provider-visible 前缀零变化）。
>   未实现接口的工具（含全部 MCP 工具）零行为变化——外部 MCP schema 不兼容不会进入该校验。
> - 单测：internal/agent/usecapability_task_prevalidate_test.go（6 用例：无效参数在权限前失败
>   且 0 授权记录/audit/ledger、合法参数照常过门恰好一次、畸形 JSON 两层都拦、无 validator
>   工具不变、mcp-tool 单字符串归一化不变）；internal/provider/schema_validate_args_test.go
>   （5 用例：合法/拒绝含嵌套路径/畸形实例/缓存重复/畸形 schema 响亮失败）。
> - 门禁：agent/tool/provider 三包全量 go test + `-race` PASS；gofmt -l 无输出；go vet clean；
>   全仓 go build PASS；repolint clean（1270 baselined 零新增，未用 -update）。
> - 不动边界：use_capability 自身 provider-visible schema 未改（cache-stable）；
>   Execute 内原校验保留作纵深防御；mcp-tool: 归一化与 resolveSkillCall 均未动。

> **P1 落地：TUI 工具卡能力分类（2026-08-27，cli-researcher-claude）**：
> - 对应第 10 节第 5 条"TUI 工具卡分类"建议，落地为 verb 标签分类（`cli/toolcard.go`
>   `capabilityDisplayName`/`toolCardVerb`）：`use_capability` 卡片不再一律显示 "MCP"，按
>   capability_id 命名空间分类——`mcp-tool:*`/`mcp-server:*` 仍为 MCP；`task:subagent`/
>   `task:read_only_subagent` → Sub-agent；`task:fleet`/`task:parallel_tasks` → Fleet；
>   `tool:`/`skill:`/`workflow:`/`session:`/`memory:` 等其余本地能力与无目标调用
>   （action=list/inspect/decline）→ Capability。
> - 覆盖三处渲染：transcript 工具卡（toolCard）、失败行（"● Verb ⊘ err"，此前
>   "MCP ⊘ prompt is required" 误导）、diff 块 header。`shellToolDisplayName` 增 args 参数，
>   失败行复用同一分类；chat_tui.go 零净增行（repolint 基线内）。
> - 单测：`TestToolCardCapabilityClassification`（11 例：Sub-agent×2 / Fleet×2 / Capability×5 /
>   MCP×2），既有 `TestToolCard` 中 action=list 断言由 "MCP" 改为 "Capability"（无目标调用是
>   本地代理操作，非 MCP）。
> - 门禁：cli 全量 ok、vet/build/gofmt clean、repolint clean（1270 baselined 零新增，未用
>   -update）。
> - 边界：分类只改显示标签，不动 use_capability 的 provider-visible schema（cache-stable
>   铁律）；`toolCategory` 颜色类目保持现状（超出本任务范围）。

> **集成验收（2026-08-27，integration-tester-claude，只读）**：
> - 结论：capability 参数预校验 + TUI 工具卡分类两处改动收口无漂移，权限语义零变化。
> - 预校验链路：`parseUseCapabilityArgs`（usecapability.go:565）在 ResolveCall 入口最先执行，
>   `normalizeMCPToolArguments` 仅对 `action=call` 且 `mcp-tool:` 前缀归一化（接受 JSON 对象或
>   单个 JSON 字符串，拒绝数组/标量/畸形/嵌套字符串），`tool:`/`mcp-server:` 等其余路径不变；
>   解析失败直接 return err——permission/hooks/evidence/audit 均不启动，授权记录不产生，
>   与 architecture-analyst 第 10 节第 5 条建议的"授权之前失败"边界一致。
> - 权限无副作用：预校验只收紧"非法参数早失败"，不新增/收窄任何授权规则；合法调用路径与
>   merge 前一致（`ResolveCall` → 授权 → `Execute` 纵深防御保留）。
> - 外部 MCP 兼容：mcp-tool: 归一化兼容既有单 JSON 字符串传参（`"{\"value\":1}"` 解包），
>   外部 MCP server 调用与未实现归一化能力的工具行为不变；`capabilityDisplayName` 对
>   `mcp-tool:`/`mcp-server:` 保持 "MCP" 标签，不误分类。
> - 工作树冲突：无冲突标记；`git diff --check` clean；改动仅 5 文件（toolcard.go/+47、
>   toolcard_test.go/+38、chat_tui.go/+5、diffview.go/+2 签名适配、本文档）。
> - 独立门禁（最终收口版磁盘状态）：agent 全量 24.9s ok、cli 全量 13.9s ok、cli -race 全量
>   39.5s ok、agent/cli 定向 race ok、team/tool 全家 ok、vet/build/gofmt clean、repolint clean
>   （1270 baselined 零新增，未用 -update）。定向验证：`TestToolCardCapabilityClassification`
>   12 例（Sub-agent×2 / Fleet×2 / Capability×6 / MCP×2，含 action=list/decline 与畸形 JSON）、
>   `TestNormalizeMCPToolArguments` 9 例、`TestUseCapabilityNormalizesMCPArgumentsBeforeResolution`
>   全 PASS。
> - 遗留：architecture-analyst 建议的 `ArgsValidator` 可选接口（`tool:` 本地 capability 必填
>   预校验）未在本轮实现——现仅 mcp-tool: 归一化 + 显示分类落地，`tool:` 参数错误仍晚于授权
>   暴露（第 10 节第 5 条记录在案，待后续排期）；工作树含本轮改动未提交。

> **P2 回归测试收口（2026-08-27，test-engineer-claude）**：
> - 测试就绪 6 组，覆盖 leader 全部场景：agent 层 5 组（`internal/agent/usecapability_task_prevalidate_test.go`）
>   ——① `tool:task`/`task:subagent` 缺嵌套 prompt、prompt 非字符串、arguments 非对象 → 授权前失败，
>   断言 gate 不触发（无 allow_persistent 可能）、目标不被 resolve/execute、audit/ledger 零记录；
>   ② 合法 prompt 仍过权限门恰好一次并解析到真实 task 工具（权限语义不变）；③ 嵌套参数校验
>   （缺 prompt/类型错/非对象三分支）；④ malformed JSON 两层（顶层解析层 + 嵌套 blob）同位置失败；
>   ⑤ 未实现 validator 的本地工具（tool:grep）与 mcp-tool 单字符串参数兼容不变（gate 照常、
>   Execute 照常、mcp 走 ExplicitlyDenies）。cli 层 1 组（`toolcard_test.go` 增
>   `TestToolCardLocalTaskVsExternalMCP`：本地 task 卡 Task(description) vs 外部 mcp__ 卡短名，
>   与 cli-researcher 的 12 例分类矩阵互补不重复）。
> - 当前状态：6 组全绿（-race 通过）。2 组设计红已随 plugin-engineer 实现落地转绿——第 10 节
>   第 5 条方案 A 落地为 `tool.ArgsValidator`（新文件 `internal/tool/validator.go`）+ `TaskTool.
>   ValidateArgs`（对象/非空字符串 prompt 预校验）+ `resolveRegistryTool` 内 target 绑定后、授权前
>   调用；回归钉为纯行为级断言，未改动一行测试即转绿。cli-researcher 遗留项（`tool:` 必填预校验
>   未实现）随之解决。
> - 门禁（实现落地后最终状态）：agent 全量 24.5s ok、agent -race 全量 39.8s ok、cli 全量 12.7s
>   ok（含工具卡 13 例）、targeted 预校验组 0.031s ok、gofmt/vet/build clean、repolint clean
>   （1270 baselined 零新增，未用 -update）。
> - 阻塞：无。工作树含本轮全部改动（测试 2 文件 + 实现 3 文件 + 文档）未提交，待 leader 收口。

> **P1 落地：ArgsValidator 授权前参数预校验（2026-08-27，tui-researcher-claude；接管 plugin-engineer 未落盘实现）**：
> - 对应第 10 节第 5 条 API 建议①，落地为可选接口：新文件 `internal/tool/validator.go`
>   （`ArgsValidator { ValidateArgs(ctx, args json.RawMessage) error }`，工具实现则可选预校验，
>   未实现零变化）。
> - 调用点：`UseCapabilityTool.resolveRegistryTool`（usecapability_registry.go:15）base.Target
>   绑定后、返回前——`target.(tool.ArgsValidator)` 命中则 `ValidateArgs(ctx, base.Args)`，
>   失败直接 return err，走既有 ResolveCall 错误语义：permission/approval/audit/hooks/Execute
>   均不启动、授权记录零产生、ledger 零条目。
> - TaskTool 实现（`internal/agent/task_validate.go`，独立文件控制 task.go 超限）：arguments
>   必须是 JSON object + prompt 存在且为非空 string；错误文案 "prompt is required" /
>   "prompt must be a string" 与 Execute 深度校验一致。Execute 内原校验（task.go:638-640）保留
>   作纵深防御；use_capability provider-visible schema 零改动（cache-stable 铁律）；mcp-tool:
>   归一化与授权路径（ExplicitlyDenies）不动。
> - 回归：`internal/agent/usecapability_task_prevalidate_test.go` 5 用例全绿（invalid args 三态
>   授权前失败 + 无 audit/ledger、valid 恰好一次 gate、malformed JSON 双层失败、无 validator
>   工具行为不变、mcp-tool 单 JSON 字符串兼容不变）。
> - 门禁：agent 全量 24.0s ok、tool/control/boot ok、vet/gofmt clean、repolint clean（1270
>   baselined 零新增，未用 -update）。
