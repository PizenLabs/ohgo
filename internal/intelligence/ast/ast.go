// Package ast finds code structurally.
//
// It replaces the original `pi-ast` crate, which wraps tree-sitter to power the
// agent's `ast-grep` tool. Backends are pluggable so the default build can stay
// free of cgo while a tree-sitter backend remains available for languages Go's
// standard library cannot parse.
//
// # The no-cgo rule, and why this package has a build tag
//
// The port's standing rule was "pure Go or nothing" for the Rust crates. That
// rule is right for storage (pure-Go SQLite exists) and wrong for parsing: no
// pure-Go tree-sitter exists at comparable quality, so the honest options were
// (a) cgo tree-sitter for everything, (b) pure-Go per-language parsers, or
// (c) a pluggable backend where the default needs no C toolchain.
//
// Option (c) is what this package does. `BackendGo` uses the standard
// library's `go/ast` and needs no cgo at all. The multi-language adapters live
// in files guarded by `//go:build cgo`; the same package compiles without cgo,
// keeps the native Go path, and reports every other supported extension as
// unsupported. No grammar is fetched at runtime and no host shared library is
// required: the grammar sources are pinned in go.mod and compiled into the
// binary.
//
// # Repository snapshot
//
// GoBackend is also the native language adapter for Index, the in-process
// structural snapshot shared by the symbols, graph, refs, and structure tools.
// One Index owns symbol coordinates, file/package facts, imports, references,
// direct calls, tests, entrypoints, language coverage, and the canonical
// relationship projection. It is incremental: a refresh parses only changed
// files and re-resolves relationships from retained syntax. It writes nothing;
// the source tree remains authoritative and <project>/.ohgo/graph.db stays a
// deferred, disposable location.
package ast

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// Kind is the declaration a match describes.
type Kind string

// Declaration kinds.
const (
	// KindFunc is a function declaration. A KindFunc filter also matches
	// methods for backward compatibility; use KindMethod for methods only.
	KindFunc Kind = "func"
	// KindType is a type declaration.
	KindType Kind = "type"
	// KindVar is a package-level variable.
	KindVar Kind = "var"
	// KindConst is a package-level constant.
	KindConst Kind = "const"
	// KindImport is an import declaration.
	KindImport Kind = "import"
	// KindMethod is a method declaration.
	KindMethod Kind = "method"
	// KindPackage is a package declaration.
	KindPackage Kind = "package"
)

// Match is one located declaration.
type Match struct {
	// Kind is the declaration kind.
	Kind Kind
	// Name is the declared identifier, empty for an import.
	Name string
	// QualifiedName is the deterministic qualified coordinate for the symbol.
	QualifiedName string
	// Package is the Go package name the declaration belongs to.
	Package string
	// Path is the file the declaration was found in.
	Path string
	// Line is the 1-based line the declaration starts on.
	Line int
	// Column is the 1-based column the declaration starts on.
	Column int
	// EndLine is the 1-based line it ends on.
	EndLine int
	// Test reports that the declaration came from a _test.go file.
	Test bool
	// Canonical is the language-neutral declaration kind. Kind remains the
	// Go adapter's compatibility projection.
	Canonical SymbolKind
	// NativeKind is the adapter's original syntax kind, e.g.
	// "interface_declaration" or "impl_item". It is data, never a dispatch key.
	NativeKind string
	// Language is the declaration's source language.
	Language Language
	// Adapter is the parser identity that produced the declaration.
	Adapter string
	// Visibility is public, protected, private, internal, or package.
	Visibility string
	// Container is the enclosing class, struct, module, or namespace name.
	Container string
	// Receiver is the method receiver type, when the language has one.
	Receiver string
	// Exported reports a declaration visible outside its package/module.
	Exported bool
	// Doc is the declaration's doc comment, trimmed.
	Doc string
	// Signature is a one-line rendering of the declaration's head.
	Signature string
}

// Options controls a search.
type Options struct {
	// Kinds restricts the search when non-empty.
	Kinds []Kind
	// Paths restricts the search to these files. Empty means every file given
	// to the searcher.
	Paths []string
	// Relationships asks Parse to project syntactic references and direct
	// calls in the same parser pass. It is off by default because Search is a
	// declaration-only surface; the repository index enables it because the
	// relationship queries must not trigger a second parse.
	Relationships bool
}

// Import is one import declaration from a parsed file.
//
// It is relationship data, not a navigable declaration: imports deliberately
// stay out of the symbol taxonomy so a file or package listing never fills with
// import noise. The package graph and the symbol-reference projection are the
// only consumers.
type Import struct {
	// Path is the unquoted import path.
	Path string
	// Alias is the explicit import name. It is empty for a default import,
	// "." for a dot import, and "_" for a blank import.
	Alias string
	// Line is the 1-based line the import declaration starts on.
	Line int
	// Test reports that the declaration came from a test file.
	Test bool
	// Language is the importing file's language.
	Language Language
	// Kind is module, relative, include, require, package, or namespace.
	Kind string
	// Names are the imported symbols, when the syntax names them.
	Names []string
	// Resolution is RESOLVED when the import target exists in the snapshot,
	// UNRESOLVED for an external/unindexed module, and UNSUPPORTED when the
	// adapter cannot resolve this import form.
	Resolution Resolution
	// Resolved is the repository-relative target path or module node name.
	Resolved string
}

// FileStructure is the result of one parse: the package clause, the
// declarations selected by Options, and every import declaration.
//
// Symbols and Imports share one parser pass. That is the reason the graph can
// be derived without becoming a second repository index or doubling the parse
// cost.
type FileStructure struct {
	// Package is the package clause's name.
	Package string
	// Symbols are the declarations that passed the kind filter.
	Symbols []Match
	// Imports are all imports, regardless of the kind filter.
	Imports []Import

	// references and calls are the syntactic relationship projection. They
	// are populated only when Options.Relationships is set and are consumed
	// by Index during the same build. They never leave the package.
	references []rawReference
	calls      []rawCall
}

// Searcher finds declarations. Implementations must be safe for concurrent use
// across distinct files.
type Searcher interface {
	// Supported reports whether a path can be parsed by this backend.
	Supported(path string) bool
	// Search parses one file and returns its declarations.
	Search(path string, opts Options) ([]Match, error)
}

// Registry maps paths to the legacy per-file Searcher backend.
//
// The repository snapshot uses DefaultAdapters/adapterSet for structural
// ownership; Registry remains the small compatibility surface for callers that
// search one file. Both consume the same adapter identities, so this is not a
// second structural registry.
type Registry struct {
	backends []Searcher
}

// New returns a registry with the built-in Go backend.
func New() *Registry {
	return &Registry{backends: []Searcher{GoBackend{}}}
}

// Register adds a backend, consulted after the existing ones.
func (r *Registry) Register(s Searcher) { r.backends = append(r.backends, s) }

// BackendFor returns the backend that can parse a path, or nil.
func (r *Registry) BackendFor(path string) Searcher {
	for _, backend := range r.backends {
		if backend.Supported(path) {
			return backend
		}
	}
	return nil
}

// Search finds declarations in one file.
func (r *Registry) Search(path string, opts Options) ([]Match, error) {
	backend := r.BackendFor(path)
	if backend == nil {
		return nil, fmt.Errorf("ast: no backend for %s (supported: %s)",
			filepath.Base(path), r.SupportedExtensions())
	}
	return backend.Search(path, opts)
}

// SupportedExtensions lists what the registry can parse.
func (r *Registry) SupportedExtensions() string {
	var names []string
	for _, backend := range r.backends {
		if goBackend, ok := backend.(GoBackend); ok {
			names = append(names, goBackend.Extensions()...)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// GoBackend parses Go source with the standard library.
//
// It needs no cgo and no third-party grammar, because Go ships its own parser.
// The trade is that it covers one language, which is exactly why the backend
// exists behind an interface.
type GoBackend struct{}

// Extensions lists the file suffixes this backend claims.
func (GoBackend) Extensions() []string { return []string{".go"} }

// Supported reports whether this backend parses a path.
//
// Test files are included: a test declares functions a caller searches for just
// as often.
func (GoBackend) Supported(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".go")
}

// Search implements Searcher.
func (b GoBackend) Search(path string, opts Options) ([]Match, error) {
	structure, err := b.Parse(path, opts)
	if err != nil {
		return nil, err
	}
	return structure.Symbols, nil
}

// Parse parses one file once and returns its package clause, the declarations
// selected by opts, and every import declaration.
//
// Index uses ParseFile rather than Search because the package graph needs the
// imports from the same pass that produces symbol coordinates. This wrapper
// preserves the historical FileStructure API while ParseFile carries the
// canonical facts (tests, entrypoints, relationship projection).
func (b GoBackend) Parse(path string, opts Options) (FileStructure, error) {
	facts, err := b.parse(path, opts)
	if err != nil {
		return FileStructure{}, err
	}
	return FileStructure{
		Package:    facts.Package,
		Symbols:    facts.Symbols,
		Imports:    facts.Imports,
		references: facts.references,
		calls:      facts.calls,
	}, nil
}

// parse is the one Go parser pass. ParseFile and Parse are projections of this
// method, so no caller can accidentally introduce a second parse.
func (b GoBackend) parse(path string, opts Options) (FileFacts, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return FileFacts{}, fmt.Errorf("ast: parse %s: %w", path, err)
	}

	var matches []Match
	var imports []Import
	testFile := strings.HasSuffix(filepath.Base(path), "_test.go")

	// A package declaration is a first-class symbol: it anchors every other
	// declaration in the file and is what a package-level query resolves to.
	if kindAllowed(opts, KindPackage) {
		pos := fset.Position(file.Package)
		matches = append(matches, Match{
			Kind:      KindPackage,
			Name:      file.Name.Name,
			Package:   file.Name.Name,
			Path:      path,
			Line:      pos.Line,
			Column:    pos.Column,
			EndLine:   fset.Position(file.Name.End()).Line,
			Signature: "package " + file.Name.Name,
		})
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			m := goFuncMatch(fset, path, d)
			if !kindAllowed(opts, m.Kind) {
				continue
			}
			matches = append(matches, m)

		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if kindAllowed(opts, KindType) {
						matches = append(matches, goSpecMatch(fset, path, KindType, s, d))
					}
				case *ast.ValueSpec:
					kind := KindVar
					if d.Tok == token.CONST {
						kind = KindConst
					}
					if !kindAllowed(opts, kind) {
						continue
					}
					for _, name := range s.Names {
						// Skip the blank identifier: it declares nothing usable.
						if name.Name == "_" {
							continue
						}
						matches = append(matches, goNamedMatch(fset, path, kind, name.Name, s, d))
					}
				case *ast.ImportSpec:
					imports = append(imports, goImportRecord(fset, s, testFile))
					if kindAllowed(opts, KindImport) {
						matches = append(matches, goImportMatch(fset, path, s))
					}
				}
			}

		}
	}

	var references []rawReference
	var calls []rawCall
	if opts.Relationships {
		references, calls = collectFileRelationships(fset, file)
	}

	// Populate package and qualified coordinates deterministically. The
	// package clause is the authority for the package name; never fabricate
	// one when it is missing.
	var tests []TestDecl
	var entrypoints []Match
	for i := range matches {
		matches[i].Test = testFile
		matches[i].Language = LanguageGo
		matches[i].Canonical = canonicalKind(matches[i].Kind)
		if matches[i].Kind == KindPackage {
			matches[i].Exported = true
		} else if name := matches[i].Name; name != "" {
			if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
				name = name[idx+1:]
			}
			matches[i].Exported = token.IsExported(name)
			if matches[i].Exported {
				matches[i].Visibility = "public"
			} else {
				matches[i].Visibility = "private"
			}
		}
		if matches[i].Package == "" {
			matches[i].Package = file.Name.Name
		}
		if matches[i].Kind == KindPackage {
			matches[i].QualifiedName = matches[i].Package
			continue
		}
		if matches[i].QualifiedName == "" {
			if matches[i].Package != "" {
				matches[i].QualifiedName = matches[i].Package + "." + matches[i].Name
			} else {
				matches[i].QualifiedName = matches[i].Name
			}
		}
		if matches[i].Kind != KindFunc {
			continue
		}
		if testFile {
			if kind, ok := testKindForName(matches[i].Name); ok {
				tests = append(tests, TestDecl{Symbol: matches[i], Kind: kind, Framework: "go-test"})
			}
			continue
		}
		if matches[i].Name == "main" && file.Name.Name == "main" {
			entrypoints = append(entrypoints, matches[i])
		}
	}
	return FileFacts{
		Language:    LanguageGo,
		Adapter:     "go",
		Parser:      Parser{Name: "go", Version: "stdlib"},
		Package:     file.Name.Name,
		Symbols:     matches,
		Imports:     imports,
		Tests:       tests,
		Entrypoints: entrypoints,
		references:  references,
		calls:       calls,
	}, nil
}

// testKindForName applies the Go testing package's declaration convention.
// A prefix followed by a lowercase letter is a helper, not a test.
func testKindForName(name string) (TestKind, bool) {
	if name == "TestMain" {
		return TestMain, true
	}
	for _, candidate := range []struct {
		prefix string
		kind   TestKind
	}{
		{"Test", TestUnit},
		{"Benchmark", TestBenchmark},
		{"Fuzz", TestFuzz},
		{"Example", TestExample},
	} {
		if !strings.HasPrefix(name, candidate.prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, candidate.prefix)
		if rest == "" {
			return candidate.kind, true
		}
		first := rune(rest[0])
		if first < 'a' || first > 'z' {
			return candidate.kind, true
		}
	}
	return "", false
}

// kindAllowed reports whether a declaration kind passes the filter.
//
// A filter for KindFunc also matches methods, preserving the pre-method
// taxonomy for callers that asked for functions before KindMethod existed.
func kindAllowed(opts Options, kind Kind) bool {
	if len(opts.Kinds) == 0 {
		return true
	}
	for _, want := range opts.Kinds {
		if want == kind {
			return true
		}
		if want == KindFunc && kind == KindMethod {
			return true
		}
	}
	return false
}

func goFuncMatch(fset *token.FileSet, path string, decl *ast.FuncDecl) Match {
	kind := KindFunc
	name := decl.Name.Name
	receiver := ""
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		kind = KindMethod
		// A method is qualified by its receiver, because a bare method name is
		// ambiguous across types.
		receiver = typeName(decl.Recv.List[0].Type)
		if receiver != "" {
			name = receiver + "." + decl.Name.Name
		}
	}

	signature := name + argsString(decl.Type.Params)
	if decl.Type.Results != nil {
		signature += " " + resultsString(decl.Type.Results)
	}

	return Match{
		Kind:      kind,
		Name:      name,
		Container: receiver,
		Receiver:  receiver,
		Path:      path,
		Line:      fset.Position(decl.Pos()).Line,
		Column:    fset.Position(decl.Pos()).Column,
		EndLine:   fset.Position(decl.End()).Line,
		Doc:       trimDoc(decl.Doc),
		Signature: signature,
	}
}

// typeName renders a receiver type, dropping any pointer.
func typeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.ParenExpr:
		return typeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic instantiation: Foo[T]
		return typeName(t.X)
	case *ast.IndexListExpr:
		return typeName(t.X)
	default:
		return ""
	}
}

func argsString(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return "()"
	}
	var parts []string
	for _, field := range fields.List {
		parts = append(parts, exprString(field.Type))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func resultsString(results *ast.FieldList) string {
	if results == nil || len(results.List) == 0 {
		return ""
	}
	var parts []string
	for _, field := range results.List {
		parts = append(parts, exprString(field.Type))
	}
	if len(parts) == 1 && len(results.List[0].Names) == 0 {
		return parts[0]
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// exprString renders a type expression well enough to read in one line.
//
// It is deliberately partial: an exact rendering needs a printer, and the goal
// here is a scannable signature rather than compilable Go.
func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprString(t.Elt)
		}
		return "[" + exprString(t.Len) + "]" + exprString(t.Elt)
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.ChanType:
		return "chan " + exprString(t.Value)
	case *ast.FuncType:
		return "func" + argsString(t.Params)
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.StructType:
		return "struct{...}"
	case *ast.BasicLit:
		return t.Value
	case *ast.ParenExpr:
		return "(" + exprString(t.X) + ")"
	case *ast.IndexExpr:
		return exprString(t.X) + "[...]"
	case *ast.IndexListExpr:
		return exprString(t.X) + "[...]"
	default:
		return "?"
	}
}

func goSpecMatch(fset *token.FileSet, path string, kind Kind, spec *ast.TypeSpec, gen *ast.GenDecl) Match {
	pos := fset.Position(spec.Pos())
	return Match{
		Kind:      kind,
		Name:      spec.Name.Name,
		Path:      path,
		Line:      pos.Line,
		Column:    pos.Column,
		EndLine:   fset.Position(spec.End()).Line,
		Doc:       firstNonEmpty(trimDoc(spec.Doc), trimDoc(gen.Doc)),
		Signature: spec.Name.Name,
	}
}

func goNamedMatch(fset *token.FileSet, path string, kind Kind, name string, spec *ast.ValueSpec, gen *ast.GenDecl) Match {
	signature := name
	if spec.Type != nil {
		signature += " " + exprString(spec.Type)
	}
	pos := fset.Position(spec.Pos())
	return Match{
		Kind:      kind,
		Name:      name,
		Path:      path,
		Line:      pos.Line,
		Column:    pos.Column,
		EndLine:   fset.Position(spec.End()).Line,
		Doc:       firstNonEmpty(trimDoc(spec.Doc), trimDoc(gen.Doc)),
		Signature: signature,
	}
}

func goImportMatch(fset *token.FileSet, path string, spec *ast.ImportSpec) Match {
	name := strings.Trim(spec.Path.Value, `"`)
	pos := fset.Position(spec.Pos())
	return Match{
		Kind:      KindImport,
		Name:      name,
		Path:      path,
		Line:      pos.Line,
		Column:    pos.Column,
		EndLine:   fset.Position(spec.End()).Line,
		Signature: name,
	}
}

// goImportRecord projects one import spec into relationship data.
func goImportRecord(fset *token.FileSet, spec *ast.ImportSpec, testFile bool) Import {
	path, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		// A malformed literal should have failed the parse; keep the raw
		// spelling rather than dropping the declaration.
		path = strings.Trim(spec.Path.Value, `"`)
	}
	alias := ""
	if spec.Name != nil {
		alias = spec.Name.Name
	}
	return Import{
		Path:     path,
		Alias:    alias,
		Line:     fset.Position(spec.Pos()).Line,
		Test:     testFile,
		Language: LanguageGo,
		Kind:     "module",
	}
}

func trimDoc(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	return strings.TrimSpace(doc.Text())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
