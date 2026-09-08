package cli

// The leader's recovery surface (§4): retry/cancel/reassign on durable rows
// nothing drives, plus one-shot host restart reattachment.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
	teamscheduler "reasonix/internal/team/scheduler"
)

// reattachAllTeams re-drives every durable live row this host's boards still
// carry after a restart, once per registry build (host recovery, §4). A
// freshly assembled runtime drives nothing, so the assigned/running rows a
// previous runtime left are dead until reattached here: each team's own rows
// are resumed on their member (or settled durably when the member left the
// fleet), and a row is never resumed twice — the runtime's live registry is
// the once-guard. Teams sharing one board are each reattached through their
// own per-team service, and a row claimed by one team is skipped by the next.
func (s *teamTaskService) reattachAllTeams(ctx context.Context) (string, error) {
	if s == nil || s.teamStore == nil || s.board == nil {
		return "", nil
	}
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return "", err
	}
	var notes []string
	claimed := map[team.TaskID]bool{}
	for _, t := range doc.Teams {
		svc := s.forTeam(t.Name)
		note, err := svc.reattachLive(ctx, claimed)
		if err != nil {
			return "", err
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	return strings.Join(notes, "; "), nil
}

// reattachLive re-drives the caller's durable live rows that belong to this
// team, marking each claimed so a sibling team on the same board never touches
// it. A row whose member is still in the fleet resumes on that member; a row
// whose member is gone settles through the scheduler's migration map. Rows
// this runtime already drives are skipped — a second call is a no-op, which is
// what makes "exactly once per registry build" hold without a flag.
func (s *teamTaskService) reattachLive(ctx context.Context, claimed map[team.TaskID]bool) (string, error) {
	if err := s.ready(); err != nil {
		return "", nil
	}
	live, err := s.board.LoadLiveTasks(ctx)
	if err != nil {
		return "", err
	}
	owns := s.teamMemberIDs()
	if len(owns) == 0 {
		return "", nil
	}
	pending := make([]team.Task, 0, len(live))
	for _, task := range live {
		if claimed[task.ID] || !owns[task.AssignedMember] || s.driving(task.ID) {
			continue
		}
		claimed[task.ID] = true
		pending = append(pending, task)
	}
	if len(pending) == 0 {
		return "", nil
	}
	fleet, err := s.fleet()
	if err != nil {
		return "", err
	}
	restored, err := s.scheduler.Restore(pending, fleet)
	if err != nil {
		return "", err
	}
	return s.summarizeRestored(restored), nil
}

// teamMemberIDs maps every template member id of this team — active, disabled
// and archived alike — so reattachment owns exactly the rows this team created
// and leaves other teams' rows to them. A member who left the fleet keeps its
// rows owned here so the scheduler can settle them rather than a sibling team
// cancelling them.
func (s *teamTaskService) teamMemberIDs() map[string]bool {
	if s == nil || s.teamStore == nil || strings.TrimSpace(s.teamName) == "" {
		return nil
	}
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return nil
	}
	for _, t := range doc.Teams {
		if t.Name != s.teamName {
			continue
		}
		ids := make(map[string]bool, len(t.Template))
		for _, slot := range t.Template {
			ids[slot.MemberID] = true
		}
		return ids
	}
	return nil
}

// requireTeamRow refuses a leader redrive whose durable row belongs to another
// team: on a board several teams share, the member set decides ownership
// exactly as reattachment claims it, so a leader can never retry, cancel, or
// reassign a sibling team's task. Rows of a member who left the active fleet
// stay owned — the id is still in the template — so disabled and archived rows
// keep their own leader's redrive.
func (s *teamTaskService) requireTeamRow(row team.Task) error {
	if s.teamMemberIDs()[row.AssignedMember] {
		return nil
	}
	return fmt.Errorf("task %s is not owned by team %q (recorded member %q): only the owning team's leader may retry, cancel, or reassign it", row.ID, s.teamName, row.AssignedMember)
}

// summarizeRestored renders the reattachment outcome for the leader notice:
// resumed rows name their member, settled rows say why.
func (s *teamTaskService) summarizeRestored(restored []teamscheduler.Assignment) string {
	if len(restored) == 0 {
		return ""
	}
	var resumed, settled []string
	for _, a := range restored {
		switch a.Status {
		case teamscheduler.StatusRunning:
			resumed = append(resumed, string(a.TaskID)+" on "+a.MemberID)
		default:
			settled = append(settled, string(a.TaskID)+": "+a.Note)
		}
	}
	var b strings.Builder
	if len(resumed) > 0 {
		fmt.Fprintf(&b, "resumed %d interrupted task(s): %s", len(resumed), strings.Join(resumed, ", "))
	}
	if len(settled) > 0 {
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "settled %d: %s", len(settled), strings.Join(settled, "; "))
	}
	return b.String()
}

// retryTask re-drives one durable live task the runtime no longer executes
// (leader retry after a refused dispatch, a dead runtime, or a restore that
// failed). The row keeps its TaskID; the scheduler's prior-task affinity
// hands it back to the recorded member when that member is still assignable,
// otherwise to another idle member of the task's role. A failed row first
// settles through the one re-assign edge (failed -> assigned) so no path
// invents an illegal transition; a task the runtime is already executing is
// refused — a live turn must never be submitted twice.
func (s *teamTaskService) retryTask(ctx context.Context, taskID string) (string, error) {
	row, err := s.redriveTarget(ctx, taskID)
	if err != nil {
		return "", err
	}
	if row.Status == team.TaskStatusFailed {
		if err := team.TransitionTask(row.Status, team.TaskStatusAssigned); err != nil {
			return "", fmt.Errorf("retry %s: %w", row.ID, err)
		}
		row.Status = team.TaskStatusAssigned
		if err := s.board.SaveTask(ctx, row); err != nil {
			return "", fmt.Errorf("retry %s: %w", row.ID, err)
		}
	}
	fleet, err := s.fleet()
	if err != nil {
		return "", err
	}
	assignment, err := s.scheduler.Assign(row, fleet)
	if err != nil {
		return "", s.leaderRedriveError("retry", row, err)
	}
	return fmt.Sprintf("task %s re-started on %s (status=%s)", assignment.TaskID, assignment.MemberID, assignment.Status), nil
}

// reassignTask re-points one durable live task to a named member and re-drives
// it, keeping the same TaskID (leader reassign when the recorded member left
// the fleet or is busy). Refused up front: a task the runtime is executing or
// a member outside the fleet — the row keeps its recorded state so the leader
// can retry once the target is fixed. A running row that nothing drives
// settles through the migration map (running -> failed -> assigned) before the
// re-point, so no path invents an illegal transition.
func (s *teamTaskService) reassignTask(ctx context.Context, taskID, memberID string) (string, error) {
	row, err := s.redriveTarget(ctx, taskID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(memberID) == "" {
		return "", fmt.Errorf("reassign %s: member_name is required", row.ID)
	}
	target, err := s.assignableMember(row, strings.TrimSpace(memberID))
	if err != nil {
		return "", err
	}
	if row.Status == team.TaskStatusRunning {
		settled := row
		if err := team.TransitionTask(settled.Status, team.TaskStatusFailed); err != nil {
			return "", fmt.Errorf("reassign %s: %w", row.ID, err)
		}
		settled.Status = team.TaskStatusFailed
		if err := s.board.SaveTask(ctx, settled); err != nil {
			return "", fmt.Errorf("reassign %s: %w", row.ID, err)
		}
		row = settled
	}
	if err := team.TransitionTask(row.Status, team.TaskStatusAssigned); err != nil {
		return "", fmt.Errorf("reassign %s: %w", row.ID, err)
	}
	row.Status = team.TaskStatusAssigned
	row.AssignedMember = target.ID
	// The role requirement follows the row to its new owner: the scheduler's
	// pick gate matches RequireRole, so a frozen requirement would refuse every
	// later retry on the member the leader actually chose.
	if target.Role != "" {
		row.RequireRole = target.Role
	}
	if err := s.board.SaveTask(ctx, row); err != nil {
		return "", fmt.Errorf("reassign %s: %w", row.ID, err)
	}
	assignment, err := s.scheduler.Assign(row, []team.Member{{ID: target.ID, Role: target.Role}})
	if err != nil {
		return "", s.leaderRedriveError("reassign", row, err)
	}
	return fmt.Sprintf("task %s reassigned to %s (status=%s)", assignment.TaskID, assignment.MemberID, assignment.Status), nil
}

// cancelTask closes one durable task the leader wants gone. A task the runtime
// is executing cancels through the runtime (the backend turn is stopped); a
// row nothing drives — an orphan after a dead runtime or a refused dispatch —
// cancels durably without touching any live task. Terminal rows refuse with
// their recorded state; the row is never deleted.
func (s *teamTaskService) cancelTask(ctx context.Context, taskID string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	id := team.TaskID(strings.TrimSpace(taskID))
	row, err := s.board.LoadTask(ctx, id)
	if err != nil {
		if errors.Is(err, team.ErrTaskNotFound) {
			return "", fmt.Errorf("task %q does not exist", taskID)
		}
		return "", err
	}
	if err := s.requireTeamRow(row); err != nil {
		return "", err
	}
	switch row.Status {
	case team.TaskStatusAssigned, team.TaskStatusRunning:
	default:
		return "", fmt.Errorf("task %s is already closed (recorded %s)", row.ID, row.Status)
	}
	if s.driving(row.ID) {
		if err := s.runtime.Cancel(row.ID); err != nil {
			return "", s.translateCancelError(row, err)
		}
		return fmt.Sprintf("task %s canceled", row.ID), nil
	}
	if err := s.runtime.CancelTask(ctx, row); err != nil {
		return "", err
	}
	return fmt.Sprintf("task %s canceled", row.ID), nil
}

// translateCancelError turns a refused driven cancel into an actionable
// answer: the only refusal reachable from a driven row is the registry entry
// vanishing between the driving check and claimTerminal — the same TOCTOU the
// report path translates — so the durable row decides the message.
func (s *teamTaskService) translateCancelError(row team.Task, err error) error {
	if !errors.Is(err, agentruntime.ErrTaskUnknown) {
		return err
	}
	return fmt.Errorf("task %s changed state while this cancel was in flight: a concurrent close won — no change was made", row.ID)
}

// redriveTarget loads and validates the durable row retry and reassign act
// on: it must exist, be live (assigned, running, or failed — the states a
// dead runtime or a refused dispatch leave behind), and not be executing.
// Unknown ids and terminal rows refuse by name, never a raw runtime error.
func (s *teamTaskService) redriveTarget(ctx context.Context, taskID string) (team.Task, error) {
	if err := s.ready(); err != nil {
		return team.Task{}, err
	}
	id := team.TaskID(strings.TrimSpace(taskID))
	row, err := s.board.LoadTask(ctx, id)
	if err != nil {
		if errors.Is(err, team.ErrTaskNotFound) {
			return team.Task{}, fmt.Errorf("task %q does not exist", taskID)
		}
		return team.Task{}, err
	}
	if err := s.requireTeamRow(row); err != nil {
		return team.Task{}, err
	}
	switch row.Status {
	case team.TaskStatusAssigned, team.TaskStatusRunning, team.TaskStatusFailed:
	default:
		return team.Task{}, fmt.Errorf("task %s is already closed (recorded %s)", row.ID, row.Status)
	}
	if s.driving(row.ID) {
		return team.Task{}, fmt.Errorf("task %s is already executing: let it finish or cancel it first", row.ID)
	}
	return row, nil
}

// leaderRedriveError renders a refused re-drive (retry or reassign) for the
// leader. The runtime refused the start after the row was durably re-pointed,
// so the failure is the dispatch's: the row stays live and retryable, and the
// scheduler's ErrStartFailed is the one sentinel worth naming.
func (s *teamTaskService) leaderRedriveError(verb string, row team.Task, err error) error {
	if errors.Is(err, teamscheduler.ErrStartFailed) {
		return fmt.Errorf("%s %s: the member backend refused the turn; the task stays assigned — fix the member or retry", verb, row.ID)
	}
	return fmt.Errorf("%s %s: %w", verb, row.ID, err)
}

// assignableMember validates a reassign target against the durable row: the
// member must be an active non-leader slot of this team. The leader's explicit
// choice is authoritative over the row's old role requirement — the role
// follows the row to its new owner (see reassignTask).
func (s *teamTaskService) assignableMember(row team.Task, memberID string) (team.Member, error) {
	fleet, err := s.fleet()
	if err != nil {
		return team.Member{}, err
	}
	for _, m := range fleet {
		if m.ID != memberID {
			continue
		}
		return m, nil
	}
	return team.Member{}, fmt.Errorf("reassign %s: member %q is not an assignable member of team %q", row.ID, memberID, s.teamName)
}
