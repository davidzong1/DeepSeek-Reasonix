package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/boot"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

func TestTeamRoleSkillPromptLoadsRolePlaybook(t *testing.T) {
	root := t.TempDir()
	for _, role := range []string{"leader", "member"} {
		path := filepath.Join(root, ".reasonix", "skills", "base", role, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + role + "\ndescription: team " + role + "\n---\n" + role + " playbook"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := teamRoleSkillPrompt(root, true); !strings.Contains(got, "leader playbook") {
		t.Fatalf("leader skill prompt = %q", got)
	}
	if got := teamRoleSkillPrompt(root, false); !strings.Contains(got, "member playbook") {
		t.Fatalf("member skill prompt = %q", got)
	}
}

// fakeBackend is a control.SessionAPI stand-in that only records teardown, so
// registry lifetime can be asserted without assembling a real controller. id
// distinguishes one assembled instance from another by identity, not value.
type fakeBackend struct {
	control.SessionAPI
	closed *int
	id     int
	status control.RuntimeStatus // injected busy state; zero = idle
}

func (f fakeBackend) Close()                               { *f.closed++ }
func (f fakeBackend) RuntimeStatus() control.RuntimeStatus { return f.status }

func binding(teamName, memberID string) team.MemberBinding {
	return team.MemberBinding{Team: teamName, MemberID: memberID}
}

// TestTeamBackendsBindIsLazyAndIdempotent pins the registry's core contract:
// one backend per member, assembled on first bind and reused after.
func TestTeamBackendsBindIsLazyAndIdempotent(t *testing.T) {
	builds := 0
	closed := 0
	r := newTeamBackends(func(team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return fakeBackend{closed: &closed}, nil
	}, 4)

	if _, ok := r.bound("alpha", "lead"); ok {
		t.Fatal("nothing should be assembled before the first bind")
	}
	first, err := r.bind(binding("alpha", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.bind(binding("alpha", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Errorf("builds = %d, want 1 (rebinding must reuse)", builds)
	}
	if first != second {
		t.Error("rebinding the same member must return the same backend")
	}
	if _, ok := r.bound("alpha", "lead"); !ok {
		t.Error("bound must report an assembled member")
	}
	// A different team with the same member id is a different instance.
	if _, err := r.bind(binding("beta", "lead")); err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Errorf("builds = %d, want 2 (team is part of the identity)", builds)
	}
}

// TestTeamBackendsFailedBuildLeavesRegistryClean pins the retry contract: a
// failed assembly records nothing, so the caller can bind again after fixing
// the pool entry.
func TestTeamBackendsFailedBuildLeavesRegistryClean(t *testing.T) {
	boom := errors.New("no credential")
	r := newTeamBackends(func(team.MemberBinding) (control.SessionAPI, error) {
		return nil, boom
	}, 4)
	if _, err := r.bind(binding("alpha", "lead")); !errors.Is(err, boom) {
		t.Fatalf("bind err = %v, want the build error", err)
	}
	if _, ok := r.bound("alpha", "lead"); ok {
		t.Error("a failed build must not register a backend")
	}
}

// TestTeamBackendsEvictsLeastRecentlyBound pins the cap: each backend owns a
// controller with subprocesses and a session lease, so the set is bounded and
// the least recently bound member is retired — never the one just bound.
func TestTeamBackendsEvictsLeastRecentlyBound(t *testing.T) {
	closed := 0
	r := newTeamBackends(func(team.MemberBinding) (control.SessionAPI, error) {
		return fakeBackend{closed: &closed}, nil
	}, 2)
	for _, id := range []string{"a", "b"} {
		if _, err := r.bind(binding("t", id)); err != nil {
			t.Fatal(err)
		}
	}
	// Re-bind a so b becomes least recently bound, then exceed the cap.
	if _, err := r.bind(binding("t", "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.bind(binding("t", "c")); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want 1 eviction", closed)
	}
	if _, ok := r.bound("t", "b"); ok {
		t.Error("b was least recently bound and should be retired")
	}
	for _, id := range []string{"a", "c"} {
		if _, ok := r.bound("t", id); !ok {
			t.Errorf("%s must stay assembled", id)
		}
	}
}

// TestTeamBackendsReleasePaths pins the destructive-path primitives: nothing
// may keep writing a context that is about to be cleared.
func TestTeamBackendsReleasePaths(t *testing.T) {
	closed := 0
	r := newTeamBackends(func(team.MemberBinding) (control.SessionAPI, error) {
		return fakeBackend{closed: &closed}, nil
	}, 8)
	for _, b := range []team.MemberBinding{
		binding("t", "a"), binding("t", "b"), binding("other", "a"),
	} {
		if _, err := r.bind(b); err != nil {
			t.Fatal(err)
		}
	}
	r.release("t", "a")
	if _, ok := r.bound("t", "a"); ok {
		t.Error("release must retire the member")
	}
	r.release("t", "nobody") // unknown is a no-op
	r.releaseTeam("t")
	if _, ok := r.bound("t", "b"); ok {
		t.Error("releaseTeam must retire every member of the team")
	}
	if _, ok := r.bound("other", "a"); !ok {
		t.Error("releaseTeam must not touch another team")
	}
	r.closeAll()
	if _, ok := r.bound("other", "a"); ok {
		t.Error("closeAll must retire everything")
	}
	if closed != 3 {
		t.Errorf("closed = %d, want 3 (each backend exactly once)", closed)
	}
}

// TestTeamBackendsFingerprintKeepsReuse pins the fingerprint contract's
// steady state: with a stable fingerprint an assembled backend is still reused,
// exactly as without a fingerprint installed.
func TestTeamBackendsFingerprintKeepsReuse(t *testing.T) {
	builds := 0
	closed := 0
	r := newTeamBackends(func(team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return fakeBackend{closed: &closed}, nil
	}, 4)
	r.setFingerprint(func(team.MemberBinding) (string, error) { return "stable", nil })

	if _, err := r.bind(binding("alpha", "lead")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.bind(binding("alpha", "lead")); err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Errorf("builds = %d, want 1 (stable fingerprint must reuse)", builds)
	}
}

// TestTeamBackendsRebuildsOnFingerprintChange pins the invalidation contract:
// once a fingerprint is installed, a changed identity (agent-user ref,
// provider, model, base url or API key) must retire the stale backend and
// assemble a fresh one — never reuse the old provider/credential.
func TestTeamBackendsRebuildsOnFingerprintChange(t *testing.T) {
	builds := 0
	closed := 0
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return fakeBackend{closed: &closed, id: builds}, nil
	}, 4)
	fp := "gpt-5.6"
	r.setFingerprint(func(team.MemberBinding) (string, error) { return fp, nil })

	first, err := r.bind(binding("alpha", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	fp = "deepseek-v4-flash"
	second, err := r.bind(binding("alpha", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Errorf("builds = %d, want 2 (changed fingerprint must rebuild)", builds)
	}
	if first.(fakeBackend).id == second.(fakeBackend).id {
		t.Error("a changed fingerprint must not return the stale backend")
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1 (the stale backend must be retired)", closed)
	}
}

// TestTeamBackendsFingerprintErrorKeepsServing pins the conservative path: a
// fingerprint that cannot be evaluated (dangling agent-user ref, pool read
// failure) must not interrupt the window — the assembled backend keeps serving
// until a resolvable fingerprint change rebuilds it.
func TestTeamBackendsFingerprintErrorKeepsServing(t *testing.T) {
	builds := 0
	closed := 0
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return fakeBackend{closed: &closed, id: builds}, nil
	}, 4)
	r.setFingerprint(func(team.MemberBinding) (string, error) { return "ok", nil })

	first, err := r.bind(binding("alpha", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	fpErr := errors.New("pool unreadable")
	r.setFingerprint(func(team.MemberBinding) (string, error) { return "", fpErr })
	second, err := r.bind(binding("alpha", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Errorf("builds = %d, want 1 (a fingerprint error must keep serving)", builds)
	}
	if first.(fakeBackend).id != second.(fakeBackend).id {
		t.Error("a fingerprint error must keep the assembled backend, not rebuild")
	}
	if closed != 0 {
		t.Errorf("closed = %d, want 0", closed)
	}

	// A resolvable fingerprint change afterwards still rebuilds.
	fp := "gpt-5.6"
	r.setFingerprint(func(team.MemberBinding) (string, error) { return fp, nil })
	fp = "deepseek-v4-flash"
	third, err := r.bind(binding("alpha", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if builds != 2 || second.(fakeBackend).id == third.(fakeBackend).id {
		t.Errorf("a resolvable change must rebuild: builds = %d", builds)
	}
}

// TestMemberAgentUserFingerprintSensitiveToIdentity pins what the fingerprint
// covers: every field a member backend bakes in at assembly — ref, provider,
// base url, model, API key — and that the key never appears in the digest.
func TestMemberAgentUserFingerprintSensitiveToIdentity(t *testing.T) {
	base := team.AgentUser{UserID: "u1", Provider: "openai", BaseURL: "https://x/v1", Model: "gpt-5.6", APIKey: "sk-one"}
	fp := memberAgentUserFingerprint(base)
	if strings.Contains(fp, "sk-one") {
		t.Fatal("the API key must never appear in the fingerprint")
	}
	for name, mutate := range map[string]func(*team.AgentUser){
		"provider": func(u *team.AgentUser) { u.Provider = "deepseek" },
		"base_url": func(u *team.AgentUser) { u.BaseURL = "https://y/v1" },
		"model":    func(u *team.AgentUser) { u.Model = "deepseek-v4-flash" },
		"api_key":  func(u *team.AgentUser) { u.APIKey = "sk-two" },
	} {
		changed := base
		mutate(&changed)
		if memberAgentUserFingerprint(changed) == fp {
			t.Errorf("changing %s must change the fingerprint", name)
		}
	}
}

// TestMemberBackendFingerprintResolvesFromPool pins the wiring fingerprint: it
// resolves through the same pool lookup the builder uses, so an edited pool
// entry (rebind, credential rotation) invalidates the cached backend.
func TestMemberBackendFingerprintResolvesFromPool(t *testing.T) {
	pool := fakePool{users: map[string]team.AgentUser{
		"u1": {UserID: "u1", Provider: "openai", Model: "gpt-5.6", BaseURL: "https://x/v1", APIKey: "sk"},
	}}
	fp := newMemberBackendFingerprint(memberBackendDeps{users: pool})
	got, err := fp(team.MemberBinding{Team: "t", MemberID: "m", AgentUserRef: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, memberAgentUserFingerprint(pool.users["u1"])) {
		t.Error("the fingerprint must hash the pool entry behind the ref")
	}
	if _, err := fp(team.MemberBinding{Team: "t", MemberID: "m", AgentUserRef: "missing"}); !errors.Is(err, team.ErrAgentUserNotFound) {
		t.Errorf("a dangling ref err = %v, want ErrAgentUserNotFound", err)
	}
}

// TestMemberSinkTagsEveryEvent pins the attribution the tagged channel exists
// for: one shared channel, every event labelled with its member.
func TestMemberSinkTagsEveryEvent(t *testing.T) {
	ch := make(chan memberEvent, 4)
	memberSink("lead", ch).Emit(event.Event{Kind: event.Notice, Text: "one"})
	memberSink("alice", ch).Emit(event.Event{Kind: event.Notice, Text: "two"})
	got := map[string]string{}
	for range 2 {
		e := <-ch
		got[e.member] = e.ev.Text
	}
	if got["lead"] != "one" || got["alice"] != "two" {
		t.Errorf("tagged events = %v", got)
	}
}

// TestMemberProviderResolverServesThePoolEntry pins R1.3's provider half: the
// member's ref resolves from its own pool entry, the catalog declares tools (a
// member is a full Agent, not a bare completion), and an unsupported provider
// is refused at assembly instead of at the first request.
//
// The dial is asserted on the gateway route, whose kind is openai: the
// anthropic adapter registers through a blank import in the host binaries
// (cmd/reasonix, desktop), so it is absent from this test binary. The official
// DeepSeek route is asserted on the mapping alone for that reason.
func TestMemberProviderResolverServesThePoolEntry(t *testing.T) {
	r, err := newMemberProviderResolver(team.AgentUser{
		UserID: "pool-1", Provider: "deepseek", Model: "deepseek-v4",
		BaseURL: "https://gateway.example.com/v1",
		APIKey:  "sk-secret", Effort: "max",
	}, netclient.ProxySpec{Mode: netclient.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if r.Ref() != "pool-1/deepseek-v4" {
		t.Errorf("ref = %q", r.Ref())
	}
	cat := r.Catalog()
	if len(cat) != 1 {
		t.Fatalf("catalog = %d entries, want 1", len(cat))
	}
	if cat[0].Ref != r.Ref() || cat[0].Model != "deepseek-v4" {
		t.Errorf("descriptor = %+v", cat[0])
	}
	if !cat[0].Tools {
		t.Error("a member backend must declare tool support")
	}
	if cat[0].DefaultEffort != "max" {
		t.Errorf("default effort = %q, want max", cat[0].DefaultEffort)
	}
	if r.kind != "openai" || r.endpoint != "https://gateway.example.com/v1" {
		t.Errorf("gateway route = %q at %q", r.kind, r.endpoint)
	}
	if _, err := r.Resolve(provider.Selection{Ref: r.Ref()}); err != nil {
		t.Fatalf("Resolve = %v", err)
	}

	// A per-call effort wins over the pool entry's default.
	high := "high"
	if _, err := r.Resolve(provider.Selection{Ref: r.Ref(), Effort: &high}); err != nil {
		t.Fatalf("Resolve with effort = %v", err)
	}

	// The official DeepSeek route is the anthropic-compatible endpoint.
	official, err := newMemberProviderResolver(team.AgentUser{
		UserID: "pool-2", Provider: "deepseek", Model: "deepseek-chat",
	}, netclient.ProxySpec{})
	if err != nil {
		t.Fatal(err)
	}
	if official.kind != "anthropic" || official.endpoint != team.DeepSeekDefaultBaseURL {
		t.Errorf("official route = %q at %q", official.kind, official.endpoint)
	}

	if _, err := newMemberProviderResolver(team.AgentUser{
		UserID: "x", Provider: "wat", Model: "m",
	}, netclient.ProxySpec{}); err == nil {
		t.Error("an unsupported provider must be refused at assembly")
	}
}

// fakePool serves pool entries from memory so the builder's failure paths can
// be asserted without a store on disk.
type fakePool struct {
	users map[string]team.AgentUser
	err   error
}

func (p fakePool) AgentUser(id string) (team.AgentUser, bool, error) {
	if p.err != nil {
		return team.AgentUser{}, false, p.err
	}
	u, ok := p.users[id]
	return u, ok, nil
}

// TestMemberSystemPromptIdentity pins route §2.2: the identity names the team,
// the member instance and the free-text role, and an unset role says so rather
// than leaving the specialty implied. Leader is not part of it — the leader
// property is a registry fact, never a prompt string.
func TestMemberSystemPromptIdentity(t *testing.T) {
	got := memberSystemPromptIdentity(team.MemberBinding{
		Team: "alpha", MemberID: "lead", Role: "coder", Leader: true,
	})
	for _, want := range []string{`member "lead"`, `team "alpha"`, "role is: coder"} {
		if !strings.Contains(got, want) {
			t.Errorf("identity missing %q:\n%s", want, got)
		}
	}
	bare := memberSystemPromptIdentity(team.MemberBinding{Team: "alpha", MemberID: "x"})
	if !strings.Contains(bare, "not configured") {
		t.Errorf("an unset role must be explicit:\n%s", bare)
	}
}

// TestMemberProxySpec pins the proxy contract: a team that turned its proxy off
// must not let a member fall through to the ambient environment.
func TestMemberProxySpec(t *testing.T) {
	off := memberProxySpec(team.ProxyConfig{})
	if off.Mode != netclient.ModeOff {
		t.Errorf("a disabled proxy must be an explicit off, got %q", off.Mode)
	}
	blank := memberProxySpec(team.ProxyConfig{Enabled: true})
	if blank.Mode != netclient.ModeOff {
		t.Errorf("an enabled proxy with no address must be off, got %q", blank.Mode)
	}
	on := memberProxySpec(team.ProxyConfig{Enabled: true, Address: "127.0.0.1:7980"})
	if on.Mode != netclient.ModeCustom || on.URL != "http://127.0.0.1:7980" {
		t.Errorf("proxy spec = %+v", on)
	}
}

// TestMemberBackendBuilderRefusesBadBindings pins the assembly refusals: they
// must name what to fix and leave nothing half-built, because the registry
// retries after the pool entry is corrected.
func TestMemberBackendBuilderRefusesBadBindings(t *testing.T) {
	pool := fakePool{users: map[string]team.AgentUser{
		"good": {UserID: "good", Provider: "openai", Model: "gpt", BaseURL: "https://x/v1"},
		"bad":  {UserID: "bad", Provider: "nope", Model: "m"},
	}}
	build := newMemberBackendBuilder(memberBackendDeps{
		ctx: t.Context(), users: pool, events: make(chan memberEvent, 1),
		base: func() boot.Options { return boot.Options{} },
	})

	if _, err := build(team.MemberBinding{Team: "t", MemberID: "m"}); err == nil {
		t.Error("an unbound member must be refused")
	} else if !strings.Contains(err.Error(), "no agent user bound") {
		t.Errorf("refusal should name the fix, got %v", err)
	}
	_, err := build(team.MemberBinding{Team: "t", MemberID: "m", AgentUserRef: "missing"})
	if !errors.Is(err, team.ErrAgentUserNotFound) {
		t.Errorf("a dangling ref err = %v, want ErrAgentUserNotFound", err)
	}
	if _, err := build(team.MemberBinding{Team: "t", MemberID: "m", AgentUserRef: "bad"}); err == nil {
		t.Error("an unsupported provider must be refused before boot.Build")
	}

	boom := errors.New("pool unreadable")
	failing := newMemberBackendBuilder(memberBackendDeps{
		ctx: t.Context(), users: fakePool{err: boom}, events: make(chan memberEvent, 1),
		base: func() boot.Options { return boot.Options{} },
	})
	if _, err := failing(team.MemberBinding{Team: "t", MemberID: "m", AgentUserRef: "good"}); !errors.Is(err, boom) {
		t.Errorf("a pool read failure must surface, got %v", err)
	}
}

// TestBindKeepsOldBackendServingOnRebuildFailure pins the atomic-swap half of
// the fingerprint path: when the pool entry changed but the rebuild fails, the
func TestBindKeepsPendingPromptBackendOnFingerprintChange(t *testing.T) {
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

	waiting := fakeBackend{closed: &closed, id: 1, status: control.RuntimeStatus{PendingPrompt: true}}
	r.live[backendKey("alpha", "lead")] = waiting
	got, err := r.bind(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != waiting {
		t.Error("a pending-prompt member must keep its backend on a fingerprint change")
	}
	if builds != 1 || closed != 0 {
		t.Errorf("a pending-prompt member must not rebuild: builds = %d, closed = %d", builds, closed)
	}
}
