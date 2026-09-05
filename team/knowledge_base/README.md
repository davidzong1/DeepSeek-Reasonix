# team knowledge_base（仓库文档 + 运行数据）

本目录承载多代理团队知识库的**仓库文档**与**运行期数据**；实现代码见 `internal/knowledge_base/`（含自身包级 README）。

## 文档（仓库内唯二，单一真源）

- `KNOWLEDGE_BASE_DESIGN.md` —— **唯一合并设计真源**：目标与边界、架构/接口/数据布局、数据模型与提取/质量/冲突、存储与团队隔离、检索（含失败降级 `queryByUpdated`）、成员生命周期、增量队列与维护、集成 API、可观测性、P0–P3 分阶段实施（P3 SQLite/FTS 契约冻结 deferred）、交付检查清单（§13）、已接受残差与归档说明（§14）。一切设计/契约/残差以本文件为准，不再向分片回写。
- 本文件 —— 导航与运行数据说明。
- 分析分片（`sections/*.md`）、`IMPLEMENTATION_CONTRACT.md`、`P3_SQLITE_UPGRADE_SCOPE.md` 为过程文档，内容已并入 DESIGN，归档于仓库外（gitignore 排除）。

## 运行期数据

- 默认 `DataRoot = team/knowledge_base`，即本目录下 `<team-id>/` 子目录（每团队独立，可经 `Options.DataRoot` 覆盖）。
- **真源**：`<DataRoot>/<team-id>/items/<item-id>.md`（frontmatter + Markdown 正文），只增不改，变更走版本链。
- 队列：`<DataRoot>/<team-id>/queue/events.log`（append-only）+ `queue/cursor`（已确认位点），at-least-once、崩溃游标续跑。
- 读模型：内存词法索引（复用 `internal/retrieval` BM25），启动时由 items 重建、可整体丢弃重建；读模型故障时 Query 降级为 store 全量 live 按更新时间倒序。
- `ClearTeam` 将整 team 目录 rename 至 `<DataRoot>/.trash/<ts>-<team-id>`。
- 运行数据全部由 `.gitignore`（`team/knowledge_base/**` 白名单仅放行两个文档）排除，不入库。

## 宿主接线（cli team runtime）

- 每团队一个 `Manager`，由 cli 侧 `teamTaskService`（`internal/cli/team_task_service.go`）在**成员首个完成回合**时经 `boot.OpenTeamKnowledge` 惰性打开，`DataRoot` 默认解析为团队数据目录下的 `knowledge_base`：用户全局默认 = `<user state root>/team/knowledge_base`（任意 cwd 一致，绝不落在启动目录），仅无用户状态根时项目回退 `<root>/.reasonix/team/knowledge_base`；随 registry 板关闭（`teamBackends.closeAll`）统一排空关闭。
- **采集（turn tail→Ingest）**：成员 `member_report_result` 完成的回合（报告结果即该任务回合的 turn tail）在 `report()` 成功后入队 `Ingest`；入队即返，不阻塞工具。仅规则/质量门通过的决策/约定/经验等落为 live 知识（无 LLM 时 `needs_llm` 文本不入库）。
- **读取（Query→tail）**：leader 与成员均可调用只读工具 `team_knowledge_recall`（经 `recallKnowledge` 查询本队 live 项并格式化为紧凑结果）。
- 未配置 `kbDataRoot` 的主机/测试保持 KB 关闭（零副作用）；`closeKnowledge` 幂等。
