package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/ohgo/internal/presentation/chart"
)

// maxChartDatasetBytes bounds a user-supplied dataset file. The file is read
// on the command path, not the turn path, so malformed or huge input becomes a
// notice rather than a stalled renderer.
const maxChartDatasetBytes = 1 << 20

// chartInput extracts the structured chart payload from a tool result.
//
// Details is an opaque tool-owned boundary, so this is one of the few places
// the TUI inspects a concrete type. A provider that returns a different type
// simply produces no chart; the compact result text remains the model-facing
// outcome.
func chartInput(details any) *chart.PlanInput {
	switch value := details.(type) {
	case chart.PlanInput:
		copied := value
		return &copied
	case *chart.PlanInput:
		return value
	default:
		return nil
	}
}

// renderChartInput plans and renders one chart at the actual width. A chart
// failure is a presentation notice, never a failed turn: the structured data
// and the exact table remain available through /chart and /copy.
func renderChartInput(input *chart.PlanInput, pal *Palette, width int) []string {
	if input == nil {
		return nil
	}
	if width <= 0 {
		width = chart.DefaultWidth
	}
	spec, _, err := chart.Build(*input, width)
	if err != nil {
		return chartErrorLines(err, pal, width)
	}
	lines, err := chart.Render(spec)
	if err != nil {
		return chartErrorLines(err, pal, width)
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, renderChartLine(line, pal))
	}
	return out
}

// chartErrorLines keeps a presentation failure inside the width budget and
// inside the block that owns it. It never becomes a turn failure.
func chartErrorLines(err error, pal *Palette, width int) []string {
	lines := WrapToWidth(sanitizeInline("chart unavailable: "+err.Error()), width)
	for index := range lines {
		lines[index] = pal.Warning.Render(lines[index])
	}
	return lines
}

func renderChartLine(line chart.Line, pal *Palette) string {
	if len(line.Spans) == 0 {
		return ""
	}
	var b strings.Builder
	for _, span := range line.Spans {
		if span.Text == "" {
			continue
		}
		b.WriteString(chartSpanStyle(pal, span.Role).Render(span.Text))
	}
	return b.String()
}

// chartSpanStyle maps semantic chart roles onto the existing palette. The
// chart package never sees a colour; this is the single product-language
// translation point.
func chartSpanStyle(pal *Palette, role chart.Role) lipgloss.Style {
	switch role {
	case chart.RoleTitle:
		return pal.Title
	case chart.RolePanelTitle:
		return pal.Cyan.Bold(true)
	case chart.RoleHeader:
		return pal.Heading
	case chart.RoleRule:
		return pal.Separator
	case chart.RoleMuted:
		return pal.Muted
	case chart.RoleFaint:
		return pal.Faint
	case chart.RoleEmphasis:
		return pal.Strong
	case chart.RoleWarning:
		return pal.Warning
	case chart.RoleError:
		return pal.ToolError
	case chart.RoleSuccess:
		return pal.ToolDone
	case chart.RoleAxis:
		return pal.Faint
	case chart.RoleSeries1:
		return pal.Cyan
	case chart.RoleSeries2:
		return pal.Strong
	case chart.RoleSeries3:
		return pal.ToolDone
	case chart.RoleSeries4:
		return pal.Thought
	case chart.RoleSeries5:
		return pal.Heading
	case chart.RoleSeries6:
		return pal.Secondary
	default:
		return pal.Assistant
	}
}

// sessionChartInput builds the zero-token runtime path: measured session
// telemetry becomes a Dataset, the planner decides the form, and no model call
// or extra token is involved.
func (p *Program) sessionChartInput() (chart.PlanInput, bool) {
	if p == nil {
		return chart.PlanInput{}, false
	}
	var series []chart.Series
	notes := make([]string, 0, 3)
	turns := 0
	if p.usage != nil {
		var input, output, cacheRead, cacheWrite int
		turns, input, output, cacheRead, cacheWrite = p.usage.Totals()
		if turns > 0 {
			series = append(series, chart.Series{
				Name:        "tokens",
				Unit:        chart.UnitTokens,
				Aggregation: chart.AggSum,
				Points: []chart.Point{
					{Label: "Input", Value: chart.Measured(float64(input))},
					{Label: "Output", Value: chart.Measured(float64(output))},
					{Label: "Cache read", Value: chart.Measured(float64(cacheRead))},
					{Label: "Cache write", Value: chart.Measured(float64(cacheWrite))},
				},
			})
			notes = append(notes, chart.TurnsNote(turns))
		}
	}
	calls := 0
	var toolTime time.Duration
	for _, entry := range p.state.Entries {
		if entry.Kind != EntryTool || entry.Tool == nil {
			continue
		}
		calls++
		toolTime += entry.Tool.Duration()
	}
	if calls > 0 {
		points := []chart.Point{{Label: "Tool calls", Value: chart.Measured(float64(calls))}}
		if turns > 0 {
			points = append(points, chart.Point{Label: "Turns", Value: chart.Measured(float64(turns))})
		}
		series = append(series, chart.Series{Name: "work", Unit: chart.UnitCount, Points: points})
		notes = append(notes, fmt.Sprintf("%d tool calls", calls))
	}
	if toolTime > 0 {
		notes = append(notes, "measured tool time "+formatDurationPrecise(toolTime))
	}
	if len(series) == 0 {
		return chart.PlanInput{}, false
	}
	session := p.state.Status.Session
	if session == "" {
		session = "live session"
	}
	data := chart.Dataset{
		Title:  "session telemetry",
		XLabel: "metric",
		Series: series,
		Note:   strings.Join(notes, " · "),
		Provenance: chart.Provenance{
			Kind:   chart.ProvenanceRuntime,
			ID:     session,
			Source: "tui session",
		},
	}
	return chart.PlanInput{
		Data:    data,
		Request: chart.Request{Source: "tui_session"},
	}, true
}

// showSessionChart appends the runtime-owned telemetry chart. It costs zero
// model tokens and never becomes a turn: it is a projection of measured state.
func (p *Program) showSessionChart() {
	input, ok := p.sessionChartInput()
	if !ok {
		p.state.Apply(Notice{Text: "no runtime metrics yet: run a turn first"})
		return
	}
	p.state.Apply(ChartShown{Input: input, At: time.Now()})
}

// chartCommand dispatches /chart. With no argument it projects runtime
// telemetry. With one argument it presents a user-supplied structured dataset:
// the user chose the file, so this is not a model-authored value path.
func (p *Program) chartCommand(args []string) {
	switch len(args) {
	case 0:
		p.showSessionChart()
	case 1:
		p.showChartFile(args[0])
	default:
		p.state.Apply(Notice{Text: "usage: /chart [dataset.json]"})
	}
}

// showChartFile loads one exact Dataset from a user-selected JSON file and
// appends it as a normal chart block. The renderer still validates and plans;
// this function only supplies data, never geometry or conclusions.
func (p *Program) showChartFile(path string) {
	input, err := loadChartDataset(path, p.completionRoot)
	if err != nil {
		p.state.Apply(Notice{Text: "chart dataset unavailable: " + err.Error()})
		return
	}
	p.state.Apply(ChartShown{Input: input, At: time.Now()})
}

// loadChartDataset reads a bounded, strict chart.Dataset. Strict decoding
// rejects a misspelled field instead of silently charting different data.
func loadChartDataset(path, root string) (chart.PlanInput, error) {
	if !filepath.IsAbs(path) && root != "" {
		path = filepath.Join(root, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return chart.PlanInput{}, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxChartDatasetBytes+1))
	if err != nil {
		return chart.PlanInput{}, err
	}
	if len(raw) > maxChartDatasetBytes {
		return chart.PlanInput{}, fmt.Errorf("dataset exceeds %d bytes", maxChartDatasetBytes)
	}
	var data chart.Dataset
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return chart.PlanInput{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return chart.PlanInput{}, fmt.Errorf("dataset has trailing content")
	}
	if data.Provenance.Empty() {
		data.Provenance = chart.Provenance{
			Kind:   chart.ProvenanceFile,
			ID:     filepath.Base(path),
			Source: path,
		}
	}
	if err := chart.ValidateDataset(data); err != nil {
		return chart.PlanInput{}, err
	}
	return chart.PlanInput{
		Data:    data,
		Request: chart.Request{Source: "chart_file"},
	}, nil
}
