package model

import "time"

type (
	Scope       string
	ThoughtKind string
	ItemKind    string
	Status      string
	ReviewLevel string
)

const (
	ScopeTeam    Scope = "team"
	ScopeProject Scope = "project"
	ScopeAgent   Scope = "agent"
	ScopeGlobal  Scope = "global"
)

const (
	ThoughtUserRequest ThoughtKind = "user_request"
	ThoughtDecision    ThoughtKind = "decision"
	ThoughtAction      ThoughtKind = "action"
	ThoughtObservation ThoughtKind = "observation"
	ThoughtCorrection  ThoughtKind = "correction"
	ThoughtConclusion  ThoughtKind = "conclusion"
)

const (
	ItemFact       ItemKind = "fact"
	ItemDecision   ItemKind = "decision"
	ItemConstraint ItemKind = "constraint"
	ItemConvention ItemKind = "convention"
	ItemHowTo      ItemKind = "howto"
	ItemLocation   ItemKind = "location"
	ItemWarning    ItemKind = "warning"
)

const (
	StatusDraft      Status = "draft"
	StatusLive       Status = "live"
	StatusSuperseded Status = "superseded"
	StatusDeprecated Status = "deprecated"
	StatusRetired    Status = "retired"
)

const (
	ReviewNone   ReviewLevel = "none"
	ReviewPeer   ReviewLevel = "peer"
	ReviewLeader ReviewLevel = "leader"
)

func (s Scope) Valid() bool {
	return s == ScopeTeam || s == ScopeProject || s == ScopeAgent || s == ScopeGlobal
}
func (k ThoughtKind) Valid() bool {
	switch k {
	case ThoughtUserRequest, ThoughtDecision, ThoughtAction, ThoughtObservation, ThoughtCorrection, ThoughtConclusion:
		return true
	}
	return false
}
func (k ItemKind) Valid() bool {
	switch k {
	case ItemFact, ItemDecision, ItemConstraint, ItemConvention, ItemHowTo, ItemLocation, ItemWarning:
		return true
	}
	return false
}
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusLive, StatusSuperseded, StatusDeprecated, StatusRetired:
		return true
	}
	return false
}
func (r ReviewLevel) Valid() bool {
	return r == ReviewNone || r == ReviewPeer || r == ReviewLeader
}

// Ref points an item back to the evidence that produced it.
type Ref struct {
	Kind   string `yaml:"kind"`
	Target string `yaml:"target"`
	Anchor string `yaml:"anchor,omitempty"`
}

// Thought is transient per-turn evidence collected at the host turn tail.
type Thought struct {
	ID          string
	TeamID      string
	AgentID     string
	SessionID   string
	TurnID      string
	Kind        ThoughtKind
	Text        string
	Provenance  []Ref
	CreatedAt   time.Time
	ContentHash string
}

// SourceChunk is a deterministic, content-addressed piece of source text.
type SourceChunk struct {
	ID         string
	SpanRefs   []string
	Text       string
	Order      int
	SourceType string
	TokenHint  int
	Oversize   bool
}

// QualitySignal is the scored, gated quality metadata of an item.
type QualitySignal struct {
	Confidence  float64
	ReviewLevel ReviewLevel
	Checks      []string
	Suspect     bool
}

// KnowledgeItem is the durable, searchable knowledge record (items/*.md).
type KnowledgeItem struct {
	ID           string
	Canonical    string
	AuthorID     string // producing agent; cross-author live conflicts stay both
	Title        string
	Kind         ItemKind
	Scope        Scope
	Tags         []string
	Provenance   []Ref
	Quality      QualitySignal
	Version      int
	Status       Status
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Body         string
	Supersedes   string // prior item id this version replaces ("" = none)
	SupersededBy string // set when a later version supersedes this file
	ConflictWith string // id of the other live item in an unresolved conflict
}

func (i KnowledgeItem) Live() bool { return i.Status == StatusLive }
