package config

import "reasonix/internal/provider/openai"

// migrateStoredDeepSeekEffort preserves the wire value of saved pre-contract
// aliases. New user selections still go through strict NormalizeEffort
// validation. The split is deliberate: `/effort medium` on the same entry is
// refused, because a selection just made must not be re-spelled, while a value
// already on disk keeps the depth it was chosen for. Gated on an undeclared
// vocabulary so an endpoint's own supported_efforts stays authoritative; the
// stored value is never rewritten.
func migrateStoredDeepSeekEffort(e *ProviderEntry, effort string) string {
	if (effort == "medium" || effort == "xhigh") && (ReasoningProtocolForEntry(e) == ReasoningProtocolDeepSeek || (explicitReasoningProtocol(e) == "" && openai.IsDeepSeek(e.BaseURL))) && len(e.SupportedEfforts) == 0 {
		cap := ReasoningCapabilityForEntry(e)
		if cap.Validate(e.Model, "high") == nil {
			return "high"
		}
	}
	return effort
}
