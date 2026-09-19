package runtimepolicy

import (
	"encoding/json"
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

// unprovenCommands keep the fail-closed half: real writes, write-capable
// argument forms, and anything whose effects are not statically known —
// including the interpreters that are deliberately absent from the reader
// tables (awk, jq, python).
var unprovenCommands = []string{
	"rm -rf build/",
	"find . -delete",
	"find . -exec rm {} ;",
	"sort --output=out f",
	"git branch -D feature",
	"git commit -am checkpoint",
	"go env -w GOFLAGS=-mod=mod",
	"xxd -r dump.hex out.bin",
	"xxd -rs 5 dump.hex out.bin",
	// xxd writes its second operand in every mode, not only under -r: these
	// forms are the fail-open tester reported, and the option values around
	// them must not be mistaken for the operand that counts.
	"xxd firmware.bin out.hex",
	"xxd -l 64 firmware.bin out.hex",
	"xxd -l64 firmware.bin out.hex",
	"xxd -s -5 firmware.bin out.hex",
	"xxd -ps payload.bin out.txt",
	"xxd -i firmware.bin header.h",
	"xxd -n label firmware.bin header.h",
	"base64 -o out.b64 in.bin",
	"tree -o tree.txt .",
	"nl notes.txt > numbered.txt",
	"git log --oneline | awk '{print $1}'",
	"cat data.json | python3 -m json.tool",
	"cat data.json | jq .items",
	"nl $(ls internal/shellsafe)",
	"custom-tool --run",
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
	for _, command := range unprovenCommands {
		got := guard.BeforeTool(bashCall(t, command))
		if got.Action != GuardDeny || got.Message != constraintDenialMessage {
			t.Errorf("%q: decision=%+v (message %q), want deny with %q", command, got, got.Message, constraintDenialMessage)
		}
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
