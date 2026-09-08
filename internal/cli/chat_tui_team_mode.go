package cli

import (
	"fmt"
	"strings"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/team"
)

// Member approval modes alias the team-package values, the single source both
// the persisted slot and the runtime read resolve from.
const (
	memberModeAuto   = team.ApprovalModeAuto
	memberModeManual = team.ApprovalModeManual
)

// effectiveMemberMode resolves one member's approval mode from the persisted
// template slot: the leader is always auto and a slot that never opted into
// manual (the empty field) means auto. The decision path reads this per event,
// so a mode change lands as soon as the roster tick reloads the document.
func (p *teamPicker) effectiveMemberMode(memberID string) string {
	if p == nil {
		return memberModeAuto
	}
	if slot, ok := p.slotOf(memberID); ok {
		return slot.EffectiveApprovalMode()
	}
	return memberModeAuto
}

// setMemberMode persists one member's approval mode through the store and
// reloads the document, so the decision path reads the change immediately
// instead of waiting for the next roster tick. The store refuses the leader
// slot and unknown modes; the reported mode is the post-write effective value.
func (p *teamPicker) setMemberMode(memberID, mode string) (string, error) {
	if p == nil || p.store == nil {
		return memberModeAuto, fmt.Errorf("member mode: no team store")
	}
	teamName := p.model.Name()
	if err := p.store.SetMemberApprovalMode(teamName, memberID, mode); err != nil {
		return p.effectiveMemberMode(memberID), err
	}
	if err := p.reload(teamName); err != nil {
		return p.effectiveMemberMode(memberID), err
	}
	return p.effectiveMemberMode(memberID), nil
}

// grantableAutoApproval reports whether an approval event is the ordinary
// tool-permission surface an auto mode may grant unattended. Fresh human
// decisions (memory/plan/sandbox/config) and the specialized decision kinds
// stay on the leader's surface even for auto members, matching the interactive
// Auto posture contract: auto lets the policy fallback through, it never
// answers a decision a human must make.
func grantableAutoApproval(ev event.Event) bool {
	if ev.Approval.Fresh || ev.Approval.Kind != "" {
		return false
	}
	tool := strings.ToLower(strings.TrimSpace(ev.Approval.Tool))
	if strings.Contains(tool, "remember") || strings.Contains(tool, "forget") || strings.Contains(tool, "plan") {
		return false
	}
	return true
}

// liveMemberMode resolves one member's approval mode straight from the store
// document at decision time, so a member's own tool switch applies to the very
// next approval instead of waiting for the roster tick — a switch to manual
// can never race an in-flight approval past the gate. A read failure refuses
// auto-answering: the leader surface decides whatever could not be resolved.
func (p *teamPicker) liveMemberMode(memberID string) string {
	slot, ok := p.slotOf(memberID)
	if !ok {
		return memberModeAuto
	}
	if p.store == nil {
		return slot.EffectiveApprovalMode()
	}
	doc, _, err := p.store.Load()
	if err != nil {
		return memberModeManual
	}
	name := p.model.Name()
	for _, t := range doc.Teams {
		if t.Name != name {
			continue
		}
		for _, s := range t.Template {
			if s.MemberID == memberID {
				return s.EffectiveApprovalMode()
			}
		}
	}
	return slot.EffectiveApprovalMode()
}

// autoGrantMemberApproval answers a background member's ordinary approval
// itself when that member's mode is auto, appending the grant to the team's
// authorization ledger. It reports whether it handled the event; a member with
// no live assembled backend falls back to the leader surface so the prompt is
// never dropped. A ledger failure never rolls an executed grant back — the
// decision stands and the failure surfaces on the roster error line.
func (p *teamPicker) autoGrantMemberApproval(member string, ev event.Event) bool {
	if p == nil || p.hub == nil {
		return false
	}
	if p.liveMemberMode(member) != memberModeAuto || !grantableAutoApproval(ev) {
		return false
	}
	if err := p.hub.Approve(p.session.teamName, member, ev.Approval.ID, true, false, false); err != nil {
		return false
	}
	if p.store != nil {
		if err := p.store.AppendAuthz(p.session.teamName, team.AuthzEntry{
			TS: time.Now().Format(time.RFC3339), Member: member, Source: "auto", Allow: true,
			ID: ev.Approval.ID, Tool: ev.Approval.Tool, Subject: ev.Approval.Subject,
		}); err != nil {
			p.errMsg = pickerErrMsg(err)
		}
	}
	return true
}
