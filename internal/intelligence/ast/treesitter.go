//go:build cgo

package ast

import (
	"context"
	"fmt"
	"os"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// tsGrammar returns one pinned grammar. Versions come from the module
// dependency in go.mod; no grammar is loaded at runtime from the host.
type tsGrammar func() *sitter.Language

// tsAdapter is the shared syntax adapter. Language-specific extraction lives
// in the adapter's extract function; the substrate owns parsing, coordinate
// normalization, resource cleanup, and bounded error disclosure.
type tsAdapter struct {
	name         string
	language     Language
	languages    []Language
	extensions   []string
	grammar      tsGrammar
	version      string
	capabilities Capabilities
	extract      func(*tsParser) error
}

func (a *tsAdapter) Name() string { return a.name }

func (a *tsAdapter) Language() Language { return a.language }

func (a *tsAdapter) Languages() []Language {
	if len(a.languages) > 0 {
		return append([]Language(nil), a.languages...)
	}
	return []Language{a.language}
}

func (a *tsAdapter) Extensions() []string { return append([]string(nil), a.extensions...) }

func (a *tsAdapter) Supported(pathArg string) bool {
	language, ok := LanguageForPath(pathArg)
	if !ok {
		return false
	}
	for _, candidate := range a.Languages() {
		if candidate == language {
			return true
		}
	}
	return false
}

func (a *tsAdapter) Capabilities() Capabilities { return a.capabilities }

// ParseFile implements Adapter. Every parser, tree, and node resource has one
// owner: this function closes the tree and parser before returning, including
// on parse failure. No syntax tree is retained after extraction.
func (a *tsAdapter) ParseFile(pathArg string, opts ParseOptions) (facts FileFacts, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			facts = FileFacts{}
			err = fmt.Errorf("ast: %s adapter panicked on %s: %v", a.name, pathArg, recovered)
		}
	}()
	source, err := os.ReadFile(pathArg)
	if err != nil {
		return FileFacts{}, fmt.Errorf("ast: read %s: %w", pathArg, err)
	}
	return a.parseSource(context.Background(), pathArg, source, opts)
}

// ParseFileContext implements ContextAdapter. Cancellation is observed by the
// tree-sitter parser through its cancellation flag.
func (a *tsAdapter) ParseFileContext(ctx context.Context, pathArg string, opts ParseOptions) (facts FileFacts, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			facts = FileFacts{}
			err = fmt.Errorf("ast: %s adapter panicked on %s: %v", a.name, pathArg, recovered)
		}
	}()
	source, err := os.ReadFile(pathArg)
	if err != nil {
		return FileFacts{}, fmt.Errorf("ast: read %s: %w", pathArg, err)
	}
	return a.parseSource(ctx, pathArg, source, opts)
}

func (a *tsAdapter) parseSource(ctx context.Context, pathArg string, source []byte, opts ParseOptions) (FileFacts, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(a.grammar())
	tree, err := parser.ParseCtx(ctx, nil, source)
	if err != nil {
		parser.Close()
		return FileFacts{}, fmt.Errorf("ast: parse %s: %w", pathArg, err)
	}
	defer parser.Close()
	defer tree.Close()

	p := &tsParser{
		path:     pathArg,
		source:   source,
		language: a.language,
		adapter:  a.name,
		root:     tree.RootNode(),
		facts: FileFacts{
			Language: a.language,
			Adapter:  a.name,
			Parser:   Parser{Name: a.name, Version: a.version},
			Imports:  make([]Import, 0),
			Symbols:  make([]Match, 0),
			Tests:    make([]TestDecl, 0),
		},
	}
	if err := a.extract(p); err != nil {
		return FileFacts{}, err
	}
	p.collectParseIssues()
	if p.facts.Package == "" {
		p.facts.Package = p.facts.Namespace
	}
	p.finishSymbols(opts)
	return p.facts, nil
}

// tsParser owns one parse pass. It intentionally exposes only canonical fact
// builders, never raw tree-sitter nodes, to the index or the model.
type tsParser struct {
	path     string
	source   []byte
	language Language
	adapter  string
	root     *sitter.Node
	facts    FileFacts
	issues   int
}

// tsNode is the bounded traversal unit. The parent chain is not stored;
// language extractors pass the enclosing container explicitly.
type tsNode struct {
	node  *sitter.Node
	depth int
}

const (
	tsMaxTraversalDepth = 256
	tsMaxParseIssues    = 16
)

// walk visits named syntax nodes in source order. It is iterative so a
// pathological nesting depth cannot exhaust the Go stack.
func (p *tsParser) walk(fn func(node *sitter.Node, depth int) (descend bool)) {
	if p == nil || p.root == nil {
		return
	}
	stack := []tsNode{{node: p.root, depth: 0}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.node == nil || !current.node.IsNamed() {
			continue
		}
		descend := fn(current.node, current.depth)
		if !descend || current.depth >= tsMaxTraversalDepth {
			if current.depth >= tsMaxTraversalDepth {
				p.addIssue("syntax nesting bound reached", current.node)
			}
			continue
		}
		children := namedChildren(current.node)
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, tsNode{node: children[i], depth: current.depth + 1})
		}
	}
}

// addSymbol records one canonical declaration.
func (p *tsParser) addSymbol(kind Kind, canonical SymbolKind, native, name string, node *sitter.Node, container, receiver, visibility string) Match {
	match := Match{
		Kind:       kind,
		Canonical:  canonical,
		NativeKind: native,
		Language:   p.language,
		Name:       name,
		Container:  container,
		Receiver:   receiver,
		Visibility: visibility,
		Path:       p.path,
		Line:       p.line(node),
		Column:     p.column(node),
		EndLine:    p.endLine(node),
		Test:       IsTestPath(p.path),
	}
	if p.facts.Namespace != "" {
		match.Package = p.facts.Namespace
	} else {
		match.Package = p.facts.Package
	}
	match.Exported = exportedVisibility(visibility)
	if canonical == SymbolPackage || canonical == SymbolNamespace || canonical == SymbolModule {
		match.QualifiedName = name
	} else if container != "" {
		match.Name = container + "." + name
		match.QualifiedName = p.qualified(container, name)
	} else {
		match.QualifiedName = p.qualified("", name)
	}
	p.facts.Symbols = append(p.facts.Symbols, match)
	return match
}

func (p *tsParser) qualified(container, name string) string {
	base := p.facts.Namespace
	if base == "" {
		base = p.facts.Package
	}
	if base != "" {
		base += "."
	}
	if container != "" {
		base += container + "."
	}
	return base + name
}

func (p *tsParser) addImport(imported Import) {
	imported.Language = p.language
	if imported.Resolution == "" {
		imported.Resolution = ResolutionUnresolved
	}
	if !imported.Test {
		imported.Test = IsTestPath(p.path)
	}
	p.facts.Imports = append(p.facts.Imports, imported)
}

func (p *tsParser) addTest(match Match, kind TestKind, framework string) {
	if match.Path == "" {
		match.Path = p.path
	}
	p.facts.Tests = append(p.facts.Tests, TestDecl{Symbol: match, Kind: kind, Framework: framework})
}

func (p *tsParser) addEntrypoint(match Match) {
	if match.Path == "" {
		match.Path = p.path
	}
	p.facts.Entrypoints = append(p.facts.Entrypoints, match)
}

func (p *tsParser) addCall(call rawGenericCall) {
	p.facts.genericCalls = append(p.facts.genericCalls, call)
}

func (p *tsParser) addReference(ref rawGenericReference) {
	p.facts.genericReferences = append(p.facts.genericReferences, ref)
}

func (p *tsParser) addRelation(relation rawRelation) {
	p.facts.relations = append(p.facts.relations, relation)
}

func (p *tsParser) addIssue(message string, node *sitter.Node) {
	if p.issues >= tsMaxParseIssues {
		return
	}
	issue := ParseIssue{Message: message}
	if node != nil {
		issue.Line = p.line(node)
		issue.Column = p.column(node)
	}
	p.facts.Issues = append(p.facts.Issues, issue)
	p.issues++
}

// collectParseIssues marks the facts partial if the grammar reported a
// recoverable error. A parse failure still describes the declarations the
// parser recovered; it must never be mistaken for an empty file.
func (p *tsParser) collectParseIssues() {
	if p.root == nil {
		p.facts.Partial = true
		p.addIssue("parser returned no syntax tree", nil)
		return
	}
	if !p.root.HasError() {
		return
	}
	real := 0
	p.walk(func(node *sitter.Node, _ int) bool {
		if node.IsError() {
			real++
			p.addIssue("syntax error", node)
			return p.issues < tsMaxParseIssues
		}
		if node.IsMissing() {
			// Some grammars insert an automatic-semicolon recovery node in
			// otherwise valid Kotlin. That is a grammar detail, not a
			// declaration the runtime lost.
			if strings.Contains(node.Type(), "automatic_semicolon") {
				return true
			}
			real++
			p.addIssue("missing syntax element", node)
		}
		return p.issues < tsMaxParseIssues
	})
	if real == 0 {
		return
	}
	p.facts.Partial = true
	if len(p.facts.Issues) == 0 {
		p.addIssue("recoverable syntax error", p.root)
	}
}

// finishSymbols applies ordering and the shared kind filter. Tree-sitter
// adapters always extract the bounded canonical taxonomy; the filter exists
// for Search compatibility, not for the repository index.
func (p *tsParser) finishSymbols(opts ParseOptions) {
	sortSymbols(p.facts.Symbols)
	if len(opts.Kinds) == 0 {
		return
	}
	filtered := p.facts.Symbols[:0]
	for _, symbol := range p.facts.Symbols {
		if kindAllowed(opts, symbol.Kind) {
			filtered = append(filtered, symbol)
		}
	}
	p.facts.Symbols = filtered
}

func (p *tsParser) line(node *sitter.Node) int {
	if node == nil {
		return 0
	}
	return int(node.StartPoint().Row) + 1
}

func (p *tsParser) endLine(node *sitter.Node) int {
	if node == nil {
		return 0
	}
	return int(node.EndPoint().Row) + 1
}

// column is the node's 1-based UTF-8 byte column. It uses the same coordinate
// convention as Go's token.Position, so a mixed-language snapshot has one
// column meaning. The byte range remains the authoritative source witness.
func (p *tsParser) column(node *sitter.Node) int {
	if node == nil {
		return 0
	}
	return int(node.StartPoint().Column) + 1
}

func (p *tsParser) text(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	return strings.TrimSpace(node.Content(p.source))
}

// headerText returns the declaration head before its body. Visibility and
// attribute checks must not see a private member inside a class body.
func (p *tsParser) headerText(node *sitter.Node) string {
	text := p.text(node)
	if idx := strings.IndexByte(text, '{'); idx >= 0 {
		text = text[:idx]
	}
	if idx := strings.Index(text, "=>"); idx >= 0 {
		text = text[:idx]
	}
	return text
}

func namedChildren(node *sitter.Node) []*sitter.Node {
	if node == nil {
		return nil
	}
	out := make([]*sitter.Node, 0, node.NamedChildCount())
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if child := node.NamedChild(i); child != nil {
			out = append(out, child)
		}
	}
	return out
}

func childField(node *sitter.Node, field string) *sitter.Node {
	if node == nil {
		return nil
	}
	return node.ChildByFieldName(field)
}

func childOfType(node *sitter.Node, types ...string) *sitter.Node {
	if node == nil {
		return nil
	}
	wanted := map[string]bool{}
	for _, typ := range types {
		wanted[typ] = true
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child != nil && wanted[child.Type()] {
			return child
		}
	}
	return nil
}

func identifierText(p *tsParser, node *sitter.Node) string {
	if node == nil {
		return ""
	}
	if name := childField(node, "name"); name != nil {
		return p.text(name)
	}
	switch node.Type() {
	case "identifier", "type_identifier", "field_identifier", "property_identifier",
		"simple_identifier", "constant", "namespace_identifier", "type_parameter":
		return p.text(node)
	case "scope_resolution", "qualified_identifier", "scoped_identifier",
		"navigation_expression", "member_expression":
		return lastSegment(p.text(node))
	}
	// Kotlin and C-like grammars often omit a name field. The first
	// identifier-shaped named child is the declaration name; modifiers and
	// annotations are skipped because they are different node types.
	for _, child := range namedChildren(node) {
		switch child.Type() {
		case "identifier", "type_identifier", "field_identifier",
			"property_identifier", "simple_identifier", "constant",
			"namespace_identifier":
			return p.text(child)
		}
	}
	return ""
}

// lastSegment returns the final identifier in a dotted/scoped spelling.
func lastSegment(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "(){}[]<>")
	text = strings.TrimSpace(text)
	for _, separator := range []string{"::", ".", "->"} {
		if idx := strings.LastIndex(text, separator); idx >= 0 {
			text = text[idx+len(separator):]
		}
	}
	return strings.TrimSpace(strings.Trim(text, "()[]{}<>"))
}

// extensionText strips quotes and surrounding punctuation from a string node.
func extensionText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, `"'`)
	return text
}

func exportedVisibility(visibility string) bool {
	switch strings.ToLower(strings.TrimSpace(visibility)) {
	case "public", "export", "exported", "protected", "package":
		return true
	default:
		return false
	}
}
