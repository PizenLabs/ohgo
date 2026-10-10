package main

import (
	"context"
	"errors"
	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/session"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSummarizerSendsTheConversation(t *testing.T) {
	// The summarizer must give the model the actual history: a summary built
	// from anything else would lose the context compaction is meant to
	// preserve.
	var gotRequest ai.Request
	registry := ai.NewRegistry()
	registry.SetAPIKey("anthropic", "test")

	stub := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		gotRequest = req
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "a summary"}},
		}), nil
	}
	summarize := newSummarizer(stub, catalog.MustResolve("anthropic/claude-sonnet-4-5"))

	text, err := summarize(context.Background(), []agent.Message{
		agent.UserMessage{Text: "read a.go"},
		agent.AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "reading"}},
		}},
		agent.ToolResultMessage{
			ToolCallID: "c1", ToolName: "read",
			Content: []ai.ContentBlock{ai.TextContent{Text: "package a"}},
		},
		agent.Notification{Text: "UI chatter that must never reach the model"},
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if text != "a summary" {
		t.Errorf("summary = %q, want the model's output", text)
	}

	if !strings.Contains(gotRequest.System[1].Text, "Summarise") {
		t.Errorf("system prompt = %q, want the summarisation instruction", gotRequest.System[1].Text)
	}
	sent := gotRequest.Messages[0].(ai.UserMessage).Text()
	for _, want := range []string{"read a.go", "reading", "package a"} {
		if !strings.Contains(sent, want) {
			t.Errorf("summarizer input missing %q: %s", want, sent)
		}
	}
	// A UI-only message must not be summarised: it was never sent to the model.
	if strings.Contains(sent, "UI chatter") {
		t.Errorf("a UI-only message reached the summarizer: %s", sent)
	}
}

func TestNewSummarizerPropagatesStreamFailure(t *testing.T) {
	summarize := newSummarizer(func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return nil, errSummarizeStreamFailed
	}, catalog.MustResolve("anthropic/claude-sonnet-4-5"))
	if _, err := summarize(context.Background(), []agent.Message{agent.UserMessage{Text: "x"}}); err == nil {
		t.Fatal("expected a summarizer failure to surface")
	}
}

var errSummarizeStreamFailed = errors.New("stream unavailable")

func TestPersistTurnWritesTheTranscript(t *testing.T) {
	// A replayed session must show the model's answer, so the assistant turn
	// has to be persisted alongside the tool results.
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sess, err := store.Create("/tmp", "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}

	turn := agent.Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "here is the answer"},
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a"}},
			},
			StopReason: ai.StopToolUse,
			Model:      catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		},
		Results: []agent.ToolOutcome{{
			CallID:   "c1",
			ToolName: "read",
			Result:   agent.Result{Text: "file contents"},
		}},
	}
	persistTurn(store, sess, turn)

	messages, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("persisted %d messages, want the assistant turn and the tool result", len(messages))
	}
	if messages[0].Role != "assistant" || !strings.Contains(messages[0].Text, "here is the answer") {
		t.Errorf("assistant turn = %+v, want the model's text", messages[0])
	}
	if len(messages[0].Calls) != 1 || messages[0].Calls[0].ID != "c1" {
		t.Errorf("assistant calls = %+v, want the tool call", messages[0].Calls)
	}
	if messages[1].Role != "toolResult" || messages[1].Text != "file contents" {
		t.Errorf("tool result = %+v, want the executed result", messages[1])
	}
}

func TestCompactionFromSettingsHonoursOverrides(t *testing.T) {
	disabled := false
	keep := 3
	pct := 70.0
	settings := configSettingsWithCompaction(&disabled, keep, pct)

	c := compactionFromSettings(settings)
	if c.Enabled {
		t.Error("an explicit disabled compaction was overridden")
	}
	if c.KeepRecent != 3 {
		t.Errorf("keepRecent = %d, want 3", c.KeepRecent)
	}
	if c.ThresholdPercent != 70 {
		t.Errorf("thresholdPercent = %v, want 70", c.ThresholdPercent)
	}
}

func TestCompactionFromSettingsDefaultsWhenUnset(t *testing.T) {
	c := compactionFromSettings(configSettingsWithCompaction(nil, 0, 0))
	if !c.Enabled {
		t.Error("compaction should default to enabled")
	}
	if c.KeepRecent <= 0 {
		t.Error("keepRecent should default to a positive value")
	}
}

// configSettingsWithCompaction builds settings with a compaction override.
func configSettingsWithCompaction(enabled *bool, keepRecent int, pct float64) config.Settings {
	return config.Settings{Compaction: &config.Compaction{
		Enabled:          enabled,
		KeepRecent:       keepRecentPtr(keepRecent),
		ThresholdPercent: pctPtr(pct),
	}}
}

func keepRecentPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

func pctPtr(f float64) *float64 {
	if f <= 0 {
		return nil
	}
	return &f
}
