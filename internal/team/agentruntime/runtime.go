package agentruntime

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"reasonix/internal/team"
)

var (
	// ErrMemberBusy reports a start onto a member already running a task.
	ErrMemberBusy = errors.New("agentruntime: member already running a task")
	// ErrTaskUnknown reports a cancel or complete for a task the runtime
	// never started.
	ErrTaskUnknown = errors.New("agentruntime: unknown task")
)

// Runtime drives task execution on member agent backends: it assembles the
// injected context, starts/cancels/resumes the member's agent, and records
// every state move on the blackboard. It implements scheduler.Executor, so
// the scheduler stays a strategy layer and this package owns execution.
type Runtime struct {
	agents   func(memberID string) (AgentAPI, error)
	inject   func(task team.Task) AssembledContext
	board    team.BoardStore
	boardID  string
	identity func(memberID string) team.Identity
	store    team.TaskStore
	wake     []WakeFunc

	mu       sync.Mutex
	live     map[team.TaskID]*runEntry
	byMember map[string]team.TaskID
}

// runEntry is one executing task: the task (with its live status), the
// member it runs on, and the member's agent backend.
type runEntry struct {
	task   team.Task
	member string
	api    AgentAPI
}

// NewRuntime returns a runtime whose agents are assembled through fn. board
// and identity are optional: nil skips blackboard recording (memory tests,
// host without a board).
func NewRuntime(agents func(memberID string) (AgentAPI, error), board team.BoardStore, boardID string, identity func(memberID string) team.Identity) *Runtime {
	r := &Runtime{
		agents:   agents,
		board:    board,
		boardID:  boardID,
		identity: identity,
		live:     map[team.TaskID]*runEntry{},
		byMember: map[string]team.TaskID{},
	}
	r.inject = func(task team.Task) AssembledContext { return InjectTask(task, nil, "") }
	return r
}

// SetInjector replaces the default context assembly. Hosts that want the
// full §7 chain (durable inbox commands, board view) install their own
// assembly — typically fetching the inbox and folding it in — while the
// runtime keeps owning start/cancel/resume.
func (r *Runtime) SetInjector(fn func(task team.Task) AssembledContext) {
	if fn != nil {
		r.inject = fn
	}
}

// SetTaskStore installs the durable task store. Every state move that passes
// TransitionTask is persisted before its side effect runs (write-before-commit:
// a refused save aborts the start, never an agent half-launched).
func (r *Runtime) SetTaskStore(store team.TaskStore) {
	if store != nil {
		r.store = store
	}
}

// AddWakeup registers a leader-wakeup delivery, called in registration
// order after a task reaches a terminal or attention state.
func (r *Runtime) AddWakeup(fn WakeFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wake = append(r.wake, fn)
}

// Start launches task on member's backend (scheduler.Executor): the state
// move passes team.TransitionTask, the durable store records running before
// the agent is touched, the injected context is submitted, and the blackboard
// records the assignment. A busy member or a failed assembly aborts before
// anything is submitted.
func (r *Runtime) Start(ctx context.Context, task team.Task, member team.Member) error {
	if err := team.TransitionTask(task.Status, team.TaskStatusRunning); err != nil {
		return err
	}
	// The member reservation is taken before any assembly or submit, so a second
	// Start on the same member fails here instead of double-driving the backend.
	r.mu.Lock()
	if _, busy := r.byMember[member.ID]; busy {
		r.mu.Unlock()
		return ErrMemberBusy
	}
	r.byMember[member.ID] = task.ID
	r.mu.Unlock()
	rollback := func() {
		r.mu.Lock()
		if r.byMember[member.ID] == task.ID {
			delete(r.byMember, member.ID)
		}
		r.mu.Unlock()
	}
	api, err := r.agents(member.ID)
	if err != nil {
		rollback()
		return err
	}
	task.Status = team.TaskStatusRunning
	task.AssignedMember = member.ID
	injected := r.inject(task)
	// Write-before-commit holds for the store first; the submit is then the
	// execution gate (§P1): a refused turn lands on failed durably and wakes the
	// leader, never a persisted ghost the board reads as "working".
	if r.store != nil {
		if err := r.store.SaveTask(ctx, task); err != nil {
			rollback()
			return err
		}
	}
	if err := api.SubmitUserTurnOrError(injected.Text, task.Desc); err != nil {
		r.failDispatch(ctx, task, err.Error())
		rollback()
		return err
	}
	r.record(task, "running", "")
	r.mu.Lock()
	r.live[task.ID] = &runEntry{task: task, member: member.ID, api: api}
	r.mu.Unlock()
	return nil
}

// Cancel stops one running task (scheduler.Executor): the durable store
// records canceled before the backend is stopped, the blackboard records the
// cancel, and the leader is woken. Unknown tasks are an error, not a silent
// no-op.
func (r *Runtime) Cancel(taskID team.TaskID) error {
	entry, task, restore, err := r.claimTerminal(taskID, team.TaskStatusCanceled)
	if err != nil {
		return err
	}
	if r.store != nil {
		if err := r.store.SaveTask(context.Background(), task); err != nil {
			restore()
			return err
		}
	}
	entry.api.Cancel()
	r.record(task, "canceled", "")
	r.drop(taskID, entry.member)
	r.wakeAll("task " + string(taskID) + " canceled")
	return nil
}

// claimTerminal moves one live task to a terminal status while holding the
// registry lock, and returns the snapshot to persist. The lock is what makes
// "exactly one of cancel/report wins" real: reading entry.task.Status, checking
// the transition and writing it back outside the lock let two callers both pass
// the check and both write a terminal state (plus a data race on entry.task).
// restore puts the in-memory status back for a failed durable write, or a retry
// would be refused by its own half-applied move.
func (r *Runtime) claimTerminal(taskID team.TaskID, next team.TaskStatus) (*runEntry, team.Task, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.live[taskID]
	if !ok {
		return nil, team.Task{}, nil, ErrTaskUnknown
	}
	previous := entry.task.Status
	if err := team.TransitionTask(previous, next); err != nil {
		return nil, team.Task{}, nil, err
	}
	entry.task.Status = next
	return entry, entry.task, func() {
		r.mu.Lock()
		entry.task.Status = previous
		r.mu.Unlock()
	}, nil
}

// failDispatch settles a task whose backend refused the submitted turn, using
// the two legal edges instead of one illegal write: running -> failed marks the
// delivery that never happened (and is what a crash mid-settle lands on, never a
// ghost running), then failed -> assigned takes the one re-assign edge so the
// task stays live and another member can pick it up. Writing assigned directly
// skipped both edges and, worse, said nothing — the leader kept reading the
// member as "working" on a turn that was never delivered.
func (r *Runtime) failDispatch(ctx context.Context, task team.Task, reason string) {
	if err := team.TransitionTask(task.Status, team.TaskStatusFailed); err != nil {
		return
	}
	task.Status = team.TaskStatusFailed
	if r.store != nil {
		_ = r.store.SaveTask(ctx, task) // best-effort: the refusal itself is the returned error
	}
	r.record(task, "failed", reason)
	if err := team.TransitionTask(task.Status, team.TaskStatusAssigned); err == nil {
		task.Status = team.TaskStatusAssigned
		if r.store != nil {
			_ = r.store.SaveTask(ctx, task)
		}
	}
	r.wakeAll("task " + string(task.ID) + " was refused by its member (" + reason + "); reassign or retry")
}

// Resume re-drives a task that was interrupted (scheduler.Executor, §4
// recovery): the durable store records running before the submission, the
// persisted task is submitted again with a resume marker, and the blackboard
// records the resume.
func (r *Runtime) Resume(ctx context.Context, task team.Task, member team.Member) error {
	if err := team.TransitionTask(task.Status, team.TaskStatusRunning); err != nil {
		return err
	}
	// Same member reservation as Start (§P1): the recovery path must not
	// double-drive a backend that already holds a live task.
	r.mu.Lock()
	if _, busy := r.byMember[member.ID]; busy {
		r.mu.Unlock()
		return ErrMemberBusy
	}
	r.byMember[member.ID] = task.ID
	r.mu.Unlock()
	rollback := func() {
		r.mu.Lock()
		if r.byMember[member.ID] == task.ID {
			delete(r.byMember, member.ID)
		}
		r.mu.Unlock()
	}
	api, err := r.agents(member.ID)
	if err != nil {
		rollback()
		return err
	}
	task.Status = team.TaskStatusRunning
	task.AssignedMember = member.ID
	injected := r.inject(task)
	// Write-before-commit matches Start: the durable store records running
	// before the backend is touched, so a refused save aborts the resume before
	// an agent can half-launch.
	if r.store != nil {
		if err := r.store.SaveTask(ctx, task); err != nil {
			rollback()
			return err
		}
	}
	// Same execution gate as Start: a refused resume must never persist a running
	// task that never ran, so it settles back to assigned and wakes the leader
	// instead of leaving a ghost a third restart re-resumes.
	if err := api.SubmitUserTurnOrError("[resumed]\n"+injected.Text, "[resumed] "+task.Desc); err != nil {
		r.failDispatch(ctx, task, err.Error())
		rollback()
		return err
	}
	r.record(task, "running", "resumed")
	r.mu.Lock()
	r.live[task.ID] = &runEntry{task: task, member: member.ID, api: api}
	r.mu.Unlock()
	return nil
}

// Complete marks a task reported after the member returned its result: the
// state move passes TransitionTask, the durable store records reported, the
// blackboard gets the report event, and the leader is woken. This is the
// report path's single migration point — nothing else may flip a task to
// reported.
func (r *Runtime) Complete(taskID team.TaskID, summary string) error {
	entry, task, restore, err := r.claimTerminal(taskID, team.TaskStatusReported)
	if err != nil {
		return err
	}
	if r.store != nil {
		if err := r.store.SaveTask(context.Background(), task); err != nil {
			restore()
			return err
		}
	}
	r.record(task, "reported", summary)
	r.drop(taskID, entry.member)
	r.wakeAll("task " + string(taskID) + " reported")
	return nil
}

// Drain fetches one inbox batch for a member and acknowledges it only after
// every item was processed (write-before-commit: a mid-batch failure leaves
// the watermark behind, so the failed items replay idempotently). This is
// the durable-inbox consumption loop; hosts call it from their own
// scheduling loop.
func (r *Runtime) Drain(ctx context.Context, inbox *BoardInbox, limit int, fn func(InboxItem) error) (int, error) {
	items, next, err := inbox.Fetch(ctx, -1, limit)
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if err := fn(item); err != nil {
			return 0, err
		}
	}
	if len(items) > 0 {
		if err := inbox.Ack(ctx, next); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

// record appends one task state event to the blackboard. Best-effort: the
// blackboard is observability for the runtime, never its gate.
func (r *Runtime) record(task team.Task, status, detail string) {
	if r.board == nil {
		return
	}
	var id team.Identity
	if r.identity != nil {
		id = r.identity(task.AssignedMember)
	}
	summary := string(task.ID) + " " + status
	if detail != "" {
		summary += ": " + detail
	}
	eventID := "task-" + string(task.ID) + "-" + status + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	_, _ = r.board.Append(context.Background(), team.AppendInput{
		BoardID:     r.boardID,
		EventID:     eventID,
		ClientMsgID: eventID, // the event id doubles as the idempotency key
		Kind:        team.EventAssignment,
		TaskID:      task.ID,
		Summary:     summary,
		Stamped:     id,
	})
}

// LiveTask reports whether the runtime is currently driving taskID. The
// registry entry is written only after the member's backend accepted the turn,
// so this is what separates "the member is working" from "the board has a row" —
// a refused, orphaned or pre-restart task is durable but driven by nothing.
func (r *Runtime) LiveTask(taskID team.TaskID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.live[taskID]
	return ok
}

func (r *Runtime) drop(taskID team.TaskID, member string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, taskID)
	if r.byMember[member] == taskID {
		delete(r.byMember, member)
	}
}

func (r *Runtime) wakeAll(reason string) {
	r.mu.Lock()
	wake := slices.Clone(r.wake)
	r.mu.Unlock()
	for _, fn := range wake {
		_ = fn(reason) // wakeup failure never wedges completion; boardWake stays durable
	}
}
