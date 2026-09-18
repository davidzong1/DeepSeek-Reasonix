package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/upgradefixture"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/session"
)

func TestWindowsUpgradeFixtureMigratesLegacyAndRestarts(t *testing.T) {
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
		t.Cleanup(app.closeSessionServices)
		if app.NeedsOnboarding() {
			t.Fatal("legacy fixture must restore the conversation instead of opening first-run provider settings")
		}
		if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
			t.Fatal(err)
		}
		if phase == "restart" {
			// Real startup also snapshots the current registry before recovery.
			if err := app.backupDesktopUpgradeMetadata(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if err := upgradefixture.Run("verify", home, reportPath, phase); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		var evidence struct {
			SessionID string `json:"sessionId"`
			History   string `json:"history"`
		}
		body, err := os.ReadFile(filepath.Join(filepath.Dir(reportPath), "verification-"+phase+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &evidence); err != nil {
			t.Fatal(err)
		}
		page, err := app.ReadSessionHistory(session.SessionRef{HostID: localDesktopHostID, SessionID: evidence.SessionID}, "", 10)
		if err != nil || len(page.Messages) != 2 || page.Messages[1].Content != evidence.History {
			t.Fatalf("%s history API: %+v, %v", phase, page, err)
		}
		app.closeSessionServices()
	}
}
