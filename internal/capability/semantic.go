package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

const (
	semanticMaxCandidates = 12
	semanticMaxResults    = 3
	semanticTimeout       = 3 * time.Second
	semanticCacheTTL      = 5 * time.Minute
	// semanticMaxTokens bounds the router's answer, and it has to clear the
	// answer plus whatever the endpoint spends thinking first: a measured
	// reasoning model spent all 256 on chain-of-thought and returned an empty
	// body, which read as "no relevant capability" rather than as a cut-off.
	// The observed answer is 17 tokens, so the ceiling costs nothing in the
	// ordinary case and only matters when a judgement is hard.
	semanticMaxTokens = 1024
)

// SemanticRouter calls a lightweight model when deterministic routing has no
// require/prefer hits. Failures fall back immediately to deterministic results.
type SemanticRouter struct {
	Provider provider.Provider
	Sink     event.Sink
	Model    string
	Effort   string
	// Pricing prices the router's own usage events; without it the routing
	// cost always displays as zero.
	Pricing      *provider.Pricing
	QuoteContext *event.QuoteContext
	// Audit receives router token/cost/latency counters (RecordRouterUsage).
	Audit *Audit

	mu    sync.Mutex
	cache map[string]semanticCacheEntry
}

type semanticCacheEntry struct {
	ids       []string
	expiresAt time.Time
}

// RouteSemantic may append up to 3 suggest candidates. It never overrides an
// existing require/prefer decision. On any failure it returns decision unchanged.
func (r *SemanticRouter) RouteSemantic(ctx context.Context, input string, catalog Catalog, decision RouteDecision) RouteDecision {
	if r == nil || r.Provider == nil {
		return decision
	}
	if hasStrongMatch(decision) {
		return decision
	}
	input = normalize(input)
	if input == "" {
		return decision
	}
	candidates := semanticPool(input, catalog.Entries)
	reason := "lightweight semantic match"
	if len(candidates) == 0 {
		// Discovery: nothing matched, so there is no prefilter to rank and the
		// model is asked against the whole routable directory. Reaching here is
		// the caller's decision — that is what keeps it off an ordinary turn.
		candidates = DiscoveryPool(catalog.Entries)
		reason = "semantic discovery of an unrouted request"
	}
	if len(candidates) == 0 {
		return decision
	}

	cacheKey := reason + "|" + input + "|" + catalog.Fingerprint
	if ids, ok := r.cacheGet(cacheKey); ok {
		return mergeSemanticIDs(decision, catalog, ids, "semantic cache hit")
	}

	ids, err := r.callModel(ctx, input, candidates)
	if err != nil {
		// A cut-off answer and a "nothing matched" answer are both empty, but
		// only one of them means the router could not finish its job. It is
		// counted apart so the next occurrence is visible instead of silent.
		if errors.Is(err, errSemanticTruncated) && r.Audit != nil {
			r.Audit.RecordSemanticTruncated()
		}
		return decision
	}
	if len(ids) == 0 {
		return decision
	}
	r.cachePut(cacheKey, ids)
	return mergeSemanticIDs(decision, catalog, ids, reason)
}

func hasStrongMatch(d RouteDecision) bool {
	for _, c := range d.Candidates {
		if c.Policy == AutoUseRequire || c.Policy == AutoUsePrefer {
			return true
		}
	}
	return false
}

func semanticPool(text string, entries []Entry) []Entry {
	var scored []Entry
	crossLanguageFallback := containsHan(text)
	for _, e := range entries {
		if e.Status == StatusDisabled || e.Status == StatusFailed {
			continue
		}
		switch e.Kind {
		case KindSkill, KindMCPTool, KindMCPServer:
		case KindTool:
			// Only a tool that declared triggers is a candidate. An undeclared
			// tool has nothing to match against and would only add noise.
			if len(e.Triggers) == 0 {
				continue
			}
		default:
			continue
		}
		if e.AutoUse == AutoUseOff {
			continue
		}
		if negativeMatch(text, e.NegativeTriggers) {
			continue
		}
		blob := semanticPoolBlob(e)
		if blob == "" {
			continue
		}
		// Prefer a cheap lexical match. For Han-script tasks, also admit the
		// bounded built-in/high-policy Skill set so English metadata does not make
		// the semantic router blind to Chinese requests.
		matched := false
		for tok := range strings.FieldsSeq(text) {
			if len(tok) < 3 {
				continue
			}
			if strings.Contains(blob, tok) {
				matched = true
				break
			}
		}
		if !matched && !(crossLanguageFallback && e.Kind == KindSkill && (e.Source == "builtin" || e.AutoUse == AutoUsePrefer || e.AutoUse == AutoUseRequire)) {
			continue
		}
		scored = append(scored, e)
	}
	if len(scored) > semanticMaxCandidates {
		scored = scored[:semanticMaxCandidates]
	}
	return scored
}

// semanticPoolBlob is what the cheap lexical prefilter matches against. It is
// the name plus the declared triggers and nothing else: a full description makes
// almost every entry match almost every request ("parallel", "plan"), which is
// how a pool of four tools filled for a request about three files.
func semanticPoolBlob(e Entry) string {
	return normalize(e.Name + " " + strings.Join(e.Triggers, " "))
}

// DiscoveryPool is the candidate set for a discovery call, i.e. one made when
// deterministic routing matched nothing. It is deliberately the whole routable
// directory: with no trigger fired there is nothing to prefilter on, and the
// caller bounds the request instead by requiring a multi-target signal.
func DiscoveryPool(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Kind != KindTool || len(e.Triggers) == 0 {
			continue
		}
		if e.Status == StatusDisabled || e.Status == StatusFailed || e.AutoUse == AutoUseOff {
			continue
		}
		out = append(out, e)
	}
	return out
}

// LooksMultiTarget reports whether a request names more than one thing to act
// on. It is the gate on discovery: a request with a single target has nothing to
// fan out over, so spending a model call on it cannot pay.
func LooksMultiTarget(text string) bool {
	text = normalize(text)
	if text == "" {
		return false
	}
	targets := make([]string, 0, maxDiscoveryTargets)
	for tok := range strings.FieldsSeq(text) {
		if !looksLikeTarget(tok) {
			continue
		}
		targets = append(targets, tok)
		if len(targets) >= maxDiscoveryTargets {
			break
		}
	}
	return len(targets) >= 2
}

// maxDiscoveryTargets bounds the scan; two distinct targets is the whole claim.
const maxDiscoveryTargets = 2

// looksLikeTarget recognises a filename or path fragment. It is intentionally
// narrow: prose nouns would make almost any sentence look multi-target, which is
// the same false-positive that filling a pool with descriptions caused.
func looksLikeTarget(tok string) bool {
	tok = strings.Trim(tok, ".,;:!?()'`*")
	if len(tok) < 4 || !strings.Contains(tok, ".") {
		return false
	}
	ext := tok[strings.LastIndex(tok, ".")+1:]
	if len(ext) < 1 || len(ext) > 5 {
		return false
	}
	for _, r := range ext {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func containsHan(text string) bool {
	for _, r := range text {
		if r >= '\u3400' && r <= '\u9fff' {
			return true
		}
	}
	return false
}

func (r *SemanticRouter) callModel(ctx context.Context, input string, candidates []Entry) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, semanticTimeout)
	defer cancel()
	ctx = provider.WithRequestAttemptCounter(ctx)

	var b strings.Builder
	b.WriteString("Select up to 3 capability IDs relevant to the user task. ")
	b.WriteString("Reply with ONLY a JSON array of strings, e.g. [\"skill:review\"]. ")
	b.WriteString("If none fit, reply [].\n\nTask:\n")
	b.WriteString(input)
	b.WriteString("\n\nCandidates:\n")
	for _, e := range candidates {
		fmt.Fprintf(&b, "- %s (%s): %s\n", e.ID, e.Kind, truncate(e.Description, 120))
	}

	req := provider.Request{
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: b.String(),
		}},
		Temperature: provider.TemperaturePtr(0),
		MaxTokens:   semanticMaxTokens,
	}
	if r.Model != "" {
		// Model override is provider-specific; many providers ignore Request.Model
		// and use the bound model. The Host wires a dedicated provider when configured.
		_ = r.Model
	}

	start := time.Now()
	var usage *provider.Usage
	defer func() {
		usage = provider.UsageWithRequestAttemptCount(ctx, usage)
		if usage == nil {
			return
		}
		e := event.Event{Kind: event.Usage, ModelRef: strings.TrimSpace(r.Model), Usage: usage,
			Pricing: r.Pricing, UsageSource: event.UsageSourceCapabilityRouter}
		e.CostQuote = event.EnsureCostQuote(e, r.QuoteContext)
		if (usage.PromptTokens > 0 || usage.CompletionTokens > 0) && r.Audit != nil {
			cost := 0.0
			if e.CostQuote != nil && e.CostQuote.CostComplete {
				cost = e.CostQuote.Original.Float64()
			}
			r.Audit.RecordRouterUsage(usage.PromptTokens, usage.CompletionTokens, cost, time.Since(start).Milliseconds())
		}
		if r.Sink != nil {
			r.Sink.Emit(e)
		}
	}()
	ch, err := provider.Stream(ctx, r.Provider, req)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			text.WriteString(chunk.Text)
		case provider.ChunkUsage:
			if chunk.Usage != nil {
				u := *chunk.Usage
				usage = &u
			}
		case provider.ChunkError:
			if chunk.Err != nil {
				return nil, chunk.Err
			}
		}
	}
	ids, err := parseSemanticIDs(text.String())
	if err != nil && answerHitCeiling(usage) {
		return nil, errSemanticTruncated
	}
	return ids, err
}

// errSemanticTruncated marks a router answer that stopped at the output ceiling
// rather than judging that nothing matched. The two are the same empty string,
// and treating them alike is how a too-small ceiling stays invisible.
var errSemanticTruncated = errors.New("semantic router answer was truncated")

// answerHitCeiling reports whether the call provably spent its whole output
// budget. Only usage can prove it: an empty body with no usage is still an error
// to the caller, but calling it truncated would report a cause nobody measured.
func answerHitCeiling(usage *provider.Usage) bool {
	return usage != nil && usage.CompletionTokens >= semanticMaxTokens
}

func parseSemanticIDs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty semantic response")
	}
	// Strip optional markdown fences.
	if i := strings.Index(raw, "["); i >= 0 {
		if j := strings.LastIndex(raw, "]"); j > i {
			raw = raw[i : j+1]
		}
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("invalid semantic JSON: %w", err)
	}
	out := make([]string, 0, semanticMaxResults)
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= semanticMaxResults {
			break
		}
	}
	return out, nil
}

func mergeSemanticIDs(decision RouteDecision, catalog Catalog, ids []string, reason string) RouteDecision {
	have := map[string]bool{}
	for _, c := range decision.Candidates {
		have[c.Entry.ID] = true
	}
	for _, id := range ids {
		if have[id] {
			continue
		}
		e, ok := catalog.Lookup(id)
		if !ok || e.Status == StatusFailed || e.Status == StatusDisabled {
			continue
		}
		decision.Candidates = append(decision.Candidates, RouteCandidate{
			Entry:  e,
			Policy: AutoUseSuggest,
			Reason: reason,
		})
		have[id] = true
	}
	if len(decision.Candidates) > 5 {
		decision.Candidates = decision.Candidates[:5]
	}
	return decision
}

func (r *SemanticRouter) cacheGet(key string) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		return nil, false
	}
	e, ok := r.cache[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return append([]string(nil), e.ids...), true
}

func (r *SemanticRouter) cachePut(key string, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]semanticCacheEntry{}
	}
	r.cache[key] = semanticCacheEntry{
		ids:       append([]string(nil), ids...),
		expiresAt: time.Now().Add(semanticCacheTTL),
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
