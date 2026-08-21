package team

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// toolIdentRe matches the first-cell identifier of a real mapping row, so the
// Markdown header ("旧工具名"), the "---" separator, and prose rows are skipped.
var toolIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// legacyToolNames is the 71 tools actually registered in
// cache/mult_agent_mcp/mult_agent_mcp.py: every "@mcp.tool" decorator at
// column 0 pairs with exactly one following def (the file's other "@mcp.tool"
// occurrence at line 5204 is a comment; the wrapper at 5290/5351 registers
// nothing). Inlined so this guard runs without cache/, which is gitignored.
var legacyToolNames = []string{
	"add_member",
	"check_agent_setup",
	"claim_leader",
	"delete_team",
	"get_server_config",
	"kill_team_terminals",
	"launch_team_terminals",
	"leader_ack_checkpoint",
	"leader_activate",
	"leader_add_member",
	"leader_assign_subtask",
	"leader_assign_task_to_relevant",
	"leader_authorize_member",
	"leader_batch_ack",
	"leader_broadcast",
	"leader_broadcast_to_relevant",
	"leader_check_member_status",
	"leader_checkpoint_set",
	"leader_clear_member_proxy_override",
	"leader_configure_member_permissions",
	"leader_configure_member_proxy",
	"leader_configure_proxy",
	"leader_configure_recovery",
	"leader_configure_wakeup",
	"leader_discussion_next_round",
	"leader_end_discussion",
	"leader_flush_outbox",
	"leader_get_proxy_config",
	"leader_get_recovery_context",
	"leader_grant_member_autonomy",
	"leader_launch_member_terminal",
	"leader_list_team",
	"leader_mark_task_complete",
	"leader_monitor_members",
	"leader_outbox_status",
	"leader_read_member_terminal",
	"leader_redefine_member",
	"leader_remove_member",
	"leader_select_task_members",
	"leader_set_discussion_mode",
	"leader_set_member_mode",
	"leader_sleep",
	"leader_start_discussion",
	"list_members",
	"list_teams",
	"member_acquire_file_lock",
	"member_check_leader_status",
	"member_delete_file",
	"member_get_my_task",
	"member_list_file_locks",
	"member_list_shared_files",
	"member_read_discussion",
	"member_read_file",
	"member_read_shared",
	"member_release_file_lock",
	"member_report_discussion_conclusion",
	"member_report_result",
	"member_send_message",
	"member_set_agent",
	"member_submit_patch",
	"member_terminal_status",
	"member_write_file",
	"remove_codex_mcp",
	"remove_member",
	"set_leader",
	"setup_codex_mcp",
	"team_create",
	"team_get_default_agent",
	"team_set_default_agent",
	"terminal_status",
	"unclaim_leader",
}

// TestToolMappingZeroOrphan pins TOOL_MAPPING.md (TASK.md §6.2) against the
// real registration source: every legacy tool has exactly one row, every row
// names a real tool, and the three-layer status covers each row. Any drift on
// either side fails: a row without a source symbol or a tool without a row.
func TestToolMappingZeroOrphan(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "docs", "team-mcp-port", "TOOL_MAPPING.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mapping: %v", err)
	}

	legacy := make(map[string]bool, len(legacyToolNames))
	for _, name := range legacyToolNames {
		legacy[name] = false
	}
	mapped := make(map[string]bool)
	rows := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		cells := strings.Split(strings.TrimSpace(line), "|")
		if len(cells) < 7 {
			continue // not a six-column row (prose, code fence)
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		// Only a real tool row has an identifier in the first cell; the header,
		// separator, and prose rows all fail the shape check and are skipped.
		name := strings.Trim(cells[1], "`")
		if !toolIdentRe.MatchString(name) {
			continue
		}
		rows++
		evidence, status := cells[2], cells[5]
		if _, known := legacy[name]; !known {
			t.Errorf("row %d: tool %q has no source symbol in mult_agent_mcp.py", rows, name)
		}
		if legacy[name] {
			t.Errorf("tool %q mapped more than once", name)
		}
		legacy[name] = true
		// Evidence must anchor the legacy symbol to a source line, in either
		// accepted form: "tool@line (mult_agent_mcp.py)" or the source path
		// "cache/mult_agent_mcp/mult_agent_mcp.py" (older "file:line" too).
		if !strings.Contains(evidence, "mult_agent_mcp.py") ||
			(!strings.Contains(evidence, "@") && !strings.Contains(evidence, ":")) {
			t.Errorf("row %d (%s): evidence %q not traceable to old symbol", rows, name, evidence)
		}
		if status != "核心" && status != "插件" && status != "废弃" {
			t.Errorf("row %d (%s): status %q not in {核心, 插件, 废弃}", rows, name, status)
		}
		mapped[name] = true
	}
	if rows != len(legacyToolNames) {
		t.Errorf("mapping has %d rows, want %d (one per registered tool)", rows, len(legacyToolNames))
	}
	for name, seen := range legacy {
		if !seen {
			t.Errorf("legacy tool %q has no mapping row (orphan)", name)
		}
	}
}
