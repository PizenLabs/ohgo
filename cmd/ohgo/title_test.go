package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/prompt"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// The titling request carries the dedicated instruction, the user's first
// prompt, and a small output budget; the model comes from the session snapshot.
func TestSessionTitleGeneratorSendsTheTitleRequest(t *testing.T) {
	var got ai.Request
	called := 0
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		called++
		got = req
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: `"Fix the parser"`}},
		}), nil
	}
	generate := newSessionTitleGenerator(stream, func(_ context.Context, ref string) (ai.Model, error) {
		return catalog.Resolve(ref)
	}, nil)
	title, err := generate(context.Background(), "anthropic/claude-sonnet-4-5", "fix the parser")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if title != "Fix the parser" {
		t.Fatalf("title = %q, want the normalized model answer", title)
	}
	if called != 1 {
		t.Fatalf("stream called %d times, want once", called)
	}
	if got.Model.ID == "" || got.Model.Provider != "anthropic" {
		t.Fatalf("model = %+v, want the resolved session model", got.Model)
	}
	if got.MaxTokens != 25 {
		t.Errorf("MaxTokens = %d, want the 25-token title budget", got.MaxTokens)
	}
	if len(got.System) != 1 || got.System[0].Text != prompt.SessionTitleInstruction {
		t.Errorf("System = %+v, want the title instruction", got.System)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("Messages = %+v, want one user prompt", got.Messages)
	}
	user, ok := got.Messages[0].(ai.UserMessage)
	if !ok || len(user.Content) != 1 {
		t.Fatalf("message = %+v, want one user text block", got.Messages[0])
	}
	if text, ok := user.Content[0].(ai.TextContent); !ok || text.Text != "fix the parser" {
		t.Fatalf("content = %+v, want the first prompt verbatim", user.Content[0])
	}
}

// The generator must use the runtime resolver, not static-only catalog
// lookup: a live-discovered model id (the common case for OpenRouter stealth
// models) still needs a title request.
func TestSessionTitleGeneratorResolvesLiveDiscoveredModel(t *testing.T) {
	const ref = "openrouter/stealth/crouton-alpha"
	resolved := ai.Model{ID: "stealth/crouton-alpha", Provider: "openrouter"}
	var asked string
	resolver := func(_ context.Context, next string) (ai.Model, error) {
		asked = next
		return resolved, nil
	}
	var got ai.Request
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		got = req
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "Analyze the Go project"}},
		}), nil
	}
	generate := newSessionTitleGenerator(stream, resolver, nil)
	title, err := generate(context.Background(), ref, "check this project and analyze what this project is about?")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if asked != ref {
		t.Fatalf("resolver asked for %q, want the session model %q", asked, ref)
	}
	if got.Model.ID != resolved.ID || got.Model.Provider != resolved.Provider {
		t.Fatalf("request model = %+v, want the resolved live model", got.Model)
	}
	if title != "Analyze the Go project" {
		t.Fatalf("title = %q, want the generated summary", title)
	}
}

// Titling is cheap background work, so it must use the ASK role's model even
// when the session ran on a heavier execution role. The session snapshot is
// only the fallback when the role is unset; otherwise a BUILD session would
// spend its expensive model on every title.
func TestSessionTitleGeneratorUsesTheAskRoleModel(t *testing.T) {
	settings := config.Default()
	settings.AssignRole(string(config.RoleAsk), "openai/gpt-5-mini", "")

	resolved := ai.Model{ID: "gpt-5-mini", Provider: "openai"}
	var asked string
	resolver := func(_ context.Context, ref string) (ai.Model, error) {
		asked = ref
		return resolved, nil
	}
	var got ai.Request
	stream := func(_ context.Context, req ai.Request) (*ai.Stream, error) {
		got = req
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "Fix the session title"}},
		}), nil
	}
	generate := newSessionTitleGenerator(stream, resolver, func() string {
		return askRoleModelRef(settings)
	})

	// The session itself ran on a heavy execution model; the ASK assignment
	// must win over this snapshot.
	title, err := generate(context.Background(), "anthropic/claude-opus-4-5", "fix the session title")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if asked != "openai/gpt-5-mini" {
		t.Fatalf("resolved %q, want the ASK role model", asked)
	}
	if got.Model.ID != resolved.ID || got.Model.Provider != resolved.Provider {
		t.Fatalf("request model = %+v, want the ASK role model", got.Model)
	}
	if title != "Fix the session title" {
		t.Fatalf("title = %q, want the generated summary", title)
	}
}

// An empty first prompt is refused before spending a provider call.
func TestSessionTitleGeneratorRefusesEmptyPrompt(t *testing.T) {
	called := 0
	generate := newSessionTitleGenerator(func(context.Context, ai.Request) (*ai.Stream, error) {
		called++
		return ai.NewCompletedStream(ai.AssistantMessage{}), nil
	}, func(_ context.Context, ref string) (ai.Model, error) {
		return catalog.Resolve(ref)
	}, nil)
	if _, err := generate(context.Background(), "anthropic/claude-sonnet-4-5", "   "); err == nil {
		t.Fatal("an empty prompt produced a title request")
	}
	if called != 0 {
		t.Fatalf("stream called %d times, want none", called)
	}
}

// A provider failure or timeout must still produce the deterministic local
// label rather than leaving the session untitled. The ASK model is a shortcut,
// not a prerequisite for a usable title.
func TestSessionTitleGeneratorFallsBackToSanitizedPrompt(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "transport failure", err: errors.New("provider down")},
		{name: "timeout", err: context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generate := newSessionTitleGenerator(func(context.Context, ai.Request) (*ai.Stream, error) {
				return nil, tc.err
			}, func(_ context.Context, ref string) (ai.Model, error) {
				return catalog.Resolve(ref)
			}, nil)
			title, err := generate(context.Background(), "anthropic/claude-sonnet-4-5", "Use ls to inspect the project root")
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if title != "ls project root" {
				t.Fatalf("title = %q, want the sanitized local fallback", title)
			}
		})
	}
}

// Shutdown cancellation is not a provider failure: the UI is closing, so no
// fallback title may be persisted behind the program's back.
func TestSessionTitleGeneratorKeepsShutdownCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	generate := newSessionTitleGenerator(func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
		return nil, ctx.Err()
	}, func(_ context.Context, ref string) (ai.Model, error) {
		return catalog.Resolve(ref)
	}, nil)
	title, err := generate(ctx, "anthropic/claude-sonnet-4-5", "Use ls to inspect the project root")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if title != "" {
		t.Fatalf("title = %q, want no fallback on shutdown", title)
	}
}

// The model answer is reduced to one bounded navigation label.
func TestNormalizeSessionTitle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"quotes and punctuation", `  "Fix the parser bug."  `, "Fix the parser bug"},
		{"label prefix", "Title: Refactor the session modal", "Refactor the session modal"},
		{"first line only", "Fix parser\nThen run tests", "Fix parser"},
		{"collapses whitespace", "Fix   the\tparser", "Fix the parser"},
		{"caps at six words", "one two three four five six seven eight", "one two three four five six"},
		{"strips markdown heading", "### Fix the parser", "Fix the parser"},
		{"trailing quote after punctuation", `Fix the parser."`, "Fix the parser"},
		{"drops dangling preposition", "Fix the parser in", "Fix the parser"},
		{"only stop words", "the to in", ""},
		{"rune cut keeps whole words", strings.Repeat("a", 78) + " beta", strings.Repeat("a", 78)},
		{"only punctuation", " ... ", ""},
		{"empty", "   ", ""},
		{"long single token", strings.Repeat("a", sessionTitleMaxRunes+20), strings.Repeat("a", sessionTitleMaxRunes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSessionTitle(tc.raw); got != tc.want {
				t.Fatalf("normalizeSessionTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
