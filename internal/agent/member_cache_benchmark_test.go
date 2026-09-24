// Part B (TEAM_MEMBER_CACHE_ROOTCAUSE_EXPERIMENTS.md) §5.1 / §9: the offline,
// reproducible arm of the cache experiment.
//
// The mock derives the cache split from the byte-identical prefix shared with
// the previous conversation request — how a prefix-caching provider behaves —
// and records where the two first diverge: past the end of the previous
// request means the miss is new content (P2), inside it means bytes already
// sent were rewritten (P1), and the offset says how much was re-paid.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/sessioncontext"
	"reasonix/internal/tool"
)

// prefixTrace is one conversation request's cache shape at the provider
// boundary.
type prefixTrace struct {
	promptChars int // whole request: messages + tool block
	toolsChars  int // the tool block, which sits after the messages
	// messageHit is how many bytes of the previous request's MESSAGE array this
	// request reused. It equals prevMsgChars exactly when the session is
	// append-only.
	messageHit   int
	prevMsgChars int
	// appended is messageHit - prevMsgChars. Negative means bytes already sent
	// were rewritten: the P1 signature.
	appended int
	// stableHash fingerprints the cache-stable prefix (system message + tool
	// block) the way the production diagnostics do, so a fold arm can assert the
	// fold did not move it.
	stableHash string
	// afterFold marks the first real conversation request following a fold. It is
	// the one request allowed to diverge inside the previous prefix: the
	// projection replaced the visible region.
	afterFold bool
}

// benchOutcome is the companion-metric half of the benchmark: what the run cost
// and whether it still did the work, not just what the cache did.
type benchOutcome struct {
	runs            int
	errors          int
	finalAnswers    int // turns that ended with assistant text instead of a tool call
	toolCalls       int
	toolOutputChars int
	folds           int
	foldTriggers    []string // the maintenance trigger behind each fold
	clientLatencyMs []int64  // wall clock around Run: assembly + tools + stream handling
}

// memberHash fingerprints the cache-stable prefix of one request: the system
// message plus the tool block, in wire order.
func memberHash(body []byte) string {
	msgs := decodeMessages(body)
	var system string
	if len(msgs) > 0 {
		var m struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(msgs[0], &m); err == nil && m.Role == "system" {
			system = m.Content
		}
	}
	var req struct {
		Tools json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(body, &req)
	return shortHash(map[string]string{"system": system, "tools": string(req.Tools)})
}

// prefixBenchMock records the prefix each conversation request shares with the
// one before it, and grows the session by answering with one tool call per
// non-tool turn.
type prefixBenchMock struct {
	t      *testing.T
	prev   []json.RawMessage
	traces []prefixTrace
	// lastHitChars is the reuse computed for the request being answered, so the
	// usage chunk reports the same split the trace recorded.
	lastHitChars int
	folds        int
	pendingFold  bool
	toolCalls    int
	finalAnswers int
}

func (m *prefixBenchMock) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if isSummarizeRequest(body) {
		// The fold request re-sends the live prefix plus one instruction, so it
		// counts as a conversation request for prefix accounting: a fold that
		// re-derived its own surface would show up here as a rewrite.
		m.record(body, true)
		m.folds++
		writeSSE(w, m.t, streamChunk(deltaText("- summary")), finishChunk("stop"), usageChunk(100, 20, 0, 100))
		return
	}

	m.record(body, false)
	msgs := decodeMessages(body)
	promptTok := (charsOf(msgs) + toolsBlockChars(body)) / 4
	hitTok := m.lastHitChars / 4
	if lastRole(msgs) == "tool" {
		m.finalAnswers++
		writeSSE(w, m.t, streamChunk(deltaText("Done.")), finishChunk("stop"),
			usageChunk(promptTok, 20, hitTok, promptTok-hitTok))
		return
	}
	m.toolCalls++
	writeSSE(w, m.t, streamChunk(deltaToolCall(len(m.traces), "fat_read", `{}`)), finishChunk("tool_calls"),
		usageChunk(promptTok, 20, hitTok, promptTok-hitTok))
}

// record appends one request's cache shape and advances the previous-request
// baseline.
//
// A fold request does NOT move the baseline. It sends a fold REGION plus one
// instruction, not the live view, so comparing it to the previous request — or
// comparing the next request to it — would be apples-to-oranges and would
// manufacture a rewrite that never happened. It is counted as a fold, and the
// next real request is marked as the one that follows it.
func (m *prefixBenchMock) record(body []byte, summarize bool) {
	if summarize {
		m.pendingFold = true
		return
	}
	msgs := decodeMessages(body)
	common := commonPrefixMsgs(m.prev, msgs)
	messageHit := charsOf(msgs[:common])
	prevMsgChars := charsOf(m.prev)
	m.lastHitChars = messageHit
	if len(m.prev) > 0 {
		m.traces = append(m.traces, prefixTrace{
			promptChars:  charsOf(msgs) + toolsBlockChars(body),
			toolsChars:   toolsBlockChars(body),
			messageHit:   messageHit,
			prevMsgChars: prevMsgChars,
			appended:     messageHit - prevMsgChars,
			stableHash:   memberHash(body),
			afterFold:    m.pendingFold,
		})
	}
	m.prev = msgs
	m.pendingFold = false
}

// toolsBlockChars is the serialized size of the request's tool array, which the
// wire format places after the messages.
func toolsBlockChars(body []byte) int {
	var req struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Tools) == 0 {
		return 0
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, req.Tools); err != nil {
		return len(req.Tools)
	}
	return buf.Len()
}

// memberBenchRegistry stands in for a member's provider-visible surface: the
// resident tools a member keeps, plus the tool the loop calls so each turn has
// something to append.
func memberBenchRegistry(blob string) *tool.Registry {
	reg := tool.NewRegistry()
	reg.Add(fatTool{blob: blob})
	for _, name := range []string{"bash", "use_capability", "atomic_read", "atomic_write"} {
		if tl, ok := tool.LookupBuiltin(name); ok {
			reg.Add(tl)
		}
	}
	return reg
}

// benchConfig is one arm of the benchmark. Every field is a single variable so
// an arm can change exactly one of them.
type benchConfig struct {
	name        string
	seedChars   int // opening context size: the context-size ladder
	blobChars   int // tool output size: the tool-load variable
	turns       int
	wider       bool // add the deferred tools back: the surface variable
	hostContext bool // a fresh turn-context snapshot per turn
	window      int  // 0 disables compaction; small values force folds
	recentKeep  int
}

// runPrefixBench drives one member-shaped session and returns its traces plus
// the companion metrics. seedChars sizes the opening context, which is the
// ladder's variable; wider adds the deferred tools back, which is the surface
// variable; hostContext supplies a fresh, CHANGING turn-context bundle per
// turn, which is what a member backend actually does and is the remaining
// candidate for a per-turn prefix change.
func runPrefixBench(t *testing.T, cfg benchConfig) ([]prefixTrace, benchOutcome) {
	t.Helper()
	mock := &prefixBenchMock{t: t}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()

	reg := memberBenchRegistry(strings.Repeat("tool output line\n", max(cfg.blobChars/18, 1)))
	if cfg.wider {
		for _, name := range []string{"view_image", "web_search", "todo_write", "compress"} {
			if tl, ok := tool.LookupBuiltin(name); ok {
				reg.Add(tl)
			}
		}
	}
	a, sink := newAgent(t, srv.URL, reg, cfg.window, cfg.recentKeep)
	a.Session().Add(provider.Message{
		Role:    provider.RoleUser,
		Content: strings.Repeat("context line for the ladder. ", max(cfg.seedChars/31, 1)),
	})

	var out benchOutcome
	for i := range cfg.turns {
		ctx := context.Background()
		if cfg.hostContext {
			// A different snapshot every turn: workspace and memory both move, so
			// a replaced-in-place context message would rewrite the prefix while
			// an appended revision would not.
			ctx = WithTurnContextBundle(ctx, TurnContextBundle{Executor: sessioncontext.Build(sessioncontext.Sections{
				Workspace:        fmt.Sprintf("/ws/turn-%d", i),
				BackgroundMemory: fmt.Sprintf("fact learned on turn %d", i),
			})})
		}
		start := time.Now()
		err := a.Run(ctx, fmt.Sprintf("turn %d: continue the work", i))
		out.clientLatencyMs = append(out.clientLatencyMs, time.Since(start).Milliseconds())
		out.runs++
		if err != nil {
			// A failed turn is reported, not hidden: the plan requires the
			// failure cases to travel with the numbers.
			out.errors++
			t.Logf("turn %d failed: %v", i, err)
			break
		}
	}
	for _, m := range sink.maintenance {
		if m.Status == "applied" && m.Action == maintenanceActionSummary {
			out.foldTriggers = append(out.foldTriggers, m.Trigger)
		}
	}
	out.toolCalls = mock.toolCalls
	out.finalAnswers = mock.finalAnswers
	out.folds = mock.folds
	out.toolOutputChars = mock.toolCalls * len(strings.Repeat("tool output line\n", max(cfg.blobChars/18, 1)))
	return mock.traces, out
}

// logOutcome prints the companion metrics the plan requires beside the cache
// split: task success, latency, and tool-output scale.
func logOutcome(t *testing.T, name string, out benchOutcome) {
	t.Helper()
	if len(out.clientLatencyMs) == 0 {
		t.Logf("%s: no completed turn", name)
		return
	}
	sorted := append([]int64(nil), out.clientLatencyMs...)
	slices.Sort(sorted)
	t.Logf("%s: runs=%d errors=%d finalAnswers=%d toolCalls=%d toolOutputChars=%d folds=%d triggers=%v clientLatencyMs(p50/p90)=%d/%d",
		name, out.runs, out.errors, out.finalAnswers, out.toolCalls, out.toolOutputChars, out.folds, out.foldTriggers,
		sorted[len(sorted)/2], sorted[min(len(sorted)-1, 9*len(sorted)/10)])
}

// TestMemberCachePrefixBenchmark is §5.1's offline arm. It reports, per request,
// how much of the previous request the provider could reuse and whether the
// divergence landed at the end of it (new content) or inside it (a rewritten
// prefix), across a context-size ladder, a tool-surface control, a host-context
// control, and a fold arm.
//
// The assertion is the invariant the production ledger cannot check: an
// append-only session must let the provider reuse the ENTIRE previous message
// array, so `appended` must never be negative — except on the request that
// follows a fold, where the projection legitimately rewrites the visible region.
// The stable prefix (system + tools) must survive a fold untouched, and each
// fold may cost at most one rewrite.
func TestMemberCachePrefixBenchmark(t *testing.T) {
	for _, tc := range []benchConfig{
		{name: "short/member-surface", seedChars: 8_000, blobChars: 2_000, turns: 6},
		{name: "long/member-surface", seedChars: 120_000, blobChars: 2_000, turns: 6},
		{name: "short/wider-surface", seedChars: 8_000, blobChars: 2_000, turns: 6, wider: true},
		{name: "short/changing-host-context", seedChars: 8_000, blobChars: 2_000, turns: 6, hostContext: true},
		{name: "short/large-tool-output", seedChars: 8_000, blobChars: 40_000, turns: 6},
		{name: "fold/auto", seedChars: 60_000, blobChars: 2_000, turns: 20, window: 6_000, recentKeep: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			traces, out := runPrefixBench(t, tc)
			logOutcome(t, tc.name, out)
			if len(traces) < 4 {
				t.Fatalf("expected several conversation requests, got %d", len(traces))
			}

			t.Logf("%7s %10s %9s %10s %9s %9s %10s %10s", "request", "prompt", "reuse", "prevMsgs", "appended", "tools", "msgReuse", "stable")
			stable := traces[0].stableHash
			if stable == "" {
				t.Fatal("the fixture must carry a stable prefix (system + tools)")
			}
			rewritten, foldRequests := 0, 0
			for i, tr := range traces {
				msgReuse := 0.0
				if tr.prevMsgChars > 0 {
					msgReuse = 100 * float64(tr.messageHit) / float64(tr.prevMsgChars)
				}
				marker := ""
				if tr.afterFold {
					marker = "post-fold"
					foldRequests++
				}
				t.Logf("%7d %10d %8.1f%% %10d %9d %9d %9.1f%% %10s %s",
					i, tr.promptChars, 100*float64(tr.messageHit)/float64(max(tr.promptChars, 1)),
					tr.prevMsgChars, tr.appended, tr.toolsChars, msgReuse, tr.stableHash, marker)
				// The stable prefix must never move — not across a fold, not ever.
				if tr.stableHash != stable {
					t.Errorf("request %d moved the stable prefix: %s -> %s", i, stable, tr.stableHash)
				}
				// Only the request following a fold may rewrite the visible
				// region; any other negative is an unexplained rewrite.
				if tr.appended < 0 {
					rewritten++
					if !tr.afterFold {
						t.Errorf("request %d rewrote bytes it had already sent with no fold to explain it: reused %d of the previous %d message chars",
							i, tr.messageHit, tr.prevMsgChars)
					}
				}
			}
			t.Logf("%s: %d/%d requests diverged inside the previous prefix (%d followed a fold, triggers=%v)",
				tc.name, rewritten, len(traces), foldRequests, out.foldTriggers)
			// One rewrite per fold at most: more would mean the fold re-paid the
			// prefix repeatedly.
			if out.folds > 0 && rewritten > out.folds {
				t.Errorf("%d rewrites for %d folds: a fold must cost at most one", rewritten, out.folds)
			}
			if tc.window > 0 && out.folds == 0 {
				t.Errorf("the fold arm never folded, so it asserts nothing about surviving one")
			}
			// A fold is expected to be auto-maintenance, not a manual/overflow
			// rescue: the arm must exercise the path the plan is about.
			for _, trigger := range out.foldTriggers {
				if trigger != CompactionTriggerPressure {
					t.Errorf("fold trigger = %q, want %q", trigger, CompactionTriggerPressure)
				}
			}
		})
	}
}
