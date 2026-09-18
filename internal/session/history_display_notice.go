package session

import (
	"context"
	"encoding/json"
	"errors"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncontent"
)

func indexDisplayNotice(ctx context.Context, content *sessioncontent.Store, state *historyBuildState, event Event) error {
	payload := event.Payload
	if event.PayloadRef != nil {
		var err error
		payload, err = resolveContentPayload(ctx, content, *event.PayloadRef)
		if err != nil {
			return err
		}
	}
	var body struct {
		Type          string          `json:"type"`
		DisplayRecord json.RawMessage `json:"displayRecord"`
	}
	if len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return err
	}
	if body.Type != "display-notice-v1" {
		return nil
	}
	var message provider.Message
	if err := json.Unmarshal(body.DisplayRecord, &message); err != nil {
		return err
	}
	if message.Role != "notice" || message.ID == "" {
		return errors.New("invalid persisted display notice")
	}
	return indexOneMessageBody(ctx, content, state, message, event.Sequence, false, body.DisplayRecord)
}
