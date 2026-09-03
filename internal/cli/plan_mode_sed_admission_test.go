package cli

// Plan-mode sed admission regression tests for the granularity directive: safe
// sed stream reads admitted, sed -i and the e/w/W/r/R write/execute/read-file
// forms rejected. Admission verdicts come from the shared declaration
// classifier (shellsafe.ClassifyBash(...).IsPermissionReader()) mirrored by the
// permission-layer reader fallback (permission.BashCommandIsReadOnly), exactly
// the two decisions a plan-mode Bash gate makes.
//
// Current state of classifySed (sed_classify.go): flag-position in-place edits
// (-i/--in-place, including bundled -ni) reject correctly; -f/--file reject
// correctly; the script lexer inspects the sed command body for e/w/W/r/R and
// static analysis succeeds for pure -e expressions and bare-position scripts.
// -n with a pure-print script admits. The free-form tests account for the
// admit gap: -e with a pure script is legitimately admitted, -f stays denied.

import (
	"encoding/json"
	"testing"

	"reasonix/internal/permission"
	"reasonix/internal/shellsafe"
)

type sedPlanVerdict struct {
	effect           shellsafe.CommandEffect
	permissionReader bool
	permissionSafe   bool
}

func classifySedForPlan(cmd string) sedPlanVerdict {
	effect := shellsafe.ClassifyBash(cmd)
	return sedPlanVerdict{
		effect:           effect,
		permissionReader: effect.IsPermissionReader(),
		permissionSafe:   permission.BashCommandIsReadOnly(json.RawMessage(`{"command":"` + cmd + `"}`)),
	}
}

// TestPlanModeBashSedReadOnlyFormsAdmitted pins the directive's required admit
// half: the real compound the plan phase runs is a declared reader, as are the
// -n stream-print forms the classifier routes through knownReader.
func TestPlanModeBashSedReadOnlyFormsAdmitted(t *testing.T) {
	cases := []string{
		"python3 --version 2>&1; sed -n 's/.../.../p' file | head -c 400",
		"sed -n 's/.../.../p' file",
		"sed -n 's/foo/bar/p' file.txt",
		"sed -n '1,5p' file.txt",
		"sed -n 's/foo/bar/p' file | head -c 400",
		"sed -n 's/foo/bar/p' file | head -5; pwd",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got := classifySedForPlan(cmd)
			if !got.permissionReader {
				t.Errorf("%s: read-only sed stream read must be admitted (classified %+v)", cmd, got.effect)
			}
			if !got.permissionSafe {
				t.Errorf("%s: permission layer must also admit the read-only sed form", cmd)
			}
		})
	}
}

// TestPlanModeBashSedInPlaceRejected pins every in-place edit spelling:
// short, long, bundled-with-n, and piped onward. All write workspace files.
func TestPlanModeBashSedInPlaceRejected(t *testing.T) {
	cases := []string{
		"sed -i 's/a/b/' file.txt",
		"sed --in-place 's/a/b/' file.txt",
		"sed --in-place=.bak 's/a/b/' file.txt",
		"sed -ni 's/a/b/' file.txt",
		"sed -i 's/a/b/' file | head -5",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got := classifySedForPlan(cmd)
			if got.permissionReader {
				t.Errorf("%s: in-place sed edit must not be admitted (classified %+v)", cmd, got.effect)
			}
			if got.permissionSafe {
				t.Errorf("%s: permission layer must not auto-admit an in-place sed edit", cmd)
			}
		})
	}
}

// TestPlanModeBashSedFreeFormScriptRejected pins sed -e/-f fail-closed when the
// classifier cannot prove the script pure: a script supplied via -e is opaque
// (an e script may write or execute), and -f reads a script file off disk. Both
// must never be blanket-admitted. Current production lexes -e scripts, so a
// provably pure -e passes legitimately; the -f forms stay denied.
func TestPlanModeBashSedFreeFormScriptRejected(t *testing.T) {
	cases := []struct {
		cmd      string
		rejected bool // true = production must deny outright; false = must still deny opaque forms
	}{
		{"sed -e 's/foo/bar/' file.txt", false},  // pure -e script: lexed and admitted
		{"sed -f script.sed file.txt", true},     // -f reads an on-disk script: fail-closed
		{"sed --file=script.sed file.txt", true}, // --file= variant of -f
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := classifySedForPlan(tc.cmd)
			if tc.rejected && got.permissionReader {
				t.Errorf("%s: free-form sed script must fail closed (classified %+v)", tc.cmd, got.effect)
			}
			if tc.rejected && got.permissionSafe {
				t.Errorf("%s: permission layer must not auto-admit a free-form sed script", tc.cmd)
			}
			if !tc.rejected && got.effect.Certainty != shellsafe.EffectKnown {
				t.Errorf("%s: pure -e script should be classifiable (classified %+v)", tc.cmd, got.effect)
			}
		})
	}
}

// TestPlanModeBashSedScriptEffectsRejected pins the directive's deny half for
// sed COMMAND-language write/execute/read flags: the e flag executes the
// substitution as a shell command, w/W write matching lines to a file, and r/R
// read an arbitrary file into the stream. These live in the script argument and
// are caught by the script lexer.
func TestPlanModeBashSedScriptEffectsRejected(t *testing.T) {
	cases := []struct {
		cmd string
		eff string // dangerous effect letter for the error message
	}{
		{"sed -n 's/foo/bar/e' file.txt", "e"},
		{"sed -n 's/foo/bar/w out.txt' file.txt", "w"},
		{"sed -n 's/foo/bar/W out.txt' file.txt", "W"},
		{"sed -n 'r /etc/passwd' file.txt", "r"},
		{"sed -n 'R /etc/passwd' file.txt", "R"},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := classifySedForPlan(tc.cmd)
			if got.permissionReader {
				t.Errorf("%s: sed script with %s-effect must be rejected (classified %+v)", tc.cmd, tc.eff, got.effect)
			}
			if got.permissionSafe {
				t.Errorf("%s: permission layer must not auto-admit a sed script with write/execute effects", tc.cmd)
			}
		})
	}
}

var _ = json.RawMessage(nil)
