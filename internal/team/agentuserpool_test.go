package team

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// poolAgentUsers seeds the team store's sibling agent-users registry with the
// given ids, mirroring newTeamStore's shared-root wiring.
func poolAgentUsers(t *testing.T, ts *TeamStore, root string, ids ...string) {
	t.Helper()
	au, err := NewAgentUsersStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := au.AddAgentUser(AgentUser{UserID: id, Provider: "anthropic", Model: "claude-opus-5"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTeamEffectivePoolReadShapes(t *testing.T) {
	pool := Team{Name: "p", AgentUserPool: []string{"au-1", "au-2"}}
	if got := pool.EffectivePool(); !reflect.DeepEqual(got, []string{"au-1", "au-2"}) {
		t.Fatalf("explicit pool effective = %v", got)
	}
	if got := pool.DefaultRef(); got != "au-1" {
		t.Fatalf("explicit pool head = %q, want au-1", got)
	}
	legacy := Team{Name: "l", DefaultAgentUserRef: "legacy-1"}
	if got := legacy.EffectivePool(); !reflect.DeepEqual(got, []string{"legacy-1"}) {
		t.Fatalf("legacy default effective = %v", got)
	}
	if got := legacy.DefaultRef(); got != "legacy-1" {
		t.Fatalf("legacy default ref = %q", got)
	}
	none := Team{Name: "n"}
	if got := none.EffectivePool(); got != nil {
		t.Fatalf("unconfigured effective = %v, want nil", got)
	}
	if got := none.DefaultRef(); got != "" {
		t.Fatalf("unconfigured default ref = %q", got)
	}
}

func TestTeamStoreSetTeamAgentUserPool(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2")

	// Order is preserved, duplicates collapse, and the legacy default clears
	// once the pool is written — the fold happens only on this explicit write.
	if err := ts.SetTeamAgentUserPool("alpha", []string{"au-2", "au-1", " au-2 ", ""}); err != nil {
		t.Fatal(err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Teams[0].AgentUserPool; !reflect.DeepEqual(got, []string{"au-2", "au-1"}) {
		t.Fatalf("pool = %v, want [au-2 au-1]", got)
	}
	if got := doc.Teams[0].DefaultAgentUserRef; got != "" {
		t.Fatalf("legacy default not folded away, got %q", got)
	}

	// Unknown references are refused before any write.
	if err := ts.SetTeamAgentUserPool("alpha", []string{"au-1", "ghost"}); !errors.Is(err, ErrAgentUserNotFound) {
		t.Fatalf("unknown ref err = %v, want ErrAgentUserNotFound", err)
	}
	if err := ts.SetTeamAgentUserPool("ghost", []string{"au-1"}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team err = %v, want ErrTeamNotFound", err)
	}

	// An empty pool clears the default entirely (the session gate state).
	if err := ts.SetTeamAgentUserPool("alpha", nil); err != nil {
		t.Fatal(err)
	}
	doc, _, _ = ts.Load()
	if got := doc.Teams[0].AgentUserPool; len(got) != 0 {
		t.Fatalf("empty pool did not clear, got %v", got)
	}
	if eff, _ := ts.EffectiveTeamPool("alpha"); eff != nil {
		t.Fatalf("cleared team effective pool = %v, want nil", eff)
	}
}

func TestTeamStoreSetTeamAgentUserPoolAtomicReplace(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2", "au-3")
	a := []string{"au-1", "au-2"}
	b := []string{"au-3"}
	for _, set := range [][]string{a, b} {
		if err := ts.SetTeamAgentUserPool("alpha", set); err != nil {
			t.Fatal(err)
		}
	}
	// Each call publishes a full ordered pool under the CAS loop, so a concurrent
	// writer can never interleave into a mix: the survivor is one writer's pool.
	done := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			var err error
			for range 25 {
				set := b
				if i == 0 {
					set = a
				}
				if err = ts.SetTeamAgentUserPool("alpha", set); err != nil {
					break
				}
			}
			done <- err
		}(i)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent set = %v", err)
		}
	}
	eff, err := ts.EffectiveTeamPool("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eff, a) && !reflect.DeepEqual(eff, b) {
		t.Fatalf("concurrent writers produced a torn pool %v, want one writer's pool", eff)
	}
}

func TestTeamStoreEffectiveTeamPoolReadDoesNotRewrite(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamsLegacyFile,
		`{"schema_version":1,"teams":[{"Name":"old","Template":[{"MemberID":"m1","Role":"coder","Status":"active"}],"DefaultAgentUserRef":"au-1"}]}`)

	eff, err := ts.EffectiveTeamPool("old")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eff, []string{"au-1"}) {
		t.Fatalf("legacy effective pool = %v, want [au-1]", eff)
	}
	if _, err := os.Stat(teamFile(t, root)); !os.IsNotExist(err) {
		t.Fatal("a read-only effective-pool call must not publish team.json")
	}
	// Bindings over the same legacy team resolve the default reference.
	got, err := ts.Bindings("old")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AgentUserRef != "au-1" {
		t.Fatalf("legacy bindings = %+v", got)
	}
	if _, err := os.Stat(teamFile(t, root)); !os.IsNotExist(err) {
		t.Fatal("Bindings must not publish team.json either")
	}
	if _, err := ts.EffectiveTeamPool("ghost"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team err = %v", err)
	}
}

func TestTeamBindingsResolvePoolHeadForUnboundMember(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2")
	if err := ts.SetTeamAgentUserPool("alpha", []string{"au-2", "au-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := ts.Bindings("alpha")
	if err != nil {
		t.Fatal(err)
	}
	// m1 has no override, so it inherits the pool head au-2.
	if len(got) != 1 || got[0].AgentUserRef != "au-2" {
		t.Fatalf("bindings = %+v, want m1 -> au-2", got)
	}
}

func TestTeamStoreDeletePooledAgentUserRefused(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2")
	if err := ts.SetTeamAgentUserPool("alpha", []string{"au-2"}); err != nil {
		t.Fatal(err)
	}
	// au-2 is referenced only by the pool — deleting it would orphan the pool.
	if err := ts.DeleteAgentUser("au-2"); !errors.Is(err, ErrAgentUserInUse) {
		t.Fatalf("delete pooled entry err = %v, want ErrAgentUserInUse", err)
	}
	// au-1 is referenced by nothing (the legacy default was folded away), so it
	// stays deletable.
	if err := ts.DeleteAgentUser("au-1"); err != nil {
		t.Fatalf("delete unreferenced entry = %v", err)
	}
}

func TestTeamStoreSetTeamAgentUserPoolErrorNamesRef(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1")
	err := ts.SetTeamAgentUserPool("alpha", []string{"au-1", "ghost"})
	if !errors.Is(err, ErrAgentUserNotFound) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("refusal should name the missing ref, got %v", err)
	}
}

func TestMemberSlotPoolReadShapes(t *testing.T) {
	teamPool := Team{Name: "alpha", AgentUserPool: []string{"au-1", "au-2"}}
	inheriting := MemberSlot{MemberID: "m1", Status: MemberStatusActive}
	if got := SlotEffectivePool(inheriting, teamPool); !reflect.DeepEqual(got, []string{"au-1", "au-2"}) {
		t.Fatalf("inheriting pool = %v", got)
	}
	if got := SlotNominal(inheriting, teamPool); got != "au-1" {
		t.Fatalf("inheriting nominal = %q, want au-1", got)
	}
	pinned := MemberSlot{MemberID: "m2", AgentUserRef: "au-2"}
	if got := SlotEffectivePool(pinned, teamPool); !reflect.DeepEqual(got, []string{"au-1", "au-2"}) {
		t.Fatalf("pinned pool must stay the team's (failover walks it), got %v", got)
	}
	if got := SlotNominal(pinned, teamPool); got != "au-2" {
		t.Fatalf("pinned nominal = %q, want au-2", got)
	}
	custom := MemberSlot{MemberID: "m3", PoolMode: MemberPoolCustom, AgentUserPool: []string{"au-3", "au-1"}}
	if got := SlotEffectivePool(custom, teamPool); !reflect.DeepEqual(got, []string{"au-3", "au-1"}) {
		t.Fatalf("custom pool = %v", got)
	}
	if got := SlotNominal(custom, teamPool); got != "au-3" {
		t.Fatalf("custom nominal = %q, want its own head au-3", got)
	}
	// An empty custom pool is unreachable through the write path; a hand-edited
	// document reads defensively as the team pool, mode still custom on disk.
	emptyCustom := MemberSlot{MemberID: "m4", PoolMode: MemberPoolCustom}
	if got := SlotEffectivePool(emptyCustom, teamPool); !reflect.DeepEqual(got, []string{"au-1", "au-2"}) {
		t.Fatalf("empty custom must fall back to the team pool, got %v", got)
	}
	if got := SlotNominal(emptyCustom, teamPool); got != "au-1" {
		t.Fatalf("empty custom nominal = %q, want team head", got)
	}
	if got := SlotEffectivePool(inheriting, Team{Name: "none"}); got != nil {
		t.Fatalf("both-empty pool = %v, want nil", got)
	}
	if got := SlotNominal(inheriting, Team{Name: "none"}); got != "" {
		t.Fatalf("both-empty nominal = %q", got)
	}
}

// TestTeamStoreSetMemberPoolPinsTheWriteContract drives the SetMemberPool table:
// custom writes dedupe keeping first order and refuse empty or unknown refs;
// inherit demands an empty pool but keeps the entries field for a lossless round
// trip; a legacy pin refuses a custom write; an unknown mode is refused.
func TestTeamStoreSetMemberPoolPinsTheWriteContract(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2", "au-3")

	if err := ts.SetMemberPool("alpha", "m1", MemberPoolCustom, []string{"au-2", "au-1", " au-2 ", ""}); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberPool("alpha", "m1", "", nil); err != nil { // back to inherit
		t.Fatal(err)
	}
	slot := mustSlot(t, ts, "alpha", "m1")
	if slot.PoolMode != "" {
		t.Fatalf("inherit write must canonicalize the mode to empty, got %q", slot.PoolMode)
	}
	if !reflect.DeepEqual(slot.AgentUserPool, []string{"au-2", "au-1"}) {
		t.Fatalf("inherit write must keep the entries for a lossless round trip, got %v", slot.AgentUserPool)
	}
	if err := ts.SetMemberPool("alpha", "m1", MemberPoolCustom, []string{"au-1"}); err != nil {
		t.Fatal(err)
	}
	slot = mustSlot(t, ts, "alpha", "m1")
	if !slot.IsCustomPool() || !reflect.DeepEqual(slot.AgentUserPool, []string{"au-1"}) {
		t.Fatalf("re-switch to custom must restore the kept entries, slot %+v", slot)
	}

	for _, tc := range []struct {
		name string
		mode string
		pool []string
		want error
	}{
		{"custom empty refused", MemberPoolCustom, nil, ErrMemberPoolEmpty},
		{"custom unknown ref named", MemberPoolCustom, []string{"au-1", "ghost"}, ErrAgentUserNotFound},
		{"inherit with pool refused", "", []string{"au-1"}, ErrMemberPoolNonEmpty},
		{"unknown mode refused", "banana", nil, ErrInvalidMemberPool},
	} {
		err := ts.SetMemberPool("alpha", "m1", tc.mode, tc.pool)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := ts.SetMemberPool("alpha", "m1", MemberPoolCustom, []string{"au-1", "ghost"}); !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown ref refusal should name the ref, got %v", err)
	}
	slot = mustSlot(t, ts, "alpha", "m1") // every refusal above left the slot intact
	if !slot.IsCustomPool() || !reflect.DeepEqual(slot.AgentUserPool, []string{"au-1"}) {
		t.Fatalf("refusals must leave zero partial writes, slot %+v", slot)
	}
}

// TestTeamStoreSetMemberPoolPinRefused pins the legacy-pin conflict: a custom
// write onto a pinned slot is refused and nothing is written — the roster g
// reset folds the pin first, so custom and pin never coexist.
func TestTeamStoreSetMemberPoolPinRefused(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2")
	if err := ts.BindAgentUser("alpha", "m1", "au-1"); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberPool("alpha", "m1", MemberPoolCustom, []string{"au-2"}); !errors.Is(err, ErrMemberPoolPin) {
		t.Fatalf("custom write on a pinned slot err = %v, want ErrMemberPoolPin", err)
	}
	slot := mustSlot(t, ts, "alpha", "m1")
	if slot.IsCustomPool() || slot.AgentUserRef != "au-1" {
		t.Fatalf("the refusal must leave pin and mode untouched, slot %+v", slot)
	}
}

// TestTeamStoreBindAgentUserRefusedByCustomPool pins the mirror of the pin
// refusal: a custom pool already binds its own head, so a BindAgentUser on the
// slot is an inert pin write refused up front — and the same slot binds again
// once the pool row returns to inherit (the retained entries stay inert).
func TestTeamStoreBindAgentUserRefusedByCustomPool(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2")
	if err := ts.SetMemberPool("alpha", "m1", MemberPoolCustom, []string{"au-2", "au-1"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.BindAgentUser("alpha", "m1", "au-1"); !errors.Is(err, ErrMemberPoolBind) {
		t.Fatalf("bind on a custom slot err = %v, want ErrMemberPoolBind", err)
	}
	if slot := mustSlot(t, ts, "alpha", "m1"); !slot.IsCustomPool() || slot.AgentUserRef != "" || !reflect.DeepEqual(slot.AgentUserPool, []string{"au-2", "au-1"}) {
		t.Fatalf("the refusal must leave the custom pool untouched, slot %+v", slot)
	}
	if err := ts.SetMemberPool("alpha", "m1", "", nil); err != nil { // back to inherit
		t.Fatal(err)
	}
	if err := ts.BindAgentUser("alpha", "m1", "au-1"); err != nil {
		t.Fatalf("binding after the inherit round trip must not regress: %v", err)
	}
	if slot := mustSlot(t, ts, "alpha", "m1"); slot.PoolMode != "" || slot.AgentUserRef != "au-1" {
		t.Fatalf("round-tripped bind slot = %+v, want inherit mode + pin au-1", slot)
	}
}

// TestMemberPoolReadFoldsThreeBranches drives the store read through the same
// fold Bindings uses: custom walks its own pool, a pinned member keeps the pin
// as nominal while the pool stays the team's, an unbound member inherits the
// team head.
func TestMemberPoolReadFoldsThreeBranches(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2", "au-3")
	addTestMembers(t, ts, "alpha", "m2", "m3")
	if err := ts.SetTeamAgentUserPool("alpha", []string{"au-1", "au-2"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.BindAgentUser("alpha", "m2", "au-3"); err != nil { // pool-external pin
		t.Fatal(err)
	}
	if err := ts.SetMemberPool("alpha", "m3", MemberPoolCustom, []string{"au-3", "au-2"}); err != nil {
		t.Fatal(err)
	}

	pool, nominal, err := ts.MemberPool("alpha", "m1")
	if err != nil || !reflect.DeepEqual(pool, []string{"au-1", "au-2"}) || nominal != "au-1" {
		t.Fatalf("unbound member pool = %v/%q err %v, want team pool + head", pool, nominal, err)
	}
	pool, nominal, err = ts.MemberPool("alpha", "m2")
	if err != nil || !reflect.DeepEqual(pool, []string{"au-1", "au-2"}) || nominal != "au-3" {
		t.Fatalf("pinned member pool = %v/%q err %v, want team pool + pin", pool, nominal, err)
	}
	pool, nominal, err = ts.MemberPool("alpha", "m3")
	if err != nil || !reflect.DeepEqual(pool, []string{"au-3", "au-2"}) || nominal != "au-3" {
		t.Fatalf("custom member pool = %v/%q err %v, want its own pool + head", pool, nominal, err)
	}
	if _, _, err := ts.MemberPool("alpha", "nobody"); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("unknown member err = %v", err)
	}
	if _, _, err := ts.MemberPool("ghost", "m1"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("unknown team err = %v", err)
	}
}

// TestTeamBindingsResolveCustomMemberPins the Bindings fold over a custom slot:
// the binding's AgentUserRef is the custom head (even when it differs from the
// team pool) and Pool carries the custom entries for the failover walk.
func TestTeamBindingsResolveCustomMember(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2", "au-3")
	if err := ts.SetTeamAgentUserPool("alpha", []string{"au-1", "au-2"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberPool("alpha", "m1", MemberPoolCustom, []string{"au-3", "au-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := ts.Bindings("alpha")
	if err != nil {
		t.Fatal(err)
	}
	b := got[0]
	if b.AgentUserRef != "au-3" {
		t.Fatalf("custom binding ref = %q, want its own head au-3", b.AgentUserRef)
	}
	if !reflect.DeepEqual(b.Pool, []string{"au-3", "au-1"}) {
		t.Fatalf("custom binding pool = %v", b.Pool)
	}
}

// TestTeamStoreDeleteAgentUserRefusedByCustomPool pins the in-use scan over
// member custom pools: an entry referenced anywhere in a custom pool — a
// non-head entry included — refuses deletion.
func TestTeamStoreDeleteAgentUserRefusedByCustomPool(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2", "au-3")
	if err := ts.SetMemberPool("alpha", "m1", MemberPoolCustom, []string{"au-1", "au-2"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"au-1", "au-2"} {
		if err := ts.DeleteAgentUser(id); !errors.Is(err, ErrAgentUserInUse) {
			t.Fatalf("delete %s referenced by a custom pool err = %v, want ErrAgentUserInUse", id, err)
		}
	}
	if err := ts.DeleteAgentUser("au-3"); err != nil {
		t.Fatalf("delete unreferenced entry = %v", err)
	}
}

// TestCloneDocDeepCopiesMemberCustomPool pins the CAS-aliasing trap: a mutator
// appending through the working doc's member custom pool must never reach the
// expected version cloneDoc produced.
func TestCloneDocDeepCopiesMemberCustomPool(t *testing.T) {
	doc := TeamDoc{Teams: []Team{{Name: "alpha", Template: []MemberSlot{
		{MemberID: "m1", PoolMode: MemberPoolCustom, AgentUserPool: []string{"au-1"}},
	}}}}
	cp := cloneDoc(doc)
	cp.Teams[0].Template[0].AgentUserPool = append(cp.Teams[0].Template[0].AgentUserPool, "au-2")
	if !reflect.DeepEqual(doc.Teams[0].Template[0].AgentUserPool, []string{"au-1"}) {
		t.Fatalf("cloneDoc must deep-copy member custom pools, got %v", doc.Teams[0].Template[0].AgentUserPool)
	}
}

// TestMemberPoolConcurrentWriters runs racing SetMemberPool writers on two
// members of one team under -race: every write lands whole, no torn pool.
//
// Every writer's error is asserted, not discarded: a dropped write is exactly
// what this test exists to catch, and ignoring the error let it fail only when
// a second assertion happened to notice months later.
func TestMemberPoolConcurrentWriters(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2", "au-3")
	addTestMembers(t, ts, "alpha", "m2")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for g := range 16 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			member := "m1"
			if g%2 == 1 {
				member = "m2"
			}
			pool := []string{"au-1", "au-2", "au-3"}
			if g%3 == 0 {
				pool = []string{"au-2", "au-1"}
			}
			if err := ts.SetMemberPool("alpha", member, MemberPoolCustom, pool); err != nil {
				errs <- err
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a racing writer was refused: %v", err)
	}
	for _, id := range []string{"m1", "m2"} {
		slot := mustSlot(t, ts, "alpha", id)
		if slot.PoolMode != MemberPoolCustom {
			t.Fatalf("member %s mode = %q after racing writers", id, slot.PoolMode)
		}
		if len(slot.AgentUserPool) < 2 {
			t.Fatalf("member %s pool = %v after racing writers, want a whole write", id, slot.AgentUserPool)
		}
	}
}

// addTestMembers appends coder-role member slots to the named team.
func addTestMembers(t *testing.T, ts *TeamStore, teamName string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := ts.AddMember(teamName, MemberSlot{MemberID: id, Role: RoleCoder, Status: MemberStatusActive}); err != nil {
			t.Fatal(err)
		}
	}
}

// mustSlot returns the named member's slot, failing the test on any miss.
func mustSlot(t *testing.T, ts *TeamStore, teamName, memberID string) MemberSlot {
	t.Helper()
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i := range doc.Teams {
		if doc.Teams[i].Name != teamName {
			continue
		}
		for _, slot := range doc.Teams[i].Template {
			if slot.MemberID == memberID {
				return slot
			}
		}
		t.Fatalf("member %q not found in team %q", memberID, teamName)
	}
	t.Fatalf("team %q not found", teamName)
	return MemberSlot{}
}

// TestMemberPoolReadSurvivesReopen pins restart recovery at the store seam: a
// fresh store over the same data dir reads the custom mode, order, and nominal
// back exactly as written.
func TestMemberPoolReadSurvivesReopen(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	poolAgentUsers(t, ts, root, "au-1", "au-2")
	if err := ts.SetMemberPool("alpha", "m1", MemberPoolCustom, []string{"au-2", "au-1"}); err != nil {
		t.Fatal(err)
	}
	ts2, err := NewTeamStoreAt(root, "")
	if err != nil {
		t.Fatal(err)
	}
	pool, nominal, err := ts2.MemberPool("alpha", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pool, []string{"au-2", "au-1"}) || nominal != "au-2" {
		t.Fatalf("after reopen pool = %v nominal %q, want [au-2 au-1] / au-2", pool, nominal)
	}
}

// TestMemberPoolLegacyPinDocReadsWithoutWrite pins old-document compatibility:
// a pre-P3 member with a legacy pin but no pool fields reads as inherit + pin
// (team pool for failover, pin as nominal), and every read leaves the legacy
// file byte-identical — no migration write happens.
func TestMemberPoolLegacyPinDocReadsWithoutWrite(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamsLegacyFile,
		`{"schema_version":1,"teams":[{"Name":"alpha","Template":[{"MemberID":"m1","Role":"coder","Status":"active","AgentUserRef":"au-1"}],"agent_user_pool":["au-1","au-2"]}]}`)
	pool, nominal, err := ts.MemberPool("alpha", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pool, []string{"au-1", "au-2"}) || nominal != "au-1" {
		t.Fatalf("legacy pin member pool = %v nominal %q, want team pool + pin", pool, nominal)
	}
	got, err := ts.Bindings("alpha")
	if err != nil || got[0].AgentUserRef != "au-1" {
		t.Fatalf("legacy bindings = %+v err %v", got, err)
	}
	if _, err := os.Stat(teamFile(t, root)); !os.IsNotExist(err) {
		t.Fatal("a read of a legacy doc must not publish team.json")
	}
}
