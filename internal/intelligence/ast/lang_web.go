//go:build cgo

package ast

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

func treeSitterAdapters() []Adapter {
	return []Adapter{
		&tsAdapter{
			name: "tree-sitter-typescript", language: LanguageTypeScript,
			extensions: []string{".ts", ".mts", ".cts"}, grammar: typescript.GetLanguage,
			version: "smacker-20240827", capabilities: tsWebCapabilities(),
			extract: extractTypeScript,
		},
		&tsAdapter{
			name: "tree-sitter-tsx", language: LanguageTSX,
			extensions: []string{".tsx"}, grammar: tsx.GetLanguage,
			version: "smacker-20240827", capabilities: tsWebCapabilities(),
			extract: extractTypeScript,
		},
		&tsAdapter{
			name: "tree-sitter-javascript", language: LanguageJavaScript,
			languages:  []Language{LanguageJavaScript, LanguageJSX},
			extensions: []string{".js", ".mjs", ".cjs", ".jsx"},
			grammar:    javascript.GetLanguage, version: "smacker-20240827",
			capabilities: tsWebCapabilities(), extract: extractTypeScript,
		},
		&tsAdapter{
			name: "tree-sitter-java", language: LanguageJava,
			extensions: []string{".java"}, grammar: javaGrammar,
			version: "smacker-20240827", capabilities: tsJvmCapabilities(),
			extract: extractJava,
		},
		&tsAdapter{
			name: "tree-sitter-csharp", language: LanguageCSharp,
			extensions: []string{".cs"}, grammar: csharpGrammar,
			version: "smacker-20240827", capabilities: tsDotNetCapabilities(),
			extract: extractCSharp,
		},
		&tsAdapter{
			name: "tree-sitter-rust", language: LanguageRust,
			extensions: []string{".rs"}, grammar: rustGrammar,
			version: "smacker-20240827", capabilities: tsRustCapabilities(),
			extract: extractRust,
		},
		&tsAdapter{
			name: "tree-sitter-kotlin", language: LanguageKotlin,
			extensions: []string{".kt", ".kts"}, grammar: kotlinGrammar,
			version: "smacker-20240827", capabilities: tsJvmCapabilities(),
			extract: extractKotlin,
		},
		&tsAdapter{
			name: "tree-sitter-ruby", language: LanguageRuby,
			extensions: []string{".rb"}, grammar: rubyGrammar,
			version: "smacker-20240827", capabilities: tsRubyCapabilities(),
			extract: extractRuby,
		},
		&tsAdapter{
			name: "tree-sitter-c", language: LanguageC,
			extensions: []string{".c", ".h"}, grammar: cGrammar,
			version: "smacker-20240827", capabilities: tsCCapabilities(),
			extract: extractC,
		},
		&tsAdapter{
			name: "tree-sitter-cpp", language: LanguageCpp,
			extensions: []string{".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx"},
			grammar:    cppGrammar, version: "smacker-20240827",
			capabilities: tsCppCapabilities(), extract: extractCpp,
		},
	}
}

func tsWebCapabilities() Capabilities {
	return Capabilities{
		Symbols: true, Imports: true, Exports: true, References: true,
		Calls: true, Inheritance: true, Tests: true, Entrypoints: true,
	}
}

func tsJvmCapabilities() Capabilities {
	return tsWebCapabilities()
}

func tsDotNetCapabilities() Capabilities {
	return tsWebCapabilities()
}

func tsRustCapabilities() Capabilities {
	return tsWebCapabilities()
}

func tsRubyCapabilities() Capabilities {
	return tsWebCapabilities()
}

func tsCCapabilities() Capabilities {
	return tsWebCapabilities()
}

func tsCppCapabilities() Capabilities {
	return tsWebCapabilities()
}

// extractTypeScript handles TypeScript, TSX, JavaScript, and JSX. The
// grammars differ in node names for type syntax and JSX, but the declarations
// and relationship shapes below are shared.
func extractTypeScript(p *tsParser) error {
	root := p.root
	if root == nil {
		return nil
	}
	for _, child := range namedChildren(root) {
		extractTSStatement(p, child, "", false)
	}
	addTSFileEntrypoint(p)
	return nil
}

func extractTSStatement(p *tsParser, node *sitter.Node, container string, exported bool) {
	if node == nil {
		return
	}
	switch node.Type() {
	case "import_statement":
		extractTSImport(p, node)
	case "export_statement":
		declaration := childField(node, "declaration")
		if declaration != nil {
			extractTSStatement(p, declaration, container, true)
			return
		}
		// A re-export names a source module but declares no local symbol.
		if source := childField(node, "source"); source != nil {
			p.addImport(Import{
				Path: extensionText(p.text(source)), Kind: "re-export",
				Line: p.line(node),
			})
		}
		// export { a, b } references local declarations; it is an export
		// visibility fact, not a new declaration.
	case "class_declaration", "abstract_class_declaration", "class":
		extractTSClass(p, node, container, exported)
	case "interface_declaration":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		p.addSymbol(KindType, SymbolInterface, node.Type(), name, node, container, "", tsVisibility(p, node, exported))
		if body := childField(node, "body"); body != nil {
			extractTSInterfaceMembers(p, body, name)
		}
		extractTSHeritage(p, node, name)
	case "type_alias_declaration":
		name := identifierText(p, node)
		if name != "" {
			p.addSymbol(KindType, SymbolTypeAlias, node.Type(), name, node, container, "", tsVisibility(p, node, exported))
		}
	case "enum_declaration":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		p.addSymbol(KindType, SymbolEnum, node.Type(), name, node, container, "", tsVisibility(p, node, exported))
		extractTSEnumMembers(p, node, name)
	case "function_declaration", "generator_function_declaration", "function":
		extractTSFunction(p, node, container, exported, "")
	case "lexical_declaration", "variable_declaration":
		extractTSVariables(p, node, container, exported)
	case "namespace_declaration", "internal_module", "module":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		p.addSymbol(KindPackage, SymbolNamespace, node.Type(), name, node, container, "", tsVisibility(p, node, exported))
		if body := childField(node, "body"); body != nil {
			for _, child := range namedChildren(body) {
				extractTSStatement(p, child, joinContainer(container, name), exported)
			}
		}
	case "ambient_declaration":
		if body := childField(node, "body"); body != nil {
			extractTSStatement(p, body, container, exported)
		}
	case "expression_statement":
		// A top-level test/describe call is a declaration even though it is
		// not inside a named function.
		if call := childOfType(node, "call_expression"); call != nil {
			handleTSCall(p, call, Match{})
		}
	}
}

func extractTSClass(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	qualifiedContainer := joinContainer(container, name)
	p.addSymbol(KindType, SymbolClass, node.Type(), name, node, container, "", tsVisibility(p, node, exported))
	extractTSHeritage(p, node, name)
	body := childField(node, "body")
	if body == nil {
		return
	}
	for _, member := range namedChildren(body) {
		switch member.Type() {
		case "method_definition":
			methodName := identifierText(p, member)
			if methodName == "" {
				continue
			}
			canonical := SymbolMethod
			kind := KindMethod
			if methodName == "constructor" {
				canonical, kind = SymbolConstructor, KindMethod
			}
			match := p.addSymbol(kind, canonical, member.Type(), methodName, member,
				qualifiedContainer, name, tsVisibility(p, member, exported))
			collectTSCalls(p, member, match)
		case "public_field_definition", "field_definition", "property_signature",
			"abstract_method_signature", "method_signature":
			fieldName := identifierText(p, member)
			if fieldName == "" {
				continue
			}
			canonical := SymbolField
			if member.Type() == "property_signature" || member.Type() == "field_definition" {
				canonical = SymbolProperty
			}
			p.addSymbol(KindVar, canonical, member.Type(), fieldName, member,
				qualifiedContainer, name, tsVisibility(p, member, exported))
		case "class_static_block", "static_block":
			// There is no named symbol; calls still belong to the class
			// initializer and are intentionally not attributed to a method.
		}
	}
}

func extractTSInterfaceMembers(p *tsParser, body *sitter.Node, container string) {
	for _, member := range namedChildren(body) {
		switch member.Type() {
		case "method_signature", "abstract_method_signature", "call_signature":
			name := identifierText(p, member)
			if name == "" {
				continue
			}
			p.addSymbol(KindMethod, SymbolMethod, member.Type(), name, member,
				container, container, "public")
		case "property_signature":
			name := identifierText(p, member)
			if name != "" {
				p.addSymbol(KindVar, SymbolProperty, member.Type(), name, member,
					container, container, "public")
			}
		}
	}
}

func extractTSEnumMembers(p *tsParser, node *sitter.Node, container string) {
	body := childField(node, "body")
	if body == nil {
		return
	}
	for _, member := range namedChildren(body) {
		if member.Type() != "enum_assignment" && member.Type() != "property_identifier" {
			continue
		}
		name := identifierText(p, member)
		if name == "" {
			continue
		}
		p.addSymbol(KindConst, SymbolConstant, member.Type(), name, member,
			container, "", "public")
	}
}

func extractTSFunction(p *tsParser, node *sitter.Node, container string, exported bool, assignedName string) {
	name := assignedName
	if name == "" {
		name = identifierText(p, node)
	}
	if name == "" {
		return
	}
	match := p.addSymbol(KindFunc, SymbolFunction, node.Type(), name, node,
		container, "", tsVisibility(p, node, exported))
	collectTSCalls(p, node, match)
}

func extractTSVariables(p *tsParser, node *sitter.Node, container string, exported bool) {
	for _, child := range namedChildren(node) {
		if child.Type() != "variable_declarator" {
			continue
		}
		name := identifierText(p, child)
		if name == "" {
			continue
		}
		value := childField(child, "value")
		if value != nil && (value.Type() == "arrow_function" ||
			value.Type() == "function_expression" || value.Type() == "function") {
			extractTSFunction(p, value, container, exported, name)
			continue
		}
		p.addSymbol(KindVar, SymbolVariable, child.Type(), name, child,
			container, "", tsVisibility(p, child, exported))
	}
}

func extractTSImport(p *tsParser, node *sitter.Node) {
	source := childField(node, "source")
	if source == nil {
		return
	}
	spec := extensionText(p.text(source))
	imported := Import{
		Path: spec, Line: p.line(node), Kind: "module",
	}
	if strings.HasPrefix(spec, ".") {
		imported.Kind = "relative"
	}
	clause := childOfType(node, "import_clause")
	if clause != nil {
		for _, child := range namedChildren(clause) {
			switch child.Type() {
			case "identifier":
				imported.Alias = p.text(child)
			case "named_imports":
				for _, specifier := range namedChildren(child) {
					if specifier.Type() == "import_specifier" {
						if name := identifierText(p, specifier); name != "" {
							imported.Names = append(imported.Names, name)
						}
					}
				}
			case "namespace_import":
				for _, id := range namedChildren(child) {
					if id.Type() == "identifier" {
						imported.Alias = p.text(id)
					}
				}
			}
		}
	}
	p.addImport(imported)
}

func extractTSHeritage(p *tsParser, node *sitter.Node, container string) {
	heritage := childOfType(node, "class_heritage", "extends_type_clause", "extends_clause")
	if heritage == nil {
		return
	}
	for _, child := range namedChildren(heritage) {
		switch child.Type() {
		case "extends_clause":
			for _, value := range namedChildren(child) {
				if value.Type() == "value" || value.Type() == "identifier" ||
					value.Type() == "member_expression" || value.Type() == "generic_type" {
					addInheritance(p, child, value, EdgeExtends, container)
				}
			}
		case "implements_clause":
			for _, value := range namedChildren(child) {
				addInheritance(p, child, value, EdgeImplements, container)
			}
		case "identifier", "member_expression", "generic_type", "type_identifier":
			addInheritance(p, heritage, child, EdgeExtends, container)
		}
	}
}

func addInheritance(p *tsParser, clause, value *sitter.Node, kind EdgeKind, _ string) {
	target := lastSegment(p.text(value))
	if target == "" {
		return
	}
	p.addRelation(rawRelation{Kind: kind, Target: target, Line: p.line(clause), Column: p.column(clause)})
	p.addReference(rawGenericReference{Name: target, Line: p.line(value), Column: p.column(value), Kind: kind})
}

// collectTSCalls records call sites and test declarations. It descends into
// the body but stops at nested declarations: those are processed separately so
// a call is attributed to its own enclosing function, never its grandparent.
func collectTSCalls(p *tsParser, node *sitter.Node, caller Match) {
	if node == nil {
		return
	}
	stack := []tsNode{{node: node, depth: 0}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.node == nil || !current.node.IsNamed() {
			continue
		}
		switch current.node.Type() {
		case "function_declaration", "generator_function_declaration",
			"class_declaration", "abstract_class_declaration", "method_definition":
			if current.depth > 0 {
				continue
			}
		}
		if current.node.Type() == "call_expression" {
			handleTSCall(p, current.node, caller)
		}
		if current.node.Type() == "new_expression" {
			if constructor := childField(current.node, "constructor"); constructor != nil {
				name := lastSegment(p.text(constructor))
				if name != "" {
					p.addCall(rawGenericCall{
						Name: name, Qualifier: qualifierText(p.text(constructor)),
						Line: p.line(current.node), Column: p.column(current.node),
						CallerName: caller.Name, CallerLine: caller.Line,
						CallerEndLine: caller.EndLine, CallerKind: caller.Kind,
					})
				}
			}
		}
		if current.depth >= tsMaxTraversalDepth {
			continue
		}
		children := namedChildren(current.node)
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, tsNode{node: children[i], depth: current.depth + 1})
		}
	}
}

func handleTSCall(p *tsParser, node *sitter.Node, caller Match) {
	function := childField(node, "function")
	if function == nil {
		return
	}
	name := ""
	qualifier := ""
	switch function.Type() {
	case "identifier":
		name = p.text(function)
	case "member_expression", "subscript_expression":
		name = lastSegment(p.text(function))
		qualifier = qualifierText(p.text(function))
	case "call_expression":
		return
	}
	if name == "" {
		return
	}
	// Test framework calls are declarations, not application calls. The
	// callback body is collected under the test declaration so its calls can
	// become static test associations without inventing a named function.
	if isTSWebTestCall(name) {
		label := tsFirstStringArgument(p, node)
		if label != "" || name == "describe" {
			test := Match{
				Kind: KindFunc, Canonical: SymbolFunction, NativeKind: name,
				Language: p.language, Name: name + ": " + label,
				QualifiedName: p.qualified("", name+": "+label),
				Path:          p.path, Line: p.line(node), Column: p.column(node),
				EndLine: p.endLine(node), Test: true,
			}
			p.addTest(test, TestUnit, "jest")
			if callback := tsTestCallback(p, node); callback != nil {
				collectTSCalls(p, callback, test)
			}
		}
		return
	}
	if name == "require" {
		if spec := tsFirstStringArgument(p, node); spec != "" {
			p.addImport(Import{Path: spec, Kind: "require", Line: p.line(node)})
		}
	}
	p.addCall(rawGenericCall{
		Name: name, Qualifier: qualifier, Line: p.line(node), Column: p.column(node),
		CallerName: caller.Name, CallerLine: caller.Line,
		CallerEndLine: caller.EndLine, CallerKind: caller.Kind,
	})
}

func tsTestCallback(p *tsParser, call *sitter.Node) *sitter.Node {
	args := childField(call, "arguments")
	if args == nil {
		return nil
	}
	for _, child := range namedChildren(args) {
		switch child.Type() {
		case "arrow_function", "function_expression", "function":
			if body := childField(child, "body"); body != nil {
				return body
			}
			return child
		}
	}
	return nil
}

func tsFirstStringArgument(p *tsParser, call *sitter.Node) string {
	args := childField(call, "arguments")
	if args == nil {
		return ""
	}
	for _, child := range namedChildren(args) {
		if child.Type() == "string" {
			return extensionText(p.text(child))
		}
	}
	return ""
}

func isTSWebTestCall(name string) bool {
	switch name {
	case "it", "test", "describe", "specify", "context":
		return true
	default:
		return false
	}
}

func tsVisibility(p *tsParser, node *sitter.Node, exported bool) string {
	text := p.headerText(node)
	for _, visibility := range []string{"private", "protected", "public"} {
		if strings.Contains(" "+text+" ", " "+visibility+" ") {
			return visibility
		}
	}
	if exported {
		return "public"
	}
	if node.Type() == "class_declaration" || node.Type() == "interface_declaration" ||
		node.Type() == "type_alias_declaration" || node.Type() == "enum_declaration" {
		return "private"
	}
	return "public"
}

func addTSFileEntrypoint(p *tsParser) {
	rel := strings.ReplaceAll(p.path, "\\", "/")
	base := rel[strings.LastIndex(rel, "/")+1:]
	conventional := false
	for _, prefix := range []string{"app/", "src/app/", "pages/", "src/pages/"} {
		if strings.Contains("/"+rel, "/"+prefix) {
			conventional = true
			break
		}
	}
	if !conventional {
		return
	}
	switch base {
	case "page.tsx", "page.ts", "page.jsx", "page.js", "layout.tsx", "layout.ts",
		"layout.jsx", "layout.js", "index.tsx", "index.ts", "index.jsx", "index.js":
	default:
		return
	}
	name := firstExportedSymbolName(p.facts.Symbols)
	if name == "" {
		name = strings.TrimSuffix(base, pathExt(base))
	}
	p.addEntrypoint(Match{
		Kind: KindFunc, Canonical: SymbolFunction, NativeKind: "route_entrypoint",
		Language: p.language, Name: name, QualifiedName: p.qualified("", name),
		Path: p.path, Line: 1, Column: 1, EndLine: 1, Exported: true,
	})
}

func joinContainer(container, name string) string {
	if container == "" {
		return name
	}
	return container + "." + name
}

func qualifierText(text string) string {
	text = strings.TrimSpace(text)
	for _, separator := range []string{"::", "."} {
		if idx := strings.LastIndex(text, separator); idx > 0 {
			qualifier := strings.TrimSpace(text[:idx])
			if qualifier == "this" || qualifier == "self" || qualifier == "super" {
				return ""
			}
			return lastSegment(qualifier)
		}
	}
	return ""
}

func firstExportedSymbolName(symbols []Match) string {
	for _, symbol := range symbols {
		if symbol.Exported && symbol.Container == "" &&
			(symbol.Canonical == SymbolFunction || symbol.Canonical == SymbolClass) {
			return symbol.Name
		}
	}
	return ""
}
