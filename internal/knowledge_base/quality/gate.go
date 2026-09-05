package quality

import (
	"strings"

	"reasonix/internal/knowledge_base/model"
)

// Gate names the thresholds that decide live vs draft.
type Gate struct {
	MinLiveConfidence float64
}

func DefaultGate() Gate { return Gate{MinLiveConfidence: 0.6} }

var garbled = []string{"lorem ipsum", "占位", "placeholder", "待补充", "???"}

// Apply stamps a candidate's Checks/Suspect/Status. High-confidence, clean
// items go live; everything else becomes draft and is never indexed.
func (g Gate) Apply(it *model.KnowledgeItem) {
	body := strings.ToLower(it.Body)
	suspect := false
	for _, m := range garbled {
		if strings.Contains(body, m) {
			suspect = true
			break
		}
	}
	if len(it.Provenance) == 0 {
		suspect = true
	}
	if it.Quality.Confidence < 0 || it.Quality.Confidence > 1 {
		it.Quality.Confidence = 0
		suspect = true
	}
	it.Quality.Suspect = suspect
	it.Quality.Checks = []string{"len", "prov", "anchor", "garbled"}
	if !suspect && it.Quality.Confidence >= g.MinLiveConfidence {
		it.Status = model.StatusLive
	} else {
		it.Status = model.StatusDraft
	}
}
