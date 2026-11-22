//go:build cgo

package ast

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

func pythonGrammar() *sitter.Language { return python.GetLanguage() }

// tsPythonCapabilities declares the facts the Python adapter actually emits.
// Module exports are implicit in Python (a public name is importable), so
// Exports is true while no synthetic export declaration is created.
func tsPythonCapabilities() Capabilities {
	return Capabilities{
		Symbols: true, Imports: true, Exports: true, References: true,
		Calls: true, Inheritance: true, Tests: true, Entrypoints: true,
	}
}

func extractPython(p *tsParser) error {
	for _, child := range namedChildren(p.root) {
		extractPythonStatement(p, child, "", "")
	}
	return nil
}

// extractPythonStatement handles one module- or class-body statement.
//
// container is the enclosing class (or nested function) qualified name. It is
// empty at module scope. Python has no package clause; a symbol's qualified
// name is therefore the lexical container, not a module path.
func extractPythonStatement(p *tsParser, node *sitter.Node, container, class string) {
	if node == nil {
		return
	}
	switch node.Type() {
	case "decorated_definition":
		definition := childField(node, "definition")
		if definition == nil {
			definition = childOfType(node, "function_definition", "class_definition")
		}
		extractPythonStatement(p, definition, container, class)
	case "class_definition":
		extractPythonClass(p, node, container)
	case "function_definition":
		extractPythonFunction(p, node, container, class)
	case "import_statement":
		extractPythonImport(p, node)
	case "import_from_statement":
		extractPythonFromImport(p, node)
	case "expression_statement":
		extractPythonAssignment(p, node, container, class)
	case "if_statement":
		if pythonMainGuard(p, node) {
			p.addEntrypoint(Match{
				Kind: KindFunc, Canonical: SymbolFunction, NativeKind: "main_guard",
				Language: p.language, Name: "__main__", QualifiedName: p.qualified("", "__main__"),
				Path: p.path, Line: p.line(node), EndLine: p.endLine(node),
				Column: p.column(node), Test: IsTestPath(p.path),
			})
		}
	}
}

func extractPythonClass(p *tsParser, node *sitter.Node, container string) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	qualified := joinContainer(container, name)
	p.addSymbol(KindType, SymbolClass, node.Type(), name, node, container, "",
		pythonVisibility(name))
	extractPythonBases(p, node, name)
	if body := childField(node, "body"); body != nil {
		for _, child := range namedChildren(body) {
			extractPythonStatement(p, child, qualified, name)
		}
	}
}

func extractPythonBases(p *tsParser, node *sitter.Node, className string) {
	bases := childField(node, "superclasses")
	if bases == nil {
		return
	}
	for _, base := range namedChildren(bases) {
		if base.Type() == "keyword_argument" {
			// metaclass=... and other keywords are not base classes.
			continue
		}
		target := lastSegment(p.text(base))
		if target == "" {
			continue
		}
		p.addRelation(rawRelation{
			Kind: EdgeExtends, Target: target, Source: className,
			Line: p.line(base), Column: p.column(base),
		})
		p.addReference(rawGenericReference{
			Name: target, Qualifier: qualifierText(p.text(base)),
			Line: p.line(base), Column: p.column(base), Kind: EdgeExtends,
		})
	}
}

func extractPythonFunction(p *tsParser, node *sitter.Node, container, class string) {
	name := identifierText(p, node)
	if name == "" {
		return
	}
	kind := KindFunc
	canonical := SymbolFunction
	receiver := ""
	if container != "" {
		kind = KindMethod
		canonical = SymbolMethod
		receiver = class
		if name == "__init__" {
			canonical = SymbolConstructor
		}
	}
	match := p.addSymbol(kind, canonical, node.Type(), name, node, container,
		receiver, pythonVisibility(name))
	extractPythonTypeReferences(p, node)
	if body := childField(node, "body"); body != nil {
		collectPythonCalls(p, body, match, container)
		for _, child := range namedChildren(body) {
			switch child.Type() {
			case "function_definition", "class_definition", "decorated_definition":
				// A nested declaration is its own symbol; its calls belong to
				// the nested scope, not to the enclosing function.
				extractPythonStatement(p, child, joinContainer(container, name), name)
			}
		}
	}
	if pythonIsTestFunction(name) {
		framework := "pytest"
		if class != "" && strings.HasPrefix(class, "Test") {
			framework = "unittest"
		}
		p.addTest(match, TestUnit, framework)
	}
}

func extractPythonAssignment(p *tsParser, node *sitter.Node, container, class string) {
	assignment := childOfType(node, "assignment")
	if assignment == nil {
		return
	}
	left := childField(assignment, "left")
	if left == nil {
		return
	}
	names := pythonAssignedNames(p, left)
	for _, name := range names {
		canonical := SymbolVariable
		kind := KindVar
		if pythonIsConstant(name) {
			canonical = SymbolConstant
			kind = KindConst
		}
		p.addSymbol(kind, canonical, assignment.Type(), name, assignment, container, "",
			pythonVisibility(name))
	}
	if typ := childField(assignment, "type"); typ != nil {
		collectPythonTypeRefs(p, typ)
	}
}

func pythonAssignedNames(p *tsParser, node *sitter.Node) []string {
	switch node.Type() {
	case "identifier":
		return []string{p.text(node)}
	case "pattern_list", "tuple", "list":
		var names []string
		for _, child := range namedChildren(node) {
			names = append(names, pythonAssignedNames(p, child)...)
		}
		return names
	default:
		return nil
	}
}

func extractPythonImport(p *tsParser, node *sitter.Node) {
	for _, child := range namedChildren(node) {
		switch child.Type() {
		case "dotted_name", "aliased_import":
			spec := p.text(child)
			alias := ""
			if child.Type() == "aliased_import" {
				if name := childField(child, "name"); name != nil {
					spec = p.text(name)
				}
				if aliasNode := childField(child, "alias"); aliasNode != nil {
					alias = p.text(aliasNode)
				}
			}
			if spec == "" {
				continue
			}
			p.addImport(Import{Path: spec, Alias: alias, Kind: "module", Line: p.line(child)})
		}
	}
}

func extractPythonFromImport(p *tsParser, node *sitter.Node) {
	module := ""
	if moduleNode := childField(node, "module_name"); moduleNode != nil {
		module = p.text(moduleNode)
	}
	var names []string
	alias := ""
	for _, child := range namedChildren(node) {
		if child == nil || child.Type() == "relative_import" || child.Type() == "dotted_name" && p.text(child) == module {
			continue
		}
		switch child.Type() {
		case "dotted_name", "identifier":
			names = append(names, lastSegment(p.text(child)))
		case "aliased_import":
			name := ""
			if nameNode := childField(child, "name"); nameNode != nil {
				name = lastSegment(p.text(nameNode))
			}
			if name != "" {
				names = append(names, name)
			}
			if aliasNode := childField(child, "alias"); aliasNode != nil && alias == "" {
				alias = p.text(aliasNode)
			}
		case "wildcard_import":
			names = append(names, "*")
		}
	}
	if module == "" {
		return
	}
	p.addImport(Import{Path: module, Names: names, Alias: alias, Kind: "from", Line: p.line(node)})
}

func collectPythonCalls(p *tsParser, body *sitter.Node, caller Match, container string) {
	if body == nil {
		return
	}
	stack := []tsNode{{node: body}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.node == nil || !current.node.IsNamed() {
			continue
		}
		if current.depth > 0 {
			switch current.node.Type() {
			case "function_definition", "class_definition", "decorated_definition", "lambda":
				continue
			}
		}
		if current.node.Type() == "call" {
			fn := childField(current.node, "function")
			if fn != nil {
				callee := p.text(fn)
				name := lastSegment(callee)
				if name != "" {
					qualifier := qualifierText(callee)
					if container != "" && (strings.HasPrefix(callee, "self.") || strings.HasPrefix(callee, "cls.")) {
						// self.method() is a member call. The class is the
						// qualifier; leaving it empty would let a same-named
						// method in another class resolve incorrectly.
						qualifier = lastSegment(container)
					}
					p.addCall(rawGenericCall{
						Name: name, Qualifier: qualifier,
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

// extractPythonTypeReferences collects annotation identifiers as references.
// It is deliberately bounded to annotations; a bare value read is not a
// reference, and collecting every identifier would manufacture edges.
func extractPythonTypeReferences(p *tsParser, fn *sitter.Node) {
	if typ := childField(fn, "return_type"); typ != nil {
		collectPythonTypeRefs(p, typ)
	}
	params := childField(fn, "parameters")
	if params == nil {
		return
	}
	for _, param := range namedChildren(params) {
		if typ := childField(param, "type"); typ != nil {
			collectPythonTypeRefs(p, typ)
		}
	}
}

func collectPythonTypeRefs(p *tsParser, node *sitter.Node) {
	if node == nil {
		return
	}
	p.walkNode(node, func(child *sitter.Node) {
		switch child.Type() {
		case "identifier":
			name := p.text(child)
			if name != "" {
				p.addReference(rawGenericReference{
					Name: name, Line: p.line(child), Column: p.column(child),
					Kind: EdgeReferences,
				})
			}
		}
	})
}

// walkNode visits node and its named descendants in source order.
func (p *tsParser) walkNode(node *sitter.Node, fn func(*sitter.Node)) {
	if node == nil {
		return
	}
	stack := []*sitter.Node{node}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current == nil || !current.IsNamed() {
			continue
		}
		fn(current)
		children := namedChildren(current)
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, children[i])
		}
	}
}

func pythonMainGuard(p *tsParser, node *sitter.Node) bool {
	condition := childField(node, "condition")
	if condition == nil {
		return false
	}
	text := p.text(condition)
	return strings.Contains(text, "__name__") && strings.Contains(text, "__main__")
}

func pythonVisibility(name string) string {
	if strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__") {
		return "public"
	}
	if strings.HasPrefix(name, "_") {
		return "private"
	}
	return "public"
}

func pythonIsTestFunction(name string) bool {
	return name == "test" || strings.HasPrefix(name, "test_")
}

func pythonIsConstant(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' {
			return false
		}
	}
	return true
}
