package chart

import (
	"strconv"
	"testing"
)

func benchInput() PlanInput { return PlanInput{Data: benchmarkData()} }

func benchSpec(b *testing.B, width int) ChartSpec {
	b.Helper()
	spec, _, err := Build(benchInput(), width)
	if err != nil {
		b.Fatal(err)
	}
	return spec
}

func BenchmarkBuild(b *testing.B) {
	input := benchInput()
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Build(input, 100); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderPanels(b *testing.B) {
	spec := benchSpec(b, 100)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(spec); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderTable(b *testing.B) {
	spec := benchSpec(b, 100)
	spec.Kind = KindTable
	spec.Panels = nil
	spec.IncludeTable = true
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(spec); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderBar(b *testing.B) {
	data := Dataset{Series: []Series{{Name: "calls", Unit: UnitCount, Points: []Point{
		{Label: "read", Value: Measured(42)},
		{Label: "execute", Value: Measured(27)},
		{Label: "search", Value: Measured(14)},
		{Label: "write", Value: Measured(6)},
	}}}}
	spec, _, err := Build(PlanInput{Data: data}, 80)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(spec); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderLineBraille(b *testing.B) {
	data := latencyOnly(benchmarkData())
	data.Series = data.Series[3:]
	spec := benchSpecFor(b, data, 80)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(spec); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderLineCells(b *testing.B) {
	data := latencyOnly(benchmarkData())
	spec := benchSpecFor(b, data, 80)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := RenderWith(spec, RenderOptions{NoBraille: true}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExactText(b *testing.B) {
	input := benchInput()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ExactText(input); err != nil {
			b.Fatal(err)
		}
	}
}

func benchSpecFor(b *testing.B, data Dataset, width int) ChartSpec {
	b.Helper()
	spec, _, err := Build(PlanInput{Data: data}, width)
	if err != nil {
		b.Fatal(err)
	}
	return spec
}

func syntheticDataset(seriesCount, pointCount int) Dataset {
	labels := make([]string, pointCount)
	points := make([][]Point, seriesCount)
	for s := range points {
		points[s] = make([]Point, pointCount)
	}
	for i := 0; i < pointCount; i++ {
		labels[i] = strconv.Itoa(i + 1)
		for s := 0; s < seriesCount; s++ {
			points[s][i] = Point{Label: labels[i], X: float64(i + 1), HasX: true, Value: Measured(float64((i + 1) * (s + 1)))}
		}
	}
	data := Dataset{Ordered: true, XLabel: "step", Series: make([]Series, seriesCount)}
	for s := 0; s < seriesCount; s++ {
		data.Series[s] = Series{Name: "series-" + strconv.Itoa(s+1), Unit: UnitMilliseconds, Points: points[s]}
	}
	return data
}

func BenchmarkBuildScales(b *testing.B) {
	for _, tc := range []struct {
		name           string
		series, points int
	}{
		{"small", 4, 4},
		{"medium", 4, 200},
		{"large", 4, 2000},
		{"many_series", 50, 50},
	} {
		input := PlanInput{Data: syntheticDataset(tc.series, tc.points)}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := Build(input, 100); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRenderScales(b *testing.B) {
	for _, tc := range []struct {
		name           string
		series, points int
	}{
		{"small", 4, 4},
		{"medium", 4, 200},
		{"large", 4, 2000},
		{"many_series", 50, 50},
	} {
		spec, _, err := Build(PlanInput{Data: syntheticDataset(tc.series, tc.points)}, 100)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Render(spec); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBuildWidths(b *testing.B) {
	input := benchInput()
	for _, width := range []int{40, 80, 120, 200} {
		b.Run(strconv.Itoa(width), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := Build(input, width); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
