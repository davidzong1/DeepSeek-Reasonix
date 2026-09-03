package shellsafe

import "testing"

// TestClassifySed verifies the sed command classification: stream operations
// are known readers, and in-place edits / script output / exec / file reads
// all fail closed or are explicit writers.
func TestClassifySed(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		certainty  Certainty
		writes     WriteDomain
		permission bool
		executes   bool
	}{
		// Safe: pure stream readers
		{
			name: "substitute print", certainty: EffectKnown, permission: true,
			command: `sed -n 's/a/b/p' file`,
		},
		{
			name: "delete lines", certainty: EffectKnown, permission: true,
			command: `sed -n '/^#/d' file`,
		},
		{
			name: "print line number", certainty: EffectKnown, permission: true,
			command: `sed -n '5p' file`,
		},
		{
			name: "address range print", certainty: EffectKnown, permission: true,
			command: `sed -n '1,10p' file`,
		},
		{
			name: "multiline next", certainty: EffectUnknown,
			command: `sed 'N' file`,
		},
		{
			name: "quiet mode substitute", certainty: EffectKnown, permission: true,
			command: `sed -n 's/foo/bar/g' file`,
		},
		{
			name: "select string", certainty: EffectKnown, permission: true,
			command: `sed -n '/pattern/p' file`,
		},
		{
			name: "multiple expressions", certainty: EffectKnown, permission: true,
			command: `sed -n -e 's/a/b/' -e 's/c/d/p' file`,
		},
		{
			name: "bundled short flags", certainty: EffectKnown, permission: true,
			command: `sed -nE 's/a/b/p' file`,
		},
		{
			name: "label and branch", certainty: EffectKnown, permission: true,
			command: `sed -n '/pattern/btarget; :target p' file`,
		},
		{
			name: "transliterate", certainty: EffectUnknown,
			command: `sed 'y/abc/xyz/' file`,
		},
		{
			name: "hold space exchange", certainty: EffectUnknown,
			command: `sed 'x' file`,
		},
		{
			name: "help", certainty: EffectKnown, permission: true,
			command: `sed --help`,
		},
		{
			name: "version", certainty: EffectKnown, permission: true,
			command: `sed --version`,
		},
		// Safe: compound with other readers
		{
			name: "pipe after sed", certainty: EffectKnown, permission: true,
			command: `sed -n 's/a/b/p' file | head -c 400`,
		},
		// Dangerous: in-place edit
		{
			name: "in-place short", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed -i 's/a/b/' file.txt`,
		},
		{
			name: "in-place long", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed --in-place 's/a/b/' file.txt`,
		},
		{
			name: "in-place with backup", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed -i.bak 's/a/b/' file.txt`,
		},
		{
			name: "in-place bundled with n", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed -ni 's/a/b/p' file.txt`,
		},
		// Dangerous: e flag (exec)
		{
			name: "e flag in s", certainty: EffectUnknown, executes: true,
			command: `sed -n 's/foo/bar/e' file.txt`,
		},
		{
			name: "e flag in script", certainty: EffectUnknown, executes: true,
			command: `sed -n 'e touch x' file.txt`,
		},
		// Dangerous: w/W flag (write file)
		{
			name: "w flag in s", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed -n 's/foo/bar/w out.txt' file.txt`,
		},
		{
			name: "W flag in s", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed -n 's/foo/bar/W out.txt' file.txt`,
		},
		{
			name: "w cmd in script", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed -n 'w out.txt' file.txt`,
		},
		{
			name: "W cmd in script", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed -n 'W out.txt' file.txt`,
		},
		// Dangerous: r/R (read file)
		{
			name: "r cmd in script", certainty: EffectUnknown,
			command: `sed -n 'r /etc/passwd' file.txt`,
		},
		{
			name: "R cmd in script", certainty: EffectUnknown,
			command: `sed -n 'R /etc/passwd' file.txt`,
		},
		// Dangerous: -f script file (unknown effects)
		{
			name: "file flag", certainty: EffectUnknown,
			command: `sed -f script.sed file.txt`,
		},
		// Dangerous: compound with in-place writer (the pipe keeps the writer's
		// Known certainty and content-write bit, so the call is not a reader)
		{
			name: "in-place pipe to reader", certainty: EffectKnown, writes: WriteWorkspaceContent,
			command: `sed -i 's/a/b/' file | head -5`,
		},
		// No script = unknown
		{
			name: "no script", certainty: EffectUnknown,
			command: `sed file.txt`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyBash(tt.command)
			if got.Certainty != tt.certainty || got.Writes != tt.writes ||
				got.IsPermissionReader() != tt.permission || got.ExecutesCode != tt.executes {
				t.Errorf("ClassifyBash(%q) = %+v, want certainty=%v writes=%v permission=%t executes=%t",
					tt.command, got, tt.certainty, tt.writes, tt.permission, tt.executes)
			}
		})
	}
}
