package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestSessionExportHTTPFixedCompleteSnapshot(t *testing.T) {
	service, err := session.NewService("serve", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "export"})
	if err != nil {
		t.Fatal(err)
	}
	appendMessage := func(id string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: id, Origin: provider.MessageOrigin("user")}})
		if _, err := runtime.Session().AppendBatch(t.Context(), id, []session.Event{{Kind: "message/complete", Payload: body}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 110 {
		appendMessage(fmt.Sprintf("question-%03d", i))
	}
	bc := NewBroadcaster()
	ctrl := control.New(control.Options{SessionService: service, SessionRuntime: runtime, ExclusiveSession: true, Sink: bc})
	defer ctrl.Close()
	server := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer server.Close()
	response, err := http.Get(server.URL + "/session-export/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot session.ExportSnapshot
	err = json.NewDecoder(response.Body).Decode(&snapshot)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot: %d %v", response.StatusCode, err)
	}
	appendMessage("AFTER-SNAPSHOT")
	request, _ := json.Marshal(map[string]any{"snapshot": snapshot, "format": "json"})
	response, err = http.Post(server.URL+"/session-export/document", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Reasonix-Export-Records") != "110" || !json.Valid(body) {
		t.Fatalf("response: %d %s", response.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("question-000")) || !bytes.Contains(body, []byte("question-109")) || bytes.Contains(body, []byte("AFTER-SNAPSHOT")) {
		t.Fatal("export was truncated or changed its snapshot")
	}
	snapshot.Ref.SessionID = "other"
	request, _ = json.Marshal(map[string]any{"snapshot": snapshot, "format": "json"})
	response, err = http.Post(server.URL+"/session-export/document", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("accepted mismatched identity: %d", response.StatusCode)
	}
	malicious, _ := json.Marshal(map[string]any{"snapshot": snapshot, "format": "../../manifest.json"})
	response, err = http.Post(server.URL+"/session-export/document", "application/json", bytes.NewReader(malicious))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("accepted path-like export format: %d", response.StatusCode)
	}
	response, err = http.Post(server.URL+"/session-export/diagnostic", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !json.Valid(body) {
		t.Fatalf("diagnostic: %d %v", response.StatusCode, err)
	}
}
