package agent

import (
	"encoding/json"
	"slices"
	"testing"

	"reasonix/internal/cachereason"
	"reasonix/internal/provider"
)

func TestCaptureShapeNormalizesToolSchemaOrder(t *testing.T) {
	schemas := []provider.ToolSchema{
		{
			Name:        "write_file",
			Description: "write a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
		{
			Name:        "read_file",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
	}
	reordered := []provider.ToolSchema{schemas[1], schemas[0]}

	first := CaptureShape("system", schemas, 1)
	second := CaptureShape("system", reordered, 1)

	if first.ToolsHash != second.ToolsHash {
		t.Fatalf("ToolsHash should be stable across schema order: %q != %q", first.ToolsHash, second.ToolsHash)
	}
	if first.PrefixHash != second.PrefixHash {
		t.Fatalf("PrefixHash should be stable across schema order: %q != %q", first.PrefixHash, second.PrefixHash)
	}
	if schemas[0].Name != "write_file" || schemas[1].Name != "read_file" {
		t.Fatalf("CaptureShape mutated caller schema order: got [%s %s]", schemas[0].Name, schemas[1].Name)
	}
}

// shapeWithMessages builds a prefix shape whose conversation is exactly msgs,
// the way CapturePrefixShape does for a real request.
func shapeWithMessages(msgs ...provider.Message) PrefixShape {
	shape := CaptureShape("system", nil, 0)
	shape.Messages = CaptureMessageShape(msgs)
	return shape
}

func conversation(t *testing.T) []provider.Message {
	t.Helper()
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "do the thing"},
		{Role: provider.RoleAssistant, Content: "working"},
		{Role: provider.RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "file body"},
	}
}

// TestMessageShapeTellsAnAppendFromARewrite is the distinction the whole message
// shape exists for: appending must leave every previously sent message reusable,
// and rewriting one in place must be visible as exactly that.
func TestMessageShapeTellsAnAppendFromARewrite(t *testing.T) {
	base := conversation(t)
	prev := shapeWithMessages(base...)

	appended := append(slices.Clone(base), provider.Message{Role: provider.RoleUser, Content: "keep going"})
	cur := shapeWithMessages(appended...)

	diag := CompareShape(prev, cur, nil, nil)
	if !diag.MessagesComparable {
		t.Fatalf("an append after a previous request must be comparable: %+v", diag)
	}
	if diag.MessagesRewritten != 0 {
		t.Errorf("appending rewrote %d messages; want 0", diag.MessagesRewritten)
	}
	if want := prev.Messages.Count; diag.FirstDivergenceOffset != want {
		t.Errorf("FirstDivergenceOffset = %d, want %d (the end of the previous request)",
			diag.FirstDivergenceOffset, want)
	}
	if diag.PrefixChanged {
		t.Errorf("an append must not be reported as a prefix change: %+v", diag)
	}
	if diag.MessagePrefixHash != cur.Messages.Hash || diag.MessagePrefixHash == "" {
		t.Errorf("MessagePrefixHash = %q, want the current conversation hash %q", diag.MessagePrefixHash, cur.Messages.Hash)
	}
}

// TestMessageShapeNamesAnUnexplainedRewrite pins the case the vocabulary had no
// value for: bytes already sent were rewritten in place and no operation claimed
// the rewrite. It must not pass as an ordinary append.
func TestMessageShapeNamesAnUnexplainedRewrite(t *testing.T) {
	base := conversation(t)
	prev := shapeWithMessages(base...)

	rewritten := slices.Clone(base)
	// base[0] is the system prompt, so base[2] is conversation message 1.
	rewritten[2].Content = "something else entirely"
	cur := shapeWithMessages(rewritten...)

	diag := CompareShape(prev, cur, nil, nil)
	if diag.MessagesRewritten != 2 {
		t.Errorf("MessagesRewritten = %d, want 2 (the edited message and the one after it)", diag.MessagesRewritten)
	}
	if diag.FirstDivergenceOffset != 1 {
		t.Errorf("FirstDivergenceOffset = %d, want 1 (the edited message, system prompt excluded)", diag.FirstDivergenceOffset)
	}
	if !diag.PrefixChanged {
		t.Fatal("an unexplained rewrite of already-sent messages is a prefix change")
	}
	if !slices.Equal(diag.PrefixChangeReasons, []string{cachereason.Messages}) {
		t.Errorf("reasons = %v, want [%s]", diag.PrefixChangeReasons, cachereason.Messages)
	}
	// The stable prefix is untouched: this is a body rewrite, not a framing one.
	if diag.StablePrefixChanged {
		t.Errorf("a message rewrite must not move the stable prefix: %+v", diag)
	}
	if kind, ok := cachereason.KindOf(cachereason.Messages); !ok || kind != cachereason.Rewrite {
		t.Errorf("kind of %q = (%q, %v), want (%q, true)", cachereason.Messages, kind, ok, cachereason.Rewrite)
	}
}

// TestMessageShapeDefersToAReportedReason keeps the attribution honest: when the
// request's framing changed, a fold was claimed, or the turn tail was revised,
// the divergence is already named and must not also be reported as an
// unexplained message rewrite.
func TestMessageShapeDefersToAReportedReason(t *testing.T) {
	base := conversation(t)
	rewritten := slices.Clone(base)
	rewritten[2].Content = "something else entirely"

	for _, tc := range []struct {
		name   string
		diag   CacheDiagnostics
		absent string
		want   string
	}{
		{
			name: "a claimed fold",
			diag: CompareShape(shapeWithMessages(base...), shapeWithMessages(rewritten...), nil,
				[]string{cachereason.CompactAuto}),
			absent: cachereason.Messages,
			want:   cachereason.CompactAuto,
		},
		{
			name: "a system prompt refresh",
			diag: func() CacheDiagnostics {
				prev, cur := shapeWithMessages(base...), shapeWithMessages(rewritten...)
				cur.SystemHash = "moved"
				// PrefixHash folds the system hash in, so it moves with it.
				cur.PrefixHash = shortHash(map[string]string{"system": cur.SystemHash})
				return CompareShape(prev, cur, nil, nil)
			}(),
			absent: cachereason.Messages,
			want:   cachereason.System,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if slices.Contains(tc.diag.PrefixChangeReasons, tc.absent) {
				t.Errorf("reasons = %v must not also name %q", tc.diag.PrefixChangeReasons, tc.absent)
			}
			if !slices.Contains(tc.diag.PrefixChangeReasons, tc.want) {
				t.Errorf("reasons = %v must name %q", tc.diag.PrefixChangeReasons, tc.want)
			}
			// The magnitude is still reported, whoever named the cause.
			if tc.diag.MessagesRewritten == 0 {
				t.Error("the rewritten count must be reported even when a reason explains it")
			}
		})
	}
}

// TestMessageShapeIgnoresLocalOnlyFieldsAndTheSystemPrompt keeps the shape
// narrow: a change the provider never sees must not move it, and a change the
// system hash already reports must not be counted twice.
func TestMessageShapeIgnoresLocalOnlyFieldsAndTheSystemPrompt(t *testing.T) {
	base := conversation(t)
	first := CaptureMessageShape(base)

	local := slices.Clone(base)
	local[2].DecisionReceipts = []*provider.DecisionReceipt{{Tool: "read_file", Outcome: "approved"}}
	if got := CaptureMessageShape(local); got.Hash != first.Hash || got.Count != first.Count {
		t.Errorf("a local-only field moved the message shape: %q -> %q", first.Hash, got.Hash)
	}

	refreshed := slices.Clone(base)
	refreshed[0].Content = "system refreshed"
	if got := CaptureMessageShape(refreshed); got.Hash != first.Hash {
		t.Errorf("the system prompt is SystemHash's business, not the conversation's: %q -> %q", first.Hash, got.Hash)
	}
	if first.Count != len(base)-1 {
		t.Errorf("Count = %d, want %d conversation messages", first.Count, len(base)-1)
	}
}

// TestMessageShapeReportsNothingWithoutAPreviousRequest stops the first request
// of a session from being read as an append-only one: there is nothing to append
// to yet, so the offsets must say so rather than report zero.
func TestMessageShapeReportsNothingWithoutAPreviousRequest(t *testing.T) {
	cur := shapeWithMessages(conversation(t)...)
	diag := CompareShape(CaptureShape("system", nil, 0), cur, nil, nil)
	if diag.MessagesComparable {
		t.Error("a first request has no previous conversation to compare against")
	}
	if diag.FirstDivergenceOffset != -1 {
		t.Errorf("FirstDivergenceOffset = %d, want -1", diag.FirstDivergenceOffset)
	}
	if diag.MessagesRewritten != 0 || diag.PrefixChanged {
		t.Errorf("a first request reports no divergence: %+v", diag)
	}
}

// TestMessageShapeCountsATruncationAsARewrite covers the other direction of the
// comparison: a shorter conversation means everything past the new end was taken
// away from the provider, which is a rewrite of what it had already read.
func TestMessageShapeCountsATruncationAsARewrite(t *testing.T) {
	base := conversation(t)
	short := base[:2]
	diag := CompareShape(shapeWithMessages(base...), shapeWithMessages(short...), nil, nil)
	if diag.MessagesRewritten != 2 {
		t.Errorf("MessagesRewritten = %d, want 2", diag.MessagesRewritten)
	}
	if diag.FirstDivergenceOffset != 1 {
		t.Errorf("FirstDivergenceOffset = %d, want 1 (the last reused message)", diag.FirstDivergenceOffset)
	}
	if !slices.Contains(diag.PrefixChangeReasons, cachereason.Messages) {
		t.Errorf("reasons = %v, want %s", diag.PrefixChangeReasons, cachereason.Messages)
	}
}

func TestCompareShapeIgnoresBareLogRewriteVersionDrift(t *testing.T) {
	before := CaptureShape("system", nil, 5)
	// Simulates AddDecisionReceipt/UpdateToolCallPreview/UpdateToolCallResolution/
	// ReplaceLocalMetadata bumping rewriteVersion alone, with no drained content
	// reason: none of those touch provider-visible bytes, so this must not be
	// reported as a cache-prefix change.
	after := CaptureShape("system", nil, 9)
	if diag := CompareShape(before, after, nil, nil); diag.PrefixChanged {
		t.Fatalf("LogRewriteVersion drift with no drained reason must not report a change: %+v", diag)
	}

	if diag := CompareShape(before, after, nil, []string{"compact_auto"}); !diag.PrefixChanged || len(diag.PrefixChangeReasons) != 1 || diag.PrefixChangeReasons[0] != "compact_auto" {
		t.Fatalf("a real drained reason must be reported: %+v", diag)
	}
}
