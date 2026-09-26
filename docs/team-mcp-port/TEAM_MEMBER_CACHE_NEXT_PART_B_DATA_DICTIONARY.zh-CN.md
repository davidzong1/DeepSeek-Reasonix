# Part B 下一轮：请求/turn 观测数据字典与 account scope 规范

> 状态：**只读盘点 + 离线分析工具已落地，生产字段未改动**。日期：2026-09-25。
> 依据：`TEAM_MEMBER_CACHE_NEXT_ROUND_3AGENT_PLAN.zh-CN.md` §Part B B.2 第 1/4/5/8 项、§B.3 写集。
> 配套：`TEAM_MEMBER_CACHE_NEXT_PART_B_QUALITY_LATENCY_SPEC.zh-CN.md`（B.2 第 2/3 项）、
> `TEAM_MEMBER_CACHE_NEXT_PART_B_RESULT.zh-CN.md`（核验表与结论）。
> 本文不含任何 prompt 正文、工具参数、凭据、答案或可还原用户内容的字段。

## 0. 本文回答什么

§B.2 第 1 项要求的最小请求/turn 观测契约，逐字段回答「现在有没有、在哪个字段、缺的是什么」；
第 4 项回答「现有 `MemberCacheRequest` / `cachelab.Sample` / usage 发布链路能不能承载」；
第 5 项回答「account scope 与 route identity 如何分离」；第 8 项回答隐私与保留。

**结论摘要**：18 个契约字段中，**8 项**在生产记录内可直接读（#1/3/5/6/10/11/12/16），
**4 项**生产侧只有素材、需派生或只有聚合（#7/8/9/15），**3 项**只存在于实验夹具
（`internal/cachelab.Sample`：#2 arm、#14 latency、#18 quality），**3 项**两处都没有
（#4 account scope、#13 TTFT、#17 并发度）。**缺失项一律不做 B 侧落地**——其中
`internal/team/cacherequest.go`、`internal/cachelab/**` 与 `internal/cli/team_usage_publish.go`
按 §B.3 与既往接口请求（`TEAM_MEMBER_CACHE_PART_B_TO_C_INTERFACE_REQUEST.zh-CN.md`）属 C 的写集，
本文只提交 schema 变更提案与消费者清单。

## 1. 契约字段落位表（§B.2 第 1 项）

两处载体：**P** = 生产记录 `team.MemberCacheRequest`（`internal/team/cacherequest.go`，成员
`.cache_requests.jsonl` 的唯一形状，由 `internal/cli/team_usage_publish.go:425` 投影）；
**L** = 实验夹具 `cachelab.Sample`（`internal/cachelab/sample.go`，`go test -tags live` 路径）。

| # | 契约字段 | 现状 | 载体与字段 |
|---|---|---|---|
| 1 | 匿名实验样本 ID | ✅ | P `request_id`(`:55`) + `request_id_source`；值是 `turn:<TurnID>:<seq>` 或 `writer:<team>:<member>:<seq>`，**是本地假名而非匿名**，见 §4 边界说明 |
| 2 | 条件臂 | ⚠️ **只在夹具** | L `arm`(`:17`) + `config_digest`(`:34`)；P 无 arm、无 config digest |
| 3 | member/session lineage | ✅ | P `member_id`、`session_id_hash`(`:81`)、`session_ordinal`、`session_first_request_seq`、`session_request_seq`(`:77`) |
| 4 | account scope 匿名 ID | ❌ | 无。见 §3 |
| 5 | 网关/route | ✅ | P `route_bucket`(`:67`) ← `memberProviderResolver.RouteBucket()`(`team_backend_build.go:145`)；L `upstream_host` |
| 6 | model | ✅ | P `model_ref`、`provider`；L `model_ref` |
| 7 | logical turn | ⚠️ | P `turn_id`(`:70`) + `session_sequence`；**turn 外事件为空**（`turn_event_publication.go` 的 `publishOutsideTurn` 分支不带身份），空值必须读作「未观测」 |
| 8 | attempt / retry | ⚠️ **只有聚合** | P `request_count` + `request_count_source`(`:112`，闭集 observed/defaulted/unrecorded)：一条记录可代表 N 次 HTTP attempt，但**没有逐 attempt 身份**；L 有 `attempt`(`:47`)、`turn_seq`(`:43`) |
| 9 | cold / warm | ⚠️ **可派生** | 由 `session_ordinal` / `session_first_request_seq` / `has_prev_request` 派生，判据在 `startsColdPrefix`(`cachemisscause.go:178`)：轮换后首个请求与 writer 首个请求都是 cold，**无 writer session 状态的记录是 unknown 而非 cold** |
| 10 | 输入 token | ✅ | P `prompt_tokens`(`:98`)、`context_prompt_tokens`(`:99`)（分桶用后者：settled attempt 自身大小） |
| 11 | 缓存 token | ✅ | P `cache_hit_tokens` / `cache_miss_tokens` / `cache_write_tokens` |
| 12 | 维护/rewrite 原因 | ✅ | P `prefix_change_reasons`(`:132`)、`session_context_reasons`，闭集由 `internal/cachereason` 拥有；`MessagesRewritten`(`:155`) 是对应的量化字段 |
| 13 | 首 token 时间 | ❌ | 全仓不存在 TTFT（`grep -rni 'ttft\|first_token'` 零命中），见质量/延迟规范 §2.1 |
| 14 | 端到端 latency | ❌ **P 不存在** | L `latency_ms`(`:48`)、`gap_ms`(`:51`) 存在；P 无任何时间量。turn 墙钟存在但**在另一个文件里**：`turnevent` 的 `TerminalSummary.startedAt/finishedAt/durationMs`(`ledger.go:79-81`)，可按 `turn_id` 连接 |
| 15 | 完成状态 | ⚠️ **两个信号，都不是质量判据** | P `finish_reason`(`:116`) 是 provider 停止信号；turn 终态 `outcome`/`status` 在 `turnevent` 侧。二者都**不得**当作任务成功 |
| 16 | unknown 原因 | ✅ | P `usage_unknown`(`:113`)、`usage_estimated`、`usage_source`、`accounting_valid` + `accounting_issues`(`:123`)、`messages_comparable`；闭集且 additive，读者可按原因剔除而非按单一布尔 |
| 17 | 并发度 | ❌ | 两个载体都没有 in-flight 请求数。见 §2 提案 |
| 18 | 质量结果 | ⚠️ **只在夹具** | L `quality_check`(`:85`)（三值 `quality_pass`/`quality_fail`/`quality_unchecked`）+ `confounds`；P 无 |

**分桶维度可直接复用**：`team.CacheRequestBucketOf`（`cachereport.go`，左闭右开 8 桶，
非正大小落 `unknown_prompt`）与 `team.CacheRequestStages`。

## 2. 缺口与 schema 变更提案（§B.2 第 4 项）

**B 不实现下列任何一项**（§B.3：需生产观测字段时先出提案，由协调者指定唯一实现者）。
每项给出：落点、上游真值、消费者、兼容策略。

| 提案 | 落点 | 上游真值 | 消费者 | 兼容 |
|---|---|---|---|---|
| **N-1** latency 四元组 | `team.MemberCacheRequest` 新增 `provider_latency_ms` / `turn_wall_ms` / `turn_started_at` / `queued_ms`（全部 `omitempty`） | provider 段：`cachelab` recorder（`recorder.go:270` reserve → `:341` 定值）；turn 段：`turnevent.TerminalSummary`（`ledger.go:79-81`）按 `turn_id` 连接；排队/重试段：`Σ attempt 延迟` 与 `gap` | 报表 `renderCacheTurnCost`；C 的延迟分层 | 旧行缺键 → 读作「未观测」，**不得补 0**；`schema_version` 不变（additive 字段） |
| **N-2** arm 与配置摘要 | `MemberCacheRequest` 新增 `condition_arm`、`config_digest` | 冻结的 experiment registry（协调者持有），由 runner 注入；**不可从历史数据反推** | C 的矩阵分层、B 的 128 分层 | 同上；无 arm 的记录只允许进「未分层」桶，禁止按默认值归臂 |
| **N-3** account scope | 新增独立 `account_scope`（见 §3），与 `route_bucket` 并排、**互不推导** | 池条目身份经域分隔哈希，密钥/凭据零参与 | 跨账号结论；128 外推边界 | 同上 |
| **N-4** 并发度 | `MemberCacheRequest` 新增 `concurrent_requests`（采样值，非请求路径取锁） | 现成真值只有 `teamTaskService.busyMembers()`（runtime 确认在跑的成员数），**粗于 in-flight provider 请求数** | 128 的并发维度 | 同上；缺失时报 `n/a`，禁止写 1 |
| **N-5** turn 连接 | 不改记录结构：报表侧按 `turn_id` 左连 `TerminalSummary` | 已有 | 覆盖表新增 `turn_joined/turn_unjoined` 两列 | 纯读侧新增，无 schema 变更 |

**成本最低、收益最高的是 N-5**：它不需要任何新字段，只需要把两个**已经落盘**的身份连起来，
就能让「单 turn 墙钟」与「缓存读数」在同一个数据集里对账。N-1..N-4 都需要动 C 的写集。

## 3. account scope 与 route identity 必须分离（§B.2 第 5 项）

### 3.1 `route_bucket` 精确是什么

`memberProviderResolver.RouteBucket()`（`internal/cli/team_backend_build.go:145-154`）：

```text
material = kind \x00 endpoint \x00 name \x00 proxy.Mode \x00 proxy.Type
route_bucket = kind + "/" + hex(sha256(material)[:6])
```

`name` 是**成员池条目的 UserID**。因此它同时混合了「适配器种类」「端点」「池条目身份」「代理姿态」
四个维度，用于「两条请求是否共享 provider cache」这一判断，也正是它作为基线分层键的原因。

### 3.2 为什么它不能当 account scope（三条独立理由）

1. **同账号不同端点 → 不同桶**（endpoint 参与哈希）；`route_bucket` 变了不代表账号变了。
2. **同端点不同账号共享桶**（若两个账号共用一个网关且条目 `name` 相同或未区分）→ **跨账号样本会被合层**，
   这正是 §3.4「至少两个独立账号作用域」结论所不能接受的。
3. **它不可轮换、不可按访问策略收窄**：6 字节哈希前缀只为「区分两条 route」而设计，
   没有域分隔、没有盐、没有轮换机制；把它当账号 ID 会让「账号匿名化」这个要求失去落点。

### 3.3 提案的匿名 account scope（N-3 细则）

- **值**：`account_scope = base32(sha256("reasonix/account-scope/v1" \x00 <pool-entry UserID>)[:8])`。
  域分隔字符串使它与 `route_bucket` 的哈希**不可能相等**，且同一账号在不同端点上得到同一 scope。
- **不参与哈希的东西**：API key、token、SecretRef、base URL 的 query、任何凭据派生值。
  「密钥的哈希仍然是密钥的派生值」——本仓库既有的措辞，这里沿用。
- **轮换**：scope 是**每次实验运行的盐**（`salt` 记入 experiment registry，不落盘到成员记录之外）。
  同一次运行内稳定，跨运行不可连接。这是「跨环境可比较」与「跨运行不可追踪」的取舍，取后者。
- **访问**：`account_scope` 只在 B/C 的分析产物里出现；成员记录里它只是一个哈希，与 `route_bucket` 同级。
- **机械守卫（已落地）**：`internal/team/allowance_sensitivity_test.go` 的
  `TestAllowanceAuditRefusesToReadRouteBucketAsAnAccountScope` 断言两层不同 route 产生两层、
  且渲染表**必须**打印 `account_scope: unrecorded - route_bucket is a route/pool fingerprint, not an account scope`；
  同文件 `TestAllowanceAuditRefusesToReadRouteBucketAsAnAccountScope` 同时断言
  `AccountScopeMeasured == false`（记录类型确实没这个维度时，分析器不得声称有）。

### 3.4 现状下的 128 分层能到什么程度

分析器已能按 `route_bucket × prompt bucket` 分层出 §B.2 第 6 项要求的每层
`n` / 命中 / 违规 / `max excess` / p50 / p90 / max / unknown 率。**账号层与并发层恒为
`unrecorded` / `n/a`**，所以本轮任何「跨账号」结论都不可得——这是记录形状的结论，不是采样量的结论。

## 4. 隐私、保留与安全检查（§B.2 第 8 项）

**记录中不得出现的**（现有两处载体均已满足，本轮的离线分析工具也遵守）：

1. prompt / 消息正文、工具参数、工具输出、模型答案、deliverable 文本；
2. 凭据、token、API key、SecretRef 值，以及**任何由它们派生的摘要**（哈希也是派生值）；
3. 原始 UserID、邮箱、文件系统路径、`session_id` 明文（记录只存 `session_id_hash`）。

**本轮新增的可读面**：`allowance_sensitivity_test.go` 只读已落盘的
`MemberCacheRequest`（其字段本身就不含正文），输出的是计数与百分位；
表格里出现的字符串只有 `route_bucket`、prompt bucket 名与 cause 名。

**保留**：沿用既有策略——成员日志 `2 MiB` 上限、压缩时保留 7 天
（`cacherequest.go` 的 `cacheRequestMaxBytes` / `cacheRequestRetention`），读取上限 8 MiB；
分析产物（本文与结果文档）**不含样本**，只含聚合数字。

**访问**：成员记录落在用户自己的 `~/.reasonix/team/<team>/<member>/` 内，权限 0600/0700；
跨账号比较需要 §3.3 的 scope，而 scope 的盐不进记录。

**可逆性自检**：本文与结果文档中的每个数字都可从 `team cache-report --export-requests`
导出的 JSONL 重新生成（结果文档 §证据给出复算命令）；文档中不含任何单条样本的原文。

## 5. 未决项

| # | 项 | 状态 |
|---|---|---|
| B-D1 | N-1..N-4 的落点与唯一实现者 | **待协调者裁定**；B 未落地任何一项 |
| B-D2 | arm 只能由 runner 注入，历史数据不可反推 | 已在提案中标注；C 的 pilot 需先确认注入点 |
| B-D3 | `account_scope` 的盐由谁持有、是否随 registry 冻结 | 待裁；默认每次运行新盐（不可跨运行连接） |
| B-D4 | `concurrent_requests` 的真值粒度粗于 in-flight 语义 | 提案中已标注，若 C 需要更细粒度需先定义采样点 |
| B-D5 | 生产记录无 latency → 本轮延迟结论只在夹具可得 | 见质量/延迟规范 §2 与结果文档核验表 |
