# Part A RESULT：维护状态机与压缩成本契约核验

> 执行依据：`TEAM_MEMBER_CACHE_ONE_SHOT_3AGENT_EXECUTION_PLAN.zh-CN.md` §3「Part A」、§2.3「统一产物格式」。
> **注意**：ONE_SHOT 方案与 `TEAM_MEMBER_CACHE_FINAL_PARALLEL_EXECUTION_PLAN.zh-CN.md` 的 Part A **逐字相同**；
> 本文中形如「计划 §6.2」的引用指向 **`TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md` §6.2（建议判定指标）**，
> 即 ONE_SHOT 方案所继承的原始契约。
> 日期：2026-09-25。**状态：`CONDITIONAL`**。
> 快照：`2c0f1dc61`（Team-agent）经 `git archive HEAD` 提取的**不可变快照**；
> 内容哈希 `9e2b9623784d74cdf2d69581277553d96eb0368c30db741c1d6e49c8c38fcf24`。
> 本报告**只核验 A 侧**；不判断真实 Provider 命中率是否改善（计划 A.1）。

---

## 0. 结论

| # | 核验项 | 判定 | 依据 |
|---|---|---|---|
| A-1 | 七态决策覆盖 | **PASS（含一处文档缺口）** | §2 |
| A-2 | headroom 目标与 `visible_window_tokens` cap | **PASS** | §3 |
| A-3 | low-yield latch 的锁存 / 释放 / overflow bypass | **PASS** | §4 |
| A-4 | maintenance ladder 与 rescue 最后一级约束 | **PASS** | §5 |
| A-5 | maintenance cost 四项计数器 | **PASS（粒度偏离未获书面接受）** | §6 |
| A-6 | M1 粒度偏离的接受意见 | **CONDITIONAL** | §6.2 |
| A-7 | generation / turn 重复调用检查 | **PASS（有界重复，且为既有行为）** | §7 |
| A-8 | additive 字段不改 provider-visible 字节与持久化 schema | **PASS（证据有上限）** | §8 |
| A-9 | `low_yield` / `recovered` 边界攻击 | **PASS（发现 2 处 fail-open 语义）** | §9 |
| A-10 | `compactionProgress` 死字段清理建议 | **已给出建议，本轮不实施** | §10 |

**总体：`CONDITIONAL`** —— 唯一阻塞项是 **A-6**：计划 M1 的两处粒度偏离**没有协调者的书面接受**，
而计划 A.5 要求「telemetry 粒度限制已被记录，且不会被误报为逐 turn 精确值」。
A 侧自己**无权自接受**（A 文档 §6 与联合结论 §5.2 都把它标为「需协调 Agent 裁定」）。

**A 侧没有发现行为缺陷**：A-9 的两处 fail-open 是**已记录的设计**（A 文档 §3.3、L-1），
A-7 的有界重复是**既有行为且 A 未加重**。

---

## 1. 快照与门禁

| 项 | 值 |
|---|---|
| 基线 commit | `2c0f1dc61da07a7919c71ba3adbb90bc4522fe2a`（Step 4） |
| 快照方式 | `git archive HEAD \| tar -x -C /tmp/oneshot-A`（**不含工作树未提交改动**） |
| 内容哈希 | `9e2b9623784d74cdf2d69581277553d96eb0368c30db741c1d6e49c8c38fcf24` |
| 工作树状态 | **干净**：`git status --porcelain` 只有 3 份文档（2 未跟踪 + 1 改标题），**无代码改动** |

**因此本报告是「独立构建验证」，不是「当前工作树证据」**（计划 §2.1 的区分）。

| 门禁 | 结果 | 原始输出 |
|---|---|---|
| `go build ./...` | **通过** | `/tmp/oneshot-A-artifacts/build.txt` |
| `go vet ./internal/agent/` | **通过** | 同上 |
| `gofmt -l internal/agent/` | **空** | 同上 |
| `go test ./internal/agent/` | **ok**（62.3s） | §2–§9 各节 |
| `go test ./internal/boot/` | **ok**（23.9s） | 同上 |
| `go test ./internal/control/` | **ok**（99.4s） | 同上 |
| `bash scripts/cache-guard.sh` | **10/10** | `/tmp/oneshot-A-artifacts/cache-guard.txt` |
| `go run ./tools/repolint` | RED SET 与 `2c0f1dc61` **逐字节相同** | `/tmp/oneshot-A-artifacts/repolint.txt` |

---

## 2. A-1：七态决策覆盖（计划 A.2.1 第 1 项）

### 2.1 定向测试

`TestMaintenanceDecisionNamesEveryOutcome` 用一张表钉住七个状态。**全部 PASS。**

```
--- PASS: TestMaintenanceDecisionNamesEveryOutcome (0.00s)
--- PASS: TestMaintenanceDecisionReportsHeadroomAndReduction (0.00s)
```

### 2.2 状态与判据（`maintenanceDecision.State()`，顺序即优先级）

| 序 | 条件 | 状态 |
|---|---|---|
| 1 | `Rescued` | `rescued` |
| 2 | `Blocked` | `blocked` |
| 3 | `Applied && GoalMet()` | `recovered` |
| 4 | `Applied` | `low_yield` |
| 5 | `Hard > 0 && Estimate >= Hard` | `at_ceiling` |
| 6 | `Fold > 0 && Estimate >= Fold` | `above_boundary` |
| 7 | 其他 | `below_boundary` |

### 2.3 **发现：7 个状态中只有 3 个能到达已发布的 receipt**

计划 A.5 要求「**所有状态均有可达性或明确不可达理由**」。核验结果是**前一半不成立、后一半未写明**。

**证据**：`maintenanceDecision.State()` 在生产中只有三个构造点（`grep -rn '\.State()' internal/ --include=*.go | grep -v _test`）：

| 构造点 | 设置 | 可产出的状态 |
|---|---|---|
| `compact_commit.go:130`（summary fold） | `maintenanceDecisionFor(...)` → `Applied: true` | `recovered` / `low_yield` |
| `maintenance_commit.go:62`（prune / truncate） | 同上 | `recovered` / `low_yield` |
| `context_receipt.go:166`（blocked / failed） | `Blocked: status != "applied"`，而 `status` 在该函数内**恒为** `blocked` 或 `failed` | **只有** `blocked` |

**不可达的 4 个状态及其理由**（本轮核验得出，此前**未记录**）：

| 状态 | 为何不可达 |
|---|---|
| `below_boundary` | 需要 `Applied=false && Blocked=false && Estimate < Fold`。三个构造点要么置 `Applied=true`，要么置 `Blocked=true` |
| `above_boundary` | 同上 |
| `at_ceiling` | 同上 |
| `rescued` | 需要 `Rescued=true`。**`grep -rn 'Rescued:' --include=*.go internal/ \| grep -v _test` 无命中** |

**`rescued` 的不可达性已端到端验证**：`rescueOverCeiling` 在认证成功时直接调
`noteMaintenanceDecision(maintenanceStateRescued)`（`context_rescue.go:256`）计数，
**但不写任何 receipt**——它返回 `&ContextRescueRequired{Plan: plan}` 作为错误。
因此 `rescue_count` 会增长，而 `maintenance_state` 永远不出现 `rescued`。

实测（强制 rescue 路径）：

```
forced: err=context exceeds provider limit and compaction failed: truncated view still 250032 >= 39744
        receipt_state=low_yield summaries=1 rescueCount=0
```

**判定**：这是**文档缺口，不是行为缺陷**。`maintenanceDecision` 是值类型，其完整取值域由单元测试覆盖；
receipt 是它的一个投影。但计划 A.5 的字面要求是「可达性**或**不可达理由」，
而这两者目前**都没有被写下来**。

**建议**：在 `context_headroom.go` 的 `State()` 上补一段注释，或在 A 文档 §2.3 补一列
「receipt 可达性」，说明 4 个状态是**分类器取值域的成员、不是 receipt 的取值**。
**本轮不实施**（计划 A.2.6 的同一条纪律：不顺手扩大代码范围）。

---

## 3. A-2：headroom 目标与 `visible_window_tokens` cap

### 3.1 定义（已核验）

```go
HeadroomTokens = Fold − Result            // 可为负，不 clamp
Goal           = recentTailBudget() = min(window × 0.16, visible_window_tokens)
GoalMet()      = Fold <= 0 || Goal <= 0 || HeadroomTokens >= Goal
```

### 3.2 定向测试

```
--- PASS: TestRecentTailBudgetIsFixedSixteenPercent (0.00s)
--- PASS: TestMaintenanceDecisionReportsHeadroomAndReduction (0.00s)
```

### 3.3 实测的持久化证据

真实持久化一个 low-yield fold 的 receipt 并读回：

```
persisted schema_version=4
receipt keys carrying A's fields:
  headroom_tokens=2801  fold_trigger_tokens=25000  hard_ceiling_tokens=49744
  reduction_ratio=0.40175708087422857  maintenance_state=low_yield
reloaded schema_version=4 state="low_yield"
```

**四个数可复算**：`headroom = fold_trigger − result = 25000 − 22199 = 2801` ✓，
`reduction = (input − result) / input` ✓。判据不是只能相信 `maintenance_state` 的字面值。

### 3.4 `visible_window_tokens` cap（与 B 的接口）

`recentTailBudget()` 取 `min(窗口 × 0.16, VisibleWindowTokens)`。
**B 修复的正是 `visible_window_tokens` 被 `agent.New()` 静默丢弃**——所以两条改动**有顺序依赖**：
在设置了该 cap 的部署里，B 修复之前 A 的 headroom 目标偏大，会把本该 `recovered` 的 fold 判成 `low_yield`。
**这一条不在 A 的写集内，本报告只登记，不验证**（B 侧负责）。

---

## 4. A-3：low-yield latch（计划 A.2.1 第 3 项）

### 4.1 定向测试

```
--- PASS: TestLowYieldFoldLatchesInsteadOfRepayingTheSameView (0.05s)
--- PASS: TestLowYieldLatchReleasesOnGrowth (0.09s)
--- PASS: TestOverflowBypassesTheLowYieldLatch (0.14s)
```

### 4.2 三条路径的实测行为（本轮独立复跑，非只读断言）

| 场景 | 调用次数 | summary 次数 | 结论 |
|---|---|---|---|
| 已锁存、视图**仍高于触发边界**、未增长过释放阈值 | 6 | **1** | 重复被抑制 |
| 已 blocked（receipt backoff）、视图高于触发边界 | 10 | **1** | 重复被抑制 |
| overflow 触发 | 1 | **≥2**（绕过） | 物理恢复不被抑制 |

**这三条正是计划 A.5「未发现同一 view 的无界重复维护」的直接证据。**

### 4.3 释放条件的实现细节

`releaseMaintenanceLatch` 用 `maintenanceGrowthDue(stuckTokens, est)`，
复用既有常量 `maintenanceRetryGrowthRatio = 0.05`——与 `maintenanceRetryDue`（失败回执的同类规则）
**同一条规则**，不是新引入的阈值。核验：`grep -n maintenanceRetryGrowthRatio internal/agent/*.go`
只在该常量定义处与这两处调用。

---

## 5. A-4：ladder 与 rescue 最后一级

```
--- PASS: TestTheLadderOnlyReachesRescueFromTheCeiling (0.03s)
--- PASS: TestRescueIsTheLastRungAndCountsOnce (0.04s)
```

核验要点（代码级）：

- `rescueOverCeiling` 的两个可达条件：`policy.Trigger == CompactionTriggerOverflow` **或**
  `hard > 0 && result.InputTokens >= hard`（`context_manager.go:269-271`）。
- `!policy.AllowContextRescue` 时走 `rescueByTruncation`——**未 opt-in 的构建不会轮转 session**。
- 认证成功计 1 次 rescue、**装 0 个投影**（`noteMaintenanceDecision` 只加计数器，不写 receipt）。

---

## 6. A-5 / A-6：maintenance cost 四项计数器与 M1 粒度偏离

### 6.1 计数器

```
--- PASS: TestMaintenanceCostCountsEverySummaryPath (0.01s)
```

| 计数器 | 计数点 | 核验 |
|---|---|---|
| `SummaryRequests` | `runSummaryRequest` 入口 | ✅ 四条 summary 通道（fold / transcript / fragment / rescue）全经过 |
| `ProjectionInstalls` | `commitSummaryProjection` / `installMaintenanceProjection` | ✅ 只计真实安装，receipt 重发不计 |
| `RescueCount` | `rescueOverCeiling` 认证成功 | ✅ 实测：强制 rescue 路径 `rescueCount` 会增长 |
| `RepeatBlocks` | `recordContextMaintenanceOutcome` 持久化成功后 | ✅ 实测：`repeatBlocks=1` 在 blocked 场景出现 |

### 6.2 **A-6 判定：`CONDITIONAL`**

计划 A.2.2 要求「确认偏离**是否已经由协调者书面接受**」。

**核验结果：没有。** 全仓库检索（`grep -rn "summary_requests\|rescue_planned" docs/team-mcp-port/*.md`）：

| 文档 | 记载 |
|---|---|
| A 文档 §6 | 「**两处与计划的偏离，需协调 Agent 裁定**」 |
| 联合结论 §5.2 | 「A 已登记，**需协调 Agent 裁定**」 |
| 联合结论 §8 待办 | 「**明确解决 M1 的粒度偏离**：要么补充逐 turn 的维护调用计数，要么由协调者书面接受」 |

**没有任何一份文档记录协调者的接受。** 因此按计划 A.5 的第 5 条，A 侧**不能标记 `PASS`**。

**偏离的实际代价（本轮量化）**：

根因方案 §6.2 要 `compaction_requests_per_turn`（逐 turn）。A 的计数器是**会话累计**，
C 的 `turn_cost` 提供 turn 分母，两者相除只能得到「会话总量 / 会话 turn 数」的**粗粒度**，
**不能**给出「第 N 个 turn 花了多少次 summary」。联合结论已把它记为 L-10。

**A 侧的意见（供协调者裁定，不构成接受）**：

1. **接受是可行的**：`summary_requests` 的「决策 vs 调用」区分是真实的（一次决策最多 4 次调用），
   而 receipt 是**决策**记录。把它放到 receipt 上要么丢信息、要么改变 receipt 粒度。
2. **但「会话总量 / turn 数」不足以支撑根因方案 §6.2 的判定**：该指标要用于「重复压缩是否显著下降」，
   而粗粒度会把「一个 turn 集中压缩」与「多个 turn 各压一次」混为一谈。
3. **补逐 turn 计数的成本**：需要一个 per-turn 的 `atomic.Int64` 槽 + turn 边界重置，
   约 20 行，且**不改变任何行为**（纯观测）。**建议协调者在两者中明确选一个**。

---

## 7. A-7：generation / turn 重复调用检查（计划 A.2.3）

### 7.1 生产调用链（只读还原）

```
turn loop
 └ prepareSamplingRequest
    ├ [1] buildSamplingRequest(pressure)      → Prepare(pressure)
    ├ [2] applyAdmissionToRequest 失败
    │      → Prepare(overflow, Force)          ← 一次性物理恢复
    └ [3] buildSamplingRequest(pressure)      → Prepare(pressure)   ← 重建
```

### 7.2 实测：一个 turn 最多付几次 summary

用**计数 provider** 驱动这条真实链（`foldableSessionOverForce(40)`）：

```
[1] pressure stream calls=1
[2] overflow stream calls=2
[3] pressure stream calls=2
POST-A TOTAL one turn: 2
```

**同一链在 `HEAD~1`（A 的改动之前）上：**

```
[1] pressure stream calls=1
[2] overflow stream calls=2
[3] pressure stream calls=2
PRE-A TOTAL one turn: 2
```

**结论：有界重复（每 turn 2 次）是既有行为，A 未加重也未减轻。**

### 7.3 三道守卫（代码级核验）

| 守卫 | 位置 | 作用 |
|---|---|---|
| 单飞锁 | `context_manager.go:102` `compactionRunMu.Lock()` | 同一时刻只有一个 Prepare 事务 |
| receipt backoff | `context_manager.go:146` | `status=blocked/failed` 且 `est < hard` 时拒绝重试；**manual 与 overflow 豁免** |
| low-yield latch（A 新增） | `context_manager.go:154` | `stuck && pressure && est < hard` 时拒绝 |

**第 3 次调用（重建的 pressure）被哪一道挡住？** 实测中它**没有折叠**（summaries 停在 2），
而当时 `est=22561 >= fold=20000`——**所以挡住它的是 receipt backoff，不是阈值**。

**判定：`PASS`。** 同一 view 的重复是有界的（每 turn ≤ 2 次 summary + 1 次 overflow 恢复），
且 A 的改动**只收紧了释放条件**（`stuckInputHash` 变化即解锁 → 必须增长 ≥ 窗口 5%）。

---

## 8. A-8：additive 字段不改 provider-visible 字节与 schema（计划 A.2.4）

### 8.1 证据

| 检查 | 结果 |
|---|---|
| `compactionStateSchemaCurrent` | **V4**，A **未改动该常量**（`git show 2c0f1dc61 -- internal/agent/projection.go` 无 schema 行） |
| receipt 新增字段 | 全部 `omitempty`：`headroom_tokens` / `fold_trigger_tokens` / `hard_ceiling_tokens` / `reduction_ratio` / `maintenance_state` |
| 持久化 → 读回 | **通过**（§3.3） |
| **旧 sidecar 读入** | 新增字段解码为零值；由它构造的 decision `Fold=0` → `GoalMet()` **fail-open** → 行为等同 HEAD 的「任何 applied fold 都算成功」 |
| provider-visible golden | `TestGoldenBaselineNoExtensions` / `TestGoldenBaselineContractSanity` **PASS** |
| 旧 sidecar 兼容性测试 | `TestLegacyUsageDocumentStillReads` 等 **PASS** |

### 8.2 **证据上限（必须随结论引用）**

1. **golden 证明的是指纹不变，不是「所有 provider 的 wire 字节不变」**。
   `prefix_shape.json` 覆盖 system/tools/prefix/token 计数与 B 新增的 `Messages` 增量对象；
   它**不覆盖**每个 provider 适配器的实际序列化。
2. **`projection.go` 的 diff 混有两个 Agent 的改动**（A 的 receipt 字段 + B 的 `wireCall`/`wireMsg` 提取）。
   A 文档 L-7 已如实记录「我无法把该 hunk 从工作树摘出」。本报告在**合流后的完整文件**上验证：
   `go build` 通过、`internal/agent` 全绿、golden PASS——**该文件在合流态下自洽**，
   但 **A 的那次隔离门禁确实没有跑在最终字节上**。
3. **schema 的「不改」是常量未变 + 字段 `omitempty`**，不是对全部历史 sidecar 的穷举回归。

**判定：`PASS`，附上述上限。**

---

## 9. A-9：`low_yield` / `recovered` 边界攻击（计划 A.2.5）

计划明确要求「**避免只测试理想构造值**」。本轮用 9 个边界构造攻击（发布表只有 7 个理想值）。

| 构造 | 结果 | 评估 |
|---|---|---|
| `headroom == goal`（**恰好在线**） | `recovered` | ✅ 边界含端点，符合 `>=` |
| `headroom == goal − 1`（**差一**） | `low_yield` | ✅ 边界不含外侧 |
| `goal == 0` | `recovered`，**headroom = −2000（负）** | ⚠️ fail-open |
| `fold == 0` | `recovered`，headroom = 0 | ⚠️ fail-open |
| `result > fold`（负 headroom） | `low_yield` | ✅ 不 clamp 成 0 |
| `applied && above ceiling` | `low_yield` | ✅ `Applied` 优先于 ceiling 分支 |
| `applied && below trigger` | `recovered` | ✅ |
| `blocked && rescued` | `rescued` | ✅ 顺序符合注释 |
| 全零 | `below_boundary` | ✅ |

### 9.1 两处 fail-open 的评估

`GoalMet()` 在 `Fold <= 0 || Goal <= 0` 时返回 `true`。攻击显示：
**一个落在触发边界之上 2000 token 的 fold，会被报成 `recovered`。**

**这是缺陷吗？——不是，但需要看清它的可达性：**

| 量 | 能否为 0 | 依据 |
|---|---|---|
| `Goal` | **不能**（`recentTailBudget()` 在窗口未知时返回 **1**，不是 0） | `compact.go:152-160` |
| `Fold` | **能**（`compactTrigger()` 在窗口未知时返回 **0**） | `compact.go:99-103` |

且 `prepareOnce` 在 `contextWindow <= 0` 时对**非 manual** 提前返回（`context_manager.go:133`）。
**所以 `Fold==0` 的 receipt 只在「manual compact + 窗口未知」下可达。**

**A 文档 §3.3 与 L-1 已如实记录该 fail-open 及其代价**（「窗口未知的会话拿不到低收益抑制」）。
本轮核验确认记录属实，且**可达范围比文档描述的更窄**（只有 manual）。

**判定：`PASS`。** 边界行为正确、fail-open 已记录、可达范围已量化。

---

## 10. A-10：`compactionProgress` 死字段（计划 A.2.6）

### 10.1 复核结果（与 A 文档 §4.1 一致，本轮独立确认）

在冻结快照上统计**生产代码**的读写（排除 `_test.go`）：

| 字段 | 写 | 读 | 判定 |
|---|---|---|---|
| `stuck` | 23 | 1（`context_manager.go:154`） | **活** |
| `stuckTokens` | 7 | 1（`context_headroom.go:180`，A 新增） | **活** |
| `failedTurn` | 6 | 1（`context_receipt.go:54`） | **活** |
| `stuckInputHash` | 8 | 1（`context_headroom.go:177`） | **活** |
| `consecutive` | 7 | **0** | **死** |
| `lastTurn` | 4 | **0** | **死** |

**A 文档 §4.1 的结论正确**（`consecutive` / `lastTurn` 读点数为 0）。

### 10.2 `lastTurn` 的注释与实现已经脱节

```go
// lastTurn stops the post-turn observer and the pre-send preflight from
// paying for two summaries during one active tool loop.
lastTurn atomic.Int64
```

它**不再阻止任何事**：读点已被删除，而它声称要防的 post-turn observer 现在是空实现：

```go
// ObserveUsage is retained as a compatibility hook. Usage observations never
// mutate the provider-visible checkpoint.
func (m ContextManager) ObserveUsage(u *provider.Usage) { _ = u }
```

`lastTurn` 的全部 4 个写入点都是 `Store(0)` 或 `Store(commit.activeTurn)`——**没有任何分支读它**。

### 10.3 清理建议（本轮**不实施**）

| 字段 | 建议 | 风险 |
|---|---|---|
| `consecutive` | **删除**（字段 + 7 处写） | 无：只 `++` 与 `=0`，无分支 |
| `lastTurn` | **删除**（字段 + 4 处写），并删掉那段已脱节的注释 | 无：无读点 |
| 测试中的引用 | 一并删除（12 处 `_test.go` 引用，均为断言而非驱动） | 低 |

**为什么不本轮做**：计划 A.2.6 明确「**本轮不得顺手扩大代码范围**」。
本轮只**给出建议**，由协调 Agent 决定是否单开一次清理。

**副产物**：`stuckInputHash` 曾被认为可能是死的（8 写 1 读），核验后**是活的**——
那一读在 `releaseMaintenanceLatch`。**不要删它。**

---

## 11. 回滚说明（计划 A.4 第 5 项）

A 侧的改动是**两个可独立回滚的行为变更**加一组**纯增量观测**（与 A 文档 §11 一致，本轮复核）：

| 变更 | 回滚方式 | 回滚后行为 |
|---|---|---|
| **R-1** 低收益锁存 + 增长释放 | `settleMaintenanceFold` 的 `latchLowYield(decision)` 换回 `resetCompactionProgress()`；删除 `releaseMaintenanceLatch` 调用点 | 回到 HEAD 的「input hash 变了就解锁」 |
| **R-2** headroom 判据 | 让 `GoalMet()` 恒返回 `true` | 所有装了投影的决策都报 `recovered` |
| **R-3** 观测字段与计数器 | 删除字段与计数器调用 | 纯增量，无 consumer 依赖 |

**R-1 与 R-2 必须一起回滚**：只回 R-2 会让 `low_yield` 永不出现；只回 R-1 会让 `low_yield`
出现但无行为效果（观测噪声）。

**回滚后需复跑**：

```bash
go test ./internal/agent/ -count=1
go run ./tools/repolint          # RED SET 应与 2c0f1dc61 逐字节相同
bash scripts/cache-guard.sh      # 10/10
```

回滚 R-1/R-2 后，三个依赖锁存的测试（`TestLowYieldFoldLatchesInsteadOfRepayingTheSameView`、
`TestLowYieldLatchReleasesOnGrowth`、`TestOverflowBypassesTheLowYieldLatch`）**会失败**，
这是预期的——它们是**行为契约**，不是回归守卫。`TestMaintenanceDecisionNamesEveryOutcome`
（纯值测试）回滚后仍成立。

---

## 12. 失败 / 跳过 / unknown 清单（计划 §2.3 第 4 项）

| 项 | 状态 | 说明 |
|---|---|---|
| A 侧定向测试 | **0 失败** | 5 组全绿（§2–§6） |
| `internal/agent` 全量 | **0 失败** | 62.3s |
| `internal/boot` / `internal/control` | **0 失败** | 23.9s / 99.4s |
| 边界攻击探针 | **全部执行完毕，已删除** | 未留下 `zz_probe_*` 文件 |
| **未验证：真实 Provider 上的重复压缩下降** | **unknown** | A 文档 L-5 已声明；属计划 §5 P3，由 C 负责 |
| **未验证：`visible_window_tokens` cap 在活 Agent 上的继承** | **unknown** | 属 B 的写集 |
| **未验证：`desktop/` 模块** | **unknown** | 本轮未跑；其 host-contract 测试在 HEAD 上即红（既有） |
| **未执行：`golangci-lint`** | **skipped** | 本机未安装；CI pin 是 2.12.2 |
| **未执行：M1 粒度偏离的补丁** | **skipped** | 属协调者裁定范围（§6.2） |

---

## 13. 状态

# `CONDITIONAL`

**通过的部分**：A-1（附文档缺口）、A-2、A-3、A-4、A-5、A-7、A-8、A-9、A-10 的建议。
**阻塞的部分**：**A-6** —— M1 的两处粒度偏离**未获协调者书面接受**，
且联合结论 §8 自己把它列为「GO 之前的待办」。A 侧**无权自接受**。

**解除条件**（二选一，由协调 Agent 决定）：

1. **书面接受**「会话总量 / turn 数」的粒度限制，并在联合结论中记录其代价（§6.2 的量化）；或
2. **要求补逐 turn 计数**（约 20 行纯观测，不改行为），由 A 侧另开一轮实施。

**此外建议协调者同时处理**（不阻塞，但计划 A.5 字面要求）：

3. 在 `State()` 或 A 文档中写明 4 个状态的 **receipt 不可达理由**（§2.3）。

---

## 14. 原始命令记录（计划 §2.3 第 2 项）

```bash
# 快照
mkdir -p /tmp/oneshot-A && cd /home/zwc/Agent/DeepSeek-Reasonix
git archive HEAD | tar -x -C /tmp/oneshot-A
cd /tmp/oneshot-A && find . -type f | LC_ALL=C sort | xargs sha256sum | sha256sum
# → 9e2b9623784d74cdf2d69581277553d96eb0368c30db741c1d6e49c8c38fcf24

# 门禁
go build ./... && go vet ./internal/agent/ && gofmt -l internal/agent/
go test ./internal/agent/ ./internal/boot/ ./internal/control/ -count=1
bash scripts/cache-guard.sh && go run ./tools/repolint

# A 侧五组定向
go test ./internal/agent/ -run 'TestMaintenanceDecision' -v -count=1
go test ./internal/agent/ -run 'TestRecentTailBudget|TestMaintenanceDecisionReportsHeadroom' -v -count=1
go test ./internal/agent/ -run 'TestLowYield|TestOverflowBypasses' -v -count=1
go test ./internal/agent/ -run 'TestTheLadderOnly|TestRescueIsTheLast' -v -count=1
go test ./internal/agent/ -run 'TestMaintenanceCost' -v -count=1

# 对比快照（A 之前）
mkdir -p /tmp/head3 && git archive HEAD~1 | tar -x -C /tmp/head3
```

**环境**：Go（`go version` 见下）、`linux/amd64`、`Team-agent` 分支。

```
go version go1.26.6 linux/amd64
```

（原始输出归档于 `/tmp/oneshot-A-artifacts/`：`a-directed-tests.txt`、`a-directed-5groups.txt`、
`build.txt`、`cache-guard.txt`、`repolint.txt`。）
