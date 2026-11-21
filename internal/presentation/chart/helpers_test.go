package chart

import (
	"strings"
	"testing"
)

// benchmarkData mirrors the real structural benchmark shape: one ordered
// dimension, four latency series with a query-only outlier, and a document
// count that is capped at the largest scale.
func benchmarkData() Dataset {
	labels := []string{"1,000", "5,000", "20,000", "50,000"}
	x := []float64{1000, 5000, 20000, 50000}
	points := func(values []float64) []Point {
		out := make([]Point, len(values))
		for index, value := range values {
			out[index] = Point{Label: labels[index], X: x[index], HasX: true, Value: Measured(value)}
		}
		return out
	}
	docs := points([]float64{1001, 5001, 20000, 20000})
	docs[3].Note = "capped"
	return Dataset{
		Title:      "structural benchmark",
		XLabel:     "files",
		XUnit:      UnitFiles,
		Ordered:    true,
		Provenance: Provenance{Kind: ProvenanceBenchmark, ID: "phase8", Source: "structural"},
		Series: []Series{
			{Name: "cold", Unit: UnitMilliseconds, Points: points([]float64{43, 210, 976, 1290})},
			{Name: "warm load", Unit: UnitMilliseconds, Points: points([]float64{17, 80, 370, 548})},
			{Name: "refresh", Unit: UnitMilliseconds, Points: points([]float64{12, 56, 243, 477})},
			{Name: "query-only", Unit: UnitMilliseconds, Aggregation: AggP50, Points: points([]float64{0.43, 3, 23, 21})},
			{Name: "docs", Unit: UnitFiles, Points: docs},
		},
	}
}

func latencyOnly(data Dataset) Dataset {
	out := data
	out.Series = out.Series[:4]
	return out
}

func mustBuild(t *testing.T, input PlanInput, width int) (ChartSpec, Plan) {
	t.Helper()
	spec, plan, err := Build(input, width)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return spec, plan
}

func benchmarkCharts(t *testing.T, data Dataset, width int) (ChartSpec, Plan, string) {
	t.Helper()
	spec, plan := mustBuild(t, PlanInput{Data: data}, width)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	return spec, plan, text
}

func containsBraille(text string) bool {
	for _, r := range text {
		if r >= 0x2800 && r <= 0x28ff {
			return true
		}
	}
	return false
}

// assertWidths fails when any rendered line exceeds the cell budget. The
// check uses the same rune width rules as the terminal.
func assertWidths(t *testing.T, text string, width int) {
	t.Helper()
	for index, line := range strings.Split(text, "\n") {
		if got := CellWidth(line); got > width {
			t.Fatalf("line %d is %d cells, budget %d: %q", index+1, got, width, line)
		}
	}
}
