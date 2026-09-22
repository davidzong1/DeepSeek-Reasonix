package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/provider"
)

// repeatReminderExempt names the tools whose repetition is the point. A leader's
// wait is meant to block until an event arrives, so a run of identical waits is
// the tool working, not the "is another call useful?" shape this reminder asks
// about. They still advance the streak key below, so an intervening wait breaks
// a genuine streak of a following tool rather than hiding it.
//
// The name is a literal because this package cannot import the CLI that owns the
// tool: the dependency runs the other way.
var repeatReminderExempt = map[string]bool{"leader_wait": true}

// applyRepeatReminders is advisory only. It never changes execution state,
// blocks a call, marks the turn as needing the user, or requests completion.
func (a *Agent) applyRepeatReminders(calls []provider.ToolCall, results []string) {
	for i, call := range calls {
		key := strings.ToLower(strings.TrimSpace(call.Name)) + "\x00" + canonicalToolArguments(call.Arguments)
		if key != a.turn.repeatKey {
			a.turn.repeatKey = key
			a.turn.repeatCount = 1
		} else {
			a.turn.repeatCount++
		}
		switch a.turn.repeatCount {
		case 3, 5, 8:
			if repeatReminderExempt[strings.ToLower(strings.TrimSpace(call.Name))] {
				continue
			}
			results[i] += fmt.Sprintf("\n\n[repeat reminder] %s was called %d consecutive times with the same arguments. Check whether another call is useful before repeating it again.", call.Name, a.turn.repeatCount)
		}
	}
}

func canonicalToolArguments(raw string) string {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return strings.TrimSpace(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return string(encoded)
}
