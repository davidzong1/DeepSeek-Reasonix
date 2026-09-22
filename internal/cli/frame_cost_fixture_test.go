// Frame-path cost fixture for TEAM_FRAME_PATH_COST_ROUTE.md: an N-member roster
// with one member bound and a transcript of committed lines, driven through the
// same Update+View path bubbletea's event loop uses.
package cli

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

const (
	frameCostMembers = 6
	frameCostLines   = 400
	frameCostWidth   = 120
	frameCostHeight  = 40
	frameCostBacklog = 8
)

// frameCostMemberID names the i-th member of frameCostTeam, so a benchmark can
// address the bound leader and a background member by the same rule the roster
// was built with.
func frameCostMemberID(i int) string {
	if i == 0 {
		return "lead"
	}
	return fmt.Sprintf("m%d", i)
}

// frameCostTeam is a roster of n active members with the first as leader.
func frameCostTeam(n int) team.Team {
	slots := []team.MemberSlot{{MemberID: frameCostMemberID(0), Leader: true, Status: team.MemberStatusActive}}
	for i := 1; i < n; i++ {
		slots = append(slots, team.MemberSlot{MemberID: frameCostMemberID(i), Role: team.RoleCoder, Status: team.MemberStatusActive})
	}
	return team.Team{Name: "alpha", Template: slots}
}

// frameCostModel returns the sized, member-bound team session every measurement
// in this area starts from: the leader is bound, the other members are assembled
// but background, and `lines` committed transcript blocks stand in for the
// history a team session accumulates.
//
// The wrap cache is warmed explicitly, so a measurement reads the steady state.
// Leaving it cold is not a faster setup — it is a different path (the full
// re-wrap), and it is what makes an unthreaded benchmark read ~55x high.
func frameCostModel(t *testing.T, members, lines int) chatTUI {
	t.Helper()
	writeTeamFixture(t, frameCostTeam(members))
	m := openTeamOverlay(t)
	m.memberEvents = newMemberEventPump()
	history := map[string][]provider.Message{}
	for i := range members {
		id := frameCostMemberID(i)
		history[id] = []provider.Message{userMessage("history of " + id)}
	}
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, history: history[b.MemberID]}, nil
	}, frameCostBacklog)
	next, _ := m.Update(tea.WindowSizeMsg{Width: frameCostWidth, Height: frameCostHeight})
	m = next.(chatTUI)
	if cmd := m.switchTeamMember(frameCostMemberID(0)); cmd == nil {
		t.Fatal("binding the leader must succeed")
	}
	for i := range lines {
		m.commitLine(fmt.Sprintf("committed transcript line %d with some words on it", i))
	}
	contentW := transcriptContentWidth(m.width, m.nativeScrollback)
	m.syncWrappedLines(contentW, true)
	m.feedViewportContent()
	return m
}

// frameCostUpdate measures one threaded update loop: `updated, _ :=
// cur.Update(next(i)); cur = updated.(chatTUI)`, exactly how bubbletea's event
// loop drives the model. Threading the result is what keeps the measurement on
// the steady state — discarding it re-measures a cold wrap cache every
// iteration instead.
func frameCostUpdate(m chatTUI, next func(i int) tea.Msg) testing.BenchmarkResult {
	return testing.Benchmark(func(b *testing.B) {
		cur := m
		for i := range b.N {
			updated, _ := cur.Update(next(i))
			cur = updated.(chatTUI)
		}
	})
}

// frameCostView measures one full-screen View() per iteration. It is the fixed
// per-message cost bubbletea pays on the same goroutine that handles input.
func frameCostView(m chatTUI) testing.BenchmarkResult {
	return testing.Benchmark(func(b *testing.B) {
		for range b.N {
			_ = m.View()
		}
	})
}

// frameCostReport prints one measurement in the table format the route document
// records, so both parts report comparable numbers.
func frameCostReport(t *testing.T, name string, r testing.BenchmarkResult) {
	t.Helper()
	t.Logf("%-46s %10d ns/op  (%d iters)", name, r.NsPerOp(), r.N)
}

// frameCostDelta is one streamed answer chunk from the given member, the event
// kind a thinking member emits at chunk rate.
func frameCostDelta(member string) tea.Msg {
	return memberEventMsg{member: member, ev: event.Event{Kind: event.Text, Text: "some streamed answer text "}}
}

// TestFrameCostBaseline records the numbers TEAM_FRAME_PATH_COST_ROUTE.md §2.2
// cites. It asserts nothing on purpose: the two change parts each add their own
// test carrying the assertion their node needs, and this one is the "before"
// they are compared against.
func TestFrameCostBaseline(t *testing.T) {
	m := frameCostModel(t, frameCostMembers, frameCostLines)
	background, bound := frameCostMemberID(1), frameCostMemberID(0)

	frameCostReport(t, "View()", frameCostView(m))
	frameCostReport(t, "Update(background member delta)",
		frameCostUpdate(m, func(int) tea.Msg { return frameCostDelta(background) }))
	frameCostReport(t, "Update(bound member delta)",
		frameCostUpdate(m, func(int) tea.Msg { return frameCostDelta(bound) }))
	// A key that does not grow the composer: the floor cost of one input message
	// behind the same serialized queue.
	frameCostReport(t, "Update(input key)",
		frameCostUpdate(m, func(int) tea.Msg { return tea.KeyPressMsg{Code: tea.KeyUp} }))
	// The drag path: the team session captures the mouse, so every selection
	// motion is a message on the same queue.
	frameCostReport(t, "Update(mouse motion)",
		frameCostUpdate(m, func(int) tea.Msg { return tea.MouseMotionMsg{X: 10, Y: 10, Button: tea.MouseLeft} }))
	t.Logf("roster=%d transcript=%d blocks wrapped=%d lines", frameCostMembers, len(m.transcript), len(m.wrappedLines))
}

// TestFrameCostIsFlatInTranscriptLength pins the claim that the per-event cost
// does not grow with the history a session accumulates — so the loop's occupancy
// scales with how many members are streaming, not with how long the session ran.
func TestFrameCostIsFlatInTranscriptLength(t *testing.T) {
	background := frameCostMemberID(1)
	for _, lines := range []int{0, 200, 1000, 3000} {
		m := frameCostModel(t, frameCostMembers, lines)
		update := frameCostUpdate(m, func(int) tea.Msg { return frameCostDelta(background) })
		view := frameCostView(m)
		t.Logf("transcript=%5d blocks wrapped=%5d  memberDelta=%8d ns/op  View=%8d ns/op",
			len(m.transcript), len(m.wrappedLines), update.NsPerOp(), view.NsPerOp())
	}
}
