# Session archive and recovery upgrade / 会话归档与恢复升级

## User behavior / 用户行为

Archive remains available. Archived sessions move out of the sidebar into
Trash. Canonical history is retained in place. Restore makes the
original workspace visible and opens the same SessionRef. A missing project
directory does not authorize moving the session into the current project.

保留归档能力。归档后会话从侧栏移入“回收站”，canonical 正文原位保留。
恢复重新显示原工作区，并打开同一 SessionRef；原项目目录离线不能作为改变归属的理由。

Trash provides read-only preview, restore, permanent deletion and empty-trash.
Historical recovery is a separate auxiliary entry, not another lifecycle tab. Historical
entries with ambiguous old `.trash` intent are excluded from bulk purge.
Importing historical data preserves the original files. A successful restore
followed by a list refresh failure is reported as restored with a refresh
failure; retry must not create another session.

回收站提供只读预览、恢复、彻底删除及清空。“历史待恢复”是独立辅助入口，
不设置“已删除”可恢复分页。无法确认旧 `.trash` 操作语义的
记录进入历史待恢复，不参与一键清空。恢复历史资料保留旧原件；持久化恢复成功后
若列表刷新失败，显示“已恢复，列表刷新失败”，重试不得重复创建会话。

## Authority and compatibility / 权威状态与兼容边界

| Owner / 所有者 | Responsibility / 职责 |
| --- | --- |
| Canonical store | Headers, events, messages and content references / 会话头、事件、正文及内容引用 |
| Upstream migration ledger (#10395) | Source/head/conversion receipts and provenance / 来源、head、转换回执与谱系 |
| Registry schema 2 | Workspace membership, lifecycle, provenance and replay operations / 工作区成员、生命周期、来源映射及可重放操作 |
| Indexes and legacy projections | Discovery and presentation only / 仅负责发现及展示 |

The registry stays at `desktop/workspace-state-v1.json`; its internal version
is 2. Do not create a parallel empty registry. Version 1 is backed up before
the first schema write. Unknown JSON fields survive read/modify/write,
including nested records. Unsupported schemas and states fail closed.
`archivedSessionIds` is a derived compatibility view of `sessionStates`.

注册表路径仍是 `desktop/workspace-state-v1.json`，内部版本升级为 2，不能另建空
注册表。第一次结构写入前备份 v1 原件；读改写保留未知 JSON 字段及嵌套对象。
未知版本、未知状态必须明确失败。`archivedSessionIds` 只从 `sessionStates` 派生。

Desktop directory version 5, registry schema 2 and the canonical codec are
independent. This change does not rewrite codec semantics, message IDs,
provider-visible content or event order. Local shell/host protocol is 10;
remote Serve protocol is unchanged.

Desktop 目录 v5、注册表 schema 2 与正文 codec 分别管理。本次不改变 codec 语义、
消息 ID、provider 可见内容和事件顺序。本地 shell/host 协议为 10，远端 Serve 不变。

## Historical sources / 历史来源

| Format / 格式 | New reader / 新版读取 | Previous writer / 旧版写入 | Boundary / 边界 |
| --- | --- | --- | --- |
| Registry schema 1 | Convert with immutable backup / 备份后转换 | Retained backup only / 仅独立旧备份 | Forward upgrade / 单向升级 |
| Registry schema 2 | Read and preserve unknown fields / 读取并保留未知字段 | Schema-1 reader rejects writes / schema-1 程序拒绝写入 | No shared editing with old releases / 不支持新旧共享编辑 |
| Future registry schema/state | Explicit error, leave intact / 明确错误并保留 | Unsupported / 不支持 | Never interpret as empty / 不解释为空 |
| Local host protocol 10 | Matching shell/service only / shell 与 service 必须匹配 | Protocol 9 handshake rejected / 拒绝协议 9 握手 | Serve unaffected / 不影响 Serve |

| Source / 来源 | Handling / 处理 |
| --- | --- |
| Legacy JSONL, checkpoint, schema 1 | Existing import adapter; keep originals / 复用适配器导入，保留原件 |
| Schema 2 DAG | Upstream live-head discovery and proven lineage; keep incomparable histories / 沿用上游存活 head 与已证明谱系，保留不可比较历史 |
| v3, v3.1 and v4 draft | Explicit existing preview adapter into isolated staging; validate before registration / 现有预览适配器隔离导入，校验后登记 |
| Canonical v4 | Validate and copy through canonical export/import / 校验后经 canonical 导出导入 |
| Desktop v5 | Reuse IDs; repair unambiguous missing membership / 复用 ID，补登记可确定归属的成员 |
| Historical `.trash` | Proven recoverable trash becomes archived; ambiguous intent needs recovery review; retain originals / 明确可恢复记录导入为归档，语义不明进入待恢复，保留原件 |
| Unknown/damaged/conflicting sources | Preserve and diagnose; never merge by title or message equality / 保留并诊断，不按标题或正文相同合并 |

A source key includes filesystem identity and an explicit historical head when
one is selected. The fingerprint covers transcript/event bytes, not mutable
title/index sidecars. Continued destinations are never overwritten by old
snapshots. Changed sources receive distinct recovery versions. Original DAGs
and unmapped auxiliary records remain in the legacy source directories.

来源 key 包含文件系统身份和明确选定的历史 head。指纹覆盖正文与事件，不把可变
标题、索引 sidecar 当成正文变化。新会话继续写入后不能被旧快照覆盖；来源变化形成
独立待恢复版本。原 DAG 和尚无新格式映射的附属记录保留在旧目录中。

## Durability and retry / 持久化与重试

Operations progress through `prepared → content_ready → committed`.
Publication reserves target IDs before publishing content. Registry commit
atomically changes membership, lifecycle, source mapping and the completion
receipt. Topic archive commits staged imports as dependencies in the same
registry replacement. A failed target does not partially archive the batch.

操作按 `prepared → content_ready → committed` 推进。发布正文前先持久化目标 ID；
注册表原子提交成员、生命周期、来源映射和完成结果。主题归档将隔离导入作为依赖
一并提交，单项失败不能造成部分归档。

Replay validates content, workspace and writer ownership again. An external
writer blocks lifecycle replay. Committed restore requests return their
original SessionRef and generation even if the old source is now offline.
Registry corruption stops writes; do not replace it with an empty file.

重放重新校验正文、工作区和写入所有权。外部 writer 占用时暂停重放。已提交恢复
请求即使旧来源离线，仍返回原 SessionRef 与 generation。注册表损坏时停止写入，
不得用空文件替换。

Runtime rebinding removes obsolete aliases, checks the published SessionRef
on lookup and fences events by generation. Task cancellation verifies both
session identity and the recorder's runtime owner, so an old `task-1` cannot
cancel the replacement session's `task-1`.

运行时重绑定删除过期别名，查找时核验已发布 SessionRef，事件按 generation 隔离。
停止任务同时校验会话与 recorder 的 runtime owner，避免旧 `task-1` 控制新会话同名任务。

## Backups and rollback / 备份与回滚

Permanent deletion uses `prepared → tombstoned → content_removed → committed`.
It requires an archived, idle identity and filesystem ownership, retains a
durable anti-resurrection tombstone, and validates a staging receipt on retry.
Interrupted deletion remains visible in Trash. Shared objects, upgrade originals
and independent backups are retained; this is not secure erasure. Empty-trash
freezes the confirmed target set and reports partial failure.

彻底删除按上述独立阶段推进，只允许已归档且空闲的身份，在文件所有权保护下写入
防复活墓碑。重试校验暂存凭据，删除未完成的条目仍显示在回收站。共享对象、升级
原件和独立备份保留，不承诺安全擦除。清空冻结确认时的目标集合并反馈部分失败。

Metadata backups are immutable and content-addressed. They include the
registry, project/tab state, migration ledger, legacy topic JSON and consistent
SQLite topic snapshots including committed WAL pages and unknown tables.
Backup failure prevents the schema upgrade. Large history originals remain
in place rather than being destructively moved.

元数据备份按内容寻址且不可覆盖，包括注册表、项目/标签状态、迁移台账、旧主题
JSON 和包含已提交 WAL 及未知表的 SQLite 一致性快照。备份失败不得升级结构；
大文件正文原位保留，不做破坏性迁移。

Only newer readers/writers may use schema 2. To run an older application,
restore pre-upgrade metadata into an **independent copy** of the old data.
Never overwrite the current registry with a pre-upgrade backup after new
sessions have been created. A production rollback needs a build that still
understands schema 2. This work does not authorize a public release.

只有支持 schema 2 的程序能使用升级数据。运行旧程序时，在**独立数据副本**中
恢复升级前资料。产生新会话后，不能把旧注册表覆盖到当前目录。线上回退应使用
仍理解 schema 2 的修复构建。本次实施不包含正式发布。

## Qualification / 验收

Release requires archive → restart → restore → sidebar → readable history →
restart, failure/replay tests, identity and task isolation, separate root and
Desktop Go suites, race checks, frontend tests/typecheck/build and a real
production Electron package with protocol handshake and clean exit. Native
Windows/Linux runs must be reported separately from cross-compilation.

发布门禁包括归档→重启→恢复→侧栏可见→正文可读→再次重启、失败重放、身份与任务
隔离、根模块与 Desktop 独立测试、竞态检查、前端测试/类型检查/构建，以及真实
Electron 生产包握手与干净退出。Windows/Linux 原生运行与交叉编译必须分别报告。
