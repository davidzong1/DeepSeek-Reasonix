package cli

// Pool save gate regressions: a busy referencing backend refuses the write, an
// idle one retires so the next bind rebuilds, and new/unbound entries pass.

import (
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// poolSaveStub records the pool model an assembly dialed, so a rebuild can be
// asserted on the persisted entry it was built from.
type poolSaveStub struct {
	fakeBackend
	model string
}

func poolUserIndex(users []team.AgentUser, id string) int {
	for i, u := range users {
		if u.UserID == id {
			return i
		}
	}
	return -1
}

// poolSavePicker opens the overlay on a team doc, seeds the pool entry and the
// editor state for it, and returns the picker plus the injected registry.
func poolSavePicker(t *testing.T, doc []team.Team, seed team.AgentUser) (*teamPicker, *teamBackends, *int) {
	t.Helper()
	writeTeamFixture(t, doc...)
	m := openTeamOverlay(t)
	p := m.teamPick
	if err := p.store.AddAgentUser(seed); err != nil {
		t.Fatal(err)
	}
	closed := 0
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		if _, ok, err := p.store.AgentUser(b.AgentUserRef); err != nil {
			return nil, err
		} else if !ok {
			return nil, team.ErrAgentUserNotFound
		}
		return fakeBackend{closed: &closed}, nil
	}, 4)
	p.backends = r
	p.pool = poolState{active: true}
	if err := p.reloadPool(); err != nil {
		t.Fatal(err)
	}
	return p, r, &closed
}

// poolSaveDraft arms the editor on the persisted entry with a model edit.
func poolSaveDraft(t *testing.T, p *teamPicker, id string) {
	t.Helper()
	i := poolUserIndex(p.pool.users, id)
	if i < 0 {
		t.Fatalf("pool entry %q not loaded", id)
	}
	p.pool.focus = i
	p.pool.adding = false
	draft := p.pool.users[i]
	draft.Model = "deepseek-v4-flash[1m]"
	p.pool.draft = draft
}

// TestPoolSaveRefusesWhileReferencingBackendBusy pins the gate's two reach
// paths at once: a member that inherits the team default and a member with an
// explicit override. A mid-turn backend of either must refuse the save, leave
// the stored entry untouched, and stay assembled.
func TestPoolSaveRefusesWhileReferencingBackendBusy(t *testing.T) {
	seed := team.AgentUser{UserID: "u1", Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "sk"}
	p, r, closed := poolSavePicker(t, []team.Team{
		{Name: "alpha", DefaultAgentUserRef: "u1", Template: []team.MemberSlot{
			{MemberID: "lead", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
		{Name: "beta", Template: []team.MemberSlot{
			{MemberID: "worker", Role: team.RoleCoder, Status: team.MemberStatusActive, AgentUserRef: "u1"},
		}},
	}, seed)
	r.live[backendKey("alpha", "lead")] = fakeBackend{closed: closed, status: control.RuntimeStatus{Running: true}}
	r.live[backendKey("beta", "worker")] = fakeBackend{closed: closed, status: control.RuntimeStatus{PendingPrompt: true}}

	poolSaveDraft(t, p, "u1")
	p.savePoolEdit()

	if p.pool.errMsg == "" {
		t.Fatal("a mid-turn referencing backend must refuse the save")
	}
	for _, want := range []string{"alpha/lead", "beta/worker"} {
		if !strings.Contains(p.pool.errMsg, want) {
			t.Errorf("refusal should name %q, got %q", want, p.pool.errMsg)
		}
	}
	if got := readStoredPool(t); got[0].Model != "deepseek-v4-flash" {
		t.Errorf("busy gate must not write, stored model = %q", got[0].Model)
	}
	if *closed != 0 {
		t.Errorf("busy gate must not release a backend, closed = %d", *closed)
	}
	if _, ok := r.bound("alpha", "lead"); !ok {
		t.Error("busy gate must leave the running backend assembled")
	}
}

// TestPoolSaveReleasesIdleBoundBackendAndRebuilds pins the idle half: with no
// referencing backend mid-turn the edit persists, the assembled backend is
// retired, and the next bind assembles against the stored [1m] model.
func TestPoolSaveReleasesIdleBoundBackendAndRebuilds(t *testing.T) {
	seed := team.AgentUser{UserID: "u1", Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "sk"}
	builds := 0
	writeTeamFixture(t, team.Team{Name: "alpha", DefaultAgentUserRef: "u1", Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openTeamOverlay(t)
	p := m.teamPick
	if err := p.store.AddAgentUser(seed); err != nil {
		t.Fatal(err)
	}
	closed := 0
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		user, ok, err := p.store.AgentUser(b.AgentUserRef)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, team.ErrAgentUserNotFound
		}
		builds++
		return poolSaveStub{fakeBackend: fakeBackend{closed: &closed, id: builds}, model: user.Model}, nil
	}, 4)
	p.backends = r
	p.pool = poolState{active: true}
	if err := p.reloadPool(); err != nil {
		t.Fatal(err)
	}

	if _, err := r.bind(team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1"}); err != nil {
		t.Fatal(err)
	}
	poolSaveDraft(t, p, "u1")
	p.savePoolEdit()

	if p.pool.errMsg != "" {
		t.Fatalf("idle referencing backends must not refuse, got %q", p.pool.errMsg)
	}
	if got := readStoredPool(t); got[0].Model != "deepseek-v4-flash[1m]" {
		t.Fatalf("save must persist the model edit, got %q", got[0].Model)
	}
	if closed != 1 {
		t.Errorf("an idle referencing backend must be released, closed = %d", closed)
	}
	if _, ok := r.bound("alpha", "lead"); ok {
		t.Error("a released backend must leave the registry")
	}
	rebound, err := r.bind(team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Errorf("the next bind must rebuild, builds = %d", builds)
	}
	if got := rebound.(poolSaveStub).model; got != "deepseek-v4-flash[1m]" {
		t.Errorf("the rebuilt backend must dial the new [1m] model, got %q", got)
	}
}

// TestPoolSaveIdentityOnlyEditSkipsBusyGate pins the runtime-fingerprint scope:
// an edit that changes no field a backend bakes in (identity only) must persist
// even while a referencing backend is mid-turn, and must not release it.
func TestPoolSaveIdentityOnlyEditSkipsBusyGate(t *testing.T) {
	seed := team.AgentUser{UserID: "u1", Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "sk"}
	p, r, closed := poolSavePicker(t, []team.Team{
		{Name: "alpha", DefaultAgentUserRef: "u1", Template: []team.MemberSlot{
			{MemberID: "lead", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
	}, seed)
	r.live[backendKey("alpha", "lead")] = fakeBackend{closed: closed, status: control.RuntimeStatus{BackgroundJobs: 1}}

	i := poolUserIndex(p.pool.users, "u1")
	p.pool.focus = i
	p.pool.adding = false
	draft := p.pool.users[i]
	draft.Identity = "acct@moved"
	p.pool.draft = draft
	p.savePoolEdit()

	if p.pool.errMsg != "" {
		t.Fatalf("an identity-only edit must not gate on busy members, got %q", p.pool.errMsg)
	}
	if got := readStoredPool(t); got[0].Identity != "acct@moved" {
		t.Errorf("identity-only edit must persist, got %q", got[0].Identity)
	}
	if *closed != 0 {
		t.Errorf("identity-only edit must not release the backend, closed = %d", *closed)
	}
}

// TestPoolSaveUnboundAndNewEntriesUnaffected pins the untouched paths: editing
// an entry no binding references persists without consulting the busy gate, and
// adding a brand-new entry is never gated even while another entry's backend is
// mid-turn.
func TestPoolSaveUnboundAndNewEntriesUnaffected(t *testing.T) {
	seed := team.AgentUser{UserID: "u1", Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "sk"}
	p, r, closed := poolSavePicker(t, []team.Team{
		{Name: "alpha", DefaultAgentUserRef: "u1", Template: []team.MemberSlot{
			{MemberID: "lead", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
	}, seed)
	r.live[backendKey("alpha", "lead")] = fakeBackend{closed: closed, status: control.RuntimeStatus{Running: true}}
	if err := p.store.AddAgentUser(team.AgentUser{UserID: "u2", Provider: "openai", Model: "gpt-5.6", APIKey: "sk2"}); err != nil {
		t.Fatal(err)
	}
	if err := p.reloadPool(); err != nil {
		t.Fatal(err)
	}

	// An unbound entry's edit is unrelated to the running member on u1.
	u2 := poolUserIndex(p.pool.users, "u2")
	p.pool.focus = u2
	p.pool.adding = false
	draft := p.pool.users[u2]
	draft.Model = "gpt-5.7"
	p.pool.draft = draft
	p.savePoolEdit()
	if p.pool.errMsg != "" {
		t.Fatalf("an unbound entry must not be gated, got %q", p.pool.errMsg)
	}
	if got := readStoredPool(t); got[poolUserIndex(got, "u2")].Model != "gpt-5.7" {
		t.Errorf("unbound edit must persist, got %+v", got)
	}

	// Adding a fresh entry never consults the gate.
	p.pool.adding = true
	p.pool.draft = team.AgentUser{UserID: "u3", Provider: "anthropic", Model: "claude-opus-5", APIKey: "sk3"}
	p.savePoolEdit()
	if p.pool.errMsg != "" {
		t.Fatalf("adding a new entry must not be gated, got %q", p.pool.errMsg)
	}
	if got := readStoredPool(t); poolUserIndex(got, "u3") < 0 {
		t.Errorf("add must persist u3, got %+v", got)
	}
	if *closed != 0 {
		t.Errorf("neither unbound edit nor add may release a backend, closed = %d", *closed)
	}
}
