package boot

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/event"
)

// TestBuildExplicitKeylessModelErrorNamesConfigSource: the RequireKey failure
// for an explicit keyless ref names the requested model, the resolved provider
// route, the missing credential source, and the config file the route came
// from — so a user can fix the right env in the right file without hunting.
// The keyless-default reroute notice itself is covered by
// TestBuildNoticesSkippedKeylessDefaultModel (provider_errors_test.go).
func TestBuildExplicitKeylessModelErrorNamesConfigSource(t *testing.T) {
	const keylessEnv = "REASONIX_EXPLICIT_KEYLESS_CONFIG_SOURCE_KEY"
	const configuredEnv = "REASONIX_EXPLICIT_KEYLESS_CONFIG_SOURCE_CONFIGURED"

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
default_model = "minimax/MiniMax-M3"

[[providers]]
name = "deepseek"
kind = "openai"
base_url = "https://api.deepseek.com"
model = "deepseek-v4-flash"
api_key_env = "`+keylessEnv+`"

[[providers]]
name = "minimax"
kind = "openai"
base_url = "https://api.MiniMax.chat/v1"
model = "MiniMax-M3"
api_key_env = "`+configuredEnv+`"
`)

	_, err := Build(context.Background(), Options{
		Sink:       event.Discard,
		Model:      "deepseek/deepseek-v4-flash",
		RequireKey: true,
	})
	if err == nil {
		t.Fatal("explicit keyless ref must fail loudly even with a configured fallback available")
	}
	for _, want := range []string{
		`model "deepseek/deepseek-v4-flash" resolved to provider "deepseek"`,
		"kind openai",
		"https://api.deepseek.com",
		keylessEnv,
		"reasonix.toml",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %q", err.Error(), want)
		}
	}
}
