package tools

import (
	"strings"
	"testing"
)

// nextAppFixture is the shape that produced the original contradiction: a
// TypeScript App Router project with no adapter for its sources. The overview
// counted route-module discoveries while the entrypoints view counted only
// adapter-proven entries, so the two views disagreed about the same repository.
func nextAppFixture(t *testing.T) string {
	t.Helper()
	return symbolsFixture(t, map[string]string{
		"package.json":                  `{"name":"app","dependencies":{"next":"16.2.4","react":"19.0.0"}}`,
		"app/layout.tsx":                "export default function RootLayout() {}\n",
		"app/page.tsx":                  "export default function Page() {}\n",
		"app/info/page.tsx":             "export default function Info() {}\n",
		"app/posts/[slug]/page.tsx":     "export default function Post() {}\n",
		"components/ui/button.tsx":      "export const Button = 1\n",
		"components/theme-provider.tsx": "export function ThemeProvider() {}\n",
	})
}

// Every entry coordinate is either adapter-proven or convention-discovered,
// and both views report the same list with the same provenance split. A count
// in one view that the other cannot reproduce is a contradiction a consumer
// cannot resolve.
func TestStructureEntrypointsAgreeAcrossViews(t *testing.T) {
	tool := NewStructureTool(NewStructuralSource(nextAppFixture(t)))
	ctx := testContext()

	overview := tool.Execute(ctx, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	if !strings.Contains(overview.Text, "entrypoints: 4 (verified: 0, discovered: 4)") {
		t.Fatalf("overview entrypoint totals = %s", overview.Text)
	}
	// The overview is metadata-first, so the sources are still pending. What
	// matters is that the header never presents the snapshot's freshness as
	// structural completeness.
	if !strings.Contains(overview.Text, "graph_state: CURRENT (snapshot freshness only; structural is not complete)") {
		t.Fatalf("overview claimed more than snapshot freshness:\n%s", overview.Text)
	}
	if strings.Contains(overview.Text, "structural: COMPLETE") {
		t.Fatalf("unparsed sources were reported as structurally complete:\n%s", overview.Text)
	}

	view := tool.Execute(ctx, map[string]any{"view": "entrypoints"})
	if view.IsError {
		t.Fatalf("entrypoints view: %s", view.Text)
	}
	if !strings.Contains(view.Text, "status: ok") {
		t.Fatalf("entrypoints view reported %q for a repository with discovered entries:\n%s",
			firstStatusLine(view.Text), view.Text)
	}
	if !strings.Contains(view.Text, "total: 4 (verified: 0, discovered: 4)") {
		t.Fatalf("entrypoints view totals = %s", view.Text)
	}
	if !strings.Contains(view.Text, "verified: false") {
		t.Fatalf("a convention discovery was presented without its provenance:\n%s", view.Text)
	}

	overviewDetails := structureDetails(t, overview)
	viewDetails := structureDetails(t, view)
	overviewRows, _ := overviewDetails["entrypoints"].([]map[string]any)
	if len(overviewRows) != viewDetails["total"] {
		t.Fatalf("overview rows = %d, entrypoints view total = %v", len(overviewRows), viewDetails["total"])
	}
	if got := viewDetails["verified"]; got != 0 {
		t.Errorf("verified = %v, want 0 for an adapter-less snapshot", got)
	}
}

// A proven entry keeps its verified provenance, and the totals still add up.
func TestStructureProvenGoEntrypointStaysVerified(t *testing.T) {
	tool := NewStructureTool(NewStructuralSource(structureFixture(t, map[string]string{
		"cmd/app/main.go": "package main\n\nfunc main() {}\n",
	})))
	view := tool.Execute(testContext(), map[string]any{"view": "entrypoints"})
	if view.IsError {
		t.Fatalf("entrypoints view: %s", view.Text)
	}
	if !strings.Contains(view.Text, "total: 1 (verified: 1, discovered: 0)") {
		t.Fatalf("proven entrypoint totals = %s", view.Text)
	}
	if !strings.Contains(view.Text, "verified: true") {
		t.Fatalf("proven entrypoint lost its provenance:\n%s", view.Text)
	}
}

// firstStatusLine returns the status header for a failure message.
func firstStatusLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "status: ") {
			return strings.TrimPrefix(line, "status: ")
		}
	}
	return "?"
}
