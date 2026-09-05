package quality

import (
	"testing"

	"reasonix/internal/knowledge_base/model"
)

func baseItem(author, title, body string, conf float64) model.KnowledgeItem {
	return model.KnowledgeItem{
		AuthorID: author, Title: title, Kind: model.ItemDecision, Scope: model.ScopeTeam,
		Provenance: []model.Ref{{Kind: "decision", Target: "t1"}},
		Quality:    model.QualitySignal{Confidence: conf},
		Version:    1, Status: model.StatusLive, Body: body,
	}
}

func TestGateLiveVsDraft(t *testing.T) {
	g := DefaultGate()
	live := baseItem("a", "high", "body high confidence", 0.95)
	g.Apply(&live)
	if live.Status != model.StatusLive || live.Quality.Suspect {
		t.Fatalf("high conf clean item should be live: %+v", live)
	}
	low := baseItem("a", "low", "garbled lorem ipsum placeholder", 0.95)
	g.Apply(&low)
	if low.Status != model.StatusDraft || !low.Quality.Suspect {
		t.Fatalf("garbled item must be draft+suspect: %+v", low)
	}
	noprov := baseItem("a", "noprov", "body", 0.95)
	noprov.Provenance = nil
	g.Apply(&noprov)
	if noprov.Status != model.StatusDraft {
		t.Fatalf("item without provenance must not be live: %+v", noprov)
	}
}

func TestDecideExactDedupFirst(t *testing.T) {
	existing := []model.KnowledgeItem{baseItem("a", "same", "exact body", 0.9)}
	incoming := existing[0]
	incoming.ID = model.NewID() // same content, new id
	if d := Decide(existing, incoming); d.Action != ActionSkip {
		t.Fatalf("exact duplicate should skip, got %+v", d)
	}
}

func TestDecideStoreNewWhenSlotEmpty(t *testing.T) {
	incoming := baseItem("a", "fresh", "new body", 0.9)
	incoming.Canonical = model.CanonicalKey(model.ScopeTeam, incoming.Kind, incoming.Title)
	if d := Decide(nil, incoming); d.Action != ActionStoreNew {
		t.Fatalf("empty slot should store new, got %+v", d)
	}
}

func TestDecideSupersedeSameAuthor(t *testing.T) {
	prev := baseItem("a", "slot", "v1 body", 0.9)
	prev.Version = 1
	next := baseItem("a", "slot", "v2 body", 0.9)
	d := Decide([]model.KnowledgeItem{prev}, next)
	if d.Action != ActionSupersede || d.ExistingID != prev.ID {
		t.Fatalf("same author should supersede prev, got %+v", d)
	}
}

func TestDecideConflictCrossAuthor(t *testing.T) {
	prev := baseItem("alice", "slot", "alice body", 0.9)
	next := baseItem("bob", "slot", "bob body", 0.9)
	if d := Decide([]model.KnowledgeItem{prev}, next); d.Action != ActionConflict {
		t.Fatalf("cross author live conflict should not overwrite: %+v", d)
	}
}

func TestDecideDraftNeverOverwritesLive(t *testing.T) {
	prev := baseItem("a", "slot", "live body", 0.9)
	incoming := baseItem("a", "slot", "draft candidate body", 0.2)
	incoming.Status = model.StatusDraft
	if d := Decide([]model.KnowledgeItem{prev}, incoming); d.Action != ActionSkip {
		t.Fatalf("draft against live slot must skip, got %+v", d)
	}
}
