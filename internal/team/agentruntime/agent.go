package agentruntime

// AgentAPI is the runtime's narrow view of one member's agent backend: the
// submit/cancel/turn surface needed to drive a task, cut from
// control.SessionAPI. Hosts adapt their backend to this port (compile-time:
// `var _ agentruntime.AgentAPI = (*control.Controller)(nil)` at the host);
// the runtime never depends on the controller package.
//
// The one submit variant a task needs beyond this surface —
// SubmitUserTurnFramedOrError, which keeps a dispatched order's wording from
// becoming the member's policy — is optional on purpose: see framedSubmitter
// in runtime.go. A backend that only satisfies this interface still drives
// tasks; it just cannot promise the framing.
type AgentAPI interface {
	Submit(input string)
	SubmitUserTurn(input, display string)
	// SubmitUserTurnOrError surfaces a refused submit; the runtime migrates
	// Start/Resume to it so a refused turn never persists a "running" task.
	SubmitUserTurnOrError(input, display string) error
	Cancel()
	Running() bool
	Turn() int
	Compose(text string) string
	Close()
}
