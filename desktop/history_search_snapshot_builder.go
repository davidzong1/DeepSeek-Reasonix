package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/history"
	"reasonix/internal/historycatalog"
	"reasonix/internal/provider"
	"reasonix/internal/retrieval"
)

type historySearchSnapshotBuild struct {
	ctx          context.Context
	app          *App
	store        *readSnapshotStore
	snapshot     *readSnapshot
	catalog      *historycatalog.Catalog
	request      HistorySearchRequest
	active       string
	meta         HistorySearchPage
	fence        *readSourceFence
	overlays     map[string]catalogRuntimeOverlay
	terms        []string
	cutoff       time.Time
	messages     []provider.Message
	loadedPath   string
	loadedDigest string
	covered      bool
	intact       bool
}

func (a *App) buildHistorySearchSnapshot(ctx context.Context, store *readSnapshotStore, snap *readSnapshot, req HistorySearchRequest, targetPath, active string) error {
	status := a.GetHistoryIndexStatus()
	meta := HistorySearchPage{Status: status, Revision: status.Revision, Partial: status.State != "ready" || status.Pending > 0}
	catalog := history.SharedCatalog()
	if catalog == nil || req.Query == "" {
		snap.metadata, _ = json.Marshal(meta)
		return nil
	}
	candidates := &readSnapshot{}
	defer store.dispose(candidates)
	roots := historySearchRootFilter(a, req)
	if targetPath != "" {
		roots = nil
	}
	search := historycatalog.SearchRequest{Query: req.Query, Scope: req.Scope, WorkspaceRoot: req.WorkspaceRoot, SessionPath: targetPath, Kinds: req.Kinds, ToolName: req.ToolName, Roots: roots}
	if err := catalog.CaptureSearch(ctx, search, func(row historycatalog.Candidate) error { return store.append(ctx, candidates, row) }); err != nil {
		return err
	}
	terms, err := retrieval.QueryTerms(req.Query)
	if err != nil {
		return err
	}
	fence, err := a.newReadSourceFence(store, snap)
	if err != nil {
		return err
	}
	_, overlays := a.catalogRuntimeOverlays()
	build := &historySearchSnapshotBuild{ctx: ctx, app: a, store: store, snapshot: snap, catalog: catalog, request: req, active: active, meta: meta, fence: fence, overlays: overlays, terms: terms, cutoff: time.Now()}
	if err := candidates.walk(ctx, build.visit); err != nil {
		return err
	}
	snap.validate = fence.freeze()
	snap.metadata, err = json.Marshal(build.meta)
	return err
}

func (b *historySearchSnapshotBuild) visit(encoded []byte) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	var row historycatalog.Candidate
	if err := json.Unmarshal(encoded, &row); err != nil {
		return err
	}
	overlay := b.overlays[sessionRuntimeKey(row.SessionPath)]
	if !historyStatusMatches(b.request.Status, overlay.open, row.SessionPath == b.active) || !historyTimeMatchesAt(row.LastActivityAt, b.request.TimeFilter, b.cutoff) {
		return nil
	}
	if b.loadedPath != row.SessionPath {
		if err := b.loadSource(row.SessionPath); err != nil {
			return err
		}
	}
	if b.covered {
		return nil
	}
	if !b.intact || row.ContentDigest == "" || b.loadedDigest != row.ContentDigest {
		b.catalog.EnqueueExisting(b.ctx, row.SessionPath)
		b.meta.Partial = true
		return nil
	}
	text, ok := desktopHistoryText(b.messages, row)
	if !ok {
		b.catalog.EnqueueExisting(b.ctx, row.SessionPath)
		b.meta.Partial = true
		return nil
	}
	if err := b.fence.add(b.ctx, row.SessionPath); err != nil {
		return err
	}
	hit := HistorySearchHit{SessionPath: row.SessionPath, SessionID: strings.TrimSuffix(filepath.Base(row.SessionPath), filepath.Ext(row.SessionPath)), Source: row.Source, MessageIndex: row.MessageIndex, PartIndex: row.PartIndex, ContentDigest: b.loadedDigest, Role: row.Role, Kind: row.Kind, ToolName: row.ToolName, Snippet: retrieval.MakeSnippet(text, b.request.Query, b.terms, 240), Score: row.Score, SessionTitle: row.SessionTitle, TopicTitle: row.TopicTitle, WorkspaceRoot: row.WorkspaceRoot, LastActivityAt: row.LastActivityAt, Open: overlay.open, Running: overlay.running, Current: row.SessionPath == b.active}
	return b.store.append(b.ctx, b.snapshot, hit)
}

func (b *historySearchSnapshotBuild) loadSource(path string) error {
	ctx := b.ctx
	b.loadedPath, b.loadedDigest = path, ""
	b.covered, b.intact, b.messages = false, false, nil
	if sessions := b.app.sessionCatalog.Load(); sessions != nil {
		record, ok, err := sessions.GetSession(ctx, path)
		if err != nil {
			return err
		}
		b.covered = ok && record.RecoveryCopy
	}
	if b.covered {
		return nil
	}
	if err := b.fence.add(ctx, path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b.meta.Partial = true
		return nil
	}
	var state agent.PersistedState
	var err error
	b.messages, state, b.intact, err = agent.LoadSessionDisplayMessages(path)
	if errors.Is(err, os.ErrNotExist) {
		b.meta.Partial = true
		b.messages, b.intact = nil, false
		return nil
	}
	if err != nil {
		return err
	}
	b.loadedDigest = state.DigestHex
	return nil
}
