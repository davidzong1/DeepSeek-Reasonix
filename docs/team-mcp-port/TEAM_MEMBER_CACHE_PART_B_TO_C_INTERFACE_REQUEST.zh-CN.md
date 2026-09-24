# Part B → Part C：`TeamRequestObservation` 字段落位请求（B-C1）

> 发起：Agent B（`TEAM_MEMBER_CACHE_PART_B_P0_INVENTORY.zh-CN.md` §4 的 B-1）。日期：2026-09-24。
> 依据：`TEAM_MEMBER_CACHE_POST_JOINT_3AGENT_PLAN.zh-CN.md` §4 M1 字段契约、§3 Agent C 建议写集。
> **本文只提请求，不实现。** `internal/team/cacherequest.go` 在方案里是 Agent C 的写集，B 不触碰。

## 1. 为什么需要 C 来落

M1 契约的 `TeamRequestObservation` 有 6 项在代码里没有落点（B 的 P0 盘点 §3 逐项核验）。其中
`session_id` / `attempt` / `concurrency_level` 三个字段的**记录位置**是 `team.MemberCacheRequest`
（`internal/team/cacherequest.go`），该文件在方案 §3 的 Agent C 建议写集内。

B 侧写集只有实验脚本、脱敏日志和报告文档；B 已在 P0 阶段确认**不加字段也能开始 pilot**（缺失按
coverage 缺口如实披露，不填 0、不推断）。但 pilot 之后的**正式分层实验**（方案 §5 阶段 P3）要求
「按会话阶段、并发分层」，而这两个维度**没有字段就无法分层**，只能退化为描述性结果。

## 2. 请求落位的字段（按优先级）

| 优先级 | 字段 | 落点 | 上游真值 | 代价 |
|---|---|---|---|---|
| **P0** | `session_id` | `MemberCacheRequest` | `event.Event.SessionID` **已经存在**（`internal/event/event.go:439`），由 turn ledger 在 `internal/turnevent/ledger.go:525` 盖上 | 只需在 `memberCacheRequest`（`internal/cli/team_usage_publish.go:360`）多读一个已存在的字段 |
| **P0** | `concurrency_level` | `MemberCacheRequest` | 无现成字段；最近的运行时真值是 `teamTaskService.busyMembers()`（`internal/cli/team_task_service.go:405-420`，返回 runtime 确认在跑的成员集合），但 `memberUsagePublisher` 目前**不持有**该句柄 | 需要给 publisher 加一个只读句柄，并在 `observe`（`team_usage_publish.go:207`）采样；**不得在请求路径上取 runtime 锁** |
| **P1** | `attempt` | `MemberCacheRequest` | 无逐 attempt 身份。`event.AttemptID` 存在但只用于 streaming 显示，`emitTurnUsage` 不写它 | 需要决定 attempt 身份的来源；B 建议**只记录 `request_count` 与重试关系**（`RequestCount > 1` 已表达聚合），不新造 attempt 号 |
| **P1** | `maintenance_phase` | 可派生，不新增字段 | `CacheDiagnostics.PrefixChangeReasons` 携带 `cachereason` 闭集值；`team.CacheRequestStages` 已把它映射为 `post_rewrite`（`internal/team/cachereport.go:96-108`） | **零代价**：B 可在报告侧派生，C 不需要动字段 |
| **P2** | `task_family` | 不在记录里 | **代码里完全不存在**该概念 | B 由实验驱动方**冻结并登记**任务族，不进记录 |

## 3. 三个必须随字段一起交付的语义约束

C 在落 `session_id` 时，以下三条是 B 侧分层的前提，请一并写进字段注释：

1. **空值 ≠ 无 turn**。turn 外事件走 `publishOutsideTurn`（`internal/control/turn_event_publication.go:10-23`），
   该路径**不盖** `TurnID`/`Sequence`/`SessionID`。因此 `session_id` 为空只表示「未观测到」，不是「该请求不属于任何会话」。
   B 的报告会把空值计入 coverage 缺口，不归入任何 session。

2. **`session_request_seq` 是 publisher 进程级的，不是 session 级的**。
   `memberUsageObservation.seq`（`internal/cli/team_usage_publish.go:93-99,217`）在每次成员 backend
   重建时归零，而记录里**没有披露这一点**。同 `session_id` 的连续请求若跨越一次重建，序号会回退。
   B 需要 C 明确：是补一个「writer 世代」标记，还是在字段注释里声明该限制（B 倾向后者，代价为零）。

3. **`hit == 0` 一律读作「未报告 cache read」**（A v1 契约 §2.3、A 的矩阵 §2.1）。
   新的未决形状（省略 split 的全命中，A 矩阵第 17 行）当前读数是 `hit=0, miss=全部`。
   字段注释不得把它描述为冷启动。

## 4. 请求的边界

- B **不要求** C 修改 `internal/provider/**`（那是 A 的写集）。
- B **不要求** C 修改请求字节、缓存策略、上下文裁剪或成员隔离。
- B **不要求** C 为 B 的实验改变统计分母或排除规则。
- 若 C 判定某字段不应落（例如 `attempt` 无法诚实取得），B 接受，并会把该维度标为
  `insufficient_provenance` 而不是用近似值填充。

## 5. B 侧的对接方式

- C 落字段后，B 的 pilot/正式实验**照常读 `.cache_requests.jsonl`**：新字段 `omitempty`，旧行缺键。
- B 的报告会同时给出「字段覆盖率」与「该维度的分层结果」，字段缺失时分层退化为
  `insufficient_provenance` 并如实披露，不静默池化。
- B 不会把 C 的实验 journal（`internal/cachelab` 的 JSONL）与成员账本相加（A v1 契约 §8.1 已冻结）。

## 6. 本文未做

- 未修改任何文件。未采集任何真实请求。未提交、未推送、未开 PR。
