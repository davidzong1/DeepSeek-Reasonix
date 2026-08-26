package agentruntime

// AgentAPI is the runtime's narrow view of one member's agent backend: the
// submit/cancel/turn surface needed to drive a task, cut from
// control.SessionAPI. Hosts adapt their backend to this port (compile-time:
// `var _ agentruntime.AgentAPI = (*control.Controller)(nil)` at the host);
// the runtime never depends on the controller package.
type AgentAPI interface {
	Submit(input string)
	SubmitUserTurn(input, display string)
	Cancel()
	Running() bool
	Turn() int
	Compose(text string) string
	Close()
}
