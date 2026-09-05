package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"reasonix/internal/fileutil"
	"reasonix/internal/knowledge_base/model"
)

var (
	// ErrConflict reports a create-only Put against an existing, different file.
	ErrConflict = errors.New("store: item already exists with different content")
	// ErrNotFound reports a missing item id.
	ErrNotFound = errors.New("store: item not found")
)

// Store is one team's item truth source under <root>/<team>/items.
type Store struct {
	itemsDir string
	mu       sync.RWMutex
	hashes   map[string]string // derived id->content hash cache, rebuildable
}

// Open creates (if needed) and opens the item store rooted at dir.
func Open(dir string) (*Store, error) {
	itemsDir := filepath.Join(dir, "items")
	if err := os.MkdirAll(itemsDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{itemsDir: itemsDir, hashes: map[string]string{}}
	if err := s.rebuildHashes(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) itemPath(id string) (string, error) {
	if err := model.ValidateRelID(id); err != nil {
		return "", err
	}
	if filepath.Base(id) != id {
		return "", fmt.Errorf("%w: id %q is not a bare segment", model.ErrInvalid, id)
	}
	return filepath.Join(s.itemsDir, id+".md"), nil
}

// Root returns the team data directory (parent of items/).
func (s *Store) Root() string { return filepath.Dir(s.itemsDir) }

// Put writes an item create-only: same id + identical bytes is a no-op,
// same id + different content is ErrConflict, never a silent overwrite.
func (s *Store) Put(i model.KnowledgeItem) (bool, error) {
	if err := model.ValidateItem(i); err != nil {
		return false, err
	}
	path, err := s.itemPath(i.ID)
	if err != nil {
		return false, err
	}
	data, err := marshalItem(i)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		if bytesEqual(existing, data) {
			return false, nil
		}
		return false, ErrConflict
	case !os.IsNotExist(rerr):
		return false, rerr
	}
	if err := fileutil.AtomicWriteFileStrict(path, data, 0o600); err != nil {
		return false, err
	}
	s.hashes[i.ID] = model.ItemContentHash(i)
	return true, nil
}

// Transition rewrites one item's metadata in place (status/version links).
// The knowledge body is preserved; only the apply callback's field edits land.
func (s *Store) Transition(id string, apply func(*model.KnowledgeItem) error) error {
	path, err := s.itemPath(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.readLocked(id)
	if err != nil {
		return err
	}
	prev := cur
	if err := apply(&cur); err != nil {
		return err
	}
	if err := model.ValidateItem(cur); err != nil {
		return err
	}
	data, err := marshalItem(cur)
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFileStrict(path, data, 0o600); err != nil {
		return err
	}
	if cur.Status != prev.Status || cur.SupersededBy != prev.SupersededBy {
		s.hashes[id] = model.ItemContentHash(cur)
	}
	return nil
}

// Get returns one item by id ("" status zero value = file missing).
func (s *Store) Get(id string) (model.KnowledgeItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readLocked(id)
}

func (s *Store) readLocked(id string) (model.KnowledgeItem, error) {
	path, err := s.itemPath(id)
	if err != nil {
		return model.KnowledgeItem{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return model.KnowledgeItem{}, ErrNotFound
		}
		return model.KnowledgeItem{}, err
	}
	return unmarshalItem(data)
}

// HasContentHash reports whether any item file already has this L1 fingerprint.
func (s *Store) HasContentHash(h string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.hashes {
		if v == h {
			return true
		}
	}
	return false
}

// List returns all items, newest file name first (ULID ids sort by time).
func (s *Store) List() ([]model.KnowledgeItem, error) {
	entries, err := os.ReadDir(s.itemsDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Name() > entries[b].Name() })
	out := make([]model.KnowledgeItem, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		it, gerr := s.Get(id)
		if gerr != nil {
			return nil, gerr
		}
		out = append(out, it)
	}
	return out, nil
}

// ByCanonical returns every stored item sharing the canonical key.
func (s *Store) ByCanonical(canonical string) ([]model.KnowledgeItem, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []model.KnowledgeItem
	for _, it := range all {
		if it.Canonical == canonical {
			out = append(out, it)
		}
	}
	return out, nil
}

// LatestLive returns the highest-version live item for a canonical key, or nil.
func (s *Store) LatestLive(canonical string) (*model.KnowledgeItem, error) {
	all, err := s.ByCanonical(canonical)
	if err != nil {
		return nil, err
	}
	var best *model.KnowledgeItem
	for i := range all {
		it := &all[i]
		if it.Status != model.StatusLive {
			continue
		}
		if best == nil || it.Version > best.Version {
			best = it
		}
	}
	return best, nil
}

func (s *Store) rebuildHashes() error {
	s.hashes = map[string]string{}
	entries, err := os.ReadDir(s.itemsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		it, gerr := s.Get(strings.TrimSuffix(e.Name(), ".md"))
		if gerr != nil {
			return gerr
		}
		s.hashes[it.ID] = model.ItemContentHash(it)
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
