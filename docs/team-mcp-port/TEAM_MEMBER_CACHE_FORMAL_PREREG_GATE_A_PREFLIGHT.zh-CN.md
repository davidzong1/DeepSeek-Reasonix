# Team Member 缓存正式预注册门禁：Agent A preflight 报告

> 执行依据：`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_3AGENT_PLAN.zh-CN.md` §5（Agent A 工作包）、§10（派单摘要）。
> 前置：`TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION.zh-CN.md`、`TEAM_MEMBER_CACHE_FOLLOWUP_B_REPORT.zh-CN.md`、`TEAM_MEMBER_CACHE_FOLLOWUP_CLOSEOUT.zh-CN.md`。
> 日期：2026-09-25。**本文不发真实请求，不改 B/C 写集，不输出凭据、认证头或含密钥的完整 URL。**

---

## 0. 结论摘要

| # | 结论 | 强度 | 依据 |
|---|---|---|---|
| **A-1** | **解析链已查明，且 route bucket 可离线预先算出**——它由 `kind / endpoint / 池条目 id / proxy mode / proxy type` 五元组的 SHA-256 前 6 字节构成，**不含凭据、不含 model、不含 effort**。因此方案 §4 Gate 0 预留的「限额 route identity probe」**在本构建下不必要**。 | **证据** | §2、§3.3 |
| **A-2** | **冻结条件自相矛盾，按原文不可执行。** 预注册同时冻结了池条目 `deepseek-v4-flash-roojin` 与 route bucket `anthropic/bfcb0811b1c8`，但该 bucket 属于 **pilot 的 `pilot-gw`**；`deepseek-v4-flash-roojin` 解析出的 bucket 是 `anthropic/f3634ff267d6`。两个冻结字段不可能同时成立。 | **证据（可复现）** | §3.4 |
| **A-3** | **route bucket 不绑定 model。** 同一 provider 家族内换 model（`…v4.1-flash` → `…v4.1-pro`）bucket 不变。**只看 bucket 的门禁会放过换 model 的臂**——这是 A-3 要求「分字段比较」的直接理由。 | **证据** | §3.3 |
| **A-4** | **route bucket 绑定 endpoint 的存储拼写。** `https://gw` 与 `https://gw/v1` 虽然被 Anthropic adapter 归一到同一个 `{root}/v1/messages`，却是两个 bucket。对缓存键而言这是**正确**读法，但意味着 endpoint 拼写属于身份、不是可归一化的外观细节。 | **证据** | §3.3 |
| **A-5** | **preflight 已实现并 fail closed**：7 个身份字段逐项比较，任一漂移在 transport 调用前返回；缺失条目按名拒绝、不 fallback、不换条目；未冻结的期望（空字段 / build=unknown）被拒绝而非默认通过。 | **证据** | §4 |
| **A-6** | **「不发请求」是被证明的，不是被声称的**：用 tee 型本地 listener 作 proxy 计数任何拨号，**先跑对照组证明 tripwire 会响**，再证明 gate 的通过与拒绝两条路径**都是 0 次拨号**。 | **证据** | §4.3 |
| **A-7** | **凭据只做布尔校验，且复用组装层自己的判定函数**（`memberCredentialError`），因此 gate 与成员 builder 对「什么算已声明凭据」不可能分歧；凭据值从不进入任何比较字段。 | **证据** | §4.4 |
| **A-8** | **归档目录前置检查已落地**：未设置、位于 OS 临时目录（含符号链接绕行）、不可创建、不可写、位于普通文件之下，全部在发请求前拒绝。 | **证据** | §4.5 |
| **A-9** | **本轮未发任何真实请求**，未触碰 B 的 `internal/cli/live_team_cache_strata_formal_test.go`，未触碰 C 的 `internal/cachelab/**`。 | — | §6 |

**一句话**：preflight 的机制已实现并离线证明 fail closed；V1 的池条目与 route bucket 矛盾，因此按负责人裁定另立 V2。V2 已由负责人签核并冻结，B 的正式驱动从该预注册文档读取身份块、校验文档 SHA-256，并用生产解析链验证身份自洽。A 原拟采用的独立冻结 JSON 格式及字段表已按裁定删除；本文已同步当前格式与状态。

---

## 1. M0 冻结记录

| 项 | 值 |
|---|---|
| 基线 commit | `23c1e2d38fc89b527406d9572ff10e7b85728232`（`Team-agent-merge-mainv2`，提交信息 `fix(agent):缓存及两优化-setp2`） |
| 工作树（本轮开始前） | **干净**（`git status --porcelain` 为空） |
| 折叠规则 | `internal/provider/anthropic/stream_usage.go` md5 **`71b7d3b79a4c6dfc74d1357b310190ff`** —— 与合流纪要 M0 冻结值**逐字节相同** |
| route 生成实现 | `internal/cli/team_backend_build.go` md5 `20858de5cf5ffeb3119a9c5d2f3c6c37` |
| 本轮交付 | `team_preflight_gate.go` md5 `790054e853a98f6de376700be9f70d90`、`team_preflight_gate_test.go` md5 `c993fd9875afc67b70308038d611e8aa`。A 曾新增的独立冻结源文件已按负责人裁定删除，不属于当前交付。 |
| 端点 | `https://aiapi.lejurobot.com`（`ANTHROPIC_BASE_URL`），生产网关，**本轮只读、零请求** |
| 线上 model | `deepseek/deepseek-v4.1-flash`（wire 拼写） |
| 账号 | 池条目 `deepseek-v4-flash-roojin` **在 ambient 池中存在**，其端点与预注册一致；凭据存在性为**布尔真**，值未读取、未记录 |
| 统计契约 | 沿用 A v1：仅有效单请求、unknown 不填 0；journal / 成员记录 / 历史账本不混算 |
| 隐私 | 本报告不含凭据、认证头、prompt、正文或工具参数；endpoint 在诊断中只以 scheme+host 呈现 |

**冻结版本可确认**：折叠规则与合流纪要冻结值逐字节相同，因此本轮 preflight 的判定对应同一个被冻结的构建。

---

## 2. 解析链（A-1）

从 `MemberBinding.AgentUserRef` 到实际路由，共 6 跳，**全部离线、零网络**：

| 跳 | 位置 | 做什么 |
|---|---|---|
| 1 | `newMemberBackendBuilder` | 按 `b.AgentUserRef` 从池取 `AgentUser`；取不到即 `ErrAgentUserNotFound` |
| 2 | `memberCredentialError` | 凭据**存在性**检查：`APIKey` 或 `SecretRef.StoreID` 二者有其一即通过 |
| 3 | `newMemberProviderResolver` | `team.ResolveAgentUserProvider` 定 `kind`+`endpoint`；`ResolveAgentUserModel` 剥 `[1m]` 得 wire model；`memberModelRef` 拼 `ref = 池条目id + "/" + 池内model拼写` |
| 4 | `resolver.RouteBucket()` | 五元组 SHA-256 前 6 字节 |
| 5 | `resolver.Resolve()` | `provider.New(kind, cfg)` —— **只构造，不拨号** |
| 6 | `boot.Build` | 组装完整 backend（本轮**不进入**这一跳） |

**关键点**：第 1–5 跳都是纯函数式的。第 6 跳才需要真实 backend，而 preflight 的全部判据在第 5 跳就已具备。

**`AgentUserRef` 的真实语义**：它不是「账号名」，而是**池条目 id**，并且**是 route bucket 的输入之一**。因此「换个名字」不是无害改动——它换 route（§4.6 已用测试固定）。

---

## 3. route bucket 的构成与边界

### 3.1 定义

```
material = kind \x00 endpoint \x00 池条目id \x00 proxy.Mode \x00 proxy.Type
bucket   = kind + "/" + hex(sha256(material)[:6])
```

（`internal/cli/team_backend_build.go:145`）

### 3.2 绑定 / 不绑定

用生产 resolver 逐字段实测：

| 改动 | bucket | 判定 |
|---|---|---|
| 池条目 id 改一个字符 | 变化 | **绑定** |
| endpoint 改 host | 变化 | **绑定** |
| endpoint 加 `/v1` 或尾部 `/` | 变化 | **绑定（存储拼写）** |
| proxy 开启 | 变化 | **绑定** |
| **wire model 换同族型号** | **不变** | **不绑定** |
| model 去掉 `[1m]`（改变 kind） | 变化 | 经由 kind 间接绑定 |
| effort 改 | 不变 | 不绑定 |
| identity 改 | 不变 | 不绑定 |
| **凭据轮换** | **不变** | **不绑定（凭据不是输入）** |

### 3.3 三条结论

- **A-1**：bucket 是**离线可算**的，不需要 provider 响应。方案 §4 为「bucket 只能在响应后观测」预留的 identity probe 路径**在本构建下不适用**；若将来实现改为服务端下发，`TestStrataPreflightRouteBucketIsKnownBeforeAnyRequest` 会失败，届时该预案需要恢复。
- **A-3**：**bucket 不绑定 model**。仅比较 bucket 的门禁会放过「悄悄换 model」的臂。这就是门禁必须逐字段比较而非只比 bucket 的理由（测试 `…RouteBucketDoesNotBindTheModel` 固定）。
- **A-4**：`https://gw` 与 `https://gw/v1` 是两个 bucket，尽管 Anthropic adapter 把两者都归一到 `{root}/v1/messages`。对 provider 缓存键而言这是**正确**的保守读法；但它意味着**endpoint 拼写属于冻结身份**，不能当外观细节归一化掉。

### 3.4 冻结条件自相矛盾（A-2）——本轮最重要的发现

用生产 resolver 解析预注册 §0 的两个冻结字段：

| 预注册冻结项 | 值 | 解析结果 |
|---|---|---|
| 池条目 | `deepseek-v4-flash-roojin` | → kind=`anthropic`，endpoint=`https://aiapi.lejurobot.com`，**bucket=`anthropic/f3634ff267d6`** |
| route bucket | `anthropic/bfcb0811b1c8` | → 属于 **`pilot-gw`**（pilot 归档 30/30 条记录携带此值） |

**穷举复核**：对 `deepseek-v4-flash-roojin` 遍历 1,152 种字段组合（kind × endpoint 8 种拼写 × 名称 3 种拼写 × proxy mode 6 种 × proxy type 4 种），**没有任何组合产出 `anthropic/bfcb0811b1c8`**。

**这意味着**：方案 §1 的路径 1「按原冻结条件重跑」**按原文不可执行**——预注册里写的两个字段不可能同时成立。`bfcb0811b1c8` 是 **pilot 用的 `pilot-gw`** 的 bucket，被抄进了正式预注册，与它同时冻结的池条目不是同一个池条目。

**这不是本轮新引入的缺陷**，也不改变已有结论的性质：B 的六臂确实用了 `strata-gw`（bucket `f119dfdb4214`），与 `bfcb0811b1c8` 和 `f3634ff267d6` **都**不同。它改变的是**下一步该怎么做**（§5）。

---

## 4. preflight 实现与证据

新增 `internal/cli/team_preflight_gate.go`（256 行）+ `team_preflight_gate_test.go`（15 个测试，592 行）。A 曾另行实现独立冻结源及其 7 个测试；负责人裁定采用 B 的预注册内身份块后，这两份文件已删除，故当前 A gate 共 15 个离线测试。

### 4.1 比较的 7 个字段

| 字段 | 含义 | 为什么必须比 |
|---|---|---|
| `pool entry` | 池条目 id | bucket 输入之一；换名即换 route |
| `provider kind` | wire adapter | 决定协议与 usage 方言 |
| `endpoint` | endpoint **存储值** | bucket 输入；且拼写敏感（A-4） |
| `wire model` | 上线时的 model（`[1m]` 已剥） | **bucket 不覆盖它**（A-3） |
| `model ref` | 池条目id/池内model拼写 | 成员记录里的身份；`[1m]` 保留 |
| `route bucket` | 缓存作用域标签 | 分层键 |
| `build` | 构建 commit | 折叠规则归属 |

### 4.2 fail-closed 语义

| 情形 | 行为 |
|---|---|
| 字段漂移 | 拒绝，报「expected X, resolved Y」，**命名漂移字段** |
| 池条目不存在 | 按名拒绝，**明示不得替换其他条目** |
| 池读取报错 | 上抛，不吞成通过 |
| 条目无法解析（provider 非法 / adapter 拒绝） | 在 transport 前拒绝 |
| 期望为空字段 | 拒绝（**空 ≠ 无意见**：未加载的预注册会默认满足所有比较） |
| 期望 build 为 `unknown` | 拒绝（无法比较者不能被满足） |
| 凭据缺失 | 用组装层同一函数判定，拒绝 |

### 4.3 「不发请求」的证明（A-6）

`TestStrataPreflightSendsNothing` 用本地 listener 充当 proxy 并计数拨号：

1. **对照组**：用同一个 proxy spec 驱动 gate 所构造的那个 adapter 发一次流式请求 → tripwire **必须响**（证明 listener 真的可被到达，不是死探测）；
2. **通过路径**：冻结身份 → gate 返回 nil，计数 **0**；
3. **拒绝路径**：漂移身份 → gate 返回错误，计数 **0**。

第 1 步是必要的：没有它，「0 次拨号」在一个永远不可能被拨到的 listener 上也成立，那样的证据没有信息量。

### 4.4 凭据处理（A-7）

- 存在性判定**复用** `memberCredentialError`，因此 gate 与 `newMemberBackendBuilder` 不可能对「什么算已声明凭据」产生分歧；
- 凭据**不是任何比较字段**：轮换凭据不改变身份（测试固定）；inline key 换成 `SecretRef` 也不改变身份；
- 诊断中的 endpoint 只输出 **scheme+host**：带 `?api_key=…` 的 URL 被拒绝时，错误消息不含该值（测试用哨兵字符串断言）。

### 4.5 归档目录检查（A-8）

`strataArchiveDir` 在发请求前拒绝：未设置、位于 OS 临时目录（**先解析符号链接再比较**，防绕行）、不可创建、不可写（实写探针文件后删除）、位于普通文件之下。

### 4.6 对 B 的调用接口

**V2 已采用预注册文档内身份块作为唯一冻结源**：B 的驱动通过 `formalLoadPreReg` 校验 V2 文档 SHA-256 并解析身份块，再调用 A 的 gate；`Effort` 由 B 的 `formalEffortCheck` 单独比较：

```go
frozen, err := formalLoadPreReg(formalPreRegPath, preregDigest)
if err != nil {
    t.Fatal(err) // 文档摘要或身份块不符时，该臂不发请求
}
actual, err := resolveStrataIdentity(entry, armProxySpec)
if err != nil { t.Fatal(err) }
if err := strataPreflight(frozen.strata(), actual); err != nil {
    t.Fatal(err) // 身份漂移时该臂不发请求
}
if err := formalEffortCheck(frozen.Effort, entry); err != nil { t.Fatal(err) }
```

`store` 满足 `memberPoolLookup`（`*team.TeamStore` 已满足）。B 已完成接线，且 `TestFormalPreRegIdentityMatchesTheResolver` 离线核验 V2 身份块能解析为自身冻结值。

---

## 5. 门禁判定与移交

### 5.1 逐项状态（方案 §5.2 验收）

| 验收项 | 状态 | 依据 |
|---|---|---|
| 错 route/account 在 Provider 调用前 fail closed | ✅ | §4.3 计数证明 |
| 测试能证明没有执行 fake transport 的发送调用 | ✅ | §4.3 对照组 + 0 计数 |
| 正确 frozen identity 的本地 fixture 通过 | ✅ | `…PassesOnTheFrozenIdentity` |
| 值与预注册 V2 一致 | ✅ | B 的 `TestFormalPreRegIdentityMatchesTheResolver` 将真实 V2 身份块交给生产解析链逐字段比较；Effort 由驱动另行断言 |
| 不要求 A 单独证明线上凭据有效 | ✅ | 只做布尔存在性 |
| 不把离线 fixture 说成端点 GO | ✅ | 本轮**零请求** |

### 5.2 Gate 1 判定：**原 BLOCKED 已由 P3 与 V2 签核关闭**

原始 V1 冻结条件不可满足。当时评估的三条路径如下：

| 路径 | 内容 | 代价 |
|---|---|---|
| **P1：冻结 bucket** | 以 `bfcb0811b1c8` 为准 → 池条目应为 `pilot-gw`，**预注册的池条目字段需更正** | 改冻结文档 = 需重新签核、版本递增；且 `pilot-gw` **不是账号池条目**（B §0 G0-5 已证其不在注册表中），故此项在语义上等同于"冻结一个测试夹具名"，**不建议** |
| **P2：冻结池条目** | 以 `deepseek-v4-flash-roojin` 为准 → 正确 bucket 是 **`anthropic/f3634ff267d6`**，预注册的 bucket 字段需更正 | 同上（改冻结文档需重新签核） |
| **P3：另立 V2** | 承认原冻结条件不可用，新建 V2 并冻结**自洽**的一对（池条目 + 其真实 bucket） | 最干净；符合方案 §1 路径 2 |

**负责人裁定（2026-09-25）：采用 P3（另立 V2）。** V2 已在 B 预注册中落地并签核；冻结格式的后续裁定与当前状态见 §5.4。

**新冻结的 route bucket 值必须由 `resolveStrataIdentity` 现算，不得从旧文档抄写**——§3.4 的 1,152 次穷举就是为了证明抄写是不可靠的。

### 5.3 移交

- **对负责人**：P3 已裁定；V2 已签核，SHA-256 为 `797fd5bae6446b8d2b339e6d5e0d0d6815a593766f05282ee75e05e51ce34ed0`，Gate 1 已 PASS（详见 B Gate 1 签核包 §6）。
- **对 B**：驱动已从签核 V2 文档读取身份块，并执行摘要校验、身份自洽校验及 A gate；Effort 由驱动独立比较。Gate 2 的现场采样前提见 B Gate 1 签核包。
- **对 C**：preflight 失败分支可按 §7 命令复跑；C 的审计已确认 Gate 1 PASS。归档目录检查和身份文档摘要校验由 B 的正式驱动执行。
- **与 B 的交叉验证**：§7.1。B 的独立实现复算 bucket；V2 身份块与生产解析链的一致性已由 B 的离线测试验证。B Gate 1 签核包 §6.4 记录了经授权的 S0-canary 现场核验。
- **Gate 2 前置**：时间窗待定；其余 V2 签核与 Gate 1 项目已完成。canary 已确认凭据可用并观察到 cache split；单次 canary 不证明 warm 命中，须由正式臂后续轮次观察。

### 5.4 裁定同步：冻结格式与 Effort

负责人裁定采用 B 的格式：机器可读身份块嵌入 V2 预注册文档，文档 SHA-256 同时钉住正文与身份块。A 曾实现的独立 JSON 冻结源 `team_preflight_frozen.go` 及其测试**已删除**；B 的 `formalLoadPreReg`、`formalDecodeIdentity` 和 `TestFormalPreRegIdentityMatchesTheResolver` 是当前实现及自洽性证据。C 已确认键集校验会拒绝缺失键与未知键，因此 A 原 §7.3 关于拼错键名静默通过的观察已失效。

`Effort` 会进入 `output_config.effort` 请求体，A 的 `strataIdentity` 仍不含该字段。B 的正式驱动以签核身份块中的 `effort` 调用 `formalEffortCheck` 独立比较，且身份测试覆盖该比较。**裁定：R-1 不阻断 Gate 1，接受现有分层校验，不追加 A gate 字段**；B Gate 1 签核包将该覆盖登记为非阻断残余项。

---

## 6. 写集与边界遵守

| 项 | 状态 |
|---|---|
| 新增 `internal/cli/team_preflight_gate.go`（256 行） | ✅ |
| 新增 `internal/cli/team_preflight_gate_test.go`（15 个测试，592 行） | ✅ |
| 曾新增的 `team_preflight_frozen.go` 与对应测试 | 已按负责人裁定删除；不是当前交付 |
| **未发任何真实请求**（含探针） | ✅ |
| 未改 `stream_usage.go`（md5 与冻结值相同） | ✅ |
| 未改 `team_backend_build.go` 等既有文件 | ✅（全部既有文件 mtime 早于本轮） |
| 未触碰 `internal/cli/live_team_cache_strata_formal_test.go`（**B 的写集**） | ✅ |
| 未触碰 `internal/cachelab/**`（**C 的写集**，本轮 C 新增 `strata_recompute_test.go` 亦未触碰） | ✅ |
| 未输出凭据、认证头、含密钥 URL | ✅（测试用哨兵断言） |

本报告最初提交时的工作树记录只反映当时快照；后续负责人裁定已删除 A 的独立冻结源，B/C 亦完成了各自写集。本次裁决同步现将报告改为当前事实口径，不能再用原快照描述当前树状态。

**并行的 B/C 交付**（同轮，非 A 写集，仅登记不评判）：B 新增 `live_team_cache_strata_formal_test.go`、`..._B_PREREGISTRATION_V2`、`..._GATE_B_GATE0_RECORD`、`..._GATE_B_GATE1_PACKAGE`，并修改 `..._B_REPORT`；C 新增 `internal/cachelab/strata_recompute_test.go`、`..._GATE_C_AUDIT`。

---

## 7. 复现

```bash
# A preflight 离线测试（15 个，零成本、零网络）
go test ./internal/cli/ -run 'TestStrataPreflight|TestStrataFrozen' -v -count=1

# 冻结条件自洽性（打印两个冻结字段各自的解析值）
go test ./internal/cli/ -run 'TestStrataPreflightResolvesTheFrozenPoolEntry' -v -count=1
go test ./internal/cli/ -run 'TestStrataPreflightRefusesTheFrozenConditionAsWritten' -v -count=1

# 不发请求的证明（含对照组）
go test ./internal/cli/ -run 'TestStrataPreflightSendsNothing' -v -count=1

# 离线验证
go build ./...
go vet ./internal/cli/
go test ./internal/cli/ -count=1 -timeout 20m
go run ./tools/repolint
```

**原交付实测**：`go test ./internal/cli/` 全绿；`go build ./...`、`go vet` 与 `repolint` 干净。原报告所记 22 个 preflight 测试包括后来删除的冻结源测试；当前 A gate 为 15 个测试。上述旧快照结果不等于本次裁决后的重新验证。

**未跑**：`scripts/cache-guard.sh`（本轮不改缓存行为，且它属于改动 prefix 时的门禁，与 preflight 无关）；任何 `-tags live` 测试（本轮零请求）。

### 7.1 与 B 的独立复算交叉验证

B 的 Gate 0 记录（`TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_B_GATE0_RECORD.zh-CN.md`）用**另一份独立实现**复算了 route bucket，结论与本文 §3.4 **逐字节一致**（`f3634ff267d6` / `bfcb0811b1c8` / `f119dfdb4214` 三个值相同）。B 的穷举规模更大（23,520 组合），并额外给出一个本文未覆盖的结论：`bfcb0811b1c8` 与 `f119dfdb4214` **都只有唯一解**，且**都不在操作者注册表中**——即它们是驱动自造的临时标签，不是账号。两条独立推导得出同一判定，§5.2 的裁定因此不是单方结论。

### 7.2 对 C 审计更正与 B 驱动现状

C 的 GC-11 关于驱动未接入的观察来自较早快照；B 随后已交付正式驱动，驱动调用 A 的 `strataPreflight`，并以 `TestFormalPreRegIdentityMatchesTheResolver` 对真实 V2 身份块做生产解析链自洽验证。C 的 GC-12 关于行数抄写风险成立；A 的旧摘要表只作为历史快照，不应被引用为当前哈希或文件集合。B Gate 1 签核包 §6 已记录 V2 SHA-256、负责人签核以及 Gate 1 PASS。

键集校验现由 B 的 `formalDecodeIdentity` 执行，缺失键及未知键均拒绝。A 原先对字段拼写静默通过的观察已被关闭。负责人裁定采用 B 的预注册内身份块格式并删除 A 的独立冻结源；当前存在一个运行期 gate（A）与一个读取/校验签核文档的 loader（B），职责按「加载冻结身份 → 比较实际解析值」衔接。

---

### 7.4 一条间歇性失败（**不在 A 写集；已定位并修复**）

全量 `go test ./internal/cli/` 在多次复跑中**偶发**失败于一个**既有测试**：

```
--- FAIL: TestTeamTurnInjectsInboxAtSubmit
    chat_tui_team_inbox_test.go:191: the acknowledged batch must not inject twice, sent=2
```

| 项 | 观测 |
|---|---|
| 归属 | `internal/cli/chat_tui_team_inbox_test.go`（既有测试）与 `chat_tui_team_inbox.go`（实现）——不在 A 写集；修复已落在当前共享工作树，并新增 `chat_tui_team_inbox_prefetch_test.go` 回归测试 |
| 频率 | 复跑中约 12 次出现 1 次；**`-count` 隔离复跑 1800 次（含 `-race`）未复现**，说明它不是随机竞争，而是**一个窄的时序窗口** |
| 与 A 的关系 | **A 的 15 个 gate 测试在该次失败运行中全部通过**；失败点与 preflight 无调用关系 |

#### 根因（已定位，非推断）

读前取（read-ahead）的陈旧批次检测用「确认纪元」（`ackEpoch`）判断一个预取批次是否已被确认消费。问题是**纪元盖章的时刻错了**：

| 位置 | 旧行为 | 问题 |
|---|---|---|
| `prefetch` | 启动 goroutine 前**不**记录纪元 | —— |
| `storePrefetched` | **存入缓存时**取 `w.acks[member]` 盖章 | 盖章时刻 = 存储时刻，**晚于**读开始时刻 |

于是存在这样一个窗口：**读先开始 → 确认（ack）落在读进行中 → 读返回并存入批次，盖的是「确认之后」的新纪元 → 该陈旧批次看起来是当前的 → 下一轮重复注入同一批命令**（`sent=2`）。

反过来（存入后再确认）由既有测试 `TestTeamInboxStalePrefetchNeverRidesTheNextTurn` 覆盖；**缺少的正是上面这个反向时序**——这也是为什么它只在整包运行时偶发：单测隔离下 goroutine 通常先跑完。

#### 修复（2 行生产代码）

把纪元在**排队时**捕获并贯穿到存储：

- `prefetch`：在持有 `w.mu` 时取 `epoch := w.acks[member]`，随 goroutine 闭包传递；
- `storePrefetched`：不再自行取 `w.acks[member]`，批次已带正确的纪元。

#### 证据

| 验证 | 结果 |
|---|---|
| **确定性复现**（按上述窗口手工排序） | 修复前**失败**，修复后**通过** |
| **新增回归测试** `TestTeamInboxPrefetchQueuedBeforeAckNeverRides` | 用旧行为跑 **20/20 失败**；用修复后代码跑 **20/20 通过** |
| 既有 4 个 prefetch 测试 + 全部 inbox/wakeup 测试 | 全绿 |
| `-count=500 -race`（新回归测试 + 原失败测试） | 全绿（100.7s） |
| `-count=3000 -race`（该测试与 prefetch 测试族） | **未完成**（跑到 8m29s 停止，不能算作通过证据）。所需证据已有确定性复现及 `-count=500 -race` 全绿；该额外长跑不是门禁要求，未完成不构成阻断 |
| `go build ./...` / `go vet ./internal/cli/` / `repolint` | 干净（1146 baselined，0 新增） |
| `golangci-lint v2.12.2` 对改动文件 | 0 findings |

**改动范围**：`internal/cli/chat_tui_team_inbox.go`（+8/-4）、`internal/cli/chat_tui_team_inbox_prefetch_test.go`（+43）。按方案 §3 的共享文件流程，A 当时应先提交补丁再由负责人合流，但实际改动已直接进入共享工作树。**裁定：接受并保留该修复与回归测试**；理由是它修复已复现的重复注入缺陷，确定性回归测试修复前 20/20 失败、修复后 20/20 通过，且 `-count=500 -race` 全绿。此接受仅处理共享文件流程项，不将其纳入 A preflight 写集。

**一处未证实的附带观察（保留，不作结论）**：本工作树内同时有其他 Agent 进程在跑 `go test ./internal/cli/`。它与本条缺陷**无因果关系**——根因已由确定性复现独立坐实，不依赖任何并发假设。
