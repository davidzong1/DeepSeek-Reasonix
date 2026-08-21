package team

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
)

// ErrCASConflict reports that the stored document changed since expected was
// read: re-load and retry (optimistic concurrency, §3.4).
var ErrCASConflict = errors.New("team: cas conflict: document changed since expected version")

// CompareAndSwap publishes doc only if the stored document still equals
// expected — the version the caller read, compared as a document rather than
// as raw bytes (sameDocument). expected == nil requires the file to be absent
// (create-if-absent). The read-compare-publish sequence runs under writeMu, so
// no other write through the chokepoint can interleave before the atomic
// rename. A current file that fails the schema gate is corrupt and refused
// loudly, never silently overwritten.
func (s *FileStore) CompareAndSwap(path string, expected any, doc any) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	full, err := safePath(s.root, path)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(full)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	missing := os.IsNotExist(err)
	if expected == nil {
		if !missing {
			return fmt.Errorf("%w: %s already exists", ErrCASConflict, path)
		}
	} else {
		if missing {
			return fmt.Errorf("%w: %s does not exist", ErrCASConflict, path)
		}
		same, err := sameDocument(current, expected)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("%w: %s changed since expected", ErrCASConflict, path)
		}
		if err := checkSchemaVersion(path, current); err != nil {
			return err
		}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := checkSchemaVersion(path, data); err != nil {
		return err
	}
	return atomicWriteLocked(s.root, path, data)
}

// sameDocument reports whether the stored bytes hold the same document as
// expected. Equivalent encodings must compare equal, so a mismatch is retried
// through a canonical re-encoding of both sides: a hand-indented file, or one
// written when a slice was nil where the caller now holds an empty one, is the
// same document and must not read as a conflict. Bytes that do not decode into
// expected's type are a different document, not an error.
func sameDocument(current []byte, expected any) (bool, error) {
	want, err := json.Marshal(expected)
	if err != nil {
		return false, err
	}
	if bytes.Equal(current, want) {
		return true, nil
	}
	typ := reflect.TypeOf(expected)
	if typ == nil || typ.Kind() != reflect.Pointer {
		return false, nil
	}
	wantCanonical, err := canonicalJSON(typ.Elem(), want)
	if err != nil {
		return false, err
	}
	gotCanonical, err := canonicalJSON(typ.Elem(), current)
	if err != nil {
		return false, nil
	}
	return bytes.Equal(gotCanonical, wantCanonical), nil
}

// canonicalizer is implemented by document types with a single canonical
// encoded form; CompareAndSwap compares that form rather than raw bytes.
type canonicalizer interface{ canonicalize() }

// canonicalJSON decodes data as typ and re-encodes it canonically. A type
// without a canonical form round-trips unchanged.
func canonicalJSON(typ reflect.Type, data []byte) ([]byte, error) {
	fresh := reflect.New(typ).Interface()
	if err := json.Unmarshal(data, fresh); err != nil {
		return nil, err
	}
	if c, ok := fresh.(canonicalizer); ok {
		c.canonicalize()
	}
	return json.Marshal(fresh)
}
