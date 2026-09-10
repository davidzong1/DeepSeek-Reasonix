package agent

import (
	"strings"

	"reasonix/internal/tool"
)

// readOnlyExecutionBlock gates every tool call a read-only agent may make:
// statically declared tools when no dynamic call resolved, resolved dynamic
// calls by proxy action. The boundary's refusal outcomes are constructed by
// the helpers below so each gate stays a few branches.
func (a *Agent) readOnlyExecutionBlock(visible tool.Tool, resolved *tool.ResolvedCall) (toolOutcome, bool) {
	if a == nil || !a.readOnlyExecution {
		return toolOutcome{}, false
	}
	if resolved == nil {
		return a.blockStaticCall(visible)
	}
	switch resolved.ProxyAction {
	case "list", "inspect":
		if !resolved.SkipExecute || resolved.Target != nil || !resolved.ReadOnly {
			return readOnlyExecutionBlockedOutcome("execute a malformed dynamic inspection")
		}
		return toolOutcome{}, false
	case "decline":
		return readOnlyExecutionBlockedOutcome("decline a capability decision")
	case "call":
		return a.blockDynamicCall(resolved)
	default:
		return readOnlyExecutionBlockedOutcome("execute an unknown dynamic capability action")
	}
}

func readOnlyExecutionBlockedOutcome(reason string) (toolOutcome, bool) {
	return toolOutcome{
		output:  "blocked: read-only agent cannot " + reason,
		blocked: true,
		errMsg:  "blocked by read-only execution boundary",
	}, true
}

// Destructive MCP is left for the Executor; Planner must not misread this
// refusal as missing configuration or an unavailable MCP server.
func readOnlyExecutionDestructiveBlocked(name string) (toolOutcome, bool) {
	msg := "blocked: MCP capability " + name + " is destructive and is reserved for the Executor. Write the required operation into the plan/handoff so the Coordinator can hand it to the Executor; do not treat this as missing MCP configuration or an unavailable capability."
	return toolOutcome{
		output:  msg,
		blocked: true,
		errMsg:  "blocked: destructive MCP reserved for executor",
	}, true
}

// blockStaticCall gates a statically declared tool, which has no resolved
// dynamic call to disambiguate.
func (a *Agent) blockStaticCall(visible tool.Tool) (toolOutcome, bool) {
	if a.plannerMCPExecution && isMCPExecutionTarget(visible, "") {
		if !mcpServerAuthorized(visible) {
			return readOnlyExecutionBlockedOutcome("execute an MCP capability from an unauthorized server")
		}
		if readOnlyExecutionMCPDestructive(visible) {
			return readOnlyExecutionDestructiveBlocked(visible.Name())
		}
		return toolOutcome{}, false
	}
	if visible == nil || !visible.ReadOnly() {
		if reason := readOnlyExecutionUnwritableReason(visible); reason != "" {
			return readOnlyExecutionBlockedOutcome(reason)
		}
		return toolOutcome{}, false
	}
	if isInstalledMCPTool(visible) && !mcpServerAuthorized(visible) {
		return readOnlyExecutionBlockedOutcome("execute a reader from an unauthorized MCP server")
	}
	if readOnlyExecutionMCPDestructive(visible) {
		return readOnlyExecutionBlockedOutcome("execute a destructive MCP capability")
	}
	if h, ok := visible.(tool.ReadOnlyExecutionHostMutation); ok && h.ReadOnlyExecutionHostMutation() && !readOnlyExecutionAllowsMCPStartup(visible) {
		return readOnlyExecutionBlockedOutcome("start or mutate a host capability")
	}
	return toolOutcome{}, false
}

// blockDynamicCall gates one resolved dynamic call: an unresolved target is a
// planner-execution handoff only when the recorded capability completed, and
// read-only dynamic readers still face the same server and host checks as
// statically declared ones.
func (a *Agent) blockDynamicCall(resolved *tool.ResolvedCall) (toolOutcome, bool) {
	if resolved.Target == nil {
		if a.plannerMCPExecution && resolved.HostCompleted && resolved.SkipExecute && resolved.ReadOnly && !resolved.Unavailable {
			if _, ok := parseMCPServerCapabilityID(resolved.CapabilityID); ok {
				return toolOutcome{}, false
			}
		}
		return readOnlyExecutionBlockedOutcome("execute an unresolved dynamic capability")
	}
	if a.plannerMCPExecution && plannerAllowsMCPTarget(resolved.Target, resolved.TargetName) {
		return a.blockPlannerMCPCall(resolved)
	}
	if !resolved.ReadOnly {
		if reasoner, ok := resolved.Target.(tool.ReadOnlyExecutionBlockReason); ok && strings.TrimSpace(reasoner.ReadOnlyExecutionBlockReason()) != "" {
			return readOnlyExecutionBlockedOutcome(reasoner.ReadOnlyExecutionBlockReason())
		}
		return readOnlyExecutionBlockedOutcome("execute a state-changing dynamic capability")
	}
	if isInstalledMCPTool(resolved.Target) && !mcpServerAuthorized(resolved.Target) {
		return readOnlyExecutionBlockedOutcome("execute a dynamic reader from an unauthorized MCP server")
	}
	if readOnlyExecutionMCPDestructive(resolved.Target) {
		return readOnlyExecutionBlockedOutcome("execute a destructive MCP capability")
	}
	if h, ok := resolved.Target.(tool.ReadOnlyExecutionHostMutation); ok && h.ReadOnlyExecutionHostMutation() && !readOnlyExecutionAllowsMCPStartup(resolved.Target) {
		return readOnlyExecutionBlockedOutcome("start or mutate a host capability")
	}
	return toolOutcome{}, false
}

// blockPlannerMCPCall gates the planner-execution MCP branch: connect targets
// need planner authorization, other targets need server authorization, and
// destructive capabilities stay reserved for the Executor.
func (a *Agent) blockPlannerMCPCall(resolved *tool.ResolvedCall) (toolOutcome, bool) {
	if isMCPLifecycleConnectTarget(resolved.Target) {
		if !plannerMCPConnectAllowed(resolved.Target) {
			return readOnlyExecutionBlockedOutcome("start an unauthorized MCP server")
		}
	} else if !mcpServerAuthorized(resolved.Target) {
		return readOnlyExecutionBlockedOutcome("execute an MCP capability from an unauthorized server")
	}
	if readOnlyExecutionMCPDestructive(resolved.Target) {
		name := resolved.TargetName
		if name == "" {
			name = resolved.CapabilityID
		}
		return readOnlyExecutionDestructiveBlocked(name)
	}
	return toolOutcome{}, false
}
