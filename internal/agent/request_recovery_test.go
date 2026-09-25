package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/extension/dispatch"
	"reasonix/internal/extension/protocol"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestRecoveredHistoryReachesModelWithoutExecutingTools(t *testing.T) {
	mp := &mockProvider{name: "fixture", chunks: []provider.Chunk{{Type: provider.ChunkDone}}}
	s := NewSession("system")
	s.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "old", Name: "write_file", Arguments: `{"body":"["text"]"}`}}})
	s.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "old", Name: "write_file", ToolRunState: provider.ToolRunUnknown, Content: "original outcome uncertain"})
	s.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
	a := New(mp, tool.NewRegistry(), s, Options{}, event.Discard)
	req, err := a.buildSamplingRequest(t.Context(), CompactionTriggerPressure)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := a.streamProviderRequest(t.Context(), req.req)
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	if len(mp.requests) != 1 || s.Snapshot()[1].ToolCalls[0].Arguments != `{"body":"["text"]"}` {
		t.Fatal("request did not reach provider or canonical arguments changed")
	}
}

func TestRequestExtensionRecoveryHonorsRequiredAndExplicitBlocks(t *testing.T) {
	for _, point := range []extension.InterceptorPoint{extension.PointContextPrepare, extension.PointProviderRequest} {
		for _, mode := range []string{"optional", "required", "block"} {
			t.Run(string(point)+"/"+mode, func(t *testing.T) {
				client := &fakeDispatchClient{interceptFn: func(ev protocol.InterceptEvent, raw json.RawMessage) (protocol.InterceptResult, error) {
					if mode == "block" {
						return blockWith("policy refused"), nil
					}
					var invalid []protocol.ProviderMessage
					if err := json.Unmarshal([]byte(`[{"role":"assistant","tool_calls":[{"id":"a","name":"read","arguments":"[]"}]}]`), &invalid); err != nil {
						t.Fatal(err)
					}
					if point == extension.PointContextPrepare {
						return replaceWith(t, dispatch.ContextPayload{Messages: invalid}), nil
					}
					var payload dispatch.ProviderRequestPayload
					if err := json.Unmarshal(raw, &payload); err != nil {
						t.Fatal(err)
					}
					payload.Request.Messages = invalid
					return replaceWith(t, payload), nil
				}}
				warnings := 0
				d := newExtDispatcher(client, mode == "required", func(string) { warnings++ }, point)
				a := New(&mockProvider{name: "fixture"}, tool.NewRegistry(), NewSession("system"), Options{Extensions: d}, event.Discard)
				got, err := a.buildSamplingRequest(t.Context(), CompactionTriggerPressure)
				if mode != "optional" {
					if err == nil {
						t.Fatal("required extension or explicit block bypassed")
					}
					return
				}
				if err != nil || warnings != 1 || len(got.req.Messages) != 1 || got.req.Messages[0].Content != "system" {
					t.Fatalf("optional invalid replacement did not preserve original request: %+v %v, warnings=%d", got, err, warnings)
				}
			})
		}
	}
}

func TestTruncationRecoversOversizedProtectedToolResult(t *testing.T) {
	a := New(&mockProvider{name: "fixture"}, tool.NewRegistry(), NewSession("sys"), Options{}, event.Discard)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "keep the latest request"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "large", Name: "read", Arguments: `{}`}}},
		{Role: provider.RoleTool, ToolCallID: "large", Name: "read", Content: strings.Repeat("large observation ", 10000)},
	}
	target := 5000
	got, affected := a.truncateView(messages, target)
	if affected == 0 || a.estimatedVisibleRequestTokens(got) >= target {
		t.Fatal("protected oversized result still blocks the request")
	}
	if got[1].Content != messages[1].Content || len(messages[3].Content) <= 2048 || !strings.Contains(got[3].Content, "omitted") {
		t.Fatal("latest user request or canonical tool result changed")
	}
	if err := provider.ValidateModelTranscript(got); err != nil {
		t.Fatal(err)
	}
}
