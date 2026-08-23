# 改造方案 —— TUI 与成员 Agent 解耦

> 状态：**已拍板；R0、R1、R2.1、R2.2 完成，下一节点 R2.3（主 composer 接管）（2026-08-23）**。本文档只描述改造方向、落点与节点，不描述任何未实施的东西为已实现。
> 权威优先级：用户拍板 > `docs/team-mcp-port/TASK.md` > 本文档 > 实现代码注释。
> 关联：`TEAM_SESSION_TECHNICAL_ROUTE.md`（现行实现的路线，其 §11 P4 部分被本方案取代）、
> `TEAM_MEMBER_POOL_AGENT_CONFIG_ROUTE.md`（成员池/凭据，本方案的输入，不改）。
> 进度只在 §7 更新；每完成一个节点，改该节点的状态与证据列，不改其他章节。

---

## 1. 问题定性

用户目标：**TUI 只是可视化与交互外壳，通过接入不同 Agent 后端显示不同成员的上下文**——
进团队默认显示 leader 的会话与历史，`[ TEAM ]` 旁列出成员按钮，点击即把当前会话切到该成员的
Agent（类比 mult_agent_mcp 的 TUI 与 claude code / codex 解耦）。

现状不是"接线没接完"，而是**接错了层**：

| 应接的边界 | 实际接的边界 |
|---|---|
| `control.SessionAPI`（Agent 边界：工具、回环、memory、skill、hook、trajectory） | `provider.Provider`（模型边界：一次 chat completion） |

证据锚点：

- `internal/team/agentruntime/member.go:166` 直接 `m.prov.Stream(ctx, *req)`，绕过 `control` 与 `agent`。
- `member.go:213` `buildRequest` 只设 `Messages`；`provider.Request.Tools`（`internal/provider/provider.go:217`）**从未被设置**，且无 tool-call 回环 → 成员"Agent"没有工具、没有 memory/skill/hook/trajectory。
- `internal/cli/chat_tui_team.go:110` `agentruntime.NewRegistry(sessions)` 是唯一生产构造点且**未传 factory** → `rt == nil`，`Send` 只 append，`Subscribe` 返 `ErrNotAssembled`。用户敲字后无补全、无事件、无报错。
- `internal/cli/chat_tui_team_render.go:456` `renderTeamSession` 把会话画成一个 `choicePanelStyle` 面板，左栏混着 `Role/Leader/Status` 与最多 `sessionHistoryLines = 5` 行截断历史；主 transcript 始终还是原来那个普通会话。
- `internal/cli/chat_tui_team.go:20` `appendTeamButton` 只拼一个字面量 `"[ TEAM ]"`；成员按钮从未实现。

因为选了"在前端里再造一个前端"，才必然派生出自研事件总线、自研持久化、自研面板渲染，
以及它们各自的缺陷（§6 逐条映射）。

---

## 2. 可复用的既有接缝（不需要新发明）

| 接缝 | 位置 | 能力 |
|---|---|---|
| TUI 只依赖接口 | `internal/cli/chat_tui.go:53` `ctrl control.SessionAPI` | `internal/control/port.go:261`，13 个子端口；`Controller` 只是一个实现 |
| 单一事件管道 | `chat_tui.go:232` `eventCh` → `waitForAgentEvent` → `ingestEvent` | 谁被绑上就往同一通道写，渲染代码不动 |
| 后端热插 | `chat_tui.go:1943-1983` `modelSwitchMsg` 处理分支 | `m.ctrl = msg.ctrl` + label/commands/skills/host 同步 + `oldControllers` 延后拆除 + `followSessionLease()` |
| 后端装配 | `chat_tui.go:380` / `internal/cli/cli.go:1227` `buildController` | `setupQuietProfile(ctx, ModelRef, maxSteps, false, sink, overrides)` |
| 会话重绑 | `internal/control/controller.go:3571` `Controller.Resume(s, path)` | 重绑 executor session、sessionPath、checkpoints、goals、guardian、recovery、session temp |
| 会话载入 | `cli.go:457` `loadResumableSession(path)`（启动期用法见 `cli.go:640-655`，含 `leases.Rebind`） | 含 `agent.ErrSessionLeaseHeld` 占用保护 |
| transcript 重建 | `newChatTUI` 由 `ctrl.History()` 播种 | 换后端后按同法重播即可 |
| 事件 sink | `internal/event/event.go:868` `Sink`（单方法）+ `FuncSink` | 可在 cli 层包一层打标签 |
| 宽度度量 | `internal/cli/box.go:14` `visibleWidth()` / `padRight()` | ANSI + CJK/emoji 正确 |
| 共享插件宿主 | `internal/boot` `Options.SharedHost *plugin.Host` | 多后端并存时避免 N 份 MCP 子进程 |

---

## 3. 目标架构

```text
              ┌──────────────── chatTUI（唯一可视化外壳）────────────────┐
              │  transcript · composer · statusline（含成员按钮条）      │
              └───────┬─────────────────────────────┬───────────────────┘
                      │ m.ctrl control.SessionAPI   │ memberEventCh
                      ▼                             ▲
        ┌─────────────────────────────┐             │ memberEvent{id, event.Event}
        │ teamBackends（internal/cli）│             │
        │  (team, memberID) → SessionAPI            │
        │  懒建 · 热插 · 空闲回收      ├─────────────┘
        └──────┬──────────────────────┘
               │ buildMemberController
               ▼
        boot.Build(Options{Model, Sink, SessionDir, SharedHost, …})
               │
               ▼  Resume(loadResumableSession(memberSessionPath))
        control.Controller —— 完整 Agent：工具/回环/memory/skill/hook/trajectory

        internal/team（纯数据，不 import control）
          团队/成员/Role/Leader/AgentUserRef/成员会话文件路径
```

三条硬约束：

1. **分层**：`tools/repolint/layers.go:71` 规定只有 frontends 与 hosts 可 import `internal/control`。
   因此 `(team, memberID) → SessionAPI` 的绑定层**必须落在 `internal/cli`**，`internal/team` 只出纯数据。
2. **缓存**：成员 system prompt 的 role 注入只在**装配时**发生，会话中途绝不改前缀（`REASONIX.md` cache-first）。
3. **单写者**：一个成员会话文件同时只被一个后端持有（lease 保证），切换时 lease 跟随。

---

## 4. 关键设计决策

**D1 —— 后端粒度：一成员一 `SessionAPI`，懒建、热插、空闲回收。**
成员各自的 `AgentUserRef` 决定 provider/baseURL/model/effort，一个 controller 无法同时服务两套模型，
所以不能"单 controller + 每次 Resume"。复用 `modelSwitchMsg` 的换手形状。

**D2 —— 绑定层落 `internal/cli`。**
`internal/team` 新增的只是纯数据与路径计算，不引入任何 `control` 依赖（否则 repolint 分层直接拒绝）。

**D3 —— 事件：单通道 + 成员打标，替换自研 `EventSource`。**
每个成员后端拿到 `event.FuncSink(func(e event.Event){ ch <- memberEvent{id, e} })`，
`ch` 是**一条**共享通道。于是：

- 保住既有"单 `waitForAgentEvent` goroutine"不变式，不再有第二条总线；
- 事件天然带成员归属：当前成员 → `ingestEvent`；非当前成员的终态 → unread 计数，不污染 transcript；
- `agentruntime/event.go` 的有界广播、回放环、`evictDelta` 全部不再需要。

**D4 —— 历史走 `Resume`，不走 `AdoptHistory`。**
`cli.go:1351` 的 `adoptCarriedHistoryPreservingProfileAndGrants` 最终调 `c.AdoptHistory(carry, path)`，
语义是**替换**历史——用在成员切换上会清空该成员的文件。成员切换必须是
`loadResumableSession(memberPath)` + `ctrl.Resume(s, path)`，随后按 `ctrl.History()` 重播 transcript。

**D5 —— 成员历史 = 真正的 Reasonix 会话文件，废除自造 `messages.jsonl`/`cursor.json`/`state.json`。**
落在会话目录（`SessionDir`）下而非 cwd，于是：checkpoint/rewind/fork/compact/trajectory 全部免费获得；
`context/`、`session/` 不再出现在仓库根（本方案顺带闭合该缺陷）。`internal/team` 只记录路径。

**D6 —— 共享插件宿主。**
N 个存活后端否则会拉起 N 份插件/MCP 子进程。用 `Options.SharedHost`，并对存活后端数设上限 +
空闲回收（超限时停最久未用的成员，其历史在磁盘上，再次切回重建）。

**D7 —— 切换门禁。**
`runtimeSwitchBusy()`（`chat_tui.go:457`）已有判据：当前成员回合运行中/待审批时拒绝切换并提示，
不允许在 in-flight 时换 `m.ctrl`。

---

## 5. 阶段拆解

### R0 —— 止血（与架构改造解耦，可独立先合）

| 项 | 内容 |
|---|---|
| R0.1 | `session/agent_designed_team.json` 从版本库撤出（`git rm --cached`）；`context/`、`session/` 移出仓库根；store 改根到 `.reasonix/team` 并用双向物理位置测试钉住（**不加 `.gitignore` 兜底**，理由见 §10.1） |
| R0.2 | repolint 回绿：`internal/cli/chat_tui.go` function-size 1316 → ≤1311（抽函数，**不动 baseline**） |
| R0.3 | `chat_tui_team_render.go` 174/383/505 三处 `utf8.RuneCountInString` → `visibleWidth`；`truncateRunes` 按显示宽度截断 |

**完成标准**：`git status` 干净无运行态文件；`go run ./tools/repolint` exit 0 且 baseline 未加宽；
截图中的 `│` 列对齐，中文行不溢出。
**回滚**：三项互不依赖，逐项 revert。

### R1 —— 成员后端抽象

| 项 | 内容 |
|---|---|
| R1.1 | `internal/team`：`MemberBinding{Team, MemberID, Role, Leader, AgentUserRef, SessionPath}` + 路径计算；纯数据，零 `control` 依赖 |
| R1.2 | `internal/cli`：`memberEvent{id, event.Event}` + 打标 sink；`teamBackends` 注册表（懒建/热插/空闲回收/上限） |
| R1.3 | `internal/cli`：`buildMemberController(binding)` —— `AgentUserRef` → `agent_users.json` 取 provider/baseURL/model/effort/key → `boot.Build`（自带 sink + `SharedHost` + `SessionDir`）→ `loadResumableSession` + `ctrl.Resume`；Role 注入 system prompt（装配时一次）。**已拆为 R1.3a（provider 解析，已完成）与 R1.3b（boot.Build 装配 + Resume + Role 注入，阻塞于 §8-8 拍板）** |

**完成标准**：两个成员共用同一 `AgentUserRef` 时 runtime/历史/lease 完全独立；
成员后端具备工具（`provider.Request.Tools` 非空，effect 测试在 `boot.Build` 真实装配上断言）；
凭据不进事件、不进渲染、不进报告（K1/K2 延续）。
**回滚**：新增文件删除即回滚，旧路径仍在。

> **R1.3 接缝（2026-08-22 勘测完成，R1.3a 已实施）**：
> `setupQuietProfile(ctx, modelName, …)`（`cli.go:386`）只是 `boot.Build` 的薄封装。成员自定义
> provider/baseURL/model/key 的注入点是 **`boot.Options.ProviderResolver`**；`boot.go:427` 起
> `entryResolver := opts.ProviderResolver` 对**每个 ref 都是权威**，`resolveModelEntry` →
> `syntheticEntryFromResolver` 会按 `Catalog()` 的 Descriptor 合成 `config.ProviderEntry`。
> 所以自定义 resolver 只需提供一条 Descriptor（ref 用 `name/model` 形状）+ `Resolve`。
> kind/endpoint 走**已存在的** `team.ResolveProvider`（`provider_options.go:49`）。
> **密钥不能经 `config.ProviderEntry`**：`resolvedAPIKey` 只从 `APIKeyEnv` 读环境变量
> （`config.go:2147-2155`），把池内明文 key 导出到进程环境会跨成员泄漏——直接进
> `provider.New(kind, provider.Config{APIKey: …})`，即 `boot.NewProviderWithProxy` 内部做的事。
>
> **R1.3b（2026-08-23 已实施）**：Role 注入按 §8-8 选项 (a) 落地为
> `boot.Options.SystemPromptIdentity`，折点在 output style 之后、core policies 之前。
> 成员后端复用 `cliProfileBuildOptions` 继承本次启动的权限/目录/工作区，只覆盖
> Model / ProviderResolver / Sink / SystemPromptIdentity；会话文件已存在则 `Resume`，
> 首次进入则 `SetSessionPath`（缺文件是空历史，不是损坏）。

### R2 —— 会话切换

| 项 | 内容 |
|---|---|
| R2.1 | `switchTeamMember(id)`：门禁 `runtimeSwitchBusy()` → 取/建后端 → `m.ctrl = backend` → 由 `ctrl.History()` 重建 transcript → `followSessionLease()` → label/commands/skills/host/modelRef 同步 → 旧后端进 `oldControllers` 或保留在注册表 |
| R2.2 | 进团队默认落 leader（`firstLeader()` 已有）；无 leader 停在管理页并说明原因 |
| R2.3 | 主 composer 接管输入：删 `p.session.buf` 迷你 composer，粘贴/图片/右键路径回归既有汇聚点；Esc 语义（会话 → 管理页 → 关窗） |
| R2.4 | `/model` 在团队会话内改的是**当前成员用哪个模型**：选中模型后写回该成员的 `AgentUserRef`（`agent_users.json`），再按 R2.1 重建该成员后端；不在团队会话内时语义不变（拍板 §8-5） |

**完成标准**：点击成员 → 主 transcript 显示该成员历史，composer 发给该成员，回复流式进主区域；
切回上一个成员历史完整；in-flight 时切换被拒并提示。
**回滚**：保留 `renderTeamSession` 旧分支于开关后，一次提交内可切回。

### R3 —— 状态栏成员按钮

| 项 | 内容 |
|---|---|
| R3.1 | `appendTeamButton` 扩为按 roster 生成按钮条（含 leader 项），当前成员高亮 |
| R3.2 | 命中测试复用 `ansi.Strip` + `visibleWidth`（`teamButtonHit` 已有形状），支持窄屏折行；unread 徽标 |

**完成标准**：鼠标点击任一成员按钮完成 R2.1 切换；窄屏不错位；无成员时只显示 `[ TEAM ]`。
**回滚**：按钮条退回单按钮。

### R4 —— 拆除平行栈

| 项 | 内容 |
|---|---|
| R4.1 | 删 `internal/team/agentruntime/{event.go,event_test.go,member.go,member_test.go,lifecycle_test.go,registry_subscribe_test.go,registry_send_isolation_test.go}` |
| R4.2 | 删 `internal/cli/chat_tui_team_events.go` 及其测试 |
| R4.3 | `renderTeamSession` 退化为成员选择/状态展示，历史交回主 transcript |
| R4.4 | `sessionstore.go` 的 `messages.jsonl`/`cursor.json`/`state.json` 与 `.trash` 语义按 D5 收敛；`Registry` 降级为 binding 注册表或删除 |

**完成标准**：`grep -rn "EventSource\|MemberAgent\|teamRuntimeEventMsg" internal/` 为空；
删除的测试覆盖点在 R1/R2 的新测试里有对位（逐条列出映射，不允许净减覆盖）。
**回滚**：删除是最后一步，R2/R3 验收通过后才执行。

### R5 —— 遗留缺陷清理

| 项 | 内容 |
|---|---|
| R5.1 | AgentType 白名单真正实现（`validateAgentType` 注释把它推给"caller"而 caller 不存在）；清理 `team.json` 里的 `"agent_type":"c"` |
| R5.2 | 陈旧 `state.json`（残留 `"running"`）随 D5 一并消失或开窗时校正 |
| R5.3 | `Snapshot()` 持 `m.mu` 做磁盘 I/O —— 随 `member.go` 删除消失 |
| R5.4 | 会话内 `ctrl+c` 停 in-flight：回归主 composer 的既有取消路径 |
| R5.5 | `AGENTS.md` 抛弃（拍板 2026-08-22：团队协作指令改用共享指令形式，不进仓库指令层级）—— 已 `git rm`，内容可从 `43ddaa112` 取回 |

### R6 —— 门禁与验收

```bash
gofmt -l .
go vet ./...
CGO_ENABLED=0 go build ./...
go test ./internal/cli/... ./internal/team/... -count=1
go test ./internal/team/... ./internal/cli/ -race -count=1
go run ./tools/repolint          # 必须 exit 0，baseline 不得加宽
make lint
```

**铁律**：任何门禁受环境限制必须记录真实阻塞，不得改写为通过；不得以他人复验代替本终端执行
（本轮 `TEAM_SESSION_TECHNICAL_ROUTE.md` §12 P4.6 "C5 授权阻塞…采用成员独立复验证据" 是反面样本）。

---

## 6. 缺陷 → 节点映射

> R0 只闭合了落盘位置、门禁与渲染三类；`agentruntime` 的四个并发/生命周期 P0/P1
> **仍然存在**，它们随 R4.1 删除平行栈一并消失。表内逐条标注了实际状态，不得据本表
> 推断"已修"。

| 缺陷 | 严重度 | 闭合节点 | 闭合方式 |
|---|---|---|---|
| 生产路径无 ProviderFactory，会话发消息无任何反应 | P0 | R1.3 + R2.1 | 走 `boot.Build`，装配即完整 Agent。**未修** |
| `context/`、`session/` 落仓库根，`session/` 已进 git | P0 | R0.1 | **已闭合**：store 改根 `.reasonix/team` + 撤出版本库 + 双向物理位置测试 |
| `event.go:162` DATA RACE（`nextSeq` 锁外自增，`-race` 实测） | P0 | R4.1 | 删除自研广播器，回归 `eventCh`。**R0 未修**：现有 `-race` 全绿只因无测试并发 `Publish`，缺陷仍在 |
| `Subscribe` after `Close` panic（nil map，探针实测） | P0 | R4.1 | 同上。**R0 未修** |
| 终态事件对已有订阅者永久丢失（`flushLocked` 只在 `Subscribe` 调用，探针实测） | P1 | R4.1 | 同上。**R0 未修** |
| `Registry.Close` 跳过 stopped 实例 → 事件源永不关闭（探针实测） | P1 | R4.1/R4.4 | 同上。**R0 未修** |
| `evictDelta` 持锁阻塞风险 + 终态事件重排 | P1 | R4.1 | **阻塞风险已在 R0.2 lint 收尾时闭合**（接收改非阻塞 `select`+`default`，不再可能持源锁等待）；**重排仍在**，随 R4.1 消失 |
| **`EventDone` 先于最终持久化**（`run()` 的 defer 在 publish done 之后才 `persistSeq`）：消费者以 done 为结束信号即与 runtime 的收尾写入竞争 | P1 | R4.1 | R0 期间由 `t.TempDir()` 清理失败暴露（`unlinkat …/context/t/m: directory not empty`）。R0 只让测试自己 `defer r.Close()` 收尾（`Stop` 会 `<-done` 等待）；**新设计必须保持"不得以 done 作为可拆除信号"**，见 D3 |
| 面板宽度按 rune 计（截图竖线错位） | P1 | R0.3 | **已闭合**：`padColumn`/`truncateCells` 按终端格计，2 个回归测试 |
| 成员"Agent"无工具/memory/skill/hook/trajectory | P0 | R1.3 | **已闭合（装配侧）**：`newMemberBackendBuilder` 走 `boot.Build`，成员即完整 Agent；resolver catalog 声明 `Tools`。端到端可见性待 R2.1 接进 TUI |
| `[ TEAM ]` 旁无成员按钮 | P1 | R3 | 按钮条。**未修** |
| 会话窗口不是会话（属性列表 + 5 行截断） | P1 | R2.1/R4.3 | 历史交回主 transcript。**未修** |
| repolint 红（HEAD 1314、工作区 1316；`240d89d86` 为 clean 1276） | P1 | R0.2 | **已闭合**：clean(1275)，baseline 未加宽 |
| AgentType 白名单未实现 | P2 | R5.1 | 落地白名单。**未修** |
| 陈旧 `state.json` / `Snapshot()` 持锁 I/O / `ctrl+c` 空实现 | P2 | R5.2-R5.4 | 随拆除消失或回归既有路径。**未修** |
| `make lint` 15 项（含外部团队存量 14 项 + 本轮新增 1 项） | P1 | R0.2 收尾 | **已闭合**：全部修掉而非放行，`make lint` 0 issues。详见 §10 v0.2 |

---

## 7. 工作进度表

> **每完成一个节点只改本表的"状态"与"证据"两列**，其余章节不动；状态取值
> `未开始` / `进行中` / `待验收` / `已完成` / `阻塞`。阻塞必须写真实原因。

| 节点 | 内容 | 依赖 | 状态 | 证据 |
|---|---|---|---|---|
| R0.1 | 运行态文件撤出版本库 + 落 `.reasonix/team` | — | **已完成** | `sessionstore.go` 改 `NewFileStore(TeamRoot(...))`；新测试 `TestSessionPathsStayUnderTeamRoot` 钉住物理位置（正/反双向）；`git rm --cached session/agent_designed_team.json`、删仓库根 `context/`+`session/`；`registry_send_isolation_test.go` / `sessionstore_test.go` 路径断言随迁 |
| R0.2 | repolint 回绿（不加宽 baseline） | — | **已完成** | 从 `chat_tui.go:update` 抽出 `mouseCopyOrPaste` 到新文件 `chat_tui_mouse.go`（27 行 → 8 行调用；`chat_tui_paste.go` 已 781 行故不落该文件）；`go run ./tools/repolint` = `clean (1275 baselined findings)`，`baseline.json` 零改动 |
| R0.3 | 面板宽度改 `visibleWidth` / 显示宽度截断 | — | **已完成** | 3 处 `utf8.RuneCountInString` → 新 `padColumn`（`visibleWidth`）；`truncateRunes` → `truncateCells`（`ansi.Truncate`，含 CJK 双宽）；删 §5 站点手写省略号与 `len()` 判据；新测试 `chat_tui_team_render_test.go` 2 个（`padColumn`/`truncateCells` 单测 + 分隔线同列断言），已用临时回归探针验证二者确实能抓住（`divider at column 48 … want 37`） |
| R1.1 | `internal/team` MemberBinding 纯数据 + 路径 | — | **已完成** | `internal/team/memberbinding.go`：`MemberBinding`、`MemberSessionFile`（走 `validateSessionKey`，成员无法命名越界文件）、`Bindings`/`Binding`（收口 AgentUserRef/AgentType 的团队默认回退，调用方不再各自实现）。零 `control` 依赖。测试 `memberbinding_test.go` 2 个（越界键拒绝 + 默认回退/覆盖/leader/未知团队与成员错误路径） |
| R1.2 | `memberEvent` 打标 sink + `teamBackends` 注册表 | R1.1 | **已完成** | `internal/cli/team_backends.go`：`memberEvent{member, ev}` + `memberSink`（阻塞发送，与主路径 `eventSink` 同语义，靠 1024 缓冲而非丢弃策略）；`teamBackends` 懒建/复用/LRU 上限 4/`release`/`releaseTeam`/`closeAll`，assembly 失败不留痕以便重试。测试 5 个（懒建幂等、团队参与身份、失败不注册、LRU 只淘汰非当前成员、三条 release 路径各关一次） |
| R1.3a | 成员 provider 解析（AgentUserRef → provider） | R1.1 | **已完成** | `internal/cli/team_backend_build.go`：`memberProviderResolver` 单条目 catalog + `Resolve`。走 `team.ResolveProvider` 定 kind/endpoint（不猜端点）；**密钥不经 `config.ProviderEntry`**（该类型只能从 env 解析 key，把池内明文 key 导出到进程环境会跨成员泄漏），直接进 `provider.New`。catalog 声明 `Tools: true` —— 成员是完整 Agent 而非裸补全。测试 1 个（ref 形状/描述符/tools/默认 effort/网关与官方双路由/per-call effort 覆盖/非法 provider 装配即拒） |
| R1.3b | `boot.Build` 装配 + `Resume` + Role 注入 | R1.3a | **已完成** | 拍板取 §8-8 选项 (a)：`boot.Options.SystemPromptIdentity`（装配期一次，折在 output style 之后、core policies 之前——"replace" 风格删不掉它，而策略/环境/记忆仍作用于它；空值零插入）。`internal/cli/team_backend_build.go`：`memberSystemPromptIdentity`（团队/成员/Role，Role 空显式写 not configured；leader 不进 prompt——它是注册表事实）、`memberProxySpec`（团队关代理 = 显式 `ModeOff`，不回落环境）、`newMemberBackendBuilder`（复用 `cliProfileBuildOptions` 继承本次启动的权限/目录/工作区，仅覆盖 Model/ProviderResolver/Sink/Identity）、`bindMemberSession`（已有文件 `Resume`，首次进入 `SetSessionPath`，缺文件是空历史不是损坏）。**cache 守卫**：`internal/boot/effect_test.go` 新增 effect 测试，在**真实 `boot.Build` + provider 边界**断言 identity 落在精确插入点、跨两轮前缀字节一致、空值零插入。另 4 个 CLI 测试（identity 文案/proxy 三态/装配四条拒绝路径） |
| R2.1a | `switchTeamMember` 热插 + transcript 重建 + 事件归属 | R1.3 | **已完成** | `internal/cli/chat_tui_team_switch.go`：`switchTeamMember`（`runtimeSwitchBusy()` 门禁 → `store.Binding` → `teamBackends.bind` → `bindBackend`，拒绝路径写 session errMsg 且保持原绑定）；`bindBackend` 镜像 `modelSwitchMsg` 分支的收尾（label/modelRef/commands/skills/host/lease/effort 同步 + 清 transcript + 按 `transcriptSourceReplayBundle` 重放该成员 `History()`，形状照 `branch.go:123 replayActiveBranch`）；`waitForMemberEvent`/`memberEventMsg`/`handleMemberEvent`（**单 goroutine 单通道**：绑定成员 → `ingestEvent`，非绑定成员仅终态计 unread，delta 不计）。`chatTUI` 新增 `teamBackends`/`memberEvents` 两个字段。测试 3 个（切换重绑+重放+旧成员历史不残留、未知成员拒绝且保持原绑定、事件归属三态含 delta 不计与绑定即消 badge） |
| R2.1b | 生产接线：`cli.go` 建 registry + `update` 分支 | R2.1a | **已完成** | `bindTeamBackendSeam(maxSteps, overrides)`（`cli.go:1263`，紧跟 `bindRuntimeRebuilder`）装通道（缓冲 `memberEventBuffer = 1024`，与主 `eventCh` 一致）+ 选项模板（`cliProfileBuildOptions` 继承权限/目录/工作区，model 与 sink 由成员 builder 逐成员覆盖）；`bindTeamBackends(store)` 在 `onTeamButtonClick` 里用 overlay 自己的 store 建注册表，seam 未装时为惰性 no-op（非交互宿主/测试）；`update` 新增 3 行 `case memberEventMsg` 分支（repolint function-size 仍 clean，未动 baseline）。测试 2 个（seam 装通道容量+选项继承 maxSteps+无 seam 惰性、`Update` 真的路由到 member handler） |
| R2.2 | 进团队默认 leader；无 leader 停管理页 | R2.1 | **已完成** | `restoreSession` → `openLeaderSession()`（只建会话状态并返回要绑的 leader；无 leader 返 `""` 安静停管理页），`onTeamButtonClick` 据此调 `m.switchTeamMember(leader)`——**进入即绑 leader 的完整 Agent 后端**。`handleSessionKey`/`sessionInputKey`/`stepSession` 由 `*teamPicker` 函数改为 `*chatTUI` 方法，`stepSession` 现在**导航即绑定**（roster 高亮与 `m.ctrl` 不可能不一致）。新增 `chatTUI.ambient` + `unbindTeamMember`：首次绑成员时保存聊天自身后端，`closeSession` 归还——否则绑过成员后普通会话再也回不去。`bindTeamBackends` 改为幂等（重开 overlay 保留已装配后端，不再孤立其 lease）。测试 2 个（进入即绑 leader、transcript 显示其历史、重开保留注册表；关闭归还聊天后端、成员后端仍在、再进复用） |
| R2.3 | 主 composer 接管，删迷你 composer，粘贴回归 | R2.1 | 未开始 | |
| R2.4 | `/model` 写回当前成员 `AgentUserRef` 并重建后端 | R2.1 | 未开始 | |
| R3.1 | 状态栏成员按钮条（含 leader，当前高亮） | R2.1 | 未开始 | |
| R3.2 | 命中测试 + 窄屏折行 + unread 徽标 | R3.1 | 未开始 | |
| R4.1 | 删 `agentruntime` 自研事件/成员 loop 及其测试 | R2 R3 验收 | 未开始 | |
| R4.2 | 删 `chat_tui_team_events.go` | R2 R3 验收 | 未开始 | |
| R4.3 | `renderTeamSession` 退化为成员选择 | R2.1 | 未开始 | |
| R4.4 | `sessionstore` 收敛 / `Registry` 降级 | R4.1 | 未开始 | |
| R5.1 | AgentType 白名单 + 脏数据清理 | — | 未开始 | |
| R5.2 | 陈旧 `state.json` 处置 | R4.4 | 未开始 | |
| R5.3 | `Snapshot()` 持锁 I/O | R4.1 | 未开始 | |
| R5.4 | 会话内 `ctrl+c` 停 in-flight | R2.3 | 未开始 | |
| R5.5 | `AGENTS.md` 处置 | 用户拍板 | **已完成** | 拍板抛弃，改用共享指令形式；`git rm AGENTS.md`（`43ddaa112` 里可取回）。`internal/instruction` 不再加载它，`REASONIX.md`+`CLAUDE.md` 不受影响 |
| R6 | 六项工程门禁 + `-race` + `make lint` 全过 | 全部 | 部分（R0 范围内已过） | R0 收尾实跑：`gofmt -l` clean · `go vet ./...` exit 0 · `CGO_ENABLED=0 go build ./...` OK · `go test ./...` **全量 exit 0 零失败** · `go test ./internal/team/... ./internal/cli/ -race` 全绿 · `make lint` **0 issues** · repolint clean(1275)。R1 起每节点重跑 |

---

## 8. 风险与未决

| # | 风险 | 处置 |
|---|---|---|
| 1 | N 个存活后端的内存与子进程成本 | `SharedHost` + 存活上限 + 空闲回收（D6）；上限值待实测定 |
| 2 | 删除 `agentruntime` 约 1300 行既有绿测试 | R4.1 完成标准要求逐条列出覆盖对位，不允许净减；先加新测试再删旧代码 |
| 3 | 成员 system prompt 注入影响缓存前缀 | 只在装配时注入（D2/约束 2）；用 `internal/boot/effect_test.go` 模式在 provider 请求边界断言前缀稳定 |
| 4 | 会话文件 lease 冲突（同一成员被两处打开） | `leases.Rebind` 已有 `ErrSessionLeaseHeld`；切换时明确拒绝并提示，不静默夺锁 |
| 5 | 成员切换与 `/model` 切换语义重叠 | **已拍板（2026-08-22）**：成员切换换的是"哪个成员"，`/model` 换的是"**当前成员用哪个模型**"并**写回该成员的 `AgentUserRef`**。落点见 R2.4 |
| 6 | `TASK.md` 与本方案的关系 | 本方案取代 `TEAM_SESSION_TECHNICAL_ROUTE.md` §11 P4；`TASK.md` §4/§8 的对应行须在 R6 收尾时按实际结果修订，含把 P4.3 的 ✅ 更正为实际状态 |
| 7 | 证据目录归属 | 仓库内 `evidence/` 已不存在，`TASK.md` 的 `evidence/*.md` 链接目前悬空；本方案证据一律落仓库内路径并在 §7 记录 |
| 8 | ~~Role 注入 system prompt 无现成接缝~~ **已拍板选 (a) 并实施（2026-08-22）** | 新增 `boot.Options.SystemPromptIdentity`，装配期一次折入，折点在 output style 之后、core policies 之前。cache 安全性由 `internal/boot/effect_test.go` 的 effect 测试守卫（跨轮前缀字节一致 + 空值零插入）。**本项改动触及 cache 敏感包 `internal/boot/`，提 PR 时必须带 `Cache-impact: none - 空值零插入，前缀逐轮字节稳定，effect 测试守卫` 与 `Cache-guard: TestEffectSystemPromptIdentityReachesProviderAndStaysStable`；因同时改了 `internal/boot/`，还需 `System-prompt-review: <reviewer>`（该字段拒收 none/n/a，必须点名评审人）** |
| 9 | provider kind 由宿主二进制空导入注册 | `anthropic`/`openai` adapter 的 `provider.Register` 在 `cmd/reasonix/main.go:13`、`desktop/main.go:26` 空导入里，不在 `internal/`。生产两种 kind 都在；但 `internal/cli` 测试二进制只有 `openai`。R1.3a 的测试据此把 dial 断言放在网关路由，官方 DeepSeek 路由只断言映射——不是生产缺陷，但新增 kind 时须同步宿主导入 |
| 10 | `runtimeSwitchBusy` 读的是**被切离**的后端 | `switchTeamMember` 的门禁调 `m.ctrl.RuntimeStatus()`，即当前绑定成员的状态——正确语义（不能在它跑着的时候切走），但意味着任何 `control.SessionAPI` 替身都必须实现 `RuntimeStatus()`，否则第二次切换才 panic。R2.1a 的 `stubBackend` 因此实现了它 |
| 11 | 承载 history 的后端替身不可比较 | `stubBackend` 含 `[]provider.Message` 字段，两个同类型接口值做 `==`/`!=` 会运行时 panic。测试改为比较 `Label()`——也是更贴切的断言（绑的是哪个成员）。后续节点写替身时沿用此形状 |
| 12 | 部分 `control.SessionAPI` 替身撑不过一次 `Update` | 渲染路径的状态栏读 `Goal()`/`GoalStatus()`/`ToolApprovalMode()` 等子端口（`chat_tui.go:4168 modeTagText`），嵌入 nil 接口的替身会在那里 panic。**结论**：只测方法契约可用替身；一旦经 `m.Update` 渲染，必须绑真实 controller。R2.1b 的 `TestUpdateRoutesMemberEvent` 因此保持 ambient controller 绑定，只置会话状态 |
| 13 | `switchTeamMember` 尚未有 UI 触发点 | ~~R2.1a/b 只交付机制~~ **R2.2 已接通**：`[ TEAM ]` 点击与会话内 ↑/↓/Tab 都走 `switchTeamMember`。旧 `agentruntime` 路径**并行保留**（`startSessionTarget`/`bindSessionSubscription`/`StopTeam`），生产中因无 factory 而惰性，但仍被旧测试观测，R4 一并删除 |
| 14 | 半替换导航路径会挂测试 | R2.2 初版只把 `stepSession` 改到新路径、不再取消旧订阅，`TestTeamSessionSubscribeRebindOnSwitch` 在 `<-old.C` 上**永久阻塞**（整个 cli 包超时）。**教训**：一条导航路径要么整条换、要么两条并存，不能只换一半。当前采用并存，`stepSession` 两条都驱动 |
| 15 | "无 seam" 不等于"切换被拒" | `stepSession` 曾把 `switchTeamMember` 返回 nil 一律当拒绝，于是没装注册表的环境（测试、非交互宿主）里导航完全不动。现按 `m.teamBackends != nil` 区分：只有已接线的注册表返回 nil 才是真拒绝 |

---

## 9. 完成定义

同时满足才算完成：

1. `[ TEAM ]` 旁列出成员按钮（含 leader），点击即切换当前会话；
2. 进团队默认显示 leader 的会话与历史，历史占据**主 transcript**，输入走**主 composer**；
3. 每个成员是完整 Agent（工具/memory/skill/hook/trajectory 齐备），共用 `AgentUserRef` 的成员互不串线；
4. 平行栈（自研事件总线、成员 provider loop、迷你会话面板与迷你 composer）已删除，无残留符号；
5. 运行态数据不出现在仓库树内；
6. §6 表内 P0/P1 缺陷全部闭合；
7. §R6 六项门禁 + `-race` + `make lint` 在本终端实跑通过，阻塞如实记录。

---

## 10. 变更记录

| 版本 | 日期 | 变更 | 责任 |
|---|---|---|---|
| v0.1 | 2026-08-22 | 初稿：问题定性、既有接缝盘点、目标架构、D1-D7 设计决策、R0-R6 阶段、缺陷映射、进度表。待用户拍板 | — |
| v0.2 | 2026-08-22 | 用户拍板 + R0 完成：①§8-5 拍定 `/model` 改"当前成员用哪个模型"并写回 `AgentUserRef`，新增 R2.4；②R5.5 拍定抛弃 `AGENTS.md` 改共享指令形式，已 `git rm`；③R0.1/R0.2/R0.3 完成并落证据；④§6 逐条标注实际状态（`agentruntime` 四个并发 P0/P1 **仍在**，随 R4.1 消失）；⑤新登记缺陷"`EventDone` 先于最终持久化"，`evictDelta` 阻塞风险已顺带闭合；⑥`make lint` 15 项全修（外部团队存量 14 + 本轮 1），非放行 | — |
| v0.3 | 2026-08-22 | R1 推进：①R1.1 `MemberBinding`/`Bindings`/`MemberSessionFile` 落地（纯数据，零 control 依赖）；②R1.2 `memberEvent` 打标 sink + `teamBackends` LRU 注册表落地；③R1.3 拆为 R1.3a（provider 解析，已完成——密钥绕开 `config.ProviderEntry` 直进 `provider.New`，catalog 声明 `Tools`）与 R1.3b（阻塞）；④**新登记 §8-8**：`boot.Options` 无 system-prompt 注入位，Role 注入需拍板，R1.3b 因此阻塞；⑤新登记 §8-9：provider kind 由宿主二进制空导入注册。门禁全过：gofmt/vet/build/`go test ./...` 全量 exit 0/`-race` 绿/`make lint` 0 issues/repolint clean(1275) baseline 未动 | — |
| v0.4 | 2026-08-23 | **R1 完成**：§8-8 拍板取选项 (a)，R1.3b 落地——`boot.Options.SystemPromptIdentity`（折点在 output style 之后、core policies 之前）+ `memberSystemPromptIdentity`/`memberProxySpec`/`newMemberBackendBuilder`/`bindMemberSession`。`MemberBinding` 增 `Proxy`（`ProxyFor` 解析收口在 team 侧）。cache 守卫为真实 `boot.Build` + provider 边界的 effect 测试。§6 "成员 Agent 无工具" 一行改为装配侧已闭合。门禁：gofmt/vet/`CGO_ENABLED=0 build`/`go test ./...` 全量 exit 0/`make lint` 0 issues/repolint clean(1275) baseline 未动。**PR 元数据要求记在 §8-8**（改了 `internal/boot/` → `Cache-impact:` + `Cache-guard:` + `System-prompt-review:`） | — |
| v0.5 | 2026-08-23 | R2.1a 完成（切换机制），R2.1b 登记为剩余接线项。`AGENTS.md` 与 `session/agent_designed_team.json` 已提交删除（`0306f11a9`），两者不再在 HEAD 内。§7 R2.1 拆为 a/b。实施中的两个发现记入 §8-10/§8-11。门禁：gofmt/vet/build/`go test ./...` 全量 exit 0/`make lint` 0 issues/repolint clean(1275) | — |
| v0.6 | 2026-08-23 | **R2.1b 完成**（生产接线）：`bindTeamBackendSeam` + `bindTeamBackends` + `update` 的 `memberEventMsg` 分支。新增 §8-12（部分替身撑不过 `Update` 渲染路径）与 §8-13（`switchTeamMember` 的 UI 触发点在 R2.2/R3，旧 `agentruntime` 路径并存到 R4）。门禁：gofmt/vet/`CGO_ENABLED=0 build`/`go test ./...` 全量 exit 0/`-race`（cli+team）绿/`make lint` 0 issues/repolint clean(1275) baseline 未动 | — |
| v0.7 | 2026-08-23 | **R2.2 完成，第一个端到端可见节点**：进 `[ TEAM ]` 即绑 leader 的完整 Agent 后端，主 transcript 显示其历史；会话内导航即绑定；`ambient` 保存/归还让普通会话可回。§8-13 更新为"已接通"，新增 §8-14（半替换导航路径导致 cli 包超时的教训）与 §8-15（"无 seam" 不等于"切换被拒"）。门禁：gofmt/vet/build/`go test ./...` 全量 exit 0/`-race`(cli) 绿/`make lint` 0 issues/repolint clean(1275) | — |

### 10.1 R0 收尾说明

- **未用 `.gitignore` 兜底**（原 R0.1 计划项）：根因已修，且 `/context/`、`/session/`
  这类通用名字加进 `.gitignore` 会把将来真实的同名泄漏一并静默隐藏。改为用
  `TestSessionPathsStayUnderTeamRoot` 双向断言（正：文件在团队根下；反：项目根下
  不存在）。`.reasonix/*` 本来就已被忽略。
- **`mouseCopyOrPaste` 落在新文件而非 `chat_tui_paste.go`**：后者已 781 行，加进去撞
  800 行文件上限。鼠标复制/粘贴约定是独立职责，`chat_tui_mouse.go` 单独承载。
- **`git rm --cached` 的删除已进暂存区**，尚未提交——提交时机留给用户。
