# Part B 下一轮结果：数据契约、质量/延迟可测性与 128 外推边界

> 状态：**B 侧 `CONDITIONAL`**。日期：2026-09-25。分支 `Team-agent`，基线快照 `f53c4bf91`。
> 依据：`TEAM_MEMBER_CACHE_NEXT_ROUND_3AGENT_PLAN.zh-CN.md` §Part B、§B.4 通过条件。
> 交付物：本文 + `TEAM_MEMBER_CACHE_NEXT_PART_B_DATA_DICTIONARY.zh-CN.md` +
> `TEAM_MEMBER_CACHE_NEXT_PART_B_QUALITY_LATENCY_SPEC.zh-CN.md` +
> `internal/team/allowance_sensitivity_test.go`（离线重算/敏感性工具）。
> **本轮未改任何生产文件**：工作树新增 1 个测试文件 + 3 份文档；`git diff --stat -- '*.go'` 为空
> （改动全是未跟踪新文件），`git status --short` 只列出这四项与协调者的计划文档。

## 1. 结论（§B.4 五条通过条件）

| # | 条件 | 结论 | 依据 |
|---|---|---|---|
| 1 | 质量与延迟指标有明确、可复现定义，且不采集敏感正文 | **PASS** | 质量/延迟规范 §1、§2；白名单只存分数/失败类型/受控引用 |
| 2 | account scope 与 route identity 分离，不以 `route_bucket` 代替账号 | **PASS（分离）/ 未实现（采集）** | 数据字典 §3；`TestAllowanceAuditRefusesToReadRouteBucketAsAnAccountScope` 机械钉住 |
| 3 | missing/unknown/retry/error 能保留且可审计 | **PASS** | 字段落位表 #16（闭集 reason 字段）；分析器的「未知计数」不变量 `TestAllowanceLayerAccountingCloses` |
| 4 | 128 结论严格限于实际覆盖范围；跨网关条件缺失记 `CONDITIONAL` | **CONDITIONAL** | §5；无凭证、无样本、记录形状不含 account/并发维度 |
| 5 | 报表可从原始脱敏记录重新生成，分母与总量对账 | **PASS** | `team cache-report --export-requests` 为输入；分析器的分区不变量 + 复算命令见 §4.3 |

**总体 `CONDITIONAL`**：本轮的**可交付部分（定义、契约、分析工具、分层与敏感性口径）全部完成并复算通过**；
卡住的是**采集面**——`account_scope`、`concurrent_requests`、生产侧 latency 与 arm 都不在记录里，
而它们的落点属 C 的写集（§B.3），B 依规则不并行改动。因此 §B.2 第 6 项要求的
「≥2 账号 × ≥2 网关」验证**在本轮不可能完成**，只能交付它的计算器与判据。

## 2. 交付物与写集

| 文件 | 性质 | 说明 |
|---|---|---|
| `internal/team/allowance_sensitivity_test.go` | **新增**（测试/离线分析） | 分层 128 审计 + 规则敏感性；复用生产的 `startsColdPrefix` / `prefixMoveCause` / `newCacheMissCauseTracker` |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_NEXT_PART_B_DATA_DICTIONARY.zh-CN.md` | **新增**（文档） | §B.2 1/4/5/8 |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_NEXT_PART_B_QUALITY_LATENCY_SPEC.zh-CN.md` | **新增**（文档） | §B.2 2/3 |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_NEXT_PART_B_RESULT.zh-CN.md` | **新增**（本文） | 核验表、证据、结论、回滚 |

**写集归属复核**：A 的 `internal/agent/**`、C 的 `internal/cachelab/**` / `internal/team/cacherequest.go` /
`internal/cli/team_usage_publish.go` / runner 一律**未触碰**；本文件是新文件，
不与任何既有文件同名，`git status --short` 只列出它与三份文档。

## 3. 离线工具：为什么需要在 `internal/team` 内做

§B.2 第 6/7 项要的是「用已有计算器重算 128，并给出敏感性」。已有计算器
（`internal/cli/team_cache_append_granularity_test.go`，上一轮落地）给的是**扁平分布**：
全样本的 `samples/unknown/over_allowance/mean/p50/p90/max`。本轮缺的是：

1. **分层**（route × prompt bucket 的每层 `n`/违规/`max excess`/百分位），以及
2. **规则敏感性**（现行 128 vs 更小阈值 vs 零豁免 vs 未知不归类）。

分层需要「同一 writer session 内上一条请求的 prompt」——这是生产的
`cacheMissCauseTracker`（`internal/team/cachemisscause.go`）的职责。放在 `internal/team` 的包内测试里，
审计可以**直接复用**生产的 `startsColdPrefix`、`prefixMoveCause`、`cacheReasonKinds`、`newCacheMissCauseTracker`，
只重述**被审计的那一行**（allowance 比较）。这是本轮设计上的关键取舍：
与其在别处实现第二份「什么算 append-only、什么算 cold」的真相，
不如让 B 的假设**只能在一处**与生产不同——而那处由一致性守卫钉住（§4.1）。

## 4. 证据

### 4.1 一致性守卫（本轮最重要的一条）

`TestAllowanceRuleMatchesProductionAtTheValueInForce` 对
`{hasPrev/prevPrompt} × {prompt} × {miss} × {diagnostics} × {reason} × {messagesRewritten}`
共 **1440 组**输入枚举，断言审计重述的规则在**报表在用的那个 allowance（128）**上
与生产的 `classifyCacheMiss` **逐组一致**。

**变异探针（实跑，已还原）**：把 `excess <= rule.Allowance` 改成 `excess < rule.Allowance`，
该测试立即失败并给出反例：

```text
--- FAIL: TestAllowanceRuleMatchesProductionAtTheValueInForce
    allowance_sensitivity_test.go:352: rule on {prompt 1000 miss 128 prevPrompt 1000 …}:
    got "provider_residual_unexplained", want production "append_only_expected"
```

还原后全绿。也就是说：敏感性表只能**因为 allowance 与「未决样本处置」两个轴**偏离生产，
不可能因为重述失误而偏离。

### 4.2 always-run 断言（无凭证也跑）

```text
--- PASS: TestAllowanceRuleMatchesProductionAtTheValueInForce (0.00s)
--- PASS: TestAllowanceAuditRefusesToReadRouteBucketAsAnAccountScope (0.00s)
--- PASS: TestAllowanceLayerAccountingCloses (0.00s)
--- PASS: TestAllowanceSensitivityMovesTheResidualInTheExpectedDirection (0.00s)
--- SKIP: TestAllowanceAuditFromARealStore (0.00s)
PASS  ok  reasonix/internal/team  0.012s
```

- **不变量**：每层 `requests == measured + unknown` 且 `measured == within + violations`；
  7 条夹具里 4 条必须落进「未决/其他」（undiagnosed、prefix 移动、收缩、无认领的数组改写），
  即**未知被计数而不是被丢弃**。
- **敏感性方向**：`share(128) ≤ share(64) ≤ share(0)`，并断言「零豁免」与「现行」**不得相等**
  （否则夹具对规则不敏感，表格就是空转）；同时断言严格口径 `share(strict) ≤ share(absorbed)`。
- **route ≠ account**：两条 route → 两层；`AccountScopeMeasured == false`、
  `ConcurrencyMeasured == false`；渲染表**必须**出现
  `account_scope: unrecorded - route_bucket is a route/pool fingerprint, not an account scope`
  与 `concurrency: n/a`。

### 4.3 真实日志臂的语义（无样本时**跳过**而不是伪装通过）

`TestAllowanceAuditFromARealStore` 的输入是 `REASONIX_CACHE_SAMPLE_LOG=<team>/<member>/.cache_requests.jsonl`
（与上一轮计算器同一个变量：**同一份输入，两个视角**）。无变量时跳过；有变量但**没有可测量样本**时
`Skipf("… the allowance is inconclusive on this log, not confirmed")`——「什么都没测到」永远不是「通过」。

**渲染验证（人为构造 10 条记录，仅用于证明表格形状，不是测量结果）**：

```text
append-block allowance audit (allowance in force 128 tok)
  account_scope: unrecorded - route_bucket is a route/pool fingerprint, not an account scope
  concurrency: n/a - no record carries an in-flight request count
  layer                prompt_bucket  requests  measured  within  over_allowance  unknown  excess_p50  excess_p90  excess_max
  anthropic/aaaaaaaaaaaa lt_32k                7         3       1               2        4         272         272         272
  openai/bbbbbbbbbbbb  128k_256k             3         2       0               2        1        1200        2200        2200
rule sensitivity (same sample set, one rule per row)
  allowance  undecided_separate  scoped  append_expected  residual  undecided  other  published_residual  residual_miss_share
        128               false      10                1         4          1      4                    5                6.09%
         64               false      10                0         5          1      4                    6                6.68%
          0               false      10                0         5          1      4                    6                6.68%
        128                true      10                1         4          1      4                    4                5.70%
```

三点读法（**这三条是口径结论，不是 provider 结论**）：

1. **「未决样本」的处置不是细节。** 同一份样本，「未决并进 residual」得 6.09%，
   「未决单列」得 5.70%。生产分区没有「未决」类，所以它必然是前者——
   **报告里那个 residual 里始终含着一部分「没人测过 append 是否成立」的样本**，二者必须并列披露。
   若把 `undecided` 的 miss 一并算进 residual（如某条记录是整个轮换后的冷请求），
   这个口径差可以放大到**数十个百分点**（本工具在另一组不一致夹具上实测 6% vs 93%）。
2. **零豁免并不自动更保守。** 此例里 64 与 0 同分（`<=` 与 `<` 只差一个等号，
   而 excess 全为整数），说明「阈值收紧」与「改变 residual」不是线性关系；
   必须逐规则报告而不是宣布「我们用了更严的口径」。
3. **prompt bucket 是真实分层维度**：小桶 p50=272 而大桶 p50=1200，
   最脆的 128 模型在**大上下文桶**上承压更大。这正是 §B.2 第 6 项要求分层、
   禁止用总均值代替分层的原因。

### 4.4 工程门禁（实跑）

```text
gofmt -l internal/team/            → 空
go vet ./internal/team/            → 干净
go build ./...                     → 通过
go test ./internal/team/ -count=1  → ok (7.939s)
go run ./tools/repolint            → repolint: clean (1145 baselined findings)
```

repolint **零新增**（我的新文件不在任何 baseline 名单里）。

## 5. 128 模型：外推边界与所需验证（§B.2 第 6 项）

### 5.1 结论

**仍然只能称「在样本集内观察到的模型」。** 本轮没有新增任何真实样本：
环境无 `ANTHROPIC_*` / `REASONIX_LIVE_CACHE_*` / `DEEPSEEK_API_KEY`，
磁盘上也没有非空的 `.cache_requests.jsonl`（`find ~ -name '.cache_requests.jsonl' -size +0` 零命中）。
上一轮 30 条数据集建立在 `t.TempDir()` 上，随临时目录消失，**不可复算**。

### 5.2 为什么「≥2 账号 × ≥2 网关」在本轮不可能完成

数据字典 §3 已确证：`route_bucket` 是「适配器 + 端点 + 池条目名 + 代理姿态」的四元哈希，
**不含账号语义也不可轮换**。因此即使拿到更多样本：

- 多个**账号**若共用同一端点，可能落进**同一个** `route_bucket` → 账号维度被合层；
- 同一账号换端点会落进**不同** `route_bucket` → 假阳性分层。

这是**记录形状**的缺口（不是采样量缺口），所以分析器把该列恒印为 `unrecorded`，
并在 §B.4 第 4 项记 `CONDITIONAL`。修复路径是提案 N-3（数据字典 §2/§3.3）。

### 5.3 一旦 N-3/N-4 落地，验证矩阵（预先冻结）

| 维度 | 层数下限 | 每层要报的量 |
|---|---|---|
| `account_scope` | ≥2 | `n`、命中、违规、`max excess`、p50/p90/max、unknown 率 |
| `route_bucket`（网关/route） | ≥2 | 同上 |
| `prompt bucket` | 至少 `lt_32k` / `128k_256k` / `768k_1m` 各 ≥1 | 同上（§4.3 已显示层间差异） |
| `concurrent_requests` | 采样覆盖 ≥1 个 >1 的并发档 | 同上 |

**通过判据**（预先声明，避免事后挑选口径）：两个账号层各自 `over_allowance == 0` 且
`max excess ≤ 128`；任一账号层出现违规，则 128 **不得**作为跨账号模型使用，
而应报告为「在此网关、此账号、此上下文桶内观察到的切分余量」。

## 6. 敏感性报告（§B.2 第 7 项）

规则集与方向已在 §4.3 给出，并写成可复跑的表（`defaultAllowanceRules()`）：

| 规则 | 语义 | 与生产的差异 |
|---|---|---|
| `{128, absorbed}` | **生产在用**（`cachereport.go` 发布的 `append_block_allowance`） | 无（由 §4.1 守卫钉住） |
| `{64, absorbed}` | 更小阈值 | 仅阈值 |
| `{0, absorbed}` | 零豁免：miss 必须不超过新增内容 | 仅阈值 |
| `{128, separate}` | 未知样本**不归类**进 residual | 仅未决处置 |

**禁止选择最有利口径作为主结论**已落成断言：四个规则行**必须描述同一批样本**
（`report.Scoped == len(records)`），且方向约束 `128 ≤ 64 ≤ 0` 与 `strict ≤ absorbed` 由测试强制。
报告的主口径固定为**生产在用的 `{128, absorbed}`**，其余三行作为并列披露。

## 7. 对 C 的接口请求（未落地，登记）

| # | 请求 | 落点 | 为什么 B 不做 |
|---|---|---|---|
| I-1 | 延迟四元组（N-1） | `internal/team/cacherequest.go` + recorder | C 的写集 |
| I-2 | arm / `config_digest` 注入（N-2） | runner + 记录 | C 的 runner |
| I-3 | 匿名 `account_scope`（N-3） | 记录字段 + registry 盐 | C 的写集 + 协调者裁定 |
| I-4 | `concurrent_requests` 采样（N-4） | `memberUsagePublisher` + 记录 | 需运行时句柄，C 的写集 |
| I-5 | `cachelab` 延迟分母修正（质量/延迟规范 §2.5） | `internal/cachelab/stats.go:100-132` | C 的写集 |
| I-6 | turn 连接（N-5，**零 schema 变更**） | 报表读侧 | 可随时由任何一方落地，B 已备好定义 |

## 8. 回滚与成本

- **回滚**：本轮只新增 `internal/team/allowance_sensitivity_test.go` 与三份文档。
  回滚 = 删除这四个文件；**零生产行为影响、零 schema 影响、零请求字节影响、零构建标签影响**。
- **成本**：分析工具是纯函数 + 一个按需跳过的真实日志臂，`internal/team` 套件约 8 s（两次实跑 7.9 s / 9.5 s）；
  无网络、无凭证、无新依赖。
- **不变量**：未改任何默认开关；未改 prompt / 工具 schema / 统计分母；未采集任何真实请求。

## 9. 未验证 / 未做（明确边界）

1. **128 的跨账号、跨网关结论：未验证**（§5.1、§5.2），原因是记录形状与样本双重缺失。
2. **生产路径的 latency / 质量 / arm / 并发：未测量**（字段不存在，见数据字典 §1 落位表）。
3. **rubric 与盲评流程：规范已冻结，具体任务清单未填**（待 C 的任务集定稿，质量/延迟规范 §4 B-Q1）。
4. 未修改 `internal/agent/**`、`internal/cachelab/**`、`internal/team/cacherequest.go`、
   `internal/cli/team_usage_publish.go` 或任何 runner。
5. 未提交、未推送、未开 PR。
