package team

import (
	"math"
	"slices"
	"strings"
)

// CacheReportCoverage is the field-coverage ledger of one report: for every
// dimension the report groups or excludes by, how many of the scoped samples
// actually carried it. It counts the samples that entered the member-scoped
// window, which is the population the baseline and the strata describe, and it
// is published over every one of them rather than over the included subset:
// coverage that is only reported for the samples that survived is not coverage.
//
// Every counter is a disclosure of absence, never a correction. A sample with
// no route is not assigned one, and a sample whose request count was defaulted
// is not reclassified as measured.
type CacheReportCoverage struct {
	Scoped int `json:"scoped"`
	// RequestCountObserved is the only provenance that can qualify a sample as a
	// single provider request; the other two are the ways a count is not one.
	RequestCountObserved   int `json:"request_count_observed"`
	RequestCountDefaulted  int `json:"request_count_defaulted"`
	RequestCountUnrecorded int `json:"request_count_unrecorded"`
	// RequestCountUnrecognized counts samples whose source value is outside the
	// vocabulary. They are treated as unverified, and named here so a producer
	// that grew a value is visible instead of silently absorbed.
	RequestCountUnrecognized int `json:"request_count_unrecognized"`
	// UsageSource counts samples by whether the emitting event named the billable
	// call source (executor, planner, compaction, …).
	UsageSourcePresent int `json:"usage_source_present"`
	UsageSourceAbsent  int `json:"usage_source_absent"`
	// RouteBucket and ModelRef count samples by whether the request could be
	// located to one provider cache scope and one model.
	RouteBucketPresent int `json:"route_bucket_present"`
	RouteBucketAbsent  int `json:"route_bucket_absent"`
	ModelRefPresent    int `json:"model_ref_present"`
	ModelRefAbsent     int `json:"model_ref_absent"`
	// DiagnosticsPresent counts samples that carried a prefix diagnosis. A sample
	// without one is not evidence that its prefix stayed put.
	DiagnosticsPresent int `json:"diagnostics_present"`
	DiagnosticsAbsent  int `json:"diagnostics_absent"`
	// SessionPresent counts samples that named the session they were observed in.
	// A sample without one was published outside a turn, so it cannot be grouped
	// into a session: its absence is a coverage gap, never a session of its own.
	SessionPresent int `json:"session_present"`
	SessionAbsent  int `json:"session_absent"`
	// SessionIdentityPresent counts samples whose writer stamped a hashed session
	// identity and a request sequence. Only these can tell a rotation's first
	// request from a growing session's; SessionPresent answers a different one.
	SessionIdentityPresent int `json:"session_identity_present"`
	SessionIdentityAbsent  int `json:"session_identity_absent"`
	// MessageShapeComparable counts samples that could compare their conversation
	// array against the previous request's. It is what gates the rewrite class: a
	// sample without it is not evidence that nothing was rewritten.
	MessageShapeComparable   int `json:"message_shape_comparable"`
	MessageShapeUncomparable int `json:"message_shape_uncomparable"`
}

// observe files one scoped sample into the coverage ledger.
func (c *CacheReportCoverage) observe(rec MemberCacheRequest) {
	c.Scoped++
	switch rec.RequestCountSource {
	case RequestCountObserved:
		c.RequestCountObserved++
	case RequestCountDefaulted:
		c.RequestCountDefaulted++
	case RequestCountUnrecorded, "":
		// An empty value is a document written before the field existed, which is
		// the same fact as a source that recorded none: nobody can audit the count.
		c.RequestCountUnrecorded++
	default:
		c.RequestCountUnrecognized++
	}
	if strings.TrimSpace(rec.UsageSource) == "" {
		c.UsageSourceAbsent++
	} else {
		c.UsageSourcePresent++
	}
	if strings.TrimSpace(rec.RouteBucket) == "" {
		c.RouteBucketAbsent++
	} else {
		c.RouteBucketPresent++
	}
	if strings.TrimSpace(rec.ModelRef) == "" {
		c.ModelRefAbsent++
	} else {
		c.ModelRefPresent++
	}
	if rec.DiagnosticsAvailable {
		c.DiagnosticsPresent++
	} else {
		c.DiagnosticsAbsent++
	}
	if strings.TrimSpace(rec.SessionID) == "" {
		c.SessionAbsent++
	} else {
		c.SessionPresent++
	}
	if strings.TrimSpace(rec.SessionIDHash) == "" || rec.SessionRequestSeq <= 0 {
		c.SessionIdentityAbsent++
	} else {
		c.SessionIdentityPresent++
	}
	if rec.MessagesComparable {
		c.MessageShapeComparable++
	} else {
		c.MessageShapeUncomparable++
	}
}

// CacheRateStats is a distribution of per-request cache hit rates. Percentiles
// are nearest-rank over the eligible requests, so every published value is one
// a request actually had.
type CacheRateStats struct {
	Requests int     `json:"requests"`
	Mean     float64 `json:"mean"`
	P10      float64 `json:"p10"`
	P50      float64 `json:"p50"`
	P90      float64 `json:"p90"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
}

// CacheTokenTotals is one aggregation's raw token ledger. The integers stay
// available so a caller can re-aggregate without the derived rates.
type CacheTokenTotals struct {
	Requests   int `json:"requests"`
	Members    int `json:"members"`
	HitTokens  int `json:"hit_tokens"`
	MissTokens int `json:"miss_tokens"`
}

// Rate returns the token-weighted hit rate, and reports false when there is no
// denominator at all. A group with no tokens has no rate; it is neither 0% nor
// 100%, and a provider that reports no cache split must not be read as a miss.
func (t CacheTokenTotals) Rate() (float64, bool) {
	total := t.HitTokens + t.MissTokens
	if total <= 0 {
		return 0, false
	}
	return float64(t.HitTokens) / float64(total), true
}

// MissTokensPerRequest is the stratum's mean uncached prompt per request, and
// reports false when there is no request to divide by. It is published beside
// the rate because a rate alone cannot say whether a change moved the uncached
// work or only the composition of the prompt: a request set whose fixed
// overhead is constant shows a rising rate as its prompt grows, with no request
// actually costing less. A candidate optimization is judged on this column.
func (t CacheTokenTotals) MissTokensPerRequest() (float64, bool) {
	if t.Requests <= 0 {
		return 0, false
	}
	return float64(t.MissTokens) / float64(t.Requests), true
}

// HitTokensPerRequest is the companion of MissTokensPerRequest, so a reader can
// see both halves of the prompt per request without re-deriving them.
func (t CacheTokenTotals) HitTokensPerRequest() (float64, bool) {
	if t.Requests <= 0 {
		return 0, false
	}
	return float64(t.HitTokens) / float64(t.Requests), true
}

// CacheMemberRate is one member's contribution inside a group.
type CacheMemberRate struct {
	MemberID        string  `json:"member_id"`
	Requests        int     `json:"requests"`
	HitTokens       int     `json:"hit_tokens"`
	MissTokens      int     `json:"miss_tokens"`
	WeightedRate    float64 `json:"weighted_rate"`
	HasWeightedRate bool    `json:"has_weighted_rate"`
	MeanRequestRate float64 `json:"mean_request_rate"`
}

// simpleMeanOfMembers is the equal-weight mean over the members that have a
// rate of their own, which differs from the group's token-weighted rate
// whenever members differ in size. A member with no denominator contributes
// nothing rather than a zero, and a group of only such members has no mean.
func simpleMeanOfMembers(members []CacheMemberRate) (float64, bool) {
	sum, counted := 0.0, 0
	for _, member := range members {
		if !member.HasWeightedRate {
			continue
		}
		sum += member.WeightedRate
		counted++
	}
	if counted == 0 {
		return 0, false
	}
	return sum / float64(counted), true
}

// cacheRateStats summarises a request-rate distribution. Percentiles are
// nearest-rank, so every published value is a rate some request actually had.
func cacheRateStats(rates []float64) CacheRateStats {
	if len(rates) == 0 {
		return CacheRateStats{}
	}
	sorted := slices.Clone(rates)
	slices.Sort(sorted)
	sum := 0.0
	for _, rate := range sorted {
		sum += rate
	}
	return CacheRateStats{
		Requests: len(sorted),
		Mean:     sum / float64(len(sorted)),
		P10:      nearestRank(sorted, 0.10),
		P50:      nearestRank(sorted, 0.50),
		P90:      nearestRank(sorted, 0.90),
		Min:      sorted[0],
		Max:      sorted[len(sorted)-1],
	}
}

// nearestRank returns the nearest-rank percentile of an ascending slice.
func nearestRank(sorted []float64, fraction float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(fraction * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}

// CacheSessionMaintenance is one member's published cumulative maintenance
// spend: what the session paid the summarizer, what it installed, how many
// rescues it certified and how many repeats it blocked. A cache rate alone
// cannot show this cost, so the session ledger carries it.
type CacheSessionMaintenance struct {
	SummaryRequests    int `json:"summary_requests"`
	ProjectionInstalls int `json:"projection_installs"`
	RescueCount        int `json:"rescue_count"`
	RepeatBlocks       int `json:"repeat_blocks"`
}

// CacheSessionTotals is one member's published session cache ledger: the input
// to the session-cumulative rate, which is a different number from any
// per-request rate.
type CacheSessionTotals struct {
	TeamID       string `json:"team_id"`
	MemberID     string `json:"member_id"`
	CacheHit     int    `json:"session_cache_hit"`
	CacheMiss    int    `json:"session_cache_miss"`
	LastTurnHit  int    `json:"last_turn_cache_hit"`
	LastTurnMiss int    `json:"last_turn_cache_miss"`
	// Maintenance is the member's published maintenance spend, meaningful only
	// when MaintenancePublished is true: a document written before the field
	// existed reports no spend, which is unknown rather than zero.
	Maintenance          CacheSessionMaintenance `json:"maintenance"`
	MaintenancePublished bool                    `json:"maintenance_published"`
}

// buildCacheSessionReport folds the published session ledgers. The session rate
// describes how a whole session's input tokens were served, so it is a
// different metric from every per-request rate in the report.
func buildCacheSessionReport(sessions []CacheSessionTotals) CacheSessionReport {
	var report CacheSessionReport
	lastHit, lastMiss := 0, 0
	for _, session := range sessions {
		if strings.TrimSpace(session.MemberID) == "" {
			continue
		}
		totals := CacheTokenTotals{Members: 1, HitTokens: session.CacheHit, MissTokens: session.CacheMiss}
		rate, ok := totals.Rate()
		report.Members = append(report.Members, CacheMemberRate{
			MemberID: session.MemberID, HitTokens: session.CacheHit, MissTokens: session.CacheMiss,
			WeightedRate: rate, HasWeightedRate: ok,
		})
		lastHit += session.LastTurnHit
		lastMiss += session.LastTurnMiss
		report.Totals.HitTokens += session.CacheHit
		report.Totals.MissTokens += session.CacheMiss
		// Summed only over members that published, and the count travels with the
		// sum: an omitted member would otherwise read as having spent nothing.
		if session.MaintenancePublished {
			report.Maintenance.SummaryRequests += session.Maintenance.SummaryRequests
			report.Maintenance.ProjectionInstalls += session.Maintenance.ProjectionInstalls
			report.Maintenance.RescueCount += session.Maintenance.RescueCount
			report.Maintenance.RepeatBlocks += session.Maintenance.RepeatBlocks
			report.MaintenanceMembers++
		}
	}
	slices.SortFunc(report.Members, func(l, r CacheMemberRate) int { return strings.Compare(l.MemberID, r.MemberID) })
	report.Totals.Requests, report.Totals.Members = len(report.Members), len(report.Members)
	report.TokenWeightedRate, report.HasRate = report.Totals.Rate()
	report.MemberSimpleMean, report.HasMemberMean = simpleMeanOfMembers(report.Members)
	last := CacheTokenTotals{HitTokens: lastHit, MissTokens: lastMiss}
	report.LastTurnWeightedRate, report.HasLastTurnRate = last.Rate()
	return report
}
