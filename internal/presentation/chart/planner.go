package chart

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Planner limits. They are derived from what the renderer can draw without
// overlap, not from a particular terminal's width.
const (
	// DefaultWidth is used only when the caller has no measured width. Every
	// interactive caller supplies the actual terminal budget.
	DefaultWidth = 80
	// MinChartWidth is the smallest budget that can hold a y-axis label, a
	// plot, and one x label without overlap.
	MinChartWidth = 40
	// maxChartSeries keeps a single plot legible in monochrome and color.
	// More series become independent panels, never one unreadable tangle.
	maxChartSeries = 4
	// maxPanels bounds small multiples so one command cannot flood a
	// transcript with independent axes.
	maxPanels = 3
	// magnitudeGap is the ratio at which two series are no longer comparable
	// on one axis. A query-only series two orders of magnitude below a cold
	// series would flatten to the baseline; it gets its own panel instead.
	magnitudeGap = 10.0
)

// Build normalizes the input, decides a truthful presentation, and returns the
// validated spec plus the plan that produced it.
//
// Insufficient data is not an error: the exact table is always a valid answer
// and carries a note explaining what was omitted. Structural violations (an
// unknown kind, misaligned columns, a non-finite value) are errors because
// silently repairing them would fabricate meaning.
func Build(input PlanInput, width int) (ChartSpec, Plan, error) {
	if width <= 0 {
		width = DefaultWidth
	}
	if err := ValidateRequest(input.Request); err != nil {
		return ChartSpec{}, Plan{}, err
	}
	data, err := normalizeDataset(input.Data)
	if err != nil {
		return ChartSpec{}, Plan{}, err
	}
	if err := ValidateDataset(data); err != nil {
		return ChartSpec{}, Plan{}, err
	}
	series, err := selectedSeries(data, input.Request)
	if err != nil {
		return ChartSpec{}, Plan{}, err
	}
	spec := ChartSpec{
		Title:      data.Title,
		XLabel:     data.XLabel,
		XUnit:      data.XUnit,
		Series:     series,
		Width:      width,
		Finding:    data.Finding,
		Provenance: data.Provenance,
	}
	plan := Plan{Kind: KindTable, IncludeTable: true}
	spec.IncludeTable = !input.Request.OmitTable

	measured, missing := measuredValues(series)
	if missing > 0 {
		plan.Notes = append(plan.Notes, fmt.Sprintf("%d values missing (shown as —)", missing))
	}
	if measured == 0 {
		plan.Notes = append(plan.Notes, "no measured values: the exact table is the only representation")
		spec.Notes = appendNotes(data.Note, plan.Notes)
		return finish(spec, plan)
	}
	if width < MinChartWidth {
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"chart omitted: %d cells available (need %d)", width, MinChartWidth))
		spec.Notes = appendNotes(data.Note, plan.Notes)
		return finish(spec, plan)
	}

	switch input.Request.Kind {
	case KindTable:
		spec.Notes = appendNotes(data.Note, plan.Notes)
		return finish(spec, plan)
	case KindChange:
		return buildChange(spec, plan, data, series, width)
	case KindBar:
		if len(series) != 1 {
			plan.Notes = append(plan.Notes, fmt.Sprintf(
				"bar omitted: %d series share no categorical scale; showing the exact table", len(series)))
			spec.Notes = appendNotes(data.Note, plan.Notes)
			return finish(spec, plan)
		}
		panel := buildPanel(data, series, []int{0}, false, input.Request)
		plan.Kind = KindBar
		plan.Panels = []Panel{panel}
		plan.IncludeTable = spec.IncludeTable
		spec.Kind = KindBar
		spec.Panels = plan.Panels
		spec.Notes = appendNotes(data.Note, plan.Notes)
		return finish(spec, plan)
	case KindLine:
		if !data.Ordered || len(series[0].Points) < 2 {
			plan.Notes = append(plan.Notes, "line omitted: the dimension is not ordered")
			spec.Notes = appendNotes(data.Note, plan.Notes)
			return finish(spec, plan)
		}
	}

	panels, notes := autoPanels(data, series, input.Request)
	plan.Notes = append(plan.Notes, notes...)
	if len(panels) == 0 {
		if len(notes) == 0 {
			plan.Notes = append(plan.Notes, "chart omitted: the selected series share no ordered axis")
		}
		spec.Notes = appendNotes(data.Note, plan.Notes)
		return finish(spec, plan)
	}
	plan.Panels = panels
	plan.IncludeTable = spec.IncludeTable
	if len(panels) == 1 {
		plan.Kind = panels[0].Kind
	} else {
		plan.Kind = KindPanels
	}
	spec.Kind = plan.Kind
	spec.Panels = panels
	spec.Notes = appendNotes(data.Note, plan.Notes)
	return finish(spec, plan)
}

// finish records the final include-table decision on both views.
func finish(spec ChartSpec, plan Plan) (ChartSpec, Plan, error) {
	if spec.Kind == "" {
		spec.Kind = plan.Kind
	}
	if plan.Kind == KindTable {
		// The exact table is the representation; omitting it would leave
		// nothing truthful to render.
		plan.IncludeTable = true
	}
	spec.IncludeTable = plan.IncludeTable
	if err := ValidateSpec(spec); err != nil {
		return ChartSpec{}, Plan{}, err
	}
	return spec, plan, nil
}

// appendNotes merges a dataset caveat with planner notes, preserving order and
// dropping empties.
func appendNotes(datasetNote string, notes []string) []string {
	out := make([]string, 0, len(notes)+1)
	if note := sanitizeLabel(datasetNote); note != "" {
		out = append(out, note)
	}
	for _, note := range notes {
		if note = sanitizeLabel(note); note != "" {
			out = append(out, note)
		}
	}
	return out
}

// measuredValues counts present and missing observations across the selected
// series. A missing value is not a zero measurement.
func measuredValues(series []Series) (measured, missing int) {
	for _, s := range series {
		for _, point := range s.Points {
			if point.Value.Present {
				measured++
			} else {
				missing++
			}
		}
	}
	return measured, missing
}

// buildChange plans the before/after form. It is used only when exactly two
// series with one unit were selected; otherwise the exact table is safer.
func buildChange(spec ChartSpec, plan Plan, data Dataset, series []Series, width int) (ChartSpec, Plan, error) {
	if len(series) != 2 || series[0].Unit != series[1].Unit {
		plan.Notes = append(plan.Notes, "change omitted: exactly two series with one unit are required")
		spec.Notes = appendNotes(data.Note, plan.Notes)
		return finish(spec, plan)
	}
	if len(series[0].Points) < 1 {
		plan.Notes = append(plan.Notes, "change omitted: no points")
		spec.Notes = appendNotes(data.Note, plan.Notes)
		return finish(spec, plan)
	}
	plan.Kind = KindChange
	// The change grid is the exact table for this form; repeating a raw
	// before/after table above it would present the same values twice.
	plan.IncludeTable = false
	spec.Kind = KindChange
	spec.Panels = nil
	spec.Notes = appendNotes(data.Note, plan.Notes)
	return finish(spec, plan)
}

// autoPanels groups by unit, then by magnitude within a unit. Different units
// are never placed on one axis; a series two orders of magnitude below its
// peers gets an independent scale rather than being flattened.
func autoPanels(data Dataset, series []Series, req Request) ([]Panel, []string) {
	var notes []string
	unitGroups := groupByUnit(series)
	seenUnit := make(map[Unit]bool, len(unitGroups))
	var panels []Panel
	for _, group := range unitGroups {
		unit := series[group[0]].Unit
		if seenUnit[unit] {
			continue
		}
		seenUnit[unit] = true
		if !data.Ordered {
			if len(group) == 1 {
				panels = append(panels, buildPanel(data, series, group, false, req))
			} else {
				notes = append(notes, fmt.Sprintf(
					"chart omitted for %s: %d categorical series share no ordered axis",
					UnitLabel(unit), len(group)))
			}
			continue
		}
		subgroups := splitByMagnitude(series, group)
		for _, subgroup := range subgroups {
			chunks := chunkIndexes(subgroup, maxChartSeries)
			for _, chunk := range chunks {
				panels = append(panels, buildPanel(data, series, chunk, true, req))
			}
		}
	}
	if len(panels) <= maxPanels {
		return panels, notes
	}
	// More independent scales than the presentation can hold. Try to merge
	// the closest magnitude groups within one unit; different units may not
	// merge because their axes are incompatible.
	if len(panels) > maxPanels {
		notes = append(notes, "independent scales exceed the panel limit")
	}
	return nil, notes
}

// groupByUnit returns series indexes grouped by unit in first-appearance
// order. The slice order is deterministic and independent of map iteration.
func groupByUnit(series []Series) [][]int {
	order := make([]Unit, 0, len(series))
	groups := make(map[Unit][]int, len(series))
	for index, s := range series {
		if _, ok := groups[s.Unit]; !ok {
			order = append(order, s.Unit)
		}
		groups[s.Unit] = append(groups[s.Unit], index)
	}
	out := make([][]int, 0, len(order))
	for _, unit := range order {
		out = append(out, groups[unit])
	}
	return out
}

// splitByMagnitude splits one unit group where the largest measured magnitude
// drops by at least one order of magnitude. The result is ordered from the
// largest scale to the smallest, with stable index order inside a group.
func splitByMagnitude(series []Series, indexes []int) [][]int {
	type ranked struct {
		index int
		max   float64
	}
	rankedSeries := make([]ranked, 0, len(indexes))
	for _, index := range indexes {
		max := maxAbs(series[index])
		rankedSeries = append(rankedSeries, ranked{index: index, max: max})
	}
	sort.SliceStable(rankedSeries, func(i, j int) bool {
		if rankedSeries[i].max == rankedSeries[j].max {
			return rankedSeries[i].index < rankedSeries[j].index
		}
		return rankedSeries[i].max > rankedSeries[j].max
	})
	var groups [][]int
	current := []int{}
	previous := 0.0
	for _, entry := range rankedSeries {
		if len(current) > 0 && previous > 0 && entry.max > 0 && previous/entry.max >= magnitudeGap {
			groups = append(groups, sortedIndexes(current))
			current = nil
		}
		current = append(current, entry.index)
		if entry.max > 0 {
			previous = entry.max
		}
	}
	if len(current) > 0 {
		groups = append(groups, sortedIndexes(current))
	}
	groups = capGroups(series, groups)
	return groups
}

// capGroups merges the closest adjacent magnitude groups until the panel
// limit is met. It never merges across units because callers pass one group.
func capGroups(series []Series, groups [][]int) [][]int {
	for len(groups) > maxPanels {
		best := 0
		bestGap := math.Inf(1)
		for i := 0; i+1 < len(groups); i++ {
			left := groupMax(series, groups[i])
			right := groupMax(series, groups[i+1])
			gap := 0.0
			if left > 0 && right > 0 {
				gap = math.Log10(left) - math.Log10(right)
			}
			if gap < bestGap {
				bestGap, best = gap, i
			}
		}
		merged := append(append([]int(nil), groups[best]...), groups[best+1]...)
		groups[best] = sortedIndexes(merged)
		groups = append(groups[:best+1], groups[best+2:]...)
	}
	return groups
}

func groupMax(series []Series, indexes []int) float64 {
	max := 0.0
	for _, index := range indexes {
		if value := maxAbs(series[index]); value > max {
			max = value
		}
	}
	return max
}

// maxAbs returns the largest absolute measured value. A zero-only series has
// max 0 and therefore no magnitude gap; it stays with the smallest group.
func maxAbs(series Series) float64 {
	max := 0.0
	for _, point := range series.Points {
		if !point.Value.Present {
			continue
		}
		if value := math.Abs(point.Value.Number); value > max {
			max = value
		}
	}
	return max
}

func sortedIndexes(indexes []int) []int {
	out := append([]int(nil), indexes...)
	sort.Ints(out)
	return out
}

func chunkIndexes(indexes []int, size int) [][]int {
	if size <= 0 || len(indexes) == 0 {
		return nil
	}
	var out [][]int
	for start := 0; start < len(indexes); start += size {
		end := start + size
		if end > len(indexes) {
			end = len(indexes)
		}
		out = append(out, append([]int(nil), indexes[start:end]...))
	}
	return out
}

// buildPanel chooses the line/bar form and the axis scales for one panel. The
// scale note is printed with the panel, not repeated in the plan notes.
func buildPanel(data Dataset, series []Series, indexes []int, ordered bool, req Request) Panel {
	kind := KindBar
	if ordered {
		kind = KindLine
	}
	panel := Panel{
		Title:  panelTitle(series, indexes),
		Kind:   kind,
		Series: append([]int(nil), indexes...),
		XScale: chooseXScale(data, series, indexes),
		YScale: ScaleLinear,
	}
	var notes []string
	logRequested := req.LogY || allSeriesLog(series, indexes)
	if logRequested {
		if allPositive(series, indexes) {
			panel.YScale = ScaleLog
			notes = append(notes, "log scale")
		} else {
			notes = append(notes, "log scale unavailable: a zero or negative value is present; using linear")
		}
	}
	if panel.XScale == ScaleLog {
		notes = append(notes, "log x axis")
	}
	panel.ScaleNote = strings.Join(notes, "; ")
	return panel
}

func allSeriesLog(series []Series, indexes []int) bool {
	if len(indexes) == 0 {
		return false
	}
	for _, index := range indexes {
		if series[index].Scale != ScaleLog {
			return false
		}
	}
	return true
}

func allPositive(series []Series, indexes []int) bool {
	found := false
	for _, index := range indexes {
		for _, point := range series[index].Points {
			if !point.Value.Present {
				continue
			}
			if point.Value.Number <= 0 {
				return false
			}
			found = true
		}
	}
	return found
}

// chooseXScale uses a logarithmic position only when the numeric dimension
// spans at least two orders of magnitude and every position is positive. A
// categorical axis stays linear because its positions are ordinals.
func chooseXScale(data Dataset, series []Series, indexes []int) ScaleKind {
	if !data.Ordered || len(indexes) == 0 {
		return ScaleLinear
	}
	min := math.Inf(1)
	max := math.Inf(-1)
	for _, point := range series[indexes[0]].Points {
		if !point.HasX {
			return ScaleLinear
		}
		if point.X <= 0 {
			return ScaleLinear
		}
		if point.X < min {
			min = point.X
		}
		if point.X > max {
			max = point.X
		}
	}
	if min > 0 && max/min >= 100 {
		return ScaleLog
	}
	return ScaleLinear
}

// panelTitle names the series and their shared unit. The renderer truncates
// it to the available width; this function never invents an aggregate.
func panelTitle(series []Series, indexes []int) string {
	if len(indexes) == 0 {
		return ""
	}
	names := make([]string, 0, len(indexes))
	for _, index := range indexes {
		names = append(names, seriesName(series[index]))
	}
	title := strings.Join(names, " · ")
	if unit := UnitLabel(series[indexes[0]].Unit); unit != "" {
		title += " (" + unit + ")"
	}
	return title
}

// Summarize builds the compact model-facing acknowledgement.
func Summarize(spec ChartSpec, plan Plan) Summary {
	summary := Summary{
		Kind:   spec.Kind,
		Series: len(spec.Series),
		Table:  spec.IncludeTable,
	}
	if len(plan.Panels) > 0 {
		summary.Panels = len(plan.Panels)
	} else if len(spec.Panels) > 0 {
		summary.Panels = len(spec.Panels)
	}
	for _, series := range spec.Series {
		summary.Points += len(series.Points)
		for _, point := range series.Points {
			if !point.Value.Present {
				summary.Missing++
			}
		}
	}
	return summary
}

// String renders the semantic acknowledgement. It is intentionally one line
// and contains no chart geometry.
func (s Summary) String() string {
	table := "no"
	if s.Table {
		table = "yes"
	}
	text := fmt.Sprintf("chart rendered: kind=%s series=%d points=%d panels=%d table=%s",
		s.Kind, s.Series, s.Points, s.Panels, table)
	if s.Missing > 0 {
		text += fmt.Sprintf(" missing=%d", s.Missing)
	}
	return text
}
