# Part B 正式分层实验 —— 预注册 V2（**待签核，尚未冻结**）

> 角色：Agent B。日期：2026-09-25。
> 依据：`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_3AGENT_PLAN.zh-CN.md` §6（B 工作包）、§8（硬防线）。
> 前置：`TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION.zh-CN.md`（V1，**保持不变，不追溯修改**）、`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_B_GATE0_RECORD.zh-CN.md`（Gate 0 排查）、`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_A_PREFLIGHT.zh-CN.md`（A 的解析链与 preflight）。
>
> **签核状态：未签核。** D-1 已由负责人裁定为 **①**（§1.1），D-2 按建议 A 实现、待逐字确认。**在负责人书面确认、C 出具 PASS、且本文 SHA-256 被记录之前，本文不构成采样依据，不得发出任何真实请求**（方案 §4 Gate 1）。提交复审的修订与摘要见 `..._GATE_B_GATE1_PACKAGE.zh-CN.md` §2。
> 本文**不修改 V1**，也**不把 V1 的旧样本改标为正式结果**（方案 §1 禁止事项）。

## 0. 为什么需要 V2（而不是"按原条件重跑"）

方案 §1 给出两条路径。Gate 0 排查（`..._B_GATE0_RECORD` §1、A 报告 §3.4）证明**路径 1 在字面上不可执行**：

```
V1 冻结了 池条目 = deepseek-v4-flash-roojin
V1 冻结了 route bucket = anthropic/bfcb0811b1c8
但 route bucket 是 池条目 id 的哈希输入之一：
  deepseek-v4-flash-roojin -> anthropic/f3634ff267d6
  pilot-gw（pilot 驱动自造） -> anthropic/bfcb0811b1c8
=> 两个冻结字段不可能同时成立
```

因此走**路径 2**：记录原条件不可用的证据（已完成），另立 V2。V2 的身份**必须由 `resolveStrataIdentity` 现算**，**不得从 V1 抄写**——A 的 1,152 次与 B 的 23,520 次穷举都是为证明抄写不可靠。

## 1. 身份块（D-1 已裁定 = ①）

| 字段 | 值 | 来源 |
|---|---|---|
| 池条目 id | `deepseek-v4-flash-roojin` | **已裁定（D-1 = ①）** |
| provider（池内声明） | `deepseek` | 注册表实测 |
| provider kind（wire adapter） | `anthropic` | `ResolveAgentUserProvider`：deepseek + `[1m]` → anthropic |
| endpoint（**存储拼写**） | `https://aiapi.lejurobot.com`（**无** `/v1`） | 注册表实测；拼写是身份的一部分（A-4） |
| **effort** | **`max`** | 注册表实测。**改变 wire body**（`output_config.effort`，`reasoning_replay.go:79`），故属于身份（见 §1.3） |
| wire model | `deepseek/deepseek-v4.1-flash` | `[1m]` 已由 `ResolveAgentUserModel` 剥离 |
| model_ref | `deepseek-v4-flash-roojin/deepseek/deepseek-v4.1-flash[1m]` | `memberModelRef`：池条目 id + 池内拼写 |
| **route bucket** | **`anthropic/f3634ff267d6`** | **由 `resolveStrataIdentity` 现算，非抄写** |
| build | `23c1e2d38fc8`（签核时以 `git rev-parse --short=12 HEAD` 复核） | 二进制 |
| 折叠规则 | `internal/provider/anthropic/stream_usage.go` md5 `71b7d3b79a4c6dfc74d1357b310190ff` | 与 M0 冻结值相同 |

> **凭据来源**：按 D-2 选项 A，条目 id 与凭据**同源**取自操作者注册表的只读副本，**无环境变量回退**。凭据值不进入本表、banner 或归档。

**机器可读身份块**（驱动**唯一**的期望来源；上表是人读视图，二者必须一致）。方案 §8.1 要求「期望身份必须来自 V2 单一冻结源，运行器不保留第二套手填默认值」——驱动解析本节第一个 `json` 代码块，并校验本文件的 SHA-256，故改表即改单，不可能只改一侧：

```json
{
  "pool_entry": "deepseek-v4-flash-roojin",
  "provider": "deepseek",
  "kind": "anthropic",
  "endpoint": "https://aiapi.lejurobot.com",
  "effort": "max",
  "wire_model": "deepseek/deepseek-v4.1-flash",
  "model_ref": "deepseek-v4-flash-roojin/deepseek/deepseek-v4.1-flash[1m]",
  "route_bucket": "anthropic/f3634ff267d6",
  "build": "23c1e2d38fc8"
}
```

| 机器字段 | 对应上表行 | 谁比较 |
|---|---|---|
| `pool_entry` / `kind` / `endpoint` / `wire_model` / `model_ref` / `route_bucket` / `build` | 同名行 | A 的 `strataPreflight`（7 字段逐项） |
| `effort` | `effort` | **仅 B 的驱动**（A 的 `strataIdentity` 尚无该字段，§1.3） |
| `provider` | `provider` | 仅用于读表定位；kind 才是 wire 契约 |

### 1.1 裁定记录

| # | 决策 | 裁定 | 时间 |
|---|---|---|---|
| **D-1** | V2 冻结哪个账号 | **①= `deepseek-v4-flash-roojin` + `anthropic/f3634ff267d6`** | 2026-09-25 |
| **D-2** | 凭据如何绑定身份 | **A**（注册表只读副本同源取出 id 与凭据，移除环境回退）。这是 B 建议并**已按此实现驱动**的选项；**负责人尚未逐字确认**，签核时应一并确认 | 2026-09-25 |
| **D-3** | 限额 identity probe | **不申请**（bucket 离线可算）。凭据可用性仍只在 Gate 2 现场做布尔核验 | 2026-09-25 |
| **D-4** | 采样额度与时间窗 | 沿用 §4 预算表；时间窗待负责人给出 | 待定 |

### 1.2 D-2 的残余限制（如实声明）

D-2 选项 A 消除的是**同一进程内的两源不一致**（本次事故的根因）。它**不能**证明一把凭据属于某个账号：route bucket 有意**不哈希凭据**（`team_backend_build.go:141`），客户端无从验证。"这把 key 属于 `roojin`"仍由操作者断言。见 §7 L-10。

### 1.3 `Effort` 必须与 bucket、model **分别**冻结（本轮新发现）

`strataIdentity`（A 的 gate）**没有 Effort 字段**，因此当前 gate **不比较它**。但：

- 注册表里 `roojin` 的 `Effort = "max"`；V1 的驱动自造条目**没有** Effort 字段（`""`）。
- `reasoning_replay.go:79-80`：`effort != ""` 时 `output_config.effort` **进入请求体**。

因此 **V2 的请求形状与 V1 不同**（多一个 body 字段），且**改变 Effort 不会触发 gate**。两条处置：

1. **B 侧**：Effort 已进入本节身份表，V2 的驱动必须把 `Effort` 与 gate 的 7 个字段**并列比较**（B 的写集，见 §6）。
2. **A 侧（移交）**：建议把 `Effort` 加入 `strataIdentity` 与 `strataExpectationIsFrozen`，否则 gate 的"7 个字段逐项比较"有一个可本地确定的缺口。**A 的写集，B 不改**。

**对 S4 的影响**：`output_config` 是请求参数、不是内容，故**首轮 prompt 不变**（落桶安全）；风险只在**逐轮增长速率**——见 §5.2。

## 2. 主要问题与唯一主要变量

**主要问题**：在冻结的 K1 构建与固定 route/model/account 下，真实 Team member 的 `miss_tokens_per_request` 是否随**上下文大小**、**会话阶段**、**压缩边界**或**并发档**变化？

**唯一主要变量（每次只增一个）**：S0 无 → S1 **上下文大小** → S4 **长上下文** → S2 **会话阶段**（可达性见 §5.2）→ S3 **并发档**。

**明确不做**：不切换 model/route/account/缓存配置；不用裸请求或合成长 prompt 冒充真实 Team；不为触发 fold 改任务族或压缩配置。

## 3. 臂登记（采样前冻结）

**任务族（冻结）**：`long-context-single-word-recall`，与 pilot / V1 六臂同族，**驱动方登记，不进记录**。

| 臂 | 目标桶 | 首轮 brief | 成员 | 轮/成员 | warm 目标 | 并发 | 臂 token 上限 |
|---|---|---|---:|---:|---:|---:|---:|
| **S0-canary**（§6.3.1 现场核验） | `lt_32k` | 40 KB | 1 | 1 | **不计入任何分母** | 1 | 64K |
| **S1a-small** | `lt_32k` | 40 KB | 3 | 12 | 33 | 1 | 2M |
| **S1b-mid** | `128k_256k` | 900 KB | 3 | 12 | 33 | 1 | 10M |
| **S1c-large** | `512k_768k` | 2,800 KB | 3 | 12 | 33 | 1 | 26M |
| **S4-768k** | `768k_1m` | 3,750 KB | 3 | **12**（V1 为 8） | **33**（V1 为 21） | 1 | 30M |
| **S3-c2** | `lt_32k` | 40 KB | 3 | 12 | 33 | 2 | 2M |
| **S3-c3** | `lt_32k` | 40 KB | 3 | 12 | 33 | 3 | 2M |
| **合计** | — | — | 18 + 1 | — | **198** | — | **72.1M** |

- **总上限仍为 80M**（方案 §6.2 的既有上限，**不提高**）；上表各臂上限合计 **72.1M**，留约 8M 余量，**总上限与臂上限同时生效，先到先停**。按 V1 实测外推的**实际**消耗约 **58.1M**（六个样本臂 + canary）：0.43 + 6.94 + 21.33 + **28.5** + 0.43 + 0.43 + 0.012。
- **每臂上限都高于其 V1 实测值**（S1b 6.94→10、S1c 21.33→26、S4 28.5→30），即上限是**停止保护**而非预期消耗；**不因上限宽裕而增加样本**。
- **S0-canary 是 §6.3.1 要求的现场核验，不是样本臂**：1 成员 × 1 轮，回答三个是非问题——① 这把凭据**真的可用**；② 该账号**真的报 cache split**；③ 落地的 route bucket **真的等于冻结值**。三者任一为否则**立即终止全部采样**（§8.5：与正式样本分开编号，计入总预算与独立探针日志，**不计正式 warm 分母**）。
  - **③ 的证据强度须如实标注**：`route_bucket` 由 `name/endpoint/proxy` **纯客户端**派生（`team_backend_build.go:145`），而运行 store 由**同一个** `entry` 构造，故记录里的 bucket **必然**等于 preflight 通过的值——③ 因此**随会话成立，不是独立证据**（C 的 R1-b）。真正未被覆盖的是**凭据 ↔ 账号**的绑定，而它**客户端不可验证**（L-10）。canary 的实质价值在 ① ②。
  - **运行后仍断言 ③**（一行，B 的写集）：`report.RouteBuckets` 必须**恰好等于**冻结的 bucket——不是"恰好一个"（`assertStrataPipeline` 只断言后者，C 的 R1-a）。它捕捉的是"记录归属被别的路径改写"这一类问题，**不**构成对 ③ 的独立证明。
  - **为什么不并入 S1a**：S1a 要为 `lt_32k` 贡献 33 个 warm 样本，是**正式分母**；把核验塞进去会让"发现凭据不可用"与"该臂样本不足"混成同一个结论。分开跑，代价仅 1 次请求，却能在大额臂之前止损。
  - **S1a 不再另设 S0-baseline 复现臂**：构建/route 漂移已由 A 的 preflight **离线**逐字段拦截（0 token），重复跑一遍 lt_32k 只换来同一份数字，是纯浪费。
- **S4 由 8 轮补到 12 轮**是方案 §6.2 的明文要求（旧 21 warm 不计入，须 ≥30/≥3）。按 V1 实测 19,018,148 token × 12/8 ≈ **28.5M** 外推——此为**规划估算，不是实际消费**，最终按真实输入 token 累加。
- **S3 保留**：V1 六臂的"并发 1→2→3 无差异"是本轮最便宜的可复现发现（两臂合计 <1M token），换 route 后必须重建。
- **`gte_1m` 不设臂**：结构性不可达（§5.1），记为 BLOCKED。
- **落错桶如实记录，不重跑凑数。**

## 4. 样本量、预算与停止规则

### 4.1 样本量

- 主要桶门槛：**≥30 个有效 warm 单请求 且 ≥3 成员**（方案 §1）。
- 每臂 3 成员 × 12 轮 = 36 条，其中首轮 3 条单列、warm 33 条 → **达标**。
- **独立单元另行报告**：每成员 1 个 session，故跨成员桶的独立单元是 **3**，不是 33。**同一 session 的连续请求不得当作独立重复**（方案 §1）。
- 三个率并列且不可互换：token 加权率 / 成员等权率 / session 汇总率。

### 4.2 预算

- 价格未配置（`REASONIX_LIVE_CACHE_PRICE_*` 未设）→ **成本报 unknown，不编造、不用外部价表折算**。
- 费用控制按 **token 预算**执行，可在无价格时确定性执行。
- 达到**臂上限或总上限（80M）即停止**，不论进度。

### 4.3 停止规则（仅限已登记原因）

1. 累计输入 token 达到臂上限或总上限；
2. 连续 3 次服务错误；
3. 连续 3 次 usage 不可用；
4. `request_count_source != observed`；
5. **身份漂移**：`strataPreflight` 任一字段不匹配（池条目 / kind / endpoint / wire model / model_ref / route bucket / build）；
6. 隐私违规（任何正文/凭据进入归档）；
7. 单臂挂钟 > 60 分钟。

**中途命中率有利或不利都不构成停止理由。**

### 4.4 排除规则（沿用 A v1 契约，不新增、不放宽）

| 分类 | 处理 |
|---|---|
| `request_count_source != observed` 或 `count != 1` | 排除并单列 token |
| `usage_unknown` / `usage_estimated` | 排除 |
| `accounting_invalid` | 排除 |
| `hit + miss <= 0`（no cache split） | 排除，**不读作 0 命中** |
| `observed_at` 不可解析 | 排除 |
| 首轮 | 单列为 `first_request`，不并入 warm |
| `hit == 0` | 表述为"**未报告 cache read**"，**不称冷启动** |

**低命中样本不因结果不利而被排除或重分类。**

## 5. 长上下文与压缩边界

### 5.1 `gte_1m`：结构性不可达

```
桶下界                = 1_048_576
成员 hardInputCeiling = 1_000_000 − 256 = 999_744
=> 1_048_576 > 999_744 => 不可达
```

记为 **BLOCKED / NOT EXECUTABLE**（客观证据），**不构造替代**。

### 5.2 压缩/fold 边界：**可达窗口比 V1 推导的更窄**（本轮新发现）

V1 预注册 §2 推出 S4 的 brief 带宽 ≈3,633–3,905 KB，上界用的是 `hardInputCeiling`（999,744）。**这不对**：次轮起标定比值生效后，**绑定上界的是 fold 触发线（800,000），不是硬上限**。

```
实测标定（同族、同端点、V1 的 S4）:
  brief 3,750 KB = 3,840,000 chars  ->  首轮 ctx 792,340 tok   =>  0.20634 tok/char

两个边界必须同时满足:
  桶下界        ctx  >= 786,432
  fold 触发线   估算 ≈ ctx  <  800,000      <- 次轮起生效，比硬上限低 ~200K
  hardInputCeiling                          仅约束首轮回退估算 0.25×chars

首轮口径（下面 §5.3 补齐逐轮增长项后为准）:
  =>  brief ∈ (3,722 KB, 3,785 KB)，宽约 63 KB
```

**首轮为什么没触发 fold**（机制核验）：首轮无标定 → 回退比值 0.25 → 估算 960,000 > 800,000，**已越过触发线**，折叠路径确实被进入，但**找不到可折叠区**：

```
msgs = [system, user(brief)]，pinnedPrefixLen -> head = 1（只钉住 system）
tailStart：单条巨消息撑爆 tail 预算 -> start = len(msgs) = 2
planFoldRegion 先试 min=minCompactMessages(2)：start-head = 1 < 2 -> 失败
  -> **显式回退到 min=1**（compact_projection.go:682）：1 >= 1 -> 通过
activeTurnStart -> active = 1（本轮起始的用户消息）
splitActive=false（非 overflow 救援）-> **start := active = 1**   <- 绑定约束
return start > head  =>  1 > 1 = false  => 无可折叠区，no-op
```

> **机制归因更正（2026-09-25，依据 C 的 GC-5 并经本人逐行复核）**：本文原写「`planCompaction` 要求可折叠区 ≥ `minCompactMessages = 2`」。**不成立**——`planFoldRegion` 在 `!ok` 时**显式回退**到 `planCompaction(msgs, 1, force)`（`compact_projection.go:682`），该约束因此被绕过。真正的绑定约束是随后的**活动轮钳制**（`compact_projection.go:684-691`：首轮全部可见消息都在活动轮内 → `start` 被钳回 `head` → `start > head` 为假）。**结论方向（fold 不可达）不变，引用错了约束。**

**因此 S4 依赖首轮的消息形状**（不是"依赖 minCompactMessages"）：只要首轮多出一条非活动轮的历史消息，折叠区就非空，**fold 会在首轮触发**，S4 的整个上下文前提随之失效。驱动必须保持与已验证形状一致（首轮 = system + 单条 brief 用户消息），且每臂必须记录 `prefix_change_reasons`，**fold 一旦出现即被观测到**（判据是观测量，不是推导）。

**次轮起为何不再进入折叠路径**：标定比值（0.20634）已由首轮真实 usage 写入 → 估算 ≈ ctx ≈ 792,000 < 800,000 → `est < fold` 直接早退，折叠路径连进入都不会。**故 fold 的可用性由两段机制共同保证**：首轮靠活动轮钳制，次轮起靠标定后的估算低于触发线。

**判定**：压缩阶段记为**不可执行**（本任务族 + 本窗口下无样本），**不得**为触发 fold 而改任务族、窗口或压缩配置——那会同时改动两个变量。

### 5.3 S4 的带宽须含**逐轮增长项**（本轮自查修正）

§5.2 的带宽只用了**首轮** ctx 与 fold 线比较，那是不完整的：fold 线在**每一轮**判定，而 ctx 逐轮增长，故**绑定约束在最后一轮**。

```
ctx_N ≈ ctx_1 + (N-1)·g          g = 每轮增长
约束（对全部 N=1..12）:
  ctx_1              >= 786,432        桶下界
  ctx_1 + 11g        <  800,000        fold 线（第 12 轮）

V1 实测（18 个成员独立复算，见下）:
  g ∈ {23, 24}，每个成员一个固定值，无一轮减少
  -> 取保守值 g = 24:
  ctx_1 ∈ [786,432, 799,736)  ->  brief ∈ (3,722.0 KB, 3,784.9 KB)，宽 62.9 KB
  推荐 3,750 KB：距下界 28.0 KB，距上界 34.9 KB（大致居中）
```

> **g 的取值更正（2026-09-25，依据 C 的 GC-6 并经本人独立复算）**：本文原写「g = 23 tok/轮（每成员一个固定值）」。独立复算 18 个成员的逐轮增量：**15 个为 +23，3 个为 +24**（`S3-c2-m3`、`S4-768k-m1`、`S4-768k-m2`）。**「每个成员一个固定值、无一轮减少」成立**（没有任何成员出现混值），但「全部 +23」不成立。上式已取保守值 g = 24；带宽因此从 63.0 KB 微调为 **62.9 KB**，**推荐值 3,750 KB 不受影响**。

**增长容限**：在 3,750 KB 下，首轮 ctx = 792,340，余量 7,660 token / 11 轮 → **可容忍 g < 696 tok/轮**，而实测 g ∈ {23, 24}，余量约 30×。S4 因此**不是**"贴着 fold 线"，但它是**唯一一个余量有限的臂**。

**与 §1.3 的 Effort 变化联动（本轮已登记的风险）**：V1 的 g = 23 是在 `Effort=""` 下测得的；V2 的条目 `Effort="max"` 会送出 `output_config.effort`。若该参数改变了**被回放的推理长度**，g 就可能变大。

- **观测事实**：V1 十二轮内 g 是**恒定单值**（每成员一个固定值，无一轮减少）。若推理块被回放进后续 prompt，g 会随推理长度**浮动**而非恒定。故在这个任务族下**推理未进入后续 prompt**（要么未产生、要么未回放）。
- **仍不能免除验证**：`ReasoningReplayCapabilities` 对 deepseek 声明 `Format: "anthropic-thinking"`（`reasoning_replay.go:84-89`），即回放**是被支持的**，只是在本任务族的实测中未表现为增长。
- **处置**：驱动必须逐轮记录 `ctx` 与 `prefix_change_reasons`；**若末轮 ctx 触及 fold 线或任一出现 fold，该臂按"预设边界被触发"如实报告，不重跑凑数**。

## 6. 硬防线落实（方案 §8 逐条）

> **状态说明（2026-09-25 更新）**：本表原为**规格**——C 的 GC-11 指出它读起来像已实现，而当时正式驱动 `internal/cli/live_team_cache_strata_formal_test.go` **尚不存在**，现有六臂驱动也不调用 preflight（这正是漂移得以通过的原因）。**该驱动现已交付**（B 的写集），本表的每一行都对应其中一处接线；**未运行任何真实请求**，接线为编译与离线 dry-run 所验证。

| §8 | V2 的落实 | 驱动中的落点 |
|---|---|---|
| 1. 期望身份来自单一冻结源，不留第二套手填默认值 | 驱动解析 V2 §1 的 JSON 身份块，并校验该文件 SHA-256；**不**再手填条目名（D-2 选项 A） | `formalLoadPreReg` / `formalJSONBlock` |
| 2. 计费请求前比较可本地确定项 | 每臂首请求前调用 `strataPreflightForPoolEntry`，任一漂移 fail closed；**另加 B 侧自己的 `Effort` 比较**（A 的 gate 尚无该字段，§1.3） | `formalEffortCheck` + A 的 gate |
| 3. 记录与 banner 保存实际身份，报告从记录汇总 | banner 写冻结 pre-reg 摘要、测量层摘要、解析后的实际身份、臂登记与 token 上限；报告从 `MemberCacheRequest` 汇总，**不从启动参数推定** | `formalArchive` |
| 4. 一臂出现多 route/model/account → 整臂隔离 | `assertStrataPipeline` 要求每臂恰一个 route bucket；每臂结束即核 | `assertStrataPipeline`（复用） |
| 5. identity preflight 与正式样本分开编号 | **S0-canary**（1 成员 × 1 轮）单列：计入总预算与独立探针日志，**不计入任何 warm 分母**；D-3 判定不需要额外的 route identity probe（bucket 离线可算） | `formalArms` 的 S0-canary |
| 6. run/member/session id 不跨臂误复用 | 成员 id 前缀为臂名；`run_id` 为纳秒时间戳；归档文件名 `formal-<arm>-<kind>-<runid>` 含两者 | `strataMemberIDs` / `formalArchive` |

**驱动额外的三条自我保护**（方案未要求，但本次事故直接要求）：**①** 未注册臂名被拒绝并列出已注册项；**②** 测量层摘要**读不到即拒绝运行**，而不是在 banner 里留 `unreadable` 占位（A 的六臂 banner 正是这样留下空字段的，见 §7 L-13）；**③** 一条**离线自洽性测试** `TestFormalPreRegIdentityMatchesTheResolver` 把本文的身份块送回生产解析链解析，断言它**解析到自己**——这正是 V1 缺失的那道检查。

> **自洽性测试已被负面对照验证**（2026-09-25）：把 V1 的矛盾对（`roojin` + `bfcb0811b1c8`）临时写回本文的身份块，该测试立即以 `route bucket drifted: expected "anthropic/bfcb0811b1c8", resolved "anthropic/f3634ff267d6"` 失败；文档随后**逐字节还原**（sha256 前后相同）。**即：若 V1 当初有这道检查，它会在冻结前就被拒绝。** 运行方式（离线、零请求、零凭据）：
>
> ```bash
> go test -tags live ./internal/cli/ -run TestFormalPreRegIdentityMatchesTheResolver -v
> ```
>
> 它也覆盖了方案 §5.2 的验收项「正确 frozen identity 的本地 fixture 通过，**且值与预注册 V2 一致**」——A 的测试用的是 `https://gw.example` 合成夹具，**不比对 V2 的真实值**；本测试补上该比对。

## 7. 已知限制（随结论一起引用）

| # | 限制 |
|---|---|
| L-1 | 单网关、单账号池条目、单 route、单 model。**不得外推为生产 Team 成员平均命中率。** |
| L-2 | 任务族是**合成**的单字召回，不代表代码读改、长文档分析或工具密集任务。 |
| L-3 | **成员数 = 3**：独立单元是 3 个 session，请求数达标也**不能**做跨成员总体推断。 |
| L-4 | `gte_1m` 结构性不可达（§5.1）。 |
| L-5 | 成本 **unknown**（未配置价格）。 |
| L-6 | 并发档是**登记值**，逐请求无并发字段（M1 冻结：有理由地拒落）。 |
| L-7 | oracle coverage 在成员侧**永久 `insufficient_provenance`**。 |
| L-8 | 128 块粒度仍是**归纳值**，未跨账号验证。 |
| L-9 | 历史账本与本轮**不可比**（窗口不重叠 + 无 provenance）。本轮只建立**新的前瞻基线**。 |
| **L-10** | **凭据与账号的绑定不可客户端验证**（§1.2）：bucket 有意不哈希凭据，故"这把 key 属于该账号"只能由操作者断言。 |
| **L-11** | **S4 的可用 brief 带宽约 63 KB**（§5.3，含逐轮增长项），且依赖首轮消息形状；首轮若多出一条非活动轮历史消息，fold 会在首轮触发。增长容限 g < 696 tok/轮（实测 g ∈ {23, 24}）。 |
| **L-12** | **`Effort` 未进 A 的 gate**（§1.3）：它改变 wire body（`output_config.effort`）。B 的驱动已并列比较它；A 的 `strataIdentity` 补齐前，gate 自身对该字段是盲的。 |
| **L-13** | **banner 的测量层摘要字段此前是空的**：V1 六臂的 `frozen_stream_usage_sha256` 全部写成 `unreadable:stream_usage.go`（驱动用相对路径，而 `go test` 的 CWD 是包目录）。测量层仍可由 banner 记录的 build `71d2e3695fdd` 的 blob 追溯到（与 M0 冻结值 md5 相同），**但不是通过该字段**。V2 的驱动改为从仓库根解析，且**读不到即拒绝运行**。 |

## 8. 归档与复算

- 归档至 `~/reasonix-partb-archive/<date>/`（**持久位置**；方案禁止 `/tmp` 作为唯一副本），文件名含臂名与 run id。
- 内容：逐请求记录 JSONL、报告 JSON、banner（含 preflight 结果与冻结版本 SHA-256）、run log。**不含正文、工具参数、凭据或认证头。**
- **复算口径**：由 C 从**只读归档**独立重建纳入/排除集与指标；B 只回答数据字典问题，**不自行改动被冻结规则**（方案 §4 P3）。
- 归档前置检查沿用 pilot 三条硬门禁（显式归档根、显式 build、不依赖 home 推导），并由 `strataArchiveDir` 在发请求前拒绝临时目录。

## 9. 签核清单（Gate 1）

| # | 项 | 状态 |
|---|---|---|
| 1 | A 交付 preflight 设计与测试 | ✅ `..._GATE_A_PREFLIGHT`；`team_preflight_gate_test.go` 现 **15 项测试**，B 独立复跑 **15/15 通过** |
| 2 | B 完成 V2 预注册 | ⏳ **本文**；身份块 D-1 已裁定（§1.1） |
| 3 | C 独立逐条审核并签署 PASS | ✅ **PASS**（`..._GATE_C_AUDIT` §12.8）：对修订 `b366ced4…` 签署 PASS，附三条非阻断建议——**三条已全部采纳**（§9.1 末三行），故本文摘要已变更，**请 C 确认采纳版本无异议**（按 §4「若有实质性变更，版本号递增并重新签核」——此三处只改引用与算术，**不属实质性变更**，由负责人判定） |
| 4 | 负责人记录签核时间与本文 SHA-256 | ⏳ 待负责人 |

**在 3、4 完成前，Gate 1 = BLOCKED，不发真实请求。**

**本文当前 SHA-256**（**签核时须现算并记录，不得从本文抄写**——C 的 GC-12 指出本文上一版内记的摘要与其自身实际摘要不符，那正是 §0 同一类教训）：

```bash
sha256sum docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION_V2.zh-CN.md
```

> **不复述数字的理由**：本文在签核前仍会变更，任何写进正文的"当前摘要"到签核时都已过期；把它印在正文里只会制造一个看起来权威的错值。签核人现算即可。

### 9.1 本文的更正记录（未冻结，故可更正）

| 日期 | 项 | 原文 | 更正为 | 来源 |
|---|---|---|---|---|
| 2026-09-25 | §3 各臂 token 上限 | S1b=12M / S1c=30M / S4=32M，**合计 82M > 80M 上限** | 10M / 26M / 30M，**合计 72.1M**（后续加 S0-canary 后） | 签核前算术自查 |
| 2026-09-25 | §3 臂表 | 含 S0-baseline（与 S1a 同条件，纯重复） | 删去；改为 **S0-canary**（§6.3.1 要求的小规模现场核验，1 成员 × 1 轮，不计入任何分母） | 方案 §6.3.1 / §8.5 对照 |
| 2026-09-25 | §1 | 缺 `Effort` | 补入身份表 + JSON 身份块（它改变 wire body） | 自查 + C 未覆盖 |
| 2026-09-25 | §5.2 机制归因 | 首轮不折叠归因于 `minCompactMessages = 2` | 归因于**活动轮钳制**；`min=2` 被 `compact_projection.go:682` 的显式回退绕过 | C 的 GC-5，本人逐行复核 |
| 2026-09-25 | §5.3 g 取值 | g = 23 tok/轮 | g ∈ {23, 24}（3/18 成员为 +24）；取保守值 24，带宽 63.0→62.9 KB | C 的 GC-6，本人独立复算确认 |
| 2026-09-25 | §9 | 正文内印了一个 SHA-256 | 删去，改为签核时现算 | C 的 GC-12 |
| 2026-09-25 | §3 外推式 | **7 项**，含已删除的 S0-baseline 残留 | **6 个样本臂 + canary**，合计 58.49M → **58.1M** | C 的 R1 建议 1 |
| 2026-09-25 | §1.3 引用 | `reasoning_replay.go:79-81` | `:79-80`（`:81` 是右花括号） | C 的 R1 建议 3 |
| 2026-09-25 | §3 canary 的 ③ | 三个是非问题并列为"可证" | ③ **随会话成立、非独立证据**（bucket 纯客户端派生）；canary 的实质价值在 ① ②；并加**运行后** `RouteBuckets[0] == 冻结值` 断言 | C 的 R1-a / R1-b |

**每一条都只改引用与算术，不改任何门槛、样本量或结论方向。**

## 10. 本文未做

- **未发起任何真实请求**；未构造、未使用任何替代凭据。
- 未修改 V1 预注册、未修改任何历史归档、未修改 A/C 的写集。
- 未把 V1 六臂的 204 条探索性记录计入本文的任何分母。
- 未提交、未推送、未开 PR。
