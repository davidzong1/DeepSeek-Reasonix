package workspacelease

// A writer queued behind another used to read as "another session". Publishing a
// holder record beside the acquired locks lets it name that writer; the record is
// diagnostic, so no lease decision reads it.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"reasonix/internal/filelock"
)

const (
	// holderRecordSuffix names the sidecar beside a lock file. It lives in the
	// user-level lease directory, never in the workspace.
	holderRecordSuffix = ".holder"
	// holderLabelLimit keeps one label short enough for a single notice line.
	holderLabelLimit = 64
	// queueLockSuffix marks the transient ordering lock. It is taken for every
	// queued acquisition and released immediately, so publishing beside it would
	// buy nothing and cost a record per acquisition.
	queueLockSuffix = ".queue"
	// HolderModeExclusive and HolderModeShared name the mode a holder took.
	HolderModeExclusive = "exclusive"
	HolderModeShared    = "shared"
)

// HolderInfo describes the writer a queued acquisition is waiting behind.
type HolderInfo struct {
	Label string
	PID   int
	Mode  string
	Since time.Time
}

// holderRecord is the on-disk shape. Token lets the writer that published a
// record delete exactly its own: two Owners can hold the same lock in shared
// mode, and the last one still holding keeps the record until it releases.
type holderRecord struct {
	Label string `json:"label"`
	PID   int    `json:"pid"`
	Mode  string `json:"mode"`
	Since string `json:"since"`
	Token string `json:"token"`
}

// SetIdentity labels this Owner in the holder records it publishes, so a queued
// writer can name it. It is diagnostic only, safe to call while holding, and an
// unlabeled Owner publishes nothing.
func (o *Owner) SetIdentity(label string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.identity = sanitizeHolderLabel(label)
	o.mu.Unlock()
}

// WaitingOn reports the holder observed when this Owner last queued for a lock.
// ok is false when nothing was observed: no identity was published, the record
// was unreadable, or the wait has since been served.
func (o *Owner) WaitingOn() (HolderInfo, bool) {
	if o == nil {
		return HolderInfo{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lease.blockedBy == nil {
		return HolderInfo{}, false
	}
	return *o.lease.blockedBy, true
}

// observeHolder keeps a holder record beside lockPath for as long as this
// acquisition holds the lock. Write failures are silent: the record only
// improves a notice.
func (o *Owner) observeHolder(lockPath string, mode filelock.Mode, release func()) func() {
	if release == nil || !o.publishesHolderRecord(lockPath, mode) {
		return release
	}
	record := o.newHolderRecord(mode)
	if record == nil {
		return release
	}
	writeHolderRecord(lockPath, record)
	var once sync.Once
	return func() {
		once.Do(func() {
			// Remove before releasing, so a waiter that takes the lock next does
			// not read this record as its holder. An exclusively held lock admits
			// no other publisher, so only shared records need the token check.
			removeHolderRecord(lockPath, record.Token, mode == filelock.ModeExclusive)
			release()
		})
	}
}

// publishesHolderRecord reports whether a waiter can read this acquisition's
// record. Publishing beside every lock a hold touches would cost a record per
// ancestor directory for no diagnostic gain, so this keeps the two shapes that
// can actually block a writer: every exclusive acquisition, and the workspace
// root's own lock — a file writer's shared hold there is what a workspace-wide
// writer queues behind.
func (o *Owner) publishesHolderRecord(lockPath string, mode filelock.Mode) bool {
	if strings.HasSuffix(lockPath, queueLockSuffix) {
		return false
	}
	return mode == filelock.ModeExclusive || lockPath == o.holderRootPath
}

// canonicalRootLockPath is the lock file the workspace root itself uses. It is
// derived exactly as acquireCompatibilityRoots derives it, so the root domain
// published here is the one a queued writer reads.
func (o *Owner) canonicalRootLockPath() string {
	roots := append(ancestorDirectories(o.canonical), ancestorDirectories(o.compatibility)...)
	for _, root := range orderedWorkspaceRoots(roots) {
		if normalizeIdentityPath(root) == o.canonical {
			return workspaceLockPath(o.lockDir, root)
		}
	}
	return ""
}

// newHolderRecord snapshots this Owner's identity for one acquisition. It
// returns nil when no identity is set, so unlabeled sessions pay nothing.
func (o *Owner) newHolderRecord(mode filelock.Mode) *holderRecord {
	o.mu.Lock()
	label := o.identity
	o.mu.Unlock()
	if label == "" {
		return nil
	}
	return &holderRecord{
		Label: label,
		PID:   os.Getpid(),
		Mode:  holderModeName(mode),
		Since: time.Now().UTC().Format(time.RFC3339Nano),
		Token: newHolderToken(),
	}
}

// noteBlockedHolder records who held lockPath when this acquisition first had to
// queue. Reading happens while queued, never from State or WaitingOn, so the
// accessors stay I/O free.
func (o *Owner) noteBlockedHolder(lockPath string) {
	info, ok := readHolderRecord(lockPath)
	if !ok {
		return
	}
	o.mu.Lock()
	o.lease.blockedBy = &info
	o.mu.Unlock()
}

// clearBlockedHolder drops an observation once an acquisition succeeds, so a
// served wait does not keep naming a holder.
func (o *Owner) clearBlockedHolder() {
	o.mu.Lock()
	o.lease.blockedBy = nil
	o.mu.Unlock()
}

func holderRecordPath(lockPath string) string {
	return lockPath + holderRecordSuffix
}

func holderModeName(mode filelock.Mode) string {
	if mode == filelock.ModeShared {
		return HolderModeShared
	}
	return HolderModeExclusive
}

func sanitizeHolderLabel(label string) string {
	label = strings.Join(strings.Fields(label), " ")
	if len(label) > holderLabelLimit {
		label = strings.TrimSpace(label[:holderLabelLimit])
	}
	return label
}

func newHolderToken() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf[:])
}

func writeHolderRecord(lockPath string, record *holderRecord) {
	raw, err := json.Marshal(record)
	if err != nil {
		return
	}
	_ = os.WriteFile(holderRecordPath(lockPath), raw, 0o600)
}

// removeHolderRecord retires a record this Owner published. exclusive says the
// lock admits no other publisher while this Owner holds it, which skips the
// read-and-compare a shared record needs.
func removeHolderRecord(lockPath, token string, exclusive bool) {
	path := holderRecordPath(lockPath)
	if !exclusive {
		raw, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var record holderRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return
		}
		if token == "" || record.Token != token {
			return
		}
	}
	_ = os.Remove(path)
}

func readHolderRecord(lockPath string) (HolderInfo, bool) {
	raw, err := os.ReadFile(holderRecordPath(lockPath))
	if err != nil {
		return HolderInfo{}, false
	}
	var record holderRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return HolderInfo{}, false
	}
	label := sanitizeHolderLabel(record.Label)
	if label == "" {
		return HolderInfo{}, false
	}
	info := HolderInfo{Label: label, PID: record.PID, Mode: record.Mode}
	if since, err := time.Parse(time.RFC3339Nano, record.Since); err == nil {
		info.Since = since
	}
	return info, true
}
