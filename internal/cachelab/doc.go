// Package cachelab is the fixture, journal and analysis side of the Team
// member prompt-cache provider experiment (Part B of
// docs/team-mcp-port/TEAM_MEMBER_CACHE_OPTIMIZATION_EXECUTION_PLAN.zh-CN.md).
//
// It exists because a provider cache-hit conclusion needs evidence this
// repository cannot produce from its own telemetry: the exact provider-visible
// request bytes, the provider's raw usage as it reported it, the interval
// between requests, and a pre-registered sample size. Everything here is
// fixture code: nothing in this package is imported by a production binary, and
// nothing here changes provider-visible behavior. The only caller is the
// live-tagged experiment driver, which assembles real Team members and dials a
// real provider through Recorder.
//
// Two boundaries are measured, and they answer different questions:
//
//   - Recorder sits between the client and the provider and journals one record
//     per HTTP attempt: request body digest and length, HTTP status, latency,
//     response usage as the provider wrote it, and the raw request bodies only
//     as digests. This is the "what did the provider actually see and report"
//     evidence the offline benchmarks cannot give.
//   - Journal stores those records plus the experiment context (arm, member,
//     client build, config digest, turn index) as JSONL, so a run can be
//     replayed and re-analysed without the prompt or tool text.
//
// Classify and Stats are the analysis contract: which samples may enter a
// baseline (warm, exact usage, accounted, not a retry), which must be reported
// but excluded (first request, usage missing or estimated, invalid accounting,
// transport error, confound), and what may be claimed (token-weighted rate,
// request-level distribution, paired interval, and a verdict that stays
// "inconclusive" whenever the evidence gate is not met).
package cachelab
