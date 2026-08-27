package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

const taskFamilySchema = `{
"type":"object",
"properties":{
  "prompt":{"type":"string"},
  "max_steps":{"type":"integer","minimum":1},
  "tasks":{"type":"array","items":{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"]}}
},
"required":["prompt"]
}`

// TestValidateToolArgsValid pins the accept side: well-formed instances pass,
// including fields the schema does not declare (additionalProperties defaults
// to true, so model-generated extra fields must not be rejected).
func TestValidateToolArgsValid(t *testing.T) {
	cases := []string{
		`{"prompt":"review"}`,
		`{"prompt":"review","max_steps":3}`,
		`{"prompt":"review","unexpected_field":42}`,
	}
	for _, in := range cases {
		if err := ValidateToolArgs(jsonRaw(t, taskFamilySchema), jsonRaw(t, in)); err != nil {
			t.Errorf("args %s must validate: %v", in, err)
		}
	}
}

// TestValidateToolArgsRejects pins the reject side with the schema's own
// wording: missing required fields, wrong types, and nested item violations
// are all reported at the offending instance path.
func TestValidateToolArgsRejects(t *testing.T) {
	cases := []struct {
		name, args, want string
	}{
		{"missing prompt", `{"description":"x"}`, "missing property 'prompt'"},
		{"wrong prompt type", `{"prompt":42}`, "/prompt"},
		{"nested missing prompt", `{"prompt":"p","tasks":[{"description":"x"}]}`, "missing property 'prompt'"},
		{"nested wrong type", `{"prompt":"p","tasks":[{"prompt":7}]}`, "/tasks/0/prompt"},
		{"minimum violation", `{"prompt":"p","max_steps":0}`, "/max_steps"},
		{"non-object instance", `[1,2]`, "object"},
		{"scalar instance", `"just a string"`, "object"},
	}
	for _, c := range cases {
		err := ValidateToolArgs(jsonRaw(t, taskFamilySchema), jsonRaw(t, c.args))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.want, err)
		}
	}
}

// TestValidateToolArgsMalformedInstance pins that broken argument JSON is a
// validation error, not a panic or silent accept.
func TestValidateToolArgsMalformedInstance(t *testing.T) {
	for _, in := range []string{`{`, `not json`, `{"prompt":"p"} trailing`} {
		if err := ValidateToolArgs(jsonRaw(t, taskFamilySchema), jsonRaw(t, in)); err == nil {
			t.Errorf("args %q must fail", in)
		}
	}
}

// TestValidateToolArgsCachedRepeat pins the compile cache: the same schema
// validates repeatedly and identical failures stay identical.
func TestValidateToolArgsCachedRepeat(t *testing.T) {
	schema := jsonRaw(t, taskFamilySchema)
	for i := 0; i < 3; i++ {
		if err := ValidateToolArgs(schema, jsonRaw(t, `{"prompt":"ok"}`)); err != nil {
			t.Fatalf("repeat %d: valid args must pass: %v", i, err)
		}
		if err := ValidateToolArgs(schema, jsonRaw(t, `{}`)); err == nil {
			t.Fatalf("repeat %d: missing prompt must fail", i)
		}
	}
}

// TestValidateToolArgsMalformedSchema pins the loud failure for a schema that
// cannot compile: a developer error must never be silently accepted.
func TestValidateToolArgsMalformedSchema(t *testing.T) {
	if err := ValidateToolArgs(jsonRaw(t, `{"type":"object","properties":`), jsonRaw(t, `{}`)); err == nil {
		t.Fatal("a malformed schema must fail loudly")
	}
}

func jsonRaw(t *testing.T, s string) json.RawMessage {
	t.Helper()
	return json.RawMessage(s)
}
