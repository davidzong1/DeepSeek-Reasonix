# Team Member 缓存计量：Part A Provider / route K1 验证

> 执行依据：`TEAM_MEMBER_CACHE_FOLLOWUP_EXECUTION_PLAN.zh-CN.md` §4（Agent A）、§9 派单摘要。
> 前置记录：`TEAM_MEMBER_CACHE_MERGE_RECORD.zh-CN.md` §9（未决清单 1、7）、`TEAM_MEMBER_CACHE_PART_A_PROVIDER_USAGE_MATRIX.zh-CN.md`。
> 日期：2026-09-25。**本文不改 Team 请求构造、缓存策略和统计分母。**

---

## 0. 结论摘要

| # | 结论 | 强度 | 依据 |
|---|---|---|---|
| **F-A1** | 三条 usage 路径**互不共享实现**：`anthropic` 折叠多事件，`openai` 归一化单对象，`responses` 读终局对象。**"OpenAI 路径覆盖 responses" 不成立**——两者词汇表相反（Responses 的 `input_tokens` 是整段，chat 路径的 Anthropic 回退里是未缓存部分）。 | **证据** | §2、§3.3 |
| **F-A2** | **DeepSeek-compatible 本 route：通过。** 真实端点只读复核：3 轮（2 warm），adapter == 事件流逐轮成立，`prompt == hit + miss` 全部闭合，命中不超出端点实际报告的 read。 | **证据** | §4.1 |
| **F-A3** | 该复核**不依赖 `billing_usage`**：判据是 adapter 读数 vs 原始事件流。本轮 3 个 usage 事件恰好都带私有块，但判据在缺失时同样成立。 | **证据** | §4.1 |
| **F-A4** | **约定拆分（A-5 修复）在真实端点上确认生效**：`inclusiveInput()==false`，与端点的 remainder 语义一致。 | **证据** | §4.1 |
| **F-A5** | **native Anthropic / LongCat / 官方 DeepSeek：未决（无凭据）。** 表驱动 fixture 覆盖其形状，但**不升级为端点通过**。 | 未决 | §4.2、§5 |
| **F-A6** | **OpenAI chat 与 responses：本轮已审。** 两条路径各自的形状矩阵已落地；两者都没有 `billing_usage` 概念，也都不需要。 | **证据（代码+fixture）** | §3.1、§3.2 |
| **F-A7** | **无端点观测的形状：全部未验证。** 三条路径的 malformed 行（read > prompt、负值）都**不 clamp**，读数原样穿过，由下游 `accounting_valid` 具名排除。 | **证据** | §3.4 |
| **F-A8** | **未发现误折叠或归一化回归。** 本轮未改任何生产 usage 语义代码，只加测试与一个 live 探针。 | — | §6 |

**一句话**：三条方言的 usage 形状已各自表驱动固定，本 route 的真实端点复核**无 oracle 依赖**地通过；其余 route 因缺凭据保持未决，且**不得**用本 route 的结果概括。

---

## 1. M0 冻结记录

| 项 | 值 |
|---|---|
| 基线 commit | `71d2e3695fddd093c2a06f8a8045486db73b78ff`（`Team-agent-merge-mainv2`） |
| 工作树 | 起始干净（`git status --porcelain` 为空）；本轮只新增文件，未改既有文件 |
| `stream_usage.go` | md5 **`71b7d3b79a4c6dfc74d1357b310190ff`** —— 与合流纪要 M0 冻结值**逐字节相同** |
| `messages_usage.go` | md5 `d4c15868ce765c02aacc033036e6258e` |
| `anthropic.go` | md5 `5f2ddea6cb63d741bb524e4de7361d52`（含 A-5 的 `inclusiveUsage` 拆分） |
| `cachelab/usage.go` | md5 `01a5cb37e30cf272a095fce1a09837f1`（与合流纪要 §9.8 记录的当前值一致；**属 C 的写集，本文不判**） |
| route | `aiapi.lejurobot.com`，池条目 `deepseek-v4-flash-roojin` |
| 线上 model | `deepseek/deepseek-v4.1-flash` |
| 端点属性 | 生产网关，**只读**；本轮只发 3 次最小请求（2 warm） |
| 账号 | 单一（凭据来自既有环境变量，未写入任何报告或归档） |
| 统计契约 | 沿用 A v1：有效单请求、`observed` 计数、unknown 不填 0；journal / 成员记录 / 历史账本不混算 |
| 隐私 | 探针只解析 counter 与 `billing_usage` 在场性；不读 prompt、正文、工具参数、凭据 |

**冻结版本可确认**：`stream_usage.go` 与合流纪要的冻结摘要逐字节相同，因此本轮的测试与端点结果对应同一个被冻结的折叠规则。

---

## 2. 三条 usage 路径的调用链

三条路径**互不共享实现**，因此必须分别验证：

| 方言 | 折叠单元 | 入口 | 归一化 |
|---|---|---|---|
| **anthropic** | **多事件折叠**：`message_start`（usage 嵌在 `message.usage`）+ `message_delta`（usage 在顶层） | `readStream` → `streamUsage.merge` | `messagesUsage(in, out, create, read, billed, inclusive)` |
| **openai** | **单对象**：每个 SSE chunk 的顶层 `usage` | `readStream` → `normaliseUsage` | 顶层 hit/miss → 嵌套 `prompt_tokens_details` → Anthropic 回退，三级优先 |
| **responses** | **单对象**：终局 `response.completed` / `response.incomplete` 的 `response.usage` | `emitTerminalResponseUsage` → `usageFromResponse` | `input_tokens` 为整段，`cached_tokens` 为子集 |

**关键差异（F-A1）**：anthropic 与 openai 的**跨事件**问题（K1 的根因）在 responses 上**结构上不存在**——它只读一个终局对象，没有第二个事件可以混用。但反过来，responses 的 `input_tokens` 是**整段**，而 openai 路径的 Anthropic 回退把 `input_tokens` 当**未缓存部分**。同一组数字在两条路径上得到不同 prompt（§3.3 已用测试固定）。

---

## 3. 形状矩阵（本轮新增）

### 3.1 openai 方言：`TestNormaliseUsageEventShapeMatrix`（11 行）

`internal/provider/openai/usage_shape_test.go`（新增）。

| # | 形状 | prompt | hit | miss | 状态 |
|---|---|---|---:|---:|---|
| 1 | DeepSeek 顶层 hit+miss 显式 | 1000 | 900 | 100 | verified |
| 2 | DeepSeek 只报 hit，miss 为余量 | 1000 | 900 | 100 | verified |
| 3 | OpenAI/MiMo 嵌套 `prompt_tokens_details` | 1000 | 900 | 100 | verified |
| 4 | reasoning 在 `completion_tokens_details` | 1000 | 600 | 400 | verified |
| 5 | Anthropic 回退（含 write） | 1000 | 880 | 120 | verified |
| 6 | Anthropic 回退（无 write） | 1000 | 880 | 120 | verified |
| 7 | **cold：无任何 cache 键** | 1000 | 0 | **0** | verified |
| 8 | 全零 usage 对象 | 0 | 0 | 0 | verified |
| 9 | 只报 miss，无 hit 键 | 1000 | 0 | 100 | **ambiguous** |
| 10 | 畸形：嵌套 read > prompt | 1000 | 1200 | 0 | **malformed** |
| 11 | 畸形：顶层 hit > prompt | 100 | 500 | 0 | **malformed** |

**第 7 行是本矩阵最重要的一行**：openai 路径在「端点没报任何 cache 键」时给出 `hit=0, miss=0`，**不是** `miss=prompt`。这与 anthropic 路径（`miss = prompt`）**相反**，且两者都是对的——各自遵循自己的词汇表。任何跨方言的 miss 对比必须知道这一点。

第 9–11 行 `prompt != hit + miss`：counter 本身不闭合，读数**不 clamp、不构造**，交由下游排除。

### 3.2 responses 方言：`TestUsageFromResponseShapeMatrix`（8 行）

`internal/provider/responses/usage_shape_test.go`（新增）。

| # | 形状 | prompt | hit | miss | 状态 |
|---|---|---|---:|---:|---|
| 1 | warm：read 是 input 的子集 | 1000 | 900 | 100 | verified |
| 2 | cold：无 cache detail 块 | 1000 | 0 | **1000** | verified |
| 3 | 全命中：cached == input | 1000 | 1000 | 0 | verified |
| 4 | reasoning 在 `output_tokens_details` | 1000 | 600 | 400 | verified |
| 5 | total 缺失，由 input+output 推导 | 1000 | 900 | 100 | verified |
| 6 | 完全没有 usage 对象 | 0 | 0 | 0 | verified |
| 7 | nil response | 0 | 0 | 0 | verified |
| 8 | 畸形：cached > input | 1000 | 1200 | 0 | **malformed** |

**第 2 行与 openai 第 7 行相反**：responses 在「无 cache detail」时给出 `miss = input`（整段未缓存），因为它文档化了 `cached_tokens` 是 `input_tokens` 的子集——「没报」在这个词汇表里意味着「没命中」。openai 的 chat 路径没有这个文档化前提，所以它不推断。**两条路径都正确，但不可互换。**

### 3.3 两条路径不可互换（`TestUsageFromResponseIsNotTheChatCompletionsPath`）

同一组数字 `input=1000, read=900`：

| 路径 | prompt | miss |
|---|---:|---:|
| responses | **1000** | 100 |
| chat 路径的 Anthropic 回退 | **1900** | 1000 |

测试断言两者**必须不同**——若将来相同，说明分离声明需要重新检查。这是对「OpenAI 路径覆盖 responses」这一假设的直接反证。

### 3.4 畸形形状的处理（F-A7）

三条路径**一致**：**不 clamp 报出来的 counter**，只对**派生量**取 floor。

| 情形 | 处理 |
|---|---|
| 派生 miss 为负（`input - cached < 0`） | floor 到 0（anthropic 的 `max(inTok-cacheRead, 0)`、responses 的 `max(u.InputTokens-cached, 0)`） |
| 端点**报出**的负值 | **原样穿过**，不修正 |
| 报出的 read/hit > prompt | **两个数都保留**，不 clamp |

理由（已写入测试注释）：猜一个下限等于发明一个上游从未声明的读数。这些样本由 A v1 契约的 `MemberCacheRequest.Accounting()` 标为 `negative:*` / `split_exceeds_prompt`，经 `accounting_invalid` **具名排除**。

---

## 4. 真实端点验证

### 4.1 本 route：**通过**（F-A2、F-A3、F-A4）

`TestLiveUsageConvention`（新增，`internal/provider/anthropic/usage_convention_live_test.go`，`-tags live`）。

**方法**：adapter 拨一个 tee 代理 → tee 拨真实端点。adapter 读端点的原始字节，tee 留一份响应体供复读。**只解析 counter 与 `billing_usage` 在场性。**

**实测输出**：

```
endpoint host: aiapi.lejurobot.com
declared convention: inclusive=false (endpoint-derived, not reasoning_protocol-derived)
turn 1: adapter prompt=1225 hit=0    miss=1225 write=0 | events=2 closed=true
turn 2: adapter prompt=1225 hit=1152 miss=73   write=0 | events=2 closed=true
turn 3: adapter prompt=1225 hit=1152 miss=73   write=0 | events=2 closed=true
3 turns, 2 warm, 3 usage events carried the gateway's private billing block
```

**判据（全部无 oracle 依赖）**：

1. `prompt == hit + miss` 逐轮闭合；
2. 无负值 counter；
3. **adapter 报的 hit ≤ 端点在事件流里报过的最大 read** —— 读数没有超出端点实际说过的数；
4. 端点语义自洽：warm 的 `read=1152 > input=73`，故 `input_tokens` 是余量，`inclusiveInput()==false` 与之一致。

**与 C 的独立复核一致**：C 的 `TestLiveUsageFoldVerification`（`internal/cachelab`，另一实现、另一判据）本轮同机复跑通过——4 warm 轮、0 违反、`adapter == served` 逐轮成立，且被替换的逐字段 max 规则在同一形状上会报 `miss=450`（多 377）。

**覆盖边界（诚实标注）**：
- 本轮 3 次请求、2 warm，**样本极小**，只够判「约定一致」，不够判命中率；
- 只覆盖 1.2K prompt；**未覆盖 768K+**（合流纪要 §9.3 仍未决）；
- 只覆盖**一个账号、一个网关**；
- 端点每次都带了 `billing_usage`，因此本轮的「无 oracle」是**判据上**的无依赖，而非「样本里没有 oracle」。判据本身在缺失时同样成立（它比较的是事件流，不是 oracle）。

### 4.2 其余 route：**未决**（F-A5）

| route | 凭据 | 本轮状态 |
|---|---|---|
| native Anthropic（`api.anthropic.com`） | `ANTHROPIC_*` 指向本网关，**非** native | **未决** |
| LongCat（`api.longcat.chat`） | `LONGCAT_API_KEY` **不存在** | **未决** |
| 官方 DeepSeek（`api.deepseek.com`） | `DEEPSEEK_API_KEY` **不存在** | **未决** |
| OpenAI 直连 | `OPENAI_API_KEY` **不存在** | **未决** |

**表驱动 fixture 不升级为端点通过**（方案 §4 第 3 条）。四者的形状由 §3 的矩阵与 Part A 的 17 行矩阵覆盖，状态保持 `unverified/blocked`。

---

## 5. 按 Provider 的最终判定

| Provider / route | 判定 | 依据 | 缺什么 |
|---|---|---|---|
| **DeepSeek-compatible 自定义网关**（本 route） | **GO**（限已测形状与边界） | §4.1 真实端点 3 轮 0 违反 | 768K+ / 并发 / 多账号 |
| native Anthropic | **未决** | 形状由 fixture 固定（Part A 矩阵 1–3 行） | 端点凭据 |
| LongCat Anthropic | **未决** | 形状由既有真实测试固定（Part A 矩阵 4–5 行） | 端点凭据 |
| 官方 DeepSeek Anthropic | **未决** | `inclusiveUsage` 由 `IsDeepSeek` 判定；读数为构造 | 端点凭据 |
| **OpenAI chat（openai kind）** | **有限通过** | §3.1 的 11 行矩阵；**无端点观测** | 端点凭据 |
| **Responses 方言** | **有限通过** | §3.2 的 8 行矩阵；**无端点观测**；与 chat 路径确认不共享实现 | 端点凭据 |
| 其他兼容端点 | **未决** | 无 fixture、无观测 | — |

**不得用本 route 结果概括其他 route**（方案 §8.1）。**"未决"不得被读成"通过"**（合流纪要 §9.1）。

---

## 6. 写集与边界遵守

| 项 | 状态 |
|---|---|
| 新增 `internal/provider/openai/usage_shape_test.go` | ✅ |
| 新增 `internal/provider/responses/usage_shape_test.go` | ✅ |
| 新增 `internal/provider/anthropic/usage_convention_live_test.go`（`-tags live`） | ✅ |
| 未改任何生产 usage 语义（`stream_usage.go` md5 与冻结值相同） | ✅ |
| 未改 Team 请求构造 / 缓存策略 / 统计分母 | ✅ |
| 未写入 prompt、正文、工具参数、凭据 | ✅（探针只解析 counter 与布尔在场性） |
| 未触碰 `internal/cli/live_team_cache_strata_test.go`（**B 的文件**，mtime 本轮内） | ✅ |
| 未触碰 `internal/cachelab/**`（**C 的写集**） | ✅ |

---

## 7. 复现

```bash
# 三条方言的形状矩阵（离线，零成本）
go test ./internal/provider/openai/    -run 'TestNormaliseUsage|TestMergeUsage' -v -count=1
go test ./internal/provider/responses/ -run 'TestUsageFromResponse' -v -count=1
go test ./internal/provider/anthropic/ -run 'TestUsage' -v -count=1

# 真实端点只读复核（需凭据；本机用 ANTHROPIC_* 回退）
go test -tags live ./internal/provider/anthropic/ -run 'TestLiveUsageConvention' -v -count=1

# C 的独立复核（另一实现、另一判据）
go test -tags live ./internal/cachelab/ -run 'TestLiveUsageFoldVerification' -v -count=1

# 离线验证
go test ./internal/provider/... ./internal/agent/ ./internal/team/ ./internal/stats/ ./internal/cachelab/ -count=1
go vet ./internal/provider/... ./internal/team/... ./internal/cachelab/ ./internal/cli/...
go run ./tools/repolint
bash scripts/cache-guard.sh
```

**本轮实测**：provider 4 包 / agent 49.9s / team 6.3s / stats / cachelab **全绿**；`go vet` 干净；`repolint` 对本轮新增文件**无违规**（唯一红项 `internal/cli/live_team_cache_strata_test.go` 是 **B 的未提交文件**，见 §8）。

---

## 8. 与另外两方的接口

- **对 B**：§5 的「有限通过 / 未决」意味着 B 的跨 route 对比必须声明**端点**（口径由它决定）。本轮起 `reasoning_protocol` **不再影响** usage 读数（A-5 拆分，本 route 已实测确认），因此 B 的臂登记不需要再记录 protocol 来保证可比性——但需要记录 endpoint。
- **对 C**：§4.1 提供了一条与 C 的 `TestLiveUsageFoldVerification` **不同实现、不同判据**的端点复核，两者本轮同机通过、结论一致。C 的 §9 判定式仍成立；本轮的判据（adapter vs 事件流）不依赖 `billing_usage`。
- **未决移交**：§5 的四个未决 route 需要端点凭据；§4.1 的三条覆盖边界（768K+ / 并发 / 多账号）移交下一轮。
- **未触碰**：`internal/cli/live_team_cache_strata_test.go` 是 B 本轮新增的文件，`internal/cachelab/**` 是 C 的写集，本报告均未修改。
