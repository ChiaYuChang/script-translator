package llm

import (
	"encoding/json/v2"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// MustSchema builds a *jsonschema.Schema from T using jsonschema.For and
// enforces non-null slices.
func MustSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("llm: build schema: %v", err))
	}
	// jsonschema.For infers Go slices as ["null", "array"]; OpenAI Strict
	// Structured Outputs should require non-null arrays in our schemas.
	DisallowNullArrays(schema)
	return schema
}

// ToSchemaMap converts a *jsonschema.Schema into map[string]any required by
// the OpenAI Responses API parameter struct.
func ToSchemaMap(s *jsonschema.Schema) (map[string]any, error) {
	if s == nil {
		return nil, fmt.Errorf("llm: schema is nil")
	}
	raw, err := s.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("llm: marshal schema: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("llm: unmarshal schema map: %w", err)
	}
	return out, nil
}

// ValidateJSON parses rawJSON and validates it against resolved.
func ValidateJSON(resolved *jsonschema.Resolved, rawJSON string) error {
	var instance any
	if err := json.Unmarshal([]byte(rawJSON), &instance); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	if err := resolved.Validate(instance); err != nil {
		return fmt.Errorf("validate schema: %w", err)
	}
	return nil
}

// DecodeJSON validates rawJSON using resolved.Validate and unmarshals it into T.
func DecodeJSON[T any](resolved *jsonschema.Resolved, rawJSON string) (T, error) {
	var zero T
	if err := ValidateJSON(resolved, rawJSON); err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal([]byte(rawJSON), &out); err != nil {
		return zero, fmt.Errorf("decode JSON: %w", err)
	}
	return out, nil
}

func DisallowNullArrays(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if len(s.Types) == 2 &&
		((s.Types[0] == "null" && s.Types[1] == "array") ||
			(s.Types[0] == "array" && s.Types[1] == "null")) {
		s.Types = nil
		s.Type = "array"
	}
	DisallowNullArrays(s.Items)
	for _, prop := range s.Properties {
		DisallowNullArrays(prop)
	}
}
