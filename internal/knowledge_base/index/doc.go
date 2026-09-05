// Package index is the rebuildable lexical read model over live items. It is
// never the truth source: items are, and index state can always be re-derived
// from the store. Query is filter-first; scoring reuses the leaf package
// internal/retrieval (stdlib-only CJK-bigram BM25).
package index
