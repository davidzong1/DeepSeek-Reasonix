# Team Member 缓存：Part A 维护状态机与 headroom 契约

> 执行依据：`TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md` §3「Agent A」、§9 派单、§8「必须交付」。
> 日期：2026-09-24。基线：`5241384b6`（Team-agent）+ 本轮未提交改动。
> 写集：`internal/agent/context_headroom.go`（新增）、`context_headroom_test.go`（新增）、
> `context_manager.go`、`context_receipt.go`、`context_status.go`、`compact_commit.go`、`maintenance_commit.go`、
> `compact.go`、`compact_projection.go`、`context_rescue.go`、`preflight.go`、`prune.go`、`truncate.go`、
> `sessionstate.go`、`projection.go`（仅新增字段）。
> **未触碰**：Team owner writer 与报表统计口径、provider wire usage 归一化、system prompt 与 tool schema 内容、
> 真实 Provider 实验样本筛选（§3 禁止修改清单）。

---

## 0. 结论摘要

| # | 交付 | 强度 |
|---|---|---|
| **A-1** | 冻结了**一次维护决策的七态分类**（§2），并把它写进 receipt（`maintenance_state`）。`Status` 语义**逐字未动**。 | 证据（§2.4 测试） |
| **A-2** | 引入**可验证的 headroom 目标**：fold 必须留下 `≥ recentTailBudget()`（窗口的 16%，受 `visible_window_tokens` 封顶）的空间才算 recovered。**未达标不再报成功**。 | 证据（§3） |
| **A-3** | 同一 provider-visible 视图的**重复 summary 被抑制**：低收益 fold 锁存该视图，只有视图比锁存时的估算**增长 ≥ 窗口 5%** 才释放。 | 证据（§4） |
| **A-4** | **维护成本 telemetry**：`summary_requests` / `projection_installs` / `rescue_count` / `repeat_blocks` 四个会话级计数器。 | 证据（§5） |
| **A-5** | **发现并记录一个既有缺陷**：`compactionProgress.stuck` / `stuckInputHash` / `consecutive` 与 `lastTurn` **四个字段在 HEAD 上全是死代码**（只有写、没有读）。 | 证据（§4.1） |
| **A-6** | 门禁：`go build ./...`、8 个包测试全绿、`go vet`、`gofmt`、`cache-guard.sh` 10/10、repolint RED SET 与 HEAD **逐字节相同**。 | 证据（§7） |

**一句话**：把「压缩完成」从**断言**变成**可复算的判据**（headroom ≥ 目标），并让同一个上下文代际在没买到空间时**不再反复付费**。**没有改动任何 provider-visible 字节。**

---

## 1. 决策链现状（只读还原，计划 §5 P0 交付物 1）

### 1.1 谁调用 `Prepare`

`ContextManager.Prepare` 是**唯一**的自动维护入口（`compactionRunMu` 单飞整个事务）。生产调用点只有三处：

| 调用点 | Trigger | 备注 |
|---|---|---|
| `sampling_request.go:142`（`buildSamplingRequest`） | `pressure` | **每次采样前**都跑。这是「每轮都进维护」的入口 |
| `sampling_request.go:113` | `overflow` + `Force` | `applyAdmissionToRequest` 失败后的**一次性**物理恢复 |
| `context_recovery.go:76` | `overflow` + `Force` | provider 返回 `ContextLimitError` 后的恢复，`budget.retries == 0` 时一次 |
| `agent.go:767`（`CompactNow`） | `manual` + `Force` | 用户 `/compact` |
| `context_manager.go:74`（`PrepareContext`） | `pressure` | smoke/benchmark 兼容入口 |

> **计划 §5 P0 问「哪一个事件触发了压缩」的答案**：正常轮次里是**每一次采样前的 `pressure`**。
> 不存在「post-turn observer 独立触发」——`handleFinalResponse` 与 `handleToolRound` 里的 `ObserveUsage` 是**兼容空实现**（`context_manager.go:81`），它不改变 provider-visible 检查点。

### 1.2 `prepareOnce` 的判定顺序

```
Prepare
 └ prepareOnce
    ├ [1] 取 visible = modelVisibleMessages()
    ├ [2] est = estimatedVisibleRequestTokens(visible)          ← 稳定前缀 + 工具 schema + 角色投影
    ├ [3] contextWindow <= 0 且非 manual            → 返回（不做维护）
    ├ [4] fold = compactTrigger()  hard = hardInputCeiling()
    ├ [5] ObservedInputTokens > 0                   → est 改用它（兼容 harness）
    ├ [6] contextMaintenanceBlocked(hash, viewEst)  → 返回（回执退避，manual/overflow 除外）
    ├ [7] est < fold                                → resetCompactionProgress()
    ├ [8] releaseMaintenanceLatch(hash, est)        ← 【本轮新增】增长释放
    ├ [9] stuck 且 pressure 且 est < hard           → 返回（同一视图不再付费）【本轮收紧】
    ├ [10] forceFold = Force || manual || overflow || est >= hard
    ├ [11] est < fold 且 !forceFold                 → 返回
    ├ [12] shouldPruneBeforeFold                    → prune（免费投影，不调 summarizer）
    │        └ 落地后重新估 est；pressure 下 est < fold / overflow 下 est < hard → 返回
    └ foldContext → summary ladder
```

**关键量**（`compact.go`）：

| 量 | 定义 | 备注 |
|---|---|---|
| `compactTrigger()` | `window × compact_ratio`（默认 0.80） | `cache_aware_compaction` 且 `warmCache()` 时提升到 hard ceiling |
| `hardInputCeiling()` | `window − 256` | **物理**边界，不是第二个用户阈值 |
| `recentTailBudget()` | `min(window × 0.16, visible_window_tokens)` | 折叠后保留的逐字尾部 |

### 1.3 `foldContext` 的梯子

```
foldContext
 ├ maxSummaries = 2（pressure）/ 1（overflow）/ 4（manual 且超限）
 ├ 循环 ladder.next():
 │   ├ compactToProjectionLocked(...)          ← 一次 summary
 │   │   ├ 失败 → absorbOverflow（provider 报窗口/更密的 tokenizer → 重规划，不消耗配额）
 │   │   │         否则 → summaryFailed → rescueOrFail
 │   │   ├ CompactionNoop → summaryNoop
 │   │   └ 安装成功
 │   ├ foldLanded(policy, result, fold, hard)?
 │   │   pressure  : result < fold
 │   │   manual/overflow : hard<=0 || result<hard || result<fold
 │   ├ 落地 → settleMaintenanceFold(...)  ← 【本轮：不再无条件 reset】
 │   └ 未落地 → forceFold=false, 重算 hash, 继续梯子
 └ 梯子耗尽 → recordContextMaintenanceBlocked + stuck=true
    ├ overflow 或 result >= hard → rescueOverCeiling
    └ 否则 → 返回 result（turn 带着超触发的视图继续）
```

---

## 2. 七态状态图（计划 §3 职责 4 的直接交付）

### 2.1 状态与转移

```
                     est < fold                       ┌──────────────┐
        ┌───────────────────────────────────────────► │below_boundary│
        │                                             └──────────────┘
        │
   ┌────┴────┐  fold<=est<hard, 无投影   ┌────────────────┐
   │ 一次维护 │ ────────────────────────► │above_boundary  │
   │  决策   │                           └────────────────┘
   └────┬────┘  est >= hard, 无投影      ┌───────────┐
        │      ────────────────────────► │ at_ceiling│
        │                                └───────────┘
        │
        │  装了投影 且 headroom >= goal   ┌───────────┐
        │      ────────────────────────► │ recovered │
        │                                └───────────┘
        │  装了投影 且 headroom <  goal   ┌───────────┐
        │      ────────────────────────► │ low_yield │──┐
        │                                └───────────┘  │ 锁存该视图
        │  控制权交给 continuation rescue  ┌─────────┐   │
        │      ────────────────────────► │ rescued │   │
        │                                └─────────┘   │
        │  summary 失败/耗尽 或 stale      ┌─────────┐   │
        └      ────────────────────────► │ blocked │   │
                                         └─────────┘   │
                                                       │
        ┌──────────────────────────────────────────────┘
        │ 释放条件（releaseMaintenanceLatch）
        │   est >= stuckTokens + window × 5%      ← 增长到有新折叠区
        │   或 stuckTokens == 0（legacy sidecar）  ← fail-open
        └─► 回到 [8]，允许下一次 summary
```

### 2.2 分类判据（`maintenanceDecision.State()`，顺序即优先级）

| 序 | 条件 | 状态 |
|---|---|---|
| 1 | `Rescued` | `rescued` |
| 2 | `Blocked` | `blocked` |
| 3 | `Applied && GoalMet()` | `recovered` |
| 4 | `Applied` | `low_yield` |
| 5 | `Hard > 0 && Estimate >= Hard` | `at_ceiling` |
| 6 | `Fold > 0 && Estimate >= Fold` | `above_boundary` |
| 7 | 其他 | `below_boundary` |

顺序是有意的：被 rescue 的事务**就是** rescued（即使同一视图也被禁止再次 summary）；装了投影的事务按**买到的空间**判，不按状态码判。

### 2.3 与计划 §3 职责 4 七项的对应

| 计划要求的状态 | 本实现 |
|---|---|
| 未达到维护边界 | `below_boundary` |
| 已达普通维护边界但仍低于 hard ceiling | `above_boundary` |
| 已达到 hard ceiling | `at_ceiling` |
| 普通压缩成功并拥有足够 headroom | `recovered` |
| 普通压缩无效/收益不足 | `low_yield` |
| 已进入 Context Rescue | `rescued` |
| 当前 generation 已被阻断 | `blocked` |

**七项一一对应，无遗漏、无多余。**

### 2.4 契约测试

`TestMaintenanceDecisionNamesEveryOutcome` 用**一个表**钉住七个状态的边界，包括 `recovered` / `low_yield` 的分界线（`headroom == goal` 恰好算 recovered）。

---

## 3. headroom 目标（计划 §3 职责 3）

### 3.1 定义

```go
HeadroomTokens = foldTrigger − resultTokens
Goal           = recentTailBudget() = min(window × 0.16, visible_window_tokens)
GoalMet()      = Fold <= 0 || Goal <= 0 || HeadroomTokens >= Goal
```

### 3.2 为什么是 `recentTailBudget()`

不是拍的：fold 本身**保留 16% 的逐字尾部**（`recentTailBudgetRatio`）。因此留下少于 16% 的空间意味着**下一轮自己的工具输出就会重新越过触发边界**——那次 summary 等于白付。

这也让 headroom 目标与 Context Rescue 的 10% 收益判据**不冲突**：`rescue` 判的是「相对于源请求删掉了多少」（`reduction_ratio`），headroom 判的是「相对于触发边界留下了多少」。两者在 receipt 上都有（`reduction_ratio` / `headroom_tokens`）。

### 3.3 fail-open 的两个条件

| 条件 | 含义 | 理由 |
|---|---|---|
| `Fold <= 0` | 窗口未知 → 无触发边界 | 不该被一个从未拥有过的边界永久锁存 |
| `Goal <= 0` | 窗口未知 → 无尾部预算 | 同上 |

**这两个 fail-open 是有代价的**：窗口未知的会话拿不到低收益抑制。见 §8 限制清单 L-1。

### 3.4 receipt 上的可复算字段

| 字段 | 含义 |
|---|---|
| `headroom_tokens` | `fold_trigger_tokens − result_tokens`，**可为负** |
| `fold_trigger_tokens` | 该决策生效时的触发边界 |
| `hard_ceiling_tokens` | 该决策生效时的硬上限 |
| `reduction_ratio` | `(input_tokens − result_tokens) / input_tokens` |
| `maintenance_state` | 七态之一 |

**这四个数让判据可复算，而不是只能相信 `maintenance_state` 的字面值**（计划 §2.2「不把本地 prefix hash 当作 Provider cache key 的证明」的同类要求：不把自己的分类当作自己的证明）。

---

## 4. 重复压缩抑制（计划 §3 职责 2、5）

### 4.1 一个既有缺陷：四个死字段

在改动前先做了全仓库引用盘点，结论：

| 字段 | HEAD 上的引用 |
|---|---|
| `compactionProgress.stuck` | 读：`prepareOnce`（只用于 pressure 退避）；写：6 处 |
| `compactionProgress.stuckInputHash` | 读：同上；写：6 处 |
| `compactionProgress.consecutive` | **读：0 处**（全部是写与测试断言） |
| `compactionProgress.lastTurn` | **读：0 处**（`git log -S` 定位到读点由 `9f59996e2` 删除后留下空壳） |

`consecutive` 与 `lastTurn` 是**注释描述的行为已不存在**的遗留（`lastTurn` 的注释仍写着「stops the post-turn observer and the pre-send preflight from paying for two summaries」，而 post-turn observer 已是空实现）。**本轮不删除它们**（属于清理，不属于本计划范围），但**记录在此**，供协调 Agent 决定。

### 4.2 收紧的规则

**改动前**（`prepareOnce`）：

```go
if a.sess.compaction.stuck && a.sess.compaction.stuckInputHash != inputHash {
    // 只要 input hash 变了就解锁
    a.sess.compaction.stuck = false; ...
}
```

**问题**：`inputHash` 是**整个可见视图**的指纹。追加一条工具结果就变了。于是「fold 后只加了 3K token 又越过触发边界」的视图会**再付一次 summary**，得到同样的收益不足结果。

**改动后**：

```go
if a.sess.compaction.stuck && a.sess.compaction.stuckInputHash != inputHash {
    // 必须比锁存时的估算增长 >= 窗口的 5%
    if a.maintenanceGrowthDue(a.sess.compaction.stuckTokens, est) { 解锁 }
}
```

`stuckTokens` 记录**该次 fold 自己的源估算**（`decision.Estimate`）。`maintenanceGrowthDue` 复用既有常量 `maintenanceRetryGrowthRatio = 0.05` —— 与 `maintenanceRetryDue`（失败回执的同类规则）**同一条规则**，不是新引入的阈值。

### 4.3 谁能绕过锁存

| 路径 | 是否被抑制 | 理由 |
|---|---|---|
| `pressure` | ✅ 被抑制 | 这是要修的路径 |
| `overflow` | ❌ 不抑制 | 物理恢复路径，拒绝它就是让 turn 带着 provider 会拒绝的请求出去 |
| `est >= hard`（pressure 下） | ❌ 不抑制 | 同上，`prepareOnce:154` 的条件是 `est < hard` |
| `manual` | ❌ 不抑制 | 用户显式请求 |
| 增长 ≥ 5% 的视图 | ❌ 不抑制 | 有新的可折叠区 |

### 4.4 契约测试

| 测试 | 钉住什么 |
|---|---|
| `TestLowYieldFoldLatchesInsteadOfRepayingTheSameView` | 首次 fold 后**必须**锁存；随后视图**真的再次越过触发边界**（不增长过 5% 阈值）时**不再付费** |
| `TestLowYieldLatchReleasesOnGrowth` | 视图增长超过 5% 后**必须**重新允许一次 summary |
| `TestOverflowBypassesTheLowYieldLatch` | 锁存存在时，overflow 恢复**仍然运行** |
| `TestTheLadderOnlyReachesRescueFromTheCeiling` | 低于 ceiling 的失败**不进 rescue**（只 blocked）；到达 ceiling 才进 |
| `TestRescueIsTheLastRungAndCountsOnce` | 认证的 plan 计 1 次 rescue、装 0 个投影；未 opt-in 的梯子计 0 次 |

> **测试自身的空断言防护**：低收益 fixture 在 `lowYieldAgent` 里断言「估算 ≥ 触发边界且 < 硬上限」，重复测试里断言「增长后确实再次越过触发边界**且**未超过 5% 释放阈值」。否则测试会因为 fixture 不满足前提而变成空转。

---

## 5. 维护成本 telemetry（计划 §3 职责 6）

### 5.1 四个计数器

挂在 `compactionProgress.spend`（**不序列化**，随 lineage 重置）：

| 计数器 | 计数点 | 为什么是这个点 |
|---|---|---|
| `SummaryRequests` | `runSummaryRequest` 入口 | **四条** summary 通道（普通 fold、transcript/slim 形式、fragment 路径、continuation rescue）全部经过它，这是唯一不会漏计的位置 |
| `ProjectionInstalls` | `commitSummaryProjection` / `installMaintenanceProjection` | 只计真正的安装；receipt 重发（`session_checkpoint.go`）不计 |
| `RescueCount` | `rescueOverCeiling` 认证 plan 成功时 | 只计认证成功；`not_eligible` 走截断不算 |
| `RepeatBlocks` | `recordContextMaintenanceOutcome` 持久化成功后 | **在持久化之后**：早退分支（同一视图重复被拒）不发布 receipt，计数器也不该虚增 |

### 5.2 暴露面

`ContextMaintenanceSnapshot.MaintenanceCost`（`MaintenanceCost` 结构体导出，供 `control` / `desktop` 消费）。新增字段是 additive：`internal/cli/team_follower.go:311` 与 `internal/control/context_status.go:9` 的零值返回不受影响。

### 5.3 计划 §3 职责 6 的四项

| 计划要求 | 实现 |
|---|---|
| summary requests | `SummaryRequests` |
| projection installs | `ProjectionInstalls` |
| rescue count | `RescueCount` |
| 重复阻断次数 | `RepeatBlocks` |

---

## 6. 字段契约（计划 §4 M1 对齐）

计划 §4 M1 要求 A/B/C **同一命名与语义**。本轮的字段命名与语义如下，供 B/C 对齐：

```text
ContextMaintenanceReceipt（provider 中立，持久化到 sidecar）
  generation            ← 未新增：CompactionState.Generation 已有
  trigger               ← 已有
  source_tokens         ← 已有（InputTokens）
  result_tokens         ← 已有（ResultTokens）
  hard_ceiling          ← 新增 HardCeilingTokens
  headroom_tokens       ← 新增
  reduction_ratio       ← 新增
  action                ← 已有
  projection_version    ← 已有
  cache_break_reason    ← 未新增：CacheBreak bool 已有 + CacheReason 在 event 层
  summary_requests      ← 未落在 receipt：改为**会话级累计**（§5），逐次决策不可归属
  rescue_planned        ← 未落在 receipt：rescue 走 error 通道（ContextRescueRequired）
  maintenance_state     ← 新增（七态）

MaintenanceCost（会话级累计，不持久化）
  SummaryRequests / ProjectionInstalls / RescueCount / RepeatBlocks
```

**两处与计划的偏离，需协调 Agent 裁定**：

1. `summary_requests` 计划放在 receipt 上，本实现放在**会话累计**。理由：一次决策可以跑多次 summary（梯子最多 4 次），receipt 是**决策**记录而非**调用**记录；放在 receipt 上要么丢信息（只记最后一次）要么改变 receipt 的粒度。
2. `rescue_planned` 计划放在 receipt 上，本实现**不放**。理由：rescue 的 plan 通过 `ContextRescueRequired` 错误携带（`prepared.Recovery` 只是诊断副本，调用方在 `err != nil` 时丢弃 `PreparedContext`），把它复制进 receipt 会产生第二个真相来源。

---

## 7. 门禁（隔离快照）

在 `git archive HEAD` + **仅 Agent A 的 15 个文件**的快照上执行。理由：工作树同时存在 B/C 的在途改动，实时树上的结果不可归属（C 在其文档 §6 记录了同一问题）。

| 门禁 | 结果 |
|---|---|
| `go build ./...` | ✅ |
| `go test ./internal/agent/` | ✅ 62.7s |
| `go test ./internal/cli/` | ✅ 107.5s |
| `go test ./internal/control/` | ✅ 117.9s |
| `go test ./internal/team/ ./internal/provider/... ./internal/boot/ ./internal/stats/ ./internal/session/` | ✅ 全绿 |
| `go vet ./internal/agent/ ./internal/control/` | ✅ |
| `gofmt -l internal/agent/` | ✅ 空 |
| `scripts/cache-guard.sh` | ✅ 10/10 case |
| `go run ./tools/repolint` RED SET | ✅ **与 HEAD 逐字节相同** |

**未验证**：`desktop/` 模块。其 `TestHostContractGeneratedFilesAreCurrent` 在 **HEAD 上同样红**（既有问题，非本轮引入）；`desktop` 的其余构建在本轮改动下通过。

---

## 8. 限制清单（必须随结论一起引用）

| # | 限制 | 影响 |
|---|---|---|
| **L-1** | `GoalMet` 在窗口未知时 **fail-open**，因此窗口未知的会话**拿不到低收益抑制**。 | 这类会话可能仍有重复 summary。当前生产路径上 `contextWindow` 总是已配置，但 `contextWindow <= 0` 在 `prepareOnce:133` 已被提前返回，所以实际到达该分支的只有 `manual`。 |
| **L-2** | headroom 目标取 `recentTailBudget()`，而它是**窗口的 16%**，不是实测的「一轮工具输出量」。 | 工具输出远大于 16% 窗口的会话仍可能连续触发。这是**有意的保守**：目标可复算、与折叠策略自洽，比拍一个实测均值更稳。 |
| **L-3** | `stuckTokens` **不持久化**（`compactionProgress` 是运行期状态）。 | 重启后锁存丢失，会话会重试一次。这与既有 `stuck` 的行为一致（`stuck` 本身也不持久化，只有 receipt 的 `blocked_input_hash` 持久化）。 |
| **L-4** | `consecutive` 与 `lastTurn` 仍是**死字段**（§4.1）。 | 无功能影响；但注释描述的行为不存在，容易误导后续读者。 |
| **L-5** | 本轮**没有**验证真实 Provider 上的重复压缩是否真的下降。 | 计划 §6.1 要求「summary requests 显著下降」需要在 P3 真实 Provider 对照中验证。本轮的证据只到**单元测试**与**本地夹具**。 |
| **L-6** | `ContextMaintenanceSnapshot` 的 `HeadroomGoalMet` 是**当前视图**的读数，不是最后一次决策的读数。 | 两者会不同：决策后视图继续增长。要读决策本身请用 `LastReceipt.MaintenanceState`。 |
| **L-7** | `projection.go` 的 diff 中混有另一个 Agent 的 `wireCall`/`wireMsg` 提取。 | 我无法把该 hunk 从工作树摘出（会连带破坏 B 的文件）。隔离快照里手工重建了「HEAD 的 projection.go + 仅我的字段」版本，门禁在**那个**版本上跑。 |

---

## 9. 与计划「不可接受方案」（§2.2）的逐条对照

| 禁止项 | 本轮 |
|---|---|
| 不通过降低统计分母、排除低命中样本或修改 usage 归一化来「提高」命中率 | ✅ 未触碰 `internal/stats`、`internal/team` 任何统计文件 |
| 不把本地 prefix hash 当作 Provider cache key 的证明 | ✅ `contextMaintenanceInputHash` 仅用于**退避去重**，未用于任何命中率声明 |
| 不为了缓存稳定而永久关闭压缩 | ✅ 只抑制**同一视图**的重复付费，且 overflow 与 ceiling 路径不受抑制 |
| 不在每个工具结果到达后无条件调用 summary | ✅ 本轮的改动方向恰好相反 |
| 不把 Context Rescue 作为正常 compaction 的替代路径 | ✅ `rescueOverCeiling` 只在 `overflow` 或 `result >= hard` 时可达；`TestTheLadderOnlyReachesRescueFromTheCeiling` 钉住这一点 |
| 不在没有真实 Provider 对照和任务质量护栏的情况下直接扩大到全部成员 | ✅ 本轮无灰度动作；真实对照属 P3 |

---

## 10. 复现

```bash
# 隔离快照（推荐：工作树上有其他 Agent 的在途改动）
mkdir -p /tmp/parta && git archive HEAD | tar -x -C /tmp/parta
# 把本轮的 15 个文件复制进去，然后：
cd /tmp/parta
go build ./...
go test ./internal/agent/ -count=1
go test ./internal/agent/ -run 'TestLowYield|TestOverflowBypasses|TestMaintenance|TestTheLadderOnly|TestRescueIsTheLast' -v
go run ./tools/repolint
bash scripts/cache-guard.sh
```

**本轮的 15 个文件**：

```
internal/agent/context_headroom.go              （新增）
internal/agent/context_headroom_test.go         （新增）
internal/agent/context_manager.go
internal/agent/context_receipt.go
internal/agent/context_status.go
internal/agent/compact.go
internal/agent/compact_commit.go
internal/agent/compact_projection.go
internal/agent/context_rescue.go
internal/agent/maintenance_commit.go
internal/agent/preflight.go
internal/agent/prune.go
internal/agent/truncate.go
internal/agent/sessionstate.go
internal/agent/projection.go                    （仅新增字段；同文件含 B 的 wireCall 提取）
```

---

## 11. 回滚说明（计划 §8 Agent A 交付物 4）

### 11.1 回滚粒度

本轮的改动是**两个可独立回滚的行为变更**加一组**纯增量观测**：

| 变更 | 位置 | 回滚方式 | 回滚后的行为 |
|---|---|---|---|
| **R-1** 低收益锁存 + 增长释放 | `context_headroom.go` 的 `releaseMaintenanceLatch` / `settleMaintenanceFold` / `latchLowYield`；`context_manager.go:153` 的调用点 | 把 `settleMaintenanceFold` 的 `latchLowYield(decision)` 换回 `resetCompactionProgress()`；把 `releaseMaintenanceLatch` 调用点删除 | 回到 HEAD 的「input hash 变了就解锁」。**provider-visible 字节不变**，只是重复 summary 回来 |
| **R-2** headroom 判据（只影响 receipt 上的 `maintenance_state`） | `context_headroom.go` 的 `maintenanceDecision.GoalMet` | 让 `GoalMet()` 恒返回 `true` | 所有装了投影的决策都报 `recovered`，即 HEAD 的语义。**R-1 若不同时回滚，锁存就永远不会触发** |
| **R-3** 观测字段与计数器 | `projection.go` 的 receipt 字段、`context_status.go` 的 snapshot 字段、`maintenanceSpend` | 删除字段与计数器调用 | 纯增量，删除不影响任何既有 consumer（`Status` 语义未变） |

**R-1 与 R-2 必须一起回滚**：只回 R-2 会让 `low_yield` 永不出现，只回 R-1 会让 `low_yield` 出现但无行为效果（观测噪声）。二者是一组。

### 11.2 无行为影响的改动（不需回滚）

- `noteSummaryRequest` / `noteProjectionInstall` / `noteMaintenanceDecision`：只写原子计数器。
- receipt 的 `HeadroomTokens` / `FoldTriggerTokens` / `HardCeilingTokens` / `ReductionRatio` / `MaintenanceState`：additive，`omitempty`，老 sidecar 解码后为零值。
- `snapshot.MaintenanceCost`：additive。
- `stuckTokens = 0` 的四处重置补齐（`preflight.go` ×2、`sessionstate.go`、`resetCompactionProgress`）：与既有 `stuckInputHash = 0` 同址，无行为差异。

### 11.3 不需要重建 sidecar

`compactionStateSchemaCurrent` 仍是 **V4**，未升版。新增的 receipt 字段全部 `omitempty`，因此：

- 老 sidecar 读入：新字段为零值，`GoalMet` 走 `Fold <= 0 || Goal <= 0` 的 fail-open 分支，行为等同 HEAD。
- 新 sidecar 被老二进制读入：未知 JSON 键被忽略，`Status`/`Action`/`BlockedInputHash` 等既有字段语义未变，`LoadCompactionState` 的版本检查通过。

**即：本轮不产生需要迁移或清理的持久化状态。**

### 11.4 回滚后需要复跑的验收

```bash
go test ./internal/agent/ -count=1
go run ./tools/repolint          # RED SET 应与 HEAD 逐字节相同
bash scripts/cache-guard.sh      # 10/10
```

回滚 R-1/R-2 后，`context_headroom_test.go` 中依赖锁存的三个测试（`TestLowYieldFoldLatchesInsteadOfRepayingTheSameView`、`TestLowYieldLatchReleasesOnGrowth`、`TestOverflowBypassesTheLowYieldLatch`）**会失败**，这是预期的——它们是行为契约，不是回归守卫。回滚时一并删除或标记 `t.Skip`，并保留 `TestMaintenanceDecisionNamesEveryOutcome`（纯值测试，回滚后仍成立）。
