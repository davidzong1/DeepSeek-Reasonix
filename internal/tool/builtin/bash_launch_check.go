package builtin

import (
	"context"
	"strings"
	"time"

	"reasonix/internal/sandbox"
	"reasonix/internal/shellrun"
	"reasonix/internal/tool"
)

func bashLaunchFailure(ex *tool.ShellExecution, start time.Time, err error) (tool.DetailedResult, error) {
	ex.State = tool.ShellStateNotRun
	ex.FailurePhase = tool.ShellPhaseAuthorization
	if strings.Contains(err.Error(), "session temporary") {
		ex.FailurePhase = tool.ShellPhaseLaunch
	}
	ex.MutationRisk = tool.ShellMutationNotStarted
	ex.DurationMs = time.Since(start).Milliseconds()
	return tool.DetailedResult{Execution: ex}, err
}

// checkLaunch probes the same environment used by the selected execution path.
func (b bash) checkLaunch(ctx context.Context, p bashParams, sh sandbox.Shell, prepared sandbox.Prepared, cmdEnv []string, start time.Time, ex *tool.ShellExecution) (tool.DetailedResult, error, bool) {
	probeEnv := cmdEnv
	if b.shouldUsePersistent(ctx, p, sh) {
		probeEnv = persistEnv(cmdEnv)
	}
	failure := shellrun.CheckShellLaunch(ctx, shellrun.Request{
		ProbeArgv: shellrun.WindowsProbeArgv(b.specForCall(ctx), sh, prepared.SessionTemp),
		Dir:       b.workDir, Env: probeEnv, Timeout: b.foregroundTimeout(),
		ShellKind: sh.Kind.String(), ShellPath: sh.Path,
	})
	if failure == nil {
		return tool.DetailedResult{}, nil, false
	}
	ex.State, ex.FailurePhase = failure.State, failure.FailurePhase
	ex.ExitCode, ex.OutputTail = failure.ExitCode, failure.OutputTail
	ex.MutationRisk = tool.ShellMutationNotStarted
	ex.DurationMs = time.Since(start).Milliseconds()
	return tool.DetailedResult{Output: failure.Combined, Execution: ex}, failure.Err, true
}
