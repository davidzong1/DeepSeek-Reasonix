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

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
	"reasonix/internal/sessioncontext"
	"reasonix/internal/tool"
)

// prefixTrace is one conversation request's cache shape at the provider
// boundary.
type prefixTrace struct {
	// stream is which request stream this request belongs to. Two members share
	// one mock, so a request is only ever compared with its OWN stream's
	// previous request: comparing across members would fabricate a divergence.
	stream      string
	promptChars int // whole request: messages + tool block
	toolsChars  int // the tool block, which sits after the messages
	// toolsHash fingerprints the tool block as it was serialized. Only this one
	// proves the provider's own bytes stayed put while the registry was being
	// re-registered underneath the run.
	toolsHash string
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
	// rebuilds counts member backends torn down and rebuilt mid-run, and
	// reregistrations counts dynamic tool re-registrations. Both must be
	// non-zero in the arms that claim the provider's bytes survived them.
	rebuilds        int
	reregistrations int
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
// one before it in the same stream, and grows the session by answering with one
// tool call per non-tool turn.
type prefixBenchMock struct {
	t *testing.T
	// prev is the previous request of each stream, keyed by the stream's own
	// identity. A single baseline would compare a member's request against
	// whichever member happened to speak last.
	prev   map[string][]json.RawMessage
	traces []prefixTrace
	// lastHitChars is the reuse computed for the request being answered, so the
	// usage chunk reports the same split the trace recorded.
	lastHitChars map[string]int
	folds        int
	pendingFold  map[string]bool
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
	stream := streamKey(msgs)
	promptTok := (charsOf(msgs) + toolsBlockChars(body)) / 4
	hitTok := m.lastHitChars[stream] / 4
	if lastRole(msgs) == "tool" {
		m.finalAnswers++
		writeSSE(w, m.t, streamChunk(deltaText("Done.")), finishChunk("stop"),
			usageChunk(promptTok, 20, hitTok, promptTok-hitTok))
		return
	}
	m.toolCalls++
	writeSSE(w, m.t, streamChunk(deltaToolCall(m.toolCalls, "fat_read", `{}`)), finishChunk("tool_calls"),
		usageChunk(promptTok, 20, hitTok, promptTok-hitTok))
}

// streamKey names the request stream a request belongs to. The member's own
// identity lives in its system prompt, so hashing that message is what tells two
// members sharing one route apart without the mock needing to know anything
// about teams.
func streamKey(msgs []json.RawMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	return shortHash(string(msgs[0]))
}

// record appends one request's cache shape and advances its stream's
// previous-request baseline.
//
// A fold request does NOT move the baseline. It sends a fold REGION plus one
// instruction, not the live view, so comparing it to the previous request — or
// comparing the next request to it — would be apples-to-oranges and would
// manufacture a rewrite that never happened. It is counted as a fold, and the
// next real request of that stream is marked as the one that follows it.
func (m *prefixBenchMock) record(body []byte, summarize bool) {
	msgs := decodeMessages(body)
	stream := streamKey(msgs)
	if m.pendingFold == nil {
		m.pendingFold = map[string]bool{}
	}
	if summarize {
		m.pendingFold[stream] = true
		return
	}
	prev := m.prev[stream]
	common := commonPrefixMsgs(prev, msgs)
	messageHit := charsOf(msgs[:common])
	prevMsgChars := charsOf(prev)
	if m.lastHitChars == nil {
		m.lastHitChars = map[string]int{}
	}
	m.lastHitChars[stream] = messageHit
	if len(prev) > 0 {
		m.traces = append(m.traces, prefixTrace{
			stream:       stream,
			promptChars:  charsOf(msgs) + toolsBlockChars(body),
			toolsChars:   toolsBlockChars(body),
			toolsHash:    shortHash(string(rawToolsBlock(body))),
			messageHit:   messageHit,
			prevMsgChars: prevMsgChars,
			appended:     messageHit - prevMsgChars,
			stableHash:   memberHash(body),
			afterFold:    m.pendingFold[stream],
		})
	}
	if m.prev == nil {
		m.prev = map[string][]json.RawMessage{}
	}
	m.prev[stream] = msgs
	m.pendingFold[stream] = false
}

// toolsBlockChars is the serialized size of the request's tool array, which the
// wire format places after the messages.
func toolsBlockChars(body []byte) int {
	block := rawToolsBlock(body)
	if len(block) == 0 {
		return 0
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, block); err != nil {
		return len(block)
	}
	return buf.Len()
}

// rawToolsBlock is the request's tool array exactly as it was serialized.
func rawToolsBlock(body []byte) []byte {
	var req struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	return req.Tools
}

// memberBenchRegistry stands in for a member's provider-visible surface: the
// resident tools a member keeps, plus the tool the loop calls so each turn has
// something to append.
//
// mcpServers registers server blocks under real MCP names. They are ordinary
// visible tools here (no allowlist, no native tool search), which is what makes
// the re-registration arm a byte-level test: RemovePrefix + Add moves one
// server's whole block to the end of the registry's insertion order, the way an
// MCP tools/list_changed or a lazy spawn does in production.
//
// reverse flips every registration order. A rebuilt member backend registers the
// same tools in the other order, so an arm that rebuilds can only keep its
// provider bytes if the surface is canonicalized rather than insertion-ordered.
func memberBenchRegistry(blob string, wider, reverse bool, mcpServers int) *tool.Registry {
	names := []string{"bash", "use_capability", "atomic_read", "atomic_write"}
	if wider {
		names = append(names, "view_image", "web_search", "todo_write", "compress")
	}
	if reverse {
		slices.Reverse(names)
	}
	reg := tool.NewRegistry()
	// The loop's own tool is registered first either way: it is not part of the
	// question this arm asks, and its arrival order must not be the variable.
	reg.Add(fatTool{blob: blob})
	for _, name := range names {
		if tl, ok := tool.LookupBuiltin(name); ok {
			reg.Add(tl)
		}
	}
	for server := range mcpServers {
		for _, name := range mcpServerToolNames(server) {
			reg.Add(fakeTool{name: name, readOnly: true})
		}
	}
	return reg
}

// mcpServerToolNames is one MCP server's tool block, as the agent sees it after
// an mcp__<server>__<tool> registration.
func mcpServerToolNames(server int) []string {
	return []string{
		fmt.Sprintf("mcp__srv%d__alpha", server),
		fmt.Sprintf("mcp__srv%d__beta", server),
	}
}

// reregisterMCPServer tears one server's block out of the registry and adds it
// back, which is exactly what an MCP list_changed or a lazy reconnect does. The
// server's whole block moves to the end of the registry's insertion order while
// its contents stay identical.
func reregisterMCPServer(t *testing.T, reg *tool.Registry, server int) {
	t.Helper()
	prefix := fmt.Sprintf("mcp__srv%d__", server)
	if removed := reg.RemovePrefix(prefix); removed != len(mcpServerToolNames(server)) {
		t.Fatalf("RemovePrefix(%q) removed %d tools, want %d", prefix, removed, len(mcpServerToolNames(server)))
	}
	for _, name := range mcpServerToolNames(server) {
		reg.Add(fakeTool{name: name, readOnly: true})
	}
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
	// members is how many member backends share the one route and mock. Each
	// carries its own identity in its system prompt and takes turns round-robin,
	// which is what interleaving two members on one provider looks like.
	members int
	// rebuildEvery tears the member's backend down and rebuilds it every N turns,
	// keeping the transcript and re-registering the same tools in the opposite
	// order: an eviction/rebind that must not move its provider-visible bytes.
	rebuildEvery int
	// reregisterEvery re-registers one MCP server block every N turns, which is
	// an mcp tools/list_changed or a lazy reconnect.
	reregisterEvery int
	// mcpServers is how many MCP server blocks are registered.
	mcpServers int
}

// benchMember is one member under test: its identity, its transcript, its
// registry and its agent. A rebuild replaces the agent and the registry while
// keeping the transcript and the identity, which is what a member backend
// eviction, model rebind or quota failover does.
type benchMember struct {
	system     string
	session    *Session
	reg        *tool.Registry
	agent      *Agent
	sinks      []*collectSink
	blob       string
	wider      bool
	window     int
	recentKeep int
	mcpServers int
	// reverse flips on every rebuild, so a rebuilt backend registers the same
	// tools in the other order.
	reverse bool
}

// newMemberAgent wires a real openai provider at url into an Agent that uses the
// member's own system prompt and, when given, an existing transcript.
func newMemberAgent(t *testing.T, url string, reg *tool.Registry, sess *Session, window, recentKeep int) (*Agent, *collectSink) {
	t.Helper()
	prov, err := openai.New(provider.Config{
		Name:    "deepseek",
		BaseURL: url,
		Model:   "deepseek-reasoner",
		APIKey:  "test",
		Extra:   map[string]any{"api_key_env": "DEEPSEEK_API_KEY"},
	})
	if err != nil {
		t.Fatalf("provider New: %v", err)
	}
	sink := &collectSink{}
	if sess == nil {
		sess = NewSession("")
	}
	return New(prov, reg, sess, Options{
		Temperature:   0,
		ContextWindow: window,
		RecentKeep:    recentKeep,
	}, sink), sink
}

// build (re)creates the member's registry and agent, keeping its transcript.
func (m *benchMember) build(t *testing.T, url string) {
	t.Helper()
	m.reg = memberBenchRegistry(m.blob, m.wider, m.reverse, m.mcpServers)
	agent, sink := newMemberAgent(t, url, m.reg, m.session, m.window, m.recentKeep)
	m.agent, m.sinks = agent, append(m.sinks, sink)
}

// runPrefixBench drives one or more member-shaped sessions against one mock and
// returns the traces plus the companion metrics. seedChars sizes the opening
// context, which is the ladder's variable; wider adds the deferred tools back,
// which is the surface variable; hostContext supplies a fresh, CHANGING
// turn-context bundle per turn, which is what a member backend actually does and
// is the remaining candidate for a per-turn prefix change.
//
// members > 1 interleaves the members round-robin on one route, rebuildEvery
// tears a member's backend down and back up, and reregisterEvery re-registers an
// MCP server block. All three are events a member must survive without moving
// the bytes its provider already cached.
func runPrefixBench(t *testing.T, cfg benchConfig) ([]prefixTrace, benchOutcome) {
	t.Helper()
	mock := &prefixBenchMock{t: t}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()

	members := make([]*benchMember, max(cfg.members, 1))
	for i := range members {
		system := systemPrompt
		if len(members) > 1 {
			// Each member's own identity lives in its system prompt, which is the
			// member's cache scope as far as the provider is concerned.
			system += fmt.Sprintf("\nYou are member %d of the team.", i)
		}
		member := &benchMember{
			system:     system,
			session:    NewSession(system),
			blob:       strings.Repeat("tool output line\n", max(cfg.blobChars/18, 1)),
			wider:      cfg.wider,
			window:     cfg.window,
			recentKeep: cfg.recentKeep,
			mcpServers: cfg.mcpServers,
		}
		member.build(t, srv.URL)
		member.session.Add(provider.Message{
			Role:    provider.RoleUser,
			Content: strings.Repeat("context line for the ladder. ", max(cfg.seedChars/31, 1)),
		})
		members[i] = member
	}

	var out benchOutcome
	for i := range cfg.turns {
		member := members[i%len(members)]
		if cfg.rebuildEvery > 0 && i > 0 && i%cfg.rebuildEvery == 0 {
			member.reverse = !member.reverse
			member.build(t, srv.URL)
			out.rebuilds++
		}
		if cfg.reregisterEvery > 0 && cfg.mcpServers > 0 && i > 0 && i%cfg.reregisterEvery == 0 {
			reregisterMCPServer(t, member.reg, i%cfg.mcpServers)
			out.reregistrations++
		}
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
		err := member.agent.Run(ctx, fmt.Sprintf("turn %d: continue the work", i))
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
	for _, member := range members {
		for _, sink := range member.sinks {
			for _, m := range sink.maintenance {
				if m.Status == "applied" && m.Action == maintenanceActionSummary {
					out.foldTriggers = append(out.foldTriggers, m.Trigger)
				}
			}
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
	t.Logf("%s: runs=%d errors=%d finalAnswers=%d toolCalls=%d toolOutputChars=%d folds=%d rebuilds=%d reregistrations=%d triggers=%v clientLatencyMs(p50/p90)=%d/%d",
		name, out.runs, out.errors, out.finalAnswers, out.toolCalls, out.toolOutputChars, out.folds,
		out.rebuilds, out.reregistrations, out.foldTriggers,
		sorted[len(sorted)/2], sorted[min(len(sorted)-1, 9*len(sorted)/10)])
}

// TestDeferredMCPTailIsTheHashedSurface closes the shape the HTTP arms cannot
// reach: the defered MCP tail only exists when a provider advertises native tool
// search, and it is appended to the wire array by providerToolSchemas rather
// than coming out of the registry.
//
// Two things must hold, and both are what a re-registration can break. The tail
// must be part of the array CaptureShape hashes — a tail outside it means a
// changed provider surface with no local diagnosis — and re-registering a server
// must not move the array, because an MCP tools/list_changed or a lazy reconnect
// does exactly that at runtime and the provider caches on the bytes.
func TestDeferredMCPTailIsTheHashedSurface(t *testing.T) {
	restore := provider.SetNativeToolSearchPreviewForTest(true)
	defer restore()

	build := func(servers ...int) (*Agent, []provider.ToolSchema) {
		reg := tool.NewRegistry()
		reg.Add(fakeTool{name: "read_file", readOnly: true})
		for _, server := range servers {
			for _, name := range mcpServerToolNames(server) {
				reg.Add(fakeTool{name: name, readOnly: true})
			}
		}
		// The boot allowlist never names an MCP tool, so all of them stay
		// deferred: registered, reachable through use_capability, absent from the
		// visible surface — and re-appended as the tail.
		reg.SetProviderVisibleTools([]string{"read_file"})
		a := New(&nativeToolSearchFake{}, reg, NewSession("sys"), Options{}, event.Discard)
		return a, a.providerToolSchemas()
	}

	_, forward := build(0, 1)
	_, reverse := build(1, 0)
	forwardJSON, _ := json.Marshal(forward)
	reverseJSON, _ := json.Marshal(reverse)
	if !bytes.Equal(forwardJSON, reverseJSON) {
		t.Fatalf("the tail follows registration order\ngot  %s\nwant %s", forwardJSON, reverseJSON)
	}
	wantTail := len(mcpServerToolNames(0)) + len(mcpServerToolNames(1))
	if len(forward) != 1+wantTail {
		t.Fatalf("expected the visible tool plus %d deferred MCP tools, got %v", wantTail, wireSchemaNames(forward))
	}
	for _, schema := range forward[1:] {
		if !schema.Deferred {
			t.Errorf("the tail must be marked deferred: %v", wireSchemaNames(forward))
		}
	}

	a, _ := build(0, 1)
	withTail := a.capturePrefixShape(forward)
	if withTail.ToolSchemaTokens == 0 {
		t.Fatal("the shape must carry a tool-schema footprint")
	}

	// Re-registering one server must not move the array or its hash.
	a, _ = build(0, 1)
	reg := a.svc.tools
	reregisterMCPServer(t, reg, 0)
	afterReregister := a.providerToolSchemas()
	afterJSON, _ := json.Marshal(afterReregister)
	if !bytes.Equal(forwardJSON, afterJSON) {
		t.Fatalf("re-registering a server moved the wire tool array\ngot  %s\nwant %s", afterJSON, forwardJSON)
	}
	if got := a.capturePrefixShape(afterReregister).ToolsHash; got != withTail.ToolsHash {
		t.Errorf("re-registering a server moved ToolsHash: %s -> %s", withTail.ToolsHash, got)
	}

	// The tail is inside the hashed surface: dropping one deferred tool must
	// move ToolsHash, or a changed provider surface would go undiagnosed.
	a, _ = build(0, 1)
	reg = a.svc.tools
	reg.SetProviderVisibleTools([]string{"read_file", mcpServerToolNames(0)[0]})
	// Promoting a tail tool to the visible surface changes both arrays; what
	// matters here is that the shape follows the bytes.
	promoted := a.providerToolSchemas()
	if got := a.capturePrefixShape(promoted).ToolsHash; got == withTail.ToolsHash {
		t.Errorf("ToolsHash did not follow a changed tool array: %s", got)
	}
}

func wireSchemaNames(schemas []provider.ToolSchema) []string {
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	return names
}

// TestMemberCachePrefixBenchmark is §5.1's offline arm. It reports, per request,
// how much of the previous request the provider could reuse and whether the
// divergence landed at the end of it (new content) or inside it (a rewritten
// prefix), across a context-size ladder, a tool-surface control, a host-context
// control, a member-interleaving arm, a member-rebuild arm, a dynamic-tool
// re-registration arm, and a fold arm.
//
// The assertions are the invariants the production ledger cannot check: an
// append-only session must let the provider reuse the ENTIRE previous message
// array (so `appended` is never negative, except on the request after a fold,
// at most once per fold); the stable prefix and the serialized tool block must
// not move within a stream — not across a fold, an interleaved member, a rebuilt
// backend, or an MCP re-registration; and each member is its own stream, so a
// comparison only ever crosses one member's own previous request.
func TestMemberCachePrefixBenchmark(t *testing.T) {
	for _, tc := range []benchConfig{
		{name: "short/member-surface", seedChars: 8_000, blobChars: 2_000, turns: 6},
		{name: "long/member-surface", seedChars: 120_000, blobChars: 2_000, turns: 6},
		{name: "short/wider-surface", seedChars: 8_000, blobChars: 2_000, turns: 6, wider: true},
		{name: "short/changing-host-context", seedChars: 8_000, blobChars: 2_000, turns: 6, hostContext: true},
		{name: "short/large-tool-output", seedChars: 8_000, blobChars: 40_000, turns: 6},
		{name: "switch/two-members", seedChars: 8_000, blobChars: 2_000, turns: 12, members: 2},
		{name: "switch/two-members-host-context", seedChars: 8_000, blobChars: 2_000, turns: 12, members: 2, hostContext: true},
		{name: "rebuild/member-surface", seedChars: 8_000, blobChars: 2_000, turns: 8, rebuildEvery: 2},
		{name: "reregister/mcp-servers", seedChars: 8_000, blobChars: 2_000, turns: 8, mcpServers: 2, reregisterEvery: 1},
		{name: "fold/auto", seedChars: 60_000, blobChars: 2_000, turns: 20, window: 6_000, recentKeep: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			traces, out := runPrefixBench(t, tc)
			logOutcome(t, tc.name, out)
			if len(traces) < 4 {
				t.Fatalf("expected several conversation requests, got %d", len(traces))
			}

			t.Logf("%7s %8s %10s %9s %10s %9s %9s %10s %10s", "request", "stream", "prompt", "reuse", "prevMsgs", "appended", "tools", "msgReuse", "stable")
			stableByStream := map[string]string{}
			toolsByStream := map[string]string{}
			rewritten, foldRequests := 0, 0
			for i, tr := range traces {
				if tr.stableHash == "" {
					t.Fatalf("the fixture must carry a stable prefix (system + tools)")
				}
				msgReuse := 0.0
				if tr.prevMsgChars > 0 {
					msgReuse = 100 * float64(tr.messageHit) / float64(tr.prevMsgChars)
				}
				marker := ""
				if tr.afterFold {
					marker = "post-fold"
					foldRequests++
				}
				t.Logf("%7d %8s %10d %8.1f%% %10d %9d %9d %9.1f%% %10s %s",
					i, tr.stream, tr.promptChars, 100*float64(tr.messageHit)/float64(max(tr.promptChars, 1)),
					tr.prevMsgChars, tr.appended, tr.toolsChars, msgReuse, tr.stableHash, marker)
				// The stable prefix must never move inside a stream — not across a
				// fold, a member switch, a rebuild or a tool re-registration.
				if want, ok := stableByStream[tr.stream]; ok && tr.stableHash != want {
					t.Errorf("request %d moved stream %s's stable prefix: %s -> %s", i, tr.stream, want, tr.stableHash)
				}
				stableByStream[tr.stream] = tr.stableHash
				// The tool block the provider actually received must not move
				// either: ToolsHash is computed over the same shapes, and this is
				// the byte-level check that they agree.
				if want, ok := toolsByStream[tr.stream]; ok && tr.toolsHash != want {
					t.Errorf("request %d moved stream %s's tool block: %s -> %s", i, tr.stream, want, tr.toolsHash)
				}
				toolsByStream[tr.stream] = tr.toolsHash
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
			t.Logf("%s: %d/%d requests diverged inside the previous prefix (%d followed a fold, %d streams, triggers=%v)",
				tc.name, rewritten, len(traces), foldRequests, len(stableByStream), out.foldTriggers)
			// Each member must be accounted for as its own stream, so the
			// interleaving arm cannot pass by accident on one shared baseline.
			if tc.members > 1 && len(stableByStream) != tc.members {
				t.Errorf("%d streams observed, want %d distinct members", len(stableByStream), tc.members)
			}
			// One rewrite per fold at most: more would mean the fold re-paid the
			// prefix repeatedly.
			if out.folds > 0 && rewritten > out.folds {
				t.Errorf("%d rewrites for %d folds: a fold must cost at most one", rewritten, out.folds)
			}
			// An arm that asserts it survives an event must actually have seen it.
			if tc.rebuildEvery > 0 && out.rebuilds == 0 {
				t.Errorf("the rebuild arm never rebuilt, so it asserts nothing about surviving one")
			}
			if tc.reregisterEvery > 0 && out.reregistrations == 0 {
				t.Errorf("the re-registration arm never re-registered, so it asserts nothing")
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
