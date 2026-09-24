# Team member 缓存：Part C 独立复核、夹具修正与合流准入（后续轮）

> 执行依据：`TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md` §3「Agent C」、§6 验收门槛、§9「派给 Agent C」。
> 日期：2026-09-24。基线：`5241384b6`（Team-agent）+ 本轮 Agent C 未提交改动。
> 写集：`internal/cachelab/**`、`internal/team/cachereport*`、`internal/team/cacherequest.go`、
> `internal/cli/team_cache_report.go`、`internal/cli/team_usage_publish.go`、`internal/cli/team_cache_audit.go`、本文件。
> **未触碰** Agent A 的 Provider 语义实现与上下文维护状态机、Agent B 的 Team 请求构造与任务组成。
>
> **本文引用的「计划」= `TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md`**（该文件是本轮唯一在场的执行方案）。

---

## 0. 结论摘要

| # | 结论 | 强度 | 影响 |
|---|---|---|---|
| **N-1** | `cachelab` 的 `resolve()` 遇到**混合 usage 词表**（同一响应同时带 `input_tokens` 与 `prompt_tokens`）时直接判 `unresolved_vocabulary`，把一个**确实带了 cache read 的响应读成「无 split」**。已改为按词表依次尝试，并记录实际解析成功的 `Shape`。 | **证据**（§2.1） | 受影响的臂必须重跑；B 的 `no_cache_split` 计数此前可能虚高 |
| **N-2** | 解析器缺少**显式零 vs 键缺失**的判别测试，且「无 cache read」与「词表无法解析」被归为同一 problem。已补：`cache_read_input_tokens: 0` 是**全 miss 的实测值**，键缺失才是「未报告」。 | **证据**（§2.2） | 全命中省略 split 与显式零不再可能被混淆 |
| **N-3** | `Recorder.finishSample` 先发布样本、后写 journal，**读回 journal 的断言与写入竞争**。`TestRecorderRecordsExactRequestAndRawUsage` 在 HEAD 上 `-count=30` 即复现失败。已改为**先写 journal、后发布**。 | **证据**（§2.3） | 这是 HEAD 已存在的偶发红项，非本轮引入 |
| **N-4** | 成员观测新增**会话身份维度**（`session_id_hash` / `session_ordinal` / `session_first_request_seq`），使 **Context Rescue 轮转后的首个请求**可被判为冷前缀。此前 `HasPrevRequest` 是**写入者进程级**的，轮转后的首请求读起来是「warm」。 | **证据**（§3.1） | 直接决定 `cold_start_miss_tokens` 与 `rotations` 能否成立 |
| **N-5** | 报表新增**未命中归因分区**（7 类，互斥、可对账），其中 `provider_residual_unexplained` 是**「本地无法解释」的显式类**。 | **证据**（§3.2） | 计划 §3 要求的「普通追加 / rewrite / rescue 冷前缀 / Provider 未知」四问由此可答 |
| **N-6** | 报表新增**每逻辑 turn 成本**与**维护成本**，并把**本数据集测不到的指标逐条写进输出**（任务质量、延迟、维护操作数、provider cache key、scope/TTL、attempt 行）。 | **证据**（§3.4） | 计划 §6.1 的指标清单里，可测的已可测，不可测的**不再以 0 出现** |
| **N-7** | 条件矩阵（Baseline / A-only / B-only / A+B / Rescue）已登记为**独立条件臂**，每个条件自带开关与门禁；Rescue 被标记为**非例行臂**，且注册校验会拒绝在它下面跑请求字节变量臂。 | **证据**（§4） | 计划 §5 P3 的实验矩阵可执行，且不可能被误配成「优化臂」 |
| **N-8** | **本轮的 12 与 30 条真实成员会话**：`cold_prefix` 吃掉了 **100% / 99%** 的 scoped miss，`append_only_expected` 只占 0% / 1%，`provider_residual_unexplained` 为 **0**。 | **证据**（§5） | 见 §5.3 的**适用范围限制**——这是受控短会话，不是生产基线 |
| **N-9** | **冷前缀判定被收紧**：此前「无前驱」即判冷，这会让**路由级账本行**（`HasPrevRequest=false`、无 `session_request_seq`）整批读成「冷启动」——把一整个数据集的 miss 归给一个没人观测过的冷启动。现在冷前缀需要**写入者自己的会话状态**。 | **证据**（§3.3） | 账本行的 miss 归因现在落在 `undiagnosed`（未观测），而非伪造的冷启动 |
| **N-10** | 新增 **`K1-warm-fold` 回归守卫臂**：warm 响应的未命中余量是**两个事件之差**，不是任何单个事件里的数，所以它的期望值只能来自 provider 自己的读数。折叠回归**静默**（`prompt == hit + miss` 两种情况下都闭合），只有独立读数能抓住它。 | **证据**（§4.2） | 给了 K1 一个**可复跑、可预注册**的守卫条件 |
| **N-11** | 合流门禁：`go build ./...`、六个包的测试全绿（team / cachelab / stats / provider ×3 / cli 子集）、`go vet` 通过、`scripts/cache-guard.sh` 10/10、repolint RED SET 与 HEAD **逐字节相同**。 | **证据**（§6） | Agent C 未新增任何仓库违规 |

### 0.1 合流轮新增（2026-09-25，A/B/C 全部交付后）

| # | 结论 | 强度 | 影响 |
|---|---|---|---|
| **N-12** | **B 的 `messages_rewritten` 已接入观测并成为独立归因类** `messages_rewritten_unclaimed`：一个改写了 provider 已读字节、却没有任何 fold/prune/truncate 认领的请求，**不再落进「本地无法解释」那一类**。这正是计划 §7 F4 的入口。 | **证据**（§11.1） | F4 从「有字段但没人读」变成「可归因、可计数、可渲染」 |
| **N-13** | **A 的维护成本计数器已接入报表**：`summary_requests` / `projection_installs` / `rescue_count` / `repeat_blocks` 经 `.usage.json` 的 `maintenance` 段进入会话累计行，并带**发布成员数**（`published by N of M`），使部分合计不会被读成全会话合计。 | **证据**（§11.2） | 计划 §6.2 的 `compaction_requests_per_turn` 一族首次可读 |
| **N-14** | **B 的 `system_hash` / `tools_hash` 已接入**逐请求记录（此前只有合并的 `stable_prefix_hash`），覆盖率账本新增 `message_shape_comparable`。 | **证据**（§11.1） | 计划 M1 的 `RequestShape` 字段在 C 侧全部落地 |
| **N-15** | **合流后的实测**：`message_shape_comparable=12/12`、`session_identity=12/12`、`cold_prefix` 仍吃 100% 的 scoped miss、`messages_rewritten_unclaimed` 为 **0**；30 条会话的 `maintenance spend: ... (published by 3 of 3 members)` 显示 A 的计数器**在生产路径上真的被读取**。 | **证据**（§11.3） | 三方数据面已打通；**行为结论仍不放行**（§11.4） |

**一句话**：本轮把「哪些 miss 是本地造成的」从**无法回答**变成**可分区、可对账、可复跑**；并修掉了夹具里一个会让「有 cache read」被读成「无 split」的词表缺陷。**但 §5 的实测数字只支持「受控短会话里冷启动是唯一大头」，不支持任何生产命中率结论。**

**合流轮补充**：A 的状态机与 B 的前缀稳定性交付后，C 把 B 的消息数组指纹与 A 的维护计数器**接进了落盘与报表**（§11），三方数据面因此打通。**但这只打通了观测，没有新增任何行为证据**——条件矩阵仍未执行，桶级样本仍不足，F4 入口与 rescue 路径在真实负载上都还没被触发过（§11.4、§11.5）。

---

## 1. 口径差异表：三方数据不得互相合并（计划 §3 交付物 3）

| 数据集 | 生产者 | 落盘位置 | 是否带 `request_count_source` | C 的读取方式 |
|---|---|---|---|---|
| 成员记录 | owner writer（`internal/cli/team_usage_publish.go`） | `<owner dir>/.cache_requests.jsonl` | ✅ | `team.BuildCacheReport`（`source: member records`） |
| 历史账本 | `internal/stats/recorder.go` | `config.StatsDir()/<day>.jsonl` | ✅（新构建） | `team cache-audit`（`source: stats-ledger`） |
| 实验 journal | `internal/cachelab` Recorder | 操作者指定路径 | ❌ | `cachelab.RenderReport` / `Summarize` |
| 会话累计 | owner writer | `<owner dir>/.usage.json` | ✅ | `CacheReport.Sessions`（**不是** per-request 指标） |

**C 的判定（计划 §3 交付物 3）**：

1. **实验 journal 不是成员账本**：`cachelab.Sample` 没有 `member_id` 语义上的 Team 归属（它记的是实验臂的 `MemberID` 字符串），也没有 `request_count_source`。两者**不得相加**，`RenderReport` 与 `cache-report` 的输出**不得互为基线**。
2. **账本不是成员基线**：`ledgerRecords` 合成 `TeamID: "stats-ledger"` 与由 model ref 推出的 route 标签，**不是成员身份**。这一点在 audit 输出里自带声明。
3. **C 本轮没有修改任何统计分母**：`cacheRequestIsBaselineEligible` 的五条件、`CacheGroupExclusions` 的各类计数、`AllSamplesTotals()` 的对账等式**逐字未动**（`git diff HEAD -- internal/team/cachereport.go` 只新增字段与调用）。

---

## 2. `cachelab` 夹具修正（计划 §3 交付物 2）

### 2.1 N-1：混合词表被读成「无 split」

**现象**。旧 `resolve()` 用 `anthropic && !openai` / `openai && !anthropic` 的互斥判定，两者同时成立时直接 `UsageProblemUnresolved`。而 DeepSeek 兼容网关在 Anthropic 事件流里嵌一个 OpenAI 风格 usage 对象是**常见形状**，`billing_usage.openai_usage` 就是例子。结果是：一个**明确带了 `cache_read_input_tokens: 90` 的响应**被判为「没有 cache read」。

**修复**。按词表依次尝试（Anthropic 优先，因为事件流说的是它），成功即返回并记录 `Shape`；两者都失败才 `unresolved`。

**逐形状验证**（`TestUsageParserRefusesAMixedVocabulary` 等）：

| 响应 | 旧结果 | 新结果 |
|---|---|---|
| `{input_tokens:10, cache_read_input_tokens:90, prompt_tokens:100, prompt_cache_hit_tokens:90}` | `unresolved`，无 split | `anthropic`，hit 90 / miss 10 / prompt 100 |
| `{prompt_tokens:1000, prompt_cache_hit_tokens:900, cache_read_input_tokens:0}` | `unresolved` | `openai`，hit 900 / miss 100 |
| `{input_tokens:42, prompt_tokens:42}`（两词表都不完整） | `unresolved` | `no_cache_read`（更具体的拒因） |
| `{prompt_tokens:10, cached_tokens:50}` | `negative_split` | `negative_split`（不变） |
| `{input_tokens:100}` | `no_cache_read` | `no_cache_read`（不变） |

**受影响面**：任何**同时**携带两种词表的响应。B 的 9 臂在旧夹具下 `usage_split=true`（B 的 journal 显示未触发），**但 C 复现 B0-pilot 时曾出现 9/9 `no_cache_split`**——两者不相容，正是这个缺陷的表现。**结论：B 的 `no_cache_split` 计数需要在新夹具下重跑确认。**

**判定式（重跑时逐请求断言）**：

```text
若响应含 cache read 语义（任一词表给出非零 read / cached / hit）：
    Sample.UsageSplit == true  且  Sample.CacheHitTokens == 该值
仅当两词表都无法给出 read 时：UsageSplit == false 且 UsageProblem 为具体拒因（非 unresolved）
```

### 2.2 N-2：显式零与键缺失

`has()` 一直能区分（键存在即为真），但**没有测试固定这一点**，且两种拒因被合并。现在：

- `cache_read_input_tokens: 0` → `Split=true`，`Hit=0`，`Miss=100`（**全 miss 是实测值**）；
- 键缺失 → `Split=false`，`Problem=no_cache_read`（**未报告，不是零率**）。

这是计划 §2.2「不通过降低统计分母、排除低命中样本或**修改 usage 归一化**来『提高』命中率」的**直接封堵**：一个把「有 cache read」读成「无 split」的解析器，等于在归一化层把样本从分母里删掉。`TestUsageParserDistinguishesAMissingKeyFromAnExplicitZero` 固定它。

### 2.3 N-3：journal 读回的写入竞争（HEAD 已存在的偶发红项）

`finishSample` 旧顺序：`r.samples = append(...)` → 解锁 → `journal.Append(sample)`。
`TestRecorderRecordsExactRequestAndRawUsage` 的断言是「`WaitForSamples(1)` 之后读 journal 必须含该样本」。样本可见早于 journal 落行，于是**该断言与写入竞争**。

复现证据（两侧同样失败，故**非本轮引入**）：

```text
/tmp/partC-head (纯 HEAD):  go test ./internal/cachelab/ -run TestRecorderRecordsExactRequestAndRawUsage -count=30
  --- FAIL: TestRecorderRecordsExactRequestAndRawUsage
      recorder_test.go:140: journal does not carry the recorded sample
```

**修复**：先 `journal.Append`，后发布样本。修复后 `-count=30` 通过。这使「等样本再读 journal」成为一个**可靠的观测协议**，而不只是碰巧。

### 2.4 N-7：条件矩阵

`RegisteredConditions()` 登记 5 个条件，每个带 `Switches`（复现配方）与 `Unchanged`（不得改动项）：

| 条件 | 开关 | 性质 |
|---|---|---|
| `baseline` | `cache_aware_compaction=false, context_rescue=false` | 参照 |
| `a_only` | `cache_aware_compaction=true, context_rescue=false` | 维护状态机 |
| `b_only` | 与 baseline 同开关（形状稳定性不是开关） | 前缀稳定性 |
| `a_plus_b` | 同 `a_only` | 叠加 |
| `rescue_enabled` | 两者都 true | **`NeverRoutine`** |

`ConditionArms()` 为每个条件生成一个 `VariableVersion` 臂，**冻结请求字节**（`Bytes=0, Component="", IntervalMS=0`），并强制 `Gated`——没有真正配置该条件的构建不得跑。

**注册校验的两条硬约束**（`TestRescueConditionIsNotARoutineArm`）：

1. `NeverRoutine` 条件下**不得**注册请求字节变量臂（ladder/component/interval）——否则会把轮转的冷前缀成本读成「优化的收益」；
2. 基线臂在 rescue 下**可以**注册（条件本身就是变量）。

**C 的坦白**：`b_only` 的开关与 baseline 相同，因为「provider-visible 形状稳定性」在本仓库里**不是一个配置开关**，而是 Agent B 的实现改动。这使 `b_only` 臂的复现配方**依赖于具体构建**，而不是配置——`Switches` 因此不能完整表达它。这一缺口写在 §7 L-3，**不掩盖**。

---

## 3. 观测契约扩展（计划 §6.1 指标清单落地）

### 3.1 会话身份与 Rescue 冷前缀

**问题**。`MemberCacheRequest.HasPrevRequest` 是**写入者进程级**的：Context Rescue 轮转后，成员仍在同一个 publisher 里，所以轮转后的首请求 `HasPrevRequest == true`、`SessionRequestSeq` 很大——**它读起来是 warm**，尽管它面对的是一个全新的 provider 前缀。

**修复**。观测新增三个字段（`internal/team/cacherequest.go`）：

| 字段 | 语义 | 取值规则 |
|---|---|---|
| `session_id_hash` | 本地会话身份的 digest，**不是身份本身** | `sha256(SessionID)[:8]`，写入者进程内计算 |
| `session_ordinal` | 该写入者见过的**第几个**不同会话身份，1-based | 0 = 该请求没带身份（**未知，不是第一个**） |
| `session_first_request_seq` | 当前会话开启时的写入者序号 | 与 `SessionRequestSeq` 比较可识别轮转首请求 |

同批还补了 **`system_hash` / `tools_hash`**（`internal/team/cacherequest.go`）：稳定前缀的**两半**分别落盘，此前只有合并后的 `stable_prefix_hash`。计划的 M1 接口契约把 `system_hash` 与 `tools_hash` 列为 `RequestShape` 的独立字段——合并值无法回答「是 system 动了还是 tools 动了」，而这两者的处置完全不同（前者是身份/角色提示，后者是工具面）。

覆盖率账本同步新增 `session_identity_present/absent`：**没有会话身份的样本，其冷状态无法判定**，这一点现在与其它字段覆盖率一样被披露，而不是让判定静默地默认成「warm」。

**上游依据**（已核实）：成员 sink 看到的是**已盖戳**的事件——`control.New` 把 `turnEventSink` 装在成员 sink **外层**，`Usage` 事件在 `ledger.AppendEnvelope` 里被盖上 `SessionID` / `TurnID` / `Sequence` 后才 `publishInner`。因此 `e.SessionID` 在成员观测点**非空**（除非事件在 turn 外发出）。

**不能观测的（已核实并写入限制）**：
- `RuntimeEpoch` 在 CLI/Team 路径上**恒为空**（只有 Desktop 调 `SetTurnEventRoutingMetadata`）；
- `AttemptID` 在 `Usage` 事件上**从未被设置**；
- 轮转的 lineage / `ParentSessionID` 只在子会话的 `header.json` 与一条 session event 里，**不经过 `event.Sink`**；
- turn 外发出的 usage（如标题生成）**不带身份**，`observeSession` 会提前返回、**不推进 ordinal**——「没身份」不被当成「新会话」。

`TestCacheMissCauseReportsARotationAsCold` 固定：有 ordinal 的轮转首请求判为冷前缀；**去掉 ordinal 后不得判为冷**（老文档不能被假定）。

### 3.2 未命中归因分区（计划 §3 要求的三问）

`CacheMissCause` 是**闭集**，每个 scoped 样本落**恰好一类**（`internal/team/cachemisscause.go`）：

| 类 | 判据 | 计划对应 |
|---|---|---|
| `cold_prefix` | 写入者首请求，**或**轮转首请求 | 「rescue 新 session 的冷前缀 miss」+ 冷启动 |
| `rewrite` | 前缀移动且 reason ∈ `cachereason.Rewrite`（`compact_auto`/`prune`/`truncate`/…） | 「compaction/prune/truncate 造成的 rewrite miss」 |
| `structural` | 前缀移动且 reason 全是 `Structural`（`system`/`tools`/`session_context`/…） | 框架变更，无可 fold |
| `unexplained_prefix_change` | 前缀移动但**无 reason** 或 reason **不在词表内** | 词表漂移可见 |
| `append_only_expected` | 前缀未动，且 `miss ≤ 前缀增长 + 128` | 「普通追加造成的 miss」 |
| `provider_residual_unexplained` | 前缀未动，且 `miss >` 上述上界 | 「Provider scope/TTL/route 造成的未知或外部 miss」 |
| `undiagnosed` | 无前缀诊断 | 「未观测」≠「未变化」 |

**两个方向性设计（都偏保守）**：

1. **128 tok 的追加允许量**：来自 B 的受控实验归纳（命中量恒为 128 的整数倍，未命中是追加内容向上取整到一个块），**不是文档化的 provider 常量**。它被**发布在报表里**（`append_block_allowance`），读者可据同报的每类增长统计重新划分。**允许量偏大 → 样本进「expected append」；偏小 → 进「residual」。**
2. **prompt 缩小时不给追加豁免**：未报告前缀变更却缩小，不是追加，其 miss 留在 `residual`。

**可对账性**（`TestCacheMissCauseIsAPartitionOfTheScopedPopulation`）：各 `requests` 之和 == `coverage.scoped`；各 `hit/miss` 之和 == scoped 的 hit/miss。

**前驱查找按写入者序号**，不按到达顺序（`TestCacheMissCauseTrackerFindsThePredecessorByWriterSequence`）：报表可能建立在被过滤或拼接的样本集上，只有写入者自己盖的 `session_request_seq` 才是可靠顺序。

### 3.3 冷前缀判定必须要求写入者状态（N-9）

**问题**（自查发现）。最初的实现是「`HasPrevRequest == false` 即冷」。这条规则在成员记录上是对的，但在**路由级账本行**上是错的：`ledgerRecords` 合成的记录**从不设置** `HasPrevRequest`（也无 `session_request_seq`），于是**每一行都会被判成冷前缀**——把一整个数据集的 miss 归给一个**没人观测过的冷启动**。这是计划 §2.1 与 §2.2 同时禁止的那类结论：「Rescue 生成的恢复块必须严格小于 10K tokens；**新 session 首次请求的冷缓存成本必须单独计量**」（§2.1），以及「不通过降低统计分母、排除低命中样本或修改 usage 归一化来『提高』命中率」（§2.2）。把整批账本行判成冷启动，正是用一个未观测的类别替换真实的归因。

**修复**。冷前缀现在需要**写入者自己的会话状态**，两种形状之一：

1. **轮转首请求**：`session_ordinal > 1` 且 `session_request_seq == session_first_request_seq`；
2. **写入者首个请求**：`!has_prev_request` **且** `session_request_seq > 0`。

账本行两个条件都不满足，落到 `undiagnosed`（未观测），并**在 audit 输出里具名披露**：

```text
  - a miss cause: the cause partition needs the writer's own session state, which a
    route-level row never carried, so every row here is reported as undiagnosed
    rather than attributed
```

`TestCacheMissCauseDoesNotClaimARouteLevelRowWasCold` 固定这条；`TestCacheAuditStatesWhatItCannotEstablish` 固定审计输出里的这句披露。

**代价（如实记录）**：一条**真正来自成员写入者**、但 `session_request_seq` 缺失的旧记录（本轮字段引入之前写的），现在也会落到 `undiagnosed` 而不是冷前缀。这是**有意的保守方向**——「没观测到」不等于「是冷启动」。

### 3.4 每 turn 成本、维护成本、以及**测不到的指标**

`CacheTurnTotals`（计划 §6.1）：

```
turns / requests / requests_without_turn
prompt_tokens_per_turn  hit_tokens_per_turn  miss_tokens_per_turn
completion_tokens_per_turn  requests_per_turn
```

分母是**不同的 `TurnID` 数**，不是请求数——一个 turn 花三个请求仍是一个 turn（`TestCacheTurnCostDividesByDistinctTurnsNotRequests`）。**没有 turn 身份时输出 `n/a`，不是 0**（`TestCacheTurnCostRefusesAPerTurnFigureWithoutTurns`）。

`CacheMaintenanceCost`：

```
rewrite_requests  structural_requests  rotations  rotations_per_100_turns
rewrite_requests_per_turn  cold_start_miss_tokens
  ├─ first_session_cold_miss_tokens   （写入者自己的首个会话，非 rescue 造成）
  └─ rotations 部分                     （rescue 造成的）
finish_reasons                        （完成信号，不是任务质量判定）
```

**`rewrite_requests` 是「宣告计数」不是「操作计数」**：已核实 fold 在**它自己那一轮的 `Prepare`** 里安装，而 reason 在**下一轮** drain——所以是**下一轮请求**报告 `compact_auto`。这个 off-by-one 写在结构体注释、报表行标题（`announcement counts, not operation counts`）和 `CacheReportUnobservable()` 三处。

**`CacheReportUnobservable()`**（计划 §6.1 的诚实边界）逐条列出本数据集**无法**产出的指标，并**打印在报表里**：

1. 任务质量（记录不含任务结果）；
2. provider 请求延迟（usage 事件不含时长）；
3. 维护操作数（只有宣告请求被记录）；
4. provider cache key（本地 hash 覆盖 system+tools，不是 provider 的键）；
5. provider scope / TTL / 逐出 / 账号池（本机完全不可观测）；
6. attempt 级行（多次尝试存为一条聚合，不可拆回）。

`TestCacheReportPublishesWhatItCannotMeasure` 固定这份清单存在且具名。

---

## 4. 与 A / B 的对账

| 接口 | A / B 提供 | C 本轮的处理 |
|---|---|---|
| A 的统计契约 v1 | `request_count_source` 闭集、五条件资格、排除分类、`coverage`、`AllSamplesTotals` | **逐字未动**；C 只**新增**字段（`miss_causes` / `turn_cost` / `maintenance_cost`）与渲染 |
| A 的 `no_cache_split` | `hit+miss == 0` | 与 B 的 `usage_split=false` 同判据；**C 的 N-1 修复让这个计数不再因混合词表而虚高** |
| B 的 9 臂 | 真实 Provider 受控结果 | C **不改其解释**；N-1 意味着**受影响臂需重跑**（§2.1 判定式） |
| B 的 232/168/197 tok/请求 | 已由 C 上一轮判定为 128-block 切分余量 | 本轮**不改该判定**；`append_block_allowance = 128` 采用同一粒度模型，并**显式标注为归纳值** |
| 历史账本降级 | A 判为 per-request 基线不可用 | C **同意**，且本轮**保留分支**：`source` 字段区分 `member records` 与 `stats-ledger`，两者不合并（§1） |

**C 没有做的事**：没有为让结果好看而调整分母、排除规则或历史数值；没有把实验 journal 当成员账本；没有修改 Agent A 的 Provider 语义实现；没有修改 Agent B 的请求构造。

### 4.1 夹具解析改动对 A / B 的接口影响

| 改动 | 影响 A？ | 影响 B？ |
|---|---|---|
| `resolve()` 按词表依次尝试（N-1） | ❌ A 不读 `cachelab` | ✅ **B 的 `no_cache_split` / `unresolved` 计数会变**，受影响臂需重跑 |
| 更具体的拒因（N-2） | ❌ | ✅ `UsageProblem` 取值集合变化，按 `unresolved` 分支的下游需同步 |
| `finishSample` 发布顺序（N-3） | ❌ | ✅ 使「等样本再读 journal」成为可靠协议；B 的重跑脚本可依赖它 |
| 条件矩阵（N-7） | ❌ | ✅ 新增 5 个 `C-*` 臂；B 的 `B*` 臂注册**未改**（只给 `B1` 加了 `Conditions`） |

**A 的接口零变化**：`cachelab` 不被任何生产二进制导入（`internal/cachelab/doc.go` 首段），A 的统计契约与 Provider 实现都不经过它。

### 4.2 `K1-warm-fold` 守卫臂（N-10）

**为什么需要一个专门的臂**。K1 修的缺陷**静默**：适配层对 warm 响应的未命中余量是**两个事件之差**（整段预估事件 − 已缓存读数事件），任何**单个**事件里都没有这个数。旧的逐字段 `max` 折叠把它读成「整段预估 − 缓存读数」，而 `prompt == hit + miss` 这个内部恒等式**在两种折叠下都闭合**——所以账目自检**永远发现不了回归**。

**这个臂登记什么**：`Variable: baseline`（请求字节不变），`Changed` 说明它是折叠回归守卫，期望的 warm 率是 **provider 自己的读数**，不是任何一个事件里的数。它不新增夹具变量，因此可以和 `B1-baseline-repeat` 在同一批样本上并读。

**判定式（跑这个臂时逐请求断言）**：

```text
对每个 warm 响应：
  Sample.CacheHitTokens == 该响应的 settled cache_read 读数
  Sample.PromptTokens   == settled input_tokens + settled cache_read 读数
  Sample.CacheMissTokens == settled input_tokens        （即余量，而非「整段 − 命中」）
```

若某条 warm 样本的 `CacheMissTokens` 明显大于 settled 余量（本网关实测 30–90 tok 量级），即为**折叠回归**。

**B 的 `usage_split` 未受影响**：B 的录制器用的是逐键 last-wins（`internal/cachelab/recorder.go` 的 `rawUsage.merge`），本来就没有这个缺陷；这个臂守的是**生产适配层**的折叠。

---

## 5. 真实 Provider 实测（本轮新增）

### 5.1 会话 A：`TestLiveTeamMemberCacheSession`（12 条 / 3 成员）

> 下列数字来自**收紧冷前缀判定之后**的复跑（§3.3 的 N-9 修复已生效），逐 token 略有波动属正常。

```text
coverage over 12 scoped samples: request_count measured=12 defaulted=0 unrecorded=0 unrecognized=0
  route_bucket=12/12 model_ref=12/12 usage_source=12/12 prefix_diagnostics=12/12 (present/scoped)
  session_identity=12/12 (present/scoped); a sample without one cannot have its cold status decided
miss causes over 12 scoped samples (partition; append allowance 128 tok):
  cold_prefix            requests=3   eligible=3   miss=191267  (100% of scoped miss)
  append_only_expected   requests=9   eligible=9   miss=411     (0% of scoped miss)
per logical turn (turns=12 requests=12 requests_without_turn=0)
  prompt/turn=66725 hit/turn=50752 miss/turn=15973 completion/turn=2 requests/turn=1
maintenance cost: rewrite=0 structural=0 rotations=0
  cold_start_miss_tokens=191267 (first session 191267, rotations 0)
  finish reasons: stop=12
overall  requests=12 members=3 weighted=76.1% member_simple_mean=79.0% p10/p50/p90=5.6%/99.4%/100.0%
warm_candidate  requests=9  weighted=99.9%  miss/req=54
first_request   requests=3  weighted=3.1%   miss/req=64646
```

### 5.2 会话 B：`TestLiveTeamMemberCacheBaselineTeam`（30 条 / 3 成员，跨过 30/3 门槛）

```text
coverage over 30 scoped samples: request_count measured=30 defaulted=0 unrecorded=0 unrecognized=0
miss causes over 30 scoped samples:
  cold_prefix            requests=3   eligible=3   miss=190600  (99% of scoped miss)
  append_only_expected   requests=27  eligible=27  miss=2281    (1% of scoped miss)
per logical turn (turns=30 requests=30 requests_without_turn=0)
  prompt/turn=65685 hit/turn=59255 miss/turn=6429 completion/turn=21 requests/turn=1
overall  requests=30 members=3 weighted=90.2% member_simple_mean=90.5%  [ok]
warm_candidate  requests=27 weighted=99.9% miss/req=84
first_request   requests=3  weighted=3.1%  miss/req=63533
```

### 5.3 这些数字支持什么、不支持什么

**支持**（证据）：

1. **归因分区在真实会话上闭合**：`cold_prefix + append_only_expected == scoped`，`provider_residual_unexplained == 0`。也就是说，在这两次会话里，**每一个未命中 token 都能被「冷启动」或「追加内容」解释**——没有一块属于「本地无法解释」。
2. **轮转计数为 0**：这两次会话没触发 Context Rescue，`rotations=0`、`rotations_per_100_turns=0`，`cold_start_miss_tokens` 全部归 `first_session`。这是**否定性证据**，不是阳性证据。
3. **append 豁免未被滥用**：30 条会话里 27 条 append-only 样本合计 miss 2,281 tok、均 84 tok/请求；12 条会话里 9 条合计 411 tok、均 46 tok/请求。两者都**远小于 128 的允许量**——说明这些样本的 miss 确实只是被追加的尾巴，而不是被允许量「收编」的残差。
4. **会话身份覆盖率 100%**：`session_identity=12/12`（与 30 条会话同）。冷前缀判定所需的写入者状态**全部在场**，没有样本因为缺状态而落到 `undiagnosed`。
5. **warm 请求的 miss/请求是 54 / 84 tok**，与 B 在受控裸请求上观察到的 30–90 tok **同量级**。

**不支持**（必须与数字同读）：

1. **这不是生产基线**。两次都是**固定脚本、固定 route、单账号、串行**、首条消息人工填充分级、无压缩、无工具循环。任务组成、并发、压缩边界**全都不在里面**。
2. **`cold_prefix` 吃掉 100% / 99% 的 scoped miss 是分母效应**：12 条里 3 条是冷启动，而冷启动每条 miss 6.4 万 tok；warm 每条只有 54 tok。**这不是「冷启动是生产问题」的结论**，是「这个脚本的 warm 请求几乎不 miss」的结论。
3. **样本量仍不足**：桶级全部 `insufficient_sample`（`32k_128k` 20 请求 / 2 成员，未达 30/3）。**不得**据此做跨成员推断。
4. **`rewrite_requests = 0`**：会话没触发压缩，所以「fold 边界是否造成 miss」在这两次数据里**没有观测**，不是「已排除」。
5. **延迟与任务质量不可测**（§3.4），因此计划 §6.2 的「质量不劣、延迟不越界」两项**本轮无法验收**。

**判定**：本轮数据支持「**归因机制可用**」，**不支持**任何命中率或缓存策略结论。

---

## 6. 合流门禁（计划 §5 P1 门槛）

在**隔离快照**（`git archive HEAD` + 只覆盖 Agent C 文件，排除另两个 Agent 的在途改动）上执行：

| 检查 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go test ./internal/team/` | **ok** |
| `go test ./internal/cachelab/` | **ok**（含新 16 个解析/条件测试） |
| `go test ./internal/stats/` | **ok** |
| `go test ./internal/provider/...` | **ok** |
| `go test ./internal/cli/ -run 'Cache\|Usage\|Team'` | **ok**（20.7s） |
| `go vet ./internal/cli/ ./internal/team/ ./internal/cachelab/` | **通过** |
| `bash scripts/cache-guard.sh` | **10/10 case pass** |
| `go run ./tools/repolint` | RED SET 与 HEAD **逐字节相同**（`diff` 为空） |
| Agent C 文件上的 repolint 违规 | **0** |

**HEAD 已有的红项（非本轮引入，carry-forward）**：`chat_tui_team_render/reset/session.go`、`team_history_sync.go`、`team_replay.go`、`team_task_service.go`、`messages_usage.go` 的 essay/file-size 超预算。

**另外发现并修复的 HEAD 偶发红项**：`TestRecorderRecordsExactRequestAndRawUsage` 的 journal 竞争（§2.3），在纯 HEAD 上 `-count=30` 可复现。

### 6.1 在途红项的归属（不属于 Agent C）

在**实时工作树**上跑 `./internal/cli/` 时观测到一次失败：

```text
--- FAIL: TestMemberBackendInheritsTheCacheShapingKnobs (0.06s)
    team_backend_options_inherit_test.go:75:
      member HeadroomGoal = 20480, want 80000: agent.visible_window_tokens did not reach the member's agent
```

**归属判定（C 已核验）**：

| 证据 | 结果 |
|---|---|
| `git cat-file -e HEAD:internal/cli/team_backend_options_inherit_test.go` | **不存在于 HEAD**（未跟踪的在途文件） |
| 文件 mtime | **20:36:35**，晚于 C 最后一次全绿运行（20:23） |
| `internal/agent/context_headroom.go` mtime | **20:33:56**（未跟踪，在途） |
| `internal/agent/context_status.go` | **已修改**（在途，`HeadroomGoal` 定义在此） |
| 把 C 的两个 cli 文件还原到 HEAD 后重跑 | **仍然失败**（`-count=6` 六次全红） |

**结论**：该失败属于 **Agent A 的在途工作**（`HeadroomGoal` / `visible_window_tokens` 的继承链路），**与 Agent C 的改动无关**。C 未修改 `team_backend_options*.go`、`context_status.go` 或 `context_headroom.go`，**也不为其负责**。

**C 的处理**：不在实时工作树上继续跑 `./internal/cli/` 的全量门禁——在途编辑会让结果不可归属。C 的所有门禁在**隔离快照**上执行（§6 上表），该快照只包含 Agent C 的文件。

**给协调 Agent 的信号**：`TestMemberBackendInheritsTheCacheShapingKnobs` 在 20:36 时**稳定红**（非偶发），需要 Agent A 在提交前处理。

**在途改动说明**：另两个 Agent 的工作树改动（`internal/agent/**`、`internal/cli/team_backend_options_inherit_test.go` 等）在本快照中被排除；**Agent C 不为其负责，也不对其验证**。

---

## 7. 限制清单（必须随结论一起引用）

| # | 限制 | 影响 |
|---|---|---|
| L-1 | `append_block_allowance = 128` 是**单网关归纳值**，不是文档化常量。 | 允许量的正确性未跨账号/跨网关验证 |
| L-2 | `session_ordinal` 是**publisher 进程内**的到达序计数：成员后端重建会重置它，队列丢一条可能丢掉会话的开场记录。 | 轮转判定是「观测到的轮转」，不是「durable 轮转账本」 |
| L-3 | `b_only` 条件的 `Switches` 与 baseline 相同——形状稳定性在本仓库**不是配置开关**。 | 该条件的复现配方依赖具体构建，配置无法完整表达 |
| L-4 | `RuntimeEpoch` 在 CLI/Team 路径恒为空、`AttemptID` 在 `Usage` 事件从未设置。 | 不能用它们做会话或 attempt 判别 |
| L-5 | 轮转的 lineage / `ParentSessionID` **不经过 `event.Sink`**。 | 成员观测只能从 `SessionID` 变化推断轮转，不能读到 lineage |
| L-6 | `rewrite_requests` 是**宣告计数**，且 fold 的 reason 落在**下一轮**请求上。 | 不能当作维护操作数，也不能精确归到发起轮 |
| L-7 | 任务质量与延迟**不在本数据集**。 | 计划 §6.2 的质量/延迟门槛本轮无法验收 |
| L-8 | 报表的 `provider_residual_unexplained = 0` 只对**本次两次受控会话**成立。 | 不得外推为「生产上不存在无法解释的 miss」 |
| L-9 | `hit == 0` 仍统一表述为「未报告 cache read」；本地 `prefix_hash` 仍不是 provider cache key。 | 不得据此推断缓存生命周期或 provider key |

---

## 8. 正式 go/no-go（计划 §3 交付物 4）

### 8.1 判定

| 问题 | 判定 | 依据 |
|---|---|---|
| 夹具修正是否必须？ | **必须，且已完成** | N-1 让「有 cache read」被读成「无 split」（§2.1） |
| A 的统计契约是否可用？ | **通过** | 五条件、排除分类、对账等式逐字未动；C 只新增字段（§4） |
| 归因机制是否可用？ | **通过** | 分区在两次真实会话上闭合，residual 为 0（§5.1–5.2） |
| 能否据此放行任何缓存行为优化？ | **不能** | 样本量不足（桶级 `insufficient_sample`）、无压缩观测、无质量/延迟指标（§5.3） |
| 条件矩阵是否可执行？ | **可执行，但未执行** | 5 个条件臂已登记且带门禁；**本轮没有跑条件对照**（§4） |
| **最终** | **有限通过 / 继续采样** | 观测与夹具可合流；**行为优化不放行** |

### 8.2 必须重跑（N-1 的后果）

在**新夹具**下重跑，并逐请求断言 §2.1 的判定式：

1. `B0-pilot`（可行性与 `usage_split` 可用性）；
2. `B1-baseline-repeat`（基线）；
3. 任何曾报出 `no_cache_split` 的臂——**这些样本必须逐条确认是「真的没有 cache read」还是「词表混合被误读」**。

### 8.3 灰度的前提条件（**尚未满足，不得开始**）

计划 §5 P4 的灰度**现在不能启动**。启动前必须依次满足：

1. 条件矩阵跑完 `baseline` 与 `a_only` / `b_only` 的**正式样本**（每臂 ≥30 有效 warm、≥3 成员）；
2. `miss_tokens_per_request` 的方向**跨至少三次独立运行一致**（计划 §6.2）；
3. 任务质量与延迟的替代证据到位——本数据集测不到（L-7），必须由别的边界提供；
4. 轮转（`rotations`）在真实负载下被观测到至少一次，确认 `cold_start_miss_tokens` 的归因在 rescue 路径上成立（当前 `rotations=0`，该路径**未被验证**）。

### 8.4 开关与回滚

本轮**没有引入任何行为开关**——改动全部是**观测字段、归因分类、夹具解析**，不改变 provider-visible 字节、缓存策略、裁剪策略或成员隔离。因此：

| 改动 | 回滚方式 | 影响面 |
|---|---|---|
| `cachemisscause.go` / `cachereport_turn.go`（新文件） | 删除文件 + 移除 `BuildCacheReport` 的两处调用 | 报表少两个 JSON 段 |
| `cacherequest.go` 三个新字段 | 删除字段（`omitempty`，旧 reader 忽略） | 无 |
| `team_usage_publish.go` 会话身份 | 删除 `observeSession` 调用 | 轮转判定失效，退化为原行为 |
| `cachelab/recorder.go` 解析与发布顺序 | 还原 `resolve()` 与 `finishSample` 尾部 | 夹具退化为旧行为 |
| `cachelab/plan.go` 条件矩阵 | 删除 `Conditions` 字段与 `ConditionArms()` | 条件臂不可用 |

**版本切换公告**：报表 JSON 新增 `miss_causes` / `turn_cost` / `maintenance_cost` 三段，**旧 reader 忽略新键即可**；`cachelab` 的 `UsageProblem` 取值集合新增了更具体的拒因（`no_cache_read` 现在也会出现在原先报 `unresolved` 的样本上），**下游若按 `unresolved` 做分支需同步**。

### 8.5 复查触发点

出现下列任一情况，本判定立即失效并重新复核：

- `provider_residual_unexplained` 在正式样本里**非零**且占比 > 5%；
- `rotations > 0` 且 `cold_start_miss_tokens` 的归因与 `first_session` 部分出现重叠或对不上；
- 任一已声明支持的 provider 形状出现 `UsageProblem` 变化（词表漂移）；
- `CacheReportUnobservable()` 中的任一条被新的观测能力解除——此时必须**同时更新清单与本节判定**。

---

## 9. 复现清单

### 9.1 离线（零成本）

```bash
go build ./...
go test ./internal/team/ ./internal/cachelab/ ./internal/stats/ ./internal/provider/... -count=1
go test ./internal/cli/ -run 'Cache|Usage|Team' -count=1
go test ./internal/cachelab/ -run TestUsageParser -v -count=1     # N-1/N-2 的判定式
go test ./internal/cachelab/ -run TestCondition -v -count=1       # N-7 条件矩阵
go test ./internal/cachelab/ -run TestRecorderRecordsExactRequestAndRawUsage -count=30   # N-3
go test ./internal/team/ -run TestCacheMissCause -v -count=1      # 归因分区 + N-9 账本行披露
go test ./internal/team/ -run TestCacheTurnCost -v -count=1       # 每 turn 成本
go vet ./internal/cli/ ./internal/team/ ./internal/cachelab/
go run ./tools/repolint
bash scripts/cache-guard.sh
```

### 9.2 真实 Provider（需凭证，`-tags live`）

```bash
go test -tags live ./internal/cli/ -run 'TestLiveTeamMemberCacheSession$' -v -count=1 -timeout 20m
go test -tags live ./internal/cli/ -run 'TestLiveTeamMemberCacheBaselineTeam$' -v -count=1 -timeout 25m
```

两次运行的输出自带：`miss causes` 分区行、`per logical turn` 行、`maintenance cost` 行、
以及 `what this dataset cannot measure` 清单（§3.4）。

### 9.3 条件矩阵（**本轮未执行**）

```bash
# 每个条件需要一个真正配置了对应开关的构建；臂被 Gated，未配置即拒绝。
go test -tags live ./internal/cli/ -run TestLiveProviderCacheExperiment -v -count=1 \
  REASONIX_LIVE_CACHE_ARM=C-a_only ...
```

臂 ID：`C-baseline`、`C-a_only`、`C-b_only`、`C-a_plus_b`、`C-rescue_enabled`。
**`C-rescue_enabled` 不是优化臂**：它轮转会话，冷前缀是其成本的一部分（§2.4）。

---

## 10. 残余不确定性

1. **N-1 的历史影响面未量化**：无法知道过去多少样本是「真无 split」vs「词表混合被误读」——旧 journal 已按旧解析器落盘，**不可回溯**。唯一出路是用新夹具重跑。
2. **128 的粒度模型未跨账号/跨网关验证**（L-1）。若它不成立，`append_only_expected` 与 `provider_residual_unexplained` 的边界需要重划。
3. **Rescue 路径完全未被验证**：两次会话 `rotations=0`，轮转判定（§3.1）**只有单元测试覆盖，没有真实证据**。
4. **fold 边界未被观测**：`rewrite_requests=0`，`compact_auto` 归因在真实会话上**未触发**。
5. **`b_only` 条件的复现配方不完整**（L-3）。
6. **09-23 / 09-24 的历史跌幅**：本轮**没有触碰**。`provider_residual_unexplained = 0` 是受控会话的结果，**与历史账本的 ~19pp 缺口无关**，不得互相引用。

---

## 11. 合流轮（2026-09-25）

A 与 B 均已交付（`TEAM_MEMBER_CACHE_PART_A_STATE_MACHINE.zh-CN.md`、`TEAM_MEMBER_CACHE_PART_B_PREFIX_STABILITY.zh-CN.md`）。
本节记录 C 在合流轮**接手的跨边界工作**、**合流后的实测**，以及**仍然不放行的理由**。

### 11.1 接手 B 移交的三项（B 文档 §7 移交表）

B 明确移交、不代为修改的三项，C 全部落地：

| B 的移交项 | C 的落地 | 测试 |
|---|---|---|
| `applyCacheDiagnostics` 的 5 行映射 | `internal/cli/team_usage_publish.go`：`MessagePrefixHash` / `MessageCount` / `MessagesComparable` / `FirstDivergenceOffset` / `MessagesRewritten` | `TestObservedRequestMapsEveryUsageField` |
| `cacherequest.go` 加带 `omitempty` 的字段 | 5 个字段，注释写明「不可比时零值表示**未测量**，不是**没改写**」 | 同上 |
| `messages_rewritten > 0 且无 rewrite reason` 单独成类 | **`CacheCauseMessagesRewritten = "messages_rewritten_unclaimed"`**，在 `prefixMoveCause` 里**优先于**其他 rewrite 值 | `TestCacheMissCauseNamesAnUnclaimedMessageRewrite` |

**为什么这个类必须排在 residual 之前**：residual 的定义是「本地没有任何东西能解释」，而一次无人认领的消息改写**恰恰是本地有东西可解释**——数组被改了。让它落进 residual 会把一个客户端原因记成 Provider 行为，与计划 §3 职责 3 的四问直接冲突。

**可达性（C 在这一轮自己踩到并修正的一个坑，如实记录）**：B 的 `CompareShape` 在「数组被改写且无人认领」时会**主动写入** `PrefixChangeReasons = ["messages"]`——也就是说，生产记录**永远不会**是「`MessagesRewritten > 0` 且 reasons 为空」这个形状。C 的第一版把新类挂在「reasons 为空且 `MessagesRewritten > 0`」上，**在真实数据上不可达**；单元测试之所以通过，是因为它喂的是**生产者从不发出的合成形状**（断言了合成边界而不是被消费的边界）。

修正后的判据在 `prefixMoveCause` 里把 `cachereason.Messages`（=「无人认领」）与其它 rewrite 值（=「某个操作认领了」）**分开**：

```
unclaimed（reasons 含 "messages"）→ messages_rewritten_unclaimed
rewrite（compact_auto / prune / truncate / rewind_* / guardian_merge）→ rewrite
```

`cachereason.Messages` 是全词表里**唯一不指向任何操作**的 rewrite 值——它存在的意义就是「没人认领」。把它和 fold 混为一类，等于把 F4 要找的东西藏进一个读者有权略过的类别里。

**测试已改为断言生产形状**：`PrefixChangeReasons = ["messages"]` + `MessagesRewritten > 0`；并加了两条边界——同一请求同时带 `session_context` 与 `messages` 时仍归 `messages_rewritten_unclaimed`（结构性变更讲的是框架，不解释被改写的历史），以及 `compact_auto` 在场时仍归 `rewrite`。反向验证过：把这条 split 去掉，该测试**立即变红**。

B 的第四项移交（`internal/eventwire/wire.go` 暴露给 desktop/ACP）**C 不做**：它属于协调 Agent 的范围（B 已如此登记），且 C 无法验证 desktop 侧消费。

### 11.2 接手 A 的维护成本（A 文档 §5.2 暴露面）

A 的 `MaintenanceCost` 挂在 `ContextMaintenanceSnapshot` 上，而**成员观测点能读到它**——`memberUsagePublisher` 持有 `control.SessionAPI`，其中就有 `ContextMaintenanceSnapshot()`。C 因此把它接到落盘面：

```
agent.MaintenanceCost
  → memberUsagePublisher.sampleMemberUsage
  → team.OwnerUsage.Maintenance (OwnerUsageMaintenance)
  → team.CacheSessionTotals.Maintenance + MaintenancePublished
  → team.CacheSessionReport.Maintenance + MaintenanceMembers
  → 报表 "maintenance spend: ... (published by N of M members)"
```

**三个设计决定**：

1. **放在会话累计而不是逐请求记录**：A 的计数器是**会话级累计**，与 C 的 `turn_cost`（逐 turn）回答不同的问题。硬塞进 `.cache_requests.jsonl` 会让同一条会话累计在每个请求上重复一遍。
2. **`MaintenancePublished` 布尔 + `MaintenanceMembers` 计数**：`omitempty` 的零值无法区分「这一项是 0」与「这个成员没发布过」。前者是「没花」，后者是「不知道」——报表因此打印 `published by 3 of 3 members`，部分合计不会被读成全会话合计。
3. **不改 `.usage.json` 的既有键**：`maintenance` 是纯新增段，老 reader 忽略即可（`TestLegacyUsageDocumentStillReads` 仍绿）。

### 11.3 合流后的实测（真实 Provider）

**12 条 / 3 成员会话**（`TestLiveTeamMemberCacheSession`）：

```text
coverage over 12 scoped samples: request_count measured=12 defaulted=0 unrecorded=0 unrecognized=0
  route_bucket=12/12 model_ref=12/12 usage_source=12/12 prefix_diagnostics=12/12 (present/scoped)
  session_identity=12/12 (present/scoped); a sample without one cannot have its cold status decided
  message_shape_comparable=12/12 (scoped); a sample without it cannot be told apart from a rewrite
miss causes over 12 scoped samples (partition; append allowance 128 tok):
  cold_prefix            requests=3   eligible=3   miss=193958  (100% of scoped miss)
  append_only_expected   requests=9   eligible=9   miss=420     (0% of scoped miss)
per logical turn (turns=12 requests=12 requests_without_turn=0)
  prompt/turn=66726 hit/turn=50528 miss/turn=16198 completion/turn=34 requests/turn=1
maintenance cost: rewrite=0 structural=0 rotations=0
  cold_start_miss_tokens=193958 (first session 193958, rotations 0)
```

**30 条 / 3 成员会话**（`TestLiveTeamMemberCacheBaselineTeam`）：

```text
coverage over 30 scoped samples: request_count measured=30 defaulted=0 unrecorded=0 unrecognized=0
  route_bucket=30/30 model_ref=30/30 usage_source=30/30 prefix_diagnostics=30/30 (present/scoped)
  session_identity=30/30 (present/scoped); a sample without one cannot have its cold status decided
  message_shape_comparable=30/30 (scoped); a sample without it cannot be told apart from a rewrite
miss causes over 30 scoped samples (partition; append allowance 128 tok):
  cold_prefix            requests=3   eligible=3   miss=187909  (99% of scoped miss)
  append_only_expected   requests=27  eligible=27  miss=2299    (1% of scoped miss)
per logical turn (turns=30 requests=30 requests_without_turn=0)
  prompt/turn=65685 hit/turn=59345 miss/turn=6340 completion/turn=20 requests/turn=1
maintenance cost: rewrite=0 structural=0 rotations=0
  cold_start_miss_tokens=187909 (first session 187909, rotations 0)

session cumulative (a different metric from every rate above)
  members=3 tokens: hit=1648000 miss=192850 token_weighted=89.5% member_simple_mean=90.1%
  last turn across members: token_weighted=99.9%
  maintenance spend: summary_requests=0 projection_installs=0 rescues=0 repeat_blocks=0 (published by 3 of 3 members)
```

**这三行合起来说明什么**：

1. **三方数据面已打通**：B 的消息数组指纹（`message_shape_comparable=12/12`）、C 的会话身份（`session_identity=12/12`）、A 的维护计数器（`published by 3 of 3 members`）**在同一次真实会话里同时在场**。
2. **`messages_rewritten_unclaimed = 0`**：这两次会话里没有出现「改写已读字节且无人认领」的请求。这是**否定性证据**——F4 的入口被接通了，但**这两次会话没有触发它**。
3. **`summary_requests=0`**：两次会话都没触发压缩，所以 A 的重复压缩抑制**在这批数据上没有被观测到**。这与 §5.3 的「fold 边界未被观测」是同一个缺口。

### 11.4 仍然不放行的理由（合流不改变它）

合流打通的是**观测面**，不是**证据面**。计划 §6.2 的通过门槛**一项都没有新增满足**：

| 门槛 | 状态 | 缺口 |
|---|---|---|
| 计量正确性 | ✅ | A/B/C 的表驱动测试已就位 |
| Provider 隔离 | ⚠️ | 仍只有一个网关、一个账号 |
| Team 可追溯性 | ✅ | 覆盖率三项 12/12 |
| 实验可比性 | ❌ | **条件矩阵（`C-*` 臂）从未执行** |
| 样本充足性 | ❌ | 桶级全部 `insufficient_sample` |
| 历史结论边界 | ✅ | 未触碰历史账本 |
| **优化准入** | ❌ | **无真实 Team 层内复现、无质量/延迟护栏证据** |

**结论不变：有限通过 / 继续采样。行为优化不放行。**

### 11.5 合流后新增的限制

| # | 限制 | 影响 |
|---|---|---|
| L-10 | A 的 `MaintenanceCost` 是**会话累计**，C 的 `turn_cost` 是**逐 turn**；两者不可相除得出「每 turn 压缩次数」。 | 计划 §6.2 的 `compaction_requests_per_turn` 需要逐 turn 归属，当前只能给出「会话总计 / 会话 turn 数」的粗粒度 |
| L-11 | `MaintenancePublished=false` 的成员**不计入** `MaintenanceMembers`，也不进合计。 | 一个老 `.usage.json` 会让该成员的维护成本显示为「未发布」而非 0；这是有意的，但报表只给计数，不给成员名单 |
| L-12 | `messages_rewritten_unclaimed` 在两次真实会话上均为 **0**，该类的**可达性只有单元测试覆盖**。 | 与 rescue 轮转同样：入口已通，路径未在真实负载上触发过 |
| L-13 | `unexplained_prefix_change` 的「reasons 为空」分支**从本仓库的 agent 生产者到不了**：`CompareShape` 在任何前缀移动时都会至少写入一个 reason（`system` / `tools` / `session_context` / 认领值 / `messages`）。该分支只对**外来记录**（手写行、别的 producer）有意义。 | 该类的真实计数在生产数据上应当恒为 0；若某天非 0，说明有第三方写入了诊断字段 |
