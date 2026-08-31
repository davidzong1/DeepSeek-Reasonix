package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
	teamscheduler "reasonix/internal/team/scheduler"
)

// teamTaskService is the single host-side bridge between Agent tools and the
// durable team runtime. The service is created per opened team overlay; the
// backend lookup remains lazy so binding a leader never recursively assembles
// every member.
type teamTaskService struct {
	teamStore *team.TeamStore
	board     *team.SQLiteStore
	teamName  string
	bind      func(team.MemberBinding) (control.SessionAPI, error)
	runtime   *agentruntime.Runtime
	scheduler *teamscheduler.RuntimeScheduler
	seq       atomic.Uint64
	cacheMu   sync.Mutex
	teams     map[string]*teamTaskService
	// wakeMu guards the late-binding deliverable upon which wakeup delivery
	// may race (the report path is a member goroutine).
	wakeMu sync.Mutex
	onWake []agentruntime.WakeFunc
}

// wakeLeader delivers one leader wakeup into the durable board wake stream.
// The stamp is resolved per wake to the team's current leader member id (see
// leaderIdentity) — the identity the TUI's consumeWakeups(leader) cursor
// selects — never the team name, which a leader id need not equal.
func (s *teamTaskService) wakeLeader(reason string) error {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	for _, fn := range s.onWake {
		_ = fn(reason)
	}
	return nil
}

// leaderIdentity resolves the team's current leader slot to the identity a
// wakeup must be stamped with: the leader member id, which is what the TUI's
// consumeWakeups(leader) filter and cursor select. The store is re-read at
// every wake so a leader change re-targets delivery without rebuilding the
// service. An empty result (no leader) makes the wake a no-op — there is no
// one to wake, and an anonymous append would be forbidden by the board.
func (s *teamTaskService) leaderIdentity() team.Identity {
	if s == nil || s.teamStore == nil {
		return team.Identity{}
	}
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return team.Identity{}
	}
	for _, t := range doc.Teams {
		if t.Name != s.teamName {
			continue
		}
		for _, slot := range t.Template {
			if slot.IsLeader() {
				return team.Identity{MemberID: slot.MemberID, Role: string(slot.Role), Generation: 1}
			}
		}
	}
	return team.Identity{}
}

func newTeamTaskService(store *team.TeamStore, board *team.SQLiteStore, teamName string, bind func(team.MemberBinding) (control.SessionAPI, error)) *teamTaskService {
	s := &teamTaskService{teamStore: store, board: board, teamName: strings.TrimSpace(teamName), bind: bind, teams: make(map[string]*teamTaskService)}
	if board != nil && store != nil && s.teamName != "" {
		s.runtime = agentruntime.NewRuntime(func(memberID string) (agentruntime.AgentAPI, error) {
			binding, err := store.Binding(s.teamName, memberID)
			if err != nil {
				return nil, err
			}
			if s.bind == nil {
				return nil, fmt.Errorf("team task runtime: member backend unavailable")
			}
			return s.bind(binding)
		}, board, team.BoardShared, func(memberID string) team.Identity {
			binding, err := store.Binding(s.teamName, memberID)
			if err != nil {
				return team.Identity{MemberID: memberID}
			}
			return team.Identity{MemberID: memberID, Role: string(binding.Role), Agent: binding.AgentType}
		})
		s.runtime.SetTaskStore(board)
		s.onWake = []agentruntime.WakeFunc{
			agentruntime.NewBoardWakeFor(board, team.BoardShared, s.leaderIdentity),
		}
		s.runtime.AddWakeup(s.wakeLeader)
		s.scheduler = teamscheduler.NewRuntimeScheduler(s.runtime)
		s.scheduler.SetTaskStore(board)
		s.teams[s.teamName] = s
	}
	return s
}

func (s *teamTaskService) forTeam(teamName string) *teamTaskService {
	if s == nil {
		return nil
	}
	teamName = strings.TrimSpace(teamName)
	if teamName == "" || teamName == s.teamName {
		return s
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if cached := s.teams[teamName]; cached != nil {
		return cached
	}
	child := newTeamTaskService(s.teamStore, s.board, teamName, s.bind)
	s.teams[teamName] = child
	return child
}

func (s *teamTaskService) ready() error {
	if s == nil || s.teamStore == nil || s.board == nil || s.runtime == nil || s.scheduler == nil {
		return fmt.Errorf("team task runtime is unavailable (open the team session with its board store)")
	}
	return nil
}

func (s *teamTaskService) fleet() ([]team.Member, error) {
	if s == nil || s.teamStore == nil {
		return nil, fmt.Errorf("team task runtime: team store unavailable")
	}
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return nil, err
	}
	for _, t := range doc.Teams {
		if t.Name != s.teamName {
			continue
		}
		busy := s.busyMembers()
		fleet := make([]team.Member, 0, len(t.Template))
		for _, slot := range t.Template {
			if slot.Status == team.MemberStatusArchived || slot.Status == team.MemberStatusDisabled || slot.IsLeader() {
				continue
			}
			member := team.Member{ID: slot.MemberID, Role: slot.Role, State: team.MemberStateIdle}
			if ref, working := busy[slot.MemberID]; working {
				member.State, member.TaskRef = team.MemberStateWorking, ref
			}
			fleet = append(fleet, member)
		}
		return fleet, nil
	}
	return nil, team.ErrTeamNotFound
}

// busyMembers maps each member the runtime is actually driving to its task. The
// fleet reported every member as idle with no TaskRef, so the scheduler's
// idle-before-busy branch never ran and a role could be dispatched onto the one
// member already working. Only a driven task counts as busy — a queued row is
// not work in progress, the same distinction memberTaskState draws.
func (s *teamTaskService) busyMembers() map[string]team.TaskID {
	busy := map[string]team.TaskID{}
	if s == nil || s.board == nil {
		return busy
	}
	tasks, err := s.board.LoadLiveTasks(context.Background())
	if err != nil {
		return busy
	}
	for _, task := range tasks {
		if s.driving(task.ID) {
			busy[task.AssignedMember] = task.ID
		}
	}
	return busy
}

func (s *teamTaskService) listTeam() (string, error) {
	if s == nil || s.teamStore == nil {
		return "", fmt.Errorf("team task runtime: team store unavailable")
	}
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return "", err
	}
	for _, t := range doc.Teams {
		if t.Name != s.teamName {
			continue
		}
		lines := []string{fmt.Sprintf("team %q members:", s.teamName)}
		for _, slot := range t.Template {
			role := string(slot.Role)
			if role == "" {
				role = "unconfigured"
			}
			state := string(slot.Status)
			if state == "" {
				state = string(team.MemberStatusActive)
			}
			marker := "member"
			if slot.IsLeader() {
				marker = "leader (do not assign)"
			}
			lines = append(lines, fmt.Sprintf("- %s: role=%s status=%s [%s]", slot.MemberID, role, state, marker))
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", team.ErrTeamNotFound
}

func (s *teamTaskService) selectMembers(task, requiredRoles string) ([]string, []string, error) {
	fleet, err := s.fleet()
	if err != nil {
		return nil, nil, err
	}
	roles := parseRoles(requiredRoles)
	if len(roles) == 0 {
		roles = inferRoles(task)
	}
	selected := make([]string, 0, len(fleet))
	for _, member := range fleet {
		if len(roles) == 0 || containsRole(roles, member.Role) {
			selected = append(selected, member.ID)
		}
	}
	return selected, roles, nil
}

func (s *teamTaskService) assignSubtask(ctx context.Context, memberID, subtask, contextText string) (teamscheduler.Assignment, error) {
	if err := s.ready(); err != nil {
		return teamscheduler.Assignment{}, err
	}
	memberID, subtask = strings.TrimSpace(memberID), strings.TrimSpace(subtask)
	if memberID == "" || subtask == "" {
		return teamscheduler.Assignment{}, fmt.Errorf("member_id and subtask are required")
	}
	binding, err := s.teamStore.Binding(s.teamName, memberID)
	if err != nil {
		return teamscheduler.Assignment{}, err
	}
	if binding.Leader {
		return teamscheduler.Assignment{}, fmt.Errorf("member %q is the leader and cannot receive a subtask", memberID)
	}
	// pick() only honours the requested member when it is in the fleet, so an
	// archived or disabled target would silently start the work on a same-role
	// sibling. Refuse before anything is written.
	fleet, err := s.fleet()
	if err != nil {
		return teamscheduler.Assignment{}, err
	}
	if !fleetContains(fleet, memberID) {
		return teamscheduler.Assignment{}, fmt.Errorf("member %q is not an assignable member of team %q (archived, disabled, or the leader)", memberID, s.teamName)
	}
	id := team.TaskID(fmt.Sprintf("%s-%d-%d", s.teamName, time.Now().UnixNano(), s.seq.Add(1)))
	task := team.Task{ID: id, RequireRole: binding.Role, Desc: subtask, ContextRef: strings.TrimSpace(contextText), Status: team.TaskStatusCreated, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), AssignedMember: memberID}
	if err := team.TransitionTask(task.Status, team.TaskStatusAssigned); err != nil {
		return teamscheduler.Assignment{}, err
	}
	task.Status = team.TaskStatusAssigned
	if err := s.board.SaveTask(ctx, task); err != nil {
		return teamscheduler.Assignment{}, err
	}
	assignment, err := s.scheduler.Assign(task, fleet)
	if err != nil {
		// The row stays live on purpose: assigned is re-dispatchable. The defect was
		// the reporting, not the state — memberTaskState renders a row nothing
		// drives as queued, never as a member "working".
		return teamscheduler.Assignment{}, err
	}
	return assignment, nil
}

func fleetContains(fleet []team.Member, memberID string) bool {
	for _, member := range fleet {
		if member.ID == memberID {
			return true
		}
	}
	return false
}

func (s *teamTaskService) memberTask(memberID string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	tasks, err := s.board.LoadLiveTasks(context.Background())
	if err != nil {
		return "", err
	}
	var lines []string
	for _, task := range tasks {
		if task.AssignedMember == memberID {
			lines = append(lines, fmt.Sprintf("task=%s status=%s role=%s description=%s", task.ID, task.Status, task.RequireRole, task.Desc))
		}
	}
	if len(lines) == 0 {
		return fmt.Sprintf("member %q has no unfinished task", memberID), nil
	}
	return strings.Join(lines, "\n"), nil
}

func (s *teamTaskService) checkStatus(memberID string) (string, error) {
	if s == nil || s.teamStore == nil || s.board == nil {
		return "", fmt.Errorf("team task runtime is unavailable")
	}
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return "", err
	}
	tasks, err := s.board.LoadLiveTasks(context.Background())
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

// driving asks the runtime whether anything is actually executing the task, so
// the leader's status read is not just the durable row echoed back.
func (s *teamTaskService) driving(id team.TaskID) bool {
	return s != nil && s.runtime != nil && s.runtime.LiveTask(id)
}

// memberTaskState describes one member's task honestly. The durable row alone
// cannot say whether anything is driving it, and rendering every live row as
// "working" is what made a refused or orphaned dispatch look like a member busy
// thinking — the leader then waited on a member that had never been handed
// anything. driving is the runtime's own registry, written only once the
// member's backend accepted the turn.
func memberTaskState(task team.Task, driving bool) string {
	switch {
	case driving:
		return fmt.Sprintf("working task=%s status=%s", task.ID, task.Status)
	case task.Status == team.TaskStatusRunning:
		return fmt.Sprintf("stalled task=%s (recorded running, nothing driving it — reassign or retry)", task.ID)
	default:
		return fmt.Sprintf("queued task=%s status=%s (not dispatched, nothing driving it)", task.ID, task.Status)
	}
}

// report closes one of the member's live tasks. taskID disambiguates: with more
// than one task owned, picking "the first one LoadLiveTasks returned" could
// close a task the member never worked on, so an ambiguous report is refused
// with the choices instead of guessing.
func (s *teamTaskService) report(memberID, taskID, result string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	if strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("result is required")
	}
	tasks, err := s.board.LoadLiveTasks(context.Background())
	if err != nil {
		return "", err
	}
	owned := make([]team.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.AssignedMember == memberID {
			owned = append(owned, task)
		}
	}
	target, err := pickReportTarget(memberID, strings.TrimSpace(taskID), owned, s.driving)
	if err != nil {
		return "", err
	}
	if target == nil {
		return fmt.Sprintf("member %q has no unfinished task to report", memberID), nil
	}
	if err := s.runtime.Complete(target.ID, strings.TrimSpace(result)); err != nil {
		return "", err
	}
	return fmt.Sprintf("task %s reported to leader", target.ID), nil
}

// pickReportTarget resolves which of a member's live tasks a report closes. The
// runtime drives at most one task per member, so the driving one is what the
// member actually executed — that is the answer whenever a queued or
// undispatched row would otherwise make the choice ambiguous. Only a genuinely
// undecidable set is refused, with the ids, rather than closing whichever task
// LoadLiveTasks happened to return first. A nil task with a nil error means the
// member owns none.
func pickReportTarget(memberID, taskID string, owned []team.Task, driving func(team.TaskID) bool) (*team.Task, error) {
	if taskID != "" {
		for i := range owned {
			if string(owned[i].ID) == taskID {
				return &owned[i], nil
			}
		}
		return nil, fmt.Errorf("task %q is not an unfinished task of member %q", taskID, memberID)
	}
	if len(owned) == 0 {
		return nil, nil
	}
	if driving != nil {
		var live []*team.Task
		for i := range owned {
			if driving(owned[i].ID) {
				live = append(live, &owned[i])
			}
		}
		if len(live) == 1 {
			return live[0], nil
		}
	}
	if len(owned) == 1 {
		return &owned[0], nil
	}
	ids := make([]string, 0, len(owned))
	for _, task := range owned {
		ids = append(ids, string(task.ID))
	}
	return nil, fmt.Errorf("member %q has %d unfinished tasks (%s) and none is currently running: pass task_id to say which one this report closes",
		memberID, len(owned), strings.Join(ids, ", "))
}

func parseRoles(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		role := strings.TrimSpace(strings.ToLower(part))
		if role != "" && !seen[role] {
			seen[role] = true
			out = append(out, role)
		}
	}
	return out
}

func inferRoles(task string) []string {
	t := strings.ToLower(task)
	roles := make([]string, 0, 1)
	for _, pair := range []struct {
		role string
		keys []string
	}{
		{string(team.RoleTester), []string{"test", "测试", "验证"}},
		{string(team.RoleReviewer), []string{"review", "审查", "评审"}},
		{string(team.RoleArchitectureAnalyst), []string{"architect", "architecture", "design", "架构", "设计"}},
		{string(team.RolePluginEngineer), []string{"plugin", "mcp", "插件"}},
		{string(team.RoleCoder), []string{"code", "implement", "fix", "实现", "修复", "编码"}},
	} {
		for _, key := range pair.keys {
			if strings.Contains(t, key) {
				roles = append(roles, pair.role)
				break
			}
		}
	}
	return roles
}

func containsRole(roles []string, role team.RoleID) bool {
	for _, want := range roles {
		if strings.EqualFold(want, string(role)) {
			return true
		}
	}
	return false
}
