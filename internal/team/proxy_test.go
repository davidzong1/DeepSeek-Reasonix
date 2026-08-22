package team

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// TestProxyForLocksPrecedence pins the §4.3 contract with the full override
// combination: member override (nil = inherit, true = force on, false = force
// off) beats the team default, which beats off. Every on result carries an
// address — the team's own or the default fallback.
func TestProxyForLocksPrecedence(t *testing.T) {
	teamOn := &ProxyConfig{Enabled: true, Address: "10.0.0.1:7890"}
	teamOff := &ProxyConfig{Enabled: false, Address: "10.0.0.1:7890"}
	on, off := true, false
	cases := []struct {
		name     string
		team     *ProxyConfig
		override *bool
		wantOn   bool
		wantAddr string
	}{
		{"no config at all", nil, nil, false, ""},
		{"team on, inherit", teamOn, nil, true, "10.0.0.1:7890"},
		{"team on, force on", teamOn, &on, true, "10.0.0.1:7890"},
		{"team on, force off", teamOn, &off, false, ""},
		{"team off, inherit", teamOff, nil, false, ""},
		{"team off, force on", teamOff, &on, true, "10.0.0.1:7890"},
		{"team off, force off", teamOff, &off, false, ""},
		{"no team, force on", nil, &on, true, DefaultProxyAddress},
		{"no team, force off", nil, &off, false, ""},
	}
	for _, tc := range cases {
		got, enabled := ProxyFor(tc.team, tc.override)
		if enabled != tc.wantOn {
			t.Errorf("%s: enabled = %v, want %v", tc.name, enabled, tc.wantOn)
		}
		if got.Address != tc.wantAddr {
			t.Errorf("%s: address = %q, want %q", tc.name, got.Address, tc.wantAddr)
		}
		if got.Enabled != tc.wantOn {
			t.Errorf("%s: config.Enabled = %v, want %v", tc.name, got.Enabled, tc.wantOn)
		}
	}
}

func TestTeamStoreSetTeamProxy(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	good := ProxyConfig{Enabled: true, Address: "127.0.0.1:7890"}
	if err := ts.SetTeamProxy("alpha", good); err != nil {
		t.Fatal(err)
	}
	// An enabled config naming no address normalizes to the default.
	if err := ts.SetTeamProxy("alpha", ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p := doc.Teams[0].Proxy; p == nil || !p.Enabled || p.Address != DefaultProxyAddress {
		t.Fatalf("proxy after empty-address set: %+v, want default %q", p, DefaultProxyAddress)
	}
	// Refusals: DNS names, non-IP hosts, malformed or out-of-range ports.
	for _, bad := range []ProxyConfig{
		{Enabled: true, Address: "proxy.example.com:7890"},
		{Enabled: true, Address: "127.0.0.1"},
		{Enabled: true, Address: "127.0.0.1:0"},
		{Enabled: true, Address: "127.0.0.1:70000"},
		{Enabled: true, Address: "127.0.0.1:abc"},
		{Enabled: true, Address: "[::1]:7890:1"},
	} {
		if err := ts.SetTeamProxy("alpha", bad); !errors.Is(err, ErrInvalidProxy) {
			t.Errorf("address %q: err = %v, want ErrInvalidProxy", bad.Address, err)
		}
	}
	if err := ts.SetTeamProxy("ghost", good); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
	if err := ts.SetTeamProxy("alpha", ProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	doc, _, err = ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p := doc.Teams[0].Proxy; p == nil || p.Enabled {
		t.Fatalf("proxy after explicit off: %+v", p)
	}
}

// TestProxyLegacyJSONCompat pins the old host/port document shape: it loads
// into Address, and the next write publishes only the new fields.
func TestProxyLegacyJSONCompat(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamFile, `{"schema_version":1,"teams":[{"Name":"alpha","Proxy":{"enabled":true,"host":"127.0.0.1","port":7890},"Template":[{"MemberID":"m1","Role":"coder","Status":"active"}]}]}`)
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := doc.Teams[0].Proxy
	if p == nil || !p.Enabled || p.Address != "127.0.0.1:7890" {
		t.Fatalf("legacy proxy should assemble into address, got %+v", p)
	}
	if err := ts.AddMember("alpha", MemberSlot{MemberID: "m2", Role: RoleTester, Status: MemberStatusActive}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(teamFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"schema_version":1,"teams":[{"Name":"alpha","Template":[{"MemberID":"m1","Role":"coder","AgentUserRef":"","Status":"active","Temporary":false},{"MemberID":"m2","Role":"tester","AgentUserRef":"","Status":"active","Temporary":false}],"DefaultAgentUserRef":"","proxy":{"enabled":true,"address":"127.0.0.1:7890"}}]}` {
		t.Fatalf("rewrite should migrate to the address form, got:\n%s", raw)
	}
}

// TestProxyUnmarshalJSONEmptyKeepsShape pins that a doc with neither host nor
// address stays address-less — the default applies at resolution, never at
// load, so an old "off" stays distinguishable.
func TestProxyUnmarshalJSONEmptyKeepsShape(t *testing.T) {
	var p ProxyConfig
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.Address != "" {
		t.Fatalf("bare-on must keep an empty address, got %+v", p)
	}
}

func TestTeamStoreSetMemberProxyOverride(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := ts.SetMemberProxyOverride("alpha", "m1", &on); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberProxyOverride("alpha", "ghost", &on); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("missing member: err = %v, want ErrMemberNotFound", err)
	}
	if err := ts.SetMemberProxyOverride("ghost", "m1", &on); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
	// nil override = inherit; verify the pointer round-trips through the file.
	if err := ts.SetMemberProxyOverride("alpha", "m1", nil); err != nil {
		t.Fatal(err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Teams[0].Template[0].ProxyEnabled; got != nil {
		t.Fatalf("nil override round-trip: %v", *got)
	}
}

// TestProxyAcceptsIPPortAddresses pins the A3 positive side of the §4.3
// contract: any literal IP:port is legal — IPv4, IPv6, the default address —
// while DNS names and stray whitespace are refused.
func TestProxyAcceptsIPPortAddresses(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	for _, good := range []string{
		DefaultProxyAddress,
		"10.0.0.1:80",
		"[::1]:8080",
		"0.0.0.0:1",
	} {
		if err := ts.SetTeamProxy("alpha", ProxyConfig{Enabled: true, Address: good}); err != nil {
			t.Errorf("address %q: err = %v, want nil", good, err)
		}
	}
	for _, bad := range []string{
		" 127.0.0.1:1",
		"127.0.0.1:1 ",
		"[::1]:8080:1",
		"::1:8080",
	} {
		if err := ts.SetTeamProxy("alpha", ProxyConfig{Enabled: true, Address: bad}); !errors.Is(err, ErrInvalidProxy) {
			t.Errorf("address %q: err = %v, want ErrInvalidProxy", bad, err)
		}
	}
}
