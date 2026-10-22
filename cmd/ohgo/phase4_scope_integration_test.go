package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/workspace"
)

// A check that changes into a workspace subdirectory is evidence for that
// subdirectory, not for the module root. The rebased scope must satisfy the
// target and preserve a correct completion.
func TestSemanticIntegrationAcceptsInsideDirectoryVerification(t *testing.T) {
	root := phase4Fixture(t, false)
	_, resolver := phase4Loop(t, root, scriptedCompletionStream(stopMessage()))
	request := "Fix GrepTool in tools/grep.go so `go test ./tools` passes."
	verdict := runPhase4(t, root, resolver, request, scriptedCompletionStream(
		completionEditCall("tools/grep.go", "bug", "fixed"),
		completionBashCall("cd tools && go test ./..."),
		stopMessage(),
	))
	if !verdict.Complete() {
		t.Fatalf("an inside-directory check was not complete: %s", verdict.Reason)
	}
}

// A check executed in another module via "cd" says nothing about this
// workspace, even when the relative package name is the same. A wrong local
// change must not inherit the foreign module's passing test.
func TestSemanticIntegrationRejectsForeignDirectoryVerification(t *testing.T) {
	root := phase4Fixture(t, false)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(other, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "tools", "tools_test.go"), []byte(`package tools
import "testing"
func TestOther(t *testing.T) {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	request := "Fix GrepTool in tools/grep.go so `go test ./tools` passes."
	intent := workspace.BuildIntentContract(request)
	toolSet := phase4ToolSet(root)
	resolver := structuralResolver(toolSet)
	snapshot := agent.CaptureTargets(root, intent, resolver)
	loop, _ := phase4LoopWith(t, root, toolSet, scriptedCompletionStream(
		completionEditCall("tools/grep.go", "bug", "wrong"),
		completionBashCall("cd "+other+" && go test ./tools"),
		stopMessage(),
	))
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: request}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.Verifies && outcome.Result.Verification != nil {
				t.Logf("verification command=%q scopes=%+v", outcome.Result.Verification.Command, outcome.Result.Verification.Scopes)
			}
		}
	}
	verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: turns, Contract: intent, Snapshot: snapshot, Resolver: resolver, Root: root,
	})
	t.Logf("verdict=%s reason=%s", verdict.State, verdict.Reason)
	if verdict.Complete() {
		t.Fatal("a foreign module's passing check satisfied the workspace target")
	}
}
