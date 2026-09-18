package main

import (
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"testing"
)

func TestIndependentLegacyHeadsAdoptOneWithoutHidingSibling(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := filepath.Join(config.SessionDir(), "independent-heads.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "question", Role: provider.RoleUser, Content: "question"})
	legacy.Add(provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ForkHead(path, legacy.Snapshot()[2].ID, agent.HeadKindFork, "child"); err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{ID: "child", Role: provider.RoleUser, Content: "child only"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	rows := expandSessionSourceRows(ProjectNode{Key: "source", Kind: "global_topic", TopicID: "same", SessionPath: path})
	if len(rows) != 2 || projectNodeSessionKey(rows[0]) == projectNodeSessionKey(rows[1]) {
		t.Fatalf("heads=%+v", rows)
	}
	// Grouping a source is metadata-only; pinning is an explicit adoption.
	if err := app.SetSessionPinned(SessionSelector{Source: rows[0].Source}, true); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.SourceMappings) != 1 {
		t.Fatalf("mapped more than requested head: %+v", state.SourceMappings)
	}
	if _, ok := state.SourceMappings[rows[0].Source.SourceKey]; !ok {
		t.Fatal("requested head was not adopted")
	}
	if _, ok := state.SourceMappings[rows[1].Source.SourceKey]; ok {
		t.Fatal("sibling was adopted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("source transcript changed during adoption")
	}
}
