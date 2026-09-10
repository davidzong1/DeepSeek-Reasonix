package team

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// failoverStore returns a TeamSessionStore rooted at a temp dir.
func failoverStore(t *testing.T) *TeamSessionStore {
	t.Helper()
	s, err := NewTeamSessionStoreDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNextPoolRefOrderAndWrap(t *testing.T) {
	pool := []string{"au-1", "au-2", "au-3"}
	if got, ok := NextPoolRef(pool, nil, "au-1"); !ok || got != "au-2" {
		t.Fatalf("after au-1 = %q ok=%v, want au-2", got, ok)
	}
	if got, ok := NextPoolRef(pool, nil, "au-3"); !ok || got != "au-1" {
		t.Fatalf("wrap after au-3 = %q ok=%v, want au-1", got, ok)
	}
	if got, ok := NextPoolRef(pool, nil, ""); !ok || got != "au-1" {
		t.Fatalf("no active starts at head = %q ok=%v, want au-1", got, ok)
	}
}

func TestNextPoolRefSkipsSaturatedAndActive(t *testing.T) {
	pool := []string{"au-1", "au-2", "au-3"}
	if got, ok := NextPoolRef(pool, []string{"au-2"}, "au-1"); !ok || got != "au-3" {
		t.Fatalf("skipping saturated au-2 = %q ok=%v, want au-3", got, ok)
	}
	if got, ok := NextPoolRef(pool, []string{"au-1", "au-2", "au-3"}, "au-1"); ok {
		t.Fatalf("all-saturated pool must have no next, got %q", got)
	}
	if _, ok := NextPoolRef(nil, nil, ""); ok {
		t.Fatal("empty pool must have no next")
	}
}

func TestAdvanceFailoverRecordsAndSaturates(t *testing.T) {
	pool := []string{"au-1", "au-2", "au-3"}
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	st := MemberFailover{Document: Document{SchemaVersion: SchemaVersion}}
	st = AdvanceFailoverAt(st, pool, "au-1", at)
	if st.ActiveRef != "au-2" || st.Generation != 1 || st.Exhausted {
		t.Fatalf("first advance = %+v, want active au-2 gen 1", st)
	}
	if len(st.Switches) != 1 || st.Switches[0].From != "au-1" || st.Switches[0].To != "au-2" || st.Switches[0].Reason != "quota" {
		t.Fatalf("switch record wrong: %+v", st.Switches)
	}
	if !containsStr(st.Saturated, "au-1") {
		t.Fatalf("failed entry must be saturated, got %v", st.Saturated)
	}
	st = AdvanceFailoverAt(st, pool, "au-2", at)
	if st.ActiveRef != "au-3" {
		t.Fatalf("second advance = %q, want au-3 (au-1 saturated)", st.ActiveRef)
	}
}

func TestAdvanceFailoverExhaustsWhenNoneRemain(t *testing.T) {
	pool := []string{"au-1", "au-2"}
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	st := MemberFailover{Document: Document{SchemaVersion: SchemaVersion}}
	st = AdvanceFailoverAt(st, pool, "au-1", at)
	st = AdvanceFailoverAt(st, pool, st.ActiveRef, at)
	if !st.Exhausted {
		t.Fatalf("member with both entries saturated must be exhausted, got %+v", st)
	}
}

func containsStr(hay []string, needle string) bool {
	return slices.Contains(hay, needle)
}

func TestMemberFailoverPersistsAcrossRestart(t *testing.T) {
	s := failoverStore(t)
	want := MemberFailover{
		Document:  Document{SchemaVersion: SchemaVersion},
		ActiveRef: "au-2", Saturated: []string{"au-1"},
		Generation: 3, Exhausted: true,
	}
	if err := s.WriteMemberFailover("alpha", "lead", want); err != nil {
		t.Fatal(err)
	}
	// A second store over the same dir simulates a restart: nothing but disk.
	root := s.store.root
	s2, err := NewTeamSessionStoreDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveRef != want.ActiveRef || got.Generation != want.Generation || !got.Exhausted {
		t.Fatalf("restart lost failover state: %+v", got)
	}
	if len(got.Saturated) != 1 || got.Saturated[0] != "au-1" {
		t.Fatalf("saturated set lost on restart: %v", got.Saturated)
	}
}

func TestMemberFailoverAbsentIsZero(t *testing.T) {
	s := failoverStore(t)
	got, err := s.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveRef != "" || got.Generation != 0 || got.Exhausted {
		t.Fatalf("absent failover must be zero, got %+v", got)
	}
}

func TestMemberFailoverClear(t *testing.T) {
	s := failoverStore(t)
	st := MemberFailover{ActiveRef: "au-2", Generation: 2}
	if err := s.WriteMemberFailover("alpha", "lead", st); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearMemberFailover("alpha", "lead"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveRef != "" || got.Generation != 0 {
		t.Fatalf("clear must zero the state, got %+v", got)
	}
}

// TestMemberFailoverIsolation pins that each member owns an independent file:
// writing one member's failover state never touches another member's.
func TestMemberFailoverIsolation(t *testing.T) {
	s := failoverStore(t)
	if err := s.WriteMemberFailover("alpha", "lead", MemberFailover{ActiveRef: "au-2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMemberFailover("alpha", "alice", MemberFailover{ActiveRef: "au-1"}); err != nil {
		t.Fatal(err)
	}
	lead, err := s.ReadMemberFailover("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if lead.ActiveRef != "au-2" {
		t.Fatalf("lead = %q, want au-2 (isolation)", lead.ActiveRef)
	}
}

// TestMemberFailoverConcurrentWriters pins the atomic write under concurrency:
// many writers across members never tear a file and each member keeps its last
// complete value.
func TestMemberFailoverConcurrentWriters(t *testing.T) {
	s := failoverStore(t)
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			member := "lead"
			if i%2 == 0 {
				member = "alice"
			}
			st := MemberFailover{ActiveRef: fmt.Sprintf("au-%d", i), Generation: i}
			if err := s.WriteMemberFailover("alpha", member, st); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, member := range []string{"lead", "alice"} {
		got, err := s.ReadMemberFailover("alpha", member)
		if err != nil {
			t.Fatal(err)
		}
		if got.Generation == 0 && got.ActiveRef == "" {
			t.Fatalf("%s lost all writes", member)
		}
	}
}
