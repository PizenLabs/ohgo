package tools

import (
	"strings"
	"testing"
)

// A structural gap and an access gap are different facts, and the overview is
// the only place a reader sees both. The observed session read "scope: PARTIAL,
// 1 excluded source file" plus "no adapter ... 0 of 5 parsed" and concluded the
// module it needed was excluded and unreadable, so it answered from an excuse
// instead of reading the file. The header must name the excluded file and state
// that reading is unaffected.
func TestStructureOverviewSeparatesAdapterGapsFromAccessGaps(t *testing.T) {
	root := structureFixture(t, map[string]string{
		".gitignore":      "generated.pb.go\n",
		"app/main.py":     "def handler():\n    return 1\n",
		"generated.pb.go": "package app\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	out := tool.Execute(testContext(), map[string]any{"view": "overview"})
	if out.IsError {
		t.Fatalf("overview: %s", out.Text)
	}
	if !strings.Contains(out.Text, "scope_gaps: 0 excluded_roots, 1 excluded_source_files (generated.pb.go)") {
		t.Fatalf("the excluded source file is not named:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "structural: UNSUPPORTED") {
		t.Fatalf("fixture did not exercise an adapter gap:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "source_access: AVAILABLE") {
		t.Fatalf("the header does not separate adapter coverage from access:\n%s", out.Text)
	}
}
