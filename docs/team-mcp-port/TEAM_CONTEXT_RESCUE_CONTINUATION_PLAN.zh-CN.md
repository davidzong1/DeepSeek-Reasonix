# Team 成员上下文救援与续跑优化路线

> 状态：分析与实施计划（2026-09-24）；本稿不改运行时代码。
> 范围：Team member 在上下文接近/超过窗口、常规 compaction 无法有效回收时，保留主线任务并在新 session 续跑。
> 目标不是改善缓存命中率本身，而是避免上下文耗尽导致任务中断；新 session 冷前缀成本必须单独观测。

## 1. 结论

机制可行，但不应等 provider 请求因超窗失败后才补救，也不能直接调用交互式 `/clear`。应在 `ContextManager.Prepare()` 的请求准入阶段判断；先生成并校验有硬上限的恢复摘要，再由 Controller 安全创建关联的新 session，注入恢复块并启动续跑。只有恢复块已持久化且新 session 可用后，才允许旧会话进入清理流程。

现有实现已经包含多数基础组件，但没有跨 session 自动续跑的事务：

| 能力 | 当前行为 | 关键位置 |
|---|---|---|
| 发送前窗口维护 | `Prepare()` 估算 provider 可见请求；触发 prune/fold，并对 overflow 做恢复 | `internal/agent/context_manager.go` |
| 常规摘要 | `compactionInstruction` 提取约束、目标、决策、文件、命令、错误和下一步；摘要输出预算为 8192 tokens | `internal/agent/compact.go` |
| 压缩失败兜底 | 达硬上限后可能进入有损 truncation rescue，不具备完整跨 session 续跑语义 | `internal/agent/context_manager.go` 的 `rescueOrFail` / `rescueByTruncation` |
| Provider 超限后重试 | `recoverContextLimit()` 先尝试缩减输出预算；无物理余量时强制 overflow prepare，要求 projection version 前进后重建请求 | `internal/agent/context_recovery.go` |
| 分块摘要 | `session_extract.go` 已有 chunked/tree-reduce 摘要，但目前是同 session compaction fallback，不是跨 session continuation payload；自动 overflow 默认不开放该 chunked fallback | `internal/agent/session_extract.go`、`internal/agent/compact.go`、`internal/agent/compact_projection.go` |
| 清空会话 | `ClearSession()` 是破坏式清理；清理旧 artifacts 并创建干净 session，不保留待恢复主线 | `internal/control/session_clear.go` |
| session 轮转 | Controller 有轮转 gate、session transition 与新旧 session 绑定机制；运行中的 turn 不允许普通 clear | `internal/control/session_binding.go`、`internal/control/session_transition.go` |
| 缓存影响 | 现有 warm-cache compaction 可推迟至 `hardInputCeiling`，但新 session/新前缀会冷启动 | `internal/agent/compact.go`、`internal/agent/cache_shape.go` |

`cpp_ipc_team` 的成员缓存优化经验可迁移的是：按成员与逐请求记录 prompt tokens、hit/miss、稳定前缀变化原因和 session 阶段；不可把 session 累计命中率或 context gauge 当作单次大 prompt 的命中率。该机制应复用/扩展现有 `CacheDiagnostics` 与 Team usage 上报，不以“恢复后命中率必须不降”为不现实验收条件。

## 2. 触发与比例语义

用户所说“压缩率 <10%”有两种相反解释，实施前需固定字段与定义：

- `remaining_ratio = compacted_tokens / source_tokens`：压缩后仅剩不到 10%。这代表压缩非常有效，不应触发救援。
- `reduction_ratio = (source_tokens - compacted_tokens) / source_tokens`：压缩收益不足 10%。这代表压缩无效，符合救援意图。

本路线建议以 `reduction_ratio < 0.10` 表示“常规压缩无效”，同时还必须满足压缩后请求仍 `>= hardInputCeiling`（或窗口占用已到达/超过 100%）。仅比例低不能触发新 session。source/result token 必须用相同的 provider-visible 估算口径；避免把摘要输入 token 与完整请求 token 混用。比例及 token 数都写入诊断事件。

超限判据以 `ContextManager` 的最终请求估算和 `hardInputCeiling` 为准，而非 UI context gauge 的四舍五入显示值。正常请求必须在 provider 调用前被截停并救援；若未知窗口、估算不可信或请求超出物理上限，走 fail-closed，不发送超限请求。

## 3. 推荐执行流程

1. 首选请求准入阶段在发送 provider 请求前冻结当前 canonical transcript、当前 projection、Team member 身份与 session generation，记录 `source_tokens`。现有 `recoverContextLimit()` 是 provider 已拒绝一次请求后的二级入口；后续集成要确保在 `Prepare()` 已发现硬超限时优先救援，而不是依赖超窗错误触发。
2. 先执行现有 prune/fold；若压缩后低于硬上限，正常发送，不轮转 session。
3. 若压缩结果仍超硬上限，计算 `reduction_ratio`。低于 10% 时进入 context rescue；比例达到/超过 10% 时优先沿用现有超限恢复策略，不因比例单独清会话。
4. 基于冻结视图生成结构化恢复块，至少含：用户主线目标、不可违背约束、已完成/未完成进度、关键决策、改动文件与状态、命令/测试结果、阻塞与下一步。摘要不得编造；不确定项标记未知。
5. 用当前模型 tokenizer 对最终注入内容计 token，严格 `<10,000` tokens；建议摘要目标预算 `<=8,000` tokens，为 wrapper、角色消息和 member/session 元数据留余量。超限先做一次有界二次压缩并复验；仍超限则不轮转、不丢原 session，向用户/上层返回明确错误。
6. 将恢复块和来源 session ID/generation、摘要 hash、token 数、触发原因写入新 session 的首条 host-generated continuation 消息；保持其为普通首条消息，不改 system prompt 或在 turn 中途重排 tools，以限制 cache-stable prefix 变化。
7. 创建新 session 并绑定相同 workspace、模型配置、Team member identity、权限与工具集合，建立 source→continuation lineage。恢复块持久化且新 session transition 成功后，才将续跑回合入队。
8. 保留旧 transcript 供审计/回退；不要调用会删除旧 artifacts 的 `/clear`。生命周期清理由独立、可恢复的过期策略处理。
9. 续跑成功后在新 session 的 usage/cache diagnostics 中标注 `context_rescue` 与冷前缀首请求；设置 lineage 级防重入标记，禁止同一轮重复 rescue。若新 session 再次超限，限制自动救援次数后暂停并通知用户。

> 说明：现有摘要生成器输出结构可复用，但它的 8192 token 输出预算不自动等于最终恢复块的 token 保证；必须对最终消息再做模型 tokenizer 硬校验。

## 4. 两部分并行推进

两个 Agent 可按不重叠写入范围并行；Part A 先提供接口契约与假实现/测试边界，Part B 基于固定契约实现 Controller 会话轮转。合并时做跨层集成与并发测试。

### Part A：Agent 上下文救援判定与恢复块

**范围**：`internal/agent/context_manager.go`、`internal/agent/compact*.go`、`internal/agent/projection.go` 及其 agent tests。不得改 Controller 清理行为。

- 定义明确结果类型，例如 `ContextRecoveryPlan`：触发原因、source/result tokens、reduction ratio、恢复消息、摘要 hash、源 transcript generation、原 turn/dedup key；结果不得直接清 session。
- 在已有 fold 完成后且 provider 请求发送前评估是否需要 rescue；避免仅凭一次缓存命中率或 gauge 触发。
- 复用结构化摘要指令及 `session_extract.go` 的 replay-safe message unit/tree-reduce 能力，但将最终注入块硬限制为 `<10K tokens`，摘要目标 `<=8K`；tool-call 与 tool-result 成对保留，未完成工具标记为 unknown 且不得自动重放；检查空摘要、输出截断、未知 tokenizer 和 generation 变化。
- 摘要失败、超预算或源 transcript 在生成期间变化时，返回可分类错误，禁止产生可被误用的 recovery plan；不得静默退化成损失关键信息的摘要。
- 输出独立 telemetry：`source_tokens`、`compacted_tokens`、`reduction_ratio`、`summary_tokens`、`trigger`、`generation`、`outcome`；不写敏感 transcript 正文。

**验收测试**：比例两种语义边界；压缩后低于硬顶不触发；压缩收益 `<10%` 且仍超限时产出 recovery plan；恢复块恰低于/等于/高于 10K 的边界；摘要失败/截断/并发 transcript 变化时不产出 recovery plan；防止无窗口估算时误发送超限请求。

### Part B：Controller 安全轮转与续跑

**范围**：`internal/control/session_transition.go`、新增独立 continuation rotation API（避免复用破坏式 clear）、相关 control tests；如需接入 Team runtime，仅通过现有 session owner/transition callback。

- 增加接收已校验恢复 plan 的专用操作；要求操作在 turn admission/handoff 边界执行，不能在运行中直接调用 `ClearSession()`。
- 原子/可恢复地先保存源 session，再准备新 session 与首条 continuation 消息；新 session 创建或写入失败时保留旧 session 可继续/可恢复。
- 新旧 session 通过 lineage 关联；复制最小必要的 workspace、模型、member 身份、权限与工具 surface，不继承不应跨会话的审批状态、临时授权或旧 UI 状态。
- 成功提交 transition 后只对该 Team member 排入续跑，不影响 leader、其他 member 或共享任务状态；清晰处理取消、并发 turn、owner lease 与崩溃恢复。
- 不调用 `ClearSession()` 的删除语义。现有 rotation transition 可参考，但 continuation 必须单独定义保留旧 source 的策略。

**验收测试**：旧 transcript 保留；新 session 恰有一条受限恢复消息并能启动后续采样；摘要持久化/新 session 创建/owner handoff 各失败注入时均不丢主线；running turn 拒绝或排队处理；同一 member 隔离；防重入；崩溃恢复后不重复启动续跑。

## 5. 集成阶段、观测与验收

1. **P0 契约与基线**：统一“压缩收益 <10%”定义；补逐请求诊断；采集触发前后 source/result/summary tokens、prompt tokens、命中/未命中绝对数、prefix hash/reasons、模型与 member ID。用真实大上下文样本验证 gauge 与 prompt tokens 的差异。
2. **P1 Part A/B 并行实现**：按上面范围分别实现；共享 recovery-plan/schema 契约先冻结，Part A 不持有 session lifecycle，Part B 不重新解释比例。
3. **P2 合流与故障注入**：验证跨层事务、序列化、取消、session lease、续跑恰好一次语义；覆盖 Team member 与普通 Agent 的隔离。
4. **P3 灰度启用**：默认关闭或仅诊断模式；先 shadow 生成恢复块但不轮转，比较摘要 token 与人工任务完成度；再对白名单 Team member 启用自动续跑，保留显式停止开关。
5. **P4 评估**：统计救援触发率、成功续跑率、主线任务完成率、摘要失败/超限率、重复救援率、冷启动首请求 cache miss tokens、后续请求 hit ratio、额外摘要成本与延迟。按成员、模型、prompt token 桶、session 阶段拆分。

成功标准：救援消息严格 `<10K tokens`；救援不丢源 transcript；新 session 能在相同权限边界内继续主线；无重复/跨成员副作用；provider 超窗请求为零；任务完成率相较现有截断兜底提高。缓存指标报告救援造成的一次性冷前缀成本，不将 session 总体命中率误作唯一通过条件。

## 6. 风险与明确不做

- 不把自动 rescue 实现为模拟输入 `/clear`；不调用破坏式 clear 删除旧 session。
- 不把 compaction 摘要放入 system prompt；不在普通回合内改变工具 schema、system prompt 或消息前缀排序。
- 不用“摘要生成成功”代替 `<10K` tokenizer 校验；不在摘要失败时仍清理源 session。
- 不跨 member 共享摘要、权限、待审批操作或运行状态；每个成员只轮转自己的 session。
- 不声称该功能会提升 provider cache hit ratio。新 session 首请求预期冷；优化目标是可靠续跑，缓存损耗需量化并尽量只发生一次。
- 不以 cache hit rate 代替上下文窗口使用量；Team 平均仍按成员和请求 token 口径分别呈现。

## 7. 本地代码核对锚点

- `internal/agent/context_manager.go`：`Prepare`/`prepareOnce`、`foldContext`、`summaryFailed`、`rescueOrFail`、`rescueByTruncation`。
- `internal/agent/compact.go`：`compactionInstruction`、`summaryOutputMaxTokens`、`hardInputCeiling` / cache-aware trigger。
- `internal/agent/projection.go`：`CompactionState`、`CompactionTelemetry` 与 source/result token 字段。
- `internal/control/session_clear.go`：破坏式 `ClearSession()`，不适合作为自动续跑 API。
- `internal/control/session_binding.go`：`rotateExclusiveSession` 与 rotation gate；普通运行 turn 不应直接 clear。
- `internal/control/session_transition.go`：transition candidate、owner handoff 与 commit/publish 生命周期。
- `internal/cli/team_usage_publish.go`、`internal/team/ownerusage.go`：Team usage 汇总扩展点；优先补逐请求 rescue/cache 诊断，不只扩累计平均值。
