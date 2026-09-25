package cachelab

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// strataRecord is one Team member request as an independent reader sees it. It
// carries no prompt or completion text — the member writers never stored any —
// so this reader cannot leak content even by accident.
type strataRecord struct {
	TeamID  string `json:"team_id"`
	Arm     string `json:"arm"`
	Member  string `json:"member_id"`
	Session string `json:"session_id"`
	Seq     int    `json:"session_request_seq"`
	Turn    string `json:"turn_id"`
	ReqID   string `json:"request_id"`

	ObservedAt string `json:"observed_at"`
	HasPrev    bool   `json:"has_prev_request"`

	Provider    string `json:"provider"`
	ModelRef    string `json:"model_ref"`
	RouteBucket string `json:"route_bucket"`

	Prompt        int `json:"prompt_tokens"`
	ContextPrompt int `json:"context_prompt_tokens"`
	Hit           int `json:"cache_hit_tokens"`
	Miss          int `json:"cache_miss_tokens"`

	RequestCount       int    `json:"request_count"`
	RequestCountSource string `json:"request_count_source"`
	UsageSource        string `json:"usage_source"`
	UsageUnknown       bool   `json:"usage_unknown"`
	UsageEstimated     bool   `json:"usage_estimated"`
	AccountingValid    bool   `json:"accounting_valid"`

	DiagnosticsAvailable bool     `json:"diagnostics_available"`
	StablePrefixChanged  bool     `json:"stable_prefix_changed"`
	PrefixChanged        bool     `json:"prefix_changed"`
	PrefixChangeReasons  []string `json:"prefix_change_reasons"`
}

// strataWarm reports whether a record is a warm candidate under the frozen
// rule: a request with a predecessor in the same member session. It is derived
// from the sequence number rather than read from the record's own flag, so the
// audit does not inherit a writer's opinion of its own data.
func (r strataRecord) strataWarm() bool { return r.Seq > 1 }

// strataExcluded names why a record is not main-baseline material, re-derived
// from the frozen contract rather than from the production classifier: an
// audit that called the same code it audits would only prove the code is
// self-consistent. An empty return means the record is includable.
func (r strataRecord) strataExcluded() string {
	switch {
	case r.RequestCountSource != "observed" || r.RequestCount <= 0:
		return "unverified_request_count"
	case r.RequestCount > 1:
		return "aggregate_requests"
	case r.UsageUnknown:
		return "unknown_usage"
	case r.UsageEstimated:
		return "estimated_usage"
	case !r.AccountingValid:
		return "accounting_invalid"
	case r.Hit+r.Miss <= 0:
		return "no_cache_split"
	}
	if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(r.ObservedAt)); err != nil {
		return "unparsable_observed_at"
	}
	return ""
}

// strataBucketOf places one request on the frozen prompt ladder. The boundaries
// are left-closed and right-open, and a non-positive size has no bucket at all.
func strataBucketOf(prompt int) string {
	switch {
	case prompt <= 0:
		return "unknown_prompt"
	case prompt < 32_768:
		return "lt_32k"
	case prompt < 131_072:
		return "32k_128k"
	case prompt < 262_144:
		return "128k_256k"
	case prompt < 524_288:
		return "256k_512k"
	case prompt < 786_432:
		return "512k_768k"
	case prompt < 1_048_576:
		return "768k_1m"
	default:
		return "gte_1m"
	}
}

// strataArmResult is one arm's independent accounting.
type strataArmResult struct {
	arm       string
	records   []strataRecord
	raw       int
	first     int
	warm      int
	excluded  map[string]int
	hit, miss int
	members   map[string]bool
	sessions  map[string]bool
	firstHit  int
	firstMiss int
}

func (a *strataArmResult) rate() (float64, bool) {
	if a.hit+a.miss <= 0 {
		return 0, false
	}
	return 100 * float64(a.hit) / float64(a.hit+a.miss), true
}

// readStrataArm decodes one arm's records. A torn trailing line is skipped the
// way every other reader of these logs skips it.
func readStrataArm(t *testing.T, path string) []strataRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []strataRecord
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec strataRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// TestRecomputeStrataArchive independently rebuilds the inclusion set and the
// metrics of one strata archive directory, from the records alone. It never
// reads a report JSON and never calls the production classifier, so a defect in
// either cannot hide behind the other. It is inert unless pointed at an
// archive, so CI never reads an operator's experiment data:
//
//	PART_C_STRATA_DIR=$HOME/reasonix-partb-archive/2026-09-24 go test \
//	  ./internal/cachelab/ -run TestRecomputeStrataArchive -v -count=1
//
// Every file is opened read-only and never written.
func TestRecomputeStrataArchive(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("PART_C_STRATA_DIR"))
	if dir == "" {
		t.Skip("set PART_C_STRATA_DIR to a read-only strata archive directory to recompute its arms")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "strata-*-records-*.jsonl"))
	if err != nil {
		t.Fatalf("globbing strata records under %s: %v", dir, err)
	}
	// The formal round archives under its own prefix. It is picked up by the
	// same reader so an arm cannot be audited only in the round that happened
	// to use the prefix already covered here.
	formal, err := filepath.Glob(filepath.Join(dir, "formal-*-records-*.jsonl"))
	if err != nil {
		t.Fatalf("globbing formal records under %s: %v", dir, err)
	}
	paths = append(paths, formal...)
	if len(paths) == 0 {
		t.Skipf("no strata or formal records under %s", dir)
	}
	slices.Sort(paths)

	results := make([]*strataArmResult, 0, len(paths))
	for _, path := range paths {
		recs := readStrataArm(t, path)
		res := &strataArmResult{
			records: recs, raw: len(recs),
			excluded: map[string]int{}, members: map[string]bool{}, sessions: map[string]bool{},
		}
		for _, rec := range recs {
			if rec.TeamID != "" {
				res.arm = rec.TeamID
			}
			if reason := rec.strataExcluded(); reason != "" {
				res.excluded[reason]++
				continue
			}
			res.members[rec.Member] = true
			res.sessions[rec.Session] = true
			if !rec.strataWarm() {
				res.first++
				res.firstHit += rec.Hit
				res.firstMiss += rec.Miss
				continue
			}
			res.warm++
			res.hit += rec.Hit
			res.miss += rec.Miss
		}
		results = append(results, res)
	}

	for _, res := range results {
		rate, ok := res.rate()
		t.Logf("%s: raw=%d first=%d warm=%d excluded=%v members=%d sessions=%d hit=%d miss=%d rate=%.4f%% ok=%v miss/req=%.2f",
			res.arm, res.raw, res.first, res.warm, res.excluded, len(res.members), len(res.sessions),
			res.hit, res.miss, rate, ok, meanOf(res.miss, res.warm))
		t.Logf("   first request: n=%d hit=%d miss=%d", res.first, res.firstHit, res.firstMiss)
	}

	// Invariants that hold per record and per arm, independent of any report.
	var violations []string
	seenRequestID, seenTurnID := map[string]string{}, map[string]string{}
	for _, res := range results {
		landed := map[string]int{}
		for _, rec := range res.records {
			landed[strataBucketOf(rec.ContextPrompt)]++
			if prev, dup := seenRequestID[rec.ReqID]; dup {
				violations = append(violations, "request_id "+rec.ReqID+" in both "+prev+" and "+res.arm)
			}
			seenRequestID[rec.ReqID] = res.arm
			if prev, dup := seenTurnID[rec.Turn]; dup {
				violations = append(violations, "turn_id "+rec.Turn+" in both "+prev+" and "+res.arm)
			}
			seenTurnID[rec.Turn] = res.arm
			if !rec.strataWarm() {
				continue
			}
			if rec.Hit%128 != 0 {
				violations = append(violations, rec.Member+" seq "+strconv.Itoa(rec.Seq)+": hit "+strconv.Itoa(rec.Hit)+" is not 128-aligned")
			}
			if (rec.Miss-rec.Prompt)%128 != 0 {
				violations = append(violations, rec.Member+" seq "+strconv.Itoa(rec.Seq)+": miss not congruent to prompt mod 128")
			}
		}
		keys := make([]string, 0, len(landed))
		for k := range landed {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		t.Logf("%s: buckets %v", res.arm, keys)
	}
	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("invariant: %s", v)
		}
	}
}
