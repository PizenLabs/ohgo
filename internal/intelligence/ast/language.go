package ast

import (
	"path"
	"sort"
	"strings"
)

// Language identifies one syntax adapter family.
//
// It is a closed vocabulary. An extension that no adapter claims is
// unsupported, not an implicit source file: the runtime must never turn an
// unknown language into a clean absence claim.
type Language string

// Supported language identities. Go is native; the others are optional
// tree-sitter adapters compiled only when cgo is available.
const (
	LanguageGo         Language = "go"
	LanguageTypeScript Language = "typescript"
	LanguageTSX        Language = "tsx"
	LanguageJavaScript Language = "javascript"
	LanguageJSX        Language = "jsx"
	LanguageJava       Language = "java"
	LanguageCSharp     Language = "csharp"
	LanguageRust       Language = "rust"
	LanguageKotlin     Language = "kotlin"
	LanguageRuby       Language = "ruby"
	LanguageC          Language = "c"
	LanguageCpp        Language = "cpp"
)

// Activation is the structural depth one query requested.
//
// The Control Plane chooses the outer depth; the tool/query layer may request
// less, never more. Semantic is a reserved escalation and is unsupported in
// this implementation; it must fail explicitly rather than silently degrade to
// syntax.
type Activation string

// Structural activation levels.
const (
	ActivationNone     Activation = "NONE"
	ActivationOverview Activation = "OVERVIEW"
	ActivationTargeted Activation = "TARGETED"
	ActivationImpact   Activation = "IMPACT"
	ActivationSemantic Activation = "SEMANTIC"
)

// Supported reports whether the runtime can execute this depth.
func (a Activation) Supported() bool {
	switch a {
	case ActivationNone, ActivationOverview, ActivationTargeted, ActivationImpact:
		return true
	default:
		return false
	}
}

// ParsesSource reports whether the depth may parse source files.
func (a Activation) ParsesSource() bool {
	switch a {
	case ActivationTargeted, ActivationImpact:
		return true
	default:
		return false
	}
}

// Resolution is the explicit state of a relationship target.
//
// UNRESOLVED and AMBIGUOUS are disclosures, not edges. Only RESOLVED
// relationships are materialised as graph edges; the others are counted so a
// query can tell the caller what it could not prove.
type Resolution string

// Relationship resolution states.
const (
	ResolutionResolved    Resolution = "RESOLVED"
	ResolutionUnresolved  Resolution = "UNRESOLVED"
	ResolutionAmbiguous   Resolution = "AMBIGUOUS"
	ResolutionUnsupported Resolution = "UNSUPPORTED"
)

// Capabilities declares what one adapter can prove.
//
// The booleans are deliberately narrower than "the grammar can parse it":
// they describe canonical facts the adapter actually emits. A missing
// capability is reported as unsupported, never synthesised.
type Capabilities struct {
	Symbols     bool `json:"symbols"`
	Imports     bool `json:"imports"`
	Exports     bool `json:"exports"`
	References  bool `json:"references"`
	Calls       bool `json:"calls"`
	Inheritance bool `json:"inheritance"`
	Tests       bool `json:"tests"`
	Entrypoints bool `json:"entrypoints"`
	Semantic    bool `json:"semantic"`
}

// Parser describes the substrate that produced a file's facts.
type Parser struct {
	// Name is the adapter identity, e.g. "go" or "tree-sitter-typescript".
	Name string `json:"name"`
	// Version is the pinned grammar/parser version, or "stdlib" for Go.
	Version string `json:"version,omitempty"`
}

// ParseIssue is one honest parse limitation. It is bounded and never contains
// source text.
type ParseIssue struct {
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// LanguageCoverage reports what the snapshot knows about one supported
// language. It separates "files exist" from "files parsed" so a CURRENT
// snapshot can still be structurally partial.
type LanguageCoverage struct {
	Language Language `json:"language"`
	// Supported reports that an adapter is registered in this build.
	Supported bool `json:"supported"`
	// Files counts inventory entries, including unparsed and failed files.
	Files int `json:"files"`
	// Parsed counts files whose facts are in the snapshot.
	Parsed int `json:"parsed"`
	// Failed counts files the adapter could not parse.
	Failed int `json:"failed"`
	// Pending counts files not parsed yet (adaptive overview).
	Pending int `json:"pending,omitempty"`
	// Symbols is the number of canonical declarations.
	Symbols int `json:"symbols,omitempty"`
	// Coverage is COMPLETE, PARTIAL, UNPARSED, or UNSUPPORTED.
	Coverage string `json:"coverage"`
}

// UnsupportedLanguage is one observed extension with no adapter.
type UnsupportedLanguage struct {
	Extension string `json:"extension"`
	Files     int    `json:"files"`
}

// languageByExtension is the deterministic extension registry. It is the only
// place a path suffix becomes a language, so two adapters cannot silently
// claim the same file.
var languageByExtension = map[string]Language{
	".go":   LanguageGo,
	".ts":   LanguageTypeScript,
	".mts":  LanguageTypeScript,
	".cts":  LanguageTypeScript,
	".tsx":  LanguageTSX,
	".js":   LanguageJavaScript,
	".mjs":  LanguageJavaScript,
	".cjs":  LanguageJavaScript,
	".jsx":  LanguageJSX,
	".java": LanguageJava,
	".cs":   LanguageCSharp,
	".rs":   LanguageRust,
	".kt":   LanguageKotlin,
	".kts":  LanguageKotlin,
	".rb":   LanguageRuby,
	".c":    LanguageC,
	".h":    LanguageC,
	".cc":   LanguageCpp,
	".cpp":  LanguageCpp,
	".cxx":  LanguageCpp,
	".hpp":  LanguageCpp,
	".hh":   LanguageCpp,
	".hxx":  LanguageCpp,
}

// LanguageForPath returns the language a path suffix identifies, if any.
//
// The lookup is case-insensitive for the extension, matching filesystem
// practice; the language values themselves stay lower-case.
func LanguageForPath(pathArg string) (Language, bool) {
	language, ok := languageByExtension[strings.ToLower(path.Ext(pathArg))]
	return language, ok
}

// IsSourcePath reports whether an extension belongs to a supported adapter.
func IsSourcePath(pathArg string) bool {
	_, ok := LanguageForPath(pathArg)
	return ok
}

// SupportedLanguages returns the canonical language list in stable order.
func SupportedLanguages() []Language {
	out := make([]Language, 0, len(languageByExtension))
	seen := map[Language]bool{}
	for _, language := range languageByExtension {
		if seen[language] {
			continue
		}
		seen[language] = true
		out = append(out, language)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// IsTestPath applies the adapter's file-level test convention. Declarations
// can still prove tests inside a non-conventional file; this is only the
// cheap inventory signal.
func IsTestPath(pathArg string) bool {
	rel := strings.ToLower(strings.ReplaceAll(pathArg, "\\", "/"))
	base := path.Base(rel)
	language, ok := LanguageForPath(rel)
	if !ok {
		return false
	}
	switch language {
	case LanguageGo:
		return strings.HasSuffix(base, "_test.go")
	case LanguageTypeScript, LanguageTSX, LanguageJavaScript, LanguageJSX:
		return strings.Contains(base, ".test.") ||
			strings.Contains(base, ".spec.") ||
			strings.Contains(rel, "/__tests__/") ||
			strings.HasPrefix(rel, "__tests__/")
	case LanguageJava:
		return strings.HasSuffix(base, "test.java") ||
			strings.HasSuffix(base, "tests.java") ||
			strings.Contains(rel, "/src/test/")
	case LanguageCSharp:
		return strings.HasSuffix(base, "test.cs") ||
			strings.HasSuffix(base, "tests.cs") ||
			strings.Contains(rel, "/tests/") ||
			strings.HasPrefix(rel, "tests/")
	case LanguageRust:
		return strings.Contains(rel, "/tests/") || strings.HasPrefix(rel, "tests/")
	case LanguageKotlin:
		return strings.HasSuffix(base, "test.kt") ||
			strings.HasSuffix(base, "tests.kt") ||
			strings.Contains(rel, "/src/test/")
	case LanguageRuby:
		return strings.HasSuffix(base, "_spec.rb") ||
			(strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".rb")) ||
			strings.Contains(rel, "/spec/") ||
			strings.Contains(rel, "/test/")
	case LanguageC, LanguageCpp:
		return strings.Contains(base, "_test.") ||
			strings.HasPrefix(base, "test_") ||
			strings.Contains(rel, "/tests/") ||
			strings.Contains(rel, "/test/")
	default:
		return false
	}
}

// DisplayLanguage is the model-facing spelling of a language identity.
func DisplayLanguage(language Language) string { return languageDisplayName(language) }

// languageDisplayName is the model-facing spelling.
func languageDisplayName(language Language) string {
	switch language {
	case LanguageGo:
		return "Go"
	case LanguageTypeScript:
		return "TypeScript"
	case LanguageTSX:
		return "TypeScript JSX"
	case LanguageJavaScript:
		return "JavaScript"
	case LanguageJSX:
		return "JavaScript JSX"
	case LanguageJava:
		return "Java"
	case LanguageCSharp:
		return "C#"
	case LanguageRust:
		return "Rust"
	case LanguageKotlin:
		return "Kotlin"
	case LanguageRuby:
		return "Ruby"
	case LanguageC:
		return "C"
	case LanguageCpp:
		return "C++"
	default:
		return string(language)
	}
}
