// Part B (TEAM_MEMBER_CACHE_BEHAVIOR_PLAN.md) B0/B2: the member provider-visible
// surface is measured at the real provider boundary, not from the allowlist — a
// member's tool schemas are re-paid as cache-miss bytes on every thinking step,
// so the delta the deferred names buy is what decides whether a second, fold
// aligned schema-pruning pass is worth implementing.
package boot

import (
	"slices"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/skill"
	"reasonix/internal/tool"
)

// memberTokenProbeTools are the host tools a member backend carries in addition
// to the built-ins, so the measurement exercises the same shape a real member
// boot has. The same list is handed to both roles: only TeamRole varies.
func memberTokenProbeTools() []tool.Tool {
	var extra []tool.Tool
	for _, name := range []string{"member_get_my_task", "member_report_result", "member_publish_deliverable", "member_read_deliverable", AtomicReadToolName, AtomicWriteToolName} {
		if _, ok := tool.LookupBuiltin(name); !ok {
			extra = append(extra, &effectExtraTool{name: name})
		}
	}
	return extra
}

// TestMemberSurfaceTokenFootprint measures the deferred-name saving on the real
// wire surface and pins the shape of that saving: the member set is the leader
// set minus exactly the deferred names, so the saving is the cost of those
// schemas and nothing else. B2's decision to implement schema pruning rests on
// this number — if the tail is already small, pruning is not the lever.
func TestMemberSurfaceTokenFootprint(t *testing.T) {
	rec := effectExtraToolStage(t)
	extra := memberTokenProbeTools()

	leaderReq := effectRunWithOptions(t, rec, Options{ExtraTools: extra, TeamRole: skill.TeamRoleLeader})
	memberReq := effectRunWithOptions(t, rec, Options{ExtraTools: extra, TeamRole: skill.TeamRoleMember})

	leader := toolSchemaNames(leaderReq.Tools)
	member := toolSchemaNames(memberReq.Tools)

	want := make([]string, 0, len(leader))
	for _, name := range leader {
		if slices.Contains(memberDeferredTools, name) {
			continue
		}
		want = append(want, name)
	}
	if len(member) != len(want) {
		t.Fatalf("member wire surface is not the leader surface minus the deferred names\ngot  %v\nwant %v", member, want)
	}
	for i, name := range want {
		if member[i] != name {
			t.Fatalf("member wire surface order/contents diverged at %d\ngot  %v\nwant %v", i, member, want)
		}
	}

	// Deferred names must be absent from the wire and still present in the
	// registry, so use_capability can still reach them.
	for _, name := range memberDeferredTools {
		if slices.Contains(member, name) {
			t.Errorf("deferred tool %s reached the member wire surface: %v", name, member)
		}
	}
	if !slices.Contains(leader, "use_capability") || !slices.Contains(member, "use_capability") {
		t.Fatalf("use_capability must stay on both surfaces: leader=%v member=%v", leader, member)
	}

	leaderShape := agent.CaptureShape("", leaderReq.Tools, 0)
	memberShape := agent.CaptureShape("", memberReq.Tools, 0)
	t.Logf("B2 schema footprint: leader %d tools / %d schema tokens; member %d tools / %d schema tokens; deferred saving %d tokens (%.1f%%)",
		len(leader), leaderShape.ToolSchemaTokens,
		len(member), memberShape.ToolSchemaTokens,
		leaderShape.ToolSchemaTokens-memberShape.ToolSchemaTokens,
		100*float64(leaderShape.ToolSchemaTokens-memberShape.ToolSchemaTokens)/float64(leaderShape.ToolSchemaTokens))

	if memberShape.ToolSchemaTokens >= leaderShape.ToolSchemaTokens {
		t.Fatalf("the deferred names must buy a schema-token saving: leader %d member %d",
			leaderShape.ToolSchemaTokens, memberShape.ToolSchemaTokens)
	}
}

// TestMemberSurfaceIsStableAcrossBoots pins the B1 precondition at the boot
// boundary: two independent boots of the same member config must produce a
// byte-identical stable prefix (system + tools). A hash that moves between boots
// would mean the prefix is not reconstructible and every cache miss downstream
// is unattributable.
func TestMemberSurfaceIsStableAcrossBoots(t *testing.T) {
	first := effectRunWithOptions(t, effectExtraToolStage(t), Options{ExtraTools: memberTokenProbeTools(), TeamRole: skill.TeamRoleMember})
	second := effectRunWithOptions(t, effectExtraToolStage(t), Options{ExtraTools: memberTokenProbeTools(), TeamRole: skill.TeamRoleMember})

	firstShape := agent.CaptureShape("", first.Tools, 0)
	secondShape := agent.CaptureShape("", second.Tools, 0)
	if firstShape.ToolsHash != secondShape.ToolsHash {
		t.Fatalf("ToolsHash moved between two member boots: %q then %q", firstShape.ToolsHash, secondShape.ToolsHash)
	}
	if firstShape.PrefixHash != secondShape.PrefixHash {
		t.Fatalf("PrefixHash moved between two member boots: %q then %q", firstShape.PrefixHash, secondShape.PrefixHash)
	}
}
