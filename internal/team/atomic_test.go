package team

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(".reasonix", "team", "teams.json")
	if err := AtomicWrite(root, path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("content = %q", got)
	}
	if err := AtomicWrite(root, path, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":2}` {
		t.Fatalf("overwrite content = %q", got)
	}
}

func TestAtomicWritePerm(t *testing.T) {
	root := t.TempDir()
	path := "teams.json"
	if err := AtomicWrite(root, path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 600", got)
	}
}

func TestAtomicWriteNoTempResidue(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(".reasonix", "team", "teams.json")
	if err := AtomicWrite(root, path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".reasonix", "team"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".atomic-") {
			t.Fatalf("temp residue: %s", e.Name())
		}
	}
}

func TestAtomicWriteRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	unsafe := []string{"../escape.json", "..", "a/../../escape.json", "/abs.json"}
	for _, p := range unsafe {
		if err := AtomicWrite(root, p, []byte("x")); err == nil {
			t.Errorf("path %q: want error, got nil", p)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.json")); !os.IsNotExist(err) {
		t.Errorf("escape file exists after rejected write")
	}
}
