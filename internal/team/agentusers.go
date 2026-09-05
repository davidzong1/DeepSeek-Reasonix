package team

import (
	"errors"
	"os"
)

// Agent-user pool errors. ErrLastAgentUser refuses emptying the pool by
// accident, mirroring ErrLastTeam; ErrAgentUserInUse refuses deleting an
// entry any team still references (§2.1), so a removal can never orphan a
// binding.
var (
	ErrAgentUserExists   = errors.New("team: agent user already exists")
	ErrAgentUserNotFound = errors.New("team: no such agent user")
	ErrLastAgentUser     = errors.New("team: refusing to delete the last agent user")
	ErrInvalidAgentUser  = errors.New("team: agent user id must not be empty")
	ErrAgentUserInUse    = errors.New("team: agent user is referenced by a member; unbind it first")
)

// AgentUsersStore is the pool document store (§2.1): agent_users.json beside
// team.json in the team data dir. It is a sibling of TeamStore — the pool
// belongs to no team, and its document must not block team.json writes —
// sharing the atomic write chokepoint, so key material lands at 0600 exactly
// like team.json. The optional inUse check lets a host store (TeamStore)
// refuse deleting a referenced entry; a standalone pool has no teams to
// consult.
type AgentUsersStore struct {
	store *FileStore
	inUse func(id string) (bool, error) // reference check; nil = none
}

// NewAgentUsersStore returns a pool store rooted at the project's team data
// dir (<projectRoot>/.reasonix/team).
func NewAgentUsersStore(projectRoot string) (*AgentUsersStore, error) {
	root, err := TeamRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	store, err := NewFileStore(root)
	if err != nil {
		return nil, err
	}
	return &AgentUsersStore{store: store}, nil
}

// Save publishes the pool document atomically.
func (s *AgentUsersStore) Save(doc AgentUsersDoc) error {
	return s.store.Save(AgentUsersFile, &doc)
}

// Load reads the pool document; an absent one is an empty pool, and a read
// never creates the file.
func (s *AgentUsersStore) Load() (AgentUsersDoc, error) {
	var doc AgentUsersDoc
	err := s.store.Load(AgentUsersFile, &doc)
	if err == nil {
		return doc, nil
	}
	if os.IsNotExist(err) {
		return AgentUsersDoc{Document: Document{SchemaVersion: SchemaVersion}}, nil
	}
	return AgentUsersDoc{}, err
}

// CompareAndSwap publishes doc only if the stored pool still matches expected.
func (s *AgentUsersStore) CompareAndSwap(expected, doc AgentUsersDoc) error {
	return s.store.CompareAndSwap(AgentUsersFile, &expected, &doc)
}

// update runs fn against the pool and publishes it under the same CAS loop as
// team.json, so concurrent writers surface as ErrCASConflict rather than a
// silent clobber. A missing file publishes create-if-absent.
func (s *AgentUsersStore) update(fn func(*AgentUsersDoc) error) error {
	for attempt := range 3 {
		doc, create, err := s.loadForUpdate()
		if err != nil {
			return err
		}
		expected := cloneAgentUsersDoc(doc)
		if err := fn(&doc); err != nil {
			return err
		}
		if create {
			err = s.store.CompareAndSwap(AgentUsersFile, nil, &doc)
		} else {
			err = s.CompareAndSwap(expected, doc)
		}
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrCASConflict) && attempt < 2 {
			continue
		}
		return err
	}
	return ErrCASConflict
}

// loadForUpdate reads the pool for a mutation, reporting whether the publish
// must create the file: Load serves a missing pool as empty for readers, but
// a mutation must create it with expected == nil.
func (s *AgentUsersStore) loadForUpdate() (AgentUsersDoc, bool, error) {
	var doc AgentUsersDoc
	err := s.store.Load(AgentUsersFile, &doc)
	if err == nil {
		return doc, false, nil
	}
	if os.IsNotExist(err) {
		return AgentUsersDoc{Document: Document{SchemaVersion: SchemaVersion}}, true, nil
	}
	return AgentUsersDoc{}, false, err
}

// AddAgentUser appends a pool entry; an empty id or a duplicate is refused.
func (s *AgentUsersStore) AddAgentUser(u AgentUser) error {
	if err := ValidateAgentUser(u); err != nil {
		return err
	}
	return s.update(func(doc *AgentUsersDoc) error {
		if agentUserIndex(doc, u.UserID) >= 0 {
			return ErrAgentUserExists
		}
		doc.AgentUsers = append(doc.AgentUsers, u)
		return nil
	})
}

// DeleteAgentUser removes a pool entry; deleting the last entry is refused.
// With an inUse check installed, an entry any team references — as a member
// override or the team default — is refused (ErrAgentUserInUse), so a
// deletion can never orphan a binding.
func (s *AgentUsersStore) DeleteAgentUser(id string) error {
	if s.inUse != nil {
		used, err := s.inUse(id)
		if err != nil {
			return err
		}
		if used {
			return ErrAgentUserInUse
		}
	}
	return s.update(func(doc *AgentUsersDoc) error {
		i := agentUserIndex(doc, id)
		if i < 0 {
			return ErrAgentUserNotFound
		}
		if len(doc.AgentUsers) == 1 {
			return ErrLastAgentUser
		}
		doc.AgentUsers = append(doc.AgentUsers[:i], doc.AgentUsers[i+1:]...)
		return nil
	})
}

// ListAgentUsers returns the pool in registry order.
func (s *AgentUsersStore) ListAgentUsers() ([]AgentUser, error) {
	doc, err := s.Load()
	if err != nil {
		return nil, err
	}
	return doc.AgentUsers, nil
}

// UpdateAgentUser replaces a pool entry in place; an unknown id is refused.
// The whole entry validates inside the CAS loop with the legacy-preserve
// exemption: an untouched provider of an imported entry survives the replace,
// so editing other fields never orphans it.
func (s *AgentUsersStore) UpdateAgentUser(u AgentUser) error {
	return s.update(func(doc *AgentUsersDoc) error {
		i := agentUserIndex(doc, u.UserID)
		if i < 0 {
			return ErrAgentUserNotFound
		}
		if err := validateAgentUserAllowLegacy(u, doc.AgentUsers[i].Provider); err != nil {
			return err
		}
		doc.AgentUsers[i] = u
		return nil
	})
}

// UpdateAgentUserField sets one field of a pool entry in place, validating the
// single field up front and the merged entry inside the CAS loop, so the
// untouched fields never clobber a concurrent writer. An unknown id is refused;
// an empty value clears the field; the api key is stored as given, so callers
// decide trimming and keep-vs-clear semantics.
func (s *AgentUsersStore) UpdateAgentUserField(id, field, value string) error {
	if err := ValidateAgentUserField(field, value); err != nil {
		return err
	}
	return s.update(func(doc *AgentUsersDoc) error {
		i := agentUserIndex(doc, id)
		if i < 0 {
			return ErrAgentUserNotFound
		}
		prev := doc.AgentUsers[i]
		switch field {
		case AgentUserFieldIdentity:
			doc.AgentUsers[i].Identity = value
		case AgentUserFieldProvider:
			doc.AgentUsers[i].Provider = value
		case AgentUserFieldBaseURL:
			doc.AgentUsers[i].BaseURL = value
		case AgentUserFieldModel:
			doc.AgentUsers[i].Model = value
		case AgentUserFieldEffort:
			doc.AgentUsers[i].Effort = value
		case AgentUserFieldAPIKey:
			doc.AgentUsers[i].APIKey = value
		default:
			return &AgentUserFieldError{Field: field, Reason: "not an editable field"}
		}
		return validateAgentUserAllowLegacy(doc.AgentUsers[i], prev.Provider)
	})
}

// GetAgentUser returns the pool entry by id, reporting whether it exists.
func (s *AgentUsersStore) GetAgentUser(id string) (AgentUser, bool, error) {
	doc, err := s.Load()
	if err != nil {
		return AgentUser{}, false, err
	}
	for _, u := range doc.AgentUsers {
		if u.UserID == id {
			return u, true, nil
		}
	}
	return AgentUser{}, false, nil
}

// agentUserIndex returns the entry's index in the pool, or -1.
func agentUserIndex(doc *AgentUsersDoc, id string) int {
	for i := range doc.AgentUsers {
		if doc.AgentUsers[i].UserID == id {
			return i
		}
	}
	return -1
}

// cloneAgentUsersDoc deep-copies the pool: a mutator appends through shared
// slice backing arrays, so the CAS expected version must not alias the
// working doc.
func cloneAgentUsersDoc(doc AgentUsersDoc) AgentUsersDoc {
	cp := doc
	cp.AgentUsers = make([]AgentUser, len(doc.AgentUsers))
	for i := range doc.AgentUsers {
		cp.AgentUsers[i] = doc.AgentUsers[i]
		cp.AgentUsers[i].RBACBindings = append([]RoleID(nil), doc.AgentUsers[i].RBACBindings...)
	}
	return cp
}

// Pool mutations on TeamStore forward to the pool store, so the TUI keeps one
// handle for both registries.
func (s *TeamStore) AddAgentUser(u AgentUser) error {
	return s.agentUsers.AddAgentUser(u)
}

// DeleteAgentUser removes a pool entry; a referenced entry — a member override
// or the team default — is refused (ErrAgentUserInUse), so a deletion can never
// orphan a binding. Deleting the last entry is also refused.
func (s *TeamStore) DeleteAgentUser(id string) error {
	return s.agentUsers.DeleteAgentUser(id)
}

func (s *TeamStore) ListAgentUsers() ([]AgentUser, error) {
	return s.agentUsers.ListAgentUsers()
}

func (s *TeamStore) UpdateAgentUser(u AgentUser) error {
	return s.agentUsers.UpdateAgentUser(u)
}

// UpdateAgentUserField forwards the field update to the pool store.
func (s *TeamStore) UpdateAgentUserField(id, field, value string) error {
	return s.agentUsers.UpdateAgentUserField(id, field, value)
}

// AgentUser returns the pool entry by id, reporting whether it exists.
func (s *TeamStore) AgentUser(id string) (AgentUser, bool, error) {
	return s.agentUsers.GetAgentUser(id)
}
