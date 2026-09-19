package boot

// Byte-stability guard for a team member's identity prefix at the provider
// boundary. TestEffectSystemPromptIdentityReachesProviderAndStaysStable covers
// one Build across two turns; this covers two Builds of one team role.

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// teamIdentityFixture mirrors the byte shape the CLI folds into
// Options.SystemPromptIdentity: the member identity lines, then the
// <team-role-skill> block the role loader appends.
const teamIdentityFixture = "You are member \"lead\" of team \"alpha\".\n" +
	"Your team role is: leader.\nParticipate in the team's work as that role and specialty.\n" +
	"\n\n<team-role-skill name=\"leader\">\n\n### leader\n\nLEADER-BASE\n</team-role-skill>"

// teamIdentitySystemPrompt builds the real stack over one workspace, runs one
// turn, and returns the system message that reached the provider boundary.
func teamIdentitySystemPrompt(t *testing.T, rec *effectRecordingProvider, opts Options, before int) string {
	t.Helper()
	ctrl, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "capture the team identity prefix"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := rec.requests()
	if len(reqs) != before+1 {
		t.Fatalf("want one more provider request, got %d (was %d)", len(reqs), before)
	}
	req := reqs[len(reqs)-1]
	if len(req.Messages) == 0 || req.Messages[0].Role != provider.RoleSystem {
		t.Fatalf("first provider message must be the system prompt: %+v", req.Messages)
	}
	return req.Messages[0].Content
}

func TestTeamMemberIdentityPrefixIsByteStableAcrossBuilds(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	// A real team skills tree, so the team-scoped skill store is exercised
	// alongside the identity rather than stubbed out.
	skillsRoot := robustTempDir(t)
	writeFile(t, skillsRoot, "team/skills/base/leader/SKILL.md",
		"---\nname: leader\ndescription: role\n---\nLEADER-BASE")
	writeFile(t, skillsRoot, "team/skills/shared/a/SKILL.md",
		"---\nname: a\ndescription: shared\n---\nSHARED-A")

	rec := &effectRecordingProvider{}
	provider.Register("boot-team-identity-stability", func(provider.Config) (provider.Provider, error) {
		return rec, nil
	})
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[[providers]]
name = "test-model"
kind = "boot-team-identity-stability"
model = "x"
`)

	opts := Options{
		Sink: event.Discard, SystemPromptIdentity: teamIdentityFixture,
		TeamSkillsRoot: skillsRoot, TeamRole: "leader",
	}
	first := teamIdentitySystemPrompt(t, rec, opts, 0)
	second := teamIdentitySystemPrompt(t, rec, opts, 1)

	if first != second {
		t.Fatalf("team member identity prefix is not byte-stable across two builds:\nfirst  (%d bytes) %q\nsecond (%d bytes) %q",
			len(first), first, len(second), second)
	}
	if !strings.Contains(first, teamIdentityFixture) {
		t.Fatalf("the team identity did not reach the provider prefix:\n%q", first)
	}
	if !strings.Contains(first, `<team-role-skill name="leader">`) {
		t.Fatalf("the role playbook block is missing from the provider prefix:\n%q", first)
	}
}
