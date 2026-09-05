package model

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// validItem is the shared package-level fixture. Tester owns this namespace per
// team split; query_test.go and item tests depend on it.
func validItem(ids ...string) KnowledgeItem {
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if len(ids) > 0 && ids[0] != "" {
		id = ids[0]
	}
	return KnowledgeItem{
		ID: id, Title: "Use Go for backend",
		Kind: ItemDecision, Scope: ScopeTeam, Status: StatusLive, Version: 1,
		Provenance: []Ref{{Kind: "decision", Target: "th-1"}},
		Quality:    QualitySignal{Confidence: 0.9, ReviewLevel: ReviewNone},
		Body:       "We chose Go for the backend.",
		Canonical:  "team:decision:use-go-for-backend",
	}
}

func TestValidateClosedEnums(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*KnowledgeItem)
	}{
		{"bad-kind", func(i *KnowledgeItem) { i.Kind = "guess" }},
		{"bad-scope", func(i *KnowledgeItem) { i.Scope = "everyone" }},
		{"bad-status", func(i *KnowledgeItem) { i.Status = "active" }},
		{"bad-review", func(i *KnowledgeItem) { i.Quality.ReviewLevel = "auto" }},
		{"empty-title", func(i *KnowledgeItem) { i.Title = "" }},
		{"empty-provenance", func(i *KnowledgeItem) { i.Provenance = nil }},
		{"stale-canonical", func(i *KnowledgeItem) { i.Title = "changed" }},
		{"version-zero", func(i *KnowledgeItem) { i.Version = 0 }},
		{"id-escape", func(i *KnowledgeItem) { i.ID = "../x" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := validItem()
			tc.mut(&i)
			if err := ValidateItem(i); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestValidateLimits(t *testing.T) {
	i := validItem()
	i.Title = strings.Repeat("字", 121)
	if err := ValidateItem(i); err == nil {
		t.Fatal("expected title limit error")
	}
	i = validItem()
	i.Body = strings.Repeat("x", 8193)
	if err := ValidateItem(i); err == nil {
		t.Fatal("expected body limit error")
	}
	i = validItem()
	i.Tags = []string{strings.Repeat("t", 65)}
	if err := ValidateItem(i); err == nil {
		t.Fatal("expected tag length error")
	}
}

func TestCanonicalKeyAndSlug(t *testing.T) {
	got := CanonicalKey(ScopeTeam, ItemDecision, "Use Go: the backend 决定")
	if got != "team:decision:use-go-the-backend-决定" {
		t.Fatalf("unexpected canonical %q", got)
	}
	if CanonicalKey(ScopeTeam, ItemFact, "Go is fast") == CanonicalKey(ScopeTeam, ItemDecision, "Go is fast") {
		t.Error("kind must separate canonical keys")
	}
	if CanonicalKey(ScopeTeam, ItemFact, "Go is fast") == CanonicalKey(ScopeAgent, ItemFact, "Go is fast") {
		t.Error("scope must separate canonical keys")
	}
	if Slug("!!--a--!!") != "a" {
		t.Errorf("Slug trim/collapse = %q", Slug("!!--a--!!"))
	}
}

func TestRelIDConfinesPathSegments(t *testing.T) {
	bad := []string{"", "../x", "a/b", ".", "..", "a\\b", strings.Repeat("a", 129)}
	for _, id := range bad {
		if err := ValidateRelID(id); err == nil {
			t.Fatalf("expected %q rejected", id)
		}
	}
	if err := ValidateRelID("01ARZ3NDEKTSV4RRFFQ69G5FAV"); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
}

func TestTeamIDAllowlist(t *testing.T) {
	bad := []string{"", "a/b", "..", "Agent设计", "a b", strings.Repeat("a", 65)}
	for _, id := range bad {
		if err := ValidateTeamID(id); err == nil {
			t.Fatalf("team id %q should be rejected", id)
		}
	}
	for _, id := range []string{"team-a_1", "A1_b-9", "x", strings.Repeat("a", 64)} {
		if err := ValidateTeamID(id); err != nil {
			t.Fatalf("team id %q rejected: %v", id, err)
		}
	}
}

func TestIDSortableAndUnique(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b {
		t.Fatal("ids must differ")
	}
	if len(a) != 26 {
		t.Fatalf("ULID len = %d", len(a))
	}
	if err := ValidateRelID(a); err != nil {
		t.Fatal(err)
	}
	early := NewIDAt(time.UnixMilli(1_000))
	late := NewIDAt(time.UnixMilli(2_000))
	if !(early < late) {
		t.Fatal("ULID must order by time")
	}
}

func TestStableFingerprints(t *testing.T) {
	if ContentHash("x") != ContentHash("x") {
		t.Fatal("content hash must be stable")
	}
	if ContentHash("x") == ContentHash("y") {
		t.Fatal("content hash must change with content")
	}
	if ChunkID(1, "a") != ChunkID(1, "a") || ChunkID(1, "a") == ChunkID(2, "a") {
		t.Fatal("chunk id must be deterministic over content+order")
	}
}

func TestRetireReasonWhitelist(t *testing.T) {
	for _, r := range []RetireReason{ReasonNoLongerTrue, ReasonSupersededByTomb, ReasonPersonal} {
		if !r.Valid() {
			t.Fatalf("whitelisted reason %q must be valid", r)
		}
	}
	for _, r := range []RetireReason{"", "because", "clear_team", "expired"} {
		if r.Valid() {
			t.Fatalf("non-whitelisted reason %q must fail closed", r)
		}
	}
}

func TestItemContentHashExcludesID(t *testing.T) {
	a, b := validItem(), validItem()
	a.ID, b.ID = "AAAAAAAAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBBBBBBBB"
	if ItemContentHash(a) != ItemContentHash(b) {
		t.Fatal("dedup hash must not depend on id")
	}
	if ItemContentHash(a) == ItemContentHash(validItemWithBody(t)) {
		t.Fatal("dedup hash must change with body")
	}
}

func TestLiveAndErrInvalidWrapping(t *testing.T) {
	if !validItem().Live() {
		t.Fatal("live item should report Live()")
	}
	for _, s := range []Status{StatusDraft, StatusSuperseded, StatusDeprecated, StatusRetired} {
		i := validItem()
		i.Status = s
		if i.Live() {
			t.Fatalf("status %s must not be live", s)
		}
	}
	i := validItem()
	i.Kind = "rumor"
	err := ValidateItem(i)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid-wrapped error, got %v", err)
	}
	i = validItem()
	i.Provenance = nil
	err = ValidateItem(i)
	if err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("provenance error should be plain, got %v", err)
	}
}

func validItemWithBody(t *testing.T) KnowledgeItem {
	t.Helper()
	i := validItem()
	i.Body += " extra words"
	return i
}
