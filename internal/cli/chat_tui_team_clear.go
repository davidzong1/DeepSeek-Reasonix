package cli

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// teamClearKind is the c key's clear-histories confirmation: warn → exact team
// name → clearing. Every stage returns to None on Esc, and a stage that sits
// untyped past the timeout cancels on the next keypress. Nothing is written
// before the name stage confirms, so a half-armed clear cannot delete a
// history.
type teamClearKind int

const (
	teamClearNone teamClearKind = iota
	teamClearWarn               // team-scope warning; nothing written yet
	teamClearName               // exact team name, typed
	teamClearDone               // finished: result or failure shown until enter/esc
)

// teamClearState is the c confirmation: the active stage, the typed team-name
// buffer, the refusal or failure message, and the stage entry time.
type teamClearState struct {
	kind    teamClearKind
	buf     string
	errMsg  string
	targets int // member histories the confirmed clear reached, for the result line
	entered time.Time
}

// startTeamClear arms the c clear-histories confirmation on the focused member,
// gated on the leader property read from the registry — a non-leader is
// refused, and c never navigates. The warning stage renders first, so the
// team-wide scope is stated before anything can be typed.
func (p *teamPicker) startTeamClear() {
	member, ok := p.model.Focused()
	if !ok {
		return
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return
	}
	if !slot.IsLeader() {
		p.errMsg = "Only the leader can clear team histories"
		return
	}
	p.errMsg = ""
	p.teamClear = teamClearState{kind: teamClearWarn, entered: time.Now()}
}

// handleTeamClearKey routes a keypress inside the clear-histories confirmation
// and reports whether it consumed the key. Esc cancels at any stage with zero
// writes; the name stage demands the team name exactly, so a slip of the
// keyboard cannot arm a fleet-wide delete; the confirmed stage runs the clear.
// A stage past the timeout cancels first, and the keypress then flows on to the
// normal handler.
func handleTeamClearKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	c := &p.teamClear
	if time.Since(c.entered) > leaderResetTimeout {
		c.kind, c.buf, c.errMsg = teamClearNone, "", ""
		return false // the keypress is a fresh one on an idle overlay
	}
	switch c.kind {
	case teamClearWarn:
		switch msg.String() {
		case "enter":
			c.kind, c.buf, c.entered = teamClearName, "", time.Now()
		case "esc", "q", "ctrl+c":
			c.kind, c.buf, c.errMsg = teamClearNone, "", ""
		}
	case teamClearName:
		switch msg.String() {
		case "enter":
			if strings.TrimSpace(c.buf) == p.model.Name() {
				p.executeTeamClear()
			} else {
				c.errMsg = "Team name does not match — Esc to cancel"
				c.buf = ""
			}
		case "esc", "ctrl+c":
			c.kind, c.buf, c.errMsg = teamClearNone, "", ""
		case "backspace":
			if c.buf != "" {
				c.buf = strings.TrimSuffix(c.buf, lastRune(c.buf))
			}
		default:
			if msg.String() == "space" {
				c.buf += " "
			} else if printableKey(msg.String()) {
				c.buf += msg.String()
			}
		}
	case teamClearDone:
		switch msg.String() {
		case "enter", "esc", "q", "ctrl+c":
			c.kind, c.buf, c.errMsg = teamClearNone, "", ""
		}
	}
	return true
}

// executeTeamClear runs the confirmed clear. It re-reads the leader property
// from the registry — the flag may have moved since the UI armed — stops the
// team's assembled backends so nothing is writing while the histories go, and
// only then delegates the destructive half to clearTeamHistories: the one path
// k also clears through, so the two keys cannot drift on what "cleared" means.
// A failure lands in the confirmation instead of closing it — a partial clear
// announced as success is the outcome this exists to prevent.
func (p *teamPicker) executeTeamClear() {
	c := &p.teamClear
	teamName := p.model.Name()
	member, ok := p.model.Focused()
	if !ok {
		c.kind, c.errMsg = teamClearDone, "No member focused — the clear was not run"
		return
	}
	if slot, ok := p.slotOf(member.ID); !ok || !slot.IsLeader() {
		c.kind, c.errMsg = teamClearDone, "Only the leader can clear team histories"
		return
	}
	c.targets = p.clearHistoryTargets(teamName)
	if err := p.stopTeamBackends(teamName); err != nil {
		c.kind, c.errMsg = teamClearDone, err.Error()
		return
	}
	if err := p.clearTeamHistories(teamName); err != nil {
		c.kind, c.errMsg = teamClearDone, err.Error()
		return
	}
	if err := p.reload(""); err != nil {
		c.kind, c.errMsg = teamClearDone, err.Error()
		return
	}
	c.kind, c.buf, c.errMsg = teamClearDone, "", ""
}

// clearHistoryTargets is how many member histories the clear reaches: the union
// of the team's canonical owner directories and its roster session files. The
// count names what the operator is about to lose rather than one storage half,
// and it is read before the clear — afterwards both halves are gone.
func (p *teamPicker) clearHistoryTargets(teamName string) int {
	ids := map[string]bool{}
	if p.owners != nil {
		if ownerIDs, err := p.owners.MemberIDs(teamName); err == nil {
			for _, id := range ownerIDs {
				ids[id] = true
			}
		}
	}
	if p.store != nil {
		if bindings, err := p.store.Bindings(teamName); err == nil {
			for _, b := range bindings {
				ids[b.MemberID] = true
			}
		}
	}
	return len(ids)
}

// renderTeamClear renders the c clear-histories confirmation: the team-scope
// warning, the exact-name stage, or the finished result. The team named is the
// focused one from the registry, never the typed buffer.
func (p *teamPicker) renderTeamClear(w int) string {
	var b strings.Builder
	c := &p.teamClear
	teamName := p.model.Name()
	b.WriteString(accent(teamName+" · clear histories") + "\n")
	switch c.kind {
	case teamClearWarn:
		b.WriteString(dim("  This deletes every member's history in this team — the leader's") + "\n")
		b.WriteString(dim("  included: the canonical owner transcripts and the legacy session files.") + "\n")
		b.WriteString(dim("  Other teams and the chat's own session are untouched; the roster and") + "\n")
		b.WriteString(dim("  the leader marker stay as they are.") + "\n")
	case teamClearName:
		b.WriteString(dim("  Type the exact team name to confirm: ") + accent(teamName) + "\n")
		b.WriteString("  " + c.buf + "▏" + "\n")
	case teamClearDone:
		if c.errMsg == "" {
			b.WriteString(dim(fmt.Sprintf("  Cleared %d member histories of ", c.targets)) + accent(teamName) + dim(".") + "\n")
		}
	}
	if c.errMsg != "" {
		b.WriteString(c.errMsg + "\n")
	}
	switch c.kind {
	case teamClearWarn:
		b.WriteString(dim("Enter continue · Esc cancel"))
	case teamClearName:
		b.WriteString(dim("Enter confirm · Esc cancel"))
	case teamClearDone:
		b.WriteString(dim("Enter/Esc close"))
	}
	return choicePanelStyle.Width(w).Render(b.String())
}
