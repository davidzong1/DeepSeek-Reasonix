# 改造方案 —— TUI 与成员 Agent 解耦

> 状态：**已拍板；R0-R5 全部节点完成（R4.4 部分、R3.2 两项降级见 §7）；R7~R11 全部完成；R12 为 `17513305b` 批次的复核结论——R12.1~R12.4 已完成，R12.2 的 Ack 时点风险与 R12.5 的「已落地未接线」见 §8，2026-08-26**。共享上下文黑板是另一条路线，进度记在 `SHARED_CONTEXT_BLACKBOARD_TECHNICAL_ROUTE.md` §8/§9.1/§4.5，不在本文档重复。
> **2026-08-26**：R13 修掉「openai provider 不可用」现场报障的三个缺陷（绑定失败不可见 / 错字保存得进盘 / 错误不回显输入值），详见 §7 R13 与 §8-25。
> R18 修掉现场新形状：`update_goal` 被无条件提供却在无 goal 时必被拒，且其拒绝是死路、描述又诱导模型「每轮都调」——模型因此以为已收尾而不调 `finish`。见 §7 R18 与 §8-42。
> R17 按用户拍板做方案 ②（干净停止+可见答案即合法边界，cache 零影响）；同时撤回 R15（其表述已不再准确，缓存代价归零）；并新发现 **R17.3 成员会话未绑到自己的文件**——它同时是 ③ 无法评估的原因，建议作为下一节点。
> R16 按用户拍板做方案 ③：`<turn-protocol>` 瞬时块挪到**回合尾**、且**观测到一次违约后才启用**（cache 零影响，合规模型零成本），待现场验证；无效则转 ②。见 §7 R16。
> R15 按用户拍板把回合收尾契约写进 `finish` 的工具描述（cache 前缀 +18 tokens，`SystemHash` 未变，golden 已正规更新，§7 R15 与 §8-40）。
> R14 让协议修复警告可诊断（`Detail` 落 transcript，§8-39），并复核确认「Agent user 更新自动同步成员」已由 R12.1 指纹实现——为此撤回了一处自写的死代码，见 §7 R14.2。
> **退出逻辑复审已收口（2026-08-25）**：§8-26~33 的八项里，#26/#27/#29(半)/#30/#33 已修（节点 R11），
> #28 随 #26 收敛，#31/#32 记为已知未做并说明其今日为何不可达。
> 本文档只描述改造方向、落点与节点，不描述任何未实施的东西为已实现。
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

> **2026-08-26 事实修订**：上面对 `internal/team/agentruntime/` 的定性针对的是 R4.1 删除的**那一个**包（`member.go` 直连 `provider.Stream`）。`17513305b` 以同名重建了一个**语义相反**的包：它只 import `internal/team`，零 `provider`/`control`，由宿主经 `boot.ControllerAsAgent` 把真实 `control.Controller` 适配进窄 `AgentAPI` 端口。读本节时不要把两者当同一件事，详见 §7 R12.5 与 §8-35。

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

### R7 —— 会话面板默认隐藏（用户优化任务 2026-08-23）

诉求：进入团队会话后，那块 `团队 · session` 面板（成员属性 + roster 列 + 一行提示）占掉 8 行
终端高度，而它要展示的历史上下文本来就在主 transcript 里。**面板改为按需**：进入会话默认不渲染，
点 `[ TEAM ]` 才显示，再点收起。这是 R2.3/R4.3 的自然终点——面板已退化为选择器，而选择器的职责
在 R3 已由状态栏成员按钮承担，面板剩下的价值不值 8 行。

| 节点 | 内容 |
|---|---|
| R7.1 | `sessionState.panel` + `[ TEAM ]` 绑定态切换 + `esc` 分层退出 |
| R7.2 | 绑定态归还鼠标捕获（否则按钮点不到）与图片粘贴 |

### R8 —— 一键退出团队（用户优化任务 2026-08-24）

诉求：在团队成员管理界面加一个退出 TEAM 的接口，用快捷键直接退出团队、回到初始会话。
现状是 `esc` 逐层回退：绑定态要 面板 → 会话 → roster → 团队列表 四次 `esc` 才回到普通聊天，
而**退出团队** 与 **回退一层** 是两个不同意图，不该共用一个键。

落法：新增保留键 `ctrl+t`（`teamExitKey`），在 `handleTeamKey` 里**先于所有状态归属者**判定，
因此任何深度——打开的字段、已武装的删除确认、池屏、三级 step-down——都不会把它吞掉。
`exitTeam()` 是唯一拆卸路径：解绑成员（归还 `m.ambient`）→ 收掉成员 model picker → 落 overlay →
重钉 tail。`esc` 的分层语义完全不动。

| 节点 | 内容 |
|---|---|
| R8.1 | `teamExitKey`/`exitTeam` + 三处帮助行提示（roster / 成员编辑器 / 会话面板脚注） |
| R8.2 | 顺带闭合的三个缺陷：overlay 关闭不解绑、悬空成员 picker 的 nil deref、`handleTeamKey` 无 overlay 谎报 consumed |

---

## 6. 缺陷 → 节点映射

> **2026-08-23 更新**：R4.1 删除整个 `agentruntime` 包后，表内曾标"未修"的四个并发/
> 生命周期 P0/P1 全部随之消失（符号零残留）。逐条状态见下；本表仍是判断"是否已修"的
> 唯一依据，不要据节点状态推断。

| 缺陷 | 严重度 | 闭合节点 | 闭合方式 |
|---|---|---|---|
| 生产路径无 ProviderFactory，会话发消息无任何反应 | P0 | R1.3 + R2.1 | **已闭合**：`newMemberBackendBuilder` 走 `boot.Build` 装出完整 Agent，R2.2 进入即绑、R2.3 主 composer 提交经 `m.ctrl.SendWithRaw` 直达该成员 |
| `context/`、`session/` 落仓库根，`session/` 已进 git | P0 | R0.1 | **已闭合**：store 改根 `.reasonix/team` + 撤出版本库 + 双向物理位置测试 |
| `event.go:162` DATA RACE（`nextSeq` 锁外自增，`-race` 实测） | P0 | R4.1 | **已闭合（删除）**：整包 `agentruntime` 移除，符号零残留 |
| `Subscribe` after `Close` panic（nil map，探针实测） | P0 | R4.1 | **已闭合（删除）** |
| 终态事件对已有订阅者永久丢失（`flushLocked` 只在 `Subscribe` 调用，探针实测） | P1 | R4.1 | **已闭合（删除）** |
| `Registry.Close` 跳过 stopped 实例 → 事件源永不关闭（探针实测） | P1 | R4.1/R4.4 | **已闭合（删除）**；新 `teamBackends` 的退役路径由测试断言每个后端恰好关闭一次 |
| `evictDelta` 持锁阻塞风险 + 终态事件重排 | P1 | R4.1 | **已闭合（删除）** |
| **`EventDone` 先于最终持久化** | P1 | R4.1 | **已闭合（删除）**。新设计里终态语义由 `control.Controller` 的 `TurnDone` + 自身快照顺序保证，不再由 TUI 侧的事件顺序推断 |
| 面板宽度按 rune 计（截图竖线错位） | P1 | R0.3 | **已闭合**：`padColumn`/`truncateCells` 按终端格计，2 个回归测试 |
| 成员"Agent"无工具/memory/skill/hook/trajectory | P0 | R1.3 | **已闭合（装配侧）**：`newMemberBackendBuilder` 走 `boot.Build`，成员即完整 Agent；resolver catalog 声明 `Tools`。端到端可见性待 R2.1 接进 TUI |
| `[ TEAM ]` 旁无成员按钮 | P1 | R3 | **已闭合**：状态栏按钮条 + 点击绑定 |
| 会话窗口不是会话（属性列表 + 5 行截断） | P1 | R2.1/R4.3 | **已闭合**：历史回到主 transcript，输入回到主 composer；会话面板降级为成员选择器 |
| repolint 红（HEAD 1314、工作区 1316；`240d89d86` 为 clean 1276） | P1 | R0.2 | **已闭合**：clean(1275)，baseline 未加宽 |
| AgentType 白名单未实现 | P2 | R5.1 | **已闭合**：纯命令词白名单，store 边界拒绝 |
| 陈旧 `state.json` / `Snapshot()` 持锁 I/O / `ctrl+c` 空实现 | P2 | R5.2-R5.4 | **已闭合**：前两条随 `agentruntime` 删除消失，`ctrl+c` 随 R2.3 回归主 composer 取消路径 |
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
| R2.3 | 主 composer 接管，删迷你 composer，粘贴回归 | R2.1 | **已完成** | 键盘所有权反转：新增 `teamSessionBound()`/`teamOverlayModal()`，`hideComposer` 与 `update` 的 team 分支改为**只有非绑定态才模态**；`handleTeamKey` 在绑定态只消费保留键（`ctrl+up`/`ctrl+down` 换成员、`esc` 退出），其余全部落到主 composer。提交因此自动走 `m.ctrl.SendWithRaw` 到该成员后端——**没有第二条发送路径**。删除 `session.buf`/`session.input`/`sessionSend`/`sessionInputKey`/`handleSessionKey`/`restartSessionTarget`；`teamPasteTarget` 去掉 session 分支；`applyComposerPasteCount` 的模态守卫改为 `teamOverlayModal()`（否则绑定态粘贴被丢弃——这是本节点我引入并修掉的真缺陷）。`bindBackend` 换后端时清空 composer 草稿（草稿属于它被写给的那个后端，不能悄悄投给别的成员）。会话面板降级为成员选择器 + 一行提示 |
| R2.4 | `/model` 写回当前成员 `AgentUserRef` 并重建后端 | R2.1 | **已完成** | `runModelSubcommand` 开头先走 `runMemberModelSubcommand`：绑定态下 `/model <ref>` 走 `BindAgentUser` 写回该成员并 `teamBackends.release`（旧后端的 provider/凭据/role prompt 在装配期已烘死，只有重建才会生效），`/model` 无参开成员池 picker（新 `quickPickerMemberAgentUser` kind，当前条目预选）。聊天自身模型不动。测试 2 个（写回+退役+持久化+聊天模型不动、picker 列池并预选） |
| R3.1 | 状态栏成员按钮条（含 leader，当前高亮） | R2.1 | **已完成** | `appendTeamButton` 在 `[ TEAM ]` 后按 roster 追加 `[ <id> ]` 按钮，绑定成员 `accent`、其余 `dim`；`statusMemberIDs` 取会话 roster（无会话时取 model members），`statusMemberButtonLimit = 6` 封顶避免挤掉状态行 |
| R3.2 | 命中测试 + 窄屏折行 + unread 徽标 | R3.1 | **已完成（折行/徽标见备注）** | `teamButtonHit` 泛化为 `teamStatusButtonHit(x,y) (member, teamEntry)` + `labelHit` 按终端格量宽（CJK 成员 id 与相邻样式不移动命中框）；`handleTeamStatusClick` 路由：成员按钮 → `switchTeamMember`，`[ TEAM ]` → 开 overlay。`update` 的鼠标分支 +2 行，repolint 仍 clean。**未做**：窄屏折行沿用状态行既有响应式布局（按钮条按 6 个封顶而非折行）；unread 徽标仍只在会话右栏，未上状态栏。测试 2 个（渲染+命中+点击绑定+高亮/dim、无团队时不显示且封顶生效） |
| R4.1 | 删 `agentruntime` 自研事件/成员 loop 及其测试 | R2 R3 验收 | **已完成** | **整包删除** `internal/team/agentruntime/`（15 文件 / 2364 行）——它是平行栈本体，只被 CLI 引用。`grep -rn "EventSource\|MemberAgent\|agentruntime\|evictDelta"` 在 `internal/`/`cmd/`/`desktop/` 零命中（残留命中都在无关的 `providerext`/`serve`/`taskcatalog`） |
| R4.2 | 删 `chat_tui_team_events.go` | R2 R3 验收 | **已完成** | 文件删除；`update` 的 `teamRuntimeEventMsg` 分支移除；`sessionState.sub`、`sessionSpec`、`startSessionTarget`、`bindSessionSubscription`/`cancelSessionSubscription` 一并删除 |
| R4.3 | `renderTeamSession` 退化为成员选择 | R2.1 | **已完成** | R2.3 已去迷你 composer；本轮再去掉读 legacy `messages.jsonl` 的历史块（成员历史已在主 transcript，读那里只会显示空或过期的 pre-D5 数据）与随之无用的 `sessionHistoryLines` |
| R4.4 | `sessionstore` 收敛 / `Registry` 降级 | R4.1 | **部分完成** | `Registry` 随整包删除。`sessionstore` 的存活消费者只剩 `WriteSelection`/`ReadSelection`（会话选择）与 `MemberDirs`/`ClearTeamTrash`（清理）；`Messages`/`AppendMessage`/`ReadCursor`/`WriteCursor`/`ReadState`/`WriteState` 已无生产调用方但**仍保留为导出 API**（`unused` 不拦导出符号）。**未做**：物理删除这些方法——留待与 D5 的成员会话文件语义一并收口，见 §8-19 |
| R5.1 | AgentType 白名单 + 脏数据清理 | — | **已完成** | `validateAgentType` 落地 §7.5 白名单：空=继承，`claude`/`codex` 直通，其余必须是**一个纯命令词**（仅字母数字与 `-_.`，长度 ≤32），因此启动类型再也带不进参数列表、路径或 shell 元字符。与既有错误消息 "claude, codex, or a plain command word" 一致。脏数据 `"agent_type":"c"` 已随 team.json 重写消失。测试 2 个（白名单正反例含 `claude;rm -rf /`、`$(id)`、超长；两个 setter 在 store 边界拒绝且不落盘） |
| R5.2 | 陈旧 `state.json` 处置 | R4.4 | **已完成** | 唯一写者 `Registry.WriteState` 随 R4.1 删除，`state.json` 不再被写；残留文件不再被任何路径读取。**并顺带修掉一个 R4 引入的真缺陷**：leader 解除承诺"清空整个团队所有成员的历史上下文"，但 D5 之后真实历史在会话文件里，原代码只清空了（已无人写的）legacy context 树 —— 三级确认还逐项告知用户要清什么，这是个空承诺。新增 `clearTeamHistories`：退役后端 + 删除每个成员的会话文件 + 仍清 legacy 树（pre-D5 残留），幂等、不碰其他团队。picker 新增 `sessionDir` 字段（开窗时取 `m.ctrl.SessionDir()`）。测试 1 个 |
| R5.3 | `Snapshot()` 持锁 I/O | R4.1 | **已完成** | 随 `MemberAgent` 删除消失 |
| R5.4 | 会话内 `ctrl+c` 停 in-flight | R2.3 | **随 R2.3 闭合** | `ctrl+c` 不在 `handleTeamKey` 的保留键内，因此绑定态直接落到主 composer 既有的取消路径——原先那个"待 P4.2 接线"的空实现连同 `sessionInputKey` 一起删除了 |
| R5.5 | `AGENTS.md` 处置 | 用户拍板 | **已完成** | 拍板抛弃，改用共享指令形式；`git rm AGENTS.md`（`43ddaa112` 里可取回）。`internal/instruction` 不再加载它，`REASONIX.md`+`CLAUDE.md` 不受影响 |
| R6 | 六项工程门禁 + `-race` + `make lint` 全过 | 全部 | 部分（R0 范围内已过） | R0 收尾实跑：`gofmt -l` clean · `go vet ./...` exit 0 · `CGO_ENABLED=0 go build ./...` OK · `go test ./...` **全量 exit 0 零失败** · `go test ./internal/team/... ./internal/cli/ -race` 全绿 · `make lint` **0 issues** · repolint clean(1275)。R1 起每节点重跑 |
| R7.1 | 会话面板默认隐藏，`[ TEAM ]` 切换 | R3.2 | **已完成** | `sessionState.panel`（默认 false，`closeSession` 归零即自动复位）+ `setSessionPanel`（切换时置 `forceGotoBottom`，否则面板收起后 viewport 保留旧 offset，最新输出被顶出屏外）+ `sessionPanelHidden`；`renderTeamPicker` 在 `session.active && !panel` 时返回 `""`，行数核算（`bottomRows`）与布局因此自动跟随。`[ TEAM ]` 在绑定态改为原地切换面板而非重进团队（`handleTeamStatusClick` 新增一条 case，返回 nil cmd）；`esc` 分层退出（有面板先收面板，无面板才关会话——离开团队是再一次 esc）。面板脚注 `Esc back` → `Esc hide panel`。实测 110×20：隐藏时 `bottomRows=4 / viewport=16`，显示时 `12 / 8`，两态帧高恒为 20 行 |
| R7.2 | 绑定态归还鼠标捕获与图片粘贴 | R7.1 | **已完成** | `View()` 的 `MouseMode` 与图片粘贴守卫从 `m.teamPick != nil` 改为 `m.teamOverlayModal()`。前者是**阻断 R3 的真缺陷**：overlay 一开就 `MouseModeNone`，进团队后状态栏成员按钮与 `[ TEAM ]` 在真实终端里根本收不到点击（R3 的测试直调 `handleTeamStatusClick`，结构上测不到）。后者与 R2.3 修的粘贴守卫同族：绑定态 composer 是成员输入，图片路径必须落进去。已用真实 `Update(tea.MouseClickMsg{...})` 双击验证整链 |
| R8.1 | `ctrl+t` 一键退出团队 + 帮助行提示 | R7.1 | **已完成** | `teamExitKey = "ctrl+t"` 与 `teamExitHint = "Ctrl+T exit team"` 同处声明（键与文案不会各改一半）；`exitTeam()` 收口拆卸：`closeSession()`（解绑归还 `m.ambient`）→ 清成员 model picker → `closeTeamOverlay()` + `teamPick = nil` → 置 `forceGotoBottom`（overlay 的行回到 transcript，不重钉则最新输出被顶出屏外）。判定放在 `handleTeamKey` **最前**，故任何深度都能退。提示落 roster 帮助行、成员编辑器第二行、会话面板脚注三处，均由 `teamExitHint` 常量拼接。`ctrl+t` 在 `internal/` 零占用；它只在 overlay 打开时遮蔽 textarea 的 transpose 绑定。实测 110×20：绑定态隐藏 `bottomRows=4/viewport=16`、显示面板 `12/8`、roster `8/12`，`ctrl+t` 后一律回到 `4/16` 且状态行只剩 `[ TEAM ]`，帧高恒 20。测试 4 个（9 个屏幕子用例逐一退出含三种已武装写状态、绑定态解绑+后端存活+成员 transcript 不残留、picker 随团队关闭且悬空确认不 panic、提示可见 + 无 overlay 时不消费）。**变异探针**：把 `teamExitKey` 改成未绑定值 → 11 处失败；保留 overlay 不落 → 12 处失败 |
| R8.2 | 三个顺带闭合的缺陷 | R8.1 | **已完成** | ① overlay 关闭路径（`esc`/`q` 从团队列表）此前只 `teamPick = nil` 而不解绑，改为共用 `exitTeam()`——原先仅因"到团队列表必然已 `closeSession`"而侥幸安全，一键退出会直接踩中，后果是 `m.ctrl` 停在成员后端且再无 overlay 可退；② `rebindMemberAgentUser` 加 `teamSessionBound()` 门禁：成员 model picker 打开时会话若已关闭，确认将 `p.session.current` 取空乃至 `p == nil` **nil deref**（今日经 esc-esc-enter 即可复现），且 `exitTeam` 一并收掉该 picker；③ `handleTeamKey` 在 `teamPick == nil` 时返回 `consumed = false`——原先无条件 `true`，只靠调用点 `chat_tui.go:1385` 的 nil 守卫才没吞键，是留给下一个调用方的陷阱 |
| R8.3 | leader 生命周期测试节点（test-engineer） | R8.1 | **已完成** | 新增 8 个测试：数据层 `internal/team/blackboard_stepdown_test.go` 2 个（unbind 后 BindRecord 身份 MemberID/LeaderID/Generation/TaskID 完整可读、cursor 消费行随 step-down 保留）；CLI 层 `internal/cli/chat_tui_team_lifecycle_test.go` 6 个——入口重开不恢复旧成员（`TestTeamReentryStartsFreshOnLeader`）、ctrl+t 退出后 leader 后端与 transcript 保留（`TestTeamExitKeepsLeaderContext`）、k 顺序（先全后端退役再删会话文件再发布 leader off，`TestKStepDownStopsAllSessionsBeforeClearing`）、k 失败保留（live 注入未入 order 的后端 → stop 失败 → 文件与 leader 标记均不清，`TestKStepDownStopFailureKeepsContext`）、k 重复调用安全拒绝（`TestKStepDownRepeatedCallIsSafe`）、l 无 leader 指定/有 leader 拒绝并点名 holder（`TestLAssignAppointsLeaderlessTeam`/`TestLAssignRefusesWhenLeaderExists`）。契约 seam：`TestRoleEditingReadOnly`（memberEditFields 移除 role 后自动激活，现为激活态）。**旧测试适配**（role 只读/l assign-only 语义回归，6 个）：删 role 字段粘贴 ×2（paste_test，功能随 role 文本字段消失）、`TestTeamMemberEditRolePersistsAndClears` 与 `TestTeamMemberEditLeaderPersists` 删除（覆盖由 TestRoleEditingReadOnly 与 TestLAssign* 承接）、`TestTeamRosterLeaderToggleAndSessionGate` → `TestTeamRosterLeaderAssignAndSessionGate`（l 拒绝+assign、t 门禁保留、无 leader 指定→t 可开）、`TestTeamMemberEditStatusPersists` 按键序列适配（status 升至 field 0）。验证：`go test ./internal/cli/ ./internal/team/ ./cmd/reasonix-blackboard/` 全绿、`-race` 全绿、gofmt 干净、repolint 无新增违规（仅剩 chat_tui.go 既有 drift，architecture 拆分中）。不碰 Python bus、不用 repolint `-update` |
| R9 | 可复用选项弹出列表（optionList）验收测试节点（test-engineer） | R8.3 | **已完成** | 组件 `internal/cli/option_list.go`（tui-researcher 实现：`option{id,label,disabled}`/`optionSingle`/`optionMulti`/`handleKey→(consumed,action)`/`wheel`/`resize`/`view(w,h)`/`optionListHeight` 高度预算；member 编辑器 status/proxy/agent 与 pool provider 替换行内循环 picker，role 只读不变，picker 激活期宿主吞全部键）。测试矩阵：**组件级 `option_list_test.go`（8 个）**——单选 initial 定位光标+enter 提交（`TestOptionListSinglePinsCursorCommitCancel`）、initial 未命中落 0（`TestOptionListInitialAbsentLandsZero`）、多选空格 toggle 幂等+按序提交+帮助行（`TestOptionListMultiToggleCommitsSet`）、禁用跳过/双面墙不移动（`TestOptionListDisabledSkipped`）、pgdn/pgup 窗口步进+end/home 极限+offset 跟随钳制（`TestOptionListScrollPinsOffsetFollow`）、view 高度适配（height-3 行、长列表全显、空列表占位、长标签截断不超宽，`TestOptionListViewAdaptsHeight`）、无 resize 时光标仍可见（`TestOptionListViewKeepsCursorInside`）、滚轮±1+空列表 unconsumed（`TestOptionListWheelMovesAndInerts`）、高度预算表驱动（`TestOptionListHeightBudget`）。**接入集成 `option_list_integration_test.go`（6 个）**——字母键（含 s/t）不泄漏 composer 且零写盘（`TestMemberPickerLettersInertAndComposerClean`）、滚轮事件移动光标+提交落盘（`TestMemberPickerWheelMovesCursorAndCommits`）、esc 取消零写盘（`TestMemberPickerEscCancelsZeroWrite`）、archived 全路径提交（`TestMemberPickerCommitPersistsArchived`）、pool provider 滚轮选择落盘（`TestPoolProviderWheelMovesCursorAndCommits`）、弹窗边框/帮助行渲染（`TestMemberPickerRendersBorderedPopup`）。**旧测试适配**：memberEditState `buf`/`pick` 字段移除 → `TestTeamMemberEditRoleLeaderReadOnly` 改断言 choice 未提交、`TestTeamPasteInertOnMemberPickerRows` 改比较 `list` 状态并换 proxy 行；pool provider 2 个旧测试（'k' wrap/legacy 替换路径）由实现侧行为兼容。验证：`go test ./internal/cli/ -count=1 -race` 全绿（34.3s）、定向 race 绿、gofmt 干净、`go vet ./internal/cli/` 0、repolint clean（1271 baselined，零新增；essay 已压 ≤3 行）。不碰实现文件（option_list.go 归 tui-researcher）、不用 repolint `-update` |
| R10 | P0 Ctrl+T 退出 TEAM 状态链回归测试节点（test-engineer） | R8.3 | **已完成** | 讨论收敛的 P0：`exitTeam` 原先缺 x 的两个副作用——不清持久化 selection（磁盘残留最后 MemberID，k/x 均清而 Ctrl+T 不清，三退出路径不一致）且不置 `teamSuppressAutoSession`（Ctrl+T 退出后 [TEAM] 点击自动重进 leader 会话，与 x 的「下次 [TEAM] 停管理页」契约相反）。实现侧（tui-researcher，c79ee9ccc）已修复：exitTeam 补 `WriteSelection({Team})`（带 `sessions==nil` 守卫）+ `teamSuppressAutoSession=true`，与 x 仅差「x 停管理页、Ctrl+T 关 overlay」。**回归矩阵 `chat_tui_team_ctrlt_test.go`（7 个）**——session 态 Ctrl+T 全退+selection 清空+suppress 置位（`TestCtrlTExitsSessionClearsSelectionAndSuppresses`）、[TEAM] 重开停 ModeTeams 且 space 进 ModeList 无旧会话（`TestCtrlTReentryParksOnTeamManagement`）、roster 态 esc 保留 selection 而 Ctrl+T 清空（`TestCtrlTFromRosterClearsStaleSelection`）、composer 草稿丢弃+chat 后端归还+composer 不隐藏（`TestCtrlTDiscardsComposerDraftAndUnbinds`）、重复退出幂等（`TestCtrlTRepeatedExitIdempotent`）、无 leader/坏 store 不 panic（`TestCtrlTNoLeaderAndBrokenStoreNoPanic`）、t 显式进入恢复且落 leader 非旧成员（`TestCtrlTReentryThenTResumesExplicitly`）。验证：`go test ./internal/cli/ -count=1 -race -run TestCtrlT` 7/7 绿、全量 cli 11.9s 绿、repolint 零新增（我的文件 essay ≤3 行；`chat_tui_team.go` 809 行超线归实现侧处理）。不引入 restoreSession 读取（leader-first 入口被 `TestTeamButtonOpensOnLeaderAfterMemberRemoved`/`ReopenLandsOnLeader` 锁定）；esc 语义不动（导航非退出）。只碰测试文件 |
| R11.1 | **退出偏好下盘持久化（用户 P0）+ 摘掉共享拆卸点上的 rider** | R10 | **已完成** | 用户实测缺陷：ctrl+t 退出后重启 `./bin/reasonix` 再点 `[ TEAM ]` 仍自动进上次团队会话——因为 `teamSuppressAutoSession` 只是 `chatTUI` 的一个内存字段，进程一换即失忆；而 `restoreSession` 的兜底 `openSession("")` 恒开 leader 会话。**同一个 bug 的另一半**（§8-26）：该 rider 被写在 `exitTeam()` 里，而 `exitTeam()` 是所有关闭路径的共用拆卸点，于是从团队列表 `esc`、`q`+确认（什么都没做就退出）也会置位，把 `[ TEAM ]` 的入口语义永久降级成管理页。落法：`team.SessionSelection` 新增 `Suspended bool`（`omitempty`，零值=自动会话，因此**旧文件与新项目行为不变**）；`restoreSession` 读到 `Suspended` 即返回 `""` 停管理页；`exitTeam` **只做拆卸**，偏好由新的 `leaveTeamDeliberately`（ctrl+t）与 `x` 经 `suspendAutoSession()` 写盘；`teamSuppressAutoSession` 字段**整个删除**——磁盘是唯一事实源，不再有两处状态可漂移。`t` 与 `ctrl+↑/↓` 走既有 `persistSessionSelection`（写 `{Team,MemberID}`，`Suspended` 由 omit 归零）自动解除。新增 `exitingTeamName()`（绑定态取 `session.teamName`、否则焦点团队，且必须在 `closeSession` **之前**读——后者会把 `teamName` 清零）收口 §8-29 那个「两个来源写同一把键」的隐患。实测磁盘：ctrl+t 后 `{"schema_version":1,"team":"alpha","suspended":true}` → 重启 `session.active=false`；再按 t 后 `{..."member_id":"lead"}` → 重启自动进 lead。测试 3 个新增（`TestTeamExitSurvivesRestart` 用同目录第二个 `chatTUI` 模拟重启，含「t 之后再重启要恢复自动会话」的反向断言；`TestPlainOverlayCloseKeepsAutoSession` 表驱动钉住 esc/q 两条路径**不得**改偏好；`TestTeamExitDropsSelectionAndParksNextClick` 改断言磁盘 `Suspended`），另 4 处旧断言由内存字段改为读盘（更强：它们现在钉的正是重启后仍成立的事实）。**变异探针**：去掉 `restoreSession` 的 `Suspended` 判据 → 2 处失败；把 rider 放回共享拆卸点 → `TestPlainOverlayCloseKeepsAutoSession` 两个子用例失败 |
| R11.2 | `x` 键收敛为「有门禁、有反馈」（§8-27） | R11.1 | **已完成** | 原状：`x` 无 `memberListKeyAllowed` 门禁（`t`/`k` 都有），已武装的删除确认下仍执行（实测 `inputKind` 保持 4 而 suppress 已置位），团队列表页/成员编辑器页也能触发而只有 roster 帮助行写了它；且它**在自己声称要退出的状态里按不到**（绑定态字母归 composer，能到管理页就说明会话已被 esc 关掉，`m.closeSession()` 恒 no-op），于是唯一实效是两件不可见的事，实测按下后 transcript +0 行、errMsg 空、屏幕零变化。落法：加 `memberListKeyAllowed` 门禁 + `m.notice("team X: auto-session off — [ TEAM ] parks here until t")` 把它做过的事说出来（管理页上没有会话可供变化自证）+ 键名走 `teamExitAllKey` 常量。**仍待用户裁决**：ctrl+t 已能从任意深度完成全部工作，`x` 唯一剩余差异是「保留 overlay 停在管理页」，是否值得占一个键 |
| R11.3 | 状态栏成员按钮不再在管理页渲染（§8-30） | R11.1 | **已完成** | `statusMemberIDs()` 原先在无会话时回落 `model.Members()`，于是管理页也渲染 `[ lead ] [ alice ]`；而管理页模态 → `MouseMode = MouseModeNone`（鼠标交还终端做文本选择）→ 那些按钮收不到点击：**可见且不可用**。改为仅绑定态返回 roster，其余返回 nil。顺带把管理页的状态行宽度还给别的字段。`TestStatusMemberButtonsAbsentWithoutTeam` 的夹具原本 9 个成员**都没有 leader**（因此不会开会话），改为首个成员是 leader 才真正测到 6 个封顶，并加一条「esc 回管理页后按钮行消失」的断言 |
| R11.4 | 仓库产物与 npm 清理（§8-33） | — | **已完成** | `git rm --cached reasonix-blackboard`（10,233,030 字节可执行文件，磁盘保留）+ `.gitignore` 补 `/reasonix-blackboard`(`.exe`)，紧邻既有 `/reasonix` 条目；证据：`git check-ignore -v` 命中 `.gitignore:8`，且 `Makefile`/`scripts/`/`.github/` 对它零引用（跨进程测试 `cmd/reasonix-blackboard/blackboard_cross_process_test.go:35` 自己 `go build` 到临时目录，不依赖仓库内那份）。`git rm package.json package-lock.json`（唯一依赖 `@anthropic-ai/claude-agent-sdk`，1507 行 lockfile）：全仓库 `*.go/*.py/*.js/*.mjs/*.ts/*.sh/*.toml` 对该 SDK 零引用，且本仓库既有约定是 npm 清单落在需要它的子项目里（CI 的 `npm ci` 全部带 `working-directory: site` / `workers/crash-report`）。若外部桥确实需要，应按该约定落到自己的子目录；内容可从 `f590a50eb` 取回 |
| R12.1 | **成员后端指纹失效**（`17513305b` 的标题所指：让会话用对模型） | R2.4 | **已完成（他人实施，本轮复核）** | 缺陷：R2.4 只在 `/model` 路径显式 `teamBackends.release`，因此从**池屏 `e` 编辑**同一条 pool 条目后，已装配的成员后端仍在用旧 provider/model/key——「当前会话用错模型」。落法：`memberAgentUserFingerprint` 对 `UserID/Provider/BaseURL/Model/Effort/APIKey` 逐段带 `\0` 分隔做 sha256，`newMemberBackendFingerprint` 再拼 `Role` 与 `proxyFingerprint`（只编码 `memberProxySpec` 真正读的位），注册表 bind 时指纹不符即重建；ref 解析不出错误 → 保守重建并暴露失败，而不是复用可疑后端。密钥只进哈希，不落盘不入日志。另加 `memberCredentialError`：pool 条目既无 `APIKey` 又无 `SecretRef.StoreID` 时装配即拒，错误点名成员与条目，不再回落成聊天自身的 DeepSeek 默认提示。**该项的 P0 架构审核（A rebind 失败窗口死 / B 指纹失效绕过 Running 保护 / C 指纹维度遗漏）与其收口证据记在 `SHARED_CONTEXT_BLACKBOARD_TECHNICAL_ROUTE.md` §4.5，不在此重复** |
| R12.2 | TUI durable command inbox + 回合注入 | R2.3 | **已完成（他人实施，本轮复核）** | `internal/cli/chat_tui_team_inbox.go`（165 行）：`teamInboxWire` 持 board store，`openTeamInbox(cwd)` 随 overlay 建、`exitTeam` 随 overlay `close()`；`inboxFor` 按**服务端持久化的 `BindRecord` generation** 建一次（旧窗口拿不到它答不起的指令）；`injectTeamTurn` 在 `SendWithRaw`/`SubmitDisplay` 的**发送参数**上折入 `[command inbox]` 块，**显示参数保持原文**。`consumeWakeups(firstLeader())` 把 leader 唤醒变成 transcript notice。**cache 判据**：注入落在回合正文（`b.WriteString("\n" + text)`），system-prompt 前缀零改动——符合 REASONIX.md「ride the turn tail」，本轮实跑 `repolint clean (1271)` 且 baseline 变动来自上游 `c3f5b497d` 而非本分支。**遗留风险见 §8-36（Ack 时点）** |
| R12.3 | leader 入口语义改写：非 leader 自动纠正 + `refusal` 横幅 + leader 置顶 | R2.2 | **已完成（他人实施，本轮复核）** | **语义变更，影响本文档既有描述**：`enterTeamSession` 原先「非 leader 按 t → 写 `errMsg` 拒绝」，现改为**自动纠正到该团队 leader**（`p.model.FocusMember(leader)`，roster 高亮随之移动），只有**整个团队没有 leader** 才拒绝。新增 `teamPicker.refusal` 字段与 `errMsg` 分离：refusal 是**活页面上的横幅**（列表仍可见、`l` 可指派、`reload` 自动清），errMsg 仍是数据不可用的死页。`restoreSession` 签名改为 `(string, bool)`，第二个返回值区分「用户主动离开（suspended，静默停管理页）」与「无 leader（挂 refusal）」——即 R11.1 那位 `Suspended` 的消费点。`internal/team/tui/model.go`：leader 恒排在非 leader 之前（状态优先级失效）、多 leader 按 state→ID、`FocusMember` 按身份聚焦。测试证据在 `chat_tui_team_leader_entry_test.go`(6) 与 `model_leader_pin_test.go`(6)，明细记在黑板路线文档 |
| R12.4 | 黑板快照导出（`export` op） | — | **已完成（他人实施，本轮复核）** | `internal/team/blackboard_export.go`（159 行）`SQLiteStore.ExportSnapshot` 写 checkpoint 一致的 JSONL；`cmd/reasonix-blackboard/protocol.go` 新增 `case "export"` → `handleExport`，**有生产调用方**（不是只有测试）。属黑板路线 P1 导出器，其状态与验收记在该路线文档 |
| R12.5 | 团队运行时宿主 / 任务存储 / 调度运行时 | R12.1 | **已落地，未接线（dormant）** | `17513305b` 重新引入 `internal/team/agentruntime/`（7 个实现文件 719 行：`runtime.go` 269、`inbox.go` 97、`inject.go` 59、`wakeup.go` 46、`watermark.go` 45、`agent.go` 16、`doc.go` 11）+ `internal/team/taskstore.go`(89) + `internal/team/scheduler/runtime.go`(148) + `internal/boot/team_runtime_host.go`(121)。**与 R4.1 删掉的同名包不是一回事**（见 §8-35）：新包只 import `reasonix/internal/team`，**零 `provider`、零 `control`**，宿主用 `boot.ControllerAsAgent(*control.Controller) agentruntime.AgentAPI` 把真实 Agent 适配进窄端口——正是 §1 要求的方向反转。**但是**：`ControllerAsAgent`、`RecoverTeamRuntime`、`WireTeamInjector` 三个 seam 与 `scheduler.NewRuntimeScheduler`、`SQLiteStore.SaveTask/LoadTask/LoadLiveTasks` 的**非测试调用方为零**（实跑 `grep -rln 'team/agentruntime\|team/scheduler' --include=*.go | grep -v _test` 只有 `chat_tui_team_inbox.go`（用 `BoardInbox`，属 R12.2 那半）、`scheduler/runtime.go` 自身、`team_runtime_host.go` 自身；`cmd/`、`desktop/`、`internal/cli/cli.go`、`internal/serve/` 零命中）。即：**已装配、有测试、产品里到不了**。故状态记「未接线」而非「已完成」，见 §8-37 |
| R13.1 | **成员绑定失败必须可见**（用户报「openai 不可用」的真因之一） | R7.1 | **已完成** | 现场诊断：leader 绑 `wanapi-gpt5.6-sol` 后右下角仍显示 `deepseek-v4-flash`。根因链：pool 条目 `Effort: "hight"` 拼写错误 → `openai.New()` 构造期校验拒绝 → `newMemberBackendBuilder` 报错 → `teamBackends.bind` 报错 → `switchTeamMember` 写 `session.errMsg` 并 **`return nil`**（不调 `bindBackend`）→ `m.ctrl`/`m.modelRef` 一字未动，仍是聊天自身后端。**不是回落到默认 agent user**，代码里不存在这种路径；是绑定被拒、窗口从未切换。而 `session.errMsg` 全仓库**只有一个渲染点**（`chat_tui_team_render.go` 的 `renderTeamSession`），R7 把面板改成默认隐藏后，`switchTeamMember`/`/model` 共 8 条拒绝消息全部静音——症状因此表现为「进去了但显示错模型」。落法：新增 `refuseTeamSession(msg)`，写 errMsg 的同时 `m.notice(msg)` 落进 transcript（永远可见），8 处拒绝全部改走它，两处清空 `errMsg = ""` 不动。测试 `TestMemberBindFailureReachesTranscript`（装配失败 → transcript 含原因 + 面板保留细节 + `m.ctrl` 仍是 ambient + `m.ambient` 未被写入）。**这是 R7 引入的缺陷，§8-25 当初只记成「帮助文本不可发现」，低估了：它藏的是错误** |
| R13.2 | pool 保存时按 adapter 试装，把错字拦在编辑器里 | R13.1 | **已完成** | effort 的合法集合**按模型族分支**（`openai.go`：DeepSeek low/high/max/disabled、MiniMax adaptive/disabled、Zhipu 与 LongCat enabled/disabled、Ollama none/low/medium/high/max、默认分支 low/medium/high 且 `max` 静默钳到 `high`），而 `agentuser_validate.go` 对 effort 只做长度校验（≤64），所以 `hight` 存得进盘、只在装配期炸。**不自造第二份白名单**（那正是我反复批的两套事实）：`dryRunPoolEntry` 走 `team.ResolveProvider` + `newMemberProviderResolver` + `Resolve()`，即**装配用的同一条路**，让 adapter 自己判；`savePoolEdit` 在 store 写入前调它，拒绝则留在草稿上零写盘。三条豁免：表单未填完（无 provider/model）、本构建未注册该 kind（宿主的决定）、provider 名解析不出（**store 的 legacy 保留豁免的决定**）。校验只在写路径，加载不校验，所以既有坏值不会导致 pool 加载失败。测试 `TestDryRunPoolEntryUsesTheAdapterAsAuthority`（7 子用例含 legacy 保留）+ `TestSavePoolEditRefusesUnusableEntryZeroWrite`（拒绝零写盘 → 改对即落盘）|
| R13.3 | provider 错误回显用户输入的值 | — | **已完成** | `openai.go` 六条 effort 拒绝消息原先只说「must be low, medium, or high」，不回显实际输入——用户读到只会想「我填的就是 high 啊」，这正是本次错字难定位的原因。六条统一加 `%q` 回显（与同文件 `supported_efforts` 那条已有的回显风格一致）。**上轮我说「消息漏了 max 所以不准确」是错的**：`max` 是被静默钳到 `high` 的兼容别名，写进提示等于推荐一个会被降级的值，消息本身准确 |
| R13.4 | 用户 pool 数据修正（一个字符） | R13.2 | **已完成** | `.reasonix/team/agent_users.json` 的 `wanapi-gpt5.6-sol` 条目 `"hight"` → `"high"`；改前建了带时间戳的 `.bak`，仅替换该一处字符串（文件 617→616 字节，密钥未读出未打印）。复验：直接读真实 pool 文件跑 `dryRunPoolEntry`，两条条目均 `<nil>`。指纹失效（R12.1）会让下次 bind 自动重建后端，无需重启 |
| R14.1 | **Notice 的 `Detail` 落进 transcript**（协议修复警告可诊断） | R13.1 | **已完成** | 用户报障：成员每轮回答后都出现 `! The model did not complete the required turn protocol; requesting one repair.`。机制：`internal/agent/finalization.go:153` 的 `requestProtocolRepair` —— 回合必须以「一次 `finish` 工具调用（且为该批次唯一）+ 一段可见最终答复」收尾，五个触发点各带不同 `reason`（无 finish 调用 / finish 与其他工具混批 / outcome 非法 / 无可见正文 / finish 被判无效），只容忍**一次**修复，第二次返回 `ProtocolFailedError` → 回合终态 `protocol_failed`。**问题在于**：`reason` 被放进 `event.Detail`（字段注释写的是「optional diagnostic text for expandable details」），而 CLI 的 Notice 分支 `chat_tui.go` 只渲染 `e.Text`——五种原因共用同一句话，`Detail` 不显示、不落盘、trajectory 也不记，于是唯一的诊断信息被生成出来又扔掉。落法：新增 `chat_tui_notice.go`（`commitNoticeDetail` + `noceDetailLines`，按 `ansi.Wrap` 以终端格量宽换行、4 格缩进 dim 次级行、空 detail 零行），Notice 分支只加**一行调用**——`chat_tui.go` 已 5490 行且 file-size 在 ratchet 上，逻辑必须落在新文件里才不顶破预算（实跑 `repolint clean (1271)`，baseline 零改动）。测试 3 个（Detail 落 transcript 且 warn 保留 `!` 字形、无 detail 恰好一行、长文本与 CJK 换行不溢出帧）|
| R14.2 | 「Agent user 更新自动同步到调用它的成员」—— **复核后确认已实现，未加代码** | R12.1 | **已核实** | 用户要求：pool 条目改动（本次是补 `/v1`）要自动同步到所有在用它的成员。**核实结论：R12.1 的指纹已经覆盖，不需要新机制。** ① 非绑定态成员：缓存后端的指纹与新条目不符 → 下次 `bind()` 重建（`team_backends.go:83`，且**先装配新的再退役旧的**，运行中的回合不会被杀）；② 绑定态改配置的唯一入口是 `/model`，它已走 `rebindTeamBackend`（R12 那轮 P0 修的）；③ 「编辑 pool 时有成员正绑着」这个状态**到不了**——进 pool 屏必须先 esc 出会话，而 esc 就已 `closeSession` → `unbindTeamMember`。**我曾为此写了 `resyncBoundMember` + `teamBackends.stale()` 并接到 pool 分支上，自查发现 `teamSessionBound()` 在那条路径上恒为 false，是够不到的死代码，已整体撤回**——留下它就是我一直在批的那种「有测试、产品里到不了」。改为**把保证钉死**：`TestFingerprintSensitiveToRoleProxyEffort` 增加 BaseURL 用例（`https://x` vs `https://x/v1` 指纹必须不同），因为 BaseURL 正是本次故障的字段而原测试只变 Model。实测真实 pool：两条条目现在都拼出 `POST .../v1/chat/completions`。**唯一的既有例外**：成员正忙（Running/PendingPrompt/后台作业）时 `bind()` 按 §4.5 继续用旧后端，配置在该回合结束后的下一次 bind 才生效 |
| R15 | **回合收尾契约写进模型读得到的地方**（用户拍板方案 a） | R14.1 | **已完成** | 现场诊断（证据来自用户真实会话文件 `team-agent_designed_team-test-leader.json`，JSONL 逐条）：`[4] assistant` 出了可见答案且**无 tool_calls** → `[5] user` 是宿主追加的 `Protocol repair: finish this turn now…` → `[6] assistant calls=['finish']` → `[7] tool` `Turn finalization accepted by the host.`。**由此排除三项**：① 网关工具能力正常（finish 真的执行且被宿主接受）；② 与 `/v1`、effort 无关（那两项已在 R13 修好且装配通过）；③ 我一度怀疑 `[2]` 那条无配对的空 `tool` 消息污染了请求——**查证为误报**，它是 `session.go:158` 的 `LocalOnly` 决策收据，`provider/projection.go:32` 的 `if candidate.LocalOnly { continue }` 明确将其排除在 provider 投影之外（实测八条消息中仅它 `LocalOnly=true`），该线索已撤回。**真因**：契约由 `finalization.go` 强制执行，却从未在模型读得到的地方声明为强制——19,083 字符的 system prompt 里 `finish` 只出现一次且是无关散文（"clean up before finishing"），而 `FinishTool.Description()` 原文只讲「怎么调」（"Finalize this turn… Call finish exactly once, in a separate tool-call batch"），没有一个字说每轮**必须**以它收尾。模型第一次被明确告知是在违约之后的修复提示里，且**当场服从**——证明是「没被告知」而非「做不到」。代价是该成员每轮两次模型往返。**落法（拍板取 a：只改工具描述，不新增 system prompt 章节）**：新 Description 以强制性开头、给出后果、并与 `requestProtocolRepair` 的措辞对齐（`exactly once` / `only tool call` 两处共用词汇，两条消息因此说的是同一条规则）。**cache 影响已量化**：`SystemHash` **逐字节未变**（`c4a54b2f61c5e15f`，system prompt 未被触碰）；`ToolsHash`/`PrefixHash` 改变；`ToolSchemaTokens` 4298 → **4316（+18 tokens）**；`tool_schemas.json` 22939 → 23010 字节。golden 按其文档流程正规更新（`REASONIX_UPDATE_GOLDEN=1 go test ./internal/boot -run TestGoldenBaseline`），仅 `prefix_shape/provider_request/tool_schemas` 三个文件变动，`system_prompt.txt` 未改。**新增守卫** `TestFinishDescriptionStatesTheObligation`：断言的是义务的**词汇**而非字节（字节已由 golden 守卫，重复会让一次改词失败两遍），并同时校验描述与修复提示共用措辞。**变异探针**：把 Description 改回旧文案 → 语义测试与 golden **各失败一处**（共 2）。**提 PR 必须带**：`Cache-impact: medium - 工具 schema 前缀一次性变更（+18 tokens），system prompt 未动，全机器所有会话冷启一次` 与 `Cache-guard: TestGoldenBaseline + TestFinishDescriptionStatesTheObligation`；因未触及 `internal/config`、`memory`、`outputstyle`、`skill`、`boot` 的 prompt 组装，`System-prompt-review` 由 `check-cache-impact.sh` 判定是否必填 |
| R16 | **回合协议提醒挪到回合尾，观测后才启用**（用户拍板方案 ③，②备用） | R15 | **已完成，待现场验证** | R15 把强制性写进工具描述后**问题依旧**——模型读到了也不照做。抓请求确证我们这边无误：`request #1: tools=[finish] msgs=1 lastMsg=user(9 chars)`、`request #2: tools=[finish] msgs=3 lastMsg=user(201 chars)`，**两次请求的工具数组完全相同且都含 finish**，唯一差异是最后那条 user 消息是否明确要求收尾。**另外证伪两个我自己提出的假设**（都已撤回，勿再顺着排查）：① 适配器在「文本+tool_calls 同时出现」时丢调用——`openai.go:1085` 流结束后 `sort.Ints(order)` 逐个**无条件** emit，content 与 tool_calls 各自独立按 index 累积；② 历史里那条无配对的空 `tool` 污染请求——它是 `LocalOnly` 收据，`projection.go:32` 明确排除。**结论**：该模型只在紧邻的上一条 user 消息明确要求时才发 tool call。**落法**：新增 `internal/agent/turn_protocol.go` —— `<turn-protocol>` 瞬时块，措辞镜像 `requestProtocolRepair`（那条经验上唯一有效的消息），注入点紧挨既有先例 `withInterruptedRecovery`（`run_loop.go`），即**回合尾而非稳定前缀**。**关键设计：观测驱动启用。** `turnProtocolWatch`（`sessionRuntime` 的命名子状态，单标量，符合 REASONIX.md struct-state 要求；已登记进 `sessionstate_test.go` 的 `sessionReset`，与同类 provider 怪癖观测 `missingReasoning` 同侧）由 `requestProtocolRepair` 置位。于是**自行收尾的模型永不看到该块、零 token 代价**；不合规的会话第一轮付一次修复，之后每轮一次往返。这避免了「为一个模型的毛病给全体用户每轮加 ~40 tokens」。**cache 影响：零。** golden **无需重新生成即通过**（提醒只在修复之后出现，golden 捕获中无修复），`git diff` 仍只是 R15 那三个文件的 5 行；前缀 effect 守卫绿。测试 3 个（`reminderSensitiveProvider` 复现真实行为：第一轮 2 次往返并置位、第二轮**1 次**往返且块在 user 消息里且用户原文保留；合规模型全程无块且不置位；注入器幂等/未置位不注入/不与修复提示叠加/空回合不注入）。**变异探针**：不置位 → 失败；置位但不注入 → 失败。**待验证**：需用户现场跑第二轮确认警告消失；无效则转方案 ②（`finalization.go:35` 处已可读 `usage.FinishReason`，按 provider 作用域放宽 stop+可见答案为合法边界）|
| R17.1 | **放宽收尾判据：干净停止 + 可见答案即合法边界**（用户拍板方案 ②） | R16 | **已完成** | ③ 落地后现场仍报警——**但排查发现 ③ 从未生效**：全 sessions 目录 grep `turn-protocol` **零命中**。根因是另一个缺陷（见 R17.3）：leader 成员没绑到自己的会话文件，每次进团队都是全新会话，于是 ③ 的 per-session 观测标志每次归零，永远等不到「第二轮」。**因此不能判定 ③ 无效，只能判定它不可评估**。② 不依赖任何跨轮连续性、第一轮即生效，故直接落地。落点 `finishRequiredAtResponseEnd`：请求修复之前插入 `cleanStopEndsTurn` —— `finishCalls == 0` **且** `pendingFinalAnswer`（非空可见答案）**且** `usage.FinishReason == stop` 三条同时成立才接受该回合。**安全论证**：被放宽的守卫（`reasoningOnlyFinishHonoured` 注释）防的是「把截断/空回合当最终答案提交」，从来不是「带真实答案的回合」；三个条件正是排除退化情形的判据。且该函数只从 `handleFinalResponse`（无工具调用的最终响应）进入，接受它**不可能截断未完成的工具工作**。**outcome 诚实留空**、不臆造为 completed —— 空 outcome 本就是无 finish 工具的 provider 的常态（唯一消费者 `turn_events.go:329` 已能处理），臆造会把 blocked 报成完成。**未按 provider 作用域**（与我先前说法不同，理由记此）：需要它的模型都在 OpenAI 兼容网关后，provider 身份说明不了它是否主动发终止调用，名单填不对；保证安全的是那三个条件而非 provider 名。**cache 影响：零**（不动 prompt、不动 schema）。测试 5 组（干净停止一次往返且 outcome 空；退化情形逐条仍修复：无可见答案／length 截断／tool_calls 但无调用／完全无 usage；诚实 finish 仍声明 outcome；判据五个反例；纯文本 provider 不受影响）。**变异探针**：删该分支 → 1 处失败；去掉 finish_reason 检查 → **5 处失败** |
| R17.2 | 撤回 R15（工具描述的强制表述） | R17.1 | **已完成** | ② 落地后 R15 那句「Required at the end of every turn … the host will ask again」**不再准确** —— 干净停止且带可见答案的回合现在被接受、不会被退回。故恢复原描述并撤掉配套语义守卫测试。`git checkout internal/boot/testdata/golden/` 后 `TestGoldenBaseline` **直接通过**、golden 三文件 `git diff` 为 0 行 —— 证明描述逐字节回到原状，**R15 的缓存代价（+18 tokens、一次冷启）完全归零**。教训仍留 §8-40，只是不再用前缀承载 |
| R17.3 | **成员会话未绑到自己的文件（新发现，待修）** | — | **未修，已定位** | 证据：`test-leader`（gpt）的成员会话文件 `team-agent_designed_team-test-leader.json` **不存在**，只有其 `.inbox` 目录；该成员的回合历史落在 `20260826-123550.…-gpt-5.6-sol.jsonl`（**普通会话命名**，今日另有 `114713` 一组）。对照 `test member`（deepseek）**有** 自己的 `team-…json`。定位：`bindMemberSession` 在成员文件不存在时走 `ctrl.SetSessionPath(path)`；该调用重绑了 sidecar（`.inbox`/checkpoint/guardian，故成员路径的 `.inbox` 存在），但对话的持久化目标没跟着改 —— `Resume` 的职责含「重绑 executor session」，`SetSessionPath` 少了这一半；控制层另有 `SetFreshSessionPath`（专用于新建会话的路径）而此处未用。**后果按严重度**：① 成员首次进入会默默沿用 `boot.Build` 挑中的会话，可能不是自己的；② 成员历史不落 `team-…json`，每次进团队失忆，违反 D5；③ 同模型的两个成员可能落到同一会话文件，威胁 §3 单写者约束；④ 任何 per-session 状态（如 R16 的观测标志）每次进入即归零。**未修理由**：改动落在 `control`/`boot` 的会话绑定路径，需先定清 `SetSessionPath` 与 `Resume` 的职责边界（候选：改走 `SetFreshSessionPath`；或给成员装配传显式 session path 让 Build 不 auto-resume）。比协议警告更要紧，建议作为下一个独立节点 |
| R18.1 | **`update_goal` 的拒绝改为路标，不再是死路** | R17.1 | **已完成** | 现场新形状：`● update_goal` → `⊘ update_goal is only available while an active goal turn is running` → 协议修复警告。**这次模型发了工具调用**，所以它并非「不会发 tool call」——它发错了一个**本不该被提供**的工具。查明：`update_goal` 在 `HostControlToolNames()` 静态可见名单里，而 provider 可见面由 boot 期 `SetProviderVisibleTools(UnifiedProviderToolNames())` 一次定死（为前缀字节稳定），**`ProviderVisible(ctx)` 根本不是决定它出现在工具表里的那道闸**——它只在 `contextualToolGateOutcome` 里当执行期门禁。于是该工具永远被提供、无 goal 时必然被拒。落法（零 cache 影响）：拒绝文案改为**路标**——点明前提、声明未写入任何状态、并给出替代路径 `End this turn with finish instead.`。同一 switch 里 `complete_step` 早就这么做（指向 todo_write），只有 `update_goal` 是死路：模型为收尾而调它，被拒后无事可做，于是结束回合，协议随即报缺失终止调用。顺带把该 switch 抽到新文件 `gate_messages.go`（`contextualToolGateMessage`）——`execute_one.go` 由此 825 → **817 行**，低于其 repolint 预算（**不加宽 baseline，按 R0.2 先例抽函数**）。测试：新 `gate_messages_test.go` 钉住通用契约「每条 contextual 拒绝都必须点名替代路径」＋ update_goal 需声明前提与未写入；4 处既有断言随文案更新为同时校验前提与 `finish` 指路。**变异探针**：删掉 finish 指路 → 2 处失败 |
| R18.2 | `update_goal` 描述补前提（唯一的前缀变更） | R18.1 | **已完成** | 原描述写「Report this turn disposition for the active goal. **Call it at the end of every goal turn**…」——模型无法判断「这是不是 goal turn」（无活动 goal 时对话里也没有任何信号），于是读成「每轮结束时调它」，并因此**以为已经报告过收尾而不再调 `finish`**：两个工具都自称回合结束时调，模型选错了那个。改为前提先行：Only usable while a goal is running — with no active goal it is refused and you should end the turn with finish instead。**cache 影响已量化**：`SystemHash` **逐字节未变**（`c4a54b2f61c5e15f`，system prompt 未触碰）；`ToolsHash`/`PrefixHash` 变；`ToolSchemaTokens` 4298 → **4331（+33）**；`tool_schemas.json` 22939 → 23072 字节。golden 按其文档流程正规更新（`REASONIX_UPDATE_GOLDEN=1`），只动 `prefix_shape`/`provider_request`/`tool_schemas` 三个文件。**与 R15 的区别**（R15 已撤）：R15 试图用前缀去**强迫**模型服从、结果无效；本项是**移除一句会误导模型的指令**，属纠正而非施压 |
| R18.3 | 既有机制复核：该场景早已有界 | R18.1 | **已核实** | `delivery_hardening_test.go` 里本就有两个测试覆盖此形状：`TestNonGoalUpdateGoalWithVisibleTextDoesNotSpendRepairRound`（**带可见文本时不花修复轮次**）与 `TestNonGoalToolOnlyUpdateGoalGetsAtMostOneRepairRound`（**纯工具调用最多一次修复**）。用户日志正是后者：`update_goal` 无伴随文本 → 被拒 → 花掉那一次修复。即该行为在设计内且有上界，R18.1/R18.2 是把「误发」在源头掐掉并让拒绝可行动，而非修一个失控缺陷。**记此以免后来者把它当新 bug 重查** |

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
| 16 | 测试助手绕过键盘路由会掩盖反转 | `teamKey` 原本直调 `handleTeamPickerKey`，绕过了 R2.3 新增的 `handleTeamKey`。修为先走 `handleTeamKey`，未被消费则转发给 `m.Update`——与生产完全同形。**写 TUI 测试助手时必须走真实入口**，否则键盘所有权的改动测不出来 |
| 17 | 换成员/退出会话必须清 composer 草稿 | 草稿属于它被写给的那个后端。不清的话，为成员 A 写的一句会在切到 B 后被提交给 B。`bindBackend` 里 `m.input.SetValue("")`。代价是丢一次草稿，比投错对象轻 |
| 19 | `sessionstore` 残留的导出 API（R4.4 未做的一半） | `Messages`/`AppendMessage`/`ReadCursor`/`WriteCursor`/`ReadState`/`WriteState` 已无生产调用方，但作为导出符号 `unused` 不会拦。**留着的风险**是有人再次把成员历史写进 `.reasonix/team/context/`，绕开 D5 的会话文件语义。收口时应连同 `contextRootDir` 常量一并删除，只留 `WriteSelection`/`ReadSelection` 与 `MemberDirs`/`ClearTeamTrash`（后两者仍被 leader 解除用于清理 pre-D5 残留树） |
| 20 | 成员后端 `Close()` 在 `Update` 内同步执行 | `teamBackends` 的退役路径（超限淘汰、`release`、`releaseTeam`）在 Update 处理器里同步调 `Close()`，而 `/model` 切换那条路径**故意**把 `oldCtrl.Close()` 延后成一个 `tea.Cmd`（`model.go:70-100` 注释：Close 会跑 SessionEnd hook 与杀插件子进程，从 goroutine 里做会破坏 bubbletea 的 raw mode）。目前未观察到问题，但若淘汰/清理时出现终端残留，应照 `modelSwitchMsg` 的形状把 Close 延后 |
| 18 | R2.3 换掉的按键（旧测试据此更新） | 成员切换 `up`/`down`/`j` → **`ctrl+up`/`ctrl+down`**（绑定态方向键归 transcript、字母归 composer）；`r` 重启入口随 `restartSessionTarget` 一并删除。`chat_tui_team_session_composer_test.go`（9 个迷你 composer 测试）与 `chat_tui_team_session_events_test.go`（8 个 legacy 事件/订阅测试）已删除——覆盖点由 `chat_tui_team_switch_test.go` 与 `chat_tui_team_keyboard_test.go` 的新路径测试承接 |
| 21 | 「overlay 是否在」不等于「overlay 是否模态」 | 绑定态下 overlay 存在但**不模态**（composer 是成员输入）。凡按 `m.teamPick != nil` 分流的地方都会在绑定态误判：R2.3 的粘贴守卫、R7.2 的 `MouseMode` 与图片粘贴，三处同族。**判据一律用 `m.teamOverlayModal()`**；新增任何 overlay 分流点时同理。`MouseMode` 那处尤其隐蔽——它不报错，只是让整个鼠标层静默失效 |
| 22 | 假通过的测试比没有测试更坏 | `TestTeamButtonSessionKeepsKeysFromChatComposer` 断言"会话键绝不到达 composer"，与 R2.3 的设计**正好相反**，却一直绿：`tea.KeyPressMsg{Code: 'x'}` 不带 `Text` 字段，textarea 收到也不插入任何字符——它测的是构造函数的空缺，不是产品行为。已改写为 `TestTeamButtonSessionRoutesTypingToTheComposer`（带 `Text` 且经 `m.Update` 全链）。**写 TUI 键盘测试必须填 `Text`**，否则断言恒真。反向陷阱同样存在：`{Code:'t', Mod:ModCtrl, Text:"t"}` 的 `String()` 是 `"t"` 而非 `"ctrl+t"`，和弦测试**不能**填 `Text` |
| 23 | ~~**repolint 门禁在 HEAD 已红**~~ **已解除（2026-08-25 实跑 `clean (1269 baselined findings)`，全量 `go test ./...` exit 0）** | 原文：`make lint` 的 repolint 段当前失败：`boot.go`（complexity 265/264、file-size 2088/2078、function-size 1732/1726）、`chat_tui.go`（file-size 4701/4684、function-size 1301/1299）、`shell_completion.go`（118/117）、`chat_tui_test.go`（3598/3592）、repo test-file-size 总量 69387/69381。**四个文件本轮一处未动**；已用 `git stash` 前后两次实跑对比，findings 数字逐字相同 → 本轮增量为 0。成因：合并 `fa0621c71` 使 `boot.go` +40 行而 baseline 未随之刷新（`baseline.json` 最后一次刷新在 `3ebf6126f`）。按 REASONIX.md「不得加宽 baseline 以落地改动」，**本轮不执行 `-update`**；该刷新应由造成漂移的合并单独承担并在其 PR 说明 |
| 24 | 一键退出必须先于所有状态归属者判定 | 团队 overlay 里有五类"占住整个键盘"的状态（写状态、池屏、step-down、打开的字段、绑定态 composer）。退出键若排在它们之后，**恰好在用户最需要它的深度失效**——被吞掉且无任何提示。`exitTeam` 的判定因此放在 `handleTeamKey` 第一行，代价是 `ctrl+t` 在 overlay 打开期间遮蔽 textarea 的 transpose 绑定（overlay 之外不受影响）。新增任何"全局逃逸键"时同理 |
| 25 | ~~面板隐藏态没有任何提示面~~ **错误可见性已修（R13.1）；帮助文本仍只在面板里** | R7 之后绑定态默认不渲染面板，于是会话里唯一的帮助文本（面板脚注）也默认不可见——`ctrl+t` 在该状态下不可发现，发现路径是 `[ TEAM ]` → 面板 → 脚注。未做：把提示上状态行（那一行宽度已被成员按钮条占用，封顶 6 个）或写进 composer placeholder（涉 i18n）。若用户反馈找不到，优先做状态行 |

> **以下 #26~#33 是 2026-08-25 退出逻辑复审的结论，均以实跑探针为证（一次性探针 `TestProbeExitPaths`，未入库）。**
> **等用户对齐颗粒度后才开修复节点**：本表只记事实与判据，不预设方案。

| # | 风险 | 处置 |
|---|---|---|
| 26 | ~~**P0 回归：普通 esc/q 退出会永久降级 `[ TEAM ]` 的入口语义**~~ **已修，R11.1** | `teamSuppressAutoSession = true` 写在 `exitTeam()` 里，而 `exitTeam()` 是**所有** overlay 关闭路径的共用拆卸点（`chat_tui_team.go:419`，`handleTeamSharedKey` 返回 closed 即调）。于是从团队列表 `esc`、`q`+确认——**什么都没做就退出**——都会置位 suppress。实跑：`[ TEAM ]` → esc → esc → 再点 `[ TEAM ]` → `session=(inactive)`，**不再进 leader 会话**，与最初拍板的「点 [ TEAM ] 默认显示 leader 会话」（§11.4）相反。解除方式只有 roster 上按 `t`，界面无任何提示说明入口为何变了 → 实际是**单向棘轮**。来源 `f590a50eb`（`c79ee9ccc` 的 exitTeam 里两者都没有）。意图是「与 x 对齐」，但挂点该是显式退出键，不是共享拆卸点 |
| 27 | **`x`「exit all」在它声称要退出的状态里按不到** —— **门禁与反馈已修（R11.2）；是否保留该键待裁决** | 绑定态只消费保留键（`ctrl+up/down`/`esc`/`ctrl+t`），字母全归 composer——`x` 只能在管理页按，而**能到管理页就意味着会话已被 esc 关掉**，`exitAllTeamSessions` 里的 `m.closeSession()` 恒为 no-op。真正生效的只剩两件不可见的事：清磁盘 selection + 置 suppress。实跑 roster 按 `x`：transcript +0 行、errMsg 空、屏幕零变化，却改了下次入口行为。另：`x` **没有** `memberListKeyAllowed` 门禁（`t`/`k` 都有），已武装的删除确认下按 `x` 仍执行（实跑 `inputKind` 仍为 4 且 suppress 已置位），团队列表页与成员编辑器页也能触发，而只有 roster 帮助行写了 `x exit all`。名字亦无对应物：UI 里同时只存在一个会话窗口，"all" 指向空 |
| 28 | ~~三条退出路径的持久化语义各不相同~~ **随 #26 收敛（R11.1）**：现在只有两类——`esc`/`q` 是导航（不碰偏好），`ctrl+t`/`x` 是退出（写盘）。原文见下 | esc（会话→列表）：关会话／**留** selection／不 suppress；esc（列表→关闭）与 `q`+确认：**清** selection／**置** suppress；`ctrl+t`：三者全做；`x`：会话 no-op／清 selection／置 suppress／**无反馈**；`k` 解除：清 selection／不 suppress。后果是**同一个"退出"意图，走 esc 还是 ctrl+t 决定下一次入口落在哪**，而两者在屏幕上完全一样 |
| 29 | 持久化 selection 近乎空转 —— **team-name 隐患已收口（R11.1 的 `exitingTeamName`）；`Suspended` 现在是真正承载行为的那一位** | `restoreSession()` 只在「记住的成员本身是 leader」时恢复它，否则回落 `firstLeader()`；多 leader 已被 `assignFocusedLeader` 显式拒绝（一团队一 leader）→ 恢复结果恒等于 `firstLeader()`，清不清 selection **观察不到差别**。更矛盾：`[ TEAM ]` 点击入口（`restoreSession`/`openSession`）**从不写** selection，只有 `t` 与 `ctrl+↑/↓` 写（实跑：点击进入后 selection 仍为 `""`）。所以 R10 记为 P0 的"三退出路径 selection 不一致"，那一项本身没有行为后果；真正改变行为的是 suppress 标志，而它挂错了位置（#26）。另：`exitTeam`/`exitAllTeamSessions` 用 `p.model.Name()`（焦点团队）写 selection，`persistSessionSelection` 用 `p.session.teamName`（绑定团队）——今天同步只因绑定态无法导航，两个来源写同一把键是隐患 |
| 30 | ~~状态栏成员按钮在管理页可见但点不动~~ **已修，R11.3** | `statusMemberIDs()` 无会话时回落 `model.Members()`，所以管理页也渲染 `[ lead ] [ alice ]`；而管理页模态 → `MouseMode = MouseModeNone`（鼠标交还终端做文本选择）→ 这些按钮收不到点击。**可见 + 不响应**，与 §8-21 是同一处判据的另一面 |
| 31 | 从会话回到管理页要四步，落点是团队列表而非 roster —— **未做（导航重设计，需拍板）** | 实跑 `[ TEAM ]` 进入后 `model.Mode()` 是 `teams`，会话只是叠在团队列表上。esc 关会话后落在**团队列表**，要 `Enter` 进 roster、`↓` 选中 leader、再 `t` 才回到会话——原本一次点击的事变成四步，也是 #26 那个棘轮让人难受的放大器 |
| 32 | k 路径的 m 层解绑缺口 —— **未做（不可达，靠键路由而非代码保证；见处置末尾的守约条件）** | `closeTeamSessions()` 只清 `p.session`，其注释自认 m 层解绑「是 TUI 的职责」，但 k 路径无一处调 `unbindTeamMember()`；而 `stopTeamBackends` 已把该团队所有后端 `Close()` 掉了。今天不可达只因 `k` 在绑定态按不到（字母归 composer，同 #27）。一旦给 k 加 ctrl 和弦、或允许从会话内触发 step-down，就会留下 `m.ctrl` 指向已 Close 的后端 |
| 33 | ~~仓库混入构建产物与 npm 依赖（R0.1 同族）~~ **已修，R11.4** | `f590a50eb` 提交了 **`reasonix-blackboard` 10,233,030 字节可执行文件**（mode 100755，未被任何 `.gitignore` 覆盖），以及仓库根新增 `package.json`（唯一依赖 `@anthropic-ai/claude-agent-sdk`）+ 1507 行 `package-lock.json`。前者与 R0.1「运行态/产物文件撤出版本库」同类，且每次重建都会产生一个新的 10MB blob；后者需说明一个 Go 仓库为何在根上引入 npm 依赖树。建议 `git rm --cached reasonix-blackboard` + `.gitignore`；npm 依赖若为外部桥所需，应落到自己的子目录并在路线文档说明 |
| 34 | **golangci-lint 门禁在 HEAD 已红，且在增长** | 2026-08-26 实跑（CI pin）：**70 findings**（上轮 58，`17513305b` 又添 12）。分布：`internal/team` 58、`cmd/reasonix-blackboard` 8、`internal/cli` 3、`internal/team/scheduler` 1；构成 `intrange` 54、`modernize` 9、`errcheck` 4、`unused` 1、`ineffassign` 1、`errorlint` 1。**70 条里 62 条在测试文件、63 条可 `--fix` 机械修**，真正要人看的 7 条是：`blackboard_export.go:43` `tx.Rollback` 未检查、`blackboard_sqlite.go:137/141/305` 三处未检查返回值、`scheduler/runtime.go:73` `fmt.Errorf` 未用 `%w`、`option_list_integration_test.go:109` 无效赋值、`blackboard_binding.go:54` `transitionTimeout` 未使用。仍与本文档的改造无关（我方改动零命中），按铁律记录真实阻塞、不改写为通过；宜由黑板路线一趟收口 |
| 35 | **同名包回归：`agentruntime` 是新的那个，不是被删的那个** | R4.1 整包删除 `internal/team/agentruntime/`（15 文件 2364 行）并在 §7 记「符号零残留」；`17513305b` 用**同一路径同一包名**新建了 7 个实现文件。两者语义相反（旧的绕过 `control` 直连 provider，新的只 import `internal/team` 并由宿主适配 `control.Controller`），但**同名**会让任何按 §1/§7 R4.1 检索的人得出错误结论——包括未来的自动化审计。已在 §1 加事实修订。**建议**：若要长期保留，改名为其职责（如 `taskruntime`）比留下同名歧义便宜 |
| 36 | **durable inbox 的 Ack 发生在提交时点，回合失败无回滚** | `teamInboxWire.inject` 的顺序是 `Fetch` → **`Ack(next)`** → 拼 `[command inbox]` 块 → 交给 `SendWithRaw`。注释解释的是「Ack 失败则 watermark 留后、下回合重放」，但没有覆盖**Ack 成功而回合随后失败**（provider 报错、用户 ctrl+c 打断、lease 丢失）：指令已从 durable inbox 消费掉，却从未到达模型。实跑确认 `chat_tui_team_inbox.go`/`agentruntime/inbox.go`/`watermark.go` 里**无任何 requeue/rollback/Nack 路径**，`chat_tui_team_inbox_test.go` 的 5 个用例也无一覆盖回合失败。这是 at-most-once，而黑板路线 §2.2 要的是幂等 at-least-once。**属黑板路线的子系统，未擅自改**；建议把 Ack 移到回合成功之后，或让 watermark 推进与回合完成同一个提交点。另附：`inject` 在 UI 线程上同步做 SQLite `Fetch`+`Ack`（有 `teamBoardTimeout` 兜底），即在 Bubble Tea 的 update 里做磁盘 I/O——今日有界，压力下是卡帧来源 |
| 37 | 团队运行时宿主三个 seam 无生产调用方（§8-19 同族） | `boot.ControllerAsAgent`/`RecoverTeamRuntime`/`WireTeamInjector`、`scheduler.RuntimeScheduler`、`team.SQLiteStore.SaveTask/LoadTask/LoadLiveTasks` 的非测试调用方为零：装配完整、测试充分（`team_runtime_host_test.go` 5 用例含 kill/reopen 与 member-gone 持久化、`team_injector_test.go` 4 用例含全链路），但 `cmd/`、`desktop/`、`cli.go`、`serve/` 都不引用。风险与 R4.4 残留导出 API 相同：**「有测试」被读成「产品里已生效」**。§7 R12.5 因此记「未接线」。接线前它不改变任何用户可见行为 |
| 38 | 合并提交带着冲突标记落库（过程缺陷，已被下一提交修好） | `44eabe76f`（合并 upstream）把 `go.mod` 里的 `<<<<<<< HEAD` / `=======` / `>>>>>>> upstream/main-v2` 一起提交了——该提交处的仓库**不可构建**；`4e6f192de "update upstream"` 才删掉标记（保留 `gorilla/websocket v1.5.3`）。本轮已全仓库复扫 `*.go/*.mod/*.sum/*.md/*.json/*.yml` 冲突标记：零命中，HEAD 干净。**教训**：合并后至少跑一次 `go build ./...` 再提交；`git grep -nE '^(<<<<<<<|>>>>>>>|=======)$'` 是一条秒级的提交前检查 |
| 39 | 事件的 `Detail` 是第二个「原因存在但不给看」的现场 | `event.Event.Detail` 的注释说它是「expandable details」，但 CLI 的 Notice 分支只渲染 `Text`，没有任何 frontend、trajectory 或日志消费它（`grep` 全仓库确认）。协议修复警告因此把五种截然不同的失败压成同一句话。R14.1 已让 Notice 落地渲染；**其余带 Detail 的事件种类未逐一排查**，新增事件时应默认「Detail 必须有渲染出口」，否则等于没生成。与 §8-25（会话 errMsg 只在隐藏面板里）是同一族：诊断信息的生成点和呈现点必须成对存在 |
| 42 | 静态可见面 + 上下文门禁 = 必然出现「提供了却会被拒」的工具 | provider 可见面为前缀稳定而在 boot 期一次定死（`SetProviderVisibleTools(UnifiedProviderToolNames())`），但 `ContextualTool.ProviderVisible(ctx)` 只在执行期当门禁。二者组合的必然结果：这类工具**每轮都被提供**，在不满足前提时**必然被拒**。`update_goal`（需活动 goal）、`complete_step`（需计划批准）、后台作业三件都在此列。**因此每条这类拒绝都必须是路标而非死路**——模型是为推进才调它的，不给替代路径它就无事可做、直接结束回合，而回合协议随即把这记成缺失终止调用（R18.1 即此）。并且此类工具的**描述必须先声明前提**，否则模型会把「在 X 时调用」读成「总是调用」（R18.2 即此）。与 §8-40 同源：宿主的判据必须对模型显式声明 |
| 41 | 「改了没效果」先证明改动真的生效了 | R15 与 R16 都被现场判为「无效」，但 R16 实际**从未执行**：全 sessions 目录 grep `<turn-protocol>` 零命中，因为成员会话未绑定（§7 R17.3）使 per-session 标志每次归零。差点据此错误否定一个正确的机制。**判据**：报「改动无效」之前，先找到该改动应留下的可观测痕迹（历史里的块、落盘的字段、golden 的漂移）；找不到痕迹就是没生效，不是无效。R15 的验证同理——二进制 mtime 20:33 晚于源码 20:05 才证明它真进了那次运行 |
| 40 | 强制执行的契约必须在模型读得到的地方声明 | `finish` 的回合收尾要求由 `finalization.go` 机械强制，但 R15 之前只在**违约后**的修复提示里被说明；system prompt 与工具描述都没有声明它是强制的。后果不是偶发失误而是**结构性的**：不会自行推断的模型每轮都多付一次往返。**通用规则**：凡宿主会拒绝的行为，其规则必须出现在模型下一次决策前能读到的表面（工具描述或 system prompt），否则强制执行等于每轮罚一次而从不告知。检查新增的宿主级契约时按此判据。另注：`internal/provider/openai/openai.go` 的 effort 白名单**按模型族分支**，同类「宿主判据未对模型声明」的地方值得一并排查 |

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
| v0.9 | 2026-08-23 | **R2.4 / R3 / R4 / R5.1-5.3 完成，方案节点全部走完**（R4.4 部分、R3.2 折行与状态栏徽标降级，见 §7 与 §8-19）。R4.1 整包删除 `internal/team/agentruntime`（15 文件 / 2364 行），§6 里那四个并发 P0/P1 随之闭合。R2.4 `/model` 在绑定态改成员池条目并退役旧后端。R3 状态栏成员按钮条 + 命中测试 + 点击绑定。R5.1 AgentType 纯命令词白名单。**并修掉一个 R4 引入的空承诺**：leader 解除原本只清空已无人写的 legacy context 树，真实历史（会话文件）会存活——新增 `clearTeamHistories`。新增 §8-19（sessionstore 残留导出 API）。门禁：gofmt/vet/`CGO_ENABLED=0 build`/`go test ./...` 全量 exit 0/`-race`(cli+team) 绿/`make lint` 0 issues/repolint clean(1275) baseline 未动 | — |
| v0.8 | 2026-08-23 | **R2.3 完成，R5.4 随之闭合**：键盘所有权反转（绑定态只保留 `ctrl+up`/`ctrl+down`/`esc`，其余归主 composer），迷你 composer 与 legacy 发送/订阅入口删除，提交经 `m.ctrl.SendWithRaw` 直达成员——无第二条发送路径。修掉本节点自引入的粘贴丢弃缺陷（模态守卫改 `teamOverlayModal()`）；换后端清 composer 草稿。§6 三条 P0/P1 标为已闭合。新增 §8-16（测试助手必须走真实键盘入口）、§8-17（草稿归属）、§8-18（换掉的按键与删除的 17 个旧测试及其覆盖承接）。门禁：gofmt/vet/`CGO_ENABLED=0 build`/`go test ./...` 全量 exit 0/`-race`(cli 33.4s) 绿/`make lint` 0 issues/repolint clean(1275) baseline 未动 | — |

### 10.1 R0 收尾说明

- **未用 `.gitignore` 兜底**（原 R0.1 计划项）：根因已修，且 `/context/`、`/session/`
  这类通用名字加进 `.gitignore` 会把将来真实的同名泄漏一并静默隐藏。改为用
  `TestSessionPathsStayUnderTeamRoot` 双向断言（正：文件在团队根下；反：项目根下
  不存在）。`.reasonix/*` 本来就已被忽略。
- **`mouseCopyOrPaste` 落在新文件而非 `chat_tui_paste.go`**：后者已 781 行，加进去撞
  800 行文件上限。鼠标复制/粘贴约定是独立职责，`chat_tui_mouse.go` 单独承载。
- **`git rm --cached` 的删除已进暂存区**，尚未提交——提交时机留给用户。

---

## 11. 交接（2026-08-23）

> 接手者先读 §7 进度表（唯一进度来源），再读 §8 的 18 条实施记录——每条都是一个卡点的
> 根因与教训，不必重新推导。§6 缺陷表逐条标注了"已闭合 / 未修"，**不要据节点状态推断缺陷已修**。

### 11.1 已提交 vs 未提交

- **已提交**：`0306f11a9`（删 `AGENTS.md` + 泄漏的 session 文件）、`1c0bb50dd` 及其之前
  —— 覆盖 R0、R1、R2.1、R2.2、R5.5。
- **未提交（工作区，全绿）**：R2.3 那一批，即键盘所有权反转：

  ```text
   M internal/cli/chat_tui.go                        # hideComposer 与 update 的 team 分支
   M internal/cli/chat_tui_paste.go                  # 模态粘贴守卫改 teamOverlayModal()
   M internal/cli/chat_tui_team.go                   # t 键改走 chatTUI；teamPasteTarget 去 session 分支
   M internal/cli/chat_tui_team_member.go            # 同上
   M internal/cli/chat_tui_team_render.go            # 面板降级为成员选择器
   M internal/cli/chat_tui_team_session.go           # 删迷你 composer / sessionSend / restartSessionTarget
   M internal/cli/chat_tui_team_switch.go            # teamSessionBound / teamOverlayModal / handleTeamKey
  ?? internal/cli/chat_tui_team_keyboard_test.go     # 新：键盘所有权 3 测
   D internal/cli/chat_tui_team_session_composer_test.go   # 迷你 composer 9 测，已被取代
   D internal/cli/chat_tui_team_session_events_test.go     # legacy 事件/订阅 8 测，已被取代
   M internal/cli/chat_tui_team_{entry_state,lifecycle,paste,session,test}_test.go
   M internal/cli/team_session_acceptance_test.go    # 按键 down→ctrl+down 等契约更新
   M docs/team-mcp-port/TEAM_TUI_AGENT_DECOUPLING_PLAN.md
  ```

  删除的 17 个旧测试的覆盖承接见 §8-18。

### 11.2 复现门禁（提交前必跑）

```bash
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH   # go 与 golangci-lint 不在默认 PATH
gofmt -l . | grep -v '^cache/'
go vet ./...
CGO_ENABLED=0 go build ./...
go test ./... -count=1
go test ./internal/cli/ -race -count=1
make lint                    # 必须 0 issues
go run ./tools/repolint      # 必须 clean，且 tools/repolint/baseline.json 零改动
```

最后一次实跑结果：全部通过，repolint `clean (1275 baselined findings)`。

### 11.3 下一步（按建议顺序）

1. **R2.4** —— `/model` 写回当前成员 `AgentUserRef`（§8-5 已拍板）。
2. **R3** —— `[ TEAM ]` 旁成员按钮条，用户原始诉求的最后一块；命中测试复用
   `teamButtonHit` 的 `ansi.Strip` + `visibleWidth` 形状。
3. **R4** —— 拆除并存的旧 agentruntime 栈。**这一步同时闭合 §6 里仍标"未修"的四条
   并发缺陷**（`nextSeq` 数据竞争、`Subscribe` after `Close` panic、终态事件对已有
   订阅者永久丢失、`Registry.Close` 跳过 stopped 实例致事件源泄漏）。
4. **R5.1/5.2/5.3** —— AgentType 白名单等遗留。

### 11.4 提 PR 时的元数据

`internal/boot/` 被改过（`Options.SystemPromptIdentity`），两道 CI 守卫会读 PR body：

```text
Cache-impact: none - 空值零插入，前缀逐轮字节稳定，effect 测试守卫
Cache-guard: TestEffectSystemPromptIdentityReachesProviderAndStaysStable
System-prompt-review: <必须点名评审人，该字段拒收 none/n/a>
```
