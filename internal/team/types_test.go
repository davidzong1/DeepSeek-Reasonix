package team

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEnumConstantsUnique guards against value collisions and empty constants
// introduced while extending the role set.
func TestEnumConstantsUnique(t *testing.T) {
	roles := []RoleID{RoleLeader, RoleCoder, RoleReviewer, RoleTester, RoleArchitectureAnalyst, RolePluginEngineer}
	seen := map[RoleID]bool{}
	for _, r := range roles {
		if r == "" {
			t.Fatal("empty role constant")
		}
		if seen[r] {
			t.Fatalf("duplicate role constant %q", r)
		}
		seen[r] = true
	}
}

// TestSecretRefJSONCarriesNoKey enforces K1 at the wire format level: a
// serialized AgentUser must not contain key-like material, only the store
// reference id.
func TestSecretRefJSONCarriesNoKey(t *testing.T) {
	u := AgentUser{
		UserID:    "au-1",
		Provider:  "deepseek",
		SecretRef: SecretRef{StoreID: "store-entry-1", Scope: CredentialScopeAgentUser},
	}
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(data)
	for _, marker := range []string{"sk-", "password", "token", "api_key", "apikey"} {
		if strings.Contains(wire, marker) {
			t.Fatalf("wire format leaks key material %q: %s", marker, wire)
		}
	}
	if !strings.Contains(wire, "store-entry-1") {
		t.Fatalf("wire format lost the secret-store reference: %s", wire)
	}
}

// TestTaskStatusCycle exercises the documented §2.6 lifecycle transition
// values so a rename of a status constant is caught by the test suite.
func TestTaskStatusCycle(t *testing.T) {
	cycle := []TaskStatus{
		TaskStatusCreated,
		TaskStatusAssigned,
		TaskStatusReported,
		TaskStatusArchived,
	}
	for _, s := range cycle {
		if s == "" {
			t.Fatal("empty task status constant")
		}
	}
}
