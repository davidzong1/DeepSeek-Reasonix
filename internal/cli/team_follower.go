// Read-only team-session follower: a second process attaches to a member whose
// canonical history a live writer owns, reading it through the identity in
// OwnerMeta.History.Stem. It takes no lease and never scans the versioned store.

package cli

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/billing"
	"reasonix/internal/checkpoint"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/hook"
	"reasonix/internal/jobs"
	"reasonix/internal/memory"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/sandbox"
	"reasonix/internal/session"
	"reasonix/internal/sessioninbox"
	"reasonix/internal/skill"
	"reasonix/internal/team"
)

// errMemberStaleImport is the refusal a member assembly reaches when the
// transcript it just imported is not the session its owner metadata publishes.
// It is not a writer contention: the import succeeded, but on an identity the
// live owner has already rotated away from (see memberOwnerStemIsCurrent).
var errMemberStaleImport = errors.New("cli: team member transcript imported an identity the owner no longer runs")

// errFollowerReadOnly is the stable refusal every mutating call on a follower
// returns. One sentinel rather than a per-method message: frontends only need
// to say "this window cannot write", and tests assert the class, not the
// wording. It is deliberately not session.ErrReadOnly — that names the durable
// store's own read handle, and conflating the two would make a follower look
// like a writable session whose append happened to be refused.
var errFollowerReadOnly = errors.New("cli: team member session is read-only: another runtime owns its writer lease")

// isMemberWriterContention reports whether a member bind failed because another
// runtime owns the session. Only these two sentinels take the follower path;
// every other bind error stays visible, because a member whose provider, agent
// user or session file is broken must not be papered over as read-only.
//
// The axes differ in where contention surfaces: a v3-exclusive import refuses
// with session.ErrWriterOwned from the store's ownership lock, while the legacy
// axis has no path lease to import through and refuses at write-authority
// binding with agent.ErrSessionLeaseHeld.
func isMemberWriterContention(err error) bool {
	return errors.Is(err, session.ErrWriterOwned) || errors.Is(err, agent.ErrSessionLeaseHeld)
}

// staleMemberImport reports whether the session this attempt just bound is one
// the member's owner metadata has already rotated away from, and so must not be
// handed back as a writable member backend.
//
// The owner stem is authoritative: it names the session the live writer runs (a
// v3 SessionID, else the legacy branch id), and a clear or branch republishes it
// while leaving the legacy transcript it was imported from in place. Re-importing
// that transcript is deterministic — its target is derived from the source path
// and digest — so it reproduces the pre-rotation identity and succeeds, which is
// how a second process became a second writer on a session the owner had left.
//
// The identity compared is the axis's own: the imported SessionRef on the
// exclusive axis, the transcript's branch id on the legacy one. An owner with
// nothing published is not a stale-import verdict — a corrupt owner still fails
// closed where it always did.
func staleMemberImport(owners *team.OwnerStore, b team.MemberBinding, ctrl *control.Controller, path string) bool {
	if owners == nil || ctrl == nil {
		return false
	}
	fingerprint, err := owners.Fingerprint(team.OwnerKey{TeamID: b.Team, MemberID: b.MemberID})
	if err != nil {
		return false
	}
	if !fingerprint.Present || fingerprint.Corrupt {
		return false
	}
	published, ok := followerStemIdentity(fingerprint.Stem)
	if !ok {
		return false
	}
	imported := agent.BranchID(path)
	if ctrl.UsesExclusiveSession() {
		ref, ok := ctrl.SessionRef()
		if !ok {
			return false
		}
		imported = ref.SessionID
	}
	return strings.TrimSpace(imported) != "" && published != imported
}

// followerReadSource is the narrow read surface a follower needs: the member's
// committed transcript plus the identity of what it is reading. Both axes
// implement it, so the adapter is axis-agnostic.
type followerReadSource interface {
	// History returns the member's committed messages. An empty slice is a
	// cleared transcript, not a miss.
	History(ctx context.Context) ([]provider.Message, error)
	// Stamp is the opaque identity of the history just read.
	Stamp() string
}

// memberFollowerBackend is a read-only control.SessionAPI for one member whose
// writer another runtime owns. It binds exactly like an ordinary member backend
// — the window renders that member's transcript, roster row and status — but
// every mutation reached by submit, clear, approve or branch refuses with
// errFollowerReadOnly instead of touching the session.
//
// The embedded port stays nil and is only a compile-time filler, so a method
// that is not overridden does not return a zero value: it dereferences the nil
// interface and panics. That loud failure is right for a test stub, not for the
// host — two reads reach it with no user action behind them (the roster tick's
// history poll and the turn-end balance refresh), both inside a bubbletea Cmd
// goroutine, where the panic is recovered as a program-level failure and takes
// the whole TUI down. A read-only member must answer quietly instead; see
// TestFollowerHostReadsDoNotPanic.
type memberFollowerBackend struct {
	control.SessionAPI
	source followerReadSource
	sink   event.Sink
	label  string
	ref    string
	dir    string
	path   string
	root   string
	prompt string
	// mu guards lastStamp: the roster tick's history poll arrives on a Cmd
	// goroutine while the window may be re-binding on the update goroutine.
	mu        sync.Mutex
	lastStamp string
	// usage is the writer's published telemetry channel (nil without team data).
	// Its own mutex, because usageMu is taken on the render goroutine while mu is
	// taken on a Cmd goroutine.
	usage       followerUsageReader
	usageMu     sync.Mutex
	usageDoc    *team.OwnerUsage
	usageReadAt time.Time
	// usageTicked records that a host refreshed the snapshot off the frame path
	// (refreshUsage). The band then serves what that read installed instead of
	// stat-ing and parsing the document itself.
	usageTicked bool
}

// Compile-time proof the follower is bindable as a member backend.
var _ control.SessionAPI = (*memberFollowerBackend)(nil)

// newMemberFollowerBackend builds the follower for one member. A missing read
// source or an empty identity is an error: a follower that cannot read is not a
// follower, and the caller must keep the member visibly unavailable.
func newMemberFollowerBackend(source followerReadSource, sink event.Sink, label, ref, dir, path, root, prompt string, usage followerUsageReader) (*memberFollowerBackend, error) {
	if source == nil || strings.TrimSpace(source.Stamp()) == "" {
		return nil, errors.New("cli: team member follower has no readable history identity")
	}
	return &memberFollowerBackend{
		source: source, sink: sink, label: label, ref: ref,
		dir: dir, path: path, root: root, prompt: prompt, usage: usage,
	}, nil
}

// refuse is the void-returning half of the refusal surface: the port methods
// that cannot report an error still must not reach the session, so the reason
// goes to the window's own transcript through the member's event sink. Without
// it a refused submit would look like a turn that silently did nothing.
func (b *memberFollowerBackend) refuse() {
	if b.sink == nil {
		return
	}
	b.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: errFollowerReadOnly.Error()})
}

// Transcript, identity and status: the reads the window renders.

func (b *memberFollowerBackend) Label() string         { return b.label }
func (b *memberFollowerBackend) ModelRef() string      { return b.ref }
func (b *memberFollowerBackend) SessionPath() string   { return b.path }
func (b *memberFollowerBackend) SessionDir() string    { return b.dir }
func (b *memberFollowerBackend) WorkspaceRoot() string { return b.root }
func (b *memberFollowerBackend) SystemPrompt() string  { return b.prompt }

// Close is a no-op: the follower owns no controller, no service and no lease,
// so retiring it must not release anything the writer still holds.
func (b *memberFollowerBackend) Close() {}

// History is the member's committed transcript, re-read from the durable source
// on every call: the writer may append between two polls, and a cached copy
// would make the window show a stale tail.
func (b *memberFollowerBackend) History() []provider.Message {
	messages, err := b.source.History(context.Background())
	if err != nil {
		return nil
	}
	return messages
}

// HistoryStamp is the identity being read, so a peer's change is visible here
// exactly as it is to any other window.
func (b *memberFollowerBackend) HistoryStamp() string { return b.source.Stamp() }

// ReloadHistoryIfChanged reports whether the caller must re-render, mirroring
// the controller's own stamp bookkeeping (internal/control/history_sync.go).
// A follower owns no in-memory transcript to adopt — History re-reads the
// durable source on every call — so the only state is the stamp already served,
// and the first poll for a stamp answers "re-read" (the window rebuilds from
// the live read, which is how a follower observes the writer appending). A
// repeat answers false, which also stops the once-a-second tick from reading
// the same history again.
func (b *memberFollowerBackend) ReloadHistoryIfChanged(_ context.Context, stamp string) (bool, error) {
	if b == nil || strings.TrimSpace(stamp) == "" {
		return false, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lastStamp == stamp {
		return false, nil
	}
	b.lastStamp = stamp
	return true, nil
}

// Balance reports no wallet: the follower drives no provider of its own, so it
// has no balance to show. Nil is the quiet answer the balance readout already
// understands, and it keeps a read-only member from blanking the status line of
// the window that shows it.
func (b *memberFollowerBackend) Balance(context.Context) (*billing.Balance, error) { return nil, nil }

// HookRunner returns no runner: the follower runs no hooks. *hook.Runner is
// nil-receiver-safe, so the /hooks viewer renders an empty list instead of
// dispatching anything through the nil port.
func (b *memberFollowerBackend) HookRunner() *hook.Runner { return nil }

// RuntimeStatus is always idle: the follower drives no turn of its own, and
// reporting the writer's activity would make the roster show this window as
// working while its own composer refuses every submission.
func (b *memberFollowerBackend) RuntimeStatus() control.RuntimeStatus {
	return control.RuntimeStatus{}
}

// ToolApprovalMode reports the read-only preset so the footer and the
// permission shortcut agree with what the follower actually permits.
func (b *memberFollowerBackend) ToolApprovalMode() string { return control.ToolApprovalReadOnly }

func (b *memberFollowerBackend) Running() bool          { return false }
func (b *memberFollowerBackend) CancelRequested() bool  { return false }
func (b *memberFollowerBackend) PendingPrompt() bool    { return false }
func (b *memberFollowerBackend) Turn() int              { return 0 }
func (b *memberFollowerBackend) SteerConsumed() bool    { return true }
func (b *memberFollowerBackend) AutoApproveTools() bool { return false }
func (b *memberFollowerBackend) Bypass() bool           { return false }
func (b *memberFollowerBackend) PlanMode() bool         { return false }
func (b *memberFollowerBackend) Goal() string           { return "" }
func (b *memberFollowerBackend) GoalStatus() string     { return "" }
func (b *memberFollowerBackend) AgentPreset() string    { return "" }
func (b *memberFollowerBackend) QualityFloor() string   { return "" }

func (b *memberFollowerBackend) GoalRuntime() control.GoalRuntimeView {
	return control.GoalRuntimeView{}
}

// The usage surfaces read the writer's published observation: a follower owns no
// provider and no context of its own, so it reports the writer's last published
// numbers and nothing once they age past the TTL (see usageSnapshot).

func (b *memberFollowerBackend) ContextSnapshot() (int, int) {
	usage, ok := b.usageSnapshot()
	if !ok {
		return 0, 0
	}
	return usage.ContextUsed, usage.ContextWindow
}

func (b *memberFollowerBackend) LastUsage() *provider.Usage {
	usage, ok := b.usageSnapshot()
	if !ok {
		return nil
	}
	return providerUsageFromLastTurn(usage.LastTurn)
}

func (b *memberFollowerBackend) Jobs() []jobs.View {
	usage, ok := b.usageSnapshot()
	if !ok {
		return nil
	}
	return jobViewsFromOwnerUsage(usage.Jobs)
}
func (b *memberFollowerBackend) Todos() []evidence.TodoItem { return nil }
func (b *memberFollowerBackend) BoundShell() sandbox.Shell  { return sandbox.Shell{} }
func (b *memberFollowerBackend) SessionCache() (int, int) {
	usage, ok := b.usageSnapshot()
	if !ok {
		return 0, 0
	}
	return usage.CacheHit, usage.CacheMiss
}
func (b *memberFollowerBackend) SessionHasUnsavedChanges() bool            { return false }
func (b *memberFollowerBackend) IsDestroyingSession(string) bool           { return false }
func (b *memberFollowerBackend) ToolResult(string) *control.ToolResultData { return nil }
func (b *memberFollowerBackend) ContextMaintenanceSnapshot() agent.ContextMaintenanceSnapshot {
	return agent.ContextMaintenanceSnapshot{}
}

// The catalog reads are deliberately empty rather than delegated: a follower
// has no controller behind it, and an empty slash/skill/MCP catalog renders as
// "nothing to offer here", which is the truth. Delegating would need the
// writer's host, which this process does not have.
func (b *memberFollowerBackend) Host() *plugin.Host                     { return nil }
func (b *memberFollowerBackend) Commands() []command.Command            { return nil }
func (b *memberFollowerBackend) Skills() []skill.Skill                  { return nil }
func (b *memberFollowerBackend) SlashSkills() []skill.Skill             { return nil }
func (b *memberFollowerBackend) AllSkills() []skill.Skill               { return nil }
func (b *memberFollowerBackend) DisabledSkills() []skill.Skill          { return nil }
func (b *memberFollowerBackend) Memory() *memory.Set                    { return nil }
func (b *memberFollowerBackend) MemoryRevisions(string) []memory.Memory { return nil }
func (b *memberFollowerBackend) LastMemoryRecall() memory.RecallResult {
	return memory.RecallResult{}
}
func (b *memberFollowerBackend) Checkpoints() []checkpoint.Meta { return nil }
func (b *memberFollowerBackend) CheckpointFileState(string) (checkpoint.FileState, bool) {
	return checkpoint.FileState{}, false
}
func (b *memberFollowerBackend) CheckpointTurnChanges(int) *checkpoint.TurnChanges { return nil }
func (b *memberFollowerBackend) CheckpointTurnsByMessageIndex() map[int]int        { return nil }
func (b *memberFollowerBackend) CheckpointHasBoundary(int) bool                    { return false }
func (b *memberFollowerBackend) SessionHead() (agent.HeadRef, bool)                { return agent.HeadRef{}, false }
func (b *memberFollowerBackend) Branches() ([]agent.BranchInfo, error)             { return nil, nil }
func (b *memberFollowerBackend) BranchTreeText() string                            { return "" }
func (b *memberFollowerBackend) CurrentBranchID() string                           { return "" }
func (b *memberFollowerBackend) CompactRatio() float64 {
	usage, ok := b.usageSnapshot()
	if !ok {
		return 0
	}
	return usage.CompactRatio
}
func (b *memberFollowerBackend) ContextReport() (string, string)                 { return "", "" }
func (b *memberFollowerBackend) MCPCapabilityViews() []plugin.CapabilityView     { return nil }
func (b *memberFollowerBackend) ConfiguredMCPNames() []string                    { return nil }
func (b *memberFollowerBackend) DisconnectedMCPNames() []string                  { return nil }
func (b *memberFollowerBackend) ExtensionActions() []control.ExtensionActionView { return nil }
func (b *memberFollowerBackend) ProviderCatalog() []provider.Descriptor          { return nil }
func (b *memberFollowerBackend) LoadSkill(string) (skill.Skill, bool)            { return skill.Skill{}, false }
func (b *memberFollowerBackend) SkillEnabled(string) bool                        { return false }
func (b *memberFollowerBackend) CustomCommand(string) (string, bool)             { return "", false }
func (b *memberFollowerBackend) RunSkill(string) (string, bool)                  { return "", false }
func (b *memberFollowerBackend) Compose(text string) string                      { return text }
func (b *memberFollowerBackend) ComposeSynthetic(text string) string             { return text }
func (b *memberFollowerBackend) HasRefs(string) bool                             { return false }
func (b *memberFollowerBackend) ImageInputEnabled() bool                         { return false }
func (b *memberFollowerBackend) ResolveRefs(context.Context, string) (string, []string) {
	return "", nil
}

// The prompt and pending-work surfaces are empty because the follower owns no
// runtime: there is no prompt here to replay, and nothing to cancel.
func (b *memberFollowerBackend) ReplayPendingPrompts()                      {}
func (b *memberFollowerBackend) ReplayPendingPromptsTo(event.Sink)          {}
func (b *memberFollowerBackend) ReplayPendingPromptsWith(func() event.Sink) {}
func (b *memberFollowerBackend) EnableInteractiveApproval()                 {}
func (b *memberFollowerBackend) Cancel()                                    {}
func (b *memberFollowerBackend) SetDisplayRecorder(func(string, string))    {}

// Snapshot and its shutdown siblings are no-ops, not refusals: the window calls
// them on paths that are not history mutations (a model switch that never
// lands, a shutdown), and refusing would report an error for work the follower
// correctly has nothing to do. Nothing is written either way.
func (b *memberFollowerBackend) Snapshot() error            { return nil }
func (b *memberFollowerBackend) SnapshotForShutdown() error { return nil }
func (b *memberFollowerBackend) SnapshotActivity() error    { return nil }
func (b *memberFollowerBackend) CloseAfterDestroy()         {}
func (b *memberFollowerBackend) ReleaseResources()          {}

// The four mutation paths: submit, clear, approve, branch.

// Submit and every sibling entry point refuse. They are the path a keystroke in
// the composer takes, so the refusal must be visible in the transcript rather
// than only in a return value the caller discards.
func (b *memberFollowerBackend) Submit(string)                               { b.refuse() }
func (b *memberFollowerBackend) SubmitDisplay(string, string)                { b.refuse() }
func (b *memberFollowerBackend) SubmitUserTurn(string, string)               { b.refuse() }
func (b *memberFollowerBackend) SubmitDeliveryRecovery(string, string)       { b.refuse() }
func (b *memberFollowerBackend) SubmitFinalReadinessRecovery(string, string) { b.refuse() }
func (b *memberFollowerBackend) SubmitEditedDisplay(string, string, string)  { b.refuse() }
func (b *memberFollowerBackend) SubmitInvocationDisplay(string, string, []control.InvocationRequest) {
	b.refuse()
}
func (b *memberFollowerBackend) SubmitHTTP(string)               { b.refuse() }
func (b *memberFollowerBackend) SubmitHTTPFormat(string, string) { b.refuse() }
func (b *memberFollowerBackend) Send(string)                     { b.refuse() }
func (b *memberFollowerBackend) SendWithRaw(string, string)      { b.refuse() }
func (b *memberFollowerBackend) RunShell(string)                 { b.refuse() }
func (b *memberFollowerBackend) Steer(string)                    { b.refuse() }

// SubmitUserTurnOrError, Run and RunTurn can report, so they return the
// sentinel: the team runtime's task-driving host relies on an explicit refusal
// instead of a silently dropped turn.
func (b *memberFollowerBackend) SubmitUserTurnOrError(string, string) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) Run(context.Context, string) error     { return errFollowerReadOnly }
func (b *memberFollowerBackend) RunTurn(context.Context, string) error { return errFollowerReadOnly }
func (b *memberFollowerBackend) RunFinalReadinessRecovery(context.Context, string) error {
	return errFollowerReadOnly
}

// Clear and new refuse: both rotate the member's session, which the writer owns.
func (b *memberFollowerBackend) ClearSession() error { return errFollowerReadOnly }
func (b *memberFollowerBackend) NewSession() error   { return errFollowerReadOnly }

// ImportMCPEntries refuses: it installs MCP servers into the session's config,
// which is a write the writer's own window must make. The refusal names itself
// instead of returning zeros, so the importer cannot report a successful import
// that never happened.
func (b *memberFollowerBackend) ImportMCPEntries([]config.PluginEntry) (total, added, updated, connected, failed, skipped int, err error) {
	return 0, 0, 0, 0, 0, 0, errFollowerReadOnly
}

// Approvals refuse: the prompt belongs to the writer's run goroutine, and an id
// this process never minted cannot be answered here.
func (b *memberFollowerBackend) Approve(string, bool, bool, bool)                    { b.refuse() }
func (b *memberFollowerBackend) AnswerMCPInteraction(string, string, map[string]any) { b.refuse() }
func (b *memberFollowerBackend) AnswerQuestion(string, []event.AskAnswer)            { b.refuse() }
func (b *memberFollowerBackend) SetToolApprovalMode(string)                          { b.refuse() }
func (b *memberFollowerBackend) SetAutoApproveTools(bool)                            { b.refuse() }
func (b *memberFollowerBackend) SetBypass(bool)                                      { b.refuse() }
func (b *memberFollowerBackend) SetMode(bool, bool)                                  { b.refuse() }
func (b *memberFollowerBackend) ResolveApproval(string, bool, sandbox.ApprovalScope) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) ResolvePlanDecision(string, control.PlanDecisionAction) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) ResolvePlanDecisionWithFeedback(string, control.PlanDecisionAction, string) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) ResolveRecovery(string, agent.RecoveryAction, string) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) AnswerMCPInteractionChecked(string, string, map[string]any) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) AnswerQuestionChecked(string, []event.AskAnswer) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) Ask(context.Context, []event.AskQuestion) ([]event.AskAnswer, error) {
	return nil, errFollowerReadOnly
}

// Branch and its fork/switch siblings refuse: each mints a new head, which is a
// write to the log the writer owns.
func (b *memberFollowerBackend) Branch(string) (string, error) { return "", errFollowerReadOnly }
func (b *memberFollowerBackend) Fork(int) (string, error)      { return "", errFollowerReadOnly }
func (b *memberFollowerBackend) ForkNamed(int, string) (string, error) {
	return "", errFollowerReadOnly
}
func (b *memberFollowerBackend) ForkSession(int, string) (string, error) {
	return "", errFollowerReadOnly
}
func (b *memberFollowerBackend) SwitchBranch(string) (agent.BranchInfo, error) {
	return agent.BranchInfo{}, errFollowerReadOnly
}

// The remaining log-restructuring and identity mutations refuse too. They are
// not on the four named paths, but each rewrites the writer's transcript, so a
// silent no-op would be worse than a refusal.
func (b *memberFollowerBackend) Resume(*agent.Session, string)                            { b.refuse() }
func (b *memberFollowerBackend) SetSessionPath(string)                                    { b.refuse() }
func (b *memberFollowerBackend) SetGoal(string)                                           { b.refuse() }
func (b *memberFollowerBackend) SetGoalWithResearchMode(string, control.GoalResearchMode) { b.refuse() }
func (b *memberFollowerBackend) GoalStrict(bool)                                          { b.refuse() }
func (b *memberFollowerBackend) ClearGoal()                                               { b.refuse() }
func (b *memberFollowerBackend) ResetPlannerSession()                                     { b.refuse() }
func (b *memberFollowerBackend) SetPlanMode(bool)                                         { b.refuse() }
func (b *memberFollowerBackend) SetAgentPreset(string)                                    { b.refuse() }
func (b *memberFollowerBackend) QueueMemory(string)                                       { b.refuse() }
func (b *memberFollowerBackend) SetResponseLanguage(string)                               { b.refuse() }
func (b *memberFollowerBackend) SetReasoningLanguage(string)                              { b.refuse() }
func (b *memberFollowerBackend) ResumeGoal() bool                                         { b.refuse(); return false }
func (b *memberFollowerBackend) PauseGoal() bool                                          { b.refuse(); return false }
func (b *memberFollowerBackend) Rewind(int, control.RewindScope) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) PrepareRewind(int, control.RewindScope) (checkpoint.RewindPlan, error) {
	return checkpoint.RewindPlan{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) CommitRewind(string) (checkpoint.RewindResult, error) {
	return checkpoint.RewindResult{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) CommitRewindInPlace(string) (checkpoint.RewindResult, error) {
	return checkpoint.RewindResult{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) UndoRewind(string) (checkpoint.RewindResult, error) {
	return checkpoint.RewindResult{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) PrepareFileRevert(string) (checkpoint.RewindPlan, error) {
	return checkpoint.RewindPlan{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) CommitFileRevert(string, checkpoint.ConflictResolution) (checkpoint.RewindResult, error) {
	return checkpoint.RewindResult{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) Compact(context.Context, string) error { return errFollowerReadOnly }
func (b *memberFollowerBackend) SummarizeFrom(context.Context, int) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) SummarizeUpTo(context.Context, int) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) SetGoalDurable(string) error { return errFollowerReadOnly }
func (b *memberFollowerBackend) EditGoalDurable(string, *uint64) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) SetQualityFloor(string) error { return errFollowerReadOnly }
func (b *memberFollowerBackend) QuickAdd(memory.Scope, string) (string, error) {
	return "", errFollowerReadOnly
}
func (b *memberFollowerBackend) SaveDoc(string, string) (string, error) {
	return "", errFollowerReadOnly
}
func (b *memberFollowerBackend) SaveMemory(memory.Memory) (string, error) {
	return "", errFollowerReadOnly
}
func (b *memberFollowerBackend) ForgetMemory(string) error { return errFollowerReadOnly }
func (b *memberFollowerBackend) RestoreMemory(string, int) (memory.Memory, error) {
	return memory.Memory{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) RestoreArchivedMemory(string) (memory.Memory, error) {
	return memory.Memory{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) ReloadCommands(context.Context) error { return errFollowerReadOnly }
func (b *memberFollowerBackend) SetSkillEnabled(string, bool) error   { return errFollowerReadOnly }
func (b *memberFollowerBackend) CreateSkill(string, skill.Scope, string) (string, error) {
	return "", errFollowerReadOnly
}
func (b *memberFollowerBackend) UpdateSkill(string, skill.Scope, string) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) DeleteSkill(string, skill.Scope) error { return errFollowerReadOnly }
func (b *memberFollowerBackend) MCPPrompt(context.Context, string) (string, bool, error) {
	return "", false, errFollowerReadOnly
}
func (b *memberFollowerBackend) InvokeExtensionAction(context.Context, string, map[string]string) (string, error) {
	return "", errFollowerReadOnly
}
func (b *memberFollowerBackend) RegisterExternalFolderRef(string) (string, string, error) {
	return "", "", errFollowerReadOnly
}
func (b *memberFollowerBackend) ApplyComposerProfile(bool, string, string) ([]string, error) {
	return nil, errFollowerReadOnly
}
func (b *memberFollowerBackend) SetInboxPaused(bool) error       { return errFollowerReadOnly }
func (b *memberFollowerBackend) DeleteInboxItem(string) error    { return errFollowerReadOnly }
func (b *memberFollowerBackend) MoveInboxItem(string, int) error { return errFollowerReadOnly }
func (b *memberFollowerBackend) RetryInboxItem(string) error     { return errFollowerReadOnly }
func (b *memberFollowerBackend) RefreshInboxReferences(string) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) RunInboxTurn(context.Context, string) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) CancelWithInboxItems([]string, string) error {
	return errFollowerReadOnly
}
func (b *memberFollowerBackend) CancelWithInboxItemsResult([]string, string) (control.InboxCancelResult, error) {
	return control.InboxCancelResult{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) AddMCPServer(config.PluginEntry) (int, error) {
	return 0, errFollowerReadOnly
}
func (b *memberFollowerBackend) ConnectMCPServer(config.PluginEntry) (int, error) {
	return 0, errFollowerReadOnly
}
func (b *memberFollowerBackend) RegisterMCPServerOnDemand(config.PluginEntry) (int, error) {
	return 0, errFollowerReadOnly
}
func (b *memberFollowerBackend) ConnectConfiguredMCPServer(string) (int, error) {
	return 0, errFollowerReadOnly
}
func (b *memberFollowerBackend) RemoveMCPServer(string) (bool, error) {
	return false, errFollowerReadOnly
}
func (b *memberFollowerBackend) DisconnectMCPServer(string) bool      { return false }
func (b *memberFollowerBackend) UnregisterMCPServerTools(string) bool { return false }
func (b *memberFollowerBackend) BeginDestroySession(string) control.SessionDestroyHandle {
	return control.SessionDestroyHandle{}
}

// Inbox.

// The inbox is the member's queued work, which only its writer can dispatch, so
// the reads report an empty queue and every mutation refuses. An empty snapshot
// is the honest answer: this process holds no items for a session it cannot run.
func (b *memberFollowerBackend) InboxSnapshot() sessioninbox.InboxSnapshot {
	return sessioninbox.InboxSnapshot{}
}
func (b *memberFollowerBackend) EnqueueInbox(control.InboxRequest) (sessioninbox.InboxReceipt, error) {
	return sessioninbox.InboxReceipt{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) ReadInboxItem(string) (sessioninbox.InboxItemMeta, sessioninbox.PromptEnvelope, error) {
	return sessioninbox.InboxItemMeta{}, sessioninbox.PromptEnvelope{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) UpdateInboxItem(string, string, string, string) (sessioninbox.InboxItemMeta, error) {
	return sessioninbox.InboxItemMeta{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) AppendInboxItem(string, string, string, map[string]string) (sessioninbox.InboxItemMeta, error) {
	return sessioninbox.InboxItemMeta{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) TrySubmitInboxItem(string) (sessioninbox.InboxReceipt, error) {
	return sessioninbox.InboxReceipt{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) TrySteerInboxItem(string) (sessioninbox.InboxReceipt, error) {
	return sessioninbox.InboxReceipt{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) TryEnqueueAndSteer(control.InboxRequest) (sessioninbox.InboxReceipt, error) {
	return sessioninbox.InboxReceipt{}, errFollowerReadOnly
}
func (b *memberFollowerBackend) TryEnqueueFollowup(control.InboxRequest) (sessioninbox.InboxReceipt, error) {
	return sessioninbox.InboxReceipt{}, errFollowerReadOnly
}

// Read sources.

// followerV3Source reads one v3-exclusive session through the host's query
// surface. Query opens a cold read handle: the writer's lease is untouched.
type followerV3Source struct {
	service *session.Service
	ref     session.SessionRef
	stamp   string
}

func (s followerV3Source) History(ctx context.Context) ([]provider.Message, error) {
	if s.service == nil || s.ref.SessionID == "" {
		return nil, errors.New("cli: follower has no v3 session service")
	}
	return s.service.Query().History(ctx, s.ref)
}

func (s followerV3Source) Stamp() string { return s.stamp }

// followerLegacySource reads one legacy transcript inside the member's
// canonical owner directory. LoadSession takes the transcript's in-process save
// lock only, never the cross-process session lease the writer holds, so a second
// process can read it while the writer keeps appending.
type followerLegacySource struct {
	path  string
	stamp string
}

func (s followerLegacySource) History(ctx context.Context) ([]provider.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loaded, err := agent.LoadSession(s.path)
	if err != nil {
		return nil, err
	}
	return loaded.Snapshot(), nil
}

func (s followerLegacySource) Stamp() string { return s.stamp }

// followerStemIdentity splits an owner history stem into the identity a
// read-only reader can open. The shape is fixed by the two stamp producers
// (control.historyStampDurable and Controller.HistoryStamp's legacy branch): a
// v3 stem is "<SessionID>:<sequence>", a legacy stem is
// "<branch id>:<size>:<mtime>". Only the leading field is needed and the
// remainder is never interpreted, so a malformed stem is refused by the caller
// rather than guessed at.
//
// The follower reports the stem verbatim as its own identity rather than
// recomputing one: the stem is what every peer compares, and a recomputed value
// would drift from the writer's the moment either side's sequence advanced.
func followerStemIdentity(stem string) (string, bool) {
	stem = strings.TrimSpace(stem)
	if stem == "" {
		return "", false
	}
	if i := strings.Index(stem, ":"); i > 0 {
		return stem[:i], true
	}
	return stem, true
}

// followerSourceFor resolves the read source for one member from its canonical
// owner identity. exclusive selects the axis and comes from the controller the
// caller just built, so no axis is ever inferred from the stem.
//
// An absent owner directory, an unreadable document, or a stem naming no
// readable session is an error: the member stays visibly unavailable rather
// than showing an invented empty transcript.
func followerSourceFor(ctx context.Context, owners *team.OwnerStore, key team.OwnerKey, exclusive bool, service *session.Service, ownerDir, sessionFile string) (followerReadSource, error) {
	if owners == nil {
		return nil, errors.New("cli: team member follower requires canonical owner storage")
	}
	fingerprint, err := owners.Fingerprint(key)
	if err != nil {
		return nil, err
	}
	if !fingerprint.Present {
		return nil, errors.New("cli: team member has no canonical history to follow")
	}
	if fingerprint.Corrupt {
		// An unreadable document is not an unnamed owner: following it would
		// render an empty transcript as if the member had been cleared.
		return nil, errors.New("cli: team member owner metadata is unreadable")
	}
	id, ok := followerStemIdentity(fingerprint.Stem)
	if !ok {
		return nil, errors.New("cli: team member has published no history identity to follow")
	}
	if !exclusive {
		// The stem's leading field is the transcript's branch id — its file name
		// without the extension. A stem naming any other branch is refused rather
		// than silently resolved to whatever sits in the owner directory.
		path := filepath.Join(ownerDir, sessionFile)
		if id != agent.BranchID(path) {
			return nil, errors.New("cli: team member history identity does not match its canonical transcript")
		}
		if _, statErr := agent.LoadSession(path); statErr != nil {
			return nil, statErr
		}
		return followerLegacySource{path: path, stamp: fingerprint.Stem}, nil
	}
	if service == nil {
		return nil, errors.New("cli: team member follower has no session service")
	}
	ref := session.SessionRef{HostID: service.HostID(), SessionID: id}
	if _, err := service.Query().Recent(ctx, ref); err != nil {
		return nil, err
	}
	return followerV3Source{service: service, ref: ref, stamp: fingerprint.Stem}, nil
}

// newMemberFollower attaches the read-only follower for a member whose bind
// lost to a live writer runtime. It runs inside the member builder, right where
// the refusal happened: the controller built for this attempt is closed first —
// it never bound a session, so it holds no lease — and the follower takes its
// place in the registry, so the window shows that member's transcript and
// roster row instead of an unavailable member.
//
// The bind error is returned unchanged when no readable identity can be
// resolved: the caller keeps the member visibly unavailable rather than showing
// an empty transcript as if the member had no history.
func newMemberFollower(deps memberBackendDeps, ctrl *control.Controller, b team.MemberBinding, bindErr error) (control.SessionAPI, error) {
	if ctrl == nil || deps.owners == nil {
		if ctrl != nil {
			ctrl.Close()
		}
		return nil, bindErr
	}
	key := team.OwnerKey{TeamID: b.Team, MemberID: b.MemberID}
	ownerDir := ""
	if paths, err := deps.owners.Paths(key); err == nil {
		ownerDir = paths.Dir
	}
	if ownerDir == "" {
		ctrl.Close()
		return nil, bindErr
	}
	exclusive := ctrl.UsesExclusiveSession()
	source, err := followerSourceFor(deps.ctx, deps.owners, key, exclusive, ctrl.SessionService(), ownerDir, b.SessionFile)
	if err != nil {
		ctrl.Close()
		// Report the bind failure, not the read error: "member unavailable" is the
		// truthful summary, and the cause belongs in the log, not in a UI string.
		slog.Warn("team member follower unavailable", "team", b.Team, "member", b.MemberID, "err", err)
		return nil, bindErr
	}
	path := filepath.Join(ownerDir, b.SessionFile)
	follower, err := newMemberFollowerBackend(
		source, deps.events.sink(b.MemberID),
		b.MemberID, ctrl.ModelRef(), ctrl.SessionDir(), path, ctrl.WorkspaceRoot(), ctrl.SystemPrompt(),
		newFollowerUsageReader(deps.owners, key),
	)
	ctrl.Close()
	if err != nil {
		return nil, bindErr
	}
	// Said out loud because it is the one state an operator cannot see from the
	// window: this window is a reader. Whether its gauges can be filled depends
	// on another runtime publishing, so the log must name who was refused.
	slog.Info("team member bound read-only (writer is another runtime)",
		"team", b.Team, "member", b.MemberID, "reason", bindErr)
	return follower, nil
}
