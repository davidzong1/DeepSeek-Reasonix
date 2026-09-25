package cli

// Offline tests for the formal strata preflight. Nothing here dials a provider:
// the one test that talks to a socket talks to a local listener that exists to
// prove the gate does not.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// frozenTestEntry is a pool entry shaped exactly like the one the pre-registration
// names, so the positive cases exercise the real resolution chain.
func frozenTestEntry() team.AgentUser {
	return team.AgentUser{
		UserID: "deepseek-v4-flash-roojin", Provider: "deepseek",
		Model: "deepseek/deepseek-v4.1-flash[1m]", BaseURL: "https://gw.example",
		APIKey: "sk-not-a-real-key",
	}
}

// frozenTestIdentity resolves that entry and pins the build, so each test starts
// from a genuinely frozen expectation rather than a hand-written one.
func frozenTestIdentity(t *testing.T) strataIdentity {
	t.Helper()
	t.Setenv("REASONIX_LIVE_CACHE_CLIENT_BUILD", "0123456789ab")
	id, err := resolveStrataIdentity(frozenTestEntry(), netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	id.Build = strataBuildIdentity()
	return id
}

// TestStrataPreflightPassesOnTheFrozenIdentity is the positive case: the entry
// the pre-registration names, resolved through the builder's own path, matches
// the identity the pre-registration froze.
func TestStrataPreflightPassesOnTheFrozenIdentity(t *testing.T) {
	expected := frozenTestIdentity(t)
	pool := fakePool{users: map[string]team.AgentUser{expected.PoolEntry: frozenTestEntry()}}

	if err := strataPreflightForPoolEntry(pool, expected.PoolEntry, netclient.ProxySpec{Mode: netclient.ModeOff}, expected); err != nil {
		t.Fatalf("the frozen identity must pass its own gate: %v", err)
	}
	// The gate must be reading the pool entry, not the expectation: resolving the
	// same entry twice has to land on the same identity.
	if expected.RouteBucket == "" || expected.ModelRef == "" || expected.WireModel == "" {
		t.Fatalf("resolution produced an incomplete identity: %+v", expected)
	}
}

// TestStrataPreflightRefusesEveryFieldDrift walks the identity one field at a
// time. Each case changes exactly one thing, so a gate that compared only the
// route bucket — or only the model string — would fail this test rather than
// pass an arm on a route nobody registered.
//
// Two cases re-point the pool entry instead of the expectation, because that is
// how the drift actually arrives: the pre-registration stays as frozen and the
// entry underneath it moves.
func TestStrataPreflightRefusesEveryFieldDrift(t *testing.T) {
	base := frozenTestIdentity(t)
	cases := []struct {
		name   string
		drift  func(*strataIdentity)
		user   func(*team.AgentUser)
		expect string
	}{
		{
			name:   "another pool entry",
			drift:  func(id *strataIdentity) { id.PoolEntry = "some-other-entry" },
			expect: "pool entry",
		},
		{
			name:   "another wire model",
			drift:  func(id *strataIdentity) { id.WireModel = "deepseek/deepseek-v4.1-pro" },
			expect: "wire model",
		},
		{
			name:   "another model ref spelling",
			drift:  func(id *strataIdentity) { id.ModelRef = base.PoolEntry + "/deepseek/deepseek-v4.1-flash" },
			expect: "model ref",
		},
		{
			name:   "another route bucket",
			drift:  func(id *strataIdentity) { id.RouteBucket = "anthropic/000000000000" },
			expect: "route bucket",
		},
		{
			name:   "another build",
			drift:  func(id *strataIdentity) { id.Build = "ffffffffffff" },
			expect: "build",
		},
		{
			// The expectation stays frozen and the entry is re-pointed at another
			// host — the route change an operator makes by editing one field.
			name:   "an entry re-pointed at another endpoint",
			user:   func(u *team.AgentUser) { u.BaseURL = "https://elsewhere.example" },
			expect: "endpoint",
		},
		{
			// A provider change that leaves the DeepSeek family moves the wire
			// kind, and with it the route bucket.
			name:   "an entry moved to another provider",
			user:   func(u *team.AgentUser) { u.Provider = "openai" },
			expect: "provider kind",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := base
			if tc.drift != nil {
				tc.drift(&expected)
			}
			user := frozenTestEntry()
			if tc.user != nil {
				tc.user(&user)
			}
			pool := fakePool{users: map[string]team.AgentUser{user.UserID: user}}

			err := strataPreflightForPoolEntry(pool, user.UserID, netclient.ProxySpec{Mode: netclient.ModeOff}, expected)
			if err == nil {
				t.Fatalf("a %s drift must refuse the arm", tc.expect)
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("the refusal must name the %s field: %v", tc.expect, err)
			}
			if !strings.Contains(err.Error(), "must not run") {
				t.Errorf("the refusal must say the arm does not run: %v", err)
			}
		})
	}
}

// TestStrataPreflightRefusesAMissingPoolEntry pins the substitution rule: an
// absent entry is refused by name. Falling back to a neighbouring entry is how
// a run ends up on a route nobody pre-registered, so it must not happen here.
func TestStrataPreflightRefusesAMissingPoolEntry(t *testing.T) {
	expected := frozenTestIdentity(t)
	pool := fakePool{users: map[string]team.AgentUser{}} // empty pool

	err := strataPreflightForPoolEntry(pool, expected.PoolEntry, netclient.ProxySpec{}, expected)
	if err == nil {
		t.Fatal("a missing pool entry must refuse the arm")
	}
	if !strings.Contains(err.Error(), expected.PoolEntry) {
		t.Errorf("the refusal must name the missing entry: %v", err)
	}
	if !strings.Contains(err.Error(), "must not substitute") {
		t.Errorf("the refusal must rule out substitution: %v", err)
	}

	// A lookup error is surfaced, never swallowed into a pass.
	broken := fakePool{err: errors.New("pool unreadable")}
	if err := strataPreflightForPoolEntry(broken, "any", netclient.ProxySpec{}, expected); err == nil {
		t.Fatal("a pool read failure must refuse the arm rather than pass it")
	}
}

// TestStrataPreflightRefusesAnUnfrozenExpectation pins the fail-closed default.
// An empty field is not "no opinion": a pre-registration that was never loaded
// would otherwise satisfy every comparison, and an unnamed build cannot be
// pinned to anything.
func TestStrataPreflightRefusesAnUnfrozenExpectation(t *testing.T) {
	base := frozenTestIdentity(t)
	for _, tc := range []struct {
		name   string
		blank  func(*strataIdentity)
		expect string
	}{
		{"no pool entry", func(id *strataIdentity) { id.PoolEntry = "" }, "pool entry"},
		{"no route bucket", func(id *strataIdentity) { id.RouteBucket = "" }, "route bucket"},
		{"no endpoint", func(id *strataIdentity) { id.Endpoint = "" }, "endpoint"},
		{"no build", func(id *strataIdentity) { id.Build = "" }, "build"},
		{"unknown build", func(id *strataIdentity) { id.Build = "unknown" }, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := base
			tc.blank(&expected)
			err := strataPreflight(expected, base)
			if err == nil {
				t.Fatal("an unfrozen expectation must be refused, not satisfied by default")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("the refusal must name what is missing (%q): %v", tc.expect, err)
			}
			if !strings.Contains(err.Error(), "not frozen") {
				t.Errorf("the refusal must say the expectation is not frozen: %v", err)
			}
		})
	}
}

// TestStrataPreflightDiagnosticsCarryNoSecret pins the privacy boundary the plan
// requires of every preflight output. The endpoint is compared in full but
// printed as scheme and host only, because a URL can carry credentials or a
// sensitive query; the credential itself is never an identity field at all.
func TestStrataPreflightDiagnosticsCarryNoSecret(t *testing.T) {
	const secret = "sk-super-secret-value"
	user := team.AgentUser{
		UserID: "acct", Provider: "deepseek", Model: "deepseek/deepseek-v4.1-flash[1m]",
		BaseURL: "https://gw.example/anthropic?api_key=" + secret, APIKey: secret,
	}
	expected, err := resolveStrataIdentity(user, netclient.ProxySpec{})
	if err != nil {
		t.Fatal(err)
	}
	expected.Build = "0123456789ab"
	// Force the one refusal that prints both sides of the endpoint comparison.
	actual := expected
	actual.Endpoint = "https://other.example/anthropic?api_key=" + secret

	gateErr := strataPreflight(expected, actual)
	if gateErr == nil {
		t.Fatal("the endpoint drift must be refused")
	}
	msg := gateErr.Error()
	for _, forbidden := range []string{secret, "api_key", "?api_key"} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("the diagnostic carries %q: %s", forbidden, msg)
		}
	}
	if !strings.Contains(msg, "gw.example") || !strings.Contains(msg, "other.example") {
		t.Errorf("the diagnostic must still name the two hosts: %s", msg)
	}

	// The credential is not an identity input at all: rotating it must leave the
	// identity unchanged, so it can never appear in a comparison or a refusal.
	rotated := user
	rotated.APIKey = "sk-a-different-secret"
	rotatedID, err := resolveStrataIdentity(rotated, netclient.ProxySpec{})
	if err != nil {
		t.Fatal(err)
	}
	if rotatedID != (func() strataIdentity { id := expected; id.Build = ""; return id })() {
		t.Errorf("rotating the credential changed the identity: %+v vs %+v", rotatedID, expected)
	}
}

// TestStrataPreflightSendsNothing is the fail-before-transport claim. A proxy
// spec is the one seam the adapter's transport reads from the pool entry's
// resolver, so a listener standing in as the proxy counts any dial the gate
// makes. The control below proves the tripwire fires when something does dial,
// so "no hits" is evidence rather than a dead listener.
func TestStrataPreflightSendsNothing(t *testing.T) {
	t.Setenv("REASONIX_LIVE_CACHE_CLIENT_BUILD", "0123456789ab")
	var hits atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "the preflight must not reach here", http.StatusBadGateway)
	}))
	defer proxy.Close()

	spec := netclient.ProxySpec{Mode: netclient.ModeCustom, URL: proxy.URL}
	user := frozenTestEntry()
	user.BaseURL = "https://gw.example"
	user.APIKey = "sk-not-a-real-key"

	// Control: the same proxy spec, driven through the adapter the gate builds,
	// must trip the tripwire. Without this the assertion below would pass on a
	// listener that could never have been reached anyway.
	control, err := provider.New("anthropic", provider.Config{
		Name: "control", BaseURL: user.BaseURL, Model: "deepseek/deepseek-v4.1-flash",
		APIKey: user.APIKey, Extra: map[string]any{"proxy_spec": spec},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := control.Stream(ctx, provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "control"}}, MaxTokens: 1,
	}); err == nil {
		t.Log("control dial completed against the stand-in proxy (the error is the point, not the reply)")
	}
	if hits.Load() == 0 {
		t.Fatal("the tripwire never fired on a real dial, so it cannot prove the gate's silence")
	}

	// The gate itself, on the same spec.
	hits.Store(0)
	expected, err := resolveStrataIdentity(user, spec)
	if err != nil {
		t.Fatal(err)
	}
	expected.Build = "0123456789ab"
	pool := fakePool{users: map[string]team.AgentUser{user.UserID: user}}
	if err := strataPreflightForPoolEntry(pool, user.UserID, spec, expected); err != nil {
		t.Fatalf("the frozen identity must pass: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the preflight sent %d request(s) before deciding: it must fail before the transport", n)
	}

	// And on the refusal path, which is the one that matters: a drifted arm must
	// be refused without a dial too.
	drifted := expected
	drifted.RouteBucket = "anthropic/000000000000"
	if err := strataPreflightForPoolEntry(pool, user.UserID, spec, drifted); err == nil {
		t.Fatal("the drift must be refused")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the refusing preflight sent %d request(s): a refused arm must cost nothing", n)
	}
}

// TestStrataPreflightRefusesAnUnsupportedPoolEntry pins that a pool entry the
// adapter cannot construct is refused here rather than at the first request,
// which is the whole reason the check exists.
func TestStrataPreflightRefusesAnUnsupportedPoolEntry(t *testing.T) {
	user := frozenTestEntry()
	user.Provider = "" // unconfigured entry
	expected := frozenTestIdentity(t)
	pool := fakePool{users: map[string]team.AgentUser{user.UserID: user}}

	err := strataPreflightForPoolEntry(pool, user.UserID, netclient.ProxySpec{}, expected)
	if err == nil {
		t.Fatal("an entry with no provider must be refused before the transport")
	}
	if !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("the refusal must say the entry did not resolve: %v", err)
	}
}

// TestStrataPreflightChecksCredentialPresenceNotItsValue pins the credential
// rule the plan sets: a preflight verifies that a credential is usable, as a
// boolean, and never prints its value or an auth header. The check is delegated
// to the assembly's own function, so the gate and the member builder agree on
// what counts as a declared source; a secret-store ref counts, and a keyless
// entry is refused here rather than at the first billable request.
func TestStrataPreflightChecksCredentialPresenceNotItsValue(t *testing.T) {
	expected := frozenTestIdentity(t)

	keyless := frozenTestEntry()
	keyless.APIKey = ""
	pool := fakePool{users: map[string]team.AgentUser{keyless.UserID: keyless}}
	err := strataPreflightForPoolEntry(pool, keyless.UserID, netclient.ProxySpec{}, expected)
	if err == nil {
		t.Fatal("a keyless pool entry must be refused before the transport")
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Errorf("the refusal must say the entry declares no credential: %v", err)
	}

	// A secret-store reference is a declared source, and the identity is the same
	// either way — the credential is not an input to any compared field.
	byRef := frozenTestEntry()
	byRef.APIKey = ""
	byRef.SecretRef = team.SecretRef{StoreID: "kv/team-alpha"}
	refID, err := resolveStrataIdentity(byRef, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	refID.Build = expected.Build
	if refID != expected {
		t.Errorf("switching from an inline key to a secret-store ref changed the identity: %+v vs %+v", refID, expected)
	}
}

// TestStrataPreflightResolvesTheFrozenPoolEntry is the Gate 1 worksheet: it
// resolves the pool entry the pre-registration names, on the endpoint it names,
// and logs every identity field. The values are what an operator pastes into a
// corrected pre-registration — or compares against the frozen one to see the
// mismatch. The credential is deliberately absent: it is not an identity input,
// and the entry's own presence check is a separate, boolean question.
func TestStrataPreflightResolvesTheFrozenPoolEntry(t *testing.T) {
	entry := frozenTestEntry()
	entry.BaseURL = "https://aiapi.lejurobot.com" // the frozen gateway, no /v1
	entry.Effort = "max"                          // as stored in the pool

	id, err := resolveStrataIdentity(entry, memberProxySpec(team.ProxyConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	id.Build = "0123456789ab"
	for _, f := range strataIdentityFields(id) {
		t.Logf("%-14s = %s", f.name, f.value)
	}
	t.Logf("archive dir check would also require a durable destination; see TestStrataPreflightRefusesAnUnusableArchiveDir")
}

// TestStrataPreflightRefusesTheFrozenConditionAsWritten pins a finding this
// work package produced by resolving the pre-registration's own two frozen
// fields. The frozen condition names pool entry `deepseek-v4-flash-roojin` AND
// route bucket `anthropic/bfcb0811b1c8`, but the route bucket is a hash of the
// pool entry among other things: that entry resolves to a different bucket, and
// `anthropic/bfcb0811b1c8` is what the pilot's own `pilot-gw` entry produced.
// The two frozen fields therefore cannot both hold, so the "re-run on the
// frozen condition" path is not executable as written and the gate refuses it
// instead of silently satisfying one half.
func TestStrataPreflightRefusesTheFrozenConditionAsWritten(t *testing.T) {
	// Field 1 of the frozen condition: the pool entry.
	entry := frozenTestEntry()
	entry.BaseURL = "https://aiapi.lejurobot.com" // the frozen gateway, no /v1
	actual, err := resolveStrataIdentity(entry, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	actual.Build = "0123456789ab"

	// Field 2 of the frozen condition: the route bucket.
	frozen := actual
	frozen.RouteBucket = "anthropic/bfcb0811b1c8"

	if actual.RouteBucket == frozen.RouteBucket {
		t.Skip("the two frozen fields are now consistent; the report's finding needs re-checking")
	}
	err = strataPreflight(frozen, actual)
	if err == nil {
		t.Fatal("the pre-registration's own frozen condition must be refused: its pool entry and its route bucket cannot both hold")
	}
	if !strings.Contains(err.Error(), "route bucket") {
		t.Errorf("the refusal must name the route bucket: %v", err)
	}
	// The bucket that entry really resolves to, so the report and this test agree
	// on the number an operator has to freeze instead.
	t.Logf("frozen condition names entry %q with bucket %q, but that entry resolves to %q",
		entry.UserID, frozen.RouteBucket, actual.RouteBucket)
}

// TestStrataPreflightRefusesAnUnusableArchiveDir pins the archive precondition
// the plan puts in every arm's banner: the destination must exist, be writable,
// and not be the OS temp directory, which the plan forbids as an experiment's
// only copy. A run that discovers this after its requests were paid for has
// already spent the budget on evidence it cannot keep.
func TestStrataPreflightRefusesAnUnusableArchiveDir(t *testing.T) {
	// The production entry point reads the real disposable root, and a test's own
	// TempDir is under it — so this asserts the rule on a directory that is
	// genuinely disposable rather than a stand-in for one.
	if _, err := strataArchiveDir(t.TempDir()); err == nil {
		t.Fatal("a directory under the OS temp directory must be refused")
	} else if !strings.Contains(err.Error(), "temp directory") {
		t.Errorf("the refusal must name the temp directory: %v", err)
	}
	if _, err := strataArchiveDir(""); err == nil {
		t.Fatal("an unset archive directory must be refused")
	} else if !strings.Contains(err.Error(), "only copy is disposable") {
		t.Errorf("the refusal must explain why an unset destination is unsafe: %v", err)
	}

	// The remaining cases pass the disposable root in, so they can exercise
	// acceptance and the boundary without writing outside the test's own tree.
	root := t.TempDir()
	disposable := filepath.Join(root, "tmp")
	durable := filepath.Join(root, "archive")

	if dir, err := strataArchiveDirAvoiding(durable, disposable); err != nil {
		t.Fatalf("a durable directory must be accepted: %v", err)
	} else if dir != durable {
		t.Fatalf("the accepted directory is %q, want %q returned for the banner", dir, durable)
	}
	if _, err := strataArchiveDirAvoiding(filepath.Join(disposable, "runs"), disposable); err == nil {
		t.Fatal("a directory under the disposable root must be refused")
	}
	if _, err := strataArchiveDirAvoiding(disposable, disposable); err == nil {
		t.Fatal("the disposable root itself must be refused")
	}

	// A file where a directory belongs is not writable storage.
	file := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := strataArchiveDirAvoiding(filepath.Join(file, "archive"), disposable); err == nil {
		t.Fatal("a path under a regular file must be refused")
	}
}

// removes the plan's identity-probe contingency: the route bucket is computed
// client-side from the resolved entry, so it is known offline and needs no
// provider response. The pair asserted here is the archived one — the pilot's
// 30/30 records carry `anthropic/bfcb0811b1c8` for pool entry `pilot-gw` on
// this endpoint, and the arithmetic below reproduces that value without a
// network call. If the bucket ever stops being derivable offline, this test
// fails and the plan's probe provision has to come back.
func TestStrataPreflightRouteBucketIsKnownBeforeAnyRequest(t *testing.T) {
	entry := frozenTestEntry()
	entry.UserID = "pilot-gw"
	entry.BaseURL = "https://aiapi.lejurobot.com"
	id, err := resolveStrataIdentity(entry, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	const archived = "anthropic/bfcb0811b1c8"
	if id.RouteBucket != archived {
		t.Fatalf("pool entry %q resolved to %q, but the archived pilot records carry %q — either the bucket's "+
			"inputs changed or the archive and this build disagree", entry.UserID, id.RouteBucket, archived)
	}
	t.Logf("route bucket %s is reproducible offline from the pool entry alone", id.RouteBucket)
}

// TestStrataPreflightRouteBucketDoesNotBindTheModel pins a real limit of the
// bucket, and the reason the gate compares more than it. Two wire models in the
// same provider family hash to the same bucket, so a bucket-only check would
// pass an arm that had quietly moved to another model. The gate's separate
// wire-model and model-ref fields are what catch that.
func TestStrataPreflightRouteBucketDoesNotBindTheModel(t *testing.T) {
	flash := frozenTestEntry()
	pro := frozenTestEntry()
	pro.Model = "deepseek/deepseek-v4.1-pro[1m]"

	flashID, err := resolveStrataIdentity(flash, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	proID, err := resolveStrataIdentity(pro, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if flashID.RouteBucket != proID.RouteBucket {
		t.Skipf("the bucket now separates these two models (%s vs %s); the report's limit note needs updating",
			flashID.RouteBucket, proID.RouteBucket)
	}
	if flashID.WireModel == proID.WireModel {
		t.Fatal("the two models must differ on the wire model field")
	}
	proID.Build = flashID.Build
	if err := strataPreflight(flashID, proID); err == nil {
		t.Fatal("a model drift must be refused even though the route bucket is unchanged")
	}
	t.Logf("both models share bucket %s; the wire-model comparison is what refuses the drift", flashID.RouteBucket)
}

// TestStrataPreflightIsNotThePoolEntryNameAlone pins the reason the gate
// compares a derived identity instead of a name. Two entries whose names differ
// only by spelling are two different routes: the pool entry is an input to the
// route bucket, so an operator who "just renames" the entry silently moves the
// arm off its registered cache scope.
func TestStrataPreflightIsNotThePoolEntryNameAlone(t *testing.T) {
	expected := frozenTestIdentity(t)
	renamed := frozenTestEntry()
	renamed.UserID = expected.PoolEntry + "-copy"

	actual, err := resolveStrataIdentity(renamed, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if actual.RouteBucket == expected.RouteBucket {
		t.Fatalf("renaming the pool entry kept the route bucket %q; if the bucket no longer binds the entry, "+
			"the gate's premise needs re-checking", actual.RouteBucket)
	}
	actual.Build = expected.Build
	if err := strataPreflight(expected, actual); err == nil {
		t.Fatal("a renamed pool entry must be refused: it is a different route")
	}
}

// TestStrataPreflightRefusesAnEndpointSpellingThatDialsTheSameHost records the
// second finding: the route bucket hashes the endpoint string as stored, so two
// entries that the Anthropic adapter sends to the same URL — it strips a
// trailing "/v1" — are still two cache scopes. That is the correct reading for
// a provider cache key, but it means the frozen endpoint spelling is part of the
// identity, not a cosmetic detail to normalise away.
func TestStrataPreflightRefusesAnEndpointSpellingThatDialsTheSameHost(t *testing.T) {
	plain := frozenTestEntry()
	plain.BaseURL = "https://aiapi.lejurobot.com"
	withV1 := plain
	withV1.BaseURL = "https://aiapi.lejurobot.com/v1"

	plainID, err := resolveStrataIdentity(plain, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	v1ID, err := resolveStrataIdentity(withV1, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if plainID.RouteBucket == v1ID.RouteBucket {
		t.Fatalf("both endpoint spellings produced %q; if the bucket no longer binds the stored endpoint, "+
			"this test's premise needs re-checking", plainID.RouteBucket)
	}
	v1ID.Build = plainID.Build
	if err := strataPreflight(plainID, v1ID); err == nil {
		t.Fatal("a re-spelled endpoint must be refused: it is a different cache scope")
	}
	// The preflight compares the stored strings, which is what makes it refuse —
	// the diagnostic still shows one host, because both spellings dial it.
	t.Logf("stored endpoints %q and %q are different cache scopes (%s vs %s)",
		plainID.Endpoint, v1ID.Endpoint, plainID.RouteBucket, v1ID.RouteBucket)
}
