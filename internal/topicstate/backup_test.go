package topicstate

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupExistingIncludesWALAndUnknownTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE future_data(value TEXT); INSERT INTO future_data VALUES ('preserved');`); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := BackupExisting(t.Context(), path, dest); err != nil {
		t.Fatal(err)
	}
	backup, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var value string
	if err := backup.QueryRow("SELECT value FROM future_data").Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("backup value=%q err=%v", value, err)
	}
	if err := BackupExisting(t.Context(), filepath.Join(t.TempDir(), "missing"), dest); !os.IsNotExist(err) {
		t.Fatalf("missing source: %v", err)
	}
}
