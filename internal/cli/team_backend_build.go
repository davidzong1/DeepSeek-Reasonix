package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// memberProviderResolver serves exactly one member's pool entry as a catalog of
// one, so boot.Build assembles that member's backend from its AgentUser rather
// than from the ambient session's configured model. boot treats a caller-owned
// resolver as authoritative for every ref (resolveModelEntry), so the member's
// ref resolves here and its provider is dialled with the pool entry's endpoint
// and credential.
//
// The credential never travels through config.ProviderEntry: that type resolves
// keys from an environment variable only, and exporting a pool key into the
// process environment would leak it across every member. provider.New takes it
// directly instead, so it stays on this call path.
type memberProviderResolver struct {
	ref                   string
	name                  string
	kind                  string
	endpoint              string
	model                 string
	apiKey                string
	effort                string
	reasoningProtocol     string
	proxy                 netclient.ProxySpec
	context1M             bool
	deepSeekAnthropic     bool
	anthropicBearerHeader bool
}

// newMemberProviderResolver maps one AgentUser onto a single-entry resolver.
// The provider kind and endpoint come from team.ResolveProvider, so a member
// never starts against a guessed endpoint; an unsupported provider is refused
// here rather than at the first request.
func newMemberProviderResolver(u team.AgentUser, proxy netclient.ProxySpec) (*memberProviderResolver, error) {
	kind, endpoint, err := team.ResolveAgentUserProvider(u)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(u.UserID)
	if name == "" {
		name = kind
	}
	model := strings.TrimSpace(u.Model)
	wireModel, context1M := team.ResolveAgentUserModel(u)
	providerName := team.NormalizeProvider(strings.TrimSpace(u.Provider))
	deepSeekAnthropic := providerName == team.ProviderDeepSeek && kind == "anthropic" && context1M
	if context1M {
		// [1m] is a client-side context alias for every provider. DeepSeek's
		// Anthropic-compatible route additionally enables its protocol beta below.
		model = wireModel
	}
	return &memberProviderResolver{
		ref:               memberModelRef(name, u.Model),
		name:              name,
		kind:              kind,
		endpoint:          endpoint,
		model:             model,
		apiKey:            u.APIKey,
		effort:            strings.TrimSpace(u.Effort),
		reasoningProtocol: memberReasoningProtocol(providerName, kind, endpoint, model),
		proxy:             proxy,
		context1M:         context1M,

		// MCP Claude profiles use ANTHROPIC_AUTH_TOKEN for this route. Preserve
		// that wire contract after importing the profile into a team AgentUser.
		deepSeekAnthropic:     deepSeekAnthropic,
		anthropicBearerHeader: deepSeekAnthropic,
	}, nil
}

// memberModelRef is the "provider/model" ref shape boot splits back apart when
// it synthesizes a config entry from the catalog descriptor.
func memberModelRef(name, model string) string {
	if model = strings.TrimSpace(model); model == "" {
		return name
	}
	return name + "/" + model
}

// memberReasoningProtocol names the adapter protocol a pool entry's effort
// vocabulary is read from; the adapter owns that vocabulary, so an undeclared
// one would reject every level a member sets. The endpoint-derived protocol wins
// so a re-pointed gateway keeps its own contract; the entry's declared provider
// is the fallback for endpoints the config table cannot classify. Empty leaves
// the adapter default, which is permissive.
func memberReasoningProtocol(providerName, kind, endpoint, model string) string {
	if protocol := config.ReasoningProtocolForEntry(&config.ProviderEntry{
		Kind: kind, BaseURL: endpoint, Model: model,
	}); protocol != "" {
		return protocol
	}
	switch providerName {
	case team.ProviderDeepSeek:
		return config.ReasoningProtocolDeepSeek
	case team.ProviderOpenAI:
		return config.ReasoningProtocolOpenAI
	}
	return ""
}

// memberEffortVocabulary is the adapter's declared levels for this member's
// endpoint. boot's role preflight reads it through syntheticEntryFromResolver,
// whose entry has no Kind, so a default declared without it is self-contradictory.
func (r *memberProviderResolver) memberEffortVocabulary() []string {
	return config.ReasoningCapabilityForEntry(&config.ProviderEntry{
		Kind: r.kind, BaseURL: r.endpoint, Model: r.model,
		ReasoningProtocol: r.reasoningProtocol,
	}).IDs()
}

// Ref is the model ref boot.Options.Model must carry for this member.
func (r *memberProviderResolver) Ref() string { return r.ref }

// Catalog reports the one entry this resolver owns. Tools and Reasoning are
// declared: a member is a full Agent, so the assembled request carries the tool
// schemas — the capability that a bare completion loop lacked. The [1m] alias
// carries the 1M context window so the TUI gauge uses the correct denominator.
// Efforts carries the adapter's levels, so /effort offers what the endpoint
// accepts; the default rides along only beside them, because the preflight
// refuses a default it has no levels to check against — and with no levels
// declared at all, the adapter's own default is the honest answer.
func (r *memberProviderResolver) Catalog() []provider.Descriptor {
	efforts := r.memberEffortVocabulary()
	d := provider.Descriptor{
		Ref:         r.ref,
		DisplayName: r.name,
		Model:       r.model,
		Tools:       true,
		Reasoning:   true,
		Efforts:     efforts,
	}
	if len(efforts) > 0 {
		d.DefaultEffort = r.effort
	}
	if r.context1M {
		d.ContextWindow = 1_000_000
	}
	return []provider.Descriptor{d}
}

// Resolve dials the member's endpoint. An explicit per-call effort wins over
// the pool entry's default; everything else is fixed by the entry.
func (r *memberProviderResolver) Resolve(sel provider.Selection) (provider.Provider, error) {
	effort := r.effort
	if sel.Effort != nil && strings.TrimSpace(*sel.Effort) != "" {
		effort = strings.TrimSpace(*sel.Effort)
	}
	extra := map[string]any{
		"effort":     effort,
		"proxy_spec": r.proxy,
	}
	if r.reasoningProtocol != "" {
		extra["reasoning_protocol"] = r.reasoningProtocol
	}
	if r.deepSeekAnthropic {
		extra["reasoning_protocol"] = config.ReasoningProtocolDeepSeek
		extra["thinking"] = "enabled"
		extra["anthropic_beta"] = "context-1m-2025-08-07"
	}
	if r.anthropicBearerHeader {
		extra["auth_header"] = true
	}
	return provider.New(r.kind, provider.Config{
		Name:    r.name,
		BaseURL: r.endpoint,
		Model:   r.model,
		APIKey:  r.apiKey,
		Extra:   extra,
	})
}

// memberCredentialError refuses assembly for a pool entry with no credential
// source at all. The member's credential is the pool entry's own — the
// resolver dials it directly and never falls back to the ambient config — so
// a missing key is that entry's fault, named here with the member and the
// entry, never the chat's own DeepSeek-default notice. A secret-store ref
// counts as a declared credential source.
func memberCredentialError(b team.MemberBinding, user team.AgentUser) error {
	if strings.TrimSpace(user.APIKey) == "" && user.SecretRef.StoreID == "" {
		return fmt.Errorf("member %q: agent user %q has no API key configured — set one in the pool before this member can send requests", b.MemberID, b.AgentUserRef)
	}
	return nil
}

// memberSystemPromptIdentity is the durable identity one member's Agent carries
// for its whole session (route §2.2): the team, the member instance, and the
// free-text role. It is folded into the cache-stable prefix once at assembly
// (boot.Options.SystemPromptIdentity), never rewritten mid-session. An unset
// role says so explicitly rather than leaving the member's specialty implied.
func memberSystemPromptIdentity(b team.MemberBinding) string {
	role := strings.TrimSpace(string(b.Role))
	if role == "" {
		role = "not configured"
	}
	identity := fmt.Sprintf(
		"You are member %q of team %q.\nYour team role is: %s.\nParticipate in the team's work as that role and specialty.",
		b.MemberID, b.Team, role)
	return identity + "\n" + team.CollaborationDiscipline(b.Leader)
}

// memberWriteLabel names one member in workspace-lease holder records and in
// write-queue reports: a teammate queued behind it should read a person, not a
// pid. Diagnostic only — no lease or token decision reads it.
func memberWriteLabel(memberID, teamName string) string {
	member := strings.TrimSpace(memberID)
	if member == "" {
		return ""
	}
	if team := strings.TrimSpace(teamName); team != "" {
		return "member " + member + " of team " + team
	}
	return "member " + member
}

// memberWorkspaceLeaseLabel labels this member's workspace-lease holder records.
// It shares the token's label, so a wait notice reads the same whether the peer
// was reached through the token or through the lease; an unbound member
// publishes nothing.
func memberWorkspaceLeaseLabel(b team.MemberBinding) string {
	return memberWriteLabel(b.MemberID, b.Team)
}

// memberProxySpec maps one member's resolved team proxy onto a transport spec.
// The team form is address-only (IP:port, no auth), so a disabled proxy is an
// explicit off rather than a fall-through to the ambient environment: a member
// must not silently inherit a proxy its team turned off.
func memberProxySpec(p team.ProxyConfig) netclient.ProxySpec {
	if !p.Enabled || strings.TrimSpace(p.Address) == "" {
		return netclient.ProxySpec{Mode: netclient.ModeOff}
	}
	return netclient.ProxySpec{Mode: netclient.ModeCustom, URL: "http://" + p.Address}
}

// memberBackendDeps is what assembling one member backend needs beyond its
// binding: the pool lookup, the event pump every member emits into, and
// the boot options this session launched with, so a member inherits the same
// permissions, workspace root and session directory as the ambient session.
type memberBackendDeps struct {
	ctx      context.Context
	users    memberPoolLookup
	store    *team.TeamStore
	sessions *team.TeamSessionStore
	tasks    *teamTaskService
	// events is the pump every member emits into: one bounded queue per member,
	// drained by the window's single pump goroutine. A nil pump keeps the
	// historical silence (a member with no frontend to report to).
	events *memberEventPump
	base   func() boot.Options
	// workspaceRoot is captured from the ambient controller when the overlay
	// opens: members created while the process CWD is elsewhere must still
	// resolve project skills against this root, not the session directory.
	workspaceRoot string
	// ambient reads the chat's own conversation for a leader's first member
	// session (see leaderAmbientCarry). It is captured by reference at registry
	// build time — nil by default, and never the bound member's own.
	ambient func() []provider.Message
	// release retires one member's assembled backend. Late-bound like tasks'
	// bind hook, because the registry is constructed around this builder.
	release func(teamName, memberID string)
	// escalations is the window-scoped decider for out-of-scope writes. Nil
	// leaves every write-access card on the operator's surface.
	escalations *writeAccessEscalations
	// owners is the canonical owner storage over the team data root. Nil keeps
	// the historical behavior — session-directory candidates only, no adoption.
	// A host that has a team data root always supplies one.
	owners *team.OwnerStore
	// conclusions is the member-side reader of other members' shared
	// conclusions. Nil leaves every member's executor without a board hook,
	// which is what a build with no board at all looks like.
	conclusions conclusionDeltaReader
}

// memberPoolLookup reads one pool entry. Narrowed to the one method the builder
// needs so tests can supply a pool without a store on disk.
type memberPoolLookup interface {
	AgentUser(id string) (team.AgentUser, bool, error)
}

// memberAgentUserFingerprint hashes the pool-entry identity a member backend
// bakes in at assembly: ref, provider, base url, model, effort and API key. Any
// change must invalidate the cached backend — the old one keeps serving the
// previous provider otherwise. The key is hashed, never stored or logged.
func memberAgentUserFingerprint(user team.AgentUser) string {
	h := sha256.New()
	for _, part := range []string{user.UserID, user.Provider, user.BaseURL, user.Model, user.Effort, user.APIKey} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// newMemberBackendFingerprint returns the fingerprint function the backend
// registry uses to invalidate a member's assembled backend when its pool entry
// changed. An unresolvable ref returns an error so the registry conservatively
// rebuilds (and surfaces the failure) instead of reusing a stale backend.
func newMemberBackendFingerprint(deps memberBackendDeps) func(team.MemberBinding) (string, error) {
	return func(b team.MemberBinding) (string, error) {
		ref := strings.TrimSpace(b.AgentUserRef)
		if ref == "" {
			return "", nil
		}
		user, ok, err := deps.users.AgentUser(ref)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("%w: %q (bound by member %q)", team.ErrAgentUserNotFound, ref, b.MemberID)
		}
		// The member's role and proxy are baked into its system-prompt identity
		// and transport at assembly too, so either change must invalidate.
		return memberAgentUserFingerprint(user) + "\x00" +
			string(b.Role) + "\x00" + proxyFingerprint(b.Proxy), nil
	}
}

// proxyFingerprint is the stable encoding of one member's resolved proxy for
// fingerprinting: only what memberProxySpec reads is significant.
func proxyFingerprint(p team.ProxyConfig) string {
	if !p.Enabled {
		return "off"
	}
	return "on\x00" + p.Address
}

// dryRunPoolEntry refuses a pool entry the member assembly could not use, by
// doing exactly what assembly does: resolve the provider and construct it. The
// adapter owns its own effort and model vocabulary (and those differ per model
// family), so asking it is the only check that cannot drift from the real one —
// a second whitelist here would be a second truth. Construction is network-free.
//
// An in-progress form (no provider or model yet) passes, matching the field
// validators. So does a provider name this build cannot resolve at all: whether
// a legacy provider may stay is the store's judgement (it preserves one until the
// user picks a legal option), and a kind with no registered adapter is the host
// binary's. This check only answers the one question the adapter owns.
func dryRunPoolEntry(u team.AgentUser) error {
	if strings.TrimSpace(u.Provider) == "" || strings.TrimSpace(u.Model) == "" {
		return nil
	}
	kind, _, err := team.ResolveAgentUserProvider(u)
	if err != nil || !slices.Contains(provider.Kinds(), kind) {
		return nil
	}
	resolver, err := newMemberProviderResolver(u, netclient.ProxySpec{})
	if err != nil {
		return nil
	}
	_, err = resolver.Resolve(provider.Selection{})
	return err
}

// memberApprovalPosture is the posture one role's backend runs with. The leader
// works unattended — the operator reads the transcript rather than answering a
// modal per write — while a member answers its own ordinary prompts and escalates
// only what its write scope does not cover. The fresh-human tools stay gated for
// both, whatever this returns.
func memberApprovalPosture(leader bool) string {
	if leader {
		return control.ToolApprovalYolo
	}
	return control.ToolApprovalAuto
}

// memberWriteRoots widens one role's build-time write scope. The leader works
// anywhere. A member adds the user state root, whose session stores,
// settings.json and runtime ledgers SessionDataGuard keeps denying whatever the
// roots say — the guard is a separate layer and is only lifted by an explicit
// config allow_write entry, never by a write root.
func memberWriteRoots(leader bool) []string {
	if leader {
		return []string{string(filepath.Separator)}
	}
	if root := strings.TrimSpace(config.MemoryUserDir()); root != "" {
		return []string{root}
	}
	return nil
}

// memberExtraTools is the per-role tool surface one member backend carries: the
// task board, the deliverable half, and (for a leader) the member registry and
// the escalation queue. The escalation constructor returns nil for anyone else,
// so a member never holds the surface that clears its own cards.
func memberExtraTools(deps memberBackendDeps, b team.MemberBinding, stderr io.Writer) []tool.Tool {
	tasks := deps.tasks.forTeam(b.Team)
	var out []tool.Tool
	if b.Leader && deps.store != nil {
		out = append(out, newLeaderMemberTools(deps.store, deps.sessions, b.Team, b.MemberID, deps.release)...)
		out = append(out, newLeaderTaskTools(tasks, b.Team, b.MemberID)...)
	} else {
		out = append(out, newMemberTaskTools(tasks, b.Team, b.MemberID)...)
	}
	// The deliverable surface needs no store handle of its own: it resolves the
	// fixed user state root, so both roles get their half whether or not this
	// build was handed a registry.
	if b.Leader {
		out = append(out, newLeaderDeliverableTools(b.Team, b.MemberID, stderr)...)
	} else {
		out = append(out, newMemberDeliverableTools(b.Team, b.MemberID, stderr)...)
	}
	return append(out, newLeaderApprovalTools(deps.escalations, b.Team, b.MemberID, b.Leader)...)
}

// newMemberBackendBuilder returns the assembly function teamBackends binds
// with: one member's pool entry becomes a full Agent backend — tools, memory,
// skills, hooks and trajectory included — pointed at that member's own session
// file. Assembly failure is returned so the caller can surface it and retry
// after the pool entry is fixed, rather than leaving a half-bound member.
func newMemberBackendBuilder(deps memberBackendDeps) func(team.MemberBinding) (control.SessionAPI, error) {
	return func(b team.MemberBinding) (control.SessionAPI, error) {
		ref := strings.TrimSpace(b.AgentUserRef)
		if ref == "" {
			return nil, fmt.Errorf("member %q has no agent user bound (and the team has no default)", b.MemberID)
		}
		user, ok, err := deps.users.AgentUser(ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: %q (bound by member %q)", team.ErrAgentUserNotFound, ref, b.MemberID)
		}
		if err := memberCredentialError(b, user); err != nil {
			return nil, err
		}
		resolver, err := newMemberProviderResolver(user, memberProxySpec(b.Proxy))
		if err != nil {
			return nil, err
		}

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
		opts.Sink = deps.events.sink(b.MemberID)
		opts.SystemPromptIdentity = memberSystemPromptIdentity(b) +
			// Invalid team_role declarations warn through the assembly's own
			// diagnostic writer (nil keeps the historical silence).
			teamRoleSkillPrompt(opts.TeamSkillsRoot, b.Leader, opts.Stderr)
		opts.ExtraTools = append(opts.ExtraTools, memberExtraTools(deps, b, opts.Stderr)...)
		ctrl, err := boot.Build(deps.ctx, opts)
		if err != nil {
			return nil, err
		}
		path, fresh, follower, err := bindMemberOwnerSession(deps, ctrl, b)
		if err != nil {
			return nil, err
		}
		if follower != nil {
			return follower, nil
		}
		// Publish the member's transcript identity for cross-window observation.
		if err := recordMemberOwnerHistory(deps.ctx, deps.owners, b, ctrl, false); err != nil {
			ctrl.Close()
			return nil, err
		}
		// bindMemberSession may resume a transcript whose leading system message
		// was assembled for an older role/proxy/skill configuration: keep the
		// conversation, refresh only that message, so identity stays correct.
		ctrl.SetSystemPromptPreservingHistory(ctrl.SystemPrompt())
		// A leader's first (file-less) entry continues from the chat's context;
		// anyone else starts or resumes its own. The seed loads straight from
		// boot.Build's history, so the member identity survives verbatim.
		if err := seedMemberAmbient(ctrl, path, fresh, b.Leader, deps.ambient); err != nil {
			ctrl.Close()
			return nil, err
		}
		// Re-publish after the seed: it replaces the transcript the bind resolved,
		// so the identity published above is stale — and a stale owner document
		// makes every later bind of this member a refused stale import.
		if err := recordMemberOwnerHistory(deps.ctx, deps.owners, b, ctrl, false); err != nil {
			ctrl.Close()
			return nil, err
		}
		// The member's own session must be writable by this controller alone
		// (members never share the host's lease), so acquire its write-authority
		// lease before the first submitted task (admission-6 gate).
		wl, err := bindMemberSessionAuthority(ctrl, path, true)
		if err != nil {
			// The legacy axis contends here rather than at import: it has no path
			// lease to import through, so another runtime's hold on the transcript
			// surfaces when this one asks for write authority.
			if isMemberWriterContention(err) {
				return newMemberFollower(deps, ctrl, b, err)
			}
			ctrl.Close()
			return nil, err
		}
		ctrl.EnableInteractiveApproval()
		// Order matters: EnableInteractiveApproval builds the gate from the posture
		// then in force, so setting the mode first would leave expansion flagged
		// non-interactive and deny an out-of-scope write as if headless.
		ctrl.SetToolApprovalMode(memberApprovalPosture(b.Leader))
		installMemberConclusionDelta(deps, b, ctrl)
		// Installed before the first turn reads it. A leader gets none: its scope
		// is the whole filesystem, so it raises no card to escalate, and a member
		// must never be able to answer its own.
		if esc := memberWriteAccessEscalator(deps.escalations, b.Team, b.MemberID, b.Leader); esc != nil {
			ctrl.SetWriteAccessEscalator(esc)
		}
		// This is the one place a writable member backend is built (every exit
		// above returned a follower), so starting the publisher here makes
		// "only the writer publishes usage" a property of the call graph.
		publisher := newMemberUsagePublisher(deps.owners, team.OwnerKey{TeamID: b.Team, MemberID: b.MemberID}, ctrl)
		publisher.Start()
		// Every post-bind step above is done, so this is the member's "runtime
		// is ready" moment. A member has nobody watching it, so a continuation
		// whose resumed turn never began would otherwise sit there forever.
		ctrl.RecoverUnstartedContinuation()
		return memberLeasedBackend{SessionAPI: ctrl, stop: wl, usage: publisher}, nil
	}
}

// bindMemberOwnerSession resolves the member's canonical session path and binds
// it, returning a read-only follower instead of an error when another runtime
// owns the writer or the bind landed on a stale import.
func bindMemberOwnerSession(deps memberBackendDeps, ctrl *control.Controller, b team.MemberBinding) (string, bool, control.SessionAPI, error) {
	roots, createRoot := memberSessionRoots(ctrl, deps.workspaceRoot)
	// Canonical owner storage comes first when the host has a team data root:
	// a history still in a session-directory candidate is adopted into the
	// member's owner directory once, and every later launch reads it there.
	ownerDir := ""
	if deps.owners != nil {
		dir, err := adoptMemberOwnerHistory(deps.ctx, ctrl, deps.owners, b, roots)
		if err != nil {
			ctrl.Close()
			return "", false, nil, err
		}
		ownerDir = dir
		roots, createRoot = memberSessionRootsWithOwner(ctrl, deps.workspaceRoot, ownerDir)
	}
	// The owner document may name a session this directory cannot offer: binding
	// it IS the member's history (see ownerSessionTakeover).
	switch took, err := ownerSessionTakeover(ctrl, deps.owners, b, ownerDir); {
	case err != nil && isMemberWriterContention(err):
		follower, ferr := newMemberFollower(deps, ctrl, b, err)
		return "", false, follower, ferr
	case err != nil:
		ctrl.Close()
		return "", false, nil, err
	case took:
		return "", false, nil, nil
	}
	path, fresh, err := bindMemberSession(ctrl, b.SessionFile, roots, createRoot)
	if err != nil {
		// Another runtime owns the writer: attach a read-only follower. Every
		// other failure stays visible, so a broken provider is never shown as
		// a read-only session.
		if isMemberWriterContention(err) {
			follower, ferr := newMemberFollower(deps, ctrl, b, err)
			return "", false, follower, ferr
		}
		ctrl.Close()
		return "", false, nil, err
	}
	// A clear republishes the owner stem while leaving the legacy transcript it
	// was imported from unchanged, so a re-import deterministically reproduces
	// the pre-rotation identity; the published stem wins over that import.
	if staleMemberImport(deps.owners, b, ctrl, path) {
		follower, ferr := newMemberFollower(deps, ctrl, b, errMemberStaleImport)
		return "", false, follower, ferr
	}
	return path, fresh, nil, nil
}

// memberLeasedBackend wraps one member's controller with its session lease so
// retiring the backend (release/evict/rebuild) releases the member's lease.
type memberLeasedBackend struct {
	control.SessionAPI
	stop *memberWriteLease
	// usage publishes this member's gauges for read-only windows elsewhere. It
	// is non-nil only on the writable path; a zero-valued backend (tests, a
	// hand-built mirror of the builder) leaves it nil, which Close tolerates.
	usage *memberUsagePublisher
}

// Close stops the controller first, then releases the member's session lease —
// the same order the ambient CLI retires its own controller, so no in-flight
// save races the release. The history becomes stealable only on retirement.
//
// The usage publisher closes first of all: it is stopped and waited for before
// the controller goes, so no observation can be published after this backend
// gave its session up.
func (b memberLeasedBackend) Close() {
	b.usage.Close()
	b.SessionAPI.Close()
	if b.stop != nil {
		b.stop.Close()
	}
}

// memberWriteLease is one member's session write-authority ownership. The
// ambient CLI keeps the chat's own session under its lease keeper
// (rebindSessionLease + BindControllerAuthority), so production writes require a
// live path-bound authority. A member backend persisted its own session file,
// so it must hold its own lease for that path — a controller can never write
// another runtime's session, not even the host's. Released when the backend
// closes, so a member's history is stealable the moment it is retired.
type memberWriteLease struct {
	leases *control.SessionLeaseKeeper
	strict bool
}

// bindMemberSessionAuthority acquires the member's own session lease and binds
// the controller's write authority to it. Originating the fix for the admission
// 6 refusal: a persisted member session with no bound authority is refused by
// the write-authority gate at the first submitted task. strict=false keeps
// headless/test hosts (no persistence, or a seam without a real controller)
// on the pre-fix permissive path.
//
// Under the v3-exclusive store there is no path lease to hold: the session file
// was consumed as an import source and frozen, so its identity is the
// controller's immutable SessionRef rather than a path. Production releases that
// lease at exactly this point (internal/serve/session_resume_commit.go,
// internal/cli/session_lease.go), and holding it would refuse every other
// runtime the frozen artifact. The authority binding still runs, with a nil
// lease: that is what installs the session-transition handler, and a nil lease
// clears a binding the v3 session never reads.
func bindMemberSessionAuthority(ctrl *control.Controller, path string, strict bool) (*memberWriteLease, error) {
	wl := &memberWriteLease{leases: control.NewSessionLeaseKeeper(), strict: strict}
	if !strict {
		return wl, nil
	}
	if !ctrl.UsesExclusiveSession() {
		if err := wl.leases.Rebind(path); err != nil {
			wl.leases.Release()
			return nil, fmt.Errorf("member session lease: %w", err)
		}
	}
	if err := wl.leases.BindControllerAuthority(ctrl); err != nil {
		wl.leases.Release()
		return nil, fmt.Errorf("member session write authority: %w", err)
	}
	return wl, nil
}

// Close releases the member's session lease when its backend closes.
func (w *memberWriteLease) Close() {
	if w == nil || w.leases == nil {
		return
	}
	w.leases.Release()
}

// bindMemberSession points a freshly built backend at the member's own session
// file and reports the path it settled on. Each candidate root is probed: the
// first holding the file is adopted, so history, checkpoints and recovery state
// come back whatever directory this build launched from, and a candidate that
// exists but cannot be read is a hard error, never papered over by a miss.
//
// Only a total miss is the member's first entry: the file is created under
// createRoot (this controller's canonical root), so the next launch finds it.
// createRoot is separate because it is also the highest-priority probe on v3 —
// pinning it last would hand a stale logical copy the priority the order exists
// to deny. fresh reports that branch, when a leader's ambient chat may be used.
//
// The axes differ: legacy executes its path, so Resume pins it; v3 has none, so
// both branches use the error-returning ContinueLegacySession / BindFreshSession
// rather than a logged no-op — an unreachable identity refuses visibly.
func bindMemberSession(ctrl *control.Controller, name string, roots []string, createRoot string) (string, bool, error) {
	if len(roots) == 0 {
		return "", false, fmt.Errorf("member session %q has no candidate session root", name)
	}
	if createRoot == "" {
		createRoot = roots[len(roots)-1]
	}
	exclusive := ctrl.UsesExclusiveSession()
	for _, root := range roots {
		path := filepath.Join(root, name)
		if _, err := os.Stat(path); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return "", false, err
			}
			continue
		}
		if exclusive {
			if _, err := ctrl.ContinueLegacySession(context.Background(), path, ""); err != nil {
				return "", false, fmt.Errorf("member session %q: import %s: %w", name, path, err)
			}
			return path, false, nil
		}
		session, err := loadResumableSession(path)
		if err != nil {
			return "", false, err
		}
		ctrl.Resume(session, path)
		return path, false, nil
	}
	path := filepath.Join(createRoot, name)
	// The store the file lives in may not exist yet on a first launch.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	if exclusive {
		if _, err := ctrl.BindFreshSession(context.Background(), ""); err != nil {
			return "", false, fmt.Errorf("member session %q: bind fresh: %w", name, err)
		}
		return path, true, nil
	}
	ctrl.SetSessionPath(path)
	return path, true, nil
}

// leaderAmbientCarry turns the ambient chat history into the messages a
// leader's first member session continues from. The carried system message is
// replaced by the member's own, so the team agent speaks with its member
// identity rather than whatever profile launched the chat. An empty carrier
// or a member built around a larger system prompt (extra leader tools) keeps
// the splice side: put the booted prefix first, retire the generated System.
func leaderAmbientCarry(ambient []provider.Message, memberPrefix []provider.Message) []provider.Message {
	if len(ambient) == 0 {
		return nil
	}
	out := make([]provider.Message, 0, len(ambient)+1)
	for _, msg := range memberPrefix {
		if msg.Role == provider.RoleSystem {
			if len(out) == 0 {
				out = append(out, msg) // the member identity stays the head
				continue
			}
			break // one prefix system message is enough; retire the rest
		}
		out = append(out, msg)
	}
	for _, msg := range ambient {
		if msg.Role == provider.RoleSystem {
			continue // the member's own identity already opened the transcript
		}
		out = append(out, msg)
	}
	return out
}

// seedMemberAmbient hands a leader's first (file-less) member session the
// chat's own conversation and persists it, so the file's existence — not a
// flag — stops a second handover on any reopen. Never for non-leaders or a
// resuming session. Failure reaches the caller: a leader session that cannot
// persist its seed must not register a backend that pretends to have it.
func seedMemberAmbient(ctrl *control.Controller, path string, fresh, leader bool, ambient func() []provider.Message) error {
	if !fresh || !leader || ambient == nil {
		return nil
	}
	carry := leaderAmbientCarry(ambient(), ctrl.History())
	if len(carry) == 0 {
		return nil
	}
	ctrl.AdoptHistory(carry, path)
	return ctrl.Snapshot()
}
