// Package agentruntime drives one task on one member's agent backend: the
// runtime stage of TASK.md §3.7, previously reserved as interface only. The
// scheduler stays a strategy layer; this package owns execution.
//
// The runtime talks to agents through the narrow AgentAPI port (hosts adapt
// their control.SessionAPI), persists the durable command inbox and the
// consumer watermark over the blackboard event log, and delivers leader
// wakeups through injected WakeFuncs. Every state move goes through
// team.TransitionTask, so the scheduler, the runtime and the report path
// share one migration map.
package agentruntime
