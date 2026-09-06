package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/team"
)

// Discussion host seam: teamTaskService methods drive the team's DiscussionDoc
// and deposit its terminal outcome into the team knowledge base; the structured
// submit is the contract, so prompt text is never parsed as a conclusion.

// discussionAdvanceInput is the leader's decision at a round boundary.
type discussionAdvanceInput struct {
	ConsensusReached bool
	Consensus        string
	TechnicalRoute   string
}

func (s *teamTaskService) discussionEnabled() bool {
	return s != nil && strings.TrimSpace(s.discussionDataDir) != "" && strings.TrimSpace(s.teamName) != ""
}

func (s *teamTaskService) knowledgeEnabled() bool {
	return s != nil && strings.TrimSpace(s.kbDataRoot) != "" && strings.TrimSpace(s.teamName) != ""
}

func (s *teamTaskService) discussionStore() (*team.DiscussionStore, error) {
	if !s.discussionEnabled() {
		return nil, nil
	}
	s.discussionMu.Lock()
	defer s.discussionMu.Unlock()
	if s.dstore == nil {
		fs, err := team.NewFileStore(s.discussionDataDir)
		if err != nil {
			return nil, err
		}
		s.dstore = team.NewDiscussionStore(fs)
	}
	return s.dstore, nil
}

// DiscussionStart opens the team's one active discussion. participants empty
// derives the frozen set from the active non-leader roster; an explicit list
// must be active non-leader members. A previous session whose outcome has not
// reached the KB blocks a new start, so a crash between end and deposit is
// never resolved by overwriting the consensus.
func (s *teamTaskService) DiscussionStart(leaderID, topic string, participants []string, maxRounds int) (team.DiscussionDoc, error) {
	leader, err := s.requireDiscussionLeader(leaderID)
	if err != nil {
		return team.DiscussionDoc{}, err
	}
	frozen, err := s.discussionParticipants(participants)
	if err != nil {
		return team.DiscussionDoc{}, err
	}
	if err := s.deliverPendingDeposit(); err != nil {
		return team.DiscussionDoc{}, err
	}
	ds, err := s.discussionStore()
	if err != nil || ds == nil {
		return team.DiscussionDoc{}, fmt.Errorf("discussion store unavailable")
	}
	session := fmt.Sprintf("disc-%s-%d", s.teamName, time.Now().UTC().UnixNano())
	if err := ds.Update(s.teamName, func(d *team.DiscussionDoc) error {
		return d.Start(leader, topic, frozen, maxRounds, session, nowRFC3339Nano())
	}); err != nil {
		return team.DiscussionDoc{}, err
	}
	return ds.Load(s.teamName)
}

// DiscussionSubmitConclusion is the structured participant contract: one
// member, one conclusion, for the current round. It is idempotent — a retry or
// a revision replaces that member's own entry — and it is the authoritative
// source; prompt text is never parsed as the conclusion.
func (s *teamTaskService) DiscussionSubmitConclusion(memberID string, round int, conclusion string) (string, error) {
	if _, err := s.teamStore.Binding(s.teamName, strings.TrimSpace(memberID)); err != nil {
		return "", err
	}
	ds, err := s.discussionStore()
	if err != nil || ds == nil {
		return "", fmt.Errorf("discussion store unavailable")
	}
	if err := ds.Update(s.teamName, func(d *team.DiscussionDoc) error {
		return d.Submit(strings.TrimSpace(memberID), round, conclusion, nowRFC3339Nano())
	}); err != nil {
		return "", err
	}
	doc, err := ds.Load(s.teamName)
	if err != nil {
		return "", err
	}
	return doc.Summary(), nil
}

// DiscussionNextRound advances or ends at the leader's decision. A consensus
// statement or technical route — authoritative outcome text — ends the session
// and starts the KB deposit even if the reached flag is unset; a full round
// budget ends without an outcome; otherwise the discussion moves one round.
func (s *teamTaskService) DiscussionNextRound(leaderID string, in discussionAdvanceInput) (string, error) {
	leader, err := s.requireDiscussionLeader(leaderID)
	if err != nil {
		return "", err
	}
	ds, err := s.discussionStore()
	if err != nil || ds == nil {
		return "", fmt.Errorf("discussion store unavailable")
	}
	deposit := s.knowledgeEnabled()
	ended := false
	err = ds.Update(s.teamName, func(d *team.DiscussionDoc) error {
		outcome := strings.TrimSpace(in.Consensus) != "" || strings.TrimSpace(in.TechnicalRoute) != ""
		switch {
		case in.ConsensusReached || outcome:
			// Outcome text is authoritative: a leader who wrote a consensus or
			// route has converged, so the text is never dropped on an Advance.
			if !outcome {
				return fmt.Errorf("consensus_reached requires a consensus statement or technical route")
			}
			ended = true
			return d.End(leader, team.EndReasonConsensus, in.Consensus, in.TechnicalRoute, deposit, nowRFC3339Nano())
		case d.Round >= d.MaxRounds:
			ended = true
			return d.End(leader, team.EndReasonMaxRounds, "", "", deposit, nowRFC3339Nano())
		default:
			return d.Advance(leader, nowRFC3339Nano())
		}
	})
	if err != nil {
		return "", err
	}
	if ended {
		if err := s.deliverPendingDeposit(); err != nil {
			return "", err
		}
	}
	doc, err := ds.Load(s.teamName)
	if err != nil {
		return "", err
	}
	return doc.Summary(), nil
}

// DiscussionEnd ends an active discussion for an explicit reason (manual close
// or no idle participants); a consensus end should go through
// DiscussionNextRound so the outcome text is captured.
func (s *teamTaskService) DiscussionEnd(leaderID string, reason team.DiscussionEndReason) (string, error) {
	leader, err := s.requireDiscussionLeader(leaderID)
	if err != nil {
		return "", err
	}
	ds, err := s.discussionStore()
	if err != nil || ds == nil {
		return "", fmt.Errorf("discussion store unavailable")
	}
	doc, err := ds.Load(s.teamName)
	if err != nil {
		return "", err
	}
	consensus, route := doc.Consensus, doc.TechnicalRoute
	if err := ds.Update(s.teamName, func(d *team.DiscussionDoc) error {
		if d.Status != team.DiscussionActive {
			return team.ErrDiscussionNotActive
		}
		return d.End(leader, reason, consensus, route, s.knowledgeEnabled(), nowRFC3339Nano())
	}); err != nil {
		return "", err
	}
	if err := s.deliverPendingDeposit(); err != nil {
		return "", err
	}
	after, err := ds.Load(s.teamName)
	if err != nil {
		return "", err
	}
	return after.Summary(), nil
}

// DiscussionSummary reads the current session for status and round prompts.
func (s *teamTaskService) DiscussionSummary() (string, error) {
	ds, err := s.discussionStore()
	if err != nil || ds == nil {
		return "team discussion is not enabled", nil
	}
	doc, err := ds.Load(s.teamName)
	if err != nil {
		return "", err
	}
	if doc.Status == team.DiscussionIdle && doc.SessionID == "" {
		return "no discussion for this team", nil
	}
	return doc.Summary(), nil
}

// DiscussionDeposit delivers a pending terminal outcome to the KB and marks it
// delivered — the retry point for a stranded deposit. Idempotent; when the KB is
// disabled it reports the stranded state rather than pretending delivery
// happened, and Ingest itself is durable-async, so no discussion round blocks.
func (s *teamTaskService) DiscussionDeposit() error {
	ds, err := s.discussionStore()
	if err != nil || ds == nil {
		return fmt.Errorf("discussion store unavailable")
	}
	doc, err := ds.Load(s.teamName)
	if err != nil {
		return err
	}
	if !doc.DepositPending() {
		return nil
	}
	if !s.knowledgeEnabled() {
		return fmt.Errorf("%w: team knowledge base is disabled; retry when it is available", team.ErrDepositPending)
	}
	tk, err := s.ensureKB()
	if err != nil {
		return err
	}
	if tk == nil {
		return fmt.Errorf("discussion: team knowledge base unavailable")
	}
	if _, err := tk.Manager.Ingest(context.Background(), []model.Thought{{
		ID: model.NewID(), TeamID: s.teamName, AgentID: doc.Leader,
		SessionID: doc.Deposit.SessionID, Kind: model.ThoughtConclusion, Text: doc.Deposit.Text,
	}}); err != nil {
		return err
	}
	return ds.Update(s.teamName, func(d *team.DiscussionDoc) error {
		return d.MarkDeposited(nowRFC3339Nano())
	})
}

// deliverPendingDeposit recovers an undelivered terminal outcome before any new
// session may start. Delivery is retried on every entry while the outcome stays
// pending, so a KB that is briefly closed never leaves the team stuck once it
// returns: DiscussionDeposit reports the stranded state until it can deliver.
func (s *teamTaskService) deliverPendingDeposit() error {
	ds, err := s.discussionStore()
	if err != nil || ds == nil {
		return fmt.Errorf("discussion store unavailable")
	}
	doc, err := ds.Load(s.teamName)
	if err != nil {
		return err
	}
	if !doc.DepositPending() {
		return nil
	}
	return s.DiscussionDeposit()
}

func (s *teamTaskService) requireDiscussionLeader(leaderID string) (string, error) {
	leaderID = strings.TrimSpace(leaderID)
	if s == nil || s.teamStore == nil {
		return "", fmt.Errorf("discussion: team store unavailable")
	}
	binding, err := s.teamStore.Binding(s.teamName, leaderID)
	if err != nil {
		return "", err
	}
	if !binding.Leader {
		return "", fmt.Errorf("%w: %q", team.ErrNotDiscussionLeader, leaderID)
	}
	return binding.MemberID, nil
}

// discussionParticipants freezes the debate's roster: an explicit list must be
// active non-leader members; an empty list derives from the active non-leader
// roster.
func (s *teamTaskService) discussionParticipants(want []string) ([]string, error) {
	doc, _, err := s.teamStore.Load()
	if err != nil {
		return nil, err
	}
	var eligible []string
	for _, t := range doc.Teams {
		if t.Name != s.teamName {
			continue
		}
		for _, slot := range t.Template {
			if !slot.IsLeader() && slot.Status != team.MemberStatusArchived && slot.Status != team.MemberStatusDisabled {
				eligible = append(eligible, slot.MemberID)
			}
		}
		break
	}
	if len(want) == 0 {
		return eligible, nil
	}
	set := map[string]bool{}
	for _, m := range eligible {
		set[m] = true
	}
	for _, m := range want {
		if !set[strings.TrimSpace(m)] {
			return nil, fmt.Errorf("discussion: %q is not an active non-leader member of team %q", m, s.teamName)
		}
	}
	return want, nil
}

func nowRFC3339Nano() string { return time.Now().UTC().Format(time.RFC3339Nano) }
