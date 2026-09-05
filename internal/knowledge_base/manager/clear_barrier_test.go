package manager

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/knowledge_base/model"
)

// ClearTeam is the write linearization point: an Ingest issued after a
// ClearTeam is accepted must never land in the log that is being moved to
// .trash. This test drives the accepted clear and the follow-up ingest back to
// back from one goroutine, so any window where the ingest could be lost would
// surface as a missing item (deterministic in outcome under the barrier).
func TestClearBarrier_IngestAfterAcceptedClearSurvives(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{e2eThought("decision: doomed before clear", "alice")})
	if got := len(e2eQueryAll(t, m)); got != 1 {
		t.Fatalf("pre-clear live = %d, want 1", got)
	}

	if err := m.ClearTeam(context.Background(), "alpha", model.ScopeTeam); err != nil {
		t.Fatalf("ClearTeam: %v", err)
	}
	// Accepted after the clear: must wait out the swap and land on the fresh
	// queue, never be dropped into the trashed log.
	if _, err := m.Ingest(context.Background(), []model.Thought{
		e2eThought("decision: survives the clear", "alice"),
	}); err != nil {
		t.Fatalf("Ingest after ClearTeam: %v", err)
	}
	e2eFlush(t, m)

	res := e2eQueryAll(t, m)
	if len(res) != 1 || !strings.Contains(res[0].Item.Body, "survives the clear") {
		t.Fatalf("post-clear live = %d items, want the single survives-the-clear item", len(res))
	}
}

// ClearTeam must not silently ignore its scope parameter: only team scope is
// meaningful on a per-team facade, anything else fails closed synchronously.
func TestClearBarrier_NonTeamScopeRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	for _, scope := range []model.Scope{model.ScopeProject, model.ScopeAgent, model.ScopeGlobal} {
		if err := m.ClearTeam(context.Background(), "alpha", scope); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("ClearTeam scope %q = %v, want ErrInvalid", scope, err)
		}
	}
}
