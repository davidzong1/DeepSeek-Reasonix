# Team Member 缓存正式预注册门禁：三 Agent 并行执行计划

> 状态：待派发。目标是修复 Part B 六臂实验的 route/account 漂移，在不改写既有冻结记录的前提下，完成一次条件固定、可审计、可复算的正式 Team 分层实验。
> 当前判定：原六臂 204 条记录仅为探索性数据，不计入新正式实验的样本量或门禁通过率。
> 依据：`TEAM_MEMBER_CACHE_FOLLOWUP_EXECUTION_PLAN.zh-CN.md` §§5、7、8；`TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION.zh-CN.md`；`TEAM_MEMBER_CACHE_FOLLOWUP_CLOSEOUT.zh-CN.md`。
> 禁止事项：不得改写原始预注册使旧数据追溯合格；不得用旧 route/account 的 strata 或 pilot 样本补新门槛；不得因结果方向调整样本、排除项、停止条件或分母；本计划不准入缓存行为优化。

## 1. 目标、门禁范围与完成定义

本计划只关闭 **Team 正式分层实验的预注册门禁**。K1/K3、历史账本根因、其他 Provider 的端点验证，以及缓存优化准入不由此实验自动关闭。

正式采样必须满足以下条件之一：

1. **按原冻结条件重跑**：账号池条目 `deepseek-v4-flash-roojin`、route bucket `anthropic/bfcb0811b1c8`，model、构建、任务族及统计契约均经采样前核验一致；或
2. **原条件不可用时另立新版本**：先记录原条件不可用的证据，由负责人批准新账号池条目/route/model；创建并冻结新版本预注册后才可发请求。新版本不能把旧实验改标为正式结果。

正式门禁完成须同时满足：

- 采样前 M0、预注册版本、费用/token 上限、route/account 预检、并发窗口和停止规则均有可审计记录。
- 所有纳入正式报告的记录都来自同一冻结版本及同一预注册 route/account/model；关键字段缺失或漂移的样本隔离，不纳入正式分母。
- 每个声称达标的主要上下文桶至少有 **30 个有效 warm 单请求、3 个成员**；session 数另行报告。同一 session 的连续请求不得伪装成独立重复。
- 对达不到门槛或结构性不可达的桶，报告须有客观阻断证据和明确边界；不得用其他桶、裸请求或旧实验样本代替。
- 独立复算、隐私审查、预算核对、测试结果、最终逐桶 GO/limited/unresolved/blocked 表完整。
- 任何行为优化仍单独保持 **NO-GO**，除非之后另立方案并满足效果、质量、上下文、错误率、延迟和成本护栏。

## 2. 现状及必须保持的证据边界

当前归档六臂共 204 条记录，route 为 `anthropic/f119dfdb4214`、账号池/AgentUserRef 为 `strata-gw`，model_ref 为 `strata-gw/deepseek/deepseek-v4.1-flash[1m]`。冻结的原预注册条件是账号池条目 `deepseek-v4-flash-roojin`、route `anthropic/bfcb0811b1c8`。两者不一致，因此：

- 旧 strata records、旧报告、旧 banner 不纳入本轮正式样本、有效请求分母、样本量或因果对照；只保留为探索性归档。
- 旧实验里的 `768k_1m` 21 个 warm 样本也不计入新正式桶。新正式 route/account 必须独立达到门槛。
- 旧数据可以作为任务准备、驱动调试、token 预算估算参考，但在冻结新版预注册前，任何调试样本不得混入正式采样归档。
- 历史账本与本轮 Team 记录继续保持分源，不相加、不作历史重跑。

## 3. 三 Agent 派工与独立写集

| Agent | 任务 | 独立写集 | 不能做 |
|---|---|---|---|
| **Agent A：route/account 与运行前门禁** | 查明配置解析链、确认冻结账号条目实际解析出的 provider/model/route；设计并实现无秘密泄露的 preflight/运行前硬检查；提供本地验证证据 | 新的 preflight helper/脚本及其独立测试；`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_A_PREFLIGHT.zh-CN.md` | 不发真实请求；不改 B 的实验臂、正式预注册或报告；不输出凭据、认证头、完整 endpoint secret |
| **Agent B：正式采样与数据报告** | 准备新版预注册和实验运行；只有前置 Gate 通过后，执行锁定的 Team strata 采样，生成脱敏归档及正式 B 报告 | `TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION_V2.zh-CN.md`、`TEAM_MEMBER_CACHE_FOLLOWUP_B_FORMAL_REPORT.zh-CN.md`、`internal/cli/live_team_cache_strata_formal_test.go`（如需独立新驱动）及专属归档 | 不改历史预注册；不复用错误 route 的样本；不改变 Provider/统计契约；Gate 未通过时不发请求 |
| **Agent C：预注册审计、独立复算与门禁验收** | 独立审阅 B 的 V2 预注册和 A 的 preflight；冻结样本/排除/复算规则；对正式归档独立复算、隐私扫描、出具逐桶验收及合流记录 | `TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_C_AUDIT.zh-CN.md`、独立复算脚本/测试（若有，限 `internal/cachelab/` 或新独立审计工具）；最终 closeout 由负责人合流 | 不发真实请求；不修改 A/B 的写集或冻结后的样本规则；不得只复述 B 报告代替独立复算 |

**写集冲突处理**：三个 Agent 开工前先登记当前工作树已有的 2 个修改文件与 10 个未跟踪文件，确认归属。新增文件按上表命名，不覆盖已有 B 预注册/报告、closeout 或 live test。若必须编辑共享文件，先在交付中提出补丁，由负责人合流。

## 4. 前置依赖与并行关系

### Gate 0：负责人启动确认（A/B/C 可开始只读准备）

负责人确认：

- 当前工作树基线、已有修改/未跟踪文件归属、运行构建标识。
- 真实采样是否获准、可用 token/费用上限、凭据由哪个安全配置提供、可用时间窗。
- 是否允许一次独立、限额的 route identity probe。若 route bucket 只能在 Provider 响应后观测，探针必须在预注册中写明最大调用数/token，并计入总预算；探针不计正式样本。
- 旧账号池条目是否仍可使用。若不可用，选择“原条件阻断”还是“新 route/account 另立 V2”，并由负责人书面确认。

Gate 0 未完成时只允许只读排查、离线测试和文档准备，不允许任何真实端点请求。

### 并行阶段 P1：三 Agent 独立准备

- A 检查配置解析、route fingerprint 产生路径、运行前约束实现及本地负/正向测试。
- B 以原始冻结条件为基准草拟 V2 预注册、采样臂与脱敏归档方案；可以本地 dry-run，但不得访问 Provider。
- C 审查正式验收定义、独立复算规则、session/样本量口径及停止条件；对 B 文档逐条提出门禁意见。

三方在 P1 不发真实请求。A、C 的审查结论是 B V2 预注册的冻结前置输入。

### Gate 1：采样前正式冻结（串行签核）

依次完成：

1. A 交付 preflight 设计和测试，证明错误账号/route 会在发请求前失败，正确条件可通过本地解析；输出只含脱敏标识。
2. B 完成 V2 预注册，写定账号池条目、预期 provider/model/route、构建摘要、臂定义、每臂请求/成员/session 数、预算、停止/排除规则、归档目录和复算口径。
3. C 对 V2 进行独立逐条审核并签署 PASS，确认新样本不会与旧探索性数据混用。
4. 负责人记录签核时间和预注册文件摘要（SHA-256），冻结后不再修改；若有实质性变更，版本号递增并重新签核。

任何一项未通过，Gate 1 为 **BLOCKED**，不发真实请求。

### Gate 2：逐次运行前 preflight（每臂必做）

每个实验臂第一次发请求前，运行 Agent A 的 preflight 并把脱敏结果写入该臂 banner：

- 预期与实际账号池条目一致；实际 model/provider 一致；实际 route bucket 精确匹配 V2。
- 构建标识、折叠规则摘要、统计契约版本和臂配置与冻结文件相符。
- 测试归档目录不是临时目录，磁盘可写；报告/记录目录已按 V2 创建。
- token 预算有剩余；凭据只验证“可用/不可用”，不打印其值或认证头。
- 无其他真实端点采样正在运行；本臂的并发条件、运行时长、停止阈值已登记。

本地 pool/provider/model/build 配置必须先通过；route bucket 若只能在响应后得知，须使用 Gate 0/Gate 1 已批准的、独立限额的 identity probe 确认，probe 不计正式样本但计入总预算。任何身份或 route 字段不匹配立即停止该臂，保留失败 banner，不重试另一路由，不计样本；由负责人决定修配置或新建预注册版本。

### 执行阶段 P2：B 顺序执行正式采样

Agent B 按 V2 冻结的臂顺序串行运行，A 不同时运行真实 Provider 验证。C 可旁观归档完整性和预算，但不得修改分母/停止规则。每臂结束后做 route、model、account、记录数、provenance、usage accounting 和隐私快速检查；通过后才开始下一臂。

### P3：C 独立复算 + A/B 修复限定差错 + 最终合流

C 从只读归档重新计算各臂指标；B 仅回答数据字典/复现问题，不自行改动被冻结规则；A 仅处理运行前门禁实现问题，不改变已采记录的分类。负责人根据 C 审计结论更新 closeout 和总 go/no-go。

## 5. Agent A 工作包：route/account preflight

### 5.1 具体步骤

1. 沿 Agent 配置解析、`AgentUserRef`、账号池选择、provider/model 解析和 route bucket 生成链路，找到 `deepseek-v4-flash-roojin` 在当前构建下的实际解析值。
2. 明确 route bucket 是从哪些稳定字段生成，核对它是否绑定 endpoint/account/provider/model；不得只靠 model 字符串推断 route。
3. 在独立 helper 或脚本里提供 `expected` 与 `actual` 比较：账号池条目、provider、wire model/model_ref、route bucket、build id。生产请求发送前验证失败即返回，不尝试 fallback 或静默换条目。
4. 为 preflight 写离线测试：全部匹配通过；route 不匹配失败；pool entry 不匹配失败；model/provider/build 漂移失败；诊断输出不含密钥、token、认证头、完整 endpoint URL 中的敏感 query。
5. 若当前配置无法在不联网情况下预先得到 route bucket，明确指出最小安全的 route 身份探针方式，并要求探针响应只落 bucket/provider/model 的脱敏元数据；不得静默用探针请求充当正式样本。
6. 输出 A 报告：解析链路、校验点、正负向测试、残余风险、运行命令、负责人每臂应查看的 PASS 标识。

### 5.2 验收

- 错 route/account 在 Provider 调用前 fail closed；测试能证明没有执行 fake transport 的发送调用。
- 正确 frozen identity 的本地 fixture 通过，且值与预注册 V2 一致。
- 不要求 A 单独证明线上凭据有效；凭据有效性只在获准后的 preflight 中做布尔验证。
- 对真实 route 无法静态证明时，将其作为 Gate 2 的现场核验项，不把离线 fixture 说成端点 GO。

## 6. Agent B 工作包：新版预注册、样本和正式报告

### 6.1 预注册冻结前必须写定

- 版本：新文件名使用 `...PREREGISTRATION_V2...`；明确它是原预注册条件的后续正式轮次，旧条件/旧结果不可追溯改变。
- 身份：账号池条目、provider、网关标识（不含凭据）、route bucket、wire model、model_ref、构建及 `stream_usage.go` 摘要。
- 实验单元：每臂成员数、每成员 session 数、每 session 轮数、有效 warm 请求门槛、上下文桶、任务族、并发档（按臂登记）。
- 预算：每臂和总 token 硬上限；价格未配置时 USD 标 unknown；总预算不得超过先前 token cap，除非负责人在采样前另行批准并写入 V2。
- 停止/排除：route/account/build 漂移、非 observed request count、usage 无法解析、invalid accounting、连续错误/usage failure、隐私命中、超预算、请求正文落盘。
- 分析：主读数、miss/request、首请求与 warm 分组、成员等权/session 汇总率、置信范围或明确不作总体推断、逐项排除账。
- 报告分类：正式纳入、探索性/错 route、invalid、unknown、结构性不可达；分类规则采样前冻结。

### 6.2 样本量最低安排

沿用原任务族/成员路径，不因旧数据方向调整目标。

| 桶/臂 | 最低正式安排 | warm 样本目标 | 验收 |
|---|---|---:|---|
| `lt_32k` | 3 成员 × 至少 11 轮/成员 | ≥30（建议 33） | 目标桶、route/account/model 一致 |
| `128k_256k` | 3 成员 × 至少 11 轮/成员 | ≥30（建议 33） | 同上 |
| `512k_768k` | 3 成员 × 至少 11 轮/成员 | ≥30（建议 33） | 同上 |
| `768k_1m` | **重新采集** 3 成员 × 至少 11 轮/成员 | ≥30（建议 33） | 旧 21 warm 不计入；brief 与 hard ceiling 先 dry-run 校准 |
| `gte_1m` | 不要求伪造样本 | 0 | 以模型窗口和协议预留证明结构性不可达，记录 BLOCKED/不可执行 |
| fold/compact 阶段 | 不承诺一定触发 | 触发后报告；未触发则明确无样本 | 不改变任务族/窗口/压缩策略凑触发；写明机制边界 |

“11 轮”仅在定义为首轮 1 条 + 10 条 warm 时成立；正式驱动和 V2 必须以实际 session/request 编号核对。若每个成员不是一个 session，按冻结设计另算，不得只按总请求数达标。

按原粗估，完整六臂输入约 48.6M token；将 S4 从 8 轮/成员补到 12 轮/成员约增加 11.5M，总量粗估约 60.1M，低于既有 80M 总上限。此为规划估算，不是实际消费；最终必须由 V2 复核各臂预算，并按真实输入 token 累加，达到臂/总上限即停止。

### 6.3 正式采样步骤

1. V2 冻结并通过 Gate 1 后，先用小规模、**不计入正式样本**的身份 preflight 验证实际 route/account；身份对不上立即终止。
2. 每个臂启动前完成 Gate 2，banner 写入冻结版本 SHA-256、build、pool entry、provider、model、route bucket、arm、token cap 和 start time。
3. 先完成串行基线及三个普通上下文桶，再依 V2 冻结顺序进行长上下文与并发臂；每臂结束后即刻核账。
4. `768k_1m` 的 brief 只允许在原先推导的可执行窗口内校准。预检只能用本地 estimator/dry-run；任何真实请求都要计入该臂预算并按 V2 分类，不能先发请求再决定是否纳入。
5. 任务只观察、不改请求行为；任务质量护栏明确写为未测试。不得在本工作包顺手修改工具 schema、折叠、压缩或 prompt 构造。
6. 归档原始脱敏 JSONL、banner、报告 JSON、run logs（确认无正文/凭据）、精确重算命令和文件摘要；归档目录须在持久位置，不以 `/tmp` 为唯一副本。

### 6.4 B 交付与停止条件

交付 V2 预注册、正式 B 报告、逐臂样本账、route/account preflight 记录和归档清单。遇到漂移时隔离整臂；只有 V2 明确允许且漂移原因已修复、未查看结果影响分析选择时才可重跑。隐私、provenance、预算或 usage accounting 问题触发停止并报告，不允许手工修记录。

## 7. Agent C 工作包：独立审计及最终门禁

### 7.1 采样前审计

1. 审阅 V2 的主要变量、桶边界、warm 分母、成员/session 独立单位、`768k_1m` 的 33 warm 目标和 80M 总预算。
2. 独立核算 token 预算、每臂轮数、停止条件是否能在驱动中机械执行。
3. 对照 A preflight，检查失败分支 fail closed、字段同源、输出脱敏；验证错 route 的 records 不可能进入正式报告。
4. 在 V2 冻结前书面给出 PASS/BLOCKED 清单；冻结后不得借分析结果变更规则。

### 7.2 采样后独立复算

- 只读归档，不使用 B 报告汇总数作为输入替代原始记录。
- 先逐条按冻结字段重建纳入/排除集，再独立计算 `Σhit/(Σhit+Σmiss)`、miss/request、prompt 分布及 token 对账。
- 核对身份覆盖率；每条正式记录须具备 request/member/session/sequence、route/model/provider、observed request count、accounting_valid、usage source 和需要的诊断字段。
- 按 arm 汇总 raw/effective/excluded 数、成员/session 数、首请求/warm 数、路由分布、token 使用和异常/重试；检查每臂身份唯一性。
- 复跑 B 的命令并与 B 报告逐项对账；差异须定位到原始记录，不允许直接改报告数字。
- 扫描归档凭据、正文、工具内容和敏感头；报告扫描命令、文件范围及结果，不在审计报告复制命中内容。

### 7.3 最终门禁分类

每个上下文桶逐项使用以下状态：

- **PASS**：route/account/model/build 精确匹配；coverage 与 usage 记账有效；≥30 warm、≥3 members；审计复算一致。
- **LIMITED**：桶达到可报告质量但独立 session 数较少、并发仅臂登记、oracle coverage 缺口等使外推受限；需列明不影响哪些结论。
- **BLOCKED / NOT EXECUTABLE**：硬上限、route 条件不可用、结构性上下文上限或 fold 条件无样本；附客观证据，不声称结果为零。
- **UNRESOLVED**：必要 provenance/归档/计数无法确认或复算不一致；先修复审计链，不作门禁通过。

Agent C 交付独立审计报告及建议状态；负责人汇总成最终 closeout。B 报告和 C 报告结论不一致时，先逐条解释差异，再签最终状态。

## 8. 路由与采样实现的硬防线

1. 期望身份必须来自 V2 单一冻结源，运行器不保留第二套手填默认值。
2. 任何计费请求前必须比较可本地确定的 pool entry/provider/model/build；route 若不能静态得到，只能通过已批准并单独计量的 identity probe 确认。正式样本一旦返回 route 与 V2 不符，立即 fail closed。
3. 每条记录和 banner 都保存实际身份；报告从记录汇总身份，不从启动参数推定身份。
4. 同一臂记录出现多个 route/model/account，整臂隔离并调查；不得挑选匹配子集来保留有利结果，除非 V2 预先定义了该分层且样本仍独立达标。
5. identity preflight 与正式样本分开编号；如果其会产生真实 Provider 请求，必须在 Gate 0/Gate 1 预先获准并设置最大调用数/token，记入总预算和独立探针日志，但不计正式 warm 分母。
6. run id、member id、session id 不可跨臂误复用；成员/session 分组由原始记录核验。

## 9. 最终合流及决策表

负责人在 A/B/C 交付后更新 `TEAM_MEMBER_CACHE_FOLLOWUP_CLOSEOUT.zh-CN.md`，至少给出：

| 审查面 | 必须记录 |
|---|---|
| A preflight | 正负向测试、route/account 解析链、是否 fail closed、剩余线上限制 |
| B 正式实验 | 冻结 V2 摘要、各臂身份、raw/effective/excluded、有效 warm/member/session、coverage、token/费用和结构性阻断 |
| C 独立复算 | 复现命令、独立结果、差异说明、隐私扫描、归档完整性 |
| 最终判断 | 每桶 PASS/LIMITED/BLOCKED/UNRESOLVED；正式预注册门禁整体结果；不可声称事项；优化候选仍 NO-GO |
| 工作树状态 | 新增/修改清单、测试结果、未提交/提交/发布状态；不虚报已合流或发布 |

整体正式 Team 预注册门禁只有在三个可达主要桶至少 PASS、`768k_1m` 达到预注册样本或有合规阻断且状态如实降级、`gte_1m`/fold 边界有客观证据、C 复算与隐私审查通过后，才可解除 BLOCKED。若计划目标要求 `768k_1m` 也是正式达标桶，则该桶必须 ≥30 warm、≥3 members；不能仅以“旧实验有 21 条”通过。

## 10. 可直接派发给 Agent 的任务摘要

### Agent A

> 只读追踪 AgentUserRef/账号池/provider/model/route 解析链，设计并实现正式 Team strata 的采样前 identity preflight。错 pool、route、model、provider 或 build 时必须在 transport 调用前 fail closed。新增独立测试证明正确 fixture 通过、错误身份拒绝且不会发送请求。输出 `TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_A_PREFLIGHT.zh-CN.md`；不发真实请求、不改 B/C 写集、不输出任何凭据。

### Agent B

> 基于现有冻结条件起草新的 V2 预注册，原始预注册保持不变。固定账号池 `deepseek-v4-flash-roojin` 与预期 route `anthropic/bfcb0811b1c8`（若不可用，先报阻断并等负责人冻结新条件），为每个可达主要桶至少设计 30 warm/3 members；`768k_1m` 旧 21 条不计入，按每成员首轮 + 10 warm 重新采集。预算、identity preflight、停止/排除和归档规则全写入 V2。待负责人 Gate 0、A preflight 与 C 预审签核后才可发真实请求。输出 V2 预注册、正式报告和脱敏归档。

### Agent C

> 独立审阅 B V2 的样本、session、预算、停止与排除口径，审查 A 的 fail-closed preflight；冻结前出具 PASS/BLOCKED。正式采样后只读归档，按冻结规则独立重建纳入/排除集、重算主要指标、核对身份/coverage/token 账及隐私扫描，输出 `TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_C_AUDIT.zh-CN.md` 和逐桶状态；不得修改 A/B 写集或发真实请求。
