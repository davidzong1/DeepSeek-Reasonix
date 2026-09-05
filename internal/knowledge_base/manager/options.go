package manager

import (
	"errors"

	"reasonix/internal/knowledge_base/extract"
	"reasonix/internal/knowledge_base/quality"
)

var (
	// ErrClosed is returned by public methods after Close.
	ErrClosed = errors.New("manager: closed")
	// ErrInvalidTeam is returned when a method names a different team.
	ErrInvalidTeam = errors.New("manager: wrong team")
)

// Options configures one Manager.
type Options struct {
	// DataRoot is the parent of the per-team directory. Default "team/knowledge_base".
	DataRoot string
	// TeamID is required and is the only team this Manager may address.
	TeamID string
	// Chunk overrides the default chunking bounds; zero value means defaults.
	Chunk extract.Config
	// Gate overrides the live/draft confidence gate; zero value means defaults.
	Gate quality.Gate
	// LLM, when set, is used for needs_llm chunks. Its extractor must be safe to
	// call serially from the single worker.
	LLM extract.Extractor
}

// JobID names an accepted write (ingest/retire/clear) for observability.
type JobID string
