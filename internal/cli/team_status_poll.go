// Leader status polling: a status answer is ~48 tokens, but every read re-sends
// the whole provider prefix, so an unthrottled poll loop costs the prefix per
// call rather than the answer.
package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"reasonix/internal/team"
)

// minStatusPollInterval is the floor between two leader status reads that
// answer identically. It is a floor, not a cap: a status that actually changed
// is returned immediately (see checkStatus), because making the leader wait out
// the interval to see a finished member would trade tokens for latency.
const minStatusPollInterval = 60 * time.Second

// statusPollState memoizes the last answer per query key so an unchanged poll
// can be recognized without the caller paying for it again. Keyed by the
// queried member id ("" = the whole roster) so one member's polling never
// throttles another's.
type statusPollState struct {
	mu   sync.Mutex
	last map[string]statusPollEntry
}

type statusPollEntry struct {
	at   time.Time
	text string
}

// pollClock resolves the interval clock, defaulting to the wall clock.
func (s *teamTaskService) pollClock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// throttledStatus reports whether the previous answer for key still stands and
// how long until the key may be polled again. An answer that changed never
// throttles: the memo comparison is what makes "state moved" bypass the floor.
func (s *teamTaskService) throttledStatus(key, fresh string) (string, bool) {
	now := s.pollClock()
	s.statusPoll.mu.Lock()
	defer s.statusPoll.mu.Unlock()
	prev, ok := s.statusPoll.last[key]
	if ok && prev.text == fresh {
		if elapsed := now.Sub(prev.at); elapsed < minStatusPollInterval {
			return statusUnchangedReply(fresh, minStatusPollInterval-elapsed), true
		}
	}
	if s.statusPoll.last == nil {
		s.statusPoll.last = map[string]statusPollEntry{}
	}
	s.statusPoll.last[key] = statusPollEntry{at: now, text: fresh}
	return "", false
}

// statusUnchangedReply is the throttled answer. The roster text is repeated
// verbatim so the provider prefix stays byte-stable and only this tail differs;
// the remaining seconds tell the leader exactly when the next read is worth
// making, which is what stops a blind retry loop.
func statusUnchangedReply(text string, remaining time.Duration) string {
	return fmt.Sprintf("unchanged (next read in %ds)\n%s", max(int(remaining.Seconds()), 0), text)
}

// checkStatus answers the leader's durable-status read, throttled to
// minStatusPollInterval. The read itself is unconditional: a throttle that
// skipped it could not tell "unchanged" from "changed", and a changed status
// must reach the leader immediately. Only the reply is suppressed, never the
// read — so a finished member is never hidden behind the floor.
func (s *teamTaskService) checkStatus(memberID string) (string, error) {
	if s == nil || s.teamStore == nil || s.board == nil {
		return "", fmt.Errorf("team task runtime is unavailable")
	}
	text, err := s.readStatus(memberID)
	if err != nil {
		return "", err
	}
	if throttled, ok := s.throttledStatus(strings.TrimSpace(memberID), text); ok {
		return throttled, nil
	}
	return text, nil
}

// readStatus renders the roster's durable task state. Split from checkStatus so
// the throttle wraps a pure read and the freshness comparison sees the real
// answer rather than a previously throttled one.
func (s *teamTaskService) readStatus(memberID string) (string, error) {
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	tasks, err := s.board.LoadLiveTasks(ctx)
	if err != nil {
		return "", err
	}
	taskByMember := map[string][]team.Task{}
	for _, task := range tasks {
		taskByMember[task.AssignedMember] = append(taskByMember[task.AssignedMember], task)
	}
	var lines []string
	for _, t := range doc.Teams {
		if t.Name != s.teamName {
			continue
		}
		for _, slot := range t.Template {
			if !slot.IsLeader() && memberID != "" && slot.MemberID != memberID {
				continue
			}
			state := "idle"
			if owned := taskByMember[slot.MemberID]; len(owned) > 0 {
				states := make([]string, 0, len(owned))
				for _, task := range owned {
					states = append(states, memberTaskState(task, s.driving(task.ID)))
				}
				state = strings.Join(states, "; ")
			}
			role := string(slot.Role)
			if role == "" {
				role = "unconfigured"
			}
			lines = append(lines, fmt.Sprintf("%s: role=%s state=%s", slot.MemberID, role, state))
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", team.ErrTeamNotFound
}
