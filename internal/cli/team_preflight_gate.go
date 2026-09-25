package cli

// Preflight for a formal Team strata arm: the frozen identity a run must be
// taken under, resolved from the pool entry through the member builder's own
// path and compared before any request exists.

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// strataIdentity is one arm's identity: what the frozen pre-registration says
// the run must be, and — after resolution — what its pool entry actually is.
// Every field is compared, so a drift in any one of them refuses the arm
// before it can send a billable request.
type strataIdentity struct {
	// PoolEntry is the AgentUserRef a member binds. It is an input to the
	// route bucket, so two entries on one endpoint are two routes.
	PoolEntry string
	// Kind is the wire adapter the entry resolves to: anthropic or openai.
	Kind string
	// Endpoint is the endpoint root the entry dials. Compared in full,
	// printed redacted: a URL may carry userinfo or a sensitive query.
	Endpoint string
	// WireModel is the model as it goes on the wire. The [1m] alias is a
	// client-side context spelling and is stripped before the request.
	WireModel string
	// ModelRef is the ref the member builder hands boot: the pool entry id
	// joined to the entry's own model spelling, [1m] included. It is what the
	// member records carry, and it is NOT the wire model.
	ModelRef string
	// RouteBucket is the provider cache scope, derived client-side from the
	// adapter, endpoint, pool entry and proxy. It is the label a cache
	// baseline stratifies by.
	RouteBucket string
	// Build is the build commit the run must be taken on. It comes from the
	// binary, not from the pool entry, so resolution fills it separately.
	Build string
}

// strataIdentityField is one named half of the identity. The gate and the
// frozen-check both walk this list, so neither can grow a field the other
// forgets to compare.
type strataIdentityField struct{ name, value string }

// strataIdentityFields lists the identity in comparison order.
func strataIdentityFields(id strataIdentity) []strataIdentityField {
	return []strataIdentityField{
		{"pool entry", id.PoolEntry},
		{"provider kind", id.Kind},
		{"endpoint", id.Endpoint},
		{"wire model", id.WireModel},
		{"model ref", id.ModelRef},
		{"route bucket", id.RouteBucket},
		{"build", id.Build},
	}
}

// strataPreflightForPoolEntry is the gate's entry point for one arm: it reads
// the pool entry the arm binds, resolves it, and compares the result against
// the frozen expectation. A missing entry is refused by name rather than
// substituted — falling back to a neighbouring entry is exactly how a run ends
// up on a route nobody pre-registered.
func strataPreflightForPoolEntry(users memberPoolLookup, ref string, proxy netclient.ProxySpec, expected strataIdentity) error {
	// The expectation is validated before the pool is touched, so an unfrozen
	// one is reported as unfrozen rather than as whatever the pool happens to
	// say about an entry nobody named.
	if err := strataExpectationIsFrozen(expected); err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("strata preflight: the arm binds no pool entry")
	}
	user, ok, err := users.AgentUser(ref)
	if err != nil {
		return fmt.Errorf("strata preflight: reading pool entry %q: %w", ref, err)
	}
	if !ok {
		return fmt.Errorf("strata preflight: pool entry %q does not exist — the arm must not run and must not substitute another entry", ref)
	}
	return strataPreflightAgainstEntry(user, proxy, expected)
}

// strataPreflightAgainstEntry gates an already-read pool entry against the
// frozen expectation. It is the shared half of both entry points, so a caller
// that has read the entry (and will record it) cannot drift from the one that
// reads it here.
func strataPreflightAgainstEntry(user team.AgentUser, proxy netclient.ProxySpec, expected strataIdentity) error {
	if err := strataExpectationIsFrozen(expected); err != nil {
		return err
	}
	// The credential is checked as a presence boolean by the assembly's own
	// function, so the gate and the member builder cannot disagree about what
	// counts as a declared credential source. Its value is never read.
	if err := memberCredentialError(team.MemberBinding{Team: expected.PoolEntry, MemberID: expected.PoolEntry, AgentUserRef: user.UserID}, user); err != nil {
		return fmt.Errorf("strata preflight: %w", err)
	}
	actual, err := resolveStrataIdentity(user, proxy)
	if err != nil {
		return err
	}
	actual.Build = strataBuildIdentity()
	return strataPreflight(expected, actual)
}

// resolveStrataIdentity derives the identity a pool entry actually resolves to,
// through the resolver the member builder itself uses. It is network-free: the
// resolver dials nothing, and the adapter construction it triggers builds a
// transport without using it. The build half is filled by the caller, because
// no pool entry knows which binary is running.
func resolveStrataIdentity(u team.AgentUser, proxy netclient.ProxySpec) (strataIdentity, error) {
	resolver, err := newMemberProviderResolver(u, proxy)
	if err != nil {
		return strataIdentity{}, fmt.Errorf("strata preflight: pool entry does not resolve: %w", err)
	}
	// Construction only, and the adapter owns its own model and effort
	// vocabulary, so asking it is the one check that cannot drift from the real
	// assembly. TestStrataPreflightSendsNothing holds the "no dial" claim.
	if _, err := resolver.Resolve(provider.Selection{}); err != nil {
		return strataIdentity{}, fmt.Errorf("strata preflight: adapter refused the pool entry: %w", err)
	}
	return strataIdentity{
		PoolEntry:   resolver.name,
		Kind:        resolver.kind,
		Endpoint:    resolver.endpoint,
		WireModel:   resolver.model,
		ModelRef:    resolver.ref,
		RouteBucket: resolver.RouteBucket(),
	}, nil
}

// strataBuildIdentity is the build this process is running, from the stamp the
// version command reports. A test binary carries no VCS stamp, so the live
// driver's explicit variable wins; without either the answer is "unknown", and
// the gate refuses an unknown build rather than reading it as a match.
func strataBuildIdentity() string {
	if build := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_CLIENT_BUILD")); build != "" {
		return build
	}
	if rev, ok := buildSetting("vcs.revision"); ok {
		return shortGitRevision(rev)
	}
	return "unknown"
}

// strataPreflight compares a frozen identity against the resolved one and fails
// closed on the first difference. It never falls back to a neighbouring pool
// entry, never retries another route and never rewrites the expectation: a
// mismatch is the arm's answer, and the operator either fixes the pool entry or
// freezes a new pre-registration version.
func strataPreflight(expected, actual strataIdentity) error {
	if err := strataExpectationIsFrozen(expected); err != nil {
		return err
	}
	want, got := strataIdentityFields(expected), strataIdentityFields(actual)
	for i := range want {
		if want[i].value != got[i].value {
			return fmt.Errorf(
				"strata preflight: %s drifted: expected %q, resolved %q — the arm must not run; fix the pool entry or freeze a new pre-registration version",
				want[i].name, strataDisplay(want[i].name, want[i].value), strataDisplay(got[i].name, got[i].value))
		}
	}
	return nil
}

// strataExpectationIsFrozen refuses an expectation that is not a frozen one. An
// empty field is not "no opinion": it is a pre-registration that was never
// loaded, and accepting it would let the gate pass by default — the exact
// failure it exists to prevent. An unknown build is refused for the same
// reason: it cannot be compared, so it cannot be satisfied.
func strataExpectationIsFrozen(expected strataIdentity) error {
	var missing []string
	for _, f := range strataIdentityFields(expected) {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"strata preflight: the expected identity is not frozen — %s empty; load it from the frozen pre-registration rather than defaulting it",
			strings.Join(missing, ", "))
	}
	if expected.Build == "unknown" {
		return fmt.Errorf("strata preflight: the expected build is unknown, so the expectation is not frozen; a run cannot be pinned to a build it cannot name")
	}
	return nil
}

// strataDisplay renders one identity value for a diagnostic. An endpoint is
// reduced to scheme and host, because the full value is what gets compared but
// a URL may carry credentials or a sensitive query that must not reach a log or
// a report. Every other field is already a non-secret identifier.
func strataDisplay(field, value string) string {
	if field != "endpoint" {
		return value
	}
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" {
		return "unparseable-endpoint"
	}
	return u.Scheme + "://" + u.Host
}

// strataArchiveDir checks the arm's archive destination before it is used. The
// plan forbids the OS temp directory as an experiment's only copy — a run whose
// evidence dies with the process produced no evidence — and an unwritable
// destination must fail here rather than after the requests were paid for. The
// path itself is returned so a banner can record where the evidence landed; it
// is a directory, never a credential.
func strataArchiveDir(dir string) (string, error) {
	return strataArchiveDirAvoiding(dir, os.TempDir())
}

// strataArchiveDirAvoiding is strataArchiveDir with the disposable root passed
// in, so the check is testable without writing outside the test's own tree.
// A path may reach the disposable root through a symlink, so both sides are
// resolved before they are compared rather than trusting the spelling.
func strataArchiveDirAvoiding(dir, disposable string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("strata preflight: no archive directory given; the plan forbids an experiment whose only copy is disposable")
	}
	if resolved, err := filepath.EvalSymlinks(disposable); err == nil {
		disposable = resolved
	}
	resolved := dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		resolved = r
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("strata preflight: archive directory %q is unusable: %w", dir, err)
	}
	if resolved == disposable || strings.HasPrefix(resolved, disposable+string(filepath.Separator)) {
		return "", fmt.Errorf("strata preflight: archive directory %q is under the OS temp directory, which the plan forbids as an experiment's only copy", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("strata preflight: archive directory %q cannot be created: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".preflight-writable-*")
	if err != nil {
		return "", fmt.Errorf("strata preflight: archive directory %q is not writable: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	if err := os.Remove(name); err != nil {
		return "", fmt.Errorf("strata preflight: archive directory %q accepted a probe file it cannot remove: %w", dir, err)
	}
	return dir, nil
}
