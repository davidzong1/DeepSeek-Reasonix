package control

import (
	"testing"

	"reasonix/internal/capability"
)

func cand(policy capability.AutoUse, ids ...string) capability.RouteDecision {
	d := capability.RouteDecision{}
	for _, id := range ids {
		d.Candidates = append(d.Candidates, capability.RouteCandidate{
			Entry: capability.Entry{ID: id, Kind: capability.KindTool}, Policy: policy,
		})
	}
	return d
}

// Each group below is a real request measured against the assembled router. The
// gate is what decides whether the one model call routing may make happens, so
// it decides both the token cost of a turn and — because the semantic router
// only ever re-ranks the candidates it is given — whether a request that matched
// nothing can be discovered at all.
func TestSemanticRoutingGatePerMeasuredRequest(t *testing.T) {
	cases := []struct {
		name     string
		decision capability.RouteDecision
		want     bool
	}{
		// "analyze these files in parallel" — two delegation tools suggested.
		{"g1_hit", cand(capability.AutoUseSuggest, "tool:fleet", "tool:parallel_tasks"), true},
		// "fix the typo" — nothing matched.
		{"g2_miss", cand(capability.AutoUseSuggest), false},
		// "tell me what each one does" — nothing matched.
		{"g3_fuzzy", cand(capability.AutoUseSuggest), false},
		// "check them separately and summarize" — nothing matched.
		{"g4_realistic", cand(capability.AutoUseSuggest), false},
		// A strong match already decides; the model call would be wasted.
		{"strong", cand(capability.AutoUsePrefer, "tool:fleet"), false},
	}
	for _, c := range cases {
		if got := semanticRoutingApplies(c.decision); got != c.want {
			t.Errorf("%s: semantic routing %v, want %v", c.name, got, c.want)
		}
	}
}

// The discovery call is the second, separately gated model call. Its gate is a
// multi-target signal, so the requests that never matched a trigger are read as
// "did the user name more than one thing to act on" rather than "did the user
// use the word parallel" — which is the whole point of opening it.
func TestSemanticDiscoveryGatePerMeasuredRequest(t *testing.T) {
	empty := capability.RouteDecision{}
	routed := cand(capability.AutoUseSuggest, "tool:fleet", "tool:parallel_tasks")
	cases := []struct {
		name     string
		decision capability.RouteDecision
		input    string
		want     bool
	}{
		{"g1_hit", routed, "Analyze mod1.go, mod2.go and mod3.go in parallel", false},
		{"g2_miss", empty, "Fix the typo in mod1.go: the comment says retuns", false},
		{"g3_fuzzy", empty, "Take a look at the three files in this workspace and tell me what each one does", false},
		{"g4_realistic", empty, "Check mod1.go, mod2.go and mod3.go separately and give me a combined summary", true},
		{"single target", empty, "Refactor controller.go to use a map", false},
		{"prose", empty, "Why is the build slow and what should we do about it", false},
		{"two modules by path", empty, "audit internal/boot/boot.go and internal/config/load.go for cycles", true},
	}
	for _, c := range cases {
		if got := semanticDiscoveryApplies(c.decision, c.input); got != c.want {
			t.Errorf("%s: discovery %v, want %v", c.name, got, c.want)
		}
	}
}
