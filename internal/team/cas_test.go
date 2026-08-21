package team

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func teamPath() string {
	return filepath.Join(".reasonix", "team", TeamsFile)
}

func teamDoc(names ...string) *TeamDoc {
	doc := &TeamDoc{Document: Document{SchemaVersion: SchemaVersion}}
	for _, n := range names {
		doc.Teams = append(doc.Teams, Team{Name: n})
	}
	return doc
}

func loadDoc(t *testing.T, store *FileStore) *TeamDoc {
	t.Helper()
	var doc TeamDoc
	if err := store.Load(teamPath(), &doc); err != nil {
		t.Fatal(err)
	}
	return &doc
}

func TestCompareAndSwapSuccess(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Save(teamPath(), teamDoc("a")); err != nil {
		t.Fatal(err)
	}
	cur := loadDoc(t, store)
	next := teamDoc("a", "b")
	if err := store.CompareAndSwap(teamPath(), cur, next); err != nil {
		t.Fatal(err)
	}
	if got := loadDoc(t, store); !reflect.DeepEqual(got, next) {
		t.Fatalf("doc = %+v, want %+v", got, next)
	}
}

func TestCompareAndSwapStaleExpectedConflicts(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Save(teamPath(), teamDoc("a")); err != nil {
		t.Fatal(err)
	}
	cur := loadDoc(t, store)
	if err := store.CompareAndSwap(teamPath(), cur, teamDoc("a", "b")); err != nil {
		t.Fatal(err)
	}
	err := store.CompareAndSwap(teamPath(), cur, teamDoc("a", "c"))
	if !errors.Is(err, ErrCASConflict) {
		t.Fatalf("err = %v, want ErrCASConflict", err)
	}
	if got := loadDoc(t, store); !reflect.DeepEqual(got, teamDoc("a", "b")) {
		t.Fatalf("conflict mutated doc: %+v", got)
	}
}

func TestCompareAndSwapCreateIfAbsent(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.CompareAndSwap(teamPath(), nil, teamDoc("a")); err != nil {
		t.Fatal(err)
	}
	if got := loadDoc(t, store); !reflect.DeepEqual(got, teamDoc("a")) {
		t.Fatalf("doc = %+v, want %+v", got, teamDoc("a"))
	}
}

func TestCompareAndSwapCreateConflictWhenExists(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Save(teamPath(), teamDoc("a")); err != nil {
		t.Fatal(err)
	}
	err := store.CompareAndSwap(teamPath(), nil, teamDoc("b"))
	if !errors.Is(err, ErrCASConflict) {
		t.Fatalf("err = %v, want ErrCASConflict", err)
	}
	if got := loadDoc(t, store); !reflect.DeepEqual(got, teamDoc("a")) {
		t.Fatalf("conflict mutated doc: %+v", got)
	}
}

func TestCompareAndSwapMissingFileConflicts(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	err := store.CompareAndSwap(teamPath(), teamDoc("a"), teamDoc("b"))
	if !errors.Is(err, ErrCASConflict) {
		t.Fatalf("err = %v, want ErrCASConflict", err)
	}
}

func TestCompareAndSwapRejectsInvalidSchemaDoc(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Save(teamPath(), teamDoc("a")); err != nil {
		t.Fatal(err)
	}
	cur := loadDoc(t, store)
	err := store.CompareAndSwap(teamPath(), cur, &TeamDoc{Document: Document{SchemaVersion: 2}})
	if !errors.Is(err, ErrSchemaVersion) {
		t.Fatalf("err = %v, want ErrSchemaVersion", err)
	}
	if got := loadDoc(t, store); !reflect.DeepEqual(got, teamDoc("a")) {
		t.Fatalf("rejected CAS mutated doc: %+v", got)
	}
}

func TestCompareAndSwapRejectsCorruptCurrentFile(t *testing.T) {
	// The current file's bytes equal what expected marshals to, but the
	// schema_version header is not an integer: corrupt, refused loudly.
	store := newTestStore(t, `{"schema_version":"one"}`)
	type broken struct {
		SchemaVersion string `json:"schema_version"`
	}
	err := store.CompareAndSwap(teamPath(), broken{SchemaVersion: "one"}, teamDoc("a"))
	var ce *CorruptFileError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CorruptFileError", err)
	}
	got, err := os.ReadFile(filepath.Join(store.root, teamPath()))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"schema_version":"one"}` {
		t.Fatalf("corrupt file was overwritten: %s", got)
	}
}

func TestCompareAndSwapRejectsUnsafePath(t *testing.T) {
	root := t.TempDir()
	store, _ := NewFileStore(root)
	if err := store.CompareAndSwap("../escape.json", nil, teamDoc("a")); err == nil {
		t.Fatal("CAS escaped the project root without error")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.json")); !os.IsNotExist(err) {
		t.Fatal("escape file exists after rejected CAS")
	}
}

// TestCompareAndSwapConcurrentNoLostUpdate hammers one file with concurrent
// read-modify-write CAS loops: every team appended by a goroutine must be
// present when the dust settles, and each goroutine must win eventually.
func TestCompareAndSwapConcurrentNoLostUpdate(t *testing.T) {
	const n = 32
	store, _ := NewFileStore(t.TempDir())
	errCh := make(chan error, n)
	for i := range n {
		go func(id int) {
			errCh <- appendTeamCAS(store, fmt.Sprintf("t-%02d", id))
		}(i)
	}
	for range n {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if got := loadDoc(t, store); len(got.Teams) != n {
		t.Fatalf("teams = %d, want %d (lost update)", len(got.Teams), n)
	}
}

// TestCompareAndSwapConcurrentExactlyOneWinner has two writers starting from
// the same snapshot: the first CAS wins, the second must see ErrCASConflict.
func TestCompareAndSwapConcurrentExactlyOneWinner(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Save(teamPath(), teamDoc("base")); err != nil {
		t.Fatal(err)
	}
	cur := loadDoc(t, store)
	result := make(chan string, 2)
	for _, next := range []*TeamDoc{teamDoc("base", "x"), teamDoc("base", "y")} {
		go func(next *TeamDoc) {
			if err := store.CompareAndSwap(teamPath(), cur, next); err == nil {
				result <- "win"
			} else if errors.Is(err, ErrCASConflict) {
				result <- "conflict"
			} else {
				result <- fmt.Sprintf("unexpected: %v", err)
			}
		}(next)
	}
	wins, conflicts := 0, 0
	for range 2 {
		msg := <-result
		switch msg {
		case "win":
			wins++
		case "conflict":
			conflicts++
		default:
			t.Fatal(msg)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins = %d, conflicts = %d, want 1 and 1", wins, conflicts)
	}
}

// appendTeamCAS appends one team with a read-modify-write CAS retry loop:
// re-read on conflict, create-if-absent when the file does not exist yet.
func appendTeamCAS(store *FileStore, name string) error {
	for {
		var cur TeamDoc
		err := store.Load(teamPath(), &cur)
		missing := os.IsNotExist(err)
		if err != nil && !missing {
			return err
		}
		var expected any
		if !missing {
			expected = &cur
		}
		next := TeamDoc{
			Document: Document{SchemaVersion: SchemaVersion},
			Teams:    append(cur.Teams, Team{Name: name}),
		}
		err = store.CompareAndSwap(teamPath(), expected, &next)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return err
		}
	}
}

// TestCASAcceptsEquivalentEncoding pins that CAS compares documents, not raw
// bytes: a hand-indented file, and one holding null where the caller holds an
// empty slice, are the same document and must not read as a conflict. Byte
// comparison made every write to such a registry fail forever.
func TestCASAcceptsEquivalentEncoding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored string
	}{
		{"indented", "{\n  \"schema_version\": 1,\n  \"teams\": []\n}"},
		{"null slice", `{"schema_version":1,"teams":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewFileStore(root)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(".reasonix", "team", TeamFile)
			full := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(tc.stored), 0o600); err != nil {
				t.Fatal(err)
			}

			expected := TeamDoc{Document: Document{SchemaVersion: SchemaVersion}, Teams: []Team{}}
			next := TeamDoc{Document: Document{SchemaVersion: SchemaVersion}, Teams: []Team{{Name: "first"}}}
			if err := store.CompareAndSwap(path, &expected, &next); err != nil {
				t.Fatalf("CompareAndSwap over an equivalent encoding: %v", err)
			}
			var got TeamDoc
			if err := store.Load(path, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Teams) != 1 || got.Teams[0].Name != "first" {
				t.Fatalf("stored doc = %+v, want the published one", got.Teams)
			}
		})
	}
}

// TestCASStillRejectsDifferentDocument keeps the equivalence check from
// weakening the conflict guarantee.
func TestCASStillRejectsDifferentDocument(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".reasonix", "team", TeamFile)
	stored := TeamDoc{Document: Document{SchemaVersion: SchemaVersion}, Teams: []Team{{Name: "other"}}}
	if err := store.Save(path, &stored); err != nil {
		t.Fatal(err)
	}
	expected := TeamDoc{Document: Document{SchemaVersion: SchemaVersion}, Teams: []Team{}}
	next := TeamDoc{Document: Document{SchemaVersion: SchemaVersion}, Teams: []Team{{Name: "first"}}}
	if err := store.CompareAndSwap(path, &expected, &next); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("err = %v, want ErrCASConflict", err)
	}
	var got TeamDoc
	if err := store.Load(path, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Teams) != 1 || got.Teams[0].Name != "other" {
		t.Fatalf("refused CAS must not publish, got %+v", got.Teams)
	}
}
