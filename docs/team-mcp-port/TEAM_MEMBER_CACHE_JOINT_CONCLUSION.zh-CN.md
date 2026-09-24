# Team Member 缓存命中率：三 Agent 联合结论（供交叉分析）

> 状态：A / B / C 三方均已交付，C 已合流。日期：2026-09-24。基线：`58e4e9c6c`（Team-agent）+ 本轮未提交改动。
> 用途：**供其他 Agent 交叉分析**。本文只汇总三方已交付的结论与证据，并明确标注每条结论的强度、可复现方式与**待攻击点**。
> 详细材料：A `TEAM_MEMBER_CACHE_PART_A_CONTRACT.zh-CN.md`；B `TEAM_MEMBER_CACHE_PROVIDER_EXPERIMENT_PART_B.zh-CN.md`；C `TEAM_MEMBER_CACHE_PART_C_REVIEW.zh-CN.md`。

---

## 0. 给交叉分析者的读法

**先读 §2（核心发现），它改变了其他所有结论的解读方式。**

本文最有价值的待攻击点，按价值排序：

1. **§2 的适配层缺陷判定**：它依赖"网关的 `billing_usage.openai_usage` 块是可信 oracle"。若该块本身不可信，K1 的全部论证作废。
2. **§3.2 的块粒度模型**：`hit % 128 == 0` 与 `miss ≡ prompt (mod 128)` 是从 11 份 journal + 8 点扫描归纳的，不是文档化的 provider 行为。
3. **§5 的"跌幅未被完全解释"**：`32k_128k` 桶 09-24 的 miss 占 prompt 33%，而假象上限 14%。剩下的 ~19pp 无人解释。
4. **§4.1 的口径降级**：A 把历史账本的 per-request 基线判为不可用，理由是旧行没有 provenance。这是**统计决定**，不是观测，值得独立复核。
5. **§6 的候选撤回**：B 的候选 (i) 被撤回的依据是块对齐。若块对齐不成立，该候选应恢复。

**本文不主张**：任何"已找到根因"的结论。三方一致同意：本地原因已基本排除，但跌幅的**全部**来源仍未定位。

---

## 1. 三方各自交付了什么

| Part | 交付物 | 核心产出 |
|---|---|---|
| **A**（数据可信度） | `TEAM_MEMBER_CACHE_PART_A_CONTRACT.zh-CN.md`（v1 契约）、`TEAM_MEMBER_CACHE_DATA_AUDIT.md` | 冻结统计契约；发现 `request_count` 的「0 视为 1」无法区分"观测到 1 个"与"无人观测"，补 provenance 字段并**据此把历史账本降级** |
| **B**（真实 Provider 实验） | `TEAM_MEMBER_CACHE_PROVIDER_EXPERIMENT_PART_B.zh-CN.md` + `internal/cachelab` 夹具 | 9 臂 + 1 装配探针的真实 Provider 受控实验；预注册样本量与停止规则 |
| **C**（复核与准入） | `TEAM_MEMBER_CACHE_PART_C_REVIEW.zh-CN.md` | 独立复核 A/B；发现并修复适配层 usage 缺陷（K1）；撤回 B 的候选 (i)；合流门禁 |

---

## 2. 核心发现：命中率数字一直被一个适配层缺陷污染

### 2.1 现象

同一个 warm 请求，适配层报 **85.34%**，网关自己的读数报 **99.91%**。

```
=== warm 请求的 3 个 usage 事件（裸请求，直读 SSE）
   [0] message_start  input=  39149 read=      0 create=      0
   [1] message_start  input=     29 read=  33408 create=      0
       billing_usage.openai_usage: prompt_tokens=33437 cached_tokens=33408 miss=29
   [2] message_delta  input=     29 read=  33408 create=      0
       billing_usage.openai_usage: prompt_tokens=33437 cached_tokens=33408 miss=29
   RULE A (逐字段 MAX，修复前): prompt=39149 hit=33408 miss=5741 rate=85.34%
   RULE B (网关读数)          : prompt=33437 hit=33408 miss=  29 rate=99.91%
```

### 2.2 机制

`message_start` 里 `input_tokens` 是**整段 prompt 的预估**（39149），`message_delta`（或第二次 `message_start`）里是**未缓存余量**（29）。旧代码对每个字段取 `max`：

```go
inTok = max(inTok, usage.InputTokens)   // 取到 39149，即"整段"
```

再按 inclusive 折算 `miss = 39149 − 33408 = 5741` —— **这个数在任何一次上游读数里都不存在**。

### 2.3 为什么长期未被发现

1. 账本只存归一化值，`prompt == hit + miss` 是构造不变式，**闭合检查永远通过**（A 的审计文档 §6 已自我更正过这一点）。
2. 假象的形状与"前缀每轮稳定多丢一块"**完全相容** —— 正是 A §4.3 与 B §3.5 观察到的 C5 形状（分布整体平移、p10 从 0.2% 跳到 21.4%）。它被合理地读成了缓存行为。
3. 两套词表同时出现在一个响应里，使 B 的录制器直接放弃解析（C 复现 B0-pilot 时 9/9 样本 `no_cache_split`）。

### 2.4 量级（真实 Provider）

| 路径 | prompt | 适配层（修复前） | 网关读数 | 假象 |
|---|---:|---:|---:|---:|
| 裸请求 | 33,437 | 85.34% | 99.91% | 5,741 token |
| member ~40K warm | 40,665 | 90.02% | 100.00% | 4,013 token ≈ 9.9pp |
| member ~400K warm | 347,762 | 85.77% | 99.97% | 57,567 token ≈ 14.2pp |

### 2.5 修复（K1，已落地）

新增 `internal/provider/anthropic/stream_usage.go`（58 行），把折叠逻辑从 `readStream` 提取为具名类型：

| 字段 | 旧规则 | 新规则 |
|---|---|---|
| `input_tokens` | 逐事件 max | 取**最后一个带 cache split 的事件**的读数 |
| `cache_read` / `cache_creation` | 逐事件 max | 同上 |
| `output_tokens` | max | max（不变，该字段确实单调累积） |

**约定判定**（两个独立信号，任一成立即 input 是余量）：

1. 前面有一个**无 split 且更大的** `input_tokens`（那是整段预估）；
2. `cache_read > input_tokens`（根本装不下，只可能是余量）。

**为什么不能只换 fold 规则**：max 对 `cache_read` 是对的（本网关 cold 时 `message_start` 报 2048、delta 报 0，取 max 反而救了这个值）。错的是 `input_tokens` 在不同事件里**含义不同**。必须按字段分开处理。

**验证**（用网关自己的 `billing_usage` 当 oracle，逐请求比对）：裸请求 4/4、成员路径 6/6 **完全一致**；`prompt == hit + miss` 不变式全部保持。低命中形状（`read` 小、余量大的启发式最易失效处）用同 body 的 cold 请求钉真值 35193，新规则差 0，旧规则差 −1792。

**影响面**：provider-visible 字节**完全未变**；所有命中率数字**阶跃**到真值（本路由 warm 从 ~85% → ~99.9%）；历史账本不回填。

### 2.6 待攻击点

- **oracle 可信性**：`billing_usage` 是网关私有扩展，不在任何公开协议里，且**不是每个响应都有**（低命中那次就没有）。若它本身是错的，K1 的论证基础不成立。
- **单网关结论**：全部证据来自 `aiapi.lejurobot.com` 一个网关、一个账号。native Anthropic、LongCat、OpenAI 直连是否也有同类跨事件字段混用，**未审**。
- **修复的盲点**：若某上游在"整段 prompt 100% 命中"时发 `delta(input=0, read=0)`（省略 split），规则会取到前面的预估，从而**高估 miss**。本网关未观察到，但未排除。

---

## 3. 缓存行为：真实 Provider 的实测形状

### 3.1 裸请求尺寸阶梯（33K–533K）

| 请求字符 | prompt tokens | hit | miss | 命中率 |
|---:|---:|---:|---:|---:|
| 150,000 | 33,440 | 33,408 | 32 | 99.90% |
| 400,000 | 88,992 | 88,960 | 32 | 99.96% |
| 800,000 | 177,878 | 177,792 | 86 | 99.95% |
| 1,600,000 | 355,665 | 355,584 | 81 | 99.98% |
| 2,400,000 | 533,437 | 533,376 | 61 | 99.99% |

**结论（证据）**：本路由上稳定前缀的 warm 请求在 33K–533K 全档命中 99.9%+，miss 只有 30–90 token。"长上下文命中率低"在本路由上**不成立**。

### 3.2 块粒度模型

B 的 11 份 journal 全部满足两条不变量：

1. **`hit` 恒为 128 的整数倍**；
2. **`miss ≡ prompt (mod 128)`**。

| 臂 | prompt | hit | miss | `hit % 128` | `miss % 128` |
|---|---:|---:|---:|---:|---:|
| B0-pilot ×3 / B1 / B2 / B3-serialization / B4-small | 3048 | 2816 | 232 | 0 | 104 |
| B3-system-tail | 3058 | 2816 | 242 | 0 | 114 |
| B3-tool-schema | 3054 | 2816 | 238 | 0 | 110 |
| B4-ladder-mid | 17448 | 17280 | 168 | 0 | 40 |
| B4-ladder-large | 67269 | 67072 | 197 | 0 | 69 |

独立复现（8 点扫描，步进 60 字符）：`cached % 128 == 0` 与 `miss == prompt % 128` 逐条成立。

**同一夹具、不同时刻，miss 是 232 / 12 / 3**（k 从 1 变到 0）。固定开销不会随运行改变，块边界落点会。

### 3.3 客户端字节稳定性

真实 member 会话逐请求字节（录制器实测）：

```
turn=1 bytes=165498  turn=2 +165  turn=3 +165  turn=4 +165
```

每轮**恰好 +165 字节**（一次 assistant 回复 + 一次 user 追加），**无任何一轮字节减少**。P1（客户端重写前缀）在生产形状上被证伪。

### 3.4 待攻击点

- 128 是**归纳值**，不是文档化的 provider 行为。它在 B 的 11 份 journal 与 C 的 8 点扫描上成立，但未在 768K–1M 桶或跨账号验证。
- +165 字节只在一个成员、一个会话上测得，样本量小。
- 全部是**串行单请求**；并发下的行为未测。

---

## 4. 统计契约：A 的口径收紧及其代价

### 4.1 历史账本被降级（这是本轮最重要的统计决定）

A 发现 `request_count` 的兼容规则「0 视为 1」**无法区分"观测到 1 个请求"与"无人观测"**，补了 `RequestCountObserved` / `request_count_source`（闭集：`observed` / `defaulted` / `unrecorded`），并据此把主基线收窄为"计数被观测且等于 1"。

**代价**：三天账本各 1,714 行**全部无 provenance 标记**，因此 per-request 基线**不可用**：

| 指标 | 09-23 臂 | 09-24 臂 |
|---|---:|---:|
| 计入 per-request 基线 | **0** | **0** |
| 排除：计数未验证 | 1,714 | 1,714 |
| **全样本加权率** | **84.5%** | **67.7%** |

原先的 85.1% / 68.4% 降级为**全样本口径**（方向与幅度几乎不变：−16.8pp vs −16.7pp）。新构建的真实成员记录 **100% `observed`**，成员级基线不受影响。

### 4.2 报表新增的能力

- 每个 stratum 并列 `hit_tokens_per_request` / `miss_tokens_per_request`（防止"命中率上升"实为"prompt 变大、固定开销不变"的组成效应）；
- 字段覆盖率 `coverage`（计数来源、route/model/usage_source/诊断的在场率）；
- `AllSamplesTotals()` 可对账的全样本口径。

### 4.3 待攻击点

- **这是统计决定，不是观测**。它把可用数据从 1,714 行降到 0 行。值得独立复核"是否过于保守"——一个替代方案是保留旧行为一条"历史口径"支路。
- **L-1（A 自己标注）**：provenance 只表达"usage 是否带了计数"，**不能**区分"上游 merge 造出的 1"。而这正是 §2 缺陷的上游版本。
- 全样本率把不同性质的样本（聚合、未验证计数）混在一起，只能作全样本口径。

---

## 5. 未决：跌幅里仍有一块无人解释

复跑账本（等样本量 1714 行）：

| 日期 | 加权命中率 | `32k_128k` miss/每请求 | 均值 prompt | miss 占 prompt |
|---|---:|---:|---:|---:|
| 09-23 | 85.1% | 7,015 | 71,070 | 9.9% |
| 09-24 | 68.4% | 27,869 | 84,252 | **33.1%** |

假象上限约 14pp，而 09-24 的 miss 占 prompt 33%。**至少还有 ~19pp 未被解释。**

**三方一致同意的排除项**：

| 假设 | 判定 | 依据 |
|---|---|---|
| P1 客户端前缀被重写 | **排除** | §3.3 逐请求 +165 字节，append-only；B §9.2 离线同结论 |
| P2 内容增长 | **背景量，非变化量** | miss 比每轮新增大 ~20×（复跑：24,927 vs 1,228） |
| P3 简单 TTL | **排除** | 间隔分层无阶跃（09-24 全档 ~32%） |
| P5 模型/尺寸组成 | **排除** | 精确 model 串三天一致；同尺寸桶内跌幅仍在 |
| D2 provider scope/容量 | **排除为主因** | §3.1 裸请求同 route/账号五档 99.9%+ |
| **P4 usage 口径** | **部分定位**（§2），**未穷尽** | 假象解释不了 33% |

### 5.1 待攻击点

- **这是全篇最大的未决**。~19pp 的来源无人给出候选。
- 一个未被检验的方向：09-24 的**任务组成**与 09-23 不同（三天工作负载未知）。等样本量只对齐了样本数。
- 另一个方向：`billing_usage` oracle **不是每个响应都有**，因此无法用它逐条重算历史账本。

---

## 6. 候选变更登记（合流版）

| # | 候选 | 状态 | 依据 |
|---|---|---|---|
| **K1** | 修 `mergeUsage` 跨事件字段混用 | **已实施**（§2.5） | 真实 Provider，网关 oracle 逐请求一致 |
| K2 | 加口径自检诊断字段 | 建议实施（低风险、additive） | §2.6 的盲点 |
| K3 | 修 `cachelab` 的 `resolve()` 只认顶层词表 | 建议实施（仅夹具） | B0-pilot 复现时 9/9 `no_cache_split` |
| ~~K4~~ | ~~成员工具面剪枝 `schema_prune`~~ | **不实施** | 实测 1567 tok 占 128K 的 1.2%；且 miss 是块对齐余量 |
| ~~K5~~ | ~~fold/compaction 边界调整~~ | **不实施** | B §9.2：fold 后仅一次冷轮、前缀不变 |
| ~~K6~~ | ~~工具输出限量/摘要~~ | **不实施** | 背景量，非跌幅来源 |
| ~~K7~~ | ~~前缀中段重写修复（P1）~~ | **关闭**（已证伪） | §3.3 |
| ~~K8~~ | ~~provider scope/TTL/路由亲和性（D2）~~ | **关闭**（已证伪为主因） | §3.1 |
| ~~K9~~ | ~~B 的候选 (i)：稳定前缀外的固定开销~~ | **撤回** | §3.2 块对齐余量；同一夹具实测 miss 232/12/3 |

**计划 §2「候选优化顺序」逐条对照**：第 1–5 项**全部不成立**（前缀稳定、schema 太小、内容是背景量、fold 干净、provider 已排除）。**证据最强的候选不在原清单里** —— 它不是缓存行为优化，而是**测量修复**。

---

## 7. 质量与成本护栏（K1）

| 护栏 | 判据 | 状态 |
|---|---|---|
| 口径自洽 | `prompt == hit + miss` | ✅ 全部测试保持 |
| oracle 一致 | 适配层 == 网关 `billing_usage` | ✅ 裸请求 4/4、成员路径 6/6 |
| native Anthropic 不回归 | `start` 带 input+cache、`delta` 只带 output | ✅ 测试固定 |
| LongCat 形状不回归 | 只有 `delta` 带全部计数 | ✅ 测试固定 |
| 计费不变 | `CacheWriteBilledTokens` 与修复前相同 | ✅ |
| 请求字节不变 | 修复不改 provider-visible 字节 | ✅ 全部 diff 未触及请求构造 |
| 仓库守卫 | `scripts/cache-guard.sh` | ✅ 10/10 case |
| repolint | RED SET 与 HEAD 逐字节相同 | ✅ |

---

## 8. 复现清单

### 8.1 离线（零成本）

```bash
go build ./...
go test ./internal/provider/... ./internal/agent/ ./internal/team/ ./internal/stats/ ./internal/cli/
go test ./internal/provider/anthropic/ -run TestUsage -v      # K1 的形状守卫
go run ./tools/repolint
bash scripts/cache-guard.sh
```

### 8.2 历史账本（零成本）

```bash
reasonix team cache-audit --model deepseek-v4-flash-roojin \
  --from 2026-09-23 --to 2026-09-23 --first 1714 --json --out arm-0923.json
reasonix team cache-audit --model deepseek-v4-flash-roojin \
  --from 2026-09-24 --to 2026-09-24 --first 1714 --json --out arm-0924.json
```

### 8.3 真实 Provider（需凭证，`-tags live`）

```bash
go test -tags live ./internal/cli/ -run 'TestLiveTeamMemberCache(Session|BaselineTeam)$' -v -count=1
REASONIX_LIVE_CACHE_ARM=B1-baseline-repeat go test -tags live ./internal/cli/ \
  -run TestLiveProviderCacheExperiment -v -count=1 -timeout 60m
```

> C 的 12 个独立探针（`zz_partc_*` / `zz_k1_*` / `zz_k2_*`）是临时文件，**已在合流前删除**；其输出与判据记录在 `TEAM_MEMBER_CACHE_PART_C_REVIEW.zh-CN.md`。要重跑需按该文档 §9 重建。

---

## 9. 结论强度总表

| # | 结论 | 强度 | 独立可复现 |
|---|---|---|---|
| 1 | 适配层把"整段预估"减"真实命中"记成 miss（§2） | **证据** | ✅ 裸请求 + 成员路径 + oracle |
| 2 | K1 修复后适配层与网关逐请求一致（§2.5） | **证据** | ✅ 同上 |
| 3 | 本路由 33K–533K warm 命中 99.9%+（§3.1） | **证据** | ✅ 需凭证 |
| 4 | 客户端字节 append-only（§3.3） | **证据** | ✅ 需凭证 |
| 5 | `hit % 128 == 0` / `miss ≡ prompt (mod 128)`（§3.2） | **证据（单网关归纳）** | ✅ B 的 journal 可直接验算 |
| 6 | 历史账本 per-request 基线不可用（§4.1） | **证据** | ✅ 零成本 |
| 7 | 09-24 跌幅里 ~19pp 未被解释（§5） | **证据（缺口）** | ✅ 零成本 |
| 8 | 块粒度 = 128（§3.2） | **推断** | ⚠️ 未跨账号/未覆盖 768K+ |
| 9 | `billing_usage` 是可信 oracle（§2.6） | **假设** | ⚠️ 网关私有扩展，非每响应都有 |
| 10 | 跌幅的剩余来源 | **未决** | — |

---

## 10. 本文作者建议的交叉分析方向

1. **攻击 §2.6 的 oracle**：找一个不依赖 `billing_usage` 的独立核对方式（例如用同一请求的两次发送做差分）。
2. **攻击 §3.2 的 128**：在 768K–1M 桶、跨账号、并发条件下重测块粒度。若 128 不成立，K9 撤回需恢复。
3. **攻复现 §5 的缺口**：用**新构建**重跑一次历史时段，拿到带 provenance 的账本行，再用修复后的口径重算 09-23/09-24 的差距。这是唯一可能把 ~19pp 拆开的路径。
4. **审 §4.1 的保守度**：账本从 1,714 行降到 0 行是否过度？建议给出"历史口径支路"的代价评估。
5. **审 K1 的盲点**：`delta(input=0, read=0)` 形状是否在别的网关上存在；若存在，规则 2 会高估 miss。
