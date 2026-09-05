package manager

import (
	"context"
	"path/filepath"
	"testing"

	"reasonix/internal/knowledge_base/model"
)

// First line is shared across versions so they fill one canonical slot; the
// extra lines differ so the body (and L1 hash) changes and v2 supersedes v1
// instead of deduping against it.
const foldCanonTitle = "decision: keep only one live version per canonical slot"

func foldThought(agent, extra string) model.Thought {
	return model.Thought{
		ID: model.NewID(), TeamID: "alpha", AgentID: agent,
		Kind: model.ThoughtDecision,
		Text: foldCanonTitle + "\n" + extra,
	}
}

func foldLiveByAuthor(t *testing.T, m *Manager, author string) (model.KnowledgeItem, bool) {
	t.Helper()
	items, err := m.st.List()
	if err != nil {
		t.Fatalf("store list: %v", err)
	}
	for _, it := range items {
		if it.Status == model.StatusLive && it.AuthorID == author {
			return it, true
		}
	}
	return model.KnowledgeItem{}, false
}

func foldStored(t *testing.T, m *Manager, id string) model.KnowledgeItem {
	t.Helper()
	it, err := m.st.Get(id)
	if err != nil {
		t.Fatalf("store get %s: %v", id, err)
	}
	return it
}

// F2 crash window: commitSupersede wrote v2 live but crashed before stamping v1
// superseded, leaving two live items of one canonical+author. Replaying or
// re-ingesting the already-committed newer content lands on an L1 skip and must
// fold the lingering older live v1 under v2 (SupersededBy + index), never
// leaving a double live.
func TestIngestFoldLingeringLiveAfterSupersedeCrashWindow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{foldThought("alice", "earlier wording for the first version")})
	v1, ok := foldLiveByAuthor(t, m, "alice")
	if !ok {
		t.Fatal("v1 did not land live")
	}

	e2eIngest(t, m, []model.Thought{foldThought("alice", "authoritative wording for the current version")})
	v2, ok := foldLiveByAuthor(t, m, "alice")
	if !ok {
		t.Fatal("v2 did not land live")
	}
	if v2.Version <= v1.Version || v2.Supersedes != v1.ID {
		t.Fatalf("expected v2 to supersede v1: v1=%s(v%d) v2=%s(v%d supersedes=%s)", v1.ID, v1.Version, v2.ID, v2.Version, v2.Supersedes)
	}
	if got := foldStored(t, m, v1.ID); got.Status != model.StatusSuperseded || got.SupersededBy != v2.ID {
		t.Fatalf("normal supersede left v1=%s/%q, want superseded/%s", got.Status, got.SupersededBy, v2.ID)
	}

	// Re-create the residue: v1 flipped back to live (its stamp was lost) and the
	// read model seeded from both live store items, as a crash-restart rebuild sees.
	if err := m.st.Transition(v1.ID, func(x *model.KnowledgeItem) error {
		x.Status = model.StatusLive
		x.SupersededBy = ""
		return nil
	}); err != nil {
		t.Fatalf("simulate lost v1 stamp: %v", err)
	}
	v1 = foldStored(t, m, v1.ID)
	m.ix.Upsert(v1)
	if live := m.ix.LiveCount(); live != 2 {
		t.Fatalf("pre-repair index live = %d, want 2 (v1+v2)", live)
	}

	// Duplicate of the newer content must repair the lingering v1.
	e2eIngest(t, m, []model.Thought{foldThought("alice", "authoritative wording for the current version")})
	res := e2eQueryAll(t, m)
	if len(res) != 1 || res[0].Item.ID != v2.ID {
		t.Fatalf("after repair ingest: %d live results %v, want exactly newest %s", len(res), idsOf(res), v2.ID)
	}
	if got := foldStored(t, m, v1.ID); got.Status != model.StatusSuperseded || got.SupersededBy != v2.ID {
		t.Fatalf("lingering v1 not folded: status=%s superseded_by=%q", got.Status, got.SupersededBy)
	}
	if live := m.ix.LiveCount(); live != 1 {
		t.Fatalf("index live after repair = %d, want 1", live)
	}

	// A further duplicate is a pure no-op: v1 stays folded under v2.
	e2eIngest(t, m, []model.Thought{foldThought("alice", "authoritative wording for the current version")})
	if got := foldStored(t, m, v1.ID); got.Status != model.StatusSuperseded || got.SupersededBy != v2.ID {
		t.Fatalf("duplicate after repair re-forked v1: status=%s superseded_by=%q", got.Status, got.SupersededBy)
	}
	res, err := m.Query(context.Background(), model.Query{Scope: model.ScopeTeam, Limit: 50})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 1 || res[0].Item.ID != v2.ID {
		t.Fatalf("post-idempotency live = %d %v, want newest %s", len(res), idsOf(res), v2.ID)
	}
}

// The repair must not collapse a genuine cross-author conflict pair: a duplicate
// ingest of one author's content leaves the other author's live item untouched.
func TestIngestFoldKeepsCrossAuthorConflict(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{foldThought("alice", "alice sees a single source of truth")})
	e2eIngest(t, m, []model.Thought{foldThought("bob", "bob sees per-repo docs as truth")})
	if res := e2eQueryAll(t, m); len(res) != 2 {
		t.Fatalf("conflict seed live = %d, want 2", len(res))
	}

	// Duplicate of bob's already-committed content hits the L1 skip path; alice's
	// live version is a different author and must survive untouched.
	e2eIngest(t, m, []model.Thought{foldThought("bob", "bob sees per-repo docs as truth")})

	res := e2eQueryAll(t, m)
	if len(res) != 2 {
		t.Fatalf("after duplicate ingest live = %d %v, want both conflict authors", len(res), idsOf(res))
	}
	alice, _ := foldLiveByAuthor(t, m, "alice")
	bob, _ := foldLiveByAuthor(t, m, "bob")
	if alice.Status != model.StatusLive || alice.SupersededBy != "" {
		t.Fatalf("alice conflict item damaged: status=%s superseded_by=%q", alice.Status, alice.SupersededBy)
	}
	if bob.Status != model.StatusLive || bob.SupersededBy != "" {
		t.Fatalf("bob conflict item damaged: status=%s superseded_by=%q", bob.Status, bob.SupersededBy)
	}
}
