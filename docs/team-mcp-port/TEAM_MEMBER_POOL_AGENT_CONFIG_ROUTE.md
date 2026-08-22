# 技术路线 —— 成员池与 Agent 代理配置移植(mult_agent_mcp → Reasonix)

> 状态: **已拍板(2026-08-21),领域层已实施**。拍板项见 §11(A/A/A/A + 凭据 0600 决策);TUI 阶段(P2/P3)待后续。
> 范围: 仅 [TEAM] 团队列表的 **成员池管理** 与 **成员 Agent 代理配置** 两项能力移植;不动既有团队/成员/生命周期管理。
> 关联: 权威任务文档 `TASK.md`(本路线不取代它;拍板后以增量条目并入 TASK.md §4/§8)。领域层落点 `internal/team`: `agentusers.go`/`binding.go`/`proxy.go`/`import.go`。

---

## 1. 现状与目标

### 1.1 现状

- **Reasonix 侧**: `[ TEAM ]` 已提供团队列表 → 成员名单 → 成员详情三层导航(`internal/cli/chat_tui_team.go` + `internal/team/tui/`),持久化于 `.reasonix/team/team.json`(schema v1,CAS 原子写)。**缺口**: 无 AgentUser(成员池)管理入口,无成员 Agent 代理配置(启动类型/模型/代理)编辑;`AgentUser`/`SecretRef`/RBAC 概念在 `types.go` 已定义但无 store、无 TUI 界面。
- **mult_agent_mcp 侧**: `~/.mult_agent_mcp/teams_data.json` 持有团队配置 + 顶层 `agent_users` 凭据池(多 provider 并存,含**明文 API key**),代理配置语义为"成员覆盖 > 团队默认 > 关闭"。

### 1.2 目标

1. 把 mult_agent_mcp 的**配置语义**(成员池、启动类型、代理优先级)迁移进 Reasonix TeamStore/TUI,入口仍在 `[ TEAM ]`。
2. 数据落在 Reasonix 自有文件(`team.json` + 新 `agent_users.json`),不再只依赖外部 `teams_data.json`。
3. 密钥零迁移: Reasonix 侧永不落明文 key(K1 红线延续)。

### 1.3 非目标

- 不做双向同步/写回 mult_agent_mcp(默认;见 §11 拍板项 C)。
- 不迁移任何运行态字段(tmux 窗口、任务/上下文指针、leader/成员状态机)。
- 不通过 Reasonix 启动/管理终端(§9 禁止 tmux/spawn 语义延续)。
- 不做密钥注入/凭据执行(仅引用占位)。

---

## 2. mult_agent_mcp 语义盘点

### 2.1 必须等价移植(配置语义,决定启动行为)

| mult_agent_mcp 字段 | 语义 | Reasonix 落点 |
|---|---|---|
| `teams[].default_agent` | 团队默认启动类型(claude/codex/自定义) | `Team.AgentType`(新增,可选) |
| `teams[].default_agent_user` | 团队默认凭据身份 | `Team.DefaultAgentUserRef`(已有) |
| `teams[].proxy{enabled,host,port}` | 团队级代理 | `Team.Proxy{Enabled,Host,Port}`(新增,可选) |
| `teams[].members[].role` | 成员角色 | `MemberSlot.Role`(已有) |
| `teams[].members[].agent` | 成员启动类型覆盖 | `MemberSlot.AgentType`(新增,可选) |
| `teams[].members[].model` | 成员模型配置(随 agent_user) | `MemberSlot.AgentUserRef` 绑定(已有字段,补 UI) |
| 代理优先级(成员覆盖 > 团队默认 > 关闭) | 行为契约 | 解析函数统一保证(§7) |
| 顶层 `agent_users[]` 非敏感字段 | 凭据池 | 新 `agent_users.json`(§4) |

### 2.2 不迁移的运行态字段(20+ 项,只取配置不取状态)

`tmux_window_*`、`last_task`、`last_context`、`last_report_*`、`last_observed_state`、`quota_hits`、`last_blocked_ts`、`leader_state`、`leader_work_state`、`leader_checkpoint*`、`leader_wakeup*`、`leader_sleep*`、`monitor_*`、`discussion`、`member_outbox`、`leader_pending_reports`、`leader_last_ack` 等全部运行态字段**绝不移植**——它们是进程生命周期观测,不是模板配置。

`workspace_dir`/`context_dir` 环境绑定**不进核心 schema**(Reasonix 不启终端);可在导入报告中作参考信息输出。

`work_mode`/`auto_authorize` 为审批运行策略,本轮不做(降级后续)。`takeover_enabled` 为 mult_agent_mcp 特有,不迁移。`agent_user_pool`+cursor 为已弃用调度字段(Agent设计 团队为 None),不做池调度绑定。

### 2.3 agent_user 多 provider 结构(盘点结论)

同一 agent_user 并存 `anthropic_*`/`openai_*`/`codex_model`(部分含 `dsh_*`)多组 provider 字段——**结构化建模**: Reasonix `AgentUser` 已有 `Provider/BaseURL/Model/Effort` 四元组,迁移时按"一组 provider 四元组 = 一条 AgentUser 记录"归一,保留多 provider 并存能力(拍板项 B)。

---

## 3. 推荐架构: 单向只读导入

```
mult_agent_mcp/teams_data.json ──(ImportFromMCP 只读读取)──► 映射校验
        ▲                                                          │
        │ 不写回(默认)                                              ▼
  (运行态权威,保持不动)                          .reasonix/team/team.json(v1,扩展可选字段)
        ▲                                                   + agent_users.json(新)
        │                                                   + 导入备份/导入报告
  用户拍板后经 [TEAM] 第四屏/API 手动维护 ────────────────────────┘
```

- **权威方向**: mult_agent_mcp 保持运行态权威;Reasonix 侧为配置模板。单向导入,不做双向同步(避免双权威漂移)。
- **幂等**: 导入按 `user_id`/成员 id 去重,可重复执行。
- **可逆**: 写前备份;删除新文件/新字段即回滚,不触碰 mult_agent_mcp。

---

## 4. 数据结构草案

### 4.1 team.json 扩展(schema_version 保持 1,全可选 omitempty,旧文件零迁移)

```go
type Team struct {                    // 现有字段不变,新增可选字段:
    Name                string
    Template            []MemberSlot
    DefaultAgentUserRef string
    AgentType           string       // +团队默认启动类型(claude/codex/自定义);空=旧行为
    Proxy               *ProxyConfig // +团队级代理;nil=关闭(旧行为)
}

type MemberSlot struct {
    MemberID     string
    Role         RoleID
    AgentUserRef string
    Status       MemberStatus
    Temporary    bool
    AgentType    string // +成员启动类型覆盖;空=继承团队
    ProxyEnabled *bool  // +成员代理覆盖开关;nil=继承团队;true=强制启用;false=强制关闭
}

type ProxyConfig struct {
    Enabled bool   `json:"enabled"`
    Host    string `json:"host"`
    Port    int    `json:"port"`
}
```

### 4.2 新 agent_users.json(独立文档,§2.1 既有 schema 落地)

```go
type AgentUsersDoc struct {
    Document                    // schema_version: 1
    AgentUsers []AgentUser `json:"agent_users"`
}

type AgentUser struct {         // types.go 定义,拍板后加一个明文字段:
    UserID       string
    Identity     string
    Provider     string
    BaseURL      string
    Model        string
    Effort       string
    APIKey       string   // +明文 key,仅因 0600 原子写才安全(§7.1 注);omitempty,永不渲染/入报告
    SecretRef    SecretRef // {StoreID};引用,永不携带密钥
    RBACBindings []RoleID
}
```

- 与 `team.json` 分离,理由: AgentUser 不属于任何 team(TASK.md §2.1 语义);写锁互不阻塞。
- 一致性: 成员绑定校验在**写入时**进行(见 §5),不做文件间扫描式校验。

---

## 5. TeamStore API 清单(复用 FileStore + CAS)

| API | 语义 | 拒绝条件 |
|---|---|---|
| `AddAgentUser(u AgentUser)` | 新增池条目 | 空 user_id / 重名 → `ErrAgentUserExists` |
| `DeleteAgentUser(id)` | 删除池条目 | 不存在 → `ErrAgentUserNotFound`;**最后一个拒绝** → `ErrLastAgentUser` |
| `ListAgentUsers()` | 列出池 | — |
| `BindAgentUser(team, member, ref)` | 绑定成员→AgentUser | 引用不存在 → `ErrAgentUserNotFound`;成员不存在 → `ErrMemberNotFound` |
| `UnbindAgentUser(team, member)` | 解绑回团队默认 | 同左 |
| `SetTeamAgentType(team, t)` / `SetMemberAgentType(team, member, t)` | 启动类型 | 非法类型(白名单外)→ `ErrInvalidAgent` |
| `SetTeamProxy(team, p)` / `SetMemberProxyOverride(team, member, enabled)` | 代理 | 同上 |
| `SetMemberWritePolicy(p)` | 成员增删门控(默认全员;leader-only 可配置) | 非法策略值 → `ErrInvalidPolicy`;leader-only 下 Add/DeleteMember → `ErrLeaderOnly` |
| `ImportFromMCP(path, opts)` | 只读导入(§8) | 文件不可读/损坏 → 报错并给出路径;key 默认剔除(`ImportCredentials` 可选携带,0600 落盘,永不入报告) |

全部走现有 `update()` CAS 循环:并发写者以 `ErrCASConflict` 显式失败,不静默覆盖;`canonicalize()` 规范化比较延续。

---

## 6. [TEAM] 第四屏: 成员池与代理配置

### 6.1 导航层级与状态机

```
ModeTeams(团队列表) ──u──► ModeAgentUsers(成员池列表) ──enter──► 池条目详情(绑定该 AgentUser 的成员)
      ▲                        │ esc 返回
      │                        ▼ esc
ModeList(成员名单) ──enter──► ModeContext(成员详情)  [现有]
      │ e / b(聚焦成员)
      ▼
写状态(teamInputKind 扩展): EditAgentType / EditProxyOverride / BindAgentUser
      └─ enter 确认 → store 发布 → reload 读回;esc 取消
```

- 新模式: `ModeAgentUsers`(池屏)。
- 新写状态: `teamInputEditAgentType`、`teamInputEditProxy`、`teamInputBindAgentUser`。
- 写状态收尾全部走现有 `confirm()` 模式: 写 → 发布 → 写后重载(`reload`),视图永远显示持久化状态(§8.3 写后读回语义延续)。

### 6.2 按键表(新键 u/e/b/p 与现有 a/d/s/j/k/enter/esc/q/space 无冲突)

| 位置 | 按键 | 动作 |
|---|---|---|
| ModeTeams | `u` | 进入成员池屏(ModeAgentUsers) |
| ModeAgentUsers | `a` / `d` | 增/删池条目(删除需确认;最后一个拒绝) |
| ModeAgentUsers | `enter` | 查看绑定该 AgentUser 的成员列表;`esc` 返回 |
| ModeList(成员名单) | `e` | 进入代理配置编辑(启动类型输入;`p` 循环 proxy 开关: 继承/强制开/强制关) |
| ModeList(成员名单) | `b` | 进入绑定循环: 循环候选池条目,`enter` 绑定,`esc` 解绑 |
| 任意写状态 | `enter` / `esc` | 确认 / 取消 |

### 6.3 空态与错误态

| 场景 | 呈现 |
|---|---|
| agent_users.json 缺失 | = 空池,可 `a` 创建第一条;不报错 |
| 损坏/schema 不匹配 | 覆盖层错误消息("Agent data unavailable: ..."),不渲染占位数据 |
| 成员引用不存在的 AgentUserRef | 成员行尾警示标记;`b` 重新绑定可修复,不崩溃 |
| 绑定/类型/代理非法 | `pickerErrMsg` 扩展映射(不存在/重复/非法类型/代理值),保持现有错误消息风格 |
| 导入不可读 | 报错 + 源路径;不静默跳过 |

---

## 7. provider / proxy / SecretRef 安全边界

1. **密钥零迁移(默认)**: `ImportFromMCP` 默认跳过 `*_api_key` 字段;导入/导出测试断言输出与落盘内容不含任何 key 字段(§9)。**拍板补充(2026-08-21)**: `ImportOptions.ImportCredentials=true` 可选携带明文 key 进 `agent_users.json`(`AgentUser.APIKey`)——安全条件是 0600 原子写 chokepoint(§3.4)与永不渲染/入报告(K2 延续)。
2. **SecretRef 纯引用**: agent_users.json 中 `secret_ref.store_id = "mult-agent-mcp:<user_id>"`,Reasonix 不解析、不注入密钥;注入密钥为后续单独授权项,不入本轮。
3. **渲染层脱敏**: TUI/日志/blackboard/消息渲染层永不输出 `SecretRef` 内容、`*_api_key`、proxy host/port 明文(host/port 仅管理屏显示,不进日志)。
4. **代理优先级**: 成员覆盖(nil=继承 / true=开 / false=关) > 团队 `Proxy.Enabled` > 关闭——由单一解析函数统一实现,测试锁定。
5. **启动类型白名单**: `claude` / `codex` 直接通过;自定义命令需显式确认并登记,拒绝空/危险命令。

---

## 8. 旧数据兼容与迁移

1. **team.json v1 零迁移**: 全部新增字段可选(`omitempty`),旧文件读入行为不变;`schema_version` 保持 1。
2. **legacy 回退延续**: `teams.json` 只读回退 + 首次写迁移的既有机制不动。
3. **ImportFromMCP 规则**:
   - 映射: `teams[].name` → Team;`members[].id` → MemberSlot(去重幂等);`role` → Role;`agent`+`model` → AgentType/AgentUserRef 绑定;`default_agent_user` → Team.DefaultAgentUserRef;`proxy` → Team.Proxy。
   - 运行态字段一律丢弃(§2.2)。
   - 写前备份: 生成 `team.json.<ts>.pre-import.bak` 与 `agent_users.json.<ts>.pre-import.bak`。
   - 产出导入报告(映射条数/跳过项/密钥字段断言结果),报告不入核心文档。

---

## 9. 测试 / 验收 / 回滚

| 层 | 用例 |
|---|---|
| store | AgentUser CRUD;最后一个删除拒绝;CAS 并发冲突;canonicalize 兼容 |
| 导入 | 映射正确性;幂等(重跑不重复);密钥字段断言(输入含 key 时拒绝/剔除);备份存在;损坏源报错 |
| 绑定 | 引用不存在拒绝;解绑回默认;成员/团队不存在错误路径 |
| 代理 | 优先级解析(覆盖>团队>关闭)全组合单测;非法 proxy 值拒绝 |
| TUI | 新 mode 导航/增删/绑定按键单测;写状态 enter/esc 收尾;空态/错误态渲染 |
| effect | 渲染层脱敏断言(SecretRef/key 不出现在任何 sink);写路径走真实 `boot.Build` 装配 |
| 回归 | 每阶段 repolint + 全量测试绿;既有团队/成员/生命周期功能不变 |

**验收门禁**: 每阶段独立合入,门禁 = repolint + 全量测试绿 + schema v1 兼容(旧文件可读)。
**回滚**: 删除新字段/新文件即回滚,零触碰 mult_agent_mcp;导入备份可恢复。

---

## 10. 分阶段交付

| 阶段 | 内容 | 交付物 / 验证 | 回滚 |
|---|---|---|---|
| P1 | 模型字段 + AgentUsersStore + ImportFromMCP | 导入幂等 + 密钥断言测试;备份机制 | 删 agent_users.json + 新字段 |
| P2 | TUI ModeAgentUsers 池管理屏(先只读列表,后写路径) | 按键/CRUD/脱敏 effect 测试 | 撤屏代码,数据不动 |
| P3 | 成员代理/绑定编辑(`e`/`b`/`p`) | 绑定校验 + 优先级单测 + effect 测试 | 同上 |
| P4 | 打磨 + 文档(并入 TASK.md 变更记录) | 全量回归绿 | 文档回滚 |

---

## 11. 拍板项(已拍板 2026-08-21,均按推荐项)

| # | 拍板项 | A | B | 已选 |
|---|---|---|---|---|
| A | 写入口权限 | 全员可写(本地文件,CAS 保护) | 仅 leader 可写(配置项切换,不硬编码) | **A** — 默认全员 CAS;`TeamStore.SetMemberWritePolicy(MemberWriteLeaderOnly)` 切换 leader-only,Add/DeleteMember 拒绝(`ErrLeaderOnly`) |
| B | agent_user provider 建模 | 多 provider 结构化(一组 provider 四元组=一条记录,保留并存) | 单 provider 简化(每用户仅一条主配置) | **A** — 导入按 provider 组拆分为 `key` / `key@provider` 多记录;成员 model 唯一匹配即绑定 |
| C | P4 写回 mult_agent_mcp | 不写回(单向只读,mult_agent_mcp 保持运行态权威) | 写回(双向同步;需解决双权威冲突与密钥注入授权) | **A** — 单向只读,只读 `teams_data.json`,永不写回 |
| D | proxy 是否进核心 schema | 进核心 schema(Team.Proxy + MemberSlot.ProxyEnabled,§4.1) | 不进核心(仅导入报告参考,UI 只读展示) | **A** — 进核心 schema;`ProxyFor` 单一解析函数锁优先级 |

> **拍板补充**: 明文凭据字段按 0600 文件权限保存(§7.1 注,`AgentUser.APIKey`);导入默认跳过 key,`ImportCredentials` 可选携带。
> **实施状态**: 领域层完成于 `internal/team`(agentusers.go / binding.go / proxy.go / import.go,含单测、CAS、0600、幂等导入);`internal/cli` TUI 未动(P2/P3 待后续)。

---

> **已拍板并实施领域层(2026-08-21);TUI 阶段(P2/P3)待后续。**
