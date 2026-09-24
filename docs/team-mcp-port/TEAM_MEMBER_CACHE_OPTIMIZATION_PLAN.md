# Team 成员缓存命中率优化总计划

> 状态：并行拆分协调入口。日期：2026-09-24。
> 范围：Team session 中 member 的 prompt cache 命中率；不把 leader 与普通 CLI session 混入成员均值。
> 原则：先确认低命中发生在哪些请求、由什么前缀变化导致，再针对证据优化；不以压短 system prompt 或无差别压缩历史作为默认解法。

## 1. 结论摘要

当前最值得优先投入的方向不是继续猜测并缩短工具描述，而是**把成员级逐请求缓存诊断补齐，并将命中率按 prompt 大小和会话阶段拆开**。现有 Team usage 只有最近一次请求的 prompt/hit/miss 与 session 累计 hit/miss，没有持久化逐请求的 cache prefix 诊断；仅凭 session 平均值无法判定约 1M 上下文的某次请求为何低命中。

本机 `cpp_ipc_team` 快照（2026-09-23 发布的 owner usage）给出以下参照。这里只统计四个 member，不含 `codex-leader`：

| 成员 | 累计会话命中率 | 最近请求命中率 | `context_used` | 最近 `prompt_tokens` |
|---|---:|---:|---:|---:|
| `ipc-protocol` | 58.2% | 86.7% | 1,264,632 | 539,611 |
| `ipc-review-security` | 67.8% | 83.8% | 929,205 | 281,178 |
| `ipc-test-perf` | 68.3% | 61.5% | 137,897 | 128,028 |
| `ipc-transport` | 79.4% | 83.2% | 65,736 | 65,876 |
| 简单成员平均 | **68.4%** | **78.8%** | — | — |
| 按累计输入 token 加权 | **70.1%** | — | — | — |

这些数字是单个 owner 快照，不是全体 Team 的代表性实验。它们揭示两点：

1. `ipc-protocol` 的 context gauge 为 1,264,632，但最后一次 provider prompt 是 539,611；“到 1M 命中率不到 60%”不能直接由该成员的 session 累计率推出。需要先澄清 `context_used` 的定义、统计时点和压缩行为，再以真实 `prompt_tokens` 定义 1M 桶。
2. session 累计率是 `Σhit / Σ(hit+miss)`，与最近请求率不是同一个指标。成员之间的会话长度和 token 量差异也会改变加权结果。

因此当前证据**不能证明**命中率随 prompt 增大必然下降，也不能证明 schema 是主要剩余原因。

## 2. 已有机制与边界

- Team member 的 provider-visible 工具表已在 `internal/boot/tool_surface.go` 经过 `dropMemberDeferredTools` 过滤；延期工具仍可经 `use_capability` 调用。故 `docs/team-mcp-port/MEMBER_CACHE_TAIL_ROUTE.md` 中“方案，未实施”的状态与当前代码不一致。先核对生效构建的工具表和 `ToolSchemaTokens`，不要重复实现同一过滤。
- Agent 已生成 `CacheDiagnostics`，包括稳定前缀 hash、工具 schema token 估算、cache hit/miss 和 prefix-change reasons；诊断随 usage event 发出。Team owner 发布目前只持久化简化的最近 usage 与 session totals，未包含这些诊断字段。锚点：`internal/agent/cache_shape.go`、`internal/agent/run_usage.go`、`internal/cli/team_usage_publish.go`、`internal/team/ownerusage.go`。
- 成员 session 累计 hit/miss 由每个 provider usage chunk 累加，Team avg 显示为 token 加权率。锚点：`internal/agent/agent.go`、`internal/cli/chat_tui_status.go`。
- 1M 路线已经讨论 fold 对齐、schema 剪枝与 warm-cache compaction，但其中每项都应以逐请求证据验证；不要因为文档里有设计就假设对应开关已启用或能解释当前数据。参考 `docs/team-mcp-port/1M_CONTEXT_P2_P3_DESIGN.md`。

## 3. 根因假设与判别方式

| 优先级 | 假设 | 应观察的证据 | 对应方向 |
|---|---|---|---|
| P0 | 统计口径混用：session avg、last-turn rate、context gauge 被当成同一指标 | 同一请求的 `prompt_tokens` 与 `context_used` 差异；按 prompt 桶统计后趋势消失 | 明确 UI/报告口径；以 prompt tokens 分桶，不用 context gauge 代替请求长度 |
| P1 | provider 请求前缀被重置或改变 | 低命中与 `PrefixChangeReasons`、`StablePrefixHash` 变化同时出现 | 按具体 reason 查 system/tools/context 重建、log rewrite、模型切换、session rebind；修除非预期变化 |
| P1 | 大 prompt 下存在固定或增长的 cache-miss 尾部 | 同模型连续请求稳定前缀下，miss 的绝对 token 数与 schema/新增尾部对应；命中率随 prompt 上升而改善或持续偏低 | 先分离工具 schema、消息新增量、provider framing；只压缩确认为每请求重付的部分 |
| P1 | compaction/fold 或上下文恢复产生冷请求 | 冷请求集中在 `compact_auto`、projection/rewrite 边界或恢复后的首请求 | 检查 cache-aligned fold 契约、summary 的插入位置、是否整段前缀被改写；调整必须在 fold commit 边界进行 |
| P2 | provider cache TTL、路由或账号池造成跨请求不复用 | 相同成员与稳定 hash，短间隔请求暖、长间隔/换 endpoint 后冷；按 modelRef/account/endpoint 分组差异明显 | 保持成员连续请求走相同 cache scope；验证 provider cache 生命周期与实际路由；避免跨成员强行共享 cache |
| P2 | 真实 prompt 膨胀、而非缓存机制回归 | prompt token 增长与长工具结果、代码/日志回显、重复消息一致；miss 与新增内容量匹配 | 控制工具输出与重复内容、按需读取；不要重写已缓存历史来追求表面命中率 |

## 4. 并行拆分与交付边界

两个 Agent 可以并行推进，写入文件彼此隔离。Part A 是 Part B 的测量依赖，但不阻塞其静态分析与基准设计；需要真实逐请求诊断结果才能执行的行为改动，必须等 Part A 数据交付后再开始。

| Part | 交付文件 | 独占范围 | 交付物 |
|---|---|---|---|
| A：可观测性与基线 | `docs/team-mcp-port/TEAM_MEMBER_CACHE_OBSERVABILITY_PLAN.md` | Team usage/stat 逐请求诊断、统计口径、prompt 分桶、数据保留/脱敏、归因与报告 | 字段/API 数据流、实现步骤、兼容策略、测试和验收；给出按 prompt bucket × prefix reason × model/route 的基线报告契约 |
| B：行为优化与基准 | `docs/team-mcp-port/TEAM_MEMBER_CACHE_BEHAVIOR_PLAN.md` | stable prefix、工具 schema、消息增长、compaction、provider cache scope 的实验及修复方案 | 按证据分流的实施顺序、实验矩阵、灰度/回滚、回归测试与 1M 验收门槛；明确静态准备与真实诊断依赖 |

### 并行约束

1. 两个 Agent 只修改各自交付文件；本总计划作为接口与范围契约，不由子任务改写。
2. A 不实现 prompt/工具/compaction 行为改动；B 不重复设计或实现 telemetry 存储管线。
3. B 可先审查和准备受控实验，但凡是根因依赖逐请求数据的生产优化，都必须引用 A 的报表结果，不得先验归因为 schema 或 compaction。
4. 交接时 A 提供字段定义、样本量与查询维度；B 指明每项实验消费哪些字段/分桶，并对照同一口径报告前后结果。
5. 合流顺序：A 的字段契约/诊断样例先冻结 → B 完成基线对照设计 → 接入 telemetry → 先跑只读基线 → 再选一项行为优化灰度。若基线样本不足，先补采样，不降低证据门槛。

## 5. 现有证据与共同红线

以下结论两个 Part 共用，禁止分别重新定义：

- 本机 `cpp_ipc_team` 快照中四个 member 的累计会话命中率简单平均约 **68.4%**、按 token 加权约 **70.1%**；快照不是全量代表性实验。
- `context_used`、最近请求 `prompt_tokens`、session 累计命中率必须分开报告。真实 prompt 的 1M 桶只由 provider `prompt_tokens` 定义。
- 成员 provider-visible 工具表已在 `internal/boot/tool_surface.go` 执行 `memberDeferredTools` 过滤；不能把“成员工具延期过滤”当作待做功能重复实现。仍需确认真实构建/请求的 schema token 与 tools hash。
- Agent 已计算 `CacheDiagnostics`；缺口是 Team member 持久化/聚合可观测性，不是从零发明诊断字段。
- 不改变 cache-stable system/tool 前缀、不在 turn 中动态重排 schema、不重写已缓存历史、不跨成员共享 provider cache。输入硬上限优先于命中率。
- 目标指标要分别标出冷启动、稳定前缀 warm 请求、compaction/fold 后请求、route/model；样本不足或 provider 未报告 cache split 时明确标“不确定/不适用”。

## 6. 后续阶段（两份子计划合流后）

### 阶段 0：冻结基线与厘清指标（只读分析）

1. 从 owner usage、provider usage event 和可用的 session event 中导出**每请求**样本；字段至少包括时间、team/member、modelRef/provider route（允许脱敏或分桶）、`prompt_tokens`、`cache_hit_tokens`、`cache_miss_tokens`、`context_used`、`context_window`、请求序号、距离上一请求的时间。
2. 用 `prompt_tokens` 而非 `context_used` 分桶：`<32K`、`32–128K`、`128–256K`、`256–512K`、`512–768K`、`768K–1M`、`>1M`。窗口应按实际 prompt 命名；超窗数据单独审查，不悄悄归入 1M。
3. 每个桶同时报告请求数、成员数、模型/路由、token 加权命中率、请求率中位数/P10/P90、miss token 中位数/P90；另外报告首请求、稳定前缀 warm 请求、compaction 后首请求。不得只给总体平均。
4. 检查历史事件格式是否保留 cache diagnostics。缺失时将样本标为“无法归因”，不要从 `prefixHash` 或 session totals 猜测原因。

**阶段门**：能回答“真实 prompt 达到 512K–1M 的样本有多少、这些请求的 miss 来自何种类别”，否则不进入行为优化。

### 阶段 1：补齐 Team 逐请求可观测性（Part A）

1. 扩展 Team usage 发布/持久化路径，让最近请求诊断随 `LastTurn` 一并发布：`PrefixChangeReasons`、`StablePrefixHash`（仅短 hash）、`ToolSchemaTokens`、`CacheHitTokens`、`CacheMissTokens`；补请求时间和 prompt token。保留 session totals 作为另一个独立指标。
2. 先保存最近 N 条 usage 的有界环形历史或写入本地 stats，再由报表离线聚合。当前单条 `.usage.json` 全量替换适合状态快照，不适合存无限逐请求历史。
3. 对 stats 增加可过滤的 team/member 维度或安全的 session correlation ID，使成员请求能与 model、route、prefix-change reason 关联。不得记录原始 prompt、工具参数、deliverable 正文或敏感路径。
4. 处理未知 usage、重试与多采样：标记 `Unknown` / request count；明确统计单位是 provider request，不把一个 turn 当成一个请求。避免重试聚合导致的 prompt/hit 双计或误判。
5. 验证 `context_used` 在 compaction 前后、投影恢复、消息追加时的定义；UI 分开展示“当前上下文估算”“最近请求 prompt”和“会话累计缓存率”。

**代码候选**：`internal/team/ownerusage.go`、`internal/cli/team_usage_publish.go`、`internal/cli/team_follower_usage.go`、`internal/stats/record.go`、`internal/stats/recorder.go`。字段演进保持向后兼容、缺字段可读。

### 阶段 2：根据归因结果定向优化（Part B）

按下列决策顺序执行，一次只验证一类变化：

1. **命中低且 stable prefix hash/reason 变化**：先定位非预期变化的生产调用点。优先保证 member backend、模型/路由和 provider-visible tools 在连续请求间稳定；区分应有的 session/context tail 更新与会破坏共享前缀的 system/tools/rewrite 变化。任何 prompt identity 更新、tool surface 变更应只在新会话或显式兼容边界发生。
2. **前缀稳定但 miss 有固定 schema 尾巴**：读取实际 `ToolSchemaTokens` 和成员生效的 provider schema。成员延期过滤已存在；若工具面仍过大，只在 member role 对低频工具 schema 做有界剪枝/描述缩短，继续保留 `use_capability` 可达性。不能在思考循环里每请求动态变更 schema，否则 `ToolsHash` 翻转本身会冷缓存。
3. **miss 与工具结果/消息新增量高度相关**：限制重复状态、冗长日志和大文件输出进入 transcript；支持摘要/分段读取，但不得为了减上下文而把已缓存的前缀中间段重写。将内容压缩放到明确的 cache-aligned fold commit。
4. **低命中集中在 compaction/fold 后**：对比 fold 前后 system/tools hash 与消息前缀，确认是否只追加摘要且保留 stable prefix。若是预期冷轮，量化冷轮 token 成本，再评估 warm-cache 延迟压缩或分段 fold；硬输入上限始终优先于命中率。
5. **stable prefix、工具面、内容形状都稳定但仍低命中**：用同 endpoint、同模型、同账号池的短间隔与长间隔对照，检查 cache TTL/缓存粒度和路由亲和性。provider 行为需经真实请求验证，不把本地 hash 等同于 provider cache key。

### 阶段 3：可复现基准与灰度（A+B 联合验收）

1. 构造同一 Team member、同一模型/route 的连续请求阶梯：32K、64K、128K、256K、512K、768K、接近 1M。每个阶梯至少包含一次冷启动和多次稳定前缀 warm 请求；单独记录 compaction 前后。
2. 做对照组：当前工具表 vs 已确认有效的缩减表；稳定 session vs 强制触发已知前缀变化；短间隔 vs 超过疑似 cache TTL 间隔。每次只改变一个变量。
3. 先对单一 member role 做灰度，确认 schema 工具仍可通过 `use_capability` 执行、leader 和普通 session surface 未变化、token 账目闭合。
4. 目标作为**项目验收门槛而非 provider 保证**：稳定前缀的 warm 请求，在 `prompt_tokens >= 128K` 的每个有足够样本桶中，加权命中率目标 ≥90%；各桶同时公布 P10/P50/P90，任何改善不得以 hard input ceiling 违规或工具不可达为代价。首次请求、provider 未报告 cache split、TTL 冷请求分别报告，不并入 warm 指标。
5. 如模型/路由不支持缓存或样本不足，验收结论标记“不确定/不适用”，不以低样本百分比判成功或失败。

## 7. 风险、明确不做与回滚

- 不以“1M 时 60%”作为已确认事实或单一 KPI；先确认真实 prompt 分布及样本数。
- 不再重复实现成员工具延期过滤；先核对 `ToolsHash`、schema token 估算与实际 provider request。
- 不缩短 cache-stable system prompt、成员 identity 或已命中的历史来追求 cache hit；改这些内容可能让整个后缀冷掉。
- 不在 turn 中途重排工具或动态改变 provider tool schema；需要 schema 收缩时放到新 session/fold 对齐边界。
- 不以跨成员共享 provider cache 为优化目标；成员身份、权限与独立上下文必须隔离。
- 数据采集默认脱敏、有限保留；不落原始 prompt、工具调用正文或秘密值。
- 新遥测字段为 additive；异常/缺失只影响诊断，不阻断成员任务。行为优化按功能开关灰度，保留旧路径回滚。

## 8. 完成定义

- 能从同一份可复现报表区分最近请求率、会话累计率、成员简单平均和 token 加权率。
- 能按真实 `prompt_tokens` 证明大上下文命中率分布，并区分冷启动、warm 请求和 fold 后请求。
- 对低命中样本能归因为稳定尾巴、前缀变化、compaction、内容增长、provider cache scope 或数据不足之一；未知原因必须保留为未知。
- 每项优化都有前后对照、样本量、工具可达性验证及不回退 leader/普通 session 的回归测试。

## 9. 相关文档

- `docs/team-mcp-port/MEMBER_CACHE_TAIL_ROUTE.md`：成员工具尾部优化历史方案；其实施状态需按当前代码更新。
- `docs/team-mcp-port/1M_CONTEXT_P2_P3_DESIGN.md`：大上下文与 cache-aligned compaction/schema pruning 设计边界。
- `docs/team-mcp-port/MEMBER_USAGE_PUBLICATION_CONTRACT.md`：成员 usage 的发布契约。
- `internal/agent/cache_shape.go`：缓存形状诊断。
- `internal/cli/team_usage_publish.go`：Team member usage 发布。
