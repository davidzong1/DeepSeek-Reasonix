package cli

// 1M context regression, CLI level: the [1m] alias strips from the wire model
// and advertises a one-million-token window. DeepSeek additionally routes to
// Anthropic with its protocol flags; the wire contract lives in
// chat_tui_team_model_rebind_test.go, while this file pins resolver behavior.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/netclient"
	"reasonix/internal/team"
)

// Test1MResolverRoutesAndStripsAlias pins the resolver half of the [1m]
// contract for a DeepSeek anthropic entry: the payload with the [1m] suffix
// routes to the anthropic kind, keeps the entry's endpoint, strips the alias
// from the wire model, and flags the protocol metadata (deepSeekAnthropic +
// bearer header) that the builder's assembly-time checks read.
func Test1MResolverRoutesAndStripsAlias(t *testing.T) {
	r, err := newMemberProviderResolver(team.AgentUser{
		UserID: "u1", Provider: "deepseek", BaseURL: "https://gateway.example.com/v1",
		Model: "deepseek/deepseek-v4-flash[1m]", APIKey: "sk", Effort: "max",
	}, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if r.kind != "anthropic" {
		t.Fatalf("kind = %q, want anthropic (the [1m] suffix forces the anthropic route)", r.kind)
	}
	if r.endpoint != "https://gateway.example.com/v1" {
		t.Fatalf("endpoint = %q, want the entry's own", r.endpoint)
	}
	if r.model != "deepseek/deepseek-v4-flash" {
		t.Fatalf("wire model = %q, want the [1m] suffix stripped before transport", r.model)
	}
	if !r.deepSeekAnthropic || !r.anthropicBearerHeader {
		t.Fatalf("the 1M DeepSeek route must carry the anthropic flags, got %+v", r)
	}
	if got := r.Ref(); got != "u1/deepseek/deepseek-v4-flash[1m]" {
		t.Fatalf("ref = %q, want the member ref from the entry's full model", got)
	}
	cat := r.Catalog()
	if len(cat) != 1 || cat[0].Model != "deepseek/deepseek-v4-flash" || cat[0].Ref != r.Ref() {
		t.Fatalf("descriptor = %+v, want the stripped model and the member ref", cat)
	}
	if cat[0].ContextWindow != 1_000_000 {
		t.Fatalf("descriptor ContextWindow = %d, want 1_000_000 (the [1m] alias carries the 1M window so the TUI gauge uses the right denominator)", cat[0].ContextWindow)
	}
}

// Test1MResolverOmitsAliasFlagsForPlainDeepSeek pins the negative: a DeepSeek
// model without the [1m] suffix keeps its plain name and never carries the
// anthropic protocol metadata — the alias opt-in is suffix-gated.
func Test1MResolverOmitsAliasFlagsForPlainDeepSeek(t *testing.T) {
	r, err := newMemberProviderResolver(team.AgentUser{
		UserID: "u1", Provider: "deepseek", BaseURL: "https://gateway.example.com/v1",
		Model: "deepseek/deepseek-v4-flash", APIKey: "sk",
	}, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if r.kind != "openai" || r.model != "deepseek/deepseek-v4-flash" {
		t.Fatalf("plain custom DeepSeek route = %q model %q, want openai with the model unchanged", r.kind, r.model)
	}
	if r.deepSeekAnthropic || r.anthropicBearerHeader {
		t.Fatal("a plain DeepSeek model must not carry the [1m] protocol flags")
	}
	cat := r.Catalog()
	if len(cat) != 1 || cat[0].ContextWindow != 0 {
		t.Fatalf("a plain DeepSeek model must not advertise the 1M window, got %+v", cat)
	}
}

// Test1MResolverAdvertisesWindowForOpenAIAlias pins the provider-independent
// part of the alias contract: an OpenAI-compatible member strips [1m] and
// advertises the one-million-token window without inheriting DeepSeek's
// Anthropic protocol flags.
func Test1MResolverAdvertisesWindowForOpenAIAlias(t *testing.T) {
	r, err := newMemberProviderResolver(team.AgentUser{
		UserID: "long-openai", Provider: "openai", BaseURL: "https://gateway.example.com/v1",
		Model: " gpt-5.6-sol[1M] ", APIKey: "sk",
	}, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if r.kind != "openai" || r.model != "gpt-5.6-sol" {
		t.Fatalf("openai 1M route = kind %q model %q, want openai and stripped model", r.kind, r.model)
	}
	if r.deepSeekAnthropic || r.anthropicBearerHeader {
		t.Fatal("an OpenAI 1M alias must not carry DeepSeek Anthropic protocol flags")
	}
	cat := r.Catalog()
	if len(cat) != 1 || cat[0].ContextWindow != 1_000_000 {
		t.Fatalf("openai 1M descriptor = %+v, want ContextWindow 1_000_000", cat)
	}
}

// Test1MResolverCaseInsensitiveAndWhitespace pins the suffix match: the alias
// is trimmed before comparison, and a suffix with mixed case still strips —
// a member pasting DEEPSEEK-V4-FLASH[1M] gets the same wire model as the
// canonical lowercase.
func Test1MResolverCaseInsensitiveAndWhitespace(t *testing.T) {
	r, err := newMemberProviderResolver(team.AgentUser{
		UserID: "u1", Provider: "deepseek", BaseURL: "https://gateway.example.com/v1",
		Model: "  DEEPSEEK/DEEPSEEK-V4-FLASH[1M]  ", APIKey: "sk",
	}, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if !r.deepSeekAnthropic {
		t.Fatal("the [1M] suffix must match case-insensitively")
	}
	if r.model != "DEEPSEEK/DEEPSEEK-V4-FLASH" {
		t.Fatalf("wire model = %q, want the mixed-case alias stripped with surrounding spaces trimmed", r.model)
	}
}

// TestTeamPoolModelContext1MSavesAndReopens drives the real Agent User model
// editor. The UI trims only surrounding whitespace, keeps the user's suffix
// spelling, publishes it, and a fresh pool reads the same opt-in back with an
// explicit context status and a one-million-token runtime descriptor.
func TestTeamPoolModelContext1MSavesAndReopens(t *testing.T) {
	const oldModel = "deepseek/deepseek-v4-flash"
	const savedModel = "deepseek/deepseek-v4-flash[1M]"
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "long-context", Provider: "deepseek", BaseURL: "https://gateway.example.com/v1",
		Model: oldModel, APIKey: "sk-test",
	})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	for poolEditFields[m.teamPick.pool.edit] != team.AgentUserFieldModel {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range []rune(oldModel) {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m = typeTeamName(m, "  "+savedModel+"  ")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Context: 1M") {
		t.Fatalf("confirmed model draft must show the 1M status, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})

	stored := readStoredPool(t)
	if len(stored) != 1 || stored[0].Model != savedModel {
		t.Fatalf("stored model = %+v, want suffix and case preserved as %q", stored, savedModel)
	}
	reopened := openPool(t)
	if got := ansi.Strip(reopened.renderTeamPicker()); !strings.Contains(got, "Context: 1M") {
		t.Fatalf("reopened pool list must show the 1M status, got:\n%s", got)
	}
	reopened = teamKey(reopened, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := ansi.Strip(reopened.renderTeamPicker()); !strings.Contains(got, "Context: 1M") {
		t.Fatalf("reopened pool detail must show the 1M status, got:\n%s", got)
	}

	r, err := newMemberProviderResolver(stored[0], netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	d := r.Catalog()[0]
	if d.Model != "deepseek/deepseek-v4-flash" || d.ContextWindow != 1_000_000 {
		t.Fatalf("reopened descriptor = %+v, want stripped wire model with 1M context", d)
	}
}

func TestTeamPoolPlainModelShowsDefaultContext(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "plain", Provider: "deepseek", BaseURL: "https://gateway.example.com/v1",
		Model: "deepseek-v4-flash", APIKey: "sk-test",
	})
	m := openPool(t)
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "Context: 1M") {
		t.Fatalf("plain model must not be marked as 1M, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Context: default") {
		t.Fatalf("plain model detail must state the default context, got:\n%s", got)
	}
}
