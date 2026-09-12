package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"reasonix/internal/i18n"
)

// truncateReadFileOutput returns a contiguous prefix on a complete rendered
// line when possible. Its recovery cursor is exactly the first unseen byte, so
// paging can reconstruct the original without a gap or duplicate. budget is a
// parameter rather than a read of maxToolOutputBytes so a smaller preview can
// be measured before it ships; the one production call site passes
// maxToolOutputBytes.
//
// Lowering budget was tried and measured, and it does not pay: two paired
// real-provider experiments (2x10 runs) both showed the smaller preview
// *costing* prompt tokens, median +21,502 and +6,882, every pair differing the
// same way. A smaller preview forces roughly one more round trip per session,
// and each round trip resends the whole accumulated context — a term the
// synthetic curve in read_file_budget_ab_test.go cannot see. Re-measure there
// before proposing a smaller budget; never reason from the bytes saved alone.
func truncateReadFileOutput(s, toolName, toolCallID string, budget int) (string, string) {
	resultRef := toolResultRef(toolCallID, s)
	headKeep := budget - 1024
	if headKeep < 1024 {
		headKeep = budget / 2
	}
	head := snapToRuneBoundary(s, 0, min(headKeep, len(s)))
	if newline := strings.LastIndexByte(head, '\n'); newline >= 1024 {
		head = head[:newline+1]
	}
	for range 4 {
		marker := toolOutputRecoveryMarkerAt(toolName, toolCallID, resultRef, len(s), len(head), len(head))
		if len(head)+len(marker) <= budget {
			notice := fmt.Sprintf(i18n.M.ToolOutputTruncatedFmt, len(s)-len(head), len(s))
			return head + marker, notice
		}
		trimTo := len(head) - (len(head) + len(marker) - budget)
		if trimTo <= 0 {
			head = ""
			continue
		}
		head = snapToRuneBoundary(head, 0, trimTo)
		if newline := strings.LastIndexByte(head, '\n'); newline >= 0 {
			head = head[:newline+1]
		}
	}
	marker := toolOutputRecoveryMarkerAt(toolName, toolCallID, resultRef, len(s), len(head), len(head))
	if len(marker) > budget {
		marker = snapToRuneBoundary(marker, 0, budget)
	}
	notice := fmt.Sprintf(i18n.M.ToolOutputTruncatedFmt, len(s)-len(head), len(s))
	return head + marker, notice
}

func snapToRuneBoundary(s string, lo, hi int) string {
	for lo > 0 && !utf8.RuneStart(s[lo]) {
		lo--
	}
	for hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi++
	}
	return s[lo:hi]
}
