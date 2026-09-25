# Part B RESULT：Provider-visible 前缀与 128 参数验证

> 状态：**`CONDITIONAL`**。日期：2026-09-25。
> 快照：`2c0f1dc61da07a7919c71ba3adbb90bc4522fe2a`（"Step 4"）+ 工作树。
> **Go 源码与该 commit 逐字节一致**（`git diff --stat -- '*.go'` 为空）；工作树只多出本 Agent 新增的
> **2 个测试文件**（见 §1.2），以及另两个 Agent 的文档/测试文件（A：`..._ONESHOT_PART_A_RESULT.zh-CN.md`）。
> 执行方案：`TEAM_MEMBER_CACHE_ONE_SHOT_3AGENT_EXECUTION_PLAN.zh-CN.md` Part B；
> 配套：`TEAM_MEMBER_CACHE_ABC_JOINT_CONCLUSION.zh-CN.md`、`TEAM_MEMBER_CACHE_PART_B_PREFIX_STABILITY.zh-CN.md`。

---

## 0. 结论

| # | 计划 §B.2 项 | 结果 | 章节 |
|---|---|---|---|
| B-1 | 复跑消息数组指纹 / 首个分歧位置 / 改写数 / rewrite reason | **PASS** | §2 |
| B-2 | 复跑四类离线 prefix benchmark（成员切换 / 后端重建 / MCP 重注册 / fold） | **PASS** | §3 |
| B-3 | 配置继承在**活 Agent** 上生效（`visible_window_tokens`、`cache_aware_compaction`） | **PASS** | §4 |
| B-4 | `messages` reason 的**生产可达形状**（不依赖合成输入） | **PASS（本轮新增端到端证据）** | §5 |
| B-5 | `append_block_allowance` 的真实块粒度分布 | **未验证（无法采集）** | §6 |
| B-6 | 无跨环境证据时不得外推 | **PASS（已按 §B.2 第 6 项标为未验证）** | §6.3 |
| B-7 | 4 个新增字段的 eventwire 影响范围 | **PASS（范围已界定，补丁建议见 §7）** | §7 |

**总体：`CONDITIONAL`。** 唯一阻塞项是 **B-5**：`append_block_allowance = 128` 是
**单网关归纳值**，本环境**既无凭证也无历史真实样本**，因此按 §B.2 第 6 项**标记为“未验证”**，
不用离线或单网关结果外推 Provider 常量。其余六项都有本轮复跑或新增的可复核证据。

**不因此得出的结论**（与 §B.3 禁止事项逐条对应）：

- 本地 hash 相同 ≠ Provider cache key 相同；
- 离线 benchmark 全绿 ≠ 真实 Provider 上 fold 只产生一次改写；
- `messages` 端到端测试跑通 ≠ 真实负载出现过无人认领改写（真实数据里 `messages_rewritten_unclaimed = 0`，
  即**路径未触发**，不是**路径无缺陷**）；
- 128 未跨账号、网关、桶验证 → 归因分区的“闭合”仍以该参数为前提。

---

## 1. 快照与门禁

### 1.1 原始命令与结果

```text
$ go version
go version go1.26.6 linux/amd64

$ git rev-parse HEAD
2c0f1dc61da07a7919c71ba3adbb90bc4522fe2a

$ git diff --stat -- '*.go'          # 空：Go 源码与 HEAD 一致
```

| 检查 | 命令 | 结果 |
|---|---|---|
| 构建 | `go build ./...` | 通过（无输出） |
| vet | `go vet ./internal/agent/ ./internal/cli/ ./internal/team/ ./internal/cachereason/ ./internal/event/` | 通过 |
| gofmt | `gofmt -l` 全部改动目录 | 0 未格式化 |
| 定向套件 | `go test ./internal/agent/ ./internal/cachereason/ ./internal/event/ ./internal/eventwire/ ./internal/team/ -count=1` | `agent 50.3s`、`cachereason`、`event`、`eventwire`、`team 6.1s` 全 ok |
| cli 全量 | `go test ./internal/cli/ -count=1` | `ok reasonix/internal/cli 80.250s` |
| boot golden | `go test ./internal/boot/ -run TestGoldenBaselineNoExtensions -count=1` | `ok reasonix/internal/boot 0.072s` |
| repolint | `go run ./tools/repolint` | **本轮新增文件 0 违规**；RED SET 仍是既有 carry-forward（`chat_tui_team_*` / `team_history_sync` / `team_replay` / `team_task_service` / `messages_usage`），与 HEAD 相同 |

**证据上限**：这是**当前工作树**证据，不是协调 Agent 冻结的不可变构建产物；无法提供
`golangci-lint`（本机未安装，CI pin 2.12.2）与 `desktop/` 模块（独立 Go module）结论。

### 1.2 本轮的代码改动 = 仅两个新增测试文件

按 §2.2“生产代码变更必须先报告阻塞原因并由协调 Agent 单独批准”，本轮**未改动任何生产文件**：

| 文件 | 性质 | 作用 |
|---|---|---|
| `internal/cli/team_cache_unclaimed_rewrite_test.go` | 新增（仅测试） | §5 的端到端可达性证明 |
| `internal/cli/team_cache_append_granularity_test.go` | 新增（仅测试） | §6 的 128 分布计算器（含跳过臂） |

两者都是 `package cli` 内自包含测试，不引用、不修改 C 的 `team_usage_*` / `team_cache_*` 文件。

---

## 2. B-1：请求形状诊断（PASS）

```text
$ go test ./internal/agent/ -run 'TestMessageShape|TestCompareShape|TestCaptureShape|TestPrefixShape|TestCacheShape' -count=1 -v
--- PASS: TestCaptureShapeNormalizesToolSchemaOrder (0.00s)
--- PASS: TestMessageShapeTellsAnAppendFromARewrite (0.00s)
--- PASS: TestMessageShapeNamesAnUnexplainedRewrite (0.00s)
--- PASS: TestMessageShapeDefersToAReportedReason (0.00s)
    --- PASS: TestMessageShapeDefersToAReportedReason/a_claimed_fold (0.00s)
    --- PASS: TestMessageShapeDefersToAReportedReason/a_system_prompt_refresh (0.00s)
--- PASS: TestMessageShapeIgnoresLocalOnlyFieldsAndTheSystemPrompt (0.00s)
--- PASS: TestMessageShapeReportsNothingWithoutAPreviousRequest (0.00s)
--- PASS: TestMessageShapeCountsATruncationAsARewrite (0.00s)
--- PASS: TestCompareShapeIgnoresBareLogRewriteVersionDrift (0.00s)
```

| 字段 | 生产写入点 | 消费者 | 语义边界（已核验） |
|---|---|---|---|
| `MessagePrefixHash` / `MessageCount` | `CompareShape` ← `CaptureMessageShape`（系统消息按角色剔除） | `event.CacheDiagnostics` → cli `applyCacheDiagnostics` → `team.MemberCacheRequest` | 系统提示与工具 schema **不在**数组指纹内（各有自己的 hash），否则一次变化报两次 |
| `MessagesComparable` | 同上（无上一请求时为 `false`） | 同上 | `false` 表示两个偏移**未定义**，不是 0 |
| `FirstDivergenceOffset` | `messageDivergence` | 同上 | append-only 时等于上一请求的消息条数；不可比时 `-1` |
| `MessagesRewritten` | `messageDivergence` | 同上 → `cachemisscause` 的 `messages_rewritten_unclaimed` 分支 | `>0` = provider 已读字节被改写；数组变短时按“尾部全部未复用”计 |
| `cachereason.Messages` | 仅 `CompareShape`，且**仅当 `len(reasons)==0`** | `prefixMoveCause` | 全词表唯一“不指向任何操作”的 rewrite 值 |

**cache 契约未变**：HEAD 对 `internal/boot/testdata/golden/prefix_shape.json` 的唯一改动是新增一个**空**对象：

```diff
-  "SessionContextDigest": ""
+  "SessionContextDigest": "",
+  "Messages": { "Hash": "", "Count": 0 }
```

四个既有契约 hash（`SystemHash`/`ToolsHash`/`PrefixHash`/`ToolSchemaTokens`）**逐字节不变**，
说明 `providerVisibleFingerprint` 的重构没有移动 provider 看到的字节。

---

## 3. B-2：四类离线 prefix benchmark（PASS）

`go test ./internal/agent/ -run 'TestMemberCachePrefixBenchmark|TestDeferredMCPTailIsTheHashedSurface' -count=1 -v` → 10 个臂全 PASS，以下为原始数字。

**成员切换（两条独立流，按 system 身份分流）**

```text
switch/two-members-host-context: runs=12 errors=0 folds=0 rebuilds=0 reregistrations=0
  stream 4526832300823abd 与 58fd607695ff7409 交替出现，各 11 条
  每条 msgReuse=100.0%，appended=0
switch/two-members-host-context: 0/22 requests diverged inside the previous prefix (0 followed a fold, 2 streams, triggers=[])
```

**后端重建（每 2 turn 拆建 + 反向重注册）**

```text
rebuild/member-surface: runs=8 errors=0 folds=0 rebuilds=3 reregistrations=0
  15 条样本 stable hash 恒为 31c32caf32723f3e，toolsHash 逐字节不变，appended=0
rebuild/member-surface: 0/15 requests diverged inside the previous prefix (0 followed a fold, 1 streams, triggers=[])
```

**MCP 重注册（`RemovePrefix` + `Add`，即 `tools/list_changed` 路径）**

```text
reregister/mcp-servers: runs=8 errors=0 folds=0 reregistrations=7
  15 条样本 stable hash 恒为 ee127679cf932b97，tools=4549 chars 恒定
reregister/mcp-servers: 0/15 requests diverged inside the previous prefix (0 followed a fold, 1 streams, triggers=[])
```

**fold（窗口 6000 强制自动压缩）**

```text
fold/auto: runs=20 errors=0 folds=4 triggers=[pressure pressure pressure pressure]
  12 c779fecb10192d09 18233 76.9% 14015 -13905  post-fold   msgReuse=1.4%
  20 c779fecb10192d09 18261 76.9% 14042 -13749  post-fold   msgReuse=2.7%
  36 c779fecb10192d09 18276 76.9% 14057 -13764  post-fold   msgReuse=2.7%
  （三条 post-fold 之后立刻恢复 appended=0 / msgReuse=100.0%）
fold/auto: 4/39 requests diverged inside the previous prefix (4 followed a fold, 1 streams, triggers=[pressure×4])
```

**这四类臂断言什么**（不是“命中率好看”，而是不变量）：非 fold 请求 `appended >= 0` 恒成立；
fold 之后**至多一次**改写（`rewritten(4) <= folds(4)`）；同一流内 stable prefix 与工具块字节不变；
每个臂声明的事件**必须真的发生**（`rebuilds=3`、`reregistrations=7`、`folds=4`，否则测试自身变红）。

**原生 tool-search 延迟尾巴**：`TestDeferredMCPTailIsTheHashedSurface` PASS —— 尾巴（deferred MCP 工具）
**在**被 hash 的数组内，且反向注册顺序不改变 wire 数组字节。

---

## 4. B-3：配置继承在活 Agent 上（PASS）

```text
$ go test ./internal/cli/ -run TestMemberBackendInheritsTheCacheShapingKnobs -count=1 -v
--- PASS: TestMemberBackendInheritsTheCacheShapingKnobs (0.12s)

$ go test ./internal/agent/ -run 'TestSubagentInheritsTheCacheShapingKnobs' -count=1 -v
--- PASS: TestSubagentInheritsTheCacheShapingKnobs (0.00s)
```

核验的不是 options 结构体，而是**消费结果**：成员后端走
`config 快照 → memberBackendOptions → boot.Build → 成员自己的 Agent`，
断言 `ctrl.Executor().ContextMaintenanceSnapshot().HeadroomGoal == 80000`
（即配置的 `visible_window_tokens`，无该 cap 时会是窗口 16% = 160000）。
成员 spawn 的 task 子 agent 同样继承两个开关，且**零值保持文档默认**（16% 窗口、不启用 cache-aware）。

---

## 5. B-4：`messages` reason 的生产可达性（PASS，含新增端到端证据）

### 5.1 生产写点审计（为什么该值可达、以及谁在无认领地改写）

| 路径 | 位置 | 是否入队 reason |
|---|---|---|
| fold / summary 投影安装 | `internal/agent/maintenance_commit.go:122` → `noteProjectionRewrite` | ✅ `compact_auto` |
| compact 投影安装 | `internal/agent/compact_commit.go:82` | ✅ `compact_auto` |
| prune / truncate | `cache_shape.go:113-115`（`projectionRewriteReason`） | ✅ `prune` / `truncate` |
| rewind 截断 / 恢复 | `internal/control/rewind.go:59,79`（`Session.Rewrite`） | ✅ `rewind_truncate` / `rewind_restore` |
| guardian merge | `internal/guardian/guardian.go:358`（`Session.Rewrite`） | ✅ `guardian_merge` |
| 系统提示迁移 | `control/pinned_context.go`、`control/session_write_authority.go` | ✅ `system_prompt_refresh` |
| **前端采纳持久历史** | `control/history_sync.go:138`（`ReloadHistoryIfChanged`）→ `Replace` | ❌ **无** |
| **会话事件恢复** | `control/session_events.go:249`（`restoreExecutorFromSessionEvents`） | ❌ **无** |
| **模型上下文替换** | `control/session_events.go:306`（`replaceSessionModelContext(ctx, msgs, reason)`） | ❌ **无**（`reason` 只进 durable event，未进 `NoteContentRewrite`） |
| **取消/中断恢复** | `control/termination.go:264,327` | ❌ **无** |
| **legacy 取消恢复** | `control/controller.go:4030` | ❌ **无** |
| **guardian 复核回滚** | `guardian/guardian.go:305,322`（`rollbackReview`） | ❌ **无** |
| **fork 续跑恢复** | `agent/fork.go:214`（`armForkContinuation`） | ❌ **无** |
| **planner 回滚** | `agent/coordinator_rollback.go:14,30`（`rollbackPlannerTurn`） | ❌ **无** |

结论：`cachereason.Messages` **不是**只在合成输入里存在的值 —— 它有 8 个生产改写点会触发，
其中 `replaceSessionModelContext` 尤其值得注意：**它自带一个 `reason` 形参、却从不入队**，
所以一个“有名字的操作”仍会落进 `messages_rewritten_unclaimed`。

### 5.2 新增端到端证明（producer → publisher → consumer，一次跑通）

`internal/cli/team_cache_unclaimed_rewrite_test.go`：真实 member agent（脚本化 provider）
→ 真实 observation sink → 真实 `memberUsagePublisher`（自己的 goroutine + 队列）
→ owner store 的 `.cache_requests.jsonl` → 真实消费者 `team.CacheMissCauseOf`。

```text
$ go test ./internal/cli/ -run TestUnclaimedArrayRewriteReachesItsOwnCauseClass -count=1 -v
--- PASS: TestUnclaimedArrayRewriteReachesItsOwnCauseClass (0.04s)
```

断言（全部为**生产形状**，无一为手搓输入）：

- 第二条记录 `PrefixChangeReasons == ["messages"]`、`MessagesComparable == true`、
  `MessagesRewritten == 1`、`FirstDivergenceOffset == 0`；
- `StablePrefixChanged == false` —— 这正是 B 存在的理由：system/tools 都没动，
  改动前的诊断对“已读字节被改写”**完全看不见**；
- `team.CacheMissCauseOf(第二条) == messages_rewritten_unclaimed`，而
  `team.CacheMissCauseOf(第一条) == cold_prefix`（对照：分类器不会无条件返回新类）。

**反向验证（防止“断言合成边界”）**：把 `CompareShape` 里追加 `cachereason.Messages` 的那 4 行短路后，
该测试**立即变红**（`reasons = [], want exactly [messages]`），随后已还原（`git diff` 为空、重跑通过）。
即：这条断言**依赖生产者的真实输出**，而不是测试自己的构造。

### 5.3 与消费者侧的接口

`internal/team/cachereason`→`prefixMoveCause` 的 split 在 C 侧已有测试
（`go test ./internal/team/ -run TestCacheMissCause -count=1` → 13 个全 PASS，含
`TestCacheMissCauseNamesAnUnclaimedMessageRewrite`）。两侧的**唯一接口**是
`cachereason.Messages` 这个共享常量（生产者写它、消费者按它 split），本轮用一次真实运行把它钉住。

**仍未验证**：真实负载上出现过该类（`messages_rewritten_unclaimed = 0`）。这属于“路径未触发”。

---

## 6. B-5 / B-6：`append_block_allowance = 128`（未验证）

### 6.1 本轮尝试采集真实样本的原始记录

```text
$ env | grep -E 'REASONIX_LIVE_CACHE|ANTHROPIC_(BASE_URL|AUTH_TOKEN)|DEEPSEEK_API_KEY'
REASONIX_LIVE_CACHE_BASE_URL = unset
REASONIX_LIVE_CACHE_API_KEY  = unset
ANTHROPIC_BASE_URL           = unset
ANTHROPIC_AUTH_TOKEN         = unset
DEEPSEEK_API_KEY             = unset

$ find /home/zwc -name ".cache_requests.jsonl" -size +0 -printf "%s %p\n" | sort -rn | head
（无输出）
```

两个 `-tags live` 基线**未执行**：无凭证，且执行真实付费会话需协调 Agent 授权。
另外，`TEAM_MEMBER_CACHE_ABC_JOINT_CONCLUSION.zh-CN.md` §4.1 那份 30 条 / 3 成员数据集
**不是可复算的产物**：`live_team_cache_baseline_test.go:59` 与 `:341` 都把 owner store 建在
`t.TempDir()` 下，运行结束即随临时目录消失。因此**当前无法给出**计划 §B.2 第 5 项要求的
「样本数 / 均值 / p50 / p90 / 最大值 / 超 128 数量 / unknown 数量」。

### 6.2 跨环境覆盖表（计划 §B.4 要求）

| 维度 | 要求 | 本轮覆盖 | 结论 |
|---|---|---|---|
| 账号 | ≥ 2 | 0（无凭证） | ❌ 未覆盖 |
| 网关 / route | ≥ 2 | 0 | ❌ 未覆盖 |
| 上下文桶 | 多桶 | 0（无样本） | ❌ 未覆盖 |
| 样本量 | 计划 §C 建议每臂 ≥ 30 warm | 0 | ❌ 不足 |
| 离线对照 | 允许，但不可外推 | 10 臂 benchmark（mock 派生） | ⚠️ 只能作为形状证据，**不可**用于 128 |

**唯一已有的独立支持（沿用，不再加强）**：合流结论 §4.3 —— 27 条 append-only 样本的 miss 均值 85 tok，
**远低于** 128；若允许量偏大，这些样本本可掩盖真实 residual，实际没有。这**支持**该值在本数据集内安全，
**不证明**它是 Provider 常量。

### 6.3 已按 §B.2 第 6 项处理

**`append_block_allowance = 128` 标记为“未验证”**：不得用单网关、离线夹具或本数据集外推为 Provider 常量。
`provider_residual_unexplained == 0` 因此**只对当前样本、当前规则与该假设同时成立**。

### 6.4 交付：可复算的计算器（让下一次有样本时只需一条命令）

`internal/cli/team_cache_append_granularity_test.go`（纯测试，无生产改动）：

- `appendMissGranularity(records, allowance)`：对**append-only warm** 请求计算
  `excess = miss − 追加内容`（即 `ContextPromptTokens` 增量）的分位数、均值、最大值，
  并单独计数 `over_allowance` 与**读不出来的样本**（无前驱 / 无诊断 / prompt 缩短 / 无 split）；
- 夹具测试用手算值钉住算法（`Samples=3`、`Max=272`、`OverAllowance=2`、`P50=272`、`Mean=208`）：
  `--- PASS: TestAppendMissGranularityIsComputedFromTheSamples`；
- 真实样本臂在当前环境**跳过**（`--- SKIP: TestAppendMissGranularityFromARealStore`），
  跳过理由以 `t.Skip` 文本记录在案，不伪装成通过。有样本时：

```bash
REASONIX_CACHE_SAMPLE_LOG="$HOME/.reasonix/team/<team>/<member>/.cache_requests.jsonl" \
  go test ./internal/cli/ -run TestAppendMissGranularityFromARealStore -v
```

---

## 7. B-7：4 个新增字段的 eventwire 范围（PASS，含补丁建议）

**现状（已核对 `internal/eventwire/wire.go` 与 desktop 生成的契约）**：

| 内容 | 是否跨进程可见 | 依据 |
|---|---|---|
| `prefixChangeReasons`（因此**包含** `"messages"` 值） | ✅ 可见 | `eventwire.CacheDiagnostics.PrefixChangeReasons` + `ToWireCacheDiagnostics` 逐项映射 |
| `MessagePrefixHash` / `MessageCount` / `MessagesComparable` / `FirstDivergenceOffset` / `MessagesRewritten` | ❌ **不可见** | `wire.go` 的 `CacheDiagnostics` 里没有这些字段；`desktop/frontend/src/generated/desktopContract.generated.ts` 中 grep `messagePrefixHash`/`firstDivergenceOffset` 均为 0 命中 |

**后果与范围**：进程内（成员观测 → `memberUsagePublisher` → owner log → 报表）链路完整，
C 的生产证据链因此成立；**跨进程前端（desktop / serve / ACP / transcript / trajectory）
只能看到“发生了无人认领改写”，看不到“改了多少、从哪条开始”**。
按 §B.3，这**不得**被描述成“前端已可观测”。

**补丁建议（需协调 Agent 批准，本轮未做）**：在 `eventwire.CacheDiagnostics` 加 4 个字段
（`messagePrefixHash`、`messageCount`、`messagesComparable`、`firstDivergenceOffset`、`messagesRewritten`）
并在 `ToWireCacheDiagnostics` 里映射，然后重新生成 desktop 契约
（`desktop/frontend/src/generated/*`）。这是一次跨 module 的 wire 变更，且 `desktop/` 的
host-contract 门禁在 HEAD 上**已经红**，属于应单独排期的动作，不适合塞进本次收口。
**风险记录**：若最终决定不补，请在合流结论中保留“跨进程只能看到 reason 值”的适用范围声明。

---

## 8. 失败 / 跳过 / unknown 样本清单

| 项 | 状态 | 原因 |
|---|---|---|
| `TestAppendMissGranularityFromARealStore` | **SKIP**（预期） | 未设 `REASONIX_CACHE_SAMPLE_LOG`；无真实样本可读 |
| `TestLiveTeamMemberCacheBaseline` / `...BaselineTeam`（`-tags live`） | **未执行** | 无凭证；付费真实会话需授权 |
| 128 的真实分布表 | **缺失** | §6.1 |
| 跨账号 / 跨网关 / 并发 / 1M 桶 | **未覆盖** | 同上 |
| 真实负载上的 fold 改写与 `messages_rewritten_unclaimed` | **未触发**（非“无缺陷”） | 合流数据集 `rewrite=0`、`messages_rewritten_unclaimed=0` |
| `golangci-lint` / `desktop/` 模块 | **未执行** | 本机未安装 / 独立 module 门禁在 HEAD 即红 |
| 本轮测试**无失败样本** | — | 所有已运行的定向与全量套件通过；cli 既有 inbox flake 不在本轮门禁内（见合流结论 §5.1） |

---

## 9. 回滚

| 变更 | 回滚步骤 | 依赖 / 影响 |
|---|---|---|
| **B：`messages` reason**（`CompareShape` 追加 4 行） | 删除 `cache_shape.go` 中 `if rewritten > 0 && len(reasons) == 0 { … }` 块 | 消费者侧 `messages_rewritten_unclaimed` 类随即**不可达**（该类只由该值触发）；字节路径不受影响 |
| **B：4 个观测字段** | 纯增量，无读者时无副作用；如需彻底移除，删 `event.CacheDiagnostics` 的 5 个字段与 cli 映射 | 需与 C 的 `applyCacheDiagnostics` 同步，否则编译失败 |
| **B：消息数组指纹** | `CompareShape` 不再填 `Message*` 字段即可；`providerVisibleFingerprint` 的输出不受影响（golden 已证明逐字节不变） | `Messages` 空增量可留在 golden |
| **本轮新增 2 个测试文件** | `rm internal/cli/team_cache_unclaimed_rewrite_test.go internal/cli/team_cache_append_granularity_test.go` | 无生产依赖 |
| 计划新增的 eventwire 补丁（§7，未做） | 未落地，无需回滚 | 若未来落地，回滚 = 删字段 + 重生成 desktop 契约 |

**没有一条需要迁移或清理持久化状态。**

---

## 10. 状态与放行门槛

**B 侧：`CONDITIONAL`。**

- 观测链路（B-1）、离线形状不变量（B-2）、配置继承（B-3）、`messages` 可达性（B-4）、
  eventwire 范围（B-7）**均已收口并有本轮可复核证据**；
- **未收口**：`append_block_allowance = 128` 的真实分布与跨环境覆盖（B-5）。

**从 `CONDITIONAL` 升到 `PASS` 需要（任一）**：

1. 在 ≥2 账号、≥2 网关或 route、多个上下文桶下采集真实 append-only 样本，用 §6.4 的计算器报告
   样本数 / 均值 / p50 / p90 / 最大值 / 超 128 数量 / unknown 数量；或
2. 由协调 Agent **书面接受**“128 = 单网关归纳参数，仅由 27 条样本（均值 85 tok）支持，
   不作为 Provider 常量外推”，并据此在合流结论中保留相应的适用范围声明。

**在任何情况下都不得**（计划 §1 与本轮 §0 一致）：以本地测试全绿、`provider_residual_unexplained == 0`、
单次会话命中率较高、或“128 未产生 residual”为由，声称行为优化已生效或启动灰度。
