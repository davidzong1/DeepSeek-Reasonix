# Part B 下一轮：质量评估规范与延迟测量定义

> 状态：**规范与定义已冻结，生产代码未改动**。日期：2026-09-25。
> 依据：`TEAM_MEMBER_CACHE_NEXT_ROUND_3AGENT_PLAN.zh-CN.md` §Part B B.2 第 2、3 项、§B.4 通过条件。
> 配套：`TEAM_MEMBER_CACHE_NEXT_PART_B_DATA_DICTIONARY.zh-CN.md`（字段落位与 schema 提案）、
> `TEAM_MEMBER_CACHE_NEXT_PART_B_RESULT.zh-CN.md`（核验表）。
> 本文不采集、不要求采集任何 prompt 正文、工具参数或答案文本。

## 1. 质量评估规范（§B.2 第 2 项）

### 1.1 为什么要写成 rubrics 而不是「看一眼答案」

现有链路里唯一的质量字段是 `cachelab.Sample.quality_check`（`internal/cachelab/sample.go:85`），
取值是**闭集三值**：`quality_pass` / `quality_fail` / `quality_unchecked`
（`sample.go` 的 `QualityPass/Fail/Unchecked`；`Validate()` 强制每条样本必须有值）。
也就是说：链路已经能承载「机械判定」的结论，但**判定标准**还没定义。规范补齐的是标准，
而不是字段。

### 1.2 每个固定任务预先冻结的 rubric（四类断言）

一条 rubric 由四组可机械检查的断言构成，全部针对**结构**而不是措辞：

| 组 | 断言形式 | 例子（示意，实际清单随任务冻结） |
|---|---|---|
| 必需事实 | 答案中必须出现受控标记或必需条目集合 | 任务里要求的精确 marker、必须被列举的条目名 |
| 约束 | 输出必须满足的边界 | 不得修改冻结文件、不得新增依赖、命令必须非交互 |
| 工具目标 | 必须发生/不得发生的工具调用 | 指定目标被读取、指定路径被写入、指定命令被执行 |
| 完成判据 | 回合以合法终态收尾 | 终态为完成（非 `protocol_failed`/`interrupted`）且存在可见正文 |

判据的**真值来源**只允许两类：被测产物（文件、工具调用记录、终态）与任务自带的受控标记。
禁止把「模型自述做了」当作判据。

### 1.3 自动断言与盲评必须分开（两层，不得互相替代）

- **A 层（自动断言）**：机械可判，产出 `quality_pass` / `quality_fail`，可复算、可进 CI。
  §1.2 的四组断言全部在 A 层。
- **B 层（盲评）**：只用 A 层判不了的维度（例如「必要上下文是否被保留」「解释是否与产物一致」）。
  评分者**不知道条件臂**；样本以打乱顺序呈现；两名评分者独立打分，分歧走 §1.5。

两层**分别报告、禁止合并成一个平均分**：把不可机械判定的部分混进机械分数，
会让「条件臂差异」与「评分者差异」无法区分。

### 1.4 允许保存的内容（白名单）

只保存：任务 ID、rubric 版本与摘要、每条断言的通过/失败与**失败类型**（枚举，如
`missing_marker` / `extra_file_touched` / `wrong_tool_target` / `no_visible_answer` /
`illegal_terminal_state`）、盲评分数与评分者代号、以及**受控 artifact 引用**
（相对路径或内容摘要，不落正文）。

禁止保存：答案正文、prompt 正文、工具参数原文、任何用户内容、任何可还原用户内容的摘要。
产物引用的路径若含用户名/主机名，按仓库既有约定脱敏（参见 `internal/capdiag` 的路径脱敏）。

### 1.5 无法评分、分歧与安全失败

| 情形 | 处理 | 不得做的事 |
|---|---|---|
| 无法评分（产物缺失、标记不存在、任务被中断） | 记 `quality_unchecked` + 原因枚举，**保留在分母外但记数** | 不得记为 fail（会把「没测到」变成「测到失败」） |
| 评分者分歧 | 记分歧本身（两分 + `disagreement` 标记），交第三人；分歧率随 n 报告 | 不得取平均掩盖分歧 |
| 安全失败（越权写入、危险命令、跨成员/跨会话串扰） | **立即停臂**，保留脱敏证据，按 §C.2 第 7 项处理 | 不得仅记为一次 `quality_fail` 后继续采样 |
| 成本/延迟越界 | 停臂并保留证据 | 不得「跑完再说」 |

判定顺序固定为：**安全失败 → 完成判据 → 自动断言 → 盲评**。前一层失败不再进入后一层
（避免用一个更高层的分数掩盖更低层的硬失败）。

## 2. 延迟定义（§B.2 第 3 项）

### 2.1 四条可测的腿，各自独立

| 腿 | 起点事件 | 终点事件 | 时钟与单位 | 现状 |
|---|---|---|---|---|
| **L1 provider 请求延迟** | 请求被 recorder 保留（`internal/cachelab/recorder.go:270`，`pending.started = now`） | 响应体捕获完成（`recorder.go:341`，`finishSample` 内 `time.Since(pending.started).Milliseconds()`） | 进程内 `time.Now()`，毫秒 | ✅ **仅夹具**（`Sample.LatencyMS`） |
| **L2 单 turn 墙钟** | turn 开始（`internal/turnevent/ledger.go:314/353`，`turnStarted = time.Now().UnixMilli()`） | 终态记录落盘（`TerminalSummary.finishedAt`） | 同进程 `time.Now()`，unix 毫秒；`durationMs = finishedAt − startedAt`（`ledger.go:79-81`、`transcript_ref.go:43-49`） | ✅ 已落盘，但**与缓存记录未连接** |
| **L3 排队/重试时间** | — | — | — | ❌ **不可分离**，见 §2.3 |
| **L4 工具调用时间** | `event.Event.startedAt`（`internal/event/event.go:258`） | `endedAt`(`:259`)，`durationMs`(`:255`) | 进程内 `time.Now()`，unix 毫秒 | ✅ 生产事件已有（仅 ToolResult，未运行时为 0） |

### 2.2 三条必须写进报告的定义

1. **L1 不是 TTFT。** 它在**响应体读完之后**取值，因此对 SSE 流而言是「请求开始 → 流结束」，
   即总请求时长。全仓不存在首 token 时间（`grep -rni 'ttft\|first_token\|first token'` 在
   `internal/cachelab`、`internal/event`、`internal/agent/run_usage.go` 零命中）。
   报告里写 `provider_request_ms`，**不得**写成 `time_to_first_token`。
2. **被客户端中断的流也有 L1。** `captureBody.done` 在拷贝结束时触发（`recorder.go:283-288`），
   出错路径同样调用 `finishSample`（`recorder.go:322` 的 `failRequest`），所以中断样本会得到一个
   **偏短**的 L1。这类样本 `status`/`error` 非空，按 `Sample.Classify()` 归入 `error`，
   必须**单独报告**，不得混进 warm 的 p50/p90。
3. **L2 与 L1 不可相减得到「客户端开销」。** 一个 turn 内可以有多个 provider 请求（重试、
   guardian、compaction）与多个工具调用，L2 ≥ Σ L1 只在单请求单工具的退化情形成立。

### 2.3 L3（排队/重试）为什么标「不可分离」

- 生产记录只有聚合 `request_count` + `request_count_source`（`cacherequest.go:112`），
  **没有逐 attempt 时间戳**，因此「重试占了多久」在生产路径上不可得。
- 夹具侧有 `attempt`（`sample.go:47`）与 `gap_ms`（`:51`），但 `gap_ms` 是**同一 arm 内两条请求之间的
  墙钟**，它包含模型思考、工具执行与客户端处理——把它当排队时间会把三者算成排队。

因此本轮定义：**L3 报 `n/a`（未测量），不报 0**。若 C 需要它，前置是 N-1（逐 attempt 时间戳落点）。

### 2.4 分层与样本数（§B.2 第 3 项的报告要求）

- 分层维度：**臂 × model × route_bucket**，最低还要给 `cold/warm`、`error/retry`、`unknown` 的
  独立行。**禁止**用一个跨维度的 p50 覆盖差异。
- 每条百分位必须带 `n`；`n` 低于该臂冻结门槛时标 `insufficient_sample`（沿用
  `CacheGroupStat.SampleGateReached` 的既有措辞）。
- 分母必须与 §3.4 的有效样本定义一致：只有 `eligible`（`Sample.Eligible()`）样本进主表；
  其余按 `Classify()` 的分类**逐类计数并报告**。

### 2.5 现有实现的一个已核验偏差（建议，未改动）

`internal/cachelab/stats.go` 在**资格判定之前**就把延迟收进分布：

```text
stats.go:100  latencies = append(latencies, s.LatencyMS)
stats.go:101  if !s.Eligible() { continue }
stats.go:131  stats.LatencyP50MS = quantileInt(latencies, 0.50)
stats.go:132  stats.LatencyP95MS = quantileInt(latencies, 0.95)
```

后果：`ArmStats.LatencyP50MS/P95MS` 是**该臂全部样本**（含冷启、错误、重试、usage 缺失）的分布，
与同表的加权命中率（只统计 eligible）**分母不同**；报告里两者并排会被读成同一总体。
同时它没有 model/route 分层。

**处置**：`internal/cachelab/**` 属 C 的写集（§B.3），B 不并行修改。记为对 C 的接口请求：
「延迟分布要么另存 eligible-only 的分位数，要么在报告里命名其分母」。本轮仅登记，未落地。

## 3. 与数据字典的对应关系

| 规范项 | 需要的字段 | 现状 |
|---|---|---|
| A 层自动断言 | 工具调用记录、终态、受控 artifact | ✅ 已有（`event`/`turnevent`） |
| B 层盲评 | 与样本 ID 分离的评分表 | ✅ 只需新增**文档侧**表格，无新字段 |
| L1 | `cachelab.Sample.LatencyMS` | ✅ 夹具；生产缺（提案 N-1） |
| L2 | `TerminalSummary` + `turn_id` | ✅ 两者都在，缺**连接**（提案 N-5，零 schema 变更） |
| L3 | 逐 attempt 时间戳 | ❌ 提案 N-1 |
| L4 | `event.DurationMs/StartedAt/EndedAt` | ✅ 生产已有 |
| 分层 | `arm` / `model_ref` / `route_bucket` / `account_scope` | arm 仅夹具（N-2）、account_scope 缺（N-3） |

## 4. 未决项

| # | 项 | 状态 |
|---|---|---|
| B-Q1 | 每个固定任务的 rubric 清单（§1.2 的四组断言）需随任务集冻结 | 待 C 的任务集定稿后填写 |
| B-Q2 | 盲评评分者与流程（谁、几人、分歧裁决人） | 待协调者指派 |
| B-Q3 | `cachelab` 延迟分母（§2.5）的修正人 | 属 C 写集，待裁定 |
| B-Q4 | L3 是否本轮纳入（需要 N-1 先行） | 默认**不纳入**，报 `n/a` |
