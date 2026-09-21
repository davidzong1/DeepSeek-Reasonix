package main

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"sync"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

type topicIndexEntry struct {
	nodes []ProjectNode
	used  uint64
}

// Rebuildable, bounded materialized orders. Every lookup is bound to current
// registry, source and canonical metadata; a cache hit never bypasses the
// caller's final double-collect consistency check.
type workspaceTopicIndex struct {
	mu      sync.Mutex
	clock   uint64
	rows    int
	entries map[[32]byte]topicIndexEntry
}

func (a *App) indexedWorkspaceTopics(req ProjectTopicPageRequest, state workspacestate.State, workspace workspacestate.Workspace, org workspacestate.Organization, infos map[string]session.SessionInfo, sources []ProjectNode) []ProjectNode {
	created := loadTopicCreatedAts(topicTitleRoot(req.Scope, req.WorkspaceRoot))
	encoded, _ := json.Marshal([]any{workspace, org, state.SessionStates, state.SourceMappings, state.Presentation, infos, sources, created,
		projectTopicCursorBinding(req, req.GroupFilter, req.GroupID, org.Revision), a.localizedDefaultTopicTitle()})
	key := sha256.Sum256(encoded)
	c := &a.desktopSessions.topicIndex
	c.mu.Lock()
	c.clock++
	entry, hit := c.entries[key]
	if hit {
		entry.used = c.clock
		c.entries[key] = entry
	}
	c.mu.Unlock()
	// Relative-time filters expire without any owner mutation.
	cacheable := desktopSessionTimeCutoff(req.TimeFilter) == 0
	if hit && cacheable {
		return entry.nodes
	}
	all := req
	all.Query, all.TimeFilter, all.GroupFilter = "", "", "all"
	all.ExcludePinned, all.groupSelected, all.groupAll = false, nil, nil
	nodes := a.canonicalTopicNodes(all, state, workspace, infos, sources, created)
	nodes = filterWorkspaceSessionNodes(req, org, state, workspace.ID, nodes)
	sort.SliceStable(nodes, func(i, j int) bool { return projectTopicLess(nodes[i], nodes[j], req.SortMode, org.ManualOrderEnabled) })
	if !cacheable || len(nodes) > 8192 {
		return nodes
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[[32]byte]topicIndexEntry{}
	}
	if _, exists := c.entries[key]; exists {
		return nodes
	}
	for len(c.entries) >= 16 || c.rows+len(nodes) > 8192 {
		var oldest [32]byte
		var age uint64
		first := true
		for k, candidate := range c.entries {
			if first || candidate.used < age {
				oldest, age, first = k, candidate.used, false
			}
		}
		c.rows -= len(c.entries[oldest].nodes)
		delete(c.entries, oldest)
	}
	c.clock++
	c.entries[key] = topicIndexEntry{nodes, c.clock}
	c.rows += len(nodes)
	return nodes
}

func cloneTopicPage(nodes []ProjectNode) []ProjectNode {
	result := make([]ProjectNode, len(nodes))
	for i, node := range nodes {
		node.Children = cloneTopicPage(node.Children)
		node.IdentityAliases = append([]string(nil), node.IdentityAliases...)
		if node.Session != nil {
			ref := *node.Session
			node.Session = &ref
		}
		if node.ParentSession != nil {
			ref := *node.ParentSession
			node.ParentSession = &ref
		}
		if node.Source != nil {
			ref := *node.Source
			node.Source = &ref
		}
		if node.Remote != nil {
			ref := *node.Remote
			node.Remote = &ref
		}
		result[i] = node
	}
	return result
}
