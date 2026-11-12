package ast

// Adapter is the language boundary of the structural index.
//
// There is exactly one adapter in the MVP: the Go backend. The interface exists
// so the index owns a project-structure contract rather than a Go-specific
// parser call, without introducing a second parser or a second index. A future
// language is registered here and mapped into the same canonical facts; it does
// not get its own graph.
type Adapter interface {
	// Name is the stable adapter identity used in diagnostics.
	Name() string
	// Extensions lists the file suffixes the adapter claims.
	Extensions() []string
	// Supported reports whether a path belongs to this adapter.
	Supported(path string) bool
	// ParseFile performs one parser pass and returns the canonical file facts.
	ParseFile(path string, opts ParseOptions) (FileFacts, error)
}

// ParseOptions controls one adapter parse.
//
// It is an alias of Options so existing Search callers keep their API while the
// adapter boundary has a name that describes its role.
type ParseOptions = Options

// FileFacts is the canonical output of one parser pass.
//
// Symbols, imports, tests, entrypoints, and raw relationships come from the
// same AST. The raw relationship fields are unexported because resolution is an
// index concern: a caller receives resolved coordinates, never syntactic
// fragments that could be mistaken for proven edges.
type FileFacts struct {
	// Language is the adapter name, "go" for the built-in backend.
	Language string
	// Package is the language-level package clause.
	Package string
	// Symbols are the declarations selected by ParseOptions.Kinds.
	Symbols []Match
	// Imports are every import declaration in the file.
	Imports []Import
	// Tests are Go test declarations (Test*, Benchmark*, Fuzz*, Example*,
	// TestMain) in a _test.go file.
	Tests []TestDecl
	// Entrypoints are non-test main functions in package main.
	Entrypoints []Match

	references []rawReference
	calls      []rawCall
}

// Language returns the adapter identity used in FileInfo.
func (GoBackend) Name() string { return "go" }

// ParseFile implements Adapter. It is the single Go parser pass; Parse and
// Search are compatibility projections of this method.
func (b GoBackend) ParseFile(path string, opts ParseOptions) (FileFacts, error) {
	structure, err := b.parse(path, opts)
	if err != nil {
		return FileFacts{}, err
	}
	return structure, nil
}

// var _ Adapter = GoBackend{} keeps the Go backend honest at compile time.
var _ Adapter = GoBackend{}
