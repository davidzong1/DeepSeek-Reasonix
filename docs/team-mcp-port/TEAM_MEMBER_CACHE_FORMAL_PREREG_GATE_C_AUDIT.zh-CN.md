# 正式预注册门禁：Agent C 独立审计与逐桶验收

> 角色：Agent C（预注册审计、独立复算、门禁验收）。日期：2026-09-25。
> 依据：`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_3AGENT_PLAN.zh-CN.md` §7（C 工作包）、§8（硬防线）、§9（最终合流）。
> 前置：`..._FOLLOWUP_EXECUTION_PLAN`（§6 上一轮 C）、`..._FOLLOWUP_C_AUDIT`、`..._MERGE_RECORD`、`..._FOLLOWUP_CLOSEOUT`、A `..._GATE_A_PREFLIGHT`、B `..._GATE_B_GATE0_RECORD` + `..._B_PREREGISTRATION_V2`。
> **本文不发任何真实请求，不修改 A/B 的写集或已冻结规则，不输出凭据、认证头或其派生值。**
> 独立复算工具已进仓库：`internal/cachelab/strata_recompute_test.go`（`PART_C_STRATA_DIR` 门控，CI 不读实验数据）。

> **状态说明（最终裁定）**：本文 §1–§11 及其摘要表保留的是签核前快照，其中 Gate 1 = BLOCKED、D-1 待裁定等表述均属历史状态。§12 是后续复审；负责人随后已在 B Gate 1 签核包 §6 完成签核，V2 SHA-256 为 `797fd5bae6446b8d2b339e6d5e0d0d6815a593766f05282ee75e05e51ce34ed0`，**当前 Gate 1 = PASS**。正式采样仍未由本文授权，后续执行以冻结 V2 和 Gate 2 条件为准。

---

## 0. 结论摘要

| # | 结论 | 强度 | 依据 |
|---|---|---|---|
| **GC-1** | **冻结条件自相矛盾，用「三点标定法」独立证实。** 我没有做穷举搜索，而是把两份归档当作**两个方程**解出 route bucket 的未知输入（endpoint 存储拼写 + proxy mode = `off`），再用**已标定**的输入去评估**第三个**池条目——这是**预测**，不是查表。结果：`pilot-gw`→`bfcb0811b1c8`（与 pilot 归档 30/30 相符）、`strata-gw`→`f119dfdb4214`（与六臂 204/204 相符）、`deepseek-v4-flash-roojin`→**`f3634ff267d6` ≠ `bfcb0811b1c8`**。**A-2 / B G0-1 成立，且证据形式比穷举更强。** | **证据**（§2） | 冻结条件按原文不可执行 |
| **GC-2** | **两份归档的 route bucket 都指向「不存在于注册表的标签」。** 全 9 月账本中 `pilot-gw` 与 `strata-gw` 各出现 **0 次**；账本里真实存在的账号前缀只有 `deepseek-v4-flash-roojin`(5188) / `deepseek-v4-flash`(1261) / `wan-gpt-5.6`(1046) / 空(161)。**因此 pilot 与六臂的记录都无法证明「打在同一个账号上」**——它们只证明了「打在同一个自造标签上」。 | **证据**（§2.3） | 支持 A/B 的降级判定 |
| **GC-3** | **V2 §1 身份块算术自洽，且我独立复核了注册表一侧。** `agent_users.json` 中 `deepseek-v4-flash-roojin` 的 `Provider=deepseek` / `BaseURL=https://aiapi.lejurobot.com`（**无** `/v1`）/ `Model=deepseek/deepseek-v4.1-flash[1m]` 与 V2 逐项相同；由这些值现算得 `anthropic/f3634ff267d6`，与 V2 写的一致。wire model 与 model_ref 的推导（剥 `[1m]` / `池条目id + "/" + 池内拼写`）也与代码一致。 | **证据**（§3.1） | D-1 选项① 可执行 |
| **GC-4** | **V2 的预算算术全部复核通过**：臂上限合计 **74M ≤ 80M** ✓；按 V1 实测外推的实际消耗 **58.49M ≈ 58.5M** ✓；S4 由 8 轮补到 12 轮的缩放 **28.53M ≈ 28.5M** ✓。V2 §9 的更正记录（82M→74M）**成立**。 | **证据**（§3.2） | 预算门禁可机械执行 |
| **GC-5** | **V2 §5.2 的 fold 机制归因不正确（结论不变）。** V2 说首轮不折叠是因为 `planCompaction` 要求 `minCompactMessages = 2`。**但 `planFoldRegion` 在 `!ok` 时会显式回退到 `planCompaction(msgs, 1, force)`**（`compact_projection.go:682`），该约束因此**被绕过**。真正的绑定约束是随后的**活动轮钳制**（`compact_projection.go:686-691`：首轮全部可见消息都在活动轮内 → `start` 被钳回 `head` → `start > head` 为假）。**结论方向（fold 不可达）不变**，但**引用错了约束**。 | **代码阅读**（§3.3） | V2 §5.2 需改写理由 |
| **GC-6** | **B 报告 §4.2 的「逐轮单调 +23」不成立（3/18 成员为 +24）。** 独立复算 18 个成员的逐轮增量：`S4-768k-m1`/`-m2` 为 **+24**，`S3-c2-m3` 为 **+24**，其余 15 个为 +23。**单调性（无回落）成立**，因此「未触发 fold」的结论不受影响；但「+23」作为**定值**是错的。 | **证据**（§5.4） | B 需更正措辞 |
| **GC-7** | **B 报告 §3.2 的并发间隔数字不可按任何陈述规则复现（现象本身成立且被我独立证实）。** B 引用的 S1a「1.47–2.08s」**不是任何字段的范围**（记录内 `seconds_since_prev_request` 实为 1.44–2.84；跨记录实测 1.44–2.84）。S3-c2/S3-c3 引用的 0.07–0.28 / 0.09–0.93 是**人工挑出的低间隔簇**，非计算值。**但用一条明确规则（同臂内相邻 `observed_at` 差）我复现并强化了现象**：S1a **0** 个 <1s 间隔、S3-c2 **13** 个、S3-c3 **30** 个；挂钟 67.6→50.0→23.0s 单调。**结论方向不变，引用值不可复现。** | **证据**（§5.4） | B 需给出计算规则 |
| **GC-8** | **六臂 banner 的「冻结测量层摘要」字段全部为空。** 六个 banner 都写 `frozen_stream_usage_sha256: unreadable:stream_usage.go`——因为驱动用**相对路径**读文件，而 `go test` 的 CWD 是包目录。B 报告 §1 写「sha256 写入每臂 banner」**与归档不符**。**补救证据存在**：banner 记的 build `71d2e3695fdd` 的 blob 与 HEAD **逐字节相同**（`4c834294…`），md5 亦与 M0 冻结值相同，故测量层可追溯——但**不是通过 banner 字段**。 | **证据**（§4.4、§5.4） | B 需更正 §1 或补记 |
| **GC-9** | **六臂归档的纳入/排除集与指标被独立重建并逐项吻合。** 我用**自己写的分类器**（不调用生产 `cacheExclusionReasons`）重建：六臂排除项**全 0**；warm 33/33/33/21、hit 388,608 / 6,359,808 / 19,552,512 / 16,639,488 / 388,608 / 388,608；与报告 JSON 的 `stages[warm_candidate]` **逐字节相同**；六臂 warm hit 之和 **43,717,632** 与 closeout §2.1 相同。 | **证据**（§5.1–5.2） | 数字可独立复现 |
| **GC-10** | **不变量与身份唯一性独立复核通过**：`hit % 128 == 0` 与 `miss ≡ prompt (mod 128)` **186/186 成立、0 违反**；`request_id`/`turn_id` **无跨臂复用**；`session_id`/`member_id` 无跨臂复用（各 18 个，每臂 3 个）；六臂**各自恰好落在其注册桶**（36/36、36/36、36/36、24/24、36/36、36/36）；每成员记录数**等于注册轮数**。 | **证据**（§5.3） | 数据面完整 |
| **GC-11** | **`strataPreflight` 尚未接线，V2 §6 是规格而非现状。** 现有六臂驱动 `live_team_cache_strata_test.go` **不调用** preflight；计划 §3 列出的 `live_team_cache_strata_formal_test.go` **尚不存在**。这正是漂移得以通过的原因，也是 V2 §6 要修的东西——但 §6 的表述读起来像已实现。 | **证据**（§4.3） | 措辞需区分规格/现状 |
| **GC-12** | **A 报告的冻结摘要表与交付文件不一致。** A 记 `team_preflight_gate.go` md5 `4bd6a97e…`、237 行；交付文件实为 md5 `3af53d15…`、**187 行**（A 在写报告后继续编辑）。**V2 §9 打印的自有 SHA-256 也不等于文件实际摘要**（V2 记 `6eb8433f…`，实际 `170a3219…`）。**两者都不影响判定**，但 Gate 1 第 4 项要求「记录签核时摘要」，**必须现算，不得抄写**——这正是 GC-1 教训的同一类。 | **证据**（§1.3） | 签核时必须重算 |
| **GC-13** | **逐桶门禁：无一桶可为 PASS。** 正式采样未执行（Gate 1 = BLOCKED），因此四桶**都没有正式样本**；V1 六臂的 204 条是**探索性**数据，且 `768k_1m` 只有 21 warm（报告 JSON `sample_gate_reached: false`），**不足 30**。按 §7.3：三桶 **NOT EXECUTED**、`768k_1m` **NOT EXECUTED（且 V1 探索性样本不足门槛）**、`gte_1m` **BLOCKED/NOT EXECUTABLE（结构性）**、fold 边界 **NOT EXECUTABLE（无样本）**。 | **判定**（§7） | 门禁保持 BLOCKED |
| **GC-14** | **隐私扫描 0 命中。** 六臂归档 18 个文件对 `sk-*` / `api_key` / `bearer` / `authorization` / `ANTHROPIC_AUTH_TOKEN` / `PRIVATE KEY` 扫描 **0 命中**；对 brief 正文与任务标记词（`Reference brief` / `brief line NNNNNN` / `Turn N: reply with exactly` / `ACK N`）扫描 **0 命中**。**我拒绝独立复核 B 的 G0-5**（凭据归属需读取密钥材料），理由见 §6。 | **证据**（§6） | 隐私边界成立 |
| **GC-15** | **V2 未签核，我的签署状态为「条件 PASS / 身份块 BLOCKED」。** 除 §1 身份块（D-1 待负责人裁定）外，V2 的样本量、预算、停止、排除、硬防线**逐条 PASS**；身份块的**算术**已由我独立验证自洽（GC-3），但**选项本身**属负责人权限，C 不代签。 | **判定**（§3.5） | Gate 1 保持 BLOCKED |

**一句话**：冻结条件的矛盾用「三点标定法」独立证实且证据形式强于穷举；V2 除身份块待裁定外逐条通过，但它的 fold 机制归因、B 报告的三处引用（+23、并发间隔、banner 摘要字段）都需要更正——**方向全部不变，可复现性不成立**。六臂数据面（纳入集、指标、不变量、身份唯一性）独立复现**逐项吻合**，问题始终是**它属于哪个 route**。

---

## 1. M0 冻结与本轮输入摘要

### 1.1 冻结

| 项 | 值 |
|---|---|
| 基线 commit | `23c1e2d38fc89b527406d9572ff10e7b85728232`（`Team-agent-merge-mainv2`） |
| 工作树（本轮开始） | 已跟踪文件**零改动**；未跟踪 = A 的 2 个 + B 的 3 个文档 + C 的 1 个测试 |
| 折叠规则 | `internal/provider/anthropic/stream_usage.go` md5 **`71b7d3b79a4c6dfc74d1357b310190ff`** —— 与合流纪要 M0 **逐字节相同** |
| 折叠规则跨构建 | `71d2e3695:…stream_usage.go` 与 `23c1e2d38:…stream_usage.go` **同一 blob** `4c834294bb7289d7ea1f44d5f31fe8dc9cab2980` —— **六臂采样的测量层与 HEAD 相同** |
| 夹具解析 | `internal/cachelab/usage.go` md5 `01a5cb37e30cf272a095fce1a09837f1`（上一轮已证为冻结快照的语义等价物） |
| 账本完整性 | `2026-09-23/24.jsonl` md5 `98961189…` / `61eaaa3a…` —— 与 M0 **逐字节相同**，全程只读 |
| 本轮新增（C） | `internal/cachelab/strata_recompute_test.go` md5 `65a974c94afe5b8d75cec726f77ea477` |

### 1.2 独立复算的输入摘要

```text
~/reasonix-partb-archive/2026-09-24/strata-<arm>-records-<runid>.jsonl   （6 份，204 条）
  S1a-small  c17e35752a570fd6ab104f5e30d64aaf
  S1b-mid    7e531cebccd4ef64270ad14982203274
  S1c-large  36b64c84420aec0bb4bd99a6efce9495
  S4-768k    8afb60b725ffa4505aa0f6a69bed8da5
  S3-c2      ac8e1a29f7288bd4256176bf804bba66
  S3-c3      634d6b74b2879b3de137a67a258dad42
```

**数据源严格隔离**：本节及 §5 **只读 B 的六臂归档**，**不**读成员记录、**不**读统计账本（§2.3 的账本查询只为验证「标签不存在」，不参与任何指标）、**不**与任何其他数据集合并。

### 1.3 交付文件的当前摘要（Gate 1 第 4 项必须现算，不得抄写）

| 文件 | 当前 md5 | 报告/预注册中记的值 |
|---|---|---|
| `internal/cli/team_preflight_gate.go`（A） | **`3af53d1540ad0015c6b57eeca41e62d8`**（187 行） | A 报告记 `4bd6a97e…`、237 行 → **不一致** |
| `internal/cli/team_preflight_gate_test.go`（A） | 交付 592 行 / 15 个顶层测试 | A 报告记 15 个测试 / 592 行 → **一致** |
| `internal/cli/live_team_cache_strata_test.go`（B） | `40494e01663cf4a9ab188cbbd5df9791` | 未记 |
| `..._B_PREREGISTRATION_V2.zh-CN.md`（B） | **SHA-256 `170a32196cbb23101da7548b6a2e7e91b30dfc0abfcd4baa610eae95e66e7751`** | V2 §9 记 `6eb8433f…` → **不一致** |

**判定（GC-12）**：两份「自带摘要」都与交付物不符。A 的差异源于报告写完后继续编辑；V2 的差异源于其 §9 已声明「任何一项落定后本文都会变更」。**两者都不是数据问题，但都证明「摘要不可抄写」**——与 GC-1 是同一类教训。**Gate 1 第 4 项签核时必须对当时的文件现算。**

---

## 2. 冻结条件的自洽性：三点标定法（GC-1、GC-2）

### 2.1 方法：解方程，而不是查表

A 做了 1,152 组合穷举，B 做了 23,520 组合穷举，两者都得出「`bfcb0811b1c8` 只有 `pilot-gw` 一解」。**穷举能证明「在搜索空间内无解」，但搜索空间本身是猜的**（endpoint 有多少种拼写？proxy mode 有几种取值？）。

我改用**标定法**：route bucket 的输入是五元组 `(kind, endpoint, name, proxy.Mode, proxy.Type)`。其中 `kind=anthropic` 由 `ResolveAgentUserProvider` 对 `deepseek + [1m]` 的判定唯一确定，`name` 由归档记录唯一确定（pilot 用 `pilot-gw`、六臂用 `strata-gw`）。**剩下 endpoint 拼写与 proxy mode/type 是未知的。**

两份归档给出**两个方程**：

```
f(anthropic, EP, "pilot-gw",  M, T) == "anthropic/bfcb0811b1c8"   （pilot 归档 30/30）
f(anthropic, EP, "strata-gw", M, T) == "anthropic/f119dfdb4214"   （六臂 204/204）
```

解出 `(EP, M, T) = ("https://aiapi.lejurobot.com", "off", "")` —— **同时满足两个方程**。

**解的正确性有代码侧的独立佐证**：`memberProxySpec`（`team_backend_build.go:269`）对「未启用或地址为空」的 team proxy 返回 `netclient.ProxySpec{Mode: netclient.ModeOff}`（`:271`），即 `Mode == "off"`、`Type == ""`；`ResolveAgentUserProvider` 对 `deepseek + [1m] + 非空 BaseURL` 返回 `"anthropic"`。**我解出的值不是拟合出来的，是代码写死的。**

### 2.2 用已标定的输入做预测

有了标定，就可以对**第三个**池条目做**预测**（而非在搜索空间里找）：

| 池条目 | 标定输入下现算的 bucket | 与冻结值的关系 |
|---|---|---|
| `pilot-gw`（自造） | `anthropic/bfcb0811b1c8` | = V1 冻结的 bucket |
| `strata-gw`（自造） | `anthropic/f119dfdb4214` | = 六臂实测值 |
| **`deepseek-v4-flash-roojin`**（**V1 冻结的账号**） | **`anthropic/f3634ff267d6`** | **≠ `bfcb0811b1c8`** |

**判定（GC-1）：V1 的两个冻结字段不可能同时成立。** 证据形式比穷举强：穷举证明「在猜的搜索空间内无解」，标定证明「在**代码确定**的输入下，第三个条目解析到第三个值」——**这是预测，且被两次独立测量（两份归档）校验过**。

### 2.3 为什么这件事比「route 漂移」更严重（GC-2）

route bucket **有意不哈希凭据**（`team_backend_build.go:141`：「密钥的指纹仍派生自密钥」）。这个设计是对的，但有一个**必然后果**：bucket 只能证明「两次运行打在同一个**标签**上」，**不能**证明「打在同一个**账号**上」。

而 V1 预注册要固定的恰恰是**账号**。独立统计全 9 月账本：

| 账本中的 model 前缀 | 行数 |
|---|---:|
| `deepseek-v4-flash-roojin` | 5,188 |
| `deepseek-v4-flash` | 1,261 |
| `wan-gpt-5.6` | 1,046 |
| （空） | 161 |

| 标签 | 在全部 9 月账本中的出现次数 |
|---|---:|
| `pilot-gw` | **0** |
| `strata-gw` | **0** |

**判定**：`pilot-gw` 与 `strata-gw` 是驱动**手填的字符串**，从不参与计费路由，也从不进入账本。因此 pilot 与六臂的记录**无法证明它们打在同一账号上**——**A-2 / B G0-2/G0-3/G0-5 成立**。

> **我未独立复核 B 的 G0-5**（「环境凭据等于 `deepseek-v4-flash-xie` 的存储 key」）。该断言需要把密钥读入内存做比对，**超出本轮隐私边界**（§6）。B 已记录该发现，我把它标为**「B 单方证据，C 未复核」**，不作为我的独立结论。

---

## 3. V2 预注册的独立逐条审核（§7.1）

### 3.1 身份块（GC-3）

| V2 §1 字段 | V2 的值 | 我的独立核验 | 判定 |
|---|---|---|---|
| 池条目 id | `deepseek-v4-flash-roojin` | `agent_users.json` 中存在该 `UserID` | ✅ |
| provider（池内声明） | `deepseek` | 注册表该条目 `Provider = "deepseek"` | ✅ |
| provider kind | `anthropic` | `ResolveAgentUserProvider`：deepseek + `[1m]` + 非空 BaseURL → `anthropic` | ✅ |
| endpoint（存储拼写） | `https://aiapi.lejurobot.com`（**无** `/v1`） | 注册表该条目 `BaseURL = "https://aiapi.lejurobot.com"` | ✅ |
| wire model | `deepseek/deepseek-v4.1-flash` | 注册表 `Model = "deepseek/deepseek-v4.1-flash[1m]"`，剥 `[1m]` 后相同 | ✅ |
| model_ref | `deepseek-v4-flash-roojin/deepseek/deepseek-v4.1-flash[1m]` | `memberModelRef(name, u.Model)` = `UserID + "/" + Model` | ✅ |
| **route bucket** | **`anthropic/f3634ff267d6`** | 用 §2.1 的**标定输入**现算，逐字节相同 | ✅ |
| build | `23c1e2d38fc8` | `git rev-parse --short=12 HEAD` = `23c1e2d38fc8` | ✅ |
| 折叠规则 | md5 `71b7d3b79a4c6dfc74d1357b310190ff` | 实测相同 | ✅ |

**判定：身份块算术自洽，且注册表一侧已独立复核。** V2 明文要求「必须由 `resolveStrataIdentity` 现算，不得从 V1 抄写」——**这条要求是本次事故的直接解药**，我予以确认。

**唯一未决项是 D-1（选哪个账号）**，那是负责人的权限（§3.5）。

### 3.2 样本量、预算与停止规则（GC-4）

**预算算术逐项复核**：

| 检查 | V2 声称 | 我现算 | 判定 |
|---|---|---|---|
| 七臂上限合计 | 74M | 2+2+10+26+30+2+2 = **74M** | ✅ ≤ 80M |
| 按 V1 实测外推的实际消耗 | ≈58.5M | 0.43+0.43+6.94+21.33+28.5+0.43+0.43 = **58.49M** | ✅ |
| S4 由 8 轮补到 12 轮 | ≈28.5M | 19,018,148 × 12/8 = **28.53M** | ✅ |
| S0–S1 四臂 ≤ 40M | 29.13M | 0.43(S0 估算)+0.426654+6.940818+21.332754 = **29.13M** | ✅ |

**V2 §9 的更正记录成立**：原写 S1b=12M / S1c=30M / S4=32M（合计 **82M > 80M**），已下调为 10/26/30（合计 74M）。**这是自查发现的，且更正方向正确。**

> **一处措辞提示（非错误）**：V2 §3 把 0.43M 标为「V1 实测」并同时列给 S0-baseline 与 S1a-small。S0 是**新臂**，无 V1 实测；该值是以同条件的 S1a 实测值作代理。**结论不受影响**，但建议在 V2 中标注 S0 那一格为「外推」。

**样本量**：每臂 3 成员 × 12 轮 = 36 条（首轮 3 单列 + warm 33 ≥ 30）✓；独立单元 = **3 个 session**（明文声明不做跨成员总体推断）✓；`gte_1m` 记 BLOCKED（结构性）✓。

**停止规则**：7 条，全部可在驱动中机械执行；其中第 5 条「`strataPreflight` 任一字段不匹配」是**本次新增的硬防线**，正是缺失它的那一条导致了 V1 六臂的漂移 ✓。

**排除规则**：沿用 A v1 契约，**未新增、未放宽**；「低命中样本不因结果不利而被排除」明文保留 ✓。

### 3.3 fold 机制归因（GC-5）——**V2 需改写理由**

V2 §5.2 写：

> 首轮无标定 → 回退比值 0.25 → 估算 960,000 > 800,000，**已越过触发线**；但 `planCompaction` 要求可折叠区 ≥ `minCompactMessages = 2` 条消息，而首轮只有一条用户消息 → **no-op**。

**`minCompactMessages` 不是绑定约束。** 代码阅读（三处，逐行）：

```text
compact.go:316              planCompaction(msgs, min int, force bool)
compact.go:348-350          if start-head < min { return head, start, false }
compact_projection.go:680   head, start, ok = a.planCompaction(msgs, minCompactMessages, force)
compact_projection.go:681-683   if !ok { head, start, ok = a.planCompaction(msgs, 1, force) }   ← 显式回退到 min=1
compact_projection.go:687-693   if active := a.activeTurnStart(msgs); active >= head && active < start { ... start = active ... }
compact_projection.go:694   return head, start, start > head
```

**首轮的实际路径**：`msgs = [system, user(brief)]`，`head = pinnedPrefixLen = 1`。`planCompaction(min=2)` 因 `start-head = 1 < 2` 返回 false → **回退到 `min=1` 后返回 `ok=true, head=1, start=2`**。随后 `activeTurnStart` 命中 index 1（该轮唯一用户消息即活动轮）→ `start` 被钳回 `1` → `start > head` 为假 → **不折叠**。

**判定**：绑定约束是**活动轮钳制**（首轮的全部可见消息都在活动轮内，没有可折叠的历史），**不是** `minCompactMessages`——后者被一行显式回退绕过了。

**结论方向不变**：fold 在首轮不发生，且次轮起标定比值 0.2063 使估算 792,340 < 800,000，**确定性低于触发线**。V2 的**判定**（压缩阶段不可执行）成立；**理由**需改写。

> **证据等级声明**：本条是**代码阅读**结论（含精确行号），**不是**执行结论。我未运行 fold 路径（那需要构造一个真实 session 并驱动压缩，超出 C 的只读写集与预算）。建议 B 用一个纯离线单测固定它——那属于 B 的写集。

### 3.4 硬防线可执行性（§8 逐条）

| §8 | V2 的落实 | C 的核验 | 判定 |
|---|---|---|---|
| 1. 期望身份来自单一冻结源 | `strataIdentity` 单一结构 | 结构存在（A 的 `team_preflight_gate.go:22`，`strataIdentity` 结构体），7 字段由 `strataIdentityFields` 单点枚举 | ✅ 规格成立 |
| 2. 计费请求前比较可本地确定项 | 每臂首请求前调 `strataPreflightForPoolEntry` | **函数存在，但尚未被任何驱动调用**（GC-11） | ⚠️ **规格 ≠ 现状** |
| 3. 记录与 banner 保存实际身份 | banner 写 preflight 结果与实际身份 | **V1 六臂 banner 的测量层摘要为空**（GC-8）；实际身份（route/model_ref）**在记录里有** | ⚠️ 部分 |
| 4. 一臂多 route/model/account → 整臂隔离 | 每臂结束核唯一性 | V1 六臂每臂**恰好 1 个 route、1 个 model_ref** ✓（§5.3） | ✅ |
| 5. identity preflight 与正式样本分开编号 | probe 不计 warm 分母 | D-3 判定**不需要 probe**（bucket 离线可算，GC-1 已证）✓ | ✅ |
| 6. run/member/session id 不跨臂误复用 | 成员 id 前缀臂名 | V1 六臂 **0 跨臂复用** ✓（§5.3） | ✅ |

### 3.5 签署状态（GC-15）

| V2 项 | C 的签署 |
|---|---|
| §1 身份块（除 D-1） | **PASS**（算术自洽 + 注册表复核，§3.1） |
| §1 D-1（选哪个账号） | **BLOCKED —— 属负责人权限，C 不代签** |
| §2 主要问题与唯一变量 | **PASS** |
| §3 臂登记与预算 | **PASS**（算术复核通过，§3.2） |
| §4 样本量/停止/排除 | **PASS** |
| §5.1 `gte_1m` 结构性不可达 | **PASS**（桶下界 1,048,576 > `hardInputCeiling` 999,744，代码复核一致） |
| §5.2 fold 边界 | **PASS（判定）/ 需改写理由**（§3.3） |
| §6 硬防线 | **PASS（规格）**，但第 2、3 条须标注为「待实现」 |
| §7 已知限制 | **PASS**（L-10 的诚实声明尤其正确） |
| §9 签核清单第 3 项 | **条件 PASS**：D-1 落定且 §3.3/§6 措辞更正后，可签 PASS |

**结论：V2 在 D-1 落定并完成 §3.3/§6 两处措辞更正后，可由 C 签署 PASS。** 在此之前 **Gate 1 = BLOCKED**（§4 明文）。

---

## 4. A preflight 的独立审查（§7.1.3）

### 4.1 失败分支 fail closed：成立

我独立复跑 A 的 15 个顶层测试（27 个含子测试），**全部通过**。逐项核验 §7.1.3 要求：

| 要求 | 核验 |
|---|---|
| 失败分支 fail closed | ✅ 7 字段逐项比较，首个差异即返回；无 fallback、无换条目 |
| 字段同源 | ✅ 身份由 `resolveStrataIdentity` 走**成员 builder 自己的 resolver**，不是第二套白名单 |
| 输出脱敏 | ✅ endpoint 只输出 `scheme://host`；`TestStrataPreflightDiagnosticsCarryNoSecret` 用哨兵断言 |
| 错 route 的 records 不可能进入正式报告 | ⚠️ **逻辑上成立，但尚无驱动接线**（GC-11） |

### 4.2 一处我特别认可的设计

`strataExpectationIsFrozen` 拒绝**空字段**的期望：

> 空 ≠ 「无意见」：它是**从未加载的预注册**，接受它会让门禁**默认通过**——正是它存在的目的所要防的失败。

**这条判断是正确的，而且它正好防住了本次事故。** 若 V1 的驱动接了这道门禁、且期望块是从 V1 文档逐字读出的，那么 `pool entry = deepseek-v4-flash-roojin` 与 `route bucket = anthropic/bfcb0811b1c8` 会**在第一个请求前**冲突并拒绝。**这是本轮最有价值的单个设计决策。**

### 4.3 GC-11：规格与现状必须分开陈述

```text
grep -n "strataPreflight" internal/cli/live_team_cache_strata_test.go   → 无匹配
计划 §3 列出的 live_team_cache_strata_formal_test.go                     → 尚不存在
```

**现有六臂驱动不调用 preflight。** V2 §6 第 2 行写「每臂首请求前调用 `strataPreflightForPoolEntry`」，读起来像现状，实际是**对尚未存在的正式驱动的规格**。A 报告 §4.6 已正确声明「A 不替 B 接线——接线属于 B 的写集」，**B 应沿用同一措辞**。

### 4.4 GC-8：A 未覆盖的一处缺陷——banner 的冻结摘要为空

六个 strata banner 全部写：

```text
frozen_stream_usage_sha256: unreadable:stream_usage.go
```

原因：`strataFrozenDigest("internal/provider/anthropic/stream_usage.go")` 用**相对路径**，而 `go test` 把 CWD 设为包目录（`internal/cli/`），故 `os.ReadFile` 失败，函数按设计返回 `"unreadable:" + filepath.Base(path)`。

**该函数的行为是正确的**（「不可读就如实报告，而不是省略」），**用错了路径**。后果：**六臂 banner 没有固定住测量层摘要**——而 banner 存在的唯一理由就是这一点。

**补救证据存在**：banner 记的 `build_commit: 71d2e3695fdd`，其 `stream_usage.go` blob 与 HEAD 逐字节相同（`4c834294…`），md5 与 M0 冻结值相同。**测量层因此仍可追溯，但途径是 build id，不是 banner 字段。**

> 这是 **B 的写集**（驱动 + banner 格式），C 只报差异。

---

## 5. 六臂归档的独立复算（§7.2）——**探索性数据，明确标注**

> **正式性声明**：本节全部数字来自真实、可审计的 204 条记录，但 route/account 与预注册冻结值不一致（GC-1、GC-2），且 `strataPreflight` 从未运行（GC-11）。**因此这些数据只能作为探索性基线，不进入任何正式门禁分母。**

### 5.1 纳入/排除集重建（GC-9）

我**没有调用生产分类器** `cacheExclusionReasons`——审计一个分类器时调用它自己，只能证明它自洽。`strata_recompute_test.go` 按 A v1 契约**重新实现**了排除规则（`request_count_source != observed` / `count != 1` / `usage_unknown` / `usage_estimated` / `accounting_invalid` / `hit+miss<=0` / `observed_at` 不可解析），并从 `session_request_seq` **派生** warm（而非读记录自己的 `has_prev_request` 标志）。

**结果：六臂排除项全部为 0**，与报告 JSON 的 `coverage.excluded = 0` 及 `exclusions` 全 0 **逐项相同**。

### 5.2 指标（GC-9）

| 臂 | 记录 | 首轮 | warm | hit | miss | 加权率 | miss/请求 |
|---|---:|---:|---:|---:|---:|---:|---:|
| S1a-small | 36 | 3 | 33 | 388,608 | 2,871 | 99.2666% | 87.00 |
| S1b-mid | 36 | 3 | 33 | 6,359,808 | 2,988 | 99.9530% | 90.55 |
| S1c-large | 36 | 3 | 33 | 19,552,512 | 2,892 | 99.9852% | 87.64 |
| S4-768k | 24 | 3 | 21 | 16,639,488 | 1,640 | 99.9901% | 78.10 |
| S3-c2 | 36 | 3 | 33 | 388,608 | 2,871 | 99.2666% | 87.00 |
| S3-c3 | 36 | 3 | 33 | 388,608 | 2,805 | 99.2834% | 85.00 |
| **合计** | **204** | **18** | **186** | **43,717,632** | **16,067** | **99.9633%** | **86.38** |

**逐项对账**：

| 对照对象 | 结果 |
|---|---|
| 各臂报告 JSON `stages[warm_candidate].totals` | **逐字节相同**（6/6） |
| closeout §2.1 合计 hit 43,717,632 | **相同** |
| closeout §2.1 合计 miss 16,067 | **相同** |
| B 报告 §2.1 首轮 ctx（11725/192674/592450/792340/11723/11723） | **全部相同** |
| 首请求 `hit=0` 且 `miss=prompt` | **6/6 精确成立**（如 11,725×3 = 35,175 ✓） |
| 六臂每臂恰好 1 个 route_bucket / model_ref / provider | **成立**（各 204/204 同值） |
| `usage_source = executor`、`request_count_source = observed` | **204/204** |
| 每成员记录数 = 注册轮数 | **18/18 成立**（12/12/12/8/12/12） |

### 5.3 不变量与身份唯一性（GC-10）

| 检验 | 结果 |
|---|---|
| `hit % 128 == 0`（warm） | **186/186，0 违反** |
| `miss ≡ prompt (mod 128)`（warm） | **186/186，0 违反** |
| 桶落点 | 每臂**恰好落其注册桶**：36/36、36/36、36/36、24/24、36/36、36/36 |
| `request_id` 跨臂复用 | **0** |
| `turn_id` 跨臂复用 | **0** |
| `session_id` 跨臂复用 | **0**（18 个，每臂 3 个） |
| `member_id` 跨臂复用 | **0**（18 个，每臂 3 个） |
| 必填字段缺口 | **0**（21 个必需字段，204/204 全在） |
| `finish_reason` | 全部 `stop`；无重试、无重复 turn_id |
| `session_request_seq` | 每成员**连续 1..N**，无跳号 |
| `prefix_change_reasons` | **全臂为空**；`stable_prefix_changed` / `prefix_changed` 均 0 |
| ctx 单调性 | 每成员**单调不减**（无回落） |

**判定**：数据面**完整、自洽、可复现**。「数字可信、身份不可信」这一判定得到再次印证。

### 5.4 与 B 报告的差异定位（GC-6、GC-7、GC-8）

#### GC-6：§4.2 的「逐轮单调 +23」——3/18 成员为 +24

V1 报告 §4.2 写「ctx 逐轮单调 **+23** 无回落」。独立复算 18 个成员的逐轮增量集合：

| 增量 | 成员数 | 成员 |
|---|---:|---|
| `{23}` | **15** | S1a-m1/m2/m3、S1b-m1/m2/m3、S1c-m1/m2/m3、S4-m3、S3-c2-m1/m2、S3-c3-m1/m2/m3 |
| `{24}` | **3** | **S4-768k-m1、S4-768k-m2、S3-c2-m3** |

**判定**：**单调性成立**（无任何一轮减少），因此「未触发 fold」的结论**不受影响**；但「+23」作为**定值**不成立。建议改为：

> 每个成员的 prompt 逐轮增量是**一个固定值**（15 个成员 +23、3 个成员 +24），18 个成员 186 个 warm 样本**无一轮减少**。

（这与上一轮 C 在 pilot 报告发现的 `+23/+24` 是**同一类错误**，出现在另一份文档中。）

#### GC-7：§3.2 的并发间隔——现象成立，引用值不可复现

V1 报告 §3.2 写「S1a（串行）间隔稳定在 **1.47–2.08s**；S3-c2 出现 **0.07–0.28s** 的间隔；S3-c3 间隔进一步压缩到 **0.09–0.93s**」，并称这是「独立核验，非假设」。

**我的三种测量**：

| 测量 | S1a-small | S3-c2 | S3-c3 |
|---|---|---|---|
| 记录内字段 `seconds_since_prev_request`（同成员） | 1.44–2.84 | 1.54–3.28 | 1.47–2.75 |
| 同臂内相邻 `observed_at` 差（含跨成员） | 1.44–2.84 | 0.06–2.61 | 0.001–1.37 |
| 其中 **<1.0s 的个数** | **0** | **13** | **30** |
| 挂钟（banner） | 67.6s | 50.0s | 23.0s |
| B 引用的范围 | 1.47–2.08 | 0.07–0.28 | 0.09–0.93 |

**判定**：

- B 引用的 S1a「1.47–2.08」**不是任何字段的范围**——两种测法都是 1.44–2.84。作为**范围陈述**它是错的。
- S3-c2/S3-c3 的引用值近似**低间隔簇**（我实测最小值 0.06 与 0.001，B 写 0.07 与 0.09），作为「**出现了**这样的间隔」在实质上成立，但**不可由任何陈述的规则复现**。
- **现象本身被我独立证实并强化**：串行臂 **0** 个亚秒间隔，并发臂 **13 / 30** 个；挂钟 67.6→50.0→23.0s 与并发档 1→2→3 单调一致。**「并发确实发生了」成立。**

**建议 B 给出计算规则**（例如「同臂内相邻 `observed_at` 差的 <1s 计数与极值」），使数字可复现。**这是可复现性问题，不是数据缺陷。**

#### GC-8：§1 的 banner 摘要字段与归档不符

V1 报告 §1 表写「折叠规则 | `internal/provider/anthropic/stream_usage.go` sha256 写入每臂 banner」。**归档中六个 banner 全部是 `unreadable:stream_usage.go`**（§4.4）。建议改为「写入 build id；测量层摘要可由该 build 的 blob 复核」，或修路径后补记。

---

## 6. 隐私扫描（§7.2 末项）

**扫描范围**：`~/reasonix-partb-archive/2026-09-24/strata-*`（18 个文件：6 records + 6 report + 6 banner）。

| 扫描项 | 模式 | 命中 |
|---|---|---:|
| 凭据 | `sk-[A-Za-z0-9]{16,}`、`api_key`、`apikey`、`bearer `、`authorization:`、`ANTHROPIC_AUTH_TOKEN`、`BEGIN … PRIVATE KEY` | **0** |
| 正文 / 任务标记 | `Reference brief`、`brief line [0-9]{6}`、`Turn [0-9]+: reply with exactly`、`ACK[0-9]+` | **0** |

**记录本身的字段集（33 个键）不含任何内容字段**：全部是标识、计数、状态与摘要（`prefix_hash` / `stable_prefix_hash` / `session_context_digest` 是哈希）。**因此「记录从不持有正文」是结构性的，不是扫描出来的。**

### 6.1 我拒绝做的一件事

B 的 G0-5 断言「环境凭据的值等于 `deepseek-v4-flash-xie` 的存储 key」。**我未独立复核**，理由是：复核需要把密钥材料读入内存做比对，而本轮隐私边界禁止 C 读取凭据值。**该断言在本文中记为「B 单方证据，C 未复核」**，不作为我的独立结论。

**这不影响任何判定**——GC-2 已经用**不需要读密钥**的方式证明了两份归档的标签不在账本中，那足以支持「无法证明同账号」的结论。

---

## 7. 逐桶门禁状态（§7.3）

**正式采样未执行**（Gate 1 = BLOCKED），因此四桶**都没有正式样本**。下表区分「正式门禁状态」与「V1 探索性数据」。

| 桶 | 正式门禁状态 | V1 探索性数据（不计入正式分母） | 依据 |
|---|---|---|---|
| `lt_32k` | **NOT EXECUTED** | 3 臂（S1a/S3-c2/S3-c3）各 33 warm / 3 成员 | 正式采样未执行 |
| `128k_256k` | **NOT EXECUTED** | S1b-mid 33 warm / 3 成员 | 同上 |
| `512k_768k` | **NOT EXECUTED** | S1c-large 33 warm / 3 成员 | 同上 |
| `768k_1m` | **NOT EXECUTED** | S4-768k **21 warm** / 3 成员 → 报告 JSON `sample_gate_reached: **false**` | 同上；且探索性样本**不足 30** |
| `gte_1m` | **BLOCKED / NOT EXECUTABLE** | 无 | 桶下界 1,048,576 > `hardInputCeiling` 999,744（结构性） |
| fold / compact 边界 | **NOT EXECUTABLE** | 无 | `prefix_change_reasons` 全空、ctx 无回落 → **没有样本**，不是「未观察到差异」 |

**判定（GC-13）**：

- **无一桶可为 PASS**——PASS 要求「route/account/model/build 精确匹配」，而 §2 已证六臂的 route/account 与预注册**不一致**，且正式采样未执行。
- 计划 §9 要求「三个可达主要桶至少 PASS、`768k_1m` 达到预注册样本或有合规阻断且状态如实降级」——**三项均未满足**。
- V2 §3 把 S4 由 8 轮补到 12 轮（21 → 33 warm）**正面回应了这一点**，方向正确。
- **整体正式预注册门禁保持 BLOCKED。**

---

## 8. 对 A / B 的更正清单（C 只报差异，不改他方文件）

### 对 B

| # | 位置 | 现状 | 应改为 |
|---|---|---|---|
| 1 | `..._B_REPORT` §4.2 | 「ctx 逐轮单调 **+23** 无回落」 | 「每个成员一个固定值（15 个 +23、**3 个 +24**），无一轮减少」（GC-6） |
| 2 | `..._B_REPORT` §3.2 | 引用「1.47–2.08 / 0.07–0.28 / 0.09–0.93」 | 给出**计算规则**并重算（如「同臂相邻 `observed_at` 差的 <1s 计数与极值」）；**结论（并发发生）成立**（GC-7） |
| 3 | `..._B_REPORT` §1 | 「sha256 写入每臂 banner」 | 归档实为 `unreadable`；改为「写入 build id，测量层摘要由该 build 的 blob 复核」（GC-8） |
| 4 | `..._B_PREREGISTRATION_V2` §5.2 | 首轮不折叠归因于 `minCompactMessages = 2` | 归因于**活动轮钳制**（`compact_projection.go:686-691`）；`min=2` 被 `:682` 的显式回退绕过（GC-5） |
| 5 | `..._B_PREREGISTRATION_V2` §6 第 2、3 行 | 读起来像已实现 | 标注为**对尚未存在的正式驱动的规格**（`live_team_cache_strata_formal_test.go` 不存在，现有六臂驱动不调用 preflight）（GC-11） |
| 6 | `..._B_PREREGISTRATION_V2` §3 | S0-baseline 的 0.43M 标为「V1 实测」 | S0 是新臂，该格为**外推**（以同条件的 S1a 实测为代理）（§3.2） |

### 对 A

| # | 位置 | 现状 | 应改为 |
|---|---|---|---|
| 1 | `..._GATE_A_PREFLIGHT` §1 冻结表 | `team_preflight_gate.go` md5 `4bd6a97e…`、237 行 | 现为 md5 **`3af53d15…`**、**187 行**（A 写报告后继续编辑）（GC-12） |
| 2 | `..._GATE_A_PREFLIGHT` §4 | 「新增 …（237 行）」 | 同上 |

### 对负责人

| # | 项 |
|---|---|
| 1 | **D-1 裁定**：V2 冻结 `roojin` + `f3634ff267d6`（B 建议①，我确认其算术自洽）／`xie` + `744755eb058b`／原条件阻断。 |
| 2 | **Gate 1 第 4 项**：签核时对**当时的** V2 文件现算 SHA-256（当前 `170a3219…` ≠ 文档内记的 `6eb8433f…`）。 |
| 3 | **合流**：本报告 §7 的逐桶状态并入 closeout；正式门禁保持 BLOCKED。 |

---

## 9. 复现清单

```bash
# 冻结核对
git rev-parse HEAD                     # 23c1e2d38fc89b527406d9572ff10e7b85728232
md5sum internal/provider/anthropic/stream_usage.go   # 71b7d3b79a4c6dfc74d1357b310190ff
md5sum internal/cachelab/usage.go                    # 01a5cb37e30cf272a095fce1a09837f1
md5sum ~/.reasonix/stats/2026-09-23.jsonl ~/.reasonix/stats/2026-09-24.jsonl

# 六臂独立复算（只读归档；默认 t.Skip，CI 不读实验数据）
PART_C_STRATA_DIR=$HOME/reasonix-partb-archive/2026-09-24 \
  go test ./internal/cachelab/ -run TestRecomputeStrataArchive -v -count=1

# 冻结条件的自洽性（三点标定；纯离线，不读注册表）
python3 - <<'PY'
import hashlib
def bucket(kind, ep, name, mode="off", typ=""):
    return kind + "/" + hashlib.sha256("\x00".join([kind,ep,name,mode,typ]).encode()).hexdigest()[:12]
EP = "https://aiapi.lejurobot.com"
print("pilot-gw                 ->", bucket("anthropic", EP, "pilot-gw"))          # bfcb0811b1c8
print("strata-gw                ->", bucket("anthropic", EP, "strata-gw"))         # f119dfdb4214
print("deepseek-v4-flash-roojin ->", bucket("anthropic", EP, "deepseek-v4-flash-roojin"))  # f3634ff267d6
PY

# 账本中自造标签的出现次数（应为 0）
grep -c -e pilot-gw -e strata-gw ~/.reasonix/stats/2026-09-*.jsonl

# 隐私扫描（应 0 命中）
grep -rIl -E "sk-[A-Za-z0-9]{16,}|api_key|bearer |authorization:" \
  ~/reasonix-partb-archive/2026-09-24/strata-*
grep -rIl -E "Reference brief|brief line [0-9]{6}|ACK[0-9]+" \
  ~/reasonix-partb-archive/2026-09-24/strata-*

# A 的 preflight 测试（离线）
go test ./internal/cli/ -run 'TestStrataPreflight' -v -count=1

# 门禁
go build ./... && go vet ./internal/cachelab/ ./internal/team/ ./internal/cli/
go vet -tags live ./internal/cli/ ./internal/cachelab/ ./internal/provider/anthropic/
go test ./internal/cachelab/ ./internal/team/ ./internal/stats/ ./internal/provider/anthropic/ -count=1
go test ./internal/cli/ -run 'Cache|Usage|Team|Strata' -count=1
bash scripts/cache-guard.sh
go run ./tools/repolint
```

**本轮实测**：`go build ./...` 通过；`go vet`（含 `-tags live`）干净；`cachelab` / `team` / `stats` / `provider/anthropic` 全绿；`cli` 缓存相关测试全绿（15.9s）；`cache-guard.sh` 通过；`repolint` **clean (1146 baselined findings)**，`baseline.json` 未改动；A 的 preflight **15 个顶层测试 / 27 含子测试全部通过**。

---

## 10. 归档与可追溯

| 位置 | 内容 |
|---|---|
| `~/reasonix-partc-archive/2026-09-25/strata-recompute.log` | 六臂独立复算输出（**两次运行逐字节相同**，已验证确定性） |
| `internal/cachelab/strata_recompute_test.go` | 复算工具（进仓库，不依赖一次性脚本） |
| `~/reasonix-partb-archive/2026-09-24/strata-*` | B 的六臂归档（**本轮只读，未写入**） |
| `~/.reasonix/stats/*.jsonl` | **全程只读**；md5 与 M0 冻结值逐字节相同 |

**本轮未覆盖、未重写任何归档或账本文件。**

---

## 11. 本文未做

- **未发起任何真实请求**（含探针）；未构造、未使用任何替代凭据。
- **未读取任何凭据值**（因此未独立复核 B 的 G0-5，见 §6.1）。
- 未修改 A 或 B 的写集（`team_preflight_gate.go`、`live_team_cache_strata_test.go`、V2 预注册、A/B 报告**一行未改**）。
- 未修改 V1 预注册、任何历史归档或统计账本。
- 未把 V1 六臂的 204 条探索性记录计入任何正式分母。
- 未改动 `tools/repolint/baseline.json`。
- **未提交、未推送、未开 PR。**

**C 的最终判定（§1–§11，对 V2 的 `170a3219…` 修订）**：Gate 1 = **BLOCKED**（D-1 待裁定）；V2 除身份块外**逐条 PASS**；正式预注册门禁**无一桶可为 PASS**；六臂探索性数据**可独立复现且不变量全成立**，但其身份不可信，**不得升格为正式结果**。

> **§12 是对 V2 当前修订 `b366ced4…` 的复审，签署已更新为 PASS。** 本节以下内容保持不变，作为对更早修订的记录。

---

## 12. 复审（R1）：V2 修订 `b366ced4…` —— 对 §1–§11 的增量

> **本节新增于 2026-09-25，回应 B 的 Gate 1 签核包。** §0–§11 审的是 V2 的**更早修订**（含 `S0-baseline`、74M、未更正的 §5.2/§5.3）；B 已通过 `..._GATE_B_GATE1_PACKAGE` §2/§3 如实标注该差异。本节记录对**当前修订**的独立复审。

### 12.1 被审版本锚定

| 制品 | 摘要 | 与我 §1.3 所审版本的关系 |
|---|---|---|
| `..._B_PREREGISTRATION_V2.zh-CN.md` | **SHA-256 `b366ced41c443a5a0946dae3de1e34dce1dba2478aa6754deb4360852e70d685`**（326 行） | 与我审的 `170a3219…` **不同**；与 B 签核包 §2 锚定值**逐字节相同** ✓ |
| `internal/cli/live_team_cache_strata_formal_test.go`（B，新） | md5 `f51cec2812b653f614faac2bac84d3a0`（561 行） | 我审时**尚不存在**（GC-11） |
| `internal/cli/team_preflight_gate.go`（A） | md5 `4bd6a97eb6cbaed8fc5d0447ae012f05`（**239 行**） | 我 §1.3 实测为 `3af53d15…`/187 行 → **A 已更新；A 报告现在与交付物一致，GC-12 的 A 侧条目已消解** |

### 12.2 GC-12 的 A 侧条目：已消解

A 报告 §1 冻结表所记的 `4bd6a97e…` / 237 行，与**当前**交付物 `4bd6a97e…` / 239 行 **md5 一致**（行数差 2 是 A 报告写完后的小幅增补）。**A 侧的摘要不匹配已不存在。** 建议 A 把行数从 237 更正为 239 即可，属文字更正。

### 12.3 V2 变更 1–5（实质项）的复审

| # | 变更 | C 的独立核验 | 判定 |
|---|---|---|---|
| 1 | D-1 锁定 ①`roojin` + `f3634ff267d6` | 身份块算术我已在 §3.1 独立复算；`TestFormalPreRegIdentityMatchesTheResolver` 在**当前修订**上通过，且把身份块送回**生产解析链**解析、断言其**解析到自己**——这比读表强，是**可执行**验证 | ✅ **PASS** |
| 2 | 新增机器可读 JSON 身份块 | 全文**恰好 1 个** ` ```json ` 围栏（我实测 `grep -c`）：`formalJSONBlock` 取**第一个**围栏，故「多一个围栏 → 身份来自另一处」这一风险当前不成立 ✓；驱动另校验**文档 SHA-256**，改表即改摘要 | ✅ **PASS** |
| 3 | 补入 `Effort` | 我核验代码：`c.effort` 经 `applyDeepSeekThinking` 在 `effort != ""` 时置 `r.OutputConfig`（**`internal/provider/anthropic/reasoning_replay.go:79-80`**，`t == "disabled"` 时早退）。V2 引用 `reasoning_replay.go:79` **正确**；§1.3 另一处写「`reasoning_replay.go:79-81`」中的 `:81` 是右花括号，**建议改为 `:79-80`**。`roojin` 的 `Effort = "max"`（注册表实测）→ 确实改变 wire body | ✅ **PASS**（含一处理由更正） |
| 4 | `S0-baseline` → `S0-canary` | 见 §12.4（我提出两点，均不阻断） | ⚠️ **PASS + 条件** |
| 5 | 臂上限 74M → 72.1M | 现算：63,536+2M+10M+26M+30M+2M+2M = **72,065,536 = 72.07M ≈ 72.1M** ✓ ≤ 80M，余量 **7.93M** ✓；warm 目标合计 **198** ✓；S4 缩放 19,018,148×12/8 = **28.53M** ✓ | ✅ **PASS** |

**一处残留算术瑕疵（非阻断）**：§3 第 114 行的外推式 `0.43 + 0.43 + 6.94 + 21.33 + 28.5 + 0.43 + 0.43` 有 **7 项**，而样本臂只有 **6 个**——其中一项 `0.43` 是已删除的 `S0-baseline` 残留（应替换为 canary 的 `0.012`）。合计仍是 58.49M，**不影响任何阈值或判定**。建议改为六项 + canary。

### 12.4 `S0-canary` 的两点独立发现（不阻断 Gate 1）

B 的设计意图我认可：**把「凭据是否可用 / 该账号是否报 cache split / bucket 是否落对」从 S1a 的正式分母里分出来**，代价 1 次请求，换大额臂之前止损。这是对的。以下两点是**可执行性缺口**，建议随 Gate 1 一并处置：

| # | 发现 | 证据 | 影响 |
|---|---|---|---|
| **R1-a** | **canary 的三个是非问题，驱动一条都没有断言。** `assertStrataPipeline` 只断言「至少有一条记录、有桶、计数已测、每臂恰一个 route bucket」；而 canary 的**唯一一条记录就是首轮**，故 `MissingTokens` 分支**不执行**；route bucket 的断言是「**恰好一个**」，不是「**等于冻结值**」 | `live_team_cache_strata_test.go:493` 为 `len(report.RouteBuckets) != 1`，非 `!= frozen.RouteBucket`；`assertStrataPipeline` 无 `frozen` 参数 | **③「落地的 bucket 真的等于冻结值」在 canary 上不可证**——而这条正是 §2 那场漂移的直接解药。建议在正式驱动里加**运行后**断言：`report.RouteBuckets[0] == frozen.RouteBucket`（一行，B 的写集） |
| **R1-b** | **`route_bucket` 在架构上不可能与冻结值不符**，故 canary 的 ③ 只能捕捉很窄的一类问题。`RouteBucket` 由 `name/endpoint/proxy` **纯客户端**派生，而 `formalAssembly(entry)` 用**同一个 `entry`** 构造成员 → 记录里的 bucket 必然等于 preflight 通过的解析值 | `team_backend_build.go:145`（纯函数）；`formalAssembly` 传 `entry.UserID` | ③ 实为**同义反复**。真正未被覆盖的是**凭据 ↔ 账号**（GC-2 / L-10），而它**客户端不可验证**。canary 因此主要是「**凭据可用 + 该账号报 split**」的检查（①②），③ 应如实标为「随会话成立，非独立证据」 |

### 12.5 `formalAssembly` 的写范围：独立核验为**不触碰操作者注册表**

`formalAssembly` 调用 `AddAgentUser`，而 `pool` 由 `formalPoolFile(poolFile)` 以**操作者目录**为 root 建立。我怀疑这条路径会写操作者的 `team/`，故用**副本**做了探针：

| 探针 | 结果 |
|---|---|
| 在副本目录上 `store.AddAgentUser(一个不在表内的条目)` | **成功**，副本 `agent_users.json` **md5 改变** → 该 store 确实以 `AgentUsersFile` 为固定名写**它 root 所在目录** |
| 在副本目录上重放**驱动真实调用序列**（`formalLoadPreReg` 空摘要拒绝 → `formalPoolFile` → `AgentUser` → `strataPreflightForPoolEntry(期望 build=23c1e2d38fc8)`） | preflight 以 `build drifted: expected "23c1e2d38fc8", resolved "unknown"` **失败并中止**（测试二进制无 VCS 戳）→ 该路径**不发请求** |
| `formalAssembly` 的 store root | `/tmp/TestZZRealSequence…/001`，即 **`t.TempDir()`**，非 pool 目录 |
| 重放后副本两份文件 | `agent_users.json` **UNCHANGED**、`team.json` **UNCHANGED** |

**判定**：**驱动实际读取操作者文件但不写它**。`formalAssembly` 的 `AddAgentUser` 落在 `t.TempDir()` 上。这与签核包「store is used for its production parser and schema check, never written」的表述一致 ✓。

> **残余风险（如实登记，非缺陷）**：`formalPoolFile` 返回的 store 具备**写能力**，其 root 就是操作者 `team/` 目录。当前没有任何调用会写它（探针已证），但**该能力的存在**意味着一次未来编辑就可能把实验团队写进操作者的 `team.json`。建议 B 加一条断言或注释把它钉住。

### 12.6 `Effort` 补丁（签核包 §7）：我的意见

补丁**方向正确**：`Effort` 改变 wire body，A 的 gate 当前对它是盲的（V2 L-12）。三点提示：

1. **「空 ≠ 无意见」的后果被低估了。** `strataExpectationIsFrozen` 会遍历新增字段，空值即拒绝（这是期望行为）。因此该补丁**同时要求** A 的 `frozenTestEntry()` 补 `Effort: "max"`——签核包 §7 已列出，**正确**。但这也意味着：**补丁上线前，A 与 B 的 `strataIdentity` 字面量必须同批更新**，否则驱动会被自己的 gate 拒绝。
2. **B 的 `formalEffortCheck` 应保留**，不要因补丁合并而删除。理由是门禁的一条原则：**同一个字段由两处独立比较，比一处更强**；且 A 侧若日后重构掉该字段，B 侧仍会拦住。
3. **补丁不含 `Provider`**。`formalFrozen` 有 `provider` 字段，`strataIdentity` 没有——V2 §1 的表已正确说明「provider 只用于读表定位，kind 才是 wire 契约」。**无需补**，但要确保 `provider` 的值不被误当成比较项。

**处置建议**：采纳，**但不阻断 Gate 1**——现状下该字段由 B 侧 `formalEffortCheck` 单独比较，**覆盖等价**。补丁属 A 的写集，须走计划 §3 的补丁合流流程。

### 12.7 GC-11 复审：**已消解**

我 §4.3 指出「`strataPreflight` 尚未接线」。当前核验：

| 核验点 | 结果 |
|---|---|
| 正式驱动存在 | ✅ `internal/cli/live_team_cache_strata_formal_test.go`（561 行） |
| 首请求前调用 preflight | ✅ `:519` 在 `formalRun`（`:539`）之前 |
| 期望来自**冻结文档**而非手填 | ✅ `formalLoadPreReg` 解析 §1 的 JSON 围栏，并校验文档 SHA-256；空摘要被拒（我实测：`no pre-registration digest given`） |
| Effort 单列比较 | ✅ `formalEffortCheck`（`:529`） |
| 测量层摘要**读不到即拒绝**（而非留 `unreadable` 占位） | ✅ `formalFrozenLayerDigest`（`:389-396`）在**任何请求之前**执行 |
| banner 记实际解析身份 | ✅ `identity_*` 七行（`:451-457`） |
| 未注册臂被拒绝 | ✅ `formalArmByID` |

**这正好修掉了造成 V1 漂移的那条缺口。** GC-11 **关闭**。

### 12.8 R1 复审结论

| 项 | 判定 |
|---|---|
| V2 变更 1–3、5 | ✅ **PASS** |
| V2 变更 4（`S0-canary`） | ✅ **PASS**，附 §12.4 两条可执行性建议（**不阻断**） |
| V2 变更 6–10（引用与算术） | ✅ **PASS**（§5.2 归因、§5.3 的 g∈{23,24}、§7 L-13 均与我 §5.4/§3.3 一致） |
| GC-11（preflight 未接线） | ✅ **关闭** |
| GC-12 的 A 侧条目 | ✅ **关闭**（A 已更新文件；仅剩行数 237→239 的文字更正） |
| GC-12 的 V2 侧条目 | ✅ **关闭**（V2 §9 已删去正文内摘要，改为签核时现算） |

> **C 对 V2 修订 `b366ced4…` 的签署：PASS（附三条非阻断建议）。**

**三条非阻断建议**（均不改变任何门槛、样本量或结论方向）：
1. §3 外推式去掉残留的第 7 项 `0.43`（改为六项 + canary `0.012`）——§12.3；
2. canary 加入**运行后** `report.RouteBuckets[0] == frozen.RouteBucket` 断言，并把 ③ 标为「随会话成立」——§12.4；
3. §1.3 的引用由 `reasoning_replay.go:79-81` 改为 `:79-80`——§12.3。

**签核时状态（历史快照）**：第 3 项（C 的 PASS）已满足；当时 Gate 1 的剩余阻断仅为第 4 项。该项随后已由负责人完成，V2 重新现算摘要为 `797fd5ba…` 并冻结。

### 12.9 最终裁定同步

负责人已采纳 D-1、D-2、D-3、D-4，接受三条非阻断建议并完成 Gate 1 第 4 项签核。故本审计当前结论为：**Gate 1 = PASS**；V2 以 `797fd5bae6446b8d2b339e6d5e0d0d6815a593766f05282ee75e05e51ce34ed0` 为冻结摘要。§1–§11 中关于 Gate 1 BLOCKED、D-1 待裁定和旧摘要的文字只用于追溯，不是当前门禁状态。正式采样仍须另行满足 Gate 2 的时间窗、凭据现场条件和停止规则。
