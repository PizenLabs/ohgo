package ast

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Relationship query bounds.
//
// References are a flat, static projection of the same parser pass that
// produces symbol coordinates. The bounds below keep one query from turning
// into an unbounded scan of the whole repository or an unbounded call-chain
// traversal.
const (
	// RefDefaultDepth is the default call-graph traversal depth.
	RefDefaultDepth = 1
	// RefMaxDepth is the hard call-graph depth ceiling.
	RefMaxDepth = 4
	// RefDefaultLimit is the default number of returned references or edges.
	RefDefaultLimit = 50
	// RefMaxLimit is the hard returned-row ceiling.
	RefMaxLimit = 200
	// RefMaxEdges is the hard call-edge ceiling for one traversal.
	RefMaxEdges = 400
	// RefWorkLimit bounds inspected references/edges for one query.
	RefWorkLimit = 200_000
	// refCancelCheckInterval bounds work between cancellation checks.
	refCancelCheckInterval = 256
	// refBuildCheckInterval bounds work between cancellation checks while
	// building the relationship projection.
	refBuildCheckInterval = 1024
)

// RefRelation selects the relationship a refs query answers.
type RefRelation string

// Supported relationship queries.
const (
	// RelationReferences asks where a symbol is referenced.
	RelationReferences RefRelation = "references"
	// RelationCallers asks who calls a function or method.
	RelationCallers RefRelation = "callers"
	// RelationCallees asks what a function or method calls.
	RelationCallees RefRelation = "callees"
)

// CallCertainty describes how an edge was derived.
type CallCertainty string

// Certainty values. Only statically provable edges are materialised; unknown
// dispatch is counted, never turned into an edge.
const (
	// CertaintyStatic is a direct syntactic call whose target declaration was
	// resolved in the same index and whose caller is the enclosing named
	// function.
	CertaintyStatic CallCertainty = "static"
	// CertaintyLexical is a statically resolved call inside a function
	// literal or a package-level closure. The target is proven, but the
	// caller attribution is lexical: the call executes when the closure runs,
	// not necessarily when the enclosing declaration runs.
	CertaintyLexical CallCertainty = "lexical"
)

// RefQuery is one bounded symbol-relationship request.
type RefQuery struct {
	// Symbol is the exact symbol or qualified name to resolve.
	Symbol string
	// Relation is references (default), callers, or callees.
	Relation RefRelation
	// Path scopes the declaration lookup to a file or directory.
	Path string
	// Depth is the call-graph traversal depth for callers/callees. It must be
	// zero for references.
	Depth int
	// Limit is the maximum returned references, the maximum returned edges
	// for a direct call query, or the maximum call-graph nodes for a
	// transitive callers/callees traversal.
	Limit int
	// EdgeLimit is the maximum returned call edges for a transitive
	// traversal. Zero uses 2 * Limit capped at RefMaxEdges.
	EdgeLimit int
	// Skip is the number of matching rows to skip for pagination:
	// references, direct call edges, or traversal call edges.
	Skip int
}

// RefTarget is one resolved declaration coordinate.
type RefTarget struct {
	// Path is the repository-relative file.
	Path string
	// Package is the Go package name.
	Package string
	// Symbol is the declaration name; a method is Receiver.Method.
	Symbol string
	// QualifiedName is the package-qualified coordinate.
	QualifiedName string
	// Kind is the declaration kind.
	Kind Kind
	// Line and EndLine bound the declaration.
	Line    int
	EndLine int
}

// RelationshipStats is a diagnostic summary of the resolved relationship
// projection. It is not a query result and carries no evidence semantics.
type RelationshipStats struct {
	// References is the number of resolved static reference sites.
	References int
	// Calls is the number of resolved static call edges.
	Calls int
	// UnknownCalls is the number of call sites whose target is not statically
	// provable.
	UnknownCalls int
	// DotImportFiles is the number of files whose dot imports create an
	// unqualified-name blind spot.
	DotImportFiles int
}

// RelationshipStats returns a point-in-time summary of the index's resolved
// relationship projection.
func (idx *Index) RelationshipStats() RelationshipStats {
	if idx == nil {
		return RelationshipStats{}
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.symbolGraph == nil {
		return RelationshipStats{}
	}
	dotFiles := map[string]bool{}
	for _, files := range idx.symbolGraph.dotAffected {
		for path := range files {
			dotFiles[path] = true
		}
	}
	return RelationshipStats{
		References:     len(idx.symbolGraph.refs),
		Calls:          len(idx.symbolGraph.calls),
		UnknownCalls:   idx.symbolGraph.unknownTotal,
		DotImportFiles: len(dotFiles),
	}
}

// ReferenceSite is one static syntactic reference to a target declaration.
type ReferenceSite struct {
	// Path is the repository-relative file containing the reference.
	Path string
	// Line and Column locate the reference.
	Line   int
	Column int
	// Qualified reports a package-qualified reference (pkg.Symbol) rather
	// than an unqualified one (Symbol).
	Qualified bool
	// Call reports that the reference is the statically resolved callee of a
	// call expression.
	Call bool
	// Test reports a reference in a _test.go file.
	Test bool

	targetKey string
}

// CallNode is one function, method, or package-initializer node in a call
// graph result.
type CallNode struct {
	// Path is the repository-relative file declaring the node, or the call
	// site file for a package initializer.
	Path string
	// Package is the Go package name.
	Package string
	// Symbol is the declaration name; a package initializer is rendered as
	// "<package init>".
	Symbol string
	// Kind is the declaration kind. It is empty for a package initializer.
	Kind Kind
	// Line and EndLine bound the declaration. A package initializer uses the
	// call site line for both.
	Line    int
	EndLine int
	// Depth is the traversal distance from the focal node.
	Depth int
	// CallerKind is "function", "method", or "package_initializer".
	CallerKind string

	key string
}

// CallEdge is one statically provable direct call.
type CallEdge struct {
	// Caller is the calling function, method, or package initializer.
	Caller CallNode
	// Callee is the called function or method.
	Callee CallNode
	// CallPath and CallLine are the exact call-site witness.
	CallPath string
	CallLine int
	// Column is the 1-based call-site column.
	Column int
	// InClosure reports a call lexically inside a function literal. It is
	// still a direct static call, but it executes when the closure runs.
	InClosure bool
	// Test reports a call site in a _test.go file.
	Test bool
	// Certainty distinguishes a direct named-function call from a
	// statically resolved call inside a closure.
	Certainty CallCertainty

	callerKey string
	calleeKey string
}

// UnknownSummary states what the static projection cannot prove.
type UnknownSummary struct {
	// DotImportFiles counts files whose dot imports could capture an
	// unqualified name for the queried package, either because the file is in
	// that package or because it dot-imports it.
	DotImportFiles int
	// AmbiguousUses is a repository-wide count of references whose target
	// resolved to more than one declaration. It is disclosure, not a claim
	// that the queried target is affected.
	AmbiguousUses int
	// UnresolvedUses is a repository-wide count of module-local references
	// whose target package was absent from the index.
	UnresolvedUses int
	// DynamicCalls counts call sites that are not statically resolvable
	// (method dispatch, function values, unresolved targets).
	DynamicCalls int
	// PackageInitializerCalls counts static calls made from package-level
	// initializers.
	PackageInitializerCalls int
	// Reasons aggregates the reasons behind DynamicCalls.
	Reasons map[string]int
}

// RefResult is the deterministic answer to one RefQuery.
type RefResult struct {
	// Module is the go.mod module path the index was built against.
	Module string
	// Relation is the queried relationship.
	Relation RefRelation
	// Symbol is the selector the caller supplied.
	Symbol string
	// Target is the resolved declaration when Found is true.
	Target RefTarget
	// Found reports whether the symbol resolved exactly once.
	Found bool
	// Ambiguous reports that the symbol matched more than one declaration.
	Ambiguous bool
	// Candidates lists the declaration candidates when Ambiguous is true.
	Candidates []RefTarget
	// Unsupported reports a relation the static model cannot answer.
	Unsupported bool
	// UnsupportedReason explains Unsupported.
	UnsupportedReason string
	// Depth is the effective call-graph depth.
	Depth int
	// References are the returned reference sites.
	References []ReferenceSite
	// Nodes and Edges are the returned call-graph projection.
	Nodes []CallNode
	Edges []CallEdge
	// TotalReferences or TotalEdges is the number of matching rows before
	// pagination, within the work bound.
	TotalReferences int
	TotalEdges      int
	// Truncated reports that a bound stopped the query.
	Truncated bool
	// Reason names the bound: limit, edge_limit, node_limit, depth, or
	// work_limit.
	Reason string
	// Work is the number of inspected references/edges.
	Work int
	// ParseFailures counts files or directories the index could not inspect.
	ParseFailures int
	// Complete reports that no *observed static* blind spot affects this
	// answer. It is not a claim that no runtime caller exists: reflection,
	// external processes, and code outside the indexed source are outside this
	// model.
	Complete bool
	// Unknown states the blind spots that were observed.
	Unknown UnknownSummary
}

// rawReference is one syntactic reference found during a parse.
type rawReference struct {
	Name         string
	Qualifier    string
	Line         int
	Column       int
	SelectorBase bool
}

// rawCallKind is the syntactic shape of a call target.
type rawCallKind string

const (
	rawCallIdent      rawCallKind = "ident"
	rawCallSelector   rawCallKind = "selector"
	rawCallMethodExpr rawCallKind = "method_expr"
	rawCallFuncLit    rawCallKind = "func_lit"
	rawCallType       rawCallKind = "type_expr"
	rawCallPointer    rawCallKind = "pointer_expr"
	rawCallDynamic    rawCallKind = "dynamic_expr"
)

// rawCall is one call site found during a parse.
type rawCall struct {
	Kind          rawCallKind
	Qualifier     string
	Receiver      string
	Name          string
	Line          int
	Column        int
	FunLine       int
	FunColumn     int
	pos           token.Pos
	CallerName    string
	CallerLine    int
	CallerEndLine int
	CallerMethod  bool
	InClosure     bool
	PackageInit   bool
	Anonymous     bool
	// Local and LocalType record that the syntactic callee names a
	// function-local object rather than a package-level declaration. A local
	// value is a function value; a local type makes the expression a
	// conversion.
	Local     bool
	LocalType bool
}

// tokenRange is one function-literal extent.
type tokenRange struct {
	start token.Pos
	end   token.Pos
}

// collectFileRelationships extracts the syntactic reference and call sites
// from the same AST the symbol parse already built.
//
// It deliberately keeps only stable coordinates and syntactic shapes. Target
// resolution happens after every file is parsed, when the declaration table is
// complete; a per-file parse cannot know another file's declarations.
func collectFileRelationships(fset *token.FileSet, file *ast.File) ([]rawReference, []rawCall) {

	var refs []rawReference
	selectorBases := map[*ast.Ident]bool{}
	skip := map[*ast.Ident]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.File:
			skip[n.Name] = true
		case *ast.ImportSpec:
			if n.Name != nil {
				skip[n.Name] = true
			}
		case *ast.Field:
			for _, name := range n.Names {
				skip[name] = true
			}
		case *ast.TypeSpec:
			skip[n.Name] = true
		case *ast.ValueSpec:
			for _, name := range n.Names {
				skip[name] = true
			}
		case *ast.FuncDecl:
			skip[n.Name] = true
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				for _, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						skip[id] = true
					}
				}
			}
		case *ast.RangeStmt:
			if n.Tok == token.DEFINE {
				if id, ok := n.Key.(*ast.Ident); ok {
					skip[id] = true
				}
				if id, ok := n.Value.(*ast.Ident); ok {
					skip[id] = true
				}
			}
		case *ast.LabeledStmt:
			skip[n.Label] = true
		case *ast.BranchStmt:
			if n.Label != nil {
				skip[n.Label] = true
			}
		case *ast.SelectorExpr:
			// The selected member is not an unqualified name reference. A
			// selector whose base is unresolved may be package-qualified;
			// resolution later discards it when the base is not an import.
			skip[n.Sel] = true
			if base, ok := n.X.(*ast.Ident); ok {
				selectorBases[base] = true
				local := base.Obj != nil && (file.Scope == nil || file.Scope.Objects[base.Name] != base.Obj)
				// An unresolved base may be an import or a cross-file
				// declaration. A resolved package-level base may be shadowed
				// by an import of the same name. A local base is a value
				// selector, never a package qualifier. Resolution discards a
				// candidate whose qualifier is not an import.
				if !local {
					pos := fset.Position(n.Pos())
					refs = append(refs, rawReference{
						Name:      n.Sel.Name,
						Qualifier: base.Name,
						Line:      pos.Line,
						Column:    pos.Column,
					})
				}
			}
		case *ast.KeyValueExpr:
			// A composite-literal key is a field name or a map key. Without
			// type information the two cannot be separated, so neither is
			// reported as a symbol reference. This is a documented loss.
			if id, ok := n.Key.(*ast.Ident); ok {
				skip[id] = true
			}
		}
		return true
	})

	ast.Inspect(file, func(node ast.Node) bool {
		id, ok := node.(*ast.Ident)
		if !ok || skip[id] || id.Name == "_" {
			return true
		}
		pos := fset.Position(id.Pos())
		switch {
		case id.Obj == nil:
			// Unresolved in this file: a cross-file package-level name, a
			// predeclared name, or a dot-imported name. Resolution against the
			// package table happens after every file is parsed.
			refs = append(refs, rawReference{
				Name:         id.Name,
				Line:         pos.Line,
				Column:       pos.Column,
				SelectorBase: selectorBases[id],
			})
		case file.Scope != nil && file.Scope.Objects[id.Name] == id.Obj:
			// A same-file package-level declaration. Local variables and
			// parameters are already bound to another object and are not
			// references to a package symbol.
			refs = append(refs, rawReference{
				Name:         id.Name,
				Line:         pos.Line,
				Column:       pos.Column,
				SelectorBase: selectorBases[id],
			})
		}
		return true
	})

	var calls []rawCall
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		callerName := funcDeclName(fd)
		callerLine := fset.Position(fd.Pos()).Line
		callerEnd := fset.Position(fd.End()).Line
		callerMethod := fd.Recv != nil && len(fd.Recv.List) > 0

		var literals []tokenRange
		var pending []rawCall
		ast.Inspect(fd.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.FuncLit:
				literals = append(literals, tokenRange{start: n.Pos(), end: n.End()})
			case *ast.CallExpr:
				raw := rawCallFromExpr(fset, n, callerName, callerLine, callerEnd, callerMethod, false, false)
				raw.Local, raw.LocalType = callTargetScope(file, n.Fun)
				pending = append(pending, raw)
			}
			return true
		})
		for i := range pending {
			pending[i].InClosure = inAnyRange(pending[i].pos, literals)
		}
		calls = append(calls, pending...)
	}

	// Package-level initializers are not function bodies. A call there has a
	// file/line witness but no enclosing function declaration, so it is
	// labelled as a package initializer rather than attributed to an
	// arbitrary function.
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		var literals []tokenRange
		var pending []rawCall
		ast.Inspect(gen, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.FuncLit:
				literals = append(literals, tokenRange{start: n.Pos(), end: n.End()})
			case *ast.CallExpr:
				raw := rawCallFromExpr(fset, n, "", 0, 0, false, false, false)
				raw.Local, raw.LocalType = callTargetScope(file, n.Fun)
				pending = append(pending, raw)
			}
			return true
		})
		for i := range pending {
			// A call inside a package-level function literal runs when the
			// closure is invoked, not during package initialization. It is
			// still a static call, but its caller is anonymous.
			anonymous := inAnyRange(pending[i].pos, literals)
			pending[i].Anonymous = anonymous
			pending[i].PackageInit = !anonymous
			pending[i].InClosure = anonymous
		}
		calls = append(calls, pending...)
	}

	return refs, calls
}

// funcDeclName renders a function or method declaration the same way the
// symbol index does: a method is Receiver.Method.
func funcDeclName(fd *ast.FuncDecl) string {
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		if receiver := typeName(fd.Recv.List[0].Type); receiver != "" {
			return receiver + "." + fd.Name.Name
		}
	}
	return fd.Name.Name
}

// rawCallFromExpr projects one call expression into its syntactic target
// shape. It never resolves names; that requires the complete declaration
// table.
func rawCallFromExpr(fset *token.FileSet, call *ast.CallExpr, caller string, callerLine, callerEnd int, callerMethod, inClosure, packageInit bool) rawCall {
	fun := unwrapCallFun(call.Fun)
	pos := fset.Position(call.Pos())
	funPos := fset.Position(fun.Pos())
	out := rawCall{
		Line:          pos.Line,
		pos:           call.Pos(),
		Column:        pos.Column,
		FunLine:       funPos.Line,
		FunColumn:     funPos.Column,
		CallerName:    caller,
		CallerLine:    callerLine,
		CallerEndLine: callerEnd,
		CallerMethod:  callerMethod,
		InClosure:     inClosure,
		PackageInit:   packageInit,
	}
	switch f := fun.(type) {
	case *ast.Ident:
		out.Kind = rawCallIdent
		out.Name = f.Name
	case *ast.SelectorExpr:
		out.Name = f.Sel.Name
		if qualifier, receiver, ok := selectorReceiver(f); ok {
			if receiver != "" {
				out.Kind = rawCallMethodExpr
				out.Qualifier = qualifier
				out.Receiver = receiver
				break
			}
			out.Kind = rawCallSelector
			out.Qualifier = qualifier
			break
		}
		out.Kind = rawCallDynamic
	case *ast.FuncLit:
		out.Kind = rawCallFuncLit
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType,
		*ast.InterfaceType, *ast.StructType:
		// A composite type expression here is a conversion, not a call.
		out.Kind = rawCallType
	case *ast.StarExpr:
		// (*T)(x) is a pointer conversion; (*f)() is a call through a
		// function value. A composite pointer target is a conversion without
		// needing a declaration lookup.
		if isTypeExpr(f.X) {
			out.Kind = rawCallType
			break
		}
		out.Kind = rawCallPointer
		if qualifier, name, ok := qualifiedTypeName(f.X); ok {
			out.Qualifier = qualifier
			out.Name = name
		}
	default:
		// f()(), (<-ch)(), and other computed callees are function values.
		out.Kind = rawCallDynamic
	}
	return out
}

// isTypeExpr reports a composite type expression, which can only be a
// conversion target. A bare identifier is deliberately not classified: it
// may be a type or a function value and the declaration table decides.
func isTypeExpr(expr ast.Expr) bool {
	for {
		switch e := expr.(type) {
		case *ast.ParenExpr:
			expr = e.X
		case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType,
			*ast.InterfaceType, *ast.StructType, *ast.StarExpr:
			return true
		default:
			return false
		}
	}
}

// qualifiedTypeName extracts the spelling of a possibly package-qualified
// type/value name from a pointer operand.
func qualifiedTypeName(expr ast.Expr) (qualifier, name string, ok bool) {
	for {
		switch e := expr.(type) {
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		default:
			goto done
		}
	}
done:
	switch e := expr.(type) {
	case *ast.Ident:
		return "", e.Name, true
	case *ast.SelectorExpr:
		if base, ok := e.X.(*ast.Ident); ok {
			return base.Name, e.Sel.Name, true
		}
	}
	return "", "", false
}

// unwrapCallFun removes parentheses and generic instantiation so the callee
// shape is visible. A generic call Foo[T](x) resolves to the same target as
// Foo(x).
func unwrapCallFun(expr ast.Expr) ast.Expr {
	for {
		switch e := expr.(type) {
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		default:
			return expr
		}
	}
}

// selectorReceiver distinguishes a value selector (x.M) from a method
// expression (T.M, (*T).M, pkg.T.M, (*pkg.T).M). receiver is empty for a value
// selector; the resolver decides whether the qualifier names a type.
func selectorReceiver(sel *ast.SelectorExpr) (qualifier, receiver string, ok bool) {
	base := sel.X
	for {
		switch e := base.(type) {
		case *ast.IndexExpr:
			base = e.X
		case *ast.IndexListExpr:
			base = e.X
		default:
			goto unwrapped
		}
	}
unwrapped:
	if id, ok := base.(*ast.Ident); ok {
		return id.Name, "", true
	}
	inner, ok := base.(*ast.SelectorExpr)
	if !ok {
		// (*T).M: peel the pointer and parentheses around the receiver type.
		expr := base
		for {
			switch e := expr.(type) {
			case *ast.ParenExpr:
				expr = e.X
			case *ast.StarExpr:
				expr = e.X
			default:
				goto done
			}
		}
	done:
		if id, ok := expr.(*ast.Ident); ok {
			return "", id.Name, true
		}
		return "", "", false
	}
	baseIdent, ok := inner.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	return baseIdent.Name, inner.Sel.Name, true
}

// callTargetScope reports whether the syntactic callee names a function-local
// object and whether that object is a type. This is what keeps a local
// variable named like a package function from being resolved as a package
// call.
func callTargetScope(file *ast.File, expr ast.Expr) (local, localType bool) {
	fun := unwrapCallFun(expr)
	switch f := fun.(type) {
	case *ast.Ident:
		return identScope(file, f)
	case *ast.SelectorExpr:
		if base, ok := f.X.(*ast.Ident); ok {
			return identScope(file, base)
		}
		if inner, ok := f.X.(*ast.SelectorExpr); ok {
			if base, ok := inner.X.(*ast.Ident); ok {
				return identScope(file, base)
			}
		}
		if id, ok := pointerOperandIdent(f.X); ok {
			return identScope(file, id)
		}
	case *ast.StarExpr:
		if id, ok := pointerOperandIdent(f.X); ok {
			return identScope(file, id)
		}
	}
	return false, false
}

func identScope(file *ast.File, id *ast.Ident) (local, localType bool) {
	if id == nil || id.Obj == nil {
		return false, false
	}
	if file.Scope != nil && file.Scope.Objects[id.Name] == id.Obj {
		return false, false
	}
	return true, id.Obj.Kind == ast.Typ
}

func pointerOperandIdent(expr ast.Expr) (*ast.Ident, bool) {
	for {
		switch e := expr.(type) {
		case *ast.ParenExpr:
			expr = e.X
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		default:
			goto done
		}
	}
done:
	id, ok := expr.(*ast.Ident)
	return id, ok
}

// inAnyRange reports whether pos is inside one of the function-literal
// ranges.
func inAnyRange(pos token.Pos, ranges []tokenRange) bool {
	for _, r := range ranges {
		if pos >= r.start && pos < r.end {
			return true
		}
	}
	return false
}

// symbolGraph is the resolved relationship projection of one index build.
//
// It is derived state from the same file records and package graph as the
// symbol coordinates. It is not a second parse, index, or daemon.
type symbolGraph struct {
	refs   []ReferenceSite
	calls  []CallEdge
	module string

	// dotAffected maps a package identity to the files whose dot imports
	// could capture an unqualified reference to that package. It covers both
	// a dot-importing file's own package and the package it dot-imports.
	dotAffected map[packageKey]map[string]bool
	// unknownByCaller counts dynamic call sites inside one function.
	unknownByCaller map[string]int
	// unknownReasonsByCaller aggregates the reasons for one function's
	// dynamic calls. It keeps a callees answer's reason breakdown consistent
	// with its per-function count.
	unknownReasonsByCaller map[string]map[string]int
	// unknownReasons aggregates dynamic call reasons for disclosure.
	unknownReasons map[string]int
	// unknownTotal is the number of dynamic call sites in the repository.
	unknownTotal int
	// ambiguousUses and unresolvedUses are reference-side blind spots.
	ambiguousUses  int
	unresolvedUses int
}

// packageKey identifies one Go package within one directory. It is the unit
// of declaration lookup; a directory with two package names is two packages.
type packageKey struct {
	dir string
	pkg string
}

// refTargetKey is the stable identity of a non-method declaration.
func refTargetKey(target RefTarget) string {
	return packageDir(target.Path) + "\x00" + target.Package + "\x00" + target.Symbol
}

// callKey is the stable identity of one declaration in the call graph.
func callKey(path string, line int, name string) string {
	return path + ":" + strconv.Itoa(line) + ":" + name
}

// buildSymbolGraph resolves the raw syntactic relationships against the
// complete declaration table. It runs inside the index build lock and before
// the snapshot is published, so a cancelled build never exposes partial
// relationships.
func buildSymbolGraph(ctx context.Context, records map[string]fileRecord, module string, graph *packageGraph) (*symbolGraph, error) {
	g := &symbolGraph{
		module:                 module,
		dotAffected:            map[packageKey]map[string]bool{},
		unknownByCaller:        map[string]int{},
		unknownReasonsByCaller: map[string]map[string]int{},
		unknownReasons:         map[string]int{},
	}
	if graph == nil {
		return g, nil
	}

	decls := map[packageKey]map[string][]Match{}
	paths := make([]string, 0, len(records))
	for rel, rec := range records {
		paths = append(paths, rel)
		if rec.err != nil {
			continue
		}
		key := packageKey{dir: packageDir(rel), pkg: rec.packageName}
		table := decls[key]
		if table == nil {
			table = map[string][]Match{}
			decls[key] = table
		}
		for _, sym := range rec.symbols {
			if sym.Kind == KindPackage || sym.Kind == KindImport {
				continue
			}
			table[sym.Name] = append(table[sym.Name], sym)
		}
	}
	sort.Strings(paths)

	importTargets := importTargetTable(module, graph)

	work := 0
	check := func() error {
		work++
		if work%refBuildCheckInterval != 0 {
			return nil
		}
		return ctx.Err()
	}

	callRefs := map[string]bool{}
	for _, rel := range paths {
		rec := records[rel]
		if rec.err != nil {
			continue
		}
		fileKey := packageKey{dir: packageDir(rel), pkg: rec.packageName}
		qualifiers, ambiguousQualifiers, dot := qualifierTable(rec, module, graph)
		if dot {
			addDotAffected(g.dotAffected, fileKey, rel)
			for _, imp := range rec.imports {
				if imp.Alias != "." {
					continue
				}
				targetDir, local := moduleLocalTarget(module, imp.Path)
				if !local {
					continue
				}
				targetPkg := packageForDir(decls, targetDir)
				if targetPkg == "" {
					continue
				}
				addDotAffected(g.dotAffected, packageKey{dir: targetDir, pkg: targetPkg}, rel)
			}
		}

		for _, raw := range rec.calls {
			if err := check(); err != nil {
				return nil, err
			}
			resolution := resolveRawCall(decls, module, importTargets, qualifiers, ambiguousQualifiers, fileKey, dot, raw)
			switch resolution.kind {
			case callResolved:
				caller := callNodeForRaw(rel, rec.packageName, raw)
				callee := callNodeForMatch(resolution.target)
				certainty := CertaintyStatic
				if raw.InClosure || raw.Anonymous {
					certainty = CertaintyLexical
				}
				g.calls = append(g.calls, CallEdge{
					Caller:    caller,
					Callee:    callee,
					CallPath:  rel,
					CallLine:  raw.Line,
					Column:    raw.Column,
					InClosure: raw.InClosure,
					Test:      isTestFile(rel),
					Certainty: certainty,
					callerKey: caller.key,
					calleeKey: callee.key,
				})
				if resolution.target.Kind == KindFunc && !raw.PackageInit &&
					(raw.Kind == rawCallIdent || raw.Kind == rawCallSelector) {
					callRefs[refPositionKey(rel, raw.FunLine, raw.FunColumn)] = true
				}
			case callDynamic:
				g.unknownTotal++
				g.unknownReasons[resolution.reason]++
				if !raw.PackageInit && !raw.Anonymous {
					key := callKey(rel, raw.CallerLine, raw.CallerName)
					g.unknownByCaller[key]++
					reasons := g.unknownReasonsByCaller[key]
					if reasons == nil {
						reasons = map[string]int{}
						g.unknownReasonsByCaller[key] = reasons
					}
					reasons[resolution.reason]++
				}
			}
		}

		for _, raw := range rec.references {
			if err := check(); err != nil {
				return nil, err
			}
			target, reason := resolveRawReference(decls, module, importTargets, qualifiers, ambiguousQualifiers, fileKey, dot, raw)
			switch reason {
			case refAmbiguous:
				g.ambiguousUses++
				continue
			case refUnresolved:
				g.unresolvedUses++
				continue
			case refExternal, refNotASymbol:
				continue
			}
			if target == nil {
				continue
			}
			g.refs = append(g.refs, ReferenceSite{
				Path:      rel,
				Line:      raw.Line,
				Column:    raw.Column,
				Qualified: raw.Qualifier != "",
				Call:      callRefs[refPositionKey(rel, raw.Line, raw.Column)],
				Test:      isTestFile(rel),
				targetKey: refTargetKey(RefTarget{
					Path:    target.Path,
					Package: target.Package,
					Symbol:  target.Name,
				}),
			})
		}
	}

	sort.SliceStable(g.refs, func(i, j int) bool {
		if g.refs[i].targetKey != g.refs[j].targetKey {
			return g.refs[i].targetKey < g.refs[j].targetKey
		}
		if g.refs[i].Path != g.refs[j].Path {
			return g.refs[i].Path < g.refs[j].Path
		}
		if g.refs[i].Line != g.refs[j].Line {
			return g.refs[i].Line < g.refs[j].Line
		}
		return g.refs[i].Column < g.refs[j].Column
	})
	sort.SliceStable(g.calls, func(i, j int) bool {
		if g.calls[i].callerKey != g.calls[j].callerKey {
			return g.calls[i].callerKey < g.calls[j].callerKey
		}
		if g.calls[i].CallLine != g.calls[j].CallLine {
			return g.calls[i].CallLine < g.calls[j].CallLine
		}
		if g.calls[i].Column != g.calls[j].Column {
			return g.calls[i].Column < g.calls[j].Column
		}
		return g.calls[i].calleeKey < g.calls[j].calleeKey
	})

	// The raw projection is no longer needed; resolved coordinates are
	// retained instead. Clearing it keeps resident memory proportional to the
	// answered relationships, not to every syntactic identifier.
	for rel, rec := range records {
		if rec.references != nil || rec.calls != nil || rec.imports != nil {
			rec.references = nil
			rec.calls = nil
			// Imports have already been projected into the package graph and
			// the resolved relationships; retaining the raw declarations
			// would only duplicate that memory.
			rec.imports = nil
			records[rel] = rec
		}
	}
	return g, nil
}

// importTargetTable maps module-local import paths to repository directories.
func importTargetTable(module string, graph *packageGraph) map[string]string {
	out := map[string]string{}
	if module == "" || graph == nil {
		return out
	}
	for _, dir := range graph.order {
		importPath := module
		if dir != "." {
			importPath = module + "/" + dir
		}
		out[importPath] = dir
	}
	return out
}

// qualifierTable maps the names a file can use for an import to its import
// path. A default import's effective name is the target package's declared
// name, which is only knowable after the target package has been parsed.
func qualifierTable(rec fileRecord, module string, graph *packageGraph) (map[string]string, map[string]bool, bool) {
	qualifiers := map[string]string{}
	ambiguous := map[string]bool{}
	dot := false
	add := func(name, importPath string) {
		if name == "" {
			return
		}
		if existing, ok := qualifiers[name]; ok && existing != importPath {
			ambiguous[name] = true
			return
		}
		qualifiers[name] = importPath
	}
	for _, imp := range rec.imports {
		switch imp.Alias {
		case ".":
			dot = true
			continue
		case "_":
			continue
		case "":
			base := path.Base(imp.Path)
			if module != "" && graph != nil {
				if target, local := moduleLocalTarget(module, imp.Path); local && !unsafeModuleTarget(target) {
					if node := graph.nodes[target]; node != nil && node.name != "" {
						add(node.name, imp.Path)
						continue
					}
				}
			}
			add(base, imp.Path)
		default:
			add(imp.Alias, imp.Path)
		}
	}
	return qualifiers, ambiguous, dot
}

// callResolutionKind is the outcome of resolving one raw call.
type callResolutionKind int

const (
	callUnknown callResolutionKind = iota
	callResolved
	callDynamic
	callExternal
	callBuiltin
	callConversion
	callAnonymous
)

type callResolution struct {
	kind   callResolutionKind
	target Match
	reason string
}

// resolveRawCall resolves a syntactic call target against the declaration
// table. Only a direct call to an indexed function or method is an edge.
func resolveRawCall(decls map[packageKey]map[string][]Match, module string, importTargets map[string]string, qualifiers map[string]string, ambiguousQualifiers map[string]bool, fileKey packageKey, dot bool, raw rawCall) callResolution {
	switch raw.Kind {
	case rawCallFuncLit:
		return callResolution{kind: callAnonymous}
	case rawCallType:
		// A conversion expression such as []byte(s) or map[K]V(x) has a
		// type, not a callable target. The type is already a reference; it is
		// not a dynamic call.
		return callResolution{kind: callConversion}
	case rawCallPointer:
		return resolvePointerExpr(decls, module, importTargets, qualifiers, ambiguousQualifiers, fileKey, raw)
	case rawCallDynamic:
		return callResolution{kind: callDynamic, reason: "function_value"}
	case rawCallMethodExpr:
		if raw.Local {
			return callResolution{kind: callDynamic, reason: "method_dispatch"}
		}
		// An unqualified exported receiver type may be shadowed by a dot
		// import; a lowercase receiver cannot be dot-imported.
		if raw.Qualifier == "" && dot && ast.IsExported(raw.Receiver) {
			return callResolution{kind: callDynamic, reason: "dot_import"}
		}
		if raw.Qualifier != "" {
			importPath, ok := qualifiers[raw.Qualifier]
			if !ok || ambiguousQualifiers[raw.Qualifier] {
				return callResolution{kind: callDynamic, reason: "ambiguous_import"}
			}
			targetDir, local := moduleLocalTarget(module, importPath)
			if !local {
				return callResolution{kind: callExternal}
			}
			targetPkg := packageForDir(decls, targetDir)
			if targetPkg == "" {
				return callResolution{kind: callDynamic, reason: "unresolved_import"}
			}
			if target, ok := resolveMethod(decls, packageKey{dir: targetDir, pkg: targetPkg}, raw.Receiver, raw.Name); ok {
				return callResolution{kind: callResolved, target: target}
			}
			return callResolution{kind: callDynamic, reason: "method_dispatch"}
		}
		if target, ok := resolveMethod(decls, fileKey, raw.Receiver, raw.Name); ok {
			return callResolution{kind: callResolved, target: target}
		}
		return callResolution{kind: callDynamic, reason: "method_dispatch"}
	case rawCallIdent:
		if raw.Local {
			if raw.LocalType {
				return callResolution{kind: callConversion}
			}
			return callResolution{kind: callDynamic, reason: "function_value"}
		}
		// A dot import can only capture exported names. A lowercase
		// unqualified name is safe to resolve against the local package and
		// predeclared universe.
		if dot && ast.IsExported(raw.Name) {
			return callResolution{kind: callDynamic, reason: "dot_import"}
		}
		candidates := packageSymbolCandidates(decls, fileKey, raw.Name)
		switch len(candidates) {
		case 1:
			return callResolutionForTarget(candidates[0])
		case 0:
			if isPredeclaredType(raw.Name) {
				return callResolution{kind: callConversion}
			}
			if isPredeclaredFunc(raw.Name) {
				return callResolution{kind: callBuiltin}
			}
			return callResolution{kind: callDynamic, reason: "undefined"}
		default:
			return callResolution{kind: callDynamic, reason: "ambiguous_target"}
		}
	case rawCallSelector:
		if raw.Local {
			return callResolution{kind: callDynamic, reason: "method_dispatch"}
		}
		if importPath, ok := qualifiers[raw.Qualifier]; ok {
			if ambiguousQualifiers[raw.Qualifier] {
				return callResolution{kind: callDynamic, reason: "ambiguous_import"}
			}
			targetDir, local := moduleLocalTarget(module, importPath)
			if !local {
				return callResolution{kind: callExternal}
			}
			targetPkg := packageForDir(decls, targetDir)
			if targetPkg == "" {
				return callResolution{kind: callDynamic, reason: "unresolved_import"}
			}
			candidates := packageSymbolCandidates(decls, packageKey{dir: targetDir, pkg: targetPkg}, raw.Name)
			switch len(candidates) {
			case 1:
				return callResolutionForTarget(candidates[0])
			case 0:
				return callResolution{kind: callDynamic, reason: "unresolved_target"}
			default:
				return callResolution{kind: callDynamic, reason: "ambiguous_target"}
			}
		}
		// A selector whose base is not an import may still be a method
		// expression (T.M) when the base names a type. An exported base may
		// be shadowed by a dot import; a lowercase base cannot be.
		if dot && ast.IsExported(raw.Qualifier) {
			return callResolution{kind: callDynamic, reason: "dot_import"}
		}
		candidates := packageSymbolCandidates(decls, fileKey, raw.Qualifier)
		if len(candidates) == 1 && candidates[0].Kind == KindType {
			if method, ok := resolveMethod(decls, fileKey, raw.Qualifier, raw.Name); ok {
				return callResolution{kind: callResolved, target: method}
			}
			return callResolution{kind: callDynamic, reason: "method_dispatch"}
		}
		return callResolution{kind: callDynamic, reason: "method_dispatch"}
	default:
		return callResolution{kind: callDynamic, reason: "dynamic_target"}
	}
}

// resolvePointerExpr distinguishes a pointer conversion (*T)(x) from a call
// through a pointer-to-function value (*f)().
func resolvePointerExpr(decls map[packageKey]map[string][]Match, module string, importTargets map[string]string, qualifiers map[string]string, ambiguousQualifiers map[string]bool, fileKey packageKey, raw rawCall) callResolution {
	if raw.LocalType {
		return callResolution{kind: callConversion}
	}
	if raw.Local || raw.Name == "" {
		return callResolution{kind: callDynamic, reason: "function_value"}
	}
	targetKey := fileKey
	if raw.Qualifier != "" {
		importPath, ok := qualifiers[raw.Qualifier]
		if !ok || ambiguousQualifiers[raw.Qualifier] {
			return callResolution{kind: callDynamic, reason: "function_value"}
		}
		targetDir, local := moduleLocalTarget(module, importPath)
		if !local {
			return callResolution{kind: callExternal}
		}
		targetPkg := packageForDir(decls, targetDir)
		if targetPkg == "" {
			return callResolution{kind: callDynamic, reason: "unresolved_import"}
		}
		targetKey = packageKey{dir: targetDir, pkg: targetPkg}
	}
	candidates := packageSymbolCandidates(decls, targetKey, raw.Name)
	if len(candidates) == 1 && candidates[0].Kind == KindType {
		return callResolution{kind: callConversion}
	}
	if len(candidates) == 0 && isPredeclaredType(raw.Name) {
		return callResolution{kind: callConversion}
	}
	return callResolution{kind: callDynamic, reason: "function_value"}
}

func callResolutionForTarget(target Match) callResolution {
	switch target.Kind {
	case KindFunc, KindMethod:
		return callResolution{kind: callResolved, target: target}
	case KindType:
		// T(x) is a conversion, not a call.
		return callResolution{kind: callConversion}
	case KindVar, KindConst:
		return callResolution{kind: callDynamic, reason: "function_value"}
	default:
		return callResolution{kind: callDynamic, reason: "unresolved_target"}
	}
}

// refResolutionKind is the outcome of resolving one raw reference.
type refResolutionKind int

const (
	refResolved refResolutionKind = iota
	refAmbiguous
	refUnresolved
	refExternal
	refNotASymbol
)

// resolveRawReference resolves one syntactic reference to a package-level
// declaration. Method names are not package-scope symbols and are therefore
// not resolved as unqualified or qualified references.
func resolveRawReference(decls map[packageKey]map[string][]Match, module string, importTargets map[string]string, qualifiers map[string]string, ambiguousQualifiers map[string]bool, fileKey packageKey, dot bool, raw rawReference) (*Match, refResolutionKind) {
	if raw.Qualifier != "" {
		importPath, ok := qualifiers[raw.Qualifier]
		if !ok {
			// The selector base is a value, not an import. The base itself was
			// already recorded as an unqualified reference.
			return nil, refNotASymbol
		}
		if ambiguousQualifiers[raw.Qualifier] {
			return nil, refAmbiguous
		}
		targetDir, local := moduleLocalTarget(module, importPath)
		if !local {
			return nil, refExternal
		}
		targetPkg := packageForDir(decls, targetDir)
		if targetPkg == "" {
			return nil, refUnresolved
		}
		candidates := packageSymbolCandidates(decls, packageKey{dir: targetDir, pkg: targetPkg}, raw.Name)
		switch len(candidates) {
		case 1:
			return &candidates[0], refResolved
		case 0:
			return nil, refNotASymbol
		default:
			return nil, refAmbiguous
		}
	}

	if raw.SelectorBase {
		// An import name is file-scoped and shadows a package-level name. A
		// selector base that matches an import qualifier is the package, not
		// a reference to the shadowed declaration.
		if _, isImport := qualifiers[raw.Name]; isImport {
			return nil, refNotASymbol
		}
	}
	if dot && ast.IsExported(raw.Name) {
		// A dot import can capture an exported unqualified name. The target
		// package's dot-import count already tells a references query that its
		// answer is incomplete; counting every identifier here would only add
		// noise. Lowercase names cannot be dot-imported, so they resolve
		// against the local package normally.
		return nil, refNotASymbol
	}
	candidates := packageSymbolCandidates(decls, fileKey, raw.Name)
	switch len(candidates) {
	case 1:
		return &candidates[0], refResolved
	case 0:
		return nil, refNotASymbol
	default:
		return nil, refAmbiguous
	}
}

// packageSymbolCandidates returns the package-scope declarations named name.
// Methods are not in package scope, so they are filtered out even though the
// index keeps them under their Receiver.Method name.
func packageSymbolCandidates(decls map[packageKey]map[string][]Match, key packageKey, name string) []Match {
	table := decls[key]
	if table == nil {
		return nil
	}
	var found []Match
	for _, match := range table[name] {
		if match.Kind == KindMethod {
			continue
		}
		found = append(found, match)
	}
	return found
}

// resolveMethod resolves a direct method declaration Receiver.Method.
func resolveMethod(decls map[packageKey]map[string][]Match, key packageKey, receiver, method string) (Match, bool) {
	table := decls[key]
	if table == nil || receiver == "" || method == "" {
		return Match{}, false
	}
	found := table[receiver+"."+method]
	if len(found) != 1 || found[0].Kind != KindMethod {
		return Match{}, false
	}
	return found[0], true
}

// packageForDir returns the canonical non-test package name for a directory,
// or empty when the directory has no unique package identity.
func packageForDir(decls map[packageKey]map[string][]Match, dir string) string {
	var out string
	for key := range decls {
		if key.dir != dir || strings.HasSuffix(key.pkg, "_test") {
			continue
		}
		if out != "" && out != key.pkg {
			return ""
		}
		out = key.pkg
	}
	return out
}

// isPredeclaredFunc reports a Go predeclared function. A call to one is not a
// repository call and not an unknown dynamic call.
func isPredeclaredFunc(name string) bool {
	switch name {
	case "append", "cap", "clear", "close", "complex", "copy", "delete",
		"imag", "len", "make", "max", "min", "new", "panic", "print",
		"println", "real", "recover":
		return true
	}
	return false
}

// isPredeclaredType reports a Go predeclared type. T(x) is a conversion, not
// a dynamic call through a function value.
func isPredeclaredType(name string) bool {
	switch name {
	case "any", "bool", "byte", "comparable", "complex64", "complex128",
		"error", "float32", "float64", "int", "int8", "int16", "int32",
		"int64", "rune", "string", "uint", "uint8", "uint16", "uint32",
		"uint64", "uintptr":
		return true
	}
	return false
}

func callNodeForRaw(rel, pkg string, raw rawCall) CallNode {
	if raw.PackageInit {
		return CallNode{
			Path:       rel,
			Package:    pkg,
			Symbol:     "<package init>",
			Line:       raw.Line,
			EndLine:    raw.Line,
			CallerKind: "package_initializer",
			key:        "pkginit:" + rel + ":" + strconv.Itoa(raw.Line),
		}
	}
	if raw.Anonymous {
		return CallNode{
			Path:       rel,
			Package:    pkg,
			Symbol:     "<closure>",
			Line:       raw.Line,
			EndLine:    raw.Line,
			CallerKind: "anonymous",
			key:        "anon:" + rel + ":" + strconv.Itoa(raw.Line) + ":" + strconv.Itoa(raw.Column),
		}
	}
	kind := KindFunc
	callerKind := "function"
	if raw.CallerMethod {
		kind = KindMethod
		callerKind = "method"
	}
	return CallNode{
		Path:       rel,
		Package:    pkg,
		Symbol:     raw.CallerName,
		Kind:       kind,
		Line:       raw.CallerLine,
		EndLine:    raw.CallerEndLine,
		CallerKind: callerKind,
		key:        callKey(rel, raw.CallerLine, raw.CallerName),
	}
}

func callNodeForMatch(match Match) CallNode {
	return CallNode{
		Path:       match.Path,
		Package:    match.Package,
		Symbol:     match.Name,
		Kind:       match.Kind,
		Line:       match.Line,
		EndLine:    match.EndLine,
		CallerKind: callKindName(match.Kind),
		key:        callKey(match.Path, match.Line, match.Name),
	}
}

func callKindName(kind Kind) string {
	if kind == KindMethod {
		return "method"
	}
	return "function"
}

func refPositionKey(path string, line, column int) string {
	return path + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(column)
}

// normalizeRefQuery applies defaults and rejects out-of-range bounds.
func normalizeRefQuery(query RefQuery) (RefRelation, int, int, int, error) {
	relation := query.Relation
	if relation == "" {
		relation = RelationReferences
	}
	switch relation {
	case RelationReferences, RelationCallers, RelationCallees:
	default:
		return "", 0, 0, 0, fmt.Errorf("ast: unsupported relationship %q", relation)
	}

	depth := query.Depth
	if relation == RelationReferences {
		if depth > 1 {
			return "", 0, 0, 0, fmt.Errorf("ast: references do not take a traversal depth")
		}
		depth = 0
	} else {
		if depth == 0 {
			depth = RefDefaultDepth
		}
		if depth < 1 || depth > RefMaxDepth {
			return "", 0, 0, 0, fmt.Errorf("ast: relationship depth must be between 1 and %d, got %d", RefMaxDepth, depth)
		}
	}

	limit := query.Limit
	if limit == 0 {
		limit = RefDefaultLimit
	}
	if limit < 1 || limit > RefMaxLimit {
		return "", 0, 0, 0, fmt.Errorf("ast: relationship limit must be between 1 and %d, got %d", RefMaxLimit, limit)
	}

	edgeLimit := query.EdgeLimit
	if edgeLimit == 0 {
		edgeLimit = limit * 2
		if edgeLimit > RefMaxEdges {
			edgeLimit = RefMaxEdges
		}
	}
	if edgeLimit < 1 || edgeLimit > RefMaxEdges {
		return "", 0, 0, 0, fmt.Errorf("ast: relationship edge limit must be between 1 and %d, got %d", RefMaxEdges, edgeLimit)
	}
	if query.Skip < 0 {
		return "", 0, 0, 0, fmt.Errorf("ast: relationship skip must be >= 0, got %d", query.Skip)
	}
	if strings.TrimSpace(query.Symbol) == "" {
		return "", 0, 0, 0, fmt.Errorf("ast: relationship query requires a symbol")
	}
	return relation, depth, limit, edgeLimit, nil
}

// References answers one bounded symbol-relationship query over the shared
// index snapshot.
func (idx *Index) References(ctx context.Context, query RefQuery) (RefResult, error) {
	if err := ctx.Err(); err != nil {
		return RefResult{}, err
	}
	relation, depth, limit, edgeLimit, err := normalizeRefQuery(query)
	if err != nil {
		return RefResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return RefResult{}, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.symbolGraph == nil {
		return RefResult{}, fmt.Errorf("ast: relationship snapshot is unavailable")
	}
	result := RefResult{
		Module:        idx.modulePath,
		Relation:      relation,
		Symbol:        query.Symbol,
		Depth:         depth,
		ParseFailures: idx.parseFailuresLocked(),
	}
	target, ambiguous, candidates, found, err := idx.resolveRefTargetLocked(query.Symbol, query.Path)
	if err != nil {
		return RefResult{}, err
	}
	if ambiguous {
		result.Ambiguous = true
		result.Candidates = candidates
		return result, nil
	}
	if !found {
		return result, nil
	}
	result.Found = true
	result.Target = target

	switch relation {
	case RelationReferences:
		if target.Kind == KindMethod {
			result.Unsupported = true
			result.UnsupportedReason = "method references require receiver type resolution; only package-scope declarations are indexed"
			return result, nil
		}
		if err := idx.referencesForTargetLocked(ctx, &result, target, query.Skip, limit); err != nil {
			return RefResult{}, err
		}
	case RelationCallers, RelationCallees:
		if target.Kind != KindFunc && target.Kind != KindMethod {
			result.Unsupported = true
			result.UnsupportedReason = "call edges exist only for functions and methods"
			return result, nil
		}
		if err := idx.callsForTargetLocked(ctx, &result, target, query.Skip, limit, edgeLimit); err != nil {
			return RefResult{}, err
		}
	}
	return result, nil
}

// resolveRefTargetLocked resolves an exact symbol name or qualified name,
// optionally scoped to a file or directory. It never picks a winner between
// multiple declarations.
func (idx *Index) resolveRefTargetLocked(symbol, scope string) (RefTarget, bool, []RefTarget, bool, error) {
	var scopeRel string
	scopeDir := false
	if scope != "" {
		rel, err := normalizeIndexRel(idx.Root, scope)
		if err != nil {
			return RefTarget{}, false, nil, false, err
		}
		scopeRel = filepath.ToSlash(rel)
		abs := filepath.Join(idx.Root, filepath.FromSlash(scopeRel))
		info, err := os.Stat(abs)
		if err != nil {
			return RefTarget{}, false, nil, false, fmt.Errorf("ast: relationship scope %q: %w", scope, err)
		}
		scopeDir = info.IsDir()
	}

	var matches []Match
	for rel, rec := range idx.files {
		for _, sym := range rec.symbols {
			if sym.Kind == KindPackage || sym.Kind == KindImport {
				continue
			}
			if sym.Name != symbol && sym.QualifiedName != symbol {
				continue
			}
			if scopeRel != "" && !refScopeContains(scopeRel, scopeDir, rel) {
				continue
			}
			matches = append(matches, sym)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		if matches[i].Line != matches[j].Line {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].QualifiedName < matches[j].QualifiedName
	})
	if len(matches) == 0 {
		return RefTarget{}, false, nil, false, nil
	}
	if len(matches) > 1 {
		candidates := make([]RefTarget, 0, len(matches))
		for _, match := range matches {
			candidates = append(candidates, refTarget(match))
		}
		return RefTarget{}, true, candidates, false, nil
	}
	return refTarget(matches[0]), false, nil, true, nil
}

func refTarget(match Match) RefTarget {
	return RefTarget{
		Path:          match.Path,
		Package:       match.Package,
		Symbol:        match.Name,
		QualifiedName: match.QualifiedName,
		Kind:          match.Kind,
		Line:          match.Line,
		EndLine:       match.EndLine,
	}
}

func refScopeContains(scope string, isDir bool, rel string) bool {
	if !isDir {
		return rel == scope
	}
	if scope == "." || scope == "" {
		return true
	}
	return rel == scope || strings.HasPrefix(rel, scope+"/")
}

// referencesForTargetLocked scans the resolved reference projection. The work
// bound makes the scan finite even on a very large repository; a limit hit is
// reported as truncation, never as a complete answer.
func (idx *Index) referencesForTargetLocked(ctx context.Context, result *RefResult, target RefTarget, skip, limit int) error {
	key := refTargetKey(target)
	skipped := 0
	work := 0
	truncated := false
	reason := ""
	for _, ref := range idx.symbolGraph.refs {
		work++
		if work%refCancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if work > RefWorkLimit {
			truncated, reason = true, "work_limit"
			break
		}
		if ref.targetKey != key {
			continue
		}
		result.TotalReferences++
		if skipped < skip {
			skipped++
			continue
		}
		if len(result.References) >= limit {
			truncated = true
			continue
		}
		result.References = append(result.References, ReferenceSite{
			Path:      ref.Path,
			Line:      ref.Line,
			Column:    ref.Column,
			Qualified: ref.Qualified,
			Call:      ref.Call,
			Test:      ref.Test,
			targetKey: ref.targetKey,
		})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.Work = work
	result.Truncated = truncated
	result.Reason = reason
	if result.Truncated && result.Reason == "" {
		result.Reason = "limit"
	}
	result.Unknown = idx.referenceUnknownLocked(target)
	// Ambiguous/unresolved uses are repository-wide disclosures. A query for
	// a uniquely resolved declaration is only incomplete when its own package
	// has a dot import (which can capture an unqualified name) or the index
	// could not inspect part of the tree.
	result.Complete = result.ParseFailures == 0 && result.Unknown.DotImportFiles == 0
	return nil
}

// referenceUnknownLocked reports the reference-side blind spots that apply to
// the target package. Ambiguous/unresolved uses are global counters; they are
// disclosed even though most do not affect a uniquely resolved target.
func (idx *Index) referenceUnknownLocked(target RefTarget) UnknownSummary {
	key := packageKey{dir: packageDir(target.Path), pkg: target.Package}
	return UnknownSummary{
		DotImportFiles: len(idx.symbolGraph.dotAffected[key]),
		AmbiguousUses:  idx.symbolGraph.ambiguousUses,
		UnresolvedUses: idx.symbolGraph.unresolvedUses,
	}
}

// callsForTargetLocked answers callers/callees for one function or method.
func (idx *Index) callsForTargetLocked(ctx context.Context, result *RefResult, target RefTarget, skip, limit, edgeLimit int) error {
	focal := callNodeForMatch(Match{
		Name:    target.Symbol,
		Package: target.Package,
		Path:    target.Path,
		Line:    target.Line,
		EndLine: target.EndLine,
		Kind:    target.Kind,
	})
	focal.Depth = 0
	result.Unknown = UnknownSummary{
		DynamicCalls: idx.symbolGraph.unknownTotal,
		Reasons:      copyReasons(idx.symbolGraph.unknownReasons),
	}
	if result.Relation == RelationCallees {
		result.Unknown.DynamicCalls = idx.symbolGraph.unknownByCaller[focal.key]
		result.Unknown.Reasons = copyReasons(idx.symbolGraph.unknownReasonsByCaller[focal.key])
	}
	result.Complete = result.ParseFailures == 0 && result.Unknown.DynamicCalls == 0

	if result.Depth <= 1 {
		return idx.directCallsLocked(ctx, result, focal, skip, limit)
	}
	return idx.traverseCallsLocked(ctx, result, focal, skip, limit, edgeLimit)
}

// directCallsLocked scans the flat call projection once, preserving every
// call-site witness.
func (idx *Index) directCallsLocked(ctx context.Context, result *RefResult, focal CallNode, skip, limit int) error {
	skipped := 0
	work := 0
	truncated := false
	seen := map[string]CallNode{focal.key: focal}
	far := map[string]bool{}
	for _, edge := range idx.symbolGraph.calls {
		work++
		if work%refCancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if work > RefWorkLimit {
			truncated, result.Reason = true, "work_limit"
			break
		}
		if result.Relation == RelationCallers {
			if edge.calleeKey != focal.key {
				continue
			}
		} else if edge.callerKey != focal.key {
			continue
		}
		result.TotalEdges++
		if skipped < skip {
			skipped++
			continue
		}
		if len(result.Edges) >= limit {
			truncated = true
			continue
		}
		result.Edges = append(result.Edges, publicCallEdge(edge))
		callerNode := edge.Caller
		calleeNode := edge.Callee
		callerNode.Depth = 1
		calleeNode.Depth = 1
		if callerNode.key == focal.key {
			callerNode.Depth = 0
		}
		if calleeNode.key == focal.key {
			calleeNode.Depth = 0
		}
		seen[edge.callerKey] = callerNode
		seen[edge.calleeKey] = calleeNode
		if result.Relation == RelationCallees {
			far[edge.calleeKey] = true
		} else {
			far[edge.callerKey] = true
		}
	}
	// A direct page is a depth-1 traversal. If a returned endpoint continues
	// beyond that depth, the answer is depth-limited even though the scan
	// itself did not need a BFS.
	if !truncated && len(far) > 0 {
		for _, edge := range idx.symbolGraph.calls {
			work++
			if work%refCancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if work > RefWorkLimit {
				truncated, result.Reason = true, "work_limit"
				break
			}
			if result.Relation == RelationCallees && far[edge.callerKey] {
				truncated, result.Reason = true, "depth"
				break
			}
			if result.Relation == RelationCallers && far[edge.calleeKey] {
				truncated, result.Reason = true, "depth"
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.Nodes = orderCallNodes(seen)
	result.Truncated = truncated
	if result.Truncated && result.Reason == "" {
		result.Reason = "limit"
	}
	result.Work = work
	result.Unknown.PackageInitializerCalls = countPackageInit(result.Edges)
	return nil
}

// traverseCallsLocked runs a bounded breadth-first traversal over the static
// call graph. Visited nodes make cycles safe; depth, node, edge, and work
// bounds make the traversal finite.
func (idx *Index) traverseCallsLocked(ctx context.Context, result *RefResult, focal CallNode, skip, limit, edgeLimit int) error {
	work := 0
	out := map[string][]CallEdge{}
	in := map[string][]CallEdge{}
	for _, edge := range idx.symbolGraph.calls {
		work++
		if work%refCancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if work > RefWorkLimit {
			result.Truncated, result.Reason = true, "work_limit"
			break
		}
		if result.Relation == RelationCallees {
			out[edge.callerKey] = append(out[edge.callerKey], edge)
		} else {
			in[edge.calleeKey] = append(in[edge.calleeKey], edge)
		}
	}
	if result.Truncated {
		// The adjacency is partial. Return the focal node with an explicit
		// truncation rather than a plausible-looking partial graph.
		result.Nodes = []CallNode{focal}
		result.Work = work
		return nil
	}

	visited := map[string]bool{focal.key: true}
	depths := map[string]int{focal.key: 0}
	nodes := []CallNode{focal}
	queue := []string{focal.key}
	var edges []CallEdge
	seenEdge := map[string]bool{}
	truncated := false
	reason := ""

	for pos := 0; pos < len(queue) && !truncated; pos++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		from := queue[pos]
		fromDepth := depths[from]
		if fromDepth >= result.Depth {
			continue
		}
		var outgoing []CallEdge
		if result.Relation == RelationCallees {
			outgoing = out[from]
		} else {
			outgoing = in[from]
		}
		for _, edge := range outgoing {
			work++
			if work%refCancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if work > RefWorkLimit {
				truncated, reason = true, "work_limit"
				break
			}
			edgeKey := edge.callerKey + "\x00" + edge.calleeKey + "\x00" +
				strconv.Itoa(edge.CallLine) + "\x00" + strconv.Itoa(edge.Column)
			if seenEdge[edgeKey] {
				continue
			}
			next := edge.Callee
			if result.Relation == RelationCallers {
				next = edge.Caller
			}
			if !visited[next.key] && len(nodes) >= limit {
				truncated, reason = true, "node_limit"
				break
			}
			if len(edges) >= edgeLimit {
				truncated, reason = true, "edge_limit"
				break
			}
			if !visited[next.key] {
				visited[next.key] = true
				depths[next.key] = fromDepth + 1
				next.Depth = fromDepth + 1
				nodes = append(nodes, next)
				if next.CallerKind != "package_initializer" && next.CallerKind != "anonymous" {
					queue = append(queue, next.key)
				}
			}
			seenEdge[edgeKey] = true
			edges = append(edges, publicCallEdge(edge))
		}
	}

	// A node at the requested depth with relationships beyond it is a depth
	// scope, not a complete traversal.
	if !truncated {
		for _, node := range nodes {
			if depths[node.key] != result.Depth {
				continue
			}
			var beyond []CallEdge
			if result.Relation == RelationCallees {
				beyond = out[node.key]
			} else {
				beyond = in[node.key]
			}
			if len(beyond) > 0 {
				truncated, reason = true, "depth"
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	result.Nodes = nodes
	result.Edges = edges
	result.TotalEdges = len(edges)
	sort.SliceStable(result.Nodes, func(i, j int) bool {
		if result.Nodes[i].Depth != result.Nodes[j].Depth {
			return result.Nodes[i].Depth < result.Nodes[j].Depth
		}
		if result.Nodes[i].Path != result.Nodes[j].Path {
			return result.Nodes[i].Path < result.Nodes[j].Path
		}
		if result.Nodes[i].Line != result.Nodes[j].Line {
			return result.Nodes[i].Line < result.Nodes[j].Line
		}
		if result.Nodes[i].Symbol != result.Nodes[j].Symbol {
			return result.Nodes[i].Symbol < result.Nodes[j].Symbol
		}
		return result.Nodes[i].CallerKind < result.Nodes[j].CallerKind
	})
	sort.SliceStable(result.Edges, func(i, j int) bool {
		if result.Edges[i].Caller.Path != result.Edges[j].Caller.Path {
			return result.Edges[i].Caller.Path < result.Edges[j].Caller.Path
		}
		if result.Edges[i].Caller.Line != result.Edges[j].Caller.Line {
			return result.Edges[i].Caller.Line < result.Edges[j].Caller.Line
		}
		if result.Edges[i].CallLine != result.Edges[j].CallLine {
			return result.Edges[i].CallLine < result.Edges[j].CallLine
		}
		if result.Edges[i].Callee.Path != result.Edges[j].Callee.Path {
			return result.Edges[i].Callee.Path < result.Edges[j].Callee.Path
		}
		return result.Edges[i].Callee.Line < result.Edges[j].Callee.Line
	})
	result.Truncated = truncated
	result.Reason = reason
	result.Work = work
	result.Unknown.PackageInitializerCalls = countPackageInit(result.Edges)
	// Pagination applies to the returned edge page. The traversal bounds are
	// independent: skipping rows never lets the walk inspect more edges.
	if skip > 0 {
		if skip >= len(result.Edges) {
			result.Edges = nil
		} else {
			result.Edges = result.Edges[skip:]
		}
	}
	if result.Relation == RelationCallees {
		// Unknown calls inside any returned callee also limit the transitive
		// answer.
		dynamic := 0
		reasons := map[string]int{}
		for _, node := range result.Nodes {
			dynamic += idx.symbolGraph.unknownByCaller[node.key]
			for reason, count := range idx.symbolGraph.unknownReasonsByCaller[node.key] {
				reasons[reason] += count
			}
		}
		result.Unknown.DynamicCalls = dynamic
		result.Unknown.Reasons = copyReasons(reasons)
		result.Complete = result.ParseFailures == 0 && dynamic == 0
	}
	return nil
}

func publicCallEdge(edge CallEdge) CallEdge {
	// Keys are internal traversal state; a result edge carries coordinates
	// only.
	edge.Caller.key = ""
	edge.Callee.key = ""
	return edge
}

func countPackageInit(edges []CallEdge) int {
	count := 0
	for _, edge := range edges {
		if edge.Caller.CallerKind == "package_initializer" {
			count++
		}
	}
	return count
}

func addDotAffected(targets map[packageKey]map[string]bool, key packageKey, path string) {
	files := targets[key]
	if files == nil {
		files = map[string]bool{}
		targets[key] = files
	}
	files[path] = true
}

func orderCallNodes(nodes map[string]CallNode) []CallNode {
	out := make([]CallNode, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].CallerKind < out[j].CallerKind
	})
	return out
}

func copyReasons(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for reason, count := range in {
		out[reason] = count
	}
	return out
}
