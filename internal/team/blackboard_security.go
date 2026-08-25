package team

import (
	"regexp"
	"strings"
)

// BoardNamespace is the path grammar for board ids (route §6.1): "shared"
// plus per-member "private/<member>". The service resolves ids to paths,
// so clients can never pass permission bits or traversal segments.
type BoardNamespace string

const (
	NamespaceShared  BoardNamespace = BoardShared
	NamespacePrivate BoardNamespace = BoardPrivatePrefix
)

// ValidBoardID rejects traversal and escape: an id must be "shared" or
// "private/<member>" with a non-empty, slash-free member id. Anything else
// fails closed before it reaches the store.
func ValidBoardID(boardID string) bool {
	if boardID == BoardShared {
		return true
	}
	m, ok := PrivateBoard(boardID)
	return ok && !strings.Contains(m, "/") && m != "." && m != ".."
}

// CheckBoardAccess is the service-layer ACL (route §6.2). The store repeats
// the same boundary, so a client that bypasses this layer never reaches
// persistence. Writes to shared boards need a stamped member; private
// boards are owner-only for reads and writes; an empty identity is always
// denied, and the target's content is never revealed.
func CheckBoardAccess(boardID string, id Identity, write bool) error {
	if !ValidBoardID(boardID) {
		return ErrForbidden
	}
	if id.MemberID == "" {
		return ErrForbidden
	}
	if owner, private := PrivateBoard(boardID); private && owner != id.MemberID {
		return ErrForbidden
	}
	return nil
}

// RequireManagement gates the delete/archive channel (route §6.2): only
// the leader role may destroy or archive board state. Member writes never
// reach these operations.
func RequireManagement(id Identity) error {
	if id.MemberID == "" || id.Role != "leader" {
		return ErrForbidden
	}
	return nil
}

// Stamp resolves the server-side identity for one member window (route
// §6.2). The binding is the single source of truth: any client-supplied
// claim is discarded and reported as forged, so a mismatch is auditable
// instead of silently accepted.
func Stamp(bind BindRecord, claimed Identity, role, agent string) (Identity, bool) {
	if claimed.MemberID != bind.MemberID || claimed.Generation != bind.Generation {
		return Identity{}, true
	}
	return Identity{
		MemberID:   bind.MemberID,
		Role:       role,
		Agent:      agent,
		Generation: bind.Generation,
	}, false
}

// RedactKind names the deny-list category a secret matched (route §6.3).
type RedactKind string

const (
	RedactProviderKey RedactKind = "provider-key"
	RedactAWSKey      RedactKind = "aws-key"
	RedactPrivateKey  RedactKind = "private-key"
	RedactCredential  RedactKind = "credential"
)

// RedactPattern is one deny-list entry: a category and the regexp that
// finds it. Patterns are anchored to key shapes, never to surrounding
// prose, so a key can't slip through by changing context.
type RedactPattern struct {
	Kind   RedactKind
	Regexp *regexp.Regexp
}

// RedactHit records one replacement for audit; the original text is never
// kept, so later boundaries cannot restore it.
type RedactHit struct {
	Kind  RedactKind
	Start int
}

// Redactor applies deny-list patterns at every materialization boundary
// (route §6.3): persist, inject and summarize each run the same Redact
// call, so a secret cannot cross to a later boundary. It is immutable and
// safe for concurrent use.
type Redactor struct {
	patterns []RedactPattern
}

// NewRedactor builds a redactor from explicit patterns; nil uses the
// default deny-list.
func NewRedactor(patterns []RedactPattern) *Redactor {
	if patterns == nil {
		patterns = DefaultRedactPatterns
	}
	return &Redactor{patterns: patterns}
}

// DefaultRedactPatterns is the built-in deny-list (route §6.3): provider
// keys, AWS access keys, private-key blocks and credential assignments.
var DefaultRedactPatterns = []RedactPattern{
	{RedactProviderKey, regexp.MustCompile(`sk-[A-Za-z0-9]{16,}`)},
	{RedactAWSKey, regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{RedactPrivateKey, regexp.MustCompile(`-----BEGIN (?:RSA|OPENSSH|EC) PRIVATE KEY-----`)},
	{RedactCredential, regexp.MustCompile(`(?i)\b(?:password|passwd|token|secret|api[_-]?key)\b\s*[=:]\s*[^\s,;"']+`)},
}

// Redact replaces every deny-list hit with [REDACTED:<kind>] and returns
// the hits for audit. No hit leaves the input byte-identical; the output
// never contains the matched secret. Re-scanning from the start keeps the
// replacement inert: a marker never matches a later pattern.
func (r *Redactor) Redact(s string) (string, []RedactHit) {
	var hits []RedactHit
	out := s
	for _, p := range r.patterns {
		for {
			loc := p.Regexp.FindStringIndex(out)
			if loc == nil {
				break
			}
			hits = append(hits, RedactHit{Kind: p.Kind, Start: loc[0]})
			out = out[:loc[0]] + "[REDACTED:" + string(p.Kind) + "]" + out[loc[1]:]
		}
	}
	return out, hits
}
