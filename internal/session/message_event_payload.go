package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/provider"
)

type messageRetractPayload struct {
	MessageIDs []string `json:"messageIds"`
	Reason     string   `json:"reason,omitempty"`
}

func retractedMessageIDs(event Event, payload json.RawMessage) ([]string, error) {
	var body messageRetractPayload
	if err := strictPayload(payload, &body); err != nil {
		return nil, damagedPayload(event, err)
	}
	if len(body.MessageIDs) == 0 {
		return nil, damagedPayload(event, fmt.Errorf("empty messageIds"))
	}
	seen := make(map[string]bool, len(body.MessageIDs))
	for _, id := range body.MessageIDs {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || seen[id] {
			return nil, damagedPayload(event, fmt.Errorf("invalid or duplicate message id"))
		}
		seen[id] = true
	}
	return body.MessageIDs, nil
}

// Execution, recovery, history and search must decode the same durable event
// schema. Projection-specific DTOs used to reject valid rewrite/import metadata.
type historyReplacePayload struct {
	Messages []provider.Message `json:"messages"`
	Reason   string             `json:"reason,omitempty"`
	Sources  []uint64           `json:"sourceSequences,omitempty"`
}

type legacyImportPayload struct {
	Source        Source             `json:"source"`
	Messages      []provider.Message `json:"messages"`
	Goal          json.RawMessage    `json:"goal,omitempty"`
	ModelRef      string             `json:"modelRef,omitempty"`
	ModelIdentity string             `json:"modelIdentity,omitempty"`
}

func replacementEventMessages(event Event, payload json.RawMessage) ([]provider.Message, error) {
	var messages []provider.Message
	var err error
	if event.Kind == "legacy/import" {
		var body legacyImportPayload
		err = strictPayload(payload, &body)
		messages = body.Messages
	} else {
		var body historyReplacePayload
		err = strictPayload(payload, &body)
		messages = body.Messages
	}
	if err != nil || messages == nil {
		return nil, damagedPayload(event, err)
	}
	return messages, nil
}
