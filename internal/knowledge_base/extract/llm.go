package extract

import (
	"context"
	"errors"
	"sync"

	"reasonix/internal/knowledge_base/model"
)

// ErrBudget reports that the configured LLM call budget for a batch is spent.
var ErrBudget = errors.New("extract: llm call budget exceeded")

// Extractor turns unstructured text into structured items. Implementations are
// provider-specific and live outside the core.
type Extractor interface {
	Extract(ctx context.Context, text string) ([]model.KnowledgeItem, error)
}

// Budgeted wraps an Extractor with a per-batch call cap. The manager fails
// closed on budget exhaustion: no further provider calls are attempted.
type Budgeted struct {
	inner Extractor
	max   int

	mu    sync.Mutex
	calls int
}

// NewBudgeted returns an extractor limited to max calls.
func NewBudgeted(inner Extractor, max int) *Budgeted {
	if max <= 0 {
		max = 1
	}
	return &Budgeted{inner: inner, max: max}
}

// Extract returns ErrBudget once the cap is spent; inner errors pass through.
func (b *Budgeted) Extract(ctx context.Context, text string) ([]model.KnowledgeItem, error) {
	b.mu.Lock()
	if b.calls >= b.max {
		b.mu.Unlock()
		return nil, ErrBudget
	}
	b.calls++
	b.mu.Unlock()
	return b.inner.Extract(ctx, text)
}
