package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The production mutation-completion path, end to end: a real turn entrypoint,
// real write and bash tools against a real module, the runtime projection, the
// completion evaluator, the durable evidence boundary, a checkpoint, and a
// restart.
//
// The controller-level tests record evidence directly, which bypasses the tool
// and projection path. This test is the one that proves the wiring: the change
// is observed by the cognition projector, the proof references that observation
// and the verification after it, and the objective is PROVEN only once the whole
// path succeeds.
func TestPhase3ProductionMutationCompletionThroughRuntime(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root)
	role := workspace.Build
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"
	index := 0
	messages := []ai.AssistantMessage{
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:   "write-1",
				Name: "write",
				Arguments: map[string]any{
					"path":    "tools/grep.go",
					"content": "package tools\n\nfunc GrepTool() string { return \"new\" }\n",
				},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:        "check-1",
				Name:      "bash",
				Arguments: map[string]any{"command": "go test ./tools"},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "implemented and verified"}},
			StopReason: ai.StopEndTurn,
		},
	}
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		message := messages[index]
		index++
		return ai.NewCompletedStream(message), nil
	}
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   stream,
		Tools:    []agent.Tool{tools.NewWriteTool(root), tools.NewBashTool(root)},
		MaxTurns: 8,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	loop.SetApprovalHook(func(ai.ToolCall, agent.Approval) (bool, string) {
		return true, "approved by the integration test"
	})
	program := NewProgram(loop, "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{Current: func() string { return string(role) }})
	program.SetCompletionContext(root, fixedResolver{})
	program.SetTaskHooks(runtimeHooks(controller))

	if _, err := os.Stat(filepath.Join(root, "tools", "grep.go")); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	program.runTurn(objective)
	drainProgram(program)

	completed, ok := controller.Current()
	if !ok {
		t.Fatal("the turn did not create a durable task")
	}
	if completed.Status != task.StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED (err=%s)", completed.Status, completed.ContextWarning)
	}
	if completed.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("objective = %s, want PROVEN: %s", completed.ObjectiveState(), completed.CompletionSummary())
	}
	proof, ok := completed.CompletionProof()
	if !ok {
		t.Fatal("PROVEN had no completion proof")
	}
	if !proof.Observed || len(proof.Verified) == 0 || len(proof.EvidenceIDs) == 0 {
		t.Fatalf("proof = %+v, want an observed change, its verification, and evidence ids", proof)
	}

	// The mutation was observed and is associated with the contract target.
	mutation := evidenceForKind(completed, task.EvidenceArtifact)
	if mutation == nil {
		t.Fatalf("evidence = %+v, want the observed change", completed.Context.Evidence)
	}
	if !strings.Contains(mutation.Source, "tools/grep.go") && !strings.Contains(mutation.Summary, "tools/grep.go") {
		t.Fatalf("change evidence = %+v, want the contract target", mutation)
	}
	verification := evidenceForKind(completed, task.EvidenceTest)
	if verification == nil {
		t.Fatalf("evidence = %+v, want the verification observation", completed.Context.Evidence)
	}
	if !proof.VerifiesEvidence(verification.ID) {
		t.Fatalf("proof = %+v, want the verification %q", proof, verification.ID)
	}
	// The actual file on disk changed, which is the production definition of
	// "observed change" the proof depends on.
	data, err := os.ReadFile(filepath.Join(root, "tools", "grep.go"))
	if err != nil {
		t.Fatalf("read changed file: %v", err)
	}
	if !strings.Contains(string(data), "new") {
		t.Fatalf("file content = %q, want the written change", data)
	}

	// A restart keeps the identity and the valid proof.
	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return role })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.ID != completed.ID {
		t.Fatalf("restored id = %q, want %q", restored.ID, completed.ID)
	}
	if restored.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("restored objective = %s, want PROVEN", restored.ObjectiveState())
	}
}

// writeModule creates a tiny offline Go module whose only package has a test,
// so the real bash tool can run a real check.
func writeModule(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":        "module example.com/phase3\n\ngo 1.21\n",
		"tools/grep.go": "package tools\n\nfunc GrepTool() string { return \"old\" }\n",
		"tools/grep_test.go": "package tools\n\nimport \"testing\"\n\n" +
			"func TestGrepTool(t *testing.T) {\n\tif GrepTool() == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// evidenceForKind returns the newest evidence record of one kind.
func evidenceForKind(t task.Task, kind task.EvidenceKind) *task.EvidenceRecord {
	for i := len(t.Context.Evidence) - 1; i >= 0; i-- {
		if t.Context.Evidence[i].Kind == kind {
			record := t.Context.Evidence[i]
			return &record
		}
	}
	return nil
}
