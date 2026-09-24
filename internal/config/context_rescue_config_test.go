package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The switch the rescue documentation tells a user to set is a wire contract:
// a rename here would silently leave the feature off for everyone who followed
// it. So the exact key is parsed, not assumed from the struct tag.
func TestAgentContextRescueKeyParses(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	body := "[agent]\ncontext_rescue = true\n"
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Agent.ContextRescue {
		t.Fatal("context_rescue = true did not reach the agent config")
	}
}

// The default matters as much as the key: the plan stages this behind a
// gray-release gate, so an unset config must never arm it.
func TestAgentContextRescueDefaultsOff(t *testing.T) {
	isolateUserConfigHome(t)
	cfg, err := LoadForRootReadOnly(t.TempDir())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Agent.ContextRescue {
		t.Fatal("context rescue is armed without being asked for")
	}
}
