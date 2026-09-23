# 实现方案 —— 压低团队成员每步都重付的工具尾巴

> 状态：**方案，未实施**。Part A 与 Part B 文件集不相交，两个 Agent 并行，合流前不改对方的文件。
> 依据：2026-09-21 至 2026-09-23 `~/.reasonix/stats`。成员模型 `deepseek-v4-flash` 在思考循环里，缓存前缀按本步新增 token 同步变长，未命中却停在一块固定尾巴上（常见约 4000–7500 token，个别约 25000）。命中率 = `1 - 尾巴 / prompt`。`gpt-5.6-sol` 的尾巴中位数约 1050 token，prompt 到 2 万已到 95% 以上；成员尾巴要 prompt 到尾巴的约 20 倍才到 95%。
> 机制：DeepSeek 按请求前缀缓存。工具 schema 排在消息之后，每多一步新消息，整段工具定义就再次计为未命中。系统提示、`<team-role-skill>`、成员身份已经落在命中前缀里，缩短它们不提高命中率。
> 代码锚点：`internal/boot/tool_surface.go`（`applyUnifiedProviderToolSurface`、`providerVisibleTools`）、`internal/tool/tool.go`（`SetProviderVisibleTools`：不在可见名单里的工具仍可由 `use_capability` 执行）。

---

## 0. 结论

1. **只缩短每步重付的那截。** 目标是把成员请求的固定未命中从数千 token 降到「本步新增 + 一份更短的常驻工具表」。prompt 到 4 万时，命中率应到 95% 以上。
2. **成员常驻工具改成一小份白名单的补集：从现有表面减去延期名单。** 延期工具仍注册，仍可通过 `use_capability` 调用，只是不再出现在 provider schema 里。
3. **仍常驻的工具只缩短描述，不改参数语义、不改执行。** 描述里的每百 token，思考的每一步都再付一次。
4. **不折叠、不改写已经缓存的历史。** 前缀现在是稳定的；为了命中率去压缩中间消息，会把已命中的部分整段打回未命中。
5. **本方案只覆盖 `TeamRole == "member"`。** leader 与不进 team session 的基线表面保持今天的名单。leader 在 `gpt-6-sol` 上「只缓存约 1 万 token」是另一件事，不在本文。

---

## 1. 不做什么

| 不做 | 原因 |
| --- | --- |
| 缩短系统提示、角色技能、`<session-context>` 技能目录 | 这些字节在命中前缀里。缩短它们会让命中率略降 |
| 改 `CoreProviderToolNames` 的全局名单 | 基线会话和 leader 还用这份名单 |
| 把延期工具从注册表删掉 | `use_capability` 必须还能执行它们 |
| 为延期工具再写一套 schema | 执行路径用现有 schema；省的是 provider 可见那一份 |
| 改回报的存储规则（标题 + 摘要） | 已落地，本文只缩短它的工具说明 |
| 在思考循环里触发压缩 | 见结论 4 |
| 用本方案代替一次真实 `prompt_tokens` 对照 | 夹具字节对得上工具表之后，才用相邻两步的 `cache_miss` 验收 |

---

## 2. 并行边界

两个 Agent 同时开工。合流之前：

- 只改自己的文件。
- 不改本方案文档的状态行。
- 不跑对方包里的测试来「顺手修」。

| | Part A | Part B |
| --- | --- | --- |
| 内容 | 成员 provider 可见名单：从现有表面减去延期工具 | 仍常驻工具的描述与 schema 说明缩短，并改对应冻结字节 |
| 文件 | `internal/boot/tool_surface.go`；新建 `internal/boot/member_visible_tools_test.go` | `internal/tool/builtin/atomicwrite.go`（只动 `Description`）、`internal/tool/builtin/bash.go`（只动 posix 描述与 `bashToolSteer`）、`internal/cli/team_member_tools.go`（只动 `member_report_result` 的描述与 `result` 说明）、`internal/cli/team_deliverable_tools.go`（只动 publish 与 read 的 `desc`）、`internal/tool/builtin/atomicfs_write_measure_test.go`、`internal/tool/builtin/atomicfs_bash_token_test.go` |
| 不许碰 | 任何 `Description()`、schema JSON、测量冻结值、`team_member_tools.go`、`team_deliverable_tools.go` | `internal/boot/tool_surface.go`、可见名单、`member_visible_tools_test.go` |

延期名单与常驻意图以本文 §3 为准。Part B 缩短的是**仍然常驻**的说明；即使 Part A 尚未合并，这些说明今天也在成员表面上，缩短它们是独立的收益。

---

## 3. 成员可见名单（Part A 的合同）

在 `providerVisibleTools` / `applyUnifiedProviderToolSurface` 现有逻辑之后，仅当 `opts.TeamRole == "member"` 时，从已经算好的 allow 里去掉下列名字。先做原子替换（`dropSubstitutedLegacy`），再去掉延期项，这样 `atomic_fs` 关闭时 `read_file` / `write_file` / `edit_file` 还在。

延期（成员 schema 中消失，注册与 `use_capability` 保留）：

- `job_output`、`job_kill`
- `view_image`、`compress`、`web_search`
- `ask`、`create_goal`、`get_goal`、`update_goal`、`todo_write`
- `member_set_approval_mode`、`team_knowledge_recall`、`member_list_deliverables`

成员思考仍直接看见的，至少包括：`bash`、`use_capability`、文件工具（原子对，或未启用原子表面时的旧三件）、`member_get_my_task`、`member_report_result`、`member_publish_deliverable`、`member_read_deliverable`。不要在本方案里新增工具。

`TeamRole` 为空或 `"leader"` 时，allow 与今天逐字节相同。

---

## Part A —— 成员可见名单

### A1. 实现

在 `internal/boot/tool_surface.go` 增加成员延期集合，并在 `applyUnifiedProviderToolSurface` 得到 allow 且完成 `dropSubstitutedLegacy` 之后调用。函数只接收 `TeamRole` 字符串和名单，不读工具描述。

伪代码：

```go
func dropMemberDeferredTools(teamRole string, allow []string) []string
```

`teamRole != "member"` 时原样返回。比较前 `strings.TrimSpace`。未知名字忽略，不报错：名单里还没有的工具（例如某次构建没注册 `web_search`）保持现状。

不要把延期名单写进 `CoreProviderToolNames`。

### A2. 测试

新建 `internal/boot/member_visible_tools_test.go`，不导入 `internal/cli` 的工具描述断言以外的行为。覆盖：

1. `TeamRole == "member"`：延期名单中的每一个名字都不在 provider 可见集合里；§3 列出的常驻名字仍在（原子表面开启时断言 `atomic_read` / `atomic_write` 在、`read_file` / `write_file` / `edit_file` 不在；原子表面关闭时相反）。
2. 同一注册表上，延期工具 `Get` 仍成功。这是「还能执行」的代理；本测试不调用 `use_capability`。
3. `TeamRole == ""` 与 `TeamRole == "leader"`：可见名单与调用 `dropMemberDeferredTools` 之前相同。

运行：

```text
go test ./internal/boot/ -count=1 -run 'TestMemberVisibleTools'
```

### A3. 完成标准

- 只改 §2 表里 Part A 的文件。
- 成员可见集合少了延期名单，leader 与基线集合不变。
- 不改任何工具的描述字节，因此不改 `frozenAtomicWriteBytes` 一类冻结值。

---

## Part B —— 缩短仍常驻的说明

只改描述文本和已经冻结这些字节的测试。不改 schema 的字段、枚举、`required`，不改执行。

### B1. 文案

每条都比今天短，并保留模型靠它才能做对的那半句。

| 工具 | 位置 | 必须留下的意思 |
| --- | --- | --- |
| `atomic_write` | `Description()` | `create` / `replace` / `append` / `patch` / `delete`；`patch` 用 `edits`、`range` 或 `symbol`；拒绝与已读内容不一致的覆盖；`ops` 是一次事务；回执有界、不回文件正文 |
| `bash`（posix 返回值与 `bashToolSteer`） | `bash.go` | 跑一条命令，回合并的 stdout/stderr；写工作区外要 `additional_write_dirs`，宿主不从命令文本推断写路径。`bashToolSteer` 改成一句：搜、读、改文件用 `atomic_read` / `atomic_write`，不要用 `cat` / `sed` / `>>`。删掉对 `grep`、`read_file`、`ls`、`glob`、`edit_file`、`move_file`、LSP 的罗列 |
| PowerShell 分支 | `bash.go` | 不动那组语法条目。`bashToolSteer` 缩短后两边共用，这是预期 |
| `member_report_result` | `team_member_tools.go` 的描述与 `result.description` | 默认 `report` 只存第一行标题和随后的短摘要，更长的正文不入库；全文用 `member_publish_deliverable` 并在摘要里引用 id。`operation=read` 不完成任务。schema 的 `enum` 与键名不变 |
| `member_publish_deliverable` | `team_deliverable_tools.go` 的 `desc` | 发布后返回稳定 id；相同正文再次发布是 no-op |
| `member_read_deliverable` | 同文件 read 的 `desc` | 默认大纲；`mode=full` 才是正文。不要在描述里鼓励 `mode=full` |

`member_list_deliverables`、`member_set_approval_mode`、`team_knowledge_recall` 的描述留给 Part A 移出表面，Part B 不改它们。

`docs/TOOL_CONTRACT.md` 与中文半句：只有某句是这些描述的逐字拷贝时才改成新半句。没有逐字拷贝就不要为了本文扩写契约。

### B2. 冻结值

`atomic_write` 与 bash 的测量测试把 schema+描述的字节冻成实测值。描述变短之后：

1. 跑测量测试，读出新的字节数。
2. 把冻结常量改成实测值，不要靠原来的余量把变短假装成没变。
3. 门禁保持「原子对 < `read_file` + `write_file` + `edit_file`」。变短应使这个不等式更松，而不是破坏它。

运行：

```text
go test ./internal/tool/builtin/ -count=1 -run 'TestAtomicWriteMeasurePrefixBudget|TestAtomicVsBashPrefixCost'
go test ./internal/cli/ -count=1 -run 'TestMemberReport|TestDeliverable'
```

第二行按仓库里已有的报告与交付物测试名收窄；没有匹配测试时不要新写行为测试，描述缩短没有新的执行分支。

### B3. 完成标准

- 只改 §2 表里 Part B 的文件（以及确有逐字拷贝时的两份 `TOOL_CONTRACT`）。
- 常驻描述短于实施前。
- `atomic_write` 的 schema JSON（`atomicWriteSchema`）字节不变。
- 报告仍只持久化标题和摘要；本部分不改 `reportTitleAndSummary`。

---

## 4. 合流

两支都落地之后，由合流的人做一次，不并进任一支的中途提交：

1. 成员可见工具的 schema+描述合计字节低于实施前。实施前先在合流环境量一次，把数字写回本文状态行。目标量级：延期名单移出后，固定尾巴里的工具部分应降到大约今天的一半以下；Part B 的缩短算在同一笔里。
2. `go test ./internal/boot/ -count=1 -run 'TestMemberVisibleTools'` 与 §B2 的测量测试一起绿。
3. 不在本文里改 leader 名单，也不改基线 `CoreProviderToolNames`。

真实流量验收（合流之后、另一次成员思考，不阻塞两支开发）：

- 同一条思考链上相邻两步，`cache_miss` 接近「本步新增 token」，不再停在 4000 以上的平台上。
- prompt ≥ 40000 时，该步命中率 ≥ 95%。
- 若某次会话的未命中仍停在约 25000，先查 provider 可见集合里有没有 MCP 工具的整份 schema。本文的延期名单不包含 MCP；那笔要单独把 MCP 留在 `use_capability` 后面，不在 Part A/B 里顺手做。
