package capability

import (
	"slices"
	"testing"

	"reasonix/internal/tool"
)

// A delegation tool is hidden from the provider schema, so the router is the
// only way it reaches the model without being asked for. These cases pin the
// three rules that make that safe: only a declared tool routes, it only ever
// suggests, and an ordinary request matches nothing.
func TestDeclaredToolsRouteAndUndeclaredOnesDoNot(t *testing.T) {
	entries := ToolEntries([]tool.ContractEntry{
		{Name: "orchestrate", Description: "declare a plan", Triggers: []string{"orchestrate", "编排"}},
		{Name: "kill_shell", Description: "stop a shell"}, // no declaration → unroutable
	})
	if got := entries[0].AutoUse; got != AutoUseSuggest {
		t.Fatalf("a declared tool carries auto-use %q, want suggest", got)
	}
	if got := entries[1].AutoUse; got != "" {
		t.Fatalf("an undeclared tool carries auto-use %q, want none", got)
	}

	hit := Route("please orchestrate these steps", entries)
	if len(hit.Candidates) != 1 || hit.Candidates[0].Entry.ID != "tool:orchestrate" {
		t.Fatalf("declared trigger did not route: %+v", hit.Candidates)
	}
	if hit.Candidates[0].Policy != AutoUseSuggest {
		t.Fatalf("routed tool carries policy %q; a tool may only suggest", hit.Candidates[0].Policy)
	}
	if miss := Route("stop that shell", entries); len(miss.Candidates) != 0 {
		t.Fatalf("an undeclared tool was routed: %+v", miss.Candidates)
	}
	if none := Route("fix the typo in main.go", entries); len(none.Candidates) != 0 {
		t.Fatalf("an ordinary request routed something: %+v", none.Candidates)
	}
}

// A tool with no triggers contributes no catalog entry at all, so opting in is
// what grows the directory — not registration.
func TestUndeclaredToolsStayOutOfTheCatalog(t *testing.T) {
	opts := CatalogOptions{
		Tools: []tool.ContractEntry{{Name: "read_file", Description: "read"}},
		RoutableTools: []tool.ContractEntry{
			{Name: "silent", Description: "no triggers"},
			{Name: "orchestrate", Description: "plan", Triggers: []string{"编排"}},
		},
	}
	ids := []string{}
	for _, e := range BuildCatalog(opts).Entries {
		ids = append(ids, e.ID)
	}
	if !slices.Contains(ids, "tool:orchestrate") {
		t.Fatalf("a declared hidden tool is missing from the catalog: %v", ids)
	}
	if slices.Contains(ids, "tool:silent") {
		t.Fatalf("an undeclared hidden tool entered the catalog: %v", ids)
	}
}
