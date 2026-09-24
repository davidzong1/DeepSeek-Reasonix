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
	// MessagePrefixHash fingerprints the provider-visible message array: roles
	// and the fields that reach the wire, never the system prompt or the tool
	// schemas, which StablePrefixHash already covers.
	MessagePrefixHash string
	// MessageCount is how many conversation messages the array carried.
	MessageCount int
	// MessagesComparable reports whether there was a previous request's message
	// shape to compare against. False means the two offsets below are unset, not
	// zero: a first request has no divergence and is not an append-only one.
	MessagesComparable bool
	// FirstDivergenceOffset is the index of the first message that differs from
	// the previous request's message at the same index, or -1 when there was
	// nothing comparable. An append-only request reports the previous count.
	FirstDivergenceOffset int
	// MessagesRewritten is how many messages of the previous request this one did
	// not reuse. Zero means append-only; a nonzero value means bytes the provider
	// had already read were rewritten, the one client-side cause of a re-read.
	MessagesRewritten int
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
