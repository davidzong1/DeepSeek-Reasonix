package manager

import (
	"sync"
	"time"

	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/knowledge_base/queue"
)

const (
	defaultDataRoot = "team/knowledge_base"
	jobKindIngest   = "ingest"
	jobKindRetire   = "retire"
	jobKindClear    = "clearteam"
	jobKindExpire   = "expire"
)

type job struct {
	seq     uint64
	kind    string
	payload []byte
	q       *queue.Queue // queue that issued seq (ClearTeam swaps the live one)
}

// pendingState serializes enqueue vs the closing worker. The scalar flags live
// here, grouped by lifetime, so Close/Backlog stay race-free.
type pendingState struct {
	mu       sync.Mutex
	cond     *sync.Cond
	jobs     []job
	busy     bool // a job is popped and being processed/committed
	clearing bool // a ClearTeam is accepted/pending; new enqueues wait on cond
	closed   bool
}

// wire payloads (JSON, self-contained so replay needs no ambient state)
type ingestReq struct {
	Thoughts []model.Thought `json:"thoughts"`
}

type retireReq struct {
	IDs    []string           `json:"ids"`
	Reason model.RetireReason `json:"reason"`
}

type expireReq struct {
	Before time.Time          `json:"before"`
	Reason model.RetireReason `json:"reason"`
}

// expireResult hands one expire job's retired count back to the caller that
// waited on it. It lives behind its own guard so pendingState's flag set stays
// unchanged.
type expireResult struct {
	mu sync.Mutex
	n  int
}

type clearReq struct {
	Team  string      `json:"team"`
	Scope model.Scope `json:"scope"`
}
