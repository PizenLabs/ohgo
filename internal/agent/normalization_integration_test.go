package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// normalizationTool is the execution capability the regression test offers.
// Its schema is deliberately realistic: path is a string, so a serialized
// <parameter> value must stay a string rather than being guessed into a
// number.
type normalizationTool struct {
	mu    sync.Mutex
	calls []map[string]any
}

func (t *normalizationTool) Name() string        { return "ls" }
func (t *normalizationTool) Description() string { return "list a directory" }
func (t *normalizationTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"path":{"type":"string"}},
		"required":["path"]
	}`))
}
func (t *normalizationTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (t *normalizationTool) Exclusive(map[string]any) bool { return false }
func (t *normalizationTool) Execute(_ Context, args map[string]any) Result {
	t.mu.Lock()
	t.calls = append(t.calls, args)
	t.mu.Unlock()
	return Result{Text: fmt.Sprintf("listed %v", args["path"])}
}
func (t *normalizationTool) invoked() []map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]map[string]any(nil), t.calls...)
}

func TestPseudoToolCallsEnterTheCanonicalExecutionPath(t *testing.T) {
	pseudo1 := `<dotsfunctioncall>
  <invoke name="ls">
    <parameter name="path">backend/cmd</parameter>
  </invoke>
</dotsfunctioncall>`
	pseudo2 := `<dotsfunctioncall>
  <invoke name="ls">
    <parameter name="path">backend/internal</parameter>
  </invoke>
</dotsfunctioncall>`

	first := sseTextChunk("I'll inspect. ") +
		sseToolCallChunk(0, "call_native", "ls", `{"path":"backend"}`) +
		sseTextChunk(pseudo1) +
		sseTextChunk("\nRecap: backend binaries live under backend/cmd.\n") +
		sseTextChunk(pseudo2) +
		sseFinishChunk("stop") +
		"data: [DONE]\n\n"
	second := sseTextChunk("Done.") + sseFinishChunk("stop") + "data: [DONE]\n\n"

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			_, _ = w.Write([]byte(first))
			return
		}
		_, _ = w.Write([]byte(second))
	}))
	defer server.Close()

	registry := ai.NewRegistry()
	registry.SetAPIKey("test", "test-key")
	registry.SetBaseURL("test", server.URL)

	model := ai.Model{
		ID: "dots-regression", Provider: "test", API: ai.APIOpenAIChatCompletions,
		BaseURL: server.URL, ContextWindow: 100_000, MaxOutputTokens: 4096,
	}
	tool := &normalizationTool{}
	authorizer := &scriptedAuthorizer{}

	var liveText strings.Builder
	loop, err := New(Config{
		Model:     model,
		Stream:    registry.StreamFn(),
		Tools:     []Tool{tool},
		Authorize: authorizer,
		OnTextDelta: func(delta string) {
			liveText.WriteString(delta)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "check this project"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}

	// The native call and both pseudo calls are the same kind of outcome.
	firstTurn := turns[0]
	if got := len(firstTurn.Results); got != 3 {
		t.Fatalf("results = %d, want native + two pseudo calls", got)
	}
	wantPaths := []string{"backend", "backend/cmd", "backend/internal"}
	for i, want := range wantPaths {
		outcome := firstTurn.Results[i]
		if outcome.ToolName != "ls" {
			t.Errorf("result %d tool = %q, want ls", i, outcome.ToolName)
		}
		if outcome.Args["path"] != want {
			t.Errorf("result %d path = %#v, want %q", i, outcome.Args["path"], want)
		}
		if outcome.Result.IsError {
			t.Errorf("result %d failed: %s", i, outcome.Result.Text)
		}
		// Every call — including the pseudo ones — required a security
		// decision before execution.
		if outcome.Authorization == nil {
			t.Errorf("result %d has no authorization decision", i)
		}
	}
	if firstTurn.Results[0].CallID != "call_native" {
		t.Errorf("native call id = %q, want call_native", firstTurn.Results[0].CallID)
	}
	for i := 1; i < 3; i++ {
		if !strings.HasPrefix(firstTurn.Results[i].CallID, "pseudo_") {
			t.Errorf("pseudo call %d id = %q, want a pseudo id", i, firstTurn.Results[i].CallID)
		}
	}

	if evaluated, _ := authorizer.counts(); evaluated != 3 {
		t.Errorf("authorizer evaluated %d calls, want all 3 to cross the boundary", evaluated)
	}
	if got := len(tool.invoked()); got != 3 {
		t.Errorf("tool executed %d times, want 3", got)
	}
	if firstTurn.Assistant.StopReason != ai.StopToolUse {
		t.Errorf("stop reason = %q, want %q after pseudo-call recognition",
			firstTurn.Assistant.StopReason, ai.StopToolUse)
	}

	// The renderer never sees the protocol. The recap does.
	for _, text := range []string{liveText.String(), firstTurn.Assistant.Text(), turns[1].Assistant.Text()} {
		for _, marker := range []string{
			"<dotsfunctioncall>", "</dotsfunctioncall>", "<invoke", "<parameter",
		} {
			if strings.Contains(text, marker) {
				t.Errorf("visible text leaked %q: %q", marker, text)
			}
		}
	}
	if !strings.Contains(liveText.String(), "Recap: backend binaries live under backend/cmd.") {
		t.Errorf("live text lost the assistant recap: %q", liveText.String())
	}
	if got := turns[1].Assistant.Text(); got != "Done." {
		t.Errorf("final answer = %q, want Done.", got)
	}

	// A streaming renderer reveals these deltas, then skips the completed
	// turns by common prefix. The live sequence must therefore equal the
	// completed turns' visible text exactly, or the TUI republishes the recap
	// and the final answer as a second tail.
	var completed strings.Builder
	for _, turn := range turns {
		completed.WriteString(turn.Assistant.Text())
	}
	if liveText.String() != completed.String() {
		t.Errorf("live text diverged from the completed turns:\n live = %q\n done = %q",
			liveText.String(), completed.String())
	}
}

func sseTextChunk(text string) string {
	return sseChunk(map[string]any{
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"content": text},
		}},
	})
}

func sseToolCallChunk(index int, id, name, arguments string) string {
	return sseChunk(map[string]any{
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index": index,
					"id":    id,
					"type":  "function",
					"function": map[string]any{
						"name":      name,
						"arguments": arguments,
					},
				}},
			},
		}},
	})
}

func sseFinishChunk(reason string) string {
	return sseChunk(map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": reason,
		}},
	})
}

func sseChunk(payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return "data: " + string(encoded) + "\n\n"
}
