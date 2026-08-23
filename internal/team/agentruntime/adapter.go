package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"reasonix/internal/team"
)

// Registry errors.
var (
	// ErrInstanceNotFound reports an operation on an instance key that was
	// never started.
	ErrInstanceNotFound = errors.New("agentruntime: no such member instance")
	// ErrNotAssembled reports a runtime-only operation (subscribe) on an
	// instance whose provider assembly was not configured.
	ErrNotAssembled = errors.New("agentruntime: member instance has no runtime")
)

// Snapshot is the read-only observation of one member instance (route §3:
// the CLI is a display window). It carries copies, never references into
// mutable instance state, and never carries key material (K1).
type Snapshot struct {
	Key      InstanceKey
	State    RuntimeState
	Role     string // assembled role for display; system prompt itself is not exposed
	Messages []team.SessionMessage
	Cursor   team.SessionCursor
}

// Instance is one member's runtime instance (route §2.1): its own assembly
// snapshot, lifecycle state, recovery cursor, and — when a ProviderFactory is
// wired — the assembled member runtime. Instances sharing an AgentUserRef
// share nothing mutable — each owns this struct.
type Instance struct {
	key    InstanceKey
	spec   Spec
	state  RuntimeState
	cursor team.SessionCursor
	rt     MemberRuntime
}

// Key returns the instance's composite key.
func (i *Instance) Key() InstanceKey { return i.key }

// Registry is the member-level runtime adapter (route §3 TeamRuntimeRegistry):
// it maps the (team, memberID) key to an independent instance, restores and
// persists per-member state through the session store, and exposes the
// lifecycle and observation surface the CLI window drives. The provider
// assembly is injected through the factory (route §11.2: provider calls stay
// inside the runtime adapter); without a factory the registry runs in
// state-only mode — Send records history without executing a loop.
type Registry struct {
	mu      sync.Mutex
	store   *team.TeamSessionStore
	factory ProviderFactory
	active  map[InstanceKey]*Instance
	current InstanceKey // CLI session-window target; zero value = none
}

// NewRegistry returns an empty registry backed by store. An optional factory
// assembles each instance's member runtime from its spec (credential
// resolution happens in the wired factory, never inside this package).
func NewRegistry(store *team.TeamSessionStore, factory ...ProviderFactory) *Registry {
	r := &Registry{store: store, active: make(map[InstanceKey]*Instance)}
	if len(factory) > 0 {
		r.factory = factory[0]
	}
	return r
}

// Start assembles and starts the member instance for key. An existing
// instance is reused (idempotent) with its new spec applied as the next
// assembly snapshot — reconfiguring an AgentUserRef never resets history or
// the recovery cursor (route §2.1). A fresh instance restores its cursor and
// state from the session store; an absent member directory is empty history.
// When a factory is wired, a fresh instance assembles its member runtime
// (provider resolution); assembly failure surfaces as an error so the caller
// can stay in the session window and offer the retry entry (route §11.6).
// ctx is the future agent-loop context; this layer returns immediately.
func (r *Registry) Start(ctx context.Context, spec Spec) (*Instance, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateKey(spec.Key); err != nil {
		return nil, err
	}
	if inst, ok := r.active[spec.Key]; ok {
		inst.spec = spec
		// restart (§11.6): a stopped instance comes back running on the next
		// Start, persisting it so recovery never finds a live window on a
		// stopped member; the reused runtime starts on the next Send.
		if inst.state == RuntimeStateStopped {
			inst.state = RuntimeStateRunning
			if err := r.store.WriteState(spec.Key.Team, spec.Key.MemberID, string(RuntimeStateRunning)); err != nil {
				return nil, err
			}
		}
		return inst, nil
	}
	cursor, err := r.store.ReadCursor(spec.Key.Team, spec.Key.MemberID)
	if err != nil {
		return nil, err
	}
	var rt MemberRuntime
	if r.factory != nil {
		prov, err := r.factory(spec)
		if err != nil {
			return nil, fmt.Errorf("agentruntime: assemble %s/%s: %w", spec.Key.Team, spec.Key.MemberID, err)
		}
		rt = NewMemberAgent(spec.Key, spec, r.store, prov, cursor.Sequence)
	}
	inst := &Instance{key: spec.Key, spec: spec, state: RuntimeStateRunning, cursor: cursor, rt: rt}
	r.active[spec.Key] = inst
	if err := r.store.WriteState(spec.Key.Team, spec.Key.MemberID, string(RuntimeStateRunning)); err != nil {
		delete(r.active, spec.Key)
		return nil, err
	}
	if r.current == (InstanceKey{}) {
		r.current = spec.Key
	}
	return inst, nil
}

// Stop stops the instance, persists its stopped state, and cancels any
// in-flight completion (outside the registry lock, so one member's long
// loop never blocks another's lifecycle). An unknown key is refused;
// stopping an already-stopped instance is a no-op.
func (r *Registry) Stop(key InstanceKey) error {
	r.mu.Lock()
	inst, err := r.instanceLocked(key)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	if inst.state == RuntimeStateStopped {
		r.mu.Unlock()
		return nil
	}
	inst.state = RuntimeStateStopped
	err = r.store.WriteState(key.Team, key.MemberID, string(RuntimeStateStopped))
	rt := inst.rt
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if rt != nil {
		return rt.Stop()
	}
	return nil
}

// StopTeam stops every running instance of one team — the pre-clear gate for
// k/delete/team-cleanup (route §11.6): no member runtime may keep writing
// context while its team's contexts are being cleared. Instances stop through
// Registry.Stop, so state persistence and loop cancellation stay in one path;
// unknown teams are a no-op.
func (r *Registry) StopTeam(teamName string) error {
	r.mu.Lock()
	var keys []InstanceKey
	for key, inst := range r.active {
		if key.Team == teamName && inst.state != RuntimeStateStopped {
			keys = append(keys, key)
		}
	}
	r.mu.Unlock()
	var firstErr error
	for _, key := range keys {
		if err := r.Stop(key); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Switch moves the session window to key and returns its observation; an
// unknown key is refused.
func (r *Registry) Switch(key InstanceKey) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, err := r.instanceLocked(key)
	if err != nil {
		return Snapshot{}, err
	}
	r.current = key
	return r.observeLocked(inst), nil
}

// Current returns the session-window target; the second result reports
// whether any instance was ever started.
func (r *Registry) Current() (InstanceKey, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current, r.current != (InstanceKey{})
}

// Status returns the instance's lifecycle state.
func (r *Registry) Status(key InstanceKey) (RuntimeState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, err := r.instanceLocked(key)
	if err != nil {
		return "", err
	}
	return inst.state, nil
}

// Observe returns a read-only snapshot of the instance; an unknown key is
// refused.
func (r *Registry) Observe(key InstanceKey) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, err := r.instanceLocked(key)
	if err != nil {
		return Snapshot{}, err
	}
	return r.observeLocked(inst), nil
}

// observeLocked assembles the observation snapshot under the lock: history
// from the session store, cursor and state from the instance.
func (r *Registry) observeLocked(inst *Instance) Snapshot {
	msgs, err := r.store.Messages(inst.key.Team, inst.key.MemberID)
	if err != nil {
		msgs = nil
	}
	return Snapshot{
		Key:      inst.key,
		State:    inst.state,
		Role:     inst.spec.Role,
		Messages: msgs,
		Cursor:   inst.cursor,
	}
}

// Send submits one interaction message from the CLI window into the member's
// history and, when the instance has an assembled runtime, starts the
// completion loop (route §11.3: the user message is persisted first; a
// failure keeps it as retryable history). Without a runtime — state-only
// mode, or an instance stopped before assembly — the message is recorded for
// the next assembly without executing anything. The cursor is not advanced
// here; the loop advances it together with its persisted assistant message.
func (r *Registry) Send(key InstanceKey, text string) error {
	r.mu.Lock()
	inst, err := r.instanceLocked(key)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	rt := inst.rt
	r.mu.Unlock()
	if err := r.store.AppendMessage(key.Team, key.MemberID, team.SessionMessage{
		Kind: "user",
		From: "cli",
		Text: text,
		TS:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	if rt == nil {
		return nil
	}
	return rt.Send(text)
}

// Subscribe returns the member's bounded runtime event stream; an instance
// without an assembled runtime is refused. The subscription's Cancel
// unsubscribes it.
func (r *Registry) Subscribe(key InstanceKey) (Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, err := r.instanceLocked(key)
	if err != nil {
		return Subscription{}, err
	}
	if inst.rt == nil {
		return Subscription{}, fmt.Errorf("%w: (%s, %s)", ErrNotAssembled, key.Team, key.MemberID)
	}
	return inst.rt.Subscribe(), nil
}

// MarkConsumed advances the instance's recovery cursor to the current
// history length — the point a runtime consumer has reached. Cursors are
// per-instance: consuming one member's history never moves another's.
func (r *Registry) MarkConsumed(key InstanceKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, err := r.instanceLocked(key)
	if err != nil {
		return err
	}
	msgs, err := r.store.Messages(key.Team, key.MemberID)
	if err != nil {
		return err
	}
	inst.cursor.Cursor = len(msgs)
	inst.cursor.ResumeCount++
	return r.store.WriteCursor(key.Team, key.MemberID, inst.cursor)
}

// Instances lists the started instance keys in team-then-member order.
func (r *Registry) Instances() []InstanceKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]InstanceKey, 0, len(r.active))
	for k := range r.active {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Team != keys[j].Team {
			return keys[i].Team < keys[j].Team
		}
		return keys[i].MemberID < keys[j].MemberID
	})
	return keys
}

// Close stops every instance — persisting each one's stopped state, then
// cancelling loops and closing event sources outside the registry lock.
func (r *Registry) Close() error {
	r.mu.Lock()
	var firstErr error
	var runtimes []MemberRuntime
	for key, inst := range r.active {
		if inst.state == RuntimeStateStopped {
			continue
		}
		inst.state = RuntimeStateStopped
		if err := r.store.WriteState(key.Team, key.MemberID, string(RuntimeStateStopped)); err != nil && firstErr == nil {
			firstErr = err
		}
		if inst.rt != nil {
			runtimes = append(runtimes, inst.rt)
		}
	}
	r.mu.Unlock()
	for _, rt := range runtimes {
		rt.Close()
	}
	return firstErr
}

// instanceLocked returns the live instance for key; callers hold r.mu.
func (r *Registry) instanceLocked(key InstanceKey) (*Instance, error) {
	inst, ok := r.active[key]
	if !ok {
		return nil, fmt.Errorf("%w: (%s, %s)", ErrInstanceNotFound, key.Team, key.MemberID)
	}
	return inst, nil
}

// validateKey refuses empty team or member ids at assembly time, so no
// instance can ever be registered against a key the session store rejects.
func validateKey(key InstanceKey) error {
	if err := validateKeyPart(key.Team); err != nil {
		return err
	}
	return validateKeyPart(key.MemberID)
}

func validateKeyPart(id string) error {
	if id == "" {
		return fmt.Errorf("agentruntime: instance key %q must not be empty", id)
	}
	return nil
}
