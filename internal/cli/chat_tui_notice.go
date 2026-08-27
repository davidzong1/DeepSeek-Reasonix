package cli

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// commitNoticeDetail renders a notice's diagnostic detail under its headline.
//
// The headline names the category; Detail names the actual cause, and it used to
// be dropped on the floor. The protocol-repair notice is what made that
// expensive: "The model did not complete the required turn protocol" is the same
// sentence for all five contract failures, and which one fired — no finish call,
// finish batched with other tools, an invalid outcome, no visible answer — is
// only ever in Detail. Nothing else persisted it either, so the one diagnostic
// the agent produced was unreadable.
func (m *chatTUI) commitNoticeDetail(detail string) {
	for _, line := range noticeDetailLines(detail, m.width) {
		m.commitLine(dim("    " + line))
	}
}

// noticeDetailLines wraps a detail into the transcript's width, indent included,
// and reports no lines for an empty detail. Wrapping is measured in terminal
// cells, so a CJK or styled cause does not overflow the frame.
func noticeDetailLines(detail string, width int) []string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return nil
	}
	// The 4-cell indent plus a small margin; a pathologically narrow frame still
	// gets a positive budget rather than one column per line.
	budget := max(width-6, 20)
	return strings.Split(ansi.Wrap(detail, budget, ""), "\n")
}
