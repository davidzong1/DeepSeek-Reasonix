package model

// Query is a read-only request against one Manager. It deliberately carries no
// TeamID: the Manager's construction-bound team is the only addressing domain.
type Query struct {
	Text          string
	Scope         Scope
	Kinds         []ItemKind
	Tags          []string
	Limit         int // clamped to [1, maxQueryLimit]
	MinConfidence float64
}

const (
	maxQueryLimit   = 50
	defaultQueryHit = 20
)

// Result is one returned knowledge item plus the fields that matched.
type Result struct {
	Item    KnowledgeItem
	Score   float64
	Fields  []string
	Snippet string
}

func (q Query) EffectiveLimit() int {
	if q.Limit <= 0 {
		return defaultQueryHit
	}
	if q.Limit > maxQueryLimit {
		return maxQueryLimit
	}
	return q.Limit
}

// ItemContentHash is the exact-dedup (L1) fingerprint: canonical+title+body.
func ItemContentHash(i KnowledgeItem) string {
	return ContentHash(i.Canonical + "\n" + i.Title + "\n" + i.Body)
}

// RetireReason whitelist used by manager.Retire (fail-closed on unknown).
type RetireReason string

const (
	ReasonNoLongerTrue     RetireReason = "no_longer_true"
	ReasonSupersededByTomb RetireReason = "tombstone"
	ReasonPersonal         RetireReason = "personal_data"
)

func (r RetireReason) Valid() bool {
	switch r {
	case ReasonNoLongerTrue, ReasonSupersededByTomb, ReasonPersonal:
		return true
	}
	return false
}
