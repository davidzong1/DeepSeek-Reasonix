package provider

import (
	"errors"
	"net/http"
	"testing"
)

func TestCapacityOrError(t *testing.T) {
	fallback := errors.New("protocol error")
	for _, tc := range []struct {
		name, ref string
		want      bool
	}{
		{"openai code", "server_is_overloaded", true},
		{"openai type", "service_unavailable_error", true},
		{"anthropic type", "overloaded_error", true},
		{"gateway spelling", "server_busy", true},
		{"mixed case", "Server_Is_Overloaded", true},
		{"plain failure", "invalid_request_error", false},
		{"reasoning replay", "reasoning_content_missing", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CapacityOrError("p", "", "", tc.ref, "upstream says no", fallback)
			var capacity *CapacityError
			if isCapacity := errors.As(got, &capacity); isCapacity != tc.want {
				t.Fatalf("CapacityOrError(%q) = %v, want capacity %v", tc.ref, got, tc.want)
			}
			if !tc.want && !errors.Is(got, fallback) {
				t.Fatalf("CapacityOrError(%q) = %v, want the caller's fallback", tc.ref, got)
			}
		})
	}
}

// TestCapacityErrorKeepsReferenceAndStatus pins what the retry policy and the
// display layer each depend on: the 503 class, the provider's own code, and the
// provider's verbatim words.
func TestCapacityErrorKeepsReferenceAndStatus(t *testing.T) {
	err := CapacityOrError("opencode-go", "Go", "responses", "server_is_overloaded", "  Our servers are currently overloaded.  ", errors.New("fallback"))
	f := ClassifyRecovery(err)
	if !f.Retryable {
		t.Fatal("a capacity refusal must be retryable")
	}
	if f.Status != http.StatusServiceUnavailable || f.Code != "server_is_overloaded" {
		t.Fatalf("ClassifyRecovery = %+v, want the 503 class and the provider code", f)
	}
	var capacity *CapacityError
	if !errors.As(err, &capacity) || capacity.Message != "Our servers are currently overloaded." {
		t.Fatalf("capacity = %+v, want the trimmed provider text", capacity)
	}
}

func TestStreamErrorCode(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"code wins", `{"error":{"code":"server_is_overloaded","type":"service_unavailable_error"}}`, "server_is_overloaded"},
		{"type fallback", `{"error":{"type":"overloaded_error","message":"overloaded"}}`, "overloaded_error"},
		{"no envelope", `{"choices":[]}`, ""},
		{"not json", `plain text`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StreamErrorCode(tc.raw); got != tc.want {
				t.Fatalf("StreamErrorCode(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
