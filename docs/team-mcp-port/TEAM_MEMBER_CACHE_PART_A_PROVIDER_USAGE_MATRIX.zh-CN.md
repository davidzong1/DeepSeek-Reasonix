# Team Member 缓存命中率：Part A 跨 Provider usage 语义与 K1 正确性

> 执行依据：`TEAM_MEMBER_CACHE_POST_JOINT_3AGENT_PLAN.zh-CN.md` §3 Agent A、§8.2 派单文本。
> 日期：2026-09-24。基线：`7bda0c3d3`（Team-agent-merge-mainv2）。
> 前置：`TEAM_MEMBER_CACHE_JOINT_CONCLUSION.zh-CN.md`、`TEAM_MEMBER_CACHE_PART_A_CONTRACT.zh-CN.md`（v1）、`TEAM_MEMBER_CACHE_PART_C_REVIEW.zh-CN.md`。
> 本文只处理 Provider usage 语义与 K1 正确性。**不改**请求构造、缓存策略、上下文裁剪、成员隔离和统计分母。
> **修订（同日）**：A-5 已从「已证据化未修复」升级为**已修复**，见 §5；repolint 的 8 处既有红项已全部清零。

---

## 0. 结论摘要

| # | 结论 | 强度 | 依据 |
|---|---|---|---|
| **A-1** | 事件形状矩阵已表驱动落地（17 行 / 3 方言），每行断言完整归一化读数而非单个字段。 | **证据** | §2、`stream_usage_test.go` |
| **A-2** | K1 在**本网关**的形状上**不需要** `billing_usage` oracle 即可独立验证：warm 请求的 `cache_read (33,408) > input_tokens (29)` 本身构成矛盾证明，把 estimate 前驱事件删掉读数不变。 | **证据** | §3.1 |
| **A-3** | 但该矛盾证明**只在 read > input 时成立**。当 split 事件的 `input_tokens > cache_read` 时两种约定自洽，estimate 前驱是唯一信号，且它是**启发式不是证明**。 | **证据** | §3.2 |
| **A-4** | 发现并修复 K1 的一个**折叠缺口**：split 之后到达的 split-free 事件会把整段 prompt 重新写进 `in`，使 `prompt = input + read` 被重复计入（实测 33,437 → 66,845）。已加 `haveSplit` 守卫。 | **证据** | §4 |
| **A-5** | K1 的 `exclusive` 判定曾与**路由声明的 reasoning protocol 强耦合**：同一份 LongCat 形状字节，在声明 `deepseek` 的 route 上读成 `prompt=13`，在声明 `none`/native 的 route 上读成 `prompt=25`。这是**已存在的跨路由口径风险**，不是本轮引入；**本轮已修复**（`client.inclusiveUsage` 与 `client.deepseek` 拆开）。 | **证据** | §5 |
| **A-6** | 负值 counter 不 clamp，直接进入账单字段；该样本由 A 的 v1 契约 `accounting_valid` 具名排除（`negative:cache_miss_tokens`），不静默修正。 | **证据** | §2.4 |
| **A-7** | 缺失与显式零在 `wireUsage` 上**不可区分**（都解码为 `int` 的 0）。任何需要该区分的判定必须读原始事件。 | **证据** | §3.3 |
| **A-8** | 按 Provider 的判定见 §6：native Anthropic / LongCat / DeepSeek(official) **通过**；DeepSeek-compatible 自定义网关 **有限通过**；非 Anthropic 方言（OpenAI `responses` 等）**未审**。 | — | §6 |

**一句话**：K1 的形状语义在本仓库支持的三种 Anthropic 方言上已被表驱动测试固定；其中生产网关那条路径的正确性由**算术矛盾**而非私有 oracle 支撑，因此比联合结论 §2.6 记录的强度更高。矩阵同时暴露出两个**未决**形状（省略 split 的全命中、input > read 的 delta-only），它们必须随结论一起引用。第三个发现——**跨路由口径耦合**——本轮已修复（§5）。

---

## 1. 调用链盘点（P0 交付）

### 1.1 wire usage → stream fold → inclusive/exclusive → stats

```
SSE data 行
  └─ anthropic.go:readStream  (anthropic.go:519 主循环)
       ├─ case "message_start"  → mergeUsage(ev.Message.Usage)   // usage 嵌在 message.usage
       └─ case "message_delta"  → mergeUsage(ev.Usage)           // usage 在顶层
                                   │
                                   ▼
       stream_usage.go:streamUsage.merge(*wireUsage)
            ├─ out            = max(out, OutputTokens)      // 唯一真正累积的字段
            ├─ 无 split 分支  → estimateInput = max(...); in = InputTokens（haveSplit 之前）
            └─ 有 split 分支  → exclusive = estimateInput > InputTokens
                                          || CacheReadInputTokens > InputTokens
                                in, cacheCreate, cacheRead = 本事件读数
                                haveSplit = true
                                   │
                                   ▼
       anthropic.go:finalize (anthropic.go:634)
            └─ inclusive := c.inclusiveInput() && !usage.exclusive
                 c.inclusiveInput() == (c.deepseek)      // anthropic.go:258
                                   │
                                   ▼
       messages_usage.go:messagesUsage(in, out, create, read, billed, inclusive)
            ├─ exclusive 约定: miss = in + create;  prompt = in + create + read
            └─ inclusive 约定: miss = max(in - read, 0); prompt = read + miss
            // 两种约定都保证 prompt == hit + miss
                                   │
                                   ▼
       provider.Usage → ChunkUsage → agent.run_usage.mergeSamplingUsage
                                   → stats.Recorder.recordProviderUsage（账本）
                                   → cli.memberCacheRequest（成员记录）
```

### 1.2 未知点清单（P0 交付）

| # | 未知点 | 位置 | 本轮状态 |
|---|---|---|---|
| U-1 | `c.deepseek` 同时决定**推理回放**与**usage 约定** | `anthropic.go:98-104`、`:258` | **本轮修复**：拆出 `client.inclusiveUsage`，见 §5 |
| U-1b | `reasoning_protocol="none"` 会**同时**关掉 DeepSeek 回放与 inclusive 口径 | `anthropic.go:99-104` | 实测确认（§5.2）；本轮修复后**只关回放**，口径不再随之改变 |
| U-2 | split 事件缺 `cache_read` 但带 `cache_creation` 时是否算 "有 split" | `stream_usage.go:53` | 已测（§2 表：write-only 行） |
| U-3 | split 之后再来一个 split-free 事件 | `stream_usage.go:53-58` | **本轮修复**，见 §4 |
| U-4 | 全命中且网关省略 split（`delta(input=0, read=0)`） | `stream_usage.go` | **未决**，§3.4 |
| U-5 | delta-only 且 `input > read` | `stream_usage.go:63` | **未决**，§3.2 |
| U-6 | 负值 counter | `stream_usage.go` 全程 | 已测，不 clamp（§2.4） |
| U-7 | 缺失 vs 显式零 | `wireUsage`（`int` 字段） | **结构上不可区分**，§3.3 |
| U-8 | 非 Anthropic 方言的同类跨事件混用 | `provider/openai`、`provider/responses` | **未审**（超出 Agent A 写集，§6） |

---

## 2. Provider / 事件形状矩阵（交付物 1、2、3）

**测试**：`internal/provider/anthropic/stream_usage_test.go` → `TestUsageEventShapeMatrix`（17 行子测试）。
每行断言 `prompt/hit/miss/write/out` **全部五个字段**，并断言 `prompt == hit + miss`。
**证据等级**列的含义：

- **代码可证** — 读数由路由声明 + 折叠规则直接推出，与上游无关。
- **fixture 可证** — 该形状由 `usageStream()` 合成，断言的是适配层对它的处理。
- **真实上游已观测** — 该形状在本轮之前的真实网关抓包/测试中出现过（来源见「依据」列）。
- **未验证** — 无真实观测，仅构造。

| # | 形状 | 方言/路由 | 事件序列 | prompt | hit | miss | write | out | 证据等级 | 依据 |
|---|---|---|---|---|---:|---:|---:|---:|---:|---|---|
| 1 | native 标准 | native | `start(100,10,50,0)` → `delta(0,0,0,25)` | 160 | 50 | 110 | 10 | 25 | 代码可证 + fixture 可证 | `messages_usage_test.go` 既有 |
| 2 | native cold | native | `start(100,0,0,0)` → `delta(0,0,0,25)` | 100 | 0 | 100 | 0 | 25 | 代码可证 | 同上 |
| 3 | native 全命中 | native | `start(0,0,100,0)` → `delta(0,0,0,25)` | 100 | 100 | 0 | 0 | 25 | 代码可证 | 同上 |
| 4 | LongCat delta-only | LongCat | `start(无 usage)` → `delta(13,5,7,3)` | 25 | 7 | 18 | 5 | 3 | **真实上游已观测** | `anthropic_test.go:482 TestReadStreamUsageFromMessageDelta` |
| 5 | LongCat 重复 delta | LongCat | `start(无)` → `delta(同)` ×2 | 25 | 7 | 18 | 5 | 3 | fixture 可证 | 幂等性 |
| 6 | DeepSeek 双 start | DeepSeek-compat | `start(39149,0,0,0)` → `start(29,0,33408,0)` → `delta(同)` | 33437 | 33408 | 29 | 0 | 2 | **真实上游已观测** | 联合结论 §2.1 |
| 7 | DeepSeek cold | DeepSeek-compat | `start(39149,0,0,0)` → `delta(33437,0,0,2)` | 33437 | 0 | 33437 | 0 | 2 | 推断（Part C §9 形状表第 2 行） | 未在真实抓包中单独确认 |
| 8 | split 重复 | DeepSeek-compat | `start(估)` → `start(29,0,33408)` ×2 → `delta` | 33437 | 33408 | 29 | 0 | 2 | fixture 可证 | 幂等性 |
| 9 | split 后 split-free 重申 | DeepSeek-compat | `start(估)` → `start(29,0,33408)` → `delta(33437,0,0)` | 33437 | 33408 | 29 | 0 | 2 | fixture 可证 | **本轮修复**，§4 |
| 10 | 乱序：split 在前 | DeepSeek-compat | `start(29,0,33408)` → `start(39149,0,0)` → `delta` | 33437 | 33408 | 29 | 0 | 2 | fixture 可证 | 顺序无关性 |
| 11 | read > input（单事件） | DeepSeek-compat | `delta(10,0,50,5)` | 60 | 50 | 10 | 0 | 5 | 代码可证 | 算术矛盾 |
| 12 | 全命中 + `input=0` | DeepSeek-compat | `start(39149,0,0,0)` → `delta(0,0,39149,2)` | 39149 | 39149 | 0 | 0 | 2 | fixture 可证 | 算术矛盾 |
| 13 | **delta-only 且 input > read** | DeepSeek-compat | `start(无)` → `delta(39047,0,39040,4)` | 78087 | 39040 | 39047 | 0 | 4 | **未验证 / 未决** | §3.2 |
| 14 | split 后到达更大的 estimate | DeepSeek-compat | `start(估)` → `start(split)` → `start(99999,0,0,0)` → `delta(split)` | 33437 | 33408 | 29 | 0 | 2 | fixture 可证 | 本轮修复的边界 |
| 15 | write-only split | DeepSeek-compat | `start(39149,0,0,0)` → `delta(29,33408,0,2)` | 33437 | 0 | 33437 | 33408 | 2 | **未验证**（本网关 `cache_creation` 恒为 0） | Part C §6.3-3 |
| 16 | 负值 counter | DeepSeek-compat | `delta(-5,0,10,2)` | 5 | 10 | **−5** | 0 | 2 | **畸形**（无受支持 Provider 会发） | §2.4 |
| 17 | 省略 split 的全命中 | DeepSeek-compat | `start(39149,0,0,0)` → `delta(0,0,0,2)` | 39149 | 0 | 39149 | 0 | 2 | **未验证 / 未决** | §3.4 |

> 表内 17 行全部由 `TestUsageEventShapeMatrix` 覆盖；「缺失 vs 显式零」另由 `TestUsageMissingAndExplicitZeroAreIndistinguishable` 固定（§3.3）。
> **每行同时断言 `prompt == hit + miss`**——该不变式是**必要非充分**条件（适配层自行构造它），此处仅用于捕捉「折叠丢字段」这一类错误。

### 2.1 每种形状的归一化结果与未知状态

- **已验证（14 行）**：读数由路由声明的约定唯一确定，或由 counter 之间的算术矛盾强制。
- **未决（2 行）**：第 13、17 行。另有「缺失 vs 显式零」这一结构性不可区分（§3.3），它不是表内某一行，而是整个矩阵共同面对的读取限制。未决行**照常断言当前读数**，理由是下游必须把 `hit == 0` 读作「**未报告 cache read**」（A v1 契约 §2.3），而不是冷启动。
- **畸形（1 行）**：第 16 行。适配层不 clamp，交给 v1 契约的 `accounting_valid` 具名排除。

### 2.2 非回归守卫

| 测试 | 作用 |
|---|---|
| `TestUsageNativeShapeIsUnchangedByTheRemainderRule` | native 路由的读数与 K1 前逐字段 max 的结果相同 |
| `TestUsageLongCatShapeIsUnchangedByTheRemainderRule` | delta-only 网关同理（无 estimate 前驱） |
| `TestUsageRemainderRuleIsInertWithoutAnOverEstimate` | 无矛盾时规则不触发；两种路由对同一字节仍给出不同（各自正确的）读数 |
| `TestUsageNoUsageAnywhereEmitsNoUsageChunk` | 全程无 usage 的流**不发** usage chunk（零值 usage 会被下游误读为「0 token 的服务」） |

### 2.3 `prompt == hit + miss` 的地位

A v1 契约 §3.1 已声明该等式**不能**作为命中率正确性的充分证明（归一化代码自行构造它）。矩阵据此把它降为**结构性守卫**：只捕捉「某次折叠把字段整体丢掉」。真正的正确性证据在 §3。

### 2.4 负值 counter 的处理（结论 A-6）

实测（`deepSeekClient`，`delta(-5,0,10,2)`）：`prompt=5 hit=10 miss=-5`。

- 适配层**不 clamp**：猜一个下限等于发明一个上游从未声明的读数。
- 该样本在下游被 A v1 契约的 `MemberCacheRequest.Accounting()`（`internal/team/cacherequest.go:159`）标为 `negative:cache_miss_tokens`，`AccountingValid=false`，经 `accounting_invalid` 具名排除并计数，**不静默修正**。
- 矩阵第 16 行把当前行为钉住：若将来加 clamp，测试会以「刻意的改动」而非「无声漂移」的方式失败。

---

## 3. K1 能否在无 `billing_usage` 时独立验证（交付物 4）

**结论：在本网关的形状上能，且比联合结论记录的更强；但只在 read > input 时成立。**

### 3.1 本网关的 warm 形状：算术矛盾即证明

`TestUsageWarmShapeIsDecidedByTheCountersAlone` 断言：

```
with    estimate: start(39149,0,0,0) → start(29,0,33408,0) → delta(29,0,33408,2)
without estimate:                     start(29,0,33408,0) → delta(29,0,33408,2)
两者的读数完全相同：prompt=33437 hit=33408 miss=29
```

理由：**`cache_read (33,408) > input_tokens (29)`**。cache read 不可能是 `input_tokens` 的子集——它根本装不下。这个矛盾**只由 counter 本身给出**，与 `billing_usage` 无关。

因此，联合结论 §2.6 把 K1 的正确性建立在「网关私有 `billing_usage` 是可信 oracle」这一**假设**（其结论强度表第 9 条）之上，而本条把它提升为**证据**：本网关的 warm 形状可由算术独立判定。estimate 前驱规则不是这条形状的判据——删掉前驱读数不变。

### 3.2 边界：read > input 是唯一可判定的情形

`TestUsageEstimatePredecessorIsTheOnlySignalOnAnAmbiguousShape` 断言另一半：

```
start(2000,0,0,0) → delta(900,0,300,2)
  → prompt=1200 hit=300 miss=900        （estimate 前驱触发 remainder 读法）
去掉前驱：delta(900,0,300,2)
  → prompt=900  hit=300 miss=600        （回落到路由声明的 inclusive 约定）
```

当 `input_tokens (900) > cache_read (300)` 时两种约定**都自洽**：inclusive 读法下 read 是 input 的子集；remainder 读法下 read 是独立的一段。counter 无法判别，**estimate 前驱是唯一信号，而它是启发式**——一个声明 inclusive 的网关若先发一个偏大的估算，就会被读成 remainder。

**这是 K1 的真实证据边界**，必须随结论引用。它同时解释了为什么本行在矩阵中标记 `unresolved`。

### 3.3 缺失 vs 显式零：结构上不可区分（结论 A-7）

`TestUsageMissingAndExplicitZeroAreIndistinguishable` 断言 `{"output_tokens":2}` 与 `{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}` 的读数**逐字段相同**。

原因：`wireUsage` 的四个 counter 都是 `int`（`anthropic.go:800-805`），JSON 缺失键与显式 `0` 解码为同一个值。**任何需要区分「网关省略了字段」与「网关报了零」的判定，都不能读折叠后的 counter**，只能读原始事件。

这直接限制了 §3.4 与 K2（口径自检诊断）的设计空间：诊断必须在**事件层**记录，不能在归一化后回推。

### 3.4 未决形状：省略 split 的全命中

`delta(input=0, read=0)`（整段 100% 命中、网关省略 split）与「网关没报任何 cache counter」在 counter 上完全同形。当前读数是 `hit=0, miss=39149`——**高估 miss**。

- 本网关**未观察到**该形状（联合结论 §2.6 已列为 K1 的已知盲点）。
- 判定：**未决**。它需要 §3.3 所述的事件层诊断（即 K2）才能变成可观测，本轮不实现（超出 Agent A 的写集边界，且 K2 属 C 的候选登记）。
- 下游影响受控：该形状下 `hit == 0`，按 A v1 契约必须表述为「未报告 cache read」，不是「冷启动」。措辞正确时不会产出错误的缓存生命周期结论。

---

## 4. 本轮发现的 K1 折叠缺口（结论 A-4）

### 4.1 现象

矩阵第 9 行在修改前失败：

```
事件: start(39149,0,0,0) → start(29,0,33408,0) → delta(33437,0,0,2)
修改前: prompt=66845 hit=33408 miss=33437     ← prompt 被重复计入
期望:   prompt=33437 hit=33408 miss=29
```

### 4.2 机制

`merge` 的 split-free 分支无条件执行 `u.in = usage.InputTokens`。当一个 split-free 事件在 split **之后**到达时，它把**整段 prompt**（33,437）写进 `u.in`，而 `u.cacheRead` 仍是已服务的 33,408，于是 inclusive 折算得到 `prompt = 33437 + 33408 = 66845`——同一段 cache read 被算了两次。

这与 K1 修复前的缺陷**同源**：都是「用错事件描述这次服务」。K1 修的是「split 之前取到 estimate」，这一处是「split 之后被重申覆盖」。

### 4.3 修复

`stream_usage.go` 增加一个状态位：

```go
// haveSplit records that a split-bearing event has been folded, so the reading
// describes how the request was served and no later split-free event may
// replace its input reading.
haveSplit bool
```

split-free 分支改为 `if usage.InputTokens != 0 && !u.haveSplit`；split 分支末尾置 `u.haveSplit = true`。

**最小性**：只加一个布尔位与一处条件，不动既有判定式、不动 `estimateInput` 的语义、不动 `exclusive` 的推导。`estimateInput` 的更新也被一并收进守卫，因此 split 之后到来的更大 estimate（矩阵第 14 行）同样无法污染读数。

**回归证据**：修改后 `internal/provider/anthropic`、`internal/agent`、`internal/team`、`internal/stats`、`internal/cachelab`、`internal/cli -run 'Cache|Usage|Team'` 全绿；`scripts/cache-guard.sh` 10/10 case 通过；native/LongCat 的非回归守卫（§2.2）全部保持。

### 4.4 严重度评估（诚实标注）

- 该形状**未在真实抓包中出现**：本网关的 `message_delta` 与第二次 `message_start` 都带 split（联合结论 §2.1 的 3 个 usage 事件）。
- 因此这是**防御性修复**，不是对已观测跌幅的解释。它不改变任何历史数字，也不构成 §5 那 ~19pp 的解释。
- 之所以修：它是 K1 同源缺陷的另一半，留着会让「split 后重申」这一网关演进方向变成静默的错误读数，且修复成本是一个布尔位。

---

## 5. 跨路由口径耦合（结论 A-5，本轮已修复）

### 5.1 缺陷

`c.deepseek` 一个标志位曾同时决定两件无关的事：

| 用途 | 位置 |
|---|---|
| 推理回放义务（`RequiresAssistantReasoningReplay`） | `anthropic.go` |
| **usage 归一化约定**（`inclusiveInput`） | `anthropic.go` |

`c.deepseek` 的取值由 `reasoning_protocol` 与 endpoint 共同决定，因此**推理协议的选择会改变 token 记账口径**。一个配置开关能移动一个 token 读数，这本身就是缺陷。

### 5.2 实测（同一份字节，不同路由声明；修复前）

LongCat 形状 `start(无 usage) → delta(13,5,7,3)`：

| 路由声明 | `inclusiveInput()` | 读数 |
|---|---|---|
| `reasoning_protocol="deepseek"`（成员池 `Provider=deepseek` 的默认） | true | `prompt=13 hit=7 miss=6 write=5` |
| `reasoning_protocol="none"` / native | false | `prompt=25 hit=7 miss=18 write=5` |

native 形状 `start(39047,0,39040,4)`：

| 路由声明 | 读数 |
|---|---|
| native | `prompt=78087 hit=39040 miss=39047` |
| `deepseek` | `prompt=39047 hit=39040 miss=7` |

影响面实测：`memberReasoningProtocol`（`internal/cli/team_backend_build.go`）在 endpoint 无法分类时按 `Provider` 回落。`Provider=deepseek` + `api.longcat.chat/anthropic` → 声明 `deepseek` → 用 inclusive 口径读 LongCat 形状，`prompt` 少 12（48% 偏差）。

### 5.3 修复

新增 `client.inclusiveUsage`，与 `client.deepseek` 分开：

```go
// inclusiveUsage is the route's declared cache-accounting convention: true
// when input_tokens already covers the cache counters. See streamUsage for
// why it is not derived from deepseek, which is a replay obligation.
inclusiveUsage bool
```

取值规则：`inclusiveUsage = openai.IsDeepSeek(root)` —— **只看 endpoint，不看 `reasoning_protocol`**。理由：

- 只有官方 DeepSeek 端点文档化了「`input_tokens` 覆盖 cache counter」；
- 其余所有 Anthropic 兼容端点跟随 native Messages 契约，`input_tokens` 是未缓存部分；
- 推理协议是「下一个请求要回放哪个 reasoning 块」，与「这次响应的 counter 怎么读」无关。

`inclusiveInput()` 改为 `c.inclusiveUsage`。**未改动** `deepseek` 的任何语义：推理回放、思考开关、输出预算、context window 共享全部不变。

### 5.4 回归证据

| 测试 | 断言 |
|---|---|
| `TestUsageConventionIsIndependentOfTheReplayObligation` | 两个 client 携带**相同**的 `deepseek`，仅口径不同，读数按各自契约不同 |
| `TestUsageConventionFollowsTheEndpointNotTheProtocol` | 同一个非官方网关，在 `""`/`auto`/`deepseek`/`none` 四种 `reasoning_protocol` 下读数**逐字节相同** |
| `TestUsageOfficialEndpointStillDeclaresTheInclusiveConvention` | 官方端点仍声明 inclusive，读数仍是 `prompt=39047 hit=39040 miss=7` |

**关键不变量**：修复前后，**所有已观测形状的读数完全不变**。实测对照（成员网关的真实字节）：

| 形状 | 修复前 | 修复后 |
|---|---|---|
| warm（estimate + split + delta） | `prompt=33437 hit=33408 miss=29` | **相同** |
| warm（delta-only split） | `prompt=33437 hit=33408 miss=29` | **相同** |
| cold（estimate + 整段） | `prompt=33437 hit=0 miss=33437` | **相同** |
| 成员 ~400K warm | `prompt=347762 hit=347648 miss=114` | **相同** |
| 裸请求 33K warm | `prompt=33440 hit=33408 miss=32` | **相同** |

原因是 K1 的 `exclusive` 覆盖：真实网关的 split 事件 `cache_read (33,408) > input_tokens (29)` 本身就构成矛盾，无论声明哪个约定，覆盖规则都把它读成 remainder（§3.1）。**修复改变的是「没有矛盾时由谁决定」**——那里本来就不该是推理协议。

### 5.5 残余边界

- 修复后，**声明为 exclusive 的路由上 `exclusive` 覆盖不再起作用**（它只会把 inclusive 改成 exclusive，而默认已是 exclusive）。实测矩阵第 11、12 行仍 `verified`，因为它们由算术强制，与声明无关。
- 矩阵第 13 行（delta-only 且 `input > read`）**仍然未决**：修复后它在成员网关上的读数从 39,047 变为 78,087。这是**方向正确的一步**（该网关无任何观测显示 `input_tokens` 覆盖 cache counter），但 counter 本身仍不足以判定，行状态保持 `unresolved`。
- 跨 route 的绝对数字**仍不可直接比较**：不同 route 的 shapes 与声明都不同。§6 的「有限通过」保留。

---

## 6. 按 Provider 的判定（交付物 5）

| Provider / 路由 | 判定 | 依据 |
|---|---|---|
| **native Anthropic**（`api.anthropic.com`） | **通过** | `inclusiveInput()` 为 false；矩阵第 1–3 行；非回归守卫 §2.2。native 从不发 estimate 前驱，remainder 规则在它上面**惰性**。 |
| **LongCat Anthropic**（`api.longcat.chat/anthropic`） | **通过** | delta-only 形状由既有真实测试固定（矩阵第 4–5 行）。修复前，池条目 `Provider=deepseek` 会让 `memberReasoningProtocol` 返回 `"deepseek"` 并把口径翻成 inclusive（同一份字节 `prompt=13` 而非 25）；§5 的修复把口径改为只看 endpoint，因此该翻转**不再可能**。 |
| **DeepSeek 官方 Anthropic 端点**（`api.deepseek.com/anthropic`） | **通过** | `openai.IsDeepSeek` 为真 → `inclusiveUsage=true` → inclusive 口径，与官方语义一致。修复前 `reasoning_protocol="none"` 会连口径一起关掉；修复后它**只**关回放。 |
| **DeepSeek 兼容网关**（如 `aiapi.lejurobot.com`，成员实际使用） | **有限通过** | 矩阵第 6、9–12、14–16 行覆盖其形状，warm 读数由算术矛盾独立判定（§3.1）。**有限**之处：第 13、17 行未决（§3.2、§3.4）。§5 的 protocol 耦合已修复，该网关的读数不再随 `reasoning_protocol` 变化。 |
| **非 Anthropic 方言**（`provider/openai` 的 OpenAI/MiMo/DeepSeek 顶层 cache 字段；`provider/responses`） | **未审** | 联合结论 §2.6 的待攻击点 2。`openai.normaliseUsage`（`openai.go:1043`）走的是**单事件** usage（无跨事件折叠），结构性风险与 Anthropic 方言不同，但**本轮未验证**。 |
| **其他兼容端点** | **未决** | 无 fixture、无观测。 |

**不能用单网关结果概括所有兼容端点**：§6 的每一行只覆盖它自己那一行的形状与声明。不同 route 的 shapes 与声明都不同，因此跨路由的绝对数字**不可直接比较**。

---

## 7. 写集与边界遵守

| 项 | 状态 |
|---|---|
| `internal/provider/anthropic/stream_usage.go`：`haveSplit` 守卫 | ✅ |
| `internal/provider/anthropic/anthropic.go`：拆出 `inclusiveUsage`（A-5） | ✅ |
| 只加 `internal/provider/anthropic/stream_usage_test.go` | ✅ |
| 未改 `internal/team/**` 统计契约与分母 | ✅ |
| 未改 `internal/cachelab` 实验结果解释 | ✅ |
| 未改 Team member 的 provider-visible 请求构造 | ✅（`git diff` 未触及 `buildRequest` 及其调用路径） |
| 未写入 prompt、工具正文、凭据、完整响应 | ✅（测试只用合成 counter） |

> `internal/cachelab/zz_partc_probe_test.go` 是**另一个 Agent** 的未跟踪文件，本轮未触碰。

---

## 8. 复现

```bash
# 矩阵与全部 K1 形状测试（17 行子测试 + 10 个独立测试）
go test ./internal/provider/anthropic/ -run TestUsage -v -count=1

# 离线验证（本轮实测全绿）
go test ./internal/provider/... ./internal/agent/ ./internal/team/ ./internal/stats/ ./internal/cachelab/ -count=1
go test ./internal/cli/ -run 'Cache|Usage|Team' -count=1
go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...
go run ./tools/repolint
bash scripts/cache-guard.sh
```

### 8.1 repolint 红项：8 处 → 0 处

HEAD 上**已有** 8 处违规（非本轮引入）。本轮一并清零：

| 文件 | 规则 | 处理 |
|---|---|---|
| `internal/cli/chat_tui_team_render.go` | essay ×1 | 注释收敛到 3 行 |
| `internal/cli/chat_tui_team_reset.go` | essay ×2 | 同上 |
| `internal/cli/chat_tui_team_session.go` | essay ×3 | 同上 |
| `internal/cli/team_history_sync.go` | essay ×1 | 同上 |
| `internal/cli/team_replay.go` | essay ×2 | 同上 |
| `internal/control/history_sync.go` | essay ×1 | 同上 |
| `internal/provider/anthropic/messages_usage.go` | essay ×1 | 同上 |
| `internal/cli/team_task_service.go` | file-size（834 行） | 抽出纯函数到 `internal/cli/team_report_shape.go`（760 + 87 行） |

**未删除任何信息**：所有收敛都是把同一事实写得更短，或把纯函数整体搬到新文件。`team_report_shape.go` 只装 `taskIDs` / `undrivenTaskError` / `undrivenTasksError` / `parseRoles` / `inferRoles` / `containsRole` —— 不触碰 store、board 或 backend 的函数。

结果：`go run ./tools/repolint` → `repolint: clean (1146 baselined findings)`，退出码 0。**baseline.json 未被修改**（`-update` 未运行）。

---

## 9. 未验证清单（必须随结论引用）

1. 矩阵第 13 行（delta-only 且 input > read）：两种约定自洽，estimate 前驱是启发式，**未决**。
2. 矩阵第 17 行（省略 split 的全命中）：与「未报告 cache read」同形，**未决**；需事件层诊断（K2）。
3. 矩阵第 15 行（write-only split）：本网关 `cache_creation` 恒为 0，**无真实观测**。
4. 缺失 vs 显式零：`wireUsage` 结构上不可区分（§3.3）。
5. 非 Anthropic 方言（`openai` / `responses`）的同类跨事件混用：**未审**。
6. §5 的 protocol/口径耦合：**本轮已修复**；残余边界见 §5.5。
7. 联合结论 §5 的 ~19pp 剩余跌幅：本报告**不解释**它。K1 及其缺口修复都不改变历史账本，也不改变 provider-visible 字节。

---

## 10. 对其它两方的接口影响

- **对 B**：§6 的「有限通过」意味着 B 的任何跨 route 命中率对比必须声明 route 的 endpoint（口径由它决定）。§5 修复后 `reasoning_protocol` 不再影响读数，但不同 endpoint 的 shapes 仍不同。
- **对 C**：§3.1 把 K1 在本网关上的证据强度从「依赖私有 oracle 的假设」提升为「算术证据」。C 的 §9 判定式（`CacheHitTokens == message_delta.cache_read`）仍成立，但**不再是唯一路径**——`read > input` 的算术矛盾足以独立判定该形状。C 的 §3.4 影响面评估无需修订。
- **对 A 自身契约（v1）**：本轮**不改** §3 的任何定义，因此**不升版本号**。矩阵是 v1 之下的 Provider 侧证据，不是契约修订。
