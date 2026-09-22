package cli

// L1 (TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md): the label a member publishes in
// workspace-lease holder records must name the person a queued teammate reads.

import (
	"strings"
	"testing"

	"reasonix/internal/team"
)

func TestMemberWorkspaceLeaseLabelNamesMemberAndTeam(t *testing.T) {
	cases := []struct {
		name   string
		bind   team.MemberBinding
		want   string
		absent string
	}{
		{name: "member and team", bind: team.MemberBinding{Team: "alpha", MemberID: "lead"}, want: "member lead of team alpha"},
		{name: "member only", bind: team.MemberBinding{MemberID: "lead"}, want: "member lead"},
		{name: "unbound member", bind: team.MemberBinding{Team: "alpha"}, want: ""},
		{name: "trimmed", bind: team.MemberBinding{Team: " alpha ", MemberID: " lead "}, want: "member lead of team alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := memberWorkspaceLeaseLabel(tc.bind)
			if got != tc.want {
				t.Fatalf("memberWorkspaceLeaseLabel = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "\n") {
				t.Fatalf("lease label %q must stay on one notice line", got)
			}
		})
	}
}
