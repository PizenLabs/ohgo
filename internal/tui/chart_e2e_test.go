package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/ohgo/internal/presentation/chart"
)

// benchmarkDataset is the exact acceptance dataset. Values stay in
// milliseconds; 1.29s and 0.43ms encode the four-order magnitude spread.
func benchmarkDataset() chart.Dataset {
	labels := []string{"1,000", "5,000", "20,000", "50,000"}
	x := []float64{1000, 5000, 20000, 50000}
	points := func(values []float64) []chart.Point {
		out := make([]chart.Point, len(values))
		for i, v := range values {
			out[i] = chart.Point{Label: labels[i], X: x[i], HasX: true, Value: chart.Measured(v)}
		}
		return out
	}
	return chart.Dataset{
		Title:   "files vs load latency",
		XLabel:  "files",
		XUnit:   chart.UnitFiles,
		Ordered: true,
		Series: []chart.Series{
			{Name: "cold", Unit: chart.UnitMilliseconds, Points: points([]float64{43, 210, 976, 1290})},
			{Name: "warm_load", Unit: chart.UnitMilliseconds, Points: points([]float64{17, 80, 370, 548})},
			{Name: "refresh", Unit: chart.UnitMilliseconds, Points: points([]float64{12, 56, 243, 477})},
			{Name: "query_only_p50", Unit: chart.UnitMilliseconds, Aggregation: chart.AggP50, Points: points([]float64{0.43, 3, 23, 21})},
		},
	}
}

func writeDataset(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := json.MarshalIndent(benchmarkDataset(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertChartFrameWidths(t *testing.T, out string, width int) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if got := CellWidth(ansi.Strip(line)); got > width {
			t.Fatalf("frame line exceeds %d cells (%d): %q", width, got, ansi.Strip(line))
		}
	}
}

func TestChartFileCommandRendersExactBenchmarkDataset(t *testing.T) {
	dir := t.TempDir()
	writeDataset(t, dir, "latency.json")

	p := NewProgram(nil, "test", 0)
	p.state.Width, p.state.Height, p.state.Theme = 120, 70, ThemeDark
	p.completionRoot = dir
	command, ok := ParseCommand("/chart latency.json")
	if !ok {
		t.Fatal("chart command did not parse")
	}
	if cmd := p.runCommand(command); cmd != nil {
		t.Fatal("chart file command returned a runtime command")
	}
	if len(p.state.Entries) != 1 || p.state.Entries[0].Kind != EntryChart || p.state.Entries[0].Chart == nil {
		t.Fatalf("entries = %+v", p.state.Entries)
	}
	out := ansi.Strip(Render(p.state))
	for _, want := range []string{
		"files vs load latency",
		"1,000", "5,000", "20,000", "50,000",
		"cold", "warm_load", "refresh", "query_only_p50",
		"43 ms", "210 ms", "976 ms", "1.29 s",
		"17 ms", "80 ms", "370 ms", "548 ms",
		"12 ms", "56 ms", "243 ms", "477 ms",
		"0.43 ms", "3 ms", "23 ms", "21 ms",
		"cold · warm_load · refresh (ms)",
		"query_only_p50 (ms)",
		"source: file", "latency.json",
		"┤", "└", "⣀",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered chart missing %q:\n%s", want, out)
		}
	}
	assertChartFrameWidths(t, out, 120)
}

func TestChartFileDatasetReflowsWithoutLosingData(t *testing.T) {
	dir := t.TempDir()
	path := writeDataset(t, dir, "latency.json")
	input, err := loadChartDataset(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{120, 90, 60, 50} {
		state := State{Theme: ThemeDark, Width: width, Height: 70}
		state.Apply(ChartShown{Input: input, At: time.Now()})
		out := ansi.Strip(Render(state))
		for _, want := range []string{"files vs load latency", "1,000", "5,000", "20,000", "50,000", "1.29 s", "0.43 ms"} {
			if !strings.Contains(out, want) {
				t.Fatalf("width %d lost %q:\n%s", width, want, out)
			}
		}
		if strings.Contains(out, "20,00 ") || strings.Contains(out, "50,00 ") {
			t.Fatalf("width %d truncated a numeric row label:\n%s", width, out)
		}
		assertChartFrameWidths(t, out, width)
	}
	// The full TUI refuses a frame below its own chrome minimum. The chart
	// adapter still has to degrade deterministically there, so exercise it at
	// the exact renderer seam the TUI would call below the chrome floor.
	for _, width := range []int{39, 30, 20} {
		lines := renderChartInput(&input, PaletteFor(ThemeDark), width)
		out := ansi.Strip(strings.Join(lines, "\n"))
		for _, want := range []string{"1,000", "5,000", "20,000", "50,000", "1.29 s", "0.43 ms"} {
			if !strings.Contains(out, want) {
				t.Fatalf("chart width %d lost %q:\n%s", width, want, out)
			}
		}
		if strings.Contains(out, "20,00 ") || strings.Contains(out, "50,00 ") {
			t.Fatalf("chart width %d truncated a numeric row label:\n%s", width, out)
		}
		for _, line := range lines {
			if got := CellWidth(ansi.Strip(line)); got > width {
				t.Fatalf("chart width %d overflowed (%d): %q", width, got, ansi.Strip(line))
			}
		}
	}
}

func TestChartFileRejectsMalformedOrUnknownFields(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown.json": `{"series": [], "typo": 1}`,
		"wrong.json":   `{"series": "not-a-list"}`,
		"trailing.json": `{"series": []}
{}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		p := NewProgram(nil, "test", 0)
		p.state.Width, p.state.Height, p.state.Theme = 80, 30, ThemeDark
		p.chartCommand([]string{path})
		if len(p.state.Entries) != 1 || p.state.Entries[0].Kind != EntryNotice {
			t.Fatalf("%s accepted malformed data: %+v", name, p.state.Entries)
		}
		if !strings.Contains(p.state.Entries[0].Notice, "chart dataset unavailable") {
			t.Fatalf("%s notice = %q", name, p.state.Entries[0].Notice)
		}
	}
}
