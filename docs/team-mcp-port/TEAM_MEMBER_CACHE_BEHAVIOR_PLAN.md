# Part B：Team member 缓存行为优化与 1M 基准执行方案

> 范围：Team session 中 member 的 provider prompt-cache 行为，聚焦稳定前缀、工具 schema、compaction/fold 以及可复现基准。
>
> 本文是 `TEAM_MEMBER_CACHE_OPTIMIZATION_PLAN.md` 的 Part B 执行方案。Part A 负责逐请求 usage/诊断发布；本文不重复设计其数据发布协议，也不修改主计划。
>
> 日期：2026-09-24。**执行记录见 §10**（含实测证据、本轮修复、未完成项与 Part A 交接）。

## 1. 目标与非目标

### 1.1 目标

1. 证明 member 的低命中来自哪一类变化：稳定前缀变化、工具 schema 尾部、内容增长、fold/compaction，还是 provider 的 TTL/路由行为。
2. 在不牺牲工具可达性和硬上下文上限的前提下，让同一 member、同一模型/路由的连续 warm 请求保持可复用前缀。
3. 将 schema 收缩限制在可解释的 fold 边界，避免思考循环内动态变更 `ToolsHash`。
4. 建立从 `32K` 到接近 `1M` 的阶梯基准，分别报告冷启动、warm、fold 后和 TTL 冷却样本。

### 1.2 非目标

- 不把 `context_used` 当作 provider 的 `prompt_tokens`；1M 验收以真实请求 usage 为准。
- 不把 session 累计命中率当作单次请求命中率；累计率、最近请求率、成员简单平均和 token 加权平均必须分开。
- 不跨 member 共享 provider cache；member 身份、权限和上下文仍然隔离。
- 不为了命中率改写已经命中的 system prompt、member identity 或历史前缀。
- 不在本 Part B 重做逐请求诊断或 Team owner usage 发布；相关字段、序列化和展示由 Part A 负责。

## 2. 当前实现盘点：已做与不能重复做

### 2.1 已经在当前代码实现的路线

| 路线 | 当前实现 | 代码锚点 | 对本计划的含义 |
|---|---|---|---|
| member 延期工具过滤 | member provider-visible surface 会移除 `memberDeferredTools`，工具仍保留在 registry，可经 `use_capability` 到达 | `internal/boot/tool_surface.go`: `memberDeferredTools`、`dropMemberDeferredTools`、`applyUnifiedProviderToolSurface` | **不能重复实现**“把延期工具从 member schema 移除”；只需验证生效构建、实际 schema 和 token 足迹 |
| schema 归一化 | 工具 schema 在计算 hash 前排序，避免注册顺序导致假变化 | `internal/agent/cache_shape.go`: `normalizeToolSchemas` | 不再用排序作为新优化；实验必须记录排序前后的实际 wire schema |
| 前缀诊断 | 已记录 `SystemHash`、`ToolsHash`、`StablePrefixHash`、`PrefixChangeReasons`、`ToolSchemaTokens` | `internal/agent/cache_shape.go`: `CaptureShape`、`CompareShape`；`internal/agent/run_usage.go`: `emitTurnUsage` | 不重复发明 hash；Part A 只需把已有诊断接入 member usage |
| fold cache alignment | compaction 请求在稳定 system/tools 前缀后追加一条 compaction instruction；projection commit 后再继续请求 | `internal/agent/compact.go`: `compactionInstruction`；`internal/agent/compact_projection.go`: `checkpointProjectionMessages`、`planFoldRegion` | 不重写 fold 形态；重点补 effect test，确认 fold 后原因是预期的 `compact_auto`，而非 system/tools 变化 |
| warm-cache 延迟 compaction | 已有 `cache_aware_compaction`，warm 时将触发点推迟到 `hardInputCeiling`，硬上限仍优先 | `internal/agent/compact.go`: `compactTrigger`、`warmCache`；`internal/agent/agent.go`: `agentConfig.CacheAwareCompaction`；`internal/agent/cache_aware_compaction_test.go` | 不再添加第二套 warm 阈值；只做默认关闭的灰度、1M 压测和回滚验证 |
| 单一自动 compaction 阈值 | 当前以 `compact_ratio` 为主要自动维护边界，默认 `0.80` | `internal/agent/compact.go`: `defaultCompactRatio`、`compactTrigger`；`internal/config/config_harness.go`: `CompactRatio` | 不恢复旧 soft/snip/force 多阈值路径；实验只调 `compact_ratio` 或显式开关 |
| 工具结果有界内容 | provider-visible `Content` 与本地 `RawContent` 分离，避免大工具结果线性污染后续请求 | `docs/research/cache-aware-compaction-design.md`；相关实现按该设计的 `Content`/`RawContent` 路径验证 | 不以重新截断 canonical transcript 作为命中率修复；只验证 wire 内容是否符合上限 |

### 2.2 尚未实现、可作为后续改动的路线

| 路线 | 允许的实现方式 | 默认值 |
|---|---|---:|
| fold 对齐的 schema pruning | 在 projection commit 时记录可选工具集合，之后的 `providerToolSchemas()` 仅过滤 provider-visible schema；强制核心工具和 `use_capability` 保留 | 关闭 |
| 逐请求行为诊断发布 | 将 `CacheDiagnostics` 的 hash/reason/schema token 与 request prompt bucket 发布到 member usage；由 Part A 落地 | 仅观测，不改变行为 |
| provider cache scope/TTL 对照 | 用相同 prompt shape 做短间隔、长间隔、不同 route/account pool 对照 | 不改代码 |

**重要边界**：schema pruning 尚未因 `1M_CONTEXT_P2_P3_DESIGN.md` 中的设计文字而自动存在；必须先查当前 `providerToolSchemas()` 和 projection 状态，再决定是否落代码。设计文档不是实现证明。

## 3. 优化顺序与代码实施路线

### 阶段 B0：冻结基线与生效面核对

**先做，且不改行为。**

1. 在同一 member、同一模型、同一路由下采集至少 20 个连续请求；记录真实 `prompt_tokens`、`cache_hit_tokens`、`cache_miss_tokens`、`CacheWriteTokens`、`StablePrefixHash`、`ToolsHash`、`ToolSchemaTokens` 和 rewrite reasons。
2. 对每个请求同时记录 provider-visible 工具名集合，确认 `memberDeferredTools` 没有进入 wire schema；检查 `use_capability` 能否调用被延期工具。
3. 将请求分为：首请求、稳定 warm、工具调用后、fold 前、fold 后、超过 TTL 后恢复；禁止用单个 session 累计率替代这些桶。
4. 核对 `context_used` 与最近请求 `prompt_tokens` 的差异，1M 桶只使用真实 `prompt_tokens`。

**退出条件**：能定位每个低命中请求的 `ToolsHash` 是否变化、是否有 content rewrite reason、是否为首请求/TTL 冷请求；无法归因的样本标记 `unknown`，不得直接进入 schema 优化。

### 阶段 B1：稳定前缀先行

**优先级最高，因为 schema 缩短无法修复前缀整体被重置。**

实现/核查顺序：

1. 保证 member backend、模型/route、system prompt、member identity 和 provider-visible tools 在连续请求中不变；变化必须发生在新 session 或显式 fold 边界。
2. 检查 `internal/agent/finalization.go`: `providerToolSchemas`、`internal/agent/sampling_request.go` 的 request 构造，以及 `internal/agent/run_loop.go` 的重试/工具循环，确认没有按思考轮次动态排序、增删 schema。
3. 对每个 `PrefixChangeReasons` 建立白名单：`compact_auto`、明确的 `snip`/`rewind_truncate` 等内容变化可解释；`system`、`tools`、非预期 `session_context` 变化必须阻断灰度并定位调用点。
4. 为 fold 增加 effect test：fold 前后 system/tools hash 不变，预期只有内容重写原因；同一 fold 内 `ToolsHash` 最多变化一次。
5. 如果 provider 仍低命中而本地 stable prefix 完全不变，转入 B4 的 TTL/route 对照，不继续改 prompt。

**不可接受的修复**：每轮重建带时间戳的 system prompt、按最近工具调用动态重排 schema、将 member 临时状态塞入 system prompt、或在 cache miss 后重写整个历史。

### 阶段 B2：验证现有 member schema 收缩，再决定是否新增 pruning

1. 先以当前 `dropMemberDeferredTools` 为 control，测量实际 `ToolSchemaTokens`、schema token 占 prompt 的比例和固定 miss 尾巴。
2. 若工具尾巴已小且 `ToolsHash` 稳定，不新增 schema pruning；继续 B3/B4。
3. 只有当 schema 占比高、且低命中集中在工具尾部时，才实现 fold 对齐 pruning：
   - 新增配置开关 `schema_prune`，默认 `false`，只对 member 生效；leader/普通 session 保持原 surface。
   - 在 projection/compaction commit 记录“本 fold 已实际调用”的可选工具名集合；核心工具集合和 `use_capability` 永远保留。
   - `providerToolSchemas()` 每次读取已提交集合，但回合中不更新集合；工具集合只能在新 session 或 fold commit 变化。
   - 未进入 visible schema 的工具仍由 `use_capability` 按名称解析和执行；不可达即回滚开关，不接受只看 token 的成功。
   - 记录 pruning 前后 `ToolsHash`、schema token 差值、工具调用成功率；每个 fold 最多一次 tools hash 变化。
4. 该路线必须在 B1 基线稳定、且有足够 schema token 证据后实施；不能因为 1M 变大就默认开启。

建议实现锚点：`internal/agent/finalization.go`: `providerToolSchemas`、`internal/agent/compact_projection.go`: projection commit；若状态需要持久化，沿现有 projection schema 做 additive 字段并保留旧 sidecar 读取。

### 阶段 B3：compaction/fold cache alignment

1. 保持当前单一 `compact_ratio` 路径；不要恢复已退役的 soft/snip/force 自动维护。
2. 默认关闭 `cache_aware_compaction` 做对照；开启后验证 warm request 会延迟到 `hardInputCeiling`，但不会越过硬上限。
3. 在 `internal/agent/compact.go`: `compactTrigger` 和 `warmCache` 增加/保留行为测试：
   - 关闭开关：在 `compact_ratio` 处按原路径 fold；
   - 开启开关且 warm：允许延迟，但在硬顶前仍必须能安全维护；
   - 无 cache split、冷启动、TTL 冷恢复：按 cold 路径，不假设 warm；
   - ablation/compaction 关闭：不被 warm 开关绕过。
4. 在 `internal/agent/compact_projection.go`、`internal/agent/compact_commit.go` 验证：canonical transcript 不被破坏；provider-visible projection 至多一条 summary；fold 后继续请求时 stable system/tools prefix 不变。
5. 若 fold 后一次预期冷请求造成的 miss 成本大于避免的 compaction 成本，记录为 provider/窗口权衡，不通过提前重写前缀掩盖指标。

### 阶段 B4：provider cache scope/TTL 与路由排除

当 B1–B3 显示本地 prefix stable、schema 尾部可控、fold 原因可解释而 warm 命中仍低时，才执行：

1. 同一 prompt shape、同一模型做短间隔与长间隔请求；长间隔仅用于识别 TTL，不纳入 warm KPI。
2. 固定 endpoint/account pool，再与实际 Team route 对照；若 route 变化导致 hash 相同但 hit 不同，归因为 provider scope/affinity。
3. 对 DeepSeek/OpenAI 等 provider 分开报告，不把不同 cache 计费/返回语义混成一个平均值；解析锚点包括 `internal/provider/openai/openai.go` 的 cache usage 字段映射。
4. provider 不支持 cache split 或返回不完整时，标记 `unknown/not applicable`，不以估算值验收行为优化。

## 4. 实验矩阵

所有实验固定：member role、workspace、模型、endpoint、route、工具调用脚本、输出上限和随机种子（若 provider 支持）。每个 cell 至少 1 个 cold + 5 个 warm 请求；1M 关键 cell 至少 20 个 warm 请求，另加 3 个 fold 后请求。

| 维度 | Control | Variant A | Variant B | 目的 |
|---|---|---|---|---|
| 前缀 | 当前 system/tools，连续 append-only | 注入一次已知 system 变化 | 注入一次已知 tools 变化 | 校准 hash/reason 对命中下降的敏感度 |
| member schema | 当前 `dropMemberDeferredTools` | 全量 member surface（仅测试/诊断，不上线） | fold-aligned `schema_prune=true` | 量化已有过滤和新 pruning 的边际收益 |
| compaction | `cache_aware_compaction=false` | `true` 且 warm | `true` 但 cold/no receipt | 验证 warm 延迟只影响目标路径 |
| context size | 32K、64K、128K、256K、512K、768K、900K、接近 1M | 同一阶梯 | — | 观察命中率是否随真实 prompt 增长变化 |
| 生命周期 | 首请求 | 2 秒内连续 warm | 超过疑似 TTL 后恢复 | 分离冷启动、warm、TTL 冷却 |
| fold | 无 fold | auto fold | manual/forced fold | 区分预期维护 miss 与非预期 rewrite |
| 工具负载 | 无工具 | 小 schema、少调用 | 大 schema、多调用、大结果 | 分离 schema 尾部和消息内容增长 |

每个 cell 输出：`prompt_tokens`、hit/miss/write、单次命中率、累计命中率、stable/tools hash 变化次数、rewrite reasons、schema token、fold 次数、工具成功率、hard ceiling 是否触发。报告 P10/P50/P90、简单平均和 token 加权平均。

## 5. 灰度开关与配置契约

| 开关 | 当前状态 | 灰度策略 | 关闭/异常行为 |
|---|---|---|---|
| `agent.cache_aware_compaction` | 已实现，默认 `false` | 仅单一 member、单模型、10% 会话开始；确认硬顶安全后扩大 | 恢复原 `compact_ratio` 触发，不影响 transcript |
| `agent.compact_ratio` | 已实现，默认 `0.80` | 只在基准中作为实验变量；生产先保持默认 | 恢复上一个已验证值；范围仍受配置校验约束 |
| `agent.schema_prune` | **待实现**，默认 `false` | 仅 member、仅新 session/fold 边界；先 1 个 member，再 10% Team | 立即回到全量当前 member schema；保留 projection/canonical 数据 |
| Part A 逐请求诊断发布 | 由 Part A 实现 | 先观测全量，行为开关独立 | 发布失败只丢 telemetry，不阻断请求 |

> **状态修正（2026-09-24 执行）**：`agent.cache_aware_compaction` 在修复前**不是**"已实现可灰度"——`Options.CacheAwareCompaction` 在 `New()` 组装 `agentConfig` 时被丢弃，该配置键在任何构建里都不生效（详见 §10.4）。`agent.visible_window_tokens` 同样被丢弃。两者已修复。

开关要求：

- 不把多个行为变化绑在一个不可拆分的总开关；B1 稳定性修复、B2 schema pruning、B3 cache-aware compaction 分开回滚。
- 配置缺失按关闭处理；未知 sidecar 字段按 additive 兼容处理。
- schema pruning 不允许在一轮中途热切换；配置变更需新 session 或明确 projection commit 边界。
- 所有灰度样本带 flag snapshot，避免把不同策略混入同一累计率。

## 6. 测试与验证清单

### 6.1 单元/效果测试

- `internal/boot/member_visible_tools_test.go`：延期工具不出现在 member provider surface；leader/普通 role 不变化；延期工具仍可执行。
- `internal/boot/team_identity_stability_test.go`：不同 build/restart 的 member identity 前缀字节稳定。
- `internal/agent/cache_shape_test.go`、`internal/agent/cache_diagnostics_test.go`：schema 排序稳定；tail-only 变化不误报 stable prefix；system/tools 变化必报。
- `internal/agent/cache_aware_compaction_test.go`：开关关闭、warm、cold、无 receipt、硬顶和 ablation 分支。
- `internal/agent/compact_*_test.go`、`internal/agent/compact_commit_emit_test.go`：fold 后 projection 形态、summary 数量、canonical 保留和 rewrite reason。
- 若实现 pruning：新增 member-only surface、fold 一次变更、`use_capability` fallback、旧 projection 无字段和关闭开关回归测试。

### 6.2 集成/在线验证

1. 离线先跑固定脚本，确认每个阶梯的 prompt 估算、hard ceiling、fold 次数和 schema 集合。
2. 再跑 provider 在线 cache smoke；只使用费用上限和脱敏 telemetry，不保存原始 prompt、工具参数或秘密。
3. 对失败请求保留 estimated/unknown 标志；没有 provider cache split 的请求不能计算成功命中率。
4. 验证 Team member、leader、普通 CLI 三类 surface 不互相污染。

建议最小命令集（按仓库现有测试入口调整）：

```text
go test ./internal/boot ./internal/agent
go test ./internal/cli ./internal/team
./scripts/check-cache-impact.sh
```

## 7. 回滚、故障处理与数据保护

### 7.1 回滚顺序

1. schema pruning 出现工具不可达、工具成功率下降或 `ToolsHash` 高频变化：立即关闭 `agent.schema_prune`，新请求恢复当前 member surface。
2. warm 延迟导致 hard ceiling、overflow、响应错误或成本上升：关闭 `agent.cache_aware_compaction`，恢复 `compact_ratio` 原路径。
3. 发现 system/tools 非预期变化：停止所有行为灰度，保留诊断，修复产生变化的调用点；不以降低 schema 作为临时掩盖。
4. provider route/TTL 变化：撤销实验 route 或标记样本失效，不修改 transcript 以迎合 provider cache。

### 7.2 回滚不变量

- 不删除或覆盖 canonical transcript；projection sidecar 采用现有兼容读取，必要时重建 derived projection。
- 不回滚 member 工具 registry；只回滚 provider-visible filtering/pruning。
- cache telemetry 失败不阻断成员任务；敏感数据只保留 hash、计数、桶和原因枚举。
- 回滚后至少保留一轮前后配置快照和最后 20 个请求的聚合指标，便于确认命中率是否恢复。

## 8. 1M 验收指标

“1M”指配置的 `context_window=1,000,000`；验收分桶按实际单次 `prompt_tokens`，不按 `context_used`。接近硬顶的请求必须同时满足 provider 请求成功和硬上限约束。

### 8.1 必达指标

在同一 member、同一模型/route、稳定前缀、短间隔 warm 条件下，至少 20 个样本/桶：

1. `128K–256K`、`256K–512K`、`512K–768K`、`768K–999K` 四个 prompt 桶的 token 加权命中率均 **≥90%**；低于样本数要求的桶标记“数据不足”，不能判定通过。
2. `768K–999K` 桶的 warm 单请求命中率 P50 **≥90%**，P10 **≥80%**；冷启动、TTL 冷却、provider 未返回 split 单独列出。
3. 稳定 warm 请求的 `StablePrefixHash` 不变；非 fold 请求 `PrefixChangeReasons` 不得出现 `system`/`tools`；一次 fold 只允许最多一次预期 tools 变化，默认无 pruning 时应为零。
4. `ToolSchemaTokens` 不因回合次数增长；若启用 pruning，fold 后 schema token 足迹相对 control 降低至少 **20%**，且工具调用成功率不低于 control 的 **99%**。
5. 无请求越过 `hardInputCeiling`；无因 warm 延迟导致的 context overflow、无限 fold/retry 或 canonical transcript 丢失。
6. warm-cache 会话的 fold 次数相对 `cache_aware_compaction=false` control 降低至少 **30%**，且 compaction write 成本、总 prompt 成本和延迟必须分别报告，不得只看 hit rate。

### 8.2 通过/不通过规则

- **通过**：必达指标全部满足，且 leader/普通 session 回归通过。
- **部分通过**：命中率满足但 provider scope/TTL 造成的样本被标记 unknown；只能通过“行为优化有效、provider 能力未定”结论，不能宣称全链路 1M 达标。
- **不通过**：任何硬顶违规、工具不可达、非预期 system/tools 变化、canonical 数据损坏，或仅靠减少可见内容获得命中率而破坏功能。

## 9. 交付顺序与责任边界

1. B0 基线/生效面核对：先完成，输出数据集和低命中分类。
2. B1 稳定前缀：根据 B0 证据修复非预期变化，并补 effect tests。
3. B2 schema：只有 schema 尾巴被证明为主因时实现 `schema_prune`；否则保留当前过滤实现。
4. B3 compaction：单独灰度现有 `cache_aware_compaction`，验证 1M 硬顶和 warm fold 频率。
5. B4 provider：在本地原因排除后做 TTL/route 对照，输出 provider-specific 结论。
6. Part A 接入逐请求发布后，再进行最终 1M 验收；没有逐请求 `prompt_tokens` 和 reason，不接受“平均命中率提升”的结论。

最终交付物应包含：实验配置快照、每个 prompt 桶的样本数和 P10/P50/P90、control/variant 的加权命中率、schema token、fold/overflow/工具成功率，以及明确的“已实现、已灰度、未实现、unknown”列表。

## 10. 执行记录（2026-09-24，Part B）

### 10.1 阶段状态

| 阶段 | 状态 | 边界说明 |
|---|---|---|
| B0 冻结基线与生效面核对 | **静态部分完成** | 生效面、schema 足迹、前缀变化源、`context_used` 口径、`use_capability` 可达性（§11.1/§11.2）已核对；§3 要求的"同一 member 连续 20 请求"逐请求样本需 Part A 数据，未完成 |
| B1 稳定前缀 | **完成（静态部分）** | 无回合内 schema 变更；fold 前缀不变性已补 effect test；发现并修复 2 处前缀风险 |
| B2 验证现有 schema 收缩 | **完成，结论：不实施 pruning** | 实测 member 尾巴 1567 tokens（§10.2），按 §3 B2 第 2 条不新增 `schema_prune` |
| B3 compaction cache alignment | **完成（测试补齐 + 开关修复）** | 4 个未覆盖分支已补；发现开关不可达并修复 |
| B4 provider TTL/route | **未开始** | 依赖 B1–B3 在真实 route 上的基线，且需 Part A 报表先确认本地原因已排除 |

### 10.2 生效面与 schema 足迹（B0/B2 证据）

新增 `internal/boot/member_surface_tokens_test.go`，走真实 `boot.Build` 到 provider 边界（不是 allowlist 推断）：

- `TestMemberSurfaceTokenFootprint`：同一 config、同一 host 工具集，仅 `TeamRole` 不同。
- `TestMemberSurfaceIsStableAcrossBoots`：两次独立 boot 的 `ToolsHash`/`PrefixHash` 必须一致。

实测：

| 角色 | provider-visible 工具数 | schema tokens |
|---|---:|---:|
| leader | 17 | 3,397 |
| member | 8 | 1,567 |
| 差值 | −9 | −1,830（−53.9%） |

结论：

1. `memberDeferredTools`（13 个名字）在真实构建生效；member surface 逐项等于 leader surface 减去这些名字，顺序与内容一致——是减法，不是静默增删或重排。
2. 延期工具仍在 registry 中，`use_capability` 保留在两个 surface 上。**可达性的完整核对见 §11.2**——既有 `internal/boot/member_visible_tools_test.go` 的 `TestMemberVisibleToolsStayExecutable` 只是"名字仍注册"的代理，其蕴含的结论（按名派发可行）本轮已对着真实派发路径验证成立，但它对"模型能否发现这些工具"不作任何声明。
3. **1,567 tokens 占 128K prompt 约 1.2%，占 1M 约 0.16%**。schema 不是剩余低命中的合理主因，因此**不实现 `agent.schema_prune`**：新增开关会带来工具可达性与 `ToolsHash` 翻转风险，换不到可测量的收益。

### 10.3 前缀变化源清单（B1）

`PrefixChangeReasons` 的全部生产点（已核对，无遗漏调用点）：

| reason | 归类 | 生产点 | 触发时机 |
|---|---|---|---|
| `system` | **预期外（应阻断灰度）** | `CompareShape`（`SystemHash` 变化） | system 消息变化 |
| `tools` | **预期外（应阻断灰度）** | `CompareShape`（`ToolsHash` 变化） | provider-visible 工具面变化 |
| `session_context` | 需按 section 判断 | `CompareShape`（tail digest 变化） | 仅当 environment/workspace/memory/skills 之一真的变化；digest 是 sections 的纯函数，不含时间戳，不会每回合漂移 |
| `system_prompt_refresh` | 预期外 | `internal/agent/session.go:133` | 显式 `SetLeadingSystemPrompt` |
| `legacy_pinned_system_migration` | 兼容边界 | `internal/control/pinned_context.go:33`、`desktop/session_prompt.go:65` | 会话绑定/恢复前 |
| `team_role_prompt_refresh` | **member 相关边界，一次性** | `internal/control/pinned_context.go:67`，调用点 `internal/cli/team_backend_build.go:491` | 成员 bind 时，在首轮之前；只在恢复到的 transcript 首条 system 属于旧角色时触发 |
| `managed-runtime-activation` | 兼容边界 | `internal/control/session_write_authority.go:59` | 写权/运行时重绑 |
| `rewind_truncate` / `rewind_restore` | 预期 | `internal/control/rewind.go:58,78` | 用户回退 |
| `guardian_merge` | 预期 | `internal/guardian/guardian.go:357` | guardian 合并 |

关键结论：

1. `SetProviderVisibleTools` 全仓库只有 `internal/boot/boot.go:2020`（经 `applyUnifiedProviderToolSurface`）一处调用，**没有任何回合内变更路径**。§3 阶段 B1 第 2 项怀疑的"按思考轮次动态增删 schema"不成立：`providerToolSchemas` 只读取 `Registry.Schemas()`（`internal/agent/finalization.go:29`）。
2. `Registry.Schemas()` 按 name 排序（`internal/tool/tool.go:718`），与 `normalizeToolSchemas` 的排序一致，因此**可见块的 wire 顺序与 hash 顺序都是确定的**。
3. **缺口：fold 不产生任何 reason。** compaction 安装 projection 只改消息，不经过 `Rewrite`/`NoteContentRewrite`，所以 fold 后的冷请求在 `PrefixChangeReasons` 里没有任何标记（`PrefixChanged` 仍为 `false`）。而 `internal/agent/cache_shape.go:69` 的注释已用 `"compact_auto"` 举例——该 reason 目前无人发出。这落在 Part A 的 reason 词表冻结范围内，见 §10.6。

### 10.4 本轮修复

**修复 1：`Options` 两个字段从未生效（`internal/agent/agent.go`）**

`New()` 组装 `agentConfig` 时漏了 `visibleWindowTokens` 与 `cacheAwareCompaction`，导致 `agent.visible_window_tokens` 和 `agent.cache_aware_compaction` 两个配置键在任何构建里都是死键——`internal/boot/boot.go` 的三处传参（`:1167`、`:1750`、`:1837`）全部被丢弃。既有单测直接构造 `&Agent{agentConfig: agentConfig{...}}`，绕过了 `New()`，所以缺陷一直不可见。

修复：补两行赋值。回归测试放在**消费边界**而非 helper 上：

- `TestVisibleWindowTokensReachesTheAgentFromOptions`（`internal/agent/visible_window_tokens_test.go`）
- `TestCacheAwareCompactionDefersToTheCeilingButStillMaintains`（`internal/agent/cache_aware_compaction_test.go`）

两者在回退修复后均失败（实测量化：`warm trigger = 80000, want the hard ceiling 99744`；`recentTailBudget = 160000, want 80000`），修复后通过。

影响：修复前 B3 的"灰度开关"无法打开，§8 的 1M 验收（fold 次数对比、warm 命中率）也就无从测量。

**修复的爆炸半径与回滚**：`cache_aware_compaction` 默认 `false`，零值语义不变，默认构建行为完全不变。`visible_window_tokens` 的零值同样保持默认（16% 的 recent tail）；但**任何显式设置了该键的 config 从此会真正生效**——它会把 provider-visible 的逐字 tail 压到配置值以下（`internal/agent/compact.go:161`），从而改变 fold 边界。该键在 `internal/config/config_harness.go:299-301` 有注明语义，属于"本来就该生效"的行为，但回滚时要知道它现在真的有作用：置零即恢复 16% 默认。由于 `New()` 是唯一入口，这两项无法按角色分别开关——如需按角色区分，应在上层 `Options` 组装处决定。

**修复 2：延期 MCP 尾巴的顺序依赖注册顺序（`internal/agent/finalization.go`）**

`deferredMCPSchemas` 原按 `Registry.AllNames()`（插入序）输出，`ApplyNativeToolSearch` 按该顺序把尾巴追加到 wire 工具数组；而 `CaptureShape`/`normalizeToolSchemas` 先排序再 hash。`Registry.RemovePrefix` + `Add` 会把一个 server 的整块移到尾部，而 MCP `tools/list_changed`、按需注册、lazy cache-miss spawn 三条真实路径都会这么做——**wire 字节变了，`ToolsHash` 不动**，缓存失效但本地零诊断（`StablePrefixChanged` 仍为 `false`）。

修复：按 name 排序输出。回归测试 `TestDeferredMCPTailOrderIsCanonical`（回退后失败，已实测）。

现状定级：**潜在缺陷，非当前线上问题**。该尾巴只在 `NativeToolSearchEnabled` 下非空，而 `nativeToolSearchPreview` 默认关闭且无生产调用点（仅 first-party OpenAI Responses + `gpt-5.4/5.5/5.6` 前缀才会实现 `NativeToolSearchAvailable`）。定序修复不改变任何现有 wire 输出（尾巴当前恒为空），只是消除陷阱。

**修复 3：补齐 `cache_aware_compaction` 分支测试（`internal/agent/cache_aware_compaction_test.go`）**

原有覆盖只在 `compactTrigger`/`warmCache` 的算式层。`TestCompactionAblationCollapsesTheCachePreservingDeferral`（`internal/agent/ablation_test.go:30`）虽然名为"collapses the cache-preserving deferral"，但既不设开关也不注入 warm 回执，`internal/agent/compact.go:115` 的 `!a.ablation.Off(ablation.Compaction)` 守卫从未被执行——而该守卫是有承载的：ablation 把 ratio 降到 0.5，没有它，warm 的 ablated 会话会从 50% 直接跳到硬顶。

新增 4 个 effect 级测试（真实 `ContextManager.Prepare`，断言"该请求是否安装了 projection"）：

| 测试 | 覆盖 §3 分支 | 断言要点 |
|---|---|---|
| `...DefersToTheCeilingButStillMaintains` | 分支 2 + 5 | warm 时 85,000 不 fold；到 ceiling（99,744）必须 fold；安装后的 projection 低于 ceiling |
| `...ReturnsToTheRatioAfterTTLCooldown` | 分支 3（TTL 半边） | warm → 不 fold；miss-heavy 回执 → 触发点回到 `compact_ratio` → 再次 fold |
| `...CannotBypassCompactionAblation` | 分支 4 | ablation + warm 时触发点是 50%，不得跳到 ceiling，且仍能 fold |
| `...LeavesManualCompactAlone` | 分支 1 的另一侧 | 延迟只管辖自动维护，显式 `/compact` 不受影响 |

**语义澄清（写测试时确认，与 §3 B3 第 3 项的措辞不同）**：warm 时 `compactTrigger == hardInputCeiling`，而 `prepareOnce` 只在 `est >= hard` 时才进入维护（`internal/agent/context_manager.go:152` 的 `forceFold`）。所以"延迟到硬顶"的实际含义是**在硬顶上 fold**，而不是"在硬顶之下某个更早的点 fold"。测试按这个真实语义断言；§8.1 第 6 项描述 fold 次数下降时，应理解成"避免 ratio→ceiling 之间的所有中间 fold"，不是"在 ceiling 之前 fold"。

**新增 4：fold 前缀不变性 effect test（`internal/agent/fold_cache_prefix_test.go`，新文件）**

| 测试 | 断言 |
|---|---|
| `TestFoldPreservesTheStableCachePrefix` | fold 前后 `SystemHash`/`ToolsHash`/`ToolSchemaTokens` 相同，`StablePrefixChanged=false`，reasons 不含 `system`/`tools` |
| `TestFoldRequestReusesTheLiveToolSurface` | fold 请求（`summaryRequest`）的工具表与实时请求逐字节相同，消息是"实时前缀 + 一条追加指令"（append-only） |
| `TestFoldKeepsTheCanonicalTranscriptAndOneSummary` | canonical transcript 逐字节不变；provider-visible 至多一条 summary |
| `TestFoldInstallsAtMostOneToolSchemaChange` | pruning 关闭时 fold 对工具面的改动为 0（§8.1 第 3 项"最多一次"的上界） |

### 10.5 验证

| 检查 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `go test ./internal/agent/ ./internal/boot/` | 通过 |
| `go test ./internal/cli/ ./internal/team/... ./internal/config/ ./internal/control/` | 通过 |
| `golangci-lint run ./internal/agent/... ./internal/boot/...` | `0 issues` |
| `gofmt -l internal/agent internal/boot` | 干净 |
| `go run ./tools/repolint` | 本 Part 新增/修改的文件**无新增违规** |
| `scripts/cache-guard.sh` | 通过，见下 |

`scripts/cache-guard.sh`（仓库既有的缓存守卫）通过，本轮实测基线：

| case | tail 平均命中率 | 阈值 |
|---|---:|---:|
| `plain-dialogue` | 92% | 90 |
| `long-dialogue` | 93% | 90 |
| `tool-loop` | 92% | 90 |
| `long-tool-loop` | 95% | 90 |
| `mixed-message-sizes` | 94% | 90 |
| `large-tool-64k` / `large-tool-256k` | — | `provider_bytes_max=32768` |

注意口径：这是**本地模拟 provider**（`httptest` + 真实 `openai` adapter 走真实 wire 字节，`internal/agent/cachehit_e2e_test.go`），不是真实 provider。它是本仓库指定的缓存回归守卫，可以作为 §8"warm 命中率 ≥90%"的本地代理门槛，**不能**替代 §8.2 要求的真实 provider 结论。

`go run ./tools/repolint` 剩余的 essay / file-size / function-size 违规均为本分支既有（`internal/cli/*`、`internal/provider/anthropic/messages_usage.go`），不在本 Part 范围内。

### 10.6 依赖 Part A、本 Part 未完成的项

**不得先验归因**——以下都需要 Part A 的逐请求数据或字段契约冻结后才能推进：

1. **§8 的 1M 验收**：需要逐请求 `prompt_tokens` + `CacheHitTokens`/`CacheMissTokens` + `PrefixChangeReasons` 按 prompt 桶、`StablePrefixHash`、冷/warm/fold 后分桶。字段发布由 Part A 落地，本 Part 不重复实现。
2. ~~**fold 请求自身的 cache 诊断缺失**~~：**Part A 闭环后已核对为"按设计不需要"，见 §12.3 第 1 项**——该行使 Part A 以 `usage_source=compaction` 记录并落入 `undiagnosed_samples`，按规则不并入固定前缀集合，故不会被误归因；给它挂诊断反而更糟。
3. ~~**`compact_auto` reason 无人发出**（§10.3 第 3 条）：需要 Part A 先冻结 reason 词表。~~ **Part A 闭环后已实现，见 §12.1。**
4. **B0 的"同一 member 连续 20 请求"样本**与 §4 实验矩阵：需要真实 route 与 Part A 的分桶报表。
5. **B4（provider cache scope / TTL / route 对照）**：需先由 Part A 报表确认"本地 prefix stable、schema 尾巴可控、fold 原因可解释"三个前提，再开始。

### 10.7 交付物状态：已实现 / 已修复 / 未实现 / unknown

- **已实现（代码 + 测试）**：fold 前缀不变性 effect test（4 项）；`cache_aware_compaction` 四分支 effect test + control 臂；member schema 足迹实测；`Options` 两字段生效；延期 MCP 尾巴定序；`PrefixChangeReasons` 全量生产点清单；`context_used` 口径澄清；延期工具可达性三层核对；**fold/prune/truncate 的归因 reason 接通（§12.1）**。
- **已修复缺陷**：2 个——`Options` 两个配置键失效（`agent.go`）；延期 MCP 尾巴顺序依赖注册顺序（`finalization.go`）。
- **未实现（且经证据判定不应实现）**：`agent.schema_prune`。理由见 §10.2 第 3 条。
- **未实施**：任何 prompt / 工具面 / compaction 的行为改动。B0/B1 的结论是"当前无需行为改动"——没有证据支持为了命中率去改这三者。
- **unknown（需真实数据）**：provider 的 TTL 与 route 亲和性行为；`>512K` 真实 prompt 桶的命中率分布；`context_used` 与最近请求 `prompt_tokens` 的**差量分布**（口径本身已在 §11.1 澄清，不再是未知）。这三项在拿到 Part A 报表前一律标 unknown，不用估算值验收。

### 10.8 PR 元数据（缓存守卫门槛）

`scripts/check-cache-impact.sh` 会把本分支的改动判为 cache-sensitive（路径规则命中 `internal/agent/agent.go`、`internal/agent/cache*`、`internal/boot/*`），并要求 PR body 含三行；`scripts/check-docs-impact.sh` 另要求一行 `Documentation-impact:`。本 Part 的建议值：

```text
Cache-impact: low - agent.go 让两个既有配置键(visible_window_tokens / cache_aware_compaction)真正生效(此前被静默丢弃); finalization.go 把延期 MCP 尾巴按名字定序(尾巴当前恒为空,输出不变); compact_commit.go/maintenance_commit.go 在投影安装点上报 compact_auto/prune/truncate 归因原因(仅诊断字段,不改 provider-visible 字节); 其余为测试与文档
Cache-guard: bash scripts/cache-guard.sh (通过 10/10 case, 阈值 90); 另有 go test ./internal/agent -run 'TestFold|TestProjectionRewriteReasons|TestCacheAwareCompaction|TestVisibleWindowTokens|TestDeferredMCPTailOrderIsCanonical'
System-prompt-review: 未改动 provider-visible system prefix、工具面字节或 system 消息; internal/boot/* 命中路径规则的是新增只读测试 member_surface_tokens_test.go; §12.1 只新增诊断原因字段
Documentation-impact: updated - docs/team-mcp-port/TEAM_MEMBER_CACHE_BEHAVIOR_PLAN.md 增加 §10 执行记录、§11 补充核对与 §12 Part A 闭环后的接续工作(实测 schema 足迹、PrefixChangeReasons 全量生产点、context_used 口径、延期工具可达性、两处修复、缓存守卫基线、Part A 交接项), 并修正 §5 中 cache_aware_compaction "已实现可灰度"的表述
```

注意：`check-cache-impact.sh` 的判定是**路径规则**而非语义判定——`internal/boot/member_surface_tokens_test.go` 因为落在 `internal/boot/*` 而被判 system-prompt-sensitive，但它只读取 schema，不改变任何 prompt 字节。上面第三行按事实声明。


## 11. 补充核对（2026-09-24，Part B 自查）

以下三项是首轮执行时被"推给 Part A"或"依赖代理测试"而实际可以静态回答的问题。补做后结论如下。

### 11.1 `context_used` 与最近请求 `prompt_tokens` 的口径（Root plan P0）

Root plan §1 的观察——`ipc-protocol` 的 gauge 1,264,632 而最后一次 provider prompt 539,611——**不需要任何"大 prompt 命中率下降"的假设就能解释**，因为两者是不同的量：

| 指标 | 定义 | 锚点 |
|---|---|---|
| `context_used` | **下一次**请求视图的**估算**输入 token 数 | `internal/agent/context_usage.go:24` `ContextUsedTokens()`；估算入口 `internal/agent/context_manager.go:363` `estimatedVisibleRequestTokens` |
| 最近请求 `prompt_tokens` | provider 报告的**上一次已完成**请求的输入 | `internal/cli/chat_tui_status.go:92` `LastUsage()` |

`ContextUsedTokens()` 的组成：`normalizeModelRequestMessages(可见消息)` + `providerToolSchemas()`（**含工具 schema**）+ `MaxTokens`/`Temperature`，经 `estimatedRequestTokens` → `estimatedShapeTokens` → `calibratedPromptTokens`（`internal/agent/output_budget.go:347`、`:310`）计算；即用最近一次真实 usage 得到的 **chars→tokens 比率**外推到当前视图的字符数，并对超出校准样本的 CJK 字节按冷速率补价（`output_budget.go:322-328`），无校准时退化为 `chars × fallbackTokPerChar`。结果按 `(session, transcriptVersion, projectionVersion, calibration, toolSchemaRevision)` 记忆化（`context_usage.go:37-45`）。

因此 gauge 结构性**领先**于最近一次 `prompt_tokens`，原因有三，且都与命中率无关：

1. **时点不同**：gauge 量的是"下一次要发的东西"，此时上一轮已经追加了 assistant 输出与工具结果；`prompt_tokens` 量的是"上一次已发的东西"。两者相差恰好一轮的增长。
2. **估算 vs 实测**：gauge 是外推估算（比率来自上一次请求），`prompt_tokens` 是 provider 实测。
3. **压缩边界**：gauge 以 `projectionVersion` 为记忆化键，fold commit 后立刻重算为折叠后的视图；fold **之前**它合法地停在 `compact_ratio` 之上，这不是缺陷。

`ContextUsedTokens` 自身注释已说明为何不能用上一轮 usage 喂 gauge（滞后一轮、含 completion token、rebound 会话读 0，会出现"8% 却在压缩"）。

**UI 口径现状**：CLI 状态行已经分开显示两个率——`turn hit`（最近请求 `hit/(hit+miss)`）与 `avg`（会话累计 `Σhit/Σ(hit+miss)`），见 `internal/cli/chat_tui_status.go:176-205` 及其注释。所以 Root plan 的 P0"口径混用"在 **CLI 展示面已经解决**；剩余风险只在 Part A 负责的**已发布 member usage 文档**里：`context_used` / `context_window` 与最近请求的 `prompt_tokens` / `hit` / `miss` 同处一份记录（`internal/team/ownerusage.go:92-94`），读者容易把 gauge 与请求长度读成同一个指标。建议 Part A 在字段旁保留单位/时点标注，而不是在报表里把两者并列成"上下文大小 vs 命中率"。

**对本 Part 的影响**：§8 的 1M 分桶只用 `prompt_tokens`；`context_used` 不参与任何命中率分桶。

### 11.2 延期工具的 `use_capability` 可达性（§3 B0 第 2 项）

§3 B0 要求"检查 `use_capability` 能否调用被延期工具"。既有测试只是注册名代理，本轮对着真实路径核对，结论分两层：

- **按名派发：13/13 可行。** `internal/agent/usecapability_registry.go:26` 是 `t.registry.Get(name)`，**没有任何 provider-visibility 过滤**；全仓库读 `ProviderVisible` 的位置（`tool.go:714`/`:774`、`tool_surface.go:108`、`usecapability_list.go:55`、`finalization.go:68`）都不在派发路径上。所以 `use_capability("tool:<name>")` 对延期名字照常解析并执行，§8.2 的"工具不可达"不成立。
- **目录可枚举：13/13 可行。** `use_capability` 的 catalog 用 `reg.CapabilityContractEntries()`（`internal/boot/boot.go:1667`），即 `contractEntries(false, true)`（`internal/tool/contract.go:86`）——第一个参数 `providerVisibleOnly=false`，**不读 provider-visible allowlist**，只丢 `CapabilityCatalogHidden`。所以 member 的延期工具在 `list`/`search`/`inspect` 里仍然出现并可给出 `input_schema`。已有测试独立证明了这一点：`internal/agent/usecapability_context_test.go:25-60` 用 `SetProviderVisibleTools([]string{"use_capability"})` 隐藏一个工具，断言 `search`/`list`/`inspect` 三者都仍能返回它。
- **主动路由：0/13。** 这是本轮新发现的不对称，且**不是 `dropMemberDeferredTools` 引入的**：capability router 的两个输入分别是 `c.ToolContractEntries()`（provider-visible，`internal/control/capability.go:93-100`）和 `RoutableTools = c.routableToolEntries()`（`AllContractEntries()` 但要求 `len(e.Triggers) > 0`，`capability.go:201-202`）。全仓库只有 4 个类型实现 `CapabilityTriggers()`（`fleet`/`task`/`parallel_tasks`/`orchestrate`），**13 个延期名无一声明 triggers**，因此 router 对它们零条目——这在 leader 上同样成立，只是 leader 的模型能直接看到 schema，不需要 router 提示。

**净效果**：`dropMemberDeferredTools` 不破坏可达性，但把这 13 个工具从"模型直接可见"移到"模型必须主动按名询问"；member playbook 从不提及这些名字，`use_capability` 的 `Description()` 也是静态串（`usecapability.go:601`）。这与 Root plan 的既有契约（"仍注册、仍可经 `use_capability` 到达"）不冲突，故**不作为缺陷，也不在本轮修**——补 triggers 需要产品级的短语判断，且会改变所有角色的路由建议面，属于 §7.1 第 3 条之外的独立行为变更。留作决策项：

- 若要恢复成员对这些工具的**主动**可发现性，最小改动是对确有价值的少数名字（如 `web_search`、`compress`、`todo_write`）补 `CapabilityTriggers()`；triggers 是 `json:"-"`，不会移动 provider-visible 前缀（与 `docs/...`/记忆中的既有结论一致）。
- 若判定"按名可达即可"，则应在 `memberDeferredTools` 的注释里写明这是**有意**的降级，避免后来者把它当作缺陷反复"修复"。

### 11.3 自查中修正的两个先前表述

1. §10.7 原把 `context_used` 与 `prompt_tokens` 的**差量口径**列为 unknown。差量的**定义**部分静态可答（§11.1），只有**分布**需要真实数据。已更正。
2. §10.2 原写既有测试"覆盖'仍可执行'"。该测试是注册名代理，不含派发或发现语义；其蕴含的派发结论本轮已独立验证（§11.2），但发现语义从未被它覆盖。已更正。

## 12. Part A 闭环后的接续工作（2026-09-24）

### 12.1 接通 fold 与 prune 的归因 reason（实现 Part A 的冻结契约）

Part A 把归因标签与原因枚举冻结在 `internal/team/cachediagnosis.go:91`：

```go
var rewriteReasons = []string{"compact_auto", "snip", "rewind_truncate", "guardian_merge", "prune"}
```

**缺口**：`compact_auto` 与 `prune` 在生产代码里**无人发出**（§10.3 第 3 条），因此 `rewrite_or_compaction_correlated` 结构上不可能触发。后果不是"少一个标签"，而是**误归因**：fold 不移动 system/tools（`StablePrefixChanged=false`），所以 fold 之后那次冷请求会落进"固定前缀"集合，被标成 `stable_prefix_high_miss_unattributed`——把一个必然的投影重写报成"不可归因的尾部问题"。这正是 §11.10 里 Part A 已经因为冷启动踩过一次的同一类错误。

**实现**：

| 改动 | 位置 | 说明 |
|---|---|---|
| `projectionRewriteReason(action)` | `internal/agent/cache_shape.go` | 纯函数：`summary`→`compact_auto`、`prune`→`prune`、`truncate`→`truncate`；`noop`/空/未知 → `""`（不入队） |
| `noteProjectionRewrite(receipt)` | `internal/agent/cache_shape.go` | 按 `receipt.Action` 入队，nil 安全 |
| 安装点调用 | `internal/agent/compact_commit.go:77`、`internal/agent/maintenance_commit.go:103` | 两个**创建** receipt 的地方各一次 |
| `NoteContentRewrite` 去重 | `internal/agent/session.go` | 同一 drain 窗口内相同原因只保留一条 |
| action 常量 | `internal/agent/maintenance_commit.go` | `maintenanceActionSummary/Prune/Truncate` 归一，替换 `prune.go`/`compact_commit.go` 的魔法串 |

**为什么只在安装点入队**：`emitContextMaintenance` 还有两个调用方——`internal/agent/context_receipt.go:152` 与 `internal/agent/session_checkpoint.go:129`——它们是**重发已存的 `LastReceipt`**，不是新的安装。把入队放进 `emitContextMaintenance` 会让一个"什么都没重写"的请求背上 rewrite 原因，正是本轮要消除的那类误归因。

**为什么要去重**：一次救援 fold 可在同一 drain 窗口内安装多个投影（`maxSummaries` 最多 4，pressure 路径 2），`NoteContentRewrite` 原样 append 会发布 `["compact_auto","compact_auto"]`。reason 列表描述的是**变了什么**，不是**变了几次**；`CompareShape` 把它并入 `PrefixChangeReasons`，Part A 用枚举匹配，重复只是噪声。

**契约变更（请 review 时重点看这一条）**：`internal/agent/projection_test.go` 的 `TestCompactRewriteVersionFeedsCacheDiagnostics` 原本断言"projection 不得排队任何 reason"。该断言与 `NoteContentRewrite` 自身的文档矛盾——后者的注释明确写：

> NoteContentRewrite queues a provider-visible prefix-change reason without mutating Messages. **Projection installs** and resume-time system migrations use this so cache diagnostics attribute the next request's miss while the canonical transcript and its persistence baseline stay intact.

即这个机制**就是为投影安装建的**（它不动 `Messages`、不动持久化基线，正是为此而存在），只是投影安装侧的调用点从未接上——目前全仓库只有 `desktop/session_prompt.go:65` 与 `control/pinned_context.go` 的恢复路径在用。另外 `CompareShape` 的注释以 `"compact_auto"` 举例，Part A 的枚举也把它列为已知原因，三处独立证据都指向"投影安装应当上报"。故本轮把该断言改为：`RewriteVersion` 不变（canonical 不动）**且**恰好入队 `[compact_auto]`。这是本轮唯一一处推翻既有断言的行为改动。

**新增测试（均在消费边界，逐条做过红证据验证）**：

| 测试 | 断言 | 移除调用后的表现 |
|---|---|---|
| `TestFoldAnnouncesItselfInTheNextRequestsDiagnostics`（`fold_prefix_boundary_test.go`） | 真实 HTTP 边界跑 8 轮：fold 必须上报 `compact_auto`、prune 必须上报 `prune`，且全程不得出现 `system`/`tools` | 移除 `compact_commit.go` 的调用 → "never reported compact_auto"；移除 `maintenance_commit.go` 的调用 → "pruned 6 times but never reported the prune reason" |
| `TestProjectionRewriteReasonsOnlyComeFromInstalls`（`fold_cache_prefix_test.go`） | reason 映射表（含 `noop`/空/未知→不排队）；重发已存 receipt 必须静默；重复原因去重 | — |
| `TestCompactRewriteVersionFeedsCacheDiagnostics`（`projection_test.go`） | 安装点端到端 | 移除调用 → 入队为空 |

### 12.2 交回 Part A 的一项枚举补齐

`truncate`（`internal/agent/truncate.go` 的溢出救援投影）是一次真实的 provider-visible 内容重写，但**不在** Part A 的 `rewriteReasons` 枚举里。我按 §3.1"未知原因保留原值并归入 unknown 统计"如实发出 `truncate`，因此它会在报表里被计为未识别原因（这是诚实的默认，也符合 Root plan"未知原因必须保留为未知"）。

若希望它也参与 `rewrite_or_compaction_correlated`，需要在 `internal/team/cachediagnosis.go:91` 的枚举里补上 `truncate`。**未擅自修改 Part A 的文件。**

### 12.3 §10.6 两项依赖项的更新结论

1. **第 2 项（fold 请求自身缺 cache 诊断）——结论改为"按设计不需要"，本轮不改。**
   核对 Part A 的 `internal/cli/team_usage_publish.go:202` `observe`：它记录**每一个**带 payload 的 `event.Usage`，包括 `runSummaryRequest` 发出的 `usage_source=compaction` 那条。该行没有 `CacheDiagnostics` → Part A 记为 `diagnostics_available=false` → 落入其 `undiagnosed_samples`，而按 §11.6 的规则**未诊断样本绝不并入"固定前缀"集合**。
   所以 fold 请求既被正确计数（§3.1 的统计单位是 provider request），又不会被误当作"前缀未变"的样本。**反过来给 fold 事件挂诊断会更糟**：那会把它从 `undiagnosed_samples` 推进"固定前缀"集合，而它是一次大范围重发，miss 一旦偏高就会强化一个错误的 `stable_prefix_high_miss_unattributed`。fold 自身的成本可按 `usage_source` 分层读取，无需改代码。

2. **第 3 项（`compact_auto` 无人发出）——已实现（§12.1）。**

### 12.4 本轮验证

- `go build ./...` 通过。
- `go test ./internal/agent/ ./internal/boot/ ./internal/cli/ ./internal/team/... ./internal/control/` 全绿（含 Part A 已落地的 `internal/team` 与 `internal/cli` 套件，说明新增的 reason 值没有破坏其解析）。
- `golangci-lint run ./internal/agent/...`：`0 issues`（首轮曾报 1 处 `slicescontains`，已改用 `slices.Contains`）。
- `gofmt` 干净。
- `go run ./tools/repolint`：本 Part 文件无新增违规。过程中一次自查发现 `internal/agent/projection_test.go` 因新增测试越过 800 行 test-file-size 上限并产生一处超长行内注释，已把新测试移到 `fold_cache_prefix_test.go`（787 行）并压缩注释，未使用 `-update`。
- `scripts/cache-guard.sh` 通过。
- 仓库中 `internal/cli/chat_tui_team_session.go` 仍有 7 处 essay 超额，属 Part A 改动的文件，本 Part 未触碰。
