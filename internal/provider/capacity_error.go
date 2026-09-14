package provider

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CapacityError reports a provider capacity refusal delivered inside an
// otherwise-successful response: HTTP 200, then an error event in the body —
// OpenAI's server_is_overloaded, Anthropic's overloaded_error. The same
// condition reaches us as a 503 status when the gateway uses one, and the
// recovery classifier must treat both alike: the request was well-formed, the
// upstream is out of capacity, and a backoff replay is the remedy.
type CapacityError struct {
	Provider            string
	ProviderDisplayName string
	Protocol            string
	Status              int
	Code                string // the provider's own code, e.g. server_is_overloaded
	Message             string // the provider's verbatim reason, already user-facing
}

func (e *CapacityError) Error() string {
	msg := cmp.Or(e.Message, "the provider reported it is at capacity")
	return fmt.Sprintf("%s: %s", ProviderDisplayLabel(e.Provider, e.ProviderDisplayName, e.Protocol), msg)
}

// Unwrap exposes the 503 to the status consumers — the retry classifier's
// retryable decision and the display mapping — while keeping APIError's
// "request error the user must fix" meaning out of it.
func (e *CapacityError) Unwrap() error {
	return &APIError{Provider: e.Provider, ProviderDisplayName: e.ProviderDisplayName, Protocol: e.Protocol, Status: e.Status, Body: capacityBody(e.Code, e.Message)}
}

// capacityBody restates the provider's code and message in the error-envelope
// shape the status consumers parse, so the code survives classification and the
// display layer quotes the provider's own words rather than a status line.
func capacityBody(code, message string) string {
	body, err := json.Marshal(map[string]map[string]string{"error": {"code": code, "message": message}})
	if err != nil {
		return message
	}
	return string(body)
}

// capacityMarkers are the code and type spellings that mean "out of capacity",
// matched against the provider's code/type field only — never the free text of
// the message, where the same words appear in unrelated failures.
var capacityMarkers = []string{"overloaded", "over_load", "service_unavailable", "server_busy"}

// CapacityOrError returns the retryable capacity error when ref — a provider
// error code or type — names a capacity refusal, and the caller's own fallback
// otherwise. An unclassifiable refusal must fail the turn, never replay.
func CapacityOrError(name, displayName, protocol, ref, message string, fallback error) error {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for _, marker := range capacityMarkers {
		if strings.Contains(ref, marker) {
			return &CapacityError{Provider: name, ProviderDisplayName: displayName, Protocol: protocol, Status: http.StatusServiceUnavailable, Code: ref, Message: strings.TrimSpace(message)}
		}
	}
	return fallback
}

// StreamErrorCode reads the code from an OpenAI-shaped error event payload,
// falling back to its type. It exists so adapters that only surface the message
// need not declare fields they would not otherwise read.
func StreamErrorCode(raw string) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return ""
	}
	return cmp.Or(envelope.Error.Code, envelope.Error.Type)
}
