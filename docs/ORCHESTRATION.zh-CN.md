# 编排：用声明式计划驱动会话自身的派发

<a href="./ORCHESTRATION.md">English</a>

状态：**已交付并在第 2 阶段收口**——spec、编译器、runner 与 host tool 均已交付。当前可运行 `agent` 节点；`tool` 与 `reduce` 节点在库层面已可编译运行，但在 host tool 的边界被拒绝（见[当前限制](#当前限制)）。第 3 阶段本应解除该边界，现已**取消**：其前置的预注册测量显示，本工作负载的工具结果增长中，只有约 23% 是闭合算子能够吸收的形态，而门槛为 80%。测量过程、数据与局限见设计文档；工具为 `tools/contextgrowth/`。设计动机与论证见 [`agent_architecture_decision.md`](agent_architecture_decision.md)，本文说明实际交付的 API。

**这不是什么。** 它不是省上下文的功能。支撑该说法的测量已经跑过，答案是否定的：本工作负载的工具结果增长中，只有约 23% 是闭合算子能够吸收的形态，而门槛为 80%，因此第三阶段已取消，`tool`／`reduce` 节点也无法从本工具触达。`orchestrate` 的价值更窄，且值得直说——它是一种带有**声明式权限主张**（`read_only` 是主张，由宿主与自身答案取合取，而非授权）与 **id 级交接**（`$nodes.<id>.<field>` 解析为 receipt id，绝不是正文）的计划格式。`fleet` 能表达同样的图，但表达不了这两者。如果你想要的是更好用的 `fleet`，就是它；如果你为省 token 而来，这里没有。

本能力不新增运行时。计划是数据，它跑的每个节点都是会话本来就会发起的调用：`agent` 节点走 `task` 的 `RunProfileSpec`，调度器、写声明、子 transcript、checkpoint 存储与证据 ledger 全部复用会话既有的那一份，而不是另建副本。

## Spec

`orchestrate` 只接收一个对象，其中唯一的必填字段是 `spec`。解码器拒绝未知键（`DisallowUnknownFields`），因此拼写错误是报错，而不是被静默忽略的声明。

```json
{
  "spec": {
    "version": 1,
    "mode": "sequence",
    "nodes": [
      {"id": "survey", "kind": "agent", "prompt": "列出 parseNodeRefs 的调用点。",
       "outputs": [{"name": "sites", "kind": "list"}], "read_only": true},
      {"id": "patch", "kind": "agent", "needs": ["survey"],
       "prompt": "把重命名应用到 $nodes.survey.sites 指定的调用点。",
       "write_paths": ["internal/agent"]}
    ]
  }
}
```

| 字段 | 含义 |
| --- | --- |
| `version` | 只接受 `1`。 |
| `mode` | `sequence`、`parallel`、`pipeline`——见[模式](#模式)。 |
| `nodes[]` | 计划本体，按声明顺序排列。1 到 64 个节点。 |
| `reduce` | 可选的 `{operator}`，用于 `fanout_reduce`。 |
| `caps` | 可选的请求，只能**收窄**会话的上限，永远不能放宽。 |

节点包含 `id`、`kind`，以及可选的 `needs`、`profile`、`prompt`、`outputs`、`max_tokens`、`write_paths`、`read_only`。`tool` 节点还会声明 `tool` 与其 `args`。

格式中没有条件、循环、计算出的边，也没有脚本字段。这个"没有"本身就是防漂移机制：需要这些能力的计划，应当是这个格式**表达不了**的计划，而不是被半表达出来的计划。

### 引用

节点通过 `$nodes.<id>.<field>` 引用更早节点的结果，该串可以出现在该节点 JSON 参数的任意位置。三条规则：

- `<id>` 必须是已声明节点，且是该节点的**传递前驱**——前向引用与自引用都会被拒绝。
- `<field>` 必须在该生产者自己的 `outputs` 中声明。未声明的字段是拒绝，不是空字符串。
- 跨边界的只是 **id**，永远不是正文。生产者的文本留在 host 侧；下游节点拿到的是可引用的句柄。若生产者没有返回引用，消费方会得到一个具名拒绝，而不是把字面量 `$nodes.a.out` 当作 id 塞给工具。

## 模式

| 模式 | 形状 | 写者 |
| --- | --- | --- |
| `sequence` | 全序：任意两个节点之间都有边给出先后 | 允许，因为顺序可证串行 |
| `parallel` | 任何节点都不声明 `needs` | 拒绝 |
| `pipeline` | 链式：每个节点最多一个 `needs` | 拒绝 |
| `fanout_reduce` | 先生产，再用一个 `reduce` 节点归约 | 拒绝 |

`read_only: true` 是**声明，不是授权**。有效权限是该声明与会话自身答复的**合取**，因此节点无法放宽会话允许的范围。`sequence` 之外的写者在任何东西启动前就被拒绝，因为只有在全序里，边才让顺序可证串行。

## 运行计划

```go
import "reasonix/internal/agent"

plan, err := agent.Compile(spec, agent.ValidateOptions{
    AllowAgentNodes: true,          // 装配时绑定，永不从 spec 读取
    Budget:          budget,        // 声明上限；Validate 不读实际消耗
    Tools:           registry,      // 只读查询，供 tool 节点使用
})
if err != nil {
    return err                     // 所有拒绝都发生在这里，派发之前
}
options := agent.NewRunOptions(taskTool, registry)
options.BudgetCheck = agent.BindBudgetCheck(a)     // 本轮的实际消耗判据
options.Checkpoint  = func(r agent.OrchestrationNodeResult) { /* 进度 */ }
result, err := agent.RunOrchestration(ctx, plan, options)
```

- `Compile` 是纯函数，且会先调用 `ValidateOrchestration`，因此不存在绕过校验得到 `Plan` 的路径。`ValidateOrchestration` 本身不读磁盘、不执行任何东西、也不读消耗。
- `Plan` 不可变且可哈希。每个访问器都返回深拷贝，哈希进入计划的事件，`Plan.Items()` 发布的是**编译后**的视图——有效的 `ReadOnly`、构图所用的边——而 `Plan.Nodes()` 返回的是按声明原样的 spec。权限判断一律取 `Items()`；从 `Nodes()` 读回 `read_only` 等于把声明当成结论。
- `RunOptions` 的接缝都是可选的。`RunAgent`/`RunTool` 为 nil 时拒绝需要它的节点，`BudgetCheck` 为 nil 视为无上限，`Checkpoint` 为 nil 则不记录。替换某个字段是为会话自身路径加策略，而不是引入第二套机制。

### 预算

`ValidateOptions.Budget` 是**声明式**上限：校验阶段累加各节点的 `max_tokens`，数字本身不自洽的计划会被拒绝。它不读实际消耗。真实消耗在每次派发时经 `RunOptions.BudgetCheck` 检查，装配处把它绑到本轮自己的 `taskBudgetLimit` + `runBudget.exceeded`——runner 只读判据，不做算术。被切断时计划停止，未到达的节点记为 `skipped`，轴名出现在 `OrchestrationResult.BudgetAxis` 中并随错误返回。

### 取消、重试、跳过

- **取消**会停止计划；已派发的节点发布 `cancelled`，图未到达的节点读作 `skipped`。一次运行结束时不会有节点停留在 `pending`。
- **重试**恰好一次，且只对无副作用的节点。已用掉本次尝试的节点会被拒绝，因此失败不会在此形成循环。
- **失败**是隔离的：该节点的依赖方被跳过，计划其余部分不受影响。

### Checkpoint 与进度

`Checkpoint` 在**每个节点**的终态报告归档时调用一次——发布之后，以及终态收敛时为那些未经派发便被图定论的节点再调用一次。host tool 把每次调用渲染为既有的有界 `ToolResultPreview` 事件（`orchestrate/<node-id>`），而不新造事件种类。runner 自身不持久化任何东西：transcript、checkpoint 存储与 `read_subagent_result` 都是会话既有的。

每个节点的报告同时携带时间边界与证据量，且都能在工具自身的结果里看到：

| 字段 | 含义 |
| --- | --- |
| `StartedAt` / `EndedAt` | Unix 毫秒边界，分别在节点被派发与其报告归档时设置。图未经派发便定论的节点也会拿到这两个值，因此不会有节点读作"从未存在过"。 |
| `DurationMs()` | `EndedAt - StartedAt`；节点从未被派发时为 0。 |
| `ReceiptCount` | 该节点在飞期间，**本轮 ledger** 记录了多少条 receipt。这是 ledger 的计数，不是 runner 的记账，因此"完成了却没留下证据"的节点报告 `0`。 |
| `Receipts` | 该节点发布的 receipt id，下游引用正是据此解析。 |

工具的结果是每个节点一行有界文本——`- w [agent] completed receipts=1 12ms`——进度预览则在事件自身的 `StartedAt`/`EndedAt`/`DurationMs` 字段上携带同样的数字。两个通道都不会出现节点正文。

在边界处读取 receipt gap：装配好的 controller 会通过其 audit sink 发布宿主的完成报告（`event.CompletionReportAudit`——verdict、gap 数量与 gap 种类，内容无关）。这是 `internal/agent` 之外的调用者读取 A4 断言的接缝；包内半边则直接读 `OrchestrationResult.Completion`。

### Receipt 与证据

节点的工作留下常规 receipt，子 agent 的 receipt 经与 `task` 相同的路径并入调用方 ledger。`OrchestrationResult.Completion` 是对该 ledger 调用 `completion.Build` 的结果，因此改了工作区却从未验证的计划无法读作 `done`。结论取自 receipt，而不是 runner 的记账——计划报告"所有节点 completed"，并不能说明工作已被证明。

## 如何触达该工具

`orchestrate` 与 `task`、`parallel_tasks`、`fleet`、`read_subagent_result` 并列注册，且**不在** provider 可见的工具面中。provider 可见前缀保持逐字节不变，这正是前缀缓存能保持热的原因。

它经既有的 capability 通道触达：

```
use_capability {action: "call", capability_id: "tool:orchestrate", arguments: {spec: {…}}}
```

两个值得知道的后果：

- **`workflow:orchestrate` 在该工具注册的瞬间即可被寻址。** `workflow:` 是既有的 capability 前缀，会解析到注册表工具，所以这是"顺带得到"而非新增。它同时也是本能力的表面与设计中"它不得变成的那个东西"发生碰撞的唯一位置：`orchestrate` 是委派计划，不是工作流引擎；以工作流为框架的请求，应当用这四种模式回答或直接拒绝，而不是不断扩展直到那个词变得贴切。
- **把它纳入 provider 工具面，复用的是既有 host tool 通道**（`boot.Options.ExtraTools`，或宿主自行注册的等价物）。不存在单独的配置键；opt-in 是刻意行为——此时工具 schema 与前缀都会变化，前缀稳定性守卫正是为此存在。

在 `ablation.Subagent` 下该工具**根本不注册**，因此任何派发路径——`tool:`、`workflow:`、`task:`——都无法触达它。这是结构性的，而不是调用时的门禁。

## Team 会话

Team 会话与其他会话一样运行计划，**成员**也不例外。`AllowAgentNodes` 在装配时对所有角色一律绑定为 `true`，因此 `agent` 节点在成员会话、leader 会话与单机会话中同等被接收。

这是对早前限制的一次反转，值得写明，因为旧行为比「能力受限」更糟——它等于完全不可用。Team 会话过去绑定 `AllowAgentNodes: false`，与 host tool 的类型门（只接收 `agent` 节点）叠加后接收集合为**空**：`agent` 的 spec 被校验器拒绝，`tool`／`reduce` 的 spec 被类型门拒绝，于是每一份 spec 都被拒绝。这一点对 leader 同样成立，因为 `opts.TeamRole` 只会是 `"leader"` 或 `"member"`，永远不是那道门所比较的空串。

三个事实决定了这次反转：

- 成员是**独立会话**，由自己的 `boot.Build` 装配，其根 agent 处于 subagent depth 0。它不是 leader 的嵌套子 agent，因此其计划只委派一层——与单机会话相同。
- 成员本来就能用 `task`、`parallel_tasks` 与 `fleet`，而 `fleet` 经与 `agent` 节点相同的路径扇出子 agent。那道门扣下的，只是该会话已经拥有的能力的另一种拼写。
- 递归委派由 `max_subagent_depth`（默认 2）结构性地设限，在每个会话中同等逐次派发时强制。

机制仍为需要它的宿主保留：`AllowAgentNodes` 依然是装配时绑定的构造字段，从不从 spec、工具 schema 或调用参数读取，所以计划无法为自己争取权限。宿主若绑定 `false`，会得到点明会话的校验器拒绝，且不派发任何节点。

Team 会话仍然跑不了的，只有任何会话都跑不了的那些：`tool` 与 `reduce` 节点，在**每个**会话中都被类型门拒绝。这是对等，而非 team 限制。

## 当前限制

- **`tool` 与 `reduce` 节点被 host tool 拒绝，且这是刻意保留的储备。** 编译器与 runner 支持它们（`fanout_reduce` 的 `select`/`map`/`filter`/`count`，只保留 id，从不保留正文），库 API 仍可运行它们，其测试也持续通过；但 `orchestrate` 的边界只接收 `agent` 节点，并给出具名拒绝。跨类型的 `FieldKind` 传播与更大的算子集合原属第三阶段，而该阶段已**取消**——它们保留在树中作为储备，是四种算子含义的书面记录，而非腐坏。若某个计划确实需要，那需要的是另一次阶段决策，而不是绕过。
- **`fanout_reduce` 不在该工具的 `mode` 枚举中**，因此目前只能经库 API（`Compile` + `RunOrchestration`）触达。
- **不存在 team 限制。** 成员会话与单机会话一样运行 `agent` 计划；只有类型门生效，而它处处生效（见上）。
- **`caps` 只收窄，不放宽。** `max_nodes` 上限 64、`max_parallel` 上限 32、`max_writers` 上限 3——即会话自身的上限，超出即拒绝。
- **并发取自会话。** 计划获取会话调度器的槽位（默认同时 6 个、写者 3 个），无法抬高；要求更多会在校验阶段被拒绝。
- **计划是一次调用，不是持久化 job。** runner 不自建队列；超出本轮生命周期的那部分运行会随本轮一起取消。

## 校验器拒绝项

以下每一项都发生在派发之前，被拒绝的计划不留下任何东西——没有工具被执行、没有文件被写入、没有子 agent 被启动。

| 拒绝 | 例子 |
| --- | --- |
| 未知字段 | schema 未描述的任何键 |
| 版本错误 | `"version": 2` |
| id 非法 | `""`、与 `"a"` 并列的带空格 `" a"`、超出 `[A-Za-z0-9._-]{1,64}` 的 id |
| 图非法 | 重复 id、未知 `needs`、自环、环 |
| 存在无序对 | 两个 `sequence` 节点之间没有任何路径 |
| 序列之外的写者 | `parallel` 或 `pipeline` 中的写入节点 |
| 引用非法 | `$nodes.ghost.out`、自引用、前向引用、未声明的字段 |
| `reduce` 无字段 | `reduce` 节点所归约的生产者未声明 `outputs` |
| output kind 未知 | `outputs[].kind` 不在 `scalar`、`list`、`ref` 之内。省略 kind 仍然合法 |
| output 无名 | `outputs[]` 条目的 `name` 缺失或为空。schema 要求该字段，且无名字段不可被寻址 |
| 超出自身 cap | `caps` 高于会话上限，或 `max_tokens` 超出声明预算 |
| `tool` 节点无法运行 | 未注册的工具、由 MCP 服务的工具、参数校验失败或 `Skipped` |
| 被扣下处出现 `agent` 节点 | 宿主将 `AllowAgentNodes` 绑定为 false 时的任何 `agent` 节点。今天没有任何角色这样绑定 |

## 测试

- `internal/agent/orchestrate_compile_test.go`——校验器与编译器用例。
- `internal/agent/orchestrate_run_test.go`——runner：各模式、引用、预算切断、取消、失败、重试、跳过、逐节点 checkpoint，以及"结论随 receipt 走"规则在真实 receipt 上的验证。
- `internal/agent/orchestrate_tool_test.go`——host tool 自身边界：严格解码、版本错误、被拒的节点类型，以及有界的结果。
- `internal/boot/effect_orchestrate_surface_test.go`——A1 与 A6：工具已注册且可派发，而 provider 可见面保持钉定；opt-in 后连续请求的前缀保持稳定。
- `internal/boot/effect_orchestrate_ablation_test.go`——A2：subagent ablation 在结构上移除该工具。
- `internal/boot/effect_orchestrate_receipt_test.go`——A4：一次 headless 轮次，模型经 `use_capability` 触达该工具，节点真实写入文件，而宿主的完成审计拒绝将其读作 done。
- `internal/boot/effect_orchestrate_slots_test.go`——A5：在装配好的会话调度器上跑宽并行计划，实测峰值不超过 6。
- `internal/agent/orchestrate_fleet_equivalence_test.go`——P1 的等价性：显式 id、既无 `outputs` 也无 `reduce` 节点的计划，编译结果恰好等于把同样的工作直接交给 `fleet` 所构建的 `fleetPlan`。
- `internal/agent/orchestrate_fleet_contrast_test.go`——A5 的 `fleet` 对照：一个计划与一次 `fleet` 在同一调度器上，就重叠的 `write_paths` 并发运行；以及让这种重叠得以安全的同文件互斥。
- `internal/boot/effect_orchestrate_team_test.go`——A7 的对等半边：成员会话经 `use_capability` *运行*一份写入计划，且文件落盘。
- `internal/agent/orchestrate_node_observability_test.go`——逐节点契约：报告与事件上的时间边界，以及取自本轮 ledger 的 receipt 计数。
