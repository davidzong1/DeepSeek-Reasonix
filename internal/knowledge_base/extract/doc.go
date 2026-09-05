// Package extract turns thoughts into deterministic chunks and classifies
// each chunk as rule-promotable (allow), noise (deny), or LLM-only. The LLM
// path is an interface the host supplies; the core never depends on a provider.
package extract
