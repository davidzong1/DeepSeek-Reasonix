package manager

import (
	"context"
	"encoding/json"
	"strings"

	"reasonix/internal/knowledge_base/extract"
	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/knowledge_base/quality"
)

func (m *Manager) processIngest(payload []byte) error {
	var req ingestReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return err
	}
	chunks, _ := extract.ChunkThoughts(req.Thoughts, m.chunk)
	budget := 0
	if q := m.a.Quota(); q.AllowLLM && m.llm != nil {
		budget = q.MaxLLMCalls
	}
	for _, c := range chunks {
		v := extract.Classify(c)
		if v.Decision == extract.DecisionDeny {
			continue
		}
		var item model.KnowledgeItem
		switch v.Decision {
		case extract.DecisionAllow:
			item = extract.BuildItem(c, v, authorOf(req.Thoughts), model.ScopeTeam, resolveProv(c, req.Thoughts), m.now())
		case extract.DecisionNeedsLLM:
			items, err := m.extractLLM(c, &budget)
			if err != nil {
				m.emit("llm_failed", "", err.Error())
				continue
			}
			for _, it := range items {
				if err := m.commitItem(it); err != nil {
					m.emit("item_failed", "", err.Error())
				}
			}
			continue
		}
		if err := m.commitItem(item); err != nil {
			m.emit("item_failed", "", err.Error())
		}
	}
	return nil
}

// extractLLM consults the provider once per budgeted call and returns fully
// normalized, validated candidates (fail-closed on provider error).
func (m *Manager) extractLLM(c model.SourceChunk, budget *int) ([]model.KnowledgeItem, error) {
	if *budget <= 0 {
		return nil, nil
	}
	*budget--
	raw, err := m.llm.Extract(context.Background(), c.Text)
	if err != nil {
		return nil, err
	}
	author := "unknown-agent"
	prov := resolveProv(c, nil)
	now := m.now()
	var out []model.KnowledgeItem
	for i := range raw {
		it := raw[i]
		if it.Scope == "" {
			it.Scope = model.ScopeTeam
		}
		if it.AuthorID == "" {
			it.AuthorID = author
		}
		if len(it.Provenance) == 0 {
			it.Provenance = prov
		}
		it.Title = strings.TrimSpace(it.Title)
		it.Body = strings.TrimSpace(it.Body)
		it.CreatedAt = now
		it.UpdatedAt = now
		it.Version = 1
		it.Status = model.StatusLive // gate below re-decides draft
		if !it.Kind.Valid() || it.Title == "" || len([]rune(it.Title)) > 120 {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// commitItem runs gate -> dedup/conflict -> store/index for one candidate.
func (m *Manager) commitItem(item model.KnowledgeItem) error {
	if item.Scope == "" {
		item.Scope = model.ScopeTeam
	}
	if item.Quality.ReviewLevel == "" {
		item.Quality.ReviewLevel = model.ReviewNone
	}
	item.Canonical = model.CanonicalKey(item.Scope, item.Kind, item.Title)
	m.gate.Apply(&item)
	existing, err := m.st.ByCanonical(item.Canonical)
	if err != nil {
		return err
	}
	switch d := quality.Decide(existing, item); d.Action {
	case quality.ActionSkip:
		return m.foldLiveVersions(existing)
	case quality.ActionStoreNew:
		item.ID = model.NewID()
		if _, err := m.st.Put(item); err != nil {
			return err
		}
		if item.Status == model.StatusLive {
			m.ix.Upsert(item)
		}
		m.emit(eventOf(item.Status), item.ID, item.Canonical)
		return nil
	case quality.ActionSupersede:
		return m.commitSupersede(item, d.ExistingID)
	case quality.ActionConflict:
		return m.commitConflict(item, d.ExistingID)
	}
	return nil
}

func (m *Manager) commitSupersede(item model.KnowledgeItem, prevID string) error {
	prev, err := m.st.Get(prevID)
	if err != nil {
		return err
	}
	item.ID = model.NewID()
	item.Version = prev.Version + 1
	item.Supersedes = prev.ID
	item.UpdatedAt = m.now()
	if _, err := m.st.Put(item); err != nil {
		return err
	}
	if err := m.st.Transition(prev.ID, func(x *model.KnowledgeItem) error {
		x.Status = model.StatusSuperseded
		x.SupersededBy = item.ID
		x.UpdatedAt = m.now()
		return nil
	}); err != nil {
		return err
	}
	m.ix.Remove(prev.ID)
	if item.Status == model.StatusLive {
		m.ix.Upsert(item)
	}
	m.emit("item_superseded", prev.ID, item.ID)
	return nil
}

func (m *Manager) commitConflict(item model.KnowledgeItem, prevID string) error {
	item.ID = model.NewID()
	if _, err := m.st.Put(item); err != nil {
		return err
	}
	if err := m.st.Transition(prevID, func(x *model.KnowledgeItem) error {
		x.ConflictWith = item.ID
		x.UpdatedAt = m.now()
		return nil
	}); err != nil {
		return err
	}
	m.ix.Upsert(item)
	m.emit("item_conflict", item.ID, prevID)
	return nil
}

// foldLiveVersions is the crash-window repair for supersede. A supersede that
// wrote the new live version but crashed before stamping the old one leaves two
// live items of one canonical+author; replaying or re-ingesting that content
// lands on an L1 skip, and this folds every older same-author live peer under
// the highest live version. Different-author live items stay, so a cross-author
// conflict pair is untouched. No-op on a healthy single-live canonical.
func (m *Manager) foldLiveVersions(existing []model.KnowledgeItem) error {
	best := map[string]model.KnowledgeItem{}
	for i := range existing {
		it := existing[i]
		if it.Status != model.StatusLive {
			continue
		}
		b, ok := best[it.AuthorID]
		if !ok || it.Version > b.Version || (it.Version == b.Version && it.ID > b.ID) {
			best[it.AuthorID] = it
		}
	}
	for i := range existing {
		it := existing[i]
		if it.Status != model.StatusLive {
			continue
		}
		surv, ok := best[it.AuthorID]
		if !ok || surv.ID == it.ID {
			continue
		}
		if err := m.st.Transition(it.ID, func(x *model.KnowledgeItem) error {
			x.Status = model.StatusSuperseded
			x.SupersededBy = surv.ID
			x.UpdatedAt = m.now()
			return nil
		}); err != nil {
			return err
		}
		m.ix.Remove(it.ID)
		m.ix.Upsert(surv)
		m.emit("item_superseded", it.ID, surv.ID)
	}
	return nil
}

func eventOf(s model.Status) string {
	if s == model.StatusDraft {
		return "item_draft"
	}
	return "item_committed"
}

func authorOf(thoughts []model.Thought) string {
	for i := range thoughts {
		if thoughts[i].AgentID != "" {
			return thoughts[i].AgentID
		}
	}
	return "unknown-agent"
}

func resolveProv(c model.SourceChunk, thoughts []model.Thought) []model.Ref {
	byID := map[string]model.Thought{}
	for i := range thoughts {
		byID[thoughts[i].ID] = thoughts[i]
	}
	out := make([]model.Ref, 0, len(c.SpanRefs))
	for _, id := range c.SpanRefs {
		if th, ok := byID[id]; ok {
			out = append(out, model.Ref{Kind: string(th.Kind), Target: th.ID})
		} else {
			out = append(out, model.Ref{Kind: "thought", Target: id})
		}
	}
	if len(out) == 0 {
		out = append(out, model.Ref{Kind: "thought", Target: "unknown"})
	}
	return out
}
