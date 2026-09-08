package provider

import (
	"errors"
	"testing"
)

// apiErr is the tiny constructor the classifier tests share.
func apiErr(status int, body string) *APIError {
	return &APIError{Provider: "test", Status: status, Body: body}
}

func TestQuotaExhaustedHTTP402(t *testing.T) {
	for _, err := range []error{
		apiErr(402, ""),
		apiErr(402, "insufficient balance: top up and retry"),
		apiErr(402, "{\"error\":{\"message\":\"quota exceeded\"}}"),
	} {
		if !QuotaExhausted(err) {
			t.Errorf("HTTP 402 must be quota: %v", err)
		}
	}
}

func TestQuotaExhaustedBodySignals(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{429, "insufficient_quota"},
		{429, `{"error":{"message":"Insufficient Quota"}}`},
		{400, "account quota exceeded"},
		{500, "payment required upstream"},
		{402, "余额不足，请充值后重试"},
		{402, "额度不足"},
	} {
		if !QuotaExhausted(apiErr(tc.status, tc.body)) {
			t.Errorf("quota body must classify: status %d body %q", tc.status, tc.body)
		}
	}
}

func TestQuotaExhaustedIgnoresNonQuotaFailures(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("network: connection reset"),
		apiErr(429, "rate limit exceeded, retry after 30s"),
		apiErr(429, "Too Many Requests"),
		apiErr(401, "invalid api key"),
		apiErr(403, "forbidden"),
		apiErr(400, "prompt is too long: 900000 > 200000 context length"),
		apiErr(500, "upstream server error"),
		&AuthError{},
	} {
		if QuotaExhausted(err) {
			t.Errorf("must not classify as quota: %v", err)
		}
	}
}

func TestQuotaExhaustedWrapped(t *testing.T) {
	wrapped := errors.Join(errors.New("outer"), apiErr(402, ""))
	if !QuotaExhausted(wrapped) {
		t.Error("a wrapped 402 APIError must classify as quota")
	}
}

func TestQuotaExhaustedNonAPIErrorBody(t *testing.T) {
	// A body-only signal on an error that is not an APIError is not quota: the
	// classifier never guesses from an unwrapped error string.
	if QuotaExhausted(errors.New("insufficient_quota")) {
		t.Error("an unwrapped error mentioning quota must not classify")
	}
}
