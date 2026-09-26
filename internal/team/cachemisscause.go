package team

import "reasonix/internal/cachereason"

// CacheMissCause is the closed classification of one request's uncached prompt
// tokens by the local event that can explain them. It answers the plan's four
// questions in one vocabulary: an append-only miss, a rewrite miss, a cold-prefix
// miss, and the residual that nothing local explains.
//
// The residual is the point of the classification, not a gap in it. A request
// whose cache-stable prefix did not move and whose miss outruns the content it
// appended is a request this repository cannot explain — the miss is provider
// scope, TTL, eviction or account behaviour, none of which is observable here.
// Naming that class explicitly is what keeps it from being read as a client-side
// defect.
type CacheMissCause string

const (
	// CacheCauseColdPrefix is a request that opened a session: the writer's first
	// observed request, or the first request after a context rescue rotated the
	// member onto a fresh session. Nothing was cached before it, so its whole
	// prompt is an expected miss.
	CacheCauseColdPrefix CacheMissCause = "cold_prefix"
	// CacheCauseRewrite is a prefix move caused by the turn rewriting its own
	// provider-visible content: a compaction fold, a prune, or a lossy truncate.
	// Only these are fold-boundary candidates.
	CacheCauseRewrite CacheMissCause = "rewrite"
	// CacheCauseStructural is a prefix move in the request's own framing — the
	// system prompt, the tool surface or the session-context tail. The cause is
	// known and there is nothing to fold: the framing is what it is.
	CacheCauseStructural CacheMissCause = "structural"
	// CacheCauseUnexplainedPrefix is a prefix move with no reason, or with a
	// reason value outside the shared vocabulary. It is reported as such rather
	// than guessed at.
	CacheCauseUnexplainedPrefix CacheMissCause = "unexplained_prefix_change"
	// CacheCauseAppendExpected is a request whose stable prefix did not move and
	// whose miss is accounted for by the content it appended: only the new tail
	// was uncached, which is what an append-only request costs.
	CacheCauseAppendExpected CacheMissCause = "append_only_expected"
	// CacheCauseProviderResidual is the class the plan's step 3 exists for: the
	// stable prefix did not move, no rewrite was reported, and the miss outruns
	// the appended content. Nothing observable here explains it.
	CacheCauseProviderResidual CacheMissCause = "provider_residual_unexplained"
	// CacheCauseMessagesRewritten is a request that rewrote conversation bytes the
	// provider had already read, with no fold, prune or truncate claiming it. It
	// is its own class rather than a residual because it names a client-side
	// cause the residual class exists to exclude: something rewrote the array and
	// nothing said what. It is only ever reachable when the writer published a
	// message-array fingerprint, so it never absorbs a sample whose array was
	// never observed.
	CacheCauseMessagesRewritten CacheMissCause = "messages_rewritten_unclaimed"
	// CacheCauseUndiagnosed is a request that carried no prefix diagnosis at all.
	// "Not observed" is not "did not change", so it is counted apart from the
	// append-only classes rather than assumed into them.
	CacheCauseUndiagnosed CacheMissCause = "undiagnosed"
)

// cacheMissCauses is the report order: the locally explained causes first, the
// classes a reader must not skim past last.
var cacheMissCauses = []CacheMissCause{
	CacheCauseColdPrefix, CacheCauseRewrite, CacheCauseStructural,
	CacheCauseUnexplainedPrefix, CacheCauseMessagesRewritten,
	CacheCauseAppendExpected, CacheCauseProviderResidual, CacheCauseUndiagnosed,
}

// cacheAppendBlockAllowance is how many tokens of slack an append-only request
// gets before its miss stops being explained by what it appended. It is the
// provider's observed cache-block granularity from the controlled experiment
// (hits land on block boundaries, so the uncached tail is the appended content
// rounded up to one), not a documented provider constant.
//
// The allowance is published in the report so the rule is auditable, and it is
// deliberately generous in the conservative direction: too large an allowance
// moves samples into "expected append", too small a one moves them into
// "residual". A reader who disagrees with the value can re-derive the partition
// from the per-cause prompt-growth statistics the report also publishes.
const cacheAppendBlockAllowance = 128

// cacheMissCauseInput is everything the classification may read: the record
// itself, and the prompt size of the previous request in the same writer session,
// which is what separates an expected append miss from a residual one.
type cacheMissCauseInput struct {
	rec        MemberCacheRequest
	prevPrompt int
	hasPrev    bool
}

// CacheMissCauseOf classifies one received sample. Every sample gets exactly one
// cause, so the classification partitions the samples rather than labelling them.
//
// The order is the plan's order: a cold start is decided before any prefix story
// because nothing was cached before it; a prefix move is then split by the kind
// of reason that moved it; and only a sample whose prefix provably did not move
// reaches the append-only split.
func CacheMissCauseOf(rec MemberCacheRequest) CacheMissCause {
	return classifyCacheMiss(cacheMissCauseInput{rec: rec})
}

// classifyCacheMiss is the classification with the predecessor's prompt size
// available. Without it the append-only split cannot be made and the sample is
// reported as residual, which is the direction that never under-reports the
// unexplained class.
func classifyCacheMiss(in cacheMissCauseInput) CacheMissCause {
	rec := in.rec
	if startsColdPrefix(rec) {
		return CacheCauseColdPrefix
	}
	if !rec.DiagnosticsAvailable {
		return CacheCauseUndiagnosed
	}
	if rec.StablePrefixChanged || rec.PrefixChanged || len(rec.PrefixChangeReasons) > 0 {
		return prefixMoveCause(rec.PrefixChangeReasons)
	}
	// The array itself moved bytes the provider had already read, and no fold,
	// prune or truncate claimed it. That is a client-side cause, so it must not
	// fall through into the class that means "nothing local explains this".
	if rec.MessagesComparable && rec.MessagesRewritten > 0 {
		return CacheCauseMessagesRewritten
	}
	if appendExplainsMiss(in) {
		return CacheCauseAppendExpected
	}
	return CacheCauseProviderResidual
}

// prefixMoveCause splits a prefix move by the kind of reason that moved it.
//
// `cachereason.Messages` is separated from the other rewrite values on purpose.
// The producer emits it for exactly one case: the conversation array moved bytes
// the provider had already read and **nothing else claimed them**. Every other
// rewrite value names an operation that did the rewriting (a fold, a prune, a
// truncate, a rewind, a guardian merge), so the two are different findings: one
// says "this operation rewrote the prefix", the other says "something rewrote it
// and no operation admits to it". Collapsing them would bury the second inside a
// class a reader is entitled to skim as already-explained.
func prefixMoveCause(reasons []string) CacheMissCause {
	rewrite, unclaimed, unrecognized, hasReasons := cacheReasonKinds(reasons)
	switch {
	case unclaimed:
		return CacheCauseMessagesRewritten
	case rewrite:
		return CacheCauseRewrite
	case !hasReasons || unrecognized:
		return CacheCauseUnexplainedPrefix
	default:
		return CacheCauseStructural
	}
}

// appendExplainsMiss reports whether the request's miss is no larger than the
// content it appended, within the block allowance. A request with no usable
// predecessor prompt cannot be checked, and is left to the residual class.
func appendExplainsMiss(in cacheMissCauseInput) bool {
	if !in.hasPrev || in.rec.ContextPromptTokens <= 0 || in.prevPrompt <= 0 {
		return false
	}
	growth := in.rec.ContextPromptTokens - in.prevPrompt
	if growth < 0 {
		// The prompt shrank without a reported prefix change. A shrink is not an
		// append, so the miss is not something this class can explain.
		return false
	}
	return in.rec.CacheMissTokens <= growth+cacheAppendBlockAllowance
}

// startsColdPrefix reports whether this request opened a session. Two shapes
// qualify: the first request after a session rotation — a context rescue starts a
// fresh provider-side prefix, so its first request is as cold as a session's first
// one even though the writer has a predecessor for it — and the writer's own
// first observed request.
//
// "Cold" is a claim that nothing was cached before this request, so both shapes
// require the writer's own session state. A record without it never came from a
// writer that could observe whether anything preceded it — a route-level ledger
// row, say — and its cold status is then unknown rather than true. Reporting such
// a record as cold would attribute a whole dataset's miss to a cold start nobody
// observed.
func startsColdPrefix(rec MemberCacheRequest) bool {
	if rec.SessionOrdinal > 1 && rec.SessionFirstRequestSeq > 0 &&
		rec.SessionRequestSeq == rec.SessionFirstRequestSeq {
		return true
	}
	return !rec.HasPrevRequest && rec.SessionRequestSeq > 0
}

// cacheReasonKinds summarises one sample's reason values through the shared
// vocabulary: whether any is a rewrite, whether any is unrecognized, and whether
// there were any at all. A structural value is known, so it is neither a rewrite
// nor unexplained.
// cacheReasonKinds summarises one sample's reason values through the shared
// vocabulary: whether any is a rewrite, whether any is the "nobody claimed it"
// value, whether any is unrecognized, and whether there were any at all. A
// structural value is known, so it is none of the first three.
func cacheReasonKinds(reasons []string) (rewrite, unclaimed, unrecognized, hasReasons bool) {
	for _, reason := range reasons {
		kind, ok := cachereason.KindOf(reason)
		switch {
		case !ok:
			unrecognized = true
		case reason == cachereason.Messages:
			// The one rewrite value that names no operation, because no operation
			// reported doing it. It is a distinct finding, not a fold.
			unclaimed = true
		case kind == cachereason.Rewrite:
			rewrite = true
		}
	}
	return rewrite, unclaimed, unrecognized, len(reasons) > 0
}

// CacheMissCauseStat is one cause's share of the received population. Requests
// and tokens are raw counts; the share is derived at render time so a reader can
// re-derive it.
type CacheMissCauseStat struct {
	Cause    CacheMissCause `json:"cause"`
	Requests int            `json:"requests"`
	// BaselineEligible counts how many of these requests entered the main
	// baseline. A cause whose requests are mostly excluded carries tokens the
	// baseline does not describe, which is why the count travels with the tokens.
	BaselineEligible int `json:"baseline_eligible"`
	HitTokens        int `json:"hit_tokens"`
	MissTokens       int `json:"miss_tokens"`
}

// CacheMissCauseReport partitions one report's received member-scoped samples by
// cause. It is a partition, not an overlapping ledger: the requests and tokens
// sum to the scoped population and to its total miss, so the classification
// reconciles against Coverage and against the baseline plus its exclusions.
type CacheMissCauseReport struct {
	Causes []CacheMissCauseStat `json:"causes"`
	// ScopedRequests/ScopedMissTokens are the totals the causes must sum to. They
	// are published beside the partition so a reader reconciles without
	// re-deriving the population.
	ScopedRequests   int `json:"scoped_requests"`
	ScopedMissTokens int `json:"scoped_miss_tokens"`
	ScopedHitTokens  int `json:"scoped_hit_tokens"`
	// AppendBlockAllowance is the slack the append-only split used, published so
	// the partition's rule is auditable rather than implicit.
	AppendBlockAllowance int `json:"append_block_allowance"`
	// UncheckedAppendSamples counts append-only samples whose predecessor prompt
	// was unavailable, so the split could not be made and they were reported as
	// residual. It is the size of the residual class the rule could not decide.
	UncheckedAppendSamples int `json:"unchecked_append_samples,omitempty"`
}

// add files one received sample into its cause's bucket.
func (r *CacheMissCauseReport) add(in cacheMissCauseInput, eligible bool) {
	cause := classifyCacheMiss(in)
	index := -1
	for i := range r.Causes {
		if r.Causes[i].Cause == cause {
			index = i
			break
		}
	}
	if index < 0 {
		r.Causes = append(r.Causes, CacheMissCauseStat{Cause: cause})
		index = len(r.Causes) - 1
	}
	stat := &r.Causes[index]
	stat.Requests++
	if eligible {
		stat.BaselineEligible++
	}
	stat.HitTokens += in.rec.CacheHitTokens
	stat.MissTokens += in.rec.CacheMissTokens
	if cause == CacheCauseProviderResidual && !in.hasPrev {
		r.UncheckedAppendSamples++
	}
	r.ScopedRequests++
	r.ScopedMissTokens += in.rec.CacheMissTokens
	r.ScopedHitTokens += in.rec.CacheHitTokens
}

// orderCacheMissCauses renders the partition in the fixed vocabulary order,
// dropping the causes no sample reached.
func (r *CacheMissCauseReport) order() {
	r.AppendBlockAllowance = cacheAppendBlockAllowance
	byCause := map[CacheMissCause]CacheMissCauseStat{}
	for _, stat := range r.Causes {
		byCause[stat.Cause] = stat
	}
	out := make([]CacheMissCauseStat, 0, len(byCause))
	for _, cause := range cacheMissCauses {
		if stat, ok := byCause[cause]; ok {
			out = append(out, stat)
		}
	}
	r.Causes = out
}

// cacheMissCauseTracker supplies each sample's predecessor prompt size. The
// predecessor is found by the writer's own request sequence, not by arrival
// order: the log is written by one writer and read oldest-first, but a report may
// be built from a filtered or concatenated sample set, and the sequence is the
// only ordering the writer actually stamped.
//
// The session is identified by the member plus the session digest the record
// carries. Without a digest the member alone is the key, which cannot tell a
// rotation apart from a growing session — and a rotation then reads as a shrink,
// which the append split refuses to explain. That is the conservative direction.
type cacheMissCauseTracker struct {
	bySession map[string][]cacheMissCausePoint
}

// cacheMissCausePoint is one request's place in its writer session's sequence.
type cacheMissCausePoint struct {
	seq    int
	prompt int
}

func newCacheMissCauseTracker() *cacheMissCauseTracker {
	return &cacheMissCauseTracker{bySession: map[string][]cacheMissCausePoint{}}
}

// observe returns the classification input for one record: the record, and the
// prompt of the request the writer stamped immediately before it in the same
// session.
func (t *cacheMissCauseTracker) observe(rec MemberCacheRequest) cacheMissCauseInput {
	key := cacheMissCauseSessionKey(rec)
	in := cacheMissCauseInput{rec: rec}
	if rec.ContextPromptTokens > 0 && rec.SessionRequestSeq > 0 {
		t.bySession[key] = append(t.bySession[key], cacheMissCausePoint{seq: rec.SessionRequestSeq, prompt: rec.ContextPromptTokens})
	}
	prev, ok := t.predecessor(key, rec.SessionRequestSeq)
	in.prevPrompt, in.hasPrev = prev, ok
	return in
}

// predecessor returns the prompt of the nearest earlier request in one writer
// session, by the writer's own sequence.
func (t *cacheMissCauseTracker) predecessor(key string, seq int) (int, bool) {
	if seq <= 0 {
		return 0, false
	}
	best, found := 0, false
	for _, point := range t.bySession[key] {
		if point.seq >= seq {
			continue
		}
		if !found || point.seq > best {
			best, found = point.seq, true
		}
	}
	if !found {
		return 0, false
	}
	for _, point := range t.bySession[key] {
		if point.seq == best {
			return point.prompt, true
		}
	}
	return 0, false
}

// cacheMissCauseSessionKey names one writer session within one member.
func cacheMissCauseSessionKey(rec MemberCacheRequest) string {
	return rec.MemberID + "\x00" + rec.SessionIDHash
}
