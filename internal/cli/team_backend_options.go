package cli

import (
	"strings"

	"reasonix/internal/boot"
	"reasonix/internal/team"
)

// memberBackendOptions assembles the boot options one member backend inherits.
// The observation sink is built here — before the controller exists — while the
// publisher that drains it starts only on the writable path, so a backend that
// resolves to a follower keeps the wrapper and records nothing.
func memberBackendOptions(deps memberBackendDeps, b team.MemberBinding, resolver *memberProviderResolver) (boot.Options, *memberUsagePublisher) {
	opts := deps.base()
	if strings.TrimSpace(deps.workspaceRoot) != "" {
		opts.WorkspaceRoot = deps.workspaceRoot
	}
	// Both the write scope and the posture are fixed here rather than carried
	// across rebuilds: this builder is the only construction point, so eviction,
	// model rebind and quota failover all re-derive them for free.
	opts.AdditionalDirs = append(opts.AdditionalDirs, memberWriteRoots(b.Leader)...)
	opts.HeadlessApprovalMode = memberApprovalPosture(b.Leader)
	// Team playbooks are user-global: the member's role tree is read from
	// the user state root, so it resolves from any launching directory and
	// a team's recorded workspace cannot steer it.
	opts.TeamSkillsRoot = teamSkillsBase()
	opts.TeamRole = string(roleForLeader(b.Leader))
	opts.WorkspaceLeaseLabel = memberWorkspaceLeaseLabel(b)
	// The team's in-process write token. It must follow the workspace-root
	// assignment above: that root is what the token compares scopes against.
	opts.WriteIntentGate = memberWriteIntentGate(b.Team, opts.WorkspaceRoot, b.MemberID)
	opts.Model = resolver.Ref()
	opts.ProviderResolver = resolver
	observatory := newMemberUsagePublisher(deps.owners, team.OwnerKey{TeamID: b.Team, MemberID: b.MemberID}, resolver.RouteBucket())
	opts.Sink = memberObservationSink(observatory, deps.events.sink(b.MemberID), b.Leader)
	opts.SystemPromptIdentity = memberSystemPromptIdentity(b) +
		// Invalid team_role declarations warn through the assembly's own
		// diagnostic writer (nil keeps the historical silence).
		teamRoleSkillPrompt(opts.TeamSkillsRoot, b.Leader, opts.Stderr)
	opts.ExtraTools = append(opts.ExtraTools, memberExtraTools(deps, b, opts.Stderr)...)
	return opts, observatory
}
