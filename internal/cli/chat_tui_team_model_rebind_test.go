package cli

// Regression tests for the member model contract: an openai pool entry routes
// to the openai resolver with its own base url and model, a changed fingerprint
// forces a rebuild unless busy, and /model rebind updates modelRef and replays.

import (
	"errors"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestMemberProviderResolverRoutesOpenAI pins the openai half of the resolver
// mapping: a provider "openai" entry dials the openai runtime, the official
// endpoint when no base url is set, the entry's own endpoint otherwise, and the
// model reaches the ref, the catalog descriptor and the dial config alike.
func TestMemberProviderResolverRoutesOpenAI(t *testing.T) {
	r, err := newMemberProviderResolver(team.AgentUser{
		UserID: "u1", Provider: "openai", Model: "gpt-5.6",
		APIKey: "sk-secret", Effort: "high",
	}, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if r.kind != "openai" || r.endpoint != "https://api.openai.com" {
		t.Errorf("official route = %q at %q, want openai at https://api.openai.com", r.kind, r.endpoint)
	}
	if r.Ref() != "u1/gpt-5.6" {
		t.Errorf("ref = %q", r.Ref())
	}
	if r.model != "gpt-5.6" {
		t.Errorf("model = %q", r.model)
	}
	cat := r.Catalog()
	if cat[0].Model != "gpt-5.6" || cat[0].Ref != r.Ref() {
		t.Errorf("descriptor = %+v", cat[0])
	}
	if _, err := r.Resolve(provider.Selection{Ref: r.Ref()}); err != nil {
		t.Fatalf("Resolve = %v", err)
	}

	// A base url override dials the entry's own endpoint, never the official one.
	override, err := newMemberProviderResolver(team.AgentUser{
		UserID: "u2", Provider: "openai", Model: "gpt-5.6", BaseURL: "https://gateway.example.com/v1",
	}, netclient.ProxySpec{})
	if err != nil {
		t.Fatal(err)
	}
	if override.kind != "openai" || override.endpoint != "https://gateway.example.com/v1" {
		t.Errorf("override route = %q at %q", override.kind, override.endpoint)
	}
}

// TestBindRebuildsOnPoolEntryChange pins the fingerprint chain end to end: the
// registry's own fingerprint resolves the pool entry behind the binding, so
// editing the entry (a model change here) retires the assembled backend and
// builds a fresh one on the next bind — the stale provider never keeps serving.
func TestBindRebuildsOnPoolEntryChange(t *testing.T) {
	builds := 0
	closed := 0
	pool := fakePool{users: map[string]team.AgentUser{
		"u1": {UserID: "u1", Provider: "openai", Model: "gpt-5.6", BaseURL: "https://x/v1", APIKey: "sk"},
	}}
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return fakeBackend{closed: &closed, id: builds}, nil
	}, 4)
	r.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: pool}))
	b := team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1"}

	if _, err := r.bind(b); err != nil {
		t.Fatal(err)
	}
	if _, err := r.bind(b); err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("an unchanged entry must reuse, builds = %d", builds)
	}

	pool.users["u1"] = team.AgentUser{UserID: "u1", Provider: "openai", Model: "gpt-5.7", BaseURL: "https://x/v1", APIKey: "sk"}
	second, err := r.bind(b)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Errorf("a changed entry must rebuild, builds = %d", builds)
	}
	if second.(fakeBackend).id == 1 {
		t.Error("a changed entry must not return the stale backend")
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1 (the stale backend must be retired)", closed)
	}
	if _, err := r.bind(b); err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Errorf("the rebuilt entry must reuse, builds = %d", builds)
	}
}

// TestBindKeepsOldBackendWhenRebuildFails pins the failure half of the rebuild
// gate: a changed identity whose assembly fails must leave the previous backend
// serving — the caller surfaces the error and the window is never stranded on a
// half-bound member. The next successful bind retires it and rebuilds.
func TestBindKeepsOldBackendWhenRebuildFails(t *testing.T) {
	builds := 0
	closed := 0
	fail := false
	pool := fakePool{users: map[string]team.AgentUser{
		"u1": {UserID: "u1", Provider: "openai", Model: "gpt-5.6", BaseURL: "https://x/v1", APIKey: "sk"},
	}}
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		if fail {
			return nil, errors.New("provider unreachable")
		}
		return fakeBackend{closed: &closed, id: builds}, nil
	}, 4)
	r.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: pool}))
	b := team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1"}

	first, err := r.bind(b)
	if err != nil {
		t.Fatal(err)
	}
	pool.users["u1"] = team.AgentUser{UserID: "u1", Provider: "openai", Model: "gpt-5.7", BaseURL: "https://x/v1", APIKey: "sk"}
	fail = true
	if _, err := r.bind(b); err == nil {
		t.Fatal("a failed rebuild must surface the error")
	}
	if got, ok := r.bound("alpha", "lead"); !ok || got != first {
		t.Error("a failed rebuild must keep the previous backend serving")
	}
	if closed != 0 {
		t.Errorf("a failed rebuild must not retire the old backend, closed = %d", closed)
	}

	fail = false
	second, err := r.bind(b)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Error("a successful rebuild after a failure must retire the stale backend")
	}
	if closed != 1 {
		t.Errorf("the stale backend must retire on the successful rebuild, closed = %d", closed)
	}
}

// TestBindKeepsBusyBackendOnFingerprintChange pins the busy half of the rebuild
// gate: while a member's backend is in flight — running a turn, waiting on a
// prompt, or running background jobs — a changed identity must not tear it
// down. Rebuilding would kill the turn or strand the prompt; only an idle bind
// retires it and assembles the fresh one.
func TestBindKeepsBusyBackendOnFingerprintChange(t *testing.T) {
	for _, busy := range []control.RuntimeStatus{
		{Running: true},
		{PendingPrompt: true},
		{BackgroundJobs: 2},
	} {
		builds := 0
		closed := 0
		pool := fakePool{users: map[string]team.AgentUser{
			"u1": {UserID: "u1", Provider: "openai", Model: "gpt-5.6", BaseURL: "https://x/v1", APIKey: "sk"},
		}}
		r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
			builds++
			return fakeBackend{closed: &closed, id: builds}, nil
		}, 4)
		r.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: pool}))
		b := team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1"}
		if _, err := r.bind(b); err != nil {
			t.Fatal(err)
		}
		pool.users["u1"] = team.AgentUser{UserID: "u1", Provider: "openai", Model: "gpt-5.7", BaseURL: "https://x/v1", APIKey: "sk"}
		busyBackend := fakeBackend{closed: &closed, id: 1, status: busy}
		r.live[backendKey("alpha", "lead")] = busyBackend

		got, err := r.bind(b)
		if err != nil {
			t.Fatalf("busy %+v: bind = %v", busy, err)
		}
		if got != busyBackend {
			t.Errorf("busy %+v: a changed identity must keep the assembled backend", busy)
		}
		if builds != 1 || closed != 0 {
			t.Errorf("busy %+v: must not rebuild or retire, builds = %d closed = %d", busy, builds, closed)
		}
	}
}

// modelRefBackend is a stubBackend whose ModelRef reports the pool entry the
// builder resolved, so a rebind can be asserted on the window's model line.
type modelRefBackend struct {
	stubBackend
	ref string
}

func (b modelRefBackend) ModelRef() string { return b.ref }

// TestFingerprintSensitiveToRoleProxyEffort pins every assembly-time identity
// beyond the pool entry itself: the member's role and proxy shape its system
// prompt and transport, and the pool entry's effort its dial default, so each
// change must invalidate the cached backend.
func TestFingerprintSensitiveToRoleProxyEffort(t *testing.T) {
	pool := fakePool{users: map[string]team.AgentUser{
		"u1": {UserID: "u1", Provider: "openai", Model: "gpt-5.6", Effort: "low", BaseURL: "https://x/v1", APIKey: "sk"},
	}}
	fp := newMemberBackendFingerprint(memberBackendDeps{users: pool})
	base := team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1"}
	baseFP, err := fp(base)
	if err != nil {
		t.Fatal(err)
	}

	effort, err := fp(base)
	if err != nil {
		t.Fatal(err)
	}
	pool.users["u1"] = team.AgentUser{UserID: "u1", Provider: "openai", Model: "gpt-5.6", Effort: "high", BaseURL: "https://x/v1", APIKey: "sk"}
	if got, _ := fp(base); got == effort {
		t.Error("changing the pool entry's effort must change the fingerprint")
	}
	roleFP, err := fp(team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1", Role: team.RoleCoder})
	if err != nil {
		t.Fatal(err)
	}
	if roleFP == baseFP {
		t.Error("changing the member's role must change the fingerprint")
	}
	proxyFP, err := fp(team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1", Proxy: team.ProxyConfig{Enabled: true, Address: "127.0.0.1:7890"}})
	if err != nil {
		t.Fatal(err)
	}
	if proxyFP == baseFP {
		t.Error("changing the member's proxy must change the fingerprint")
	}
}

// TestRebindMemberAgentUserUpdatesModelRef pins the /model success path: while
// a member is bound, choosing another agent-user entry repoints the member's
// backend at the new entry and the window's model line follows — the member's
// model is the pool entry's, never the chat's own.
func TestRebindMemberAgentUserUpdatesModelRef(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive, AgentUserRef: "pool-a"},
	}})
	m := openTeamOverlay(t)
	if err := m.teamPick.store.AddAgentUser(team.AgentUser{
		UserID: "pool-a", Provider: "openai", Model: "gpt-5.6", BaseURL: "https://x/v1", APIKey: "sk",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.teamPick.store.AddAgentUser(team.AgentUser{
		UserID: "pool-b", Provider: "deepseek", Model: "deepseek-v4", BaseURL: "https://y/v1", APIKey: "sk2",
	}); err != nil {
		t.Fatal(err)
	}
	store := m.teamPick.store
	m.memberEvents = make(chan memberEvent, 8)
	builds := 0
	replays := 0
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		user, ok, err := store.AgentUser(b.AgentUserRef)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, team.ErrAgentUserNotFound
		}
		return modelRefBackend{stubBackend: stubBackend{label: b.MemberID, replays: &replays},
			ref: memberModelRef(user.UserID, user.Model)}, nil
	}, 4)
	m.teamBackends.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: store}))
	m.switchTeamMember("lead")
	if got := m.modelRef; got != "pool-a/gpt-5.6" {
		t.Fatalf("modelRef after bind = %q, want pool-a/gpt-5.6", got)
	}
	if replays != 1 {
		t.Fatalf("binding must ask the backend to replay prompts, replays = %d", replays)
	}

	m.runMemberModelSubcommand("/model pool-b")
	if got := m.modelRef; got != "pool-b/deepseek-v4" {
		t.Errorf("modelRef after rebind = %q, want pool-b/deepseek-v4", got)
	}
	if m.teamPick.session.errMsg != "" {
		t.Errorf("a valid rebind must not refuse, got %q", m.teamPick.session.errMsg)
	}
	if builds != 2 {
		t.Errorf("a rebind must reassemble the member's backend, builds = %d", builds)
	}
	if replays != 2 {
		t.Errorf("a rebind must replay the freshly assembled backend, replays = %d", replays)
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Errorf("the rebind must keep the member bound, got %q", got)
	}
}
