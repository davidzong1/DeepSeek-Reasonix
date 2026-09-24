# Part B：Team member 缓存命中率的真实 Provider 受控实验（预注册 + 执行记录）

> 状态：**已在真实 Provider 上执行 9 个臂 + 1 个装配探针（2026-09-24）**；B2-interval-long、B5、B6 未执行（原因见 §7）。
> 本文是 `TEAM_MEMBER_CACHE_OPTIMIZATION_EXECUTION_PLAN.zh-CN.md` §2「Agent B」的交付：预注册的固定请求受控实验、夹具实现、逐请求记录与结果。
> 本文**不改**任何 provider-visible 请求构造、缓存策略、统计分母或成员隔离；未修改 `internal/team/**`、`internal/cli/team_usage_publish.go`、`internal/cli/team_cache_report.go`。
> 结论边界：全部测量只覆盖**一个账号池条目、一个网关路由、一个模型、串行单请求**；不得外推为生产 Team 成员平均命中率，也不是"根因已找到"。

## 1. 范围

| 项 | 值 |
|---|---|
| 代码版本 | 工作树 `58e4e9c6cbf5`（`git rev-parse --short=12 HEAD`）+ 本次新增的夹具与驱动（未提交） |
| Provider / 路由 | 账号池条目 `deepseek-v4-flash` 的 `BaseURL`（OpenAI 风格 `.../v1`，经 `team.ResolveAgentUserProvider` 判定为 **anthropic** 适配器路由） |
| 模型 | `deepseek/deepseek-v4.1-flash`（边界臂）；装配探针用池内原始拼写 `deepseek/deepseek-v4.1-flash[1m]` |
| 请求构造 | 合成、无敏感内容的冻结请求字节（`internal/cachelab` 夹具：nonce + 固定 system/工具 schema/user），**不含**任何项目正文 |
| 成员 | 边界臂：`member_id=provider-boundary`（记录的是同一账号池条目）；装配探针：真实成员 backend（`probe`） |
| 并发 | 串行（单请求在途），客户端间隔 = 注册值（见 §2） |
| 凭据 | 运行期由账号池条目提供，从未写入测试二进制、日志或本文档 |

## 2. 预注册（在任何正式结果被读取之前冻结）

预注册以代码常量与臂表落地（`internal/cachelab/plan.go`），并在正式臂运行**之前**已写入工作树。

**样本量**：pilot 每条件 ≥8 个有效 warm 请求；正式臂每臂目标 **30**、下限 **20**、上限 **100**（提高上限需在读取正式结果前重新登记）。首请求单列，不并入 warm。

**最小关注效应**：token 加权命中率 **5.0pp**（配对区间下界必须超过它才算"支持"；区间跨过它只能是"未观察到超过登记最小效应的差异"，不是"证明无效应"）。

**费用上限**：单次实验 25.00 USD（价格由 `REASONIX_LIVE_CACHE_PRICE_*` 提供；未提供价格时成本报告为 unknown，不编造）。

**停止条件（仅限已登记原因）**：费用超限；连续 3 次服务错误；连续 3 次 usage 不可用；工具面在同一臂内漂移；recorder 端点改变客户端派生的 reasoning protocol。**中途命中率有利或不利都不构成停止理由**（代码中不存在该分支）。

**臂表（固定条件相同：member、Provider、账号范围、model、route、请求参数、工具面、客户端版本、并发）**：

| 臂 | 阶段 | 唯一变量 | 目标 warm | 附加登记 |
|---|---|---|---|---|
| B0-pilot | pilot | 无（可用性检查） | 8 | — |
| B1-baseline-repeat | formal | 无（完全相同字节、串行短间隔） | 30 | — |
| B2-interval-short / -long | formal | 仅请求间隔 | 30 | 2s / 300s |
| B3-tool-schema / -system-tail / -serialization | formal | 仅一个组件 | 30 | 工具描述一个字段 / system 尾部一句 / 一个 schema 的键顺序 |
| B4-ladder-small / -mid / -large | formal | 仅冻结请求字节 | 30 | 8KB / 60KB / 240KB |
| B5-route-or-account | formal | route 或账号范围 | 30 | **门控**：需已批准且可固定的 route/账号对 |
| B6-client-build | formal | 客户端构建 | 30 | **门控**：需第二个可追溯的构建 |

## 3. 夹具与记录实现

新增包 `internal/cachelab`（**仅被 live 测试导入，不进生产二进制**）：

- `Recorder`：环回反向代理，位于客户端与真实 Provider 之间。只改目的地（scheme/host/path 前缀），方法、头部、body、query 原样转发；逐次记录 **请求体 digest + 字节数**、HTTP 状态、时延、间隔，以及**响应里 Provider 自己写的 usage**（原值，不做归一化）。
- `usage` 解析按 **Provider 词表**读取，不认识就如实报 unknown：`anthropic`（`input_tokens` 为未命中提示，`cache_read_input_tokens` 为命中，`cache_creation_input_tokens` 计入 miss）与 `openai`（`prompt_tokens` 为总数，`cached_tokens`/`prompt_cache_hit_tokens` 为命中，`prompt_cache_miss_tokens` 优先）。每个样本记录实际出现的 key 名，作为读取方式的审计痕迹。
- `Sample.Classify` 依序判：错误／重试／usage 缺失／usage 估算／**无 cache split**／账目不一致／首请求／重复／warm。只有 warm 与"成功后的逐字节重复"进入基线；其余全部计数并给出排除理由（`hit == 0` 一律表述为"未报告 cache read"，不称为冷启动）。
- `Journal`：JSONL，一行一次写入，坏行跳过并计数；写入前用夹具文本做守卫（命中即拒绝写入）。记录里**没有任何可以承载 prompt／工具参数／正文的字段**。
- `Stats`：token 加权率、请求级分位、配对 bootstrap 95% 区间（固定种子，可复现）、错误率、时延、输入 token、成本，以及证据门未达成时一律 `inconclusive` 的判定。
- 汇总耗时 ≤ 数百毫秒/臂；`p25=p50=p75` 说明请求级分布极稳（见 §4）。

驱动（`internal/cli/live_team_cache_experiment_test.go`，`-tags live`）：

```bash
REASONIX_LIVE_CACHE_BASE_URL=... REASONIX_LIVE_CACHE_API_KEY=... \
REASONIX_LIVE_CACHE_MODEL=deepseek/deepseek-v4.1-flash \
REASONIX_LIVE_CACHE_ARM=B1-baseline-repeat \
REASONIX_LIVE_CACHE_JOURNAL=/tmp/cachelab-runs/B1.jsonl \
go test -tags live ./internal/cli/ -run TestLiveProviderCacheExperiment -v -count=1 -timeout 60m
```

另有一条**不花钱**的门控检查：`-run TestExperimentRecorderEndpointKeepsTheClientProtocol`，它比较"真实端点"与"环回端点"下客户端派生的 reasoning protocol。本次结果：**一致**，因此 recorder 端点不构成混杂。

## 4. 结果（2026-09-24，真实 Provider）

同一账号池条目、同一模型、串行；每臂首个请求都是全新 nonce，故首请求是真冷启动。**每个臂内所有请求的 request digest 完全相同（`uniq_hash=1`）**，即"冻结字节"这一前提在真实链路上成立。

| 臂 | 样本 | 有效 warm | token 加权率 | hit / miss tokens | prompt/请求 | miss/请求 | 时延 p50 / p95 | 质量 |
|---|---|---|---|---|---|---|---|---|
| B0-pilot | 9 | 8 | **92.39%** | 22528 / 1856 | 3048 | 232 | 534 / 851 ms | 9/9 |
| B1-baseline-repeat | 31 | 30 | **92.39%** | 84480 / 6960 | 3048 | 232 | 580 / 765 ms | 31/31 |
| B2-interval-short（2s） | 31 | 30 | **92.39%** | 84480 / 6960 | 3048 | 232 | 577 / 833 ms | 31/31 |
| B3-tool-schema | 31 | 30 | **92.21%** | 84480 / 7140 | 3054 | 238 | 660 / 844 ms | 31/31 |
| B3-system-tail | 32 | 29（另有 1 次 502 与 1 次客户端重试） | **92.09%** | 81664 / 7018 | 3058 | 242 | 706 / 1013 ms | 31 通过 / 1 失败（502） |
| B3-serialization | 31 | 30 | **92.39%** | 84480 / 6960 | 3048 | 232 | 612 / 892 ms | 31/31 |
| B4-ladder-small（8KB） | 31 | 30 | **92.39%** | 84480 / 6960 | 3048 | 232 | 669 / 940 ms | 31/31 |
| B4-ladder-mid（60KB） | 31 | 30 | **99.04%** | 518400 / 5040 | 17448 | 168 | 710 / 960 ms | 31/31 |
| B4-ladder-large（240KB） | 31 | 30 | **99.71%** | 2012160 / 5910 | 67269 | 197 | 947 / 1297 ms | 31/31 |

所有臂：错误 0（B3-system-tail 除外）、usage 缺失 0、**无 cache split 0**、账目不一致 0、混杂项 0。冷启动一律全 miss（B1: 0/3048；B4-mid: 0/17448；B4-large: 0/67269）。

跨运行复现：B1、B2-short、B3-serialization、B4-small 是四次独立运行，token 加权率与 hit/miss 总量**完全一致**（92.39%，84480/6960）。

**生产成员路径探针**（每次运行都执行）：真实成员 backend 的请求经 recorder 抵达同一 Provider，成员自己的 `.cache_requests.jsonl` 每次记录 2 条（`member records=2`），端点协议无差异（`confounds=[]`）。

## 5. 结论（按证据强度）

**已确认（真实 Provider 报告）**

1. **该 route/账号/模型下 Provider 稳定报告精确 cache split**：9 个臂、280+ 次真实请求全部拿到 `usage_split=true`，原始 key 逐条留存（anthropic 词表：`input_tokens`/`cache_read_input_tokens`/`cache_creation_input_tokens`/`output_tokens`）。因此"usage 语义是否可用"在本路由上是**可回答**的，`hit == 0` 在字节完全相同的重复请求上没有出现。
2. **字节完全相同的重复请求存在稳定上限，而不是 100%**：3K prompt 下 warm 命中 92.39%，每次固定 miss 232 tokens；首请求全 miss。
3. **"命中率随上下文变大而上升"可以完全由组成效应产生**：prompt 每请求 3048 → 17448 → 67269 tokens 时，token 加权率 92.39% → 99.04% → 99.71%，而**未命中 token 每请求几乎不变（232 → 168 → 197）**。这与历史账本"同尺寸桶仍有差异"的旁证方向一致：平均命中率的变化可以在没有任何"更少未命中工作"的情况下发生。
4. **间隔（0.5–1.4s vs 2.4–3.2s）没有差异**：B2-interval-short 与 B1 的率、hit/miss 完全一致（0.00pp）。
5. **单组件扰动均落在未命中侧**：工具描述加一句（+34 字节、+6 tokens/请求）与 system 尾部加一句（+66 字节、+10 tokens/请求）都**没有改变命中量**（命中始终 2816/请求），只把新增 token 全部计入 miss。同一 schema 的 JSON 键顺序重排（字节改变、长度不变）则**与基线逐项相同**：该 Provider 的缓存对这段前缀是内容/token 敏感的，对键顺序不敏感。
6. **错误/重试分类在真实数据上生效**：B3-system-tail 出现 1 次 502（25ms、无 usage），客户端对同一 turn 重发一次；recorder 分别记为"错误样本"与 `attempt=2` 的重试样本，二者都被排除出基线，重试成功，运行继续（未触发连续错误停止规则）。

**未决／不能声称**

- 第 3 条的机制（是分块粒度、还是网关每次附加的不可缓存尾部、还是两者叠加）**本次实验不能判定**；不应写成"每轮新增内容"或任一具体根因。
- 232/168/197 的 miss 不随尺寸线性，故不能用它推算生产成员的真实 miss 结构；生产成员的尾部内容与这里不同。
- B2-interval-long（300s）未跑，**不能**对 TTL 做任何>3.2s 的结论。
- B5/B6 未跑（需已批准的 route/账号对或第二个可追溯构建），本次**没有**任何 route 漂移、账号池、scope 或容量证据。
- 装配探针每次仅 2 次请求、使用 openai 词表形状的另一条适配路径，只能证明"成员路径可观测"，**不构成**成员级命中率基线。

## 6. 交接

**交 Agent A（数据可信度）**

- 本实验的 `usage_split` 判定与"无 cache split"分类可直接复用为契约参考：报告每臂都必须给出"有效 warm 数 / 排除明细"，本实现的对应规则是 `internal/cachelab/sample.go` 的 `Classify`。
- 请求 A 在成员级报表中显式保留"未命中 token/请求"这一列（不要只给命中率），否则第 3 条的组成效应会让任何候选改动看起来有效或无效。
- 冷启动、错误、重试、无 split 四类在本实验中都被排除后仍计数；A 的成员报表若把其中任一类归零，双方口径将不可比。

**交 Agent C（优化准入）**

- **C1 证据门状态：仍不通过**。本实验是**路由级**受控证据，不含成员级归属与覆盖率；A 的成员契约未冻结前不应据此改动生产请求构造。
- 可登记为候选的证据只有两条，且都必须先有 A 的成员级复现：(i) 稳定前缀之外的固定 miss 开销（本路由 168–242 tokens/请求）是否在生产成员请求中以同量级出现；(ii) 工具 schema 描述的**每 token 都落在未命中侧**——若有界剪枝能让该部分不再随每轮付费，其收益应按"每请求 miss token"衡量，而不是按命中率百分点。
- 反证条件：若 A 的成员报表显示 miss/请求与该尺寸无关、或 B3 类扰动在生产拼装下不再全落 miss 侧，则上述候选不成立。
- 质量护栏：本次质量检查只是"冻结任务要求的标记词是否原样返回"（合成任务，31/31 通过），**不是**任务质量度量；任何候选仍须自带质量/成本回归。

## 7. 未执行项与复跑方法

| 项 | 状态 | 原因 / 复跑 |
|---|---|---|
| B2-interval-long（300s × 30 warm） | 未执行 | 单臂约 2h35m 挂钟时间；命令同上，`REASONIX_LIVE_CACHE_ARM=B2-interval-long`（间隔已在臂表冻结，不可临时下调，否则条件未注册） |
| B5 route/account | 未执行（门控） | 需要已批准且可固定的第二个 route 或账号范围；驱动直接拒绝未门控的请求 |
| B6 client-build | 未执行（门控） | 需要第二个可追溯构建；本工作树未提交，无法给出两个可比构建 |
| 多轮重复正式臂（方差） | 未执行 | 四次独立运行的基线已逐项一致，但登记方差分析仍缺；建议每臂 ≥3 次重复后再定验收阈值 |

原始逐请求记录（JSONL，无 prompt/工具/凭据，268KB）位于 `/tmp/cachelab-runs/cachelab-<arm>-<runid>.jsonl`；请在使用前归档到有备份的位置，`/tmp` 可能被清理。

## 8. 验证与门禁

- `go test ./internal/cachelab/`：离线夹具自测全绿（journal 守卫与坏行、recorder 逐字节/raw usage/重试/错误/流式标记、分类顺序、统计与判定、夹具确定性与单组件扰动、臂注册完整性）。
- `go test -race -count=2 ./internal/cachelab/`：全绿。
- `go vet -tags live ./internal/cli/`：干净；`go test ./internal/cli/` 非 live 套件全绿。
- `go run ./tools/repolint`：本次新增文件**无新增违规**（仓库既有红项在他人已提交文件上，未触碰）。
- 未执行：`golangci-lint`（本机未安装）。`desktop/` 与 `sdk/` 未受影响，未运行。
- 凭据检查：运行后的 journal 不含 API key、不含夹具正文（grep 复核通过）。

## 9. 本次未做（明确边界）

- 未修改任何 provider-visible 请求字节、缓存策略、上下文裁剪/折叠策略、成员隔离或统计分母。
- 未新增生产遥测；`internal/cachelab` 只被 live 测试导入。
- 未修改 `TEAM_MEMBER_CACHE_OPTIMIZATION_EXECUTION_PLAN.zh-CN.md`、`TEAM_MEMBER_CACHE_DATA_AUDIT.md` 或其他 Agent 的文件。
- 未提交、未推送、未开 PR。
