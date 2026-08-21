package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CorruptFileError reports a team data file that exists but is not a valid
// JSON object for its document type: it must fail loudly, never read as zero.
type CorruptFileError struct {
	Path string
	Err  error
}

func (e *CorruptFileError) Error() string {
	return fmt.Sprintf("team data file %s is corrupt: %v", e.Path, e.Err)
}

func (e *CorruptFileError) Unwrap() error { return e.Err }

// ErrSchemaVersion reports a stored document whose schema_version header is
// missing or differs from SchemaVersion: migrate before loading.
var ErrSchemaVersion = errors.New("team: schema_version missing or does not match SchemaVersion; migrate first")

// Store is the JSON persistence surface for .reasonix/team (§3.4). Save
// writes atomically through the single chokepoint; Load rejects corrupt
// files and schema-version mismatches. CompareAndSwap publishes a doc only
// if the stored document still matches the expected version, else
// ErrCASConflict. Paths are project-relative; anything absolute or escaping
// the project root is refused.
type Store interface {
	Save(path string, doc any) error
	Load(path string, doc any) error
	CompareAndSwap(path string, expected any, doc any) error
}

// FileStore is the on-disk Store. root is the absolute project root.
type FileStore struct {
	root string
}

// NewFileStore returns a Store rooted at projectRoot (made absolute).
func NewFileStore(projectRoot string) (*FileStore, error) {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}
	return &FileStore{root: abs}, nil
}

// Save marshals doc and writes it atomically at the project-relative path.
// A document whose serialized schema_version header is missing or differs
// from SchemaVersion is refused with ErrSchemaVersion before any write.
func (s *FileStore) Save(path string, doc any) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := checkSchemaVersion(path, data); err != nil {
		return err
	}
	return AtomicWrite(s.root, path, data)
}

// Load reads the document at the project-relative path, enforcing the
// schema_version header and rejecting corrupt files.
func (s *FileStore) Load(path string, doc any) error {
	full, err := safePath(s.root, path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	if err := checkSchemaVersion(path, data); err != nil {
		return err
	}
	if err := json.Unmarshal(data, doc); err != nil {
		return &CorruptFileError{Path: path, Err: err}
	}
	return nil
}

// checkSchemaVersion validates the integer schema_version header on
// serialized document data. A non-object document or a non-integer header is
// corrupt; a missing or differing version is ErrSchemaVersion.
func checkSchemaVersion(path string, data []byte) error {
	var head map[string]json.RawMessage
	if err := json.Unmarshal(data, &head); err != nil {
		return &CorruptFileError{Path: path, Err: err}
	}
	raw, ok := head["schema_version"]
	if !ok {
		return fmt.Errorf("%w: %s has no schema_version header", ErrSchemaVersion, path)
	}
	var ver int
	if err := json.Unmarshal(raw, &ver); err != nil {
		return &CorruptFileError{Path: path, Err: err}
	}
	if ver != SchemaVersion {
		return fmt.Errorf("%w: %s version=%d want=%d", ErrSchemaVersion, path, ver, SchemaVersion)
	}
	return nil
}
