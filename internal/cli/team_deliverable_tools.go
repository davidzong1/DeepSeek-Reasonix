package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// The member-facing view of team.DeliverableStore: the member identity is bound
// at assembly, so a caller can neither name the team nor publish as another
// member. The publish tool stays write-class and the read tools stay read-only,
// so no existing boundary moves.
const (
	publishDeliverableName = "member_publish_deliverable"
	readDeliverableName    = "member_read_deliverable"
	listDeliverableName    = "member_list_deliverables"
)

// deliverableTool is one tool of the deliverable surface, bound to the team and
// member identity of the session it was assembled for.
type deliverableTool struct {
	name     string
	desc     string
	schema   json.RawMessage
	teamName string
	memberID string
	publish  bool
	// warn is the host's diagnostic writer; every refusal and every failed call
	// logs one line there, and a host that passed none gets a discarded line
	// rather than a silent one.
	warn io.Writer

	mu    sync.Mutex
	store *team.DeliverableStore
}

func (t *deliverableTool) Name() string            { return t.name }
func (t *deliverableTool) Description() string     { return t.desc }
func (t *deliverableTool) Schema() json.RawMessage { return t.schema }

// ReadOnly and PlanModeSafe separate the read half of the surface from the
// publish half, so a read-only or plan turn keeps its read of the team's
// documents and still cannot add one.
func (t *deliverableTool) ReadOnly() bool     { return !t.publish }
func (t *deliverableTool) PlanModeSafe() bool { return !t.publish }

// logLine writes one structured line about a refused call. The store's own
// refusals carry the team, the id and the category; the tool never logs the
// body it was handed or any host path.
func (t *deliverableTool) logLine(format string, args ...any) {
	writer := t.warn
	if writer == nil {
		writer = io.Discard
	}
	fmt.Fprintf(writer, "deliverable: "+format+"\n", args...)
}

// open resolves the store once per tool, always with this tool's logger. A
// session without a resolvable user state root fails here, naming the reason
// rather than writing a document somewhere no peer would look.
func (t *deliverableTool) open() (*team.DeliverableStore, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.store != nil {
		return t.store, nil
	}
	store, err := team.NewDeliverableStore(t.logLine)
	if err != nil {
		t.logLine("tool=%s kind=no-root team=%q", t.name, t.teamName)
		return nil, fmt.Errorf("%s: %w", t.name, err)
	}
	t.store = store
	return store, nil
}

func (t *deliverableTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	if strings.TrimSpace(t.teamName) == "" || strings.TrimSpace(t.memberID) == "" {
		t.logLine("tool=%s kind=unbound", t.name)
		return "", fmt.Errorf("%s: no team or member identity is bound to this tool", t.name)
	}
	store, err := t.open()
	if err != nil {
		return "", err
	}
	switch t.name {
	case publishDeliverableName:
		return t.publishDeliverable(store, args)
	case readDeliverableName:
		return t.readDeliverable(store, args)
	case listDeliverableName:
		return t.listDeliverables(store)
	}
	return "", fmt.Errorf("%s: unsupported operation", t.name)
}

func (t *deliverableTool) publishDeliverable(store *team.DeliverableStore, args json.RawMessage) (string, error) {
	var p struct {
		Slug string `json:"slug"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		t.logLine("tool=%s kind=bad-args team=%q", t.name, t.teamName)
		return "", fmt.Errorf("%s: invalid arguments: %w", t.name, err)
	}
	item, err := store.Publish(t.teamName, t.memberID, strings.TrimSpace(p.Slug), []byte(p.Body))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("published %s (%d bytes, sha256 %s)\nreading it back needs only this id: %s",
		item.ID, item.Size, item.SHA256, item.ID), nil
}

func (t *deliverableTool) readDeliverable(store *team.DeliverableStore, args json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		t.logLine("tool=%s kind=bad-args team=%q", t.name, t.teamName)
		return "", fmt.Errorf("%s: invalid arguments: %w", t.name, err)
	}
	body, err := store.Read(t.teamName, strings.TrimSpace(p.ID))
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return fmt.Sprintf("deliverable %s is an empty document", p.ID), nil
	}
	return string(body), nil
}

func (t *deliverableTool) listDeliverables(store *team.DeliverableStore) (string, error) {
	items, err := store.List(t.teamName)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return fmt.Sprintf("team %q has no deliverables; publish one with %s", t.teamName, publishDeliverableName), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "team %q deliverables (%d):", t.teamName, len(items))
	for _, item := range items {
		fmt.Fprintf(&b, "\n%s  %dB  %s", item.ID, item.Size, item.SHA256[:12])
	}
	return b.String(), nil
}

// newMemberDeliverableTools assembles the member half of the surface: publish
// (own identity only), read and list.
func newMemberDeliverableTools(teamName, memberID string, warn io.Writer) []tool.Tool {
	if strings.TrimSpace(teamName) == "" || strings.TrimSpace(memberID) == "" {
		return nil
	}
	return []tool.Tool{
		&deliverableTool{
			name:     publishDeliverableName,
			desc:     "Publish a deliverable document for this team and return its stable id. Repeated publishes of the same body are a no-op; new content gets a new id and the earlier document stays readable. Member-only, bound to your own identity.",
			schema:   json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string"},"body":{"type":"string"}},"required":["slug","body"],"additionalProperties":false}`),
			teamName: teamName, memberID: memberID, publish: true, warn: warn,
		},
		readDeliverable(teamName, memberID, warn),
		listDeliverables(teamName, memberID, warn),
	}
}

// newLeaderDeliverableTools assembles the leader half: the whole team's
// documents are readable, and nothing here publishes.
func newLeaderDeliverableTools(teamName, memberID string, warn io.Writer) []tool.Tool {
	if strings.TrimSpace(teamName) == "" || strings.TrimSpace(memberID) == "" {
		return nil
	}
	return []tool.Tool{readDeliverable(teamName, memberID, warn), listDeliverables(teamName, memberID, warn)}
}

func readDeliverable(teamName, memberID string, warn io.Writer) tool.Tool {
	return &deliverableTool{
		name:     readDeliverableName,
		desc:     "Read one deliverable document of this team by its id. Read-only.",
		schema:   json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
		teamName: teamName, memberID: memberID, warn: warn,
	}
}

func listDeliverables(teamName, memberID string, warn io.Writer) tool.Tool {
	return &deliverableTool{
		name:     listDeliverableName,
		desc:     "List this team's deliverable documents in stable ascending id order. Read-only.",
		schema:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		teamName: teamName, memberID: memberID, warn: warn,
	}
}
