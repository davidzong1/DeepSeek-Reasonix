package quality

import (
	"sort"

	"reasonix/internal/knowledge_base/model"
)

// Action is the deterministic outcome of dedup / conflict resolution.
type Action string

const (
	ActionStoreNew  Action = "store_new" // no live canonical yet
	ActionSkip      Action = "skip"      // L1 exact duplicate (content hash)
	ActionSupersede Action = "supersede" // same producer: new version wins
	ActionConflict  Action = "conflict"  // two live producers: keep both
)

// Decision is what the manager executes against the store.
type Decision struct {
	Action     Action
	ExistingID string
}

// Decide classifies an incoming candidate against the stored items of its
// canonical slot. Order is exact-dedup first, then live-slot arbitration.
func Decide(existing []model.KnowledgeItem, incoming model.KnowledgeItem) Decision {
	h := model.ItemContentHash(incoming)
	var live []model.KnowledgeItem
	for i := range existing {
		e := existing[i]
		if model.ItemContentHash(e) == h {
			return Decision{Action: ActionSkip, ExistingID: e.ID}
		}
		if e.Canonical == incoming.Canonical && e.Status == model.StatusLive {
			live = append(live, e)
		}
	}
	if len(live) == 0 {
		return Decision{Action: ActionStoreNew}
	}
	if incoming.Status != model.StatusLive {
		return Decision{Action: ActionSkip, ExistingID: live[0].ID}
	}
	sort.Slice(live, func(a, b int) bool {
		if live[a].Version != live[b].Version {
			return live[a].Version > live[b].Version
		}
		return live[a].ID > live[b].ID
	})
	top := live[0]
	if top.AuthorID != "" && incoming.AuthorID != "" && top.AuthorID != incoming.AuthorID {
		return Decision{Action: ActionConflict, ExistingID: top.ID}
	}
	return Decision{Action: ActionSupersede, ExistingID: top.ID}
}
