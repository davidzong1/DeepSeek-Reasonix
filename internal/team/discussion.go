package team

// Discussion is one team's durable debate state machine: leader transitions
// under CAS, per-(round,member) idempotent member conclusions, and the terminal
// consensus carried as a KB deposit (semantics: discussion_semantics_acceptance_test.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// DiscussionStatus is a discussion session's lifecycle state.
type DiscussionStatus string

const (
	DiscussionIdle   DiscussionStatus = "idle"
	DiscussionActive DiscussionStatus = "active"
	DiscussionEnded  DiscussionStatus = "ended"
)

// DiscussionEndReason says why a session ended; the reason is audit data and
// never gates the deposit.
type DiscussionEndReason string

const (
	EndReasonConsensus DiscussionEndReason = "consensus"
	EndReasonMaxRounds DiscussionEndReason = "max_rounds"
	EndReasonLeader    DiscussionEndReason = "leader"
	EndReasonNoIdle    DiscussionEndReason = "no_idle"
)

const (
	// DiscussionMaxRoundsCap bounds a session's round count; parity with the
	// reference discussion, which clamps every request into [1,3].
	DiscussionMaxRoundsCap = 3
	// DiscussionConclusionMax bounds one member's per-round conclusion text.
	DiscussionConclusionMax = 4000
	// DiscussionTextMax bounds the leader's consensus and technical route.
	DiscussionTextMax = 4000
	// DiscussionTopicMax bounds the topic line, which opens every prompt.
	DiscussionTopicMax = 200
)

// Deposit statuses on the KB marker.
const (
	DiscussionDepositPending   = "pending"
	DiscussionDepositDelivered = "delivered"
)

// DiscussionDeposit is the durable KB-delivery marker written at end, so the
// ended document itself is the outbox: pending until the host's Ingest call
// returns, then flipped to delivered under CAS. Recovering a crashed end just
// re-delivers the same body; the hash records what was handed over.
type DiscussionDeposit struct {
	SessionID   string `json:"session_id"`
	Hash        string `json:"hash"`
	Text        string `json:"text"`
	Status      string `json:"status"` // pending | delivered
	DeliveredAt string `json:"delivered_at,omitempty"`
}

// DiscussionConclusion is one member's conclusion for one round. The key
// (round, member) is fixed at first write: a member may revise their own
// current-round conclusion, never anyone else's and never a closed round.
type DiscussionConclusion struct {
	MemberID  string `json:"member_id"`
	Round     int    `json:"round"`
	Text      string `json:"text"`
	UpdatedAt string `json:"updated_at"`
}

// DiscussionDoc is the schema-versioned discussion document: one per team,
// holding the current session (idle when none was ever started, ended until
// the next Start replaces it). Leader records who owns the transitions.
type DiscussionDoc struct {
	Document
	SessionID      string                                  `json:"session_id"`
	Topic          string                                  `json:"topic"`
	Leader         string                                  `json:"leader"`
	Status         DiscussionStatus                        `json:"status"`
	Reason         DiscussionEndReason                     `json:"reason,omitempty"`
	Round          int                                     `json:"round"`
	MaxRounds      int                                     `json:"max_rounds"`
	Participants   []string                                `json:"participants"`
	Conclusions    map[int]map[string]DiscussionConclusion `json:"conclusions,omitempty"`
	Consensus      string                                  `json:"consensus,omitempty"`
	TechnicalRoute string                                  `json:"technical_route,omitempty"`
	Deposit        *DiscussionDeposit                      `json:"deposit,omitempty"`
	CreatedAt      string                                  `json:"created_at"`
	UpdatedAt      string                                  `json:"updated_at"`
	EndedAt        string                                  `json:"ended_at,omitempty"`
}

// Sentinel errors the transitions share; callers wrap them with the offending
// value, never inventing their own state rules.
var (
	ErrDiscussionActive    = errors.New("team: a discussion is already active")
	ErrDiscussionNotActive = errors.New("team: no active discussion")
	ErrNotParticipant      = errors.New("team: member is not a discussion participant")
	ErrWrongRound          = errors.New("team: conclusion targets a round that is not current")
	ErrRoundMaxed          = errors.New("team: discussion already at the maximum round")
	ErrRoundIncomplete     = errors.New("team: not every participant concluded the current round")
	ErrNotDiscussionLeader = errors.New("team: leader-only discussion transition")
	ErrDepositPending      = errors.New("team: previous discussion outcome not yet deposited")
	ErrInvalidDeposit      = errors.New("team: no pending discussion deposit")
	ErrDiscussionTooLong   = errors.New("team: text exceeds the discussion limit")
)

// newDiscussionDoc returns a fresh, schema-versioned document ready to Start.
func newDiscussionDoc() DiscussionDoc {
	return DiscussionDoc{Document: Document{SchemaVersion: SchemaVersion}, Status: DiscussionIdle}
}

// canonicalize gives the document one encoded form so CAS compares equal
// regardless of nil-versus-empty slices (the registry canonicalize pattern).
func (d *DiscussionDoc) canonicalize() {
	if d.Participants == nil {
		d.Participants = []string{}
	}
	if d.Conclusions == nil {
		d.Conclusions = map[int]map[string]DiscussionConclusion{}
	}
}

// Start opens a new active session, freezing participants and the round cap.
// A previous ended session whose deposit is still pending blocks the next
// start so the consensus is never overwritten out from under the KB delivery.
func (d *DiscussionDoc) Start(leader, topic string, participants []string, maxRounds int, sessionID, now string) error {
	if d.Status == DiscussionActive {
		return ErrDiscussionActive
	}
	if d.Status == DiscussionEnded && d.DepositPending() {
		return ErrDepositPending
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return fmt.Errorf("team: discussion topic is required")
	}
	if len(topic) > DiscussionTopicMax {
		return ErrDiscussionTooLong
	}
	if err := validateSessionKey(sessionID); err != nil {
		return fmt.Errorf("team: discussion session id: %w", err)
	}
	if err := validateSessionKey(leader); err != nil {
		return fmt.Errorf("team: discussion leader: %w", err)
	}
	frozen, err := dedupeValidMembers(participants)
	if err != nil {
		return err
	}
	*d = newDiscussionDoc()
	d.Status = DiscussionActive
	d.SessionID = sessionID
	d.Topic = topic
	d.Leader = leader
	d.Round = 1
	d.MaxRounds = clampMaxRounds(maxRounds)
	d.Participants = frozen
	d.Conclusions = map[int]map[string]DiscussionConclusion{1: {}}
	d.CreatedAt = now
	d.UpdatedAt = now
	return nil
}

// Submit records a participant's conclusion for the current round. The write
// is keyed (round, member), so a retried or revised submission overwrites the
// member's own entry instead of appending — idempotent by construction.
func (d *DiscussionDoc) Submit(memberID string, round int, text, now string) error {
	if d.Status != DiscussionActive {
		return ErrDiscussionNotActive
	}
	if !containsString(d.Participants, memberID) {
		return fmt.Errorf("%w: %q", ErrNotParticipant, memberID)
	}
	if round != d.Round {
		return fmt.Errorf("%w: current round is %d, got %d", ErrWrongRound, d.Round, round)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("team: a conclusion is required")
	}
	if len(text) > DiscussionConclusionMax {
		return ErrDiscussionTooLong
	}
	if d.Conclusions == nil {
		d.Conclusions = map[int]map[string]DiscussionConclusion{}
	}
	roundMap := d.Conclusions[d.Round]
	if roundMap == nil {
		roundMap = map[string]DiscussionConclusion{}
		d.Conclusions[d.Round] = roundMap
	}
	roundMap[memberID] = DiscussionConclusion{MemberID: memberID, Round: d.Round, Text: text, UpdatedAt: now}
	d.UpdatedAt = now
	return nil
}

// Advance moves a session to the next round once every participant has
// concluded the current one; a session already at the cap must End instead.
// Leader-only, mirroring the acceptance spec's Advance.
func (d *DiscussionDoc) Advance(leader, now string) error {
	if err := d.requireLeader(leader); err != nil {
		return err
	}
	if d.Status != DiscussionActive {
		return ErrDiscussionNotActive
	}
	if d.Round >= d.MaxRounds {
		return ErrRoundMaxed
	}
	if missing := d.roundMissing(); len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrRoundIncomplete, strings.Join(missing, ", "))
	}
	d.Round++
	if d.Conclusions[d.Round] == nil {
		d.Conclusions[d.Round] = map[string]DiscussionConclusion{}
	}
	d.UpdatedAt = now
	return nil
}

// roundMissing lists participants who have not yet concluded the current round.
func (d *DiscussionDoc) roundMissing() []string {
	round := d.Conclusions[d.Round]
	var missing []string
	for _, p := range d.Participants {
		if _, ok := round[p]; !ok {
			missing = append(missing, p)
		}
	}
	return missing
}

// End finishes a session. Leader-only. deposit=true makes the ended document
// carry the durable KB marker; the host decides, since only a KB-enabled host
// has anywhere to deliver. Consensus and technical route are kept verbatim so
// a later Start never erases the outcome the host already deposited.
func (d *DiscussionDoc) End(leader string, reason DiscussionEndReason, consensus, technicalRoute string, deposit bool, now string) error {
	if err := d.requireLeader(leader); err != nil {
		return err
	}
	if d.Status != DiscussionActive {
		return ErrDiscussionNotActive
	}
	switch reason {
	case EndReasonConsensus, EndReasonMaxRounds, EndReasonLeader, EndReasonNoIdle:
	default:
		return fmt.Errorf("team: invalid discussion end reason %q", reason)
	}
	consensus = strings.TrimSpace(consensus)
	technicalRoute = strings.TrimSpace(technicalRoute)
	if len(consensus) > DiscussionTextMax || len(technicalRoute) > DiscussionTextMax {
		return ErrDiscussionTooLong
	}
	d.Status = DiscussionEnded
	d.Reason = reason
	d.Consensus = consensus
	d.TechnicalRoute = technicalRoute
	d.EndedAt = now
	d.UpdatedAt = now
	// A deposit is only meaningful when the leader left an outcome; ending a
	// session with no consensus or route (manual close) carries nothing to KB.
	if deposit && (consensus != "" || technicalRoute != "") {
		if body := d.DepositText(); body != "" {
			sum := sha256.Sum256([]byte(body))
			d.Deposit = &DiscussionDeposit{
				SessionID: d.SessionID, Hash: hex.EncodeToString(sum[:]),
				Text: body, Status: DiscussionDepositPending,
			}
		}
	}
	return nil
}

func (d *DiscussionDoc) requireLeader(leader string) error {
	if d.Leader == "" || leader != d.Leader {
		return fmt.Errorf("%w: %q", ErrNotDiscussionLeader, leader)
	}
	return nil
}

// MarkDeposited flips a pending deposit to delivered; only the host calls it
// after its KB Ingest call returned.
func (d *DiscussionDoc) MarkDeposited(now string) error {
	if d.Deposit == nil || d.Deposit.Status != DiscussionDepositPending {
		return ErrInvalidDeposit
	}
	d.Deposit.Status = DiscussionDepositDelivered
	d.Deposit.DeliveredAt = now
	d.UpdatedAt = now
	return nil
}

// DepositPending reports whether the current session ended but its outcome has
// not reached the knowledge base yet.
func (d *DiscussionDoc) DepositPending() bool {
	return d.Status == DiscussionEnded && d.Deposit != nil && d.Deposit.Status == DiscussionDepositPending
}

// DepositText is the canonical body the host ingests — deterministic, so a
// crash-and-redeliver hands the KB byte-identical text to dedup on.
func (d *DiscussionDoc) DepositText() string {
	var b strings.Builder
	if s := strings.TrimSpace(d.Topic); s != "" {
		fmt.Fprintf(&b, "讨论主题: %s\n", s)
	}
	if len(d.Participants) > 0 {
		fmt.Fprintf(&b, "参与成员: %s\n", strings.Join(d.Participants, ", "))
	}
	if s := strings.TrimSpace(d.Consensus); s != "" {
		fmt.Fprintf(&b, "\n共识: %s", s)
	}
	if s := strings.TrimSpace(d.TechnicalRoute); s != "" {
		fmt.Fprintf(&b, "\n技术路线: %s", s)
	}
	return strings.TrimSpace(b.String())
}

// Summary renders a compact human view of the session for round prompts and
// status reads. Conclusion bodies are truncated so a summary never balloons a
// prompt.
func (d *DiscussionDoc) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "主题: %s\n", d.Topic)
	if d.SessionID != "" {
		fmt.Fprintf(&b, "会话: %s\n", d.SessionID)
	}
	state := string(d.Status)
	if d.Status == DiscussionEnded {
		state = "已结束(" + string(d.Reason) + ")"
	}
	fmt.Fprintf(&b, "状态: %s\n", state)
	fmt.Fprintf(&b, "轮次: %d/%d\n", d.Round, d.MaxRounds)
	if len(d.Participants) > 0 {
		fmt.Fprintf(&b, "参与成员: %s\n", strings.Join(d.Participants, ", "))
	}
	if current := d.Conclusions[d.Round]; len(current) > 0 {
		fmt.Fprintf(&b, "本轮结论:\n")
		for _, member := range d.Participants {
			if c, ok := current[member]; ok {
				fmt.Fprintf(&b, "- %s: %s\n", member, shortLine(c.Text, 200))
			}
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// DiscussionStore persists one discussion document per team under a team data
// dir (files discussion-<team>.json). All transitions are load-modify-CAS, so
// a concurrent submit or advance surfaces as a retried CAS rather than a lost
// write; a corrupt or schema-mismatched file fails closed.
type DiscussionStore struct {
	store *FileStore
}

// NewDiscussionStore returns a store rooted at the caller's FileStore — the
// same data dir that holds team.json, so the discussion colocates with the
// registry it belongs to.
func NewDiscussionStore(store *FileStore) *DiscussionStore {
	return &DiscussionStore{store: store}
}

// DiscussionFile returns the per-team discussion document path; the team name
// is validated so a team can never name a file outside the flat namespace.
func DiscussionFile(teamName string) (string, error) {
	if err := validateSessionKey(teamName); err != nil {
		return "", err
	}
	return fmt.Sprintf("discussion-%s.json", teamName), nil
}

// Load returns the team's current discussion document. An absent file is the
// zero document (idle), not an error; a corrupt file is an error, never a
// silent reset.
func (s *DiscussionStore) Load(teamName string) (DiscussionDoc, error) {
	path, err := DiscussionFile(teamName)
	if err != nil {
		return DiscussionDoc{}, err
	}
	doc, absent, err := s.load(path)
	if err != nil {
		return DiscussionDoc{}, err
	}
	if absent {
		return newDiscussionDoc(), nil
	}
	return doc, nil
}

// Update runs mutate on the team's current document and publishes it under
// CAS, retrying on conflict. mutate must leave the document schema-versioned;
// an unchanged document is not rewritten. The loaded document is deep-cloned
// into the CAS expected snapshot before mutate runs, so an in-place mutation
// of a nested map never pollutes the value the CAS compares against.
func (s *DiscussionStore) Update(teamName string, mutate func(*DiscussionDoc) error) error {
	path, err := DiscussionFile(teamName)
	if err != nil {
		return err
	}
	for {
		cur, absent, err := s.load(path)
		if err != nil {
			return err
		}
		expected := cloneDiscussionDoc(cur)
		if err := mutate(&cur); err != nil {
			return err
		}
		cur.SchemaVersion = SchemaVersion
		if !absent && documentsEqual(&expected, &cur) {
			return nil
		}
		if absent {
			err = s.store.CompareAndSwap(path, nil, &cur)
		} else {
			err = s.store.CompareAndSwap(path, &expected, &cur)
		}
		if errors.Is(err, ErrCASConflict) {
			continue
		}
		return err
	}
}

// cloneDiscussionDoc deep-copies a document through its JSON form so a caller
// mutating the copy can never alias the original's nested maps or slices.
func cloneDiscussionDoc(doc DiscussionDoc) DiscussionDoc {
	raw, err := json.Marshal(&doc)
	if err != nil {
		return doc
	}
	var out DiscussionDoc
	_ = json.Unmarshal(raw, &out)
	return out
}

func (s *DiscussionStore) load(path string) (DiscussionDoc, bool, error) {
	var doc DiscussionDoc
	err := s.store.Load(path, &doc)
	if err == nil {
		return doc, false, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return DiscussionDoc{}, true, nil
	}
	return DiscussionDoc{}, false, err
}

func documentsEqual(a, b *DiscussionDoc) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(ab) == string(bb)
}

func containsString(xs []string, want string) bool {
	return slices.Contains(xs, want)
}

func clampMaxRounds(maxRounds int) int {
	if maxRounds < 1 {
		return 1
	}
	if maxRounds > DiscussionMaxRoundsCap {
		return DiscussionMaxRoundsCap
	}
	return maxRounds
}

func dedupeValidMembers(members []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(members))
	for _, m := range members {
		m = strings.TrimSpace(m)
		if err := validateSessionKey(m); err != nil {
			return nil, fmt.Errorf("team: discussion participant %q: %w", m, err)
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("team: at least one participant is required")
	}
	return out, nil
}

func shortLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
