package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"reasonix/internal/provider"
)

// ValidateArgs implements tool.ArgsValidator: prompt missing/blank keeps
// Execute's exact error, the declared schema owns every other constraint,
// and it runs before permission, hooks, or Execute.
func (t *TaskTool) ValidateArgs(_ context.Context, args json.RawMessage) error {
	return validateTaskPrompt(args, t.Schema())
}

// ValidateArgs implements tool.ArgsValidator for read_only_task: same prompt
// pre-validation, read-only schema.
func (r *ReadOnlyTaskTool) ValidateArgs(_ context.Context, args json.RawMessage) error {
	return validateTaskPrompt(args, r.Schema())
}

// ValidateArgs implements tool.ArgsValidator for parallel_tasks: every item's
// prompt must be present and non-blank, then the declared schema owns the
// nested structure (counts, item types, constraints).
func (p *ParallelTasksTool) ValidateArgs(_ context.Context, args json.RawMessage) error {
	return validateNestedTaskPrompts(args, p.Schema())
}

// ValidateArgs implements tool.ArgsValidator for fleet: same item-array
// pre-validation as parallel_tasks against the fleet schema.
func (t *FleetTool) ValidateArgs(_ context.Context, args json.RawMessage) error {
	return validateNestedTaskPrompts(args, t.Schema())
}

// validateTaskPrompt is the registry-backed schema pre-validation for the
// single-prompt delegation tools (task, read_only_task). A missing or blank
// prompt keeps Execute's exact error so the earlier failure reads the same;
// every other constraint — including prompt type errors — falls to the
// declared schema, which extra fields can never bypass.
func validateTaskPrompt(args json.RawMessage, schema json.RawMessage) error {
	var p struct {
		Prompt *string `json:"prompt"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return provider.ValidateToolArgs(schema, args)
	}
	if p.Prompt == nil || strings.TrimSpace(*p.Prompt) == "" {
		return errors.New("prompt is required")
	}
	return provider.ValidateToolArgs(schema, args)
}

// validateNestedTaskPrompts is the same pre-validation for the item-array
// delegation tools (parallel_tasks, fleet). Each item's prompt follows
// Execute's blank-counts-as-missing rule (wording mirrors
// validateParallelTaskItems), then the schema owns everything else, so a
// nested type error or constraint violation is reported before permission.
func validateNestedTaskPrompts(args json.RawMessage, schema json.RawMessage) error {
	var p struct {
		Tasks []struct {
			Prompt *string `json:"prompt"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return provider.ValidateToolArgs(schema, args)
	}
	if len(p.Tasks) == 0 {
		return errors.New("at least one task is required")
	}
	for i, item := range p.Tasks {
		if item.Prompt == nil || strings.TrimSpace(*item.Prompt) == "" {
			return fmt.Errorf("task %d: prompt is required", i+1)
		}
	}
	return provider.ValidateToolArgs(schema, args)
}
