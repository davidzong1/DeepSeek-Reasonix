package main

import "strings"

func prepareRestoredTabIdentity(tab *WorkspaceTab, entry desktopTabEntry) bool {
	tab.goal = strings.TrimSpace(entry.Goal)
	if entry.historicalSource != nil {
		tab.HistoricalSource = entry.historicalSource
		tab.retainLegacyPinnedFiles(entry.PinnedFiles)
		return false
	}
	if entry.restoreBlocked {
		tab.StartupErr = "Saved session identity could not be verified. Recovery data was preserved."
		tab.retainLegacyPinnedFiles(entry.PinnedFiles)
		return false
	}
	tab.goal = runningTabSessionGoal(strings.TrimSpace(entry.SessionPath), tab.goal)
	restoreTabPinnedContext(tab, entry.PinnedFiles)
	return true
}
