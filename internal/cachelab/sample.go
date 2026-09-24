package cachelab

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Sample is one HTTP attempt observed at the recorder boundary for one
// experiment arm. It carries no prompt, tool argument or completion text: the
// cache question needs request identity, size, timing and the provider's own
// usage numbers, none of which require content.
type Sample struct {
	Arm string `json:"arm"`
	Seq int    `json:"seq"`
	// RunID identifies the run that took this sample, so one journal file can
	// accumulate runs without their samples being pooled by a reader.
	RunID  string `json:"run_id,omitempty"`
	At     string `json:"at"`
	TeamID string `json:"team_id,omitempty"`
	// MemberID and ModelRef fix the identity the arm was registered with.
	MemberID string `json:"member_id,omitempty"`
	ModelRef string `json:"model_ref,omitempty"`
	// UpstreamHost is the provider host only; endpoint paths and credentials
	// never reach the journal.
	UpstreamHost string `json:"upstream_host,omitempty"`
	Client       string `json:"client,omitempty"`
	ClientCommit string `json:"client_commit,omitempty"`
	// ConfigDigest hashes the frozen arm configuration (endpoint, model,
	// params, tool surface identity) so a drifted run is detectable.
	ConfigDigest string `json:"config_digest,omitempty"`
	// RequestHash/RequestBytes are the local digest and length of this attempt's
	// provider-visible request body. They are a client-side identity, never a
	// provider cache key.
	RequestHash  string `json:"request_hash,omitempty"`
	RequestBytes int    `json:"request_bytes,omitempty"`
	// TurnSeq is the driver's turn index within the member's session (1 = the
	// session's first request). Repeat counts earlier attempts in the same arm
	// with byte-identical RequestHash.
	TurnSeq int `json:"turn_seq,omitempty"`
	Repeat  int `json:"repeat,omitempty"`
	// Attempt counts requests issued for the same turn after a failed one; it is
	// 1 for a request that follows a success or starts the arm.
	Attempt   int   `json:"attempt,omitempty"`
	LatencyMS int64 `json:"latency_ms"`
	// GapMS is the wall time since the previous request in this arm, which is
	// the variable the interval arms change.
	GapMS int64 `json:"gap_ms,omitempty"`
	// Status is the HTTP status (0 when the transport failed before a response).
	Status int    `json:"status"`
	Error  string `json:"error,omitempty"`
	// UsageReported is false when the response carried no usage at all; the
	// remaining usage fields are then unset rather than zero-valued evidence.
	UsageReported bool `json:"usage_reported"`
	// UsageSplit is true only when a hit count and a miss count were both
	// resolved from the provider's own vocabulary. A response that reports a
	// prompt but no cache read has no split and cannot yield a cache rate.
	UsageSplit     bool   `json:"usage_split"`
	UsageShape     string `json:"usage_shape,omitempty"`
	UsageSource    string `json:"usage_source,omitempty"`
	UsageProblem   string `json:"usage_problem,omitempty"`
	UsageEstimated bool   `json:"usage_estimated,omitempty"`
	// UsageKeys are the raw usage key names the response carried, sorted. They are
	// the audit trail for how the numbers above were read.
	UsageKeys []string `json:"usage_keys,omitempty"`
	// UsageOraclePresent records that the response carried the gateway's own
	// second account of the request. A sample without one is undecided, never
	// agreeing: the oracle fields stay unset rather than being filled in.
	UsageOraclePresent      bool     `json:"usage_oracle_present,omitempty"`
	UsageOracleAgrees       bool     `json:"usage_oracle_agrees,omitempty"`
	UsageOracleKeys         []string `json:"usage_oracle_keys,omitempty"`
	UsageOraclePromptTokens int      `json:"usage_oracle_prompt_tokens,omitempty"`
	UsageOracleHitTokens    int      `json:"usage_oracle_hit_tokens,omitempty"`
	UsageOracleMissTokens   int      `json:"usage_oracle_miss_tokens,omitempty"`
	PromptTokens            int      `json:"prompt_tokens,omitempty"`
	CacheHitTokens          int      `json:"cache_hit_tokens,omitempty"`
	CacheMissTokens         int      `json:"cache_miss_tokens,omitempty"`
	CacheWriteTokens        int      `json:"cache_write_tokens,omitempty"`
	CompletionTokens        int      `json:"completion_tokens,omitempty"`
	// QualityCheck is one of quality_pass, quality_fail or quality_unchecked:
	// the frozen task asks for an exact marker, so a missing marker is evidence.
	QualityCheck string `json:"quality_check"`
	// Confounds names every reason this sample cannot support a causal claim
	// (endpoint-derived client drift, protocol divergence, shared account pool).
	Confounds []string `json:"confounds,omitempty"`
}

// Quality outcomes recorded per sample. The frozen task requests an exact
// token, so the check is mechanical and does not need model judgement.
const (
	QualityPass      = "quality_pass"
	QualityFail      = "quality_fail"
	QualityUnchecked = "quality_unchecked"
)

// Class is the disposition of one sample under the frozen analysis contract.
// Exactly one class is assigned, by the ordered rules in Classify.
type Class string

const (
	// ClassFirstRequest is a session's first request: it can never be warm.
	ClassFirstRequest Class = "first_request"
	// ClassWarm is a later request that may enter a warm baseline.
	ClassWarm Class = "warm"
	// ClassRepeat is a byte-identical re-send of an already successful attempt.
	ClassRepeat Class = "repeat"
	// ClassRetry is an attempt issued after a failed one for the same turn.
	ClassRetry Class = "retry"
	// ClassError is a request that failed at the transport or HTTP level.
	ClassError Class = "error"
	// ClassUsageMissing is a response that reported no usage at all.
	ClassUsageMissing Class = "usage_missing"
	// ClassNoCacheSplit is a response whose usage carries no cache read, or whose
	// vocabulary this parser could not resolve into a hit/miss pair. It says
	// nothing about caching and is never scored as a zero rate.
	ClassNoCacheSplit Class = "no_cache_split"
	// ClassUsageEstimated is a response whose usage was derived rather than
	// reported, so it cannot support a provider claim.
	ClassUsageEstimated Class = "usage_estimated"
	// ClassInvalidAccounting is a response whose cache split does not close
	// against its own prompt.
	ClassInvalidAccounting Class = "invalid_accounting"
)

// Classify assigns the sample's single class, in the order the contract fixes:
// a failed request is never counted as a usage anomaly, and an unusable usage
// split outranks the warm/first split because it cannot enter any baseline.
func (s Sample) Classify() Class {
	switch {
	case s.Error != "" || s.Status < 200 || s.Status > 299:
		return ClassError
	case s.Attempt > 1:
		return ClassRetry
	case !s.UsageReported:
		return ClassUsageMissing
	case s.UsageEstimated:
		return ClassUsageEstimated
	case !s.UsageSplit:
		return ClassNoCacheSplit
	case !s.AccountingValid():
		return ClassInvalidAccounting
	case s.TurnSeq == 1:
		return ClassFirstRequest
	case s.Repeat > 1:
		return ClassRepeat
	default:
		return ClassWarm
	}
}

// AccountingValid reports whether the reported token fields are internally
// consistent. The numbers themselves are never corrected: an inconsistent split
// is excluded and reported, not rewritten.
func (s Sample) AccountingValid() bool {
	for _, v := range []int{s.PromptTokens, s.CacheHitTokens, s.CacheMissTokens, s.CacheWriteTokens, s.CompletionTokens} {
		if v < 0 {
			return false
		}
	}
	if split := s.CacheHitTokens + s.CacheMissTokens; split > 0 && s.PromptTokens > 0 && split > s.PromptTokens {
		return false
	}
	return true
}

// CacheDenominator is the sample's own hit+miss split, which is the rate
// denominator the contract fixes. It is zero when nothing was reported.
func (s Sample) CacheDenominator() int { return s.CacheHitTokens + s.CacheMissTokens }

// HitRate is this sample's token-weighted rate. It is only meaningful on a
// sample whose Classify makes it eligible.
func (s Sample) HitRate() float64 {
	denom := s.CacheDenominator()
	if denom <= 0 {
		return 0
	}
	return float64(s.CacheHitTokens) / float64(denom)
}

// Eligible reports whether the sample may enter a baseline. Everything else is
// still counted in the arm totals and its exclusion reason is reported.
func (s Sample) Eligible() bool {
	switch s.Classify() {
	case ClassWarm, ClassRepeat:
		return true
	default:
		return false
	}
}

// AddConfound records a reason this sample cannot support a causal claim. The
// reasons are a set, so re-recording one is not a duplicate.
func (s *Sample) AddConfound(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	for _, have := range s.Confounds {
		if have == reason {
			return
		}
	}
	s.Confounds = append(s.Confounds, reason)
}

// AtTime parses the sample's observation stamp. A record with an unparsable
// stamp is reported as absent rather than silently ordered as the epoch.
func (s Sample) AtTime() (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s.At))
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// RequestDigest is the local, client-side identity of a request body. It is
// deliberately not called a cache key: only the provider knows how it keys its
// own cache.
func RequestDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:16])
}

// SortedConfounds returns the confound set in a stable order and without
// duplicates: a confound is a reason, and naming it twice is the same reason.
func SortedConfounds(reasons []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		out = append(out, reason)
	}
	sort.Strings(out)
	return out
}

// Validate reports whether the sample carries the fields the contract requires
// for its class. A sample that cannot name its own request identity or timing is
// a fixture bug, not provider evidence, and is refused before it is journalled.
func (s Sample) Validate() error {
	if strings.TrimSpace(s.Arm) == "" {
		return fmt.Errorf("cachelab: sample has no arm")
	}
	if s.Seq <= 0 {
		return fmt.Errorf("cachelab: sample %s has sequence %d", s.Arm, s.Seq)
	}
	if _, ok := s.AtTime(); !ok {
		return fmt.Errorf("cachelab: sample %s/%d has no usable timestamp", s.Arm, s.Seq)
	}
	if s.Error == "" && (s.Status < 100 || s.Status > 599) {
		return fmt.Errorf("cachelab: sample %s/%d has status %d", s.Arm, s.Seq, s.Status)
	}
	if s.Error == "" && strings.TrimSpace(s.RequestHash) == "" {
		return fmt.Errorf("cachelab: sample %s/%d recorded no request digest", s.Arm, s.Seq)
	}
	if s.QualityCheck == "" {
		return fmt.Errorf("cachelab: sample %s/%d recorded no quality outcome", s.Arm, s.Seq)
	}
	return nil
}
