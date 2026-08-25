package team

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"
)

// BindStatus names the single state a member binding may be in (route §4.2):
// one enum field, never a pair of booleans — unbound|bound|transitioning
// are mutually exclusive by construction.
type BindStatus string

const (
	BindStatusUnbound       BindStatus = "unbound"
	BindStatusBound         BindStatus = "bound"
	BindStatusTransitioning BindStatus = "transitioning"
)

// BindRecord is the server-side binding of one member window to one leader
// session (route §4.1). Generation is the member's window generation: a
// newer generation replaces the record, an older one is gated.
type BindRecord struct {
	MemberID   string
	LeaderID   string
	Generation uint64
	Status     BindStatus
	TaskID     TaskID
	BoundAt    time.Time
}

// Handoff carries what leaves a member's private context on unbind: a
// digest plus artifact pointers, never the artifacts themselves (route §4.3).
type Handoff struct {
	TaskID       TaskID
	Digest       string
	ArtifactRefs []ArtifactRef
	Pending      string
}

// Binding errors. The registry never leaves a half-bound state behind: a
// failed Unbind stays bound, an expired transition rolls back.
var (
	ErrNotBound       = errors.New("team: member is not bound")
	ErrBindConflict   = errors.New("team: bind conflict: another leader or task holds the member")
	ErrInvalidHandoff = errors.New("team: invalid handoff: task id, digest or artifact pointers")
	ErrInvalidTask    = errors.New("team: bind requires a task id")
)

// transitionTimeout is the max lifetime of an in-flight bind/unbind
// transition (route §4.2); past it the registry rolls back.
const transitionTimeout = 5 * time.Second

// BindingRegistry is the in-memory binding state machine (route §4.2):
// unbound --Bind--> bound --Unbind--> unbound, with a transitioning window
// that rolls back on timeout. A non-nil persist writes every record change
// to durable storage, so a restarted server restores the same generations
// instead of invalidating live windows (route §4.3).
type BindingRegistry struct {
	mu      sync.Mutex
	records map[string]bindingState
	persist func(BindRecord) error
}

// bindingState is one member's record plus an optional in-flight transition.
// pending != nil marks transitioning; a transition past its deadline rolls
// back to prev on the next touch (route §4.2).
type bindingState struct {
	rec     BindRecord
	pending *pendingTransition
}

// pendingTransition is the named sub-state of an operation in flight: the
// record before the operation and when the transition expires.
type pendingTransition struct {
	prev     *BindRecord
	deadline time.Time
}

// NewBindingRegistry returns a registry with no bound members and no
// persistence.
func NewBindingRegistry() *BindingRegistry {
	return &BindingRegistry{
		records: make(map[string]bindingState),
	}
}

// NewBindingRegistryWithPersister returns a registry that durably stores
// every record change through persister. A nil persister keeps the
// registry purely in memory.
func NewBindingRegistryWithPersister(p BindingPersister) *BindingRegistry {
	r := NewBindingRegistry()
	if p != nil {
		r.persist = func(rec BindRecord) error {
			return p.SaveBinding(context.Background(), rec)
		}
	}
	return r
}

// Restore loads previously persisted records after a server restart (route
// §4.3). Generation and status come back unchanged — a restart never bumps
// generations. In-flight transitions were never persisted and die with the
// process, so they are skipped.
func (r *BindingRegistry) Restore(records []BindRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range records {
		if rec.Status == BindStatusTransitioning {
			continue
		}
		r.records[rec.MemberID] = bindingState{rec: rec}
	}
}

// Bind binds member to leader for task, stamping Generation from the
// server-issued identity. A same-generation rebind of the same leader and
// task is an idempotent replay; a higher generation replaces the record
// (leader take-over); a same-generation different leader or task conflicts.
func (r *BindingRegistry) Bind(memberID string, id Identity, task TaskID) (BindRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if task == "" {
		return BindRecord{}, ErrInvalidTask
	}
	st, ok := r.records[memberID]
	r.rollbackExpiredLocked(&st)
	if ok {
		if id.Generation < st.rec.Generation {
			return BindRecord{}, ErrStaleGeneration
		}
		if st.rec.Status == BindStatusBound && id.Generation == st.rec.Generation {
			if st.rec.LeaderID != id.MemberID {
				return BindRecord{}, ErrBindConflict
			}
			if st.rec.TaskID != task {
				return BindRecord{}, ErrBindConflict
			}
			return st.rec, nil // idempotent replay
		}
	}
	rec := BindRecord{
		MemberID:   memberID,
		LeaderID:   id.MemberID,
		Generation: id.Generation,
		Status:     BindStatusBound,
		TaskID:     task,
		BoundAt:    time.Now(),
	}
	// Persist before committing the memory state: a failed write leaves
	// the member where it was — the record never half-exists.
	if r.persist != nil {
		if err := r.persist(rec); err != nil {
			return BindRecord{}, err
		}
	}
	r.records[memberID] = bindingState{rec: rec}
	return rec, nil
}

// Unbind unbinds member, requiring a handoff that matches the bound task.
// An invalid handoff fails while the member stays bound (no half-unbind);
// unbinding an already-unbound member with the same task is an idempotent
// replay, an unknown member is ErrNotBound.
func (r *BindingRegistry) Unbind(memberID string, id Identity, h Handoff) (BindRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.records[memberID]
	r.rollbackExpiredLocked(&st)
	if !ok || st.rec.Status == BindStatusUnbound {
		if ok && h.TaskID != "" && h.TaskID == st.rec.TaskID {
			return st.rec, nil // idempotent replay
		}
		return BindRecord{}, ErrNotBound
	}
	if id.Generation < st.rec.Generation {
		return BindRecord{}, ErrStaleGeneration
	}
	if err := validateHandoff(h, st.rec.TaskID); err != nil {
		return st.rec, err // stays bound
	}
	rec := st.rec
	rec.Status = BindStatusUnbound
	// Persist before committing: a failed write keeps the member bound
	// (no half-unbind), matching the in-memory rollback contract.
	if r.persist != nil {
		if err := r.persist(rec); err != nil {
			return st.rec, err
		}
	}
	r.records[memberID] = bindingState{rec: rec}
	return rec, nil
}

// GetBind returns the member's current record; an expired transition is
// rolled back first, so callers never observe a stale transitioning state.
func (r *BindingRegistry) GetBind(memberID string) (BindRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.records[memberID]
	if !ok {
		return BindRecord{}, false
	}
	r.rollbackExpiredLocked(&st)
	r.records[memberID] = st
	return st.rec, true
}

// All returns every member's current record, for roster rendering and
// server snapshots.
func (r *BindingRegistry) All() []BindRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BindRecord, 0, len(r.records))
	for mid, st := range r.records {
		r.rollbackExpiredLocked(&st)
		r.records[mid] = st
		out = append(out, st.rec)
	}
	return out
}

// rollbackExpiredLocked reverts a transition whose deadline has passed
// back to its previous record, so a crashed operation never leaves a
// half-bound state behind.
func (r *BindingRegistry) rollbackExpiredLocked(st *bindingState) {
	if st.pending == nil {
		return
	}
	if time.Now().Before(st.pending.deadline) {
		return
	}
	if st.pending.prev != nil {
		st.rec = *st.pending.prev
	}
	st.pending = nil
}

// validateHandoff enforces route §4.3: the handoff must carry the bound
// task id, a digest within the token budget, and artifact pointers that
// name a path.
func validateHandoff(h Handoff, task TaskID) error {
	if h.TaskID == "" || h.TaskID != task {
		return ErrInvalidHandoff
	}
	if utf8.RuneCountInString(h.Digest) > 200 {
		return ErrInvalidHandoff
	}
	for _, ref := range h.ArtifactRefs {
		if ref.Path == "" {
			return ErrInvalidHandoff
		}
	}
	return nil
}
