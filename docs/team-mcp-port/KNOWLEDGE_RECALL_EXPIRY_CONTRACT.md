# 知识库检索/过期契约与重大缺陷边界 —— 冻结设计文档

> 状态：**契约冻结（2026-09-06，discussion 20260906_103322 round1/2 收敛，architect-claude 落盘）**。
> 范围：本文件只冻结**设计契约与接口说明**，不改生产实现；供 coder/tester 直接实现。
> 关联代码 seam：`internal/cli/team_task_service.go`（`recallKnowledge`/`captureTurn`/`ensureKB`）、
> `internal/cli/team_member_tools.go`（`team_knowledge_recall` 工具面）、
> `internal/knowledge_base/manager`（`Query`/`Retire`/`Ingest`）、`internal/team/blackboard.go`（`BoardStore`）。
> 权威定义按 §0 优先级：用户拍板需求 > `TASK.md` > 本文档 > 讨论结论。

## 0. 与讨论结论的一致性

本文件把 discussion 20260906_103322 四成员冻结建议收敛为单一契约（对齐点逐条映射）：

| 冻结面 | tester | reviewer | coder | architect（本文件定稿） |
|---|---|---|---|---|
| 检索 API | 纯读转 Manager.Query，不新增 IncludeExpired/新检索器 | 工具包裹 recallKnowledge seam | 同左 | **§1** |
| 检索 schema | {query}（勿改 text） | {query}，禁自由 scope | {query}，MVP 不加 limit | **§1.1**：MVP=仅 {query}；limit 1..8/kinds/tags 列 post-MVP |
| scope | 团队绑定恒 ScopeTeam | ScopeTeam（team_task_service.go:219 已钉） | ScopeTeam | **§1.1**：恒 ScopeTeam，禁放宽 project/global |
| 过期语义 | Retire(tombstone)→Query 自动排除，禁硬删 | Retire+可重跑 sweep，禁 ClearTeam 做过期 | Retire 白名单 | **§2** |
| cutoff 锚 | 需单一 cutoff | 需单一 cutoff（单时间戳或 KB scope 标记） | 需 leader/architect 定 | **§2.1**：单时间戳锚 `before`；主线条规则见 §2.2 |
| 沉淀 | Ingest 增量，幂等键 ItemContentHash | captureTurn 单生产者，勿加第二直连工具 | captureTurn 单生产者 best-effort 不挂 pending | **§3** |
| 缺陷边界 | 重大缺陷→KB 无新 item+黑板收到 | SKILL 层路由约定，captureTurn 非缺陷投递口 | 同左 | **§4** |
| 终局沉淀 | 终局 re-deposit no-op 已证 | 终局性沉淀挂 pending+入口重试 | 已实现恰一次 | **§3.1**（冻结现状） |
| 测试 | boot-effect/acceptance | boot 边界效应测试 | tester 出 | **§5** |
| 分工 | coder 逻辑 / tester 测试 | architect 冻结 schema+锚 | coder 提供工具名/schema/示例 | **§6** |

## 1. 检索工具封装（冻结）

**原则：检索=纯读转发现有 seam，不新增检索器/第二生产者/IncludeExpired。**

- 工具名保持 `team_knowledge_recall`，leader 与 member 工具面同用（`team_member_tools.go:77,91`）。
- **§1.1 schema 冻结（MVP）**：仅 `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`。
  - `query` 必填、trim 后非空；空串/空白 → 错误 `query is required`（现语义）。
  - **不暴露** scope/limit/kinds/tags 参数。scope 由团队绑定恒为 `ScopeTeam`，内部 `Limit: 8` 已钉
    （`team_task_service.go:219`）；任何参数不得放宽到 project/global。
  - `limit 1..8（默认 8）`、`kinds`、`tags` 为 **post-MVP 可选**：需再评审 + Cache-guard + boot effect，不在本契约内落地。
- **§1.2 只读与降级**：`ReadOnly()==true`（`team_member_tools.go:109` 既有）；工具内部只触 `Manager.Query`，
  不得触发 Ingest/Retire/ClearTeam。KB 未启用（`kbDataRoot==""` 或 `ensureKB` 为 nil）→ 返回友好串
  `team knowledge base is not enabled for this session`，非错误。
- **§1.3 呈现边界**：检索结果只作**回合尾 tool-result** 呈现（现 `recallKnowledge` 语义——逐条
  `- [kind] title status (author)` + 首行摘要）；**绝不回灌系统提示前缀**（REASONIX cache-first 字节稳定硬规则）。
- **§1.4 分层**：不新增 `internal/tool/builtin` KB 检索工具——该注册表在 cache-impact 名单且是通用工具面；
  团队 KB 检索是团队绑定读 seam，留在 cli 团队工具面。加进 builtin 是错误分层。

## 2. 过期（expire）契约（冻结）

**语义：过期="删除"的唯一实现是 `Retire`(tombstone)；禁止物理删除，禁止以 `ClearTeam` 做过期。**

- `Manager.Retire(ctx, ids, reason)`：reason 白名单 `no_longer_true | tombstone | personal_data`
  （`model/query.go`，`Valid()` fail-closed）；未知 id **no-op**。Retire 后 item 不再 live，
  `Query` 自动排除（`queryByUpdated` 只收 `StatusLive`）。
- 如需为"主线过期"新增 reason（如 `pre_mainline`），属 **API 增项**：必须同步 `model.RetireReason.Valid()`
  + `RetireReason` 常量 + repolint/测试；**默认不新增**——现契约用 `no_longer_true` 即可表达"该知识已过时"。
- 公开签名 `Query/Retire/Ingest` **不得变**。

**§2.1 cutoff 锚（权威）**：expire 操作带**单一确定性时间戳参数 `before`**（RFC3339，闭区间取 `item.CreatedAt < before`）。
Go 侧**不 baked 主线条语义**——sweep 是 leader 维护动作，锚由调用方（leader/任务板）解析后传入，保证：
- 确定性：同一 `before` + 同一 live 集 → 同一 Retire 目标集。
- 可重跑幂等：Retire 对已 retire/未知 id no-op；重复 sweep 不产生新副作用。
- 测试可种子化：pre/post 锚两侧各造一条 item，`before` 取中间值。

**§2.2 主线条 cutoff 定义（任务板规则，供 leader/调用方解析 `before`）**：
- 任务板 = 团队权威任务记录（含 `Task.CreatedAt`，RFC3339；完成态经 `TransitionTask` 终态：reported/archived）。
- 取**当前任务之前**、**最近三条已完成主线任务**（主线=任务板中标记为主线的已完成任务；不足三条按实际条数）。
- `before = min(CreatedAt of the three)`（即这三条主线中最先开始者的 CreatedAt）。
- **边界情形**：无任何已完成主线任务 → sweep 为 **no-op**（`before` 不可解析时返回可行动提示，不静默空跑）。
- **SKILL 同步**：上述自动解析规则已写入 leader SKILL「Knowledge And Defects」段
  （`team/skills/base/leader/SKILL.md`）作为 leader 操作程序——执行 `team_knowledge_expire` 前按本规则
  从任务板取锚；契约文字与 SKILL 不得漂移（改任一侧须同步另一侧）。
- 注：Go 侧 `team.Task` 现无 `mainline` 标记字段；如需 Go 侧自行解析，须由 leader 先裁决
  "主线条"如何在任务板/任务记录中表达（新增字段 or 复用 role/标签），**本契约不强加**——Go 实现侧只消费 `before`。

**§2.3 实现 seam（最小只读枚举 helper）**：`Retire` 收 id list，而 `Manager` 现无"按 CreatedAt 枚举 live id"的
公开只读路径（`store.List` 内部可用但非公开）。契约冻结一个**最小只读 helper 签名**（coder 实现，不改公开行为）：

```go
// ExpireBefore retires every live item of this team whose CreatedAt is strictly
// before cutoff. Leader-only. Deterministic and idempotent: re-running with the
// same cutoff retires nothing new (unknown/already-retired ids are a no-op).
// reason must be on the whitelist.
func (m *Manager) ExpireBefore(ctx context.Context, cutoff time.Time, reason model.RetireReason) (int, error)
```

- 该 helper 是 leader-only 动作的**底层原语**；上层入口（leader 工具 or 管理面）见 §2.4。
- 不进 recall 工具 schema、不改 cache-stable 前缀。属 API 增项：过 `model`/repolint，公开签名只增不删。
- 空结果不是错误（返回 `n=0`），与 `Retire` no-op 语义一致。

**§2.4 上层触发面（已裁决 B，2026-09-06 落地）**：
- **B 已实现**：leader 工具 `team_knowledge_expire` 已在 leader 工具面落地（`team_member_tools.go:78`），
  schema `{before 必填 RFC3339, reason?}`，reason 默认 `no_longer_true`；member 工具面不含该工具。
  Execute → `service.expireKnowledge`（`team_task_service.go:268`）→ `Manager.ExpireBefore`，
  结果带数量（`retired N <reason> knowledge item(s)`）。
- A（管理面直调）不另做——B 即触发面；触发面权限：**仅 leader**；成员工具面永不出现写/过期工具（§6.2）。
- **§2.3 helper 落盘**：`Manager.ExpireBefore(ctx, before, reason) (int, error)` 已实现
  （`manager/manager.go:121`）：校验 before 非零 + reason 白名单（fail-closed）→ enqueue `jobKindExpire`
  → `Flush` 等 job 处理 → 返回本批 retired 计数。worker `processExpire`（`worker.go:111`）按
  **`KnowledgeItem.CreatedAt < before`（严格 before）** 筛选 live item（非 UpdatedAt）→ 复用
  `processRetire`（tombstone、未知 id no-op）→ 经 `expireResult` 记录计数。重复调用幂等：已 retire 项
  不再 live → 返回 0。团队隔离：Manager 构造绑定单团队，`st.List()` 只扫本团队。

## 3. 沉淀（deposit）契约（冻结现状）

- **回合尾单生产者 = `captureTurn`**（`team_task_service.go:186`，member report 后调用）：best-effort，
  非终局、**不挂 pending**；`Ingest` 幂等键 = `ItemContentHash`（L1 精确去重，同 delta 二次不新增）。
- **终局沉淀 = 讨论结论 `DiscussionDeposit`**（`team_discussion.go`，`deliverPendingDeposit` 入口重试，
  pending→delivered 恰一次，确定性 body + sha256，re-deposit no-op）——**已实现且被测试锚定，冻结不改**。
- **禁止**：加第二条直连 Ingest 工具（双生产者→双 deposit→supersede 冲突）；把 captureTurn 当缺陷投递口（§4）；
  成员侧直连 `Manager.Ingest`。
- "变更才沉淀"由调用方只喂变更文本决定（成员基线/系统文件变更增量 → 只把变更内容交给既有单生产者通道），
  不靠 manager 判语义。

## 4. 重大缺陷只进共享黑板 —— 事件边界（冻结）

- **定义**：重大缺陷 = 已复现、可定位、阻塞或劣化已交付行为的缺陷（对应 `DEFECT_FIX_LIST.md` 的
  P0/P1 类目），**不是**常规知识/结论/进度。
- **路由约定（SKILL 层，代码不判语义）**：成员/leader 发现重大缺陷时，通过共享黑板既有通道写入
  **结构化板事件**（`BoardStore.Append`，`internal/team/blackboard.go:195`），并附稳定缺陷标记与
  `task_id`/`summary`；**绝不**经 `captureTurn`/`team_knowledge_recall` 写入 KB，KB 永不出现缺陷型 live item。
- **代码边界**：Go 侧**不新增语义分类器**判断"这是不是缺陷"。边界由角色 skill 文本（leader/member
  SKILL.md）与工具面分工表达：KB 检索/沉淀工具只服务知识；缺陷走黑板 append/report。
- **测试注入点**：黑板通道以 `BoardStore.Append`（或既有 report/board 双写 seam）为可断言注入点——
  缺陷事件 → 断言 KB 无新 item + 黑板收到（§5.4）。

## 5. 测试契约（boot 边界效应测试，tester 出）

测试统一走 **boot 边界真实组装**：临时 dataRoot + `teamTaskService`（`setKnowledgeDataRoot` +
`forTeam`），`-race`。覆盖：

- §5.1 检索只返 live：含已 Retire item 必排除；同 query 多次一致。
- §5.2 pre/post-cut 种子：pre/post `before` 两侧各造 item（带 `Flush`），`ExpireBefore` 后 Query 只回 post-cut。
- §5.3 幂等：同 delta `Ingest`×2 → 计 1（L1）；`ExpireBefore` 双跑 → 第二次 n=0、live 集不变；终局 re-deposit no-op。
- §5.4 缺陷边界：缺陷事件走黑板 Append → KB 无新 item + 黑板可读回该事件。
- §5.5 工具面：member backend 工具集含 `team_knowledge_recall`、**不含**任何写/过期工具；`ReadOnly()` 为真。
- §5.6 cache-guard：recall/expire 不改变 cache-stable 前缀字节（boot effect 断言 provider 可见前缀稳定）。
- disabled 友好串：`kbDataRoot==""` 时 recall 返回友好提示，非错误。

## 6. 权限与分工（冻结）

- §6.1 读（recall）对 Leader/Member 开放且只读；写（沉淀）只经 `captureTurn`（回合尾）与讨论终局
  `DiscussionDeposit`；**过期/ExpireBefore 仅 leader**。
- §6.2 成员/leader 工具面均不暴露 Ingest/Retire/ClearTeam/ExpireBefore 直调工具；成员永不拿到写工具。
- §6.3 分工：**architect** 冻结 schema/scope/cutoff 锚语义 + §2.3 helper 签名（ExpireBefore 由实现 lane
  按 §2.3/§2.4 B 落地，2026-09-06）；**coder** 提供 SKILL.md 工具名/schema/示例给文档 lane；
  **文档 lane**（非 coder）落 leader/member SKILL.md recall/缺陷路由指引（cache-guard）；**tester** 出
  §5 全套；**reviewer** 查 gofmt/vet/repolint（既有分支红债不扩，新文件不得增 budget-0 违规）。

### §6.3.1 SKILL.md 落盘状态（2026-09-06，architect 按 leader 指令闭合）

Leader/Member SKILL.md 已写入两处闭合并可被 §5 断言：

- **leader**：`team/skills/base/leader/SKILL.md`「Knowledge And Defects」段——按需
  `team_knowledge_recall`（query-only/team scope/limit 8/read-only）+ 重大缺陷进黑板不进 KB
  + `team_knowledge_expire` 自动 cutoff（任务板最近三条已完成主线 CreatedAt 取最小，
  不足三条按实际、无主线 no-op 不调用，§2.2）。
- **member**：`team/skills/base/member/SKILL.md`——Execute 段 recall 指引 + Blockers 段
  重大缺陷进黑板（defect marker + task id，永不经 KB/recall 知识面）。

**可测试 seam（供 §5.4）**：黑板写入 seam = `BoardStore.Append`（`internal/team/blackboard.go:195`，
`EventConclusion`/`EventReport` + defect marker + task_id）；KB 侧断言 = 缺陷事件后
`captureTurn`/`Ingest` 未新增 live item（Query live 集不变）。Skill 文本断言 = §5.5 工具面
（member/leader backend 含 recall 不含写工具）+ §5.1/§5.4 的 KB/黑板计数。

## 7. 未决（待 leader）

1. ~~`before` 权威取值~~ → **已裁决**：§2.4 B 工具由 leader 触发时传 RFC3339 `before`；主线条任务板解析规则仍由 leader 在调用时给出锚值（§2.2 供参考）。
2. ~~上层触发面 A/B 选型~~ → **已裁决 B**（2026-09-06 落地，见 §2.4）。
3. 是否新增 `RetireReason`（默认否）。
4. schema 是否扩 limit（MVP 否，post-MVP 待评审）。
