package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/fileops"
	"reasonix/internal/fileutil"
	fileenc "reasonix/internal/fileutil/encoding"
	"reasonix/internal/tool"
)

// The ops transaction: several files changed by one call.

// atomicOpsJournalDir is where a transaction's plan and outcome are recorded.
// It lives under .reasonix/, which the repository gitignores, so a journal can
// never show up as an untracked file in the user's `git status`.
const atomicOpsJournalDir = ".reasonix/atomic-writes"

// atomicOpsMaxTargets bounds one transaction. The bound exists because every
// target is staged at once, and because a transaction wide enough to be a bulk
// rewrite is a different operation than "change these three files together".
const atomicOpsMaxTargets = 32

// atomicOp is one parsed ops[] entry: an ordinary atomic_write call body with
// its own path.
type atomicOp struct {
	atomicWriteParams
}

// executeOps runs the prepare → journal → commit sequence for one transaction.
//
// The promise is narrow and stated honestly (route §3.3). prepare is side-effect
// free: every target is read, CAS-checked, spliced in memory and staged BEFORE
// any rename, so a failure in prepare leaves the workspace byte-identical.
// commit is atomic per file but NOT across files — a crash between renames
// leaves a subset committed, reported exactly with a txid and a journal.
// Cross-file visibility atomicity would need a filesystem transaction; claiming
// it without one is a lie a crash exposes.
func (w atomicWrite) executeOps(ctx context.Context, p atomicWriteParams) (string, error) {
	ops, err := parseAtomicOps(p.Ops)
	if err != nil {
		return "", err
	}
	if len(ops) == 0 {
		return "", fmt.Errorf("ops must not be empty")
	}
	if len(ops) > atomicOpsMaxTargets {
		return "", &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:     tool.FSTooLarge,
				Path:     w.workDir,
				Recovery: fmt.Sprintf("one transaction is capped at %d files; split it into smaller transactions", atomicOpsMaxTargets),
			},
			Cause: fmt.Errorf("transaction of %d files exceeds the cap", len(ops)),
		}
	}
	// Two entries for the same path would make the outcome ambiguous (which one
	// committed?) and can deadlock the lock ordering, so it is refused up front.
	paths := make([]string, 0, len(ops))
	seen := map[string]bool{}
	for i := range ops {
		if strings.TrimSpace(ops[i].Path) == "" {
			return "", fmt.Errorf("ops[%d]: path is required", i)
		}
		resolved := resolveIn(w.workDir, ops[i].Path)
		if seen[resolved] {
			return "", fmt.Errorf("ops names %s twice; a transaction may change each file once", ops[i].Path)
		}
		seen[resolved] = true
		ops[i].Path = resolved
		paths = append(paths, resolved)
	}
	// Every target goes through the same boundary as a single-file call before
	// anything is read: an out-of-bounds path must fail the whole transaction,
	// not the third entry after two were prepared.
	for _, path := range paths {
		if err := w.confine(ctx, path); err != nil {
			return "", err
		}
	}
	unlock := lockMutationPaths(paths)
	defer unlock()

	plan, err := w.prepareOps(ctx, ops)
	if err != nil {
		return "", err
	}
	return w.commitOps(ctx, plan)
}

func parseAtomicOps(raw []json.RawMessage) ([]atomicOp, error) {
	ops := make([]atomicOp, 0, len(raw))
	for i, entry := range raw {
		var op atomicOp
		if err := json.Unmarshal(entry, &op); err != nil {
			return nil, fmt.Errorf("ops[%d]: invalid args: %w", i, err)
		}
		op.Mode = strings.ToLower(strings.TrimSpace(op.Mode))
		if op.Mode == "" {
			return nil, fmt.Errorf("ops[%d]: mode is required", i)
		}
		if op.Mode == "append" {
			// append cannot participate: it publishes with its own O_APPEND
			// write(2), which is exactly the step prepare exists to defer.
			return nil, fmt.Errorf("ops[%d]: mode=append cannot be part of a transaction (it publishes immediately); issue it as its own call", i)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// atomicPreparedOp is one target whose new bytes are computed and whose temp
// file is already durable. Nothing here has touched the destination yet.
type atomicPreparedOp struct {
	path     string
	mode     string
	newBytes []byte
	oldBytes []byte
	tmp      string
	missing  bool
	// receipt is the per-file receipt line the caller sees after commit.
	receipt string
}

// atomicOpsPlan is a fully prepared transaction: every target staged, nothing
// published.
type atomicOpsPlan struct {
	txid string
	ops  []atomicPreparedOp
}

// prepareOps reads, CAS-checks and stages every target. It is the phase that
// must leave no trace: on any error the staged temp files are removed and the
// destinations are untouched.
func (w atomicWrite) prepareOps(ctx context.Context, ops []atomicOp) (*atomicOpsPlan, error) {
	plan := &atomicOpsPlan{txid: atomicTxID()}
	staged := make([]string, 0, len(ops))
	discard := func() {
		for _, tmp := range staged {
			_ = os.Remove(tmp)
		}
	}
	for _, op := range ops {
		prepared, err := w.prepareOp(ctx, op)
		if err != nil {
			discard()
			return nil, err
		}
		if prepared.tmp != "" {
			staged = append(staged, prepared.tmp)
		}
		plan.ops = append(plan.ops, prepared)
	}
	return plan, nil
}

// prepareOp stages one target: read through the same route a single-file call
// uses, verify the anchor, compute the new bytes, and stage a temp file next to
// the destination. The destination is not touched.
func (w atomicWrite) prepareOp(ctx context.Context, op atomicOp) (atomicPreparedOp, error) {
	path := op.Path
	switch op.Mode {
	case "create":
		// Read the source rather than the session store: the store keys on the
		// file's native identity once it exists, so an earlier absent observation
		// is not reachable by the present-form key.
		if _, err := readEditSource(ctx, w.overlay, path); err == nil {
			return atomicPreparedOp{}, &tool.OperationError{
				Diagnostic: tool.OperationDiagnostic{
					Code:     tool.FSStaleVersion,
					Path:     path,
					Recovery: "the file already exists; read it with atomic_read, then use patch",
				},
				Cause: ErrFileChanged,
			}
		} else if !os.IsNotExist(err) {
			return atomicPreparedOp{}, err
		}
		tmp, err := fileutil.StageAtomicWrite(path, fileenc.Encode(op.Content, fileenc.UTF8), 0o644)
		if err != nil {
			return atomicPreparedOp{}, fmt.Errorf("stage %s: %w", path, err)
		}
		return atomicPreparedOp{
			path: path, mode: op.Mode, newBytes: []byte(op.Content), tmp: tmp, missing: true,
			receipt: atomicReceiptLine("create", path, len(op.Content), atomicLineCount([]byte(op.Content))),
		}, nil
	case "delete":
		anchor, err := w.anchorFor(ctx, path, op.Since)
		if err != nil {
			if atomicNotObserved(err) || os.IsNotExist(err) {
				return atomicPreparedOp{path: path, mode: op.Mode, receipt: atomicReceiptLine("delete", path, 0, 0, "already absent")}, nil
			}
			return atomicPreparedOp{}, err
		}
		_ = anchor
		return atomicPreparedOp{path: path, mode: op.Mode, receipt: atomicReceiptLine("delete", path, 0, 0)}, nil
	case "replace", "patch":
	default:
		return atomicPreparedOp{}, fmt.Errorf("ops: unknown mode %q for %s", op.Mode, path)
	}
	anchor, err := w.anchorFor(ctx, path, op.Since)
	if err != nil {
		return atomicPreparedOp{}, err
	}
	src, current, err := atomicRecheckSource(ctx, w.overlay, path, anchor)
	if err != nil {
		return atomicPreparedOp{}, err
	}
	var updated, receipt string
	if op.Mode == "replace" {
		updated = op.Content
		receipt = atomicReceiptLine("replace", path, len(op.Content), atomicLineCount([]byte(op.Content)))
	} else {
		updated, receipt, err = atomicPatchContent(path, src.content, op)
		if err != nil {
			return atomicPreparedOp{}, err
		}
	}
	// The overlay is the authority for a buffered target: it cannot be staged as
	// a temp file and renamed, so commit writes it back through the host. Prepare
	// still proves the splice and the anchor, so a failure here is still free.
	if src.overlay {
		return atomicPreparedOp{
			path: path, mode: op.Mode, newBytes: []byte(updated), oldBytes: current, receipt: receipt,
		}, nil
	}
	tmp, err := fileutil.StageAtomicWrite(path, fileenc.Encode(updated, src.enc), 0o644)
	if err != nil {
		return atomicPreparedOp{}, fmt.Errorf("stage %s: %w", path, err)
	}
	return atomicPreparedOp{
		path: path, mode: op.Mode, newBytes: []byte(updated), oldBytes: current, tmp: tmp, receipt: receipt,
	}, nil
}

func atomicPatchContent(path, content string, op atomicOp) (string, string, error) {
	if op.Range != nil {
		updated, err := atomicSpliceRange(content, *op.Range, op.Content)
		if err != nil {
			return "", "", err
		}
		return updated, atomicReceiptLine("patch", path, len(updated), atomicLineCount([]byte(updated))), nil
	}
	if len(op.Edits) == 0 {
		return "", "", fmt.Errorf("patch %s needs edits:[{old,new}] or range:{start,end} with content", path)
	}
	updated, _, err := applyAtomicEdits(path, content, op.Edits)
	if err != nil {
		return "", "", err
	}
	return updated, atomicReceiptLine("patch", path, len(updated), atomicLineCount([]byte(updated))), nil
}

// commitOps publishes the prepared plan, recording the plan before the first
// rename and the outcome after the last. A rename that fails mid-way does not
// roll back what already landed — rollback would itself be a non-atomic
// multi-file operation — it reports exactly which paths committed.
func (w atomicWrite) commitOps(ctx context.Context, plan *atomicOpsPlan) (string, error) {
	journal := atomicWriteJournal{
		TxID:      plan.txid,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Planned:   make([]atomicJournalEntry, 0, len(plan.ops)),
	}
	for _, op := range plan.ops {
		journal.Planned = append(journal.Planned, atomicJournalEntry{Path: op.path, Mode: op.mode})
	}
	journalPath := w.journalPath(plan.txid)
	_ = writeAtomicJournal(journalPath, journal)

	var committed, failed []string
	for _, op := range plan.ops {
		if err := w.commitOp(ctx, op); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", op.path, err))
			continue
		}
		committed = append(committed, op.path)
		if op.mode != "delete" {
			w.commitDiskObservation(ctx, op.path, string(op.newBytes))
		}
	}
	journal.Committed = committed
	journal.Failed = failed
	journal.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_ = writeAtomicJournal(journalPath, journal)

	lines := []string{fmt.Sprintf("ops %s: %d/%d committed", plan.txid, len(committed), len(plan.ops))}
	for _, op := range plan.ops {
		status := "not committed"
		for _, done := range committed {
			if done == op.path {
				status = "committed"
				break
			}
		}
		lines = append(lines, fmt.Sprintf("%s: %s", status, op.receipt))
	}
	summary := atomicClipUTF8(strings.Join(lines, "\n"), atomicReceiptBudgetBytes*4)
	if len(failed) > 0 {
		return "", &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:      tool.FSStaleVersion,
				Path:      strings.Join(failed, ", "),
				Recovery:  fmt.Sprintf("transaction %s partially committed (%v); resend only the paths reported as not committed", plan.txid, committed),
				Retryable: true,
			},
			Cause: fmt.Errorf("%s", summary),
		}
	}
	return summary, nil
}

// commitOp publishes one prepared target. A delete unlinks; a disk target is
// published from its staged temp; an overlay target goes back through the host
// transport so the user's unsaved buffer is updated with the file.
func (w atomicWrite) commitOp(ctx context.Context, op atomicPreparedOp) error {
	if op.mode == "delete" {
		if err := os.Remove(op.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fileops.FromContext(ctx).ObserveAbsent(fileops.DiskTarget(op.path, nil))
		return nil
	}
	if op.tmp == "" {
		// Overlay route: re-read through the host, re-verify, and write back, so
		// the buffer and the file move together.
		src, err := readEditSource(ctx, w.overlay, op.path)
		if err != nil {
			return err
		}
		if err := src.assertUnchanged(ctx, w.overlay, op.path); err != nil {
			return err
		}
		return src.write(ctx, w.overlay, op.path, string(op.newBytes))
	}
	return fileutil.PublishStagedWrite(op.tmp, op.path)
}

// journalPath is the journal file for one transaction.
func (w atomicWrite) journalPath(txid string) string {
	root := w.workDir
	if strings.TrimSpace(root) == "" {
		root, _ = os.Getwd()
	}
	return filepath.Join(root, filepath.FromSlash(atomicOpsJournalDir), txid+".json")
}

// atomicWriteJournal is the on-disk record of one transaction. It is written
// before the first rename and rewritten after the last, so a crash leaves a
// file that names the plan and, when the second write landed, the outcome.
type atomicWriteJournal struct {
	TxID       string               `json:"txid"`
	StartedAt  string               `json:"started_at"`
	FinishedAt string               `json:"finished_at,omitempty"`
	Planned    []atomicJournalEntry `json:"planned"`
	Committed  []string             `json:"committed,omitempty"`
	Failed     []string             `json:"failed,omitempty"`
}

type atomicJournalEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

func writeAtomicJournal(path string, journal atomicWriteJournal) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	return writeFileEncoded(path, string(encoded)+"\n", fileenc.UTF8)
}

// atomicTxID names one transaction. The clock is the only monotonic source
// available without shared state, and the id is a journal filename and a
// correlation key — never an authorization input — so a collision across two
// processes costs a journal overwrite, not a lost update.
func atomicTxID() string {
	return fmt.Sprintf("tx-%d", time.Now().UnixNano())
}
