# Team Member 缓存命中率：Part C 独立复核与三方合流结论

> 执行依据：`TEAM_MEMBER_CACHE_OPTIMIZATION_EXECUTION_PLAN.zh-CN.md` §2 Agent C、§3 交接接口、§4 C0–C5。
> 日期：2026-09-24。代码版本：`58e4e9c6c`（Team-agent）。**本文不改任何生产代码。**
> 复核对象（三方均已交付）：
> - A：`TEAM_MEMBER_CACHE_PART_A_CONTRACT.zh-CN.md`（v1 统计契约）、`TEAM_MEMBER_CACHE_DATA_AUDIT.md`、`TEAM_MEMBER_CACHE_OBSERVABILITY_PLAN.md`
> - B：`TEAM_MEMBER_CACHE_PROVIDER_EXPERIMENT_PART_B.zh-CN.md`（9 臂 + 1 装配探针）
> - C：本文

---

## 0. 合流结论（决策先行）

| # | 结论 | 强度 | 影响 |
|---|---|---|---|
| **M-1** | **B 交上来的候选 (i)「稳定前缀之外的固定 miss 开销（232 tokens/请求）」不成立。** 那是网关 **128-token 缓存块**的切分余量，不是每轮成本：B 自己 11 份 journal 的 `hit` 全部是 128 的整数倍，且 `miss ≡ prompt (mod 128)` 逐条成立；同一夹具今日实测 miss 从 232 变成 3。 | **证据**（§2） | **候选 (i) 撤回**；据此做的工具描述剪枝不会有可测收益 |
| **M-2** | **适配层把"整段 prompt 预估"减"真实命中量"记成 miss。** 网关对同一请求发两次 usage，`input_tokens` 含义不同，而 `mergeUsage` 逐字段取 `max`。同一请求适配层报 **85.34%**，网关自己的 OpenAI 口径报 **99.91%**。 | **证据**（§3） | 这是**测量缺陷**（P4），不是缓存行为；所有绝对命中率数字都需重算 |
| **M-3** | **A 的 v1 契约把历史账本的 per-request 基线判为不可用，这是对的**，且与本轮发现同源：账本只存归一化值，而该归一化有缺陷。A 的 84.5%/67.7% 全样本口径是当前**唯一诚实**的历史数字。 | **证据**（§4.1） | 与 A 一致，无需修订 |
| **M-4** | **B 的 92.39% 与我复现的同一臂**（用 B 的夹具、B 的臂登记、直读网关原始数字）**是 99.87%**。差异来自 B 的适配层口径（见 M-2 同类缺陷的另一形式）。 | **证据**（§3.4） | B 的 9 臂结论方向可用，绝对数字需按 M-2 重测 |
| **M-5** | **A 的「每个 stratum 必须并列 miss/请求」是对的，但理由被 B 的假象误导了**：A §3.1 用 B 的 232→197 作为组成效应的证据，而那组数字是块对齐余量。组成效应本身仍成立（用我的裸请求阶梯可证），引用来源需换。 | **推断**（§4.2） | A 的列保留，引用换源 |
| **M-6** | **合流门禁通过**：`go build ./...`、五个包的测试全绿、repolint RED SET 与 HEAD **逐字节相同**（33 个改动文件无新增违规）。 | **证据**（§6） | 可合流 |

**一句话**：三方交付在**数据面**已经收敛（A 的契约、B 的实验矩阵、C 的复核都指向"本地原因需要先排除"），但**测量面**被一个适配层缺陷污染了——B 的 232 tokens 与 C 的 85.34% 是同一个缺陷的两种表现。修掉它之前，命中率数字不可验收。

---

## 1. 三方接口对齐检查（C0）

| 接口 | A 提供 | B 提供 | C 复核 |
|---|---|---|---|
| 数据口径 | v1 契约：主指标、§3.2 五条件、§3.3 排除分类、§3.4 覆盖率、`source` 标签 | 按 `cachelab.Sample.Classify` 自成一体的 arm 级口径，**不写 `.cache_requests.jsonl`** | ✅ 两者**不相加**（B §6 与 A §8.1 都明确声明）；无口径冲突 |
| 请求证据 | 成员/逻辑请求/attempt 关联；`request_count_source` 闭集 | 请求 digest + 字节数 + route/model + 版本 + 原始 usage | ✅ C 用同一录制器独立复现（§3.3） |
| 实验结果 | 覆盖率、成员归属、分层基线 | 9 臂真实 Provider 结果、混杂因素、质量结果 | ⚠️ **B 的结论方向可用，绝对数字被 M-2 污染**（§3） |
| 行为代码 | 只加字段/排除分类/覆盖率/渲染 | 只加 `internal/cachelab`（仅 live 测试导入） | ✅ 双方都未改 provider-visible 字节；C 未改任何代码 |

**C0 判定：通过。** 三方对"有效请求、主指标、排除项"的定义一致；对"usage 精度"的定义**形式上**一致但**实质上**都建立在被 M-2 污染的归一化之上——这是本轮最重要的接口级发现。

---

## 2. M-1：B 的"固定 232 tokens 开销"是 128-token 块对齐余量

### 2.1 B 自己的数据

B 的 11 份 journal（`/tmp/cachelab-runs/`，我只读，未修改）：

| 臂 | prompt | hit | miss | `hit % 128` | `miss % 128` | `prompt % 128` |
|---|---:|---:|---:|---:|---:|---:|
| B0-pilot ×3 | 3048 | 2816 | 232 | **0** | 104 | 104 |
| B1-baseline-repeat | 3048 | 2816 | 232 | **0** | 104 | 104 |
| B2-interval-short | 3048 | 2816 | 232 | **0** | 104 | 104 |
| B3-serialization | 3048 | 2816 | 232 | **0** | 104 | 104 |
| B3-system-tail | 3058 | 2816 | 242 | **0** | 114 | 114 |
| B3-tool-schema | 3054 | 2816 | 238 | **0** | 110 | 110 |
| B4-ladder-small | 3048 | 2816 | 232 | **0** | 104 | 104 |
| B4-ladder-mid | 17448 | 17280 | 168 | **0** | 40 | 40 |
| B4-ladder-large | 67269 | 67072 | 197 | **0** | 69 | 69 |

**11/11 条**成立的不变量是两条：

1. **`hit` 恒为 128 的整数倍**（`hit % 128 == 0`）；
2. **`miss ≡ prompt (mod 128)`**（即 `miss % 128 == prompt % 128`）。

由此 `miss = prompt − hit`，而 `hit` 只能落在 128 的整数倍上——**miss 的取值是"余数 + k×128"，k 由 provider 把前缀切在哪个块边界决定**。B 的 232 就是 `104 + 1×128`，不是"每次固定付 232"。

### 2.2 我的独立复现

`TestPartCGranularityProbe`（裸请求，步进 60 字符，走完一个完整块）：

| prompt | cached | miss | `prompt % 128` | `cached % 128` | `miss ≡ prompt (mod 128)` |
|---:|---:|---:|---:|---:|---|
| 1384 | 1280 | 104 | 104 | **0** | ✓ |
| 1403 | 1280 | 123 | 123 | **0** | ✓ |
| 1403 | 1280 | 123 | 123 | **0** | ✓ |
| 1422 | 1408 | 14 | 14 | **0** | ✓ |
| 1441 | 1408 | 33 | 33 | **0** | ✓ |
| 1441 | 1408 | 33 | 33 | **0** | ✓ |
| 1460 | 1408 | 52 | 52 | **0** | ✓ |
| 1479 | 1408 | 71 | 71 | **0** | ✓ |

`cached` 恒为 128 的整数倍，`miss` 恒等于余数（这一组 k=0，B 的那组 k=1）。**同一个不变量，不同的块边界落点。**

### 2.3 同一夹具、不同时刻，miss 是 232 / 12 / 3

用 B 的夹具字节重发（`TestPartCFixedMissSourceProbe`、`TestPartCBlockRuleProbe`）：

| 时刻 | prompt | cached | miss | `prompt % 128` | k |
|---|---:|---:|---:|---:|---:|
| B 的 journal（15:58） | 3048 | 2816 | **232** | 104 | 1 |
| 我的复现（18:20） | 3084 | 3072 | **12** | 12 | 0 |
| 我的复现（18:40） | 3075 | 3072 | **3** | 3 | 0 |

**同一个夹具，miss 在 232 / 12 / 3 之间移动，k 从 1 变到 0。** 这不是"固定开销"——固定开销不会随运行改变，而块边界落点会。

### 2.4 为什么这会误导候选 (i)

B 的 §6 把 168–242 tokens/请求 交给 A 作为候选 (i)："工具 schema 描述的**每 token 都落在未命中侧**，若有界剪枝能让该部分不再随每轮付费，其收益应按 miss/请求衡量"。

**这个推理在块对齐下不成立**：剪掉描述里的 N 个 token 只会把 `prompt` 平移 N，从而把 `prompt mod 128` 推到一个**几乎随机**的新值——测出来的"收益"是 ±128 的噪声，不是剪掉的 token 数。

**判定（证据）**：候选 (i) **撤回**。B 在 §5「未决」里已经正确地写了"232/168/197 不随尺寸线性，不能推算真实 miss 结构"——这个自我限制是对的，但 §6 又把它作为候选登记，两者不一致。

---

## 3. M-2：适配层的 `max` 合并把整段 prompt 减命中量记成 miss

### 3.1 现象（裸请求，不经 team 层）

`TestPartCDefinitiveFoldProbe`：

```text
=== warm (turn 2): 3 usage objects
   [0] message_start/message  input=  39149 read=      0 create=      0
   [1] message_start/message  input=     29 read=  33408 create=      0
       oracle(billing_usage.openai_usage): prompt_tokens=33437 cached_tokens=33408 miss=29
   [2] message_delta/top      input=     29 read=  33408 create=      0
       oracle(billing_usage.openai_usage): prompt_tokens=33437 cached_tokens=33408 miss=29
   RULE A (per-field MAX, production today): prompt=  39149 hit=  33408 miss=   5741 rate= 85.34%
   RULE B (LAST-WINS, cachelab)            : prompt=  33437 hit=  33408 miss=     29 rate= 99.91%
   oracle says                             : prompt=  33437 hit=  33408 miss=     29
```

`message_start` 里的 `input_tokens=39149` 是**整段 prompt 的预估**；`message_delta` 里的 `input_tokens=29` 是**未缓存余量**。`mergeUsage` 取 max 得到 39149，再按 inclusive 折算 `miss = 39149 − 33408 = 5741`——这个数在任何一次上游读数里都不存在。

### 3.2 量级（真实 member 生产路径）

`TestPartCMemberRawUsageProbe`，同一成员相邻两轮，唯一变量是"本轮追加了内容"：

| 成员 prompt | `message_delta`（真值） | 成员记录（适配层） | 假象 |
|---:|---|---:|---:|
| ~40K warm | input=44, read=36,608 | **90.02%** | 4,013 token ≈ **9.9pp** |
| ~400K warm | input=114, read=347,648 | **85.77%** | 57,567 token ≈ **14.2pp** |

`message_delta` 的 `read` 在两轮之间**完全相同**——真实命中没有退化。

### 3.3 为什么 B 的 92.39% 也受同一缺陷影响（M-4）

B 的录制器用的是**逐键 last-wins**（`internal/cachelab/recorder.go` 的 `rawUsage.merge`），所以 B 的数字**没有**被 max 合并污染。但 B 的 92.39% 仍不是网关的读数：

| 来源 | prompt | hit | miss | 率 |
|---|---:|---:|---:|---:|
| B 的 journal（last-wins） | 3048 | 2816 | 232 | 92.39% |
| 我用 B 的夹具直读网关（`TestPartCReproduceB1WithOracle`） | 3076 | 3072 | **4** | **99.87%** |
| 我用 B 的夹具经生产适配器（`TestPartCAdapterShapeProbe`） | 3050 | 2944 | 106 | 96.5% |

同一夹具、同一臂、同一账号，三个口径三个数。**差异全部来自"用哪个事件、按哪个约定读"**，不来自缓存行为。

### 3.4 影响面

- **B 的 9 臂结论方向**（间隔无差异、组件扰动全落 miss 侧、组成效应）**仍成立**——这些是比较性的，口径偏差在臂间近似相同。
- **B 的绝对数字**（92.39% / 99.04% / 99.71%）需按 M-2 重测。
- **A 的历史账本数字**（84.5% / 67.7%）同样是该口径下的值，A §5.3 已正确标注"上游是 inclusive 还是 exclusive 不可验证"——现在**可以验证了**（§3.1 的 oracle），所以这条限制可以从"不可验证"升级为"已验证为缺陷"。

---

## 4. 与 A / B 交付的对账

### 4.1 A 的 v1 契约：**通过**，且与本轮发现同源

| A 的结论 | 我的复核 |
|---|---|
| C-1 `request_count` 的「0 视为 1」无法区分观测与缺省 → 补 `RequestCountObserved` | ✅ 代码核验相符（`provider.Usage.RequestCountObserved`、`team.RequestCountSourceOf`、`stats` 的 `requests_observed`） |
| C-2 主基线只收 `observed && count==1` | ✅ 相符（`cachereport.go` 的 `cacheRequestIsBaselineEligible` 新增条件） |
| C-3 **历史账本 per-request 基线不可用**，只能出全样本 84.5% / 67.7% | ✅ **判定正确**。我复跑账本得到 09-23 = 84.5%、09-24 = 67.7%（全样本），与 A 一致 |
| C-4 新构建的成员记录 100% `observed` | ✅ 我复跑 `TestLiveTeamMemberCacheSession` 得到 12/12 计入，与 A 一致 |
| C-6 每个 stratum 并列 `hit/miss_tokens_per_request` | ✅ 相符（`CacheGroupStat.MissTokensPerRequest`） |
| L-1 provenance 不能区分"上游 merge 造出的 1" | ✅ **A 自己已如实标注**——这正是 M-2 的同一类问题的上游版本 |

**唯一需要补强的一条**：A 的 §3.1 用 B 的 `232 → 197` 作为"组成效应"的证据。组成效应本身成立（我用裸请求阶梯独立证实：33K→533K 全档 miss 30–90 token，率 99.90%→99.99%），但**引用的那组数字是块对齐余量**（§2）。建议 A 把引用换成裸请求阶梯，或直接引用 A 自己的 `DATA_AUDIT §2.4`（两臂 `128k_256k` 桶均值 prompt 更小而 miss/请求涨 71%）。

### 4.2 A 的措辞修正清单（在 v1 基础上）

| # | 位置 | 现措辞 | 建议 |
|---|---|---|---|
| 1 | A(v1) §5.3 / DATA_AUDIT §1.2 | "上游是 inclusive 还是 exclusive：**不可验证**" | 改为"账本不可验证；**经 loopback 代理直读原始事件可验证**——本网关同一响应发两次 usage，`input_tokens` 含义不同（§3.1）" |
| 2 | A(v1) §3.1 | 用 B 的 232→197 作组成效应证据 | 换用裸请求阶梯或 A 自己的 §2.4 桶表（§4.1） |
| 3 | A(v1) §8.1 表格第 2 行 | "A 的 `no_cache_split` 与 B 的 `usage_split=false` 是同一判据" | ✅ 成立；但 B 的录制器**实际未触发过该分类**（B 的 9 臂全部 `usage_split=true`），措辞可保留 |
| 4 | DATA_AUDIT §3 / OBSERVABILITY §7 | H1（长上下文可回收量） | 建议**作废**：裸请求阶梯显示大上下文 warm 命中 99.9%，H1 的前提（大上下文低命中）不成立 |

### 4.3 B 的措辞修正清单

| # | 位置 | 现措辞 | 问题 | 建议 |
|---|---|---|---|---|
| 1 | B §5 第 2 条、§6 候选 (i) | "字节完全相同的重复请求存在稳定上限…每次固定 miss 232 tokens" | 是 128-block 切分余量，且**不稳定**（同一夹具实测 232/12/3） | 改为"命中量恒为 128 的整数倍，`miss ≡ prompt (mod 128)`；miss 的绝对值由块边界落点决定，不是每轮成本" |
| 2 | B §6 候选 (i) | "工具 schema 描述的每 token 都落在未命中侧" | 成立，但**收益不可测**：剪枝只平移 `prompt mod 128` | **撤回候选**（§2.4） |
| 3 | B §5 第 3 条 | "prompt 3048→67269 时率 92.39%→99.71%，miss/请求几乎不变" | 数字受口径影响（§3.3）；结论方向成立 | 保留结论，标注口径 |
| 4 | B §4 表格 | 9 臂绝对率 | 受 last-wins 口径影响 | 标注"该口径下的值" |
| 5 | B §6 交 C 的 C1 状态 | "C1 证据门：仍不通过" | ✅ 正确 | 保留；但 C 侧已用独立证据补齐 M-2 的门（§5） |

---

## 5. 候选变更登记表（合流版）

| # | 候选 | 证据 | 目标组件 | 预期收益 | 风险 | 反证条件 | 回滚 |
|---|---|---|---|---|---|---|---|
| **K1** | **修正 `mergeUsage` 的跨事件字段混用**（`internal/provider/anthropic/anthropic.go:518-529`） | **强**（§3.1–3.3，真实 Provider，网关自带 oracle） | 无（不改 provider-visible 字节） | 上报命中率回到真值 | 中：影响所有 DeepSeek-Anthropic 网关；需验证 native/LongCat 不回归 | 若 native Anthropic 或 LongCat 修复后 input=0 | 单函数一行，无状态 |
| **K2** | 给 K1 加口径自检诊断字段 | 中 | 无 | 让同类上游变化可见 | 低（additive） | 上游本就自洽则恒空 | 删字段 |
| **K3** | 修 `cachelab` 的 `resolve()`：只认顶层 usage 词表 | 强（B 的 9 臂全 `usage_split=true`，说明当前未触发；但一旦网关形状变化会静默失败） | 实验夹具 | 提升夹具健壮性 | 低 | — | 还原 |
| ~~**K4**~~ | ~~成员工具面剪枝 `schema_prune`~~ | **不成立**：1567 tok 占 128K 的 1.2%；且 miss 是块对齐余量（§2） | — | — | — | — | — |
| ~~**K5**~~ | ~~fold/compaction 边界调整~~ | **不成立**：B §9.2 实测 fold 后仅一次冷轮、前缀不变 | — | — | — | — | — |
| ~~**K6**~~ | ~~工具输出限量/摘要~~ | **背景量**：P2 是内容量不是跌幅来源（我复跑：miss 比每轮新增大 ~20×） | — | — | — | — | — |
| ~~**K7**~~ | ~~前缀中段重写修复（P1）~~ | **已证伪**：真实会话逐请求 +165 字节，append-only（§6.2） | — | — | — | — | — |
| ~~**K8**~~ | ~~provider scope/TTL/路由亲和性（D2）~~ | **已证伪为主因**：裸请求 33K–533K 全档 99.9%+ | — | — | — | — | — |
| ~~**K9（新）**~~ | ~~B 的候选 (i)：稳定前缀外的固定开销~~ | **不成立**：128-block 切分余量（§2） | — | — | — | — | — |

**计划 §2「候选优化顺序」逐条对照**：

1. "修正被请求差分和真实 Provider 对照共同支持的稳定前缀意外变化" → **无候选**（前缀稳定，§6.2）
2. "固定工具 schema 尾部成本被实验支持" → **K4 不满足**
3. "工具输出或重复状态被实验支持" → **K6 是背景量**
4. "低命中集中在 fold/compaction 边界" → **K5 不满足**
5. "Provider scope/TTL/容量/路由亲和性" → **K8 已排除**
6. **计划未列、证据最强**：**K1**（不是缓存行为优化，是测量修复）

---

## 6. 合流门禁（C5）

### 6.1 构建与测试

| 检查 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go test ./internal/team/ ./internal/provider/... ./internal/stats/ ./internal/cachelab/` | **全绿** |
| `go test ./internal/agent/ ./internal/cli/` | **全绿**（53.5s / 81.3s） |
| `go run ./tools/repolint` | RED SET 与 HEAD **逐字节相同**（`diff` 为空）；33 个改动文件无新增违规 |
| 临时探针 | 12 个 `zz_partc_*_test.go` 已**全部删除**，`go test -tags live -run TestPartC` 报 `no tests to run` |

**既有红项（HEAD 已有，非本轮引入）**：`chat_tui_team_render/reset/session.go`、`team_history_sync.go`、`team_replay.go`、`team_task_service.go`、`messages_usage.go` 的 essay/file-size 超预算，按 carry-forward 处理。

### 6.2 独立复核证据汇总（本轮新增，全部真实 Provider）

| 探针 | 结论 | 支撑 |
|---|---|---|
| `TestPartCDefinitiveFoldProbe` | 同一请求两种口径差 14.6pp，网关 oracle 站 last-wins 侧 | M-2 |
| `TestPartCMemberRawUsageProbe` | member 生产路径上，40K 假象 9.9pp、400K 假象 14.2pp；`read` 无退化 | M-2 |
| `TestPartCSizeLadderRawProbe` | 33K–533K 五档 99.90%–99.99%，miss 30–90 token | K8 排除 |
| `TestPartCMemberRequestStabilityProbe` | 真实 member 会话逐请求 **+165 字节**，无原位重写 | K7 排除 |
| `TestPartCReproduceB1WithOracle` | 用 B 的夹具直读网关：**99.87%**（B 报 92.39%） | M-4 |
| `TestPartCGranularityProbe` | `cached % 128 == 0`、`miss ≡ prompt (mod 128)` 逐条成立 | M-1 |
| `TestPartCBlockRuleProbe` | B 的夹具今日实测 miss=3（B 当时 232） | M-1 |
| `TestPartCPerAccountConventionProbe` | 同一网关不同账号：`billing_usage` oracle 有时在有时不在 | 口径不稳定 |

### 6.3 残余不确定性

1. **09-24 跌幅里未被假象解释的部分仍未知**：`32k_128k` 桶 miss 占 prompt **33%**，大于假象上限 14%。
2. **本路由之外未审**：native Anthropic、LongCat、OpenAI 直连、`responses` 是否也有同类跨事件字段混用。K1 的规则必须对它们逐一验证。
3. **`cache_creation_input_tokens` 本网关恒为 0**：A §5.3 已如实记录，仍未决。
4. **`billing_usage` 是网关私有扩展**，不在任何公开协议里：可作 oracle，不可作契约依赖。
5. **`route_bucket` 跨进程变化**：同一 pool 条目、同一 endpoint，bucket 在两次运行间不同。与 A §11.11 的"轮换 API key 不换 bucket"不矛盾（我未换 key），但值得单独核查。
6. **B 的 B2-interval-long / B5 / B6 未执行**：B 已在 §7 如实列明原因。

---

## 7. C1 / C2 准入判定（合流版）

### C1 证据准入

| 侧 | 状态 | 依据 |
|---|---|---|
| A | **通过** | 成员归属、诊断覆盖率、可复跑基线、provenance 闭集均已核验（§4.1） |
| B | **部分通过** | 9 臂真实 Provider、单变量、可复现（B 自己 4 次独立运行逐项一致）；但绝对数字受口径影响（§3.3） |
| C 侧补齐 | **K1 的证据门由 C 独立提供** | K1 不是缓存行为改动，不需要 B 的缓存对照；其判定式是"适配层读数 == 网关 `message_delta` + `billing_usage` oracle"，C 已逐请求验证 |

### C2 候选准入

**K1 放行**（满足全部四项）：

| C2 要求 | K1 |
|---|---|
| 只处理一个被支持因素 | ✅ 跨事件字段混用 |
| 有基线差分 | ✅ 85.34% → 99.91%（同一请求） |
| 预注册判定 | ✅ 逐请求断言 `CacheHitTokens == message_delta.cache_read` 且 `PromptTokens == message_delta.input + read` |
| 质量/成本测试 + 快速回退 | ✅ §8；单函数一行 |

**K2/K3 放行**（低风险、additive/仅夹具）。
**K4–K6 不放行**（证据弱）；**K7–K9 关闭**（已证伪或不成立）。

### C3/C4 不适用

K1 不改 provider-visible 字节、不改成员隔离、不改上下文策略，因此**没有"限单一测试 member"的灰度语义**——它的影响面是**上报口径**，全量生效即全量正确。按 member 灰度反而会产生"同一团队两个成员口径不同"的更糟状态。

**但它需要一次口径切换公告**：所有下游消费者（账本、报表、UI、`.usage.json` 历史）会看到数字**阶跃**。这不是灰度问题，是版本问题——与 A 的 v1 契约升版同一性质。

---

## 8. 质量与成本护栏（K1）

| 护栏 | 判据 | 越界 |
|---|---|---|
| 口径自洽 | 每条记录 `prompt == hit + miss` **且** `prompt == message_delta(input + cache_read)` | 阻断 |
| oracle 一致 | 有 `billing_usage` 时 `cached_tokens == CacheHitTokens` 且 `prompt_tokens == PromptTokens` | 阻断 |
| native Anthropic 不回归 | `start` 带 input+cache、`delta` 只带 output 的流，结果与修复前逐字节相同 | 阻断 |
| LongCat 形状不回归 | 只有 `delta` 带全部计数的流，结果与修复前相同 | 阻断 |
| 计费不变 | `CacheWriteBilledTokens` 与修复前相同 | 阻断 |
| 任务质量 | 修复不改请求字节 → 结构上不可能变化；以"请求字节摘要逐字相同"作替代证据 | 阻断 |
| 观测覆盖率 | `cachereport` 的 `exclusions` 各类计数不变 | 告警 |

**成本**：K1 本身零 API 成本。验证成本见 §9。

---

## 9. 最小补丁提案（**未落地**）

> 按计划 §2 的禁止事项与 C1 接口约定（A 独占统计契约），K1 触及**所有消费者**的分母口径，**不由 C 单方面落地**。

**位置**：`internal/provider/anthropic/anthropic.go:518-529`。

**现状**：

```go
// Counters are cumulative and non-negative, so retaining the largest value also
// tolerates gateways that repeat partial usage in both events.
inTok = max(inTok, usage.InputTokens)
outTok = max(outTok, usage.OutputTokens)
cacheCreate = max(cacheCreate, usage.CacheCreationInputTokens)
cacheRead = max(cacheRead, usage.CacheReadInputTokens)
```

**问题**：`input_tokens` 在不同事件里含义不同（整段预估 vs 未缓存余量），max 取到"整段"。

**提案规则**（对每个字段独立）：

1. 取**最后一个携带 cache split**（`cache_read` 或 `cache_creation` 非零）的事件的全部四个读数；
2. 若没有任何事件携带 split，退化为**最后一个非零读数**；
3. `output_tokens` 维持 max（该字段确实单调累积）。

**逐形状验证**：

| 形状 | 事件序列 | 提案结果 | vs 修复前 |
|---|---|---|---|
| 本网关 warm | start(input=39149) → start(input=29, read=33408) → delta(同) | 规则 1 → input=29, read=33408 | **修正** |
| 本网关 cold | start(input=39149) → delta(input=33437) | 规则 1 → 33437 | **修正** |
| native Anthropic | start(input=N, read=R) → delta(output=O) | 规则 1 → N/R（delta 无 split） | **不变** |
| LongCat | start(无 usage) → delta(全部计数) | 规则 1 → delta | **不变** |

**已知盲点（须一并评审）**：若某上游在"整段 prompt 100% 命中"时发 `delta(input=0, read=0)`（省略 split），规则 2 会取到 start 的 N，从而**高估 miss**。本网关未观察到该形状，但它是规则 2 的已知盲点——建议实现时对"delta 存在但全零"单独记一条诊断（即 K2）。

**判定式（落地后逐请求断言）**：

```text
CacheHitTokens   == message_delta.cache_read_input_tokens
PromptTokens     == message_delta.input_tokens + message_delta.cache_read_input_tokens
CacheWriteTokens == message_delta.cache_creation_input_tokens
billing_usage.openai_usage.cached_tokens == CacheHitTokens   # 有该字段时
```

**回滚**：还原 `mergeUsage` 的四行；无状态、无迁移、无持久化影响（历史账本保持原值，不回填）。

---

## 10. 最终判定

| 问题 | 判定 |
|---|---|
| 是否找到可归因的单一因素？ | **是**：usage 跨事件字段混用（P4 的具体形式） |
| 是否通过 C1？ | A **通过**；B **部分通过**（数字受口径影响）；K1 证据由 C 补齐 |
| 是否通过 C2？ | K1/K2/K3 **通过**；K4–K6 **不通过**；K7–K9 **关闭** |
| 是否实施生产改动？ | **K1 建议实施**（§9）；K2/K3 低风险可一并；其余不实施 |
| 是否继续采样？ | **是**：B 需按新口径重测 9 臂；A 需在 K1 落地后重算基线并升 v2 |
| 合流是否可放行？ | **可放行**：门禁全绿（§6.1），三方接口对齐（§1），无冲突文件 |
| 最诚实的总结 | 三方在数据面已经收敛；测量面被一个适配层缺陷污染。**修掉它之前，"命中率下降"无法验收；修掉它之后，"命中率"这道题可能大部分已经不存在了** |
