package queue

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"reasonix/internal/fileutil"
)

const (
	eventsFile = "events.log"
	cursorFile = "cursor"
)

// Event is one durable job record. Payload is caller-owned JSON.
type Event struct {
	Seq     uint64          `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// ErrCursorAhead is returned when Commit would move the cursor backwards.
var ErrCursorAhead = errors.New("queue: commit regresses cursor")

// Queue is one team's append-only event log plus its committed cursor.
type Queue struct {
	mu        sync.Mutex
	dir       string
	file      *os.File
	maxSeq    uint64
	committed uint64
}

// Open opens (creating if needed) the queue in dir.
func Open(dir string) (*Queue, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, eventsFile)
	_, statErr := os.Stat(logPath)
	created := os.IsNotExist(statErr)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if created {
		syncDirEntries(dir) // best-effort so the new file's directory entry survives power loss
	}
	q := &Queue{dir: dir, file: f}
	if q.maxSeq, err = q.scanMax(); err != nil {
		f.Close()
		return nil, err
	}
	q.committed, err = q.readCursor()
	if err != nil {
		f.Close()
		return nil, err
	}
	if q.committed > q.maxSeq {
		f.Close()
		return nil, fmt.Errorf("queue: cursor %d past log %d", q.committed, q.maxSeq)
	}
	return q, nil
}

// Append durably records one event and returns its sequence.
func (q *Queue) Append(kind string, payload []byte) (uint64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	seq := q.maxSeq + 1
	ev := Event{Seq: seq, Kind: kind, Payload: payload}
	line, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	line = append(line, '\n')
	if _, err := q.file.Write(line); err != nil {
		return 0, err
	}
	if err := q.file.Sync(); err != nil {
		return 0, err
	}
	q.maxSeq = seq
	return seq, nil
}

// Committed returns the last confirmed sequence.
func (q *Queue) Committed() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.committed
}

// Commit persists the confirmed sequence atomically (temp+rename+fsync).
func (q *Queue) Commit(seq uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if seq <= q.committed {
		return nil // idempotent, non-regressing
	}
	if seq > q.maxSeq {
		return fmt.Errorf("%w: %d > %d", ErrCursorAhead, seq, q.maxSeq)
	}
	if err := fileutil.AtomicWriteFileStrict(filepath.Join(q.dir, cursorFile), []byte(strconv.FormatUint(seq, 10)), 0o600); err != nil {
		return err
	}
	q.committed = seq
	return nil
}

// Replay invokes fn for every event with seq > after, in log order.
func (q *Queue) Replay(after uint64, fn func(Event) error) error {
	f, err := os.Open(filepath.Join(q.dir, eventsFile))
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return err
		}
		if ev.Seq <= after {
			continue
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Close releases the log file handle.
func (q *Queue) Close() error { return q.file.Close() }

// syncDirEntries best-effort fsyncs dir so a newly created file's directory
// entry can survive power loss. Unsupported dir fsync (Windows / some network
// filesystems) is ignored; this is an optimization, never a correctness gate.
func syncDirEntries(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	_ = f.Sync()
}

func (q *Queue) scanMax() (uint64, error) {
	var max uint64
	err := q.Replay(0, func(ev Event) error {
		if ev.Seq > max {
			max = ev.Seq
		}
		return nil
	})
	return max, err
}

func (q *Queue) readCursor() (uint64, error) {
	data, err := os.ReadFile(filepath.Join(q.dir, cursorFile))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(string(data), 10, 64)
}
