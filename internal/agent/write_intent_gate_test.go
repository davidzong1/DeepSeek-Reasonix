package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
	"reasonix/internal/workspacelease"
)

// probeWriteIntentGate is a gate that records what it was asked for, so a test
// can assert the agent consulted it — and released it — without a real token.
type probeWriteIntentGate struct {
	mu       sync.Mutex
	intents  []WriteIntent
	released int
	// wait, when set, is reported to every caller: the shape of a gate that
	// always has to queue behind a peer.
	wait *WriteIntentWait
	// err, when set, is returned instead of a release.
	err error
}

func (g *probeWriteIntentGate) gate(_ context.Context, intent WriteIntent, onWait func(WriteIntentWait)) (func(), error) {
	g.mu.Lock()
	g.intents = append(g.intents, intent)
	wait, err := g.wait, g.err
	g.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if wait != nil && onWait != nil {
		onWait(*wait)
	}
	return func() {
		g.mu.Lock()
		g.released++
		g.mu.Unlock()
	}, nil
}

func (g *probeWriteIntentGate) recorded() ([]WriteIntent, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]WriteIntent(nil), g.intents...), g.released
}

func TestAgentConsultsTheWriteIntentGateForAWriteToolCall(t *testing.T) {
	root := t.TempDir()
	gate := &probeWriteIntentGate{}
	owner, err := workspacelease.New(root, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	reg.Add(&recordingWriter{name: "write_file"})
	a := New(nil, reg, NewSession(""), Options{
		WorkspaceLease:     owner,
		WriteIntentGate:    gate.gate,
		WriteWorkspaceRoot: root,
	}, event.Discard)

	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID:        "write-1",
		Name:      "write_file",
		Arguments: string(mustJSON(t, map[string]string{"path": filepath.Join(root, "internal", "a.go"), "content": "one"})),
	})
	if out.blocked || out.errMsg != "" {
		t.Fatalf("write call: %+v", out)
	}
	intents, released := gate.recorded()
	if len(intents) != 1 {
		t.Fatalf("gate saw %d intents, want exactly one per write call: %+v", len(intents), intents)
	}
	if intents[0].Whole {
		t.Fatalf("a path-bound write must not claim the whole workspace: %+v", intents[0])
	}
	if want := []string{filepath.Join(root, "internal", "a.go")}; len(intents[0].Paths) != 1 || intents[0].Paths[0] != want[0] {
		t.Fatalf("gate scope = %v, want %v", intents[0].Paths, want)
	}
	if intents[0].Label != filepath.Join("internal", "a.go") {
		t.Fatalf("gate label = %q, want the workspace-relative path", intents[0].Label)
	}
	if released != 1 {
		t.Fatalf("gate released %d times, want the write call to release its intent", released)
	}
}

func TestAcquireWriteIntentReportsTheWaitOnTheSessionStream(t *testing.T) {
	root := t.TempDir()
	sink := &recordSink{}
	gate := &probeWriteIntentGate{wait: &WriteIntentWait{
		Holder: "member m2 of team alpha",
		Scope:  "internal/shared.go",
	}}
	a := New(nil, tool.NewRegistry(), NewSession(""), Options{
		WriteIntentGate:    gate.gate,
		WriteWorkspaceRoot: root,
	}, sink)

	release := a.acquireWriteIntent(context.Background(), WriteIntent{
		Paths: []string{filepath.Join(root, "internal", "shared.go")},
		Label: "internal/shared.go",
	})
	notices := sink.kinds(event.Notice)
	if len(notices) != 1 {
		t.Fatalf("notices = %d, want the queue wait reported while it happens: %+v", len(notices), sink.evs)
	}
	notice := notices[0]
	if notice.Code != event.NoticeCodeWorkspaceLease || notice.Level != event.LevelInfo {
		t.Fatalf("wait notice = %+v, want an info workspace-lease notice", notice)
	}
	for _, want := range []string{"member m2 of team alpha", "internal/shared.go"} {
		if !strings.Contains(notice.Text, want) || !strings.Contains(notice.Detail, want) {
			t.Fatalf("wait notice %+v must name %q", notice, want)
		}
	}
	if _, released := gate.recorded(); released != 0 {
		t.Fatal("the intent was released before its caller said so")
	}
	release()
	if _, released := gate.recorded(); released != 1 {
		t.Fatal("the returned release must release the gate intent")
	}
}

func TestAcquireWriteIntentDegradesWithoutAToken(t *testing.T) {
	root := t.TempDir()
	sink := &recordSink{}
	broken := &probeWriteIntentGate{err: errors.New("token unavailable")}
	a := New(nil, tool.NewRegistry(), NewSession(""), Options{
		WriteIntentGate:    broken.gate,
		WriteWorkspaceRoot: root,
	}, sink)

	release := a.acquireWriteIntent(context.Background(), WriteIntent{
		Paths: []string{filepath.Join(root, "a.go")}, Label: "a.go",
	})
	release()
	if len(sink.evs) != 0 {
		t.Fatalf("a token failure must not emit a wait notice: %+v", sink.evs)
	}

	// No gate at all is the pre-token shape: a no-op release, never a panic.
	plain := New(nil, tool.NewRegistry(), NewSession(""), Options{WriteWorkspaceRoot: root}, event.Discard)
	plain.acquireWriteIntent(context.Background(), WriteIntent{Whole: true})()
	var nilAgent *Agent
	nilAgent.acquireWriteIntent(context.Background(), WriteIntent{Whole: true})()
}

func TestWriteIntentLabelNamesTheScopeForPeers(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name  string
		scope []string
		whole bool
		want  string
	}{
		{name: "whole", whole: true, want: "the whole workspace"},
		{name: "empty", want: "the workspace"},
		{name: "single", scope: []string{filepath.Join(root, "internal", "a.go")}, want: filepath.Join("internal", "a.go")},
		{name: "several", scope: []string{filepath.Join(root, "a.go"), filepath.Join(root, "b.go")}, want: "a.go (+1 more)"},
		{name: "outside", scope: []string{filepath.Join(filepath.Dir(root), "elsewhere.go")}, want: filepath.Join(filepath.Dir(root), "elsewhere.go")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := writeIntentLabel(root, tc.scope, tc.whole); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNoteWriteIntentWaitIsSafeWithoutASink(t *testing.T) {
	a := New(nil, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	a.svc.sink = nil
	a.noteWriteIntentWait(WriteIntentWait{Holder: "member m2", Scope: "internal/a.go"})
	var nilAgent *Agent
	nilAgent.noteWriteIntentWait(WriteIntentWait{Holder: "member m3"})
}
