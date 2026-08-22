# 技术路线 —— TEAM 独立 Agent 会话与 Leader 生命周期

> 状态：**实现完成，已通过针对性验收**（2026-08-22）。全量 CLI 仍受当前沙箱禁止 IPv6 `httptest` 监听的环境限制；该失败与本次改动无关。
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
> **test-engineer 独立验收（2026-08-22 第三轮，test-engineer-claude）**：`TEAM_SESSION_TEST_PLAN.md` §1 矩阵 A1-A9/B1-B6/C1 全部 ✅。本会话补 5 个缺口测试：`TestProxyAcceptsIPPortAddresses`（A3 正向）、`TestClearTeamIdempotent`（A9 幂等）、`TestTeamEnterSessionRefusedWithoutLeader`（B2 无 Leader 分支）、`TestSessionSelectionFallsBackToLeaderAfterMemberRemoved` 与 `TestSessionSelectionNoLeaderStaysOnRosterWithReason`（A7 回退/停留分支）。独立重跑全部门禁：`go test ./internal/team/... ./internal/cli/...` 全绿、gofmt / go vet / CGO_ENABLED=0 build / repolint（1276 baseline）clean。实现者记录的 IPv6 httptest 失败（`TestFetchModelListCompatWalksCandidates`）在当前环境**已通过**，未见复现。§5 既有测试受影响清单已全部处理（`TestTeamToggleLeaderPersists` 由 `TestTeamRosterLeaderToggleAndSessionGate` 重写承接）。

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
