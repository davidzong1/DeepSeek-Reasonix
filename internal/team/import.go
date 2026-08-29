package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// ImportOptions controls the one-way mult_agent_mcp import (§8.3). The zero
// value is the K1 default: *_api_key fields are dropped, never copied into
// .reasonix. ImportCredentials carries them into agent_users.json, where the
// 0600 write chokepoint (§3.4) is the only thing that makes them safe; values
// still never render in the report or any output.
type ImportOptions struct {
	ImportCredentials bool
}

// ImportReport summarizes one ImportFromMCP run (§8.3): counts and backup
// paths only. It never carries key material, not even by reference.
type ImportReport struct {
	Source                  string
	TeamsCreated            int
	TeamsUpdated            int
	MembersImported         int
	AgentUsersCreated       int
	CredentialFields        int // key fields carried into agent_users.json (ImportCredentials)
	CredentialFieldsSkipped int // key fields dropped (default, K1)
	UnresolvedRefs          int // model/default_agent_user refs with no pool match
	Backups                 []string
}

// ImportFromMCP performs the one-way, read-only import of a mult_agent_mcp
// teams_data.json (§8.3): teams and their members merge into team.json by
// name/member id (idempotent, never clobbering local edits), and the
// agent_users pool merges by user id. Runtime-state fields (tmux_*, last_*,
// leader_*, checkpoint, outbox, ...) are simply not read. Both existing
// documents are backed up at 0600 before the first write; a failed publish
// leaves the backup paths in the report for manual rollback.
func (s *TeamStore) ImportFromMCP(path string, opts ImportOptions) (*ImportReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("team: import source %s: %w", path, err)
	}
	var src mcpDataFile
	if err := json.Unmarshal(data, &src); err != nil {
		return nil, fmt.Errorf("team: import source %s: %w", path, err)
	}
	if len(src.Teams) == 0 && len(src.AgentUsers) == 0 {
		return nil, fmt.Errorf("team: import source %s: no teams or agent_users", path)
	}
	report := &ImportReport{Source: path}
	backups, err := s.backupForImport()
	if err != nil {
		return nil, err
	}
	report.Backups = backups
	imp := &importer{ts: s, src: src, opts: opts, report: report}

	// Pool first: team refs resolve against the merged pool.
	if err := s.agentUsers.update(func(doc *AgentUsersDoc) error {
		imp.mergePool(doc)
		return nil
	}); err != nil {
		return report, err
	}
	pool, err := s.agentUsers.Load()
	if err != nil {
		return report, err
	}
	imp.pool = pool.AgentUsers

	if err := s.update(func(doc *TeamDoc) error {
		imp.mergeTeams(doc)
		return nil
	}); err != nil {
		return report, err
	}
	return report, nil
}

// mcpDataFile is the mult_agent_mcp teams_data.json shape (§2.1): dict-keyed
// teams and agent_users. Fields the port does not migrate (runtime state) are
// absent from these structs and therefore dropped by the decoder.
type mcpDataFile struct {
	Teams      map[string]mcpTeam      `json:"teams"`
	AgentUsers map[string]mcpAgentUser `json:"agent_users"`
}

type mcpTeam struct {
	DefaultAgent     string               `json:"default_agent"`
	DefaultAgentUser string               `json:"default_agent_user"`
	Proxy            *mcpProxy            `json:"proxy"`
	Members          map[string]mcpMember `json:"members"`
}

type mcpProxy struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

// mcpProxyAddress assembles the source proxy's host/port split into the
// address form the registry stores.
func mcpProxyAddress(p *mcpProxy) string {
	if p == nil {
		return ""
	}
	return net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}

type mcpMember struct {
	Role  string `json:"role"`
	Agent string `json:"agent"`
	Model string `json:"model"`
}

// mcpAgentUser carries one agent_user pool entry: a declared provider/model
// quadruple plus per-provider key and model groups (§2.3). Each non-empty
// group becomes one AgentUser record — multi-provider stays multi-record.
type mcpAgentUser struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
	Effort   string `json:"effort"`

	AnthropicAPIKey  string `json:"anthropic_api_key"`
	AnthropicBaseURL string `json:"anthropic_base_url"`
	AnthropicModel   string `json:"anthropic_model"`
	OpenAIAPIKey     string `json:"openai_api_key"`
	OpenAIBaseURL    string `json:"openai_base_url"`
	OpenAIModel      string `json:"openai_model"`
	CodexModel       string `json:"codex_model"`
	DshAPIKey        string `json:"dsh_api_key"`
	DshBaseURL       string `json:"dsh_base_url"`
	DshModel         string `json:"dsh_model"`
}

// importer carries the parse and merge state for one ImportFromMCP run.
// Merge functions run inside the CAS loop, so they must be pure against the
// working document; counts accumulate into the report the last attempt wins.
type importer struct {
	ts     *TeamStore
	src    mcpDataFile
	opts   ImportOptions
	report *ImportReport
	pool   []AgentUser // merged pool, read back after the pool publish
}

// mergePool appends source agent_user groups to the pool by user id,
// skipping entries that already exist (idempotent, first wins).
func (imp *importer) mergePool(doc *AgentUsersDoc) {
	for _, key := range sortedKeys(imp.src.AgentUsers) {
		u := imp.src.AgentUsers[key]
		for _, g := range providerGroups(u) {
			id := g.id(key)
			if agentUserIndex(doc, id) >= 0 {
				continue
			}
			baseURL := g.baseURL
			if baseURL == "" {
				baseURL = u.BaseURL
			}
			record := AgentUser{
				UserID:   id,
				Provider: g.provider,
				BaseURL:  baseURL,
				Model:    g.model,
				Effort:   u.Effort,
			}
			if g.model == "" {
				record.Model = u.Model
			}
			if g.apiKey != "" {
				if imp.opts.ImportCredentials {
					record.APIKey = g.apiKey
					imp.report.CredentialFields++
				} else {
					imp.report.CredentialFieldsSkipped++
				}
			}
			doc.AgentUsers = append(doc.AgentUsers, record)
			imp.report.AgentUsersCreated++
		}
	}
}

// mergeTeams merges source teams by name: new teams are appended whole;
// existing teams keep local edits and only fill empty team-level fields and
// missing members (§8.3).
func (imp *importer) mergeTeams(doc *TeamDoc) {
	for _, name := range sortedKeys(imp.src.Teams) {
		src := imp.src.Teams[name]
		i := teamIndex(doc, name)
		if i < 0 {
			doc.Teams = append(doc.Teams, imp.buildTeam(name, src))
			imp.report.TeamsCreated++
			continue
		}
		imp.report.TeamsUpdated++
		t := &doc.Teams[i]
		if t.AgentType == "" {
			t.AgentType = src.DefaultAgent
		}
		if t.Proxy == nil && src.Proxy != nil && src.Proxy.Enabled {
			t.Proxy = &ProxyConfig{Enabled: true, Address: mcpProxyAddress(src.Proxy)}
		}
		if t.DefaultAgentUserRef == "" {
			if ref, ok := imp.resolveDefaultAgentUser(src.DefaultAgentUser); ok {
				t.DefaultAgentUserRef = ref
			} else if src.DefaultAgentUser != "" {
				imp.report.UnresolvedRefs++
			}
		}
		for _, mid := range sortedKeys(src.Members) {
			if memberIndex(t, mid) >= 0 {
				continue
			}
			t.Template = append(t.Template, imp.buildMember(mid, src.Members[mid]))
			imp.report.MembersImported++
		}
	}
}

// buildTeam maps a source team onto a new Team, counting members as imported.
func (imp *importer) buildTeam(name string, src mcpTeam) Team {
	t := Team{Name: name, AgentType: src.DefaultAgent}
	if src.Proxy != nil && src.Proxy.Enabled {
		t.Proxy = &ProxyConfig{Enabled: true, Address: mcpProxyAddress(src.Proxy)}
	}
	if ref, ok := imp.resolveDefaultAgentUser(src.DefaultAgentUser); ok {
		t.DefaultAgentUserRef = ref
	} else if src.DefaultAgentUser != "" {
		imp.report.UnresolvedRefs++
	}
	for _, mid := range sortedKeys(src.Members) {
		t.Template = append(t.Template, imp.buildMember(mid, src.Members[mid]))
		imp.report.MembersImported++
	}
	return t
}

// buildMember maps a source member onto a MemberSlot. The source encodes the
// leader as the role "leader"; the import separates the two, carrying it onto
// the standalone Leader property so the member's role stays a business role.
// The member's model binds an AgentUserRef when it resolves to exactly one
// pool record; ambiguous or missing matches leave the ref empty and count as
// unresolved.
func (imp *importer) buildMember(id string, m mcpMember) MemberSlot {
	slot := MemberSlot{MemberID: id, Status: MemberStatusActive, AgentType: m.Agent}
	if m.Role == string(RoleLeader) {
		slot.Leader = true
	} else {
		slot.Role = RoleID(m.Role)
	}
	if m.Model == "" {
		return slot
	}
	matches := 0
	for _, u := range imp.pool {
		if u.Model == m.Model {
			matches++
			slot.AgentUserRef = u.UserID
		}
	}
	if matches != 1 {
		slot.AgentUserRef = ""
		imp.report.UnresolvedRefs++
	}
	return slot
}

// resolveDefaultAgentUser reports whether the source default_agent_user
// reference exists in the merged pool; only then is it carried over.
func (imp *importer) resolveDefaultAgentUser(ref string) (string, bool) {
	if ref == "" {
		return "", false
	}
	for _, u := range imp.pool {
		if u.UserID == ref {
			return ref, true
		}
	}
	return "", false
}

// providerGroup is one non-empty provider quadruple inside a source
// agent_user entry (§2.3); each becomes its own AgentUser record.
type providerGroup struct {
	provider string
	baseURL  string
	model    string
	apiKey   string
	index    int
}

// id returns the record id: the source key for the first group, and
// key@provider for later groups so multi-provider entries stay addressable.
func (g providerGroup) id(key string) string {
	if g.index == 0 {
		return key
	}
	return key + "@" + g.provider
}

// providerGroups splits a source entry into its non-empty provider groups in
// a fixed order, so multi-provider splits are deterministic.
func providerGroups(u mcpAgentUser) []providerGroup {
	var groups []providerGroup
	appendGroup := func(provider, baseURL, model, apiKey string) {
		provider = NormalizeProvider(provider)
		if model != "" || apiKey != "" {
			groups = append(groups, providerGroup{provider: provider, baseURL: baseURL, model: model, apiKey: apiKey, index: len(groups)})
		}
	}
	appendGroup("anthropic", u.AnthropicBaseURL, u.AnthropicModel, u.AnthropicAPIKey)
	appendGroup("openai", u.OpenAIBaseURL, u.OpenAIModel, u.OpenAIAPIKey)
	appendGroup("codex", u.OpenAIBaseURL, u.CodexModel, "")
	appendGroup("dsh", u.DshBaseURL, u.DshModel, u.DshAPIKey)
	if len(groups) == 0 && u.Provider != "" {
		groups = append(groups, providerGroup{provider: NormalizeProvider(u.Provider), model: u.Model, index: 0})
	}
	return groups
}

// backupForImport copies the team and pool documents (when present) to
// timestamped .pre-import.bak paths at 0600, returning the absolute paths.
func (s *TeamStore) backupForImport() ([]string, error) {
	ts := time.Now().Format("20060102-150405")
	var backups []string
	for _, file := range []string{TeamFile, AgentUsersFile} {
		rel := filepath.Join(".reasonix", "team", file)
		full, err := safePath(s.store.root, rel)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		bak := filepath.Join(".reasonix", "team", file+"."+ts+".pre-import.bak")
		if err := AtomicWrite(s.store.root, bak, data); err != nil {
			return nil, err
		}
		bakFull, err := safePath(s.store.root, bak)
		if err != nil {
			return nil, err
		}
		backups = append(backups, bakFull)
	}
	return backups, nil
}

// sortedKeys returns a map's keys in deterministic order, so re-imports are
// byte-stable and reports are reproducible.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// memberIndex returns the member slot index in the team, or -1.
func memberIndex(t *Team, id string) int {
	for i := range t.Template {
		if t.Template[i].MemberID == id {
			return i
		}
	}
	return -1
}
