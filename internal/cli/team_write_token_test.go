package cli

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
)

func TestMemberWriteIntentGateQueuesTeamPeers(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	intent := agent.WriteIntent{Paths: []string{target}, Label: "internal/shared.go"}

	leader := memberWriteIntentGate("alpha", root, "lead")
	follower := memberWriteIntentGate("alpha", root, "m1")
	if leader == nil || follower == nil {
		t.Fatal("a team with a workspace root must hand its members a gate")
	}
	releaseHeld, err := leader(context.Background(), intent, func(wait agent.WriteIntentWait) {
		t.Fatalf("the first member must not queue behind anyone: %+v", wait)
	})
	if err != nil {
		t.Fatal(err)
	}

	waits := make(chan agent.WriteIntentWait, 1)
	admitted := make(chan func(), 1)
	go func() {
		release, err := follower(context.Background(), intent, func(wait agent.WriteIntentWait) {
			waits <- wait
		})
		if err != nil {
			return
		}
		admitted <- release
	}()

	select {
	case wait := <-waits:
		if !strings.Contains(wait.Holder, "member lead") || !strings.Contains(wait.Holder, "team alpha") {
			t.Fatalf("wait holder = %q, want the holding teammate named", wait.Holder)
		}
		if wait.Scope != "internal/shared.go" {
			t.Fatalf("wait scope = %q, want the file it wants to write", wait.Scope)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a peer writing the same file never queued on the team token")
	}

	releaseHeld()
	select {
	case release := <-admitted:
		release()
	case <-time.After(5 * time.Second):
		t.Fatal("the queued peer was never admitted after its teammate released")
	}
}

func TestMemberWriteIntentGateScopesTheTokenToTeamAndWorkspace(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()

	// One team in one workspace is one token, however many services assemble it.
	shared, again := teamWriteIntentToken("alpha", first), teamWriteIntentToken("alpha", first)
	if shared != again {
		t.Fatal("the same team in the same workspace must share one token")
	}
	if teamWriteIntentToken("alpha", first) == teamWriteIntentToken("beta", first) {
		t.Fatal("two teams over one workspace are not each other's peers")
	}
	if teamWriteIntentToken("alpha", first) == teamWriteIntentToken("alpha", second) {
		t.Fatal("one team's two workspaces are not each other's peers")
	}

	intent := agent.WriteIntent{Paths: []string{filepath.Join(first, "internal", "shared.go")}, Label: "internal/shared.go"}
	releaseHeld, err := memberWriteIntentGate("alpha", first, "m1")(context.Background(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseHeld()
	// Another team's member writing the same path must not queue: only the
	// token's own peers are queued behind each other.
	other, err := memberWriteIntentGate("beta", first, "m2")(context.Background(), intent, func(wait agent.WriteIntentWait) {
		t.Fatalf("a member of another team queued on alpha's token: %+v", wait)
	})
	if err != nil {
		t.Fatal(err)
	}
	other()
}

func TestMemberWriteIntentGateWithoutAWorkspaceRootKeepsTheOldBehavior(t *testing.T) {
	if gate := memberWriteIntentGate("alpha", "", "m1"); gate != nil {
		t.Fatal("a team with no workspace root has no token to queue on")
	}
	if gate := memberWriteIntentGate("", t.TempDir(), "m1"); gate != nil {
		t.Fatal("an unnamed team has no recognized peer set")
	}
}

func TestMemberWriteLabelNamesAMemberOnlyWhenItCan(t *testing.T) {
	for _, tc := range []struct {
		member, team, want string
	}{
		{member: "m1", team: "alpha", want: "member m1 of team alpha"},
		{member: "m1", want: "member m1"},
		{team: "alpha", want: ""},
		{member: "  ", team: " alpha ", want: ""},
	} {
		if got := memberWriteLabel(tc.member, tc.team); got != tc.want {
			t.Fatalf("memberWriteLabel(%q, %q) = %q, want %q", tc.member, tc.team, got, tc.want)
		}
	}
}

func TestSubtaskWriteAreasReadsPathsOutOfProse(t *testing.T) {
	subtask := "Fix the writer in `internal/team/role.go`; update docs/team-mcp-port/ROUTE.md and " +
		"internal/agent/tool_write_coordination.go. Do not touch /etc/hosts, C:\\temp\\x.go, " +
		"https://example.com/a.go, *.go, $HOME/a.go or internal/team/role.go again. Also check the test."
	want := []string{
		"docs/team-mcp-port/ROUTE.md",
		"internal/agent/tool_write_coordination.go",
		"internal/team/role.go",
	}
	got := subtaskWriteAreas(subtask)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("areas = %v, want %v", got, want)
	}
	if areas := subtaskWriteAreas("make the widget faster"); len(areas) != 0 {
		t.Fatalf("prose areas = %v, want none", areas)
	}
}

func TestFanoutWriteAreasReportsOnlyWhenItKnowsSomething(t *testing.T) {
	if report := fanoutWriteAreas("fix internal/team/role.go", 1); report != "" {
		t.Fatalf("single-member dispatch report = %q, want nothing to warn about", report)
	}
	if report := fanoutWriteAreas("make the widget faster", 3); report != "" {
		t.Fatalf("path-free subtask report = %q, want no invented paths", report)
	}

	report := fanoutWriteAreas("fix internal/team/role.go", 3)
	for _, want := range []string{"internal/team/role.go", "team write token"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report %q must mention %q", report, want)
		}
	}

	long := fanoutWriteAreas("touch a/b.go c/d.go e/f.go g/h.go i/j.go k/l.go", 2)
	if !strings.Contains(long, "(+2 more)") {
		t.Fatalf("long report %q must summarize the remaining areas", long)
	}
}

func TestAssignToRelevantReportsTheWriteAreasItShares(t *testing.T) {
	tool := func(t *testing.T) *teamTaskTool {
		released := make(chan struct{})
		close(released)
		// A fresh fixture per dispatch: a member already running a task refuses a
		// second assignment, which is the dispatch contract, not this report's.
		svc, _ := fanoutFixture(t, &assemblyProbe{entered: make(chan struct{}, 3), release: released})
		return &teamTaskTool{name: "leader_assign_task_to_relevant", service: svc, teamName: "alpha", memberID: "lead", leader: true}
	}

	out, err := tool(t).assignToRelevant(context.Background(), []string{"m1", "m2", "m3"}, []string{"coder"},
		"fix the bug in internal/team/role.go", "fix the bug")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "task assigned to m1, m2, m3 (roles=coder)") {
		t.Fatalf("assignment output = %q, want the historical line preserved", out)
	}
	if !strings.Contains(out, "internal/team/role.go") {
		t.Fatalf("assignment output = %q, want the shared write area named", out)
	}

	// A subtask that names no paths keeps the output exactly as it was: the
	// report is advice, and inventing it for every dispatch would be noise.
	out, err = tool(t).assignToRelevant(context.Background(), []string{"m1", "m2"}, []string{"coder"},
		"make the widget faster", "make it faster")
	if err != nil {
		t.Fatal(err)
	}
	if out != "task assigned to m1, m2 (roles=coder)" {
		t.Fatalf("path-free assignment output = %q, want no extra clause", out)
	}
}

// TestTeamWriteTokenIsOneQueuePerTeam pins the property the registry exists for:
// every member of a team reaches the same queue, whatever test or service
// assembled it, while another team's members reach their own.
func TestTeamWriteTokenIsOneQueuePerTeam(t *testing.T) {
	root := t.TempDir()
	token := teamWriteIntentToken("alpha", root)
	if token == nil {
		t.Fatal("a named team over a real root must have a token")
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			if teamWriteIntentToken("alpha", root) != token {
				t.Error("the registry handed out two tokens for one team")
			}
			_ = i
		})
	}
	wg.Wait()
}
