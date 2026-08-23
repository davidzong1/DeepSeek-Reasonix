package cli

import (
	"strings"
	"testing"

	"reasonix/internal/team"
)

// TestPadColumnAndTruncateMeasureCells pins the width primitives the team
// panels align with: both count terminal cells, so ANSI SGR codes add nothing
// and CJK/emoji count as the two columns they occupy.
func TestPadColumnAndTruncateMeasureCells(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want int
	}{
		{"plain", padColumn("ab", 6), 6},
		{"styled", padColumn(accent("ab"), 6), 6},
		{"wide", padColumn("中文", 6), 6},
		{"styled wide", padColumn(dim("中文"), 6), 6},
	} {
		if w := visibleWidth(tc.got); w != tc.want {
			t.Errorf("%s: padColumn width = %d, want %d", tc.name, w, tc.want)
		}
	}
	// A value already at or past the column keeps one space before the divider.
	if got := padColumn("中文中文", 4); !strings.HasSuffix(got, " ") {
		t.Errorf("padColumn must always leave a trailing space, got %q", got)
	}
	for _, s := range []string{"中文中文中文", accent("中文中文"), "abcdefghij"} {
		if w := visibleWidth(truncateCells(s, 6)); w > 6 {
			t.Errorf("truncateCells(%q, 6) width = %d, want <= 6", s, w)
		}
	}
}

// TestSessionPanelDividerStaysInOneColumn pins the session window's two-column
// layout: the divider must land in the same terminal column on every row.
// Measuring rune counts instead of cells put it at a different column per row
// — ANSI codes counted as characters and CJK counted as one cell each.
func TestSessionPanelDividerStaysInOneColumn(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		{MemberID: "alice", Role: team.RoleTester, Status: team.MemberStatusActive},
	}})
	m := openTeamOverlay(t)
	if !m.teamPick.session.active {
		t.Fatal("the click must open the session window")
	}
	for _, text := range []string{"你好，这是一条中文消息", "plain ascii reply", "混合 mixed 内容"} {
		if err := m.teamPick.sessions.AppendMessage("alpha", "lead", team.SessionMessage{
			Kind: "agent", Text: text, TS: "2026-08-22T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}

	column := -1
	rows := 0
	for line := range strings.SplitSeq(m.renderTeamPicker(), "\n") {
		before, _, found := strings.Cut(line, "│")
		if !found {
			continue
		}
		rows++
		at := visibleWidth(before)
		if column == -1 {
			column = at
			continue
		}
		if at != column {
			t.Errorf("divider at column %d on row %d, want %d:\n%s", at, rows, column, line)
		}
	}
	if rows < 4 {
		t.Fatalf("expected the session panel to render several divided rows, got %d", rows)
	}
}
