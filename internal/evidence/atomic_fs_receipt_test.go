package evidence_test

import (
	"encoding/json"
	"testing"

	"reasonix/internal/evidence"
)

// The team atomic-FS surface mounts atomic_read/atomic_write in place of the
// legacy trio. Receipts are how the host decides whether a turn produced
// evidence: a writer receipt that is not classified as a write silently drops
// out of every "was this path actually changed" check, and a reader receipt that
// is not classified as a read drops out of the novelty accounting.
func TestAtomicPairClassifiesAsReaderAndWriter(t *testing.T) {
	read := evidence.ReceiptFromToolCall("atomic_read", json.RawMessage(`{"path":"internal/a.go","mode":"window"}`), true, true)
	if !read.Read {
		t.Error("atomic_read must produce a read receipt")
	}
	if read.Write {
		t.Error("atomic_read must not produce a write receipt")
	}
	if len(read.Paths) != 1 || read.Paths[0] != "internal/a.go" {
		t.Errorf("atomic_read receipt paths = %v, want the declared path", read.Paths)
	}

	write := evidence.ReceiptFromToolCall("atomic_write", json.RawMessage(`{"path":"internal/a.go","mode":"patch"}`), true, false)
	if !write.Write {
		t.Error("atomic_write must produce a write receipt; otherwise its paths never count as changed")
	}
	if write.Mutation != true {
		t.Error("atomic_write must be a content mutation")
	}
	if len(write.Paths) != 1 || write.Paths[0] != "internal/a.go" {
		t.Errorf("atomic_write receipt paths = %v, want the declared path", write.Paths)
	}

	// The effect classification must agree with the receipt, since permission
	// and evidence policy read the effects directly.
	effects := evidence.ClassifyToolCall("atomic_write", json.RawMessage(`{"path":"internal/a.go","mode":"patch"}`), false)
	if !effects.StateMutation || !effects.WorkspaceMutation || !effects.ContentMutation {
		t.Errorf("atomic_write effects = %+v, want a known workspace content write", effects)
	}
	readEffects := evidence.ClassifyToolCall("atomic_read", json.RawMessage(`{"path":"internal/a.go"}`), true)
	if readEffects.StateMutation || !readEffects.Known {
		t.Errorf("atomic_read effects = %+v, want a known read-only call", readEffects)
	}
}

// A multi-file transaction names every target, so the receipt covers each file
// the call can change — not just the first.
func TestAtomicWriteReceiptCoversEveryOpTarget(t *testing.T) {
	args := json.RawMessage(`{"ops":[{"path":"a.go","mode":"patch"},{"path":"b.go","mode":"replace"}]}`)
	receipt := evidence.ReceiptFromToolCall("atomic_write", args, true, false)
	if !receipt.Write {
		t.Fatal("a transaction receipt must still be a write")
	}
	for _, want := range []string{"a.go", "b.go"} {
		found := false
		for _, got := range receipt.Paths {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("transaction receipt is missing %s: %v", want, receipt.Paths)
		}
	}
}
