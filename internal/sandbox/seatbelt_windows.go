//go:build windows

package sandbox

// Windows has no OS-level shell sandbox (see OSSandboxSupported), so every
// launch runs unwrapped as the current OS user.

// Command returns the shell invocation unwrapped.
func Command(_ Spec, sh Shell, command string) ([]string, bool) {
	return sh.argv(command), false
}

// CommandArgs returns the raw argv unwrapped.
func CommandArgs(_ Spec, args []string) ([]string, bool) {
	return args, false
}

// Available is always false on Windows.
func Available() bool { return false }
