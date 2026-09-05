package manager

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"reasonix/internal/knowledge_base/model"
)

// Query must only surface live items. processRetire and commitSupersede write
// the new status to the store before they drop the id from the index, so a
// query inside that window still sees a stale index hit whose store item is
// already retired/superseded. Reproduce the interleaving deterministically
// (store moved, index untouched) and assert Query skips the item.
func TestQuerySkipsNonLiveOnIndexLag(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{e2eThought("decision: keep the index warm", "alice")})
	res := e2eQueryAll(t, m)
	if len(res) != 1 {
		t.Fatalf("seeded live = %d, want 1", len(res))
	}
	id := res[0].Item.ID

	for _, st := range []model.Status{model.StatusSuperseded, model.StatusRetired} {
		// First half of the worker's supersede/retire: the store moves, the
		// index still lists the doc (its Remove runs just after).
		if err := m.st.Transition(id, func(x *model.KnowledgeItem) error {
			x.Status = st
			x.UpdatedAt = m.now()
			return nil
		}); err != nil {
			t.Fatalf("transition to %s: %v", st, err)
		}
		for _, r := range e2eQueryAll(t, m) {
			if r.Item.ID == id {
				t.Fatalf("Query returned %s item %s while the index still listed it", st, id)
			}
		}
	}
}

// Query racing Retire must never hand out a non-live item: every index hit is
// re-validated against the store after the fetch. The invariant holds for any
// interleaving once the post-fetch live check is in place, so this stress is
// meaningful under -race without being flaky on the fixed code.
func TestQueryConcurrentRetireOnlyLive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")
	for round := 0; round < 3; round++ {
		const n = 6
		var ids []string
		for i := 0; i < n; i++ {
			text := "decision: concurrent retire round " + strconv.Itoa(round) + " item " + strconv.Itoa(i)
			e2eIngest(t, m, []model.Thought{e2eThought(text, "alice")})
		}
		for _, r := range e2eQueryAll(t, m) {
			ids = append(ids, r.Item.ID)
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Retire(context.Background(), ids, model.ReasonNoLongerTrue); err != nil {
				t.Errorf("round %d Retire: %v", round, err)
			}
		}()
		for k := 0; k < 4; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := m.Query(context.Background(), model.Query{Scope: model.ScopeTeam, Limit: 50})
				if err != nil {
					t.Errorf("concurrent Query: %v", err)
					return
				}
				for _, r := range res {
					if !r.Item.Live() {
						t.Errorf("Query returned non-live item %s (status %s)", r.Item.ID, r.Item.Status)
					}
				}
			}()
		}
		wg.Wait()
		e2eFlush(t, m)
	}
}
