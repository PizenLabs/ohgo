//go:build cgo

package ast

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
)

func cGrammar() *sitter.Language   { return c.GetLanguage() }
func cppGrammar() *sitter.Language { return cpp.GetLanguage() }

func extractC(p *tsParser) error {
	return extractCFamily(p, false)
}

func extractCpp(p *tsParser) error {
	return extractCFamily(p, true)
}

func extractCFamily(p *tsParser, cppMode bool) error {
	for _, child := range namedChildren(p.root) {
		extractCStatement(p, child, "", cppMode)
	}
	extractCTestMacros(p)
	return nil
}

func extractCStatement(p *tsParser, node *sitter.Node, container string, cppMode bool) {
	if node == nil {
		return
	}
	switch node.Type() {
	case "preproc_include":
		extractCInclude(p, node)
	case "namespace_definition":
		name := identifierText(p, node)
		if name == "" {
			name = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(p.text(node), "namespace"), "{"))
		}
		qualified := joinContainer(container, name)
		if name != "" {
			p.addSymbol(KindPackage, SymbolNamespace, node.Type(), name, node, container, "", "public")
		}
		if body := childField(node, "body"); body != nil {
			for _, child := range namedChildren(body) {
				extractCStatement(p, child, qualified, cppMode)
			}
		}
	case "class_specifier", "struct_specifier", "union_specifier", "enum_specifier":
		extractCType(p, node, container, cppMode)
	case "function_definition":
		extractCFunction(p, node, container, cppMode)
	case "declaration":
		extractCDeclaration(p, node, container, cppMode)
	case "template_declaration":
		if body := childField(node, "declaration"); body != nil {
			extractCStatement(p, body, container, cppMode)
		}
	case "type_definition":
		if declarator := childOfType(node, "type_identifier"); declarator != nil {
			p.addSymbol(KindType, SymbolTypeAlias, node.Type(), p.text(declarator), node, container, "", "public")
		}
	case "linkage_specification":
		if body := childField(node, "body"); body != nil {
			for _, child := range namedChildren(body) {
				extractCStatement(p, child, container, cppMode)
			}
		}
	}
}

func extractCInclude(p *tsParser, node *sitter.Node) {
	pathNode := childOfType(node, "string_literal", "system_lib_string")
	if pathNode == nil {
		return
	}
	spec := p.text(pathNode)
	kind := "include"
	if pathNode.Type() == "system_lib_string" {
		kind = "system_include"
	}
	spec = strings.Trim(spec, `"<>`)
	// Local includes resolve relative to the including file. System includes
	// remain unresolved unless the indexed tree contains the same path.
	p.addImport(Import{Path: spec, Kind: kind, Line: p.line(node),
		Resolution: ResolutionUnresolved})
}

func extractCType(p *tsParser, node *sitter.Node, container string, cppMode bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	canonical := SymbolStruct
	kind := KindType
	switch node.Type() {
	case "class_specifier":
		canonical = SymbolClass
	case "enum_specifier":
		canonical = SymbolEnum
	case "union_specifier":
		canonical = SymbolUnion
	}
	qualified := joinContainer(container, name)
	p.addSymbol(kind, canonical, node.Type(), name, node, container, "", "public")
	if cppMode {
		if base := childOfType(node, "base_class_clause"); base != nil {
			for _, child := range namedChildren(base) {
				target := lastSegment(p.text(child))
				if target == "" || target == "public" || target == "private" || target == "protected" {
					continue
				}
				p.addRelation(rawRelation{Kind: EdgeExtends, Target: target,
					Source: name, Line: p.line(child), Column: p.column(child)})
			}
		}
	}
	body := childField(node, "body")
	if body == nil {
		body = childOfType(node, "field_declaration_list", "enumerator_list")
	}
	if body == nil {
		return
	}
	for _, member := range namedChildren(body) {
		switch member.Type() {
		case "function_definition":
			extractCFunction(p, member, qualified, cppMode)
		case "field_declaration":
			extractCField(p, member, qualified)
		case "declaration":
			extractCDeclaration(p, member, qualified, cppMode)
		case "enumerator":
			if enumName := identifierText(p, member); enumName != "" {
				p.addSymbol(KindConst, SymbolConstant, member.Type(), enumName, member, qualified, "", "public")
			}
		case "class_specifier", "struct_specifier", "union_specifier", "enum_specifier":
			extractCType(p, member, qualified, cppMode)
		}
	}
}

func extractCFunction(p *tsParser, node *sitter.Node, container string, _ bool) {
	declarator := childField(node, "declarator")
	if declarator == nil {
		return
	}
	name, receiver := cDeclaratorName(p, declarator)
	if name == "" {
		return
	}
	canonical := SymbolFunction
	kind := KindFunc
	if receiver != "" || container != "" {
		canonical, kind = SymbolMethod, KindMethod
		if receiver == "" {
			receiver = container
		}
	}
	match := p.addSymbol(kind, canonical, node.Type(), name, node, container,
		receiver, cVisibility(p, node))
	collectCCalls(p, node, match)
	if name == "main" {
		p.addEntrypoint(match)
	}
	if IsTestPath(p.path) && cHasTestMacro(p, node) {
		p.addTest(match, TestUnit, "c-test")
	}
}

func extractCDeclaration(p *tsParser, node *sitter.Node, container string, _ bool) {
	// A declaration with a function declarator is a prototype; a declaration
	// with a plain declarator is a field or global variable.
	if declarator := childOfType(node, "function_declarator"); declarator != nil {
		name, receiver := cDeclaratorName(p, declarator)
		if name != "" {
			kind := KindFunc
			canonical := SymbolFunction
			if receiver != "" || container != "" {
				kind, canonical = KindMethod, SymbolMethod
			}
			p.addSymbol(kind, canonical, declarator.Type(), name, node, container, receiver, cVisibility(p, node))
		}
		return
	}
	for _, child := range namedChildren(node) {
		if child.Type() == "init_declarator" || child.Type() == "identifier" {
			name := identifierText(p, child)
			if name == "" {
				continue
			}
			canonical := SymbolVariable
			if strings.Contains(" "+p.text(node)+" ", " const ") {
				canonical = SymbolConstant
			}
			p.addSymbol(KindVar, canonical, child.Type(), name, child, container, "", cVisibility(p, node))
		}
	}
}

func extractCField(p *tsParser, node *sitter.Node, container string) {
	for _, child := range namedChildren(node) {
		if child.Type() != "field_identifier" && child.Type() != "identifier" {
			continue
		}
		p.addSymbol(KindVar, SymbolField, child.Type(), p.text(child), node, container, container, "public")
	}
}

func cDeclaratorName(p *tsParser, node *sitter.Node) (string, string) {
	if node == nil {
		return "", ""
	}
	switch node.Type() {
	case "function_declarator":
		return cDeclaratorName(p, childField(node, "declarator"))
	case "identifier", "field_identifier":
		return p.text(node), ""
	case "qualified_identifier":
		scope := childField(node, "scope")
		name := childField(node, "name")
		if name != nil {
			receiver := ""
			if scope != nil {
				receiver = lastSegment(p.text(scope))
			}
			return lastSegment(p.text(name)), receiver
		}
	case "pointer_declarator", "reference_declarator", "parenthesized_declarator":
		for _, child := range namedChildren(node) {
			if name, receiver := cDeclaratorName(p, child); name != "" {
				return name, receiver
			}
		}
	}
	return "", ""
}

func collectCCalls(p *tsParser, node *sitter.Node, caller Match) {
	stack := []tsNode{{node: node}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.node == nil || !current.node.IsNamed() {
			continue
		}
		if current.depth > 0 {
			switch current.node.Type() {
			case "function_definition", "class_specifier", "struct_specifier":
				continue
			}
		}
		if current.node.Type() == "call_expression" {
			function := childField(current.node, "function")
			if function != nil {
				name := lastSegment(p.text(function))
				if name != "" {
					p.addCall(rawGenericCall{
						Name: name, Qualifier: qualifierText(p.text(function)),
						Line: p.line(current.node), Column: p.column(current.node),
						CallerName: caller.Name, CallerLine: caller.Line,
						CallerEndLine: caller.EndLine, CallerKind: caller.Kind,
					})
				}
			}
		}
		for i := int(current.node.NamedChildCount()) - 1; i >= 0; i-- {
			if child := current.node.NamedChild(i); child != nil {
				stack = append(stack, tsNode{node: child, depth: current.depth + 1})
			}
		}
	}
}

func cVisibility(_ *tsParser, _ *sitter.Node) string { return "public" }

// extractCTestMacros recognizes the common C/C++ test declaration macros.
// The macro name is data; no test body is invented when the grammar did not
// retain one.
func extractCTestMacros(p *tsParser) {
	p.walk(func(node *sitter.Node, _ int) bool {
		if node.Type() != "call_expression" {
			return true
		}
		function := childField(node, "function")
		if function == nil {
			return true
		}
		name := lastSegment(p.text(function))
		switch name {
		case "TEST", "TEST_F", "TEST_P", "TEST_CASE", "SCENARIO":
		default:
			return true
		}
		match := Match{
			Kind: KindFunc, Canonical: SymbolFunction, NativeKind: name,
			Language: p.language, Name: name + " " + p.text(node),
			QualifiedName: p.qualified("", name+" "+p.text(node)),
			Path:          p.path, Line: p.line(node), Column: p.column(node),
			EndLine: p.endLine(node), Test: true,
		}
		p.addTest(match, TestUnit, "c-test")
		collectCCalls(p, node, match)
		return true
	})
}

func cHasTestMacro(p *tsParser, node *sitter.Node) bool {
	text := p.text(node)
	for _, macro := range []string{"TEST(", "TEST_F(", "TEST_P(", "TEST_CASE(", "SCENARIO("} {
		if strings.Contains(text, macro) {
			return true
		}
	}
	return false
}
