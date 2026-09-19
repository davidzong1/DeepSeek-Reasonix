package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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

// startLeaderConfirm arms the leader-gated destructive confirmation the roster
// key names: k steps the leader down, c clears the team's histories. Both are
// refused on a non-leader and neither navigates, so the key and the stage
// cannot drift apart.
func (p *teamPicker) startLeaderConfirm(key string) {
	if key == "c" {
		p.startTeamClear()
		return
	}
	p.startLeaderReset()
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
// verify the member is still the leader, then delegates the destructive half
// to stepDownLeader — stop, clear, publish off — and shows the finished
// result. The step-down semantics live in one place, so the k key and any
// other caller cannot drift from each other; a failure keeps the confirmation
// on the error message, and the .trash/atomic semantics live in the domain
// store, never here.
func (p *teamPicker) executeLeaderReset() {
	r := &p.reset
	member, ok := p.model.Focused()
	if !ok {
		r.kind = leaderResetNone
		return
	}
	teamName := p.model.Name()
	if err := p.stepDownLeader(teamName, member.ID); err != nil {
		r.errMsg = err.Error()
		return
	}
	r.kind, r.buf, r.errMsg = leaderResetDone, "", ""
}

// clearTeamHistories removes every member's conversation history for the team.
// A member's history is its own Reasonix session file (D5), so that file is what
// step-down deletes; the legacy context tree is cleared too so a pre-D5 tree left
// on disk does not survive a step-down that promised to remove it.
//
// Canonical owner storage is cleared as well, and first: since the owner
// directory is where a member's transcript and every derived sidecar now live, a
// step-down that only deleted the session-directory copies would leave the real
// histories on disk while telling the user they were removed. Clearing by owner
// key — rather than by session file — is what keeps another team untouched.
func (p *teamPicker) clearTeamHistories(teamName string) error {
	if p.sessions != nil {
		if err := p.sessions.ClearTeamTrash(teamName); err != nil {
			return err
		}
	}
	if p.owners != nil {
		if err := p.clearOwnerHistories(teamName); err != nil {
			return err
		}
	}
	if p.sessionDir == "" || p.store == nil {
		return nil
	}
	bindings, err := p.store.Bindings(teamName)
	if err != nil {
		return err
	}
	for _, b := range bindings {
		path := filepath.Join(p.sessionDir, b.SessionFile)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// clearOwnerHistories stages and sweeps every member's canonical owner directory
// for the team. The member list comes from the owner store's own team directory,
// not from the registry: a slot already removed from team.json still owns a
// directory, and a step-down that promised to remove the team's histories must
// not leave those behind. Every error is returned — a partial clear that reports
// success is exactly the promise this function exists to keep.
func (p *teamPicker) clearOwnerHistories(teamName string) error {
	ids, err := p.owners.MemberIDs(teamName)
	if err != nil {
		return err
	}
	for _, id := range ids {
		key := team.OwnerKey{TeamID: teamName, MemberID: id}
		if _, err := p.owners.Delete(key); err != nil {
			return err
		}
		if err := p.owners.SweepOwnerTrash(key); err != nil {
			return err
		}
	}
	return nil
}
