# Part B（后续）：Team 正式分层实验 —— 预注册（采样前冻结）

> 状态：**预注册条件于 2026-09-25 冻结；采样随后已执行，执行后审计发现 route/account 漂移，详见文末附记。** 本附记不追溯修改冻结条件。
> 依据：`TEAM_MEMBER_CACHE_FOLLOWUP_EXECUTION_PLAN.zh-CN.md` §5（Agent B）、§7（开始门禁）、§3（共用 M0）。
> 前置：`TEAM_MEMBER_CACHE_MERGE_RECORD.zh-CN.md`、`TEAM_MEMBER_CACHE_PART_B_PILOT_REPORT.zh-CN.md`。
> **本文在读取任何本轮结果之前冻结。** 冻结后不得因中途命中率有利或不利而修改样本量、停止规则或排除规则。

## 0. M0 冻结记录（§3 要求）

| 项 | 值 |
|---|---|
| 基线 commit | `71d2e3695fddd093c2a06f8a8045486db73b78ff`（`Team-agent-merge-mainv2`，提交信息 `fix:修复缓存计量`） |
| 工作树 | **干净**（仅 `?? internal/provider/openai/zz_probe_a_test.go`，属 **Agent A** 的临时探针，非本工作包） |
| 折叠规则 | `internal/provider/anthropic/stream_usage.go` md5 **`71b7d3b79a4c6dfc74d1357b310190ff`** —— 与合流纪要 M0 冻结值**一致** |
| 夹具解析 | `internal/cachelab/usage.go` md5 **`01a5cb37e30cf272a095fce1a09837f1`** —— **与归档快照 `cc0cda4fcbd22bd7f68602d376656865` 不同**。差异已由本人独立比对：**仅一处注释块改写**（`Oracle*` 字段说明），无代码差异。该文件的摘要裁定属 **Agent C** 的写集（§6.1），本工作包不裁决，只记录。 |
| 实际测试版本 | 本轮的 B 侧测试与采样**不使用** `internal/cachelab`（B 只读成员记录与统计账本），因此该摘要差异**不影响**本工作包的任何结论 |
| 网关 / 账号 | `aiapi.lejurobot.com`，池条目复用现有 `deepseek-v4-flash-roojin` 端点与凭据（**不新增凭据，不写入报告或归档**） |
| 线上 model | `deepseek/deepseek-v4.1-flash`（wire 拼写；池内拼写带 `[1m]`，由 `team.ResolveAgentUserModel` 剥离） |
| route bucket | `anthropic/bfcb0811b1c8`（pilot 实测值，本轮须复核是否一致） |
| 统计契约 | 沿用 A v1 契约：主指标 `Σhit/(Σhit+Σmiss)`，仅"有效单请求"；`miss_tokens_per_request` 必须并列；unknown 不填 0；**journal / 成员记录 / 历史账本三者永不相加** |
| 隐私边界 | 不落 prompt、工具参数/结果正文、完整响应、密钥或认证头 |

**开始门禁（§7）状态**：

| 门禁项 | 状态 |
|---|---|
| 共享工作树变更归属已确认 | ✅ 见上表 |
| 三方写集已确认 | ✅ A `internal/provider/**usage*`；B `internal/team/**` 观测字段 + pilot 驱动/归档 + B 报告；C `internal/cachelab/**` + 复算/审计 |
| 端点/采样额度 | ⚠️ **待负责人确认**。本轮为**付费真实请求**；费用上限见 §3 |
| 真实采样错开运行 | ⚠️ **待负责人协调**。B 的采样窗口内**不得**有 A 或 C 的真实端点请求并发，否则并发条件不可控 |

## 1. 主要问题与唯一主要变量

**主要问题**：在冻结的 K1 构建与固定 route/model/account 下，真实 Team member 的 `miss_tokens_per_request` 是否随**上下文大小**、**会话阶段**、**压缩/重建边界**或**并发档**变化？

**唯一主要变量（按顺序，每次只增一个）**：

| 阶段 | 唯一主要变量 | 其余固定 |
|---|---|---|
| S0 | 无（串行基线复现，确认构建/route 未漂移） | route/model/account/任务族/成员数/轮数 |
| S1 | **上下文大小**（四个臂，见 §2） | 同上 |
| S2 | **会话阶段**（首轮 / 连续 warm / 压缩前后 —— 压缩阶段可达性见 §4.3） | 同上 |
| S3 | **并发档**（1 → 2 → 4，臂登记） | 同上 |

**臂执行顺序**：S1a → S1b → S1c →（用 S1c 实测 chars/token 校准后）S4-768k → S2 → S3。
S1c 必须在 S4 之前，因为 S4 的 brief 带宽窄（§2），需要用实测比值定大小。S2/S3 依赖 S1 的落桶结果。

**明确不做**：不切换 model、route、account 或缓存配置；不为凑满上下文桶而以裸请求代替真实 Team；不为触发 fold 而临时改任务族或压缩配置。

## 2. 上下文桶与臂登记

**任务族（冻结）**：`long-context-single-word-recall` —— 与 pilot 同一族，由驱动方登记，**不进记录**（代码里不存在该概念）。

| 臂 | 目标桶 | 首轮 brief | 成员数 | 每成员轮数 | 预估首轮 prompt | 预估估算 token |
|---|---|---|---|---|---|---|
| **S1a-small** | `lt_32k` | 40 KB | 3 | 12 | ~8.7K | ~10.2K |
| **S1b-mid** | `128k_256k` | 900 KB | 3 | 12 | ~195K | ~230K |
| **S1c-large** | `512k_768k` | 2,800 KB | 3 | 12 | ~606K | ~717K |
| **S4-768k**（专项） | `768k_1m` | 3,750 KB | 3 | 8 | ~812K | ~960K |

**brief 大小的标定依据**：pilot 实测 560KB → 121,128 tok（**4.73 chars/token**）。上表用该比值外推，并同时给出估算器（`fallbackTokPerChar = 0.25`，即 4 chars/token）会算出的值——**桶的落点由 provider 报告决定，但 fold 的触发由估算值决定**，两者必须分别检查：

```
桶区间            = provider 报告的 context_prompt_tokens
fold 触发条件     = 估算的可见 token > 0.80 × 1_000_000 = 800_000
hardInputCeiling  = 1_000_000 − 256 = 999_744   （估算值必须低于它，否则请求被拦）
```

**S4-768k 的 brief 带宽很窄**：要同时满足「报告落进 `[786_432, 1_048_576)`」与「估算 < 999_744」，brief 必须落在 **≈3,633–3,905 KB**。超出上界会被 `hardInputCeiling` 拦住；低于下界会落进 `512k_768k`。**S4 因此是最脆弱的一臂**，须先跑 S1c 校准实测 chars/token 再定 S4 的最终大小。

**落错桶如实记录，不重跑凑数。**

**为什么选这三个桶**：pilot 只覆盖了 `lt_32k` 与 `32k_128k`，且**这两个桶的 miss/请求已几乎相同（99.7 / 96.7）**——要检验"miss/请求与尺寸无关"，必须在**跨一个数量级**的桶上取数。`512k_768k` 是 1M 窗口成员在**不触发 fold** 的前提下能达到的最大桶（S1c 的估算 717K < 800K 触发线）。

## 3. 样本量、费用上限与停止规则

### 3.1 样本量（预注册）

- 主要跨成员桶门槛：**≥30 个有效单请求 且 ≥3 个成员**（沿用方案 §5）。
- 每臂：3 成员 × 12 轮 = 36 条记录，其中首轮 3 条、warm 33 条 → **warm 33 ≥ 30，达标**。
- **独立 session/batch 数另行报告**：同一 session 的连续请求**不得**当作完全独立重复。本设计每个成员**一个 session**，因此跨成员桶的"独立单元"数是 **3**，不是 33。报告必须同时给出这两个数。
- 首轮单独成组（`first_request`），不并入 warm。

### 3.2 费用上限

- 单臂上限 **5.00 USD**，全部四臂合计上限 **20.00 USD**。
- **价格来源**：`REASONIX_LIVE_CACHE_PRICE_HIT_PER_MTOK` / `_MISS_PER_MTOK` / `_OUT_PER_MTOK`。
- **⚠️ 当前状态：本机未配置这三个变量**（`env` 无匹配），因此按方案 §5「价格未提供时成本报告为 unknown，不编造」，**成本将报告为 unknown**。
- **因此费用控制改为按 token 预算执行**（可在无价格时确定性执行）：

| 臂 | 预估输入 token/成员 | 成员 | 合计输入 token | 说明 |
|---|---:|---:|---:|---|
| S1a-small | ~10K × 12 轮 ≈ 0.12M | 3 | ~0.37M | |
| S1b-mid | ~230K × 12 轮 ≈ 2.76M | 3 | ~8.3M | |
| S1c-large | ~717K × 12 轮 ≈ 8.6M | 3 | ~25.8M | **最大单项** |
| S4-768k | ~960K × 8 轮 ≈ 7.7M | 3 | ~23M | 见 §4.3 的带宽约束 |

- **硬上限：S0–S1 合计输入 ≤ 40M token；含 S4 的全部臂合计 ≤ 80M token。** 超过即停止，不论进度。
- **要求**：采样前由负责人确认可用额度；若额度不足以覆盖 S1c 或 S4，该臂降级为**不可执行**并如实记录，**不得**用更小的 brief 冒充目标桶。
- **成本口径**：因价格未配置，报告只能给 **token 量**，不能给 USD。**不得**用任何外部价格表事后折算并当作本轮实测成本。

### 3.3 停止规则（仅限已登记原因）

任一成立即**停止该臂**、隔离样本、先审数据：

1. 累计输入 token 达到 §3.2 上限；
2. 连续 3 次服务错误；
3. 连续 3 次 usage 不可用；
4. `request_count_source != observed`（计数 provenance 不足）；
5. 构建、route 或 account 漂移（route bucket 与 §0 不符）；
6. 隐私违规（任何正文/凭据进入归档）；
7. 预注册时长上限：**单臂 60 分钟**。

**中途命中率有利或不利都不构成停止理由。**

### 3.4 排除规则

沿用 A v1 契约 §3.2/§3.3，**不新增、不放宽**：

| 分类 | 处理 |
|---|---|
| `request_count_source != observed` 或 `count != 1` | 排除并单列 token |
| `usage_unknown` / `usage_estimated` | 排除 |
| `accounting_invalid` | 排除 |
| `hit + miss <= 0`（no cache split） | 排除，**不读作 0 命中** |
| `observed_at` 不可解析 | 排除 |
| 首轮 | **单列**为 `first_request`，不并入 warm |
| `hit == 0` | 表述为"**未报告 cache read**"，**不称冷启动** |

**低命中样本不因结果不利而被排除或重分类。**

## 4. 并发档与长上下文/压缩边界

### 4.1 并发档（§5「并发字段处理」）

**结论：本轮使用臂登记表达并发，不实现逐请求字段。** 依据：

1. **已独立审计**（本工作包 P0，非引用他方结论）：`internal/team/agentruntime/runtime.go` 的 `live`/`byMember` 是 `sync.Mutex` 保护的普通 map，**没有原子计数器**；`Runtime` 的导出方法里**没有** `LiveCount`/`ActiveCount`。
2. 唯一可用的运行时读数是 `teamTaskService.busyMembers()`，它走 `board.LoadLiveTasks(ctx)`——**一次 store 读**。把它挂到每次 usage 事件上会让 provider 请求等待遥测，违反 `memberUsagePublisher.observe` 的既有契约。
3. `internal/agent/scheduler.go` 的 `activeTotal` 是**子代理**调度器计数（`maxTotal` 默认 6），与**成员并发**不是同一个量；它也不在 `control.SessionAPI` 上。

**因此**：并发档由臂登记冻结，分层以**臂**为单位而非以**行**为单位。每臂的 banner 写明该臂的并发定义（同时运行的成员数、单成员 in-flight 数）。

**若负责人后续要求逐请求信号**，需先提供：竞态分析、观测路径开销测量、以及"provider 请求非阻塞"的证明。本工作包**不**在没有该证明时实现。

### 4.2 长上下文边界（`gte_1m` 的可达性判定）

**结构性判定：在 1M 窗口的成员上 `gte_1m` 桶不可达。** 推导：

```
桶下界                     = 1_048_576  (CacheBucketGTE1M)
成员 hardInputCeiling      = 1_000_000 − 256 = 999_744   (window − protocolReserveTokens)
=> 1_048_576 > 999_744  =>  不可达
```

`768k_1m` 桶 `[786_432, 1_048_576)` 与可达区间 `[·, 999_744]` 的**交集为 `[786_432, 999_744]`，宽 213,312 token** —— 可达。

**判定**：`gte_1m` 记为**不可执行**（结构性，非额度或时间原因），**不得**用裸请求或合成长 prompt 替代。`768k_1m` 由 S4 专项采集。

### 4.3 压缩/重建边界（**可达性判定：未确认，见下**）

**推导与它的缺口**：

```
fold 触发       = 0.80 × 1_000_000 = 800_000  （估算的可见 token）
S4-768k 首轮    ≈ 960_000 估算 token  > 800_000  => 估算上越过触发线
fold 后可见     ≈ summary(≤8_192) + 保留尾(≤160_000) ≈ 168_000  => 一次冷轮
```

**但估算越过触发线不等于 fold 会发生。** 折叠还受 `planCompaction`（`internal/agent/compact.go:316`）的边界约束：它把「固定前缀 + 会话上下文快照」钉住（`pinnedPrefixLen`、`latestSessionContextIndex`），只对**可压缩的历史区**做摘要；`minCompactMessages = 2` 还要求可压缩区至少 2 条消息。一个**单条巨大用户消息**是否留下足够的可折叠历史，**本预注册不做断言**。

**因此**：

- S2 的"压缩前 / 压缩后"阶段**标记为「可达性未确认」**，不写成"会天然产生"。
- 判据是运行时的**观测量**，不是推导：若某成员的记录里出现 `prefix_change_reasons` 含 `compact_auto`，则该臂的压缩边界**已触发**，其后的首个请求即"压缩后"样本；否则记为**未触发**，如实标注，**不构造替代**。
- 若 S1c/S4 均未触发 fold，则 S2 的压缩阶段判定为**不可执行**（在当前任务族与窗口下），并记录该结论——**不得**为了触发 fold 而临时改变任务族、窗口或压缩配置（那会同时改动两个变量）。

**压缩边界是本轮最可能落空的子目标**，预先声明，避免事后把它读成"没观察到差异"。

## 5. 必报指标（§5）

每个 stratum 至少报告：

1. 有效请求数、成员数、**独立 session/batch 数**；
2. `hit_tokens` / `miss_tokens` / `hit_tokens_per_request` / `miss_tokens_per_request`；
3. prompt 均值与分位数；
4. **三个率并列且注明不可互换**：token 加权率、成员等权率、session 汇总率；
5. route/model/account、上下文桶、任务族、维护阶段、并发臂、请求形状摘要；
6. error / retry / unknown / estimated / no-split / aggregate / invalid-accounting 的**排除计数**；
7. `usage_source`、oracle coverage（成员侧**永久 `insufficient_provenance`**，见 M1 冻结）、request-count provenance、请求形状诊断覆盖率。

**质量护栏**：本轮**只观测、不改变行为**，因此任务完成率/必要上下文保留/工具正确率**标记为未测试**，不得声称已通过。若日后提出行为变更，须**另行预注册**这些护栏。

## 6. 交付物与归档

1. **本文**（预注册，采样前冻结）。
2. `TEAM_MEMBER_CACHE_FOLLOWUP_B_REPORT.zh-CN.md`（结果）。
3. 脱敏归档至 `~/reasonix-partb-archive/<date>/`：banner（含构建标识与臂登记）、逐请求记录、报告 JSON、复算命令。**不含正文或凭据。**

**归档前置检查**（沿用 pilot 的三条硬门禁，缺失即拒绝运行）：

1. `REASONIX_LIVE_CACHE_ARCHIVE` 必须显式给出，且**不得**位于 OS 临时目录；
2. `REASONIX_LIVE_CACHE_CLIENT_BUILD` 必须给出（测试二进制不带 VCS 戳）；
3. 不得依赖 home 推导默认路径（cli 测试二进制把 `HOME` 重定向到一次性目录）。

## 7. 已知限制（随结论一起引用）

| # | 限制 |
|---|---|
| L-1 | **单网关、单账号池条目、单 route、单 model。** 不得外推为生产 Team 成员平均命中率。 |
| L-2 | **任务族是合成的**（单字召回），不是真实工作负载。它测的是"稳定前缀 + 逐轮小增量"，**不代表**代码读改、长文档分析或工具密集任务。 |
| L-3 | **成员数 = 3。** 跨成员推断的独立单元是 3 个 session，即使请求数达标也**不能**做真正的跨成员总体推断。 |
| L-4 | **`gte_1m` 结构性不可达**（§4.2）。 |
| L-5 | **成本报告为 unknown**（未配置价格），费用控制按 token 预算执行（§3.2）。 |
| L-6 | **并发档是登记值，不是测量值**（§4.1）。 |
| L-7 | **oracle coverage 在成员侧不可得**（M1 冻结：私有扩展不提升为观测契约）。 |
| L-8 | **`128` 块粒度仍是归纳值**，未跨账号验证。 |
| L-9 | 历史账本与本轮采样**不可比**（合流纪要 §3.1：窗口不重叠 + 无 provenance）。本轮只建立**新的前瞻基线**。 |

## 8. 本文未做（冻结时点）

- 未修改任何生产代码、观测字段或统计口径。
- 冻结时未发起任何真实请求；采样由后续执行记录承载。
- 未提交、未推送、未开 PR。

## 9. 执行后审计附记（不修改预注册条件）

六个 strata 臂执行后对归档记录与驱动代码进行核验：预注册冻结的账号池条目为 `deepseek-v4-flash-roojin`、route bucket 为 `anthropic/bfcb0811b1c8`；实际六臂均使用 `strata-gw`，204/204 记录的 route bucket 为 `anthropic/f119dfdb4214`，`model_ref` 为 `strata-gw/deepseek/deepseek-v4.1-flash[1m]`。因此正式预注册条件未满足。原冻结条件保持不变；六臂结果降级为探索性基线，正式实验须在冻结条件上重跑，或另行冻结新的预注册版本后再执行。
