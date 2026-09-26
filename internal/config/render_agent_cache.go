package config

import (
	"fmt"
	"strings"
)

// renderAgentCacheControls writes the agent keys that shape provider-cache
// behaviour. Each is written only when it carries a value, so a config that
// never mentions them renders exactly as it did before they existed.
//
// They must be written at all because the renderer is what a config edit
// rewrites the file from. A key the renderer omits is dropped by the next save,
// and these decode to their defaults when absent — so an operator's explicit
// `false` would silently revert to enabled, and the rollback would not survive
// one unrelated edit.
func renderAgentCacheControls(b *strings.Builder, c *Config) {
	if c == nil {
		return
	}
	// A pointer key is written whenever it is set, in both directions: false is
	// the rollback, and it is indistinguishable from absent once dropped.
	if c.Agent.LowYieldLatch != nil {
		fmt.Fprintf(b, "low_yield_latch         = %v   # bars a view from another summary when a fold gave it no headroom\n", *c.Agent.LowYieldLatch)
	}
	if c.Agent.ShapeDiagnosis != nil {
		fmt.Fprintf(b, "message_shape_diagnosis = %v   # fingerprints the provider-visible message array; tells an append from a rewrite\n", *c.Agent.ShapeDiagnosis)
	}
	// A value key is written when it differs from its zero value, which is its
	// documented default.
	if c.Agent.CacheAwareCompaction {
		b.WriteString("cache_aware_compaction  = true   # defers an automatic fold to the hard ceiling while the prefix stays warm\n")
	}
	if c.Agent.ContextRescue {
		b.WriteString("context_rescue          = true   # certifies a continuation instead of a lossy truncation when a fold cannot recover\n")
	}
	if c.Agent.VisibleWindowTokens > 0 {
		fmt.Fprintf(b, "visible_window_tokens   = %d   # caps the recent verbatim tail below its default 16%% of the window\n", c.Agent.VisibleWindowTokens)
	}
}

// renderAgentCacheAndSafetyControls is the single [agent] hook the full
// renderer calls. The two halves live together because render.go's render
// functions are far over their line budgets already: one call, one line.
func renderAgentCacheAndSafetyControls(b *strings.Builder, c *Config, scope RenderScope) {
	renderAgentCacheControls(b, c)
	renderAgentSafetyControls(b, c, scope)
}

// diffAgentSectionControls is the same hook for a project delta. The cache keys
// are emitted under the same condition the full renderer uses: a key that is set
// is by definition one that differs from the default it would otherwise decode
// to.
func diffAgentSectionControls(agentBuf *strings.Builder, c, d *Config, anyAgent *bool) {
	if c == nil {
		return
	}
	diffRecoveryAndCompletionValidation(agentBuf, *c, *d, anyAgent)
	var buf strings.Builder
	renderAgentCacheControls(&buf, c)
	if buf.Len() > 0 {
		agentBuf.WriteString(buf.String())
		*anyAgent = true
	}
}
