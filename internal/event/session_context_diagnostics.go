package event

// CacheDiagnostics describes whether and why the cacheable prefix changed.
type CacheDiagnostics struct {
	PrefixHash    string
	PrefixChanged bool
	// PrefixChangeReasons holds values from internal/cachereason, which owns the
	// vocabulary and each value's meaning. A value outside it is unrecognized and
	// must be reported as such, never guessed at.
	PrefixChangeReasons []string
	// StablePrefixHash hashes the cache-stable prefix alone. PrefixHash also
	// folds in the turn tail's session-context digest, so it moves when only the
	// tail moved; this one is what to compare against real provider reuse.
	StablePrefixHash string
	// StablePrefixChanged reports whether that stable prefix moved since the
	// previous capture — false for a tail-only change, the case the derived
	// PrefixChanged cannot distinguish.
	StablePrefixChanged bool
	SystemHash          string
	ToolsHash           string
	LogRewriteVersion   int
	ToolSchemaTokens    int
	CacheMissTokens     int
	CacheHitTokens      int
	SessionContext      *SessionContextDiagnostics
}

// SessionContextSectionDiagnostics is a content-free fingerprint.
type SessionContextSectionDiagnostics struct {
	Digest string
	Chars  int
}

// SessionContextDiagnostics attributes a snapshot change without logging bodies.
type SessionContextDiagnostics struct {
	Version          int
	Digest           string
	TargetRole       string
	Reasons          []string
	Environment      SessionContextSectionDiagnostics
	Workspace        SessionContextSectionDiagnostics
	BackgroundMemory SessionContextSectionDiagnostics
	SkillsCatalog    SessionContextSectionDiagnostics
}
