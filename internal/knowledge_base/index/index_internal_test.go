package index

import (
	"testing"

	"reasonix/internal/knowledge_base/model"
)

func coderDoc(id, title, body string, kind model.ItemKind, scope model.Scope, tags []string, status model.Status) model.KnowledgeItem {
	return model.KnowledgeItem{
		ID: id, Title: title, Kind: kind, Scope: scope, Tags: tags,
		Status: status, Version: 1, Body: body,
		Canonical: model.CanonicalKey(scope, kind, title),
	}
}

func TestRebuildIndexesLiveOnly(t *testing.T) {
	ix := New()
	items := []model.KnowledgeItem{
		coderDoc("id-live", "单写队列", "队列入队使用单写队列保证顺序", model.ItemDecision, model.ScopeTeam, nil, model.StatusLive),
		coderDoc("id-draft", "草稿待审", "低置信内容不进入索引", model.ItemWarning, model.ScopeTeam, nil, model.StatusDraft),
	}
	ix.Rebuild(items)
	if n := ix.LiveCount(); n != 1 {
		t.Fatalf("LiveCount = %d, want 1 (draft excluded)", n)
	}
	hits, err := ix.Search(model.Query{Text: "单写队列"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "id-live" {
		t.Fatalf("CJK query hits = %+v, want id-live", hits)
	}
}

func TestUpsertRemovesOnTransition(t *testing.T) {
	ix := New()
	it := coderDoc("x", "backend go", "we use go for backend scoring", model.ItemDecision, model.ScopeTeam, nil, model.StatusLive)
	ix.Upsert(it)
	if ix.LiveCount() != 1 {
		t.Fatal("live item not indexed")
	}
	retired := it
	retired.Status = model.StatusRetired
	ix.Upsert(retired)
	if ix.LiveCount() != 0 {
		t.Fatalf("retired item must leave index, count=%d", ix.LiveCount())
	}
}

func TestSearchFilters(t *testing.T) {
	ix := New()
	ix.Upsert(coderDoc("d", "后端使用Go", "后端语言决定采用Go", model.ItemDecision, model.ScopeTeam, []string{"go"}, model.StatusLive))
	ix.Upsert(coderDoc("w", "禁止外部库", "不要引入外部向量库作为依赖", model.ItemConstraint, model.ScopeTeam, []string{"policy"}, model.StatusLive))
	// kind filter excludes the constraint
	hits, _ := ix.Search(model.Query{Text: "后端语言", Kinds: []model.ItemKind{model.ItemDecision}})
	if len(hits) != 1 || hits[0].ID != "d" {
		t.Fatalf("kind-filtered hits = %+v", hits)
	}
	// tag filter requires the tag present
	if hits, _ := ix.Search(model.Query{Text: "外部向量", Tags: []string{"policy"}}); len(hits) != 1 || hits[0].ID != "w" {
		t.Fatalf("tag-filtered hits = %+v", hits)
	}
	// scope filter with wrong scope excludes all
	if hits, _ := ix.Search(model.Query{Text: "外部向量", Scope: model.ScopeGlobal}); len(hits) != 0 {
		t.Fatalf("scope-filtered hits should be empty: %+v", hits)
	}
}

func TestSearchLimitAndEmptyQuery(t *testing.T) {
	ix := New()
	for i := 0; i < 5; i++ {
		ix.Upsert(coderDoc(string(rune('a'+i)), "item", "some stable body text", model.ItemFact, model.ScopeTeam, nil, model.StatusLive))
	}
	hits, _ := ix.Search(model.Query{Text: "", Limit: 3})
	if len(hits) != 3 {
		t.Fatalf("empty-query limit hits = %d, want 3", len(hits))
	}
}
