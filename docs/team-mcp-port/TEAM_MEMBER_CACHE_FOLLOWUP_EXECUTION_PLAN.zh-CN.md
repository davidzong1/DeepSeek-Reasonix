# Team Member 缓存计量主线：三 Agent 后续执行方案

> 状态：待派发。目标是关闭合流纪要 §9 的可执行未决项，形成可审查的后续证据与发布决策。
> 前置记录：`TEAM_MEMBER_CACHE_MERGE_RECORD.zh-CN.md`、`TEAM_MEMBER_CACHE_POST_JOINT_3AGENT_PLAN.zh-CN.md` 及 Part A/B/C 交付物。
> 执行原则：三个 Agent 可并行做各自写集；需要共享真实端点、费用额度或同一 pilot 窗口时，由负责人先协调时间，禁止并发实验互相污染。

## 1. 目标与当前边界

本轮已完成本 route 的 K1/K3 阶段性验证、Team pilot 描述性基线和 P4 合流记录。主线仍未闭环，原因包括其他 Provider/方言未验证、真实 Team 分层样本不足、1M/压缩边界无样本、历史账本计数尚未统一，以及当前 `cachelab/usage.go` 与冻结快照摘要不一致。

本计划只要求完成可验证的后续工作，并据结果明确继续、有限支持、未决或阻断。它**不预设**其他 Provider 会通过，不把新采样说成历史重放，也不授权任何缓存行为优化。旧账本缺失的原始 usage/provenance 不可回填；未来采集只建立新的前瞻基线。

## 2. 并行派工总览

| Agent | 工作包 | 独立写集 | 主要交付 |
|---|---|---|---|
| A | Provider/route K1 覆盖与回归验证 | `internal/provider/**usage*`、对应测试；A 专属报告 | 按 Provider/事件形状的状态矩阵与测试证据 |
| B | Team 正式分层实验、并发信号与长上下文/压缩边界 | `internal/team/**` 的观测字段（先审后改）、pilot 驱动/归档、B 专属报告 | 预注册、脱敏数据、分层结果和质量护栏 |
| C | 历史账本复算调和、K3 冻结差异审计与统计独立复核 | `internal/cachelab/**`、复算脚本/审计报告；不改 Provider 和 Team 生产观测代码 | 可复现的历史计数口径、摘要差异结论、独立 go/no-go |

三个工作包不共享代码写集。跨包接口变更先提交给负责人记录，再由负责人安排合并；任何 Agent 不得直接覆盖其他 Agent 的未提交文件。

## 3. 所有 Agent 共用的 M0 冻结

开始写代码或采样前，各 Agent 在自己的报告中记录：

1. 当前基线 commit、工作树是否干净、实际构建标识；不得把未提交改动写成已发布构建。
2. 使用的 `stream_usage.go` 与 `cachelab/usage.go` 路径、MD5/SHA256、快照来源。已知 `stream_usage.go` 冻结 MD5 为 `71b7d3b79a4c6dfc74d1357b310190ff`；K3 归档快照 MD5 为 `cc0cda4fcbd22bd7f68602d376656865`，但当前工作树 `usage.go` 摘要不同，须由 C 判定当前差异后再声明测试对应版本。
3. route、model、账号范围、测试/生产端点属性、时间窗、请求类型和 usage 来源。凭据只从既有安全配置读取，不写入报告或归档。
4. 统计契约：有效单请求、聚合/重试、unknown、no-split、invalid accounting、请求数来源和排除规则；unknown 不填 0，journal、成员记录和历史账本不混算。
5. 隐私边界：不落 prompt、工具参数/结果正文、完整响应、密钥或认证头；只允许脱敏事件形状、token 计数、状态、摘要和必要元数据。

如果冻结版本无法确认，或发现请求字节/route 漂移，暂停依赖该版本的实验并报告阻断原因；不得用旧快照测试结果替代当前代码结果。

## 4. Agent A：Provider 与 route 的 K1 验证

### 目标

界定 K1 在已声明支持的 Provider/事件形状上的正确性，特别处理合流纪要列出的 native Anthropic、LongCat、官方 DeepSeek、DeepSeek-compatible route，以及尚未审查的 OpenAI 直连与 `responses` 方言。

### 执行步骤

1. 审查当前 `stream_usage.go`、`messages_usage.go` 和调用路由配置；先核实冻结摘要及 inclusive/exclusive usage 约定与 reasoning protocol 的分离状态。
2. 为每条 route/Provider 建立矩阵，分别标注：代码测试、合成 fixture、脱敏真实事件、真实端点观测。测试覆盖缺失/显式零、重复和乱序事件、split-free 估算、split 后 split-free、全命中省略 split、`input > read`、write-only、负值和多次 start/delta。
3. 对有凭据且获准的真实端点做最小只读 usage 验证。端点不可用或没有凭据时，保留 `unverified/blocked`，不得把表驱动 fixture 升级为端点通过。
4. 分 route 验证归一化结果、usage convention、`prompt/hit/miss/write` 与 unknown 处理；`prompt == hit + miss` 仅作为必要不变量，不单独作为正确性证明。
5. 对 OpenAI chat 与 `responses` 独立判断是否复用同一 usage 路径；如果不在本轮改动范围，明确标为未审并说明影响。

### 交付物与验收

- `TEAM_MEMBER_CACHE_FOLLOWUP_A_PROVIDER_VALIDATION.zh-CN.md`：覆盖矩阵、证据等级、端点可用性、已知盲点、每 route 判定及复现命令。
- Provider 测试及必要的最小实现修复；不改 Team 请求构造、缓存策略和统计分母。
- 通过要求：所有声称支持的事件形状有确定性测试；每条 Provider 单独判定；无证据的 route 维持未决；相关 Provider 包测试通过。

### 停止条件

发现计数误折叠/归一化回归、缺失与显式零被错误确定化、凭据或正文进入 fixture、或真实端点结果与实现矛盾时，停止该 route 的 GO 判定，提交最小复现和阻断说明。

## 5. Agent B：真实 Team 正式分层实验

### 目标

在冻结的 K1 构建和固定 route/model 下，把当前 3 成员 × 10 轮的 pilot 扩展为预注册、可审计的 Team 分层观察；补足 `768k–1m`、`>=1m` 和真实压缩/重建边界。不得为了凑满上下文桶而以裸请求代替真实 Team。

### 采样设计

采样开始前提交预注册，写明主要问题、唯一主要变量、上下文桶、任务族、成员/session 数、停止规则、最大时长/费用和排除规则。主要跨成员桶至少 **30 个有效单请求、3 个成员**；另报独立 session/batch 数，同一 session 的连续请求不得当成完全独立重复。样本达不到门槛时只发布描述性结果并标 `insufficient_sample`。

每个阶段固定 route/model/account，先串行建立可重复基线，再每次只增加一个主要变量。分层至少包括：上下文大小、会话阶段（首轮/warm/长会话后段/压缩或重建前后）、任务族，以及可安全实现的并发阶梯。真实 `768k–1m`、`>=1m` 请求只在成员工作流自然且受控地产生时采集；若服务/上下文上限阻止采样，记录为不可执行，不造合成替代结论。

### 并发字段处理

先审查是否存在不获取 Team store 锁、不会让 provider 请求等待遥测的并发信号。若找不到安全信号，继续用**实验臂登记**表达并发条件，并在每臂记录并发定义；不得在逐请求路径上临时增加锁等待。实现逐请求信号前需给出竞态、开销和 provider 请求非阻塞证明。

### 必报指标与质量护栏

- 有效请求、成员、独立 session/batch；加权率、成员等权率、session 汇总率，并注明不可互换。
- `hit_tokens`、`miss_tokens`、hit/miss per request、prompt 均值与分位数、usage source/oracle coverage、request-count provenance。
- error、retry、unknown/estimated、no-split、aggregate、invalid-accounting 的排除计数。
- route/model/account、context bucket、task family、maintenance phase、并发臂和请求形状摘要。
- 若只观测且未改变行为，明确质量指标为未测试；若提出行为变更，则必须另行预注册任务完成率、必要上下文保留、工具正确率、错误率和延迟护栏，未获准入前不得上线。

### 交付物与验收

- `TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION.zh-CN.md`（采样前冻结）及 `TEAM_MEMBER_CACHE_FOLLOWUP_B_REPORT.zh-CN.md`（结果）。
- 脱敏归档：banner、构建标识、臂登记、逐请求记录和复算命令；不含正文或凭据。
- 对样本不足、桶不可达、并发信号缺失和压缩边界未触发如实标注，不得从命中率百分比单独推导缓存收益。

### 停止条件

构建/route/account 漂移、usage 无法解析、请求数 provenance 不足、隐私违规、异常错误/重试或费用/时长达到预注册上限时立即停止对应臂，隔离样本并先审数据。

## 6. Agent C：历史复算调和、K3 快照审计与独立统计复核

### 目标

解决合流纪要指出的审计差异，并明确历史账本能回答与不能回答的问题。C 不改 Provider 生产语义或 Team 生产请求观测字段。

### 执行步骤

1. 对照归档中的 `cachelab-usage.go.snapshot`、`source-md5.txt`、当前 `internal/cachelab/usage.go` 与后续提交/工作树 diff，逐段说明摘要不同的来源。对当前版本重跑 K3 相关测试和 journal 重放；报告必须同时写明归档冻结版本与实际测试版本，不能混称。
2. 从只读历史账本重新计算 09-23/09-24 的模型过滤、行数、小时覆盖、每小时 hit/miss 汇总和共同小时切片。保存可复现脚本/命令、时区规则、输入文件摘要、每层排除计数；禁止覆盖原账本。
3. 对账合流纪要与 C §4.1 的 `1964/1713` vs `1973/1714`、共同切片 `169` vs `174` 差异。若无法通过明确过滤规则解释，标记旧数字不可复现并撤销精确行数主张；不得挑选更有利的一组。
4. 检查历史账本是否存在可用原始 usage/provenance。若无，明确不能重算 per-request、不能推断 oracle 在场性、不能识别成员/session/task family，也不能把未来采样称作历史重跑。
5. 独立重算新的 Team/journal 样本时，严格隔离数据源、使用已冻结分类器、逐项核验排除和分母；不得合并 journal、成员记录、统计账本。

### 交付物与验收

- `TEAM_MEMBER_CACHE_FOLLOWUP_C_AUDIT.zh-CN.md`：摘要链、复算代码/命令、行数与比率复核、不可推断项、独立审计结论。
- 当前 K3 相关测试、journal replay 和适用统计测试结果；测试记录包含确切代码摘要。
- 对历史比较的判定须为“可复现并解释口径”或“原报告精确数字撤回、仍不可归因”，不可仅重复报告值。

### 停止条件

历史账本缺失/被改写、输入摘要无法冻结、归档快照不完整或解析器改动改变已有 journal 分类且原因不清楚时，停止作确定性统计结论，先提交审计差异。

## 7. 并行协作与阶段门禁

### 开始门禁

- 负责人确认共享工作树当前变更归属、三方写集和可用端点/采样额度后，三个工作包可并行启动。
- 真实端点/Team 采样应错开运行；只读复算与 fixture 测试可并行。
- 代码变更由各 Agent 限定在自己的写集；新增跨包字段或契约先发起接口提案，不直接改他方代码。

### 合流门禁

负责人收到三份报告后，先统一冻结版本、历史过滤口径和样本定义，再做最终 P4 复核。最终判定逐 route 给出 GO / limited / unresolved / blocked；Team 结论逐桶给出样本数和 coverage；历史差异明确撤回或保留的证据边界。

只有候选在真实 Team 分层中可复现、达到预注册样本门槛、miss/request 有最小效应、任务质量与必要上下文护栏通过、请求形状影响可解释时，才能另行进入缓存行为优化准入。否则结论保持观察/未决，不改缓存行为。

## 8. 总体完成定义

本轮后续主线可标记完成，须同时满足：

1. 所有已声明支持的 Provider/route 有独立判定，未验证 route 明确保留未决且没有泛化声明。
2. Team 正式分层报告满足预注册门槛，或对客观不可达的层明确给出阻断与边界；1M/压缩边界结论不得由低上下文 pilot 推导。
3. 历史账本行数与切片数字统一可复现，或原精确数字已撤回并附明原因；没有伪称重建历史因果。
4. K3 冻结快照与当前测试版本对应关系可追溯，相关测试通过。
5. 最终变更审阅完成，工作树归属清楚，提交/发布状态在发布记录中如实声明。提交、推送和发布须由负责人按项目流程单独执行，不由此计划自动触发。

## 9. 派单摘要

- **Agent A**：负责 Provider/route 的 K1 验证与测试，输出 A 报告；不改 Team/统计写集。
- **Agent B**：负责预注册和真实 Team 正式分层采样、长上下文/压缩边界与安全并发信号评估，输出 B 报告；不改 Provider/统计语义。
- **Agent C**：负责历史账本复算调和、K3 摘要与快照审计、独立统计复核，输出 C 报告；不改 Provider/Team 请求观测生产逻辑。

任一工作包受端点、凭据、费用、上下文上限或安全观测限制时，交付阻断证据与下一步条件；不得为了形式上的“全部通过”降低验收标准。
