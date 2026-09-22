package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"reasonix/internal/event"
)

// Team members are separate agents sharing one workspace, so their write tool
// calls race the same cross-process lease. A write intent gate queues them in
// process first: a performance token only, never a safety boundary.

// WriteIntent is one tool call's intended write scope: the absolute paths it can
// touch, or the whole workspace when the call cannot name them.
type WriteIntent struct {
	Paths []string
	// Whole marks a call that may write anywhere (bash, an unproven hook).
	Whole bool
	// Label renders that scope for a peer's wait report.
	Label string
}

// WriteIntentWait names the in-process peer a write intent queued behind, so the
// blocked session can say what it is waiting for instead of only waiting.
type WriteIntentWait struct {
	Holder string
	Scope  string
}

// WriteIntentGateFunc queues one write intent against in-process peers. onWait is
// called at most once, synchronously at enqueue, so a wait is observable while it
// happens; a gate that errors degrades to no token.
type WriteIntentGateFunc func(ctx context.Context, intent WriteIntent, onWait func(WriteIntentWait)) (release func(), err error)

// acquireWriteIntent takes the in-process token if one is configured, and
// reports the wait on the session's own stream when it queues behind a peer.
func (a *Agent) acquireWriteIntent(ctx context.Context, intent WriteIntent) func() {
	if a == nil || a.svc.writeIntentGate == nil {
		return func() {}
	}
	release, err := a.svc.writeIntentGate(ctx, intent, a.noteWriteIntentWait)
	if err != nil || release == nil {
		return func() {}
	}
	return release
}

// noteWriteIntentWait emits the in-process wait as a notice: a member queued
// behind a teammate otherwise looks like one working. The code stays
// NoticeCodeWorkspaceLease, so existing consumers see the same event family.
func (a *Agent) noteWriteIntentWait(wait WriteIntentWait) {
	if a == nil || a.svc.sink == nil {
		return
	}
	holder := strings.TrimSpace(wait.Holder)
	if holder == "" {
		holder = "a teammate"
	}
	scope := strings.TrimSpace(wait.Scope)
	if scope == "" {
		scope = "the workspace"
	}
	a.svc.sink.Emit(event.Event{
		Kind:   event.Notice,
		Level:  event.LevelInfo,
		Code:   event.NoticeCodeWorkspaceLease,
		Text:   fmt.Sprintf("Waiting for %s to finish writing %s; this session continues automatically when it is safe.", holder, scope),
		Detail: fmt.Sprintf("queued in the team's write token behind %s (%s); read-only work remains concurrent", holder, scope),
	})
}

// writeIntentLabel renders one call's scope, workspace-relative where the path
// permits, for the wait report a queued peer reads.
func writeIntentLabel(workspaceRoot string, scope []string, whole bool) string {
	if whole {
		return "the whole workspace"
	}
	if len(scope) == 0 {
		return "the workspace"
	}
	label := relativeWriteLabel(workspaceRoot, scope[0])
	if len(scope) > 1 {
		label += fmt.Sprintf(" (+%d more)", len(scope)-1)
	}
	return label
}

// relativeWriteLabel shortens one path against the workspace root when it is
// inside it; outside paths are already the honest answer.
func relativeWriteLabel(root, path string) string {
	if strings.TrimSpace(path) == "" {
		return "the workspace"
	}
	if !pathWithinFold(root, path) {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}
