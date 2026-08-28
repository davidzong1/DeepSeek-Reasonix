# Agent 优化技术路线

## 1. 目标与范围

本路线承接 Agent/task、provider、团队协作和 TUI 的问题审计结果，记录已拍板的修复方向、实现边界、验收门禁和进度。当前路线已进入实现阶段；后续每完成一个节点，必须先更新本文件的进度表，再提交实现与验收证据。

范围包括：

- provider 模型选择失败时的严格报错和可观测性；
- task/use_capability 的稳定调用形状与空 prompt 防护；
- leader/member 协作纪律在 system prompt 和工具注入中的固化；
- 大型 Go 文件与既有 repolint 债务的渐进拆分；
- 按功能提交和跨成员合流门禁。

明确不在当前路线实施的事项：并发测试环境的 IPv6/loopback 权限问题，作为后续独立任务保留。

## 2. 已拍板决策

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| D1 | 改动提交方式 | 按功能拆分提交，不做一次性大提交。建议顺序：task 预校验、TUI 工具卡、文档/门禁。 |
| D2 | provider fallback | 采用严格报错。模型/provider 不匹配、凭证缺失或路由不可用时直接返回来源明确的错误，不静默回退到 DeepSeek 等其他模型。 |
| D3 | provider 可观测性 | P0 采用方案 A：在解析/装配失败处报告请求模型、实际 provider、配置来源和缺失凭证，不改变错误归属。 |
| D4 | task 空 prompt | P1 采用方案 A：保持 `use_capability` provider-visible schema 和缓存稳定性，不扩展嵌套 required；依靠授权前 `ArgsValidator` 和清晰错误提示。 |
| D5 | 协作纪律 | 研究是否能通过 leader/member system prompt 固化工具使用顺序、文件锁、回报和 checkpoint 确认；确认可行后再实施。 |
| D6 | repolint 债务 | P2 采用方案 A：按独立节点渐进拆分大型文件并降低复杂度，不使用 `repolint -update` 扩大 baseline。 |
| D7 | 测试环境限制 | 暂不修改业务代码；IPv6/loopback 受限导致的 `httptest` 失败作为后续环境任务。 |

## 3. 技术路线

### P0. 严格 provider 错误与提交收口

1. 盘点当前工作树中属于本路线的文件，排除构建产物、缓存和用户私有配置。
2. 对 provider/model 路由、成员凭证、默认模型解析建立严格失败语义：不可用时返回错误，不再静默 fallback。
3. 错误至少包含请求的 provider/model、解析后的路由和缺失凭证来源；不得把成员凭证错误误报为全局 `DEEPSEEK_API_KEY`。
4. 按 D1 拆分提交，每个提交必须包含实现、对应测试和路线文档更新。
5. 每批提交前运行 targeted test、`-race`、`go vet`、`go build`、`gofmt` 和 `repolint`。

验收：非法 provider/model 不进入 backend 执行；缺凭证不创建半成品 backend；合法 OpenAI/DeepSeek/Anthropic 路由保持既有兼容性；提交可独立回滚。

### P1. task 调用稳定性与协作纪律

#### P1.1 task 调用

- 保持 `use_capability` 固定 schema，避免 provider cache 失效。
- 保持 `task`、`read_only_task`、`fleet`、`parallel_tasks` 的授权前参数校验。
- 错误提示明确指出 `arguments.prompt`，帮助模型修正字段层级。
- 不在 provider 层增加自动重试；参数错误应直接返回并由 Agent loop 重新规划。
- 记录空 prompt、字段错层和重复 task 调用的计数，作为后续是否需要增强描述的依据。

可选增强仅在空 prompt 频率仍高时启动：在不改变 schema 的前提下，对 `use_capability.Description()` 增加一次性调用示例，并重新执行 cache guard。未经评审不得把 `prompt` 加入代理固定 schema。

#### P1.2 system prompt 固化协作纪律

先做只读设计和效果验证，再改 prompt。候选规则如下：

- leader：先 `leader_list_team`，再 `leader_select_task_members`，分配后必须 `leader_sleep(max_seconds=600)`；用 `leader_check_member_status` 追踪，不轮询终端；高漂移时先确认 checkpoint。
- member：先读取任务和共享上下文；代码修改前申请文件锁；完成后第一个动作是 `member_report_result`；不得用 monitor 推断代替正式回报。
- 双方：区分团队派单工具与本地 `task` capability；不得用空 prompt 重试；命令批次发生 dependency skip 或 permission deny 时拆分重跑。

实现时应把这些规则放在角色专属 system prompt/工具说明中，而不是动态业务上下文中，避免污染稳定前缀；同时增加行为测试验证工具调用顺序和回报闭环。

### P2. 大文件与 repolint 债务

- 以函数边界和职责为单位拆分 `task.go`、`boot.go`、`chat_tui.go` 等大型文件。
- 每次只处理一个明确模块，保持行为不变并补回归测试。
- 不修改 baseline，不用 `-update` 掩盖新增复杂度。
- 拆分后分别运行 repolint 和全量测试，记录前后 file-size/function-size/complexity 指标。

**P2.1 首拆记录（2026-08-27, architecture-analyst-claude）**：从 `internal/agent/task.go`（2035 行）提取身份/模型/effort 解析组 5 个方法（effectiveProfile / effectiveIdentity / usageModelRef / effectiveModelIdentity / effectiveEffortIdentity）至新文件 `internal/agent/task_identity.go`（59 行，task.go 降至 ~1970 行）。纯计算、只读配置字段、方法签名与实现逐字节搬移，行为零变化。新增聚焦回归测试 `task_identity_test.go`（4 用例：subagent 默认回退、base 回退、identityProfile 解析分支、trim 语义），叠加既有 `usage_accounting_test.go` 的 usageModelRef 钉住。验证：gofmt clean、agent 全量 23.6s ok、repolint 本文件零新增违规。说明：repo 级 repolint 失败（essay 1967>1965 等）由工作树中其他成员未提交改动引入（provider 相关 + boot.go 历史计数），非本次拆分；按纪律未用 `-update`，待 P0.1 提交收口时统一处理。

**P0.2 落地记录（2026-08-27, boot-claude / testing-claude）**：
- 实现（已提交 7cf7051c6）：`provider.New` 未知 kind 错误点名请求的 provider/model/route；`internal/boot/provider_errors.go` 三件套——`missingCredentialText`（缺失凭证来源，含 api_key_env 与查找来源）、`strictEntryFailure`（请求模型/实际 provider/kind/route/缺失凭证信封，`%w` 保留 cause 与错误归属）、`ensureRegisteredKind`（未注册 kind 在装配前快速失败，非法 provider/model 不进 backend）；`NewProviderWithProxy` 接入信封与 gate。
- wiring（testing-claude，未提交）：RequireKey 失败包 `strictEntryFailure` + `ensureRegisteredKind` 门；#6996 keyless default 回退保留有效路由且可观测——`skippedKeylessDefault` 跟踪 + LevelWarn notice「Skipped a keyless default_model.」（点名被跳过的 default、缺失凭证、替换路由）；重复的 provider_failure.go 片段已删除；Build 级验收测试全绿。
- config-source 补齐（boot-claude，未提交）：新增 `providerConfigSource`（最高优先级配置文件或 `<no config file>`），D3 配置来源接入三处失败/提示点——RequireKey 错误 `(config: %s)`、keyless 回退 notice Detail、`resolveModelEntry` 未知模型错误（`configured: ..., config: ...`）；notice Detail 改用 `missingCredentialText` 统一凭证片段（空 api_key_env 场景不再拼出裸逗号）。
- 测试：`provider_errors_test.go`（missingCredentialText/strictEntryFailure/ensureRegisteredKind/NewProviderWithProxy 失败包裹与有效路由构造 + Build 级 `TestBuildNoticesSkippedKeylessDefaultModel` 断言请求模型/替换路由/配置来源/路由保留）；`provider_failure_notice_test.go`（`TestBuildExplicitKeylessModelErrorNamesConfigSource`：显式 keyless + RequireKey 错误点名请求模型、provider、route、缺失凭证、config 文件）；`TestBuildUnknownModelErrorIsActionable` 增补 `config:` 断言。
- 验收对照：非法 provider/model 不进入 backend（ensureRegisteredKind 装配前失败）✓；缺凭证不创建半成品 backend（RequireKey 路径失败；keyless 回退保持可观测并点名来源）✓；合法 OpenAI/DeepSeek/Anthropic 路由保持既有兼容性（`TestBuildKeylessDefaultFallsBackToConfiguredProvider`/`TestNewProviderWithProxyValidRoute`/既有 boot 全量回归绿）✓；成员凭证错误不误报全局 `DEEPSEEK_API_KEY`（既有 `memberCredentialError` + `TestMemberResolverEntryNeverTriggersGlobalKeyNotice` 未触碰）✓。提交可独立回滚：实现按 7cf7051c6（已提交）+ 收口批（待 P0.1）两段拆分。
- 门禁：boot 全量 10.7s ok；P0.2 定向 -race ok；gofmt clean；vet clean（provider_errors.go 重构窗口期短暂中断已恢复）；repolint 剩余违规全为并行未提交工作引入（boot.go essay/file-size、provider.go file-size、provider_test.go），未用 `-update`，P0.1 提交收口统一处理。

### P2-deferred. 并发测试环境

保留现有业务测试和验收代码，不为规避受限环境改写网络语义。后续独立任务处理：

- CI/沙箱 loopback 权限和 IPv4/IPv6 监听策略；
- `httptest` 在受限环境的兼容测试；
- 真实环境与沙箱环境的门禁差异记录。

## 4. 工作进度

| 节点 | 工作内容 | 状态 | 交付物/验收 |
| --- | --- | --- | --- |
| R0 | 汇总问题、记录拍板决策和边界 | 已完成 | 本文档第 1-2 节 |
| P0.1 | 工作树清点、构建产物排除、按功能提交 | 已完成 | P0/P1/P2.1 已按功能拆分提交；仅共享路线文档待收口 |
| P0.2 | provider fallback 严格报错和来源可观测性 | 已完成 | 实现+测试全绿（见 §3 P0.2 落地记录）；提交收口待 P0.1 |
| P1.1 | task 空 prompt 频率观测和错误提示评估 | 已完成 | ArgsValidator 授权前拦截与明确 prompt 错误；cache-stable schema 保持不变 |
| P1.2 | leader/member system prompt 协作纪律设计 | 已完成 | prompt 变更提案、顺序/回报行为测试 |
| P1.3 | leader 专属成员管理工具接线 | 已完成 | leader backend 注入增删成员/修改角色工具；执行时复核 leader 身份，角色变化清理成员上下文 |
| P1.4 | 团队默认 Agent 用户 | 已完成 | `g` 选择团队默认用户；未配置时拒绝启动 session；存储、TUI 与恢复路径回归全绿 |
| P1.5 | 成员 Role 动态更新 | 已完成 | TUI 支持编辑 Role；保存前检查忙态，保存后清空成员上下文并退役旧 backend，下一次绑定按新 Role 重建 system prompt |
| P2.1 | 大文件渐进拆分 | 已完成 | task_identity.go 首拆完成，4 项回归全绿；后续模块按独立任务排期 |
| P2.2 | 受限 loopback/IPv6 测试环境 | 后续任务 | 独立环境修复，不改业务语义 |
| R-final | 全量门禁、文档收口、最终交付 | 已完成 | 全仓编译、targeted tests、vet、gofmt、repolint clean；运行期 httptest IPv6 监听受限，列入 P2.2 |

## 5. 协作与变更门禁

1. 新节点开始前，leader 必须确认当前 checkpoint，避免任务目标漂移。
2. 分配前先选择相关成员，leader 不把自身记录当作可分配成员。
3. 代码文件改动必须有明确文件边界；共享文档并发编辑要使用文档锁。
4. 成员完成任务后的第一个动作是正式回报；monitor 自动推断只能作为提醒，不能作为验收证据。
5. 任何 schema、稳定前缀、provider-visible description 改动都必须补 cache guard 和效果测试。
6. 已获用户拍板后按节点启动实现；未拍板的扩展仍保持待命，不得借路线文档扩大范围。
