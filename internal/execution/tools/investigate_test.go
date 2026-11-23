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
	name   string
	source string
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
		Source: p.source,
		Facts:  []investigate.Fact{{Provider: p.name, Subject: "auth.go", Detail: "score 1.0"}},
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
	if !strings.Contains(result.Text, "investigation: provider_sufficient") ||
		!strings.Contains(result.Text, "control plane's evidence facets") {
		t.Fatalf("provider-local sufficiency was not disclosed: %s", result.Text)
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

// The investigation entrypoint is evidence-only. Its approval tier is the
// authority contract that stops a structural result from becoming a mutation.
func TestInvestigateToolIsReadOnly(t *testing.T) {
	engine := investigate.New(stubInvestigationProvider{name: "retrieval"})
	tool := NewInvestigateTool(t.TempDir(), engine, investigate.Budget{})
	if got := tool.Approval().Tier; got != agent.TierRead {
		t.Fatalf("approval tier = %v, want read", got)
	}
}

// A structural result must say which mechanism produced it. The step provider
// is structural; the source distinguishes native AST from delegated Gry
// evidence without making Gry a second role.
func TestInvestigateToolRendersFactProviderAttribution(t *testing.T) {
	engine := investigate.New(stubInvestigationProvider{name: "structural", source: "gry"})
	tool := NewInvestigateTool(t.TempDir(), engine, investigate.Budget{})
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"question": "what calls checkout?", "intent": "flow", "focus": "checkout",
	})
	if result.IsError {
		t.Fatalf("execute failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "[structural/gry]") {
		t.Fatalf("rendered facts lost the mechanism:\n%s", result.Text)
	}
	details := result.Details.(map[string]any)
	facts, _ := details["facts"].([]map[string]any)
	if len(facts) == 0 || facts[0]["source"] != "gry" {
		t.Fatalf("fact details lost the source: %+v", facts)
	}
}

// requestCapturingProvider records the request the engine actually received,
// so a tool-level test can observe argument wiring without re-implementing
// the router.
type requestCapturingProvider struct {
	name     string
	requests []investigate.Request
}

func (p *requestCapturingProvider) Name() planner.Provider { return planner.Provider(p.name) }

func (p *requestCapturingProvider) Available(context.Context) bool { return true }

func (p *requestCapturingProvider) Observe(_ context.Context, req investigate.Request, _ planner.Step, _ *investigate.Meter) (investigate.Observation, error) {
	p.requests = append(p.requests, req)
	return investigate.Observation{
		State: evidence.Observed, Confidence: 0.8, Coverage: 0.7, Summary: "observed",
		Facts: []investigate.Fact{{Provider: p.name, Subject: "auth.go", Detail: "score 1.0"}},
	}, nil
}

// The provider preference is a typed opt-in: the model passes the parameter,
// the tool never guesses it from the question text, and an unknown value is
// rejected before anything executes.
func TestInvestigateToolPassesProviderPreferenceToRequest(t *testing.T) {
	provider := &requestCapturingProvider{name: "retrieval"}
	engine := investigate.New(provider)
	tool := NewInvestigateTool(t.TempDir(), engine, investigate.Budget{})
	toolCtx := agent.Context{Ctx: context.Background()}

	result := tool.Execute(toolCtx, map[string]any{
		"question": "what is the structure of this repository?",
		"provider": "external",
	})
	if result.IsError {
		t.Fatalf("execute failed: %s", result.Text)
	}
	if len(provider.requests) == 0 || !provider.requests[0].PreferExternal {
		t.Fatalf("external preference was not forwarded: %+v", provider.requests)
	}

	provider.requests = nil
	result = tool.Execute(toolCtx, map[string]any{
		"question": "what is the structure of this repository?",
	})
	if result.IsError {
		t.Fatalf("execute failed: %s", result.Text)
	}
	if len(provider.requests) == 0 || provider.requests[0].PreferExternal {
		t.Fatalf("default provider must stay automatic: %+v", provider.requests)
	}

	result = tool.Execute(toolCtx, map[string]any{
		"question": "what is the structure of this repository?",
		"provider": "gry",
	})
	if !result.IsError {
		t.Fatal("unknown provider value was accepted")
	}
}
