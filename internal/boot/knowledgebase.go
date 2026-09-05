package boot

import (
	"errors"
	"time"

	"reasonix/internal/knowledge_base/adapter"
	"reasonix/internal/knowledge_base/manager"
)

// KBHost adapts a host runtime to the knowledge-base adapter contract. The
// compile-time assertion pins the Manager's expectations to this host.
type KBHost struct {
	// Now overrides the wall clock (tests); nil means time.Now.
	Now         func() time.Time
	AllowLLM    bool
	MaxLLMCalls int
	OnEvent     func(adapter.Event)
}

func (h *KBHost) Clock() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *KBHost) Quota() adapter.Quota {
	return adapter.Quota{AllowLLM: h.AllowLLM, MaxLLMCalls: h.MaxLLMCalls}
}

func (h *KBHost) Emit(e adapter.Event) {
	if h.OnEvent != nil {
		h.OnEvent(e)
	}
}

var _ adapter.Adapter = (*KBHost)(nil)

// TeamKnowledge is an opened, team-bound knowledge base a host owns. The
// Manager worker runs until Close; the host closes it with its runtime.
type TeamKnowledge struct {
	Team     string
	DataRoot string
	Manager  *manager.Manager
}

// Close drains and stops the Manager's worker. Nil-safe and idempotent.
func (tk *TeamKnowledge) Close() error {
	if tk == nil || tk.Manager == nil {
		return nil
	}
	return tk.Manager.Close()
}

// OpenTeamKnowledge is the boot/control entry hosts call once per team: it
// binds the Manager to exactly one team under dataRoot and starts its durable
// worker. host carries the runtime's clock, quota, and event sink; dataRoot ""
// uses the knowledge_base default. The returned handle owns the Manager until
// Close.
func OpenTeamKnowledge(host *KBHost, teamID, dataRoot string) (*TeamKnowledge, error) {
	if host == nil {
		return nil, errors.New("boot: nil knowledge host")
	}
	m, err := manager.New(host, manager.Options{DataRoot: dataRoot, TeamID: teamID})
	if err != nil {
		return nil, err
	}
	return &TeamKnowledge{Team: teamID, DataRoot: dataRoot, Manager: m}, nil
}
