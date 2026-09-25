# Team Member 缓存一次性收口：A / B / C 与合流工作结论汇总

> 日期：2026-09-25。基线 commit：`2c0f1dc61`（Team-agent，"Step 4"）。
> 执行方案：`TEAM_MEMBER_CACHE_ONE_SHOT_3AGENT_EXECUTION_PLAN.zh-CN.md`。
> 分侧原始报告：`TEAM_MEMBER_CACHE_ONESHOT_PART_{A,B,C}_RESULT.zh-CN.md`；最终决策：`TEAM_MEMBER_CACHE_ONESHOT_FINAL_DECISION.zh-CN.md`。
> 用途：**供交叉分析与后续派单**。本文只汇总**已交付的结论**，逐条标注强度、适用范围与待攻击点。

---

## 0. 一页结论

| 侧 | 交付 | 自评 | 协调者裁定 |
|---|---|---|---|
| **A**（维护状态机与压缩成本契约） | 10 项核验全部完成，**未发现行为缺陷** | `CONDITIONAL` | `CONDITIONAL` |
| **B**（Provider-visible 前缀与 128 参数） | 7 项中 6 项 PASS，1 项未验证 | `CONDITIONAL` | `CONDITIONAL` |
| **C**（真实 Provider 条件矩阵与准入） | 条件矩阵**第一次执行**，发现两个阻塞性缺口 | `CONDITIONAL` | `CONDITIONAL` |
| **合流** | 给 A/B 的行为加开关；更正条件注册表；新增矩阵守卫 | — | — |

### 最终决策

# `NO-GO / CONTINUE-SAMPLING`

观测链路与受控分类逻辑已完成收口；行为优化仍未验证，继续采样，禁止灰度。

### 一句话

**这一轮把「优化是否有效」从无法回答，变成了一组可执行、可验证、但结论为「测不出来」的实验**——四个条件臂在真实 Provider 上跑通并可验证配置，同时暴露出**三个使 GO 结构性不可达的缺口**（§5）。

---

## 1. 三方各自交付了什么

### 1.1 A：把「压缩完成」从断言变成可复算的判据

| # | 核验项 | 判定 | 关键证据 |
|---|---|---|---|
| A-1 | 七态决策覆盖 | **PASS（含一处文档缺口）** | 7 个状态中**只有 3 个能到达已发布的 receipt**；`below_boundary` / `above_boundary` / `at_ceiling` / `rescued` 不可达（A 报告 §2.3） |
| A-2 | headroom 目标与 `visible_window_tokens` cap | **PASS** | 真实持久化并读回：`headroom = 25000 − 22199 = 2801` ✓，`reduction_ratio` ✓ |
| A-3 | low-yield latch 的锁存 / 释放 / overflow bypass | **PASS** | 实测：已锁存视图 6 次调用只付 1 次 summary；overflow 绕过 |
| A-4 | ladder 与 rescue 最后一级约束 | **PASS** | rescue 只在 `overflow` 或 `result >= hard` 可达 |
| A-5 | 四项维护成本计数器 | **PASS** | 四条 summary 通道全经过同一点；`rescueCount` / `repeatBlocks` 实测增长 |
| **A-6** | **M1 粒度偏离的接受意见** | **`CONDITIONAL`** | 无协调者书面接受（**本轮已裁定接受**，见 §3） |
| A-7 | generation / turn 重复调用 | **PASS** | 每 turn ≤ 2 次 summary + 1 次 overflow，**与 HEAD~1 逐字相同**，A 未加重 |
| A-8 | additive 字段不改字节与 schema | **PASS（证据有上限）** | schema 仍 V4；新字段全 `omitempty`；**上限见 A 报告 §8.2** |
| A-9 | `low_yield` / `recovered` 边界攻击 | **PASS（发现 2 处 fail-open）** | 9 个边界构造（发布表只有 7 个理想值） |
| A-10 | `compactionProgress` 死字段 | **已给建议，未实施** | `consecutive` / `lastTurn` 读点数为 **0** |

**A 没有发现行为缺陷**：A-9 的两处 fail-open 是**已记录的设计**，A-7 的有界重复是**既有行为**。

### 1.2 B：让「改写已读字节」第一次可观测

| # | 项 | 结果 | 关键证据 |
|---|---|---|---|
| B-1 | 消息数组指纹 / 首个分歧位置 / 改写数 / rewrite reason | **PASS** | 5 个字段的 producer → publisher → consumer 链路逐项核对 |
| B-2 | 四类离线 prefix benchmark | **PASS** | 10 个臂全绿：成员切换 0/22 分歧、后端重建 0/15、MCP 重注册 0/15、fold **4/39**（4 次 fold → 恰好 4 次改写） |
| B-3 | 配置继承在**活 Agent** 上 | **PASS** | 断言 `HeadroomGoal == 80000`（配置的 cap），不是 options 结构体 |
| B-4 | `messages` reason 的生产可达形状 | **PASS（新增端到端证据）** | 8 个生产改写点会触发；`replaceSessionModelContext` **自带 reason 形参却从不入队** |
| **B-5** | `append_block_allowance` 的真实分布 | **未验证** | 无凭证、无历史样本；`-tags live` 未执行 |
| B-6 | 无跨环境证据时不得外推 | **PASS** | 已标为「未验证」 |
| B-7 | 4 个新增字段的 eventwire 范围 | **PASS（含补丁建议）** | **跨进程不可见**，进程内链路完整 |

**B-2 的四类臂断言的是不变量而非命中率**：非 fold 请求 `appended >= 0` 恒成立；fold 之后**至多一次**改写；每个臂声明的事件**必须真的发生**（否则测试自身变红）。

### 1.3 C：把条件矩阵从「已登记、从未执行」推到「已执行且可验证配置」

| # | 结论 | 强度 | 关键证据 |
|---|---|---|---|
| C-1 | 四个条件臂在真实 Provider 上**可执行且可分离验证** | 证据 | 用**消费结果**（`compactTrigger` vs `hardCeiling`）验证，不是 options 结构体 |
| **C-2** | **`b_only` 在当前构建里不可配置** | 证据 | `ConditionBOnly` 与 `ConditionBaseline` 的开关集**逐字相同**；B 侧改动**零配置开关** |
| **C-3** | **条件臂差异落在噪声内** | 证据 | baseline `[494,522,508]` / a_only `[494,508,522]` / a_plus_b `[515,494,515]`，**臂内散布与臂间范围完全重叠** |
| C-4 | **128 是「滞后一块」模型** | 证据 | `hit == floor(prev_prompt / 128) × 128` 在 **251/251** 条真实样本上成立 |
| C-5 | **C 的豁免值比模型上界多 1 token** | 证据 | 模型蕴含 `miss − growth ≤ 127`，规则用 128；实测 `max excess = 127`、`over_allowance = 0` |
| C-6 | `provider_residual_unexplained == 0` 在 5 次运行、150 条上重复成立 | 证据 | 此前是 1 次 30 条 |
| **C-7** | **A/B 行为路径在真实负载中全部未触发** | 证据 | `summary_requests=0`、`rewrite=0`、`rotations=0`、`MessagesRewritten=0/150` |
| C-8 | `cold_prefix` 占 99% 是**分母效应** | 证据 | 3 条冷请求 × ~6.3 万 tok vs 135 条 warm × ~85 tok |
| **C-9** | **质量、延迟、任务完成率不可测** | 证据 | 记录无 task result、无请求时长；`RunTurn` 返回 `error` 不返回答案 |
| C-10 | **`route_bucket` 不是账号作用域** | 证据 | 哈希材料含 UserID、**不含凭据**；同一账号不同 UserID → 不同 bucket |

### 1.4 合流：给 A/B 的行为加开关

**这是本轮唯一的生产代码变更**（用户拍板「默认开」并授权动工）。

| 新配置键 | 默认 | 关掉后的行为 |
|---|---|---|
| `agent.low_yield_latch` | **开** | 恢复「输入哈希一变就重试」的旧行为 |
| `agent.message_shape_diagnosis` | **开** | 数组完全不比较 |

命名沿用 `Disable*` 前缀（与 `DisableWriteAccessExpand` 一致），使 `agent.Options{}` 的零值保持行为开启——仓库有 **378 处**测试直接构造零值 `Options{}`，用 `Enable*` 会让它们静默丢掉诊断。

---

## 2. 三条跨侧成立、单独一侧看不到的结论

### 2.1 `baseline` 不是 baseline —— 本轮修正前的事实

C 的 §3.3 发现：**四个注册条件没有一个能隔离 A 或 B**。

| 注册条件 | 修正前的实际行为 |
|---|---|
| `baseline` | **已包含 A 的全部行为改动**（七态分类、headroom 判据、低收益锁存、四个计数器**全部无门控**） |
| `a_only` | baseline + 一个 **2026-09-03 从上游合入**的既有特性（`cache_aware_compaction`） |
| `b_only` | = baseline（B 无开关） |
| `a_plus_b` | = a_only（开关集逐字相同） |

**为什么长期没被发现**：`TestConditionSwitchesAreAReproductionRecipe` 只断言 A-only 与 B-only 的开关**不相同**——它们确实不相同（A-only 有 `cache_aware=true`），所以**测试绿了但前提不成立**。这与上一轮记录的教训同类：**断言了合成边界，而非被消费的边界**。

### 2.2 A 的 headroom 目标依赖 B 修的那个配置键

`GoalMet() = HeadroomTokens >= recentTailBudget()`，而 `recentTailBudget() = min(窗口 × 0.16, visible_window_tokens)`。

**B 修的正是 `visible_window_tokens` 被 `agent.New()` 静默丢弃**。所以：在任何设置了该 cap 的部署里，**A 的 headroom 目标在 B 的修复之前是错的**（退化成窗口 16%，比配置的 cap 大，于是 A 会把「本该算 recovered」的 fold 判成 `low_yield`）。

**两条改动有顺序依赖，不是独立的。**（已写入上一轮联合结论 §3.3）

### 2.3 条件注册表现在是自洽的

修正后（`internal/cachelab/plan.go`）：

| 条件 | `cache_aware_compaction` | `low_yield_latch` | `message_shape_diagnosis` | `context_rescue` |
|---|---|---|---|---|
| `baseline` | false | **false** | **false** | false |
| `a_only` | true | **true** | false | false |
| `b_only` | false | false | **true** | false |
| `a_plus_b` | true | true | true | false |
| `rescue_enabled` | true | true | true | true |

`baseline` **真的是基线**；`b_only` 与 `a_plus_b` **第一次可执行**。新增守卫 `TestEveryConditionReachesTheMembersOwnAgent` 把五个条件逐一走生产成员构造器验证。

**该守卫的设计边界**（写在测试里）：只有 `cache_aware_compaction` 有 snapshot 可观测，且它的延迟需 warm receipt 才显现，因此该臂断言**配方**；**每个开关的行为守卫留在行为所在处**。

---

## 3. 协调者的两处书面裁定（计划 §4 要求）

**A-6：`summary_requests` 粒度偏离 —— 接受，附量化代价。**

A 把它放在**会话级累计**而非 receipt 上（一次决策最多 4 次调用，receipt 是决策记录）。代价：计划 §6.2 的 `compaction_requests_per_turn` 只能给「会话总量 / turn 数」的**粗粒度**，**不可读作逐 turn 精确值**。**不要求补逐 turn 计数**，理由是 C 的 `turn_cost` 已提供逐 turn 视角。

**A-6：`rescue_planned` 不放 receipt —— 接受。** plan 走 `ContextRescueRequired` 错误通道，复制进 receipt 会产生第二个真相来源。

**B-5：128 未跨环境验证 —— 接受为已知限制，不阻塞合流。** 要求所有后续报告沿用「单网关归纳参数，不作 Provider 常量外推」的声明。

**这三项裁定不改变任何一方的 `CONDITIONAL` 状态**，只解除「未获书面接受」这一形式阻塞。

---

## 4. 128 参数：从「归纳值」到「可逐样本证伪的模型」

这是本轮**最强的一条单侧发现**（C-4/C-5）。

```text
模型：hit == floor(prev_prompt / 128) × 128
```

| 样本集 | 样本数 | 成立 | 违反 |
|---|---:|---:|---:|
| team matrix（5 次运行，warm） | 135 | 135 | 0 |
| `K1-warm-fold` 固定字节臂 | 30 | 30 | 0 |
| `B1-baseline-repeat` 固定字节臂 | 29 | 29 | 0 |
| `B4-ladder-large` 固定字节臂 | 30 | 30 | 0 |
| session probe（3 次） | 27 | 27 | 0 |
| **合计** | **251** | **251** | **0** |

**恒等式（全部 251 条成立）**：`miss == prompt − hit`、`hit % 128 == 0`。

**与「累积前缀块」的区别**：若命中是「当前请求自身 prompt 的块」，则 `hit = floor(prompt/128)×128`。251 条里 **21 条违反该式但全部满足滞后模型** —— 所以「滞后一块」才是正确读法。

**C 的豁免值比模型上界多 1**：模型蕴含 `miss − growth ≤ 127`，规则用 `128`。B 的计算器在真实样本上给出 `max excess = 127`、`over_allowance = 0` —— **是结构性必然，不是运气**。上一轮「均值 85 远低于 128」的论证是**弱的**（均值低不代表上界没被逼近）；`max=127` 才是强论证。

### 4.1 敏感性重算（计划 §C.2 第 6 项）

| 规则 | `cold_prefix` | `append_only_expected` | `provider_residual_unexplained` | 未归类 |
|---|---|---|---|---|
| **当前（+128）** | 15 / 941,364 | 135 / 11,513 | **0 / 0** | 0 |
| 较小阈值（+64） | 15 / 941,364 | 70 / 3,858 | **65 / 7,655** | 0 |
| 严格（+0） | 15 / 941,364 | 1 / 23 | **134 / 11,490** | 0 |
| **「未知不归类」** | 15 / 941,364 | 0 / 0 | — | **135** |

四种规则都精确对账到同一 scoped miss 总量 **952,877**。

**读法**：`provider_residual_unexplained` 从 0 到 134 **只取决于阈值选择，不是测量结果**。「未知不归类」那一行是本轮最保守、也最诚实的读法：135 条 append-only 样本在不知道块粒度的前提下，一条都不该被归入「已解释」。

---

## 5. 使 `GO` 结构性不可达的三个缺口

### 5.1 缺口一：A/B 的行为路径在现实会话里不触发（最要紧）

| 路径 | 观测值（150 条 + 3 个固定字节臂） | 判定 |
|---|---|---|
| `summary_requests` / `projection_installs` / `rescue_count` / `repeat_blocks` | 全 **0** | **未触发** |
| `rewrite_requests` / `structural_requests` / `rotations` | 全 **0** | **未触发** |
| `messages_rewritten_unclaimed` / `MessagesRewritten` | **0 / 150** | **未触发** |

**原因可解释，不是故障**：触发 A 的维护路径需要 prompt 越过 `compact_ratio × window`。本轮探针最大 prompt ≈ **232,000**，而 1M 窗口的触发边界是 **800,000**（baseline）或 **999,744**（a_only）—— **差 3–4 倍**。

**这直接解释了 C-3**：不是样本不够，是**三个臂测的是同一个不触发的路径**。

**按计划 §C.2 第 7 项**：只能报告为「路径未触发」，**不得**报告为「路径无缺陷」。

### 5.2 缺口二：质量与延迟不可测

| 项 | 核对结果 |
|---|---|
| 任务质量 | `MemberCacheRequest` 无 task result 字段；`Controller.RunTurn` 返回 `error` 而非答案文本 → **驱动侧也拿不到** ❌ |
| 请求延迟 | `MemberCacheRequest` 无 latency 字段；`cachelab.Sample` 有 `LatencyMS` 但只覆盖 provider-boundary 臂 ❌ |
| 成员隔离 | 报告按 `member_id` 分组，leader 记录被排除 ✅ |
| 安全边界 | 日志只存 digest / 长度 / 枚举 ✅ |

计划 §C.5 的条件 4、5 因此**无法评估** → `GO` 结构性不可达。

### 5.3 缺口三：`route_bucket` 不能当账号维度用

`RouteBucket()` 的哈希材料是 `(kind, endpoint, UserID, proxy)`，**不含凭据**。

| 观测 | 值 |
|---|---|
| 5 次 matrix（三成员共用 `AgentUserRef: "gw"`） | 1 个 bucket |
| 3 次 session probe（三成员各有 UserID） | **3 个不同 bucket** |

**两组的 endpoint 与凭据完全相同。** 所以 `route_bucket` 是「池条目身份」的指纹，**不是账号作用域**。计划 §C.2 第 4 项「每臂使用相同账号范围」**无法从数据自证**，只能靠操作纪律（同一个 `AgentUserRef`）。

**任何把 `route_bucket` 当账号维度分组的做法，会把同一个账号拆成多组。**

---

## 6. 门禁（合流快照）

**快照**：`git archive 2c0f1dc61` + 本轮 13 个改动/新增文件，全 `.go` 聚合 sha256 = `00d60c9d21ff3432364bb3e2044519980643e053b0c940be5da339bf45a341aa`。

| 检查 | 结果 |
|---|---|
| `go build ./...` | ✅ |
| `go test` × 11 个包（agent / boot / config / cli / control / cachelab / team / event / eventwire / cachereason / stats） | ✅ 全绿 |
| `go vet`（agent / config / boot / cachelab / cli） | ✅ |
| `gofmt -l internal/` | ✅ 空 |
| `scripts/cache-guard.sh` | ✅ **10/10** |
| `go run ./tools/repolint` | ✅ RED SET 与 `2c0f1dc61` **逐字节相同** |
| 条件矩阵守卫 | ✅ 5/5 条件 |

### 6.1 既有红项（非本轮引入）

| 项 | 状态 |
|---|---|
| 7 个文件的 essay/file-size 超预算 | carry-forward（HEAD 上即红） |
| `TestTeamTurnInjectsInboxAtSubmit` 的 `-count>1` flake | **既有**（30 次 6 次失败，改动前后相同） |
| `desktop/` 的 `TestHostContractGeneratedFilesAreCurrent` | HEAD 上即红；本轮未跑 desktop |
| `golangci-lint` | 未执行（本机未装；CI pin 2.12.2） |

---

## 7. 快照哈希的可复现性（**必须记录的一处不一致**）

| 来源 | 发布的内容哈希 | 可复现性 |
|---|---|---|
| **A** | `9e2b9623784d74cdf2d69581277553d96eb0368c30db741c1d6e49c8c38fcf24` | ❌ **本轮无法复现** |
| **C** | `669e2be74b346926e212f52fab5f3c69532ed03fc9013cae6714cfa5b31fff0c` | ✅ 复现。命令（本轮在 `/tmp/verify-head` 逐字执行）：`mkdir -p /tmp/verify-head && git archive 2c0f1dc61 \| tar -x -C /tmp/verify-head && cd /tmp/verify-head && find . -type f -name '*.go' \| sort \| xargs sha256sum \| sha256sum` |
| **合流** | `00d60c9d21ff3432364bb3e2044519980643e053b0c940be5da339bf45a341aa` | ✅ 同一方法，快照含本轮改动 |

**A 的哈希不可复现**：其报告 §1 声明快照是「纯 `git archive HEAD`」且工作树**无代码改动**，但用它与 C **完全相同的方法**哈希同一棵树，得到的是 **C 的** `669e2be7`，不是 `9e2b9623`。本轮试了 5 种方法（逐文件哈希、拼接、archive 流、tree 对象、含非 `.go` 文件），均未命中。

**这不影响 A 的结论**（A 的每一条都有 `go test` 原始输出与文件行号支撑，且本轮在合流快照上独立复跑了门禁）。但**任何引用该哈希作为「同一构建」凭据的读者都会读错**：`9e2b9623` 无法指向任何可重建的字节集。

**给下一轮的纪律**：发布内容哈希时必须同时给出**产生它的确切命令**。C 做到了（其报告 §2.2 完整列出命令），A 没有。

---

## 8. 结论强度总表

| # | 结论 | 强度 | 独立可复现 |
|---|---|---|---|
| 1 | A：七态分类覆盖计划七项，其中 4 态不可达 receipt | **证据** | ✅ 零成本 |
| 2 | A：headroom 判据可复算，未达标不报成功 | **证据** | ✅ 零成本 |
| 3 | A：同一视图的低收益 fold 不再重复付费（overflow/ceiling/manual 除外） | **证据（单元）** | ✅ 零成本 |
| 4 | A：每 turn ≤ 2 次 summary，与改动前逐字相同 | **证据** | ✅ 零成本 |
| 5 | B：消息数组指纹区分「追加」与「改写已读字节」 | **证据** | ✅ 零成本 |
| 6 | B：四个 prefix benchmark 臂在离线夹具下全绿 | **证据（离线）** | ✅ 零成本 |
| 7 | B：`messages` reason 有 8 个生产触发点 | **证据** | ✅ 零成本 |
| 8 | C：条件矩阵四臂在真实 Provider 上跑通 | **证据（真实 Provider）** | ✅ 需凭证 |
| 9 | C：128 是「滞后一块」模型，251/251 成立 | **证据（单网关）** | ✅ 零成本（journal 可直接验算） |
| 10 | C：归因分区在 5 次运行上闭合 | **证据（受控会话）** | ✅ 需凭证；**依赖 128 假设** |
| 11 | 合流：`baseline` 修正前含 A 的行为 | **证据** | ✅ 零成本 |
| 12 | **`9e2b9623` 指向哪一份字节** | **不可复现** | ❌ |
| 13 | **A/B 的任何行为改动在真实 Provider 上有效** | **未验证** | — |
| 14 | **`append_block_allowance = 128` 是 provider 常量** | **未证实（单网关归纳）** | ⚠️ 未跨账号/网关/并发 |
| 15 | **A 的 headroom 目标「16% 窗口」正确** | **自洽性论证** | ⚠️ 非测量 |
| 16 | **任务质量 / 延迟 / 完成率** | **本数据集不可测** | ❌ |

---

## 9. 给交叉分析的待攻击点

按价值排序：

1. **攻 A 的哈希**：`9e2b9623…` 用任何方法都无法复现。是否还有其它「不可复现的凭据」被当作已验证事实引用？本轮只检查了这一个。
2. **攻 §5.1 的「未触发」**：当前四个条件臂测一条不触发的路径。**这是最该被先解决的一项**——在此之前，任何条件臂的结论都无法区分「A 无效」与「A 未生效」。
3. **攻 128 模型**：`hit == floor(prev_prompt/128)×128` 在 251/251 上成立，但全是**单网关**。若它不成立，§4.1 的四行全部要重算，`provider_residual_unexplained == 0` 随之失效。
4. **攻本文 §2.1 的「测试绿了但前提不成立」**：`TestConditionSwitchesAreAReproductionRecipe` 的同类问题是否还在别处？检查方法是问「这个测试断言的是生产者真的会发出的形状吗」。
5. **攻协调者的两处裁定**（§3）：接受 A-6 的代价是 `compaction_requests_per_turn` 只有粗粒度。是否真的够用？
6. **攻 §5.3 的 `route_bucket`**：它是一个**看似账号标识、实则池条目身份**的字段。检查其它报表字段是否有同类语义错位。

---

## 10. 后续派单门槛

在形成灰度或优化准入结论前，**必须**完成：

1. **让维护路径真的触发**（§5.1）：把 `compact_ratio` 作为实验变量写进 `RegisteredConditions()`，或在探针会话里把 prompt 撑过 80 万 tok。**这是唯一的硬前置**。
2. **把质量与延迟纳入数据集**（§5.2）：扩数据面，属跨边界改动，需单独排期。
3. **跨账号 / 跨网关重跑 128 分布**（§4）：判据与计算器都已就绪，换环境只需重跑。
4. **桶级样本量提到 30/3**：本轮 `lt_32k` 10/1、`32k_128k` 20/2。
5. 补 `eventwire` 的 4 个字段（B-7），若需要 desktop/ACP 消费消息诊断。

在此之前保持：**继续采样，行为优化不放行，禁止灰度。**

**不得**以任何以下结果声称优化成功：本地测试全绿、`provider_residual_unexplained == 0`、单次或单网关会话命中率较高、维护计数器落盘成功但值为零、128 未产生 residual。
