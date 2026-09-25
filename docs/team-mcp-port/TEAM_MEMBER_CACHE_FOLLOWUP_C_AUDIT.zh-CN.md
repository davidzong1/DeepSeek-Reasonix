# Team Member 缓存计量：Part C 后续复核 —— 历史复算调和、K3 快照审计与独立统计复核

> 执行依据：`TEAM_MEMBER_CACHE_FOLLOWUP_EXECUTION_PLAN.zh-CN.md` §6（Agent C）、§3（M0 冻结）、§8（完成定义）。
> 前置：`TEAM_MEMBER_CACHE_MERGE_RECORD.zh-CN.md`（合流纪要）、`TEAM_MEMBER_CACHE_PART_C_FINAL_REVIEW.zh-CN.md`（上一轮 C）。
> 日期：2026-09-25。**本文不改 Provider 生产语义，不改 Team 生产请求观测字段，不覆盖任何账本文件。**

---

## 0. 结论摘要

| # | 结论 | 强度 | 影响 |
|---|---|---|---|
| **FC-1** | 合流纪要 §9.8 的"K3 快照摘要不一致"**已解决，且差异不存在于语义层**：归档快照与当前 `usage.go` 的差异**只有一处**，是 `ReportedUsage.Oracle*` 的一个字段注释块（9 行 → 3 行，为过 repolint 的 essay 限额而压缩）。去掉注释与空行后两份文件的 SHA-256 **逐字节相同**（`c8055a37…`）。**当前文件可以被声明为冻结版本的等价物。** | **证据**（§1） | §9.8 关闭；K3 的冻结可追溯 |
| **FC-2** | 合流纪要 §3.1 的行数差异**已完全解释**：两个口径分别是"模型前缀匹配的全部行"与"其中带 `prompt` 键的行"。`1973/1964`、`1714/1713` 的差正好是**没有 usage 计数器的行数**（9 / 1）。**两个数字都正确，只是口径不同**；共同小时切片的 `174/169` 同源（169 是带 `prompt` 键的那个）。**比率完全一致（83.83% / 67.70% / 86.14% / 76.51%）**，所以**不需要撤回任何精确数字**，只需要把口径写清楚。 | **证据**（§2） | §3.1 的"未消解"关闭 |
| **FC-3** | 历史账本的**全部 17 个键已枚举**。计划 §6.2 要求的每一个分层维度——route/账号、成员/session、请求身份、usage provenance、上下文形状、前缀诊断、**原始 wire 计数器**、oracle——在账本上**全部缺席（无字段）**。因此 per-request 重算、oracle 在场性、成员/session/task family 识别**在结构上不可能**，未来采样**不能**称为历史重跑。 | **证据**（§3） | §6.4 判定成立 |
| **FC-4** | 账本**唯一**可用于"单请求资格"的字段是 `requests`，且它**没有 provenance**。按它切分：单请求行 84.50% / 68.37%，聚合行 72.81% / 56.60%。**这个切分不能升级为 per-request 基线**（无人标记过该计数是实测的），但它证明 A 的降级决定**在数据上有后果**：约 3.3% 的行、4–5% 的 miss token 属于多请求行。 | **证据**（§3.2） | 支持 A v1 的降级，不改变其结论 |
| **FC-5** | B 的 pilot **独立复算逐项吻合**：从 B 归档的 30 条记录独立求和得 `hit=1,613,440 / miss=171,990`，与其报告 JSON 的 `overall.totals` **完全相同**；三次运行的 warm 分成员率稳定在 0.1pp 内；块对齐不变量 30/30 成立。 | **证据**（§4.1） | B 的数字可独立复现 |
| **FC-6** | 发现 B 报告两处**可追溯性问题**：① §3.5 写"三个成员的 prompt 逐轮增量**全部恰好 +23**"，独立复算显示 `pilot-small` 是 **+24**；② §3.1、§3.2 主表、§3.2 复跑列、JSON totals **四组数字分别来自三次不同的运行**，并排呈现时未标注来源，且 §3.2 主表的 `pilot-large` warm hit 印 1,093,432 而记录为 1,090,432（**差 3,000，一位数字，转写笔误**）。**结论方向不变**（append-only、无一轮减少、warm 99.2–99.9%）。 | **证据**（§4.2、§4.1） | B 需措辞与来源标注更正 |
| **FC-7** | 独立复算**不影响**任何已发布的判定：K1/K3 的 GO、历史对比的撤回、无缓存行为候选准入，全部维持。 | **决策**（§5） | 合流判定不变 |

**一句话**：本轮把合流纪要留下的三处"未消解"全部关闭了——K3 快照差异是**纯注释**、行数差异是**纯口径**、历史账本的不可推断性是**结构性的**。同时用独立复算确认了 B 的 pilot 数字，并查出其报告里一处 +23/+24 的措辞过宽。

---

## 1. K3 快照审计：摘要差异的来源（§6 步骤 1）

### 1.1 三份材料的摘要

| 材料 | 路径 | MD5 | 行数 |
|---|---|---|---:|
| 归档冻结快照 | `~/reasonix-partb-archive/2026-09-24/cachelab-usage.go.snapshot` | **`cc0cda4fcbd22bd7f68602d376656865`** | 485 |
| 当前工作树（= 已提交） | `internal/cachelab/usage.go` | **`01a5cb37e30cf272a095fce1a09837f1`** | 479 |
| 冻结登记表 | `~/reasonix-partb-archive/2026-09-24/source-md5.txt` | 记 `cc0cda4f…` | — |

当前文件与 `git HEAD`（`71d2e3695`）**逐字节相同**（`git diff HEAD -- internal/cachelab/` 为空），即已提交的就是被测试的就是被归档的。

### 1.2 差异的完整内容

用 `difflib.SequenceMatcher` 对两份文件做全文比较：**只有 1 个差异块**。

```diff
 	Completion int
-	// Oracle is the gateway's own secondary account of the same request, when the
-	// response carried one under billing_usage. It is recorded as a sidecar and is
-	// never read to produce the fields above: it is a private gateway extension
-	// that some responses omit, and a reading taken from it would be a claim about
-	// the gateway's bookkeeping rather than about the protocol's usage event.
-	//
-	// OracleAgrees is true only when both readings exist and describe the same
-	// prompt, hit and miss. A response with no oracle leaves it false and says so
-	// through OraclePresent, never by assuming agreement.
+	// Oracle* is the gateway's own second account of the same request, recorded
+	// as a sidecar and never read to produce the fields above. A response that
+	// carried none leaves OraclePresent false: undecided, not agreeing.
 	OraclePresent bool
```

**9 行注释替换为 3 行注释，代码行零改动。**

### 1.3 语义等价的证明

| 检验 | 结果 |
|---|---|
| 差异块数量 | **1** |
| 差异块类型 | 全部是 `//` 注释行 |
| 去掉注释行与空行后的 SHA-256 | 快照 `c8055a3794a2c98421745222e6805220295cb0fe17fd7e6b68db05592df12046`<br>当前 `c8055a3794a2c98421745222e6805220295cb0fe17fd7e6b68db05592df12046` → **相同** |
| 块注释 `/* */` | 两份均为 **0** |
| 反引号字符串（会含 `//`） | 两份均为 **0** |

去注释比较是**可靠**的：两份文件都没有块注释，也没有能把 `//` 包进字符串字面量的反引号字符串。

### 1.4 变更时间线与原因

| 时刻（2026-09-24/25） | 事件 |
|---|---|
| 20:43 | B 归档快照，记 `cc0cda4f…` |
| 21:12 | C 为通过 repolint 的 essay 限额（`capFieldDoc = 3` 行）压缩该字段注释，文件变为 `01a5cb37…` |
| 21:31 | C 把 51 个探针产物复制到 `~/reasonix-partc-archive/2026-09-24/`（**复制的是归档时点的文件，不含此后压缩的版本**） |
| 09-25 01:51 | `71d2e3695` 提交当前版本 |

**因此 `source-md5.txt` 记录的是 20:43 的版本，而 21:12 之后的版本只改了注释。** 合流纪要 §9.8 把它记为"当前文件不能直接声称与冻结版本逐字节相同"——**逐字节确实不同，但语义相同**，这个区分现在有了证明。

### 1.5 在当前版本上重跑 K3 相关测试与重放

**被测版本的确切摘要（本节所有结果都对应这一组）：**

```text
internal/cachelab/usage.go            md5 01a5cb37e30cf272a095fce1a09837f1
internal/cachelab/usage.go + recorder.go（解析器全体）md5 051a0afe356429a408c2c332d3894569
internal/cachelab/usage_test.go       md5 3ef1267c63e3aab01591158871f58a43
internal/provider/anthropic/stream_usage.go  md5 71b7d3b79a4c6dfc74d1357b310190ff（与冻结值相同）
```

| 检查 | 命令 | 结果 |
|---|---|---|
| 包测试 | `go test ./internal/cachelab/ -count=1` | **全绿**（32 个顶层测试） |
| K3 形状表 | `go test ./internal/cachelab/ -run TestParseUsageReadsEachProviderShape -v` | **11/11 子测试通过** |
| K3 定向测试 | `-run TestParseUsageBinds\|ReportsTheOracle\|PrefersAPopulated\|KeepsTheOracleOut\|RecorderReadsTheGatewayShape\|RecorderLeavesTheOracle` | **6/6 通过** |
| journal 重放 | `PART_C_JOURNAL_DIR=/tmp/cachelab-runs go test ./internal/cachelab/ -run TestReplayJournals -v` | **11 份 journal，各臂 rate 与 B 报告逐项相同**（见下表） |

**journal 重放结果（当前解析器）：**

| 臂 | samples | eligible | rate | hit / miss |
|---|---:|---:|---:|---|
| B0-pilot | 12 | 8 | 0.9239 | 22528 / 1856 |
| B1-baseline-repeat | 31 | 30 | 0.9239 | 84480 / 6960 |
| B2-interval-short | 31 | 30 | 0.9239 | 84480 / 6960 |
| B3-serialization | 31 | 30 | 0.9239 | 84480 / 6960 |
| B3-tool-schema | 31 | 30 | 0.9221 | 84480 / 7140 |
| B3-system-tail | 32 | 29 | 0.9209 | 81664 / 7018 |
| B4-ladder-mid | 31 | 30 | 0.9904 | 518400 / 5040 |
| B4-ladder-large | 31 | 30 | 0.9971 | 2012160 / 5910 |

**判定（FC-1）**：K3 的冻结版本与当前测试版本**语义同一**，差异已逐段说明；归档快照仍可追溯；当前版本通过全部 K3 测试与 journal 重放。**§9.8 关闭。**

---

## 2. 历史账本复算与口径调和（§6 步骤 2、3）

### 2.1 复算的可执行形式

复算逻辑已落在仓库里，不依赖一次性脚本：

```bash
# Go（推荐：与仓库同一测试框架，只读打开账本）
PART_C_LEDGER_DIR=~/.reasonix/stats \
  go test ./internal/cachelab/ -run TestRecomputeLedgerNumbers -v -count=1
```

`internal/cachelab/ledger_recompute_test.go` 默认处理 `2026-09-23` / `2026-09-24` 两天，**只读**打开 `~/.reasonix/stats/<day>.jsonl`，从不写入。若目录不存在则 `t.Skip`，因此在 CI 中不读操作者数据。

**时区规则**：每行 `ts` 是带显式偏移的 RFC3339，而日文件名用的是**账本自己的本地日**。复算**直接取 `ts[11:13]` 作为小时**，**不做 UTC 转换**——转换会把行搬到本次比较所针对的那条小时边界之外。

**输入摘要（复算时实测）**：

```text
2026-09-23.jsonl  md5 98961189e6e7f477d52a5e1e7c83b441  sha256 c06def5f57a45da1…  2272 行
2026-09-24.jsonl  md5 61eaaa3a8969b8626156d06e315e9299  sha256 156ad274fc2bc939…  1777 行
```

两个 md5 与 M0 冻结值（`~/reasonix-partb-archive/2026-09-24/M0-FREEZE.txt`）**逐字节相同**：账本在本轮期间未被改写。

### 2.2 行数差异的来源（FC-2）

合流纪要 §3.1 记「C §4.1 报告 1,964/1,713 与 169，本次独立汇总 1,973/1,714 与 174，口径差异未消解」。复算给出**唯一的解释**：

| 日 | 模型前缀匹配的全部行 | 其中**带 `prompt` 键**的行 | 差 | 加权率 |
|---|---:|---:|---:|---:|
| 09-23 | **1,973** | **1,964** | 9 | 两者均 **83.83%** |
| 09-24 | **1,714** | **1,713** | 1 | 两者均 **67.70%** |

**差正好等于"没有 usage 计数器的行"**：09-23 有 9 行、09-24 有 1 行，其键集只有 `ts/model/source/requests/completion/reasoning/total/…`，**没有 `prompt` / `cache_hit` / `cache_miss`**（写入方对零值省略键）。这类行不贡献任何 token，因此**两个口径的加权率完全相同**。

共同小时切片的 `174 / 169` 同源：

| 口径 | 09-23 `00h+01h` | 09-24 `00h+01h` |
|---|---:|---:|
| 前缀匹配全部行 | **174** | **633** |
| 其中带 `prompt` 键 | **169** | **633** |
| 加权率 | **86.14%**（两口径相同） | **76.51%** |

**判定**：`1973/1964`、`1714/1713`、`174/169` **都是正确的**，只是统计口径不同（"模型前缀匹配的行"vs"其中带 usage 计数器的行"）。**比率不受影响，因此不需要撤回任何精确数字**；合流纪要 §3.1 与 §3.3 中的"未消解"与"不作为独立验证"可以撤销，但须把两个口径都写进报告。

> 计划 §6.3 要求"若无法通过明确过滤规则解释，标记旧数字不可复现并撤销精确行数主张"。**本轮的结论是相反方向**：差异**可以**被一条明确规则解释，因此保留数字，并在本文中把口径固定下来。

### 2.3 逐小时复算（与上一轮 C §4.1 逐项相同）

```text
09-23  00h n= 76 83.42%   01h n= 98 87.29%   17h n= 50 68.08%   18h n=430 88.29%
       19h n=243 88.41%   20h n=358 89.79%   21h n=138 91.92%   22h n=316 74.75%   23h n=264 79.03%
09-24  00h n=293 76.00%   01h n=340 76.88%   02h n=240 69.69%   03h n=229 66.33%
       04h n=198 60.85%   05h n=193 59.67%   06h n=184 60.60%   07h n= 37 54.37%
```

**窗口覆盖**：09-23 = `00,01,17,18,19,20,21,22,23`；09-24 = `00,01,02,03,04,05,06,07`；**共同小时仅 `00h,01h`**。

**两种口径的两个关键数字**：

| 规则 | 09-23 | 09-24 | 差 |
|---|---:|---:|---:|
| 等样本量（各取 ts 排序前 1714 行） | 1,714 行 / **84.47%** | 1,714 行 / **67.70%** | **−16.77pp** |
| 钟点对齐（仅共同小时 00h+01h） | 174 行 / **86.14%** | 633 行 / **76.51%** | **−9.63pp** |

**判定**：合流纪要的 C-5 / C-6 结论**不变**——等样本量对比因窗口不匹配而不可比；对齐切片的 −9.6pp 仍是**观察值**，不能升级为结论（样本与 provenance 都不足）。本轮的贡献是把这两个数字**变成可复现的**，而不是改变它们。

---

## 3. 历史账本能回答与不能回答什么（§6 步骤 4）

### 3.1 全部字段枚举（FC-3）

对两天 route 行做键的并集，得到**全部 17 个键**：

```text
ts  model  source  prompt  completion  reasoning  cache_hit  cache_miss  total
requests  usage_source  cost_complete  cost_estimated  display_complete
display_status  incomplete_reason
```

按计划 §6.2 要求的分层维度逐项核验：

| 计划要求的维度 | 账本字段 | 判定 |
|---|---|---|
| route / 账号 | — | **无字段** |
| 成员 / session / turn | — | **无字段** |
| 请求身份 / 计数 provenance | `requests`（**无 provenance 标记**） | **只有计数，没有来源** |
| usage provenance（unknown/estimated） | — | **无字段**（只有计费侧的 `incomplete_reason`，与 usage 解析无关） |
| 上下文形状（`context_prompt_tokens`） | — | **无字段**；只有折叠后的 `prompt` |
| 前缀诊断 | — | **无字段** |
| **原始 wire 计数器**（`input_tokens` / `cache_read_*`） | — | **无字段**（归一化后即丢弃） |
| oracle（`billing_usage`） | — | **无字段** |

### 3.2 唯一可用的资格信号，及其边界（FC-4）

账本上唯一与"单请求"有关的字段是 `requests`：

| 日 | 分布 | 单请求行 | 多请求行 |
|---|---|---|---|
| 09-23 | `1: 1904, 2: 68, 3: 1` | hit 139,255,168 / miss 25,535,866 → **84.50%** | hit 7,313,920 / miss 2,731,727 → **72.81%** |
| 09-24 | `1: 1657, 2: 56, 3: 1` | hit 105,157,632 / miss 48,649,797 → **68.37%** | hit 5,280,000 / miss 4,048,619 → **56.60%** |

**这个切分不能升级为 per-request 基线**，原因与 A v1 §2.2 完全一致：`requests` 没有 provenance 标记，兼容规则「0 视为 1」让"观测到 1 个请求"与"没人观测"在账本上不可区分。但它是**数据上的后果证明**：约 3.3% 的行、4–5% 的 miss token 属于多请求行，把它们当作单请求会直接污染 per-request 率。

### 3.3 不能重算的清单（判定）

| 问题 | 能否用现存账本回答 | 依据 |
|---|---|---|
| per-request 命中率 | **不能** | `requests` 无 provenance（§3.2） |
| oracle 在场性 / 历史 K1 假象占比 | **不能** | 无原始 wire 计数器、无 oracle 字段（§3.1） |
| 成员 / session / 任务族分层 | **不能** | 无对应字段（§3.1） |
| 前缀是否被重写（P1） | **不能** | 无诊断字段 |
| 冷启动确认 | **不能** | 无 session 身份；`hit == 0` 只能读作"未报告 cache read" |
| 两天差异的**归因** | **不能** | 窗口不匹配 + 无分层字段 + 无 provenance（§2.3） |
| 两天差异的**描述**（钟点对齐的 −9.6pp） | **能** | 可复现（§2.3） |

**因此：未来含 K1 的采样只能建立新的前瞻、可分层基线，不能重现或证明过去的历史时段原因。** 不得把新采样称为"历史重跑"。

---

## 4. 独立复算新的 Team / journal 样本（§6 步骤 5）

**数据源严格隔离**：本节只读 **B 的归档记录**（`~/reasonix-partb-archive/2026-09-24/pilot-records-*.jsonl`）与 **B 的 journal**（`/tmp/cachelab-runs/`），**不**读成员记录、**不**读统计账本、**不**与任何其他数据集合并。

### 4.1 B 的 pilot 逐项复算（FC-5）

从 B 归档的 30 条记录**独立求和**：

| 量 | 独立求和 | B 报告 JSON | 一致 |
|---|---:|---:|---|
| 全部 hit | **1,613,440** | 1,613,440 | ✅ |
| 全部 miss | **171,990** | 171,990 | ✅ |
| 记录数 / 成员数 | 30 / 3 | 30 / 3 | ✅ |
| `request_count_source` | `observed` 30/30 | 30/30 | ✅ |
| 块对齐违反 | **0**（`hit % 128 == 0` 且 `miss ≡ prompt (mod 128)`，30/30） | 27/27 warm | ✅ |

**warm 分成员（剔除每成员首请求）**，逐运行列出，与 B 报告 §3.2 对照：

| 归档运行 | 成员 | warm hit | warm miss | 加权率 | miss/请求 |
|---|---|---:|---:|---:|---:|
| `…492890560284` | small / mid / large | 105,728 / 408,576 / 1,090,432 | 805 / 906 / 791 | 99.24% / 99.78% / 99.93% | 89.4 / 100.7 / 87.9 |
| **`…655106980265`** | small / mid / large | **105,600 / 408,576 / 1,090,432** | **897 / 870 / 755** | **99.16% / 99.79% / 99.93%** | **99.7 / 96.7 / 83.9** |
| `…916540630588` | small / mid / large | 105,728 / 408,576 / 1,090,432 | 823 / 879 / 764 | 99.23% / 99.79% / 99.93% | 91.4 / 97.7 / 84.9 |
| `final`（= `…916540630588`） | small / mid / large | 105,728 / 408,576 / 1,090,432 | 823 / 879 / 764 | 99.23% / 99.79% / 99.93% | 91.4 / 97.7 / 84.9 |

**B 报告 §3.2 的主表对应运行 `…655106980265`（11/12 项逐字节吻合）**，唯一差异是 `pilot-large` 的 warm hit：报告印 **1,093,432**，记录求和为 **1,090,432**（**差 3,000，一位数字**），率仍同为 99.93%（三位小数内不可分辨）。**判定为转写笔误，不是数据差异。**

**B 报告 §3.2 的"独立复跑"列**（small 99.23% / 91.4、mid 99.79% / 97.7、large 99.93% / 84.9）**逐项等于运行 `…916540630588`（= `final`）**。

**B 报告 §3.1 的 `warm_candidate` 行与 `first_request` 行来自运行 `…492890560284`**（`warm hit/miss = 1,604,736 / 2,502` 与 `first hit/miss = 8,448 / 169,789` 两项都只在该运行成立）；**B 报告 JSON 的 `overall.totals`（1,613,440 / 171,990）来自运行 `…916540630588`（= `final`）**。

**判定**：**B 报告的 §3.1、§3.2 主表、§3.2 复跑列、JSON totals 四组数字分别来自三次不同的运行**，各自内部自洽，但报告把它们并排呈现时**没有标注各自的来源运行**。各运行之间的率差异在 **0.1pp 内**（99.16–99.24% / 99.78–99.79% / 99.93%），且 miss/请求的移动（small 的 89.4→99.7→91.4）正是上一轮 C §2 预言的**块边界落点差异**，不是缓存行为差异。**这不是数据缺陷，是报告可追溯性问题**——建议 B 为每张表标注来源 run id。

**跨运行一致性**（三次独立运行 + final，warm 加权率）：

| 归档运行 | large | mid | small |
|---|---|---|---|
| `…492890560284` | 99.93% / 87.9 | 99.78% / 100.7 | 99.24% / 89.4 |
| `…655106980265` | 99.93% / 83.9 | 99.79% / 96.7 | 99.16% / 99.7 |
| `…916540630588` | 99.93% / 84.9 | 99.79% / 97.7 | 99.23% / 91.4 |
| `final` | 99.93% / 84.9 | 99.79% / 97.7 | 99.23% / 91.4 |

（单元格 = 加权率 / miss·请求⁻¹）

**率在四次运行间稳定在 0.1pp 内**；miss/请求的移动正是块边界落点差异，不是缓存行为差异（§4.1 末）。

### 4.2 一处措辞更正（FC-6）

B 报告 §3.5 写：

```text
三个成员的 prompt 逐轮增量全部恰好 +23 tokens，无一轮减少
```

**独立复算的结果**：

| 成员 | 全部 9 个 delta 的取值 |
|---|---|
| `pilot-large` | `{23}` |
| `pilot-mid` | `{23}` |
| **`pilot-small`** | **`{24}`** |

**`pilot-small` 是 +24，不是 +23。** 另两个成员确为 +23。

**影响**：结论方向（**append-only、无一轮减少**）**不变**——三个成员的 delta 都是**单一固定值**，没有任何一轮减少。但"全部恰好 +23"与数据不符，应改为：

> 每个成员的 prompt 逐轮增量是**一个固定值**（`pilot-large` / `pilot-mid` 为 +23，`pilot-small` 为 +24），9 轮无一轮减少。

**这是 B 的写集**，C 只报差异、不改 B 的文件。

### 4.3 journal 侧的分类复核

用**已冻结的分类器**（`Sample.Classify` + `Summarize`）重放 B 的 11 份 journal，逐项核验排除与分母：全部样本都被**恰好分入一类**（`samples == 各分类之和`），排除项（`first_request` 1 条/臂、`B3-system-tail` 的 1 条 502 + 1 条 retry）逐类计数，**没有样本被静默丢弃**。各臂 rate 与 B 报告逐项相同（§1.5 表）。

---

## 5. 独立 go/no-go（§6 交付物）

| 项 | 判定 | 与合流纪要相比 |
|---|---|---|
| **K1（本 route）** | **GO** | 不变 |
| K1（其他 Provider / 方言） | **未决**（无端点凭据） | 不变 |
| K1 盲点（省略 split 的全命中） | **未决** | 不变 |
| **K3（`cachelab` 解析）** | **GO**，且**冻结版本与当前版本语义同一**（§1） | **加强**：§9.8 关闭 |
| `session_id` + 覆盖率 | **GO** | 不变 |
| 历史账本 per-request 基线 | **不可用** | 不变 |
| 等样本量对比（84.5%/67.7%） | **不可比**（−16.8pp） | 不变 |
| 钟点对齐切片（86.14%/76.51%） | **可复现的观察值**，−9.6pp **不可归因** | **加强**：数字现在可复现 |
| 行数口径（1973/1964、1714/1713、174/169） | **两个口径都正确**，不撤回 | **关闭**：§3.1 的"未消解" |
| B 的 pilot 数字 | **可独立复现**；§3.5 的"+23"需更正为"+23/+24" | **新增** |
| **缓存行为优化候选** | **全部 NO-GO** | 不变 |

**未触发计划 §6 的任何停止条件**：账本未被改写（md5 与冻结值相同）、输入摘要可冻结、归档快照完整、解析器改动对已有 journal 的分类**未改变**（重放逐项相同）。

**未触发方案 §6.3 的任何阻断条件**：无 Provider 误折叠回归、无缺失/显式零的确定性误判、私有 oracle 未扩大为契约、无 provenance 缺口被当作单请求、无低命中样本被用于改压缩/工具/请求构造、无正文或凭据进入日志。

---

## 6. 复现清单

```bash
# 冻结核对
md5sum internal/cachelab/usage.go internal/provider/anthropic/stream_usage.go
md5sum ~/.reasonix/stats/2026-09-23.jsonl ~/.reasonix/stats/2026-09-24.jsonl
md5sum ~/reasonix-partb-archive/2026-09-24/cachelab-usage.go.snapshot

# 快照语义等价（去注释后逐字节比较）
diff <(grep -v '^\s*//' ~/reasonix-partb-archive/2026-09-24/cachelab-usage.go.snapshot | grep -v '^\s*$') \
     <(grep -v '^\s*//' internal/cachelab/usage.go | grep -v '^\s*$') && echo "semantically identical"

# K3 测试与重放
go test ./internal/cachelab/ -count=1
go test ./internal/cachelab/ -run 'TestParseUsage|TestRecorderReadsTheGatewayShape|TestRecorderLeavesTheOracle' -v
PART_C_JOURNAL_DIR=/tmp/cachelab-runs go test ./internal/cachelab/ -run TestReplayJournals -v -count=1

# 历史账本复算
PART_C_LEDGER_DIR=~/.reasonix/stats go test ./internal/cachelab/ -run TestRecomputeLedgerNumbers -v -count=1

# 门禁
go build ./... && go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...
go run ./tools/repolint
bash scripts/cache-guard.sh
```

**B 的 pilot 独立复算**（只读归档，不跑真实端点）：

```bash
python3 - <<'PY'
import json
recs=[json.loads(l) for l in open("/home/zwc/reasonix-partb-archive/2026-09-24/pilot-records-final.jsonl") if l.strip()]
h=sum(r["cache_hit_tokens"] for r in recs); m=sum(r["cache_miss_tokens"] for r in recs)
print("hit=%d miss=%d rate=%.4f"%(h,m,100*h/(h+m)))   # 1613440 / 171990 / 90.3670
PY
```

---

## 7. 归档与可追溯

| 位置 | 内容 |
|---|---|
| `~/reasonix-partb-archive/2026-09-24/` | M0 冻结记录、两次 pilot 的 records/report/banner/日志、`cachelab-usage.go.snapshot`（**冻结版本**）、`source-md5.txt`、RUNBOOK |
| `~/reasonix-partc-archive/2026-09-24/` | 上一轮 C 的 51 个探针产物 |
| `/tmp/cachelab-runs/` | B 的 11 份 journal（已由上一轮 C 归档） |

**本轮未新增归档**：复算逻辑已进入仓库（`internal/cachelab/ledger_recompute_test.go`），不再需要一次性脚本。**账本文件全程只读，md5 与冻结值相同。**

---

## 8. 交下一轮 / 负责人

1. **B 需两处更正**：① pilot 报告 §3.5 的"全部恰好 +23"应改为"每个成员一个固定值（large/mid +23，small +24）"（§4.2）；② §3.1、§3.2 主表、§3.2 复跑列、JSON totals 分别来自三次不同运行，应各自标注 run id，且 §3.2 主表 `pilot-large` 的 warm hit 应为 **1,090,432**（§4.1）。
2. **合流纪要需更新三处**：§3.1 的"复算口径差异未消解"→ 已解释（§2.2）；§9.8 的"摘要不一致"→ 已解释为纯注释（§1）；§3.3 表中 09-23 的共同小时行数应标注口径（174 或 169）。
3. **其他 route 的 K1 验证**仍需端点与凭据；在此之前"未决"不得读成"通过"。
4. **历史差异**：现存账本**结构上**无法归因（§3），未来采样只能建立新的前瞻基线。
5. **1M / 压缩边界**仍无样本（B 的写集）。

---

## 9. 本文未做

- 未修改 Provider 生产语义（`internal/provider/**` 一行未改）。
- 未修改 Team 生产请求观测字段（`internal/team/cacherequest.go` / `cachereport*.go` 未改）。
- 未覆盖、未重写任何账本文件；`~/.reasonix/stats/*.jsonl` 全程只读。
- 未修改 B 的归档或报告；未修改 A 的任何文件。
- 未把 journal、成员记录、统计账本合并计算。
- 未提交、未推送、未开 PR。
