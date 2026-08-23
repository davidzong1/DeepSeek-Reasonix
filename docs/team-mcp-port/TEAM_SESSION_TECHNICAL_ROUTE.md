# 技术路线 —— TEAM 独立 Agent 会话与 Leader 生命周期

> 状态：**P1/P2/P3 基础实现完成，P4 runtime-TUI 接入待实施**（2026-08-22）。全量 CLI 仍受当前沙箱禁止 IPv6 `httptest` 监听的环境限制；该失败与本次改动无关。
> 本文档只描述本次增量：成员属性编辑、TEAM 会话、成员独立 Agent、独立上下文、Proxy 配置和 Leader 解除清理。
> 权威优先级：用户拍板 > `docs/team-mcp-port/TASK.md` > 本文档 > 实现代码注释。
> 本文档不替代 `TASK.md`，也不把计划描述为已实现。

## 1. 已确认约束

1. 成员编辑页采用成员池式属性列表：上下选择，`Enter/Space` 编辑，`s` 保存，`Esc` 取消且零写入。
2. `t` 进入团队会话，但前提是当前选中成员必须是 Leader；非 Leader 不得启动会话。
3. 团队会话中每个成员拥有独立 Agent。CLI 只是显示和交互窗口，成员切换显示对应 Agent 的历史和当前状态。
4. 所有成员上下文均可写，但只能写入自己的成员目录：

   ```text
   .reasonix/team/context/<team-name>/<member-id>/
   ```

5. 跨成员上下文共享不在本阶段实现，后续通过共享黑板任务实现。
6. `Role` 是自由文本，用于成员身份和专精方向，并注入该成员的 system prompt；Role 不承担 Leader 标识职责。
7. 多个成员可以复用同一个 `AgentUserRef`，但成员实例、runtime、上下文、游标和恢复状态必须完全独立。
8. Proxy 只支持 `IP:port`，默认值为 `127.0.0.1:7980`，本阶段不支持认证字段。
9. `k` 是独立的解除 Leader 快捷键。解除前必须经过三级确认；确认完成后清空整个团队所有成员的历史上下文。
10. 解除 Leader 后团队可以暂时无 Leader；没有 Leader 时 `t` 必须拒绝进入团队会话，直到重新授予 Leader。

## 2. 概念与身份模型

### 2.1 AgentUser 与 MemberID 解耦

`AgentUserRef` 是静态配置引用，不是运行实例身份。它可以被多个成员共享，装配时读取为配置快照。

`MemberID` 是团队内唯一的成员实例身份。所有运行状态都由以下复合键派生：

```text
(team-name, member-id)
```

该复合键决定：

- Agent runtime 句柄；
- 上下文目录；
- 恢复游标；
- 当前任务和状态；
- CLI 会话窗口的目标。

修改 `AgentUserRef` 只影响下一次 Agent 装配，不得重置当前成员历史。删除 Agent user 前必须检查成员引用，避免产生悬挂绑定。

### 2.2 Role system prompt

每个成员实例的 system prompt 装配必须包含团队、成员 ID 和自由文本 Role，例如：

```text
你是团队 <team-name> 的成员 <member-id>。
你的团队角色是：<role>。
请以该角色和专精方向参与任务。
```

Role 为空时使用明确的未配置提示，不得把 `leader` 字符串作为 Leader 状态的唯一来源。

## 3. 分层架构

```text
TEAM TUI / CLI 显示窗口
        |
        v
TeamSessionController
  - 当前团队、当前成员、Leader 门禁
  - t 进入、成员切换、重启恢复
  - k 解除 Leader 流程
        |
        +--> TeamRuntimeRegistry
        |      - (team, memberID) -> 独立 Agent runtime
        |      - AgentUserRef 只作为装配快照
        |
        +--> TeamContextStore
        |      - 成员目录、消息、状态、游标
        |      - 文件锁、CAS、原子写、恢复
        |
        +--> TeamStore / AgentUserStore
               - Member、Role、Leader、AgentUserRef、Proxy
               - 现有 CAS 与写后重载语义
```

TUI 不直接操作文件，不创建共享可变 Agent 状态；所有写入通过领域层接口完成。

## 4. 存储设计

### 4.1 成员上下文

每个成员目录至少包含：

```text
<member-id>/messages.jsonl   # 该成员的消息/思考历史
<member-id>/state.json       # runtime 状态和版本信息
<member-id>/cursor.json      # 恢复游标、ResumeCount、ContextRef
```

实际文件名可以由实现统一，但不得改变目录隔离语义。

写入规则：

- 成员 runtime 是自己目录的唯一逻辑写者；
- 同成员写入使用文件锁和 CAS 版本；
- 先写临时文件，再 `rename` 原子替换；
- 写后重新读取并校验；
- 不同成员之间不共享游标、历史或 runtime 句柄；
- 上下文内容不得进入日志、团队回报或共享黑板，除非后续黑板功能显式发布。

### 4.2 会话选择持久化

团队级会话状态保存当前选中成员 ID，例如：

```text
.reasonix/team/session/<team-name>.json
```

该状态只保存当前窗口选择和版本信息，不保存其他成员的历史内容。重启时先加载团队成员，再恢复选中成员；若成员已删除或不可用，回退 Leader，若无 Leader 则停留在团队管理页并显示原因。

### 4.3 Proxy

Proxy 配置存入现有 Team/Proxy 配置文档，字段最小化为：

```text
enabled: bool
address: "127.0.0.1:7980"
```

地址必须解析为合法 IP 和端口；空值使用默认地址。不得引入用户名、密码或 Token 字段。

## 5. TUI 状态机

```text
MemberRoster
  e/Enter -> MemberEdit
  l       -> SetMemberLeader CAS toggle（指定/取消焦点成员 Leader）
  t       -> TeamSession（仅当前成员 IsLeader，帮助栏文案 "🌟 t Enter_session"）
  k       -> LeaderResetConfirm1（仅当前成员 IsLeader）
  帮助块  -> 单段文案自适应：宽度足够一行，窄屏在词边界换行

MemberEdit
  ↑/↓     -> 字段焦点
  Enter   -> 字段编辑
  s       -> Validate + CAS 保存
  Esc     -> 返回且零写入

TeamSession
  默认    -> Leader memberID
  右侧列表 -> 切换当前成员 Agent 窗口
  Esc     -> 返回 MemberRoster，保留各成员上下文
```

`t` 的 Leader 门禁必须在控制层再次校验，不能只依赖 UI 标记。成员切换只改变当前 runtime 目标，不能复制或合并上下文。

## 6. `k` 解除 Leader 与清理流程

> 实现状态（2026-08-22）：`k` 已从导航键独立为解除 Leader 快捷键，仅选中 Leader 可触发（roster 与成员编辑 fieldList 生效；字段编辑与 session 内 `k` 保持导航）。三级确认（警告 → 精确 Leader ID → 目录清单）任一阶段 Esc/30s 超时零写入回到 Idle；Confirm2 精确匹配 Leader ID，空 Enter 或错误 ID 被拒；Confirm3 显示团队名、成员目录数（实际 context 清单）与清理范围。执行前 Verify 重读 `team.json` 与清单；清理走领域层 `.trash` 时间戳 staging（`TrashTeam`/`RemoveTrash`/`ClearTeamTrash`：先移入 `.trash/<team>-<ts>` 再删、崩溃残留扫描、幂等、失败保留 trash 并报告）；完成后 Leader=false 持久化、会话选择清空、重读验证。无 Leader 时 `t` 拒绝进入团队会话（IsLeader 门控 + 会话恢复回退）。测试全绿（team+cli），repolint clean。

解除操作是破坏性动作，采用以下不可跳过状态机：

```text
Idle
  -> Confirm1: 警告将解除 Leader 并清空全团队历史
  -> Confirm2: 输入准确的 Leader memberID
  -> Confirm3: 显示待清理目录清单并再次确认
  -> Verify
  -> Clearing
  -> Done / Error
```

安全要求：

1. 任一级 `Esc` 或 30 秒超时都回到 `Idle`，上下文字节必须保持不变。
2. Confirm2 必须精确匹配当前 Leader ID，禁止只按 Enter 通过。
3. Confirm3 必须显示团队名、成员目录数量和清理范围。
4. Verify 阶段重新读取 `team.json` 和目录清单，防止确认期间成员变化。
5. 清理期间持有团队控制锁，暂停新的 Agent 装配和会话进入。
6. 先将 context 根目录移动到带时间戳的 `.trash`，再执行删除；失败时保留 `.trash` 并报告错误。
7. 清理完成后重读验证：Leader 标记已解除，原 context 子树不存在或仅保留可审计 trash。
8. 使用 journal/marker 支持进程崩溃恢复；重复执行必须幂等。
9. 清空范围严格限制为 `.reasonix/team/context/<team-name>/`，不得触及其他团队或普通会话历史。

建议清理结果：`MemberSlot.Leader=false` 持久化，团队上下文全部清空；重新指定 Leader 后，新 Agent 从空上下文启动。

## 7. 迁移与兼容

- 旧 `MemberSlot`、`Role`、`AgentUserRef` 和现有 CAS 文件格式保持兼容；新增字段使用可选/`omitempty` 语义。
- 旧的 `Role == "leader"` 只作为兼容读取，加载后映射到独立 Leader 字段。
- 既有普通聊天历史不迁入团队上下文，团队上下文按成员首次进入会话时懒创建。
- 已存在但没有上下文目录的成员视为空历史，不视为损坏。
- 相同 AgentUserRef 的多个成员不得共享旧的全局游标；每个成员首次装配都创建独立 runtime 状态。
- Agent user 删除、成员删除和团队删除都必须增加引用/上下文孤儿检查。

## 8. 实施分期

### P1：领域和配置 —— ✅ 领域契约已完成（2026-08-22，architecture-analyst-claude）

> 已落地：`ValidateRole`（自由文本校验：UTF-8/长度/控制字符）+ `SetMemberRole`（AddMember 同门控）+ `SystemPromptForRole`（turn tail 装配，空角色未配置提示）；Proxy `Address` 单字段 IP:port 校验 + 默认 `127.0.0.1:7980` + legacy host/port 兼容读取；`DeleteAgentUser` 引用保护（`ErrAgentUserInUse`，成员 override 与团队默认均受检）。成员编辑 TUI 界面由 TUI 侧接线（领域接口已就绪）。
>
> **CLI 接缝状态（2026-08-22，cli-researcher）**：poolErrMsg 已映射 `ErrAgentUserInUse`；旧 s 循环-status 测试已迁移为成员编辑保存流程（TestTeamMemberEditStatusPersists）；成员编辑态输入隔离已接线。handleTeamPickerKey 复杂度已拆分，repolint 通过。
>
> **粘贴路由审计与修复（2026-08-22，cli-researcher + tui-researcher）**：审计 [TEAM] 入口后全部 overlay/编辑态的粘贴路径 —— `tea.PasteMsg`（终端 bracket paste）与 `clipboardTextPasteMsg`（Ctrl+V/Shift+Insert/右击异步剪贴板）均汇聚 `applyComposerPasteCount`，TEAM 打开时经 `teamPasteTarget` 路由到活动文本缓冲区，非文本态静默丢弃、composer 全程隔离。**修复**：`teamPasteTarget` 补上成员编辑 role 行（`memberEditFieldEdit` + `"role"`）与 k 解除确认的精确 ID 阶段（`leaderResetID`）——此前两处自由文本输入的 bracket paste 被吞、Ctrl+V 不触发剪贴板读取，现粘贴分别追加进 `memberEdit.buf` / `reset.buf`（role 经 Enter+s 落盘，ID 经精确匹配进入目录清单阶段）。**验收**：新增回归测试 8 个（role 字段 bracket paste/Ctrl+V、成员编辑 picker 行惰性、session 窗口惰性、delete 确认态惰性、解除确认 ID 阶段粘贴端到端、TEAM 关闭后 composer 粘贴恢复；composer 均断言不被污染），连同既有 6 个粘贴测试共 14 个覆盖全部状态；provider/成员 picker 等选择控件不吞贴，API key 明文契约未改。门禁全过：gofmt、`go test ./internal/cli/... ./internal/team/...` 全绿、vet、`CGO_ENABLED=0 go build`、repolint clean（baseline 1276 未加宽）。

- 成员字段列表编辑接口和 UI；
- Role 自由文本校验与 system prompt 装配数据；
- Proxy `IP:port` 配置和默认值；
- AgentUserRef 删除引用保护。

### P2：独立 Agent 会话

> 实现状态（2026-08-22）：核心适配层已落地 —— `internal/team/sessionstore.go`（TeamSessionStore：成员上下文 messages.jsonl/state.json/cursor.json + 团队选择 session/&lt;team&gt;.json + 清理原语）与 `internal/team/agentruntime`（spec.go/prompt.go/adapter.go：InstanceKey=(team,memberID)、ConfigSnapshot 无密钥快照、ComposeSystemPrompt role 注入、Registry 的 Start/Stop/Switch/Observe/Send/MarkConsumed/Close，共享 AgentUserRef 成员实例完全隔离）。测试全绿（team+agentruntime），repolint clean。TUI/CLI 消费方已接入（tui-researcher）：成员属性编辑器（Role/Leader/Status/Proxy/Agent 字段，s 逐字段 CAS 保存，Esc 零写入）、`t` 门禁（控制层读 slot.IsLeader，非 Leader 拒绝）、TeamSession 窗口（默认 Leader、右侧成员切换、WriteSelection 持久化 + 重开恢复、历史经 Messages 读取）、`k` 解除 Leader 三级确认（警告→精确 ID→目录清单→ClearTeamTrash trash staging + SetMemberLeader(false) + selection 清空，Esc/30s 超时零写入，Verify 重读 registry）。Leader 入口对齐（2026-08-22）：roster `l` 直达指定/取消焦点成员 Leader（走 store.SetMemberLeader CAS + reload，焦点按 ID 保持），门禁与独立 Leader 字段同源（slot.IsLeader）；帮助栏文案精确为 "🌟 t Enter_session"（roster 与成员编辑两处），roster 帮助行注明 "l leader on/off"；指定后 `t` 立即可进、取消后 `t` 明确拒绝。测试全绿（cli+team），gofmt/vet/build/repolint 全过。

- TeamRuntimeRegistry；
- TeamContextStore 和恢复游标；
- `t` Leader 门禁；
- Leader 默认窗口、成员切换和选择状态持久化；
- 相同 AgentUserRef 的成员隔离。

### P3：Leader 解除与清理 —— ✅ 已完成（2026-08-22，architecture-analyst-claude）

> 已落地：`k` 独立为解除 Leader 快捷键（仅选中 Leader 可触发）；三级确认（警告 → 精确 Leader ID → 目录清单），Esc/30s 超时零写入；Confirm2 精确匹配拒绝错误 ID；执行前 Verify 重读 registry 与清单；领域层 `.trash` 时间戳 staging 原语（`TrashTeam`/`RemoveTrash`/`ClearTeamTrash`：崩溃残留扫描、幂等、失败保留 trash 并报告、范围严格限定团队 context 根）；清理后 Leader=false 持久化 + 会话选择清空 + 重读验证；无 Leader 时 `t` 拒绝进入（IsLeader 门控 + 恢复回退）。测试全绿（team+cli），repolint clean。

- `k` 三级确认；
- 控制锁、journal、`.trash` 清理和崩溃恢复；
- 无 Leader 时的安全阻断；
- 全团队 context 清空验收。

## 9. 测试与门禁

> 测试状态（2026-08-22）：P1/P2/P3 实现与接线已落盘，`TEAM_SESSION_TEST_PLAN.md` 矩阵已按当前 API 核对。团队包与针对性 CLI 测试通过；`go vet ./...`、`CGO_ENABLED=0 go build ./...`、gofmt 和 repolint 均通过。全量 `go test ./internal/team/... ./internal/cli/...` 的唯一失败是沙箱禁止 IPv6 `httptest` 监听（`TestFetchModelListCompatWalksCandidates`），非本次功能回归。
> **test-engineer 独立验收（2026-08-22 第三轮，test-engineer-claude）**：`TEAM_SESSION_TEST_PLAN.md` §1 矩阵 A1-A9/B1-B6/C1 全部 ✅。本会话补 5 个缺口测试：`TestProxyAcceptsIPPortAddresses`（A3 正向）、`TestClearTeamIdempotent`（A9 幂等）、`TestTeamEnterSessionRefusedWithoutLeader`（B2 无 Leader 分支）、`TestSessionSelectionFallsBackToLeaderAfterMemberRemoved` 与 `TestSessionSelectionNoLeaderStaysOnRosterWithReason`（A7 回退/停留分支）。独立重跑全部门禁：`go test ./internal/team/... ./internal/cli/...` 全绿、gofmt / go vet / CGO_ENABLED=0 build clean。实现者记录的 IPv6 httptest 失败（`TestFetchModelListCompatWalksCandidates`）在当前环境**已通过**，未见复现。§5 既有测试受影响清单已全部处理（`TestTeamToggleLeaderPersists` 由 `TestTeamRosterLeaderToggleAndSessionGate` 重写承接）。
> ⚠️ **C1 门禁 1 例外（阻塞项，待实现者）**：repolint 报 `internal/cli/chat_tui.go` 4 个函数超 ratchet budget 3 行（1314/1311，chatTUI.update/View/ingestEvent/runSlashCommand），属已提交 P3 接线代码（43ddaa112），非测试改动引入；按约定不扩 baseline，需实现者收敛函数大小后复跑。

### 单元测试

- MemberID 与 AgentUserRef 解耦；
- Role prompt 注入；
- Proxy 默认值、IP/端口校验；
- context 路径只包含合法 team/member 键；
- 同 AgentUserRef 的两个成员拥有不同 runtime、游标和历史；
- 文件锁、CAS、原子写和恢复游标。

### TUI/集成测试

- 成员编辑 Enter/Space、保存、取消零写入；
- roster `l` 指定/取消 Leader（SetMemberLeader CAS 落盘、不影响其他成员），指定后 `t` 进入、取消后 `t` 明确拒绝；
- 帮助栏文案 "🌟 t Enter_session"（roster 与成员编辑两处），roster 行注明 "l leader on/off"；
- roster 帮助块自适应：宽屏整段一行、窄屏词边界换行（文案与快捷键不变）；
- 粘贴路由（2026-08-22）：bracketed `tea.PasteMsg` 与 Ctrl+V/Shift+Insert 异步剪贴板结果在活动文本字段落地（add team/member 输入、pool 非 provider 字段、成员编辑 role 行、k 解除确认的精确 ID 阶段），provider picker 与成员 picker 行/会话窗口/删除确认等非文本态丢弃且 composer 保持原样，TEAM 关闭后粘贴恢复进 composer；
- 非 Leader 按 `t` 被拒绝；
- Leader 按 `t` 进入并默认显示 Leader；
- 成员切换显示各自历史，重启后恢复；
- `k` 的三阶段确认、错误 ID、Esc、超时均不改数据；
- 清理后所有成员 context 消失，其他团队和普通会话不受影响；
- 清理与 Agent 装配并发时不会产生新 runtime 或半清理状态。

### 工程门禁

```text
gofmt
go test ./internal/cli/... ./internal/team/...
go vet ./...
CGO_ENABLED=0 go build ./...
go run ./tools/repolint
```

任何门禁受环境限制时必须记录真实阻塞，不得改写为通过。

### §9 验收状态（2026-08-22，P1/P2/P3 落盘后由 integration-tester 独立执行）

| 组 | 结果 |
|---|---|
| A1-A9 领域单测 | ✅ 全过（sessionstore_test / proxy_test / agentruntime prompt+adapter 测试，实现者落盘、本终端全量复跑 ok） |
| B1-B6 TUI/集成 | ✅ 全过（TeamMemberEdit* / TeamSession* / TeamLeaderReset* 系列） |
| B7 清理与装配并发 | ⚠️ 无专项测试，建议后续补 |
| 工程门禁 | ✅ gofmt clean · go test ./internal/team/... ./internal/cli/... ok · go vet exit 0 · CGO_ENABLED=0 go build exit 0 · repolint clean（1276 baselined 未拓宽） |
| 风险记录 | 路线 §4.1"文件锁和 CAS 版本"未在 store 实现，当前依赖成员目录唯一逻辑写者（agent loop 未落地，无真实并发写者）；P2 引入并发写者前需补 CAS |

## 10. 完成定义

本路线只有同时满足以下条件才算完成：

1. 所有成员可独立运行、写入和恢复 Agent 上下文；
2. AgentUserRef 可以复用，但不会共享成员 runtime 或历史；
3. `t` 只能从 Leader 进入团队会话；
4. `k` 三级确认后只清理目标团队全部成员上下文；
5. 取消、超时、错误和崩溃恢复不会误删数据；
6. Proxy 默认值和地址校验稳定；
7. 测试与工程门禁全部通过。

## 11. P4：成员 Agent runtime 接入 TUI（用户已拍板，待实施）

### 11.1 当前缺口与边界

P1/P2/P3 已完成成员配置、上下文存储、选择恢复、Leader 门禁和历史展示，但尚未完成可交互的 Agent 会话：

- `sessionState` 目前只保存当前成员、成员列表和焦点；`handleSessionKey` 仅支持成员切换与退出，没有会话输入缓冲和发送动作。
- `renderTeamSession` 只读取 `TeamSessionStore.Messages` 渲染历史，不显示活动输入框，也没有实时响应区域。
- `internal/team/agentruntime.Registry` 当前负责实例登记、隔离、历史读写和 `Send` 持久化；实际 Agent loop、provider 调用和事件生产仍未接入。
- TEAM 打开时普通聊天 composer 必须继续隐藏；会话输入框是 TEAM overlay 自己的独立输入控件。

本节点只扩展团队会话，不改变普通聊天会话、Agent user 字段契约、API key 明文管理契约或跨成员共享黑板范围。

### 11.2 分层结构

```text
TEAM TUI
  |
  v
TeamSessionController
  - 当前 team/member、输入缓冲、成员选择、未读数
  - 发送、切换、退出、重启、事件订阅
  |
  +--> TeamRuntimeRegistry
  |      - (team, memberID) 独立实例
  |      - Start/Stop/Switch/Send/Subscribe/Observe
  |
  +--> MemberRuntimeAdapter
  |      - provider 调用和 Agent loop
  |      - user/assistant/error/status 事件
  |
  +--> TeamSessionStore
         - messages.jsonl/state.json/cursor.json
         - selection 持久化
```

`AgentUserRef` 只能作为装配配置快照，不能进入运行实例键。运行实例、事件订阅、消息队列、cursor、未读状态均以 `(teamName, memberID)` 隔离。

### 11.3 runtime 接口契约

在现有 `agentruntime.Registry` 与 provider 执行实现之间增加明确适配边界。建议接口语义如下，具体命名可按现有包风格调整：

```text
MemberRuntime
  Start(ctx)
  Stop()
  Send(prompt)
  Subscribe() <-chan RuntimeEvent
  Snapshot()
```

事件统一携带实例身份和单调序号：

```text
RuntimeEvent {
  Team
  MemberID
  Sequence
  Kind       // started, delta, message, done, error, stopped
  Text
  Timestamp
}
```

约束：

1. provider 调用只发生在 runtime adapter 内，TUI 不直接调用 provider。
2. API key 只存在于装配快照和受控 provider 请求中，不进入事件、日志、报告或共享上下文。
3. `Send` 先将 user 消息写入目标成员 context，再提交给 Agent loop；失败时保留 user 消息并生成 error 状态。
4. 完整 assistant 消息原子写入目标成员 `messages.jsonl`；delta 可以在内存中聚合，不能跨成员合并。
5. 事件通道必须有界；慢消费者通过 delta 合并或丢弃中间刷新降低 UI 压力，但不得丢失最终消息和错误事件。

### 11.4 TUI 会话输入与操作

`t` 进入时只启动当前 Leader runtime，其他成员在首次切换时懒启动；每次启动都使用独立 `InstanceKey`。这样既满足所有成员独立，又避免进入会话立即启动全部 provider 请求。

会话 overlay 的输入协议：

| 操作 | 行为 |
|---|---|
| `Enter` | 将输入发送给当前成员，写入 user 消息并清空输入框 |
| `Shift+Enter` / `Alt+Enter` | 在会话输入框插入换行 |
| `Ctrl+Up` / `Ctrl+Down` | 输入框获得焦点时切换成员 |
| `Tab` / `Shift+Tab` | 循环切换成员 |
| `↑` / `↓` | 输入框内移动光标/浏览输入历史 |
| `Ctrl+C` | 停止当前成员正在运行的请求，不退出 TEAM |
| `r` | 停止并重新启动当前成员 runtime |
| `Esc` | 关闭团队会话，返回成员管理界面 |

成员列表显示当前成员、Leader、Role、runtime 状态和未读数。切换成员时只改变显示目标和发送目标，不复制、清空或合并上下文。

> **入口状态机对齐（2026-08-22，tui-researcher）**：点击 `[TEAM]` 的目标态定义为——`session.active=true`、`current=选定团队 leader`（`restoreSession` 改为一律 `firstLeader()`，不再读持久化 selection）、普通 composer 隐藏（`hideComposer` 的 `teamPick != nil` 门）、session 历史从 `(teamName, leaderID)` 独立 context 加载、TEAM 顶栏保留成员栏并可切换（右侧 roster 列 + `stepSession` 重订阅）。Esc 关闭 session 落回团队列表（roster 管理页再 Enter 进入），overlay 保持、composer 保持隐藏；session 打开期间所有键归 session（`handleTeamPickerKey` session 分支优先），重入/点按不会落回 roster 或默认 composer。无 Leader 团队点击 `[TEAM]` 安静停留管理页（Leader 标记即门禁，与 `t` 一致；roster 的 `l` 可补授）。`TestTeamSessionReopenLandsOnLeader` 钉住"重开回 Leader 而非上次成员"，`TestTeamButtonOpensLeaderSession`/`TestTeamButtonSessionKeepsKeysFromChatComposer`/`TestTeamButtonEscapeReturnsToTeamList` 钉住目标态（session active/leader current/composer 隐藏/leader 历史加载/成员栏/键隔离/退出落点）。此变更废弃 §4.2 selection 恢复语义：`WriteSelection` 保留（seam 单向写，切换仍持久化），`restoreSession` 不再消费。受影响测试同步适配：`TestTeamSessionRestoresSelection` → `TestTeamSessionReopenLandsOnLeader`、`TestSessionSelectionFallsBackToLeaderAfterMemberRemoved`/`TestSessionSelectionNoLeaderStaysOnRosterWithReason` → `TestTeamButtonOpensOnLeaderAfterMemberRemoved`/`TestTeamButtonNoLeaderStaysOnManagementPage`、`openRoster` helper 加 session 感知（先 Esc 再 Enter）、cli-researcher 的 entry_test.go 两处断言与 `TestTeamOverlayCloseStopsEveryInstance` 双 Esc。门禁：gofmt clean、`go test ./internal/cli/... ./internal/team/...` 8 包全绿、vet/build exit 0；repolint 仅 `chat_tui.go` function-size 既有基线项（1316/1311，本节点未触碰 chat_tui.go、未加宽 baseline）。

> **入口接缝审计与回归（2026-08-22，cli-researcher）**：对 `[TEAM]` 按钮的打开/关闭两条命令接缝做结构审计——**打开**：鼠标左键命中 `teamButtonHit`（chat_tui.go update）→ `return m, m.onTeamButtonClick()`（P4.1b 接线），`onTeamButtonClick` 建 picker（`NewRegistry(sessions)` 无 factory = 纯状态模式）后 `return p.restoreSession()`，Cmd 即订阅命令（§11.5 事件流 arm 点）；**关闭**：session Esc 走 `handleSessionKey` → `closeSession`（`StopTeam` 同步等 in-flight 落盘），overlay 整体关闭走 `closeTeamOverlay`（`runtime.Close()` 同步停全部遗留实例），键处理返回的 Cmd 恒为 nil——两条路径均无悬挂异步 Cmd。**回归**：新增 `chat_tui_team_entry_test.go` 2 测试——`TestTeamButtonEntryReturnsRestoreCmd` 钉住"点击必须返回订阅命令而非只开 roster + 打开即 leader session（current==firstLeader）"；cmd 契约依赖装配 registry，P4.2 factory 落地前以注入 fake factory 后重调 `restoreSession` 方式断言（注释标注 P4.2 接线依赖，保留契约不放松）；`TestTeamOverlayCloseReturnsNoCmd` 钉住关闭路径（先 Esc 关 session 再 Esc 关 overlay，两段均 cmd nil 且 teamPick 置 nil）。语义变更（selection 恢复移除）后 entry_test.go 断言由 tui-researcher 适配、cli-researcher 复核 import 与编译。门禁：gofmt clean、`go test ./internal/cli/... ./internal/team/...` 8 包全绿（cli 10.6s）、`go vet ./internal/cli/... ./internal/team/...` exit 0、`CGO_ENABLED=0 go build ./...` exit 0；repolint 仅既有 P3 已知 `chat_tui.go` function-size 基线项（1316/1311，多轮记录在案、P4.5 统一处理，entry_test.go 属 `_test.go` 豁免，无新增违规、未加宽 baseline）。

### 11.5 Bubble Tea 事件刷新

runtime 订阅通过 `tea.Cmd` 转换为 `teamRuntimeEventMsg`，由 TUI 主更新循环处理：

```text
runtime event
  -> tea.Cmd
  -> teamRuntimeEventMsg
  -> 校验 (team, memberID, sequence)
  -> 写入对应 context / 更新 snapshot
  -> 当前成员刷新历史，非当前成员增加 unread
```

处理规则：

- 当前成员的 `delta/message/status/error` 立即刷新左侧会话区；
- 非当前成员事件仍写入其独立 context，只更新右侧 unread/status；
- 切换成员时重新读取该成员历史和 runtime snapshot，禁止使用当前成员缓存替代；
- 旧订阅事件若 team/member 或 sequence 不匹配，静默丢弃；
- 关闭 overlay 时取消订阅并关闭事件 channel，防止 goroutine 和消息泄漏。

> **P4.3 实现与验收（2026-08-22，tui-researcher）**：TUI 侧事件接线落地 —— 新文件 `chat_tui_team_events.go`：`teamRuntimeEventMsg{sub, event}` 经 `subscribeTeamRuntime` tea.Cmd 转译并在每次事件后自续挂；`handleTeamRuntimeEvent` 当前成员 error/message/done 事件写/清 session errMsg，非当前成员终态事件（message/done/error/stopped，delta 不计）对会话成员列表内成员计 unread、列表外成员丢弃，team/member 不匹配或零值（closed 流）静默丢弃；`bindSessionSubscription`/`cancelSessionSubscription` 管理订阅。`sessionState` 增 `sub`/`unread` 字段；`enterTeamSession`/`restoreSession`/`stepSession` 均挂/重绑订阅（切换即 Cancel 旧 + Subscribe 新 + 清目标 unread，显示即消费），`Esc`/`Ctrl+C` 走 `closeSession` 取消订阅；`chat_tui.go` Update 增 `case teamRuntimeEventMsg`（与 cli-researcher 协调，2 行最小增量）。`renderTeamSession` 增：session 级 errMsg 行、右侧成员 unread 计数徽标、聚焦 composer 草稿行与操作提示（Enter send/Shift+Enter newline/Ctrl+Up/Down/Tab switch）、浏览态 Enter compose 提示。**验收**：新增 8 个回归测试（chat_tui_team_session_events_test.go：消息事件刷新窗口、非当前成员终态计 unread + delta 不计 + 切换消费、error 显示且 message 清除、closed/foreign/未知成员事件丢弃、关闭后事件丢弃不碰 composer、composer 渲染、真实装配 registry 的切换重订阅（旧 channel 关闭）与关闭取消订阅）；cli+team 全量测试绿，gofmt/vet/`CGO_ENABLED=0 go build` 过；repolint 仅 `chat_tui.go` function-size 既有基线项（P3 已知，P4.3 必要接线 +2 行至 1316，P4.5 统一处理）。待接：Esc/r 生命周期与 k 清理前 `StopTeam` 接线（P4.4，领域原语已就绪）。

### 11.6 生命周期、错误和破坏性操作

```text
Idle
  -> t: Start Leader + Subscribe
  -> Send: Append user + Agent loop
  -> Event: Render/append assistant
  -> Switch: Start or reuse target member + reload context
  -> Esc: cancel subscription + flush + stop/close runtimes
```

- runtime 启动失败：留在会话窗口，显示当前成员错误和 `r` 重试入口；不得回写其他成员状态。
- provider 请求失败：保留 user 消息，追加 error 事件，允许重试；不重复发送已确认的 user 消息。
- `k` 解除 Leader、成员删除或团队清理开始前，必须先关闭会话并停止相关 runtime，避免清理期间继续写 context。
- 点击 `[TEAM]` 直接进入 Leader 会话窗口（`restoreSession` = `firstLeader`），不再恢复上次 selection；无 Leader 团队停留管理页（§11.4 入口状态机对齐）。
- 普通聊天历史与团队成员 context 永不互迁。

> **P4.4 CLI 侧接线收口（2026-08-22，cli-researcher）**：`internal/cli` 四条生命周期接线落地（领域原语来自 P4.4 的 `Registry.Stop/StopTeam/Close`）。① **session Esc/退出**：`closeSession` 取消订阅后调用 `runtime.StopTeam(teamName)`（§11.6 Esc 语义——Stop 内部 `<-done` 等待 in-flight loop 最终落盘，即 flush；窗口启动过的所有成员实例全部停机，上下文留盘）；overlay 整体关闭（`closeTeamOverlay`，`chat_tui_team.go` 唯一 `teamPick=nil` 路径）调用 `runtime.Close()`，覆盖其他团队遗留实例。② **r 重启**：浏览态新增 `r` 键 → `restartSessionTarget` = `Stop(key)`（容忍 `ErrInstanceNotFound`，即装配失败未注册场景）+ `Start(key, spec)`（P4.4 stopped→running 幂等恢复，序号续接不重排）+ 重绑订阅；失败显示 session errMsg 留在窗口（§11.6 r 重试入口语义），成功清 errMsg；输入态 r 仍是普通字母。③ **破坏性操作前停机**：`stopTeamBeforeClear(teamName)` 供 k 解除 Leader（`executeLeaderReset` 在 `ClearTeamTrash` 前）、删除成员（`deleteMember`）、删除团队（`deleteTeam`）三路径调用——StopTeam 失败拒绝操作并显示消息，半清理比拒绝更糟。④ 渲染：浏览态提示行加 `r restart`。**验收**：新增 `chat_tui_team_lifecycle_test.go` 5 测试全绿——`TestTeamSessionCloseStopsTeamRuntimes`（Esc 后 lead+alice 均 stopped，重进恢复 running）/`TestTeamSessionRestartRetriesAssembly`（装配失败无实例→r 重试成功 + 重订阅 + 清 errMsg）/`TestTeamDeleteMemberStopsTeamBeforeClear`（删除成员时遗留 runtime 被停 + 槽位删除）/`TestTeamLeaderResetStopsTeamBeforeClear`（k 全流程：实例停 + 目录清 + leader 标记关）/`TestTeamOverlayCloseStopsEveryInstance`（双团队遗留实例在 overlay 关闭时全部停机）。门禁：gofmt clean、`go test ./internal/cli/... ./internal/team/...` 8 包全绿（cli 8.9s）、`go vet ./internal/cli/` exit 0、`CGO_ENABLED=0 go build ./...` exit 0；repolint 无新增违规（仅 P3 已知 `chat_tui.go` function-size 1316/1311 基线项，本轮未触碰 chat_tui.go、未加宽 baseline）。遗留：session 输入态 `ctrl+c` 停当前成员 in-flight 请求仍为惰性 stub（P4.1 刻意设计，未在本轮接线，避免超出任务边界）。

### 11.7 剪贴板和输入接缝

会话输入框必须纳入 `teamPasteTarget`，覆盖 bracketed paste、Ctrl+V、Shift+Insert、中键/右键粘贴、异步文本剪贴板和图片剪贴板结果。所有粘贴都只能写入当前会话输入缓冲；provider/member picker、成员列表、确认态等非文本状态必须静默丢弃。

现有审计还发现右键粘贴和 `clipboardImageMsg` 可能绕过统一路由。该接缝在 P4 输入节点中一并修复并单独测试，不能以普通 composer 隐藏作为丢弃活动会话输入的理由。

> 接缝状态（2026-08-22，integration-tester-claude）：文本路径（bracketed/右键/Ctrl+V/Shift+Insert 异步）已全部经 `applyComposerPasteCount` + `teamPasteTarget` 收敛；`clipboardImageMsg` 成功路径已加 teamPick 守卫（overlay 打开时图像结果不再写入隐藏 composer，`TestTeamClipboardImageResultWhileOverlayOpen` 钉住），由 test-engineer 全量门禁复验。剩余部分——会话输入框本身纳入 `teamPasteTarget`——属 P4.1 输入节点。
>
> **鼠标接缝收口（2026-08-22，cli-researcher）**：中键/右键粘贴原被 `hideComposer` 一票否决——TEAM 打开且会话 composer 聚焦时右键不触发剪贴板读取、中键不读 selection。新增 `mousePasteAllowed()`（chat_tui_paste.go：composer 可见或 overlay 有活动文本目标 `teamPasteTarget != nil` 时放行，其余模态态维持关闭），替换 `chat_tui.go` 中键（1130）/右键（1146）两处门控；结果仍经 `pasteMiddleClick`/`pasteClipboardText` → `applyComposerPasteCount` → `teamPasteTarget` 收敛进会话草稿，普通聊天路径语义不变。新增 2 测试（`TestTeamMouseRightPasteIntoSessionInput`：右键读剪贴板入草稿 + 浏览态无目标惰性；`TestTeamMouseMiddlePasteIntoSessionInput`：中键读 PRIMARY selection 入草稿），与既有 15 个粘贴测试全绿。注：overlay 请求 MouseModeNone，此修复是终端仍上报点击时的兜底接缝，语义上满足"所有粘贴路径都只能写入当前会话输入缓冲"。

### 11.8 分期与验收门槛

**P4.1 输入与发送接缝**

- 独立会话输入框、Enter 发送、成员切换、消息写入；
- `Registry.Send` 接入当前成员；
- 所有文本/图片/右键粘贴路径隔离；
- fake runtime 单测覆盖发送目标不串线。

> **P4.1 实现与验收（2026-08-22，tui-researcher）**：会话窗口新增独立 composer——浏览态 Enter 聚焦，Enter 发送并清空草稿，Shift+Enter/Alt+Enter 插入换行，Ctrl+Up/Ctrl+Down 与 Tab/Shift+Tab 切换成员（composer 保持聚焦），输入态 ↑/↓ 惰性（防草稿被箭头带走），Esc 两级退出（composer → 浏览态保草稿 → 关窗），Ctrl+C 惰性（P4.2 loop 落地后接停止）；发送经 `agentruntime.Registry` 懒启动（`t` 进入启动 Leader、`restoreSession` 与 `stepSession` 启动目标、发送前再启动，均幂等）+ `Send` 写 user 消息到当前成员 `messages.jsonl`，失败保留草稿并在 session 级 errMsg 显示（独立于 roster 门禁 errMsg）。`teamPasteTarget` 新增 session composer 分支（§11.7 收口：bracketed/Ctrl+V/Shift+Insert/异步文本全部经既有汇聚点落地草稿），浏览态与其余非文本态仍静默丢弃、隐藏 composer 全程隔离。**验收**：新增 9 个回归测试（chat_tui_team_session_composer_test.go：键入发送清空、空发送忽略、Esc 保草稿、多行发送、切换后发送目标跟随不串线、输入态箭头惰性、bracket paste、Ctrl+V 异步、失败保草稿），连同既有 14 个粘贴/会话测试全绿；门禁 gofmt/`go test ./internal/cli/... ./internal/team/...`/vet/CGO build 全过；repolint 仅剩 `chat_tui.go` function-size 既有基线 drift（HEAD 起即存在，干净 worktree 复验，非本节点引入，P4.5 统一处理）。
>
> **P4.1 鼠标接缝（2026-08-22，cli-researcher）**：右键/中键粘贴门控自 `hideComposer` 收窄为 `mousePasteAllowed()`（见 §11.7 接缝状态）——会话 composer 聚焦时鼠标粘贴入草稿、无活动文本目标（浏览态等）惰性、普通聊天路径不变；+2 测试（右键/中键）。门禁：gofmt clean、`go test ./internal/cli/... ./internal/team/...` 8 包全绿（cli 9.3s/team 1.9s）、`go vet ./...` exit 0、`CGO_ENABLED=0 go build ./...` exit 0；repolint 仍为 P3 已知 `chat_tui.go` function-size 基线项（1316/1311，P4.3 接线所致，非本节点引入，未加宽 baseline）。

**P4.2 真实 runtime 与事件**

- provider adapter 和成员 Agent loop；
- `Start/Stop/Send/Subscribe/Observe`；
- `teamRuntimeEventMsg` 实时刷新；
- delta 聚合、最终消息、错误和未读状态。

**P4.3 生命周期与集成**

- 关闭、重启、成员切换和崩溃恢复；
- `k` 清理前停止 runtime；
- 有界事件通道、取消订阅和并发写入测试；
- 更新路线验收矩阵并通过全部工程门禁。

## 12. 节点与工作进度表

| 节点 | 交付内容 | 负责角色 | 状态 | 完成标准 |
|---|---|---|---|---|
| P4.0 | TeamSessionController 与 runtime 接缝设计冻结 | architecture-analyst、cli-researcher | ✅ 已完成（方案拍板） | 接口、状态机、隔离键和错误边界写入本节 |
| P4.1 | 独立会话输入框、发送、成员切换、文本/图片/右键粘贴 | tui-researcher、cli-researcher | ✅ 已完成（2026-08-22，tui-researcher） | 输入不进入普通 composer；user 消息写入正确 member context |
| P4.1b | [TEAM] 入口状态机：点击直接进 Leader 会话（restoreSession=firstLeader，selection 恢复语义移除） | tui-researcher | ✅ 已完成（2026-08-22，tui-researcher） | session.active=true、current=leader、composer 隐藏、leader 历史加载、成员栏可切换（§11.4 记录） |
| P4.2 | 成员 Agent loop、provider adapter、runtime 事件订阅 | plugin-engineer、architecture-analyst | 🟡 核心已落地（plugin-engineer，2026-08-22） | `(team, memberID)` 独立运行；事件可持续产出并持久化 |

> P4.2 实现状态（2026-08-22，plugin-engineer）：`internal/team/agentruntime` 新增 `event.go`（RuntimeEvent + EventSource 有界广播：终态 message/done/error/stopped 不丢、慢消费者仅丢 delta、订阅回放终态、Subscription{C,Cancel} 句柄）+ `member.go`（ProviderFactory 注入装配、MemberRuntime 接口、MemberAgent 单轮流式 loop：role 注入 system prompt、delta 事件流、完整 assistant 原子落盘、message 事件后 cursor+Sequence 同步推进、失败保留 user 消息 + error 事件、ErrBusy 同成员串行、Stop 取消 loop、AuthError 只暴露 provider 名不泄漏 Body）。`adapter.go` 改造：`NewRegistry(store, factory...)` 变参兼容（无 factory = 纯状态模式，P4.1 消费方零破坏）、Start 装配失败显式返回（§11.6 r 重试入口）、Send 委托 runtime、新增 `Subscribe(key)`（未装配返回 `ErrNotAssembled`）、Stop/Close 锁外停 runtime 并关事件源。事件序号经 `SessionCursor.Sequence` 持久化续接（architecture-analyst 已加字段）。测试：event_test 5 + member_test 8 全绿（含 `-race`），既有 adapter_test 兼容。门禁：gofmt/vet/`go test ./internal/team/...` 全过；repolint 无 agentruntime 违规（残留 chat_tui.go function-size 为 P3 已知基线项）。待接：TUI 侧 `Subscribe` 消费（P4.3）、Esc/r 生命周期接线（P4.4）。
| P4.3 | Bubble Tea 实时刷新、delta、错误、未读状态 | tui-researcher、cli-researcher | ✅ 已完成（2026-08-22，tui-researcher） | 当前成员实时显示，非当前成员 unread，不串线 |
| P4.4 | 关闭/重启/清理生命周期与并发保护 | architecture-analyst、plugin-engineer、cli-researcher | ✅ 已完成（2026-08-22：领域侧 architecture-analyst，CLI 侧 cli-researcher） | Esc/r/k/删除路径无 goroutine 泄漏或半清理 |

> P4.4 领域侧实现状态（2026-08-22，architecture-analyst）：`adapter.go` 两处增量 —— ① Start 幂等分支恢复 stopped→running 并落盘（§11.6 r 重启语义：Stop 后再次 Start 复用同一 MemberRuntime，事件源/Sequence 种子不重建，序号跨重启续接不重排）；② 新增 `Registry.StopTeam(teamName)`（§11.6 k/删除/清理前停机原语：锁内收集该团队未停实例，经 `Registry.Stop` 单点路径逐个落盘 stopped + 锁外取消 loop，其他团队与 unknown team 不受影响）。`sessionstore.go` 侧：`SessionCursor.Sequence int64 omitempty` 持久化事件序号（P4.2 已署名）+ 旧 cursor.json（无 sequence 字段）读取为 0 的 §7 兼容测试。测试：`lifecycle_test.go` 8 用例全绿（restart 恢复 running 并落盘 / StopTeam 团队隔离与 unknown no-op / StopTeam 取消挂起 loop 且 EventStopped 送达 / Stop 后 Send 复用 runtime 序号续接 / 并发 Send ErrBusy 且消息保留游标不动 / StopTeam 后重启恢复 / Stop 与 StopTeam 互幂等 / K3 错误事件不含密钥）。门禁：gofmt/vet/`go test ./internal/team/...` 全过、`CGO_ENABLED=0 go build ./...` 过、repolint 无 agentruntime 违规（chat_tui.go function-size 为 P3 已知基线项，cli 线收敛）。
>
> **P4.4 CLI 侧接线（2026-08-22，cli-researcher）**：见 §11.6 记录——`closeSession` 停团队 runtime（Esc/ctrl+c）、overlay 关闭 `Close()`、r 键 Stop+Start+重订阅、k 解除 Leader/删成员/删团队前 `StopTeam`（失败拒绝操作）、浏览态提示 `r restart`；新增 `chat_tui_team_lifecycle_test.go` 5 测试，cli+team 8 包全绿，repolint 无新增违规。遗留：session 输入态 ctrl+c 停 in-flight 仍惰性（未超界）。
| P4.5 | 测试矩阵与六项工程门禁 | test-engineer、integration-tester | 🟡 进行中：① integration-tester 已执行门禁清单（2026-08-22，本终端命令受拒，采用成员独立门禁证据）；② test-engineer 测试先行已落盘（2026-08-22：D1 磁盘级隔离/游标 2 测 + P4.2 落盘后激活 D2 注册表级 Subscribe 契约 3 测，共 5 测 PASS 含 `-race`，TEST_PLAN §6 矩阵 D1-D7）；D3-D7 待 P4.1/P4.3/P4.4 落盘后执行 | gofmt、go test、vet、CGO build、repolint 全通过 |
| P4.6 | 路线回写与最终验收 | leader + 全体相关成员 | 🟡 验收记录已回写（integration-tester，2026-08-22）；入口修复最终验收完成（见下），待 leader 收口 | §9/本表更新为实际结果，遗留风险明确记录 |

> P4.6 最终验收记录（2026-08-22，integration-tester）：基于 P4.1-P4.4 四份正式回报（plugin-engineer 18:24、architecture-analyst 18:35、tui-researcher 18:38/18:44）对 `evidence/p4-acceptance-checklist.md` 逐项只读映射。**C1 输入/发送/粘贴 ✅**：会话独立 composer 收口（Enter 发送当前成员不串线、Shift/Alt+Enter 换行、Ctrl+Up/Down+Tab 切换、Esc 两级退出保草稿），teamPasteTarget 新增 session 分支覆盖 bracketed/Ctrl+V/Shift+Insert/异步文本，非文本态丢弃、隐藏 composer 全程隔离（chat_tui_team_session_composer_test.go 9 测试 + 既有 14 粘贴测试回归）。**C2 runtime 契约 ✅**：有界事件广播终态不丢、慢消费者仅丢 delta、订阅回放终态、Sequence 持久化续接、Send 失败保留 user 消息 + error 事件、ErrBusy 同成员串行、K3 错误事件无密钥（event_test 5 + member_test 8 全绿含 -race + registry_send_isolation_test）。**C3 生命周期 ✅ 领域侧**：Start 幂等恢复 stopped→running（r 重启语义，序号续接不重排）、Registry.StopTeam 为 k/删除/清理前停机原语（锁内收集锁外停、团队隔离、互幂等）、Stop 后 Send 复用 runtime、并发 Send 消息保留游标不动（lifecycle_test 8 用例）；CLI 侧 Esc/r 键接线与 k 清理前调 StopTeam 标注待接（领域原语已就绪，P4.4 注记同）。**C4 旧数据兼容 ✅**：SessionCursor.Sequence omitempty 兼容旧 cursor.json 读 0，legacy teams.json/role==leader/proxy host-port 迁移由 P1 测试覆盖。**C5 工程门禁 ⚠️ 授权阻塞**：本终端六门禁命令两次被用户拒绝（按规则不重试、不代跑），门禁结论采用成员独立复验证据——gofmt clean、`go test ./internal/cli/... ./internal/team/...` 8 包全绿（architecture-analyst 18:35 与 tui-researcher 18:44 各自独立复验）、`go vet ./...` exit 0、`CGO_ENABLED=0 go build ./...` exit 0；repolint 仅剩 `internal/cli/chat_tui.go` function-size 既有基线项（P3 已知 1314/1311，P4.3 必要接线 +2 至 1316，未加宽 baseline，属 cli 线收敛）。遗留风险：①CLI 侧 Esc/r 与 k 清理前 StopTeam 接线（P4.4 注记）；②chat_tui.go function-size 收敛；③崩溃窗口重放为设计语义（消息已落 cursor 未进时重放已完成消息，plugin-engineer 评估）；④P4.6 最终收口由 leader 完成。
>
> **P4.6 入口状态机最终验收（2026-08-22，integration-tester）**：实现落盘（tui-researcher 19:13 + cli-researcher 19:13:38）后只读磁盘审计——`onTeamButtonClick`（chat_tui_team.go:99）建 picker（NewRegistry 变参纯状态模式）→ `reload` → `restoreSession`（chat_tui_team.go:130）一律 `current=firstLeader()`：session.active=true、current=leader、members/focus 构建、startSessionTarget 懒启动、bindSessionSubscription 订阅；无 leader 团队 return nil 安静停留管理页（与 t 门禁同源）。目标态核对：普通 composer 隐藏 ✅（hideComposer 因 teamPick != nil，chat_tui.go:2329）；history 从 (teamName,current) 独立 context 加载 ✅（renderTeamSession → sessions.Messages）；顶栏成员栏可切换 ✅（右栏全 roster + unread 徽标，↑/↓/j/ctrl+up/down/tab 切换重订阅）；Esc 落回团队列表 ✅；键隔离 ✅（session 打开期间键全归 handleSessionKey，不落回 roster/composer）。截图 issue1.jpg 无法核对（Read 工具不支持该 JPEG 格式、格式转换被用户拒绝），以 test-engineer 验收矩阵（点击 TEAM→session.active、leader current、composer 隐藏、leader history/成员栏渲染、已有 session 恢复、无 leader 错误）+ tui-researcher 3 入口回归测试（chat_tui_team_entry_state_test.go）为核对依据，逐项 ✅。门禁：本终端 gofmt -l clean；`go test` 命令被用户拒绝（不重试不代跑），采用成员独立复验（tui-researcher 19:13 与 cli-researcher 19:13:38 各自独立跑 cli+team 8 包全绿、vet exit 0、CGO build exit 0、repolint 仅 P3 已知 chat_tui.go function-size 基线项未加宽）。
