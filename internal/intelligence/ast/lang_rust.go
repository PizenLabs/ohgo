//go:build cgo

package ast

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/rust"
)

func rustGrammar() *sitter.Language { return rust.GetLanguage() }

func extractRust(p *tsParser) error {
	for _, child := range namedChildren(p.root) {
		extractRustItem(p, child, "", "")
	}
	return nil
}

func extractRustItem(p *tsParser, node *sitter.Node, container, implType string) {
	if node == nil {
		return
	}
	switch node.Type() {
	case "use_declaration":
		spec := strings.TrimSpace(p.text(node))
		spec = strings.TrimPrefix(spec, "pub ")
		spec = strings.TrimPrefix(spec, "use ")
		spec = strings.TrimSuffix(spec, ";")
		p.addImport(Import{Path: strings.TrimSpace(spec), Kind: "module", Line: p.line(node)})
	case "mod_item":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		p.addSymbol(KindPackage, SymbolModule, node.Type(), name, node, container, "", rustVisibility(p, node))
		if body := childField(node, "body"); body != nil {
			for _, child := range namedChildren(body) {
				extractRustItem(p, child, joinContainer(container, name), "")
			}
		}
	case "struct_item":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		p.addSymbol(KindType, SymbolStruct, node.Type(), name, node, container, "", rustVisibility(p, node))
		if body := childField(node, "body"); body != nil {
			extractRustFields(p, body, name)
		}
	case "enum_item":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		p.addSymbol(KindType, SymbolEnum, node.Type(), name, node, container, "", rustVisibility(p, node))
		if body := childField(node, "body"); body != nil {
			for _, variant := range namedChildren(body) {
				if variant.Type() == "enum_variant" {
					if variantName := identifierText(p, variant); variantName != "" {
						p.addSymbol(KindConst, SymbolConstant, variant.Type(), variantName, variant,
							name, "", "public")
					}
				}
			}
		}
	case "trait_item":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		qualified := joinContainer(container, name)
		p.addSymbol(KindType, SymbolTrait, node.Type(), name, node, container, "", rustVisibility(p, node))
		if body := childField(node, "body"); body != nil {
			for _, child := range namedChildren(body) {
				if child.Type() == "function_signature_item" || child.Type() == "function_item" {
					extractRustFunction(p, child, qualified, name)
				}
			}
		}
	case "impl_item":
		typeNode := childField(node, "type")
		if typeNode == nil {
			return
		}
		typeName := lastSegment(p.text(typeNode))
		if typeName == "" {
			return
		}
		if traitNode := childField(node, "trait"); traitNode != nil {
			target := lastSegment(p.text(traitNode))
			if target != "" {
				p.addRelation(rawRelation{Kind: EdgeImplements, Target: target,
					Source: typeName, Line: p.line(node), Column: p.column(node)})
				p.addReference(rawGenericReference{Name: target, Line: p.line(traitNode),
					Column: p.column(traitNode), Kind: EdgeImplements})
			}
		}
		if body := childField(node, "body"); body != nil {
			for _, child := range namedChildren(body) {
				switch child.Type() {
				case "function_item", "function_signature_item":
					extractRustFunction(p, child, joinContainer(container, typeName), typeName)
				case "type_item":
					extractRustTypeItem(p, child, typeName)
				}
			}
		}
	case "function_item":
		extractRustFunction(p, node, container, implType)
	case "type_item":
		extractRustTypeItem(p, node, container)
	case "const_item", "static_item":
		name := identifierText(p, node)
		if name == "" {
			return
		}
		p.addSymbol(KindConst, SymbolConstant, node.Type(), name, node, container, "", rustVisibility(p, node))
	case "macro_definition":
		name := identifierText(p, node)
		if name != "" {
			p.addSymbol(KindFunc, SymbolFunction, node.Type(), name, node, container, "", "public")
		}
	}
}

func extractRustTypeItem(p *tsParser, node *sitter.Node, container string) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	p.addSymbol(KindType, SymbolTypeAlias, node.Type(), name, node, container, "", rustVisibility(p, node))
}

func extractRustFunction(p *tsParser, node *sitter.Node, container, receiver string) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	canonical := SymbolFunction
	kind := KindFunc
	if receiver != "" {
		canonical, kind = SymbolMethod, KindMethod
	}
	match := p.addSymbol(kind, canonical, node.Type(), name, node, container,
		receiver, rustVisibility(p, node))
	collectRustCalls(p, node, match)
	if rustHasTestAttribute(p, node) || (IsTestPath(p.path) && !strings.Contains(container, "impl")) {
		p.addTest(match, TestUnit, "rust-test")
	}
	if name == "main" && container == "" {
		p.addEntrypoint(match)
	}
}

func extractRustFields(p *tsParser, body *sitter.Node, container string) {
	for _, field := range namedChildren(body) {
		if field.Type() != "field_declaration" {
			continue
		}
		name := identifierText(p, field)
		if name != "" {
			p.addSymbol(KindVar, SymbolField, field.Type(), name, field, container, container, "public")
		}
	}
}

func collectRustCalls(p *tsParser, node *sitter.Node, caller Match) {
	stack := []tsNode{{node: node}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.node == nil || !current.node.IsNamed() {
			continue
		}
		if current.depth > 0 {
			switch current.node.Type() {
			case "function_item", "impl_item", "trait_item", "struct_item", "enum_item":
				continue
			}
		}
		if current.node.Type() == "call_expression" {
			function := childField(current.node, "function")
			if function != nil {
				name := lastSegment(p.text(function))
				if name != "" && name != "macro" {
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

func rustVisibility(p *tsParser, node *sitter.Node) string {
	if strings.Contains(" "+p.text(node)+" ", " pub ") {
		return "public"
	}
	return "private"
}

func rustHasTestAttribute(p *tsParser, node *sitter.Node) bool {
	for prev := node.PrevNamedSibling(); prev != nil; prev = prev.PrevNamedSibling() {
		if prev.Type() != "attribute_item" {
			break
		}
		if strings.Contains(p.text(prev), "test") {
			return true
		}
	}
	return false
}
