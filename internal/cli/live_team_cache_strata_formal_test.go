//go:build live

package cli

// Formal strata driver (pre-registration V2): one arm per run, its identity
// read from the frozen pre-registration and the operator's own pool file, so
// the label a record carries and the credential that dials cannot split apart.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/team"
)

// formalPreRegPath names the pre-registration the driver reads. It is where
// the document lives, not a copy of what it says: the values exist in one
// place, and this constant only points at it.
const formalPreRegPath = "docs/team-mcp-port/TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION_V2.zh-CN.md"

// formalFrozen is the pre-registration's machine-readable identity block. It
// is the single source the runner may take an expectation from: a second
// hand-written copy is exactly how a run ends up on a route nobody froze.
type formalFrozen struct {
	PoolEntry   string `json:"pool_entry"`
	Provider    string `json:"provider"`
	Kind        string `json:"kind"`
	Endpoint    string `json:"endpoint"`
	Effort      string `json:"effort"`
	WireModel   string `json:"wire_model"`
	ModelRef    string `json:"model_ref"`
	RouteBucket string `json:"route_bucket"`
	Build       string `json:"build"`
}

// strata returns the expectation in the shape A's gate compares. Every field
// it carries is one the gate checks; Effort is not among them and is checked
// separately, because A's strataIdentity does not have the field.
func (f formalFrozen) strata() strataIdentity {
	return strataIdentity{
		PoolEntry:   f.PoolEntry,
		Kind:        f.Kind,
		Endpoint:    f.Endpoint,
		WireModel:   f.WireModel,
		ModelRef:    f.ModelRef,
		RouteBucket: f.RouteBucket,
		Build:       f.Build,
	}
}

// formalJSONBlock returns the first ```json fence's contents. The
// pre-registration carries exactly one, so a second would change which
// identity a run takes — the digest check is what makes that visible.
func formalJSONBlock(doc string) (string, error) {
	const fence = "```json"
	at := strings.Index(doc, fence)
	if at < 0 {
		return "", fmt.Errorf("no json identity block found")
	}
	rest := doc[at+len(fence):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", fmt.Errorf("json identity block is unterminated")
	}
	return rest[:end], nil
}

// formalLoadPreReg reads the frozen pre-registration and refuses it unless its
// digest is the one recorded at sign-off. The digest is what makes "the
// expectation comes from the frozen document" checkable rather than asserted:
// an edited document is a different document, and a run pinned to the old
// digest must not silently follow the new one.
func formalLoadPreReg(path, wantDigest string) (formalFrozen, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return formalFrozen{}, fmt.Errorf("no pre-registration path given")
	}
	wantDigest = strings.ToLower(strings.TrimSpace(wantDigest))
	if wantDigest == "" {
		return formalFrozen{}, fmt.Errorf("no pre-registration digest given; a document whose digest is unrecorded is not a frozen one")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return formalFrozen{}, fmt.Errorf("reading pre-registration: %w", err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantDigest {
		return formalFrozen{}, fmt.Errorf("pre-registration digest is %s, but the run was pinned to %s — freeze a new version and re-record it rather than proceeding", got, wantDigest)
	}
	block, err := formalJSONBlock(string(data))
	if err != nil {
		return formalFrozen{}, err
	}
	return formalDecodeIdentity(block)
}

// formalPoolFile opens the operator's pool registry read-only. Only reads are
// ever issued against it: the store is used for its production parser and
// schema check, never written, so the operator's own state is not a run
// artifact.
//
// The store resolves the registry by its fixed name inside the given
// directory, so a differently-named file would be ignored rather than read —
// and the run would silently validate whatever `agent_users.json` happens to
// sit beside it. The name is therefore required outright instead of assumed.
func formalPoolFile(path string) (*team.TeamStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("no pool file given; the run must read the entry it dials from one record")
	}
	if base := filepath.Base(path); base != team.AgentUsersFile {
		return nil, fmt.Errorf("pool file must be named %q, but %q was given — the registry is resolved by that fixed name, so any other spelling would read a different file", team.AgentUsersFile, base)
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("pool file is unreadable: %w", err)
	}
	return team.NewTeamStoreAt("", filepath.Dir(path))
}

// formalEffortCheck compares the one identity field A's gate does not carry.
// Effort reaches the request body (output_config.effort), so an entry whose
// effort moved is a different request shape even though every field the gate
// compares still matches.
func formalEffortCheck(want string, entry team.AgentUser) error {
	want = strings.TrimSpace(want)
	if want == "" {
		return fmt.Errorf("the frozen identity states no effort, so it is not frozen")
	}
	if got := strings.TrimSpace(entry.Effort); got != want {
		return fmt.Errorf("effort drifted: the pre-registration froze %q but the pool entry carries %q — the request body would differ from the registered one", want, got)
	}
	return nil
}

// formalArms is the V2 registration itself, copied from
// TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION_V2 §3. An id absent here is
// refused rather than approximated, so a typo cannot run an unregistered arm.
// S0-canary is the pre-registration's own field check: one member, one turn,
// answering whether the credential works, whether the account reports a cache
// split, and whether the route bucket lands on the frozen value. It is not a
// sample arm and enters no denominator.
func formalArms() []strataArm {
	return []strataArm{
		{ID: "S0-canary", BriefKB: 40, Members: 1, Turns: 1, TargetBucket: team.CacheBucketLT32K, Concurrency: 1, TokenBudget: 65_536, SecondsPerTurn: 0.4},
		{ID: "S1a-small", BriefKB: 40, Members: 3, Turns: 12, TargetBucket: team.CacheBucketLT32K, Concurrency: 1, TokenBudget: 2_000_000, SecondsPerTurn: 0.4},
		{ID: "S1b-mid", BriefKB: 900, Members: 3, Turns: 12, TargetBucket: team.CacheBucket128K256K, Concurrency: 1, TokenBudget: 10_000_000, SecondsPerTurn: 0.4},
		{ID: "S1c-large", BriefKB: 2800, Members: 3, Turns: 12, TargetBucket: team.CacheBucket512K768K, Concurrency: 1, TokenBudget: 26_000_000, SecondsPerTurn: 0.4},
		{ID: "S4-768k", BriefKB: 3750, Members: 3, Turns: 12, TargetBucket: team.CacheBucket768K1M, Concurrency: 1, TokenBudget: 30_000_000, SecondsPerTurn: 0.4},
		{ID: "S3-c2", BriefKB: 40, Members: 3, Turns: 12, TargetBucket: team.CacheBucketLT32K, Concurrency: 2, TokenBudget: 2_000_000, SecondsPerTurn: 0.4},
		{ID: "S3-c3", BriefKB: 40, Members: 3, Turns: 12, TargetBucket: team.CacheBucketLT32K, Concurrency: 3, TokenBudget: 2_000_000, SecondsPerTurn: 0.4},
	}
}

// formalArmByID resolves one registered arm, or reports what is registered.
func formalArmByID(id string) (strataArm, error) {
	id = strings.TrimSpace(id)
	for _, arm := range formalArms() {
		if arm.ID == id {
			return arm, nil
		}
	}
	ids := make([]string, 0, len(formalArms()))
	for _, arm := range formalArms() {
		ids = append(ids, arm.ID)
	}
	return strataArm{}, fmt.Errorf("formal strata: %q is not a registered arm; registered: %s", id, strings.Join(ids, ", "))
}

// formalEnv names a required input. An unset variable is refused rather than
// defaulted: every one of these decides which identity, credential or
// destination a paid run uses, and a default can only make that decision
// silently.
func formalEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("set %s: a formal run has no usable default for it", name)
	}
	return value
}

// formalAssembly seeds a run store with the entry read from the operator's
// pool file, verbatim. The member builder then resolves the same record the
// preflight validated, so the label and the credential are one record by
// construction — a run cannot label itself one entry and dial another.
func formalAssembly(t *testing.T, arm strataArm, entry team.AgentUser) (*team.OwnerStore, func(team.MemberBinding) (control.SessionAPI, error), *team.TeamStore, string) {
	t.Helper()
	root := t.TempDir()
	store, err := team.NewTeamStoreAt("", root)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := team.NewTeamSessionStoreDir(root)
	if err != nil {
		t.Fatal(err)
	}
	owners, err := team.NewOwnerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTeam(team.Team{Name: arm.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddAgentUser(entry); err != nil {
		t.Fatal(err)
	}
	for _, memberID := range strataMemberIDs(arm) {
		if err := store.AddMember(arm.ID, team.MemberSlot{MemberID: memberID, Role: "member", AgentUserRef: entry.UserID}); err != nil {
			t.Fatal(err)
		}
	}
	deps := memberBackendDeps{
		ctx: t.Context(), users: store, store: store, sessions: sessions,
		events: newMemberEventPump(), owners: owners,
		workspaceRoot: t.TempDir(),
		base:          func() boot.Options { return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard} },
	}
	return owners, newMemberBackendBuilder(deps), store, root
}

// formalRunMember assembles one member's backend from the run store and drives
// its turns serially. The pool entry is the one the run was seeded with, so
// the member dials exactly the record the preflight passed.
func formalRunMember(t *testing.T, ctx context.Context, owners *team.OwnerStore, build func(team.MemberBinding) (control.SessionAPI, error), arm strataArm, ref, memberID string) string {
	t.Helper()
	sessionFile, err := team.MemberSessionFile(arm.ID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := build(team.MemberBinding{
		Team: arm.ID, MemberID: memberID, Role: "member", AgentUserRef: ref,
		SessionFile: sessionFile,
	})
	if err != nil {
		t.Fatalf("assemble member %s: %v", memberID, err)
	}
	defer backend.Close()
	for turn := 1; turn <= arm.Turns; turn++ {
		if reason := strataStopReason(t, owners, arm, memberID); reason != "" {
			return reason
		}
		prompt := fmt.Sprintf("Turn %d: reply with exactly the word ACK%d and nothing else.", turn, turn)
		if turn == 1 {
			prompt = pilotBrief(arm.BriefKB) + "\n" + prompt
		}
		if err := backend.RunTurn(ctx, prompt); err != nil {
			t.Fatalf("member %s turn %d: %v", memberID, turn, err)
		}
		time.Sleep(time.Duration(arm.SecondsPerTurn * float64(time.Second)))
	}
	return ""
}

// formalRun drives the arm's roster, honouring its registered concurrency. A
// member is serial within itself either way, so the arm varies only how many
// members run together.
func formalRun(t *testing.T, ctx context.Context, owners *team.OwnerStore, build func(team.MemberBinding) (control.SessionAPI, error), arm strataArm, ref string, members []string) string {
	t.Helper()
	if arm.Concurrency <= 1 {
		for _, memberID := range members {
			if reason := formalRunMember(t, ctx, owners, build, arm, ref, memberID); reason != "" {
				return reason
			}
		}
		return ""
	}
	stop := make(chan string, len(members))
	var wg sync.WaitGroup
	for worker := range min(arm.Concurrency, len(members)) {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for idx := worker; idx < len(members); idx += arm.Concurrency {
				if reason := formalRunMember(t, ctx, owners, build, arm, ref, members[idx]); reason != "" {
					select {
					case stop <- reason:
					default:
					}
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	select {
	case reason := <-stop:
		return reason
	default:
		return ""
	}
}

// formalIdentityKeys is the exact key set the identity block must carry. It is
// checked before decoding, because json.Unmarshal ignores a key it does not
// know: a misspelled name leaves its field empty rather than failing, and the
// empty value is then refused as "not frozen" — a true statement pointing at
// the wrong cause. The check makes the failure name the key instead.
var formalIdentityKeys = []string{
	"pool_entry", "provider", "kind", "endpoint", "effort",
	"wire_model", "model_ref", "route_bucket", "build",
}

// formalDecodeIdentity decodes the identity block, refusing a key set that is
// not exactly the frozen one. A missing key and an unknown key are both
// refused: the first would decode to an empty field, the second is almost
// always the first with a typo, and neither should reach a comparison.
func formalDecodeIdentity(block string) (formalFrozen, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(block), &raw); err != nil {
		return formalFrozen{}, fmt.Errorf("the identity block does not parse: %w", err)
	}
	seen := make(map[string]bool, len(raw))
	for key := range raw {
		seen[key] = true
	}
	var missing, unknown []string
	for _, key := range formalIdentityKeys {
		if !seen[key] {
			missing = append(missing, key)
		}
		delete(seen, key)
	}
	for key := range seen {
		unknown = append(unknown, key)
	}
	slices.Sort(unknown)
	if len(missing) > 0 || len(unknown) > 0 {
		return formalFrozen{}, fmt.Errorf(
			"the identity block's keys are not the frozen set: missing %v, unrecognized %v — a misspelled key decodes to an empty field rather than failing, so it is refused here instead",
			missing, unknown)
	}
	var f formalFrozen
	if err := json.Unmarshal([]byte(block), &f); err != nil {
		return formalFrozen{}, fmt.Errorf("parsing the identity block: %w", err)
	}
	return f, nil
}

// TestFormalPreRegIdentityMatchesTheResolver pins the pre-registration's own
// identity block against the production resolution chain. Offline: no request,
// no credential, no registry. It is the check that would have caught V1, whose
// document named a pool entry and a route bucket that entry cannot produce —
// a contradiction invisible to anyone reading the table, and visible the
// moment the document is asked to resolve to itself.
//
// The key set is checked before decoding, and every stated field is compared,
// including those with no cross-derivation: a typo is a wrong answer that must
// not be reported as a different wrong answer.
func TestFormalPreRegIdentityMatchesTheResolver(t *testing.T) {
	data, err := os.ReadFile(formalRepoFile(t, formalPreRegPath))
	if err != nil {
		t.Fatal(err)
	}
	block, err := formalJSONBlock(string(data))
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := formalDecodeIdentity(block)
	if err != nil {
		t.Fatal(err)
	}

	// The block states the entry's own model spelling inside model_ref, and the
	// wire model is that spelling with the [1m] alias stripped. Both are
	// asserted, so independently edited fields cannot pass.
	prefix := frozen.PoolEntry + "/"
	if !strings.HasPrefix(frozen.ModelRef, prefix) {
		t.Fatalf("model_ref %q is not %q prefixed by the pool entry", frozen.ModelRef, prefix)
	}
	poolModel := strings.TrimPrefix(frozen.ModelRef, prefix)
	if wire := strings.TrimSuffix(poolModel, "[1m]"); wire != frozen.WireModel {
		t.Fatalf("model_ref implies wire model %q, but the block states %q", wire, frozen.WireModel)
	}

	entry := team.AgentUser{
		UserID: frozen.PoolEntry, Provider: frozen.Provider, Model: poolModel,
		BaseURL: frozen.Endpoint, Effort: frozen.Effort,
		APIKey: "not-a-real-key", // resolution is network-free and never dials
	}
	actual, err := resolveStrataIdentity(entry, memberProxySpec(team.ProxyConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	actual.Build = frozen.Build

	if err := strataPreflight(frozen.strata(), actual); err != nil {
		t.Fatalf("the pre-registration does not resolve to itself: %v", err)
	}
	if err := formalEffortCheck(frozen.Effort, entry); err != nil {
		t.Fatal(err)
	}
	for _, f := range strataIdentityFields(actual) {
		t.Logf("%-14s = %s", f.name, f.value)
	}
	t.Logf("%-14s = %s (compared by the driver, not by A's gate)", "effort", entry.Effort)
}

// formalRepoFile resolves a repository-relative path from this source file,
// not from the process working directory. go test runs a package's test binary
// with that package's directory as the working directory, so a relative path
// names a different file per package — and a digest that silently reads
// nothing is worse than no digest, because the banner then claims a
// measurement layer it never hashed.
func formalRepoFile(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed, so a repository-relative path cannot be resolved")
	}
	// internal/cli -> repo root
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, filepath.FromSlash(rel))
}

// formalFrozenLayerDigest names the measurement layer an arm's readings are
// taken under, resolved from the repository root. An unreadable file is
// refused outright rather than recorded as a placeholder: a banner whose
// digest field says "unreadable" cannot answer which fold rule produced the
// numbers, which is the one question the field exists for.
func formalFrozenLayerDigest(t *testing.T, rel string) string {
	t.Helper()
	digest := strataFrozenDigest(formalRepoFile(t, rel))
	if strings.HasPrefix(digest, "unreadable:") {
		t.Fatalf("measurement layer %s could not be read (%s), so this arm cannot name the fold rule it ran under", rel, digest)
	}
	return digest
}

// formalArchive writes the arm's evidence: the records exactly as the writers
// stored them, the report, and a banner naming the frozen pre-registration,
// the resolved identity and the arm's registration. Nothing here can carry
// prompt, tool argument or credential text: the records never held any, and
// the banner is built from identifiers the run already resolved.
func formalArchive(t *testing.T, dir, runID, clientBuild, preregDigest, frozenLayerDigest string, frozen formalFrozen, arm strataArm, requests []team.MemberCacheRequest, report team.CacheReport, started, finished time.Time, stopped string) {
	t.Helper()
	prefix := "formal-" + arm.ID + "-"
	recordsPath := filepath.Join(dir, prefix+"records-"+runID+".jsonl")
	f, err := os.OpenFile(recordsPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, rec := range requests {
		if err := enc.Encode(rec); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(dir, prefix+"report-"+runID+".json")
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	input := 0
	for _, rec := range requests {
		input += rec.PromptTokens
	}
	banner := strings.Join([]string{
		"arm: " + arm.ID,
		"run_id: " + runID,
		"started_utc: " + started.Format(time.RFC3339Nano),
		"finished_utc: " + finished.Format(time.RFC3339Nano),
		"build_commit: " + clientBuild,
		"prereg_digest_sha256: " + preregDigest,
		"frozen_stream_usage_sha256: " + frozenLayerDigest,
		"task_family: " + pilotTaskFamily,
		fmt.Sprintf("concurrency_level: %d (registered, not measured)", arm.Concurrency),
		"target_bucket: " + string(arm.TargetBucket),
		fmt.Sprintf("roster: %d members x %d turns, brief %d KB each", arm.Members, arm.Turns, arm.BriefKB),
		"members: " + strings.Join(strataMemberIDs(arm), ","),
		fmt.Sprintf("token_budget: %d, actual_input_tokens: %d", arm.TokenBudget, input),
		"stopped_early: " + strataOrNone(stopped),
		// The resolved identity, so a banner is readable without the pool file
		// it came from. None of these is a credential: the key never enters a
		// comparison field, a log line or this file.
		"identity_pool_entry: " + frozen.PoolEntry,
		"identity_kind: " + frozen.Kind,
		"identity_endpoint: " + frozen.Endpoint,
		"identity_wire_model: " + frozen.WireModel,
		"identity_model_ref: " + frozen.ModelRef,
		"identity_route_bucket: " + frozen.RouteBucket,
		"identity_effort: " + frozen.Effort,
		"records_file: " + filepath.Base(recordsPath),
		"report_file: " + filepath.Base(reportPath),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, prefix+"banner-"+runID+".txt"), []byte(banner), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("archived: %s", recordsPath)
	t.Logf("archived: %s", reportPath)
	t.Logf("input tokens: %d of %d budget", input, arm.TokenBudget)
}

// TestLiveTeamMemberCacheStrataFormal runs one registered arm under the frozen
// pre-registration's identity. It asserts only what this code owns — every
// member recorded a request per turn, every record names a route and a
// diagnosis, the request count was measured, the arm stayed inside its budget,
// and the report accounts for every sample — and logs the real numbers instead
// of asserting a hit rate, which is the provider's behaviour. Run one arm with:
//
//	REASONIX_LIVE_CACHE_PREREG=docs/...PREREGISTRATION_V2.zh-CN.md \
//	REASONIX_LIVE_CACHE_PREREG_SHA256=<recorded at sign-off> \
//	REASONIX_LIVE_CACHE_POOL_FILE=$HOME/.reasonix/team/agent_users.json \
//	REASONIX_LIVE_CACHE_ARCHIVE=$HOME/reasonix-partb-archive \
//	REASONIX_LIVE_CACHE_CLIENT_BUILD=$(git rev-parse --short=12 HEAD) \
//	REASONIX_LIVE_CACHE_FORMAL_ARM=S1a-small \
//	  go test -tags live ./internal/cli/ -run TestLiveTeamMemberCacheStrataFormal -v
func TestLiveTeamMemberCacheStrataFormal(t *testing.T) {
	preregPath := formalEnv(t, "REASONIX_LIVE_CACHE_PREREG")
	preregDigest := formalEnv(t, "REASONIX_LIVE_CACHE_PREREG_SHA256")
	poolFile := formalEnv(t, "REASONIX_LIVE_CACHE_POOL_FILE")
	frozen, err := formalLoadPreReg(preregPath, preregDigest)
	if err != nil {
		t.Fatal(err)
	}
	arm, err := formalArmByID(os.Getenv("REASONIX_LIVE_CACHE_FORMAL_ARM"))
	if err != nil {
		t.Fatal(err)
	}
	// The gate's own archive check, not the pilot-era helper: that one compares
	// EvalSymlinks output, which errors on a path that does not exist yet, so a
	// fresh path under the OS temp directory slipped through the rule.
	archiveRoot, err := strataArchiveDir(os.Getenv("REASONIX_LIVE_CACHE_ARCHIVE"))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(archiveRoot, time.Now().UTC().Format("2006-01-02"))
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	clientBuild := pilotBuildID(t)
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	// Resolved before any request: a measurement layer this run cannot name is
	// a reason not to spend, not a line to leave blank in the banner later.
	frozenLayerDigest := formalFrozenLayerDigest(t, "internal/provider/anthropic/stream_usage.go")

	// A private user state root: member skills and deliverable tools resolve
	// there, and a live run must not touch the operator's own state.
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	if dir := config.MemoryUserDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// The preflight runs against the operator's own record, not a copy of it:
	// validating a copy would only prove the copy is self-consistent. A's gate
	// compares seven fields; the effort check below covers the field it lacks.
	pool, err := formalPoolFile(poolFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := memberProxySpec(team.ProxyConfig{})
	if err := strataPreflightForPoolEntry(pool, frozen.PoolEntry, proxy, frozen.strata()); err != nil {
		t.Fatalf("the frozen pre-registration must pass its own gate before any request: %v", err)
	}
	entry, ok, err := pool.AgentUser(frozen.PoolEntry)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("pool entry %q vanished between the preflight and the run", frozen.PoolEntry)
	}
	if err := formalEffortCheck(frozen.Effort, entry); err != nil {
		t.Fatal(err)
	}

	owners, build, store, root := formalAssembly(t, arm, entry)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	started := time.Now().UTC()
	members := strataMemberIDs(arm)
	stopped := formalRun(t, ctx, owners, build, arm, entry.UserID, members)
	finished := time.Now().UTC()

	requests := waitForStrataRecords(t, owners, arm)
	roots := &teamDataRoots{store: store, owners: owners, dataDir: root}
	collected, sessionLedgers, err := collectCacheSamples(roots, cacheReportSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(collected) != len(requests) {
		t.Fatalf("the report reader collected %d of %d recorded requests", len(collected), len(requests))
	}
	report := team.BuildCacheReport(team.CacheReportInput{
		Requests: requests, Sessions: sessionLedgers,
		CodeVersion: "live", CodeCommit: clientBuild, GeneratedAt: time.Now(),
	})
	logStrataRecords(t, requests)
	t.Logf("report:\n%s", renderCacheReport(report))
	if stopped != "" {
		t.Logf("stopped early: %s", stopped)
	}
	formalArchive(t, archive, runID, clientBuild, preregDigest, frozenLayerDigest, frozen, arm, requests, report, started, finished, stopped)
	assertFormalRouteIsTheFrozenOne(t, frozen, report)
	assertStrataPipeline(t, arm, requests, report)
}

// assertFormalRouteIsTheFrozenOne checks the run landed on the route the
// pre-registration froze, not merely on one route. assertStrataPipeline only
// asserts "exactly one", which every arm satisfies by construction; this names
// the value. It cannot prove much — the bucket is derived client-side from the
// same entry the run was seeded with, so it holds unless some other path
// rewrote the records — and that limit is the point: the canary's real answers
// are whether the credential works and whether the account reports a split.
func assertFormalRouteIsTheFrozenOne(t *testing.T, frozen formalFrozen, report team.CacheReport) {
	t.Helper()
	if len(report.RouteBuckets) != 1 {
		t.Fatalf("arm recorded %d routes %v, but the frozen identity names one", len(report.RouteBuckets), report.RouteBuckets)
	}
	if got := report.RouteBuckets[0]; got != frozen.RouteBucket {
		t.Fatalf("the run landed on route %q, but the pre-registration froze %q — the records do not belong to the frozen route", got, frozen.RouteBucket)
	}
	t.Logf("every record carries the frozen route %s", frozen.RouteBucket)
}
