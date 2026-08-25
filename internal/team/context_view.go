package team

import (
	"context"
	"errors"
	"sync"
)

// BoardCursor is a member's read position on one board (route §3.2). LastSeq
// 0 means the member has never read: the next Advance is the first full
// delta. The member holds the cursor; the store's board_cursors table is
// the server-side mirror advanced through AdvanceCursor (route §2.2).
type BoardCursor struct {
	BoardID    string
	ConsumerID string
	LastSeq    int64
	Epoch      uint64
}

// CursorStore persists member cursors. It is the member-side half of the
// cursor contract; the memory implementation stands in until a durable
// home lands (P4). LoadCursor returns a zero cursor when the member has no
// saved position.
type CursorStore interface {
	LoadCursor(boardID, consumerID string) (BoardCursor, error)
	SaveCursor(cursor BoardCursor) error
}

// MemoryCursorStore is an in-memory CursorStore keyed by (board, consumer).
type MemoryCursorStore struct {
	mu    sync.Mutex
	byKey map[string]BoardCursor
}

// NewMemoryCursorStore returns an empty cursor store.
func NewMemoryCursorStore() *MemoryCursorStore {
	return &MemoryCursorStore{byKey: map[string]BoardCursor{}}
}

func (s *MemoryCursorStore) LoadCursor(boardID, consumerID string) (BoardCursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byKey[boardID+"\x00"+consumerID], nil
}

func (s *MemoryCursorStore) SaveCursor(cursor BoardCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey[cursor.BoardID+"\x00"+cursor.ConsumerID] = cursor
	return nil
}

// ViewKey addresses one cache entry: (board, member, level, epoch,
// source_seq) (route §3.2). The epoch member makes an epoch change miss on
// every level at once — whole keys invalidate, never partial reuse.
type ViewKey struct {
	BoardID   string
	MemberID  string
	Level     ViewLevel
	Epoch     uint64
	SourceSeq int64
}

// viewRef addresses the last-known view used by the stale fallback.
type viewRef struct {
	boardID  string
	memberID string
	level    ViewLevel
}

// indexRef addresses the accumulated task-count map behind the L0 index.
type indexRef struct {
	boardID  string
	memberID string
	epoch    uint64
}

const maxCachedViews = 64

// ViewCache holds rendered views, the last-known view per (board, member,
// level) for the stale fallback, and the accumulated L0 task counts. It is
// not concurrency-safe: one member's turn assembles on a single goroutine.
type ViewCache struct {
	entries map[ViewKey]BoardView
	last    map[viewRef]BoardView
	indexes map[indexRef]map[string]int
}

// NewViewCache returns an empty cache.
func NewViewCache() *ViewCache {
	return &ViewCache{
		entries: map[ViewKey]BoardView{},
		last:    map[viewRef]BoardView{},
		indexes: map[indexRef]map[string]int{},
	}
}

func (c *ViewCache) Get(key ViewKey) (BoardView, bool) {
	v, ok := c.entries[key]
	return v, ok
}

func (c *ViewCache) Put(key ViewKey, v BoardView) {
	if len(c.entries) >= maxCachedViews {
		c.entries = map[ViewKey]BoardView{}
	}
	c.entries[key] = v
	c.last[viewRef{key.BoardID, key.MemberID, key.Level}] = v
}

// Last returns the most recent view for (board, member, level) whatever
// its epoch — the stale-fallback source (§3.2).
func (c *ViewCache) Last(boardID, memberID string, level ViewLevel) (BoardView, bool) {
	v, ok := c.last[viewRef{boardID, memberID, level}]
	return v, ok
}

// Index returns the member's accumulated task-count map for (board, epoch);
// an epoch change starts over. The caller mutates the returned map and L0
// renders from it — the counts are the materialized part of the index.
func (c *ViewCache) Index(boardID, memberID string, epoch uint64) map[string]int {
	ref := indexRef{boardID, memberID, epoch}
	if m, ok := c.indexes[ref]; ok {
		return m
	}
	m := map[string]int{}
	c.indexes[ref] = m
	return m
}

// InvalidateEpoch drops every cached entry of the member's epoch; the
// last-known views survive so the stale fallback still has content.
func (c *ViewCache) InvalidateEpoch(boardID, memberID string, epoch uint64) {
	for key := range c.entries {
		if key.BoardID == boardID && key.MemberID == memberID && key.Epoch == epoch {
			delete(c.entries, key)
		}
	}
	delete(c.indexes, indexRef{boardID, memberID, epoch})
}

// Assembler builds a member's board context for one turn (route §7): load
// the member cursor, read the delta, render L0/L1 (L2 on request), persist
// the new position. A read failure degrades to the last-known views marked
// Stale — a member never gets an empty board (§3.2).
type Assembler struct {
	boardID  string
	identity Identity
	store    BoardStore
	cursors  CursorStore
	cache    *ViewCache
	builder  *ViewBuilder
}

// NewAssembler wires one member's view pipeline. builder's epoch is the
// board epoch: a new epoch plus InvalidateEpoch starts the member fresh.
func NewAssembler(boardID string, identity Identity, store BoardStore, cursors CursorStore, cache *ViewCache, builder *ViewBuilder) *Assembler {
	return &Assembler{
		boardID: boardID, identity: identity,
		store: store, cursors: cursors, cache: cache, builder: builder,
	}
}

// Assembly is one member turn's board context (route §7). NeedResync marks
// a cursor the store could not serve continuously, so the turn content was
// rebuilt from an earlier point (§2.3).
type Assembly struct {
	Cursor     BoardCursor
	L0         BoardView
	L1         BoardView
	L2         BoardView
	NeedResync bool
}

// Advance renders the next turn. wantDetail adds L2. On success the member
// cursor and the store's cursor mirror advance to the last served seq.
func (a *Assembler) Advance(ctx context.Context, wantDetail bool) (Assembly, error) {
	cur, err := a.cursors.LoadCursor(a.boardID, a.identity.MemberID)
	if err != nil {
		return Assembly{Cursor: cur}, err
	}
	page, err := a.store.ReadAfter(ctx, a.boardID, cur.LastSeq, Filter{Stamped: a.identity, Limit: viewPageSize})
	if err != nil {
		return a.staleFallback(cur, err)
	}
	resync := page.NeedResync
	after := cur.LastSeq
	if resync {
		page, err = a.store.ReadAfter(ctx, a.boardID, 0, Filter{Stamped: a.identity, Limit: viewPageSize})
		if err != nil {
			return a.staleFallback(cur, err)
		}
		after = 0
		// §2.3: need_resync 要求丢弃本地缓存重新拉取,整 epoch 失效
		// (游标洞内的旧视图不能与新 tail 混用)。
		a.cache.InvalidateEpoch(a.boardID, a.identity.MemberID, a.builder.epoch)
	}
	// 空 delta 时 store 返回 NextSeq=0,直接持久化会把游标倒退到首次态;
	// 保持原位,下次仍从同一位置续读(store 契约:NextSeq=0 不分首次/空板)。
	last := page.NextSeq
	if len(page.Events) == 0 {
		last = cur.LastSeq
	}
	latest := last
	l0 := a.cached(ViewLevelIndex, latest, func() BoardView {
		counts := a.cache.Index(a.boardID, a.identity.MemberID, a.builder.epoch)
		for _, ev := range page.Events {
			counts[string(ev.TaskID)]++
		}
		return a.builder.L0(counts, page.Events)
	})
	// L1/L2 的内容由读起点(after)决定:按 latest 建键会在空 delta 时
	// 命中上一轮的旧视图,把已消费事件再渲染一遍。
	l1 := a.cached(ViewLevelDelta, after, func() BoardView {
		return a.builder.L1(after, page.Events)
	})
	var l2 BoardView
	if wantDetail {
		l2 = a.cached(ViewLevelDetail, after, func() BoardView {
			return a.builder.L2(foldCheckpoints(page.Events, a.builder.epoch), nil)
		})
	}
	// 空 delta 时 store 返回 NextSeq=0,直接持久化会把游标倒退到首次态;
	// 保持原位,下次仍从同一位置续读(store 契约:NextSeq=0 不分首次/空板)。
	next := BoardCursor{
		BoardID: a.boardID, ConsumerID: a.identity.MemberID,
		LastSeq: last, Epoch: a.builder.epoch,
	}
	if err := a.cursors.SaveCursor(next); err != nil {
		return Assembly{Cursor: next, L0: l0, L1: l1, L2: l2}, err
	}
	if err := a.advanceMirror(ctx, last); err != nil {
		return Assembly{Cursor: next, L0: l0, L1: l1, L2: l2}, err
	}
	return Assembly{Cursor: next, L0: l0, L1: l1, L2: l2, NeedResync: resync}, nil
}

// advanceMirror mirrors the member cursor into the store's board_cursors
// table. A stale generation or backwards mirror is tolerated — the read
// itself succeeded and the mirror is a recovery aid, not the read source.
func (a *Assembler) advanceMirror(ctx context.Context, lastSeq int64) error {
	err := a.store.AdvanceCursor(ctx, CursorUpdate{
		BoardID: a.boardID, ConsumerID: a.identity.MemberID,
		Generation: a.identity.Generation, LastSeq: lastSeq,
	})
	if errors.Is(err, ErrStaleGeneration) || errors.Is(err, ErrCursorBackwards) {
		return nil
	}
	return err
}

// cached returns the view for the key, building and storing it on miss.
func (a *Assembler) cached(level ViewLevel, sourceSeq int64, build func() BoardView) BoardView {
	key := ViewKey{
		BoardID: a.boardID, MemberID: a.identity.MemberID, Level: level,
		Epoch: a.builder.epoch, SourceSeq: sourceSeq,
	}
	if v, ok := a.cache.Get(key); ok {
		return v
	}
	v := build()
	a.cache.Put(key, v)
	return v
}

// staleFallback returns the member's last-known views marked Stale, or the
// bare cursor when nothing was ever rendered. The caller keeps the error:
// stale content is a degradation signal, not a success (§3.2).
func (a *Assembler) staleFallback(cur BoardCursor, err error) (Assembly, error) {
	out := Assembly{Cursor: cur}
	mark := func(level ViewLevel) BoardView {
		v, ok := a.cache.Last(a.boardID, a.identity.MemberID, level)
		if !ok {
			return BoardView{}
		}
		v.Stale = true
		return v
	}
	out.L0 = mark(ViewLevelIndex)
	out.L1 = mark(ViewLevelDelta)
	out.L2 = mark(ViewLevelDetail)
	return out, err
}
