package cli

import (
	"strings"
	"testing"
)

// assertUndrivenReportError pins the report-repair contract shared by the
// coder regression suite (team_report_regression_test.go) and the acceptance
// fixtures: the member sees an actionable refusal naming the undriven row and
// the leader's fix, never the runtime's raw ErrTaskUnknown. Both refusal
// shapes share the contract: the single-id form says "is not executing
// (recorded X, nothing is driving it)", the multi-row form says "none is
// executing (id (recorded X))".
func assertUndrivenReportError(t *testing.T, err error) {
	t.Helper()
	text := err.Error()
	if strings.Contains(text, "agentruntime: unknown task") {
		t.Fatalf("report must not leak the raw runtime refusal, got: %s", text)
	}
	if !strings.Contains(text, "executing") || !strings.Contains(text, "retry or reassign") {
		t.Fatalf("report error must name the undriven row and the leader's fix, got: %s", text)
	}
}
