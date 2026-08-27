package boot

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/event"

	_ "reasonix/internal/provider/openai"
	_ "reasonix/internal/provider/responses"
)

// TestBuildRequireKeyMissingCredentialErrorIsStrict pins the P0.2 acceptance
// contract for the fail-fast gate: a missing member credential must fail with
// the requested model, resolved provider kind/name, route, and the member's
// own api_key_env — never a misreported global DEEPSEEK_API_KEY — and must not
// leave a half-built controller behind.
func TestBuildRequireKeyMissingCredentialErrorIsStrict(t *testing.T) {
	const memberEnv = "REASONIX_STRICT_PROVIDER_MEMBER_KEY_TEST"
	dir := robustTempDir(t)
	fenceBootTestHistoryCatalog(t)
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "member"

[[providers]]
name = "member"
kind = "openai"
base_url = "https://member.example.invalid/v1"
model = "member-3"
api_key_env = "`+memberEnv+`"
`)

	ctrl, err := Build(context.Background(), Options{Sink: event.Discard, RequireKey: true})
	if err == nil {
		t.Fatal("missing credential with RequireKey must fail fast")
	}
	if ctrl != nil {
		t.Fatal("a failed build must not return a controller")
	}
	msg := err.Error()
	for _, want := range []string{`"member"`, `"member"`, "openai", "https://member.example.invalid/v1", memberEnv} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q should mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "DEEPSEEK_API_KEY") {
		t.Fatalf("error %q must not misreport the member credential as the global DEEPSEEK_API_KEY", msg)
	}
}

// TestBuildRequireKeyUnknownKindFailsStrict: a configured kind no provider
// factory serves must fail at the RequireKey gate with strict route details,
// before any backend assembly, even though the entry has a valid credential.
func TestBuildRequireKeyUnknownKindFailsStrict(t *testing.T) {
	const keyEnv = "REASONIX_STRICT_PROVIDER_KIND_KEY_TEST"
	t.Setenv("REASONIX_HOME", t.TempDir())
	t.Setenv("REASONIX_CREDENTIALS_STORE", "file")
	if _, err := config.SetCredential(keyEnv, "sk-test"); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	dir := robustTempDir(t)
	fenceBootTestHistoryCatalog(t)
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "member"

[[providers]]
name = "member"
kind = "openai-legacy"
base_url = "https://member.example.invalid/v1"
model = "member-3"
api_key_env = "`+keyEnv+`"
`)

	ctrl, err := Build(context.Background(), Options{Sink: event.Discard, RequireKey: true})
	if err == nil {
		t.Fatal("unregistered kind with RequireKey must fail fast")
	}
	if ctrl != nil {
		t.Fatal("a failed build must not return a controller")
	}
	msg := err.Error()
	for _, want := range []string{`"member"`, "openai-legacy", "not registered", "https://member.example.invalid/v1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q should mention %q", msg, want)
		}
	}
	if !strings.Contains(msg, "registered:") {
		t.Fatalf("error %q should list the registered kinds for a corrective path", msg)
	}
}

// TestBuildValidProviderRoutesStillResolve is the compatibility regression for
// the strict-failure gate: every registered adapter kind that a user can
// configure must still build a backend end to end.
func TestBuildValidProviderRoutesStillResolve(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic", "responses"} {
		t.Run(kind, func(t *testing.T) {
			const keyEnv = "REASONIX_STRICT_PROVIDER_VALID_KEY_TEST"
			t.Setenv("REASONIX_HOME", t.TempDir())
			t.Setenv("REASONIX_CREDENTIALS_STORE", "file")
			if _, err := config.SetCredential(keyEnv, "sk-test"); err != nil {
				t.Fatalf("seed key: %v", err)
			}
			dir := robustTempDir(t)
			fenceBootTestHistoryCatalog(t)
			t.Chdir(dir)
			writeFile(t, dir, "reasonix.toml", `
default_model = "member"

[[providers]]
name = "member"
kind = "`+kind+`"
base_url = "https://member.example.invalid/v1"
model = "member-3"
api_key_env = "`+keyEnv+`"
`)

			ctrl, err := Build(context.Background(), Options{Sink: event.Discard, RequireKey: true})
			if err != nil {
				t.Fatalf("registered kind %q must still build: %v", kind, err)
			}
			defer ctrl.Close()
			if got := ctrl.Label(); got != "member-3" {
				t.Fatalf("controller label = %q, want member-3", got)
			}
		})
	}
}

// TestBuildNoticesSkippedKeylessDefaultModel: the #6996 keyless-default
// reroute is preserved, but the reroute must stay observable — the build names
// the skipped default's own credential env and the replacement model.
func TestBuildNoticesSkippedKeylessDefaultModel(t *testing.T) {
	const keylessEnv = "REASONIX_STRICT_SKIPPED_KEYLESS_ENV"
	const configuredEnv = "REASONIX_STRICT_SKIPPED_CONFIGURED_ENV"

	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("REASONIX_CREDENTIALS_STORE", "file")
	if _, err := config.SetCredential(configuredEnv, "sk-test"); err != nil {
		t.Fatalf("seed configured key: %v", err)
	}

	dir := robustTempDir(t)
	fenceBootTestHistoryCatalog(t)
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "deepseek/deepseek-v4-flash"

[[providers]]
name = "deepseek"
kind = "openai"
base_url = "https://api.deepseek.com"
model = "deepseek-v4-flash"
api_key_env = "`+keylessEnv+`"

[[providers]]
name = "audio"
kind = "openai"
base_url = "https://audio.example.com/v1"
model = "tts-1"
api_key_env = "`+configuredEnv+`"

[[providers]]
name = "minimax"
kind = "openai"
base_url = "https://api.MiniMax.chat/v1"
model = "MiniMax-M3"
api_key_env = "`+configuredEnv+`"
`)

	var notices []event.Event
	ctrl, err := Build(context.Background(), Options{
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.Notice {
				notices = append(notices, e)
			}
		}),
	})
	if err != nil {
		t.Fatalf("Build should fall back to a configured provider: %v", err)
	}
	defer ctrl.Close()

	for _, n := range notices {
		if n.Level == event.LevelWarn && n.Text == "Skipped a keyless default_model." &&
			strings.Contains(n.Detail, keylessEnv) &&
			strings.Contains(n.Detail, "MiniMax-M3") &&
			strings.Contains(n.Detail, `"deepseek/deepseek-v4-flash"`) &&
			strings.Contains(n.Detail, `"minimax/MiniMax-M3"`) &&
			strings.Contains(n.Detail, "reasonix.toml") {
			if got, want := ctrl.Label(), "MiniMax-M3"; got != want {
				t.Fatalf("controller label = %q, want %q (fallback route preserved)", got, want)
			}
			return
		}
	}
	t.Fatalf("expected a warn notice naming the skipped keyless default, its credential, the replacement, and the config file; got %v", notices)
}
