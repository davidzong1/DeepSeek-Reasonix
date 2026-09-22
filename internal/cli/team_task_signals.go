package cli

// The task service's half of the wait route's signal side: a member's durable
// state move is reported into the registry's bus, so a leader blocked in its
// interruptible wait is released with the reason in hand (see attention.go).

// attention reports one durable task move into the registry's wait bus.
// Registering it is the whole of this half of the producer side: the durable
// wakeup row is still written, by wakeLeader, on the same move — this is the
// edge trigger beside it.
func (s *teamTaskService) attention(kind, id, summary string) {
	if s == nil {
		return
	}
	if bus := s.signals.Load(); bus != nil {
		bus.Signal(WaitEvent{Kind: kind, Team: s.teamName, ID: id, Summary: summary})
	}
}

// setSignals installs the registry's wait bus on this service and every per-team
// child, including children created before the overlay wired the bus.
func (s *teamTaskService) setSignals(bus *waitBus) {
	if s == nil {
		return
	}
	s.signals.Store(bus)
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	for _, child := range s.teams {
		if child != s {
			child.signals.Store(bus)
		}
	}
}
