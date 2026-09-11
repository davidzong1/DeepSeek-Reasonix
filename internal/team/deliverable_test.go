package team

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDeliverables(t *testing.T) (*DeliverableStore, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "cache")
	return NewDeliverableStoreAt(root, nil), root
}

func mustPublish(t *testing.T, s *DeliverableStore, team, member, slug, body string) Deliverable {
	t.Helper()
	item, err := s.Publish(team, member, slug, []byte(body))
	if err != nil {
		t.Fatalf("Publish(%s/%s/%s): %v", team, member, slug, err)
	}
	return item
}

func TestDeliverableRoundTripKeepsThePublishedBytes(t *testing.T) {
	s, _ := testDeliverables(t)
	body := "# 交付文档\n\n中文正文 with ascii\n"
	item := mustPublish(t, s, "team-a", "coder-claude", "route", body)

	if want := DeliverableDigest([]byte(body)); item.SHA256 != want {
		t.Fatalf("SHA256 = %s, want %s", item.SHA256, want)
	}
	if item.Size != int64(len(body)) {
		t.Fatalf("Size = %d, want %d", item.Size, len(body))
	}
	if !strings.HasPrefix(item.ID, "coder-claude-route-") || !strings.HasSuffix(item.ID, ".md") {
		t.Fatalf("id %q does not name owner, slug and suffix", item.ID)
	}
	got, err := s.Read("team-a", item.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != body {
		t.Fatalf("read %q, want %q", got, body)
	}
	stat, err := s.Stat("team-a", item.ID)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat != item {
		t.Fatalf("Stat = %+v, want %+v", stat, item)
	}
	list, err := s.List("team-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != item.ID {
		t.Fatalf("List = %+v, want the published document alone", list)
	}
}

// Republishing one body is idempotent; new content is a new document and the
// earlier one stays readable, so a reader holding an old id never loses it.
func TestDeliverableRepublishIsIdempotentAndContentAddressed(t *testing.T) {
	s, _ := testDeliverables(t)
	first := mustPublish(t, s, "t", "member", "plan", "v1")
	again := mustPublish(t, s, "t", "member", "plan", "v1")
	if again.ID != first.ID {
		t.Fatalf("republish produced %s, want the same id %s", again.ID, first.ID)
	}
	second := mustPublish(t, s, "t", "member", "plan", "v2")
	if second.ID == first.ID {
		t.Fatal("changed content reused the previous id")
	}
	body, err := s.Read("t", first.ID)
	if err != nil {
		t.Fatalf("the earlier document became unreadable: %v", err)
	}
	if string(body) != "v1" {
		t.Fatalf("earlier document = %q, want v1", body)
	}
	list, err := s.List("t")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List kept %d documents, want both versions", len(list))
	}
}

// A slug may differ only in case, or the owner may differ, and still be a
// different document: the id is the whole identity, not the slug alone.
func TestDeliverableIdentityCarriesOwnerAndSlug(t *testing.T) {
	s, _ := testDeliverables(t)
	a := mustPublish(t, s, "t", "member-a", "notes", "same")
	b := mustPublish(t, s, "t", "member-b", "notes", "same")
	if a.ID == b.ID {
		t.Fatal("two owners publishing the same body share one id")
	}
	if _, err := s.Read("t", a.ID); err != nil {
		t.Fatalf("owner A document: %v", err)
	}
	if _, err := s.Read("t", b.ID); err != nil {
		t.Fatalf("owner B document: %v", err)
	}
}

// One team's id never addresses another team's document, and a team that never
// published lists empty rather than failing.
func TestDeliverableTeamsAreIsolated(t *testing.T) {
	s, root := testDeliverables(t)
	item := mustPublish(t, s, "team-a", "member", "doc", "a-only")
	if _, err := s.Read("team-b", item.ID); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("cross-team read = %v, want ErrDeliverableNotFound", err)
	}
	empty, err := s.List("team-b")
	if err != nil {
		t.Fatalf("List on a team that never published: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("List = %+v, want an empty list", empty)
	}
	if _, err := s.List("never-existed"); err != nil {
		t.Fatalf("List on an unknown team: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "team-b")); !os.IsNotExist(err) {
		t.Fatalf("listing created team-b's directory: %v", err)
	}
}

func TestDeliverableRefusesInvalidTeamMemberAndSlug(t *testing.T) {
	s, _ := testDeliverables(t)
	for _, tc := range []struct {
		name   string
		team   string
		member string
		slug   string
		want   error
	}{
		{name: "empty team", team: "", member: "m", slug: "s", want: ErrInvalidDeliverableName},
		{name: "team separator", team: "../escape", member: "m", slug: "s", want: ErrInvalidDeliverableName},
		{name: "team nested", team: "a/b", member: "m", slug: "s", want: ErrInvalidDeliverableName},
		{name: "team dot", team: "..", member: "m", slug: "s", want: ErrInvalidDeliverableName},
		{name: "team control", team: "a\x00b", member: "m", slug: "s", want: ErrInvalidDeliverableName},
		{name: "member separator", team: "t", member: "a/b", slug: "s", want: ErrInvalidDeliverableName},
		{name: "member traversal", team: "t", member: "..", slug: "s", want: ErrInvalidDeliverableName},
		{name: "empty slug", team: "t", member: "m", slug: "", want: ErrInvalidDeliverableSlug},
		{name: "slug uppercase", team: "t", member: "m", slug: "Plan", want: ErrInvalidDeliverableSlug},
		{name: "slug traversal", team: "t", member: "m", slug: "../x", want: ErrInvalidDeliverableSlug},
		{name: "slug leading dot", team: "t", member: "m", slug: ".hidden", want: ErrInvalidDeliverableSlug},
		{name: "slug space", team: "t", member: "m", slug: "a b", want: ErrInvalidDeliverableSlug},
		{name: "slug too long", team: "t", member: "m", slug: strings.Repeat("a", deliverableSlugMax+1), want: ErrInvalidDeliverableSlug},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Publish(tc.team, tc.member, tc.slug, []byte("x")); !errors.Is(err, tc.want) {
				t.Fatalf("Publish = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDeliverableRefusesInvalidID(t *testing.T) {
	s, _ := testDeliverables(t)
	for _, tc := range []struct{ name, id string }{
		{name: "empty", id: ""},
		{name: "absolute", id: "/etc/passwd"},
		{name: "traversal", id: "../escape.md"},
		{name: "nested", id: "sub/doc-abcdefabcdef.md"},
		{name: "dot", id: "."},
		{name: "dotdot", id: ".."},
		{name: "other extension", id: "doc-abcdefabcdef.txt"},
		{name: "short digest", id: "doc-abcdefab.md"},
		{name: "uppercase digest", id: "doc-ABCDEFABCDEF.md"},
		{name: "temp file", id: ".atomic-123.tmp"},
		{name: "no digest", id: "doc.md"},
		{name: "control", id: "doc-abcdefabcdef\x00.md"},
		{name: "too long", id: strings.Repeat("a", deliverableIDMax+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Read("t", tc.id); !errors.Is(err, ErrInvalidDeliverableID) && !errors.Is(err, ErrDeliverableNotFound) {
				t.Fatalf("Read(%q) = %v, want an invalid-id refusal", tc.id, err)
			}
		})
	}
}

// A symbolic link anywhere below the cache root is named and refused, whatever
// it points at: the write path would otherwise rename through the link.
func TestDeliverableRefusesSymlinks(t *testing.T) {
	s, root := testDeliverables(t)
	outside := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "team-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := s.Publish("team-link", "m", "doc", []byte("x")); !errors.Is(err, ErrDeliverableSymlink) {
		t.Fatalf("publish through a linked team dir = %v, want ErrDeliverableSymlink", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "m-doc.md")); !os.IsNotExist(err) {
		t.Fatalf("the link target was written through: %v", err)
	}

	item := mustPublish(t, s, "team", "m", "real", "body")
	dir := filepath.Join(root, "team")
	if err := os.Remove(filepath.Join(dir, item.ID)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	target := filepath.Join(outside, "stolen.md")
	if err := os.WriteFile(target, []byte("body"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, item.ID)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := s.Read("team", item.ID); !errors.Is(err, ErrDeliverableSymlink) {
		t.Fatalf("read through a linked leaf = %v, want ErrDeliverableSymlink", err)
	}
}

// The cache tree is created 0700 and a directory an earlier run left loose is
// tightened, because MkdirAll alone never narrows an existing mode.
func TestDeliverableDirectoryAndFileModes(t *testing.T) {
	s, root := testDeliverables(t)
	loose := filepath.Join(root, "team")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	item := mustPublish(t, s, "team", "m", "doc", "body")
	for _, dir := range []string{root, loose} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Fatalf("%s mode = %o, want 700", dir, got)
		}
	}
	fi, err := os.Stat(filepath.Join(loose, item.ID))
	if err != nil {
		t.Fatalf("stat document: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("document mode = %o, want 600", got)
	}
}

func TestDeliverableBodyLimits(t *testing.T) {
	s, _ := testDeliverables(t)
	if _, err := s.Publish("t", "m", "big", make([]byte, DeliverableMaxBytes+1)); !errors.Is(err, ErrDeliverableTooLarge) {
		t.Fatalf("oversized publish = %v, want ErrDeliverableTooLarge", err)
	}
	exact := make([]byte, DeliverableMaxBytes)
	for i := range exact {
		exact[i] = 'x'
	}
	item, err := s.Publish("t", "m", "exact", exact)
	if err != nil {
		t.Fatalf("a body of exactly the cap was refused: %v", err)
	}
	body, err := s.Read("t", item.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(body) != DeliverableMaxBytes {
		t.Fatalf("read %d bytes, want %d", len(body), DeliverableMaxBytes)
	}
}

// A document edited behind the component's back no longer hashes to the digest
// its id carries, and reading it fails instead of returning the wrong bytes.
func TestDeliverableReadRefusesTamperedContent(t *testing.T) {
	s, root := testDeliverables(t)
	item := mustPublish(t, s, "t", "m", "doc", "original")
	path := filepath.Join(root, "t", item.ID)
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := s.Read("t", item.ID); !errors.Is(err, ErrDeliverableDigest) {
		t.Fatalf("Read = %v, want ErrDeliverableDigest", err)
	}
	if _, err := s.Stat("t", item.ID); !errors.Is(err, ErrDeliverableDigest) {
		t.Fatalf("Stat = %v, want ErrDeliverableDigest", err)
	}
	if _, err := s.List("t"); !errors.Is(err, ErrDeliverableDigest) {
		t.Fatalf("List = %v, want ErrDeliverableDigest", err)
	}
}

// Concurrent publishers of the same body converge on one id, distinct bodies
// all land, and no temporary file survives a publish.
func TestDeliverableConcurrentPublishLeavesNoPartialFile(t *testing.T) {
	s, root := testDeliverables(t)
	const writers = 8
	var wg sync.WaitGroup
	ids := make([]string, writers*2)
	errs := make([]error, writers*2)
	for i := range writers {
		wg.Go(func() {
			// t.Fatalf is unsafe off the test goroutine, so each writer records
			// its failure for the test goroutine to report.
			shared, err := s.Publish("t", "m", "shared", []byte("same-body"))
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = shared.ID
			own, err := s.Publish("t", "m", "own", []byte("body-"+string(rune('a'+i))))
			if err != nil {
				errs[writers+i] = err
				return
			}
			ids[writers+i] = own.ID
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	for i := 1; i < writers; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("concurrent publisher %d got %s, want %s", i, ids[i], ids[0])
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if len(seen) != writers+1 {
		t.Fatalf("%d distinct ids, want %d", len(seen), writers+1)
	}
	entries, err := os.ReadDir(filepath.Join(root, "t"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), deliverableTempMark) {
			t.Fatalf("a temporary file survived the publish: %s", e.Name())
		}
	}
	if len(entries) != writers+1 {
		t.Fatalf("%d files on disk, want %d documents", len(entries), writers+1)
	}
	list, err := s.List("t")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != writers+1 {
		t.Fatalf("List = %d documents, want %d", len(list), writers+1)
	}
}

// The fixed root is the user state root. With no state root the constructor
// fails rather than falling back to a relative directory.
func TestDeliverableStoreRequiresAStateRoot(t *testing.T) {
	t.Setenv("REASONIX_STATE_HOME", "")
	t.Setenv("REASONIX_HOME", "")
	t.Setenv("HOME", "")
	if _, err := NewDeliverableStore(nil); !errors.Is(err, ErrDeliverableNoRoot) {
		t.Fatalf("NewDeliverableStore = %v, want ErrDeliverableNoRoot", err)
	}
	dir := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", dir)
	s, err := NewDeliverableStore(nil)
	if err != nil {
		t.Fatalf("NewDeliverableStore: %v", err)
	}
	want := filepath.Join(dir, "team", "cache")
	if s.Root() != want {
		t.Fatalf("root = %s, want %s", s.Root(), want)
	}
	mustPublish(t, s, "t", "m", "doc", "body")
	if _, err := os.Stat(filepath.Join(want, "t")); err != nil {
		t.Fatalf("publish did not land under the fixed root: %v", err)
	}
}

// Refusals are logged once, with the team and id and without the body.
func TestDeliverableRefusalsAreLoggedWithoutBody(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	s := NewDeliverableStoreAt(t.TempDir(), func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	secret := "sekrit-body"
	if _, err := s.Publish("t", "m", "BAD SLUG", []byte(secret)); err == nil {
		t.Fatal("invalid slug was accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly one refusal", len(lines))
	}
	if strings.Contains(lines[0], secret) {
		t.Fatalf("the refusal log leaked the body: %s", lines[0])
	}
	if !strings.Contains(lines[0], "invalid-slug") {
		t.Fatalf("refusal line = %q, want the category", lines[0])
	}
}

func TestDeliverableListOrdersByIDAndSkipsLinksAndTemps(t *testing.T) {
	s, root := testDeliverables(t)
	mustPublish(t, s, "t", "m", "b", "second")
	mustPublish(t, s, "t", "m", "a", "first")
	dir := filepath.Join(root, "t")
	if err := os.WriteFile(filepath.Join(dir, deliverableTempMark+"x.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not-a-deliverable.txt"), []byte("stray"), 0o600); err != nil {
		t.Fatalf("write stray: %v", err)
	}
	list, err := s.List("t")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List = %+v, want the two published documents", list)
	}
	if list[0].ID > list[1].ID {
		t.Fatalf("List is not ordered by id: %+v", list)
	}
	if _, err := s.Stat("t", "not-a-deliverable.txt"); !errors.Is(err, ErrInvalidDeliverableID) {
		t.Fatalf("a stray file was addressable: %v", err)
	}
}

func TestDeliverableMetadataIsStableAcrossReads(t *testing.T) {
	s, _ := testDeliverables(t)
	item := mustPublish(t, s, "t", "m", "doc", "body")
	time.Sleep(2 * time.Millisecond)
	again, err := s.Stat("t", item.ID)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if again.SHA256 != item.SHA256 || again.Size != item.Size || !again.ModTime.Equal(item.ModTime) {
		t.Fatalf("metadata drifted: %+v vs %+v", again, item)
	}
}

// The parent containment check is what sees a directory swapped for a symbolic
// link after the segment guard has already passed — the window the write lock
// does not close, because a process outside this one is not holding it.
func TestDeliverableConfineParentRefusesASwapAfterTheSegmentGuard(t *testing.T) {
	s, root := testDeliverables(t)
	outside := t.TempDir()
	item := mustPublish(t, s, "team", "m", "doc", "v1")
	teamDir := filepath.Join(root, "team")
	if err := os.RemoveAll(teamDir); err != nil {
		t.Fatalf("remove team dir: %v", err)
	}
	if err := os.Symlink(outside, teamDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := s.confineParent(path.Join("team", item.ID)); !errors.Is(err, ErrDeliverableSymlink) {
		t.Fatalf("confineParent = %v, want ErrDeliverableSymlink", err)
	}
	if _, err := s.Publish("team", "m", "doc", []byte("v2")); !errors.Is(err, ErrDeliverableSymlink) {
		t.Fatalf("publish after a swap = %v, want ErrDeliverableSymlink", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read the swap target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the swap target received %v", entries)
	}
}

// A publish racing a directory swap must never land outside the cache root:
// every attempt either succeeds or is refused by one of the guards, and the
// swap target stays empty.
func TestDeliverablePublishRacingADirectorySwapNeverEscapes(t *testing.T) {
	s, root := testDeliverables(t)
	outside := t.TempDir()
	teamDir := filepath.Join(root, "team")
	if err := os.MkdirAll(teamDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	stop := make(chan struct{})
	var swap sync.WaitGroup
	swap.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.RemoveAll(teamDir)
			if err := os.Symlink(outside, teamDir); err != nil {
				return
			}
			_ = os.RemoveAll(teamDir)
		}
	})

	var publishers sync.WaitGroup
	for i := range 6 {
		publishers.Go(func() {
			for n := range 6 {
				_, _ = s.Publish("team", "m", fmt.Sprintf("doc-%d-%d", i, n), []byte("body"))
			}
		})
	}
	publishers.Wait()
	close(stop)
	swap.Wait()

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read the swap target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a publish escaped the cache root into %s: %v", outside, entries)
	}
}

// A listing walks the team directory, so it meets whatever else is there: a
// directory named like an id, a stray note, a temp file. None of those is a
// deliverable, so none may fail the team's listing.
func TestDeliverableListSkipsForeignEntries(t *testing.T) {
	s, root := testDeliverables(t)
	keep := mustPublish(t, s, "t", "m", "keep", "body")
	dir := filepath.Join(root, "t")

	// A directory whose name has an id's shape, holding files of its own.
	nested := filepath.Join(dir, "m-nested-abcdefabcdef.md")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "inner-abcdefabcdef.md"), []byte("inner"), 0o600); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	for name, body := range map[string]string{"notes.txt": "stray", deliverableTempMark + "x.tmp": "partial"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	list, err := s.List("t")
	if err != nil {
		t.Fatalf("a foreign entry failed the whole listing: %v", err)
	}
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("List = %+v, want the one published document", list)
	}
	if err := os.RemoveAll(nested); err != nil {
		t.Fatalf("remove nested: %v", err)
	}
	if _, err := s.List("t"); err != nil {
		t.Fatalf("listing after the nested tree went away: %v", err)
	}
}

// An id-shaped name holding bytes that do not hash to its digest is a tampered
// document, not a foreign entry: the name is the store's own namespace, so the
// listing keeps reporting it instead of hiding the evidence.
func TestDeliverableListStillRefusesTampering(t *testing.T) {
	s, root := testDeliverables(t)
	item := mustPublish(t, s, "t", "m", "doc", "body")
	dir := filepath.Join(root, "t")
	if err := os.WriteFile(filepath.Join(dir, item.ID), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := s.List("t"); !errors.Is(err, ErrDeliverableDigest) {
		t.Fatalf("List = %v, want ErrDeliverableDigest", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m-ghost-abcdefabcdef.md"), []byte("never published"), 0o600); err != nil {
		t.Fatalf("write ghost: %v", err)
	}
	if _, err := s.Read("t", "m-ghost-abcdefabcdef.md"); !errors.Is(err, ErrDeliverableDigest) {
		t.Fatalf("a file with an id-shaped name and foreign bytes = %v, want ErrDeliverableDigest", err)
	}
}
