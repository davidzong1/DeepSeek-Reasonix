package team

// AgentUser is a standalone credential-and-config registry entry (§2.1): a
// stable identity plus provider/model settings, with a secret-store reference
// in place of key material and the roles it may be granted. It belongs to no
// team; members reference it by ID.
type AgentUser struct {
	UserID       string    // stable cross-session, cross-team identifier
	Identity     string    // human account identity
	Provider     string    // provider name
	BaseURL      string    // provider endpoint; empty is the provider default
	Model        string    // model identifier
	Effort       string    // reasoning effort
	SecretRef    SecretRef // secret-store reference, never the key (K1)
	RBACBindings []RoleID  // roles this user may be granted (§2.1)
}

// RoleID names a role; the set is open-ended (§2.3). Roles are the RBAC
// subject — capability grants go through rules, never by editing code.
type RoleID string

const (
	RoleLeader              RoleID = "leader"
	RoleCoder               RoleID = "coder"
	RoleReviewer            RoleID = "reviewer"
	RoleTester              RoleID = "tester"
	RoleArchitectureAnalyst RoleID = "architecture-analyst"
	RolePluginEngineer      RoleID = "plugin-engineer"
)

// Role is a role's default capability signature (§2.3): the role id plus the
// capability ids it may exercise by default.
type Role struct {
	ID           RoleID
	Capabilities []CapabilityID
}

// CapabilityID names one capability; ids are globally unique and mutually
// exclusive within a semantic domain (§7.2).
type CapabilityID string

// Scope is an RBAC resource class (§2.4); judgement runs over the five kinds.
type Scope string

const (
	ScopeTeam      Scope = "team"
	ScopeMember    Scope = "member"
	ScopeAgentUser Scope = "agent-user"
	ScopeStorage   Scope = "storage"
	ScopePlugin    Scope = "plugin"
)

// RBACRule is one declarative (role, capability, scope) allow/deny rule
// (§2.4). Enforcement is centralized; rules are data, not code paths.
type RBACRule struct {
	Role       RoleID
	Capability CapabilityID
	Scope      Scope
	Allow      bool
}

// MemberState is a member's lifecycle state; the classifier priority order
// approval > working > quota > dead > idle is contractual (§2.2).
type MemberState string

const (
	MemberStateApproval MemberState = "approval"
	MemberStateWorking  MemberState = "working"
	MemberStateQuota    MemberState = "quota"
	MemberStateDead     MemberState = "dead"
	MemberStateIdle     MemberState = "idle"
)

// Member is one member's state unit within a team (§2.2): an AgentUser
// reference (override), a role, a lifecycle state, and pointers into the
// task/context space. Members exist only through team lifecycle operations.
type Member struct {
	ID           string // member id, unique within the team
	AgentUserRef string // agent-user override; empty = team default (§3.1)
	Role         RoleID
	State        MemberState
	TaskRef      TaskID // current task pointer; empty when idle
	ResumeCount  int    // recovery counter
	ContextRef   string // context-view pointer
}

// MemberStatus is a member slot's explicit lifecycle state (§2.5): active,
// disabled, or archived, changed only through lifecycle operations.
type MemberStatus string

const (
	MemberStatusActive   MemberStatus = "active"
	MemberStatusDisabled MemberStatus = "disabled"
	MemberStatusArchived MemberStatus = "archived"
)

// MemberSlot is one stable position in the team template (§2.5): identity,
// role, and lifecycle state. The template is fixed at creation; explicit
// lifecycle operations change it, never runtime spawn.
type MemberSlot struct {
	MemberID     string
	Role         RoleID
	AgentUserRef string
	Status       MemberStatus
	Temporary    bool // temporary member, outside the archived topology
}

// Team is the fixed-topology container (§2.5): the member template and the
// team-default AgentUser reference for credential fallback. The message feed,
// blackboard, and memory stores are team-keyed data, not nested fields.
type Team struct {
	Name                string
	Template            []MemberSlot
	DefaultAgentUserRef string // team default credential (§3.1)
}

// TaskID names a dispatch unit (§2.6).
type TaskID string

// TaskStatus is a dispatch unit's lifecycle (§2.6). Report-before-compact is
// a hard contract, not a UI convention.
type TaskStatus string

const (
	TaskStatusCreated  TaskStatus = "created"
	TaskStatusAssigned TaskStatus = "assigned"
	TaskStatusReported TaskStatus = "reported"
	TaskStatusArchived TaskStatus = "archived"
)

// Task is one dispatch unit (§2.6) with references into the context, report,
// and checkpoint spaces; the scheduler assigns it to a member.
type Task struct {
	ID             TaskID
	RequireRole    RoleID // role the scheduler must match; empty = any (§3.5)
	Desc           string
	ContextRef     string
	Expected       string
	ReportRef      string
	CheckpointRef  string
	Status         TaskStatus
	AssignedMember string // member id; empty until assigned
	CreatedAt      string // RFC3339 timestamp
}

// MessageKind classifies feed messages (§2.7). Chat is the member interaction
// channel; the other kinds carry the orchestration flow. Content never
// impersonates the system channel (§9-8).
type MessageKind string

const (
	MessageKindChat     MessageKind = "chat"
	MessageKindAssign   MessageKind = "assign"
	MessageKindReport   MessageKind = "report"
	MessageKindWakeup   MessageKind = "wakeup"
	MessageKindSystem   MessageKind = "system"
	MessageKindApproval MessageKind = "approval"
)

// Message is one unit of the team feed (§2.7), replacing terminal injection:
// a typed envelope with channel and timestamp.
type Message struct {
	ID      string
	Kind    MessageKind
	From    string
	To      string
	Channel string
	Content string
	TS      string // RFC3339 timestamp
}

// BlackboardEntry is one immutable blackboard revision (§2.8): every write
// produces a new revision, so history is replayable and rollback is a pointer
// change, never a rewrite.
type BlackboardEntry struct {
	Rev        int
	Kind       string
	ContentRef string
	Author     string
	TS         string // RFC3339 timestamp
}

// MemoryLayer picks the team-shared or member-private memory store (§2.9);
// the team layer holds facts and decisions, never credentials or keys.
type MemoryLayer string

const (
	MemoryLayerTeam   MemoryLayer = "team"
	MemoryLayerMember MemoryLayer = "member"
)

// MemoryEntry is one durable, traceable memory fact (§2.9): who recorded it,
// when, and against which blackboard revision.
type MemoryEntry struct {
	Layer               MemoryLayer
	OwnerID             string // member id; empty for the team layer
	Content             string
	SourceBlackboardRev int    // blackboard revision the fact derives from
	TS                  string // RFC3339 timestamp
}
