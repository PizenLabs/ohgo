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
// library's `go/ast` and needs no cgo at all. A tree-sitter backend, if it is
// ever added, lives behind an opt-in build tag, so the shipped binary stays a
// single static Go executable and only a caller who explicitly wants
// multi-language parsing pays for cgo.
//
// The rule is therefore amended rather than broken: **cgo is opt-in and
// isolated, never a default dependency.**
package ast

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// Kind is the declaration a match describes.
type Kind string

// Declaration kinds.
const (
	// KindFunc is a function or method declaration.
	KindFunc Kind = "func"
	// KindType is a type declaration.
	KindType Kind = "type"
	// KindVar is a package-level variable.
	KindVar Kind = "var"
	// KindConst is a package-level constant.
	KindConst Kind = "const"
	// KindImport is an import declaration.
	KindImport Kind = "import"
)

// Match is one located declaration.
type Match struct {
	// Kind is the declaration kind.
	Kind Kind
	// Name is the declared identifier, empty for an import.
	Name string
	// Path is the file the declaration was found in.
	Path string
	// Line is the 1-based line the declaration starts on.
	Line int
	// EndLine is the 1-based line it ends on.
	EndLine int
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
}

// Searcher finds declarations. Implementations must be safe for concurrent use
// across distinct files.
type Searcher interface {
	// Supported reports whether a path can be parsed by this backend.
	Supported(path string) bool
	// Search parses one file and returns its declarations.
	Search(path string, opts Options) ([]Match, error)
}

// Registry maps paths to the backend that can parse them.
//
// It exists so adding a language is a registration rather than a branch at
// every call site.
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
func (GoBackend) Search(path string, opts Options) ([]Match, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("ast: parse %s: %w", path, err)
	}

	var matches []Match
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !wantKind(opts, KindFunc) {
				continue
			}
			matches = append(matches, goFuncMatch(fset, path, d))

		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if wantKind(opts, KindType) {
						matches = append(matches, goSpecMatch(fset, path, KindType, s, d))
					}
				case *ast.ValueSpec:
					kind := KindVar
					if d.Tok == token.CONST {
						kind = KindConst
					}
					if !wantKind(opts, kind) {
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
					if wantKind(opts, KindImport) {
						matches = append(matches, goImportMatch(fset, path, s))
					}
				}
			}

		}
	}
	return matches, nil
}

func wantKind(opts Options, kind Kind) bool {
	if len(opts.Kinds) == 0 {
		return true
	}
	for _, want := range opts.Kinds {
		if want == kind {
			return true
		}
	}
	return false
}

func goFuncMatch(fset *token.FileSet, path string, decl *ast.FuncDecl) Match {
	name := decl.Name.Name
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		// A method is qualified by its receiver, because a bare method name is
		// ambiguous across types.
		if receiver := typeName(decl.Recv.List[0].Type); receiver != "" {
			name = receiver + "." + decl.Name.Name
		}
	}

	signature := name + argsString(decl.Type.Params)
	if decl.Type.Results != nil {
		signature += " " + resultsString(decl.Type.Results)
	}

	return Match{
		Kind:      KindFunc,
		Name:      name,
		Path:      path,
		Line:      fset.Position(decl.Pos()).Line,
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
	return Match{
		Kind:      kind,
		Name:      spec.Name.Name,
		Path:      path,
		Line:      fset.Position(spec.Pos()).Line,
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
	return Match{
		Kind:      kind,
		Name:      name,
		Path:      path,
		Line:      fset.Position(spec.Pos()).Line,
		EndLine:   fset.Position(spec.End()).Line,
		Doc:       firstNonEmpty(trimDoc(spec.Doc), trimDoc(gen.Doc)),
		Signature: signature,
	}
}

func goImportMatch(fset *token.FileSet, path string, spec *ast.ImportSpec) Match {
	name := strings.Trim(spec.Path.Value, `"`)
	return Match{
		Kind:      KindImport,
		Name:      name,
		Path:      path,
		Line:      fset.Position(spec.Pos()).Line,
		EndLine:   fset.Position(spec.End()).Line,
		Signature: name,
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
