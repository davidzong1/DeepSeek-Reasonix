package cachelab

import (
	"bytes"
	"encoding/json"
	"sort"
)

// ReportedUsage is a provider's own usage numbers, read out of the raw response
// through the vocabulary that response used. Reported with Split false is a real
// answer too: it means the response carried no cache split, which is not a zero
// hit rate.
type ReportedUsage struct {
	Reported   bool
	Split      bool
	Problem    string
	Shape      string
	Keys       []string
	Prompt     int
	Hit        int
	Miss       int
	Write      int
	Completion int
	// Oracle* is the gateway's own second account of the same request, recorded
	// as a sidecar and never read to produce the fields above. A response that
	// carried none leaves OraclePresent false: undecided, not agreeing.
	OraclePresent bool
	OracleKeys    []string
	OraclePrompt  int
	OracleHit     int
	OracleMiss    int
	OracleAgrees  bool
}

// Usage-vocabulary names, one per provider family this repository talks to.
const (
	UsageShapeAnthropic = "anthropic"
	UsageShapeOpenAI    = "openai"
)

// Usage problems, recorded so an excluded sample states why it was excluded.
const (
	UsageProblemNoUsage       = "no_usage"
	UsageProblemNoCacheRead   = "no_cache_read"
	UsageProblemUnresolved    = "unresolved_vocabulary"
	UsageProblemNoPrompt      = "no_prompt"
	UsageProblemNegativeSplit = "negative_split"
	// UsageProblemBodyReadError marks a 2xx response whose body could not be read
	// to the end, so the observation is incomplete rather than empty.
	UsageProblemBodyReadError = "body_read_error"
	// UsageProblemDialectMix marks a stream whose usage events did not all speak
	// one vocabulary. Which of the two describes how the request was served is
	// not decidable from the response, so no split is reported instead of one
	// dialect being preferred silently.
	UsageProblemDialectMix = "cross_event_dialect_mix"
	// UsageProblemOracleOnly marks a response that carried only the gateway's
	// private billing block. A reading exists, but it is the gateway's own second
	// account of the request rather than the protocol's usage event, so it is
	// reported as such instead of being promoted to the primary reading.
	UsageProblemOracleOnly = "oracle_only"
)

// The usage key names this parser recognises. They are the two provider
// vocabularies this repository talks to; anything else is an unresolved
// vocabulary, reported as such instead of guessed at.
const (
	keyPromptTokens       = "prompt_tokens"
	keyCacheHitTokens     = "prompt_cache_hit_tokens"
	keyCacheMissTokens    = "prompt_cache_miss_tokens"
	keyCachedTokens       = "cached_tokens"
	keyCacheReadTokens    = "cache_read_tokens"
	keyInputTokens        = "input_tokens"
	keyCacheReadInput     = "cache_read_input_tokens"
	keyCacheCreationInput = "cache_creation_input_tokens"
	keyCompletionTokens   = "completion_tokens"
	keyOutputTokens       = "output_tokens"
)

// keyBillingUsage is the gateway-private wrapper around its own oracle block.
// Nothing outside it is treated as an oracle, and nothing inside it produces the
// primary reading.
const keyBillingUsage = "billing_usage"

// usageDetailBlocks are the nested blocks a usage object may carry its own
// counters in. OpenAI places the cache read inside prompt_tokens_details rather
// than beside prompt_tokens, so a usage object that owns such a block owns what
// is in it. The block is read only from the object that declares it, never
// harvested from elsewhere in the document.
var usageDetailBlocks = map[string]bool{"prompt_tokens_details": true}

// usageObject is one JSON object that carries usage counters, with the key names
// that object itself used.
//
// Keys are bound to the object that owns them. Harvesting every recognised name
// at any depth merged the top-level event with the gateway's nested oracle block
// and with the oracle's own nested details, so the stored value of a name that
// appeared at two depths depended on the order Go walked a map — the same bytes
// parsed to two different readings.
type usageObject struct {
	values map[string]int
	// oracle records that this object was found under billing_usage, so it is the
	// gateway's own account and not the protocol's usage event.
	oracle bool
}

// keys lists the object's own key names in a stable order.
func (o usageObject) keys() []string {
	out := make([]string, 0, len(o.values))
	for key := range o.values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// get reads one key's value.
func (o usageObject) get(key string) (int, bool) {
	n, ok := o.values[key]
	return n, ok
}

// has reports whether any of the keys was present at all, including a zero.
func (o usageObject) has(keys ...string) bool {
	for _, key := range keys {
		if _, ok := o.values[key]; ok {
			return true
		}
	}
	return false
}

// firstOf reads the first key of a family that carries a value, which is how one
// logical number appears under different provider spellings. A key present with
// an explicit zero does not shadow a sibling spelling that carries a real value:
// two spellings of one number disagreeing means the zero is the unpopulated one,
// and preferring it would read a served cache read as a zero hit. When every
// present spelling is zero the zero is the reading, which is a different fact
// from the key being absent.
func (o usageObject) firstOf(keys ...string) (int, bool) {
	zero, haveZero := 0, false
	for _, key := range keys {
		n, ok := o.values[key]
		if !ok {
			continue
		}
		if n != 0 {
			return n, true
		}
		if !haveZero {
			zero, haveZero = n, true
		}
	}
	return zero, haveZero
}

// isUsageKey reports whether a name is a usage counter this parser recognises.
func isUsageKey(key string) bool {
	switch key {
	case keyPromptTokens, keyCacheHitTokens, keyCacheMissTokens, keyCachedTokens, keyCacheReadTokens,
		keyInputTokens, keyCacheReadInput, keyCacheCreationInput, keyCompletionTokens, keyOutputTokens:
		return true
	default:
		return false
	}
}

// isAnthropicKey and isOpenAIKey partition the recognised names by dialect, so a
// folded map can be classified instead of guessed at.
func isAnthropicKey(key string) bool {
	switch key {
	case keyInputTokens, keyCacheReadInput, keyCacheCreationInput, keyOutputTokens:
		return true
	default:
		return false
	}
}

func isOpenAIKey(key string) bool {
	switch key {
	case keyPromptTokens, keyCacheHitTokens, keyCacheMissTokens, keyCachedTokens,
		keyCacheReadTokens, keyCompletionTokens:
		return true
	default:
		return false
	}
}

// parseUsage extracts the provider's own usage numbers from a raw response body
// (plain JSON or an event stream). Numbers are taken last-wins across a stream,
// because a stream reports input on its first event and output on its last.
func parseUsage(body []byte) ReportedUsage {
	var objects []usageObject
	for _, chunk := range jsonChunks(body) {
		collectUsage(chunk, false, &objects)
	}
	return resolveUsage(objects)
}

// collectUsage appends every usage object one decoded JSON value contains, in
// document order.
//
// Descent stops at a usage object: its own scalars and its declared detail
// blocks are the whole of what it says. The one exception is the billing
// wrapper, which is descended into precisely so the oracle block inside it can be
// recorded — as an oracle, never as the primary reading.
func collectUsage(value any, inBilling bool, out *[]usageObject) {
	switch node := value.(type) {
	case map[string]any:
		obj := usageObject{values: map[string]int{}, oracle: inBilling}
		for key, child := range node {
			if n, ok := asInt(child); ok && isUsageKey(key) {
				obj.values[key] = n
				continue
			}
			// A declared detail block belongs to this object, so its scalars are
			// read here rather than reached by a later descent.
			if usageDetailBlocks[key] {
				if detail, ok := child.(map[string]any); ok {
					for name, leaf := range detail {
						if n, ok := asInt(leaf); ok && isUsageKey(name) {
							obj.values[name] = n
						}
					}
				}
			}
		}
		if len(obj.values) > 0 {
			*out = append(*out, obj)
			// The gateway's own block sits inside the usage object, so it is
			// reached from here rather than by descending into the object's keys.
			if billing, ok := node[keyBillingUsage]; ok {
				collectUsage(billing, true, out)
			}
			return
		}
		for key, child := range node {
			collectUsage(child, inBilling || key == keyBillingUsage, out)
		}
	case []any:
		for _, child := range node {
			collectUsage(child, inBilling, out)
		}
	}
}

// resolveUsage folds the collected objects into one reading plus its oracle.
//
// The primary reading is folded from the objects that are not the gateway's
// billing block. Every one of them must speak the same dialect: which of two
// dialects describes how the request was served is not decidable from the
// response, so a mix is reported as a problem instead of one being preferred.
func resolveUsage(objects []usageObject) ReportedUsage {
	primary := usageObject{values: map[string]int{}}
	oracle := usageObject{values: map[string]int{}, oracle: true}
	var primaryKeys, oracleKeys []string
	dialects := map[string]bool{}
	mixed := false
	for _, obj := range objects {
		if obj.oracle {
			// The oracle block declares its own vocabulary (semantic: openai), so
			// it is read through the OpenAI names it carries; its zero-valued
			// legacy Anthropic fields are not a second reading.
			for key, n := range obj.values {
				if isOpenAIKey(key) {
					oracle.values[key] = n
				}
			}
			oracleKeys = append(oracleKeys, obj.keys()...)
			continue
		}
		anthropic, openai := false, false
		for key, n := range obj.values {
			primary.values[key] = n
			if isAnthropicKey(key) {
				anthropic = true
			}
			if isOpenAIKey(key) {
				openai = true
			}
		}
		if anthropic && openai {
			// One object mixing the dialects is a parse hazard, not a fold rule.
			mixed = true
		}
		if anthropic {
			dialects[UsageShapeAnthropic] = true
		}
		if openai {
			dialects[UsageShapeOpenAI] = true
		}
		primaryKeys = append(primaryKeys, obj.keys()...)
	}

	out := ReportedUsage{Keys: sortedUnique(primaryKeys)}
	if len(primary.values) == 0 && len(oracle.values) == 0 {
		out.Problem = UsageProblemNoUsage
		return out
	}
	if len(primary.values) == 0 {
		// Only the gateway's private block was present. A reading exists, but it
		// is the gateway's own account, so it is named as such and no split is
		// claimed for the protocol's usage event.
		out.Reported = true
		out.Problem = UsageProblemOracleOnly
		out.OracleKeys = sortedUnique(oracleKeys)
		out.OraclePresent = true
		out.OraclePrompt, out.OracleHit, out.OracleMiss = oracleSplit(oracle)
		return out
	}

	out.Reported = true
	out.Completion, _ = primary.firstOf(keyCompletionTokens, keyOutputTokens)
	if mixed {
		out.Problem = UsageProblemUnresolved
	} else if len(dialects) > 1 {
		out.Problem = UsageProblemDialectMix
	}
	if out.Problem != "" {
		return withOracle(out, oracle, oracleKeys)
	}

	switch {
	case dialects[UsageShapeAnthropic]:
		out.Shape = UsageShapeAnthropic
		out = resolveAnthropic(primary, out)
	case dialects[UsageShapeOpenAI]:
		out.Shape = UsageShapeOpenAI
		out = resolveOpenAI(primary, out)
	}
	return withOracle(out, oracle, oracleKeys)
}

// withOracle attaches the gateway's own reading and says whether it agrees with
// the primary one. A response without an oracle keeps OraclePresent false, so a
// missing oracle is never read as an agreeing one.
func withOracle(out ReportedUsage, oracle usageObject, keys []string) ReportedUsage {
	if len(oracle.values) == 0 {
		return out
	}
	out.OracleKeys = sortedUnique(keys)
	out.OraclePresent = true
	out.OraclePrompt, out.OracleHit, out.OracleMiss = oracleSplit(oracle)
	out.OracleAgrees = out.Split &&
		out.Prompt == out.OraclePrompt && out.Hit == out.OracleHit && out.Miss == out.OracleMiss
	return out
}

// oracleSplit reads the oracle's own hit/miss split. It reports zeroes when the
// block does not resolve, which the caller reads together with OraclePresent.
func oracleSplit(oracle usageObject) (prompt, hit, miss int) {
	resolved := resolveOpenAI(oracle, ReportedUsage{Reported: true, Shape: UsageShapeOpenAI})
	if !resolved.Split {
		return 0, 0, 0
	}
	return resolved.Prompt, resolved.Hit, resolved.Miss
}

// resolveAnthropic reads the Anthropic vocabulary, where input_tokens counts the
// prompt that was not cached and cache_creation_input_tokens counts what was
// written: the miss side is the sum of those two.
func resolveAnthropic(obj usageObject, out ReportedUsage) ReportedUsage {
	prompt, ok := obj.get(keyInputTokens)
	if !ok {
		out.Problem = UsageProblemNoPrompt
		return out
	}
	read, ok := obj.get(keyCacheReadInput)
	if !ok {
		out.Problem = UsageProblemNoCacheRead
		return out
	}
	write, _ := obj.firstOf(keyCacheCreationInput)
	out.Hit, out.Write, out.Miss = read, write, prompt+write
	out.Prompt = out.Hit + out.Miss
	out.Split = true
	return out
}

// resolveOpenAI reads the OpenAI-compatible vocabulary, where prompt_tokens is
// the whole prompt and the cache read is a subset of it (the DeepSeek spelling
// reports the miss explicitly, so that value is preferred when present).
func resolveOpenAI(obj usageObject, out ReportedUsage) ReportedUsage {
	prompt, ok := obj.get(keyPromptTokens)
	if !ok {
		out.Problem = UsageProblemNoPrompt
		return out
	}
	hit, ok := obj.firstOf(keyCacheHitTokens, keyCachedTokens, keyCacheReadTokens)
	if !ok {
		out.Problem = UsageProblemNoCacheRead
		return out
	}
	miss, ok := obj.get(keyCacheMissTokens)
	if !ok {
		miss = prompt - hit
	}
	if miss < 0 {
		out.Problem = UsageProblemNegativeSplit
		return out
	}
	out.Prompt, out.Hit, out.Miss = prompt, hit, miss
	out.Split = true
	return out
}

// sortedUnique returns the names in a stable order without duplicates, so the
// journal records the same audit trail for the same response.
func sortedUnique(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// asInt accepts a JSON number as an integer token count.
func asInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}

// jsonChunks yields the JSON values a response body carries: an event stream's
// data payloads, or the body itself when it is plain JSON documents.
func jsonChunks(body []byte) []any {
	if bytes.Contains(body, []byte("data:")) {
		return sseChunks(body)
	}
	var out []any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	for {
		var value any
		if err := dec.Decode(&value); err != nil {
			return out
		}
		out = append(out, value)
	}
}

// sseChunks decodes the data payloads of an event stream, skipping the ones that
// are not JSON objects (comments, keep-alives, terminators).
func sseChunks(body []byte) []any {
	var out []any
	for _, line := range bytes.Split(body, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		payload, ok := bytes.CutPrefix(trimmed, []byte("data:"))
		if !ok {
			continue
		}
		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 || payload[0] != '{' {
			continue
		}
		var value any
		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			continue
		}
		out = append(out, value)
	}
	return out
}
