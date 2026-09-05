package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/knowledge_base/adapter"
	"reasonix/internal/knowledge_base/model"
)

// e2eAdapter is the test host: a fixed clock, rule-only quota, and a sink that
// records every event. Helpers carry an e2e prefix so coder's tests never
// collide with them (per team split, manager tests are tester-owned).
type e2eAdapter struct {
	clock time.Time
	quota adapter.Quota

	mu sync.Mutex
	ev []adapter.Event
}

func (e *e2eAdapter) Clock() time.Time      { return e.clock }
func (e *e2eAdapter) Quota() adapter.Quota  { return e.quota }
func (e *e2eAdapter) Emit(ev adapter.Event) { e.mu.Lock(); e.ev = append(e.ev, ev); e.mu.Unlock() }

func (e *e2eAdapter) eventNames() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]int{}
	for _, ev := range e.ev {
		out[ev.Name]++
	}
	return out
}

func e2eNew(t *testing.T, dir, team string) (*Manager, *e2eAdapter) {
	t.Helper()
	a := &e2eAdapter{clock: time.Now()}
	m, err := New(a, Options{DataRoot: dir, TeamID: team})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, a
}

func e2eThought(text, agent string) model.Thought {
	return model.Thought{
		ID: model.NewID(), TeamID: "alpha", AgentID: agent,
		Kind: model.ThoughtDecision, Text: text, CreatedAt: time.Now(),
	}
}

func e2eFlush(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func e2eIngest(t *testing.T, m *Manager, thoughts []model.Thought) {
	t.Helper()
	if _, err := m.Ingest(context.Background(), thoughts); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	e2eFlush(t, m)
}

func e2eQueryAll(t *testing.T, m *Manager) []model.Result {
	t.Helper()
	res, err := m.Query(context.Background(), model.Query{Scope: model.ScopeTeam, Limit: 50})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return res
}

func TestE2EGoldenPathThoughtToQuery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, a := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{e2eThought("decision: use postgres as the primary datastore", "alice")})

	res := e2eQueryAll(t, m)
	if len(res) != 1 {
		t.Fatalf("Query after golden ingest = %d results, want 1", len(res))
	}
	it := res[0].Item
	if it.Status != model.StatusLive || it.ID == "" {
		t.Errorf("golden item not live/complete: %+v", it)
	}
	if it.Body != "decision: use postgres as the primary datastore" {
		t.Errorf("body mismatch: %q", it.Body)
	}
	if it.Scope != model.ScopeTeam || it.Kind != model.ItemDecision {
		t.Errorf("scope/kind = %s/%s", it.Scope, it.Kind)
	}
	// Durable on disk under DataRoot/<team>/items/*.md.
	md, gerr := filepath.Glob(filepath.Join(dir, "alpha", "items", "*.md"))
	if gerr != nil || len(md) != 1 {
		t.Errorf("items on disk = %v (err %v), want 1 file", md, gerr)
	}
	names := a.eventNames()
	if names["item_committed"] != 1 {
		t.Errorf("expected one item_committed, got %v", names)
	}
}

func TestE2EDenyAndNeedsLLMProduceNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	deny := e2eThought("lorem ipsum placeholder text should never become knowledge", "alice")
	needsLLM := e2eThought("this is just a plain descriptive sentence with no rule marker", "bob")
	e2eIngest(t, m, []model.Thought{deny, needsLLM})

	if res := e2eQueryAll(t, m); len(res) != 0 {
		t.Fatalf("deny/needs_llm produced results: %+v", res)
	}
}

func TestE2EConcurrentIngestNoLossNoDup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	for k := 0; k < n; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			text := "decision: parallel topic item number " + itoa(k)
			_, errs[k] = m.Ingest(context.Background(), []model.Thought{e2eThought(text, "alice")})
		}(k)
	}
	wg.Wait()
	for k := range errs {
		if errs[k] != nil {
			t.Fatalf("concurrent Ingest #%d: %v", k, errs[k])
		}
	}
	e2eFlush(t, m)

	res := e2eQueryAll(t, m)
	if len(res) != n {
		t.Fatalf("Query after concurrent ingest = %d, want %d (no loss)", len(res), n)
	}
	if b, err := m.Backlog(context.Background()); err != nil || b != 0 {
		t.Errorf("Backlog after flush = %d err %v, want 0", b, err)
	}
	seen := map[string]bool{}
	for _, r := range res {
		if seen[r.Item.ID] {
			t.Fatalf("duplicate item id %s", r.Item.ID)
		}
		seen[r.Item.ID] = true
	}
}

func TestE2EDuplicateIngestIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, a := e2eNew(t, dir, "alpha")

	th := e2eThought("decision: exactly one canonical slot", "alice")
	e2eIngest(t, m, []model.Thought{th})
	e2eIngest(t, m, []model.Thought{th}) // L1 duplicate

	if res := e2eQueryAll(t, m); len(res) != 1 {
		t.Fatalf("duplicate ingest produced %d live items, want 1", len(res))
	}
	if names := a.eventNames(); names["item_committed"] != 1 {
		t.Errorf("expected one commit for a duplicate, got %v", names)
	}
}

func TestE2ESupersedeVersionChain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{e2eThought("decision: adopt blue-green deploys", "alice")})
	v1 := e2eQueryAll(t, m)
	if len(v1) != 1 {
		t.Fatal("expected one live after first ingest")
	}
	id1 := v1[0].Item.ID

	// Same canonical (same first line), richer body => next version supersedes.
	e2eIngest(t, m, []model.Thought{e2eThought("decision: adopt blue-green deploys\nrolled back via canary gate", "alice")})
	v2 := e2eQueryAll(t, m)
	if len(v2) != 1 || v2[0].Item.ID == id1 {
		t.Fatalf("expected a single superseding live item, got %+v", v2)
	}
	id2 := v2[0].Item.ID
	if v2[0].Item.Version != 2 || v2[0].Item.Supersedes != id1 {
		t.Errorf("v2 version/links = %d / %q", v2[0].Item.Version, v2[0].Item.Supersedes)
	}
	old, err := m.st.Get(id1)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != model.StatusSuperseded || old.SupersededBy != id2 {
		t.Errorf("v1 not marked superseded: %+v", old)
	}
}

func TestE2EConflictKeepsBothProducers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{e2eThought("decision: choose the primary region", "alice")})
	first := e2eQueryAll(t, m)
	if len(first) != 1 {
		t.Fatal("expected one live first")
	}
	idA := first[0].Item.ID

	e2eIngest(t, m, []model.Thought{e2eThought("decision: choose the primary region\nbob insists on ap-southeast", "bob")})
	res := e2eQueryAll(t, m)
	if len(res) != 2 {
		t.Fatalf("cross-producer conflict should keep both live, got %d", len(res))
	}
	var bID string
	for _, r := range res {
		if r.Item.AuthorID == "bob" {
			bID = r.Item.ID
		}
	}
	if bID == "" {
		t.Fatal("bob's conflicting item missing from query")
	}
	old, err := m.st.Get(idA)
	if err != nil {
		t.Fatal(err)
	}
	if old.ConflictWith != bID {
		t.Errorf("alice item conflict link = %q, want %q", old.ConflictWith, bID)
	}
}

func TestE2ERetireSoftDelete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{e2eThought("decision: retire this later", "alice")})
	res := e2eQueryAll(t, m)
	if len(res) != 1 {
		t.Fatal("expected one live before retire")
	}
	id := res[0].Item.ID

	if err := m.Retire(context.Background(), []string{id}, model.ReasonNoLongerTrue); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	e2eFlush(t, m)

	if out := e2eQueryAll(t, m); len(out) != 0 {
		t.Errorf("retired item still queryable: %+v", out)
	}
	it, err := m.st.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != model.StatusRetired {
		t.Errorf("item status after retire = %s", it.Status)
	}

	// Idempotent re-retire and unknown-id retire are no-ops.
	if err := m.Retire(context.Background(), []string{id}, model.ReasonNoLongerTrue); err != nil {
		t.Errorf("re-retire should be a no-op, got %v", err)
	}
	if err := m.Retire(context.Background(), []string{model.NewID()}, model.ReasonNoLongerTrue); err != nil {
		t.Errorf("retire unknown id should be a no-op, got %v", err)
	}
	e2eFlush(t, m)

	// Fail-closed on a non-whitelisted reason.
	if err := m.Retire(context.Background(), []string{id}, "because"); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("bad reason = %v, want ErrInvalid", err)
	}
	if err := m.Retire(context.Background(), []string{"../x"}, model.ReasonNoLongerTrue); err == nil {
		t.Error("escaping id should be rejected by Retire")
	}
}

func TestE2ERestartPersistsAndReplaysCursor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	a1 := &e2eAdapter{clock: time.Now()}
	m1, err := New(a1, Options{DataRoot: dir, TeamID: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	e2eIngest(t, m1, []model.Thought{e2eThought("decision: state survives restart", "alice")})
	e2eIngest(t, m1, []model.Thought{e2eThought("decision: second durable item", "alice")})
	if res := e2eQueryAll(t, m1); len(res) != 2 {
		t.Fatalf("before restart live = %d", len(res))
	}
	if err := m1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Restart: durable items are re-indexed from store, committed cursor is not
	// replayed, so nothing is duplicated.
	m2, a2 := e2eNew(t, dir, "alpha")
	res := e2eQueryAll(t, m2)
	if len(res) != 2 {
		t.Fatalf("after restart live = %d, want 2", len(res))
	}
	if names := a2.eventNames(); names["item_committed"] != 0 {
		t.Errorf("restart re-committed events: %v", names)
	}
}

func TestE2EClearTeamIsolation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")

	e2eIngest(t, m, []model.Thought{e2eThought("decision: clear me away", "alice")})
	if res := e2eQueryAll(t, m); len(res) != 1 {
		t.Fatal("expected one live before clear")
	}

	if err := m.ClearTeam(context.Background(), "beta", model.ScopeTeam); !errors.Is(err, ErrInvalidTeam) {
		t.Errorf("ClearTeam wrong team = %v, want ErrInvalidTeam", err)
	}
	if err := m.ClearTeam(context.Background(), "alpha", model.ScopeTeam); err != nil {
		t.Fatalf("ClearTeam: %v", err)
	}
	e2eFlush(t, m)

	if res := e2eQueryAll(t, m); len(res) != 0 {
		t.Errorf("after clear query returned %d", len(res))
	}
	trash, gerr := filepath.Glob(filepath.Join(dir, ".trash", "*"))
	if gerr != nil || len(trash) != 1 {
		t.Fatalf("expected one trash dir, got %v (err %v)", trash, gerr)
	}
	// Same team can be re-ingested into a fresh, empty store.
	e2eIngest(t, m, []model.Thought{e2eThought("decision: new life after clear", "alice")})
	if res := e2eQueryAll(t, m); len(res) != 1 {
		t.Errorf("post-clear ingest live = %d, want 1", len(res))
	}
}

func TestE2EIllegalTeamAndInvalidNew(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	a := &e2eAdapter{clock: time.Now()}
	for _, team := range []string{"", "../escape", "a/b", "Agent设计"} {
		if _, err := New(a, Options{DataRoot: dir, TeamID: team}); err == nil {
			t.Errorf("New with team %q should fail", team)
		}
	}

	ma, _ := e2eNew(t, dir, "alpha")
	mb, _ := e2eNew(t, dir, "beta")
	e2eIngest(t, ma, []model.Thought{e2eThought("decision: alpha-only knowledge", "alice")})

	if ra := e2eQueryAll(t, ma); len(ra) != 1 {
		t.Errorf("alpha should see its item, got %d", len(ra))
	}
	if rb := e2eQueryAll(t, mb); len(rb) != 0 {
		t.Errorf("beta leaked alpha items: %d", len(rb))
	}
	betaFiles, _ := filepath.Glob(filepath.Join(dir, "beta", "items", "*.md"))
	if len(betaFiles) != 0 {
		t.Errorf("beta items dir has files: %v", betaFiles)
	}
}

func TestE2EQueryOnlySeesCommitted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")
	for i := 0; i < 5; i++ {
		e2eIngest(t, m, []model.Thought{e2eThought("decision: committed item number "+itoa(i), "alice")})
		if res := e2eQueryAll(t, m); len(res) != i+1 {
			t.Fatalf("after %d committed ingests query = %d, want %d (only committed visible)", i+1, len(res), i)
		}
	}
}

// TestE2EConcurrentClearTeamIngestLinearized races ClearTeam against Ingest and
// Query calls. The clear job is enqueued first and the single worker drains
// FIFO, so every round must show a clean cut: every item older than the round's
// ClearTeam disappears, exactly the round's post-clear ingests survive, and no
// item is lost or duplicated. beta's data must stay untouched (no overreach).
// Run under -race this also exercises host reads against the store/queue swap.
func TestE2EConcurrentClearTeamIngestLinearized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")

	// beta is seeded once; alpha's concurrent clears must never disturb it.
	mb, _ := e2eNew(t, dir, "beta")
	e2eIngest(t, mb, []model.Thought{e2eThought("decision: beta keeps region-eu", "bob")})
	e2eIngest(t, mb, []model.Thought{e2eThought("decision: beta keeps region-us", "bob")})
	e2eIngest(t, mb, []model.Thought{e2eThought("decision: beta keeps region-ap", "bob")})

	m, a := e2eNew(t, dir, "alpha")
	for i := 0; i < 4; i++ {
		e2eIngest(t, m, []model.Thought{e2eThought("decision: alpha seed number "+itoa(i), "alice")})
	}

	for round := 0; round < 6; round++ {
		var older []string
		for _, r := range e2eQueryAll(t, m) {
			older = append(older, r.Item.ID)
		}
		if err := m.ClearTeam(context.Background(), "alpha", model.ScopeTeam); err != nil {
			t.Fatalf("round %d ClearTeam: %v", round, err)
		}
		const postN = 5
		var wg sync.WaitGroup
		var qErr error
		var qErrMu sync.Mutex
		for i := 0; i < postN; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				text := "decision: alpha round " + itoa(round) + " post " + itoa(i)
				if _, err := m.Ingest(context.Background(), []model.Thought{e2eThought(text, "alice")}); err != nil {
					t.Errorf("round %d ingest: %v", round, err)
				}
			}(i)
		}
		for k := 0; k < 4; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := m.Query(context.Background(), model.Query{Limit: 50}); err != nil {
					qErrMu.Lock()
					qErr = err
					qErrMu.Unlock()
				}
			}()
		}
		wg.Wait()
		e2eFlush(t, m)

		if qErr != nil {
			t.Fatalf("round %d concurrent Query errored: %v", round, qErr)
		}
		res := e2eQueryAll(t, m)
		if len(res) != postN {
			t.Fatalf("round %d survivors = %d, want the %d post-clear ingests", round, len(res), postN)
		}
		seen := map[string]bool{}
		for _, r := range res {
			if seen[r.Item.ID] {
				t.Fatalf("round %d duplicate survivor %s", round, r.Item.ID)
			}
			seen[r.Item.ID] = true
			for _, old := range older {
				if r.Item.ID == old {
					t.Fatalf("round %d pre-clear item %s survived ClearTeam", round, r.Item.ID)
				}
			}
		}
	}

	if b, err := m.Backlog(context.Background()); err != nil || b != 0 {
		t.Errorf("Backlog after concurrent rounds = %d err %v", b, err)
	}
	if names := a.eventNames(); names["job_failed"]+names["commit_failed"] != 0 {
		t.Errorf("worker failures during concurrent clears: %v", names)
	}

	// Final clear, then one post-clear ingest proves alpha is fully usable.
	if err := m.ClearTeam(context.Background(), "alpha", model.ScopeTeam); err != nil {
		t.Fatalf("final ClearTeam: %v", err)
	}
	e2eFlush(t, m)
	e2eIngest(t, m, []model.Thought{e2eThought("decision: alpha alive after all clears", "alice")})
	if res := e2eQueryAll(t, m); len(res) != 1 {
		t.Fatalf("post-clear alpha ingest live = %d, want 1", len(res))
	}

	// No cross-team overreach: beta kept its items and stays queryable.
	if len(e2eQueryAll(t, mb)) != 3 {
		t.Fatalf("beta items disturbed by alpha clears, want 3")
	}
	betaFiles, _ := filepath.Glob(filepath.Join(dir, "beta", "items", "*.md"))
	if len(betaFiles) != 3 {
		t.Errorf("beta items dir = %d files, want 3", len(betaFiles))
	}
	if _, err := mb.Query(context.Background(), model.Query{Limit: 50}); err != nil {
		t.Fatalf("beta query after alpha clears: %v", err)
	}
}

// TestE2ERepeatedClearTeamIdempotent clears the same team several times. Every
// repeat must succeed, leave the query surface empty, move one more dir to
// .trash, and keep the manager able to accept new life afterwards.
func TestE2ERepeatedClearTeamIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, a := e2eNew(t, dir, "alpha")
	e2eIngest(t, m, []model.Thought{e2eThought("decision: disposable fact one", "alice")})
	e2eIngest(t, m, []model.Thought{e2eThought("decision: disposable fact two", "alice")})

	const clears = 3
	for i := 0; i < clears; i++ {
		if err := m.ClearTeam(context.Background(), "alpha", model.ScopeTeam); err != nil {
			t.Fatalf("ClearTeam #%d: %v", i+1, err)
		}
		e2eFlush(t, m)
		if res := e2eQueryAll(t, m); len(res) != 0 {
			t.Fatalf("after clear #%d live = %d, want 0", i+1, len(res))
		}
		if b, err := m.Backlog(context.Background()); err != nil || b != 0 {
			t.Fatalf("after clear #%d backlog = %d err %v", i+1, b, err)
		}
	}
	trash, gerr := filepath.Glob(filepath.Join(dir, ".trash", "*"))
	if gerr != nil || len(trash) != clears {
		t.Fatalf(".trash dirs = %d (err %v), want %d", len(trash), gerr, clears)
	}
	if names := a.eventNames(); names["team_cleared"] != clears {
		t.Errorf("team_cleared events = %d, want %d", names["team_cleared"], clears)
	}
	e2eIngest(t, m, []model.Thought{e2eThought("decision: fresh after repeated clears", "alice")})
	if res := e2eQueryAll(t, m); len(res) != 1 {
		t.Errorf("post-clear ingest live = %d, want 1", len(res))
	}
}

// TestE2EClearRecreatesQueueLogAndRestart pins the events.log recovery path:
// ClearTeam moves the old <team>/queue away and reopens a fresh empty queue at
// the bound path. A post-clear ingest must survive a real restart with no replay
// duplication and no resurrection of pre-clear events.
func TestE2EClearRecreatesQueueLogAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")
	e2eIngest(t, m, []model.Thought{e2eThought("decision: pre-clear durable", "alice")})

	if err := m.ClearTeam(context.Background(), "alpha", model.ScopeTeam); err != nil {
		t.Fatalf("ClearTeam: %v", err)
	}
	e2eFlush(t, m)

	logPath := filepath.Join(dir, "alpha", "queue", "events.log")
	if fi, err := os.Stat(logPath); err != nil {
		t.Fatalf("events.log not recreated after ClearTeam: %v", err)
	} else if fi.Size() != 0 {
		t.Fatalf("recreated events.log not empty: %d bytes", fi.Size())
	}
	if _, err := os.Stat(filepath.Join(dir, "alpha", "queue", "cursor")); !os.IsNotExist(err) {
		t.Errorf("fresh queue should have no cursor file yet (err %v)", err)
	}

	e2eIngest(t, m, []model.Thought{e2eThought("decision: after-clear survivor", "alice")})
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	m2, a2 := e2eNew(t, dir, "alpha")
	if res := e2eQueryAll(t, m2); len(res) != 1 {
		t.Fatalf("restart after clear live = %d, want 1 (no pre-clear resurrect, no dup)", len(res))
	}
	if names := a2.eventNames(); names["item_committed"] != 0 {
		t.Errorf("restart re-processed events: %v", names)
	}
}

// TestE2EQueryScopeFilter pins Query scope-filter behavior at the manager
// boundary: an empty scope means "no scope filter", ScopeTeam matches the team
// items this MVP builds, and any other scope matches nothing.
func TestE2EQueryScopeFilter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kb")
	m, _ := e2eNew(t, dir, "alpha")
	e2eIngest(t, m, []model.Thought{e2eThought("decision: scope scoped decision", "alice")})

	if len(mustQueryE2E(t, m, model.Query{Scope: model.ScopeTeam, Limit: 50})) != 1 {
		t.Fatal("ScopeTeam query missed the team item")
	}
	if len(mustQueryE2E(t, m, model.Query{Limit: 50})) != 1 {
		t.Fatal("empty scope must not filter out team items")
	}
	for _, sc := range []model.Scope{model.ScopeAgent, model.ScopeProject, model.ScopeGlobal} {
		if got := mustQueryE2E(t, m, model.Query{Scope: sc, Limit: 50}); len(got) != 0 {
			t.Fatalf("Scope %q leaked team items: %d", sc, len(got))
		}
	}
}

func mustQueryE2E(t *testing.T, m *Manager, q model.Query) []model.Result {
	t.Helper()
	res, err := m.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return res
}

func itoa(k int) string {
	if k == 0 {
		return "0"
	}
	var b []byte
	for k > 0 {
		b = append([]byte{byte('0' + k%10)}, b...)
		k /= 10
	}
	return string(b)
}
