package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// The default atomic-FS surface is team: an unset key mounts the pair on team
// builds and leaves every other build's provider surface byte-for-byte alone.
func TestAtomicFSDefaultsToTeam(t *testing.T) {
	cfg := Default()
	if raw := cfg.Tools.AtomicFS; raw != "" {
		t.Fatalf("default raw atomic_fs = %q, want empty (the default is resolved, not written)", raw)
	}
	mode, ok := cfg.AtomicFSMode()
	if !ok || mode != AtomicFSTeam {
		t.Fatalf("AtomicFSMode() = (%q, %v), want (%q, true)", mode, ok, AtomicFSTeam)
	}
	if !cfg.AtomicFSSurfaceEnabled(true) {
		t.Fatal("the default must mount the pair on a team build")
	}
	if cfg.AtomicFSSurfaceEnabled(false) {
		t.Fatal("the default must leave an ordinary build's surface unchanged")
	}
}

func TestAtomicFSExplicitModes(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		team      bool
		wantMode  string
		wantOK    bool
		wantSurfa bool
	}{
		{raw: "off", team: true, wantMode: AtomicFSOff, wantOK: true, wantSurfa: false},
		{raw: "off", team: false, wantMode: AtomicFSOff, wantOK: true, wantSurfa: false},
		{raw: "team", team: true, wantMode: AtomicFSTeam, wantOK: true, wantSurfa: true},
		{raw: "team", team: false, wantMode: AtomicFSTeam, wantOK: true, wantSurfa: false},
		{raw: "all", team: true, wantMode: AtomicFSAll, wantOK: true, wantSurfa: true},
		{raw: "all", team: false, wantMode: AtomicFSAll, wantOK: true, wantSurfa: true},
		{raw: " TEAM ", team: true, wantMode: AtomicFSTeam, wantOK: true, wantSurfa: true},
		// A typo resolves to the default and reports ok=false, so the surface
		// cannot be widened by a misspelling on a non-team build.
		{raw: "nonsense", team: true, wantMode: AtomicFSTeam, wantOK: false, wantSurfa: true},
		{raw: "nonsense", team: false, wantMode: AtomicFSTeam, wantOK: false, wantSurfa: false},
	} {
		cfg := Default()
		if _, err := toml.Decode("[tools]\natomic_fs = \""+tc.raw+"\"\n", cfg); err != nil {
			t.Fatalf("decode %q: %v", tc.raw, err)
		}
		mode, ok := cfg.AtomicFSMode()
		if mode != tc.wantMode || ok != tc.wantOK {
			t.Errorf("atomic_fs=%q team=%v: AtomicFSMode() = (%q, %v), want (%q, %v)",
				tc.raw, tc.team, mode, ok, tc.wantMode, tc.wantOK)
		}
		if got := cfg.AtomicFSSurfaceEnabled(tc.team); got != tc.wantSurfa {
			t.Errorf("atomic_fs=%q team=%v: surface enabled = %v, want %v", tc.raw, tc.team, got, tc.wantSurfa)
		}
	}
}

// A nil Config is what a host with no loaded configuration passes; it must
// resolve to the same default rather than panicking or silently disabling the
// pair.
func TestAtomicFSModeOnNilConfig(t *testing.T) {
	var cfg *Config
	mode, ok := cfg.AtomicFSMode()
	if !ok || mode != AtomicFSTeam {
		t.Fatalf("nil config: AtomicFSMode() = (%q, %v), want (%q, true)", mode, ok, AtomicFSTeam)
	}
	if !cfg.AtomicFSSurfaceEnabled(true) || cfg.AtomicFSSurfaceEnabled(false) {
		t.Fatal("nil config must resolve to the team default")
	}
}
