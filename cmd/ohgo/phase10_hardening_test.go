package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/tools"
)

// A read-only delegation must actually receive a read-only tool set. The
// spawner is the only place that can narrow it before RunSubagent's fail-closed
// validation, so this helper is the wiring point.
func TestReadOnlyToolSetKeepsOnlyObservationTools(t *testing.T) {
	root := t.TempDir()
	all := []agent.Tool{
		tools.NewReadTool(root),
		tools.NewWriteTool(root),
		tools.NewBashTool(root),
		tools.NewTodoTool(),
	}
	filtered := readOnlyToolSet(all)
	if len(filtered) != 2 {
		t.Fatalf("read-only set = %d tools, want read and todo", len(filtered))
	}
	names := map[string]bool{}
	for _, tool := range filtered {
		names[tool.Name()] = true
	}
	for _, want := range []string{"read", "todo"} {
		if !names[want] {
			t.Fatalf("read-only set is missing %q: %v", want, names)
		}
	}
	for _, forbidden := range []string{"write", "bash"} {
		if names[forbidden] {
			t.Fatalf("read-only set contains %q", forbidden)
		}
	}
	if !agent.IsReadOnly(filtered) {
		t.Fatal("filtered set is not recognized as read-only")
	}
}
