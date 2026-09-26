# Team Member 重复压缩根因修复：三 Agent 并行执行方案

> 状态：待执行
> 日期：2026-09-24
> 目标：在不牺牲主线任务质量和必要上下文的前提下，减少无效压缩、避免同一上下文代际反复压缩、降低 miss tokens/request，并提高 Team member 的真实 Provider 缓存命中率。
> 适用范围：Team member session；普通 Agent 仅作为回归对照，不在本计划中改变其默认行为。

---

## 1. 执行结论

当前不应把问题简单归因于“`compact_ratio = 0` 导致每轮压缩”。现有实现中，`compact_ratio <= 0` 会回退到默认 `0.80`，而不是使用 0% 阈值；真正需要解决的是以下闭环：

1. 上下文估算、Provider 实际 prompt 和硬上限之间可能存在偏差，导致维护决策反复进入 overflow/force 路径。
2. 压缩完成后可能只释放很少空间，下一次工具结果或用户输入立即再次越过维护边界。
3. 每次 projection/fold/truncate 都可能重写 provider-visible 前缀，造成缓存重新付费。
4. Team member 的工具面、system prompt、session context 或动态注入内容若不稳定，会在没有新增有效历史的情况下产生 cache miss。
5. 紧急 Context Rescue 只能处理“普通压缩已无法把视图降到 hard ceiling 以下”的末端故障，不能替代日常缓存和压缩策略。

因此本次不以“满上下文时更快清空”为主目标，而以以下优先级推进：

- **P0：停止同一代上下文的重复无效压缩。**
- **P0：保证必要压缩后具有可验证的 headroom，避免下一轮立即再次压缩。**
- **P1：减少无必要的 provider-visible 前缀重写。**
- **P1：确认 Team member 的稳定前缀、工具 schema 和动态尾部边界。**
- **P1：用真实 Provider usage 验证命中率和 token 成本，而不是只看本地 hash。**
- **P2：仅在普通压缩失败且仍超硬上限时启用 Context Rescue。**

---

## 2. 已知约束与不可接受方案

### 2.1 语义约束

- `compact_ratio` 是普通自动维护的起始边界，不是“压缩效率”或“上下文剩余比例”。
- `compact_ratio = 0` 必须继续保持兼容语义：回退默认值或在配置层明确拒绝，不能静默变成每轮强制压缩。
- 压缩收益率采用：

  ```text
  reduction_ratio = (source_tokens - compacted_tokens) / source_tokens
  ```

  Context Rescue 的 `<10%` 指普通压缩收益不足 10%，不是压缩后只剩 10%。

- Rescue 生成的恢复块必须严格小于 10K tokens；新 session 首次请求的冷缓存成本必须单独计量。
- 原始 session 不能被破坏式删除；自动续跑采用受控的新 session rotation。

### 2.2 不允许的优化

- 不通过降低统计分母、排除低命中样本或修改 usage 归一化来“提高”命中率。
- 不把本地 prefix hash 当作 Provider cache key 的证明。
- 不为了缓存稳定而永久关闭压缩，导致请求越过 Provider hard ceiling。
- 不在每个工具结果到达后无条件调用 summary。
- 不把 Context Rescue 作为正常 compaction 的替代路径。
- 不在没有真实 Provider 对照和任务质量护栏的情况下直接扩大到全部成员。

---

## 3. 三 Agent 分工与不重叠写集

### Agent A：上下文维护状态机与重复压缩修复

**目标：**解决“同一上下文代际反复压缩”和“压缩后立即再次压缩”的根因。

**主要职责：**

1. 还原每次 `Prepare` 的真实决策链：估算 token、`compact_ratio`、hard ceiling、pressure/overflow/manual trigger、projection version、maintenance receipt。
2. 增加或完善 generation/turn 级维护状态，保证同一 provider-visible view 的失败或低收益结果不会在一个活跃 turn 内重复付费。
3. 为成功压缩增加可验证的 headroom 目标：压缩完成后记录 `source_tokens`、`result_tokens`、`hard_ceiling`、`headroom`，若没有达到目标，不得把它报告为已恢复。
4. 区分以下状态：
   - 未达到维护边界；
   - 已达到普通维护边界但仍低于 hard ceiling；
   - 已达到 hard ceiling；
   - 普通压缩成功并拥有足够 headroom；
   - 普通压缩无效/收益不足；
   - 已进入 Context Rescue；
   - 当前 generation 已被阻断，等待新增内容达到重试条件。
5. 保证 pressure、overflow、pre-send admission 和 post-turn observer 不会对同一代上下文重复执行 summary。
6. 验证 `cache_aware_compaction` 的策略：缓存 warm 时延迟普通 fold，但 hard ceiling 仍必须有安全维护路径。
7. 仅在满足“收益低于 10%且仍越过 hard ceiling”等条件时把控制权交给 Context Rescue；普通压缩成功时不得轮转 session。

**建议代码范围：**

- `internal/agent/context_manager.go`
- `internal/agent/context_receipt.go`
- `internal/agent/compact.go`
- `internal/agent/compaction_*`
- `internal/agent/context_rescue.go`（仅修改触发契约和状态传递）
- `internal/agent/sampling_request.go`
- 对应 `internal/agent/*_test.go`

**禁止修改：**

- Provider wire usage 归一化；
- Team owner writer 和报表统计口径；
- system prompt、tool schema 的具体内容；
- 真实 Provider 实验样本筛选。

**必须交付：**

- “一次维护决策”的状态图和字段契约；
- 同一 view 多次 `Prepare` 不重复 summary 的单元测试；
- 压缩后 headroom 不足时的行为测试；
- pressure → overflow → rescue 的唯一转移测试；
- maintenance cost 统计：summary requests、projection installs、rescue count、重复阻断次数。

---

### Agent B：Team member provider-visible 前缀稳定性与缓存形状修复

**目标：**降低无必要的前缀重写，使新增内容主要落在缓存前缀之后，并确认 Team member 与普通 Agent 的形状差异。

**主要职责：**

1. 对每次成员请求记录脱敏后的请求形状：system hash、tools hash、session context digest、消息前缀 hash、首个分歧位置、rewrite reason；不保存 prompt 正文、工具参数、凭据。
2. 检查 Team member 构造链路是否稳定继承：
   - member identity；
   - role prompt；
   - tool schema 顺序和内容；
   - workspace/session context；
   - MCP 延迟工具尾部；
   - provider route/model；
   - approval/permission 相关 provider-visible 字段。
3. 保证稳定前缀只在 session 初始化或确有配置变化时改变；动态任务通知、成员状态、唤醒信息和当前 turn 指令必须位于稳定前缀之后。
4. 对 tool schema 做稳定排序、稳定序列化和重复注册去重；工具列表变化必须有明确 reason，不得因 map 遍历顺序或成员切换造成随机改写。
5. 检查 compaction/prune/truncate 后的消息投影是否只改写必要历史区域，不重新生成 system/tools 前缀。
6. 对 `cache_aware_compaction`、`visible_window_tokens`、Team member backend 继承配置做端到端验证；确认成员不是因为选项在 `boot.Build` 前后被丢弃而回到默认行为。
7. 建立离线 prefix benchmark：追加一条新消息只能导致尾部 divergence；发生 fold 时最多允许一次可解释的历史区域重写。

**建议代码范围：**

- `internal/agent/cache_shape.go`
- `internal/agent/cache_shape_test.go`
- `internal/agent/session.go`
- `internal/agent/projection.go`
- `internal/agent/prune.go`
- `internal/cli/team_backend_build.go`
- `internal/cli/team_backend_options.go`
- `internal/cli/chat_tui_team_session.go`
- `internal/boot/boot.go`
- 对应 Team backend、工具面和 prefix benchmark 测试

**禁止修改：**

- Agent A 所有 compaction 状态机和 retry 规则；
- Agent C 的统计分母、Provider 采样判定和灰度开关；
- 为追求命中率直接删除主线必要消息或工具能力。

**必须交付：**

- Team member 请求形状字段表；
- 稳定前缀与动态尾部的边界说明；
- system/tools/session-context 变更的 reason 枚举；
- 长上下文、工具输出、成员切换、MCP 延迟工具四类离线 benchmark；
- 能证明“非 fold 请求不重写已发送前缀”的测试。

---

### Agent C：观测、真实 Provider 对照实验与合流准入

**目标：**建立可追溯的逐请求证据，判断 A/B 修改是否真的提高命中率并降低成本，负责最终 go/no-go，而不是预先假设优化有效。

**主要职责：**

1. 冻结逐请求统计契约：member、team、session lineage、logical turn、provider attempt、route/model、prompt tokens、cache hit/miss/write、usage source、request shape hash、maintenance reason、context bucket。
2. 明确样本分类：valid single request、aggregated attempts、retry、error、missing cache split、unknown/estimated、cold first request、warm request；分类后不得混为 0 hit。
3. 扩展 Team member 观测，使其能够区分：
   - 普通追加造成的 miss；
   - compaction/prune/truncate 造成的 rewrite miss；
   - Context Rescue 新 session 的冷前缀 miss；
   - Provider scope/TTL/route 造成的未知或外部 miss。
4. 建立固定任务、固定 member、固定 route/model、固定账号范围的实验矩阵：
   - 基线：当前版本；
   - A-only：仅上下文维护状态机；
   - B-only：仅请求形状稳定性；
   - A+B：合流版本；
   - Rescue-enabled：仅验证极限恢复，不作为常规优化臂。
5. 每个条件先运行 pilot，再进行正式样本；建议 pilot 每臂至少 8 个有效请求，正式每臂至少 30 个有效 warm requests，并记录无效样本和停止原因。
6. 同时报告：
   - 加权 cache hit rate；
   - miss tokens/request；
   - 总 input tokens/request；
   - summary requests/turn；
   - projection rewrite 次数；
   - 首次冷 session 成本；
   - 任务完成率、必要上下文保留、工具调用正确率；
   - p50/p90 latency；
   - usage 覆盖率和 unknown 比例。
7. 负责灰度、回滚和最终准入，不得仅凭命中率百分点放行。

**建议代码范围：**

- `internal/team/cachediagnosis.go`
- `internal/team/cachereport.go`
- `internal/team/cachereport_stats.go`
- `internal/cli/team_cache_audit.go`
- `internal/cli/team_cache_report.go`
- `internal/cli/team_usage_observe.go`
- `internal/cli/team_usage_publish.go`
- `internal/provider/*` 的原始 usage 保留（仅在需要时）
- `internal/cachelab/*`
- `internal/cli/*cache*_test.go`
- 实验及报告文档

**禁止修改：**

- 在证据门通过前修改 provider-visible 请求；
- 改变 A/B 的统计口径以适配结果；
- 把 mock benchmark 当作真实 Provider 结果；
- 与 A/B 同时编辑相同生产文件。

**必须交付：**

- 逐请求数据字典和有效样本 SQL/脚本；
- 基线、A-only、B-only、A+B 报表；
- 真实 Provider 实验日志及费用/安全边界；
- 质量和成本对照结论；
- 灰度配置、回滚步骤和 go/no-go 建议。

---

## 4. 先行依赖与共同冻结项

三 Agent 可以立即并行进行只读盘点，但正式改动和实验前必须完成以下共同依赖。

### M0：执行负责人冻结契约

由项目负责人或单独的协调 Agent 完成，不归属于 A/B/C 任一写集：

- 冻结本文版本和目标分支；
- 冻结当前基线 commit、模型、route、账号范围和实验时间窗；
- 冻结 `compact_ratio = 0` 的兼容语义；
- 冻结压缩收益率、hard ceiling、headroom 和 rescue 的定义；
- 冻结 Team member 统计分母和 unknown 样本处理；
- 确认日志不包含 prompt 正文、工具参数和凭据；
- 为每个 Agent 建立独立工作区或独立分支，禁止互相覆盖未提交修改。

M0 未完成时：A/B/C 可以读代码和写测试设计，但不得提交会改变生产请求行为的补丁。

### M1：A/B 接口契约对齐

A、B 需要共同确认但不共享实现文件的字段：

```text
MaintenanceReceipt:
  generation
  trigger
  source_tokens
  result_tokens
  hard_ceiling
  headroom_tokens
  reduction_ratio
  action
  projection_version
  cache_break_reason
  summary_requests
  rescue_planned

RequestShape:
  member_id
  session_id_hash
  lineage
  route_bucket
  system_hash
  tools_hash
  session_context_hash
  message_prefix_hash
  first_divergence_offset
  rewrite_reasons
```

字段可以按现有结构复用，不要求照抄新增；但 A、B、C 必须使用同一命名和语义，不能出现“projection version”“rewrite version”“fold count”各自代表不同事件的情况。

### M2：C 建立观测可行性

C 先确认以下条件是否成立：

- 能拿到真实 Provider 的 cache hit/miss 或等价原始字段；
- 能关联 member/session/turn/attempt；
- 能区分冷请求、重试、错误和无 cache split；
- 能固定 route/model/账号和实验输入；
- 费用与数据脱敏边界获批准。

若 M2 不成立，A/B 仍可做客户端稳定性修复，但不能声称已提高真实 Provider 命中率。

---

## 5. 并行执行顺序

### 阶段 P0：并行只读盘点

A、B、C 同时进行，交付时间点相同：

- A：输出当前维护状态图和“每轮压缩”的可能入口；
- B：输出 Team member provider-visible 请求形状图和潜在 rewrite 点；
- C：输出现有 usage 链路、数据缺口和实验可行性报告。

合流条件：三份报告必须能回答“哪一个事件触发了压缩、哪一个事件改变了前缀、哪一个字段证明 Provider 命中下降”。

### 阶段 P1：并行实现与测试

M0/M1 完成后：

- A 实现维护状态机、headroom 和重复压缩抑制；
- B 实现请求形状稳定性、工具面稳定排序和 prefix benchmark；
- C 实现逐请求诊断、实验夹具和基线报告。

A/B 不修改同一文件；C 只修改观测、实验和报告文件。若必须跨边界修改，先提交接口变更说明，由协调 Agent 合并。

### 阶段 P2：离线合流验证

协调 Agent 按以下顺序合并：

1. C 的数据结构和测试契约；
2. A 的 compaction 状态机；
3. B 的 cache shape 稳定性；
4. C 的集成测试和报表。

验证重点：

- 普通 append-only 请求不会重复 summary；
- 同一失败 view 在同一 turn 不重复付费；
- 必要 fold 后达到 headroom，不会下一请求立即重 fold；
- 非 fold 请求不改写已发送前缀；
- fold 只产生一次可解释的 rewrite；
- Team member 与普通 Agent 的默认行为不发生意外变化；
- Context Rescue 只在 hard ceiling 仍无法恢复时触发。

### 阶段 P3：真实 Provider 对照

至少执行以下实验臂：

| 实验臂 | 改动 | 目的 |
|---|---|---|
| Baseline | 当前基线 | 确认问题可复现和波动范围 |
| A-only | 仅维护状态机/headroom | 判断重复压缩是否是主要成本源 |
| B-only | 仅 provider-visible 形状稳定 | 判断前缀 rewrite 是否是主要 miss 源 |
| A+B | 两者合流 | 验证叠加效果和交互影响 |
| Rescue | 开启 rescue，仅极限场景 | 验证不会把常规请求变成 session 冷启动循环 |

每臂必须记录相同的任务脚本、成员、route/model、账号范围和时间间隔。遇到 usage 缺失、route 漂移或样本不满足契约，标记为 unknown，不得强行纳入命中率。

### 阶段 P4：灰度与回滚

只有通过 P3 准入门槛才可灰度：

1. 先以 shadow/diagnostic 模式运行，不改变请求行为；
2. 再对白名单 Team member 开启 A；
3. 再对白名单开启 B；
4. 最后在极限上下文成员上单独开启 Rescue；
5. 每阶段保留上一阶段配置和二进制，出现质量、成本、延迟或命中恶化立即回滚。

---

## 6. 合流验收门槛

### 6.1 必须同时满足

- 重复压缩：同一 generation/turn 的 summary requests 显著下降，且不存在无界增长；
- headroom：成功维护后，下一次正常请求不会仅因相同 view 再次进入维护；
- 缓存：有效 warm 请求的加权命中率提升，或在命中率不显著变化时 miss tokens/request 和总输入 token 明显下降；
- 成本：summary token、总 input token、provider 请求数不出现不可接受增长；
- 质量：主线任务完成率、必要事实保留、工具调用正确率不劣于基线；
- 稳定性：Team member 之间无跨 session、跨成员、跨 route 的状态污染；
- 安全：不产生超 hard ceiling 的 provider 请求；
- 可回滚：所有行为变更有独立开关或可通过版本回滚。

### 6.2 建议判定指标

具体阈值须由 C 在 M0 后根据 pilot 波动冻结，不能事后调整。建议至少报告：

```text
cache_hit_rate_weighted
cache_miss_tokens_per_request
input_tokens_per_turn
compaction_requests_per_turn
compaction_rewrite_count_per_turn
rescue_count_per_100_turns
cold_start_miss_tokens
quality_success_rate
latency_p50 / latency_p90
usage_coverage
unknown_rate
```

建议以“相对基线方向一致、跨至少三次独立运行”作为必要条件；若只有命中率上升但 miss tokens、质量或延迟恶化，不放行。

---

## 7. 失败分支与回退方案

### F1：发现实际阈值并非 0，但每轮仍触发维护

优先检查：

- overflow/pre-send 是否覆盖了 pressure receipt；
- observed token 与 estimated token 是否不在同一尺度；
- projection 安装后是否正确更新 generation/input hash；
- Team member 是否每轮重新构造工具面或 session context；
- retry 是否重复执行整个 turn 的 preflight。

不得直接把阈值调高或关闭 compaction。

### F2：压缩收益低且无法达到 headroom

保留原 session，先阻止重复 summary；只有满足 rescue 条件才生成恢复块并轮转。若 rescue 连续失败，暂停自动重试并显式报告，而不是无限消耗 token。

### F3：客户端前缀稳定但 Provider 命中仍低

停止继续改消息排序；转向 Provider scope、TTL、容量、账号池和 usage 语义实验。此时只能报告“客户端无法解释”，不能宣称本地优化失败或成功。

### F4：客户端前缀发生无理由改写

优先修复变更来源和 reason 归因；在修复前不扩大 Team 灰度。若是必要 fold，接受一次性 miss，但必须证明后续请求重新 append-only。

### F5：真实 Provider 实验不可行

可以合并日志、状态机和离线不变量修复，但将缓存收益标记为“未验证”；禁止以 mock 结果作为生产命中率承诺。

---

## 8. 最终交付物

### Agent A

- 状态机设计与实现；
- 重复压缩和 headroom 测试；
- compaction telemetry；
- A 分支结果报告和回滚说明。

### Agent B

- Team member 请求形状审计；
- stable prefix / dynamic tail 修复；
- prefix benchmark 和 rewrite reason；
- B 分支结果报告和回滚说明。

### Agent C

- 逐请求观测契约和报告；
- 基线、A-only、B-only、A+B、Rescue 实验结果；
- 质量/成本/延迟护栏；
- 灰度开关、回滚步骤和最终 go/no-go。

### 协调 Agent

- M0/M1/M2 冻结记录；
- 合并冲突处理；
- P2/P3/P4 验收记录；
- 最终是否扩大灰度的决策。

---

## 9. 直接派单文本

### Part A / Agent A

> 负责上下文维护状态机与重复压缩根因修复。先只读还原 `Prepare`、pressure、overflow、pre-send、post-turn 和 Context Rescue 的触发链；再实现 generation/turn 级去重、压缩后 headroom 目标和明确的维护 receipt。不得修改 Team 统计、Provider usage 归一化和工具 schema。交付状态图、代码、单元测试、重复压缩成本 telemetry 和回滚说明。

### Part B / Agent B

> 负责 Team member provider-visible 前缀稳定性。审计 system/tools/session context/member identity/MCP 动态尾部/工具排序/配置继承，定位无理由 rewrite；实现稳定序列化、明确 rewrite reason 和离线 prefix benchmark。不得修改 Agent A 的 compaction 状态机，不得删除主线必要上下文，不得改变统计分母。交付请求形状字段表、代码、测试和回滚说明。

### Part C / Agent C

> 负责逐请求观测、真实 Provider 对照实验和最终准入。冻结 member/session/turn/attempt/route/model/usage/rewrite reason 口径，区分 warm/cold/retry/error/unknown；建立 Baseline、A-only、B-only、A+B、Rescue 实验，报告命中率、miss tokens/request、压缩成本、总输入 token、质量和延迟。证据不足时必须给出“不放行/继续采样”，不得为配合结果修改统计口径。

---

## 10. 预期结果

成功标准不是单纯让某个平均命中率数字上升，而是同时满足：

1. 同一上下文代际不再反复执行无效压缩；
2. 必要压缩后拥有可验证 headroom，后续请求不会立即再次压缩；
3. 非必要请求保持 append-only provider-visible 前缀；
4. Team member 的 miss tokens/request 和总 input token 下降或不劣化；
5. 有效 warm 请求的命中率在真实 Provider 上方向一致改善，或在命中率不变时成本显著下降；
6. Context Rescue 只处理末端超限故障，不形成新的 session 冷启动循环；
7. 任务质量、必要上下文、工具调用和成员隔离不下降；
8. 所有行为变更可以灰度、观测和回滚。
