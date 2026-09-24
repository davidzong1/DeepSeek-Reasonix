# 实现方案 —— 共享结论黑板：成员主动提交，增量同步，leader 默认看不见

> 状态：**方案，未实施**。Part A 与 Part B 文件集不相交，两个 Agent 并行，合流前不改对方的文件。
> 现状（2026-09-24）：共享板 `shared` 已在 team session 里记下任务状态，并把 leader 指令注入成员下一回合。结论物化视图 `boardView`（`internal/boot/team_runtime_host.go`）没有接到 CLI。`WireTeamInjector` 只有测试调用。leader 自动读到的只有唤醒行 `task <id> reported`（`EventWakeup`），没有结论正文。
> 目标：成员把会影响主线的发现写成一条短结论；其他成员在**下一次模型调用之前**只收到这次新增的结论。板上没有新结论时，成员提示词一个字节都不加。leader 的回合、`leader_wait`、状态轮询都不带结论；leader 只有主动调用工具才读到。

---

## 0. 结论

1. **结论是成员主动写的，不是回报，也不是任务流水。** 写 `EventConclusion`，并按 topic 修订 `board_conclusions`。不写 `EventWakeup`，不调用 `notifyAttention` / `wakeAll`，不改 `member_report_result`。
2. **自动同步只给非 leader 成员，且只在有新结论时发生。** 注入点是该成员 `runToolLoop` 里、每次采样之前。空增量不追加消息，前缀与今天逐字节相同。
3. **每个成员一条独立游标，只吞 `conclusion`。** 与指令 inbox、leader 唤醒游标分开。第一次见到该成员时把游标放到当前尾部，不回放历史。之后只渲染游标之后的新结论。
4. **leader 能读同一块板，但没有任何自动路径会把结论放进它的上下文。** 主动工具读的是当前 topic 列表，不推进成员游标。
5. **正文进不了黑板。** topic 与 summary 有字数上限。超长直接拒绝，不截断后假装已保存。长文走已有的 `member_publish_deliverable`，结论里只留 id。

---

## 1. 不做什么

| 不做 | 原因 |
| --- | --- |
| 调用 `WireTeamInjector` 或 CLI 里的 `boardView` | 那是整表结论，每回合都注入，违反「只在更新时、只同步增量」 |
| 把 L0 索引放进成员提示 | 没有新结论时也要占 token |
| 自动同步 `assignment` / `command` / `wakeup` / `report` | 这些不是结论；混进去会让任务流水把每个成员的前缀打穿 |
| 在 `leader_wait`、`leader_check_member_status`、唤醒摘要里附上结论 | leader 默认不可感知 |
| 用私有板 `private/<member>` 装共享结论 | 其他成员读不到，共享就不成立 |
| 把结论写进 team knowledge | 知识库是另一条存储；重大缺陷进黑板的约定不变 |
| 回放该成员入座前的旧结论 | 第一次只建游标。要历史时 leader 或成员以后另说，本方案不回放 |
| 把发布者自己刚写的那条再注入给自己 | 工具回执里已经有。游标仍要越过它，避免下次补投 |
| 改系统提示、原子工具、回报裁剪 | 与本功能无关。技能只加下面规定的那几句 |

---

## 2. 并行边界

两个 Agent 同时开工。合流之前：

- 只改自己的文件。
- 不改本方案的状态行。
- 不跑对方包里的测试来顺手修。
- 不把技能复制到 `~/.reasonix/team/skills/`。运行中的成员读的是那棵树；半边落地会让技能描述一个还不存在的行为。复制留到合流。

| | Part A | Part B |
| --- | --- | --- |
| 内容 | 结论的写入、增量读取、leader 主动读、两个工具、技能里的投稿规则 | 成员每次思考前拉取增量并写入会话；leader 与普通会话不挂钩子 |
| 文件 | `internal/team/conclusion_feed.go`（新建）、`internal/team/conclusion_feed_test.go`（新建）、`internal/cli/team_conclusion_tools.go`（新建）、`internal/cli/team_conclusion_tools_test.go`（新建）、`internal/cli/team_member_tools.go`（只加两处工具注册）、`internal/cli/team_member_tools_test.go`（只把两个名字补进期望名单）、`team/skills/base/member/SKILL.md`、`team/skills/base/leader/SKILL.md` | `internal/agent/board_delta.go`（新建）、`internal/agent/board_delta_test.go`（新建）、`internal/agent/run_loop.go`（只在 `applyQueuedSteers` 之后加一次调用）、`internal/cli/team_conclusion_inject.go`（新建）、`internal/cli/team_conclusion_inject_test.go`（新建）、成员 backend 绑定处设钩子的那一个函数（实现时定位 `bindBackend` 附近，只给 `TeamRole == member` 的 executor 设钩子） |
| 不许碰 | `internal/agent/`、`run_loop.go`、`team_conclusion_inject.go` | `internal/team/conclusion_feed.go`、`team_member_tools.go`、两份 `SKILL.md`、`team_conclusion_tools.go` |

合流才把 Part B 的注入器接到 Part A 的 `ReadConclusionDelta` / `AckConclusionDelta`。合流前 Part B 的单测用打桩的 `BoardDeltaFunc`，不引用 `conclusion_feed.go`。

---

## 3. 冻结合同（两边都按这个实现，不许单方面改字段）

### 3.1 常量

| 名字 | 值 | 含义 |
| --- | --- | --- |
| `conclusionTopicMaxRunes` | 48 | topic，按 rune |
| `conclusionSummaryMaxRunes` | 160 | summary，按 rune |
| `conclusionDeltaMaxItems` | 8 | 一次思考最多注入的条数 |
| 消费者 id | `"conclusions:" + memberID` | 与指令 inbox（消费者 = memberID）、leader 唤醒游标（消费者 = leaderID）错开 |

板 id 固定 `team.BoardShared`。kind 固定 `team.EventConclusion`。

### 3.2 Part A 导出的函数

```go
func PostConclusion(ctx context.Context, store team.BoardStore, id team.Identity, taskID team.TaskID, topic, summary string) (team.Conclusion, error)

func ReadConclusionDelta(ctx context.Context, store *team.SQLiteStore, memberID string) (text string, advanceTo int64, err error)

func AckConclusionDelta(ctx context.Context, store *team.SQLiteStore, memberID string, advanceTo int64) error

func ListConclusions(ctx context.Context, store team.BoardStore, id team.Identity, limit int) ([]team.Conclusion, error)
```

`PostConclusion`：

- `id.MemberID`、`topic`、`summary` 去空白后为空，返回错误，不写板。
- topic 或 summary 超过上限，返回错误，不截断、不写板。
- 同一 topic 已有结论时，带当前 `BaseEpoch` 做一次 CAS 修订。冲突再读一次 epoch 并重试一次，第二次仍冲突则把错误返回给工具。
- `ClientMsgID` = `memberID + "\x00" + topic + "\x00" + summary 的 sha256 前 16 字节的十六进制`。完全相同的重放返回原事件，不产生新 seq。
- 成功后的结论摘要就是传入的 summary。不附加唤醒事件。

`ReadConclusionDelta`：

- 没有游标行：读 `board_events` 上该板的 `MAX(seq)`（没有事件则为 0），把游标写到这个尾部，返回 `text=""`、`advanceTo=0`。这次写游标是本函数自己完成的，调用方不要再 Ack。
- 有游标：`ReadAfter`，`Kind=EventConclusion`，`Limit=conclusionDeltaMaxItems`，`Stamped.MemberID=memberID`。
- 丢掉 `MemberID == memberID` 的事件（自己的投稿）。
- 若剩下零条：`text=""`。若这页里其实有自己的事件或被滤掉的结论，`advanceTo` 仍是这页最后一条的 seq，调用方 Ack 后游标越过它们。
- 有别人的结论时，`text` 是下面的格式，`advanceTo` 是**本页最后一条**的 seq（含被自己滤掉的），不是历史上的最大 seq。后面还有结论时，留到下一次思考。
- 读失败返回错误和空 text。调用方遇到错误不注入、不 Ack。不要把上一份视图再贴一遍。

文本格式（前缀缓存只在有新结论时多出这一段）：

```text
[board delta]
- <memberID> <topic>: <summary>
```

每个结论一行。topic 来自该事件对应的 conclusion 修订；事件上没有 topic 字段时，Part A 在 `PostConclusion` 里把 summary 存成 `topic + "\n" + summary`，读增量时按第一个换行拆回。不要在提示里写 seq、epoch、digest。

`AckConclusionDelta`：把消费者 `"conclusions:"+memberID` 的游标推进到 `advanceTo`。`advanceTo <= 0` 时不做任何事。拒绝倒退（沿用 `ErrCursorBackwards`）。

`ListConclusions`：`ReadView(BoardShared, ViewSpec{Limit: limit})`。`limit <= 0` 时用 16，上限 16。不推进任何游标。`id.MemberID` 为空则拒绝。

### 3.3 Part B 的钩子

```go
// BoardDeltaFunc 返回要追加的用户消息。空字符串表示这次思考前什么都不写。
type BoardDeltaFunc func(ctx context.Context) (string, error)

func (a *Agent) SetBoardDelta(fn BoardDeltaFunc)
```

`runToolLoop` 在 `applyQueuedSteers` 成功返回之后、`providerToolSchemas` 之前调用。`fn == nil` 或返回空字符串：不写会话，不改变 prefix shape。返回非空：追加一条 `user` 消息，内容就是该字符串，然后再采样。返回错误：这次思考跳过注入，继续采样，不把错误写进提示词。

Ack 在消息追加成功之后。追加失败则不 Ack，下一次思考会再拿到同一段。

---

## Part A —— 写入、隔离、主动读

### A1. `conclusion_feed.go`

按 §3.2 实现。存储只用现有 `BoardStore` / `SQLiteStore`：`Append`（带 `Conclusion` 修订）、`ReadAfter`、`GetCursor`、`AdvanceCursor`、`ReadView`。不新开表。

尾部 seq 用一条查询：`SELECT COALESCE(MAX(seq),0) FROM board_events WHERE board_id = ?`。把它放在 `conclusion_feed.go` 的私有函数里，不改 `ReadAfter` 的空页语义。

首次建游标与「已有游标」的区分以 `GetCursor` 的错误为准（与 `consumeWakeups` 相同：没有行才是首次）。已有行且 `LastSeq == 0` 表示尾部本来就是 0，按正常增量读，不要再次当成首次而跳过以后的结论。

### A2. 工具

新建 `internal/cli/team_conclusion_tools.go`，不往 `teamTaskTool` 上加分支。

`member_post_conclusion`（只注册在 `newMemberTaskTools`）：

- 参数：`topic`（必填）、`summary`（必填）。没有 `task_id` 参数；任务 id 用该成员当前未完成任务，没有任务就写空 task id。
- 描述一句话：`Post one short conclusion that can change the main task. It is not a status report and does not finish the task.`
- schema 的 `summary` 说明：`One sentence, at most 160 characters. Longer text is refused; publish a deliverable and quote its id.`
- 回执只含 topic 与 `stored` 或错误原文。不把整块板贴回去。
- 工具分类：可写、非只读。不要把它算进会结束任务的路径。

`leader_read_conclusions`（只注册在 `newLeaderTaskTools`）：

- 参数：无。
- 描述一句话：`Read the current shared conclusions. They are not delivered to this leader unless this tool is called.`
- 回执用与增量相同的行格式，但标题是 `[board conclusions]`，每行 `- <memberID> <topic>: <summary>`。空板返回 `[board conclusions]` 加一行 `(none)`。
- 只读。

`team_member_tools_test.go` 的 `wantMember` 增加 `member_post_conclusion`，`wantLeader` 增加 `leader_read_conclusions`。不要改其他名字的顺序以外的内容；新名字追加在各自切片末尾。

### A3. 技能（只改仓库里的两份）

`team/skills/base/member/SKILL.md` 的 Execute 节加一条：

- 发现会改变主线任务下一步的事实时，调用 `member_post_conclusion`：一个 topic、一句话。不要用它汇报进度、贴文件内容或代替 `member_report_result`。长文用 `member_publish_deliverable`，summary 里只引用 id。别人的新结论会在你下一次思考前以短增量出现；没有更新时不会出现。不要为了看黑板去轮询。

`team/skills/base/leader/SKILL.md` 加一条：

- 成员的共享结论不会进入 `leader_wait` 和成员状态。需要时调用 `leader_read_conclusions`。

### A4. 测试

`conclusion_feed_test.go`：

- 超长 topic、超长 summary、空 topic 都不产生事件。
- 同一 topic 第二次投稿修订 epoch，板上仍是一条当前结论。
- 相同 member+topic+summary 重放不增加 seq。
- `PostConclusion` 之后 `ReadAfter` 过滤 `EventWakeup` 为空。
- 成员 m1 首次 `ReadConclusionDelta` 在已有结论时返回空文本，游标在尾部；m2 再投稿后 m1 只看到 m2 那一行。
- m1 自己的新结论不出现在 m1 的 text 里，Ack 之后再读仍为空。
- 超过 8 条时第一次 text 只有 8 行，Ack 后第二次拿到余下的。
- `ListConclusions` 不改变 `conclusions:m1` 的游标。

`team_conclusion_tools_test.go`：

- 成员工具成功路径回执不含 `[board delta]`。
- leader 工具回执含投稿的 topic，且不调用唤醒。
- 成员工具名单里没有 `leader_read_conclusions`；leader 名单里没有 `member_post_conclusion`。

### A5. 验收命令

```bash
gofmt -w internal/team/conclusion_feed.go internal/team/conclusion_feed_test.go internal/cli/team_conclusion_tools.go internal/cli/team_conclusion_tools_test.go internal/cli/team_member_tools.go internal/cli/team_member_tools_test.go
go test ./internal/team/ ./internal/cli/ -count=1 -run 'TestConclusion|TestMemberPostConclusion|TestLeaderReadConclusions|TestTeamMemberTools|TestLeaderTaskTools'
```

---

## Part B —— 下次思考前的增量

### B1. 钩子

按 §3.3 实现 `SetBoardDelta`。钩子持有的函数在采样前调用，追加使用会话现成的用户消息追加路径（与 steer 写入同一类消息，前缀不另加「中途补充」那句）。空结果不得分配新的 message id，也不得标记 content rewrite。

`board_delta_test.go`：

- `fn == nil`：一轮采样前消息条数不变。
- 返回 `""`：消息条数不变。
- 返回一段文本：下一次请求的最后一条用户消息就是这段文本，且只追加一次；下一次返回空则不再增长。
- 返回错误：消息条数不变，采样仍然发出。

测试可以打一个假的采样器，不必连真实 provider。

### B2. 成员绑定

`internal/cli/team_conclusion_inject.go`：

```go
func conclusionDeltaFunc(store *team.SQLiteStore, memberID string) agent.BoardDeltaFunc
```

合流前这个函数可以先接收一个接口，单测不依赖 Part A：

```go
type conclusionDeltaReader interface {
    Read(ctx context.Context, memberID string) (text string, advanceTo int64, err error)
    Ack(ctx context.Context, memberID string, advanceTo int64) error
}
```

行为：

- `Read` 得到空 text：不把任何字节交给钩子，即使 `advanceTo > 0` 也要 Ack（自己的投稿被滤掉、或本页没有可显示行时，游标必须往前，否则每次思考都读到同一页）。
- `Read` 得到非空：钩子返回该 text；**Ack 放在钩子外、消息写入成功之后**。实现时让 `BoardDeltaFunc` 只负责返回文本，CLI 侧在 `SetBoardDelta` 的闭包里先 Read，把 text 交给 agent；agent 追加成功后由闭包里注册的完成回调 Ack。若 agent 侧追加失败，完成回调不被调用。
- 为了让 Part B 在不改 Part A 的情况下测完，完成回调放在 `BoardDeltaFunc` 的闭包里：闭包返回 text 的同时，用 `runtime.Goexit` 做不到。改为 `BoardDeltaFunc` 的实现在返回 text 之前不 Ack；agent 在 `append` 成功后调用可选的 `BoardDeltaAck func()`，由闭包设置。`SetBoardDelta` 的签名保持一个函数；Ack 用配对的：

```go
func (a *Agent) SetBoardDelta(fn BoardDeltaFunc, ack func())
```

`ack` 只在本次确实追加了非空消息且追加成功时调用。空 text 的 Ack（越过自己的投稿）在闭包内、返回空字符串之前完成，不经过 agent。

leader 的 backend 不调用 `SetBoardDelta`。非 team 会话不调用。成员切换绑定到另一个 memberID 时，替换闭包，旧闭包不再被调用。

`team_conclusion_inject_test.go` 用假的 reader：

- 连续两次 Read 都为空：钩子返回空，Ack 仍被调用（advanceTo 为该页尾部时）。
- Read 返回一行：第一次钩子返回该行，成功追加后 Ack 一次；下一次 Read 为空则不再追加。
- Read 返回错误：钩子返回空和 nil 错误（错误吞掉），不 Ack。

### B3. 验收命令

```bash
gofmt -w internal/agent/board_delta.go internal/agent/board_delta_test.go internal/agent/run_loop.go internal/cli/team_conclusion_inject.go internal/cli/team_conclusion_inject_test.go
go test ./internal/agent/ ./internal/cli/ -count=1 -run 'TestBoardDelta|TestConclusionInject'
```

---

## 4. 合流

1. 把 `conclusionDeltaFunc` 接到 `ReadConclusionDelta` 与 `AckConclusionDelta`。删除临时接口，或让 `*team.SQLiteStore` 的薄包装实现它。包装只放在 `team_conclusion_inject.go`。
2. 增加一个 CLI 测试：m2 `PostConclusion` 之后，m1 的 delta 函数返回恰好一行；leader 的 executor 上 `BoardDelta` 仍为 nil；`leader_wait` 的结果字符串不含该 summary。
3. 把仓库里两份技能的新增句子复制到 `~/.reasonix/team/skills/base/member/SKILL.md` 与 `leader/SKILL.md` 的同样位置。不要跑 `make install-team-skills`（它会用仓库覆盖整棵用户技能树）。
4. 跑 Part A 与 Part B 的验收命令，再跑：

```bash
go test ./internal/team/ ./internal/cli/ ./internal/agent/ -count=1 -run 'TestConclusion|TestBoardDelta|TestConclusionInject|TestLeaderWait'
```

合流通过的标准：

- 成员思考在板上无新结论时，请求消息与未接钩子时一致。
- 另一成员投稿后，下一次采样前多出一段 `[board delta]`，且只有新行。
- 同一段不会在后续思考里再出现。
- leader 会话的消息里没有 `[board delta]`，除非模型调用了 `leader_read_conclusions` 并且工具回执在上下文里。
