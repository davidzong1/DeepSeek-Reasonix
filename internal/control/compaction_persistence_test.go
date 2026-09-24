package control

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func compactionPersistenceHistory(tools bool) *agent.Session {
	s := agent.NewSession("system")
	if tools {
		s.Add(provider.Message{Role: provider.RoleUser, Content: "inspect the files"})
		for i := range 3 {
			id := fmt.Sprintf("call-%d", i)
			s.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: id, Name: "read_file", Arguments: `{}`}}})
			s.Add(provider.Message{Role: provider.RoleTool, ToolCallID: id, Name: "read_file", Content: strings.Repeat("x", 16_000)})
		}
	} else {
		for i := range 24 {
			s.Add(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("task %d", i)})
			s.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("x", 2_000)})
		}
	}
	s.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "ready"})
	return s
}

func TestContextMaintenanceProjectionSurvivesSessionSwitch(t *testing.T) {
	for _, mode := range []string{"summary", "prune", "truncate", "auto-summary", "auto-prune", "auto-truncate"} {
		t.Run(mode, func(t *testing.T) {
			service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v5")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
			runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "maintenance"})
			if err != nil {
				t.Fatal(err)
			}
			providerMock := testutil.NewMock("maintenance", testutil.Turn{Text: "durable summary"}, testutil.Turn{Text: "final answer"}, testutil.Turn{Text: "final answer"})
			if strings.Contains(mode, "truncate") {
				providerMock = testutil.NewMock("maintenance", testutil.Turn{StreamError: errors.New("summary unavailable")}, testutil.Turn{Text: "final answer"})
			}
			exec := agent.New(providerMock, tool.NewRegistry(), compactionPersistenceHistory(strings.Contains(mode, "prune")), agent.Options{ContextWindow: 10_000, CompactRatio: .8}, event.Discard)
			controller := newOwnedTestController(t, Options{Runner: exec, Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})

			canonicalBefore := runtime.Session().ExecutionSnapshot().Projection.Messages
			if strings.HasPrefix(mode, "auto-") {
				err = controller.RunTurn(t.Context(), "continue the task")
			} else if mode == "prune" {
				err = exec.PrepareContext(t.Context())
			} else {
				err = controller.Compact(t.Context(), "")
			}
			if err != nil {
				t.Fatal(err)
			}

			wantModel := provider.ModelMessages(exec.ModelHistorySnapshot())
			snapshot := runtime.Session().ExecutionSnapshot()
			if !reflect.DeepEqual(snapshot.Projection.ModelMessages, wantModel) {
				t.Fatal("durable model projection differs from the installed projection")
			}
			if !strings.HasPrefix(mode, "auto-") && !reflect.DeepEqual(snapshot.Projection.Messages, canonicalBefore) {
				t.Fatal("context maintenance rewrote canonical history")
			}

			other, err := service.Create(t.Context(), session.CreateOptions{SessionID: "other"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := controller.OpenSession(t.Context(), other.Ref()); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.OpenSession(t.Context(), runtime.Ref()); err != nil {
				t.Fatal(err)
			}
			if got := provider.ModelMessages(exec.ModelHistorySnapshot()); !reflect.DeepEqual(got, wantModel) {
				t.Fatal("model projection changed after switching away and reopening")
			}
		})
	}
}

// A history reload of the writer's own durable log must keep the fold. The
// team window polls that log after every settled turn, including a cancelled
// one, and replacing the live transcript with the canonical page is what made
// the context gauge size the whole history.
func TestReloadHistoryKeepsTheCompactedProjection(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v5")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "reload-fold"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(testutil.NewMock("fold", testutil.Turn{Text: "durable summary"}), tool.NewRegistry(), compactionPersistenceHistory(false), agent.Options{ContextWindow: 10_000, CompactRatio: .8}, event.Discard)
	controller := newOwnedTestController(t, Options{Runner: exec, Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	if err := controller.Compact(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if !exec.ProjectionValid() {
		t.Fatal("compaction did not install a projection")
	}
	before := len(provider.ModelMessages(exec.ModelHistorySnapshot()))
	usedBefore, _ := controller.ContextSnapshot()
	if _, err := controller.ReloadHistoryIfChanged(t.Context(), controller.HistoryStamp()); err != nil {
		t.Fatal(err)
	}
	if !exec.ProjectionValid() {
		t.Fatal("reload dropped the projection")
	}
	if got := len(provider.ModelMessages(exec.ModelHistorySnapshot())); got != before {
		t.Fatalf("visible history = %d messages, want the folded %d", got, before)
	}
	usedAfter, _ := controller.ContextSnapshot()
	if usedAfter != usedBefore {
		t.Fatalf("context gauge = %d, want the folded %d", usedAfter, usedBefore)
	}

	// A prefix edit drops the fold, so the gauge sizes the whole canonical
	// log. Reloading the durable history puts that prefix back and the fold
	// applies again.
	edited := append([]provider.Message(nil), exec.Session().Snapshot()...)
	edited[len(edited)/2].Content += " broken prefix"
	exec.Session().Rewrite(edited, "test-break-fold")
	if exec.ProjectionValid() {
		t.Fatal("a covered-prefix edit must drop the fold")
	}
	broken, _ := controller.ContextSnapshot()
	if broken <= usedBefore {
		t.Fatalf("unfolded gauge = %d, want more than the folded %d", broken, usedBefore)
	}
	if _, err := controller.ReloadHistoryIfChanged(t.Context(), controller.HistoryStamp()+"-again"); err != nil {
		t.Fatal(err)
	}
	if !exec.ProjectionValid() {
		t.Fatal("durable reload did not restore the fold")
	}
	restored, _ := controller.ContextSnapshot()
	if restored != usedBefore {
		t.Fatalf("restored gauge = %d, want the folded %d", restored, usedBefore)
	}
}
