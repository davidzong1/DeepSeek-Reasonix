package boot

import (
	"context"
	"strings"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
	"reasonix/internal/team/scheduler"
)

// ControllerAsAgent adapts a shared control.Controller to the team runtime's
// AgentAPI: the controller's driving surface (Submit/SubmitUserTurn/Cancel/
// Running/Turn/Compose/Close) is exactly the member agent backend the runtime
// drives. Hosts keep one controller per member window and wire the lookup
// into agentruntime.NewRuntime's agents function.
func ControllerAsAgent(c *control.Controller) agentruntime.AgentAPI { return c }

// Compile-time proof: the controller satisfies the member agent contract
// without a wrapper — a signature drift between the two surfaces fails the
// build here, in the host that assembles both.
var _ agentruntime.AgentAPI = (*control.Controller)(nil)

// RecoverTeamRuntime is the host recovery loop (§4 recovery): the durable
// store's live tasks (assigned/running) are handed to scheduler.Restore,
// which resumes those whose member is still in the fleet and persists the
// failures — a second restart never re-drives them. The store is installed
// here so failed restores always land durably; hosts call this once at
// startup after wiring the executor.
func RecoverTeamRuntime(ctx context.Context, store team.TaskStore, sched *scheduler.RuntimeScheduler, fleet []team.Member) ([]scheduler.Assignment, error) {
	sched.SetTaskStore(store)
	tasks, err := store.LoadLiveTasks(ctx)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	return sched.Restore(tasks, fleet)
}

// teamInjectTimeout bounds one turn's context assembly; a stalled board never
// blocks the turn (same bound as the cli-side wire).
const teamInjectTimeout = 2 * time.Second

// teamInjectLimit bounds one turn's injected commands; the rest wait for the
// next turn (same bound as the cli-side wire).
const teamInjectLimit = 8

// WireTeamInjector installs the runtime's §7 context assembly (route §7): the
// member's unread durable commands and the materialized board view are folded
// into InjectTask's task -> inbox -> board order, so every submitted turn
// carries the full chain instead of the default task-only assembly. Commands
// are acknowledged only after injection succeeded — a failed ack leaves the
// watermark behind and replays the batch next turn (write-before-commit, same
// semantics as the cli-side wire). Any board failure degrades to the partial
// chain, never blocks the turn. Hosts call this once at startup, after the
// runtime's agents function is wired.
func WireTeamInjector(rt *agentruntime.Runtime, store *team.SQLiteStore) {
	if rt == nil || store == nil {
		return // no board, no injection — same off-switch as the cli wire
	}
	var inboxes = map[string]*agentruntime.BoardInbox{}
	rt.SetInjector(func(task team.Task) agentruntime.AssembledContext {
		ctx, cancel := context.WithTimeout(context.Background(), teamInjectTimeout)
		defer cancel()
		view, err := boardView(ctx, store)
		if err != nil {
			view = ""
		}
		inbox, ok := inboxes[task.AssignedMember]
		if !ok {
			rec, err := bindingFor(ctx, store, task.AssignedMember)
			if err != nil || rec.MemberID == "" {
				return agentruntime.InjectTask(task, nil, view)
			}
			inbox = agentruntime.NewBoardInbox(store, team.BoardShared, task.AssignedMember, rec.Generation)
			inboxes[task.AssignedMember] = inbox
		}
		items, next, err := inbox.Fetch(ctx, -1, teamInjectLimit)
		if err != nil || len(items) == 0 {
			return agentruntime.InjectTask(task, nil, view)
		}
		if err := inbox.Ack(ctx, next); err != nil {
			return agentruntime.InjectTask(task, nil, view)
		}
		return agentruntime.InjectTask(task, items, view)
	})
}

// bindingFor returns the persisted binding of one member, or an empty record
// when the member is not bound — the server window generation is the gate, so
// a stale local window never drains commands it cannot answer for.
func bindingFor(ctx context.Context, store *team.SQLiteStore, member string) (team.BindRecord, error) {
	records, err := store.LoadBindings(ctx)
	if err != nil {
		return team.BindRecord{}, err
	}
	for _, rec := range records {
		if rec.MemberID == member {
			return rec, nil
		}
	}
	return team.BindRecord{}, nil
}

// boardView renders the shared board's materialized conclusions as the §7
// board link body; an empty or unreadable board yields the empty link.
func boardView(ctx context.Context, store *team.SQLiteStore) (string, error) {
	view, err := store.ReadView(ctx, team.BoardShared, team.ViewSpec{Limit: 16})
	if err != nil || len(view.Conclusions) == 0 {
		return "", err
	}
	var b strings.Builder
	for _, c := range view.Conclusions {
		b.WriteString("[task: " + string(c.TaskID) + "][" + c.Topic + "] " + c.Summary + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
