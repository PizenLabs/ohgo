//go:build cgo

package ast

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// auditFixture writes an exact tree. Unlike graphFixture it never injects a
// root go.mod, which is the point of the nested-module cases.
func auditFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TypeScript path aliases are how a Next.js app actually imports its own
// components. Treating every non-relative import as external drops the page ->
// component graph and reports the project as independent modules.
func TestTypeScriptAliasImportsResolveDependencyEdges(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"tsconfig.json": `{
  // JSONC comments are legal in tsconfig.
  "compilerOptions": {
    "baseUrl": ".",
    "paths": { "@/*": ["./*"] },
  },
}`,
		"app/page.tsx": `import { Widget } from "@/components/Widget";
export default function Page() { return <Widget />; }
`,
		"components/Widget.tsx": "export function Widget() { return null; }\n",
	})
	idx := mustIndex(t, root)
	if got, err := idx.Lookup("Widget"); err != nil || len(got) != 1 {
		t.Fatalf("Widget lookup = %+v, %v", got, err)
	}
	deps, err := idx.Dependencies(context.Background(), DependencyQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !deps.Complete {
		t.Fatalf("dependencies incomplete: %+v", deps)
	}
	if len(deps.Edges) != 1 || deps.Edges[0].From != "app" || deps.Edges[0].To != "components" {
		t.Fatalf("alias dependency edges = %+v, want app -> components", deps.Edges)
	}
}

// An unresolved alias is a local gap, not an external package. Leaving it out
// of the unresolved list would let an incomplete import graph claim
// completeness.
func TestTypeScriptAliasUnresolvedIsDisclosed(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["./*"]}}}`,
		"app/page.tsx":  "import { Missing } from \"@/components/Missing\";\nexport const Page = Missing;\n",
	})
	idx := mustIndex(t, root)
	deps, err := idx.Dependencies(context.Background(), DependencyQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if deps.Complete || len(deps.Unresolved) != 1 || deps.Unresolved[0].Import != "@/components/Missing" {
		t.Fatalf("alias gap = complete:%v unresolved:%+v", deps.Complete, deps.Unresolved)
	}
}

// A JSX element is a reference to the component it renders. Without this the
// page -> component relationship is invisible to refs and impact, even though
// the import is present.
func TestJSXComponentUsageBecomesReference(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"app/page.tsx": `import { Widget } from "../components/Widget";
export default function Page() { return <Widget />; }
`,
		"components/Widget.tsx": "export function Widget() { return null; }\n",
	})
	idx := mustIndex(t, root)
	refs := mustRefs(t, idx, RefQuery{Symbol: "Widget"})
	if !refs.Found || refs.TotalReferences != 1 {
		t.Fatalf("Widget refs = found:%v total:%d refs:%+v", refs.Found, refs.TotalReferences, refs.References)
	}
	if refs.References[0].Path != "app/page.tsx" {
		t.Fatalf("JSX reference path = %q, want app/page.tsx", refs.References[0].Path)
	}

	impact, err := idx.Impact(context.Background(), ImpactQuery{Symbol: "Widget", Depth: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, site := range impact.References {
		if site.Path == "app/page.tsx" {
			found = true
		}
	}
	if !found {
		t.Fatalf("impact omitted the JSX reference: %+v", impact.References)
	}
}

// A TypeScript type annotation is a reference to a declared type. The blog's
// Post type is used across components; returning zero references made it look
// unused.
func TestTypeScriptTypeUsageBecomesReference(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"lib/types.ts": "export interface Post { title: string }\n",
		"components/Card.tsx": `import type { Post } from "../lib/types";
export function Card({ post }: { post: Post }) { return <div>{post.title}</div>; }
`,
	})
	idx := mustIndex(t, root)
	refs := mustRefs(t, idx, RefQuery{Symbol: "Post"})
	if !refs.Found || refs.TotalReferences != 1 {
		t.Fatalf("Post refs = found:%v total:%d refs:%+v", refs.Found, refs.TotalReferences, refs.References)
	}
	if refs.References[0].Path != "components/Card.tsx" {
		t.Fatalf("type reference path = %q, want components/Card.tsx", refs.References[0].Path)
	}
}

// Importers is a fact of the resolved import graph. Reporting zero for a
// non-Go package is a false absence, not an uncomputed column.
func TestNonGoPackageImportersAreComputed(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"tsconfig.json":         `{"compilerOptions":{"paths":{"@/*":["./*"]}}}`,
		"app/page.tsx":          "import { Widget } from \"@/components/Widget\";\nexport const Page = Widget;\n",
		"components/Widget.tsx": "export function Widget() { return null; }\n",
	})
	idx := mustIndex(t, root)
	result, err := idx.Packages(context.Background(), PackageQuery{Name: "components"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packages) != 1 || result.Packages[0].Importers != 1 {
		t.Fatalf("component package importers = %+v, want 1", result.Packages)
	}
}

// Build output referenced from authored source is a known exclusion. Treating
// it as an unresolved local import makes a healthy Next.js project report an
// incomplete dependency graph.
func TestGeneratedImportIsExcludedNotUnresolved(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"next-env.d.ts": "import \"./.next/dev/types/routes.d.ts\";\n",
		"app/page.tsx":  "export default function Page() { return null; }\n",
	})
	idx := mustIndex(t, root)
	deps, err := idx.Dependencies(context.Background(), DependencyQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !deps.Complete || len(deps.Unresolved) != 0 {
		t.Fatalf("generated import became a gap: complete:%v unresolved:%+v", deps.Complete, deps.Unresolved)
	}
}

// A repository can contain two modules that declare the same name (a template
// duplicated into a component directory). Global-name lookup called that
// AMBIGUOUS and emitted no edge, so the importing file’s resolved import was
// ignored and a real consumer became invisible.
func TestImportedDuplicateNameResolvesThroughImportPath(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["./*"]}}}`,
		"hooks/use-toast.ts": `export function useToast() { return { toasts: [] }; }
`,
		"components/ui/use-toast.ts": `export function useToast() { return { toasts: [] }; }
`,
		"components/ui/toaster.tsx": `import { useToast } from "@/hooks/use-toast";
export function Toaster() { const { toasts } = useToast(); return <div>{toasts.length}</div>; }
`,
	})
	idx := mustIndex(t, root)
	refs := mustRefs(t, idx, RefQuery{Symbol: "useToast", Path: "hooks/use-toast.ts"})
	if !refs.Found || refs.TotalReferences != 1 {
		t.Fatalf("useToast refs = found:%v total:%d refs:%+v", refs.Found, refs.TotalReferences, refs.References)
	}
	if refs.References[0].Path != "components/ui/toaster.tsx" {
		t.Fatalf("reference path = %q, want components/ui/toaster.tsx", refs.References[0].Path)
	}
}

// Resolving a call must not rewrite the shared name index. Filtering a call
// target in place used to overwrite the byName backing array, so a later type
// reference with the same name became ambiguous depending on query order.
func TestCallResolutionDoesNotCorruptNameIndex(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"lib/type.ts": `export type Status = "ok";
`,
		"lib/fn.ts": `export function Status() { return "ok"; }
`,
		"app/use.ts": `import { Status } from "../lib/fn";
export function run() { return Status(); }
`,
		"app/declare.ts": `import { Status } from "../lib/type";
export const state: Status = "ok";
`,
	})
	idx := mustIndex(t, root)
	// The call resolves first; the reference must still resolve afterwards.
	if got := mustRefs(t, idx, RefQuery{Symbol: "Status", Path: "lib/fn.ts", Relation: RelationCallers}); !got.Found || len(got.Edges) != 1 {
		t.Fatalf("callers = found:%v edges:%+v", got.Found, got.Edges)
	}
	if got := mustRefs(t, idx, RefQuery{Symbol: "Status", Path: "lib/type.ts"}); !got.Found || got.TotalReferences != 1 {
		t.Fatalf("type refs = found:%v total:%d refs:%+v", got.Found, got.TotalReferences, got.References)
	}
}

// Two imported modules can both declare the name, so the import does not prove
// which one the reference means. That case must stay ambiguous and emit no
// edge rather than picking one arbitrarily.
func TestTwoImportedDuplicateNamesStayAmbiguous(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"components/a/Widget.tsx": `export function Widget() { return null; }
`,
		"components/b/Widget.tsx": `export function Widget() { return null; }
`,
		"app/page.tsx": `export default function Page() { return <Widget />; }
`,
	})
	idx := mustIndex(t, root)
	refs := mustRefs(t, idx, RefQuery{Symbol: "Widget"})
	if !refs.Ambiguous || refs.TotalReferences != 0 {
		t.Fatalf("duplicate global resolution = ambiguous:%v total:%d refs:%+v",
			refs.Ambiguous, refs.TotalReferences, refs.References)
	}
}

// A `typeof X` type query reads the value X; wrappers use it to derive prop
// types from a component. Counting only `type_identifier` and JSX openings
// under-reported those components as unused.
func TestTypeQueryCountsAsValueReference(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"components/button.tsx": "export function Button() { return null; }\n",
		"components/wrapper.tsx": `import { Button } from "./button";
export type WrapperProps = { as?: React.ComponentProps<typeof Button> };
`,
	})
	idx := mustIndex(t, root)
	refs := mustRefs(t, idx, RefQuery{Symbol: "Button"})
	if !refs.Found || refs.TotalReferences != 1 {
		t.Fatalf("typeof refs = found:%v total:%d refs:%+v", refs.Found, refs.TotalReferences, refs.References)
	}
	if refs.References[0].Path != "components/wrapper.tsx" {
		t.Fatalf("typeof reference path = %q, want components/wrapper.tsx", refs.References[0].Path)
	}
}

// A qualified call whose qualifier is not a known declaration or import does
// not prove it calls a same-named local function. `console.log()` must stay
// unresolved even when the repository declares `log`.
func TestUnknownQualifierDoesNotResolveToBareName(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"lib/log.ts": `export function log(message: string) { return message; }
`,
		"app/use.ts": `export function run() { console.log("hello"); }
`,
	})
	idx := mustIndex(t, root)
	refs := mustRefs(t, idx, RefQuery{Symbol: "log", Path: "lib/log.ts", Relation: RelationCallers})
	if !refs.Found || len(refs.Edges) != 0 {
		t.Fatalf("console.log resolved to local log: found:%v edges:%+v", refs.Found, refs.Edges)
	}
}

// A namespace import is proof of the member's module even when another module
// exports the same name.
func TestNamespaceImportDisambiguatesQualifiedCall(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"lib/fmt.ts": `export function format() { return "fmt"; }
`,
		"lib/other.ts": `export function format() { return "other"; }
`,
		"app/use.ts": `import * as fmt from "../lib/fmt";
export function run() { return fmt.format(); }
`,
	})
	idx := mustIndex(t, root)
	refs := mustRefs(t, idx, RefQuery{Symbol: "format", Path: "lib/fmt.ts", Relation: RelationCallers})
	if !refs.Found || len(refs.Edges) != 1 || refs.Edges[0].CallPath != "app/use.ts" {
		t.Fatalf("namespace call = found:%v edges:%+v", refs.Found, refs.Edges)
	}
	other := mustRefs(t, idx, RefQuery{Symbol: "format", Path: "lib/other.ts", Relation: RelationCallers})
	if !other.Found || len(other.Edges) != 0 {
		t.Fatalf("namespace call leaked to other module: %+v", other.Edges)
	}
}

// The packages view advertises a directory-shaped package name for languages
// without a package clause. The package selector must accept that same name;
// otherwise a consumer that reads the row and asks for its files or symbols
// gets a false not-found.
func TestNonGoPackageSelectorMatchesAdvertisedName(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"components/blog/TerminalBlog.tsx": "export function TerminalBlog() { return null; }\n",
		"components/blog/TitleBar.tsx":     "export function TitleBar() { return null; }\n",
		"app/page.tsx":                     "export default function Page() { return null; }\n",
	})
	idx := mustIndex(t, root)
	packages, err := idx.Packages(context.Background(), PackageQuery{Name: "components/blog"})
	if err != nil {
		t.Fatal(err)
	}
	if len(packages.Packages) != 1 || packages.Packages[0].Name != "components/blog" {
		t.Fatalf("packages = %+v, want components/blog", packages.Packages)
	}
	if packages.Packages[0].Symbols != 2 {
		t.Fatalf("package symbols = %d, want 2", packages.Packages[0].Symbols)
	}
	files, err := idx.Files(context.Background(), FileQuery{Package: "components/blog"})
	if err != nil {
		t.Fatal(err)
	}
	if files.Total != 2 {
		t.Fatalf("package files = %d, want 2 (%+v)", files.Total, files.Files)
	}
	symbols, err := idx.Package("components/blog")
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 2 {
		t.Fatalf("Package symbols = %+v, want 2", symbols)
	}
}

// A zero-reference answer is still subject to the same static blind spots as a
// non-zero one. The early return for an empty reference set skipped the
// completeness demotion, so a repository with unresolved references reported
// "complete: true, total: 0" for an unrelated declaration.
func TestZeroReferencesDoesNotClaimCompletenessWithUnresolvedSites(t *testing.T) {
	root := auditFixture(t, map[string]string{
		"components/card.tsx": "export function Card() { return null; }\n",
		"app/page.tsx":        "export default function Page() { return <Missing />; }\n",
	})
	idx := mustIndex(t, root)
	refs := mustRefs(t, idx, RefQuery{Symbol: "Card"})
	if !refs.Found || refs.TotalReferences != 0 {
		t.Fatalf("Card refs = found:%v total:%d", refs.Found, refs.TotalReferences)
	}
	if refs.Unknown.GenericUnresolved == 0 {
		t.Fatalf("fixture produced no unresolved site: %+v", refs.Unknown)
	}
	if refs.Complete {
		t.Fatalf("zero-reference answer claimed completeness despite %d unresolved sites",
			refs.Unknown.GenericUnresolved)
	}
}
