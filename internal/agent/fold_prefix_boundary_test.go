// Part B (TEAM_MEMBER_CACHE_BEHAVIOR_PLAN.md) B3.4: "fold 后继续请求时 stable
// system/tools prefix 不变" stated at the real provider boundary. The unit tests
// in fold_cache_prefix_test.go compare captured shapes around a direct
// CompactNow; this one drives whole turns through the actual HTTP adapter so the
// assertion is on the bytes a provider would see, across a session that folds.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// prefixRecord is the cache-relevant head of one provider request: the system
// message and the tool array, in wire order.
type prefixRecord struct {
	summarize bool
	system    string
	tools     []string
}

// recordPrefix extracts the head of one provider request body.
func recordPrefix(body []byte) prefixRecord {
	rec := prefixRecord{summarize: isSummarizeRequest(body)}
	msgs := decodeMessages(body)
	if len(msgs) == 0 {
		return rec
	}
	var sys struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msgs[0], &sys); err == nil && sys.Role == "system" {
		rec.system = sys.Content
	}
	// The OpenAI-compatible shape nests the name under "function"; accept a
	// flat "name" too so the assertion cannot pass vacuously on a decode miss.
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(body, &req)
	for _, tl := range req.Tools {
		name := tl.Function.Name
		if name == "" {
			name = tl.Name
		}
		rec.tools = append(rec.tools, name)
	}
	return rec
}

// TestStablePrefixSurvivesFoldsAtTheProviderBoundary drives a session through
// enough turns to fold, over a window small enough that folding is forced, and
// asserts that every request the provider actually received carried the same
// system message and the same tool array in the same order. Summarizing requests
// count too: the fold request must reuse the live prefix rather than pay for a
// second shape.
func TestStablePrefixSurvivesFoldsAtTheProviderBoundary(t *testing.T) {
	var records []prefixRecord
	mock := &loopMock{t: t, finalText: "step complete, continuing the work"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		records = append(records, recordPrefix(body))
		r.Body = io.NopCloser(bytes.NewReader(body))
		mock.handler(w, r)
	}))
	defer srv.Close()

	reg := tool.NewRegistry()
	reg.Add(fatTool{blob: strings.Repeat("fat tool output line\n", 400)})
	a, _ := newAgent(t, srv.URL, reg, 6_000, 4)
	folds := 0
	a.svc.sink = event.FuncSink(func(e event.Event) {
		if e.Kind == event.CompactionStarted {
			folds++
		}
	})

	for i := range 8 {
		if err := a.Run(context.Background(), fmt.Sprintf("turn %d: keep going, continue the work", i)); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}

	if folds == 0 {
		t.Fatal("the fixture never folded, so this asserts nothing about surviving a fold")
	}
	if len(records) < 4 {
		t.Fatalf("expected several provider requests, got %d", len(records))
	}

	first := records[0]
	if first.system == "" {
		t.Fatal("the recorded requests must carry a system message")
	}
	if len(first.tools) == 0 {
		t.Fatal("the recorded requests must carry a tool array")
	}
	summarizeSeen := 0
	for i, rec := range records {
		if rec.summarize {
			summarizeSeen++
		}
		if rec.system != first.system {
			t.Fatalf("request %d moved the system prefix\n got %q\nwant %q", i, rec.system, first.system)
		}
		if !reflect.DeepEqual(rec.tools, first.tools) {
			t.Fatalf("request %d moved the tool array\n got %v\nwant %v", i, rec.tools, first.tools)
		}
	}
	if summarizeSeen == 0 {
		t.Fatal("no summarizing request was recorded, so the fold request's prefix is untested")
	}
}

// TestFoldAnnouncesItselfInTheNextRequestsDiagnostics is the consumed boundary
// for the compact_auto reason. A fold does not move system+tools, so the
// diagnostics it produces need a content reason or the miss it causes is
// unattributable — which is exactly what the team cache report's
// rewrite_or_compaction_correlated label keys on. The reason is queued at the
// projection install and drained at the next request's shape capture, so this
// asserts on the usage events a session with a real fold emits.
func TestFoldAnnouncesItselfInTheNextRequestsDiagnostics(t *testing.T) {
	mock := &loopMock{t: t, finalText: "step complete, continuing the work"}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()

	reg := tool.NewRegistry()
	reg.Add(fatTool{blob: strings.Repeat("fat tool output line\n", 400)})
	a, _ := newAgent(t, srv.URL, reg, 6_000, 4)

	var diagnostics []*event.CacheDiagnostics
	pruned := 0
	a.svc.sink = event.FuncSink(func(e event.Event) {
		if e.Kind == event.Usage && e.CacheDiagnostics != nil {
			diagnostics = append(diagnostics, e.CacheDiagnostics)
		}
		if e.Kind == event.ContextMaintenanceEvent && e.Maintenance != nil &&
			e.Maintenance.Status == "applied" && e.Maintenance.Action == maintenanceActionPrune {
			pruned++
		}
	})

	for i := range 8 {
		if err := a.Run(context.Background(), fmt.Sprintf("turn %d: keep going, continue the work", i)); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	if len(diagnostics) < 4 {
		t.Fatalf("expected several diagnosed requests, got %d", len(diagnostics))
	}
	if pruned == 0 {
		t.Fatal("the fixture never pruned, so the free-rewrite install site is untested")
	}

	folded, pruneReasons := 0, 0
	for i, diag := range diagnostics {
		// The two reasons that mean the shared prefix was reset would make every
		// later request cold; neither a fold nor a prune may produce them.
		if slices.Contains(diag.PrefixChangeReasons, "system") || slices.Contains(diag.PrefixChangeReasons, "tools") {
			t.Fatalf("diagnosed request %d reports a stable-prefix change: %v", i, diag.PrefixChangeReasons)
		}
		if slices.Contains(diag.PrefixChangeReasons, "compact_auto") {
			folded++
			if !diag.PrefixChanged {
				t.Fatalf("request %d carries compact_auto but does not report a prefix change: %+v", i, diag)
			}
			if diag.StablePrefixChanged {
				t.Fatalf("request %d carries compact_auto AND a stable-prefix change: %+v", i, diag)
			}
		}
		if slices.Contains(diag.PrefixChangeReasons, "prune") {
			pruneReasons++
		}
	}
	// Both are projection installs and both go through the same queue; a prune
	// that never announces itself leaves its own miss unattributable.
	if folded == 0 {
		t.Fatal("a session that folded never reported compact_auto, so the fold's miss stays unattributable")
	}
	if pruneReasons == 0 {
		t.Fatalf("the session pruned %d times but never reported the prune reason", pruned)
	}
}
