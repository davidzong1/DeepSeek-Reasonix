package runtimepolicy

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/evidence"
)

// bashCall is the runtime input the guards actually see for a bash call:
// internal/agent's pipelineDecision classifies the command with
// evidence.ClassifyEffect and hands the resulting profile to BeforeTool. These
// cases go through the same door, so a hand-built profile cannot make them
// pass.
func bashCall(t *testing.T, command string) CallContext {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return CallContext{
		ToolName: "bash",
		Args:     args,
		Profile:  evidence.ClassifyEffect(evidence.EffectInput{ToolName: "bash", Args: args}),
	}
}

const constraintDenialMessage = "blocked: the current constraints forbid state mutation"

// provenReaders are commands the shell contract can prove read-only. A member
// working under a read-only task must still reach permission and execution with
// them; the constraint only forbids mutation.
var provenReaders = []string{
	"git log --oneline | nl",
	"git log --oneline | nl | head -20",
	"git diff | md5sum",
	"cat internal/shellsafe/effect.go | od -c | tail -5",
	"nl -ba internal/shellsafe/effect.go",
	"od -c notes.txt",
	"xxd -l 64 firmware.bin",
	"xxd -l 64 -c 8 firmware.bin",
	"xxd -l64 -o 8 firmware.bin",
	"xxd -s -5 firmware.bin",
	"xxd -ps payload.bin",
	"base64 -w0 payload.bin",
	"sha256sum ./artifact.bin",
	"tree -L 2 internal/shellsafe",
	"seq 1 5",
}

// unprovenCommands are the calls the shell contract cannot prove: write-capable
// argument forms the reader tables refuse to guess about (xxd's second operand,
// --output, redirection), interpreters deliberately absent from those tables
// (awk, jq, python), and anything whose shape is dynamic (a command
// substitution). They are refused — and the refusal says why, because that is
// what tells the model a proven-reader rewrite is possible.
var unprovenCommands = []string{
	"rm -rf build/",
	"find . -exec rm {} ;",
	// xxd writes its second operand in every mode, not only under -r: these
	// forms are the fail-open tester reported, and the option values around
	// them must not be mistaken for the operand that counts.
	"xxd -n label firmware.bin header.h",
	"git log --oneline | awk '{print $1}'",
	"cat data.json | python3 -m json.tool",
	"cat data.json | jq .items",
	"nl $(ls internal/shellsafe)",
	"custom-tool --run",
}

// provenWrites are calls whose write effects the contract does prove, so the
// ban refuses them as the state mutations they are, with the stable sentence.
var provenWrites = []string{
	"find . -delete",
	"sort --output=out f",
	"git branch -D feature",
	"git commit -am checkpoint",
	"go env -w GOFLAGS=-mod=mod",
	"xxd -r dump.hex out.bin",
	"xxd -rs 5 dump.hex out.bin",
	"xxd firmware.bin out.hex",
	"xxd -l 64 firmware.bin out.hex",
	"xxd -l64 firmware.bin out.hex",
	"xxd -s -5 firmware.bin out.hex",
	"xxd -ps payload.bin out.txt",
	"xxd -i firmware.bin header.h",
	"base64 -o out.b64 in.bin",
	"tree -o tree.txt .",
	"nl notes.txt > numbered.txt",
}

func TestConstraintGuardPassesProvenReadersUnderForbidMutation(t *testing.T) {
	guard := ConstraintGuard{Constraints: Constraints{ForbidMutation: true}}
	for _, command := range provenReaders {
		ctx := bashCall(t, command)
		if ctx.Profile.MutatesState() {
			t.Fatalf("%q: profile still reads as a state mutation (%+v); the reader assertion below would be vacuous", command, ctx.Profile)
		}
		if got := guard.BeforeTool(ctx); got.Action != GuardAbstain {
			t.Errorf("%q: decision=%+v (message %q), want abstain", command, got, got.Message)
		}
	}
}

func TestConstraintGuardDeniesWritesAndUnprovenCommandsUnderForbidMutation(t *testing.T) {
	guard := ConstraintGuard{Constraints: Constraints{ForbidMutation: true}}
	for _, command := range provenWrites {
		ctx := bashCall(t, command)
		if !ctx.Profile.Known {
			t.Fatalf("%q: profile reads unproven (%+v); the proven-write assertion below would be vacuous", command, ctx.Profile)
		}
		got := guard.BeforeTool(ctx)
		if got.Action != GuardDeny || got.Message != constraintDenialMessage {
			t.Errorf("%q: decision=%+v (message %q), want deny with %q", command, got, got.Message, constraintDenialMessage)
		}
	}
	for _, command := range unprovenCommands {
		ctx := bashCall(t, command)
		if ctx.Profile.Known {
			t.Fatalf("%q: profile reads proven (%+v); the unproven assertion below would be vacuous", command, ctx.Profile)
		}
		got := guard.BeforeTool(ctx)
		if got.Action != GuardDeny {
			t.Errorf("%q: decision=%+v, want deny", command, got)
			continue
		}
		if !strings.HasPrefix(got.Message, constraintDenialMessage+";") {
			t.Errorf("%q: message %q must keep the stable sentence as its prefix", command, got.Message)
		}
		if !strings.Contains(got.Message, string(ctx.Profile.Reason)) {
			t.Errorf("%q: message %q must name the classifier's reason %q", command, got.Message, ctx.Profile.Reason)
		}
	}
}

// TestMutationBanMessageTellsADecisionFromAGuess keeps the split honest: a
// proven write and an unprovable call are both refused, but only the second
// tells the model its effects were never established — which is the half the
// model can actually act on. Without this, "cannot prove" and "is a write"
// collapse back into one sentence and the loop returns.
func TestMutationBanMessageTellsADecisionFromAGuess(t *testing.T) {
	guard := ConstraintGuard{Constraints: Constraints{ForbidMutation: true}}
	proven := guard.BeforeTool(bashCall(t, "git commit -am checkpoint"))
	unproven := guard.BeforeTool(bashCall(t, "cat data.json | jq .items"))
	if proven.Message == unproven.Message {
		t.Fatalf("both halves share one message %q", proven.Message)
	}
	if !strings.Contains(unproven.Message, "turn that allows mutation") {
		t.Fatalf("the unproven refusal must name the way out, got %q", unproven.Message)
	}
}

// TestConstraintDenialIsTheConstraintNotTheCommand keeps the table above
// non-vacuous: without ForbidMutation the same writing and unproven commands
// abstain, so those denials are attributable to the constraint alone.
func TestConstraintDenialIsTheConstraintNotTheCommand(t *testing.T) {
	open := ConstraintGuard{}
	for _, command := range []string{"rm -rf build/", "git log --oneline | awk '{print $1}'"} {
		if got := open.BeforeTool(bashCall(t, command)); got.Action != GuardAbstain {
			t.Errorf("%q: unconstrained decision=%+v, want abstain", command, got)
		}
	}
}

// TestPlanGuardAlsoSeesProvenReaders covers the second user-visible denial that
// reads the same profile field: plan mode has its own message, so a reader must
// pass there too.
func TestPlanGuardAlsoSeesProvenReaders(t *testing.T) {
	guard := PlanGuard{}
	for _, command := range provenReaders {
		ctx := bashCall(t, command)
		ctx.PlanReadOnly = true
		if got := guard.BeforeTool(ctx); got.Action != GuardAbstain {
			t.Errorf("%q: decision=%+v (message %q), want abstain", command, got, got.Message)
		}
	}
	ctx := bashCall(t, "find . -delete")
	ctx.PlanReadOnly = true
	if got := guard.BeforeTool(ctx); got.Action != GuardDeny {
		t.Errorf("find -delete under plan mode: decision=%+v, want deny", got)
	}
}
