# 分析报告与优化路线 —— Team 成员写工作区时的租约争用

本路线承接 `TEAM_MEMBER_PARALLELISM_ROUTE.md` 的 §3.2.1/§3.2.2 与 Part B 的 B3 残留，
把「多个成员同时写工作区」这条跨进程独占锁路径的争用、放大机制与优化次序写清楚。
与那份文档一样：**节点完成的定义是代码 + 先红后绿的验收用例 + 门禁实跑 + 本文状态表更新**，
不允许把「定向测试变绿」当作完节点。

---

## 1. 现象

Team 会话里成员并行工作时，成员侧（或主窗口）出现：

```
Another session is writing to this workspace; this session will continue automatically
when it is safe. workspace write lease is busy; read-only work remains concurrent
```

特征：

- 只出现在成员**调用写工具**期间，思考/流式推理路径上没有它；
- 一次争用每个成员各弹一次，不刷屏；
- 被挡的成员 turn 仍是 Running、UI 上没有工具结果 —— 看起来与「正在思考」完全同形；
- 团队越大、写得越密，出现得越频繁；单窗口串行使用几乎见不到。

## 2. 结论

**这条提示与「思考」无关，它只表示：某个成员此刻要写工作区，而另一个成员正持有写租约。**
推理层没有互斥（`TEAM_MEMBER_PARALLELISM_ROUTE.md` §2.2 已证），成员确实是并行 stream 的；
争用发生在**写工具的跨进程文件锁**上。

### 2.1 租约的语义单位

| 事实 | 位置 |
| --- | --- |
| 读者从不取租约；写工具的 hold 在工具返回时释放；后台任务有界保留 | `internal/workspacelease/lease.go:1-3` |
| 后台保留窗口 `backgroundGrace = 30s` | `internal/workspacelease/lease.go:25` |
| hold 覆盖整个工具执行（取在 Execute 之前、defer 释放） | `internal/agent/execute_one.go:329`、`internal/agent/execute_one.go:44` |
| 锁文件按 canonical worktree 根哈希，目录是**OS 用户级全局**的（刻意忽略 `REASONIX_HOME`） | `internal/workspacelease/scope.go` + `internal/config/paths.go:353-362` |
| 每个 boot 装配一个 `workspacelease.Owner`，`onWait` 发 `NoticeCodeWorkspaceLease` | `internal/boot/background_scope.go:14-42` |

### 2.2 获取链（谁取到什么级别的锁）

| 工具形态 | 判定点 | 取到的租约 |
| --- | --- | --- |
| hooks 可写工作区（见 §2.3 第 1 条） | `internal/agent/tool_write_coordination.go:55-58` | **整工作区独占** |
| 7 个内置写工具 | `internal/agent/path_bound_tools.go:150` 的白名单 | 路径级：canonical **shared** + 目录 tree **shared** + stripe **exclusive**（`internal/workspacelease/scope.go:265,271,279`） |
| 其余（bash、MCP、自定义 writer、不可静态证明） | 同上兜底分支 | **整工作区独占** |

独占路径：`HoldWrite` → `acquireWorkspace`（canonical root 取 **exclusive** + 其余 root shared，
`internal/workspacelease/lease.go:557-597`），因此**任何整工作区写都会挡住所有路径级写**。

失败—等待—通知的顺序：`filelock.TryAcquireModeWithKey` 返回 `ErrHeld` → `markWaiting()`
（`internal/workspacelease/lease.go:548`）→ 首次调用 `onWait` → 之后是**无界**阻塞等待
`AcquireModeWithKey(ctx, …)`（`internal/workspacelease/lease.go:737-761`）。

### 2.3 为什么在团队里被放大

1. **所有成员共用一把锁。** 每个成员都用同一个 workspace root 装配
   （`internal/cli/team_backend_build.go:426`），而 `boot.Build` 给每个成员单独建 Owner
   （`internal/boot/boot.go:556`）→ 同团队 N 个成员是 **N 个 Owner 抢同一组锁文件**。
2. **只有 7 个内置文件写工具是路径级锁，其他一律整工作区独占。**
   `pathBoundWriterNames`（`internal/agent/path_bound_tools.go:150`）**不含 bash**，
   所以 `go test ./...`、`npm run build`、`cargo check`、`make`、任何 MCP / 自定义 writer
   都取整工作区独占，并把锁**持满整场构建**。这是 §3.2.1 的核心。
3. **只要有任意写期 hook，就升格成整工作区独占。** `hook.Runner.ToolMutationHooksEnabled()`
   在 **Pre/PostToolUse/PostToolUseFailure 任一存在**时返回真（`internal/hook/runner.go:83-85`），
   `internal/agent/tool_hooks_mutation.go:7-17` 把它当布尔用 —— 一个纯日志 hook 也能让每次写工具
   变成整工作区独占。这是当前最大的放大器。
4. **写者优先会连带阻塞不相关的写。** `filelock` 的进程级队列里，只要同 key 有排队的 writer，
   `ModeShared` 获取一律被拒（`internal/filelock/filelock.go:227-232`、`:252-257`）。
   于是一个成员排队等整工作区独占，会连带挡住另一个成员对**不相关文件**的路径级写。
5. **后台任务把锁留住。** `boot.go:562` 装了 `jobs.WithJobStartObserver(workspaceLease.RetainUntil)`，
   工具结束后已完成的 hold 会继续活到后台任务结束 **再加 30s**（`internal/workspacelease/lease.go:436-457`）。
   一个成员起的 dev server 会在该窗口内持续挡人。

### 2.4 症状对照表（现场定位用）

| 观察到的 | 最可能的原因 |
| --- | --- |
| 提示 detail 是通用回退文案 | 等待瞬间 `Owner.State()` 没报在等待中，或该构建早于 Part B 的可区分通知 |
| 成员卡很久、无工具结果 | 等的是整工作区独占（bash/构建），且工具 ctx 无 deadline → 无界等待 |
| 一个成员跑构建时全员写不动 | §2.3 第 2 条：bash 整工作区独占 + 持满构建 |
| 只在配了 hooks 的会话里频发 | §2.3 第 3 条 |
| 某个成员起的服务还在跑时反复被挡 | §2.3 第 5 条：`backgroundGrace` |
| 两个成员写不同文件也互相挡 | §2.3 第 4 条（写者优先连带）或 stripe 哈希碰撞（概率见 L9） |

## 3. 已落地与残留

- **已落地（Part B B3-1）**：`internal/boot/workspace_lease_notice.go` 的 `workspaceLeaseWaitEvent`
  从 `workspacelease.Owner.State()` 把等待分类成「整工作区」或「某文件」，沿用
  `event.NoticeCodeWorkspaceLease`；证据 `internal/boot/workspace_lease_wait_notice_test.go`。
- **残留（Part B B3-2）**：工具级租约等待没有 deadline（`internal/agent/tool_write_coordination.go`
  的 ctx 来自工具调用，无界）。见本路线 L8。
- **已落地（本路线 L1–L5、L3b）**：等待通知指名持有者（L1）、hooks 按写面取租约（L2）、
  `backgroundGrace` 30s→3s（L3）、驻留 hold 在队友等待时立刻让锁（L3b）、成员写批量纪律（L4）、
  团队写令牌 + 派发时预约报告（L5）；每节点的证据与红→绿记录见 §6 状态表。
- **已落地（M0）**：租约现场可观测量 + 诊断输出——等待次数、等待时长分布（桶上界）、被挡锁域（按目标域与按锁层次两视图）、
  超时数、进行中等待数、被收回的保留贷款数；出口为 `Owner.Metrics()/MetricsReport()`、`control.Controller.WorkspaceLeaseMetrics()/WorkspaceLeaseDiagnostics()`
  与会话诊断导出的 `workspaceLeaseDiagnostics` 字段。口径与局限见 §5。
- **残留（渲染）**：把等锁成员渲染成「等待工作区」需要 Part A 独占文件
  （`chat_tui_team_render.go` / `chat_tui_team.go`），本路线只提供可区分的事件载荷；
  团队写令牌的等待 notice 同理（`event.NoticeCodeWorkspaceLease`，文案已指名队友与范围）。

---

## 4. 优化路线

### 4.0 分层原则

三层杠杆，收益与成本反向：

1. **减少假冲突** —— 扩大真正能并行的集合（L2、L6）；
2. **减少排队者** —— 让成员在派发时就避开冲突，而不是在工具调用里被动等（L5）；
3. **缩短持锁时间** —— 收窄保留窗口与调用次数（L3、L4）。

**排序原则**：先做「不改安全模型、不碰跨进程语义」的项；任何放宽 `EffectUnknown → 独占`
的改动（L6）必须先给出等价安全论证，否则不做（沿用 `TEAM_MEMBER_PARALLELISM_ROUTE.md` B3-3）。

### 4.1 阶段 P0 —— 廉价且确定性高

#### L1（P0）等待通知「指名道姓」

- **问题**：§2.4。`Another session is writing` 在团队里是误导：挡住你的其实是**同一实例里的兄弟成员**。
- **做法**：让租约携带持有者身份。锁文件当前是空的，在租约目录旁写一个身份 sidecar
  （pid、成员名、scope、起始时间、命令族），`onWait` 的事件 detail 由「整工作区/某文件」升级为
  「成员 alice 正在整工作区跑 `go test ./...`（已 12s）」。身份来源：`boot.Build` 已知成员名，
  `Owner` 增加一个可选的 `SetIdentity`。
- **验收**：真实双 Owner 争用下，等待事件里出现持有者成员名与 scope、且不包含路径之外的敏感信息；
  无身份时回退到现有分类文案（不回归 `workspace_lease_wait_notice_test.go` 的断言）。
- **风险**：低。注意 sidecar 的写入必须在**取锁成功后**、不得成为新的争用点；
  身份是诊断信息，**不得**用于任何安全判定。
- **依赖**：无。

**落地收窄（2026-09-22，实测驱动）**：最初的实现对「每次成功获取的每个锁文件」写记录，
在 `BenchmarkUncontendedPathHold` 上把无争用路径写从 ~510µs 抬到 ~1.40ms（2.6×）——
一次路径写会触碰约 20 个锁文件（祖先目录、目录树、stripe，以及每个 queued 模式的 `.queue`）。
最终只发布**两类**记录：① 每次**独占**获取（任何写者都可能排在其后）；② **工作区根锁**上的获取
（不区分模式）——文件写者在根锁上的 shared 持有正是整工作区写者排队的原因。其余（`.queue`、
祖先目录的 shared、更深的目录树 slot）不发布：无人会读。收窄后实测 ~660µs（+150µs，约 +29%）。
命令族（`go test ./...`）**未做**：租约层拿不到它，且唯一来源是 `internal/agent/**`（本轮冻结），
已记为残留。

#### L2（P0）hooks 不再把每次写工具升格成整工作区

- **问题**：§2.3 第 3 条。`ToolMutationHooksEnabled()` 的布尔语义让「有 hook」等于「可能写任何地方」。
- **做法**：把布尔改成 **scope**（新增能力接口，保留旧布尔作为保守回退，避免破坏自定义 `ToolHooks` 实现）：
  1. hook 命令用既有 `shellsafe.ClassifyBash` 分类：能证明为读者（`echo`、`git status`、`jq` 读等）
     的 hook 不扩权；
  2. 形如 `gofmt -w $FILE` / `prettier --write` 的格式化 hook，用**该工具自身的文件路径参数**
     （`extractWritePathsFromArgs`，`internal/agent/path_bound_tools.go:278`）作为 scope，
     走 `HoldWriteForPaths`；
  3. 分类不出、或 hook 配置本身声明为「任意写」（如调用脚本/网络）则回退整工作区独占。
- **验收**：① 纯日志 hook 下路径级写工具仍取路径级租约（用租约探针断言 scope 标签）；
  ② 格式化 hook 下 scope 等于该工具的目标文件；③ 无法分类的 hook 仍是整工作区（安全回退用例）。
- **风险**：中（安全面）。必须 fail-closed：只要不能证明 hook 是读者或定点写者，就保持现状。
  不允许出现「因为 hook 看起来无害所以不取锁」的分支。
- **依赖**：`internal/hook`（新增能力查询）与 `internal/agent/tool_hooks_mutation.go`（改调用方）。

**落地实现（2026-09-22）**：

1. **hook 侧分类**：`internal/hook/writesurface.go` 的 `Runner.ToolCallWriteSurface(toolName)`，
   判定顺序是**证明优先、声明补位**：① 插件 `contextFile` hook 不写任何东西；
   ② `shellsafe.StaticWritePaths` 给出「完整写面」时就按字面路径算（目前只覆盖 `echo`/`printf` + 字面重定向）；
   ③ `shellsafe.ClassifyBash` 为 `Known` 且 `Writes == 0` 的读者不扩权（含 `FOO=1 grep …` 这类 env 前缀）；
   ④ 上述都证不出时，才看 hook 自己的 `writeScope` 声明：`"none"`（不写）/`"tool"`（只改本次调用指名的文件）；
   ⑤ 其余一律整工作区。多个 hook 聚合时取**最宽**的那个。
2. **agent 侧决策**：`ToolHookWriteSurface` + `writeLeaseScope`（`internal/agent/tool_hooks_mutation.go`）
   把写面翻成租约范围；`workspaceWritePaths` 把字面目标按工作区根过滤（**写到 /tmp 的日志 hook 不再需要工作区租约**）。
   声明为 `tool` 但本次调用的文件路径取不出来时**失败关闭**（整工作区），而不是拿一个可能不覆盖的范围去持锁。
   调用方 `tool_write_coordination.go`、计划字段在 `tool_call_plan.go`，`execute_batch` 与进程内写调度仍保守（见残留）。
3. **分层**：`internal/agent` 刻意不 import `internal/hook`，所以适配器放在 boot：
   `internal/boot/hook_lease_surface.go` 把 runner 的写面转成 `agent.ToolHookWriteSurface`，在 `boot.go` 的主 agent 装配点接线。
   未装适配器的路径（自定义 `ToolHooks` 实现）回退到旧布尔 → 整工作区。

**与原文的偏差（必读）**：原文 ② 要求「静态识别出 `gofmt -w $FILE` 这类格式化 hook 并按其文件参数定范围」。
实测不可行且**不可能安全**：hook 的路径来自 stdin 上的 JSON payload（常见写法是 `jq … | xargs gofmt -w`），
静态分析无法证明操作数只来自 path 字段（`jq -r .tool_input.old_string` 也能喂出一个任意文件名）。
因此②改为**显式声明**：`writeScope: "tool"` 表示「这个 hook 只重写本次调用指名的文件」，
由用户为自己的 hook 作保；且声明只能**收窄**无法证明的 hook，一旦 `shellsafe` 证出具体的写路径，**证明赢**（不可能用声明抹掉证据）。
默认（不声明）保持现状：整工作区。

**冻结文件偏差**：本节点改了 `internal/agent/tool_write_coordination.go`、`tool_hooks_mutation.go`、
`tool_call_plan.go`、`services.go`、`agent.go`（L2 的依赖原本就点名了 agent 侧改调用方）；
**`execute_one.go` 一行未动** —— 计划里派生的 `hooksMayMutateWorkspace` 保住了它那两个消费者（checkpoint gap 与进程内写调度）的语义。
冻结理由（Part A 并行避让）已随 Part A 提交（`95b01b599`）失效，详见 §5.2。

**残留**：① `execute_batch` 的「有 hook 就不并行」仍保守（安全侧，不改）；
② 进程内写调度对有界 hook 写仍按 opaque/整工作区预留（另一个机制，同样保守）；
③ 子 agent 不继承 hooks（`task_options.go` 不传 `Hooks`），因此没有可度量的写面；
④ hook 写面的分类结果不影响任何权限判定，只影响租约宽度。

#### L3（P0）`backgroundGrace` 从 30s 收窄

- **问题**：§2.3 第 5 条。
- **做法**：把 30s 常量改为按 scope 判定的策略：仅当该后台任务确实写过该 scope 时保留，
  否则立刻释放；无法判定时用显著更短的窗口（2–5s）。保留仍必须遵守
  「运行中/挂起提示的后端永不退休」的既有约束（`internal/cli/team_backends.go` 的 `evictOverCap`）。
- **验收**：① 后台任务结束后 hold 在窗口内释放（复用 `background_grace_test.go` 的夹具）；
  ② 反复「写一次 → 再写同一路径」的重取成本有基准数据（见 §5），证明没有引入抖动。
- **风险**：中。放宽会提高重取频率（每次取锁 = 打开锁文件 + flock），必须以基准数据支撑。
- **依赖**：§5 的度量先到位。

**落地实现（2026-09-22，先度量后决定）**：

1. **探针**：新增 `internal/workspacelease/background_grace_window_test.go`。
   `TestRetainedWindowMatchesTheConfiguredGrace` 直接**量**出窗口：会话在驻留任务下发呆后，租约保持在 `[grace/2, 5×grace]` 内释放
   （用轮询+上下界，不用 sleep 计时口径）；`TestBackgroundGraceStaysInTheDecidedBand` 把决定写成断言（窗口必须在 2–5s）。
2. **重取成本基准**（同一台机器、同一基准口径，300x×3）：
   `BenchmarkUncontendedPathHold` ~0.51ms/op（完全重新获取）vs 新增的 `BenchmarkResidentJobHoldReuse` ~0.50ms/op
   （租约被保留、同路径重写时直接复用 hold）—— **保留窗口在成本上几乎买不到东西**：
   两者都被 `pathSpecs` 的 identity 解析与 Stat 主导，差在噪声以内。
3. **决定**：`backgroundGrace` 从 **30s → 3s**（取 2–5s 带的偏保守值：足量覆盖写入突发，又比旧值少一个数量级）。
   成本模型：一个开着 dev server 的成员每释放一次写工具调用，队友最长等 `grace`；20 次写 = 旧值 600s vs 新值 60s 的额外独占。
   反过来，提前释放的代价是**一次 ~0.5ms 的重取**（或者压根不需要），差额 3 个数量级。
4. 保留机制一字未改（驻留任务在跑 → 窗口；任务结束 → 立即释放），仍是「有上界的保护」而不是「任务期间全程保护」的性质 ——
   旧值 30s 同样不覆盖整场 dev server，所以这次收窄不减少任何**完整**保证。

**为什么没做 scope 感知（原文的「仅当该后台任务确实写过该 scope 时保留」）**：后台任务由 bash 启动，
launching call 持的就是**整工作区**，按域快照的快照集就是「全部」——对团队场景零收益。
真正的「按需让锁」（队友正在等就把保留的 hold 立刻交给它）已在 **L3b** 落地：
原文把它记为「需要 `filelock` 暴露同进程等待者」是**误判前提**——被挡方在 `acquireMode` 里本来就看得见
`filelock.ErrHeld`，「本进程内谁占着这个域」的判断在 workspacelease 内部即可闭环，`filelock` 一字未改。

#### L3b（P0）驻留 hold 在队友等待时立刻让锁

- **问题**：§2.3 第 5 条的后半段——保留窗口本意是省一次 ~0.5ms 的重取，却让队友**等满窗口**。
  L3 已经把收益/代价量化清楚（差三个数量级），所以窗口只能是「没人需要时」的省钱手段，
  不能是「有人需要时」的挡路石。
- **做法**：把保留的 hold 变成**贷款**，三个动作：
  ① 被保留（`refs==0`）的 hold 发布它占用的锁文件（进程内注册表，键 = 锁文件路径）；
  ② 任何 Owner 在 `acquireMode` 撞上 `filelock.ErrHeld` 时，先按该锁文件路径收回本进程内尚未结束的贷款，
  带同一 key 重试一次，成功就直接返回（不进入排队、不产生等待通知）；
  ③ Owner 因为**自己**的保留 hold 而等待时（`hasPathHoldsLocked` / `pathOrderAllowedLocked`）同样立刻收回。
- **验收**：① 队友在保留窗口内取得同一路径，且**没有**产生等待通知；② 正在被工具使用的 hold（`refs>0`）任何人都拿不走；
  ③ 收回后注册表无残留，被收回的路径仍能被后续写者正常取得；④ 跨锁域 / 跨工作区不受影响；
  ⑤ 取消语义不被削弱（取消仍报 `context.Canceled`）；⑥ 后台任务在同一窗口内**再次**完成的 hold 也要能被收回。
- **风险**：低——只缩短保留时长，从不放宽写面（安全方向是单向的）。唯一的危险是误放「活」的 hold，
  因此判定必须以 `refs==0` 为准（验收 ②）。
- **依赖**：L3（同一机制的另一半：先收窄窗口，再让窗口可被提前收回）。

**落地实现（2026-09-22）**：

1. **贷款注册表（`internal/workspacelease/yield.go`，新）**：`retainedLocks` 把锁文件映射到
   「本进程内为**已完成的工具调用**继续持有它的 Owner」。成员资格 == 「存在一个 `refs==0` 的 hold 覆盖该路径」，
   所以收回永远安全：它只会放开**没有调用者在使用**的 hold。
   发布/收回都只在 `o.mu` 下成对发生（`publishRetainedLocked` / `retractRetainedLocked`），注册表不可能与 hold 集合不一致；
   `yieldRetainedOn` 先在注册表锁下**收齐** Owner 再逐个取 `o.mu`，与发布方向的锁序相反但不交叉持有，因此不存在锁序反转。
2. **被挡方先收贷款、再排队（`lease.go` 的 `acquireMode`）**：命中 `filelock.ErrHeld` 后先问一次注册表，
   有贷款就收回并重试一次；这一步发生在**读持有者记录之前**，所以身份（L1）依旧完全不参与租约判定。
3. **自己等自己同样收回（`scope.go` / `lease.go`）**：`HoldWrite` 等自己的路径 hold 结束、
   `HoldWriteForPaths` 等自己的 stripe 顺序，两个等待循环都先尝试收回自己的贷款，
   避免「为了省 0.5ms 让自己等 3s」。
4. **一处必须补的漏洞**：窗口是**第一次** hold 完成时点着的，此后再完成的 hold 不再经过点火处，
   于是那次完成的 hold 变成「谁也收不回的贷款」。修法是把发布放进**hold 完成的唯一漏斗**（`collectInactiveLocked`），
   而不是放在窗口点火处；这一条有专门的用例（验收 ⑥）。
5. **热路径成本**：`cancelGraceLocked` 现在每次成功取锁都会跑到（`addHoldLocked`、`HoldWrite*` 成功路径、`BeginRun`），
   因此收回改为按「本 Owner 实际发布过的路径」精确删除：未发布过（= 没有后台任务的会话）时只是一次 `len()==0` 判断，
   不触碰进程级互斥锁。实测见 §5.1。
6. **文件拆分**：保留/窗口机制移入 `internal/workspacelease/retention.go`（`lease.go` 原本已 853 行，逼近仓库的文件长度预算），
   让锁注册表留在 `yield.go`。

**残留**：让锁只在**进程内**成立——另一个 Reasonix 进程的保留窗口照旧（注册表是进程内的，也无法去问对端）。
这不是这次偷懒：跨进程的持有者同样无从得知对端在等（§2.3 第 1 条的同因），
跨进程收敛只能靠 L8 的有界等待，把「等满窗口」变成「有上界地等」。

#### L4（P0）减少取锁次数（工具可用性层）

- **问题**：`write_file` 连续 N 次 = N 次取锁；成员工具调用越碎，争用越大。
- **做法**：① 在成员 system prompt/工具注入中固化「同一文件多处改动用 `multi_edit`/`edit_file`」
  的纪律（承接 `AGENT_OPTIMIZATION_TECHNICAL_ROUTE.md` 的 D5 方向）；② 复核 `write_file` 的
  指导文案，让模型自然地少调用几次。
- **验收**：工具选择统计/用例证明同一文件的多处修改走 `multi_edit` 而非多次 `write_file`
  （团队 system prompt 的既有测试套件里加断言）。
- **风险**：低（提示词层）。不改任何锁语义。
- **依赖**：无。

**落地说明（2026-09-22）**：只做了①，位置是「共享纪律」而不是成员专属文本，因为 leader 也会写文件。
②（复核 `write_file` 的指导文案）**未做**：工具描述在 `internal/agent/**`，本轮冻结。
另：提示词只能减少调用次数，不能保证；真正的减少来自 L5 的团队写令牌 + 写路径预约。

### 4.2 阶段 P1 —— 结构性（收益最大）

#### L5（P1）团队写令牌 + 写路径预约：用调度代替「发现冲突」

- **问题**：Part B 的 B5 之后 fan-out 让 N 个成员**同时**开始，于是 N-1 个几乎同时撞锁；
  争用被发现得太晚（在工具调用里），成员只能干等。
- **做法**：
  1. **团队写令牌**：同一时刻只让一个成员去尝试跨进程租约，其余在**进程内**排队。
     进程内已有 `writeScheduler`（`internal/agent/services.go:91-93`）与
     `ReserveParentWrite`/`Realize`/`MarkOpaque`（`internal/agent/tool_write_coordination.go:76-108`），
     令牌是它在团队层的门面。
  2. **写路径预约**：装配阶段就用 `extractWritePathsFromArgs` 算出各 subtask 的写路径，
     互不相交的成员直接并发，相交的才排队 —— 让争用在**派发时**解决。
  3. 预约结果对成员可见（「你在等 alice 写完 `internal/team/x.go`」），便于模型改做只读工作。
- **验收**：① N 个成员的 subtask 写路径互不相交时全部并发进入写工具（探针统计并发峰值，**不使用 sleep 计时**）；
  ② 写路径相交时只有一个成员进入跨进程取锁，其余在进程内排队且顺序稳定；
  ③ 现有 fan-out 失败语义不回滚（不回归 `team_fanout_assembly_test.go`）。
- **风险**：中高。令牌是**性能优化**，不是安全边界：它失效时必须能退化为现状（每个成员仍各自取跨进程锁），
  因此令牌只能减少尝试次数，**不能**替代 `filelock` 的互斥。写路径解析失败（未知工具）必须按
  「可能写任何地方」处理。
- **依赖**：L1（可见性）；与 `TEAM_MEMBER_PARALLELISM_ROUTE.md` 的 B4/B5 顺承。

**落地实现（2026-09-22）**

1. **团队写令牌**：`internal/agent/write_intent_token.go` 的 `WriteIntentToken`（按 `(team, workspace root)`
   注册一次，见 `internal/cli/team_write_token.go` 的 `teamWriteIntentTokens`）。成员在**取跨进程租约之前**先取
   进程内 intent，释放顺序相反（`internal/agent/tool_write_coordination.go` 的 `acquireCallWriteGuard`）。
   重叠判定**复用** `ScheduleOverlaps`（`write_claims.go`），与写调度器同一套语义，因此「令牌放行的并发」
   与「调度器认为可并发的」不会两套真理。FIFO 是「**相遇才排队**」：不相交的 intent 可以越过队列里被挡的
   成员（`firstBlockerLocked`），相交的严格按到达顺序放行。等锁可被 ctx 取消，取消后会重新 sweep，
   不会把排在其后的成员吊死。
2. **成员可见**：约定「`onWait` 在入队时同步回调」，agent 侧把它落成 `event.NoticeCodeWorkspaceLease` 的
   notice（`noteWriteIntentWait`），文案指名成员与范围（「Waiting for member m1 of team alpha to finish
   writing internal/team/x.go」）。这是 L1 的进程内对照物：L1 负责跨进程持有者，令牌负责同进程队友。
3. **写路径预约 —— 落地为「派发时的可见性」，不是互斥**（原文第 2 条按可实现的部分改写）：
   `fanoutWriteAreas` 从 fan-out 的共享 subtask 文本里抽出**仓库相对源码路径**（`subtaskWriteAreas`：
   需有 `/`、已知源码后缀、无 shell 元字符；绝对路径/URL/glob 一律拒绝），写进
   `leader_assign_task_to_relevant` 的返回行。为什么不做成「按预约排队」：fan-out 现在把**同一个 subtask**
   发给 N 个成员（`team_member_tools.go`），各自的实际写路径由成员运行时的工具调用决定，静态预约既不可能
   区分成员、也不能作为互斥依据（预约错一个文件就会**错误地**阻止一次并发写）；而「按预约串行派发」比
   令牌更保守（会把成员的读/思考也串行化）。所以预约只做它唯一安全的事：**把争用告诉 leader**，
   让它在派发时就把成员分到不同文件。
4. **fail-open**：无令牌（未命名团队 / 空 workspace root）、令牌报错、子 agent（`task_options.go` 明确不继承，
   否则子写会排在父 intent 后面 —— 父等子返回，死锁）都退化为「各自取跨进程租约」，与 L5 之前一字不差。
   无法归一化的 intent（工作区外路径、glob、空 scope）**claim 整工作区**：多排队的代价远小于让两个写者同时进去。

#### L6（P1）产物通道：可证明只写构建产物的命令不再独占整工作区

- **问题**：§2.3 第 2 条 —— 一个成员构建，其他所有成员的写被挡住整场构建。
- **做法**：
  1. `internal/shellsafe/effect.go` 的 `WriteDomain` 增加 `WriteBuildArtifacts` 域
     （当前 `AnyMutation()`/`WorkspaceMutation()` 把 `EffectUnknown` 也算作 mutation，见 `:45-48`）；
  2. `workspacelease` 增加第三条通道：canonical root 取 **shared** + 产物目录
     （`target/`、`dist/`、`.next/`、`node_modules/.cache`、`build/`…）的 tree lock 取 **exclusive**。
     交集语义已天然成立：路径级写只取 canonical shared + 目录 tree shared + stripe exclusive
     （`internal/workspacelease/scope.go:265,271,279`），所以产物目录独占与 `internal/foo.go`
     的编辑互不相交；而整工作区写（未知命令/不可证明）依旧挡住一切。
- **安全前提（不可跳过）**：confinement 必须**能证明**才放行。
  - 可以：`go build`（无 `-o` 落回工作区）、`go vet`、`go list` 等不执行仓库代码的构建；
  - **不可以**：`go test`、`cargo check`（跑 `build.rs`）、`npm run build`（跑任意脚本）——
    它们执行仓库代码，可以写任何地方。对这些必须**保持现状**或要求显式 opt-in。
  - opt-in 形态：项目配置声明「这些构建命令只写这些目录」+ 允许并行；或把命令放进
    worktree/overlay 沙箱（见 L7），一旦有沙箱 confinement 就重新可证明。
- **验收**：① 可证明的构建命令取产物通道（scope 标签为产物目录，canonical 为 shared 的断言）；
  ② 与它并发的路径级写**不被阻塞**（真实双 Owner 探针）；
  ③ `go test`/`npm run build` 等不可证明命令仍取整工作区独占（安全回退用例）；
  ④ 整工作区写仍能挡住产物通道（交集用例）。
- **风险**：**高（安全面）**。这是本路线唯一会放宽安全门节点的改动；无等价安全论证**不做**。
- **依赖**：§5 度量（证明收益真实）；L2 的分类基础设施。

### 4.3 阶段 P2 —— 架构与后期

#### L7（P2）每成员一个 worktree：从根上消掉争用

- **问题**：只要所有成员共享一个 root，跨进程租约就一定会在写密集时互相挡。
- **做法**：让成员各自在 git worktree 里干活，写冲突退化为「各自目录」，
  只在合流时对主 worktree 取一次锁。仓库里已有先例：`workspacelease.HoldWriteRoots`
  （`internal/workspacelease/roots.go:27`）与 `desktop/delivery_worktree.go:566` 的多 root 租约。
- **验收**：多成员在同一交付里各自 worktree 并行写、互不取对方 root 的锁（探针）；
  合流路径只在合并点取锁且失败可重试。
- **风险**：高。需要重做成员的目录归属、合并/评审流程、证据与权限路径；成本远超本路线其他项。
- **依赖**：L5 可用作过渡；需要产品拍板。

#### L8（P2）等锁有界 + 超时可见（Part B B3-2 残留）

- **问题**：工具级取锁可以无限等；提示文案承诺「会自动继续」但在 ctx 无 deadline 时没有上界。
- **做法**：给成员写工具路径的租约等待一个有界 deadline（可配置），超时按既有的
  `blocked:` 结果返回给模型，使其能改做只读工作或换路径；同时把「等了多久、被谁挡」写进事件。
- **验收**：用例证明等待到 deadline 后立刻返回 blocked 结果（不是无限挂起），
  且失败路径不产生持久化的幽灵状态（不回归 `workspace_lease_regression_test.go`）。
- **风险**：中高。把「等一等就成功」变成失败可能改变成功率的分布；值必须有依据
  （不与既有 `busy_timeout` 类上限打架），且必须记录新的失败可见性。
- **依赖**：L1（先能指名再决定是否需要超时）。
- **注**：需要改 `internal/agent/tool_write_coordination.go`/`execute_one.go`；本轮冻结，
  动之前先报备（`TEAM_MEMBER_PARALLELISM_ROUTE.md` §6.1 第 3 条）。

#### L9（P2，评估项）写者优先的连带阻塞与 stripe 碰撞

- **问题**：§2.3 第 4 条；以及 4096 条 stripe / 4096 条 tree slot 的哈希碰撞
  （`internal/workspacelease/scope.go:20,25`）—— N 个成员同时写不同文件时碰撞概率约 N²/8192
  （N=20 时约 5%，**不是主因**，随团队规模增长）。
- **做法**：① stripe 改「按目录挂锁文件」可消除碰撞，代价是锁文件数量随目录增长
  （注释里说明这是刻意避免的），仅在碰撞被实测为瓶颈时做；
  ② 写者优先的 bypass 预算：**评估后大概率不做** —— canonical root 是所有路径写共享的 key，
  放宽会让整工作区写手饿死，风险/收益比最差。
- **验收**：先给出实测数据（`tryAcquireLocal` 被 `waitingWriters` 拒的次数与等待时长分布），
  有数据再立项。
- **风险**：高（公平性/饥饿）。默认不做。

### 4.4 明确不做

| 项 | 为什么不做 |
| --- | --- |
| 放宽 `EffectUnknown → 整工作区独占` | 安全设计（`internal/shellsafe/effect.go:45-48`），无等价安全论证不做（沿用 B3-3 与 §4.4 结论） |
| 取消 hooks 的不可写性判定 | hook 是用户 shell 代码，只能收窄 scope，不能免除判定 |
| 让共享获取无视排队的 writer | 会饿死 writer，见 L9② |
| 改 board/SQLite 或 `internal/agent/**` 的其他并发模型 | 不属本路线域；`internal/agent/**` 已于 L2 解冻，按节点立项并在 §5.2 登记 |
| 用写路径预约做**互斥**（或按预约串行派发） | 预约来自自由文本 subtask 的启发式，且 fan-out 下 N 个成员拿的是同一个 subtask；拿它阻止一次真实写就是错锁（见 §4.2 L5①。进程内互斥归 `writeScheduler`，跨进程归 lease） |

---

## 5. 度量与埋点（先有数再改）

改动前必须先有基线，否则 L2/L3/L6 的收益无法判定：

1. **基准**：以 `internal/workspacelease/lease_benchmark_test.go` 与
   `parallel_fixture_test.go` 为夹具，新增「N 个 Owner × M 次写工具 + 1 个长构建」的场景，
   输出：等待次数、等待时长 p50/p99、被挡 scope 的分布、被 `waitingWriters` 拒的次数。
2. **计数器**：`Owner` 上暴露（仅测试/诊断用）等待次数、等待总时长、超时数、当前 scope 标签。
3. **现场信号**：`event.NoticeCodeWorkspaceLease` 已是天然埋点；L1 落地后它同时携带持有者身份，
   可直接回答「是谁挡住了我」。
4. **验收口径**：每个节点必须给出**改动前后同一基准**的对比数字，否则状态只能记「已落地，收益未量化」。

**已落地（M0）：现场可观测量与诊断输出**

`internal/workspacelease/metrics.go` 把「等锁到底花了多少」变成可读的结构，而不是只能靠日志回味：

| 量 | 含义 | 取值口径 |
| --- | --- | --- |
| `Waits` | 等锁次数 | **一次取租约 = 一次写工具调用**；同一次获取在多个锁文件上排队只记一次 |
| `P50/P90/P99Upper`、`Max`、`Waited` | 等待时长分布 | 直方图桶上界：1ms/10ms/100ms/1s/10s/1m/10m +溢出桶；**是上界不是采样值** |
| `Scopes[]` | 被挡锁域（按等待方需求看） | 目标域：`whole workspace` 或文件基名；每个域带次数/超时数/总时长/最大值 |
| `Locks[]` | 被挡锁域（按层次结构看） | `workspace`（根锁）/ `ancestor`（祖先兼容锁）/ `queue`（写者优先队列锁）/ `directory`（目录树 stripe）/ `file`（路径 stripe） |
| `Timeouts` | 超时数 | 排队期间 ctx 被取消/到期（`context.Canceled`/`DeadlineExceeded`）；其它错误算失败不算超时 |
| `InFlight` | 正在进行中的等待 | 现场卡住时能看见「有人正在等」，而不是只有事后统计 |
| `Repaid` | 被收回的保留贷款 | 记在**出借者**账上（只有它能区分「服务了一次等待」与「根本没人等」）；被服务的一方 `Waits == 0` |

**出口（现场怎么拿到）**：① `workspacelease.Owner.Metrics()` / `MetricsReport()`；
② `control.Controller.WorkspaceLeaseMetrics()` / `WorkspaceLeaseDiagnostics()`（Desktop/CLI/serve 都能读）；
③ 会话诊断导出（`WriteSessionDiagnostics`）新增字段 `workspaceLeaseDiagnostics`，用户导出的诊断包直接带这段文本。
报告样例（`MetricsReport()`，无等待时是一行「no waits recorded」）：

```
workspace lease: 12 waits, 1 timeouts, 0 in flight, 3 repaid loans
  waited 4.2s total, max 2.1s, p50 <=100ms, p90 <=1s, p99 <=10s
  blocked scopes: whole workspace 9 (waited 4.1s, max 2.1s, 1 timeouts); internal/team.go 3 (waited 90ms, max 60ms, 0 timeouts)
  blocked locks: queue 8 (1 timeouts), workspace 5 (0 timeouts), file 3 (0 timeouts)
```

**刻意写明的口径与局限**（不写就会有人误读）：① `Locks` **不含任何 canonical key**，所以这段文本可安全外发；
② 子秒读数按毫秒展示、秒级按 100ms 取整；亚毫秒读作 `<1ms` —— 首版渲染把一次真实等待四舍五入成 `0s`，与同一行的 `waits: 1` 自相矛盾，已修并补了一条渲染用例；③ 域数量上限 64（含 `other scopes` 折叠槽），
长会话写几百个文件也不会让诊断结构无界增长；④ **只观测进程内**：另一个 Reasonix 进程的等待不可见（与 §2.3 同因）；
⑤ 已知盲区：同一 Owner **会话内部**的等待（另一次获取在途、或等自己的 hold 排空）不计入 `Waits`，
因为它们不是租约争用而是会话内串行化；L8 若需要「放弃总数」，得给那条分支单独记账（已记为待办，不复用 `Waits` 以免把两种等待混成一个数）。

**成本**：`beginAcquisitionLocked`/`finishAcquisitionLocked` **只在进入排队时才读时钟**，未争用路径不读表、不分配；
实测 `BenchmarkUncontendedPathHold` ~540µs/op（3 次 517–558）vs 埋点前 526–535 —— 差值在噪声内。

**红→绿已验（4 组探针，均已还原）**：① `noteBlockedLock` 直接 return → 三条用例红（`in-flight waits = 0, want 1`、
`blocked scopes = [], want exactly one`、`waits/timeouts/inFlight = 0/0/0`）；② 不记时长 → `max wait 0s outside (0, 575µs]`；
③ `noteLockTimeout` 直接 return → `timed-out lock = {file 1 0}, want file with 1 timeout`；
④ `noteRepaidLocked` 直接 return → `lender repaid/waits = 0/0, want 1/0`。

### 5.1 已测得的基线（L1 落地时）

```
go test -run '^$' -bench 'BenchmarkUncontendedPathHold' -benchtime 300x -count=3 ./internal/workspacelease/
```

本机（i9-14900KF，linux/amd64）：

| 基准 | 中位 | 说明 |
| --- | --- | --- |
| `BenchmarkUncontendedPathHold` | ~510µs/op | 既有基准：一次无争用路径写（约 20 个锁文件获取） |
| `BenchmarkUncontendedPathHoldWithHolderRecord`（新增） | ~660µs/op | 同一路径 + 已设身份；+150µs（约 +29%） |
| `BenchmarkResidentJobHoldReuse`（L3 新增） | ~500µs/op | 驻留任务下同路径重写：直接复用保留的 hold，0.50ms vs 0.51ms（差值在噪声内） |
| 收窄前的同一基准（历史，未保留） | ~1.40ms/op | +890µs（2.6×）：每个锁文件都写记录 |

结论：身份记录的代价必须**按「有人会读」的锁域计价**；全量发布在热路径上不可接受。
后续节点（L2/L6）若添加新的发布点，必须重跑这一组基准并写在这里。

L2 不影响这一组基准：写面分类发生在取租约之前的纯计算（`shellsafe` 解析 + 字符串处理），不新增文件 IO；
唯一新增开销是未命中时的零值返回。实测口径见 §5.1 的基准命令。

**L5 新增开销**：令牌现在站在每次写工具调用与租约之间，因此必须自己量一次。

```
go test -run '^$' -bench 'BenchmarkWriteIntentTokenAcquire' -benchtime 300x -count=3 ./internal/agent/
```

| 基准 | 中位 | 说明 |
| --- | --- | --- |
| `BenchmarkWriteIntentTokenAcquire`（L5 新增） | ~50µs/op（44–56µs），90 allocs/op | 无争用 intent 取得 + 释放 |
| `BenchmarkUncontendedPathHold`（对照） | ~510µs/op | 它前面那次租约获取 |

结论：令牌自身约为它前置的租约获取的 **~10%**，而它省下的是队友的 **整次失败尝试 + 一条等待通知**
（~510µs/次，N 个成员省 N-1 次），维持净收益。90 allocs 主要来自 `NormalizeWritePaths` 的路径归一化
（与写调度器同源）。**未做**（但已记账）：按路径字符串记忆归一化结果可以再降一半以上，
代价是符号链接变化时可能拿到陈旧 scope——对一个只做「延迟」的性能令牌而言，这点风险仍要权衡，
真要做需先有 profile 证明它是瓶颈（当前不是）。

**L3b 对热路径的影响**（让锁本身只在「被挡」时才跑，但收回逻辑站在每次成功取锁的路径上）：

```
go test -run '^$' -bench 'BenchmarkUncontendedPathHold|BenchmarkResidentJobHoldReuse' -benchtime 300x ./internal/workspacelease/
```

| 基准 | 中位 | 说明 |
| --- | --- | --- |
| `BenchmarkUncontendedPathHold` | ~530µs/op（526–535，3 次） | 与 L3 基线 ~510µs 差值在噪声内（每次取锁多一次 `len()==0` 判定） |
| `BenchmarkResidentJobHoldReuse` | ~520µs/op（517–549） | 保留路径复用 |
| `BenchmarkUncontendedPathHoldWithHolderRecord` | ~650µs/op（646–665） | L1 的持有者记录，不受影响 |

结论：让锁**不给无争用路径写加成本**。收益端是结构性的——被挡方从「等满 3s」变成一次重试
（~0.5ms，且不需要重取整条路径）；没有这个机制时，L3 的窗口再短也只是把伤害标尺调小。

### 5.2 偏差与冻结文件记录

| 节点 | 改了什么（超出原文档描述） | 为什么 | 冻结文件 |
| --- | --- | --- | --- |
| L1 | `internal/cli/team_backend_build.go`（新增成员租约标签）、`boot.go` 的 `SetIdentity` + 新 `Options` 字段 | 身份必须由装配方提供成员名，租约层拿不到 | 未触碰 |
| L3 | 无（只改了 `internal/workspacelease/lease.go` 的常量与新增用例） | 原文就把收窄写在节点里，常量的依据记在 §5.1 | 未触碰 |
| L2 | `internal/agent/tool_write_coordination.go`、`tool_hooks_mutation.go`、`tool_call_plan.go`、`services.go`、`agent.go`、`internal/boot/hook_lease_surface.go` + `boot.go` 装配点；`internal/hook/hook.go` 新增 `WriteScope` 字段 | 收窄租约的决策点就在 agent 侧；agent 不 import hook，因此适配器落在 boot | **触碰 `tool_write_coordination.go`（原冻结）；`execute_one.go` 未动** |
| L3b | `internal/workspacelease/lease.go`（`acquireMode` 的先收回后重试、`systemHold.paths`、`ownerLease.retained`）、新 `yield.go` + `retention.go`（保留机制从 `lease.go` 拆出）、`scope.go`（自己等自己时收回）、`lease_test.go` **两个既有用例改用贷款语义**（`TestBackgroundRetentionOutlivesRun` → `TestBackgroundRetentionYieldsToABlockedPeer`、`TestLeaseWaitsForEveryRetainedBackgroundJob` → `TestBlockedPeerRepaysEveryRetainedBackgroundJob`） | 原文档把这一项记为「需要 `filelock` 暴露同进程等待者」的后续项，实测发现前提有误：被挡方在 `acquireMode` 里就看得见 `filelock.ErrHeld`，判断闭环在 workspacelease 内部，`filelock` 未改。改既有用例是因为它们把「保留期间队友必须超时」写成了断言——那正是本节点要推翻的性质；改名后的用例仍断言「无人等待时 hold 在驻留任务期间被保留」，只去掉「必须让队友等」这一条（§7.1 第 3 条例外，在此登记） | 未触碰 |
| L5 | `internal/agent/write_intent_gate.go`、`write_intent_token.go`、`services.go`、`agent.go`、`task_options.go`、`tool_write_coordination.go`（再次）、`internal/boot/boot.go`（`Options.WriteIntentGate` + 1 行透传）、`internal/cli/team_write_token.go`、`team_backend_build.go`、`team_member_tools.go`（fan-out 返回行加预约报告） | 令牌必须落在写工具取锁的那一个决策点上（agent 侧），而成员身份/团队/workspace root 只有装配方（cli/boot）知道 | **再次触碰 `tool_write_coordination.go`；`execute_one.go` 未动** |

L5 的两处**接线只有 1 行、没有 boot 级用例**：`boot.Options.WriteIntentGate → agent.Options`（`boot.go:1722`）
与 `newMemberBackendBuilder` 里 `opts.WriteIntentGate = memberWriteIntentGate(...)`（必须写在 workspace root 赋值**之后**）。
行为由 agent 级用例（真实写工具调用确实咨询了 gate）与 cli 级用例（gate 语义、标签、team/root 分屉）覆盖；
接线本身靠审阅，与 L2 的 `HookWriteSurface` 同形。

冻结约束的现状：它原本是为了 Part A/Part B 并行施工不撞文件；Part A 已提交（`95b01b599`），该理由不再成立。
后续节点若需要改 `internal/agent/**`，按节点立项并在本表登记即可；`execute_one.go` 仍应尽量不动（本路线目前未动）。

---

## 6. 节点状态

| 节点 | 主题 | 阶段 | 状态 | 证据 |
| --- | --- | --- | --- | --- |
| L1 | 等待通知指名持有者 | P0 | 已完成 | **提交**：`6697501b6`（本节点单独入库）。`internal/workspacelease/holder.go`（`SetIdentity`/`WaitingOn`/`observeHolder`/`noteBlockedHolder` + `Owner.holderRootPath`）、`Owner.acquireMode` 的两处挂钩、`boot.Options.WorkspaceLeaseLabel` + `boot.Build` 的 `SetIdentity`、`memberWorkspaceLeaseLabel`（`team_backend_build.go`）、`workspace_lease_notice.go` 的 `holderWaitDetail`/`leaseHoldAge`。**用例**：`workspacelease/holder_identity_test.go` 5 条（发布/退休、无身份不发布、排队时指名持有者与模式、路径写者挡住整工作区写者、shared 记录不被先释放者删除）、`boot/workspace_lease_holder_notice_test.go` 3 条（指名 + 模式 + 年龄、无身份回退原文案、年龄格式化）、`cli/team_lease_label_test.go` 1 条。**红→绿已验**：去掉 `noteBlockedHolder` → 两个用例分别报 “the queued writer never reported waiting”/“want it to name the holding member”；把 `observeHolder` 变成直通 → 三条发布/指名用例报 “holder records ... = []”。**度量**：见 §5.1。 |
| L2 | hooks 按写面取租约 | P0 | 已完成 | **提交**：`3fc1907a3`（本节点单独入库）。hook 侧：`internal/hook/writesurface.go`（`ToolCallWriteSurface` / `classifyHookWrites`，证明优先、声明补位）+ `HookConfig.WriteScope`。agent 侧：`ToolHookWriteSurface`、`writeLeaseScope`、`workspaceWritePaths`、`pathBoundWriteScope`（`tool_hooks_mutation.go`）、`tool_write_coordination.go` 的租约选择、`tool_call_plan.go` 的 `hookSurface`/`hookWritePaths`、`agent.Options.HookWriteSurface` + `services.go` 透传。接线：`internal/boot/hook_lease_surface.go` 适配器（agent 包刻意不 import hook）+ `boot.go:1713` 主装配点。**用例**：`hook/writesurface_test.go` 15 条子用例（读者不扩权、env 前缀读者、字面重定向路径及其排序去重、声明 none/tool、未声明与未知声明 fail-closed、证明压过声明、plugin contextFile、匹配器与事件过滤、多 hook 取最宽）、`agent/hook_lease_scope_test.go` 3 条（决策表 8 种形状、工作区外目标过滤含 `$TARGET`/`~`、报告优先与无报告时的保守回退）。**红→绿已验**：① `writeLeaseScope` 对任何 `Fires` 都取整工作区 → 决策表 3 条红；② `classifyHookWrites` 一律整工作区 → 15 条中 6 条红。**偏差**：payload 派生的格式化命令改为显式 `writeScope: "tool"` 声明（静态证明不可能安全，理由见 §4.2）；冻结文件偏差见 §5.2。 |
| L3 | `backgroundGrace` 收窄 | P0 | 已完成 | **提交**：`0740f226f`（本节点单独入库）。`internal/workspacelease/lease.go` 的 `backgroundGrace` **30s → 3s**（注释里写明白依据）。**用例/探针**：`workspacelease/background_grace_window_test.go` 2 条——`TestRetainedWindowMatchesTheConfiguredGrace` 量出窗口落在 `[grace/2, 5×grace]`、`TestBackgroundGraceStaysInTheDecidedBand` 把 2–5s 的决定写成断言；新增 `BenchmarkResidentJobHoldReuse`。**红→绿已验**：只加探针不改常量 → `backgroundGrace = 30s, over the decided 2–5s band`；改成 3s 后绿。**数据**：重取/复用成本都是 ~0.5ms（§5.1），而窗口每多一秒就是队友多等一秒 → 收窄。**残留**：没有做 scope 感知（bash 启动的任务本身就是整工作区，快照无收益）；保留的语义由 **L3b** 补全为**贷款**（无人等待时省钱，有人等待时立刻让出）。 |
| L3b | 驻留 hold 在队友等待时立刻让锁 | P0 | 已完成 | **注册表**：`internal/workspacelease/yield.go`（`retainedLocks` + `publishRetainedLocked`/`retractRetainedLocked`/`yieldRetainedOn`；只在 `o.mu` 下成对发布/收回，收齐 Owner 后再逐个取 `o.mu`，无锁序反转）。**让锁点**：`lease.go` 的 `acquireMode`（`ErrHeld` → 收回 → 同 key 重试一次，仍在读持有者记录之前）、`scope.go` 的 `HoldWriteForPaths` 与自己 `lease.go` 的 `HoldWrite`（自己等自己也立刻收回）。**修漏洞**：发布放在 hold 完成的唯一漏斗 `collectInactiveLocked`（`retention.go`）。**用例**：`workspacelease/yield_test.go` 8 条 + 2 条改写后的既有用例（共 10 条）——`TestRetainedPathHoldYieldsToABlockedPeer`（被挡方不产生等待通知就拿到域）、`TestLiveHoldIsNeverYieldedToAPeer`（`refs>0` 的 hold 谁也拿不走，工具释放后才轮到队友）、`TestYieldRepaysRetainedHoldsOnlyAndLeavesLiveOnes`（同一 Owner 上活 hold 与贷款共存，只收回贷款）、`TestOwnerRepaysItsOwnRetainedHoldInsteadOfWaitingItOut`（自己等自己）、`TestHoldCompletingAfterTheWindowIsArmedIsStillYieldable`（窗口点火后完成的 hold 仍可收回）、`TestYieldLeavesNoStaleRegistryEntry`（收回后注册表归零，路径可被后续写者正常取得）、`TestYieldIsScopedToTheLockDomain`（同一进程内另一个工作区不能收回别人的贷款）、`TestYieldDoesNotWeakenCancellation`、以及重写后的两条既有用例（§5.2）。全部用超时只作死锁守卫（2s），判定靠等待探针与 `State()`，不用 sleep 计时。**红→绿已验（三次，均在最终文件布局上重跑）**：① `yieldRetainedOn` 直接 `return false` → 三条同伴用例红（`peer did not repay the retained hold/path/later-completed hold: context deadline exceeded`，且 `TestLiveHoldIsNeverYieldedToAPeer` 保持绿，证明红的是让锁而不是夹具）；② `repayRetainedLocked` 直接 `return nil` → 同伴用例 + `TestOwnerRepaysItsOwnRetainedHoldInsteadOfWaitingItOut` 红（`owner waited out its own retained hold`）；③ 发布只放在窗口点火处 → 仅 `TestHoldCompletingAfterTheWindowIsArmedIsStillYieldable` 红而 `TestRetainedPathHoldYieldsToABlockedPeer`/`TestYieldLeavesNoStaleRegistryEntry` 保持绿（精确命中「后完成的 hold 变成收不回的贷款」这个漏洞）。三次均已还原。**度量**：见 §5.1（热路径无回归）。**残留**：跨进程不成立（注册表是进程内的），跨进程收敛归 L8。 |
| L4 | 减少取锁次数（提示层） | P0 | 已完成 | **提交**：`22efded6e`（本节点单独入库）。`internal/team/role.go` 的 `sharedCollaborationDiscipline` 新增第 4 条：同一文件的多次改动合并成一次 `multi_edit`（或 `edit_file`）调用，并把理由写明（每次写工具调用都取工作区写租约，队友会逐个排队）；该文本对 leader 与 member 同时生效（`SystemPromptForRole` 两路都拼 `sharedCollaborationDiscipline`）。**用例**：`team/role_write_discipline_test.go` 2 条 —— 指名 `multi_edit` + 给出 `write lease` 理由（member/leader/完整 prompt 三路都断言）、且纪律文本与成员 prompt 逐字节稳定（保住 cache-first 契约）。**红→绿已验**：先写用例 → 报 “discipline must name the batching tool”；加文本后绿。**风险/残留**：提示词只能降低工具调用次数，不能强制；真正的减少取锁次数仍需 L5 的调度。 |
| L5 | 团队写令牌 + 写路径预约 | P1 | 已完成 | **令牌**：`internal/agent/write_intent_token.go`（`Acquire`/`firstBlockerLocked`/`sweepLocked`/`State`，重叠判定复用 `ScheduleOverlaps`）、`write_intent_gate.go`（`WriteIntent`/`WriteIntentWait`/`WriteIntentGateFunc` + `noteWriteIntentWait` 通知）、`tool_write_coordination.go` 的 `acquireCallWriteGuard`（先令牌后租约，失败时回吐令牌）、`services.go`/`agent.go`/`boot.Options` 透传、`cli/team_write_token.go` 的按 `(team, root)` 注册表 + `memberWriteIntentGate`、`team_backend_build.go` 接线；**预约报告**：`subtaskWriteAreas`/`fanoutWriteAreas` + `team_member_tools.go` 的 `assignToRelevant` 返回行。**用例**：`agent/write_intent_token_test.go` 9 条（不相交全部并发且零排队、相交按到达顺序排队且报告持有者与范围、不相交者可越过被挡队列、整工作区双向互斥、取消者不吊死其后成员、工作区外路径保守升格、同目录兄弟文件仍并发、同一 peer 永不自排（否则自己等自己：写工具在单 agent 内串行，但批量并行段里的伪装写者仍可能重叠；跳过同 peer 是安全的，因为令牌从不互斥，互斥仍在租约）、重复释放幂等 + nil 安全）、`agent/write_intent_gate_test.go` 5 条（真实 `write_file` 调用确实咨询 gate 且释放、入队即报 notice 且文案指名双方、gate 失败/无 gate/nil agent 均 fail-open、label 渲染 5 形、无 sink 安全）、`cli/team_write_token_test.go` 8 条（同团队同 root 同队列、跨团队/跨 root 不互阻、无名团队/空 root 无 gate、标签、路径抽取表、报告只在知道时才说、`assignToRelevant` 保留原文行并在指名路径时追加报告、注册表并发提同一令牌）；另加基准 `BenchmarkWriteIntentTokenAcquire`（§5.1）。**红→绿已验**：① 去掉 `acquireCallWriteGuard` 里的 `acquireWriteIntent` → “gate saw 0 intents”；② `blockerIn` 改成「遇到任何在途 intent 就排队」→ 不相交用例 20s 超时（第二个不相交 peer 的 `Acquire` 永不返回）；③ 反过来改成「永不排队」→ 5 条排队用例报 “was admitted immediately, want it queued”；④ 去掉同 peer 跳过 → 自排用例报 “member m1 queued behind member m1”。**语义未变**：`TestAssignSubtasksAssemblesMembersInParallel`、`TestAssignSubtasksKeepsSiblingsWhenOneMemberRefuses`、§6.2 点名的 4 条并发用例逐条 PASS。**残留**：boot/cli 两处 1 行接线无 boot 级用例（见 §5.2）；进程内写调度仍按 opaque 处理 hooks 写者（L2 残留）；预约报告基于共享 subtask 文本，无法区分单个成员。 |
| L6 | 产物通道（可证明的构建并行） | P1 | 未开始 | — |
| L7 | 每成员 worktree | P2 | 未开始 | 先例：`desktop/delivery_worktree.go:566` |
| L8 | 等锁有界 + 超时可见（B3-2 残留） | P2 | 未开始 | — |
| L9 | 写者优先 bypass / stripe（评估项） | P2 | 未开始 | — |
| M0 | 租约现场可观测量 + 诊断输出（§5 前置） | — | 已完成 | `internal/workspacelease/metrics.go`（`LeaseMetrics`/`ScopeWait`/`LockWait` + 桶直方图 + `Report()`）、埋点落在 `beginAcquisitionLocked`/`finishAcquisitionLocked`、`acquireMode` 的排队点与 `noteLockTimeout`、`yield.go` 的 `noteRepaidLocked`、`lockClass` 的层次分类。**出口**：`Owner.Metrics()/MetricsReport()`、`control.Controller.WorkspaceLeaseMetrics()/WorkspaceLeaseDiagnostics()`（`internal/control/controller.go`）、会话诊断导出字段 `workspaceLeaseDiagnostics`（`internal/control/goal_diagnostics.go`）。**用例**：`workspacelease/metrics_test.go` 9 条（未争用零噪声且报告为一行、一次获取只记一次等待且域/锁归属正确、整工作区等待归 `whole workspace` + 根锁、放弃算超时并归到实际挡住的锁文件、被服务的一方 `Waits==0` 而 `Repaid` 记在出借者、直方图只给桶上界与溢出桶、层次分类 7 种、域上限 64 折叠、nil Owner 报 unavailable）、`control/workspace_lease_metrics_test.go` 1 条（无租约时两个出口不 panic 不谎报）。**红→绿已验（4 组，均已还原）**：见 §5。**度量**：埋点不给未争用路径加成本（§5）。**残留**：会话内部等待（另一次获取在途 / 等自己的 hold 排空）不计入 `Waits`，L8 若要「放弃总数」需单独记账；跨进程等待不可观测。 |
| — | 等锁可区分（Part B B3-1） | — | 已完成 | `internal/boot/workspace_lease_notice.go` + `workspace_lease_wait_notice_test.go` |

状态取值：`未开始` / `进行中` / `待验收` / `已完成` / `阻塞`。
**阻塞必须写真实原因，不得改写为通过**（沿用 `TEAM_TUI_AGENT_DECOUPLING_PLAN.md` 的铁律）。

**节点提交**（按功能拆分，均以 `03fb376b8` 为父提交；四个提交各自构建、`vet` 与相关用例均已实跑通过）：

| 节点 | 提交号 | 主题 |
| --- | --- | --- |
| L1 | `6697501b6` | `feat(team-lease): name the holder a queued writer waits for` |
| L4 | `22efded6e` | `feat(team): batch a member's edits to one file into one write call` |
| L2 | `3fc1907a3` | `fix(team-lease): size the write hold from what the call can actually write` |
| L3 | `0740f226f` | `perf(team-lease): shorten the retained-hold window to 3s` |

拆分口径（免得后来者以为漏提交）：L2 的 `tool_write_coordination.go` 在 L5 被改写成
「先令牌后租约」（`acquireCallWriteGuard`），所以 L2 那个提交是**当时形态**的重建（hooks 写面决定租约宽窄，无令牌），
不是把今天的文件砍一刀；L1 提交里的 `memberWriteLabel` 当时还住在 `team_backend_build.go`，
L5 之后它才与写令牌共享（该文件现在的未提交差异里能看到这处搬动与注释改词）。
截至 `0740f226f`，**L3b、L5、M0 的改动尚未入库**（本文档已记录其内容与证据，提交号待它们入库后回填）。

推荐施工顺序：**~~L1~~ → ~~L4~~ → ~~L2~~ → ~~L3~~ → ~~L3b~~ → ~~L5~~ → L6 → L8 → L7 → L9**
（已完成：L1、L4、L2、L3、L3b、L5；下一个节点按上表顺序推进 —— L6 产物通道）。

---

## 7. 施工约定

### 7.1 硬约束

1. **不动冻结文件**：`internal/agent/tool_write_coordination.go`、`execute_one.go`（L8 需要时先报备）、
   Part A 独占的 `chat_tui*.go` / `team_replay.go` / `team_history_sync.go`、board 的 SQLite 实现。
   **注**：`internal/agent/**` 的冻结是 Part A 并行施工期的避让约束，理由已随 Part A 提交（`95b01b599`）失效；
   按节点立项（L2 已如此，L8 同理）即可改动，但改动范围要写进 §5.2 的偏差记录，且 `execute_one.go` 尽量不动。
2. **安全门只收窄判定范围，不放宽事实认定**：L2/L6 都必须保留「无法证明即整工作区独占」的回退分支，
   且回退分支必须有测试。
3. **新增测试写进自己新增的 `_test.go`**，不共用、不改动他人测试。
4. **`gofmt` 必须干净**；改动文件跑 `golangci-lint`（本机 `make lint` 不可用，用
   `$(go env GOPATH)/bin/golangci-lint run --timeout=5m`）。
5. **repolint 不得加宽 baseline**；新文件预算为 0，不要把浮动注释带进新文件
   （`function-size` 计入注释行数）。

### 7.2 门禁（每个节点）

```bash
gofmt -l <changed files>                      # 必须为空
go build ./...
go vet ./internal/cli/ ./internal/team/... ./internal/workspacelease/ ./internal/boot/
go test ./internal/cli/ ./internal/team/... ./internal/workspacelease/ ./internal/boot/   # 全绿
# boot 的 -count=2 有既有隔离问题，见 §7.3；其余用 -count=2。
go test -race ./internal/workspacelease/ -count=2
go test -race ./internal/boot/ -count=1
go test -race ./internal/cli/ -run 'Team|Cockpit|Replay|Roster' -count=2
```

并确认既有并发用例未被削弱：`TestTeamE2EAssignTaskToRelevantRunsBothMembersAndReportsBoth`、
`TestTeamBackendsDistinctMembersAssembleInParallel`、`TestCockpitSerializesPerMemberAndRunsMembersInParallel`、
`internal/team/agentruntime/agentruntime_concurrent_test.go`、`internal/team/scheduler/scheduler_concurrent_test.go`。

**并发用例禁止用 sleep 计时**，用探针统计并发峰值（沿用 Part B 的手法）。

### 7.3 已知的既有红灯与 flaky（不要误判为自己引入）

- `TestTeamHubLeaderAndMemberDoNotSerialize` 偶发失败：靠 sleep 计时测并发，高负载下不同时在途。
- `TestTeamTurnInjectsInboxAtSubmit` 在 `internal/cli` 全包跑时观察到偶发失败一次，隔离与随后连续 5 次
  全包跑均绿（Part B 已记录）。
- `TestEscalationWakeNamesTheQueue`、`TestTerminalSizeRecoveryFollowsTheTerminalUnderTheFrame` 有既有
  测试内竞态。
- `internal/cli` 全包 `-race` 可能因 inotify `max_user_instances=128` 在 skillwatch 套件变红（环境因素）。
- `internal/worktree` 需要 git ≥2.38（本机 2.34.1），该包 30 个用例红，与本路线无关。
- `internal/boot` 的 `-race -count=2` 有两个**既有**隔离问题（与租约无关，L1 落地时观察到）：
  `TestBuildCoalescesAgentStreamDeltas` 在同进程第二次运行时 panic 于 `internal/provider.Register`
  （重复注册，`coalesce_wiring_test.go:57`）；`TestBuildRegistersUsableHistoryAndMemoryRetrievalTools`
  在全包 `-count=2` 下失败、隔离 `-count=2` 绿。因此 boot 的门禁用 `-count=1`，
  `workspacelease` 用 `-count=2`。
- repolint 剩余红项全在**已提交的 Part A 文件**（`chat_tui_team_*.go`、`team_history_sync.go`、
  `team_replay.go`）与既有的 `internal/provider/anthropic/messages_usage.go`；本路线的新文件必须保持 0 预算。
- L5 落地后观察到**两次未定位的失败**，两次都没捕获用例名（都因我截断了输出）：
  ① `go test -race ./internal/cli/ -run 'Team|Write|Cockpit' -count=2`（`tail -3`，连跑 5 次又绿）；
  ② 与 `agent+boot+team+…` 同时跑的全包非竞态一次（`tail -13`，其后同一命令连跑 6 次全绿）。
  两次都发生在**多包并发/竞态**的高负载下，与环境相关；假说中最像的对象是上一条
  `TestTeamHubLeaderAndMemberDoNotSerialize`（sleep 计时测并发），但已对它做「后台跑 agent 包 + `-count=30` ×
  3 轮」的压力复现（90 次）**未复现**，因此**不定论**。按铁律记为未定位的偶发，不当已通过。
  下次复现务必把完整输出落盘（`> /tmp/xxx.log`）再归因。
- golangci-lint 在改动包上仅剩 1 项：`internal/cli/chat_tui_team_switch_test.go:79` 的 `SA4005`
  （Part A 文件，非本路线改动）。

### 7.4 完成定义

① 代码落地；② 该节点的验收用例存在且**先红后绿**；③ §7.2 门禁实跑通过；
④ §6 状态表与证据列更新；⑤ 改动涉及性能时，附 §5 基准的改动前后对比。
门禁受阻时继续做其他独立节点，并在回报里写明缺失的证据。

---

## 附录 A：证据索引

### A.1 租约获取链

| 位置 | 内容 |
| --- | --- |
| `internal/workspacelease/lease.go:1-3` | 包语义：读者不取租约；工具 hold 返回即释放；后台有界保留 |
| `internal/workspacelease/lease.go:25` | `backgroundGrace = 30s` |
| `internal/workspacelease/lease.go:289` | `HoldWrite`（整工作区） |
| `internal/workspacelease/lease.go:557-597` | `acquireWorkspace` / `acquireCompatibilityRoots`（canonical 独占，其余 shared） |
| `internal/workspacelease/lease.go:601-661` | `acquireQueuedMode` / `acquireSharedDomain`（同 Owner 重入短路，其他 Owner 排队） |
| `internal/workspacelease/lease.go:737-761` | `acquireMode`：Try → `markWaiting`/`onWait`（一次）→ 无界阻塞获取 |
| `internal/workspacelease/lease.go:436-457` | `RetainUntil` + `collectInactiveLocked`（grace 保留） |
| `internal/workspacelease/scope.go:20,25` | `pathLockStripes = 4096`、`treeLockStripes = 4096` |
| `internal/workspacelease/scope.go:56,265,271,279` | 路径级：canonical shared + 目录 tree shared + stripe exclusive |
| `internal/workspacelease/roots.go:27` | `HoldWriteRoots`（多 root 租约，L7 先例） |
| `internal/config/paths.go:353-362` | 租约目录是 OS 用户级全局（刻意忽略 `REASONIX_HOME`） |

### A.2 放大机制

| 位置 | 内容 |
| --- | --- |
| `internal/cli/team_backend_build.go:426` | 所有成员共用同一 `WorkspaceRoot` |
| `internal/boot/background_scope.go:14-42` | 每成员一个 Owner；`onWait` 发通知；`RetainUntil` 挂到后台任务 |
| `internal/agent/path_bound_tools.go:150` | 路径级白名单（7 个内置写工具，不含 bash） |
| `internal/agent/tool_write_coordination.go:24-76` | hooks → 整工作区；白名单 → 路径级；其余 → 整工作区 |
| `internal/agent/path_bound_tools.go:278` | `extractWritePathsFromArgs`（工具参数的写路径：租约粒度与令牌 scope 都用它） |
| `internal/cli/team_write_token.go` | L5 落地：`subtaskWriteAreas`（自由文本 subtask 的路径抽取，**只用于派发报告**）、`teamWriteIntentTokens` 注册表 |
| `internal/agent/write_claims.go:151` | `WholeWorkspaceWriteClaim` |
| `internal/shellsafe/effect.go:45-48` | `AnyMutation`/`WorkspaceMutation` 把 `EffectUnknown` 当 mutation（安全设计） |
| `internal/hook/runner.go:83-85` | 任一 Pre/PostToolUse hook → `ToolMutationHooksEnabled()` 为真 |
| `internal/agent/tool_hooks_mutation.go:7-17` | 布尔语义 + 自定义 `ToolHooks` 的保守回退 |
| `internal/filelock/filelock.go:227-232,252-257` | 有排队 writer 时 `ModeShared` 被拒（写者优先连带） |
| `internal/agent/execute_one.go:329,44` | 租约覆盖整个工具执行 |

### A.3 可区分等待（已落地）

| 位置 | 内容 |
| --- | --- |
| `internal/boot/workspace_lease_notice.go` | `workspaceLeaseWaitEvent`：整工作区 vs 某文件的分类 |
| `internal/boot/workspace_lease_wait_notice_test.go` | 真实双 Owner 争用下的分类断言 |
| `internal/workspacelease/lease.go:96-110` | `Owner.State()`（`Waiting`/`Scope`/`Label`）供分类使用 |

### A.4 安全边界（为什么不能随便放宽）

- `internal/shellsafe/effect.go`：`ClassifyBash` 对每个 segment 自证；`go test`/`npm run`/`cargo check`
  之类**执行仓库代码**的命令不能被证明 confined（测试与 build script 可写任何地方）。
  因此 L6 只对「不执行仓库代码的构建」放行，其余保持整工作区独占或要求显式 opt-in。
- `internal/agent/tool_hooks_mutation.go`：hook 是用户 shell 代码，**必须** fail-closed。
