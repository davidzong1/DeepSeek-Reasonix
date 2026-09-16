package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

// Both recovery and the sidebar resolve durable registry members. Legacy
// catalog rows remain available only until their exact source is adopted.
func (a *App) unifiedProjectTopics(req ProjectTopicPageRequest) (ProjectTopicPage, error) {
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return ProjectTopicPage{Items: []ProjectNode{}}, err
	}
	workspaceID := desktopWorkspaceID(req.Scope, req.WorkspaceRoot)
	workspace, exists := state.Workspaces[workspaceID]
	if !exists || len(workspace.SessionIDs) == 0 {
		page, err := a.listProjectTopics(req)
		page.Revision += state.Generation
		return page, err
	}
	offset := 0
	prefix := fmt.Sprintf("workspace:%d:", state.Generation)
	if req.Cursor != "" {
		if !strings.HasPrefix(req.Cursor, prefix) {
			return ProjectTopicPage{Items: []ProjectNode{}}, fmt.Errorf("workspace session cursor is stale")
		}
		offset, err = strconv.Atoi(strings.TrimPrefix(req.Cursor, prefix))
		if err != nil || offset < 0 {
			return ProjectTopicPage{Items: []ProjectNode{}}, fmt.Errorf("invalid workspace session cursor")
		}
	}
	adopted := map[string]bool{}
	adoptedTopics := map[string]bool{}
	runtimeTopics := map[string]string{}
	for _, source := range state.SourceMappings {
		if source.WorkspaceID == workspaceID {
			adopted[sessionRuntimeKey(source.Path)] = true
		}
	}
	for _, id := range workspace.SessionIDs {
		if topic := state.Presentation[id].TopicID; topic != "" {
			adoptedTopics[topic] = true
		}
	}
	a.mu.RLock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.SessionWorkspace.ID == workspaceID && state.SessionStates[tab.SessionID].Lifecycle != "" && tab.TopicID != "" {
			adoptedTopics[tab.TopicID] = true
			runtimeTopics[tab.SessionID] = tab.TopicID
		}
	}
	a.mu.RUnlock()

	legacy, err := a.unadoptedLegacyTopics(req, adopted, adoptedTopics)
	if err != nil {
		return legacy, err
	}
	nodes := a.canonicalTopicNodes(req, state, workspace, runtimeTopics, legacy.Items)
	nodes = groupWorkspaceTopics(nodes)
	sort.SliceStable(nodes, func(i, j int) bool {
		return projectTopicLess(nodes[i], nodes[j], req.SortMode, manualTopicOrderFor(req.Scope, req.WorkspaceRoot))
	})
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset > len(nodes) {
		offset = len(nodes)
	}
	end := min(offset+limit, len(nodes))
	legacy.Items, legacy.Revision = append([]ProjectNode{}, nodes[offset:end]...), legacy.Revision+state.Generation
	if end < len(nodes) {
		legacy.NextCursor = prefix + strconv.Itoa(end)
	}
	return legacy, nil
}

func groupWorkspaceTopics(nodes []ProjectNode) []ProjectNode {
	result := []ProjectNode{}
	positions := map[string]int{}
	leaf := func(node ProjectNode) ProjectNode {
		if node.Kind == "global_topic" {
			node.Kind = "global_session"
		} else {
			node.Kind = "session"
		}
		node.Children = []ProjectNode{}
		return node
	}
	for _, node := range nodes {
		index, exists := positions[node.TopicID]
		if !exists || node.TopicID == "" {
			positions[node.TopicID] = len(result)
			result = append(result, node)
			continue
		}
		parent := &result[index]
		if len(parent.Children) == 0 {
			parent.Children = []ProjectNode{leaf(*parent)}
		}
		if len(node.Children) == 0 {
			parent.Children = append(parent.Children, leaf(node))
		} else {
			parent.Children = append(parent.Children, node.Children...)
		}
		parent.Pinned = parent.Pinned || node.Pinned
		if node.LastActivityAt > parent.LastActivityAt {
			parent.LastActivityAt, parent.SessionPath, parent.Session = node.LastActivityAt, node.SessionPath, node.Session
		}
	}
	return result
}

func desktopSessionTimeCutoff(filter string) int64 {
	value := strings.ToLower(strings.TrimSpace(filter))
	switch value {
	case "day":
		value = "24h"
	case "week", "7d":
		value = "168h"
	case "month", "30d":
		value = "720h"
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0
	}
	return time.Now().Add(-duration).UnixMilli()
}

func (a *App) unifiedProjectRevision(catalogRevision uint64) uint64 {
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return catalogRevision
	}
	return catalogRevision + state.Generation
}

func (a *App) updateCanonicalTopicPresentation(topicID string, title *string, pinned *bool) (bool, error) {
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return false, err
	}
	ids := []string{}
	for _, workspace := range state.Workspaces {
		for _, id := range workspace.SessionIDs {
			if state.Presentation[id].TopicID == topicID || "canonical-"+id == topicID {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return false, nil
	}
	if title != nil {
		for _, id := range ids {
			if err := a.desktopSessionService("").SetTitle(a.bootContext(), session.SessionRef{HostID: localDesktopHostID, SessionID: id}, *title); err != nil {
				return true, err
			}
		}
	}
	if err := a.workspaceRegistry().UpdatePresentation(a.bootContext(), ids, title, pinned); err != nil {
		return true, err
	}
	if title != nil {
		a.updateOpenTopicTitle(topicID, *title, topicTitleSourceManual)
		a.saveTabsFromRemote()
	}
	a.emitProjectTreeMetadataChanged()
	return true, nil
}

func (a *App) mergeCanonicalWorkspaceShells(projects []ProjectNode) []ProjectNode {
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return projects
	}
	present := map[string]bool{}
	visible := make([]ProjectNode, 0, len(projects))
	for _, project := range projects {
		if project.Remote != nil {
			visible = append(visible, project)
			continue
		}
		scope := "project"
		if project.Kind == "global_folder" {
			scope = "global"
		}
		id := desktopWorkspaceID(scope, project.Root)
		if workspace, ok := state.Workspaces[id]; ok && !workspace.Visible {
			continue
		}
		present[id] = true
		visible = append(visible, project)
	}
	projects = visible
	for _, id := range state.WorkspaceIDs {
		workspace := state.Workspaces[id]
		if !workspace.Visible || present[id] {
			continue
		}
		kind, key := "project", "project_"+workspace.Root
		if id == workspacestate.GlobalWorkspaceID {
			kind, key = "global_folder", "global_folder"
		}
		projects = append(projects, ProjectNode{Key: key, Kind: kind, Root: workspace.Root, Label: workspace.Title, Children: []ProjectNode{}})
	}
	// Pinned shells must use the same canonical rows as ordinary pages. Legacy
	// shells otherwise overwrite the title/key on refresh and resurrect pins
	// for sessions whose registry lifecycle is already archived.
	for index := range projects {
		project := &projects[index]
		if project.Remote != nil {
			continue
		}
		scope, root := "project", project.Root
		if project.Kind == "global_folder" {
			scope, root = "global", ""
		}
		workspace := state.Workspaces[desktopWorkspaceID(scope, root)]
		if len(workspace.SessionIDs) == 0 {
			continue
		}
		req := ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 200}
		pins := []ProjectNode{}
		for {
			page, err := a.unifiedProjectTopics(req)
			if err != nil {
				project.Health = "metadata_failed"
				break
			}
			unpinned := false
			for _, node := range page.Items {
				if node.Pinned {
					pins = append(pins, node)
				} else {
					unpinned = true
				}
			}
			if unpinned || page.NextCursor == "" {
				project.Children = pins
				break
			}
			req.Cursor = page.NextCursor
		}
	}
	return projects
}

func (a *App) unadoptedLegacyTopics(req ProjectTopicPageRequest, adopted, adoptedTopics map[string]bool) (ProjectTopicPage, error) {
	legacyReq := req
	legacyReq.Cursor, legacyReq.Limit = "", 200
	legacy := ProjectTopicPage{Items: []ProjectNode{}}
	for {
		page, err := a.listProjectTopics(legacyReq)
		if err != nil {
			return page, err
		}
		legacy.Complete, legacy.ReadyDirectories, legacy.PendingDirectories, legacy.FailedDirectories = page.Complete, page.ReadyDirectories, page.PendingDirectories, page.FailedDirectories
		legacy.Revision = max(legacy.Revision, page.Revision)
		for _, node := range page.Items {
			if adopted[sessionRuntimeKey(node.SessionPath)] || (node.SessionPath == "" && adoptedTopics[node.TopicID]) {
				remaining := []ProjectNode{}
				for _, child := range node.Children {
					if !adopted[sessionRuntimeKey(child.SessionPath)] {
						remaining = append(remaining, child)
					}
				}
				if len(remaining) > 0 {
					node.Children = remaining
					node.SessionPath = remaining[0].SessionPath
					legacy.Items = append(legacy.Items, node)
				}
				continue
			}
			legacy.Items = append(legacy.Items, node)
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == legacyReq.Cursor {
			return legacy, fmt.Errorf("legacy session cursor did not advance")
		}
		legacyReq.Cursor = page.NextCursor
	}
	return legacy, nil
}

func (a *App) canonicalTopicNodes(req ProjectTopicPageRequest, state workspacestate.State, workspace workspacestate.Workspace, runtimeTopics map[string]string, initial []ProjectNode) []ProjectNode {
	workspaceID := workspace.ID
	service := a.desktopSessionService("")
	infos, _ := listWorkspaceSessionInfo(a.bootContext(), service.Query(), workspace.SessionIDs)
	nodes := initial
	query := strings.ToLower(strings.TrimSpace(req.Query))
	cutoff := desktopSessionTimeCutoff(req.TimeFilter)
	createdTopics := loadTopicCreatedAts(topicTitleRoot(req.Scope, req.WorkspaceRoot))
	projects := loadProjectsFile()
	for index, id := range workspace.SessionIDs {
		if state.SessionStates[id].Lifecycle != workspacestate.Active {
			continue
		}
		info, found := infos[id]
		row := workspaceSessionRow(workspaceID, id, info, found, false, service)
		if found && cutoff > 0 && max(row.CreatedAt, row.UpdatedAt) < cutoff {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(row.Title+"\n"+row.Preview+"\n"+id), query) {
			continue
		}
		ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
		presentation := state.Presentation[id]
		label := row.Title
		if label == "" {
			label = row.Preview
		}
		if label == "" {
			label = presentation.Title
		}
		if label == "" {
			label = defaultTopicTitle
		}
		kind := "topic"
		if req.Scope != "project" {
			kind = "global_topic"
		}
		topicID := presentation.TopicID
		if topicID == "" {
			topicID = runtimeTopics[id]
		}
		if topicID == "" {
			topicID = "canonical-" + id
		}
		sortOrder := index
		ordering := historicalTopicPresentationFrom(projects, workspaceID, presentation)
		if ordering.SortOrder >= 0 {
			sortOrder = ordering.SortOrder
		}
		createdAt := row.CreatedAt
		if previous := createdTopics[topicID]; previous > 0 {
			createdAt = previous
		}
		nodes = append(nodes, ProjectNode{
			Key: "canonical_" + id, Kind: kind, Label: label, Root: workspace.Root,
			TopicID: topicID, Session: &ref, SessionPath: sessionRoute(id), CanArchive: row.Health != "missing",
			Preview: row.Preview, Turns: row.Turns, TurnsState: row.MetadataStatus, Health: row.Health,
			CreatedAt: createdAt, LastActivityAt: row.UpdatedAt, Open: row.Running,
			Pinned: presentation.Pinned, SortOrder: sortOrder, Children: []ProjectNode{},
		})
	}
	return nodes
}
