package manager

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/knowledge_base/model"
)

// TestExpireBeforeRetiresByCreatedAt seeds the one state that separates the
// candidate anchors: a live item created before the cut but touched (UpdatedAt)
// after it. Only a CreatedAt filter retires it; an UpdatedAt filter would keep
// it, so a regression to the wrong anchor fails here.
func TestExpireBeforeRetiresByCreatedAt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, a := e2eNew(t, dir, "alpha")

	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cut := t0.Add(2 * time.Hour)
	later := t0.Add(4 * time.Hour)

	a.clock = t0
	e2eIngest(t, m, []model.Thought{e2eThought("decision: alpha picks region singapore", "alice")})
	pre := e2eQueryAll(t, m)
	if len(pre) != 1 {
		t.Fatalf("seed must create one live item, got %d", len(pre))
	}
	id := pre[0].Item.ID
	if !pre[0].Item.CreatedAt.Equal(t0) {
		t.Fatalf("seed item must be created at t0, got %v", pre[0].Item.CreatedAt)
	}

	// A later touch pushes the item's UpdatedAt past the cut while its CreatedAt
	// stays before it — exactly what a supersede/conflict sibling does to the
	// older live peer. Direct seeding keeps the discriminator deterministic.
	a.clock = later
	if err := m.st.Transition(id, func(x *model.KnowledgeItem) error {
		x.UpdatedAt = later
		return nil
	}); err != nil {
		t.Fatalf("bump UpdatedAt: %v", err)
	}
	// A second, genuinely newer item seeds after the cut and must survive.
	e2eIngest(t, m, []model.Thought{e2eThought("decision: alpha rotates signing keys", "bob")})

	all := e2eQueryAll(t, m)
	if len(all) != 2 {
		t.Fatalf("fixture must hold two live items, got %d", len(all))
	}
	var oldLive bool
	for _, r := range all {
		if r.Item.ID == id {
			oldLive = r.Item.Status == model.StatusLive
			if !r.Item.CreatedAt.Before(cut) || !r.Item.UpdatedAt.After(cut) {
				t.Fatalf("discriminator not armed: CreatedAt=%v UpdatedAt=%v cut=%v", r.Item.CreatedAt, r.Item.UpdatedAt, cut)
			}
		}
	}
	if !oldLive {
		t.Fatal("older item must still be live before the expire")
	}

	n, err := m.ExpireBefore(context.Background(), cut, model.ReasonNoLongerTrue)
	if err != nil {
		t.Fatalf("ExpireBefore: %v", err)
	}
	if n != 1 {
		t.Fatalf("first expire must retire the one item created before the cut, got %d", n)
	}
	res := e2eQueryAll(t, m)
	if len(res) != 1 {
		t.Fatalf("one item must remain live after expire, got %d", len(res))
	}
	if !res[0].Item.CreatedAt.After(cut) {
		t.Fatalf("surviving item must have been created after the cut, got %v", res[0].Item.CreatedAt)
	}
	n2, err := m.ExpireBefore(context.Background(), cut, model.ReasonNoLongerTrue)
	if err != nil {
		t.Fatalf("second ExpireBefore: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("re-running expire must retire 0, got %d", n2)
	}
}

// TestExpireBeforeFutureCutRetiresAllAndCounts verifies a far-future cutoff
// takes the whole live set and a re-run counts zero.
func TestExpireBeforeFutureCutRetiresAllAndCounts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")
	e2eIngest(t, m, []model.Thought{
		e2eThought("decision: single transport seam for members", "alice"),
		e2eThought("decision: defect reports stay on the shared board", "bob"),
	})
	n, err := m.ExpireBefore(context.Background(), time.Now().UTC().Add(24*time.Hour), model.ReasonNoLongerTrue)
	if err != nil {
		t.Fatalf("ExpireBefore: %v", err)
	}
	if n != 2 {
		t.Fatalf("future cutoff must retire all live items, got %d", n)
	}
	if live := e2eQueryAll(t, m); len(live) != 0 {
		t.Fatalf("team must be empty after a future-cut expire, got %d live", len(live))
	}
	if n2, err := m.ExpireBefore(context.Background(), time.Now().UTC().Add(24*time.Hour), model.ReasonNoLongerTrue); err != nil || n2 != 0 {
		t.Fatalf("re-run must retire 0, got n=%d err=%v", n2, err)
	}
}
