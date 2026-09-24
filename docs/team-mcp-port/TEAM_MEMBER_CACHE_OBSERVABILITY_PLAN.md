# Team Member 缓存命中率：可观测性与基线执行方案（Part A）

> 范围：只负责 Team member prompt-cache 的请求级观测、统计口径和优化前基线；不实施缓存行为优化，不改 leader 或普通 CLI session 的指标。
> 状态：执行方案；实现前需按本文字段契约推进。关联主计划：`docs/team-mcp-port/TEAM_MEMBER_CACHE_OPTIMIZATION_PLAN.md`。

## 1. 目标与非目标

本 Part 的目标是让一次 provider request 可被定位、分桶和归因，并能在多个 Team member 间生成可复现的基线。完成后，应能回答：低命中是某个请求的现象还是 session 累积结果；发生在何种 prompt 长度、成员、模型/路由和会话阶段；当时稳定前缀是否变化；变化与缓存 miss 是否相关。

本 Part 不试图仅凭本地 hash 证明 provider 一定使用了相同 cache key，也不把 `context_used` 当作请求 prompt 长度，不在此阶段改写 prompt、工具 schema、compaction 或 provider 路由。所有内容正文继续不采集。

## 2. 当前实现与缺口

当前主链路如下：

1. 每轮请求前，`internal/agent/run_loop.go` 捕获 `PrefixShape` 并排空内容重写原因。
2. `internal/agent/cache_shape.go` 的 `CaptureShape` / `CompareShape` 生成稳定前缀 hash、工具 schema token 估算、前缀变化原因和本次 usage 的 hit/miss。
3. `internal/agent/run_usage.go` 将 provider usage 与 `CacheDiagnostics` 一起发为 `event.Usage`；`internal/event/session_context_diagnostics.go` 定义诊断字段。
4. CLI 的 `internal/cli/chat_tui_events.go`、`internal/cli/run_metrics.go` 消费 usage event；Team writer 则由 `internal/cli/team_usage_publish.go` 每 2 秒采样 controller gauges，映射到 `internal/team/ownerusage.go` 的 `.usage.json`。
5. 当前 `.usage.json` 只保存 `last_turn` 的 token 摘要、session 累计 hit/miss 和 context gauge，不保留逐请求历史或 `CacheDiagnostics`。因此周期快照会覆盖前值，不能用于事后按请求分析。

现有诊断的解释边界：`StablePrefixHash` 仅表示本地 system + tools 形状；`PrefixHash` 另包含 session-context digest；`PrefixChangeReasons` 是本地已识别变化，不是 provider cache miss 的证明。`ToolSchemaTokens` 是 `internal/agent/cache_shape.go` 中按字节长度估算的值，不是 tokenizer 精确账单。`CacheDiagnostics.CacheHitTokens/CacheMissTokens` 和 `provider.Usage` 的字段可能反映聚合 attempt，故必须结合 `RequestCount`、`Unknown`、`Estimated` 和 `Context*` 字段定义统计单位。

## 3. 字段契约

### 3.1 每请求记录的必需字段

建议实现一个独立的、版本化的 `MemberCacheRequest` 记录（存储可以是有界本地历史或 stats event；不要把无界数组追加进 `.usage.json`）。字段契约如下：

| 字段 | 类型/来源 | 语义与要求 |
|---|---|---|
| `schema_version` | int | 记录 schema 版本；新增字段向后兼容，读取旧记录不得失败。 |
| `request_id` | string | 单个 provider request 的唯一关联 ID；优先复用 attempt/request ID。不可用时由 writer 生成稳定唯一 ID，并标记生成来源。 |
| `observed_at` | RFC3339Nano | provider usage 被接收/记录的时间，UTC；区别于 Team `.usage.json` 的 publish 心跳时间。 |
| `team_id`, `member_id` | string | 由 owner key 提供；member 必须是 Team member。不得把 leader 混入 member 聚合。 |
| `provider`, `model_ref`, `route_bucket` | string/可选 | 能定位模型和路由变化；账号、endpoint 等敏感/高基数字段仅用稳定脱敏值或配置分桶。未知值为空并保留 unknown 状态。 |
| `prompt_tokens`, `cache_hit_tokens`, `cache_miss_tokens` | int | provider 报告的请求输入与 cache split；不得用 completion token 计入命中率。保留原始整数，不先做比率舍入。 |
| `cache_write_tokens` | int/optional | 若 provider usage 支持，单独记录；按 `provider.Usage` 定义，它是 miss 子集，绝不加到 miss 上再次计数。 |
| `usage_unknown`, `usage_estimated` | bool | 分别映射 `provider.Usage.Unknown`、`Estimated`。Unknown/估算样本不得悄悄并入精确 cache 基线。 |
| `request_count` | int | usage 代表的 provider request 数；0 按现有兼容语义视为 1。多 request 聚合样本须标注，不可假装成单请求。 |
| `context_prompt_tokens` | int/optional | usage 的最新单请求 context prompt；如果大于 0，用于 last-request 观测；与可计费聚合 `prompt_tokens` 分列。 |
| `context_used`, `context_window` | int | 同时点 Team context gauge，仅作上下文状态参考，不作 prompt 桶键或命中率分母。 |
| `session_request_seq` | int/optional | 同一 member session 内递增序号；用于区分首请求、后续 warm request、恢复后请求。若无法可靠原子生成则允许缺失并记录原因。 |
| `seconds_since_prev_request` | number/optional | 同一 writer/member 的相邻 provider request 间隔；首请求为空。禁止跨 session 计算。 |
| `prefix_hash`, `stable_prefix_hash` | string/短 hash | 映射 `event.CacheDiagnostics`；不得存原 prompt 或原始工具 schema。 |
| `prefix_changed`, `stable_prefix_changed` | bool | 原样映射诊断。没有前序 shape 时需以 `diagnostics_available=false` 表示，不能当作 false（未变化）。 |
| `prefix_change_reasons` | string[] | 受控枚举：当前包括 `system`、`tools`、`session_context` 及内容重写原因（如 `compact_auto`、`snip`、`rewind_truncate`）；未知原因保留原值并归入 unknown 统计。 |
| `tool_schema_tokens_estimate` | int | 映射 `ToolSchemaTokens`，字段名明确标识估算值。 |
| `diagnostics_available` | bool | 是否存在完整诊断对象；nil 和“诊断字段全零”必须区分。 |
| `session_context_digest`, `session_context_reasons` | string/array/optional | 可选映射 `SessionContext` 的 digest/reasons/section chars；只保留内容无关的 digest、字符数与枚举，不采集内容。 |
| `finish_reason`, `usage_source` | string/optional | 分析异常终止、数据来源；不改变 cache hit 公式。 |

所有 token 字段应校验非负。`prompt_tokens`、`cache_hit_tokens`、`cache_miss_tokens` 的不闭合情况（例如 hit + miss 大于 prompt）必须记为 `accounting_valid=false` / 数据质量异常，而不是截断或静默修正。不同 provider 对 prompt 与 cache split 的计费定义可能不同，基线以 provider 给出的 split 为准并按 provider/model 分层。

### 3.2 快照字段与历史记录分工

`OwnerUsage.LastTurn` 继续表达最近一次 usage 快照；建议 additive 地挂入最近请求的 `CacheDiagnostics`、request ID/时间和 request-count/estimated 状态，供 Team UI 展示。`.usage.json` 保持小、有界、原子替换，不承担历史用途。逐请求基线必须进有界历史或既有 stats 记录通道，并有保留期限/容量上限；写失败只影响诊断，不阻塞 member 请求。

`OwnerUsage.CacheHit/CacheMiss` 仍是 session totals，仅用于 session 累计率。不要由 2 秒轮询快照差值反推 request history：进程退出、计数重置、发布丢失都会造成漏记或错记。应在收到 `event.Usage` 的请求边界写一条记录，再由发布层读取最近样本用于快照。

## 4. 数据流实施路线

1. **定准 request 边界**：从 `internal/agent/run_loop.go` 的一次 `streamWithSamplingRecovery` 结束点和 `internal/agent/run_usage.go:emitTurnUsage` 跟踪 usage。确认 `provider.Usage.RequestCount`、重试/采样恢复聚合与 `Context*` 的约定；选择一个可关联的 request/attempt ID。若一次 event 包含多 provider request，第一版可以存聚合事件，但必须 `request_count > 1`，并从单请求分布中排除。
2. **保留 Agent 已有诊断**：不要重复计算 shape。将 `event.Usage` 的 usage 与 `CacheDiagnostics` 投影到 Team member 记录；只有 member owner writer 路径开启 Team 记录，普通 CLI 和 leader 不进入该数据集。
3. **建立记录模型和持久化**：在 `internal/team/ownerusage.go` 附近只放跨层存储契约，不引入对 agent/provider 的反向依赖；CLI 做映射（模式参考 `ownerUsageLastTurn` / `providerUsageFromLastTurn`）。历史存储采用有界 append/环形保留或 `internal/stats` 现有记录能力，明确并发、原子性、最大记录数/字节数、保留期、损坏跳过行为。禁止写入 prompt、completion、工具参数正文。
4. **暴露快照与导出**：`internal/cli/team_usage_publish.go` 的 2 秒 publisher 继续仅发布最新快照；`internal/cli/team_follower_usage.go` 映射只读展示字段时兼容老 schema。分析导出应输出逐请求样本或聚合 CSV/JSON，且每行包含 `team_id/member_id`、schema 版本和采样状态。
5. **生成基线报表**：聚合器对原始整数做汇总，输出每个分桶的样本数、成员数、hit/miss 总数、token 加权命中率、请求命中率分布 P10/P50/P90，以及冷/warm/unknown 分层。保存查询条件、时间窗、版本和排除样本数，使结果可重跑。

## 5. 统计口径

### 5.1 请求级指标

- **请求 token 命中率**：`cache_hit_tokens / (cache_hit_tokens + cache_miss_tokens)`。仅当分母大于 0、usage 精确、accounting valid 且请求单位为单 provider request 时参与主基线。
- **请求 miss 量**：`cache_miss_tokens`，必须与命中率并列展示；相同 miss token 在不同 prompt 规模下代表不同影响。
- **缓存覆盖率/usage 完整率**：分别报告有有效 cache split 的请求数 / 全部请求数，以及精确有效请求数 / 全部请求数。若 provider 不返回 split，不推导为 0% 或 100%。
- Provider 的 `prompt_tokens` 与 `hit + miss` 若不一致，保留 provider 原值并标记质量异常；prompt 桶以 `context_prompt_tokens`（有值时）为请求实际 context 形状，否则 `prompt_tokens`。在报告中同时注明选用字段和两字段偏差分布。

### 5.2 Session 与成员聚合

- **session 累计率（token 加权）**：`Σsession_cache_hit / (Σsession_cache_hit + Σsession_cache_miss)`。它描述整个 session 的输入 token 分配，不等于末次请求或特定 prompt 桶。
- **桶内 token 加权率**：桶内 `Σhit / (Σhit + Σmiss)`；这是判断大 prompt 桶整体 token 命中效果的主聚合指标。
- **请求率算术均值/分位数**：先计算每个有效请求的 rate，再计算简单均值及 P10/P50/P90；反映一次请求体验，不可与加权率混称“平均命中率”。
- **成员简单平均**：先按 member 分别汇总（优先在同模型/路由、同时间窗和同 prompt 桶内），再对有足够样本的成员等权平均。必须同时列出成员数和每成员样本量。
- **跨成员 token 加权率**：只作为补充总量指标，不替代成员简单平均。leader、普通 CLI session、unknown/estimated、多 request 聚合、无效账目均不混入主精确基线；排除数逐类披露。

## 6. Prompt 分桶及阶段分层

主分桶按请求实际 prompt/context prompt token 数，边界左闭右开，单位 tokens：

| bucket | 范围 |
|---|---:|
| `lt_32k` | `< 32,768` |
| `32k_128k` | `32,768–131,071` |
| `128k_256k` | `131,072–262,143` |
| `256k_512k` | `262,144–524,287` |
| `512k_768k` | `524,288–786,431` |
| `768k_1m` | `786,432–1,048,575` |
| `gte_1m` | `>= 1,048,576` |
| `unknown_prompt` | 无有效请求 prompt token 值 |

分桶不得使用 `context_used`。`context_used` 与请求 prompt 同时报差值/比值分布，帮助解释 UI gauge；不可用 gauge 把样本重新归到 1M。按需另做 `32k`、`64k`、`128k` 等基准阶梯，但不可替代上述固定报告桶。

每个 prompt 桶至少再按以下维度切分或筛选：

- Team/member role 与 member ID；跨成员比较同时给出总体和逐成员数据。
- `provider`、`model_ref`、脱敏 `route_bucket`；模型或路由不兼容时不合并。
- **请求阶段**：`first_request`（该 session 第一个可识别请求）、`warm_candidate`（同 session 有前请求且间隔可计算）、`post_rewrite`（存在前缀变化/内容重写）、`unknown_stage`。阶段可以重叠，报表应标出定义；“warm candidate”不是 provider 缓存已命中的事实。
- 距上次请求时间分桶：`<1m`、`1m–5m`、`5m–30m`、`>=30m`、`unknown`。这些只是观察分层，不预设 TTL。
- 是否 `stable_prefix_changed`、`prefix_changed` 和具体 `prefix_change_reasons`；另按 `ToolSchemaTokens` 估算范围检查 schema 大小相关性。
- `usage_unknown`、`usage_estimated`、`request_count`、cache split 缺失、账目不闭合等数据质量维度；主基线只收有效精确单请求，另有质量覆盖报表。

每桶建议最低样本门槛：不少于 30 个有效请求且不少于 3 个 member 才发布跨成员推断；未达门槛标注 `insufficient_sample`，仍可展示原始 count 和 member 内观测但不得声称趋势。主计划的优化 KPI（warm 且 prompt ≥128K 的桶加权率目标）属于后续行为优化验收，不作为本 Part 的观测系统正确性门槛。

## 7. 诊断归因流程

对低命中样本按以下顺序排查；每一步只产生关联证据，不把相关性写成因果结论：

1. **先排数据与口径**：检查 usage 是否未知/估算、cache split 是否报告、是否多 request 聚合、`prompt_tokens` 与 `context_prompt_tokens`/`hit+miss` 是否差异异常；确认是否误把 session totals 或 `context_used` 当作该请求数据。
2. **再确认比较组可比性**：匹配 Team member、provider/model/route、时间窗和 prompt 桶；区分首请求、重写后请求和 warm candidate。缺 route 信息时归为未知，不假设相同 cache scope。
3. **稳定前缀是否变化**：比较相邻请求 `stable_prefix_hash` 和 `stable_prefix_changed`。变化时按 `system`、`tools`、session context / rewrite reason 统计 miss token 增量；若 hash 变化但 reason 缺失，归为 unexplained-prefix-change，优先补 instrumentation。
4. **稳定前缀不变但 miss 偏高**：对比绝对 miss token 与 prompt 新增/尾部增长、`tool_schema_tokens_estimate`、时间间隔、model/route。稳定 hash 只覆盖 system/tools，不代表完整 provider 请求前缀稳定；本地目前不能独立精确测量消息尾部，应把该类标为 `stable_prefix_high_miss_unattributed`，避免武断归因 schema。
5. **重写/压缩关联**：单独对比 `compact_auto`、`snip`、`rewind_truncate`、session-context digest/reason 前后请求；按 first-after-rewrite 和后续请求分别统计。不能只因同轮命中低就断定 compaction 导致 provider cache reset。
6. **cache scope/TTL 候选**：在 stable hash、模型、路由可比时，对比间隔分桶与命中率；只报告“与时间/route 关联”，需受控同 route 对照或 provider 证据才能归因为 TTL/账号池。
7. **归因结果枚举**：`data_quality_or_semantics`、`stable_prefix_changed`、`rewrite_or_compaction_correlated`、`schema_size_correlated`、`tail_growth_or_content_correlated`、`route_or_interval_correlated`、`stable_prefix_high_miss_unattributed`、`insufficient_sample`。允许多标签；没有证据必须保留 unattributed/unknown。

不记录原始 prompt/tool schema 来提高可观测性。若需要更强的尾部诊断，应新增内容无关的长度/分段 fingerprint，并单独做隐私审查；Part A 的第一版不要求该扩展。

## 8. 代码锚点与建议测试

### 代码锚点

- `internal/agent/cache_shape.go`：已有 shape capture/compare、reason 生成和 schema token 估算；复用并清晰声明 hash 覆盖边界。
- `internal/event/session_context_diagnostics.go`、`internal/event/event.go`：诊断结构及 usage event 挂载点。
- `internal/agent/run_loop.go`、`internal/agent/run_usage.go`：请求前 shape 捕获、usage event 产出与 request 聚合边界。
- `internal/provider/provider.go`：`Usage` 的 cache split、Unknown/Estimated、RequestCount、Context* 字段语义。
- `internal/agent/agent.go`：`SessionCache()` totals 与 ChunkUsage 累加位置；验证重试/多 usage chunk 的计数单位。
- `internal/cli/team_usage_publish.go`：owner writer 每 2 秒快照发布、provider usage 映射；只扩展最近快照，不用于历史采集。
- `internal/team/ownerusage.go`：跨进程 `.usage.json` schema、大小限制、读写兼容；不要把它变成无界历史库。
- `internal/cli/team_follower_usage.go`：follower 对新增 optional 字段的读取映射。
- `internal/cli/run_metrics.go`：现有 usage/cache 诊断消费可供参考，但需确认 Team member scope、保留期与是否已有可复用的逐请求持久化。
- 测试锚点：`internal/agent/cache_diagnostics_test.go`、`internal/team/ownerstore_test.go`、`internal/cli/team_follower_usage_test.go`；wire 兼容另参考 `internal/eventwire/wire_test.go`。

### 必需测试

1. **映射 round-trip**：provider usage → owner/request record → decode，覆盖 hit/miss/write、Unknown、Estimated、RequestCount、Context*；缺字段旧记录仍可读。
2. **诊断投影**：Usage event 中的所有 cache diagnostics 准确传递；nil diagnostics 与零值 diagnostics 可区分；reasons 顺序/内容稳定，hash 不泄露正文。
3. **单位与聚合**：验证一次请求只写一个请求样本；RequestCount 0 的兼容语义；多 request aggregate 被标记/排除；session totals 不被重复累加；cache write 是 miss 子集而非额外加数。
4. **统计公式/边界**：命中率整数汇总后再除；零分母输出 N/A；prompt 桶边界 `32767/32768`、`131071/131072`、`786431/786432`、`1048575/1048576`；context gauge 不影响 bucket。
5. **数据质量**：Unknown/Estimated、split 缺失、负数、账目不闭合、prompt 缺失分别进入正确排除/异常计数，不静默归零或修正。
6. **历史存储健壮性**：容量上限、过期清理、并发追加/原子性、坏记录跳过、写失败不影响 Agent member 请求；隐私测试确保 prompt/tool 参数正文从不落盘。
7. **Team 隔离和兼容**：只记录 owner writer 的 Team member；follower 不重复写；leader/普通 CLI 不计入 member 数据；旧 `.usage.json` 可读，新旧 writer/reader 混跑字段缺失安全。
8. **报表可复现性**：相同 fixture 与查询条件生成相同桶、有效样本数、排除数、加权率、简单成员平均与分位数；样本不足触发明确状态。

## 9. 验收标准

- 单个有效 provider request 能从 usage event 追到一条持久记录，关联 Team/member、模型/路由桶、时间、prompt/cache split、请求单位与 cache diagnostics；诊断缺失时明确标为 unavailable。
- `.usage.json` 仍有界，保留向后兼容；历史记录有明确最大容量/保留时间，采集失败不影响 member 正常执行。
- 可以独立生成并复核四种数值：最近请求率、session 累计 token 加权率、成员简单平均、跨成员 token 加权率；报告不混淆 `context_used` 和 prompt tokens。
- 固定 prompt 分桶在边界测试通过；每桶公开有效样本数、member 数、hit/miss 总量、加权率、请求分布分位数和排除/未知计数。
- 对低命中样本能按本文流程产出明确诊断标签或 `unattributed/insufficient_sample`，不会仅凭稳定前缀 hash 宣称 provider cache key 相同。
- 不采集 prompt、工具参数/结果正文、secret；对 route/账号信息执行脱敏或分桶。
- 建立优化前基线快照：记录代码版本、观察时间窗、成员集合、模型/route 切分、样本门槛、各 prompt 桶统计与数据覆盖率。小样本及 cache split 不支持的模型明确标为不确定/不适用。

## 10. 实施顺序与交付

1. 审核 `provider.Usage` 的重试聚合语义并确定 request ID/序号来源；输出字段映射表及失败语义。
2. 实现 additive request record 与有界历史记录器；先用 fixture/测试证明 privacy、容量和故障隔离。
3. 从 Team member writer 的 usage event 接入记录，确保 leader/普通 session/follower 不写入；扩展 last-turn 快照仅用于在线展示。
4. 实现导出和统计报表，冻结分桶、排除规则和基线时间窗；提供可重复命令或测试 fixture。
5. 收集足够真实样本后发布 Part A 基线及覆盖率报告，再由 Part B 据此实施行为优化。Part A 不以提高命中率作为验收指标。

交付物：版本化逐请求字段契约、Team member 有界历史、可复跑基线报表、统计口径/数据质量测试和隐私检查结果。只有这些交付通过后，才将低命中根因假设交给优化阶段验证。

## 11. 实施记录

本节记录 §10 第 1~4 项的落地情况与实测口径。第 5 项（真实样本基线）尚未执行：它需要生产 Team 运行积累样本，不在本 Part 的代码交付范围内。

### 11.1 `provider.Usage` 聚合语义（§10.1 审核结论）

`internal/agent/run_usage.go` 的 `mergeSamplingUsage` / `finalizeSamplingUsage` 决定了 usage event 的形状：

- `PromptTokens` 是**可计费输入**，在 cache split 存在时被直接改写为 `CacheHitTokens + CacheMissTokens`，因此它不是单次请求的 prompt 大小；多 attempt 恢复时会跨 attempt 求和。
- `ContextPromptTokens` 等 `Context*` 字段由 `applyLatestContextShape` 只填**最后一次 settle 的 attempt**，才是"该请求自身的 prompt 形状"。
- `RequestCount` 表示该 usage 代表多少个 provider request；`mergeSamplingUsage` 在合并时累加，单次为 1（0 按 1 兼容）。大于 1 即多 request 聚合样本。
- `Estimated` 由失败 attempt 的估算路径置位；`Unknown` 表示至少一个 request 没有 provider usage。

据此：**分桶键取 `ContextPromptTokens`，无值时回退 `PromptTokens`**，并在报表中分开计数（`prompt_basis`）。`RequestCount > 1` 的样本标为聚合、排除出主基线。

### 11.2 request ID 与序号来源

usage event **不携带 provider request id**；`emitTurnUsage` 也不写 `event.AttemptID`（`settledAttemptID` 仅用于同轮日志与 stream-attempt 事件）。因此按契约"不可用时由 writer 生成稳定唯一 ID，并标记生成来源"实现：

- `RequestID`：event 带 turn 身份（`TurnID` 非空且 `Sequence > 0`）时取 `turn:<turnID>:<sequence>`，来源 `turn_event`；否则取 `writer:<team>:<member>:<seq>`，来源 `writer`。`TurnID`/`Sequence` 由 turnevent ledger 在发布前盖章，因此实际样本多为 `turn_event`。
- `SessionRequestSeq`：writer 进程内自增，随 `HasPrevRequest` / `SecondsSincePrevRequest` 一起用于区分首请求与 warm candidate。**不跨 session 计算**（计数随 publisher 生命周期重置）。

### 11.3 字段映射表

| 记录字段 | 来源 | 说明 |
|---|---|---|
| `schema_version` | `team.SchemaVersion` | 每条记录一份，旧记录缺字段可读 |
| `request_id` / `request_id_source` | 见 11.2 | |
| `observed_at` | usage event 到达 sink 的时刻 | UTC RFC3339Nano，区别于 `.usage.json` 的 publish 心跳 |
| `team_id` / `member_id` | `team.OwnerKey` | 仅非 leader 的可写 member |
| `provider` / `model_ref` | `event.ModelRef` 切分 | |
| `route_bucket` | 建造期的 provider 身份（见 §11.11） | `kind/hex12`，形如 `anthropic/107c70a37f88`；endpoint、账号、代理凭据全部只进哈希不进字段 |
| `turn_id` / `session_sequence` | `event.TurnID` / `event.Sequence` | 可为空，缺失即记空而不臆造 |
| `prompt_tokens` / `context_prompt_tokens` | `Usage.PromptTokens` / `ContextPromptTokens` | 后者为分桶键 |
| `cache_hit_tokens` / `cache_miss_tokens` | `Usage.CacheHitTokens` / `CacheMissTokens` | 命中率分母 |
| `cache_write_tokens` | `Usage.CacheWriteTokens` | miss 子集，**不**再加到 miss 上 |
| `request_count` | `max(Usage.RequestCount, 1)` | >1 标记聚合 |
| `usage_unknown` / `usage_estimated` | `Usage.Unknown` / `Estimated` | 精确基线排除项 |
| `usage_source` / `finish_reason` | `event.UsageSource` / `Usage.FinishReason` | 不参与命中率公式 |
| `context_used` / `context_window` | publisher 最近一次采样的 gauge | 只作上下文状态参考，**不作分桶键**；最多落后一个 publish 周期（2s） |
| `prefix_*` / `stable_prefix_*` / `prefix_change_reasons` / `tool_schema_tokens_estimate` | `event.CacheDiagnostics` 原样投影 | hash 只覆盖 system+tools，不代表 provider cache key |
| `session_context_digest` / `session_context_reasons` | `CacheDiagnostics.SessionContext` | 只取 digest 与枚举 |
| `diagnostics_available` | `CacheDiagnostics != nil` | 区分"无诊断"与"诊断全零（前缀未变）" |
| `accounting_valid` / `accounting_issues` | 写入时计算（`MemberCacheRequest.Accounting`） | 负数字段与 `hit+miss > prompt` 记为异常，**原值不修正** |

失败语义：记录写入是尽力而为。writer 侧队列（64，按需分配，follower/ambient 不分配）满则丢弃样本，写盘失败只记一次日志，均不影响 member 请求与 turn 结果。

### 11.4 存储与保留

- 位置：member owner 目录下 `.cache_requests.jsonl`（`.usage.json` 的兄弟文件）；`.usage.json` 继续保持整体替换，只挂最近一次诊断。
- 有界：单 member 上限 2 MiB；追加越界后按"先过期、再丢最旧一半"压缩，压缩用原子替换，读者只会看到压缩前或压缩后的完整文件。保留期 7 天。
- 容忍损坏：单行 JSON 解析失败（崩溃截断或手工编辑）跳过，不影响其余记录；读取上限 8 MiB，超出即视为非本日志并返回空。
- 并发：一个 member 只有一个 writer（赢得 session bind 的进程），因此不取锁；写入用 `O_APPEND` 单次写整批。

### 11.5 统计口径与分桶（冻结）

- 分桶键与边界按 §6：`lt_32k` / `32k_128k` / `128k_256k` / `256k_512k` / `512k_768k` / `768k_1m` / `gte_1m` / `unknown_prompt`，左闭右开（`32767/32768`、`131071/131072`、`786431/786432`、`1048575/1048576` 已有边界测试）。
- 主基线只收：精确（非 Unknown/Estimated）、单请求（`request_count <= 1`）、账目有效、有 cache split、时间戳可解析的样本；其余按 §11.3 的 `Exclusions` 逐类计数，**不静默归零**。
- 报表同时给出四个数：请求级命中率分布（含 nearest-rank P10/P50/P90）、桶内 token 加权率、member 等权平均、跨成员 token 加权率；session 累计率与最近请求率另列，不并入上述任一。
- 每桶样本门槛 30 请求 / 3 member，未达标记 `sample_gate_reached=false`（`insufficient_sample`）。

### 11.6 归因标签（§7 流程的实现）

`internal/team/cachediagnosis.go` 把 §7 的排查顺序实现为纯函数，输出受控枚举标签，**每条标签都带一行证据数字**，读者可复核而不必采信：

| 标签 | 触发条件（证据） |
|---|---|
| `data_quality_or_semantics` | 该层无任何诊断样本；或该层被排除样本占比 ≥20%（附排除原因明细） |
| `stable_prefix_changed` | stable prefix 变化的样本承担了该层 ≥50% 的 miss token |
| `rewrite_or_compaction_correlated` | 上述变化样本中带 rewrite 原因（`compact_auto`/`snip`/`rewind_truncate`/`guardian_merge`/`prune`）者承担 ≥50% 的 miss token |
| `schema_size_correlated` | 固定前缀样本的 miss 稳定（P90 ≤ 1.5×P10）且中位数落在工具 schema 估算中位数的 **[0.5×, 2×]** 区间内——低于下界说明 schema 大部分已命中，高于上界说明 miss 不是 schema 能解释的 |
| `tail_growth_or_content_correlated` | 报表级：高 prompt 桶的未命中占比 ≥50% 且 ≥1.5× 最低桶 |
| `route_or_interval_correlated` | 报表级：间隔分层的加权率极差 ≥10 个百分点（**只报"与时间关联"**，不主张 TTL） |
| `stable_prefix_high_miss_unattributed` | 固定前缀但 miss 不满足上一条（过高或过低）；明确写明本地 hash 只覆盖 system+tools |
| `insufficient_sample` | 未达 30 请求 / 3 member 门槛（计数照常公布） |

规则边界（有意为之）：

- 只有**低于 low-hit 阈值**（默认 0.90，`--low-hit` 可调）的层才做归因；达标的层只出覆盖率与门槛结论，标签为空。
- `insufficient_sample` 与覆盖率结论在达标层也会输出，因为它们陈述的是样本而非因果。
- **首请求（cold）不参与归因**：它之前没有任何缓存，整段 prompt 的 miss 是预期结果。若把它算进"固定前缀"集合，真正的信号会被一个必然的冷启动淹没。cold 样本单独计数（`cold_samples`），并已由 `first_request` 分层单独报告；标签里的 miss 占比分母是 **warm miss**，证据行也如此措辞。
- **未诊断样本单列**（`undiagnosed_samples`），绝不并入"固定前缀"集合——"未观测"不等于"未变化"。
- `tail_growth_or_content_correlated` 只在**跨桶**比较时给出：单桶内 prompt 规模本就相近，桶内相关性没有信息量。
- 归因口径是纯函数且确定性（同一样本集两次运行结果一致），因此两份报表可直接对比。

### 11.7 复跑方式

```
reasonix team cache-report --json --out report.json        # 聚合报表（可 diff）
reasonix team cache-report --export-requests --out req.jsonl  # 逐请求样本导出
reasonix team cache-report --team T --member M --model REF --from 2026-09-01 --to 2026-09-24
reasonix team cache-report --route anthropic/107c70a37f88   # 只取一个 provider cache scope
```

报表内嵌 `window_*`、`min_*`、`low_hit_threshold`、`bucket_key_field`、`schema_version`、`code_version`/`code_commit` 与 `exclusions`，因此同一输入可复跑得到逐字节相同的输出（有测试固定）。跨模型样本按 `--model` 过滤后再比较，不与不同模型合并分桶。每个分层随率一起公布 `coverage`（received/included/excluded 及各原因）与 `mean_prompt_tokens`，使"某个率覆盖了多少样本"与率本身同时可见。

### 11.8 范围与已知缺口

- 只记录非 leader 的可写 member backend；leader（`memberObservationSink` 对 leader 直接返回原 sink）与普通 CLI session 不进入该数据集，follower 的 sink 带包装但 publisher 未启动，故不写入。
- ambient 窗口的 publisher 只发布快照、不记录逐请求样本：该窗口的 chat 就是 team leader 的 canonical session，按上一条本就在数据集之外。
- `route_bucket` 已由建造期补齐（§11.11）：字段不再是空占位。`route_or_interval_correlated` 因此具备路由输入，但**目前仍由间隔维度触发**——路由维度要触发需要同一 member 跨路由切换（重建/换池）的真实样本，尚未采到。
- 未采集原始 prompt、工具参数/结果正文、路径或凭据；有隐私测试断言落盘内容不含这些值。

### 11.9 本轮验证

- `go build ./...`（根模块与 `desktop` 模块）通过。
- `go test ./internal/team/ ./internal/cli/` 通过；新代码另跑 `-race -count=2` 通过。
- 端到端：真实 `reasonix team cache-report` 二进制在空 state home 与手工 fixture owner 目录下均按预期输出——空态各率打印 `n/a`、fixture 同时给出请求级 62.6%/83.3%/62.5%（分桶）、member 等权 62.6%、session 累计 80.0%、last turn 50.0%，`--export-requests` 输出逐请求 JSONL，`--from/--to` 越界样本计入 `outside_window` 而非静默丢弃。
- repolint：相对 HEAD 的发现集**完全相同**（本改动未新增任何 finding）；HEAD 上已有的 7 处 essay/file-size 违规与本任务无关，按 carry-forward 处理。
- golangci-lint：本改动新增的 3 处（intrange ×2、unused ×1）已修复；余下 2 处（`search_footnotes_test.go` ineffassign、`chat_tui_team_switch_test.go` SA4005）为 HEAD 已有。
- 全量 `internal/cli` 套件曾出现一次 `TestTeamTurnInjectsInboxAtSubmit` 失败，隔离与重复运行（`-count=3`）及随后两次全量运行均通过，判定为并发负载下的偶发，与本改动无关（该用例不构造 publisher）。
- 归因与覆盖率已按 §9.4/§9.5 补齐：`cachediagnosis_test.go` 固定了"达标层不出归因""变化前缀优先于 schema""rewrite 与普通变化分列""schema 尾巴仅在稳定且量级相符时命名、否则标 unattributed""首请求不参与归因""未诊断样本单列""报表级间隔与跨桶增长"以及确定性；`cachereport_test.go` 固定了每桶 `coverage` 与 `code_version`/`code_commit`。会计有效性改由读取方从数值**重算**（不再信任记录里的标志），因此外来或手工编辑的记录无法蒙混进基线。
- 真实样本小基线见 §11.10：`go test -tags live ./internal/cli/ -run TestLiveTeamMemberCacheBaseline -v`（需要 `REASONIX_LIVE_CACHE_BASE_URL`/`REASONIX_LIVE_CACHE_API_KEY`，或该 agent 自身的 `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`）。

代码锚点：`internal/team/cacherequest.go`（记录契约与有界日志）、`internal/team/cachereport.go`（分桶与聚合）、`internal/team/cachediagnosis.go`（§7 归因标签与证据）、`internal/team/ownerusage.go`（快照 additive 字段）、`internal/cli/team_usage_publish.go`（sink 观测与映射）、`internal/cli/team_backend_build.go`（接入点）、`internal/cli/team_cache_report.go`（导出/报表命令）。

### 11.10 真实样本小基线（§10.5 的最小可复现样本）

`internal/cli/live_team_cache_baseline_test.go`（`//go:build live`，不参与 CI）跑通完整生产链路：真实 provider 请求 → controller（盖章 model ref 与 turn 身份）→ member writer 观测 sink → owner 目录有界日志 → 报表。它**只断言本代码拥有的不变量**（每请求一条记录、序号递增、每条都有诊断、账目闭合），不通话命中率——命中率是 provider 行为，不是本代码的契约。

一次真实运行（model `deepseek/deepseek-v4.1-flash`，Anthropic 兼容网关，6 个 turn，系统前缀 140K 字符并带 run nonce 以保证首请求真冷）：

| 分层 | 请求数 | hit | miss | 加权率 | p10/p50/p90 |
|---|---:|---:|---:|---:|---|
| `first_request`（冷） | 1 | 0 | 31,402 | 0.0% | — |
| `warm_candidate` | 5 | 156,288 | 1,052 | **99.3%** | 99.2%/99.3%/99.5% |
| 桶 `lt_32k`（全体） | 6 | 156,288 | 32,454 | 82.8% | 0.0%/99.3%/99.5% |
| session 累计 | — | 156,288 | 32,454 | 82.8% | — |

这组数字正是把四个口径分开的理由：**同一个 session，warm 请求 99.3%，session 累计只有 82.8%**，差 16.5 个百分点全部来自那一次冷启动。任何"该成员命中率 82.8%"式的单一数字都会误导；而"1M 上下文命中率低"这类结论同样必须按 warm/冷/桶拆开才成立。

本次运行同时验证/暴露了两点：

1. **归因口径的缺陷已被真实数据暴露并修复**：修复前，那次冷启动的 miss 使整体层得到 `stable_prefix_high_miss_unattributed`——把一个必然的冷启动误报成"不可归因的尾部问题"。现已改为首请求不参与归因（见 §11.6），修复后证据行如实写为"5 个 warm 样本，miss 中位数 214（P10 152 / P90 258），低于 schema 575 tok"。
2. **缓存 scope 确实跨进程保留**：另一次运行（前缀字节相同、无 nonce）的首请求 hit=31,232，即上一进程写入的 provider 缓存被复用。这说明 `stable_prefix_hash` 所描述的前缀稳定性是有实际意义的，但也说明"冷启动"必须由内容唯一性来构造，不能靠换进程。

覆盖率与门槛如实标注：6 个请求、1 个 member，**全部低于 30/3 门槛**，报告因此只给 `insufficient_sample`，不作跨成员推断——这与 §6 的样本门槛一致，不因为这是"自己的测试"就放宽。本次样本只落在 `lt_32k` 一个桶，因此 §7.6/§7.7 的跨桶与间隔比较（`tail_growth_or_content_correlated`、`route_or_interval_correlated`）在真实样本上尚未触发，只有单测覆盖。

### 11.11 路由字段补上游（§9.1/§9.6/§9.7 的路由维度）

上一版 `route_bucket` 是一个只声明、无写入方的字段。现补上游来源：**成员 backend 建造期解析出的 provider 身份**。

- `memberProviderResolver.RouteBucket()`（`internal/cli/team_backend_build.go`）把 `kind`（线协议）、`endpoint`、pool entry id、代理 mode/type 拼在一起取 SHA-256 前 6 字节，产出 `kind/hex12`，例如 `anthropic/107c70a37f88`。
- **凭据与 endpoint 都不进字段**：只有指纹进记录。代理只贡献 mode/type——**口令的指纹仍是口令的派生值**，所以 `Username`/`Password` 被排除，有测试断言这一点。
- 同一路由稳定，换 endpoint / 换池条目 / 挂代理都会换 bucket；轮换 API key 不换 bucket（凭据不是路由身份的一部分）。这四条都有测试。
- 消费侧同步落地：报表新增 `route_buckets`（列出样本触达的全部 cache scope）与 `--route` 过滤。两个路由的样本**不会**被平均成一个率——有测试固定"不过滤时 0.5、过滤后 0.9"。

边界：同一 endpoint 背后若有两个账号池条目会被分到不同 bucket（因为 pool entry id 入哈希），但两个账号若共用一个 pool entry 则无法区分——这是有意为之，凭据不入哈希。

### 11.12 真实团队运行（§10.5 的团队样本）

`TestLiveTeamMemberCacheBaselineTeam` 建了一个**真实团队**并按生产路径跑通：注册表 + owner store + pool 条目 → `newMemberBackendBuilder` 逐个组装三个非 leader 成员 → 每个成员 `RunTurn` 十个 turn → 用 `collectCacheSamples`（报表命令自己的读取路径）取回记录与会话账本。

members：`cache-small` / `cache-mid` / `cache-large`，各自的开场 brief 为 48KB / 240KB / 600KB，使请求落在不同 prompt 桶。模型 `deepseek/deepseek-v4.1-flash`（经 `[1m]` 别名交给团队解析层剥离），同一 pool 条目。

一次真实运行（30 个请求、3 个成员）：

| 分层 | 请求 | members | hit | miss | 加权率 | p10/p50/p90 |
|---|---:|---:|---:|---:|---:|---|
| 总体（**过门槛**） | 30 | 3 | 1,783,936 | 179,851 | 90.8% | 26.6%/100%/100% |
| `first_request`（冷） | 3 | 3 | 8,448 | 179,851 | 4.5% | 2.2%/5.5%/26.6% |
| `warm_candidate` | 27 | 3 | 1,775,488 | **0** | **100.0%** | 100%/100%/100% |
| 桶 `lt_32k` | 10 | 1 | 124,160 | 7,770 | 94.1% | 26.6%/100%/100% |
| 桶 `32k_128k` | 20 | 2 | 1,659,776 | 172,081 | 90.6% | 5.5%/100%/100% |
| session 累计 | — | 3 | 1,783,936 | 179,851 | 90.8% | — |
| last turn 跨成员 | — | 3 | — | — | 100.0% | — |

这次运行给出四件此前只有单测证据的事：

1. **成员等权平均与 token 加权是两个不同的数**：91.9% vs 90.8%。规模不同的成员在这个口径下不再互相淹没。
2. **冷/热分离在真实数据上成立**：27 个 warm 请求 miss 为 **0**，而冷启动的 3 个请求承担了全部 179,851 miss。总体 90.8% 完全由冷启动拉低——任何"该团队命中率 90.8%"的说法都必须附带这个分解。
3. **成员/门槛/覆盖率全部真实过闸**：30 请求 / 3 成员，`sample_gate_reached=true`，报告不再只是 `insufficient_sample`。
4. **`route_bucket` 在生产路径上有值**：全部 30 条记录都是 `anthropic/107c70a37f88`（同一 pool 条目），与"同路由应同 bucket"的契约一致。

仍未覆盖（如实标注）：

- **样本只到 `32k_128k`**。600KB 的 brief 经真实 tokenizer 落在 111K token 左右（重复文本的 token/字符比高于 4:1），因此 §6 要求的 512K–1M 桶仍无真实样本，"大上下文命中率分布"这道阶段门仍未通过。要覆盖需要 MB 级 brief。
- **`tail_growth_or_content_correlated` 与 `route_or_interval_correlated` 仍未在真实样本上触发**：本次所有 warm 请求 miss=0，跨桶增长没有信号；同一成员也未发生路由切换。两者仍只有单测证据。
- **`cache_write_tokens` 全为 0**：`CacheWriteTokens` 来自响应里的 `cache_creation_input_tokens`，该网关在本轮所有请求上都没有给出该字段（单成员探针的逐请求日志同样每条 `write=0`）。记录如实写 0，报告不据此推断 cache 创建行为——"provider 未报告"不是"没有创建"。
