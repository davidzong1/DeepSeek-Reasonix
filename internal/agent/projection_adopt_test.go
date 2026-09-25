package agent

import (
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestAdoptCoveringProviderViewRestoresABrokenFold(t *testing.T) {
	sess := NewSession("system")
	for range 8 {
		sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("canonical ", 400)})
		sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("answer ", 400)})
	}
	a := New(nil, nil, sess, Options{ContextWindow: 8_000}, event.Discard)
	if a.ProjectionValid() {
		t.Fatal("a fresh session has no fold")
	}
	unfolded := a.ContextUsedTokens()
	view := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "summary of the canonical log"},
	}
	if !a.AdoptCoveringProviderView(view) {
		t.Fatal("adopt refused a shorter provider view")
	}
	if !a.ProjectionValid() {
		t.Fatal("adopted view does not validate")
	}
	folded := a.ContextUsedTokens()
	if folded >= unfolded {
		t.Fatalf("folded tokens = %d, want less than the canonical %d", folded, unfolded)
	}
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "new tail"})
	visible := a.modelVisibleMessages()
	if len(visible) != 3 || visible[len(visible)-1].Content != "new tail" {
		t.Fatalf("visible = %+v, want the adopted view plus the new tail", visible)
	}
	if a.ContextUsedTokens() >= unfolded {
		t.Fatal("the new tail re-expanded the gauge to the whole canonical log")
	}
}

func TestAdoptCoveringProviderViewRefusesTheFullTranscript(t *testing.T) {
	sess := NewSession("system")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "only"})
	a := New(nil, nil, sess, Options{}, event.Discard)
	if a.AdoptCoveringProviderView(sess.Snapshot()) {
		t.Fatal("the full transcript is not a fold")
	}
}
