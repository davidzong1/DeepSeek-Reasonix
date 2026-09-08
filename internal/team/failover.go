package team

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// MemberFailoverFile is one member's durable agent-pool failover state. It is
// its own file beside the member cursor — never the shared team document — so
// members walking one team pool each keep independent runtime cursors that
// survive restarts and team edits (§P2).
const MemberFailoverFile = "failover.json"

// maxFailoverHistory bounds the persisted switch history; a reset clears it,
// so the cap only trims a member that cycled through many pool entries.
const maxFailoverHistory = 32

// FailoverSwitch records one agent-user switch: when it happened, which entry
// refused the turn (From), which entry took over (To), and the reason.
type FailoverSwitch struct {
	At     time.Time `json:"at"`
	From   string    `json:"from,omitempty"`
	To     string    `json:"to,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// MemberFailover is one member's independent agent-pool failover runtime state:
// the pool entry its backend is currently pointed at (ActiveRef), the entries
// that already hit quota for this member (Saturated), a bounded switch history,
// a Generation counter that rises on every switch so a stale event can never be
// replayed as the current turn, and Exhausted set when no pool entry remains.
// A member without a file walks the pool head; nothing here is ever written to
// the shared team document.
type MemberFailover struct {
	Document
	ActiveRef  string           `json:"active_ref,omitempty"`
	Saturated  []string         `json:"saturated,omitempty"`
	Switches   []FailoverSwitch `json:"switches,omitempty"`
	Generation int              `json:"generation"`
	Exhausted  bool             `json:"exhausted,omitempty"`
}

// ReadMemberFailover returns the member's durable failover state; an absent
// file or member directory is the zero state (pool head is the default).
func (s *TeamSessionStore) ReadMemberFailover(teamName, memberID string) (MemberFailover, error) {
	if err := validateSessionKey(teamName); err != nil {
		return MemberFailover{}, err
	}
	if err := validateSessionKey(memberID); err != nil {
		return MemberFailover{}, err
	}
	var st MemberFailover
	err := s.store.Load(filepath.Join(contextRootDir, teamName, memberID, MemberFailoverFile), &st)
	if errors.Is(err, os.ErrNotExist) {
		return MemberFailover{Document: Document{SchemaVersion: SchemaVersion}}, nil
	}
	if err != nil {
		return MemberFailover{}, err
	}
	return st, nil
}

// WriteMemberFailover persists the member's failover state atomically through
// the store's single chokepoint, so a crash never leaves a torn file behind.
func (s *TeamSessionStore) WriteMemberFailover(teamName, memberID string, st MemberFailover) error {
	if err := validateSessionKey(teamName); err != nil {
		return err
	}
	if err := validateSessionKey(memberID); err != nil {
		return err
	}
	dir, err := s.memberDir(teamName, memberID)
	if err != nil {
		return err
	}
	st.Document = Document{SchemaVersion: SchemaVersion}
	return s.store.Save(filepath.Join(dir, MemberFailoverFile), &st)
}

// ClearMemberFailover removes the member's failover state; an absent file is a
// no-op. The g reset uses it so the member walks the pool head again.
func (s *TeamSessionStore) ClearMemberFailover(teamName, memberID string) error {
	if err := validateSessionKey(teamName); err != nil {
		return err
	}
	if err := validateSessionKey(memberID); err != nil {
		return err
	}
	full, err := safePath(s.store.root, filepath.Join(contextRootDir, teamName, memberID, MemberFailoverFile))
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// NextPoolRef returns the next usable entry of the ordered pool after active:
// the pool is walked from active's position, wrapping once, skipping active
// itself and every saturated entry. The second result is false when the pool is
// empty or every remaining entry is saturated.
func NextPoolRef(pool, saturated []string, active string) (string, bool) {
	if len(pool) == 0 {
		return "", false
	}
	start := 0
	if i := slices.Index(pool, active); i >= 0 {
		start = i + 1
	}
	for seen := 0; seen < len(pool); seen++ {
		ref := pool[(start+seen)%len(pool)]
		if ref == active || slices.Contains(saturated, ref) {
			continue
		}
		return ref, true
	}
	return "", false
}

// AdvanceFailover is the pure transition after one pool entry refused a turn as
// quota-exhausted: the failed entry joins the saturated set, the next usable
// entry becomes active (generation rises, the switch is recorded), or Exhausted
// is set when no usable entry remains.
func AdvanceFailover(st MemberFailover, pool []string, failed string) MemberFailover {
	return AdvanceFailoverAt(st, pool, failed, time.Now().UTC())
}

// AdvanceFailoverAt is AdvanceFailover with an explicit timestamp, for tests.
func AdvanceFailoverAt(st MemberFailover, pool []string, failed string, at time.Time) MemberFailover {
	sat := append([]string(nil), st.Saturated...)
	if failed != "" && !slices.Contains(sat, failed) {
		sat = append(sat, failed)
	}
	st.Saturated = sat
	next, ok := NextPoolRef(pool, sat, failed)
	if !ok {
		st.Exhausted = true
		return st
	}
	switches := append(st.Switches, FailoverSwitch{At: at, From: failed, To: next, Reason: "quota"})
	if len(switches) > maxFailoverHistory {
		switches = append([]FailoverSwitch(nil), switches[len(switches)-maxFailoverHistory:]...)
	}
	st.Switches = switches
	st.ActiveRef = next
	st.Generation++
	st.Exhausted = false
	return st
}
