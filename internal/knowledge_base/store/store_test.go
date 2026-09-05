package store

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/knowledge_base/model"
)

func item(id, title string, body string, version int, status model.Status) model.KnowledgeItem {
	if id == "" {
		id = model.NewID()
	}
	now := time.Now()
	return model.KnowledgeItem{
		ID: id, Title: title, Kind: model.ItemDecision, Scope: model.ScopeTeam,
		Provenance: []model.Ref{{Kind: "decision", Target: "th-1"}},
		Quality:    model.QualitySignal{Confidence: 0.9, ReviewLevel: model.ReviewNone},
		Version:    version, Status: status,
		CreatedAt: now, UpdatedAt: now,
		Body:      body,
		Canonical: model.CanonicalKey(model.ScopeTeam, model.ItemDecision, title),
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "alpha"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestPutRoundTrip(t *testing.T) {
	s := openTest(t)
	i := item("", "Use Go for backend", "We chose Go for the backend.", 1, model.StatusLive)
	created, err := s.Put(i)
	if err != nil || !created {
		t.Fatalf("Put created=%v err=%v", created, err)
	}
	got, err := s.Get(i.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != i.Title || got.Body != i.Body || got.Status != model.StatusLive || got.Version != 1 {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.CreatedAt.Equal(i.CreatedAt) {
		t.Errorf("created_at drifted: %v vs %v", got.CreatedAt, i.CreatedAt)
	}
	if _, serr := os.Stat(filepath.Join(s.Root(), "items", i.ID+".md")); serr != nil {
		t.Errorf("item file not under Root()/items: %v", serr)
	}
}

func TestPutCreateOnly(t *testing.T) {
	s := openTest(t)
	i := item("", "Create only", "identical bytes", 1, model.StatusLive)
	if _, err := s.Put(i); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	created, err := s.Put(i)
	if err != nil || created {
		t.Errorf("re-Put identical = created %v err %v, want (false, nil)", created, err)
	}
	other := i
	other.Body = "different bytes on the same id"
	if _, err := s.Put(other); !errors.Is(err, ErrConflict) {
		t.Errorf("re-Put same id diff content = %v, want ErrConflict", err)
	}
}

func TestPutRejectsEscapingID(t *testing.T) {
	s := openTest(t)
	for _, id := range []string{"../x", "a/b", "a\\b", "..", "."} {
		i := item(id, "bad", "should not land", 1, model.StatusLive)
		if _, err := s.Put(i); err == nil {
			t.Errorf("Put id %q accepted; want rejection", id)
		}
	}
	if entries := listFiles(t, s.itemsDir); len(entries) != 0 {
		t.Errorf("escaping ids wrote files: %v", entries)
	}
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	es, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestGetMissing(t *testing.T) {
	s := openTest(t)
	if _, err := s.Get("no-such-item-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}
}

func TestTransitionRetirePreservesBody(t *testing.T) {
	s := openTest(t)
	i := item("", "Retire me", "body that must survive", 1, model.StatusLive)
	if _, err := s.Put(i); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(time.Minute)
	if err := s.Transition(i.ID, func(x *model.KnowledgeItem) error {
		x.Status = model.StatusRetired
		x.UpdatedAt = ts
		return nil
	}); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	got, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusRetired || got.Body != i.Body {
		t.Errorf("after retire status=%s body=%q", got.Status, got.Body)
	}
	if err := s.Transition("missing-id", func(x *model.KnowledgeItem) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("Transition missing = %v, want ErrNotFound", err)
	}
}

func TestLatestLiveHighestVersion(t *testing.T) {
	s := openTest(t)
	a := item("", "same slot", "v1 text", 1, model.StatusLive)
	b := item("", "same slot", "v2 text", 2, model.StatusLive)
	if _, err := s.Put(a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(b); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestLive(a.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Version != 2 {
		t.Fatalf("LatestLive version = %+v, want v2", got)
	}
	all, err := s.ByCanonical(a.Canonical)
	if err != nil || len(all) != 2 {
		t.Errorf("ByCanonical len=%d err=%v", len(all), err)
	}
}

func TestHasContentHash(t *testing.T) {
	s := openTest(t)
	i := item("", "hash me", "findable body", 1, model.StatusLive)
	if _, err := s.Put(i); err != nil {
		t.Fatal(err)
	}
	if !s.HasContentHash(model.ItemContentHash(i)) {
		t.Error("HasContentHash should find the stored item")
	}
	if s.HasContentHash("deadbeef") {
		t.Error("HasContentHash false positive")
	}
}

func TestConcurrentPutDistinct(t *testing.T) {
	s := openTest(t)
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for k := 0; k < n; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			i := item("", "concurrent-title", "distinct body", 1, model.StatusLive)
			_, errs[k] = s.Put(i)
		}(k)
	}
	wg.Wait()
	for k := range errs {
		if errs[k] != nil {
			t.Fatalf("concurrent Put #%d: %v", k, errs[k])
		}
	}
	if all, err := s.List(); err != nil || len(all) != n {
		t.Errorf("List len=%d err=%v, want %d", len(all), err, n)
	}
}
