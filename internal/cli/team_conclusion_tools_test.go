package cli

// The shared-conclusion tools: a member posts one short finding, the leader
// reads the board's current topics on request, and neither surface carries the
// other's half. The store-level semantics live in the team package's tests.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// conclusionFixture roots a two-member team on a real SQLite board and returns
// the per-team service both faces are assembled from.
func conclusionFixture(t *testing.T) (*teamTaskService, *team.SQLiteStore) {
	t.Helper()
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
			{MemberID: "m1", Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "m2", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { board.Close() })
	return newTeamTaskService(teamStore, board, "alpha", nil), board
}

func conclusionToolByName(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, candidate := range tools {
		if candidate.Name() == name {
			return candidate
		}
	}
	t.Fatalf("%s is missing from the surface", name)
	return nil
}

// TestMemberPostConclusionReceiptIsJustTheTopic pins the member receipt: it names
// the topic it stored and carries no board rendering, so a post never becomes a
// second read of the board inside the member's own context.
func TestMemberPostConclusionReceiptIsJustTheTopic(t *testing.T) {
	svc, board := conclusionFixture(t)
	post := conclusionToolByName(t, newMemberTaskTools(svc, "alpha", "m1"), postConclusionName)
	out, err := post.Execute(context.Background(), json.RawMessage(`{"topic":"storage","summary":"use sqlite, not files"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "storage") {
		t.Fatalf("receipt must name the stored topic, got %q", out)
	}
	for _, unwanted := range []string{"[board delta]", "[board conclusions]", "- m1"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("receipt leaked the board rendering %q:\n%s", unwanted, out)
		}
	}
	view, err := board.ReadView(context.Background(), team.BoardShared, team.ViewSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Conclusions) != 1 || view.Conclusions[0].Summary != "use sqlite, not files" {
		t.Fatalf("board after the post = %+v", view.Conclusions)
	}
}

// TestMemberPostConclusionRefusesAnOverlongSummary pins that the tool surface
// refuses what the store refuses: the model gets the cap back as an error instead
// of a stored truncation, and nothing reaches the board.
func TestMemberPostConclusionRefusesAnOverlongSummary(t *testing.T) {
	svc, board := conclusionFixture(t)
	post := conclusionToolByName(t, newMemberTaskTools(svc, "alpha", "m1"), postConclusionName)
	args, err := json.Marshal(map[string]string{"topic": "storage", "summary": strings.Repeat("s", 161)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := post.Execute(context.Background(), args); err == nil {
		t.Fatal("an over-cap summary must be refused")
	}
	view, err := board.ReadView(context.Background(), team.BoardShared, team.ViewSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Conclusions) != 0 {
		t.Fatalf("a refused post wrote %d conclusions", len(view.Conclusions))
	}
}

// TestLeaderReadConclusionsRendersTheBoardWithoutWaking pins the leader face: the
// read returns the posted topic under its own header, an empty board says so, and
// reading produces no wakeup event — a conclusion is never a reason to wake a
// leader.
func TestLeaderReadConclusionsRendersTheBoardWithoutWaking(t *testing.T) {
	svc, board := conclusionFixture(t)
	read := conclusionToolByName(t, newLeaderTaskTools(svc, "alpha", "lead"), readConclusionsName)

	empty, err := read.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if empty != "[board conclusions]\n(none)" {
		t.Fatalf("empty read = %q", empty)
	}
	post := conclusionToolByName(t, newMemberTaskTools(svc, "alpha", "m2"), postConclusionName)
	if _, err := post.Execute(context.Background(), json.RawMessage(`{"topic":"storage","summary":"use sqlite"}`)); err != nil {
		t.Fatal(err)
	}
	out, err := read.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[board conclusions]") || !strings.Contains(out, "- m2 storage: use sqlite") {
		t.Fatalf("leader read = %q, want the topic under the conclusions header", out)
	}
	page, err := board.ReadAfter(context.Background(), team.BoardShared, 0, team.Filter{
		Kind: team.EventWakeup, Stamped: team.Identity{MemberID: "lead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("a post or read produced %d wakeups", len(page.Events))
	}
}

// TestConclusionToolsStayOnTheirOwnFaces pins the surface split: a member never
// holds the leader's read and a leader never holds the member's post, and each
// half keeps its own read-only classification.
func TestConclusionToolsStayOnTheirOwnFaces(t *testing.T) {
	svc, _ := conclusionFixture(t)
	member := newMemberTaskTools(svc, "alpha", "m1")
	leader := newLeaderTaskTools(svc, "alpha", "lead")
	for _, candidate := range member {
		if candidate.Name() == readConclusionsName {
			t.Error("a member must not hold the leader's conclusions read")
		}
	}
	for _, candidate := range leader {
		if candidate.Name() == postConclusionName {
			t.Error("a leader must not hold the member's post tool")
		}
	}
	post := conclusionToolByName(t, member, postConclusionName)
	if post.ReadOnly() {
		t.Error("member_post_conclusion is a write and must not be read-only")
	}
	if safe, ok := post.(tool.PlanModeClassifier); !ok || safe.PlanModeSafe() {
		t.Error("member_post_conclusion is a write and must not be plan-safe")
	}
	if writer, ok := post.(tool.TeamLifecycleStateWriter); !ok || !writer.TeamLifecycleStateWriter() {
		t.Error("member_post_conclusion writes team coordination state and must stay reachable under a read-only turn")
	}
	read := conclusionToolByName(t, leader, readConclusionsName)
	if !read.ReadOnly() {
		t.Error("leader_read_conclusions is a read and must be read-only")
	}
	if safe, ok := read.(tool.PlanModeClassifier); !ok || !safe.PlanModeSafe() {
		t.Error("leader_read_conclusions is a read and must be plan-safe")
	}
}

// TestConclusionToolsRefuseWithoutABoard pins the unavailable path: a service
// with no board answers with an actionable refusal instead of panicking or
// silently reporting success.
func TestConclusionToolsRefuseWithoutABoard(t *testing.T) {
	svc := &teamTaskService{teamName: "alpha"}
	for _, tc := range []struct {
		name string
		tl   tool.Tool
	}{
		{postConclusionName, newMemberConclusionTool(svc, "alpha", "m1")},
		{readConclusionsName, newLeaderConclusionTool(svc, "alpha", "lead")},
	} {
		if _, err := tc.tl.Execute(context.Background(), json.RawMessage(`{"topic":"t","summary":"s"}`)); err == nil {
			t.Errorf("%s on a board-less service must refuse", tc.name)
		}
	}
}
