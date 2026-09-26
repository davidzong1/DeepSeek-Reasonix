# Team Member 缓存 A / B / C 合流审查与证据边界（供交叉分析）

> 状态：**A / B / C 全部交付，已合流，未提交**。日期：2026-09-25。
> 基线：`5241384b6`（Team-agent）+ 三方未提交改动（35 个已跟踪文件改动 + 15 个新文件，共 50 项）。
> 执行方案：`TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md`。
> 用途：**供其他 Agent 交叉分析**。本文只汇总三方已交付的结论与证据，逐条标注**强度、可复现方式、待攻击点**。
> 本文不是“根因已确认”或“优化已生效”的报告；在不可变合流快照和正式实验完成前，所有可复现性均指**当前工作树可复现**。
> 文中明确区分“观测链路已验证”“受控样本上的分类逻辑成立”和“真实 Provider 上的行为有效性”，
> 并显式列出**三方一致否认的结论**与**互相矛盾之处**。详细材料见各自的交付文档：
> A `TEAM_MEMBER_CACHE_PART_A_STATE_MACHINE.zh-CN.md`；
> B `TEAM_MEMBER_CACHE_PART_B_PREFIX_STABILITY.zh-CN.md`；
> C `TEAM_MEMBER_CACHE_PART_C_FOLLOWUP_REVIEW.zh-CN.md`。

---

## 0. 给交叉分析者的读法

**先读 §2（三方各自改变了什么）与 §5（互相对不上的地方）。** §5 是本文最该被攻击的一节。

按价值排序的**待攻击点**：

1. **§4 的「归因分区闭合」**：`provider_residual_unexplained == 0` 依赖 `append_block_allowance = 128` 这个**归纳值**。若 128 不成立，residual 会吸收样本，整个「本地已解释」的结论会变。
2. **§3.2 的 headroom 目标**：A 取「窗口的 16%」而不是实测的一轮工具输出量。这是一条**自洽性论证**，不是测量。
3. **§5.2 的 `messages` 归属**：C 把 `cachereason.Messages` 从 rewrite 里拆出来单独成类，理由是它「不指向任何操作」。这依赖 B 的实现约定（只有无人认领时才写它）——**若 B 将来在别处也写 `messages`，该类的语义就变了**。
4. **§6 的「未验证」清单**：三方**都没有**真实 Provider 层的行为证据。所有行为结论的强度上限是**单元测试 + 离线夹具**。
5. **§5.1 的 flake 归属更正**：B 把一次 cli flake 归因于「C 并行编辑」，C 复现后证明它在**纯 HEAD 上同样失败**。这是一个**归因错误**的例子，值得检查是否还有同类。

**本文不主张**：任何「已找到根因」或「命中率已改善」的结论。三方一致：**行为优化不许放行**（§7）。

---

## 1. 三方各自交付了什么

| Part | 目标（计划 §3） | 核心产出 | 代码 |
|---|---|---|---|
| **A** | 上下文维护状态机与重复压缩修复 | 七态决策分类 + 可验证 headroom 目标 + 低收益锁存 + 4 个维护成本计数器 | 15 个文件（2 新） |
| **B** | provider-visible 前缀稳定性与缓存形状 | 消息数组指纹 + 首个分歧位置 + 已改写条数 + `messages` reason + 4 臂离线 prefix benchmark + 配置继承守卫 | 11 个文件（3 新） |
| **C** | 观测、真实 Provider 对照与合流准入 | 未命中归因分区（8 类）+ 会话身份维度 + 每 turn 成本 + 条件矩阵登记 + cachelab 解析修正 + 接手 A/B 的跨边界落地 | 19 个文件（6 新） |

**写集零重叠**（计划 §3 要求）：A 只碰 `internal/agent` 的 compaction 状态机；B 只碰 `cache_shape`/`shape 诊断`/`cachereason`/`boot golden`；C 只碰 `internal/team`、`internal/cachelab`、`internal/cli/team_*`。**唯一的共享文件是 `internal/agent/projection.go`**（A 加 receipt 字段、B 提取 `wireCall`/`wireMsg`）——见 §5.3。
上表的三方计数**不含**该文件；15 + 11 + 19 + 5 份文档 = 50 项。

---

## 2. 三方各自改变了什么（可复现）

### 2.1 A：把「压缩完成」从断言变成判据

| # | 结论 | 强度 | 复现 |
|---|---|---|---|
| A-1 | **一次维护决策的七态分类**：`below_boundary` / `above_boundary` / `at_ceiling` / `recovered` / `low_yield` / `rescued` / `blocked`，逐条对应计划 §3 职责 4 的七项。 | 证据 | `TestMaintenanceDecisionNamesEveryOutcome` |
| A-2 | **headroom 目标**：fold 必须留下 `≥ recentTailBudget()`（窗口 16%，受 `visible_window_tokens` 封顶）才算 `recovered`。**未达标不再报成功**。 | 证据 | `TestMaintenanceDecisionReportsHeadroomAndReduction` |
| A-3 | **同一视图的重复 summary 被抑制**：低收益 fold 锁存该视图，只有视图比锁存时增长 **≥ 窗口 5%** 才释放。overflow / ceiling / manual 三条路径**不受抑制**。 | 证据 | `TestLowYieldFoldLatchesInsteadOfRepayingTheSameView`、`TestLowYieldLatchReleasesOnGrowth`、`TestOverflowBypassesTheLowYieldLatch` |
| A-4 | **维护成本 telemetry**：`SummaryRequests` / `ProjectionInstalls` / `RescueCount` / `RepeatBlocks`，挂在会话级。 | 证据 | §11.2 的落盘链路（C 接的） |
| A-5 | **发现 HEAD 上的四个死字段**：`compactionProgress.stuck/consecutive/lastTurn` 中 `consecutive` 与 `lastTurn` **读点数为 0**（`git log -S` 定位到读点被 `9f59996e2` 删除）。**本轮未删**，留给协调 Agent。 | 证据 | A 文档 §4.1 |
| A-6 | **`rescueOverCeiling` 只在 `overflow` 或 `result >= hard` 时可达**——低于 ceiling 的失败只 `blocked`，不进 rescue。 | 证据 | `TestTheLadderOnlyReachesRescueFromTheCeiling`、`TestRescueIsTheLastRungAndCountsOnce` |

**A 未改动 provider-visible 字节**：receipt 字段与计数器全部 additive，`compactionStateSchemaCurrent` 仍是 V4，**不产生需要迁移的持久化状态**。

### 2.2 B：让「改写已读字节」这件事第一次可观测

**改动前**：客户端能观测的前缀只有 system + tools（`SystemHash`/`ToolsHash`/`StablePrefixHash`）与 session-context digest。**消息数组本身没有任何指纹**——一个非 fold 请求改写了已发出去的历史字节，本地诊断完全看不见。

| # | 结论 | 强度 | 复现 |
|---|---|---|---|
| B-1 | **消息数组指纹**：`MessageShape.Hash` / `Count` / 逐消息 digest；`CacheDiagnostics` 新增 `MessagePrefixHash` / `MessageCount` / `MessagesComparable` / `FirstDivergenceOffset` / `MessagesRewritten`。 | 证据 | `TestMessageShapeTellsAnAppendFromARewrite` 等 6 个 |
| B-2 | **归因规则**：只在**没有任何其他 reason** 能解释时才写入 `cachereason.Messages`；有认领者时沿用认领者的 reason。 | 证据 | `TestMessageShapeDefersToAReportedReason` |
| B-3 | **`MessagesRewritten` 与 `FirstDivergenceOffset` 无论有没有原因都发布**：原因回答「谁改的」，这两个字段回答「改掉了多少、从哪条开始」。 | 证据 | B 文档 §4 |
| B-4 | **四个离线 prefix benchmark 臂全绿**：成员切换（两条独立流）、后端重建（反向重注册工具后 wire 字节不变）、MCP 重注册（`toolsHash` 逐字节不变）、fold（4 次 fold → 恰好 4 次改写，之后立刻恢复 append-only）。 | 证据 | B 文档 §5 |
| B-5 | **配置继承守卫**：`agent.visible_window_tokens` 与 `agent.cache_aware_compaction` 曾被 `agent.New()` 静默丢弃。新增两条测试，**观测活 agent 的消费结果**而不是 options 结构体。 | 证据 | `TestMemberBackendInheritsTheCacheShapingKnobs`、`member_backend_inheritance_test.go` |
| B-6 | **`providerVisibleFingerprint` 输出逐字节不变**（boot golden 只有 `Messages` 一个空增量对象）。 | 证据 | `TestGoldenBaselineNoExtensions` |

### 2.3 C：把「哪些 miss 是本地造成的」变成可分区

| # | 结论 | 强度 | 复现 |
|---|---|---|---|
| C-1 | **`cachelab` 混合词表缺陷**：旧 `resolve()` 遇到「同一响应同时带 `input_tokens` 与 `prompt_tokens`」直接判 `unresolved_vocabulary`，**把一个确实带了 cache read 的响应读成「无 split」**。已改为按词表依次尝试。 | 证据 | `TestUsageParserRefusesAMixedVocabulary` 等 |
| C-2 | **显式零 vs 键缺失**：`cache_read_input_tokens: 0` 是**全 miss 的实测值**；键缺失才是「未报告」。 | 证据 | `TestUsageParserDistinguishesAMissingKeyFromAnExplicitZero` |
| C-3 | **`Recorder` 的 journal 写入竞争**：样本可见早于 journal 落行。**纯 HEAD 上 `-count=30` 可复现**。已改为先写后发布。 | 证据 | `TestRecorderRecordsExactRequestAndRawUsage`（HEAD 快照上复现） |
| C-4 | **未命中归因分区**（8 类，互斥可对账）：`cold_prefix` / `rewrite` / `structural` / `unexplained_prefix_change` / `messages_rewritten_unclaimed` / `append_only_expected` / `provider_residual_unexplained` / `undiagnosed`。 | 证据 | `TestCacheMissCauseIsAPartitionOfTheScopedPopulation` |
| C-5 | **会话身份维度**：`session_id_hash` / `session_ordinal` / `session_first_request_seq`，使 **Context Rescue 轮转后的首个请求**可被判为冷前缀（此前 `HasPrevRequest` 是写入者**进程级**的，轮转后首请求读起来是 warm）。 | 证据 | `TestCacheMissCauseReportsARotationAsCold` |
| C-6 | **冷前缀判定收紧**：最初「无前驱即判冷」会让**路由级账本行整批**读成「冷启动」。现在需要写入者自己的会话状态。 | 证据 | `TestCacheMissCauseDoesNotClaimARouteLevelRowWasCold` |
| C-7 | **条件矩阵登记**：Baseline / A-only / B-only / A+B / Rescue，每个带开关配方与门禁；Rescue 标 `NeverRoutine`，注册校验**拒绝**在其下跑请求字节变量臂。 | 证据 | `TestRescueConditionIsNotARoutineArm` |
| C-8 | **`K1-warm-fold` 回归守卫臂**：warm 响应的未命中余量是**两个事件之差**，任何单个事件里都没有这个数；折叠回归**静默**（`prompt == hit + miss` 在两种折叠下都闭合）。 | 证据 | C 文档 §4.2 |

---

## 3. 合流后才成立的两条（单独列出，因为它们是**跨边界**的）

### 3.1 B 的指纹 → C 的归因类：`messages_rewritten_unclaimed`

B 的 `CompareShape` 在「数组改写且无人认领」时**主动写入** `PrefixChangeReasons = ["messages"]`。
C 的归因分区把 `cachereason.Messages`（全词表**唯一不指向任何操作**的 rewrite 值）与其它 rewrite 值**分开**：

```
messages（无人认领）          → messages_rewritten_unclaimed
compact_auto / prune / truncate / rewind_* / guardian_merge（有人认领）→ rewrite
```

**为什么必须分开**：`rewrite` 的定义是「某个操作改写了前缀」，读者有权把它当作**已解释**略过；而「有人改写了已发出去的历史字节、且没有操作承认」正是计划 §7 F4 要找的东西。混为一类等于把 F4 藏起来。

### 3.2 A 的维护成本 → C 的报表：生产路径上真的被读到

链路：`agent.MaintenanceCost` →（`memberUsagePublisher` 持有 `control.SessionAPI`）→ `.usage.json` 的 `maintenance` 段 → 报表会话累计行。

**实测证据**（30 条会话）：

```text
maintenance spend: summary_requests=0 projection_installs=0 rescues=0 repeat_blocks=0 (published by 3 of 3 members)
```

**`published by 3 of 3 members` 是这条链路真正被打通的证据**；全零是**否定性证据**（这批会话没触发压缩），不是链路故障。

### 3.3 A 的 headroom 目标依赖 B 修的那个配置键

`GoalMet() = HeadroomTokens >= recentTailBudget()`，而 `recentTailBudget() = min(窗口 × 0.16, visible_window_tokens)`。

**B 修的正是 `visible_window_tokens` 被静默丢弃**。所以：**在任何设置了该 cap 的部署里，A 的 headroom 目标在 B 的修复之前是错的**（会退化成窗口 16%，比配置的 cap 大，于是 A 会把「本该算 recovered」的 fold 判成 `low_yield`）。两条改动**有顺序依赖**，不是独立的。

---

## 4. 合流后的真实数据（三方字段同时在场）

### 4.1 30 条 / 3 成员真实会话（`TestLiveTeamMemberCacheBaselineTeam`）

```text
coverage over 30 scoped samples: request_count measured=30 defaulted=0 unrecorded=0 unrecognized=0
  route_bucket=30/30 model_ref=30/30 usage_source=30/30 prefix_diagnostics=30/30   ← C
  session_identity=30/30                                                            ← C
  message_shape_comparable=30/30                                                    ← B
miss causes over 30 scoped samples (partition; append allowance 128 tok):           ← C
  cold_prefix            requests=3   eligible=3   miss=187909  (99% of scoped miss)
  append_only_expected   requests=27  eligible=27  miss=2299    (1% of scoped miss)
per logical turn (turns=30 requests=30 requests_without_turn=0)
  prompt/turn=65685 hit/turn=59345 miss/turn=6340 completion/turn=20 requests/turn=1
maintenance cost: rewrite=0 structural=0 rotations=0
  cold_start_miss_tokens=187909 (first session 187909, rotations 0)
session cumulative
  members=3 tokens: hit=1648000 miss=192850 token_weighted=89.5% member_simple_mean=90.1%
  maintenance spend: ... (published by 3 of 3 members)                              ← A
```

### 4.2 这些数字支持什么、不支持什么

**支持**（证据）：

1. **归因分区闭合**：`cold_prefix + append_only_expected == scoped`，**`provider_residual_unexplained == 0`**、`messages_rewritten_unclaimed == 0`。
2. **三方字段覆盖率 100%**：B 的数组指纹、C 的会话身份、A 的计数器在同一次会话里同时在场。
3. **append 豁免未被滥用**：27 条 append-only 样本合计 2,299 tok（均 85 tok/请求），**远小于 128 的允许量**——不是被允许量「收编」的残差。

**不支持**（必须与数字同读）：

1. **`cold_prefix` 吃 99% 是分母效应**：3 条冷启动 × 6.3 万 tok vs warm 每条 85 tok。这不是「冷启动是生产问题」，是「这个脚本的 warm 请求几乎不 miss」。
2. **`rewrite=0` / `rotations=0` / `summary_requests=0`**：这批数据**没有触发**压缩、没有触发 rescue。A 的抑制、B 的 fold 改写路径、C 的 F4 类**全部未被真实负载验证**。
3. **样本量不足**：桶级全部 `insufficient_sample`（`32k_128k` 20 请求 / 2 成员，未达 30/3）。
4. **任务质量与延迟不可测**：成员记录不含任务结果、不含请求时长。

### 4.3 `append_block_allowance = 128` 是整篇最脆的一环

它是 B 受控实验的**归纳值**（命中量恒为 128 的整数倍，未命中是追加内容向上取整到一个块），**不是文档化的 provider 常量**，且**未跨账号、未跨网关验证**。它决定了 `append_only_expected` 与 `provider_residual_unexplained` 的分界。

**当前数据对它有独立支持**：27 条 append-only 样本的 miss 均值 85 tok，**远低于** 128。若允许量偏大，这些样本本可以掩盖一个真实的 residual；实际没有。

---

## 5. 三方互相对不上的地方（**本文最该被攻击的一节**）

### 5.1 B 的一次归因错误（C 已更正）

| | |
|---|---|
| **B 的陈述** | `TestTeamTurnInjectsInboxAtSubmit` 在一次 cli 全量运行中失败，单独运行与二次全量均通过，**「判定为与 C 并行编辑中的测试顺序敏感，非本次改动」**。 |
| **C 的复现** | 该测试**在纯 HEAD 快照上同样失败**（`-count=30` → 28/30 通过）。与任何 Part 的改动**无关**，是**既有 flake**。 |
| **机制** | 只在 `-count>1` 下出现 → 是**跨迭代状态**，不是跨测试顺序。断言 `cb.sent` 长度（「已确认的批次不得注入两次」）。 |
| **影响** | B 的**结论**（非本次改动）正确，**理由**错误。评审者若据此认为「C 的编辑引入了顺序敏感」，会得出相反的行动项。 |

**待攻击点**：是否还有其它被归因给「并行编辑」的红项？C 用同一方法（纯 HEAD 快照 + `-count=N`）验证过 `cachelab` 的 journal 竞争，那一条**确实是既有 flake**（C 已修）。

### 5.2 A 与计划 M1 的两处偏离（A 已登记，**需协调 Agent 裁定**）

| 计划 M1 字段 | A 的实现 | A 的理由 |
|---|---|---|
| `summary_requests`（在 receipt 上） | 改为**会话级累计** | 一次决策可跑多次 summary（梯子最多 4 次）；receipt 是**决策**记录而非**调用**记录 |
| `rescue_planned`（在 receipt 上） | **不放** | rescue 的 plan 通过 `ContextRescueRequired` 错误携带，复制进 receipt 会产生第二个真相来源 |

**C 的消费侧判定**：两处都**不构成冲突**。C 的 `turn_cost` 是逐 turn、A 的是会话级，回答不同问题；C 的 `rotations` 从会话身份变化数出来，不依赖任何 plan。**但计划 §6.2 的 `compaction_requests_per_turn` 因此只能给「会话总计 / turn 数」的粗粒度**（C 文档 L-10）。

### 5.3 唯一的共享文件：`internal/agent/projection.go`

A 加 receipt 字段、B 提取 `wireCall`/`wireMsg`。A 在文档 §8 L-7 如实记录：「我无法把该 hunk 从工作树摘出（会连带破坏 B 的文件）」，其门禁在**手工重建的隔离版本**上跑。

**C 的验证**：合流树 `go build ./...` 通过，`internal/agent` 全绿（48.9s），`providerVisibleFingerprint` 的 boot golden **逐字节不变**。**该文件在合流态下是自洽的**——但 **A 的那次门禁确实没有跑在最终字节上**，这是一个应当被复核的点。

### 5.4 B 的 5 个新诊断字段**不进 eventwire**

B 明确移交协调 Agent（其 §7）：要让 desktop/ACP 看到其中 4 个新增消息诊断字段，需在 `CacheDiagnostics` wire 结构与 `ToWireCacheDiagnostics` 各加 4 项，**B 未改**。第 5 个新增字段是 `PrefixChangeReasons` 的新枚举值 `messages`，不对应一个独立 wire 字段。

**当前后果**：这些字段只存在于**进程内事件**上。成员观测走的是进程内路径，所以 C 的生产证据链是通的；**但任何跨进程前端都看不到它们**。这是有意的范围控制，不是遗漏。

### 5.5 C 自己踩到并修正的一个坑（同类教训，供交叉分析者警惕）

C 第一版把 `messages_rewritten_unclaimed` 挂在「`MessagesRewritten > 0` 且 **reasons 为空**」上。**单元测试绿了**，但该形状**生产者从不发出**（B 会写入 `["messages"]`），所以该类**在真实数据上不可达**。

修正后判据改为按 reason 值分流，测试改为断言**生产形状**，并反向验证：去掉这条 split，测试立即变红。

**教训**：断言了**合成边界**而不是**被消费的边界**。检查你自己的结论时，先问「生产者真的会发出这个形状吗」。

---

## 6. 未验证清单（三方一致）

| 项 | 状态 | 谁需要它 |
|---|---|---|
| 真实 Provider 上**重复压缩是否真的下降** | **未验证**（只有单元测试 + 本地夹具） | A 文档 L-5 |
| 真实负载上**fold 是否真的只产生一次可解释改写** | **未验证**（`rewrite=0`） | B 文档 §5 的 fold 臂是离线的 |
| **Context Rescue 轮转**路径 | **未验证**（`rotations=0`，只有单元测试） | C 文档 §11.5 L-12 |
| **F4（无人认领改写）** | **未验证**（`messages_rewritten_unclaimed=0`） | 同上 |
| **1M 上下文桶** | **未验证**（本机测试最大到 `128k_256k`） | 计划 §6.2 |
| **并发** | **未验证**（全部串行单请求） | 计划 §5 P3 |
| **跨账号 / 跨网关** | **未验证**（单网关、单账号） | 计划 §6.2「Provider 隔离」 |
| **任务质量 / 延迟护栏** | **本数据集不可测** | C 文档 §3.4 |
| **条件矩阵（Baseline/A-only/B-only/A+B/Rescue）** | **已登记，从未执行** | C 文档 §9.3 |
| `desktop/` 模块 | **未验证**（A 未跑；其 `TestHostContractGeneratedFilesAreCurrent` 在 HEAD 上即红） | A 文档 §7 |
| `golangci-lint` | **未执行**（本机未安装；CI pin 是 2.12.2） | B 文档 §9 |

---

## 7. 合流门禁与决策

### 7.1 门禁（合流后的完整树）

| 检查 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| 全量测试 | **12 个测试目标通过**（agent 48.9s / cli 78.8s / control 84.2s / boot 21.1s / team / cachelab / session 50.6s / stats / provider×3 / event / eventwire / cachereason）；既有 `cli` flake 另列，不计入本次合流门禁 |
| `go vet` | 通过 |
| `gofmt`（全部改动文件） | 0 未格式化 |
| `scripts/cache-guard.sh` | 10/10 |
| `go run ./tools/repolint` | **RED SET 与 HEAD 逐字节相同** |
| 既有红项（非本轮引入） | `chat_tui_team_*`/`team_history_sync`/`team_replay`/`team_task_service`/`messages_usage` 的 essay/file-size 超预算（carry-forward）；`desktop` host-contract；以及 **§5.1 的 cli inbox flake** |

### 7.2 计划 §6.2 通过门槛的逐条状态

| 门槛 | 状态 | 缺口 |
|---|---|---|
| 计量正确性 | ✅ | 三方表驱动测试已就位 |
| Provider 隔离 | ⚠️ | 仍只有一个网关、一个账号 |
| Team 可追溯性 | ✅ | 覆盖率三项 30/30 |
| 实验可比性 | ❌ | **条件矩阵从未执行** |
| 样本充足性 | ❌ | 桶级全部 `insufficient_sample` |
| 历史结论边界 | ✅ | 三方都未触碰历史账本 |
| **优化准入** | ❌ | **无真实 Team 层内复现、无质量/延迟证据** |

### 7.3 决策

**有限通过 / 继续采样。行为优化不放行。**

**灰度（计划 §5 P4）不得启动**——四项前提一项未满足：正式条件样本、`miss_tokens_per_request` 跨三次独立运行方向一致、质量与延迟的替代证据、rescue 轮转至少被真实观测一次。

### 7.4 回滚（三方各自的独立开关）

| 变更 | 回滚方式 | 依赖 |
|---|---|---|
| **A R-1** 低收益锁存 | `latchLowYield(decision)` → `resetCompactionProgress()`，删 `releaseMaintenanceLatch` 调用点 | **R-1 与 R-2 必须一起回滚** |
| **A R-2** headroom 判据 | `GoalMet()` 恒返回 `true` | 同上 |
| **A R-3** 观测字段与计数器 | 删字段与调用 | 纯增量 |
| **B** `messages` reason | 删 `CompareShape` 里追加 `cachereason.Messages` 的 4 行 | 两个偏移字段可保留（无人读时无副作用） |
| **C** 归因分区 / 会话身份 | 删 `cachemisscause.go` / `cachereport_turn.go` + 移除两处调用 | 报表少两个 JSON 段 |
| **C** cachelab 解析与发布顺序 | 还原 `resolve()` 与 `finishSample` 尾部 | 夹具退化为旧行为 |

**A 的两条必须同回**；其余各自独立。**没有一条需要迁移或清理持久化状态**。

---

## 8. 复现清单（交叉分析者用）

```bash
# 全量（零成本）
go build ./...
go test ./internal/agent/ ./internal/cli/ ./internal/control/ ./internal/boot/ \
        ./internal/team/ ./internal/cachelab/ ./internal/session/ ./internal/stats/ \
        ./internal/provider/... ./internal/event/ ./internal/eventwire/ ./internal/cachereason/ -count=1
go vet ./internal/agent/ ./internal/control/ ./internal/team/ ./internal/cachelab/ ./internal/cli/
go run ./tools/repolint && bash scripts/cache-guard.sh

# A 的契约
go test ./internal/agent/ -run 'TestLowYield|TestOverflowBypasses|TestMaintenanceDecision|TestTheLadderOnly|TestRescueIsTheLast' -v
# B 的契约
go test ./internal/agent/ -run 'TestMessageShape|TestMemberCachePrefixBenchmark|TestDeferredMCPTail' -v
go test ./internal/cli/ -run TestMemberBackendInheritsTheCacheShapingKnobs -v
# C 的契约
go test ./internal/cachelab/ -run 'TestUsageParser|TestCondition|TestRecorderRecordsExactRequestAndRawUsage' -v
go test ./internal/team/ -run 'TestCacheMissCause|TestCacheTurnCost' -v

# 既有 flake 验证（§5.1）：应在纯 HEAD 上同样失败
go test ./internal/cli/ -run TestTeamTurnInjectsInboxAtSubmit -count=30

# 真实 Provider（需凭证，-tags live）
go test -tags live ./internal/cli/ -run 'TestLiveTeamMemberCacheSession$' -v -count=1 -timeout 20m
go test -tags live ./internal/cli/ -run 'TestLiveTeamMemberCacheBaselineTeam$' -v -count=1 -timeout 25m
```

**当前仍未提交、未推送、未开 PR。** 35 个已跟踪文件改动 + 15 个新文件（含本文）全部在工作树；因此下述“独立可复现”应理解为“在当前工作树按命令复现”，不是不可变构建产物上的审计结论。

---

## 9. 结论强度总表

| # | 结论 | 强度 | 独立可复现 |
|---|---|---|---|
| 1 | A：七态决策分类覆盖计划 §3 职责 4 的全部七项 | **证据** | ✅ 零成本 |
| 2 | A：headroom 目标使「压缩成功」可复算，未达标不报成功 | **证据** | ✅ 零成本 |
| 3 | A：同一视图的低收益 fold 不再重复付费（overflow/ceiling/manual 除外） | **证据（单元）** | ✅ 零成本 |
| 4 | B：消息数组指纹区分「追加」与「改写已读字节」 | **证据** | ✅ 零成本 |
| 5 | B：`messages` reason 只在无人认领时写入 | **证据** | ✅ 零成本 |
| 6 | B：四个 prefix benchmark 臂在离线夹具下全绿 | **证据（离线）** | ✅ 零成本 |
| 7 | C：混合 usage 词表曾被读成「无 split」 | **证据** | ✅ 零成本 |
| 8 | C：未命中归因分区互斥且可对账 | **证据** | ✅ 零成本 |
| 9 | C：合流会话上 `provider_residual_unexplained == 0` | **证据（受控会话）** | ✅ 需凭证；依赖 128 token 豁免假设 |
| 10 | **A/B/C 的任何行为改动在真实 Provider 上有效** | **未验证** | — |
| 11 | **`append_block_allowance = 128` 是 provider 常量** | **未证实（单网关归纳参数）** | ⚠️ 未跨账号、网关或并发验证 |
| 12 | **A 的 headroom 目标「16% 窗口」正确** | **自洽性论证** | ⚠️ 非测量 |
| 13 | **跌幅的剩余来源** | **未决** | — |

---

## 10. 本文作者建议的交叉分析方向

1. **攻击 §4.3 的 128**：在 768K–1M 桶、跨账号、并发条件下重测块粒度。若 128 不成立，`append_only_expected` 与 `provider_residual_unexplained` 的分界需要重划，§4.2 的「闭合」随之失效。
2. **攻击 §3.3 的顺序依赖**：在设置了 `visible_window_tokens` 的部署上，**单独回滚 B 而保留 A**，观察 `low_yield` 是否虚增。这是两条改动「有顺序依赖」这一论断的直接检验。
3. **攻复现 §5.1**：把「归因给并行编辑」的每一处红项都在纯 HEAD 快照上验证一遍。当前只验证了两条，其中一条（inbox flake）**归因是错的**。
4. **攻 §5.2 的两处偏离**：`summary_requests` 放会话级是否真的优于 receipt？计划 §6.2 要 `compaction_requests_per_turn`，而 A 的实现只能给粗粒度——这个代价是否被评估过？
5. **攻 §5.5 那一类错误**：C 的 `messages_rewritten_unclaimed` 曾经不可达。检查 A 与 B 的契约测试是否也存在「断言合成形状」的问题——特别是 B 的四个 benchmark 臂，它们断言的是「声明了重建/重注册/fold 的臂必须真的发生过该事件」（B 文档 §5 已自我防护），但 A 的七态分类中 `low_yield` 与 `recovered` 的分界只有构造值测试。
6. **执行条件矩阵**：`C-*` 臂已登记且带门禁，**从未跑过**。这是从「观测可用」到「行为有证据」的唯一路径。

---

## 11. 最终收口与后续放行门槛

本轮可以收口的结论只有三层：

1. **观测链路已验证**：A 的维护计数、B 的消息形状诊断和 C 的会话/归因字段能够在受控成员会话中同时落盘；
2. **分类逻辑在受控样本上成立**：归因类别可以互斥对账，`provider_residual_unexplained == 0` 仅对当前样本、当前归因规则和 128 token 豁免假设成立；
3. **行为优化有效性仍未验证**：不能据此声称重复压缩下降、前缀改写减少、命中率提升、成本下降或任务质量不变。

在形成灰度或优化准入结论前，必须完成以下事项：

- 发布不可变合流快照，并在该快照上重跑 build、test、vet、repolint 和 cache guard；
- 执行 Baseline / A-only / B-only / A+B 条件矩阵，每个实验臂满足预先冻结的有效 warm 样本量，并至少进行三次独立运行；
- 在至少两个账号、两个网关或 route、多个上下文桶下验证 `append_block_allowance`，报告分布、分位数和超阈值样本，而非只报告均值；
- 同时报告 miss tokens/request、总输入 tokens、summary requests/turn、projection rewrite、p50/p90 latency、任务完成率、必要上下文保留和工具调用正确率；
- 明确解决 M1 的粒度偏离：要么补充逐 turn 的维护调用计数，要么由协调者书面接受“会话总量 / turn 数”的限制；
- 若需要 desktop/ACP 消费消息诊断，补齐 eventwire 的 4 个字段并增加跨进程 golden；
- 完成后再由协调者作 go/no-go 决策；在此之前保持“继续采样，行为优化不放行”。
