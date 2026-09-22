package hook

import (
	"slices"
	"strings"

	"reasonix/internal/shellsafe"
)

// A tool-call hook is user shell code, so the workspace write lease it forces is
// sized from evidence: only a statically bounded command narrows it, and an
// explicit writeScope declaration narrows an unproven hook but never a proof.

const (
	// WriteScopeNone is the declared writeScope of a hook that writes nothing.
	WriteScopeNone = "none"
	// WriteScopeToolCall is the declared writeScope of a hook that only rewrites
	// the files the tool call itself names.
	WriteScopeToolCall = "tool"
)

// ToolCallWriteSurface is what the tool-call hooks firing for one tool can write.
// WholeWorkspace is authoritative: when it is set the caller must take the whole
// workspace regardless of the other fields.
type ToolCallWriteSurface struct {
	// Fires reports whether any Pre/PostToolUse/PostToolUseFailure hook applies
	// to this tool.
	Fires bool
	// Paths are the literal paths those hooks are proven to write.
	Paths []string
	// ToolPathsScoped reports that a firing hook is bounded to the paths the tool
	// call names, by proof or by declaration.
	ToolPathsScoped bool
	// WholeWorkspace reports that a firing hook could not be bounded.
	WholeWorkspace bool
}

// ToolCallWriteSurface reports what this session's tool-call hooks can write when
// they run around toolName.
func (r *Runner) ToolCallWriteSurface(toolName string) ToolCallWriteSurface {
	if r == nil || !r.Enabled() {
		return ToolCallWriteSurface{}
	}
	surface := ToolCallWriteSurface{}
	for _, h := range r.hooks {
		if !UsesToolMatcher(h.Event) || !MatchesTool(h, toolName) {
			continue
		}
		surface.Fires = true
		switch proof, paths := classifyHookWrites(h); proof {
		case hookWritesWholeWorkspace:
			surface.WholeWorkspace = true
		case hookWritesToolCall:
			surface.ToolPathsScoped = true
		case hookWritesLiteral:
			surface.Paths = append(surface.Paths, paths...)
		}
	}
	slices.Sort(surface.Paths)
	surface.Paths = slices.Compact(surface.Paths)
	return surface
}

type hookWriteProof uint8

const (
	hookWritesNone hookWriteProof = iota
	hookWritesLiteral
	hookWritesToolCall
	hookWritesWholeWorkspace
)

// classifyHookWrites bounds one hook's writes. The order matters: a proof always
// wins, a declaration only fills the gap a proof leaves.
func classifyHookWrites(h ResolvedHook) (hookWriteProof, []string) {
	// A plugin contextFile hook reads that file and emits it as stdout.
	if h.Scope == ScopePlugin && strings.TrimSpace(h.ContextFile) != "" {
		return hookWritesNone, nil
	}
	if paths, ok := shellsafe.StaticWritePaths(h.Command); ok {
		return hookWritesLiteral, paths
	}
	if effect := shellsafe.ClassifyBash(h.Command); effect.Certainty == shellsafe.EffectKnown && effect.Writes == 0 {
		return hookWritesNone, nil
	}
	switch strings.ToLower(strings.TrimSpace(h.WriteScope)) {
	case WriteScopeNone:
		return hookWritesNone, nil
	case WriteScopeToolCall:
		return hookWritesToolCall, nil
	}
	return hookWritesWholeWorkspace, nil
}
