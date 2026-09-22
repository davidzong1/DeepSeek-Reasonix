package boot

// The workspace write lease is the one cross-process resource every Reasonix
// session shares. A session queued behind another writer used to look exactly
// like one that is working: the notice said nothing about what it queued for.

import (
	"reasonix/internal/event"
	"reasonix/internal/workspacelease"
)

// workspaceLeaseWaitEvent is the notice one blocked writer emits. The lease
// reports what it is queued for: a whole-workspace wait means another session is
// writing the workspace — typically a member's build holding the exclusive
// scope — while a file wait names the file it is queued for. Both keep
// NoticeCodeWorkspaceLease, so existing consumers are unaffected; the detail is
// what distinguishes a blocked writer from a working one.
func workspaceLeaseWaitEvent(lease *workspacelease.Owner) event.Event {
	ev := event.Event{
		Kind:   event.Notice,
		Level:  event.LevelInfo,
		Code:   event.NoticeCodeWorkspaceLease,
		Text:   "Another session is writing to this workspace; this session will continue automatically when it is safe.",
		Detail: "workspace write lease is busy; read-only work remains concurrent",
	}
	state := lease.State()
	if !state.Waiting {
		// Not observably queued (the wait already cleared): the generic text is
		// the honest one, and it is what every other consumer expects.
		return ev
	}
	// An empty scope is an unclassified wait — a grouped root acquisition, which
	// is a whole-workspace wait by construction.
	if state.Scope == "" || state.Scope == "workspace" {
		ev.Detail = "workspace write lease is busy: queued for the whole workspace; read-only work remains concurrent"
		return ev
	}
	label := state.Label
	if label == "" {
		label = state.Scope
	}
	ev.Detail = "workspace write lease is busy: queued for " + label + "; read-only work remains concurrent"
	return ev
}
