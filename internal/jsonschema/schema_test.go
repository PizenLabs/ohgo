package jsonschema

import (
	"strings"
	"testing"
)

func TestValidateObjectRequiredAndType(t *testing.T) {
	schema := MustCompile([]byte(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"count": {"type": "integer"},
			"ratio": {"type": "number"},
			"tags": {"type": "array", "items": {"type": "string"}}
		},
		"required": ["path"]
	}`))

	if err := schema.Validate(map[string]any{"path": "a.go"}); err != nil {
		t.Errorf("valid document rejected: %v", err)
	}

	// A missing required property must name the property, so the model can fix
	// the exact field.
	err := schema.Validate(map[string]any{})
	if err == nil {
		t.Fatal("expected a missing required property to fail")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("error = %q, want it to name the missing property", err)
	}
}

func TestValidateReportsEveryErrorInOnePass(t *testing.T) {
	schema := MustCompile([]byte(`{
		"type": "object",
		"properties": {
			"a": {"type": "string"},
			"b": {"type": "integer", "minimum": 1}
		},
		"required": ["a", "b"]
	}`))

	err := schema.Validate(map[string]any{})
	validation, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("error = %T, want *ValidationError", err)
	}
	// Both problems are reported so the model can fix them in one retry.
	if len(validation.Errors) != 2 {
		t.Errorf("errors = %d, want 2: %v", len(validation.Errors), err)
	}
}

func TestValidateRejectsWrongScalarTypes(t *testing.T) {
	schema := MustCompile([]byte(`{"type":"object","properties":{"a":{"type":"string"}}}`))
	if err := schema.Validate(map[string]any{"a": 1.0}); err == nil {
		t.Error("expected a number where a string is required to fail")
	}
}

func TestValidateIntegerRejectsFractions(t *testing.T) {
	schema := MustCompile([]byte(`{"type":"integer"}`))
	if err := schema.Validate(3.0); err != nil {
		t.Errorf("whole float rejected: %v", err)
	}
	if err := schema.Validate(3.5); err == nil {
		t.Error("expected a fractional value to fail an integer check")
	}
}

func TestValidateArrayItemTypeAndBounds(t *testing.T) {
	schema := MustCompile([]byte(`{
		"type": "array",
		"items": {"type": "string"},
		"minItems": 1,
		"maxItems": 2
	}`))
	if err := schema.Validate([]any{"a"}); err != nil {
		t.Errorf("valid array rejected: %v", err)
	}
	if err := schema.Validate([]any{}); err == nil {
		t.Error("expected an empty array to fail minItems")
	}
	if err := schema.Validate([]any{"a", "b", "c"}); err == nil {
		t.Error("expected three items to fail maxItems")
	}
	if err := schema.Validate([]any{1.0}); err == nil {
		t.Error("expected a non-string item to fail the item schema")
	}
}

func TestValidateAdditionalPropertiesFalse(t *testing.T) {
	schema := MustCompile([]byte(`{
		"type": "object",
		"properties": {"a": {"type": "string"}},
		"additionalProperties": false
	}`))
	if err := schema.Validate(map[string]any{"a": "x"}); err != nil {
		t.Errorf("valid document rejected: %v", err)
	}
	err := schema.Validate(map[string]any{"a": "x", "surprise": true})
	if err == nil {
		t.Fatal("expected an undeclared property to fail")
	}
	if !strings.Contains(err.Error(), "surprise") {
		t.Errorf("error = %q, want it to name the offending property", err)
	}
}

func TestValidateEnumAndConst(t *testing.T) {
	enum := MustCompile([]byte(`{"enum": ["low", "medium", "high"]}`))
	if err := enum.Validate("medium"); err != nil {
		t.Errorf("permitted value rejected: %v", err)
	}
	if err := enum.Validate("extreme"); err == nil {
		t.Error("expected a value outside the enum to fail")
	}

	const_ := MustCompile([]byte(`{"const": "v1"}`))
	if err := const_.Validate("v1"); err != nil {
		t.Errorf("matching const rejected: %v", err)
	}
	if err := const_.Validate("v2"); err == nil {
		t.Error("expected a mismatched const to fail")
	}
}

func TestValidateStringLengthCountsRunesNotBytes(t *testing.T) {
	schema := MustCompile([]byte(`{"type":"string","minLength":3}`))
	// Three multi-byte characters: a byte count would wrongly reject this.
	if err := schema.Validate("日本語"); err != nil {
		t.Errorf("valid multi-byte string rejected: %v", err)
	}
	if err := schema.Validate("ab"); err == nil {
		t.Error("expected a short string to fail minLength")
	}
}

func TestValidateUnionTypeWithNull(t *testing.T) {
	schema := MustCompile([]byte(`{"type": ["string", "null"]}`))
	if err := schema.Validate(nil); err != nil {
		t.Errorf("null rejected for a nullable union: %v", err)
	}
	if err := schema.Validate("x"); err != nil {
		t.Errorf("string rejected for a nullable union: %v", err)
	}
	if err := schema.Validate(1.0); err == nil {
		t.Error("expected a number to fail a string|null union")
	}
}

func TestValidateAnyOfAndOneOf(t *testing.T) {
	anyOf := MustCompile([]byte(`{"anyOf":[{"type":"string"},{"type":"integer"}]}`))
	if err := anyOf.Validate("x"); err != nil {
		t.Errorf("string rejected by anyOf: %v", err)
	}
	if err := anyOf.Validate(1.0); err != nil {
		t.Errorf("integer rejected by anyOf: %v", err)
	}
	if err := anyOf.Validate(true); err == nil {
		t.Error("expected a boolean to fail anyOf")
	}

	oneOf := MustCompile([]byte(`{"oneOf":[{"type":"string"},{"type":"integer"}]}`))
	if err := oneOf.Validate("x"); err != nil {
		t.Errorf("string rejected by oneOf: %v", err)
	}
}

func TestAnyOfReportsBranchReasons(t *testing.T) {
	schema := MustCompile([]byte(`{"anyOf":[
		{"type":"object","required":["tool"]},
		{"type":"object","required":["prompt"]}
	]}`))
	err := schema.Validate(map[string]any{})
	if err == nil {
		t.Fatal("expected an empty object to fail anyOf")
	}
	message := err.Error()
	for _, want := range []string{"branch 1", "branch 2", "tool", "prompt"} {
		if !strings.Contains(message, want) {
			t.Errorf("anyOf error %q is missing %q", message, want)
		}
	}
}

func TestValidateNestedObjectPaths(t *testing.T) {
	schema := MustCompile([]byte(`{
		"type":"object",
		"properties":{
			"outer":{"type":"object","properties":{"inner":{"type":"string"}},"required":["inner"]}
		}
	}`))
	err := schema.Validate(map[string]any{"outer": map[string]any{}})
	if err == nil {
		t.Fatal("expected a missing nested required property to fail")
	}
	// The path must point at the object that is actually wrong, so a UI can
	// highlight the right field.
	if !strings.Contains(err.Error(), "outer/inner") && !strings.Contains(err.Error(), "/outer") {
		t.Errorf("error = %q, want a nested JSON pointer path", err)
	}
}

func TestValidateJSONRejectsMalformedInput(t *testing.T) {
	schema := MustCompile([]byte(`{"type":"object"}`))
	err := schema.ValidateJSON([]byte(`{"a":`))
	if err == nil {
		t.Fatal("expected malformed JSON to fail")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error = %q, want an invalid-JSON message", err)
	}
}

func TestEmptySchemaAcceptsAnything(t *testing.T) {
	schema := MustCompile(nil)
	if err := schema.Validate(map[string]any{"anything": true}); err != nil {
		t.Errorf("empty schema rejected a document: %v", err)
	}
	if err := schema.Validate("a string"); err != nil {
		t.Errorf("empty schema rejected a scalar: %v", err)
	}
}

func TestCompileRejectsMalformedSchema(t *testing.T) {
	if _, err := Compile([]byte(`{not json`)); err == nil {
		t.Fatal("expected a malformed schema to fail compilation")
	}
}
