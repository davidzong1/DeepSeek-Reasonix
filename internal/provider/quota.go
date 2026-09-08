package provider

import (
	"errors"
	"net/http"
	"strings"
)

// QuotaExhausted reports whether err is a provider refusal a team agent pool
// can recover from by switching agent-user credentials: an APIError whose HTTP
// status is 402 (payment required), or any status whose body explicitly names
// quota or balance exhaustion. Plain rate limits (429 without a quota body),
// auth refusals, network drops and context-length failures all return false —
// they need the user or the caller, not another pool entry.
func QuotaExhausted(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status == http.StatusPaymentRequired {
		return true
	}
	return quotaBody(apiErr.Body)
}

// quotaSignals are the phrases providers and relays use for an account that
// ran out of credit, matched case-insensitively against the error body after
// underscores fold to spaces (so insufficient_quota and "Insufficient Quota"
// match one token). The English set includes the wording OpenAI-compatible
// APIs return; the Chinese set is the DeepSeek/relay wording users see.
var quotaSignals = []string{
	"insufficient quota", "quota exceeded", "out of quota", "payment required",
	"insufficient balance", "balance not enough", "no balance",
	"余额不足", "额度不足",
}

func quotaBody(body string) bool {
	lower := strings.ToLower(body)
	lower = strings.ReplaceAll(lower, "_", " ")
	for _, s := range quotaSignals {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}
