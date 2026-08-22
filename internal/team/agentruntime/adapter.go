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
// snapshot, lifecycle state, and recovery cursor. Instances sharing an
// AgentUserRef share nothing mutable — each owns this struct.
type Instance struct {
	key    InstanceKey
	spec   Spec
	state  RuntimeState
	cursor team.SessionCursor
}

// Key returns the instance's composite key.
func (i *Instance) Key() InstanceKey { return i.key }

// Registry is the member-level runtime adapter (route §3 TeamRuntimeRegistry):
// it maps the (team, memberID) key to an independent instance, restores and
// persists per-member state through the session store, and exposes the
// lifecycle and observation surface the CLI window drives. The agent loop
// itself remains deferred (§3.7); this layer owns assembly, isolation, and
// the durable side of the instance lifecycle.
type Registry struct {
	mu      sync.Mutex
	store   *team.TeamSessionStore
	active  map[InstanceKey]*Instance
	current InstanceKey // CLI session-window target; zero value = none
}

// NewRegistry returns an empty registry backed by store.
func NewRegistry(store *team.TeamSessionStore) *Registry {
	return &Registry{store: store, active: make(map[InstanceKey]*Instance)}
}

// Start assembles and starts the member instance for key. An existing
// instance is reused (idempotent) with its new spec applied as the next
// assembly snapshot — reconfiguring an AgentUserRef never resets history or
// the recovery cursor (route §2.1). A fresh instance restores its cursor and
// state from the session store; an absent member directory is empty history.
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
		return inst, nil
	}
	cursor, err := r.store.ReadCursor(spec.Key.Team, spec.Key.MemberID)
	if err != nil {
		return nil, err
	}
	inst := &Instance{key: spec.Key, spec: spec, state: RuntimeStateRunning, cursor: cursor}
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

// Stop stops the instance and persists its stopped state. An unknown key is
// refused; stopping an already-stopped instance is a no-op.
func (r *Registry) Stop(key InstanceKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, err := r.instanceLocked(key)
	if err != nil {
		return err
	}
	if inst.state == RuntimeStateStopped {
		return nil
	}
	inst.state = RuntimeStateStopped
	return r.store.WriteState(key.Team, key.MemberID, string(RuntimeStateStopped))
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

// Send enqueues one interaction message from the CLI window into the
// member's history (route §3: the CLI is an interaction window). The message
// is recorded even when the instance is stopped — it becomes part of the
// history the next assembly resumes from. The cursor is not advanced;
// MarkConsumed records consumption.
func (r *Registry) Send(key InstanceKey, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.instanceLocked(key); err != nil {
		return err
	}
	return r.store.AppendMessage(key.Team, key.MemberID, team.SessionMessage{
		Kind: "user",
		From: "cli",
		Text: text,
		TS:   time.Now().UTC().Format(time.RFC3339),
	})
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

// Close stops every instance, persisting each one's stopped state.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for key, inst := range r.active {
		if inst.state == RuntimeStateStopped {
			continue
		}
		inst.state = RuntimeStateStopped
		if err := r.store.WriteState(key.Team, key.MemberID, string(RuntimeStateStopped)); err != nil && firstErr == nil {
			firstErr = err
		}
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
