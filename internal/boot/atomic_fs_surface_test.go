package boot

// The team atomic-FS surface (tools.atomic_fs) swaps the legacy file trio for
// the atomic pair on team builds. Boot resolves the mode from the role a build
// already carries, so every host derives the same answer.

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestAtomicFSSurfaceAddsTheAtomicPair(t *testing.T) {
	got := atomicFSSurface(configForAtomicFS(t, config.AtomicFSTeam), true)
	if got == nil {
		t.Fatal("team mode must produce an allowlist for a team build")
	}
	for _, name := range []string{AtomicReadToolName, AtomicWriteToolName} {
		if !slices.Contains(got, name) {
			t.Fatalf("team surface is missing %s: %v", name, got)
		}
	}
	// Everything on the unified surface stays: the substitution is a separate
	// step that only runs once the pair is known to be registered.
	for _, name := range UnifiedProviderToolNames() {
		if !slices.Contains(got, name) {
			t.Fatalf("team surface dropped %s: %v", name, got)
		}
	}
}

// TestAtomicPairSubstitutesTheLegacyTrio pins the other half: the trio leaves the
// surface only when its replacement is registered, so a build that never mounted
// the pair keeps file access.
func TestAtomicPairSubstitutesTheLegacyTrio(t *testing.T) {
	newReg := func(names ...string) *tool.Registry {
		reg := tool.NewRegistry()
		for _, name := range append([]string{"bash", "read_file", "write_file", "edit_file", "compress", "use_capability"}, names...) {
			if tl, ok := tool.LookupBuiltin(name); ok {
				reg.Add(tl)
			} else {
				reg.Add(&effectExtraTool{name: name})
			}
		}
		return reg
	}
	surface := atomicFSSurface(configForAtomicFS(t, config.AtomicFSTeam), true)

	// Pair mounted: the trio is substituted away.
	withPair := newReg(AtomicReadToolName, AtomicWriteToolName)
	applyUnifiedProviderToolSurface(withPair, []tool.Tool{
		&effectExtraTool{name: AtomicReadToolName}, &effectExtraTool{name: AtomicWriteToolName},
	}, surface, "")
	got := toolSchemaNames(withPair.Schemas())
	for _, name := range []string{AtomicReadToolName, AtomicWriteToolName} {
		if !slices.Contains(got, name) {
			t.Fatalf("mounted pair missing %s: %v", name, got)
		}
	}
	for _, name := range []string{"read_file", "write_file", "edit_file"} {
		if slices.Contains(got, name) {
			t.Fatalf("mounted pair must substitute %s: %v", name, got)
		}
	}

	// Pair not mounted: the trio stays, so the surface never loses file access.
	withoutPair := newReg()
	applyUnifiedProviderToolSurface(withoutPair, nil, surface, "")
	got = toolSchemaNames(withoutPair.Schemas())
	for _, name := range []string{"read_file", "write_file", "edit_file"} {
		if !slices.Contains(got, name) {
			t.Fatalf("unmounted pair must leave %s in place: %v", name, got)
		}
	}
}

func TestAtomicFSSurfaceModeMatrix(t *testing.T) {
	for _, tc := range []struct {
		mode string
		team bool
		want bool
	}{
		{mode: config.AtomicFSOff, team: true, want: false},
		{mode: config.AtomicFSOff, team: false, want: false},
		{mode: config.AtomicFSTeam, team: true, want: true},
		{mode: config.AtomicFSTeam, team: false, want: false},
		{mode: config.AtomicFSAll, team: true, want: true},
		{mode: config.AtomicFSAll, team: false, want: true},
		// The default is team: an unset key and an unrecognized one both resolve
		// to it, so a team build gets the pair and a non-team build keeps the
		// legacy trio. A typo therefore cannot WIDEN a non-team surface.
		{mode: "", team: true, want: true},
		{mode: "", team: false, want: false},
		{mode: "TEAM ", team: true, want: true},
		{mode: "nonsense", team: true, want: true},
		{mode: "nonsense", team: false, want: false},
	} {
		cfg := configForAtomicFS(t, tc.mode)
		got := atomicFSSurface(cfg, tc.team) != nil
		if got != tc.want {
			t.Errorf("mode=%q team=%v: enabled=%v, want %v", tc.mode, tc.team, got, tc.want)
		}
	}
}

// TestAtomicFSSurfaceIsTheOnlySurfaceThatChanges pins the "team build" signal:
// the team role is set by the team builder and nowhere else, so an ordinary
// session cannot accidentally lose read_file/write_file/edit_file.
func TestAtomicFSSurfaceIsTheOnlySurfaceThatChanges(t *testing.T) {
	cfg := configForAtomicFS(t, config.AtomicFSTeam)
	if providerVisibleTools(Options{TeamRole: "member"}, cfg) == nil {
		t.Fatal("a member build must take the team surface")
	}
	if providerVisibleTools(Options{}, cfg) != nil {
		t.Fatal("an ordinary build must keep the legacy surface")
	}
	// An explicit host allowlist wins outright, mode or not.
	explicit := []string{"bash"}
	if got := providerVisibleTools(Options{ProviderVisibleTools: explicit, TeamRole: "member"}, cfg); !reflect.DeepEqual(got, explicit) {
		t.Fatalf("explicit allowlist was overridden: %v", got)
	}
}

// TestProviderVisibleSurfaceNarrowsWithoutRevealing pins the safety property the
// allowlist rests on: it can select a registered compile-time built-in that the
// unified surface does not carry (how the atomic pair is mounted), but it can
// never reveal a tool the boot did not register — an MCP server, a plugin tool.
func TestProviderVisibleSurfaceNarrowsWithoutRevealing(t *testing.T) {
	reg := tool.NewRegistry()
	for _, name := range []string{"bash", "read_file", "write_file", "edit_file", "multi_edit", "use_capability"} {
		if tl, ok := tool.LookupBuiltin(name); ok {
			reg.Add(tl)
		}
	}
	reg.Add(&effectExtraTool{name: "mcp__server__write"})

	applyUnifiedProviderToolSurface(reg, nil, []string{"bash", "multi_edit", "mcp__server__write", "no_such_tool"}, "")

	var names []string
	for _, s := range reg.Schemas() {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"bash", "multi_edit"}) {
		t.Fatalf("surface = %v, want [bash multi_edit]", names)
	}
	// Nothing was unregistered: use_capability and replay still reach the trio.
	for _, name := range []string{"read_file", "write_file", "edit_file"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("%s must stay registered after a surface narrowing", name)
		}
	}
}

// TestTeamAtomicSurfaceReachesTheProvider drives the real stack: a team-role
// build with tools.atomic_fs=team must present the pair to the provider and drop
// the trio, and an ordinary build of the same config must be byte-identical to
// today's surface.
func TestTeamAtomicSurfaceReachesTheProvider(t *testing.T) {
	rec := effectExtraToolStage(t)
	writeFile(t, ".", "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[tools]
atomic_fs = "team"

[[providers]]
name = "test-model"
kind = "`+effectExtraToolProviderKind+`"
model = "x"
`)
	extra := []tool.Tool{&effectExtraTool{name: AtomicReadToolName}, &effectExtraTool{name: AtomicWriteToolName}}

	teamReq := effectRunWithOptions(t, rec, Options{ExtraTools: extra, TeamRole: "member"})
	teamNames := toolSchemaNames(teamReq.Tools)
	for _, name := range []string{AtomicReadToolName, AtomicWriteToolName} {
		if !slices.Contains(teamNames, name) {
			t.Fatalf("team surface missing %s: %v", name, teamNames)
		}
	}
	for _, name := range []string{"read_file", "write_file", "edit_file"} {
		if slices.Contains(teamNames, name) {
			t.Fatalf("team surface still carries %s: %v", name, teamNames)
		}
	}

	// The same config without the team role keeps the legacy surface.
	plainReq := effectRunWithOptions(t, rec, Options{})
	if got := toolSchemaNames(plainReq.Tools); !reflect.DeepEqual(got, unifiedBootToolNames()) {
		t.Fatalf("non-team surface changed\ngot  %v\nwant %v", got, unifiedBootToolNames())
	}
}

// effectRunWithOptions is effectRunWithExtraTools with the boot options the test
// needs (the team role, the atomic pair as host tools) instead of just ExtraTools.
func effectRunWithOptions(t *testing.T, rec *effectRecordingProvider, opts Options) provider.Request {
	t.Helper()
	before := len(rec.requests())
	opts.Sink = event.Discard
	ctrl, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "reply ok"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := rec.requests()[before:]
	if len(reqs) == 0 {
		t.Fatal("no request reached the provider boundary")
	}
	return reqs[0]
}

// configForAtomicFS builds the config a boot would load for one atomic_fs mode:
// the real fixture config with the mode applied, so the surface is resolved the
// same way Build resolves it.
func configForAtomicFS(t *testing.T, mode string) *config.Config {
	t.Helper()
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[tools]
atomic_fs = "`+mode+`"

[[providers]]
name = "test-model"
kind = "`+effectExtraToolProviderKind+`"
model = "x"
`)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func TestAtomicFSSurfacePrefixIsStableAcrossTurns(t *testing.T) {
	cfg := configForAtomicFS(t, config.AtomicFSTeam)
	first := atomicFSSurface(cfg, true)
	second := atomicFSSurface(cfg, true)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("surface is not stable across assemblies:\n%v\n%v", first, second)
	}
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatal("surface order changed between assemblies")
	}
}
