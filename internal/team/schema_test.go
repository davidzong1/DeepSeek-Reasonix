package team

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// TestSchemaDocsRoundTrip exercises every v1 schema document through the
// atomic store: save, load, and the schema_version header survive the wire.
func TestSchemaDocsRoundTrip(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want any
	}{
		{
			path: filepath.Join(".reasonix", "team", TeamsFile),
			want: &TeamDoc{Document: Document{SchemaVersion: SchemaVersion}, Teams: []Team{{
				Name:     "demo",
				Template: []MemberSlot{{MemberID: "alice", Role: RoleCoder, Status: MemberStatusActive}},
			}}},
		},
		{
			path: filepath.Join(".reasonix", "team", AgentUsersFile),
			want: &AgentUsersDoc{Document: Document{SchemaVersion: SchemaVersion}, AgentUsers: []AgentUser{{
				UserID:    "au-1",
				Provider:  "deepseek",
				SecretRef: SecretRef{StoreID: "store-entry-1", Scope: CredentialScopeAgentUser},
			}}},
		},
		{
			path: filepath.Join(".reasonix", "team", MemoryFile),
			want: &MemoryDoc{Document: Document{SchemaVersion: SchemaVersion}, Entries: []MemoryEntry{{
				Layer:   MemoryLayerTeam,
				Content: "decision: keep the chokepoint",
			}}},
		},
		{
			path: filepath.Join(".reasonix", "team", "blackboard", "rev-1.json"),
			want: &BlackboardDoc{Document: Document{SchemaVersion: SchemaVersion}, Entry: BlackboardEntry{
				Rev:        1,
				Kind:       "decision",
				ContentRef: "ctx/20260821",
				Author:     "alice",
			}},
		},
	}
	for _, tc := range cases {
		if err := store.Save(tc.path, tc.want); err != nil {
			t.Fatalf("Save %s: %v", tc.path, err)
		}
		got := reflect.New(reflect.TypeOf(tc.want).Elem()).Interface()
		if err := store.Load(tc.path, got); err != nil {
			t.Fatalf("Load %s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s round-trip mismatch:\n got %+v\nwant %+v", tc.path, got, tc.want)
		}
	}
}

// TestSchemaDocsRejectMissingVersion pins the schema_version=1 constraint on
// every v1 document type: a document without the header is refused by Save.
func TestSchemaDocsRejectMissingVersion(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		doc  any
	}{
		{filepath.Join(".reasonix", "team", TeamsFile), &TeamDoc{}},
		{filepath.Join(".reasonix", "team", AgentUsersFile), &AgentUsersDoc{}},
		{filepath.Join(".reasonix", "team", MemoryFile), &MemoryDoc{}},
		{filepath.Join(".reasonix", "team", "blackboard", "rev-1.json"), &BlackboardDoc{}},
	}
	for _, tc := range cases {
		if err := store.Save(tc.path, tc.doc); !errors.Is(err, ErrSchemaVersion) {
			t.Fatalf("%T missing version: err = %v, want ErrSchemaVersion", tc.doc, err)
		}
	}
}

// TestBlackboardRevFile pins the rev-N.json naming and its boundary: the
// revision file is project-relative and rev < 1 is refused.
func TestBlackboardRevFile(t *testing.T) {
	for rev, want := range map[int]string{
		1:  filepath.Join("blackboard", "rev-1.json"),
		42: filepath.Join("blackboard", "rev-42.json"),
	} {
		got, err := BlackboardRevFile(rev)
		if err != nil {
			t.Fatalf("rev %d: %v", rev, err)
		}
		if got != want {
			t.Fatalf("rev %d: got %q, want %q", rev, got, want)
		}
	}
	for _, rev := range []int{0, -1, -42} {
		if _, err := BlackboardRevFile(rev); err == nil {
			t.Fatalf("rev %d: expected error", rev)
		}
	}
}

// TestBlackboardRevFileStaysInTeamDir guards the layering boundary: the
// rev-N path must never escape .reasonix/team no matter the revision number.
func TestBlackboardRevFileStaysInTeamDir(t *testing.T) {
	root := t.TempDir()
	teamDir, err := TeamRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	revPath, err := BlackboardRevFile(1)
	if err != nil {
		t.Fatal(err)
	}
	full, err := safePath(root, revPath)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(teamDir, full)
	if err != nil {
		t.Fatal(err)
	}
	if rel == ".." || filepath.IsAbs(rel) {
		t.Fatalf("rev file escapes team dir: %q", full)
	}
}
