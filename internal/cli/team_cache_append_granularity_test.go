// Part B (§B.2 items 5–6 of the one-shot plan): `append_block_allowance = 128`
// is an induced parameter, not a documented provider constant, yet the split
// between "expected append" and "provider residual" rests on it.
//
// This file is its calculator, and only its calculator: one gateway on one
// account is the extrapolation §B.2 item 6 forbids, so an offline or single
// environment distribution measures the parameter without ever substituting for
// the live run.
package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"reasonix/internal/team"
)

// cacheAppendAllowanceUnderTest mirrors the published allowance
// (internal/team/cacheAppendBlockAllowance, reported as append_block_allowance)
// so this analysis can state how many samples exceed the value currently in
// force. It is a copy, not a read, because the number being audited is the one a
// reader can see in the report.
const cacheAppendAllowanceUnderTest = 128

// appendGranularity is the distribution §B.2 item 5 asks for.
type appendGranularity struct {
	// Samples is how many append-only warm requests had a readable miss and a
	// readable predecessor prompt.
	Samples int
	// Mean, P50, P90 and Max describe the excess of the miss over the appended
	// content (miss − growth), which is what a block allowance bounds: zero means
	// the provider cached everything it had seen.
	Mean, P50, P90, Max float64
	// Unknown counts the received samples this analysis could not read: no
	// predecessor, no diagnosis, a shrinking prompt or no cache split. Counted,
	// never dropped — "not observed" is not "zero excess".
	Unknown int
	// OverAllowance counts the samples whose excess exceeds the allowance. A
	// nonzero count is the direct counter-evidence to the allowance: the partition
	// would file those as residual rather than as an expected append.
	OverAllowance int
}

// appendMissGranularity derives the distribution from a writer's records in log
// order. Only append-only requests are measured: a request whose array was
// rewritten, or whose prefix moved, is a different phenomenon and its miss is
// not what the allowance bounds.
func appendMissGranularity(records []team.MemberCacheRequest, allowance int) appendGranularity {
	var out appendGranularity
	var excess []float64
	var prev *team.MemberCacheRequest
	for i := range records {
		rec := records[i]
		switch {
		case prev == nil || prev.SessionIDHash != rec.SessionIDHash:
			out.Unknown++
		case !rec.DiagnosticsAvailable:
			out.Unknown++
		case rec.StablePrefixChanged || rec.PrefixChanged || len(rec.PrefixChangeReasons) > 0:
			// Not an append-only sample, so the allowance does not apply to it.
			out.Unknown++
		case !rec.MessagesComparable || rec.MessagesRewritten != 0:
			// The array moved (or was never observed), so nothing here is a
			// question about appended bytes.
			out.Unknown++
		case rec.ContextPromptTokens <= 0 || prev.ContextPromptTokens <= 0:
			out.Unknown++
		case rec.ContextPromptTokens < prev.ContextPromptTokens:
			out.Unknown++
		default:
			growth := float64(rec.ContextPromptTokens - prev.ContextPromptTokens)
			value := float64(rec.CacheMissTokens) - growth
			excess = append(excess, value)
			if value > float64(allowance) {
				out.OverAllowance++
			}
		}
		prev = &records[i]
	}
	out.Samples = len(excess)
	if out.Samples == 0 {
		return out
	}
	sum := 0.0
	for _, v := range excess {
		sum += v
	}
	out.Mean = sum / float64(out.Samples)
	sorted := slices.Clone(excess)
	slices.Sort(sorted)
	out.P50 = appendExcessPercentile(sorted, 0.50)
	out.P90 = appendExcessPercentile(sorted, 0.90)
	out.Max = sorted[len(sorted)-1]
	return out
}

// appendExcessPercentile is the nearest-rank percentile over an ascending slice,
// matching how the cache report states its own miss percentiles.
func appendExcessPercentile(ascending []float64, p float64) float64 {
	if len(ascending) == 0 {
		return 0
	}
	rank := int(float64(len(ascending)) * p)
	if float64(rank) < float64(len(ascending))*p {
		rank++
	}
	if rank < 1 {
		rank = 1
	}
	if rank > len(ascending) {
		rank = len(ascending)
	}
	return ascending[rank-1]
}

// TestAppendMissGranularityIsComputedFromTheSamples pins the calculator against
// hand-computed values, so the real-store arm below measures the provider rather
// than the arithmetic.
func TestAppendMissGranularityIsComputedFromTheSamples(t *testing.T) {
	rec := func(seq, prompt, miss int, reasons ...string) team.MemberCacheRequest {
		return team.MemberCacheRequest{
			SessionIDHash: "writer", SessionRequestSeq: seq, SessionOrdinal: 1,
			HasPrevRequest: seq > 1, DiagnosticsAvailable: true,
			ContextPromptTokens: prompt, CacheMissTokens: miss,
			MessagesComparable: true, PrefixChangeReasons: reasons,
		}
	}
	records := []team.MemberCacheRequest{
		rec(1, 10_000, 10_000),              // cold: no readable predecessor
		rec(2, 11_000, 1_080),               // growth 1000, excess 80
		rec(3, 13_000, 2_272),               // growth 2000, excess 272 (over 128)
		rec(4, 13_500, 772),                 // growth 500, excess 272 (over 128)
		rec(5, 14_000, 762, "compact_auto"), // a claimed rewrite is not an append
		rec(6, 13_000, 700),                 // a shrinking prompt is not an append
	}
	got := appendMissGranularity(records, cacheAppendAllowanceUnderTest)
	if got.Samples != 3 {
		t.Fatalf("Samples = %d, want 3 (the three append-only warm requests)", got.Samples)
	}
	if got.Unknown != 3 {
		t.Errorf("Unknown = %d, want 3 (cold, claimed rewrite, shrink)", got.Unknown)
	}
	if got.OverAllowance != 2 {
		t.Errorf("OverAllowance = %d, want 2 (excesses 272 twice)", got.OverAllowance)
	}
	if got.Max != 272 {
		t.Errorf("Max = %v, want 272", got.Max)
	}
	if got.P50 != 272 {
		t.Errorf("P50 = %v, want 272 (nearest rank over 80, 272, 272)", got.P50)
	}
	if got.Mean != 208 {
		t.Errorf("Mean = %v, want 208 ((80+272+272)/3)", got.Mean)
	}
}

// TestAppendMissGranularityFromARealStore is the measurement itself: it reads a
// writer's archived record log and reports the distribution. It skips without
// REASONIX_CACHE_SAMPLE_LOG, because the sample this table needs can only come
// from a real provider run:
//
//	REASONIX_CACHE_SAMPLE_LOG=<team>/<member>/.cache_requests.jsonl \
//	  go test ./internal/cli/ -run TestAppendMissGranularityFromARealStore -v
func TestAppendMissGranularityFromARealStore(t *testing.T) {
	path := os.Getenv("REASONIX_CACHE_SAMPLE_LOG")
	if path == "" {
		t.Skip("set REASONIX_CACHE_SAMPLE_LOG to a writer's .cache_requests.jsonl to measure the append allowance against real samples")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open sample log: %v", err)
	}
	defer file.Close()

	var records []team.MemberCacheRequest
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec team.MemberCacheRequest
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("parse sample log line %d: %v", len(records)+1, err)
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read sample log: %v", err)
	}

	got := appendMissGranularity(records, cacheAppendAllowanceUnderTest)
	t.Logf("append miss granularity over %d records (allowance %d tok): samples=%d unknown=%d over_allowance=%d mean=%.1f p50=%.0f p90=%.0f max=%.0f",
		len(records), cacheAppendAllowanceUnderTest, got.Samples, got.Unknown, got.OverAllowance, got.Mean, got.P50, got.P90, got.Max)
	if got.Samples == 0 {
		t.Fatal("no append-only warm sample in the log, so the allowance is neither supported nor contradicted by it")
	}
}
