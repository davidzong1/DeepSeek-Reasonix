# Team member 缓存观测：Part A 统计契约与字段核验（v1）

> 状态：**已冻结（v1）**，日期 2026-09-24。所有者：Agent A。
> 执行方案：`TEAM_MEMBER_CACHE_OPTIMIZATION_EXECUTION_PLAN.zh-CN.md` §2 Agent A / §8「派给 Agent A」。
> 前置：`TEAM_MEMBER_CACHE_DATA_AUDIT.md`（本文修订其 §1.3/§2.2 的口径，见 §6）。
> 本文只定义**统计与观测**契约；不改 provider-visible 请求字节、缓存策略、上下文裁剪或成员隔离。

## 0. 结论摘要

| # | 结论 | 强度 |
|---|---|---|
| C-1 | `request_count` 的「0 视为 1」兼容规则**无法区分「观测到 1 个请求」与「无人观测」**。现已补 `RequestCountObserved` / `request_count_source`，从 provider attempt 计数一路带到账本行。 | 证据（§2.2、§4.2） |
| C-2 | 主基线现在只收**计数被观测且等于 1** 的样本。默认计数与未记录来源的样本**排除并单列**，token 单独入账（`unverified_hit_tokens` / `unverified_miss_tokens`）。 | 证据（§3.2、§4.3） |
| C-3 | 因此**历史账本的 per-request 基线不可用**：三天全部 1,714/1,714 行的 `requests` 键都由旧构建写入、无 provenance 标记。旧数字 85.1% / 68.4% 现在只能以「全样本加权率」形式复现：**84.5% / 67.7%（−16.8pp）**。 | 证据（§5.1） |
| C-4 | 新构建的真实成员会话与真实 Team 会话**每条记录都是 `observed`**（12/12、30/30），所以成员级基线不受该规则影响。 | 证据（§5.2） |
| C-5 | 报表新增 `coverage` 字段（计数来源、route/model/usage_source/前缀诊断的字段覆盖率），与每个 rate 同读；`all_samples` 行给出可对账的全样本口径。 | 证据（§3.3、§4.4） |
| C-6 | 每个 stratum 新增 `hit_tokens_per_request` / `miss_tokens_per_request`。命中率单独出现时无法区分「未命中工作量变小」与「prompt 变大、固定未命中开销不变」，Part B 的真实实验已量到后者的量级（prompt ×22，miss/请求 232→197）。 | 证据（§3.1、§8.1） |
| C-7 | 该 provenance **只能表达「usage 是否带了计数」**，不能区分「上游 merge 造出的 1」。这一限制写在 §7，不能当成已验证。 | 限制（§7 L-1） |

## 1. 范围与观测单位

### 1.1 三个数据集，永不相加

| 数据集 | 来源 | `source` 标签 | 能回答 |
|---|---|---|---|
| 成员记录 | `<owner dir>/.cache_requests.jsonl`（每个成员自己的目录） | `member records (owner writer)` | 成员级、含前缀诊断与请求形状 |
| 历史账本 | `config.StatsDir()/<day>.jsonl` | `stats-ledger (route-level, no prefix diagnosis)` | 路由级、无成员身份、无诊断 |
| 会话累计 | `<owner dir>/.usage.json` 的 `session_cache_hit/miss` | —（`sessions` 段） | 整个会话的 token 如何被服务，**不是** per-request 指标 |

报表在 `source` 里自报数据集；`team cache-report` 与 `team cache-audit` 的输出**不可相减、不可互为基线**。

### 1.2 身份层级（哪些可用、哪些不可用）

| 层级 | 字段 | 语义 | 可用于会话级分析？ |
|---|---|---|---|
| Team member | `team_id` / `member_id` | owner writer 的目录身份 | ✅（仅成员记录） |
| 逻辑 turn | `turn_id` + `session_sequence` | 发出 usage 事件的 turn 身份；事件没带就是空，**不补造** | ✅（仅作关联） |
| 写入者会话内的请求序号 | `session_request_seq` | 该 writer 进程观察到的第 N 次请求 | ✅（仅用于间隔/阶段分层） |
| Provider request | `request_count` + `request_count_source` | 该 usage 代表几个 provider 请求，以及这个数是否被观测 | ✅（**仅当 `observed`**） |
| Provider attempt | 无独立字段 | 只有聚合计数，**无法还原为逐 attempt** | ❌ |
| Provider cache key | 无 | 本地 `prefix_hash` 覆盖 system+tools，**不是** provider cache key | ❌ |

**账本相邻行不得当作同一 session**：账本无 session 身份，聚合行也不能还原 attempts。

## 2. 字段实现核验清单

### 2.1 三段数据流（混用即错）

| 层 | 位置 | 字段 | 语义 |
|---|---|---|---|
| 上游 wire | DeepSeek 网关 `/v1/messages` | `input_tokens`、`cache_read_input_tokens`、`cache_creation_input_tokens` | 口径**不携带判别位**：inclusive 与 exclusive 的同一字段名含义相反 |
| 适配归一化 | `internal/provider/anthropic/messages_usage.go` | `messagesUsage(..., inclusive)` | `inclusive` 由 `client.inclusiveInput()`（= 该 client 是否 `deepseek`）决定；两种口径都保证 `prompt == hit + miss` |
| 采样合并 | `internal/agent/run_usage.go` | `mergeSamplingUsage` / `finalizeSamplingUsage` | 多 attempt 累加；`PromptTokens` 被重写为 `hit+miss`；`ContextPromptTokens` 只取最后一次 attempt |
| 账本落盘 | `internal/stats/recorder.go` | `prompt/cache_hit/cache_miss/completion/requests/requests_observed` | 归一化后的值；**不含**原始 wire 字段、不含 team/member、不含前缀诊断 |

### 2.2 请求计数 provenance（本次新增）

| 层 | 字段 | 写入点 | 规则 |
|---|---|---|---|
| provider | `provider.Usage.RequestCountObserved` | `ApplyRequestAttemptCount`（`internal/provider/retry.go`）、`UsageWithRequestAttemptCount`、`runSamplingAttempt`（agent）、`auxiliary_recovery` | 计数来自 ctx 上的 HTTP attempt 计数器且 >0 时为 `true` |
| provider 合并 | 同上 | `openai.mergeUsage`、`agent.mergeSamplingUsage` | **AND 语义**：任一部分未被观测，合计就是下界 |
| 成员记录 | `request_count_source` | `memberCacheRequest`（`internal/cli/team_usage_publish.go`） | `team.RequestCountSourceOf(count, observed)` |
| 快照文档 | `request_count_source` | `ownerUsageLastTurn` | 同上（`providerUsageFromLastTurn` 不回填，方向单向） |
| 账本行 | `requests_observed` | `Recorder.recordProviderUsage` | 直接抄 `usage.RequestCountObserved`；`omitempty`，旧行缺键 |

**取值词汇（闭集）**：

| 值 | 含义 | 可否进主基线 |
|---|---|---|
| `observed` | usage 带了 producer 实际观测到的计数 | ✅（且 `count == 1`） |
| `defaulted` | usage 没带计数，写入者存了兼容默认值 1 | ❌ |
| `unrecorded` | 来源没有记录任何 provenance：本次新增字段之前写的文档，或外来行 | ❌ |
| 其他值 | 闭集外的值（producer 长了新值而 reader 未跟进） | ❌，并在 `coverage.request_count_unrecognized` 单列 |

### 2.3 其余字段：来源与可比较性

| 字段 | 来源 | 原始 provider 值？ | 可否跨记录比较 |
|---|---|---|---|
| `prompt_tokens` | 归一化后 | ❌（由 adapter 折算） | ✅ 同一 route 内 |
| `cache_hit_tokens` / `cache_miss_tokens` | 归一化后 | ❌ | ✅（`cache_write` 是 miss 的**子集**，不是加数） |
| `context_prompt_tokens` | 最后一次 attempt 自身 prompt | ❌ | ✅（桶键；多 request 行没有该形状，故为空） |
| `usage_unknown` / `usage_estimated` | `provider.Usage.Unknown` / `Estimated` | ✅ 标志位 | ✅ |
| `usage_source` | `event.Event.UsageSource` | ✅ | ✅（`executor` / `planner` / `compaction` / …） |
| `route_bucket` | builder 在建后端时固定的路由标签 | ✅ | ✅（**两条 route 永不共享 provider cache**） |
| `prefix_hash` / `stable_prefix_hash` | agent 的 shape 比较 | 本地计算 | ⚠️ 只作**客户端差分**，不是 provider cache key |
| `accounting_valid` | 由数值重算（`Accounting()`） | 本地判定 | ✅ |
| `hit == 0` | — | — | ⚠️ 统一表述为「**未报告 cache read**」，不是「冷启动」 |

## 3. 冻结的统计契约（v1）

### 3.1 主指标

```
主指标 = Σcache_hit_tokens / (Σcache_hit_tokens + Σcache_miss_tokens)
        只对「有效单请求样本」求和
```

伴随指标（必须与主指标同报）：有效样本覆盖率、`hit/miss tokens` 总量、**`hit_tokens_per_request` 与
`miss_tokens_per_request`**、请求率 p10/p50/p90、`mean_prompt_tokens`、成员等权平均、会话累计率（另一指标）。

**为什么 miss/请求必须与命中率并列**：一个命中率单独出现时，无法区分「未命中的工作量变少了」与
「prompt 变大了、而未命中的固定开销没变」——后者的命中率会自然上升而没有任何请求变便宜。
Part B 的真实 Provider 实验直接量到了这一组成效应：prompt 3,048 → 67,269 tokens 时加权率
92.39% → 99.71%，而 miss/请求几乎不变（232 → 197）。因此**任何候选改动只能按 miss/请求这一列判定**，
不得按命中率百分点判定（见 §8 对 B 的回应）。

**四个数字不可互相替代**：请求级加权率、成员等权平均、请求率中位数、会话累计率。报表同时给出。

### 3.2 有效单请求样本（全部条件同时成立）

1. `request_count_source == "observed"` 且 `request_count == 1`；
2. 非 `usage_unknown`、非 `usage_estimated`；
3. `accounting_valid`（非负且 `hit + miss <= prompt`）；
4. `hit + miss > 0`（有 cache split）；
5. `observed_at` 可解析（RFC3339Nano）。

### 3.3 排除分类（各自独立计数，重叠不合并）

`exclusions` 与每个 stratum 的 `coverage.reasons` 都逐类计数，**不静默并入、不修正**：

| 分类 | 字段 | token 是否单独入账 |
|---|---|---|
| 窗口外 | `outside_window` | ❌（不属于本次读入） |
| 非成员范围 | `non_member_scope` | ❌ |
| unknown usage | `unknown_usage` | ❌ |
| estimated usage | `estimated_usage` | ❌ |
| 多请求聚合 | `aggregate_requests` | ✅ `aggregate_hit_tokens` / `aggregate_miss_tokens` |
| 计数未验证 | `unverified_request_count` | ✅ `unverified_hit_tokens` / `unverified_miss_tokens` |
| 账目无效 | `accounting_invalid` | ❌ |
| 无 cache split | `no_cache_split` | ❌ |
| 时间戳不可解析 | `unparsable_observed_at` | ❌ |

**token 归属是互斥的**：一个样本的 token 只进一类，聚合优先于未验证计数（`bookExcludedTokens`）。因此

```
all_samples_hit  = overall.hit  + aggregate_hit_tokens  + unverified_hit_tokens
all_samples_miss = overall.miss + aggregate_miss_tokens + unverified_miss_tokens
```

是**可对账**的，`CacheReport.AllSamplesTotals()` 就是它。全样本率**只作为全样本口径**给出，**不得**当作 per-request 率引用。

### 3.4 字段覆盖率（`coverage`）

对**成员范围内的样本**（进入窗口且 team/member 非空）逐字段计数，包含被排除的样本：

- `request_count_observed/defaulted/unrecorded/unrecognized`
- `usage_source_present/absent`、`route_bucket_present/absent`、`model_ref_present/absent`、`diagnostics_present/absent`

任何 rate 必须与 `coverage` 同读。覆盖率是「缺席的披露」，**永不**是「缺席的修正」：没有 route 的样本不会被分配一个 route。

### 3.5 分层维度

member、`provider/model/route`、`context_prompt_tokens` 桶（`<32k`、`32k–128k`、`128k–256k`、`256k–512k`、`512k–768k`、`768k–1m`、`>=1m`；`context_used` **只作旁证**）、请求阶段（`first_request` / `warm_candidate` / `post_rewrite` / `unknown_stage`，**标签可重叠**）、距上一请求间隔、`request_count` 及其来源、usage 精确/估算/未知、prefix diagnostics。

样本门槛：每桶 ≥30 请求且 ≥3 成员才允许跨成员推断；不足则标 `insufficient_sample`（原始计数照常发布）。低命中阈值 0.90 是**诊断触发**，不是目标。

### 3.6 低命中样本不得过滤

低命中样本**不因结果不利而被排除或重分类**。唯一离开主基线的路径是 §3.3 的具名排除。`TestCacheReportKeepsLowHitSamplesInTheBaseline` 固定这一点。

## 4. 实现与验收对照

### 4.1 交付物 1–3 的落地位置

| 交付物 | 位置 |
|---|---|
| 字段数据流表 | §2（本文） |
| 冻结契约 | §3（本文） |
| 记录模型与 provenance 词汇 | `internal/team/cacherequest.go` |
| 聚合、排除、覆盖率、全样本口径 | `internal/team/cachereport.go`、`internal/team/cachereport_stats.go` |
| 诊断（不改分母） | `internal/team/cachediagnosis.go` |
| 写入者映射 | `internal/cli/team_usage_publish.go` |
| 报表 / 审计命令 | `internal/cli/team_cache_report.go`、`internal/cli/team_cache_audit.go` |
| 账本 provenance | `internal/stats/record.go`、`internal/stats/recorder.go` |

### 4.2 测试清单（交付物 3）

| 要求 | 测试 |
|---|---|
| 计数 provenance 闭集与 AND 语义 | `TestCacheReportCountProvenanceVocabularyIsClosed`、`TestMergeSamplingUsageMarksAnAssumedCount`、`TestRequestCountProvenanceTracksTheCounter` |
| 单请求资格可审计 | `TestCacheReportRequiresAMeasuredRequestCount` |
| miss/请求与命中率并列（组成效应） | `TestCacheReportPublishesMissTokensPerRequest`、`TestCacheReportPerRequestColumnsAreEncoded` |
| 聚合识别 | `TestMultiAttemptUsageIsStoredAsAnAggregate`、`TestCacheReportDisclosesExclusionsWithoutCorrecting` |
| 缺字段显示未知而非推断 | `TestCacheReportCoverageDescribesTheScopedPopulation` |
| 低命中不过滤 | `TestCacheReportKeepsLowHitSamplesInTheBaseline` |
| 成员隔离（leader 0 条） | `TestObservationSinkForALeaderIsTheGivenSink`、live `TestLiveTeamMemberCacheSession` |
| follower 不写 | `TestFollowerRecordsNothing` |
| 日志失败不阻断成员 | `TestRequestLogFailureDoesNotReachTheMember` |
| 敏感正文不落盘 | `TestObservedRequestsCarryNoContent` |
| 账本 provenance 与单列 | `TestCacheAuditExcludesRowsWithAnUnverifiedRequestCount` |
| 账本聚合单独入账 | `TestCacheAuditBooksAggregateRowsApart` |
| 确定性复跑 | `TestCacheReportIsReproducible`、`TestCacheAuditIsReproducible` |
| 可复算的下沉 | `TestRecorderPersistsTheRequestCountProvenance` |

### 4.3 可复跑数据质量报告（交付物 4）

```bash
# 成员级（需一次真实 Team 会话留下记录）
reasonix team cache-report --json --out member-report.json
reasonix team cache-report --export-requests --out member-requests.jsonl

# 路由级历史账本（等样本量双臂）
reasonix team cache-audit --model deepseek-v4-flash-roojin \
  --from 2026-09-23 --to 2026-09-23 --first 1714 --json --out arm-0923.json
reasonix team cache-audit --model deepseek-v4-flash-roojin \
  --from 2026-09-24 --to 2026-09-24 --first 1714 --json --out arm-0924.json
```

真实会话复跑（需要凭证；`-tags live`）：

```bash
go test -tags live ./internal/cli/ -run 'TestLiveTeamMemberCache(Session|BaselineTeam)$' -v -count=1
```

输出自带：`source`、解析窗口（按**账本本地时钟**）、读入/窗口外/不可解析行数、`coverage` 逐字段覆盖率、`exclusions` 逐类计数、`all samples` 对账行、请求计数 provenance 行，以及「本次审计无法确立什么」清单。相同输入与过滤条件输出逐字节一致。

### 4.4 验收对照（方案 §2 Agent A 验收标准）

| 验收项 | 状态 |
|---|---|
| 每个纳入主指标的样本都有明确成员、usage 来源、统计单位和纳入理由 | ✅ 成员/来源字段 + `coverage` + §3.2 五条件 |
| `request_count` 缺失或默认补成 1 时不自动认定为单请求；资格可审计 | ✅ `request_count_source` 闭集；默认/未记录一律排除并单列 token |
| 低命中样本不因结果不利而过滤；有效、排除、未知均计数 | ✅ `TestCacheReportKeepsLowHitSamplesInTheBaseline`；`exclusions` 逐类 |
| 同一输入和筛选条件可确定性复跑，并能与原始记录对账 | ✅ 复现性测试；`all_samples` 加回等式 |
| 观测日志不含 prompt、工具参数/结果正文或 secret；日志故障不阻断成员请求 | ✅ `TestObservedRequestsCarryNoContent`、`TestRequestLogFailureDoesNotReachTheMember` |
| `hit == 0` 统一表述为「未报告 cache read」；本地 hash 不称 provider cache key | ✅ 审计输出自带该措辞；`stable_prefix_hash` 文档标注 |
| **禁止**：不改 provider-visible 请求字节 / 缓存策略 / 裁剪 / 成员隔离 | ✅ 本次改动只加字段、加排除分类、加覆盖率与渲染 |

## 5. 数据质量报告（2026-09-24 快照）

### 5.1 历史账本：per-request 基线**不可用**，全样本率可复现

两天账本各 1,714 行**全部无 provenance 标记**（`request-count provenance: measured 0 of 1714 rows`）：

| 指标 | 09-23 臂 | 09-24 臂 |
|---|---:|---:|
| 读入行数 | 1,714 | 1,714 |
| 计入 per-request 基线 | **0** | **0** |
| 排除：多请求聚合 | 58 | 57 |
| 排除：计数未验证 | 1,714 | 1,714 |
| 排除：无 cache split | 9 | 1 |
| **全样本加权率** | **84.5%** | **67.7%** |
| 全样本 hit / miss | 130,642,816 / 24,027,267 | 110,437,632 / 52,698,416 |
| 其中 聚合 hit / miss | 6,295,168 / 2,265,119 | 5,280,000 / 4,048,619 |
| 其中 计数未验证 hit / miss | 124,347,648 / 21,762,148 | 105,157,632 / 48,649,797 |

**这取代了** `DATA_AUDIT.md` §2.2 的 85.1% / 68.4%：方向与幅度几乎不变（−16.8pp vs −16.7pp），但那个数字现在只能以**全样本加权率**的形式引用，**不能**称为「有效单请求 token 加权命中率」。`§2.3` 的分布形状与 `§2.4` 的分桶结论建立在同一批行上，其口径同样降级为「全样本、路由级」。

### 5.2 真实会话：provenance 完整，成员级基线不受影响

| 会话 | 记录 | 成员 | 计数来源 | 主基线 | 全样本加权率 |
|---|---:|---:|---|---:|---:|
| `TestLiveTeamMemberCacheSession` | 12 | 3 | **observed 12/12** | 12/12 计入 | 66.3%（hit 607,104 / miss 308,327） |
| `TestLiveTeamMemberCacheBaselineTeam` | 30 | 3 | **observed 30/30** | 30/30 计入 | 78.4%（hit 1,780,352 / miss 491,683） |

两次真实会话的 `exclusions` 全零（无聚合、无 unknown/estimated、无账目无效、无缺 split、无未验证计数），`coverage` 显示 route/model/诊断覆盖率 100%。**样本量仍不足以做跨成员推断**（12 与 30 条、3 成员，桶级仍 `insufficient_sample`）。

### 5.3 不可验证项（与数字同读）

- 上游是 inclusive 还是 exclusive：**不可验证**（适配层归一化后原始字段即丢弃）。
- 是否发生重复计数：只有启发式判别（`0 < prompt − 2×hit < 500`，两天 2/3 行），**不是独立核对**。
- 冷启动 / 前缀变化 / fold：历史账本**不可验证**（无 session 身份、无诊断字段）。
- 多 request 聚合的拆分：**不可拆分**。
- Provider 侧 TTL、并发、容量、逐出、账号池：**不可观测**，是未控制的混杂因素。
- 任务组成与工作负载：三天的组成未知且很可能不同；等样本量只对齐了样本数。

## 6. 对 `DATA_AUDIT.md` 的口径修订

1. **§1.3 有效样本规则**：原规则「`requests == 1`」在账本上**不可执行**——旧行的 `requests` 可能来自兼容默认值。新增条件「计数必须被观测」。账本因此没有 per-request 基线。
2. **§2.2 等样本量双臂**：85.1% / 68.4% 降级为全样本加权率 84.5% / 67.7%（§5.1），结论方向不变。
3. **§2.3 / §2.4 / §3**：其行级统计建立在同一批无 provenance 的行上，口径同降级；§3 的 H1 仍是**假设**，未因此增强或削弱。
4. **§4.2 成员级报表**：当时的口径不含计数 provenance；本次重跑（§5.2）显示新构建的成员记录 100% `observed`，故成员级结论**不变**，但样本数从 12 更新为「12 与 30 两次会话」。

## 7. 限制清单（必须随结论一起引用）

| # | 限制 | 影响 |
|---|---|---|
| L-1 | provenance 只表达「usage 是否带了计数」，**不能**区分「上游 merge 造出的 1」。`mergeSamplingUsage` 在 `attempt.RequestCount == 0` 时补 1 且标 `false`，但上游若自己写了 1，本层无法识别。 | 不能宣称「计数已被独立核对」 |
| L-2 | 历史账本无 provenance，**不可回溯补齐**。除非用新构建重跑，否则旧账本永远只能出全样本口径。 | 历史对比只能作为线索 |
| L-3 | `requests_observed` 是账本行的新键；**旧 reader 会忽略它**，仍按 `requests` 读取。 | 兼容，但旧 reader 得不到该区分 |
| L-4 | `providerUsageFromLastTurn` 不回填 provenance（`RequestCountSource` 是单向的）。快照文档缺失时 `RequestCount` 可能为 0。 | `.usage.json` 的该字段只作展示 |
| L-5 | 覆盖率只统计「进入成员范围的样本」，不含窗口外与非成员样本。 | 覆盖率不是「读入行」的覆盖率 |
| L-6 | `all_samples` 率把不同性质的样本（聚合、未验证计数）混在一起，**只能作全样本口径**。 | 不得作为 per-request 率或验收目标 |
| L-7 | 本地 `prefix_hash` 覆盖 system+tools，不是 provider cache key。 | P1 只能作客户端差分证据 |
| L-8 | `hit == 0` = 未报告 cache read，**不是**冷启动。 | 不得据此推断缓存生命周期 |

## 8. 变更通知机制（给 Agent B / Agent C）

- **契约版本**：`v1`（本文）。记录与报表的 `schema_version` 是 `team.SchemaVersion`。
- **冻结面**：§3 的主指标、§3.2 的有效条件、§3.3 的排除分类与 token 归属、§3.4 的覆盖率定义、§1.1 的 `source` 标签。
- **谁改什么**：Agent A 独占观测/统计契约与报表文件（§4.1 所列）。Agent B 按本契约标记实验样本与无效样本，**不另立分母**；Agent C 独立复核，**不另立口径**。
- **变更规则**：任何对 §3 的改动必须**升版本号**并在本文追加一节，同时标注「自哪个版本起数据不可与旧版比较」。B 必须为实验标记契约版本；C 必须重新确认结论是否仍可比较。
- **本轮通知**：v1 相对 `DATA_AUDIT.md` 的差异即 §6 的三条，**影响**：账本的 per-request 率不可用（B/C 若引用过 85.1%/68.4% 需改口径）；成员级不受影响（新构建 100% `observed`）。

### 8.1 对 Part B 交付的回应（2026-09-24）

Part B 的 `TEAM_MEMBER_CACHE_PROVIDER_EXPERIMENT_PART_B.zh-CN.md` 已交付（9 臂 + 1 装配探针）。
A 对其三条交接请求的处理：

| B 的请求 | A 的处理 | 落地 |
|---|---|---|
| 成员级报表显式保留「未命中 token/请求」这一列 | ✅ **已实现** | `CacheGroupStat.MissTokensPerRequest` / `HitTokensPerRequest` / `HasPerRequestTokens`，报表每个 stratum 都打印 `hit/req=… miss/req=…`；`TestCacheReportPublishesMissTokensPerRequest` 固定组成效应，`TestCacheReportPerRequestColumnsAreEncoded` 固定 JSON 面 |
| 「无 cache split」分类可直接复用为契约参考 | ✅ **口径一致**：A 的 `no_cache_split`（`hit+miss == 0`）与 B 的 `usage_split=false` 是同一判据；A 另有 `unverified_request_count` 这一 B 侧不存在（B 自己按 arm 登记，不走账本计数） | §3.3 |
| 冷启动、错误、重试、无 split 四类排除后仍计数 | ✅ **口径一致**：A 的 `first_request` 是**阶段标签**（不进排除分类），错误/重试在 A 的账本路径上表现为 `unknown_usage`/`estimated_usage` 或缺失样本，无 split 为 `no_cache_split`；四类均计数不归零 | §3.3、§3.5 |

**A 对 B 结论的口径引用要求**（B 的 §6 已自行声明，A 在此确认）：

- B 的第 3 条（组成效应）**只能用 `miss_tokens_per_request` 表达**，不得写成「命中率下降/上升」。
  这与 A 在 `DATA_AUDIT.md` §2.4 的读法一致：两臂 `128k_256k` 桶均值 prompt 更小而 miss/请求涨 71%，
  即**命中率的变化可以在未命中工作量反向变化时发生**。
- B 的 232/168/197 tokens/请求是**该 route、该账号、串行单请求**下的固定开销；B 自己已声明不能外推。
  A 的成员级复现要求：在 `32k_128k` 等桶上，若 `miss/req` 与尺寸无关且落在同一量级，才可作为候选 (i) 的证据。
- **B 的实验不进入 A 的任何主指标**：B 的记录在 `internal/cachelab` 的 journal 里，不写 `.cache_requests.jsonl`，
  不带 `request_count_source`。A 的成员级报表**不读它**，两套数据不得相加。

## 9. 需要补采的数据清单

1. 成员级样本量：当前 12 与 30 条、3 成员，桶级全部 `insufficient_sample`；需要 ≥30 请求 / ≥3 成员**每桶**才够跨成员推断。
2. 覆盖 ≥128K prompt 的会话：`128k_256k` 桶需要独立达到门槛，才能检验 H1。
3. 用**新构建**重跑一次历史时段，才能得到带 provenance 的账本行（L-2 的唯一出路）。
4. 若可能，保留一轮 provider 原始 usage（`input_tokens` / `cache_read_*`）用于独立核对口径——当前账本不保留，属结构性缺口。
