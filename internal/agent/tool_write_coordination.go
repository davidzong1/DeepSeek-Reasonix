package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// prepareWriteCoordination resolves the real execution target, then acquires
// every write guard that must cover hooks, checkpoints, and Execute.
func (a *Agent) prepareWriteCoordination(ctx context.Context, plan *toolCallPlan) (toolOutcome, bool) {
	plan.runTool = plan.execTool
	plan.runArgs = plan.execArgs
	plan.hookSurface = toolHookWriteSurface(a.svc.hooks, a.svc.hookWriteSurface, hookToolName(plan))
	plan.hookWritePaths = workspaceWritePaths(a.writeWorkspaceRoot, plan.hookSurface.Paths)
	plan.hooksMayMutateWorkspace = plan.hookSurface.WholeWorkspace ||
		plan.hookSurface.ToolPathsScoped || len(plan.hookWritePaths) > 0
	if plan.resolved.Target != nil {
		plan.runTool = plan.resolved.Target
		plan.runArgs = plan.resolved.Args
		if len(plan.runArgs) == 0 {
			plan.runArgs = json.RawMessage(`{}`)
		}
	}
	if (plan.effects.WorkspaceMutation || plan.hooksMayMutateWorkspace) && a.svc.workspaceLease != nil {
		release, err := a.acquireCallWriteGuard(ctx, plan)
		if err != nil {
			return toolOutcome{
				output:  fmt.Sprintf("blocked: the workspace did not become available for writing: %v", err),
				blocked: true, errMsg: "blocked: workspace write lease unavailable",
			}, true
		}
		plan.releaseLease = release
	}
	release, err := a.reserveCoordinatedParentWrite(plan)
	if err != nil {
		return writeClaimBlockedOutcome(err), true
	}
	plan.releaseParentWrite = release
	return a.applyLiveWriteReservation(ctx, plan)
}

func (a *Agent) reserveCoordinatedParentWrite(plan *toolCallPlan) (func(), error) {
	if plan.hooksMayMutateWorkspace &&
		a.svc.writeScheduler != nil && a.subagentDepth == 0 {
		claim, err := WholeWorkspaceWriteClaim(a.writeWorkspaceRoot)
		if err != nil {
			return func() {}, err
		}
		return a.svc.writeScheduler.ReserveParentWrite(claim)
	}
	return a.reserveParentWrite(plan.runTool, plan.runArgs, !plan.effects.WorkspaceMutation)
}

// acquireCallWriteGuard takes the in-process write token first, then the
// workspace lease. Peers therefore queue among themselves before racing the
// cross-process lock; the token never replaces that lease.
func (a *Agent) acquireCallWriteGuard(ctx context.Context, plan *toolCallPlan) (func(), error) {
	toolPaths, toolBounded := a.pathBoundWriteScope(plan)
	scope, whole := writeLeaseScope(plan.hookSurface, plan.hookWritePaths, toolPaths, toolBounded)
	intent := WriteIntent{
		Paths: scope,
		Whole: whole,
		Label: writeIntentLabel(a.writeWorkspaceRoot, scope, whole),
	}
	token := a.acquireWriteIntent(ctx, intent)
	release, err := a.acquireWorkspaceLease(ctx, plan, scope, whole)
	if err != nil {
		token()
		return nil, err
	}
	return func() {
		release()
		token()
	}, nil
}

func (a *Agent) acquireWorkspaceLease(ctx context.Context, plan *toolCallPlan, scope []string, whole bool) (func(), error) {
	noop := func() {}
	if a == nil || a.svc.workspaceLease == nil || plan == nil || plan.runTool == nil {
		return noop, nil
	}
	if whole {
		return a.svc.workspaceLease.HoldWrite(ctx)
	}
	return a.svc.workspaceLease.HoldWriteForPaths(ctx, scope)
}

// pathBoundWriteScope returns the call's own write paths when the tool declares
// them; ok is false when the tool cannot name what it writes.
func (a *Agent) pathBoundWriteScope(plan *toolCallPlan) ([]string, bool) {
	name := plan.runTool.Name()
	if !pathBoundWriterNames[name] {
		return nil, false
	}
	paths, err := extractWritePathsFromArgs(name, a.writeWorkspaceRoot, plan.runArgs)
	if err != nil || len(paths) == 0 {
		return nil, false
	}
	for i := range paths {
		paths[i] = resolveMaybeRelative(a.writeWorkspaceRoot, paths[i])
	}
	return paths, true
}

func (a *Agent) applyLiveWriteReservation(ctx context.Context, plan *toolCallPlan) (toolOutcome, bool) {
	if a == nil || plan == nil || a.svc.writeScheduler == nil || plan.runTool == nil {
		return toolOutcome{}, false
	}
	id := SubagentClaimID(ctx)
	if id == 0 {
		return toolOutcome{}, false
	}
	name := plan.runTool.Name()
	if plan.hooksMayMutateWorkspace {
		if err := a.svc.writeScheduler.MarkOpaque(id); err != nil {
			return writeClaimBlockedOutcome(err), true
		}
		return toolOutcome{}, false
	}
	if !plan.effects.WorkspaceMutation {
		return toolOutcome{}, false
	}
	if pathBoundWriterNames[name] {
		claim, err := parentWriteReservation(a.writeWorkspaceRoot, name, plan.runArgs)
		if err != nil {
			return writeClaimBlockedOutcome(err), true
		}
		if err := a.svc.writeScheduler.Realize(id, claim); err != nil {
			return writeClaimBlockedOutcome(err), true
		}
		return toolOutcome{}, false
	}
	if parentWriteGuardTarget(name) {
		if err := a.svc.writeScheduler.MarkOpaque(id); err != nil {
			return writeClaimBlockedOutcome(err), true
		}
	}
	return toolOutcome{}, false
}

func writeClaimBlockedOutcome(err error) toolOutcome {
	return toolOutcome{
		output: "blocked: " + err.Error(), blocked: true,
		errMsg: "blocked: write path claimed by background subagent",
	}
}
