package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The plain REPL's /copy must select and transport exactly what the full-screen
// command does, or the same slash line would mean two different things.
func TestReplCopyEmitsSmartPayload(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TMUX", "")
	t.Setenv("OHGO_CLIPBOARD", "")

	history := []agent.Message{
		agent.UserMessage{Text: "show me"},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "Here:\n\n```go\nfunc main() {}\n```\n"},
		}}},
	}
	var out bytes.Buffer
	if err := repl(&harness{}, &out, strings.NewReader("/copy\n/quit\n"), history); err != nil {
		t.Fatalf("repl: %v", err)
	}

	emitted := out.String()
	start := strings.Index(emitted, "\x1b]52;c;")
	if start < 0 {
		t.Fatalf("no OSC 52 sequence in %q", emitted)
	}
	payload := emitted[start+len("\x1b]52;c;"):]
	if end := strings.IndexByte(payload, '\x07'); end >= 0 {
		payload = payload[:end]
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("clipboard payload is not base64: %v", err)
	}
	if string(decoded) != "func main() {}" {
		t.Fatalf("copied %q, want the code body", decoded)
	}
	if !strings.Contains(emitted, "Copied code block.") {
		t.Fatalf("missing confirmation in %q", emitted)
	}
}

// A code-edit turn can answer with prose only. The diff the tool reported is
// still the most valuable thing to copy, and the REPL must select it exactly
// as the full-screen command does.
func TestReplCopyUsesTurnDiffWhenProseHasNone(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TMUX", "")
	t.Setenv("OHGO_CLIPBOARD", "")

	const diff = "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new"
	history := []agent.Message{
		agent.UserMessage{Text: "fix it"},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "Done."},
		}}},
	}
	var out bytes.Buffer
	copyREPL(&out, history, diff, nil)

	emitted := out.String()
	start := strings.Index(emitted, "\x1b]52;c;")
	if start < 0 {
		t.Fatalf("no OSC 52 sequence in %q", emitted)
	}
	payload := emitted[start+len("\x1b]52;c;"):]
	if end := strings.IndexByte(payload, '\x07'); end >= 0 {
		payload = payload[:end]
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("clipboard payload is not base64: %v", err)
	}
	if string(decoded) != diff {
		t.Fatalf("copied %q, want the turn diff", decoded)
	}
	if !strings.Contains(emitted, "Copied unified diff.") {
		t.Fatalf("missing diff confirmation in %q", emitted)
	}
}

func TestToolDiffsJoinsMutationsAndDropsDuplicates(t *testing.T) {
	const first = "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a\n+b"
	const second = "--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-c\n+d"
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{
			{ToolName: "edit", Result: agent.Result{Details: map[string]any{"diff": first}}},
			{ToolName: "read", Result: agent.Result{Details: map[string]any{}}},
		}},
		{Results: []agent.ToolOutcome{
			{ToolName: "write", Result: agent.Result{Details: map[string]any{"diff": second}}},
			{ToolName: "edit", Result: agent.Result{Details: map[string]any{"diff": first}}},
		}},
	}
	got := toolDiffs(turns)
	if got != first+"\n"+second {
		t.Fatalf("toolDiffs = %q, want both distinct diffs in order", got)
	}
}

// A terminal that cannot accept OSC 52 must say so rather than printing the
// payload and calling it a copy.
func TestReplCopyReportsUnavailableClipboard(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("TMUX", "")
	t.Setenv("OHGO_CLIPBOARD", "")

	history := []agent.Message{
		agent.UserMessage{Text: "show me"},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "Here:\n\n```go\nfunc main() {}\n```\n"},
		}}},
	}
	var out bytes.Buffer
	if err := repl(&harness{}, &out, strings.NewReader("/copy\n/quit\n"), history); err != nil {
		t.Fatalf("repl: %v", err)
	}
	if strings.Contains(out.String(), "\x1b]52;c;") {
		t.Fatal("a dumb terminal emitted a clipboard sequence")
	}
	if !strings.Contains(out.String(), "clipboard is unavailable") {
		t.Fatalf("output = %q, want an unavailable notice", out.String())
	}
}
