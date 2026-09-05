package boot

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/knowledge_base/adapter"
	"reasonix/internal/knowledge_base/manager"
	"reasonix/internal/knowledge_base/model"
)

// TestOpenTeamKnowledgeThroughRealBuild is the F7 host-wiring assembly test:
// it builds the real runtime stack, opens the team knowledge base through the
// exported boot entry (OpenTeamKnowledge), and proves the assembled Manager is
// live — a Thought ingested through it becomes a queryable live item. The KB
// host assembly therefore coexists with a real controller build and its
// events round-trip end to end.
func TestOpenTeamKnowledgeThroughRealBuild(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	ctrl, err := Build(context.Background(), Options{Sink: event.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	tk, err := OpenTeamKnowledge(&KBHost{OnEvent: func(adapter.Event) {}}, "alpha", filepath.Join(dir, "kb"))
	if err != nil {
		t.Fatalf("OpenTeamKnowledge: %v", err)
	}
	defer tk.Close()
	if tk.Team != "alpha" {
		t.Fatalf("knowledge base bound to %q, want alpha", tk.Team)
	}

	ctx := context.Background()
	if _, err := tk.Manager.Ingest(ctx, []model.Thought{{
		ID: model.NewID(), TeamID: "alpha", AgentID: "alice",
		Kind: model.ThoughtDecision,
		Text: "decision: use sqlite for the audit store\nwe chose sqlite over files for transactional queries",
	}}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := tk.Manager.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got, err := tk.Manager.Query(ctx, model.Query{Text: "sqlite", Scope: model.ScopeTeam, Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("query returned %d live items, want the ingested decision", len(got))
	}
	item := got[0].Item
	if item.AuthorID != "alice" || !item.Live() || item.Kind != model.ItemDecision {
		t.Fatalf("ingested item %+v is not the live alice decision", item)
	}
}

// TestTeamKnowledgeCloseRefusesLateIngest pins the owned-handle lifecycle:
// after Close the worker is stopped, so a follow-up Ingest is refused with
// manager.ErrClosed rather than enqueueing onto a dead manager.
func TestTeamKnowledgeCloseRefusesLateIngest(t *testing.T) {
	tk, err := OpenTeamKnowledge(&KBHost{}, "alpha", filepath.Join(t.TempDir(), "kb"))
	if err != nil {
		t.Fatalf("OpenTeamKnowledge: %v", err)
	}
	if err := tk.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = tk.Manager.Ingest(context.Background(), []model.Thought{{
		TeamID: "alpha", AgentID: "alice", Kind: model.ThoughtConclusion,
		Text: "conclusion: close must have stopped the kb worker",
	}})
	if !errors.Is(err, manager.ErrClosed) {
		t.Fatalf("Ingest after Close = %v, want manager.ErrClosed", err)
	}
}
