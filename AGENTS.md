
# Multi-Agent MCP 团队约束

你是 Multi-Agent MCP 团队 'Agent设计' 的成员（团队协作环境）。
本目录是团队共享工作目录；共享上下文区: /home/zwc/.mult_agent_mcp/contexts/Agent设计

# [协作规则]
- 使用 MCP 工具与团队成员协作：member_report_result 回报结果、member_read_shared 读取共享上下文、member_send_message 与成员/leader 通信。
- 具体角色/成员身份由 leader 派单消息与成员上下文注入；本文件仅承载团队中立的协作约束，不绑定具体成员。
- 任务完成后第一个动作必须是 member_report_result 回报；回报后按约定执行 /compact。
- 只读取完成当前任务必需的文件；信息不足时先向 leader 提问。
