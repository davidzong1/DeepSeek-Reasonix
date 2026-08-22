package team

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateAgentUserFieldAcceptsBlanks pins the form contract: every field
// is optional, so the minimal a-create path (id only) stays legal and the
// pool renders "unconfigured" until fields are filled.
func TestValidateAgentUserFieldAcceptsBlanks(t *testing.T) {
	for _, name := range []string{
		AgentUserFieldID, AgentUserFieldIdentity, AgentUserFieldProvider,
		AgentUserFieldBaseURL, AgentUserFieldModel, AgentUserFieldEffort,
		AgentUserFieldAPIKey,
	} {
		if err := ValidateAgentUserField(name, ""); err != nil {
			t.Errorf("field %q blank: %v", name, err)
		}
	}
}

// TestValidateAgentUserFieldUnknownRefuses pins that a form typo surfaces
// instead of silently passing a field nobody validates.
func TestValidateAgentUserFieldUnknownRefuses(t *testing.T) {
	err := ValidateAgentUserField("provdier", "anthropic")
	var fe *AgentUserFieldError
	if !errors.As(err, &fe) || fe.Field != "provdier" {
		t.Fatalf("unknown field: err = %v, want AgentUserFieldError naming the field", err)
	}
}

// TestValidateBaseURL pins the only format rule: a non-empty base_url must be
// an http(s) URL with a host. Everything else the provider layer could never
// dial is a typo, not a config.
func TestValidateBaseURL(t *testing.T) {
	for _, ok := range []string{
		"https://api.anthropic.com",
		"http://127.0.0.1:8080/v1",
	} {
		if err := ValidateAgentUserField(AgentUserFieldBaseURL, ok); err != nil {
			t.Errorf("base_url %q should pass: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"ftp://example.com",
		"anthropic.com",
		"//no-scheme",
		"https://",
		"not a url at all",
	} {
		if err := ValidateAgentUserField(AgentUserFieldBaseURL, bad); err == nil {
			t.Errorf("base_url %q should be refused", bad)
		}
	}
}

// TestValidateAgentUserFieldLengths pins the length ceiling on every field.
func TestValidateAgentUserFieldLengths(t *testing.T) {
	cases := []struct {
		name, value string
	}{
		{AgentUserFieldID, strings.Repeat("x", agentUserIDLimit+1)},
		{AgentUserFieldProvider, strings.Repeat("x", agentUserProviderLimit+1)},
		{AgentUserFieldBaseURL, "https://" + strings.Repeat("x", agentUserBaseURLLimit)},
		{AgentUserFieldModel, strings.Repeat("x", agentUserModelLimit+1)},
		{AgentUserFieldEffort, strings.Repeat("x", agentUserEffortLimit+1)},
	}
	for _, c := range cases {
		if err := ValidateAgentUserField(c.name, c.value); err == nil {
			t.Errorf("field %q over limit should be refused", c.name)
		}
	}
}

// TestValidateAgentUserOpenEnded pins that provider, model, and effort take
// any non-blank value: import supports providers the TUI has never heard of,
// and the agent layer treats effort as a free string.
// TestValidateAgentUserProviderWhitelist pins the provider contract: only the
// three canonical values plus blank (unconfigured) pass. Unknown values are
// refused because a provider the runtime cannot resolve would fail member
// launch (provider.New refuses unknown kinds). Model and effort stay
// open-ended.
func TestValidateAgentUserProviderWhitelist(t *testing.T) {
	for _, p := range []string{"", "anthropic", "openai", "deepseek"} {
		u := AgentUser{UserID: "au-1", Provider: p, Model: "model-xyz/2026", Effort: "max"}
		if err := ValidateAgentUser(u); err != nil {
			t.Errorf("provider %q: %v", p, err)
		}
	}
	for _, p := range []string{"some-future-provider", "claude", "gpt-4", "Anthropic", "openai-compatible"} {
		u := AgentUser{UserID: "au-1", Provider: p}
		if err := ValidateAgentUser(u); err == nil {
			t.Errorf("provider %q should be refused", p)
		}
	}
}

// TestValidateAgentUserFieldProviderListsOptions pins the refusal contract: an
// unknown provider is an AgentUserFieldError naming the field, listing every
// legal option, and never echoing the typed value (K2).
func TestValidateAgentUserFieldProviderListsOptions(t *testing.T) {
	err := ValidateAgentUserField(AgentUserFieldProvider, "gpt-4o")
	var fe *AgentUserFieldError
	if !errors.As(err, &fe) || fe.Field != AgentUserFieldProvider {
		t.Fatalf("err = %v, want provider field error", err)
	}
	for _, want := range []string{"anthropic", "openai", "deepseek"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should list %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "gpt-4o") {
		t.Fatalf("error must not echo the typed value: %v", err)
	}
}

// TestAgentUsersStoreRefusesUnknownProvider pins the store write paths: a
// non-canonical provider is refused on add and on update, leaving the last
// good write on disk untouched.
func TestAgentUsersStoreRefusesUnknownProvider(t *testing.T) {
	s, err := NewAgentUsersStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgentUser(AgentUser{UserID: "au-1", Provider: "claude"}); err == nil {
		t.Fatal("AddAgentUser should refuse a non-canonical provider")
	}
	if err := s.AddAgentUser(AgentUser{UserID: "au-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAgentUser(AgentUser{UserID: "au-1", Provider: "gpt-4"}); err == nil {
		t.Fatal("UpdateAgentUser should refuse a non-canonical provider")
	}
	u, ok, err := s.GetAgentUser("au-1")
	if err != nil || !ok {
		t.Fatalf("au-1 should still hold the last good write (ok=%v err=%v)", ok, err)
	}
	if u.Provider != "" {
		t.Fatalf("provider = %q, want the last good write (blank)", u.Provider)
	}
}

// TestAgentUsersStoreLegacyProviderPreserved pins legacy-preserve: a provider
// value written by an older version still loads, editing other fields must not
// break on it, and only an explicit change to a non-canonical value is refused.
func TestAgentUsersStoreLegacyProviderPreserved(t *testing.T) {
	s, err := NewAgentUsersStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Save bypasses validation, simulating an old document on disk.
	legacy := AgentUsersDoc{
		Document:   Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{UserID: "au-1", Provider: "legacy-x", Model: "m1"}},
	}
	if err := s.Save(legacy); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Load()
	if err != nil {
		t.Fatalf("a legacy provider must load: %v", err)
	}
	if doc.AgentUsers[0].Provider != "legacy-x" {
		t.Fatalf("loaded provider = %q, want %q", doc.AgentUsers[0].Provider, "legacy-x")
	}
	// Editing another field keeps the legacy value and must not fail.
	edit := AgentUser{UserID: "au-1", Provider: "legacy-x", Model: "m2"}
	if err := s.UpdateAgentUser(edit); err != nil {
		t.Fatalf("editing other fields must not break on a legacy provider: %v", err)
	}
	u, ok, err := s.GetAgentUser("au-1")
	if err != nil || !ok {
		t.Fatalf("au-1 should exist after the edit (ok=%v err=%v)", ok, err)
	}
	if u.Provider != "legacy-x" || u.Model != "m2" {
		t.Fatalf("after edit = %q/%q, want legacy-x/m2", u.Provider, u.Model)
	}
	// An explicit change to a non-canonical value is refused.
	if err := s.UpdateAgentUser(AgentUser{UserID: "au-1", Provider: "claude"}); err == nil {
		t.Fatal("changing the provider to a non-canonical value should be refused")
	}
}

// TestValidateAgentUserKeepsErrInvalidAgentUser pins the store contract: an
// empty id is still the same sentinel error the pool screen maps.
func TestValidateAgentUserKeepsErrInvalidAgentUser(t *testing.T) {
	if err := ValidateAgentUser(AgentUser{}); !errors.Is(err, ErrInvalidAgentUser) {
		t.Fatalf("empty id: err = %v, want ErrInvalidAgentUser", err)
	}
}

// TestValidateAgentUserErrorHidesKey pins K2 on the error path: a refusal
// caused by api_key content never carries that content.
func TestValidateAgentUserErrorHidesKey(t *testing.T) {
	key := "sk-ant-secret-value"
	u := AgentUser{UserID: "au-1", APIKey: strings.Repeat(key, 256)}
	err := ValidateAgentUser(u)
	if err == nil {
		t.Fatal("oversized api_key should be refused")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("validation error leaked api_key material: %v", err)
	}
}

// TestAgentUsersStoreValidatesOnWrite pins that the store enforces the field
// rules on both write paths, not just the form: an entry that would never
// dial (bad base_url) is refused with the file untouched.
func TestAgentUsersStoreValidatesOnWrite(t *testing.T) {
	s, err := NewAgentUsersStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := AgentUser{UserID: "au-1", BaseURL: "ftp://example.com"}
	if err := s.AddAgentUser(bad); err == nil {
		t.Fatal("AddAgentUser should refuse a bad base_url")
	}
	if err := s.AddAgentUser(AgentUser{UserID: "au-1"}); err != nil {
		t.Fatal(err)
	}
	good := AgentUser{UserID: "au-1", BaseURL: "https://api.example.com"}
	if err := s.UpdateAgentUser(good); err != nil {
		t.Fatalf("UpdateAgentUser should accept a good base_url: %v", err)
	}
	if err := s.UpdateAgentUser(bad); err == nil {
		t.Fatal("UpdateAgentUser should refuse a bad base_url")
	}
	u, ok, err := s.GetAgentUser("au-1")
	if err != nil || !ok {
		t.Fatalf("au-1 should still hold the last good write (ok=%v err=%v)", ok, err)
	}
	if u.BaseURL != good.BaseURL {
		t.Fatalf("base_url = %q, want %q", u.BaseURL, good.BaseURL)
	}
}

// TestValidateAgentUserStableOrder pins deterministic refusal order (id →
// identity → provider → base_url → model → effort → api_key), so the form
// can predict which field a composite entry would fail on.
func TestValidateAgentUserStableOrder(t *testing.T) {
	u := AgentUser{
		UserID:  "au-1",
		BaseURL: "ftp://example.com", // first refusal
		Effort:  strings.Repeat("x", agentUserEffortLimit+1),
	}
	err := ValidateAgentUser(u)
	var fe *AgentUserFieldError
	if !errors.As(err, &fe) || fe.Field != AgentUserFieldBaseURL {
		t.Fatalf("err = %v, want the base_url refusal first", err)
	}
}
