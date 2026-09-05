// Package quality gates candidates before they reach the store and resolves
// dedup / version / cross-author conflicts deterministically. P0 has no draft
// promotion: the gate stamps high-confidence items live and the rest draft.
package quality
