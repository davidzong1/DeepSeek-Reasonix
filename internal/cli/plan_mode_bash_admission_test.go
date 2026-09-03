package cli

// Plan-mode Bash admission regression tests. These pin the shared declaration
// classifier that every plan-mode Bash gate reads:
//
//   - the permission gate reclassifies bash as read-only when
//     shellsafe.ClassifyBash(...).IsPermissionReader() returns true
//   - internal/runtimepolicy guard.go's PlanGuard / ConstraintGuard hard-deny
//     any Bash call whose evidence profile MutatesState()
//   - readOnlyBash (agent/task.go) refuses, at Execute time, any call that
//     BashCommandIsReadOnly() rejects (foreground requirement included)
//
// None of these gates treat a user command's own exit status as a denial: a
// read-only probe that happens to exit 1 (e.g. grep with no match) is a plain
// command result, never a "blocked" event.
//
// The verdicts are leader/member-symmetric: PlanGuard/ConstraintGuard key on
// the shared EffectProfile, so both produce exactly the same outcome.

import (
	"encoding/json"
	"testing"

	"reasonix/internal/permission"
	"reasonix/internal/shellsafe"
)

// planBashReader records the classification a plan-mode gate computes before a
// call is admitted (permissionReader true) or left to the mutation lane.
type planBashReader struct {
	effect           shellsafe.CommandEffect
	permissionReader bool
	permissionSafe   bool
}

type planBashCommand struct {
	cmd string
	sub planBashReader
}

// classifyForPlan runs the two admission decisions a plan-mode bash gate makes.
func classifyForPlan(cmd string) planBashCommand {
	effect := shellsafe.ClassifyBash(cmd)
	return planBashCommand{
		cmd: cmd,
		sub: planBashReader{
			effect:           effect,
			permissionReader: effect.IsPermissionReader(),
			permissionSafe:   permission.BashCommandIsReadOnly(json.RawMessage(`{"command":"` + cmd + `"}`)),
		},
	}
}

// TestPlanModeBashReadOnlyChecksPass pins that the probes a plan phase needs
// are classified read-only by the declaration classifier and admitted by the
// permission-layer reader fallback — reads under plan mode should not be forced
// into the approval channel.
func TestPlanModeBashReadOnlyChecksPass(t *testing.T) {
	cases := []string{
		"pwd",
		"ls -la",
		"cat go.mod",
		"grep -n Foo internal/team/member.go", // a dry probe; zero matches exits 1 as a normal result
		"python3 --version",                   // declared read-only via readOnlyPrefixes
		"git status",
		"git log --oneline",
		"go vet ./...",
		"ps aux",
		"find . -name '*.go'",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got := classifyForPlan(cmd)
			if !got.sub.permissionReader {
				t.Errorf("%s: reader decision must admit the command (exit 1 is a normal result, not a denial); classified %+v", cmd, got.sub.effect)
			}
			if !got.sub.permissionSafe {
				t.Errorf("%s: permission-layer BashCommandIsReadOnly must also admit", cmd)
			}
		})
	}
}

// TestPlanModeBashReadOnlyCompoundPipesPass pins that read-only segments wired
// together by pipes or separators stay admitted: nothing in the call mutates,
// so a verified inspection is still an inspection.
func TestPlanModeBashReadOnlyCompoundPipesPass(t *testing.T) {
	cases := []string{
		"grep Foo . | head -5",
		"head -5 a.txt | tail -2",
		"ls -la | wc -l; pwd",
		"git status --short; git diff --stat",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got := classifyForPlan(cmd)
			if !got.sub.permissionReader {
				t.Errorf("%s: read-only compound command must be admitted", cmd)
			}
			if !got.sub.permissionSafe {
				t.Errorf("%s: permission-layer reader fallback must also admit the compound", cmd)
			}
		})
	}
}

// TestPlanModeBashWritesStillBlocked pins the mutation half of the fork: plan
// mode aims at inspectability, not weakened writes. Every writer — file,
// redirection, process, external — must keep being pushed out of the reader
// lane.
func TestPlanModeBashWritesStillBlocked(t *testing.T) {
	cases := []string{
		"sed -i 's/a/b/' file.txt", // in-place write flips the reader
		"touch marker.go",
		"rm -rf build",
		"mv a b",
		"cp a b",
		"echo hi > file.txt",               // output redirection to a real file
		"echo hi >> file.txt",              // append redirection
		"git push",                         // network + external state
		"git commit -m x",                  // repository metadata
		"git remote add o url",             // repository metadata
		"python3 -c 'open(\"x\",\"w\")'",   // inline interpreter stays opaque
		"python3 -c 'print(1)' > file.txt", // interpreter + redirect
		"cat a | tee b",                    // pipe to a writer
		"grep Foo . && touch marker",       // chain with a mutation anywhere
		"ls; rm x",                         // separator with a mutation anywhere
		"git status > out.txt",             // read-only base + write redirect
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got := classifyForPlan(cmd)
			if got.sub.permissionReader {
				t.Errorf("%s: writer must stay banned (classified %+v)", cmd, got.sub.effect)
			}
			if got.sub.permissionSafe {
				t.Errorf("%s: permission layer must not auto-admit a writer", cmd)
			}
		})
	}
}

// TestPlanModeBashGreplessNotABlock pins that a read-only probe's own reaction
// (grep exits 1 on zero matches) is not turned into a policy denial — the
// admission verdict is already allowed; the exit code is a command result.
func TestPlanModeBashGreplessNotABlock(t *testing.T) {
	cmd := "grep -nf /dev/null /dev/null"
	got := classifyForPlan(cmd)
	if !got.sub.permissionReader {
		t.Errorf("%s: zero-match grep (exit 1) must still be admitted; exit status is never a denial (classified %+v)", cmd, got.sub.effect)
	}
	if !got.sub.permissionSafe {
		t.Errorf("%s: no-match grep must not surface as blocked in the permission layer", cmd)
	}
}

var _ = json.RawMessage(nil)
