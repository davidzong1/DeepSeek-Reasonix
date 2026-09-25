# Team Member 缓存最终收口：三 Agent 并行执行方案

> 状态：待执行
> 日期：2026-09-25
> 适用范围：当前 A / B / C 合流工作树及其后续不可变快照
> 配套结论：`TEAM_MEMBER_CACHE_ABC_JOINT_CONCLUSION.zh-CN.md`
> 执行方式：一次并行执行；三个 Agent 同时工作，完成后由协调 Agent 统一验收

## 1. 目标与最终决策边界

本方案不是重新设计 A / B / C，也不是直接启动灰度，而是完成当前工作的最终收口：

1. 在同一个不可变代码快照上复核三方交付；
2. 把“观测链路已通”“受控样本分类成立”和“真实 Provider 行为有效”分开取证；
3. 补齐当前已知的实验、参数和跨边界证据缺口；
4. 输出可审计的 go / no-go 建议。

在本方案全部完成前，默认决策保持：**继续采样，行为优化不放行，不启动灰度**。

本方案不得把以下任何结果直接表述为优化成功：

- 本地单元测试或离线 benchmark 全绿；
- `provider_residual_unexplained == 0`；
- 单次或单网关真实会话命中率较高；
- 维护计数器成功落盘但本次值为零；
- 128 token append allowance 在当前样本中没有产生 residual。

## 2. 共同执行规则

### 2.1 固定输入

协调 Agent 在并行开始前提供一份执行清单，至少包含：

- 不可变基线 commit、合流 commit 或工作树归档 hash；
- Go 版本、测试命令、配置文件、模型、route、账号范围和实验时间窗；
- 本次允许修改的文件清单；
- 凭证使用边界和日志脱敏规则；
- 统一的样本命名、artifact 目录和报告模板。

若无法产生不可变快照，三个 Agent 可以先完成静态审计和本地验证，但最终报告必须标记为“当前工作树证据”，不得标记为独立构建验证。

### 2.2 并行隔离

- 三个 Agent 使用独立工作区、分支或只读快照；不得互相覆盖未提交文件。
- 每个 Agent 只能修改自己负责的报告、证据和测试夹具；生产代码变更必须先报告阻塞原因并由协调 Agent 单独批准。
- 不修改 usage 归一化、统计分母、Provider 原始 usage、任务脚本或失败样本分类来适配结果。
- 任何 unknown、retry、error、缺失 usage、route 漂移或账号漂移样本都必须保留并单独计数。
- 不采集 prompt 正文、工具参数、凭据或可还原用户内容。

### 2.3 统一产物格式

每个 Agent 结束时必须提交：

1. `RESULT.md`：结论、证据强度、适用范围、未决风险和建议；
2. 原始命令、版本和配置记录；
3. 测试或实验原始输出；
4. 失败、跳过、unknown 样本清单；
5. 回滚或撤销说明；
6. 明确的 `PASS` / `CONDITIONAL` / `BLOCKED` 状态。

不得只提交“全部通过”或“无问题”这类不可复核结论。

## 3. 三 Agent 并行分工

三个部分必须同时启动，写集互不重叠；每个 Agent 只对自己的证据负责。

---

## Part A：维护状态机与压缩成本契约

### A.1 目标

验证 A 侧改动是否满足维护状态机契约，重点关注重复 summary、headroom、low-yield latch、rescue 边界和 telemetry 粒度；不判断真实 Provider 命中率是否改善。

### A.2 工作内容

1. 在固定快照上复跑 A 侧定向测试：
   - 七态决策覆盖；
   - headroom 目标与 `visible_window_tokens` cap；
   - low-yield latch 的锁存、释放和 overflow bypass；
   - maintenance ladder 与 rescue 最后一级约束；
   - maintenance cost 四项计数器。
2. 对照计划 M1 检查 `summary_requests` 和 `rescue_planned` 的实际粒度，确认偏离是否已经由协调者书面接受。
3. 检查同一 generation / turn 下 pressure、overflow、pre-send、post-turn 是否可能重复调用 summary；给出状态转移或调用链证据。
4. 验证 A 侧 additive 字段不改变 provider-visible 请求字节和持久化 schema；若只能通过单元测试证明，明确证据上限。
5. 用边界构造测试攻击 `low_yield` 与 `recovered` 的分界，避免只测试理想构造值。
6. 对 `compactionProgress` 的死字段给出单独清理建议；本轮不得顺手扩大代码范围。

### A.3 禁止事项

- 不改 Team 统计、Provider usage、工具 schema 或 B/C 负责的请求形状；
- 不把“状态分类可复算”写成“压缩在生产中有效”；
- 不以调高阈值、关闭 compaction 或删掉失败样本解决失败；
- 不将 Context Rescue 作为常规优化臂。

### A.4 交付物

- A 状态机契约核验表；
- 定向测试命令和原始输出；
- generation / turn 重复调用检查结果；
- `summary_requests` 粒度偏离的接受或阻塞意见；
- A 侧回滚步骤和 `PASS` / `CONDITIONAL` / `BLOCKED`。

### A.5 A 侧通过条件

只有同时满足以下条件才可标记 `PASS`：

- 所有状态均有可达性或明确不可达理由；
- headroom、latch、rescue 的边界测试通过；
- 未发现同一 view 的无界重复维护；
- provider-visible golden 和持久化 schema 未发生非预期变化；
- telemetry 粒度限制已被记录，且不会被误报为逐 turn 精确值。

---

## Part B：Provider-visible 前缀与 128 参数验证

### B.1 目标

验证 B 侧请求形状是否稳定、消息改写是否可解释，并优先攻击 `append_block_allowance = 128` 这一影响归因闭合的关键假设。

### B.2 工作内容

1. 在固定快照上复跑消息数组指纹、首个分歧位置、消息改写数和 rewrite reason 测试。
2. 复跑四类离线 prefix benchmark：成员切换、后端重建、MCP 重注册和 fold；确认非 fold 请求保持 append-only，fold 至多产生一次可解释 rewrite。
3. 核验 Team member 配置继承链：`visible_window_tokens`、`cache_aware_compaction` 必须在活 Agent 消费结果上生效，而不是只检查 options 结构体。
4. 检查 `messages` reason 的生产可���形状，确保测试不依赖 producer 从不发出的合成输入。
5. 在可用账号、网关或 route 和上下文桶上采集真实请求形状摘要，验证 append miss 的块粒度分布；至少报告样本数、均值、p50、p90、最大值、超 128 数量和 unknown 数量。
6. 如果环境无法提供跨账号/跨网关样本，必须将 128 标为“未验证”，不得用单网关结果外推 Provider 常量。
7. 检查 B 的 4 个新增消息诊断字段未进入 eventwire 的影响范围；如本轮不补齐，提供明确的跨进程可见性风险记录。

### B.3 禁止事项

- 不把本地 hash 相同当作 Provider cache key 相同；
- 不修改 append allowance 以制造 residual 为零；
- 不改变请求排序、工具 schema 或动态尾部来迎合样本结果；
- 不将一次性必要 fold 的 miss 记成无理由 rewrite；
- 不把 eventwire 未接入描述成“前端已可观测”。

### B.4 交付物

- B 请求形状核验表；
- 四类 benchmark 原始结果；
- `append_block_allowance` 分布和跨环境覆盖表；
- messages reason 的 producer/consumer 可达性证明；
- eventwire 可见性风险及是否需要补丁的建议；
- B 侧回滚步骤和 `PASS` / `CONDITIONAL` / `BLOCKED`。

### B.5 B 侧通过条件

只有同时满足以下条件才可标记 `PASS`：

- append、fold、rewrite 的诊断字段与生产形状一致；
- 非 fold rewrite 均能关联到明确 reason，或进入显式 residual/unknown；
- 128 参数在预定环境中有足够分布证据；若无跨环境证据，只能标记 `CONDITIONAL`；
- 配置 cap 在 Team member 活 Agent 上可观察；
- eventwire 范围和限制已被记录。

---

## Part C：真实 Provider 条件矩阵与最终准入

### C.1 目标

建立真实 Provider 的可审计对照证据，执行 Baseline、A-only、B-only、A+B 四个正式条件；Rescue 仅作为末端故障验证，不得作为常规优化臂。C 负责最终 go/no-go 建议，但不得预设结果。

### C.2 工作内容

1. 冻结逐请求数据字典：member、team、session lineage、logical turn、provider attempt、route/model、账号、prompt tokens、cache hit/miss/write、usage source、request shape hash、maintenance reason、context bucket、latency、质量结果。
2. 预先定义有效样本：warm request、cold first request、retry、error、unknown、缺失 cache split、route 漂移和账号漂移不得混合；记录每类数量和剔除原因。
3. 执行四个条件矩阵：
   - Baseline：当前基线；
   - A-only：只启用维护状态机；
   - B-only：只启用请求形状稳定性；
   - A+B：启用两者；
   - Rescue：仅在达到末端超限条件时触发，标记 `NeverRoutine`。
4. 每臂使用相同任务脚本、成员、route/model、账号范围、时间间隔和上下文桶；先做 pilot，再按协调者冻结的样本量运行正式样本。计划建议每臂至少 30 个有效 warm request，并至少三次独立运行。
5. 报告加权 cache hit rate、miss tokens/request、总 input tokens/request、summary requests/turn、projection rewrite、cold session 成本、p50/p90 latency、任务完成率、必要上下文保留、工具调用正确率、usage 覆盖率和 unknown 比例。
6. 对 128 豁免造成的分类敏感性做重算：至少提供当前规则、较小阈值和“未知不归类”三种结果，不得只给单一闭合数字。
7. 复核 A/B 结果是否有真实触发：若 rewrite、rescue、`messages_rewritten_unclaimed` 或维护计数器为零，报告为“路径未触发”，不能报告为“路径无缺陷”。
8. 根据预先冻结的门槛输出 go/no-go；质量、延迟、安全、成员隔离任一缺失时，结论只能是 `NO-GO / CONTINUE-SAMPLING`。

### C.3 禁止事项

- 不把 mock、离线夹具或单次受控会话当作正式收益证据；
- 不为了满足样本量把 invalid/unknown 样本强行纳入；
- 不在实验完成后修改分母、阈值或样本分类；
- 不仅凭命中率百分点放行；
- 不在 rescue 未真实触发时声称 rescue 安全。

### C.4 交付物

- 逐请求数据字典和样本分类报告；
- Baseline/A-only/B-only/A+B/Rescue 原始实验记录；
- 质量、成本、延迟和 usage 覆盖率对照表；
- 128 参数敏感性分析；
- 灰度开关、回滚步骤和 go/no-go 建议；
- C 侧 `PASS` / `CONDITIONAL` / `BLOCKED`。

### C.5 C 侧通过条件

C 只有在以下事项全部满足时才可建议 `GO`：

- 四个正式条件均有足够有效 warm 样本，且三次独立运行方向一致；
- A/B 行为路径在真实负载中确实触发并有可解释变化；
- miss tokens/request 或总 input tokens 有改善且无不可接受成本回归；
- 任务完成率、必要上下文保留、工具调用正确率不劣于基线；
- p50/p90 latency、usage 覆盖率、unknown 比例和安全边界满足预设阈值；
- 结果不依赖未经验证的 128 常量；
- 所有变更有独立开关和可执行回滚。

否则必须建议 `NO-GO / CONTINUE-SAMPLING`。

---

## 4. 协调 Agent 的一次性合流动作

三个 Agent 完成后，协调 Agent 只执行以下一次性动作：

1. 收集三份 `RESULT.md`、原始输出和 artifact hash；
2. 检查三个写集没有交叉覆盖，并确认所有结果来自同一快照或明确标注版本差异；
3. 将 `PASS` / `CONDITIONAL` / `BLOCKED` 映射到统一结论：
   - A 或 B 为 `BLOCKED`：停止合流，修复或回滚；
   - C 为 `BLOCKED`：不作行为准入，保留观测和契约改动；
   - 任一关键证据为 `CONDITIONAL`：最多有限合流，不得灰度；
   - 三方全部满足准入条件：才可提交 go/no-go 评审。
4. 在不可变快照上重跑 build、test、vet、gofmt、repolint 和 cache guard；
5. 更新 `TEAM_MEMBER_CACHE_ABC_JOINT_CONCLUSION.zh-CN.md`，只写已证实结论，并保留未决项、原始 artifact hash 和适用范围；
6. 形成最终决策记录：`GO`、`NO-GO` 或 `CONTINUE-SAMPLING`，不得使用含糊的“基本通过”。

## 5. 并行派单摘要

### 派单给 Agent A

> 在固定快照上核验维护状态机、headroom、low-yield latch、rescue 边界和维护 telemetry；复跑 A 侧契约测试，检查 generation/turn 重复调用和 M1 粒度偏离。不得改 Team 统计、Provider usage、工具 schema 或 B/C 请求形状。交付 A 侧 `RESULT.md`、原始测试输出、回滚说明及 PASS/CONDITIONAL/BLOCKED。

### 派单给 Agent B

> 在固定快照上核验 provider-visible 前缀、消息数组指纹、rewrite reason、配置继承和四类 prefix benchmark；重点验证 append block allowance=128 的跨环境分布。不得以本地 hash 证明 Provider cache key，不得调阈值迎合结果。交付 B 侧 `RESULT.md`、原始输出、参数分布、eventwire 风险和回滚说明。

### 派单给 Agent C

> 在固定快照上冻结逐请求数据字典并执行 Baseline/A-only/B-only/A+B 条件矩阵；Rescue 只作末端验证。报告命中率、miss tokens/request、总输入 token、维护成本、rewrite、质量、延迟和 unknown；不修改分母或阈值适配结果。交付 C 侧 `RESULT.md`、实验原始记录、敏感性分析、回滚方案和 go/no-go。

## 6. 收口判定

本方案完成但未满足真实 Provider、质量、延迟或跨环境证据要求时，最终结论必须是：

> **观测链路和受控分类逻辑已完成收口；行为优化仍未验证，继续采样，禁止灰度。**

只有协调 Agent 在同一不可变快照上收集到三方完整证据，并确认所有预设门槛满足后，才允许输出 `GO`。
