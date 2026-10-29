package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// stubPickerTool never runs: the scripted answers request no tools. It
// exists because a loop without tools is a misconfiguration, not a test.
type stubPickerTool struct{}

func (stubPickerTool) Name() string                   { return "stub" }
func (stubPickerTool) Description() string            { return "stub tool for picker tests" }
func (stubPickerTool) Parameters() *jsonschema.Schema { return nil }
func (stubPickerTool) Approval() agent.Approval {
	return agent.Approval{Tier: agent.TierExec, Reason: "never requested"}
}
func (stubPickerTool) Exclusive(map[string]any) bool { return false }
func (stubPickerTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	return agent.Result{Text: "unused"}
}

// TestPickerSelectionDrivesInference proves the picked label is not paint:
// switching to a newly discovered OpenRouter id moves the harness, the
// loop's next request, and the usage pricing together.
func TestPickerSelectionDrivesInference(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearProviderKeys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"future/picked-v2","name":"Picked V2",
			"context_length":200000,"pricing":{"prompt":"0","completion":"0"},
			"supported_parameters":["tools"]}]}`)
	}))
	defer server.Close()

	registry := ai.NewRegistry()
	registry.SetAPIKey("openrouter", "test-key")
	registry.SetBaseURL("openrouter", server.URL)

	var requested ai.Model
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		requested = req.Model
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "done"}},
		}), nil
	}
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	loop, err := agent.New(agent.Config{
		Model:  model,
		Stream: stream,
		Tools:  []agent.Tool{stubPickerTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	held := newHarness(registry, loop, nil, nil, &agent.UsageLog{}, nil, t.TempDir(), model)

	// The id the bundle never saw resolves through live discovery, exactly
	// as the picker offers it: provider-qualified, verbatim.
	switched, err := held.SwitchModel(context.Background(), "openrouter/future/picked-v2")
	if err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if switched.Provider != "openrouter" || switched.ID != "future/picked-v2" {
		t.Fatalf("switched = %s/%s, want the discovered id", switched.Provider, switched.ID)
	}
	if held.model.ID != "future/picked-v2" {
		t.Errorf("harness model = %s, want the switch to land", held.model.ID)
	}

	// The next inference request carries the picked model: selection is
	// configuration, not a label.
	if _, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "hi"}}); err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	if requested.Provider != "openrouter" || requested.ID != "future/picked-v2" {
		t.Errorf("requested = %s/%s, want the picked model on the wire", requested.Provider, requested.ID)
	}
}
