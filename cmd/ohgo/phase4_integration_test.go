package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

func phase4Fixture(t *testing.T, grepFixed bool) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":         "module example.com/phase4\n\ngo 1.21\n",
		"note.txt":       "old\n",
		"important.txt":  "keep\n",
		"docs/README.md": "docs\n",
		"tools/grep_test.go": `package tools

import "testing"

func TestGrepTool(t *testing.T) {
	if GrepTool() != "fixed" {
		t.Fatal("GrepTool is not fixed")
	}
}
`,
		"ast/ast.go": `package ast

func Value() int { return 1 }
`,
		"ast/ast_test.go": `package ast

import "testing"

func TestValue(t *testing.T) {
	if Value() != 1 {
		t.Fatal("wrong value")
	}
}
`,
	}
	grep := "bug"
	if grepFixed {
		grep = "fixed"
	}
	files["tools/grep.go"] = `package tools

func GrepTool() string { return "` + grep + `" }
`
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

func phase4ToolSet(root string) []agent.Tool {
	return []agent.Tool{
		tools.NewReadTool(root),
		tools.NewWriteTool(root),
		tools.NewEditTool(root),
		tools.NewDeleteTool(root),
		tools.NewBashTool(root),
		tools.NewSymbolsTool(root),
	}
}

func phase4LoopWith(t *testing.T, root string, toolSet []agent.Tool, stream ai.StreamFn) (*agent.Loop, agent.StructuralResolver) {
	t.Helper()
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Build)
	gate := opts.newSecurityGate(nil)
	loop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    stream,
		Tools:     toolSet,
		Authorize: gate,
		MaxTurns:  12,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop, structuralResolver(toolSet)
}

func phase4Loop(t *testing.T, root string, stream ai.StreamFn) (*agent.Loop, agent.StructuralResolver) {
	t.Helper()
	return phase4LoopWith(t, root, phase4ToolSet(root), stream)
}

func completionEditCall(path, oldText, newText string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID:   "edit-call",
			Name: "edit",
			Arguments: map[string]any{
				"path":       path,
				"old_string": oldText,
				"new_string": newText,
			},
		}},
		StopReason: ai.StopToolUse,
	}
}

func completionDeleteCall(path string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID:        "delete-call",
			Name:      "delete",
			Arguments: map[string]any{"path": path},
		}},
		StopReason: ai.StopToolUse,
	}
}

func completionReadCall(path string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID:        "read-call",
			Name:      "read",
			Arguments: map[string]any{"path": path},
		}},
		StopReason: ai.StopToolUse,
	}
}

func runPhase4(t *testing.T, root string, resolver agent.StructuralResolver, request string, stream ai.StreamFn) agent.Completion {
	t.Helper()
	intent := workspace.BuildIntentContract(request)
	var snapshot *agent.TargetSnapshot
	if intent.Kind == workspace.ObjectiveMutation {
		snapshot = agent.CaptureTargets(root, intent, resolver)
	}
	loop, _ := phase4Loop(t, root, stream)
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: request}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    turns,
		Contract: intent,
		Snapshot: snapshot,
		Resolver: resolver,
		Root:     root,
	})
}

// A real write to the wrong file followed by a passing test suite is not
// completion. This is the end-to-end H0 shape the contract must reject.
func TestSemanticIntegrationRejectsWrongTarget(t *testing.T) {
	root := phase4Fixture(t, true)
	_, resolver := phase4Loop(t, root, scriptedCompletionStream(stopMessage()))
	verdict := runPhase4(t, root, resolver, "Update note.txt and run go test ./...", scriptedCompletionStream(
		completionWriteCall("other.txt", "changed\n"),
		completionBashCall("go test ./..."),
		stopMessage(),
	))
	if verdict.Complete() {
		t.Fatal("a wrong target was reported complete")
	}
}

// A real change followed by a revert is not completion even though two writes
// and a passing test were observed.
func TestSemanticIntegrationRejectsRevert(t *testing.T) {
	root := phase4Fixture(t, true)
	_, resolver := phase4Loop(t, root, scriptedCompletionStream(stopMessage()))
	verdict := runPhase4(t, root, resolver, "Update note.txt and run go test ./...", scriptedCompletionStream(
		completionWriteCall("note.txt", "changed\n"),
		completionWriteCall("note.txt", "old\n"),
		completionBashCall("go test ./..."),
		stopMessage(),
	))
	if verdict.Complete() {
		t.Fatal("a reverted target was reported complete")
	}
}

// A real symbol edit, located through the existing symbols index, is complete
// when the tests for that symbol's package pass.
func TestSemanticIntegrationAcceptsSymbolTarget(t *testing.T) {
	root := phase4Fixture(t, false)
	_, resolver := phase4Loop(t, root, scriptedCompletionStream(stopMessage()))
	request := "Fix GrepTool in tools/grep.go so `go test ./tools` passes."
	verdict := runPhase4(t, root, resolver, request, scriptedCompletionStream(
		completionEditCall("tools/grep.go", "bug", "fixed"),
		completionBashCall("go test ./tools"),
		stopMessage(),
	))
	if !verdict.Complete() {
		t.Fatalf("verified symbol edit was not complete: %s", verdict.Reason)
	}
}

// The same edit checked only in another package is not verified.
func TestSemanticIntegrationRejectsUnrelatedPackageCheck(t *testing.T) {
	root := phase4Fixture(t, false)
	_, resolver := phase4Loop(t, root, scriptedCompletionStream(stopMessage()))
	request := "Fix GrepTool in tools/grep.go so `go test ./tools` passes."
	verdict := runPhase4(t, root, resolver, request, scriptedCompletionStream(
		completionEditCall("tools/grep.go", "bug", "fixed"),
		completionBashCall("go test ./ast"),
		stopMessage(),
	))
	if verdict.Complete() {
		t.Fatal("an unrelated package's check was accepted")
	}
}

// A correct edit plus an unrelated delete is not complete.
func TestSemanticIntegrationRejectsUnexpectedDelete(t *testing.T) {
	root := phase4Fixture(t, true)
	_, resolver := phase4Loop(t, root, scriptedCompletionStream(stopMessage()))
	verdict := runPhase4(t, root, resolver, "Update note.txt and run go test ./...", scriptedCompletionStream(
		completionWriteCall("note.txt", "changed\n"),
		completionDeleteCall("important.txt"),
		completionBashCall("go test ./..."),
		stopMessage(),
	))
	if verdict.Complete() {
		t.Fatal("an out-of-scope delete was reported complete")
	}
}

// A read request that only reads remains a complete informational turn.
func TestSemanticIntegrationAcceptsInformationalRead(t *testing.T) {
	root := phase4Fixture(t, true)
	_, resolver := phase4Loop(t, root, scriptedCompletionStream(stopMessage()))
	verdict := runPhase4(t, root, resolver, "Explain what GrepTool returns.", scriptedCompletionStream(
		completionReadCall("tools/grep.go"),
		stopMessage(),
	))
	if !verdict.Complete() {
		t.Fatalf("informational read was not complete: %s", verdict.Reason)
	}
}

// A read request that writes is not a successful read, even if a later check
// passes.
func TestSemanticIntegrationRejectsMutationDuringRead(t *testing.T) {
	root := phase4Fixture(t, true)
	_, resolver := phase4Loop(t, root, scriptedCompletionStream(stopMessage()))
	verdict := runPhase4(t, root, resolver, "Explain what GrepTool returns.", scriptedCompletionStream(
		completionReadCall("tools/grep.go"),
		completionWriteCall("note.txt", "changed\n"),
		completionBashCall("go test ./..."),
		stopMessage(),
	))
	if verdict.Complete() {
		t.Fatal("a mutating read request was reported complete")
	}
}
