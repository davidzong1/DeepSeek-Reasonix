package team

import (
	"math"
	"slices"
	"strings"
)

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
	}
	slices.SortFunc(report.Members, func(l, r CacheMemberRate) int { return strings.Compare(l.MemberID, r.MemberID) })
	report.Totals.Requests, report.Totals.Members = len(report.Members), len(report.Members)
	report.TokenWeightedRate, report.HasRate = report.Totals.Rate()
	report.MemberSimpleMean, report.HasMemberMean = simpleMeanOfMembers(report.Members)
	last := CacheTokenTotals{HitTokens: lastHit, MissTokens: lastMiss}
	report.LastTurnWeightedRate, report.HasLastTurnRate = last.Rate()
	return report
}
