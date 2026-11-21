package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/presentation/chart"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tui"
)

func measuredUsageLog() *agent.UsageLog {
	log := &agent.UsageLog{}
	log.Add([]agent.Turn{{Assistant: ai.AssistantMessage{Usage: ai.Usage{
		Input: 2000, Output: 1000, CacheRead: 100, CacheWrite: 50, Total: 3150,
	}}}})
	return log
}

func TestUsageChartProviderRejectsUnknownSource(t *testing.T) {
	provider := newUsageChartProvider(measuredUsageLog())
	if _, err := provider.RenderChart(tools.ChartRequest{Source: "made_up"}); err == nil {
		t.Fatal("unknown source produced a chart")
	}
}

func TestUsageChartProviderDoesNotFabricateWithoutMeasurement(t *testing.T) {
	provider := newUsageChartProvider(&agent.UsageLog{})
	if _, err := provider.RenderChart(tools.ChartRequest{Source: sessionUsageSource}); err == nil {
		t.Fatal("empty ledger produced a chart")
	}
}

func TestUsageChartProviderReturnsCompactSummaryAndReplannableDetails(t *testing.T) {
	provider := newUsageChartProvider(measuredUsageLog())
	outcome, err := provider.RenderChart(tools.ChartRequest{
		Source: sessionUsageSource, Kind: "bar",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind=bar", "series=1", "points=4", "table=yes"} {
		if !strings.Contains(outcome.Summary, want) {
			t.Fatalf("summary = %q, missing %q", outcome.Summary, want)
		}
	}
	if strings.Contains(outcome.Summary, "█") || strings.Contains(outcome.Summary, "\n") {
		t.Fatalf("summary carried rendered chart geometry: %q", outcome.Summary)
	}
	input, ok := outcome.Details.(chart.PlanInput)
	if !ok {
		t.Fatalf("details = %T, want chart.PlanInput", outcome.Details)
	}
	spec, _, err := chart.Build(input, 60)
	if err != nil {
		t.Fatal(err)
	}
	text, err := chart.RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2,000", "1,000", "100", "50", "session token telemetry"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered chart missing %q:\n%s", want, text)
		}
	}
}

func TestBuildToolsAdvertisesChartOnlyWithALedger(t *testing.T) {
	root := t.TempDir()
	without := buildTools(testOptions(t, root, 1), ai.NewRegistry())
	if hasTool(without, "render_chart") {
		t.Fatal("chart capability advertised without a usage ledger")
	}
	opts := testOptions(t, root, 1)
	opts.usage = measuredUsageLog()
	with := buildTools(opts, ai.NewRegistry())
	if !hasTool(with, "render_chart") {
		t.Fatal("chart capability is missing with a usage ledger")
	}
}

func TestAgentChartToolReachesTheTUIWithoutRenderedGeometry(t *testing.T) {
	tool := tools.NewRenderChartTool(newUsageChartProvider(measuredUsageLog()))
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"source": sessionUsageSource,
		"kind":   "bar",
	})
	if result.IsError {
		t.Fatalf("Execute: %s", result.Text)
	}
	if strings.ContainsAny(result.Text, "█⣀·┤└") || strings.Contains(result.Text, "\n") {
		t.Fatalf("model-facing result carried geometry: %q", result.Text)
	}
	input, ok := result.Details.(chart.PlanInput)
	if !ok {
		t.Fatalf("details = %T, want chart.PlanInput", result.Details)
	}
	state := tui.New()
	state.Width, state.Height, state.Theme = 100, 40, tui.ThemeDark
	state.Apply(tui.ChartShown{Input: input, At: time.Now()})
	out := tui.Render(state)
	for _, want := range []string{"session token telemetry", "Input", "2,000", "█"} {
		if !strings.Contains(out, want) {
			t.Fatalf("TUI chart missing %q:\n%s", want, out)
		}
	}
}
