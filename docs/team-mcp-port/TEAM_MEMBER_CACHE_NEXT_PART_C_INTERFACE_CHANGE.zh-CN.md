# Part C 接口变更说明：维护诊断投影（协调裁定 §2.4）

> 依据：`TEAM_MEMBER_CACHE_NEXT_ROUND_COORDINATION_DECISIONS.zh-CN.md` §2.4。
> 裁定指定 **Agent C 为唯一实现者**，并要求「兼容性、omitempty/缺失语义及与 Part B 数据字典的合并
> 由 C 在实现前写入接口变更说明」。本文即该说明，**写在实现之前**。
> 日期：2026-09-25。快照 `f53c4bf91d17b9d45fbf8a31656a8eed9cfffdeb`。

---

## 1. 变更内容

把三个**已存在**的 receipt 诊断值投影到成员记录 `team.MemberCacheRequest`：

| 值 | 语义 | 现有来源 |
|---|---|---|
| `maintenance_state` | 该次维护决策落在七个状态中的哪一个 | `ContextMaintenanceReceipt.MaintenanceState` |
| `headroom_tokens` | 装上的视图还剩多少 room（对 fallback 的边界而言） | `ContextMaintenanceReceipt.HeadroomTokens` |
| `reduction_ratio` | 该次折叠移除了请求的多大比例 | `ContextMaintenanceReceipt.ReductionRatio` |

**不新增其他 sidecar 字段。** 裁定 §2.4 列了三项，就只做这三项。

## 2. 为什么是「投影」而不是「新事实」

三个值**已经在** `ContextMaintenanceReceipt` 上（`internal/agent/projection.go:113-122`），
并且**已经**通过 `ContextMaintenanceSnapshot()` 暴露给进程内读者
（`internal/agent/context_status.go:24-29`）。本变更**不产生第二个真相源**：

- 不重算：值从 receipt 原样读出，不在记录侧重新推导；
- 不新增行为消费：没有代码读这些字段做决策；
- 不改 receipt：`internal/agent/**` **不在本变更的写集内**。

## 3. 怎么传到记录里（两处读点，各取所需）

`memberUsagePublisher` 有两条路径，**各读各的、互不替代**：

| 路径 | 读法 | 得到什么 |
|---|---|---|
| **发布文档**（`.usage.json`，2s 节拍） | `ctrl.ContextMaintenanceSnapshot()` → `LastReceipt` | 该 writer **当前最新**的维护决策 |
| **逐请求记录**（`.cache_requests.jsonl`，每请求） | 事件流里**最近一次** `ContextMaintenance` | 该请求**之前**发生过的那次决策 |

**逐请求路径需要事件携带这三个值**，因为请求记录是在**事件到达时**写的，
而快照是「此刻最新」——两者在时间上不是一回事。因此本变更给
`event.ContextMaintenance` 加同样三个字段，让 `emitContextMaintenance` 把它们带上。

**这是本变更唯一触碰 `internal/event` 与 `internal/eventwire` 的原因**：
不是为了记录，是为了让记录能拿到**正确的那个**决策。

### 3.1 归属语义（必须写清，否则会被误读）

逐请求记录上的 `maintenance_state` 读作：

> **本请求之前、该 writer 最近一次维护决策的状态。**

**不是**「本请求触发的维护」，也**不是**「本请求之后发生的维护」。一次决策与它之后的
第一个请求相关，这正是「按样本核验维护状态与触发结果」需要的口径（裁定 §2.4 原文）。

明确不成立的读法：
- ❌ 本记录的 `maintenance_state=applied` **不代表**本请求引起了这次折叠；
- ❌ 一条记录**没有**这三个字段 **不代表**该会话没有维护（可能是决策发生在本记录之后）；
- ❌ **不得**用这些字段按记录聚合出「维护次数」——那是 `MaintenanceCost` 计数器的职责。

## 4. 缺失语义（裁定 §2.4 的硬要求）

**缺失必须保持为「未观测」，不得补零、不得推断。**

| 情形 | 记录里的样子 | 读法 |
|---|---|---|
| 该 writer 在本请求前**没有任何**维护决策 | 三个字段**全部缺键** | 未观测 |
| 决策存在但状态是 `below_boundary` | `maintenance_state` 有值；headroom/reduction **缺键** | 状态已观测；两个量**不适用** |
| 规范化 TOML / 旧行 | 缺键 | 未观测（**不是** 0） |

实现上的落点：
- 三个字段都是 `omitempty`，且 `maintenance_state` **为空字符串即视为未观测**；
- `headroom_tokens` 与 `reduction_ratio` 只在 `maintenance_state` 非空时写，
  且各自 `omitempty`——但**`0` 与「缺键」必须可区分**：headroom 完全可能是 0
  （fold 恰好落在边界上），所以 nil/缺失用「键不存在」表达，而不是用 0 表达。

> **实现取舍**：`headroom_tokens` 用 `*int`、`reduction_ratio` 用 `*float64` 会最直白，
> 但成员记录现有字段**一律是值类型 + omitempty**（`internal/team/cacherequest.go`）。
> 为不引入该文件里没有的先例，实现采用**一个伴随的 `maintenance_observed` 布尔**
> 作为「这三个字段是否已观测」的总开关——与既有 `DiagnosticsAvailable`、
> `MessagesComparable`、`UsageOraclePresent` 的写法**同一形状**。
> 读者必须先看 `maintenance_observed`，再读三个值。

## 5. 与 Part B 数据字典的合并

B 的字典（`TEAM_MEMBER_CACHE_NEXT_PART_B_DATA_DICTIONARY.zh-CN.md`）**尚未收录**这三项
（§1 落位表 #12 只到 `prefix_change_reasons` / `MessagesRewritten`）。合并方式：

- 本变更**新增**字典条目，编号沿用 B 的提案编号序列（N-6），**不重写** B 的任何既有条目；
- **不触碰** B 定义的任何分母、unknown 分类、质量/延迟口径或敏感性规则（裁定 §5）；
- B 的 N-1..N-4 **本轮不实现**（裁定未指定它们，且 §3 第 3 步把字典合并留给 B）。

**唯一实现者的边界**：本文实现 §2.4 的三个字段；
`internal/team/cacherequest.go` 与 `internal/cli/team_usage_publish.go` 是实现点，
`internal/event` / `internal/eventwire` 只为承载这三个值而改。**A 与 B 的写集不被触碰。**

## 6. 兼容性

| 面 | 影响 |
|---|---|
| JSON 兼容 | **纯 additive**：旧读者忽略新键；新读者对旧行读作「未观测」 |
| `schema_version` | **不变**（additive 字段；B 的字典 §2 对 N-1..N-4 也是这条策略） |
| 事件线协议 | `ContextMaintenance` 加三个 `omitempty` 字段；前端可忽略 |
| 存储 | 无新文件、无迁移、无默认值变化 |
| 行为 | **零**：没有代码读这些字段做决策 |

## 7. 回滚

删除 `MemberCacheRequest` 的三个字段与 `maintenance_observed`、
`event.ContextMaintenance` 与 eventwire 镜像的三个字段、以及 `observe` 里的记录逻辑。
**无持久化影响**：旧记录本就没有这些键。

## 8. 实现记录（本文之后追加）

实现按上述说明落地。**实测编码形状**（`json.Marshal` 一个只设了这三个字段的记录）：

```json
{"maintenance_observed":true,"maintenance_state":"recovered"}
```

**读法**：`maintenance_observed=true` 且 `headroom_tokens` 键不存在 → **值为 0**（该次 fold 恰好没买到 room）。
这**不是**「未观测」——未观测的样子是**连 `maintenance_observed` 都没有**。
门是唯一的判据，缺键不再承担第二个含义。这与本文件既有约定同形
（`messages_comparable` 之于 `messages_rewritten`、`diagnostics_available` 之于 prefix 字段）。

**改动清单**：

| 文件 | 改动 |
|---|---|
| `internal/event/maintenance.go` | **新增**：`ContextMaintenance` 从 `event.go` 提取出来（原文 798 行 + 新字段触顶 800 行上限），加三个 `omitempty` 字段 |
| `internal/event/event.go` | 该类型移出（782 行） |
| `internal/eventwire/wire.go` | 镜像三个字段 + `ToWire` 逐字段赋值 |
| `internal/agent/context_receipt.go` | `emitContextMaintenance` 带上三个值 |
| `internal/cli/team_usage_publish.go` | `memberMaintenanceObservation` + `rememberMaintenance` + 记录时盖章 |
| `internal/team/cacherequest.go` | 四个字段（门 + 三值） |
| `internal/cli/team_usage_observe_test.go` | 5 个新守卫 |

**新守卫**（`internal/cli`）：决策随后的请求带上它、无决策保持未观测且**不写键**、
记录的 0 与「缺键」可区分、只盖在**其后**的请求上、维护事件仍原样转发给前端。

## 9. 本文未做

- 未发起任何 live 请求；
- 未改默认值、统计分母、请求字节或工具 schema；
- 未触碰 A 的 `internal/agent` 行为实现（只在 `context_receipt.go` 的**发射点**加三个字段转发）
  与 B 的数据字典/分析口径；
- 未提交、未推送。
