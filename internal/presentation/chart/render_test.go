package chart

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestTableIsExactAndUnitAware(t *testing.T) {
	_, _, text := benchmarkCharts(t, benchmarkData(), 120)
	for _, want := range []string{
		"files", "cold", "warm load", "query-only p50", "docs (files)",
		"43 ms", "1.29 s", "0.43 ms", "20,000 (capped)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("table missing %q:\n%s", want, text)
		}
	}
	assertWidths(t, text, 120)
}

func TestNarrowTableStacksExactValues(t *testing.T) {
	_, _, text := benchmarkCharts(t, benchmarkData(), 24)
	for _, want := range []string{
		"query-only p50", "0.43 ms", "docs (files)", "20,000 (capped)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("stacked table missing %q:\n%s", want, text)
		}
	}
	assertWidths(t, text, 24)
}

func TestBarChartRendersCategoricalComparison(t *testing.T) {
	data := Dataset{Series: []Series{{Name: "calls", Unit: UnitCount, Points: []Point{
		{Label: "Tool calls", Value: Measured(42)},
		{Label: "Requests", Value: Measured(27)},
		{Label: "Turns", Value: Measured(14)},
		{Label: "Zero", Value: Measured(0)},
	}}}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 60)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Tool calls", "█", "42", "27", "14", "0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("bar missing %q:\n%s", want, text)
		}
	}
	assertWidths(t, text, 60)
}

func TestBarChartCentersNegativeValuesWithoutClipping(t *testing.T) {
	data := Dataset{Series: []Series{{Name: "delta", Unit: UnitMilliseconds, Points: []Point{
		{Label: "improved", Value: Measured(-500)},
		{Label: "regressed", Value: Measured(250)},
	}}}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 60)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "│") {
		t.Fatalf("negative bar has no baseline:\n%s", text)
	}
	if !strings.Contains(text, "-500 ms") || !strings.Contains(text, "250 ms") {
		t.Fatalf("signed values missing:\n%s", text)
	}
	assertWidths(t, text, 60)
}

func TestSingleSeriesLineUsesBraille(t *testing.T) {
	data := latencyOnly(benchmarkData())
	data.Series = data.Series[3:]
	spec, _ := mustBuild(t, PlanInput{Data: data}, 80)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !containsBraille(text) {
		t.Fatalf("single-series line has no braille:\n%s", text)
	}
	if !strings.Contains(text, "0 ms") {
		t.Fatalf("line axis lost the zero baseline:\n%s", text)
	}
	assertWidths(t, text, 80)
}

func TestMultiSeriesLineKeepsSeriesIdentityWithoutColor(t *testing.T) {
	data := latencyOnly(benchmarkData())
	data.Series = data.Series[:3]
	spec, _ := mustBuild(t, PlanInput{Data: data}, 80)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 cold", "2 warm load", "3 refresh"} {
		if !strings.Contains(text, want) {
			t.Fatalf("legend missing %q:\n%s", want, text)
		}
	}
	if containsBraille(text) {
		t.Fatalf("multi-series plot used unlabeled braille:\n%s", text)
	}
	assertWidths(t, text, 80)
}

func TestNoBrailleFallbackKeepsPoints(t *testing.T) {
	data := latencyOnly(benchmarkData())
	data.Series = data.Series[3:]
	spec, _ := mustBuild(t, PlanInput{Data: data}, 80)
	lines, err := RenderWith(spec, RenderOptions{NoBraille: true})
	if err != nil {
		t.Fatal(err)
	}
	text := LinesText(lines)
	if containsBraille(text) {
		t.Fatalf("fallback still contains braille:\n%s", text)
	}
	if !strings.Contains(text, "●") {
		t.Fatalf("fallback lost its points:\n%s", text)
	}
	assertWidths(t, text, 80)
}

func TestPanelsGiveOutlierAnIndependentScale(t *testing.T) {
	_, plan, text := benchmarkCharts(t, benchmarkData(), 100)
	if plan.Kind != KindPanels || len(plan.Panels) != 3 {
		t.Fatalf("plan = %q/%d, want panels/3", plan.Kind, len(plan.Panels))
	}
	if !strings.Contains(text, "query-only p50 (ms)") {
		t.Fatalf("outlier panel missing:\n%s", text)
	}
	// The first panel's axis reaches seconds; the query-only panel's axis
	// stays in milliseconds. If both shared one scale, 30 ms would be a
	// single baseline row rather than an axis label.
	if !strings.Contains(text, "1.5 s") || !strings.Contains(text, "30 ms") {
		t.Fatalf("independent scales not visible:\n%s", text)
	}
	assertWidths(t, text, 100)
}

func TestRenderRespectsEveryWidthBudget(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120, 200} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			_, _, text := benchmarkCharts(t, benchmarkData(), width)
			assertWidths(t, text, width)
		})
	}
}

func TestChangeRendersAbsoluteAndPercentageDelta(t *testing.T) {
	data := Dataset{Series: []Series{
		{Name: "before", Unit: UnitMilliseconds, Points: []Point{{Label: "cold", Value: Measured(12200)}}},
		{Name: "after", Unit: UnitMilliseconds, Points: []Point{{Label: "cold", Value: Measured(370)}}},
	}}
	spec, _ := mustBuild(t, PlanInput{Data: data, Request: Request{Kind: KindChange}}, 80)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"12.2 s", "370 ms", "-11.8", "-97%"} {
		if !strings.Contains(text, want) {
			t.Fatalf("change missing %q:\n%s", want, text)
		}
	}
	assertWidths(t, text, 80)
}

func TestFindingAndProvenanceArePreservedNotGenerated(t *testing.T) {
	data := latencyOnly(benchmarkData())
	data.Series = data.Series[:2]
	data.Finding = "Both files improved on warm load."
	data.Provenance = Provenance{
		Kind: ProvenanceEvidence, ID: "ev-42", Source: "TEST",
		State: "OBSERVED", Freshness: "CURRENT",
	}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 100)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Key finding:", "Both files improved", "source: evidence", "ev-42", "OBSERVED, CURRENT"} {
		if !strings.Contains(text, want) {
			t.Fatalf("presentation missing %q:\n%s", want, text)
		}
	}
}

func TestExactTextNeverTruncatesData(t *testing.T) {
	text, err := ExactText(PlanInput{Data: benchmarkData()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1.29 s", "0.43 ms", "20,000 (capped)", "query-only p50"} {
		if !strings.Contains(text, want) {
			t.Fatalf("exact text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "…") {
		t.Fatalf("exact text truncated a value:\n%s", text)
	}
}

func TestTableShowsMissingValueAsAbsence(t *testing.T) {
	data := Dataset{Ordered: true, Series: []Series{{Name: "latency", Unit: UnitMilliseconds, Points: []Point{
		{Label: "1", Value: Measured(10)},
		{Label: "2", Value: Missing()},
	}}}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 80)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "—") {
		t.Fatalf("missing value was not explicit:\n%s", text)
	}
	if strings.Contains(text, "0 ms") && !strings.Contains(text, "10 ms") {
		t.Fatalf("missing value became zero:\n%s", text)
	}
}

func TestRendererStripsControlBytesFromLabels(t *testing.T) {
	data := Dataset{Title: "bad \x1b[31mred\x1b[0m", Ordered: true, Series: []Series{
		{Name: "latency", Unit: UnitMilliseconds, Points: []Point{
			{Label: "one\x1b[2J", Value: Measured(10)},
			{Label: "two", Value: Measured(20)},
		}},
	}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 80)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("control byte escaped into output: %q", text)
	}
}

func TestRenderRejectsMalformedSpec(t *testing.T) {
	spec := ChartSpec{
		Kind:  KindBar,
		Width: 80,
		Series: []Series{{Name: "a", Unit: UnitCount, Points: []Point{
			{Label: "x", Value: Measured(1)},
		}}},
	}
	if _, err := Render(spec); !errors.Is(err, ErrMisaligned) {
		t.Fatalf("Render = %v, want ErrMisaligned", err)
	}
}

func TestXAxisTicksAlignWithPlottedPositions(t *testing.T) {
	data := Dataset{Ordered: true, Series: []Series{{Name: "latency", Unit: UnitMilliseconds, Points: []Point{
		{Label: "1", Value: Measured(1)},
		{Label: "2", Value: Measured(2)},
		{Label: "3", Value: Measured(3)},
	}}}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 50)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	axis := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "└") {
			axis = line
			break
		}
	}
	if axis == "" {
		t.Fatalf("no x axis line:\n%s", text)
	}
	if got := strings.Count(axis, "┬"); got != 3 {
		t.Fatalf("ticks = %d, want 3:\n%s", got, text)
	}
	first := strings.Index(axis, "┬")
	last := strings.LastIndex(axis, "┬")
	if last-first < 5 {
		t.Fatalf("ticks are not spread across the plot:\n%s", text)
	}
}

func TestNumericRowLabelsFallBackToStackedInsteadOfTruncating(t *testing.T) {
	data := Dataset{Ordered: true, XLabel: "files", XUnit: UnitFiles, Series: []Series{
		{Name: "cold", Unit: UnitMilliseconds, Points: []Point{
			{Label: "1,000", X: 1000, HasX: true, Value: Measured(43)},
			{Label: "5,000", X: 5000, HasX: true, Value: Measured(210)},
			{Label: "20,000", X: 20000, HasX: true, Value: Measured(976)},
			{Label: "50,000", X: 50000, HasX: true, Value: Measured(1290)},
		}},
	}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 50)
	text, err := RenderText(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "20,00 ") || strings.Contains(text, "50,00 ") {
		t.Fatalf("numeric label truncated:\n%s", text)
	}
	if !strings.Contains(text, "20,000") || !strings.Contains(text, "50,000") {
		t.Fatalf("numeric label lost:\n%s", text)
	}
}

func TestDegenerateDatasetsRenderOrDeclineTruthfully(t *testing.T) {
	point := func(label string, value float64) Point {
		return Point{Label: label, Value: Measured(value)}
	}
	cases := []struct {
		name string
		data Dataset
	}{
		{"one point", Dataset{Ordered: true, Series: []Series{{
			Name: "latency", Unit: UnitMilliseconds, Points: []Point{point("1,000", 43)},
		}}}},
		{"two points", Dataset{Ordered: true, Series: []Series{{
			Name: "latency", Unit: UnitMilliseconds, Points: []Point{point("1,000", 43), point("5,000", 210)},
		}}}},
		{"zero values", Dataset{Ordered: true, Series: []Series{{
			Name: "latency", Unit: UnitMilliseconds, Points: []Point{point("1,000", 0), point("5,000", 0)},
		}}}},
		{"huge value", Dataset{Ordered: true, Series: []Series{{
			Name: "latency", Unit: UnitMilliseconds, Points: []Point{point("1", 1e15), point("2", 2e15)},
		}}}},
		{"tiny value", Dataset{Ordered: true, Series: []Series{{
			Name: "latency", Unit: UnitMilliseconds, Points: []Point{point("1", 0.0000001), point("2", 0.0000002)},
		}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, _ := mustBuild(t, PlanInput{Data: tc.data}, 80)
			text, err := RenderText(spec)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(text) == "" {
				t.Fatal("empty rendering")
			}
		})
	}
	if _, _, err := Build(PlanInput{Data: Dataset{}}, 80); !errors.Is(err, ErrNoData) {
		t.Fatalf("empty dataset error = %v, want ErrNoData", err)
	}
}
