package main

import (
	"fmt"
	"os"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/store"
	"strings"
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

// Cache the bounded DAG read against both files that define its heads. No
// transcript is rewritten and ordinary flat history needs no head expansion.
func sessionSourceHeads(path string) ([]agent.SessionHead, error) {
	var observation strings.Builder
	for _, file := range []string{path, store.SessionEventLog(path)} {
		info, err := os.Stat(file)
		if os.IsNotExist(err) {
			observation.WriteString("missing;")
			continue
		}
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&observation, "%d:%d;", info.Size(), info.ModTime().UnixNano())
	}
	stamp := observation.String()
	if cached, ok := sourceHeadRows.Load(path); ok && cached.(sourceHeadObservation).stamp == stamp {
		entry := cached.(sourceHeadObservation)
		return entry.heads, entry.err
	}
	heads, err := agent.ListSessionHeads(path)
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
		return []ProjectNode{node}
	}
	rows := []ProjectNode{}
	for _, head := range live {
		row := node
		row.Source = &SessionSourceRef{HostID: localDesktopHostID, Path: node.SessionPath, HeadID: head.ID, SourceKey: desktopSourceKey(node.SessionPath, head.ID)}
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
