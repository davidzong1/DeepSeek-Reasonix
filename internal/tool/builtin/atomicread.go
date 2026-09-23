package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(atomicRead{}) }

// atomicRead is the read half of the team-session atomic file pair. It answers
// the three shapes team members actually need — a window, a symbol map, and
// "what changed since I last looked" — while leaving behind the anchor that
// atomic_write compares against. It goes through the same read contract as
// read_file (ExecuteRead + ReadEnvelope + ResolveReadPath), so its results are
// not second-class: paging, evidence and transport clipping all behave.
type atomicRead struct {
	workDir     string
	paths       *PathResolver
	forbidRoots []string
	// overlay, when non-nil, serves content from the host transport (unsaved
	// editor buffers) before falling back to disk, exactly as read_file does.
	overlay  FileOverlay
	captured *tool.ReadResultSource
}

const (
	atomicModeAuto    = "auto"
	atomicModeWindow  = "window"
	atomicModeOutline = "outline"
	atomicModeDelta   = "delta"
	atomicModeTail    = "tail"
)

const (
	atomicOutlineBudgetBytes = 4 << 10
	atomicDeltaBudgetBytes   = 8 << 10
	atomicTailBudgetBytes    = 8 << 10
	// atomicWindowDefaultLimit matches read_file's default so a member that
	// switches tools sees the same window for the same arguments.
	atomicWindowDefaultLimit = 2000
	atomicTailDefaultLines   = 80
	atomicAutoHeadLines      = 60
	// atomicOutlineFallbackLines is how many lines a file with no discoverable
	// symbols shows instead: enough to recognize it, not enough to be a read.
	atomicOutlineFallbackLines = 40
	// atomicHeaderReserveBytes is subtracted from the result budget before the
	// body is rendered, so the header never pushes a result past its cap.
	atomicHeaderReserveBytes = 256
	atomicBinaryPeekBytes    = 8 << 10
)

func (atomicRead) Name() string { return "atomic_read" }

func (atomicRead) Description() string {
	return "Read a file cheaply. mode=auto|window (numbered lines, offset/limit; auto adds an outline first when the file is large), outline (section/symbol map only), delta (only hunks changed since a read_id you already have — use after you or a teammate edited), tail (last lines). Every read records the snapshot the write tool builds on; results are bounded and mark what was not delivered."
}

func (atomicRead) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string","enum":["auto","window","outline","delta","tail"]},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1},"since":{"type":"string"}},"required":["path"]}`)
}

func (atomicRead) ReadOnly() bool { return true }

// PlanModeSafe: reading is what planning is for.
func (atomicRead) PlanModeSafe() bool { return true }

// ClassifyCall lets several reads in one turn run together. Reading is
// side-effect-free, so a batch of them has no ordering to preserve.
func (atomicRead) ClassifyCall(json.RawMessage) tool.CallClass {
	return tool.CallClass{Known: true, ReadOnly: true, ParallelSafe: true}
}

// SnipHint front-loads file content, matching read_file: the most relevant
// lines are near the top, so keep a generous head and a short tail.
func (atomicRead) SnipHint() tool.SnipHint {
	return tool.SnipHint{Head: 120, Tail: 12, HeadChars: 12000, TailChars: 2000}
}

func (r atomicRead) ResolveReadPath(args json.RawMessage) (string, error) {
	// Path identity must remain available even when another argument is
	// invalid, so a failed continuation still belongs to its bounded task.
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("path is required")
	}
	return resolveReadablePath(r.workDir, p.Path, r.paths).Path, nil
}

// atomicReadParams is one validated atomic_read call with defaults applied.
type atomicReadParams struct {
	Path        string
	Mode        string
	Offset      int
	Limit       int
	Since       string
	WindowGiven bool
}

func parseAtomicReadParams(args json.RawMessage) (atomicReadParams, error) {
	var raw struct {
		Path   string `json:"path"`
		Mode   string `json:"mode"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
		Since  string `json:"since"`
	}
	if err := json.Unmarshal(args, &raw); err != nil {
		return atomicReadParams{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(raw.Path) == "" {
		return atomicReadParams{}, fmt.Errorf("path is required")
	}
	mode := strings.ToLower(strings.TrimSpace(raw.Mode))
	if mode == "" {
		mode = atomicModeAuto
	}
	switch mode {
	case atomicModeAuto, atomicModeWindow, atomicModeOutline, atomicModeDelta, atomicModeTail:
	default:
		return atomicReadParams{}, fmt.Errorf("mode must be auto, window, outline, delta, or tail (got %q)", raw.Mode)
	}
	offset, limit := raw.Offset, raw.Limit
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = atomicWindowDefaultLimit
	}
	return atomicReadParams{
		Path:        raw.Path,
		Mode:        mode,
		Offset:      offset,
		Limit:       limit,
		Since:       strings.TrimSpace(raw.Since),
		WindowGiven: readWindowGiven(args),
	}, nil
}

func (r atomicRead) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	output, _, err := r.ExecuteRead(ctx, args)
	return output, err
}

func (r atomicRead) ExecuteRead(ctx context.Context, args json.RawMessage) (string, tool.ReadResultEnvelope, error) {
	p, err := parseAtomicReadParams(args)
	if err != nil {
		return "", tool.ReadResultEnvelope{}, err
	}
	rp := resolveReadablePath(r.workDir, p.Path, r.paths)
	if confineRead(r.forbidRoots, rp.Path) {
		// A forbidden root must look absent, not forbidden: matching the
		// readers' tmpfs semantics is what keeps the boundary unprobeable.
		return "", tool.ReadResultEnvelope{}, &os.PathError{Op: "open", Path: rp.DisplayPath, Err: os.ErrNotExist}
	}
	if err := r.rejectBinary(ctx, rp); err != nil {
		return "", tool.ReadResultEnvelope{}, err
	}

	anchor, src, err := atomicCaptureSource(ctx, r.overlay, rp.Path)
	if err != nil {
		return "", tool.ReadResultEnvelope{}, r.readError(rp, err)
	}
	if anchor.Missing {
		return "", tool.ReadResultEnvelope{}, &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{Code: tool.FSNotFound, Path: rp.DisplayPath, Recovery: "the file is absent; create it only if the task requires a new file"},
			Cause:      &os.PathError{Op: "read", Path: rp.DisplayPath, Err: os.ErrNotExist},
		}
	}

	kind := tool.ReadSourceDisk
	if anchor.Route == atomicRouteOverlay {
		kind = tool.ReadSourceOverlay
	}
	source := tool.ReadResultSource{CanonicalPath: rp.Path, Kind: kind, Identity: anchor.Version}
	source.Snapshot = tool.SourceSnapshot(source.Kind, source.CanonicalPath, source.Identity)
	r.captured = &source

	plan, err := r.plan(ctx, p, rp, anchor, src.content)
	if err != nil {
		return "", tool.ReadResultEnvelope{}, err
	}
	output := plan.render(anchor, rp)
	env := r.envelope(p, rp, source, anchor, plan, output)
	return output, env, nil
}

// readError maps a capture failure onto the reader's error vocabulary. A
// directory is the one case worth naming: it is not "missing", and the model's
// next move (list it) is different.
func (r atomicRead) readError(rp ResolvedPath, err error) error {
	if info, statErr := os.Stat(rp.Path); statErr == nil && info.IsDir() {
		return fmt.Errorf("%s is a directory, not a file — use the ls tool to list it, or read a specific file inside it", rp.DisplayPath)
	}
	return fmt.Errorf("read %s: %s", rp.DisplayPath, rp.ErrorText(err))
}

// rejectBinary refuses a non-text target before anything is anchored. It peeks
// a bounded prefix rather than the whole file, so a multi-GB archive is never
// slurped just to be discarded. An overlay-served target is text by contract
// and is never peeked.
func (r atomicRead) rejectBinary(ctx context.Context, rp ResolvedPath) error {
	if r.overlay != nil && !rp.External {
		if _, ok := r.overlay.ReadTextFile(ctx, rp.Path); ok {
			return nil
		}
	}
	f, err := os.Open(rp.Path)
	if err != nil {
		return nil // absence and permissions are reported by the capture below
	}
	defer f.Close()
	peek := make([]byte, atomicBinaryPeekBytes)
	n, _ := f.Read(peek)
	if atomicLooksBinary(peek[:n]) {
		return fmt.Errorf("%s is a binary file; use a binary inspection tool", rp.DisplayPath)
	}
	return nil
}

// ReadEnvelope reports what this read delivered, using the same host protocol
// read_file uses so the reader's proof survives transport clipping. It runs
// when a caller (a host extension, a replayed session) asks after the fact, so
// it recovers the source identity from the read id in the result's own header
// rather than trusting a fresh read: the header id is minted from exactly the
// bytes that were delivered.
func (r atomicRead) ReadEnvelope(ctx context.Context, args json.RawMessage, output string) (tool.ReadResultEnvelope, bool) {
	p, err := parseAtomicReadParams(args)
	if err != nil {
		return tool.ReadResultEnvelope{}, false
	}
	rp := resolveReadablePath(r.workDir, p.Path, r.paths)
	env := tool.ReadResultEnvelope{
		ProtocolVersion: tool.ReadResultProtocolVersion,
		Source:          tool.ReadResultSource{CanonicalPath: rp.Path},
		Intent:          atomicReadIntent(p),
	}
	if p.WindowGiven {
		requested := tool.ReadRange{Start: p.Offset, End: p.Offset + p.Limit}
		env.RequestedRange = &requested
	}
	if r.captured != nil {
		env.Source = *r.captured
	} else if header, ok := atomicParseHeader(output); ok {
		if anchor, found := atomicCachedAnchor(header.ReadID, rp.Path); found {
			env.Source.Kind = tool.ReadSourceDisk
			if anchor.Route == atomicRouteOverlay {
				env.Source.Kind = tool.ReadSourceOverlay
			}
			env.Source.Identity = anchor.Version
			env.SourceEnd = &anchor.Lines
		}
	}
	env.Source.Snapshot = tool.SourceSnapshot(env.Source.Kind, rp.Path, env.Source.Identity)

	window, hasWindow := tool.ParseReadWindow(output)
	if hasWindow {
		env.DeliveredRanges = []tool.ReadRange{window.Range()}
		env.WindowDigest = tool.WindowDigest(rp.Path, window)
	}
	trailer := tool.ParseReadTrailer(output)
	env.HasMore = trailer.HasMore
	env.EOF = !trailer.HasMore
	if env.EOF && env.SourceEnd == nil {
		if hasWindow {
			end := window.Range().End
			env.SourceEnd = &end
		} else if header, ok := atomicParseHeader(output); ok {
			end := header.Lines
			env.SourceEnd = &end
		}
	}
	return env, true
}

// atomicReadHeader is the parsed first line of a read result.
type atomicReadHeader struct {
	ReadID string
	Path   string
	Lines  int
}

// atomicParseHeader reads back the header this reader wrote. It is the reader's
// own format, so the reader owns both sides of it — the same reasoning that
// lets read_file parse its own paging trailer.
func atomicParseHeader(output string) (atomicReadHeader, bool) {
	line, _, _ := strings.Cut(output, "\n")
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != "read" || !atomicReadIDShape(fields[1]) {
		return atomicReadHeader{}, false
	}
	header := atomicReadHeader{ReadID: fields[1], Path: fields[2]}
	open := strings.LastIndex(line, "(")
	closeIdx := strings.LastIndex(line, ")")
	if open < 0 || closeIdx < open {
		return header, true
	}
	count, _, found := strings.Cut(strings.TrimPrefix(line[open+1:closeIdx], "("), " lines")
	if !found {
		return header, true
	}
	if n, err := strconv.Atoi(strings.TrimSpace(count)); err == nil {
		header.Lines = n
	}
	return header, true
}

// envelope is the in-band path: ExecuteRead already knows exactly what it
// delivered, so it reports that rather than re-deriving it from the text.
func (r atomicRead) envelope(p atomicReadParams, rp ResolvedPath, source tool.ReadResultSource, anchor atomicAnchor, plan atomicReadPlan, output string) tool.ReadResultEnvelope {
	env := tool.ReadResultEnvelope{
		ProtocolVersion: tool.ReadResultProtocolVersion,
		Source:          source,
		Intent:          atomicReadIntent(p),
	}
	if p.WindowGiven {
		requested := tool.ReadRange{Start: p.Offset, End: p.Offset + p.Limit}
		env.RequestedRange = &requested
	}
	if plan.delivered > 0 {
		delivered := tool.ReadRange{Start: plan.firstLine - 1, End: plan.firstLine - 1 + plan.delivered}
		env.DeliveredRanges = []tool.ReadRange{delivered}
		if window, ok := tool.ParseReadWindow(output); ok {
			env.WindowDigest = tool.WindowDigest(rp.Path, window)
		}
	}
	// Coverage, not mode, decides the paging flags: an outline or a head window
	// that did not reach the last line is a partial view and must say so, or a
	// later evidence check would treat it as whole-file coverage.
	lastLine := 0
	for _, r := range env.DeliveredRanges {
		lastLine = max(lastLine, r.End)
	}
	env.HasMore = lastLine < anchor.Lines
	env.EOF = !env.HasMore
	if env.EOF {
		end := anchor.Lines
		env.SourceEnd = &end
	}
	return env
}

// atomicReadIntent mirrors read_file: an explicit window is a range, everything
// else is a bounded inspection that completes in one page.
func atomicReadIntent(p atomicReadParams) tool.ReadIntent {
	if p.WindowGiven {
		return tool.ReadIntentRange
	}
	return tool.ReadIntentInspect
}
