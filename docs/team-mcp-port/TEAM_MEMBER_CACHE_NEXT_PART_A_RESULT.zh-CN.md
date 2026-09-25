# Part A 下一轮结果：维护路径触发与 A 因子验证

> 执行依据：`TEAM_MEMBER_CACHE_NEXT_ROUND_3AGENT_PLAN.zh-CN.md` §4「Part A」、§3「启动门禁与共享实验契约」、§2.3「统一产物格式」。
> 日期：2026-09-25。分支 `Team-agent`，快照 `f53c4bf91`。
> **状态：`BLOCKED`** —— 依据是计划 §3.2 自己写的判定：「没有这项书面裁定时标记 `BLOCKED`，不运行可能扩大真实行为暴露的 live 实验」。
> 本报告**区分**：A 侧静态/功能核验**已完成**（§3–§9），live 触发与每臂统计**未执行且不可执行**（§2、§10）。
>
> **⚠️ 本文 §1–§14 是裁定前的记录，保持原样。** 协调裁定
> `TEAM_MEMBER_CACHE_NEXT_ROUND_COORDINATION_DECISIONS.zh-CN.md` 已于同日发布，
> 四项裁定全部落地；A 侧在该裁定下的执行与对 C 交付的独立交叉复核见 **§15**。
> 当前状态以 §15 为准：**`CONDITIONAL`**。

---

## 0. 结论

| # | 计划条款 | 判定 | 依据 |
|---|---|---|---|
| A-1 | §3.2 默认开启与 NO-GO 的冲突处理（协调者书面裁定） | **`BLOCKED`** | **不存在**。全仓检索无 `experiment-contract.yaml`，无「方案甲/乙」裁定 |
| A-2 | §3.1 冻结的实验契约 | **`BLOCKED`** | 同上；无冻结值可对照，A.4「配置消费值与冻结契约一致」无对象 |
| A-3 | §A.2.1 两类触发任务（功能触发 / 生产代表性） | **`CONDITIONAL`** | 配方已给出并实测消费（§3）；live 执行被 A-1 阻断 |
| A-4 | §A.2.2 实验专用 `compact_ratio` 作为显式变量 | **`PASS`（配方可用，未注册）** | 实测 `ratio=0.05` 被生产成员构造器消费；**注册表属 C 的写集，A 不改** |
| A-5 | §A.2.3 每臂配置消费证据 | **`BLOCKED`** | 无臂可跑（A-1）；静态消费链路已核对（§5） |
| A-6 | §A.2.4 边界验证（latch / headroom / hard ceiling） | **`PASS`** | §6，含 pre-A 对照 |
| A-7 | §A.2.5 因果链与「未触发」判定 | **`PASS`（记录为未观测）** | §7 |
| A-8 | §A.2.6 区分行为变化与纯观测字段，A-only 可隔离 | **发现缺口** | §8：A 的行为面**只有一个**受控开关；另有一处隔离脆弱性 |
| A-9 | §A.2.7 触发分布与代表性判据 | **`PASS`（给出判据）** | §9 |
| A-10 | §6 安全停止 | **未触发** | 未发起 live 请求 |
| A-11 | §A.4 回滚步骤 | **`PASS`** | §11 |

**本轮最重要的三条发现**（都不是 A 的代码缺陷，但都挡着「A-only 是否真的隔离了 A」这个结论）：

1. **计划 §3.3 的「`cache_aware_compaction` 在所有臂固定为同一值」在当前注册表上不成立**（§4）。实测 `baseline=false / a_only=true / b_only=false / a_plus_b=true` —— 计划自己的措辞是「**不得把它悄悄塞进 A-only**」，而现状正是如此。
2. **A 的行为面只有一个受控开关**（§8）。七态分类与 headroom 判据是**纯观测**（`MaintenanceState` / `HeadroomGoalMet` 在 `internal/agent` 之外**零消费者**），latch 是唯一有行为后果的改动且**已受控**。因此 A-only 的隔离**成立**，但成立的原因是「判据没有消费者」，这是一条**脆弱的**隔离——任何人将来让 `MaintenanceState` 驱动行为，baseline 臂就不再是基线。
3. **实验阈值配方可用且已被验证消费**（§3.2）：`agent.compact_ratio = 0.05` 把触发边界从 80 万降到 5 万，且编辑器 0.30 的下限**在配置加载路径上不强制**。这是把 A 的路径拉进可负担样本的唯一现实手段。

---

## 1. 快照与门禁

| 项 | 值 |
|---|---|
| 快照 | `git archive HEAD`，HEAD = `f53c4bf91d17b9d45fbf8a31656a8eed9cfffdeb` |
| 全 `.go` 聚合 sha256 | `f092831f5e9084e8a73fb7435991da2a1a56d8d8f8f1cdf2efd518a3ead13bc0` |
| 全文件聚合 sha256 | `db8b6be3c3f5cb228ebf0c6e348f1f769487fb880a2fc712aafeb93a1bc19f3b` |
| 生成命令 | `mkdir -p /tmp/nextA && git archive HEAD \| tar -x -C /tmp/nextA && cd /tmp/nextA && find . -type f -name '*.go' \| LC_ALL=C sort \| xargs sha256sum \| sha256sum` |
| 对照快照（pre-A） | `2c0f1dc61^` = `5241384b6`，全 `.go` 聚合 sha256 `d909d6eb9d5e407fb3dfdf1eaeeac7162729c8b671aa1de70e4a90a4849f21bc` |
| Go | `go1.26.6 linux/amd64` |

**工作树无代码改动**：`git status --porcelain` 只有 4 份文档 + 1 个测试文件，全部属于**另两个 Agent**（B 的三份交付 + 计划本身 + `internal/team/allowance_sensitivity_test.go`）。A 只新增本文。

| 门禁 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go vet ./internal/agent/` | **通过** |
| `gofmt -l internal/agent/` | **空** |
| `go test ./internal/agent/` | **ok**（51.681s） |
| `go test ./internal/boot/` | **ok**（18.579s） |
| `go test ./internal/control/` | **ok**（89.023s） |
| `go test ./internal/cli/` | **ok**（88.163s） |
| `bash scripts/cache-guard.sh` | **10/10 pass**（`TestReleaseCacheHitGuard` 8 例 + `large-tool-64k` + `large-tool-256k`） |
| `go run ./tools/repolint` | `repolint: clean (1145 baselined findings)` |
| A 侧定向测试（7 组） | **全部 PASS** |

原始输出归档于 `/tmp/nextA-artifacts/`：`gates-part1.txt`、`gates-part2.txt`、`gates-part3.txt`、`gates-cli.txt`、`a-probes.txt`、`a-margin.txt`、`c-probes.txt`、`prea-probes.txt`。

---

## 2. A-1：启动门禁未满足（**唯一的阻塞项**）

计划 §3.1 要求协调者在并行启动前建立版本化的 `experiment-contract.yaml`（或等价记录）。计划 §3.2 要求协调者**书面选择**方案甲或方案乙，并写明：

> 没有这项书面裁定时标记 `BLOCKED`，不运行可能扩大真实行为暴露的 live 实验。

**核验结果：两者都不存在。**

```bash
find / -maxdepth 4 -name 'experiment-contract*'      # 零命中
grep -rn "experiment-contract" docs/ internal/       # 只命中计划自己那一行
grep -rn "方案甲\|方案乙" docs/                       # 只命中计划自己那两行
```

因此 A 侧**不发起任何 live 请求**。这不是 A 的选择，是计划 §3.2 的字面要求。

**连带后果**：§A.4 的通过条件第 2 条「每臂配置消费值与冻结契约一致」**没有对照对象**——不存在冻结值。A 只能给出「当前构建实际消费什么」（§5），不能给出「是否与契约一致」。

---

## 3. A-3：触发配方（§A.2.1、§A.2.2）

### 3.1 边界在哪（生产成员装配实测）

在**生产成员构造器**（`memberBackendOptions` → `boot.Build`）上读 `ContextMaintenanceSnapshot()`，五个注册条件逐一：

```text
PROBE production  condition=baseline       fold_trigger=800000 hard_ceiling=999744 headroom_goal=160000 consumed_ratio=0.80
PROBE production  condition=a_only         fold_trigger=800000 hard_ceiling=999744 headroom_goal=160000 consumed_ratio=0.80
PROBE production  condition=b_only         fold_trigger=800000 hard_ceiling=999744 headroom_goal=160000 consumed_ratio=0.80
PROBE production  condition=a_plus_b       fold_trigger=800000 hard_ceiling=999744 headroom_goal=160000 consumed_ratio=0.80
PROBE production  condition=rescue_enabled fold_trigger=800000 hard_ceiling=999744 headroom_goal=160000 consumed_ratio=0.80
```

**读法**：五个臂的边界**完全相同**。这是**冷会话**读数（装配后尚无 usage receipt，`warmCache()` 为假），所以 `cache_aware_compaction` 的延迟效果（上一轮 Part C 报的 999,744）**没有出现**——探针不构成「a_only 与 baseline 同界」的结论，只说明冷态下同界。

**可负担的样本有多大**（`cachelib` 注册夹具）：

```text
PROBE fixture_bytes=8000   approx_prompt_tokens=2000
PROBE fixture_bytes=60000  approx_prompt_tokens=15000
PROBE fixture_bytes=240000 approx_prompt_tokens=60000
```

最大夹具约 **6 万 token**，而生产触发边界 **80 万** —— 差 **13 倍**。上一轮报的是「差 3–4 倍」（探针会话 23.2 万），本轮按夹具上限算是 13 倍。**结论方向一致：生产阈值下 A 的路径不可达。**

### 3.2 实验专用阈值的配方（§A.2.2）——**实测消费**

```text
PROBE ratio=0.05            condition=baseline fold_trigger=50000 hard_ceiling=999744 headroom_goal=160000 consumed_ratio=0.05
PROBE ratio=0.05,cap=16000  condition=baseline fold_trigger=50000 hard_ceiling=999744 headroom_goal=16000  consumed_ratio=0.05
PROBE window=200k           condition=baseline fold_trigger=800000 hard_ceiling=999744 headroom_goal=160000 consumed_ratio=0.80
```

三条结论：

1. **`agent.compact_ratio` 被生产成员构造器消费**：`0.05 → 50,000`，且 `ctrl.CompactRatio()` 回读也是 `0.05`。`ratio=0.05` 时最大夹具（6 万）**越过**触发边界，A 的 fold 路径**第一次变得可达**。
2. **`agent.visible_window_tokens` 被消费**：`headroom_goal` 从 160,000 降到 **16,000**。这正是**上一轮 B 修的那个「被 `agent.New()` 静默丢弃」的键**在本轮合流态下的**端到端确认**——headroom 目标受配置 cap 封顶，在活成员上可观察。
3. **provider 条目级 `context_window` 覆盖对成员无效**：`window=200k` 读数与生产逐字相同（800,000/999,744/160,000）。成员的窗口来自**模型条目**（`boot.go:1157` 的 `entry.ContextWindow`），配置里 `cfg.Providers[i].ContextWindow = 200_000` 没有走到那条解析路径。**该旋钮不可用**，不要把它写进任何配方。

**边界旁证**：`config.SetCompactRatio` 强制 `[0.30, 0.85]`（`edit.go:45`），但**加载路径不强制**：

```text
PROBE toml_ratio=0.05  loaded_ratio=0.0500
PROBE toml_ratio=0.10  loaded_ratio=0.1000
PROBE toml_ratio=0.99  loaded_ratio=0.9900
PROBE editor_ratio=0.05 refused: compact ratio 0.05: must be between 0.30 and 0.85
```

`load.go:691` 只做 `if CompactRatio <= 0 { 用默认 }`。所以**配置文件可以写 0.05**，编辑器不能。这既是配方的可行性依据，也是一处应当被知晓的**校验不对称**（编辑器与加载器对同一字段的合法域不同）。

### 3.3 两类任务

| 类 | 配方 | 能证明什么 | 本轮状态 |
|---|---|---|---|
| **功能触发** | `agent.compact_ratio=0.05` + `agent.visible_window_tokens=16000` + 6 万 token 夹具 | 代码路径确实运行、状态可复现 | **配方已验证消费**；live 执行被 §2 阻断 |
| **生产代表性** | 生产阈值不变，真实常见任务分布 | 自然维护事件频率 | **未观测**（需 80 万 token 会话） |

**计划 §A.2.2 的纪律照录**：功能触发只证明路径运行，**不可用于生产收益结论**；且该阈值必须作为显式实验变量写进 registry。

**A 侧不改 registry**：`internal/cachelab/plan.go` 是 C 的写集（§C.3「C 负责矩阵和分析」），计划 §4 要求「任何生产代码交叉需求先提交接口提案」。故本文只给配方，注册动作归 C。

---

## 4. **发现：计划 §3.3 的固定值要求不成立**

计划 §3.3 逐字要求：

> 主矩阵采用 2×2，`cache_aware_compaction` 在所有臂固定为同一值（默认建议固定 `false`）……若业务问题确实要评估 `cache_aware_compaction`，另开独立因子/矩阵，**不得把它悄悄塞进 A-only**。

**实测当前注册表**：

```text
PROBE baseline       agent.cache_aware_compaction=false
PROBE a_only         agent.cache_aware_compaction=true     ← 
PROBE b_only         agent.cache_aware_compaction=false
PROBE a_plus_b       agent.cache_aware_compaction=true     ← 
    PROBE PLAN-3.3-VIOLATED agent.cache_aware_compaction not fixed across routine arms: map[false:[b_only baseline] true:[a_plus_b a_only]]
```

**A 因子逐项清单**（相对 baseline 移动了哪些开关）：

```text
PROBE arm=baseline       moves_vs_baseline=[]
PROBE arm=a_only         moves_vs_baseline=[agent.cache_aware_compaction=true agent.low_yield_latch=true]
PROBE arm=b_only         moves_vs_baseline=[agent.message_shape_diagnosis=true]
PROBE arm=a_plus_b       moves_vs_baseline=[agent.cache_aware_compaction=true agent.low_yield_latch=true agent.message_shape_diagnosis=true]
PROBE arm=rescue_enabled moves_vs_baseline=[agent.cache_aware_compaction=true agent.context_rescue=true agent.low_yield_latch=true agent.message_shape_diagnosis=true]
```

**a_only 移动了 2 个开关，其中 `cache_aware_compaction` 是一个 2026-09-03 从上游合入的既有特性**（上一轮 FINAL_DECISION §2.2 明确记录「未把 `cache_aware_compaction` 改为默认开……不在批复范围内」）。

**这不是本轮引入的**：上一轮修 `baseline`/`b_only` 时把 a_only 注册成 `(cache_aware + latch)`，本轮计划 §3.3 **反转了该要求**。所以这是**计划与既有注册表之间的一处未对齐**。

**后果**：任何 `a_only` 的臂间差异，都**无法归因到 A 的维护状态机**——它同时移动了一个与维护无关的前缀延迟特性。

**处置**：属 C 的写集，A 只报告。**建议协调者二选一**：(a) 把四个常规臂的 `cache_aware_compaction` 固定为同一值（计划的字面要求），`cache_aware` 另开矩阵；或 (b) 书面接受 a_only 携带它，并**在结论里禁止把它读成 A 的效果**。

---

## 5. A-5：配置消费链路（§A.2.3 的静态部分）

无臂可跑（§2），但消费链路已逐段核对：

| 开关 | 配置键 | 生产消费点 | 实测 |
|---|---|---|---|
| `low_yield_latch` | `agent.low_yield_latch` | `config_ui.go:361 LowYieldLatchEnabled()` → `boot.go:1159/1742/1830` `DisableLowYieldLatch` → `agent.go:1068` `lowYieldLatch` → `context_headroom.go:224` | ✅ §6 探针 |
| `message_shape_diagnosis` | `agent.message_shape_diagnosis` | 同上三处 → `agent.go:1069` → `session_context.go:271` | ✅ 上一轮 B-3 |
| `cache_aware_compaction` | `agent.cache_aware_compaction` | `boot.go:1159` → `agent.go:1067` → `compact.go:115` | ✅ §3.1 冷态读数 |
| `context_rescue` | `agent.context_rescue` | 同上 → `agent.go:1070` | 静态核对 |
| `visible_window_tokens` | `agent.visible_window_tokens` | 同上 → `agent.go:1066` → `compact.go:163` | ✅ §3.2（goal 160k→16k） |
| `compact_ratio` | `agent.compact_ratio` | `boot.go:1163` → `agent.go:1062` → `compact.go:105` | ✅ §3.2（800k→50k） |

**A 侧的维护事件面**（§A.2.3 列的六项）在**当前快照上**落在哪里：

| 计划要求的事件 | 载体 | 成员记录里可见？ |
|---|---|---|
| summary 请求数 | `MaintenanceCost.SummaryRequests`（会话累计） | ✅ `.usage.json` 的 `maintenance` 段 |
| projection install | `MaintenanceCost.ProjectionInstalls` | ✅ 同上 |
| 状态结果 | `ContextMaintenanceReceipt.MaintenanceState` | ❌ **只在 sidecar 与进程内快照**，不进 `MemberCacheRequest` |
| headroom | `ContextMaintenanceReceipt.HeadroomTokens` | ❌ 同上 |
| reduction ratio | `ContextMaintenanceReceipt.ReductionRatio` | ❌ 同上 |
| latch block / release | `MaintenanceCost.RepeatBlocks` + `stuck` | ⚠️ 计数可见；**release 事件不可见** |
| rescue 次数 | `MaintenanceCost.RescueCount` | ✅ 同上 |
| 下一次相同 view 是否重入维护 | 无独立字段 | ❌ 需由 `MaintenanceCost` 差分推断 |

**计划 §A.3 要求的最小字段集（`triggered` / `trigger_type` / `condition` / `maintenance_generation` / `same_view_repeat` / `headroom_tokens`）**：实测 `trigger_type`、`maintenance_generation`、`same_view_repeat` **在全仓零命中**；`triggered` 零命中；`headroom_tokens` 存在于 `projection.go:116`（sidecar）但**不进成员记录**。

**计划 §A.3 同时要求「字段名以现有 schema 为准，不得并行创建重复真相源」**，且 B 的数据字典（`TEAM_MEMBER_CACHE_NEXT_PART_B_DATA_DICTIONARY.zh-CN.md` #12）已把维护/rewrite 的契约载体冻结为 `prefix_change_reasons` + `MessagesRewritten`。**A 侧因此不新增字段**：新增会与 B 冻结的字典冲突，且 §B.3 明确要求「需生产观测字段时先出提案，由协调者指定唯一实现者」。

**给协调者的缺口陈述**：A 的六项事件里，**三项只在 sidecar/进程内可见**。若矩阵需要按样本归因维护状态，需要一次**接口提案 + 单一实现者**，不是 A 单方面加字段。

---

## 6. A-6：边界验证（§A.2.4）

### 6.1 同一 view 的 latch 与 headroom（实测）

```text
PROBE arm=baseline(latch=off)  summaries=1 receipt_state=low_yield latched=false headroom=2801 goal=8000
PROBE arm=baseline(latch=off)  same_view_second_call_delta=0 stuckTokens=0
PROBE arm=a_only(latch=on)     summaries=1 receipt_state=low_yield latched=true  headroom=2801 goal=8000
PROBE arm=a_only(latch=on)     same_view_second_call_delta=0 stuckTokens=0
```

**两件事同时被证明**：

1. **latch 的边界正确**：`headroom=2801 < goal=8000` → `low_yield`；latch 打开时 `stuck=true`，关闭时 `stuck=false`。第二次同 view 调用**两个臂都是 0 次新增 summary**——但**原因不同**：baseline 臂是 `stuck=false`，说明挡住它的是 **receipt backoff**（`contextMaintenanceBlocked`），不是 latch。这与上一轮 A 报告的结论一致（「挡住第 3 次调用的是 receipt backoff，不是阈值」）。
2. **headroom 判据本身没有开关**：baseline 臂**照样发布** `receipt_state=low_yield`。见 §8。

### 6.2 hard ceiling 与 overflow（实测 + pre-A 对照）

```text
PROBE overflow_call=1 delta=1 err=<nil> est_before=37107 fold=25000 hard=49744 stuck=false
PROBE overflow_call=2 delta=1 err=...checkpoint candidate rejected... est_before=22199
PROBE overflow_call=3 delta=1 err=...checkpoint candidate rejected... est_before=22199
PROBE overflow_call=4 delta=1 err=...checkpoint candidate rejected... est_before=22199
PROBE manual_call=1   delta=1 err=<nil> est_before=37107 stuck=false
PROBE manual_call=2   delta=1 err=...checkpoint candidate rejected... est_before=22199
```

**pre-A（`5241384b6`）同形对照**：

```text
PREA overflow_call=1 delta=1 err=<nil> est_before=37107 fold=25000 hard=49744 stuck=false
PREA overflow_call=2 delta=1 err=<nil> est_before=1205
PREA overflow_call=3 delta=1 err=...checkpoint candidate rejected... est_before=46
PREA manual_call=1   delta=1 err=<nil> est_before=37107
PREA manual_call=2   delta=1 err=<nil> est_before=1205
```

**读法与边界结论**：

- **overflow 与 manual 两条梯级在 latch 之外**（`context_manager.go:154` 的守卫要求 `Trigger == pressure`）。这是**设计**，不是缺陷：物理恢复与用户显式请求不该被低收益抑制挡住。
- **pre-A 同样每次调用付 1 次 summary**（`delta=1` 四连）。A **没有**引入这个形状，也**没有**消除它——它是既有行为。
- **生产调用链有自己的上界**：`sampling_request.go:120` 与 `context_recovery.go:76` 都是「一次性物理恢复，不循环」（`budget.retries++` 与 `projectionVersion` 比对），所以上面的四连调用需要一个**绕过 turn loop 的合成调用者**才能构造。**探针演示的是 API 契约，不是生产可达路径**——这一点必须随数字一起引用。
- **硬天花板边界正确**：`hard=49744`，`foldLanded` 对 overflow 要求 `tokens < hard || tokens < fold`；`rescueOrFail` 只在 `Trigger==overflow || latest >= hard` 时进 rescue。pre-A 与 post-A 的 `hard` 都是 49,744，**A 未改动该边界**。

### 6.3 rescue 末端

`rescueOverCeiling` 的两个可达条件（`context_manager.go:269-271`）与上一轮一致：`Trigger == overflow` **或** `hard > 0 && result >= hard`。`!policy.AllowContextRescue` 时走 `rescueByTruncation`（无损降级，不轮转 session）。**live rescue 未触发**（§2），只有静态证据。

---

## 7. A-7：因果链与「未触发」判定（§A.2.5）

计划要求「事件为零时明确判为**未触发**」。A 侧**没有产生任何 live 样本**，因此：

> **A 的维护路径在真实使用分布上**尚未观测**。** 不是「未触发」（那需要一个真实会话跑到 80 万 token 而没触发），也不是「无缺陷」。

**因果链的静态完整版**（每条都已在代码上核对）：

```text
实验条件(compact_ratio / latch / shape / rescue)
  → 成员实际配置（boot.go:1159/1742/1830 → agent.Options）
    → 维护决策（context_manager.go prepareOnce → foldContext）
      → Provider attempt（runSummaryRequest → noteSummaryRequest）
        → usage/事件（receipt → emitContextMaintenance；counters → .usage.json）
          → 后续 turn（settleMaintenanceFold → latch → 下一次 prepareOnce 的守卫）
```

**该链上唯一未被本轮验证的一环**是第 4→5 步的**真实 Provider usage 回流**（需要凭证）。前 3 步与第 6 步已由 §3、§6 的探针在生产装配上验证。

---

## 8. A-8：A 因子的行为面 vs 观测面（§A.2.6）——**发现两处**

### 8.1 结论：A 的行为面**只有一个**受控开关

逐项盘点 A 侧改动（`2c0f1dc61` 引入 + `6aed0fbcc` 加开关）：

| 改动 | 性质 | 开关 | 判定 |
|---|---|---|---|
| `lowYieldLatch`（`settleMaintenanceFold` 的 latch 分支 + `releaseMaintenanceLatch`） | **行为** | `agent.low_yield_latch` | ✅ 受控 |
| headroom 判据 `GoalMet()` + `maintenanceDecisionFor` 的 receipt 字段 | **观测** | 无 | ⚠️ 见 8.2 |
| 七态分类 `State()` | **观测** | 无 | ⚠️ 见 8.2 |
| 四项计数器（`maintenanceSpend`） | **观测** | 无 | ✅ 纯计数，无分支 |
| `context_status.go` 的 `HeadroomGoal`/`HeadroomGoalMet`/`MaintenanceState` | **观测** | 无 | ✅ 无消费者 |

**判定：A-only 确实隔离了 A 的**行为**变化。** 理由：`GoalMet()` 的唯一行为调用点是 `settleMaintenanceFold`，而该分支被 `!a.lowYieldLatch` 提前返回——**latch 关闭时 `GoalMet()` 的结果不影响任何行为**。

### 8.2 但这是一条**脆弱**的隔离

`MaintenanceState` / `HeadroomGoalMet` 的消费者检索：

```bash
grep -rn 'MaintenanceState\|HeadroomGoalMet' internal/ --include=*.go | grep -v _test | grep -v 'internal/agent/'
# 零命中
```

（`internal/control/maintenance.go:361` 与 `internal/event/runtime_state.go:30` 的 `MaintenanceState` 是**控制器自己的维护作业状态**，与 receipt 字段同名不同物。）

**含义**：headroom 判据今天**不改变任何行为**，只改一个没人读的 receipt 字段。**一旦有人让 `MaintenanceState` 驱动行为（例如按状态做 backoff），baseline 臂立刻不再是 baseline**——而矩阵不会因此变红，因为没有任何测试断言「`MaintenanceState` 无消费者」。

**建议**（不阻塞，属 C/协调者）：在 registry 的 `Unchanged` 里加一条可机械检查的项，或让 `team_condition_matrix_test.go` 的守卫补一句「A 的行为面 = `lowYieldLatch` 一个开关」的显式声明。

### 8.3 顺带确认：`_ = u` 的 post-turn observer 仍在

```go
// ObserveUsage is retained as a compatibility hook. Usage observations never
// mutate the provider-visible checkpoint.
func (m ContextManager) ObserveUsage(u *provider.Usage) { _ = u }
```

`run_loop.go` 有 **6 个**调用点，全部是空操作。A 未改动它（`2c0f1dc61` 的 diff 里没有 `context_manager.go` 的 `ObserveUsage`）。**上一轮 A-10 记录的 `lastTurn` 死字段（其注释声称阻止该 observer 重复付费）与本条是同一处**——observer 已是空实现，`lastTurn` 更无从阻止。

---

## 9. A-9：触发分布与代表性判据（§A.2.7）

**本轮可给出的判据**（无 live 样本，故只给判据不给分布）：

| 判据 | 阈值 | 依据 |
|---|---|---|
| 压力触发可达性 | 夹具 prompt ≥ `compact_ratio × window` | §3.2：`ratio=0.05` 时 6 万 ≥ 5 万 ✅ |
| 自然触发可达性 | 真实会话 prompt ≥ 80 万 | **本机不可达**（夹具上限 6 万；上一轮探针 23.2 万） |
| 生产代表性充分性 | 至少一条真实会话越过生产触发边界，且**未被实验阈值修改** | 未满足 |
| 压力 vs 生产可区分性 | 压力臂必须标 `ratio=0.05` 且结论**不外推** | 配方已标 |

**结论**：**压力触发配方已就绪，生产代表性采样在当前资源下不可行**。要让生产路径可观测，只有两条路：(a) 真实撑到 80 万 token（费用/时延/Provider 限制需审批，计划 §6 已警告「不把 token 膨胀至 80 万作为唯一触发方式」）；(b) 接受「只测压力触发 + 显式标注不可外推」。**这是协调者的选择，A 不代选。**

---

## 10. 安全停止（§6）

**未触发**：A 侧未发起任何 live 请求，无超 hard ceiling、无跨成员污染、无费用。

**A 侧主动执行的边界**：所有探针都在**冻结快照**（`/tmp/nextA`、`/tmp/preA`）内运行，探针文件运行后即删；工作树**零代码改动**。

---

## 11. 回滚（§A.4）

本轮 A 侧**没有生产代码变更**，因此无需回滚。若要回滚**上一轮**的 A 侧改动（供参考，与上一轮 A 报告 §11 一致）：

| 变更 | 配置回滚 | 代码回滚 |
|---|---|---|
| `agent.low_yield_latch` | 设为 `false` → 恢复「输入哈希一变就重试」 | `context_headroom.go:224` 的 `!a.lowYieldLatch` 项 |
| `agent.message_shape_diagnosis` | 设为 `false` → 数组完全不比较 | `session_context.go:271` 的 `diagnoseMessages` |
| headroom 判据（观测） | 无配置面 | `GoalMet()` 恒返回 `true`（`context_headroom.go:70`） |
| 四项计数器（观测） | 无配置面 | 删字段与调用 |

**回滚后需复跑**：`go test ./internal/agent/ -count=1`、`bash scripts/cache-guard.sh`、`go run ./tools/repolint`。回滚 latch 后 `TestLowYieldFoldLatchesInsteadOfRepayingTheSameView` / `TestLowYieldLatchReleasesOnGrowth` **会失败**，这是预期的——它们是**行为契约**。

---

## 12. 失败 / 跳过 / unknown 清单（§2.3 第 4 项）

| 项 | 状态 | 说明 |
|---|---|---|
| `go build` / `vet` / `gofmt` | **0 失败** | §1 |
| `internal/agent` / `boot` / `control` / `cli` | **0 失败** | 51.7s / 18.6s / 89.0s / 88.2s |
| `cache-guard` | **10/10 pass** | §1 |
| `repolint` | **clean（1145 baselined）** | §1 |
| A 侧 7 组定向测试 | **0 失败** | §1 |
| 探针文件 | **全部执行后删除** | 快照哈希复核未变 |
| **live 触发（功能 / 生产代表性）** | **`BLOCKED`** | §2：计划 §3.2 的书面裁定不存在 |
| **每臂配置消费统计** | **`BLOCKED`** | 无臂可跑；§5 只给静态链路 |
| **rescue 真实触发** | **未验证** | 需 `context_rescue=true` + 真实越限 |
| **1M 桶 / 并发** | **未验证** | 计划 §5 P3、§6.2 |
| `desktop/` 模块 | **未跑** | 其 host-contract 测试在 HEAD 上即红（既有） |
| `golangci-lint` | **skipped** | 本机未装；CI pin 2.12.2 |
| **§3.3 固定值不成立** | **已报告，未修** | §4：属 C 的写集 |
| **§A.3 最小字段集缺口** | **已报告，未加字段** | §5：需接口提案 + 单一实现者 |

---

## 13. 原始命令记录（§2.3 第 2 项）

```bash
# 快照
mkdir -p /tmp/nextA && cd /home/zwc/Agent/DeepSeek-Reasonix
git archive HEAD | tar -x -C /tmp/nextA
cd /tmp/nextA && find . -type f -name '*.go' | LC_ALL=C sort | xargs sha256sum | sha256sum
# → f092831f5e9084e8a73fb7435991da2a1a56d8d8f8f1cdf2efd518a3ead13bc0

# pre-A 对照
mkdir -p /tmp/preA && git archive 2c0f1dc61^ | tar -x -C /tmp/preA
cd /tmp/preA && find . -type f -name '*.go' | LC_ALL=C sort | xargs sha256sum | sha256sum
# → d909d6eb9d5e407fb3dfdf1eaeeac7162729c8b671aa1de70e4a90a4849f21bc

# 门禁（全部在 /tmp/nextA）
go build ./... && go vet ./internal/agent/ && gofmt -l internal/agent/
go test ./internal/agent/ ./internal/boot/ ./internal/control/ ./internal/cli/ -count=1
bash scripts/cache-guard.sh && go run ./tools/repolint

# A 侧定向
go test ./internal/agent/ -run 'TestMaintenanceDecision|TestRecentTailBudget|TestLowYield|TestOverflowBypasses|TestTheLadderOnly|TestRescueIsTheLast|TestMaintenanceCost' -v -count=1

# 探针（运行后删除，输出归档于 /tmp/nextA-artifacts/）
go test ./internal/cli/   -run 'TestZZProbeTriggerMargin' -v -count=1   # → a-margin.txt
go test ./internal/agent/ -run 'TestZZProbe' -v -count=1                # → a-probes.txt
go test ./internal/cachelab/ -run 'TestZZProbe' -v -count=1             # → c-probes.txt（含 §3.3 违规）
go test ./internal/config/ -run 'TestZZProbeCompactRatioLoadBound' -v -count=1
cd /tmp/preA && go test ./internal/agent/ -run 'TestZZPreAOverflowPerCall' -v -count=1  # → prea-probes.txt
```

**环境**：`go version go1.26.6 linux/amd64`，`linux/amd64`，分支 `Team-agent`。

---

## 14. 状态

# `BLOCKED`

**阻塞项**（计划 §3.2 的字面判定）：协调者未书面选择方案甲/乙，也未建立 `experiment-contract.yaml`。**A 侧不发起 live 实验。**

**已完成且不依赖该裁定的部分**：触发配方与消费验证（§3）、§3.3 固定值缺口（§4）、配置消费链路（§5）、边界证据含 pre-A 对照（§6）、因果链（§7）、行为面/观测面盘点（§8）、代表性判据（§9）、回滚（§11）。**A 侧未发现 A 的代码缺陷。**

**解除 `BLOCKED` 需要协调者**（按依赖排序）：

1. **书面选择方案甲或方案乙**（§3.2），并建立冻结契约（§3.1）——这是唯一的硬前置。
2. **裁定 §3.3 的固定值**：把 `cache_aware_compaction` 在四个常规臂固定为同一值（计划字面要求），或书面接受 a_only 携带它并禁止归因（§4）。
3. **裁定实验阈值**：`agent.compact_ratio = 0.05` 是否作为显式实验变量注册（配方已验证可用，§3.2），以及是否接受「只测压力触发、不可外推」。
4. **决定 §5 的三项 sidecar-only 事件是否需要进成员记录**；若需要，指定唯一实现者（A 不自行加字段）。

**在此之前维持**：**不发起 live 实验，不宣称 A 的路径在生产分布上有效或无效——它尚未被观测。**

---

## 15. 协调裁定下的执行与对 C 交付的独立交叉复核（裁定后追加）

> 本节记录协调裁定 `TEAM_MEMBER_CACHE_NEXT_ROUND_COORDINATION_DECISIONS.zh-CN.md`
> 发布后 A 侧的工作。§1–§14 是裁定前的记录，**保持原样不修改**。
> C 已交付：`TEAM_MEMBER_CACHE_NEXT_PART_C_RESULT.zh-CN.md` + `contract.go` +
> `plan.go`/`condition_test.go` 修正 + `live_team_cache_condition_test.go` + 冻结的
> `/tmp/experiment-contract.yaml`。

### 15.1 快照

| 项 | 值 |
|---|---|
| 内容 | `git archive HEAD`（`f53c4bf91`）+ **C 的 5 个未提交文件**（契约正是从这些字节渲染的） |
| 全 `.go` 聚合 sha256 | `1741b97aefcc21af382bbc6c6ff1ea2de0830e0b153595fed0a8fbb178a32cd3` |
| 全文件聚合 sha256 | `3ad4a4310e721e7f6f84ec8c00bc915a8144c6a6b006104d486c4c38ef100d4f` |
| Go | `go1.26.6 linux/amd64` |

**与 §1 的 `f092831f…` 不可混淆**：那一份是纯 `HEAD`，**不含** `contract.go`、`TriggerProfile`、
新守卫与 live pilot。契约声明的 `baseline_commit: f53c4bf91…` 指的是**树**，但契约本身
**不在那棵树里**（见 §15.3 第 1 条）。

### 15.2 门禁（本节快照）

| 检查 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go vet ./internal/cachelab/` + `go vet -tags live ./internal/cli/` | **通过** |
| `gofmt -l internal/cachelab/ internal/cli/ internal/agent/ internal/team/` | **空** |
| `go test ./internal/agent/` | **ok**（54.057s） |
| `go test ./internal/boot/` | **ok**（23.752s） |
| `go test ./internal/cli/` | **ok**（100.256s） |
| `go test ./internal/cachelab/` | **ok** |
| `TestEveryConditionReachesTheMembersOwnAgent` | **5/5 条件 PASS** |
| C 的 5 个新守卫（§15.3 第 3 条） | **全部 PASS** |
| `scripts/cache-guard.sh` | **10/10 pass** |
| `go run ./tools/repolint` | **clean（1145 baselined findings）** |

原始输出归档于 `/tmp/nextA2-artifacts/`：`gates1.txt`、`gates2.txt`、`gates3.txt`、
`a-crosscheck.txt`、`a-crosscheck-notes.txt`。交叉复核脚本 `/tmp/nextA2-audit/audit.py`。

### 15.3 对 C 交付的独立复核（§3 第 2 步 / §2.4 的交叉复核要求）

#### 1. **契约的内容哈希可复现，但它指向的提交不含契约本身**

`ContractDigest` 独立复算：**`1de71fbc13b41ff4`，与冻结产物逐字相符**；
只改 baseline commit 字符串即改变摘要（短 SHA → `d3329542663eabdd`），说明它确实覆盖注册。

但 `experiment-contract.yaml` 记录 `baseline_commit: f53c4bf91…`，而该提交：

```text
HEAD 有 TriggerProfile 吗：0 处
HEAD 有 contract.go 吗：  否
HEAD 有 §2.2 的新守卫吗：  0 处
```

**契约的 `content_hash` 覆盖注册，但那些注册只存在于未提交的工作树里。** 这不影响本轮的结论
（我按 §15.1 的叠加快照复算，摘要对得上），但**冻结凭据必须连同产生它的字节一起被保存**，
否则 `f53c4bf91` 这个名字指向的是一棵渲染不出该契约的树。**建议 C 提交后重新冻结一次**，
或把「工作树叠加态」写进契约（`content_hash` 旁边加一个工作树摘要字段）。

#### 2. **C 文档 §9 的复算命令指向一个不存在的测试**

```bash
go test ./internal/cachelab/ -run TestRenderContractForArtifact -v   # 见 contract.go 的 DraftContract
```

全树检索 `TestRenderContractForArtifact`：**零命中**。该命令不会渲染任何东西（`no tests to run`）。
`DraftContract`/`RenderContract`/`ContractDigest` 在生产与测试代码里**都没有调用者**——
`/tmp/experiment-contract.yaml` 是**在快照外一次性生成的**，仓库里没有任何东西能再生成它。
**建议补一个最小的渲染测试**（或把命令改成实际可跑的复算脚本）。

#### 3. **逐臂配置消费：11 条 banner 全部独立复现**（这是本轮最有价值的一条）

对 C 归档里**每一条** `condition-banner-*.txt`，我做了两件独立的事：

1. 把 banner 的 `switches` 字符串与 `RegisteredConditions()` 的注册**逐字符**比对；
2. 用**生产成员构造器**独立装配同一个臂，读它自己 agent 的边界，与 banner 声明的值比对。

```text
PROBE banner=1790305719820006583 condition=baseline       switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305747888896110 condition=a_only         switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305774816310881 condition=b_only         switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305790716146282 condition=a_plus_b       switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305809745819285 condition=rescue_enabled switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305904987652751 condition=baseline       switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305926458542062 condition=a_only         switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305946013338369 condition=baseline       switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305965033045810 condition=a_only         switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790305987769266826 condition=baseline       switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banner=1790306009471148209 condition=a_only         switches_match=true boundaries_match=true (50000/16000/999744)
PROBE banners_checked=11
```

**11/11 全对。** 这同时确认了裁定 §2.2（四臂 `cache_aware_compaction=false` 一致）
与 §2.3（`pressure` profile 的 50,000/16,000 成对到达成员 agent）在**证据层**成立，
不只是注册层。

#### 4. **归档的原始记录：逐条独立复算，全部相符**

只读 C 的归档（`/home/zwc/reasonix-partc-archive/condition-matrix`），用我自己的实现重算：

| 报告声明 | 我的复算 | 判定 |
|---|---|---|
| boundary ↔ members 逐样本 `prompt/hit/miss` 相等 | 11 轮 × 全部样本，**0 处不符** | ✅ |
| 每臂有效 warm（4/4/4/4/4，`a_only` 一次 7） | `[4,4,4,4]` / `[7,4,4,4]` / `[4]` / `[4]` / `[4]`，合计 **47** | ✅ |
| warm 段加权命中率 | `20.03/20.72/19.81/20.78`、`37.04/20.08/20.74/20.17`、`20.00`、`20.34`、`20.10` | ✅ 逐字相符 |
| 128 模型（append 步） | **held=25 / violated=0**，shrink 步 22，非 128 倍数者 **0** | ✅ |
| oracle 一致 | present=58 / agrees=58 | ✅ |
| 首请求 hit | `{2944: 10, 2816: 1}` | ✅ |

**另外用生产分类器（`Sample.Classify`）的语义重实现了一遍**：`first_request` 1 个/轮，
其余全部 `warm`，**`repeat`/`retry`/`error`/`no_split` 均为 0**。与报告的 warm 计数一致。

#### 5. **归因留痕：维护事件在成员记录里确实可见，但只有"有一个 fold"**

11 轮**每一轮**的 `session_request_seq == 4` 都带 `prefix_change_reasons = ["compact_auto"]`，
且 seq4 的 prompt 相对 seq3 缩小（`3805 → 3838` 等，`a_only` 那次 `3666 → 4881` 是另一次写）。
**这正是"fold 发生了"的独立痕迹**——C 报告的 `summaries=1 installs=1` 不是孤证。

**但 fold 的**结果**（`recovered`/`low_yield`、headroom、reduction）在成员记录里读不到**：

```text
全部 58 条成员记录中，maintenance_state / headroom_tokens / reduction_ratio 的非空出现次数：0
```

C 报告 §4.2 的 `state=recovered` 读的是 `ctrl.ContextMaintenanceSnapshot().MaintenanceState`，
即**进程内快照**。这与 A 报告 §5 的缺口陈述完全一致，也正是**协调裁定 §2.4 要解决的那一项**。

### 15.4 A 侧对 C 的四条判定（§3 第 2 步）

| # | C 的声明 | A 的复核判定 | 依据 |
|---|---|---|---|
| 1 | §2.1 矩阵无混杂，A 行为面 = 一个开关 | **成立** | §15.3 第 3 条；11/11 banner 复现；`TestAOnlyMovesTheLatchAndNothingElse` 通过 |
| 2 | §2.3 契约已冻结，摘要可复算 | **成立，附一处凭据缺陷** | §15.3 第 1、2 条 |
| 3 | §4.2 维护路径首次真实触发 | **成立** | §15.3 第 5 条：每轮 seq4 带 `compact_auto` 且 prompt 缩小 |
| 4 | §3.4 profile 算术修正（`goal < trigger`） | **成立** | 11/11 banner `50000/16000`；`TestTriggerProfilesKeepTheMaintenancePathReachable` 通过 |

**A 未发现 C 的行为缺陷。** 四条判定全部成立，两处是**凭据/可复现性**问题，不是结论问题。

### 15.5 A 侧发现的**新**证据边界（C 报告未记录，建议补入其 §6.3）

#### 1. 那组"臂内离散度"读数，测量的是**桶混合**，不是臂稳定性

`baseline` 每轮 5 个请求里，**两个大请求占了 90% 的 token 权重**：

```text
baseline run 006583  per request:
  seq=1 turn=1 prompt=53803 hit=2944  hit_rate= 5.5%  token_share_of_run=45.2%
  seq=2 turn=2 prompt=53671 hit=2688  hit_rate= 5.0%  token_share_of_run=45.1%
  seq=3 turn=2 prompt= 3805 hit=2944  hit_rate=77.4%  token_share_of_run= 3.2%
  seq=4 turn=3 prompt= 3838 hit=3712  hit_rate=96.7%  token_share_of_run= 3.2%
  seq=5 turn=4 prompt= 3871 hit=3712  hit_rate=95.9%  token_share_of_run= 3.3%
```

所以 `20.03%` 这个数**几乎完全由两个"首次读取 53k brief"的请求决定**（它们只能命中
~3k 的 system/tools 前缀）。换成"每 turn 取最后一个请求"的匹配口径，同一批数据变成
**89–94%**（baseline 四轮 90.05/92.07/92.35/93.57）。

**这不是说 C 算错了**——契约把 `warm` 定义为「任何有 split 的非首请求」，C 按契约算，
算法可复现。**但报告把 19.81–20.78% 的极差解释为"臂内离散度与臂间差异同量级"，
而这个极差主要由两个大请求的缓存写入时机决定，不是维护行为。** 建议在 §6.3 补一句
分桶口径说明，否则读者会把 20% 当成"该臂的真实命中率"。

#### 2. 那次 `37.04%` 是**三次 `quality_fail` 请求**拉高的

`a_only` run 896110 有 8 个请求，其中 seq3–5 的 `quality_check = quality_fail`：

```text
seq=3 turn=2 quality_fail prompt= 3666 hit=2944 miss= 722
seq=4 turn=2 quality_fail prompt= 4881 hit=3584 miss=1297
seq=5 turn=2 quality_fail prompt= 6006 hit=4864 miss=1142
```

这三次是**短请求且前缀已被缓存**，命中率很高。剔掉它们（只留 `quality_pass`）后，
同一轮的 warm 命中率是 **28.73%**，不是 37.04%。

C 报告 §4.3 已如实记录这三条 `quality_fail`，也说明了"该轮任务未通过"；
但 §6.3 把 `37.04%*` 与其它三次并列展示，**星号只解释样本数为 7，没解释那 3 个失败请求
在 token 权重上的作用**。建议补注：该数**包含三次任务失败的请求**，不可与其余三次同读。

### 15.6 状态

# `CONDITIONAL`

**裁定 §4 说「不自动将 Part A 的整体状态改为 PASS」——A 侧同意，且本轮也确实不该是 PASS**，
理由不是形式上的：A 的**行为**路径至今只有**功能触发 profile 下的 5 个臂各 1 次**样本，
生产代表性 profile **仍未观测**（裁定 §2.3 也要求分开汇报）。

**已解除的阻塞**：§2 的 `BLOCKED`（缺书面裁定/冻结契约）——裁定已发布，契约已冻结，
A 侧的隔离工作已按 §3 第 2 步完成，并对 C 的交付做了独立交叉复核（§15.3–§15.5）。

**仍未解除**（按依赖排序，全部属协调者/其它 Agent）：

1. **C 提交契约相关的字节并重新冻结**（§15.3 第 1、2 条）——否则 `content_hash` 与
   `baseline_commit` 的对应关系不成立。
2. **§2.4 的三个字段接线**（唯一实现者 C）——A 已确认语义与生成时点（§15.7）。
3. **每臂 30 有效 warm × 3 次运行**（C 报告 §12 第 2 条已算出工作量）。
4. **live pilot 预算与凭证范围的单独批准**（裁定 §3 第 5 步）。
5. **生产代表性 profile 的触发**——未观测，且当前资源下不可达。

### 15.7 给 C 的 §2.4 接口说明（裁定要求 A 确认的语义与生成时点）

| 字段 | 语义 | 生成时点 | 本轮实测值 |
|---|---|---|---|
| `MaintenanceState` | 七个取值之一，**只有 3 个能到达已发布的 receipt** | 三个构造点：`compact_commit.go:139`（summary fold）、`maintenance_commit.go:70`（prune/truncate）、`context_receipt.go:166`（blocked/failed） | `recovered` |
| `HeadroomTokens` | `Fold − Result`，**可为负**（fold 落在边界之上时） | 同上；由 `maintenanceDecision.Headroom()` 计算 | `50000 − result` |
| `ReductionRatio` | `(Estimate − Result) / Estimate` | 同上；`Estimate <= 0` 或 `!Applied` 时**失败关闭为 0** | 与 headroom 同源 |

**两条接线时必须知道的语义**：

1. **三个字段都是 `omitempty`**，且 `ReductionRatio` 在 `Estimate <= 0` 时**返回 0** ——
   于是「失败关闭的 0」与「真的没有削减」在 JSON 上**无法区分**。裁定 §2.4 要求
   「缺失值必须保持为未观测，不得补零或推断」，所以接线时**不能用零值判断缺失**；
   应当以 `MaintenanceState` 是否存在作为「本请求发生过维护决策」的开关，其余两字段随它。
2. **`context_receipt.go:166` 那个构造点恒 `Blocked=true`**，所以它只会产出 `blocked`；
   而 `rescued` 由 `rescueOverCeiling` 直接计数、**不写 receipt**。
   因此 `maintenance_state` 的**取值域是 3 个**（`recovered` / `low_yield` / `blocked`），
   不是 7 个——数据字典若写成七态会与实现不符。

**A 的写集边界**：本节只做确认，**未修改任何代码**。§2.4 的接线由 C 独占。

