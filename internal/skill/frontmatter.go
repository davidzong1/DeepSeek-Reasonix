package skill

import (
	"slices"
	"strings"

	"reasonix/internal/frontmatter"
)

// Frontmatter vocabulary and the value coercions applied to it. A key listed
// here is also what marks a bare <name>.md from another tool's root as a skill,
// so the list is the discovery contract, not just a parsing table.

const (
	skillFrontmatterDescription      = "description"
	skillFrontmatterName             = "name"
	skillFrontmatterRunAs            = "runas"
	skillFrontmatterContext          = "context"
	skillFrontmatterAgent            = "agent"
	skillFrontmatterAllowedTools     = "allowed-tools"
	skillFrontmatterModel            = "model"
	skillFrontmatterEffort           = "effort"
	skillFrontmatterReadOnly         = "read-only"
	skillFrontmatterTriggers         = "triggers"
	skillFrontmatterNegativeTriggers = "negative-triggers"
	skillFrontmatterAutoUse          = "auto-use"
	skillFrontmatterNeedsFreshData   = "needs-fresh-data"
	skillFrontmatterCost             = "cost"
	skillFrontmatterColor            = "color"
	skillFrontmatterInvocation       = "invocation"
	skillFrontmatterRequires         = "requires"
	skillFrontmatterProfiles         = "profiles"
)

var skillMarkerFrontmatterKeys = []string{
	skillFrontmatterDescription,
	skillFrontmatterName,
	skillFrontmatterRunAs,
	skillFrontmatterContext,
	skillFrontmatterAgent,
	skillFrontmatterAllowedTools,
	skillFrontmatterModel,
	skillFrontmatterEffort,
	skillFrontmatterReadOnly,
	skillFrontmatterTriggers,
	skillFrontmatterNegativeTriggers,
	skillFrontmatterAutoUse,
	skillFrontmatterNeedsFreshData,
	skillFrontmatterCost,
	skillFrontmatterColor,
	skillFrontmatterInvocation,
	skillFrontmatterRequires,
	skillFrontmatterProfiles,
}

func hasSkillMarker(content string, fm map[string]string) bool {
	for _, key := range skillMarkerFrontmatterKeys {
		if strings.TrimSpace(fm[key]) != "" {
			return true
		}
	}
	return frontmatterHasSkillMarkerKey(content)
}

func frontmatterHasSkillMarkerKey(content string) bool {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return false
	}
	for _, line := range lines[1:end] {
		key, _, ok := strings.Cut(line, ":")
		if ok && isSkillMarkerFrontmatterKey(strings.ToLower(strings.TrimSpace(key))) {
			return true
		}
	}
	return false
}

func isSkillMarkerFrontmatterKey(key string) bool {
	return slices.Contains(skillMarkerFrontmatterKeys, key)
}

// parseAllowedTools splits a comma-separated `allowed-tools` value into trimmed,
// non-empty tool names; nil when absent.
func parseAllowedTools(raw string) []string {
	return parseCSVFrontmatter(raw)
}

// parseCSVFrontmatter splits simple comma-separated frontmatter values. Full
// YAML lists are intentionally out of scope for the existing frontmatter parser.
func parseCSVFrontmatter(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimSpace(raw[1 : len(raw)-1])
	}
	var out []string
	for p := range strings.SplitSeq(raw, ",") {
		if t := strings.Trim(strings.TrimSpace(p), `"'`); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parseAutoUse(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "off", "suggest", "prefer", "require":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

// parseProfilesFrontmatter keeps only economy|balanced|delivery values and
// returns the rejected ones separately so doctor can surface typos instead of
// the parser hiding them.
func parseProfilesFrontmatter(raw string) (valid, invalid []string) {
	seen := map[string]bool{}
	for _, p := range parseCSVFrontmatter(raw) {
		p = strings.ToLower(strings.TrimSpace(p))
		switch p {
		case "economy", "balanced", "delivery":
			if !seen[p] {
				seen[p] = true
				valid = append(valid, p)
			}
		case "":
		default:
			if !seen[p] {
				seen[p] = true
				invalid = append(invalid, p)
			}
		}
	}
	return valid, invalid
}

func parseBoolFrontmatter(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "1", "on":
		return true
	default:
		return false
	}
}

func parseCost(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

// parseInvocation maps frontmatter to an invocation mode. Anything other than
// "manual" (including absent) is "auto" — the existing, universal behavior.
func parseInvocation(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "manual") {
		return "manual"
	}
	return "auto"
}

// parseRunAs maps frontmatter to a run mode. An unknown value defaults to the
// safe (non-spawning) inline mode; a `context: fork` or a non-empty `agent:`
// field (cross-tool conventions) signals subagent isolation.
func parseRunAs(runAs, context, agent string) RunAs {
	if strings.TrimSpace(runAs) == "subagent" {
		return RunSubagent
	}
	if strings.EqualFold(strings.TrimSpace(context), "fork") {
		return RunSubagent
	}
	if strings.TrimSpace(agent) != "" {
		return RunSubagent
	}
	return RunInline
}

// splitFrontmatter is a thin wrapper kept for internal use; the real parser
// lives in internal/frontmatter.
func splitFrontmatter(s string) (map[string]string, string) {
	return frontmatter.Split(s)
}
