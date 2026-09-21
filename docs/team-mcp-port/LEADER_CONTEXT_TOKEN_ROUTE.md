# Leader 上下文的前缀 token 成本：实测与六条修复路线

> 状态：**实测 + 路线稿（2026-09-20）**。测量对象是真实运行数据，不是估算模型。
> R1/R2/R3/R4/R5 已落地并闭环（R1 两侧、R5 三步）；R6 待排期。
> R5 与 R1 的 MCP 侧改动落在独立仓库 `mult_agent_mcp`，不在本仓库 PR 内。
> 红线：不改写 cache-stable prefix 的字节稳定性契约（`REASONIX.md`）；不改 provider 可见
> 工具 schema 的字节稳定性；不扩宽 repolint baseline。

## 0. 结论摘要

`internal/team/events/events.go` **不是** leader 获取 member 状态的机制 —— 它是纯类型/接口桩
（`doc.go` 自述 "no implementation body this round"，NG1），全仓库 **0 个实现、0 个导入者**。
真正的机制见 §2。

56K 不是"获取一次状态"产生的：**状态查询本身只花 ~48 token**。56K 是整条 provider 前缀被
重新发送的结果，而轮询频率把它乘以了几十次。

---

## 1. 实测数据

样本：`~/.reasonix/projects/-home-zwc-cpp_ipc_dds/sessions-v4/4330cb0e…/events.frames`
（zstd 分帧，2697 条记录 / 206 条消息），以及 `~/.reasonix/stats/2026-09-19.jsonl`
（22:58–23:28 窗口，80 轮请求）。

### 1.1 前缀构成

| 组成 | token（CJK 感知估算） | 说明 |
|---|---:|---|
| system prompt | 4,981 | 19,396 字符 |
| transcript | 37,905 | 占 79% |
| ├ tool results | 31,221 | 占 transcript 82% |
| ├ user msgs | 5,687 | |
| └ assistant msgs | 997 | |
| tool schemas | 4,797 | 核心 15 个 14,666B + leader 团队工具 18 个 4,525B |
| **合计** | **~47,700** | 与观测到的 50–62K 同量级（133 条记录落在此区间） |

窗口内 prompt 中位数 **52,934**，均值 43,242，最大 123,468。

### 1.2 状态查询自身的成本

- `leader_check_member_status` 输出：78–242 字符，**均值 138 ≈ 48 token**。
- 45 次调用中 **37 次**被 `internal/agent/duplicate_result.go` 去重成存根，整个会话省 8,624 字符。
- 工具输出侧已经接近最优 —— **56K 里没有一分钱是它花的**。

### 1.3 真正的驱动：频率 × 前缀

- 45 次查询分布在 1,358 秒内，间隔中位数 **15.2s**，20 次 <15s，10 次 <5s。
- 每次查询 = 一次完整 provider round-trip，重发整条前缀。
- cache_hit 均值 32,674 / miss 均值 **10,569**。80 轮 ≈ 845K 全价输入 token，
  只为轮询一个 48 token 的状态字符串。

### 1.4 前缀里的最大单项贡献者

| 贡献 | 量 | 来源 |
|---|---:|---|
| `member_read_deliverable` | 13,106 字符 ≈ 5.7K tok | leader 把整份验证报告读进上下文，永不释放 |
| `read_file` ×8 | ≈11K tok | 源码进上下文 |
| `bash` ×36 | 7,361 tok | 单次最大 8,000 字符 git log |
| `<team-role-skill>` 段 | 13,975 字符 ≈ 3.5K tok | base/leader 6,973B + shared/deliverable 5,004B + special/leader/member-create 2,842B |
| 首轮 user 消息 | 7,248 字符 ≈ 3.1K tok | `<session-context>` 3,208 + 内联引用的 `docs/uf010_evidence_registration.md` 全文 |
| `<reasoning-language>` | 179 字符 × 4 次 | 每条 user 消息重复注入 |

### 1.5 排除项：MCP 工具 schema 不是本会话的原因

MCP `tools/list` 载荷实测 63,555 字节 / 76 个工具（CJK 感知估算 **17.5K token**），但解码后的
frames 里 `mcp__` 出现 **0 次** —— `/home/zwc/cpp_ipc_dds` 下既无 `.mcp.json` 也无
`reasonix.toml`，leader 走的是 Reasonix 原生 `leader_*` 工具（18 个 / 4,525 字节）。

**但 Claude Code 当 leader 时这条成立**：`~/.claude/projects/-home-zwc-mult-agent-mcp/` 下的
会话单次状态查询后 `cache_read=89,472`，那 17.5K schema 每次请求都在前缀里。

---

## 2. 真实的状态获取机制（链路）

| 层 | 位置 | 作用 |
|---|---|---|
| 工具声明 | `internal/cli/team_member_tools.go:80` | schema，ReadOnly，PlanModeSafe |
| 工具分发 | `internal/cli/team_member_tools.go:193` | → `service.checkStatus()` |
| 实际读取 | `internal/cli/team_task_service.go:573` | `teamStore.Load()` + `board.LoadLiveTasks()` |
| 状态判定 | `internal/cli/team_task_service.go:625` `memberTaskState` | `runtime.LiveTask()` 区分 working/stalled/queued |
| 执行注册表 | `internal/team/agentruntime/runtime.go:368` | `LiveTask` —— 只有后端接受 turn 才算在跑 |
| durable inbox | `internal/team/agentruntime/inbox.go:62` | `BoardInbox.Fetch`，watermark 增量读 |
| Python MCP 侧 | `mult_agent_mcp.py` `leader_check_member_status` | 读 `last_observed_state`（60s 节流同契约） |

`memberTaskState` 已能区分三种态，但 leader 仍然**盲轮** —— 没有任何机制在状态未变时抑制查询。

---

## 3. 六条修复路线

### R1 状态轮询降频（最小间隔 60s）— 本次落地（两侧）

**问题**：45 次查询 / 1,358 秒，中位间隔 15.2s，10 次 <5s。每次都是一次完整 round-trip。

**覆盖面**：这条有两个独立实现 —— Reasonix 原生（Go）与 Claude Code 走 MCP（Python）。
实测调用量 68 : 122，**轮询主要发生在 MCP 侧**，因此两侧都必须改。

**落地**（`internal/cli/team_status_poll.go`）：
- `teamTaskService` 增命名子状态 `statusPoll`（`statusPollState`：按查询键记忆上次答案 + 时间）
  与可注入时钟 `now func() time.Time`；`minStatusPollInterval = 60s`。
- `checkStatus` 拆成两层：`readStatus` 是**无条件**的纯读，`throttledStatus` 只决定要不要
  抑制**回复**。节流不能跳过读取 —— 跳过就读不出"变了"，而变了的必须立刻到达 leader。
- 答案未变且在间隔内时返回 `unchanged (next read in Ns)\n<原样 roster>`；roster 逐字重复，
  provider 前缀保持字节稳定，只有尾部不同。
- 按 `memberID` 分桶（`""` = 整个 roster），一个成员的轮询不饿死另一个。
- `forTeam` 的子服务继承时钟。

**边界**：只影响 leader 的**读**；调度、报告、wakeup 路径不变。60s 是下限不是节流上限 ——
状态**变化**时（成员完成、任务失败）绕过节流，leader 不会等满 60s 才看到完成。

**验收**（`internal/cli/team_status_poll_test.go`）：首次放行、60s 内重复被抑制且 roster 逐字
重复、跨过间隔放行且剩余秒数递减、状态变化绕过节流、不同 member 独立计时。

### R2 deliverable 正文不进 leader 上下文 — 本次落地

**问题**：`member_read_deliverable` 单次 13,106 字符，读一次永久驻留。
实测驻留成本（真实时间轴，读之后还有 43 轮）：

```
6,446 字符 = 2,823 tok  →  43 轮  = 121,378 tok
13,106 字符 = 5,452 tok →  43 轮  = 234,414 tok
                          R2 合计 = 355,793 tok
                = 会话总前缀 3,459,393 的 10%
```

这两份 deliverable 落在 `23:11:10`，紧接着 `23:11:24` 出现一次冷缓存轮
（prompt 从 ~31K 跳到 47,968，`cache_hit=None`）—— 所以 R2 不只省驻留，还减少冷轮。

**落地**（`internal/cli/team_deliverable_view.go`）：
- 读工具默认返回 **outline**：`id` + 字节数/行数 + markdown 分节标题 + 开头摘录。
- `mode="full"` 才返回正文；`offset`/`limit` 分页，单页上限 32 KiB、默认 8 KiB。
- `mode` 未知值**拒绝**而不是静默降级 —— 想读正文的调用方不该以为自己读到了。
- 无分页参数且文档能一页装下时，`mode="full"` 逐字返回正文（不带头部噪音）。
- 标题扫描跳过代码注释（`#include` 不是分节）。

**边界**：`team.DeliverableStore` 存储层**未改**（复用既有 `Read`/`Stat`）；
只改读工具的投影。publish/list 不变。

**验收**（`team_deliverable_view_test.go`）：默认 outline 且体积是正文的零头、
`mode=full` 逐字、分页重建全文、未知 mode 拒绝、500 分节文档的 outline 有界、
代码注释不算分节。

### R3 `reasoning-language` 每条 user 消息重复注入 — 本次落地

**问题**：`internal/agent/reasoning_language.go:199` `WithReasoningLanguageForSource` 在**每条**
user 消息前加 179 字符块。leader 会话里出现 4 次（含 3 条 mid-turn steer）。

**落地**（`internal/agent/reasoning_language.go`、`internal/control/input.go`）：
- 新增 `(*Agent).WithReasoningLanguageOnce(content, lang, source)`：会话作用域记忆。
  `lang` 由调用方传入而非读 agent 自己的 atomic —— `control.compose` 持有权威值，
  两边不会对"注入了什么"产生分歧。
- 记忆落在 `sessionRuntime.reasoningLanguageInjected`（`sessionstate.go`），随 `SetSession`
  重置，并在 `sessionstate_test.go` 的 reset 清单中登记（该测试按结构体字段逐项校验）。
- `SetReasoningLanguage` 清空记忆，使语言切换重新注入一次。
- 调用方自带块时 `hasLeadingInjectedBlock` 语义保留，且计作"本次会话已注入"。
- `control` 侧新增 `(*Controller).withReasoningLanguageOnce`：有 executor 时走会话记忆，
  无 executor（纯 runner）保持历史逐轮行为。

**边界**：`TransientUserBlockTags` / `StripTransientUserBlocks` / preview 派生逻辑不变 ——
它们按 tag 剥离，与注入次数无关。`serve_test.go`、`save_test.go`、`title_test.go` 的
单条消息断言不受影响。

**验收**（`internal/agent/reasoning_language_once_test.go`）：同会话第二次不再加块、
`SetReasoningLanguage` 后重新加一次、显式带块的输入不被二次包裹、auto 英文静默、
`SetSession` 后重新注入、`withTurnPreferences` 端到端只注一次。

### R4 首轮 `@file` 引用内联全文 — 本次落地

**问题**：`internal/control/refs.go` 原 `maxFileRefBytes = 64 * 1024`，单文件最多内联 64KB。
leader 首轮把 `docs/uf010_evidence_registration.md` 全文（6,960 字符）塞进 prompt。

**落地**（`internal/control/refs_oversized.go`）：
- `maxFileRefBytes` 降到 **16 KiB**；新增 `refPreviewBytes = 4 KiB` 作为预览上限。
- 超限时返回 `oversizedFileRefNote`：路径 + 字节数 + `read_file(path=...)` 指引 + 头部预览，
  而不是截断后的 64KB 正文。
- 只报**字节数**不报行数 —— 行数需要读完整个文件，正是这条 note 要避免的成本。
- 小文件路径逐字不变（`readFileRefWithVision` / `readFileRefUnscoped` 共用同一分支）。

**边界**：`refs_test.go` 的截断断言随常量一起更新为指针断言；
PDF stderr 上限测试用同一常量，自动跟随。

**验收**：`refs_oversized_test.go` 钉住 —— 大文件产出指针而非正文、512 KiB 文件产出有界
note、小文件逐字不变；`refs_test.go:TestReadFileRef` 同步更新。

### R5 Claude Code leader 的工具面收窄 — 本次落地（三步 + 三通道，已闭环）

**问题**：MCP `tools/list` 线上载荷 63,555 字节 / **17,459 token**（CJK 感知），
按前缀分：`leader_*` 39 个 10,317 tok（59%）、`member_*` 19 个 4,560 tok（26%）、
无前缀 18 个 2,583 tok（15%）。

**扫描 357 份 Claude Code 会话的实测**：76 个工具里只有 **42 个被调用过**。
leader 会话用 23/76 或 13/76。每轮中位 prompt 217,962 ~ 270,934 token，
schema 占 6~15%；`181dffd5` 跑 1,230 轮 ≈ **21.5M token** 光花在 schema 上。

**第一步（安全收紧）**：原门控只按前缀剥 `leader_*`，18 个无前缀工具全部漏进成员面
—— 其中 `delete_team` **没有任何身份检查**，一个 `scope=member` 的成员仍能删掉整个团队。
改为**正向白名单**（`_scope_allows`）：没分类默认关掉，新增工具忘记归类时缺的是能力
而不是多出权限。

**第二步（进程级 leader 面）**：新增 `MULT_AGENT_MCP_TOOL_SCOPE=leader`。
但 leader 面 73/76，只排除 3 个 operator 工具 —— **单进程全量注册下 leader 侧省不到
token**，因为 leader 与成员默认连同一个 endpoint，进程级 env 门控区分不了连接。

**第三步（逐请求角色路由，本次补完）**：实测确认 Claude Code 的 HTTP MCP 条目支持
`headers` 字段（2.1.269 验证：自定义 header 到达服务端），因此角色可以**逐请求声明**，
不必为每个角色各起一个进程：

| 层 | 位置 | 作用 |
|---|---|---|
| 服务端过滤 | `mult_agent_mcp._RoleScopeMiddleware` | `on_list_tools` 摘掉不属于该角色的工具；`on_call_tool` 复核（只过滤 list 会留下"知道名字就能调"的缺口） |
| 客户端声明 | `common/tmux_utils.claude_role_mcp_config` | 经 `--mcp-config` 下发 `X-Mcp-Team-Role` |

**header 只有 `--mcp-config` 这条通道生效** —— 实测三种写法：

| 通道 | 连接建立 | 自定义 header 到达 |
|---|---|---|
| 项目 `.claude/mcp.json` | ✅ | ❌ 丢 |
| `--settings <file>` 的 `mcpServers` | ✅ | ❌ 丢 |
| `--mcp-config <json>` | ✅ | ✅ |

（前两者丢 header 且不报错，这正是它容易踩的地方。）`--mcp-config` 与项目 mcp.json
同 server 名不冲突：实测只注册一次，`--mcp-config` 的条目生效。

**端到端验收**（真实 Claude Code 进程 + 探针 server，6 个工具）：

| header | 模型实际看到的工具 |
|---|---|
| `member` | `member_get_my_task` `member_report_result` |
| `leader` | 上述 + `leader_assign_subtask` `leader_check_member_status` |
| 无 | 全部 6 个（含 `delete_team` `setup_codex_mcp`） |

调用侧复核也实测过：绕过 `tools/list` 直接调 `delete_team`，member 角色收到
`isError:true` + "not available in the member role scope"。

**边界**：角色为空/未知时不下发 `--mcp-config`，argv 与既有逐字一致；
进程级 env 门控仍是第一道，中间件只会让面更小。

**验收**（`tests/test_dsh_adapter.py`）：member/leader 面剥离与保留、未知 scope 全量、
台账覆盖全部无前缀工具、header 选择与回落、中间件 list 过滤、调用侧复核、
`--mcp-config` payload 与双 builder 一致。

### R5 三条客户端通道（已全部接线）

角色 header 每个客户端写法不同，都实测过：

| 客户端 | 生效通道 | 落点 |
|---|---|---|
| claude | `--mcp-config <json>` | `claude_agent_args(team_role=…)` |
| codex | `-c mcp_servers.<name>.http_headers={…}` | `codex_role_args(team_role)` |
| dsh | overlay mcp-client `config.headers` | `build_dsh_mcp_overlay(team_role=…)` |

角色判定只有一个来源（`member_team_role`），三条通道都从它取。

**codex 实测**（codex-cli 0.154.0）：`http_headers` 是 config.toml 既有 schema
字段（`--strict-config` 接受）；`-c` 只给 header、url 仍来自 config.toml 时两者
正确合并 —— 探针 server 在 `initialize` 与 `tools/list` 上都收到了 header。

**dsh 实测**：`dsh-mcp-client` 的 schema 有 `headers: z.dict(String).default({})`，
streamable-http 分支把它交给 `StreamableHTTPClientTransport` 的 `requestInit.headers`。

两者都**不能**把角色写进配置文件 —— `~/.codex/config.toml` 与 overlay profile 都是
全局/团队级的，leader 与成员共读，写死一个角色等于把所有人钉成同一个面。只有
codex 的每终端 argv、dsh 的 per-member patch 能区分。

### R5 已知边界

- **权限预配置若换通道会绕开中间件**：`_write_claude_permissions_internal` 会把
  `allow` 写进**共享** `.claude/settings.json`（leader+成员共读），因此那里刻意
  不放 `member_*`/`leader_*` 规则 —— 中间件的过滤依赖 `--allowedTools` 保持
  per-terminal。这一点原代码注释已写明，本次沿用。
- **未知角色不下发 header**：拼错角色退化成"服务端进程级 scope"（默认全量），
  而不是静默拿到一个更小的面。
- **护栏**：`TestEveryProductionSpawnSiteDeclaresARole` 扫描生产源码里所有
  `claude_agent_args` / `codex_command` 调用点，任何一处没传 `team_role` 就变红
  —— 已用"摘掉一处再跑"反向验证它会点名具体位置。新增 spawn 路径不会再静默退回
  全量面。

### R6 团队 role skill 按需加载 — 待排期

**问题**：`<team-role-skill>` 段 13,975 字符 ≈ 3.5K token 全程驻留，
其中 `special/leader/member-create`（2,842 字节）只在真要加成员时才需要。

**设计**：base/<role> 常驻；`shared/` 与 `special/<role>` 改为按需（`use_capability` 或
skill 索引），与 `docs/team-mcp-port/1M_CONTEXT_P2_P3_DESIGN.md` 的 T4 schema 剪枝同构。

**边界**：`teamRoleSkillBudget` 与 `roleSkillSection` 的"整块丢弃、绝不截断"语义保留。

---

## 4. 落地顺序与验证

R1 → R3 → R4 → R2 → R5 全部落地（R6 待排期）。每条独立可合、有单测钉住。

Go 侧验证见 `CONTRIBUTING.md`：
`go test ./internal/cli/ ./internal/control/ ./internal/agent/`、
`go run ./tools/repolint`（clean）、`golangci-lint run ./internal/{agent,control,cli}/...`。
根 Go 测试覆盖（`desktop/` 模块不涉及）。

MCP 侧（独立仓库，不在本 PR）：`python3 -m pytest tests/test_dsh_adapter.py -q`。

本机上 `internal/worktree`（30 个测试，git 2.34.1 缺 `merge-tree --write-tree`）、
`internal/skill` 与 `internal/skill/skillwatch`（inotify `max_user_instances=128` 并行耗尽）
在**改动前后同样红**，属环境问题，不是本次改动引入。

MCP 侧全量套件（2943 条）有若干**与本次改动无关**的 flake —— 都是"顺序/时序写死"
类断言，且 flake 集合每次采样都不同：

| 采样 | 结果 | 红的用例 |
|---|---|---|
| HEAD #1 | 3 failed / 2596 passed | `test_leader_sleep_anti_stall`、`test_member_prompt_template`、`test_team_manger` |
| HEAD #2 | 4 failed / 2595 passed | 同上三条 + `test_ack_batch_outbox::test_batch_ack_delivers_to_all_members` |
| 当前树 | 1 failed / 2943 passed | `test_leader_wakeup_injection::test_d8_...` |

两次 HEAD 采样共 7 次失败、5 个不同用例，当前树 1 次失败 —— **flake 是套件级的，
不是改动引入的**（HEAD 样本比当前树红得更多）。这些用例单独跑或按文件跑恒绿
（`test_leader_wakeup_injection.py` 15 次 × 3 轮无失败）。

其中一条是**确定的**顺序 bug，本次顺手修了：
`test_ack_batch_outbox.py::test_batch_ack_partial_failure_others_delivered` 断言
`已送达: b, c` 的固定顺序，但投递是并行的（`ThreadPoolExecutor`，
`OUTBOX_SEND_WORKERS=4`）—— 同一文件的另一条用例此前已用集合断言修过。
本次一并改为集合断言，20 次连跑全绿。

## 5. 未纳入本轮的观测

- Python MCP 侧的 `leader_check_member_status`（`mult_agent_mcp.py`）读的是
  `last_observed_state`，由 30s 后台监控线程刷新 —— 与 Go 侧是**两套独立实现**。
  本轮两侧**都加了 60s 节流**（同契约：读取永远执行，只抑制回复；状态变化绕过节流）。
- `_NUDGE_ALWAYS_TOOLS` 让查询类工具**豁免节流**，只要 pending 非空就追加提醒串 ——
  这会放大轮询的文本成本。本次未动：它是提示旁路，且追加的文本远小于一次
  round-trip 的前缀重发，优先级低于节流本身。
- `member_read_shared`（Python 侧）默认返回最近 10 条、增量上限 50 条，**已有界**；
  未实测其驻留成本，因此不能断言它有 R2 同类问题。
