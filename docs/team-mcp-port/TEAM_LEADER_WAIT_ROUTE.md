# 分析与优化路线 —— Leader 的可中断等待（leader wait）

> 状态：**Part 1（等待侧）与 Part 2（信号侧）均已完成并过门禁**（2026-09-22，两段合并后的树上实跑，
> 证据见 §5.2/§5.3/§6）。
> §4.5 的四项拍板已由用户裁定（见 §4.5）。节点状态只在 §6 更新，证据只写已实跑过的。
> 权威优先级：用户拍板 > `docs/team-mcp-port/TASK.md` > 本文档 > 实现代码注释。
> 关联：`TEAM_MEMBER_PARALLELISM_ROUTE.md`（同一支线的帧路径/串行点，本文档复用其 A/B 拆分纪律）、
> `LEADER_CONTEXT_TOKEN_ROUTE.md`（leader 轮询 token 成本的实测来源，本文档的动机之一）、
> `TOOL_MAPPING.md`（`leader_sleep` 的废弃记录，本文档的起点）。

---

## 1. 现象

Leader 需要「等成员干活」时，当前没有等待原语可用，于是退化成两种坏形态之一：

1. **前台 `bash sleep`**（如 `Bash(sleep 50)`）：占满整个 agent step，期间**任何事件都进不来**
   ——成员回报、权限升级请求、用户输入都不例外。已核实链路见 §3.1。
2. **轮询 `leader_check_member_status`**：每轮一次完整 provider round-trip（整个前缀重发），
   而 `LEADER_CONTEXT_TOKEN_ROUTE.md` 实测过这一类浪费：**45 次查询 / 1,358 秒，中位间隔 15.2s**。

两种形态都是「用模型回合换时间」，而真正需要的是**让出时间但不停止思考的能力**：
一有事件就立刻带着事件内容继续推理。

---

## 2. 根因：等待原语在移植时被删除且未替代

`TOOL_MAPPING.md:70` 记录得很明确：

| 旧工具 | 来源 | 处置 |
| --- | --- | --- |
| `leader_sleep` | `mult_agent_mcp.py:7999 def leader_sleep` | **废弃** — 「tmux leader 生命周期产物；新域无对应状态」 |

而旧流程**要求** leader 分配后必须等待（`AGENT_OPTIMIZATION_TECHNICAL_ROUTE.md:57`：
「分配后必须 `leader_sleep(max_seconds=600)`」）。移植把 `leader_sleep` 判为 tmux 生命周期产物而删除，
但没有在进程内域补上等价能力。

结果：`internal/cli/team_member_tools.go:68-88` 的 leader 工具集里
（`leader_list_team` / `leader_select_task_members` / `leader_assign_subtask` /
`leader_assign_task_to_relevant` / `leader_check_member_status` / `leader_retry_task` /
`leader_cancel_task` / `leader_reassign_task` / `leader_authz_log` /
`team_knowledge_recall` / `team_knowledge_expire`）**没有任何等待工具**。
Leader 唯一的「等」就是反复读状态，而该读还被 `minStatusPollInterval = 60s` 节流。

**结论：「改成内部组件睡眠」不是新增优化，而是补回一个被误删的必需原语。**
`bash sleep` 只是用户在缺原语时的替代品，不该被当成设计。

---

## 3. 为什么必须「可中断」，而不是「睡够时间再看」

### 3.1 睡眠期间的现有事件全部到不了 leader

| 事件 | 现状终点 | 能否打断 `Bash(sleep 50)` |
| --- | --- | --- |
| 成员回报 | `runtime.Complete` → `wakeAll`（`internal/team/agentruntime/runtime.go:418`）→ `wakeLeader`（`internal/cli/team_task_service.go:72`）→ **board 追加 wakeup 事件** | ❌ |
| 权限升级请求 | `writeAccessEscalations.wake`（`internal/cli/team_escalation.go:204-227`）→ `hub.Submit` 失败后 `backend.Steer` | ❌（steer 在 **step 开头**消费：`internal/agent/run_loop.go:172`） |
| 用户输入 / steer | `Controller.Steer` → agent steer 队列（`internal/agent/agent.go:640`） | ❌（同上，等 step 边界） |
| 提交新一轮 | `SubmitUserTurnOrError` → `turnDroppedRunning`（`internal/control/admission_guard.go:158-165`） | ❌（**不排队**） |

「等 step 边界」在**前台工具占满 step 时等于等满整个工具时长**。
`bash` 没有自动转后台（`run_in_background` 是显式参数，`internal/tool/builtin/bash.go:100`），
所以 `sleep 50` 就是 50 秒的盲区。

### 3.2 真正能打断的只有 `Cancel()`，而没有任何成员侧路径会调它

`Controller.Cancel()` 会终止当前 turn —— 但那会把 leader 的工作**丢掉**，
不是「带着事件继续」。所以「打断」的正确语义不是取消，而是**让等待先返回**。

---

## 4. 方案

### 4.1 形态：一个 leader-only 的等待工具，等待发生在**工具内部**

新增 `leader_wait`（名称待定，见 §4.5），由 `newLeaderTaskTools` 注册，只有 leader 后端拿到。
它**阻塞在自己的 `Execute` 里**，直到下列之一发生，然后**把原因作为工具结果返回**：

- 成员回报 / 取消 / 派发失败（`wakeAll` 的三类原因）；
- 面向 leader 的权限升级请求入队；
- 上下文窗口收到输入（用户输入 / steer）;
- 超时上限到达。

关键收益：**返回时原因已经在那条工具结果里**，leader 下一个 step 的模型采样直接看到它
—— 不需要额外一轮 round-trip 去「查状态」。这同时消掉了 §1 的两种坏形态。

### 4.2 中断机制：事件驱动（无轮询臂）——**落地形状（2026-09-22）**

最终实现比草案更简单，因为 §8.3 的「唯一 dispatcher」把轮询搬到了别处：

```
sub := sig.Subscribe(team)     // 订阅；总线把「订阅前发生」的事件预置进 channel
select {
case ev := <-sub.C():          // 进程内信号：零延迟
    return batch(ev, drainRest(sub))
case <-ctx.Done():             // Esc / Ctrl+C 或超时：立刻返回，不等任何下一边界
    return cancelled / timeout
}
```

- **主通道（进程内信号总线）**：`waitBus`（`team_wake_signal.go`）。生产者调 `Signal(WaitEvent)`；
  它边沿触发、非阻塞、不读盘。**没有匹配的活订阅时事件被 retain**（`waitPendingLimit=32`，丢最旧），
  `Subscribe(team)` 时把保留窗口交给新订阅——这正是「事件在等待开始之前发生」不丢的机制。
- **兜底（board 游标）**：不再是等待侧的事。`teamWakeDispatcher` 是 leader 游标的**唯一 owner**：
  它读一次 board、推进一次游标，再把结果 `publish` 进总线，TUI 提示与 `leader_wait` 由同一批事件服务。
  因此等待侧**没有任何轮询臂**，也不读写 cursor。
- **顺序正确性**：草案要求「先 drain 后 select」以覆盖 check-then-wait 竞态。落地后这个竞态由
  总线自身的 retain/订阅语义覆盖，比靠调用顺序更稳：订阅是一个原子动作，
  订阅前发生的事件在 channel 里，订阅后发生的事件投递给活订阅，两个窗口都不丢。
- **保留窗口不被消费**（收尾时修正）：无订阅期间 retain 的事件交给**每个**该 team 的新订阅，
  而不是被第一个订阅取走——否则第二个消费者（日后的 TUI）会被先到者饿死。代价是同一个等待方
  下次调用会再看到那批事件，因此等待侧用**跨调用单调的 `afterSeq`** 丢弃已报告的部分。
- **去重**：由总线负责（`waitRecentWindow=5m`，键为 team+summary）。等待侧**不按 ID 去重**——
  `WaitEvent.ID` 是主语 id（task/request id），同一 task 先 report 后 cancel 会复用同一个 id，
  按 ID 去重会吃掉真实事件。同一订阅内 `Seq` 单调递增，可直接用于排序与审计。


### 4.3 直接回答：`sleep 60` 拆成 60 次 `sleep 1` 可行吗？

**分两种理解，结论不同：**

| 理解 | 结论 |
| --- | --- |
| **在 bash 层拆**（模型连续调 60 次 `bash sleep 1`） | **严格更差，不要做。** 每次都是一轮完整 provider round-trip（整个前缀重发），`LEADER_CONTEXT_TOKEN_ROUTE.md` 实测的 45 次查询 / 1,358 秒就是这一类的代价；而且它有 60 次机会被 `applyRepeatReminders` 打上重复提醒。 |
| **在工具内部拆**（一次 `leader_wait`，内部 1s 一片） | **可行，且就是 §4.2 兜底臂的形状。** 但它只是兜底：代价是①每次唤醒最多多等一个切片（延迟）；②对「无事件」的睡眠白白唤醒 60 次/分。 |

**因此不必二选一**：§4.2 的循环本身就是「切片 + 每片检查」，切片检查里既有「总线有没有信号」
也有「board 有没有新事件」。用户提议的切片方案就是这个循环的 `<-ticker.C` 臂。

**建议**：总线为主（零延迟），切片为兜底，**切片间隔取 1s**（与 roster tick 同量级，
且 `teamBoardTimeout = 2s` 的 board 读上限不会与之打架）。若首版要更简单，
**只做切片臂**也成立（功能等价、延迟 ≤1s），可作为 Part 1 的第一步落地，Part 2 再接总线。

### 4.4 必须处理的交互（这是本方案真正的工作量）

| # | 机制 | 事实 | 设计要求 |
| --- | --- | --- | --- |
| I1 | **工作区写租约** | 写工具按 `plan.effects.WorkspaceMutation` 取租约（`internal/agent/tool_write_coordination.go:22-31`） | `leader_wait` **不得**取租约（它不改工作区）；即不得被分类为 workspace mutation |
| I2 | **并行批** | 只有 `CallClass{Known, ReadOnly, ParallelSafe}` 全真的连续调用才并行（`internal/agent/execute_batch.go:287-307,336-337`） | 等待**绝不能**与其他工具并行 → **不要**实现 `BatchClassifier`，让它落进单调用串行批 |
| I3 | **TUI 看门狗** | `tuiWatchdogStall = 10s`；活动 turn 期间由 elapsed tick 每秒喂 `noteWatchdogHeartbeat("elapsed_tick")`（`internal/cli/chat_tui.go:1589`） | 长等待**本身安全**，但仅当 elapsed tick 链持续运行；实现不得让 liveness 依赖 agent 事件 |
| I4 | **重复工具提醒** | 同一工具同参连续调用第 3/5/8 次会追加 `[repeat reminder]`（`internal/agent/repeat_reminder.go:14-31`） | 决定：接受 / 让参数自然变化（带上 `since` 游标）/ 显式豁免。**须拍板** |
| I5 | **预算与收尾** | 每轮前检查 `task.budget.exceeded` 与 `max_steps`（`internal/agent/run_loop.go:421-432`） | 等待消耗墙钟但**不是**「重复失败」；no-progress 阶梯不得把一次正常等待读成卡死。**须拍板**：等待是否计入 task budget |
| I6 | **取消** | Esc / Ctrl+C → `ctrl.Cancel()` → ctx 取消 | 等待必须 `select` 在 `ctx.Done()` 上，**立刻**返回，不等下一片 |
| I7 | **前缀稳定性** | REASONIX.md：provider 可见的 system prefix 与工具 schema 必须逐轮字节稳定 | 新增工具是**一次**前缀变更（`SystemHash`/golden 会动），须在落地时记录并正规更新 golden |
| I8 | **工具分类** | 未知工具经 `evidence.ClassifyToolCall` 得出 `Known=false` | 新工具无 profile → 不取租约（符合 I1）；但要确认权限层对未知工具的姿态 |

### 4.5 拍板结论（2026-09-22，用户裁定）

1. **工具名**：`leader_wait`。
2. **I4 重复提醒**：**显式豁免**。`internal/agent/repeat_reminder.go` 加豁免名单，
   `leader_wait` 不再产生 `[repeat reminder]`。这是本轮唯一一次触碰 `internal/agent/**`
   （§5.4-3 要求先回报；用户在本节点直接裁定，故视为已授权）。豁免只抑制提示文本，
   连续计数照常推进，因此一次插入的等待仍会打断其后工具的真实连续段。
3. **I5 等待是否计入 task budget**：**计入 wall 轴**，不做特殊记账，不改 `internal/agent`。
   `TaskBudget` 三个轴默认全关（`run_budget.go:16-17`），普通 chat 无影响；只有显式配了
   wall 预算的无人值守循环才会感知到等待。
4. **默认超时 / 上限**：默认 **120s**，上限 **600s**（兼容旧 `leader_sleep(max_seconds=600)`）。
   宿主侧与 schema（`minimum 1 / maximum 600`）双重校验。
5. **首版不做「仅切片」分支**：见 §4.2 的修订——§8.3 的唯一 dispatcher 模型把轮询从等待侧
   移到了 dispatcher，等待侧因此没有轮询臂。

---

## 5. 拆分为两段（文件互斥，可并行）

拆分依据同 `TEAM_MEMBER_PARALLELISM_ROUTE.md`：**零文件重叠**。
本文档的两段是**消费侧 / 生产侧**，因此先冻结一个接口，两侧各自对它编程。

### 5.1 冻结接口（两侧共同遵守，**落地形状**）

```go
// internal/cli/team_wake_signal.go —— 由 Part 2 owner 落地，Part 1 只消费
//
// Signal 报告「leader 关心的事件发生了」。边沿触发、非阻塞、不读盘。没有活订阅时事件被
// retain（有界），Subscribe(team) 时交给新订阅，因此「订阅前发生」不会静默丢弃。
type WaitEvent struct{ Kind, Team, ID, Summary string; Seq uint64 }

type WaitSubscription interface {
	C() <-chan WaitEvent
	Close()
}

type WaitSignal interface {
	Signal(WaitEvent)
	Subscribe(team string) WaitSubscription
}
// internal/cli/team_leader_wait.go —— 由 Part 1 落地
//
// awaitLeaderWait 阻塞到有事件、ctx 取消或 ctx 截止；返回整批原因（只含 Seq > afterSeq 的部分）。
// 截止 = 普通结果（带 timeout 事件、err 为 nil）；只有取消才返回 error。
func awaitLeaderWait(ctx context.Context, sig WaitSignal, team string, afterSeq uint64) ([]WaitEvent, error)
```

与原草案的差异（两条签名改动 + 两条语义收紧，两侧已互相确认，符合 §5.4-2 的「改签名须双方同意」）：

1. **`Subscribe` 带 `team` 参数**：总线是 registry 级的（跨 team），订阅方必须声明要哪个 team 的事件。
2. **`drain func() []string` 参数被删除**：§8.3 的唯一 dispatcher 把 board 读取收归自己，
   等待方只订阅。等待侧因此**不持有任何 cursor 访问**，比原草案更难写错。

另外三条语义在落地时收紧（同为两侧确认过的形状，等待侧按此编程）：

3. **去重落在总线**：生产者的 `Signal` 与 dispatcher 从 board 读回后的 `publish` 是同一 occurrence 的
   两条路径，总线按 `(team, summary)` 在 5 分钟窗口内只投递一次，因此等待循环**不需要自己按 summary
   去重**；同一订阅内 `Seq` 单调递增，可直接用于排序/审计。
4. **channel 关闭 = 结束信号**：`closeAll()` → `bus.close()` → 所有订阅 channel 关闭。等待方遇到
   closed channel 必须正常返回，不能把零值 `WaitEvent` 当成事件。
5. **retain 不按「先到先得」消费**（Part 1 owner 在收尾时修正了我的初版）：无订阅时 retain 的事件
   会交给**每个**该 team 的新订阅，而不是被第一个订阅取走——否则「TUI 也订阅」的那天，先到者会把
   pending 取走、后到的 `leader_wait` 睡过那批事件。代价是等待方可能在**下一次**调用里再看到同一批
   保留事件，因此 `awaitLeaderWait` 增加 `afterSeq` 参数：调用方传入「我已经报告过的最大 Seq」，
   等待方只返回更新的部分。这是 §5.1 冻结合同的第 3 处签名变化（两侧同意）。

### 5.2 Part 1 —— 等待侧（原语 + 工具）

**范围**：等待本体、leader 工具、注册与分类、取消、以及 I1–I6/I8 的落地。

**实际改动文件**（比原清单多了 `internal/agent/repeat_reminder.go`，理由见 §4.5-2；
收尾时另加两处，理由见各自的「落地结果」条目）：

```
internal/cli/team_leader_wait.go        （新增：等待循环 + leader_wait 工具）
internal/cli/team_leader_wait_test.go   （新增：17 条用例）
internal/cli/team_member_tools.go       （注册工具、schema、分类声明）
internal/cli/team_member_tools_test.go  （leader 工具面清单加 leader_wait）
internal/agent/repeat_reminder.go       （I4 豁免名单，唯一一次越界，已裁定）
internal/agent/repeat_reminder_test.go  （豁免用例）
internal/cli/team_wake_signal.go        （收尾：retain 不再被首个订阅消费，见落地结果 5）
internal/cli/team_escalation_test.go    （收尾：修掉 -race 门禁上的测试内竞态，见落地结果 6）
```

**落地结果**：

1. 等待循环：`awaitLeaderWait(ctx, WaitSignal, team, afterSeq)` —— 订阅、`select` 事件/`ctx.Done()`、
   取整批（只保留 `Seq > afterSeq` 的部分）。**没有轮询臂**（§4.2 修订）。
2. 信号总线：由 Part 2 owner 落在 `internal/cli/team_wake_signal.go`，挂在 `teamBackends`
   （`bus` 字段、`newTeamBackends` 创建、`closeAll` 关闭），与 `memberCockpit` 同一生命周期。
   Part 1 通过 `teamTaskService.signals` 这个迟到绑定缝取用，不重复实现总线。
3. 工具注册在 `newLeaderTaskTools` 的 leader 面上：`ReadOnly()=true`；`PlanModeSafe()=true`，
   **这是对 plan 边界的显式绕过**——等待只观察、不写工作区也不写团队状态，而正在规划它所等待的
   那批工作的 leader 会被这个「拦写者」的边界搁死；机制是分类器不是边界本身：报 Safe 后
   `planmode.Policy.Decide` 返回不阻断（`internal/agent/execute_one.go` 先翻译这份报告再查策略）；
   `TeamLifecycleStateWriter()=false`（等待不写任何团队状态，可达性由 ReadOnly 保证，声明写者会
   描述一个不存在的副作用）；`EffectHint{Known,ReadOnly}`；
   **实现 `BatchClassifier` 返回 `Known+ReadOnly+!ParallelSafe`**（§8.4 已推翻原文「不实现分类器」）；
   `timeout_seconds` 默认 120s、上限 600s，宿主与 schema 双重校验。
4. I6 取消语义与 I8 工具面的验证见下。
5. **retain 改为「每个订阅各自持有」**：`waitBus.retainedForLocked` 把保留窗口交给每个该 team 的
   新订阅，不再被第一个订阅取走（原文的 consume-on-subscribe 会让日后的第二个消费者饿死第一个）。
   代价是同一个等待方在**下一次**调用里会再看到那批保留事件，因此等待侧加了跨调用单调的
   `afterSeq` 高水位：只返回更新的部分，且整批都是重放时**继续等待**而不是返回空结果。
6. **修掉 `-race` 门禁上的既有测试内竞态**：`team_escalation_test.go` 的 fake 后端在 worker goroutine
   里无锁写 `turns`/`resolved`、用例在测试 goroutine 里读同一 slice，`-run` 命中
   `TestEscalationWakeNamesTheQueue` 时报 DATA RACE。改为 `escalationRecorder[T]`（自持锁 +
   `all()` 快照），fake 的 `mu` 随之删除。这是测试自身的竞态，不是被测代码的缺陷。

**验收（均已落地并做过变异验证：去掉对应修复即红）**：

- `ctx` 取消后等待**立刻**返回（`TestLeaderWaitEndsOnCancelPromptly`，且断言未等下一个边界）；
- 超时是**普通结果**不是错误（`TestLeaderWaitReportsTimeoutAsAnOrdinaryResult`）；
- 「信号在等待开始**之前**发生」不丢（`TestLeaderWaitDeliversAnEventThatPrecededIt`）；
- 等待中投递的信号能唤醒、一批事件返回一条结果（`...WakesOnASignalDeliveredWhileWaiting`、
  `...ReturnsABurstAsOneBatch`）；
- 不按 team 串扰（`...IgnoresAnotherTeamsEvents`）；
- `closeAll` 解除等待（`...EndsWhenTheRegistryCloses`）；
- 无总线时仍有界、不会卡死（`...WithoutABusIsBoundedByItsContext`）；
- 等待**不取**工作区写租约（`...NeverTakesAWorkspaceWriteLease`，按
  `plan.effects.WorkspaceMutation` 的分类断言）；
- 等待不会被放进并行批（`...IsNeverBatchedInParallel`）；
- 工具端到端：订阅 → 生产者 `Signal` → 原因出现在工具结果里（`...ToolBlocksUntilTheBusSignals`）；
- 参数校验与 schema 边界（`...ToolReportsTimeoutAndValidatesItsBound`）；
- `leader_wait` 只出现在 leader 面（`...IsLeaderOnly`）；
- 重复提醒豁免（`internal/agent/repeat_reminder_test.go` 的
  `TestRepeatReminderExemptsTheLeadersWait`，并断言连续计数仍推进）；
- **保留窗口对每个订阅可见、不被取走**（`TestWaitBusHeldWindowReachesEveryWaiter`）；
- **同一 leader 不会把保留事件报告两次、且重放不会造成假醒**，新事件仍能唤醒
  （`TestLeaderWaitToolDoesNotRepeatAnEventItAlreadyReported`）；
- **plan 边界被显式绕过**——断言的是 agent 真正走到的那次决策，而不只是那一字的自报
  （`TestLeaderWaitBypassesThePlanBoundary`，走 `planmode.Policy.Decide`）；
- **I3：等待不持有帧路径需要的锁**（`TestLeaderWaitHoldsNoLockTheFramePathNeeds`：等待 park 期间，
  另一个消费者的 `Subscribe`/`Close` 与 teardown 的 `bus.close()` 都必须照常完成——若等待握着
  总线锁 park，帧线程会被拖住并让看门狗升级）。I3 的另一半（长时间等待本身安全）由「不持锁」
  加「elapsed tick 由 TUI 自己的 Update 循环喂」共同保证，**没有端到端看门狗用例**。

**门禁实跑**：`gofmt -l` 干净；`go build ./...` 通过；`go vet ./internal/cli/ ./internal/agent/` 通过；
`go test ./internal/cli/` 全包绿（46.8s）；`go test -race ./internal/cli/
-run 'Escalation|Team|Cockpit|Replay|Roster|LeaderWait|WaitBus|Wake' -count=2` 绿（32.5s，
修掉 #6 之前用同一条含 `Wake` 的模式是红的）；本段改动文件 `golangci-lint` 与 `repolint` 零新增。

**已知阻塞（已修复）**：`internal/cli/chat_tui_team.go` 的 overlay-open drain 一度写成
`p.board.wakes.drain(...)`——字段选择，而 `p.board` 在 `openTeamInbox` 失败时是 nil（旧代码是
`p.board.consumeWakeups(...)`，方法调用有 nil 接收者守卫），于是 panic 并 abort 整个
`internal/cli` 测试二进制。修法：`teamInboxWire.wakeDispatcher()` 这个 nil 安全访问器，两处调用点
（overlay open 与 tick）都改用它（由引入该缺陷的一方修复）。证据：`go test ./internal/cli/
-run TestStepDownLeaderStrictOrder` 转绿；全包 `go test ./internal/cli/` 已能跑完（见门禁）。
`TestTeamTurnInjectsInboxAtSubmit` 全包红 / 隔离 5/5 绿属既有偶发（见
`TEAM_MEMBER_PARALLELISM_ROUTE.md` §5.1），与本段无关。

**不在本段范围、本机环境导致的整包红**（供门禁互认，均与两段改动无关，清掉代理环境变量即绿）：
`internal/netclient`（`TestDirectHostsBypassProxy` / `TestNoDirectHostsKeepsEveryoneProxied`
依赖 `http_proxy=127.0.0.1:7890`）、`internal/plugin`（`TestSSETransportUnsupported` 需要真实网络）、
`internal/worktree`（本机 git 2.34.1 < 2.38，见 `TEAM_MEMBER_PARALLELISM_ROUTE.md` §6.3）。

### 5.3 Part 2 —— 信号侧（生产者 + 兜底）

**范围**：把所有能打断睡眠、且**应当**打断睡眠的生产者接上 `Signal`，
以及 board 游标 drain/poll 的兜底实现与 wakeup 消费缺口。

**独占文件**：

```
internal/team/agentruntime/runtime.go       （wakeAll：回报/取消/派发失败 → Signal）
internal/team/agentruntime/wakeup.go        （若需在 wake 路径上补挂）
internal/cli/team_escalation.go             （升级请求入队 → Signal）
internal/cli/team_task_service.go           （report 路径 / wakeLeader）
internal/cli/chat_tui_team.go               （用户输入 / 会话入口）
internal/cli/chat_tui_team_inbox.go         （consumeWakeups / 游标 drain）
internal/cli/chat_tui_team_session.go       （tick 驱动消费，见下）
```

**任务**：
1. `runtime.wakeAll` 在写 board 之外补一次 `Signal`（**不改变** board 写的既有语义，
   它仍是持久真相）。
2. 升级路径 `writeAccessEscalations.wake` 入队后 `Signal`。
3. 用户输入 / steer 到达时 `Signal`（让「上下文窗口有输入」也能打断睡眠）。
4. 实现 `drain()`：按 leader 游标读 board 的 `EventWakeup`（复用
   `teamInboxWire.consumeWakeups` 的读法与游标推进语义）。
5. **顺带修一个已确认缺口**：`consumeWakeups` 目前**只在打开 [TEAM] overlay 时调用一次**
   （`internal/cli/chat_tui_team.go:200`，`onTeamButtonClick` 内），
   所以「开着 overlay 时到达的回报不会有提示，要重开才看得到」。
   接上 tick 驱动消费（走已有的 `teamRosterRefreshMsg`，**不新增 Update 分支**）。

**验收**：
- 一条用例证明成员 `Complete` 后，等待方 `drain()` 能拿到该原因（游标推进且只读一次）；
- 一条用例证明升级入队后等待方被唤醒；
- 一条用例证明「保存的两条」在无总线（纯切片）路径下同样能唤醒（兜底不依赖总线）；
- 一条用例证明 tick 也消费 wakeup（覆盖 §3.1 的缺口）。

**实际改动文件**（比原清单多一个 `team_wake_dispatcher.go`、一个 `team_task_signals.go`，
以及两处越界，理由见下）：

```
internal/cli/team_wake_signal.go            （新增：WaitEvent/WaitSubscription/WaitSignal/waitBus）
internal/cli/team_wake_dispatcher.go        （新增：唯一 cursor owner）
internal/cli/team_wake_signal_test.go       （新增：总线 7 条用例）
internal/cli/team_wake_dispatcher_test.go   （新增：dispatcher 5 条 + tick 1 条）
internal/cli/team_wake_producers_test.go    （新增：生产者 4 条）
internal/cli/team_wake_registry_test.go     （新增：registry 两条迟到绑定缝 2 条）
internal/cli/team_task_signals.go           （新增：service 侧的 Signal 缝；从 team_task_service.go
                                              拆出，因为该文件已到 800 行上限）
internal/team/agentruntime/attention.go     （新增：AttentionFunc/AddAttention/notifyAttention）
internal/team/agentruntime/attention_test.go（新增：慢 board 下的先后顺序 + 三类 reason）
internal/team/agentruntime/runtime.go       （Complete/Cancel/CancelTask/failDispatch 各补一次
                                              notifyAttention，均在状态落盘之后、board 写之前）
internal/cli/team_backends.go               （bus 字段、创建、closeAll 关闭、setInbox/setTasks 接线）
internal/cli/team_task_service.go           （字段 + AddAttention 注册 + forTeam 继承；见上）
internal/cli/team_escalation.go             （begin 入队后 Signal）
internal/cli/chat_tui_team.go               （overlay open 走 dispatcher + composer 输入 Signal）
internal/cli/chat_tui_team_inbox.go         （wakes/attachSignals/wakeDispatcher 缝）
internal/cli/chat_tui_team_session.go       （tick 驱动 drain + teamRosterRefreshMsg.wake 载荷）
internal/cli/chat_tui.go                    （两处 composer 入队点各一行 signalTeamInput 调用）
```

**落地结果**：

1. **总线**（`team_wake_signal.go`）：`Signal` 非阻塞、不读盘、不取锁等待订阅者；没有活订阅时事件
   **retain**（有界 32 条，新的进、旧的丢），`Subscribe(team)` 取走该 team 的 retain 作为初始批次。
   同一 occurrence 的「生产者 Signal」与「dispatcher 从 board 读回后 publish」按 `(team, summary)`
   在 5 分钟窗口内**只投递一次**（`why`：summary 文本与持久 wakeup 行逐字节相同，是本方案唯一的
   去重键；等待期间同一任务不可能合法地以同一 summary 再次到达——leader 正阻塞在等待里，无法
   retry）。`close()` 关闭所有订阅 channel = 结束信号。
2. **dispatcher**（`team_wake_dispatcher.go`）：`drain(team, leader)` 在**自己的锁**下调用现有的
   `teamInboxWire.consumeWakeups`（读一次、推进一次游标），把批次 publish 进总线，并把批次
   返回给窗口做 notice。消费者（tick、overlay open）**不再直接碰 cursor**。
3. **生产者**：
   - 任务状态移动：`Runtime` 新增 `AttentionFunc` 钩子，在 `SaveTask` 之后、board 写**之前**触发
     （`attention.go`）；`teamTaskService.attention` 把它转成 `WaitEvent`（Kind 用 agentruntime 的
     `AttentionReport`/`AttentionCancel`/`AttentionDispatchFailed`，ID = task id）。
     **注意这不是把 Signal 塞进 `wakeAll`**：`wakeAll` 在 `record` 之后，慢 board 会把进程内唤醒拖住，
     违反 §8.5，所以钩子挂在更早的持久化点。
   - 升级请求：`writeAccessEscalations.begin` 入队后 `Signal`（Kind `escalation`，ID = request id）。
   - composer 输入：`chatTUI.signalTeamInput` 在 steer/follow-up 入队后 `Signal`（Kind `input`），
     仅当绑定的成员**就是该队 leader** 且会话处于活动态（其他成员/无会话一律不发）。
4. **tick 消费**：`refreshTeamRoster` 在既有 `teamRosterRefreshMsg` 载荷里新增 `wake` 字段，
   读盘走 `drainTeamWakeups()` 这个 `tea.Cmd`（离开帧线程），结果回到 `refreshTeamRoster` 顶部
   应用 notice——**未新增 Update 分支**，也未削弱「只有真 tick 才续期」的规则。
5. **生命周期**：bus 挂在 `teamBackends`（`newTeamBackends` 创建、`closeAll` 关闭），
   `setInbox` 把它接到 board 的 dispatcher，`setTasks` 把它交给 task service 并随 `forTeam`
   继承给 per-team 子服务。

**验收（均已落地并做过变异验证：去掉对应实现即红）**：

| 门禁（§5.3 / §8.7） | 用例 | 变异验证 |
| --- | --- | --- |
| 回报后等待方能拿到原因，游标推进且只读一次 | `TestWakeDispatcherAdvancesTheLeaderCursorOnce` | — |
| 升级入队后等待方被唤醒 | `TestEscalationRequestWakesAWaitingLeader` | 去掉 `begin` 里的 Signal → 红 |
| 无总线（无生产者信号）也能靠 board 兜底唤醒 | `TestWakeDispatcherWakesOnTheDurableRowAlone` | — |
| tick 也消费 wakeup（§3.1 缺口） | `TestTeamTickConsumesWakeupsWhileTheOverlayIsOpen` | 去掉 tick 里的 drain 武装 → 红 |
| TUI 与等待方共用一个 cursor，只推进一次 | `TestWakeDispatcherServesEveryConsumerFromOneAdvance` | 去掉 dispatcher 的锁 → 4 并发全部返回同一事件（红） |
| 订阅前发生的事件不丢 | `TestWaitBusHoldsAnEventThatPrecededTheSubscription` | 去掉 retain → 红 |
| 同一 occurrence 不重复投递 | `TestWaitBusDeliversOneOccurrenceOnce` | 去掉 publish 的 dedupe → 红 |
| 事件按稳定 Seq 有序、可审计 | `TestWaitBusKeepsTheArrivalOrder` | — |
| `closeAll()` 解除等待 | `TestWakeRegistryCloseReleasesWaiters` | 去掉 `closeAll` 里的 `bus.close()` → 红 |
| 慢 board 不能拖延进程内唤醒 | `TestRuntimeReportsCompletionWhileTheBoardWriteIsInFlight` | 去掉 `notifyAttention` → 红 |
| 三类 reason（report/cancel/dispatch_failed） | `TestRuntimeReportsCancelAndRefusedDispatch` | 同上 → 红 |
| composer 输入能唤醒（且非 leader/无会话不发） | `TestComposerInputWakesAWaitingLeader` + 两条反向用例 | 去掉 `signalTeamInput` 的 Signal → 红 |
| registry 把自己的 bus 交给 service（含 per-team 子服务） | `TestRegistryHandsItsBusToTheTaskService` | 去掉 `setTasks` 的 `setSignals` → 红 |
| registry 把自己的 bus 交给 board 的 dispatcher | `TestRegistryHandsItsBusToTheBoardDispatcher` | — |

**验收范围的诚实说明**：`chat_tui.go` 里两个 `signalTeamInput` **调用点**（steer 被接受、follow-up 入队）
没有用「按键驱动 + running turn」的端到端用例覆盖——覆盖的是被调用的那个函数（含 team/leader 判定与
事件内容）。调用点本身是两行直插，靠编译与 `TestTeamTurnInjectsInboxAtSubmit` 一类的既有提交路径用例
间接保证；若要更硬的证据，需要另造一个「绑定 leader + 处于 running 态」的窗口 fixture。

**门禁实跑（2026-09-22，本机，含 Part 1 合并后的工作区）**：

```
gofmt -l <改动文件>                                    # 空
go build ./...                                         # 通过
go vet ./internal/cli/ ./internal/team/... ./internal/boot/   # 空
go test ./internal/team/...                            # 全绿
go test ./internal/cli/                                # 全绿（76.1s，含 Part 1 的全部用例，两段合并后的树）
go test ./internal/boot/ ./internal/agent/             # 全绿
go test -race ./internal/team/agentruntime/ -count=2   # 绿
go test -race ./internal/cli/ -run 'TestWaitBus|TestWake|TestComposerInput|TestEscalationRequestWakes|TestTeamTickConsumes|TestRegistryHandsItsBus' -count=2   # 绿
go test -race ./internal/cli/ -run 'Escalation|Team|Cockpit|Replay|Roster|LeaderWait|WaitBus|Wake' -count=2   # 绿
go test ./internal/cli/ (收尾复跑，含 Part 1 的 retain/afterSeq 改动)   # ok 59.2s
repolint                                               # 本段改动文件零新增
golangci-lint run ./internal/cli/...                   # 本段改动文件零新增
```

**已收口的既有问题**：`TestEscalationWakeNamesTheQueue` 曾在 `-race` 下报写/读竞态——竞态双方都在
该测试自己的 fake 与用例体内（`team_escalation_test.go` 的 `escalationBackend` 在 worker goroutine
里无锁写 `turns`/`resolved`），与本轮改动文件无关，`TEAM_MEMBER_PARALLELISM_ROUTE.md` §6.3 在本轮
之前就记为既有测试内竞态。Part 1 owner 在收尾时直接修掉了（`escalationRecorder[T]`，自持锁 + 快照），
因此把 `Wake` 加进 `-run` 模式的扩展跑法现在也是绿的（`-count=2` 实测）。

**风险与残留**：

1. retain 的语义已在收尾时改为「交给每个该 team 的新订阅」（§5.1-5），代价是等待方可能重复看到保留
   窗口内的事件，由 `afterSeq` 在等待侧丢弃。`waitPendingLimit = 32` 是保留窗口的上界：更早的
   occurrence 只能靠 TUI notice（board 游标已推进，不再重放）。
2. de-dup 键是 `(team, summary)`：同一任务在同一等待窗口内不可能合法地重复到达（leader 阻塞中无法
   retry），但跨窗口不保留记忆——重复投递只可能造成一次多余唤醒，不会丢事件。
3. 等待方与 TUI 共享一个 cursor 的所有权已由 dispatcher 保证，但**TUI 目前不订阅总线**（它只用
   drain 的返回值做 notice）。若日后 TUI 也订阅，需注意它以 `waitEventKey` 之外的粒度消费事件。

### 5.4 两段并行时的硬约束

1. 只编辑自己那份独占文件清单；需要动对方的文件时**停手并在回报里写明**。
2. 冻结接口（§5.1）改名或改签名前必须双方同意——它是唯一的耦合面。
3. 冻结文件（本轮谁都不改）：`internal/control/**`、`internal/agent/**`、`internal/provider/**`、
   `internal/cli/team_event_pump.go`、`internal/cli/team_member_cockpit.go`、
   `internal/tool/**`。若要动 `internal/agent` 才能落地 I4/I5，**先回到 §4.5 拍板**。
4. **与在飞工作的序关系**：本方案的 Part 2 与 `TEAM_MEMBER_PARALLELISM_ROUTE.md` 的 Part B
   在 `internal/team/agentruntime/runtime.go`、`team_task_service.go` 上重叠。
   建议排序：**先合并那条线，再开本方案**；否则两个 Agent 会在同一文件上互相覆盖。

---

## 6. 节点状态

| 节点 | 主题 | 所属 | 状态 | 证据 |
| --- | --- | --- | --- | --- |
| W0 | §4.5 四项拍板 | — | **已完成** | §4.5 的裁定结论（2026-09-22） |
| W1 | 等待循环（订阅、ctx 取消立即返回、整批返回、afterSeq 过滤与重放不假醒） | Part 1 | **已完成** | `internal/cli/team_leader_wait.go` 的 `awaitLeaderWait`/`afterWaitSeq`；`team_leader_wait_test.go` 的取消/超时/订阅前事件/整批/跨 team/无总线/不重报 7 条用例（去掉对应实现即红，已验） |
| W2 | 进程内信号总线（挂 `teamBackends`，含订阅前 retain、跨订阅可见） | Part 1 原定，**由 Part 2 owner 落地** | **已完成** | `internal/cli/team_wake_signal.go` 的 `waitBus`/`retainLocked`/`retainedForLocked`（retain 语义见 §5.1-5：保留窗口交给每个新订阅、不被首个订阅取走）；`team_backends.go` 的 `bus` 字段、`newTeamBackends` 创建、`closeAll` 关闭；收敛用例 `team_leader_wait_test.go` 的 `TestWaitBusHeldWindowReachesEveryWaiter`（改回「取走」即红，已验） |
| W3 | `leader_wait` 工具注册与分类（I1/I2/I8 + §8.4 分类器） | Part 1 | **已完成** | `team_member_tools.go` 的 `newLeaderWaitTool` + `newLeaderTaskTools` 末位注册；`team_leader_wait.go` 的 `ReadOnly`/`PlanModeSafe`/`TeamLifecycleStateWriter`/`EffectHint`/`ClassifyCall`；租约、并行批、leader-only、参数边界、**plan 边界显式绕过**（`TestLeaderWaitBypassesThePlanBoundary`，走 `planmode.Policy.Decide`）、**I3 不持帧路径的锁**（`TestLeaderWaitHoldsNoLockTheFramePathNeeds`）共 6 条用例（变异验证已验） |
| W4 | `wakeAll` / 升级 / 用户输入接上 `Signal` | Part 2 | **已完成** | 任务状态移动走 `agentruntime.AttentionFunc`（`attention.go` + `runtime.go` 的 `notifyAttention`，挂在 `SaveTask` 之后、board 写之前，**不是** `wakeAll`——见 §5.3 落地结果 3）；`team_task_signals.go` 的 `attention`；`team_escalation.go` 的 `begin`；`chat_tui_team.go` 的 `signalTeamInput`（+ `chat_tui.go` 两个入队点）。用例：`attention_test.go` 的慢 board 顺序用例与三类 reason 用例、`team_wake_producers_test.go` 的 4 条；变异验证见 §5.3 表 |
| W5 | board 游标 `drain()` 兜底（唯一 dispatcher） | Part 2 | **已完成** | `internal/cli/team_wake_dispatcher.go`（`drain` 在自有锁下调用 `consumeWakeups`，读一次/推进一次，publish 进总线并把批次交给窗口）；`chat_tui_team_inbox.go` 的 `wakes`/`attachSignals`/`wakeDispatcher`。用例：`team_wake_dispatcher_test.go` 的 5 条（含 4 并发只推进一次、无生产者信号的 board 兜底、逐 leader 游标） |
| W6 | tick 消费 wakeup（§3.1 缺口） | Part 2 | **已完成** | `chat_tui_team_session.go` 的 `drainTeamWakeups`（`tea.Cmd`，离开帧线程）+ `teamRosterRefreshMsg.wake` 载荷；`chat_tui_team.go` 的 overlay-open drain 改走 dispatcher。用例：`TestTeamTickConsumesWakeupsWhileTheOverlayIsOpen`（去掉 tick 武装即红，已验）。**原缺陷 D1 已修复**：`p.board` 为 nil 时的字段选择 panic 改由 `wakeDispatcher()` 的 nil 安全访问器承担，`TestStepDownLeaderStrictOrder` 已由 panic 转绿 |

状态取值：`未开始` / `进行中` / `待验收` / `已完成` / `阻塞`。**阻塞必须写真实原因，不得改写为通过。**
Part 1（W1/W3）的用例与门禁、以及 W2 的总线落地，见 §5.2/§5.3；Part 2（W4/W5/W6）的用例、
变异验证与门禁实跑见 §5.3。两侧的用例都做过「去掉修复即红」的变异验证，`repolint` 相对改动前零新增。


---

## 7. 明确不做

| 项 | 为什么不做 |
| --- | --- |
| **在 bash 层切片**（模型连调 60 次 `sleep 1`） | §4.3：60 轮完整 round-trip，严格劣于现状。 |
| **让成员回报去 `Cancel()` leader 的 turn** | 语义错误：那是丢弃 leader 的工作，不是带着事件继续。 |
| **把 wakeup 做成「自动开新一轮」** | 与本文档正交。本文档解决的是「leader **正在等**时能否被叫醒」；「leader 没在等时要不要自动开工」是产品决策，且受前台长工具限制（见 `TEAM_MEMBER_PARALLELISM_ROUTE.md` §4.4）。 |
| **移植 `leader_sleep` 的 tmux 字段** | `TOOL_MAPPING.md:70` 的废弃判断本身是对的（运行态字段不移植）；错的是删了能力没补等价物。 |

---

## 附录 A：证据索引

### A.1 缺口

| 结论 | 位置 |
| --- | --- |
| `leader_sleep` 被废弃且未替代 | `docs/team-mcp-port/TOOL_MAPPING.md:70`；`AGENT_OPTIMIZATION_TECHNICAL_ROUTE.md:57` |
| leader 工具集里没有等待工具 | `internal/cli/team_member_tools.go:68-88` |
| 状态读被 60s 节流 | `internal/cli/team_status_poll.go:19` |
| 轮询 token 成本的实测 | `docs/team-mcp-port/LEADER_CONTEXT_TOKEN_ROUTE.md`（45 次查询 / 1,358 秒） |

### A.2 睡眠期间事件到不了 leader

| 结论 | 位置 |
| --- | --- |
| 回报 → board wakeup 事件（不提交 turn） | `internal/team/agentruntime/runtime.go:418`（`wakeAll`）、`internal/cli/team_task_service.go:72`（`wakeLeader`） |
| 升级尝试提交，失败后回退 steer | `internal/cli/team_escalation.go:204-227` |
| steer 在 step 开头消费 | `internal/agent/run_loop.go:172`、`internal/agent/agent.go:640-652` |
| 运行中提交被拒且不排队 | `internal/control/admission_guard.go:158-165` |
| bash 不自动转后台 | `internal/tool/builtin/bash.go:100`（`run_in_background` 显式） |

### A.3 方案必须处理的机制

| 结论 | 位置 |
| --- | --- |
| 写租约按 `WorkspaceMutation` 取 | `internal/agent/tool_write_coordination.go:22-31` |
| 并行批只收 `Known+ReadOnly+ParallelSafe` | `internal/agent/execute_batch.go:287-307,336-337` |
| 分类接口 | `internal/tool/tool.go:52-58,74-76`、`internal/tool/effect_hint.go:7-21` |
| 看门狗 10s 停顿 + elapsed tick 喂心跳 | `internal/cli/tui_diagnostics.go:22-23`、`internal/cli/chat_tui.go:1589` |
| 重复工具提醒（3/5/8 次） | `internal/agent/repeat_reminder.go:14-31`（`applyRepeatReminders`） |
| 预算/步数检查在轮前 | `internal/agent/run_loop.go:421-432` |
| 前缀必须字节稳定 | `REASONIX.md`（provider-visible prefix / tool schemas） |
| 总线生命周期应比照 cockpit（挂注册表） | `internal/cli/team_member_cockpit.go:13-26` |
| wakeup 只在开 overlay 时消费 | `internal/cli/chat_tui_team.go:200`、`internal/cli/chat_tui_team_inbox.go:301` |

---

## 8. 方案合理性复核与对齐修订（2026-09-22）

### 8.1 总体结论

本文档识别的问题成立：`leader_sleep` 被移除后没有补回等待能力，前台 `bash sleep` 会占满当前 agent step，状态轮询又会产生额外 provider round-trip。因此，**增加一个 leader 专用、可被事件唤醒、带超时和取消语义的等待原语是合理方向**。

但原方案的“消费侧 / 生产侧”拆分还不能直接施工。当前文本级设计有几个会影响正确性的缺口：

1. 等待总线接口只声明了 `Signal`，伪代码却调用 `C()`，接口无法实际实现。
2. `leader_wait` 若 `ReadOnly() == true` 且不实现 `BatchClassifier`，执行器会回退到 `target.ReadOnly()`，仍可能把它放入并行批；“不实现分类器即可串行”的结论不成立。
3. 等待方和 TUI 若分别调用 `consumeWakeups`，会共同推进同一个 leader cursor，出现 TUI 先消费、等待方读不到事件的竞态。
4. 进程内边沿信号只对“已有订阅者”可靠；成员回报之外的用户输入、steer、权限升级没有 board 持久副本，不能简单声称“总线丢失也不丢事件”。
5. Part 1 的 `team_member_tools.go` 与 `TEAM_MEMBER_PARALLELISM_ROUTE.md` 的 Part B 重叠；Part 2 的 `runtime.go`、`wakeup.go`、`team_task_service.go` 和多个 TUI 文件也重叠。因此当前两段不能和并行路线同时独立施工。

修订后的判断是：**等待原语值得实施，但必须先收紧事件所有权、批处理分类和生命周期边界；在现有并行路线合并前，不应启动本路线的 Part 2。**

### 8.2 等待总线接口必须与伪代码一致

§5.1 的接口需要提供订阅和取消订阅能力，不能只有 `Signal`。同时，单一共享 channel 会让多个消费者互相抢事件，建议使用“每个等待者一个订阅、总线保留待处理状态”的形状：

```go
type WaitEvent struct {
	Kind    string // report, cancel, dispatch_failed, escalation, steer, timeout
	ID      string // task/event id when durable; empty for a pure UI signal
	Summary string
	Seq     uint64
}

type WaitSubscription interface {
	C() <-chan WaitEvent
	Close()
}

type WaitSignal interface {
	Signal(WaitEvent)
	Subscribe() WaitSubscription
}
```

`Signal` 必须是非阻塞的，不能在成员完成路径上读盘、拿 workspace lease 或等待 board 写入。总线至少要做到：

- 订阅前到达的信号不会被静默丢弃，或者明确由持久 cursor 覆盖；
- 同一事件不会因为多个订阅者而重复推进同一个 cursor；
- 关闭 `teamBackends` 时关闭所有订阅，等待方收到结束信号而不是永久阻塞；
- 事件带类型和稳定 ID，工具结果不要依赖不可审计的自由文本匹配。

等待循环的顺序应改为：

1. 建立订阅；
2. 读取一次权威的 pending/durable 快照；
3. 再进入 `select`；
4. 收到总线事件后再次读取快照并合并去重。

这样才能覆盖 check-then-wait 窗口。仅写成“先 drain 再 select”仍不足以解决“信号在 drain 与 select 之间到达”的问题。

### 8.3 board wakeup 必须只有一个 cursor owner

当前 `teamInboxWire.consumeWakeups` 会读取并推进 leader 的 board cursor。`leader_wait` 不能再直接调用同一个函数，否则会出现以下竞态：

```text
TUI tick:       consumeWakeups -> AdvanceCursor
leader_wait:    consumeWakeups -> 读到空，等待超时
```

推荐的所有权模型是：

- `teamInboxWire` 或新的 `teamWakeDispatcher` 作为**唯一 cursor owner**；
- 它负责从 board 读取一次、推进一次 cursor，再向 TUI 通知和 `leader_wait` 总线广播；
- `leader_wait` 只订阅 dispatcher，不直接读写 board cursor；
- TUI 的提示和等待工具看到的是同一批已去重事件。

如果确实需要两个独立消费者，则必须使用不同的 consumer ID、独立 cursor 和明确的保留/清理策略；不能让两个路径共享当前 leader cursor。

这也意味着 §5.3 的“Part 2 实现 `drain()`”应改成“实现唯一 dispatcher 的 drain，并提供订阅结果”，而不是让等待工具和 tick 各自 drain。

### 8.4 `leader_wait` 的工具分类需要反转原文结论

`internal/agent/execute_batch.go` 的实际规则是：没有 `BatchClassifier` 时，执行器会直接使用 `target.ReadOnly()` 判断是否可并行。因此以下组合**不能**保证串行：

```text
ReadOnly() == true
不实现 BatchClassifier
```

首版应采用：

- `ReadOnly() == true`：等待不修改 workspace，也不应取写租约；
- `PlanModeSafe()` 是否为 true 需单独拍板，不能从 ReadOnly 自动推导；
- 实现 `BatchClassifier`，返回 `Known=true, ReadOnly=true, ParallelSafe=false`；
- 增加回归测试，证明 `leader_wait` 与相邻只读工具不会被合并到 parallel batch。

这样既保留“不取 workspace 写租约”的语义，又明确阻止等待和其他工具并行。原文 §4.4 I2、§5.2 任务 3 中“不要实现 `BatchClassifier`”应按此修订。

### 8.5 唤醒时序、超时和取消语义

成员完成、取消或派发失败时，应在任务状态已经成功持久化后尽快 `Signal`，再进行可能阻塞的 board 追加；不能等 board 写完才发进程内信号，否则 board 的 5 秒等待会重新把“可中断等待”拖成不可见延迟。board 仍是跨进程的持久真相，但不应成为本进程低延迟唤醒的前置条件。

工具结果需要区分三种结束方式：

| 结束原因 | 工具结果语义 |
| --- | --- |
| 成员/升级/steer 事件 | 返回结构化事件列表，正常结束当前工具调用 |
| 超时 | 返回 `timeout` 及已观察到的事件，正常结束工具调用 |
| `ctx.Done()` / Esc / Ctrl+C | 遵循 agent 取消语义；不要把取消伪装成成员完成事件 |

等待时长必须有默认值、最大值和参数校验。建议兼容旧流程的 600 秒上限，但首版应提供较短默认值，并在结果中返回实际等待原因，避免模型无界占用一个 turn。

### 8.6 生命周期和并行施工边界修订

当前 Part 1/Part 2 的文件清单需要增加以下明确约束：

| 重叠区域 | 处理方式 |
| --- | --- |
| `internal/cli/team_member_tools.go` 与并行路线 Part B | 先完成并行路线 B5 的工具注册改动，再接入 `leader_wait`；或由同一 owner 顺序处理，不能两个 Agent 同时编辑 |
| `internal/team/agentruntime/runtime.go`、`wakeup.go`、`team_task_service.go` | 等并行路线 B1/B2 合并并稳定后，再接入 Signal；不得通过 cherry-pick 强行覆盖 |
| `team_backends` 生命周期 | 文档要求总线挂在 `teamBackends`，但 Part 1 清单未包含 `team_backends.go`。必须把字段、关闭和订阅清理纳入同一 owner，禁止用全局 map 替代生命周期管理 |
| `chat_tui_team_inbox.go` 与 `chat_tui_team_session.go` | dispatcher 接入和 tick 消费应由一个 owner 完成，避免 cursor 逻辑被拆到两个互不知情的实现 |

因此，本路线推荐的施工顺序是：

1. 先合并并行路线中涉及 `team_member_tools.go`、`runtime.go`、`wakeup.go`、`team_task_service.go` 的改动；
2. 单独落地唯一 wake dispatcher 和结构化 `WaitSignal` 接口；
3. 再注册 `leader_wait`，补齐非并行分类、取消、超时和生命周期测试；
4. 最后接入 TUI 提示和 tick 广播，并跑跨段集成测试。

如果必须同时施工，只能把共享文件中的改动拆成预先冻结的最小接口提交，由 leader 负责集成；不能把“不同 Agent 修改不同文件”作为充分条件。

### 8.7 修订后的验收门禁

除原 §5.2/§5.3 的用例外，必须增加：

- `leader_wait` 实现 `BatchClassifier` 且 `ParallelSafe=false` 的分类测试；
- 信号在订阅前、drain 期间、select 建立后到达时均不会丢失或重复；
- TUI 与 `leader_wait` 同时存在时只推进一次 board cursor，双方都能收到同一事件；
- `teamBackends.closeAll()` 能解除等待，避免 teardown 泄漏 goroutine；
- 成员完成路径在 board 写入变慢时仍能先唤醒本进程等待者；
- 多个成员在一个切片内完成时，事件按稳定 `Seq/ID` 去重并保持可审计顺序；
- 600 秒上限、默认超时、`ctx` 取消和重复调用提示均有明确测试；
- 与 `TEAM_MEMBER_PARALLELISM_ROUTE.md` 合并后的全量 `go test`、`go vet`、定向 `-race` 和 lint 门禁通过。

### 8.8 节点状态对齐

在上述接口、cursor 所有权和工具分类修订落地前，W0-W6 继续保持“未开始”，不能把文档中的设计草案描述为已实现。尤其是：

- W2 不仅是新增 channel，还包括订阅前 pending、关闭、去重和多等待者策略；
- W5 不再是等待工具自己的 board 轮询，而是唯一 dispatcher 的 drain；
- W6 的 tick 消费必须通过 dispatcher，不能与等待工具重复推进 cursor；
- W3 只有在 `BatchClassifier` 和 leader-only/contextual availability 测试通过后才可验收。

> **2026-09-22 更新**：本节要求的四项修订均已落地（W2 的订阅前 pending 与关闭、
> W5 的唯一 dispatcher、W6 走 dispatcher 的 tick 消费、W3 的 `BatchClassifier` 与
> leader-only 用例）。**节点状态以 §6 为准**；本节保留为当时的对齐条件记录。
> W6 当前另有一个与该修订无关的 nil 解引用缺陷（见 §6 的 D1）。

