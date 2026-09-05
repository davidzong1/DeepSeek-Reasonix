package cli

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reasonix/internal/team"
)

// teamCommand dispatches the team-facing CLI commands. Only "team import" is
// wired for now; the seam exists so the mult_agent_mcp port has a CLI entry
// point without touching the TUI (chat_tui_team.go).
func teamCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "team: expected a subcommand (import)")
		return 2
	}
	switch args[0] {
	case "import":
		return teamImportCommand(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "team: unknown subcommand %q (import)\n", args[0])
		return 2
	}
}

// teamImportCommand runs the one-way mult_agent_mcp teams_data.json import
// (§8.3): team.TeamStore.ImportFromMCP merges teams and the agent_users pool
// into .reasonix/team, never writing back to the source. Plaintext *_api_key
// fields are skipped by default (K1); --credentials opts in, and even then a
// confirmation is required unless --yes is given, so keys cannot land in
// agent_users.json by accident.
func teamImportCommand(args []string) int {
	fs := flag.NewFlagSet("team import", flag.ContinueOnError)
	from := fs.String("from", defaultMCPTeamsPath(), "path to the mult_agent_mcp teams_data.json")
	withCredentials := fs.Bool("credentials", false, "import plaintext *_api_key fields into agent_users.json")
	yes := fs.Bool("yes", false, "confirm the plaintext-key import without prompting")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "team import: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	confirmed := false
	if *withCredentials {
		confirmed = *yes
	}
	if *withCredentials && !*yes {
		confirmed = teamConfirmCredentialImport(
			"team import: --credentials copies plaintext API keys into .reasonix/team/agent_users.json (0600-protected). Continue? [y/N] ")
		if !confirmed {
			fmt.Fprintln(os.Stderr, "team import: cancelled — plaintext keys not imported (default: keys skipped)")
			return 1
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "team import:", err)
		return 1
	}
	roots, err := openTeamDataRoots(cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team import:", err)
		return 1
	}
	if roots.note != "" {
		fmt.Fprintln(os.Stderr, "team import:", roots.note)
	}
	store := roots.store

	report, err := store.ImportFromMCP(*from, team.ImportOptions{ImportCredentials: confirmed})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	printImportReport(report)
	teamImportSummary(store)
	return 0
}

// defaultMCPTeamsPath is where the mult_agent_mcp server persists its team
// data (§2.1), the natural --from default.
func defaultMCPTeamsPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".mult_agent_mcp", "teams_data.json")
	}
	return filepath.Join(".mult_agent_mcp", "teams_data.json")
}

// teamConfirmCredentialImport asks for the explicit plaintext-key opt-in. A
// var so tests can drive the decision without a terminal; the prompt never
// carries key material (K3).
var teamConfirmCredentialImport = func(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// printImportReport renders the ImportReport counts. The report never carries
// key material by contract, so this output is safe to log (§11-D, K3).
func printImportReport(r *team.ImportReport) {
	fmt.Printf("Imported from %s\n", r.Source)
	fmt.Printf("  teams created: %d\n", r.TeamsCreated)
	fmt.Printf("  teams updated: %d\n", r.TeamsUpdated)
	fmt.Printf("  members imported: %d\n", r.MembersImported)
	fmt.Printf("  agent_users created: %d\n", r.AgentUsersCreated)
	if r.CredentialFields > 0 {
		fmt.Printf("  credential fields imported: %d\n", r.CredentialFields)
	}
	fmt.Printf("  credential fields skipped: %d (plaintext keys; re-run with --credentials to import)\n", r.CredentialFieldsSkipped)
	fmt.Printf("  unresolved refs: %d\n", r.UnresolvedRefs)
	for _, b := range r.Backups {
		fmt.Printf("  backup: %s\n", b)
	}
}

// teamImportSummary reads the merged documents back and prints the
// provider/model/proxy view the user asked for. Best-effort: a read failure
// degrades to a warning line, never a failed import.
func teamImportSummary(store *team.TeamStore) {
	doc, _, err := store.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "team import: teams unreadable:", err)
	} else {
		fmt.Println("teams:")
		for _, t := range doc.Teams {
			line := fmt.Sprintf("  %s  agent=%s  members=%d", t.Name, orDefault(t.AgentType, "inherit"), len(t.Template))
			if t.Proxy != nil && t.Proxy.Enabled {
				line += fmt.Sprintf("  proxy=%s", orDefault(t.Proxy.Address, "default"))
			}
			fmt.Println(line)
		}
	}
	users, err := store.ListAgentUsers()
	if err != nil {
		fmt.Fprintln(os.Stderr, "team import: agent_users unreadable:", err)
		return
	}
	pool := make([]poolEntry, 0, len(users))
	for _, u := range users {
		pool = append(pool, poolEntry{UserID: u.UserID, Provider: u.Provider, BaseURL: u.BaseURL, Model: u.Model, Effort: u.Effort})
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].UserID < pool[j].UserID })
	fmt.Println("agent_users:")
	for _, u := range pool {
		line := fmt.Sprintf("  %s  provider=%s  model=%s", u.UserID, u.Provider, u.Model)
		if u.BaseURL != "" {
			line += "  base_url=" + u.BaseURL
		}
		fmt.Println(line)
	}
}

// poolEntry is the display-only projection of one agent_users record: identity,
// provider, and config, never key material (K3).
type poolEntry struct {
	UserID   string
	Provider string
	BaseURL  string
	Model    string
	Effort   string
}

// orDefault renders empty as inherit, so the summary line reads as the
// launch-type semantics instead of a blank.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
