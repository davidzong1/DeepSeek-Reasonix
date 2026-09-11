package builtin

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

// maxAvailableReceiptIDs bounds the recovery list in a rejection. It exists so
// the model can pick one, not so it can replay the turn.
const maxAvailableReceiptIDs = 8

// resolveCitedReceipts turns host-issued receipt IDs into the facts they
// record. A citation the host never issued is rejected with the IDs that are
// actually available, so the model selects an existing fact instead of
// composing another command string for the host to match by text.
func resolveCitedReceipts(ctx context.Context, ids []string) ([]evidence.ReceiptRef, error) {
	ledger, ok := evidence.FromContext(ctx)
	if !ok || len(ids) == 0 {
		return nil, nil
	}
	out := make([]evidence.ReceiptRef, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		ref, found := ledger.ReceiptRef(id)
		switch {
		case !found:
			return nil, citedReceiptError(ledger, id, "was not issued by the host this turn")
		case !ref.Success:
			return nil, citedReceiptError(ledger, id, "records a call that failed, so it proves nothing")
		}
		out = append(out, ref)
	}
	return out, nil
}

func citedReceiptError(ledger *evidence.Ledger, id, why string) error {
	available := availableReceiptIDs(ledger)
	d := tool.OperationDiagnostic{
		Code:              tool.VerificationReceiptMissing,
		Recovery:          "cite one of the available receipt ids, run the verifier, or record the check as manual",
		AvailableReceipts: available,
		AllowedRecovery:   allowedReceiptRecovery(available),
		Retryable:         true,
		RetryBudget:       1,
	}
	return &tool.OperationError{Diagnostic: d, Cause: fmt.Errorf("receipt %q %s", id, why)}
}

// resolveCitedOperation settles which operation this sign-off is about and
// checks that every runtime-issued citation covers it. An omitted id is taken
// from the citations themselves: it is derived from the call's arguments, so the
// model can only know it by reading it off a receipt, and demanding it outright
// rejected sign-offs that named perfectly good evidence. Citations spanning
// several operations stay ambiguous and still have to name one.
func resolveCitedOperation(ctx context.Context, cited []evidence.ReceiptRef, operationID string) (string, error) {
	ledger, ok := evidence.FromContext(ctx)
	operationID = strings.TrimSpace(operationID)
	if !ok || len(cited) == 0 {
		return operationID, nil
	}
	if operationID == "" {
		operationID = citedOperation(ledger, cited)
	}
	for _, ref := range cited {
		if ref.OperationID == "" && operationID == "" {
			continue // compatibility for hand-built/legacy receipts
		}
		if operationID == "" {
			return "", citedReceiptMismatchError(ledger, ref.ID, operationID, "spans more than one operation, so the one being completed must be named")
		}
		if !ledger.ReceiptCoversOperation(ref.ID, operationID) {
			return "", citedReceiptMismatchError(ledger, ref.ID, operationID, "does not cover the cited operation")
		}
	}
	return operationID, nil
}

// citedOperation resolves the one operation the citations belong to. A receipt
// the host attached to an operation as its mutation or verification names that
// operation — the strong link a verifier has to satisfy — and otherwise the
// receipt's own stamp answers. Empty means the citations are ambiguous.
func citedOperation(ledger *evidence.Ledger, cited []evidence.ReceiptRef) string {
	ops := ledger.Operations()
	if ops == nil {
		return ""
	}
	snapshot := ops.Snapshot()
	operationID := ""
	for _, ref := range cited {
		current := ref.OperationID
		for _, candidate := range snapshot {
			if (candidate.Mutation != nil && candidate.Mutation.ID == ref.ID) ||
				(candidate.Verification != nil && candidate.Verification.ID == ref.ID) {
				current = candidate.ID
				break
			}
		}
		if current == "" {
			continue
		}
		if operationID == "" {
			operationID = current
			continue
		}
		if operationID != current {
			return ""
		}
	}
	return operationID
}

func citedReceiptMismatchError(ledger *evidence.Ledger, receiptID, operationID, why string) error {
	available := availableReceiptIDsForOperation(ledger, operationID)
	d := tool.OperationDiagnostic{
		Code:              tool.VerificationReceiptMismatch,
		OperationID:       strings.TrimSpace(operationID),
		Recovery:          "cite receipts attached to this operation, run a verifier for it, or record the check as manual",
		AvailableReceipts: available,
		AllowedRecovery:   allowedReceiptRecovery(available),
		Retryable:         true,
		RetryBudget:       1,
	}
	return &tool.OperationError{
		Diagnostic: d,
		Cause:      fmt.Errorf("receipt %q %s", receiptID, why),
	}
}

// allowedReceiptRecovery bounds the offered actions at its own boundary rather
// than trusting the caller's slice length: this list is for the model to choose
// from, so a long one is useless even when it is cheap.
func allowedReceiptRecovery(available []string) []string {
	out := make([]string, 0, maxAvailableReceiptIDs+2)
	for _, id := range available {
		if len(out) == maxAvailableReceiptIDs {
			break
		}
		out = append(out, tool.RecoveryUseReceipt+id)
	}
	return append(out, tool.RecoveryRunVerifier, tool.RecoveryMarkManual)
}

func availableReceiptIDs(ledger *evidence.Ledger) []string {
	refs := ledger.CitableReceipts(maxAvailableReceiptIDs)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.ID)
	}
	return out
}

func availableReceiptIDsForOperation(ledger *evidence.Ledger, operationID string) []string {
	if ledger == nil || strings.TrimSpace(operationID) == "" {
		return nil
	}
	refs := ledger.CitableReceipts(maxAvailableReceiptIDs)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ledger.ReceiptCoversOperation(ref.ID, operationID) {
			out = append(out, ref.ID)
		}
	}
	return out
}

func availableReceiptHint(ledger *evidence.Ledger) string {
	refs := ledger.CitableReceipts(maxAvailableReceiptIDs)
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		parts = append(parts, fmt.Sprintf("%s (%s: %s)", ref.ID, ref.Kind, ref.Summary))
	}
	return " — receipts available this turn: " + strings.Join(parts, ", ")
}

// missingVerificationReceipt reports a verification citation the host cannot
// resolve. It offers receipt IDs rather than asking for the command text
// again: retyping the command is what produced the loop.
func missingVerificationReceipt(ctx context.Context, ledger *evidence.Ledger, index int, command string) error {
	available := availableReceiptIDs(ledger)
	d := tool.OperationDiagnostic{
		Code:              tool.VerificationReceiptMissing,
		Recovery:          "cite the receipt id of the check that ran, run the verifier now, or record it as manual",
		AvailableReceipts: available,
		AllowedRecovery:   allowedReceiptRecovery(available),
		Retryable:         true,
		RetryBudget:       1,
	}
	cause := fmt.Errorf("evidence %d: verification command %q has no matching successful receipt%s%s",
		index, command, availableReceiptHint(ledger), allCommandHints(ctx, ledger))
	return &tool.OperationError{Diagnostic: d, Cause: cause}
}

// citedReceiptForOperation accepts legacy in-memory receipts without an
// operation ID, but every runtime-issued receipt must be tied to the explicit
// operation being signed off. This prevents a successful check for another
// change from satisfying the current completion.
func citedReceiptForOperation(ledger *evidence.Ledger, cited []evidence.ReceiptRef, operationID string, kinds ...string) bool {
	for _, ref := range cited {
		if !ref.Success || !slices.Contains(kinds, ref.Kind) {
			continue
		}
		if ref.OperationID == "" && strings.TrimSpace(operationID) == "" {
			return true // compatibility for hand-built/legacy receipts
		}
		if ledger != nil && ledger.ReceiptCoversOperation(ref.ID, operationID) {
			return true
		}
	}
	return false
}

// citedCoversPaths reports whether the cited receipts touched every named path.
// An empty kind accepts any receipt.
func citedCoversPaths(cited []evidence.ReceiptRef, kind string, paths []string) bool {
	if len(cited) == 0 || len(paths) == 0 {
		return false
	}
	for _, want := range paths {
		if !citedCoversPath(cited, kind, want) {
			return false
		}
	}
	return true
}

func citedCoversPath(cited []evidence.ReceiptRef, kind, want string) bool {
	needle := strings.ToLower(filepath.ToSlash(strings.TrimSpace(want)))
	if needle == "" {
		return false
	}
	for _, ref := range cited {
		if !ref.Success || (kind != "" && ref.Kind != kind) {
			continue
		}
		for _, observed := range ref.Paths {
			candidate := strings.ToLower(filepath.ToSlash(strings.TrimSpace(observed)))
			if candidate == needle || strings.HasSuffix(candidate, "/"+needle) || strings.HasSuffix(needle, "/"+candidate) {
				return true
			}
		}
	}
	return false
}
