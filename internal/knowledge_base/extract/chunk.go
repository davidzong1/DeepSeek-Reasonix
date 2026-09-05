package extract

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"reasonix/internal/knowledge_base/model"
)

// Config bounds chunk sizing. MVP keeps one thought per chunk (no cross-thought
// merging) so each promoted item stays atomic; tokens are an estimate.
type Config struct {
	MinTokens int
	MaxTokens int // hard ceiling; above it a chunk is dropped as oversize
}

func DefaultConfig() Config { return Config{MinTokens: 0, MaxTokens: 4096} }

// Drop records a thought that produced no chunk.
type Drop struct {
	ThoughtID string
	Reason    string
}

// ChunkThoughts deterministically maps an ordered thought batch to chunks.
// Replay of the same batch yields the same chunk ids and ordering.
func ChunkThoughts(thoughts []model.Thought, cfg Config) ([]model.SourceChunk, []Drop) {
	var out []model.SourceChunk
	var drops []Drop
	for i, th := range thoughts {
		text := strings.TrimSpace(th.Text)
		toks := estimateTokens(text)
		if toks > cfg.MaxTokens {
			drops = append(drops, Drop{ThoughtID: th.ID, Reason: "oversize"})
			continue
		}
		out = append(out, model.SourceChunk{
			ID:         model.ChunkID(i, text),
			SpanRefs:   []string{th.ID},
			Text:       text,
			Order:      i,
			SourceType: "thought",
			TokenHint:  toks,
		})
	}
	return out, drops
}

// estimateTokens counts whitespace tokens, counting each CJK rune as a token.
func estimateTokens(s string) int {
	n := 0
	for _, f := range strings.Fields(s) {
		if allASCII(f) {
			n++
		} else {
			n += utf8.RuneCountInString(f)
		}
	}
	return n
}

func allASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}
