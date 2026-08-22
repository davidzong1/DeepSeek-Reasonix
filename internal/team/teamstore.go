package team

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Registry errors. ErrLastTeam refuses deleting the final team so the registry
// can never be emptied by accident; ErrMigrateRefused guards the primary file.
var (
	ErrTeamNotFound   = errors.New("team: no such team")
	ErrTeamExists     = errors.New("team: team already exists")
	ErrInvalidTeam    = errors.New("team: team name must not be empty")
	ErrMemberNotFound = errors.New("team: no such member")
	ErrMemberExists   = errors.New("team: member already exists")
	ErrInvalidMember  = errors.New("team: member id must not be empty")
	ErrLastTeam       = errors.New("team: refusing to delete the last team")
	ErrInvalidStatus  = errors.New("team: invalid member status")
	ErrMigrateRefused = errors.New("team: migration refused")
	ErrLeaderOnly     = errors.New("team: member create/delete restricted to the leader")
	ErrInvalidPolicy  = errors.New("team: invalid member write policy")
)

var (
	primaryPath = filepath.Join(".reasonix", "team", TeamFile)
	legacyPath  = filepath.Join(".reasonix", "team", TeamsLegacyFile)
)

// MemberWritePolicy gates who may add or delete member slots (§11-A). Open
// lets any caller through the CAS loop; LeaderOnly reserves creation and
// deletion for the leader. The policy is a store-level switch, defaulting to
// open; membership edits other than create/delete are never gated.
type MemberWritePolicy int

const (
	MemberWriteOpen       MemberWritePolicy = iota // default: any caller
	MemberWriteLeaderOnly                          // only the leader may create/delete members
)

// TeamStore is the domain-level registry store (§2.5, §3.4): the primary
// document is .reasonix/team/team.json; Load falls back to the legacy
// teams.json read-only, and MigrateLegacy publishes it exactly once. Every
// mutation runs load-modify-CompareAndSwap, so a concurrent writer surfaces
// as ErrCASConflict instead of being clobbered.
type TeamStore struct {
	store        *FileStore
	agentUsers   *AgentUsersStore // pool store for binding validation (§5)
	memberPolicy MemberWritePolicy
}

// NewTeamStore returns a TeamStore rooted at projectRoot.
func NewTeamStore(projectRoot string) (*TeamStore, error) {
	if _, err := TeamRoot(projectRoot); err != nil {
		return nil, err
	}
	store, err := NewFileStore(projectRoot)
	if err != nil {
		return nil, err
	}
	ts := &TeamStore{store: store, agentUsers: &AgentUsersStore{store: store}}
	ts.agentUsers.inUse = ts.agentUserInUse
	return ts, nil
}

// agentUserInUse reports whether any team references the pool entry, as a
// member override or the team default; an absent registry references nothing.
func (s *TeamStore) agentUserInUse(id string) (bool, error) {
	doc, _, err := s.Load()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for i := range doc.Teams {
		if doc.Teams[i].DefaultAgentUserRef == id {
			return true, nil
		}
		for j := range doc.Teams[i].Template {
			if doc.Teams[i].Template[j].AgentUserRef == id {
				return true, nil
			}
		}
	}
	return false, nil
}

// SetMemberWritePolicy switches the member create/delete gate; an unknown
// policy value is refused.
func (s *TeamStore) SetMemberWritePolicy(p MemberWritePolicy) error {
	switch p {
	case MemberWriteOpen, MemberWriteLeaderOnly:
		s.memberPolicy = p
		return nil
	default:
		return ErrInvalidPolicy
	}
}

// Load reads the registry from the primary file, falling back to the legacy
// file only when the primary is absent; legacy reports which source was read.
// A corrupt primary surfaces loudly and is never masked by fallback.
func (s *TeamStore) Load() (TeamDoc, bool, error) {
	var doc TeamDoc
	err := s.store.Load(primaryPath, &doc)
	if err == nil {
		return doc, false, nil
	}
	if !os.IsNotExist(err) {
		return TeamDoc{}, false, err
	}
	if err := s.store.Load(legacyPath, &doc); err != nil {
		return TeamDoc{}, false, err
	}
	return doc, true, nil
}

// Save publishes doc to the primary file atomically (§3.4).
func (s *TeamStore) Save(doc TeamDoc) error {
	return s.store.Save(primaryPath, &doc)
}

// CompareAndSwap publishes doc only if the primary file still holds expected.
func (s *TeamStore) CompareAndSwap(expected, doc TeamDoc) error {
	return s.store.CompareAndSwap(primaryPath, &expected, &doc)
}

// AddTeam appends a team; a duplicate or empty name is refused.
func (s *TeamStore) AddTeam(t Team) error {
	if strings.TrimSpace(t.Name) == "" {
		return ErrInvalidTeam
	}
	return s.update(func(doc *TeamDoc) error {
		if teamIndex(doc, t.Name) >= 0 {
			return ErrTeamExists
		}
		doc.Teams = append(doc.Teams, t)
		return nil
	})
}

// DeleteTeam removes the team by name; deleting the last team is refused.
func (s *TeamStore) DeleteTeam(name string) error {
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, name)
		if i < 0 {
			return ErrTeamNotFound
		}
		if len(doc.Teams) == 1 {
			return ErrLastTeam
		}
		doc.Teams = append(doc.Teams[:i], doc.Teams[i+1:]...)
		return nil
	})
}

// AddMember appends a member slot to the named team; duplicate and empty
// member ids are refused, as is an invalid role. Under MemberWriteLeaderOnly
// the add is refused before any read or write.
func (s *TeamStore) AddMember(teamName string, slot MemberSlot) error {
	if s.memberPolicy == MemberWriteLeaderOnly {
		return ErrLeaderOnly
	}
	if strings.TrimSpace(slot.MemberID) == "" {
		return ErrInvalidMember
	}
	if err := ValidateRole(string(slot.Role)); err != nil {
		return err
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		for _, m := range doc.Teams[i].Template {
			if m.MemberID == slot.MemberID {
				return ErrMemberExists
			}
		}
		doc.Teams[i].Template = append(doc.Teams[i].Template, slot)
		return nil
	})
}

// DeleteMember removes a member slot from the named team. Under
// MemberWriteLeaderOnly the delete is refused before any read or write.
func (s *TeamStore) DeleteMember(teamName, memberID string) error {
	if s.memberPolicy == MemberWriteLeaderOnly {
		return ErrLeaderOnly
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		tpl := doc.Teams[i].Template
		for j, m := range tpl {
			if m.MemberID == memberID {
				doc.Teams[i].Template = append(tpl[:j], tpl[j+1:]...)
				return nil
			}
		}
		return ErrMemberNotFound
	})
}

// SetMemberStatus updates a member slot's lifecycle status; an unknown status
// is refused.
func (s *TeamStore) SetMemberStatus(teamName, memberID string, status MemberStatus) error {
	switch status {
	case MemberStatusActive, MemberStatusDisabled, MemberStatusArchived:
	default:
		return ErrInvalidStatus
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		for j := range doc.Teams[i].Template {
			if doc.Teams[i].Template[j].MemberID == memberID {
				doc.Teams[i].Template[j].Status = status
				return nil
			}
		}
		return ErrMemberNotFound
	})
}

// MigrateLegacy publishes the legacy teams.json content as the primary file,
// exactly once: create-if-absent, so an existing primary is never overwritten
// (ErrMigrateRefused). The legacy file is left in place as read-only fallback.
func (s *TeamStore) MigrateLegacy() error {
	var doc TeamDoc
	if err := s.store.Load(legacyPath, &doc); err != nil {
		return err
	}
	err := s.store.CompareAndSwap(primaryPath, nil, &doc)
	if errors.Is(err, ErrCASConflict) {
		return fmt.Errorf("%w (primary %s exists; %w)", ErrMigrateRefused, TeamFile, ErrCASConflict)
	}
	return err
}

// update runs fn against the loaded registry and publishes the result under a
// CAS loop (max 3 attempts): a conflict re-loads and retries, so concurrent
// writers surface as ErrCASConflict rather than a silent clobber.
func (s *TeamStore) update(fn func(*TeamDoc) error) error {
	for attempt := range 3 {
		doc, create, err := s.loadForUpdate()
		if err != nil {
			return err
		}
		expected := cloneDoc(doc)
		if err := fn(&doc); err != nil {
			return err
		}
		if create {
			err = s.store.CompareAndSwap(primaryPath, nil, &doc)
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

// loadForUpdate reads the registry for a mutation, reporting whether the
// publish must create the primary file. That covers both a read served by the
// legacy fallback and a project with no registry at all — the latter starts
// from an empty document, so the first team can be created without hand-writing
// team.json. Anything else (corrupt, schema mismatch, I/O) surfaces unchanged.
func (s *TeamStore) loadForUpdate() (TeamDoc, bool, error) {
	doc, legacy, err := s.Load()
	if err == nil {
		return doc, legacy, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return TeamDoc{Document: Document{SchemaVersion: SchemaVersion}}, true, nil
	}
	return TeamDoc{}, false, err
}

// teamIndex returns the team's index in doc, or -1.
func teamIndex(doc *TeamDoc, name string) int {
	for i := range doc.Teams {
		if doc.Teams[i].Name == name {
			return i
		}
	}
	return -1
}

// cloneDoc deep-copies the registry: a mutator appends through shared slice
// backing arrays, so the CAS expected version must not alias the working doc.
// The Proxy and ProxyEnabled pointers are copied too, so a mutator writing
// through them can never reach the expected version.
func cloneDoc(doc TeamDoc) TeamDoc {
	cp := doc
	cp.Teams = make([]Team, len(doc.Teams))
	for i := range doc.Teams {
		cp.Teams[i] = doc.Teams[i]
		cp.Teams[i].Template = append([]MemberSlot(nil), doc.Teams[i].Template...)
		for j := range doc.Teams[i].Template {
			if enabled := doc.Teams[i].Template[j].ProxyEnabled; enabled != nil {
				v := *enabled
				cp.Teams[i].Template[j].ProxyEnabled = &v
			}
		}
		if proxy := doc.Teams[i].Proxy; proxy != nil {
			p := *proxy
			cp.Teams[i].Proxy = &p
		}
	}
	return cp
}
