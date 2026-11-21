package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

type fakeChartProvider struct {
	request ChartRequest
	outcome ChartOutcome
	err     error
	calls   int
}

func (f *fakeChartProvider) RenderChart(request ChartRequest) (ChartOutcome, error) {
	f.calls++
	f.request = request
	return f.outcome, f.err
}

func TestRenderChartToolPassesOnlySourceAndForm(t *testing.T) {
	provider := &fakeChartProvider{outcome: ChartOutcome{
		Summary: "chart rendered: kind=bar series=1 points=4 panels=1 table=yes",
		Details: "ui-only",
	}}
	tool := NewRenderChartTool(provider)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"source": "session_usage",
		"kind":   "bar",
		"series": []any{"tokens"},
	})
	if result.IsError {
		t.Fatalf("Execute error: %s", result.Text)
	}
	if provider.request.Source != "session_usage" || provider.request.Kind != "bar" {
		t.Fatalf("request = %+v", provider.request)
	}
	if len(provider.request.Series) != 1 || provider.request.Series[0] != "tokens" {
		t.Fatalf("series = %v", provider.request.Series)
	}
	if result.Details != "ui-only" {
		t.Fatalf("details = %v", result.Details)
	}
	if !strings.Contains(result.Text, "kind=bar") {
		t.Fatalf("summary = %q", result.Text)
	}
}

func TestRenderChartToolRejectsUnknownSourceAndKindWithoutCallingHost(t *testing.T) {
	provider := &fakeChartProvider{}
	tool := NewRenderChartTool(provider)
	if result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{}); !result.IsError {
		t.Fatal("missing source succeeded")
	}
	if result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"source": "session_usage",
		"kind":   "histogram",
	}); !result.IsError {
		t.Fatal("unknown kind succeeded")
	}
	if provider.calls != 0 {
		t.Fatalf("provider called %d times for rejected requests", provider.calls)
	}
}

func TestRenderChartToolSurfacesProviderFailure(t *testing.T) {
	provider := &fakeChartProvider{err: errors.New("no measured usage")}
	tool := NewRenderChartTool(provider)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"source": "session_usage"})
	if !result.IsError || !strings.Contains(result.Text, "no measured usage") {
		t.Fatalf("result = %+v", result)
	}
	if result.Details != nil {
		t.Fatalf("failed result carried details: %v", result.Details)
	}
}
