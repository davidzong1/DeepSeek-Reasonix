package boot

// The host-tool channel (Options.ExtraTools) must reach the provider-visible
// surface without moving the cache-stable system prefix. Boot may not import a
// frontend, so the seam is pinned with a boot-local tool of the same shape.

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

const effectExtraToolProviderKind = "boot-effect-extra-tool"

type effectExtraTool struct{ name string }

func (t *effectExtraTool) Name() string        { return t.name }
func (t *effectExtraTool) Description() string { return "host-supplied test tool" }
func (t *effectExtraTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *effectExtraTool) ReadOnly() bool { return true }
func (t *effectExtraTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// effectExtraToolStage writes the fixture and registers the recorder once, so a
// test may build the same kind twice: provider.Register refuses a duplicate.
func effectExtraToolStage(t *testing.T) *effectRecordingProvider {
	t.Helper()
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	rec := &effectRecordingProvider{}
	provider.Register(effectExtraToolProviderKind, func(provider.Config) (provider.Provider, error) {
		return rec, nil
	})
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[[providers]]
name = "test-model"
kind = "`+effectExtraToolProviderKind+`"
model = "x"
`)
	return rec
}

// effectRunWithExtraTools builds the real stack with a host tool list attached
// and returns the requests that one run added.
func effectRunWithExtraTools(t *testing.T, rec *effectRecordingProvider, extra []tool.Tool) []provider.Request {
	t.Helper()
	before := len(rec.requests())
	ctrl, err := Build(context.Background(), Options{Sink: event.Discard, ExtraTools: extra})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "reply ok"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := rec.requests()[before:]
	if len(reqs) == 0 {
		t.Fatal("no request reached the provider boundary")
	}
	return reqs
}

// TestEffectExtraToolsReachProviderAndKeepThePrefixStable pins the final
// boundary of the host-tool channel: the tool reaches the provider request,
// two builds send an identical surface, and the system prefix — the
// cache-stable part — stays byte-identical across them.
func TestEffectExtraToolsReachProviderAndKeepThePrefixStable(t *testing.T) {
	rec := effectExtraToolStage(t)
	extra := []tool.Tool{&effectExtraTool{name: "host_extra_one"}}
	first := effectRunWithExtraTools(t, rec, extra)
	second := effectRunWithExtraTools(t, rec, extra)

	if !toolNames(first[0])["host_extra_one"] {
		t.Fatalf("the host tool is missing from the provider surface: %v", toolSchemaNames(first[0].Tools))
	}
	if got, want := toolSchemaNames(first[0].Tools), toolSchemaNames(second[0].Tools); !reflect.DeepEqual(got, want) {
		t.Fatalf("tool surface is not stable across builds\nfirst=%v\nsecond=%v", got, want)
	}
	if got, want := effectSystemPrompt(t, first[0]), effectSystemPrompt(t, second[0]); got != want {
		t.Fatalf("system prefix differs across builds\nfirst=%q\nsecond=%q", got, want)
	}
}
