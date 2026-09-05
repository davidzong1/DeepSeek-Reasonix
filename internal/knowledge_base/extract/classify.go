package extract

import (
	"strings"
	"time"

	"reasonix/internal/knowledge_base/model"
)

// Decision is the deterministic first gate over a chunk.
type Decision string

const (
	DecisionAllow    Decision = "allow" // rule-promotable now
	DecisionDeny     Decision = "deny"  // noise / non-knowledge
	DecisionNeedsLLM Decision = "needs_llm"
)

// Verdict carries the classification decision and, for allow, the kind hint.
type Verdict struct {
	Decision   Decision
	Kind       model.ItemKind
	Confidence float64
	Reason     string
}

type rule struct {
	markers []string
	kind    model.ItemKind
	conf    float64
}

// orderedRules fire on the first marker hit (normalized lowercase).
var orderedRules = []rule{
	{[]string{"不得", "禁止", "禁止：", "must not", "never do", "严禁"}, model.ItemConstraint, 0.9},
	{[]string{"决策", "决定：", "决定:", "decision:", "we decided", "我们决定"}, model.ItemDecision, 0.9},
	{[]string{"约定", "规范", "convention:", "convention "}, model.ItemConvention, 0.8},
	{[]string{"经验", "结论：", "结论:", "conclusion:", "lesson learned"}, model.ItemFact, 0.75},
	{[]string{"警告", "caution:", "warning:"}, model.ItemWarning, 0.5},
}

var noiseMarkers = []string{
	"lorem ipsum", "占位", "placeholder", "待补充", "example to replace",
}

// Classify maps one chunk to allow / deny / needs_llm deterministically.
func Classify(chunk model.SourceChunk) Verdict {
	t := strings.ToLower(strings.TrimSpace(chunk.Text))
	if t == "" {
		return Verdict{Decision: DecisionDeny, Reason: "empty"}
	}
	for _, m := range noiseMarkers {
		if strings.Contains(t, m) {
			return Verdict{Decision: DecisionDeny, Reason: "placeholder"}
		}
	}
	for _, r := range orderedRules {
		for _, m := range r.markers {
			if strings.Contains(t, m) {
				return Verdict{
					Decision: DecisionAllow, Kind: r.kind,
					Confidence: r.conf, Reason: "marker:" + m,
				}
			}
		}
	}
	if runes := len([]rune(t)); runes < 10 {
		return Verdict{Decision: DecisionDeny, Reason: "too-short"}
	}
	return Verdict{Decision: DecisionNeedsLLM, Reason: "no-rule-match"}
}

// BuildItem turns an allowed chunk into a candidate knowledge item. Status and
// canonical stay empty until the manager's quality gate stamps them.
func BuildItem(chunk model.SourceChunk, v Verdict, author string, scope model.Scope, prov []model.Ref, now time.Time) model.KnowledgeItem {
	body := strings.TrimSpace(chunk.Text)
	return model.KnowledgeItem{
		AuthorID:   author,
		Title:      titleOf(body),
		Kind:       v.Kind,
		Scope:      scope,
		Provenance: prov,
		Quality: model.QualitySignal{
			Confidence:  v.Confidence,
			ReviewLevel: model.ReviewNone,
		},
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Body:      body,
	}
}

// titleOf picks the first meaningful line, else a bounded prefix of the body.
func titleOf(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#-* \t")
		if line != "" {
			return clip(line, 120)
		}
	}
	return clip(body, 120)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
