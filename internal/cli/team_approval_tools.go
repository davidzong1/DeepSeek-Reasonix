package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

const (
	leaderListApprovalsName   = "leader_list_member_approvals"
	leaderResolveApprovalName = "leader_resolve_member_approval"
)

// leaderApprovalTool is the leader agent's window onto the member write-access
// queue it is expected to decide. The surface is leader-only: a member never
// receives it, so no member can answer its own escalation.
type leaderApprovalTool struct {
	name     string
	desc     string
	schema   json.RawMessage
	svc      *writeAccessEscalations
	teamName string
}

func (t *leaderApprovalTool) Name() string            { return t.name }
func (t *leaderApprovalTool) Description() string     { return t.desc }
func (t *leaderApprovalTool) Schema() json.RawMessage { return t.schema }

// TeamLifecycleStateWriter marks the surface as writing team authorization
// state rather than the workspace, so the plan and read-only boundaries keep it
// reachable — a blocked member needs this answered.
func (t *leaderApprovalTool) TeamLifecycleStateWriter() bool { return true }

func (t *leaderApprovalTool) ReadOnly() bool     { return t.name == leaderListApprovalsName }
func (t *leaderApprovalTool) PlanModeSafe() bool { return t.ReadOnly() }

// newLeaderApprovalTools returns nil for anything but a leader with a live
// escalation service, which is what keeps the queue leader-owned.
func newLeaderApprovalTools(svc *writeAccessEscalations, teamName, memberID string, leader bool) []tool.Tool {
	if svc == nil || !leader || strings.TrimSpace(teamName) == "" || strings.TrimSpace(memberID) == "" {
		return nil
	}
	base := func(name, desc, schema string) *leaderApprovalTool {
		return &leaderApprovalTool{name: name, desc: desc, schema: json.RawMessage(schema), svc: svc, teamName: teamName}
	}
	return []tool.Tool{
		base(leaderListApprovalsName,
			"List the write-access requests team members are blocked on right now: each entry carries the request_id to answer, the member, the tool, and the directories it wants. Read-only.",
			`{"type":"object","properties":{"member_name":{"type":"string"}},"additionalProperties":false}`),
		base(leaderResolveApprovalName,
			"Decide one member's blocked write-access request. scope \"once\" grants this single call; \"session\" also covers later writes to the same directories for that member, which is what stops a member asking again. Persisting the grant into the project config is not available here — ask the operator if that is needed.",
			`{"type":"object","properties":{"request_id":{"type":"string"},"allow":{"type":"boolean"},"scope":{"type":"string","enum":["once","session"]}},"required":["request_id","allow"],"additionalProperties":false}`),
	}
}

func (t *leaderApprovalTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		MemberName string `json:"member_name"`
		RequestID  string `json:"request_id"`
		Allow      bool   `json:"allow"`
		Scope      string `json:"scope"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", t.name, err)
	}
	switch t.name {
	case leaderListApprovalsName:
		return t.list(p.MemberName), nil
	case leaderResolveApprovalName:
		scope := sandbox.ApprovalScope(strings.TrimSpace(p.Scope))
		if scope == "" {
			scope = sandbox.ApprovalScopeOnce
		}
		return t.svc.resolve(t.teamName, strings.TrimSpace(p.RequestID), p.Allow, scope)
	}
	return "", fmt.Errorf("%s: unsupported call", t.name)
}

// list renders the queue the leader is expected to clear. An empty queue is a
// plain answer, not an error: the leader polls this between other work.
func (t *leaderApprovalTool) list(memberFilter string) string {
	pending := t.svc.list(t.teamName)
	filter := strings.TrimSpace(memberFilter)
	var b strings.Builder
	for _, e := range pending {
		if filter != "" && e.Member != filter {
			continue
		}
		fmt.Fprintf(&b, "%s | member %s | tool %s | dirs %s | waiting %s",
			e.RequestID, e.Member, e.Tool, strings.Join(e.Dirs, ", "), time.Since(e.Created).Round(time.Second))
		if e.BroadHome {
			b.WriteString(" | WARNING: broad home access")
		}
		if e.OrdinaryNeeded {
			b.WriteString(" | also needs an ordinary permission grant")
		}
		if e.Justification != "" {
			b.WriteString(" | why: " + e.Justification)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "no member is waiting on an authorization request"
	}
	return strings.TrimRight(b.String(), "\n")
}
