package event

// ContextMaintenance is the typed wire-safe receipt for snip/prune/noop/
// blocked operations. Transcript bytes are represented by hashes and counts.
type ContextMaintenance struct {
	Status              string `json:"status,omitempty"`
	Action              string `json:"action,omitempty"`
	Trigger             string `json:"trigger,omitempty"`
	OperationID         string `json:"operationId,omitempty"`
	InputTokens         int    `json:"inputTokens,omitempty"`
	ResultTokens        int    `json:"resultTokens,omitempty"`
	SavedTokens         int    `json:"savedTokens,omitempty"`
	AffectedToolResults int    `json:"affectedToolResults,omitempty"`
	ProjectionVersion   uint64 `json:"projectionVersion,omitempty"`
	CacheBreak          bool   `json:"cacheBreak,omitempty"`
	Reason              string `json:"reason,omitempty"`
	// The decision's diagnosis, carried so a reader that records it beside a
	// request reads the decision rather than a later snapshot of it. An empty
	// state means unobserved, and the numbers are then unset rather than zero.
	MaintenanceState string  `json:"maintenanceState,omitempty"`
	HeadroomTokens   int     `json:"headroomTokens,omitempty"`
	ReductionRatio   float64 `json:"reductionRatio,omitempty"`
}
