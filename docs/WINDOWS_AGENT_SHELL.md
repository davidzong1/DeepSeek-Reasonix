# Windows Agent shell / Windows Agent 解释器

Windows Agent commands use PowerShell. PowerShell 7 is preferred; Windows PowerShell 5.1 remains supported. Valid configured PowerShell paths are retained. Legacy Git Bash preferences remain readable without rewriting the saved configuration, but do not select the Windows Agent interpreter. Remote execution uses the remote host's operating system.

Windows Agent 命令使用 PowerShell，优先选择 PowerShell 7，同时支持 Windows PowerShell 5.1。有效的 PowerShell 配置路径会保留。历史 Git Bash 偏好仍可读取且不会自动改写，但不再决定 Windows Agent 的解释器。远程执行根据远端主机操作系统选择解释器。

Ordinary foreground commands share session-local directories, variables, functions and environment changes. Background and permission-specific calls remain isolated. Timeout, cancellation or a broken shell resets persistent state; the next command starts from the workspace and initial environment. PowerShell 5.1 does not support `&&` or `||`.

普通前台命令在同一会话中保留目录、变量、函数和环境修改。后台及特殊权限调用保持隔离。超时、取消或解释器故障会清空持久状态；下一条命令从工作区和初始环境启动。PowerShell 5.1 不支持 `&&` 或 `||`。

For emergency rollback, start Reasonix with `REASONIX_POWERSHELL_ONESHOT=1` to use isolated PowerShell calls. This does not restore persistent Git Bash. Restarting discards the previous persistent shell state.

紧急回退时，可设置 `REASONIX_POWERSHELL_ONESHOT=1` 后启动 Reasonix，改用独立 PowerShell 调用。此方式不会恢复持久 Git Bash，重启会丢弃之前的持久解释器状态。

An unconfirmed submission is not evidence that sending failed. Reconnect and inspect the session before sending again. Tool timeouts and cancellations take priority over legacy zero exit codes; missing results are shown as unknown.

提交结果未确认不代表发送失败。请先重连并检查会话，再决定是否重新发送。工具超时和取消状态优先于历史零退出码；缺少结果时显示未知状态。
