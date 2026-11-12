//go:build cgo

package ast

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"
)

func javaGrammar() *sitter.Language { return java.GetLanguage() }

func extractJava(p *tsParser) error {
	for _, child := range namedChildren(p.root) {
		switch child.Type() {
		case "package_declaration":
			pkg := strings.TrimSpace(p.text(child))
			pkg = strings.TrimPrefix(pkg, "package ")
			pkg = strings.TrimSuffix(pkg, ";")
			p.facts.Namespace = strings.TrimSpace(pkg)
			p.facts.Package = p.facts.Namespace
			p.addSymbol(KindPackage, SymbolPackage, child.Type(), p.facts.Namespace, child, "", "", "public")
		case "import_declaration":
			spec := strings.TrimSpace(p.text(child))
			spec = strings.TrimPrefix(spec, "import ")
			spec = strings.TrimPrefix(spec, "static ")
			spec = strings.TrimSuffix(spec, ";")
			p.addImport(Import{Path: strings.TrimSpace(spec), Kind: "package", Line: p.line(child)})
		default:
			extractJavaType(p, child, "", false)
		}
	}
	return nil
}

func extractJavaType(p *tsParser, node *sitter.Node, container string, exported bool) {
	switch node.Type() {
	case "class_declaration", "interface_declaration", "enum_declaration",
		"record_declaration", "annotation_type_declaration":
	default:
		return
	}
	name := identifierText(p, node)
	if name == "" {
		return
	}
	canonical := SymbolClass
	kind := KindType
	switch node.Type() {
	case "interface_declaration":
		canonical = SymbolInterface
	case "enum_declaration":
		canonical = SymbolEnum
	case "record_declaration":
		canonical = SymbolRecord
	case "annotation_type_declaration":
		canonical = SymbolInterface
	}
	qualified := joinContainer(container, name)
	p.addSymbol(kind, canonical, node.Type(), name, node, container, "", javaVisibility(p, node, exported))
	extractJavaHeritage(p, node, name)

	body := childField(node, "body")
	if body == nil {
		body = childOfType(node, "class_body", "interface_body", "enum_body", "record_body")
	}
	if body == nil {
		return
	}
	for _, member := range namedChildren(body) {
		switch member.Type() {
		case "method_declaration":
			extractJavaMethod(p, member, qualified, exported)
		case "constructor_declaration":
			extractJavaConstructor(p, member, qualified, exported)
		case "field_declaration":
			extractJavaField(p, member, qualified, exported)
		case "enum_constant":
			enumName := identifierText(p, member)
			if enumName != "" {
				p.addSymbol(KindConst, SymbolConstant, member.Type(), enumName, member,
					qualified, "", "public")
			}
		case "class_declaration", "interface_declaration", "enum_declaration",
			"record_declaration", "annotation_type_declaration":
			extractJavaType(p, member, qualified, exported)
		}
	}
}

func extractJavaMethod(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	match := p.addSymbol(KindMethod, SymbolMethod, node.Type(), name, node, container,
		container, javaVisibility(p, node, exported))
	collectJavaCalls(p, node, match)
	if javaHasTestAnnotation(p, node) {
		p.addTest(match, TestUnit, "junit")
	}
	if name == "main" && javaIsStatic(p, node) && javaMainSignature(p, node) {
		p.addEntrypoint(match)
	}
}

func extractJavaConstructor(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	match := p.addSymbol(KindMethod, SymbolConstructor, node.Type(), name, node, container,
		container, javaVisibility(p, node, exported))
	collectJavaCalls(p, node, match)
}

func extractJavaField(p *tsParser, node *sitter.Node, container string, exported bool) {
	for _, declarator := range namedChildren(node) {
		if declarator.Type() != "variable_declarator" {
			continue
		}
		name := identifierText(p, declarator)
		if name == "" {
			continue
		}
		canonical := SymbolField
		if strings.Contains(p.text(node), " static ") && strings.Contains(p.text(node), " final ") {
			canonical = SymbolConstant
		}
		p.addSymbol(KindVar, canonical, declarator.Type(), name, declarator, container,
			container, javaVisibility(p, node, exported))
	}
}

func extractJavaHeritage(p *tsParser, node *sitter.Node, container string) {
	if superclass := childField(node, "superclass"); superclass != nil {
		for _, child := range namedChildren(superclass) {
			addJavaRelation(p, superclass, child, EdgeExtends)
		}
	}
	if interfaces := childField(node, "interfaces"); interfaces != nil {
		walkJavaTypes(p, interfaces, EdgeImplements)
	}
	if extended := childField(node, "extends_interfaces"); extended != nil {
		walkJavaTypes(p, extended, EdgeExtends)
	}
	_ = container
}

func walkJavaTypes(p *tsParser, node *sitter.Node, kind EdgeKind) {
	if node == nil {
		return
	}
	if node.Type() == "type_identifier" {
		addJavaRelation(p, node, node, kind)
		return
	}
	for _, child := range namedChildren(node) {
		walkJavaTypes(p, child, kind)
	}
}

func addJavaRelation(p *tsParser, clause, value *sitter.Node, kind EdgeKind) {
	target := lastSegment(p.text(value))
	if target == "" {
		return
	}
	p.addRelation(rawRelation{Kind: kind, Target: target, Line: p.line(clause), Column: p.column(clause)})
	p.addReference(rawGenericReference{Name: target, Line: p.line(value), Column: p.column(value), Kind: kind})
}

func collectJavaCalls(p *tsParser, node *sitter.Node, caller Match) {
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
		case "method_invocation":
			name := childField(current.node, "name")
			if name != nil {
				object := childField(current.node, "object")
				p.addCall(rawGenericCall{
					Name: p.text(name), Qualifier: javaQualifier(p, object),
					Line: p.line(current.node), Column: p.column(current.node),
					CallerName: caller.Name, CallerLine: caller.Line,
					CallerEndLine: caller.EndLine, CallerKind: caller.Kind,
				})
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

func javaQualifier(p *tsParser, node *sitter.Node) string {
	if node == nil {
		return ""
	}
	text := p.text(node)
	if text == "this" || text == "super" {
		return ""
	}
	return lastSegment(text)
}

func javaVisibility(p *tsParser, node *sitter.Node, exported bool) string {
	text := " " + p.headerText(node) + " "
	switch {
	case strings.Contains(text, " private "):
		return "private"
	case strings.Contains(text, " protected "):
		return "protected"
	case strings.Contains(text, " public "):
		return "public"
	case exported:
		return "public"
	default:
		return "package"
	}
}

func javaHasTestAnnotation(p *tsParser, node *sitter.Node) bool {
	text := p.headerText(node)
	for _, annotation := range []string{"@Test", "@ParameterizedTest", "@RepeatedTest",
		"@TestFactory", "@TestTemplate"} {
		if strings.Contains(text, annotation) {
			return true
		}
	}
	return false
}

func javaIsStatic(p *tsParser, node *sitter.Node) bool {
	return strings.Contains(" "+p.text(node)+" ", " static ")
}

func javaMainSignature(p *tsParser, node *sitter.Node) bool {
	text := p.text(node)
	return strings.Contains(text, "String[]") || strings.Contains(text, "String...")
}
