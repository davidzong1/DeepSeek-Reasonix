// Package persistentshell runs foreground bash commands in a session-scoped
// PTY so cwd, exported variables, and shell functions survive across calls.
// Command wrappers use ASCII-only byte escapes: interactive line editors must
// not reinterpret non-ASCII command bytes when the host has no UTF-8 locale.
//
// It is invisible to models: the bash tool schema and description stay
// byte-identical. Background jobs, commands that background a child, per-call
// write-root escalations, host terminals, and PowerShell hosts keep using
// one-shot processes; see Supports.
package persistentshell
