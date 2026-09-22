package boot

import (
	"reasonix/internal/agent"
	"reasonix/internal/hook"
)

// hookLeaseSurface adapts a hook runner's write surface to the agent's lease
// sizing, so a hook whose writes are proven bounded no longer forces a
// whole-workspace hold. The agent package deliberately does not import the hook
// package, so boot owns this seam.
func hookLeaseSurface(runner *hook.Runner) agent.ToolHookWriteSurfaceFunc {
	return func(toolName string) agent.ToolHookWriteSurface {
		surface := runner.ToolCallWriteSurface(toolName)
		return agent.ToolHookWriteSurface{
			Fires:           surface.Fires,
			Paths:           surface.Paths,
			ToolPathsScoped: surface.ToolPathsScoped,
			WholeWorkspace:  surface.WholeWorkspace,
		}
	}
}
