//go:build cgo

package ast

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/kotlin"
)

func kotlinGrammar() *sitter.Language { return kotlin.GetLanguage() }

func extractKotlin(p *tsParser) error {
	for _, child := range namedChildren(p.root) {
		extractKotlinStatement(p, child, "", false)
	}
	return nil
}

func extractKotlinStatement(p *tsParser, node *sitter.Node, container string, exported bool) {
	if node == nil {
		return
	}
	switch node.Type() {
	case "package_header":
		name := strings.TrimSpace(strings.TrimPrefix(p.text(node), "package"))
		p.facts.Namespace = name
		p.facts.Package = name
		p.addSymbol(KindPackage, SymbolPackage, node.Type(), name, node, "", "", "public")
	case "import_list":
		for _, child := range namedChildren(node) {
			extractKotlinImport(p, child)
		}
	case "import_header":
		extractKotlinImport(p, node)
	case "class_declaration":
		extractKotlinClass(p, node, container, exported)
	case "object_declaration":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		p.addSymbol(KindType, SymbolObject, node.Type(), name, node, container, "", kotlinVisibility(p, node, exported))
		body := childField(node, "body")
		if body == nil {
			body = childOfType(node, "class_body")
		}
		extractKotlinBody(p, body, joinContainer(container, name), exported)
	case "function_declaration":
		extractKotlinFunction(p, node, container, exported)
	case "property_declaration", "property_delegate":
		name := identifierText(p, node)
		if name != "" {
			p.addSymbol(KindVar, SymbolProperty, node.Type(), name, node, container, "", kotlinVisibility(p, node, exported))
		}
	}
}

func extractKotlinImport(p *tsParser, node *sitter.Node) {
	if node == nil {
		return
	}
	spec := strings.TrimSpace(strings.TrimPrefix(p.text(node), "import"))
	if spec == "" {
		return
	}
	p.addImport(Import{Path: spec, Kind: "package", Line: p.line(node)})
}

func extractKotlinClass(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	text := " " + p.headerText(node) + " "
	canonical := SymbolClass
	switch {
	case strings.Contains(text, " interface "):
		canonical = SymbolInterface
	case strings.Contains(text, " enum class "):
		canonical = SymbolEnum
	case strings.Contains(text, " data class "):
		canonical = SymbolRecord
	case strings.Contains(text, " annotation class "):
		canonical = SymbolInterface
	case strings.Contains(text, " object "):
		canonical = SymbolObject
	}
	qualified := joinContainer(container, name)
	p.addSymbol(KindType, canonical, node.Type(), name, node, container, "", kotlinVisibility(p, node, exported))
	extractKotlinConstructorProperties(p, node, qualified)
	extractKotlinHeritage(p, node)
	body := childField(node, "body")
	if body == nil {
		body = childOfType(node, "class_body")
	}
	extractKotlinBody(p, body, qualified, exported)
}

func extractKotlinBody(p *tsParser, body *sitter.Node, container string, exported bool) {
	if body == nil {
		return
	}
	for _, child := range namedChildren(body) {
		switch child.Type() {
		case "function_declaration":
			extractKotlinFunction(p, child, container, exported)
		case "property_declaration":
			name := identifierText(p, child)
			if name != "" {
				p.addSymbol(KindVar, SymbolProperty, child.Type(), name, child, container, "", kotlinVisibility(p, child, exported))
			}
		case "companion_object":
			p.addSymbol(KindType, SymbolObject, child.Type(), "Companion", child, container, "", "public")
			extractKotlinBody(p, childField(child, "body"), joinContainer(container, "Companion"), exported)
		case "class_declaration", "object_declaration":
			if child != nil {
				extractKotlinStatement(p, child, container, exported)
			}
		}
	}
}

func extractKotlinFunction(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	match := p.addSymbol(KindFunc, SymbolFunction, node.Type(), name, node, container,
		container, kotlinVisibility(p, node, exported))
	collectKotlinCalls(p, node, match)
	if kotlinHasTestAnnotation(p, node) {
		p.addTest(match, TestUnit, "junit")
	}
	if name == "main" && container == "" {
		p.addEntrypoint(match)
	}
}

func extractKotlinConstructorProperties(p *tsParser, node *sitter.Node, container string) {
	constructor := childOfType(node, "primary_constructor")
	if constructor == nil {
		return
	}
	for _, child := range namedChildren(constructor) {
		if child.Type() != "class_parameter" {
			continue
		}
		text := " " + p.text(child) + " "
		if !strings.Contains(text, " val ") && !strings.Contains(text, " var ") {
			continue
		}
		name := identifierText(p, child)
		if name != "" {
			p.addSymbol(KindVar, SymbolProperty, child.Type(), name, child, container, container, "public")
		}
	}
}

func extractKotlinHeritage(p *tsParser, node *sitter.Node) {
	for _, child := range namedChildren(node) {
		if child.Type() != "delegation_specifier" {
			continue
		}
		targets := namedChildren(child)
		for _, target := range targets {
			text := p.text(target)
			if target.Type() == "constructor_invocation" {
				if typ := childOfType(target, "user_type", "type_identifier"); typ != nil {
					text = p.text(typ)
				}
			}
			name := lastSegment(text)
			if name == "" {
				continue
			}
			p.addRelation(rawRelation{Kind: EdgeExtends, Target: name, Line: p.line(child), Column: p.column(child)})
			p.addReference(rawGenericReference{Name: name, Line: p.line(child), Column: p.column(child), Kind: EdgeExtends})
		}
	}
}

func collectKotlinCalls(p *tsParser, node *sitter.Node, caller Match) {
	stack := []tsNode{{node: node}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.node == nil || !current.node.IsNamed() {
			continue
		}
		if current.depth > 0 {
			switch current.node.Type() {
			case "function_declaration", "class_declaration", "object_declaration":
				continue
			}
		}
		if current.node.Type() == "call_expression" {
			callee := ""
			for _, child := range namedChildren(current.node) {
				if child.Type() == "simple_identifier" || child.Type() == "navigation_expression" {
					callee = p.text(child)
					break
				}
			}
			name := lastSegment(callee)
			if name != "" {
				p.addCall(rawGenericCall{
					Name: name, Qualifier: qualifierText(callee),
					Line: p.line(current.node), Column: p.column(current.node),
					CallerName: caller.Name, CallerLine: caller.Line,
					CallerEndLine: caller.EndLine, CallerKind: caller.Kind,
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

func kotlinVisibility(p *tsParser, node *sitter.Node, exported bool) string {
	text := " " + p.headerText(node) + " "
	switch {
	case strings.Contains(text, " private "):
		return "private"
	case strings.Contains(text, " protected "):
		return "protected"
	case strings.Contains(text, " internal "):
		return "internal"
	case strings.Contains(text, " public "), exported:
		return "public"
	default:
		return "public"
	}
}

func kotlinHasTestAnnotation(p *tsParser, node *sitter.Node) bool {
	return strings.Contains(p.headerText(node), "@Test")
}
