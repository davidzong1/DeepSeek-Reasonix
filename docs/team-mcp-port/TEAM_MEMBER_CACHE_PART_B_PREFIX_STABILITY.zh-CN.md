# Part B 交付：Team member provider-visible 前缀稳定性与缓存形状

> 状态：**P1 实现完成，离线验证全绿**（2026-09-24）
> 对应方案：`TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md` §3「Agent B」、§8「Agent B 交付物」
> 写集：`internal/agent/{cache_shape.go,projection.go,session_context.go,*_test.go}`、`internal/cachereason/**`、`internal/event/session_context_diagnostics.go`、`internal/cli/team_backend_options_inherit_test.go`、`internal/boot/{golden_baseline_test.go,testdata/golden/**}`
> **未触碰**：Agent A 的 compaction 状态机（`context_manager.go`、`compact*.go`、`context_receipt.go`、`prune.go`、`truncate.go`、`maintenance_commit.go`、`context_headroom.go`）、Agent C 的统计与观测（`internal/team/cachereport*.go`、`cachediagnosis.go`、`internal/cli/team_usage_publish.go`、`team_cache_report.go`、`internal/cachelab/**`）。

---

## 1. 这一部分回答的问题

方案 §1 的第 3、4 条与 §7 F4 指向同一个缺口：

> 每一次 projection/fold/truncate 都可能重写 provider-visible 前缀，造成缓存重新付费。
> F4：客户端前缀发生无理由改写 → 优先修复变更来源和 reason 归因。

在本次改动之前，客户端能观测到的「前缀」只有 **system + tools**（`SystemHash`/`ToolsHash`/`StablePrefixHash`）和 **session-context digest**。**消息数组本身没有任何指纹**：一个非 fold 请求如果改写了已经发出去的历史字节，本地诊断完全看不见——`StablePrefixChanged` 仍是 `false`，`PrefixChangeReasons` 是空的，于是这次必然的 miss 落进「固定前缀高 miss、无法归因」那一类。

本次新增的正是这一层：**每次请求的消息数组指纹 + 首个分歧位置 + 已发送字节被改写的数量**，并且**只在没有任何其他 reason 能解释时**才把它命名为一次「无理由改写」。

---

## 2. 字段表（M1 契约的 B 侧）

### 2.1 `RequestShape` 对应关系

方案 §4 M1 冻结的 `RequestShape` 字段与本次落点：

| M1 字段 | 落点 | 说明 |
|---|---|---|
| `member_id` / `lineage` / `route_bucket` | 已存在（Part A/C 的 `.cache_requests.jsonl`） | 本次不改口径 |
| `system_hash` | `PrefixShape.SystemHash` → `CacheDiagnostics.SystemHash` | 已有 |
| `tools_hash` | `PrefixShape.ToolsHash` → `CacheDiagnostics.ToolsHash` | 已有 |
| `session_context_hash` | `PrefixShape.SessionContextDigest` → `CacheDiagnostics.SessionContext.Digest` | 已有 |
| **`message_prefix_hash`** | **新增** `PrefixShape.Messages.Hash` → `CacheDiagnostics.MessagePrefixHash` | 本次交付 |
| **`first_divergence_offset`** | **新增** `CacheDiagnostics.FirstDivergenceOffset` | 本次交付 |
| `rewrite_reasons` | `CacheDiagnostics.PrefixChangeReasons`（取自 `internal/cachereason`） | 本次新增一个取值 `messages` |

### 2.2 新增/变更字段

| 字段 | 位置 | 含义 | 零值语义 |
|---|---|---|---|
| `PrefixShape.Messages` (`MessageShape`) | `internal/agent/cache_shape.go` | 一次请求的 provider-visible 会话数组指纹 | 空形状 = 未采集 |
| `MessageShape.Hash` | 同上 | 整个会话数组的指纹（发布值，16 位 hex） | `""` 表示没有可比对的上一请求 |
| `MessageShape.Count` | 同上 | 会话消息条数（**不含** system 消息） | `0` |
| `MessageShape.digests` | 同上（**不导出**） | 每条消息一个指纹，仅用于定位首个分歧；不出包、不落盘 | — |
| `CacheDiagnostics.MessagePrefixHash` | `internal/event/session_context_diagnostics.go` | 本请求的会话数组指纹 | `""` |
| `CacheDiagnostics.MessageCount` | 同上 | 本请求的会话消息条数 | `0` |
| `CacheDiagnostics.MessagesComparable` | 同上 | 是否有上一请求可比较 | `false` 表示下面两个偏移**未定义**，不是零 |
| `CacheDiagnostics.FirstDivergenceOffset` | 同上 | 首个与上一请求同位置不同的消息下标；append-only 时等于上一请求的消息条数 | 不可比时为 `-1` |
| `CacheDiagnostics.MessagesRewritten` | 同上 | 上一请求中有多少条消息**未被复用** | `0` = append-only；`>0` = 已发送字节被改写 |
| `cachereason.Messages` = `"messages"` | `internal/cachereason/cachereason.go` | 「消息数组被改写且无人认领」 | Kind = **Rewrite** |

**脱敏边界**：`MessageShape` 只保留 `Hash`/`Count` 与每消息的 sha256 前 8 字节；消息正文、工具参数、凭据、文件路径都不进入结构。`digests` 是未导出字段，`CompareShape` 之外没有任何读者。

### 2.3 指纹覆盖了什么

`providerVisibleMessageDigests` 复用 `providerVisibleFingerprint` 的同一份 `wireMsg` 编码（`internal/agent/projection.go`），即「真正到达 provider 的字段」：

- 覆盖：role、content、images/image-inputs、reasoning 字段、tool call id/name/args/signature、thinking blocks、Responses items、server-search 调用。
- 不覆盖：`LocalOnly` 消息（`ModelMessages` 会删掉，采集前已投影）、decision receipt、tool preview/resolution、`RawContent`、`CreatedAt`、presented files、`MCPApp` 等纯本地字段——这些正是 `Session.Rewrite` 注释里点名的「不会到达 provider 的东西」。

因此「本地改了但 provider 看不到」不会移动指纹，「provider 看得到但本地没归因」会移动指纹。这是 F4 能成立的前提。

---

## 3. 稳定前缀与动态尾部的边界

成员请求的四段结构，以及各自的稳定性契约：

| 段 | 内容 | 允许变化的时机 | 诊断字段 | 变化时的 reason |
|---|---|---|---|---|
| **稳定前缀** | system prompt（身份、角色、审批姿态、workspace 提示）+ provider-visible 工具 schema | 只在 session 初始化、配置变化、角色/代理变化、显式 prompt 迁移时 | `SystemHash`、`ToolsHash`、`StablePrefixHash` | `system`、`tools`（结构性），或 `system_prompt_refresh`／`team_role_prompt_refresh`／`managed-runtime-activation`（已声明） |
| **会话上下文（tail 锚点）** | `sessioncontext` 快照（environment / workspace / background memory / skills catalog） | 每次快照 digest 变化时**追加**一条新 revision，不原地替换 | `SessionContextDigest`、`SessionContext.Reasons` | `session_context`（结构性）+ `runtime_changed`／`memory_changed`／`skills_changed`／`snapshot_changed` |
| **会话消息数组** | 真实对话历史（user/assistant/tool） | append-only 是默认；fold/prune/truncate/rewind/guardian merge 才重写已发送区域 | **`MessagePrefixHash`、`MessageCount`、`FirstDivergenceOffset`、`MessagesRewritten`** | **`messages`（仅当无人认领）**，否则沿用认领者的 reason |
| **动态尾部** | 当前 turn 指令、mid-turn steer、任务通知、成员状态、唤醒信息、MCP 延迟工具尾巴 | 每 turn 变化 | 落在上面两个 hash 之后，不改写它们 | `tools`（延迟尾巴形状变化时，由工具面本身负责） |

三条契约：

1. **动态内容聚合在尾部之后。** 每个 turn 的新指令与 steer 通过 `appendCommittedMessages` 追加（`run-loop` 的 `applyQueuedSteers` 把一批 steer 合成一次追加），因此 `MessagesRewritten == 0`。
2. **稳定前缀不得因成员切换、后端重建、工具重注册而移动。** 工具表在 `Registry.Schemas()` 里按 name 排序输出，`CaptureShape` 在 hash 前再排序一次，延迟 MCP 尾巴由 `deferredMCPSchemas` 按 name 排序后追加（`ApplyNativeToolSearch` 按 name 去重）。第 5 节的四个臂逐条实测了这一点。
3. **一次 fold 最多一次可解释的重写。** fold 安装投影 → `noteProjectionRewrite` 入队 `compact_auto` → 下一个请求的分歧落在投影替换的区域，`MessagesRewritten > 0` 但 reason 是 `compact_auto`，并且**之后立刻恢复 append-only**。

---

## 4. 归因规则（为什么只在「无人认领」时才叫它改写）

`CompareShape` 的比较顺序是：

1. 结构性原因：`system`、`tools`、`session_context`；
2. 调用方 drain 出来的认领原因：`compact_auto`、`prune`、`truncate`、`rewind_truncate`、`rewind_restore`、`guardian_merge`、system prompt 迁移值；
3. 消息数组分歧：**只有当 `reasons` 仍然为空**时，才追加 `messages`。

三个后果，都是有意为之：

- 一次 fold 之后的冷请求只报 `compact_auto`，不会多出一条 `messages` 噪声。
- 一次 session-context 尾部 revision 只报 `session_context`（因为那次改动确实只换了尾部消息）。
- 一次**没人认领**的消息改写会得到 `PrefixChanged=true` 且 `PrefixChangeReasons=["messages"]`，于是 `internal/team` 现有的 `findRewrite`/`splitByReasonKind` 会把它归入 rewrite 侧并在 evidence 里点名 `messages`——**而不是**像过去那样因为「固定前缀没动」被塞进无法归因的一类。这正是 F4 的入口。

`MessagesRewritten` 与 `FirstDivergenceOffset` **无论有没有原因都会发布**：原因回答「谁改的」，这两个字段回答「改掉了多少、从哪一条开始」。

### 4.1 reason 枚举现状（`internal/cachereason`）

- **结构性（Structural）**：`system`、`tools`、`session_context`、`system_prompt_refresh`、`legacy_pinned_system_migration`、`team_role_prompt_refresh`、`managed-runtime-activation`
- **重写（Rewrite）**：`compact_auto`、`prune`、`truncate`、`rewind_truncate`、`rewind_restore`、`guardian_merge`、**`messages`（新增）**

`messages` 归类为 Rewrite 而非 Structural 是刻意的：结构性值在报表里被当作「已知、无需解释」，而一次无人认领的消息改写恰恰是必须解释的那一类。

---

## 5. 离线 prefix benchmark（§3 B 第 7 条、§8 交付物）

`internal/agent/member_cache_benchmark_test.go` 的真实 HTTP 边界基准，mock 按「与上一请求字节相同的 message 前缀」推导 cache split，并记录分歧位置。本次新增：

| 臂 | 变量 | 断言 |
|---|---|---|
| `switch/two-members` | 两个成员（各自 system 身份）在同一 route 上轮转 | 每个成员是**独立流**：按 system 消息 hash 分流比较；两条流各自 append-only、各自稳定前缀不变 |
| `switch/two-members-host-context` | 同上 + 每 turn 变化的 turn-context 快照 | 同上（成员切换 + 上下文 revision 叠加） |
| `rebuild/member-surface` | 每 2 turn 拆掉并重建成员后端，保留 transcript，**反向**重注册同一批工具 | 重建后的请求字节级前缀不变（证明工具面是规范化排序，不是插入序） |
| `reregister/mcp-servers` | 每 turn 对一个 MCP server 做 `RemovePrefix` + `Add`（`mcp tools/list_changed` 的真实路径） | wire 工具块**逐字节**不变（`toolsHash`，非仅本地 hash） |
| `fold/auto`（既有） | 小窗口强制 fold | 4 次 fold → 恰好 4 次改写，每次紧随 fold 之后，稳定前缀始终不变，fold 后立刻恢复 100% 复用 |
| 既有：short/long 上下文、wider surface、changing host-context、large tool output | 上下文尺寸、工具面、动态尾部、工具输出量 | `appended` 不得为负（非 fold 请求）、稳定前缀不变 |

新增断言（对每个臂生效）：

- 同一流内 `stableHash`（system + tools 的 wire 字节）**恒定**；
- 同一流内 `toolsHash`（序列化后的工具块）**恒定**——这是「本地 hash 与 wire 字节一致」的逐字节校验；
- 每个成员各自成流（`switch` 臂必须观测到 2 条流），否则共享基线会让臂「碰巧通过」；
- 声明了重建/重注册/fold 的臂必须真的发生过该事件，否则它是空断言。

`TestDeferredMCPTailIsTheHashedSurface` 补齐 `TEAM_MEMBER_CACHE_ROOTCAUSE_EXPERIMENTS.md` §9 明确记为未覆盖的形状：原生 tool-search 下的**延迟 MCP 尾巴**。断言：尾巴在 wire 上、被标记 `Deferred`、按 name 有序；重注册一个 server 后数组与 `ToolsHash` 都不动；提升一个尾巴工具为可见后 `ToolsHash` 必须跟着变（证明尾巴在被 hash 的surface 之内，而不是哈希之外的影子）。

---

## 6. 配置继承（§3 B 第 6 条）

`agent.visible_window_tokens` 与 `agent.cache_aware_compaction` 曾经被 `agent.New()` 静默丢弃（`Options` → `agentConfig` 漏拷），两个配置键在任何构建里都是死键。既有修复已补齐拷贝，但缺一条覆盖 **Team member** 链路与**子 agent** 链路的守卫。本次补两条：

1. `internal/agent/member_backend_inheritance_test.go`：成员 spawn 的 task 子 agent 继承这两个开关，并且**读得到消费结果**——子 agent 的 `recentTailBudget()` 等于配置的 cap；零值保持文档默认（窗口 16%），不会「凭空继承」。
2. `internal/cli/team_backend_options_inherit_test.go`：ambient config snapshot → `memberBackendOptions` → `boot.Build` → **成员自己的 agent**。观测点是 `Controller.Executor().ContextMaintenanceSnapshot().HeadroomGoal`，即 `recentTailBudget()` 的消费形式：cap 生效时为 80000，未生效时为窗口 16%（1M 窗口 → 160000）。只检查 options 结构体的测试会一路放过原缺陷，所以这条断言读的是活 agent。

---

## 7. 跨边界修改（按方案 §5「先提交接口变更说明」处理）

本次触及他人写集的文件只有**一个字段增加**，且已在此登记：

| 文件 | 变更 | 归属 | 说明 |
|---|---|---|---|
| `internal/event/session_context_diagnostics.go` | 新增 5 个诊断字段 | 共享类型（A/B 都构造 `CacheDiagnostics`） | 纯新增，无删除、无重命名；A 的 receipt/状态机不读这些字段；C 的报表可选用 |

**明确移交（不代为修改）**：

| 交给 | 位置 | 需要做的事 |
|---|---|---|
| Agent C | `internal/cli/team_usage_publish.go` 的 `applyCacheDiagnostics` | 4 行映射，把 `MessagePrefixHash`/`MessageCount`/`MessagesRewritten`/`FirstDivergenceOffset` 放进逐请求记录；`event.CacheDiagnostics` 已带这些值，无需再改 agent |
| Agent C | `internal/team/cacherequest.go` | 记录结构加 4 个带 `omitempty` 的字段（当前 C 正在同文件加 session rotation 字段，故本 Agent 未并行编辑） |
| Agent C | 报表口径 | `messages_rewritten > 0 且无 rewrite reason` 应单独成为一类样本；命中率分母不变 |
| 协调 Agent | `internal/eventwire/wire.go` | 若要让 desktop/ACP 看到这 4 个字段，需在 `CacheDiagnostics` wire 结构与 `ToWireCacheDiagnostics` 各加 4 项。本次**未改** wire：成员观测走进程内事件，先把生产证据链打通再决定前端是否暴露 |
| Agent A | 无 | B 未改任何 compaction 状态机、触发条件、retry 规则 |

---

## 8. 缓存影响与回滚

**对 provider-visible 字节的影响：零。**

- 新增的都是诊断字段：不参与 `provider.Request` 构造、不改变消息顺序、不改变 system/tools 内容、不改变 fold 边界。
- `providerVisibleFingerprint` 被重构为「逐消息编码 + 整数组编码共用同一个 `wireMsg`」，**输出逐字节不变**（投影 sidecar 里存的就是这个值，改了会让所有已存投影失效）。
- `context_cache` 哈希面的四处契约值不变：boot 的 cache-contract golden（`internal/boot/testdata/golden/prefix_shape.json` 与 `windows-powershell/` 变体）本次只有 `Messages` 一个**空**增量对象，`SystemHash`/`ToolsHash`/`PrefixHash`/`ToolSchemaTokens` 四个值逐字节不变。

**对本地行为的影响：一处，可回滚。**

- `PrefixChanged` 现在还会在一次**无人认领的消息改写**时为 `true`，`PrefixChangeReasons` 为 `["messages"]`。这是本次唯一的行为变化，目的是让 F4 可观测。使用方（`status_footer`、`run_metrics`、team 报表）都按 reason 渲染，遇到新值只会多显示一行归因文字。
- 回滚：删除 `CompareShape` 中追加 `cachereason.Messages` 的那 4 行即可回到旧语义；`MessagesRewritten`/`FirstDivergenceOffset` 作为纯观测字段可以保留（无人读时无副作用）。

**成本**：每次 provider 请求多一次逐消息 JSON 编码 + sha256。与小窗口/大窗口无关的量级：1M token 上下文约 4MB 正文 → 数十毫秒/请求，相对于秒级的模型往返可忽略；该成本与投影校验已有的整数组指纹同阶。

---

## 9. 验证记录

| 命令 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `gofmt -l`（本次改动的全部文件） | 干净 |
| `go test ./internal/agent/` | 全绿（58s） |
| `go test ./internal/cli/` | 全绿（87s；一次运行中出现 `TestTeamTurnInjectsInboxAtSubmit` 失败，单独运行与二次全量运行均通过，判定为与 C 并行编辑中的测试顺序敏感，非本次改动） |
| `go test ./internal/boot/` | 全绿（含 golden、member surface、`TestMemberSurfaceIsStableAcrossBoots`） |
| `go test ./internal/cachereason/ ./internal/event/ ./internal/eventwire/ ./internal/team/` | 全绿 |
| `go test ./internal/agent/ -run 'TestMessageShape|TestMemberCachePrefixBenchmark|TestDeferredMCPTailIsTheHashedSurface'` | 全绿，臂输出见 §5 |
| `REASONIX_UPDATE_GOLDEN=1 go test ./internal/boot/ -run TestGoldenBaselineNoExtensions` | 只写入 `prefix_shape.json`（两平台变体），diff 为单一空 `Messages` 增量 |

未执行：`golangci-lint`（本机未安装）；真实 Provider 对照（属 §5 P3，由 C 负责）。

---

## 10. 本次未做（边界）

- 未改任何 provider-visible 请求字节、cache 策略、压缩/裁剪/折叠策略、成员隔离或统计分母。
- 未新增持久化遥测：诊断字段只在进程内事件上；落盘与报表由 C 接。
- 未改 `internal/eventwire`（见 §7 移交表）。
- 未改 Agent A 的 compaction 状态机或其任何文件。
- 未提交、未推送、未开 PR。
