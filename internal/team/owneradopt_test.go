package team

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"reasonix/internal/store"
)

func TestOwnerUnitForCoversEverySidecar(t *testing.T) {
	unit := OwnerUnitFor("/sessions/team-a-coder-1.json")
	if unit.Transcript != "/sessions/team-a-coder-1.json" {
		t.Fatalf("transcript = %q", unit.Transcript)
	}
	// Every derived artifact the session store publishes must be enumerated, so
	// a hand-built list cannot silently drop one.
	want := map[string]bool{
		store.SessionGoalState(unit.Transcript):      false,
		store.SessionCheckpointDir(unit.Transcript):  false,
		store.SessionJobsDir(unit.Transcript):        false,
		store.SessionInboxDir(unit.Transcript):       false,
		store.SessionCleanupPending(unit.Transcript): false,
		store.SessionMeta(unit.Transcript):           false,
	}
	for _, f := range append(append([]string{}, unit.Files...), unit.Dirs...) {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for artifact, seen := range want {
		if !seen {
			t.Fatalf("OwnerUnitFor dropped %s", artifact)
		}
	}
	for _, f := range store.SessionSidecarFiles(unit.Transcript) {
		if !slicesContains(unit.Files, f) {
			t.Fatalf("OwnerUnitFor dropped sidecar %s", f)
		}
	}
	// The lease is not a durable artifact: adopting it would resurrect a claim
	// the new process never took.
	for _, e := range unit.Ephemeral {
		if slicesContains(unit.Files, e) || slicesContains(unit.Dirs, e) {
			t.Fatalf("ephemeral %s must not be adopted", e)
		}
	}
	if len(unit.Ephemeral) != 3 {
		t.Fatalf("ephemeral = %v, want the lock, lease lock and lease info", unit.Ephemeral)
	}
	if got := OwnerUnitFor(""); got.Transcript != "" || len(got.Files) != 0 {
		t.Fatalf("empty transcript unit = %+v, want zero", got)
	}
}

func slicesContains(list []string, want string) bool {
	return slices.Contains(list, want)
}

func TestOwnerStoreAdoptSessionUnitCopiesTranscriptAndSidecars(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	legacyDir := t.TempDir()
	unit := seedSessionUnit(t, legacyDir, key.TeamID, key.MemberID)

	rep, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != "" {
		t.Fatalf("adoption skipped: %s", rep.Skipped)
	}
	paths, err := s.Paths(key)
	if err != nil {
		t.Fatal(err)
	}
	// The transcript keeps its basename, so every derived sidecar name maps one
	// to one onto the owner directory and the stem ABI survives the move.
	if filepath.Base(paths.Transcript) != mustSessionFile(t, key.TeamID, key.MemberID) {
		t.Fatalf("canonical transcript = %q", paths.Transcript)
	}
	for _, artifact := range append(append([]string{unit.Transcript}, unit.Files...), unit.Dirs...) {
		rel := filepath.Join(paths.Dir, filepath.Base(artifact))
		if _, err := os.Stat(rel); err != nil {
			t.Fatalf("artifact %s was not adopted: %v", filepath.Base(artifact), err)
		}
	}
	// The nested contents of a directory artifact come along.
	if _, err := os.Stat(filepath.Join(paths.Dir, filepath.Base(store.SessionCheckpointDir(unit.Transcript)), "nested", "deep.json")); err != nil {
		t.Fatalf("checkpoint dir contents were not adopted: %v", err)
	}
	// Ephemeral lock/lease files must not be resurrected in the new directory.
	for _, e := range unit.Ephemeral {
		if _, err := os.Stat(filepath.Join(paths.Dir, filepath.Base(e))); !os.IsNotExist(err) {
			t.Fatalf("ephemeral %s was adopted: %v", filepath.Base(e), err)
		}
	}
	// The source is untouched: adoption copies, it never moves or deletes.
	for _, artifact := range append([]string{unit.Transcript}, unit.Files...) {
		if _, err := os.Stat(artifact); err != nil {
			t.Fatalf("source %s was removed: %v", artifact, err)
		}
	}
	meta, err := s.Meta(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Sources) != 1 || meta.Sources[0].SHA256 == "" {
		t.Fatalf("adopted source not recorded: %+v", meta.Sources)
	}
}

func TestOwnerStoreAdoptSessionUnitIsIdempotentAndResumable(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	unit := seedSessionUnit(t, t.TempDir(), key.TeamID, key.MemberID)
	if _, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{}); err != nil {
		t.Fatal(err)
	}
	meta, err := s.Meta(key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Skipped != "already adopted" {
		t.Fatalf("second adoption = %+v, want skipped", second)
	}
	after, err := s.Meta(key)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != meta.Revision || len(after.Sources) != len(meta.Sources) {
		t.Fatalf("re-adoption changed metadata: %+v vs %+v", after, meta)
	}

	// A resume: the source gains one more artifact after a partial adoption, and
	// the retry must complete the unit rather than skip it as already adopted.
	fresh := OwnerKey{TeamID: "team-b", MemberID: "coder-2"}
	resumed := seedSessionUnit(t, t.TempDir(), fresh.TeamID, fresh.MemberID)
	extra := store.SessionEventIndex(resumed.Transcript)
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	partial := resumed
	partial.Files = nil
	for _, f := range resumed.Files {
		if f != extra {
			partial.Files = append(partial.Files, f)
		}
	}
	if _, err := s.AdoptSessionUnit(ctx, fresh, partial, OwnerAdoptOptions{}); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash window: the first attempt never recorded the source, so
	// the retry re-runs the whole unit and picks up the late artifact.
	if err := s.rewriteMetaWithoutSources(fresh); err != nil {
		t.Fatal(err)
	}
	writeFile(t, extra, "late sidecar")
	if _, err := s.AdoptSessionUnit(ctx, fresh, resumed, OwnerAdoptOptions{}); err != nil {
		t.Fatal(err)
	}
	paths, err := s.Paths(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(paths.Dir, filepath.Base(extra))); err != nil {
		t.Fatalf("resumed adoption missed %s: %v", filepath.Base(extra), err)
	}
}

// rewriteMetaWithoutSources simulates an adoption interrupted before its
// completion marker was written.
func (s *OwnerStore) rewriteMetaWithoutSources(key OwnerKey) error {
	paths, err := s.Paths(key)
	if err != nil {
		return err
	}
	meta, err := s.readMeta(paths)
	if err != nil {
		return err
	}
	meta.Sources = nil
	return s.writeMeta(paths, meta)
}

func TestOwnerStoreAdoptSessionUnitRefusesForeignAndDivergentHistory(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	other := seedSessionUnit(t, t.TempDir(), key.TeamID, "coder-2")
	if _, err := s.AdoptSessionUnit(ctx, key, other, OwnerAdoptOptions{}); !errors.Is(err, ErrOwnerUnitMismatch) {
		t.Fatalf("adopting another member's session err = %v, want ErrOwnerUnitMismatch", err)
	}
	if _, err := s.AdoptSessionUnit(ctx, key, OwnerUnitFor(filepath.Join("/nowhere", mustSessionFile(t, key.TeamID, key.MemberID))), OwnerAdoptOptions{}); err != nil {
		t.Fatalf("absent source should be a no-op, got %v", err)
	}

	unit := seedSessionUnit(t, t.TempDir(), key.TeamID, key.MemberID)
	if _, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{}); err != nil {
		t.Fatal(err)
	}
	// The same path holding different bytes is a different history: adopting it
	// over the canonical one would be a clobber.
	writeFile(t, unit.Transcript, "different bytes")
	if _, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{}); !errors.Is(err, ErrOwnerSourceTaken) {
		t.Fatalf("divergent source err = %v, want ErrOwnerSourceTaken", err)
	}
}

func TestOwnerStoreAdoptSessionUnitRefusesDivergentCanonicalTranscript(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	unit := seedSessionUnit(t, t.TempDir(), key.TeamID, key.MemberID)
	paths, _, err := s.Init(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, paths.Transcript, "canonical history written by this build")
	if _, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{}); !errors.Is(err, ErrOwnerAdoptBusy) {
		t.Fatalf("adopting over a live canonical transcript err = %v, want ErrOwnerAdoptBusy", err)
	}
}

func TestOwnerStoreAdoptExclusiveSkipsRestoreSourceSidecars(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	unit := seedSessionUnit(t, t.TempDir(), key.TeamID, key.MemberID)
	rep, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := s.Paths(key)
	if err != nil {
		t.Fatal(err)
	}
	// Goal state and checkpoints are v3 events in exclusive mode; copying the
	// legacy files would create the second restore source rebindCheckpoints
	// exists to refuse.
	for _, skipped := range []string{
		store.SessionGoalState(unit.Transcript),
		store.SessionCheckpointDir(unit.Transcript),
	} {
		if _, err := os.Stat(filepath.Join(paths.Dir, filepath.Base(skipped))); !os.IsNotExist(err) {
			t.Fatalf("%s must not be adopted in exclusive mode: %v", filepath.Base(skipped), err)
		}
	}
	if rep.FilesSkipped < 2 {
		t.Fatalf("exclusive adoption reported %d skipped, want the two restore-source artifacts", rep.FilesSkipped)
	}
	// The rest of the unit is still adopted.
	for _, kept := range []string{
		unit.Transcript,
		store.SessionJobsDir(unit.Transcript),
		store.SessionInboxDir(unit.Transcript),
	} {
		if _, err := os.Stat(filepath.Join(paths.Dir, filepath.Base(kept))); err != nil {
			t.Fatalf("%s was not adopted: %v", filepath.Base(kept), err)
		}
	}
}

func TestOwnerStoreAdoptSessionUnitIsResumableAfterInterruption(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	unit := seedSessionUnit(t, t.TempDir(), key.TeamID, key.MemberID)
	paths, _, err := s.Init(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	// Copy only the sidecars, as an adoption interrupted before its transcript
	// step would leave the directory.
	root, err := s.openRoot(false)
	if err != nil {
		t.Fatal(err)
	}
	relDir, err := filepath.Rel(s.root, paths.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range unit.Files {
		if _, err := copyFileInto(root, filepath.Join(relDir, filepath.Base(f)), f); err != nil {
			t.Fatal(err)
		}
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Transcript); !os.IsNotExist(err) {
		t.Fatalf("interrupted state must have no transcript yet: %v", err)
	}
	rep, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != "" {
		t.Fatalf("resume was skipped: %s", rep.Skipped)
	}
	if _, err := os.Stat(paths.Transcript); err != nil {
		t.Fatalf("resume did not land the transcript: %v", err)
	}
	if rep.FilesSkipped == 0 {
		t.Fatal("resume re-copied every already-present sidecar instead of resuming")
	}
	// The adopted bytes equal the source bytes.
	for _, f := range append([]string{unit.Transcript}, unit.Files...) {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(paths.Dir, filepath.Base(f)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s content differs after adoption", filepath.Base(f))
		}
	}
}

func TestOwnerStoreAdoptLegacyContextPreservesBytesAndSource(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	legacy := t.TempDir()
	writeFile(t, filepath.Join(legacy, MemberMessagesFile), `{"kind":"user","text":"hi"}`+"\n")
	writeFile(t, filepath.Join(legacy, MemberStateFile), `{"schema_version":1,"state":"idle"}`)
	writeFile(t, filepath.Join(legacy, MemberCursorFile), `{"schema_version":1,"cursor":3}`)

	rep, err := s.AdoptLegacyContext(ctx, key, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesCopied != 3 {
		t.Fatalf("copied %d files, want 3", rep.FilesCopied)
	}
	paths, err := s.Paths(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{MemberMessagesFile, MemberStateFile, MemberCursorFile} {
		got, err := os.ReadFile(filepath.Join(paths.Dir, "legacy", name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(legacy, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("legacy %s bytes changed during adoption", name)
		}
		if _, err := os.Stat(filepath.Join(legacy, name)); err != nil {
			t.Fatalf("legacy source %s was removed: %v", name, err)
		}
	}
	// Idempotent, and an absent legacy tree is a no-op.
	if _, err := s.AdoptLegacyContext(ctx, key, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptLegacyContext(ctx, key, filepath.Join(legacy, "missing")); err != nil {
		t.Fatalf("absent legacy context err = %v", err)
	}
}

func TestOwnerStoreAdoptRefusesSymlinkedSource(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	dir := t.TempDir()
	unit := seedSessionUnit(t, dir, key.TeamID, key.MemberID)
	// A symlinked transcript refuses the adoption instead of following the link.
	target := filepath.Join(dir, "real.json")
	writeFile(t, target, "real")
	if err := os.Remove(unit.Transcript); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, unit.Transcript); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{}); !errors.Is(err, ErrOwnerSymlink) {
		t.Fatalf("symlinked source err = %v, want ErrOwnerSymlink", err)
	}
	// A symlinked directory artifact is refused too.
	other := seedSessionUnit(t, t.TempDir(), key.TeamID, key.MemberID)
	ckpt := store.SessionCheckpointDir(other.Transcript)
	if err := os.RemoveAll(ckpt); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), ckpt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptSessionUnit(ctx, key, other, OwnerAdoptOptions{}); !errors.Is(err, ErrOwnerSymlink) {
		t.Fatalf("symlinked dir artifact err = %v, want ErrOwnerSymlink", err)
	}
}

func TestOwnerStoreDeleteRemovesAdoptedUnitWhole(t *testing.T) {
	s := newTestOwnerStore(t)
	ctx := context.Background()
	key := OwnerKey{TeamID: "team-a", MemberID: "coder-1"}
	unit := seedSessionUnit(t, t.TempDir(), key.TeamID, key.MemberID)
	if _, err := s.AdoptSessionUnit(ctx, key, unit, OwnerAdoptOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepOwnerTrash(key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepOwnerTrash(key); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Root(), trashRootDir, ownerTrashDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("trash entries left = %d, want the owner's own staged dir swept", len(entries))
	}
	if _, err := os.Stat(filepath.Join(s.Root(), key.TeamID, key.MemberID)); !os.IsNotExist(err) {
		t.Fatalf("owner dir survived the delete: %v", err)
	}
}
