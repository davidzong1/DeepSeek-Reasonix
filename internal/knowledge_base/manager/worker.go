package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/knowledge_base/queue"
	"reasonix/internal/knowledge_base/store"
)

// run is the single consumer that drains accepted and replayed jobs in order.
func (m *Manager) run() {
	defer m.wg.Done()
	for {
		m.state.mu.Lock()
		for len(m.state.jobs) == 0 && !m.state.closed {
			m.state.cond.Wait()
		}
		if len(m.state.jobs) == 0 {
			m.state.busy = false
			m.state.mu.Unlock()
			return
		}
		j := m.state.jobs[0]
		m.state.jobs = m.state.jobs[1:]
		m.state.busy = true
		m.state.mu.Unlock()

		if err := m.process(j); err != nil {
			m.emit("job_failed", "", j.kind+": "+err.Error())
		}

		m.state.mu.Lock()
		sameQueue := m.q == j.q
		isClear := j.kind == jobKindClear
		m.state.mu.Unlock()

		// A ClearTeam that swapped the queue replaced the log j.seq lives in;
		// there is nothing left to confirm on it.
		if sameQueue {
			if err := j.q.Commit(j.seq); err != nil {
				m.emit("commit_failed", "", err.Error())
			}
		}

		m.state.mu.Lock()
		if isClear {
			m.state.clearing = false // swap done (or failed): release the write barrier
		}
		m.state.busy = false
		m.state.cond.Broadcast()
		m.state.mu.Unlock()
	}
}

func (m *Manager) process(j job) error {
	switch j.kind {
	case jobKindIngest:
		return m.processIngest(j.payload)
	case jobKindRetire:
		return m.processRetire(j.payload)
	case jobKindClear:
		return m.processClearTeam(j.payload)
	default:
		return fmt.Errorf("manager: unknown job kind %q", j.kind)
	}
}

func (m *Manager) processRetire(payload []byte) error {
	var req retireReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return err
	}
	for _, id := range req.IDs {
		it, err := m.st.Get(id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // idempotent on replay
			}
			return err
		}
		if it.Status == model.StatusRetired {
			continue
		}
		wasLive := it.Status == model.StatusLive
		if err := m.st.Transition(id, func(x *model.KnowledgeItem) error {
			x.Status = model.StatusRetired
			x.UpdatedAt = m.now()
			return nil
		}); err != nil {
			return err
		}
		if wasLive {
			m.ix.Remove(id)
		}
		m.emit("retired", id, string(req.Reason))
	}
	return nil
}

// processClearTeam fails closed on team/scope mismatch, moves the team directory
// to .trash, closes the old queue handle, then reopens an empty store/queue at
// the same path. The rename and the pointer swap share one state.mu critical
// section, so a concurrent enqueue can never write into the log being moved.
func (m *Manager) processClearTeam(payload []byte) error {
	var req clearReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return err
	}
	if req.Team != m.team {
		return fmt.Errorf("%w: got %q", ErrInvalidTeam, req.Team)
	}
	if req.Scope != model.ScopeTeam {
		return fmt.Errorf("%w: ClearTeam scope %q, only team scope is supported", model.ErrInvalid, req.Scope)
	}
	base := m.st.Root()
	dest := trashDirUnique(m.dataRoot, m.team, m.now())
	if err := os.MkdirAll(filepath.Join(m.dataRoot, ".trash"), 0o700); err != nil {
		return err
	}
	// Rename and pointer swap share one critical section so a concurrent
	// enqueue can never append into the log being moved to .trash.
	m.state.mu.Lock()
	if err := os.Rename(base, dest); err != nil {
		m.state.mu.Unlock()
		return err
	}
	newSt, err := store.Open(base)
	if err != nil {
		m.state.mu.Unlock()
		return err
	}
	newQ, err := queue.Open(filepath.Join(base, "queue"))
	if err != nil {
		m.state.mu.Unlock()
		return err
	}
	oldQ := m.q
	m.st = newSt
	m.q = newQ
	m.ix.Rebuild(nil)
	m.state.mu.Unlock()
	_ = oldQ.Close() // best-effort: release the handle on the moved-to-trash log
	m.emit("team_cleared", m.team, dest)
	return nil
}
