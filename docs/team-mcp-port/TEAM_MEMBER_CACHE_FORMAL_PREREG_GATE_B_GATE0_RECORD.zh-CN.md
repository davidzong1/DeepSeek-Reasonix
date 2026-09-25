# 正式预注册门禁：Gate 0 排查记录（Agent B 侧）

> 角色：Agent B（正式采样与数据报告）。日期：2026-09-25。
> 依据：`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_3AGENT_PLAN.zh-CN.md` §4（Gate 0/1）、§6（B 工作包）、§8（硬防线）。
> 状态：**Gate 0 只读排查完成；Gate 1 判定 BLOCKED。未发起任何真实请求。**
> 计划 SHA-256：`f9706c66d43931d18cde76dd84b5c41fcfc6e23cbe6cb5d99bceb04524959646`；构建 `23c1e2d38fc8`。

## 0. 结论

| # | 项 | 判定 |
|---|---|---|
| G0-1 | 原冻结条件（`deepseek-v4-flash-roojin` + `anthropic/bfcb0811b1c8`）可执行？ | **否。两个字段自相矛盾**——route bucket 是池条目名的哈希，该条目解析到的是另一个 bucket。 |
| G0-2 | `anthropic/bfcb0811b1c8` 来自哪里？ | **`pilot-gw`**——pilot 驱动自造的临时池条目名，**不在操作者注册表中**。 |
| G0-3 | 六臂的 `anthropic/f119dfdb4214` 来自哪里？ | **`strata-gw`**——同一驱动的另一个自造名，同样不在注册表。 |
| G0-4 | 冻结账号 `deepseek-v4-flash-roojin` 是否仍可用？ | **注册表内仍在**（端点/model 与冻结描述一致）；**最后一条账本行 2026-09-24T07:11:56+08:00**，即冻结前一天仍在计费。可用性未验证（见 §5）。 |
| G0-5 | pilot 与 strata 实际用谁的凭据计费？ | **都不是 `roojin`**：两者都取自环境变量，其值等于池条目 **`deepseek-v4-flash-xie`** 的存储 key。**V1 预注册 §0「复用 roojin 端点与凭据」与事实不符。** |
| G0-6 | 能否离线确定 route bucket？ | **能**（A 的 `TestStrataPreflightRouteBucketIsKnownBeforeAnyRequest` 已证；本人独立复算一致）→ **无需 identity probe**。 |

**一句话**：V1 预注册的身份块不是"采样后漂移"，而是**冻结时就写错了**——它把一个测试夹具的自造条目名当成了账号池条目。因此"按原冻结条件重跑"（§1 路径 1）**在字面上不可执行**，必须走 §1 路径 2（另立 V2），而 V2 的身份须由负责人裁定（§5）。

## 1. 关键发现：route bucket 绑定池条目名

`memberProviderResolver.RouteBucket()`（`internal/cli/team_backend_build.go:145`）的输入是：

```
material = kind \x00 endpoint \x00 name \x00 proxyMode \x00 proxyType
bucket   = kind + "/" + hex(sha256(material)[:6])
```

其中 `name` 取自 `u.UserID`（`team_backend_build.go:60`），**不是** model、**不是**端点。因此"账号池条目"与"route bucket"在同一个冻结表里是**两个独立字段**，而 V1 §0 把它们当成了一致的一对。

独立复算（本人，非引用 A 的结论）：

| 池条目 | 端点 | route bucket |
|---|---|---|
| `deepseek-v4-flash-roojin` | `https://aiapi.lejurobot.com` | `anthropic/f3634ff267d6` |
| `deepseek-v4-flash-xie` | `https://aiapi.lejurobot.com` | `anthropic/744755eb058b` |
| `deepseek-v4-flash-lee` | `https://aiapi.lejurobot.com` | `anthropic/f92981c34582` |
| `pilot-gw`（自造） | `https://aiapi.lejurobot.com` | **`anthropic/bfcb0811b1c8`** ← V1 冻结值 |
| `strata-gw`（自造） | `https://aiapi.lejurobot.com` | **`anthropic/f119dfdb4214`** ← 六臂实测值 |

**穷举核验**：在「3 个 kind × 7 种端点拼写 × 12 个条目名 × 5 种 proxy mode × 3 种 proxy type = 23,520 组合」中，`anthropic/bfcb0811b1c8` **只有唯一解** `(anthropic, https://aiapi.lejurobot.com, pilot-gw, off, "")`；`anthropic/f119dfdb4214` 同样只有 `strata-gw` 一解。**操作者注册表中没有任何条目能产出这两个 bucket。**

与 A 的交叉验证：A 的测试日志打印 `that entry resolves to "anthropic/f3634ff267d6"`，与本人独立实现逐字节相同；A 的 `TestStrataPreflightRefusesTheFrozenConditionAsWritten` 与本人得出同一结论（**两条独立推导**）。

## 2. 计费凭据（附加发现）

| 事实 | 证据 |
|---|---|
| pilot / strata 驱动的凭据取自 `liveCacheCredentials()`（`ANTHROPIC_AUTH_TOKEN`），**不是**注册表条目 | `live_team_cache_pilot_test.go:112`、`live_team_cache_strata_test.go:271` 用 `creds.apiKey` 构造临时 `AgentUser` |
| 该环境变量的值**等于** `deepseek-v4-flash-xie` 的存储 key | 逐条目比对，**仅 `xie` 相同**（此处不复述任何密钥或其派生值） |
| 该环境变量的值**不等于** `roojin` 的存储 key | 同上 |
| 因此 `pilot-gw` / `strata-gw` 是**纯标签**，从不参与计费路由 | 两个名字在 `2026-09-19..24` 全部账本中**出现 0 次**；账本里的账号前缀只有 `deepseek-v4-flash-roojin`(5188) / `wan-gpt-5.6`(861) / `deepseek-v4-flash`(555) |

**含义**：V1 §0 写的"复用现有 `deepseek-v4-flash-roojin` 端点与凭据（不新增凭据）"，**凭据一半不成立**。真实被计费的账号是 `xie`，而记录里的 route bucket 反映的是一个不存在的名字。这正是 route bucket 作为"非识别标签"的**设计后果**：它证明了两次运行打在**同一个**（未注册的）标签上，但**无法**证明它们打在**同一个账号**上——而后者才是预注册要固定的东西。

## 3. 工作树基线（§3 写集冲突登记）

计划 §3 写的是"开工前先登记当前工作树已有的 2 个修改文件与 10 个未跟踪文件"。**该描述已过期**：`23c1e2d38` 已提交 **13 个路径**（2 个修改 + 11 个新增），其中 11 个新增 = 计划所数的 10 个未跟踪文件 **+ 计划文件自身**。即计划 §3 的计数与提交内容一致，只是**基线已前移**——现在那批文件全部是已跟踪文件。当前实际：

| 文件 | 归属 | 状态 |
|---|---|---|
| `internal/cli/team_preflight_gate.go` + `_test.go` | A | 未跟踪（新增，`_test.go` 外为生产 helper，**尚未被任何生产路径调用**） |
| `internal/cachelab/strata_recompute_test.go` | C | 未跟踪（`PART_C_STRATA_DIR` 门控，CI 不读实验数据） |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_A_PREFLIGHT.zh-CN.md` | A | **尚未交付** |
| `docs/team-mcp-port/TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_C_AUDIT.zh-CN.md` | C | **尚未交付** |

A 与 C 均在活跃写入（本人观察到 A 的文件在本轮内变更）。**本人未改动任何 A/C 文件**；B 侧本次只新增本文。

## 4. 对 Gate 1 的影响

计划 §4 Gate 1 第 1 项要求「A 交付 preflight 设计和测试，证明错误账号/route 会在发请求前失败」。**该证明已成立**，且它证明的正是**原冻结条件会被拒绝**——A 的 gate 对 V1 的冻结对是 fail closed 的。

因此：

- **Gate 1 = BLOCKED**（§4「任何一项未通过」）。**不发真实请求。**
- 计划 §1 路径 1（按原冻结条件重跑）**不可执行**，原因不是额度或凭据，而是**条件自身矛盾**。
- 计划 §1 路径 2（另立 V2）**是唯一可行路径**，且其前置"先记录原条件不可用的证据"**已由本文 §1/§2 完成**。
- **不得**把 V1 的 `bfcb0811b1c8` 改写进 V2 当作"就是它"——那等于用自造标签冒充账号（§8.1「期望身份必须来自 V2 单一冻结源」）。

## 5. 待负责人裁决（Gate 0 未决项）

| # | 决策 | 选项 |
|---|---|---|
| D-1 | **V2 冻结哪个身份** | 见下 §5.1 |
| D-2 | **驱动如何绑定凭据与标签**，使本次漂移不可复发 | 见下 §5.2 |
| D-3 | 是否允许一次限额 identity probe | 本排查判定**不需要**（§0 G0-6）；若负责人仍要求验证凭据可用性，须单独批准最大调用数并计入预算 |
| D-4 | 采样额度与时间窗 | V2 §3.2 沿用：S0–S1 ≤40M、含 S4 全部 ≤80M（计划 §6.2 粗估 60.1M） |

### 5.1 D-1：V2 的身份

| 选项 | 池条目 | route bucket | 含义 |
|---|---|---|---|
| **A（推荐）** | `deepseek-v4-flash-roojin` | `anthropic/f3634ff267d6` | **忠于 V1 真正想固定的账号**。端点、model、provider 与 V1 §0 描述**逐项一致**；V1 的 bucket 是夹具笔误。历史可比性最弱（与 pilot/strata 不同账号）。 |
| B | `deepseek-v4-flash-xie` | `anthropic/744755eb058b` | **与既有探索性数据同账号**（pilot + 六臂实际都走它）。但这不是 V1 写下的账号，且 V1 从不知情。 |
| C | — | — | **原条件阻断**：不出 V2，Gate 1 保持 BLOCKED，记录后停止。 |

**推荐 A 的理由**：预注册要固定的是**账号**，而账号在 V1 里写的是 `roojin`；bucket 是从一个从未打算成为账号的测试标签上抄来的。选 B 会把"实际上跑的是谁"变成"应该跑的是谁"，等于让一次事故改写研究设计。**但这是负责人的判断，不是 B 的**——若认为与既有数据同账号更重要，B 同样自洽，只是必须在 V2 里写明它与 V1 的偏离。

### 5.2 D-2：凭据绑定

| 选项 | 做法 | 代价 |
|---|---|---|
| **A（推荐）** | 驱动从**操作者真实注册表**解析冻结条目，凭据随条目走，**移除环境变量回退** | 驱动将读取操作者的 `agent_users.json`（含明文 key）。§8.1 最忠实。 |
| B | 驱动仍建临时 store，但**先用冻结条目的存储 key 比对**环境凭据，不一致即 fail closed | 不读真实注册表；仍要求操作者把正确 key 放进环境。 |
| C | 维持现状（纯环境变量） | **本次漂移可复发**——标签与凭据再次解耦。 |

## 6. 本轮门禁（本机实测，2026-09-25）

| 检查 | 结果 |
|---|---|
| `go build ./...` | **通过** |
| `go vet ./internal/cli/ ./internal/cachelab/ ./internal/team/ ./internal/provider/...` | **干净** |
| `go vet -tags live ./internal/cli/ ./internal/cachelab/` | **干净**（A 的 preflight 与两个 live 驱动在 `live` 标签下都能编译） |
| `go test ./internal/cli/ -run 'Cache\|Usage\|Team' -count=1` | **通过**（21.1s） |
| `go test ./internal/team/ ./internal/cachelab/ -count=1` | **通过**（8.6s / 0.04s） |
| A 的 `TestStrataPreflight*`（**当时 13 项**） | **13/13 通过**（A 其后增至 15 项，见 §8） |
| C 的 `TestRecomputeStrataArchive`（无归档时） | **跳过**（`PART_C_STRATA_DIR` 未设，CI 不读实验数据） |
| C 的 `TestRecomputeStrataArchive`（指向 09-24 归档） | **通过**，六臂逐项复现：S1a/S1b/S1c/S3-c2/S3-c3/S4 = 33/33/33/33/33/21 warm，hit 388,608 / 6,359,808 / 19,552,512 / 388,608 / 388,608 / 16,639,488，**无任何不变量违反** |
| `go run ./tools/repolint` | **clean (1146 baselined findings)**；`baseline.json` 未改动 |
| 账本完整性 | `2026-09-23/24.jsonl` md5 与 M0 冻结值**逐字节相同**（`98961189…` / `61eaaa3a…`），本轮全程只读 |
| 冻结测量层 | `stream_usage.go` md5 仍为 `71b7d3b79a4c6dfc74d1357b310190ff`，与 M0 一致 |

**A 的 13 项 preflight 测试本人独立复跑通过**（复核时段内有 13 项；A 随后增至 15 项，见 §8，本人已复跑 15/15），且其诊断输出与本人独立实现的路由哈希**逐字节一致**（§1）。C 的复算测试在真实归档上也独立复跑通过，数字与六臂报告**逐项相同**——这印证了"数字可信、身份不可信"这一判定：**数据没问题，问题是它属于哪个 route。**

## 7. 本文未做

- **未发起任何真实请求**；未构造、未使用任何替代凭据。
- 未改动 A/C 的文件、未改动 V1 预注册或任何历史归档。
- 未复述任何密钥、认证头或其派生值。
- 未提交、未推送、未开 PR。
- 未写 V2 预注册——其身份字段取决于 §5 D-1，须先裁定。

## 8. 补记（2026-09-25，Gate 0 之后）

**D-1 已裁定 = ①**（`deepseek-v4-flash-roojin` + `anthropic/f3634ff267d6`）。据此已交付：

| 交付 | 位置 |
|---|---|
| V2 预注册（身份块已锁 ①，含机器可读身份块） | `TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION_V2.zh-CN.md` |
| 正式驱动（B 写集，含 preflight 接线 + Effort 比对 + S0-canary） | `internal/cli/live_team_cache_strata_formal_test.go` |

**与 C 的交叉验证**：C 的 `..._GATE_C_AUDIT` **GC-1 / GC-2 独立证实了本文 §1 / §2**，且用了更强的证据形式（三点标定法解方程，而非穷举搜索）；GC-2 另指出全 9 月账本中 `pilot-gw` 与 `strata-gw` 各出现 **0 次**，与本文 §2 一致。**两条独立推导 + 三种证据形式得出同一判定。**

**本文 §1 的穷举核验已由 C 的解法加固**：C 不做搜索，而是把两份归档当作两个方程解出 bucket 的未知输入，再用**已标定**的输入去**预测**第三个条目的 bucket——预测命中，故"抄写不可靠"这一结论不依赖于搜索空间的完备性。

**驱动交付后，C 的 GC-11 已消解**（该条指出正式驱动当时不存在、V2 §6 读起来像已实现）。V2 §6 现已标注状态并逐行给出落点。
