package manager

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/knowledge_base/model"
)

func idsOf(res []model.Result) []string {
	out := make([]string, 0, len(res))
	for _, r := range res {
		out = append(out, r.Item.ID)
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A query the index cannot tokenize (no letter/number) used to fail the whole
// read. Query must degrade to a live store scan ordered by update time, still
// honoring the query filters and limit — never a hard error on unindexable text.
func TestQueryFallsBackToLiveByUpdatedOnSearchFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, a := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{e2eThought("decision: alpha first", "alice")})
	e2eIngest(t, m, []model.Thought{e2eThought("decision: beta second", "bob")})
	e2eIngest(t, m, []model.Thought{e2eThought("decision: gamma third", "carol")})

	seeded := idsOf(e2eQueryAll(t, m))
	if len(seeded) != 3 {
		t.Fatalf("seeded live = %d, want 3", len(seeded))
	}
	// Re-stamp so expected order (b,c,a) differs from ingest/id order, proving
	// the degraded path sorts by UpdatedAt rather than returning file order.
	t0 := a.Clock().Truncate(time.Second)
	order := map[string]time.Time{
		seeded[0]: t0.Add(time.Second),      // was newest by id, becomes oldest
		seeded[1]: t0.Add(30 * time.Second), // becomes newest
		seeded[2]: t0.Add(2 * time.Second),
	}
	for id, ts := range order {
		if err := m.st.Transition(id, func(x *model.KnowledgeItem) error {
			x.UpdatedAt = ts
			return nil
		}); err != nil {
			t.Fatalf("re-stamp %s: %v", id, err)
		}
	}
	want := []string{seeded[1], seeded[2], seeded[0]}

	// "!!!" has no tokenizable letter/number, so the index errors and Query
	// must fall back instead of failing.
	res, err := m.Query(context.Background(), model.Query{Scope: model.ScopeTeam, Text: "!!!", Limit: 50})
	if err != nil {
		t.Fatalf("Query with unindexable text errored: %v", err)
	}
	if got := idsOf(res); !equalIDs(got, want) {
		t.Fatalf("degraded order = %v, want %v", got, want)
	}

	// Filter/limit still apply on the degraded path.
	if res, err = m.Query(context.Background(), model.Query{Scope: model.ScopeTeam, Text: "!!!", Limit: 1}); err != nil {
		t.Fatalf("limited Query errored: %v", err)
	} else if got := idsOf(res); !equalIDs(got, want[:1]) {
		t.Fatalf("degraded Limit:1 = %v, want %v", got, want[:1])
	}
	if res, err = m.Query(context.Background(), model.Query{Scope: model.ScopeAgent, Text: "!!!"}); err != nil {
		t.Fatalf("scoped Query errored: %v", err)
	} else if len(res) != 0 {
		t.Fatalf("team-scoped items leaked into a %s query: %v", model.ScopeAgent, idsOf(res))
	}

	// Retired items are never surfaced by the degraded path.
	if err := m.Retire(context.Background(), []string{seeded[2]}, model.ReasonNoLongerTrue); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	e2eFlush(t, m)
	if res, err = m.Query(context.Background(), model.Query{Scope: model.ScopeTeam, Text: "!!!"}); err != nil {
		t.Fatalf("post-retire Query errored: %v", err)
	} else if got := idsOf(res); !equalIDs(got, []string{seeded[1], seeded[0]}) {
		t.Fatalf("degraded post-retire = %v, want live-by-update only", got)
	}
}
