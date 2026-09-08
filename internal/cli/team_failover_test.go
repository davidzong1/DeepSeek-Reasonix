package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// failoverUsers returns one registry entry per pool id; the backend registry
// fingerprint only needs the entry to exist.
func failoverUsers(ids ...string) []team.AgentUser {
	users := make([]team.AgentUser, 0, len(ids))
	for _, id := range ids {
		users = append(users, team.AgentUser{UserID: id})
	}
	return users
}

// armFailoverOverlay opens a two-member team with the given pool and wires a
// counting stub-backend registry, returning the TUI plus build/close counters.
func armFailoverOverlay(t *testing.T, pool []string) (chatTUI, *int, *int) {
	t.Helper()
	fixture := team.Team{Name: "alpha", AgentUserPool: pool, Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		{MemberID: "alice", Role: team.RoleTester, Status: team.MemberStatusActive},
	}}
	writeTeamPoolFixture(t, []team.Team{fixture}, failoverUsers(pool...))
	m := openTeamOverlay(t)
	builds, closed := 0, 0
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return stubBackend{label: b.AgentUserRef, closed: &closed}, nil
	}, 4)
	m.teamBackends.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: m.teamPick.store}))
	m.teamPick.backends = m.teamBackends
	return sized(t, m), &builds, &closed
}

func quota402() *provider.APIError {
	return &provider.APIError{Provider: "deepseek", Status: 402, Body: "余额不足"}
}

// TestFailoverQuotaAdvancesToNextPoolEntry pins the P2 core: a bound member
// whose turn ended in an HTTP 402 quota refusal is switched to the next usable
// pool entry on its own session file, the old backend is retired, and the
// durable state records the move.
func TestFailoverQuotaAdvancesToNextPoolEntry(t *testing.T) {
	m, builds, closed := armFailoverOverlay(t, []string{"au-1", "au-2"})
	m.switchTeamMember("lead")
	if *builds != 1 || m.ctrl.Label() != "au-1" {
		t.Fatalf("initial bind = %d backend %s, want 1 on au-1", *builds, m.ctrl.Label())
	}

	m.failoverQuotaTurn("lead", quota402())

	if *builds != 2 {
		t.Errorf("builds = %d, want 2 (failover must rebuild on the next entry)", *builds)
	}
	if *closed != 1 {
		t.Errorf("closed = %d, want 1 (the saturated backend must be retired)", *closed)
	}
	if got := m.ctrl.Label(); got != "au-2" {
		t.Errorf("window backend = %q, want au-2 after failover", got)
	}
	st, err := m.teamPick.sessions.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveRef != "au-2" || st.Generation != 1 || st.Exhausted {
		t.Fatalf("failover state = %+v, want active au-2 gen 1 not exhausted", st)
	}
	if len(st.Saturated) != 1 || st.Saturated[0] != "au-1" {
		t.Fatalf("saturated = %v, want [au-1]", st.Saturated)
	}
	if len(st.Switches) != 1 || st.Switches[0].To != "au-2" || st.Switches[0].Reason != "quota" {
		t.Fatalf("switch history = %+v", st.Switches)
	}
}

// TestFailoverTriggeredOnBoundTurnDone pins the live seam: a TurnDone carrying
// a quota error on the bound member advances the pool, so the failover really
// rides the structured TurnDone.Err rather than a submit-time check.
func TestFailoverTriggeredOnBoundTurnDone(t *testing.T) {
	m, builds, _ := armFailoverOverlay(t, []string{"au-1", "au-2"})
	m.switchTeamMember("lead")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: event.TurnDone, Err: quota402()}})

	if *builds != 2 {
		t.Fatalf("a quota TurnDone must trigger the failover rebuild, builds = %d", *builds)
	}
	st, err := m.teamPick.sessions.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveRef != "au-2" {
		t.Fatalf("TurnDone failover must record the move, active = %q", st.ActiveRef)
	}
	joined := strings.Join(m.transcript, "\n")
	if !strings.Contains(joined, "hit its quota") {
		t.Fatalf("the failover must notice the user, got:\n%s", joined)
	}
}

// TestBackgroundFailoverDoesNotStealWindow verifies that a quota failure from
// a member whose transcript is not currently visible still advances that
// member's backend, while the TUI remains bound to its current member.
func TestBackgroundFailoverDoesNotStealWindow(t *testing.T) {
	m, builds, _ := armFailoverOverlay(t, []string{"au-1", "au-2"})
	m.switchTeamMember("lead")
	alice, err := m.teamPick.store.Binding("alpha", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.teamBackends.bind(alice); err != nil {
		t.Fatal(err)
	}
	visible := m.ctrl.Label()
	m.failoverQuotaTurn("alice", quota402())
	if got := m.ctrl.Label(); got != visible {
		t.Fatalf("background failover stole visible window: got %q, want %q", got, visible)
	}
	if *builds != 3 {
		t.Fatalf("builds = %d, want 3 (lead plus alice before/after failover)", *builds)
	}
	st, err := m.teamPick.sessions.ReadMemberFailover("alpha", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveRef != "au-2" || st.Generation != 1 {
		t.Fatalf("background failover state = %+v, want active au-2 gen 1", st)
	}
}

// TestFailoverQuotaExhaustsPoolAndGatesComposer pins the all-saturated branch:
// a single-entry pool marks the member exhausted, no rebuild is attempted, and
// the composer gate refuses further thinking with an explicit warning.
func TestFailoverQuotaExhaustsPoolAndGatesComposer(t *testing.T) {
	m, builds, _ := armFailoverOverlay(t, []string{"au-1"})
	m.switchTeamMember("lead")
	if *builds != 1 {
		t.Fatalf("initial bind = %d, want 1", *builds)
	}

	m.failoverQuotaTurn("lead", quota402())

	if *builds != 1 {
		t.Errorf("an exhausted pool must not rebuild, builds = %d", *builds)
	}
	st, err := m.teamPick.sessions.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exhausted {
		t.Fatalf("a single saturated entry must exhaust the member, got %+v", st)
	}
	if m.teamMemberPoolBlocked() == "" {
		t.Error("an exhausted member must gate the composer with a warning")
	}
	if m.teamPick.session.errMsg == "" {
		t.Error("the exhaustion warning must reach the session")
	}
	if !strings.Contains(m.teamMemberPoolBlocked(), "press g") {
		t.Errorf("the warning must point at the g reset, got %q", m.teamMemberPoolBlocked())
	}
}

// TestFailoverIgnoresNonQuotaTurn pins the classifier boundary: a plain rate
// limit (429) or network refusal must never advance the pool.
func TestFailoverIgnoresNonQuotaTurn(t *testing.T) {
	m, builds, _ := armFailoverOverlay(t, []string{"au-1", "au-2"})
	m.switchTeamMember("lead")
	m.failoverQuotaTurn("lead", &provider.APIError{Provider: "deepseek", Status: 429, Body: "rate limit"})
	m.failoverQuotaTurn("lead", &provider.APIError{Provider: "deepseek", Status: 429, Body: "Too Many Requests"})
	m.failoverQuotaTurn("lead", nil)
	if *builds != 1 {
		t.Fatalf("non-quota errors must not fail over, builds = %d", *builds)
	}
	st, _ := m.teamPick.sessions.ReadMemberFailover("alpha", "lead")
	if st.ActiveRef != "" || st.Generation != 0 {
		t.Fatalf("non-quota errors must not write failover state, got %+v", st)
	}
}

// TestFailoverIgnoresOutOfPoolPin pins the boundary for an explicitly pinned
// member: an agent-user ref that is not a pool member is an explicit choice and
// is never auto-walked across the team pool.
func TestFailoverIgnoresOutOfPoolPin(t *testing.T) {
	fixture := team.Team{Name: "alpha", AgentUserPool: []string{"au-1", "au-2"}, Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive, AgentUserRef: "au-9"},
		{MemberID: "alice", Role: team.RoleTester, Status: team.MemberStatusActive},
	}}
	users := failoverUsers("au-1", "au-2", "au-9")
	writeTeamPoolFixture(t, []team.Team{fixture}, users)
	m := openTeamOverlay(t)
	builds := 0
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return stubBackend{label: b.AgentUserRef}, nil
	}, 4)
	m.teamBackends.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: m.teamPick.store}))
	m.teamPick.backends = m.teamBackends
	m = sized(t, m)
	m.switchTeamMember("lead")
	if m.ctrl.Label() != "au-9" {
		t.Fatalf("bound = %s, want au-9 (the pinned entry)", m.ctrl.Label())
	}

	m.failoverQuotaTurn("lead", quota402())

	if builds != 1 {
		t.Errorf("an out-of-pool pin must not be failovered, builds = %d", builds)
	}
}

// TestFailoverSurvivesReopen pins restart recovery: after a failover moves the
// member to au-2, switching away and back resolves the binding through the
// durable ActiveRef, so the reopen rebuilds on au-2 rather than the head.
func TestFailoverSurvivesReopen(t *testing.T) {
	m, builds, _ := armFailoverOverlay(t, []string{"au-1", "au-2"})
	m.switchTeamMember("lead")
	m.failoverQuotaTurn("lead", quota402())
	if *builds != 2 {
		t.Fatalf("failover must rebuild, builds = %d", *builds)
	}

	m.switchTeamMember("alice")
	m.switchTeamMember("lead")
	if got := m.ctrl.Label(); got != "au-2" {
		t.Fatalf("reopen must keep the failover active ref au-2, got %q", got)
	}
	if *builds != 3 {
		t.Errorf("reopen must reuse the cached au-2 backend, builds = %d", *builds)
	}
}

// TestRosterGResetsFailoverStateAndRetiresIdleBackend pins the runtime half of
// the g reset: after an exhausted/moved member's failover file is cleared, an
// idle backend on a moved ref is retired so the next bind reassembles on the
// pool head — and only the focused member is touched.
func TestRosterGResetsFailoverStateAndRetiresIdleBackend(t *testing.T) {
	fixture := team.Team{Name: "alpha", AgentUserPool: []string{"au-1", "au-2"}, Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		{MemberID: "bob", Role: team.RoleTester, Status: team.MemberStatusActive},
	}}
	writeTeamPoolFixture(t, []team.Team{fixture}, failoverUsers("au-1", "au-2"))
	m := openRoster(t)
	closed := 0
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.AgentUserRef, closed: &closed}, nil
	}, 4)
	m.teamPick.backends = m.teamBackends
	binding, err := m.teamPick.store.Binding("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.teamBackends.bind(binding); err != nil {
		t.Fatal(err)
	}
	if err := m.teamPick.sessions.WriteMemberFailover("alpha", "lead", team.MemberFailover{
		Document:  team.Document{SchemaVersion: team.SchemaVersion},
		ActiveRef: "au-2", Saturated: []string{"au-1"},
	}); err != nil {
		t.Fatal(err)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 'g'})

	if closed != 1 {
		t.Errorf("closed = %d, want 1 (the moved idle backend must be retired)", closed)
	}
	st, err := m.teamPick.sessions.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveRef != "" || st.Exhausted || len(st.Saturated) != 0 {
		t.Fatalf("g must clear the failover state, got %+v", st)
	}
	// The member had no override, so g's P1 half writes nothing: pool head au-1
	// is already the nominal ref and the doc stays untouched.
	doc := readStoredTeamDoc(t)
	if len(doc.Teams[0].Template) != 2 || doc.Teams[0].Template[1].MemberID != "bob" {
		t.Fatalf("g must not disturb other members: %+v", doc.Teams[0].Template)
	}
}

// armCustomFailoverOverlay opens a team whose leader walks its own custom pool
// [au-2 au-3] while the team pool stays [au-1] — the no-merge fixture: quota
// must advance inside the member's own chain and never dial the team entry.
func armCustomFailoverOverlay(t *testing.T) (chatTUI, *int, *int) {
	t.Helper()
	fixture := team.Team{Name: "alpha", AgentUserPool: []string{"au-1"}, Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive,
			PoolMode: team.MemberPoolCustom, AgentUserPool: []string{"au-2", "au-3"}},
		{MemberID: "alice", Role: team.RoleTester, Status: team.MemberStatusActive},
	}}
	writeTeamPoolFixture(t, []team.Team{fixture}, failoverUsers("au-1", "au-2", "au-3"))
	m := openTeamOverlay(t)
	builds, closed := 0, 0
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return stubBackend{label: b.AgentUserRef, closed: &closed}, nil
	}, 4)
	m.teamBackends.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: m.teamPick.store}))
	m.teamPick.backends = m.teamBackends
	return sized(t, m), &builds, &closed
}

// TestFailoverCustomPoolWalksMemberChainAndNeverMerges pins the P3 failover
// fold: a custom member dials its own pool head, quota advances inside that
// chain only, and exhausting it stops — the team pool's au-1 is never dialed
// nor saturated, and the inheriting alice stays untouched.
func TestFailoverCustomPoolWalksMemberChainAndNeverMerges(t *testing.T) {
	m, builds, closed := armCustomFailoverOverlay(t)
	m.switchTeamMember("lead")
	if m.ctrl.Label() != "au-2" {
		t.Fatalf("custom member initial bind = %s, want its own head au-2", m.ctrl.Label())
	}

	m.failoverQuotaTurn("lead", quota402())
	if m.ctrl.Label() != "au-3" {
		t.Fatalf("custom failover = %s, want au-3", m.ctrl.Label())
	}
	m.failoverQuotaTurn("lead", quota402()) // chain exhausted: au-2 and au-3 spent
	if m.ctrl.Label() != "au-3" {
		t.Fatalf("an exhausted chain must keep serving the last entry, got %s", m.ctrl.Label())
	}
	if *builds != 2 {
		t.Fatalf("builds = %d, want 2 — the team pool's au-1 must never be dialed", *builds)
	}
	if *closed != 1 {
		t.Fatalf("closed = %d, want 1 (au-2 retired on the switch)", *closed)
	}
	st, err := m.teamPick.sessions.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exhausted || st.ActiveRef != "au-3" {
		t.Fatalf("failover state = %+v, want exhausted on au-3", st)
	}
	if len(st.Saturated) != 2 || st.Saturated[0] != "au-2" || st.Saturated[1] != "au-3" {
		t.Fatalf("saturated = %v, want exactly [au-2 au-3] — the team pool entry must never join", st.Saturated)
	}
	// Isolation: alice inherits and never saw a switch.
	alice, err := m.teamPick.sessions.ReadMemberFailover("alpha", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if alice.ActiveRef != "" || alice.Exhausted {
		t.Fatalf("alice's failover state must stay untouched, got %+v", alice)
	}
	if b, err := m.teamPick.store.Binding("alpha", "alice"); err != nil || b.AgentUserRef != "au-1" {
		t.Fatalf("alice's binding must stay the team head au-1, got %+v err %v", b, err)
	}
	// The exhausted member gates the composer until a roster g reset.
	if got := m.teamMemberPoolBlocked(); !strings.Contains(got, "g on the roster to reset") {
		t.Fatalf("exhausted custom member should gate the composer with the g hint, got %q", got)
	}
}

// TestFailoverCustomSurvivesReopen pins restart recovery on a custom member:
// the durable ActiveRef resolves against the member's own pool, so a reopen
// rebuilds on au-3, never falling back to the team head au-1.
func TestFailoverCustomSurvivesReopen(t *testing.T) {
	m, builds, _ := armCustomFailoverOverlay(t)
	m.switchTeamMember("lead")
	m.failoverQuotaTurn("lead", quota402())
	if *builds != 2 {
		t.Fatalf("failover must rebuild, builds = %d", *builds)
	}
	m.switchTeamMember("alice")
	m.switchTeamMember("lead")
	if got := m.ctrl.Label(); got != "au-3" {
		t.Fatalf("custom reopen must keep the failover active ref au-3, got %q", got)
	}
	if *builds != 3 {
		t.Errorf("reopen must reuse the cached au-3 backend, builds = %d", *builds)
	}
}

// TestRosterGOnCustomMemberKeepsConfigClearsFailover pins the g boundary on a
// custom member: g rewinds the runtime (failover state cleared, the moved idle
// backend retired) and writes nothing to the team document — the member's pool
// mode and entries survive untouched.
func TestRosterGOnCustomMemberKeepsConfigClearsFailover(t *testing.T) {
	fixture := team.Team{Name: "alpha", AgentUserPool: []string{"au-1"}, Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive,
			PoolMode: team.MemberPoolCustom, AgentUserPool: []string{"au-2", "au-3"}},
	}}
	writeTeamPoolFixture(t, []team.Team{fixture}, failoverUsers("au-1", "au-2", "au-3"))
	m := openRoster(t)
	closed := 0
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.AgentUserRef, closed: &closed}, nil
	}, 4)
	m.teamPick.backends = m.teamBackends
	binding, err := m.teamPick.store.Binding("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.teamBackends.bind(binding); err != nil {
		t.Fatal(err)
	}
	if err := m.teamPick.sessions.WriteMemberFailover("alpha", "lead", team.MemberFailover{
		Document:  team.Document{SchemaVersion: team.SchemaVersion},
		ActiveRef: "au-3", Saturated: []string{"au-2"},
	}); err != nil {
		t.Fatal(err)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 'g'})

	if closed != 1 {
		t.Errorf("closed = %d, want 1 (the moved idle backend must be retired)", closed)
	}
	st, err := m.teamPick.sessions.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveRef != "" || st.Exhausted || len(st.Saturated) != 0 {
		t.Fatalf("g must clear the failover state, got %+v", st)
	}
	doc := readStoredTeamDoc(t)
	slot := doc.Teams[0].Template[0]
	if !slot.IsCustomPool() || len(slot.AgentUserPool) != 2 || slot.AgentUserPool[0] != "au-2" {
		t.Fatalf("g must not touch a custom member's pool config, slot %+v", slot)
	}
}

// TestCustomLeaderSessionRefusedWithoutTeamPool pins the team-level session
// gate: a custom leader's own pool never substitutes for the team's configured
// pool, so a pool-less team refuses the session even though the leader could
// dial its own head — the acceptance gate reads the team pool only.
func TestCustomLeaderSessionRefusedWithoutTeamPool(t *testing.T) {
	fixture := team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive,
			PoolMode: team.MemberPoolCustom, AgentUserPool: []string{"au-1"}},
	}}
	writeTeamPoolFixture(t, []team.Team{fixture}, failoverUsers("au-1"))
	m := openTeamOverlay(t)
	if m.teamPick.session.active {
		t.Fatal("a pool-less team must refuse the session even for a custom leader")
	}
	if got := m.teamPick.refusal; got != poolSessionRefusal {
		t.Fatalf("refusal = %q, want the pool hint %q", got, poolSessionRefusal)
	}
}
