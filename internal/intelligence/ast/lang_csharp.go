//go:build cgo

package ast

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/csharp"
)

func csharpGrammar() *sitter.Language { return csharp.GetLanguage() }

func extractCSharp(p *tsParser) error {
	for _, child := range namedChildren(p.root) {
		extractCSharpStatement(p, child, "", false)
	}
	return nil
}

func extractCSharpStatement(p *tsParser, node *sitter.Node, namespace string, exported bool) {
	if node == nil {
		return
	}
	switch node.Type() {
	case "using_directive":
		spec := p.text(node)
		spec = strings.TrimPrefix(spec, "using ")
		spec = strings.TrimPrefix(spec, "static ")
		spec = strings.TrimSuffix(spec, ";")
		if alias := childField(node, "name"); alias != nil && strings.Contains(spec, "=") {
			spec = strings.TrimSpace(strings.SplitN(spec, "=", 2)[1])
		}
		p.addImport(Import{Path: strings.TrimSpace(spec), Kind: "namespace", Line: p.line(node)})
	case "namespace_declaration":
		name := identifierText(p, node)
		if name == "" {
			name = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(p.text(node), "namespace"), "{"))
		}
		ns := joinContainer(namespace, name)
		if name != "" {
			p.addSymbol(KindPackage, SymbolNamespace, node.Type(), name, node, "", "", "public")
		}
		p.facts.Namespace = ns
		p.facts.Package = ns
		body := childField(node, "body")
		if body == nil {
			body = childOfType(node, "declaration_list")
		}
		if body != nil {
			for _, child := range namedChildren(body) {
				extractCSharpStatement(p, child, ns, exported)
			}
		}
	case "file_scoped_namespace_declaration":
		name := identifierText(p, node)
		if name == "" {
			name = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(p.text(node), "namespace"), ";"))
		}
		ns := joinContainer(namespace, name)
		p.facts.Namespace = ns
		p.facts.Package = ns
		if name != "" {
			p.addSymbol(KindPackage, SymbolPackage, node.Type(), name, node, "", "", "public")
		}
	case "class_declaration", "struct_declaration", "interface_declaration",
		"record_declaration", "enum_declaration", "delegate_declaration":
		extractCSharpType(p, node, namespace, "", exported)
	case "global_statement":
		// Top-level statements are a C# 9 entrypoint convention. There is no
		// named main symbol, so the file itself is the witness.
		p.addEntrypoint(Match{
			Kind: KindFunc, Canonical: SymbolFunction, NativeKind: "top_level_statement",
			Language: p.language, Name: "<top-level>", QualifiedName: p.qualified("", "<top-level>"),
			Path: p.path, Line: p.line(node), Column: p.column(node),
			EndLine: p.endLine(node), Test: IsTestPath(p.path),
		})
	}
}

func extractCSharpType(p *tsParser, node *sitter.Node, namespace, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	canonical := SymbolClass
	switch node.Type() {
	case "struct_declaration":
		canonical = SymbolStruct
	case "interface_declaration":
		canonical = SymbolInterface
	case "record_declaration":
		canonical = SymbolRecord
	case "enum_declaration":
		canonical = SymbolEnum
	case "delegate_declaration":
		canonical = SymbolTypeAlias
	}
	if p.facts.Namespace == "" {
		p.facts.Namespace = namespace
		p.facts.Package = namespace
	}
	qualified := joinContainer(container, name)
	p.addSymbol(KindType, canonical, node.Type(), name, node, container, "", csharpVisibility(p, node, exported))
	extractCSharpHeritage(p, node)
	if canonical == SymbolEnum {
		extractCSharpEnumMembers(p, node, qualified)
	}
	if canonical == SymbolRecord {
		extractCSharpRecordParameters(p, node, qualified)
	}
	body := childField(node, "body")
	if body == nil {
		body = childOfType(node, "declaration_list", "enum_member_declaration_list")
	}
	if body == nil {
		return
	}
	for _, member := range namedChildren(body) {
		switch member.Type() {
		case "method_declaration":
			extractCSharpMethod(p, member, qualified, exported)
		case "constructor_declaration":
			extractCSharpConstructor(p, member, qualified, exported)
		case "property_declaration":
			extractCSharpProperty(p, member, qualified, exported)
		case "field_declaration":
			extractCSharpField(p, member, qualified, exported)
		case "event_declaration", "event_field_declaration":
			extractCSharpField(p, member, qualified, exported)
		case "class_declaration", "struct_declaration", "interface_declaration",
			"record_declaration", "enum_declaration":
			extractCSharpType(p, member, namespace, qualified, exported)
		}
	}
}

func extractCSharpMethod(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	match := p.addSymbol(KindMethod, SymbolMethod, node.Type(), name, node, container,
		container, csharpVisibility(p, node, exported))
	collectCSharpCalls(p, node, match)
	if csharpHasTestAttribute(p, node) {
		p.addTest(match, TestUnit, "xunit")
	}
	if name == "Main" && csharpIsStatic(p, node) {
		p.addEntrypoint(match)
	}
}

func extractCSharpConstructor(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	match := p.addSymbol(KindMethod, SymbolConstructor, node.Type(), name, node, container,
		container, csharpVisibility(p, node, exported))
	collectCSharpCalls(p, node, match)
}

func extractCSharpProperty(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	p.addSymbol(KindVar, SymbolProperty, node.Type(), name, node, container,
		container, csharpVisibility(p, node, exported))
}

func extractCSharpField(p *tsParser, node *sitter.Node, container string, exported bool) {
	for _, declaration := range namedChildren(node) {
		if declaration.Type() != "variable_declaration" && declaration.Type() != "event_field_declaration" {
			continue
		}
		for _, declarator := range namedChildren(declaration) {
			if declarator.Type() != "variable_declarator" {
				continue
			}
			name := identifierText(p, declarator)
			if name == "" {
				continue
			}
			text := p.text(node)
			canonical := SymbolField
			if strings.Contains(text, " const ") || strings.Contains(text, " readonly ") {
				canonical = SymbolConstant
			}
			p.addSymbol(KindVar, canonical, declarator.Type(), name, declarator, container,
				container, csharpVisibility(p, node, exported))
		}
	}
}

func extractCSharpRecordParameters(p *tsParser, node *sitter.Node, container string) {
	params := childField(node, "parameters")
	if params == nil {
		params = childOfType(node, "parameter_list")
	}
	if params == nil {
		return
	}
	for _, param := range namedChildren(params) {
		if param.Type() != "parameter" {
			continue
		}
		name := identifierText(p, param)
		if name != "" {
			p.addSymbol(KindVar, SymbolProperty, param.Type(), name, param, container, container, "public")
		}
	}
}

func extractCSharpEnumMembers(p *tsParser, node *sitter.Node, container string) {
	body := childOfType(node, "enum_member_declaration_list")
	if body == nil {
		return
	}
	for _, member := range namedChildren(body) {
		name := identifierText(p, member)
		if name != "" {
			p.addSymbol(KindConst, SymbolConstant, member.Type(), name, member, container, "", "public")
		}
	}
}

func extractCSharpHeritage(p *tsParser, node *sitter.Node) {
	base := childOfType(node, "base_list")
	if base == nil {
		return
	}
	for _, child := range namedChildren(base) {
		if child.Type() == "," {
			continue
		}
		target := lastSegment(p.text(child))
		if target == "" {
			continue
		}
		// C# syntax does not distinguish a base class from an interface in
		// the base list. The raw edge is EXTENDS; the resolver upgrades it to
		// IMPLEMENTS when the target declaration proves an interface.
		p.addRelation(rawRelation{Kind: EdgeExtends, Target: target, Line: p.line(child), Column: p.column(child)})
		p.addReference(rawGenericReference{Name: target, Line: p.line(child), Column: p.column(child), Kind: EdgeExtends})
	}
}

func collectCSharpCalls(p *tsParser, node *sitter.Node, caller Match) {
	stack := []tsNode{{node: node}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.node == nil || !current.node.IsNamed() {
			continue
		}
		if current.depth > 0 {
			switch current.node.Type() {
			case "method_declaration", "constructor_declaration", "class_declaration",
				"interface_declaration", "record_declaration":
				continue
			}
		}
		switch current.node.Type() {
		case "invocation_expression":
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
		case "object_creation_expression":
			typ := childField(current.node, "type")
			if typ != nil {
				p.addCall(rawGenericCall{
					Name: lastSegment(p.text(typ)), Line: p.line(current.node),
					Column: p.column(current.node), CallerName: caller.Name,
					CallerLine: caller.Line, CallerEndLine: caller.EndLine,
					CallerKind: caller.Kind,
				})
			}
		}
		for i := int(current.node.NamedChildCount()) - 1; i >= 0; i-- {
			if child := current.node.NamedChild(i); child != nil {
				stack = append(stack, tsNode{node: child, depth: current.depth + 1})
			}
		}
	}
}

func csharpVisibility(p *tsParser, node *sitter.Node, exported bool) string {
	text := " " + p.headerText(node) + " "
	switch {
	case strings.Contains(text, " private "):
		return "private"
	case strings.Contains(text, " protected "):
		return "protected"
	case strings.Contains(text, " internal "):
		return "internal"
	case strings.Contains(text, " public "):
		return "public"
	case exported:
		return "public"
	default:
		return "internal"
	}
}

func csharpHasTestAttribute(p *tsParser, node *sitter.Node) bool {
	text := p.headerText(node)
	for _, annotation := range []string{"[Fact", "[Theory", "[Test", "[TestMethod",
		"[TestCase", "[DataTestMethod", "[InlineData"} {
		if strings.Contains(text, annotation) {
			return true
		}
	}
	prev := node.PrevNamedSibling()
	for prev != nil && prev.Type() == "attribute_list" {
		text = p.text(prev)
		for _, annotation := range []string{"[Fact", "[Theory", "[Test", "[TestMethod",
			"[TestCase", "[DataTestMethod"} {
			if strings.Contains(text, annotation) {
				return true
			}
		}
		prev = prev.PrevNamedSibling()
	}
	return false
}

func csharpIsStatic(p *tsParser, node *sitter.Node) bool {
	return strings.Contains(" "+p.text(node)+" ", " static ")
}
