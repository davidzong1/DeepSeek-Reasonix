package team

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Approval modes decide who answers a member's tool-approval prompts: "auto"
// (the default) lets an ordinary approval through immediately; "manual" raises
// every approval to the leader for a one-shot grant. The leader slot is pinned
// to auto and refuses any change.
const (
	ApprovalModeAuto   = "auto"
	ApprovalModeManual = "manual"
)

// Approval-mode write errors. ErrInvalidApprovalMode refuses an unknown value;
// ErrLeaderApprovalModeFixed refuses the pinned leader slot.
var (
	ErrInvalidApprovalMode     = errors.New(`team: approval mode must be "auto" or "manual"`)
	ErrLeaderApprovalModeFixed = errors.New("team: the leader's approval mode is fixed to auto")
)

// EffectiveApprovalMode resolves a slot's approval mode: the leader is always
// auto regardless of stored bytes (a hand-edited document cannot unlock the
// leader), and the empty value — every legacy document, any slot before its
// first write — means auto.
func (s MemberSlot) EffectiveApprovalMode() string {
	if s.IsLeader() || s.ApprovalMode == "" {
		return ApprovalModeAuto
	}
	return s.ApprovalMode
}

// SetMemberApprovalMode persists one member's approval mode through the
// validated CAS setter, refusing the leader slot and unknown modes before any
// write. Auto writes the empty string, so a default member's slot stays
// byte-identical to a pre-mode document: old disks read zero migration, new
// writes only add a byte when a member opts into manual.
func (s *TeamStore) SetMemberApprovalMode(teamName, memberID, mode string) error {
	if mode != ApprovalModeAuto && mode != ApprovalModeManual {
		return ErrInvalidApprovalMode
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		slot, err := memberSlot(doc, i, memberID)
		if err != nil {
			return err
		}
		if slot.IsLeader() {
			return ErrLeaderApprovalModeFixed
		}
		if mode == ApprovalModeAuto {
			slot.ApprovalMode = ""
		} else {
			slot.ApprovalMode = mode
		}
		return nil
	})
}

// AuthzEntry is one recorded authorization decision: which member's approval
// was answered, by whom ("auto" mode grant or the leader), and how. The ledger
// is team-scoped audit state — never a skill or transcript write.
type AuthzEntry struct {
	TS      string `json:"ts"`                // RFC3339 decision time
	Member  string `json:"member"`            // member whose approval was answered
	Source  string `json:"source"`            // "auto" (mode auto-grant) or "leader"
	Allow   bool   `json:"allow"`             // granted or denied
	ID      string `json:"id"`                // the approval id answered
	Tool    string `json:"tool,omitempty"`    // tool under approval
	Subject string `json:"subject,omitempty"` // approval subject line
}

// Ledger read bounds: a query under the default returns the newest
// authzReadPage entries at most, whatever limit the caller asked for.
const authzReadPage = 64

// AppendAuthz appends one decision to the team's authorization ledger
// (.reasonix/team/authz/<team>.jsonl). One O_APPEND write per record is atomic
// against concurrent appends, and a torn tail (crash mid-record) is skipped by
// readers, never misread. The team name goes through the same path-safety gate
// as the session keys, so no key can escape the team data dir.
func (s *TeamStore) AppendAuthz(teamName string, e AuthzEntry) error {
	rel, err := authzLedgerPath(teamName)
	if err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	dir := filepath.Join(s.store.root, AuthzDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.store.root, rel), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// AuthzEntries returns the team's recorded decisions, newest first, bounded to
// the caller's limit with a ceiling of authzReadPage. A team with no ledger —
// no decision ever recorded — is empty, not an error.
func (s *TeamStore) AuthzEntries(teamName string, limit int) ([]AuthzEntry, error) {
	rel, err := authzLedgerPath(teamName)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.store.root, rel))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	if limit <= 0 {
		limit = authzReadPage
	}
	if limit > authzReadPage {
		limit = authzReadPage
	}
	entries := make([]AuthzEntry, 0, limit)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var e AuthzEntry
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue // torn tail from a crash mid-append; never attributable
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// Keep only the newest limit entries, newest first.
	if len(entries) > limit {
		entries = append([]AuthzEntry(nil), entries[len(entries)-limit:]...)
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

// authzLedgerPath returns the project-relative ledger path for a team,
// applying the session key path-safety gate to the team name.
func authzLedgerPath(teamName string) (string, error) {
	if err := validateSessionKey(teamName); err != nil {
		return "", fmt.Errorf("team: authorization ledger: %w", err)
	}
	return filepath.Join(AuthzDir, teamName+".jsonl"), nil
}
