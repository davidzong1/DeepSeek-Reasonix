# Team Member 缓存命中率：三方合流纪要与准入记录（M0/M1 + P4）

> 角色：负责人（合流）。日期：2026-09-25。
> 依据：`TEAM_MEMBER_CACHE_POST_JOINT_3AGENT_PLAN.zh-CN.md` §4（M0/M1 冻结）、§5（P4 合流）、§6（门槛）、§8.1（交付物）。
> 三方交付：A `TEAM_MEMBER_CACHE_PART_A_PROVIDER_USAGE_MATRIX.zh-CN.md`；B `TEAM_MEMBER_CACHE_PART_B_P0_INVENTORY.zh-CN.md` + `..._PART_B_PILOT_REPORT.zh-CN.md`；C `TEAM_MEMBER_CACHE_PART_C_FINAL_REVIEW.zh-CN.md`。
> 状态：**本轮 P4 合流记录完成；主线整体未闭环。** 未决验证与正式采样转下一阶段；本文记录的是当前工作树的审查结果，不代表已提交或发布。
> **本文只做记录与判定，不改任何一方的代码或数据。**

## 0. 合流结论（五个必答问题）

方案 §5 阶段 P4 要求合流时分别回答五个问题。逐条：

| # | 问题 | 判定 | 依据 |
|---|---|---|---|
| 1 | K1 是否按 **Provider/事件形状**通过，而非仅按一个网关通过？ | **部分通过。** 本 route（DeepSeek 兼容自定义网关）**通过**；native Anthropic / LongCat / 官方 DeepSeek 由 A 的表驱动测试覆盖但**无端点凭据**，**未决**；OpenAI 直连与 `responses` 方言**未审**。 | A §6；C §8.1 |
| 2 | K1 后真实 Team member 的命中率、miss/request、usage coverage 是多少？ | **warm 99.16–99.93%（按成员）**，**miss/请求 83.9–99.7**；usage coverage **30/30 observed**、诊断 30/30、route 30/30。**样本不足，全部 `insufficient_sample`。** | B §3 |
| 3 | 09-23/09-24 的历史差异能否可信重算？ | **不能。** 两天账本窗口**不重叠**（09-23 覆盖 00–01h 与 17–23h；09-24 只覆盖 00–07h），等样本量对比是"晚间比早晨"。按钟点对齐后为 **86.14% vs 76.51%（−9.6pp）**，不是 −16.8pp。 | C §4.1，本纪要 §3 独立复算 |
| 4 | 观察到的差异是层内变化、任务组成变化、样本选择变化，还是仍无法解释？ | 原始 −16.8pp **因时间窗口不匹配而不可比较**。共同钟点切片观测到 −9.6pp，但样本和 provenance 不足，**不能把两者差值 7.2pp 解释为窗口错配的因果贡献**；残差未决。 | C §4.1–4.2 |
| 5 | 是否存在足够证据实施新的缓存行为优化？ | **否。无任何缓存行为候选准入。** | C §9；本纪要 §5 |

**一句话**：本 route 的 K1 和 K3 已有本轮验证，真实成员 pilot 建立了描述性前瞻基线；但跨 Provider 验证与正式分层样本未完成。历史 −16.8pp 比较因窗口不匹配而撤回；对齐切片的 −9.6pp 只作观察值，不能归因。

## 1. M0 冻结记录（合流时补齐）

| 项 | 值 |
|---|---|
| 基线 commit | `7bda0c3d32d2`（`Team-agent-merge-mainv2`） |
| K1 是否在待测构建内 | **是**，已由 `5241384b6` 提交 |
| 折叠规则冻结 | `internal/provider/anthropic/stream_usage.go` md5 **`71b7d3b79a4c6dfc74d1357b310190ff`**（B 的 pilot 与 C 的 live 复核都记在这个摘要上） |
| 夹具解析冻结 | `internal/cachelab/usage.go` md5 `cc0cda4fcbd22bd7f68602d376656865`（C 的 K3 落地版本；与 B 归档 `cachelab-usage.go.snapshot` 一致。当前工作树文件摘要不同，见 §9） |
| 网关 / 账号 | `aiapi.lejurobot.com`，池条目 `deepseek-v4-flash-roojin` |
| route bucket | `anthropic/bfcb0811b1c8` |
| 线上 model | `deepseek/deepseek-v4.1-flash`（池内拼写带 `[1m]`，由 `team.ResolveAgentUserModel` 剥离） |
| 主指标 | `Σhit / (Σhit + Σmiss)`，仅有效单请求；`miss_tokens_per_request` 必须并列 |
| oracle 定位 | `billing_usage.openai_usage` 是**可选、私有**旁证，**永不**进入主读数 |
| 隐私规则 | 不落 prompt、工具正文、凭据、完整响应 |

**冻结期间发生并被记录的两件事**（都未使基线失效，理由如下）：

1. **A 的测试曾红过一次**（5 项，2026-09-24 21:05）。**根因**：A 把 usage 约定从 `c.deepseek`（推理回放义务）拆成独立的 `inclusiveUsage`（记账约定），A 自己的测试尚未同步；A 随后修复，合流时 `go test ./internal/provider/anthropic/` **全绿**。
   **对基线的影响：无。** 两条独立证据：① pilot 记录全部满足 `prompt == hit + miss` 且 warm 的 `miss > 0`、`prompt > hit`，inclusive 约定对 remainder 形状的流会给出 `miss = 0`、`prompt = read`，与实测不符 → pilot 走 remainder 约定；② A 的最终默认 `inclusiveUsage := openai.IsDeepSeek(root)`，而 `IsDeepSeek` 只匹配 `deepseek.com` 主机（`internal/provider/openai/host.go:36`），本路由**不匹配** → 仍是 remainder 约定。**前后同一约定。**
2. **C 在合流窗口内落地了 K3**（`cachelab` 解析器重写）。K3 **不进生产二进制**（`internal/cachelab` 只被 `-tags live` 的测试导入，本纪要 §4 已核验），且 C 用 B 的 11 份 journal 重放证明**臂结果逐项不变**（本纪要 §3 已独立复跑）。

## 2. M1 接口字段冻结（三方最终状态）

| 契约字段 | 落位 | 状态 |
|---|---|---|
| `provider` / `route` / `model` / `usage_source` / `raw_event_shape` | A 侧 | ✅ A 的 17 行形状矩阵 + `raw_event_shape` 由 journal 的 `usage_keys`/`usage_shape` 承载 |
| `cache_split_present` | 两侧 | ✅ 成员侧 `hit+miss > 0`；journal 侧 `usage_split` |
| `billing_oracle_present` / `oracle_match` | **仅 journal 侧** | ✅ `usage_oracle_present/agrees`。**成员侧明确不补**（C §5.5：私有扩展不得提升为观测契约），永久标 `insufficient_provenance` |
| `semantic_status` | A 侧 | ✅ 通过 / 有限通过 / 未决 / 阻断，按 Provider 分列 |
| `team_id` / `member_id` / `turn_id` / `request_seq` | 成员记录 | ✅ 30/30 |
| **`session_id`** | 成员记录 | ✅ **C 已落地**（`internal/team/cacherequest.go`），带三条语义约束 + `coverage.session_present/absent` |
| `request_count` / `request_count_source` | 成员记录 | ✅ 闭集 `observed`/`defaulted`/`unrecorded` |
| `context_prompt_tokens` / `prompt` / `hit` / `miss` / `cache_write` | 成员记录 | ✅ |
| `route_bucket` / `model_ref` | 成员记录 | ✅ |
| **`maintenance_phase`** | **派生，不新增字段** | ✅ 由 `team.CacheRequestStages` 从 `PrefixChangeReasons` 派生 |
| **`concurrency_level`** | **拒落** | ⚠️ C §5.2：唯一候选 `busyMembers()` 会在观测路径上取 store 锁，违反"provider 请求永不等待遥测"；**改由臂登记承载，分层以臂为单位** |
| **`attempt`** | **拒落** | ⚠️ C §5.3：事件上没有逐 attempt 身份；新造会**发明 provenance**。用现有 `RequestCount > 1` + `RequestCountSource` 表达重试关系 |
| **`task_family`** | **驱动方登记，不进记录** | ✅ 代码里不存在该概念；B 的 pilot 登记为 `long-context-single-word-recall` |
| `request_shape_digest` | 近似 | ⚠️ `prefix_hash` / `stable_prefix_hash` 只作**客户端差分**，**不是** provider cache key |
| `evidence_level` / `inclusion_class` / `exclusion_reason` / `coverage` / `next_action` | C 侧 | ✅ `CacheReportExclusions` + `CacheReportCoverage` + 各 stratum 的 `insufficient_sample` 标签 |

**判定：M1 冻结完成。** 四个字段被**有理由地拒落**，而不是被默默填上近似值——这符合方案 §4「任何缺字段的样本保留为 unknown/coverage 缺口，不自动填 0 或 1」。

## 3. 独立复算（负责人，非任一方产物）

### 3.1 历史账本的窗口错配（复核 C §4.1）

用 `~/.reasonix/stats/` 的原始行独立统计（`deepseek-v4-flash-roojin` 过滤）：

| 日期 | roojin 行 | 覆盖钟点 | 全样本加权率 |
|---|---:|---|---:|
| 09-23 | 1,973 | `00,01,17,18,19,20,21,22,23` | 83.83% |
| 09-24 | 1,714 | `00,01,02,03,04,05,06,07` | 67.70% |

> **复算口径差异未消解**：C §4.1 报告 09-23/09-24 的过滤行数为 1,964/1,713，共同钟点切片为 169/633；本次独立汇总为 1,973/1,714 与 174/633。账本原文件仍在，但当前记录没有保存两次计算所用的完整筛选脚本/排除清单，因此不能确认哪一组行数是各自口径下的最终集合。下列时段率和命中率只能视为已有报告值，行数差异解决前不作为完全复现的独立验证。

**已有报告的钟点对齐值（只取两天共同覆盖的 00h/01h）**：

| 切片 | n | 加权率 |
|---|---:|---:|
| 09-23 `00h+01h` | 174 | **86.14%** |
| 09-24 `00h+01h` | 633 | **76.51%** |
| 差 | — | **−9.6pp** |

**逐钟点率（独立复算，与 C §4.1 逐项相同）**：

```text
09-23  00h 83.4  01h 87.3  17h 68.1  18h 88.3  19h 88.4  20h 89.8  21h 91.9  22h 74.8  23h 79.0
09-24  00h 76.0  01h 76.9  02h 69.7  03h 66.3  04h 60.8  05h 59.7  06h 60.6  07h 54.4
```

**判定：C-5 成立。** `--first 1714` 对应 09-23 的大部分全天记录（其中含大量晚间样本）与 09-24 的早间记录，两个样本窗口不匹配。**−16.8pp 撤回为不可比。** 由于 §3.1 的计数口径差异，本纪要不把 1,169/1,964 等进一步用作独立复算证据。

**判定：C-6 成立。** 已报告的对齐切片差为 9.6pp，但切片行数在报告间存在 169/174 的差异，且账本无 provenance（`measured 0 of 1714`）、无 route/账号/成员/session/任务族字段，**不足以升级为结论**。正确表述是「历史不可比 + 观察到的残差未决」，不能把 7.2pp 归因于窗口错配。

### 3.2 另一 route 的 `hit == prompt` 指纹（复核 C §4.3，含一处日期更正）

独立统计全部 9 月账本（全部 model）：

| model | 行数 | `hit == prompt` 行 | 占比 |
|---|---:|---:|---:|
| `deepseek-v4-flash-roojin/…v4.1-flash[1m]` | 5,171 | 3 | 0.1% |
| `deepseek-v4-flash/…v4.1-flash[1m]` | 594 | **380** | **64.0%** |
| `deepseek-v4-flash/…v4-flash[1m]` | 653 | 0 | 0.0% |
| `wan-gpt-5.6/…`（各拼写） | 1,007 | 0 | 0.0% |

**更正**：C §4.3 把 380 行记为「09-22」。独立核验显示该 model 的 594 行分布在 **09-01 至 09-22 的多个日子**（09-22 当天只有 552 行属于 `deepseek-v4-flash` 家族，非全部 594）。**结论方向不变**——这是另一 route、另一账号、另一时段的机制旁证，**不得与本 route 合并**。

### 3.3 块对齐不变量的跨路径一致性

| 路径 | 样本 | `hit % 128 == 0` | `miss ≡ prompt (mod 128)` |
|---|---:|---|---|
| C：裸请求 11 份 journal | 11 臂 | ✅ | ✅ |
| C：8 点步进扫描 | 8 | ✅ | ✅ |
| **B：真实成员路径（pilot run 1）** | **27 warm** | **27/27** | **27/27** |
| **B：真实成员路径（pilot run 2）** | **27 warm** | **27/27** | **27/27** |
| **C：历史账本** | **3,665** | ✅（gcd = 128） | ✅ |

**判定**：128 块对齐**在四条互不相同的路径上独立成立**（裸请求 / 真实成员 / 历史账本 / 步进扫描）。这使 C 的 §2 归纳从"单网关单路径归纳"升级为"跨路径一致"，**但仍未跨账号、未覆盖 768K+、未在并发下验证**——这三条未决保持。

## 4. 合流门禁（本机实测，2026-09-25）

| 检查 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...` | **干净** |
| `go test ./internal/provider/... ./internal/agent/ ./internal/team/ ./internal/stats/ ./internal/cachelab/` | **全绿**（provider 4 包 / agent 51.9s / team 7.6s / stats / cachelab） |
| `go test ./internal/cli/ -run 'Cache\|Usage\|Team' -count=1` | **全绿**（15.8s） |
| `go test ./internal/cli/`（全量） | **全绿**（108.1s） |
| `go test ./internal/provider/anthropic/ -run TestUsage` | **全绿**（35 项，A 的矩阵 + 定向测试） |
| `go test -tags live ./internal/cachelab/ -run TestLiveUsageFoldVerification` | **通过**（4 warm 轮，**0 违反**；adapter == served 逐轮成立） |
| `PART_C_JOURNAL_DIR=/tmp/cachelab-runs go test ./internal/cachelab/ -run TestReplayJournals` | **通过**；11 份 journal 重放，各臂 rate 与 B 报告**逐项相同** |
| `bash scripts/cache-guard.sh` | **通过**（`TestReleaseCacheHitGuard` 10/10 `status=pass`；`TestBootStableExtensionCacheGuard` 通过） |
| `go run ./tools/repolint` | **clean (1146 baselined findings)**；`tools/repolint/baseline.json` **未改动** |
| `desktop/`（独立 module） | **build 通过**；`sdk/` 无 Go 包 |
| 敏感内容 | 候选 diff 全量扫描 `sk-*` / `api_key` / `bearer` / `ANTHROPIC_AUTH_TOKEN=` → **0 命中**；归档目录同样 0 命中 |
| 绝对路径 | 候选 diff 无 `/home/zwc` 硬编码 |

### 4.1 §6.3 阻断条件逐条核验

| 阻断条件 | 核验 | 判定 |
|---|---|---|
| K1 在任一已支持 Provider 上出现误折叠或归一化回归 | 本 route 4 判据 0 违反；native/LongCat 由 A 的表驱动测试固定 | **未触发**（其他 route 标未决） |
| 缺失与显式零无法区分导致误判 | **确认存在**（`wireUsage` 上是 `int`），但下游措辞统一为"未报告 cache read"，未产出错误的生命周期结论 | **未触发**（作为已知限制随结论引用） |
| 私有 `billing_usage` 扩大为跨 Provider 契约 | 该字段**只出现在 `internal/cachelab`**（journal 侧旁证）；**成员记录与账本均无**；`cachelab` **不被任何非测试文件导入** | **未触发** |
| Team 观测缺 provenance 仍把聚合/unknown 当单请求 | 30/30 `observed`；排除分类逐类计数；账本 `measured 0 of 1714` 已降级 | **未触发** |
| 真实 Team 低命中样本不足却据此改压缩/工具/请求构造 | 无任何候选准入；生产请求构造 diff 为空 | **未触发** |
| 请求正文、工具结果或凭据进入实验日志 | 三类扫描 0 命中；`TestObservedRequestsCarryNoContent` 保持绿 | **未触发** |
| 任何优化导致任务质量、必要上下文保留或错误率恶化 | 本轮**无优化落地**；K1 不改请求字节 | **不适用** |

### 4.2 未触碰面的核验

| 面 | 核验 |
|---|---|
| provider-visible 请求构造 | `git diff` 在 `internal/agent` / `internal/boot` / `internal/tool` / `internal/provider/provider.go` / `internal/provider/retry.go` **全为空** |
| 统计分母与排除规则 | `internal/team/cachereport.go`、`cachediagnosis.go` **未改动**；`cacherequest.go` / `cachereport_stats.go` 只**新增**字段与覆盖率计数器 |
| 低命中样本过滤 | 新增的是 `SessionPresent/Absent` 覆盖率与字段注释，**未改任何纳入/排除条件** |
| 临时探针 | A 的 `zz_partc_*` 已删除；C 的 `zz_partc_*` 已删除；`internal/cli/zz_unknown_task_repro_test.go` 是 **HEAD 既有文件**（mtime 09-08，非本轮） |

## 5. 正式 go/no-go

| 项 | 判定 | 依据 |
|---|---|---|
| **K1（`mergeUsage` 跨事件字段混用）** | **GO —— 仅限已测本 route 的形状与边界** | 适配层 == 协议读数 4/4 轮；oracle 一致；无 oracle 的 4 条判据 0 违反；未覆盖的形状仍按 §3/A 标为未决 |
| K1 在其他 Provider | **未决（NO-GO for claim）** | 无端点凭据；**不得**用单网关结果概括 |
| K1 盲点（省略 split 的全命中） | **未决** | 未观察到；下游按"未报告 cache read"措辞 |
| **K3（`cachelab` 解析）** | **GO** | 11 形状表 + 4 定向测试；B 的 11 份 journal 重放逐项不变；**不进生产二进制** |
| `session_id` 字段 | **GO** | 只增字段；旧行 `omitempty` 缺键；覆盖率如实披露 |
| 历史账本 per-request 基线 | **不可用**（维持 A 的 v1 判定） | `measured 0 of 1714` |
| A 的 84.5%/67.7% 等样本量对比 | **撤回为不可比** | §3.1 |
| 历史残差（对齐后 9.6pp） | **未决 / 不可归因** | §3.1 |
| **缓存行为优化候选** | **全部 NO-GO** | 无候选满足 §6.2 的优化准入；K4–K9 状态不变 |

### 5.1 灰度

**K1 与 `session_id` 都没有"限单一 member"的灰度语义**：两者都不改 provider-visible 字节、不改成员隔离、不改上下文策略，影响面是**上报口径**。按 member 灰度只会制造"同一团队两个成员口径不同"的更糟状态。**全量生效即全量正确。**

K3 的灰度面最窄：它只被 `-tags live` 的驱动导入，**不进生产二进制**。

### 5.2 回滚

| 变更 | 回滚 | 影响 |
|---|---|---|
| K1 | 还原 `mergeUsage` 的四行（逐字段 max） | 无状态、无迁移；历史账本**不回填**；数字回到假象值 |
| K1 的约定拆分（`inclusiveUsage` 与 `deepseek` 解耦） | `inclusiveInput()` 改回 `c.deepseek` | 无状态；仅影响 DeepSeek 兼容网关的读数 |
| K3 | 还原 `internal/cachelab/usage.go` 与 `ReportedUsage`/`Sample` 的 oracle 字段 | 仅影响夹具；已有 journal 仍可读（新字段 `omitempty`） |
| `session_id` + 覆盖率 | 删字段与映射行、删两个计数器 | 旧行本就没有该键；无迁移 |
| `internal/cli/team_report_shape.go` 的提取 | 三个 helper 移回 `team_task_service.go` | 纯搬家，无行为变化；会使该文件重新越过 800 行上限 |

## 6. 版本切换公告（必须与 A 的 v1 升版同批发出）

K1 生效时**所有下游消费者会看到数字阶跃**：账本、`team cache-report`、`team cache-audit`、UI 命中率、`.usage.json` 历史。这不是灰度问题，是**版本问题**。公告必须同时说明：

1. **阶跃的方向与量级**：本 route 上 warm 从 ~85% 量级跳到 **~99.9% 量级**（裸请求 4/4、成员路径 6/6 与网关读数一致）。
2. **历史不回填**：`stats-ledger` 的旧行保持原值，**新旧数字不可相减**。
3. **`session_id` 是新增键**：旧行缺键，覆盖率显示 `session=0/N`。
4. **本 route 的 K1 通过不覆盖其他 route**：native Anthropic / LongCat / 官方 DeepSeek / OpenAI 直连 / `responses` 一律标**未决或未审**。
5. **历史 84.5%/67.7% 的对比撤回**。可说明两天窗口不匹配；已有报告的共同钟点切片为 **86.14% vs 76.51%**，但由于计数口径差异未消解，该数值须标为待复核，残差未决。
6. **K1 不改 `miss_tokens_per_request`**：它是**测量修复**，不是缓存改善。不得据此声称"缓存命中率提高"。

## 7. 结果分支判定

| 分支 | 适用 | 说明 |
|---|---|---|
| **A：K1 通过，Team warm 接近 Provider 受控结果** | ✅ **适用（本 route）** | 成员 warm 99.16–99.93%；**不立即改工具 schema 或压缩策略** |
| **B：K1 通过但真实 Team 仍低命中** | ❌ 不适用 | 低命中只出现在**历史账本**，而那批数据**不可比** |
| **C：K1 后命中率提高，但历史跌幅仍存在** | ✅ **适用** | 历史跌幅的**对比方式本身不成立**；残差未决 |
| **D：Provider 语义无法统一或 oracle 不足** | ⚠️ **部分适用** | oracle 在场但不完整；K1 在本 route 有**不依赖 oracle** 的验证路径，故未降级；其他 route 降级为**有限支持/未决** |

## 8. 归档与可追溯

| 位置 | 内容 | 大小 |
|---|---|---|
| `~/reasonix-partb-archive/2026-09-24/` | M0 冻结记录（含 2 份 ADDENDUM）、两次 pilot 的 records/report/banner/日志、源码快照与 md5、RUNBOOK | 336K |
| `~/reasonix-partc-archive/2026-09-24/` | C 的 51 个探针产物（journal、原始 SSE 字节、脚本、审计 JSON） | 412K |
| `/tmp/cachelab-runs/` | B 的 11 份 journal（**已由 C 归档，`/tmp` 不再是唯一副本**） | 268K |

**完整性核验**：操作者账本 `~/.reasonix/stats/2026-09-24.jsonl` 的 md5 仍为 **`61eaaa3a8969b8626156d06e315e9299`**，与 M0 冻结值**逐字节相同**——本轮全部真实实验（B 的 pilot、C 的 live 复核）都运行在测试二进制的一次性 `HOME` 下，**未污染操作者的统计目录**。

## 9. 剩余未决清单（交下一轮）

1. **其他 route 的 K1 验证**：native Anthropic / LongCat / 官方 DeepSeek 需要端点与凭据；OpenAI 直连与 `responses` 仍未审。在此之前，"未决"**不得**被读成"通过"。
2. **历史差异**：现存账本缺少原始 usage 与 provenance，无法据其归因两日差异，也不能回填。未来含 K1 的采样只能建立新的前瞻、可分层基线，不能重现或证明过去的历史时段原因。
3. **1M 桶与压缩边界**：B 的 pilot 最大 ctx 121K、10 轮未触发 fold；`768k_1m` / `gte_1m` 与 fold 边界**仍无样本**。
4. **样本量**：B 的正式分层实验需按 §3 的样本规则**预注册**（每桶 ≥30 有效请求、≥3 成员），本次 30 条全部 `insufficient_sample`。
5. **`concurrency_level` 的逐请求信号**：本会话**未找到**一条不取 store 锁的运行时并发信号，属**结构性缺口**；当前以臂登记承载。
6. **`billing_usage` 在场性的历史不可考**：账本不保留原始 usage，无法判断历史行是否带过 oracle（A §9 已列为结构性缺口）。
7. **A §5 的跨路由口径耦合**（`reasoning_protocol` 与 usage 约定）：A 本轮已把二者拆开（`inclusiveUsage` 与 `deepseek` 解耦），但 **LongCat 端点的实测复核仍缺**（无凭据）。
8. **冻结快照对应关系**：`stream_usage.go` 当前 MD5 与冻结值一致；`cachelab-usage.go.snapshot` 的 MD5 为冻结值 `cc0cda4fcbd22bd7f68602d376656865`，但当前工作树 `internal/cachelab/usage.go` 为 `01a5cb37e30cf272a095fce1a09837f1`。K3 的归档快照可追溯，当前文件不能直接声称与冻结版本逐字节相同；需在提交前确认其后续差异并重新跑相关测试。
9. **发布/版本收尾**：本轮检查记录未提交、未推送、未开 PR；须在后续提交审阅中重新确认最终工作树与发布版本。

## 10. 本文未做

- 未修改任何一方的代码、数据、归档或统计口径。
- 未提交、未推送、未开 PR。

**阶段收口**：P4 的证据汇总、边界标注和本轮 go/no-go 已完成；主线保持开放，直到 §9 的验证、正式采样、差异调和及发布收尾完成。此阶段关闭不代表整项研究或发布完成。
