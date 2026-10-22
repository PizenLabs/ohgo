package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
)

// scriptedCompletionStream serves one assistant message per request.
func scriptedCompletionStream(messages ...ai.AssistantMessage) ai.StreamFn {
	var mu sync.Mutex
	index := 0
	return func(context.Context, ai.Request) (*ai.Stream, error) {
		mu.Lock()
		defer mu.Unlock()
		if index >= len(messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
		message := messages[index]
		index++
		return ai.NewCompletedStream(message), nil
	}
}

func completionWriteCall(path, content string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID:        "write-call",
			Name:      "write",
			Arguments: map[string]any{"path": path, "content": content},
		}},
		StopReason: ai.StopToolUse,
	}
}

func completionBashCall(command string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID:        "bash-call",
			Name:      "bash",
			Arguments: map[string]any{"command": command, "timeout": float64(60000)},
		}},
		StopReason: ai.StopToolUse,
	}
}

func stopMessage() ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
		StopReason: ai.StopEndTurn,
	}
}

func completionLoop(t *testing.T, root string, stream ai.StreamFn) *agent.Loop {
	t.Helper()
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Build)
	gate := opts.newSecurityGate(nil)
	loop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    stream,
		Tools:     []agent.Tool{tools.NewWriteTool(root), tools.NewBashTool(root)},
		Authorize: gate,
		MaxTurns:  10,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

func goModuleFixture(t *testing.T, failing bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/closure\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "package closure\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n"
	if failing {
		body = "package closure\n\nimport \"testing\"\n\nfunc TestBroken(t *testing.T) { t.Fatal(\"broken\") }\n"
	}
	if err := os.WriteFile(filepath.Join(root, "check_test.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A real write followed by a real successful test run is completion evidence.
func TestCompletionIntegrationAcceptsVerifiedMutation(t *testing.T) {
	root := goModuleFixture(t, false)
	loop := completionLoop(t, root, scriptedCompletionStream(
		completionWriteCall("note.txt", "changed"),
		completionBashCall("go test ./..."),
		stopMessage(),
	))

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "change note.txt and verify"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	verdict := agent.EvaluateCompletion(turns, true)
	if !verdict.Complete() {
		t.Fatalf("verified mutation was not complete: %s", verdict.Reason)
	}
}

// A real write with no verification is reviewable, not complete.
func TestCompletionIntegrationRejectsUnverifiedMutation(t *testing.T) {
	root := goModuleFixture(t, false)
	loop := completionLoop(t, root, scriptedCompletionStream(
		completionWriteCall("note.txt", "changed"),
		stopMessage(),
	))

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "change note.txt"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if agent.EvaluateCompletion(turns, true).Complete() {
		t.Fatal("an unverified write was reported complete")
	}
}

// A real write followed by a failing test run is not complete.
func TestCompletionIntegrationRejectsFailedVerification(t *testing.T) {
	root := goModuleFixture(t, true)
	loop := completionLoop(t, root, scriptedCompletionStream(
		completionWriteCall("note.txt", "changed"),
		completionBashCall("go test ./..."),
		stopMessage(),
	))

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "change note.txt and verify"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if agent.EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a failing verification was reported complete")
	}
}
