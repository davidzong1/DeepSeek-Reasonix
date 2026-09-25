# Team Member 缓存一次性收口：最终决策记录（合流闭环）

> 执行依据：`TEAM_MEMBER_CACHE_ONE_SHOT_3AGENT_EXECUTION_PLAN.zh-CN.md` §4「协调 Agent 的一次性合流动作」、§6「收口判定」。
> 日期：2026-09-25。
> 前置：三份分侧结果 `TEAM_MEMBER_CACHE_ONESHOT_PART_{A,B,C}_RESULT.zh-CN.md`。
> 本文是计划 §4 第 6 项要求的**最终决策记录**。

---

## 0. 决策

# `NO-GO / CONTINUE-SAMPLING`

观测链路与受控分类逻辑已完成收口；行为优化仍未验证，继续采样，禁止灰度。

不是 `GO`，因为计划 §C.5 的六项条件里**五项不满足**（§3.3）。不是 `BLOCKED`，因为本轮的取证工作全部产出，阻塞的是**行为准入**而非证据链。

---

## 1. 快照与写集核对（计划 §4 第 2 项）

### 1.1 不可变快照

| 项 | 值 |
|---|---|
| 基线 commit | `2c0f1dc61`（Team-agent，Step 4） |
| 快照 | `/tmp/oneshot-snap` = `git archive 2c0f1dc61` + 本轮 13 个改动/新增文件 |
| 全 `.go` 聚合 sha256 | `00d60c9d21ff3432364bb3e2044519980643e053b0c940be5da339bf45a341aa` |
| Go | `go1.26.6 linux/amd64` |

**为什么不是纯 `HEAD`**：本轮（1、2 项）改动了生产代码，见 §2。快照即「合流态」，本记录的全部门禁都跑在它上面。

**与 A 侧快照的差异**：A 报告 §1 发布的内容哈希是 `9e2b9623…`，其快照是**纯 `git archive HEAD`、不含工作树未提交改动**。两者都合法，但不可混淆：A 的哈希描述 `2c0f1dc61` 的字节，本记录的 `00d60c9d…` 描述**加上本轮两个开关之后**的字节。任何把两者当同一份构建的读者都会读错。

### 1.2 写集不交叉

| 侧 | 写集 | 与其它侧的重叠 |
|---|---|---|
| **A** | `internal/agent/context_headroom.go`（新）、`context_manager.go`、`context_receipt.go`、`context_status.go`、`compact*.go`、`prune.go`、`truncate.go`、`sessionstate.go`、`projection.go`（仅加 receipt 字段）；新 `internal/agent/context_headroom_test.go` | `projection.go` 与 B 共享（B 提取 `wireCall`/`wireMsg`）；A 已在文档 L-7 记录 |
| **B** | `internal/agent/cache_shape.go`、`cache_shape_test.go`、`session_context.go`、`cachereason/`、`event/session_context_diagnostics.go`、`boot/testdata/golden/prefix_shape.json`（×2）、`internal/cli/team_cache_append_granularity_test.go`（新）、`internal/cli/team_cache_unclaimed_rewrite_test.go`（新） | 同上 |
| **C** | 仅 `docs/team-mcp-port/TEAM_MEMBER_CACHE_ONESHOT_PART_C_RESULT.zh-CN.md`（探针只在快照内创建并已删除） | 无 |
| **协调（本轮 1、2 项）** | `internal/agent/{agent,agent_config,task,task_options,session_context,context_headroom}.go`、`internal/boot/boot.go`、`internal/config/{config_harness,config_ui}.go`、`internal/cachelab/plan.go`、`internal/cli/team_condition_matrix_test.go`（新） | **`internal/agent` 与 A/B 同目录**；改动见 §2.2 的边界声明 |

### 1.3 三方结果来自同一快照

A/B/C 的门禁各自跑在**它们自己的隔离快照**上（A 因 `projection.go` 混编而手工重建；C 因 A/B 在途编辑而排除其文件）。**本轮把这三种情形统一到 §1.1 的一个快照上重跑**，结果见 §4。

---

## 2. 本轮唯一的生产代码变更：给 A/B 的行为加开关

### 2.1 为什么

C 的 Part C 报告 §3.3 发现：**四个注册条件没有一个能隔离 A 或 B**。

| 注册条件 | 变更前的实际行为 |
|---|---|
| `baseline` | **已包含 A 的全部行为改动**（七态分类、headroom 判据、低收益锁存、四个计数器全部无门控） |
| `a_only` | baseline + 一个 2026-09-03 从上游合入的既有特性（`cache_aware_compaction`） |
| `b_only` | = baseline（B 的消息数组指纹无任何开关） |
| `a_plus_b` | = a_only（开关集逐字相同） |

计划 §1 写着「默认决策保持：**行为优化不放行**」，但 A 的行为改动**已在默认路径上生效**——这与 §C.5「所有变更有独立开关和可执行回滚」直接冲突。

### 2.2 变更内容

**两个新配置键，默认开**（nil = 启用，沿用 `agent.cold_resume_prune` 的先例）：

```toml
[agent]
# low_yield_latch：一个已失败于腾出空间的视图，禁止再次付 summary。
#   关掉 = 恢复「输入哈希一变就重试」的旧行为。
# message_shape_diagnosis：指纹化 provider-visible 消息数组，
#   使「追加」与「改写已读字节」可区分。关掉 = 数组完全不比较。
```

**选项命名沿用 `Disable*` 前缀**（与 `DisableWriteAccessExpand` 一致），使 `agent.Options{}` 的零值保持行为开启——仓库里有 378 处测试直接构造零值 `Options{}`，用 `Enable*` 会让它们静默丢掉诊断。

**边界声明**（计划 §2.2 要求生产变更先报告并获批）：

- 用户已明确拍板「修改为默认开」并授权动工。
- **未**把 `cache_aware_compaction` 改为默认开：它是 09-03 的既有上游特性，其默认值有文档（`TEAM_MEMBER_CACHE_BEHAVIOR_PLAN.md:132`「已实现，默认 `false`」），且 `HITRATE_ANALYSIS.md:121` 记着「零值即旧行为」。改它是**改变既有生产行为**，不在批复范围内。
- 新增的两个键与 `visible_window_tokens`、`cache_aware_compaction` 一样是 **decode-only**（不渲染进 canonical TOML）。实测确认：render 后再 reload，值仍为默认开，不会静默丢失。
- `internal/boot/boot.go` 的三处构造点各加一行；`build()` 的 function-size 用「两个键一行」的写法回到既有预算内（该文件已有 6 处同型写法）。

### 2.3 条件注册表随之更正

`internal/cachelab/plan.go` 的 `RegisteredConditions()` 现在对**每一个条件**声明它开启/关闭**每一项相关行为**：

| 条件 | `cache_aware_compaction` | `low_yield_latch` | `message_shape_diagnosis` | `context_rescue` |
|---|---|---|---|---|
| `baseline` | false | **false** | **false** | false |
| `a_only` | true | **true** | false | false |
| `b_only` | false | false | **true** | false |
| `a_plus_b` | true | true | true | false |
| `rescue_enabled` | true | true | true | true |

`baseline` 现在**真的是基线**；`b_only` 与 `a_plus_b` **第一次可执行**。

### 2.4 新增的回归守卫

`internal/cli/team_condition_matrix_test.go`（新）：把五个条件逐一走**生产成员构造器**，断言每个开关按配方落到成员自己的构建上。**无需凭证**（provider resolver 接受未使用的 key，不发请求）。

这个守卫的设计边界已写在测试里：只有 `cache_aware_compaction` 有 snapshot 可观测，且它的延迟效果需要 warm receipt 才能显现，因此该臂断言**配方**；**每个开关的行为守卫留在行为所在处**（`internal/agent/cache_aware_compaction_test.go`、`context_headroom_test.go`、`cache_shape_test.go`）。

这是本轮对 Part C §3.3 那个教训的直接回应：断言**被消费的边界**，而不是合成边界。

### 2.5 行号勘误

Part C 报告 §3.3 引用的 `plan.go:225-241` / `231-235` / `243-247` 是**改动前**的行号；改动后开关构造在 `plan.go:226-241`，五个条件在 `243-291`。该报告是本轮产物，其行号指向它自己跑的那个快照，无需重写。

---

## 3. 门禁映射（计划 §4 第 2、3 项）

### 3.1 状态映射

| 侧 | 自评 | 协调者裁定 | 依据 |
|---|---|---|---|
| **A** | `CONDITIONAL` | **`CONDITIONAL`** | A-6：M1 的两处粒度偏离未获书面接受 |
| **B** | `CONDITIONAL` | **`CONDITIONAL`** | B-5：`append_block_allowance = 128` 无跨环境证据 |
| **C** | `CONDITIONAL` | **`CONDITIONAL`** | §C.5 六项中五项不满足 |

按计划 §4 第 3 项的规则「任一关键证据为 `CONDITIONAL`：最多有限合流，不得灰度」：

> **三方全部 `CONDITIONAL` → 有限合流，不得灰度。**

### 3.2 协调者就 A-6 与 B-5 的书面裁定

计划 §4 要求协调者处置这两项。裁定如下：

**A-6（`summary_requests` 粒度偏离）——接受，附量化代价。**

A 把 `summary_requests` 放在**会话级累计**而非 receipt 上，理由是「一次决策可跑多次 summary（梯子最多 4 次），receipt 是决策记录而非调用记录」。协调者**接受**该理由，代价已量化：

- 计划 §6.2 的 `compaction_requests_per_turn` 因此只能给「会话总量 / turn 数」的粗粒度；
- 该数字**不可**被读作逐 turn 精确值（已写入 A 文档 L-10、C 文档 L-10）。

**不要求补逐 turn 计数**：C 的 `turn_cost` 提供逐 turn 视角，A 的计数器提供会话累计视角，两者回答不同问题，当前无证据显示该缺口影响任何已产出的结论。

**A-6（`rescue_planned` 不放 receipt）——接受。**

rescue 的 plan 通过 `ContextRescueRequired` 错误携带（`prepared.Recovery` 只是诊断副本）。复制进 receipt 会产生第二个真相来源。协调者**接受**。

**B-5（128 未跨环境验证）——接受为已知限制，不阻塞合流。**

C 已把 128 从「归纳值」提升为**可逐样本证伪的确定性模型** `hit == floor(prev_prompt / 128) × 128`（251/251 成立），并给出可复算计算器。协调者**接受**「单网关归纳参数，不作 Provider 常量外推」的适用范围声明，并要求任何后续报告沿用该声明。

**这三项裁定不改变任一方的 `CONDITIONAL` 状态**，只解除「未获书面接受」这一个形式阻塞。

### 3.3 计划 §C.5 的六项 GO 条件逐条状态

| # | 条件 | 状态 | 依据 |
|---|---|---|---|
| 1 | 四个正式条件均有足够有效 warm 样本，三次独立运行方向一致 | ❌ | 每臂 7 warm（注册要求 30）；**且臂间差异落在噪声内**（Part C §3.4） |
| 2 | A/B 行为路径在真实负载中确实触发并有可解释变化 | ❌ | 全部未触发（Part C §6）；触发边界离样本 3–4 倍 |
| 3 | miss tokens/request 或总 input tokens 有改善且无不可接受成本回归 | ❌ | 无差异可测 |
| 4 | 任务完成率、必要上下文保留、工具调用正确率不劣于基线 | ❌ | **不可测**（记录无 task result、无请求时长） |
| 5 | p50/p90 latency、usage 覆盖率、unknown 比例、安全边界满足阈值 | ⚠️ | 覆盖率与 unknown 达标；**latency 不可测** |
| 6 | 结果不依赖未经验证的 128 常量 | ⚠️ | 归因分区依赖它；Part C §5.3 已给不依赖它的读法 |

**六项里五项不满足或不可测 → `GO` 不可达。**

---

## 4. 不可变快照上的门禁重跑（计划 §4 第 4 项）

在 §1.1 的快照上：

| 检查 | 结果 |
|---|---|
| `go build ./...` | ✅ |
| `go test` × 11 个包（agent / boot / config / cli / control / cachelab / team / event / eventwire / cachereason / stats） | ✅ 全绿 |
| `go vet`（agent / config / boot / cachelab / cli） | ✅ |
| `gofmt -l internal/` | ✅ 空 |
| `scripts/cache-guard.sh` | ✅ **10/10** |
| `go run ./tools/repolint` RED SET vs `2c0f1dc61` | ✅ **逐字节相同**（7 条既有违规，无新增） |
| 条件矩阵守卫（`TestEveryConditionReachesTheMembersOwnAgent`） | ✅ 5/5 条件 |

### 4.1 既有红项（非本轮引入，逐条列出）

| 项 | 状态 |
|---|---|
| `internal/cli/chat_tui_team_render.go` / `chat_tui_team_reset.go` / `chat_tui_team_session.go` / `team_history_sync.go` / `team_replay.go` / `team_task_service.go` / `provider/anthropic/messages_usage.go` 的 essay/file-size | carry-forward（HEAD 上即红） |
| `TestTeamTurnInjectsInboxAtSubmit` 的 `-count>1` flake | **既有**（C 已在 Part C §5.1 认定为纯 HEAD 上的既有 flake；本轮实测 30 次 6 次失败，与本轮改动无关） |
| `desktop/` 的 `TestHostContractGeneratedFilesAreCurrent` | HEAD 上即红；本轮未跑 desktop |
| `golangci-lint` | 未执行（本机未装；CI pin 2.12.2） |

---

## 5. 未决项与后续门槛

### 5.1 已被本轮解除的阻塞

| 原阻塞 | 状态 |
|---|---|
| `baseline` 不是基线（含 A 的行为） | ✅ 已解除：`agent.low_yield_latch` 可关，`baseline` 注册为 false |
| `b_only` / `a_plus_b` 不可配置 | ✅ 已解除：`agent.message_shape_diagnosis` 可关，两臂第一次有独立配方 |
| 无配置层面的守卫 | ✅ 已解除：`TestEveryConditionReachesTheMembersOwnAgent` |

### 5.2 仍未解除的阻塞（按依赖排序）

| # | 阻塞 | 为什么它挡着 GO |
|---|---|---|
| **1** | **A 的行为路径在现实会话里不触发**：`compact_ratio` 默认 0.80，1M 窗口下触发边界 80 万 tok；本轮探针最大 23 万。 | 四个条件臂跑起来仍测同一个不触发的路径。要么把会话撑到 80 万 tok，要么在**实验条件**里降低 `compact_ratio` 并写进注册。 |
| **2** | **质量 / 延迟 / 任务完成率不可测**：成员记录无 task result、无请求时长；`Controller.RunTurn` 返回 `error` 而非答案文本。 | 计划 §C.5 条件 4、5 无法评估 → `GO` 结构性不可达。属**跨边界改动**（扩数据面），需单独排期。 |
| **3** | **128 未跨账号 / 跨网关验证** | 不阻塞合流，但任何引用归因分区闭合的结论必须带适用范围声明。 |
| **4** | **桶级样本量不足**：`lt_32k` 10/1、`32k_128k` 20/2，低于 30/3 门 | 报表已标 `insufficient_sample`。 |
| **5** | **并发未测**；**1M 桶未覆盖** | 计划 §5 P3、§6.2。 |

### 5.3 一个新增的、必须记录的事实

Part C 报告 §7.4 L-4：**`route_bucket` 不是账号作用域**。它的哈希材料是 `(kind, endpoint, UserID, proxy)`，**不含凭据**；同一账号下不同 UserID 会得到不同 bucket（实测：5 次 matrix 1 个值，3 次 session probe 3 个值，endpoint 与凭据完全相同）。

**任何后续实验都不得把 `route_bucket` 当账号维度分组**——那会把同一个账号拆成多组。计划 §C.2 第 4 项「每臂使用相同账号范围」只能靠操作纪律（同一个 `AgentUserRef`）。

---

## 6. 回滚

两个新键各自独立，且**默认开**。回滚 = 在配置里显式设为 false，或按代码回滚：

| 变更 | 配置回滚 | 代码回滚 |
|---|---|---|
| `agent.low_yield_latch` | `low_yield_latch = false` → 恢复「输入哈希一变就重试」 | `settleMaintenanceFold` 的 `!a.lowYieldLatch` 项 |
| `agent.message_shape_diagnosis` | `message_shape_diagnosis = false` → 数组完全不比较 | `captureTurnContextShape` 的 `diagnoseMessages` 入参 |
| 条件注册表 | 无需回滚（纯注册数据） | `plan.go` 的 `switches()` |
| 矩阵守卫测试 | 无需回滚 | 删 `team_condition_matrix_test.go` |

**不产生需要迁移的持久化状态**：两个键都是 `*bool` 且 decode-only，缺失即默认开；`compactionStateSchemaCurrent` 仍是 V4。

---

## 7. 给下一轮的派单建议

本轮结束后的正确动作是**继续采样**，而不是灰度。让下一次采样有效的最小改动是 §5.2 的第 1 项：

> 在实验条件下让维护路径**真的触发**——把 `compact_ratio` 作为实验变量写进 `RegisteredConditions()`，或在探针会话里把 prompt 撑过 80 万 tok。当前四个条件臂测的是一条不触发的路径，这与「A 无效」无法区分。

在此之前，**不得**以任何命中率、`provider_residual_unexplained == 0`、或「维护计数器落盘成功」为由放行行为优化。
