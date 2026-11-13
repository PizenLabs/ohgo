//go:build !cgo

package tools

import (
	"strings"
	"testing"
)

// TestStructureToolDisclosesGoOnlyAdapters is the shipped-binary contract: a
// Go-only build inventories a TypeScript project but cannot parse it, so the
// header must name the adapters it has and call the snapshot UNSUPPORTED.
// Without both lines a model cannot tell "this project has no symbols" from
// "this binary cannot see symbols".
func TestStructureToolDisclosesGoOnlyAdapters(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"package.json":      `{"name":"web-test5"}`,
		"tsconfig.json":     `{"compilerOptions":{}}`,
		"next.config.ts":    "export default {};\n",
		"eslint.config.mjs": "export default [];\n",
		"app/page.tsx":      "export default function Page() { return null; }\n",
		"app/layout.tsx":    "export default function Layout() { return null; }\n",
		"app/portfolio.tsx": "export function Portfolio() { return null; }\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))

	overview := tool.Execute(testContext(), map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	if !strings.Contains(overview.Text, "adapters: go\n") {
		t.Errorf("overview did not disclose the Go-only adapter set:\n%s", overview.Text)
	}
	if !strings.Contains(overview.Text, "structural: UNSUPPORTED (no adapter for .mjs, .ts, .tsx; 0 of 5 source files parsed)") {
		t.Errorf("overview misrepresented an unparseable snapshot:\n%s", overview.Text)
	}
	if strings.Contains(overview.Text, "structural: PARTIAL") {
		t.Errorf("overview labelled an unparseable snapshot PARTIAL:\n%s", overview.Text)
	}

	details := structureDetails(t, overview)
	if details["completeness"] != "UNSUPPORTED" {
		t.Errorf("completeness = %v, want UNSUPPORTED", details["completeness"])
	}
	if complete, _ := details["complete"].(bool); complete {
		t.Errorf("unsupported snapshot was reported complete: %+v", details)
	}
	// A bare "tests: 0" here is what makes an answer claim that the project has
	// no tests. The declarations were never observable.
	for _, line := range []string{
		"symbols: unobservable (no adapter)",
		"references: unobservable (no adapter)",
		"calls: unobservable (no adapter)",
		"tests: unobservable (no adapter)",
		"test_files: 0",
	} {
		if !strings.Contains(overview.Text, line) {
			t.Errorf("overview did not disclose %q:\n%s", line, overview.Text)
		}
	}
}
