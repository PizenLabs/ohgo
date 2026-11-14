package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

type stubInvestigationProvider struct {
	name string
}

func (p stubInvestigationProvider) Name() planner.Provider { return planner.Provider(p.name) }

func (p stubInvestigationProvider) Available(context.Context) bool { return true }

func (p stubInvestigationProvider) Observe(context.Context, investigate.Request, planner.Step, *investigate.Meter) (investigate.Observation, error) {
	return investigate.Observation{
		State: evidence.Observed, Confidence: 0.8, Coverage: 0.7,
		Summary: "observed",
		Records: []evidence.Record{{
			Provider: evidence.ProviderRetrieval, Source: evidence.SourceBM25,
			Subject: "file:auth.go", Relation: "ranked_for", Object: "authentication",
			State: evidence.Observed, Confidence: 0.8,
		}},
		Facts: []investigate.Fact{{Provider: p.name, Subject: "auth.go", Detail: "score 1.0"}},
	}, nil
}

// TestInvestigateToolRunsOrchestratorAndBoundsOutput proves the tool is an
// orchestration entrypoint: the engine executes providers, and the rendered
// result names the evidence and the stopping reason instead of dumping raw
// provider payloads.
func TestInvestigateToolRunsOrchestratorAndBoundsOutput(t *testing.T) {
	engine := investigate.New(stubInvestigationProvider{name: "retrieval"})
	tool := NewInvestigateTool(t.TempDir(), engine, investigate.Budget{})
	toolCtx := agent.Context{Ctx: context.Background()}
	result := tool.Execute(toolCtx, map[string]any{
		"question": "where is authentication implemented?",
	})
	if result.IsError {
		t.Fatalf("execute failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "investigation:") || !strings.Contains(result.Text, "auth.go") {
		t.Fatalf("text does not describe the investigation: %s", result.Text)
	}
	if len(result.Text) > InvestigateMaxOutput {
		t.Fatalf("output is not bounded: %d bytes", len(result.Text))
	}
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T", result.Details)
	}
	if details["status"] != string(investigate.StatusSufficient) {
		t.Fatalf("status = %v, want sufficient", details["status"])
	}
	if _, ok := details["stop_reason"]; !ok {
		t.Fatal("stop_reason is missing")
	}
}

// TestInvestigateToolRejectsEscapingFocus proves a model-supplied selector
// cannot reach outside the workspace.
func TestInvestigateToolRejectsEscapingFocus(t *testing.T) {
	engine := investigate.New(stubInvestigationProvider{name: "retrieval"})
	tool := NewInvestigateTool(t.TempDir(), engine, investigate.Budget{})
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"question": "where is auth?", "focus": "../outside",
	})
	if !result.IsError {
		t.Fatal("expected rejection")
	}
}
