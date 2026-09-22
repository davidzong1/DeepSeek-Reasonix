# Manual conversation creation and upgraded data

This is a forward fix for the draft-first behavior introduced by #10469. It is
intended for the next incremented Desktop release, with a matching Electron
shell and Go service. It does not replace historical tags or downgrade storage.

## User behavior

Each explicit local New Conversation action reserves its own operation, Session
and Topic identities, persists the canonical session, registers it with the
workspace, and starts its runtime. Retrying an operation keeps those identities.
Navigation completion is independent: background creation cannot replace a newer
selection. A failed operation remains visible for retry; existing empty sessions
are not reused. Remote, IM, automation and worktree creation keep their own APIs.

The last closed or archived conversation leaves an empty welcome page. Choose a
project and explicitly create a conversation to get an editor. Closing is not
deletion. Archiving retains unsent input; permanent deletion cleans its input
only after the canonical purge has established the tombstone.

## Input and recovery

`desktop/session-ui-v1.sqlite` holds creation operations, input revisions,
submission associations and conflict copies. Chat history remains in Session v5;
workspace ownership remains in registry v3. No UI operation identity or input
storage metadata is inserted into provider messages.

Text, attachment references, paste blocks, invocations, selected text and history
references are saved by full SessionRef, with a 250 ms debounce and revision CAS.
Pending Goal mode is saved with input; effective model and permission settings
remain owned by the formal session. Image previews and browser File objects are
not serialized. Missing files remain visible for repair. Delayed work retains
its original session target. Conflicting versions are kept and can be inspected.

A normal application exit waits for input and attachment work to persist. Save
failure retains the window. A crash can only recover the last acknowledged
database revision. Sending persists an association with the source revision and
submission identity first. Input is cleared after acceptance; a lost reply is
checked against durable receipts. Reupgrade compares both accepted submission
identities and canonical user-message history, including old/imported writes
without a SubmissionID. Assistant streaming and model-setting changes do not
invalidate unsent input. An absent ordinary user-message row is never
proof that a shell or management command did not execute. Unknown results remain
recoverable and are not automatically sent again.

## Old data

`desktop/drafts-v1.sqlite` remains in place. Previous drafts, including settings-only
drafts, can be opened, edited, submitted or discarded through the recovery entry.
The old open API only returns an existing draft. It cannot create a replacement.
Submission reconciliation, cancellation ownership and leases remain active.
Interrupted dispatch is checked, not replayed. Archived or deleted targets must
not be resurrected by recovery.

Before a schema 1–3 draft database is upgraded, SQLite `VACUUM INTO` creates a
consistent `.pre-v4.sqlite` backup including committed WAL content. Existing v4
data is not destructively migrated. Unknown future formats are left untouched.

Automatic empty-session batch registration and retry are disabled. The existing
`legacy-empty-session-cleanup-v1.json` remains readable; committed archives can
be reconciled, and trash can be manually restored. Pending candidates stay put.

| Route | Compatibility |
| --- | --- |
| 1.38.3–1.38.8 → this release | Tagged JSONL/event histories and revision-1 framed stores remain explicit import sources |
| 1.38.9 → this release | Existing migration chain upgrades registry v1 and reads history |
| 1.38.10 / 1.38.11 / subsequent data → this release | Registry v3, Session v5 and existing draft records retained |
| This release → 1.38.10 | Registry v3 remains readable, but its revision-2 session reader rejects revision 3 introduced by #10545; whole-session downgrade is unsupported |
| This release → 1.38.11 | Tested tagged reader/writer accepts the shared session format; the old binary ignores the independent UI input file |
| Reupgrade | UI input is retained; changed accepted history requires review before sending |
| Downgrade to 1.38.9 | Unsupported: its registry reader cannot read v3 |
| Unknown future format | Refuse writes; preserve the file |

Older binaries keep their own cleanup behavior. Never run different versions
against one directory without the existing instance coordination and write locks.
Recovery does not require deleting a data directory, clearing caches or reinstalling.

## 中文说明

本次通过新的递增版本回滚 #10469 的用户行为，保留 Session v5、工作区注册表 v3
及后续归档、恢复、历史加载修复，不是安装旧版。

每次点击本地“新建会话”立即创建独立正式会话并启动运行时，不复用已有空会话。
首次发送使用该会话。关闭或归档最后一个会话显示欢迎页，不自动补建。
创建失败保留操作和原身份，可重试；后台完成不抢占最新页面。

正式会话的未发送内容按 SessionRef 保存到独立 SQLite 文件，支持切换和重启恢复。
正常退出等待保存，保存失败保留窗口；异常退出只恢复已经确认落盘的版本。
跨窗口冲突保留双方副本；发送结果未知时保留恢复状态，不自动重发。
图片预览重新生成，用户原始附件不因丢弃输入而删除。

“旧版草稿”入口保留升级前的草稿、设置和未完成提交。旧草稿不批量搬移、不自动
过期，处理后自然隐藏。旧 schema 升级前先生成包含 WAL 内容的一致性备份。
未知版本和损坏记录保留原文件并显示错误。

再次升级时，同时比较接收回执和正式用户消息历史，覆盖没有 SubmissionID 的旧路径
或导入写入。历史推进后保留原输入并要求核实；模型设置变化、助手流式输出不会单独
触发此限制。旧版归档的会话继续保持归档，永久删除后的迟到输入保存不能将会话复活。

历史会话夹具来自 1.38.3–1.38.11 九个发布标签的真实存储实现；注册表覆盖 1.38.9 的 v1
及 1.38.10、1.38.11 的 v3。#10469、#10572 中间构建及 1.38.11 的旧草稿覆盖未发送、
仅设置、已转换、全部提交阶段、冻结设置、缺失附件及冲突副本。
夹具只含合成数据，生成脚本不读取用户数据目录。

停用历史空会话自动清理；已在回收站的内容保持原位，可以手动恢复。升级不改写为
旧存储格式。不支持降级到 1.38.9；1.38.10 虽然可读注册表 v3，但不能读取 #10545
引入的会话物理格式 revision 3，不能作为整套会话的降级路线。1.38.11 的真实存储读写器
已通过往返验证；它保留但不能读写新增输入文件。再次升级后，历史已推进的输入需要核实，
不自动发送。完整的版本、PR 和新增测试证据见 [兼容测试报告](testing/manual-session-compatibility.md)。

## Verification scope

Release qualification must include isolated packaged macOS, Windows and Linux
application runs. Development-server tests cannot replace SQLite file-release,
normal exit, crash/restart, attachment recovery and shell/service handshake checks.
The implementation does not itself publish a release.

### Local qualification — 2026-09-22

- Historical fixtures and the real tagged registry readers/writers passed:
  1.38.9 rejects the upgraded v3 file; 1.38.10/1.38.11 can read and append to it,
  and the current reader retains that append on reupgrade.
  This is a registry-only boundary: the actual 1.38.10 session reader rejects
  physical revision 3; the 1.38.11 session writer can append and reupgrade
  correctly marks previous input for review. All nine releases have real
  historical session fixtures, source digests and repeated upgrade checks;
  see the [extended compatibility report](testing/manual-session-compatibility.md).
- Root Go packages passed across the full run and the separate full control
  package run. The first full run hit the aggregate ten-minute control-package
  limit; its current test had run for zero seconds. The complete control rerun
  passed in 363 seconds with an explicit 20-minute local ceiling.
- Desktop subpackages passed. All main-package tests were covered by the
  non-overlapping A–D, E–I, J–O, P–R, S–Z/Example/Fuzz groups; each passed.
  Initial larger groups hit the aggregate timeout, not a stuck individual test.
  Creation, restart, tagged upgrades and composer compatibility also passed
  with the race detector; SQLite CAS and draft backup tests passed separately.
  The new database uses the shared SQLite URI builder; drive/UNC encoding tests
  and database reopen with Chinese, space, `#` and `%` path characters passed.
- Frontend production build, bundle budgets, source and test typechecks,
  lifecycle/transcript regressions, input persistence and inbox recovery passed.
  The browser test exercises three actual New clicks and verifies three durable
  identities, formal input persistence and no new legacy draft.
- The ad-hoc signed macOS arm64 `v1.38.12-preview.1` package passed the native
  script with development/service overrides unset: matching shell/host RPC,
  three ready runtimes, duplicate-operation identity, normal exit, SQLite reopen,
  image reference/preview recovery, shell crash with orphan-service lease release,
  archive/restart, and an actionable empty welcome without an editor.
- Generated inventory, repository lint and whitespace checks passed. No lint
  baseline or bundle budget was increased.

Reproducible entry points: `scripts/generate-manual-session-fixtures.py`,
`desktop/frontend/bench/manual-session-creation.mjs`, and
`desktop/electron/scripts/manual-session-smoke.mjs <packaged executable>`.
All fixtures and native runs use isolated synthetic data.

**Remaining release evidence:** packaged Windows and Linux runs are not available
from this macOS validation. The complete old desktop applications were not
installed for downgrade testing: the reverse path uses their actual tagged
storage implementations. No official release, notarization, remote CI run or
historical tag modification was performed.

本地验证已覆盖历史存储实现往返、Go 测试集合及关键 race、前端生命周期和输入恢复、
生产构建与体积检查、macOS arm64 实际打包应用。大测试组曾因累计十分钟上限退出，
已通过不重叠分组或完整包补跑完成覆盖，没有跳过失败用例或提高仓库预算。

Windows、Linux 打包验证仍缺少实际平台证据；降级往返使用历史标签的真实存储实现，
未安装完整旧客户端。测试未读取或改写用户原数据，没有正式发布、修改历史标签或
执行远程 CI。这些边界不能用浏览器通过代替。
