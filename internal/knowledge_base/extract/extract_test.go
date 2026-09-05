package extract

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"reasonix/internal/knowledge_base/model"
)

func fixedTime() time.Time { return time.Unix(0, 0).UTC() }

func th(text string) model.Thought {
	return model.Thought{ID: model.NewID(), AgentID: "a1", Kind: model.ThoughtDecision, Text: text}
}

func TestChunkThoughtsDeterministic(t *testing.T) {
	thoughts := []model.Thought{th("first thought"), th("second thought")}
	a, _ := ChunkThoughts(thoughts, DefaultConfig())
	b, _ := ChunkThoughts(thoughts, DefaultConfig())
	if len(a) != 2 {
		t.Fatalf("want one chunk per thought, got %d", len(a))
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Text != b[i].Text || a[i].Order != i {
			t.Fatalf("chunks not deterministic at %d", i)
		}
	}
}

func TestChunkThoughtsOversizeDropped(t *testing.T) {
	big := strings.Repeat("很长的内容啊", 9000)
	chunks, drops := ChunkThoughts([]model.Thought{th("ok"), th(big)}, DefaultConfig())
	if len(chunks) != 1 || len(drops) != 1 {
		t.Fatalf("chunks=%d drops=%d, want 1/1", len(chunks), len(drops))
	}
	if drops[0].Reason != "oversize" {
		t.Fatalf("drop reason = %q", drops[0].Reason)
	}
}

func TestClassifyDeterministic(t *testing.T) {
	allow := Classify(model.SourceChunk{Text: "决策：采用单写队列保证顺序。"})
	if allow.Decision != DecisionAllow || allow.Kind != model.ItemDecision {
		t.Fatalf("decision text not allowed: %+v", allow)
	}
	if c := Classify(model.SourceChunk{Text: "禁止引入外部向量库"}); c.Kind != model.ItemConstraint {
		t.Fatalf("constraint not recognized: %+v", c)
	}
	if d := Classify(model.SourceChunk{Text: "lorem ipsum 占位文本"}); d.Decision != DecisionDeny {
		t.Fatalf("placeholder should be denied: %+v", d)
	}
	if d := Classify(model.SourceChunk{Text: "嗯"}); d.Decision != DecisionDeny {
		t.Fatalf("too-short should be denied: %+v", d)
	}
	if n := Classify(model.SourceChunk{Text: "今天温度适中，适合把索引做成可重建的读模型，多测几次边界"}); n.Decision != DecisionNeedsLLM {
		t.Fatalf("unmarked content should route to llm: %+v", n)
	}
}

func TestBuildItem(t *testing.T) {
	v := Classify(model.SourceChunk{Text: "决策：后端用 Go"})
	it := BuildItem(model.SourceChunk{Text: "决策：后端用 Go", SpanRefs: []string{"t1"}}, v, "a1", model.ScopeTeam, []model.Ref{{Kind: "decision", Target: "t1"}}, fixedTime())
	if it.Kind != model.ItemDecision || it.Title == "" || it.Body == "" {
		t.Fatalf("bad item: %+v", it)
	}
	if len([]rune(it.Title)) > 120 {
		t.Fatalf("title too long")
	}
}

type fakeExtractor struct {
	items []model.KnowledgeItem
	err   error
}

func (f fakeExtractor) Extract(ctx context.Context, text string) ([]model.KnowledgeItem, error) {
	return f.items, f.err
}

func TestBudgetedCapsCalls(t *testing.T) {
	f := fakeExtractor{items: []model.KnowledgeItem{}}
	b := NewBudgeted(f, 1)
	if _, err := b.Extract(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Extract(context.Background(), "y"); !errors.Is(err, ErrBudget) {
		t.Fatalf("second call = %v, want ErrBudget", err)
	}
}

func TestBudgetedPropagatesInnerError(t *testing.T) {
	boom := errors.New("provider down")
	b := NewBudgeted(fakeExtractor{err: boom}, 2)
	if _, err := b.Extract(context.Background(), "x"); !errors.Is(err, boom) {
		t.Fatalf("inner error not propagated: %v", err)
	}
}
