package tools

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// ChartRequest is a model-facing request for a visualization. It names a
// registered source and an optional form; it never carries values or geometry.
// The host resolves the source, plans the presentation, and returns a compact
// acknowledgement.
type ChartRequest struct {
	// Source is a registered runtime/evidence dataset name.
	Source string
	// Kind is an optional requested form. Empty lets the host's planner
	// decide deterministically.
	Kind string
	// Series selects named series; empty selects the source's default.
	Series []string
}

// ChartOutcome is the provider's answer. Summary is the only text the model
// sees; Details is an opaque UI-only payload.
type ChartOutcome struct {
	Summary string
	Details any
}

// ChartProvider validates a chart request against runtime-owned data. It is
// implemented by the composition root, not by the execution plane: the tool
// must not import the presentation engine.
type ChartProvider interface {
	RenderChart(request ChartRequest) (ChartOutcome, error)
}

// RenderChartTool asks the host to render a chart from structured data.
//
// It is a presentation capability, not an authority: it cannot execute
// commands, mutate files, change completion, or read a dataset the host did
// not register. The model supplies only a source name and a form.
type RenderChartTool struct {
	Base
	provider ChartProvider
}

// NewRenderChartTool builds the chart request capability.
func NewRenderChartTool(provider ChartProvider) *RenderChartTool {
	tool := &RenderChartTool{provider: provider}
	tool.Base = NewBase("render_chart",
		"Render a deterministic chart from a runtime-owned data source. Supply only "+
			"a registered source, an optional form (table, bar, line, panels, change), "+
			"and optional series names. You cannot supply values, units, geometry, "+
			"colors, or conclusions; the host resolves the source and decides the form. "+
			"The exact table remains authoritative and the result is a short "+
			"acknowledgement, not the rendered chart.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"source"},
			"properties": map[string]any{
				"source": map[string]any{
					"type":        "string",
					"minLength":   1,
					"maxLength":   120,
					"description": "A dataset the host registered, e.g. session_usage. Unknown sources are rejected; data is never fabricated.",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"table", "bar", "line", "panels", "change"},
					"description": "Optional requested form. The planner may decline an untruthful form and return the exact table instead.",
				},
				"series": map[string]any{
					"type":     "array",
					"maxItems": 16,
					"items": map[string]any{
						"type":      "string",
						"minLength": 1,
						"maxLength": 120,
					},
					"description": "Optional series names to select from the source. Empty selects every series the source exposes.",
				},
			},
		},
		agent.Approval{Tier: agent.TierRead, Reason: "render a chart from runtime telemetry"},
	)
	tool.SetActionClasses(agent.ActionObserve)
	tool.SetGroup(ToolGroupWorkflowNetwork)
	return tool
}

// Execute implements agent.Tool.
func (t *RenderChartTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	if t.provider == nil {
		return agent.ErrorResult("chart rendering is unavailable in this session")
	}
	source, err := strArg(args, "source", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return agent.ErrorResult("missing required argument %q", "source")
	}
	kind, err := strArg(args, "kind", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	kind = strings.TrimSpace(kind)
	switch kind {
	case "", "table", "bar", "line", "panels", "change":
	default:
		return agent.ErrorResult("unknown chart kind %q: use table, bar, line, panels, or change", kind)
	}
	series, err := stringListArg(args, "series")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	outcome, err := t.provider.RenderChart(ChartRequest{
		Source: source,
		Kind:   kind,
		Series: series,
	})
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if strings.TrimSpace(outcome.Summary) == "" {
		return agent.ErrorResult("chart provider returned no summary")
	}
	return agent.Result{Text: outcome.Summary, Details: outcome.Details}
}

// stringListArg decodes a bounded string array. JSON arrays decode as []any,
// so this is the one boundary where the conversion is required.
func stringListArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("argument %q must be an array of strings", key)
	}
	if len(list) > 16 {
		return nil, fmt.Errorf("argument %q has %d entries, maximum 16", key, len(list))
	}
	out := make([]string, 0, len(list))
	for index, item := range list {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("argument %q entry %d must be a string", key, index)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("argument %q entry %d is empty", key, index)
		}
		out = append(out, value)
	}
	return out, nil
}
