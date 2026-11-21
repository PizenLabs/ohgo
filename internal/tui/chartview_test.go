package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/presentation/chart"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func chartFixture() chart.PlanInput {
	return chart.PlanInput{Data: chart.Dataset{
		Title:  "token telemetry",
		XLabel: "metric",
		Series: []chart.Series{{
			Name: "tokens", Unit: chart.UnitTokens,
			Points: []chart.Point{
				{Label: "Input", Value: chart.Measured(100)},
				{Label: "Output", Value: chart.Measured(50)},
			},
		}},
		Provenance: chart.Provenance{Kind: chart.ProvenanceRuntime, ID: "session", Source: "tui"},
	}}
}

func TestSessionChartInputUsesTheMeasuredLedger(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	log := &agent.UsageLog{}
	log.Add([]agent.Turn{{Assistant: ai.AssistantMessage{Usage: ai.Usage{
		Input: 200, Output: 80, Total: 280,
	}}}})
	p.SetUsageLog(log)
	start := time.Now().Add(-25 * time.Millisecond)
	p.state.Entries = []Entry{{Kind: EntryTool, Tool: &ToolEntry{
		Name: "read", StartedAt: start, EndedAt: time.Now(),
	}}}

	input, ok := p.sessionChartInput()
	if !ok {
		t.Fatal("session telemetry produced no chart")
	}
	if len(input.Data.Series) != 2 {
		t.Fatalf("series = %d, want token usage and work", len(input.Data.Series))
	}
	tokens := input.Data.Series[0]
	if tokens.Points[0].Value.Number != 200 || tokens.Points[1].Value.Number != 80 {
		t.Fatalf("token points = %+v", tokens.Points)
	}
	work := input.Data.Series[1]
	if len(work.Points) != 2 || work.Points[0].Value.Number != 1 || work.Points[1].Value.Number != 1 {
		t.Fatalf("work points = %+v", work.Points)
	}
}

func TestChartCommandAppendsAndRendersOwnedBlock(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Width = 80
	p.state.Height = 30
	p.state.Theme = ThemeDark
	log := &agent.UsageLog{}
	log.Add([]agent.Turn{{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 200, Output: 80, Total: 280}}}})
	p.SetUsageLog(log)

	if cmd := p.runCommand(Command{Name: "chart"}); cmd != nil {
		t.Fatal("chart command returned a runtime command")
	}

	if len(p.state.Entries) != 1 || p.state.Entries[0].Kind != EntryChart || p.state.Entries[0].Chart == nil {
		t.Fatalf("entries = %+v", p.state.Entries)
	}
	out := Render(p.state)
	for _, want := range []string{"session telemetry", "tokens", "Input", "Output", "20"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestChartToolOutcomeExpandsAndRenders(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Width = 100
	p.state.Height = 30
	p.state.Theme = ThemeDark
	input := chartFixture()
	p.state.Apply(ToolStarted{ID: "call-1", Name: "render_chart", Summary: "session_usage"})
	p.state.Apply(ToolFinished{
		ID:      "call-1",
		Text:    "chart rendered: kind=bar series=1 points=2 panels=1 table=yes",
		Summary: "chart rendered · 2 points",
		Chart:   &input,
		At:      time.Now(),
	})
	if len(p.state.Entries) != 1 || p.state.Entries[0].Tool == nil {
		t.Fatalf("entries = %+v", p.state.Entries)
	}
	if p.state.Entries[0].Tool.Chart == nil || !p.state.Entries[0].Tool.Expanded {
		t.Fatalf("tool chart/expanded = %v/%v", p.state.Entries[0].Tool.Chart, p.state.Entries[0].Tool.Expanded)
	}
	out := Render(p.state)
	if !strings.Contains(out, "token telemetry") || !strings.Contains(out, "100") {
		t.Fatalf("chart body not rendered:\n%s", out)
	}
}

func TestChartCopyUsesExactTableNotRenderedArt(t *testing.T) {
	input := chartFixture()
	entry := Entry{Kind: EntryChart, Chart: &input}
	text := BlockCopyText(entry)
	if strings.Contains(text, "\x1b") {
		t.Fatalf("copy carried ANSI: %q", text)
	}
	for _, want := range []string{"tokens", "Input", "100", "Output", "50"} {
		if !strings.Contains(text, want) {
			t.Fatalf("copy missing %q:\n%s", want, text)
		}
	}
}

func TestChartInputIgnoresForeignToolDetails(t *testing.T) {
	if got := chartInput(map[string]any{"path": "x"}); got != nil {
		t.Fatalf("foreign details produced a chart: %+v", got)
	}
}

func TestChartEntryReflowsAtTheCurrentWidth(t *testing.T) {
	input := chartFixture()
	state := State{
		Entries: []Entry{{Kind: EntryChart, Chart: &input}},
		Theme:   ThemeDark,
	}
	state.Width, state.Height = 100, 30
	wide := Render(state)
	state.Width, state.Height = 50, 24
	narrow := Render(state)
	if !strings.Contains(wide, "token telemetry") || !strings.Contains(narrow, "token telemetry") {
		t.Fatalf("chart disappeared across resize\nwide:\n%s\nnarrow:\n%s", wide, narrow)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if CellWidth(line) > state.Width {
			t.Fatalf("narrow frame overflowed: %d > %d: %q", CellWidth(line), state.Width, line)
		}
	}
}

func TestChartEntryRendersInLiveMode(t *testing.T) {
	input := chartFixture()
	state := State{Theme: ThemeDark, Width: 80, Height: 24}
	entry := Entry{Kind: EntryChart, Chart: &input}
	lines := liveEntryLinesRaw(state, 0, entry, PaletteFor(state.Theme), ComputeLayout(state))
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "token telemetry") {
		t.Fatalf("live chart lines = %v", lines)
	}
}
