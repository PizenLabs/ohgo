package chart

import (
	"math"
	"strconv"
	"strings"
)

// linePlotHeight is the cell height of one plot. Charts live in a scrollable
// transcript, so a fixed compact height is more useful than consuming the
// terminal's row budget. The width is the only dimension that must adapt to
// the actual terminal.
const linePlotHeight = 8

// linePlot is one resolved chart surface before text projection.
type plotCell struct {
	char rune
	role Role
}

// renderLinePanel draws an ordered multi-series chart. A single series uses
// Braille dots; multiple series also receive endpoint digits and a legend so
// identity survives a monochrome or reduced-color terminal.
func renderLinePanel(spec ChartSpec, panel Panel, opts RenderOptions) []Line {
	if len(panel.Series) == 0 {
		return nil
	}
	lines := chartPrelude(spec, panel)
	series := make([]Series, 0, len(panel.Series))
	for _, index := range panel.Series {
		series = append(series, spec.Series[index])
	}
	points := len(series[0].Points)
	if points < 2 {
		return append(lines, renderBarSeries(spec, series[0])...)
	}
	domain := valueDomain(series, panel.YScale)
	if domain.low == domain.high {
		if panel.YScale == ScaleLog && domain.low > 0 {
			domain.high = domain.low * 10
		} else {
			domain.high = domain.low + 1
		}
	}
	ticks, niceLow, niceHigh := axisTicks(domain.low, domain.high, panel.YScale, 5)
	labelWidth := 0
	for _, tick := range ticks {
		if width := CellWidth(axisText(tick, series[0].Unit)); width > labelWidth {
			labelWidth = width
		}
	}
	if labelWidth < 3 {
		labelWidth = 3
	}
	plotWidth := spec.Width - labelWidth - 2
	if plotWidth < 4 {
		headers := make([]gridCell, 0, len(series)+1)
		headers = append(headers, gridCell{text: spec.XLabel, role: RoleHeader})
		for _, s := range series {
			headers = append(headers, gridCell{text: tableHeader(s), role: RoleHeader})
		}
		return append(lines, renderStacked(headers, tableRows(series), spec.Width)...)
	}
	xPositions, xLabels := projectX(series[0], plotWidth, panel.XScale)
	// Braille improves single-series resolution. A multi-series plot uses
	// cell markers plus endpoint digits so identity survives monochrome.
	useBraille := !opts.NoBraille && len(series) == 1
	var braille [][]uint8
	var brailleRole [][]Role
	var cells [][]plotCell
	if useBraille {
		braille = make([][]uint8, linePlotHeight)
		brailleRole = make([][]Role, linePlotHeight)
		for row := range braille {
			braille[row] = make([]uint8, plotWidth)
			brailleRole[row] = make([]Role, plotWidth)
		}
	} else {
		cells = newCellGrid(linePlotHeight, plotWidth)
	}
	// Draw each series in dataset order. A cell already owned by an earlier
	// series keeps its identity, so overlapping values never make the first
	// series disappear nondeterministically.
	for seriesOrder, s := range series {
		role := seriesRole(seriesOrder)
		if useBraille {
			drawBrailleSeries(braille, brailleRole, s, xPositions, niceLow, niceHigh, panel.YScale, role)
			continue
		}
		marker := '●'
		if len(series) > 1 {
			marker = rune('1' + seriesOrder)
		}
		drawCellSeries(cells, s, xPositions, niceLow, niceHigh, panel.YScale, role, marker)
	}
	// Y axis and plot rows.
	tickRows := make(map[int]string, len(ticks))
	for _, tick := range ticks {
		row := valueRow(tick, niceLow, niceHigh, panel.YScale, linePlotHeight)
		if row < 0 || row >= linePlotHeight {
			continue
		}
		if _, exists := tickRows[row]; !exists {
			tickRows[row] = axisText(tick, series[0].Unit)
		}
	}
	for row := range linePlotHeight {
		var spans []Span
		if label, ok := tickRows[row]; ok {
			spans = append(spans, Span{Text: padLeft(label, labelWidth), Role: RoleAxis})
		} else {
			spans = append(spans, Span{Text: strings.Repeat(" ", labelWidth), Role: RoleAxis})
		}
		spans = append(spans, Span{Text: "┤", Role: RoleAxis})
		if useBraille {
			spans = append(spans, brailleLine(braille[row], brailleRole[row])...)
		} else {
			spans = append(spans, cellLine(cells[row])...)
		}
		lines = append(lines, Line{Spans: spans})
	}
	// Bottom axis with x tick marks.
	axis := []rune("└" + strings.Repeat("─", plotWidth))
	for _, index := range labelPositions(xPositions, xLabels, plotWidth) {
		if index < 0 || index >= len(xPositions) {
			continue
		}
		column := xPositions[index]
		if column >= 0 && column < plotWidth {
			axis[column+1] = '┬'
		}
	}
	lines = append(lines, Line{Spans: []Span{
		{Text: strings.Repeat(" ", labelWidth)},
		{Text: string(axis), Role: RoleAxis},
	}})
	lines = append(lines, xAxisLabels(xPositions, xLabels, plotWidth, labelWidth)...)
	if len(series) > 1 {
		lines = append(lines, legendLine(series, spec.Width))
	}
	return lines
}

// chartPrelude emits the panel title and scale note exactly once. Small
// multiples render the panel title in Render, so the chart itself only adds
// the scale note there.
func chartPrelude(spec ChartSpec, panel Panel) []Line {
	var lines []Line
	if spec.Kind != KindPanels && !spec.IncludeTable && panel.Title != "" {
		for _, title := range wrap(panel.Title, spec.Width) {
			lines = append(lines, textLine(title, RolePanelTitle))
		}
	}
	if panel.ScaleNote != "" {
		for _, note := range wrap(panel.ScaleNote, spec.Width) {
			lines = append(lines, textLine(note, RoleMuted))
		}
	}
	if len(lines) > 0 {
		lines = append(lines, Line{})
	}
	return lines
}

type domain struct {
	low  float64
	high float64
}

// valueDomain resolves the axis domain. Linear always includes zero so a
// truncated baseline cannot exaggerate a change.
func valueDomain(series []Series, scale ScaleKind) domain {
	min := math.Inf(1)
	max := math.Inf(-1)
	for _, s := range series {
		for _, point := range s.Points {
			if !point.Value.Present {
				continue
			}
			value := point.Value.Number
			if value < min {
				min = value
			}
			if value > max {
				max = value
			}
		}
	}
	if math.IsInf(min, 1) {
		return domain{low: 0, high: 1}
	}
	if scale == ScaleLog {
		if min <= 0 {
			// The planner rejects this combination, but a direct spec must not
			// silently draw a logarithm of a non-positive value.
			return domain{low: 1, high: 10}
		}
		return domain{low: min, high: max}
	}
	if min > 0 {
		min = 0
	}
	if max < 0 {
		max = 0
	}
	if min == max {
		max = min + 1
	}
	return domain{low: min, high: max}
}

// axisTicks returns readable tick values plus the exact nice domain used for
// mapping. Ticks are deterministic for a given domain and scale.
func axisTicks(low, high float64, scale ScaleKind, target int) ([]float64, float64, float64) {
	if target < 2 {
		target = 2
	}
	if scale == ScaleLog && low > 0 && high > 0 {
		lowExp := int(math.Floor(math.Log10(low)))
		highExp := int(math.Ceil(math.Log10(high)))
		if highExp-lowExp > 8 {
			highExp = lowExp + 8
		}
		var ticks []float64
		for exp := lowExp; exp <= highExp; exp++ {
			base := math.Pow(10, float64(exp))
			for _, multiplier := range []float64{1, 2, 5} {
				value := base * multiplier
				if value < low || value > high {
					continue
				}
				ticks = append(ticks, value)
				if len(ticks) >= target+2 {
					break
				}
			}
			if len(ticks) >= target+2 {
				break
			}
		}
		if len(ticks) == 0 {
			ticks = []float64{low, high}
		}
		return ticks, low, high
	}
	rawStep := (high - low) / float64(target-1)
	if rawStep <= 0 || math.IsInf(rawStep, 0) {
		rawStep = 1
	}
	step := niceStep(rawStep)
	niceLow := math.Floor(low/step) * step
	niceHigh := math.Ceil(high/step) * step
	if niceHigh == niceLow {
		niceHigh = niceLow + step
	}
	ticks := make([]float64, 0, target+2)
	for value := niceLow; value <= niceHigh+step/1000; value += step {
		ticks = append(ticks, normalizeZero(value))
		if len(ticks) > target+8 {
			break
		}
	}
	return ticks, normalizeZero(niceLow), normalizeZero(niceHigh)
}

// niceStep rounds a raw step to one of 1, 2, or 5 times a power of ten.
func niceStep(raw float64) float64 {
	magnitude := math.Pow(10, math.Floor(math.Log10(raw)))
	residual := raw / magnitude
	step := 10.0
	switch {
	case residual <= 1:
		step = 1
	case residual <= 2:
		step = 2
	case residual <= 5:
		step = 5
	}
	return step * magnitude
}

func normalizeZero(value float64) float64 {
	if math.Abs(value) < 1e-12 {
		return 0
	}
	return value
}

// projectX maps every point to a plot column and returns its label. Ordinal
// dimensions are evenly spaced; numeric dimensions use their actual position
// so an ordered axis is not silently treated as a category list.
func projectX(series Series, plotWidth int, scale ScaleKind) ([]int, []string) {
	positions := make([]int, len(series.Points))
	labels := make([]string, len(series.Points))
	for index, point := range series.Points {
		labels[index] = point.Label
		if labels[index] == "" {
			labels[index] = strconv.Itoa(index + 1)
		}
	}
	if len(series.Points) == 1 {
		positions[0] = plotWidth / 2
		return positions, labels
	}
	hasX := series.Points[0].HasX
	if !hasX {
		for index := range series.Points {
			positions[index] = int(math.Round(float64(index) * float64(plotWidth-1) / float64(len(series.Points)-1)))
		}
		return positions, labels
	}
	min := series.Points[0].X
	max := series.Points[0].X
	for _, point := range series.Points {
		if point.X < min {
			min = point.X
		}
		if point.X > max {
			max = point.X
		}
	}
	for index, point := range series.Points {
		fraction := 0.0
		switch {
		case max == min:
			fraction = 0.5
		case scale == ScaleLog && min > 0 && point.X > 0:
			fraction = (math.Log10(point.X) - math.Log10(min)) / (math.Log10(max) - math.Log10(min))
		default:
			fraction = (point.X - min) / (max - min)
		}
		positions[index] = clampInt(int(math.Round(fraction*float64(plotWidth-1))), 0, plotWidth-1)
	}
	return positions, labels
}

func valueRow(value, low, high float64, scale ScaleKind, height int) int {
	if height <= 1 {
		return 0
	}
	fraction := 0.0
	if scale == ScaleLog && low > 0 && high > 0 && value > 0 {
		fraction = (math.Log10(high) - math.Log10(value)) / (math.Log10(high) - math.Log10(low))
	} else if high != low {
		fraction = (high - value) / (high - low)
	}
	return clampInt(int(math.Round(fraction*float64(height-1))), 0, height-1)
}

func dotPosition(value float64, low, high float64, scale ScaleKind, height int) int {
	if height <= 1 {
		return 0
	}
	fraction := 0.0
	if scale == ScaleLog && low > 0 && high > 0 && value > 0 {
		fraction = (math.Log10(high) - math.Log10(value)) / (math.Log10(high) - math.Log10(low))
	} else if high != low {
		fraction = (high - value) / (high - low)
	}
	return clampInt(int(math.Round(fraction*float64(height-1))), 0, height-1)
}

func seriesRole(order int) Role {
	switch order % 6 {
	case 0:
		return RoleSeries1
	case 1:
		return RoleSeries2
	case 2:
		return RoleSeries3
	case 3:
		return RoleSeries4
	case 4:
		return RoleSeries5
	default:
		return RoleSeries6
	}
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// drawBrailleSeries rasterizes one series on a 2x4 dot grid. A missing value
// breaks the line rather than connecting across an observation that never
// existed.
func drawBrailleSeries(dots [][]uint8, roles [][]Role, series Series, xPositions []int, low, high float64, scale ScaleKind, role Role) {
	previous := -1
	for index, point := range series.Points {
		if !point.Value.Present {
			previous = -1
			continue
		}
		x := xPositions[index] * 2
		y := dotPosition(point.Value.Number, low, high, scale, linePlotHeight*4)
		setDot(dots, roles, x, y, role)
		if previous >= 0 {
			previousPoint := series.Points[previous]
			prevX := xPositions[previous] * 2
			prevY := dotPosition(previousPoint.Value.Number, low, high, scale, linePlotHeight*4)
			drawDotLine(dots, roles, prevX, prevY, x, y, role)
		}
		previous = index
	}
}

func setDot(dots [][]uint8, roles [][]Role, x, y int, role Role) {
	if y < 0 || y >= len(dots)*4 || x < 0 || x >= len(dots[0])*2 {
		return
	}
	cellX := x / 2
	cellY := y / 4
	if cellY < 0 || cellY >= len(dots) || cellX < 0 || cellX >= len(dots[0]) {
		return
	}
	bit := brailleBit(x%2, y%4)
	dots[cellY][cellX] |= bit
	if roles[cellY][cellX] == "" {
		roles[cellY][cellX] = role
	}
}

// brailleBit maps a dot's sub-cell column and row to the Unicode braille bit.
func brailleBit(subX, subY int) uint8 {
	if subX == 0 {
		switch subY {
		case 0:
			return 0x01
		case 1:
			return 0x02
		case 2:
			return 0x04
		default:
			return 0x40
		}
	}
	switch subY {
	case 0:
		return 0x08
	case 1:
		return 0x10
	case 2:
		return 0x20
	default:
		return 0x80
	}
}

// drawDotLine is Bresenham on the dot grid.
func drawDotLine(dots [][]uint8, roles [][]Role, x0, y0, x1, y1 int, role Role) {
	dx := absInt(x1 - x0)
	dy := -absInt(y1 - y0)
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		setDot(dots, roles, x0, y0, role)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func brailleLine(bits []uint8, roles []Role) []Span {
	spans := make([]Span, 0, len(bits))
	run := strings.Builder{}
	role := Role("")
	flush := func() {
		if run.Len() > 0 {
			spans = append(spans, Span{Text: run.String(), Role: role})
			run.Reset()
		}
	}
	for index, bit := range bits {
		nextRole := roles[index]
		if bit == 0 {
			nextRole = ""
		}
		if nextRole != role {
			flush()
			role = nextRole
		}
		if bit == 0 {
			run.WriteRune(' ')
		} else {
			run.WriteRune(rune(0x2800 + int(bit)))
		}
	}
	flush()
	return spans
}

func newCellGrid(height, width int) [][]plotCell {
	grid := make([][]plotCell, height)
	for row := range grid {
		grid[row] = make([]plotCell, width)
		for col := range grid[row] {
			grid[row][col] = plotCell{char: ' '}
		}
	}
	return grid
}

// drawCellSeries is the no-Braille and multi-series fallback. It draws a dot
// line and leaves the exact values to the table; no value is rounded into a
// bar. Endpoint digits are overlaid separately so series identity survives
// monochrome.
func drawCellSeries(cells [][]plotCell, series Series, xPositions []int, low, high float64, scale ScaleKind, role Role, marker rune) {
	previous := -1
	for index, point := range series.Points {
		if !point.Value.Present {
			previous = -1
			continue
		}
		row := valueRow(point.Value.Number, low, high, scale, len(cells))
		col := xPositions[index]
		if previous >= 0 {
			prevRow := valueRow(series.Points[previous].Value.Number, low, high, scale, len(cells))
			prevCol := xPositions[previous]
			drawCellLine(cells, prevRow, prevCol, row, col, role)
		}
		setCell(cells, row, col, marker, role)
		previous = index
	}
}

func setCell(cells [][]plotCell, row, col int, char rune, role Role) {
	if row < 0 || row >= len(cells) || col < 0 || col >= len(cells[row]) {
		return
	}
	cells[row][col] = plotCell{char: char, role: role}
}

func drawCellLine(cells [][]plotCell, row0, col0, row1, col1 int, role Role) {
	dx := absInt(col1 - col0)
	dy := -absInt(row1 - row0)
	sx := -1
	if col0 < col1 {
		sx = 1
	}
	sy := -1
	if row0 < row1 {
		sy = 1
	}
	err := dx + dy
	for {
		if cells[row0][col0].char == ' ' {
			cells[row0][col0] = plotCell{char: '·', role: role}
		}
		if row0 == row1 && col0 == col1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			col0 += sx
		}
		if e2 <= dx {
			err += dx
			row0 += sy
		}
	}
}

func cellLine(cells []plotCell) []Span {
	spans := make([]Span, 0, len(cells))
	run := strings.Builder{}
	role := Role("")
	flush := func() {
		if run.Len() > 0 {
			spans = append(spans, Span{Text: run.String(), Role: role})
			run.Reset()
		}
	}
	for _, cell := range cells {
		if cell.role != role {
			flush()
			role = cell.role
		}
		run.WriteRune(cell.char)
	}
	flush()
	return spans
}

// labelPositions selects the x labels that fit without overlap.
func labelPositions(positions []int, labels []string, plotWidth int) []int {
	maxWidth := 0
	for _, label := range labels {
		if width := CellWidth(label); width > maxWidth {
			maxWidth = width
		}
	}
	if maxWidth == 0 {
		maxWidth = 1
	}
	maxLabels := plotWidth / (maxWidth + 2)
	if maxLabels < 2 {
		maxLabels = 2
	}
	if maxLabels > len(positions) {
		maxLabels = len(positions)
	}
	selected := make([]int, 0, maxLabels)
	if maxLabels == 1 {
		selected = append(selected, 0)
		return selected
	}
	for i := range maxLabels {
		index := 0
		if maxLabels > 1 {
			index = int(math.Round(float64(i) * float64(len(positions)-1) / float64(maxLabels-1)))
		}
		if len(selected) == 0 || index != selected[len(selected)-1] {
			selected = append(selected, index)
		}
	}
	return selected
}

// xAxisLabels places each selected label on its own row under the plot. A
// label that would collide with the previous one is omitted, never wrapped
// into the plot geometry.
func xAxisLabels(positions []int, labels []string, plotWidth, labelWidth int) []Line {
	selected := labelPositions(positions, labels, plotWidth)
	var lines []Line
	spans := make([]Span, 0, len(selected)*2)
	current := 0
	prefixWidth := labelWidth + 1
	for _, index := range selected {
		label := labels[index]
		labelWidthNow := CellWidth(label)
		desired := prefixWidth + positions[index] - labelWidthNow/2
		if desired+labelWidthNow > prefixWidth+plotWidth {
			desired = prefixWidth + plotWidth - labelWidthNow
		}
		if desired < prefixWidth {
			desired = prefixWidth
		}
		if desired <= current || desired >= prefixWidth+plotWidth {
			continue
		}
		available := prefixWidth + plotWidth - desired
		if available < 1 {
			continue
		}
		label = fit(label, available)
		if label == "" {
			continue
		}
		if desired > current {
			spans = append(spans, Span{Text: strings.Repeat(" ", desired-current)})
			current = desired
		}
		spans = append(spans, Span{Text: label, Role: RoleAxis})
		current += CellWidth(label)
	}
	if len(spans) == 0 {
		return nil
	}
	lines = append(lines, Line{Spans: spans})
	return lines
}

// legendLine names every series and repeats the endpoint digit. Color is an
// aid; the digit is the identity that survives reduced color.
func legendLine(series []Series, width int) Line {
	spans := make([]Span, 0, len(series)*3)
	used := 0
	for order, s := range series {
		entry := strconv.Itoa(order+1) + " " + seriesName(s)
		entryWidth := CellWidth(entry)
		if used > 0 {
			entry = "   " + entry
			entryWidth += 3
		}
		if used+entryWidth > width && used > 0 {
			break
		}
		spans = append(spans, Span{Text: entry, Role: seriesRole(order)})
		used += entryWidth
	}
	return Line{Spans: spans}
}

// tableRows builds the fallback rows for a line plot too narrow to draw.
func tableRows(series []Series) [][]gridCell {
	rows := make([][]gridCell, 0, len(series[0].Points))
	for index := range series[0].Points {
		label := series[0].Points[index].Label
		if label == "" {
			label = strconv.Itoa(index + 1)
		}
		row := []gridCell{{text: label, role: RoleText}}
		for _, s := range series {
			row = append(row, gridCell{text: FormatValue(s.Points[index].Value, s.Unit), role: valueRole(s.Points[index].Value)})
		}
		rows = append(rows, row)
	}
	return rows
}
