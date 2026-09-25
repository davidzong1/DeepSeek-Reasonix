# Team Member 缓存一次性收口：Part C 结果（真实 Provider 条件矩阵与最终准入）

> 执行依据：`TEAM_MEMBER_CACHE_ONE_SHOT_3AGENT_EXECUTION_PLAN.zh-CN.md` §3「Part C」、§C.2 工作内容、§C.4 交付物、§5 派单。
> 日期：2026-09-25。基线：`2c0f1dc61`（Team-agent，Step 4）。
> **快照**：`git archive HEAD` 解包到 `/tmp/partc-snap`，全 `.go` 文件聚合 sha256 = `669e2be74b346926e212f52fab5f3c69532ed03fc9013cae6714cfa5b31fff0c`。
> Go `go1.26.6 linux/amd64`（`go.mod` pin `toolchain go1.26.6`）。
> **写集**：本文件。**未修改任何生产代码**（`git status` 中 `internal/` 仅含 A/B 的在途测试文件，见 §9）。
> **状态：`CONDITIONAL`**（§8 给出完整判定依据）。

---

## 0. 结论摘要

| # | 结论 | 强度 | 影响 |
|---|---|---|---|
| **C-1** | **四个条件臂在真实 Provider 上均可执行且可分离验证**：`baseline` / `a_only` / `b_only` / `a_plus_b` 各自被真正配置到成员自己的 Agent 上，用**消费结果**（`compactTrigger` vs `hardCeiling`）验证，不是只看 options 结构体。 | **证据**（§3.2） | 计划 §C.2 第 3 项的「条件矩阵已登记、从未执行」变为**已执行** |
| **C-2** | **`b_only` 在当前构建里不可配置**：B 侧的 `CompareShape` 消息指纹**没有任何开关**，`RegisteredConditions()` 对 `ConditionBOnly` 声明的开关集与 `baseline` **逐字相同**。因此 `b_only` 臂**就是 baseline 臂**，测不出 B 的贡献。 | **证据**（§3.3） | 计划 §C.2 第 3 项的 B-only 行**不可执行**，这是**阻塞性发现** |
| **C-3** | **条件臂之间的差异落在噪声内**：每臂 3 次独立运行（7 warm/次），warm miss 总量 baseline `[494,522,508]`、a_only `[494,508,522]`、a_plus_b `[515,494,515]`——**臂内散布与臂间范围完全重叠**。 | **证据**（§3.4） | 计划 §C.5「三次独立运行方向一致」**不满足** |
| **C-4** | **128 块粒度模型被逐样本证实，且是「滞后一块」模型**：`hit == floor(prev_prompt / 128) × 128`，在 **135/135** 条真实 warm 样本上成立（5 次 team matrix + 3 次 session probe + 3 个 constant-bytes 臂，共 240+ 条）。 | **证据**（§5.1） | 比 A/B 各自记录的「归纳值」强一档：这是一个**可逐样本证伪的确定性模型** |
| **C-5** | **C 的归因豁免值比模型自身允许的上界多 1 token**：模型蕴含 `miss − growth ≤ 127`，而 `cacheAppendBlockAllowance = 128`。B 的计算器在真实样本上给出 `max excess = 127`、`over_allowance = 0`——**是结构性必然，不是运气**。 | **证据**（§5.2） | §C.2 第 6 项的敏感性重算已给出（§5.3） |
| **C-6** | **`provider_residual_unexplained == 0` 在 5 次独立运行、150 条样本上重复成立**（此前是 1 次 30 条）。 | **证据**（§4.1） | 但**不是**「路径无缺陷」，见 C-7 |
| **C-7** | **A/B 的行为路径在真实负载中全部未触发**：`rewrite_requests=0`、`rotations=0`、`summary_requests=0`、`projection_installs=0`、`messages_rewritten_unclaimed=0`、`MessagesRewritten=0`（150/150）。 | **证据**（§6） | 按 §C.2 第 7 项，这只能报告为**「路径未触发」**，不得报告为「路径无缺陷」 |
| **C-8** | **`cold_prefix` 占 99% 是分母效应**，不是「冷启动是生产问题」：3 条冷请求 × ~6.3 万 tok vs 135 条 warm × ~85 tok。 | **证据**（§4.2） | 与上一轮合流结论一致 |
| **C-9** | **质量、延迟、任务完成率在本数据集仍不可测**（记录里没有 task result、没有请求时长）。计划 §C.5 要求这三项不劣于基线——**无法评估**。 | **证据**（§7.2） | §C.5 的 GO 条件之一**不满足** |
| **C-10** | **`route_bucket` 不是账号作用域**：它的哈希材料含成员池条目的 **UserID**，不含凭据。同一账号下不同 UserID 得到不同 `route_bucket`（3 次 session probe 的 3 个值全部来自同一个 endpoint 与同一份凭据）。 | **证据**（§7.4 L-4） | 计划 §C.2 第 4 项「相同账号范围」**无法从数据自证**；任何把 `route_bucket` 当账号维度的分组都会**把同一个账号拆成多组** |

**一句话**：条件矩阵从「已登记、从未执行」推进到**四个臂在真实 Provider 上跑通并可验证配置**；同时**发现了两个使 GO 不可能成立的硬缺口**——`b_only` 不可配置（C-2），以及条件臂差异在噪声内（C-3）。**建议 `NO-GO / CONTINUE-SAMPLING`。**

---

## 1. 逐请求数据字典（计划 §C.2 第 1 项）

数据来自成员写入者自己的记录（`MemberCacheRequest`，`internal/team/cacherequest.go`），经 `memberUsagePublisher` 从**进程内** usage 事件落盘。字段与计划要求的对应：

| 计划要求 | 字段 | 覆盖（5 次 matrix，150 条） |
|---|---|---|
| member / team | `member_id` / `team_id` | 150/150 |
| session lineage | `session_id_hash` / `session_ordinal` / `session_first_request_seq` | 150/150 |
| logical turn | `turn_id` + `session_request_seq` | 150/150（`turn_id` 在场；报告用 `session_request_seq` 定位前驱） |
| provider attempt | `request_count` + `request_count_source` | 150/150 全为 `observed` |
| route / model | `route_bucket` / `model_ref` | 150/150（5 次 matrix 全部同一 `route_bucket` `anthropic/107c70a37f88`；**3 次 session probe 有 3 个不同值，同一个账号**——见 §7.4 L-4） |
| 账号 | `route_bucket` = `kind + sha256(endpoint, name, proxy)[:6]`，其中 `name` 是**成员池条目的 UserID**（`team_backend_build.go:60-62,145-154`） | **结构性不可见**，且 `route_bucket` **不是账号标识**——见 §7.4 |
| prompt tokens | `context_prompt_tokens`（settled attempt 自身）/ `prompt_tokens`（多 attempt 求和） | 150/150 |
| cache hit/miss/write | `cache_hit_tokens` / `cache_miss_tokens` / `cache_write_tokens` | 150/150 |
| usage source | `usage_source` | 150/150 |
| request shape hash | `message_prefix_hash` / `prefix_hash` / `stable_prefix_hash` | 150/150（`diagnostics_available=true`） |
| maintenance reason | `prefix_change_reasons`（`internal/cachereason` 闭集） | 150/150，**全为空数组**（§6） |
| context bucket | `team.CacheRequestBucketOf(context_prompt_tokens)` | 150/150 |
| latency | **不存在** | ❌ 见 §7.2 |
| 质量结果 | **不存在** | ❌ 见 §7.2 |

**排除分类**（计划 §C.2 第 2 项）：`first_request` / `warm` / `retry` / `error` / `usage_missing` / `no_cache_split` / `usage_estimated` / `invalid_accounting`，由 `Sample.Classify()` 与 `cacheRequestIsBaselineEligible()` 给出，二者互斥。本轮 150 条中：**0 条被排除**（`received=150 included=150 excluded=0`）。

---

## 2. 执行环境与原始命令（计划 §2.3 第 2 项）

### 2.1 环境

| 项 | 值 |
|---|---|
| 快照 | `/tmp/partc-snap` = `git archive 2c0f1dc61`，`.go` 聚合 sha256 `669e2be7…fff0c` |
| Go | `go1.26.6 linux/amd64` |
| 模型 | `deepseek/deepseek-v4.1-flash[1m]`（team matrix 与 condition probe）；`deepseek/deepseek-v4.1-flash`（provider-boundary 臂） |
| route | 单一网关 `aiapi.lejurobot.com`（**账号与 endpoint 不落盘，故无法在报告里指名**，见 §7.4） |
| 凭证来源 | 进程环境 `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN`（`liveCacheCredentials()` 的 fallback 分支） |
| 时间窗 | 2026-09-25 02:0x–02:4x CST |

### 2.2 原始命令

```bash
# 条件矩阵：5 次独立运行，每臂 30 条（3 成员 × 10 turn）
go test -tags live ./internal/cli/ -run 'TestLiveTeamMemberCacheBaselineTeam$' -count=1 -v -timeout 25m

# 单成员会话探针：3 次独立运行
go test -tags live ./internal/cli/ -run 'TestLiveTeamMemberCacheSession$' -count=1 -v -timeout 20m

# provider-boundary 固定字节臂：3 个
REASONIX_LIVE_CACHE_ARM=K1-warm-fold       REASONIX_LIVE_CACHE_JOURNAL=/tmp/partc-K1-warm-fold.jsonl       go test -tags live ./internal/cli/ -run 'TestLiveProviderCacheExperiment$' -count=1 -v -timeout 60m
REASONIX_LIVE_CACHE_ARM=B1-baseline-repeat REASONIX_LIVE_CACHE_JOURNAL=/tmp/partc-B1-baseline-repeat.jsonl go test -tags live ./internal/cli/ -run 'TestLiveProviderCacheExperiment$' -count=1 -v -timeout 60m
REASONIX_LIVE_CACHE_ARM=B4-ladder-large    REASONIX_LIVE_CACHE_JOURNAL=/tmp/partc-B4-ladder-large.jsonl    go test -tags live ./internal/cli/ -run 'TestLiveProviderCacheExperiment$' -count=1 -v -timeout 60m

# 条件臂配置核验（本轮新增的临时探针，跑完已删除，见 §9）
REASONIX_LIVE_CACHE_CONDITION=<baseline|a_only|b_only|a_plus_b> REASONIX_LIVE_CACHE_TURNS=8 \
  go test -tags live ./internal/cli/ -run 'TestProbeConditionArmConfiguresTheMember' -count=1 -v -timeout 10m
```

原始日志：`/tmp/partc-matrix-{1..5}.log`、`/tmp/partc-session{,-1,-2}.log`、`/tmp/partc-arm-k1.log`、`/tmp/partc-{K1-warm-fold,B1-baseline-repeat,B4-ladder-large}.jsonl`。

---

## 3. 条件矩阵（计划 §C.2 第 3、4 项）

### 3.1 四臂在真实 Provider 上跑通

每个条件用 `ConfigSnapshot` 注入其注册开关集（`RegisteredConditions()` 的 `Switches`），组装**真实成员 backend**，跑 8 turn，然后：

1. 读**成员自己的 Agent** 的 `ContextMaintenanceSnapshot()`；
2. 读成员写入者落盘的 8 条记录。

### 3.2 配置在**消费结果**上可验证

`agent.cache_aware_compaction` 的消费结果是 `compactTrigger()`：开关打开且前缀 warm 时，触发边界被提升到硬上限。

| 条件 | 注册开关集 | `fold_trigger` | `hard_ceiling` | warm 延迟生效 |
|---|---|---:|---:|---|
| `baseline` | `cache_aware=false, rescue=false` | 800,000 | 999,744 | **false** |
| `a_only` | `cache_aware=true, rescue=false` | 999,744 | 999,744 | **true** |
| `b_only` | （与 baseline **逐字相同**） | 800,000 | 999,744 | **false** |
| `a_plus_b` | `cache_aware=true, rescue=false` | 999,744 | 999,744 | **true** |

`a_plus_b` 与 `a_only` 的开关集**也逐字相同**（`plan.go:231-235` vs `243-247`，两者都是 `{"agent.cache_aware_compaction": "true", "agent.context_rescue": "false"}`）。

### 3.3 阻塞性发现：`b_only` 与 `a_plus_b` 不是独立条件

**`ConditionBOnly` 与 `ConditionBaseline` 的 `Switches` 完全一致**（`plan.go:225-241`，两者都写着 `{"agent.cache_aware_compaction": "false", "agent.context_rescue": "false"}`）：两者都是 `{cache_aware_compaction: false, context_rescue: false}`。原因是 B 侧改动（`CompareShape` 的消息数组指纹、`cachereason.Messages`）**没有任何配置开关**——全仓库 `internal/agent/cache_shape.go`、`session_context.go`、`cachereason`、`event/session_context_diagnostics.go` 对 `config.` 的引用数均为 **0**。

后果：

1. **`b_only` 臂无法配置**。它跑起来是 baseline 字节 + baseline 行为，**不是** B 的贡献。上表 `b_only` 一行显示 `warm 延迟生效=false`，与 baseline 一致，正是这一点的直接证据。
2. **`a_plus_b` 与 `a_only` 无法区分**。两者开关集相同，因此 `a_plus_b` 臂测的是 A 而不是 A+B。
3. `plan.go:69-72` 的 `Conditions` 字段注释（「Conditions 是该臂可运行的客户端条件」）与 `ConditionArms()` 生成的 `Gated: true` 门禁都**预设**了「每个条件有一个可配置的开关集」，而 B 的行不满足这个前提。`TestConditionSwitchesAreAReproductionRecipe` 只检查 A-only 与 B-only 的开关**不相同**——它们确实不相同（因为 A-only 有 `cache_aware=true`），所以**测试绿了但前提不成立**。这与上一轮 C 文档 §5.5 记录的教训同类：断言了合成边界而非被消费的边界。

**这是 C 的准入判定中的 `BLOCKED` 项**（§8.2）。

### 3.4 四个条件臂的实测（每臂 3 次独立运行，各 7 warm 请求）

| 臂 | warm miss 总量（3 次） | 均值 | 臂内散布 |
|---|---|---:|---:|
| `baseline` | 494 / 522 / 508 | 508.0 | 28 |
| `a_only` | 494 / 508 / 522 | 508.0 | 28 |
| `b_only` | 508 / 522 / 608 | 546.0 | 100 |
| `a_plus_b` | 515 / 494 / 515 | 508.0 | 21 |

**臂间范围 494–608，臂内散布 21–100：完全重叠。** 计划 §C.5 要求「三次独立运行方向一致」——**不满足**，因为没有任何方向可读。

**这不是「A 无效」的证据**，而是「这批样本测不出差异」：`cache_aware_compaction` 只在**前缀 warm 且越过触发边界**时才改变行为，而本轮的探针会话（8–12 turn、23 万 tok）**从未越过任何触发边界**（`fold_trigger=800,000`，实测 prompt ≈ 232,000），因此 A 的开关虽被正确配置，**行为路径从未进入**。见 §6。

---

## 4. 归因分区（计划 §C.2 第 2 项）

### 4.1 5 次独立运行的一致结果

| 运行 | scoped | `cold_prefix` | `append_only_expected` | `provider_residual_unexplained` | `messages_rewritten_unclaimed` |
|---|---:|---|---|---|---:|
| 1 | 30 | 3 req / 188,811 tok | 27 req / 2,308 tok | **0** | 0 |
| 2 | 30 | 3 req / 188,799 tok | 27 req / 2,328 tok | **0** | 0 |
| 3 | 30 | 3 req / 187,927 tok | 27 req / 2,288 tok | **0** | 0 |
| 4 | 30 | 3 req / 187,909 tok | 27 req / 2,254 tok | **0** | 0 |
| 5 | 30 | 3 req / 187,918 tok | 27 req / 2,335 tok | **0** | 0 |
| **合计** | **150** | **15 / 941,364** | **135 / 11,513** | **0** | **0** |

分区对账闭合：`cold_prefix + append_only_expected == scoped`（请求数与 miss 总量都闭合）。

### 4.2 `cold_prefix` 占 99% 是分母效应

3 条冷请求各 ~6.3 万 tok，135 条 warm 各 ~85 tok。**这不是「冷启动是生产问题」**，是本轮脚本的 warm 请求几乎不 miss。计划 §C.2 第 7 项的纪律同样适用于此：不能把「冷启动吃掉了 99% 的 scoped miss」读成「优化冷启动能拿回 99%」。

### 4.3 质量与延迟：本数据集不可测

`CacheReportUnobservable()` 已把六项不可测指标写在报告里。C 独立核对：

| 项 | 核对结果 |
|---|---|
| 任务质量 | `MemberCacheRequest` 无 task result / outcome 字段（`cacherequest.go` 中 `quality` 一词只出现在 `AccountingValid` 的注释里）；`Controller.RunTurn` 返回 `error` 而非答案文本，因此**驱动侧也拿不到**。❌ |
| 请求延迟 | `MemberCacheRequest` 无 latency/duration/elapsed 字段（`ownerusage.go` 里唯一命中是 `Fresh(now, ttl)` 的参数）。`cachelab.Sample` **有** `LatencyMS`，但那只覆盖 provider-boundary 臂，不覆盖成员路径。❌ |
| 成员隔离 | 报告按 `member_id` 分组，leader 记录被排除（`TestLiveTeamMemberCacheSession` 断言 leader 贡献 0 条）。✅ |
| 安全边界 | 日志只存 digest / 长度 / 枚举，`Journal.Guard` 在落盘前拒绝夹具文本（`assertExperimentRecord` 逐字面量检查）。✅ |

---

## 5. 128 参数敏感性（计划 §C.2 第 6 项）

### 5.1 块粒度模型：`hit == floor(prev_prompt / 128) × 128`

这是本轮最强的单条发现。它对**当前请求**说：命中量等于**上一次请求的 prompt** 向下取整到 128 的整数倍。

| 样本集 | 样本数 | 模型成立 | 违反 |
|---|---:|---:|---:|
| team matrix（5 次运行，warm） | 135 | **135** | 0 |
| `K1-warm-fold` 固定字节臂 | 30 | **30** | 0 |
| `B1-baseline-repeat` 固定字节臂 | 29 | **29** | 0（1 条是首请求，无前驱） |
| `B4-ladder-large` 固定字节臂 | 30 | **30** | 0 |
| session probe（3 次，warm） | 27 | **27** | 0 |
| **合计** | **251** | **251** | **0** |

**恒等式（全部 251 条成立）**：`miss == prompt − hit`、`hit % 128 == 0`。

模型蕴含的两个可证伪推论，**均成立**：

1. `hit` 恒为 128 的倍数 → 已在上表验证。
2. `miss − growth == prev_prompt mod 128 ∈ [0,127]` → 在 135 条上实测 `excess` 的 `max = 127`。

**与「累积前缀块」的区别**：若命中是「当前请求自身 prompt 的前缀块」，则 `hit = floor(prompt/128)×128`。本轮 251 条里 **21 条**（全是冷请求与 128 边界跨越点）违反该式但**全部满足滞后模型**。所以「滞后一块」才是正确的读法——这是对上一轮结论的一处**细化**，不推翻。

### 5.2 C 的豁免值比模型上界多 1

```text
模型：excess = prev_prompt mod 128  ∈ [0, 127]
规则：appendExplainsMiss := miss <= growth + 128   ⇔   excess <= 128
```

用 B 的计算器（`appendMissGranularity`，逐字复制其代码到快照内运行，跑完删除）在**真实样本**上测量：

```text
append miss granularity over 150 records (allowance 128 tok):
  samples=135 unknown=15 over_allowance=0 mean=62.3 p50=61 p90=111 max=127
```

`max = 127` 正好是模型上界，`over_allowance = 0`。**结论**：`over_allowance = 0` 是**结构性必然**，不是「允许量偏大但恰好没掩盖 residual」。上一轮合流结论 §4.3 说「27 条样本的 miss 均值 85 远低于 128，所以没掩盖 residual」——**那个论证是弱的**（均值低不代表上界没被逼近）；本轮的 `max=127` 才是强论证。

**同时**：`128` 允许了模型禁止的 1 token。这不是缺陷（更宽松 = 更保守方向），但**必须记录**：规则与模型在边界上不一致。

### 5.3 三种规则的重算（计划 §C.2 第 6 项要求）

| 规则 | `cold_prefix` req/miss | `append_only_expected` req/miss | `provider_residual_unexplained` req/miss | 未归类 |
|---|---|---|---|---|
| **当前（+128）** | 15 / 941,364 | 135 / 11,513 | **0 / 0** | 0 |
| 较小阈值（+64） | 15 / 941,364 | 70 / 3,858 | **65 / 7,655** | 0 |
| 严格（+0） | 15 / 941,364 | 1 / 23 | **134 / 11,490** | 0 |
| **「未知不归类」** | 15 / 941,364 | 0 / 0 | — | **135** |

**四种规则都精确对账到同一 scoped miss 总量 952,877。**

**`+0` 那一行只剩 1 条**（run 4 / `cache-mid` / seq 6，`growth=23`、`miss=23`、`excess=0`）：它是 135 条里唯一一条「恰好把上一次请求的 prompt 对齐到 128 边界」的样本。这直接展示了「严格规则」会**因为一次边界巧合而保留 1 条、丢弃 134 条**——阈值选择对结论的支配力有多大。

**读法**：`provider_residual_unexplained` 从 0 到 134 只取决于阈值选择，**不是测量结果**。因此计划 §1「不得把 `provider_residual_unexplained == 0` 直接表述为优化成功」在本轮数据上有了量化依据：该数字的取值域横跨 `[0, 134]`。**「未知不归类」这一行是本报告最保守、也最诚实的读法**：135 条 append-only 样本在不知道块粒度的前提下，一条都不该被归入「已解释」。

---

## 6. A/B 行为路径的触发检查（计划 §C.2 第 7 项）

| 路径 | 观测值（150 条 + 3 个固定字节臂） | 判定 |
|---|---|---|
| `summary_requests`（A） | **0**（5 次运行 × 3 成员全为 0） | **未触发** |
| `projection_installs`（A） | **0** | **未触发** |
| `rescue_count`（A） | **0** | **未触发** |
| `repeat_blocks`（A） | **0** | **未触发** |
| `rewrite_requests`（C 报表） | **0** | **未触发** |
| `structural_requests`（C 报表） | **0** | **未触发** |
| `rotations`（C 报表） | **0** | **未触发** |
| `messages_rewritten_unclaimed`（C 归因类） | **0** | **未触发** |
| `MessagesRewritten`（B 字段，逐样本） | **0 / 150**（`MessagesComparable=true`） | **未触发** |
| `prefix_change_reasons`（B 字段） | **全为空数组 / 150** | 无改写可归因 |

**按 §C.2 第 7 项的措辞：全部报告为「路径未触发」，不得报告为「路径无缺陷」。**

**为什么未触发**（可解释，不是故障）：

1. 触发 A 的维护路径需要 prompt 越过 `compact_ratio × window`。本轮探针会话最大 prompt ≈ 232,000，而 1M 窗口的触发边界是 **800,000**（baseline）或 **999,744**（a_only）。差 3–4 倍。
2. 触发 B 的 `MessagesRewritten > 0` 需要**非 fold 的历史改写**。本轮成员会话是纯 append-only（每 turn 恰好 +23 tok 的 framing），因此 `MessagesRewritten` 恒为 0。
3. 触发 rescue 需要**普通压缩失败且仍越过硬上限**——前提是压缩先发生。

**这是一条对上一轮结论的重要补充**：上一轮把「维护计数器落盘成功但值为零」列为不可接受的「优化成功」表述（计划 §1 第 4 条）。本轮**独立复现了同样的零值**，并给出了原因：**触发边界离样本 3–4 倍远**。要让 A/B 路径真实触发，需要的不是更多样本，而是**更大的上下文**或**更低的 `compact_ratio`**。

---

## 7. 未决风险与限制（必须随结论一起引用）

### 7.1 限制清单

| # | 限制 | 影响 |
|---|---|---|
| **L-1** | **条件矩阵的 B-only / A+B 行不可执行**（§3.3）。 | 计划 §C.2 第 3 项无法完成；计划 §C.5「A/B 行为路径在真实负载中确实触发」对 B **永远无法成立**，直到 B 侧改动获得开关 |
| **L-2** | **A 的行为路径在 8–12 turn 探针里不触发**（§6）。 | 条件臂差异落在噪声内（§3.4）。要测 A，需要 prompt 越过 80 万 tok 的会话 |
| **L-3** | **质量 / 延迟 / 任务完成率不可测**（§4.3）。 | 计划 §C.5 的 GO 条件之一**无法评估**，因此 GO 不可达 |
| **L-4** | **单网关、单账号，且账号无法从数据自证**。`route_bucket` 的哈希材料是 `(kind, endpoint, UserID, proxy)`（`team_backend_build.go:145-154`），**不含账号凭据**。因此同一个账号下**不同 UserID** 会得到**不同** `route_bucket`：本轮 5 次 matrix 里三个成员共用 `AgentUserRef: "gw"` → 同一 `route_bucket`；而 3 次 session probe 里三个成员各有 UserID → **三个不同** `route_bucket`（`anthropic/577eace0fe11`、`8d302a7ba1da`、`097a1aabc6f2`），**尽管它们拨的是同一个 endpoint、同一份凭据**。 | 计划 §C.2 第 4 项的「相同账号范围」**无法从数据自证**，且 `route_bucket` **不能**被当作账号作用域使用 |
| **L-5** | **`128` 未跨账号、跨网关验证**。 | 与 B 侧结论一致；本报告**不**把它外推为 Provider 常量 |
| **L-6** | **滞后块模型是归纳的**，不是文档化的 provider 行为。251/251 成立仍只是单网关证据 | 若它不成立，§5.3 的四行全部要重算 |
| **L-7** | **并发未测**（全部串行单请求） | 计划 §5 P3 要求；本轮未覆盖 |
| **L-8** | **桶级样本量不足**：`lt_32k` 10/1 成员、`32k_128k` 20/2 成员，均低于 30/3 门 | 报表已标 `insufficient_sample` |
| **L-9** | **1M 桶未覆盖**：本机最大到 `128k_256k` | 计划 §6.2 |
| **L-10** | **`summary_requests` 是会话级，不是逐 turn** | 上一轮 A 已登记，C 侧判定不构成冲突；但计划 §6.2 的 `compaction_requests_per_turn` 只能给「会话总量 / turn 数」 |

### 7.2 质量与延迟为什么不可测（L-3 的取证）

| 项 | 核对结果 |
|---|---|
| 任务质量 | `MemberCacheRequest` 无 task result / outcome 字段（`cacherequest.go` 中 `quality` 一词只出现在 `AccountingValid` 的注释里）；`Controller.RunTurn` 返回 `error` 而非答案文本，因此**驱动侧也拿不到**。❌ |
| 请求延迟 | `MemberCacheRequest` 无 latency/duration/elapsed 字段（`ownerusage.go` 里唯一命中是 `Fresh(now, ttl)` 的参数）。`cachelab.Sample` **有** `LatencyMS`，但那只覆盖 provider-boundary 臂，不覆盖成员路径。❌ |
| 成员隔离 | 报告按 `member_id` 分组，leader 记录被排除（`TestLiveTeamMemberCacheSession` 断言 leader 贡献 0 条）。✅ |
| 安全边界 | 日志只存 digest / 长度 / 枚举，`Journal.Guard` 在落盘前拒绝夹具文本（`assertExperimentRecord` 逐字面量检查）。✅ |

### 7.3 报告自己声明的六项不可观测（`CacheReportUnobservable()`）

`internal/team/cachereport.go` 把六项不可测指标写在报告里，C 独立核对全部成立：task quality、provider request latency、maintenance **operations**（只有「宣告计数」）、provider cache key、provider scope/TTL/eviction/account pool、attempt-level rows。**这六项以「不可观测」而非「0」出现在报告里，是正确方向。**

### 7.4 `route_bucket` 不能当账号维度用（L-4 的取证）

`RouteBucket()`（`team_backend_build.go:145-154`）的哈希材料是 `(kind, endpoint, r.name, proxy.Mode, proxy.Type)`，其中 `r.name` 是**成员池条目的 UserID**（`team_backend_build.go:60-62`）。**凭据不在材料里。**

因此：

| 观测 | 值 |
|---|---|
| 5 次 matrix（三成员共用 `AgentUserRef: "gw"`） | 全部 `anthropic/107c70a37f88`（**1 个**） |
| 3 次 session probe（三成员各有 UserID：`probe-small`/`probe-mid`/`probe-big`） | `anthropic/577eace0fe11`、`8d302a7ba1da`、`097a1aabc6f2`（**3 个**） |

**两组的 endpoint 与凭据完全相同**，差异只来自 UserID。所以 `route_bucket` 是「池条目身份」的指纹，**不是账号作用域**。计划 §C.2 第 4 项要求「每臂使用相同账号范围」——该要求**无法从数据自证**，只能靠操作纪律（同一个 `AgentUserRef`）。反过来说：**把 `route_bucket` 当账号维度做分组，会把同一个账号拆成多组**，这是任何后续实验都必须避免的误用。

---

## 8. PASS / CONDITIONAL / BLOCKED 判定

### 8.1 计划 §C.4 交付物清单

| 交付物 | 状态 |
|---|---|
| 逐请求数据字典和样本分类报告 | ✅ §1 |
| Baseline / A-only / B-only / A+B / Rescue 原始实验记录 | ⚠️ **四个臂跑通，但 B-only 与 A+B 不可配置**（§3.3）；Rescue 未跑（需末端超限，§6 说明为何未达） |
| 质量、成本、延迟和 usage 覆盖率对照表 | ⚠️ 成本与覆盖率有；**质量与延迟不可测**（§4.3） |
| 128 参数敏感性分析 | ✅ §5.3（四种规则） |
| 灰度开关、回滚步骤和 go/no-go 建议 | ✅ §8.3 / §10 |
| C 侧 PASS / CONDITIONAL / BLOCKED | ✅ **`CONDITIONAL`**（§8.2） |

### 8.2 状态：`CONDITIONAL`

**不是 `PASS`**，因为计划 §C.5 的六项 GO 条件里：

| §C.5 条件 | 状态 |
|---|---|
| 四个正式条件均有足够有效 warm 样本，且三次独立运行方向一致 | ❌ 样本量不足（7 warm/臂，注册要求 30）；**且无方向可读**（§3.4） |
| A/B 行为路径在真实负载中确实触发并有可解释变化 | ❌ **全部未触发**（§6） |
| miss tokens/request 或总 input tokens 有改善且无不可接受成本回归 | ❌ 无差异可测（§3.4） |
| 任务完成率、必要上下文保留、工具调用正确率不劣于基线 | ❌ **不可测**（§4.3） |
| p50/p90 latency、usage 覆盖率、unknown 比例和安全边界满足预设阈值 | ⚠️ 覆盖率与 unknown 达标；**latency 不可测** |
| 结果不依赖未经验证的 128 常量 | ⚠️ 本报告的归因分区**依赖它**；§5.3 给出了不依赖它的读法 |

**也不是 `BLOCKED`**，因为观测链路、条件配置核验、128 模型与敏感性分析都已产出可审计结论；阻塞的是**行为准入**，不是本轮的取证工作。

**建议：`NO-GO / CONTINUE-SAMPLING`。行为优化不放行，不启动灰度。**

### 8.3 回滚（本报告未做任何生产改动，故为「撤销说明」）

| 本轮动作 | 撤销方式 |
|---|---|
| 临时条件探针（`internal/cli/zz_probe_condition_{test,helpers}.go`） | **已删除**；`/tmp/partc-snap` 已重建，`.go` 聚合 sha256 回到 `669e2be7…fff0c` |
| 真实 Provider 请求 | 不可撤销（已消耗）；用量：约 5×(3×10) + 3×12 + 3×31 + 4×3×8 ≈ **560 次请求**，全部走 `ANTHROPIC_AUTH_TOKEN` 对应的账号 |
| 落盘数据 | 全部在 `/tmp` 与各次运行的 `t.TempDir()`；**未触碰** `~/.reasonix/team/**` 与操作者自己的会话 |
| 生产代码 | **零改动**（`git status` 中 `internal/` 仅含 A/B 的在途测试文件） |

---

## 9. 写集与并行隔离（计划 §2.2）

C 本轮**只写了本文件**。工作树状态（`git status --porcelain`）：

```
 M docs/team-mcp-port/TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md   ← 非 C
?? docs/team-mcp-port/TEAM_MEMBER_CACHE_FINAL_PARALLEL_EXECUTION_PLAN.zh-CN.md     ← 非 C（计划文档）
?? docs/team-mcp-port/TEAM_MEMBER_CACHE_ONESHOT_PART_A_RESULT.zh-CN.md             ← Agent A
?? docs/team-mcp-port/TEAM_MEMBER_CACHE_ONESHOT_PART_B_RESULT.zh-CN.md             ← Agent B
?? docs/team-mcp-port/TEAM_MEMBER_CACHE_ONE_SHOT_3AGENT_EXECUTION_PLAN.zh-CN.md    ← 非 C（计划文档）
?? internal/cli/team_cache_append_granularity_test.go                              ← Agent B
?? internal/cli/team_cache_unclaimed_rewrite_test.go                               ← Agent B
```

**C 的临时探针从未出现在工作树**（只在 `/tmp/partc-snap` 内创建并已删除），因此 A/B 的在途编辑与 C 的执行**互不可见**。

**一处交叉**：C 为了在真实样本上运行 B 的 128 计算器，把 B 的 `team_cache_append_granularity_test.go` **复制进快照**（未改动 B 的原文件），跑完删除。B 的算法逐字未改。

---

## 10. go / no-go 建议与后续门槛

### 10.1 建议

> **`NO-GO / CONTINUE-SAMPLING`。观测链路与受控分类逻辑已完成收口；行为优化仍未验证，继续采样，禁止灰度。**

### 10.2 解除阻塞的最小动作（按依赖排序）

1. **给 B 侧改动一个开关**（解除 L-1）。没有它，`b_only` 与 `a_plus_b` 两个条件行**永远无法执行**，计划 §C.2 第 3 项不可能完成。这是**唯一的硬前置**。
2. **构造能让 A 路径触发的会话**（解除 L-2）。`compact_ratio` 默认 0.80，1M 窗口下触发边界 80 万 tok。要么把会话撑到 80 万 tok 以上，要么在**实验条件**里降低 `compact_ratio`（并在注册里写明）。在此之前，任何 A 的条件臂都在测一个不触发的路径。
3. **把质量与延迟纳入数据集**（解除 L-3）。成员记录没有 task result 与请求时长。驱动侧能拿到 finish reason（已有）但拿不到答案文本；要测质量必须扩数据面，属**跨边界改动**，需协调 Agent 批准。
4. **跨账号 / 跨网关重跑 128 分布**（解除 L-4、L-5）。本报告已给出可复算的判据（`hit == floor(prev_prompt/128)×128`）与可复用的计算器（B 的 `appendMissGranularity`），换环境只需重跑。
5. **桶级样本量提到 30/3**（解除 L-8）。本轮 `32k_128k` 20/2、`lt_32k` 10/1。

### 10.3 本轮可以立即收口的结论（三层）

1. **观测链路已验证**：A 的维护计数、B 的消息形状诊断、C 的会话身份与归因字段在受控成员会话中同时落盘，覆盖率 150/150。
2. **条件配置核验已可执行**：四个条件臂能被真正配置到成员自己的 Agent 上，并在**消费结果**（`compactTrigger` vs `hardCeiling`）上验证。
3. **行为优化有效性仍未验证**：不能据此声称重复压缩下降、前缀改写减少、命中率提升、成本下降或任务质量不变。
