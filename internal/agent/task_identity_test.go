package agent

import "testing"

func TestTaskEffectiveProfileFillsSubagentDefaults(t *testing.T) {
	task := &TaskTool{subagentModel: "flash", subagentEffort: "high"}
	model, effort := task.effectiveProfile("", "")
	if model != "flash" || effort != "high" {
		t.Fatalf("defaults = (%q, %q), want (flash, high)", model, effort)
	}
	model, effort = task.effectiveProfile(" pro ", " medium ")
	if model != "pro" || effort != "medium" {
		t.Fatalf("explicit = (%q, %q), want (pro, medium)", model, effort)
	}
	model, effort = task.effectiveProfile("", "low")
	if model != "flash" || effort != "low" {
		t.Fatalf("partial = (%q, %q), want (flash, low)", model, effort)
	}
}

func TestTaskEffectiveIdentityFallsBackToBase(t *testing.T) {
	task := &TaskTool{baseModel: "deepseek/deepseek-v4-pro", baseEffort: "medium"}
	model, effort := task.effectiveIdentity("", "")
	if model != "deepseek/deepseek-v4-pro" || effort != "medium" {
		t.Fatalf("inherited = (%q, %q), want (deepseek/deepseek-v4-pro, medium)", model, effort)
	}
	model, effort = task.effectiveIdentity(" flash ", " high ")
	if model != "flash" || effort != "high" {
		t.Fatalf("explicit = (%q, %q), want (flash, high)", model, effort)
	}
}

func TestTaskEffectiveIdentityUsesResolver(t *testing.T) {
	task := (&TaskTool{}).WithTranscriptIdentityResolver(
		func(modelRef, effort string) (string, string) {
			if modelRef == "flash" {
				return "deepseek/deepseek-v4-flash", "high"
			}
			return "deepseek/deepseek-v4-pro", "medium"
		},
	)
	model, effort := task.effectiveIdentity("flash", "low")
	if model != "deepseek/deepseek-v4-flash" || effort != "high" {
		t.Fatalf("resolved = (%q, %q), want (deepseek/deepseek-v4-flash, high)", model, effort)
	}
}

func TestTaskEffectiveModelAndEffortTrim(t *testing.T) {
	task := &TaskTool{baseModel: " deepseek/pro ", baseEffort: " max "}
	if got := task.effectiveModelIdentity(""); got != "deepseek/pro" {
		t.Fatalf("model fallback = %q, want deepseek/pro", got)
	}
	if got := task.effectiveModelIdentity("x"); got != "x" {
		t.Fatalf("model explicit = %q, want x", got)
	}
	if got := task.effectiveEffortIdentity(""); got != "max" {
		t.Fatalf("effort fallback = %q, want max", got)
	}
	if got := task.effectiveEffortIdentity("low"); got != "low" {
		t.Fatalf("effort explicit = %q, want low", got)
	}
}
