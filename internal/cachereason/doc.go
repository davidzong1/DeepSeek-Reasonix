// Package cachereason owns the vocabulary of cache-prefix change reasons: the
// values that travel on event.CacheDiagnostics.PrefixChangeReasons and say why
// the provider-visible prefix moved.
//
// It is a leaf so that every producer and every consumer can share one
// declaration. The vocabulary was previously written out by hand in a comment
// here, a switch in the agent, and an enum in the team report — and all three
// disagreed, because nothing forced an author adding a reason to decide what it
// meant for cache attribution. Declaring the kind beside the value is what makes
// that decision unavoidable, and it is the reason this package exists at all.
//
// The producers are:
//
//   - internal/agent CompareShape, for the request's own framing (system, tools,
//     session_context);
//   - internal/agent projectionRewriteReason, for the maintenance projections it
//     installs (compact_auto, prune, truncate);
//   - internal/control rewind, for the two rewind repairs;
//   - internal/guardian, for a guardian merge;
//   - internal/control and internal/agent session-prompt migrations, for the
//     authoritative system prompt being refreshed.
//
// A value that is not declared here is not silently treated as one of these: a
// consumer must report it as unrecognized, because guessing a cause is worse
// than saying the cause is unknown.
package cachereason
