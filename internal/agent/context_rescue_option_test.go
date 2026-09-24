package agent

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// The switch crosses three hops: Options -> agentConfig -> the pre-send
// admission policy. A silent break in any of them leaves the feature inert
// while every unit below it still passes, so it is checked end to end.

// rescueOptionAgent is newRescueAgent with the rescue switch under test.
func rescueOptionAgent(t *testing.T, prov provider.Provider, msgs []provider.Message, enable bool) *Agent {
	t.Helper()
	sess := NewSession("")
	sess.Replace(msgs)
	return New(prov, tool.NewRegistry(), sess, Options{
		ContextWindow: rescueWindow, CompactRatio: 0.80, RecentKeep: 2, ArchiveDir: t.TempDir(),
		EnableContextRescue: enable,
	}, event.Discard)
}

func rescueOptionProvider() *rescueProvider {
	return &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor\n\n## Pending & next step\nrun the package tests",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}
}

// buildSamplingRequest is the pre-send admission the rescue must reach. Its
// policy is built inside the agent, so this is where "the option was wired"
// becomes observable rather than assumed.
func buildSamplingRequestWithOption(t *testing.T, enable bool) error {
	t.Helper()
	msgs := rescueOverCeilingTranscript()
	a := rescueOptionAgent(t, rescueOptionProvider(), msgs, enable)
	calibrateRescueEstimate(a, msgs)
	_, err := a.buildSamplingRequest(context.Background(), CompactionTriggerPressure)
	return err
}

func TestContextRescueOptionReachesThePreSendAdmission(t *testing.T) {
	err := buildSamplingRequestWithOption(t, true)
	if !errors.Is(err, ErrContextRescuePlanned) {
		t.Fatalf("opted-in admission err = %v, want ErrContextRescuePlanned", err)
	}
	if _, ok := ContextRescuePlanFromError(err); !ok {
		t.Fatal("the opted-in admission must carry the certified plan")
	}
}

func TestContextRescueIsOffByDefault(t *testing.T) {
	err := buildSamplingRequestWithOption(t, false)
	if errors.Is(err, ErrContextRescuePlanned) {
		t.Fatal("a build that never opted in must not report a rescue")
	}
	if _, ok := ContextRescuePlanFromError(err); ok {
		t.Fatal("a build that never opted in must not carry a plan")
	}
}
