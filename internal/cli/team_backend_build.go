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
	"reasonix/internal/control"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/skill"
	"reasonix/internal/team"
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
	proxy                 netclient.ProxySpec
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
	deepSeekAnthropic := kind == "anthropic" && team.NormalizeProvider(u.Provider) == team.ProviderDeepSeek && strings.HasSuffix(strings.ToLower(model), "[1m]")
	if deepSeekAnthropic {
		// Claude treats [1m] as a client-side context alias. Its wire request
		// removes the suffix and enables the 1M context beta separately; sending
		// the alias as the model name makes compatible gateways reject routing.
		model = strings.TrimSpace(model[:len(model)-len("[1m]")])
	}
	return &memberProviderResolver{
		ref:      memberModelRef(name, u.Model),
		name:     name,
		kind:     kind,
		endpoint: endpoint,
		model:    model,
		apiKey:   u.APIKey,
		effort:   strings.TrimSpace(u.Effort),
		proxy:    proxy,

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

// Ref is the model ref boot.Options.Model must carry for this member.
func (r *memberProviderResolver) Ref() string { return r.ref }

// Catalog reports the one entry this resolver owns. Tools and Reasoning are
// declared: a member is a full Agent, so the assembled request carries the tool
// schemas — the capability that a bare completion loop lacked.
func (r *memberProviderResolver) Catalog() []provider.Descriptor {
	return []provider.Descriptor{{
		Ref:           r.ref,
		DisplayName:   r.name,
		Model:         r.model,
		Tools:         true,
		Reasoning:     true,
		DefaultEffort: r.effort,
	}}
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
	if r.deepSeekAnthropic {
		extra["reasoning_protocol"] = "deepseek"
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
// binding: the pool lookup, the tagged event channel every member shares, and
// the boot options this session launched with, so a member inherits the same
// permissions, workspace root and session directory as the ambient session.
type memberBackendDeps struct {
	ctx      context.Context
	users    memberPoolLookup
	store    *team.TeamStore
	sessions *team.TeamSessionStore
	tasks    *teamTaskService
	events   chan memberEvent
	base     func() boot.Options
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
		opts.Model = resolver.Ref()
		opts.ProviderResolver = resolver
		opts.Sink = memberSink(b.MemberID, deps.events)
		opts.SystemPromptIdentity = memberSystemPromptIdentity(b) + teamRoleSkillPrompt(opts.WorkspaceRoot, b.Leader)
		tasks := deps.tasks.forTeam(b.Team)
		if b.Leader && deps.store != nil {
			opts.ExtraTools = append(opts.ExtraTools, newLeaderMemberTools(deps.store, deps.sessions, b.Team, b.MemberID)...)
			opts.ExtraTools = append(opts.ExtraTools, newLeaderTaskTools(tasks, b.Team, b.MemberID)...)
		} else {
			opts.ExtraTools = append(opts.ExtraTools, newMemberTaskTools(tasks, b.Team, b.MemberID)...)
		}
		ctrl, err := boot.Build(deps.ctx, opts)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(ctrl.SessionDir(), b.SessionFile)
		if err := bindMemberSession(ctrl, path); err != nil {
			ctrl.Close()
			return nil, err
		}
		// The member's own session must be writable by this controller alone
		// (members never share the host's lease), so acquire its write-authority
		// lease before the first submitted task (admission-6 gate).
		wl, err := bindMemberSessionAuthority(ctrl, path, true)
		if err != nil {
			ctrl.Close()
			return nil, err
		}
		ctrl.EnableInteractiveApproval()
		return memberLeasedBackend{SessionAPI: ctrl, stop: wl}, nil
	}
}

// memberLeasedBackend wraps one member's controller with its session lease so
// retiring the backend (release/evict/rebuild) releases the member's lease.
type memberLeasedBackend struct {
	control.SessionAPI
	stop *memberWriteLease
}

// Close stops the controller first, then releases the member's session lease —
// the same order the ambient CLI retires its own controller, so no in-flight
// save races the release. The history becomes stealable only on retirement.
func (b memberLeasedBackend) Close() {
	b.SessionAPI.Close()
	if b.stop != nil {
		b.stop.Close()
	}
}

// teamRoleSkillPrompt loads the role playbook at backend assembly time. The
// skill remains discoverable in /skills, while its body is also present from
// the first team turn so role obligations do not depend on a model remembering
// to call run_skill. Missing files are a no-op for compatibility with tests and
// workspaces that do not install the optional playbooks.
func teamRoleSkillPrompt(root string, leader bool) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	name := "member"
	if leader {
		name = "leader"
	}
	if sk, ok := skill.New(skill.Options{ProjectRoot: root, Stderr: io.Discard}).Read(name); ok {
		body := strings.TrimSpace(sk.Body)
		if body != "" {
			return "\n\n<team-role-skill name=\"" + name + "\">\n" + body + "\n</team-role-skill>"
		}
	}
	return ""
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
func bindMemberSessionAuthority(ctrl *control.Controller, path string, strict bool) (*memberWriteLease, error) {
	wl := &memberWriteLease{leases: control.NewSessionLeaseKeeper(), strict: strict}
	if !strict {
		return wl, nil
	}
	if err := wl.leases.Rebind(path); err != nil {
		wl.leases.Release()
		return nil, fmt.Errorf("member session lease: %w", err)
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
// file: an existing file is resumed so the member's history, checkpoints and
// recovery state come back, and an absent one is simply the member's first
// entry — empty history, never corruption.
func bindMemberSession(ctrl *control.Controller, path string) error {
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		ctrl.SetSessionPath(path)
		return nil
	}
	session, err := loadResumableSession(path)
	if err != nil {
		return err
	}
	ctrl.Resume(session, path)
	return nil
}
