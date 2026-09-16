# Windows shell startup / Windows Shell 启动

Windows auto selection uses PowerShell 7 or Windows PowerShell and never falls
back to Bash, even if an old Bash path remains configured. Restricted Windows
shell execution rejects POSIX interpreters before process preparation. Explicit
Bash selection remains available in full-access mode.
The tool description uses the selected interpreter's syntax and remains stable
within a configured session.

Windows 自动选择只使用 PowerShell 7 或 Windows PowerShell，不再回退到 Bash，
即使配置中留有旧 Bash 路径。受限沙箱会在准备进程前拒绝 POSIX 解释器；完整权限模式
仍可显式选择 Bash。工具描述使用实际解释器的语法，在同一配置的会话内保持稳定。

Host discovery is not sandbox qualification. Before model shell execution
(foreground, background or persistent) and user shell execution, a harmless
shell-and-child probe runs with the command's sandbox policy, workspace,
environment and private temporary directory. Failures retain bounded original
output and report that the requested command was not run. Repeated failures are
cached for 30 seconds, keyed by launch argv (including sandbox policy), cwd and
environment. Persistent PTY startup also preserves diagnostics and does not
silently retry a started-but-unready shell through the one-shot runner.

宿主环境发现可执行文件不代表沙箱内可用。模型的普通、后台、持久 Shell 入口，以及
用户直接执行入口，会先用实际策略、工作目录、环境和私有临时目录检查 Shell 与子进程。
失败时保留有界的原始输出，并明确业务命令未执行。失败缓存持续 30 秒，按启动参数
（包含沙箱策略）、工作目录和环境隔离。持久 PTY 启动失败也保留诊断，不再静默退回
一次性执行重复启动已经失败的解释器。

## Qualification boundary / 验证边界

These changes do **not** make MSYS/Cygwin compatible with restricted tokens.
`CreateFileMapping ... Win32 error 5` and the `cygheap_user::init` token-access
signature identify initialization denial; exit status 256 alone does not.
Native PowerShell can still launch an incompatible MSYS child. Ordinary command
failures therefore retain execution/may-be-partial semantics, even when an
external child reports initialization denial. No token flags, filesystem ACLs,
or sandbox enforcement are weakened by this change.

这些改动**尚未解决 MSYS/Cygwin 与受限令牌的兼容性**。映射创建错误和 cygheap 令牌
访问错误可识别初始化权限失败，但单独的退出码 256 不足以判断。PowerShell 仍可能调用
不兼容的 MSYS 子进程，所以业务命令中的此类失败仍按执行失败处理，不能声称整条命令
没有产生修改。本次改动不放宽令牌、文件权限或沙箱限制。

Windows acceptance must run, not merely cross-compile:

Windows 验收必须实际运行，交叉编译不能替代：

```
go test ./internal/shellrun -run TestWindowsNativeShellPreflightRetainsWriteBoundary -v
go test ./internal/winsandbox -run 'TestWindows(SandboxAllowsWorkspaceWriteAndDeniesOutside|WriteRestrictedReadOnlyAllowsReadsAndDeniesWrites|RestrictedRuntimeCompatibility)' -v
```

An MSYS compatibility repair additionally needs real Git Bash/grep child-process
tests in both read-only and workspace-write modes, with denied outside writes,
before changing the restricted-token implementation. This remains unqualified
until a Windows host is available.

修改受限令牌实现前，还需要在真实 Windows 上验证 Git Bash/grep 子进程，覆盖只读与
工作区可写两种模式，并证明工作区外写入仍被拒绝。目前仍缺少这项 Windows 实测证据。
