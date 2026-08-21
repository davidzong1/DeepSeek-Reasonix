# 71 工具三层映射

依据 docs/team-mcp-port/TASK.md §6.1-6.2 的六列模板,把旧 MCP 实现
`cache/mult_agent_mcp/mult_agent_mcp.py` 中所有 `@mcp.tool` 工具映射到新架构
(核心 internal/team / 插件 plugin capability / 废弃),每工具一行,零孤儿。

## 数量:71(已解除 BLOCKED)

TASK §6 初版声称 72 个工具,实际注册源**只有 71 个**;TASK.md v1.1.2 已按此更正,
两侧一致,差异关闭。证据:

- `grep -c '@mcp.tool'` = 72,但其中 1 处是 **5204 行注释**
  (`# 便车在 @mcp.tool 装饰器层… 而非在 66 个工具`),并非装饰器。
- 其余 71 处均为列首 `@mcp.tool` 装饰器,逐一与后面紧跟的 `def` 名配对
  (下表"旧证据符号:行号"列),71 装饰器 = 71 工具,一一对应,无遗漏。
- 无任何动态注册路径:5290 行 `_mcp_tool_orig = mcp.tool` 与 5351 行
  `mcp.tool = _mcp_tool_with_nudge` 只是装饰器包装(保留 `__name__`/`__doc__`,
  不注册新工具);全文件无 `mcp.tool(...)` 调用式注册、无 `add_tool`/
  `register_tool`、无缩进装饰器、无 async def。

因此下表**恰好 71 行**。双向差集由两道护栏钉住:
`internal/team/toolmapping_test.go`(内联 71 名,CI 可跑,不依赖 cache/)与
`scripts/check-team-tool-mapping.sh`(对照真实注册源,cache/ 缺失时跳过)。

## 映射表

| 旧工具名 | 旧证据符号:行号 | 语义域 | 新落点 | 状态 | 验收证据 |
|---|---|---|---|---|---|
| team_create | mult_agent_mcp.py:5358 def team_create | team-registry | internal/team/schema.go TeamDoc + storage.go Store.Save | 核心 | storage_test.go / schema_test.go 往返 |
| team_set_default_agent | mult_agent_mcp.py:5394 def team_set_default_agent | team-registry | internal/team/schema.go Team.DefaultAgentUserRef | 核心 | types_test.go 断言 AgentUserRef 引用 |
| team_get_default_agent | mult_agent_mcp.py:5413 def team_get_default_agent | team-registry | internal/team/schema.go Team.DefaultAgentUserRef | 核心 | types_test.go 断言 AgentUserRef 引用 |
| list_teams | mult_agent_mcp.py:5430 def list_teams | team-registry | internal/team/schema.go TeamDoc Load | 核心 | storage_test.go Load 测试 |
| delete_team | mult_agent_mcp.py:5456 def delete_team | team-registry | internal/team/storage.go CompareAndSwap 移除 | 核心 | cas_test.go CAS 冲突测试 |
| add_member | mult_agent_mcp.py:5523 def add_member | member-registry | internal/team/types.go MemberSlot 模板 | 核心 | schema_test.go MemberSlot 往返 |
| remove_member | mult_agent_mcp.py:5570 def remove_member | member-registry | internal/team/types.go MemberSlot 生命周期 | 核心 | types_test.go MemberStatus 状态机 |
| list_members | mult_agent_mcp.py:5604 def list_members | member-registry | internal/team/schema.go TeamDoc.Load 成员视图 | 核心 | storage_test.go Load 测试 |
| set_leader | mult_agent_mcp.py:5643 def set_leader | leadership | — | 废弃 | tmux 接管产物;新域 leader 是 RoleID,无接管 |
| member_set_agent | mult_agent_mcp.py:5670 def member_set_agent | agent-registry | internal/team/schema.go AgentUsersDoc + AgentUser | 核心 | schema_test.go AgentUsersDoc 往返 |
| claim_leader | mult_agent_mcp.py:5691 def claim_leader | leadership | — | 废弃 | tmux 接管产物;新域 leader 是 RoleID,无接管 |
| unclaim_leader | mult_agent_mcp.py:5772 def unclaim_leader | leadership | — | 废弃 | tmux 接管产物;新域 leader 是 RoleID,无接管 |
| setup_codex_mcp | mult_agent_mcp.py:5809 def setup_codex_mcp | mcp-registry | 插件 Capability mcp-registry.ctl | 插件 | architecture P5 plugin Host 测试 |
| remove_codex_mcp | mult_agent_mcp.py:5824 def remove_codex_mcp | mcp-registry | 插件 Capability mcp-registry.ctl | 插件 | architecture P5 plugin Host 测试 |
| check_agent_setup | mult_agent_mcp.py:5838 def check_agent_setup | agent-inspect | 插件 Capability agent-inspect.view | 插件 | architecture P5 plugin UIHub 测试 |
| get_server_config | mult_agent_mcp.py:5893 def get_server_config | server-config | 插件 Capability server-config.view | 插件 | architecture P5 plugin UIHub 测试 |
| launch_team_terminals | mult_agent_mcp.py:5932 def launch_team_terminals | terminal-lifecycle | 插件 Capability terminal-lifecycle.ctl | 插件 | architecture P5 plugin Host 测试 |
| kill_team_terminals | mult_agent_mcp.py:6299 def kill_team_terminals | terminal-lifecycle | 插件 Capability terminal-lifecycle.ctl | 插件 | architecture P5 plugin Host 测试 |
| terminal_status | mult_agent_mcp.py:6322 def terminal_status | terminal-status | 插件 Capability terminal-status.view | 插件 | architecture P5 plugin UIHub 测试 |
| member_terminal_status | mult_agent_mcp.py:6379 def member_terminal_status | terminal-status | 插件 Capability terminal-status.view | 插件 | architecture P5 plugin UIHub 测试 |
| leader_list_team | mult_agent_mcp.py:6456 def leader_list_team | leader-registry | internal/team/schema.go TeamDoc.Load 成员+状态视图 | 核心 | storage_test.go Load 测试 |
| leader_assign_subtask | mult_agent_mcp.py:6513 def leader_assign_subtask | task-dispatch | internal/team/types.go Task + TaskStatusAssigned | 核心 | types_test.go Task 状态机 |
| leader_broadcast | mult_agent_mcp.py:6603 def leader_broadcast | message-feed | internal/team/types.go Message To=* 广播 | 核心 | types_test.go MessageKindSystem 断言 |
| leader_batch_ack | mult_agent_mcp.py:6687 def leader_batch_ack | outbox | — | 废弃 | outbox 投递机制被 Message 类型流取代 |
| leader_outbox_status | mult_agent_mcp.py:6757 def leader_outbox_status | outbox | — | 废弃 | outbox 投递机制被 Message 类型流取代 |
| leader_flush_outbox | mult_agent_mcp.py:6775 def leader_flush_outbox | outbox | — | 废弃 | outbox 投递机制被 Message 类型流取代 |
| leader_select_task_members | mult_agent_mcp.py:6803 def leader_select_task_members | task-dispatch | internal/team/types.go Role/RBACRule 角色推断 | 核心 | rbac_test.go 规则判定测试 |
| leader_broadcast_to_relevant | mult_agent_mcp.py:6849 def leader_broadcast_to_relevant | message-feed | internal/team/types.go Message To=* + Role 过滤 | 核心 | types_test.go MessageKindSystem 断言 |
| leader_assign_task_to_relevant | mult_agent_mcp.py:6916 def leader_assign_task_to_relevant | task-dispatch | internal/team/types.go Role/RBACRule 角色推断 | 核心 | rbac_test.go 规则判定测试 |
| leader_set_discussion_mode | mult_agent_mcp.py:6994 def leader_set_discussion_mode | discussion | — | 废弃 | 讨论模式被 Message 类型流(chat/approval)取代 |
| leader_start_discussion | mult_agent_mcp.py:7018 def leader_start_discussion | discussion | — | 废弃 | 讨论模式被 Message 类型流(chat/approval)取代 |
| leader_discussion_next_round | mult_agent_mcp.py:7132 def leader_discussion_next_round | discussion | — | 废弃 | 讨论模式被 Message 类型流(chat/approval)取代 |
| leader_end_discussion | mult_agent_mcp.py:7215 def leader_end_discussion | discussion | — | 废弃 | 讨论模式被 Message 类型流(chat/approval)取代 |
| leader_authorize_member | mult_agent_mcp.py:7233 def leader_authorize_member | terminal-approval | 插件 Capability terminal-approval.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_read_member_terminal | mult_agent_mcp.py:7287 def leader_read_member_terminal | terminal-observe | 插件 Capability terminal-observe.view | 插件 | architecture P5 plugin UIHub 测试 |
| leader_check_member_status | mult_agent_mcp.py:7365 def leader_check_member_status | member-status | internal/team/types.go MemberState 分类契约 | 核心 | types_test.go MemberState 优先级断言 |
| leader_monitor_members | mult_agent_mcp.py:7436 def leader_monitor_members | terminal-observe | 插件 Capability terminal-observe.view | 插件 | architecture P5 plugin UIHub 测试 |
| leader_checkpoint_set | mult_agent_mcp.py:7511 def leader_checkpoint_set | checkpoint | internal/team/schema.go BlackboardDoc rev-N | 核心 | schema_test.go BlackboardRevFile 测试 |
| leader_ack_checkpoint | mult_agent_mcp.py:7589 def leader_ack_checkpoint | checkpoint | internal/team/schema.go BlackboardDoc rev-N 读取 | 核心 | schema_test.go BlackboardRevFile 测试 |
| leader_get_recovery_context | mult_agent_mcp.py:7641 def leader_get_recovery_context | leader-lifecycle | — | 废弃 | tmux leader 生命周期产物;新域无对应状态 |
| leader_activate | mult_agent_mcp.py:7655 def leader_activate | leader-lifecycle | — | 废弃 | tmux leader 生命周期产物;新域无对应状态 |
| leader_sleep | mult_agent_mcp.py:7999 def leader_sleep | leader-lifecycle | — | 废弃 | tmux leader 生命周期产物;新域无对应状态 |
| leader_mark_task_complete | mult_agent_mcp.py:8067 def leader_mark_task_complete | task-dispatch | internal/team/types.go TaskStatusArchived/Reported | 核心 | types_test.go Task 状态机 |
| leader_configure_wakeup | mult_agent_mcp.py:8182 def leader_configure_wakeup | runtime-config | 插件 Capability runtime-config.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_configure_recovery | mult_agent_mcp.py:8254 def leader_configure_recovery | runtime-config | 插件 Capability runtime-config.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_set_member_mode | mult_agent_mcp.py:8300 def leader_set_member_mode | runtime-mode | 插件 Capability runtime-mode.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_grant_member_autonomy | mult_agent_mcp.py:8369 def leader_grant_member_autonomy | runtime-mode | 插件 Capability runtime-mode.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_configure_member_permissions | mult_agent_mcp.py:8503 def leader_configure_member_permissions | runtime-config | 插件 Capability runtime-config.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_configure_proxy | mult_agent_mcp.py:8553 def leader_configure_proxy | runtime-config | 插件 Capability runtime-config.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_get_proxy_config | mult_agent_mcp.py:8598 def leader_get_proxy_config | runtime-config | 插件 Capability runtime-config.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_configure_member_proxy | mult_agent_mcp.py:8664 def leader_configure_member_proxy | runtime-config | 插件 Capability runtime-config.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_clear_member_proxy_override | mult_agent_mcp.py:8727 def leader_clear_member_proxy_override | runtime-config | 插件 Capability runtime-config.ctl | 插件 | architecture P5 plugin Host 测试 |
| leader_add_member | mult_agent_mcp.py:8779 def leader_add_member | leader-registry | internal/team/types.go MemberSlot 模板扩展 | 核心 | schema_test.go MemberSlot 往返 |
| leader_remove_member | mult_agent_mcp.py:8846 def leader_remove_member | leader-registry | internal/team/types.go MemberSlot 生命周期 | 核心 | types_test.go MemberStatus 状态机 |
| leader_redefine_member | mult_agent_mcp.py:8881 def leader_redefine_member | leader-registry | internal/team/types.go MemberSlot.Role 变更 | 核心 | types_test.go MemberSlot 断言 |
| leader_launch_member_terminal | mult_agent_mcp.py:8921 def leader_launch_member_terminal | terminal-lifecycle | 插件 Capability terminal-lifecycle.ctl | 插件 | architecture P5 plugin Host 测试 |
| member_get_my_task | mult_agent_mcp.py:10475 def member_get_my_task | task-resume | internal/team/types.go Task + ResumeCount | 核心 | types_test.go Task 状态机 |
| member_report_result | mult_agent_mcp.py:10760 def member_report_result | task-report | internal/team/types.go TaskStatusReported + ReportRef | 核心 | types_test.go Task 状态机 |
| member_read_shared | mult_agent_mcp.py:10929 def member_read_shared | blackboard | internal/team/schema.go BlackboardDoc rev-N | 核心 | schema_test.go BlackboardRevFile 测试 |
| member_read_discussion | mult_agent_mcp.py:10967 def member_read_discussion | discussion | — | 废弃 | 讨论模式被 Message 类型流(chat/approval)取代 |
| member_report_discussion_conclusion | mult_agent_mcp.py:10988 def member_report_discussion_conclusion | discussion | — | 废弃 | 讨论模式被 Message 类型流(chat/approval)取代 |
| member_send_message | mult_agent_mcp.py:11051 def member_send_message | message-feed | internal/team/types.go MessageKindChat 消息投递 | 核心 | types_test.go MessageKind 断言 |
| member_check_leader_status | mult_agent_mcp.py:11113 def member_check_leader_status | leadership | — | 废弃 | tmux 存活检测产物;新域 leader 是 RoleID,无存活状态 |
| member_list_shared_files | mult_agent_mcp.py:11158 def member_list_shared_files | blackboard | internal/team/schema.go BlackboardDir 清单 | 核心 | schema_test.go BlackboardRevFile 测试 |
| member_acquire_file_lock | mult_agent_mcp.py:11232 def member_acquire_file_lock | file-lock | — | 废弃 | 锁被 storage.go CompareAndSwap 原子语义取代 |
| member_release_file_lock | mult_agent_mcp.py:11288 def member_release_file_lock | file-lock | — | 废弃 | 锁被 storage.go CompareAndSwap 原子语义取代 |
| member_list_file_locks | mult_agent_mcp.py:11306 def member_list_file_locks | file-lock | — | 废弃 | 锁被 storage.go CompareAndSwap 原子语义取代 |
| member_submit_patch | mult_agent_mcp.py:11325 def member_submit_patch | cas | internal/team/cas.go CompareAndSwap | 核心 | cas_test.go 冲突测试 |
| member_read_file | mult_agent_mcp.py:11382 def member_read_file | blackboard | internal/team/schema.go BlackboardDoc rev-N 读取 | 核心 | schema_test.go BlackboardRevFile 测试 |
| member_write_file | mult_agent_mcp.py:11429 def member_write_file | blackboard | internal/team/schema.go BlackboardDoc rev-N 写入 | 核心 | schema_test.go BlackboardRevFile 测试 |
| member_delete_file | mult_agent_mcp.py:11479 def member_delete_file | blackboard | — | 废弃 | Blackboard 只追加(immutable rev),无删除对位 |

统计:核心 30 / 插件 21 / 废弃 20,合计 71,与注册源一一对应,零孤儿。
