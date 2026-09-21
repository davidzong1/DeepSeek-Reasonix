package agent

import (
	"context"
	"strings"
	"unicode"
)

type reasoningLanguageContextKey struct{}
type responseLanguageContextKey struct{}

// NormalizeReasoningLanguage returns one of auto|zh|en for runtime-only visible
// reasoning preferences. Keep this local to agent so sub-agents can inherit the
// preference without depending on config.
func NormalizeReasoningLanguage(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "zh", "cn", "chinese", "中文":
		return "zh"
	case "en", "english":
		return "en"
	default:
		return "auto"
	}
}

// NormalizeResponseLanguage returns one of auto|zh|en for final-answer language
// preferences. Auto keeps the stable same-as-user language policy.
func NormalizeResponseLanguage(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "zh", "cn", "chinese", "中文":
		return "zh"
	case "en", "english":
		return "en"
	default:
		return "auto"
	}
}

// ResponseLanguageBlock is transient user-turn context for final answers. It
// stays out of the stable system prompt so changing the preference between turns
// does not churn the cached prefix.
func ResponseLanguageBlock(lang string) string {
	switch NormalizeResponseLanguage(lang) {
	case "zh":
		return "<response-language>\nFinal answer language preference: use Simplified Chinese for user-facing replies unless the user explicitly asks for another language. Keep code, identifiers, file paths, shell commands, and untranslated technical terms in their original form.\n</response-language>"
	case "en":
		return "<response-language>\nFinal answer language preference: use English for user-facing replies unless the user explicitly asks for another language. Keep code, identifiers, file paths, shell commands, and untranslated technical terms in their original form.\n</response-language>"
	default:
		return ""
	}
}

// ReasoningLanguageBlock is transient user-turn context. It deliberately does
// not belong in the stable system prompt or tool schemas.
func ReasoningLanguageBlock(lang string) string {
	switch NormalizeReasoningLanguage(lang) {
	case "zh":
		// Imperative wording measured against soft "偏好……请使用" phrasing:
		// the soft form loses the first reasoning segment on Chinese prompts
		// that embed English logs/code, and the first segment anchors the
		// whole turn once providers round-trip prior reasoning.
		return "<reasoning-language>\n必须使用简体中文书写全部可见思考/推理文本：从第一个字开始就用中文，并在整轮内保持中文，即使系统提示词、工具说明、工具输出或引用的代码是英文。代码、标识符、文件路径、shell 命令和未翻译的技术术语保持原文。此要求只约束可见思考文本，不覆盖用户对最终回答语言的明确要求。\n</reasoning-language>"
	case "en":
		return "<reasoning-language>\nVisible reasoning/thinking text preference: use English when the provider exposes reasoning text. Keep code, identifiers, file paths, shell commands, and untranslated technical terms in their original form. This preference does not override an explicit user request for the final answer language.\n</reasoning-language>"
	default:
		return ""
	}
}

// ResolveReasoningLanguage returns the concrete visible-reasoning language for
// a turn. Explicit zh/en settings win; auto anchors clear Chinese user prompts
// and otherwise stays provider-default to preserve the historical no-injection
// behaviour for English and ambiguous turns.
func ResolveReasoningLanguage(lang, source string) string {
	mode := NormalizeReasoningLanguage(lang)
	if mode != "auto" {
		return mode
	}
	return InferReasoningLanguageFromText(source)
}

// InferReasoningLanguageFromText conservatively detects Chinese user-authored
// turns for auto reasoning-language mode. It strips Reasonix-injected context
// wrappers first so large @file payloads or transient XML blocks do not drown
// out the user's actual prompt. English and ambiguous turns intentionally return
// auto, preserving the old no-extra-instruction behaviour.
func InferReasoningLanguageFromText(source string) string {
	source = reasoningLanguageSourceText(source)
	if source == "" {
		return "auto"
	}
	han, cjkPunct := reasoningLanguageScriptCounts(source)
	switch {
	case han >= 4:
		return "zh"
	case han >= 2 && (cjkPunct > 0 || hasChineseReasoningCue(source)):
		return "zh"
	default:
		return "auto"
	}
}

func reasoningLanguageSourceText(source string) string {
	s := strings.TrimSpace(StripTransientUserBlocks(source))
	const preamble = "Referenced context:"
	if !strings.HasPrefix(s, preamble) {
		return s
	}
	s = strings.TrimSpace(s[len(preamble):])
	for {
		s = strings.TrimSpace(s)
		if s == "" || !strings.HasPrefix(s, "<") {
			return s
		}
		tagEnd := strings.IndexAny(s, " >\t\r\n")
		if tagEnd <= 1 {
			return s
		}
		tag := s[1:tagEnd]
		switch tag {
		case "file", "dir", "resource", "image":
			closeTag := "</" + tag + ">"
			i := strings.Index(s, closeTag)
			if i < 0 {
				return s
			}
			s = strings.TrimSpace(s[i+len(closeTag):])
		default:
			return s
		}
	}
}

func reasoningLanguageScriptCounts(source string) (han, cjkPunct int) {
	for _, r := range source {
		switch {
		case unicode.In(r, unicode.Han):
			han++
		case isCJKPunctuation(r):
			cjkPunct++
		}
	}
	return han, cjkPunct
}

func isCJKPunctuation(r rune) bool {
	switch {
	case r >= 0x3000 && r <= 0x303F:
		return true
	case r >= 0xFF00 && r <= 0xFFEF:
		return true
	default:
		return false
	}
}

func hasChineseReasoningCue(source string) bool {
	for _, cue := range chineseReasoningLanguageCues {
		if strings.Contains(source, cue) {
			return true
		}
	}
	return false
}

var chineseReasoningLanguageCues = []string{
	"你好", "请", "帮我", "帮忙", "看看", "看下", "解释", "说明", "总结", "分析",
	"修复", "实现", "优化", "排查", "处理", "继续", "为什么", "怎么",
	"是否", "能否", "支持", "设置", "中文", "思考", "问题", "报错",
	"代码", "文件", "这个", "那个",
}

func reasoningLanguageBlockForSource(lang, source string) string {
	return ReasoningLanguageBlock(ResolveReasoningLanguage(lang, source))
}

// WithResponseLanguage prefixes content with the transient response-language
// block unless the turn already starts with one.
func WithResponseLanguage(content, lang string) string {
	block := ResponseLanguageBlock(lang)
	if block == "" || hasLeadingInjectedBlock(content, "response-language") {
		return content
	}
	return block + "\n\n" + content
}

// WithReasoningLanguage prefixes content with the transient reasoning-language
// block unless the turn already starts with an injected reasoning-language
// block. User-authored mentions of the tag later in the prompt must not suppress
// the configured preference.
func WithReasoningLanguage(content, lang string) string {
	return WithReasoningLanguageForSource(content, lang, content)
}

// WithReasoningLanguageForSource prefixes content using source as the language
// signal for auto mode. Callers that expand @references should pass the raw
// user prompt as source so referenced English code or logs do not override the
// user's actual conversation language.
func WithReasoningLanguageForSource(content, lang, source string) string {
	block := reasoningLanguageBlockForSource(lang, source)
	if block == "" || hasLeadingInjectedBlock(content, "reasoning-language") {
		return content
	}
	return block + "\n\n" + content
}

// WithReasoningLanguageOnce is WithReasoningLanguageForSource with a
// conversation-scoped memory: the block states a session constant, so injecting
// it on every user turn repeats bytes the model has already read in every
// request prefix. The first turn that resolves to zh (or en) carries it; later
// turns in the same conversation do not. A caller that supplies the block
// itself counts as carrying it, so an explicit injection is never followed by a
// second one.
//
// lang is the caller's preference rather than the agent's own atomic: hosts
// that compose the turn outside the agent hold the authoritative value, so the
// two can never disagree about what was injected. A language change between
// turns clears the memory, so the new preference re-injects exactly once.
func (a *Agent) WithReasoningLanguageOnce(content, lang, source string) string {
	if a == nil {
		return WithReasoningLanguageForSource(content, lang, source)
	}
	resolved := ResolveReasoningLanguage(lang, source)
	carried := hasLeadingInjectedBlock(content, "reasoning-language")
	if resolved == "auto" {
		// Auto on an English or ambiguous turn carries no block at all; an
		// explicit one the caller supplied is still remembered, so a later turn
		// that resolves to zh injects on its own merits.
		if carried {
			a.noteReasoningLanguageCarried("")
		}
		return content
	}
	if carried || a.reasoningLanguageCarried() == resolved {
		a.noteReasoningLanguageCarried(resolved)
		return content
	}
	block := ReasoningLanguageBlock(resolved)
	if block == "" {
		return content
	}
	a.noteReasoningLanguageCarried(resolved)
	return block + "\n\n" + content
}

// reasoningLanguageCarried reports which concrete block this conversation
// already carries ("" when none).
func (a *Agent) reasoningLanguageCarried() string {
	a.sess.mu.Lock()
	defer a.sess.mu.Unlock()
	return a.sess.reasoningLanguageInjected
}

func (a *Agent) noteReasoningLanguageCarried(mode string) {
	a.sess.mu.Lock()
	a.sess.reasoningLanguageInjected = mode
	a.sess.mu.Unlock()
}

// hasLeadingInjectedBlock reports whether target is already among the transient
// blocks leading content, skipping past any other injected block on the way.
// It walks TransientUserBlockTags rather than a list of its own: when the two
// disagreed, a block the host had started injecting was treated as user prose
// and stopped the walk early, so an already-present target went undetected and
// was injected a second time.
func hasLeadingInjectedBlock(content, target string) bool {
	s := strings.TrimLeft(content, " \t\r\n")
	for {
		if hasOpenTag(s, target) {
			return strings.Contains(s, "</"+target+">")
		}
		skipped := false
		for _, tag := range TransientUserBlockTags {
			if tag == target || !hasOpenTag(s, tag) {
				continue
			}
			rest, ok := trimLeadingTransientBlock(s, tag)
			if !ok {
				return false
			}
			s, skipped = rest, true
			break
		}
		if !skipped {
			return false
		}
	}
}

// hasOpenTag reports whether s opens with tag, with or without attributes
// (hook-context and capability-route carry them).
func hasOpenTag(s, tag string) bool {
	return strings.HasPrefix(s, "<"+tag+">") || strings.HasPrefix(s, "<"+tag+" ")
}

func trimLeadingTransientBlock(content, tag string) (string, bool) {
	closeTag := "</" + tag + ">"
	_, after, ok := strings.Cut(content, closeTag)
	if !ok {
		return content, false
	}
	return strings.TrimLeft(after, " \t\r\n"), true
}

// WithResponseLanguagePreference carries the runtime final-answer language
// preference to spawned tools and sub-agents.
func WithResponseLanguagePreference(ctx context.Context, lang string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, responseLanguageContextKey{}, NormalizeResponseLanguage(lang))
}

// ResponseLanguageFromContext returns auto|zh|en.
func ResponseLanguageFromContext(ctx context.Context) string {
	if ctx == nil {
		return "auto"
	}
	if v, ok := ctx.Value(responseLanguageContextKey{}).(string); ok {
		return NormalizeResponseLanguage(v)
	}
	return "auto"
}

// WithReasoningLanguagePreference carries the runtime preference to spawned
// tools, especially sub-agents whose first user turn is created outside the
// parent controller. It stores auto explicitly so live zh/en -> auto changes
// clear stale boot-time preferences in child paths.
func WithReasoningLanguagePreference(ctx context.Context, lang string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, reasoningLanguageContextKey{}, NormalizeReasoningLanguage(lang))
}

// ReasoningLanguageFromContext returns auto|zh|en.
func ReasoningLanguageFromContext(ctx context.Context) string {
	if ctx == nil {
		return "auto"
	}
	if v, ok := ctx.Value(reasoningLanguageContextKey{}).(string); ok {
		return NormalizeReasoningLanguage(v)
	}
	return "auto"
}
