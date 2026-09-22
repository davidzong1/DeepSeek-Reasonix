package cli

// Acceptance tests for the B4 fix in TEAM_MEMBER_PARALLELISM_ROUTE.md: a roster
// larger than the retention cap used to evict its own idle members on every
// bind, so a six-member team paid a full boot.Build per member switch.

import (
	"sync"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// rosterService stores a team with the given member ids (the first is the
// leader) and returns a task service over it. The registry reads the roster
// through exactly this service, the way an opened overlay installs it.
func rosterService(t *testing.T, teamName string, memberIDs []string) *teamTaskService {
	t.Helper()
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	slots := make([]team.MemberSlot, 0, len(memberIDs))
	for i, id := range memberIDs {
		slot := team.MemberSlot{MemberID: id, Role: team.RoleCoder, Status: team.MemberStatusActive}
		if i == 0 {
			slot.Leader = true
		}
		slots = append(slots, slot)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: teamName, Template: slots,
	}}}); err != nil {
		t.Fatal(err)
	}
	return newTeamTaskService(teamStore, nil, "", nil)
}

// TestTeamBackendsCapFitsTheWholeRoster pins the fix: binding every member of a
// six-member team under a cap of four assembles each one exactly once and
// retires none — no thrash, and every member stays bound for the next switch.
func TestTeamBackendsCapFitsTheWholeRoster(t *testing.T) {
	service := rosterService(t, "alpha", []string{"lead", "m1", "m2", "m3", "m4", "m5"})
	var mu sync.Mutex
	builds := map[string]int{}
	closed := 0
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		mu.Lock()
		builds[b.MemberID]++
		mu.Unlock()
		return fakeBackend{closed: &closed}, nil
	}, defaultMaxTeamBackends)
	r.setTasks(service)

	for _, id := range []string{"lead", "m1", "m2", "m3", "m4", "m5"} {
		if _, err := r.bind(binding("alpha", id)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"lead", "m1", "m2", "m3", "m4", "m5"} {
		if _, ok := r.bound("alpha", id); !ok {
			t.Fatalf("member %s was retired: the cap must hold the whole roster", id)
		}
		if got := builds[id]; got != 1 {
			t.Fatalf("member %s assembled %d times, want 1 (retire/rebuild thrash)", id, got)
		}
	}
	if closed != 0 {
		t.Fatalf("closed backends = %d, want 0", closed)
	}
}

// TestTeamBackendsCapStillSparesBusyMembers pins the other half: fitting the cap
// to a roster bounds retention, it does not disable it. A second team's binds
// still exceed the fitted cap, and a running member is still never the victim.
func TestTeamBackendsCapStillSparesBusyMembers(t *testing.T) {
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	teamDoc := func(name string, ids ...string) team.Team {
		slots := make([]team.MemberSlot, 0, len(ids))
		for i, id := range ids {
			slot := team.MemberSlot{MemberID: id, Role: team.RoleCoder, Status: team.MemberStatusActive}
			if i == 0 {
				slot.Leader = true
			}
			slots = append(slots, slot)
		}
		return team.Team{Name: name, Template: slots}
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{
		teamDoc("alpha", "m1", "m2", "m3"),
		teamDoc("beta", "n1", "n2", "n3"),
	}}); err != nil {
		t.Fatal(err)
	}
	closed := 0
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		status := control.RuntimeStatus{}
		if b.Team == "alpha" && b.MemberID == "m1" {
			status.Running = true // m1 keeps turning while the other team binds
		}
		return fakeBackend{closed: &closed, status: status}, nil
	}, defaultMaxTeamBackends)
	r.setTasks(newTeamTaskService(teamStore, nil, "", nil))

	// Three binds fill the fitted cap (largest roster wins) with alpha's roster.
	for _, id := range []string{"m1", "m2", "m3"} {
		if _, err := r.bind(binding("alpha", id)); err != nil {
			t.Fatal(err)
		}
	}
	// beta's binds now exceed the fitted cap: the sweep must spare alpha's
	// running member and retire the next idle one instead.
	for _, id := range []string{"n1", "n2"} {
		if _, err := r.bind(binding("beta", id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := r.bound("alpha", "m1"); !ok {
		t.Fatal("a running member was retired to make room: the busy guard must survive the fitted cap")
	}
	if _, ok := r.bound("alpha", "m2"); ok {
		t.Fatal("the sweep must still retire an idle member when the live set exceeds the cap")
	}
	if closed != 1 {
		t.Fatalf("closed backends = %d, want exactly 1 retirement", closed)
	}
}

// TestTeamBackendsCapUnchangedWithoutARoster pins the off-switch: a host with no
// task service (tests, non-interactive) keeps the configured cap exactly.
func TestTeamBackendsCapUnchangedWithoutARoster(t *testing.T) {
	r := newTeamBackends(func(team.MemberBinding) (control.SessionAPI, error) {
		return fakeBackend{closed: new(int)}, nil
	}, 2)
	r.fitToTeam("alpha")
	if r.max != 2 {
		t.Fatalf("cap = %d, want the configured 2 when no roster is available", r.max)
	}
}
