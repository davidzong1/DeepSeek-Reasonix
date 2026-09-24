# Team member 缓存命中率：现状分析与待交叉验证的结论

> 生成时间：2026-09-24。作者：Part A（可观测性与基线）。
> 数据来源：`~/.reasonix/stats/*.jsonl`（本机真实生产账本，逐请求行）。
> 用途：供其他 Agent 交叉分析与证伪。**本文的每一条结论都标注了强度（证据/推断/假设），并附复现命令。**

## 0. 给交叉分析者的读法与攻击面

- 本文**不主张**任何行为改动带来了提升。Part A（可观测性）与当前已落地的 Part B 改动，都没有改变 provider-visible 字节，因此**在机制上不可能改变命中率**（见 §5）。
- 本文最有价值的待攻击点是 **§7 的长上下文可回收量模型**：它依赖一个假设（"小上下文的每请求 miss 可作为每轮不可避免新增内容的代理"）。若该假设不成立，§7 的数字全部作废。
- 本文的口径与 `TEAM_MEMBER_CACHE_OPTIMIZATION_PLAN.md` §1 的成员快照**不可直接相减**，原因见 §3.3。若交叉分析认为两者可比，请给出理由。
- 直接挑战 §8「未确立的结论」比挑战 §2 的数字更有价值：数字是可复现的，解释才是分歧所在。

## 1. 结论摘要

| # | 结论 | 强度 |
|---|---|---|
| C1 | 以同一把尺子（账本日切片、deepseek 成员路由、token 加权）衡量，09-24 的命中率 **67.7%** 相比 09-23 的 **83.8%** 下跌约 **16pp** | 证据 |
| C2 | 该下跌**早于**任何相关代码改动，不是 Part A / Part B 造成的（数据止于 09-24 07:11，最早的相关提交在 10:19） | 证据 |
| C3 | 下跌不是 inclusive `input_tokens` 重复计数造成的度量假象 | 证据（§4.1） |
| C4 | 下跌不是冷启动造成的 | 证据（§4.2） |
| C5 | 下跌不是"周期性整段前缀失效"，而是**整个请求分布平移**：≥90% 命中的请求占比从 46.8–50.1% 掉到 0.3%，10–60% 的中段从 39–56 条涨到 391 条 | 证据（§4.3） |
| C6 | 长上下文（≥256K）是**唯一量级可观的可回收空间**：若 §7 假设成立，该部分可回收 miss 约占当日 miss 的 **31.3%**，日加权率 **+5.07pp**，且 `512k_768k` 桶自身可从 65.1% 升到 ~98.5% | **假设**（§7） |
| C7 | 工具 schema 固定尾巴**不是**主因：成员 provider-visible surface 实测 1,567 tokens，最多解释小上下文 miss 的 ~18%、大上下文的 ~0.75% | 证据 + 推断（§6） |
| C8 | 09-23 15:06–21:59 有四个 member 工具/skill 改动密集落地，其中 `4475e6054` 把 member skill 从 `cat/sed/>>` 换成 `atomic_read/atomic_write`——与劣化窗口相邻 | **仅时间相邻，未建立因果**（§5.2） |
| C9 | 09-24 07:11 之后**没有任何测量**，当前状态未知 | 证据（数据边界） |

## 2. 数据来源与口径（复现前提）

生产账本 `~/.reasonix/stats/<day>.jsonl`，每行一次 provider 请求，字段 `prompt` / `cache_hit` / `cache_miss` / `requests` / `model` / `ts` / `usage_source`。

**过滤**：`model` 以 `deepseek-v4-flash-roojin` 开头（成员路由）。**该过滤是路由级，不是成员级**——账本没有 team/member 维度，四个 ipc-* 成员与任何其他使用该路由的会话混在一起。

两个必须知道的账本陷阱（已在本文所有计算中处理）：

1. **`omitempty` 使零值不落键**：`cache_miss == 0` 的行**没有该键**，读取方必须 `.get(k, 0)`。直接取键会抛 `KeyError`（本文分析首次运行即因此中断）。
2. **`requests > 1` 的行是多请求聚合**（09-23 有 69 条），不应当作单请求样本。

**本文使用的判据（可从数据直接证明，无需猜测）**：
- `hit == 0` ⇒ 该请求的前缀完全没有命中（冷请求）。这是**证据**，不是对 session 边界的猜测。
- `hit > 0` ⇒ warm 请求。
- 账本**没有** session 身份、没有前缀 hash、没有 `prefix_change_reasons`，因此**无法**按 session 阶段（首请求 / warm candidate / fold 后）分层。这正是 Part A 新增逐请求持久化的原因（见 §9）。

## 3. 三天对比

### 3.1 总量（deepseek 成员路由）

| 日期 | 请求数 | prompt tokens | token 加权命中 | 冷请求 | 冷请求占 miss | warm 率 | warm 每请求平均 miss |
|---|---:|---:|---:|---:|---:|---:|---:|
| 09-22 | 1,029 | 84,816,463 | 84.7% | 9 | 0.3% | 84.7% | 12,677 |
| **09-23（本文"此前"）** | 1,973 | 174,836,681 | **83.8%** | 17 | **15.1%** | 85.9% | 12,271 |
| 09-24（00:00–07:11） | 1,714 | 163,136,048 | **67.7%** | 2 | 0.2% | 67.7% | **30,710** |

### 3.2 分桶（09-23 vs 09-24）

| prompt 桶 | 09-23 请求 / 均值 prompt / 均值 miss / 命中 | 09-24 请求 / 均值 prompt / 均值 miss / 命中 |
|---|---|---|
| `<32k` | 100 / 26,714 / 6,495 / 75.7% | 14 / 28,142 / 13,925 / 50.5% |
| `32k_128k` | 1,673 / 72,489 / 8,702 / 88.0% | 1,442 / 84,472 / **27,992** / 66.9% |
| `128k_256k` | 141 / 160,589 / 26,166 / 83.7% | 258 / 158,660 / 47,051 / 70.3% |
| `256k_512k` | 27 / 337,491 / 99,449 / **70.5%** | — |
| `512k_768k` | 32 / 597,981 / 208,889 / **65.1%** | — |

**关键对比（09-23 → 09-24，与上表同列）**：`32k_128k` 桶的均值 prompt 只涨 **16.5%**（72,489 → 84,472），均值 miss 涨 **222%**（8,702 → 27,992）。**不是"prompt 变大所以 miss 变大"。**

### 3.3 与 `OPTIMIZATION_PLAN.md` §1 成员快照的口径差异（**不可相减**）

plan §1 的"此前"= **68.4% 简单平均 / 70.1% token 加权**，来自 `cpp_ipc_team` 四个成员的 **owner usage 快照**；本文的 83.8% 是**当日账本的路由级 token 加权率**。二者不可直接比较：

- 前者是**每成员 session 累计率的等权平均**——长 session 的低命中成员（`ipc-protocol` 58.2%，`context_used` 1,264,632）与短 session 的高命中成员（`ipc-transport` 79.4%）等权；
- 后者的 session 可能跨越多日，其"累计"包含冷启动与早期低命中段；
- 账本日切片只覆盖一天，且混合了该路由上的其他会话。

**这是一个待交叉分析的口径问题，不是数字矛盾。**

## 4. 三个已排除的假象

### 4.1 不是 inclusive `input_tokens` 重复计数

`internal/provider/anthropic/messages_usage.go` 曾把 DeepSeek 网关的 inclusive `input_tokens` 与 cache 计数相加，导致同一 session 显示 48–49%（真实 92.3%）。诊断法：`prompt - 2*cache_hit` 小而正 ⇒ 重复计数。

实测三天：`prompt == cache_hit + cache_miss` **全部成立**（1029/1029、1973/1973、1714/1714），`median(prompt - 2*cache_hit)` = **−47,214 / −54,672 / −31,726**（大负数，方向相反）。**结论：09-24 的下跌是真实现象。**

### 4.2 不是冷启动

冷请求（`hit == 0`）占 miss 的 **0.2%–0.3%**（09-22 / 09-24）。09-23 例外：17 条冷请求承载 15.1% 的 miss（4,264,567 tokens），是大上下文 resume。

### 4.3 不是周期性整段失效

| 日期 | 近全 miss（<10%） | 占 miss | 中段（10–60%） | ≥90% |
|---|---:|---:|---:|---:|
| 09-22 | 11 (1.1%) | 5.5% | 39 | 516 (50.1%) |
| 09-23 | 22 (1.1%) | 22.5% | 56 | 923 (46.8%) |
| **09-24** | **2 (0.1%)** | **0.2%** | **391** | **5 (0.3%)** |

09-24 的近全 miss **更少**，而中段暴涨、高命中段几乎消失。**整个分布平移**，说明不是某类请求被打断，而是**每个请求都稳定多丢一块**。

## 5. 为什么这不是 Part A / Part B 造成的

### 5.1 时间边界（证据）

- 账本 09-24 数据：**00:00:13 – 07:11:56**。
- Part B 提交 `c1b228222`：**09-24 10:19:03**。Part A 的改动更晚。
- ⇒ 劣化窗口内不存在任何一个 Part 的改动。

### 5.2 相邻的改动（**仅时间相邻，不构成因果**）

09-23 15:06–21:59 密集落地：

| 时间 | 提交 | 内容 |
|---|---|---|
| 09-23 15:06 | `4475e6054` | member skill 从 `cat/sed/>>` 改为 `atomic_read/atomic_write` |
| 09-23 19:56 | `35669ca75` | fix team member tools and arguments handling |
| 09-23 20:34 | `0af25dc41` | update team member tools and task service |
| 09-23 21:59 | `e17c7eaa1` | fix team member token issue |

四个都是**成员工具面/skill** 改动，而工具面与 skill 目录都进 cache-stable 前缀。**这与 C5 的"每请求稳定多丢一块"形状相容，但本文没有建立因果**——需要 §10 的实验才能判定。

### 5.3 已落地代码的机制上限（证据）

按 Part B 自己的 PR 记录（`BEHAVIOR_PLAN.md`）：延期 MCP 尾巴定序（"该尾巴当前恒为空，输出不变"）、fold/prune/truncate 归因 reason（"仅诊断字段，不改 provider-visible 字节"）、以及让 `visible_window_tokens` / `cache_aware_compaction` 两个**此前被 `New()` 静默丢弃**的配置键真正生效（两者零值即旧行为）。**默认构建的 provider-visible 字节未变，命中率机制上不可能变化。**

Part A 全部改动为观测（新增记录、分桶、归因、报表），同样不改请求字节。

## 6. schema 固定尾巴的量级上界（证据 + 推断）

Part B 的 `internal/boot/member_surface_tokens_test.go` 在真实 provider 边界实测（跑 `go test ./internal/boot/ -run TestMemberSurfaceTokenFootprint -v` 可复现）：

```
B2 schema footprint: leader 17 tools / 3397 schema tokens;
                     member  8 tools / 1567 schema tokens; deferred saving 1830 tokens (53.9%)
```

**推断**：若该 1,567 tokens 每请求都重付，它也最多解释小上下文（`32k_128k`，均值 miss 8,702）的 **18.0%**，以及大上下文（`512k_768k`，均值 miss 208,889）的 **0.75%**。
**边界**：1,567 是测试探针表面（含 6 个 probe 工具）的下界；真实成员的 provider-visible 面可能更大。但即使放大数倍，也无法解释 8,702–208,889 的每请求 miss。

## 7. 长上下文可回收量模型（**假设，本文最需要被攻击的一段**）

### 7.1 假设

**H1**：`32k_128k` 桶的均值 miss（09-23 = 8,702 tokens/请求）可作为"每轮不可避免新增内容"的代理。理由：该桶请求量大（1,673 条）且命中率 88.0%，其 miss 主要由当轮追加的新内容构成。
**H1 的替代解释**：大上下文的当轮新增内容本身可能远大于 8,702（例如 `atomic_read` 返回整文件、长工具结果）。若如此，§7.2 的可回收量大幅缩小甚至归零。

### 7.2 计算（09-23）

| 桶 | 请求 | 均值 prompt | miss | 命中 | 按 H1 可回收 | 回收后该桶 |
|---|---:|---:|---:|---:|---:|---:|
| `256k_512k` | 27 | 337,491 | 2,685,111 | 70.5% | 2,450,145 | 97.4% |
| `512k_768k` | 32 | 597,981 | 6,684,444 | 65.1% | 6,405,966 | **98.5%** |
| 合计 | 59 | — | 9,369,555 | — | **8,856,112** | — |

- 占当日 miss 的 **31.3%**（8,856,112 / 28,267,593）
- 当日 token 加权率：**83.83% → 88.90%（+5.07pp）**

**稳健性检查**：若改用当日 warm 请求的整体均值 miss（12,271）作代理，结果为 **+4.94pp**（可回收占 miss 30.6%）。⇒ 结论对代理选择不敏感，**+5.0pp 量级**。

### 7.3 为什么这与"长上下文"直接相关

大上下文的每请求 miss（99,449 / 208,889）比小上下文（8,702）**高 1–2 个数量级**。若 H1 成立，则大上下文桶的 miss 中约 **91–96% 是可回收的**——**可提升的空间几乎全部集中在长上下文**。若 H1 不成立，则这些 miss 是真实内容增长，属内容治理（限制工具输出体积、分段读取），缓存机制无法解决。

**这两条路的处置完全不同，而账本无法区分它们**——见 §9。

## 8. 未确立的结论（请勿从本文推出）

1. **没有**建立任何代码改动与命中率变化的因果（C8 仅时间相邻）。
2. **没有**证明 H1（§7.1）成立。大上下文的每请求 miss 也可能是真实新增内容。
3. **没有**测量 09-24 07:11 之后的任何状态；"当前命中率"未知。
4. **没有**区分四个 ipc-* 成员——全部数字是路由级。
5. **没有** `PrefixChangeReasons`、`StablePrefixHash`、`ToolSchemaTokens` 的历史数据，因此**无法**判断劣化是否由前缀每轮变化引起。这正是 Part A 新增持久化的字段（见 §9）。
6. **没有**验证 `context_used`（如 `ipc-protocol` 的 1,264,632）与请求 prompt 的定义差异对上述任何数字的影响——本文全部使用账本的 `prompt`，未使用 gauge。
7. **没有**排除"该路由上还有其他非 ipc-* 会话"对路由级数字的影响。

## 9. 本文缺失、而新观测已经具备的字段

Part A 已落地逐请求持久化（`<team data root>/<team>/<member>/.cache_requests.jsonl`，有界、可按 `.usage.json` 同目录读取），每条含：`stable_prefix_hash`、`stable_prefix_changed`、`prefix_change_reasons`（取自 `internal/cachereason` 的 13 值受控词表）、`tool_schema_tokens_estimate`、`context_prompt_tokens`、`request_count`、`accounting_valid`、`route_bucket`。

**一条真实 session 即可判定 §7.1 的 H1**：
- 若大上下文样本 `stable_prefix_changed=false` 且 miss 与 `context_prompt_tokens` 同步增长 ⇒ **H1 不成立**（真实内容增长，P2）。
- 若 `stable_prefix_changed=true` 或 `prefix_change_reasons` 非空 ⇒ **前缀每轮在变**（P1，可修），且 reason 直接指出是哪一类变化。

报表：`reasonix team cache-report --json`（另有 `--export-requests` / `--route` / `--from` / `--to`）。

## 10. 复现与建议的下一步

### 10.1 复现本文全部数字

```bash
# 总量与冷/warm 拆分（注意 .get 默认值：omitempty 使零值不落键）
python3 - <<'PY'
import json
for day in ['2026-09-22','2026-09-23','2026-09-24']:
    rows=[]
    for l in open(f'/home/zwc/.reasonix/stats/{day}.jsonl'):
        l=l.strip()
        if not l: continue
        r=json.loads(l)
        if r.get('model','').startswith('deepseek-v4-flash-roojin'): rows.append(r)
    P=sum(r.get('prompt',0) for r in rows)
    H=sum(r.get('cache_hit',0) for r in rows)
    M=sum(r.get('cache_miss',0) for r in rows)
    cold=[r for r in rows if r.get('cache_hit',0)==0]
    print(day, len(rows), f"{H/(H+M)*100:.1f}%", "cold:", len(cold),
          "cold_miss_share:", f"{sum(r.get('cache_miss',0) for r in cold)/M*100:.1f}%")
PY
```

其它：双重计数诊断见 §4.1；schema 足迹见 §6；分桶与长上下文模型见 §7.2（分桶边界左闭右开：32,768 / 131,072 / 262,144 / 524,288 / 786,432 / 1,048,576）。

### 10.2 能一举判定 C8 与 H1 的实验

在真实 Team 上跑一个成员 session，然后：

```bash
reasonix team cache-report --json --out after.json
reasonix team cache-report --export-requests --out req.jsonl
```

判据：
- `req.jsonl` 中大 prompt 样本的 `stable_prefix_changed` / `prefix_change_reasons` 分布 ⇒ 区分 P1（前缀在变）与 P2（内容增长）。
- 若为 P1 且 reason 指向 `tools` / `system` ⇒ 与 §5.2 的四个 member 工具改动对应，属可修的每轮前缀变化。
- `route_bucket` 可确认劣化是否集中在单一路由（跨池切换会换 bucket）。

### 10.3 我不建议的做法

- 不要把 §3.1 的 83.8% 与 `OPTIMIZATION_PLAN.md` §1 的 68.4% 相减（§3.3）。
- 不要用 `context_used` 作为 prompt 桶键或命中率分母。
- 不要因为某个成员 session 累计率低就断定缓存机制退化——先按 §4.1 排除度量假象，再按 §4.3 看分布形状。

## 11. 本文作者已被证伪的两次结论（供交叉分析者校准可信度）

诚实记录，因为本文的价值取决于读者知道哪些说法已经被推翻过：

1. **"重写原因词表的唯一生产者是 `projectionRewriteReason`，`guardian_merge`/`rewind_truncate` 只存在于注释"** —— 错。`Session.Rewrite` 是第二条入队路径，二者都是活的生产点。据此做的"修复"一度删掉了两个活值，构成回归。真实词表为 **13 值 / 7 生产点 / 5 包**，现由 `internal/cachereason` 单一持有。
2. **"warm 请求已 ~99%，长上下文没有可提升空间"** —— 错，且来源是外推：该结论来自一个 31K prompt、5 个工具的探针。真实账本显示 warm 仅 84.7–85.9%（32–128K），大上下文 65–70%。**本文 §3/§7 取代该结论。**

两次错误的共同形态：**从单一视角的有限证据推断全量**。交叉分析者对本文 §7 的 H1 应持同样警惕。
