package cli

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
)

// leaderResetKind is the k key's step-down confirmation stage (§6): warn →
// exact leader id → directory list → clearing. Every stage returns to Idle on
// Esc, and a stage that sits untyped past the timeout cancels on the next
// keypress. Nothing is written before the final confirm.
type leaderResetKind int

const (
	leaderResetNone leaderResetKind = iota
	leaderResetWarn                 // warning that contexts will be cleared
	leaderResetID                   // exact leader member id, typed
	leaderResetList                 // directory list, enter confirms the clear
	leaderResetDone                 // finished: result shown until enter/esc
)

// leaderResetTimeout cancels a stale confirmation stage: any keypress after
// the timeout reads as a cancel, so a half-armed reset cannot linger.
const leaderResetTimeout = 30 * time.Second

// leaderResetState is the k step-down confirmation: the active stage, the
// typed leader id buffer, and the refusal message.
type leaderResetState struct {
	kind    leaderResetKind
	buf     string
	errMsg  string
	entered time.Time // stage entry; a stale stage cancels on the next key
}

// startLeaderReset arms the step-down confirmation on the focused member,
// gated on the leader property read from the registry — a non-leader is
// refused, and k never navigates. The warning stage renders first; nothing is
// written until the final confirm.
func (p *teamPicker) startLeaderReset() {
	member, ok := p.model.Focused()
	if !ok {
		return
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return
	}
	if !slot.IsLeader() {
		p.errMsg = "Only the leader can step down"
		return
	}
	p.errMsg = ""
	p.reset = leaderResetState{kind: leaderResetWarn, entered: time.Now()}
}

// handleLeaderResetKey routes a keypress inside the step-down confirmation and
// reports whether it consumed the key. Esc cancels at any stage with zero
// writes; the id stage demands an exact match before the list stage; the list
// stage's enter runs the clear. A stage past the timeout cancels first, and
// the keypress then flows on to the normal handler.
func handleLeaderResetKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	r := &p.reset
	if time.Since(r.entered) > leaderResetTimeout {
		r.kind, r.buf, r.errMsg = leaderResetNone, "", ""
		return false // the keypress is a fresh one on an idle overlay
	}
	switch r.kind {
	case leaderResetWarn:
		switch msg.String() {
		case "enter":
			r.kind, r.buf, r.entered = leaderResetID, "", time.Now()
		case "esc", "q", "ctrl+c":
			r.kind, r.buf, r.errMsg = leaderResetNone, "", ""
		}
	case leaderResetID:
		switch msg.String() {
		case "enter":
			if strings.TrimSpace(r.buf) == p.resetTargetID() {
				r.kind, r.buf, r.errMsg, r.entered = leaderResetList, "", "", time.Now()
			} else {
				r.errMsg = "Member id does not match the leader — Esc to cancel"
				r.buf = ""
			}
		case "esc", "ctrl+c":
			r.kind, r.buf, r.errMsg = leaderResetNone, "", ""
		case "backspace":
			if r.buf != "" {
				r.buf = strings.TrimSuffix(r.buf, lastRune(r.buf))
			}
		default:
			if msg.String() == "space" {
				r.buf += " "
			} else if printableKey(msg.String()) {
				r.buf += msg.String()
			}
		}
	case leaderResetList:
		switch msg.String() {
		case "enter":
			p.executeLeaderReset()
		case "esc", "q", "ctrl+c":
			r.kind, r.buf, r.errMsg = leaderResetNone, "", ""
		}
	case leaderResetDone:
		switch msg.String() {
		case "enter", "esc", "q", "ctrl+c":
			r.kind, r.buf, r.errMsg = leaderResetNone, "", ""
		}
	}
	return true
}

// resetTargetID is the leader id the confirmation acts on: the focused member
// as the overlay still holds it (the confirmation blocks navigation).
func (p *teamPicker) resetTargetID() string {
	if member, ok := p.model.Focused(); ok {
		return member.ID
	}
	return ""
}

// resetDirCount is the number of member context directories the clear will
// remove, from the session store when usable, else the roster size.
func (p *teamPicker) resetDirCount(teamName string) int {
	if p.sessions != nil {
		if dirs, err := p.sessions.MemberDirs(teamName); err == nil {
			return len(dirs)
		}
	}
	return len(p.model.Members())
}

// executeLeaderReset runs the final confirm: it re-reads the registry to
// verify the member is still the leader, clears the team's member contexts
// through the session store's trash staging (§6.6 — the root is renamed into
// a timestamped .trash dir before deletion, so a failed clear preserves it
// and repeats idempotently), publishes the leader flag off, and drops the
// persisted session selection. A failure keeps the confirmation on the error
// message; the .trash/atomic semantics live in the domain store, never here.
func (p *teamPicker) executeLeaderReset() {
	r := &p.reset
	member, ok := p.model.Focused()
	if !ok {
		r.kind = leaderResetNone
		return
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		r.kind = leaderResetNone
		return
	}
	if !slot.IsLeader() { // verify: the leader property may have changed mid-confirm
		r.errMsg = "The member is no longer the leader — Esc to cancel"
		return
	}
	teamName := p.model.Name()
	if p.sessions != nil {
		if err := p.sessions.ClearTeamTrash(teamName); err != nil {
			r.errMsg = "Failed to clear team contexts: " + err.Error()
			return
		}
	}
	if err := p.store.SetMemberLeader(teamName, member.ID, false); err != nil {
		r.errMsg = pickerErrMsg(err)
		return
	}
	if p.sessions != nil {
		_ = p.sessions.WriteSelection(teamName, team.SessionSelection{Team: teamName})
	}
	if err := p.reload(""); err != nil {
		r.errMsg = pickerErrMsg(err)
		return
	}
	r.kind, r.buf, r.errMsg = leaderResetDone, "", ""
}
