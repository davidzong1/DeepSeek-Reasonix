package team

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestOwnerStore(t *testing.T) *OwnerStore {
	t.Helper()
	s, err := NewOwnerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// seedSessionUnit writes a complete session artifact set for one member
// transcript: the transcript itself plus every sidecar a real session owns.
func seedSessionUnit(t *testing.T, dir, teamName, memberID string) OwnerUnit {
	t.Helper()
	transcript := filepath.Join(dir, mustSessionFile(t, teamName, memberID))
	writeFile(t, transcript, `{"schema_version":1,"messages":[]}`)
	unit := OwnerUnitFor(transcript)
	for _, sidecar := range unit.Files {
		if sidecar == transcript {
			continue
		}
		writeFile(t, sidecar, "sidecar:"+filepath.Base(sidecar))
	}
	for _, d := range unit.Dirs {
		writeFile(t, filepath.Join(d, "artifact.json"), "artifact:"+filepath.Base(d))
		writeFile(t, filepath.Join(d, "nested", "deep.json"), "deep")
	}
	for _, e := range unit.Ephemeral {
		writeFile(t, e, "ephemeral")
	}
	return unit
}

func mustSessionFile(t *testing.T, teamName, memberID string) string {
	t.Helper()
	name, err := MemberSessionFile(teamName, memberID)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerStoreInitCreatesConfinedDirAndMeta(t *testing.T) {
	s := newTestOwnerStore(t)
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	paths, meta, err := s.Init(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.Root(), "team-a", "coder-1")
	if paths.Dir != want {
		t.Fatalf("owner dir = %q, want %q", paths.Dir, want)
	}
	if meta.Revision != 1 || meta.SchemaVersion != SchemaVersion {
		t.Fatalf("meta = %+v, want revision 1 at schema %d", meta, SchemaVersion)
	}
	for _, dir := range []string{filepath.Join(s.Root(), "team-a"), paths.Dir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %o, want 0700", dir, info.Mode().Perm())
		}
	}
	info, err := os.Stat(paths.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("meta mode = %o, want 0600", info.Mode().Perm())
	}
	// Init is idempotent: a second call keeps the revision and the creation stamp.
	_, again, err := s.Init(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != meta.Revision || again.CreatedAt != meta.CreatedAt {
		t.Fatalf("second Init rewrote metadata: %+v vs %+v", again, meta)
	}
}

func TestOwnerStoreInitTightensLooseDirMode(t *testing.T) {
	s := newTestOwnerStore(t)
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	loose := filepath.Join(s.Root(), "team-a", "coder-1")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Init(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(loose)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want the existing dir tightened to 0700", info.Mode().Perm())
	}
}

func TestOwnerStoreRejectsUnsafeAndReservedIDs(t *testing.T) {
	s := newTestOwnerStore(t)
	for _, tc := range []struct {
		name string
		key  OwnerKey
		want error
	}{
		{"empty member", OwnerKey{TeamID: "t", MemberID: ""}, ErrInvalidOwnerKey},
		{"empty team", OwnerKey{TeamID: "", MemberID: "m"}, ErrInvalidOwnerKey},
		{"separator", OwnerKey{TeamID: "t", MemberID: "a/b"}, ErrInvalidOwnerKey},
		{"backslash", OwnerKey{TeamID: "t", MemberID: `a\b`}, ErrInvalidOwnerKey},
		{"dotdot", OwnerKey{TeamID: "t", MemberID: ".."}, ErrInvalidOwnerKey},
		{"dot", OwnerKey{TeamID: "t", MemberID: "."}, ErrInvalidOwnerKey},
		{"control", OwnerKey{TeamID: "t", MemberID: "a\x01b"}, ErrInvalidOwnerKey},
		{"leading dot", OwnerKey{TeamID: "t", MemberID: ".hidden"}, ErrInvalidOwnerKey},
		{"trailing dot", OwnerKey{TeamID: "t", MemberID: "m."}, ErrInvalidOwnerKey},
		{"windows device", OwnerKey{TeamID: "t", MemberID: "nul"}, ErrInvalidOwnerKey},
		{"windows device with ext", OwnerKey{TeamID: "t", MemberID: "com1.json"}, ErrInvalidOwnerKey},
		{"reserved registry file", OwnerKey{TeamID: "t", MemberID: AgentUsersFile}, ErrOwnerReserved},
		{"reserved context tree", OwnerKey{TeamID: TeamFile, MemberID: "m"}, ErrOwnerReserved},
		{"reserved blackboard", OwnerKey{TeamID: "t", MemberID: BlackboardDir}, ErrOwnerReserved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.Init(context.Background(), tc.key); !errors.Is(err, tc.want) {
				t.Fatalf("Init(%+v) err = %v, want %v", tc.key, err, tc.want)
			}
			if _, err := s.Paths(tc.key); !errors.Is(err, tc.want) {
				t.Fatalf("Paths(%+v) err = %v, want %v", tc.key, err, tc.want)
			}
		})
	}
	// A team id that merely contains a dot is not reserved; only the exact
	// entry names the root already owns are.
	if _, _, err := s.Init(context.Background(), OwnerKey{TeamID: "team.v2", MemberID: "m"}); err != nil {
		t.Fatalf("a dotted team id should be accepted: %v", err)
	}
	// A team literally named team.json would alias the registry file, so it is
	// refused rather than silently sharing the path.
	if _, _, err := s.Init(context.Background(), OwnerKey{TeamID: TeamFile, MemberID: "m"}); !errors.Is(err, ErrOwnerReserved) {
		t.Fatalf("team id %q err = %v, want ErrOwnerReserved", TeamFile, err)
	}
}

func TestOwnerStoreRejectsSymlinkedComponents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outside bool
	}{
		{"link escaping the root", true},
		{"link staying inside the root", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			s, err := NewOwnerStore(base)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(base, "real")
			if tc.outside {
				target = t.TempDir()
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(base, "team-a")); err != nil {
				t.Fatal(err)
			}
			key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
			if _, _, err := s.Init(context.Background(), key); !errors.Is(err, ErrOwnerSymlink) {
				t.Fatalf("Init through a symlinked team dir err = %v, want ErrOwnerSymlink", err)
			}
			if _, err := s.Delete(key); !errors.Is(err, ErrOwnerSymlink) {
				t.Fatalf("Delete through a symlinked team dir err = %v, want ErrOwnerSymlink", err)
			}
			// Nothing may have been created through the link.
			if _, err := os.Stat(filepath.Join(target, "coder-1")); !os.IsNotExist(err) {
				t.Fatalf("owner dir leaked through the symlink: %v", err)
			}
		})
	}
}

func TestOwnerStoreRejectsSymlinkedMemberDir(t *testing.T) {
	base := t.TempDir()
	s, _ := NewOwnerStore(base)
	if err := os.MkdirAll(filepath.Join(base, "team-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "team-a", "coder-1")); err != nil {
		t.Fatal(err)
	}
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	if _, _, err := s.Init(context.Background(), key); !errors.Is(err, ErrOwnerSymlink) {
		t.Fatalf("Init through a symlinked member dir err = %v, want ErrOwnerSymlink", err)
	}
	if _, err := s.Delete(key); !errors.Is(err, ErrOwnerSymlink) {
		t.Fatalf("Delete through a symlinked member dir err = %v, want ErrOwnerSymlink", err)
	}
	if _, err := os.Stat(filepath.Join(outside, ownerMetaFile)); !os.IsNotExist(err) {
		t.Fatalf("metadata leaked through the symlink: %v", err)
	}
}

func TestOwnerStoreInitOnAbsentRoot(t *testing.T) {
	base := t.TempDir()
	s, err := NewOwnerStore(filepath.Join(base, "deep", "team"))
	if err != nil {
		t.Fatal(err)
	}
	key := OwnerKey{TeamID: "t", MemberID: "m"}
	paths, _, err := s.Init(context.Background(), key)
	if err != nil {
		t.Fatalf("Init on an absent store root err = %v", err)
	}
	if _, err := os.Stat(paths.Meta); err != nil {
		t.Fatalf("metadata missing after Init: %v", err)
	}
	// Read-only entry points stay quiet on an absent root instead of failing.
	if got, err := s.Delete(OwnerKey{TeamID: "t2", MemberID: "m2"}); err != nil || got != "" {
		t.Fatalf("Delete on an absent owner = (%q, %v), want (\"\", nil)", got, err)
	}
	if err := s.SweepOwnerTrash(OwnerKey{TeamID: "t2", MemberID: "m2"}); err != nil {
		t.Fatalf("SweepOwnerTrash on an absent bucket err = %v", err)
	}
}

func TestOwnerStoreMetaOfAbsentOwnerIsNotFound(t *testing.T) {
	s := newTestOwnerStore(t)
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	if _, err := s.Meta(key); !errors.Is(err, ErrOwnerNotFound) {
		t.Fatalf("Meta of an absent owner err = %v, want ErrOwnerNotFound", err)
	}
	if _, _, err := s.Init(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Meta(key); err != nil {
		t.Fatalf("Meta after Init err = %v", err)
	}
}

func TestOwnerStoreUpdateMetaCAS(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	_, meta, err := s.Init(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	bumped, err := s.UpdateMeta(ctx, key, meta.Revision, func(m *OwnerMeta) error {
		m.Sources = append(m.Sources, OwnerSource{Kind: "session-unit", Path: "/legacy/a.json", SHA256: "aa"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if bumped.Revision != meta.Revision+1 {
		t.Fatalf("revision = %d, want %d", bumped.Revision, meta.Revision+1)
	}
	// A stale expected revision must be refused, not silently clobbered.
	if _, err := s.UpdateMeta(ctx, key, meta.Revision, nil); !errors.Is(err, ErrOwnerCASConflict) {
		t.Fatalf("stale CAS err = %v, want ErrOwnerCASConflict", err)
	}
	// A mutate error publishes nothing: the stored revision is unchanged.
	boom := errors.New("mutate refused")
	if _, err := s.UpdateMeta(ctx, key, bumped.Revision, func(*OwnerMeta) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("mutate error = %v, want %v", err, boom)
	}
	stored, err := s.Meta(key)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != bumped.Revision {
		t.Fatalf("stored revision = %d, want %d (a failed mutate must not publish)", stored.Revision, bumped.Revision)
	}
	// expected = 0 accepts whatever is stored (unconditional update).
	if _, err := s.UpdateMeta(ctx, key, 0, nil); err != nil {
		t.Fatalf("unconditional update err = %v", err)
	}
}

func TestOwnerStoreConcurrentUpdateMetaKeepsEverySource(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	if _, _, err := s.Init(ctx, key); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			for range 50 {
				meta, err := s.Meta(key)
				if err != nil {
					errs[i] = err
					return
				}
				_, err = s.UpdateMeta(ctx, key, meta.Revision, func(m *OwnerMeta) error {
					m.Sources = append(m.Sources, OwnerSource{Path: "src", SHA256: string(rune('a' + i))})
					return nil
				})
				if err == nil {
					return
				}
				if !errors.Is(err, ErrOwnerCASConflict) {
					errs[i] = err
					return
				}
			}
			errs[i] = errors.New("exhausted retries")
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	meta, err := s.Meta(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Sources) != writers {
		t.Fatalf("adopted sources = %d, want %d (a lost update dropped one)", len(meta.Sources), writers)
	}
	if meta.Revision != 1+writers {
		t.Fatalf("revision = %d, want %d", meta.Revision, 1+writers)
	}
}

func TestOwnerStoreDeleteTrashesOnlyItsOwnDir(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	victim := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	sibling := OwnerKey{TeamID: "team-a", MemberID: "coder-2"}
	other := OwnerKey{TeamID: "team-a-b", MemberID: "coder-1"}
	for _, key := range []OwnerKey{victim, sibling, other} {
		if _, _, err := s.Init(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	trash, err := s.Delete(victim)
	if err != nil {
		t.Fatal(err)
	}
	if trash == "" {
		t.Fatal("Delete returned no staged path")
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "team-a", "coder-1")); !os.IsNotExist(err) {
		t.Fatalf("victim dir still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), filepath.FromSlash(trash), ownerMetaFile)); err != nil {
		t.Fatalf("staged trash is missing its metadata: %v", err)
	}
	for _, key := range []OwnerKey{sibling, other} {
		if _, err := os.Stat(filepath.Join(s.Root(), key.TeamID, key.MemberID, ownerMetaFile)); err != nil {
			t.Fatalf("sibling %s was affected by the delete: %v", key, err)
		}
	}
	// A second delete is idempotent: nothing left to stage.
	again, err := s.Delete(victim)
	if err != nil || again != "" {
		t.Fatalf("second Delete = (%q, %v), want (\"\", nil)", again, err)
	}
}

func TestOwnerStoreSweepTrashDoesNotTouchPrefixSiblings(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	target := OwnerKey{TeamID: "t", MemberID: "a-b"}
	neighbour := OwnerKey{TeamID: "t", MemberID: "a-b-c"}
	teamPrefix := OwnerKey{TeamID: "t-a", MemberID: "b"}
	for _, key := range []OwnerKey{target, neighbour, teamPrefix} {
		if _, _, err := s.Init(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Delete(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SweepOwnerTrash(target); err != nil {
		t.Fatal(err)
	}
	bucket := filepath.Join(s.Root(), trashRootDir, ownerTrashDir)
	entries, err := os.ReadDir(bucket)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) != 2 {
		t.Fatalf("trash entries left = %v, want the two unrelated owners untouched", left)
	}
	for _, name := range left {
		if strings.HasPrefix(name, ownerTrashPrefix(target)) {
			t.Fatalf("sweep left the target's own trash behind: %s", name)
		}
	}
}

func TestOwnerStoreLockFileIsAdvisoryAndPerOwner(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	paths, _, err := s.Init(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.lock(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	// The lock is held by this process; the same path must block.
	blocked := make(chan error, 1)
	go func() {
		second, err := s.lock(ctx, paths)
		if err == nil {
			second()
		}
		blocked <- err
	}()
	release()
	if err := <-blocked; err != nil {
		t.Fatalf("second lock after release err = %v", err)
	}
}

// TestOwnerStoreBumpHistoryKeepsStemWhenEmpty pins the identity half of the
// cross-window contract: an empty stem never erases a name already published,
// while the generation still advances. The legacy axis has no transcript to
// name the instant a clear returns (the fresh file is written on the next
// save), so a publication there reports "" — dropping the old name would leave
// the owner unnamed for every peer until that save, and losing the bump would
// hide the clear entirely.
func TestOwnerStoreBumpHistoryKeepsStemWhenEmpty(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	read := func() OwnerFingerprint {
		t.Helper()
		got, err := s.Fingerprint(key)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if err := s.BumpHistory(ctx, key, "s1", true); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (OwnerFingerprint{Present: true, Stem: "s1", Generation: 1}) {
		t.Fatalf("first bump = %+v, want stem s1 at generation 1", got)
	}

	// The empty publication: the name is kept, the change is not.
	if err := s.BumpHistory(ctx, key, "", true); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (OwnerFingerprint{Present: true, Stem: "s1", Generation: 2}) {
		t.Fatalf("empty-stem bump = %+v, want stem s1 kept at generation 2", got)
	}
	// Whitespace is the same absence, not a name.
	if err := s.BumpHistory(ctx, key, "  ", true); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (OwnerFingerprint{Present: true, Stem: "s1", Generation: 3}) {
		t.Fatalf("whitespace-stem bump = %+v, want stem s1 kept at generation 3", got)
	}

	// A real name still replaces the stale one.
	if err := s.BumpHistory(ctx, key, "s2", true); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (OwnerFingerprint{Present: true, Stem: "s2", Generation: 4}) {
		t.Fatalf("named bump = %+v, want stem s2 at generation 4", got)
	}

	// Establishing the identity is not a change: the stem may be renamed by a
	// later assembly, but the generation must not move.
	if err := s.BumpHistory(ctx, key, "", false); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (OwnerFingerprint{Present: true, Stem: "s2", Generation: 4}) {
		t.Fatalf("unbumped empty publish = %+v, want stem s2 at generation 4", got)
	}
	if err := s.BumpHistory(ctx, key, "s3", false); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (OwnerFingerprint{Present: true, Stem: "s3", Generation: 4}) {
		t.Fatalf("unbumped named publish = %+v, want stem s3 at generation 4", got)
	}
}

// TestOwnerStoreBumpHistoryFirstIdentityStaysUnnamed pins the other half: a
// first identity with an empty stem is stored empty rather than invented, so a
// peer reads "present but unnamed" instead of a name no session ever had. The
// generation still advances, because the caller did report a change.
func TestOwnerStoreBumpHistoryFirstIdentityStaysUnnamed(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}

	if err := s.BumpHistory(ctx, key, "", false); err != nil {
		t.Fatal(err)
	}
	got, err := s.Fingerprint(key)
	if err != nil {
		t.Fatal(err)
	}
	if got != (OwnerFingerprint{Present: true}) {
		t.Fatalf("a first empty identity = %+v, want present and unnamed at generation 0", got)
	}

	if err := s.BumpHistory(ctx, key, "", true); err != nil {
		t.Fatal(err)
	}
	got, err = s.Fingerprint(key)
	if err != nil {
		t.Fatal(err)
	}
	if got != (OwnerFingerprint{Present: true, Generation: 1}) {
		t.Fatalf("an empty-stem bump on an unnamed owner = %+v, want generation 1 and still unnamed", got)
	}
	meta, err := s.Meta(key)
	if err != nil {
		t.Fatal(err)
	}
	if meta.History.UpdatedAt == "" {
		t.Fatal("a publication must stamp UpdatedAt")
	}
}

// TestOwnerStoreFingerprintDistinguishesCorruptFromUnnamed pins the fail-closed
// contract of the polling surface: an owner whose metadata document cannot be
// read is reported as present and corrupt, not as present and unnamed. The two
// states are otherwise byte-identical to a reader — both are {Present: true} —
// and a reader that cannot tell them apart adopts an identity it cannot trust,
// rendering the unreadable owner as "unnamed at generation 0" while the real
// change on disk is never seen.
func TestOwnerStoreFingerprintDistinguishesCorruptFromUnnamed(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}

	// A legitimate first identity with no name: present, unnamed, generation 0.
	if err := s.BumpHistory(ctx, key, "", false); err != nil {
		t.Fatal(err)
	}
	unnamed, err := s.Fingerprint(key)
	if err != nil {
		t.Fatal(err)
	}
	if unnamed.Corrupt {
		t.Fatalf("a legitimately unnamed owner must not read as corrupt: %+v", unnamed)
	}

	// The same directory with an unreadable document.
	paths, err := s.Paths(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Meta, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt, err := s.Fingerprint(key)
	if err != nil {
		t.Fatalf("an unreadable document must be reported through the fingerprint, not as an error: %v", err)
	}
	if !corrupt.Present || !corrupt.Corrupt {
		t.Fatalf("a corrupt owner = %+v, want present and corrupt", corrupt)
	}
	if corrupt == unnamed {
		t.Fatal("a corrupt owner must not be indistinguishable from an unnamed one")
	}
	if !corrupt.Changed(unnamed) {
		t.Fatal("a reader comparing the two states must see a change")
	}

	// An absent owner directory is still the empty state, not corruption: a
	// cleared team and a broken document call for different actions.
	absent, err := s.Fingerprint(OwnerKey{TeamID: "team-a", MemberID: "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if absent != (OwnerFingerprint{}) {
		t.Fatalf("an absent owner = %+v, want the zero fingerprint", absent)
	}
}
