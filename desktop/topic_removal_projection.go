package main

import "reasonix/desktop/internal/workspacestate"

// A completed chat-file catalog cannot represent metadata-only placeholders.
// Keep those explicit indexed objects visible, but never resurrect a hidden
// recovery lineage or an adopted/archived formal session from its old metadata.
func (a *App) withRemovablePlaceholderTopics(req ProjectTopicPageRequest, state workspacestate.State, nodes []ProjectNode, adopted map[string]bool) []ProjectNode {
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		seen[node.TopicID] = true
	}
	file, err := readTopicRemovalProjects()
	if err != nil {
		return nodes
	}
	dirs := a.knownSessionDirs()
	for _, node := range a.metadataProjectTopics(req.Scope, req.WorkspaceRoot) {
		if seen[node.TopicID] || adopted[node.TopicID] || node.RuntimeOnly {
			continue
		}
		item, err := topicRemovalCandidate(state, file, TopicRemovalTarget{TopicID: node.TopicID})
		if err != nil || item.Topic.Scope != req.Scope || (req.Scope == "project" && !sameDesktopPath(item.Topic.WorkspaceRoot, req.WorkspaceRoot)) {
			continue
		}
		if classification, _ := classifyLegacyCleanupTopicSources(item, dirs); classification != "empty" {
			continue
		}
		nodes = append(nodes, node)
		seen[node.TopicID] = true
	}
	return nodes
}
