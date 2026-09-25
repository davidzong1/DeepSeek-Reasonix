package boot

import (
	"fmt"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/workspacelease"
	"time"
)

// acquireBackgroundScope returns the scope a build owns, reusing the host's
// when one is supplied. leaseLabel is diagnostic: it lets a writer queued behind
// this one name it in the wait notice.
func acquireBackgroundScope(scope *jobs.SessionBackgroundScope, root string, sink event.Sink, stalledSeconds int, leaseLabel string) (*jobs.SessionBackgroundScope, error) {
	if scope != nil {
		if err := scope.Acquire(); err != nil {
			return nil, err
		}
		return scope, nil
	}
	// The wait notice is classified from the lease's own state, so a session
	// queued for the whole workspace is distinguishable from one queued for a
	// file — the signal a window needs to render a member as waiting. The
	// closure reads the owner through this variable because the lease cannot
	// name itself while it is still being built.
	var lease *workspacelease.Owner
	lease, err := workspacelease.New(root, config.WorkspaceLeaseDir(), func() {
		sink.Emit(workspaceLeaseWaitEvent(lease))
	})
	if err != nil {
		return nil, fmt.Errorf("initialize workspace write lease: %w", err)
	}
	lease.SetIdentity(leaseLabel)
	scope = jobs.NewSessionBackgroundScope(jobs.NewManager(sink,
		jobs.WithStalledWarningAfter(time.Duration(stalledSeconds)*time.Second),
		jobs.WithSessionOwnershipProbe(agent.SessionLeaseHeldByCurrentRuntime),
		jobs.WithJobStartObserver(lease.RetainUntil)), lease)
	return scope, nil
}

func releaseBackgroundBuild(scope *jobs.SessionBackgroundScope, controller *control.Controller) {
	if controller != nil {
		controller.ReleaseResources()
	} else {
		scope.Release(false)
	}
}

func stageModelRuntimePublication(res *BuildResult, opts Options) {
	_ = res.Owner.Gate.SweepAndForceExpire()
	res.Controller.StageReplacementPublication(func() {
		publishPreparedBuildResult(res)
		if opts.Extensions != nil && res.Plan != nil {
			go opts.Extensions.DrainPlan(res.Plan)
		}
	})
}
