package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reasonix/internal/knowledge_base/adapter"
	"reasonix/internal/knowledge_base/extract"
	"reasonix/internal/knowledge_base/index"
	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/knowledge_base/quality"
	"reasonix/internal/knowledge_base/queue"
	"reasonix/internal/knowledge_base/store"
)

// Manager is one team's knowledge-base facade. All writes are funneled through
// a durable single-consumer queue; reads use the in-memory lexical read model.
type Manager struct {
	team     string
	dataRoot string
	a        adapter.Adapter
	st       *store.Store
	q        *queue.Queue
	ix       *index.Index
	gate     quality.Gate
	chunk    extract.Config
	llm      extract.Extractor
	state    pendingState
	wg       sync.WaitGroup
}

// New binds a Manager to exactly one team and starts its worker.
func New(a adapter.Adapter, opts Options) (*Manager, error) {
	if a == nil {
		return nil, errors.New("manager: nil adapter")
	}
	if err := model.ValidateTeamID(opts.TeamID); err != nil {
		return nil, err
	}
	dataRoot := opts.DataRoot
	if dataRoot == "" {
		dataRoot = defaultDataRoot
	}
	gate := opts.Gate
	if gate.MinLiveConfidence == 0 {
		gate = quality.DefaultGate()
	}
	cfg := opts.Chunk
	if cfg.MaxTokens == 0 {
		cfg = extract.DefaultConfig()
	}
	base := filepath.Join(dataRoot, opts.TeamID)
	st, err := store.Open(base)
	if err != nil {
		return nil, err
	}
	qu, err := queue.Open(filepath.Join(base, "queue"))
	if err != nil {
		return nil, err
	}
	m := &Manager{
		team: opts.TeamID, dataRoot: dataRoot, a: a,
		st: st, q: qu, ix: index.New(),
		gate: gate, chunk: cfg, llm: opts.LLM,
	}
	m.state.cond = sync.NewCond(&m.state.mu)
	if err := m.replayPending(); err != nil {
		return nil, err
	}
	m.wg.Add(1)
	go m.run()
	return m, nil
}

// Ingest accepts thoughts for asynchronous extraction. Success means the event
// is durably appended and the job is queued; it does not mean items exist yet.
func (m *Manager) Ingest(ctx context.Context, thoughts []model.Thought) (JobID, error) {
	if len(thoughts) == 0 {
		return "", errors.New("manager: empty ingest")
	}
	data, err := json.Marshal(ingestReq{Thoughts: thoughts})
	if err != nil {
		return "", err
	}
	seq, err := m.enqueue(jobKindIngest, data)
	if err != nil {
		return "", err
	}
	return JobID(fmt.Sprintf("job-%d", seq)), nil
}

// Retire moves items to retired (tombstone). Unknown ids are a no-op; the
// reason must be on the whitelist (fail-closed).
func (m *Manager) Retire(ctx context.Context, ids []string, reason model.RetireReason) error {
	if !reason.Valid() {
		return fmt.Errorf("%w: reason %q", model.ErrInvalid, reason)
	}
	for _, id := range ids {
		if err := model.ValidateRelID(id); err != nil {
			return err
		}
	}
	data, err := json.Marshal(retireReq{IDs: ids, Reason: reason})
	if err != nil {
		return err
	}
	_, err = m.enqueue(jobKindRetire, data)
	return err
}

// ClearTeam fails closed unless team is the Manager's bound team and scope is
// team scope (this facade owns one whole team directory), then removes the
// team's data directory to DataRoot/.trash asynchronously. ClearTeam is the
// linearization point for writes: once accepted, further Ingest/Retire/ClearTeam
// calls wait until the swap finishes and land on the fresh queue.
func (m *Manager) ClearTeam(ctx context.Context, team string, scope model.Scope) error {
	if team != m.team {
		return fmt.Errorf("%w: got %q want %q", ErrInvalidTeam, team, m.team)
	}
	if scope != model.ScopeTeam {
		return fmt.Errorf("%w: ClearTeam scope %q, only team scope is supported", model.ErrInvalid, scope)
	}
	data, err := json.Marshal(clearReq{Team: team, Scope: scope})
	if err != nil {
		return err
	}
	_, err = m.enqueue(jobKindClear, data)
	return err
}

// Query returns ranked live results from the read model. The query carries no
// team: this Manager only ever addresses its bound team. The store is snapshotted
// under state.mu so a concurrent ClearTeam swap can't race the read. When the
// index cannot answer (e.g. the query is not tokenizable), Query degrades to a
// live store scan ordered by update time rather than failing the read.
func (m *Manager) Query(ctx context.Context, q model.Query) ([]model.Result, error) {
	m.state.mu.Lock()
	st := m.st
	m.state.mu.Unlock()

	hits, err := m.ix.Search(q)
	if err != nil {
		return m.queryByUpdated(st, q)
	}
	var terms []string
	text := strings.TrimSpace(q.Text)
	if text != "" {
		terms, err = retrievalQueryTerms(text)
		if err != nil {
			return m.queryByUpdated(st, q)
		}
	}
	out := make([]model.Result, 0, len(hits))
	for _, h := range hits {
		it, gerr := st.Get(h.ID)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				continue // removed concurrently; index lags safely behind store
			}
			return nil, gerr
		}
		if !it.Live() {
			continue // worker already superseded/retired it; the index drop lags
		}
		out = append(out, model.Result{
			Item:    it,
			Score:   h.Score,
			Fields:  matchedFields(it, terms),
			Snippet: makeSnippet(it, text, terms),
		})
	}
	return out, nil
}

// Backlog reports how many accepted jobs are still unprocessed.
func (m *Manager) Backlog(ctx context.Context) (int, error) {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return len(m.state.jobs), nil
}

// Flush blocks until every accepted job is fully processed and confirmed
// (queue empty and the worker idle between jobs).
func (m *Manager) Flush(ctx context.Context) error {
	t := time.NewTicker(2 * time.Millisecond)
	defer t.Stop()
	for {
		m.state.mu.Lock()
		empty := len(m.state.jobs) == 0 && !m.state.busy
		m.state.mu.Unlock()
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Close stops the worker after draining accepted jobs, then closes the queue.
func (m *Manager) Close() error {
	m.state.mu.Lock()
	m.state.closed = true
	m.state.cond.Broadcast()
	m.state.mu.Unlock()
	m.wg.Wait()
	return m.q.Close()
}

// enqueue durably appends a job and queues it for the worker. Every mutating
// write funnels through here: once a ClearTeam is accepted it reserves the
// queue as its linearization boundary — later callers wait on cond until the
// swap is done, then append to the fresh queue, so no accepted event can land
// in the log that ClearTeam is about to move to .trash.
func (m *Manager) enqueue(kind string, payload []byte) (uint64, error) {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	for m.state.clearing && !m.state.closed {
		m.state.cond.Wait()
	}
	if m.state.closed {
		return 0, ErrClosed
	}
	seq, err := m.q.Append(kind, payload)
	if err != nil {
		return 0, err
	}
	if kind == jobKindClear {
		m.state.clearing = true
	}
	m.state.jobs = append(m.state.jobs, job{seq: seq, kind: kind, payload: payload, q: m.q})
	m.state.cond.Signal()
	return seq, nil
}

// replayPending loads unconfirmed events into the queue before the worker runs.
func (m *Manager) replayPending() error {
	var first []job
	committed := m.q.Committed()
	if err := m.q.Replay(committed, func(ev queue.Event) error {
		first = append(first, job{seq: ev.Seq, kind: ev.Kind, payload: ev.Payload, q: m.q})
		return nil
	}); err != nil {
		return err
	}
	items, err := m.st.List()
	if err != nil {
		return err
	}
	var live []model.KnowledgeItem
	for i := range items {
		if items[i].Status == model.StatusLive {
			live = append(live, items[i])
		}
	}
	m.ix.Rebuild(live)
	m.state.mu.Lock()
	m.state.jobs = append(m.state.jobs, first...)
	m.state.mu.Unlock()
	return nil
}

func (m *Manager) now() time.Time { return m.a.Clock() }

func (m *Manager) emit(name, key, detail string) {
	m.a.Emit(adapter.Event{Time: m.now(), Name: name, Team: m.team, Key: key, Detail: detail})
}

// trashDirUnique returns a nonexistent .trash destination path for ClearTeam.
func trashDirUnique(dataRoot, team string, ts time.Time) string {
	root := filepath.Join(dataRoot, ".trash")
	base := filepath.Join(root, ts.UTC().Format("20060102T150405Z")+"-"+team)
	if _, err := os.Lstat(base); os.IsNotExist(err) {
		return base
	}
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, err := os.Lstat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}
