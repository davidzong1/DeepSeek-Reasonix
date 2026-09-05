package adapter

import (
	"time"

	"reasonix/internal/knowledge_base/model"
)

// Quota bounds expensive work the host is willing to fund this Manager.
type Quota struct {
	AllowLLM    bool // false = rule-only path (fail-closed for needs_llm)
	MaxLLMCalls int
}

// Host is what the manager asks the runtime for. Implementations must be cheap
// and free of side effects.
type Host interface {
	Clock() time.Time
	Quota() Quota
}

// Sink receives observability events. Emit must be non-blocking best-effort:
// the manager never waits on it, so a host that wants delivery guarantees must
// buffer asynchronously itself.
type Sink interface {
	Emit(Event)
}

// Event is a structured observability record; observe side aggregates only.
type Event struct {
	Time   time.Time
	Name   string
	Team   string
	Key    string // item/job id when the event is about one entity
	Detail string
}

// Adapter is the full contract a host provides to New().
type Adapter interface {
	Host
	Sink
}

// ItemKindForThought maps a thought kind onto the durable item kind a rule
// would produce by default; host may override per thought.
func ItemKindForThought(k model.ThoughtKind) model.ItemKind {
	switch k {
	case model.ThoughtDecision:
		return model.ItemDecision
	case model.ThoughtCorrection:
		return model.ItemConstraint
	case model.ThoughtConclusion:
		return model.ItemFact
	default:
		return model.ItemFact
	}
}
