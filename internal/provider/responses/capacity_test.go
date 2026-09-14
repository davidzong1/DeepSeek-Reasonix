package responses

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/provider"
)

// TestFailedEventCapacityRefusal pins the classification of a capacity refusal
// delivered as response.failed, which is how gpt-5.6 behind a gateway reports
// server_is_overloaded. The Agent must replay it with backoff instead of ending
// the turn on the first attempt.
func TestFailedEventCapacityRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEvents(w, `{"type":"response.failed","response":{"id":"resp","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`)
	}))
	defer server.Close()
	chunks := collect(t, New(Config{Name: "test", APIKey: "key", KeyEnv: "TEST_API_KEY", BaseURL: server.URL, Model: "gpt-5.6-luna"}), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	for _, chunk := range chunks {
		if chunk.Type != provider.ChunkError {
			continue
		}
		var capacity *provider.CapacityError
		if !errors.As(chunk.Err, &capacity) {
			t.Fatalf("error = %T %v, want a CapacityError", chunk.Err, chunk.Err)
		}
		if failure := provider.ClassifyRecovery(chunk.Err); !failure.Retryable || failure.Code != "server_is_overloaded" {
			t.Fatalf("ClassifyRecovery = %+v, want retryable with the provider code", failure)
		}
		return
	}
	t.Fatal("missing error chunk")
}

// TestFailedEventKeepsUnclassifiedFailureFatal guards the other direction: a
// rejection the classifier does not recognize must still fail the turn, and
// keep the protocol's own error text.
func TestFailedEventKeepsUnclassifiedFailureFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEvents(w, `{"type":"response.failed","response":{"id":"resp","error":{"code":"invalid_request_error","message":"bad tool schema"}}}`)
	}))
	defer server.Close()
	chunks := collect(t, New(Config{Name: "test", APIKey: "key", KeyEnv: "TEST_API_KEY", BaseURL: server.URL, Model: "m"}), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	for _, chunk := range chunks {
		if chunk.Type != provider.ChunkError {
			continue
		}
		if err := chunk.Err.Error(); err != "responses: bad tool schema" {
			t.Fatalf("error = %q, want the protocol's own text", err)
		}
		if provider.ClassifyRecovery(chunk.Err).Retryable {
			t.Fatal("an unclassified refusal must not be replayed")
		}
		return
	}
	t.Fatal("missing error chunk")
}
