package cli

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

// hubProbe records routing and in-flight overlap across concurrent hub
// submissions: per-member counts for routing, and a peak concurrency gauge
// that proves two turns genuinely overlapped (not serialized at the hub).
type hubProbe struct {
	inFlight atomic.Int64
	peak     atomic.Int64
	mu       sync.Mutex
	byMember map[string]int
}

func newHubProbe() *hubProbe { return &hubProbe{byMember: map[string]int{}} }

// entered marks one submission in-flight for member and holds the slot until
// the caller sleeps, so peak reads the true concurrency at the hub layer.
func (p *hubProbe) entered(member string) {
	cur := p.inFlight.Add(1)
	if cur > p.peak.Load() {
		p.peak.Store(cur)
	}
	p.mu.Lock()
	p.byMember[member]++
	p.mu.Unlock()
}

// hubBackend is a control.SessionAPI stand-in whose turn driver records the
// member it serves; embedding the nil interface satisfies the rest of the
// port the hub never touches.
type hubBackend struct {
	control.SessionAPI
	member string
	probe  *hubProbe
	err    error
	// decision records that a route reached this backend without ever
	// consulting the hub's own window — the per-member prompt answer.
	approves int
	answers  int
}

func (b hubBackend) SubmitUserTurnOrError(input, display string) error {
	b.probe.entered(b.member)
	time.Sleep(40 * time.Millisecond) // hold the in-flight slot until peeked
	b.probe.inFlight.Add(-1)
	return b.err
}

// approveBackend records a background prompt answer. The value receiver means
// the registry hands out a copy each bind; the probe aggregates across copies.
type approveBackend struct {
	control.SessionAPI
	member   string
	approves *int
	answers  *int
}

func (b approveBackend) Approve(id string, allow, session, persist bool) {
	*b.approves++
}
func (b approveBackend) AnswerQuestion(id string, answers []event.AskAnswer) {
	*b.answers++
}

// hubTestHub wires a real store + registry behind a hub, the same trio the
// TUI assembles: Binding resolves from the on-disk document, the registry
// builds each member's backend on first use, and the hub routes onto them.
func hubTestHub(t *testing.T, probe *hubProbe, fail error) (*teamHub, *team.TeamStore) {
	t.Helper()
	root := t.TempDir()
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive, Role: team.RoleCoder},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	backends := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return hubBackend{member: b.MemberID, probe: probe, err: fail}, nil
	}, 4)
	return newTeamHub(store, backends, "alpha"), store
}

// TestTeamHubRoutesByMemberID pins the routing contract: each member id
// resolves its own binding and lands on its own backend; the hub never
// confuses the two.
func TestTeamHubRoutesByMemberID(t *testing.T) {
	probe := newHubProbe()
	hub, _ := hubTestHub(t, probe, nil)
	if err := hub.Submit("lead", "leader turn"); err != nil {
		t.Fatal(err)
	}
	if err := hub.Submit("coder", "member turn"); err != nil {
		t.Fatal(err)
	}
	if probe.byMember["lead"] != 1 || probe.byMember["coder"] != 1 {
		t.Fatalf("routing = %v, want one submit each", probe.byMember)
	}
}

// TestTeamHubTargetResolvesByTeamAndMember pins the explicit Target route the
// composer path uses: the backend of a named member on a named team resolves
// without starting a turn, so the submit can name its target independently of
// which member the window is bound to.
func TestTeamHubTargetResolvesByTeamAndMember(t *testing.T) {
	probe := newHubProbe()
	hub, _ := hubTestHub(t, probe, nil)
	target, err := hub.Target("alpha", "coder")
	if err != nil {
		t.Fatal(err)
	}
	hubBackend, ok := target.(hubBackend)
	if !ok || hubBackend.member != "coder" {
		t.Fatalf("Target resolved %T, want the coder backend", target)
	}
	_, err = hub.Target("alpha", "nobody")
	if !errors.Is(err, team.ErrMemberNotFound) {
		t.Errorf("target unknown member err = %v, want ErrMemberNotFound", err)
	}
	if _, err := hub.Target("", "coder"); err == nil {
		t.Error("Target with an empty team must error")
	}
}

// TestTeamHubConcurrentDistinctMembers proves the P2 concurrency property:
// two distinct member ids submitting at once overlap in flight — the hub
// builds each backend once and runs both turns, with no serialization at the
// hub layer.
func TestTeamHubConcurrentDistinctMembers(t *testing.T) {
	probe := newHubProbe()
	hub, _ := hubTestHub(t, probe, nil)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{"lead", "coder"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			errs[i] = hub.Submit(id, "turn "+id)
		}(i, id)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if got := probe.peak.Load(); got < 2 {
		t.Fatalf("peak concurrency = %d, want ≥ 2 — the two member turns serialized at the hub", got)
	}
	if probe.byMember["lead"] != 1 || probe.byMember["coder"] != 1 {
		t.Fatalf("routing = %v", probe.byMember)
	}
}

// TestTeamHubLeaderAndMemberDoNotSerialize names the same property for the
// explicit acceptance case from the P2 task: a leader submission and a member
// submission proceed in parallel at the hub layer.
func TestTeamHubLeaderAndMemberDoNotSerialize(t *testing.T) {
	probe := newHubProbe()
	hub, _ := hubTestHub(t, probe, nil)
	var wg sync.WaitGroup
	for _, id := range []string{"lead", "coder"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := hub.Submit(id, "turn "+id); err != nil {
				t.Errorf("submit %s: %v", id, err)
			}
		}(id)
	}
	wg.Wait()
	if got := probe.peak.Load(); got < 2 {
		t.Fatalf("leader and member submissions serialized at the hub (peak = %d)", got)
	}
}

// TestTeamHubErrors pins the failure surface: an unknown member, empty args,
// an unavailable seam, and a refused admission all surface instead of dropping.
func TestTeamHubErrors(t *testing.T) {
	probe := newHubProbe()
	hub, store := hubTestHub(t, probe, nil)
	if _, err := store.Binding("alpha", "nobody"); !errors.Is(err, team.ErrMemberNotFound) {
		t.Fatalf("nobody binding err = %v, want ErrMemberNotFound", err)
	}
	if err := hub.Submit("nobody", "x"); !errors.Is(err, team.ErrMemberNotFound) {
		t.Errorf("unknown member err = %v, want ErrMemberNotFound", err)
	}
	if err := hub.Submit("", "x"); err == nil {
		t.Error("empty member must error")
	}
	if err := hub.Submit("lead", "   "); err == nil {
		t.Error("empty text must error")
	}
	bare := newTeamHub(nil, nil, "alpha")
	if err := bare.Submit("lead", "x"); err == nil {
		t.Error("a hub without a store/registry must error")
	}

	// A refused admission (the member backend is busy) surfaces the reason.
	refused := fmt.Errorf("member backend did not accept the turn")
	failing := newHubProbe()
	busyHub, _ := hubTestHub(t, failing, refused)
	if err := busyHub.Submit("coder", "turn"); !errors.Is(err, refused) {
		t.Errorf("refused admission err = %v, want the backend's reason", err)
	}
}

// TestTeamHubAssembleIsLazyAndReused keeps registry semantics visible from the
// hub: a second submit to an already-routed member reuses the assembled
// backend instead of rebuilding it.
func TestTeamHubAssembleIsLazyAndReused(t *testing.T) {
	probe := newHubProbe()
	builds := 0
	root := t.TempDir()
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	backends := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return hubBackend{member: b.MemberID, probe: probe}, nil
	}, 4)
	hub := newTeamHub(store, backends, "alpha")
	if err := hub.Submit("coder", "one"); err != nil {
		t.Fatal(err)
	}
	if err := hub.Submit("coder", "two"); err != nil {
		t.Fatal(err)
	}
	if builds != 1 || probe.byMember["coder"] != 2 {
		t.Fatalf("builds = %d, submits = %v — the hub must reuse the assembled backend", builds, probe.byMember)
	}
}

// TestTeamHubApprovesBackgroundMember pins the P3.2 prompt-routing contract: a
// pending approval on a non-bound member resolves on that member's backend —
// the route reaches the member no matter which member the window is bound to,
// and the bound window's own prompt state is never touched.
func TestTeamHubApprovesBackgroundMember(t *testing.T) {
	root := t.TempDir()
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive, Role: team.RoleCoder},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	approves, answers := 0, 0
	backends := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return approveBackend{member: b.MemberID, approves: &approves, answers: &answers}, nil
	}, 4)
	hub := newTeamHub(store, backends, "alpha")
	// The prompt came out of coder's own backend, so it is assembled by
	// construction. A freshly built controller holds no such prompt id and would
	// "succeed" against nothing, so the hub reads the registry instead of binding.
	binding, err := store.Binding("alpha", "coder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backends.bind(binding); err != nil {
		t.Fatal(err)
	}
	if err := hub.Approve("alpha", "coder", "a1", true, false, false); err != nil {
		t.Fatal(err)
	}
	if approves != 1 || answers != 0 {
		t.Fatalf("background approve reached the wrong backend: approves=%d answers=%d", approves, answers)
	}
	if err := hub.Answer("alpha", "coder", "q1", []event.AskAnswer{{QuestionID: "q1", Selected: []string{"ok"}}}); err != nil {
		t.Fatal(err)
	}
	if answers != 1 {
		t.Fatalf("background ask answer reached the wrong backend: answers=%d", answers)
	}
	// An unknown member must not route anywhere, and neither must a known member
	// with no assembled backend: its prompt cannot exist, so a "success" there
	// would be a lie the roster then clears the badge on.
	if err := hub.Approve("alpha", "nobody", "a1", true, false, false); err == nil {
		t.Error("approve on an unknown member must fail")
	}
	if err := hub.Approve("alpha", "lead", "a1", true, false, false); err == nil {
		t.Error("approve on a member with no live session must fail, not assemble one")
	}
	if approves != 1 {
		t.Errorf("a refused route must not reach any backend, approves = %d", approves)
	}
}
