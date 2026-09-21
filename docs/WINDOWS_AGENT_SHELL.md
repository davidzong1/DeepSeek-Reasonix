# Windows Agent shell / Windows Agent 解释器

Windows Agent commands use PowerShell. PowerShell 7 is preferred; Windows PowerShell 5.1 remains supported. Valid configured PowerShell paths are retained. Legacy Git Bash preferences remain readable without rewriting the saved configuration, but do not select the Windows Agent interpreter. Remote execution uses the remote host's operating system.

Windows Agent 命令使用 PowerShell，优先选择 PowerShell 7，同时支持 Windows PowerShell 5.1。有效的 PowerShell 配置路径会保留。历史 Git Bash 偏好仍可读取且不会自动改写，但不再决定 Windows Agent 的解释器。远程执行根据远端主机操作系统选择解释器。

The provider-visible Windows shell tool is `pwsh`. Each foreground call runs in a fresh PowerShell process, so directories, variables, functions, and environment changes do not carry into the next call. Use `run_in_background=true` for servers and watchers; it returns a `pwsh-*` job id that can be read with `job_output` and stopped with `job_kill`. PowerShell 5.1 does not support `&&` or `||`; portable calls should use `;` or `if ($?) { ... }`.

Windows 向 provider 暴露的 shell 工具名为 `pwsh`。每次前台调用都使用全新的 PowerShell 进程，因此目录、变量、函数和环境修改不会带入下一次调用。服务器和 watcher 应设置 `run_in_background=true`，调用会返回 `pwsh-*` job id，可用 `job_output` 读取、用 `job_kill` 停止。PowerShell 5.1 不支持 `&&` 或 `||`；兼容写法应使用 `;` 或 `if ($?) { ... }`。

For a local OpenMAIC checkout whose start command is `npm run start`, a
background call looks like this (replace the directory and start command for
the deployment):

```json
{"command":"Set-Location 'C:\\OpenMAIC'; $env:PORT='3000'; npm run start","description":"Start OpenMAIC service","run_in_background":true}
```

Read its incremental log or wait for a terminal state with
`job_output({"job_id":"pwsh-1","wait":false})`, and stop the whole process
tree with `job_kill({"job_id":"pwsh-1","reason":"service no longer needed"})`.

OpenMAIC and its children may use any stdio mode, including Node/libuv
`stdio: "pipe"` capture: Windows commands run as the current OS user without a
restricted token, so no sandbox-level `EPERM` occurs. Runtime failures are
reported as `execution` results with their real exit codes.

如果本地 OpenMAIC 的启动命令是 `npm run start`，可以按上面的方式后台启动；
请按实际部署替换目录和启动命令。返回 `pwsh-*` 后，使用 `job_output` 读取增量
日志或等待终态，使用 `job_kill` 停止完整进程树。

OpenMAIC 及其子进程可以使用任意 stdio 方式，包括 Node/libuv 的 `stdio: "pipe"`
捕获：Windows 命令以当前系统账户运行、没有受限令牌，因此不会出现沙箱层面的
`EPERM`。运行期失败以真实退出码作为 `execution` 结果报告。

Reasonix no longer runs a nested shell/child-process preflight before each Windows command, and it no longer launches commands through a restricted-token runner: the command starts directly as the current OS user. A process that fails to start is still reported as not run rather than as command output, so stdout/stderr cannot claim that execution never started; runtime failures remain `execution` with possible partial effects.

Reasonix 不再在每条 Windows 命令前运行嵌套 shell／子进程预检，也不再通过
restricted-token runner 启动命令：命令直接以当前系统账户运行。进程启动失败仍按
“未执行”报告而不是混入命令输出，命令输出无法伪造“未执行”状态；运行期失败仍保持
`execution` 和可能已部分修改的状态。

An unconfirmed submission is not evidence that sending failed. Reconnect and inspect the session before sending again. Tool timeouts and cancellations take priority over legacy zero exit codes; missing results are shown as unknown.

提交结果未确认不代表发送失败。请先重连并检查会话，再决定是否重新发送。工具超时和取消状态优先于历史零退出码；缺少结果时显示未知状态。
