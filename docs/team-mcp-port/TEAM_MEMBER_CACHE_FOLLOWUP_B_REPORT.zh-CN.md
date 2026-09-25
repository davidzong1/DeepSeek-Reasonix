# Part B（后续）：Team 分层实验报告（S1/S3/S4）

> 状态：**六个臂已执行（2026-09-25）**。执行依据：`TEAM_MEMBER_CACHE_FOLLOWUP_EXECUTION_PLAN.zh-CN.md` §5、预注册 `TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION.zh-CN.md`。**兜底复核发现预注册的账号池条目与 route 条件发生漂移，正式预注册门禁未满足；本文数字保留为可复现的探索性基线，不作为正式因果对照。**
> 结论边界：**单网关、单账号池条目、单 route、单 model、合成任务族、3 成员。** 不得外推为生产 Team 成员平均命中率。

## 0. 结论摘要

| # | 结论 | 强度 | 依据 |
|---|---|---|---|
| **B-0** | **预注册条件漂移：预注册为池条目 `deepseek-v4-flash-roojin`、route `anthropic/bfcb0811b1c8`；六个 strata 臂实际均为池条目/AgentUserRef `strata-gw`、route `anthropic/f119dfdb4214`，model_ref 为 `strata-gw/deepseek/deepseek-v4.1-flash[1m]`。** | **门禁阻断** | §1、预注册 §0 |
| **B-1** | **`miss/请求` 在 11.7K → 792K prompt（×68）上基本不变：87.0 / 90.5 / 87.6 / 78.1。** 命中率从 99.27% 升到 99.99%，**升幅全部来自 prompt 变大**。 | **证据**（n=120 warm） | §2 |
| **B-2** | **块对齐不变量在全部 6 个臂、186 个 warm 样本上成立**：`hit % 128 == 0` 且 `miss ≡ prompt (mod 128)`，**186/186**（其中上下文四臂 120/120）。 | **证据** | §2.3 |
| **B-3** | **并发 1 → 2 → 3 无差异**：同一 brief、同一 roster、同一轮数，warm 率 99.27% / 99.27% / 99.28%，miss/请求 87.0 / 87.0 / 85.0。 | **证据** | §3 |
| **B-4** | **`768k_1m` 桶已由真实 Team 成员采到**（ctx 792,340，21 个 warm 样本）。 | **证据** | §2.1 |
| **B-5** | **`gte_1m` 结构性不可达**：桶下界 1,048,576 > 成员 `hardInputCeiling` 999,744。**记为不可执行**，未用合成长 prompt 替代。 | **结构性判定** | §4.1 |
| **B-6** | **压缩/fold 边界未触发，且判定为在本任务族下不可达**。四个大臂的 `prefix_change_reasons` 全空、ctx 无回落。原因是 fold 触发用**估算**值，而估算在首个真实 usage 后按实测比值重标定，实测比值（~0.207 tok/char）低于回退值（0.25），因此估算始终低于 800K 触发线。 | **证据（观测）+ 机制推断** | §4.2 |
| **B-7** | **首请求全 miss**：六个臂的首请求 `hit=0`，未报告任何 cache read。 | **证据** | §2.2 |
| **B-8** | **样本量**：`lt_32k` / `128k_256k` / `512k_768k` / `768k_1m` 四桶各达 **33 / 33 / 33 / 21** 个 warm 样本、**3 成员**，除 `768k_1m` 外均达到 ≥30/≥3 门槛；但**独立 session 数只有 3**（每成员一个 session），跨成员推断仍不成立。 | **限制** | §2.1 |
| **B-9** | 全部 6 臂 **排除项为 0**（unknown / estimated / aggregate / invalid-accounting / no-split / unparsable / unverified-count 全 0），计数来源 **observed 100%**。 | **证据** | §1 |

**一句话**：在 K1 后、真实成员路径、68 倍 prompt 范围、三个并发档上，**未命中工作量恒定在 ~78–90 tokens/请求**，命中率的全部变化由组成效应解释；`768k_1m` 已采到，`gte_1m` 与 fold 边界在本配置下**结构性不可达**。由于 route/account 偏离预注册条件，这些观察只构成探索性基线。

## 1. 运行登记与样本账目

| 项 | 值 |
|---|---|
| build | `71d2e3695fdd`（`REASONIX_LIVE_CACHE_CLIENT_BUILD` 显式登记，写入每臂 banner） |
| 折叠规则 | `internal/provider/anthropic/stream_usage.go` 的摘要由本臂 build `71d2e3695fdd` 的 blob 复核（与 M0 冻结值 md5 相同）。**⚠️ banner 内的 `frozen_stream_usage_sha256` 字段本身为空**（见 §1.1） |
| 预注册网关 / 账号 | `aiapi.lejurobot.com`，池条目 `deepseek-v4-flash-roojin` |
| 预注册 route bucket | `anthropic/bfcb0811b1c8` |
| 实际网关 / 账号 | `aiapi.lejurobot.com`，池条目/AgentUserRef `strata-gw`（代码同时登记 `Provider=deepseek`） |
| 实际 route bucket | `anthropic/f119dfdb4214`（204/204 条记录一致） |
| 实际 model_ref | `strata-gw/deepseek/deepseek-v4.1-flash[1m]`（204/204 一致） |
| 任务族 | `long-context-single-word-recall`（**驱动方冻结登记**，不进记录） |
| 并发 | 每臂登记值（1 / 1 / 1 / 1 / 2 / 3），**登记而非逐请求实测**（§3.1） |
| 窗口 | 2026-09-25（六个臂串行，错开运行） |
| 归档 | `~/reasonix-partb-archive/2026-09-24/strata-<arm>-{banner,records,report}-<runid>.*` |

**样本账目（逐臂）**：

| 臂 | 记录 | 计入基线 | 排除（逐类） | 计数来源 | 诊断在场 | 路由在场 |
|---|---:|---:|---|---:|---:|---:|
| S1a-small | 36 | **36** | 全 0 | observed 36/36 | 36/36 | 36/36 |
| S1b-mid | 36 | **36** | 全 0 | observed 36/36 | 36/36 | 36/36 |
| S1c-large | 36 | **36** | 全 0 | observed 36/36 | 36/36 | 36/36 |
| S4-768k | 24 | **24** | 全 0 | observed 24/24 | 24/24 | 24/24 |
| S3-c2 | 36 | **36** | 全 0 | observed 36/36 | 36/36 | 36/36 |
| S3-c3 | 36 | **36** | 全 0 | observed 36/36 | 36/36 | 36/36 |

### 1.1 banner 的测量层摘要字段为空（补记）

六个臂的 banner 都写 `frozen_stream_usage_sha256: unreadable:stream_usage.go`——驱动用**相对路径**读该文件，而 `go test` 的 CWD 是包目录，故读取失败并留下占位。**上表 §1 原写「sha256 写入每臂 banner」与归档不符**，已按此更正。

**测量层仍可追溯，但不是通过该字段**：banner 记录的 build `71d2e3695fdd` 的 `stream_usage.go` blob 与 M0 冻结值 **md5 相同**（`71b7d3b79a4c6dfc74d1357b310190ff`），因此本批读数所依据的折叠规则可确认未变。**V2 的驱动已改为从仓库根解析，且读不到即拒绝运行**（V2 §7 L-13 / §6）。

**token 预算（预注册 §3.2）**：六臂合计输入 **48.6M**，在注册上限 80M 之内；S0–S1 四臂 28.7M，在 40M 之内。**每臂的 banner 记录实际消耗与上限**，超出即由 `assertStrataPipeline` 判失败。

## 2. 上下文分层（S1 + S4）

### 2.1 结果表

| 臂 | 目标桶 | **实际落桶** | 首轮 ctx | warm n | 成员 | 独立 session | warm 加权率 | **miss/请求** | 成员等权率 |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| S1a-small | `lt_32k` | ✅ `lt_32k`（36/36） | 11,725 | 33 | 3 | 3 | **99.27%** | **87.0** | 99.27% |
| S1b-mid | `128k_256k` | ✅ `128k_256k`（36/36） | 192,674 | 33 | 3 | 3 | **99.95%** | **90.5** | 99.95% |
| S1c-large | `512k_768k` | ✅ `512k_768k`（36/36） | 592,450 | 33 | 3 | 3 | **99.99%** | **87.6** | 99.99% |
| S4-768k | `768k_1m` | ✅ `768k_1m`（24/24） | 792,340 | 21 | 3 | 3 | **99.99%** | **78.1** | 99.99% |

**四个上下文臂全部命中其各自的目标上下文桶**，无落错桶、无重跑；但 route/account 与预注册值不一致，因此不能称为预注册 route 上的正式结果。

### 2.2 组成效应（必须与命中率同读）

```
prompt/请求  11,725  →  192,674  →  592,450  →  792,340     （×67.6）
命中率        99.27%  →   99.95%  →   99.99%  →   99.99%     （+0.72pp）
miss/请求       87.0  →     90.5  →     87.6  →     78.1     （几乎不变，略降）
```

**命中率的全部升幅由 prompt 变大造成**，未命中工作量在 68 倍 prompt 范围内基本恒定。这是 A v1 契约 §3.1「任何候选改动只能按 `miss_tokens_per_request` 判定」在**跨一个数量级以上的真实成员样本**上的又一次实测。

**首请求**：六个臂的首请求 `hit=0`、`miss=prompt`，即**未报告任何 cache read**。按契约措辞记为「未报告 cache read」，**不称冷启动**（无 session 生命周期证据）。

### 2.3 块对齐不变量

**186/186 个 warm 样本**（六个臂全部）同时满足：

1. `hit % 128 == 0`；
2. `miss ≡ prompt (mod 128)`。

（§2.1 的上下文四臂是其中 **120/120**。）

跨 11.7K–792K prompt、跨 3 个并发档、跨 4 个上下文桶。这是该不变量在**真实成员路径**上的第三次独立复现（pilot 两次 + 本轮），且首次覆盖到 `768k_1m`。

**仍未跨账号、未跨 route、未在 >1M 上下文验证** —— 这三条未决保持。

## 3. 并发分层（S3）

### 3.1 并发信号的处理（§5「并发字段处理」）

**结论：本轮以臂登记表达并发，未实现逐请求字段。** 依据是本工作包**独立审计**的结果，不是引用他方结论：

1. `internal/team/agentruntime/runtime.go` 的 `live` / `byMember` 是 `sync.Mutex` 保护的普通 map，**没有原子计数器**；`Runtime` 的导出方法中没有 `LiveCount` / `ActiveCount`。
2. 唯一可用的运行时读数是 `teamTaskService.busyMembers()`，它走 `board.LoadLiveTasks(ctx)`——**一次 store 读**。挂到每次 usage 事件上会让 provider 请求等待遥测，违反 `memberUsagePublisher.observe` 的既有契约。
3. `internal/agent/scheduler.go` 的 `activeTotal` 是**子代理**调度器计数（`maxTotal` 默认 6），与**成员并发**不是同一个量；它也不在 `control.SessionAPI` 上。

因此并发档由**臂**承载，分层以臂为单位。**驱动实现**：`strataStrataRun` 在 `Concurrency == 1` 时严格串行；更高值时把 roster 按 worker 划分，恰好 N 个成员同时在飞。**每个成员内部仍然串行**（一个 backend、一次一个 turn），所以该臂只改变"多少成员同时跑"这一个变量。

### 3.2 结果表

| 臂 | 登记并发 | 首轮 ctx | warm n | warm 加权率 | miss/请求 | 不变量 | 挂钟 |
|---|---:|---:|---:|---:|---:|---|---:|
| S1a-small | **1** | 11,725 | 33 | 99.27% | 87.0 | 33/33 ✓ | 67.6s |
| S3-c2 | **2** | 11,723 | 33 | 99.27% | 87.0 | 33/33 ✓ | 50.0s |
| S3-c3 | **3** | 11,723 | 33 | 99.28% | 85.0 | 33/33 ✓ | 23.0s |

**并发 1 → 2 → 3：命中率与 miss/请求均无差异**（最大差 0.01pp / 2.0 token，与块余量落点的量级相同）。

**并发确实发生了**（独立核验，非假设）：**计算规则**——把该臂全部记录按 `observed_at` 排序，取相邻记录的时间差，统计小于单请求时延（1s）的个数：

| 臂 | 整臂相邻差 min | <1s 个数 | 挂钟 |
|---|---:|---:|---:|
| S1a-small（串行） | 1.44s | **0** / 35 | 67.6s |
| S3-c2 | 0.06s | **13** / 35 | 50.0s |
| S3-c3 | 0.00s | **30** / 35 | 23.0s |

挂钟时间从 67.6s 降到 50.0s 再降到 23.0s，与 1/2/3 的并发档单调一致。**并发使多个成员的请求在时间轴上重叠**，故相邻差出现远小于单请求时延的值；串行臂一个都没有。

> **引用更正（2026-09-25，依据 C 的 GC-7 并经本人独立复算）**：本段原引「S1a 1.47–2.08s / S3-c2 0.07–0.28s / S3-c3 0.09–0.93s」。**这些不是任何已陈述规则下的极值**（本臂实测极值为 1.44–2.84 / 0.06–2.61 / 0.00–1.37），读起来像人工挑出的低间隔簇。**现象本身成立且已被独立复现**（上表即复算结果），但原引用值不可复现，故以本表替换。**结论方向不变。**

**局限**：并发档是**登记值**，逐请求记录里**没有**并发字段（M1 冻结：该字段被有理由地拒落）。因此本结论只能以**臂**为单位陈述，**不能**逐请求归因。

## 4. 长上下文与压缩边界

### 4.1 `gte_1m`：结构性不可达

```
桶下界                = 1_048_576  (CacheBucketGTE1M)
成员 hardInputCeiling = 1_000_000 − 256 = 999_744   (window − protocolReserveTokens)
=> 1_048_576 > 999_744  =>  不可达
```

**判定：记为不可执行（结构性原因），未用裸请求或合成长 prompt 替代。** 本配置下唯一可达的最大桶是 `768k_1m` 的 `[786_432, 999_744]` 区间（宽 213,312 token），已由 S4 采到。

### 4.2 压缩/fold 边界：未触发，且判定为不可达

**观测**：S1c-large（ctx 592K）与 S4-768k（ctx 792K）的 **`prefix_change_reasons` 全部为空**，`stable_prefix_changed` 未置位，ctx 逐轮**单调无回落**。**没有任何一个臂触发 fold。**

> **措辞更正（2026-09-25，依据 C 的 GC-6 并经本人独立复算）**：本段原写「ctx 逐轮单调 **+23** 无回落」。独立复算 18 个成员的逐轮增量：**15 个为 +23、3 个为 +24**（`S3-c2-m3`、`S4-768k-m1`、`S4-768k-m2`）。**「每个成员一个固定值、无一轮减少」成立**（没有任何成员出现混值），但把 +23 当作全体定值不成立。**单调性与「未触发 fold」的结论不变。**

**机制**（代码路径核验）：

```
fold 触发条件  est > 0.80 × 1_000_000 = 800_000
est 的来源     a.estimatedVisibleRequestTokens(visible)   (context_manager.go:114)
               → estimatedShapeTokens → calibratedPromptTokens，失败才用 fallbackTokPerChar=0.25
重标定         setPromptTokenCalibrationFromUsage 在首个真实 usage 后写入实测比值
本 route 实测  S1c: 2,867,200 chars → 592,450 tok = 0.2066 tok/char
```

**首轮**用回退值 0.25 估算 → S4 的 3,840,000 chars 估算 **960,000 > 800,000**，看似越过触发线；但 fold 仍**未触发**。**次轮起**估算改用实测比值 0.2066 → 3,840,000 × 0.2066 ≈ **793,000 < 800,000**，**确定性地低于触发线**——这条成立且已由 V2 §5.2 复核。

> **机制归因更正（2026-09-25，依据 C 的 GC-5 并经本人逐行复核）**：本段原把首轮不折叠归因于「`foldContext` 还受 `planCompaction` 的 pinned-prefix / `minCompactMessages` 约束」。**不成立**——`planFoldRegion`（`compact_projection.go:679`）在 `min=2` 失败后**显式回退**到 `planCompaction(msgs, 1, force)`（`:682`），该约束因此被绕过。真正的绑定约束是随后的**活动轮钳制**（`:684-691`）：首轮除 pinned system 外只有活动轮自己，`start` 被钳回 `head`，`start > head` 为假 → 无可折叠区。**结论方向（fold 不可达）不变，引用错了约束。**

**判定**：在本任务族（单个巨大首轮 + 逐轮固定增量，实测 15 个成员 +23、3 个 +24）与 1M 窗口下，**fold 边界不可达**。这**不是**"未观察到差异"，而是**没有样本**。

**不构造替代**：未为提高估算而改变任务族、窗口或压缩配置——那会同时改动两个变量。压缩阶段记为**不可执行**。

## 5. 必报指标（预注册 §5 逐项）

> **正式性说明**：以下指标均来自真实、可审计的 204 条记录，但因 §1 的 route/account 漂移，正式预注册实验门禁为**未满足**；这些数据只能作为探索性基线，不能支持同 route 的正式因果比较。

| 指标 | 值 |
|---|---|
| 有效请求 / 成员 / 独立 session | 204 / 3 / **3** |
| `hit_tokens` / `miss_tokens`（全部 warm） | 见各臂归档 report JSON |
| `hit_tokens_per_request` / `miss_tokens_per_request` | §2.1、§3.2 |
| prompt 均值与分位数 | 各臂 report JSON 的 `mean_prompt_tokens` 与 `p10/p50/p90` |
| **三个率并列** | token 加权率、成员等权率、session 汇总率——见各臂 report JSON；**三者不可互换**（本任务族下成员同尺寸，三者在数值上接近，但口径不同） |
| route / model / account / 桶 / 任务族 / 维护阶段 / 并发臂 | §1、§2.1、§3.2、§4.2 |
| 排除计数 | §1，**全 0** |
| `usage_source` | `executor`（全部） |
| oracle coverage | **成员侧永久 `insufficient_provenance`**（M1 冻结：私有扩展不提升为观测契约） |
| request-count provenance | `observed` 100% |
| 请求形状诊断覆盖率 | 204/204（`diagnostics_available=true`） |

**质量护栏**：本轮**只观测、未改变任何行为**，因此任务完成率 / 必要上下文保留 / 工具正确率**标记为未测试**，不声称已通过。

## 6. 未决与不能声称

- **不能**把 99.99% 称为"生产 Team 成员平均命中率"：单 route、单账号、单 model、合成任务族、3 成员。
- **不能**做跨成员总体推断：**独立单元是 3 个 session**，不是 204 个请求。同一 session 的连续请求不是独立重复。
- **不能**对 fold/compaction 边界下结论：**没有样本**（§4.2）。
- **不能**对 `gte_1m` 下结论：**结构性不可达**（§4.1）。
- **不能**对并发做逐请求归因：并发档是登记值，记录里没有该字段（§3.1）。
- **不能**说"命中率提高"：本轮是**新的前瞻性基线**，不是与 K1 前基线的对照。历史账本（84.5%/67.7%）与本轮**不可比**（合流纪要 §3.1：窗口不重叠 + 无 provenance）。
- **未解释的 ~9.6pp 历史残差仍是未决**：本报告没有触及它。
- **成本仍为 unknown**：价格未配置，报告只给 token 量（48.6M），不给 USD。
- **预注册 route/account 漂移仍未解决**：需在正确池条目 `deepseek-v4-flash-roojin` 与 route `anthropic/bfcb0811b1c8` 上重新执行，或由负责人正式批准新的预注册版本；在此之前不得把本报告标作正式门禁已满足。

## 7. 与 A / C 的对账

| 对象 | 关系 |
|---|---|
| A 的矩阵 §3.1（本网关 warm 可由算术独立判定） | ✅ 全部 warm 样本满足 `hit > input_tokens` 形状，读数无需 oracle |
| A 的矩阵 §4（split 后 split-free 重申的折叠缺口） | ✅ 204 条记录**未观察到**该形状 |
| C 的 §2（128 块对齐归纳） | ✅ 在 68× prompt 范围、3 并发档、4 个桶上独立复现（186/186） |
| C 的 §4.2（`miss/请求` 必须与命中率并列） | ✅ 本报告按该要求组织 |
| C 的 §5.2 / §5.3（并发与 attempt 字段拒落） | ✅ 本工作包**独立复核后同意**，并以臂登记落实（§3.1） |
| A v1 契约 §1.1（三个数据集永不相加） | ✅ 本报告只用成员记录 |

## 8. 复现

```bash
export REASONIX_LIVE_CACHE_ARCHIVE="$HOME/reasonix-partb-archive"
export REASONIX_LIVE_CACHE_CLIENT_BUILD="$(git rev-parse --short=12 HEAD)"
export REASONIX_LIVE_CACHE_MODEL="deepseek/deepseek-v4.1-flash"

for arm in S1a-small S1b-mid S1c-large S4-768k S3-c2 S3-c3; do
  REASONIX_LIVE_CACHE_STRATA_ARM=$arm \
    go test -tags live ./internal/cli/ -run TestLiveTeamMemberCacheStrata -v -count=1 -timeout 60m
done
```

未注册的臂名被**拒绝**并列出已注册项（不近似执行）。

## 9. 本文未做（明确边界）

- 未修改任何生产代码、观测字段、统计口径或请求构造。
- 未改分母、未排除不利样本、未把 unknown 归零。
- 未提交、未推送、未开 PR。
