package cli

// Team-scale operations on the task service: how many members one dispatch
// starts at once (the fan-out), and how large a roster the backend registry's
// retention cap has to hold. Both answer "how much of a team runs together".

import (
	"context"
	"strings"
	"sync"

	teamscheduler "reasonix/internal/team/scheduler"
)

// assignSubtasks fans one subtask out to several members in a single tool call.
// The serial loop it replaces paid each member's boot.Build in turn, so the Nth
// member did not start until the first N-1 had finished assembling — the
// dispatch path turning "all at once" into "one after another" (§3.3). Every
// member keeps assignSubtask's own semantics: its own durable row, its own
// dispatch, its own failure, so a member whose assembly is refused never rolls
// back a sibling that already started. Results keep the caller's member order,
// so a caller can report per-member outcomes without correlating them back.
func (s *teamTaskService) assignSubtasks(ctx context.Context, memberIDs []string, subtask, contextText string) ([]teamscheduler.Assignment, []error) {
	assignments := make([]teamscheduler.Assignment, len(memberIDs))
	errs := make([]error, len(memberIDs))
	var wg sync.WaitGroup
	for i, memberID := range memberIDs {
		wg.Go(func() {
			assignments[i], errs[i] = s.assignSubtask(ctx, memberID, subtask, contextText)
		})
	}
	wg.Wait()
	return assignments, errs
}

// rosterSize reports how many member slots a team declares, so the backend
// registry can size its retention cap to a whole roster instead of retiring a
// member the team is about to bind again (§B4). Zero for an unknown or
// unreadable team, where the registry keeps the cap it was configured with.
func (s *teamTaskService) rosterSize(teamName string) int {
	if s == nil || s.teamStore == nil {
		return 0
	}
	name := strings.TrimSpace(teamName)
	if name == "" {
		return 0
	}
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return 0
	}
	for _, t := range doc.Teams {
		if t.Name == name {
			return len(t.Template)
		}
	}
	return 0
}
