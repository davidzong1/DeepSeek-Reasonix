# Team Member 缓存命中率：联合结论后的三 Agent 后续执行方案

> 状态：**阶段性执行完成（P0/P1/P2 与 P4 合流有记录；P3 正式分层实验及 §9 未决项待执行）**。本状态不表示整条主线或发布已完成；阶段记录见 `TEAM_MEMBER_CACHE_MERGE_RECORD.zh-CN.md`。
> 日期：2026-09-24
> 依据：`TEAM_MEMBER_CACHE_JOINT_CONCLUSION.zh-CN.md`、`TEAM_MEMBER_CACHE_PART_A_CONTRACT.zh-CN.md`、`TEAM_MEMBER_CACHE_PROVIDER_EXPERIMENT_PART_B.zh-CN.md`、`TEAM_MEMBER_CACHE_PART_C_REVIEW.zh-CN.md`
> 目标：在 K1 计量修复之后，建立可信的 Team member 前瞻性基线，验证跨 Provider usage 语义，并定位历史跌幅中尚未解释的部分。

## 1. 执行结论

本轮不能直接继续做“缓存策略优化”，也不能把裸请求的 99.9%+ 命中率当作 Team member 的结论。联合结论已经把问题拆成三个不同层次：

1. **计量层**：Anthropic 兼容适配层曾把早期整段 `input_tokens` 估算值与后续 cache split 混合，造成虚高 miss。K1 修复的是读数正确性，不改变 provider-visible 请求字节，因此不能宣称它直接提高了真实缓存命中率或降低 API token 消耗。
2. **Provider 行为层**：单网关、单账号、串行裸请求在 33K–533K 的受控样本中达到 99.9%+，只能说明该实验条件下未观察到“上下文越长必然越低”的证据，不能覆盖真实 Team 的任务组成、并发、路由、账号和压缩边界。
3. **历史差异层**：09-23 与 09-24 的历史账本差异中，K1 假象最多解释约 14pp，而 `32k_128k` 桶仍有约 19pp 未解释。历史账本缺少 provenance，且旧数据受 K1 前归一化影响，不能把这 19pp 直接命名为缓存退化。

因此后续优先级固定为：

- **P0：验证 K1 在所有受支持 Provider/事件形状上不误报、不回归。**
- **P0：使用 K1 后真实 Team member 请求建立新的逐请求基线。**
- **P0：修正实验夹具和统计契约中的可比性缺口，避免再次用闭合等式替代真实 oracle。**
- **P1：按任务组成、上下文大小、会话阶段、并发、route/model/account 分层，定位剩余差异。**
- **P1：只有证据足够时才提出缓存行为优化；没有证据时保持“未决”。**

## 2. 不可宣称项与安全边界

以下结论在完成本计划前禁止写入发布说明或优化结论：

- 禁止把 `billing_usage` 私有字段无条件称为跨 Provider 的权威协议；它只能作为可选旁证，且并非每个响应都有。
- 禁止把 `prompt == hit + miss` 作为命中率正确性的充分证明；该等式可能由归一化代码自行构造出来。
- 禁止把本地 `prefix_hash` 当作 Provider cache key，也不能据此证明“缓存命中”。
- 禁止把单 route、单账号、串行裸请求结果外推到所有 Team member。
- 禁止把历史账本的 84.5%/67.7% 或此前 85.1%/68.4% 直接作为 K1 后的 per-request 基线。
- 禁止为了得到更高命中率而排除低命中样本、修改统计分母、把 unknown 归零或将聚合请求伪装成单请求。
- 禁止在没有真实 Team 复现前实施工具裁剪、上下文裁剪、压缩策略调整、路由切换或成员隔离变更。
- Context Rescue、`/clear` 等上下文续接机制不属于本计划的第一阶段优化；它们只能作为上下文硬上限的独立安全路径，不得用来掩盖 usage 计量问题。

## 3. 三 Agent 并行分工

### Agent A：跨 Provider usage 语义与 K1 正确性

**目标**：确认 K1 的流事件折叠和 inclusive/exclusive 归一化在已支持 Provider 与边界事件上成立，并暴露无法判定的样本。

**工作范围：**

- 盘点 `internal/provider/anthropic/stream_usage.go`、`messages_usage.go`、`anthropic.go` 的事件折叠、路由判定和归一化路径。
- 覆盖 native Anthropic、LongCat、DeepSeek 兼容流，以及字段缺失、显式零值、部分 split、全命中省略 split、重复事件、事件乱序和多次 `message_start`。
- 将“内部账目闭合”与“符合上游真实语义”分开验收。
- 有 `billing_usage` 时做逐字段旁证；无该字段时使用协议事件序列、已知 Provider 语义、冷/热受控差分或明确标记为未决。
- 不修改请求构造、缓存策略、上下文裁剪、成员隔离和统计分母。

**建议写集：**

- `internal/provider/anthropic/stream_usage.go`
- `internal/provider/anthropic/*usage*_test.go`
- 必要时增加脱敏 SSE fixture 与最小诊断字段；不得写入 prompt、工具正文、凭据或完整响应。

**禁止修改：**

- `internal/team/**` 的统计契约与报表分母；
- `internal/cachelab` 的实验结果解释；
- Team member 的 provider-visible 请求构造。

**必须交付：**

1. Provider/事件形状矩阵：每一行注明“代码可证、fixture 可证、真实上游已观测、未验证”。
2. 表驱动测试：native start+delta、LongCat delta-only、DeepSeek 双 start、无 split、split 后省略字段、零值/缺失、重复/乱序、异常计数。
3. 每种形状的归一化结果、`prompt/hit/miss/write` 关系及未知状态处理。
4. K1 是否能在无 `billing_usage` 时独立验证的结论。
5. 按 Provider 给出“通过 / 有限通过 / 未决 / 阻断”，不能用单网关结果概括所有兼容端点。

### Agent B：K1 后真实 Team 基线与剩余跌幅定位

**目标**：在 K1 后、真实 Team member 工作流中，建立逐请求基线，分解低命中是否来自任务组成、上下文阶段、并发、route/account 或其他尚未定位因素。

**工作范围：**

- 只做观测、实验和分层分析，不修改生产缓存行为。
- 固定并记录构建、team/member/session/turn、精确 route/model、账号范围、请求序号、重试关系和 usage 来源。
- 使用真实 member 请求作为主证据；裸请求只作为 Provider/route 受控对照。
- 重点补测联合结论中未验证的 1M 附近真实 Team 请求、会话后段、压缩/重建边界、任务组成和并发。

**建议写集：**

- 实验脚本、脱敏日志和报告文档；
- 如必须增加字段，仅提交观测字段，不改变请求字节；
- 不编辑 Agent A 的 Provider 文件，不编辑 Agent C 的统计口径文件。

**实验矩阵：**

| 维度 | 最低分层 | 约束 |
|---|---|---|
| 上下文大小 | `<32k`、`32k–128k`、`128k–256k`、`256k–512k`、`512k–768k`、`768k–1m`、`>=1m` | 使用实际 `context_prompt_tokens`；1M 桶必须来自真实 Team，不得用裸请求替代 |
| 并发 | 1、2、4 或实际负载阶梯 | 分开记录 Team 同时运行 member 数与单 member in-flight 数 |
| 任务组成 | 代码读改、长文档分析、工具密集、多轮推理、短问答/规划 | 冻结任务族或标记任务类型；历史无标签时不得声称可重放 |
| 会话阶段 | 首轮、连续 warm、长会话后段、压缩/重置前后 | `hit=0` 只称“未报告 cache read”，不直接称冷启动 |
| route/model | 精确 route bucket × 精确 model | 不同 route 永不池化 |
| 账号 | 固定单账号；有审批再加第二账号 | 账号不稳定时不做跨账号结论 |

**执行顺序：**

1. 固定 K1 构建、成员、任务族、route/model/account，先跑串行稳定基线。
2. 在可比样本中比较任务组成、上下文桶和会话前后段。
3. 在已稳定条件下逐级增加并发，按 session/batch 聚类分析。
4. 仅在可授权且可追溯时扩展 route/account。
5. 单独补采 `768k–1m` 与 `>=1m`，不得以合成长 prompt 替代真实 Team。

**样本规则：**

- 每个用于跨成员推断的主要桶至少 30 个有效单请求、至少覆盖 3 个成员；不足则标记 `insufficient_sample`。
- 先做 pilot 估算有效率、会话间变异和排除比例，再预注册正式样本量；同一 session 的连续请求不能当作完全独立重复。
- 预注册停止条件：达到有效样本/成员/独立会话门槛、最长采样窗口、费用上限或数据质量阻断；不得因为结果有利而提前停。
- 构建混用、route/account 漂移、usage 无法解析、任务标签失真或异常错误率出现时暂停该层并隔离样本。

**必须交付：**

1. K1 后 Team member 逐请求数据字典与脱敏日志。
2. 按上述维度的基线报告，至少同时报告命中率、`miss_tokens_per_request`、`prompt_tokens_per_request`、有效样本数、成员数和 coverage。
3. 任务权重变化与层内变化的分解；明确不可解释残差。
4. 对 1M 上下文和压缩/重建边界的单独结论。
5. 若仍低命中，给出可复现的层内候选；若只在历史账本出现，结论为“历史不可归因”，不得强行设计缓存修复。

### Agent C：独立统计复核、夹具修正与合流准入

**目标**：提供独立的统计和 oracle 复核，确保 A/B 的结果可比较，并负责最终 go/no-go、灰度和回滚建议。

**工作范围：**

- 独立审查 `billing_usage` 的出现率、字段含义与可比性；设计不依赖该私有字段的核对路径。
- 修正 `cachelab` 对混合 usage 词表、嵌套字段和缺失/零值的解析缺口；重跑受影响臂。
- 复核历史账本降级为全样本口径的合理性，并保留“历史口径”与“可信 per-request 口径”的明确分支。
- 复核 Agent A 的 Provider 测试与 Agent B 的真实 Team 报告，防止以单一指标或不完整样本放行。

**建议写集：**

- `internal/cachelab/**`
- `internal/team/cachereport*`、`internal/team/cacherequest.go`、相关 cache audit/report 测试
- 实验报告、统计审查记录、灰度/回滚文档

**禁止修改：**

- 不改变 A 的生产 Provider 语义实现；
- 不改变 B 的 Team 请求构造或任务组成；
- 不为了结果好看调整统计分母、排除规则或历史数值。

**必须交付：**

1. `billing_usage` 有/无两种路径下的 K1 复核结果；无独立证据时明确“未决”。
2. `cachelab` 混合词表、嵌套 usage、缺失字段、显式零值、unknown/no-split 的测试与重跑结果。
3. A/B 的数据口径差异表，确认两者不互相合并、不把实验 journal 当作成员账本。
4. 正式 go/no-go：通过、有限通过、继续采样或阻断；并附灰度、回滚、版本切换公告。

## 4. M0 共同冻结项与依赖

三个 Agent 可立即并行进行只读盘点、测试设计和数据字典编制；任何生产行为变更前，由负责人单独完成 M0：

- 冻结基线 commit、工作树状态、K1 是否已纳入待测构建；本轮不得把未提交改动误称为已合流。
- 冻结 Provider、route/model、账号范围、Team member、任务族、并发和采样时间窗。
- 冻结统计定义：有效单请求、聚合、unknown/estimated、无 split、错误、重试、冷启动和 warm 的分类。
- 冻结主指标：`Σhit_tokens / (Σhit_tokens + Σmiss_tokens)`，并同时报告 hit/miss tokens per request。
- 冻结字段语义：`billing_usage` 是可选 oracle，不是跨 Provider 契约；本地 prefix hash 不是 Provider cache key。
- 冻结隐私规则：不落盘 prompt、工具参数/结果正文、凭据和完整敏感响应，只保留摘要、digest、token 和状态字段。
- 为 A/B/C 建立独立工作区或写集，禁止覆盖现有未提交文件。

### M1：A/B/C 接口字段冻结

三方只共享数据契约，不共享生产实现文件：

```text
UsageEvidence:
  provider
  route
  model
  usage_source
  raw_event_shape
  cache_split_present
  billing_oracle_present
  oracle_match
  semantic_status

TeamRequestObservation:
  team_id
  member_id
  session_id
  turn_id
  request_seq
  attempt
  request_count
  request_count_source
  context_prompt_tokens
  prompt_tokens
  hit_tokens
  miss_tokens
  cache_write_tokens
  maintenance_phase
  task_family
  concurrency_level
  route_bucket
  model_ref
  request_shape_digest

DecisionReceipt:
  evidence_level
  inclusion_class
  exclusion_reason
  coverage
  next_action
```

A 负责字段语义与 Provider evidence，B 负责 TeamRequestObservation 的真实采集，C 负责分类、统计、审计和最终 DecisionReceipt。任何缺字段的样本保留为 unknown/coverage 缺口，不自动填 0 或 1。

## 5. 并行执行阶段与合流顺序

### 阶段 P0：只读盘点（A/B/C 并行）

- A：画出 wire usage → stream fold → inclusive/exclusive → stats 的调用链，列出每种事件形状的未知点。
- B：确认 Team member 观测是否能拿到 member/session/turn/route/model/context/attempt，设计不含敏感正文的日志。
- C：核对 cachelab 词表解析、历史 provenance、报告字段和样本分类，建立口径差异表。

**P0 合流门槛：**三方提交清单后，负责人确认写集、字段契约和停止规则；未通过不得扩大真实实验。

### 阶段 P1：低风险测试与夹具修正（A/C 并行，B 只做采集准备）

- A 先补 Provider 事件表驱动测试；只有测试证明当前规则不成立时才修改 K1。
- C 修复仅限实验夹具和统计诊断的缺口，重跑离线测试；不得修改 Team 生产请求。
- B 完成任务族、上下文桶、并发和会话阶段标记，执行脱敏采集试跑。

**P1 合流门槛：**`go test`、`go vet`、仓库守卫通过；请求字节摘要与 K1 前基线一致；unknown/缺失路径有明确输出。

### 阶段 P2：K1 后真实 Team pilot（B 主执行，A/C 并行审计）

- B 先跑串行、固定 route/model/account 的真实 member pilot。
- A 抽样核对 Provider 原始事件与归一化结果，覆盖有/无 oracle 场景。
- C 独立重算 pilot，确认有效样本、排除项、coverage 和 token 对账。

**P2 停止条件：**构建或 route 漂移、usage 解析失败、隐私违规、异常错误/重试、请求任务标签不稳定。停止后先修数据，不得继续扩大样本。

### 阶段 P3：正式分层实验（B 主执行，C 统计复核）

按“任务族/会话阶段 → 上下文大小 → 并发 → route/account → 1M 专项”的顺序扩展，每次只新增一个主要变量。C 在预注册分析节点独立出具分层结果，A 只处理发现的 Provider 语义阻断。

### 阶段 P4：合流与准入（A/B/C 汇合）

合流时必须分别回答：

1. K1 是否按 Provider/事件形状通过，而非仅按一个网关通过？
2. K1 后真实 Team member 的命中率、miss/request、usage coverage 是多少？
3. 09-23/09-24 的历史差异能否可信重算？若不能，哪些部分保持未决？
4. 观察到的差异是层内变化、任务组成变化、样本选择变化，还是仍无法解释？
5. 是否存在足够证据实施新的缓存行为优化？

## 6. 统一指标与验收门槛

### 6.1 必须报告

每个 stratum 至少报告：

- 有效单请求数、成员数、独立 session/batch 数；
- `hit_tokens`、`miss_tokens`、`hit_tokens_per_request`、`miss_tokens_per_request`；
- prompt tokens 的均值和分位数；
- 加权命中率、成员等权命中率、会话累计率，并注明三者不可互换；
- route/model/account、上下文桶、并发、任务族、会话阶段；
- error、retry、unknown/estimated、no-split、aggregate、invalid accounting 的排除计数；
- usage_source、oracle 覆盖率、request_count provenance、请求形状诊断覆盖率；
- 任务完成率、必要上下文保留、工具调用正确率和延迟（若实验涉及行为变化）。

### 6.2 通过门槛

- **计量正确性**：每种已声明支持的事件形状有确定性测试；`prompt == hit + miss` 仅作为必要不变式，不能单独作为充分证据。
- **Provider 隔离**：native Anthropic、LongCat、DeepSeek 和其他路由分别给出结论；未验证路由标记未决。
- **Team 可追溯性**：主指标样本具备 observed request count、成员/会话归属、有效 usage 和可审计排除理由。
- **实验可比性**：固定条件下只改变一个主要变量；不同 route/account、裸请求和真实 Team 不混合计算。
- **样本充足性**：主要跨成员桶达到 30 个有效请求且至少 3 个成员；不足只允许发布描述性结果并标记 `insufficient_sample`。
- **历史结论边界**：没有可信原始 usage 或可比新旧任务时，不能声称解释了约 19pp；只能报告新的前瞻性基线和未决残差。
- **优化准入**：只有候选在真实 Team 层内可复现、具备最小效应、任务质量不回归、token 成本不恶化、请求形状影响可解释时，才进入下一轮缓存行为优化。

### 6.3 阻断条件

- K1 在任一已支持 Provider 上出现 input/cache 误折叠或归一化回归；
- 缺失与显式零无法区分，导致全命中/无 split 被确定性误判；
- 使用私有 `billing_usage` 扩大为跨 Provider 契约；
- Team 观测缺少 provenance，仍把聚合/unknown 当作单请求；
- 真实 Team 低命中样本不足，却据此修改压缩、工具或请求构造；
- 请求正文、工具结果或凭据进入实验日志；
- 任何优化导致任务质量、必要上下文保留或错误率恶化。

## 7. 结果分支与下一步

### 分支 A：K1 通过，Team warm 也接近 Provider 受控结果

说明计量层和当前受测 route 的缓存行为没有支持“长上下文必然退化”的证据。保留新基线，继续观察真实任务组成、并发和 1M 桶；不立即改工具 schema 或压缩策略。

### 分支 B：K1 通过，但真实 Team 仍低命中

只在能够复现的 Team 分层内继续做因果实验：优先任务组成/会话阶段，再看并发、route/account 和压缩/重建边界。任何新候选必须附带独立对照、miss/request 影响和任务质量护栏。

### 分支 C：K1 后命中率提高，但历史跌幅仍存在

优先判定历史数据或工作负载不可比。除非可以用可信原始 usage 重算历史，否则不把“命中率提高”解释为真实缓存改善，也不把剩余差异归因给某个缓存 bug。

### 分支 D：Provider 语义无法统一或 oracle 不足

按 Provider/route 降级为有限支持；保留 unknown/未决状态，必要时增加 presence 信息或最小接口变更。不得用猜测性 clamp、默认值或闭合等式发布确定性命中率。

## 8. 交付物清单与派单文本

### 8.1 交付物

- Agent A：Provider usage 语义矩阵、表驱动测试、K1 边界结论、未验证清单。
- Agent B：K1 后真实 Team pilot/正式实验、逐请求脱敏数据字典、分层报告、剩余差异分解。
- Agent C：cachelab/统计修正、独立重算报告、样本分类审计、go/no-go、灰度与回滚方案。
- 负责人：M0/M1 冻结记录、合流纪要、版本切换公告、历史数据不可比声明（如适用）。

### 8.2 可直接派给 Agent A

> 只处理 Provider usage 语义和 K1 正确性。先盘点路由与事件形状，再补 native Anthropic、LongCat、DeepSeek、缺失/零值/多事件边界测试。不要修改请求构造、缓存策略、成员隔离和统计分母。最终按 Provider 给出通过/有限通过/未决/阻断，并明确无 `billing_usage` 时的证据边界。

### 8.3 可直接派给 Agent B

> 在包含 K1 的固定构建上采集真实 Team member 请求。先串行固定条件 pilot，再按任务组成、会话阶段、上下文桶、并发、route/account 和 1M 专项扩展。逐请求记录 provenance、usage source、context tokens、hit/miss、miss/request 和排除理由；不要把裸请求或历史无 provenance 账本当作 Team 基线。样本不足时保持未决。

### 8.4 可直接派给 Agent C

> 独立复核 K1 的 oracle 假设，修正 cachelab 混合 usage 词表/嵌套字段/缺失与零值解析，重跑受影响实验。复核历史账本降级口径和 A/B 可比性，输出独立 go/no-go、灰度、回滚与版本切换建议；不得为结果调整分母或排除不利样本。

## 9. 建议命令与归档规则

离线验证优先执行：

```bash
go test ./internal/provider/anthropic/ ./internal/team/ ./internal/stats/ ./internal/cachelab/
go test ./internal/cli/ -run 'Cache|Usage|Team' -count=1
go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...
go run ./tools/repolint
bash scripts/cache-guard.sh
```

真实实验需在有凭证和批准的环境执行，至少归档：

- 构建 commit、实验臂登记、route/model/account 稳定 ID、开始/结束时间；
- 脱敏 JSONL、schema 版本、样本分类与停止原因；
- 原始 usage 事件摘要及是否存在 oracle；
- 报表输入、过滤条件、版本和独立复算结果。

归档不得进入 Git 的敏感文件，不得把 `/tmp` 作为唯一副本；实验原始日志被清理前应转移到受控、可备份的位置。

## 10. 最终决策原则

本计划的成功标准不是让报表出现更高百分比，而是让以下问题可以被诚实回答：

- 命中率数字是否反映 Provider 实际 usage，而不是适配层跨事件误合并？
- K1 后真实 Team member 在 1M 上下文、长会话、并发和压缩边界的行为是什么？
- 历史约 19pp 的差异有多少能被可信数据解释，有多少必须继续保留为未决？
- 下一项优化是否真正减少 `miss_tokens_per_request` 和总 token 消耗，同时不破坏主线任务质量？

若证据不足，正确结果是“继续采样/未决”，而不是通过修改统计口径或扩大单路由实验的解释范围来制造确定性。
