package team

import "fmt"

// LeaderAddMember creates a member slot on behalf of a named leader. Agent
// facing management commands should use this API so authorization and storage
// semantics stay identical to the TUI path.
func (s *TeamStore) LeaderAddMember(teamName, leaderID string, slot MemberSlot) error {
	if err := s.requireLeader(teamName, leaderID); err != nil {
		return err
	}
	return s.addMember(teamName, slot)
}

// LeaderRemoveMember removes a member slot, refusing removal by non-leaders.
func (s *TeamStore) LeaderRemoveMember(teamName, leaderID, memberID string) error {
	if err := s.requireLeader(teamName, leaderID); err != nil {
		return err
	}
	return s.deleteMember(teamName, memberID)
}

// LeaderSetMemberRole changes a member role through the leader-only management
// surface. Role changes are persisted by the same validated CAS setter.
func (s *TeamStore) LeaderSetMemberRole(teamName, leaderID, memberID string, role RoleID) error {
	if err := s.requireLeader(teamName, leaderID); err != nil {
		return err
	}
	return s.SetMemberRole(teamName, memberID, role)
}

func (s *TeamStore) requireLeader(teamName, leaderID string) error {
	doc, _, err := s.Load()
	if err != nil {
		return err
	}
	for _, t := range doc.Teams {
		if t.Name != teamName {
			continue
		}
		for _, m := range t.Template {
			if m.MemberID == leaderID && m.IsLeader() {
				return nil
			}
		}
		return fmt.Errorf("team: member %q is not the leader", leaderID)
	}
	return ErrTeamNotFound
}
