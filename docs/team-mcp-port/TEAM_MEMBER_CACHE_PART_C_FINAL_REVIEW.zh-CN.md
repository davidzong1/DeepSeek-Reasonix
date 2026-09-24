# Team Member 缓存命中率：Part C 独立统计复核、夹具修正与合流准入（联合结论后）

> 执行依据：`TEAM_MEMBER_CACHE_POST_JOINT_3AGENT_PLAN.zh-CN.md` §3 Agent C、§5 P1–P4、§6 门槛、§8.4 派单文本。
> 日期：2026-09-24。基线：`7bda0c3d32d2`（Team-agent-merge-mainv2）+ 本轮未提交改动。
> 前置：`TEAM_MEMBER_CACHE_JOINT_CONCLUSION.zh-CN.md`、`TEAM_MEMBER_CACHE_PART_C_REVIEW.zh-CN.md`（上一轮 C）、
> `TEAM_MEMBER_CACHE_PART_A_PROVIDER_USAGE_MATRIX.zh-CN.md`（A 本轮）、`TEAM_MEMBER_CACHE_PART_B_PILOT_REPORT.zh-CN.md`（B 本轮）。
> **本文不修改 A 的生产 Provider 语义、不改 B 的请求构造与任务组成、不为结果调整统计分母或排除规则。**

---

## 0. 结论摘要（决策先行）

| # | 结论 | 强度 | 影响 |
|---|---|---|---|
| **C-1** | `cachelab` 的 usage 解析器**读不了本网关的真实响应**：把顶层 Anthropic 计数器与网关私有的嵌套 oracle 块**合并**，于是同一条响应里出现两套词表，`resolve()` 判为 `unresolved_vocabulary` 并**放弃整个样本**。上一轮 C 复现 B0-pilot 时 9/9 `no_cache_split` 就是这一条。 | **证据**（§2.1） | K3 落地；B 的 9 臂结果**不受影响**（§2.4） |
| **C-2** | 同一缺陷还让解析结果**不确定**：嵌套块里的 `input_tokens=0` 与顶层的 `input_tokens=113` 同名，取到哪个取决于 Go 的 map 遍历顺序。**同一份字节 300 次解析得到两种读数**（0 出现 58 次、113 出现 242 次）。 | **证据**（§2.2） | 任何"夹具读数"都必须先证明它可复现 |
| **C-3** | K1 在本路由上**不需要私有 oracle 即可独立验证**：4 条只用协议字段与冷/热差分的判据，4 个 warm 轮次**零违反**。有 oracle 时适配层与协议读数 **6/6 逐字段相同**，oracle **22/22 一致**。 | **证据**（§3） | K1 从"依赖私有假设"升级为"算术 + 差分可证" |
| **C-4** | K1 替换掉的旧规则在同一批原始事件上会**多报 miss**：1.6K prompt 处 450 vs 真值 73（+377，**6.2 倍**）；196K prompt 处多报 **33,953 token（占 prompt 17.3%）**。旧规则的量级在真实规模上比上一轮估计的更大。 | **证据**（§3.4） | 联合结论 §2.4 的"假象 ~14pp"是**下限** |
| **C-5** | **A 的等样本量历史对比不可比**：09-23 的账本覆盖 `00:00–02:00` 与 `17:00–24:00`，09-24 只覆盖 `00:00–07:11`。`--first 1714` 因此是拿 09-23 的**全天**（由 18h–21h 高命中时段主导）比 09-24 的**早晨**（单调降到 54–60%）。按钟点对齐（两天共同覆盖的 00h/01h）：**86.14% vs 76.51%，差 9.6pp**，而不是 16.8pp。 | **证据**（§4.1） | 84.5%/67.7% 的 −16.8pp **撤回为不可比** |
| **C-6** | 但**剩余 9.6pp 仍不可归因**：账本没有 route、账号、成员、session、任务族字段，且对齐后的切片只有 169 / 633 行、本身达不到任何门槛。所以正确结论是"历史不可比 + 未决残差"，**不是**"解释掉了 7pp"。 | **证据**（§4.2） | 分支 C 适用；不得改压缩/工具/请求构造 |
| **C-7** | **K1 对历史账本的贡献无法从账本本身界定**：旧规则的方向取决于 `E/R`（只覆盖 messages 的估算 vs 含工具面的 cache read），而这个比值**没有进入折叠后的账本**。可检出的指纹（`hit == prompt` 3 行、双计形状 15 行）在本 route 上都很小，但"没有指纹"不等于"没有假象"。 | **推断**（§4.3） | 不得用历史账本给 K1 记功或记过 |
| **C-8** | B 的接口请求（B-C1）：`session_id` **已落地**并带三条语义约束与覆盖率字段；`concurrency_level` 与 `attempt` **拒落并给出理由**；`maintenance_phase` 与 `task_family` 按 B 自己的方案（派生 / 驱动方登记）。 | **决策**（§5） | B 的正式分层实验按此推进 |
| **C-9** | **合流门禁全绿**：build / vet / 五个包测试 / cli 缓存测试 / `cache-guard.sh` / repolint（本会话新增文件**零新增违规**）。 | **证据**（§7） | 可合流 |
| **C-10** | **准入判定：K1 通过（本路由），K3 通过，无任何缓存行为候选准入。** | **决策**（§8） | 分支 A（本路由）+ 分支 C（历史） |

**一句话**：本轮的测量面修好了（K1 在本路由可独立验证，K3 让夹具能读真实响应），但**统计面暴露了一个更大的问题**——被当作"历史跌幅 −16.8pp"的那个对比，本身是两天不同钟点窗口的对比。修掉它之后，剩下的 9.6pp 仍然无解释，而且这次**连"可比"都不成立**。

---

## 1. 三方接口对齐（P0 交付物 3）

### 1.1 数据口径差异表

| 项 | A（成员记录 / 账本） | B（`internal/cachelab` journal） | C 复核 |
|---|---|---|---|
| 数据落点 | `<owner dir>/.cache_requests.jsonl`、`config.StatsDir()/<day>.jsonl` | 实验 journal（JSONL，操作者指定路径） | ✅ **三者互不相加**；本轮 C 未把任何 journal 并入成员口径 |
| 主指标分母 | `Σhit/(Σhit+Σmiss)`，仅"有效单请求" | arm 级 `Summarize`，按 `Classify` 排除 | ✅ 判据一致；C 用**同一 `Summarize`** 复算（§2.4） |
| usage 精度来源 | 适配层归一化后的值 | **原始响应字节**，按 provider 词表读 | ✅ B 的读法本轮已修（§2）；A 的账本仍只有归一化值 |
| oracle | 无该字段（B 已列为覆盖缺口） | 本轮新增 `usage_oracle_present/agrees` | ✅ 只在 journal 侧，**不进成员口径** |
| `hit == 0` 措辞 | "未报告 cache read"（A v1 §2.3） | 同 | ✅ 一致 |
| 冷启动 | 不可确认（无 session 身份） | `first_request` 分类 | ✅ 一致 |

**判定**：A/B 两套数据**不相加、不互为基线**。C 本轮新增的 oracle 字段只存在于 B 的 journal 侧；A 的成员记录与账本**没有**该维度，且 C **不建议**为此改 A 的写集（理由见 §5.3）。

### 1.2 与 A / B 本轮交付的对账

| 对象 | C 的复核 |
|---|---|
| A §0 A-2（K1 在本网关可由算术独立判定） | ✅ **成立且更强**：C 用**四条**判据（不止算术矛盾）在真实链路上零违反（§3） |
| A §0 A-4（split 后 split-free 重申的折叠缺口，已加 `haveSplit`） | ✅ 复核：修复方向正确；C 的 4 个 warm 轮次未观察到该形状，属防御性修复 |
| A §0 A-7（缺失 vs 显式零在 `wireUsage` 上不可区分） | ✅ 成立；C 在 **journal 侧**可以区分（读原始事件），故 K2 类诊断只能落在事件层 |
| A §0 A-8（DeepSeek-compatible 自定义网关"有限通过"） | ✅ C 同判：本路由通过，但 C 仍**无法**在 native Anthropic / LongCat 上验证（无端点凭据），标记**未决** |
| B §0 B-1（K1 后 warm 99.79%–99.93%） | ✅ 与 C 的独立读数同量级（C 的探针 warm 95.3%–95.4%，prompt 更小、块余量占比更大，方向一致） |
| B §0 B-3（128 块不变量在成员路径成立） | ✅ C 的独立探针同样满足 `hit % 128 == 0`；**且 C 在历史账本上也验到 3,665/3,665 行满足**（§4.4） |
| B §6 门槛失败备注（A 的 5 个测试红） | ✅ **已消解**：本轮 C 收尾时 `go test ./internal/provider/anthropic/` 全绿；A 在同一时段完成了自己的修复 |
| B §5 末条（`billing_usage` 在场性在成员侧是覆盖缺口） | ✅ 确认；C 的答复见 §5.3 |

---

## 2. K3：`cachelab` usage 解析的缺陷与修正（交付物 2）

### 2.1 缺陷：读不了真实响应（结论 C-1）

用**真实网关**在 2026-09-24 20:12 的响应字节（仅 usage 对象，无正文）直接喂给 `parseUsage`：

```text
reported=true  split=false  shape=""  problem="unresolved_vocabulary"
keys=[cache_creation_input_tokens cache_read_input_tokens cached_tokens
      completion_tokens input_tokens output_tokens prompt_tokens]
```

7 个 key 出现在**一条**响应里：顶层的 Anthropic 计数器，加上网关私有 `billing_usage.openai_usage` 里的 OpenAI 计数器。旧 `rawUsage.merge` 对 JSON 树做无差别下降，把两者收进**同一个 map**，于是 `resolve()` 的 `anthropic && !openai` / `openai && !anthropic` 两个分支都不成立，**整个样本被放弃**。

这解释了上一轮 C 的观察（"复现 B0-pilot 时 9/9 样本 `no_cache_split`"）：那不是 provider 没报，是夹具读不了。B 的 11 份 journal 里也有同样痕迹——`partc-B0-pilot.jsonl` 的 9 条记录 `usage_keys` 是那 7 个 key 的并集，`usage_problem=unresolved_vocabulary`。

### 2.2 同一缺陷的第二面：读数不确定（结论 C-2）

网关的 oracle 块里**重复了同名 key 并置零**（`input_tokens: 0`、`output_tokens: 0`），而顶层是 `input_tokens: 113`、`output_tokens: 16`。合并成一个 map 后，谁写最后取决于 Go 的 map 遍历顺序：

```text
同一份字节解析 300 次：input_tokens ∈ {0: 58 次, 113: 242 次}
```

**约 19% 的解析读到 oracle 的零**。这不是"夹具不够准"，是**同一输入有两种输出**——任何基于旧夹具的绝对数字都缺少可复现性。

### 2.3 修正

新增 `internal/cachelab/usage.go`（`parseUsage` 与整个词表解析从 `recorder.go` 迁出，`recorder.go` 净减 274 行）：

| 规则 | 旧 | 新 |
|---|---|---|
| key 归属 | 任意深度按名字收割 | **绑定到拥有它的对象**；下降在 usage 对象处停止 |
| 嵌套明细块 | 无差别下降 | 只从**声明该块的对象**读取（`prompt_tokens_details`） |
| 跨事件方言 | 合并后按"哪种方言存在"选一条路 | 同一对象内混方言 → `unresolved_vocabulary`；**不同事件之间**混方言 → 新增 `cross_event_dialect_mix`，**不猜** |
| 网关私有块 | 与协议事件混在一起 | 记为**旁证**（`OraclePresent/Agrees/Prompt/Hit/Miss/Keys`），**永不参与**主读数 |
| 只有私有块 | 无从表达 | 新增 `oracle_only`：有读数但那是网关自己的账，不冒充协议事件 |
| 同名多拼写 | 先到先得 | 优先**有值**的拼写；全部为零时零就是读数（区分"未填"与"报了零"） |

### 2.4 重跑与验证（交付物 2）

**B 的 11 份 journal 用新解析器重放**（`TestReplayJournalsReclassifyTheSameRecords`，`PART_C_JOURNAL_DIR=/tmp/cachelab-runs`）：

| 臂 | 样本 | 计入 | 加权率 | hit / miss |
|---|---:|---:|---:|---|
| B1-baseline-repeat | 31 | 30 | **0.9239** | 84480 / 6960 |
| B2-interval-short | 31 | 30 | **0.9239** | 84480 / 6960 |
| B3-serialization | 31 | 30 | **0.9239** | 84480 / 6960 |
| B3-tool-schema | 31 | 30 | **0.9221** | 84480 / 7140 |
| B3-system-tail | 32 | 29 | **0.9209** | 81664 / 7018 |
| B4-ladder-mid | 31 | 30 | **0.9904** | 518400 / 5040 |
| B4-ladder-large | 31 | 30 | **0.9971** | 2012160 / 5910 |

**与 B 的报告逐项相同**。原因是 B 的录制器用的是**逐键 last-wins**（`rawUsage.merge` 在旧代码里被调用的方式），而 B 的臂样本里 oracle 只在装配探针路径出现；本 route 的 anthropic 记录里 oracle 与协议事件**不同名**（顶层无 `prompt_tokens`），所以旧解析器在那些样本上没有触发混表。**K3 不改变 B 的任何臂结果**，它修的是**读不了 / 读不准**的两类形状。

**新表驱动测试**（`usage_test.go`，11 行形状 + 4 个定向测试）：

| 测试 | 钉住什么 |
|---|---|
| `TestParseUsageReadsEachProviderShape` | 11 种形状：网关 warm/无 oracle/全命中零余量/冷启动/openai 嵌套明细/显式零明细/无 cache read/单事件混方言/**跨事件混方言**/oracle-only/无 usage |
| `TestParseUsageBindsKeysToTheirOwnObject` | 同字节 **500 次解析结果相同**；oracle 的零 `input_tokens` 不得覆盖已服务的余量 |
| `TestParseUsageReportsTheOracleOnlyWhenItDisagrees` | 旧规则的 39149/5741 读数被**原样保留**，同时 oracle 的不一致被记录而不是被采纳 |
| `TestParseUsagePrefersAPopulatedSpellingOfOneNumber` | 同名两拼写：取有值的；全部为零时零是读数 |
| `TestParseUsageKeepsTheOracleOutOfThePrimaryReading` | oracle 是旁证，**不能产生**样本 |
| `TestRecorderReadsTheGatewayShapeWithItsNestedOracle` | 端到端：真实网关形状经录制器得到 `1102/1024/78` 且可进基线 |
| `TestRecorderLeavesTheOracleUndecidedWhenAbsent` | 无 oracle 的样本 `present=false, agrees=false`，数字留空不复制 |

---

## 3. K1 独立复核：有 oracle 与无 oracle 两条路径（交付物 1）

### 3.1 有 oracle：适配层 == 协议读数

`TestLiveUsageFoldVerification`（`internal/cachelab/fold_verification_live_test.go`，`-tags live`）：生产适配器 → tee → 录制器 → 真实网关，逐轮比对适配层读数、协议事件读数与网关自己的账。

```text
cold turn: adapter prompt=1609 hit=0 | events: message_start(input=1986 read=0 create=0 out=0 billing=false)
                                            -> message_delta(input=1609 read=0 create=0 out=5 billing=true)
turn 2..5: adapter(prompt=1609 hit=1536 miss=73) served(prompt=1609 hit=1536 miss=73)
           per_field_max(prompt=1986 hit=1536 miss=450) cold=1609
           closed=true cold_closure=true aligned=true bounded=true adapter==served=true
           oracle(prompt=1609 hit=1536)
```

**4 个 warm 轮次：4 条判据全部成立，0 违反。**

### 3.2 无 oracle：4 条只用协议字段与差分的判据

私有 oracle **不是每个响应都有**（本轮 131 条探针记录里只有 32 条带它；排除只含 503 的那份 journal 后是 58 条中 32 条，缺的 26 条是 openai 方言的装配探针路径，该路径不发私有块）。因此复核必须有一条不依赖它的路径：

| # | 判据 | 为什么它不依赖 oracle |
|---|---|---|
| 1 | **冷/热闭合**：字节完全相同的请求，warm 的 `hit+miss` 必须等于 cold 报出的 prompt | 旧规则会取 split-free 的**估算**当 prompt，而估算**高于**真值 → 闭合被打破 |
| 2 | **块对齐**：`hit % 128 == 0` | 纯算术，来自 provider 的分块 |
| 3 | **估算不得成为 prompt**：折叠后 prompt ≤ cold 的 prompt | 同 1 的方向性版本 |
| 4 | **读数即已服务事件**：适配层数字 == 最后一个带 split 的事件的计数器 | 直接从原始事件重算 |

**实测：4 个 warm 轮次，0 违反。** 判据 1 的判别力有独立旁证——**cold 报 1611、warm 报 1536+75=1611**，而旧规则的 `per_field_max` 在同一批事件上给 1989。

### 3.3 修复方向与量级（结论 C-4）

同一次运行里把**旧规则**从同一批原始事件重算：

| prompt 规模 | 真值 miss | 旧规则 miss | 差 | 占 prompt |
|---:|---:|---:|---:|---:|
| 1,609 | 73 | 450 | +377 | 23.4% |
| 196,787（`overshoot_large.py`，14,000 items） | 51 | 34,004 | +33,953 | **17.3%** |

旧规则的假象**随 prompt 线性增长**（实测 `E/P` 稳定在 1.1725–1.1733，跨 68K–197K prompt 不变），所以：

- 上一轮 C 记的"40K 假象 9.9pp、400K 假象 14.2pp"是**成员路径**上的量级；
- **裸请求、大上下文**上的假象上限更高：**17.3%**。

**因此联合结论 §2.4 的"假象上限约 14pp"应修正为"≈17%（大上下文）/ ≈10–14%（40K–400K）"。**

### 3.4 K1 的已知盲点：实测边界（交付物 1 的"未决"部分）

| 形状 | 当前读数 | 判定 |
|---|---|---|
| 省略 split 的**全命中**（`delta(input=0, read=0)`） | `prompt=E, hit=0, miss=E` → 高估 miss | **未决**。本轮 11 次探针**均未观察到**该形状（warm 时网关总是发 split），且 `wireUsage` 上它与"完全没报 cache counter"同形（A §3.3）。下游按"未报告 cache read"措辞时不会产出错误的生命周期结论 |
| **工具面不在估算内** | 估算 E 只覆盖 messages，**不含 tools**（实测：tools=0 时 E=1971；tools=5 时 E 仍=1971，而真值 prompt 3686） | **新发现**。这使旧规则在**工具面大的请求上 miss 被地板到 0**（报告 100%），方向与"高估 miss"相反（§4.3） |

---

## 4. 历史账本的独立复核（结论 C-5、C-6、C-7）

### 4.1 A 的等样本量对比**不可比**（C-5）

复跑 A 的命令（`--first 1714`，两天等样本量）：

| 日期 | 计入 per-request 基线 | 全样本加权率 | 全样本 hit / miss |
|---|---:|---:|---|
| 09-23 | **0** | **84.47%** | 130,642,816 / 24,027,267 |
| 09-24 | **0** | **67.70%** | 110,437,632 / 52,698,416 |

**数字与 A 一致（84.5% / 67.7%）。但两天读入的窗口不重叠：**

| 日期 | 账本覆盖的钟点 | 行数 |
|---|---|---:|
| 09-23 | `00h, 01h, 17h, 18h, 19h, 20h, 21h, 22h, 23h` | 1,964 |
| 09-24 | `00h, 01h, 02h, 03h, 04h, 05h, 06h, 07h` | 1,713 |

09-24 的日文件最后一次写入是 **07:15**（`mtime`），所以它只有**早晨**；09-23 的日文件到 23:59，覆盖**晚间高峰**。`--first 1714` 于是：

- 09-23 取到的是**几乎全天**（1,714/1,964），其中 `18h–21h` 高命中时段占 **1,169 行（68%）**；
- 09-24 取到的是**整个文件**（1,713 行全是 00h–07h）。

**按钟点对齐（只取两天共同覆盖的 00h/01h）：**

| 切片 | n | 加权率 | miss/请求 |
|---|---:|---:|---:|
| 09-23 `00h+01h` | 169 | **86.14%** | 13,719 |
| 09-24 `00h+01h` | 633 | **76.51%** | 20,639 |
| **差** | — | **−9.6pp** | +6,920 |

**而 −16.8pp 是拿 09-23 的晚间比 09-24 的早晨得到的。**

同一天内部的变化同样大（说明"钟点"本身是真实分层，等样本量不控制它）：

```text
09-23  00h 83.4%  01h 87.3%  17h 68.1%  18h 88.3%  19h 88.4%  20h 89.8%  21h 91.9%  22h 74.8%  23h 79.0%
09-24  00h 76.0%  01h 76.9%  02h 69.7%  03h 66.3%  04h 60.8%  05h 59.7%  06h 60.6%  07h 54.4%
```

### 4.2 剩余 9.6pp 仍不可归因（C-6）

对齐之后**仍然差 9.6pp**，但这一条**不能**升级为结论，因为：

1. **样本本身不合格**：169 / 633 行，且账本每行都没有 provenance（`measured 0 of 1714 rows`），per-request 基线为 0 行；
2. **切片不覆盖同一 workload**：账本没有 route、账号、成员、session、任务族字段（本轮枚举了全部 17 个 key，见 §4.4），无法确认两天在 00h/01h 做的是同一类事；
3. **钟点不是唯一变量**：两天都是本机时区的人工使用，00h/01h 的差异既可能是负载，也可能是别的。

**判定：分支 C 适用。** 正确表述是"**历史账本不可比；对齐后残差 9.6pp 未决**"，而不是"解释了 7pp"。

### 4.3 K1 对历史账本的贡献无法界定（C-7）

上一轮 C 说"假象最多解释约 14pp"。本轮修正为：**假象的方向取决于 `E/R`**，而这个比值**不在账本里**。

机制（本轮实测）：估算 `E` **只覆盖 messages，不含工具面**。于是

- `E > R`（工具面小）→ 旧规则 `miss = E − R` **高估** miss（§3.3）；
- `E < R`（工具面大）→ `miss = max(E−R, 0)` **地板到 0**，报成 **100% 命中**。

折叠后的账本只保留 `prompt/hit/miss`，**无法反推 `E/R`**。因此"假象上限"既不是 14pp 也不是 17pp，而是**不可界定的**。

可检出的指纹（**弱证据**，只说明"存在"，不说明"量级"）：

| 指纹 | roojin route | 另一 route（`deepseek-v4-flash/…v4.1-flash[1m]`） |
|---|---:|---:|
| `hit == prompt`（miss 被地板到 0 的形状） | **3** 行 / 5,171 | **380** 行 / 594（09-22，占 64%） |
| 审计的双计启发式 `0 < prompt−2·hit < 500` | 15 行 | 3 行 |

**本 route 上可检出的旧规则指纹很小（3 行 + 15 行 / 3,677 行）**，但按 §4.3 的机制，**大部分假象方向（高估）在折叠后不留指纹**，所以这不是"本 route 没有假象"的证据。另一 route 上 64% 的行是 `hit == prompt`，与"工具面大 → 地板到 0"完全相容——**但 route、账号、日期都不同，只能作为机制旁证，不能合并**。

### 4.4 账本的能力边界（枚举全部字段）

| 账本的 17 个 key | 值 |
|---|---|
| `ts, model, source, prompt, completion, reasoning, cache_hit, cache_miss, total, requests, usage_source, cost_*, display_*, incomplete_reason` | — |
| `model` 的取值 | **1** 个（`deepseek-v4-flash-roojin/deepseek/deepseek-v4.1-flash[1m]`） |
| `source` | `cli`（全部） |
| `usage_source` | `executor` 3,681 / `compaction` 6 |
| `requests` | 1 3,539 / 2 124 / 3 2 |
| `incomplete_reason` | `no_price` 3,666 / `usage_unknown` 21 |

**账本没有**：route、账号、成员、session、turn、任务族、前缀诊断、原始 wire 字段。**因此任何"历史分层"都只能是钟点 + 尺寸桶 + 计数来源，且都不可审计。**

**附带复核（A 的块对齐归纳）**：3,665 行（两天全部 `hit>0` 行）**100% 满足** `hit % 64 == 0`、`hit % 128 == 0`、`miss ≡ prompt (mod 128)`，全部 `hit` 的 gcd **= 128**，且 `prompt == hit + miss` 3,665/3,665。**128 块对齐在历史账本上独立成立**——这加强了 B 的成员路径复现，但它同样**不**能说明那些 miss 是"真实的"还是"假象的"（§4.3）。

---

## 5. B 的接口请求（B-C1）答复（交付物 3）

### 5.1 `session_id`（P0）：**已落地**

- `team.MemberCacheRequest.SessionID`（`internal/team/cacherequest.go`，`json:"session_id,omitempty"`），映射自 `event.Event.SessionID`（`internal/cli/team_usage_publish.go` 的 `memberCacheRequest`）。
- 字段注释带 B 要求的三条语义：**空值 = 未观测（非"不属于任何会话"）**；**`session_request_seq` 是 writer 进程级、backend 重建会回退，不是 session 内单调**；**`hit == 0` 读作"未报告 cache read"，不是冷启动**（后一条落在 `CacheMissTokens` 字段上）。
- 报表新增覆盖率维度 `coverage.session_present / session_absent`，文本渲染为 `session=N/M (present/scoped)`，JSON 同步发布。
- **旧行缺键**（`omitempty`），账本路径的派生记录 `session_present=0`（历史不可回溯，如实披露）。
- 测试：`TestObservedRequestMapsEveryUsageField` 断言 `SessionID/TurnID/SessionSequence` 逐字段来自事件；`TestCacheReportCoverageDescribesTheScopedPopulation` 断言"一个在场 + 一个缺席"。

### 5.2 `concurrency_level`（P0）：**拒落**，理由

B 提议的最近真值是 `teamTaskService.busyMembers()`（`internal/cli/team_task_service.go`）。**C 判定不落字段**：

1. `busyMembers()` 走 `board.LoadLiveTasks(ctx)`，是**一次 store 读**。观测路径的契约是"provider 请求永不等待遥测"（`memberUsagePublisher.observe` 的注释与队列设计）；把它挂在每次 usage 事件上会违反该契约。
2. 退一步"记登记值"（驱动方声明的并发档）会**把假设写成测量**——正是方案 §2 明令禁止的"把聚合/unknown 当单请求"的同类错误。
3. B 已经在做正确的事：pilot 报告写的是"`concurrency_level = 1`，**登记值，非实测**"。**保持这样**：并发档由 B 在臂登记里冻结，记录侧不出现该字段，分层时以**臂**为单位而不是以**行**为单位。

### 5.3 `attempt`（P1）：**拒落**，理由

- 事件上没有逐 attempt 身份：`event.AttemptID` 是 streaming 显示用的，`emitTurnUsage` 不写它。
- 已有的诚实信号是 `RequestCount`（>1 = 聚合）与 `RequestCountSource`（`observed`/`defaulted`）。**新造一个 attempt 号会发明 provenance**，而 A 的 v1 契约正是靠"不发明"才把账本降级成诚实的。
- 若 B 的正式实验需要"重试关系"这一维度，用**现有的** `RequestCount > 1` + `RequestCountSource` 表达，并在报告里标 `insufficient_provenance`。

### 5.4 `maintenance_phase`（P1）/ `task_family`（P2）：**按 B 的方案**

- `maintenance_phase`：零代价派生，`team.CacheRequestStages` 已把 `PrefixChangeReasons` 映射为 `post_rewrite`。**C 不加字段。**
- `task_family`：代码里不存在该概念，由驱动方冻结登记，**不进记录**。同意 B。

### 5.5 oracle 在场性（B §5 末条）

B 问成员记录侧是否需要补 `billing_usage` 在场字段。**C 的答复：不补。** 理由：

- `billing_usage` 是**网关私有扩展**，不是协议字段；把它写进成员记录等于把私有扩展提升为观测契约（方案 §2 的禁止项）。
- 本轮 C 已在 **journal 侧**补了 `usage_oracle_present/agrees/prompt/hit/miss`，用于**复核**而不是用于**统计**。成员侧的对应问题（"这条记录有没有旁证"）在成员路径上**没有独立 oracle 可用**（成员记录本来就只有归一化值）。
- 因此成员侧该维度永久标记 `insufficient_provenance`，**不填、不推断**。

---

## 6. 复现清单

### 6.1 离线（零成本）

```bash
go build ./...
go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...
go test ./internal/cachelab/ ./internal/team/ ./internal/stats/ ./internal/provider/anthropic/ -count=1
go test ./internal/cli/ -run 'Cache|Usage|Team' -count=1
go run ./tools/repolint
bash scripts/cache-guard.sh

# K3 的重放（B 的 11 份 journal，零成本）
PART_C_JOURNAL_DIR=/tmp/cachelab-runs go test ./internal/cachelab/ \
  -run TestReplayJournalsReclassifyTheSameRecords -v -count=1

# 历史账本
go run ./cmd/reasonix team cache-audit --model deepseek-v4-flash-roojin \
  --from 2026-09-23 --to 2026-09-23 --first 1714 --json --out arm-0923.json
go run ./cmd/reasonix team cache-audit --model deepseek-v4-flash-roojin \
  --from 2026-09-24 --to 2026-09-24 --first 1714 --json --out arm-0924.json
```

### 6.2 真实 Provider（需凭证，`-tags live`）

```bash
REASONIX_LIVE_CACHE_BASE_URL=... REASONIX_LIVE_CACHE_API_KEY=... \
  go test -tags live ./internal/cachelab/ -run TestLiveUsageFoldVerification -v -count=1
```

### 6.3 归档

探针产物（journal、原始 SSE 字节、脚本、审计 JSON）已从 `/tmp` 转移到
`~/reasonix-partc-archive/2026-09-24/`（51 个文件，412KB），并按方案 §9 复核：**无凭据、无 prompt/工具正文**（`sk-` / `authorization` / 夹具文本三类扫描均为 0 命中）。

---

## 7. 合流门禁（交付物 4 的"可合流"部分）

| 检查 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...` | **干净** |
| `go test ./internal/cachelab/ ./internal/team/ ./internal/stats/ ./internal/provider/anthropic/` | **全绿** |
| `go test ./internal/cli/ -run 'Cache\|Usage\|Team' -count=1` | **全绿** |
| `go test -tags live ./internal/cachelab/ -run TestLiveUsageFoldVerification` | **通过**（4 warm，0 违反） |
| `bash scripts/cache-guard.sh` | **通过**（`TestReleaseCacheHitGuard` 10/10 case `status=pass`；`TestBootStableExtensionCacheGuard` 通过） |
| `go run ./tools/repolint` | **clean (1146 baselined findings)**；本会话新增/修改的文件**零新增违规**（逐项把 essay 块压回限额） |
| 敏感内容 | 归档扫描 0 命中；`TestObservedRequestsCarryNoContent` 保持绿 |
| 请求字节 | 本会话**未触及**任何 provider-visible 请求构造（diff 只在 `internal/cachelab`、`internal/team/cachereport*`、`internal/team/cacherequest.go`、`internal/cli/team_usage_publish.go`、`internal/cli/team_cache_report.go`） |

**写集与另两个 Agent 的边界**：本会话的改动文件与 A（`internal/provider/anthropic/**`）、B（实验脚本 + `internal/cachelab` 的部分）有**交集但不同文件**——`internal/cachelab/usage.go` 是新增文件（B 的 `recorder.go` 只是删除了迁出的代码块），`internal/team/cacherequest.go` / `cachereport_stats.go` / `team_cache_report.go` 是方案 §3 划给 C 的写集。A 的 `stream_usage.go` / `anthropic.go` / `messages_usage.go` 本会话**未改动**（其 diff 属 A）。

---

## 8. 正式 go/no-go、灰度与回滚

### 8.1 go/no-go

| 项 | 判定 | 依据 |
|---|---|---|
| **K1（`mergeUsage` 跨事件字段混用）** | **通过 —— 仅限本 route** | 适配层 == 协议读数 6/6；oracle 一致 22/22；无 oracle 的 4 条判据 0 违反（§3） |
| K1 在其他 Provider（native Anthropic / LongCat / OpenAI 直连 / `responses`） | **未决** | 本机无这些端点的凭据，只能引用 A 的表驱动测试；**不得**用单网关结果概括 |
| K1 盲点（省略 split 的全命中） | **未决** | 未观察到；下游按"未报告 cache read"措辞 |
| **K3（`cachelab` 解析）** | **通过** | 11 形状表 + 4 定向测试；B 的 11 份 journal 重放结果逐项不变（§2.4） |
| 历史账本的 per-request 基线 | **不可用**（维持 A 的 v1 判定） | `measured 0 of 1714` |
| A 的 84.5%/67.7% 等样本量对比 | **撤回为不可比** | 两天窗口不重叠；钟点对齐后为 86.14% vs 76.51%（§4.1） |
| 历史残差（对齐后 9.6pp） | **未决 / 不可归因** | 切片不合格、账本无分层字段（§4.2） |
| **缓存行为优化候选** | **无准入** | 无候选在真实 Team 层内可复现；K4–K9 状态不变（撤回/关闭） |

### 8.2 灰度

**K1 没有"限单一测试 member"的灰度语义**（与上一轮 C 同判）：它不改 provider-visible 字节、不改成员隔离、不改上下文策略，影响面是**上报口径**。按 member 灰度只会制造"同一团队两个成员口径不同"的更糟状态。**全量生效即全量正确。**

K3 的灰度面更窄：它只被 `-tags live` 的驱动导入，**不进生产二进制**。

### 8.3 回滚

| 变更 | 回滚 | 影响 |
|---|---|---|
| K1 | 还原 `mergeUsage` 的四行（`inTok/outTok/cacheCreate/cacheRead` 逐字段 max） | 无状态、无迁移；历史账本**不回填**；命中率数字回到假象值 |
| K3 | 还原 `internal/cachelab/usage.go` 与 `ReportedUsage`/`Sample` 的 oracle 字段 | 仅影响夹具；已有 journal 仍可读（新字段 `omitempty`） |
| `session_id` | 删 `MemberCacheRequest.SessionID` 与映射行、覆盖率字段 | 旧行本就没有该键；无迁移 |
| 覆盖率 `session_*` | 删两个计数器与渲染 | 报告 JSON 少两个键 |

### 8.4 版本切换公告（必须发，且必须与 A 的 v1 升版同批）

**K1 生效时所有下游消费者会看到数字阶跃**：账本、`team cache-report`、`team cache-audit`、UI 命中率、`.usage.json` 历史。这不是灰度问题，是**版本问题**。公告必须同时说明：

1. **阶跃的方向与量级**：本 route 上 warm 从 ~85% 量级跳到 ~99.9% 量级（裸请求 4/4、成员路径 6/6 与网关读数一致）；
2. **历史不回填**：`stats-ledger` 的旧行保持原值，因此**新旧数字不可相减**；
3. **`session_id` 是新增键**，旧行缺键，覆盖率会显示 `session=0/N`；
4. **本 route 的 K1 通过不覆盖其他 route**，其他 route 标记未决；
5. **历史 84.5%/67.7% 的对比撤回**，替换为"两天窗口不重叠、按钟点对齐 86.14% vs 76.51%、残差未决"。

---

## 9. 结果分支判定

| 分支 | 是否适用 | 说明 |
|---|---|---|
| **A：K1 通过，Team warm 接近 Provider 受控结果** | ✅ **适用（本 route）** | B 的 pilot warm 99.79%–99.93%，C 的独立探针同量级；**不立即改工具 schema 或压缩策略** |
| **B：K1 通过但真实 Team 仍低命中** | ❌ 不适用 | 本 route 的成员 warm 命中率是 99.8% 量级；低命中只出现在**历史账本**（且那批数据不可比） |
| **C：K1 后命中率提高，但历史跌幅仍存在** | ✅ **适用** | 历史跌幅的**对比方式本身不成立**（§4.1）；剩余残差**未决**；不得把"命中率提高"解释为真实缓存改善 |
| **D：Provider 语义无法统一或 oracle 不足** | ⚠️ **部分适用** | oracle 在场但不完整；K1 在本 route 有**不依赖 oracle** 的验证路径，故未降级；其他 route 降级为**有限支持/未决** |

**下一项优化是否准入**：**否**。按方案 §6.2 的"优化准入"，任何候选必须"在真实 Team 层内可复现、具备最小效应、任务质量不回归、token 成本不恶化、请求形状影响可解释"。本轮**没有**候选满足；唯一有证据的改动（K1）是**测量修复**，不改 `miss_tokens_per_request`。

---

## 10. 需要补采的数据（更新 A §9）

1. **带 provenance 的新账本**：只有用**含 K1 的构建**重跑一次历史时段，才可能把"历史不可比"变成"可比的旧/新前瞻基线"。这是唯一出路，且**不能回填**。
2. **其他 route 的 K1 验证**：native Anthropic / LongCat / 官方 DeepSeek 需要端点与凭据；在此之前，§8.1 的"未决"不得被读成"通过"。
3. **1M 桶与压缩边界**：B 的 pilot 最大 ctx 121K、10 轮未触发 fold，两者都还没有样本。
4. **`concurrency_level` 的**正确落点不是记录字段，而是**臂登记**；若将来确需逐请求并发，需要一条**不取 store 锁**的运行时信号（本会话未找到，属结构性缺口）。
5. **oracle 在场性的稳定性**：本轮同一端点、同一账号、连续 12 次请求全部带 oracle；但账本路径上没有任何手段证明历史行是否带过。若要回答"历史 K1 假象占多少"，需要**保留一轮原始 usage**（当前账本不保留，A §9 已列为结构性缺口）。

---

## 11. 本文未做

- 未修改任何 provider-visible 请求字节、缓存策略、上下文裁剪/折叠、成员隔离。
- 未修改 A 的 `internal/provider/anthropic/stream_usage.go`、`anthropic.go`、`messages_usage.go`。
- 未修改 B 的实验脚本、任务组成或 `internal/cli/live_team_cache_*_test.go`。
- 未把 B 的实验 journal 并入成员记录或账本口径；未把裸请求结论外推为 Team member 基线。
- 未为得到更高命中率而排除低命中样本、修改分母、把 unknown 归零。
- 未提交、未推送、未开 PR。
