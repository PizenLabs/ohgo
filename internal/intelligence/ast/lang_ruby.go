//go:build cgo

package ast

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/ruby"
)

func rubyGrammar() *sitter.Language { return ruby.GetLanguage() }

func extractRuby(p *tsParser) error {
	for _, child := range namedChildren(p.root) {
		extractRubyStatement(p, child, "", false)
	}
	return nil
}

func extractRubyStatement(p *tsParser, node *sitter.Node, container string, exported bool) {
	if node == nil {
		return
	}
	switch node.Type() {
	case "module":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		qualified := joinContainer(container, name)
		p.addSymbol(KindPackage, SymbolModule, node.Type(), name, node, container, "", "public")
		extractRubyBody(p, childField(node, "body"), qualified, exported)
	case "class":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		qualified := joinContainer(container, name)
		p.addSymbol(KindType, SymbolClass, node.Type(), name, node, container, "", rubyVisibility(p, node, exported))
		if superclass := childField(node, "superclass"); superclass != nil {
			for _, child := range namedChildren(superclass) {
				addRubyRelation(p, superclass, child, EdgeExtends)
			}
		}
		extractRubyBody(p, childField(node, "body"), qualified, exported)
	case "method":
		extractRubyMethod(p, node, container, exported)
	case "singleton_method":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		match := p.addSymbol(KindMethod, SymbolMethod, node.Type(), name, node, container,
			container, "public")
		collectRubyCalls(p, node, match)
		if IsTestPath(p.path) && strings.HasPrefix(name, "test_") {
			p.addTest(match, TestUnit, "minitest")
		}
	case "assignment":
		left := childField(node, "left")
		if left != nil && left.Type() == "constant" {
			p.addSymbol(KindConst, SymbolConstant, node.Type(), p.text(left), node, container, "", "public")
		}
	case "call":
		extractRubyTopLevelCall(p, node, container)
		for _, child := range namedChildren(node) {
			if child.Type() == "block" || child.Type() == "do_block" {
				extractRubyBody(p, child, container, exported)
			}
		}
	case "if", "unless":
		text := p.text(node)
		if strings.Contains(text, "__FILE__") && (strings.Contains(text, "$0") ||
			strings.Contains(text, "$PROGRAM_NAME")) {
			p.addEntrypoint(Match{
				Kind: KindFunc, Canonical: SymbolFunction, NativeKind: "script_main",
				Language: p.language, Name: "<script>", QualifiedName: p.qualified("", "<script>"),
				Path: p.path, Line: p.line(node), Column: p.column(node),
				EndLine: p.endLine(node),
			})
		}
		if body := childField(node, "body"); body != nil {
			for _, child := range namedChildren(body) {
				extractRubyStatement(p, child, container, exported)
			}
		}
	}
}

func extractRubyBody(p *tsParser, body *sitter.Node, container string, exported bool) {
	if body == nil {
		return
	}
	for _, child := range namedChildren(body) {
		extractRubyStatement(p, child, container, exported)
	}
}

func extractRubyMethod(p *tsParser, node *sitter.Node, container string, exported bool) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	visibility := rubyVisibilityForMethod(p, node, exported)
	match := p.addSymbol(KindMethod, SymbolMethod, node.Type(), name, node, container,
		container, visibility)
	collectRubyCalls(p, node, match)
	if IsTestPath(p.path) && strings.HasPrefix(name, "test_") {
		p.addTest(match, TestUnit, "minitest")
	}
}

func extractRubyTopLevelCall(p *tsParser, node *sitter.Node, container string) {
	method := childField(node, "method")
	if method == nil {
		return
	}
	switch p.text(method) {
	case "require", "require_relative", "load":
		if spec := rubyFirstStringArgument(p, node); spec != "" {
			kind := "require"
			if p.text(method) == "require_relative" {
				kind = "relative"
			}
			p.addImport(Import{Path: spec, Kind: kind, Line: p.line(node)})
		}
	case "include", "extend", "prepend":
		args := childField(node, "arguments")
		if args != nil {
			for _, child := range namedChildren(args) {
				if child.Type() != "constant" && child.Type() != "identifier" {
					continue
				}
				target := lastSegment(p.text(child))
				if target != "" {
					p.addRelation(rawRelation{Kind: EdgeImplements, Target: target,
						Source: container, Line: p.line(node), Column: p.column(node)})
				}
			}
		}
	case "describe", "context", "it", "specify":
		if label := rubyFirstStringArgument(p, node); label != "" || p.text(method) == "describe" {
			p.addTest(Match{
				Kind: KindFunc, Canonical: SymbolFunction, NativeKind: p.text(method),
				Language: p.language, Name: p.text(method) + ": " + label,
				QualifiedName: p.qualified("", p.text(method)+": "+label),
				Path:          p.path, Line: p.line(node), Column: p.column(node),
				EndLine: p.endLine(node), Test: true,
			}, TestUnit, "rspec")
		}
	}
}

func collectRubyCalls(p *tsParser, node *sitter.Node, caller Match) {
	stack := []tsNode{{node: node}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.node == nil || !current.node.IsNamed() {
			continue
		}
		if current.depth > 0 {
			switch current.node.Type() {
			case "method", "singleton_method", "class", "module":
				continue
			}
		}
		if current.node.Type() == "call" {
			method := childField(current.node, "method")
			if method != nil {
				name := p.text(method)
				if name != "" {
					p.addCall(rawGenericCall{
						Name: name, Qualifier: rubyReceiver(p, current.node),
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

func rubyReceiver(p *tsParser, call *sitter.Node) string {
	for _, child := range namedChildren(call) {
		if child.Type() == "identifier" || child.Type() == "constant" {
			return p.text(child)
		}
		if child.Type() == "self" {
			return ""
		}
	}
	return ""
}

func rubyFirstStringArgument(p *tsParser, call *sitter.Node) string {
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

func addRubyRelation(p *tsParser, clause, value *sitter.Node, kind EdgeKind) {
	target := lastSegment(p.text(value))
	if target == "" {
		return
	}
	p.addRelation(rawRelation{Kind: kind, Target: target, Line: p.line(clause), Column: p.column(clause)})
	p.addReference(rawGenericReference{Name: target, Line: p.line(value), Column: p.column(value), Kind: kind})
}

func rubyVisibility(p *tsParser, node *sitter.Node, exported bool) string {
	text := " " + p.text(node) + " "
	if strings.Contains(text, " private ") {
		return "private"
	}
	if exported {
		return "public"
	}
	return "public"
}

func rubyVisibilityForMethod(p *tsParser, node *sitter.Node, exported bool) string {
	body := node.Parent()
	if body == nil {
		return "public"
	}
	visibility := "public"
	for _, sibling := range namedChildren(body) {
		if sibling == nil {
			continue
		}
		if sibling.Equal(node) {
			break
		}
		if sibling.Type() != "call" {
			continue
		}
		method := childField(sibling, "method")
		if method == nil {
			continue
		}
		switch p.text(method) {
		case "private":
			visibility = "private"
		case "protected":
			visibility = "protected"
		case "public":
			visibility = "public"
		}
	}
	if exported {
		return "public"
	}
	return visibility
}
