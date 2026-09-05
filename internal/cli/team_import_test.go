package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// mcpFixture writes a mult_agent_mcp teams_data.json fixture into dir and
// returns its path. It carries two teams (one with a proxy), a multi-provider
// agent_users pool with plaintext keys, and top-level runtime-state fields
// that the import must never read (tmux_*, last_*, leader_*, checkpoint,
// outbox).
func mcpFixture(t *testing.T, dir string) string {
	t.Helper()
	src := map[string]any{
		"teams": map[string]any{
			"alpha": map[string]any{
				"default_agent":      "codex",
				"default_agent_user": "au-1",
				"proxy":              map[string]any{"enabled": true, "host": "127.0.0.1", "port": 7890},
				"members": map[string]any{
					"coder-1":  map[string]any{"role": "coder", "agent": "codex", "model": "claude-opus-4"},
					"tester-1": map[string]any{"role": "tester", "agent": "claude", "model": "gpt-5"},
					"extra-1":  map[string]any{"role": "reviewer", "agent": "claude", "model": "no-such-model"},
				},
			},
			"beta": map[string]any{
				"default_agent": "claude",
				"members":       map[string]any{},
			},
		},
		"agent_users": map[string]any{
			"au-1": map[string]any{
				"provider":          "anthropic",
				"model":             "claude-opus-4",
				"anthropic_api_key": "sk-ant-secret-1",
				"anthropic_model":   "claude-opus-4",
			},
			"au-2": map[string]any{
				"provider":       "openai",
				"model":          "gpt-5",
				"openai_api_key": "sk-openai-secret-2",
				"openai_model":   "gpt-5",
				"dsh_api_key":    "dsh-secret-3",
				"dsh_model":      "deepseek-v4",
			},
		},
		"tmux_session":        "dead",
		"last_observed_state": "working",
		"leader_last_task":    "stale",
		"checkpoint":          map[string]any{"goal": "stale"},
		"outbox":              map[string]any{"queued": 1},
	}
	path := filepath.Join(dir, "teams_data.json")
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// inDir chdirs into dir for the test lifetime, since teamImportCommand
// resolves .reasonix against the working directory. The user-global default
// team root is pinned to dir/.reasonix, so the import's data dir is the same
// <dir>/.reasonix/team the read-back assertions target — each test stays
// isolated from the shared test home.
func inDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("REASONIX_STATE_HOME", filepath.Join(dir, ".reasonix"))
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// captureOutput runs fn with stdout and stderr redirected to pipes, restoring
// them before draining so output larger than the pipe buffer cannot deadlock.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	fn()
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out, _ := io.ReadAll(rOut)
	errb, _ := io.ReadAll(rErr)
	return string(out), string(errb)
}

// readAgentUsers decodes the merged agent_users.json, dropping api_key fields.
// A refused import never publishes the pool, so an absent file reads as empty.
func readAgentUsers(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".reasonix", "team", teamAgentUsersFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var doc struct {
		AgentUsers []map[string]any `json:"agent_users"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.AgentUsers
}

func TestTeamImportDefaultSkipsKeys(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	src := mcpFixture(t, dir)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	if code := teamImportCommand([]string{"--from", src}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	// One-way: the source file is untouched.
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("source teams_data.json was modified; import must be read-only")
	}

	// Plaintext keys were skipped (K1 default): no api_key field anywhere.
	for _, u := range readAgentUsers(t, dir) {
		if _, ok := u["api_key"]; ok {
			t.Fatalf("agent_users %v carries api_key without opt-in", u)
		}
	}
}

func TestTeamImportCredentialsOptIn(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	src := mcpFixture(t, dir)

	stdout, stderr := captureOutput(t, func() {
		if code := teamImportCommand([]string{"--from", src, "--credentials", "--yes"}); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	out := stdout + stderr
	for _, secret := range []string{"sk-ant-secret-1", "sk-openai-secret-2", "dsh-secret-3"} {
		if strings.Contains(out, secret) {
			t.Fatalf("output leaked key material %q", secret)
		}
	}
	if !strings.Contains(stdout, "credential fields imported: 3") {
		t.Errorf("stdout missing credential count: %s", stdout)
	}

	keys := map[string]bool{}
	for _, u := range readAgentUsers(t, dir) {
		if k, ok := u["api_key"].(string); ok && k != "" {
			keys[u["UserID"].(string)] = true
		}
	}
	if len(keys) != 3 {
		t.Fatalf("api_key present on %v, want 3 records (au-1, au-2, au-2@deepseek)", keys)
	}
}

func TestTeamImportCredentialsRefused(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	src := mcpFixture(t, dir)

	teamConfirmCredentialImport = func(string) bool { return false }
	defer func() { teamConfirmCredentialImport = nil }()

	code := teamImportCommand([]string{"--from", src, "--credentials"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 when confirmation is refused", code)
	}
	for _, u := range readAgentUsers(t, dir) {
		if _, ok := u["api_key"]; ok {
			t.Fatalf("agent_users %v carries api_key after refused confirmation", u)
		}
	}
}

func TestTeamImportConfirmationApplies(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	src := mcpFixture(t, dir)

	teamConfirmCredentialImport = func(string) bool { return true }
	defer func() { teamConfirmCredentialImport = nil }()

	if code := teamImportCommand([]string{"--from", src, "--credentials"}); code != 0 {
		t.Fatalf("exit code = %d, want 0 when confirmation is granted", code)
	}
	imported := 0
	for _, u := range readAgentUsers(t, dir) {
		if k, ok := u["api_key"].(string); ok && k != "" {
			imported++
		}
	}
	if imported != 3 {
		t.Fatalf("api_key imported on %d records, want 3", imported)
	}
}

func TestTeamImportErrorPrompts(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)

	// Missing source file.
	_, stderr := captureOutput(t, func() {
		if code := teamImportCommand([]string{"--from", filepath.Join(dir, "nope.json")}); code != 1 {
			t.Fatalf("missing file: exit code = %d, want 1", code)
		}
	})
	if !strings.Contains(stderr, "import source") {
		t.Errorf("missing file: stderr = %q, want 'import source'", stderr)
	}

	// Corrupt JSON.
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr = captureOutput(t, func() {
		if code := teamImportCommand([]string{"--from", bad}); code != 1 {
			t.Fatalf("corrupt json: exit code = %d, want 1", code)
		}
	})
	if !strings.Contains(stderr, "import source") {
		t.Errorf("corrupt json: stderr = %q, want 'import source'", stderr)
	}

	// Empty source.
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr = captureOutput(t, func() {
		if code := teamImportCommand([]string{"--from", empty}); code != 1 {
			t.Fatalf("empty source: exit code = %d, want 1", code)
		}
	})
	if !strings.Contains(stderr, "no teams or agent_users") {
		t.Errorf("empty source: stderr = %q, want 'no teams or agent_users'", stderr)
	}

	// Unknown flag.
	_, stderr = captureOutput(t, func() {
		if code := teamImportCommand([]string{"--bogus"}); code != 2 {
			t.Fatalf("unknown flag: exit code = %d, want 2", code)
		}
	})
	if stderr == "" {
		t.Error("unknown flag: expected flag error on stderr")
	}

	// Unknown subcommand.
	if code := teamCommand([]string{"nonsense"}); code != 2 {
		t.Fatalf("unknown subcommand: exit code = %d, want 2", code)
	}
}

func TestTeamImportDisplay(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	src := mcpFixture(t, dir)

	stdout, _ := captureOutput(t, func() {
		if code := teamImportCommand([]string{"--from", src}); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	for _, want := range []string{
		"teams created: 2",
		"members imported: 3",
		"agent_users created: 3",
		"credential fields skipped: 3",
		"unresolved refs: 1",
		"alpha", "agent=codex", "proxy=127.0.0.1:7890",
		"au-1", "provider=anthropic", "model=claude-opus-4",
		"au-2@deepseek", "provider=deepseek", "model=deepseek-v4",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, secret := range []string{"sk-ant-secret-1", "sk-openai-secret-2", "dsh-secret-3"} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("display leaked key material %q", secret)
		}
	}
}

func TestTeamImportIdempotent(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	src := mcpFixture(t, dir)

	first, _ := captureOutput(t, func() {
		if code := teamImportCommand([]string{"--from", src}); code != 0 {
			t.Fatalf("first run exit code = %d, want 0", code)
		}
	})
	if !strings.Contains(first, "teams created: 2") {
		t.Fatalf("first run: %s", first)
	}

	second, _ := captureOutput(t, func() {
		if code := teamImportCommand([]string{"--from", src}); code != 0 {
			t.Fatalf("second run exit code = %d, want 0", code)
		}
	})
	for _, want := range []string{"teams created: 0", "members imported: 0", "agent_users created: 0"} {
		if !strings.Contains(second, want) {
			t.Errorf("second run not idempotent, missing %q:\n%s", want, second)
		}
	}
}

// teamAgentUsersFile keeps the test independent of the team package's constant
// export shape; it must match AgentUsersFile.
const teamAgentUsersFile = "agent_users.json"

// TestTeamImportLegacyAgentFieldsStayHidden pins the rendering contract for
// imported data: the v1-era "agent" member field lands in the backend
// AgentType, but neither the compact roster nor the detail view shows it —
// the launch type is backend-only, never a list or detail row.
func TestTeamImportLegacyAgentFieldsStayHidden(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	if code := teamImportCommand([]string{"--from", mcpFixture(t, dir)}); code != 0 {
		t.Fatalf("import exit code = %d, want 0", code)
	}
	idx := -1
	for i, tm := range readStoredTeamDoc(t).Teams {
		if tm.Name == "alpha" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("alpha team missing after import")
	}
	m := openTeamOverlay(t)
	for range idx {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	list := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"coder-1", "tester-1", "(coder · active)", "(tester · active)"} {
		if !strings.Contains(list, want) {
			t.Fatalf("imported roster should show %q, got:\n%s", want, list)
		}
	}
	for _, gone := range []string{"Agent", "codex", "claude"} {
		if strings.Contains(list, gone) {
			t.Fatalf("compact roster must not show imported agent %q, got:\n%s", gone, list)
		}
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	ctx := ansi.Strip(m.renderTeamPicker())
	// The imported launch type stays backend-only: the editor shows the agent
	// binding as a property, never the launch type or its value.
	for _, gone := range []string{"AgentType", "codex", "claude"} {
		if strings.Contains(ctx, gone) {
			t.Fatalf("detail must not show imported agent %q, got:\n%s", gone, ctx)
		}
	}
}
