package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// wireSignature renders the parts of a request a provider cache keys on, in the
// order it sees them.
func wireSignature(req ai.Request) string {
	var sb strings.Builder
	for _, block := range req.System {
		sb.WriteString(block.Text)
		sb.WriteString("\x00")
	}
	for _, spec := range req.Tools {
		sb.WriteString(spec.Name)
		sb.WriteString("\x00")
		sb.WriteString(spec.Description)
		sb.WriteString("\x00")
		sb.Write(spec.Parameters)
		sb.WriteString("\x00")
	}
	return sb.String()
}

// runtimeTail returns the request's trailing runtime state, or "".
func runtimeTail(messages []ai.Message) string {
	if len(messages) == 0 {
		return ""
	}
	tail, ok := messages[len(messages)-1].(ai.RuntimeStateMessage)
	if !ok {
		return ""
	}
	return tail.Text
}

// A stable semantic state must produce a byte-identical stable prefix on every
// request. The consumer-visible failure when this regresses is a provider that
// re-reads the whole conversation every turn.
func TestStablePrefixSurvivesConsecutiveTurns(t *testing.T) {
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "one"}}, StopReason: ai.StopEndTurn},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "two"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{newScriptedTool("read", "x"), newScriptedTool("grep", "y")},
		System: []ai.SystemPromptBlock{{Text: "stable contract", Cache: true}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loop.SetRuntimeContext([]string{"runtime state: idle"})
	ctx := context.Background()
	for _, text := range []string{"first request", "second request"} {
		if _, err := loop.Run(ctx, []Message{UserMessage{Text: text}}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	got := requests()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}
	if wireSignature(got[0]) != wireSignature(got[1]) {
		t.Fatalf("stable prefix changed between turns:\n first  %q\n second %q",
			wireSignature(got[0]), wireSignature(got[1]))
	}
}

// A new user request is conversation content, never prefix content: it must not
// reach the system prompt or the tool definitions.
func TestUserRequestDoesNotTouchTheStablePrefix(t *testing.T) {
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "one"}}, StopReason: ai.StopEndTurn},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "two"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{newScriptedTool("read", "x")},
		System: []ai.SystemPromptBlock{{Text: "stable contract"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "explain the cache"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "now change the parser"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := requests()
	if wireSignature(got[0]) != wireSignature(got[1]) {
		t.Fatal("a new user request rewrote the stable prefix")
	}
	if strings.Contains(wireSignature(got[1]), "explain the cache") ||
		strings.Contains(wireSignature(got[1]), "now change the parser") {
		t.Fatal("user text entered the system prompt or the tool definitions")
	}
}

// The per-turn runtime briefing is the request's last message and appears
// exactly once: it is a view of this request, not accumulated state.
func TestRuntimeStateIsASingleTrailingMessage(t *testing.T) {
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": "a"}},
		}, StopReason: ai.StopToolUse},
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c2", Name: "read", Arguments: map[string]any{"value": "b"}},
		}, StopReason: ai.StopToolUse},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{newScriptedTool("read", "x")},
		System: []ai.SystemPromptBlock{{Text: "stable contract"}},
		Project: func(req ProjectionRequest) ProjectionResult {
			return ProjectionResult{Runtime: []string{"# Runtime state\n\nturn briefing"}}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := requests()
	if len(got) != 3 {
		t.Fatalf("requests = %d, want 3", len(got))
	}
	for i, request := range got {
		seen := 0
		for _, message := range request.Messages {
			if _, ok := message.(ai.RuntimeStateMessage); ok {
				seen++
			}
		}
		if seen != 1 {
			t.Fatalf("request %d carries %d runtime state messages, want exactly 1", i, seen)
		}
		if tail := runtimeTail(request.Messages); !strings.Contains(tail, "turn briefing") {
			t.Fatalf("request %d tail = %q, want the briefing last", i, tail)
		}
		// The conversation grows by the assistant turn and its tool result,
		// never by a replayed briefing or a duplicated observation.
		want := 2 + 2*i
		if len(request.Messages) != want {
			t.Fatalf("request %d messages = %d, want %d", i, len(request.Messages), want)
		}
	}
}

// A role change rebuilds the system prefix — and nothing else. Losing the tool
// surface or the conversation on a role switch would be a separate defect.
func TestRoleChangeRewritesOnlyTheAuthorityContract(t *testing.T) {
	contracts := map[string]string{
		"ask":   "You are operating in ASK mode: read-only understanding.",
		"build": "You are operating in BUILD mode: the mutation authority.",
	}
	role := "ask"
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "one"}}, StopReason: ai.StopEndTurn},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "two"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{newScriptedTool("read", "x"), newScriptedTool("edit", "y")},
		SystemFn: func() []ai.SystemPromptBlock {
			return []ai.SystemPromptBlock{
				{Text: "You are ohgo."},
				{Text: contracts[role], Cache: true},
			}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "same request"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	role = "build"
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "same request"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := requests()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}
	if got[0].System[1].Text == got[1].System[1].Text {
		t.Fatal("the authority contract did not change")
	}
	if got[0].System[0].Text != got[1].System[0].Text {
		t.Fatal("the role switch rewrote the identity block")
	}
	if !reflect.DeepEqual(got[0].Tools, got[1].Tools) {
		t.Fatal("the role switch rewrote the tool surface")
	}
}

// Equivalent tool registries must serialize identically: definitions are the
// largest stable part of an agent prompt.
func TestToolDefinitionsAreDeterministic(t *testing.T) {
	tools := []Tool{
		newScriptedTool("read", "x"),
		newScriptedTool("grep", "y"),
		newScriptedTool("edit", "z"),
	}
	first, err := json.Marshal(Specs(tools))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	second, err := json.Marshal(Specs(tools))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("the same tool registry serialized differently twice")
	}
	firstDigest := digestTools(Specs(tools))
	if secondDigest := digestTools(Specs(tools)); firstDigest != secondDigest {
		t.Fatal("the tool digest is not stable")
	}
	reordered := []Tool{tools[1], tools[0], tools[2]}
	if digestTools(Specs(tools)) == digestTools(Specs(reordered)) {
		t.Fatal("a reordered surface hashed identically; the digest cannot detect a tool change")
	}
	var specs []ai.ToolSpec
	if err := json.Unmarshal(first, &specs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, spec := range specs {
		if spec.Name != tools[i].Name() {
			t.Fatalf("spec %d = %q, want the registry order", i, spec.Name)
		}
	}
}
