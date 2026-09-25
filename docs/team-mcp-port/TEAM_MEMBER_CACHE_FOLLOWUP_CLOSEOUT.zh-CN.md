# Team Member 缓存计量主线：闭环记录（P4 后续合流）

> 角色：负责人（合流）。日期：2026-09-25。
> 依据：`TEAM_MEMBER_CACHE_FOLLOWUP_EXECUTION_PLAN.zh-CN.md` §7（合流门禁）、§8（总体完成定义）、§9（派单摘要）。
> 三方交付：A `TEAM_MEMBER_CACHE_FOLLOWUP_A_PROVIDER_VALIDATION.zh-CN.md`；B `..._B_PREREGISTRATION` + `..._B_REPORT`；C `..._C_AUDIT`。
> **本文只做记录、判定与对账；兜底复核对 B 的 route/account 漂移作出正式性降级，并对文件统计与门禁结论作更正。**

## 0. 闭环结论

| # | 问题 | 判定 |
|---|---|---|
| 1 | 所有已声明支持的 Provider/route 有独立判定？ | **是。** 7 行逐 route 判定：本 route **GO**；OpenAI chat / Responses **有限通过**（无端点观测）；native Anthropic / LongCat / 官方 DeepSeek **未决**（无凭据）；其他兼容端点 **未决**。**无任何泛化声明。** |
| 2 | Team 正式分层报告满足预注册门槛？ | **否（正式门禁未满足）。** 六个 strata 臂实际使用 `strata-gw` / `anthropic/f119dfdb4214`，而预注册冻结的是 `deepseek-v4-flash-roojin` / `anthropic/bfcb0811b1c8`；204 条记录可审计且可复现，但只能作为探索性基线。四桶样本量、3 成员、3 session、`gte_1m` 与 fold 边界仍按报告如实披露。 |
| 3 | 历史账本行数与切片数字统一可复现？ | **是。** 差异已由一条明确规则解释（"前缀匹配全部行" vs "其中带 `prompt` 键"），两口径加权率相同，**不撤回任何精确数字**；复算逻辑已进仓库。 |
| 4 | K3 冻结快照与当前测试版本对应关系可追溯？ | **是。** 差异仅一处字段注释，去注释后**逐字节相同**；当前版本已重跑全部 K3 测试与 journal 重放。 |
| 5 | 最终变更审阅完成、工作树归属清楚、提交/发布状态如实声明？ | **是。** 见 §4；**未提交、未推送、未开 PR**（按 §8.5，提交由负责人按项目流程单独执行）。 |

**一句话**：证据审计与 A/C/K1/K3 收尾已闭环，但主线整体**不能标记为完全闭环**。K1/K3 在已测 route 通过，B 建立了 68× prompt 的探索性分层基线；由于预注册 route/account 漂移，正式 Team 分层门禁未满足，历史归因仍未决，跨 Provider 与结构性不可达项继续保留边界，缓存行为优化全部 NO-GO。

## 1. §8 总体完成定义 —— 逐条核验

| §8 要求 | 状态 | 证据 |
|---|---|---|
| 1. 所有已声明支持的 Provider/route 有独立判定；未验证 route 明确保留未决且没有泛化声明 | ✅ | A §5 的 7 行判定表 |
| 2. Team 正式分层报告满足预注册门槛，或对客观不可达的层明确给出阻断与边界；1M/压缩边界结论不得由低上下文 pilot 推导 | ⚠️ **正式门禁未满足** | B 报告 §0、§1、§4.1、§4.2；实际 route/account 与预注册漂移，数据降级为探索性基线 |
| 3. 历史账本行数与切片数字统一可复现，或原精确数字已撤回并附明原因；没有伪称重建历史因果 | ✅ | C §2.2 + 本记录 §2.2；合流纪要 §3.1 的"未消解"已关闭 |
| 4. K3 冻结快照与当前测试版本对应关系可追溯，相关测试通过 | ✅ | C §1；本记录 §2.3 独立复算 |
| 5. 最终变更审阅完成，工作树归属清楚，提交/发布状态在发布记录中如实声明 | ✅ | §4；**未提交/未推送/未开 PR** |

## 2. 独立复算（负责人，非任一方产物）

### 2.1 B 的分层数字

从 B 归档的 6 个臂记录独立求和：

| 臂 | ctx | warm n | hit | miss | 加权率 | miss/请求 |
|---|---:|---:|---:|---:|---:|---:|
| S1a-small | 11,725 | 33 | 388,608 | 2,871 | 99.27% | 87.0 |
| S1b-mid | 192,674 | 33 | 6,359,808 | 2,988 | 99.95% | 90.5 |
| S1c-large | 592,450 | 33 | 19,552,512 | 2,892 | 99.99% | 87.6 |
| S4-768k | 792,340 | 21 | 16,639,488 | 1,640 | 99.99% | 78.1 |
| S3-c2 | 11,723 | 33 | 388,608 | 2,871 | 99.27% | 87.0 |
| S3-c3 | 11,723 | 33 | 388,608 | 2,805 | 99.28% | 85.0 |
| **合计** | — | **186** | **43,717,632** | **16,067** | **99.96%** | **86.4** |

**与 B 报告 §2.1 / §3.2 逐项相同。** 六个臂各只运行一次（每臂 1 个 records 文件），因此**不存在** pilot 那种跨运行混表的可追溯性问题。

### 2.2 历史账本的行数口径（复核 C FC-2）

独立统计 `~/.reasonix/stats/`：

| 口径 | 09-23 | 09-24 | 共同小时 09-23 | 共同小时 09-24 |
|---|---:|---:|---:|---:|
| 模型前缀匹配全部行 | **1,973** | **1,714** | **174** | **633** |
| 其中带 `prompt` 键 | **1,964** | **1,713** | **169** | **633** |
| 加权率（两口径相同） | 83.83% | 67.70% | 86.14% | 76.51% |

**差额的来源已逐行确认**：09-23 的 9 行与 09-24 的 1 行**不含 `prompt` / `cache_hit` / `cache_miss` 键**（其键集只有 `ts/model/source/requests/completion/reasoning/total/cost_*/display_*/incomplete_reason`），不贡献任何 token。

**判定：C 的 FC-2 成立。** 两个数字**都正确**，是同一批行的两个口径。**不撤回任何精确数字**，但引用时必须注明口径。合流纪要 §3.1 的"未消解"与 §3.3 的"不作为独立验证"已据此关闭。

### 2.3 K3 快照语义等价（复核 C FC-1）

```bash
diff <(grep -v '^\s*//' <snapshot> | grep -v '^\s*$') \
     <(grep -v '^\s*//' internal/cachelab/usage.go | grep -v '^\s*$')
→ 无差异（语义等价）
```

- 全文差异块数：**1**，内容为 `ReportedUsage.Oracle*` 的字段注释（9 行 → 3 行）。
- 两份文件的块注释 `/* */` 与反引号字符串均为 **0**，因此**去注释比较可靠**。
- 代码行**零改动**。

**判定：C 的 FC-1 成立。** 当前文件是冻结版本的**语义等价物**，§9.8 关闭。

### 2.4 C 对 B 报告的两处发现（逐项独立确认）

| C 的发现 | 独立核验 | 判定 |
|---|---|---|
| §3.5 "三个成员全部恰好 +23" | 四个归档运行：`pilot-mid` / `pilot-large` **始终 +23**；`pilot-small` 前两次 **+23**、后两次（含 `final`）**+24** | **成立**，已更正为"每个成员一个固定值" |
| §3.2 主表 `pilot-large` warm hit 印 1,093,432 | 四个归档运行**全部**为 **1,090,432**（差 3,000） | **成立**，已按记录值更正 |
| §3.1、§3.2 主表、§3.2 复跑列、JSON totals 来自三次不同运行 | 逐表比对：§3.1 → `…492890560284`；§3.2 主表 → `…655106980265`；§3.2 复跑列 → `…916540630588`；JSON totals → `…916540630588` | **成立**，已补 run id 标注 |
| pilot-large 的 `1,090,432` 是否曾被印成 `1,093,432` 之外的其他值 | 从未；四运行一致 | 转写笔误，非数据差异 |

**结论方向全部不变**（append-only、无一轮减少、warm 99.2–99.9%）。B 报告的**已更正版本**见 `TEAM_MEMBER_CACHE_PART_B_PILOT_REPORT.zh-CN.md`（三处更正已就地标注日期与依据）。

### 2.5 B 报告的一处范围收紧（负责人发现）

B 报告 §0 的 B-2 与 §2.3 原写"**120/120** 个 warm 样本"成立不变量，但 **120 只是上下文四臂（33+33+33+21）的样本数**，而 §2.3 的结论句同时声称覆盖"3 个并发档"。独立复算：**六个臂共 186 个 warm 样本，186/186 成立**。

**已更正**为 `186/186（其中上下文四臂 120/120）`。**结论方向不变**，只是把已成立的事实说完整。

### 2.6 兜底复核：预注册 route/account 漂移

独立读取 `~/reasonix-partb-archive/2026-09-24/` 的六个 strata records：共 **204 条记录、18 个 member、18 个 session**，每个 member 一个 session；204/204 的 `route_bucket` 都是 `anthropic/f119dfdb4214`，`model_ref` 都是 `strata-gw/deepseek/deepseek-v4.1-flash[1m]`。代码驱动也显式构造 `AgentUserRef/UserID = strata-gw`。

预注册冻结值是池条目 `deepseek-v4-flash-roojin`、route `anthropic/bfcb0811b1c8`。因此 B 的数字具备真实记录、审计覆盖和可复算性，但没有满足同一 route/account 的预注册条件，不能继续标作正式因果对照或正式门禁通过。

## 3. 三方对账

| 项 | A | B | C | 负责人判定 |
|---|---|---|---|---|
| 本 route K1 | GO（3 轮 0 违反，无 oracle 依赖） | 未直接测 | GO（4 warm 0 违反，另一实现） | **一致，独立实现互证** |
| 其他 route | 未决 / 有限通过 | — | 未决 | **一致** |
| 128 块对齐 | 未直接测 | 186/186（真实成员） | 裸请求 + 步进 + 历史账本 | **四条路径一致** |
| 历史账本 per-request 基线 | 不可用 | 不可比 | 不可用（结构性） | **一致** |
| 历史 −16.8pp | — | — | 撤回为不可比；对齐 −9.6pp 不可归因 | **一致** |
| 并发字段 | — | 臂登记承载（独立审计后同意） | 拒落（store 锁 + 发明 provenance） | **一致** |
| oracle 在成员侧 | 不需要（判据在事件流） | 无该字段 | 不补（私有扩展不升为契约） | **一致** |
| 缓存行为候选 | 无 | 无 | 无 | **一致，全部 NO-GO** |

**三方数据面互不相加**：A 的矩阵与端点探针、B 的成员记录与 strata 归档、C 的 journal 与账本复算，**三套口径独立**，未发生合并。

## 4. 最终变更审阅与工作树归属

| 文件 | 归属 | 状态 |
|---|---|---|
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_A_PROVIDER_VALIDATION.zh-CN.md` | A | 新增 |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION.zh-CN.md` | B | 新增 |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_B_REPORT.zh-CN.md` | B | 新增（兜底复核补充 route/account 漂移与探索性降级） |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_C_AUDIT.zh-CN.md` | C | 新增 |
| `internal/cli/live_team_cache_strata_test.go` | B | 新增（`-tags live`） |
| `internal/provider/openai/usage_shape_test.go` | A | 新增 |
| `internal/provider/responses/usage_shape_test.go` | A | 新增 |
| `internal/provider/anthropic/usage_convention_live_test.go` | A | 新增（`-tags live`） |
| `internal/cachelab/ledger_recompute_test.go` | C | 新增 |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_MERGE_RECORD.zh-CN.md` | 负责人 | 修改（关闭 §3.1、§9.8） |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_PART_B_PILOT_REPORT.zh-CN.md` | B | 修改（三处更正，已标注） |

**核验**：

- **无任何生产代码被改动**：`git diff` 对已跟踪文件**只有两份文档**（合流纪要、pilot 报告），`internal/**` 的已跟踪文件**零改动**。
- 所有新增 `internal/**` 文件都是**测试**（`_test.go`），其中两个是 `-tags live`，**不进生产二进制**。
- `tools/repolint/baseline.json` **未改动**。
- 工作树实际为 **2 份已修改文档 + 5 份新增文档 + 5 份新增测试**；`FOLLOWUP_CLOSEOUT` 也属于新增文档，不能从新增文件统计中漏掉。

## 5. 合流门禁（本机实测，2026-09-25）

| 检查 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...` | **干净** |
| `go vet -tags live ./internal/cli/ ./internal/cachelab/ ./internal/provider/anthropic/` | **干净** |
| `go test ./internal/provider/... ./internal/agent/ ./internal/team/ ./internal/stats/ ./internal/cachelab/` | **全绿**（provider 4 包 / agent 50.3s / team 6.2s / stats / cachelab） |
| `go test ./internal/cli/ -run 'Cache\|Usage\|Team' -count=1` | **全绿**（17.1s） |
| A 的 openai 形状矩阵 | **11/11 子测试通过** |
| A 的 responses 形状矩阵 | **8/8 子测试通过** + 2 个定向测试 |
| `PART_C_LEDGER_DIR=~/.reasonix/stats go test ./internal/cachelab/ -run TestRecomputeLedgerNumbers` | **通过**（只读账本） |
| `bash scripts/cache-guard.sh` | **通过**（`TestReleaseCacheHitGuard` 10/10 `status=pass`；`TestBootStableExtensionCacheGuard` 通过） |
| `go run ./tools/repolint` | **clean (1146 baselined findings)** |
| 账本完整性 | `2026-09-23/24.jsonl` md5 与 M0 冻结值**逐字节相同**，全程只读 |

### 5.2 兜底复跑（2026-09-25）

| 命令 | 结果 |
|---|---|
| `go test ./internal/provider/anthropic/ ./internal/team/ ./internal/cachelab/ -count=1` | **通过** |
| `go test ./internal/provider/openai/ ./internal/provider/responses/ ./internal/provider/anthropic/ -count=1` | **通过** |
| `go test ./internal/cli/ -run 'Cache\|Usage\|Team' -count=1` | **通过** |
| `git diff --check` | **通过** |

正确 route 的真实重跑本次无法进行：当前环境没有 `REASONIX_LIVE_CACHE_*` 采样配置，且 `ANTHROPIC_*`、`OPENAI_API_KEY`、`DEEPSEEK_API_KEY`、`LONGCAT_API_KEY` 均未设置。没有使用或生成替代凭据。

### 5.1 阻断条件核验（方案 §6.3 + 后续计划 §7）

| 阻断条件 | 核验 |
|---|---|
| K1 在任一已支持 Provider 上误折叠/归一化回归 | 未触发（本 route 4 判据 0 违反；其他 route 未验证且已标未决） |
| 缺失与显式零被错误确定化 | 未触发（A 的 F-A7 确认三条路径一致：不 clamp，交下游具名排除） |
| 私有 oracle 扩大为跨 Provider 契约 | 未触发（oracle 只在 `internal/cachelab`；成员记录与账本均无；`cachelab` 不被任何非测试文件导入） |
| Team 观测缺 provenance 仍当单请求 | 未触发（strata 全部 `observed`；账本已降级） |
| 低命中样本不足却据此改压缩/工具/请求构造 | 未触发（生产请求构造零改动；无候选准入） |
| 请求正文/工具结果/凭据进入日志 | 未触发（strata 归档 24 个文件扫描 0 命中） |
| 任何优化导致质量/上下文保留/错误率恶化 | 不适用（本轮无优化落地） |
| 历史账本缺失/被改写、输入摘要无法冻结 | 未触发（md5 与冻结值相同） |
| 归档快照不完整或解析器改动改变既有 journal 分类 | 未触发（重放逐项不变；快照语义等价） |

## 6. 正式 go/no-go（闭环版）

| 项 | 判定 |
|---|---|
| **K1（本 route）** | **GO** |
| K1（native Anthropic / LongCat / 官方 DeepSeek） | **未决**（无凭据） |
| K1 盲点（省略 split 的全命中） | **未决**（未观察到；下游按"未报告 cache read"措辞） |
| OpenAI chat / Responses | **有限通过**（形状矩阵固定；无端点观测） |
| **K3** | **GO**（冻结版本语义等价，测试与重放通过） |
| **B 正式预注册实验** | **BLOCKED / UNRESOLVED**（六个 strata 臂的 route/account 与预注册冻结值漂移；现有 204 条记录仅作探索性基线） |
| `session_id` + 覆盖率 | **GO** |
| 历史账本 per-request 基线 | **不可用** |
| 等样本量对比（84.5%/67.7%，−16.8pp） | **撤回为不可比** |
| 钟点对齐切片（86.14% vs 76.51%，−9.6pp） | **可复现的观察值；不可归因** |
| 行数口径（1973/1964、1714/1713、174/169） | **两口径都正确，不撤回** |
| Team 分层基线 | **前瞻探索性描述基线**（四桶，`768k_1m` 已采到；独立单元仅 3 session；route/account 偏离预注册） |
| `gte_1m` | **不可执行**（结构性） |
| 压缩/fold 边界 | **不可执行**（结构性：估算重标定后低于触发线） |
| **缓存行为优化候选** | **全部 NO-GO**（没有满足同 route、同账号池、单变量、任务质量/上下文/延迟/成本护栏的真实 Provider 对照证据） |

### 6.1 灰度与回滚

与合流纪要 §5.1 / §5.2 一致，**无变化**：K1 与 `session_id` 无"限单 member"灰度语义（影响面是上报口径）；K3 不进生产二进制。回滚路径逐项不变。

### 6.2 版本切换公告

合流纪要 §6 的六条**仍然适用**，其中第 5 条已按 §2.2 更新为：

> 5. 历史 84.5%/67.7% 的对比**撤回为不可比**（两天窗口不重叠）。共同钟点切片为 **86.14% vs 76.51%**，**行数须注明口径**（前缀匹配 174/633，带 `prompt` 键 169/633），**−9.6pp 为观察值、不可归因**。

## 7. 剩余未决（明确移交；不等同于主线完全闭环）

1. **其他 route 的 K1 端点验证**：native Anthropic / LongCat / 官方 DeepSeek / OpenAI 直连均需凭据。在此之前"未决"**不得**读成"通过"。
2. **历史归因**：现存账本**结构上**无法归因（无 route/账号/成员/session/任务族/原始 wire/oracle 字段）。未来采样只能建立新的前瞻基线，**不能**称历史重跑。
3. **`gte_1m` 与 fold 边界**：本配置下结构性不可达。若将来需要样本，须**另行预注册**改变窗口或任务族的实验（不得同时改动两个变量）。
4. **跨成员推断**：独立单元只有 3 个 session。需要更多成员/更多独立会话才可能做总体推断。
5. **并发逐请求信号**：未找到不取 store 锁的运行时并发信号（结构性缺口）；当前以臂登记承载。
6. **oracle 在场性的历史不可考**：账本不保留原始 usage。
7. **成本口径**：价格未配置，全部实验只能给 token 量（B 本轮 48.6M），不能给 USD。
8. **B 正式重跑条件**：必须在预注册冻结的池条目 `deepseek-v4-flash-roojin` 与 route `anthropic/bfcb0811b1c8` 上重新执行，或先建立新的预注册版本并重新冻结；不得直接把 `strata-gw` / `anthropic/f119dfdb4214` 的探索性数据升格为正式结果。当前环境缺少实时采样配置与 Provider 凭据，故本轮不能完成重跑。

## 8. 归档

| 位置 | 内容 |
|---|---|
| `~/reasonix-partb-archive/2026-09-24/` | M0 冻结记录（含 ADDENDUM）、两次 pilot、六个 strata 臂的 banner/records/report/日志、源码快照与摘要、RUNBOOK |
| `~/reasonix-partc-archive/2026-09-24/` | C 的 51 个探针产物 |
| `/tmp/cachelab-runs/` | B 的 11 份 journal（已由 C 归档，`/tmp` 非唯一副本） |

**隐私**：strata 归档 24 个文件对 `sk-*` / `api_key` / `bearer` / `authorization` / brief 正文 / 任务标记词的扫描**全部 0 命中**。

## 9. 提交与发布状态（如实声明）

- **未提交、未推送、未开 PR。**
- 工作树包含：2 份修改的文档、5 份新增文档、5 份新增测试（见 §4）。
- 按计划 §8.5：「提交、推送和发布须由负责人按项目流程单独执行，不由此计划自动触发」。**本记录不构成提交或发布授权。**

## 10. 本文未做

- 未改动原始观测数字、归档记录或 A/C 的技术判定；对 B 报告补充 route/account 漂移证据并下调正式性，属于兜底审计结论。
- 未修改任何生产代码、观测字段、统计口径或请求构造。
- 未覆盖、未重写任何账本文件。
- 未提交、未推送、未开 PR。

## 11. 兜底结论

本轮可以关闭的是**证据整理、独立复算、A/C/K1/K3 合流和风险门禁审计**。不能关闭的是**严格预注册条件下的 Team 正式分层、历史残差根因和任何缓存行为优化准入**。因此最终状态为：**证据审计闭环；研究主线部分闭环；优化发布明确 NO-GO**。
