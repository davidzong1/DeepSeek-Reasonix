# Part B：K1 后真实 Team member pilot 报告（P2）

> 状态：**pilot 已执行（2026-09-24）**。执行依据：`TEAM_MEMBER_CACHE_POST_JOINT_3AGENT_PLAN.zh-CN.md` §3 Agent B、§5 阶段 P2、§6.1 必报项。
> 前置：`TEAM_MEMBER_CACHE_PART_B_P0_INVENTORY.zh-CN.md`（P0 盘点与 M0 冻结）。
> 结论边界：**单网关、单账号池条目、单 route、单 model、串行单请求、3 成员 × 10 轮**。不得外推为生产 Team 成员平均命中率。

## 0. 结论摘要

| # | 结论 | 强度 | 依据 |
|---|---|---|---|
| **B-1** | K1 后真实 Team member 的 warm 命中率是 **99.79%–99.93%**（token 加权），三个成员分别 99.16% / 99.79% / 99.93%。 | **证据**（单 route/账号，n=27 warm） | §3.2 |
| **B-2** | **miss/请求 在 11.8K → 121.2K prompt 上基本不变（99.7 / 96.7 / 83.9）**，而命中率从 99.16% 升到 99.93%。这是**组成效应**：命中率的全部升幅来自 prompt 变大，未命中工作量没有下降。 | **证据** | §3.3 |
| **B-3** | **块对齐不变量在真实 Team member 请求上成立**：27/27 的 warm 样本满足 `hit % 128 == 0` 且 `miss ≡ prompt (mod 128)`；k 分布 22 个 k=0、5 个 k=1。C 的 §2 归纳因此在**成员路径**（不只是裸请求）上得到独立复现。 | **证据** | §3.4 |
| **B-4** | 每个成员的 prompt 逐轮增量是**一个固定值**（`pilot-mid` / `pilot-large` 四个运行始终 +23；`pilot-small` 前两次 +23、后两次 +24），**9 轮无一轮减少**——**客户端 append-only 在真实成员路径上成立**。 | **证据** | §3.5 |
| **B-5** | 首请求（cold）三个成员都是**全 miss**（hit 仅 2816/2944，为网关固定块余量）。 | **证据** | §3.2 |
| **B-6** | 样本**不足以做跨成员推断**：所有桶都 `insufficient_sample`（需要 ≥30 请求且 ≥3 成员每桶，pilot 是 3 成员 × 10 轮）。 | **限制** | §3.1 |
| **B-7** | **缺失字段按 coverage 缺口披露**：`session_id` 在本次采集中已由 C 落地（`pilot` 全部记录带值）；`attempt`、`task_family`、`concurrency_level` 仍无字段，前者不填、后两者由驱动方登记。 | **限制** | §4 |

**一句话**：在 K1 后、真实成员路径上，本 route 的 warm 命中率是 99.8% 量级，miss/请求 84–100 tokens，且这 84–100 全部可由 128 块对齐余量解释。**本轮没有观察到任何"长上下文命中率退化"**，也没有观察到任何随尺寸增长的未命中工作量。

## 1. 运行登记（预注册与冻结）

| 项 | 值 |
|---|---|
| build | `7bda0c3d32d2`（`REASONIX_LIVE_CACHE_CLIENT_BUILD` 显式登记，写入 banner） |
| 折叠规则冻结 | `internal/provider/anthropic/stream_usage.go` md5 `71b7d3b79a4c6dfc74d1357b310190ff` |
| 夹具/解析冻结 | `internal/cachelab/usage.go` md5 `cc0cda4fcbd22bd7f68602d376656865` |
| 网关 / 账号 | `aiapi.lejurobot.com`，池条目 `pilot-gw`（`Provider=deepseek`，`Model=deepseek/deepseek-v4.1-flash[1m]`） |
| route bucket | `anthropic/bfcb0811b1c8`（30/30 记录一致） |
| model_ref | `deepseek/deepseek-v4.1-flash`（wire 拼写，`[1m]` 已由 `team.ResolveAgentUserModel` 剥离） |
| 成员 | `pilot-small`（40KB brief）、`pilot-mid`（200KB）、`pilot-large`（560KB） |
| 轮次 | 每成员 10 轮，串行（**concurrency_level = 1，登记值，非实测**） |
| 任务族 | `long-context-single-word-recall`（**驱动方冻结登记**；代码里不存在该概念，不可从数据推断） |
| 窗口 | 2026-09-24T12:57:35Z – 12:58:54Z（78s 挂钟，**本报告主表的来源运行**） |
| 归档 | `~/reasonix-partb-archive/2026-09-24/pilot-{banner,records,report}-1790254655106980265.*` |

> **来源运行标注（2026-09-25 更正，依据 C 的独立复算 §4.1）**：本报告的三张结果表**分别来自不同的运行**，原文未标注，现逐表补上 run id。
> 四个归档运行的 id 后缀分别为 `…492890560284` / `…655106980265` / `…916540630588` / `final`（= `…916540630588`）。
>
> | 表 | 来源运行 |
> |---|---|
> | §3.1 分层表（含 `first_request` 8,448 / 169,789 与 `warm_candidate` 1,604,736 / 2,502） | `…492890560284` |
> | §3.2 主表 | `…655106980265`（**= 上表 §1 的归档运行**） |
> | §3.2 独立复跑列 | `…916540630588`（= `final`） |
> | 报告 JSON 的 `overall.totals`（1,613,440 / 171,990） | `…916540630588`（= `final`） |
>
> 四个运行之间 warm 加权率的差异在 **0.1pp 内**，miss/请求的移动是**块边界落点差异**（C §2.3 预言），不是缓存行为差异。**这不是数据缺陷，是原文的可追溯性缺陷**，已按上表修正。

**归档前置检查**（三条都在运行前生效，否则测试拒绝执行）：

1. `REASONIX_LIVE_CACHE_ARCHIVE` 必须显式给出，且**不得**位于 OS 临时目录（方案 §9「不得把 `/tmp` 作为唯一副本」）。
2. `REASONIX_LIVE_CACHE_CLIENT_BUILD` 必须给出——测试二进制自身不带 VCS 戳，banner 写 `unknown` 的归档无法回答"哪个构建产出的"。
3. cli 测试二进制会把 `HOME` 重定向到一次性目录（`internal/testenv/home.go`），因此**不能**用 home 推导默认归档路径——那会让证据随进程退出被删除。

## 2. 样本账目

| 项 | 值 |
|---|---:|
| 记录总数 | 30 |
| 计入基线 | **30** |
| 排除：unknown / estimated / aggregate / accounting_invalid / no_split / unparsable_ts / non_member | **0 / 0 / 0 / 0 / 0 / 0 / 0** |
| 排除：计数未验证 | **0** |
| 计数来源 | `observed` 30/30 |
| 诊断在场 | 30/30（`diagnostics_available=true`） |
| 路由在场 | 30/30 |
| `usage_source` | `executor`（全部） |
| 冷/热划分 | `first_request` 3 条、`warm_candidate` 27 条 |

**口径一致性**：本报告的 30 条记录**全部来自成员 writer 自己的 `.cache_requests.jsonl`**（`source` = 成员记录）。裸请求（B 的 provider 边界实验）与历史账本（`stats-ledger`）**均未并入**，三者不可相加、不可互为基线（A v1 契约 §1.1）。

## 3. 结果

### 3.1 分层（全部 `insufficient_sample`）

| 桶 | 请求 | 成员 | hit | miss | 加权率 | 成员等权 | p10/p50/p90 | 门槛 |
|---|---:|---:|---:|---:|---:|---:|---|---|
| `lt_32k` | 10 | 1 | 108,544 | 9,711 | 91.8% | 91.8% | 24.0% / 99.1% / 99.7% | `insufficient_sample` |
| `32k_128k` | 20 | 2 | 1,504,640 | 162,580 | 90.2% | 90.3% | 6.2% / 99.9% / 99.9% | `insufficient_sample` |
| `first_request` | 3 | 3 | 8,448 | 169,789 | 4.7% | 10.9% | 2.3% / 6.2% / 24.0% | `insufficient_sample` |
| `warm_candidate` | 27 | 3 | 1,604,736 | 2,502 | **99.8%** | 99.7% | 99.0% / 99.8% / 99.9% | `insufficient_sample` |
| `lt_1m`（间隔桶） | 27 | 3 | 1,604,736 | 2,502 | 99.8% | 99.7% | 99.0% / 99.8% / 99.9% | `insufficient_sample` |

**桶级数字全部含首请求**，所以看起来低（`lt_32k` 91.8% 是 9 条 99% + 1 条 24% 的结果）。**任何跨成员推断都不成立**：3 成员 × 10 轮达不到 30/3 门槛。

### 3.2 逐成员（warm 部分）

| 成员 | warm n | 首请求 hit/miss | warm hit | warm miss | warm 加权率 | **miss/请求** | 首轮 ctx |
|---|---:|---|---:|---:|---:|---:|---:|
| `pilot-small` | 9 | 2,944 / 8,774 | 105,600 | 897 | **99.16%** | **99.7** | 11,718 |
| `pilot-mid` | 9 | 2,816 / 42,563 | 408,576 | 870 | **99.79%** | **96.7** | 45,379 |
| `pilot-large` | 9 | 2,816 / 118,312 | **1,090,432** | 755 | **99.93%** | **83.9** | 121,128 |

> **转写更正（2026-09-25）**：`pilot-large` 的 warm hit 原印 **1,093,432**，独立复算四个归档运行**全部**为 **1,090,432**（差 3,000，一位数字）。率同为 99.93%（三位小数内不可分辨）。已按记录值更正。

**首请求全 miss**：三个成员的首次请求 hit 只有 2,816 / 2,944，即网关固定块余量，其余全部计为 miss。这与 B 的 provider 边界实验（裸请求 cold 全 miss）方向一致，但**这是成员路径的独立观测**。

**独立复跑（同条件、同 route、同成员、同任务，第二次运行）**：

| 成员 | warm 加权率（run 1 / run 2） | miss/请求（run 1 / run 2） | 不变量 |
|---|---|---|---|
| `pilot-small` | 99.16% / **99.23%** | 99.7 / **91.4** | 9/9 ✓ |
| `pilot-mid` | 99.79% / **99.79%** | 96.7 / **97.7** | 9/9 ✓ |
| `pilot-large` | 99.93% / **99.93%** | 83.9 / **84.9** | 9/9 ✓ |
| 合计不变量 | — | — | **27/27 ✓（两次运行均成立）** |

**两次独立运行的差异恰好是块边界落点（k∈{0,1}）的差异，不是缓存行为的差异**——这正是 C §2.3 预言的形状：同一夹具的 miss 在 232/12/3 之间移动。因此**逐成员的 miss/请求在两轮之间可以相差 ~9 token 而命中量不变**，这是**预期的**，不构成不稳定性。

### 3.3 组成效应（结论 B-2，必须与命中率同读）

```
prompt/请求 11,833  →  45,494  →  121,243     （×10.2）
命中率      99.16%  →  99.79%  →   99.93%     （+0.77pp）
miss/请求     99.7  →    96.7  →     83.9     （几乎不变，甚至略降）
```

**命中率的上升完全由 prompt 变大造成**：未命中工作量在 10 倍 prompt 范围内几乎不变。这正是 A v1 契约 §3.1 要求「任何候选改动只能按 `miss_tokens_per_request` 判定、不得按命中率百分点判定」的实测依据——**且这一次的 miss/请求不是 B 的裸请求夹具，而是真实成员请求**。

### 3.4 块对齐不变量（结论 B-3）

27/27 个 warm 样本同时满足：

1. `hit % 128 == 0`；
2. `miss % 128 == prompt % 128`。

即 `miss = prompt % 128 + k × 128`，k 分布为 **k=0: 22 个、k=1: 5 个**。

**意义**：C 的 §2 是从 **11 份裸请求 journal** 归纳出这两条不变量的。本 pilot 在**真实 Team member 请求**（经生产 builder、生产工具面、生产 system prompt）上**独立复现**了同一组不变量，且 k 的取值只在 0/1 之间。这加强了「miss 是块切分余量、不是每轮固定成本」这一判定，**并使其不再只是单一路径的归纳**。

### 3.5 逐轮增长（结论 B-4）

每个成员的 prompt 逐轮增量是**一个固定值**，9 轮**无一轮减少**：

```
pilot-small  +24 ×9 轮      ← 原印 +23，已更正
pilot-mid    +23 ×9 轮
pilot-large  +23 ×9 轮
```

> **措辞更正（2026-09-25）**：原文写"三个成员**全部恰好 +23**"。独立复算显示 `pilot-small` 在前两次运行是 +23、在**后两次运行（含 final）是 +24**，另两个成员四个运行**始终 +23**。因此正确的表述是"**每个成员一个固定值**"，而不是"三者同一值"。
> **结论方向不变**：三个成员的 delta 都是**单一固定值**，**无一轮减少**，append-only 成立。+23/+24 的差别是每轮追加文本的 token 数在两次运行间相差 1（同一句提示词的不同 token 化落点），不影响 append-only 的判定。

+23/+24 tokens ≈ 一次 assistant 回复 + 一次 user 追加（"Turn N: reply with exactly the word ACKN and nothing else."）。**append-only 在真实成员路径上成立**，P1（客户端前缀重写）在本形状下再次被证伪。

### 3.6 miss 的周期性（旁证，不作为结论）

三个成员的 miss 序列各自呈 +23 的等差递增，并在第 5 轮回落到低位（如 `pilot-large`: 63, 86, 109, 132, **27**, 50, 73, 96, 119）。这与「prompt 每轮 +23、块边界每 5–6 轮跨过一次」完全相容——**是块对齐的推论，不是独立发现**，仅作为 §3.4 的旁证列出。

## 4. 字段覆盖率与缺口（结论 B-7）

| 契约字段 | 本次采集 | 说明 |
|---|---|---|
| `team_id` / `member_id` / `turn_id` / `request_seq` | ✅ 30/30 | |
| `session_id` | ✅ **30/30** | Agent C 在本轮落地（`cacherequest.go:84`）；本次 pilot 的归档记录全部带值 |
| `request_count` / `request_count_source` | ✅ 30/30 `observed` | 计数被观测，可进 per-request 基线 |
| `context_prompt_tokens` / `prompt` / `hit` / `miss` / `cache_write` | ✅ 30/30 | |
| `route_bucket` / `model_ref` | ✅ 30/30 | 单 route、单 model |
| `maintenance_phase` | ⚠️ 可派生 | 本次 30 条 `prefix_change_reasons` 全空、`stage` 全为 `warm_candidate`/`first_request`，**未观察到 fold/compaction 边界**（10 轮太短） |
| `attempt` | ❌ 无字段 | 本次 `request_count` 全为 1，无重试，因此该缺口未影响本次结果；正式实验若出现重试则必须补 |
| `task_family` | ❌ 无字段 | 由驱动方冻结登记（`long-context-single-word-recall`），**不写入记录** |
| `concurrency_level` | ❌ 无字段 | 本次串行，登记值 1；正式并发阶梯实验前必须由 C 落字段（见 `TEAM_MEMBER_CACHE_PART_B_TO_C_INTERFACE_REQUEST.zh-CN.md`） |
| `request_shape_digest` | ⚠️ 近似 | 记录带 `prefix_hash` / `stable_prefix_hash`；**不是 provider cache key**，只能作客户端差分 |

**敏感内容检查**：归档的 records / report / banner 三类文件对 `sk-`、`api_key`、`bearer`、`authorization`、brief 正文、任务标记词的全部匹配数**为 0**。

**对操作者账本的影响**：本次运行**未写入** `~/.reasonix/stats/2026-09-24.jsonl`（该文件 md5 与 M0 冻结值 `61eaaa3a…` 逐字节相同）。测试二进制把 `HOME` 重定向到一次性目录，因此 pilot 的账本行落在那里而不是操作者的统计目录。

## 5. 未决与不能声称

- **不能**把 99.8% 称为"生产 Team 成员平均命中率"：单 route、单账号、单 model、串行、3 成员、10 轮、合成任务。
- **不能**对 128 块粒度下结论：k 只在 0/1 之间，未覆盖 768K–1M、未跨账号、未在并发下验证（C §6.3 的残余不确定性原样保留）。
- **不能**对 fold/compaction 边界下结论：10 轮没有触发任何 fold，`prefix_change_reasons` 全空。
- **不能**对 1M 桶下结论：本次最大 ctx 121K，落在 `32k_128k`。
- **不能**说"命中率提高"：本 pilot 不是与某个 K1 前基线的对照实验，它是**新的前瞻性基线**。历史账本（84.5%/67.7%）与此**不可比**（无 provenance、口径降级、任务组成未知）。
- **未解释的 ~19pp 仍是未决**：本 pilot 没有触及它。它需要新构建重跑历史时段，或对历史任务组成给出可信说明。
- **`billing_usage` 在场性**：本次未逐条记录 oracle 在场性（记录字段里没有该维度）；`internal/cachelab` 侧由 C 新增了 `usage_oracle_present`。**这是成员记录侧的一个已知覆盖缺口**，正式实验前需补或明确声明不做。

## 6. 与 A / C 的对账

> **⚠️ 一条必须与本文同时引用的门槛失败**：pilot 归档完成后，`go test ./internal/provider/anthropic/` **失败**——
> `TestUsageDeepSeekDoesNotDoubleCountCacheReads`、`TestUsageDeepSeekKeepsCacheWritesBilled`、
> `TestUsageEventShapeMatrix`、`TestUsageEstimatePredecessorIsTheOnlySignalOnAnAmbiguousShape`、
> `TestUsageRemainderRuleIsInertWithoutAnOverEstimate` 五项红。这些是 **Agent A 的测试与 A 的 `anthropic.go` 改动**。
>
> **本次 pilot 的读数不受该失败影响，理由是两条独立证据**：
>
> 1. **读数本身可判**：pilot 的 30 条记录全部满足 `prompt == hit + miss` 且 warm 的 `miss > 0`、`prompt > hit`。
>    在 inclusive（subset）约定下，remainder 形状的流会给出 `miss = max(in − read, 0) = 0`、`prompt = read`——
>    与实测不符。因此 pilot 走的是 **remainder 约定**。
> 2. **A 的新默认值在该路由上给出同一约定**：A 把约定从 `c.deepseek` 改为 `inclusiveUsage := openai.IsDeepSeek(root)`，
>    而 `IsDeepSeek` 只匹配 `deepseek.com` 主机（`internal/provider/openai/host.go:36`）；
>    本路由 `root = https://aiapi.lejurobot.com` **不匹配** → `inclusiveUsage = false` → 仍是 remainder 约定。
>
> 即：**该路由的读数在 A 的这次改动前后相同**。但 **A 的测试当前不可复现通过**，A 的「已收口」结论**不能据此确认**；
> 哪一侧正确属于 Provider 语义（A 的写集），**Part B 不能裁决**。已记入归档 `M0-FREEZE.txt` 的 ADDENDUM。

| 对象 | 关系 |
|---|---|
| A 的矩阵 §3.1（本网关 warm 由算术独立判定） | ✅ 本 pilot 的 warm 样本全部满足 `hit > input_tokens` 形状，读数无需 oracle |
| A 的矩阵 §4（split 后 split-free 重申的折叠缺口） | ✅ 本次 30 条**未观察到**该形状；A 已修，属于防御性修复 |
| C 的 §2（128 块对齐归纳） | ✅ 在成员路径上独立复现（§3.4），k 分布 22×0 / 5×1 |
| C 的 §4.2（`miss/请求` 必须与命中率并列） | ✅ 本报告按该要求组织（§3.3） |
| C 的候选 (i)（稳定前缀外固定开销） | ⚠️ 本 pilot 的 miss/请求 84–100 **全部**可由块余量解释（27/27 满足不变量），**不构成**恢复候选 (i) 的证据 |
| A v1 契约 §1.1（三个数据集永不相加） | ✅ 本报告只用成员记录 |

## 7. 下一步（交负责人 / Agent C）

> **合流后更新（2026-09-25）**：C 已在 `TEAM_MEMBER_CACHE_PART_C_FINAL_REVIEW.zh-CN.md` §5 答复本节的字段请求。
> 逐项对照：**第 1 项**——`session_id` 已落地并带三条语义约束；`concurrency_level` 与 `attempt` **被拒落并给出理由**（前者会在观测路径上取 store 锁、后者会发明 provenance）；
> **第 4 项**——oracle 在场性**明确不补**（私有扩展不得提升为观测契约）。**第 2、3 项不变，仍未开始。**

1. **正式分层实验的字段前提**：`concurrency_level`（并发阶梯必需）、`attempt`（重试必需）需由 C 落位；`task_family` 由驱动方冻结登记。接口请求已单独成文。
   **合流后判定**：并发档改由**臂登记**承载（分层以臂为单位，不以行为单位）；重试关系用现有 `RequestCount > 1` + `RequestCountSource` 表达，并标 `insufficient_provenance`。
2. **样本量**：本次 30 条、3 成员、单桶最多 20 条，全部 `insufficient_sample`。正式实验需按方案 §3 Agent B 的样本规则**预注册**每桶 ≥30 有效请求、≥3 成员，并先做 pilot 估算有效率与排除比例。
3. **1M 专项**：`768k_1m` / `gte_1m` 必须来自真实 Team，不得用合成长 prompt 替代。
4. **oracle 在场性**：成员记录侧目前无该字段；若正式实验需要 oracle 一致率，需要 C 一并落位。
   **合流后判定**：**不补**。成员侧该维度永久标 `insufficient_provenance`，不填、不推断。

## 8. 复现

```bash
# 冻结校验（两个 md5 必须匹配 §1）
md5sum internal/provider/anthropic/stream_usage.go internal/cachelab/usage.go

# 归档根与构建名必须显式给出（缺失即拒绝运行）
export REASONIX_LIVE_CACHE_ARCHIVE="$HOME/reasonix-partb-archive"
export REASONIX_LIVE_CACHE_CLIENT_BUILD="$(git rev-parse --short=12 HEAD)"
export REASONIX_LIVE_CACHE_MODEL="deepseek/deepseek-v4.1-flash"
go test -tags live ./internal/cli/ -run TestLiveTeamMemberCachePilot -v -count=1 -timeout 30m
```

## 9. 本文未做（明确边界）

- 未修改任何生产代码；未修改 `internal/provider/**`、`internal/team/**`、`internal/cachelab/**`。
- 未改统计分母、未排除不利样本、未把 unknown 归零。
- 未提交、未推送、未开 PR。
