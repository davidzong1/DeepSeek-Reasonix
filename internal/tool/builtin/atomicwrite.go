package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"reasonix/internal/diff"
	"reasonix/internal/fileops"
	fileenc "reasonix/internal/fileutil/encoding"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(atomicWrite{}) }

// atomicWrite is the team-session writer: five single-file modes plus a
// multi-file transaction, built on the shared atomic base (atomicfs_shared.go).
// It replaces the three shell habits that make file editing expensive and
// unsafe in a team — whole-file rewrites where a patch would do, read-modify-
// write with no anchor (sed -i, python - <<'PY'), and N calls where one
// transaction would do. It never returns file content: the receipt names what
// changed, a rejection names the hunks that changed underneath the caller.
//
// It does NOT take the workspace lease or the team write token: those are
// acquired by the agent's own coordination pipeline (tool_write_coordination.go)
// from the paths this tool declares, so there is exactly one place that decides
// who writes where.
type atomicWrite struct {
	roots   []string
	rootSet *sandbox.WritableRootSet
	guard   SessionDataGuard
	managed ManagedConfigPaths
	workDir string
	overlay FileOverlay
	// receipt mirrors write_file's optional per-runtime effect hook. hadPrior
	// means an existing file was overwritten; prior is its previous content.
	receipt func(path string, hadPrior bool, prior []byte)
}

func (atomicWrite) Name() string { return "atomic_write" }

func (atomicWrite) Description() string {
	return "Write a file atomically and cheaply. mode=create (fails if the file exists), replace (whole content), append (add at EOF; use instead of echo >>), patch (edits:[{old,new}] or range:{start,end}+content; use instead of sed -i), delete. symbol names one outline symbol and replaces its whole span; no prior read. Refuses with the changed hunks when the file differs from what you read; no forced overwrite. ops:[{path,mode,...}] applies several files as one transaction. Returns a bounded receipt, never the file."
}

// atomicWriteSchema is the frozen provider schema. It is a named constant so the
// provider surface stays byte-identical to the route's §2.3 text.
const atomicWriteSchema = `{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string","enum":["create","replace","append","patch","delete"]},"content":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":["old","new"]}},"range":{"type":"object","properties":{"start":{"type":"integer"},"end":{"type":"integer"}},"required":["start","end"]},"symbol":{"type":"string"},"ops":{"type":"array","items":{"type":"object"}}},"required":["path"]}`

func (atomicWrite) Schema() json.RawMessage {
	return json.RawMessage(atomicWriteSchema)
}

func (atomicWrite) ReadOnly() bool { return false }

// atomicWriteContractSchema is the same schema with the transaction form
// admitted: `path` is no longer required at the root, because an ops-only call
// has no single path and JSON Schema cannot express "path or ops". The
// provider-visible schema stays frozen (see Schema); only argument validation
// compiles this one (see tool.ArgumentContractProvider), and ValidateArguments
// below enforces the rule the schema cannot state.
var atomicWriteContractSchema = func() json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal([]byte(atomicWriteSchema), &doc); err != nil {
		panic("atomic_write schema is not valid JSON: " + err.Error())
	}
	delete(doc, "required")
	encoded, err := json.Marshal(doc)
	if err != nil {
		panic("atomic_write schema is not marshalable: " + err.Error())
	}
	return encoded
}()

// ArgumentContractSchema publishes the validation variant to the host-side
// argument gate without touching the provider surface.
func (atomicWrite) ArgumentContractSchema() json.RawMessage { return atomicWriteContractSchema }

// ValidateArguments enforces the one rule the schema cannot state: a call must
// name a single path or an ops transaction, never neither.
func (atomicWrite) ValidateArguments(raw json.RawMessage) []tool.ArgumentViolation {
	p, err := parseAtomicWriteArgs(raw)
	if err != nil {
		return nil // the schema's own parse violation already reports this
	}
	if strings.TrimSpace(p.Path) == "" && len(p.Ops) == 0 {
		return []tool.ArgumentViolation{{Keyword: "required", Expected: "path, or ops with at least one entry"}}
	}
	return nil
}

// PlanModeSafe is false: a plan-mode turn must not mutate the workspace.
func (atomicWrite) PlanModeSafe() bool { return false }

// SnipHint: the receipt is bounded by construction, so a generic head/tail split
// is never needed. The geometry is small and explicit rather than inherited.
func (atomicWrite) SnipHint() tool.SnipHint {
	return tool.SnipHint{Head: 40, Tail: 10, HeadChars: 2048, TailChars: 512}
}

func (w atomicWrite) DeclareWriteAccess(args json.RawMessage) (tool.WriteAccessDeclaration, error) {
	p, err := parseAtomicWriteArgs(args)
	if err != nil {
		return tool.WriteAccessDeclaration{}, err
	}
	return declareParentWriteDirs(w.workDir, p.targets()...)
}

// DeclareEvidenceTarget binds the write to the source the host will actually
// read. It runs the same Preview the approval card shows, so the evidence and
// the card can never describe different changes.
func (w atomicWrite) DeclareEvidenceTarget(ctx context.Context, args json.RawMessage) (tool.EvidenceTargetInfo, error) {
	change, err := w.Preview(ctx, args)
	return versionedPreviewEvidence(ctx, w.overlay, change, err)
}

// atomicWriteParams is one validated atomic_write call.
type atomicWriteParams struct {
	Path    string            `json:"path"`
	Mode    string            `json:"mode"`
	Content string            `json:"content"`
	Edits   []atomicEditStep  `json:"edits"`
	Range   *atomicWriteRange `json:"range"`
	Symbol  string            `json:"symbol"`
	Ops     []json.RawMessage `json:"ops"`
	Since   string            `json:"since"`
	raw     json.RawMessage   `json:"-"`
}

type atomicEditStep struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type atomicWriteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// targets lists every path this call can write: the top-level path, or every
// ops entry's own path. The caller resolves them against its working directory.
func (p atomicWriteParams) targets() []string {
	if len(p.Ops) > 0 {
		out := make([]string, 0, len(p.Ops))
		for _, raw := range p.Ops {
			var op struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &op); err != nil {
				continue
			}
			out = append(out, op.Path)
		}
		return out
	}
	return []string{p.Path}
}

func parseAtomicWriteArgs(args json.RawMessage) (atomicWriteParams, error) {
	var p atomicWriteParams
	if err := json.Unmarshal(args, &p); err != nil {
		return atomicWriteParams{}, fmt.Errorf("invalid args: %w", err)
	}
	p.raw = args
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	return p, nil
}

// Execute dispatches one atomic_write call. Single-file modes and the ops
// transaction share every boundary this tool has (path resolution, confinement,
// the target lock, the CAS recheck), so there is one place a boundary can be
// missed rather than two.
func (w atomicWrite) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parseAtomicWriteArgs(args)
	if err != nil {
		return "", err
	}
	if len(p.Ops) > 0 {
		return w.executeOps(ctx, p)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if p.Mode == "" {
		return "", fmt.Errorf("mode is required: one of create, replace, append, patch, delete")
	}
	path := resolveIn(w.workDir, p.Path)
	if err := w.confine(ctx, path); err != nil {
		return "", err
	}
	unlock := lockMutationPath(path)
	defer unlock()
	return w.applyMode(ctx, path, p)
}

// confine is the single write boundary: workspace roots first, then the session
// data guard, exactly the sequence write_file uses (see confineWrite). Every
// mode and every ops entry goes through it, so no mode can bypass the guard.
func (w atomicWrite) confine(ctx context.Context, path string) error {
	return confineWrite(ctx, effectiveWriteRoots(ctx, w.rootSet, w.roots), w.guard, w.managed, path)
}

// applyMode runs one mode against an already-resolved, already-confined, already
// locked path. It is the single-file body shared by the direct call and each ops
// entry, so both take the same CAS and encoding route.
func (w atomicWrite) applyMode(ctx context.Context, path string, p atomicWriteParams) (string, error) {
	switch p.Mode {
	case "create":
		return w.applyCreate(ctx, path, p)
	case "replace":
		return w.applyReplace(ctx, path, p)
	case "append":
		return w.applyAppend(ctx, path, p)
	case "patch":
		return w.applyPatch(ctx, path, p)
	case "delete":
		return w.applyDelete(ctx, path, p)
	default:
		return "", fmt.Errorf("unknown mode %q: one of create, replace, append, patch, delete", p.Mode)
	}
}

// applyCreate publishes a new file without overwriting anything. It needs no
// prior read: AtomicCreateFile's link publish already guarantees that a
// concurrent creator wins and its file is never replaced. An existing target —
// on disk or in the host's buffer — is refused before the attempt so the model
// gets the actionable "the file is already there" answer instead of a link
// error, and the refusal never echoes the existing content.
func (w atomicWrite) applyCreate(ctx context.Context, path string, p atomicWriteParams) (string, error) {
	if _, err := readEditSource(ctx, w.overlay, path); err == nil {
		return "", &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:     tool.FSStaleVersion,
				Path:     path,
				Recovery: "the file already exists; read it with atomic_read, then use patch to change it",
			},
			Cause: ErrFileChanged,
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := createFileEncoded(path, p.Content, fileenc.UTF8); err != nil {
		if os.IsExist(err) {
			return "", &tool.OperationError{
				Diagnostic: tool.OperationDiagnostic{
					Code:     tool.FSStaleVersion,
					Path:     path,
					Recovery: "another writer created this file first; read it with atomic_read, then use patch",
				},
				Cause: err,
			}
		}
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	w.commitDiskObservation(ctx, path, p.Content)
	w.noteReceipt(path, false, nil)
	return atomicReceiptLine("create", path, len(p.Content), atomicLineCount([]byte(p.Content))), nil
}

// applyReplace writes whole new content over an existing file. It requires a
// current observation: without one there is nothing to lose an update against,
// and the caller should be using create.
func (w atomicWrite) applyReplace(ctx context.Context, path string, p atomicWriteParams) (string, error) {
	anchor, err := w.anchorFor(ctx, path, p.Since)
	if err != nil {
		return "", err
	}
	src, _, err := atomicRecheckSource(ctx, w.overlay, path, anchor)
	if err != nil {
		return "", err
	}
	if src.content == p.Content {
		return atomicReceiptLine("replace", path, 0, 0, "unchanged: the file already holds this content"), nil
	}
	oldBytes := []byte(src.content)
	if err := src.write(ctx, w.overlay, path, p.Content); err != nil {
		return "", fmt.Errorf("replace %s: %w", path, err)
	}
	w.noteReceipt(path, true, oldBytes)
	oldLines := atomicLineCount(oldBytes)
	newLines := atomicLineCount([]byte(p.Content))
	return atomicReceiptLine("replace", path, len(p.Content), newLines,
		fmt.Sprintf("%d→%d lines", oldLines, newLines)), nil
}

// atomicAppendMaxBytes caps one append. The cap exists because the atomicity of
// append rests on it being a SINGLE write(2): a payload larger than the
// filesystem can accept in one call would have to be chunked, and chunking
// re-introduces exactly the interleaving the mode exists to prevent. An
// oversized append is refused with a route to a mode that can do it.
const atomicAppendMaxBytes = 4 << 20

// applyAppend adds content at EOF. It deliberately needs no prior read: an
// append cannot lose an update, because it never writes a byte the caller did
// not supply. One O_APPEND write(2) is what makes concurrent appends a total
// order instead of an interleaving — see TestAtomicAppendNeverInterleaves.
func (w atomicWrite) applyAppend(ctx context.Context, path string, p atomicWriteParams) (string, error) {
	if len(p.Content) == 0 {
		return "", fmt.Errorf("content is required for append")
	}
	if len(p.Content) > atomicAppendMaxBytes {
		return "", &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:     tool.FSTooLarge,
				Path:     path,
				Recovery: fmt.Sprintf("one append is capped at %d bytes; split the payload across appends, or use replace with the full content", atomicAppendMaxBytes),
			},
			Cause: fmt.Errorf("append payload of %d bytes exceeds the single-write cap", len(p.Content)),
		}
	}
	// The overlay is text-only and has no append: a buffered target must go
	// through the read-modify-write route so the user's unsaved buffer is not
	// silently bypassed.
	if w.overlay != nil {
		if _, ok := w.overlay.ReadTextFile(ctx, path); ok {
			return w.applyAppendThroughOverlay(ctx, path, p.Content)
		}
	}
	if err := appendFileEncoded(path, p.Content, fileenc.UTF8); err != nil {
		return "", fmt.Errorf("append %s: %w", path, err)
	}
	fileops.FromContext(ctx).Forget(fileops.DiskTarget(path, nil))
	return atomicReceiptLine("append", path, len(p.Content), atomicLineCount([]byte(p.Content))), nil
}

// applyAppendThroughOverlay appends by rewriting the host's buffer, which is the
// only route that cannot desync the editor from disk. It is not the atomic
// single-write append: the overlay is the authority for this target, and it
// takes the ordinary anchored path.
func (w atomicWrite) applyAppendThroughOverlay(ctx context.Context, path, content string) (string, error) {
	src, err := readEditSource(ctx, w.overlay, path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if err := src.requireObserved(ctx, w.overlay, path); err != nil {
		return "", err
	}
	updated := src.content + content
	if err := src.write(ctx, w.overlay, path, updated); err != nil {
		return "", fmt.Errorf("append %s: %w", path, err)
	}
	return atomicReceiptLine("append", path, len(content), atomicLineCount([]byte(content)), "through the host buffer"), nil
}

// atomicPatchLocator enforces patch's one-of-three locator rule: exact-text
// edits, a line range, or an outline symbol. Combining them would leave which
// bytes get replaced ambiguous, and an empty `content` cannot express deletion —
// that is what range and edits are for.
func atomicPatchLocator(p atomicWriteParams) (string, error) {
	symbol := strings.TrimSpace(p.Symbol)
	locators := 0
	if len(p.Edits) > 0 {
		locators++
	}
	if p.Range != nil {
		locators++
	}
	if symbol != "" {
		locators++
	}
	switch {
	case locators == 0:
		return "", fmt.Errorf("patch needs edits:[{old,new}], range:{start,end} with content, or symbol with content")
	case locators > 1:
		return "", fmt.Errorf("patch takes edits, range, or symbol, not a combination")
	}
	if symbol != "" && strings.TrimSpace(p.Content) == "" {
		return "", fmt.Errorf("patch symbol needs content; delete a symbol with range or edits")
	}
	return symbol, nil
}

// applyPatch changes part of a file: by exact-text edits, by a line range, or by
// one outline symbol. It reads, splices and publishes, so every byte outside the
// patch is carried over unchanged — the property sed -i does not have.
func (w atomicWrite) applyPatch(ctx context.Context, path string, p atomicWriteParams) (string, error) {
	symbol, err := atomicPatchLocator(p)
	if err != nil {
		return "", err
	}
	if symbol != "" {
		return w.applySymbolPatch(ctx, path, p, symbol)
	}
	anchor, err := w.anchorFor(ctx, path, p.Since)
	if err != nil {
		return "", err
	}
	src, _, err := atomicRecheckSource(ctx, w.overlay, path, anchor)
	if err != nil {
		return "", err
	}
	updated := src.content
	var receipts []editReplacementReceipt
	if p.Range != nil {
		updated, err = atomicSpliceRange(src.content, *p.Range, p.Content)
		if err != nil {
			return "", err
		}
	} else {
		updated, receipts, err = applyAtomicEdits(path, src.content, p.Edits)
		if err != nil {
			return "", err
		}
	}
	if updated == src.content {
		return atomicReceiptLine("patch", path, 0, 0, "unchanged: the patch matches the current content"), nil
	}
	oldBytes := []byte(src.content)
	if err := src.write(ctx, w.overlay, path, updated); err != nil {
		return "", fmt.Errorf("patch %s: %w", path, err)
	}
	w.noteReceipt(path, true, oldBytes)
	oldLines := atomicLineCount(oldBytes)
	newLines := atomicLineCount([]byte(updated))
	summary := atomicReceiptLine("patch", path, len(updated), newLines, fmt.Sprintf("%d→%d lines", oldLines, newLines))
	return withActualPostWriteReceipts(summary, receipts), nil
}

// applySymbolPatch replaces one outline symbol's whole span. It is the only
// patch locator that needs no earlier read: the caller already named the symbol,
// so this call's own read IS the observation, the span is resolved on exactly
// those bytes, and the publish still goes through the ordinary CAS.
func (w atomicWrite) applySymbolPatch(ctx context.Context, path string, p atomicWriteParams, symbol string) (string, error) {
	src, content, err := w.symbolPatchSource(ctx, path, p.Since)
	if err != nil {
		return "", err
	}
	start, end, label, err := atomicResolveSymbolSpan(content, path, symbol)
	if err != nil {
		return "", err
	}
	span := atomicSymbolSpanLabel(label, start, end)
	updated, err := atomicSpliceRange(content, atomicWriteRange{Start: start, End: end}, p.Content)
	if err != nil {
		return "", err
	}
	if updated == content {
		return atomicReceiptLine("patch", path, 0, 0, "unchanged: the patch matches the current content", span), nil
	}
	oldBytes := []byte(content)
	if err := src.write(ctx, w.overlay, path, updated); err != nil {
		return "", fmt.Errorf("patch %s: %w", path, err)
	}
	w.noteReceipt(path, true, oldBytes)
	oldLines := atomicLineCount(oldBytes)
	newLines := atomicLineCount([]byte(updated))
	return atomicReceiptLine("patch", path, len(updated), newLines,
		fmt.Sprintf("%d→%d lines", oldLines, newLines), span), nil
}

// symbolPatchSource resolves the bytes a symbol patch splices and the route its
// write must take back. Without `since` it reads the source itself — that read
// is this call's observation, which is what makes a symbol patch legal without a
// prior atomic_read. With `since` it is the ordinary anchored route, and the
// symbol is then resolved against the rechecked bytes so a stale outline cannot
// cut a span that no longer means what it did.
func (w atomicWrite) symbolPatchSource(ctx context.Context, path, since string) (editSource, string, error) {
	if strings.TrimSpace(since) != "" {
		anchor, err := w.anchorFor(ctx, path, since)
		if err != nil {
			return editSource{}, "", err
		}
		src, current, err := atomicRecheckSource(ctx, w.overlay, path, anchor)
		if err != nil {
			return editSource{}, "", err
		}
		return src, string(current), nil
	}
	src, err := readEditSource(ctx, w.overlay, path)
	if err != nil {
		if os.IsNotExist(err) {
			return editSource{}, "", fmt.Errorf("patch %s: the file does not exist", path)
		}
		return editSource{}, "", fmt.Errorf("read %s: %w", path, err)
	}
	return src, src.content, nil
}

// applyAtomicEdits applies edits:[{old,new}] against the source, reusing
// edit_file's exact-then-fuzzy matching so the error shapes a model already
// learned (old_string not found / not unique) are the ones it gets here.
func applyAtomicEdits(path, content string, edits []atomicEditStep) (string, []editReplacementReceipt, error) {
	updated := content
	receipts := make([]editReplacementReceipt, 0, len(edits))
	for i, step := range edits {
		if step.Old == "" {
			return "", nil, fmt.Errorf("edit %d: old is required", i+1)
		}
		result := applyOldStringEdit(updated, step.Old, step.New, false)
		switch {
		case result.applied == 1:
			updated = result.updated
			receipts = append(receipts, result.receipt)
		case result.matches == 0:
			return "", nil, fmt.Errorf("edit %d: %w", i+1, oldStringNotFoundError(path, step.Old, updated))
		default:
			return "", nil, fmt.Errorf("edit %d: %w", i+1, oldStringNotUniqueError(path, step.Old, updated, result.matches, false))
		}
	}
	return updated, receipts, nil
}

// atomicSpliceRange replaces the 1-based inclusive line range [start, end] with
// replacement. Line numbers match what a read prints, so a model can copy two
// numbers straight off its own window. Out-of-range values are refused rather
// than clamped: silently writing a different range than the one asked for is how
// a range edit destroys a file.
func atomicSpliceRange(content string, r atomicWriteRange, replacement string) (string, error) {
	lines := atomicSplitLines(content)
	if r.Start < 1 || r.End < r.Start || r.End > len(lines) {
		return "", fmt.Errorf("range %d-%d is outside the file's %d line(s); line numbers are 1-based and inclusive", r.Start, r.End, len(lines))
	}
	head := lines[:r.Start-1]
	tail := lines[r.End:]
	replacement = strings.TrimSuffix(replacement, "\n")
	var body []string
	if replacement != "" {
		body = strings.Split(replacement, "\n")
	}
	merged := make([]string, 0, len(head)+len(body)+len(tail))
	merged = append(merged, head...)
	merged = append(merged, body...)
	merged = append(merged, tail...)
	out := strings.Join(merged, "\n")
	// Preserve the file's trailing-newline convention: the splice changes lines,
	// not the file's shape.
	if trailingNewline(content) && len(merged) > 0 {
		out += "\n"
	}
	return out, nil
}

func trailingNewline(content string) bool {
	return strings.HasSuffix(content, "\n")
}

// applyDelete removes the file. unlink is atomic, so there is no half-deleted
// state; the mode is idempotent — a target that is already gone reports so
// rather than failing, because the caller's intent is satisfied either way.
func (w atomicWrite) applyDelete(ctx context.Context, path string, p atomicWriteParams) (string, error) {
	if _, err := w.anchorFor(ctx, path, p.Since); err != nil {
		if !os.IsNotExist(err) && !atomicNotObserved(err) {
			return "", err
		}
		return atomicReceiptLine("delete", path, 0, 0, "already absent"), nil
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return atomicReceiptLine("delete", path, 0, 0, "already absent"), nil
		}
		return "", fmt.Errorf("delete %s: %w", path, err)
	}
	fileops.FromContext(ctx).ObserveAbsent(fileops.DiskTarget(path, nil))
	return atomicReceiptLine("delete", path, 0, 0), nil
}

// anchorFor resolves the anchor this call compares against: the since the model
// cited, or the session's last observation. Both are host-derived; a
// model-supplied id is only ever a lookup key.
func (w atomicWrite) anchorFor(ctx context.Context, path, since string) (atomicAnchor, error) {
	return atomicAnchorFor(ctx, w.overlay, path, since)
}

// commitDiskObservation records the version the write just published, so a
// second write in the same turn builds on it instead of reporting its own edit
// as a conflict.
func (w atomicWrite) commitDiskObservation(ctx context.Context, path, content string) {
	store := fileops.FromContext(ctx)
	if store == nil {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		store.Forget(fileops.DiskTarget(path, nil))
		return
	}
	target, version := fileops.DiskSnapshot(path, info)
	store.ObservePresent(target, version)
	atomicRememberSnapshot(atomicAnchor{Path: path, Route: atomicRouteDisk, Version: string(version), ReadID: atomicReadID(atomicRouteDisk, path, version, []byte(content))}, []byte(content))
}

func (w atomicWrite) noteReceipt(path string, hadPrior bool, prior []byte) {
	if w.receipt != nil {
		w.receipt(path, hadPrior, prior)
	}
}

// appendFileEncoded appends in one O_APPEND write(2) followed by fsync. The
// single write is the whole point: two concurrent appenders therefore produce a
// total order of whole payloads, never an interleaving. Chunking it (or using
// os.WriteFile with O_APPEND after a Seek) would reintroduce interleaving.
func appendFileEncoded(path, content string, enc fileenc.Kind) error {
	data := fileenc.Encode(content, enc)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Preview computes the change this call would make, for the approval card. It
// mirrors applyMode's transformation without writing, and it is what
// DeclareEvidenceTarget is built on, so a card and the evidence can never
// disagree about what the call does.
func (w atomicWrite) Preview(ctx context.Context, args json.RawMessage) (diff.Change, error) {
	p, err := parseAtomicWriteArgs(args)
	if err != nil {
		return diff.Change{}, err
	}
	if len(p.Ops) > 0 {
		// A transaction has no single change to show; the card renders each
		// target from its own call. Returning no change is honest here.
		return diff.Change{}, fmt.Errorf("a multi-file transaction has no single-file preview; issue the ops as separate calls to preview them")
	}
	if strings.TrimSpace(p.Path) == "" {
		return diff.Change{}, fmt.Errorf("path is required")
	}
	path := resolveIn(w.workDir, p.Path)
	if err := confinePreview(effectiveWriteRoots(ctx, w.rootSet, w.roots), w.guard, w.managed, path); err != nil {
		return diff.Change{}, err
	}
	old, kind := "", diff.Create
	src, rerr := readEditSource(ctx, w.overlay, path)
	if rerr == nil {
		old, kind = src.content, diff.Modify
	} else if !os.IsNotExist(rerr) {
		return diff.Change{}, fmt.Errorf("read %s: %w", path, rerr)
	}
	if p.Mode == "delete" {
		if kind == diff.Create {
			return diff.Change{}, fmt.Errorf("delete %s: the file does not exist", path)
		}
		return diff.Build(path, old, "", diff.Delete), nil
	}
	newText, err := previewAtomicContent(path, p, old, kind == diff.Create)
	if err != nil {
		return diff.Change{}, err
	}
	if kind == diff.Modify && newText == old {
		return diff.Change{Path: path, Kind: diff.Modify, OldText: old, NewText: old}, nil
	}
	return diff.Build(path, old, newText, kind), nil
}

// previewAtomicContent is the read-only half of applyMode: it produces exactly
// the bytes Execute would publish, so a preview can never promise a change the
// call then declines to make.
func previewAtomicContent(path string, p atomicWriteParams, old string, missing bool) (string, error) {
	switch p.Mode {
	case "create":
		if !missing {
			return "", fmt.Errorf("create %s: the file already exists", path)
		}
		return p.Content, nil
	case "replace":
		if missing {
			return "", fmt.Errorf("replace %s: the file does not exist; use mode=create", path)
		}
		return p.Content, nil
	case "append":
		if len(p.Content) == 0 {
			return "", fmt.Errorf("content is required for append")
		}
		if missing {
			return p.Content, nil
		}
		return old + p.Content, nil
	case "patch":
		if missing {
			return "", fmt.Errorf("patch %s: the file does not exist", path)
		}
		symbol, err := atomicPatchLocator(p)
		if err != nil {
			return "", err
		}
		if symbol != "" {
			start, end, _, err := atomicResolveSymbolSpan(old, path, symbol)
			if err != nil {
				return "", err
			}
			return atomicSpliceRange(old, atomicWriteRange{Start: start, End: end}, p.Content)
		}
		if p.Range != nil {
			return atomicSpliceRange(old, *p.Range, p.Content)
		}
		updated, _, err := applyAtomicEdits(path, old, p.Edits)
		return updated, err
	case "delete":
		return "", fmt.Errorf("delete %s: use mode=delete without content", path)
	default:
		return "", fmt.Errorf("unknown mode %q: one of create, replace, append, patch, delete", p.Mode)
	}
}

// atomicNotObserved reports whether err is the "read before you write"
// rejection, so a caller can distinguish "nothing to delete" from a real fault.
func atomicNotObserved(err error) bool {
	var opErr *tool.OperationError
	if !asOperationError(err, &opErr) {
		return false
	}
	return opErr.Diagnostic.Code == tool.FSNotObserved
}

func asOperationError(err error, target **tool.OperationError) bool {
	for err != nil {
		if opErr, ok := err.(*tool.OperationError); ok {
			*target = opErr
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// BindAtomicWriteReceipt attaches the per-runtime write receipt hook to an
// atomic_write tool, mirroring BindFileWriteReceipt for write_file.
func BindAtomicWriteReceipt(t tool.Tool, receipt func(path string, hadPrior bool, prior []byte)) tool.Tool {
	w, ok := t.(atomicWrite)
	if !ok {
		return t
	}
	w.receipt = receipt
	return w
}
