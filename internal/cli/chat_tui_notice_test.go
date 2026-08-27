package cli

import (
	"strings"
	"testing"

	"reasonix/internal/event"
)

// TestNoticeDetailReachesTranscript pins the diagnostic that used to be dropped:
// the Notice branch rendered only Text, so every protocol-repair warning read as
// the same sentence no matter which of the five contract failures fired. The
// cause lives in Detail and nothing else persisted it — not the trajectory, not a
// log — so it has to reach the transcript.
func TestNoticeDetailReachesTranscript(t *testing.T) {
	m := newTestChatTUI()
	m.width = 100
	m.ingestEvent(event.Event{
		Kind:   event.Notice,
		Level:  event.LevelWarn,
		Text:   "The model did not complete the required turn protocol; requesting one repair.",
		Detail: "model ended without the required finish tool call",
	})
	joined := strings.Join(m.transcript, "\n")
	if !strings.Contains(joined, "required turn protocol") {
		t.Fatalf("the headline must render:\n%s", joined)
	}
	if !strings.Contains(joined, "model ended without the required finish tool call") {
		t.Fatalf("the cause must render too, or the warning is undiagnosable:\n%s", joined)
	}
	// The warning glyph stays on the headline; the cause is a dim continuation.
	if !strings.Contains(joined, "! The model did not") {
		t.Errorf("warn level must keep its glyph:\n%s", joined)
	}
}

// TestNoticeWithoutDetailAddsNoLine pins the quiet case: an ordinary notice
// carries no detail, and must not gain a blank continuation line for it.
func TestNoticeWithoutDetailAddsNoLine(t *testing.T) {
	m := newTestChatTUI()
	m.width = 100
	before := len(m.transcript)
	m.ingestEvent(event.Event{Kind: event.Notice, Text: "compacting"})
	if got := len(m.transcript) - before; got != 1 {
		t.Errorf("a detail-less notice must commit exactly one line, got %d", got)
	}
	if got := noticeDetailLines("   ", 80); got != nil {
		t.Errorf("blank detail must produce no lines, got %#v", got)
	}
}

// TestNoticeDetailWrapsInsideTheFrame pins the width contract: a long cause wraps
// into the transcript instead of overflowing it, measured in terminal cells so a
// CJK cause counts its real columns.
func TestNoticeDetailWrapsInsideTheFrame(t *testing.T) {
	long := "finish was rejected; call it once with a valid outcome after the visible answer"
	lines := noticeDetailLines(long, 40)
	if len(lines) < 2 {
		t.Fatalf("a long cause must wrap, got %d line(s): %#v", len(lines), lines)
	}
	for _, l := range lines {
		if w := visibleWidth("    " + l); w > 40 {
			t.Errorf("wrapped line overflows the frame (%d cells): %q", w, l)
		}
	}
	cjk := noticeDetailLines(strings.Repeat("协议修复失败", 8), 40)
	for _, l := range cjk {
		if w := visibleWidth("    " + l); w > 40 {
			t.Errorf("CJK line overflows the frame (%d cells): %q", w, l)
		}
	}
}
