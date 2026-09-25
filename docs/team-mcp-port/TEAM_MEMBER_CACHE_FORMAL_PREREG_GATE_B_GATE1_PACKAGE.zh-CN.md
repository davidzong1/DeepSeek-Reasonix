# 正式预注册门禁：Gate 1 签核包（Agent B 提交）

> 角色：Agent B。日期：2026-09-25。
> 依据：`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_3AGENT_PLAN.zh-CN.md` §4（Gate 1 串行签核）、§5.2（A 的验收）、§6（B 工作包）。
> 本文保留提交时的 Gate 1 快照，并在 §6 记录了负责人最终签核。文中 §0–§1 的“待负责人”措辞是签核前状态；**当前状态以 §6 为准：Gate 1 PASS，V2 已冻结**。
> **未发起任何真实请求。**

## 0. 这份包做什么

把 Gate 1 四项中**能提前做完的部分全部做完并留下可核验摘要**，使签核只需一步。它**不代替** C 的 PASS（已取得），也**不代替**负责人的签核。

**提交快照中的剩余阻断**：仅第 4 项——负责人记录签核时间与现算 SHA-256（§6）。该项已在 §6 完成；C 的复审见 `..._GATE_C_AUDIT` §12。

## 1. §4 Gate 1 四项状态

| # | 项 | 状态 | 证据 |
|---|---|---|---|
| 1 | A 交付 preflight 设计与测试；证明错账号/route 在发请求前失败、正确条件可通过本地解析 | ✅ **已交付** | `internal/cli/team_preflight_gate.go`（**256 行**）、`_test.go`（15 项测试，B 独立复跑 15/15 通过） |
| 2 | B 完成 V2 预注册：账号池条目、provider/model/route、构建摘要、臂定义、每臂请求/成员/session 数、预算、停止/排除、归档目录、复算口径 | ✅ **已交付** | `..._B_PREREGISTRATION_V2`（§1 身份块 + §3 臂表 + §4 预算/停止/排除 + §5 边界 + §8 归档/复算） |
| 3 | C 独立逐条审核并签署 PASS，确认新样本不与旧探索性数据混用 | ✅ **PASS** | `..._GATE_C_AUDIT` §12.8：对修订 `b366ced4…` 签署 **PASS**，附三条非阻断建议——**三条已全部采纳**（§3.1） |
| 4 | 负责人记录签核时间与预注册文件摘要（SHA-256），冻结后不再修改 | ✅ **PASS** | §6：2026-09-25 13:25 CST，SHA-256 `797fd5ba…` |

**当前 Gate 1 结论：PASS，四项全部满足。**

## 2. 提交复审的制品与摘要

**本次提交复审的 V2 修订**（C 应在复审记录里记下这一行，以便说明它审的是哪一版）：

```
file    docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION_V2.zh-CN.md
sha256  797fd5bae6446b8d2b339e6d5e0d0d6815a593766f05282ee75e05e51ce34ed0
```

> **这不是签核摘要。** 若 C 的复审要求改动，V2 会变更，届时须**重新**现算。把它记在此处只为锚定"复审的是哪一版"。

**同一提交内的其余制品**（md5，**提交时刻**取值，供交叉核对）：

| 文件 | 归属 | 提交时 md5 | 现状 |
|---|---|---|---|
| `internal/cli/team_preflight_gate.go` | A | `4bd6a97eb6cbaed8fc5d0447ae012f05` | ⚠️ **已变**（现 `790054e853a98f6de376700be9f70d90`，**256 行**）：签核后 A 抽出 `strataPreflightAgainstEntry`，经 dry-run 复验为**保行为重构**。**本条曾被 B 误记为 239 行**——md5 变更后 B 沿用了旧行数，正是"抄写"那一类；A 的 §7.2 记的 256 行是对的。**§1 第 1 行也仍印着 239 行，同属此误** |
| `internal/cli/team_preflight_gate_test.go` | A | `c993fd9875afc67b70308038d611e8aa` | 未变（15 项测试） |
| `internal/cli/live_team_cache_strata_formal_test.go` | B | `f51cec2812b653f614faac2bac84d3a0` | ⚠️ **已变**（现 `81cb0ad5003e5749853cbad06ca54033`）：加运行后 route 断言、键集校验、归档守卫改用 gate 版检查（§6.3） |
| `internal/cachelab/strata_recompute_test.go` | C | `65a974c94afe5b8d75cec726f77ea477` | ⚠️ **已变**（现 `33dc11a853c8ebb4f3a2cf98c75d2a00`）——C 的写集 |

> **这四行是提交快照，不是冻结摘要。** 任何一方在提交后编辑自己的文件都会使该行过期——上表 C 那一行就已经过期（写它的时候是准确的）。**签核时请对当时的工作树现算，不要抄本表**：
>
> ```bash
> md5sum internal/cli/team_preflight_gate.go internal/cli/team_preflight_gate_test.go \
>        internal/cli/live_team_cache_strata_formal_test.go internal/cachelab/strata_recompute_test.go
> sha256sum docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION_V2.zh-CN.md
> ```
>
> **真正需要锚定的是 V2 的那一行**（§2 首段）：它是被审的制品，且 B 在整个复审期间**不修改它**，所以 C 审的版本与负责人签的版本可以是同一份。

**测量层（M0 冻结，全程未变）**：

```
internal/provider/anthropic/stream_usage.go  md5 71b7d3b79a4c6dfc74d1357b310190ff
```

**操作者账本（M0 冻结，全程只读）**：

```
~/.reasonix/stats/2026-09-23.jsonl  md5 98961189e6e7f477d52a5e1e7c83b441
~/.reasonix/stats/2026-09-24.jsonl  md5 61eaaa3a8969b8626156d06e315e9299
```

## 3. V2 相对 C 首轮审计（§0–§11）的变更 —— C 已在 §12 复审通过

C 首轮审计的 V2 修订包含 `S0-baseline`、74M 臂上限、以及未更正的 §5.2/§5.3。其后变更如下；**C 已对复审版本签署 PASS（§3.1）**，本节保留变更清单以便追溯。

| # | 变更 | 依据 | 是否影响判定 |
|---|---|---|---|
| 1 | §1 身份块锁定 D-1 = ①（`roojin` + `anthropic/f3634ff267d6`） | 负责人拍板 | 是——正是 C 判为 BLOCKED 的那一项 |
| 2 | §1 新增**机器可读 JSON 身份块**（驱动的唯一期望来源） | 方案 §8.1 | 是——新增可执行契约 |
| 3 | §1 补入 `Effort`（它改变 wire body） | B 自查 | 是——新增身份字段 |
| 4 | §3 删除 `S0-baseline`（与 S1a 同条件，纯重复），改为 **S0-canary**（1 成员 × 1 轮，不计入任何分母） | 方案 §6.3.1 / §8.5 | 是——臂表与预算变化 |
| 5 | §3 臂上限合计 74M → **72.1M** | 同上 | 是——预算变化 |
| 6 | §5.2 fold 机制归因改为**活动轮钳制** | C 的 GC-5 | 否——结论方向不变 |
| 7 | §5.3 g 改为 **{23, 24}**，带宽 63.0 → 62.9 KB | C 的 GC-6 | 否——推荐值不变 |
| 8 | §6 标注状态并逐行给出驱动的落点（GC-11 消解） | C 的 GC-11 | 否——规格变现状 |
| 9 | §9 删去正文内印的 SHA-256，改为现算 | C 的 GC-12 | 否——流程更正 |
| 10 | §7 新增 L-13（banner 摘要字段曾为空） | C 的 GC-8 | 否——如实披露 |

**变更 1–5 是实质性的**，**6–10 只改引用与算术**；C 已在 §12 对含全部 10 项的修订复审通过。

### 3.1 C 的 R1 复审（§12）与三条建议的采纳

C 对修订 `b366ced4…` 签署 **PASS**（`..._GATE_C_AUDIT` §12.8），附三条非阻断建议。**三条已全部采纳**，故本文（V2）摘要再次变更：

| 建议 | 采纳内容 | 落点 |
|---|---|---|
| 1 | §3 外推式去掉已删除的 `S0-baseline` 残留项：**7 项 → 6 个样本臂 + canary**，合计 58.49M → **58.1M** | V2 §3 |
| 2 | canary 加**运行后** `RouteBuckets[0] == 冻结值` 断言；并把 ③ 如实标为「**随会话成立、非独立证据**」（bucket 纯客户端派生，C 的 R1-b） | V2 §3 + 驱动 `assertFormalRouteIsTheFrozenOne` |
| 3 | §1.3 引用 `reasoning_replay.go:79-81` → **`:79-80`**（`:81` 是右花括号） | V2 §1.3 |

**三条都只改引用与算术，不改任何门槛、样本量或结论方向**，故按方案 §4「若有实质性变更，版本号递增并重新签核」**不构成实质性变更**（最终由负责人判定）。三条已记入 V2 §9.1 的更正记录。

### 3.2 另一处独立发现：拼错的键名会被静默吞掉（A 提出，已修）

A 指出 B 自建了 `formalFrozen` + `formalLoadPreReg`，而非复用 A 的冻结源；摘要校验同样严格，但解码用 `json.Unmarshal`——**拼错的字段名会静默变成空值**。

**独立核实：成立。** `json.Unmarshal` 对未知键**不报错**（实测：`route_buckets` 拼错 → `err = nil`，字段为 `""`）。此前的护栏是 `model_ref`/`wire_model` 的互推断言（能拦住这两个）与 `strataExpectationIsFrozen`（空值会被拒绝）——**不会静默通过**，但失败信息指向「未冻结」，而真因是「键拼错」。

**已修**：新增 `formalDecodeIdentity`，**解码前先比对键集**，缺失键与未知键**都**拒绝并指名。负面对照：把 `route_bucket` 改成 `route_buckets`，测试立即报 `missing [route_bucket], unrecognized [route_buckets]`；文档随后逐字节还原。

**关于"未使用 A 的冻结源"**：这不是取舍，是**写集边界**——`strataIdentity`/`strataPreflight` 是 A 的写集（方案 §3 禁止 B 编辑），且 A 的 gate 是**在途比较**（expected 参数由调用方提供），本就不含"从冻结文档读取期望"这一职责。B 的驱动按方案 §8.1 从**冻结文档**取期望并校验其摘要，这是该职责的落点。两者是**分层**关系而非重复：`formalLoadPreReg`（读冻结文档）→ `strataPreflightForPoolEntry`（在途比较）。**A 的缺口（gate 无 `Effort`）已由签核包 §7 的补丁提议覆盖。**

## 4. 提交时发现并补齐的一处 Gate 1 验收缺口

方案 §5.2 要求「正确 frozen identity 的本地 fixture 通过，**且值与预注册 V2 一致**」。提交前核对：**A 的测试用的是 `https://gw.example` 合成夹具，没有任何测试比对 V2 的真实冻结值。** 该验收项此前**无可执行证据**。

已由 B 补上（B 写集内）：

```bash
go test -tags live ./internal/cli/ -run TestFormalPreRegIdentityMatchesTheResolver -v
```

- **离线**：无请求、无凭据、不读操作者注册表。
- 把 V2 的身份块送回**生产解析链**解析，断言它**解析到自己**（含**计算得出**的 route bucket）。
- **负面对照已验证**：把 V1 的矛盾对（`roojin` + `bfcb0811b1c8`）临时写回身份块，测试立即以 `route bucket drifted: expected "anthropic/bfcb0811b1c8", resolved "anthropic/f3634ff267d6"` 失败；文档随后**逐字节还原**。**若 V1 当初有这道检查，它会在冻结前被拒绝。**
- Effort 单列断言（A 的 gate 无该字段，§5）。

## 5. 残余项（签核须明确接受或阻断）

| # | 项 | 影响 | B 的判定 |
|---|---|---|---|
| R-1 | **A 的 gate 不比较 `Effort`**——它改变 wire body（`output_config.effort`）。B 的驱动已并列比较 | 门禁完整性的一个缺口；比较点留在 B 侧 | **不阻断**——**负责人已裁定**（§6.2）：`Effort` 继续由 `formalEffortCheck` 独立校验，不追加 A gate 字段 |
| R-2 | **凭据 ↔ 账号的绑定不可客户端验证**（V2 L-10）：bucket 有意不哈希凭据 | "这把 key 属于 `roojin`"由操作者断言，客户端无法证明 | **不阻断**，如实披露 |
| R-3 | **S0-canary 会花掉 1 次真实请求**（约 12K token），且**不计入任何 warm 分母** | 采样预算内极小；换来"凭据可用 / 该账号报 cache split / bucket 落对"三个是非答案 | **建议接受**（§6.3.1 明文要求） |
| R-4 | **S4 可用带宽仅约 63 KB**（V2 §5.3），依赖首轮消息形状 | 若 fold 在首轮触发，S4 的上下文前提失效；判据是观测量（`prefix_change_reasons`），不是推导 | **不阻断**，已登记 L-11 |
| R-5 | **驱动的跑臂路径从未对真实 provider 端到端执行** | 编译、vet、离线 dry-run 已过；真实路径待 Gate 2 | **不阻断**，但**不得**在报告中表述为"已运行" |

## 6. 负责人签核（§4 第 4 项）

签核须由负责人填写，**摘要必须现算**（C 的 GC-12 指出过：抄写正文内的摘要会得到一个看起来权威的错值）：

```bash
sha256sum docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION_V2.zh-CN.md
```

| 项 | 值 |
|---|---|
| **签核时间** | **2026-09-25 13:25 CST** |
| **现算 SHA-256** | **`797fd5bae6446b8d2b339e6d5e0d0d6815a593766f05282ee75e05e51ce34ed0`**（331 行；记录时现算，非抄写） |
| **C 的 PASS 对应摘要** | C 签署的是 `b366ced4…`；其三条非阻断建议采纳后 V2 变为上式。**负责人裁定：不需要对采纳版本再签一次**（三条只改引用与算术，不涉门槛、样本量、结论方向） |
| **D-1 确认** | **①**（`deepseek-v4-flash-roojin` + `anthropic/f3634ff267d6`）——2026-09-25 拍板 |
| **D-2 确认** | **A**（注册表只读副本同源取出 id 与凭据，移除环境变量回退）——2026-09-25 确认 |
| **D-3 确认** | 不申请额外 identity probe（bucket 离线可算）；凭据可用性由 S0-canary 现场做布尔核验 |
| **D-4 确认** | **额度通过**：臂上限合计 72.1M、总上限 80M——2026-09-25 确认。**时间窗未给定**，作为 Gate 2 运行前项保留（不阻断 Gate 1） |
| **结论** | **PASS —— Gate 1 四项全部满足** |

### 6.0 负责人裁决记录（2026-09-25，最终）

| # | 裁决 | 效果 |
|---|---|---|
| 1 | **接受并保留 inbox 修复**（`chat_tui_team_inbox.go` + 回归测试） | 修的是已确定性复现的重复注入竞态；**流程偏差记录即可，不影响 Gate 1** |
| 2 | **R-1 不阻断 Gate 1** | `Effort` 继续由 B 的 `formalEffortCheck` 独立校验；**不追加 A gate 字段**，避免改动已冻结链路 |
| 3 | **A 侧 V2 自证缺口已关闭** | `TestFormalPreRegIdentityMatchesTheResolver` 已覆盖真实 V2 身份块，含 route bucket 与 Effort |
| 4 | **未完成的 `-count=3000 -race` 不作为证据，也不阻断** | 确定性复现 + `-count=500 -race` + 全量测试已充分 |
| 5 | **Gate 1 结论：PASS**，V2 SHA-256 保持 `797fd5ba…` | 正式采样仍属 **Gate 2，尚未授权** |

**负责人本轮独立验证**：相关 race 测试通过；`go test ./internal/cli/ -count=1` 通过（约 81s）；`git diff --check` 通过。**B 已独立复核 `git diff --check` 与 V2 摘要**（后者仍为 `797fd5ba…`，与签核值逐字节相同）。

> **V2 自此冻结**（`797fd5ba…`），B 侧不再修改它。

> **V2 自此冻结**（`797fd5ba…`），B 侧不再修改它。
>
> **V2 正文的 §9 仍写着「未签核」——那是冻结时刻的状态快照，不是矛盾。** 被签的文档不应自我认证：签核的权威记录是**本表**。改动 V2 以写入签核结果，会让摘要与被签内容不一致——正是 C 的 GC-12 所禁止的那类做法。

### 6.1 本记录之外的工作树变动（登记，非本任务）

记录签核时，工作树出现两个**不属于本任务**的已修改文件：`internal/cli/chat_tui_team_inbox.go` 与 `internal/cli/chat_tui_team_inbox_prefetch_test.go`。它们与缓存计量无关，**B 未改动、未触碰**其内容，不影响本签核摘要（该摘要只覆盖 V2 文件本身）。

**负责人裁决（2026-09-25）：接受并保留该修复。** 它修的是已确定性复现的重复注入竞态（见 A 报告 §7.4：预取批次在**存储时**而非**排队时**盖 ack 纪元，故确认落在读进行中时，陈旧批次看起来是当前的）。**流程偏差记录即可，不影响 Gate 1**——两个文件不在 A 的写集，按方案 §3 本应先提补丁再合流。

（同轮出现的 `internal/cli/team_preflight_frozen.go(+_test.go)` 属另一项处置，见 §6.2。）

### 6.2 签核后的处置（负责人裁定 + 收尾）

**裁定（2026-09-25）**：冻结源的格式**采用 B 的**（身份块嵌入预注册文档，**一个** SHA-256 同时钉住正文与机器可读块），**删除 A 的格式**。

| 处置 | 对象 | 摘要（删除前） | 理由 |
|---|---|---|---|
| **删除** | `internal/cli/team_preflight_frozen.go`（170 行） | md5 `76d4223ba0a696205b14db24b5b06071` | 它定义 `strataFrozenDoc` / `strataFrozenSource` / `strataArmPreflight`：**独立 JSON 文件**格式，键为 `version/pool_entry/provider_kind/endpoint/wire_model/model_ref/route_bucket/build`，**无 `effort`**，且用 `DisallowUnknownFields` |
| **删除** | `internal/cli/team_preflight_frozen_test.go`（282 行，7 项测试） | md5 `6676ee5c01856266f2928f00408c842d` | 随其被测对象一并移除 |

**删除前已核验**：两文件**无任何外部引用**（未接线），删除后 `go build ./...`、`go vet`（含 `-tags live`）**全部通过**。

**为什么删除而非合并**：A 的格式要求**散文文档 + 独立 JSON 文件两个制品手工同步**——那正是方案 §8.1 要禁止的"第二套手填源"，而 V1 那场漂移的根因就是**一个标签与一个凭据来自两个源**。B 的格式把两者放在**同一份被签文档**里，一个摘要覆盖两者，**不可能只改一侧**。A 的两点优点已由 B 侧等价提供：严格解码 → `formalDecodeIdentity` 的**键集校验**（缺失键与未知键都拒绝并指名）；`version` 字段 → 记录为**未采纳的改进项**，因采纳需改写 V2 §1，会作废已签摘要。

**保留**：`team_preflight_gate.go` 中 A 的 `strataPreflightAgainstEntry` 抽取（`team_preflight_gate.go:96`）。这是**保行为的重构**——删除 A 的格式文件后 `strataPreflightForPoolEntry:89` 仍委派给它，`go vet` 通过，且两条漂移拒绝路径经**离线 dry-run 复验仍然触发**。其摘要已由 `4bd6a97e…` 变为 `790054e8…`（提交后的编辑），**不影响签核摘要**（后者只覆盖 V2）。

**R-1 已由负责人裁定为不阻断**（2026-09-25）：`Effort` 继续由 B 的 `formalEffortCheck` 独立校验，**不追加 A gate 字段**——避免改动已冻结的链路。故 §7 的补丁提议**不予采纳**，其文字保留在本文中仅供追溯。

### 6.3 收尾中发现并修复的一处真实缺陷（B 写集）

核验签核后的工作树时发现：**`pilotArchiveDir` 的 OS 临时目录防护在目标不存在时被静默绕过。**

```
if resolved, err := filepath.EvalSymlinks(dir); err == nil && HasPrefix(resolved, tmp) { 拒绝 }
                                  ^^^^^^^ 目录不存在 -> err != nil -> 整个条件为假 -> 放行
```

方案明令禁止 `/tmp` 作为实验的唯一副本。该函数是 pilot 期的 helper，被 V1 两个驱动与 V2 驱动共用；A 的 `strataArchiveDir` 写对了（先取字面值再 `MkdirAll`）。**V2 驱动已改用 gate 版检查**（`strataArchiveDir` + 日期子目录），并复验：

> 指向一个**尚未创建**的 `/tmp/...` 路径 → 现被拒绝：`archive directory "..." is under the OS temp directory, which the plan forbids as an experiment's only copy`。修复前该路径会被静默接受并在其中落证据。

**V1 的两个已提交驱动仍使用旧 helper**——它们的归档已落盘且不在 /tmp，故历史证据不受影响；是否回溯修改 V1 驱动属负责人的判断（改动会变更已提交的测量代码，B 未擅自处理）。

### 6.4 S0-canary 已执行（负责人授权，1 次真实请求）

| 项 | 结果 |
|---|---|
| ① `roojin` 凭据可用？ | **是**（本轮新信息：该账号自 09-24 07:11 后无使用记录，此前各轮实际用的是 `xie` 的凭据） |
| ② 该账号报 cache split？ | **是**（`hit 0 + miss 11,726 > 0`、`accounting_valid=true`、未被 `no_split` 排除） |
| ③ route bucket 等于冻结值？ | **是，但非独立证据**（bucket 纯客户端派生，C 的 R1-b） |
| **未回答** | **尚未观察到 `roojin` 上的 warm 命中**——一次请求在原理上无法证明；V1 的命中出现在**第二次**请求。该前提只能由某个臂的第 2 轮起验证 |
| 成本 | 输入 **11,726** token（臂上限 65,536），挂钟 4.3s |
| 归档 | `~/reasonix-partb-archive/2026-09-25/`（banner / records / report / `S0-CANARY-FINDINGS.md` / 发请求前的 `TREE-STATE-at-S0-canary.txt`），隐私扫描 0 命中 |

**provenance 提示**：canary 由驱动 md5 `ea214f8e…` 产出；其后驱动改为 `81cb0ad5…`（§6.3 的归档守卫修复）。该修复只改**归档目标的校验**，不触及请求路径、记录写入或报告——故 canary 的读数不受影响。**这些记录由人工整理，不由驱动生成。**

## 7. 提议给 A 的补丁（R-1；**B 不代改 A 的写集**）

方案 §3 规定：编辑共享文件须先提补丁、由负责人合流。以下为**文字补丁**，可否采纳由 A 与负责人定；**不阻断 Gate 1**。

```diff
 type strataIdentity struct {
 	PoolEntry string
 	Kind string
 	Endpoint string
 	WireModel string
 	ModelRef string
 	RouteBucket string
+	// Effort is what the entry sets for reasoning effort. It reaches the
+	// request body as output_config.effort, so an entry whose effort moved is
+	// a different request shape even when every other field still matches.
+	Effort string
 	Build string
 }

 func strataIdentityFields(id strataIdentity) []strataIdentityField {
 	return []strataIdentityField{
 		{"pool entry", id.PoolEntry},
 		{"provider kind", id.Kind},
 		{"endpoint", id.Endpoint},
 		{"wire model", id.WireModel},
 		{"model ref", id.ModelRef},
 		{"route bucket", id.RouteBucket},
+		{"effort", id.Effort},
 		{"build", id.Build},
 	}
 }

 func resolveStrataIdentity(u team.AgentUser, proxy netclient.ProxySpec) (strataIdentity, error) {
 	...
 	return strataIdentity{
 		PoolEntry:   resolver.name,
 		Kind:        resolver.kind,
 		Endpoint:    resolver.endpoint,
 		WireModel:   resolver.model,
 		ModelRef:    resolver.ref,
 		RouteBucket: resolver.RouteBucket(),
+		Effort:      strings.TrimSpace(u.Effort),
 	}, nil
 }
```

**采纳后需要同步的两处**（否则会互相拒绝）：

1. **A 的测试夹具** `frozenTestEntry()` 补 `Effort: "max"`——`strataExpectationIsFrozen` 会遍历新增字段，空值即拒绝（这是期望行为）。
2. **B 的驱动** `formalFrozen.strata()` 补 `Effort: f.Effort`（一行）。届时 B 的 `formalEffortCheck` 可保留为第二道独立比较（无害），也可删除。**B 会在 A 合并后立即同步，或在本包被接受前保持现状**——现状下该字段由 B 侧单独比较，覆盖等价。

## 8. 本文未做

- **未发起任何真实请求**；未构造、未使用任何替代凭据。
- 未改动 A/C 的文件（§7 是文字补丁，不是编辑）。
- 未代替 C 复审，未代替负责人签核。
- 未把 V1 六臂的 204 条探索性记录计入任何分母。
- 未提交、未推送、未开 PR。
