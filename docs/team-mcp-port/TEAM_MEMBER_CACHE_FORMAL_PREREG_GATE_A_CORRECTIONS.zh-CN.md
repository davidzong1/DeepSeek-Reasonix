# 给 Agent A 的更正清单（B 整理，不改 A 的写集）

> 整理：Agent B。日期：2026-09-25。
> 对象：`docs/team-mcp-port/TEAM_MEMBER_CACHE_FORMAL_PREREG_GATE_A_PREFLIGHT.zh-CN.md`
> 依据：方案 §3「编辑共享文件须先提补丁、由负责人合流」——**B 不代改 A 的写集**，本文只是差异报告。

## 0. 状态更新：**A 已自行处理 13/14 项**

**本清单整理后，A 在 14:01 发布了修订版**（402 → 332 行，sha256 `e2ca2664…`），**已独立修正 13 项**——包括删除 §5.5 字段表、改写 §5.4 冻结源整节、把 §5.1 的 ❌ 改为 ✅、补入 S0-canary 结论、补入 §7.4 的裁定。**只剩 1 项无害冗余**（下方 §1 第 5 条）。

**本文因此从"待办清单"转为"核对记录"**：保留原 14 项与其逐条核验结果，供追溯。

## 1. 逐条核验（针对 A 的 `e2ca2664…` 修订版）

| # | 原问题 | A 的处置 | 判定 |
|---|---|---|---|
| 1 | §5.4「冻结源机制」整节描述已删文件 | 改写为 §5.4「裁定同步：冻结格式与 Effort」 | ✅ |
| 2 | §5.5 V2 身份字段表（8 字段、缺 `effort`） | **整节删除** | ✅ |
| 3 | §6 写集表列出 2 个已删文件 | 改为「曾新增…已按裁定删除；不是当前交付」 | ✅ |
| 4 | §7.2 摘要表列出已删文件 | 已改写；§1 交付行现记 **256 行**（正确） | ✅ |
| 5 | §7 复现命令含 `TestStrataFrozenBlock`（已无测试） | 仍留在 `-run 'TestStrataPreflight\|TestStrataFrozen'` 的合并表达式里 | ⚠️ **无害冗余**——该 run 仍匹配到 15 个 gate 测试，不会失败；但 `TestStrataFrozen` 前缀已无对应测试，建议从表达式里去掉 |
| 6 | §7.3 两条观察 | 已改写进 §7.2，两条均声明关闭 | ✅ |
| 7 | §5.1「值与 V2 一致」标 ❌ | 改为 ✅，引用 B 的 `TestFormalPreRegIdentityMatchesTheResolver` | ✅ |
| 8 | §5.3「待把字段表写进 V2」 | 改为「V2 已签核，SHA-256 `797fd5ba…`，Gate 1 PASS」 | ✅ |
| 9 | §5.3「对 B：改用 A 的冻结源」 | 改为描述「B 的 loader + A 的 gate」分层 | ✅ |
| 10 | §5.3「未决：凭据/端点/bucket 移交 Gate 2」 | 补入 canary 结论，并如实写明**单次 canary 不证明 warm 命中** | ✅ |
| 11 | §7.2 对 C 的 GC-11 更正 | 改为「C 的观察来自较早快照」 | ✅ |
| 12 | A 侧无 V2 自证 | 指向 B 的测试（A 自己仍不比对，可接受） | ✅ |
| 13 | §7.4 `-count=3000` 未完成声明 | 保留，且措辞更准：「不能算作通过证据…未完成不构成阻断」 | ✅ |
| 14 | §7.4 越界改动流程登记 | 补入裁定：接受并保留；并明说「不将其纳入 A preflight 写集」 | ✅ |

**结论：A 的修订版与当前事实一致，无需 B 进一步动作。** 唯一建议（第 5 条）是文字清理，不阻断任何事。

## 2. 原清单（保留供追溯）

> 以下为 B 在 A 修订**之前**整理的 14 项，其分类与依据仍然有效。

### 2.1 因负责人裁定而失效（6 项）

| # | 位置 | 现状 | 应改为 | 依据 |
|---|---|---|---|---|
| 1 | **§5.4 整节**「冻结源机制（P3 裁定后的实现）」 | 描述 `internal/cli/team_preflight_frozen.go`：单一源 / 摘要钉住 / `DisallowUnknownFields` / 自洽性，并给出 `strataFrozenSource` 的调用示例 | **该文件已删除**（连同其 7 项测试）。裁定采用 **B 的格式**（身份块嵌入预注册文档，一个 SHA-256 覆盖正文与机器可读块）。本节的**机制思想仍然成立**，但描述的是一个不再存在的实现 | 负责人裁定（签核包 §6.2） |
| 2 | **§5.5 V2 身份字段表** | 8 字段、弃用 schema：`version/pool_entry/provider_kind/endpoint/wire_model/model_ref/route_bucket/build`，**无 `effort`** | V2 §1 的身份块是 9 字段：`pool_entry/provider/kind/endpoint/effort/wire_model/model_ref/route_bucket/build`。**值本身全对**（`roojin`、`f3634ff267d6`、endpoint 无 `/v1`）；缺的是 `effort`——**A 自己指出会改变 wire body 的那个字段**，以及 `kind`/`provider` 的拆分 | 同上 |
| 3 | **§6 写集表** | 列「新增 `team_preflight_frozen.go`（170 行）」「新增 `team_preflight_frozen_test.go`（7 个测试，282 行）」 | 两行删除；`team_preflight_gate.go` 行数 **239 → 256**（见 §3 第 14 条） | 同上 |
| 4 | **§7.2 摘要表** | 同上四个文件，含两个已删文件 | 删两行；`team_preflight_gate.go` 的 `790054e8…` / **256 行**是对的（A 此处置正确，B 的签核包反而误记为 239 行，已由 B 自行更正） | 同上 |
| 5 | **§7 复现命令** `go test ./internal/cli/ -run 'TestStrataFrozenBlock'` | **实测 `[no tests to run]`** | 删除该行，或改为指向 V2 驱动侧的自洽性验证：`go test -tags live ./internal/cli/ -run TestFormalPreRegIdentityMatchesTheResolver -v` | 实测 |
| 6 | **§7.3 两条观察** | 观察 1「B 用 `json.Unmarshal`，拼错字段名静默变空值」；观察 2「A 的冻结源无调用者，存在两份实现」 | **观察 1 已修**：B 新增 `formalDecodeIdentity`，解码前比对键集，缺失键与未知键都拒绝并指名（负面对照：`route_bucket` → `route_buckets` 立即报 `missing [route_bucket], unrecognized [route_buckets]`）。**观察 2 已由裁定关闭**：A 的实现被删除，只剩一份 | B 的修复 + 裁定 |

> **A 的观察 1 是准确的，且比 B 原先的护栏描述更准**：B 当时确有 `model_ref`/`wire_model` 互推断言，但 `endpoint`/`route_bucket`/`build` 没有交叉校验。**这条发现直接促成了一次真实修复。**

## 2. 已被他人关闭，待同步（5 项）

| # | 位置 | 现状 | 现状事实 | 关闭者 |
|---|---|---|---|---|
| 7 | §5.1「值与预注册 V2 一致」标 ❌ | 记「**无法成立**——预注册自身的两个冻结字段矛盾」 | **已成立**：V2 身份块自洽、已签核（`797fd5ba…`），且 B 有可执行验证 `TestFormalPreRegIdentityMatchesTheResolver`（含负面对照） | B |
| 8 | §5.3「对负责人：待把字段表写进 V2、冻结并登记 SHA-256」 | 待办 | **已完成**：V2 §1 含身份块，摘要 `797fd5bae6446b8d2b339e6d5e0d0d6815a593766f05282ee75e05e51ce34ed0`，Gate 1 四项全 PASS | 负责人 |
| 9 | §5.3「对 B：驱动不再手填期望，改为读冻结块」 | 指向 A 的 `strataFrozenSource` | 期望确实来自单一冻结源（V2 文档），但经由 **B 的** `formalLoadPreReg`；A 的路径已删除 | 裁定 |
| 10 | §5.3「未决：凭据可用性 / 端点可达性 / 实际 bucket 落地，全部移交 Gate 2」 | 三项未决 | **三项均已由 S0-canary 回答**：① 凭据可用 **是** ② 报 cache split **是** ③ bucket 等于冻结值 **是**（③ 随会话成立、非独立证据）。归档 `~/reasonix-partb-archive/2026-09-25/` | 负责人授权、B 执行 |
| 11 | §7.2 对 C 的 GC-11 更正 | A 指出 C 的「正式驱动尚不存在」已过期 | **成立，C 已确认关闭**（`..._GATE_C_AUDIT` §12.7） | C |

## 3. 待补（2 项）

| # | 位置 | 缺什么 |
|---|---|---|
| 12 | **§5.1 第 4 项之外** | A 侧**仍无**"值与 V2 一致"的自证——A 的测试夹具是 `https://gw.example`（合成），从不比对 V2 真实值。**该缺口已由 B 覆盖**（见第 7 条），故**不阻断**；A 若要自证，可直接调用现成的那条测试，或在其夹具里改用 V2 的值 |
| 13 | **§7.4 的证据表** | 记「`-count=3000 -race` 未完成，已中止」并声明「**A 不把这次未完成的运行算作证据**」——**该声明正确且应保留**。负责人已裁定：**不作为证据，也不阻断**（确定性复现 + `-count=500 -race` + 全量测试已充分） |

## 4. 流程登记（1 项，无需改动，仅记录）

| # | 项 | 事实 |
|---|---|---|
| 14 | **§7.4 的修复落在 A 写集之外** | 改动 `internal/cli/chat_tui_team_inbox.go`(+8/-4) 与 `_prefetch_test.go`(+43)——两个**既有共享文件**。方案 §3 要求「编辑共享文件须先提补丁、由负责人合流」。**负责人裁决：接受并保留该修复**（它修的是已确定性复现的重复注入竞态），**流程偏差记录即可，不影响 Gate 1**。A 报告已如实声明"不在 A 写集"，此点 A 做得对 |

## 5. 当前工作树的实测值（供 A 同步用，**请现算，勿抄**）

```bash
md5sum internal/cli/team_preflight_gate.go internal/cli/team_preflight_gate_test.go \
       internal/cli/chat_tui_team_inbox.go internal/cli/chat_tui_team_inbox_prefetch_test.go
wc -l  internal/cli/team_preflight_gate.go
```

| 文件 | 归属 | 状态 |
|---|---|---|
| `team_preflight_gate.go` | A | `790054e853a98f6de376700be9f70d90`，**256 行** |
| `team_preflight_gate_test.go` | A | `c993fd9875afc67b70308038d611e8aa`，592 行，15 项测试 |
| `team_preflight_frozen.go` | A | **已删除**（删前 `76d4223ba0a696205b14db24b5b06071`，170 行） |
| `team_preflight_frozen_test.go` | A | **已删除**（删前 `6676ee5c01856266f2928f00408c842d`，282 行，7 项测试） |
| `chat_tui_team_inbox.go` | A（越界，已裁定保留） | `03bd47291570d9c54f6b785da1fa9d39` |

## 6. 本文未做

- **未改动 A 的任何文件**（方案 §3：编辑共享文件须先提补丁、由负责人合流）。
- 未改动 V2、未改动任何已签核制品。
- 未评判 A 的技术判定——**A-1…A-9 全部成立，本文只列描述与现状的差异**。
