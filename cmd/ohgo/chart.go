package main

import (
	"fmt"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/presentation/chart"
)

// sessionUsageSource is the one runtime-owned dataset the chart capability
// exposes today. The source is measured token telemetry, not a model proposal:
// the model may select it but cannot supply its values.
const sessionUsageSource = "session_usage"

// usageChartProvider adapts the session's existing UsageLog to the chart
// engine. It owns no telemetry of its own; a second counter would immediately
// disagree with /usage.
type usageChartProvider struct {
	usage *agent.UsageLog
}

func newUsageChartProvider(usage *agent.UsageLog) *usageChartProvider {
	return &usageChartProvider{usage: usage}
}

// RenderChart implements tools.ChartProvider.
func (p *usageChartProvider) RenderChart(request tools.ChartRequest) (tools.ChartOutcome, error) {
	if request.Source != sessionUsageSource {
		return tools.ChartOutcome{}, fmt.Errorf(
			"unknown chart source %q: available sources: %s", request.Source, sessionUsageSource)
	}
	if p.usage == nil {
		return tools.ChartOutcome{}, fmt.Errorf("chart source %q is unavailable: no usage ledger", sessionUsageSource)
	}
	turns, input, output, cacheRead, cacheWrite := p.usage.Totals()
	if turns == 0 {
		return tools.ChartOutcome{}, fmt.Errorf(
			"chart source %q has no measured usage yet; a chart is not fabricated from zero", sessionUsageSource)
	}
	data := chart.Dataset{
		Title:  "session token telemetry",
		XLabel: "token category",
		Note:   chart.TurnsNote(turns),
		Provenance: chart.Provenance{
			Kind:   chart.ProvenanceRuntime,
			ID:     sessionUsageSource,
			Source: "agent.UsageLog",
		},
		Series: []chart.Series{{
			Name:        "tokens",
			Unit:        chart.UnitTokens,
			Aggregation: chart.AggSum,
			Points: []chart.Point{
				{Label: "Input", Value: chart.Measured(float64(input))},
				{Label: "Output", Value: chart.Measured(float64(output))},
				{Label: "Cache read", Value: chart.Measured(float64(cacheRead))},
				{Label: "Cache write", Value: chart.Measured(float64(cacheWrite))},
			},
			Provenance: chart.Provenance{Kind: chart.ProvenanceRuntime, ID: sessionUsageSource},
		}},
	}
	inputPlan := chart.PlanInput{
		Data: data,
		Request: chart.Request{
			Source: request.Source,
			Kind:   chart.Kind(request.Kind),
			Series: request.Series,
		},
	}
	spec, plan, err := chart.Build(inputPlan, chart.DefaultWidth)
	if err != nil {
		return tools.ChartOutcome{}, err
	}
	return tools.ChartOutcome{
		Summary: chart.Summarize(spec, plan).String(),
		// The UI re-plans this input at its actual width. Returning a spec
		// rendered for the provider's default width would make a narrow
		// terminal display a layout it never had.
		Details: inputPlan,
	}, nil
}
