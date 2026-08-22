package agentruntime

// InstanceKey is the runtime composite key of one member instance (route
// §2.1): team plus member id. Every runtime handle, context directory, and
// recovery cursor derives from it; AgentUserRef never enters the key, so
// sharing a config reference shares no runtime state.
type InstanceKey struct {
	Team     string
	MemberID string
}

// ConfigSnapshot is one assembly-time snapshot of the member's agent-user
// configuration. It is a value copy made from the registry entry; it never
// carries key material (K1), so instances sharing an AgentUserRef share only
// an immutable config view, never mutable state.
type ConfigSnapshot struct {
	AgentUserRef string // resolved reference; empty = team default was used
	Provider     string
	BaseURL      string
	Model        string
	Effort       string
}

// Spec describes one member-instance assembly: the instance key, the config
// snapshot, and the member's free-text role for system-prompt injection.
type Spec struct {
	Key        InstanceKey
	Config     ConfigSnapshot
	Role       string // free-text role; injected as data, never instructions
	BasePrompt string // base system prompt prefix
}
