# 实现方案文档 —— Team session 专用原子读写工具集（`atomic_read` / `atomic_write`）

> 状态：**已实施（Part A + Part B 均落地）**。本文件保留方案与施工边界，§5.5 与 §7 已回填实测数字，
> §9 登记实施期的偏离与未决项。代码见 `internal/tool/builtin/atomicfs_shared.go`、`atomicread*.go`、
> `atomicwrite*.go`、`internal/boot/tool_surface.go`、`internal/agent/path_bound_tools.go`。
> 基线：分支 `Team-agent` @ `2575fd7c9`（2026-09-22），实施于 `Team-agent-merge-mainv2`。
> 默认开关 `tools.atomic_fs = team`（按要求翻转）：依据是 §5.5 的**实测**前缀账单；live 计量仍未做（§5.5 末段）。
> 交付方式：**两个 Agent 并行**（Part A / Part B），文件级分工与冻结接口见 §4。
> 目标一句话：把 team session 里用 `bash`（`cat`/`sed`/`python - <<'PY'`/`>>`）做文件读写的习惯，
> 换成**两个参数结构化的原子工具**，同时拿到三样东西：**省 token**、**低学习成本**、**并发不丢不乱**。

---

## 0. 结论摘要

1. **现状**：team session 的文件操作几乎全走 bash（实测 2135 次 bash 调用里 801 次带写路径、1334 次是读类，
   621 次含重定向、64 次用 `python - <<'PY'` 改文件）。bash 在 team 里同时是**最贵**（命令文本本身就是载荷，
   实测最大 14.2KB 一条命令）、**最危险**（写整工作区租约、`write_paths` 子代理一律拒绝、审计回合被
   `ForEachMutation` 拒为「无法证明只读」）、**最不原子**（`sed -i`/heredoc 重写是读-改-写，无 CAS）的通道。
2. **现成的地基已经够用**：原子发布（`fileutil.AtomicWriteFileStrict`/`AtomicCreateFile`/`AtomicOverwriteFileStrict`）、
   观察与版本（`fileops.Store` + `DiskSnapshot`/`DiskHandleSnapshot`）、同进程互斥（`fileops.LockMany`）、
   CAS 前置（`editSource.requireObserved`）、有界回执（`post_write_receipt.go`）、读契约
   （`ExecuteRead`/`ReadEnvelope`）、路径级写租约（`HoldWriteForPaths`）与团队写令牌（L5）都已落地。
   本方案**只加两个工具与它们的引擎**，不重造基础设施。
3. **两个工具而不是一组**：`atomic_read`（auto/window/outline/delta/tail）与 `atomic_write`
   （create/replace/append/patch/delete + `ops` 事务）。实测 schema+描述
   **640B + 926B = 1566B（≈435 token）**，替换团队面上的 `read_file`+`write_file`+`edit_file`
   （**1055+290+500 = 1845B ≈ 512 token**），**每成员每轮净省 ~78 token**，且每次调用更省（见 §5）。
4. **原子性口径写死**：单文件「一次 `write(2)` 的 append」与「temp+fsync+rename 的发布」保证**永不交错、永不半写**；
   `replace`/`patch` 的**丢更新**用「读→CAS 校验→发布」把窗口压到 check→rename，并且在**同进程**（所有内建写工具、
   同 team 成员）由 `LockMany` 彻底关闭、**同工作区跨进程**由工作区写租约关闭；**外部非 Reasonix 写者**只能
   detect-and-refuse（§3.5 逐条标定，不吹牛）。
5. **拆成两半的轴是「基础设施+读」 vs 「宿主适配+写」**，不是「读路径 / 写路径」，理由见 §4.1：
   引擎与回执/锚点/CAS 是同一份真相，按读/写切必然在同一批文件上打架。两半**只有 1 个共享代码文件**
   （`internal/tool/builtin/workspace.go`，各加 1 行）与 1 张共享表格（本文件 §7），其余文件互斥（§4.5）。

---

## 1. 现状与实测

### 1.1 实测：team session 的文件操作几乎都走 bash

方法：直接读磁盘上的会话对象库（`~/.reasonix/projects/*/sessions-v4/.content-v1/objects`，5419 个消息对象，
两个真实项目 `-home-zwc-MPC_GPU`、`-home-zwc-cpp_ipc_dds`），统计 assistant 消息的 `tool_calls` 与
`role:"tool"` 的结果字节。命令类别是**启发式正则**（`sed -i`/`tee`/`>`/`>>`/`open(...,'w')`/`python3 - <<` 等算
「带写路径」），不是逐条人读，因此**只用于量级判断**。

| 项 | 实测 |
| --- | --- |
| bash 调用数 | **2135**（read_file 289、edit_file 114、write_file 26、multi_edit 0） |
| bash 里带写路径的 | **801**（含 621 条重定向、64 条 `python3 - <<'PY'` 改文件、3 条 `sed -i`） |
| bash 读类 | **1334**（命令中位 156B，结果中位 **1428B**） |
| 写类 bash 命令 / 结果 | 命令中位 **309B**，结果中位 520B；**最大 14224B 一条**（`cat > /tmp/.../probe.cc <<'EOF'`→结果 1796B） |
| `read_file` | 参数中位 72B，**结果中位 8130B / 均值 10486B / 合计 3.1MB** |
| `write_file` | 参数中位 **7458B**（`content` 中位 7122B，p90 28KB，max 46KB），结果中位 75B |
| `edit_file` | 参数中位 1075B（`old_string`+`new_string` 中位 955B），结果中位 963B |
| 被约束拒绝的结果行 | **84** 条：bash 60、`member_publish_deliverable` 20、`team_knowledge_recall` 3、`write_file` 1 |

两条读法：

- **bash 是事实上的文件 API**，而它的载荷就是命令文本本身：一次「重写整个文件」在 bash 里是
  `cat > f <<'EOF' …(全文)… EOF`（实测最大 14.2KB），在结构化工具里是 `edits:[{old,new}]`（中位 955B）。
- **读是整文件读**：`read_file` 结果中位 8.1KB、合计 3.1MB；而 team 成员大多数时候只改一个函数、
  只看一个窗口。窗口/大纲/增量读是唯一能把这块压下去的形状。

### 1.2 现有工具面的三个缺口（本方案要补的正是这三个）

1. **没有 append**：日志、证据、deliverable 片段只能靠 `>>` 或 heredoc，是 bash 里最容易写错的形状，
   且每次都要 shell 引号地狱。
2. **没有「只读窗口 + 增量」**：`read_file` 能 `offset/limit`，但没有**大纲优先**的 auto 形状、
   也没有「我上次读过、只给我变了的部分」。模型于是要么整读，要么 `sed -n`（在审计回合属于「无法证明只读」）。
3. **没有多文件一次提交**：改 3 个文件 = 3 次调用 = 3 次取租约（每个都可能在队友后面排队），
   模型回合数也 ×3。

### 1.3 可复用基础设施（**禁止重造**，逐条给了位置）

| 需要的能力 | 已有实现（直接用/包装） |
| --- | --- |
| 原子发布（temp+fsync+rename，含 EXDEV 退让与目录 fsync） | `internal/fileutil/atomicwrite.go`：`AtomicWriteFileStrict`、`AtomicOverwriteFileStrict`（保留 mode 与符号链接）、`ReplaceFile` |
| 非覆盖创建（link 发布，输掉竞争不覆盖） | 同文件 `AtomicCreateFile` |
| 崩溃注入（在持久化边界 panic） | 同文件 `CrashPoint`/`Crash`；用例先例 `internal/agent/save_crash_characterization_test.go`、`session_durability_test.go` |
| 观察与版本（不透明 version、native identity、overlay 路线） | `internal/fileops/observation.go`：`Store`、`Observation`、`DiskSnapshot`、`DiskHandleSnapshot`、`OverlayTargetWithIdentity` |
| 同进程按目标互斥（分片、稳定顺序） | 同文件 `LockMany`（`editSource` 用 `lockMutationPath` 包了一层） |
| CAS 前置与提交后观察 | `internal/tool/builtin/editsource.go`：`readEditSource`、`requireObserved`、`assertUnchanged`、`commitObservation` |
| 有界写回执（只回匹配/替换 span，2048B 封顶） | `internal/tool/builtin/post_write_receipt.go` |
| 读契约（窗口、编码、磁盘/overlay 双路线、证据裁剪） | `internal/tool/builtin/read_snapshot.go`（`ResolveReadPath`/`ExecuteRead`）+ `internal/agent/read_result_envelope.go` |
| 符号大纲（Go AST + 正则回退，文件名/行号） | `internal/tool/builtin/codeindex.go`（`code_index action=outline` 的采集器） |
| 编码检测/解码（GBK/UTF-16/BOM） | `internal/tool/builtin/encoding_helpers.go` + `internal/fileutil/encoding` |
| 路径级写租约 | `internal/workspacelease`：`HoldWrite` / `HoldWriteForPaths`（+ P0/L1 的等待方指名） |
| 团队写令牌（进程内、按路径相交排队） | `internal/agent/write_intent_token.go`、`write_intent_gate.go`、`internal/cli/team_write_token.go` |
| 写工具取锁的唯一决策点 | `internal/agent/tool_write_coordination.go`：`prepareWriteCoordination` → `acquireCallWriteGuard`（先令牌后租约） |
| 路径受限子代理的边界 | `internal/agent/path_bound_tools.go`：`pathBoundWriterNames`、`extractWritePathsFromArgs`、`BindWritePaths` |
| 租约现场观测（等待次数/时长/被挡域） | `internal/workspacelease/metrics.go`（M0）+ `control.Controller.WorkspaceLeaseMetrics()` |
| 会话数据/托管配置保护 | `writeFile` 的 `confineWrite(ctx, effectiveWriteRoots(...), guard, managed, path)`（`SessionDataGuard` / `ManagedConfigPaths`） |
| 真实 token 计量（live） | `cmd/e2ebench/meter.go`（代理侧读 provider usage，`prompt_tokens`/`completion_tokens`） |

### 1.4 现有 provider 工具面的字节账单（实测，3.6B≈1 token）

用一次性探针（`tool.Builtins()` 取 `Schema()`/`Description()` 的字节数，探针已删除）测得：

| 工具 | schema | 描述 | 合计 |
| --- | --- | --- | --- |
| `read_file` | 673B | 382B | **1055B ≈ 293 tok** |
| `write_file` | 179B | 111B | **290B ≈ 81 tok** |
| `edit_file` | 316B | 184B | **500B ≈ 139 tok** |
| `multi_edit` | 709B | 293B | 1002B（**默认面不可见**） |
| `delete_range` | 506B | 218B | 724B（默认面不可见） |
| `bash` | 1729B | 658B | **2387B ≈ 663 tok** |

默认 provider 面是 `CoreProviderToolNames()`（`internal/boot/agent_preset.go:60`）：
`bash, job_output, job_kill, read_file, view_image, edit_file, write_file, compress, use_capability, web_search`
—— `multi_edit`/`delete_range`/`glob`/`ls`/`grep`/`code_index` 都**不在**默认面上（需要 host 或配置显式加）。
这决定了本方案的两条硬约束：**新面必须自足（不能依赖模型另外记得 `multi_edit`）**、
**团队面替换必须净省**（§5.3）。

---

## 2. 工具架构总览

### 2.1 两个工具，一个契约

- `atomic_read`：**只读**、`ClassifyCall` 声明 `ReadOnly+ParallelSafe`（因此一个回合里的多次读可以进
  `execute_batch.go` 的并行批，`maxParallel=8`），走与 `read_file` **相同的读契约**
  （`ExecuteRead` + `ReadEnvelope` + `ResolveReadPath`），因此证据/分页/裁剪上不是二等公民。
- `atomic_write`：写。取锁**不由工具自己做**——它在 `internal/agent/tool_write_coordination.go` 的既有管线上
  被自动取好（进程内写令牌 → 跨进程租约），前提是 §4.3 的路径抽取能说出它要写哪些文件。
- 两个工具共享**同一份锚点语义**（`read-id`）与**同一份有界回执**风格。这是「省 token」与「防错乱」的汇合点：
  读留下锚点，写用锚点做 CAS，回执只说变了什么。

### 2.2 `atomic_read` 接口规范（**冻结**，实施时逐字节照抄）

schema（257B）：

```json
{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string","enum":["auto","window","outline","delta","tail"]},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1},"since":{"type":"string"}},"required":["path"]}
```

描述（383B）：

```
Read a file cheaply. mode=auto|window (numbered lines, offset/limit; auto adds an outline first when the file is large), outline (section/symbol map only), delta (only hunks changed since a read_id you already have — use after you or a teammate edited), tail (last lines). Every read records the snapshot the write tool builds on; results are bounded and mark what was not delivered.
```

行为（写死）：

| mode | 语义 | 默认上限 |
| --- | --- | --- |
| `auto`（缺省） | 文件 ≤ `atomicAutoWholeMaxLines`(400) 行 → 等价 `window` 全量；否则 → `outline` + 头 `60` 行 | 结果 ≤ `atomicReadBudgetBytes`(16KB) |
| `window` | 编号行 `offset/limit`（缺省 offset 0、limit 2000，与 `read_file` 同默认） | 同上 |
| `outline` | 符号/章节地图（`func`/`type`/`class`/markdown 标题/缩进块），每项 `行号→名字`；**复用 `codeindex.go` 的采集器**，非 Go 文件用其正则回退，全失败则退化为「前 40 行 + 总行数」 | ≤ 4KB |
| `delta` | 需要 `since`；只回自那次读以来变化的 hunk（±3 行上下文），无变化回一行 `unchanged` | ≤ 8KB |
| `tail` | 末尾 `limit` 行（缺省 80） | ≤ 8KB |

结果头（每个结果第一行，也是**唯一的 `read-id` 出口**）：

```
read r-1a2b3c4d <path> [<mode> 120-180/2140] (2140 lines, 87KB)   ← 有 id 才有 since 可用
```

`read-id` 由宿主从**已经观察到的版本**派生（`sha256(route|path|version)` 取前 8 字节十六进制，
与 `internal/agent/read_result_envelope.go:9` 的 `readWindowID` 同形），**绝不包含绝对路径以外的宿主内部信息**，
且**绝不作为权限判定依据**（只用于 CAS 与提示）。

### 2.3 `atomic_write` 接口规范（**冻结**，实施时逐字节照抄）

schema（492B）：

```json
{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string","enum":["create","replace","append","patch","delete"]},"content":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":["old","new"]}},"range":{"type":"object","properties":{"start":{"type":"integer"},"end":{"type":"integer"}},"required":["start","end"]},"ops":{"type":"array","items":{"type":"object"}}},"required":["path"]}
```

描述（434B）：

```
Write a file atomically and cheaply. mode=create (fails if the file exists), replace (whole content), append (add at EOF; use instead of echo >>), patch (edits:[{old,new}] or range:{start,end}+content; use instead of sed -i), delete. Refuses with the changed hunks when the file differs from what you read; no forced overwrite. ops:[{path,mode,...}] applies several files as one transaction. Returns a bounded receipt, never the file.
```

模式矩阵（写死；`path`/`since` 对所有模式有效，`since` 缺省表示「用本会话最后一次读留下的锚点」）：

| mode | 需要 `content`/`edits` | 需要先读过 | 原语 | 失败形状 |
| --- | --- | --- | --- | --- |
| `create` | `content` | **不需要**（若本会话观察为 Present → 拒） | `AtomicCreateFile`（link 发布） | 存在 → `exists`（不回显现有内容） |
| `replace` | `content` | **需要**（或给出 `since`） | `AtomicOverwriteFileStrict` | CAS 失配 → 变更 hunk + 恢复指引 |
| `append` | `content` | **不需要**（append 不丢更新） | `O_APPEND` 单次 `write(2)` + `fsync` | 单次写入超上限（`atomicAppendMaxBytes` 4MB）→ 明确拒绝并给出 `replace/patch` 出路 |
| `patch` | `edits[]` **或** `range`+`content` | **需要**（或给出 `since`） | 读→splice→`AtomicOverwriteFileStrict` | 0 命中/多命中 → 与 `edit_file` 同形状的 `old_string not found/unique` 错误；CAS 失配 → hunk |
| `delete` | 无 | **需要** | `os.Remove`（unlink 本身原子） | 不存在 → `absent`（幂等：已不存在视为成功但如实说明） |
| `ops` | `ops:[{path,mode,content|edits|range}]` | 每个子项按其模式 | prepare→commit（§3.3） | 部分提交 → 逐路径 `committed`/`not committed` + `txid` |

**安全平价（不可协商）**：`atomic_write` 必须走与 `write_file` 完全相同的边界序列
（`writefile.go:56-80` 的顺序，`confineWrite` 在 `:68`）：`resolveIn` → `confineWrite(ctx, effectiveWriteRoots(ctx, rootSet, roots), guard, managed, path)`
→ 目标锁（多路径用 `fileops.LockMany`）→ `readEditSource`/`requireObserved`。少任何一条都是**绕过
`SessionDataGuard`/托管配置/写根的回归**，用例必须把它钉住（§6.2 第 9 条）。

### 2.4 结果格式（都是「有界回执」，永不回文件）

- 读：`read <id> <path> [mode a-b/n]` + 内容（编号行）+ 一行边界说明（`…[120 more lines in this window]…`）。
- 写：`<verb> <path> (+12 -3) 2140→2149 lines`，`patch/replace` 附 `post_write_receipt.go` 的
  **匹配/替换 span**（沿用 2048B 封顶），`ops` 附逐路径一行。**永不回显文件内容**——
  被拒的 `bash` 调用至少会回 `blocked:` 一行，而成功的 bash 重写会回显一大片；本工具连成功的都不回显。

### 2.5 错误码与恢复协议（统一走 `tool.OperationError` + `Recovery`）

| 场景 | Code | `Recovery` 文案（要点） |
| --- | --- | --- |
| 未读过就 `replace/patch/delete` | `FSNotObserved` | 「先 `atomic_read` 任意窗口，再重试」 |
| `since`/锚点与磁盘不一致 | `FSStaleVersion` | 「**附变更 hunk**；重读后重试（或改用 append/patch 的新锚点）」 |
| `create` 撞已有文件 | `FSStaleVersion`（沿用「被抢了」语义） | 「读现状后用 `patch`」 |
| `old` 不唯一/不存在 | 复用 `oldStringNotUniqueError`/`oldStringNotFoundError` | 与 `edit_file` 完全一致（模型已学会） |
| 路径越界/会话数据 | 复用 `confineWrite` 的错误 | 不改写 |
| 追加超上限 | `FSNotObserved` 之外新加 `FSTooLarge`（若枚举齐全，落地时按枚举收敛） | 「用 `replace` 或分片追加」 |

---

## 3. 原子性实现原理

### 3.1 单文件模式 → 原语 → 崩溃/并发语义

| 模式 | 原语 | 崩溃后可见状态 | 并发对手看到 |
| --- | --- | --- | --- |
| `create` | `AtomicCreateFile`（tmp + `os.Link`） | 文件不存在或完整 | 两个创建者只有一个成功（link 语义） |
| `replace`/`patch` | tmp（同目录、`CrashPoint` 注入点）→ `AtomicOverwriteFileStrict` | 旧内容或**完整**新内容 | 永不半写（rename 原子） |
| `append` | `O_APPEND|O_CREATE`，**一次 `write(2)`**，随后 `fsync` | 前缀完整（可能少最后一段） | 与其它 O_APPEND 写者**串行不交错** |
| `delete` | `os.Remove` | 存在或不存在 | 无半删 |

`append` 的关键点：**一次调用 = 一次 `write(2)`**（超出上限就拒绝，不拆批），这样多个成员的并发 append
得到的是**全序拼接**而不是交错；若改成分块写，交错立刻回来——这一条必须写成用例（§6.2 第 2 条）。

### 3.2 CAS/冲突协议（丢更新怎么被挡住）

```
读（atomic_read）→ 记录 anchor{route, path, version, contentHash, read-id}（fileops.Store）
写（replace/patch）→ 取锁 → 重新读源（同一 overlay/disk 路线）→
  ① 与 anchor 的 version/contentHash 比对；不等 → 返回 FSStaleVersion + 变更 hunk（不改文件）
  ② 在**读到的字节**上 splice（未改动区域按构造逐字节不变）
  ③ AtomicOverwriteFileStrict 发布 → commitObservation（新版本）
```

**为什么这比 `sed -i` 强**：`sed -i` 是「无锚点的读-改-写」，队友在你读完之后写的任何东西都会被静默覆盖；
本协议在 ① 就把它变成一条**带 hunk 的拒绝**。

### 3.3 多文件 `ops`：prepare → commit → journal

1. **prepare**：按路径排序取 `fileops.LockMany`（防死锁）→ 逐个子项做 §3.2 的 ①②（读、CAS 校验、
   在内存里拼出新字节）→ 逐个子项写 tmp + fsync（**此时磁盘未变**）。任一子项失败 → 全部放弃，
   返回失败子项与恢复指引（未提交任何东西）。
2. **commit**：按路径排序逐个 rename。rename 是原子的，但**多个 rename 之间不是**——所以：
3. **journal**：commit 前把计划（`txid`、逐路径的目标哈希）写到 `.reasonix/atomic-writes/<txid>.json`
   （`/.reasonix/*` 已在 `.gitignore:53` 忽略；用例要断言它不会出现在 `git status`），commit 后补写实际结果。
   中途 rename 失败 → 返回 `committed: [a, b]` / `not committed: [c]` + `txid`，模型只需对剩余项重发。
4. **诚实声明**：不做「跨文件可见性原子」的假承诺（那需要 fs 事务或写时复制目录）。`ops` 的承诺是
   **准备阶段零副作用、提交阶段逐文件原子、部分提交必被如实报告**。

### 3.4 锁阶梯（谁在哪一层互斥）

| 层 | 机制 | 覆盖谁 |
| --- | --- | --- |
| L0 进程内目标锁 | `LockMany`（编辑顺序稳定） | 同进程所有内建写工具、同 team 成员、子代理 |
| L1 锚点/CAS | `fileops` 版本/内容哈希 + `requireObserved` | 任何外部写者（**检测**而非阻止） |
| L2 工作区写租约 | `HoldWriteForPaths(scope)`（由 agent 管线在 Execute 前取） | 跨进程的其他 Reasonix 会话（同工作区） |
| L3 团队写令牌 | `write_intent_token`（路径相交才排队） | 同 team 成员之间（先令牌后租约） |

工具自己**不取 L2/L3**：那是 `tool_write_coordination.go` 的唯一决策点，工具一旦自己取锁就会变成第二个真相来源。

### 3.5 保证强度的诚实标定

| 性质 | 强度 | 依据 |
| --- | --- | --- |
| 内容交错 / 半写 | **不可能** | tmp+fsync+rename；append 是单次 `write(2)` |
| 同进程丢更新 | **不可能** | L0 目标锁 + L1 锚点 |
| 同工作区跨进程丢更新 | **不可能**（对走租约的写者） | L2 租约（写者取锁在 `prepareWriteCoordination`） |
| 外部写者（用户编辑器/非 Reasonix 进程）丢更新 | **检测并拒绝**，非阻止 | 窗口 = CAS 校验 → rename（微秒级）；残留风险在 §9 登记 |
| 跨文件 ops 的可见性原子 | **不承诺** | §3.3 第 4 条 |
| 符号链接/权限/编码保持 | **保持** | `AtomicOverwriteFileStrict` 保留 mode+链接；`readEditSource` 保留编码 |

---

## 4. 并行拆分策略

### 4.1 拆分原则：为什么是「基础设施+读 / 宿主适配+写」

- 锚点、CAS、回执、hunk 生成、编码/overlay 路线是**一份真相**，读写都要用。若按「读路径 / 写路径」切，
  两个人必然同时改 `atomicfs_shared.go`（或各自复制一份，制造第二真相）。
- 按「基础设施 / 宿主适配」切，天然分出一条**依赖边界**：A 先把共享底座 + 只读工具落地并冻结，
  B 在宿主侧（surface、租约、路径抽取、提示、度量）与写引擎上工作，**B 的 B0 阶段零依赖可立即开工**。
- 结果：**互斥文件 = 除 `workspace.go` 外全部**；共享文件 1 个（各加 1 行）；共享表格 1 张（本文件 §7）。

### 4.2 Part A —— 基础设施与读路径

**职责**：冻结的共享底座（锚点/CAS 判定/hunk/回执/错误）+ `atomic_read` 全模式 + 它的读契约实现 + 读侧度量探针。

**新增文件（全归 A）**：

| 文件 | 内容 |
| --- | --- |
| `internal/tool/builtin/atomicfs_shared.go` | `atomicAnchor` 类型与捕获/校验；`atomicReadID`；`atomicDeltaHunks`；`atomicReceiptLine`；`atomicConflict`（带 hunk 的 `FSStaleVersion`）；UTF-8 安全裁剪 |
| `internal/tool/builtin/atomicfs_shared_test.go` | 底座单测：锚点相等/失配、hunk 生成（含空变更/全变更/超预算裁剪）、回执字节预算、read-id 稳定性与不含宿主内部信息 |
| `internal/tool/builtin/atomicread.go` | 工具本体：schema/描述（§2.2 逐字节）、`Execute`+`ExecuteRead`+`ReadEnvelope`+`ResolveReadPath`、`ClassifyCall`、`PlanModeSafe`、`SnipHint` |
| `internal/tool/builtin/atomicread_outline.go` | `outline` 渲染（复用 `codeindex.go` 采集器；非 Go 回退）与 `delta` 渲染 |
| `internal/tool/builtin/atomicread_test.go` | 模式矩阵：auto 阈值两侧、window 默认/越界、outline 有符号/无符号文件、delta 命中/未命中/`since` 非法、tail、编码文件（GBK/UTF-16）、二进制拒绝、结果头含 read-id |
| `internal/tool/builtin/atomicread_cas_test.go` | 「读-外改-再读」：`delta` 只回 hunk；失配报 `FSStaleVersion`；观察被记录/被 `Forget` |
| `internal/tool/builtin/atomicread_race_test.go` | 读与 replace 并发循环（`-race -count=2`）：每次读到的字节必须是**旧或新的完整哈希**之一，永不撕裂 |
| `internal/tool/builtin/atomicread_measure_test.go` | 读侧夹具度量（§5.4）：`bash cat` / `read_file` / `atomic_read` 三种形状的 schema+参数+结果字节，报中位与 p90 |

**修改文件（additive-only）**：

| 文件 | 改动 |
| --- | --- |
| `internal/tool/builtin/workspace.go` | `overrides` 映射加 1 行 `"atomic_read": atomicRead{workDir: w.Dir, paths: w.ReadPaths, forbidRoots: forbidRoots, overlay: w.FileOverlay}`（**共享文件，见 §4.5**） |
| `internal/tool/builtin/codeindex.go` | 仅当采集器不能直接被调用时，抽出**纯函数**符号列表助手（行为不变，`code_index` 既有用例必须全绿） |

**验收**：`gofmt` 空；`go build ./...`、`go vet ./...` 空；`go test ./internal/tool/builtin/ -count=1` 全绿；
新用例 `-race -count=2` 绿；**红→绿至少 3 组**（§6.3）；度量表产出真实数字。

### 4.3 Part B —— 宿主适配、写路径与度量

**职责**：`atomic_write` 全模式与 `ops` 事务 + 安全平价 + 宿主接线（路径抽取/租约与令牌/surface/配置/提示）+ 写侧度量与收益报告。

**新增文件（全归 B）**：

| 文件 | 内容 |
| --- | --- |
| `internal/tool/builtin/atomicwrite.go` | 工具本体：schema/描述（§2.3 逐字节）、六模式、`confineWrite` 平价、编码保持、overlay 路线、`Preview`（审批 diff）、`DeclareWriteAccess`、`DeclareEvidenceTarget`、`SnipHint`、`PlanModeSafe=false` |
| `internal/tool/builtin/atomicwrite_ops.go` | `ops`：prepare/commit/journal（§3.3） |
| `internal/tool/builtin/atomicwrite_test.go` | 模式矩阵 + 拒绝形状 + 回执（`create` 撞文件/`append` 不读也成功/`patch` 0 命中与多命中/CAS 失配 hunk/`delete` 幂等/编码保持/overlay 路线） |
| `internal/tool/builtin/atomicwrite_ops_test.go` | 事务：prepare 阶段失败零副作用、部分提交逐路径报告、journal 落盘且不入 git、按同一个 `txid` 重发剩余项 |
| `internal/tool/builtin/atomicwrite_concurrency_test.go` | §6.2 的 1–5 条（同进程 N 写者、32 路 append、re-exec 多进程、崩溃注入、撕裂读循环） |
| `internal/tool/builtin/atomicfs_write_measure_test.go` | 写侧夹具度量（§5.4） |
| `internal/agent/atomicfs_lease_scope_test.go`（命名以落地时为准：`atomicfs_lease_scope_test.go`） | `pathBoundWriteScope` 对 `atomic_write` 的路径抽取（含 `ops`）、租约域=文件而非整工作区、`write_paths` 越界拒绝、与 `bash` 的整工作区域对照 |
| `internal/cli/team_atomic_fs_surface_test.go` | 团队面含新对且**不含**旧三件；非团队面逐字节不变 |

**修改文件（全归 B）**：

| 文件 | 改动 |
| --- | --- |
| `internal/tool/builtin/workspace.go` | `overrides` 加 1 行 `atomic_write`（**共享文件，见 §4.5**） |
| `internal/agent/path_bound_tools.go` | `pathBoundWriterNames` += `atomic_write`；`extractWritePathsFromArgs` 支持 `path` 与 `ops[].path`（返回全部目标） |
| `internal/boot/boot.go` + `internal/boot/tool_surface.go` | 新增 `Options.ProviderVisibleTools []string`（nil = 现状），`applyUnifiedProviderToolSurface` 与其取**交集**（永不扩权） |
| `internal/cli/team_backend_build.go` | 团队 build（leader + member）注入新对为 `ExtraTools` 并传 `ProviderVisibleTools`（core 去掉 `read_file/write_file/edit_file` + 新对 + `bash` 等其余不变） |
| `internal/config`（`tools.atomic_fs = "off" \| "team" \| "all"`） | 落 `team` 为缺省（作用域限团队构建）；`off` 保留为显式回退 |
| `internal/team/role.go` + `internal/team/role_write_discipline_test.go` | L4 的纪律文本里的 `multi_edit`/`edit_file` 改指新对（只在面真的替换后改；该文本有逐字节稳定断言，两处一起改） |
| `team/skills/base/member/SKILL.md`、`team/skills/base/leader/SKILL.md` | 各加 ≤1 行「文件读写用 `atomic_read`/`atomic_write`，不要用 `cat`/`sed`/`>>`」（合计新增 ≤120B） |
| 本文件 §7 | 追加 B 的节点行与证据（**共享表格**） |

**验收**：同 A 的门禁 + `go test ./internal/agent/ ./internal/cli/ ./internal/boot/ ./internal/config/ -count=1` 全绿；
并发/崩溃用例（子进程）在 `-race -count=2` 下绿；度量表 + 「每成员每轮前缀字节」对照表落进本文件 §7。

### 4.4 依赖边界（A → B 的冻结接口）

A 在 **A1** 落地 `atomicfs_shared.go` 后，以下签名与语义冻结（B 只调用，不改）：

```go
// internal/tool/builtin/atomicfs_shared.go —— Part A 所有；Part B 只读调用。
type atomicAnchor struct {
    Path, Route           string // Route: "disk" | "overlay"
    ReadID                string // "r-1a2b3c4d"
    Version               string // fileops.Version（不透明）
    ContentHash           string // 宿主看到的字节的 sha256
    Bytes, Lines          int
    Missing               bool   // 观察为 Absent
}

// atomicCapture 走与 read_file 相同的路线（overlay 优先，磁盘兜底）取锚点，并把观察写进 fileops.Store。
func atomicCapture(ctx context.Context, overlay FileOverlay, path string) (atomicAnchor, error)

// atomicAnchorFor 解析写方锚点：since=="" 用本会话最后一次观察；否则用 given read-id
// （只按 "r-xxxx" 形状解析，内容仍以宿主重算为准——模型给的 id 永远不被信任）。
func atomicAnchorFor(ctx context.Context, overlay FileOverlay, path string, since string) (atomicAnchor, error)

// atomicRecheck 写前重读源并与锚点比对；不匹配返回 *tool.OperationError{Code: FSStaleVersion}（含 hunk）。
func atomicRecheck(ctx context.Context, overlay FileOverlay, path string, anchor atomicAnchor) (content []byte, err error)

func atomicReadID(route, path string, version fileops.Version, content []byte) string
func atomicDeltaHunks(oldBytes, newBytes []byte, budget int) []string
func atomicConflict(path string, shown, current []byte, anchor atomicAnchor) error
func atomicReceiptLine(kind, path string, bytes, lines int, extra ...string) string
func atomicClipUTF8(text string, budget int) string

const (
    atomicReadBudgetBytes  = 16 << 10
    atomicReceiptBudgetBytes = 512
    atomicAutoWholeMaxLines  = 400
)
```

**零阻塞规则**（B 依赖 A 的三段时间线）：

| 时间 | A | B |
| --- | --- | --- |
| t0 | A1：底座（上面这份 API）+ 单测 | **B0（零依赖）**：`providerVisibleTools` 选项、团队 surface 收窄（可先只做「不含旧三件」的断言）、`extractWritePathsFromArgs` 的 `ops` 路径表、配置项、`role.go` 纪律文本、子进程/崩溃测试骨架 |
| t1 | A2–A3：`atomic_read` 全模式 + 读契约 + 红→绿 | **B1**：`atomicwrite.go`（create/replace/append/patch/delete，不含 `ops`）+ 平价用例 |
| t2 | A4：race/度量/证据行 | **B2–B4**：`ops` + journal、租约/令牌用例、度量与默认翻转、§7 回填 |

**若 B 发现冻结接口不够用**：**不许改 A 的文件**，也不许复制一份助手；在 §9 登记 + 找 A 加一个**新增**函数
（additive，A 侧带用例）。这条是这次拆分唯一的硬协作条款。

### 4.5 共享文件规则（只有 1 个代码文件 + 1 张表）

1. `internal/tool/builtin/workspace.go`：只允许**在自己的 `overrides` 映射里加自己那一行**（不重排、不格式化）。
   后落地者 rebase 后**追加**，冲突解决就是两行都在。若另一名写者正在改这个文件 → 按 §8.1 第 4 条处理。
2. 本文件 §7 状态表：只允许**追加自己的节点行**，不改别人的行；后落地者 rebase。
3. 其余全部文件**文件级互斥**：A 的清单与 B 的清单**零交集**。任何一方需要清单外的新文件 → 先登记再做。

### 4.6 施工顺序与提交序列

| 提交 | 归属 | 内容 |
| --- | --- | --- |
| ① | A | `feat(atomic-fs): anchored read tool with outline/window/delta modes`（A1+A2） |
| ② | B | `feat(atomic-fs): path-scoped atomic writer (create/replace/append/patch/delete)`（B0+B1） |
| ③ | A | `test(atomic-fs): delta/CAS + read-vs-replace race evidence`（A3/A4 若未并入 ①） |
| ④ | B | `feat(atomic-fs): multi-file transaction with journal` + `feat(team): mount atomic fs pair on team surfaces`（B2/B3） |
| ⑤ | B | `feat(team): default the team surface to the atomic pair` + 度量与文档（B4） |

提交粒度与拆分方式沿用 `TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md` §6 的口径：每个提交自含可编译的树 +
自己的用例；**不 push**。

---

## 5. Token 收益测算模型

### 5.1 模型

一次操作在上下文里的字节：

```
B(op) = S_tool × T         # 前缀：schema+描述，每个 tool 面每轮都付
      + A_args             # 调用参数（模型生成，落进上下文）
      + R_result           # 结果（落进上下文，直到被 snip）
      + K_extra × (A+R)    # 多出来的回合：先读后写、写完再读确认
```

三条推论（本方案的全部设计决策都由它们决定）：

1. **结果比参数更贵**：结果**每次回合都重放**（缓存命中按 cache-read 计价），参数只出现一次。
   实测 `read_file` 结果中位 8.1KB 且合计 3.1MB —— 这是最大的一块。
2. **回合是最贵的单位**：多一次「读全文」回合 = 一次完整的前缀 + 推理；`ops` 把 N 个文件压成 1 个回合。
3. **shell 噪声是纯浪费**：`cat > f <<'EOF'` 里 `cd … &&`、引号、`EOF` 与 `set -e` 都不携带信息，
   实测写类 bash 命令中位 309B、最大 14.2KB，其中相当一部分是这类噪声。

### 5.2 三个真实工例（数字为**模型估算**；实测见 §5.4）

**工例 1 —— 改一个 2000 行文件里的 5 行（team 里最常见）**

| 路径 | 参数 | 结果 | 回合 |
| --- | --- | --- | --- |
| 今天（bash） | `sed -n '100,140p' f`(≈150B) + `python3 - <<'PY'`(≈1.5–12KB) | 1.4KB + 0.5KB | 2–3 |
| 今天（结构化） | `read_file`(72B) + `write_file`(7.1KB 全文) | **8.1KB** + 0.1KB | 2 |
| 新 | `atomic_read window 120-180`(≈120B) + `atomic_write patch`(≈500B) | **1.8KB** + 0.15KB | **2** |

新路径把**结果**从 8.1KB 压到 1.8KB（−78%），参数从 7.1KB 压到 0.5KB（−93%）。

**工例 2 —— 追加 20 行证据（≈1.2KB 内容）**

| 路径 | 参数 | 结果 | 并发/边界 |
| --- | --- | --- | --- |
| 今天（bash） | `cat >> f <<'EOF'`(≈1.25KB) | ~0.1KB | **整工作区租约**、`write_paths` 子代理拒绝、审计回合被拒 |
| 新 | `atomic_write append`(≈1.25KB) | ≈0.1KB | **路径级租约**、团队令牌按路径排队、跨进程不交错 |

字节上两者接近（bash append 本来就不回显）；这个工例的收益在**并发面与合法性**，不在字节 —— 如实记。

**工例 3 —— 一次改 3 个文件**

| 路径 | 回合 | 取租约次数 |
| --- | --- | --- |
| 今天 | 3（+ 可能 3 次读） | 3（逐个可能在队友后面排队） |
| 新（`ops`） | **1** | 1（一次取齐 3 个路径域） |

按「多一次回合 ≈ 一次完整前缀 + 推理」计，省下的 2 个回合在长会话里是 thousands of tokens 级；
更重要的是**队友不会被一串写调用反复挡住**。

### 5.3 前缀账单（每成员每轮都付）

| 面 | 字节 | ≈token |
| --- | --- | --- |
| 现状团队面（`read_file`+`write_file`+`edit_file`） | 1845B | 512 |
| 新对（`atomic_read`+`atomic_write`） | 1566B | 435 |
| **净差** | **−279B** | **−78/turn/成员** |

6 成员 × 100 轮 ≈ **4.7 万 token** 的前缀节省；若保留旧三件与新对**并存**（不建议）则是 +1566B 的**净增**，
因此 §4.3 的收窄不是可选项，而是这次拆分的组成部分。

### 5.4 验证方法（必须报分布，不许只报平均）

1. **夹具探针（必做，离线）**：在 `atomicread_measure_test.go` / `atomicfs_write_measure_test.go` 里用固定夹具
   （2KB 小文件、2000 行大文件、5 行 patch、20 行 append、3 文件 `ops`），对
   `bash 等效 / 现有工具 / 新对` 三种形状各测 **schema+参数+结果** 字节，输出中位、p90、最大与合计。
   断言用**预算**形式（如 `atomic_read window` 结果 ≤ bash 等效的 40%），避免断言绑死在会漂的绝对数字上。
2. **分布而非均值**：实测里 `write_file` 的 p90(28KB) 是均值(10.5KB)的 2.7 倍、`bash` 结果 max 31993B —— 
   只报均值会把「长尾被压掉」这件事说反。报告必须给中位 + p90 + 长尾计数。
3. **live 计量（可选，B4）**：`cmd/e2ebench` 的 `meter.go` 在 provider 代理侧读 `prompt_tokens`/`completion_tokens`，
   同一任务的 `atomic_fs=off/team` 两臂对比；`WithoutUsage` 非零要如实报「未被测到」，不许当 0。
4. **回归口径**：上下文增长用既有 `tools/contextgrowth` 的口径复核（若该工具当前不覆盖 tool 结果，B4 只做**登记**，
   不顺手扩它 —— 那是另一个面）。

### 5.5 实测（B 侧回填，`go test ./internal/tool/builtin/ -run TestAtomicWriteMeasure -v`）

夹具与 §5.4 第 1 条一致：2000 行大文件改 5 行、20 行 append、3 文件 `ops`、2000 行文件的五种模式回执。
数字由 `atomicfs_write_measure_test.go` 在**真实工具调用**上测得（不是估算），断言一律用预算形式。

**工例 1 —— 2000 行文件里改 5 行（写侧）**

| 路径 | 前缀 | 参数 | 结果 | 合计 |
| --- | --- | --- | --- | --- |
| `python3 - <<'PY'` 全文件重写 | 2387B | 129499B | 0B | **129499B** |
| `read_file` + `write_file` | 1345B | 129440B | 143395B | **272835B** |
| `atomic_read window` + `atomic_write patch` | 926B | **78B** | **209B** | **287B** |

patch 参数 = 全文件重写参数的 **0.06%**；结果侧 `write_file` 把整个文件回显（143KB），patch 回执 209B。

**工例 2 —— 20 行 append（≈1.2KB 内容）**

| 路径 | 参数 | 结果 |
| --- | --- | --- |
| `cat >> f <<'EOF'` | 990B | 0B |
| `atomic_write append` | 1011B | **81B** |

如实记录：**append 在字节上不省**（+21B 参数、+81B 回执），收益在 §5.2 工例 2 说的并发面与合法性
（路径级租约、团队令牌按路径排队、单次 `write(2)` 不交错）。不做「append 更省 token」的假宣称。

**工例 3 —— 一次改 3 个文件**

| 路径 | 参数 | 结果 | 回合 | 取租约 |
| --- | --- | --- | --- | --- |
| 3 × `edit_file` | 189B | 33B | 3 | 3 |
| 1 × `atomic_write ops` | 240B | 311B | **1** | **1** |

`ops` 的 JSON 信封每项多一个 `"mode"` 字段，参数**比三次 `edit_file` 大 51B**（+27%）；换来 2 个回合与
2 次取租约。**长会话里回合是最贵的单位**（§5.1 推论 2），但字节上必须如实记为「参数略增」。

**回执预算（五种模式，2000 行夹具）**

| 模式 | 中位 | p90 | 最大 | 预算 |
| --- | --- | --- | --- | --- |
| `create` | 88B | 88B | 88B | ≤ 2560B |
| `replace` | 117B | 117B | 117B | 同上 |
| `append` | 81B | 81B | 81B | 同上 |
| `patch` | 211B | 211B | 211B | 同上 |
| `delete` | 69B | 69B | 69B | 同上 |

预算 = `maxPostWriteReceiptBytes`(2048) + `atomicReceiptBudgetBytes`(512)。夹具是固定大小的，所以
中位=p90=最大；**真正的长尾在 `patch`**（回执含 `post_write_receipt.go` 的匹配/替换 span，2048B 封顶），
用例对每种模式都断言「回执不含文件内容」。

**前缀账单（实测，与 §5.3 的估算一致）**

| 面 | 实测字节 | §5.3 估算 |
| --- | --- | --- |
| 旧三件（`read_file`+`write_file`+`edit_file`） | **1845B** | 1845B |
| 新对（`atomic_read`+`atomic_write`） | **1568B** | 1566B |
| 净差 | **−277B** | −279B |

`atomic_write` 单件 = **926B**（schema 492B + 描述 434B），与 §2.3 冻结值逐字节一致，用例把它钉成预算。
`atomic_read` 实测 642B（A 侧测得 640B），两者相加的 2B 差来自 `atomic_read` 描述的实际字节数 —— 表里记实测值。

**`ops` 部分提交的诚实口径**：`TestAtomicOpsPartialCommitIsReportedExactly` 在 commit 阶段移除第二个目标
的 staged temp，断言回执给出逐路径 `committed`/`not committed`、`txid`，且 journal 的 `committed`/`failed`
与实际落盘一致。**跨文件可见性原子不承诺**（§3.3 第 4 条），用例不断言它。

**未做（如实登记）**：§5.4 第 3 条（`cmd/e2ebench` live 计量）本轮**未做** —— 需要真实 provider 调用与两臂
对照运行，属于发布前验证。

**默认开关（按要求改为 `team`）**：`tools.atomic_fs` 的缺省值由 `off` 改为 **`team`**，依据是上表的
**实测**前缀账单（−277B/成员/轮，5.3 估算 −279B 与之吻合），而不是 §8.1 第 6 条要求的 live 计量 ——
**live 计量仍未做**，这一点不因默认翻转而改变。翻转的作用域因此限定在**团队构建**：普通单 agent 会话的
provider 面逐字节不变（用例 `TestAtomicFSSurfaceIsTheOnlySurfaceThatChanges` 与
`TestTeamAtomicSurfaceReachesTheProvider` 双向钉住）。显式 `off` 仍可回退。

### 5.6 度量口径的边界（实测发现，已修）

本节记录一处**基础设施层面的边界**，它同时影响新对与旧三件，是「同进程丢更新」之外的第三条口径：

`fileops` 的磁盘 version 是**纯元数据**（size/mode/mtime + native ctime/ino…，见
`diskVersionParts`）。在本机 ext4 上实测：**连续两次写同一文件，14/30 次 ctime 落在同一个时钟刻度**
（ctime 约 1ms 才前进一次），因此「同尺寸重写」可以产生**完全相同的 version**。旧路径靠
`readEditSource` 自带的内容哈希兜底（实测 60/60 全部检出），**新对最初不行**：`atomicAnchorFor(ctx, path, "")`
在 version 相同时会用**当前读到的字节**构造锚点，再拿同一批字节做 CAS 比对 —— 比对必然通过，等于 CAS 空转。
实测症状：`read → 同尺寸同 mtime 外改 → atomic_write patch`，60 次中 25 次报成 `old_string not found`
（而非 `FS_STALE_VERSION`），即锚点检查根本没生效。

**已修**（A 侧，`atomicfs_shared.go` 的 `atomicAnchorFromObservation`）：version 相同时改从**本次会话读过并
缓存的字节**构造锚点，缓存与当前字节不一致即交回 `atomicRecheckSource` 报 `FS_STALE_VERSION`（带 hunk）。
修复后同一探针 **60/60 全部拒绝**，0 次误报、0 次静默覆盖；变异探针（去掉缓存分支）在文件系统级用例上变红。
回归用例：`TestAtomicWriteRefusesSameSizeRewriteAtCollidingVersion`（构造碰撞态）+
`TestAtomicWriteRefusesCollidedVersionFromTheFilesystem`（不构造，循环真实写直到文件系统自己碰撞；无碰撞的宿主 skip 而非 fail）。

同一根因还导致 `internal/tool/builtin/file_observation_test.go` 的 `TestExternalChangeMakesObservationStale`
**在 pristine HEAD 上就 flaky**（靠 ctime 粒度决定成败）。该用例已改为**显式注入过期 version**
（不再依赖时序），修复前 5/5 红、修复后 20/20 绿 —— 它现在测的是规则本身而不是时钟。

---

## 6. 并发安全验证方案

### 6.1 不变量（每条都要有断言）

1. `append`：N 个并发写者的载荷**逐个完整**且总数等于 N（无交错、无丢失）。
2. `patch/replace`：**成功返回的写者，其字节一定在最终内容里**（无静默丢更新）；
   失败者必须拿到 `FSStaleVersion` 与 hunk（**失败必须是响的**）。
3. 任何时刻的读取者看到的是**旧或新的完整哈希**（无撕裂、无半写）。
4. 崩溃注入后：目标文件是旧或新的**完整内容**，`ops` journal 与实际提交集合一致。
5. 请求被拒（越界/会话数据/未观察）时：**磁盘字节完全不变**。
6. 路径级租约：`atomic_write path=x` 的租约域是 `x`（对照 `bash` 的整工作区）；`write_paths` 子代理越界被拒。

### 6.2 用例矩阵

| # | 场景 | 断言 | 手段 |
| --- | --- | --- | --- |
| 1 | 同进程 8 个 goroutine 并发 `patch` 不同 span（同文件） | 全部成功或 `FSStaleVersion`；成功者的 span 都在；最终行数=原行+新增 | 起点门 + `fileops.LockMany`；禁止 sleep 计时 |
| 2 | 同进程 32 路并发 `append` | 载荷数=32 且逐条完整 | 单次 `write(2)` |
| 3 | **跨进程** 2 进程对同一文件 `replace`（同一锚点） | 恰好一个成功；另一个 `FSStaleVersion`；文件内容=胜者 | `re-exec`（`os.Args[0]`+env 门），先例：`internal/workspacelease/lease_test.go`、`internal/team/sessionstore_test.go` |
| 4 | 崩溃注入：`fileutil.CrashPoint` 在 `replace` 前 panic（子进程） | 目标是旧的完整内容；tmp 被清理；journal 与提交集合一致 | 先例：`internal/agent/save_crash_characterization_test.go` |
| 5 | 读-写并发循环（读侧 fuzz） | 每次读到的哈希 ∈ {旧, 新}；`-race -count=2` | A 的 `atomicread_race_test.go` |
| 6 | 外部写者（绕开工具直接写盘）后 `patch since=<旧 id>` | `FSStaleVersion` + hunk；重读后同一次 patch 成功 | A 的 CAS 用例 + B 的写侧 |
| 7 | `ops` 第二个目标 rename 必失败 | `committed:[a]`/`not committed:[b]`；a 已是新内容；journal 记 `txid` | 让第 2 个目标是目录 |
| 8 | `ops` prepare 阶段失败 | 磁盘零变化（对比 mtime+哈希） | 第 2 个目标 CAS 失配 |
| 9 | **安全平价** | `atomic_write` 到会话数据根 / 托管配置被拒；写根外被拒；与 `write_file` 的拒绝文案同形 | 复用 `ConfineWriters` 夹具 |
| 10 | `write_paths` 子代理 | 越界路径被拒；`ops` 中任一越界则整个调用被拒 | `BindWritePaths` + path-bound 夹具 |
| 11 | 团队面收窄 | 团队 build 的 `Schemas()` 含新对、不含旧三件；非团队 build 逐字节不变 | `cli` + `boot` surface 用例 |

### 6.3 变异探针（每个机制都要被证明「在承重」）

| 探针 | 期望红 |
| --- | --- |
| 去掉写前的 `atomicRecheck`（不比对锚点） | 第 6 条丢更新用例红（成功者的字节被覆盖） |
| 去掉 `LockMany` | 第 1 条出现丢更新/断言红 |
| `append` 改成分块写（两次 `write`） | 第 2 条出现载荷交错 |
| 用 `os.WriteFile` 替 `AtomicOverwriteFileStrict` | 第 5 条撕裂读红 |
| `pathBoundWriterNames` 去掉 `atomic_write` | 第 10 条 + 租约域变整工作区（第 6 条对照）红 |
| 团队面只加新对、不收窄 | §5.3 前缀账单用例红（净省为负） |

### 6.4 崩溃注入

`fileutil.CrashPoint` 已有（`internal/fileutil/atomicwrite.go:26`），且 `internal/agent` 侧有 `crashAt` 辅助先例。
新增覆盖点：`create` 的 link 前、`replace/patch` 的 rename 前、`ops` 的每个 rename 前/后。
**panic 必须在子进程**（否则会杀掉测试进程），子进程断言由父进程读结果文件完成。

### 6.5 观测出口（不新造埋点）

- 租约侧已有：`Owner.Metrics()/MetricsReport()`、`control.Controller.WorkspaceLeaseMetrics()`（M0），
  可见等待次数/时长分布/被挡域/超时 —— 本方案**复用**它们验证「路径级租约比整工作区少挡人」。
- 写令牌侧已有：`write_intent_gate` 的等待通知（指名持有者与范围）。
- 本方案**不新增全局计数器**：冲突/失配次数从工具结果与 journal 即可读；
  若 B4 证明需要汇总量，再单独立项（登记到 §9，不在本轮范围）。

---

## 7. 节点状态

| 节点 | 归属 | 主题 | 状态 | 证据 |
| --- | --- | --- | --- | --- |
| A1 | A | 共享底座（锚点/CAS/hunk/回执）+ 冻结 API | **已完成** | `internal/tool/builtin/atomicfs_shared.go` + `atomicfs_shared_test.go`（11 用例）。冻结 API 全部落地，另加 3 个 additive 函数（见下）。`gofmt`/`build`/`vet` 空；`-race -count=2` 绿。 |
| A2 | A | `atomic_read` 全模式 + 读契约 | **已完成** | `atomicread.go` + `atomicread_outline.go`；schema 257B + 描述 383B = 640B（与 §2.2 冻结值逐字节一致）。`Execute`/`ExecuteRead`/`ReadEnvelope`/`ResolveReadPath`/`ClassifyCall`/`PlanModeSafe`/`SnipHint` 全部实现；`atomicread_test.go` 16 用例（auto 阈值两侧 / window / outline（Go+markdown+无符号退化）/ delta / tail / GBK / UTF-16 / 二进制 / 越界 / 空文件 / overlay / 结果头含 read-id / 预算）。 |
| A3 | A | delta/CAS 与读-写 race 证据 | **已完成** | `atomicread_cas_test.go`（5 用例：读→外改→delta→写被拒→重读恢复；同 mtime 重写仍被检出；delta 只对**被引用的那次读**做 diff；被淘汰的 read-id 被拒；读零副作用）+ `atomicread_race_test.go`（3 用例：8 读 × 1 替换循环无撕裂；8 并发读同锚点；24 路 append 全部完整落地）。`-race -count=2` 绿。 |
| A4 | A | 读侧度量探针 | **已完成** | `atomicread_measure_test.go`。实测见 §5.4 回填表。 |
| B0 | B | 宿主接线（surface 选项/路径抽取/配置/纪律文本/测试骨架，**零依赖**） | **已完成** | `config`：`tools.atomic_fs = off/team/all` + `AtomicFSSurfaceEnabled(team)`（缺省与未识别值归 `team`，作用域限团队构建，普通构建逐字节不变；显式 `off` 可回退）；`boot`：`Options.ProviderVisibleTools` + `applyUnifiedProviderToolSurface` 选择性替换（只从**已注册的编译期内建**里选，MCP/插件永不可被点出；旧三件仍在 registry，`use_capability`/回放不受影响）+ `providerVisibleTools()` 从 `TeamRole` 派生，故 CLI/TUI 成员/ACP/desktop 一处收窄全部生效；`path_bound_tools.go`：`pathBoundWriterNames += atomic_write`、`extractWritePathsFromArgs` 支持 `ops[].path`（返回全部目标，任一无名则整体拒绝）；`role.go` 第 5 条 + 两个 `SKILL.md`（**+120B**，与方案 ≤120B 一致）。用例：`internal/boot/atomic_fs_surface_test.go`（5 用例：替换矩阵 / 模式矩阵含 typo / 只有 team 角色改变面 / 只收窄不暴露 / 真实栈双面逐字节对照）。**未按方案在 `internal/cli/team_backend_build.go` 注入**：收窄移入 boot 由 `TeamRole` 派生，少一处会漂的第二真相（偏离已记入 §9）。 |
| B1 | B | `atomic_write` 五模式 + 安全平价 | **已完成** | `atomicwrite.go`：schema 492B + 描述 434B = **926B**（与 §2.3 冻结值逐字节一致，用例钉成预算）；五模式 + `confineWrite` 平价序列（`resolveIn` → `confineWrite(effectiveWriteRoots)` → 目标锁 → CAS）；编码保持（GB18030 实测保持）、overlay 路线、`Preview`（Preview 的 NewText 必须等于 Execute 落盘字节）、`PlanModeSafe=false`、`SnipHint`、`DeclareWriteAccess`/`DeclareEvidenceTarget`。用例 `atomicwrite_test.go` 13 组：create 撞文件（**不回显现有内容**）/ replace 需先读 + 锚点跨队友写被拒且**带 hunk** / append 免读 + 缺文件创建 + 超 4MB 拒（`FSTooLarge`，给 replace 出路）/ patch 的 0 命中与多命中复用 `edit_file` 错误形状 / range 越界**拒绝而非截断** / delete 幂等 / 编码保持 / overlay 写缓冲不写盘 / **安全平价**（与 `write_file` 同文案的越界与会话数据拒绝）/ 拒绝即零字节变化（9 种）/ 回执不含文件内容 / 参数门 `path 或 ops`。另加 `tool.ArgumentContractProvider`（见 §9 R8）。 |
| B2 | B | `ops` 事务 + journal | **已完成** | `atomicwrite_ops.go`：prepare（逐目标读→CAS→内存 splice→`fileutil.StageAtomicWrite` 落 tmp，**零 rename**）→ journal（`.reasonix/atomic-writes/<txid>.json`，写计划→写结果两阶段）→ commit（逐目标 `fileutil.PublishStagedWrite`）。用例 `atomicwrite_ops_test.go` 6 组：3/3 提交 / **prepare 失败零痕迹**（磁盘字节 + tmp 都清干净）/ **部分提交逐路径报告**（移除第 2 个 staged temp，断言 `committed`/`not committed` + txid + journal 的 committed/failed 与落盘一致）/ journal 落盘且 `git check-ignore` 命中 / 重复路径与 append 入事务被拒 / 只重发剩余项成功。`fileutil.PublishStagedWrite` 加 `Crash("publish")` 注入点（additive，`CrashPoint` 为 nil 时无行为变化）。 |
| B3 | B | 租约/令牌/子代理边界用例 | **已完成** | `internal/agent/atomicfs_lease_scope_test.go` 7 用例：路径抽取覆盖 `path` 与**全部** `ops[].path`（含绝对路径）/ 无名目标整体拒绝 / **写路径边界**（claim 内可写、claim 外被拒、`ops` 任一越界则整笔拒绝且不产生 in-claim 副作用）/ **租约域是文件不是工作区**（持 a 的文件租约不挡 b，另一 owner 持同一文件才被挡）/ `DeclareWriteAccess` 覆盖每个 ops 目标的父目录 / evidence 对缺失目标 fail-closed / `PlanModeSafe=false` + 非只读。`internal/tool/builtin/atomicwrite_concurrency_test.go` 7 用例：8 路并发 patch 不同 span（成功者字节必在终态，失败者必是 `FSStaleVersion`）/ **32 路 append 不交错**（工具级 + **无锁原语级**双层，后者只有「单次 write(2)」能过）/ **跨进程** 同锚点 replace（peer 先落盘，parent 带旧 `since` 必被拒且 peer 字节存活）/ **崩溃注入**（两个持久化边界各一子进程：`atomic-write` 与事务的 `publish`，断言目标是完整旧内容、无半写）/ 外部写者被检出（带 hunk，重读后同一次 patch 成功）/ `since` 固定旧锚点被拒 + 伪造 id 报 `FSNotObserved` / 同回合第二次写不误报冲突。`-race -count=2` 绿。 |
| B4 | B | 度量、默认翻转、本文档回填 | **已完成（默认翻转**未做**）** | `atomicfs_write_measure_test.go` 5 组实测探针，数字回填 §5.5：patch 参数 78B vs 全文件 129499B（**0.06%**）、`write_file` 结果 143395B vs patch 回执 209B、append **字节不省**（990→1011B，如实记）、`ops` 参数比三次 `edit_file` **大 51B** 换 2 回合 + 2 次取租约、五模式回执中位 69–211B、前缀 1845B→1568B（**−277B/成员/轮**）。`atomic_write` 单件 926B 钉成预算。**默认翻转已做**：`tools.atomic_fs` 缺省由 `off` 改为 `team`，依据是本节与 §5.5 的**实测**前缀账单（−277B/成员/轮），不是 §8.1 第 6 条要求的 live 计量；live 计量（`cmd/e2ebench`）**仍未做**，翻转作用域因此限定在团队构建，普通构建的 provider 面逐字节不变，显式 `off` 仍可回退。`docs/TOOL_CONTRACT.md` / `.zh-CN.md` 增两行 + 团队面说明。 |

> 落地时每个节点在本表追加**实测数字**（字节/中位/p90/红→绿记录/提交号），
> 口径与 `TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md` §6 一致。

### 7.1 A 侧实测（`atomicread_measure_test.go`，夹具探针，Linux/amd64）

前缀账单（§5.3 复核，schema+描述字节）：

| 面 | 字节 | 与文档草案 |
| --- | --- | --- |
| `read_file`+`write_file`+`edit_file` | **1845B** | 一致 |
| `atomic_read`+`atomic_write` | **1568B** | 一致（草案 1566B，差 2B：§2.2 里 `atomic_read` 描述的**字节数**标注有误，实际文本为 385B 而非 383B；实现与 §2.2 的**文本**逐字节相同，仅计数订正） |
| 净差 | **−277B**（每成员每轮） | 文档估 −279B |

读结果形状（2000 行夹具，整文件 162013B；对照 = 同任务的 `read_file` 结果）：

| 模式 | 结果字节 | 对照基线 | 占比 |
| --- | --- | --- | --- |
| `auto`（小文件 ≤400 行） | 2626B | 1900B（原文） | 138%（每行带行号，属预期） |
| `auto`（大文件） | 5828B | 162013B（整文件） | **3.6%** |
| `outline`（大文件） | 4238B | 162013B | **2.6%** |
| `window 100`（大文件） | 2951B | 2859B（`read_file` 同窗口） | 103%（同量级；差在头部 read-id） |
| `tail`（大文件） | 2412B | 2859B | **84%** |

工例 1（改 2000 行文件里的 5 行）：

| 路径 | 参数 | 结果 | 合计 |
| --- | --- | --- | --- |
| bash peek + heredoc 重写 | 204B | 152B | 356B |
| `read_file` 整读 + `write_file` 整写 | 129440B | 143395B | **272835B** |
| `atomic_read window` + `atomic_write patch` | 136B | 607B | **743B** |

工例 3（追加 20 行证据，≈990B 载荷）：bash heredoc 990B / 结果 0B；`atomic_write append` 1011B / 回执 87B。
**如实记**：字节上两者接近（bash append 本就不回显），本工例的收益在并发面与合法性，不在字节。

红→绿（变异探针，均已还原）：

| 探针 | 期望红 | 实测 |
| --- | --- | --- |
| read-id 去掉内容项（不再内容寻址） | CAS/幂等用例红 | ✅ `TestAtomicReadIDIsContentAddressed` + `TestAtomicReadCASRoundTrip` 红 |
| `window` 忽略字节预算 | 预算用例红 | ✅ `TestAtomicReadResultsStayWithinBudget` 红（185089B > 16384B） |
| delta 改为对**当前文件**做 diff（不用被引用的那次读） | delta 用例红 | ✅ `TestAtomicReadDeltaDiffsAgainstTheCitedRead` + `...ReportsOnlyChanges` 红 |
| capture 记录 `Absent` 而非 `Present` | 观察互通用例红 | ✅ `TestAtomicReadObservationInteroperatesWithExistingWriters` + `TestAtomicCaptureRecordsObservationForLaterWriters` 红 |
| 结果头去掉 read-id | 头/delta 用例红 | ✅ `TestAtomicReadHeaderCarriesReadIDAndShape` + 2 个 delta 用例红 |

### 7.2 A 相对冻结 API 的 additive 增补（§4.4 允许流程）

B 已确认需要；均为**新增**函数，不改冻结签名：

| 函数 | 用途 |
| --- | --- |
| `atomicRecheckSource(ctx, overlay, path, anchor) (editSource, []byte, error)` | B 的 replace/patch/delete 需要「CAS 校验 + 读路线」一次拿到，避免二次读并与之竞态 |
| `atomicCaptureSource(ctx, overlay, path) (atomicAnchor, editSource, error)` | 读侧用同一份字节既渲染又定锚，避免二次读得到另一个版本 |
| `atomicCachedContent(readID) ([]byte, bool)` | 按 read-id（而非 path+version）取回字节；delta 的精确查表 |
| `atomicShownBytes(anchor) []byte` | 冲突 diff 的「模型看到的那一侧」；path+version 查表在同一时间戳 tick 内两次写入时会取错边，取不到就返回 nil（无 hunk 的拒绝，不撒谎） |
| `atomicSortedPaths([]string) []string` | 多目标取锁的稳定顺序（B 的 `ops` 用） |

`atomicClipHunks` 对超预算的单个 hunk 会退化为 rune 边界裁剪；`atomicConflict` 的 hunk 预算用新常量
`atomicReadConflictBudgetBytes = 2<<10`（比回执的 512B 大：hunk 是模型重试的唯一线索），回执仍用
`atomicReceiptBudgetBytes = 512`。

### 7.3 实施期修掉的两个真实缺陷（A 侧）

**1. `since==""` 路径上的 CAS 曾是空转的（已修，A 侧）**

`atomicAnchorFor(ctx, path, "")` 走 `atomicAnchorFromObservation`；当 `observed.Version == version` 时，
原实现用**刚刚读到的当前字节**计算 `ContentHash`，`atomicRecheckSource` 再拿当前字节与它比对——
同一个来源，必然相等。于是元数据版本一旦撞车（同尺寸重写 + 同一时钟 tick：`disk-v1` 只用
size/mode/mtime/ctime，本机实测 30 次背靠背写入有 14 次 ctime 相同），CAS 就退化成「文件与自己比较」。

实测（本机 ext4，同尺寸同 mtime 外部重写，60 轮）：`FS_STALE_VERSION` 35 次、
**`old_string not found` 25 次**（说明 CAS 没拦住、落到了匹配器）、成功发布 0 次。
后者是最危险的形状：它这次侥幸因为内容不含 `old` 而失败，但**同内容重写会静默发布**。

修法（`atomicfs_shared.go` 的 `atomicAnchorFromObservation`）：版本相同也要看**这次会话真正读到的那份字节**
（`atomicCachedSnapshot`）。缓存命中且与当前字节不同 → 用缓存字节构造锚点，让 recheck 报 `FSStaleVersion`
并附上 hunk。`atomicCapture` 每次读都会 `atomicRememberSnapshot`，所以 `atomic_read → atomic_write`
这条主路径上缓存总是热的。

用例：`atomicread_cas_test.go` 的
`TestAtomicWriteRefusesSameSizeRewriteAtCollidingVersion`（显式构造撞车态）与
`TestAtomicWriteRefusesCollidedVersionFromTheFilesystem`（不构造，靠真实文件系统撞出来，撞不到则 skip）。
变异探针：去掉该分支后第二条用例红；恢复后 30/30 撞车全部被拒。

**2. `TestExternalChangeMakesObservationStale` 是既有的时间竞态（已修）**

它在 pristine HEAD 上就约 1/2 概率红：用例靠 `os.Chtimes` 还原 mtime 后期待版本改变，而 `disk-v1`
是元数据版本，撞不撞车取决于 ctime 粒度——这是**测试的缺陷，不是被对象的缺陷**（真实写者走
temp+rename，目标 inode 会变，观察目标直接消失，不是「原地改」）。改为显式写入过期观察：既保持
断言（过期观察必须被拒、重读可恢复），又不再依赖时序。修复后 20/20 绿。

> 附注：`fileops.DiskSnapshot` 的版本只用元数据是**有意设计**（窗口读不必扫描整个文件来铸版本），
> 本轮不改它；两个工具用内容哈希补齐，因此不受影响。

### 7.4 宿主按工具名索引的表（A 侧补齐）

默认开关翻到 `team` 之后，团队面挂的是 `atomic_read`/`atomic_write`，而宿主里有若干
**按工具名索引**的表。这些表缺项不会报错，只会**静默降级**（卡片显示原始名、回执不计入
「改过哪些文件」、ACP 不跟读）。A 侧已逐条补上，并为每一类加了用例：

| 位置 | 补了什么 | 用例 |
| --- | --- | --- |
| `internal/cli/toolcard.go` | `toolVerb`/`toolArgKey`/`toolCategory`（卡片动词、参数、读写配色） | `internal/cli/atomic_fs_toolcard_test.go` |
| `internal/cli/chat_tui_approval.go` | 审批卡标签 | 同上 |
| `internal/acp/dispatch.go` | `toolKindFor` + `locationTools`（编辑器跟读；`atomic_read` 的 `offset` 也跟行） | 编译期 + 既有 ACP 用例 |
| `internal/evidence/evidence.go` | `isWriterTool`/`isReaderTool` 回执分类 | `internal/evidence/atomic_fs_receipt_test.go` |
| `internal/evidence/ops_paths.go` | **新**：`ops[].path` 抽进回执（此前 `atomic_write` 的 `ops` 事务回执路径为空，等于「改过哪些文件」全丢） | 同上 |
| `internal/doctor/quality.go` | 质量遥测的写者计数 | — |

`ops_paths.go` 是本轮新增的**真实缺陷修复**：`extractPaths` 只看 `path`/`paths`/`file_paths`，
不看 `ops`，所以多文件事务的回执声称「写了文件」却不带任何路径。

---

## 8. 施工约定与门禁

### 8.1 硬约束

1. **不新造基础设施**：原子发布、观察/CAS、锁、回执、读契约、符号采集一律复用 §1.3；禁止引入新的文件锁库或
   自有 rename 逻辑（`fileutil` 的 EXDEV/重试语义已经踩过坑，重写一遍就是把坑再踩一遍）。
2. **工具不自己取租约**：写路径的 L2/L3 一律由 `tool_write_coordination.go` 取；工具只负责「说出自己写哪」。
3. **安全平价不可省**：`confineWrite`（写根 + 会话数据 + 托管配置）与 `requireObserved` 的序列与既有写者一致。
4. **共享文件（`workspace.go`、本文件 §7）只允许追加自己的行**；若发现他人正在改同一文件 → 让对方先落，自己 rebase。
5. **不许把旧工具删掉**：收窄只发生在**团队 build 的 provider 面**（`ProviderVisibleTools`），
   注册表里旧工具仍在（`use_capability`/回放/其它 host 仍可用）。这是可回退的开关，不是删除。
6. **度量先于调参**：B4 的默认翻转必须附 §5.4 的实测表；没有数字就保持 `off`。

### 8.2 门禁（每个节点）

```bash
gofmt -l internal/                       # 空
go build ./...                           # 空
go vet ./...                             # 空
go test ./internal/tool/builtin/ ./internal/agent/ ./internal/cli/ ./internal/boot/ -count=1
go test -race ./internal/tool/builtin/ -count=2
"$(go env GOPATH)/bin/golangci-lint" run --timeout 10m ./internal/tool/... ./internal/agent/... ./internal/cli/...
go run ./tools/repolint                  # 自己的文件 0 项
```

新增/修改文件 **≤ 800 行**（`tools/repolint/size.go`，测试同限）。

### 8.3 已知既有红灯（不要误判为自己引入）

- `internal/netclient`、`internal/plugin`、`internal/worktree` 在 pristine HEAD 上就有失败（基线）。
- `internal/skill` 与 `internal/skill/skillwatch` 同样在 pristine HEAD 上失败：实测
  `TestCatalogWatcherInvalidatesCreateRenameAndDelete`、`TestHostWatchServiceSharedAcrossStores`
  两个用例在工作树 stash 后的 pristine HEAD 上按同名复现，与本方案无关（**补充** §8.3 基线，非改动结论）。
- `internal/cli` / `internal/control` 在全量 `go test ./...` 下会偶发 `panic: test timed out after 30m0s`，
  **两棵树都会**，且每次卡住的用例不同（pristine 与 merged 各命中不同的一个），单独跑各自通过
  （`internal/control -count=1` 单独 232s 绿）。**原因未查明，不下结论**：这既不是本方案引入的，也不能从
  现有记录诊断；若日后变成真挂起，需要单独排查。
- `internal/cli` 的 `team_leader_wait` 相关用例历史上出现过「常量提了但 schema 没提」的红灯（并行写者造成，
  已随 `99e6c3987` 入库）：若红灯落在那里，先在 pristine worktree 上归因，再判断是不是自己引入的。
- `internal/boot` 的 `-count=2` 有既有隔离问题 → boot 用 `-count=1`。
- 本文件写作时工作区是干净的（仅本文件未跟踪），但 `team/skills/base/*`、`internal/cli/team_*` 是并行施工的
  常见落点：动 `SKILL.md` 前先看 `git status`，只加自己那一行并 rebase。

### 8.4 完成定义

三件事同时成立才算完成：① 两个工具在**团队 build 的 provider 面上可用**且旧三件已收窄；
② §6.1 六条不变量各有绿色用例，且 §6.3 六条变异探针**都曾红过并且已还原**；
③ §7 有实测数字（字节、分布、红→绿、提交号），且默认开关的状态与数字一致。

---

## 9. 风险与未决

| # | 风险/未决 | 处理 |
| --- | --- | --- |
| R1 | 外部写者（用户编辑器）与 CAS 之间的窗口（check→rename）不可消除 | 明确标定（§3.5）；把「检测并拒绝」当承诺上限；不承诺外部写者的强互斥 |
| R2 | `ops` 部分提交的模型行为（模型会不会正确重发剩余项） | B2 用例只保证**报告正确**；行为面靠 B4 实测（若模型反复失败，把 `ops` 从默认描述里降级为「2–3 文件内推荐」） |
| R3 | 团队面收窄会让既有 playbook/技能里提到的 `edit_file`/`multi_edit` 失效 | B0 同节点改 `role.go` 纪律文本 + `SKILL.md` 一行；`use_capability` 仍可路由旧工具（回退路径存在） |
| R4 | 描述字节预算是**我按 3.6B/token 估的**，真实 tokenizer 可能更高 | B4 用 live meter 复核；预算断言用字节（可复现），token 只作参考 |
| R5 | 大纲模式依赖 `codeindex.go` 的采集器；非 Go 语言质量未知 | A 的用例覆盖 Go + markdown + 无符号 Python；质量不足时 `auto` 退化为「头 60 行」，不追求完美大纲 |
| R6 | 冻结接口不够用 | §4.4 的 additive 流程（登记 + A 加函数），禁止复制助手 |
| R7 | 新增全局埋点 | 本轮不做；若需要单独立项（§6.5） |
| R8 | `atomic_write` 的 provider schema 写了 `required:["path"]`，但 `ops` 事务没有单一 `path`，JSON Schema 无法表达「path 或 ops」 | 已解决，未改冻结 schema：新增 additive 接口 `tool.ArgumentContractProvider`（`ArgumentContractSchema()`），参数校验编译一个**去掉根 required** 的变体，规则由工具自己的 `ArgumentValidator` 执行；provider 面与 `ContractEntries` 仍逐字节等于 §2.3。用例 `TestAtomicWriteArgumentGateAdmitsTheTransactionForm` 钉住「变体只在宿主侧」 |
| R9 | 收窄未按方案落在 `internal/cli/team_backend_build.go`，而由 boot 从 `TeamRole` 派生 | 有意偏离：CLI/TUI 成员后端/ACP/desktop 都走 `boot.Build`，放在 boot 里是**一处真相**，避免每个 host 各写一份收窄（方案 §8.1 第 1 条的精神）。代价：一个 host 若显式传 `ProviderVisibleTools` 会**覆盖**配置模式（`providerVisibleTools` 优先显式值），已在 `TestAtomicFSSurfaceIsTheOnlySurfaceThatChanges` 钉住 |
| R10 | `internal/tool/builtin/file_observation_test.go` 的 `TestExternalChangeMakesObservationStale` 在 pristine HEAD 上就 flaky（同 mtime 重写与 metadata-only version 相撞） | **已修**（按要求，不再按 §10 第 8 条搁置）：用例改为显式注入过期 version，测规则而非测时钟，修复后 20/20 绿。同一根因在新对上的后果更严重（CAS 空转），已连同修复与回归用例记入 §5.6 |
| R11 | 磁盘 version 是纯元数据，同尺寸重写在同一 ctime 刻度内可产生相同 version（本机 ext4 实测 14/30） | 新对改从**会话缓存的字节**取锚点（§5.6），旧路径靠 `readEditSource` 的内容哈希（实测 60/60 检出）。**未改** `fileops` 的 version 定义：那是共享基础设施，改成内容哈希会让每次 stat 都读全文件，属于另一个面 —— 登记在案 |

---

## 10. 明确不做（本轮范围外）

1. **不删旧工具、不改非团队会话的 provider 面**（收窄只发生在团队 build）。
2. **不做正则/结构读**：要正则去 `grep`（它已有；不在默认面上是另一个话题）。
3. **不做二进制写**：`atomic_write` 对非文本目标直接拒绝；编码保持只覆盖 GBK/UTF-16/BOM 这类既有能力。
4. **不做跨文件可见性原子**（§3.3 第 4 条）。
5. **不做强制覆盖开关**（无 `force`）：冲突永远是「重读 + 重试」，不提供「我就想盖掉」的路径。
6. **不动 `member_publish_deliverable` 的 12KB 载荷**（实测中位 12066B）：那是团队产物通道的话题，
   与本方案相邻但不是同一件事 —— 已登记，不在本轮。
7. **不做 UI 展示**（toolcard/审批卡沿用既有写工具的渲染；若 `Preview` 落地后发现卡片不适配，另立节点）。
8. **不修 §8.3 的既有红灯**。

---

## 附录 A：证据索引

### A.1 实测数据（§1.1）

- 方法：读 `~/.reasonix/projects/*/sessions-v4/.content-v1/objects`（5419 个 JSON 消息对象）里的
  `tool_calls` 与 `role:"tool"` 结果字节；类别用启发式正则（写路径：`sed -i|tee|>|>>|open(...,'w')|python3 - <<|rm|mv|cp|mkdir|touch`）。
- 关键计数：bash 2135（带写路径 801 / 读类 1334）；read_file 289（结果中位 8130B）；write_file 26（参数中位 7458B）；
  edit_file 114（参数中位 1075B）；被拒结果 84（bash 60 / publish 20 / recall 3 / write_file 1）。
- 局限：**启发式分类不是人读**（例如「用 python heredoc 改文件」既读又写）；跨项目对象库是共享去重的，
  计数是调用数而非「文件数」；只覆盖这两台/两个项目的会话。

### A.2 工具面字节（§1.4 / §5.3）

- 探针：`tool.Builtins()` 里取 `len(Schema())`/`len(Description())`（一次性测试，已删除）。
- `read_file` 673+382、`write_file` 179+111、`edit_file` 316+184、`bash` 1729+658。
- 新对草案实测：`atomic_read` 257+383=640B，`atomic_write` 492+434=926B。

### A.3 基础设施（§1.3，逐条位置）

- `internal/fileutil/atomicwrite.go`：`AtomicWriteFileStrict` / `AtomicOverwriteFileStrict` / `AtomicCreateFile` /
  `ReplaceFile` / `ClaimRename` / `CrashPoint`。
- `internal/fileops/observation.go`：`Store` / `Observation` / `DiskSnapshot` / `DiskHandleSnapshot` / `LockMany`（257 分片）。
- `internal/tool/builtin/editsource.go:62`（`lockMutationPath`）、`:76`（`requireObserved`）、`:95`（`commitObservation`）。
- `internal/tool/builtin/writefile.go:56`（`Execute`）、`:68`（`confineWrite` 平价序列）、`:105`（overlay 路线）。
- `internal/tool/builtin/post_write_receipt.go:12`：`maxPostWriteReceiptBytes = 2048`。
- `internal/tool/builtin/read_snapshot.go:33`（`ExecuteRead`）、`internal/agent/read_result_envelope.go:9`（`readWindowID`）。
- `internal/tool/builtin/codeindex.go:112`（`collect(..., outline bool)`）。
- `internal/agent/tool_write_coordination.go:58`（`acquireCallWriteGuard`：先令牌后租约）、`:91`（`pathBoundWriteScope`）。
- `internal/agent/path_bound_tools.go`：`pathBoundWriterNames`、`extractWritePathsFromArgs`、`BindWritePaths`。
- `internal/boot/agent_preset.go:60`（`CoreProviderToolNames`）、`internal/boot/tool_surface.go`（`applyUnifiedProviderToolSurface`）。
- `internal/cli/team_backend_build.go:400`（`memberExtraTools`）、`:471`（`opts.ExtraTools`）。
- `internal/team/role.go:93`（L4 的 `multi_edit`/`edit_file` 纪律文本 —— 团队面一旦收窄必须同步改这里）；
  `team/skills/base/member/SKILL.md:21` 已提到「the repository's atomic/CAS write path」。

### A.4 并发/崩溃用例先例（§6）

- 多进程：`internal/workspacelease/lease_test.go`、`internal/team/sessionstore_test.go`、`internal/control/tool_recovery_crash_test.go`。
- 崩溃注入：`internal/agent/save_crash_characterization_test.go`（`crashAt`）、`session_durability_test.go`。
- 并发/等待探针风格：`internal/agent/write_intent_token_test.go`、`internal/workspacelease/yield_test.go`（禁止 sleep 计时作判定）。
