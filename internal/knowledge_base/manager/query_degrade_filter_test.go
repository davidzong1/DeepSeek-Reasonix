package manager

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/knowledge_base/model"
)

// The degraded read path must still enforce the query's metadata hard-filters:
// recency fallback is a ranking substitute, not a license to widen recall across
// kind/tag/confidence. These queries force the index error with unindexable text
// and assert the fallback respects each filter (and their conjunction).
func TestQueryDegradeKeepsKindAndTagFilters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, a := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{
		e2eThought("convention: alpha component layout", "alice"),
		e2eThought("decision: beta engine choice", "bob"),
		e2eThought("decision: gamma ui choice", "carol"),
	})
	items, err := m.st.List()
	if err != nil {
		t.Fatalf("store List: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("seeded live = %d, want 3", len(items))
	}
	byBody := make(map[string]string, 3)
	kinds := make(map[string]model.ItemKind, 3)
	for _, it := range items {
		for _, sub := range []string{"alpha component layout", "beta engine choice", "gamma ui choice"} {
			if strings.Contains(it.Body, sub) {
				byBody[sub] = it.ID
				kinds[sub] = it.Kind
			}
		}
	}
	// Seed one convention + two decisions: the kind filter must tell them apart.
	for sub, want := range map[string]model.ItemKind{
		"alpha component layout": model.ItemConvention,
		"beta engine choice":     model.ItemDecision,
		"gamma ui choice":        model.ItemDecision,
	} {
		if got := kinds[sub]; got != want {
			t.Fatalf("kind(%s) = %s, want %s", sub, got, want)
		}
	}
	// Tags and UpdatedAt are safe to re-stamp (not part of the canonical key);
	// kind stays as-ingested so the store's canonical invariant holds.
	base := a.Clock()
	stamp := map[string]struct {
		tags []string
		at   time.Time
	}{
		"alpha component layout": {[]string{"layout"}, base},
		"beta engine choice":     {[]string{"db"}, base.Add(time.Second)},
		"gamma ui choice":        {[]string{"ui"}, base.Add(2 * time.Second)},
	}
	for sub, s := range stamp {
		if err := m.st.Transition(byBody[sub], func(x *model.KnowledgeItem) error {
			x.Tags = s.tags
			x.UpdatedAt = s.at
			return nil
		}); err != nil {
			t.Fatalf("re-stamp %s: %v", sub, err)
		}
	}
	conv, decB, decC := byBody["alpha component layout"], byBody["beta engine choice"], byBody["gamma ui choice"]

	degrade := func(q model.Query) []string {
		t.Helper()
		q.Scope = model.ScopeTeam
		q.Text = "!!!"
		q.Limit = 50
		res, err := m.Query(context.Background(), q)
		if err != nil {
			t.Fatalf("degraded Query errored: %v", err)
		}
		return degradeIDs(res)
	}

	if got := degrade(model.Query{Kinds: []model.ItemKind{model.ItemConvention}}); !degradeSameOrder(got, []string{conv}) {
		t.Fatalf("Kinds=convention = %v, want %v", got, []string{conv})
	}
	// Newest-first within the filtered set (decC re-stamped newest).
	if got := degrade(model.Query{Kinds: []model.ItemKind{model.ItemDecision}}); !degradeSameOrder(got, []string{decC, decB}) {
		t.Fatalf("Kinds=decision = %v, want %v (convention leaked)", got, []string{decC, decB})
	}
	if got := degrade(model.Query{Tags: []string{"db"}}); !degradeSameOrder(got, []string{decB}) {
		t.Fatalf("Tags=db = %v, want %v (non-db leaked)", got, []string{decB})
	}
	if got := degrade(model.Query{Kinds: []model.ItemKind{model.ItemDecision}, Tags: []string{"db"}}); !degradeSameOrder(got, []string{decB}) {
		t.Fatalf("Kinds=decision&Tags=db = %v, want %v", got, []string{decB})
	}
}

// Confidence is a hard filter too: a live item below the query's floor must not
// surface on the degraded path, even though recency alone would put it first.
func TestQueryDegradeKeepsConfidenceFloor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, a := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{
		e2eThought("decision: confident call", "alice"),
		e2eThought("decision: shaky call", "bob"),
	})
	items, err := m.st.List()
	if err != nil {
		t.Fatalf("store List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("seeded live = %d, want 2", len(items))
	}
	var highID, lowID string
	for _, it := range items {
		if strings.Contains(it.Body, "confident") {
			highID = it.ID
		} else {
			lowID = it.ID
		}
	}
	// Newest item is the low-confidence one; the floor must still exclude it.
	base := a.Clock()
	if err := m.st.Transition(highID, func(x *model.KnowledgeItem) error {
		x.Quality.Confidence = 0.9
		x.UpdatedAt = base
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.st.Transition(lowID, func(x *model.KnowledgeItem) error {
		x.Quality.Confidence = 0.2
		x.UpdatedAt = base.Add(4 * time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	q := model.Query{Scope: model.ScopeTeam, Text: "!!!", Limit: 50, MinConfidence: 0.6}
	res, err := m.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("degraded Query errored: %v", err)
	}
	if got := degradeIDs(res); !degradeSameOrder(got, []string{highID}) {
		t.Fatalf("MinConfidence=0.6 = %v, want %v (below-floor leaked)", got, []string{highID})
	}
}

func degradeIDs(res []model.Result) []string {
	out := make([]string, 0, len(res))
	for _, r := range res {
		out = append(out, r.Item.ID)
	}
	return out
}

func degradeSameOrder(a, b []string) bool {
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
