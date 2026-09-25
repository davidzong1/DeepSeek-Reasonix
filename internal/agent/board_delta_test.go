package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// boardDeltaProbe is one scripted turn pair: a tool round, then a final answer.
// Two sampling steps are what makes a delta observable as a delta — a
// single-step turn cannot tell "appended once" from "appended every step". It
// returns every request the loop sent plus the transcript the turn left behind.
func boardDeltaProbe(t *testing.T, install func(*Agent)) ([]provider.Request, []provider.Message) {
	t.Helper()
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "probe", Arguments: `{}`}}},
		testutil.Turn{Text: "done"},
	)
	reg := tool.NewRegistry()
	reg.Add(okTool{name: "probe"})
	a := New(mp, reg, NewSession(""), Options{}, event.Discard)
	install(a)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return mp.Requests(), a.Session().Snapshot()
}

// requestShape is the comparable form of every request the loop sent and every
// message the turn committed: one "request|role|content" entry per message, so a
// difference in count, order or bytes shows up as a slice difference rather than
// a struct-field diff. Passing a nil reqs slice yields the transcript alone,
// which is the stable oracle for "how many times was this written".
func requestShape(reqs []provider.Request, committed []provider.Message) []string {
	var out []string
	for i, req := range reqs {
		for _, m := range req.Messages {
			out = append(out, fmt.Sprintf("%d|%s|%s", i, m.Role, m.Content))
		}
	}
	for _, m := range committed {
		out = append(out, fmt.Sprintf("session|%s|%s", m.Role, m.Content))
	}
	return out
}

// TestBoardDeltaWithoutHookKeepsTheSamplingShape pins the byte-stability
// contract: an agent with no hook and an agent whose hook is explicitly nil
// must send identical requests.
func TestBoardDeltaWithoutHookKeepsTheSamplingShape(t *testing.T) {
	reqs, committed := boardDeltaProbe(t, func(*Agent) {})
	control := requestShape(reqs, committed)
	reqs, committed = boardDeltaProbe(t, func(a *Agent) { a.SetBoardDelta(nil, nil) })
	explicitNil := requestShape(reqs, committed)
	if !slices.Equal(control, explicitNil) {
		t.Fatalf("a nil hook changed the request shape:\n control=%v\n   nil=%v", control, explicitNil)
	}
	if len(control) == 0 {
		t.Fatal("the probe sent no request; the comparison would pass vacuously")
	}
}

// TestBoardDeltaEmptyResultAppendsNothing pins the empty-increment path: no
// message, no acknowledgement, and a request identical to the control arm.
func TestBoardDeltaEmptyResultAppendsNothing(t *testing.T) {
	reqs, committed := boardDeltaProbe(t, func(*Agent) {})
	control := requestShape(reqs, committed)
	var acks int
	reqs, committed = boardDeltaProbe(t, func(a *Agent) {
		a.SetBoardDelta(func(context.Context) (string, error) { return "", nil }, func() { acks++ })
	})
	empty := requestShape(reqs, committed)
	if !slices.Equal(control, empty) {
		t.Fatalf("an empty delta changed the request shape:\n control=%v\n   empty=%v", control, empty)
	}
	if acks != 0 {
		t.Fatalf("an empty delta must not be acknowledged, got %d acks", acks)
	}
}

// TestBoardDeltaTextRidesExactlyOneSamplingStep pins the delta's whole life:
// it lands before the first sampling step of the turn, only that step carries
// it as the tail, and the following step neither re-appends it nor re-acks it.
func TestBoardDeltaTextRidesExactlyOneSamplingStep(t *testing.T) {
	const delta = "[board delta]\n- m2 topic: summary"
	var reads, acks int
	reqs, committed := boardDeltaProbe(t, func(a *Agent) {
		a.SetBoardDelta(func(context.Context) (string, error) {
			reads++
			if reads == 1 {
				return delta, nil
			}
			return "", nil
		}, func() { acks++ })
	})

	if len(reqs) != 2 {
		t.Fatalf("the probe should sample twice, got %d requests", len(reqs))
	}
	first := reqs[0].Messages
	if n := len(first); n == 0 || first[n-1].Role != provider.RoleUser || first[n-1].Content != delta {
		t.Fatalf("the delta must be the last message of the first request, got %+v", first)
	}
	// The transcript is the stable oracle: the delta is committed exactly once,
	// so a second step that re-appended the same page would show twice there.
	if got := strings.Count(strings.Join(requestShape(nil, committed), "\n"), delta); got != 1 {
		t.Fatalf("the delta must be committed exactly once, found %d copies", got)
	}
	// The second step's tail is its own tool result, not a fresh copy of the
	// delta: the hook returned empty for that step and nothing was written.
	second := reqs[1].Messages
	if n := len(second); n == 0 || second[n-1].Content == delta {
		t.Fatalf("the second step must not re-append the delta, tail = %+v", second[max(0, len(second)-1):])
	}
	if acks != 1 {
		t.Fatalf("a delivered delta must be acknowledged exactly once, got %d", acks)
	}
}

// TestBoardDeltaReadFailureSkipsInjectionAndKeepsSampling pins the fail-open
// rule: a board error is swallowed, writes nothing into the prompt, and never
// fails the turn.
func TestBoardDeltaReadFailureSkipsInjectionAndKeepsSampling(t *testing.T) {
	reqs, committed := boardDeltaProbe(t, func(*Agent) {})
	control := requestShape(reqs, committed)
	var acks int
	reqs, committed = boardDeltaProbe(t, func(a *Agent) {
		a.SetBoardDelta(func(context.Context) (string, error) {
			return "", errors.New("board is down")
		}, func() { acks++ })
	})
	broken := requestShape(reqs, committed)
	if !slices.Equal(control, broken) {
		t.Fatalf("a read failure changed the request shape:\n control=%v\n  broken=%v", control, broken)
	}
	if acks != 0 {
		t.Fatalf("a failed read must not be acknowledged, got %d acks", acks)
	}
}

// boardDeltaSteerTool queues mid-turn guidance from inside the first round, so
// the second round carries both a steer and a delta and their order is fixed.
type boardDeltaSteerTool struct {
	agent *Agent
	text  string
}

func (boardDeltaSteerTool) Name() string        { return "steer_probe" }
func (boardDeltaSteerTool) Description() string { return "queues a steer" }
func (boardDeltaSteerTool) ReadOnly() bool      { return true }
func (boardDeltaSteerTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t boardDeltaSteerTool) Execute(context.Context, json.RawMessage) (string, error) {
	if !t.agent.Steer(t.text) {
		return "", errors.New("steer refused")
	}
	return "ok", nil
}

// TestBoardDeltaLandsAfterTheQueuedSteerBatch pins the loop's ordering claim:
// the steer batch is applied first and the board delta after it, so a delta
// never separates a batch of guidance from the step that reads it.
func TestBoardDeltaLandsAfterTheQueuedSteerBatch(t *testing.T) {
	const delta = "[board delta]\n- m2 topic: summary"
	const steer = "use plan B"
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "steer_probe", Arguments: `{}`}}},
		testutil.Turn{Text: "done"},
	)
	reg := tool.NewRegistry()
	a := New(mp, reg, NewSession(""), Options{}, event.Discard)
	reg.Add(boardDeltaSteerTool{agent: a, text: steer})
	var reads int
	a.SetBoardDelta(func(context.Context) (string, error) {
		reads++
		if reads == 2 {
			return delta, nil
		}
		return "", nil
	}, func() {})

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := mp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the probe should sample twice, got %d requests", len(reqs))
	}
	tail := reqs[1].Messages
	if n := len(tail); n < 2 {
		t.Fatalf("second request is too short to order: %+v", tail)
	}
	last, previous := tail[len(tail)-1], tail[len(tail)-2]
	if last.Role != provider.RoleUser || last.Content != delta {
		t.Fatalf("the delta must be the last message, got %+v", last)
	}
	if previous.Role != provider.RoleUser || !strings.Contains(previous.Content, steer) {
		t.Fatalf("the steer batch must precede the delta, got %+v", previous)
	}
}
