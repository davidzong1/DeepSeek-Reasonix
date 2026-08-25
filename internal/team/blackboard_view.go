package team

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// ViewLevel selects the bounded materialized view (route §3.1).
type ViewLevel int

const (
	ViewLevelIndex  ViewLevel = iota // L0: team/task index, actions, latest seq
	ViewLevelDelta                   // L1: summary delta since the cursor
	ViewLevelDetail                  // L2: conclusions, assignments, evidence, checkpoints
)

// View budgets and rendering limits (route §3.1/§3.2). The per-line limit
// keeps any single line well under its view budget, so budgetRender never
// sees an unrenderable first line.
const (
	viewBudgetL0 = 2 << 10 // L0 index cap
	viewBudgetL1 = 4 << 10 // L1 delta cap
	viewBudgetL2 = 8 << 10 // L2 detail cap
	viewLineTail = 120     // per-line summary truncation, in runes
	viewPageSize = 256     // ReadAfter page bound for one turn's delta
)

// BoardView is one materialized view (route §3.1): bounded rendered content
// plus the source position, epoch and digest a consumer needs for caching
// and dedup. Truncated means the budget cut the tail; Stale means the
// content is the previous turn's render, not a fresh one (§3.2).
type BoardView struct {
	BoardID   string
	Level     ViewLevel
	SourceSeq int64
	Epoch     uint64
	Content   string
	Digest    string
	Truncated bool
	Stale     bool
}

// ViewBuilder renders L0-L2 views from BoardEvents. It is stateless beyond
// board and epoch, so identical inputs always render identical bytes; a
// fresh builder serves each (board, epoch) pair (route §3.2).
type ViewBuilder struct {
	boardID string
	epoch   uint64
}

// NewViewBuilder returns a builder for one (board, epoch) pair.
func NewViewBuilder(boardID string, epoch uint64) *ViewBuilder {
	return &ViewBuilder{boardID: boardID, epoch: epoch}
}

// kindPriority ranks kinds for bounded rendering (route §3.2): instructions
// outrank conclusions, which outrank evidence and history. Unknown kinds
// rank as history, so a new kind degrades gracefully instead of winning.
var kindPriority = map[EventKind]int{
	EventAssignment: 4, // 指令
	EventConclusion: 3, // 最新结论
	EventEvidence:   2, // 验收证据
	EventCheckpoint: 1,
	EventReport:     0, // 历史报告
	EventSupersede:  0,
}

// L0 renders the index view: header with the latest seq, per-task counts
// accumulated by the caller, then action lines in kind-priority order, all
// inside the L0 budget.
func (b *ViewBuilder) L0(counts map[string]int, actions []BoardEvent) BoardView {
	latest := latestSeq(actions)
	lines := []string{
		"# L0 " + b.boardID,
		"epoch " + strconv.FormatUint(b.epoch, 10) + " seq " + strconv.FormatInt(latest, 10),
	}
	tasks := sortedTaskIDs(counts)
	lines = append(lines, "tasks "+strconv.Itoa(len(tasks)))
	for _, t := range tasks {
		lines = append(lines, "task "+t+": "+strconv.Itoa(counts[t])+" items")
	}
	for _, ev := range sortByPriority(actions) {
		lines = append(lines, "- "+string(ev.Kind)+" "+string(ev.TaskID)+" "+truncateLine(ev.Summary, viewLineTail))
	}
	content, truncated := budgetRender(lines, viewBudgetL0)
	return BoardView{
		BoardID: b.boardID, Level: ViewLevelIndex, SourceSeq: latest, Epoch: b.epoch,
		Content: content, Digest: contentDigest(content), Truncated: truncated,
	}
}

// L1 renders the delta view: summaries of events after afterSeq, bounded by
// the L1 budget. Cost scales with the delta, never the board history (§3.4).
func (b *ViewBuilder) L1(afterSeq int64, delta []BoardEvent) BoardView {
	latest := afterSeq
	if n := len(delta); n > 0 {
		latest = delta[n-1].Seq
	}
	lines := []string{
		"# L1 delta " + b.boardID,
		"after " + strconv.FormatInt(afterSeq, 10) + " items " + strconv.Itoa(len(delta)),
	}
	for _, ev := range delta {
		lines = append(lines, "- seq="+strconv.FormatInt(ev.Seq, 10)+" "+string(ev.Kind)+" "+ev.MemberID+" "+string(ev.TaskID)+" "+truncateLine(ev.Summary, viewLineTail))
	}
	content, truncated := budgetRender(lines, viewBudgetL1)
	return BoardView{
		BoardID: b.boardID, Level: ViewLevelDelta, SourceSeq: latest, Epoch: b.epoch,
		Content: content, Digest: contentDigest(content), Truncated: truncated,
	}
}

// L2 renders the detail view: the newest event per task from the checkpoint
// fold plus any delta events newer than the fold, in sections ordered by
// kind priority (route §3.2).
func (b *ViewBuilder) L2(checkpoints []CheckpointSummary, delta []BoardEvent) BoardView {
	merged := map[TaskID]BoardEvent{}
	for _, cp := range checkpoints {
		merged[cp.TaskID] = BoardEvent{
			Seq: cp.SourceSeq, Kind: cp.Kind, TaskID: cp.TaskID,
			Summary: cp.Summary, ArtifactRefs: cp.ArtifactRefs,
		}
	}
	for _, ev := range delta {
		if old, ok := merged[ev.TaskID]; !ok || ev.Seq > old.Seq {
			merged[ev.TaskID] = ev
		}
	}
	var items []BoardEvent
	for _, ev := range merged {
		items = append(items, ev)
	}
	items = sortByPriority(items)
	lines := []string{"# L2 detail " + b.boardID}
	section := ""
	for _, ev := range items {
		if s := sectionFor(ev.Kind); s != section {
			lines = append(lines, s+":")
			section = s
		}
		lines = append(lines, "- task="+string(ev.TaskID)+" seq="+strconv.FormatInt(ev.Seq, 10)+" "+truncateLine(ev.Summary, 180))
		for _, ar := range ev.ArtifactRefs {
			lines = append(lines, "  artifact "+ar.Name+" "+ar.Path+" "+ar.Digest)
		}
	}
	content, truncated := budgetRender(lines, viewBudgetL2)
	return BoardView{
		BoardID: b.boardID, Level: ViewLevelDetail, SourceSeq: latestSeq(items), Epoch: b.epoch,
		Content: content, Digest: contentDigest(content), Truncated: truncated,
	}
}

// CheckpointSummary folds one task's event tail into a single bounded
// record (route §3.2): the newest event's fields plus the seqs it
// supersedes, so compaction keeps the audit chain and a resync reads the
// fold plus only the delta after SourceSeq.
type CheckpointSummary struct {
	TaskID       TaskID
	Kind         EventKind
	Epoch        uint64
	SourceSeq    int64
	Supersedes   []int64
	Summary      string
	ArtifactRefs []ArtifactRef
}

// foldCheckpoints compacts events into one CheckpointSummary per task: the
// newest event survives and every other seq of the task is listed as
// superseded (route §3.2). The result is sorted by task id.
func foldCheckpoints(events []BoardEvent, epoch uint64) []CheckpointSummary {
	newest := map[TaskID]BoardEvent{}
	for _, ev := range events {
		if old, ok := newest[ev.TaskID]; !ok || ev.Seq > old.Seq {
			newest[ev.TaskID] = ev
		}
	}
	out := make([]CheckpointSummary, 0, len(newest))
	for _, task := range sortedTaskKeys(newest) {
		ev := newest[task]
		var superseded []int64
		for _, old := range events {
			if old.TaskID == ev.TaskID && old.Seq != ev.Seq {
				superseded = append(superseded, old.Seq)
			}
		}
		sort.Slice(superseded, func(i, j int) bool { return superseded[i] < superseded[j] })
		out = append(out, CheckpointSummary{
			TaskID: ev.TaskID, Kind: ev.Kind, Epoch: epoch, SourceSeq: ev.Seq,
			Supersedes: superseded, Summary: ev.Summary, ArtifactRefs: ev.ArtifactRefs,
		})
	}
	return out
}

// latestSeq reports the newest seq in a seq-ascending slice; 0 when empty.
func latestSeq(events []BoardEvent) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}

func sortedTaskIDs(counts map[string]int) []string {
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedTaskKeys(newest map[TaskID]BoardEvent) []TaskID {
	ids := make([]TaskID, 0, len(newest))
	for id := range newest {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// sortByPriority orders events for bounded rendering: kind priority desc,
// then seq asc — determinism keeps digests byte-stable.
func sortByPriority(events []BoardEvent) []BoardEvent {
	out := append([]BoardEvent(nil), events...)
	sort.SliceStable(out, func(i, j int) bool {
		if pi, pj := kindPriority[out[i].Kind], kindPriority[out[j].Kind]; pi != pj {
			return pi > pj
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

func sectionFor(k EventKind) string {
	switch k {
	case EventAssignment:
		return "assignments"
	case EventConclusion:
		return "conclusions"
	case EventEvidence:
		return "evidence"
	case EventCheckpoint:
		return "checkpoints"
	default:
		return "history"
	}
}

// truncateLine shortens a summary to limit runes; the fixed ellipsis keeps
// rendered bytes deterministic across turns.
func truncateLine(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// contentDigest hashes rendered view bytes; identical input events and
// epoch produce identical digests (cache-first).
func contentDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// budgetRender appends lines until the byte budget is exhausted and reports
// whether the tail was cut. Callers pre-truncate single lines (truncateLine)
// so no one line can exceed the budget alone (route §3.2).
func budgetRender(lines []string, cap int) (string, bool) {
	var out strings.Builder
	for _, l := range lines {
		if out.Len() > 0 && out.Len()+1+len(l) > cap {
			return out.String(), true
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(l)
	}
	return out.String(), false
}
