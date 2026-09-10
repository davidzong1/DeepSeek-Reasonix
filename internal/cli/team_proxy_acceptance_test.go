package cli

// Proxy acceptance additions: a binding-to-transport matrix over the team
// default and member override, and a guard that proxy writes never disturb a
// deposited discussion outcome. Both compile against committed production API.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/netclient"
	"reasonix/internal/team"
)

// newProxyAcceptanceService builds a real team store plus a discussion host
// service over it, returning both so a test can mutate the store mid-flight.
func newProxyAcceptanceService(t *testing.T, root string) (*team.TeamStore, *teamTaskService) {
	t.Helper()
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	svc := newTeamTaskService(store, nil, "", func(team.MemberBinding) (control.SessionAPI, error) {
		return &taskBackendStub{}, nil
	})
	svc.setKnowledgeDataRoot(filepath.Join(root, "kb"))
	svc.setDiscussionDataDir(root)
	return store, svc.forTeam("alpha")
}

// TestProxyBindingToTransportMatrix pins the full resolution chain for one
// member: team default and member override collapse in the binding, then map to
// a transport spec where a disabled proxy is an explicit off (never an ambient
// fall-through). Matrix over team on/off x override nil/on/off.
func TestProxyBindingToTransportMatrix(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name     string
		team     *team.ProxyConfig
		override *bool
		wantMode string
		wantURL  string
	}{
		{"no team, inherit", nil, nil, netclient.ModeOff, ""},
		{"no team, force on", nil, &on, netclient.ModeCustom, "http://" + team.DefaultProxyAddress},
		{"no team, force off", nil, &off, netclient.ModeOff, ""},
		{"team on, inherit", &team.ProxyConfig{Enabled: true, Address: "10.0.0.1:7890"}, nil, netclient.ModeCustom, "http://10.0.0.1:7890"},
		{"team on, force on", &team.ProxyConfig{Enabled: true, Address: "10.0.0.1:7890"}, &on, netclient.ModeCustom, "http://10.0.0.1:7890"},
		{"team on, force off", &team.ProxyConfig{Enabled: true, Address: "10.0.0.1:7890"}, &off, netclient.ModeOff, ""},
		{"team off, inherit", &team.ProxyConfig{Enabled: false, Address: "10.0.0.1:7890"}, nil, netclient.ModeOff, ""},
		{"team off, force on", &team.ProxyConfig{Enabled: false, Address: "10.0.0.1:7890"}, &on, netclient.ModeCustom, "http://10.0.0.1:7890"},
		{"team off, force off", &team.ProxyConfig{Enabled: false, Address: "10.0.0.1:7890"}, &off, netclient.ModeOff, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newProxyAcceptanceService(t, t.TempDir())
			if tc.team != nil {
				if err := store.SetTeamProxy("alpha", *tc.team); err != nil {
					t.Fatal(err)
				}
			}
			if tc.override != nil {
				if err := store.SetMemberProxyOverride("alpha", "coder", tc.override); err != nil {
					t.Fatal(err)
				}
			}
			bindings, err := store.Bindings("alpha")
			if err != nil {
				t.Fatal(err)
			}
			var b *team.MemberBinding
			for i := range bindings {
				if bindings[i].MemberID == "coder" {
					b = &bindings[i]
				}
			}
			if b == nil {
				t.Fatalf("coder binding missing from %+v", bindings)
			}
			spec := memberProxySpec(b.Proxy)
			if spec.Mode != tc.wantMode || spec.URL != tc.wantURL {
				t.Errorf("memberProxySpec(%+v) = {Mode:%q URL:%q}, want {Mode:%q URL:%q}", b.Proxy, spec.Mode, spec.URL, tc.wantMode, tc.wantURL)
			}
		})
	}
}

// TestProxyWriteDoesNotDisturbDepositedOutcome pins KB-orthogonality: a proxy
// write after a consensus deposit must not re-deposit, lose the marker, or
// change the deterministic body the KB already holds.
func TestProxyWriteDoesNotDisturbDepositedOutcome(t *testing.T) {
	store, svc := newProxyAcceptanceService(t, t.TempDir())

	if _, err := svc.DiscussionStart("lead", "decide transport", []string{"coder"}, 1); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := svc.DiscussionSubmitConclusion("coder", 1, "proposal: keep proxy out of the deposit body"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := svc.DiscussionNextRound("lead", discussionAdvanceInput{
		ConsensusReached: true,
		Consensus:        "decision: deposit is proxy-independent",
		TechnicalRoute:   "single deterministic body; proxy is a transport concern",
	}); err != nil {
		t.Fatalf("next round: %v", err)
	}
	tk, err := svc.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// A proxy write on the same store must leave the delivered marker and the
	// KB item untouched.
	if err := store.SetTeamProxy("alpha", team.ProxyConfig{Enabled: true, Address: "10.0.0.1:7890"}); err != nil {
		t.Fatalf("set team proxy: %v", err)
	}
	ds, err := svc.discussionStore()
	if err != nil || ds == nil {
		t.Fatalf("discussion store: %v", err)
	}
	doc, err := ds.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Deposit == nil || doc.Deposit.Status != team.DiscussionDepositDelivered {
		t.Fatalf("proxy write must not disturb the delivered marker, got %+v", doc.Deposit)
	}
	got, err := tk.Manager.Query(context.Background(), model.Query{Text: "proxy-independent", Scope: model.ScopeTeam, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("proxy write must not duplicate or drop the KB item, got %d", len(got))
	}
	if it := got[0].Item; it.AuthorID != "lead" || it.Kind != model.ItemDecision || !it.Live() {
		t.Fatalf("deposited item %+v changed after proxy write", it)
	}
	if !strings.HasPrefix(doc.Deposit.Text, "讨论主题: decide transport") {
		t.Fatalf("deposit body must stay deterministic, got %q", doc.Deposit.Text)
	}
	if err := svc.DiscussionDeposit(); err != nil {
		t.Fatalf("re-deposit after proxy write must stay a no-op, got %v", err)
	}
}

// proxyTeam returns a one-member team whose slot carries the given override.
func proxyTeam(override *bool) team.Team {
	return team.Team{Name: "alpha", Template: []team.MemberSlot{{
		MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive, ProxyEnabled: override,
	}}}
}

// memberProxyPicker opens the roster member editor on the proxy row's picker,
// seeded from the slot's current override.
func memberProxyPicker(t *testing.T) chatTUI {
	t.Helper()
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // status -> proxy row
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the proxy picker
	return m
}

// TestMemberEditorProxyFieldThreeWay pins the member editor's proxy row: it
// offers inherit/on/off, and s publishes the override through the store, so a
// force-off member stays off and a force-on one on under any team default.
func TestMemberEditorProxyFieldThreeWay(t *testing.T) {
	t.Run("force on", func(t *testing.T) {
		writeTeamFixture(t, proxyTeam(nil))
		m := memberProxyPicker(t)
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // inherit -> on
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		teamKey(m, tea.KeyPressMsg{Code: 's'})
		got := readStoredTeamDoc(t).Teams[0].Template[0].ProxyEnabled
		if got == nil || !*got {
			t.Fatalf("s should persist force-on, got %v", got)
		}
	})
	t.Run("force off", func(t *testing.T) {
		writeTeamFixture(t, proxyTeam(nil))
		m := memberProxyPicker(t)
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // inherit -> on
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // on -> off
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		teamKey(m, tea.KeyPressMsg{Code: 's'})
		got := readStoredTeamDoc(t).Teams[0].Template[0].ProxyEnabled
		if got == nil || *got {
			t.Fatalf("s should persist force-off, got %v", got)
		}
	})
	t.Run("back to inherit", func(t *testing.T) {
		on := true
		writeTeamFixture(t, proxyTeam(&on))
		m := memberProxyPicker(t)
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp}) // on -> inherit
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		teamKey(m, tea.KeyPressMsg{Code: 's'})
		got := readStoredTeamDoc(t).Teams[0].Template[0].ProxyEnabled
		if got != nil {
			t.Fatalf("s should clear a force-on back to inherit, got %v", *got)
		}
	})
}

// proxyEditor opens the roster and returns it with the team proxy editor open.
func proxyEditor(t *testing.T) chatTUI {
	t.Helper()
	return teamKey(openRoster(t), tea.KeyPressMsg{Code: 'p'})
}

// TestTeamProxyEditorSeedsPersisted pins that p reopens the editor seeded from
// the persisted config rather than the defaults, and Esc leaves it untouched.
func TestTeamProxyEditorSeedsPersisted(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{{
		MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive,
	}}, Proxy: &team.ProxyConfig{Enabled: true, Address: "10.0.0.1:7890"}})
	m := proxyEditor(t)
	if pe := m.teamPick.proxyEdit; !pe.on || pe.addr != "10.0.0.1:7890" {
		t.Fatalf("p must seed from the persisted proxy, got on=%v addr=%q", pe.on, pe.addr)
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "10.0.0.1:7890") {
		t.Fatalf("the open editor must render the persisted address:\n%s", got)
	}
	before := readStoredTeamDoc(t)
	teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	bp, ap := before.Teams[0].Proxy, readStoredTeamDoc(t).Teams[0].Proxy
	if bp == nil || ap == nil || *bp != *ap {
		t.Fatalf("Esc must not rewrite the proxy, before %+v after %+v", bp, ap)
	}
}

// TestTeamProxyEditorSavesCustomAddress pins the editor's free-text address
// row: editing it and enabling through the on/off picker publishes an explicit
// custom endpoint, distinct from the default.
func TestTeamProxyEditorSavesCustomAddress(t *testing.T) {
	writeTeamFixture(t, proxyTeam(nil))
	m := proxyEditor(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // enabled -> address
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the address field
	for range len(team.DefaultProxyAddress) {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m = typeTeamName(m, "10.0.0.1:7890")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // commit the address
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})    // address -> enabled
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the on/off picker
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})    // off -> on
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm on
	teamKey(m, tea.KeyPressMsg{Code: 's'})              // publish
	p := readStoredTeamDoc(t).Teams[0].Proxy
	if p == nil || !p.Enabled || p.Address != "10.0.0.1:7890" {
		t.Fatalf("editor must persist an explicit custom proxy, got %+v", p)
	}
}

// TestProxyDefaultAddressMatchesMultAgentMCP pins the final spec's endpoint:
// the fallback is 127.0.0.1:7890 (the mult_agent_mcp default), and enabling a
// blank address normalizes onto it.
func TestProxyDefaultAddressMatchesMultAgentMCP(t *testing.T) {
	const want = "127.0.0.1:7890"
	if team.DefaultProxyAddress != want {
		t.Fatalf("DefaultProxyAddress = %q, want %q", team.DefaultProxyAddress, want)
	}
	store, err := team.NewTeamStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{Name: "alpha"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTeamProxy("alpha", team.ProxyConfig{Enabled: true}); err != nil {
		t.Fatalf("enabling a blank address must normalize, got %v", err)
	}
	doc, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p := doc.Teams[0].Proxy; p == nil || !p.Enabled || p.Address != want {
		t.Fatalf("enabled blank must store the default address, got %+v", p)
	}
}

// TestImportProxyOverrideReachesTransport pins the full route an imported
// override takes: proxy_mode lands on MemberSlot.ProxyEnabled, and bindings
// resolve to a transport where the team proxy is on for inheritors and off for
// a force-off member.
func TestImportProxyOverrideReachesTransport(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "teams_data.json")
	content := `{"teams":{"alpha":{"default_agent":"claude","proxy":{"enabled":true,"host":"127.0.0.1","port":7890},"members":{
"forced-off":{"role":"coder","proxy_mode":"disabled"},
"forced-on":{"role":"coder","proxy_mode":"enabled"},
"plain":{"role":"coder"}}}},"agent_users":{}}`
	if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := store.ImportFromMCP(src, team.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProxyOverridesImported != 2 {
		t.Fatalf("ProxyOverridesImported = %d, want 2", report.ProxyOverridesImported)
	}
	doc, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	tp := doc.Teams[0].Proxy
	if tp == nil || !tp.Enabled || tp.Address != "127.0.0.1:7890" {
		t.Fatalf("team proxy must import enabled with the host/port endpoint, got %+v", tp)
	}
	byID := map[string]*bool{}
	for _, m := range doc.Teams[0].Template {
		byID[m.MemberID] = m.ProxyEnabled
	}
	if b := byID["forced-off"]; b == nil || *b {
		t.Fatalf("proxy_mode disabled must force off, got %v", byID["forced-off"])
	}
	if b := byID["forced-on"]; b == nil || !*b {
		t.Fatalf("proxy_mode enabled must force on, got %v", byID["forced-on"])
	}
	if byID["plain"] != nil {
		t.Fatalf("absent override must stay inherit, got %v", byID["plain"])
	}
	bindings, err := store.Bindings("alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		w, ok := map[string]string{
			"forced-off": netclient.ModeOff, "forced-on": netclient.ModeCustom, "plain": netclient.ModeCustom,
		}[b.MemberID]
		if !ok {
			continue
		}
		spec := memberProxySpec(b.Proxy)
		if spec.Mode != w {
			t.Fatalf("%s transport = %q, want %q (proxy %+v)", b.MemberID, spec.Mode, w, b.Proxy)
		}
		if spec.Mode == netclient.ModeCustom && spec.URL != "http://127.0.0.1:7890" {
			t.Fatalf("%s custom URL = %q, want http://127.0.0.1:7890", b.MemberID, spec.URL)
		}
	}
}
