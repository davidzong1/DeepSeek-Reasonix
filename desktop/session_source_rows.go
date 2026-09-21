package main

import (
	"fmt"
	"os"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"sync"
)

type sourceHeadObservation struct {
	stamp string
	heads []agent.SessionHead
	err   error
}

var sourceHeadRows sync.Map

// A single-head DAG is displayed by path, while upgrades record its head ID.
// Use the same path alias in every projection so retained originals cannot
// reappear after their canonical session is archived. Multi-head rows keep
// independent identities; adopting one must never hide its siblings.
func sourceMappingHasPathAlias(mapping workspacestate.SourceMapping) bool {
	if mapping.HeadID == "" {
		return true
	}
	heads, err := sessionSourceHeads(mapping.Path)
	if err != nil {
		return false
	}
	visible := 0
	selected := false
	for _, head := range heads {
		if head.Retired {
			continue
		}
		if head.Kind != agent.HeadKindConcurrent {
			visible++
		}
		if head.Selected && head.ID == mapping.HeadID {
			selected = true
		}
	}
	return selected && visible <= 1
}

// Listing consumes only the published head index. Replaying an event log here
// would make sidebar pagination perform content work and contend with writers.
// Missing/stale indices degrade to one path row and are repaired separately.
func sessionSourceHeads(path string) ([]agent.SessionHead, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	stamp := fmt.Sprint(info.Size(), ":", info.ModTime().UnixNano())
	if cached, ok := sourceHeadRows.Load(path); ok && cached.(sourceHeadObservation).stamp == stamp {
		entry := cached.(sourceHeadObservation)
		return entry.heads, entry.err
	}
	index, err := agent.ReadSessionHeadIndex(path)
	var heads []agent.SessionHead
	if err == nil && index != nil && index.Current(path) {
		heads = index.Heads
	}
	sourceHeadRows.Store(path, sourceHeadObservation{stamp, heads, err})
	return heads, err
}

func expandSessionSourceRows(node ProjectNode) []ProjectNode {
	if node.Session != nil || node.SessionPath == "" || node.RecoveryState == "recovery_only" {
		return []ProjectNode{node}
	}
	heads, err := sessionSourceHeads(node.SessionPath)
	if err != nil {
		node.Health = "degraded"
		return []ProjectNode{node}
	}
	live := []agent.SessionHead{}
	for _, head := range heads {
		if !head.Retired && head.Kind != agent.HeadKindConcurrent {
			live = append(live, head)
		}
	}
	if len(live) <= 1 {
		headID := ""
		if len(live) == 1 {
			headID = live[0].ID
		}
		node.Source = &SessionSourceRef{HostID: localDesktopHostID, Path: node.SessionPath, HeadID: headID, SourceKey: desktopSourceKey(node.SessionPath, headID)}
		node.Historical = true
		return []ProjectNode{node}
	}
	rows := []ProjectNode{}
	for _, head := range live {
		row := node
		row.Source = &SessionSourceRef{HostID: localDesktopHostID, Path: node.SessionPath, HeadID: head.ID, SourceKey: desktopSourceKey(node.SessionPath, head.ID)}
		row.Historical, row.HistoricalBranch = true, true
		row.Key = "source_" + row.Source.SourceKey
		row.Turns, row.Preview = head.Turns, head.Preview
		if !head.LastActivity.IsZero() {
			row.LastActivityAt = head.LastActivity.UnixMilli()
		}
		if !head.CreatedAt.IsZero() {
			row.CreatedAt = head.CreatedAt.UnixMilli()
		}
		rows = append(rows, row)
	}
	return rows
}
