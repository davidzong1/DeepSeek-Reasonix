package main

import (
	"sort"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

// Sorting is owned by the immutable read snapshot. Do not retain a second
// cache of mutable materialized rows with an independent eviction policy.
func (a *App) indexedWorkspaceTopics(req ProjectTopicPageRequest, state workspacestate.State, workspace workspacestate.Workspace, org workspacestate.Organization, infos map[string]session.SessionInfo, sources []ProjectNode) []ProjectNode {
	created := loadTopicCreatedAts(topicTitleRoot(req.Scope, req.WorkspaceRoot))
	all := req
	all.Query, all.TimeFilter, all.GroupFilter = "", "", "all"
	all.ExcludePinned, all.groupSelected, all.groupAll = false, nil, nil
	nodes := a.canonicalTopicNodes(all, state, workspace, infos, sources, created)
	nodes = filterWorkspaceSessionNodes(req, org, state, workspace.ID, nodes)
	sort.SliceStable(nodes, func(i, j int) bool { return projectTopicLess(nodes[i], nodes[j], req.SortMode, org.ManualOrderEnabled) })
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
