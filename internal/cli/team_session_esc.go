package cli

// Esc inside a bound member session: the key both interrupts the member's turn
// and walks the overlay's layers, and this file holds the decision of which.

// boundMemberRunning reports whether the backend the window is bound to is
// running a turn. It reads the backend rather than m.state: the window's own flag
// only tracks turns this window submitted, and every member bind resets it, so a
// member started by the leader's dispatch would read as idle while it works.
//
// controllerRunning is the recovering accessor: a stub backend in a test, or a
// hand-built mirror of the registry, promotes Running from a nil embedded port.
func (m chatTUI) boundMemberRunning() bool {
	return m.ctrl != nil && controllerRunning(m.ctrl)
}

// escBoundSession is esc inside a bound member session, in priority order:
// un-send a turn the server has not answered, interrupt the member's running
// turn, hide the panel, close the session. Only the first two concern a turn;
// the rest is the layer walk the key always was.
func (m *chatTUI) escBoundSession() {
	if m.state == tuiRunning && m.bubblePending {
		m.unsendPending()
		return
	}
	if m.cancelBoundMember() {
		return
	}
	if m.teamPick.session.panel {
		m.setSessionPanel(false)
		return
	}
	m.closeSession()
}

// cancelBoundMember interrupts the bound member's running turn and reports
// whether it consumed the key. A turn already being cancelled does not: esc must
// fall through to closing the panel or the session, or a member that takes a
// moment to stop would swallow every esc in between.
func (m *chatTUI) cancelBoundMember() bool {
	return m.boundMemberRunning() && !m.ctrl.CancelRequested() && m.requestBoundMemberStop()
}

// interruptBoundMember is Ctrl+C's half of the same action: it stops the bound
// member's live turn and consumes the press even while that cancel is in flight.
// Falling through there would reach the chat handler's quit branch, and a member
// slow to stop must not take the whole session down with it.
func (m *chatTUI) interruptBoundMember() bool {
	if !m.teamSessionBound() {
		return false
	}
	return m.requestBoundMemberStop()
}

// requestBoundMemberStop cancels the bound member's live turn unless one is
// already being cancelled, and reports whether a turn was live at all.
func (m *chatTUI) requestBoundMemberStop() bool {
	if !m.boundMemberRunning() {
		return false
	}
	if !m.ctrl.CancelRequested() {
		m.ctrl.Cancel()
	}
	return true
}
