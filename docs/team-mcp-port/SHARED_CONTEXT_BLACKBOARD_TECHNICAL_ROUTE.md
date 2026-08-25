# 共享上下文黑板技术路线

> 状态：§1 协议/存储模型、§2 并发一致性、§3 token 节约、§4 绑定解绑、§6 隔离安全已实现（Go 内核）；§5 durable command/report bus 服务端已实现（Python MCP server）；P6.1 薄 blackboard CLI 已实现（2026-08-24）；P7 MCP 接线已实现（Python 走 Go CLI，SQLite 主写 + JSONL 双写，2026-08-24）。
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
> ✅ P6.5 可复用选项弹出列表（2026-08-25，tui-researcher-claude）：`internal/cli/option_list.go` 纯状态组件（`optionList`：`setOptions`/`handleKey`(consumed+Commit/Cancel action)/`wheel`/`resize`/`view`/`choice(s)`，单选 enter 提交光标项、多选空格 toggle + enter 提交集合；up/down/left/right/k/j 导航、home/end/pgup/pgdown 翻页、esc/ctrl+c 取消、禁用项跳过、offset 跟随、视图按窗口 1/4 自适应高度、行宽 ANSI 截断）。接入：member 编辑器 status/proxy/agent 与 pool provider 字段由行内循环 picker 迁移为弹窗（role 只读不变；legacy provider 值作为列表首项 "legacy: X" 标记、确认写回原值；picker 激活期宿主层独占键，字母/粘贴惰性不泄漏）；`memberEditState` 删 buf/opts/pick、`poolState` 删 provSel（struct-state 净减）。
> ✅ P6.5.1 弹窗布局修复（2026-08-25，tui-researcher-claude）：`option_list.view` 三处几何修正——(1) 内容行由 `padColumn`（至少补 1 空格语义）改为按 `width-4` 精确 pad，右边界框不再左移 1 列；(2) `start` clamp 到 `[0, len-rows]`，窗口缩小滚动后变大不再顶部空白 + 底部假 "no options" 填充；(3) 帮助行补齐到 width（截断后仍整宽）。新增 `option_list_layout_test.go` 7 个几何断言（边框逐行对齐、窗口增长满行、空态、一行窗口光标可见、多选标记不撑宽、窄窗口帮助行、滚动窗口连续真实行）。
> ✅ P0 Ctrl+T 持久退出（2026-08-25，tui-researcher-claude）：`exitTeam()` 对齐 x 键——补 `WriteSelection(Team only)`（清空持久化 selection）+ `teamSuppressAutoSession=true`（下次 [TEAM] 点击停 ModeTeams 管理页，space 才进成员管理，t 显式恢复 auto-session）；Ctrl+T 优先级不变（`handleTeamKey` 最前拦截，先于一切状态 owner 与 composer）。`restoreSession()` 兑现持久化读侧：`onTeamButtonClick` 在未抑制时读 `ReadSelection`，MemberID 在册且 `IsLeader()` 才恢复该成员窗口，否则（absent/非 leader/stale）回落 `firstLeader`——会话入口的 leader 门控与 t 键镜像；`p.sessions == nil` 防护。`openLeaderSession` 重构为 `openSession(initial)`。
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

## 5. Leader/成员指令与回报管道

> 实现状态（2026-08-24，cli-researcher-claude）：服务端 bus 落地于 MCP server（`/home/zwc/mult_agent_mcp`）。
> - `common/durable_bus.py`：envelope 字段级校验、ACK 状态机（received/accepted/rejected/executed，幂等不重执行）、cancel（queued 终态 / in-flight best-effort 标记）、kind=command per-member ≤5 pending 背压、ack 游标单调不倒退、after_seq 过滤（legacy 无 seq 跳过）、>2KB 指针注入文本。
> - `mult_agent_mcp.py` 接线：outbox 条目 envelope 字段（env_id/task_id/reply_to/severity/digest/generation/ttl）、>2KB 载荷落盘 `members/<name>/inbox/<id>.md` + 只注入指针行、新工具 `member_bus_ack`/`leader_bus_cancel`/`leader_bus_ack_report`、`member_report_result(reply_to=)` 回报闭环（指令 closed(executed)）、`member_read_shared(after_seq=)` 增量游标（缺省=最近 10 条兼容）、results.jsonl 全写路径（member 回报/leader 完成/复活事件）seq 服务端单调盖章。
> - 验收：`tests/test_pipeline_durable_bus.py` 16 用例全绿；既有 outbox 回归（test_ack_batch_outbox / test_task1_batch_ack_granularity）不受影响。
> - P7 MCP 接线（2026-08-24，cli-researcher-claude）：新增 `common/blackboard_client.py` 薄 client（一次性 stdio JSON 调用 Go `reasonix-blackboard` CLI，op=append/read-after/read-view/bind/cursor；业务拒绝编码进响应 JSON，非零退出仅服务失败，超时/缺二进制 → BridgeError）。`member_report_result` 主路径走 bridge：report_id→client_msg_id（Go UNIQUE 幂等，重放返回原事件不产生新 seq）、seq 由 Go store 事务内分配、identity（member/role/agent/generation）由 Python 服务端从团队数据解析盖章、results.jsonl 双写（带 board_seq/client_msg_id，seq=Go seq）；`member_read_shared(after_seq>0)` 走 bridge read-after（SQLite 事实源，返回文本附 next_seq 供游标推进；空页 NextSeq=0 不倒退），缺省"最近 10 条"保持 JSONL 路径（SQLite 无 last-N 语义，全量快照读归 P8 切读）；桥不可用（二进制缺失/超时/非零退出）自动降级纯 JSONL（Python seq 盖章兜底，提示可见）——回报绝不因桥丢失；Python bus ACK/retry/backpressure 原样保留未动。

### 5.1 两条 durable 通道

- 指令：leader -> member，outbox 持久化，成员离线时排队。
- 回报：member -> leader，写入结果日志/黑板后可确认，leader 离线不阻塞成员回报。

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

## 8. 实施分期与回滚

| 阶段 | 交付 | 回滚点 |
|---|---|---|
| P1 | schema_version/kind/seq/digest/after_seq，JSONL 兼容读 | 保留旧 `results.jsonl` 读路径 |
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
| `chat_tui_team_exit_test.go` 增量 `TestTeamExitDropsSelectionAndParksNextClick`（P0，tui-researcher） | Ctrl+T 清空持久化 selection + 设 suppress → 下次 [TEAM] 落 ModeTeams 管理页，t 显式恢复 auto-session |
| `chat_tui_team_session_test.go` 增量 `TestTeamRestoreResumesPersistedLeaderSelection`（P0，tui-researcher） | restoreSession 门控：absent/非 leader/stale selection 回落 firstLeader，在册 leader 恢复其窗口 |

验收回归结果（最终状态，2026-08-24 复核）：`go test ./internal/team/ -count=1 -race` 全绿。

最终门禁证据（2026-08-24，实现侧复核一致）：

- `go test ./internal/team/...` PASS
- `go test -race ./internal/team/...` PASS
- `go test ./internal/cli/` PASS（P6.4 TUI 接线全量，含 l assign 门控 / role·Leader 只读 / x 退出全部）
- `go test ./internal/cli/` PASS（P6.5 组件全量，2026-08-25：optionList 组件测试 + member/pool picker 迁移回归 + -race 相关子集；repolint clean 1271 baselined 未用 -update）
- `go test ./internal/cli/` PASS（P6.5.1 布局修复，2026-08-25：view 行 pad 到 width-4 使边框对齐、start clamp 到 [0, len-rows] 消除窗口增长后的顶部空白与假 "no options" 尾、帮助行补齐到 width；新增 option_list_layout_test.go 7 个几何断言；全量 9s + vet/build/repolint clean）
- `go test ./internal/cli/` PASS（P0 Ctrl+T 持久退出，2026-08-25：exitTeam 对齐 x 补 selection 清空 + suppress；restoreSession 读侧 + leader 门控；新增 TestTeamExitDropsSelectionAndParksNextClick / TestTeamRestoreResumesPersistedLeaderSelection，TestTeamReentryStartsFreshOnLeader 改为「exit 后落管理页 + t fresh on leader」语义；test-engineer chat_tui_team_ctrlt_test.go 7 测试 -race 全绿；全量 9.5s + -race 子集 + vet/build + repolint clean 1271 baselined 未用 -update）
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

## 10. 未决决策

1. SQLite 驱动和迁移工具是否引入新依赖，需单独审查 `go.mod` 体积和交叉编译影响。
2. 多主机部署时是否把 `BoardStore` 替换为 PostgreSQL；在此之前不提前引入 MQ。
3. 摘要模型的具体 provider 和成本预算由产品侧确认，协议只约束输入/输出 digest 与 token 上限。
4. `member_read_shared` 的兼容返回是否增加 `next_seq` 字段，建议采用向后兼容的可选字段。
