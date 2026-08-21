package team

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".reasonix", "team", TeamsFile)
	want := TeamDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		Teams: []Team{{
			Name: "demo",
			Template: []MemberSlot{{
				MemberID: "alice",
				Role:     RoleCoder,
				Status:   MemberStatusActive,
			}},
			DefaultAgentUserRef: "au-1",
		}},
	}
	if err := store.Save(path, &want); err != nil {
		t.Fatal(err)
	}
	var got TeamDoc
	if err := store.Load(path, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestStoreSaveRejectsMissingSchemaVersion(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".reasonix", "team", TeamsFile)
	err = store.Save(path, &TeamDoc{})
	if !errors.Is(err, ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
	if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
		t.Fatal("file published despite rejected Save")
	}
}

func TestStoreSaveRejectsWrongSchemaVersion(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".reasonix", "team", TeamsFile)
	err = store.Save(path, &TeamDoc{Document: Document{SchemaVersion: 2}})
	if !errors.Is(err, ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
	if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
		t.Fatal("file published despite rejected Save")
	}
}

func TestStoreRejectsWrongSchemaVersion(t *testing.T) {
	store := newTestStore(t, `{"schema_version":2,"teams":[]}`)
	var doc TeamDoc
	err := store.Load(filepath.Join(".reasonix", "team", TeamsFile), &doc)
	if !errors.Is(err, ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
}

func TestStoreRejectsMissingSchemaVersion(t *testing.T) {
	store := newTestStore(t, `{"teams":[]}`)
	var doc TeamDoc
	err := store.Load(filepath.Join(".reasonix", "team", TeamsFile), &doc)
	if !errors.Is(err, ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
}

func TestStoreRejectsCorruptFile(t *testing.T) {
	cases := []string{
		`not json`,
		`{"schema_version":1,"teams":[`,
		`{"schema_version":"one","teams":[]}`,
	}
	for _, data := range cases {
		store := newTestStore(t, data)
		var doc TeamDoc
		err := store.Load(filepath.Join(".reasonix", "team", TeamsFile), &doc)
		var ce *CorruptFileError
		if !errors.As(err, &ce) {
			t.Errorf("data %q: err = %v, want *CorruptFileError", data, err)
		}
	}
}

func TestStoreSaveRejectsUnsafePath(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("../escape.json", &TeamDoc{}); err == nil {
		t.Fatal("Save escaped the project root without error")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.json")); !os.IsNotExist(err) {
		t.Fatal("escape file exists after rejected Save")
	}
}

func TestTeamRoot(t *testing.T) {
	root := t.TempDir()
	got, err := TeamRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".reasonix", "team"); got != want {
		t.Fatalf("TeamRoot = %q, want %q", got, want)
	}
}

// newTestStore returns a FileStore whose teams.json already contains data,
// so tests can provoke load-time failures without going through Save.
func newTestStore(t *testing.T, data string) *FileStore {
	t.Helper()
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".reasonix", "team", TeamsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return store
}
