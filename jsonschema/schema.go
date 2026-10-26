// Package jsonschema validates decoded JSON values against the subset of JSON
// Schema that tool parameter schemas use.
//
// It is intentionally dependency-free: tool schemas are small, authored
// in-repo, and rendered to the model, so a compact validator keeps the
// dependency graph and the failure modes under control.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Error is one validation failure. Path is a JSON Pointer into the value.
type Error struct {
	// Path is a JSON Pointer to the offending value, "" for the root.
	Path string
	// Message describes the failure without echoing the value.
	Message string
}

func (e Error) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// ValidationError aggregates every failure found in one pass.
//
// Collecting all errors rather than failing fast matters because the message
// is shown back to the model, which can fix several problems at once.
type ValidationError struct {
	Errors []Error
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "invalid value"
	}
	parts := make([]string, 0, len(e.Errors))
	for _, err := range e.Errors {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

// Schema is a compiled JSON Schema.
//
// The zero value accepts any JSON value, which matches the "no schema
// declared" case for tools that take no parameters.
type Schema struct {
	// raw is the original schema bytes, retained for diagnostics.
	raw []byte

	typ string
	// properties maps property name to subschema.
	properties map[string]*Schema
	// required lists the property names that must be present.
	required []string
	// items is the element schema when typ is "array".
	items *Schema
	// enum holds the permitted literal values.
	enum []any
	// consts holds the single permitted value when set.
	consts    *any
	anyOf     []*Schema
	oneOf     []*Schema
	allOf     []*Schema
	not       *Schema
	hasNot    bool
	minimum   *float64
	maximum   *float64
	exclMin   *float64
	exclMax   *float64
	minLength *int
	maxLength *int
	minItems  *int
	maxItems  *int
	// pattern is the regular expression a string must match.
	pattern string
	// additionalProperties is nil when unspecified, which permits anything.
	additionalProperties *bool
	// additionalSchema validates properties not covered by properties.
	additionalSchema *Schema
	// format is an annotation only; no format is asserted here.
	format string
	// nullable permits a JSON null alongside the declared type.
	nullable bool
}

// Compile parses and validates a schema document.
//
// An empty or nil document yields a schema that accepts any value.
func Compile(raw []byte) (*Schema, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return &Schema{raw: raw}, nil
	}
	var wire wireSchema
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("jsonschema: decode schema: %w", err)
	}
	return compileWire(&wire, raw)
}

// MustCompile is Compile but panics on a malformed schema.
//
// It is intended for package-level schema literals, where a compile failure is
// a programming error.
func MustCompile(raw []byte) *Schema {
	schema, err := Compile(raw)
	if err != nil {
		panic(err)
	}
	return schema
}

// CompileMap compiles a schema expressed as a decoded map, which is how most
// in-repo tool schemas are authored.
func CompileMap(m map[string]any) (*Schema, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: encode schema: %w", err)
	}
	return Compile(raw)
}

// wireSchema mirrors the JSON Schema keywords this validator supports.
type wireSchema struct {
	Type                 any            `json:"type"`
	Properties           map[string]any `json:"properties"`
	Required             []string       `json:"required"`
	Items                any            `json:"items"`
	Enum                 []any          `json:"enum"`
	Const                any            `json:"const"`
	AnyOf                []any          `json:"anyOf"`
	OneOf                []any          `json:"oneOf"`
	AllOf                []any          `json:"allOf"`
	Not                  any            `json:"not"`
	Minimum              *float64       `json:"minimum"`
	Maximum              *float64       `json:"maximum"`
	ExclusiveMinimum     *float64       `json:"exclusiveMinimum"`
	ExclusiveMaximum     *float64       `json:"exclusiveMaximum"`
	MinLength            *int           `json:"minLength"`
	MaxLength            *int           `json:"maxLength"`
	MinItems             *int           `json:"minItems"`
	MaxItems             *int           `json:"maxItems"`
	Pattern              string         `json:"pattern"`
	AdditionalProperties any            `json:"additionalProperties"`
	Format               string         `json:"format"`
	Nullable             bool           `json:"nullable"`
}

func compileWire(wire *wireSchema, raw []byte) (*Schema, error) {
	schema := &Schema{raw: raw}

	switch t := wire.Type.(type) {
	case string:
		schema.typ = t
	case []any:
		// A union of types: the first entry becomes the primary type and any
		// "null" entry marks the schema nullable.
		for _, entry := range t {
			name, ok := entry.(string)
			if !ok {
				continue
			}
			if name == "null" {
				schema.nullable = true
				continue
			}
			if schema.typ == "" {
				schema.typ = name
			}
		}
	}

	for name, raw := range wire.Properties {
		child, err := compileChild(raw)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: property %q: %w", name, err)
		}
		if schema.properties == nil {
			schema.properties = map[string]*Schema{}
		}
		schema.properties[name] = child
	}
	schema.required = wire.Required

	if wire.Items != nil {
		child, err := compileChild(wire.Items)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: items: %w", err)
		}
		schema.items = child
	}

	schema.enum = wire.Enum
	if wire.Const != nil {
		value := wire.Const
		schema.consts = &value
	}

	var err error
	if schema.anyOf, err = compileChildren(wire.AnyOf, "anyOf"); err != nil {
		return nil, err
	}
	if schema.oneOf, err = compileChildren(wire.OneOf, "oneOf"); err != nil {
		return nil, err
	}
	if schema.allOf, err = compileChildren(wire.AllOf, "allOf"); err != nil {
		return nil, err
	}
	if wire.Not != nil {
		not, err := compileChild(wire.Not)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: not: %w", err)
		}
		schema.not = not
		schema.hasNot = true
	}

	schema.minimum = wire.Minimum
	schema.maximum = wire.Maximum
	schema.exclMin = wire.ExclusiveMinimum
	schema.exclMax = wire.ExclusiveMaximum
	schema.minLength = wire.MinLength
	schema.maxLength = wire.MaxLength
	schema.minItems = wire.MinItems
	schema.maxItems = wire.MaxItems
	schema.pattern = wire.Pattern
	schema.format = wire.Format
	schema.nullable = schema.nullable || wire.Nullable

	if wire.AdditionalProperties != nil {
		switch ap := wire.AdditionalProperties.(type) {
		case bool:
			schema.additionalProperties = &ap
		case map[string]any:
			child, err := compileChild(ap)
			if err != nil {
				return nil, fmt.Errorf("jsonschema: additionalProperties: %w", err)
			}
			schema.additionalSchema = child
			permitted := true
			schema.additionalProperties = &permitted
		}
	}
	return schema, nil
}

func compileChild(raw any) (*Schema, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return Compile(encoded)
}

func compileChildren(list []any, keyword string) ([]*Schema, error) {
	out := make([]*Schema, 0, len(list))
	for i, raw := range list {
		child, err := compileChild(raw)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: %s[%d]: %w", keyword, i, err)
		}
		out = append(out, child)
	}
	return out, nil
}

// RawJSON returns the original schema bytes, for rendering onto the wire.
//
// The bytes are the schema as compiled, so a consumer that needs to embed the
// schema in a provider request does not have to re-encode it.
func (s *Schema) RawJSON() json.RawMessage {
	if s == nil {
		return nil
	}
	return json.RawMessage(s.raw)
}

// Validate reports every way value violates the schema.
//
// A nil return means the value is valid. The returned error is always a
// *ValidationError.
func (s *Schema) Validate(value any) error {
	if s == nil {
		return nil
	}
	var errs []Error
	s.validate(value, "", &errs)
	if len(errs) == 0 {
		return nil
	}
	// A stable order keeps error messages reproducible across runs.
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].Path != errs[j].Path {
			return errs[i].Path < errs[j].Path
		}
		return errs[i].Message < errs[j].Message
	})
	return &ValidationError{Errors: errs}
}

// ValidateJSON decodes raw JSON and validates the result.
func (s *Schema) ValidateJSON(raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return &ValidationError{Errors: []Error{{Message: fmt.Sprintf("invalid JSON: %v", err)}}}
	}
	return s.Validate(value)
}

// Accepts reports whether value satisfies the schema.
func (s *Schema) Accepts(value any) bool { return s.Validate(value) == nil }

func (s *Schema) validate(value any, path string, errs *[]Error) {
	if value == nil && s.nullable {
		return
	}

	for _, sub := range s.allOf {
		sub.validate(value, path, errs)
	}
	if s.hasNot && s.not.Validate(value) == nil {
		*errs = append(*errs, Error{Path: path, Message: "must not match the 'not' schema"})
	}
	if len(s.anyOf) > 0 && !anyAccepts(s.anyOf, value) {
		// Preserve branch-level reasons. "must match anyOf" alone tells the
		// caller nothing about which argument shape was expected.
		branches := make([]string, 0, len(s.anyOf))
		for i, sub := range s.anyOf {
			var branchErrs []Error
			sub.validate(value, path, &branchErrs)
			if len(branchErrs) == 0 {
				continue
			}
			branches = append(branches, fmt.Sprintf("branch %d: %s", i+1, branchErrs[0].Message))
		}
		message := "must match at least one of the 'anyOf' schemas"
		if len(branches) > 0 {
			message += " (" + strings.Join(branches, "; ") + ")"
		}
		*errs = append(*errs, Error{Path: path, Message: message})
	}
	if len(s.oneOf) > 0 {
		matches := 0
		for _, sub := range s.oneOf {
			if sub.Validate(value) == nil {
				matches++
			}
		}
		if matches != 1 {
			*errs = append(*errs, Error{
				Path:    path,
				Message: fmt.Sprintf("must match exactly one of the 'oneOf' schemas (matched %d)", matches),
			})
		}
	}

	if s.typ != "" && !typeMatches(s.typ, value) {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must be of type %s", s.typ)})
		// A wrong type makes every further keyword meaningless.
		return
	}
	if s.consts != nil && !jsonEqual(value, *s.consts) {
		*errs = append(*errs, Error{Path: path, Message: "must equal the const value"})
	}
	if len(s.enum) > 0 && !enumAccepts(s.enum, value) {
		*errs = append(*errs, Error{Path: path, Message: "must be one of the permitted values"})
	}

	switch v := value.(type) {
	case string:
		s.validateString(v, path, errs)
	case float64:
		s.validateNumber(v, path, errs)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			s.validateNumber(f, path, errs)
		}
	case []any:
		s.validateArray(v, path, errs)
	case map[string]any:
		s.validateObject(v, path, errs)
	}
	if s.pattern != "" {
		if str, ok := value.(string); ok {
			matched, err := matchPattern(s.pattern, str)
			if err != nil {
				*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("pattern %q is not a valid regular expression", s.pattern)})
			} else if !matched {
				*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must match pattern %s", s.pattern)})
			}
		}
	}
}

func (s *Schema) validateString(v string, path string, errs *[]Error) {
	// Rune count, not byte count, so a multi-byte character is not over-counted.
	length := len([]rune(v))
	if s.minLength != nil && length < *s.minLength {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must be at least %d characters", *s.minLength)})
	}
	if s.maxLength != nil && length > *s.maxLength {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must be at most %d characters", *s.maxLength)})
	}
}

func (s *Schema) validateNumber(v float64, path string, errs *[]Error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		*errs = append(*errs, Error{Path: path, Message: "must be a finite number"})
		return
	}
	if s.minimum != nil && v < *s.minimum {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must be >= %v", *s.minimum)})
	}
	if s.maximum != nil && v > *s.maximum {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must be <= %v", *s.maximum)})
	}
	if s.exclMin != nil && v <= *s.exclMin {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must be > %v", *s.exclMin)})
	}
	if s.exclMax != nil && v >= *s.exclMax {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must be < %v", *s.exclMax)})
	}
}

func (s *Schema) validateArray(v []any, path string, errs *[]Error) {
	if s.minItems != nil && len(v) < *s.minItems {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must have at least %d items", *s.minItems)})
	}
	if s.maxItems != nil && len(v) > *s.maxItems {
		*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("must have at most %d items", *s.maxItems)})
	}
	for i, item := range v {
		if s.items == nil {
			break
		}
		s.items.validate(item, fmt.Sprintf("%s/%d", path, i), errs)
	}
}

func (s *Schema) validateObject(v map[string]any, path string, errs *[]Error) {
	for _, name := range s.required {
		if _, present := v[name]; !present {
			*errs = append(*errs, Error{Path: path, Message: fmt.Sprintf("missing required property %q", name)})
		}
	}

	// Deterministic iteration keeps error ordering stable.
	names := make([]string, 0, len(v))
	for name := range v {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		childPath := path + "/" + escapePointerToken(name)
		if property, ok := s.properties[name]; ok {
			property.validate(v[name], childPath, errs)
			continue
		}
		if s.additionalProperties != nil && !*s.additionalProperties {
			*errs = append(*errs, Error{Path: childPath, Message: "additional properties are not allowed"})
			continue
		}
		if s.additionalSchema != nil {
			s.additionalSchema.validate(v[name], childPath, errs)
		}
	}
}

func escapePointerToken(token string) string {
	// RFC 6901 escaping.
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}

func typeMatches(typ string, value any) bool {
	switch typ {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "integer":
		switch v := value.(type) {
		case float64:
			return v == math.Trunc(v)
		case json.Number:
			_, err := v.Int64()
			return err == nil
		case int:
			return true
		}
		return false
	case "number":
		switch value.(type) {
		case float64, json.Number, int:
			return true
		}
		return false
	default:
		// An unknown type keyword is treated as permissive rather than
		// rejecting every value.
		return true
	}
}

func anyAccepts(subs []*Schema, value any) bool {
	for _, sub := range subs {
		if sub.Validate(value) == nil {
			return true
		}
	}
	return false
}

func enumAccepts(permitted []any, value any) bool {
	for _, candidate := range permitted {
		if jsonEqual(candidate, value) {
			return true
		}
	}
	return false
}

// jsonEqual compares two decoded JSON values structurally.
func jsonEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for key, value := range av {
			other, present := bv[key]
			if !present || !jsonEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
