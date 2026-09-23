# 实现方案 —— `atomic_write` 的符号锚定局部替换（`patch` + `symbol`）

> 状态：**已实施**（§7 的五步已落地；`cmd/e2ebench` live 计量仍未做，见 §6.3）。
> 实施回填（`go test ./internal/tool/builtin/ -run 'TestAtomicWriteMeasurePrefixBudget|TestAtomicVsBashPrefixCost' -v -count=1`）：
> `atomic_write` 单件 **1029B**（原 926B，冻结值已按实测改为 1029B）、原子对 **1671B**、原子对+bash **4058B**；
> 门禁仍是「原子对 < 旧三件」（1671B < 1845B，−174B/成员/轮）。夹具行：`atomic symbol patch` 参数 99B、结果 124B、
> 调用合计 **223B**，**小于**「window + `edits`」的 870B，且回执不含被替换正文。
> 依据：`docs/team-mcp-port/ATOMIC_FS_VS_BASH_TOKEN_MEASUREMENT.md` 的夹具对比，以及
> `docs/team-mcp-port/TEAM_ATOMIC_FS_TOOLS_ROUTE.md` 已落地的 `atomic_read` / `atomic_write`。
> 代码锚点：`internal/tool/builtin/atomicwrite.go`（`applyPatch`）、`atomicread_outline.go`
> （`atomicOutlineSymbols`）、`codeindex.go`（`codeSymbol` 只有起始行）。
> 一句话：模型已经知道要改哪个符号时，宿主在一次 `patch` 里定位、替换并只回有界回执，
> 不再为了锚点把编号窗口送回模型。

---

## 0. 结论

1. **不加第三个 provider 工具。** 前缀（schema + 描述）每个回合都付。测量里原子对替换旧三件套净省 277B/轮，bash 仍在面上。再挂一个「局部编辑」工具会把这笔净省吃掉。
2. **能力挂在已有 `mode=patch` 上**，增加可选字段 `symbol`。与 `edits`、`range` 三者互斥。`atomic_read` 的窗口 / 大纲 / 增量保持不变，模型必须先看代码时继续走读。
3. **成功路径不回窗口、不回被替换正文。** 回执沿用现有 `patch <path> … lines`，多一个符号名和行区间，便于核对，不便于把正文再抄进上下文。
4. **符号解析失败就拒绝，不猜测。** 0 命中、多命中、区间超上限，文件一律不动。恢复文案指向 `atomic_read mode=outline` 或已有的 `range` / `edits`。
5. **这是 `patch` 里唯一不要求事先 `atomic_read` 的定位方式。** 锚点在同一次调用里、写锁之下捕获，CAS 仍在发布前复核。`edits` 和 `range` 的「先读」约束不动。

测量已经说明局部读写省在哪里：2000 行里改 5 行，原子对调用合计 869B，其中参数 136B、结果 733B。贵的是为了可引用行号而回传的窗口。窗口读本身比 `sed -n` 贵 25%（2947B vs 2347B），那 25% 是 `N→` 前缀，不能拿掉。本方案省的是「已知符号就不必再付那次窗口」。

---

## 1. 不做什么

| 不做 | 原因 |
| --- | --- |
| 新工具名（`span_edit` 等） | 每轮前缀账单；见 §5 |
| 去掉窗口行号，让 `atomic_read window` 压过 `sed -n` | 行号是 `range` 与后续 `edits` 的锚；测量把这笔标成原子的可引用成本 |
| 为追加再做一条通道 | 追加只贵 127B，收益在单次 `write(2)` 和路径租约，不在格式 |
| 用缩进块（`atomicIndentSymbols`）当写区间 | 大纲可以退化成「前 40 行」；写不能按这个退化去切文件 |
| 静默选第一个同名符号 | 多命中必须拒绝并列出候选行 |
| 把旧函数正文放进成功回执 | 那就把刚省下的窗口结果又写回来了 |
| 用本方案代替 `cmd/e2ebench` 两臂 | 夹具字节 ≠ `prompt_tokens`；见 §6.3 |

---

## 2. 接口

### 2.1 schema 与描述（增量，实施时改冻结串）

在 `atomicWriteSchema` 的 `properties` 增加：

```json
"symbol": {"type": "string"}
```

`required` 仍是 `["path"]`。`symbol` 可选。provider schema 与 `ArgumentContractSchema` 的差异维持现状：根上的 `required` 只在对外 schema 里，校验变体丢掉它，以便 `ops` 可以不带顶层 `path`。

描述在现有 `patch` 半句后加一句，控制在一行以内：

```
symbol names one outline symbol and replaces its whole span; no prior read.
```

中文契约（`docs/TOOL_CONTRACT.zh-CN.md`）同步半句：`symbol` 按大纲里的一个符号替换其整个区间，不必先读。英文 `docs/TOOL_CONTRACT.md` 同样半句。

`internal/tool/builtin/atomicwrite_test.go` 里「schema 含 `"required":["path"]`」的断言保留。`atomicfs_write_measure_test.go` 的 `frozenAtomicWriteBytes = 926` **不能靠 +16 的余量吞掉这段增长**：实施时先量新的 schema+描述字节，把冻结值改成实测值，门禁仍是「原子对 < 旧三件套」（`TestAtomicWriteMeasurePrefixBudget`）。超出三件套即失败，不准把描述写长来解释 `symbol`。

### 2.2 参数互斥

`patch` 的定位三选一：

| 定位 | 字段 | 事先读过 | 替换范围 |
| --- | --- | --- | --- |
| 精确文本 | `edits:[{old,new}]` | 需要（或 `since`） | `old` 的唯一命中 |
| 行区间 | `range` + `content` | 需要（或 `since`） | `[start,end]` 行，1-based 闭区间 |
| 符号 | `symbol` + `content` | **不需要** | 该符号的整个区间（见 §3） |

同时给出两种或三种定位 → 拒绝，文案与今天的 `patch takes edits or range, not both` 同一风格：`patch takes edits, range, or symbol, not a combination`。

`symbol` 缺 `content`、或 `content` 为空 → 拒绝。删掉一个符号用 `range` 或 `edits`，不用空 `content` 表示删除。

`ops[]` 的每个子项可以带自己的 `symbol`，规则与单文件相同。路径抽取不变：仍是 `path` 与 `ops[].path`（`internal/agent/path_bound_tools.go`）。`symbol` 不是路径。

### 2.3 成功回执

与 `range` 一样走摘要行，不走 `edits` 的 `withActualPostWriteReceipts`（那会附匹配 span）：

```
patch <path> <nbytes>B <nlines> lines <oldLines>→<newLines> lines symbol <name> <start>-<end>
```

`name` 用解析后的展示名（方法为 `Parent.Name`）。区间是替换前的 1-based 闭区间。回执里不得出现被替换区间的原文，也不得出现 `content` 正文。现有「回执不回显文件」用例按这条加一个 `symbol` 样本。

内容与现状一致时，沿用 `unchanged: the patch matches the current content`，并带上 `symbol` 与区间，方便模型确认对上了，而不是没找到。

### 2.4 失败形状

全部在写盘之前返回。文件字节不变。统一 `tool.OperationError`，能复用的码不新造。

| 场景 | Code | Recovery 要点 |
| --- | --- | --- |
| `symbol` 与 `edits`/`range` 同时出现，或缺少 `content` | 普通参数错误（与现有 patch 互斥相同，不新增码） | 三选一 |
| 0 个符号 | `FSNotObserved` | `atomic_read mode=outline`，或改用 `range`/`edits` |
| 2 个及以上同名 | 复用 `old_string` 不唯一的错误形状（列出候选，不附正文） | 改用 `Type.Name`，或改用 `range` |
| 解析出的区间超过上限（§3.3） | `FSTooLarge` | 改用 `range` 或 `edits`，先 `atomic_read window` |
| 无可用符号表（纯缩进退化、二进制、解析失败） | `FSNotObserved` | 此文件没有可写的符号表；用 `window` + `range`/`edits` |
| 发布前复核与本次调用开头捕获的锚点不一致 | `FSStaleVersion` | 与现有 patch 相同：附变更 hunk，不改文件 |
| 符号在复核后的字节里对不上开头捕获时的区间 | `FSStaleVersion` | 重试一次 `symbol`（宿主会按新字节重解析），或改 `window` |

候选列表有界：最多 8 行，每行 `行号→kind 展示名`，与大纲标签同形（`atomicOutlineLabel`）。不回函数体。

---

## 3. 区间怎么算

### 3.1 符号从哪来

只调用已经在读路径上用的收集器，并且**对本次调用读到的字节**收集，不另开一次磁盘读，也不调用 `code_index` 工具：

`atomicOutlineSymbols` 的三条命中路径，按它现有的顺序：

1. `.md` / `.markdown`：`atomicMarkdownSymbols`
2. `.go`：`atomicGoSymbols` → `goSymbols`（内存里 `parser.ParseFile`，与大纲一致）
3. 其余：`atomicMatcherSymbols`（`codeIndexMatchers`）

**不使用** `atomicIndentSymbols`，也不使用大纲在零符号时退化的头 40 行。那两条只服务读。写若跟着退化，会把「没有符号」切成一大段去替换。

`codeSymbol` 今天只有 `Line`（起始行），没有结束行。本方案不改 `code_index` 的对外结果，也不给大纲多打一列结束行（那会加长每一次 outline）。结束行只在写路径上现算。

### 3.2 匹配

`symbol` 去掉首尾空白后与展示名比较，大小写敏感，与源码一致：

- `Name` 命中，或
- `Parent.Name` 命中（大纲里已经印成 `Parent.Name`）

展示名相同的全部算命中。1 个 → 采用。0 个 → §2.4 的零命中。2 个及以上 → 不唯一，即使调用方只写了短名。不允许「短名在全文件只出现一次就自动选中方法」之外的模糊：短名对应多个 `codeSymbol` 就拒绝。

Go 的 `func Target` 与 `func (s *Server) Target` 是两个符号。`Target` 在两者都在时拒绝；`Server.Target` 只命中方法。

### 3.3 起止行

命中符号的起始行是 `codeSymbol.Line`（1-based，与窗口行号相同）。

结束行是**下一个符号的起始行减 1**。没有下一个符号则到文件最后一行。符号按 `Line` 升序；同一行多个符号时，结束行不超过起始行（该行本身），避免零长度或交叉。

这个规则对 Go 的顶层函数是对的：下一个 `func` / `type` 之前就是该函数。它也会把一个 Markdown 一级标题一直延伸到下一个标题。因此加硬上限，超出则拒绝而不是截断后照写（截断会改掉调用方没点名的后半段）：

- 区间行数 ≤ `atomicAutoWholeMaxLines`（400，与 `auto` 整文件阈值同一常量）
- 区间字节 ≤ `atomicReadBudgetBytes`（16KB）

两条都满足才 splice。用 `atomicSpliceRange` 做替换，不复制第二套拼接。`content` 的换行处理沿用 `atomicSpliceRange`：去掉替换文本末尾一个换行，再按行拼回，并保留原文件的末尾换行约定。

区间外的字节按构造不变。这是 `patch` 相对整文件 `replace` 的原有性质，符号路径不得改掉。

### 3.4 与「先读」的关系

`edits` / `range` 继续 `anchorFor` + `atomicRecheckSource`：没有本会话观察也没有 `since` 就 `FSNotObserved`。

`symbol` 分支：

1. 走与 `atomic_read` 相同的路线取源（`readEditSource`，overlay 优先）。这一步本身就是本次调用的观察。
2. 在**这批字节**上解析符号并 splice。解析与拼接之间不再读盘。
3. 发布仍走 `src.write` → `AtomicOverwriteFileStrict`（或 overlay 的既有写）。发布前若源版本与步骤 1 的锚点不一致，`FSStaleVersion`，与 §3.2（原子读写方案）同一窗口：校验到 rename。
4. 若调用方带了 `since`，步骤 1 改为 `atomicAnchorFor` + `atomicRecheckSource`，符号在复核后的字节上解析。不允许「按模型记忆里的旧大纲行号去切」。

同进程互斥仍是 `fileops.LockMany`，跨进程仍是写租约与团队写令牌。工具自己不取 L2/L3。符号路径不新增锁。

外部非 Reasonix 写者的口径不变：检测并拒绝，不阻止。

---

## 4. 改哪些文件

只动写路径和它的契约说明。不改 `atomic_read` 的模式、预算和行号格式。

| 文件 | 改动 |
| --- | --- |
| `internal/tool/builtin/atomicwrite.go` | schema、描述、`atomicWriteParams.Symbol`；`applyPatch` 增加第三分支并收紧互斥 |
| `internal/tool/builtin/atomicwrite_symbol.go`（新） | `atomicResolveSymbolSpan(content, path, symbol) (start, end int, label string, err error)`。只做匹配和区间，不做 I/O |
| `internal/tool/builtin/atomicwrite_ops.go` | 子项透传 `symbol`；prepare 阶段用同一解析，失败则零提交 |
| `internal/tool/builtin/atomicwrite_test.go` | §6.1 的行为用例 |
| `internal/tool/builtin/atomicfs_bash_token_test.go` | §6.2 的夹具行：符号 patch vs 现有 window+`edits` |
| `internal/tool/builtin/atomicfs_write_measure_test.go` | 按实测更新 `frozenAtomicWriteBytes`；对 < 三件套的门禁不放宽 |
| `docs/TOOL_CONTRACT.md`、`docs/TOOL_CONTRACT.zh-CN.md` | 各半句 |
| `team/skills/base/member/SKILL.md`、`team/skills/base/leader/SKILL.md` | 现有「文件用原子工具」那一行不改。符号是 `patch` 的一种参数，不增加技能正文 |

`workspace.go`、`path_bound_tools.go`、团队 surface、`tools.atomic_fs` 开关都不改。`symbol` 不是新的写入口。

`Preview` 继续产出审批 diff（宿主可见）。模型可见的 `Execute` 结果仍是 §2.3 的回执。

---

## 5. 前缀怎么守

实施前后各跑一次：

```bash
go test ./internal/tool/builtin/ -run 'TestAtomicWriteMeasurePrefixBudget|TestAtomicVsBashPrefixCost' -v -count=1
```

记录三行：`atomic_write` 单独、原子对、原子对+bash。

通过条件：

- 原子对字节 **小于** `read_file`+`write_file`+`edit_file`（现有断言）。
- `atomic_write` 的冻结预算改为本次实测值，后续增长仍被 `+16` 抓住。
- 描述新增不超过 §2.1 那一句。schema 只多一个 `symbol` 字符串属性。

若实测后原子对 ≥ 三件套，收紧描述直到回到净省，而不是接受「功能值回前缀」。

---

## 6. 验收

### 6.1 行为

夹具用临时目录，不碰真实会话库。

1. **唯一 Go 函数。** 文件中段 `func Target() int { return 1 }`，`symbol=Target`，`content` 为改后的完整函数。文件中只有该函数变化；回执含 `symbol Target` 与旧区间；回执不含 `return 1` 与新正文。调用前不执行 `atomic_read`。
2. **同名拒绝。** 两个 `Target`。文件不变；错误列出两行候选；不含函数体。
3. **限定名。** `func (s *Server) Target` 与顶层 `func Target` 共存。`symbol=Server.Target` 只改方法。
4. **互斥。** `symbol`+`edits`、`symbol`+`range` 都拒绝，文件不变。
5. **空 content。** 拒绝，文件不变。
6. **零符号文件。** 纯散文 `.txt`。拒绝，并说明没有可写符号表。不得按缩进块去切。
7. **Markdown 标题。** 两个 `##` 之间的一节被替换；下一节字节不变。
8. **超上限。** 构造一个符号区间 > 400 行。拒绝，`FSTooLarge`，文件不变。
9. **CAS。** `since` 指向一次读之后、外部改了文件。`FSStaleVersion`，不发布。
10. **`edits`/`range` 仍要先读。** 回归：不带观察、不带 `since` 的 `edits` patch 仍是 `FSNotObserved`。符号分支的豁免不得漏到这两条。
11. **overlay。** 目标在未保存缓冲里时，符号解析用缓冲字节，写回缓冲，不绕过 `readEditSource`。
12. **`ops`。** 两个文件各一个 `symbol` patch，prepare 阶段其中一个零命中 → 两个文件都不变。

### 6.2 夹具字节

在 `TestAtomicVsBashTokenComparison` 的同一 2000 行夹具上加一行，不替换现有四行：

- 形状：`atomic symbol patch`（无事先 window）。
- 参数：`path` + `mode=patch` + `symbol` + 替换后的函数 `content`。
- 结果：§2.3 回执的实际字节。
- 断言：该调用合计 **小于** 现有「window + `edits`」的 869B，且结果中不含被替换函数的旧正文。

这一行证明的是夹具，不是会话。断言钉字节，token 列仍是 `⌈字节/4⌉` 估算。

### 6.3 本方案明确不测量的

`cmd/e2ebench` 的 `atomic_fs=off` 与 `team` 两臂，以及其中「先 window 再 patch」与「直接 symbol patch」的分开计数。那是发布前验证，做了才能说真实 `prompt_tokens` 下降。本文件的夹具门禁不代替它。

### 6.4 命令

```bash
gofmt -l internal/tool/builtin/atomicwrite.go internal/tool/builtin/atomicwrite_symbol.go internal/tool/builtin/atomicwrite_ops.go
go test ./internal/tool/builtin/ -count=1 -run 'TestAtomicWrite|TestAtomicVsBash'
```

改动的 Go 文件按仓库惯例格式化。不新增依赖。

---

## 7. 施工顺序

一个提交即可，自含可编译：

`feat(atomic-fs): patch a single outline symbol without a window round-trip`

顺序：

1. `atomicResolveSymbolSpan` 与它的纯函数单测（0 命中、多命中、限定名、超上限、不用缩进退化）。
2. `applyPatch` 第三分支、回执、`ops` 透传。
3. §6.1 的写路径用例，先让「未读即可 symbol patch」和「未读的 edits 仍拒绝」同时为绿。
4. 量前缀，更新 `frozenAtomicWriteBytes`，补夹具行。
5. 两份 `TOOL_CONTRACT` 各半句。

不 push。默认开关仍是 `tools.atomic_fs = team`；本改动不翻转开关。
