package control

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// Cross-window history observation: one writer runtime owns a canonical owner
// transcript, but a second window may hold a controller on the same owner and
// must notice an append, clear or branch without a lease or a re-enter.

// historyStampDurable is the v3 stamp shape: the immutable session identity
// plus the durable sequence. Identity is part of the stamp because a rotated
// session (clear/new/branch) reuses the owner directory, so a bare sequence
// would alias a fresh session's low sequence onto the previous one's.
func historyStampDurable(ref session.SessionRef, sequence uint64) string {
	if ref.SessionID == "" {
		return ""
	}
	return ref.SessionID + ":" + strconv.FormatUint(sequence, 10)
}

// controllerHistorySync is the cross-window observation substate: the durable
// history identity this controller last adopted. It is one named substate
// rather than a bare field so the controller's scalar-state product stays
// small and the guard is obvious.
type controllerHistorySync struct {
	mu   sync.Mutex
	last string
}

// lastHistoryStamp and setLastHistoryStamp keep the observed stamp under its
// own lock without holding it across the reload: RuntimeStatus and the history
// read take controller locks of their own.
func (s *controllerHistorySync) lastHistoryStamp() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *controllerHistorySync) setLastHistoryStamp(stamp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = stamp
}

// HistoryStamp returns an opaque identity of the controller's current durable
// history. Equal stamps mean nothing observable changed; "" means the session
// has no readable history yet. It never takes a session or writer lease: the v3
// path reads the service's published query surface, and the legacy path only
// stats the transcript file.
func (c *Controller) HistoryStamp() string {
	if c == nil || c.executor == nil {
		return ""
	}
	if service, runtime, exclusive := c.v3Binding(); exclusive && service != nil && runtime != nil {
		ref := runtime.Ref()
		if recent, err := service.Query().Recent(context.Background(), ref); err == nil {
			return historyStampDurable(ref, recent.DurableSequence)
		}
		// A live runtime whose durable snapshot is still preparing is still
		// readable through its accepted sequence, which is monotonic and
		// identical to the durable one once the snapshot catches up.
		return historyStampDurable(ref, runtime.Session().EventSequence())
	}
	path := strings.TrimSpace(c.SessionPath())
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	// The legacy transcript is appended in place, so size and mtime together
	// are its revision; the branch id is included so a rotation to a different
	// transcript cannot alias it.
	return strings.Join([]string{
		agent.BranchID(path),
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}, ":")
}

// historySyncBusy reports whether this controller must not replace its own
// transcript: a running turn, an unanswered prompt or a background job owns
// the in-memory state, and adopting the durable view underneath it would drop
// local input and the in-flight turn. A busy writer never self-reloads; it is
// the writer, so its own view is the authoritative one anyway.
func (c *Controller) historySyncBusy() bool {
	if c == nil {
		return true
	}
	status := c.RuntimeStatus()
	return status.Running || status.PendingPrompt || status.BackgroundJobs > 0
}

// ReloadHistoryIfChanged replaces this controller's transcript with the
// session's current durable history when stamp differs from the one this
// controller last observed, and reports whether it reloaded. An unchanged
// stamp is a no-op, which is what keeps the 1s polling call cheap.
//
// It refuses to run while the runtime is busy (see historySyncBusy) and
// returns false without recording the stamp, so the next idle observation
// still sees the change. It never acquires, moves or releases a session lease,
// and never writes: the durable log is the only source it reads.
//
// ctx bounds the read: the durable page is served by the session's history
// index, which can rebuild itself over the whole log, and a caller on a UI
// goroutine must be able to give up rather than block on it.
func (c *Controller) ReloadHistoryIfChanged(ctx context.Context, stamp string) (bool, error) {
	if c == nil || c.executor == nil || strings.TrimSpace(stamp) == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.lastHistoryStamp() == stamp {
		return false, nil
	}
	if c.historySyncBusy() {
		return false, nil
	}
	messages, err := c.durableHistoryMessages(ctx)
	if err != nil {
		return false, err
	}
	c.setLastHistoryStamp(stamp)
	// The writer's own durable log round-trips to the same provider view.
	// Replacing the live transcript with that copy drops the in-memory fold
	// (the gauge then sizes the whole canonical log) without changing what
	// the next request should send. Keep the live session in that case.
	if agent.SameProviderView(c.executor.Session().Snapshot(), messages) {
		return false, nil
	}
	// An empty history is a real state, not a miss: the view is replaced with
	// nothing so the observer renders the cleared transcript instead of
	// keeping the previous owner's content on screen.
	c.executor.Session().Replace(messages)
	// A peer append keeps a fold whose covered prefix still matches. When the
	// reload breaks the fold, the durable provider view — the compacted
	// model context, not the canonical log — is what the next request sends.
	if !c.executor.ProjectionValid() {
		if view := c.durableProviderView(); len(view) > 0 {
			c.executor.AdoptCoveringProviderView(view)
		} else if path := strings.TrimSpace(c.SessionPath()); path != "" && !c.sessionEngineEnabled() {
			c.executor.LoadProjectionSidecar(path)
		}
	}
	return true, nil
}

// durableProviderView is the provider-visible transcript stored beside the
// canonical log. Compaction writes it without rewriting history, so it is the
// fold to restore when a history reload invalidates the in-memory one.
func (c *Controller) durableProviderView() []provider.Message {
	snapshot, ok := c.sessionEventSnapshot()
	if !ok || len(snapshot.Projection.ModelMessages) == 0 {
		return nil
	}
	return append([]provider.Message(nil), snapshot.Projection.ModelMessages...)
}

// lastHistoryStamp and setLastHistoryStamp keep the observed stamp under its
// own lock without holding it across the reload: RuntimeStatus and the history
// read take controller locks of their own.
func (c *Controller) lastHistoryStamp() string { return c.runtimeState.historySync.lastHistoryStamp() }

func (c *Controller) setLastHistoryStamp(stamp string) {
	c.runtimeState.historySync.setLastHistoryStamp(stamp)
}

// durableHistoryMessages materializes the session's committed message list
// from the durable store alone. The v3 path pages the history index, which is
// a rebuildable projection over the event log and therefore readable without
// owning the runtime; the legacy path loads the transcript file.
func (c *Controller) durableHistoryMessages(ctx context.Context) ([]provider.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if service, runtime, exclusive := c.v3Binding(); exclusive && service != nil && runtime != nil {
		ref := runtime.Ref()
		shape, err := service.Query().HistoryShape(ctx, ref)
		if err != nil {
			return nil, err
		}
		if len(shape.Positions) == 0 {
			return nil, nil
		}
		return service.Query().HistoryWindow(ctx, ref, shape.SnapshotSequence, 0, len(shape.Positions))
	}
	// The legacy load has no cancellation point of its own, so the deadline is
	// checked on both sides of it: a read already given up on must not be adopted.
	path := strings.TrimSpace(c.SessionPath())
	if path == "" {
		return nil, nil
	}
	loaded, err := agent.LoadSession(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return loaded.Snapshot(), nil
}
