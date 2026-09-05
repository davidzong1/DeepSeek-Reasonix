package index

import (
	"sync"
	"testing"

	"reasonix/internal/knowledge_base/model"
)

// ixItem builds a minimal live candidate. Index does not validate items; it
// only consumes the projection fields it stores.
func ixItem(id, title, scope string, kind model.ItemKind, tags []string, conf float64, body string) model.KnowledgeItem {
	return model.KnowledgeItem{
		ID: id, Title: title, Scope: model.Scope(scope), Kind: kind,
		Tags: tags, Status: model.StatusLive, Body: body,
		Quality: model.QualitySignal{Confidence: conf},
	}
}

func TestRebuildOnlyLive(t *testing.T) {
	ix := New()
	live := ixItem("id-live-1", "Use postgres", "team", model.ItemDecision, nil, 0.9, "postgres as primary store")
	retired := ixItem("id-ret", "retired fact", "team", model.ItemFact, nil, 0.9, "old body")
	retired.Status = model.StatusRetired
	draft := ixItem("id-draft", "draft fact", "team", model.ItemFact, nil, 0.4, "uncertain body")
	draft.Status = model.StatusDraft
	superseded := ixItem("id-super", "superseded fact", "team", model.ItemFact, nil, 0.9, "old version body")
	superseded.Status = model.StatusSuperseded

	ix.Rebuild([]model.KnowledgeItem{live, retired, draft, superseded})
	if got := ix.LiveCount(); got != 1 {
		t.Fatalf("LiveCount after rebuild = %d, want 1", got)
	}
	hits, err := ix.Search(model.Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "id-live-1" {
		t.Errorf("search after rebuild = %+v, want only id-live-1", hits)
	}
}

func TestUpsertNonLiveRemoves(t *testing.T) {
	ix := New()
	it := ixItem("id-1", "postgres tuning", "team", model.ItemDecision, nil, 0.9, "shared buffers")
	ix.Upsert(it)
	if ix.LiveCount() != 1 {
		t.Fatal("upsert live should add")
	}
	it.Status = model.StatusRetired
	ix.Upsert(it) // same id flips to non-live => removed
	if ix.LiveCount() != 0 {
		t.Fatalf("LiveCount after retire upsert = %d, want 0", ix.LiveCount())
	}
}

func TestRemoveDropsItem(t *testing.T) {
	ix := New()
	ix.Upsert(ixItem("id-1", "alpha", "team", model.ItemFact, nil, 0.9, "alpha body"))
	ix.Remove("id-1")
	if ix.LiveCount() != 0 {
		t.Fatal("Remove should drop the doc")
	}
	ix.Remove("never-added")
	if ix.LiveCount() != 0 {
		t.Fatal("Remove missing id must be a no-op")
	}
}

func TestSearchAppliesFilters(t *testing.T) {
	ix := New()
	ix.Upsert(ixItem("id-team", "team only fact", "team", model.ItemFact, []string{"golang"}, 0.9, "for team"))
	ix.Upsert(ixItem("id-agent", "agent only fact", "agent", model.ItemFact, nil, 0.9, "for agent"))
	ix.Upsert(ixItem("id-warn", "warning item", "team", model.ItemWarning, nil, 0.9, "be careful"))
	ix.Upsert(ixItem("id-low", "low confidence item", "team", model.ItemFact, nil, 0.2, "uncertain"))

	ids := func(hits []Hit) map[string]bool {
		m := map[string]bool{}
		for _, h := range hits {
			m[h.ID] = true
		}
		return m
	}

	team := mustSearch(ix, model.Query{Scope: model.ScopeTeam, Limit: 50})
	if !ids(team)["id-team"] || !ids(team)["id-warn"] || ids(team)["id-agent"] {
		t.Errorf("scope filter leak: %+v", ids(team))
	}
	kind := mustSearch(ix, model.Query{Scope: model.ScopeTeam, Kinds: []model.ItemKind{model.ItemWarning}, Limit: 50})
	if len(kind) != 1 || kind[0].ID != "id-warn" {
		t.Errorf("kind filter = %+v", kind)
	}
	tag := mustSearch(ix, model.Query{Scope: model.ScopeTeam, Tags: []string{"golang"}, Limit: 50})
	if len(tag) != 1 || tag[0].ID != "id-team" {
		t.Errorf("tag filter = %+v", tag)
	}
	conf := mustSearch(ix, model.Query{Scope: model.ScopeTeam, MinConfidence: 0.8, Limit: 50})
	if ids(conf)["id-low"] {
		t.Error("min-confidence filter leaked a low-confidence item")
	}
}

func TestSearchRespectsLimitDeterministicOrder(t *testing.T) {
	ix := New()
	for k := 0; k < 5; k++ {
		ix.Upsert(ixItem(idN(k), "shared topic", "team", model.ItemFact, nil, 0.9, "some body text"))
	}
	if got := mustSearch(ix, model.Query{Limit: 2}); len(got) != 2 {
		t.Fatalf("limit 2 returned %d", len(got))
	}
	// Empty text => equal scores => stable ID-desc tiebreak.
	first := mustSearch(ix, model.Query{Limit: 5})
	second := mustSearch(ix, model.Query{Limit: 5})
	if len(first) != 5 {
		t.Fatalf("limit 5 returned %d", len(first))
	}
	for k := range first {
		if first[k].ID != second[k].ID {
			t.Fatalf("non-deterministic order: %v vs %v", first, second)
		}
		if k > 0 && first[k-1].ID < first[k].ID {
			t.Fatalf("tiebreak not ID-desc: %v", first)
		}
	}
}

func TestSearchEmptyIndexNoError(t *testing.T) {
	hits, err := New().Search(model.Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("empty index returned %+v", hits)
	}
}

func TestConcurrentSearchDuringWrites(t *testing.T) {
	ix := New()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	// writer flips items in/out
	wg.Add(1)
	go func() {
		defer wg.Done()
		k := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			it := ixItem(idN(k), "concurrent fact", "team", model.ItemFact, nil, 0.9, "body under churn")
			ix.Upsert(it)
			ix.Remove(it.ID)
			k++
		}
	}()
	// readers hammer the snapshot under RLock
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := ix.Search(model.Query{Limit: 10}); err != nil {
					t.Errorf("search: %v", err)
					return
				}
				_ = ix.LiveCount()
			}
		}()
	}
	// let it churn briefly, then stop
	for i := 0; i < 500; i++ {
		_ = i
	}
	close(stop)
	wg.Wait()
}

func mustSearch(ix *Index, q model.Query) []Hit {
	hits, err := ix.Search(q)
	if err != nil {
		panic(err)
	}
	return hits
}

func idN(k int) string {
	return "id-" + string(rune('a'+k)) // id-a..id-e, already reverse-stable when one char
}
