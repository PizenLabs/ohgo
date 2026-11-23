package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// A streamed tool call with incomplete argument JSON must not execute, and a
// single bounded recovery must be able to replace it with a complete call.
func TestIncompleteToolArgumentsRecoverWithoutExecutingThePartialCall(t *testing.T) {
	tool := newScriptedTool("write", "wrote")
	var (
		mu       sync.Mutex
		requests []ai.Request
		attempt  int
	)
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		requests = append(requests, req)
		position := attempt
		attempt++
		mu.Unlock()
		switch position {
		case 0:
			return ai.NewFailedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{ID: "partial", Name: "write"}},
			}, &ai.ToolCallArgumentError{Tool: "write", Incomplete: true, OutputLimit: true}), nil
		case 1:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "call-1", Name: "write", Arguments: map[string]any{"value": "complete"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		default:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "finished"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
	}
	loop, err := New(Config{
		Model:                       catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:                      streamFn,
		Tools:                       []Tool{tool},
		MaxTurns:                    5,
		ToolArgumentRecoveryRequest: "recover with complete arguments",
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "write the page"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(tool.invoked()); got != 1 {
		t.Fatalf("tool invocations = %d, want only the complete call", got)
	}
	if got := tool.invoked()[0]["value"]; got != "complete" {
		t.Fatalf("executed arguments = %v, want the complete call", got)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want the recovered tool turn and the answer", len(turns))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(requests))
	}
	if !requestContainsRuntimeText(requests[1], "recover with complete arguments") {
		t.Fatal("the recovery request did not carry the argument directive")
	}
}

// Without a recovery directive the run stops in the recoverable protocol
// class, not as a failed task: the partial call never ran and any earlier
// mutation remains valid.
func TestIncompleteToolArgumentsStopAsRecoverableReview(t *testing.T) {
	tool := newScriptedTool("write", "wrote")
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return ai.NewFailedStream(ai.AssistantMessage{}, &ai.ToolCallArgumentError{
			Tool: "write", Incomplete: true,
		}), nil
	}
	loop, err := New(Config{
		Model:    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:   streamFn,
		Tools:    []Tool{tool},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "write"}})
	if !errors.Is(err, ErrToolCallIncomplete) {
		t.Fatalf("err = %v, want %v", err, ErrToolCallIncomplete)
	}
	if len(turns) != 0 {
		t.Fatalf("turns = %d, want none: the failed call is not a completed turn", len(turns))
	}
	if len(tool.invoked()) != 0 {
		t.Fatal("an incomplete call executed")
	}
	verdict, reason, _ := ClassifyRun(err, CompletionInput{}, false)
	if verdict != RunNeedsReview {
		t.Fatalf("verdict = %v, want RunNeedsReview", verdict)
	}
	if !strings.Contains(reason, "incomplete") {
		t.Fatalf("reason = %q, want the incomplete-arguments class", reason)
	}
}

// A provider that stops producing events must not hold the run forever, and a
// tool that completed before the stall must stay in the returned turns.
func TestStalledProviderStreamIsBoundedAndPreservesCompletedWork(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	provider := ai.NewOpenAIProvider("sk-test")
	provider.BaseURL = server.URL
	provider.Client = &http.Client{Timeout: 5 * time.Second}

	var (
		mu      sync.Mutex
		attempt int
	)
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		position := attempt
		attempt++
		mu.Unlock()
		if position == 0 {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "call-1", Name: "read", Arguments: map[string]any{"value": "a.go"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return provider.Stream(ctx, req)
	}
	loop, err := New(Config{
		Model:             catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:            streamFn,
		Tools:             []Tool{tool},
		MaxTurns:          5,
		StreamIdleTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	turns, err := loop.Run(ctx, []Message{UserMessage{Text: "read"}})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled run took %s; the idle bound did not fire", elapsed)
	}
	if !errors.Is(err, ErrProviderStalled) {
		t.Fatalf("err = %v, want %v", err, ErrProviderStalled)
	}
	if len(tool.invoked()) != 1 {
		t.Fatalf("tool invocations = %d, want the completed call preserved", len(tool.invoked()))
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want the completed tool turn", len(turns))
	}
	verdict, _, _ := ClassifyRun(err, CompletionInput{}, false)
	if verdict != RunNeedsReview {
		t.Fatalf("verdict = %v, want RunNeedsReview", verdict)
	}
}

// A provider failure after a completed mutation must not turn the task into a
// terminal failure: the successful turn is returned, and the durable checkpoint
// can resume rather than replay the change.
func TestProviderFailureAfterMutationKeepsTheTurnAndReviews(t *testing.T) {
	tool := newScriptedTool("write", "wrote")
	tool.result = Result{Text: "wrote", Mutates: true}
	var (
		mu      sync.Mutex
		attempt int
	)
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		position := attempt
		attempt++
		mu.Unlock()
		if position == 0 {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "call-1", Name: "write", Arguments: map[string]any{"value": "x"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return nil, errors.New("connection reset")
	}
	loop, err := New(Config{
		Model:    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:   streamFn,
		Tools:    []Tool{tool},
		MaxTurns: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "write"}})
	if !errors.Is(err, ErrProviderRequest) {
		t.Fatalf("err = %v, want %v", err, ErrProviderRequest)
	}
	if len(turns) != 1 || len(turns[0].Results) != 1 || !turns[0].Results[0].Result.Mutates {
		t.Fatalf("turns = %+v, want the completed mutation preserved", turns)
	}
	verdict, reason, _ := ClassifyRun(err, CompletionInput{Turns: turns}, false)
	if verdict != RunNeedsReview {
		t.Fatalf("verdict = %v, want RunNeedsReview", verdict)
	}
	if !strings.Contains(reason, "connection reset") {
		t.Fatalf("reason = %q, want the transport cause", reason)
	}
}

// A mid-run turn that contains only reasoning produces no action and no
// answer. It cannot advance the run, so one bounded directive must ask for a
// concrete output instead of treating the silent turn as a clean stop.
func TestReasoningOnlyTurnRecoversWithAConcreteOutput(t *testing.T) {
	tool := newScriptedTool("read", "package main")
	thinking := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ThinkingContent{Thinking: "planning the whole site in detail"}},
		StopReason: ai.StopEndTurn,
	}
	var (
		mu       sync.Mutex
		requests []ai.Request
		attempt  int
	)
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		requests = append(requests, req)
		position := attempt
		attempt++
		mu.Unlock()
		switch position {
		case 0:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"value": "a.go"}}},
				StopReason: ai.StopToolUse,
			}), nil
		case 1:
			return ai.NewCompletedStream(thinking), nil
		default:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "the site is ready"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
	}
	loop, err := New(Config{
		Model:                 catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:                streamFn,
		Tools:                 []Tool{tool},
		MaxTurns:              5,
		OutputRecoveryRequest: "emit the concrete next output",
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "build the site"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) == 0 || turns[len(turns)-1].Assistant.Text() != "the site is ready" {
		t.Fatalf("turns = %+v, want the answer produced after recovery", turns)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want the tool turn, the reasoning turn, and the recovered turn", len(requests))
	}
	if !requestContainsRuntimeText(requests[2], "emit the concrete next output") {
		t.Fatalf("the recovery request did not carry the directive: %+v", requests[2].Messages)
	}
}

// Without a recovery directive the same turn must still be a truthful,
// recoverable lifecycle stop rather than a silent end with no result.
func TestReasoningOnlyTurnWithoutDirectiveStopsRecoverably(t *testing.T) {
	tool := newScriptedTool("read", "package main")
	var attempt int
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		position := attempt
		attempt++
		if position == 0 {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"value": "a.go"}}},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ThinkingContent{Thinking: "still planning"}},
			StopReason: ai.StopEndTurn,
		}), nil
	}
	loop, err := New(Config{
		Model:    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:   streamFn,
		Tools:    []Tool{tool},
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loop.Run(context.Background(), []Message{UserMessage{Text: "build the site"}})
	if !errors.Is(err, ErrNoModelOutput) {
		t.Fatalf("err = %v, want %v", err, ErrNoModelOutput)
	}
}

// A turn cut off at the completion ceiling produced no action and no text.
// Retrying at the same budget truncates identically, so the one recovery must
// raise the ceiling to the model's own output ceiling and ask for the action.
func TestTruncatedTurnRaisesTheBudgetAndRecovers(t *testing.T) {
	tool := newScriptedTool("write", "wrote")
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	var (
		mu       sync.Mutex
		requests []ai.Request
		attempt  int
	)
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		requests = append(requests, req)
		position := attempt
		attempt++
		mu.Unlock()
		switch position {
		case 0:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.ThinkingContent{Thinking: "planning the whole site"}},
				StopReason: ai.StopMaxTokens,
			}), nil
		case 1:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "write", Arguments: map[string]any{"value": "styles.css"}}},
				StopReason: ai.StopToolUse,
			}), nil
		default:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "the site is ready"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
	}
	loop, err := New(Config{
		Model:                 model,
		Stream:                streamFn,
		Tools:                 []Tool{tool},
		MaxTurns:              5,
		MaxTokens:             1024,
		OutputRecoveryRequest: "emit the concrete next output",
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "build the site"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) == 0 || turns[len(turns)-1].Assistant.Text() != "the site is ready" {
		t.Fatalf("turns = %+v, want the answer produced after recovery", turns)
	}
	if got := len(tool.invoked()); got != 1 {
		t.Fatalf("tool invocations = %d, want the recovered call", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want the truncated turn, the recovery, and the answer", len(requests))
	}
	if requests[0].MaxTokens != 1024 {
		t.Fatalf("first request budget = %d, want the configured 1024", requests[0].MaxTokens)
	}
	if requests[1].MaxTokens != model.MaxOutputTokens {
		t.Fatalf("recovery budget = %d, want the model ceiling %d", requests[1].MaxTokens, model.MaxOutputTokens)
	}
	if !requestContainsRuntimeText(requests[1], "emit the concrete next output") {
		t.Fatal("the recovery request did not carry the directive")
	}
}

// Without a recovery directive a truncated turn is still a truthful budget
// stop, distinct from a model that produced nothing at all.
func TestTruncatedTurnWithoutDirectiveIsOutputTruncated(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ThinkingContent{Thinking: "still planning"}},
			StopReason: ai.StopMaxTokens,
		}), nil
	}
	loop, err := New(Config{
		Model:     model,
		Stream:    streamFn,
		Tools:     []Tool{newScriptedTool("read", "package main")},
		MaxTurns:  5,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "build the site"}})
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("err = %v, want %v", err, ErrOutputTruncated)
	}
	verdict, reason, _ := ClassifyRun(err, CompletionInput{Turns: turns}, false)
	if verdict != RunNeedsReview {
		t.Fatalf("verdict = %s, want review", verdict)
	}
	if !strings.Contains(reason, "OUTPUT_TRUNCATED") {
		t.Fatalf("reason = %q, want the truncation class", reason)
	}
}

// The recovery is bounded: a second truncation at the model ceiling is a
// truthful stop, not another identical request.
func TestTruncatedTurnStaysBoundedAfterRecovery(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	var (
		mu       sync.Mutex
		requests []ai.Request
	)
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ThinkingContent{Thinking: "still planning"}},
			StopReason: ai.StopMaxTokens,
		}), nil
	}
	loop, err := New(Config{
		Model:                 model,
		Stream:                streamFn,
		Tools:                 []Tool{newScriptedTool("read", "package main")},
		MaxTurns:              5,
		MaxTokens:             1024,
		OutputRecoveryRequest: "emit the concrete next output",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loop.Run(context.Background(), []Message{UserMessage{Text: "build the site"}})
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("err = %v, want %v", err, ErrOutputTruncated)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want the truncated turn and exactly one recovery", len(requests))
	}
	if requests[1].MaxTokens != model.MaxOutputTokens {
		t.Fatalf("recovery budget = %d, want the model ceiling %d", requests[1].MaxTokens, model.MaxOutputTokens)
	}
}

// A zero completion budget means "provider decides from the model ceiling".
// The loop must not substitute a flat default: a reasoning model can spend it
// on hidden reasoning and stop before any action.
func TestZeroMaxTokensIsNotReplacedByAFlatDefault(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	var (
		mu       sync.Mutex
		requests []ai.Request
	)
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
			StopReason: ai.StopEndTurn,
		}), nil
	}
	loop, err := New(Config{
		Model:    model,
		Stream:   streamFn,
		Tools:    []Tool{newScriptedTool("read", "package main")},
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if loop.cfg.MaxTokens != 0 {
		t.Fatalf("configured budget = %d, want 0 (model ceiling)", loop.cfg.MaxTokens)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "build the site"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[0].MaxTokens != 0 {
		t.Fatalf("request budget = %d, want 0 so the provider resolves the model ceiling", requests[0].MaxTokens)
	}
}

func requestContainsRuntimeText(req ai.Request, want string) bool {
	for _, msg := range req.Messages {
		state, ok := msg.(ai.RuntimeStateMessage)
		if ok && strings.Contains(state.Text, want) {
			return true
		}
	}
	return false
}
