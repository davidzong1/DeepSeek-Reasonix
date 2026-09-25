# Part B：K1 后真实 Team 基线与剩余跌幅定位 —— P0 只读盘点（Agent B）

> 状态：**P0 盘点完成，真实实验未启动（等 M0/M1 合流门槛）**。日期：2026-09-24。
> 依据：`TEAM_MEMBER_CACHE_POST_JOINT_3AGENT_PLAN.zh-CN.md` §3 Agent B、§4 M0/M1、§5 阶段 P0。
> 本文只做只读盘点与冻结记录，**不改任何生产缓存行为、请求字节、统计分母**。

## 0. 本文回答什么

P0 阶段 Agent B 的交付是「确认 Team member 观测是否能拿到 member/session/turn/route/model/context/attempt，设计不含敏感正文的日志」。
结论：**大部分字段已在链路里，六项缺失，且其中一项落在 Agent C 的写集上**（§3）。

## 1. M0 冻结记录（在任何实验之前）

| 项 | 值 |
|---|---|
| 基线 commit | `7bda0c3d32d2`（分支 `Team-agent-merge-mainv2`） |
| K1 是否在待测构建内 | **是**，已由 `5241384b6` 提交（`internal/provider/anthropic/stream_usage.go`） |
| 冻结时工作树 | Agent A：`M internal/provider/anthropic/stream_usage.go`、`?? internal/provider/anthropic/stream_usage_test.go`；Agent C：`?? internal/cachelab/zz_partc_probe_test.go`；本文档 |
| 折叠规则冻结（A 收口后） | `internal/provider/anthropic/stream_usage.go` md5 `71b7d3b79a4c6dfc74d1357b310190ff`；`internal/cachelab/usage.go` md5 `cc0cda4fcbd22bd7f68602d376656865`。**基线必须记在这两个摘要上**，任一处变动即作废 |
| 网关 | `aiapi.lejurobot.com`（`ANTHROPIC_BASE_URL`，无 `/v1` 后缀） |
| 线上 model | `deepseek/deepseek-v4.1-flash`（池内拼写 `deepseek/deepseek-v4.1-flash[1m]`，由 `team.ResolveAgentUserModel` 去后缀） |
| 账号范围 | 池条目 `deepseek-v4-flash-roojin`（`https://aiapi.lejurobot.com`，`deepseek-v4-flash` 条目带 `/v1` 后缀，二者是不同 BaseURL） |
| 适配路由 | `anthropic`（`team.ResolveAgentUserProvider`：deepseek + `[1m]` + 自定义 BaseURL → anthropic） |
| 主指标 | `Σhit / (Σhit + Σmiss)`，且必须并列 `miss_tokens_per_request` |
| oracle | `billing_usage.openai_usage` 是**可选、私有**旁证，不是跨 Provider 契约 |
| 历史账本校验和 | `2026-09-23.jsonl` md5 `98961189e6e7f477d52a5e1e7c83b441`；`2026-09-24.jsonl` md5 `61eaaa3a8969b8626156d06e315e9299` |

**冻结时的工具链状态**：`go build ./...` 通过；`go test ./internal/team/ ./internal/cachelab/ ./internal/stats/` 全绿；`go vet -tags live ./internal/cli/` 干净。
方案 §9 的离线套件在 A 收口后复跑：`go test ./internal/provider/anthropic/ ./internal/team/ ./internal/stats/ ./internal/cachelab/` 全绿；`go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...` 干净；`go test ./internal/cli/ -run 'Cache|Usage|Team' -count=1` 在 **3/3 次**独立运行中通过（首次运行出现过一次失败，未能在日志中复现，三次重跑均为 exit 0；**未定位原因，不作为已排除项**）。

## 2. 观测链路：一个 usage 事件走到成员记录的全路径

```text
agent.run_loop.go:230/248  emitTurnUsage(event.Event{Kind:Usage, ModelRef, Usage, UsageSource, CacheDiagnostics})
  → control.Controller.sink
      = inboxEventSink{ inner: turnEventSink }
        turnEventSink.Emit → stream = event.Coalesce(turnEventDurableSink)
          → turnEventDurableSink.EmitChecked → turnEventSink.persistAndPublish
              → ledger.AppendEnvelope → appendLocked  ← 唯一盖 TurnID / Sequence / SessionID 的地方
              → publishInner(stamped) → inner = cli 层成员 sink
  → memberUsagePublisher.Sink 的包装（observe，同步，转发不变）
      → memberCacheRequest(...) → team.MemberCacheRequest → 队列 → .cache_requests.jsonl
```

关键点（均为 file:line 证据）：

- `internal/turnevent/ledger.go:525` — `e.TurnID, e.Sequence, e.Status = l.active, l.nextSeq, status`；同段 `e.SessionID, ... = l.sessionID, ...`。**这是链路里唯一给 usage 事件盖上 turn/session 身份的地方。**
- `internal/control/turn_events.go:208` `persistAndPublish`：若 `ledger.ActiveTurnID() == ""`，事件走 `publishOutsideTurn`（`internal/control/turn_event_publication.go:12`）——**turn 外事件不带 TurnID/Sequence/SessionID**。这条分支必须被算作「未观测」而不是「无 turn」。
- `internal/cli/team_usage_publish.go:360` `memberCacheRequest`：成员记录的唯一投影点，已在成员写集内。

## 3. M1 契约字段落位表

对照方案 §4 的 `TeamRequestObservation`，逐字段核验（✅ 已有 / ⚠️ 部分 / ❌ 缺失）：

| 契约字段 | 现状 | 来源 / 缺口 |
|---|---|---|
| `team_id` / `member_id` | ✅ | `cacherequest.go:59-60`，来自 `OwnerKey` |
| `turn_id` | ✅（但可为空） | `cacherequest.go:70`，来自 `event.TurnID`；turn 外事件为空 |
| `request_seq` | ✅ | `SessionRequestSeq`（`cacherequest.go:73`，writer 自增） |
| `request_count` | ✅ | `cacherequest.go:89` |
| `request_count_source` | ✅ | `cacherequest.go:93` + `RequestCountSourceOf`（闭集 observed/defaulted/unrecorded） |
| `context_prompt_tokens` | ✅ | `cacherequest.go:83` ← `provider.Usage.ContextPromptTokens`，由 `finalizeSamplingUsage` 取**最后一次 attempt**（`run_usage.go:228-251`） |
| `prompt_tokens` / `hit` / `miss` / `cache_write` | ✅ | `cacherequest.go:82-87` |
| `route_bucket` | ✅ | `cacherequest.go:67` ← `memberProviderResolver.RouteBucket()`（`team_backend_build.go:145-154`，kind+endpoint+name+proxy 的 sha256 前 6 字节） |
| `model_ref` | ✅ | `cacherequest.go:66` |
| **`session_id`** | ❌ | 记录里**没有**。`event.SessionID` 由 ledger 盖（`ledger.go:525`），但 `memberCacheRequest` 不读它。**缺失**，且这是「同 session 的连续请求不算独立重复」这条样本规则的必要字段 |
| **`attempt`** | ❌ | 无逐 attempt 身份。链路只有聚合计数：`RequestCount` + `RequestCountObserved`（`provider/retry.go` 的 `ApplyRequestAttemptCount`）。`event.AttemptID` 存在（`event.go:438`）但只用于 streaming 显示，**不在 usage 事件上** |
| **`maintenance_phase`** | ⚠️ | 无该字段，但**可派生**：`CacheDiagnostics.PrefixChangeReasons` 携带 `cachereason` 闭集值（`compact_auto` / `prune` / `truncate` / `system` / `tools` / `session_context` / …），`team.CacheRequestStages` 已把它映射成 `post_rewrite` 阶段标签（`cachereport.go:96-108`）。**首轮 / 连续 warm / 长会话后段 / 压缩前后**四个阶段里，只有「压缩前后」目前有真值，其余靠 `SessionRequestSeq` + `HasPrevRequest` 推断 |
| **`task_family`** | ❌ | 代码里**完全不存在**任何任务族概念（全仓 grep 无命中，唯一同名物是 `provider/schema_validate_args_test.go` 的测试夹具常量）。历史账本无标签，方案已禁止声称可重放 |
| **`concurrency_level`** | ❌ | 代码里**不存在任何并发遥测**。可用真值只有 `teamTaskService.busyMembers()`（`team_task_service.go:405-420`，返回 runtime 确认在跑的成员集合），但 `memberUsagePublisher` 目前**不持有** `teamTaskService` 句柄（它只有 `owners/key/route/ctrl/requests`，`team_usage_publish.go:60-88`） |
| **`request_shape_digest`** | ⚠️ | 已有两个本地 hash：`PrefixHash`（system+tools，`cache_shape.go:69-74`）与 `StablePrefixHash`。**都不是 provider cache key**（方案已冻结该措辞）。若 M1 需要「请求形状摘要」用于分层，现有 `PrefixHash` 可作客户端差分，但**不能**当作缓存键 |

**缺失项汇总**：`session_id`、`attempt`、`task_family`、`concurrency_level` 完全缺失；`maintenance_phase`、`request_shape_digest` 有可派生的近似物但语义不等价。

## 4. 落位代价与写集冲突

`TeamRequestObservation` 的六个缺口分两类：

**(a) 只加观测字段、不改请求字节**（符合方案 §3「如必须增加字段，仅提交观测字段」）

需要三处改动：

1. `internal/event/event.go` — `Event` 增加一个 additve 观测结构（SessionID 已在 Event 上，只需读取；`Attempt`/`MaintenancePhase`/`Concurrency` 需新增）。
2. `internal/control/turn_events.go` — 在 `persistAndPublish`（唯一盖身份的点，`:208`）旁边补盖，与 ledger 的 `appendLocked` 同源。
3. `internal/team/cacherequest.go` — `MemberCacheRequest` 增加对应字段（`omitempty`，旧行缺键）。

**冲突**：第 3 项落在 **Agent C 的写集**内（方案 §3 Agent C「建议写集」明确列出 `internal/team/cacherequest.go`）。第 1、2 项不落在 A/C 任何人的写集里，但 `event.Event` 是全线共用结构，改它需要负责人确认。
**处置**：B 不自行落字段。已把三项的落位、上游真值与语义约束写成对 C 的接口请求
（`TEAM_MEMBER_CACHE_PART_B_TO_C_INTERFACE_REQUEST.zh-CN.md`），pilot 阶段按 coverage 缺口披露。

**(b) 需要运行时句柄**（`concurrency_level`）

`memberUsagePublisher` 需要多持一个只读句柄（`teamTaskService` 或等价的 `busyMembers()` 读取器），并在 `observe`（`team_usage_publish.go:207`）采样。**不得在请求路径上取 runtime 锁**。这是 (a) 之外的额外接线，且同样触及 C 的写集（记录字段）。

**这条不是启动 pilot 的硬阻塞**：pilot 只跑串行单成员负载，`concurrency_level` 恒为 1，无需字段即可如实登记。

## 5. 当前可做的、不越界的部分

- ✅ 本文（P0 盘点）。
- ✅ M0 冻结记录（§1）。
- ✅ 离线复核历史账本口径（§6），零成本。
- ✅ 采集脚本与数据字典设计（可写，但不落地到 A/C 的写集）。
- ⚠️ **真实 member 采样：A 已收口（折叠规则已冻结，见 §1 的 md5），技术性阻塞已解除；流程性阻塞仍在**——方案 §5「P0 合流门槛：三方提交清单后，负责人确认写集、字段契约和停止规则；**未通过不得扩大真实实验**」。
- ❌ 任何 `internal/team/cacherequest.go` / `internal/cachelab/**` 的字段改动：C 的写集。字段落位已改为对 C 的接口请求（`TEAM_MEMBER_CACHE_PART_B_TO_C_INTERFACE_REQUEST.zh-CN.md`）。

**A 收口带来的两个可用事实**（B 的基线设计据此更新）：

1. **K1 的正确性在本网关不再依赖私有 oracle**。A 用算术矛盾（`cache_read 33,408 > input_tokens 29`，cache read 不可能是 input 的子集）独立判定 warm 形状，删掉 estimate 前驱读数不变（A 矩阵 §3.1）。因此 B 的 pilot **不因 `billing_usage` 在场性不稳定而失效**——oracle 缺失时读数仍可判定，只是 oracle 一致率不可计算。
2. **A 修掉了 K1 的折叠缺口**（split 之后到来的 split-free 事件把整段 prompt 重复计入，实测 33,437 → 66,845）。该形状**未在真实抓包中出现**，所以它是防御性修复，**不解释** §6 那 ~19pp，也不改变任何历史数字。

## 6. 历史账本口径复核（零成本，已复跑）

用 `7bda0c3d32d2` 复跑方案 §8.2 的两条命令，与 A §5.1、C §4.1 逐项一致：

| 指标 | 09-23 臂 | 09-24 臂 |
|---|---:|---:|
| 读入并过滤后行数 | 1,714 | 1,714 |
| 计入 per-request 基线 | **0** | **0** |
| 排除：计数未验证 | 1,714 | 1,714 |
| 排除：多请求聚合 | 58 | 57 |
| **全样本加权率** | **84.5%**（hit 130,642,816 / miss 24,027,267） | **67.7%**（hit 110,437,632 / miss 52,698,416） |

**独立确认的 provenance 事实**：当前 `~/.reasonix/stats/2026-09-23.jsonl`（2,272 行，2,231 条 usage 行）与 `2026-09-24.jsonl`（1,777 行，1,769 条 usage 行）中，`requests_observed` 为真的行数**均为 0**。因此这两天账本的 per-request 基线确实不可用，A 的降级判定成立；这两天的差异**只能**以全样本口径报告。

**这不能作为 K1 后的 per-request 基线**（方案 §2 禁止项已明列）。

## 7. 阻断与待决项（交负责人）

| # | 项 | 状态 |
|---|---|---|
| B-1 | M1 字段契约中 `session_id` / `attempt` / `task_family` / `concurrency_level` 的落位与写集归属 | **已改为对 C 的接口请求**（`TEAM_MEMBER_CACHE_PART_B_TO_C_INTERFACE_REQUEST.zh-CN.md`）；pilot 阶段不加字段，缺失按 coverage 缺口披露 |
| B-2 | Agent A 的折叠规则**生产文件**已冻结（`stream_usage.go` md5 `71b7d3b79a4c6dfc74d1357b310190ff`），但 **A 的测试在归档后被观测为红**（5 项，`internal/provider/anthropic/`） | **技术性阻塞已解除**（规则文件未变，pilot 有效）；**A 的收口结论当前不可复现**，见 `TEAM_MEMBER_CACHE_PART_B_PILOT_REPORT.zh-CN.md` §6 的告警框 |
| B-3 | `task_family` 无任何上游真值，只能由实验驱动方冻结任务族并登记（不可从历史推断） | 设计待定 |
| B-4 | `concurrency_level` 只能取「runtime 确认在跑的成员数」，它**粗于**「in-flight provider 请求数」（一个 turn 内可有多达 8 个并行工具调用 + guardian + compaction 请求） | 已在记录措辞中标注 |
| B-5 | `billing_usage` 在场性不稳定（C §6.3 已记录「有时在有时不在」） | **影响面已缩小**：A 矩阵 §3.1 证明本网关 warm 形状可由算术独立判定，oracle 缺失只影响「oracle 一致率」这一项，不影响读数本身 |
| B-6 | 1M 桶（`768k_1m` / `gte_1m`）与并发阶梯 | **未开始**，依赖 B-1 |
| B-7 | `go test ./internal/cli/ -run 'Cache\|Usage\|Team'` 首次运行失败一次，随后 3/3 通过，日志未能复现 | **未定位**；不作为已排除项，正式实验前需再跑确认 |

## 8. 本文未做（明确边界）

- 未修改任何生产代码；未修改 `internal/provider/**`、`internal/team/**`、`internal/cachelab/**`。
- 未采集任何真实 member 请求。
- 未提交、未推送、未开 PR。
