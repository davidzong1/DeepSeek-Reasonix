package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// The two shared-conclusion tools. They are deliberately not branches on
// teamTaskTool: that tool is the task-orchestration pass-through, and a
// conclusion is neither task state nor a report. Keeping them in their own file
// keeps the task surface's ReadOnly/EffectHint logic reading one concern.
const (
	postConclusionName  = "member_post_conclusion"
	readConclusionsName = "leader_read_conclusions"
)

// conclusionTool is one tool of the shared-conclusion surface, bound to the team
// and member identity of the session it was assembled for.
type conclusionTool struct {
	name     string
	desc     string
	schema   json.RawMessage
	service  *teamTaskService
	teamName string
	memberID string
	// publish separates the member's write from the leader's read, so a
	// read-only or plan turn keeps the read and still cannot post.
	publish bool
}

func (t *conclusionTool) Name() string            { return t.name }
func (t *conclusionTool) Description() string     { return t.desc }
func (t *conclusionTool) Schema() json.RawMessage { return t.schema }

// ReadOnly and PlanModeSafe separate the leader's read from the member's post.
func (t *conclusionTool) ReadOnly() bool     { return !t.publish }
func (t *conclusionTool) PlanModeSafe() bool { return !t.publish }

// TeamLifecycleStateWriter: a conclusion lands on the shared board, never in the
// user's workspace, so the read-only and plan boundaries must not strand a
// member's finding — the same reachability the report and task surfaces keep.
func (t *conclusionTool) TeamLifecycleStateWriter() bool { return t.publish }

func (t *conclusionTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.service == nil || t.service.board == nil {
		return "", fmt.Errorf("%s: the shared board is unavailable (open the team session with its board store)", t.name)
	}
	if t.publish {
		return t.post(ctx, args)
	}
	return t.read(ctx)
}

// post writes one conclusion. The task id is resolved here rather than taken
// from the caller: the tool is bound to one member, and a model-supplied task id
// could attribute a finding to work this member never held.
func (t *conclusionTool) post(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Topic   string `json:"topic"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", t.name, err)
	}
	conclusion, err := team.PostConclusion(ctx, t.service.board,
		conclusionIdentity(t.service, t.memberID), currentTaskID(ctx, t.service, t.memberID),
		p.Topic, p.Summary)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("conclusion %q stored on the shared board", conclusion.Topic), nil
}

// read renders the board's current conclusions. The header differs from the
// member delta's on purpose: a model that sees "[board delta]" knows the text
// arrived on its own, while this one it asked for.
func (t *conclusionTool) read(ctx context.Context) (string, error) {
	conclusions, err := team.ListConclusions(ctx, t.service.board,
		conclusionIdentity(t.service, t.memberID), 0)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(conclusions)+1)
	lines = append(lines, "[board conclusions]")
	if len(conclusions) == 0 {
		return strings.Join(append(lines, "(none)"), "\n"), nil
	}
	for _, c := range conclusions {
		lines = append(lines, team.ConclusionLine(c.MemberID, c.Topic, c.Summary))
	}
	return strings.Join(lines, "\n"), nil
}

// currentTaskID is the member's unfinished task, or an empty id when it holds
// none: a conclusion is still worth recording between tasks, and refusing the
// post would make a member drop the finding instead of attaching it to whatever
// comes next.
func currentTaskID(ctx context.Context, s *teamTaskService, memberID string) team.TaskID {
	if s == nil || s.board == nil {
		return ""
	}
	tasks, err := s.board.LoadLiveTasks(ctx)
	if err != nil {
		return ""
	}
	for _, task := range tasks {
		if task.AssignedMember == memberID {
			return task.ID
		}
	}
	return ""
}

// conclusionIdentity resolves the identity a conclusion is stamped with: the
// member id the tool is bound to, plus the role and launch type the durable
// binding records. A member the team document no longer names still posts — its
// id is what other members attribute the finding to — with the rest left empty.
func conclusionIdentity(s *teamTaskService, memberID string) team.Identity {
	id := team.Identity{MemberID: memberID}
	if s == nil || s.teamStore == nil {
		return id
	}
	binding, err := s.teamStore.Binding(s.teamName, memberID)
	if err != nil {
		return id
	}
	id.Role, id.Agent = string(binding.Role), binding.AgentType
	return id
}

// newMemberConclusionTool assembles the member half: posting under its own
// identity. The leader's read is not here — a member reads other members'
// conclusions through the delta its own thinking step carries.
func newMemberConclusionTool(service *teamTaskService, teamName, memberID string) tool.Tool {
	return &conclusionTool{
		name: postConclusionName,
		desc: "Post one short conclusion that can change the main task. It is not a status report and does not finish the task.",
		schema: json.RawMessage(`{"type":"object","properties":{` +
			`"topic":{"type":"string","description":"Short topic; at most 48 characters."},` +
			`"summary":{"type":"string","description":"One sentence, at most 160 characters. Longer text is refused; publish a deliverable and quote its id."}},` +
			`"required":["topic","summary"],"additionalProperties":false}`),
		service: service, teamName: teamName, memberID: memberID, publish: true,
	}
}

// newLeaderConclusionTool assembles the leader half: the read, and nothing that
// posts. A leader never writes a member's conclusion, and no automatic path puts
// one in its context — only this call does.
func newLeaderConclusionTool(service *teamTaskService, teamName, memberID string) tool.Tool {
	return &conclusionTool{
		name:    readConclusionsName,
		desc:    "Read the current shared conclusions. They are not delivered to this leader unless this tool is called.",
		schema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		service: service, teamName: teamName, memberID: memberID,
	}
}
