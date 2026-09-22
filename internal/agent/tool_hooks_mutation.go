package agent

import "strings"

// What a tool-call hook can write decides how wide the workspace write lease must
// be. A hook is user shell code, so the surface is evidence-based: a proven reader
// does not widen the lease, a bounded write narrows it, and the unproven keeps it.

// ToolHookWriteSurface is what the hooks firing for one tool can write. The zero
// value means no tool-call hook fires; WholeWorkspace is authoritative.
type ToolHookWriteSurface struct {
	Fires           bool
	Paths           []string
	ToolPathsScoped bool
	WholeWorkspace  bool
}

// ToolHookWriteSurfaceFunc reports one tool's hook write surface. It is the seam
// boot uses to adapt its hook runner without making this package depend on it.
type ToolHookWriteSurfaceFunc func(toolName string) ToolHookWriteSurface

type toolMutationHookReporter interface {
	ToolMutationHooksEnabled() bool
}

// toolHookWriteSurface resolves the hooks' write surface for one tool call. With
// no reporter installed, a custom ToolHooks implementation keeps the
// conservative coverage its unknown callbacks require.
func toolHookWriteSurface(hooks ToolHooks, report ToolHookWriteSurfaceFunc, toolName string) ToolHookWriteSurface {
	if report != nil {
		return report(toolName)
	}
	if toolHooksMayMutateWorkspace(hooks) {
		return ToolHookWriteSurface{Fires: true, WholeWorkspace: true}
	}
	return ToolHookWriteSurface{}
}

func toolHooksMayMutateWorkspace(hooks ToolHooks) bool {
	if hooks == nil {
		return false
	}
	if reporter, ok := hooks.(toolMutationHookReporter); ok {
		return reporter.ToolMutationHooksEnabled()
	}
	// Custom ToolHooks implementations predate the capability report. Preserve
	// conservative coverage because their callbacks may write files.
	return true
}

// hookToolName is the name this call's hooks fire with: the permission-facing
// name, which for a proxy tool is the real MCP target.
func hookToolName(plan *toolCallPlan) string {
	if plan == nil {
		return ""
	}
	if name := strings.TrimSpace(plan.permName); name != "" {
		return name
	}
	if plan.runTool != nil {
		return plan.runTool.Name()
	}
	return plan.evidenceName
}

// writeLeaseScope decides the workspace lease one call needs: whole reports the
// call must take the whole workspace, otherwise scope is the bounded write scope
// it holds. An unproven hook, or one bounded to call paths that cannot be named,
// keeps the whole workspace.
func writeLeaseScope(surface ToolHookWriteSurface, hookPaths, toolPaths []string, toolBounded bool) (scope []string, whole bool) {
	if surface.WholeWorkspace {
		return nil, true
	}
	scope = append(scope, hookPaths...)
	if surface.ToolPathsScoped {
		if !toolBounded {
			return nil, true
		}
		scope = append(scope, toolPaths...)
	}
	if len(scope) > 0 {
		return scope, false
	}
	if toolBounded {
		return toolPaths, false
	}
	return nil, true
}

// workspaceWritePaths resolves a hook's proven literal targets against the
// workspace root and drops targets outside it: a hook logging to /tmp needs no
// workspace lease at all.
func workspaceWritePaths(workspaceRoot string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		// Anything the shell could still expand is not a path this call can name.
		if path == "" || strings.ContainsAny(path, "*?[{~$`") {
			continue
		}
		resolved := resolveMaybeRelative(workspaceRoot, path)
		if !pathWithinFold(workspaceRoot, resolved) {
			continue
		}
		out = append(out, resolved)
	}
	return out
}
