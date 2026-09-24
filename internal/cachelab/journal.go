package cachelab

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Journal appends experiment samples as one JSON object per line. One append is
// one write of a whole line, so a crash leaves at most a torn trailing line that
// readers skip; a reader therefore never has to repair the file.
//
// The journal is the run's own record and never production telemetry: it lives
// wherever the operator points it, and the sample type has no field that can
// carry prompt, tool argument or completion text.
type Journal struct {
	path string
	// guard holds literals that must never appear in a written line — the run's
	// prompt markers. A hit is a fixture bug and is refused, not redacted: a
	// record silently stripped of its content would be unreadable evidence.
	guard []string
	mu    sync.Mutex
	file  *os.File
	lines int
}

// OpenJournal creates or appends to one run's journal. The parent directory is
// created, and the file is 0600: an experiment record is operator-local.
func OpenJournal(path string) (*Journal, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("cachelab: journal path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cachelab: create journal dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cachelab: open journal: %w", err)
	}
	return &Journal{path: path, file: f}, nil
}

// Path is the journal's location, for a run summary.
func (j *Journal) Path() string { return j.path }

// Guard registers literals that must never be written. The driver registers the
// frozen fixture text here, so "no prompt in the journal" is enforced on the
// write path instead of trusted to review.
func (j *Journal) Guard(literals ...string) {
	for _, literal := range literals {
		literal = strings.TrimSpace(literal)
		if literal == "" || len(literal) < 8 || slices.Contains(j.guard, literal) {
			continue
		}
		j.guard = append(j.guard, literal)
	}
}

// Append writes one sample. Validation runs first: a sample that cannot name its
// own request identity or timing is a fixture defect and must not become
// evidence by being stored.
func (j *Journal) Append(s Sample) error {
	if err := s.Validate(); err != nil {
		return err
	}
	line, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("cachelab: marshal sample: %w", err)
	}
	if literal := j.firstGuardHit(line); literal != "" {
		return fmt.Errorf("cachelab: refusing to journal a line containing guarded literal %q", literal)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("cachelab: append sample: %w", err)
	}
	j.lines++
	return nil
}

// firstGuardHit reports the first registered literal that appears in a line.
func (j *Journal) firstGuardHit(line []byte) string {
	for _, literal := range j.guard {
		if bytes.Contains(line, []byte(literal)) {
			return literal
		}
	}
	return ""
}

// Lines is how many samples this journal has appended in this process.
func (j *Journal) Lines() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lines
}

// Close flushes and closes the journal. It is safe to call twice.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}

// ReadJournal reads a journal back, oldest first. A malformed line — a torn tail
// or a hand-edited file — is skipped rather than failing the whole read, and the
// skipped count is returned so the caller can report the loss instead of hiding
// it.
func ReadJournal(path string) ([]Sample, int, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	var out []Sample
	skipped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var s Sample
		if err := json.Unmarshal(line, &s); err != nil {
			skipped++
			continue
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return out, skipped, err
	}
	return out, skipped, nil
}

// ByRun keeps only one run's samples, so a journal that accumulates runs reports
// one run at a time. An empty run id keeps every sample, which is how a single
// run's file reads without knowing the id.
func ByRun(samples []Sample, runID string) []Sample {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return append([]Sample(nil), samples...)
	}
	out := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.RunID == runID {
			out = append(out, s)
		}
	}
	return out
}

// ByArm groups samples by arm, preserving each arm's recorded order.
func ByArm(samples []Sample) map[string][]Sample {
	out := map[string][]Sample{}
	for _, s := range samples {
		out[s.Arm] = append(out[s.Arm], s)
	}
	return out
}
