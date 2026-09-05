package index

import (
	"sort"
	"strings"
	"sync"

	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/retrieval"
)

// Hit is one ranked candidate returned to the manager.
type Hit struct {
	ID    string
	Score float64
}

type doc struct {
	item   model.KnowledgeItem
	counts map[string]int
	length int
}

// Index holds token statistics for the live item set. Writes come from the
// single queue worker; reads (Search) may come from many host goroutines.
type Index struct {
	mu    sync.RWMutex
	docs  map[string]*doc
	df    map[string]int
	total int
	avg   float64
}

// New returns an empty index.
func New() *Index { return &Index{docs: map[string]*doc{}} }

// Rebuild replaces all state from the given items (live ones are indexed).
func (ix *Index) Rebuild(items []model.KnowledgeItem) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.docs = map[string]*doc{}
	for i := range items {
		if items[i].Status == model.StatusLive {
			ix.addLocked(&items[i])
		}
	}
	ix.recomputeLocked()
}

// Upsert adds or replaces a live item; non-live items are removed.
func (ix *Index) Upsert(item model.KnowledgeItem) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if item.Status != model.StatusLive {
		delete(ix.docs, item.ID)
		ix.recomputeLocked()
		return
	}
	ix.addLocked(&item)
	ix.recomputeLocked()
}

// Remove drops one item id from the index.
func (ix *Index) Remove(id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	delete(ix.docs, id)
	ix.recomputeLocked()
}

func (ix *Index) addLocked(it *model.KnowledgeItem) {
	text := it.Title + " " + strings.Join(it.Tags, " ") + " " + string(it.Kind) + " " + it.Body
	tokens := retrieval.Tokens(text)
	counts := retrieval.Counts(tokens)
	ix.docs[it.ID] = &doc{item: *it, counts: counts, length: len(tokens)}
}

// LiveCount reports the number of live indexed items.
func (ix *Index) LiveCount() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.docs)
}

// Search filters live candidates, then BM25-ranks them. It never blocks
// writers and never returns more than the query's effective limit.
func (ix *Index) Search(q model.Query) ([]Hit, error) {
	ix.mu.RLock()
	cands := make([]*doc, 0, len(ix.docs))
	for _, d := range ix.docs {
		if match(q, d.item) {
			cands = append(cands, d)
		}
	}
	df := copyMap(ix.df)
	total, avg := ix.total, ix.avg
	ix.mu.RUnlock()

	var terms []string
	var err error
	if strings.TrimSpace(q.Text) != "" {
		terms, err = retrieval.QueryTerms(q.Text)
		if err != nil {
			return nil, err
		}
	}
	ranked := make([]Hit, 0, len(cands))
	for _, d := range cands {
		var score float64
		if len(terms) > 0 {
			score = retrieval.BM25Score(d.counts, d.length, terms, df, total, avg)
		}
		ranked = append(ranked, Hit{ID: d.item.ID, Score: score})
	}
	sort.Slice(ranked, func(a, b int) bool {
		if ranked[a].Score != ranked[b].Score {
			return ranked[a].Score > ranked[b].Score
		}
		// deterministic tiebreak even for the empty-query fallback
		return ranked[a].ID > ranked[b].ID
	})
	if n := q.EffectiveLimit(); len(ranked) > n {
		ranked = ranked[:n]
	}
	return ranked, nil
}

// Matches reports whether an item passes the query's scope/kind/tag and
// confidence filters — the candidate predicate the index applies before
// ranking. The manager reuses it for its degraded store-scan query path.
func Matches(q model.Query, it model.KnowledgeItem) bool { return match(q, it) }

func match(q model.Query, it model.KnowledgeItem) bool {
	if q.Scope != "" && it.Scope != q.Scope {
		return false
	}
	if len(q.Kinds) > 0 {
		ok := false
		for _, k := range q.Kinds {
			if it.Kind == k {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, t := range q.Tags {
		if !contains(it.Tags, t) {
			return false
		}
	}
	if q.MinConfidence > 0 && it.Quality.Confidence < q.MinConfidence {
		return false
	}
	return true
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func copyMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (ix *Index) recomputeLocked() {
	maps := make([]map[string]int, 0, len(ix.docs))
	lengthSum := 0
	for _, d := range ix.docs {
		maps = append(maps, d.counts)
		lengthSum += d.length
	}
	ix.total = len(ix.docs)
	ix.df = retrieval.DocumentFrequency(maps)
	if ix.total > 0 {
		ix.avg = float64(lengthSum) / float64(ix.total)
	} else {
		ix.avg = 0
	}
}
