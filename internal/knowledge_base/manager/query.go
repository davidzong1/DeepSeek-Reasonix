package manager

import (
	"sort"
	"strings"

	"reasonix/internal/knowledge_base/index"
	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/knowledge_base/store"
	"reasonix/internal/retrieval"
)

func retrievalQueryTerms(text string) ([]string, error) { return retrieval.QueryTerms(text) }

// queryByUpdated is the degraded read path when the searchable index cannot
// answer a query: it scans the store for live items matching the query filters
// and returns them newest-by-update first, capped at the effective limit.
func (m *Manager) queryByUpdated(st *store.Store, q model.Query) ([]model.Result, error) {
	items, err := st.List()
	if err != nil {
		return nil, err
	}
	live := make([]model.KnowledgeItem, 0, len(items))
	for i := range items {
		if items[i].Status == model.StatusLive && index.Matches(q, items[i]) {
			live = append(live, items[i])
		}
	}
	sort.Slice(live, func(a, b int) bool {
		if !live[a].UpdatedAt.Equal(live[b].UpdatedAt) {
			return live[a].UpdatedAt.After(live[b].UpdatedAt)
		}
		return live[a].ID > live[b].ID
	})
	if n := q.EffectiveLimit(); len(live) > n {
		live = live[:n]
	}
	out := make([]model.Result, 0, len(live))
	for i := range live {
		out = append(out, model.Result{Item: live[i]})
	}
	return out, nil
}

func makeSnippet(it model.KnowledgeItem, text string, terms []string) string {
	if text == "" || len(terms) == 0 {
		return ""
	}
	return retrieval.MakeSnippet(it.Body, text, terms, 160)
}

func matchedFields(it model.KnowledgeItem, terms []string) []string {
	if len(terms) == 0 {
		return nil
	}
	var out []string
	if hitsAny(it.Title, terms) {
		out = append(out, "title")
	}
	for _, t := range it.Tags {
		if hitsAny(t, terms) {
			out = append(out, "tags")
			break
		}
	}
	if hitsAny(it.Body, terms) {
		out = append(out, "body")
	}
	return out
}

func hitsAny(s string, terms []string) bool {
	low := strings.ToLower(s)
	for _, t := range terms {
		if strings.Contains(low, strings.ToLower(t)) {
			return true
		}
	}
	return false
}
