# TASK —— 团队 MCP 移植权威任务文档

> 状态: **实施中(v1.4,2026-08-21)**。本文档固化用户已拍板的两轮需求与全员讨论结论。
> 当前阶段: **P7(验收回归),Gate3 已过;P2-P6 已完成**。P2 门禁已过、实现代码已落地,P1 禁止实现措辞不再适用。**未闭合缺口见 §8.4**(证据文件缺失、九维第 5/7 维未实现、插件零注册)。

---

## 0. 元信息与权威性规则

| 项 | 值 |
|---|---|
| 文档路径 | `docs/team-mcp-port/TASK.md`(仓库内,唯一权威任务文档) |
| 维护人 | leader(codex-zwc);各章节 owner 标注在标题后 |
| 版本 | v1.4(变更须 leader 拍板;变更记录见 §0.1) |
| 关联文档 | 执行上下文 `exec_context.md`(共享上下文区,见附录 A);旧项目证据基座 `cache/mult_agent_mcp/`(**只读移植参考源,非生产 Go 代码**,见 §5.4;禁止拷贝内容进本文档) |
| 权威性层级 | 用户拍板需求 > 本文档 > `exec_context.md` > 成员记忆/讨论结论 |
| 规则 | ① 本文档是**唯一**权威任务文档,禁止平行定义(旧项目 prompt 三份平行定义漂移为前车之鉴);② 实现代码不得把本文档的"计划"当"已实现"引用;③ 本文档变更必须记入 §0.1;④ 阶段切换以 §4 门禁通过为唯一依据 |

### 0.1 变更记录

| 版本 | 日期 | 变更 | 拍板人 |
|---|---|---|---|
| v1.0 | 2026-08-20 | 初始固化:需求 + 概念模型 + 阶段 + 契约 + 门禁 | leader |
| v1.1 | 2026-08-20 | 按用户拍板原文纠正:①拓扑=固定模板+生命周期管理(支持显式增删/停用/归档/临时成员+动态编排,非运行期绝不增删);②默认上下文链扩为 9 环节且可审计来源/版本;③AgentUser 补 stable user_id/人员账号身份/RBAC 绑定,密钥优先 secret store 引用;④P0 只读快照+证据,不擅自动用户脏工作树;⑤cache/ 只读参考源 + repolint 最窄排除(skipDirs 加 cache,不宽基线) | leader |
| v1.1.1 | 2026-08-21 | 文档一致性修订:附录 A 恢复指针示例 TASK.md @v1.0 → @v1.1(无内容变更) | leader |
| v1.1.2 | 2026-08-21 | 文档一致性修订:工具数量 72 → 71(注册源 `@mcp.tool` 实际 71 个,`grep -c` 的 72 含 1 处注释,非装饰器),与 `TOOL_MAPPING.md` 一致;§6 增数量差异说明,附录 C 指向已完成映射表(71 行) | leader |
| v1.2 | 2026-08-21 | 阶段状态修订:①顶部当前阶段改为 P7(Gate3 环境阻塞,P2-P6 已完成),移除 P1 禁止实现措辞;②§4 阶段表增当前状态/证据列,P2-P6 标已过、P7 标进行中(环境阻塞),链接 evidence/P2-review.md、P3-gate2.md、P4-gate1.md、P5-gate2.md、P6-gate1.md、P7-final.md;③P4 完成定义补 `[ TEAM ]` 从 `.reasonix/team/teams.json` 首个 TeamDoc active 槽位加载 + 缺失/损坏/schema/空 roster 安全提示(§3.2 同步);④Gate3 明确 BLOCKED(沙箱 IPv6 listen/既有 answer normalization),禁止写成全量 go test 通过;⑤附录 D 状态矩阵更新 | leader |
| v1.3 | 2026-08-21 | P4 交付/验收按已落地代码修订:①TEAM 主界面支持团队增删(`a` 添加/`d` 删除,Enter 确认/Esc 取消)、`Space`/Enter 进入成员管理视图(↑/↓ 切换成员,Esc 返回);②持久化主文件统一 `.reasonix/team/team.json`(`team.TeamsFile` 常量别名,CLI 引用随迁),变更经存储收口原子写并写后重载;③领域 `team.TeamStore` 提供团队/成员增删与生命周期状态切换的 CAS 原子 API、删除最后团队拒绝(`ErrLastTeam`)、损坏/缺失错误可表达;④旧 `teams.json` 只读回退 + 首次写/显式 `MigrateLegacy` 迁移(create-if-absent,不覆盖主文件);⑤清理 teams.json 当主文件的过时表述(§3.2、§4 P4);Gate3 环境阻塞原样保留 | leader |
| v1.4 | 2026-08-21 | 成员生命周期显示语义按已落地代码修订:①§3.2 与 P4 完成定义"仅 active 槽位进 roster/加载成员"改为"加载**所有成员槽位**并显示持久化生命周期状态"——成员管理页对 active/disabled/archived 槽位全量展示(`s` 需对任意槽位循环状态,故不过滤);②明确新增成员默认为 active,状态可循环 active→disabled→archived→active;v1.2 历史记录原文保留,Gate3 未改 | leader |
| v1.5 | 2026-08-21 | P4 交互按"团队列表优先"修复后修订(用户拍板):①`[ TEAM ]` 入口屏改为**团队列表**(§3.2、§4 P4)——原实现首屏渲染成员却用 a/d 操作团队,主体错位;现为三层导航 团队列表→成员名单→成员详情,a/d 只作用于所在层的主体,`s` 仅成员层;②**空注册表不再是死路**:缺失 `.reasonix/team/` 视为空团队列表,`a` 可直接创建第一个团队并落盘(原实现 errMsg 守卫 + `update()` 先 Load 双重阻断,必须手写 JSON 才能起步);③修复 CAS 字节比较缺陷——`cloneDoc` 把 nil 切片变空切片,marshal 后 `[]`≠`null` 致空注册表任何写入永久 ErrCASConflict;CAS 改为按**规范化文档**比较(`TeamDoc.canonicalize`),手写缩进的 team.json 同样不再误判冲突;④Gate3 状态更正见 §8.1;⑤`make lint` 首次纳入门禁并修复 8 项存量告警 | leader |

### 0.2 术语速查(与旧模型映射)

| 本文档术语 | 旧项目对应(cache/mult_agent_mcp) | 备注 |
|---|---|---|
| AgentUser | 顶层 `agent_users` 注册表(凭证/模型配置) | 凭证载体,不属于任何 team |
| Member | `teams[].members[]` | 成员 = AgentUser 引用 + Role + 状态 |
| Role | 成员 `role` 字段(leader/coder/reviewer/...) | RBAC 判定主体 |
| RBAC | 无显式实现(靠分工惯例) | **新建**,Host 层统一判定 |
| Team | `teams[team_name]` | 固定拓扑容器 |
| Task | `member.last_task` / checkpoint | 派单单元,调度器动态指派 |
| Message | results.jsonl / member_outbox / send-keys 注入 | 群聊消息流(替代终端注入) |
| Blackboard | 共享上下文区散文件(member_contexts/、patch、锁) | 版本化黑板,每次写留版 |
| Memory | checkpoint / session 上下文 / prompt 身份 | 跨会话长期记忆,分层(团队/成员) |

---

## 1. 目标 / 非目标

### 1.1 最终目标(用户已确认)

把 `cache/mult_agent_mcp` 的多 Agent 团队能力移植进当前 Agent(Go 内核):

1. **TUI 交互移植**: 团队管理交互成为当前 CLI 的内部视图(`internal/team/tui`),从 `[ TEAM ]` 回调进入,替代独立 Python Textual TUI 与 tmux 终端矩阵。
2. **MCP 能力插件化**: 旧 71 个 MCP 工具按语义域分组,映射为插件 Capability,经插件 Host 挂载,不再依赖外部 MCP server 进程。
3. **团队数据面落地**: Team/Member/Task/Message/Blackboard/Memory 全量概念模型在 `internal/team` 落地,存储于项目 `.reasonix` 目录,凭证与密钥隔离受红线约束(§3.1)。

### 1.2 非目标(NON-GOALS,防实施滑移)

| # | 非目标 | 理由 | 延后信号 |
|---|---|---|---|
| NG1 | **单进程多 Agent 常驻运行时**(多 Agent 在同一进程内常驻并发执行任务) | 已拍板延期;本轮只预留接口与文档(§3.7) | 调度器接口稳定 + 独立立项 |
| NG2 | 保留 tmux 终端矩阵 / spawn / open terminal | 已拍板删除(§3.3) | 永不再引入 |
| NG3 | 本轮实现并行 Agent 循环或跨进程编排新形态 | 超出本轮边界 | 与 NG1 同信号 |
| NG4 | 迁移 dsh(第三方 TS TUI agent)适配层 | 源项目独立工程,不在移植范围 | 另行评估 |

---

## 2. 术语与概念模型(owner: architecture-analyst)

> 契约: 下文定义是**唯一权威**;实现代码的命名与语义必须与之一致;任何改动须经 §0.1 变更流程。

### 2.1 AgentUser(凭证/配置载体)

- 定义: 一份独立的凭证 + 模型配置注册条目(provider / api key 引用 / model / profile 设置)。
- 身份: 含 **stable user_id**(跨会话/跨团队不变的稳定标识)+ 人员账号身份 + **RBAC 绑定**(该 AgentUser 可被授予的角色能力签名,§7.4 判定)。
- 归属: 顶层注册表,不属于任何 team;`Member` 通过引用(override 或继承)绑定 AgentUser。
- 与旧模型: 对应 `agent_users` 顶层注册表(agent-opus-5 / deepseek-* 等)。
- 密钥承载: **优先存安全 secret store 的引用**(引用 id 进 `.reasonix`);`.reasonix` 普通配置**不得明文承载秘密**;仅在明确安全后端/加密或 0600 fallback 的收口下才可承载(§3.1)。
- 约束: 凭证本身永不出现在 Member/Team/Task/Message/Blackboard 中,只存引用 id(§3.1)。

### 2.2 Member(成员)

- 定义: 团队内一名成员的完整状态单元 = `agent_user_ref` + `role` + 状态机(working/idle/dead/approval/quota/...)+ 任务指针 + 恢复计数 + 上下文视图指针。
- 生命周期: 由 Team 创建/移除;由调度器(§3.5)动态指派 Task,不由终端窗口隐式存在。
- 状态机语义沿用旧项目分类优先级(approval > busy > quota > dead > idle),但**观测源从 tmux capture 改为进程内状态事件**。

### 2.3 Role(角色)

- 定义: 成员能力签名 = 角色标识 + 默认能力集。角色集: leader / coder / reviewer / tester / architecture-analyst / plugin-engineer 等(可扩展)。
- 约束: 角色是 RBAC 判定主体;Skill 按角色授予(§5.3);角色变更须经 RBAC 通道,不直接改能力。

### 2.4 RBAC(角色访问控制)

- 定义: `(role, capability, scope) → allow/deny` 的统一判定,执行点收敛在插件 Host 层(§7.4)。
- scope: team / member / agent-user / storage / plugin 五类。
- 铁律: 判定集中执行,任何插件不得自行绕过 Host 判定直接访问受限资源。

### 2.5 Team(团队容器)

- 定义: 团队 = **固定拓扑模板**(稳定成员身份 + 角色布局,支持**显式增删/停用/归档**与**临时成员**的生命周期管理)+ 群聊消息流 + 版本黑板 + 长期记忆 + 任务队列 + 团队默认 AgentUser(凭证兜底)。
- 编排: 拓扑模板固定但**可经显式管理变更**;`task → fleet` 动态编排为运行期行为(§3.5),与拓扑模板解耦。
- 与旧模型: 对应 `teams[team_name]`;模板 + 生命周期管理取代"动态 spawn 终端"的隐式拓扑。

### 2.6 Task(派单单元)

- 定义: `{id, desc, context_ref, expected, report_ref, checkpoint_ref, status, assigned_member, created_at}`。
- 流转: 创建 → 调度指派(§3.5)→ 执行 → 回报 → 归档;回报先于 compact 的顺序义务保留为硬契约。

### 2.7 Message(消息单元)

- 定义: `{id, kind, from, to, channel, content, ts}`;kind ∈ 群聊 / 派单 / 回报 / 唤醒 / 系统 / 授权。
- channel 取代旧注入通道: 群聊流(替代 send-keys 终端注入);系统通道(替代真实 system 注入的语义位);**禁止内容层伪称 system**(旧铁律延续,§9)。

### 2.8 Blackboard(版本黑板)

- 定义: 团队级共享存储,每次写操作生成新版本 `{rev, kind, content_ref, author, ts}`;替代旧共享上下文区散文件(member_contexts / patch / locks)。
- 契约: 读多写少、写必留版、可回滚;并发写走 CAS(§4 P3 门禁)。

### 2.9 Memory(长期记忆)

- 定义: 跨会话持久记忆,分层 = 团队共享层 + 成员私有层;内容 = 决策 / 教训 / 验收结果 / 成员能力画像。
- 与旧模型: 替代 checkpoint + session 上下文的碎片化方案。
- 契约: 团队层只存事实与决策,不存凭证与明文密钥(§3.1)。

### 2.10 默认上下文链(新任务注入)

- 定义: 每个新 Task 默认注入的上下文链,按序包含 **9 个环节**:
  1. **项目全局指令**(REASONIX.md / AGENTS.md / CLAUDE.md 系);
  2. **团队配置与拓扑**(成员身份 / 角色布局 / 团队默认 AgentUser 引用);
  3. **父会话摘要**(发起任务的会话上下文压缩);
  4. **当前 Goal**(任务所属目标);
  5. **任务与依赖**(TaskSpec + 相关依赖状态);
  6. **相关同级输出**(同目标其他成员的产出摘要);
  7. **共享黑板**(最新版本摘要,§2.8);
  8. **长期记忆检索**(按相关性检索的团队/成员记忆,§2.9);
  9. **成员历史**(该成员过往任务/回报/状态轨迹)。
- 替代旧 `_build_member_initial_context`;链的每个环节可显式裁剪,但顺序不可打乱。
- 可审计性: 链的**装配结果本身可审计**——每个环节记录 `来源路径 + 版本/时间戳`,来源与版本可追溯(§8.2 第 2 维)。

---

## 3. 已拍板架构决策(owner: architecture-analyst)

### 3.1 成员凭证继承与密钥红线

**凭证解析顺序(唯一)**:

```
member.agent_user_ref(成员级 override)
  → team.default_agent_user(团队默认)
  → ❌ 显式报错(绝不回退当前 Reasonix Provider 会话凭证)
```

- 解析链只此一条;**绝不**在链末端静默回退到当前会话的 Provider 凭证(防串号/串凭证/越权)。
- override 与 default 均缺失时: 返回明确错误,任务创建拒绝,不做任何隐式继承。

**密钥红线(不可谈判)**:

| # | 红线 | 后果 |
|---|---|---|
| K1 | 密钥**优先存安全 secret store 的引用**;`.reasonix` 普通配置**不得明文承载秘密**,仅明确安全后端/加密或 0600 fallback 统一写入收口(§3.4)才承载 | 违规即 P0 事故 |
| K2 | 禁止密钥进入 git 跟踪文件、TASK.md、黑板、消息、日志、报告明文 | 同上 |
| K3 | 日志与回报必须脱敏(只留 agent_user 引用 id,不留 key 内容) | 同上 |
| K4 | 凭证作用域显式声明(§7.5);未声明的跨团队/跨成员复用一律拒绝 | 违规即门禁失败 |
| K5 | 旧 `agent_users` 数据迁移时逐条验证 provider 可达性,失败项挂起不静默丢弃 | 迁移门禁(Gate2) |

### 3.2 TUI 位置与入口

- 代码位置: `internal/team/tui`(Go 包)。
- 进入方式: 当前 CLI 内 `[ TEAM ]` 回调 → 团队管理视图(成员切换、上下文视图、黑板、群聊)。
- 形态: **CLI 内部视图模式**,不是独立二进制、不新开终端、不依赖外部进程。
- 契约: `internal/team/tui` 不得反向 import `internal/cli` 之外的前端;视图层只消费 `internal/team` 暴露的领域接口。
- 数据加载(P4 交付说明): `[ TEAM ]` 回调从 `.reasonix/team/team.json`(主文档;`team.TeamsFile` 常量别名即此路径)读取注册表,入口屏为**团队列表**;进入某团队后加载**其全部成员槽位**(active/disabled/archived 全量进成员名单)并显示各槽位的持久化生命周期状态——`s` 需对任意槽位循环状态,故不按状态过滤;新增成员默认 active,状态可循环 active→disabled→archived→active;主文件缺失时领域层只读回退旧 `.reasonix/team/teams.json`。**注册表缺失或为空不是错误态**:渲染空团队列表与 `a add team` 提示,可直接创建第一个团队(存储层 create-if-absent 落盘);仅文档损坏/schema 不符渲染安全提示消息(Esc/q 可关闭)并**阻断一切写入**,**绝不渲染占位或伪造成员数据**。
- 三层导航与生命周期操作(P4 交付说明): 层级为 **团队列表 → 成员名单 → 成员详情**,`Esc` 逐层回退、在团队列表关闭覆盖层,`Enter`/`Space` 逐层下钻,`↑/↓` 在当前层移动焦点。生命周期按键**只作用于所在层的主体**:团队列表 `a` 添加团队 / `d` 删除聚焦团队;成员名单与成员详情 `a` 添加成员 / `d` 删除聚焦成员 / `s` 循环其生命周期状态(`s` 在团队列表无操作)。写入均先经确认态(输入 Enter 确认、Esc 取消;删除 Enter 确认、Esc/q 取消),经存储收口原子写落盘后重载,界面只展示持久化状态且**保持写入前所在层与焦点**;领域 `team.TeamStore` 提供成员增删/生命周期状态切换的 CAS 原子 API、删除最后团队拒绝(`ErrLastTeam`)、重名拒绝(`ErrTeamExists`/`ErrMemberExists`)、损坏/缺失错误可表达。

### 3.3 删除 tmux / spawn / open terminal

- 删除对象: 旧 `tmux_spawn_member`、`launch_terminals`、`open_leader_terminal`、send-keys 注入、tmux capture 分类器(仅在新观测源落地后删除,见 §4 P4 顺序)。
- 替代: 进程内成员状态事件 + 群聊消息流(§2.7)+ 调度器指派(§3.5)。
- 当前 CLI 内提供**成员切换与上下文视图**: 切换当前视角到某成员、查看其状态/任务/上下文/回报,不通过终端窗口。
- 验收: 九维矩阵"无 tmux 副作用"维(§8.2);仓库内 `tmux` 相关符号回归扫描为零。

### 3.4 .reasonix 项目存储

- 存储根: 项目根 `.reasonix/`(已存在);团队数据落 `.reasonix/team/`(schema 待插件角色,见待决 O2)。
- 承载: teams(拓扑)、agent_users 注册表、blackboard 版本、messages、memory、locks、acceptance ledger。
- 写入收口: 全仓 JSON 写入唯一收口 = `internal/team` 的原子写(临时文件 + fsync + rename + 0600),复制旧 `atomic_write.py` 语义;**禁止绕过收口直写**。
- 旧数据迁移: 不自动读取旧 `teams_data.json`;迁移为显式命令、逐项校验(§3.1 K5)。

### 3.5 固定拓扑模板 + task/fleet 动态调度

- **拓扑模板固定**: 团队创建时确定成员身份与角色布局**模板**,模板即"稳定成员身份 + 生命周期管理"的基准;支持**显式增删/停用/归档**成员与**临时成员**,均为经生命周期管理的合法变更,不改变模板语义(与旧"运行期绝不增删"划清界限,也不同于旧"动态 spawn 终端"的隐式拓扑)。
- **调度动态**: Task → fleet 的指派在运行期动态决定:
  - `fleet` = 可用执行 Agent 池(本轮为占位池,单进程多 Agent 延后期间由占位调度器记账;见 §3.7);
  - 调度策略: 角色匹配 → 负载均衡 → 上一任务亲和;
  - 调度器接口在 `internal/team/scheduler` 定义为接口 + 文档,实现随运行时(§3.7)。
- 成员状态机由调度器驱动,不再由终端观测驱动。

### 3.6 群聊 / 版本黑板 / 长期记忆 / 默认上下文链

- **群聊**: 团队消息流为成员交互唯一通道(替代 send-keys);leader 派单、成员回报、唤醒、授权全部走 Message。
- **版本黑板**: §2.8;旧共享上下文区(share_context_space)不迁移,由黑板版本流替代。
- **长期记忆**: §2.9;写入必须可追溯(谁、何时、基于哪个黑板版本)。
- **默认上下文链**: §2.10;每个 Task 创建时按链组装,链装配结果本身可审计。

### 3.7 单进程多 Agent 常驻运行时(延期,预留接口)

- **延期**: 本轮**不实现**常驻并发 Agent 运行时(见 NG1)。
- **本轮预留**(接口 + 文档,禁实现体):
  1. `internal/team/agentruntime` 包: 接口定义(生命周期 / 启停 / 状态事件源),doc.go 说明设计意图与延后理由;
  2. `internal/team/scheduler`: 调度接口 + 占位实现(仅记账,不真实并发执行);
  3. 事件总线接口: 成员状态/回报/唤醒事件的类型定义与订阅接口。
- 占位行为的诚实语义: 调度指派在日志与黑板上可见,但执行标注 `[runtime-pending]`,不得伪称已执行。

---

## 4. 严格阶段顺序与完成定义(owner: architecture-analyst)

> 规则: 阶段严格串行;**前序门禁未过不得进入后序**(§9 禁止事项 10)。
> 每阶段完成定义 = 可验证条件;验证结果按 §8.3 证据格式记录。

| 阶段 | 目标 | 完成定义(全部满足) | 当前状态 | 证据 |
|---|---|---|---|---|
| **P0 快照基线** | 保护既有成果 | ① 未提交工作树**只读快照 + 证据登记**(快照清单/哈希/时间戳写入证据基座);**不擅自 commit、不清空用户脏工作树,保留用户改动**;② TASK.md 路径就位;③ 旧项目测试基线(2546 passed)记录在案 | 已过 | |
| **P1 文档固化** | 权威文档与恢复上下文落地 | ① 本文档全员签收(§4 矩阵、§8 门禁、§9 禁止事项逐条确认);② 附录 A 的 `exec_context.md` 在共享上下文区落地并填充;③ 各章 owner 确认无代写缺漏 | 已过 | 本文档 |
| **P2 概念模型落地** | `internal/team` 领域类型与存储 | ① §2 全部概念在 `internal/team` 有类型定义(含 doc.go 说明,不实现业务);② 原子写收口(§3.4)实现 + 单测;③ 门禁:`go build ./... && go test ./internal/team/ && go run ./tools/repolint` 全绿 | 已过 | `evidence/P2-review.md` |
| **P3 数据与安全层** | 存储、凭证、RBAC 判定 | ① `.reasonix/team` schema 落地(待决 O2 拍板后);② 凭证解析链(§3.1)+ 密钥红线测试;③ RBAC 判定(§7.4)实现 + 单测;④ CAS 并发写测试;⑤ 门禁: 对应测试全绿 + Gate2 第 3/4 维 | 已过 | `evidence/P3-gate2.md` |
| **P4 TUI 移植** | CLI 内团队视图 | ① `[ TEAM ]` 回调从 `.reasonix/team/team.json`(主文档,`TeamsFile` 别名)加载注册表,入口屏为**团队列表**;进入团队后展示其**全部成员槽位**(active/disabled/archived)并显示持久化生命周期状态,新增成员默认 active,状态可循环 active→disabled→archived→active;注册表缺失/为空渲染空列表与创建提示且 `a` 可落盘建首个团队,文档损坏/schema 不符渲染安全提示并阻断写入,均不伪造数据;② `internal/team/tui` 视图 + 回调接线,三层导航(团队列表→成员名单→成员详情)可用,a/d 只作用于所在层主体、`s` 仅成员层(§3.3);③ 团队与成员增删经存储收口原子写落盘 team.json,写后重载展示持久化状态并保持所在层与焦点(写后读回,§8.3);领域 `TeamStore` 提供团队/成员增删与生命周期状态切换的 CAS 循环 API、`ErrLastTeam`、重名拒绝、损坏/缺失错误可表达;④ 旧 teams.json 只读回退,首次写/显式 `MigrateLegacy` 一次性迁移,create-if-absent 不覆盖主文件;⑤ 删除 tmux/spawn/open terminal 依赖(新观测源已落地,删除才生效);⑥ 门禁: 九维矩阵第 1/8 维 + Gate1 | 已过(v1.5 重修) | `evidence/P4-gate1.md`、`evidence/P4-teamlist.md` |
| **P5 插件化** | 71 工具映射落地 | ① 插件 Host/Capability/UIHub(§7)实现;② 71 工具三层映射表(§6)逐行落地,零孤儿通过;③ 门禁: 九维矩阵第 6/9 维 | 已过 | `evidence/P5-gate2.md` |
| **P6 调度与预留** | 动态调度 + 运行时接口 | ① scheduler 接口 + 占位实现(§3.7);② agentruntime/事件总线接口与文档;③ 门禁: 接口编译通过 + 占位语义测试(不伪称执行) | 已过 | `evidence/P6-gate1.md` |
| **P7 验收回归** | 全量验收与交接 | ① Gate0-Gate4 全量走完(§8.1);② 九维矩阵逐维证据归档;③ 漂移复查(TASK.md 与实现逐条比对);④ 回滚预案成文 | 进行中(Gate3 已过;②仍有缺口,见 §8.4) | `evidence/P7-final.md` |

---

## 5. 领域边界(owner: architecture-analyst)

### 5.1 团队域 `internal/team`

- 承载: §2 全部概念模型 + 存储(§3.4)+ 调度(§3.5)+ 运行时接口(§3.7)。
- 分层约束(沿用项目 layering,repolint 强制): `internal/team` 不得 import `internal/cli`、`internal/control` 等前端;前端(含 `internal/team/tui`)可 import 团队域。
- 公共面: 只暴露领域接口;内部状态不泄漏到包外。

### 5.2 工具插件边界

- 定义: 插件 = 实现插件 Host 契约(§7)的独立 Go 包,声明 Capability 后被按需加载。
- 边界判据(可操作): 一个能力如果**必须触碰团队域内部状态**,则必须是核心(`internal/team`)而非插件;若只通过公开接口消费能力,则归插件。
- 禁止: 插件绕过 Host 直接写 `.reasonix/` 存储或读凭证(§7.4/§7.5)。

### 5.3 leader / member Skill 边界

- Skill = 授予角色的行为指导包(声明式,不进代码逻辑)。
- leader Skill: 任务拆解对齐 / 分配后调度等待 / 回报评估 / 收尾闭环(对应旧 leader_duty_prompt 语义)。
- member Skill: 交付格式(回报四段式)/ 顺序义务(先回报后 compact)/ 只读调研纪律。
- 契约: ① Skill 按 Role 授予,不进代码(可读可审计);② **角色中立**要求延续旧 Codex AGENTS.md 教训——共享文件不得写死具体成员名;③ Skill 内容与实现代码互不引用(防漂移)。

### 5.4 cache/ 只读移植参考源(非生产 Go 代码)

- 定位: `cache/`(含 `cache/mult_agent_mcp/`)**只读移植参考源**——旧项目证据基座,不是生产 Go 代码;禁止被 `go build`/导入/拷贝进生产包;禁止向其写入生产代码。
- repolint 处理(已落地): 既有 `skipDirs` 排除机制(tools/repolint/source.go)新增 `"cache": true`——与 `wailsjs` 同款的**显式目录排除**,非宽基线;`baseline.json` 债务基线**未改**。此后 cache/ 下任何 .go 文件(含快照)不产生 lint 债。
- 变更纪律: 若未来需要扫描 cache/ 内文件,须先移除排除并走正常基线流程,不得静默改回。

---

## 6. 旧 MCP 71 工具三层可追溯映射(owner: plugin-engineer)

> 数量修正(v1.1.2): 注册源 `cache/mult_agent_mcp/mult_agent_mcp.py` 实测 **71** 个 `@mcp.tool` 工具——`grep -c` 的 72 含 5204 行 1 处注释,非装饰器;71 装饰器 = 71 工具,一一对应。全量映射见 `TOOL_MAPPING.md`(恰好 71 行,`internal/team/toolmapping_test.go` 零孤儿测试钉住)。

### 6.1 映射表模板(三层)

- 第一层: 旧 `@mcp.tool` 名(证据: 旧符号/行号,可追溯)。
- 第二层: 语义域分组。
- 第三层: 新宿主落点 = 核心(internal/team)/ 插件 Capability / 显式废弃(dropped)。

| 旧工具名 | 旧证据(符号:行号) | 语义域 | 新落点 | 状态 | 验收证据 |
|---|---|---|---|---|---|
| (示例)`leader_assign_subtask` | mult_agent_mcp.py:xxxx | 派单 | 核心: Task 服务 | planned | P4/P6 门禁测试 |
| (示例)`member_report_result` | mult_agent_mcp.py:10761 | 回报 | 核心: Message + 回报必达 | planned | 回报顺序测试 |
| ...(71 行,由插件角色按此模板全量填写,零孤儿原则下不许留空) | | | | | |

- 语义域分组(依据架构调研的 71 工具族划分): 团队管理 / 成员管理 / leader 工具 / member 工具 / 终端与监控(→删除或改事件源)/ 代理池(→AgentUser)/ 讨论 / 恢复与 checkpoint / 消息队列 / 授权。

### 6.2 零孤儿原则

- 规则: 71 个工具**每一个**必须在映射表中有且仅有一行,且 `新落点 ∈ {核心, 插件, 废弃}`;**不允许"未映射"或"待定"孤儿行**;状态 `planned/merged/dropped` 三态之一,不允许空。
- 验收: Gate2 第 6 维——脚本扫描映射表与旧工具注册清单差集为零。
- 漂移护栏: 映射表是唯一权威;实现后由 repolint 类检查或测试断言保证"表中 merged/dropped 与代码一致"。

---

## 7. 插件契约(owner: plugin-engineer,待补输入)

### 7.1 Host(宿主)

- 职责: 插件注册 / 生命周期(加载、校验、卸载)/ 能力路由 / 统一错误。
- 契约: Host 是插件唯一入口与唯一出口;插件不得自建服务端点;Host 校验插件声明与实现的 Capability 一致性。

### 7.2 Capability(能力声明)

- 定义: 插件声明的能力集合 `{id, kind(工具/事件/视图/存储访问), scope, version}`。
- 契约: 能力 ID 全局唯一;同一语义域能力互斥注册(冲突即加载失败);能力与 RBAC(§7.4)绑定声明。

### 7.3 UIHub(UI 注册中心)

- 定义: 插件 UI 的注册/渲染中心;插件注册视图与面板,不直接画屏。
- 契约: 视图注册经 UIHub 校验(权限 scope);渲染由 Host 统一驱动;插件不得绕过 UIHub 直接挂载终端/文件。

### 7.4 RBAC(集中判定)

- 判定点: Host 层唯一执行 `(role, capability, scope) → allow/deny`。
- 契约: ① 所有 Capability 调用入口必经判定;② 判定结果可审计(留痕);③ 插件内部二次访问受限资源同样受判(不因已进入插件而豁免)。

### 7.5 凭证作用域(credential scope)

- 定义: 插件/能力对凭证的访问范围声明 `none | agent-user | team`。
- 契约: ① 未声明即 none,拒绝一切凭证访问;② 声明 team 只可读 team.default_agent_user 的引用(非内容);③ 声明 agent-user 只可读被显式授予的引用;④ 凭证内容读取唯一入口在核心存储层(§3.1 K1-K4 全适用)。

---

## 8. 验收门禁(owner: test-engineer,待补输入细化)

> 测试角色的完整测试矩阵与命令占位在此章固化;`<...>` 为命令占位,细化时直接替换。

### 8.1 Gate0 - Gate4

| 门禁 | 内容 | 通过条件 |
|---|---|---|
| **Gate0 基线门禁** | 前置基线 | 工作树只读快照 + 证据登记完成(P0,未擅自动用户改动);`go build ./...`、`go vet ./...`、基线测试全绿;TASK.md 全员签收 |
| **Gate1 阶段门禁** | 每阶段完成定义 | §4 对应阶段完成定义逐条验证通过,证据按 §8.3 记录 |
| **Gate2 功能门禁** | 九维矩阵 | §8.2 九维逐维验收,证据归档;零孤儿扫描(§6.2)通过 |
| **Gate3 回归门禁** | 全量回归 | **已过(v1.5 复核)**: 本机(Linux 6.8, go test ./...)实测 `go build ./...`、`go vet ./...`、`go test ./...` **130 包全绿 exit 0,零 FAIL**;`-race` 复跑 `./internal/team/... ./internal/cli/` 亦全绿。v1.2 记录的"沙箱不支持 IPv6 listen + answer normalization 失败"**在本环境不复现**,属当时沙箱限制而非代码回归,阻塞解除。`make lint`(golangci-lint **0 issues** + repolint clean,基线 1275 **未加宽**)与无 tmux 副作用扫描**已过**。铁律: 证据须记录执行环境与命令(见 `evidence/P4-teamlist.md`);换环境须重跑,不得沿用本结论 |
| **Gate4 发布门禁** | 交付 | 验收汇总成文;TASK.md 与实现漂移复查为零差异;回滚预案成文;leader 拍板 |

### 8.2 九维矩阵(逐维: 验收项 / 方法 / 命令占位 / 证据)

| # | 维度 | 验收要点 | 命令/方法占位 |
|---|---|---|---|
| 1 | TUI 交互语义 | `[TEAM]` 回调进入、团队增删(a/d + 确认态)、Space 进入成员管理、成员切换/上下文视图按键/焦点/退出、无数据/损坏安全提示 | `<go test ./internal/team/tui/...>` + 交互验收清单 |
| 2 | .reasonix 持久化 | schema 落盘、原子写、0600、迁移命令 | `<go test ./internal/team/... -run Persist>` |
| 3 | AgentUser 凭证隔离 | 解析链(override→team default)、绝不回退、密钥不进 git/日志 | `<go test ./internal/team/... -run Credential>` + 红线扫描脚本 |
| 4 | RBAC | 判定矩阵、Host 集中执行、留痕 | `<go test ./internal/team/... -run RBAC>` |
| 5 | 消息/黑板/记忆 | 群聊流转、黑板版本与回滚、记忆分层可追溯 | `<go test ./internal/team/... -run Message|Blackboard|Memory>` |
| 6 | 插件 71 工具完整性 | 映射表差集为零、能力互斥、Host 校验 | `<go test ./internal/team/plugin/...>` + 零孤儿脚本 |
| 7 | Skill 指导边界 | 角色授予、角色中立、不写死成员名 | `<go test ./internal/team/... -run Skill>` + 内容扫描 |
| 8 | 无 tmux 副作用 | tmux/spawn/send-keys 符号回归为零;旧观测源删除 | `grep -rn "tmux\|spawn\|send-keys" internal/` 断言为空 |
| 9 | repolint / go test | 全量编译、测试、lint 门禁 | `go build ./... && go vet ./... && go test ./... && go run ./tools/repolint` |

### 8.3 证据格式(统一)

每条验收记录(追加到共享上下文 `acceptance-ledger.md` 或 TASK.md 附录 E 勾选表):

```
[维度 N | 阶段 Pk | 验收项] 命令: <...> 结果: PASS/FAIL
证据: <证据路径或输出文件> 执行人: <role> 时间: <ts>
```

- 铁律: 证据必须可重跑(命令可复制);不许凭内存断言(旧项目教训: 写后读回逐字段复核)。

### 8.4 未闭合缺口登记(v1.5 复查,如实记录不粉饰)

> 规则: 本节是 Gate4"漂移复查零差异"的前置清单;每项闭合后连同证据路径一并删除,**不得**在缺口仍在时把 P7 标为已过。

| # | 缺口 | 实测证据 | 影响的门禁 | 闭合动作 |
|---|---|---|---|---|
| G1 | **验收证据文件缺失 5/6**: §4 与附录 D 链接的 `P2-review.md`、`P3-gate2.md`、`P5-gate2.md`、`P6-gate1.md`、`P7-final.md` 在 `evidence/` 下均不存在,仅 `P4-gate1.md`、`P4-teamlist.md` 落盘 | `ls evidence/` | Gate1(逐阶段)、Gate4 | 按 §8.3 格式补跑并落盘,或把对应阶段状态从"已过"降级 |
| G2 | **九维第 5 维(消息/黑板/记忆)无实现**: `Message`/`BlackboardEntry`/`MemoryEntry` 仅有类型定义与 `BlackboardRevFile()` 路径助手,无群聊流转、无黑板版本/回滚、无记忆分层检索;§2.10 九环节默认上下文链亦仅文档 | `grep -rE 'func .*(Message\|Blackboard\|Memory)' internal/team/ --include='*.go' \| grep -v _test` 仅 1 命中 | Gate2 第 5 维 | 该维不在 P2-P6 任一阶段完成定义内,须新增阶段或显式降级为非目标 |
| G3 | **九维第 7 维(Skill 边界)无实现**: `internal/team` 内零 skill 命中 | `grep -ri skill internal/team/ --include='*.go'` 为空 | Gate2 第 7 维 | 同 G2 |
| G4 | **插件零注册**: Host/UIHub/RBAC/审计机制齐备且有测试,但生产代码从未调用 `Host.Register`;`TOOL_MAPPING.md` 标"插件"的 21 个工具无实现体。P5"71 工具映射落地"实为**映射表落地**,非能力移植 | `grep -rn 'Register(' internal/team/plugin/*.go internal/cli/chat_tui_team*.go \| grep -v _test` 仅定义处 | Gate2 第 6 维 | 明确 P5 语义为"表 + 宿主",能力移植另立阶段 |
| G5 | **团队域唯一消费者是 TUI 覆盖层**: 除 `internal/cli/chat_tui_team*.go` 外无前端/控制器接线,团队能力未对 Agent 暴露 | `grep -rln 'reasonix/internal/team' --include='*.go' .` | Gate4 | 随 G4 一并立项 |


---

## 9. 禁止事项(全阶段生效,违反即 P0 事故)

1. 禁止在 P2 门禁通过前编写实现代码(P2 已过,此禁止解除)。
2. 禁止引入/保留 tmux、spawn 终端、open terminal、send-keys 注入语义(§3.3)。
3. 禁止凭证回退当前 Reasonix Provider(§3.1 解析链末端)。
4. 禁止密钥进入 git / 黑板 / 消息 / 日志 / 报告明文(§3.1 K2)。
5. 禁止本轮实现单进程多 Agent 常驻运行时(§3.7;只留接口与文档)。
6. 禁止平行权威文档;TASK.md 是唯一权威,`exec_context.md` 只存指针与状态(附录 A)。
7. 禁止回报先于 compact 的顺序义务颠倒;任务完成第一个动作 = `member_report_result`。
8. 禁止内容层 `[system]` 伪称真实 system 注入(旧铁律延续)。
9. 禁止扩大 repolint 基线来落地改动(必须修代码,不宽基线)。
10. 禁止未过前序门禁进入下一阶段(§4 串行规则)。
11. 禁止插件绕过 Host 直接写存储或读凭证(§7.5)。
12. 禁止覆盖 `.reasonix/commands/` 既有内容(仓库已有 review.md)。
13. 禁止把 `cache/` 当生产 Go 代码编译/导入/引用,禁止向其写入生产代码;cache/ 只读参考源语义由 repolint skipDirs 排除保障(§5.4)。

---

## 10. 风险(owner: architecture-analyst)

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| R1 | 旧项目巨型单文件(约 1.1 万行 / 301 符号 / 71 工具)语义拆解面大 | 插件化回归 | 按语义域分组切片;Gate2 逐维;映射表零孤儿 |
| R2 | 旧收敛成果(TUI resume 接线)在未提交工作树 | 换机/回滚即丢 | **P0 只读快照 + 证据登记**(清单/哈希/时间戳),不擅自 commit、保留用户改动 |
| R3 | 旧 docs 与代码漂移无护栏(docs 不在 git) | 决策依据失真 | 以代码为准、docs 为线索;TASK.md 记证据行号 |
| R4 | Codex 平台限制(无真 system 通道) | 插件对 codex 成员能力降级 | 诚实降级文档化,不伪称(§9 第 8 条) |
| R5 | 单进程多 Agent 延期导致 TUI 移植期"成员执行体"形态未定 | P4 验收悬空 | 占位调度器记账语义 + `[runtime-pending]` 标注,不伪称执行 |
| R6 | 观测源从 tmux capture 改为事件源,分类语义需重建 | 状态机回归 | 状态机优先级契约(§2.2)先行固化,事件源逐态对照 |
| R7 | 旧数据迁移(agent_users)provider 不可达 | 凭证丢失 | K5 挂起不丢弃;迁移命令显式、逐条校验 |
| R8 | 71 工具映射遗漏(孤儿) | 能力缺口 | 零孤儿脚本差集扫描(Gate2 第 6 维) |
| R9 | 既有 flaky(旧项目 2 条已知)参考基线抖动 | 门禁误判 | 单跑复核;Go 基线独立重建,不依赖旧 flaky |

---

## 11. 待决清单(未决但不阻塞,拍板后回写 §0.1)

| # | 待决项 | 暂定方向 | 不阻塞理由 | 拍板触发点 |
|---|---|---|---|---|
| O1 | TUI 视图机制(当前 CLI 内嵌团队视图的实现方式) | 复用当前 CLI 视图/回调体系 | 属 P4 实现细节,接口先行 | P3 收尾时 |
| O2 | `.reasonix/team` schema 细节 | 待插件角色出 schema 提案 | P2 只需类型与原子写,不依赖 schema 全量 | P2 收尾时 |
| O3 | 插件 ABI 具体定义(Host/Capability 接口签名) | 待插件角色出 ABI 提案 | P2-P4 不依赖;P5 前必决 | P4 收尾时 |
| O4 | 旧数据格式兼容(是否提供旧 teams_data.json 导入) | 默认不自动导入,显式迁移命令 | 本轮不读旧数据 | P3 收尾时 |
| O5 | 验收基线 Go 等价物 | 待测试角色定基线 | Gate0 用现有 go test;总基线 P7 前必决 | P4 收尾时 |
| O6 | 群聊/黑板持久化文件格式 | JSON 多文件 vs 单文件,待定 | 存储层接口先行,格式可换 | P3 收尾时 |
| O7 | prompts/@channel 模板源是否移植 | 默认不移植(内容进 Skill 层) | Skill 层(§5.3)已承接内容语义 | P5 收尾时 |

---

## 附录 A: 执行上下文 B 模板(`exec_context.md`,共享上下文区,不入 git)

> 目的: compact 后恢复的唯一入口;只存指针与状态,**禁止复制 TASK.md 内容**;每完成一项立即更新(非 compact 前才写);与 TASK.md 冲突以 TASK.md 为准并回报差异。

```
A 路径: docs/team-mcp-port/TASK.md @v1.4
B 版本: vN(单调递增)
当前阶段: P_k/N + 门禁状态(未开|进行中|待验收|已过)
已完成项: [时间] 项 — 证据路径
下一动作: 单一(谁 + 做什么 + 交付什么)
禁止事项: 当前阶段生效项(随阶段切换同步更新)
成员分工: 角色 × 当前阶段任务 × 状态
验收门禁: 当前待过 Gate/维度 + 下一道
待补输入: 插件/测试角色挂账项(补齐才开相应门禁)
```

## 附录 B: 恢复流程

1. compact 后: 先读 `exec_context.md` → 缺失则回退 TASK.md §4(阶段+门禁)→ 按"下一动作"继续。
2. 与 TASK.md 冲突: 以 TASK.md 为准,回报差异。
3. 每完成一项: 更新 B(完成项+证据+下一动作);阶段切换时同步"禁止事项"与"验收门禁"。
4. 成员侧: `member_get_my_task` 与 B 双源对齐,差异回报;回报必达守 §9 第 7 条。

## 附录 C: 71 工具映射表(全量表见 TOOL_MAPPING.md,插件角色按 §6.1 模板填写,零孤儿)

> 全量表已由 plugin-engineer 在 `TOOL_MAPPING.md` 填满 71 行(核心 30 / 插件 21 / 废弃 20);每行必填 §6.1 六列,禁止空状态。

## 附录 D: 阶段勾选表(逐阶段勾选,证据链接)

| 阶段 | 门禁状态 | 完成定义逐条确认 | 证据链接 | 签收人 |
|---|---|---|---|---|
| P0 | 已过 | ①②③ | | |
| P1 | 已过 | ①②③ | 本文档 | architecture-analyst |
| P2 | 已过 | ①②③ | evidence/P2-review.md | |
| P3 | 已过 | ①②③④⑤ | evidence/P3-gate2.md | |
| P4 | 已过 | ①②③④⑤⑥ | evidence/P4-gate1.md | |
| P5 | 已过 | ①②③ | evidence/P5-gate2.md | |
| P6 | 已过 | ①②③ | evidence/P6-gate1.md | |
| P7 | 进行中(Gate3 已过;证据缺口见 §8.4) | ①②③④ | evidence/P7-final.md(未落盘) | |

## 附录 E: 验收证据勾选表(九维 × Gate)

> 按 §8.3 证据格式逐条追加;Gate0-Gate4 每过一道在对应维度行记录。
