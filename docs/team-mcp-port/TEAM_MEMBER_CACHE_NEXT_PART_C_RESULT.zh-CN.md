# Part C 下一轮结果：无混杂矩阵、功能触发与最终准入

> 执行依据：`TEAM_MEMBER_CACHE_NEXT_ROUND_3AGENT_PLAN.zh-CN.md` §4「Part C」、§3「启动门禁与共享实验契约」、
> 协调裁定 `TEAM_MEMBER_CACHE_NEXT_ROUND_COORDINATION_DECISIONS.zh-CN.md` §2.2/§2.3。
> 日期：2026-09-25。分支 `Team-agent`，快照 `f53c4bf91d17b9d45fbf8a31656a8eed9cfffdeb`。
> **状态：`CONDITIONAL`** —— 矩阵已无混杂并**首次在真实 Provider 上逐臂执行**，
> 功能触发路径**首次真实触发**；但正式矩阵的样本量与跨账号/网关覆盖**未满足**，
> 因此 §C.4 的六项准入条件仍不齐。本报告不授权灰度。

---

## 0. 结论

| # | 计划条款 | 判定 | 依据 |
|---|---|---|---|
| C-1 | §C.2.1 冻结 registry 与配置快照；逐臂断言 | **`PASS`** | §2；`experiment-contract.yaml` 已生成，逐臂消费断言实跑 |
| C-2 | §C.2.2 无凭证矩阵守卫 + 小规模 live pilot | **`PASS`（pilot）/ `CONDITIONAL`（对应性）** | §3、§4；五臂 live 跑通，边界与成员记录逐条对应 |
| C-3 | §C.2.3 样本量/功效计划 | **`PASS`（已冻结）/ 未达成** | §5；门槛 30 有效 warm × 3 次运行，本轮每臂 4–7 |
| C-4 | §C.2.4 执行正式矩阵 | **未执行** | §5.3：无预算批准，且功效不足 |
| C-5 | §C.2.5 A 因子两类结果 | **`CONDITIONAL`** | §6；受控触发子集已测，自然分布**未观测** |
| C-6 | §C.2.6 主指标 | **部分可测** | §6.3–§6.6；miss/hit/latency/unknown 可测，**成本未登记价格**，质量与完成率不可测 |
| C-7 | §C.2.7 安全停止 | **`PASS`（未触发）** | §7；零错误、零 no_split、零 confound |
| C-8 | §C.2.8 Rescue 独立验证 | **未验证** | §6.7；`rescues=0`，如实记「未验证」 |
| C-9 | §C.2.9 分层与敏感性 | **`CONDITIONAL`** | §6.8；单账号单网关单桶，不做外推 |
| C-10 | §C.2.10 预注册门槛 go/no-go | **`NO-GO / CONTINUE-SAMPLING`** | §8 |

**本轮最重要的三条结果**：

1. **矩阵第一次真的无混杂**（§2）。协调裁定 §2.2 的 `cache_aware_compaction=false` 已落地，
   四个常规臂**只**在目标因子上不同；新增守卫把这个性质钉死，改回去会红。
2. **A 的维护路径第一次在真实 Provider 上触发**（§4.2）。`pressure` profile 下
   `summaries=1 / installs=1 / state=recovered` 逐臂复现，且**边界与成员记录逐条对上**。
3. **发现并修正了一处会污染整轮矩阵的缺陷**（§3.4）：`compact_ratio` 一旦低于 headroom goal 的
   16% 占比，**每次 fold 都必然 latch**——那是 profile 的算术，不是维护行为的结论。
   修正方式是把 `visible_window_tokens` 一并注册为 profile 的组成部分。

---

## 1. 快照与门禁

| 项 | 值 |
|---|---|
| 快照 | `git archive HEAD`，HEAD = `f53c4bf91d17b9d45fbf8a31656a8eed9cfffdeb` |
| 契约内容哈希 | `1de71fbc13b41ff4`（由 `ContractDigest` 生成，命令见 §10） |
| 原始数据位置 | `/home/zwc/reasonix-partc-archive/condition-matrix/`（本轮 11 次运行，44 个文件） |
| 原始数据哈希 | 同目录 `MANIFEST.sha256`（逐文件 sha256，`sha256sum -c` 可复核） |
| 作废数据位置 | `.../superseded/`（§3.3 修正前的 7 次运行，36 个文件 + 自己的 `MANIFEST.sha256`），**不进入任何结论** |
| Go | `go1.26.6 linux/amd64` |
| 构建标签 | `live` |

| 门禁 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go vet ./internal/cachelab/`、`go vet -tags live ./internal/cli/` | **通过** |
| `gofmt -l internal/cachelab/ internal/cli/` | **空** |
| `go test ./internal/cachelab/ -count=1` | **ok** |
| 矩阵守卫 `TestEveryConditionReachesTheMembersOwnAgent` | **5/5 条件 PASS** |
| `go run ./tools/repolint` | **clean (1145 baselined findings)** |
| 本轮新增守卫（§2.4 四组） | **全部 PASS** |

**写集**：`internal/cachelab/plan.go`（条件注册表 + trigger profile）、
`internal/cachelab/condition_test.go`（守卫）、`internal/cachelab/contract.go`（新增，契约渲染）、
`internal/cli/live_team_cache_condition_test.go`（新增，live pilot）。
**未触碰** `internal/agent/**`（A 的写集）、`internal/team/**` 生产观测、`internal/cli/team_usage_publish.go`。
`git status --porcelain` 的其余条目属另两个 Agent。

---

## 2. C-1：无混杂矩阵与配置快照

### 2.1 协调裁定 §2.2 已落地

`cache_aware_compaction` 在四个常规臂上**固定为 `false`**，不再随 A 因子移动：

| 条件 | `cache_aware_compaction` | `low_yield_latch` | `message_shape_diagnosis` | `context_rescue` |
|---|---:|---:|---:|---:|
| `baseline` | false | false | false | false |
| `a_only` | false | **true** | false | false |
| `b_only` | false | false | **true** | false |
| `a_plus_b` | false | **true** | **true** | false |
| `rescue_enabled` | false | true | true | **true** |

**A 的行为面只有一个开关**（上一轮 Part A 的静态核验在此成为注册表事实）：`a_only` 相对 `baseline`
**只**移动 `agent.low_yield_latch`。七态分类与 headroom 判据是**观测面**，
已在注册表中以 `Observes` 单列，并出现在**包括 baseline 在内**的每一个臂上——
这正是「receipt 差异不是效果」这句话在数据里的落点。

### 2.2 触发 profile 成为注册对象（§2.3）

计划 §A.2.2 要求「若用实验专用 `compact_ratio` 降低触发线，将值作为显式实验变量写入 registry」。
本轮把它做成 `TriggerProfile`：

| profile | `compact_ratio` | `visible_window_tokens` | 生产代表性 |
|---|---:|---:|---|
| `production` | 0（即配置默认） | 0（即默认 16%） | **是** |
| `pressure` | 0.05 | 16000 | **否** |

`ProductionRepresentative` 是**机器可读的**：报告里每一行都带它，读者不需要靠上下文判断
某个数字能不能当生产频率引用。

### 2.3 契约已冻结

`cachelab.Contract` + `RenderContract` 从**同一批注册**渲染 `experiment-contract.yaml`：
快照、逐臂开关、profile、有效样本定义、指标、停止规则、隐私口径、预算、凭证范围。
内容哈希 `1de71fbc13b41ff4` 覆盖**注册**而非散文，因此快照可以被「它会执行什么」命名。
渲染是确定性的（开关经 `sortedSwitchKeys` 展平），两次渲染逐字节相同——
**diff 即注册变更**。

### 2.4 新增守卫（把上述性质钉死）

| 守卫 | 钉住什么 |
|---|---|
| `TestCacheAwareCompactionIsFixedAcrossTheMatrix` | 该键在**所有**条件上同值；任一臂移动它即红 |
| `TestAOnlyMovesTheLatchAndNothingElse` | A-only 相对 baseline **只**移动 `low_yield_latch`；观测面在两臂上逐字相同 |
| `TestOnlyTheBaselineEnablesNothing` | 只有 baseline 不打开任何开关；其他条件若一个都不开即红（「baseline 换名」） |
| `TestTriggerProfilesKeepTheMaintenancePathReachable` | 见 §3.4；`goal < trigger`，且恰有一个 profile 声明生产代表性 |
| `ConditionSpec.Validate` 扩展 | 非 baseline 条件必须至少打开一个开关；unregistered profile id 被拒绝而非默认 |

---

## 3. C-2：无凭证守卫与装置修正

### 3.1 无凭证守卫

`TestEveryConditionReachesTheMembersOwnAgent`（上一轮落地）把五个条件逐一走**生产成员构造器**，
断言每个开关落到成员**自己的** agent 上。本轮实测 **5/5 PASS**，无需凭证。

### 3.2 live pilot 的装置

`TestLiveConditionMatrixPilot`（新增）走的是**生产成员构造器本身**
（`newMemberBackendBuilder` → `memberBackendOptions` → `boot.Build` → 绑定 session 文件 →
取写租约 → 启动 usage publisher），不是手搓的子集。这样成员记录才真的存在，
「边界记录 ↔ 成员记录」的对应性才有对象。

任务：240KB 固定 brief + 4 轮单字回答；串行，一次一个在途请求。

### 3.3 三个装置缺陷（本轮发现并修正，全部发生在取证前）

| # | 缺陷 | 后果 | 修正 |
|---|---|---|---|
| 1 | 五个臂共用同一份 brief，**前缀无 run nonce** | 后一个臂的首请求命中前一个臂的缓存（实测 seq1 `hit=2944`）——矩阵在比**自己造出来的**冷暖，而不是它注册的构建 | `conditionBrief(kb, runID)` 注入 run nonce，与 `cachelab.Fixture` 同一条规则 |
| 2 | journal 的 guard 里含 run nonce | nonce 本身是 `run_id` 字段的合法内容 → **guard 拒绝每一行**，journal 为空却看起来像「干净的一轮」 | guard 只保留 prompt 自身的字样；并新增**耐久性断言**：journal 行数必须等于已发布样本数 |
| 3 | 臂的 warm 门槛写成 turn 数 | 单 turn 可合法产生多个请求（实测 a_only 一臂 8 个），门槛因此记错 | 复用注册臂 `B1-baseline-repeat` 的自身门槛（按**有效 warm 样本**计） |

**这三条都不是 A/B 的代码缺陷**，是**装置**缺陷。第 2 条尤其值得记录：
它会让一轮**失败**看起来像**成功**——空 journal 不报错。所有取证在修正后重跑，
修正前的 7 轮已移入 `superseded/`，**不进入任何结论**。

### 3.4 profile 的算术：为什么 `compact_ratio=0.05` 单独不可用

headroom goal 是 `recentTailBudget()` = **窗口的 16%**（受 `visible_window_tokens` 封顶）。
触发线是 `窗口 × compact_ratio`。因此：

```text
ratio = 0.05  →  trigger = 50,000 ; goal = 160,000   ← goal > trigger
```

**goal 高于 trigger 时，任何 fold 都不可能达标**——`settleMaintenanceFold` 必然走 latch 分支，
于是每个臂都报 `low_yield` + `stuck=true`。那是 **profile 的算术**，会被读成维护行为的结论。

协调裁定 §2.3 已同时指定 `visible_window_tokens=16000`，本轮的实现把这个**成对关系**做成了
注册对象的一部分（`TriggerProfile.VisibleWindowTokens`），并加了守卫断言 `goal < trigger`。
实测：`fold_trigger=50000 / headroom_goal=16000`，goal 已落在 trigger 之内。

**这是本轮对「压力触发结论不得外推」这条纪律最具体的一次落实**：不是靠读者记得，
而是让 profile 无法构成一个必然 latch 的配置。

---

## 4. C-2：五臂 live 执行（功能触发 profile）

### 4.1 逐臂配置消费（生产成员 agent 上实读）

五个臂**逐字相同**的边界，且与 profile 注册值一致：

```text
member agent: fold_trigger=50000 hard_ceiling=999744 headroom_goal=16000
```

`fold_trigger = 50,000` 即 `1,000,000 × 0.05`，`headroom_goal = 16,000` 即 profile 的 cap。
**配置确实落到了成员自己的 agent 上**，不是只落在 config 对象里。

### 4.2 维护路径首次真实触发

| 臂 | summaries | installs | rescues | repeat_blocks | state |
|---|---:|---:|---:|---:|---|
| baseline | 1 | 1 | 0 | 0 | `recovered` |
| a_only | 1 | 1 | 0 | 0 | `recovered` |
| b_only | 1 | 1 | 0 | 0 | `recovered` |
| a_plus_b | 1 | 1 | 0 | 0 | `recovered` |
| rescue_enabled | 1 | 1 | 0 | 0 | `recovered` |

**`state=recovered` 而非 `low_yield`**：fold 装上了投影，且装完之后的 headroom **达到了 goal**。
这正是 §3.4 修好之后才可能出现的读数——修之前它必然只能是 `low_yield`。
`repeat_blocks=0` 说明 latch 没有触发，与 `recovered` 一致。

**本轮之前，这条路径在真实 Provider 上一次都没有跑过**（上一轮全部未触发）。
现在它跑了，并且**五个臂都能跑到**。

### 4.3 边界记录 ↔ 成员记录逐条对应

| 臂 | 边界样本 | 成员记录 | 首请求 prompt | 首请求 hit |
|---|---:|---:|---:|---:|
| baseline | 5 | 5 | 53803 | 2944 |
| a_only | 8 | 8 | 53804 | 2944 |
| b_only | 5 | 5 | 53806 | 2944 |
| a_plus_b | 5 | 5 | 53807 | 2944 |
| rescue_enabled | 5 | 5 | 53806 | 2944 |

逐样本核对 `prompt/hit/miss` **全部相等**（§9 的复算命令）。两套记录来自**不同代码路径**
（loopback recorder 的 journal vs 成员 writer 的 owner 日志），能逐条对上，
说明 usage 从 Provider → 适配层 → 事件 → 记录这一整条链没有丢帧或错配。

`a_only` 的 8 个样本集中在 turn 2：该 turn 产生了 4 个请求（中间三次 `quality_fail`），
属**任务本身的模型行为**（固定 brief 下模型未按要求只答单词），不是装置故障——
记为「该轮任务未通过」，不影响 usage 归因，但它说明**4 轮任务对 a_only 臂偏短**。

### 4.4 128 模型在 append 步上的独立复核

上一轮 C 把 `hit == floor(prev_prompt/128)×128` 提升为可逐样本证伪的模型（251/251）。
本轮在**新快照、新样本**上重算（**仅计入 §3.3 修正后的运行**，`superseded/` 的样本不参与）：

```text
append 步（本请求 prompt ≥ 上一请求，即视图是追加）：held = 25, violated = 0
shrink 步（视图被 fold/裁剪重写）：22 步，模型不适用
本轮 hit 样本 58 个，非 128 倍数者 = 0
（含修正前样本共 117 个时，非 128 倍数者同样为 0）
```

**这同时给出两件事**：模型在 append 步上**再次零违反**；以及它的**适用条件**——
上一轮未明确写出的那条：**该模型描述的是追加步**。fold 之后 `prompt` 变小、
前缀被重写，128 关系**本就不该成立**（本轮 22 个 shrink 步全部违反，且方向一致）。

**这不是模型被推翻，是模型的定义域被写清楚了。** 任何后续引用必须带上这条限定。

### 4.5 维护诊断投影端到端验证（协调裁定 §2.4）

裁定 §2.4 指定 C 实现「把 receipt 的三个诊断值投影到成员记录」。
实现后在生产路径上实测（`projection-check/`，interface note 见
`TEAM_MEMBER_CACHE_NEXT_PART_C_INTERFACE_CHANGE.zh-CN.md`）：

```text
member agent: maintenance spend summaries=1 installs=1 state="recovered"
member record: seq=1 maintenance=false/ headroom=0    reduction=0.000   ← 决策之前：未观测
member record: seq=3 maintenance=true/recovered headroom=46748 reduction=0.940  ← 决策之后：带上
member record: seq=4 maintenance=true/recovered headroom=46748 reduction=0.940
member record: seq=5 maintenance=true/recovered headroom=46748 reduction=0.940
```

**三件事同时被证明**：投影**到达了生产路径**（不是只在单测夹具里）；
归属是**「本请求之前的最近一次决策」**（决策前的记录未观测，之后的带上）；
`headroom=46748 ≥ goal 16000` 与 `state=recovered` **自洽**——
这是 §3.4 修好之后才可能出现的读数组合（修之前 goal > trigger，必然 `low_yield`）。

live pilot 里加了断言：**该臂付过 summary 时，必须至少有一条成员记录带上那个决策**，
否则失败——这条守卫把「投影接好了」从一次性核对变成每跑必查。

### 4.6 逐样本的 oracle 一致

本轮 **58 个 hit 样本中的每一个**都携带网关自己的 `billing_usage`，且 `UsageOracleAgrees=true`
（`oracle present=58 / agrees=58`，逐样本比对见 §10）。这是旁证而非证明——
它只能说明**两条独立读数在每一行上都一致**。

---

## 5. C-3/C-4：样本量与正式矩阵

### 5.1 预注册门槛（冻结，未改）

| 项 | 值 |
|---|---|
| 每臂有效 warm 下限 | `FormalWarmMin = 20` |
| 每臂目标 | `FormalWarmTarget = 30` |
| 独立运行次数 | 3 |
| 最小有意义差异 | `MinEffectPP = 5.0pp` |
| 单次运行成本上限 | `CostCapUSD = 25.0` |

**门槛在结果之前冻结**：本轮没有、也不允许事后调整。

### 5.2 实际样本量

| 臂 | 本轮独立运行数 | 每臂有效 warm | 达到 30？ |
|---|---:|---:|---|
| baseline | 4 | 4 | ❌ |
| a_only | 4 | 4（一次运行 7，见 §4.3） | ❌ |
| b_only | 1 | 4 | ❌ |
| a_plus_b | 1 | 4 | ❌ |
| rescue_enabled | 1 | 4 | ❌ |

`baseline` 与 `a_only` 各跑了 4 次（用于复现稳定性），其余三臂各 1 次。
**每臂有效 warm 都是 4，距离门槛 30 约 7 倍**；独立运行次数也未达 3 次的要求（三臂各 1 次）。 因此本轮**不具备**任何臂间比较的统计功效，
`Compare()` 会如实返回 `inconclusive` 并列出未达标原因——本轮**不给出**任何臂间效果判定。

### 5.3 正式矩阵未执行

两条独立原因，任一条都足以停止：

1. **无预算批准**：协调裁定 §3 第 5 步要求 live pilot 由协调者**单独书面批准**。
   `experiment-contract.yaml` 已冻结、配置消费断言已通过、隔离与回滚**已修复并实测**（§12.1）——
   但「预算与凭证范围」的批准**尚未取得**，因此不发起正式矩阵。
2. **功效不足**：即使获批，每臂 4 个有效 warm 也远不足以支撑 5pp 的最小差异。

### 5.4 一个必须记录的观察（不是结论）

11 次修正后运行的首请求 hit 稳定在 **2816 或 2944**（10/11 为 2944），
即约 5.2–5.5% 的 prompt 命中。**首请求本应是冷启动**（`hit=0`），
唯一的例外是 §3.3 第 1 条那个被修正前的运行（`hit=0`，因为它的前缀确实是新的）。

**该现象在本轮后段被部分解释。** 13:28 的一次同条件重跑（`projection-check/`，
同一构建、同一 arm、同一 brief 构造，距前批约 2 小时）读到 **`hit=0`**：

```text
condition=baseline trigger_profile=pressure   fold_trigger=50000
member record: seq=1 prompt=53805 hit=0 miss=53805      ← 真正的冷启动
member record: seq=3 prompt=4158 hit=3072 miss=1086 maintenance=true/recovered
```

**这一次的首请求是完全冷的。** 于是前批稳定的 2944 **不是**「本臂首请求的固有命中」——
它是**可以被清掉**的东西。最简解释是网关侧对稳定前缀的缓存**有存活期**，两小时后失效；
但本轮**只有两个时间点**，不足以把「存活期」与「其他会随时间失效的机制」分开。

**仍然不作归因**，但读法要收紧一句：**任何把「首请求 hit≈2944」当作该臂冷启动基线的算法都是错的**——
它把一个会过期的数当成了常量。正式矩阵要求每臂首请求是真正的冷启动（`hit=0`），
而**本轮的一个臂已经出现过两次不同的首请求读数**，这本身就是该要求必须被断言、而不是被假设的理由。

---

## 6. C-5/C-6/C-8/C-9：指标与分层

### 6.1 A 因子的两类结果（§C.2.5）

| 类 | 本轮状态 |
|---|---|
| **受控触发子集的条件效果** | 路径已真实触发（§4.2），但**样本量不足**，不给条件效果 |
| **自然任务分布下的维护发生率** | **未观测**：生产阈值 80 万 token，本轮最大 prompt 5.4 万，差约 15 倍 |

两者**不得合并成一个平均值**——本轮如实分开：一个「已触发但无功效」，一个「未观测」。

### 6.2 质量与完成率：仍不可测（承接 B 的接口请求）

| 指标 | 状态 |
|---|---|
| usage 覆盖率 | **可测**：117/117 样本带可解析 split，0 个 `no_cache_split` |
| miss tokens / hit rate / 总 input | **可测**（边界与成员两处） |
| p50/p90 延迟 | **可测**（recorder 侧，逐样本 `latency_ms`，见 §6.3） |
| 工具调用正确率 | **不可测**：本 pilot 无工具面 |
| 任务完成率、必要上下文保留 | **不可测**：成员记录无 task result；`Controller.RunTurn` 返回 error 而非答案 |

后两项与上一轮结论一致，**仍挡着 §C.4 的质量准入条件**。B 的数据字典已给出落位提案
（N-1..N-5），**本轮不实现**——按协调裁定 §2.4 与计划 §B.3，生产观测字段由协调者指定唯一实现者。

### 6.3 延迟（§C.2.6 要求分层报告 p50/p90 与样本数）

```text
recorder 侧逐样本 latency_ms（本轮全部 58 个样本，按臂）
  baseline        n=20  p50=2084  p90=5833  max=7151
  a_only          n=23  p50=2188  p90=4865  max=6920
  b_only          n= 5  p50= 912  p90=2857  max=5896
  a_plus_b        n= 5  p50=1541  p90=2988  max=8020
  rescue_enabled  n= 5  p50=1132  p90=3182  max=6105
```

**读法**：这是 **provider 请求延迟**（recorder 起止），**不是** turn 墙钟，也不含排队/重试
（本轮零重试）。`b_only` / `a_plus_b` / `rescue_enabled` 的 p50 偏低是因为它们各只有 1 次运行，
且该运行的请求比 `baseline` 小——**样本数写在旁边正是为了不让读者把它读成臂间差异**。
按 B 的质量/延迟规范 §2.3，本轮的延迟只能作**描述性**读数：分层维度（账号/网关/并发）都不足。

### 6.4 异常样本表（§C.4 要求）

| 臂 | seq | turn | 分类 | 说明 |
|---|---:|---:|---|---|
| `a_only` | 3 | 2 | `quality_fail` | 模型未按要求只答单词（该 turn 产生 4 个请求） |
| `a_only` | 4 | 2 | `quality_fail` | 同上 |
| `a_only` | 5 | 2 | `quality_fail` | 同上 |

**异常样本共 3 个，全部在 `a_only` 的同一 turn**，且都是**任务质量**而非装置或计量问题
（`usage_split=true`、`error` 为空）。其余 55 个样本 `quality_pass`。
**零** `error`、**零** `no_cache_split`、**零** `usage_missing`、**零** confound。

### 6.5 本轮唯一可报告的主指标读数

```text
全部修正后运行（含复现运行）的 warm 段 token 加权命中率：
  baseline       4 次运行：20.03% / 20.72% / 19.81% / 20.78%
  a_only         4 次运行：37.04%* / 20.08% / 20.74% / 20.17%   * 该次 7 个 warm，见 §4.3
  b_only         1 次运行：20.00%
  a_plus_b       1 次运行：20.34%
  rescue_enabled 1 次运行：20.10%
```

**`baseline` 与 `a_only` 各 4 次运行的离散度是本节最有用的读数**：
四次 `baseline` 落在 19.81–20.78%（极差 0.97pp），四次 `a_only`（除那次 7 样本的运行）
落在 20.08–20.74%（极差 0.66pp）。**臂内离散度与臂间差异同量级**——
这正是「4 个样本不足以支撑 5pp 判定」的直接证据。

**这些数字不构成任何臂间结论**：每臂 4 个样本，且 §5.4 的待查项（首请求命中）说明
各臂的起点并不完全相同。它们的用途是**证明指标可测**，不是证明效果。

### 6.6 成本（§C.4 要求）

**本轮成本记为 `unknown`，不是零。** 逐样本的 token 数齐全（hit/miss/completion 三向都有），
但**没有登记每百万 token 的价格**：`experimentPrices()` 从 `REASONIX_LIVE_CACHE_PRICE_*` 读取，
本轮未设，因此 `Prices.Registered()` 为 false，`RenderReport` 输出 `cost: unknown`。

**不得用事后查到的价目表回填**——那会让一个当时未知的数看起来像当时测到的。
下一轮若要成本入表，须在运行**之前**把三个价格写进契约。

### 6.7 Rescue：未验证

五臂 `rescues=0`，`rescue_enabled` 也没有进入 rescue 分支——**它本来就不该**：
rescue 是「fold 无法恢复窗口」的末端路径，而本 pilot 的 fold 每次都 `recovered`。
按计划 §C.2.8 的字面要求：

> 若没有触发，结论是「**未验证**」，不能以模拟成功替代真实触发证据。

**记为「未验证」。** 构造真实 rescue 需要越 hard ceiling 的视图（100 万 token 级），
本轮预算不支持。

### 6.8 分层与敏感性：单层，不外推

| 维度 | 本轮覆盖 | 结论 |
|---|---|---|
| 账号作用域 | **1**（单 pool 条目） | 不作跨账号结论 |
| 网关/route | **1**（`anthropic/<hash>`，逐臂不同 hash 是**端点指纹**不是账号） | 不作跨网关结论 |
| prompt 桶 | **2**（`lt_32k` 与 `32k_128k`） | 层间样本均不足 30 |
| 并发度 | **1**（串行，注册值非测量值） | 未测 |
| cold/warm | **分开统计**（`first_request` vs `warm`） | ✅ |
| 128 归因规则 | 4 条规则并列（B 的工具） | 主口径固定为生产在用的 `{128, absorbed}` |

**全部结论限于：单账号、单网关、串行、`pressure` profile、功能触发。**
不得外推为生产频率、生产收益或跨环境模型。

---

## 7. C-7：安全停止

| 停止规则 | 本轮 |
|---|---|
| 连续服务错误 ≥ 3 | **未触发**（0 错误） |
| 连续 usage 缺失 ≥ 3 | **未触发**（0 缺失） |
| 工具面漂移 | **未触发**（confounds 为空） |
| recorder 端点改变推导协议 | **未触发**（`TestExperimentRecorderEndpointKeepsTheClientProtocol` 实测同协议） |
| 成本上限 | **无法评估**：未登记价格，成本记为 `unknown`（§6.6）；本轮请求量小，但「小」不是「在预算内」的证据 |
| hard ceiling / 上下文丢失 / 成员串扰 / 延迟越界 | **未触发** |

**零超限、零跨成员污染、零费用异常。** 所有 live 请求使用隔离成员与隔离 state root，
不访问任何真实用户会话。

---

## 8. C-10：最终准入

### 8.1 §C.4 六项准入条件逐条

| # | 条件 | 状态 | 依据 |
|---|---|---|---|
| 1 | 条件配方真实隔离，A 因子覆盖全部行为变化，目标路径在子集中确实触发 | **✅ 隔离与触发** | §2、§4.2；A 行为面 = 一个开关，已机器钉住 |
| 2 | 样本量满足预注册功效，方向不由单一账号/网关支配 | **❌** | §5.2：每臂 4，门槛 30；且只有单账号单网关 |
| 3 | 成本或缓存主指标达到最小有意义改善 | **❌** | 无功效，不产生效果判定 |
| 4 | 质量、必要上下文、工具调用、安全不劣于预设界限 | **❌ 不可测** | §6.2；字段不存在 |
| 5 | 延迟、错误、重试、unknown 与成员隔离满足护栏 | **⚠️ 部分** | 延迟/错误/unknown 可测且达标（§6.3、§6.4）；**质量维度不可测** |
| 6 | 跨环境结论只限已测环境，不依赖未验证的 Provider 常量 | **✅** | §6.8；128 已限定为「append 步、样本集内」 |

**六项中三项不满足或不可测 → 不满足进入灰度的条件。**

### 8.2 决策

# `NO-GO / CONTINUE-SAMPLING`

与上一轮结论一致，但**阻塞的性质变了**：

| | 上一轮 | 本轮 |
|---|---|---|
| 矩阵是否无混杂 | ❌ 四个条件没有一个能隔离 A 或 B | ✅ 已隔离，且有守卫 |
| 维护路径是否触发过 | ❌ 从未 | ✅ 五臂各触发 1 次 |
| 配置是否落到成员 agent | ⚠️ 只有静态核对 | ✅ 生产构造器上实读 |
| 主要阻塞 | 装置与隔离 | **样本量、跨环境、质量字段** |

**不扩大灰度，不宣称缓存收益。** 但本轮把「路径不可达」这个阻塞**移除了**——
剩下的阻塞是**采样规模**与**观测面**，不是机制。

---

## 9. §C.4 交付清单逐项落点

计划 §C.4 列了八项交付。逐项落点如下——**缺项如实列出，不代以「已完成」**：

| §C.4 要求 | 落点 | 状态 |
|---|---|---|
| 预注册实验契约 | §2.3、`experiment-contract.yaml`（哈希 `1de71fbc13b41ff4`） | ✅ |
| pilot/正式样本分流 | §5；pilot 已跑，正式**未执行** | ⚠️ 分流规则已定，正式缺 |
| 样本量依据 | §5.1 冻结门槛 + §6.5 实测离散度（臂内极差 0.66–0.97pp vs 门槛 5pp） | ✅ |
| 匿名原始数据位置/hash | §1 表 + `MANIFEST.sha256`（`sha256sum -c` 44/44 通过） | ✅ |
| 所有臂的配置与触发覆盖 | §4.1（配置）、§4.2（触发） | ✅ |
| 异常样本表 | §6.4（3 个 `quality_fail`，其余 55 通过） | ✅ |
| 质量/成本/延迟/安全分析 | 延迟 §6.3 ✅、安全 §7 ✅、**成本 §6.6 记为 `unknown`**、**质量 §6.2 不可测** | ⚠️ 两项缺 |
| 结论和回滚建议 | §8、§12 | ✅ |

**八项中六项完整、两项部分**（成本未登记价格、质量字段不存在）。
两项部分**都是「数据面缺字段」而非「本轮没做」**，修复路径见 §13 第 3 条。

## 10. 复算命令

```bash
# 契约（内容哈希 1de71fbc13b41ff4）
go test ./internal/cachelab/ -run TestRenderContractForArtifact -v   # 见 contract.go 的 DraftContract
python3 -c "import yaml;print(yaml.safe_load(open('/tmp/experiment-contract.yaml'))['arms'])"

# 矩阵守卫（无需凭证）
go test ./internal/cli/ -run TestEveryConditionReachesTheMembersOwnAgent -v -count=1

# 一个臂（真实请求）
REASONIX_LIVE_CACHE_CONDITION=baseline REASONIX_LIVE_CACHE_TRIGGER=pressure \
  REASONIX_LIVE_CACHE_ARCHIVE=/home/zwc/reasonix-partc-archive \
  REASONIX_LIVE_CACHE_CLIENT_BUILD=f53c4bf91 \
  go test -tags live ./internal/cli/ -run TestLiveConditionMatrixPilot -v -count=1

# 证据完整性
cd /home/zwc/reasonix-partc-archive/condition-matrix && sha256sum -c MANIFEST.sha256

# 边界 ↔ 成员逐条对应、128 模型（append 步）
cd /home/zwc/reasonix-partc-archive/condition-matrix && python3 - <<'PY'
import json,glob,os
for f in sorted(glob.glob('condition-boundary-*.jsonl'), key=os.path.getmtime):
    run=f[len('condition-boundary-'):-len('.jsonl')]
    b=dict(l.split(': ',1) for l in open('condition-banner-%s.txt'%run).read().splitlines() if ': ' in l)
    bd=[json.loads(l) for l in open(f)]
    mr=[json.loads(l) for l in open('condition-members-%s.jsonl'%run)]
    assert len(bd)==len(mr), (b['condition'], len(bd), len(mr))
    for x,y in zip(bd,mr):
        assert (x['prompt_tokens'],x['cache_hit_tokens'],x['cache_miss_tokens']) == \
               (y['prompt_tokens'],y['cache_hit_tokens'],y['cache_miss_tokens']), (b['condition'],x['seq'])
    print('%-15s boundary==members on all %d samples' % (b['condition'], len(bd)))
PY
```

---

## 11. 失败 / 跳过 / unknown 清单

| 项 | 状态 | 说明 |
|---|---|---|
| 装置缺陷（brief 无 nonce、guard 自锁、warm 门槛） | **已修正并重跑** | §3.3；修正前 7 轮移入 `superseded/`，不进入结论 |
| `TestTeamTurnInjectsInboxAtSubmit` flake | **既有**（非本轮） | 上一轮已认定 |
| **正式矩阵** | **未执行** | §5.3：无预算批准 + 功效不足 |
| **rescue 真实触发** | **未验证** | §6.7 |
| **自然分布下的维护发生率** | **未观测** | §6.1 |
| **质量 / 完成率 / 工具调用** | **不可测** | §6.2 |
| **跨账号 / 跨网关 / 并发 / 1M 桶** | **未覆盖** | §6.8 |
| 首请求 `hit≈2944` 的来源 | **部分解释，未归因** | §5.4：同条件重跑读到 `hit=0`，说明它是**可失效**的量；不足以定机制 |
| `desktop/` 模块 | **未跑** | 其 host-contract 测试在 HEAD 上即红（既有） |
| `golangci-lint` | **skipped** | 本机未装；CI pin 2.12.2 |

---

## 12. 回滚

### 12.1 本轮修复：配置回滚此前**不持久**（重要）

**发现的缺陷**：五个缓存相关键（`low_yield_latch`、`message_shape_diagnosis`、
`cache_aware_compaction`、`context_rescue`、`visible_window_tokens`）**都不进渲染器**。
渲染器是配置保存时重写文件的依据，因此：

```text
[agent]
low_yield_latch = false          ← 操作员写下回滚
$ reasonix config currency USD   ← 任意一次配置编辑
   → 文件被重写，键消失 → 解码为 nil → 默认**开启**
```

**实测**（`LoadForEdit → 无关编辑 → SaveTo → 重新加载`）：五个键**全部丢失**。
其中三个（`cache_aware_compaction`/`context_rescue`/`visible_window_tokens`）**早于本轮改动**。
触发命令很常见：`config currency`、`config telemetry`、`/effort`、`reasoning_language`、桌面端保存设置。

**这是上一轮的一处空验证**：`FINAL_DECISION` §2.2 写的「实测确认：render 后再 reload，
值仍为默认开，不会静默丢失」——它测的是「不设置任何值 → render → reload 仍是默认开」，
**不可能失败**，因此没有测到真正的风险（显式设为 `false` 后能否存活）。
所引先例 `cold_resume_prune` 也已退休（`load.go:689` 每次加载显式置 nil），本无回滚语义。

**修复**：新增 `internal/config/render_agent_cache.go`，把五个键接进**两个**渲染路径
（完整/用户 scope 与 project delta）。规则：

- 指针键（`low_yield_latch`/`message_shape_diagnosis`）**设置即写出**——`false` 就是回滚，
  丢掉它与「未设置」不可区分；
- 值键在其值非零时写出（零值即其文档默认）。

**未设置时一个键都不写**，既有配置的渲染输出不变。

**守卫**（`render_agent_cache_test.go`）：五个键各自「经历一次配置编辑后仍在」；
未设置的键在四个渲染 scope 中**都不出现**；五个键在三个 scope 中往返一致。
**变异验证**：把两个渲染钩子摘掉，前三条守卫立即失败——它们不是空断言。

| 变更 | 配置回滚 | 代码回滚 |
|---|---|---|
| 条件注册表（`cache_aware_compaction` 固定） | 无需（纯注册数据） | `plan.go` 的 `switches()` |
| `TriggerProfile` | 无需（纯注册数据） | `plan.go` 的 `RegisteredTriggerProfiles()` |
| 契约渲染 | 无需（只读渲染） | 删 `contract.go` |
| 配置回滚修复（§12.1） | 无需（它**就是**回滚路径） | 删 `render_agent_cache.go` 与 `render.go` 的两个钩子 |
| live pilot | 无需（`-tags live` + 显式 env） | 删 `live_team_cache_condition_test.go` |
| 新增守卫 | 无需 | 删 `condition_test.go` 的对应测试 |

**本轮零生产行为变更**：未改任何默认值、未改 prompt 字节、未改工具 schema、
未改统计分母、未新增持久化状态。**唯一的生产代码修复**是 §12.1 的回滚持久化——
它不改变任何默认值，只让操作员**已经写下**的显式值在保存后存活。`internal/agent/**` 与 `internal/team/**` 生产观测**未被触碰**。

---

## 13. 给下一轮的派单建议

本轮把「路径不可达」移除了。要让下一次采样**有效**，需要按依赖顺序解决三件事：

1. **预算与凭证范围批准**（协调者）：这是 §5.3 的第 1 条，也是唯一的形式阻塞。
   没有它，本轮之后的所有 live 工作都无法开始。
2. **把每臂有效 warm 从 4 提到 30**：`pressure` profile 下每轮约 2 个有效 warm，
   30 个意味着每臂约 15 轮任务；五臂 × 3 次运行 = 225 轮。**这是可算的**，
   按本轮实测每轮约 1–3 秒 provider 延迟，成本与时长都在 `CostCapUSD` 之内。
   建议同时把 `a_only` 的任务从 4 轮延长（§4.3 的 `quality_fail` 说明 4 轮对它偏短）。
3. **质量与延迟字段**（需协调者指定唯一实现者）：§6.2 的两项不可测**直接挡着** §C.4 的第 4 条，
   而它是**结构性**的——不是采样量能解决的。B 的 N-1..N-5 提案已就绪。

**在这三件之前，不得**以任何命中率、`state=recovered`、或「矩阵跑通」为由放行行为优化。
