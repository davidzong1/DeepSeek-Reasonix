package runtimepolicy

import (
	"context"
	"strings"
	"testing"
)

// nilContext reaches a nil context through a function so the assertion stays
// on the guard rather than on the linter: SA1012 refuses a literal nil argument.
func nilContext() context.Context { return nil }

// TestDispatchFramingTravelsWithTheTurn pins the marker itself: it is off by
// default, on after WithDispatchFraming, and inherited by a child context.
func TestDispatchFramingTravelsWithTheTurn(t *testing.T) {
	if DispatchFramed(context.Background()) {
		t.Fatal("a plain turn must not read as host-dispatched")
	}
	if DispatchFramed(nilContext()) {
		t.Fatal("a nil context must not read as host-dispatched")
	}
	framed := WithDispatchFraming(context.Background())
	if !DispatchFramed(framed) {
		t.Fatal("WithDispatchFraming must mark the turn")
	}
	child, cancel := context.WithCancel(framed)
	defer cancel()
	if !DispatchFramed(child) {
		t.Fatal("a derived context must keep the marking")
	}
}

// TestDispatchFramedTextWouldOtherwiseBanMutation is the discriminating half:
// the wording that used to trip the parser still does, so the marker is not
// silently covering for a parser that changed underneath it.
func TestDispatchFramedTextWouldOtherwiseBanMutation(t *testing.T) {
	const order = "只读审计 W1/W2/W5 当前状态。核查 FactorHandle 是否有 solve_device；不改生产代码。将最小证据写入团队 deliverable 并报告 id。"
	c := ParseConstraints(StripQuotedConstraints(order))
	if !c.ForbidMutation || !strings.Contains(strings.Join(c.Notes, ","), "user_forbid_mutation") {
		t.Fatalf("the order text must still read as a mutation ban when parsed: %+v", c)
	}
}
