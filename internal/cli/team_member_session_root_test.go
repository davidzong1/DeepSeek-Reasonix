package cli

// Member session root binding on both session axes: the file name is fixed, but
// the directory once came from the launching process's CWD, so a window opened
// elsewhere silently started an empty history. These tests pin the probe.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// memberSessionFixture pins a user state root and returns the stable session
// root a member's file belongs in for the given workspace.
func memberSessionFixture(t *testing.T) (projectRoot, stableDir string) {
	t.Helper()
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	projectRoot = t.TempDir()
	stableDir = config.ProjectSessionDir(projectRoot)
	if stableDir == "" {
		t.Fatal("the fixture workspace must resolve a stable session root")
	}
	return projectRoot, stableDir
}

// memberV3Controller builds the v3-exclusive counterpart of memberTestController:
// a controller whose session service is real, so it runs on the versioned store
// and its identity is the service's SessionRef rather than a path. Mirroring
// internal/serve/session_export_test.go keeps this fixture honest — a stub
// without a service would leave ExclusiveSession false and silently re-test the
// legacy axis.
func memberV3Controller(t *testing.T, sys string) *control.Controller {
	t.Helper()
	return memberV3ControllerWith(t, sys, nil)
}

// memberV3ControllerWith is the same fixture with a turn runner attached, so a
// test can drive the controller through a submitted turn rather than only
// inspecting the binding it ended up with.
func memberV3ControllerWith(t *testing.T, sys string, rec agent.Runner) *control.Controller {
	t.Helper()
	service, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "member"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, nil, agent.NewSession(sys), agent.Options{}, event.Discard)
	c := control.New(control.Options{
		Executor: exec, SessionDir: t.TempDir(), Label: "member",
		SystemPrompt: sys, DisableColdResumePrune: true,
		SessionService: service, SessionRuntime: runtime, ExclusiveSession: true,
		Runner: rec, Sink: event.Discard,
	})
	if !c.UsesExclusiveSession() {
		t.Fatal("the fixture must produce a v3-exclusive controller")
	}
	t.Cleanup(c.Close)
	return c
}

// memberTurnController is the legacy-axis member fixture with a turn runner, so
// the fresh branch can be driven to a submitted turn on both axes.
func memberTurnController(t *testing.T, sys string, rec agent.Runner) *control.Controller {
	t.Helper()
	exec := agent.New(nil, nil, agent.NewSession(sys), agent.Options{}, event.Discard)
	c := control.New(control.Options{
		Executor: exec, SessionDir: t.TempDir(), Label: "member",
		SystemPrompt: sys, DisableColdResumePrune: true,
		Runner: rec, Sink: event.Discard,
	})
	t.Cleanup(c.Close)
	return c
}

// seedMemberSession writes one member's session file at path.
func seedMemberSession(t *testing.T, path string) {
	t.Helper()
	seedMemberSessionText(t, path, "member-sys", "remembered user", "remembered reply")
}

// seedMemberSessionText writes a plain-JSON member session with the given body,
// so a test can tell two same-named candidates apart.
func seedMemberSessionText(t *testing.T, path, sys, user, reply string) {
	t.Helper()
	s := agent.NewSession(sys)
	s.Add(provider.Message{Role: provider.RoleUser, Content: user})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: reply})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
}

// memberSessionAxis is one of the two controllers a member backend can be built
// around. Every root/identity test runs on both, because the probe order and the
// create target are shared but the identity assertion is not.
type memberSessionAxis struct {
	name string
	v3   bool
	ctrl func(*testing.T) *control.Controller
}

func memberSessionAxes() []memberSessionAxis {
	return []memberSessionAxis{
		{name: "legacy", ctrl: func(t *testing.T) *control.Controller { return memberTestController(t, "member-sys") }},
		{name: "v3-exclusive", v3: true, ctrl: func(t *testing.T) *control.Controller { return memberV3Controller(t, "member-sys") }},
	}
}

// assertMemberIdentity checks that the adopted file became the controller's
// execution identity on whichever axis this controller runs on.
func assertMemberIdentity(t *testing.T, ctrl *control.Controller, v3 bool, path string) {
	t.Helper()
	if v3 {
		// The path is an import source, not an identity: a v3 binding that still
		// reported the legacy path as live would let compatibility helpers write
		// sidecars beside a frozen artifact.
		if got := ctrl.SessionPath(); got != "" {
			t.Fatalf("v3 controller session path = %q, want the path to be cleared", got)
		}
		ref, ok := ctrl.SessionRef()
		if !ok {
			t.Fatal("a v3 binding must expose a SessionRef")
		}
		if ref.HostID == "" || ref.SessionID == "" {
			t.Fatalf("incomplete session ref %+v", ref)
		}
		return
	}
	if got := ctrl.SessionPath(); got != path {
		t.Fatalf("controller session path = %q, want %q", got, path)
	}
}

// TestMemberSessionBindingAdoptsStableRootHistory is the cross-directory case:
// the member's file lives under the workspace's stable root while the freshly
// built controller points at a different (CWD-derived) directory, and the probe
// must still adopt the real history instead of starting empty.
//
// The same history must be adopted whether it was written before or after the
// store moved to its versioned form: a version bump relocates <root>/sessions to
// <root>/sessions-v4 (session.RootForLegacyDir), so a member written by the older
// build lives only under the logical root while this build resolves the versioned
// one. Adopting is read-only either way — the file is never moved to the
// canonical root, so the create target stays where this version reads.
func TestMemberSessionBindingAdoptsStableRootHistory(t *testing.T) {
	const name = "team-alpha-lead.json"

	for _, axis := range memberSessionAxes() {
		for _, tc := range []struct {
			name string
			// dir picks the root to seed from the subtest's own fixture: the logical
			// stable root, or its versioned form.
			dir func(stableDir string) string
		}{
			{"logical stable root", func(s string) string { return s }},
			{"versioned stable root", func(s string) string { return session.RootForLegacyDir(s) }},
		} {
			t.Run(axis.name+"/"+tc.name, func(t *testing.T) {
				// A fixture per subtest: the roots are on disk, so a shared fixture
				// would let the previous subtest's file satisfy this one's probe and
				// silently invert which root is being exercised.
				projectRoot, stableDir := memberSessionFixture(t)
				versioned := session.RootForLegacyDir(stableDir)
				ctrl := axis.ctrl(t)
				if ctrl.SessionDir() == stableDir {
					t.Fatal("the fixture must give the controller a different session dir")
				}
				seedDir := tc.dir(stableDir)
				stablePath := filepath.Join(seedDir, name)
				seedMemberSession(t, stablePath)
				before, err := os.ReadFile(stablePath)
				if err != nil {
					t.Fatal(err)
				}

				roots, create := memberSessionRoots(ctrl, projectRoot)
				if len(roots) < 2 {
					t.Fatalf("roots = %v, want the controller dir and the stable root", roots)
				}
				// The create target is the canonical root of this controller's own
				// axis: a v3 store writes to the versioned root, a legacy file *is*
				// the logical path.
				wantCreate := stableDir
				if axis.v3 {
					wantCreate = versioned
				}
				if create != wantCreate {
					t.Fatalf("create target = %q, want %q", create, wantCreate)
				}
				// The probe must reach the versioned root before the logical one: a
				// stale copy in the logical root may not shadow the live file.
				index := func(root string) int {
					for i, got := range roots {
						if got == root {
							return i
						}
					}
					return -1
				}
				if i, j := index(versioned), index(stableDir); i < 0 || j < 0 || i > j {
					t.Fatalf("roots = %v, want the versioned root %q before the logical %q", roots, versioned, stableDir)
				}

				path, fresh, err := bindMemberSession(ctrl, name, roots, create)
				if err != nil {
					t.Fatalf("bindMemberSession: %v", err)
				}
				if fresh {
					t.Fatal("an existing history must not be reported as a first entry")
				}
				if path != stablePath {
					t.Fatalf("bound path = %q, want %q", path, stablePath)
				}
				assertMemberIdentity(t, ctrl, axis.v3, stablePath)
				var found bool
				for _, msg := range ctrl.History() {
					if strings.Contains(msg.Content, "remembered user") {
						found = true
					}
				}
				if !found {
					t.Fatalf("the adopted history is missing its messages: %+v", ctrl.History())
				}
				// Adoption is a read: resuming must not relocate or rewrite the file
				// into the canonical root, so the history stays byte-identical where
				// its own version wrote it.
				after, err := os.ReadFile(stablePath)
				if err != nil {
					t.Fatalf("the adopted history was moved or deleted: %v", err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("resuming rewrote the adopted session file")
				}
			})
		}
	}
}

// TestMemberSessionBindingPrefersVersionedRootOnSameName pins the priority half
// of the probe order: when a stale copy of the same file is still sitting in the
// logical root while the current build's versioned root holds the live one, the
// versioned root is the authority — adopting the stale copy would silently
// roll the member's history back a version.
func TestMemberSessionBindingPrefersVersionedRootOnSameName(t *testing.T) {
	projectRoot, stableDir := memberSessionFixture(t)
	const name = "team-alpha-lead.json"
	versioned := session.RootForLegacyDir(stableDir)
	if versioned == stableDir {
		t.Fatal("the fixture needs two distinct roots")
	}
	seedMemberSessionText(t, filepath.Join(stableDir, name), "member-sys", "stale user", "stale reply")
	seedMemberSessionText(t, filepath.Join(versioned, name), "member-sys", "current user", "current reply")

	for _, axis := range memberSessionAxes() {
		t.Run(axis.name, func(t *testing.T) {
			ctrl := axis.ctrl(t)
			roots, create := memberSessionRoots(ctrl, projectRoot)
			path, fresh, err := bindMemberSession(ctrl, name, roots, create)
			if err != nil {
				t.Fatalf("bindMemberSession: %v", err)
			}
			if fresh {
				t.Fatal("a candidate exists under the versioned root and must be adopted")
			}
			if want := filepath.Join(versioned, name); path != want {
				t.Fatalf("bound path = %q, want the versioned root %q", path, want)
			}
			assertMemberIdentity(t, ctrl, axis.v3, path)
			var stale, current bool
			for _, msg := range ctrl.History() {
				stale = stale || strings.Contains(msg.Content, "stale user")
				current = current || strings.Contains(msg.Content, "current user")
			}
			if !current {
				t.Fatalf("the versioned root's history was not adopted: %+v", ctrl.History())
			}
			if stale {
				t.Fatal("the logical root's stale copy was adopted over the versioned one")
			}
		})
	}
}

// TestMemberSessionBindingTotalMissCreatesUnderCanonicalRoot pins the first-entry
// branch: nothing to adopt anywhere means fresh, and the new file is aimed at the
// canonical root of this controller's axis rather than whichever directory this
// process sits in — with no existing file in that store overwritten on the way.
func TestMemberSessionBindingTotalMissCreatesUnderCanonicalRoot(t *testing.T) {
	projectRoot, stableDir := memberSessionFixture(t)
	const name = "team-alpha-lead.json"
	versioned := session.RootForLegacyDir(stableDir)

	for _, axis := range memberSessionAxes() {
		t.Run(axis.name, func(t *testing.T) {
			wantDir := stableDir
			if axis.v3 {
				wantDir = versioned
			}
			sentinel := filepath.Join(wantDir, "unrelated.json")
			if err := os.MkdirAll(wantDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sentinel, []byte("do not overwrite me"), 0o600); err != nil {
				t.Fatal(err)
			}

			ctrl := axis.ctrl(t)
			roots, create := memberSessionRoots(ctrl, projectRoot)
			path, fresh, err := bindMemberSession(ctrl, name, roots, create)
			if err != nil {
				t.Fatalf("bindMemberSession: %v", err)
			}
			if !fresh {
				t.Fatal("a total miss is the member's first entry and must report fresh")
			}
			if want := filepath.Join(wantDir, name); path != want {
				t.Fatalf("fresh path = %q, want the canonical root %q", path, want)
			}
			if axis.v3 {
				// The v3 binding commits a session in the store, not a file at the
				// legacy path, so the reported path stays a probe result only.
				if _, ok := ctrl.SessionRef(); !ok {
					t.Fatal("a v3 first entry must bind a SessionRef")
				}
				if got := ctrl.SessionPath(); got != "" {
					t.Fatalf("v3 controller session path = %q, want the path to be cleared", got)
				}
			} else if got := ctrl.SessionPath(); got != path {
				t.Fatalf("controller session path = %q, want %q", got, path)
			}
			// The fresh binding is a path, not a write: nothing was created or clobbered.
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("a fresh binding must not write the session file, stat err = %v", err)
			}
			got, err := os.ReadFile(sentinel)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "do not overwrite me" {
				t.Fatalf("an unrelated file in the store was rewritten: %q", got)
			}
		})
	}
}

// TestMemberSessionBindingCorruptCandidateIsHardError pins the missing/corrupt
// distinction: a candidate that exists but cannot be read is an error, never a
// silent fall-through to an empty session — and the bytes are left for the
// operator to repair. The versioned root is probed before its logical sibling on
// both axes, so a corrupt file placed there is reached first.
func TestMemberSessionBindingCorruptCandidateIsHardError(t *testing.T) {
	projectRoot, stableDir := memberSessionFixture(t)
	const name = "team-alpha-lead.json"

	for _, axis := range memberSessionAxes() {
		for _, tc := range []struct {
			name string
			root func(stableDir, ctrlDir string) string
		}{
			{"versioned stable root", func(s, _ string) string { return session.RootForLegacyDir(s) }},
			{"logical stable root", func(s, _ string) string { return s }},
			{"versioned controller root", func(_, c string) string { return session.RootForLegacyDir(c) }},
		} {
			t.Run(axis.name+"/"+tc.name, func(t *testing.T) {
				ctrl := axis.ctrl(t)
				beforeRef, _ := ctrl.SessionRef()
				dir := tc.root(stableDir, ctrl.SessionDir())
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, name)
				corrupt := []byte("}{ not a session")
				if err := os.WriteFile(path, corrupt, 0o600); err != nil {
					t.Fatal(err)
				}
				roots, create := memberSessionRoots(ctrl, projectRoot)
				if _, _, err := bindMemberSession(ctrl, name, roots, create); err == nil {
					t.Fatal("a candidate that exists but cannot be read must be a hard error")
				}
				// A refused binding must leave the controller on whatever identity it
				// already had, never on the unreadable candidate.
				if got := ctrl.SessionPath(); got != "" {
					t.Fatalf("a refused binding must not rebind the controller, got %q", got)
				}
				afterRef, _ := ctrl.SessionRef()
				if afterRef != beforeRef {
					t.Fatalf("a refused binding changed the session identity: %+v -> %+v", beforeRef, afterRef)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(corrupt) {
					t.Fatalf("the unreadable file was rewritten: %q", got)
				}
			})
		}
	}
}

// TestMemberSessionBindingFreshBindsV3AuthorityLease covers the half of the
// first-entry branch that the root/identity tests above cannot see: binding a
// fresh session must leave the controller able to actually *execute* its first
// turn. Binding the identity is not the same as being admitted — a member that
// adopts the right store but is refused on its first turn has lost the session
// just as surely as one that started empty.
//
// The two axes reach that outcome differently, so each asserts its own
// post-condition. A legacy controller executes the path it was resumed with, so
// the fresh binding must be backed by a real path lease held by the runtime. A
// v3-exclusive controller has no path identity at all: the file was an import
// source and the identity is the store's SessionRef, so there is no path lease
// to hold and admission is gated on the runtime phase instead. Copying the
// legacy lease assertion onto v3 would pin a lease the store never grants.
func TestMemberSessionBindingFreshBindsV3AuthorityLease(t *testing.T) {
	projectRoot, _ := memberSessionFixture(t)
	const name = "team-alpha-lead.json"

	for _, axis := range []struct {
		name string
		v3   bool
		ctrl func(*testing.T, agent.Runner) *control.Controller
	}{
		{
			name: "legacy",
			ctrl: func(t *testing.T, rec agent.Runner) *control.Controller {
				return memberTurnController(t, "member-sys", rec)
			},
		},
		{
			name: "v3-exclusive", v3: true,
			ctrl: func(t *testing.T, rec agent.Runner) *control.Controller {
				return memberV3ControllerWith(t, "member-sys", rec)
			},
		},
	} {
		t.Run(axis.name, func(t *testing.T) {
			rec := newRecorderTurnRunner()
			ctrl := axis.ctrl(t, rec)

			roots, create := memberSessionRoots(ctrl, projectRoot)
			path, fresh, err := bindMemberSession(ctrl, name, roots, create)
			if err != nil {
				t.Fatalf("bindMemberSession: %v", err)
			}
			if !fresh {
				t.Fatal("an empty store is the member's first entry")
			}
			beforeRef, _ := ctrl.SessionRef()
			assertMemberIdentity(t, ctrl, axis.v3, path)

			// The authority binding is the same call the member backend makes, run on
			// the v3 axis with a nil lease on purpose: there is no path to lease, and
			// it must still install the session-transition handler.
			wl, err := bindMemberSessionAuthority(ctrl, path, true)
			if err != nil {
				t.Fatalf("bindMemberSessionAuthority: %v", err)
			}
			t.Cleanup(wl.Close)

			// The binding must not have re-identified the controller: a fresh member
			// that came out of authority binding on a different session than it bound
			// would execute someone else's history.
			afterBindRef, _ := ctrl.SessionRef()
			if afterBindRef != beforeRef {
				t.Fatalf("authority binding changed the session identity: %+v -> %+v", beforeRef, afterBindRef)
			}
			assertMemberIdentity(t, ctrl, axis.v3, path)

			if err := ctrl.SubmitUserTurnOrError("first turn", "first turn"); err != nil {
				t.Fatalf("a bound first-entry session must admit its first turn: %v", err)
			}
			waitForTurns(t, rec, 1)
			waitMemberTurnDone(t, ctrl)

			// Identity is stable across the turn, and the lease polarity is the axis's
			// own: a legacy turn runs under a held path lease, a v3 turn does not hold
			// one because its identity is the SessionRef rather than the path.
			finalRef, _ := ctrl.SessionRef()
			if finalRef != beforeRef {
				t.Fatalf("the first turn changed the session identity: %+v -> %+v", beforeRef, finalRef)
			}
			assertMemberIdentity(t, ctrl, axis.v3, path)

			held := agent.SessionLeaseHeldByCurrentRuntime(path)
			if axis.v3 {
				if held {
					t.Fatalf("a v3-exclusive session must not hold a path lease: %q is an import source, not the identity", path)
				}
				// No path lease means no path-scoped write authority: the generation
				// must stay unclaimed rather than report a forged one. Reading it is
				// nil-safe, so a zero here is a real answer and not a missing binding.
				if gen := ctrl.WriteAuthorityGeneration(); gen != 0 {
					t.Fatalf("v3 write authority generation = %d, want none: the v3 store grants no path lease", gen)
				}
				return
			}
			if !held {
				t.Fatalf("a legacy member turn must run under a held path lease for %q", path)
			}
			if gen := ctrl.WriteAuthorityGeneration(); gen == 0 {
				t.Fatal("a legacy member turn must run under a live write authority")
			}
		})
	}
}

// TestTeamRestoreRefusesCorruptSelectionWithoutTouchingFile pins the other half
// of the same contract on the TUI seam: a persisted member window that cannot
// be read refuses the restore instead of falling back to a member the user did
// not choose, and the preference file is left byte-identical.
func TestTeamRestoreRefusesCorruptSelectionWithoutTouchingFile(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	selection := filepath.Join(cwd, ".reasonix", "team", "session", "alpha.json")
	if err := os.MkdirAll(filepath.Dir(selection), 0o755); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{ not a selection")
	if err := os.WriteFile(selection, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	m := openTeamOverlay(t)
	if m.teamPick.session.active {
		t.Fatal("an unreadable selection must not open a fallback member's window")
	}
	if got := m.teamPick.refusal; got != sessionSelectionRefusal {
		t.Fatalf("refusal = %q, want %q", got, sessionSelectionRefusal)
	}
	got, err := os.ReadFile(selection)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("the refused selection was rewritten: %q", got)
	}
}
