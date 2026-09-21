package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

type historicalCatalogEntry struct {
	scope string
	node  ProjectNode
}

// Discovery publishes metadata only; ordinary pagination never visits source
// directories or replays a historical log. Refreshes share one bounded worker.
func (a *App) requestHistoricalCatalog() {
	c := &a.historicalImports
	c.mu.Lock()
	c.initialize(a.bootContext())
	if !c.catalogEnabled || c.stopped || a.shuttingDown.Load() || c.discoveryPending || time.Since(c.catalogAt) < 5*time.Second {
		c.mu.Unlock()
		return
	}
	c.discoveryPending = true
	c.workers.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.workers.Done()
		_, _ = a.listHistoricalSessions(c.ctx)
		c.mu.Lock()
		c.discoveryPending = false
		stopped := c.stopped
		c.mu.Unlock()
		if !stopped {
			a.emitProjectTreeChanged()
		}
	}()
}

func readHistoricalCanonicalCatalog(ctx context.Context, sources map[string]historicalSource) []historicalCatalogEntry {
	rows := []historicalCatalogEntry{}
	for key, source := range sources {
		if ctx.Err() != nil {
			break
		}
		if source.format != "canonical" || source.version != "" {
			continue
		}
		kind := "global_topic"
		if source.scope == "project" {
			kind = "topic"
		}
		node := ProjectNode{Key: "source_" + key, Kind: kind, Root: source.root, Label: filepath.Base(source.path),
			TopicID: "historical-" + key, Historical: true, SessionPath: source.path, SortOrder: -1,
			TurnsState: "unknown", Health: "metadata_pending", Children: []ProjectNode{},
			Source: &SessionSourceRef{HostID: localDesktopHostID, SourceKey: key, Path: source.path}}
		if info, err := session.NewFilesystemPersistence(filepath.Dir(source.path)).Stat(ctx, filepath.Base(source.path)); err == nil {
			if info.Title != "" {
				node.Label = info.Title
			}
			node.Preview, node.Turns = info.Preview, info.Turns
			node.CreatedAt, node.LastActivityAt = info.CreatedAt.UnixMilli(), info.UpdatedAt.UnixMilli()
			if info.MetadataStatus == session.MetadataReady {
				node.TurnsState, node.Health = "valid", "ok"
			}
		} else if stat, statErr := os.Stat(source.path); statErr == nil {
			node.CreatedAt, node.LastActivityAt = stat.ModTime().UnixMilli(), stat.ModTime().UnixMilli()
			node.Health = "degraded"
		}
		rows = append(rows, historicalCatalogEntry{scope: source.scope, node: node})
	}
	return rows
}

func (a *App) historicalCanonicalTopics(scope, root string, state workspacestate.State) []ProjectNode {
	a.requestHistoricalCatalog()
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := []ProjectNode{}
	for _, entry := range c.catalog {
		if entry.scope != scope || scope == "project" && !sameDesktopPath(entry.node.Root, root) {
			continue
		}
		node := entry.node
		if _, adopted := historicalMappingForSource(state, node.Source.SourceKey); adopted {
			continue
		}
		node.PreparationStatus = "available"
		if view, ok := c.views[node.Source.SourceKey]; ok {
			node.PreparationStatus = view.Status
		}
		rows = append(rows, node)
	}
	return rows
}

func applyHistoricalPresentations(nodes []ProjectNode, saved historicalImportQueueSidecar) {
	for i := range nodes {
		if nodes[i].Source == nil {
			continue
		}
		presentation := saved.Presentations[nodes[i].Source.SourceKey]
		if presentation.Title != "" {
			nodes[i].Label = presentation.Title
		}
		if presentation.Pinned != nil {
			nodes[i].Pinned = *presentation.Pinned
		}
	}
}

// A shell-only read must not create workspaces or migrate organization state.
// Sources without canonical members still need their persisted pin overlays.
func (a *App) historicalPinnedShells(req ProjectTopicPageRequest, state workspacestate.State) ([]ProjectNode, error) {
	adopted := map[string]bool{}
	for _, mapping := range state.SourceMappings {
		adopted["source\x00local\x00"+mapping.SourceKey] = true
		if sourceMappingHasPathAlias(mapping) {
			adopted[sessionRuntimeKey(mapping.Path)] = true
		}
	}
	page, err := a.unadoptedLegacyTopics(req, adopted, nil)
	if err != nil {
		return nil, err
	}
	nodes := append(page.Items, a.historicalCanonicalTopics(req.Scope, req.WorkspaceRoot, state)...)
	if saved, err := readHistoricalSidecar(); err == nil {
		applyHistoricalPresentations(nodes, saved)
	}
	pins := []ProjectNode{}
	for _, node := range nodes {
		if node.Pinned {
			pins = append(pins, node)
		}
	}
	sort.SliceStable(pins, func(i, j int) bool { return projectTopicLess(pins[i], pins[j], req.SortMode, false) })
	return pins, nil
}
