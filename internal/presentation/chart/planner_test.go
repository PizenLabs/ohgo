package chart

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPlannerSplitsQueryOnlyIntoIndependentScale(t *testing.T) {
	spec, plan := mustBuild(t, PlanInput{Data: latencyOnly(benchmarkData())}, 100)
	if spec.Kind != KindPanels {
		t.Fatalf("kind = %q, want panels", spec.Kind)
	}
	if len(spec.Panels) != 2 {
		t.Fatalf("panels = %d, want 2", len(spec.Panels))
	}
	if len(spec.Panels[0].Series) != 3 {
		t.Fatalf("first panel series = %d, want 3", len(spec.Panels[0].Series))
	}
	if len(spec.Panels[1].Series) != 1 {
		t.Fatalf("outlier panel series = %d, want 1", len(spec.Panels[1].Series))
	}
	if !strings.Contains(spec.Panels[1].Title, "query-only") {
		t.Fatalf("outlier panel title = %q", spec.Panels[1].Title)
	}
	if !spec.IncludeTable {
		t.Fatal("chart dropped the authoritative table")
	}
	if plan.Kind != KindPanels {
		t.Fatalf("plan kind = %q", plan.Kind)
	}
}

func TestPlannerKeepsComparableSeriesOnOneAxis(t *testing.T) {
	data := latencyOnly(benchmarkData())
	data.Series = data.Series[:2]
	spec, _ := mustBuild(t, PlanInput{Data: data}, 100)
	if spec.Kind != KindLine || len(spec.Panels) != 1 {
		t.Fatalf("kind/panels = %q/%d, want line/1", spec.Kind, len(spec.Panels))
	}
	if len(spec.Panels[0].Series) != 2 {
		t.Fatalf("panel series = %d, want 2", len(spec.Panels[0].Series))
	}
}

func TestPlannerNeverCombinesIncompatibleUnits(t *testing.T) {
	data := Dataset{Ordered: true, Series: []Series{
		{Name: "latency", Unit: UnitMilliseconds, Points: []Point{
			{Label: "1", Value: Measured(10)},
			{Label: "2", Value: Measured(20)},
		}},
		{Name: "tokens", Unit: UnitTokens, Points: []Point{
			{Label: "1", Value: Measured(100)},
			{Label: "2", Value: Measured(200)},
		}},
	}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 100)
	if spec.Kind != KindPanels || len(spec.Panels) != 2 {
		t.Fatalf("kind/panels = %q/%d, want panels/2", spec.Kind, len(spec.Panels))
	}
	for _, panel := range spec.Panels {
		unit := spec.Series[panel.Series[0]].Unit
		for _, index := range panel.Series {
			if spec.Series[index].Unit != unit {
				t.Fatalf("panel combines %q with %q", unit, spec.Series[index].Unit)
			}
		}
	}
}

func TestPlannerUsesBarForSingleCategoricalSeries(t *testing.T) {
	data := Dataset{Series: []Series{{Name: "calls", Unit: UnitCount, Points: []Point{
		{Label: "read", Value: Measured(42)},
		{Label: "execute", Value: Measured(27)},
		{Label: "search", Value: Measured(14)},
	}}}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 80)
	if spec.Kind != KindBar || len(spec.Panels) != 1 {
		t.Fatalf("kind/panels = %q/%d, want bar/1", spec.Kind, len(spec.Panels))
	}
}

func TestPlannerFallsBackToTableForCategoricalMultiSeries(t *testing.T) {
	data := Dataset{Series: []Series{
		{Name: "before", Unit: UnitCount, Points: []Point{
			{Label: "a", Value: Measured(1)},
			{Label: "b", Value: Measured(2)},
		}},
		{Name: "after", Unit: UnitCount, Points: []Point{
			{Label: "a", Value: Measured(3)},
			{Label: "b", Value: Measured(4)},
		}},
	}}
	spec, plan := mustBuild(t, PlanInput{Data: data}, 80)
	if spec.Kind != KindTable {
		t.Fatalf("kind = %q, want table", spec.Kind)
	}
	if len(plan.Notes) == 0 {
		t.Fatal("table fallback has no note")
	}
}

func TestPlannerNarrowWidthKeepsExactTable(t *testing.T) {
	spec, plan := mustBuild(t, PlanInput{Data: benchmarkData()}, 24)
	if spec.Kind != KindTable {
		t.Fatalf("kind = %q, want table", spec.Kind)
	}
	if !spec.IncludeTable {
		t.Fatal("narrow table omitted itself")
	}
	if len(plan.Notes) == 0 || !strings.Contains(strings.Join(plan.Notes, " "), "chart omitted") {
		t.Fatalf("notes = %v", plan.Notes)
	}
}

func TestPlannerChangeRequiresTwoComparableSeries(t *testing.T) {
	data := Dataset{Ordered: false, Series: []Series{
		{Name: "before", Unit: UnitMilliseconds, Points: []Point{
			{Label: "cold", Value: Measured(12200)},
		}},
		{Name: "after", Unit: UnitMilliseconds, Points: []Point{
			{Label: "cold", Value: Measured(370)},
		}},
	}}
	spec, plan := mustBuild(t, PlanInput{Data: data, Request: Request{Kind: KindChange}}, 80)
	if spec.Kind != KindChange || plan.Kind != KindChange {
		t.Fatalf("kind = %q / plan %q", spec.Kind, plan.Kind)
	}
	if spec.IncludeTable {
		t.Fatal("change repeated a separate raw table")
	}

	single := Dataset{Series: []Series{data.Series[0]}}
	spec, plan = mustBuild(t, PlanInput{Data: single, Request: Request{Kind: KindChange}}, 80)
	if spec.Kind != KindTable || plan.Kind != KindTable {
		t.Fatalf("one-series change = %q / %q, want table", spec.Kind, plan.Kind)
	}
}

func TestPlannerLogScaleIsSafeAndExplicit(t *testing.T) {
	positive := Dataset{Ordered: true, Series: []Series{{Name: "latency", Unit: UnitMilliseconds, Points: []Point{
		{Label: "1", Value: Measured(1)},
		{Label: "2", Value: Measured(10)},
		{Label: "3", Value: Measured(100)},
	}}}}
	spec, _ := mustBuild(t, PlanInput{Data: positive, Request: Request{LogY: true}}, 80)
	if spec.Panels[0].YScale != ScaleLog {
		t.Fatalf("positive log scale = %q", spec.Panels[0].YScale)
	}
	if !strings.Contains(spec.Panels[0].ScaleNote, "log scale") {
		t.Fatalf("scale note = %q", spec.Panels[0].ScaleNote)
	}

	zero := Dataset{Ordered: true, Series: []Series{{Name: "latency", Unit: UnitMilliseconds, Points: []Point{
		{Label: "1", Value: Measured(0)},
		{Label: "2", Value: Measured(10)},
	}}}}
	spec, _ = mustBuild(t, PlanInput{Data: zero, Request: Request{LogY: true}}, 80)
	if spec.Panels[0].YScale != ScaleLinear {
		t.Fatalf("zero log scale = %q, want linear", spec.Panels[0].YScale)
	}
	if !strings.Contains(spec.Panels[0].ScaleNote, "unavailable") {
		t.Fatalf("fallback note = %q", spec.Panels[0].ScaleNote)
	}
}

func TestPlannerHonoursSeriesSelectionAndRejectsUnknown(t *testing.T) {
	spec, _ := mustBuild(t, PlanInput{
		Data:    benchmarkData(),
		Request: Request{Series: []string{"query-only"}},
	}, 100)
	if len(spec.Series) != 1 || spec.Series[0].Name != "query-only" {
		t.Fatalf("selected series = %+v", spec.Series)
	}
	if _, _, err := Build(PlanInput{
		Data:    benchmarkData(),
		Request: Request{Series: []string{"missing"}},
	}, 100); !errors.Is(err, ErrUnknownSeries) {
		t.Fatalf("unknown series = %v", err)
	}
}

func TestPlannerPreservesMissingValues(t *testing.T) {
	data := Dataset{Ordered: true, Series: []Series{{Name: "latency", Unit: UnitMilliseconds, Points: []Point{
		{Label: "1", Value: Measured(10)},
		{Label: "2", Value: Missing()},
		{Label: "3", Value: Measured(30)},
	}}}}
	spec, plan := mustBuild(t, PlanInput{Data: data}, 80)
	if spec.Series[0].Points[1].Value.Present {
		t.Fatal("missing value became present")
	}
	if !strings.Contains(strings.Join(plan.Notes, " "), "missing") {
		t.Fatalf("notes = %v", plan.Notes)
	}
}

func TestPlannerIsDeterministic(t *testing.T) {
	input := PlanInput{Data: benchmarkData()}
	first, firstPlan := mustBuild(t, input, 100)
	second, secondPlan := mustBuild(t, input, 100)
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("spec is not deterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	firstPlanJSON, _ := json.Marshal(firstPlan)
	secondPlanJSON, _ := json.Marshal(secondPlan)
	if string(firstPlanJSON) != string(secondPlanJSON) {
		t.Fatalf("plan is not deterministic:\n%s\n%s", firstPlanJSON, secondPlanJSON)
	}
}

func TestPlannerCapsIndependentUnits(t *testing.T) {
	units := []Unit{UnitMilliseconds, UnitTokens, UnitFiles, UnitBytes, UnitUSD}
	series := make([]Series, 0, len(units))
	for index, unit := range units {
		series = append(series, Series{Name: string(unit), Unit: unit, Points: []Point{
			{Label: "1", Value: Measured(float64(index + 1))},
			{Label: "2", Value: Measured(float64(index + 2))},
		}})
	}
	spec, plan := mustBuild(t, PlanInput{Data: Dataset{Ordered: true, Series: series}}, 100)
	if spec.Kind != KindTable {
		t.Fatalf("kind = %q, want table", spec.Kind)
	}
	if !strings.Contains(strings.Join(plan.Notes, " "), "panel limit") {
		t.Fatalf("notes = %v", plan.Notes)
	}
}
