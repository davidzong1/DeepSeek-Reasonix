package agent

// Reasoning-language injection is once per conversation (R3): the block is a
// session constant, so repeating it on every user turn costs its bytes in every
// request prefix while telling the model nothing new.

import (
	"strings"
	"testing"

	"reasonix/internal/event"
)

// TestReasoningLanguageInjectedOncePerConversation is the token contract: the
// first turn that resolves to zh carries the block, and later turns in the same
// conversation do not — the model already read it.
func TestReasoningLanguageInjectedOncePerConversation(t *testing.T) {
	a := New(nil, nil, NewSession(""), Options{}, event.Discard)

	first := a.WithReasoningLanguageOnce("请修复这个 bug", "auto", "请修复这个 bug")
	if !strings.HasPrefix(first, "<reasoning-language>") {
		t.Fatalf("the first Chinese turn must carry the block, got %q", first)
	}
	if !strings.Contains(first, "简体中文") {
		t.Fatalf("the block must state the resolved language, got %q", first)
	}

	second := a.WithReasoningLanguageOnce("继续", "auto", "继续")
	if strings.Contains(second, "<reasoning-language>") {
		t.Fatalf("the second turn in the same conversation must not repeat the block, got %q", second)
	}
	if second != "继续" {
		t.Fatalf("the second turn must pass through unchanged, got %q", second)
	}
}

// TestReasoningLanguageReinjectsAfterPreferenceChange pins the other edge: a
// language switch between turns must take effect. Without clearing the memory,
// the once-per-conversation rule would silently swallow the new preference.
func TestReasoningLanguageReinjectsAfterPreferenceChange(t *testing.T) {
	a := New(nil, nil, NewSession(""), Options{}, event.Discard)
	a.SetReasoningLanguage("zh")

	if got := a.WithReasoningLanguageOnce("fix the bug", "zh", "fix the bug"); !strings.Contains(got, "简体中文") {
		t.Fatalf("an explicit zh turn must carry the block, got %q", got)
	}
	if got := a.WithReasoningLanguageOnce("continue", "zh", "continue"); strings.Contains(got, "<reasoning-language>") {
		t.Fatalf("a repeat under the same preference must not re-inject, got %q", got)
	}

	// The switch clears the memory, so the next turn injects the new language.
	a.SetReasoningLanguage("en")
	switched := a.WithReasoningLanguageOnce("continue", "en", "continue")
	if !strings.HasPrefix(switched, "<reasoning-language>") {
		t.Fatalf("a language change must re-inject once, got %q", switched)
	}
	if !strings.Contains(switched, "use English") {
		t.Fatalf("the re-injected block must state the new language, got %q", switched)
	}
	if again := a.WithReasoningLanguageOnce("carry on", "en", "carry on"); strings.Contains(again, "<reasoning-language>") {
		t.Fatalf("the new language must also inject only once, got %q", again)
	}
}

// TestReasoningLanguageRespectsCallerSuppliedBlock pins the boundary with the
// existing per-message helper: a caller that already injected the block is
// never wrapped a second time, and its injection counts as the conversation's
// one.
func TestReasoningLanguageRespectsCallerSuppliedBlock(t *testing.T) {
	a := New(nil, nil, NewSession(""), Options{}, event.Discard)

	supplied := ReasoningLanguageBlock("zh") + "\n\n请修复这个 bug"
	if got := a.WithReasoningLanguageOnce(supplied, "auto", "请修复这个 bug"); got != supplied {
		t.Fatalf("a caller-supplied block must not be duplicated:\n got %q\nwant %q", got, supplied)
	}
	// The caller's block is now the conversation's, so a later turn stays clean.
	if got := a.WithReasoningLanguageOnce("继续", "auto", "继续"); strings.Contains(got, "<reasoning-language>") {
		t.Fatalf("a caller-supplied block must count as the conversation's one, got %q", got)
	}
}

// TestReasoningLanguageAutoStaysSilentForEnglish pins the historical auto
// behaviour: an English or ambiguous turn carries no block, so nothing is
// remembered and nothing is injected.
func TestReasoningLanguageAutoStaysSilentForEnglish(t *testing.T) {
	a := New(nil, nil, NewSession(""), Options{}, event.Discard)
	for _, turn := range []string{"fix the bug", "ok"} {
		if got := a.WithReasoningLanguageOnce(turn, "auto", turn); got != turn {
			t.Fatalf("auto on %q must not inject, got %q", turn, got)
		}
	}
	if carried := a.reasoningLanguageCarried(); carried != "" {
		t.Fatalf("an auto-silent turn must remember nothing, got %q", carried)
	}
}

// TestReasoningLanguageMemoryResetsWithConversation pins the lifetime: the
// memory describes one conversation, so a new session injects again rather than
// inheriting the previous conversation's block.
func TestReasoningLanguageMemoryResetsWithConversation(t *testing.T) {
	a := New(nil, nil, NewSession(""), Options{}, event.Discard)
	if got := a.WithReasoningLanguageOnce("请修复", "auto", "请修复"); !strings.Contains(got, "<reasoning-language>") {
		t.Fatalf("first conversation must inject, got %q", got)
	}
	a.SetSession(NewSession(""))
	if got := a.WithReasoningLanguageOnce("请继续", "auto", "请继续"); !strings.Contains(got, "<reasoning-language>") {
		t.Fatalf("a new conversation must inject its own block, got %q", got)
	}
}

// TestTurnPreferencesInjectsOnce is the end-to-end half through the real turn
// path: withTurnPreferences is what every persisted user turn passes through,
// so the once-per-conversation rule has to hold there, not only on the helper.
func TestTurnPreferencesInjectsOnce(t *testing.T) {
	a := New(nil, nil, NewSession(""), Options{}, event.Discard)
	a.SetReasoningLanguage("zh")

	first := a.withTurnPreferences("fix the bug")
	second := a.withTurnPreferences("continue")
	if !strings.Contains(first, "<reasoning-language>") {
		t.Fatalf("the first turn must carry the block, got %q", first)
	}
	if strings.Contains(second, "<reasoning-language>") {
		t.Fatalf("the second turn must not repeat the block, got %q", second)
	}
}
