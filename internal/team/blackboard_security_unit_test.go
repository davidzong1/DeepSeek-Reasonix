package team

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Unit tests for the service-layer security policy (blackboard_security.go).
// The store-level integration tests live in blackboard_security_test.go.

func TestValidBoardID(t *testing.T) {
	valid := []string{BoardShared, "private/alice", "private/member-1"}
	for _, id := range valid {
		if !ValidBoardID(id) {
			t.Errorf("ValidBoardID(%q) = false, want true", id)
		}
	}
	invalid := []string{"", "private/", "private/a/b", "private/..", "private/.", "..", "/shared", "shared/x"}
	for _, id := range invalid {
		if ValidBoardID(id) {
			t.Errorf("ValidBoardID(%q) = true, want false", id)
		}
	}
}

func TestCheckBoardAccess(t *testing.T) {
	alice := Identity{MemberID: "alice", Role: "tester", Generation: 1}
	shared := BoardShared
	if err := CheckBoardAccess(shared, alice, false); err != nil {
		t.Errorf("shared read: %v", err)
	}
	if err := CheckBoardAccess(shared, alice, true); err != nil {
		t.Errorf("shared write: %v", err)
	}
	if err := CheckBoardAccess("private/alice", alice, true); err != nil {
		t.Errorf("private owner write: %v", err)
	}
	if err := CheckBoardAccess("private/alice", alice, false); err != nil {
		t.Errorf("private owner read: %v", err)
	}
	if err := CheckBoardAccess("private/bob", alice, false); !errors.Is(err, ErrForbidden) {
		t.Errorf("cross-member read = %v, want ErrForbidden", err)
	}
	if err := CheckBoardAccess("private/bob", alice, true); !errors.Is(err, ErrForbidden) {
		t.Errorf("cross-member write = %v, want ErrForbidden", err)
	}
	if err := CheckBoardAccess(shared, Identity{}, false); !errors.Is(err, ErrForbidden) {
		t.Errorf("empty identity = %v, want ErrForbidden", err)
	}
	if err := CheckBoardAccess("private/", alice, false); !errors.Is(err, ErrForbidden) {
		t.Errorf("malformed board = %v, want ErrForbidden", err)
	}
}

func TestRequireManagement(t *testing.T) {
	leader := Identity{MemberID: "zwc", Role: "leader", Generation: 3}
	if err := RequireManagement(leader); err != nil {
		t.Errorf("leader management: %v", err)
	}
	member := Identity{MemberID: "alice", Role: "tester", Generation: 3}
	if err := RequireManagement(member); !errors.Is(err, ErrForbidden) {
		t.Errorf("member management = %v, want ErrForbidden", err)
	}
	if err := RequireManagement(Identity{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("empty identity management = %v, want ErrForbidden", err)
	}
}

func TestStamp(t *testing.T) {
	bind := BindRecord{
		MemberID:   "alice",
		LeaderID:   "zwc",
		Generation: 2,
		Status:     BindStatusBound,
		TaskID:     TaskID("t-1"),
		BoundAt:    time.Now(),
	}
	got, forged := Stamp(bind, Identity{MemberID: "alice", Generation: 2}, "tester", "claude")
	if forged {
		t.Error("honest claim flagged as forged")
	}
	if got.MemberID != "alice" || got.Role != "tester" || got.Agent != "claude" || got.Generation != 2 {
		t.Errorf("stamped identity = %+v", got)
	}

	if _, forged := Stamp(bind, Identity{MemberID: "mallory", Generation: 2}, "tester", "claude"); !forged {
		t.Error("forged member id not flagged")
	}
	if _, forged := Stamp(bind, Identity{MemberID: "alice", Generation: 99}, "tester", "claude"); !forged {
		t.Error("forged generation not flagged")
	}
}

func TestRedact(t *testing.T) {
	r := NewRedactor(nil)
	cases := []struct {
		in   string
		want string
		hits int
	}{
		{"key sk-abcdefghijklmnop used", "key [REDACTED:provider-key] used", 1},
		{"aws AKIA0123456789ABCDEF ok", "aws [REDACTED:aws-key] ok", 1},
		{"-----BEGIN RSA PRIVATE KEY-----", "[REDACTED:private-key]", 1},
		{"password=hunter2", "[REDACTED:credential]", 1},
		{"plain text, no secrets", "plain text, no secrets", 0},
		{"token: xyz AND sk-abcdefghijklmnop", "[REDACTED:credential] AND [REDACTED:provider-key]", 2},
	}
	for _, c := range cases {
		got, hits := r.Redact(c.in)
		if got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
		if len(hits) != c.hits {
			t.Errorf("Redact(%q) hits = %d, want %d", c.in, len(hits), c.hits)
		}
		if strings.Contains(got, "sk-") || strings.Contains(got, "hunter2") {
			t.Errorf("Redact(%q) leaked the secret into %q", c.in, got)
		}
	}
}

// TestRedactThreeBoundaries runs the same redactor at the persist, inject
// and summarize boundaries (route §6.3): the later passes see markers,
// never the secret, and return no new hits.
func TestRedactThreeBoundaries(t *testing.T) {
	r := NewRedactor(nil)
	payload := "api_key=hunter2 and AKIA0123456789ABCDEF"
	persist, hits1 := r.Redact(payload)
	inject, hits2 := r.Redact(persist)
	summarize, hits3 := r.Redact(inject)
	if len(hits1) != 2 || len(hits2) != 0 || len(hits3) != 0 {
		t.Errorf("boundary hits = %d,%d,%d, want 2,0,0", len(hits1), len(hits2), len(hits3))
	}
	if strings.Contains(summarize, "hunter2") || strings.Contains(summarize, "AKIA") {
		t.Errorf("secret crossed a boundary into %q", summarize)
	}
}

func TestRedactMarkerNeverMatches(t *testing.T) {
	r := NewRedactor(nil)
	marker := "[REDACTED:credential] [REDACTED:provider-key]"
	out, hits := r.Redact(marker)
	if out != marker || len(hits) != 0 {
		t.Errorf("markers re-matched: out=%q hits=%d", out, len(hits))
	}
}

func TestRedactCustomPatterns(t *testing.T) {
	pat := regexp.MustCompile("ghp_[A-Za-z0-9]{16,}")
	r := NewRedactor([]RedactPattern{{Kind: "github-key", Regexp: pat}})
	got, hits := r.Redact("ghp_ABCDEFGHIJKLMNOPQRST")
	if got != "[REDACTED:github-key]" || len(hits) != 1 {
		t.Errorf("custom pattern: out=%q hits=%d", got, len(hits))
	}
}
