package boot

// The workspace write lease is the one cross-process resource every session
// shares. A writer queued behind another used to look exactly like one working:
// the notice named neither the scope nor the writer. Now it names both.

import (
	"fmt"
	"strings"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/workspacelease"
)

// workspaceLeaseWaitEvent is the notice one blocked writer emits. The lease
// reports what it is queued for — a whole-workspace wait means another session
// is writing the workspace, typically a member's build holding the exclusive
// scope, while a file wait names the file — and, when the holder published an
// identity, who is holding it. Both keep NoticeCodeWorkspaceLease, so existing
// consumers are unaffected; the detail is what distinguishes a blocked writer
// from a working one.
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
	// An identified holder is a member of this team — the only publisher of a
	// holder record — so the notice can say so instead of "another session".
	holder, identified := lease.WaitingOn()
	detail := "workspace write lease is busy"
	if state.Scope == "" || state.Scope == "workspace" {
		// An empty scope is an unclassified wait — a grouped root acquisition,
		// which is a whole-workspace wait by construction.
		detail += ": queued for the whole workspace"
	} else {
		label := state.Label
		if label == "" {
			label = state.Scope
		}
		detail += ": queued for " + label
	}
	if identified {
		ev.Text = "A team member is writing to this workspace; this session will continue automatically when it is safe."
		if held := holderWaitDetail(holder, time.Now()); held != "" {
			detail += held
		}
	}
	ev.Detail = detail + "; read-only work remains concurrent"
	return ev
}

// holderWaitDetail names the holder a queued acquisition observed, in its mode
// and how long it has held. It is empty when the record carried no label, which
// keeps the pre-existing wording.
func holderWaitDetail(holder workspacelease.HolderInfo, now time.Time) string {
	label := strings.TrimSpace(holder.Label)
	if label == "" {
		return ""
	}
	mode := workspacelease.HolderModeExclusive
	if holder.Mode == workspacelease.HolderModeShared {
		mode = workspacelease.HolderModeShared
	}
	detail := "; held by " + label
	if age := leaseHoldAge(holder.Since, now); age != "" {
		detail += " for " + age
	}
	return fmt.Sprintf("%s (%s)", detail, mode)
}

// leaseHoldAge renders how long an identified holder has held the lock. A mark
// under one second reads as "1s" rather than "0s", so a fresh record never
// claims an instantaneous hold.
func leaseHoldAge(since, now time.Time) string {
	if since.IsZero() || !now.After(since) {
		return ""
	}
	elapsed := now.Sub(since)
	if elapsed < time.Second {
		return "1s"
	}
	return elapsed.Round(time.Second).String()
}
