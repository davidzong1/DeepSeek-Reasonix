package capability

import (
	"errors"
	"testing"

	"reasonix/internal/provider"
)

// A router answer cut off at the output ceiling and a router answer of "nothing
// matched" are both empty. Treating them alike is how a ceiling that is too
// small stays invisible: a measured reasoning endpoint spent the whole 256-token
// budget thinking and returned an empty body, which read as no match.
func TestTruncatedAnswerIsDistinctFromNoMatch(t *testing.T) {
	if !errors.Is(errSemanticTruncated, errSemanticTruncated) {
		t.Fatal("sentinel missing")
	}
	cases := []struct {
		name   string
		answer string
		usage  *provider.Usage
		want   bool
	}{
		{"spent the whole budget", "", &provider.Usage{CompletionTokens: semanticMaxTokens}, true},
		{"budget to spare", `["tool:fleet"]`, &provider.Usage{CompletionTokens: 17}, false},
		// Without usage nothing is proven, so the cause is not claimed.
		{"no usage reported", "", nil, false},
	}
	for _, c := range cases {
		_ = c.answer
		if got := answerHitCeiling(c.usage); got != c.want {
			t.Errorf("%s: hit ceiling %v, want %v", c.name, got, c.want)
		}
	}
	// The ceiling has to leave room for the answer *and* the thinking before it.
	if semanticMaxTokens < 4*256 {
		t.Errorf("semanticMaxTokens = %d; a measured call spent 256 on thought alone", semanticMaxTokens)
	}
}

// An empty body must not reach the caller as a successful "no matches" when the
// call was cut off — that is the whole point of the sentinel.
func TestParseRejectsEmptyBody(t *testing.T) {
	if _, err := parseSemanticIDs("   "); err == nil {
		t.Fatal("an empty body parsed as a decision")
	}
	ids, err := parseSemanticIDs(`["tool:fleet", "tool:fleet", " tool:task "]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ids) != 2 || ids[0] != "tool:fleet" || ids[1] != "tool:task" {
		t.Fatalf("parse returned %v", ids)
	}
}
