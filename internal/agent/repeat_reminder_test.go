package agent

import (
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestRepeatReminderAtThreeFiveEightNeverBlocks(t *testing.T) {
	a := &Agent{}
	call := provider.ToolCall{Name: "read_file", Arguments: `{"limit":1,"path":"secret.txt"}`}
	for count := 1; count <= 8; count++ {
		results := []string{"ok"}
		a.applyRepeatReminders([]provider.ToolCall{call}, results)
		want := count == 3 || count == 5 || count == 8
		if got := strings.Contains(results[0], "[repeat reminder]"); got != want {
			t.Fatalf("count %d reminder=%v want=%v: %q", count, got, want, results[0])
		}
		if strings.Contains(results[0], "secret.txt") {
			t.Fatal("reminder copied sensitive arguments")
		}
	}
}

// TestRepeatReminderExemptsTheLeadersWait pins the exemption: a run of identical
// waits is the tool working, not the "is another call useful?" shape the reminder
// asks about. The streak is still tracked, so an intervening wait breaks a
// genuine streak of a following tool rather than hiding it.
func TestRepeatReminderExemptsTheLeadersWait(t *testing.T) {
	a := &Agent{}
	wait := provider.ToolCall{Name: "leader_wait", Arguments: `{"timeout_seconds":120}`}
	for count := 1; count <= 8; count++ {
		results := []string{"ok"}
		a.applyRepeatReminders([]provider.ToolCall{wait}, results)
		if strings.Contains(results[0], "[repeat reminder]") {
			t.Fatalf("count %d: the leader's wait must never carry the reminder: %q", count, results[0])
		}
	}
	if a.turn.repeatCount != 8 {
		t.Fatalf("an exempt tool must still advance the streak: count=%d", a.turn.repeatCount)
	}
	read := provider.ToolCall{Name: "read_file", Arguments: `{"path":"a"}`}
	for range 3 {
		a.applyRepeatReminders([]provider.ToolCall{read}, []string{"ok"})
	}
	if a.turn.repeatCount != 3 {
		t.Fatalf("the read streak after an intervening wait = %d, want 3", a.turn.repeatCount)
	}
}

func TestRepeatReminderCanonicalizesJSONAndResetsOnChange(t *testing.T) {
	a := &Agent{}
	for _, args := range []string{`{"path":"a","limit":1}`, `{"limit":1,"path":"a"}`, `{"path":"a", "limit":1}`} {
		results := []string{"ok"}
		a.applyRepeatReminders([]provider.ToolCall{{Name: "read_file", Arguments: args}}, results)
	}
	if a.turn.repeatCount != 3 {
		t.Fatalf("count=%d", a.turn.repeatCount)
	}
	a.applyRepeatReminders([]provider.ToolCall{{Name: "read_file", Arguments: `{"path":"b"}`}}, []string{"ok"})
	if a.turn.repeatCount != 1 {
		t.Fatalf("changed call did not reset: %d", a.turn.repeatCount)
	}
}
