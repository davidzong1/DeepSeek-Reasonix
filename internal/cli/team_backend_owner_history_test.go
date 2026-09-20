package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/team"
)

// TestMemberBackendPublishesOwnerHistoryIdentity pins the assembly-time half of
// the cross-window contract: the one point a member backend is built must
// establish the owner's history identity, so a peer window has something to
// compare against. It must NOT advance the generation — assembling a member is
// not a history change, and a bump here would make every peer window reload a
// transcript that did not change.
func TestMemberBackendPublishesOwnerHistoryIdentity(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	name, err := team.MemberSessionFile("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	// A member with a transcript on disk, so assembly resumes a real history and
	// the published identity has a name to carry. The empty-stem case (a brand-new
	// legacy member, whose stamp is "" until the first save) is a store unit test.
	workspace := t.TempDir()
	seedMemberSession(t, filepath.Join(config.ProjectSessionDir(workspace), name))
	deps := memberBackendDeps{
		ctx: t.Context(), owners: owners,
		users: fakePool{users: map[string]team.AgentUser{
			"u": {UserID: "u", Provider: "openai", Model: "gpt-5.6",
				BaseURL: "https://example.invalid/v1", APIKey: "k"},
		}},
		events:        make(chan memberEvent, 1),
		workspaceRoot: workspace,
		base: func() boot.Options {
			return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard}
		},
	}
	backend, err := newMemberBackendBuilder(deps)(team.MemberBinding{
		Team: "alpha", MemberID: "lead", Leader: true, AgentUserRef: "u", SessionFile: name,
	})
	if err != nil {
		t.Fatalf("assembling the member: %v", err)
	}
	t.Cleanup(backend.Close)

	meta, err := owners.Meta(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatalf("owner meta after assembly: %v", err)
	}
	if meta.History.Generation != 0 {
		t.Fatalf("assembly must not bump the generation, got %d", meta.History.Generation)
	}
	// The builder hands back the driving port; the identity is the narrower
	// observation slice, which is what the publication path asserts for.
	stamper, ok := backend.(memberHistoryStamper)
	if !ok {
		t.Fatalf("an assembled member backend must expose its history identity, got %T", backend)
	}
	if meta.History.Stem == "" || meta.History.Stem != stamper.HistoryStamp() {
		t.Fatalf("the published stem = %q, want the backend's own stamp %q", meta.History.Stem, stamper.HistoryStamp())
	}
	// The identity a peer polls must be readable without the owner's metadata
	// lock and must agree with the document it was published in.
	fingerprint, err := owners.Fingerprint(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatalf("owner fingerprint: %v", err)
	}
	if !fingerprint.Present || fingerprint.Stem != meta.History.Stem || fingerprint.Generation != 0 {
		t.Fatalf("fingerprint = %+v, want the published identity %+v", fingerprint, meta.History)
	}
}

// TestRecordMemberOwnerHistoryReportsFailure pins the propagation: a member
// whose identity cannot be published must surface a named error rather than
// binding anyway, because a member that runs with no observable identity is
// exactly the case a peer window cannot sync with. The assertion is at the
// record call rather than through the builder: adoption writes to the same
// owner store first, so a broken store fails there and would mask this half.
func TestRecordMemberOwnerHistoryReportsFailure(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	// A regular file where the team's owner directory belongs: every owner write
	// now fails with ENOTDIR, deterministically.
	if err := os.WriteFile(filepath.Join(root, "alpha"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = recordMemberOwnerHistory(t.Context(), owners,
		team.MemberBinding{Team: "alpha", MemberID: "lead"}, staticHistoryStamp("stem"), true)
	if err == nil {
		t.Fatal("an unpublished identity must report the failure, not bind anyway")
	}
	if !strings.Contains(err.Error(), "lead") {
		t.Fatalf("the failure must name the member it belongs to, got %v", err)
	}
}

// staticHistoryStamp is a member backend's identity slice with no session
// behind it, so a publication failure can be driven without a controller.
type staticHistoryStamp string

func (s staticHistoryStamp) HistoryStamp() string { return string(s) }
