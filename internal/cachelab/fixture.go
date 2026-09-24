package cachelab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The frozen request ladder, in bytes of request content. The arms are
// registered against these sizes so a "long context" arm means one fixed size
// rather than whatever a run happened to produce.
const (
	LadderSmallBytes = 8_000
	LadderMidBytes   = 60_000
	LadderLargeBytes = 240_000
)

// The registered single-component perturbations. Each moves exactly one part of
// the provider-visible request, so a measured difference can only be attributed
// to that part.
const (
	ComponentToolSchemaField = "tool_schema_field"
	ComponentSystemTail      = "system_tail"
	ComponentSchemaKeyOrder  = "schema_key_order"
)

// ToolSchema is one provider-visible tool declaration. Parameters is raw JSON so
// the fixture controls the exact bytes a serialization arm changes.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Fixture is one frozen synthetic request: non-sensitive by construction, sized
// to a registered ladder point, and stable across repeats of the same arm. The
// live driver turns it into a provider request; the digest it exposes is what
// proves two repeats carried the same client-side bytes.
type Fixture struct {
	// Nonce makes one run's prefix unique, so the run's first request is a
	// genuine cold start instead of reusing an earlier run's provider cache.
	Nonce       string       `json:"nonce"`
	System      string       `json:"system"`
	SystemTail  string       `json:"system_tail,omitempty"`
	Tools       []ToolSchema `json:"tools"`
	User        string       `json:"user"`
	Marker      string       `json:"marker"`
	MaxTokens   int          `json:"max_tokens"`
	Temperature float64      `json:"temperature"`
	// Component records the single perturbation applied, or "" for the baseline.
	Component string `json:"component,omitempty"`
}

// FixtureSpec is what one arm asks the fixture to build.
type FixtureSpec struct {
	Nonce     string
	Marker    string
	Bytes     int
	Component string
}

// BuildFixture assembles one frozen request. The content is filler plus a fixed
// instruction: it is non-sensitive by construction, and the same spec always
// produces the same bytes.
func BuildFixture(spec FixtureSpec) Fixture {
	size := spec.Bytes
	if size <= 0 {
		size = LadderSmallBytes
	}
	marker := strings.TrimSpace(spec.Marker)
	if marker == "" {
		marker = "CACHELAB-ACK"
	}
	f := Fixture{
		Nonce:       spec.Nonce,
		System:      frozenSystem(size/4, spec.Nonce),
		Tools:       frozenTools(),
		User:        frozenUser(size, marker),
		Marker:      marker,
		MaxTokens:   32,
		Temperature: 0,
		Component:   strings.TrimSpace(spec.Component),
	}
	f.applyComponent()
	return f
}

// frozenSystem builds the standing instruction block. It carries the nonce so the
// whole prefix is unique per run, and enough filler to sit in a reported prompt
// bucket rather than the smallest one.
func frozenSystem(bytes int, nonce string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a terse assistant under a cache-observation probe. Reply with the exact word requested and nothing else.\nrun-nonce: %s\n", nonce)
	b.WriteString("The following reference material is fixed for the whole session:\n")
	for i := 0; b.Len() < bytes; i++ {
		fmt.Fprintf(&b, "clause %05d: a stable prefix clause that never changes between turns in this session.\n", i)
	}
	return b.String()
}

// frozenUser builds the turn that sizes the request and asks for the marker.
func frozenUser(bytes int, marker string) string {
	var b strings.Builder
	b.WriteString("Reference material for this request:\n")
	for i := 0; b.Len() < bytes-200; i++ {
		fmt.Fprintf(&b, "item %06d: a stable clause that never changes between turns.\n", i)
	}
	fmt.Fprintf(&b, "\nReply with exactly the word %s and nothing else.\n", marker)
	return b.String()
}

// frozenTools is the read-only probe surface the arms hold constant. Five tools
// with non-trivial schemas keep the tool block a real part of the prefix.
func frozenTools() []ToolSchema {
	names := []string{"probe_read", "probe_list", "probe_stat", "probe_diff", "probe_search"}
	out := make([]ToolSchema, 0, len(names))
	for i, name := range names {
		out = append(out, ToolSchema{
			Name:        name,
			Description: "Read-only probe tool used to hold a stable, non-trivial provider-visible schema across turns.",
			Parameters: json.RawMessage(fmt.Sprintf(
				`{"type":"object","properties":{"path":{"type":"string","description":"absolute path to inspect"},"pattern":{"type":"string","description":"optional glob filter applied to the result set"},"limit":{"type":"integer","description":"maximum number of rows to return"}},"required":["path"],"additionalProperties":false,"probe_index":%d}`,
				i)),
		})
	}
	return out
}

// applyComponent applies exactly one registered perturbation to the baseline
// fixture. An unregistered component is refused by Validate, so an arm cannot
// run with two changes or with one nobody registered.
func (f *Fixture) applyComponent() {
	switch f.Component {
	case "":
		return
	case ComponentToolSchemaField:
		f.Component = ComponentToolSchemaField
		f.Tools[0].Description += " Restricted to the workspace root."
	case ComponentSystemTail:
		f.Component = ComponentSystemTail
		f.SystemTail = "\nAdditional constraint: never echo the reference material back.\n"
		f.System += f.SystemTail
	case ComponentSchemaKeyOrder:
		f.Component = ComponentSchemaKeyOrder
		f.Tools[1].Parameters = reorderKeys(f.Tools[1].Parameters)
	}
}

// Validate refuses a fixture whose perturbation is not a registered one.
func (f Fixture) Validate() error {
	switch f.Component {
	case "", ComponentToolSchemaField, ComponentSystemTail, ComponentSchemaKeyOrder:
		return nil
	default:
		return fmt.Errorf("cachelab: %q is not a registered component perturbation", f.Component)
	}
}

// reorderKeys rewrites a schema object with its keys reversed: the same document
// in a different byte order, which is the perturbation a serialization difference
// is supposed to represent.
func reorderKeys(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var b strings.Builder
	b.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		encoded, _ := json.Marshal(key)
		b.Write(encoded)
		b.WriteByte(':')
		b.Write(reorderKeys(obj[key]))
	}
	b.WriteByte('}')
	return json.RawMessage(b.String())
}

// Canonical is the fixture's client-side identity: the exact content the driver
// will hand the provider adapter. Two repeats of one arm must produce equal bytes
// here before the recorder even proves the wire bytes matched.
func (f Fixture) Canonical() []byte {
	encoded, err := json.Marshal(struct {
		Nonce       string       `json:"nonce"`
		System      string       `json:"system"`
		SystemTail  string       `json:"system_tail,omitempty"`
		Tools       []ToolSchema `json:"tools"`
		User        string       `json:"user"`
		Marker      string       `json:"marker"`
		MaxTokens   int          `json:"max_tokens"`
		Temperature float64      `json:"temperature"`
		Component   string       `json:"component,omitempty"`
	}{f.Nonce, f.System, f.SystemTail, f.Tools, f.User, f.Marker, f.MaxTokens, f.Temperature, f.Component})
	if err != nil {
		return nil
	}
	return encoded
}

// Digest is the local digest of the fixture's frozen bytes.
func (f Fixture) Digest() string { return RequestDigest(f.Canonical()) }

// GuardLiterals are distinctive fragments of the frozen text that must never
// appear in a journal line. The driver registers them so a leak is refused on the
// write path instead of reviewed for later.
func (f Fixture) GuardLiterals() []string {
	return []string{"a stable prefix clause that never changes", "Reply with exactly the word", f.Marker}
}

// ConfigDigest hashes the frozen configuration banner one arm runs under, so a
// drifted endpoint, model or client build is visible per sample.
func ConfigDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
