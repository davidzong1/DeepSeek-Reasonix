# 优化路线 —— 帧路径成本（team session 全成员思考时的卡顿）

> 状态：**分析已完成（实测见 §2）；四条改动已拍板（2026-09-22）；F0 与 Part A（F-A1/F-A2）、
> Part B（F-B1/F-B2）均已完成并过门禁**（2026-09-22，两段合并后的树上实跑）。追加节点 **F-C1**
> （段落内实时预览 + 重绘节流，2026-09-23 用户报「输出停顿后一次跳一大段，只有被选中成员这样」）
> 已完成。节点状态见 §7，证据只写已实跑过的。**未提交**：工作区是共享的，提交/合并由用户决定。
> 本文档只描述现象、实测、改造方向与 A/B 拆分，**不把任何未实施的东西描述为已实现**；节点状态只在 §7 更新。
> 权威优先级：用户拍板 > `docs/team-mcp-port/TASK.md` > 本文档 > 实现代码注释。
> 关联：`TEAM_MEMBER_PARALLELISM_ROUTE.md`（同一帧线程的前一轮改造，本文档复用其 A/B 拆分纪律与门禁口径）、
> `TEAM_TUI_AGENT_DECOUPLING_PLAN.md`（帧线程负载的历史来源）、
> `TEAM_LEADER_WAIT_ROUTE.md`（同一批成员的唤醒路径，本文档不改其语义）。

---

## 1. 现象

团队会话里**所有成员都在思考**时，TUI 交互明显变卡：切换成员、鼠标拖选文本都要等一下才有反应。
成员越少、越闲时不存在这个问题。

---

## 2. 实测与根因

### 2.1 单条成员事件 = 一次 `Update` + 一次整屏 `View()`，在同一个 goroutine 上

证据链：

| 事实 | 位置 |
| --- | --- |
| 每个流式 chunk 一个事件 | `internal/agent/agent.go:1470,1474`（`event.Reasoning` / `event.Text`，逐 `chunk.Text`） |
| 事件汇入一个 pump，**一条事件消费一次** | `internal/cli/chat_tui_team_switch.go:27` `pump.next()`；`:77` 处理完无条件重新武装 |
| bubbletea 对**每一条消息** `Update` → 派发 cmd → **`View()`**，全在同一 goroutine | `bubbletea/v2@v2.0.9/tea.go:880` `model.Update(msg)`、`:888` `p.render(model)` → `model.View()` |
| 消息通道**无缓冲** | `tea.go:606` `msgs: make(chan Msg)` |

所以：**成员 A 的第 137 个 token = 一次 `Update` + 一次整屏 `View()`，占用的是同一只处理键盘与鼠标的手**。
`View()` 是每条消息的固定成本，bubbletea 不做 diff。

### 2.2 成本量级（本机实测，6 人花名册 / 120×40 / 401 块 transcript / 包装缓存热）

| 路径 | ns/op |
| --- | --- |
| `View()`（整屏渲染） | **202,000** |
| `Update` 成员 delta（未绑定成员） | 45,777 |
| `Update` 成员 delta（已绑定成员） | 51,280 |
| `Update` 输入按键（composer 不变） | 82,609 |
| `Update` 鼠标移动（拖选） | 41,125 |

**每条成员事件 ≈ 250 µs → 事件循环上限约 4,000 条/秒。**
饱和条件：`成员数 × 每人 chunk 速率 ≥ 4,000/s`。单条成本与 transcript 长度无关（实测 1/201/1001/3001 块均为 44–45 µs），
所以占用率**随成员数线性叠加**——这就是「全员思考」才是触发条件的原因。

> **已绑定成员的 delta 不再是常数（2026-09-23 修订）**：上表这一行测的是「一个 chunk 到达」
> 的成本，而当时的 `streamAnswer` 只在段落闭合时才重绘，多数 chunk 几乎不做事。段落内实时
> 预览（`internal/cli/answer_stream.go`）让该行随回答长度增长，因此加了 `answerPaintInterval`
> （50ms）节流；本机同一 fixture 重测：`View()` 138 µs、后台 delta 50 µs、
> **绑定 delta 132 µs（节流后，未被节流的 chunk 走早退路径）**、输入按键 90 µs、鼠标移动 47 µs。
> 读这一行时注意它量的是**节流窗口内的均值**：命中重绘的那一条是 O(回答长度)（实测 3000 chunk
> 的段落流：首条 0.77ms、81KB 时 4.3ms），未命中的一条是 ~50 µs。
>
> 上表由 `internal/cli/frame_cost_fixture_test.go` 的 `TestFrameCostBaseline` 复现（同一台机器上的
> 多次运行落在 ±10% 内：`View()` 202–242 µs、后台 delta 45–47 µs、绑定 delta 51–54 µs、
> 输入按键 81–85 µs、鼠标移动 45–47 µs）。换机器/换终端尺寸会整体平移，**用同一台机器上的前后对照读结论**。
>
> 未能离线测的一项：每个成员实际的 chunk 速率（需要真实 provider）。上表给出的是阈值公式，不是倍数结论。

### 2.3 为什么偏偏「鼠标拖选」和「切换成员」最先垮

- **拖选**：团队会话绑定时 `overlayMouseMode()` 返回 `tea.MouseModeCellMotion`（`internal/cli/chat_tui_team_render.go:656-661`），
  **app 自己捕获鼠标**，于是拖拽的每一次位移都是一条 `tea.MouseMotionMsg`（`internal/cli/chat_tui.go:723`）
  走同一条饱和队列，且每条还要再触发一次 `View()`。一次快速拖拽可产生每秒数百个 motion 事件。
  **选择顺滑度 = 事件循环的剩余容量。**
- **切换成员**：那一次按键只是队列里的又一条消息，排在成员事件积压之后，处理完自己还要付 83 µs + 202 µs。

### 2.4 纯浪费：看不见的事件也在付整屏渲染

未绑定成员的 delta **屏幕上一个像素都不会变**（只进 512 上限的重放缓冲，且 `markMemberUnread` 只对
`TurnDone/Message/Approval/Ask` 生效，`chat_tui_team_switch.go:151-158`），却照样付 46 + 202 µs。
6 人里 5 个后台成员时，**约 5/6 的循环占用是纯浪费**。

### 2.5 第二个、更糟的档位：包装缓存冷掉时差 50 倍

`invalidateWrapFrom(index)` 把 `wrapBlockCount` 截断到 `index`（`internal/cli/wrap_cache.go:128-138`），
下一条消息的 `syncWrappedLines` 于是重绕 `wrapBlockCount..n` 的**全部剩余块**，随后
`flattenBlockWraps` 重建整张扁平行表（`wrap_cache.go:99-113`），`feedViewportContent` 再整份 `copy` 一次（`wrap_cache.go:80-88`）。

实测（缓存冷 = 每条消息都走重绕路径）：**2,519,584 ns/条 vs 热的 45,777 ns/条 ≈ 55×**。
任何**早位置**的块重写都会掉进这个档位：`setTranscriptBlock(answerIdx)`（`transcript.go:68-77`）、
resize 重排、`team_replay.go` 的重放改写（`:327-341` 直接改 `wrapBlockLines`/`wrappedLines` 字段）。
长会话里「突然变得很卡」大概率是这个。

### 2.6 `View()` 那 202 µs 花在哪（pprof）

| 子项 | 占 View |
| --- | --- |
| `renderTranscript` | 44.5%（其中 `lipgloss.JoinHorizontal` 占其 **99%**） |
| composer `textarea.View` | 13.3% |
| `renderStatusBlock` | 5.0% |
| 其余 `lipgloss.Style.Render` | 27%（分散在整帧） |

`renderTranscript`（`internal/cli/transcript.go:447-475`）每帧把可见行 `strings.Join` 成一块、
把滚动条列 `strings.Join` 成另一块，再 `lipgloss.JoinHorizontal(Top, …)` 做 ANSI 感知的水平拼接——
而滚动条列每行**只有 1 个 cell**，内容行本来就已被 `wrapTranscript` 补齐到 `cw` 宽。

---

## 3. 拍板结论（2026-09-22，用户裁定）

| # | 改动 | 裁定 |
| --- | --- | --- |
| 1 | 合并 drain：一次 `Update` 处理一批事件 | **执行** |
| 2 | 未绑定成员的 delta 不产生消息（不再付整屏渲染） | **执行** |
| 3 | `renderTranscript` 去掉 `lipgloss.JoinHorizontal` | **执行** |
| 4 | 把选择交回终端（团队会话不捕获鼠标） | **取消** —— 用户明确需要鼠标交互（§8） |
| 5 | 修掉包装缓存的冷档位（后缀同步不重建整表） | **执行** |

---

## 4. 拆分：两段零文件重叠，可并行

拆分依据同 `TEAM_MEMBER_PARALLELISM_ROUTE.md`：**唯一依据是文件互斥**。
本文档的两段是**事件消费侧**（改动何时产生消息）与**渲染/缓存侧**（改动每条消息与每帧花多少），
两者不共享状态，只在 `chatTUI` 这个模型上各自读自己的字段。

| | Part A —— 事件消费侧 | Part B —— 渲染与包装缓存 |
| --- | --- | --- |
| 节点 | F-A1（批量 drain）、F-A2（不可见事件不产生消息） | F-B1（去 `JoinHorizontal`）、F-B2（包装缓存后缀化） |
| 目录 | `internal/cli`（仅事件泵 / 成员事件路由 / 会话槽位） | `internal/cli`（仅 transcript / 包装缓存 / 重放改写 / 一个模型字段） |
| 改动性质 | 改变消息的产生时机与批量 | 保持语义，只改渲染与缓存的算法 |
| 风险 | 中（重放缓冲的所有权与生命周期变了） | 低（有字节等价门禁） |

**Part A 独占文件**（Part B 禁止编辑）：

```
internal/cli/team_event_pump.go
internal/cli/chat_tui_team_switch.go
internal/cli/chat_tui_team_session.go      （仅 session.live 字段与其初始化）
internal/cli/frame_drain_cost_test.go      （新增）
```

**Part B 独占文件**（Part A 禁止编辑）：

```
internal/cli/transcript.go
internal/cli/wrap_cache.go
internal/cli/team_replay.go
internal/cli/chat_tui.go                   （仅新增一个 int 字段，见 §5.2 的净行数约束）
internal/cli/frame_render_cost_test.go     （新增：F-B1 的 4 条用例 + View() 基准）
internal/cli/frame_wrap_cache_test.go      （新增：F-B2 的 5 条用例，含等价性/探针/分臂基准；
                                             与上一个文件分开是因为两节点的对照基准要各自可独立跑，
                                             且合并会顶到 repolint 的单文件预算——拆分纪律的约束面是
                                             **文件互斥**，不是文件个数）
```

**双方共同冻结（本轮谁都不改）**：`internal/agent/**`、`internal/control/**`、`internal/provider/**`、
`internal/team/**`、`internal/boot/**`、`internal/tool/**`、`internal/cli/team_event_pump.go` 之外的
团队任务/唤醒路径（`team_wake_*.go`、`team_leader_wait.go`、`team_member_tools.go`、`team_task_service.go`）、
`internal/cli/chat_tui_team_render.go`、`internal/cli/chat_tui_input.go`、`internal/cli/chat_tui_events.go`。

**前置节点 F0（已完成，两段只读它、不改它）**：

```
internal/cli/frame_cost_fixture_test.go    （已落地）
```

它导出两段共用的脚手架（同包，直接调用）：

| 名字 | 用途 |
| --- | --- |
| `frameCostModel(t, members, lines) chatTUI` | 「N 人花名册 + leader 已绑定 + `lines` 块 transcript」的模型，**包装缓存已预热** |
| `frameCostUpdate(m, next func(i int) tea.Msg) testing.BenchmarkResult` | 按 bubbletea 的方式**串接返回值**跑一轮 Update 基准 |
| `frameCostView(m) testing.BenchmarkResult` | 单次整屏 `View()` 基准 |
| `frameCostReport(t, name, r)` | 统一打印格式，便于两段对照 |
| `frameCostDelta(member) tea.Msg` | 一条成员流式 delta 消息 |
| `frameCostMemberID(i)` / 常量 `frameCostMembers/Lines/Width/Height/Backlog` | 花名册命名与默认尺寸 |

**两条必须遵守的用法**（否则数字会差 55×，见 §2.5）：

1. 基准里**必须串接 `Update` 的返回值**（`cur = updated.(chatTUI)`）——`frameCostUpdate` 已经这么做了。
   丢弃返回值会每次都测到「包装缓存冷」的那条路径。
2. 自建模型时若不经 `frameCostModel`，要么自己预热（`syncWrappedLines(contentW, true)` +
   `feedViewportContent()`），要么明确知道自己测的是冷路径。

F0 同时产出 §2.2 的基线数字，作为两段的前后对照。

---

## 5. 各节点

### 5.1 F-A1 —— 批量 drain（一次 `Update` 处理一批）

**问题**：`waitForMemberEvent` 一条事件一个 `tea.Msg`，于是每条事件一次 `Update` + 一次整屏 `View()`。

**做法（不新增 Update 分支、不改消息类型）**：`memberEventMsg` 的类型**保持不变**
（`type memberEventMsg memberEvent`，13 个测试文件构造它，改类型等于大面积测试返工），
改为在**处理侧**把积压一次抽干：

1. `memberEventPump` 新增 `drainReady(limit int) []memberEvent`：非阻塞地把当前队列里已就绪的事件
   按全局到达顺序取出（复用 `nextLocked`），最多 `limit` 条。
2. `handleMemberEvent` 处理完 `msg` 之后，再对自己 `drainReady(memberEventBatchLimit)` 的每一条走同一段
   `switch`，最后**只重新武装一次** `waitForMemberEvent`。

**为什么这样够**：`View()` 是每条消息的固定成本，把 N 条积压收进 1 次 `Update` 就把 `N × 202 µs` 变成 `1 × 202 µs`。
饱和时（生产快于消费）队列里总有积压，批量自然生效；空闲时批量=1，语义不变。

**必须拍板的参数**：`memberEventBatchLimit`（建议 64）。上限是为了给输入留出时延上界：
一次 `Update` 最多处理 64 条，而不是把队列抽空（抽空会让绑定成员的 delta 无限期等不到渲染）。

**验收**：

- 一条用例：向 pump 灌入 50 条事件后，**一次** `handleMemberEvent` 调用后全部 50 条都已落地
  （绑定成员的进 transcript / 后台成员的进重放缓冲），且函数只返回**一个**重放后续的命令；
- 一条基准（`frame_drain_cost_test.go`）：50 条积压的 burst 成本 ≈ 1 条的 `Update` 成本 + 50 次事件处理，
  显著低于 50 × (Update + View)。红→绿：把 `drainReady` 的调用去掉，基准回到 50×。
- **三条最容易被批量改坏的既有性质，由 `p2_acceptance_test.go` 兜住**：并发生产者下**不丢**、
  每个发送者**内部不乱序**、`unread` 计数**每成员恰好一次**。该用例原本是「一次 `next()` 调一次
  `handleMemberEvent`」的形状，批量 drain 使它失配（第一次调用就抽干队列），已改为「先让队列成为闭集、
  再分批 drain + route、最后单独断言重新武装」——性质不变，驱动方式随契约更新。

### 5.2 F-A2 —— 不可见事件不产生消息

**问题**：未绑定成员的 delta 屏幕不变，却付 46 + 202 µs（§2.4）。

**做法**：把「窗口不该看的事件」在**命令侧**就吃掉，不让它变成 `tea.Msg`。

1. `memberEventPump` 新增保留区：`hold(ev)` 把一条事件放进**该成员**的保留列表（有界，复用
   `memberLiveEventCap = 512` 的语义与淘汰策略），`held(member) []memberEvent` 取走并清空，
   `dropHeld(member)` 丢弃。
2. `waitForMemberEvent(pump, bound)` 的闭包循环：
   `for { ev := pump.next(); if ok && visible(ev, bound) { return memberEventMsg(ev) }; pump.hold(ev) }`。
   `visible(ev, bound)` = `ev.member == bound` **或** `!memberEventDelta(ev.ev.Kind)`（非 delta 的状态事件
   一律可见：TurnDone 要清缓冲并计未读、Approval/Ask 要弹卡、Message 要计未读）。
   —— **即只有「后台成员的流式 delta」被吃掉**，其余一律照旧。
3. `session.live`（`internal/cli/chat_tui_team_session.go:339`，`:367` 初始化）让位给 pump 的保留区：
   `bufferMemberEvent` / `replayMemberLiveEvents` 改读 pump。
   **所有权与生命周期是本节最大的风险点**：`live` 现在是 picker/session 级（随会话切换重建），
   pump 是 registry 级（`bindTeamBackends` 重建 registry 时 `m.memberEvents.close()` 并新建，
   `chat_tui_team_switch.go:409-412`）。实现前必须确认：**保留区随 pump 重建而清空**是否可接受。
   建议接受，并在 `TurnDone` 时显式 `dropHeld(member)` 保持既有「结算即清缓冲」语义
   （`chat_tui_team_switch.go:109-111`）。

**验收**：

- 一条用例：后台成员的 delta 进 pump 后，`waitForMemberEvent` 返回的命令**不产生消息**（在事件到达前超时返回 nil）；
  紧接着来一条 `TurnDone` 才产生消息，且此前的 delta 全部按原顺序在重放里；
- 一条用例（回归）：后台成员的重放语义不变——切到该成员时，它在后台跑的整个 turn 仍然完整呈现
  （既有 `TestTodoPanelScopedToBoundMember` 一类用例必须保持绿）；
- 一条基准：后台 delta 的**每事件循环成本 ≈ 0**（对照组是 §2.2 的 45,777 + 202,000 ns）。

**注意**：F-A1 与 F-A2 组合后，循环成本 ≈ 可见事件率 × 250 µs。两者必须分别可验证、可独立回滚。

### 5.3 F-B1 —— `renderTranscript` 去掉 `lipgloss.JoinHorizontal`

**问题**：`transcript.go:474` 每帧对「可见行块 × 滚动条列块」做 ANSI 感知的水平拼接，实测约 166 µs/帧。

**做法**：滚动条每行只有 1 个 cell，且内容行已由 `wrapTranscript` 补齐到 `cw` 宽。
把 `bar[r]` **直接追加**到 `rows[r]` 后面即可，不必走 `JoinHorizontal`：

```go
rows[r] = line + bar[r]   // line 已补齐到 cw；bar[r] 恰好 1 cell
return strings.Join(rows, "\n")
```

**唯一的风险是 ANSI 状态泄漏**（内容行若以未闭合的样式结尾，追加的滚动条 cell 会继承该样式）。
因此本节的门禁是**字节等价**，不是「看起来一样」。

**验收（均已落地）**：

- 一条用例：对同一模型（覆盖选中态、滚动条在首/中/末、空 transcript、`cw` 为奇数、含 CJK/emoji 的行）
  **比对改造前后 `renderTranscript()` 的字节**——`frame_render_cost_test.go` 的
  `referenceRenderTranscript` 把旧实现留作测试内参照，`bytes.Equal` 逐字节比对。7 个形状的表驱动用例
  + 宽字符/emoji 行 + 带样式行共 3 条，全绿。**变异验证**：把行尾的 `cw` 补齐去掉（`TrimRight` 后追加）
  即 4 个形状转红；
- 一条基准（`frame_render_cost_test.go` 的 `TestFrameViewCostRecordsTheDrop`，以及 F0 的
  `TestFrameCostBaseline` 作同一模型的前后对照）：**实测 `View()` 237,341 → 132,716 ns/op**
  （本机，F0 脚手架同模型同尺寸）。**低于文档预期的 −150 µs 量级**，原因见下。

**落地结果与预期偏差**：pprof 里 `renderTranscript` 占 `View()` 的 44.5%，但其中 `JoinHorizontal`
那 ~166 µs 并不全是「拼接」——`JoinHorizontal` 对每行还要做一次 `ansi.StringWidth` 取宽，而
`renderTranscript` 每行本来就已经知道宽度。去掉它之后省下的是「取宽 + 两段 `strings.Join` + 分配」，
不是全部 166 µs。剩余的大头是每行的 `selSpan`/`StyleRanges`（选中态）与 composer `textarea.View`
（13.3%），都不在本节点范围内。

### 5.4 F-B2 —— 包装缓存增量维护（消掉 55× 冷档位）——**落地形状（2026-09-22）**

**问题**：`invalidateWrapFrom(i)` 之后的 `syncWrappedLines` 会重绕 i 之后的全部块并**整表重建**
（§2.5）。`team_replay.go:327-341` 还直接改 `wrapBlockLines`/`wrappedLines` 字段，绕过了缓存不变量。

**落地时修正了本节的两个预设**（实测见下，这是本节点最重要的一条）：

1. **「sync 会重绕 i 之后的全部块」在帧路径上不成立。** `setTranscriptBlock` 在帧路径上只被**流式 slot**
   调用，而这些 slot 都是**尾部**块，而 sync 本来就从 `wrapBlockCount`（≈ 尾部）起算，所以
   「重写尾块」与「重写第 2 块」的成本几乎相同（实测 51 万 vs 50 万 ns/op，均含 viewport feed）。
   §2.5 的 55× **不是**来自「重绕 i 之后的块」，而是来自 `flattenBlockWraps` 整表重建 +
   `feedViewportContent` 整份拷贝这两个 O(transcript) 步骤。
2. **因此 §5.4-3 的 `wrapPrefixLines` 回退计数不必要。** 真正需要的是「每个块的起始行偏移」，
   有了它，splice 与截断都是 O(1) 定位 + O(改动块数) 重绕。

**实际做法**：

1. `chatTUI` 新增 `wrapBlockOffsets []int`（块 i 在 flat 列表中的起始行）与
   `wrapDirty wrapSpan{from,to}`（内容被就地改写、wrap 已失效的块区间）。**两个字段，不是文档预设的一个**
   ——但它们同属 wrap_cache 的不变量，且按 §5.5 用等量删除抵消（合并了原 4 行字段注释为 2 行，净增 0 行）。
2. `setTranscriptBlock` 不再 `invalidateWrapFrom(index)`，改为 `markWrapDirty(index)`（只标记内容变了的块）。
3. `syncWrappedLines` 的增量路径：只重绕 dirty 区间与尾部新增块，把它们的行 **splice 进 flat 列表**，
   并把区间之后各块的偏移平移 `delta`；**区间之外的块保留已有 wrap，不重绕**。
4. `invalidateWrapFrom`（删除/截断用）改为 O(1) 截断到前缀末偏移（`wrapBlockOffsets[index-1]+len(block)`），
   不再 `flattenBlockWraps`。
5. `team_replay.go` 的两处直接改字段，改为调用 wrap_cache 的新封装 `m.setWrappedBlock(index, lines, contentW)`
   （`installWrappedBlock` 现在只是它的薄包装）。

**验收（均已落地）**：

- 等价性用例（核心）：`TestWrapCacheSuffixSyncMatchesFullRebuild` —— append ×4、早块重写、
  中部 `invalidateWrapFrom`、删除块、截断、off-loop 采用、宽度变更，每一步都与「整表重建」逐元素比对，
  并在失败时报出是**哪一步**分叉；
- 探针用例：`TestWrapCacheSuffixSyncRewrapsOnlyTheNewBlocks`（统计 `wrapTranscript` 调用数：
  追加 5 块只重绕 5 块；**早块重写只重绕 1 块**）。**变异验证**：把 `markWrapDirty` 换回
  `invalidateWrapFrom` 即报「an early rewrite re-wrapped 304 blocks, want 1」（红），而等价性用例仍绿
  ——两者分工明确；
- 另两条：`TestWrapCacheInvalidateDefersAndKeepsThePrefix`（invalidate 不重绕、前缀逐元素正确）、
  `TestWrapCacheSetWrappedBlockAdoptsAndTruncates`（采用块 + 后续 sync 与重建一致）。

**基准（`TestWrapCacheColdTierCostRecordsTheDrop`，400 块 / 本机 / 同一次运行）**：

| 臂 | ns/op | 说明 |
| --- | --- | --- |
| early-block rewrite + sync（**不含** viewport feed） | **5,537** | 修好后的 wrap 成本 |
| early-block rewrite + legacy rebuild（**测试内保留的旧实现**，不含 feed） | **1,966,960** | `invalidateWrapFrom` + 全量重绕 + `flattenBlockWraps` |
| early-block rewrite + sync（含 feed） | 516,984 | 帧路径的实际每消息成本 |
| tail rewrite + sync（含 feed） | 508,978 | 两个位置现在**同价**（§2.5 的前提被消掉） |
| `feedViewportContent` alone | 493,024 | O(transcript) 整份拷贝，**本次不改**，是地板 |

即 wrap 部分 **≈355×**（与 §2.5 的 55× 同量级；§2.5 那次 2.5 ms 是含 flatten 的旧实现）。
**实施中发现的一条顺序不变量**（与上面的变异验证是一对）：`setWrappedBlock` 的「整表就是这一块」
快路径（`wrapBlockCount == 0 && index == 0 && len(transcript) == 1`）**必须排在 `invalidateWrapFrom` 之前判定**。
落地时先写成了「先 invalidate、再判快路径」，于是快路径读到的是已被截断的状态，
`TestMemberReplayPaintsTailWindowThenRendersTheRestOffThread` 报 `wrap cache = (width 0, blocks 0), want (79, 1)`。
改回「条件在先、失效在后」即绿。变异验证证明「改了就红」，这条证明**「顺序错了也红」**——
快路径的判定是对**未失效状态**的断言，读晚了就不再是同一个断言。

**诚实结论**：帧路径上剩下的 ~0.5 ms 是 `feedViewportContent` 的整份拷贝（O(transcript)，本节点范围外），
所以每条消息从 ~2.5 ms 降到 ~0.5 ms，**不是降到 0**。本节的「≤2× 热态」目标在**不含 feed** 的口径下达成
（5,537 vs 尾部重写的同量级），含 feed 时两臂相等——因为地板相同。

### 5.5 `chat_tui.go` 的净行数约束（对 Part B 的硬约束）

`chat_tui.go` 已在 repolint 基线里（2250 行 > 800 行上限），而基线按 **(文件, 规则) 的加权总量**卡口
（`tools/repolint/baseline.go:51-80`）——行号位移不影响判定，但**净增行数会直接抬高 `file-size` 权重**。

因此 F-B2 新增的字段：**必须用同一文件内的删除来抵消**（例如合并两行注释、把一段内联逻辑
搬去 Part B 自己的文件），使 `chat_tui.go` 的行数**不增**。做不到就先回到拍板。
其余 Part B 文件都没超 800（`transcript.go` 578、`wrap_cache.go` 138、`team_replay.go` 343），有余量。

**落地结果（2026-09-22）**：F-B2 实际新增了**两个**字段（`wrapBlockOffsets []int`、`wrapDirty wrapSpan`，
理由见 §5.4），抵消方式是合并那段 4 行的 wrap 字段注释为 2 行——净增 **0 行**（2250 → 2250，
`wc -l` 核对；`repolint` 的 `chat_tui.go` 权重未变，实测无该文件的 finding）。

同理 Part A 注意：`chat_tui_team_switch.go` 现有 753 行，**距离 800 只剩 47 行**；
F-A2 的改动若放不下，把新逻辑开进 Part A 自己的新文件 `team_wake_batch.go`（新文件预算为 0，
所以新文件必须零 finding：不引入浮动注释、函数体 ≤120 行、复杂度 ≤30）。

---

## 6. 门禁

两段都必须跑到全绿（各自分支上）：

```bash
gofmt -l <changed files>                      # 必须为空
go build ./...
go vet ./internal/cli/
go test ./internal/cli/                       # 全绿
# `Frame` also matches TestTerminalSizeRecoveryFollowsTheTerminalUnderTheFrame,
# whose in-test race is documented as pre-existing (TEAM_TUI_AGENT_DECOUPLING_PLAN
# v0.12/v0.13); the -skip is what keeps it from reading as a regression.
go test -race ./internal/cli/ \
  -run 'Team|Cockpit|Replay|Roster|LeaderWait|WaitBus|Wake|Frame' \
  -skip TestTerminalSizeRecoveryFollowsTheTerminalUnderTheFrame -count=2
go run ./tools/repolint -root .               # 相对改动前零新增
$(go env GOPATH)/bin/golangci-lint run --timeout=5m ./internal/cli/...
```

**门禁口径（沿用上一轮的实测结论，避免误判）**：

- `repolint` 的既有红项（改动前就存在）：`chat_tui_team_render.go` essay 1、`chat_tui_team_reset.go` essay 2、
  `chat_tui_team_session.go` essay 7、`team_history_sync.go` essay 2、`team_replay.go` essay 5、
  `provider/anthropic/messages_usage.go` essay 1。**不允许新增**，包括因行数增长而抬高的权重。
- `golangci-lint` 的既有红项：`chat_tui_team_switch_test.go:79` SA4005。
- `internal/netclient` / `internal/plugin` / `internal/worktree` 整包红是**本机环境**（代理 / 真实网络 / git 2.34.1 < 2.38），
  与本路线无关，清掉代理环境变量后前两个即绿。
- `TestTeamTurnInjectsInboxAtSubmit` 全包偶发、隔离绿，属既有（`TEAM_MEMBER_PARALLELISM_ROUTE.md` §5.1）。
- `-race` 下把 `Wake` 放进 `-run` 曾命中 `TestEscalationWakeNamesTheQueue` 的测试内竞态，**上一轮已修**；
  现在该模式应当绿。
- **`Frame` 会顺带命中 `TestTerminalSizeRecoveryFollowsTheTerminalUnderTheFrame`**，其测试内竞态是
  `TEAM_TUI_AGENT_DECOUPLING_PLAN.md` v0.12/v0.13 记录的既有项（`tui_diagnostics_test.go:592` 的测试局部
  变量被 watchdog 回调并发写，栈内无本轮文件）。因此门禁命令里带 `-skip`。**两个 Agent 各自都先踩过
  一次**，才把这条写进命令本身。

**完成定义**：① 代码落地；② 该节点的验收用例存在且先红后绿（§5 每条验收都写了「去掉修复即红」的对照）；
③ §6 门禁实跑通过；④ §7 的该行状态与证据列更新。**「第一版补丁」或「定向测试变绿」不等于完节点。**

### 6.1 Part A 落地记录（2026-09-22）

**实际改动文件**（比 §4 清单多两个：新文件 `team_member_event_batch.go`，以及两处既有测试的适配）：

```
internal/cli/team_member_event_batch.go        （新增：drainReady + 保留区 hold/heldTurn/dropHeld/dropAllHeld）
internal/cli/team_event_pump.go                （held 字段 + 初始化）
internal/cli/chat_tui_team_switch.go           （waitForMemberEvent 带 bound + 可见过滤；handleMemberEvent 批量；bufferMemberEvent/replayMemberLiveEvents 改走 pump）
internal/cli/chat_tui_team_session.go          （删除 session.live 字段与初始化）
internal/cli/frame_drain_cost_test.go          （新增：6 条用例）
internal/cli/chat_tui_team_live_test.go        （既有：断言从 session.live 改读 pump 保留区）
internal/cli/chat_tui_team_footer_state_test.go（既有：同上）
internal/cli/p2_acceptance_test.go             （既有：改为按新契约「一批 + 一次重新武装」驱动）
```

**两个必须写下来的契约变化**（否则后续会被误读为回归）：

1. **`handleMemberEvent` 现在承载整批**。它路由被交给它的那条事件，再 `drainReady(64)` 抽干积压并**只重新武装一次**；被交给的那条在生产里就是队列头（只有它会把事件从队列取出来），实现按 `seq` 跳过它避免重复路由。`memberEvents == nil` 的裸 harness 直接返回 nil。
   **连带影响**：任何「自己 `next()` 一条、调一次 `handleMemberEvent`、计数 +1」的测试都会挂死——第一次调用就把队列抽干了。`p2_acceptance_test.go` 正是这个形状，已改为「先 `wg.Wait()` 让队列成为闭集，再一批一批 drain + route，最后单独断言重新武装」。**这是本轮唯一一次修改非本节点清单的测试，理由是新契约使该测试的驱动方式失效，不修就挂到超时。**
2. **`session.live` 的生命周期从会话级变成 registry 级**。保留区挂在 pump 上，`bindTeamBackends` 重建 registry 时随 pump 一起清空（`dropAllHeld` 由 `newMemberEventPump` 的新实例天然满足）。这正是 §5.2 标注的最大风险点，落地时的取舍是「接受」：保留区属于产生它的成员后端，后端走了它就该走。

**门禁实跑（Part A，本机）**：`gofmt -l` 干净；`go build ./...` 通过；`go vet ./internal/cli/` 干净；
`go test ./internal/cli/` 全包绿（71.7s）；`go test -race` 定向门禁见下；本段改动文件 `golangci-lint` 与
`repolint` 零新增（`repolint` 只剩 6 条既有 essay，与改动前逐条一致）。

### 6.2 两段合并后的最终验证（2026-09-22）

F0 / Part A / Part B 三块收口后，在**同一棵合并后的树**上由 Part A owner 独立复跑（不采信各自的
自报数字），三条 -race 里有一条是专门为「两段合并」加的：

```
go test ./internal/cli/                                            # ok 75.2s
go test -race ./internal/cli/ -run 'Team|Cockpit|Replay|Roster|LeaderWait|WaitBus|Wake|Frame' \
  -skip TestTerminalSizeRecoveryFollowsTheTerminalUnderTheFrame -count=2   # ok 76.7s
go test -race ./internal/cli/ \
  -run 'TestRenderTranscript|TestWrapCache|TestWrapPads|TestFrame|TestLeaderWait|TestWaitBus|TestMemberEventPump' \
  -count=2                                                         # ok 62.9s  ← 两段用例混跑
go run ./tools/repolint                                            # 6 条既有 essay，逐条一致
```

第三条是必要的：前两条各自只覆盖一段，**「两段合并后互不干扰」单跑各自的模式证明不了**。
三条均无 DATA RACE、无超时。

**引用收益的硬规矩**：两个节点在同一份 `View()` 基准上叠加（F-B1 报的 `View()` 237,341 → 132,716
里含 Part A 的 F-A2——后台 delta 不再产生消息）。因此**引用任一节点的收益必须引 §5.4 的分臂表，
不得引 `View()` 总数**；`View()` 总数只能作为两段合并后的整体读数。

**状态**：三块代码均已完成并过门禁，工作区无在飞改动。**未提交** —— 工作区是共享的，提交/合并由用户决定。

**`-race` 门禁的一处口径修正**：§6 给的 `-run` 模式里 `Frame` 会**顺带命中
`TestTerminalSizeRecoveryFollowsTheTerminalUnderTheFrame`**，而该用例的测试内竞态是
`TEAM_TUI_AGENT_DECOUPLING_PLAN.md` v0.12/v0.13 早就记录的既有项（`tui_diagnostics_test.go:592` 的测试
局部变量被 watchdog 回调并发写，栈内无本轮文件；两个文件本轮谁都没改）。因此门禁跑该模式时**必须
`-skip` 掉它**，否则会把一条既有竞态读成本轮回归。

---

## 7. 节点状态

| 节点 | 主题 | 所属 | 状态 | 证据 |
| --- | --- | --- | --- | --- |
| F0 | 测量脚手架 + 基线数字（共用，冻结） | — | **已完成** | `internal/cli/frame_cost_fixture_test.go`（156 行，新增）；`TestFrameCostBaseline` 复现 §2.2 全部五条数字，`TestFrameCostIsFlatInTranscriptLength` 复现「单条成本与 transcript 长度无关」（1/201/1001/3001 块：47–50 µs）。`gofmt` 干净、`go test ./internal/cli/` 全包绿（63.9s）、repolint 与 golangci-lint 对新文件零新增 |
| F-A1 | 批量 drain（一次 Update 处理一批） | Part A | **已完成** | `chat_tui_team_switch.go` 的 `handleMemberEvent`（路由到达的那条 + `drainReady(memberEventBatchLimit)` 抽干积压，只重新武装一次）与 `team_member_event_batch.go` 的 `drainReady`；`frame_drain_cost_test.go` 的 `TestOneMemberEventDrainsTheWholeBacklog`、`TestBatchKeepsPerEventSemantics`。变异：去掉 drain 调用即红（已验） |
| F-A2 | 不可见事件不产生消息（保留区迁到 pump） | Part A | **已完成** | `waitForMemberEvent(pump, bound)` 的可见过滤 + `team_member_event_batch.go` 的 `hold`/`heldTurn`/`dropHeld`/`dropAllHeld`；`session.live` 字段已删除。用例：`TestBackgroundDeltaDoesNotBecomeAMessage`（负向断言：只有不可见事件时等待必须继续阻塞）、`TestRetainedTurnReplaysInOrderAndClears`、`TestRetainedTurnEndsWithThePump`、`TestHoldAppliesTheQueuePolicies`。变异 4 处（去掉可见过滤 / cap 丢最新 / TurnDone 不清 hold / 重放不清 hold）全部去掉即红（已验） |
| F-B1 | `renderTranscript` 去 `JoinHorizontal`（字节等价门禁） | Part B | **已完成** | `internal/cli/transcript.go` 的 `renderTranscript` 改为逐行 `line + scrollbarCell(...)`；`renderTranscriptRow` 作为等价性参照保留。用例 3 条（`frame_render_cost_test.go`：7 形状表驱动字节比对、宽字符/emoji 行、带样式行）+ 1 条不变量（`TestWrapPadsEveryLineToTheContentWidth`）；变异验证：行尾去掉 `cw` 补齐即 4 形状转红。基准（F0 的 `TestFrameCostBaseline`，同一模型前后对照）：`View()` **237,341 → 132,716 ns/op**（本机），低于文档预期的 −150 µs 量级，原因见 §5.3 的落地结果 |
| F-B2 | 包装缓存增量维护（消冷档位） | Part B | **已完成** | `internal/cli/wrap_cache.go` 的 `wrapBlockOffsets`/`wrapDirty`/`markWrapDirty`/`rewrapDirtyBlocks`/`setWrappedBlock`；`transcript.go` 的 `setTranscriptBlock` 改记 dirty 区间；`team_replay.go` 改走 `setWrappedBlock`；`chat_tui.go` 加两个字段并用等量删除抵消（净增 0 行，repolint 的 `chat_tui.go` 权重不变）。用例 5 条（`frame_wrap_cache_test.go`）；变异验证：`markWrapDirty` 换回 `invalidateWrapFrom` 即探针报「re-wrapped 304 blocks, want 1」而等价性用例仍绿。基准：wrap 部分 1,966,960 → 5,537 ns/op（≈355×），帧路径含 `feedViewportContent` 地板 516,984 → 见 §5.4 的表 |
| F-C1 | **段落内实时预览 + 重绘节流**（2026-09-23，用户报「输出停顿后一次跳一大段，只有被选中成员这样」） | 追加 | **已完成** | `internal/cli/answer_stream.go`（新文件：`answerPaintInterval`/`streamAnswer`/`commitPending`/`resetAnswerStream`/`flushableMarkdownPrefix`/`streamedMarkdownPreview`/`openMathAt`，自 `chat_tui_stream.go` 抽出并改写）；`chat_tui.go` 的 `answerPainted`/`answerPaintedAt`；`chat_tui_stream.go` 的 `clearTranscriptDisplay` 调 `resetAnswerStream`。**根因实测**：`flushableMarkdownPrefix` 只到最后一个空行，一段没有空行的回答（段落/列表/长围栏）此前**一个字节都不显示**，直到空行到达才一次性画出来（探针：5200 字节累积、painted=0、`answerIdx=-1`）。用例 5 条（`answer_stream_preview_test.go`）：预览函数表（含 `$`/`$$` 未闭合、开围栏、行尾）、首 chunk 即开块、节流窗口内不重绘而窗口外重绘、闭合块立即重绘、`clearTranscriptDisplay` 关闭答案块。**节流依据**：单次重绘 O(回答长度)（3000 chunk 段落流：首条 0.77ms、81KB 时 4.32ms），逐 chunk 重绘是 O(n²) 且会经 pump 的 delta 合流再次变成「跳一大段」。**实测**：200 chunk 段落流，painted 从 0 变为随流增长（200ms/1040B 累积 vs 843B 已绘），3000 chunk 节流后均值 248 µs/chunk。**附带修复**：`clearTranscriptDisplay` 原先不复位 `answerIdx`/`answerFlushed`，`handleReplayReadReady` 走该路径且无 `finalizeStreamed`，会让下一个 chunk 把回答写进重放块（既有缺陷，预览使其更易触发） |

状态取值：`未开始` / `进行中` / `待验收` / `已完成` / `阻塞`。**阻塞必须写真实原因，不得改写为通过。**

---

## 8. 明确不做

| 项 | 为什么不做 |
| --- | --- |
| **把选择交回终端**（团队会话不捕获鼠标） | 用户拍板取消：当前需要鼠标交互（点击 `[TEAM]`/成员按钮）。因此「拖选卡」只能从事件循环容量侧解决（F-A1/F-A2），不能靠放弃捕获绕过。 |
| **改 `memberEventMsg` 的消息类型** | 13 个测试文件构造它；F-A1 改成在处理侧抽干积压，收益相同而返工面为零。 |
| **新增 `chat_tui.go` 的 Update 分支** | 该文件的复杂度/体量已在 ratchet 基线上（§5.5）；F-A1/F-A2 都不需要新分支。 |
| **换自定义 renderer / 关掉 bubbletea 的逐消息 `View()`** | 那是重写渲染层，超出本轮范围；F-B1 把 `View()` 本身降到足够便宜是更小的改动。 |
| **`bottomRows` 记忆化 / 渲染路径的其他微优化** | `TEAM_MEMBER_PARALLELISM_ROUTE.md` §4.2 A5 已实测 7–15 µs/帧（<0.1% 单帧预算）并决定不做；本轮不推翻。 |
| **把未完成尾部单独渲染成纯文本**（F-C1 的备选） | 会让 `buildCopyTranscript` 复现不出可见块，从而静默关掉整段 transcript 的选区复制（`transcript_copy.go:197` 的逐行等值门禁），并把一份 markdown 文档劈成两段渲染（有序列表重新计数、表格/引用分块）。改走「整份 `raw` 一起渲染、只截断未闭合的数学跨度」后，`transcriptSource` 无需新字段，复制与 resize 路径一行不改。 |
| **动成员的 provider/agent 侧做 delta 限流** | 那会改变模型可见的行为且落在冻结目录；本文档只在宿主侧消费处治理。 |
| **`feedViewportContent` 的 O(transcript) 整份拷贝**（帧路径剩余的 ~0.5 ms 地板） | 它是 F-B2 修完之后帧路径上剩下的大头（`wrap_cache.go:80-88` 每次把整张扁平行表 clone 给 viewport）。本次不做：改它要动 bubbles viewport 的持有语义（`SetContentLines` 的所有权），风险与收益不匹配。**后果必须写明**：每条成员消息的成本从 ~2.5 ms 降到 ~0.5 ms，**不是降到 0**；团队越长、历史越厚，这条地板越高。 |

---

## 9. 风险与未决

1. **F-A2 的缓冲所有权**（最大风险）：把 `session.live` 迁到 pump 会改变重放缓冲的生命周期
   （registry 重建即清空）。实现前必须确认这是可接受的，并保证 `TurnDone` 的「结算即清缓冲」语义不变。
   若评估后认为风险不可接受，**退路是只做 F-A1**（批量 drain 已能消掉绝大部分循环占用，因为它把
   N 条积压折成 1 次 `Update`），F-A2 单独立项。
2. **F-A1 的批量上限**：上限过大 → 输入时延上界变大；过小 → 收益打折。建议 64 并用基准定标。
   若边界取值需要用户拍板，先回报。
3. **F-B1 的 ANSI 字节等价**：追加拼接与 `JoinHorizontal` 在极端样式下可能不等价。
   门禁要求 `bytes.Equal`，不等就停在实现里解决，**不允许**放宽成「目测一致」。
4. **F-B2 的 `wrapPrefixLines` 回退成本**：见 §5.4 第 3 点，实现者用基准决定取舍并记录。
5. **两段的收益会互相掩盖**：A 减少消息数、B 降低每条成本，各自单独测才能归因。
   因此 F0 的基线数字与各自的对照基准是必做项，不是可选项。

---

## 附录 A：证据索引

### A.1 事件产生与消费

| 结论 | 位置 |
| --- | --- |
| 每个流式 chunk 一个事件 | `internal/agent/agent.go:1470,1474` |
| 一条事件一次 `tea.Msg`、处理完重新武装 | `internal/cli/chat_tui_team_switch.go:27,49,77` |
| `memberEventMsg` 定义 | `internal/cli/chat_tui_team_switch.go:35-36` |
| 唯一的 `case memberEventMsg` | `internal/cli/chat_tui.go:1547` |
| pump 的合并/淘汰/上限 | `internal/cli/team_event_pump.go:60-223` |
| 后台成员重放缓冲 | `internal/cli/chat_tui_team_switch.go:93-140`；`internal/cli/chat_tui_team_session.go:339,367` |

### A.2 bubbletea 的逐消息渲染

| 结论 | 位置 |
| --- | --- |
| 消息通道无缓冲 | `charm.land/bubbletea/v2@v2.0.9/tea.go:606` |
| 每条消息 `Update` → cmd → `View()` | `tea.go:880`、`:888`（`p.render` → `model.View()`） |
| 帧率只节流**写出**，不节流 `View()` | `tea.go:1401-1428`（`defaultFPS = 60`，`renderer.go:13`） |

### A.3 渲染与包装缓存

| 结论 | 位置 |
| --- | --- |
| `renderTranscript` 每帧 `JoinHorizontal` | `internal/cli/transcript.go:447-475` |
| 包装缓存不变量与后缀路径 | `internal/cli/wrap_cache.go:1-58` |
| 整表重建与整份拷贝 | `internal/cli/wrap_cache.go:60-88,99-113` |
| `invalidateWrapFrom` 截断计数 | `internal/cli/wrap_cache.go:128-138` |
| 重放路径直接改缓存字段 | `internal/cli/team_replay.go:327-341` |
| 鼠标捕获开关 | `internal/cli/chat_tui_team_render.go:656-661`；拖拽入口 `internal/cli/chat_tui.go:723` |

### A.4 门禁口径

| 结论 | 位置 |
| --- | --- |
| 基线按 (文件, 规则) 加权总量卡口 | `tools/repolint/baseline.go:51-80` |
| 上限：文件 800 行、函数 120 行、复杂度 30、注释块 15/3 行 | `tools/repolint/size.go:8`、`complexity.go:10-12`、`comments.go:12-18` |
| `chat_tui.go` 当前 2250 行 | `wc -l internal/cli/chat_tui.go` |
