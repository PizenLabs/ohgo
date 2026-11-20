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
