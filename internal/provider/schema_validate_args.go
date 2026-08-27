package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// toolSchemaCache caches compiled tool parameter schemas per process. Tool
// schemas are fixed once a registry registers them, so repeated capability
// pre-validation pays a validate, not a compile.
var toolSchemaCache sync.Map // string (schema JSON) -> *jsonschema.Schema

// ValidateToolArgs validates one tool-call argument instance against the
// tool's declared parameter schema, using the same sandboxed compiler
// settings as ValidateToolSchema (no loader, draft-07 default): registry
// lookups and refs never reach the filesystem or network. It is the
// instance-level counterpart to that schema-shape check, and feeds the
// registry-backed capability pre-validation (tool.ArgsValidator).
func ValidateToolArgs(schema, args json.RawMessage) error {
	compiled, err := compiledToolSchema(schema)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	var instance any
	if err := decoder.Decode(&instance); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid arguments: multiple values")
		}
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if err := compiled.Validate(instance); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("invalid arguments: %s", ve.Error())
		}
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func compiledToolSchema(schema json.RawMessage) (*jsonschema.Schema, error) {
	key := string(schema)
	if v, ok := toolSchemaCache.Load(key); ok {
		return v.(*jsonschema.Schema), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(schema))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("invalid tool schema: multiple values")
		}
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(nil)
	compiler.DefaultDraft(jsonschema.Draft7)
	if err := compiler.AddResource(toolSchemaResource, doc); err != nil {
		return nil, fmt.Errorf("load tool schema: %w", err)
	}
	compiled, err := compiler.Compile(toolSchemaResource)
	if err != nil {
		return nil, fmt.Errorf("compile tool schema: %w", err)
	}
	toolSchemaCache.Store(key, compiled)
	return compiled, nil
}
