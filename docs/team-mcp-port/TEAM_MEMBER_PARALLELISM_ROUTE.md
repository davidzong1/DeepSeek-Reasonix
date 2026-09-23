# 分析报告与优化路线 —— Team session 成员并行思考

> 状态：**分析已完成（证据见附录 A）；优化路线拆为 Part A / Part B 两段，各节点的实施状态见 §5**（2026-09-22）。
> 本文档只描述现象、原因与改造方向，**不把任何未实施的东西描述为已实现**；每个节点的状态只在 §5 更新。
> 权威优先级：用户拍板 > `docs/team-mcp-port/TASK.md` > 本文档 > 实现代码注释。
> 关联：`TEAM_TUI_AGENT_DECOUPLING_PLAN.md`（本文档 §3.1 更正其 v0.12/v0.13 的一处「已挪走」结论）、
> `SHARED_CONTEXT_BLACKBOARD_TECHNICAL_ROUTE.md`（board 存储，本路线只读其行为不改其实现）。

---

## 1. 现象

在 team session 下观察到一个成员正在思考时，其他成员不再推进（UI 上表现为画面/进度停住），
因此怀疑成员之间是串行执行、而非多线程并行思考。

---

## 2. 结论：思考本身是真并行，没有互斥

**成员之间确实多线程并行思考。** 三层证据：

### 2.1 执行层：每成员一个独立 turn goroutine

| 机制 | 位置 | 说明 |
| --- | --- | --- |
| 每成员独立 goroutine | `internal/control/turn_loop.go:350` | `spawnGuardedTurn` 里 `go func()`；`SubmitUserTurnOrError` 只做准入即返回，不阻塞调用方（`internal/control/admission_submit.go:15`） |
| 占用是 per-member | `internal/team/agentruntime/runtime.go:35-38,102-108` | `byMember` 按成员 ID 分桶；`ErrMemberBusy` 只挡**同一成员**的重复驱动 |
| runtime 锁不跨重活 | `internal/team/agentruntime/runtime.go:167-173` | `r.mu` 从不在装配、store 写入、提交、board 记录期间持有 |
| hub 不在锁内调后端 | `internal/cli/team_hub.go:60-64` | 「No hub-level lock is held across the backend call」 |
| 装配可并行 | `internal/cli/team_backends.go:166-212` | 装配在注册表锁外，per-key in-flight guard；不同成员并行 boot |
| 事件汇入不阻塞 agent loop | `internal/cli/team_event_pump.go:1-30,52-56` | 每成员有界队列 + 非阻塞 sink，`Emit` 只入队 |
| per-member 后台 worker | `internal/cli/team_member_cockpit.go:13-26` | 同成员串行、跨成员并行 |

### 2.2 推理层：provider 请求路径上零互斥（本次最硬的证据）

`internal/provider/` 与 `internal/netclient/` 两个包（非测试文件）的全部同步原语只有 3 处：

- `internal/provider/schema_validate_args.go:17` — `var toolSchemaCache sync.Map`（只读缓存，无写侧串行）
- `internal/provider/retry.go:73` — `count atomic.Int64`
- `internal/provider/tool_search.go:17` — `nativeToolSearchPreview atomic.Bool`

**没有任何 mutex、semaphore 或有界 channel。** 即：多个成员同时向模型发请求、同时流式生成，
不存在任何排队点。`internal/agent` 的并发上限是 per-controller 的（`internal/agent/execute_batch.go:338`
的 `maxParallel`），不跨成员。

### 2.3 测试层

以下用例在本次分析中实跑通过：

```
TestTeamE2EAssignTaskToRelevantRunsBothMembersAndReportsBoth   PASS
TestTeamBackendsDistinctMembersAssembleInParallel              PASS
TestTeamBackendsConcurrentDistinctBindsOverlapAssembly         PASS
TestTeamHubLeaderAndMemberDoNotSerialize                       PASS
TestCockpitSerializesPerMemberAndRunsMembersInParallel         PASS
```

`internal/cli/team_concurrency_e2e_test.go:138-143` 直接钉住语义：一次
`leader_assign_task_to_relevant` 后 coder 与 tester **同时处于 Running**，回报也并发。
`internal/team/agentruntime/agentruntime_concurrent_test.go:19-44` 钉住 alpha/beta 并行启动、
完成 alpha 不影响 beta 的占用。

### 2.4 因此：现象不是「思考被串行化」，而是思考之外的三层阻塞

用户看到的「暂停」全部来自**渲染线程**、**跨成员共享资源**、**派发/装配路径**这三层，
与模型推理无关。下面逐层给出证据。

---

## 3. 原因

### 3.1 帧线程（Update goroutine）—— 影响面最大

所有成员的事件都汇入同一个 pump，由**同一个** `waitForMemberEvent` 命令消费，在 bubbletea 的
单线程 Update goroutine 上 ingest（`internal/cli/chat_tui_team_switch.go:17-31`）。
该线程上任何一次重活，会让**全部成员**的显示一起停住 —— 而它们的 goroutine 仍在跑。
这解释了「一个成员在思考、其他成员画面静止」的观感。

#### 3.1.1 `commitBackendReplay` 仍在线程上同步读取整段历史（**本文档要更正的核心项**）

`internal/cli/team_replay.go:117`：

```go
func (m *chatTUI) commitBackendReplay(backend control.SessionAPI, mode replayMode) tea.Cmd {
	history := backend.History()          // ← 在 Update goroutine 上同步执行
```

`TEAM_TUI_AGENT_DECOUPLING_PLAN.md` 的 v0.12/v0.13 与残留清单声称重放开销已离开帧路径 —— 但**只有
markdown 渲染与换行离开了，读取没有**。这一行在 `replayInlineMessages`（=24）窗口化判断**之前**，
因此 `backend.History()` 本身不受任何界限约束。

而团队会话绑定的后端通常是 **follower**，其 `History()` 是**每次调用都重读持久化源**
（`internal/cli/team_follower.go:198-205` 的注释原文：「re-read from the durable source on every call」）：

- `followerV3Source.History` → session-service 查询（`internal/cli/team_follower.go:646`）
- `followerLegacySource.History` → `agent.LoadSession(path)`，**整文件读取 + 解析**（`internal/cli/team_follower.go:667`）

**触发频率远高于「切换成员」**：它挂在 1s roster tick 上 ——
`teamRosterRefreshMsg` → `handleHistorySyncDone`（`internal/cli/team_history_sync.go:120`）→
`replayBoundHistory`（`internal/cli/team_history_sync.go:333-344`）→ `commitBackendReplay`。
即：**只要另一窗口/成员往历史里追加了内容，帧线程就会重读并重放整个成员历史**，开销 O(history)。

#### 3.1.2 1s tick 上的同步读盘

`internal/cli/chat_tui_team_session.go:31,47-70`，tick 间隔 `teamRosterRefreshInterval = 1s`：

```go
m.syncAmbientOwnerUsage()          // owner 指纹读盘
next := m.refreshTeamRosterView()  // 重新 reload 整个 team.json
... m.syncBoundHistory(), m.refreshBoundMemberUsage(), m.collectCockpitResults()
```

- `refreshTeamRosterView` → `p.reload(teamName)`（`internal/cli/chat_tui_team.go:294-315`）→
  `TeamStore.Load` = `os.ReadFile` + `json.Unmarshal`（`internal/team/teamstore.go:146-159`、
  `internal/team/storage.go:70-86`），**每秒重读并重解析整份 team.json（含所有团队）**；
  随后 `rosterMembers` 对每个已装配后端调 `RuntimeStatus()`，`invalidateMissingMemberState`
  再做一次 O(members) 扫描。
- 同一 tick 上**两次**独立读取同一份 owner 文档指纹：`syncAmbientOwnerUsage`
  （`internal/cli/chat_tui_team_session.go:103-124` → `boundOwnerFingerprint`）与
  `syncBoundHistory`（`internal/cli/team_history_sync.go:54`）→
  `OwnerStore.Fingerprint` → `readMeta`（`internal/team/ownerstore.go:243-259`）。

> **量级更正（2026-09-22 实测）**：本节最初把 tick 的读盘列为独立成因，实测后应降级。
> 用合成注册表（5 队 × 8 成员）测，每 tick 的稳态成本：
>
> | 操作 | ns/op |
> | --- | --- |
> | `TeamStore.Load()`（读+解析整份 team.json） | 66.8 µs |
> | `OwnerStore.Fingerprint()`（单次） | 6.2 µs |
> | `picker.reload()`（含逐成员 `RuntimeStatus()` 与 model 重建） | 24.8 µs |
> | `boundOwnerFingerprint()` | 22.4 µs |
> | 基线 `time.Now()` | 33 ns |
>
> 即 tick 稳态约 **70 µs 每秒**，是 60fps 一帧预算（16.7 ms）的 **0.4%**。
> 真正让它成为问题的是 §3.1.5 的**链重复续期**：N 条链即 N × 70 µs，且随团队历史变更
> 次数单调增长——那才是「越活跃越卡」的机制。A0 修掉链之后，本节的读盘本身不足以支撑
> 后续改造（见 §4.2 A2 的落地结论）。
>
> **一个陷阱（已处理）**：把两次 owner 指纹读合并成一次看似是免费优化，但**直接合并不安全**——
> `refreshTeamRosterView()` 可能在两次读之间重绑成员（`switchTeamMember(leader)` 是同步改
> `session.current` 的），而 `syncBoundHistory` 必须读重绑**之后**的 owner，才不会把新成员
> 的 `syncStamp` 与旧成员的指纹比对（那会误判为「历史变更」并触发一次多余整段重放）。
>
> **处理方式（2026-09-22，A6）不需要流水线**：关键是把读**移位**而不是**缓存**——把
> `syncAmbientOwnerUsage` 也挪到 roster 落定之后，然后**在 roster 之后读一次、把值传给两个消费者**
> （`syncAmbientOwnerUsage(fp, ok)` / `syncBoundHistory(fp, ok)`）。这样：
> ① 每 tick 从 2 次读降为 1 次；② 「两次读之间发生重绑」在结构上不可能，陷阱随之消失；
> ③ 无新载荷、无异步、无状态机。
> 守卫用例：`internal/cli/team_owner_poll_test.go` 的 `TestRosterTickReadsTheOwnerFingerprintOnce`
> —— 断言每 tick **恰好一次**读，且该读命名的成员是**重绑后**的当前成员（远端删除场景下必须是
> leader，不能是被离开的那个）。把读放回各消费者即红（实测 `named [lead lead]` → 计数 2）。

#### 3.1.3 每次结算 turn 仍内联 `store.Binding()`

cockpit **发布**已挪到后台（`internal/cli/team_member_cockpit.go`），但
`publishTurnOwnerHistory`（`internal/cli/team_history_sync.go:286-319`，被
`internal/cli/chat_tui_team_switch.go:74-76` 每结算一个 turn 调用一次）里的
`p.store.Binding(...)`（`:214`、`:306`）**仍在帧线程**上读盘。

#### 3.1.4 渲染路径里做磁盘 I/O 与 O(members²) 扫描

- `internal/cli/chat_tui_team_render.go:601` 的 `renderLeaderReset` 调
  `p.resetDirCount(teamName)`（`internal/cli/chat_tui_team_reset.go:144-151` →
  `MemberDirs`）—— **在渲染路径里做目录遍历**，O(context dirs)。
- `internal/cli/chat_tui.go:1621-1640` 的 `bottomRows()` **为了算行数把整个 overlay 渲染一遍**，
  而它每帧被调多次（`:522` 经 `transcriptHeight`、`internal/cli/chat_tui_stream.go:61,140`、
  `internal/cli/chat_tui_input.go:272`、`View`）。于是上面的目录遍历与
  `renderRoster`/`renderTeamSession` 的逐成员 `statusOf`→`slotOf` 全表扫描
  （`internal/cli/chat_tui_team_render.go:408-432,512-571`、`internal/cli/chat_tui_team.go:470-483`，
  O(members²)）**每帧要跑好几遍**。

#### 3.1.5 tick 链会按「结果」重复续期（实施中发现）

`refreshTeamRoster` 只在 `super` 载荷与「无 store」两种情况下提前返回，其余载荷——`replay`、
`sync`，以及本次新增的 `read`——都会走到函数末尾并**再续一条 tick**。而 `tea.Tick` 是「每次
触发各续一条」，所以每交付一个重放结果或历史同步结果，就多出一条每秒轮询的链。实测（探针已删）：

```
plain tick:          BATCH RE-ARMS a tick
cockpit result:      nil cmd (no re-arm)      ← 唯一有守卫的分支
replay result:       BATCH RE-ARMS a tick
sync result:         BATCH RE-ARMS a tick
```

后果是 §3.1.2 的读盘代价被**乘上链数**：团队越活跃、历史变更越多，每秒的 `team.json` 重读
与 owner 指纹读就越多。这是 A2 的乘数，必须先修，否则 A2 只是把「1 次读」挪走而留下 N 次。

修法沿用仓库既有的 `elapsedTickGeneration` 模式：tick 消息带代数，只有「当前代数的真 tick」
续期，会话入口 `startRosterTick` 是唯一开链点。已落地（A0）。

### 3.2 跨成员共享资源 —— 真的会互相阻塞

#### 3.2.1 工作区写租约：所有成员抢同一把跨进程独占锁

每个成员都用**同一个 workspace root** 装配（`internal/cli/team_backend_build.go:425-426`），
并且 `boot.Build` 每次都为该成员建一个租约 Owner（`internal/boot/boot.go:553`）：
`workspacelease.New(root, config.WorkspaceLeaseDir(), ...)`。
`config.WorkspaceLeaseDir()` 是 **OS 用户级全局目录**（`internal/config/paths.go:353-362`，
刻意不随 `REASONIX_HOME` 变化），锁文件路径按 canonical git worktree 根哈希
（`internal/workspacelease/lease.go:663-667`）。→ **同团队所有成员在同一组锁文件上争用。**

- **持有时长 = 单次写工具调用**（获取于 `internal/agent/tool_write_coordination.go:22-31`，
  释放于 `internal/agent/execute_one.go:41-46` 的 defer）。LLM 调用与空转不持锁。
- **不可静态证明只读的 bash 取整工作区独占**（`internal/shellsafe/effect.go:46-48`；
  bash 不在 `pathBoundWriterNames` 内），MCP/自定义 writer 与「hook 可能改工作区」同理
  （`internal/agent/tool_write_coordination.go:57-61,72`）。→ 一个成员跑 `go test ./...` /
  `npm run build`，**其他所有成员的写入被挡住整场构建**。
- **后台任务会把锁留住**：`jobs.WithJobStartObserver(workspaceLease.RetainUntil)`
  （`internal/boot/boot.go:565`）→ `RetainUntil`（`internal/workspacelease/lease.go:435-457`），
  宽限常量 `backgroundGrace = 30 * time.Second`（`internal/workspacelease/lease.go:25`）。
  常驻后台任务（dev server）在这 30s 上限后释放，但**这 30s 内其他成员仍被挡**。
- **错误信息与代价**：被挡成员收到自己 sink 的提示
  「Another session is writing to this workspace; this session will continue automatically when it is safe.」
  （`internal/boot/boot.go:553-561`，code `event.NoticeCodeWorkspaceLease`）。
  但**工具 ctx 无 deadline 时这个等待无上限**，与提示文案不符；有 deadline 时退化为工具结果
  `"blocked: the workspace did not become available for writing: …"`
  （`internal/agent/tool_write_coordination.go:26-28`）。
- **stripe 碰撞**：`pathLockStripes = 4096`、`treeLockStripes = 4096`
  （`internal/workspacelease/scope.go:20,25`），代码自述「Hash collisions conservatively serialize
  unrelated files」。

#### 3.2.2 filelock 写者优先会放大阻塞面

`internal/filelock/filelock.go:34-37` 的 `localRegistry` 按锁文件路径的 canonical identity 建
**进程级** RW 队列；`internal/filelock/filelock.go:230-241`：

```go
if mode == ModeShared {
	if !local.exclusive && local.waitingWriters == 0 {   // ← 有任何写者在排队，shared 一律拒绝
```

→ 一个成员排队等**整工作区独占**时，会连带挡住**同进程内其他成员对不相关文件**的 shared 获取：
被挡的是「有写者在等」，不是「彼此冲突」。同一进程内的两个成员是两个 Owner，没有共享 map，
因此只会排队不会自死锁（自死锁防护是 per-Owner 的，见 `internal/workspacelease/lease.go:608-618`）。

#### 3.2.3 SQLite board 单写者，5s 是唯一上限

`internal/team/blackboard_sqlite.go:28-31`：WAL + `busy_timeout(5000)` + `synchronous(NORMAL)`；
每次写走 `BEGIN IMMEDIATE`（`:127-145`）。落在成员 turn 路径上的写：`Runtime.Start`→`SaveTask`、
`Runtime.Complete`→`SaveTask`+`record`→`Append`、`wakeAll`→`wakeLeader`→`Append`、report 路径。

**关键在于 ctx**：`internal/team/scheduler/runtime.go:72` 的 `exec.Start(context.Background(), …)`
与 `internal/team/agentruntime/runtime.go:353` 的 `r.board.Append(context.Background(), …)`
都是 `context.Background()` → **5s `busy_timeout` 是唯一上限，调用方无法取消**。
即一个成员的 report 单次写最多可被同伴拖 5s，且不可中断。

#### 3.2.4 `wakeLeader` 跨 `teamStore.Load()` 持服务级锁

`internal/cli/team_task_service.go:59-66`：

```go
func (s *teamTaskService) wakeLeader(reason string) error {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	for _, fn := range s.onWake { _ = fn(reason) }
	return nil
}
```

`onWake` 是 `agentruntime.NewBoardWakeFor(board, team.BoardShared, s.leaderIdentity)`
（`internal/cli/team_task_service.go:118`），`identityOf` 在 `boardWake.wake` 内被调用 →
`s.leaderIdentity()`（`internal/cli/team_task_service.go:74-93`）**做一次 `teamStore.Load()`
（JSON 读 + 解析）**，外加一次 board `Append` —— 全部在 `wakeMu` 内。
而 `wakeAll`（`internal/team/agentruntime/runtime.go:384-391`）在每个任务 `Complete`/`Cancel` 时调用。
→ **多个成员同时完成**（团队协作里最常见的时刻）时，它们的完成路径在这里被收成串行。

#### 3.2.5 进程级全局锁

- `internal/team/atomic.go:16` — `var writeMu sync.Mutex`，**所有** `.reasonix/team` JSON 写的
  唯一 choke point（代码自述「the single chokepoint」）。所有成员共用一个 `TeamStore`。
- `internal/memory/store_v2.go:38` — `var memoryStoreMutationMu sync.Mutex`，在 `SaveWithOptions`
  （`:157`）全程持有。这条对团队特别相关：**非 leader 成员的写作用域就是
  `config.MemoryUserDir()`**（`internal/cli/team_backend_build.go:364-372`）→ **所有非 leader 成员
  共享同一个 memory store 与其进程级写锁**。
- `internal/fileops/observation.go:336-338` — `var mutationLocks [257]sync.Mutex`，内置文件写入器
  按目标路径取锁（`internal/tool/builtin/editsource.go:60-67`）。两成员改同一文件在此串行。

### 3.3 派发与装配路径 —— 让「同时开跑」变成「依次开跑」

- **`assignSubtask` 里的 `boot.Build` 是同步的**：`internal/cli/team_task_service.go:444` →
  `scheduler.Assign` → `runtime.Start` → `internal/cli/team_backends.go:bind` → `r.build(b)`
  → 完整 `boot.Build`（config、provider、MCP/plugin 子进程、resume，秒级）。
- **leader 的 dispatch 工具都是 writer，在 `execute_batch` 里只能串行**：
  `internal/agent/execute_batch.go:287-307`（连续**只读**调用才并行，`:336-337` 上限 8）。
- **`leader_assign_task_to_relevant` 内部是 for 循环逐个 assign**：
  `internal/cli/team_member_tools.go:186-191`。

→ 「派发给 N 个成员」= N 次串行的 boot，第 N 个成员在前 N-1 次 boot 完成前根本没开始。
已启动的成员这时仍在并行跑。
- 另：`defaultMaxTeamBackends = 4`（`internal/cli/team_backends.go:41`），成员数 >4 时 LRU 空闲成员
  被退休（`:292-314`），下次绑定要重新 boot。

### 3.4 已排除项（不作为原因）

- **审批阻塞不传染**：`internal/agent/execute_one.go:73-74` 明确「Permission must complete before
  any write lease」，权限在租约之前解决。卡在人工审批的成员**不会**占着工作区锁挡住别人。
  （该成员自己不推进，但不放大 §3.2.1。）
- **pump 不阻塞**：`internal/cli/team_event_pump.go` 的 sink 只入队、每成员有界队列；
  `bufferMemberEvent` 上限 `memberLiveEventCap = 512`（`internal/cli/chat_tui_team_switch.go:97`）。
  历史上「成员一多就卡」的根因是**共享阻塞通道**（cap 1024），已被 v0.10 换成 pump
  （见 `TEAM_TUI_AGENT_DECOUPLING_PLAN.md` v0.10 条目），本次不再复现。
- **`checkStatus` 不在帧线程**：`internal/cli/team_status_poll.go` 的 `checkStatus`/`readStatus`
  跑在工具处理器里；`minStatusPollInterval = 60s` 只抑制**回复**，状态变化绕过节流，不构成阻塞。

### 3.5 症状对照表（便于现场定位）

| 现场表现 | 真实原因 | 章节 |
| --- | --- | --- |
| 一个成员输出时，其他成员画面/进度静止 | 帧线程被某一成员的历史重读卡住 | §3.1.1 |
| 每约 1 秒出现一次规律性顿挫 | tick 的 team.json 重读 + 两次 owner 指纹读 | §3.1.2 |
| 某成员一直「在跑」但无任何产出/无工具结果 | 等 workspace 写租约（另一成员在跑长构建） | §3.2.1 |
| N 个成员依次启动而非同时启动 | dispatch 路径同步 boot + writer 工具串行 | §3.3 |
| 多个成员几乎同时完成时集体延迟一下 | `wakeMu` 跨 `teamStore.Load()` + board 写 | §3.2.4 |
| 成员 report 偶发卡顿后失败 | board 单写者，5s busy_timeout 且 ctx 不可取消 | §3.2.3 |

---

## 4. 优化路线：拆为可并行施工的两段

### 4.1 拆分原则与边界

拆分的唯一依据是**文件互斥**：两段必须零文件重叠，否则两个 Agent 并行编辑会互相覆盖。
按「帧路径」与「执行层/共享资源」切分天然满足这一条件。

| | Part A —— 帧路径去阻塞 | Part B —— 跨成员串行资源 |
| --- | --- | --- |
| 主题 | 把重活移出 Update goroutine | 拆掉成员之间真正的阻塞点 |
| 目录 | `internal/cli`（仅 TUI/重放/历史同步） | `internal/cli`（仅任务服务/后端注册表/工具）、`internal/team`、`internal/workspacelease`、`internal/boot` |
| 改动性质 | 保持语义，只改执行位置与缓存 | 改并发语义、ctx 生命周期、锁域 |
| 风险 | 低（多为「骑 tick 回投」的既有模式复用） | 中（涉及锁与超时语义，需谨慎） |

**Part A 独占文件**（Part B 禁止编辑）：

```
internal/cli/team_replay.go
internal/cli/team_history_sync.go
internal/cli/chat_tui_team_session.go
internal/cli/chat_tui_team_switch.go
internal/cli/chat_tui_team_render.go
internal/cli/chat_tui_team_reset.go
internal/cli/chat_tui_team.go
internal/cli/chat_tui.go
```

**Part B 独占文件**（Part A 禁止编辑）：

```
internal/cli/team_task_service.go
internal/cli/team_backends.go
internal/cli/team_member_tools.go
internal/cli/team_backend_build.go
internal/team/scheduler/runtime.go
internal/team/agentruntime/runtime.go
internal/team/agentruntime/wakeup.go
internal/workspacelease/lease.go
internal/workspacelease/scope.go
internal/boot/boot.go
```

**双方共同冻结（本轮谁都不改）**：`internal/agent/**`、`internal/control/**`、
`internal/provider/**`、`internal/netclient/**`、`internal/filelock/**`、
`internal/team/blackboard_sqlite.go`、`internal/memory/**`、`internal/fileops/**`、
`internal/cli/team_event_pump.go`、`internal/cli/team_member_cockpit.go`。
理由见 §6.3。

**测试文件**也按同一原则分配，新增用例写进各自新增的 `_test.go`，不共用测试文件。

---

### 4.2 Part A —— 把重活移出 Update goroutine

#### A1（P0）把 `backend.History()` 移出帧线程

- **问题**：§3.1.1。渲染已后台化、读取没有，是**同一次改动留下的另一半**。
- **做法**：把 `internal/cli/team_replay.go:117` 的 `backend.History()` 也放进后台 cmd，
  与已有的全量渲染一起回投。**复用现成通道**：`replayBounded` 路径已有
  `teamReplayReadyMsg`（`internal/cli/team_replay.go:191`）骑 tick 回投的机制，
  读取结果可与 `replayLoad` 一起送达，不新增 Update 分支。
- **注意**：`replayInline` 路径（ambient 归还、`/model` 重建、配额 failover，无 cmd 可回投）
  必须保持内联 —— 与 v0.12 已确立的边界一致，不要一并后台化。
- **验收**：新增用例必须**从 `Update` 返回的 cmd 里取回结果**（而不是从窗口状态重算），
  否则「算了却没交回循环」这类接线缺失会静默通过 —— 沿用 v0.13 的既有做法。
  另需一条用例证明：长时间不重放的情况下，帧路径上对该后端的 `History()` 调用次数为 0。
- **风险**：低。注意 `m.replay.gen` 世代校验在读取前移后仍需生效（被取代的读结果必须丢弃）。

- **落地范围（2026-09-22 实施时收窄）**：延后只做在 **tick 刷新路径**（`replayBoundHistory` →
  新增的 `replayDeferred`），**bind 路径保持内联读**。
  - 理由一：bind 路径的契约是「返回前必须已画出 incoming 成员的 transcript」——把读延后会先
    `clearTranscriptDisplay` 再等读回来，中间是空屏；而 bind 的调用方（11 个既有用例，其中 6 个
    与重放无关）依赖该契约。刷新路径没有这个问题：窗口本来就显示该成员，且实现上把 clear 与
    install 合并到结果落地，反而**消除了原有的每次刷新闪空**。
  - 理由二：真正被**重复**触发的是刷新路径（同伴每次追加都会经 tick 走到这里），bind 只在用户
    切换时发生一次，代价可接受。
  - 因此 A1 的收益窗口是「团队活跃时每秒多次的 O(history) 读」，而非「每次切换」。

#### A2（P0）把 1s tick 的两处读盘移出帧线程

- **问题**：§3.1.2。
- **做法**：
  1. `refreshTeamRosterView` 的 `p.reload(teamName)` 改为后台读 + 结果骑
     `teamRosterRefreshMsg`（该消息已有 `sync`/`replay`/`super` 三个载荷位，按需扩展，
     **不新增 Update 分支** —— 这是本仓库既定约定）。
  2. `syncAmbientOwnerUsage` 与 `syncBoundHistory` 的两次 owner 指纹读合并为一次
     后台读（同一 tick 读同一份文档两次是纯浪费）。
- **验收**：一条用例证明 tick 之后帧路径上零读盘（沿用在 `team_follower_usage_test.go`
  里已确立的「帧路径零读盘」断言手法），且 roster 变更仍能在一次 tick 内生效。
- **风险**：中低。`reload` 失败路径（`pickerErrMsg`）与「成员被远端删除 → rebind」分支
  （`internal/cli/chat_tui_team_session.go:170-182`）必须保持可达，不得因异步化而丢失。

- **落地结论（2026-09-22）：不做，附实测依据。** 链守卫（A0）落地后，tick 恢复到每秒一条、
  当次读盘约 **70 µs**（见 §3.1.2 的量级更正框），是单帧预算的 0.4%。而把 `p.reload()` 与
  owner 指纹读异步化，需要把 tick 拆成「arm 读 → 落地应用 → 再 arm 指纹读」的多段流水线，
  以保住本节风险栏点名的两条分支与「roster 先落定、owner 后读」的顺序——用真实的回归风险
  换 70 µs/s 不成立。
- **残留**：`p.reload()` 与两次 owner 指纹读仍在 Update goroutine；**若**将来出现成员数远超
  数十、或 team.json 显著变大（新增字段/多团队）的现场报告，应以那时的 profile 重开本节，
  而不是按本节原判断直接动手。

#### A3（P1）`publishTurnOwnerHistory` 的 `Binding` 读移出帧线程

- **问题**：§3.1.3。
- **做法**：`internal/cli/team_history_sync.go:214,306` 的 `p.store.Binding(...)` 与
  cockpit 发布一起交给后台 worker（`memberCockpit.submit` 已是既成模式）。
- **验收**：结算 turn 的路径上零 `store.Binding` 调用；owner generation 仍正确推进。
- **风险**：低。注意 `requireIdentity` 的跳过语义（无身份的成员不得凭空建 owner）。

- **实施时查明**：`recordMemberOwnerHistoryWith` **只用 binding 的 `Team` 与 `MemberID`**
  （`internal/cli/team_member_owner.go:174-183`），二者在帧上已知。因此那次 `store.Binding`
  的**唯一实质作用是「该成员是否仍在团队里」的存在性校验**（失败时 `reportHistorySyncFailure`
  报 `publish-binding:<member>`），不是取数据。
- **落地结论（2026-09-22）：不做，附实测依据。** 该读 = `Load()` + 一次扫描 ≈ **25 µs**，
  且只在**每次结算 turn 时每成员一次**（远低于 tick 频率）。
  - 想保留「读新鲜磁盘」的校验语义，就必须把发布拆成「arm 读 → 落地提交 cockpit」，
    即再增一个 tick 载荷——为一个 25 µs、低频、且 `p.doc` 在每个 tick 都会被刷新的校验，
    不抵新增状态机的成本。
  - 省事的替代（改读内存快照 `p.doc`）会把校验来源从「新鲜磁盘」换成「≤1s 快照」，在
    「成员刚被删除、owner 目录正在清理」的窗口内，一次结算会为已被删除的成员重建 owner 文档
    ——这正是 `publishTurnOwnerHistory` 注释里那条「不许凭空造 owner」的禁令要挡的事。
  - 若将来要做，应选第一种（异步读 + 保留校验语义），不要选第二种。

#### A4（P1）渲染路径里的目录遍历

- **问题**：§3.1.4 的 `renderLeaderReset`。
- **做法**：把 `resetDirCount` 的统计**在确认界面 arm 时**算好并缓存（`renderTeamClear`
  已经这么做了 —— `internal/cli/chat_tui_team_clear.go:168-199` 用的是缓存好的
  `c.targets`，照它的形状改）。
- **验收**：一条用例证明渲染 `renderLeaderReset` 不触发 `MemberDirs`。
- **风险**：低。

#### A5（P2）`bottomRows()` 每帧全量渲染 + O(members²) 扫描

- **问题**：§3.1.4 后半。`bottomRows()` 为算行数把整个 overlay 渲染一遍，且每帧多次调用，
  于是目录遍历与 `statusOf`→`slotOf` 全表扫描被放大若干倍。
- **做法**：`bottomRows` 的行数按可用宽度/尺寸缓存，仅在其输入变化时重算；
  `slotOf` 改一次构建 map 供本轮所有查询使用。
- **验收**：一条用例（或基准）证明同一帧内 `bottomRows` 只做一次 overlay 渲染。
- **风险**：低，但要注意缓存失效条件（宽度、成员数、refusal 文案变化）。
- **落地范围（2026-09-22 实施时收窄，并已补测）**：只做了 `slotOf` 的 O(members²) 消除
  （`renderRoster` 改为局部 `memberSlotMap()` 一次成表），**`bottomRows` 记忆化决定不做**。
  - **实测（本次补测，替换掉先前的未测判断）**：

    | 操作 | ns/op |
    | --- | --- |
    | `bottomRows()`（无 overlay） | 7.3 µs |
    | `bottomRows()`（团队 overlay 打开） | 6.2 µs |
    | `bottomRows()`（roster 屏，2 / 8 / 20 成员） | 3.6 / 3.4 / 3.3 µs |
    | 每帧调用次数（Update + View 各一次） | **2** |

    即约 **7–15 µs/帧**，是 60fps 单帧预算（16.7 ms）的 **< 0.1%**。
  - **本节先前的两处判断经实测为错**：① 「每帧多次调用」实为 2 次；
    ② 「放大了 O(members²)」——本项做掉之后按行扫描已是 O(members)，且 2/8/20 成员下
    实测几乎不随成员数增长。
  - 取舍：记忆化需要覆盖 14 个面板 + 主管理器/页脚 + 输入框高度 + 状态行数的组合作为失效面，
    以「失效漏判 → viewport 高度错 → 错帧」的风险换 < 0.1% 的一帧，不成立。
  - 残留：`bottomRows` 仍每帧渲染 14 个面板以数行数；**若**将来出现面板数量级增长或 render
    路径新增重活的现场报告，以那时的 profile 重开本节，而不是按本节原判断直接动手。

**Part A 不做什么**：不改 `internal/cli/team_task_service.go` 与 `team_backends.go`
（属 Part B）；不动 pump；不引入新的并发模型，只复用既有「后台算 + 骑 tick 回投」模式。

---

### 4.3 Part B —— 拆掉成员之间真正的阻塞点

#### B1（P0）`wakeLeader` 不跨 `teamStore.Load()` 持 `wakeMu`

- **问题**：§3.2.4。多个成员同时完成是最常见的时刻，这里恰好把并发收成串行。
- **做法**：把 `leaderIdentity()` 解析移到 `wakeMu` **之外**先取好，再进锁做 board `Append`
  （identity 每次重读是为了 leader 变更可重定向 —— 这个语义必须保住，只是挪出锁外）。
- **验收**：一条用例证明两个成员同时 `Complete` 时不会互相等待 `teamStore.Load()`
  （可用一个注入的慢 Load 探针，或统计并发度峰值 —— 参考
  `TestTeamHubLeaderAndMemberDoNotSerialize` 的 `probe.peak` 手法，但**不要**用 sleep 计时，
  该用例已被记录为偶发 flaky，见 `TEAM_TUI_AGENT_DECOUPLING_PLAN.md` v0.13）。
- **风险**：低。注意 leader 变更的重定向语义与「无 leader 时跳过 wake」的既有分支。

#### B2（P0）给 board 写传有 deadline 的 ctx

- **问题**：§3.2.3。`context.Background()` 让 5s `busy_timeout` 成为唯一且不可取消的上限。
- **做法**：`internal/team/scheduler/runtime.go:72` 与
  `internal/team/agentruntime/runtime.go:353` 改为传调用方可控的 ctx。
- **验收**：用例证明 ctx 取消后写即刻返回，而不是等满 5s。
- **风险**：**中高，需谨慎**。把无界等待变成有界，可能让原本「等一等就成功」的写在慢 board 上
  失败。落地时必须：① 超时值有依据（不与 `busy_timeout(5000)` 打架）；
  ② 失败路径走既有的 `failDispatch` / 状态回退语义，不产生持久化的幽灵状态；
  ③ 在文档中记录新的失败可见性（leader 会收到 wake）。
  若评估后认为风险不可控，本项应降级为「只加 deadline、不改写失败语义」并记录残留。

#### B3（P1）工作区租约：让「等锁」可取消、可区分

- **问题**：§3.2.1 + §3.2.2。等锁与「在思考」在 UI 上完全同形，且提示承诺「会自动继续」但
  在 ctx 无 deadline 时无上限。
- **做法**（按风险从低到高，建议至少做前两项）：
  1. 让被挡状态可观测：等锁时发出一个可区分的状态事件（沿用 `event.NoticeCodeWorkspaceLease`），
     使 TUI 能把该成员渲染为「等待工作区」而非「运行中」。
  2. 给成员写工具路径的租约等待一个有界 deadline，使提示文案与事实一致
     （有 deadline 时本就退化为 `blocked:` 结果，只是当前两条路径的 ctx 不同）。
  3. （评估项）缩小 bash 的 whole-workspace 范围。**注意**：`internal/shellsafe/effect.go:46-48` 的
     「不可静态证明只读即取独占」是**安全设计**，不是缺陷 —— 任何放宽都必须给出等价的安全论证，
     否则不做。
- **验收**：用例证明等锁中的成员产生可区分的状态；用例证明有界等待按 deadline 返回。
- **风险**：**高（安全面）**。第 3 项若无充分论证则明确不做，只做 1、2。

#### B4（P2）`defaultMaxTeamBackends` 与 LRU 抖动

- **问题**：§3.3 末尾。成员数 >4 时 LRU 退休空闲成员，下次绑定重新 boot（秒级），
  表现为「该成员一直在启动」。
- **做法**：上限按团队规模取值（至少 `max(4, len(team.Template))`），
  或改为配置项。退休仍必须遵守已有的「运行中/挂起提示的后端永不退休」约束
  （`internal/cli/team_backends.go:292-314` 的 `evictOverCap`）。
- **验收**：用例证明 6 人团队下依次绑定全员不发生退休抖动；且运行中的后端仍不被退休。
- **风险**：低。注意内存/子进程成本：每个后端带自己的 plugin/MCP 子进程与会话租约，
  上限放宽会提高常驻开销，需在文档记录取舍。

#### B5（P2）fan-out 真正并行装配

- **问题**：§3.3。派发给 N 个成员 = N 次串行 boot。
- **做法**：在 `assignSubtask`（或一个新的 fan-out 方法）内部并行 bind+build，
  使**一次工具调用**完成 N 个成员的启动。**不改** `internal/agent/execute_batch.go` ——
  它是本轮冻结文件，且单个工具调用内部并行不触碰它的 writer 串行规则。
- **验收**：用例证明 N 个未装配成员并行完成装配（统计装配并发峰值 ≥2，
  用探针而非 sleep 计时）；且任一成员装配失败不回滚其他已成功的分配
  （失败语义沿用既有的 `ErrStartFailed` + `failDispatch`）。
- **风险**：中。注意每个成员的分配/失败/唤醒语义在 `assignSubtask` 里是逐个写入的，
  并行化后要保证每条 task 的 durable 记录与 wake 与串行版逐字节等价。

**Part B 不做什么**：不改任何 `chat_tui*.go` 与 `team_replay.go`（属 Part A）；
不动 fluid pump / cockpit；不改 board 的 SQLite 实现（属冻结）；不放宽任何安全门（除 B3-3
经论证后明确立项）。

---

### 4.4 明确不在本轮范围

| 项 | 为什么不做 |
| --- | --- |
| **预热成员后端**（overlay 打开时预装配） | 需要 Part A 文件里的调用点（`internal/cli/chat_tui_team_switch.go:424` 的 `bindTeamBackends`、`internal/cli/chat_tui_team.go:160` 的 `onTeamButtonClick`）。为保住两段零文件重叠，本轮不做；且 **B5 已覆盖其大部分收益**（把 N 次串行 boot 变成并行），预热只是把成本前移。待 A/B 合并后可单独立项。 |
| **wakeup 驱动 leader 开新一轮** | 这是产品行为决策（当前 `internal/cli/chat_tui_team.go:198-202` 把 wakeup 只渲染成 notice），不是性能缺陷。需用户拍板。 |
| **放宽 bash 的 whole-workspace 独占** | 安全设计，见 B3-3，无等价安全论证不做。 |
| **board 存储层改造** | 属 `SHARED_CONTEXT_BLACKBOARD_TECHNICAL_ROUTE.md` 的路线，本路线只改调用方的 ctx。 |
| **`memoryStoreMutationMu` 全局化收敛** | 需要重构 memory store 的写路径（非团队域），收益为 ms 级，不抵风险。 |

---

## 5. 节点状态

| 节点 | 主题 | 所属 | 状态 | 证据 |
| --- | --- | --- | --- | --- |
| A0 | **tick 链按结果重复续期**（实施中发现，见 §3.1.5） | Part A | 已完成 | `chat_tui_team_session.go` 的 `rosterTick`/`startRosterTick` + 世代守卫；`team_replay_read_test.go` 的 `TestRosterTickDoesNotMultiplyPerDeliveredResult`（去掉守卫即红，已验） |
| A1 | `backend.History()` 移出帧线程 | Part A | 已完成（**范围收窄**，见 §4.2 A1 的「落地范围」） | `team_replay.go` 的 `replayDeferred`/`readReplayCmd`/`handleReplayReadReady`；`team_replay_read_test.go` 两个用例（把读改回内联即红，已验） |
| A2 | tick 两处读盘移出帧线程 | Part A | **链守卫已完成（A0）**；`p.reload()` 与 owner 指纹读经实测**决定不做**（依据见 §4.2 A2） | §3.1.2 量级更正框（实测 70 µs/s） |
| A3 | `publishTurnOwnerHistory` 读移出 | Part A | **决定不做**（实测 25 µs、每结算 turn 每成员一次；依据与两种做法取舍见 §4.2 A3） | — |
| A4 | 渲染路径目录遍历 | Part A | 已完成 | `leaderResetState.dirCount`（arm 时计算）；`renderLeaderReset` 读缓存 |
| A5 | `bottomRows` 每帧全量渲染 | Part A | **O(members²) 已消除**；`bottomRows` 记忆化经实测（7–15 µs/帧，<0.1% 单帧预算）**决定不做** | `memberSlotMap()`；`statusOf` 随之为死代码已删；实测表见 §4.2 A5 |
| A6 | **§3.1.2 的合并陷阱**（实施中发现，见 §3.1.2 陷阱框） | Part A | 已完成 | 读移到 roster 之后并只读一次；`team_owner_poll_test.go` 的 `TestRosterTickReadsTheOwnerFingerprintOnce`（放回各消费者即红，已验） |
| B1 | `wakeMu` 不跨 `teamStore.Load()` | Part B | 已完成 | `agentruntime.NewBoardWakeStamped`（取代 `NewBoardWakeFor`：stamp 由调用方在进锁前解析，per-wake 重定向语义不变）；`teamTaskService.leaderStamp` 为解析缝，`wakeLeader` 先解析后进 `wakeMu`，锁只覆盖 board append。新增 `team_wake_leader_lock_test.go`：探针证明两次解析同时在途（锁内解析不可能到 2），且两条 wake 仍以 leader id 落地。红→绿已验（把解析移回锁内即 5s 超时守卫失败）。 |
| B2 | board 写传 deadline ctx | Part B | 已完成 | `scheduler.RuntimeScheduler.Assign/Restore(ctx, …)`（含 `orBackground` 与 `persistRestoreFailure`）把调用方 ctx 传到 `exec.Start/Resume`；`agentruntime.Runtime.writeContext` 让 `record` 的 board append 借用调用方 ctx 并以 `boardWriteTimeout`(4s) 为界，`Cancel`/`Complete` 仍是有意不可取消的语义、只丢掉无界等待。新增 `scheduler/ctx_dispatch_test.go`（Assign/Restore 把调用方 ctx 交给 executor、传 nil 不 panic）与 `agentruntime/ctx_board_write_test.go`（取消即返回；无调用方 ctx 时带界）。红→绿已验（`Assign` 传 `Background()` 即两处断言失败）。失败语义未改：dispatch 依旧走 `failDispatch` + 唤醒 leader，只是更早可达。**残留**：`Complete`/`Cancel` 的 `SaveTask` 仍是 `context.Background()`（报告必须在其 turn 结束后仍落盘），且 `internal/team/blackboard_sqlite.go` 本轮冻结，driver 阻塞中的 `busy_timeout` 是否响应 ctx 未验。 |
| B3 | 等锁可取消/可区分 | Part B | 进行中 | **第 1 项已落地**：新增 `internal/boot/workspace_lease_notice.go` 的 `workspaceLeaseWaitEvent`，从 `workspacelease.Owner.State()`（`Waiting`/`Scope`/`Label`）把等待分类成「整工作区」或「某文件」，沿用 `event.NoticeCodeWorkspaceLease`（`boot.go` 的 `onWait` 改为发这个事件）。新增 `boot/workspace_lease_wait_notice_test.go`：真实双 Owner 争用下，整工作区等待的 detail 含 “whole workspace”、文件等待含文件名，互不混淆。**第 2 项未做（残留）**：给成员写工具路径的租约等待加有界 deadline 需要改 `internal/agent/tool_write_coordination.go` / `execute_one.go` 的 ctx，而 `internal/agent/**` 本轮冻结（§4.1 第三张清单、§6.1 第 3 条：需改冻结文件先回报）；唯一不越界的替代（在 `workspacelease.Owner` 上给所有等待加全局上限）会把主窗口「等一等就成功」的写变成失败，属产品权衡，不在本轮单方面决定。**第 3 项按文档明确不做**（安全设计，无等价安全论证）。**另注**：把该成员渲染成「等待工作区」需要 `chat_tui_team_render.go` / `chat_tui_team.go`（Part A 独占文件），Part B 只提供可区分的事件载荷。**后续优化路线（指名、有界、调度、产物通道）见 `TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md`。** |
| B4 | 后端上限与 LRU 抖动 | Part B | 已完成 | `teamBackends.fitToTeam`（在 `bind` 里、注册表锁外先读名单）把 `max` 抬到 `max(配置上限, len(team.Template))`，由 `setTasks` 安装的 `teamTaskService.rosterSize` 提供名单；`evictOverCap` 的「运行中/挂起提示的后端永不退休」约束一字未动。新增 `team_backends_cap_test.go`：6 人名单 + 上限 4 依次绑定 0 次退休/0 次重建；跨团队超出拟合上限时仍退休空闲成员且不碰运行中的成员；无名单时上限不变。红→绿已验（去掉 `fitToTeam` 即报 “member lead was retired”）。取舍：名单越大常驻后端越多（各带 plugin/MCP 子进程与会话租约）。 |
| B5 | fan-out 并行装配 | Part B | 已完成 | 新增 `teamTaskService.assignSubtasks`（`team_task_fanout.go`，`wg.Go` 按成员并行，结果保持调用方顺序），`leader_assign_task_to_relevant` 改为一次调用走 fan-out；每个成员仍是自己的 durable 行、自己的 dispatch、自己的失败。新增 `team_fanout_assembly_test.go`：探针统计装配并发峰值（3 个未装配成员同时卡在装配中，无 sleep 计时），且某成员装配失败不回滚兄弟（其行仍是 assigned、未 driven，供 leader retry）。红→绿已验（改成串行即 “only 1 member assemblies started together”）。 |

状态取值：`未开始` / `进行中` / `待验收` / `已完成` / `阻塞`。**阻塞必须写真实原因，
不得改写为通过**（沿用 `TEAM_TUI_AGENT_DECOUPLING_PLAN.md` 的铁律）。

### 5.1 Part B 门禁与偏差记录（2026-09-22）

§6.2 门禁实跑（Part B 分支，改动范围内）：

```
gofmt -l <改动文件>                                    # 空
go build ./...                                         # 通过
go vet ./internal/cli/ ./internal/team/... ./internal/workspacelease/ ./internal/boot/   # 空
go test ./internal/team/... ./internal/boot/ ./internal/workspacelease/ ./internal/cli/  # 全绿
go test -race ./internal/cli/ -run 'Team|Cockpit|Replay|Roster' -count=2                # 绿
go test -race ./internal/team/agentruntime/ ./internal/team/scheduler/ -count=2          # 绿
go vet ./...                                           # 全仓空（确认无其他包被签名变更打破）
golangci-lint run ./internal/cli/... ./internal/team/... ./internal/boot/... ./internal/workspacelease/...
```

§6.2 点名的既有并发用例逐条实跑通过：`TestTeamE2EAssignTaskToRelevantRunsBothMembersAndReportsBoth`、
`TestTeamBackendsDistinctMembersAssembleInParallel`、`TestTeamHubLeaderAndMemberDoNotSerialize`、
`TestCockpitSerializesPerMemberAndRunsMembersInParallel`、`internal/team/agentruntime/agentruntime_concurrent_test.go`、
`internal/team/scheduler/scheduler_concurrent_test.go`（后两个包整体绿）。

偏差与说明：

1. **额外改了两个不属于任何一方独占清单的文件**（因为 `scheduler.Assign/Restore` 的 ctx 参数变更必须
   在其调用点落地，否则编译不过）：`internal/cli/team_task_redrive.go`（3 处传参）与
   `internal/boot/team_runtime_host.go`（`RecoverTeamRuntime` 本就有 ctx，改为透传 1 行）。二者都不在
   Part A 独占清单、也不在冻结清单，改的是与 Part B 同域的任务服务/boot 宿主，无重叠风险。
2. **测试文件**：Part B 的 5 个新用例各写在自己的新 `_test.go` 里；此外为配合签名变更，在
   `internal/team/scheduler/runtime_test.go`、`scheduler_concurrent_test.go` 的既有调用点补了
   `context.Background()` 实参（语义不变）。未新增/未修改任何 Part A 的测试。
3. **lint/repolint**：本轮改动文件零新增；剩余红项均在 Part A 未提交的改动里
   （`chat_tui*.go`/`team_history_sync.go`/`team_replay.go` 的 essay 超限、`chat_tui_team_switch_test.go:79`
   的 SA4005）与既有的 `internal/provider/anthropic/messages_usage.go`，与本轮无关。
4. **观察到的偶发失败**：`TestTeamTurnInjectsInboxAtSubmit`（`chat_tui_team_inbox_test.go`，既有的
   Part A 域用例）在首次全包跑时失败一次，隔离跑与随后连续 5 次全包跑均绿；栈内无本轮改动文件，
   按铁律记录为偶发，不判定为本轮引入。

---

## 6. 施工约定

### 6.1 两段并行时的硬约束

1. **只编辑自己那份独占文件清单里的文件。** 需要改动对方文件时，不要动 —— 在回报里写明，
   由 leader 协调或留到合并后。
2. **新增测试写进自己新增的 `_test.go`**，不共用测试文件、不改对方的测试。
3. **冻结文件一个都不碰**（§4.1 第三张清单）。若认为非改不可，先回报再动。
4. **不新增 Update 分支**（Part A）：复用 `teamRosterRefreshMsg` 的既有载荷位，
   这是 `chat_tui.go` 的 complexity ratchet 要求的（见该文件既有注释）。
5. **`gofmt` 必须干净**；改动文件跑 `golangci-lint`（本机需用
   `$(go env GOPATH)/bin/golangci-lint run --timeout=5m ./...`，`make lint` 在本机不可用）。
6. **repolint 不得加宽 baseline**。新文件预算为 0，不要把浮动注释带进新文件；
   `function-size` 计入注释行数。

### 6.2 每段自己的验收门禁

两段都必须跑到全绿（在各自分支上）：

```bash
gofmt -l <changed files>                      # 必须为空
go build ./...
go vet ./internal/cli/ ./internal/team/... ./internal/workspacelease/
go test ./internal/cli/ ./internal/team/...   # 全绿
go test -race ./internal/cli/ -run 'Team|Cockpit|Replay|Roster' -count=2
```

两段都必须确认**既有并发用例未被削弱**：

```
TestTeamE2EAssignTaskToRelevantRunsBothMembersAndReportsBoth
TestTeamBackendsDistinctMembersAssembleInParallel
TestTeamHubLeaderAndMemberDoNotSerialize
TestCockpitSerializesPerMemberAndRunsMembersInParallel
internal/team/agentruntime/agentruntime_concurrent_test.go
internal/team/scheduler/scheduler_concurrent_test.go
```

### 6.3 已知的既有红灯与 flaky（不要误判为自己引入）

- `TestTeamHubLeaderAndMemberDoNotSerialize` 偶发失败：该用例靠 sleep 计时测并发，
  高负载下两个 goroutine 不同时在途。隔离跑 10/10 绿 —— 与本路线无关，
  但**新写的并发用例不要再用 sleep 计时**。
- `TestEscalationWakeNamesTheQueue`、`TestTerminalSizeRecoveryFollowsTheTerminalUnderTheFrame`
  存在既有测试内竞态（见 `TEAM_TUI_AGENT_DECOUPLING_PLAN.md` v0.12/v0.13 记录）。
- `internal/cli` 全包 `-race` 在本机可能因 inotify `max_user_instances=128`
  在 `internal/skill/skillwatch` 相关套件上变红 —— 环境因素，先在改动前的父提交上复现再判断。
- `internal/worktree` 需要 git ≥2.38，本机为 2.34.1，该包 30 个用例红，与本路线无关。

### 6.4 完成定义

单个节点完成的定义 = ① 代码落地；② 该节点的验收用例存在且先红后绿；
③ §6.2 的门禁实跑通过；④ §5 的该行状态与证据列更新。
**「第一版补丁」或「定向测试变绿」不等于完节点** —— 门禁受阻时继续做其他独立节点，
并在回报里写明缺失的证据。

---

## 附录 A：证据索引

### A.1 并行性（正面证据）

| 结论 | 位置 |
| --- | --- |
| 每成员独立 turn goroutine | `internal/control/turn_loop.go:350`、`internal/control/admission_submit.go:15` |
| 占用 per-member | `internal/team/agentruntime/runtime.go:35-38,102-108,167-173` |
| hub 锁不跨后端调用 | `internal/cli/team_hub.go:60-64` |
| 装配可并行 | `internal/cli/team_backends.go:166-212` |
| 事件非阻塞汇入 | `internal/cli/team_event_pump.go:1-30,52-56` |
| per-member worker | `internal/cli/team_member_cockpit.go:13-26` |
| **推理路径零互斥** | `internal/provider/schema_validate_args.go:17`、`internal/provider/retry.go:73`、`internal/provider/tool_search.go:17`（全包仅此 3 处同步） |
| e2e 并发语义 | `internal/cli/team_concurrency_e2e_test.go:138-143` |
| runtime 并发语义 | `internal/team/agentruntime/agentruntime_concurrent_test.go:19-44` |

### A.2 帧线程负担（Part A 依据）

| 结论 | 位置 |
| --- | --- |
| 重放内联读整段历史 | `internal/cli/team_replay.go:117` |
| follower 每次调用重读持久源 | `internal/cli/team_follower.go:198-205,646,667` |
| 经 tick 触发重放 | `internal/cli/team_history_sync.go:120,333-344` |
| tick 周期与内容 | `internal/cli/chat_tui_team_session.go:31,47-70` |
| reload 重读整份 team.json | `internal/cli/chat_tui_team.go:294-315`、`internal/team/teamstore.go:146-159`、`internal/team/storage.go:70-86` |
| 同 tick 两次 owner 指纹读 | `internal/cli/team_history_sync.go:54,172-185`、`internal/team/ownerstore.go:243-259` |
| 结算 turn 的 Binding 读 | `internal/cli/team_history_sync.go:214,306`、`internal/cli/chat_tui_team_switch.go:74-76` |
| 渲染路径目录遍历 | `internal/cli/chat_tui_team_render.go:601`、`internal/cli/chat_tui_team_reset.go:144-151` |
| 每帧全量渲染与 O(members²) | `internal/cli/chat_tui.go:1621-1640`、`internal/cli/chat_tui_team_render.go:408-432,512-571`、`internal/cli/chat_tui_team.go:470-483` |
| 已有缓存化先例（A4 可照抄） | `internal/cli/chat_tui_team_clear.go:168-199` |

### A.3 跨成员阻塞（Part B 依据）

| 结论 | 位置 |
| --- | --- |
| 所有成员同一 workspace root | `internal/cli/team_backend_build.go:425-426` |
| 每成员一个租约 Owner | `internal/boot/boot.go:553` |
| 租约目录为全局 | `internal/config/paths.go:353-362` |
| 租约获取/释放点（per 工具调用） | `internal/agent/tool_write_coordination.go:22-31`、`internal/agent/execute_one.go:41-46` |
| bash 取整工作区独占 | `internal/shellsafe/effect.go:46-48`、`internal/agent/tool_write_coordination.go:57-61,72` |
| 后台任务留住锁 + 30s 宽限 | `internal/boot/boot.go:565`、`internal/workspacelease/lease.go:25,435-457` |
| 等锁提示与退化结果 | `internal/boot/boot.go:553-561`、`internal/agent/tool_write_coordination.go:26-28` |
| stripe 碰撞 | `internal/workspacelease/scope.go:20,25` |
| filelock 写者优先 | `internal/filelock/filelock.go:34-37,230-241` |
| 跨 Owner 只排队不自死锁 | `internal/workspacelease/lease.go:608-618` |
| board 单写者与 5s 上限 | `internal/team/blackboard_sqlite.go:28-31,127-145` |
| 不可取消的 ctx | `internal/team/scheduler/runtime.go:72`、`internal/team/agentruntime/runtime.go:353` |
| `wakeMu` 跨 JSON 读 | `internal/cli/team_task_service.go:59-66,74-93,118`、`internal/team/agentruntime/runtime.go:384-391` |
| 进程级全局锁 | `internal/team/atomic.go:16`、`internal/memory/store_v2.go:38,157`、`internal/fileops/observation.go:336-338` |
| 非 leader 成员共享 memory 根 | `internal/cli/team_backend_build.go:364-372` |
| dispatch 路径同步 boot | `internal/cli/team_task_service.go:444`、`internal/cli/team_backends.go:171-257` |
| writer 工具串行 | `internal/agent/execute_batch.go:287-307,336-337` |
| 派发循环串行 | `internal/cli/team_member_tools.go:186-191` |
| 后端上限 4 与 LRU | `internal/cli/team_backends.go:41,292-314` |

### A.4 已排除项

| 结论 | 位置 |
| --- | --- |
| 审批不持租约、不传染 | `internal/agent/execute_one.go:73-74` |
| pump 不阻塞、buffer 有界 | `internal/cli/team_event_pump.go`、`internal/cli/chat_tui_team_switch.go:97` |
| 历史根因（共享阻塞通道）已修 | `TEAM_TUI_AGENT_DECOUPLING_PLAN.md` v0.10 |
| `checkStatus` 不在帧线程 | `internal/cli/team_status_poll.go` |

---

## 7. 双 Agent 并行修复方案评审与优化结论（2026-09-22）

### 7.1 总体判断

本路线适合采用“双 Agent 并行施工”，但“互不干扰”必须限定为**编辑边界基本不冲突**，不能解释成编译、语义和运行时资源完全独立。综合判断为：

- **拆分方向合理**：Part A 处理帧线程，Part B 处理执行层和共享资源，责任边界清晰。
- **文件级隔离基本成立**：独占文件清单没有明显直接重叠，适合从同一基线并行开发。
- **包级和运行时仍有耦合**：两段都涉及 `internal/cli`，并共享事件、任务、backend、board 和 workspace 资源。
- **合并后必须做跨段验收**：不能以两边各自定向测试通过，替代合并后的集成验证。

因此，本方案可以执行，但准确表述应为：**双 Agent 并行降低施工冲突，最终正确性由合并后的契约测试和并发门禁保证。**

### 7.2 各节点合理性复核

| 节点 | 评审结论 | 需要保留的约束或补充 |
| --- | --- | --- |
| A0 | 高优先级且必要 | tick 结果重复续期是实际乘数效应，应先于一般读盘优化处理 |
| A1 | 合理，收益明确 | 只异步化 tick 刷新路径；bind 路径继续保持同步 transcript 契约；必须保留 generation 丢弃 |
| A2 | 暂缓合理，但证据范围有限 | 当前 70 µs 测量是合成、本地稳态数据；后续应以真实慢磁盘、远端 follower 和 p95/p99 profile 重新判断 |
| A3 | 暂缓合理 | 不要用内存快照替代新鲜 binding 校验，否则可能为已删除成员重建 owner |
| A4 | 低风险、应保留 | 目录统计应在确认界面 arm 时完成，渲染函数只消费缓存 |
| A5 | 只消除 O(members²) 合理 | `bottomRows` 记忆化的收益不足以抵消失效条件复杂度，保留 profile 触发条件 |
| B1 | 设计合理 | `teamStore.Load` 移出 `wakeMu` 后仍需保留 leader 变更时的重定向语义 |
| B2 | 方向正确，风险中高 | 需要真实 SQLite/driver 取消验证；`Complete`/`Cancel` 的 `Background()` 残留必须在文档中持续标注 |
| B3 | 尚未闭环 | 已完成的是等待状态可观测，不是等待可取消；成员写工具的 deadline 和 TUI 状态渲染仍是后续工作 |
| B4 | 合理但有资源成本 | 按 roster 提高 backend 上限会增加 plugin/MCP 子进程和租约常驻量，应监控内存、进程数和启动时间 |
| B5 | 收益明确，语义风险中等 | 必须验证 durable task 顺序、单成员失败隔离、wake 次数/顺序和 board 竞争，不能只测装配峰值 |

### 7.3 “互不干扰”的四层边界

两 Agent 并行时应分别检查以下四层，而不是只看 Git 是否产生冲突：

1. **文本层**：是否编辑了对方的独占文件或冻结文件。
2. **编译层**：是否改变了跨包 API、构造函数、消息类型或接口签名。B2 已证明，scheduler 签名变化会穿透到清单外调用点。
3. **语义层**：是否改变了事件顺序、generation、失败回滚、leader wake 或 durable 状态的可观察行为。
4. **运行时层**：是否共同访问 workspace lease、SQLite board、team.json、memory store 和全局锁。

当前拆分主要解决了第一层，第二至第四层仍需合并后验证。尤其是 B5 的并行 fan-out 可能改变事件到达顺序，A1/A6 的 replay generation 和 TUI 消息回投必须作为跨段契约测试覆盖。

### 7.4 并行施工门禁

并行开始前：

- 固定共同基线 commit，并记录两段各自允许修改、禁止修改和冻结文件。
- API 签名、共享消息类型或跨段状态机需要变更时，先回报并由 leader 协调，不以“文件不重叠”为理由直接扩边界。
- 禁止对整个目录执行自动格式化；只格式化本 Agent 负责的文件。

各 Agent 独立完成：

```text
gofmt -l <changed files>
go build ./...
go vet <affected packages>
go test <affected packages>
go test -race <targeted concurrency tests>
```

合并后必须追加跨段验证：

- fan-out 同时触发 replay/roster refresh 时，历史 generation、消息回投和 transcript 不错乱；
- 两个成员同时完成时，wake leader 的重定向、board 写入和通知顺序正确；
- board 真实锁竞争下，调用方取消能够按约定返回，而不是只在 mock board 上通过；
- workspace lease 等待状态能够从后端事件传到 TUI，且文案不再把等待锁伪装成普通运行；
- 任一成员装配失败不回滚兄弟任务，也不产生重复 durable 记录或重复 wake；
- 全量 `go test`、`go vet`、定向 `-race` 及 lint 门禁通过，并将既有 flaky 与新增失败分开记录。

### 7.5 后续优先级调整

建议按以下顺序维护路线：

1. 保持 A0、A1、A4、A6、B1、B2、B4、B5 的已完成状态和现有证据。
2. 将 B3 明确保持为“进行中”，不要把“可观测”描述成“可取消”。
3. 对 B2 补真实 SQLite 取消/超时测试，并明确 `Complete`/`Cancel` 的不可取消持久化语义。
4. A2、A3、A5 继续采用 profile 驱动的延期策略，不因静态怀疑重新引入异步状态机。
5. A/B 合并后新增一组跨段并发回归用例，再决定是否启动预热 backend 或其他不在本轮范围的优化。

最终验收标准应从“两个 Agent 各自通过定向测试”提升为：**文件边界无越界、跨段 API 有记录、运行时并发契约可证明、合并后全量门禁通过。**

---

## 8. 并发是否影响「思考速度」——数据面判据（2026-09-23）

§2 已证明**仓库侧**无跨成员互斥（provider 路径零 mutex/semaphore、`maxParallel` 是 per-controller 局部量、
pump 非阻塞、无背压）。本节回答剩下的一半：**如果现场确实变慢，用现有数据能不能判读、怎么判读。**

### 8.1 结论先行：时间戳在数据面上不存在，TTFT 无法构造

| 面 | 是否有时间 | 证据 |
| --- | --- | --- |
| `event.Event` | **无时间字段** | `internal/event/event.go` 全部 Kind 无 TS |
| `trajectory.Record` | 有 `TS`（`internal/trajectory/recorder.go:27`），**但成员后端不写 trajectory** | 全仓 `trajectory.` 在 `internal/boot/`、`internal/control/` 零命中；只在 `internal/cli/run_sink.go:56` 由 CLI 的 `--trajectory` 装配 |
| `provider.Message`（落盘 transcript） | **无时间字段** | `internal/provider/provider.go:46-66` |
| `session.Manifest` | 仅 `CreatedAt` | `internal/session/store.go:67` |
| `owner` 元数据 | `UpdatedAt`/`CreatedAt`，**只在历史身份变更时推进** | `internal/team/ownerstore.go:96,104` |
| `board_events` | 有 `created_at` 列，**但全部为空** | 实测 `0001-01-01T00:00:00Z` |

因此「TTFT vs 并发成员数」这条曲线**无法从现有数据构造**。要从 transcript 反推只能退到文件 mtime，
那是「最后一次写入」而非「首次 token」。→ 该判据**需要先新增采集**（见 §8.5）。

### 8.2 现有唯一可用的判据：`stats` 的 `requests / usage 行`

`provider.Usage.RequestCount` 计数**逻辑流上每一个 HTTP 请求，含 header 重试与安全重连**
（`internal/provider/retry.go:91-99`），并被归集进 stats（`internal/stats/recorder.go:269`）。
故 **`requests / usage 行` 就是「每次逻辑请求实际发了几次 HTTP」**——直接回答「上游有没有在打回我们」。

### 8.3 实测基线（本机全部历史，2026-08-22 → 09-23，3,987 条 usage 行）

| 指标 | 值 |
| --- | --- |
| `requests / usage 行` | **1.036** |
| 其中 `requests == 1` 的行 | 3,883 / 3,987（**97.4%**） |
| `requests == 2` | 91 |
| `requests >= 3` | 13（最大 11） |
| 输入缓存命中率（全部） | **84.9%** |
| 含 ≥2 个不同 model 的分钟 vs 其余分钟 | `requests/行` **1.026 vs 1.051**；命中率 **87.9% vs 80.5%** |
| 最活跃日 09-22（1,990 行 / 3 model） | `requests/行` **1.028**，命中率 **92.9%** |

**判读：并发窗口的 `requests/行` 与命中率都不比单 model 窗口差**（命中率反而更高，是长会话的缓存效应）。
即**本机没有上游限流或配额打回的证据**：真有 429/5xx 重试时该比值会显著 >1。

**必须写明的读数陷阱**：`requests/行 = 24.158`（若按 turn 标记行算）**是错的**。
usage 行是**按流**记录的（一个 turn 内每次工具调用后都发一次请求、各记一行），
turn 标记行只标记 turn 边界。做基线时分子分母必须同口径。同理 `requests/行 = 1.036` 这个数
**不因该缺口而虚高**——缺失的恰好是「零请求即失败」的行，它们只会**拉低**分子。

### 8.4 现场判读表

| 观察 | 结论 | 依据 |
| --- | --- | --- |
| `requests/行` 稳定在 1.0x | 上游没有打回，本地也没有排队 | §8.3 |
| `requests/行` 随团队活跃度上升 | 上游在打回（限流/配额），看 429 与 `retry-after` | `internal/provider/retry.go:198-201` |
| `requests/行` 平稳但 UI 卡 | 帧线程，回到 Part A 残留项 | §3.1 |
| 某成员整个变慢/换模型 | 配额耗尽触发 failover，**不是限流** | `internal/provider/quota.go`、`internal/cli/team_failover.go` |

实时旁证：`Retrying` 事件（`internal/agent/agent.go:1339-1341`）带 attempt/max，现场能直接看到；
但它**不是受保护事件**（`internal/event/event.go` 的 `memberEventProtected` 名单里没有它），
队列溢出时会被淘汰，也不落账。

### 8.5 新增节点 T1：TTFT / 重试落账（独立立项，需拍板）

要让 §8.1 那条曲线存在，最小改动是在 agent 的流式入口记
「请求发出 → 首个 `Text`/`Reasoning` delta」的间隔，并作为 `Usage` 的旁路字段落进 trajectory 或 stats。

- **不属 Part A/Part B**：它是**新增可观测性**，不改并发语义、不动帧路径。
- **前置拍板**：① 落 trajectory 还是 stats；② 是否只在 `--trajectory` 开启时采集；
③ 字段是否进入 provider 可见前缀（**不应进入**，它只是遥测）。
- **验收**：一条用例证明该字段在正常流、重试流、以及「零 delta 即失败」三种路径下都被正确填写。

| 节点 | 主题 | 所属 | 状态 | 证据 |
| --- | --- | --- | --- | --- |
| T1 | TTFT / 重试落账 | 独立（新增可观测性） | **未开始（阻塞于拍板）** | 本节 §8.5 |
