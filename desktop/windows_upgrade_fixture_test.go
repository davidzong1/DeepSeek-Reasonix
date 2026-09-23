package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"reasonix/desktop/internal/upgradefixture"
	"reasonix/internal/agent"
	"reasonix/internal/config"
)

func TestWindowsUpgradeFixturePreservesLegacyAcrossRestart(t *testing.T) {
	isolateDesktopUserDirs(t)
	home := filepath.Join(t.TempDir(), "home # %20 中文")
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("REASONIX_STATE_HOME", home)
	t.Setenv("REASONIX_CACHE_HOME", filepath.Join(home, "cache"))
	reportPath := filepath.Join(t.TempDir(), "fixture.json")
	if err := upgradefixture.Run("create", home, reportPath, ""); err != nil {
		t.Fatal(err)
	}
	// Reports and runtime manifests may spell the same file differently.
	// Windows runtime paths fold drive case; dot segments exercise the same
	// identity requirement on every platform without rewriting real history.
	var report map[string]json.RawMessage
	body, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	var legacyPath string
	if err := json.Unmarshal(report["legacyPath"], &legacyPath); err != nil {
		t.Fatal(err)
	}
	legacyPath = agent.CanonicalSessionPath(legacyPath)
	report["legacyPath"], err = json.Marshal(filepath.Dir(legacyPath) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(legacyPath))
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{config.SessionStoreDir(), config.DesktopSessionStoreDir()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fixture must not pre-create canonical history: %s: %v", path, err)
		}
	}
	for _, phase := range []string{"first", "restart"} {
		app := NewApp()
		closeApp := sync.OnceFunc(func() {
			app.shutdown(context.Background())
			app.stopHistoricalImports()
			app.closeSessionServices()
			if err := app.draftStore().Close(); err != nil {
				t.Errorf("close fixture draft store: %v", err)
			}
		})
		t.Cleanup(closeApp)
		if app.NeedsOnboarding() {
			t.Fatal("legacy fixture must restore the conversation instead of opening first-run provider settings")
		}
		app.startDesktopSessionMigration(t.Context())
		if !app.waitForDesktopMigration(t.Context()) {
			t.Fatal("startup discovery did not finish")
		}
		if phase == "first" {
			file, _ := app.reconcileSavedTabs(t.Context(), loadTabsFile())
			if len(file.Tabs) != 1 || file.Tabs[0].historicalSource == nil {
				t.Fatalf("saved historical tab must be pending, not corrupt: %+v", file.Tabs)
			}
			tab := &WorkspaceTab{}
			if !prepareRestoredTabIdentity(tab, file.Tabs[0]) || tab.Ctrl != nil || tab.StartupErr != "" || tab.SessionPath != legacyPath {
				t.Fatalf("native identity was not admitted independently of discovery: %+v", tab)
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil || len(state.SourceMappings) != 0 || len(state.PendingOperations) != 0 {
				t.Fatalf("startup converted historical content: %+v, %v", state, err)
			}
			// A normal workspace metadata write upgrades the v1 registry without
			// preparing or importing the legacy transcript.
			if err := app.workspaceRegistry().SetWorkspaceVisible(t.Context(), "global", true); err != nil {
				t.Fatal(err)
			}
		}
		if phase == "restart" {
			// Real startup also snapshots the current registry before recovery.
			if err := app.backupDesktopUpgradeMetadata(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		closeApp()
		if err := upgradefixture.Run("verify", home, reportPath, phase); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		var evidence struct {
			LegacyPath string `json:"legacyPath"`
			History    string `json:"history"`
		}
		body, err := os.ReadFile(filepath.Join(filepath.Dir(reportPath), "verification-"+phase+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &evidence); err != nil {
			t.Fatal(err)
		}
		if agent.CanonicalSessionPath(evidence.LegacyPath) != legacyPath {
			t.Fatalf("%s changed the restored legacy path: %q", phase, evidence.LegacyPath)
		}
		if evidence.History == "" {
			t.Fatalf("%s lost the saved history", phase)
		}
	}
}
