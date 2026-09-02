# 1M 上下文路线 P2/P3 设计边界（只读交付，不实现）

> 状态：**设计稿（2026-09-02，architect-claude）**。对齐结论：P0=coder 生产改动（
> ProviderEntry.AnthropicBeta + boot Extra 注入 + effect_test）；P1=tester 回归与全量验证
> （compact 800K 触发、hardInputCeiling 边界、384K 输出预算、E2E wire header）；本稿为 P2/P3
> 的设计边界，**只出设计，不实现**，定稿以 P1 的 1M 负载实测数据为输入。
> 红线：不改写现有 compact 契约（context_manager/compact_* 被 effect_test 钉死）；不动
> cache-stable prefix 的字节稳定性；不违反 layer 规则（agent 是 provider-visible 上下文维护的
> 唯一所有者，control 只设开关，前端零逻辑）；不扩宽 repolint baseline。

## 0. 现状锚点（设计输入，均已核对代码）

| 机制 | 现状 | 位置 |
|---|---|---|
| 规范会话 | `Session.Messages` 只追加、从不被投影替换 | agent/projection.go |
| 可见投影 | `ContextProjection{ Messages, CoveredCount, CoveredPrefixHash, ... }`；可见 = projection.Messages + canonical[CoveredCount:] | agent/projection.go:61 |
| 压缩触发 | `compactTrigger = 0.80 × window`；`hardInputCeiling = window − 256` | agent/compact.go:21,75 |
| 压缩产物 | ≤2 次 cache-aligned summary checkpoint：`msgs[:head] + summary + 保留尾(16%)` | agent/compact_projection.go:593 |
| 单次摘要 | `compactionInstruction` 单层、合并既有 `<compaction-summary>` | agent/compact.go |
| 缓存对齐 | fold 后只追加 summary，rest of prefix 保持字节稳定；`currentPromptCacheKey = promptCacheKey(workspaceID, sessionLineage, modelRef)` | agent/preflight.go:306, compact.go:44 |
| 缓存诊断 | `CacheDiagnostics{PrefixChanged, Reasons[]}`，内容重写原因经 `DrainContentRewriteReasons` 消费 | agent/cache_shape.go |
| 工具 schema | `providerToolSchemas()` 全量入请求；可选工具经 `use_capability` 封口；`NativeToolSearch` 已接线（provider 支持时） | agent/finalization.go:28, agent/capability_gate.go |
| 工具结果 | head/tail 保留 + mid 剪裁（`snipStrategy`/`pruneToolResultContent`） | agent/agent.go:2776, agent/prune.go |
| 检索 | 仅 `code_index`（文件/符号轮廓）+ grep/web_search；**无对话级检索** | tool/builtin/codeindex.go |
| 侧车 | `CompactionState` schema V1–V3，`ContextStatePath` 原子发布 | agent/projection.go:119 |

五个机制（T1–T5）全部可独立成阶段、默认关闭、零值=现有行为，逐阶段可合。

---

## T1 滑动窗口（provider-visible 窗口封顶）

**问题**：1M 窗口下即使 fold 后，保留尾 16% = 160K tokens 逐字 + 摘要，每次请求全量发送，
延迟与成本随窗口线性涨。

**设计**：
- 新增 `boot.Options` / `agent.Options` 开关 `VisibleWindowTokens int`（`0`=沿用现有
  `recentTailBudget=16%`，字节不变；`>0` 时把保留尾预算压在 `min(16% window, VisibleWindowTokens)`）。
  窗口 = 摘要 + 逐字尾（≤预算），窗口外一律只经摘要表达。
- 复用现有机制实现：不新增存储结构——`recentTailBudget()` 在开关>0 时返回
  `VisibleWindowTokens`；fold 的计划（`planFoldRegion`/`checkpointProjectionMessages`）不变，
  只是 `kept` 的并入预算被压缩。
- **边界**：改「预算来源」，不改「折叠算法」。所有变更落在 `agent` 包
  （`compact.go`/`compact_projection.go`）；`control.Controller` 暴露
  `SetVisibleWindowTokens(int)` 透传，前端（cli flag `--visible-window-tokens`、serve/ACP）仅映射。
- **迁移**：零值=现行为，现有 effect_test 不动；`CompactionState` 不新增字段（预算来自 config，
  进程重启由同一 config 重建，无需持久化）。
- **性能指标**：单请求 provider-visible tokens ≤ max(硬顶, VisibleWindowTokens)；窗口封顶生效时 P95
  首 token 延迟与窗口占比近似线性下降。
- **验收（effect test）**：开启时投影尾 token ≤ VisibleWindowTokens；关闭时与基线字节一致；
  默认关闭时 effect_test 零改动。

---

## T2 分层摘要（Digest Tree）

**问题**：单层摘要 + 「合并既有 summary」的指令在 1M 级历史下信息坍缩，且受
`maximumSafeSummaryPrefixEnd` 限制摘要输入长度。

**设计**：
- **`DigestNode`**：`type DigestNode struct { Level int; SegmentStart, SegmentEnd int; Text string; Tokens int; Children []int }`
  ——每个 fold 在侧车提交一个节点；父节点是子节点 Text 的廉价拼接（fold 时 context 已在进程内，
  O(1) 提交）。存于新增 `ContextProjection.Digest *DigestNode`（`omitempty`）→ **侧车升 V4**。
- **可见视图不变**：T2 严格不动 provider-visible 内容（顶层摘要仍按现有
  `compactionInstruction` 单次构建、字节稳定、cache-aligned）。Digest Tree 只服务 T3 检索。
- **折叠约束**：`maximumSafeSummaryPrefixEnd`、`acceptCheckpointCandidate`、≤2 次摘要的既有契约全部不动。
- **接口**：`agent` 内 `DigestStore`（对 `CompactionState` 的读写封装）+ `DigestCommit(foldHead, foldEnd, text)`。
- **迁移**：V4 reader 接受 V1–V4（additive）；V3 文件加载后 `Digest=nil` 首次 fold 开始建树；
  老 reader 不读新字段（不兼容性=无，V3 读 V4 文件时忽略未知字段——Go json 天然容忍，写端仍要
  `SaveCompactionState` 时显式标 V4）。
- **性能指标**：每次 fold 的 digest 提交开销 < 1ms（无新网络调用）；摘要输出 tokens 不超
  `summaryOutputMaxTokens` 的既有预算。
- **验收（effect test）**：两次 fold 的会话 Digest 树 ≥2 节点；resume 后 Digest 恢复完整；
  折叠前后 `CaptureShape` 的 PrefixHash 不变（visible 层零扰动）。

---

## T3 检索（Digest 命中 + 可选 restore-at-commit）

**问题**：现有 fold 要么全保留（占窗口）、要么摘要化后事实不可达，无「按需取回」。

**设计**：
- **`context_retrieve` 工具**（agent 包内新工具，仿 `compress` 装配进 agent——不是 MCP/前端工具）：
  `context_retrieve(query, max_results)` → 在 Digest Tree 节点 Text 上做词法+锚点匹配（复用
  `CompressContext` 的 anchor 匹配语义，零新依赖、零 embedding/vector），返回匹配节点按 Level 排序的简要。
  不触碰 provider-visible 视图（不改变任何已有消息），缓存零扰动。
- **P2.2（可选，同阶段末）`context_restore(anchor)`**：标记「某段需要逐字」，但**不立即**改可见视图；
  由下一次 Projection commit（fold 事务）把该段并入逐字尾——只在 cache-aligned 提交点落地，
  避开跨请求缓存断裂。未标记 restore 时行为与现状一致。
- **数据**：`DigestTree` 的 term→nodeId 倒排为进程内构建（读取侧车时建，无持久化新字段）。
- **迁移**：无 Digest 的旧会话 → 工具返回
  `"no digest available (history too short or never compacted)"`，不报错。
- **性能指标**：`context_retrieve` P99 < 5ms（内存命中）；消耗 0 provider tokens；
  有效命中（检索结果被下一轮模型消费）> 60%。
- **验收（effect test）**：折叠会话内 `context_retrieve("某折叠事实")` 返回含该事实的节点；
  无 digest 会话返回优雅消息；检索后 `CaptureShape` 零变化。

---

## T4 Schema 剪枝（fold 对齐的一次性收缩）

**问题**：`providerToolSchemas()` 每请求全量发送；MCP 工具多时 schema 占用可观 token 且每次
请求都要进入 prompt（增加 prefix 体积与 cache 带宽）。

**设计**（核心约束：**tools hash 只在 projection commit 变**，绝不在回合中变）：
- `CompactionState` 增 `ToolSchemasVisible []string`（可见工具名集合，`omitempty`，**V4 additive**）。
- 新开关 `boot.Options.SchemaPrune bool`（默认 false）：开启时，fold commit 处把
  「本 fold 内有实际调用」的可选工具名写入 `ToolSchemasVisible`；`providerToolSchemas()`
  若侧车里有集合则过滤到该集合 + 强制核心集（bash/read/write/use_capability…），未命中的可选工具
  保持 `use_capability` 可达（capability_gate 已支持 on-demand 解析）。
- 已支持 `NativeToolSearch` 的 provider 走既有延迟路径，本机制只作用于非 native 端点。
- **边界**：剪枝决策只在 fold commit 做（读 `ToolSchemasVisible`），回合内 tools 恒稳定；
  `captureShape` 的 `ToolsHash` 每 fold ≤1 次变更。
- **迁移**：旧侧车缺字段 → 全量（现行为）；新侧车带集合 → 过滤。开关默认 false，行为字节不变。
- **性能指标**：schema token 足迹 = 核心集 + 常用集，上限 S（配置）；由 schema 变化导致的 cache
  miss 每 fold ≤1 次。
- **验收（effect test）**：默认关：ToolsHash 跨回合稳定、每 fold≤1 次变；开：仅 core+曾用工具
  出现在请求 schema 中，且 `use_capability` 仍能到达其余工具。

---

## T5 缓存对齐与 warm-cache 感知

**问题**：1M 下 cache miss 代价昂贵（1M tokens 全 miss）；`compactTrigger=80%` 是纯 token 阈值，
即使前缀缓存全热也照常触发 fold（徒增摘要成本并制造一次可避免的 cache write）。

**设计**：
- **T5.1 warm-cache 延迟压缩**：复用既有 `CacheStateWarm/Cold` 标签与 receipt 的
  `CacheHitTokens`，新增 `session.cacheState.lastCacheHitRatio`（进程内，不持久化）。
  新开关 `cacheAwareCompaction bool`（默认 false）：当 `lastCacheHitRatio ≥ 0.9` 且
  `est < hardInputCeiling` 时，`compactTrigger()` 允许越过 80% 直到硬顶（
  物理安全边界 `hardInputCeiling` 永远生效，绝不越界）。
  折叠触发从「纯 token 阈值」变「阈值 ∥ (warm-cache 达硬顶)」——既有语义（低于 80% 不压）保留。
- **T5.2 fold cache-alignment 形式化**：现有保证（fold 只在字节稳定 prefix 后 append summary）
  已是实现事实；补一条 effect_test 钉住：fold 后 `CacheDiagnostics.PrefixChangedReasons` 恰为
  `["compact_auto"]`（无 system/tools 原因）。不改代码，只补断言。
- **T5.3 cache-write 预算**：摘要 fold 的输入 ≤ `safeSummaryPromptTokenLimit`（已存在）+ 输出
  ≤ `summaryOutputMaxTokens`；新增报表字段 `CacheWriteBilledTokens` 已在 Usage 级存在，P1 采集基线，
  P3 按预算做 fold 分段（`CompactionModeChunked` 已存在，供 P3 用）。
- **边界**：T5 全部属 `agent`（`compact.go`/`context_manager.go`/`output_budget.go`）；
  T5.1 默认关，开时也永远不越过 `hardInputCeiling`。
- **性能指标**：warm-cache 会话 fold 频率下降 ≥30%；缓存命中率维持 ≥90%/会话；
  每次 fold 的 `CacheWriteBilledTokens ≤ 输入×1.05`。
- **验收（effect test）**：开 `cacheAwareCompaction` + 注入 warm usage → 85% 窗口不 fold 且不发请求
  越硬顶；关 → 80% 精确 fold（与基线一致）。T5.2 断言为既有链路附加（不改前缀字节）。

---

## 依赖、阶段顺序与不可实现清单

### 阶段顺序
```
P0(coder) -> P1(tester 回归+1M 负载数据) -> P2.1(T1+T2+T3 设计落地) -> P2.2(context_restore)
P3(T4+T5，依赖 P2.1 的 digest 可用 + P1 的 cache 基线)
```
P2/P3 实现以 P1 实测数据为准（T1 的封顶规模、T3 的命中率、T5 的 warm 阈值校准）。

### 本轮明确不可实现（识别拒绝项）
1. **Embedding/向量语义检索**（需新依赖、索引生命周期、provider/本地边界决策）——拒绝。
2. **`context_restore` 回合内逐字重注入**（破坏 cache-aligned 前缀）——只许在 fold commit 落地（P2.2），
   本轮不做。
3. **tools 热序重排/每回合动态 schema 收缩**（回合内 ToolsHash 翻转=cache 断裂）——只能 fold 对齐（T4）。
4. **native 1M 级 provider 端上下文编辑**（`CompactionModeNative` 依赖 provider 能力面，
   现响应对 1M 无保证）——不纳入。
5. **普通回合内改动 cache-stable prefix（system/tools 顺序）**——非协商红线。
6. **跨会话/团队共享 digest 缓存**（需共享上下文层）——不在本路线。
7. **1M 级 CI 压测常态化**（成本与托底资源）——归 P1/tester，只用定点压制用例，不进 normal CI。

### 交付验收红线
- 每阶段默认关闭、零值=现行为；现有 effect_test（compact/context/prompt-cache）全绿零改动。
- repolint 零新增（不加宽 baseline）；layer 规则不破（agent 唯一 owner，control 只透传开关，前端零逻辑）。
- `CompactionState` 只做 additive 升 V4，V1–V3 文件可读；`SaveCompactionState` 原子发布契约不变。
- 每阶段独立合入、独立验收（T2/T3 依赖 T1 的预算语义但不阻塞其合并）。