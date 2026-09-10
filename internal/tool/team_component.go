package tool

// TeamLifecycleStateWriter marks a host tool whose writes are confined to the
// team's own coordination state — task rows, shared results/blackboard, team
// knowledge, member records — never the user's workspace, host, or external
// world. Read-only and plan surfaces keep such tools reachable so a member
// under a read-only/plan task can still close out or advance the team's work;
// the tool's own identity, ownership, generation, lease and publish gates
// still apply on every call.
type TeamLifecycleStateWriter interface {
	TeamLifecycleStateWriter() bool
}
