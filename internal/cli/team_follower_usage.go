package cli

import (
	"context"
	"time"

	"reasonix/internal/team"
)

const (
	// followerUsageReadInterval throttles a follower's read of the writer's
	// published usage. The status band reads these methods at frame rate — every
	// message re-renders — so an unthrottled read would stat and parse one small
	// document dozens of times a second for numbers that move once per turn.
	followerUsageReadInterval = time.Second
	// followerUsageTTL is how long a published observation stays renderable. It
	// is a writer-liveness timeout, not a data timeout: the writer republishes
	// every memberUsagePublishInterval while it runs, so a document older than
	// this means the writer stopped, and the numbers must disappear rather than
	// pose as current.
	followerUsageTTL = 30 * time.Second
)

// followerUsageReader is the follower's read-only view of the writer's usage
// channel. An interface so a test can count reads, and read-only by shape: there
// is no publish method to call.
type followerUsageReader interface {
	ReadUsage(ctx context.Context) (team.OwnerUsage, bool, error)
}

// ownerUsageReader reads one member's published usage from canonical owner
// storage — the same directory the follower already polls for the history
// identity, so both observations describe the same writer by construction.
type ownerUsageReader struct {
	owners *team.OwnerStore
	key    team.OwnerKey
}

// newFollowerUsageReader binds a follower's usage channel to one owner key. A
// nil store yields a nil reader: a host without team data has nothing published,
// which is the same answer as an absent document.
func newFollowerUsageReader(owners *team.OwnerStore, key team.OwnerKey) followerUsageReader {
	if owners == nil {
		return nil
	}
	return ownerUsageReader{owners: owners, key: key}
}

func (r ownerUsageReader) ReadUsage(ctx context.Context) (team.OwnerUsage, bool, error) {
	if r.owners == nil {
		return team.OwnerUsage{}, false, nil
	}
	return r.owners.ReadUsage(ctx, r.key)
}

// usageSnapshot answers the band's telemetry reads from the writer's published
// observation: the document when it is both present and fresh, and nothing
// otherwise. Freshness is decided on every call, so the numbers vanish the
// moment the document ages past the TTL rather than at the next read; the read
// itself is throttled, because the band calls these methods at frame rate.
//
// Total by construction: an absent reader, an absent document, a read fault and
// a stale document all answer "nothing" instead of an error — the status band
// reads some of these methods without a recover of its own, and telemetry is
// optional state that must never fail a frame.
func (b *memberFollowerBackend) usageSnapshot() (team.OwnerUsage, bool) {
	if b == nil || b.usage == nil {
		return team.OwnerUsage{}, false
	}
	b.usageMu.Lock()
	// A host that refreshes this snapshot off the frame path owns it, so the band
	// serves what that read installed. The throttle that follows is the fallback
	// for a host with no refresher.
	if !b.usageTicked {
		if now := time.Now(); b.usageDoc == nil || now.Sub(b.usageReadAt) >= followerUsageReadInterval {
			doc, ok, err := b.usage.ReadUsage(context.Background())
			b.usageReadAt = now
			if err != nil || !ok {
				b.usageDoc = nil
			} else {
				b.usageDoc = &doc
			}
		}
	}
	doc := b.usageDoc
	b.usageMu.Unlock()
	if doc == nil || !doc.Fresh(time.Now(), followerUsageTTL) {
		return team.OwnerUsage{}, false
	}
	return *doc, true
}

// refreshUsage reads the writer's document and installs it — off the Update
// goroutine, which is the point: the status band answers these same numbers at
// frame rate, and one small stat plus parse per second is still disk I/O on the
// path that draws the frame. The roster tick refreshes every second, so the band
// finds the observation in memory and never reads here.
//
// The document's own stamp still decides freshness (usageSnapshot): a writer that
// stops publishing is hidden by the TTL, not by who did the read.
func (b *memberFollowerBackend) refreshUsage(ctx context.Context) {
	if b == nil || b.usage == nil {
		return
	}
	doc, ok, err := b.usage.ReadUsage(ctx) // outside the lock: this is the I/O
	now := time.Now()
	b.usageMu.Lock()
	b.usageTicked = true
	b.usageReadAt = now
	if err != nil || !ok {
		b.usageDoc = nil
	} else {
		b.usageDoc = &doc
	}
	b.usageMu.Unlock()
}
