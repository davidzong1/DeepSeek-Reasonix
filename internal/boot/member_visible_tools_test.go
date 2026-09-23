package boot

// A member's provider-visible surface is the shared surface minus the deferred
// names: those tools stay registered and stay reachable through use_capability,
// they only stop being re-sent to the provider on every thinking step.

import (
	"reflect"
	"slices"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/skill"
	"reasonix/internal/tool"
)

// memberResidentNames are the names a member must still see directly. The file
// pair is listed on both sides of the atomic substitution: exactly one of the
// two forms is visible in any one assembly, and which one depends on the
// surface the boot resolved.
var (
	memberResidentNames = []string{
		"bash",
		"use_capability",
		"member_get_my_task",
		"member_report_result",
		"member_publish_deliverable",
		"member_read_deliverable",
	}
	memberAtomicPairNames = []string{AtomicReadToolName, AtomicWriteToolName}
	memberLegacyTrioNames = []string{"read_file", "write_file", "edit_file"}
)

// memberSurfaceRegistry builds the inventory a member backend actually carries:
// the enabled built-ins plus the member task and deliverable tools, which arrive
// as host ExtraTools. A name that is not a compile-time built-in (web_search is
// registered by the boot's own search wiring, the member_* tools by the CLI) is
// added as a host tool of the same shape, so the deferred set is exercised
// whole rather than only where a builtin happens to exist.
func memberSurfaceRegistry(t *testing.T) (*tool.Registry, []tool.Tool) {
	t.Helper()
	reg := tool.NewRegistry()
	var extra []tool.Tool
	for _, name := range append(append([]string{}, memberResidentNames...), memberDeferredTools...) {
		if tl, ok := tool.LookupBuiltin(name); ok {
			reg.Add(tl)
			continue
		}
		tl := &effectExtraTool{name: name}
		reg.Add(tl)
		extra = append(extra, tl)
	}
	// The atomic pair is registered on every build; whether it is visible is the
	// surface's decision, which is what these cases vary.
	for _, name := range memberAtomicPairNames {
		if _, ok := reg.Get(name); !ok {
			reg.Add(&effectExtraTool{name: name})
		}
	}
	for _, name := range memberLegacyTrioNames {
		if _, ok := reg.Get(name); !ok {
			reg.Add(&effectExtraTool{name: name})
		}
	}
	return reg, extra
}

// TestMemberVisibleToolsDropsDeferredNames pins the contract: with the atomic
// surface on, the deferred names leave the provider-visible set and the atomic
// pair takes the trio's place.
func TestMemberVisibleToolsDropsDeferredNames(t *testing.T) {
	reg, extra := memberSurfaceRegistry(t)
	surface := atomicFSSurface(configForAtomicFS(t, config.AtomicFSTeam), true)
	if surface == nil {
		t.Fatal("the team fixture must resolve an atomic surface")
	}
	applyUnifiedProviderToolSurface(reg, extra, surface, skill.TeamRoleMember)

	got := toolSchemaNames(reg.Schemas())
	for _, name := range memberDeferredTools {
		if slices.Contains(got, name) {
			t.Errorf("deferred tool %s is still provider-visible: %v", name, got)
		}
	}
	for _, name := range append(append([]string{}, memberResidentNames...), memberAtomicPairNames...) {
		if !slices.Contains(got, name) {
			t.Errorf("member surface is missing %s: %v", name, got)
		}
	}
	for _, name := range memberLegacyTrioNames {
		if slices.Contains(got, name) {
			t.Errorf("the mounted atomic pair must substitute %s: %v", name, got)
		}
	}
}

// TestMemberVisibleToolsKeepsLegacyTrioWithoutThePair pins the other half of the
// ordering: the substitution runs before the deferred-name pass, so a build with
// no atomic surface keeps its file tools rather than losing them to a name that
// was only ever meant to remove the trio's replacements' siblings.
func TestMemberVisibleToolsKeepsLegacyTrioWithoutThePair(t *testing.T) {
	reg, extra := memberSurfaceRegistry(t)
	applyUnifiedProviderToolSurface(reg, extra, nil, skill.TeamRoleMember)

	got := toolSchemaNames(reg.Schemas())
	for _, name := range memberLegacyTrioNames {
		if !slices.Contains(got, name) {
			t.Errorf("a member without the atomic pair must keep %s: %v", name, got)
		}
	}
	for _, name := range memberAtomicPairNames {
		if slices.Contains(got, name) {
			t.Errorf("%s is not on a surface that never named it: %v", name, got)
		}
	}
	for _, name := range memberDeferredTools {
		if slices.Contains(got, name) {
			t.Errorf("deferred tool %s is still provider-visible: %v", name, got)
		}
	}
}

// TestMemberVisibleToolsStayExecutable is the proxy for use_capability: every
// deferred name is still registered, so dispatch by name still reaches it. This
// test does not call use_capability itself.
func TestMemberVisibleToolsStayExecutable(t *testing.T) {
	reg, extra := memberSurfaceRegistry(t)
	applyUnifiedProviderToolSurface(reg, extra, nil, skill.TeamRoleMember)
	for _, name := range memberDeferredTools {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("deferred tool %s was unregistered, so it can no longer be executed: %v", name, reg.AllNames())
		}
	}
}

// TestMemberVisibleToolsLeaveOtherRolesAlone pins the blast radius: only the
// member role narrows. The leader and a role-less build keep exactly what
// dropMemberDeferredTools was handed, and a leader's full surface still carries
// every deferred name it has registered.
func TestMemberVisibleToolsLeaveOtherRolesAlone(t *testing.T) {
	handed := []string{"bash", "job_output", "ask", "read_file", "use_capability", "web_search"}
	for _, role := range []string{"", "leader"} {
		if got := dropMemberDeferredTools(role, slices.Clone(handed)); !reflect.DeepEqual(got, handed) {
			t.Errorf("role %q narrowed the surface: got %v, want %v", role, got, handed)
		}
	}
	// The member role is recognized through surrounding whitespace, as the boot
	// options are; an unknown name in the list is ignored, not an error.
	if got := dropMemberDeferredTools(" member ", slices.Clone(handed)); !reflect.DeepEqual(got, []string{"bash", "read_file", "use_capability"}) {
		t.Errorf("member surface = %v, want [bash read_file use_capability]", got)
	}

	reg, extra := memberSurfaceRegistry(t)
	surface := atomicFSSurface(configForAtomicFS(t, config.AtomicFSTeam), true)
	applyUnifiedProviderToolSurface(reg, extra, surface, skill.TeamRoleLeader)
	got := toolSchemaNames(reg.Schemas())
	for _, name := range memberDeferredTools {
		if !slices.Contains(got, name) {
			t.Errorf("a leader must keep %s visible: %v", name, got)
		}
	}
}

// TestMemberVisibleToolsMatchNonMemberSurfaceMinusDeferred pins the two
// surfaces against each other on the same inventory: the member set is exactly
// the leader set minus the deferred names, so the narrowing is a subtraction and
// never a silent addition or reorder.
func TestMemberVisibleToolsMatchNonMemberSurfaceMinusDeferred(t *testing.T) {
	leaderReg, leaderExtra := memberSurfaceRegistry(t)
	memberReg, memberExtra := memberSurfaceRegistry(t)
	surface := atomicFSSurface(configForAtomicFS(t, config.AtomicFSTeam), true)
	applyUnifiedProviderToolSurface(leaderReg, leaderExtra, surface, skill.TeamRoleLeader)
	applyUnifiedProviderToolSurface(memberReg, memberExtra, surface, skill.TeamRoleMember)

	leader := toolSchemaNames(leaderReg.Schemas())
	member := toolSchemaNames(memberReg.Schemas())
	want := make([]string, 0, len(leader))
	for _, name := range leader {
		if slices.Contains(memberDeferredTools, name) {
			continue
		}
		want = append(want, name)
	}
	if !reflect.DeepEqual(member, want) {
		t.Fatalf("member surface is not the leader surface minus the deferred names\ngot  %v\nwant %v", member, want)
	}
}
