package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// readFileBudgetAB measures the read_file preview budget synthetically: the same
// bodies rendered under the shipped budget and under each candidate, so the
// bytes saved and the page-reads it costs are read off one run.
//
// This curve is not the verdict. A real-provider A/B over this same question
// (two budgets, 2x10 paired runs, forced full read and natural question) found
// the smaller budget *costing* prompt tokens — median +21,502 and +6,882 — with
// every pair differing in the same direction. The reason is the term this
// analysis cannot see: a cut preview forces roughly one more round trip per
// session, and each round trip resends the whole accumulated context, which
// swamps the per-page bytes saved. Use this test to pick which budgets are worth
// paying to measure; never to ship one.
func TestReadFileBudgetAB(t *testing.T) {
	corpus := readFileBudgetCorpus(t)
	if len(corpus) < 100 {
		t.Fatalf("corpus has %d files; too few to say anything", len(corpus))
	}
	current := readFileBudgetArm(corpus, maxToolOutputBytes)
	t.Logf("corpus: %d files, %d bytes", current.files, current.raw)
	t.Logf("%-9s %12s %8s %7s %7s %7s", "budget", "visible", "saved", "meanPg", "p90Pg", "maxPg")
	for _, budget := range []int{32 << 10, 24 << 10, 20 << 10, 18 << 10, 16 << 10, 12 << 10, 8 << 10} {
		arm := readFileBudgetArm(corpus, budget)
		t.Logf("%-9s %12d %7.1f%% %7.2f %7d %7d",
			byteBudget(budget), arm.visible, arm.saved(current),
			arm.meanPages(), arm.p90Pages(), arm.maxPages)
	}
	// A sanity bound, not a verdict: the decision needs a human reading the
	// tradeoff, so this only catches an arm that saves nothing or explodes.
	arm := readFileBudgetArm(corpus, 8<<10)
	if arm.saved(current) <= 0 {
		t.Fatalf("an 8 KiB budget saved nothing over the shipped one")
	}
	if arm.meanPages() > 4*current.meanPages() {
		t.Fatalf("an 8 KiB budget costs %.1f mean page-reads against the control's %.1f",
			arm.meanPages(), current.meanPages())
	}
	// The control arm must actually truncate, or the A/B compares against nothing.
	if current.visible >= current.raw {
		t.Fatalf("control arm kept %d of %d raw bytes; it is not truncating", current.visible, current.raw)
	}
}

// budgetArm is one arm's totals over the corpus. Pages count every file, so an
// untruncated body contributes the single read it really costs.
type budgetArm struct {
	files     int
	raw       int64
	visible   int64
	pages     []int
	maxPages  int
	truncated int
}

func (a budgetArm) saved(control budgetArm) float64 {
	if control.visible == 0 {
		return 0
	}
	return 100 * float64(control.visible-a.visible) / float64(control.visible)
}

func (a budgetArm) meanPages() float64 {
	if a.files == 0 {
		return 0
	}
	sum := 0
	for _, p := range a.pages {
		sum += p
	}
	return float64(sum) / float64(a.files)
}

func (a budgetArm) p90Pages() int {
	if len(a.pages) == 0 {
		return 0
	}
	sorted := append([]int(nil), a.pages...)
	slices.Sort(sorted)
	return sorted[len(sorted)*9/10]
}

// readFileBudgetArm renders every body under one budget. A body at or under the
// budget is passed through untouched, which is what the production gate does.
func readFileBudgetArm(corpus map[string]string, budget int) budgetArm {
	var arm budgetArm
	for path, body := range corpus {
		arm.files++
		arm.raw += int64(len(body))
		if len(body) <= budget {
			arm.visible += int64(len(body))
			arm.pages = append(arm.pages, 1)
			continue
		}
		head, _ := truncateReadFileOutput(body, "read_file", path, budget)
		arm.visible += int64(len(head))
		arm.truncated++
		if kept := len(head); kept > 0 {
			pages := (len(body) + kept - 1) / kept
			arm.pages = append(arm.pages, pages)
			if pages > arm.maxPages {
				arm.maxPages = pages
			}
		}
	}
	return arm
}

// readFileBudgetCorpus is the real thing read_file is pointed at: tracked source
// files, which is what makes the size distribution meaningful.
func readFileBudgetCorpus(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	corpus := map[string]string{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() < 8<<10 {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		corpus[path] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return corpus
}

func byteBudget(n int) string {
	switch {
	case n >= 1<<20:
		return strconv.Itoa(n>>20) + " MiB"
	case n >= 1<<10:
		return strconv.Itoa(n>>10) + " KiB"
	}
	return strconv.Itoa(n) + " B"
}
