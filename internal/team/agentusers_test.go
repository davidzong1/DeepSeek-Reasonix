package team

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newAgentUsersStore returns a pool store over a fresh temp project root.
func newAgentUsersStore(t *testing.T) (*AgentUsersStore, string) {
	t.Helper()
	root := t.TempDir()
	au, err := NewAgentUsersStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return au, root
}

func agentUsersFile(t *testing.T, root string) string {
	t.Helper()
	return filepath.Join(root, ".reasonix", "team", AgentUsersFile)
}

func TestAgentUsersDocCanonicalize(t *testing.T) {
	doc := AgentUsersDoc{Document: Document{SchemaVersion: SchemaVersion}}
	doc.canonicalize()
	if doc.AgentUsers == nil {
		t.Fatal("canonicalize must normalize a nil pool to []")
	}
	if len(doc.AgentUsers) != 0 {
		t.Fatalf("canonicalize grew the pool: %+v", doc.AgentUsers)
	}
}

func TestAgentUsersStoreRoundTrip(t *testing.T) {
	au, root := newAgentUsersStore(t)
	doc := AgentUsersDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{
			UserID:       "au-1",
			Provider:     "anthropic",
			Model:        "claude-opus-5",
			APIKey:       "sk-ant-plaintext-key",
			RBACBindings: []RoleID{RoleCoder},
		}},
	}
	if err := au.Save(doc); err != nil {
		t.Fatal(err)
	}
	got, err := au.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, doc)
	}
	// The credential lands at 0600 like every team write (§3.4).
	info, err := os.Stat(agentUsersFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("agent_users.json perm = %o, want 600", perm)
	}
}

func TestAgentUsersStoreLoadMissingIsEmptyPool(t *testing.T) {
	au, root := newAgentUsersStore(t)
	doc, err := au.Load()
	if err != nil {
		t.Fatalf("missing pool must read as empty, got %v", err)
	}
	if len(doc.AgentUsers) != 0 {
		t.Fatalf("missing pool must be empty, got %+v", doc.AgentUsers)
	}
	if _, err := os.Stat(agentUsersFile(t, root)); !os.IsNotExist(err) {
		t.Fatal("a read must not create agent_users.json")
	}
	if u, ok, err := au.GetAgentUser("ghost"); err != nil || ok {
		t.Fatalf("GetAgentUser on empty pool: u=%+v ok=%v err=%v", u, ok, err)
	}
}

func TestAgentUsersStoreAddAgentUser(t *testing.T) {
	au, root := newAgentUsersStore(t)
	if err := au.AddAgentUser(AgentUser{UserID: "au-1", Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatalf("AddAgentUser on a fresh project: %v", err)
	}
	if err := au.AddAgentUser(AgentUser{UserID: "au-1", Provider: "openai"}); !errors.Is(err, ErrAgentUserExists) {
		t.Fatalf("duplicate id: err = %v, want ErrAgentUserExists", err)
	}
	if err := au.AddAgentUser(AgentUser{UserID: "  "}); !errors.Is(err, ErrInvalidAgentUser) {
		t.Fatalf("blank id: err = %v, want ErrInvalidAgentUser", err)
	}
	pool, err := au.ListAgentUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 1 || pool[0].UserID != "au-1" {
		t.Fatalf("pool after AddAgentUser: %+v", pool)
	}
	info, err := os.Stat(agentUsersFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("bootstrapped agent_users.json perm = %o, want 600", perm)
	}
}

func TestAgentUsersStoreDeleteAgentUser(t *testing.T) {
	au, _ := newAgentUsersStore(t)
	for _, id := range []string{"au-1", "au-2"} {
		if err := au.AddAgentUser(AgentUser{UserID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := au.DeleteAgentUser("au-2"); err != nil {
		t.Fatal(err)
	}
	if err := au.DeleteAgentUser("ghost"); !errors.Is(err, ErrAgentUserNotFound) {
		t.Fatalf("missing entry: err = %v, want ErrAgentUserNotFound", err)
	}
	if err := au.DeleteAgentUser("au-1"); !errors.Is(err, ErrLastAgentUser) {
		t.Fatalf("last entry: err = %v, want ErrLastAgentUser", err)
	}
	pool, err := au.ListAgentUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 1 || pool[0].UserID != "au-1" {
		t.Fatalf("pool changed after refused delete: %+v", pool)
	}
}

func TestAgentUsersStoreGetAgentUser(t *testing.T) {
	au, _ := newAgentUsersStore(t)
	if err := au.AddAgentUser(AgentUser{UserID: "au-1", Provider: "anthropic", Model: "claude-opus-5"}); err != nil {
		t.Fatal(err)
	}
	u, ok, err := au.GetAgentUser("au-1")
	if err != nil || !ok {
		t.Fatalf("GetAgentUser existing: ok=%v err=%v", ok, err)
	}
	if u.Provider != "anthropic" || u.Model != "claude-opus-5" {
		t.Fatalf("entry mismatch: %+v", u)
	}
}

func TestAgentUsersStoreCompareAndSwapConflict(t *testing.T) {
	au, _ := newAgentUsersStore(t)
	doc := AgentUsersDoc{
		Document:   Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{UserID: "au-1"}},
	}
	if err := au.Save(doc); err != nil {
		t.Fatal(err)
	}
	stale := AgentUsersDoc{
		Document:   Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{UserID: "stale"}},
	}
	if err := au.CompareAndSwap(stale, doc); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("err = %v, want ErrCASConflict", err)
	}
}

func TestAgentUsersStoreUpdateAgentUserField(t *testing.T) {
	au, root := newAgentUsersStore(t)
	doc := AgentUsersDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{
			UserID: "au-1", Provider: "anthropic", BaseURL: "https://api.anthropic.com",
		}},
	}
	if err := au.Save(doc); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(agentUsersFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	// Refusals first, so the unchanged-file assertion covers exactly them.
	if err := au.UpdateAgentUserField("nope", AgentUserFieldModel, "x"); !errors.Is(err, ErrAgentUserNotFound) {
		t.Fatalf("unknown id err = %v, want ErrAgentUserNotFound", err)
	}
	if err := au.UpdateAgentUserField("au-1", AgentUserFieldID, "renamed"); err == nil {
		t.Fatal("the id must not be editable through the field seam")
	}
	if err := au.UpdateAgentUserField("au-1", AgentUserFieldBaseURL, "not a url"); err == nil {
		t.Fatal("an invalid field value must be refused")
	}
	after, err := os.ReadFile(agentUsersFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("refused updates must not touch agent_users.json")
	}
	if err := au.UpdateAgentUserField("au-1", AgentUserFieldModel, "claude-opus-5"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := au.GetAgentUser("au-1")
	if err != nil || !ok {
		t.Fatalf("get au-1 = %+v, %v", got, err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-5" {
		t.Fatalf("field update must touch only the field, got %+v", got)
	}
	if err := au.UpdateAgentUserField("au-1", AgentUserFieldAPIKey, "sk-rotated"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := au.GetAgentUser("au-1"); got.APIKey != "sk-rotated" {
		t.Fatalf("api key rotation must persist, got %q", got.APIKey)
	}
	if err := au.UpdateAgentUserField("au-1", AgentUserFieldModel, ""); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := au.GetAgentUser("au-1"); got.Model != "" {
		t.Fatalf("empty value must clear the field, got %+v", got)
	}
}

// TestAgentUsersAddRefusesUnknownProvider pins the write seam for new
// entries: a non-empty provider outside the canonical set never reaches disk.
func TestAgentUsersAddRefusesUnknownProvider(t *testing.T) {
	au, root := newAgentUsersStore(t)
	if err := au.AddAgentUser(AgentUser{UserID: "au-1", Provider: "mystery"}); err == nil {
		t.Fatal("add with an unknown provider must be refused")
	}
	if err := au.AddAgentUser(AgentUser{UserID: "au-1", Provider: ProviderOpenAI}); err != nil {
		t.Fatalf("add with a canonical provider should pass: %v", err)
	}
	if _, err := os.ReadFile(agentUsersFile(t, root)); err != nil {
		t.Fatalf("refused add must not create the document: %v", err)
	}
}

// TestAgentUsersUpdateProviderChangeRefused pins the change seam on the
// whole-entry update: an unknown provider written into an existing entry is
// refused, while the same update with a canonical value passes.
func TestAgentUsersUpdateProviderChangeRefused(t *testing.T) {
	au, root := newAgentUsersStore(t)
	doc := AgentUsersDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{
			UserID: "au-1", Provider: ProviderAnthropic,
		}},
	}
	if err := au.Save(doc); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(agentUsersFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	bad := doc.AgentUsers[0]
	bad.Provider = "mystery"
	if err := au.UpdateAgentUser(bad); err == nil {
		t.Fatal("a provider change onto an unknown value must be refused")
	}
	after, err := os.ReadFile(agentUsersFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("refused update must not touch agent_users.json")
	}
	good := doc.AgentUsers[0]
	good.Provider = ProviderDeepSeek
	if err := au.UpdateAgentUser(good); err != nil {
		t.Fatalf("a provider change onto a canonical value should pass: %v", err)
	}
}

// TestAgentUsersUpdateLegacyProviderPreserved pins the legacy-preserve
// exemption on the whole-entry update: an entry imported with a pre-canonical
// provider keeps it while its other fields edit, and is never rewritten.
func TestAgentUsersUpdateLegacyProviderPreserved(t *testing.T) {
	au, _ := newAgentUsersStore(t)
	doc := AgentUsersDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{
			UserID: "au-1", Provider: "dsh", Model: "deepseek-v4-flash",
		}},
	}
	if err := au.Save(doc); err != nil {
		t.Fatal(err)
	}
	legacy, ok, err := au.GetAgentUser("au-1")
	if err != nil || !ok {
		t.Fatalf("legacy entry should load through the store: %+v, %v", legacy, err)
	}
	edited := legacy
	edited.Model = "deepseek-v4-pro"
	if err := au.UpdateAgentUser(edited); err != nil {
		t.Fatalf("editing other fields of a legacy entry must not be refused: %v", err)
	}
	got, _, err := au.GetAgentUser("au-1")
	if err != nil {
		t.Fatalf("get au-1: %v", err)
	}
	if got.Provider != "dsh" || got.Model != "deepseek-v4-pro" {
		t.Fatalf("legacy provider must survive untouched, got %+v", got)
	}
	changed := got
	changed.Provider = ProviderDeepSeek
	if err := au.UpdateAgentUser(changed); err != nil {
		t.Fatalf("moving a legacy entry onto the canonical value should pass: %v", err)
	}
}

// TestAgentUsersUpdateFieldProviderStrict pins the field seam on the
// provider: an unknown value is refused, a canonical one lands.
func TestAgentUsersUpdateFieldProviderStrict(t *testing.T) {
	au, _ := newAgentUsersStore(t)
	doc := AgentUsersDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{
			UserID: "au-1", Provider: ProviderAnthropic,
		}},
	}
	if err := au.Save(doc); err != nil {
		t.Fatal(err)
	}
	if err := au.UpdateAgentUserField("au-1", AgentUserFieldProvider, "mystery"); err == nil {
		t.Fatal("an unknown provider must be refused through the field seam")
	}
	if err := au.UpdateAgentUserField("au-1", AgentUserFieldProvider, ProviderOpenAI); err != nil {
		t.Fatalf("a canonical provider should pass the field seam: %v", err)
	}
	if got, _, _ := au.GetAgentUser("au-1"); got.Provider != ProviderOpenAI {
		t.Fatalf("provider field update must persist, got %+v", got)
	}
}

// TestAgentUsersUpdateFieldLegacyPreserved pins the legacy exemption on the
// field seam: editing a non-provider field of a legacy entry succeeds and
// leaves the legacy provider alone.
func TestAgentUsersUpdateFieldLegacyPreserved(t *testing.T) {
	au, _ := newAgentUsersStore(t)
	doc := AgentUsersDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		AgentUsers: []AgentUser{{
			UserID: "au-1", Provider: "codex", Model: "gpt-5-codex",
		}},
	}
	if err := au.Save(doc); err != nil {
		t.Fatal(err)
	}
	if err := au.UpdateAgentUserField("au-1", AgentUserFieldModel, "gpt-5.1-codex"); err != nil {
		t.Fatalf("editing a non-provider field of a legacy entry must pass: %v", err)
	}
	if got, _, _ := au.GetAgentUser("au-1"); got.Provider != "codex" || got.Model != "gpt-5.1-codex" {
		t.Fatalf("legacy provider must survive the field edit, got %+v", got)
	}
}
