package cli

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/team"
)

// fixtureStateRoot returns the state root writeTeamFixture seeded this test's
// environment with (writeTeamDoc owns the env when it runs first).
func fixtureStateRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("REASONIX_STATE_HOME")
	if root == "" {
		t.Fatal("fixture state root is not set")
	}
	return root
}

// stateSnapshot maps every file under root to its size, so a later snapshot
// can prove an operation wrote nothing outside a whitelist.
func stateSnapshot(t *testing.T, root string) map[string]int64 {
	t.Helper()
	got := map[string]int64{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		got[rel] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// ledgerEntries reads the overlay store's recorded authorization decisions for
// the fixture team.
func ledgerEntries(t *testing.T, m chatTUI) []team.AuthzEntry {
	t.Helper()
	entries, err := m.teamPick.store.AuthzEntries("alpha", 0)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestMemberModesDefaultAutoAndLeaderPinned pins the persisted defaults: every
// member starts auto (the empty slot field), the leader refuses any mode
// change with auto left intact, a member's manual override persists to the
// document, and switching back clears the field again.
func TestMemberModesDefaultAutoAndLeaderPinned(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	if got := m.teamPick.effectiveMemberMode("alice"); got != memberModeAuto {
		t.Fatalf("member default mode = %q, want auto", got)
	}
	if got := m.teamPick.effectiveMemberMode("lead"); got != memberModeAuto {
		t.Fatalf("leader default mode = %q, want auto", got)
	}
	if mode, err := m.teamPick.setMemberMode("lead", memberModeManual); err == nil || mode != memberModeAuto {
		t.Fatalf("leader mode change must refuse: mode=%q err=%v", mode, err)
	}
	if mode, err := m.teamPick.setMemberMode("alice", memberModeManual); err != nil || mode != memberModeManual {
		t.Fatalf("member manual override: mode=%q err=%v", mode, err)
	}
	doc, _, err := m.teamPick.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range doc.Teams[0].Template {
		if slot.MemberID == "alice" && slot.ApprovalMode != team.ApprovalModeManual {
			t.Fatalf("manual override must persist to the document, slot = %+v", slot)
		}
		if slot.MemberID == "lead" && slot.ApprovalMode != "" {
			t.Fatalf("refused leader write must not persist, slot = %+v", slot)
		}
	}
	if mode, err := m.teamPick.setMemberMode("alice", memberModeAuto); err != nil || mode != memberModeAuto {
		t.Fatalf("member switch back to auto: mode=%q err=%v", mode, err)
	}
	if _, err := m.teamPick.setMemberMode("alice", "yolo"); err == nil {
		t.Fatal("unknown mode must be refused")
	}
	doc, _, err = m.teamPick.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range doc.Teams[0].Template {
		if slot.MemberID == "alice" && slot.ApprovalMode != "" {
			t.Fatalf("auto must clear the persisted field, slot = %+v", slot)
		}
	}
}

// TestAutoMemberBackgroundApprovalAutoGrantedAndAudited pins the auto path: a
// background auto member's ordinary approval is granted once through the hub
// without entering the inbox, and the grant lands in the team's authorization
// ledger. Fresh human decisions still surface for the leader.
func TestAutoMemberBackgroundApprovalAutoGrantedAndAudited(t *testing.T) {
	approves := 0
	m := promptTestTUI(t, &approves)

	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1", Tool: "bash", Subject: "run tests"}}})
	if approves != 1 {
		t.Fatalf("auto grant routed = %d, want 1", approves)
	}
	if _, ok := m.teamPick.session.prompts["alice"]; ok {
		t.Fatal("auto grant must not record a leader prompt")
	}
	log := ledgerEntries(t, m)
	if len(log) != 1 || log[0].Member != "alice" || log[0].Source != "auto" || !log[0].Allow || log[0].ID != "a1" || log[0].Tool != "bash" || log[0].Subject != "run tests" {
		t.Fatalf("auto grant ledger = %+v, want one alice/auto/allow a1", log)
	}

	// A fresh human decision (plan approval) is not auto-grantable: it surfaces.
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a2", Tool: "bash", Fresh: true}}})
	if approves != 1 {
		t.Fatalf("fresh approval must not auto-grant, routed = %d", approves)
	}
	if got := m.teamPick.session.prompts["alice"]; got.kind != promptApproval || got.id != "a2" {
		t.Fatalf("fresh approval must surface, prompt = %+v", got)
	}
}

// TestMemberToolModeSwitchAppliesToNextApproval pins the race boundary: a mode
// switch written through the store (the member tool's path — the picker's
// cached document has not reloaded yet) still gates the very next approval, so
// a switch to manual can never race an in-flight approval past the auto gate.
func TestMemberToolModeSwitchAppliesToNextApproval(t *testing.T) {
	approves := 0
	m := promptTestTUI(t, &approves)
	if err := m.teamPick.store.SetMemberApprovalMode("alpha", "alice", team.ApprovalModeManual); err != nil {
		t.Fatal(err)
	}
	// No picker reload happened above: the cached slot still reads auto, which
	// is exactly the state a member's own tool write leaves behind mid-tick.
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1", Tool: "bash"}}})
	if approves != 0 {
		t.Fatalf("approval after a store-side manual switch must surface, routed = %d", approves)
	}
	if got := m.teamPick.session.prompts["alice"]; got.id != "a1" {
		t.Fatalf("manual switch must surface the next approval, prompt = %+v", got)
	}
}

// TestManualMemberApprovalSurfacesAndLeaderGrantIsAudited pins the manual path
// and the one-shot grant audit: the leader's ctrl+a answer routes allow_once to
// the member's backend and records a leader-source ledger entry; ctrl+x denial
// records a deny entry.
func TestManualMemberApprovalSurfacesAndLeaderGrantIsAudited(t *testing.T) {
	approves := 0
	m := promptTestTUI(t, &approves)
	if _, err := m.teamPick.setMemberMode("alice", memberModeManual); err != nil {
		t.Fatal(err)
	}
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1", Tool: "bash", Subject: "run tests"}}})
	if _, ok := m.teamPick.session.prompts["alice"]; !ok {
		t.Fatal("manual approval must surface to the leader")
	}

	next, _, consumed := m.handleTeamKey(promptApproveKey)
	m = next.(chatTUI)
	if !consumed || approves != 1 {
		t.Fatalf("leader grant: consumed=%v routed=%d, want 1", consumed, approves)
	}
	log := ledgerEntries(t, m)
	if len(log) != 1 || log[0].Member != "alice" || log[0].Source != "leader" || !log[0].Allow || log[0].ID != "a1" || log[0].Tool != "bash" || log[0].Subject != "run tests" {
		t.Fatalf("leader grant ledger = %+v, want one alice/leader/allow a1", log)
	}

	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a2", Tool: "read_file"}}})
	if _, _, consumed := m.handleTeamKey(promptDenyKey); !consumed {
		t.Fatal("ctrl+x must consume a pending manual approval")
	}
	log = ledgerEntries(t, m)
	if len(log) != 2 || log[0].Allow || log[0].ID != "a2" || log[0].Source != "leader" || log[0].Tool != "read_file" {
		t.Fatalf("deny ledger = %+v, want a newest leader deny of a2", log)
	}
}

// TestAuthorizationPathWritesOnlyTeamState pins the write boundary of the whole
// authorization seam: decisions (auto grant, deny, leader grant) append only to
// the team's authorization ledger and a mode switch rewrites only team.json —
// everything else under the state root, skills included, stays byte-identical.
func TestAuthorizationPathWritesOnlyTeamState(t *testing.T) {
	approves := 0
	m := promptTestTUI(t, &approves)
	root := fixtureStateRoot(t)

	before := stateSnapshot(t, root)
	// Auto-grant (alice is auto by default), a manual override, then a manual
	// deny and a manual grant across both surfaces.
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1", Tool: "bash"}}})
	if _, err := m.teamPick.setMemberMode("alice", memberModeManual); err != nil {
		t.Fatal(err)
	}
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a2", Tool: "read_file", Subject: "notes.md"}}})
	if _, _, consumed := m.handleTeamKey(promptDenyKey); !consumed {
		t.Fatal("ctrl+x must consume the pending approval")
	}
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a3", Tool: "bash"}}})
	if _, _, consumed := m.handleTeamKey(promptApproveKey); !consumed {
		t.Fatal("ctrl+a must consume the pending approval")
	}
	if approves != 3 {
		t.Fatalf("approve routed = %d, want the auto grant, deny and one leader grant", approves)
	}
	allowed := map[string]bool{
		"team/team.json":          true, // the manual override
		"team/authz/alpha.jsonl":  true, // every decision
		"team/session/alpha.json": true, // selection writes are the overlay's own
	}
	after := stateSnapshot(t, root)
	for path, size := range before {
		if after[path] == size || allowed[path] {
			continue
		}
		t.Fatalf("authorization path modified %s under the state root", path)
	}
	for path := range after {
		if _, ok := before[path]; !ok && !allowed[path] {
			t.Fatalf("authorization path created %s under the state root", path)
		}
		if allowed[path] && after[path] == 0 {
			t.Fatalf("expected writes to %s never happened", path)
		}
	}
}
