package chart

import (
	"fmt"
	"strings"
)

// Role is a semantic style request. The chart package never emits an ANSI
// escape sequence: the caller maps roles to its product palette, which keeps
// one style system authoritative and lets a monochrome terminal ignore roles
// without losing the text.
type Role string

// Semantic roles.
const (
	RoleText       Role = ""
	RoleTitle      Role = "title"
	RolePanelTitle Role = "panel_title"
	RoleHeader     Role = "header"
	RoleRule       Role = "rule"
	RoleMuted      Role = "muted"
	RoleFaint      Role = "faint"
	RoleEmphasis   Role = "emphasis"
	RoleWarning    Role = "warning"
	RoleError      Role = "error"
	RoleSuccess    Role = "success"
	RoleAxis       Role = "axis"
	RoleSeries1    Role = "series1"
	RoleSeries2    Role = "series2"
	RoleSeries3    Role = "series3"
	RoleSeries4    Role = "series4"
	RoleSeries5    Role = "series5"
	RoleSeries6    Role = "series6"
)

// Span is one run of text with one semantic role.
type Span struct {
	Text string `json:"text"`
	Role Role   `json:"role,omitempty"`
}

// Line is one rendered row.
type Line struct {
	Spans []Span `json:"spans"`
}

// Text joins the spans without styling. It is the plain-terminal form and the
// copy payload; it never contains escape sequences.
func (l Line) Text() string {
	var b strings.Builder
	for _, span := range l.Spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

// RenderOptions selects optional presentation capabilities.
type RenderOptions struct {
	// NoBraille forces the cell-resolution fallback. The data is unchanged.
	NoBraille bool
}

// Render is the canonical renderer entry point.
func Render(spec ChartSpec) ([]Line, error) {
	return RenderWith(spec, RenderOptions{})
}

// RenderWith renders one validated spec. It is deterministic for a given spec:
// no clock, no randomness, and no map iteration affects a row.
func RenderWith(spec ChartSpec, opts RenderOptions) ([]Line, error) {
	if err := ValidateSpec(spec); err != nil {
		return nil, err
	}
	var lines []Line
	if spec.Title != "" {
		for _, title := range wrap(spec.Title, spec.Width) {
			lines = append(lines, textLine(title, RoleTitle))
		}
		lines = append(lines, Line{})
	}
	if spec.IncludeTable {
		lines = append(lines, renderTable(spec)...)
		lines = append(lines, Line{})
	}
	if spec.Kind == KindChange && !spec.IncludeTable {
		lines = append(lines, renderChange(spec)...)
		lines = append(lines, Line{})
	}
	if spec.Finding != "" {
		lines = appendFinding(lines, spec.Finding, spec.Width)
	}
	switch spec.Kind {
	case KindBar:
		lines = append(lines, renderBarPanel(spec, spec.Panels[0])...)
	case KindLine:
		lines = append(lines, renderLinePanel(spec, spec.Panels[0], opts)...)
	case KindPanels:
		for index, panel := range spec.Panels {
			if index > 0 {
				lines = append(lines, Line{})
			}
			for _, title := range wrap(panel.Title, spec.Width) {
				lines = append(lines, textLine(title, RolePanelTitle))
			}
			if panel.Kind == KindBar {
				lines = append(lines, renderBarPanel(spec, panel)...)
			} else {
				lines = append(lines, renderLinePanel(spec, panel, opts)...)
			}
		}
	case KindTable, KindChange:
		// The table (or change grid) above is the complete representation.
	}
	if len(spec.Notes) > 0 {
		lines = ensureBlank(lines)
		for _, note := range spec.Notes {
			for _, wrapped := range wrap("note: "+note, spec.Width) {
				lines = append(lines, textLine(wrapped, RoleMuted))
			}
		}
	}
	if text := provenanceText(spec.Provenance); text != "" {
		lines = ensureBlank(lines)
		for _, wrapped := range wrap(text, spec.Width) {
			lines = append(lines, textLine(wrapped, RoleFaint))
		}
	}
	return trimTrailingBlank(lines), nil
}

// RenderText is the plain-terminal form, used by benchmarks and copy paths.
func RenderText(spec ChartSpec) (string, error) {
	lines, err := Render(spec)
	if err != nil {
		return "", err
	}
	return LinesText(lines), nil
}

// LinesText joins rendered lines with newlines.
func LinesText(lines []Line) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line.Text())
	}
	return b.String()
}

// ExactText returns the authoritative table for a PlanInput at a width large
// enough that no label or value is truncated. It is the clipboard and
// machine-facing representation; a chart is never the only copy of the data.
func ExactText(input PlanInput) (string, error) {
	spec, _, err := Build(input, 4096)
	if err != nil {
		return "", err
	}
	lines := renderTable(spec)
	if spec.Kind == KindChange {
		lines = renderChange(spec)
	}
	return LinesText(trimTrailingBlank(lines)), nil
}

func textLine(text string, role Role) Line {
	return Line{Spans: []Span{{Text: text, Role: role}}}
}

func appendFinding(lines []Line, finding string, width int) []Line {
	lines = ensureBlank(lines)
	prefix := "Key finding: "
	prefixWidth := CellWidth(prefix)
	if prefixWidth >= width {
		for _, wrapped := range wrap(finding, width) {
			lines = append(lines, textLine(wrapped, RoleEmphasis))
		}
		return lines
	}
	wrapped := wrap(finding, width-prefixWidth)
	for index, text := range wrapped {
		if index == 0 {
			lines = append(lines, Line{Spans: []Span{
				{Text: prefix, Role: RoleEmphasis},
				{Text: text, Role: RoleText},
			}})
			continue
		}
		lines = append(lines, textLine(strings.Repeat(" ", prefixWidth)+text, RoleText))
	}
	lines = append(lines, Line{})
	return lines
}

func trimTrailingBlank(lines []Line) []Line {
	for len(lines) > 0 && len(lines[len(lines)-1].Spans) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ensureBlank adds one separator row unless the output already ends in one.
// Repeated table/finding/note sections must not accumulate empty rows.
func ensureBlank(lines []Line) []Line {
	if len(lines) == 0 {
		return lines
	}
	if len(lines[len(lines)-1].Spans) == 0 {
		return lines
	}
	return append(lines, Line{})
}

// renderTable derives the exact data table from the spec. The table is not an
// optional decoration: it is the representation a consumer can trust without
// interpreting a plotted shape.
func renderTable(spec ChartSpec) []Line {
	labelHeader := spec.XLabel
	if labelHeader == "" {
		labelHeader = "item"
	}
	headers := make([]gridCell, 0, len(spec.Series)+1)
	headers = append(headers, gridCell{text: labelHeader, role: RoleHeader})
	for _, series := range spec.Series {
		headers = append(headers, gridCell{text: tableHeader(series), role: RoleHeader})
	}
	rows := make([][]gridCell, 0, len(spec.Series[0].Points))
	for index := range spec.Series[0].Points {
		label := spec.Series[0].Points[index].Label
		if label == "" {
			label = fmt.Sprintf("%d", index+1)
		}
		row := make([]gridCell, 0, len(spec.Series)+1)
		row = append(row, gridCell{text: label, role: RoleText})
		for _, series := range spec.Series {
			point := series.Points[index]
			text := FormatValue(point.Value, series.Unit)
			if point.Note != "" {
				text += " (" + point.Note + ")"
			}
			role := RoleText
			if !point.Value.Present {
				role = RoleMuted
			}
			row = append(row, gridCell{text: text, role: role})
		}
		rows = append(rows, row)
	}
	aligns := make([]gridAlign, len(headers))
	for i := range aligns {
		aligns[i] = alignRight
	}
	aligns[0] = alignLeft
	return renderGrid(headers, rows, spec.Width, aligns)
}

// renderChange derives the exact before/after grid. Direction is deliberately
// not colored as success or failure: whether lower is better is domain
// knowledge this renderer does not own.
func renderChange(spec ChartSpec) []Line {
	before := spec.Series[0]
	after := spec.Series[1]
	headers := []gridCell{
		{text: "item", role: RoleHeader},
		{text: tableHeader(before), role: RoleHeader},
		{text: tableHeader(after), role: RoleHeader},
		{text: "Δ", role: RoleHeader},
		{text: "Δ%", role: RoleHeader},
	}
	rows := make([][]gridCell, 0, len(before.Points))
	for index := range before.Points {
		label := before.Points[index].Label
		if label == "" {
			label = fmt.Sprintf("%d", index+1)
		}
		from := before.Points[index].Value
		to := after.Points[index].Value
		beforeText := FormatValue(from, before.Unit)
		if note := before.Points[index].Note; note != "" {
			beforeText += " (" + note + ")"
		}
		afterText := FormatValue(to, after.Unit)
		if note := after.Points[index].Note; note != "" {
			afterText += " (" + note + ")"
		}
		deltaText, percentText := "—", "—"
		if from.Present && to.Present {
			delta := to.Number - from.Number
			deltaText = FormatSigned(delta, before.Unit)
			if from.Number != 0 {
				percentText = FormatSigned(delta/from.Number*100, UnitPercent)
			}
		}
		rows = append(rows, []gridCell{
			{text: label, role: RoleText},
			{text: beforeText, role: valueRole(from)},
			{text: afterText, role: valueRole(to)},
			{text: deltaText, role: RoleText},
			{text: percentText, role: RoleText},
		})
	}
	return renderGrid(headers, rows, spec.Width, []gridAlign{
		alignLeft, alignRight, alignRight, alignRight, alignRight,
	})
}

func valueRole(value Value) Role {
	if !value.Present {
		return RoleMuted
	}
	return RoleText
}

// gridAlign names a column's alignment.
type gridAlign int

const (
	alignLeft gridAlign = iota
	alignRight
)

type gridCell struct {
	text string
	role Role
}

// renderGrid lays out a column table. Numeric columns never shrink: a value
// that no longer fits is moved to the stacked form instead of being silently
// truncated. Labels shrink first, and only down to a readable minimum.
func renderGrid(headers []gridCell, rows [][]gridCell, width int, aligns []gridAlign) []Line {
	if width <= 0 || len(headers) == 0 {
		return nil
	}
	if len(aligns) != len(headers) {
		aligns = make([]gridAlign, len(headers))
		for i := 1; i < len(aligns); i++ {
			aligns[i] = alignRight
		}
	}
	const gap = 2
	const minLabel = 4
	widths := make([]int, len(headers))
	for index, header := range headers {
		widths[index] = CellWidth(header.text)
	}
	for _, row := range rows {
		for index := range headers {
			if index >= len(row) {
				continue
			}
			if valueWidth := CellWidth(row[index].text); valueWidth > widths[index] {
				widths[index] = valueWidth
			}
		}
	}
	if len(headers) > 1 {
		widths[0] = max(widths[0], minLabel)
	}
	// A numeric row label is part of the data, not decoration. Truncating
	// "20,000" to "20,00" changes the order of magnitude while still looking
	// like a number, so such labels fall back to the stacked form instead of
	// shrinking.
	minFirst := minLabel
	if numericFirstColumn(rows) {
		minFirst = widths[0]
	}
	total := func() int {
		sum := gap * (len(headers) - 1)
		for _, w := range widths {
			sum += w
		}
		return sum
	}
	if total() > width {
		for len(headers) > 1 && widths[0] > minFirst && total() > width {
			widths[0]--
		}
	}
	if total() > width {
		return renderStacked(headers, rows, width)
	}
	var lines []Line
	lines = append(lines, gridRow(headers, widths, aligns, gap, RoleHeader))
	ruleWidth := total()
	if ruleWidth > width {
		ruleWidth = width
	}
	lines = append(lines, textLine(strings.Repeat("─", ruleWidth), RoleRule))
	for _, row := range rows {
		lines = append(lines, gridRow(row, widths, aligns, gap, RoleText))
	}
	return lines
}

// numericFirstColumn reports whether every row label is numeric. A numeric
// label is exact data: shrinking it changes its value, unlike a prose label.
func numericFirstColumn(rows [][]gridCell) bool {
	if len(rows) == 0 {
		return false
	}
	for _, row := range rows {
		if len(row) == 0 {
			return false
		}
		label := strings.TrimSpace(row[0].text)
		if label == "" {
			return false
		}
		for _, r := range label {
			if (r < '0' || r > '9') && r != ',' && r != '.' && r != '-' && r != '+' {
				return false
			}
		}
	}
	return true
}

func gridRow(row []gridCell, widths []int, aligns []gridAlign, gap int, defaultRole Role) Line {
	spans := make([]Span, 0, len(widths)*2)
	for index := range widths {
		if index > 0 {
			spans = append(spans, Span{Text: strings.Repeat(" ", gap)})
		}
		cell := gridCell{}
		if index < len(row) {
			cell = row[index]
		}
		role := cell.role
		if role == "" {
			role = defaultRole
		}
		var text string
		if aligns[index] == alignRight {
			text = padLeft(cell.text, widths[index])
		} else {
			text = padRight(cell.text, widths[index])
		}
		spans = append(spans, Span{Text: text, Role: role})
	}
	return Line{Spans: spans}
}

// renderStacked is the narrow fallback: one label followed by one exact
// `header: value` line per column. It preserves every value and never invents
// a column width.
func renderStacked(headers []gridCell, rows [][]gridCell, width int) []Line {
	var lines []Line
	for rowIndex, row := range rows {
		if rowIndex > 0 {
			lines = append(lines, Line{})
		}
		label := ""
		if len(row) > 0 {
			label = row[0].text
		}
		for _, wrapped := range wrap(label, width) {
			lines = append(lines, textLine(wrapped, RoleText))
		}
		for index := 1; index < len(headers); index++ {
			if index >= len(row) {
				continue
			}
			header := headers[index].text
			value := row[index].text
			prefix := "  " + header + ": "
			prefixWidth := CellWidth(prefix)
			// A short remaining budget splits a unit across rows ("0.43" /
			// "ms"). When the value cannot fit whole, put the header and the
			// exact value on separate lines instead.
			if prefixWidth >= width || width-prefixWidth < CellWidth(value) {
				for _, wrapped := range wrap(header, maxInt(width-2, 1)) {
					lines = append(lines, textLine("  "+wrapped, RoleMuted))
				}
				for _, wrapped := range wrap(value, maxInt(width-2, 1)) {
					lines = append(lines, textLine("  "+wrapped, row[index].role))
				}
				continue
			}
			wrapped := wrap(value, width-prefixWidth)
			for lineIndex, text := range wrapped {
				if lineIndex == 0 {
					lines = append(lines, Line{Spans: []Span{
						{Text: prefix, Role: RoleMuted},
						{Text: text, Role: row[index].role},
					}})
					continue
				}
				lines = append(lines, textLine(strings.Repeat(" ", CellWidth(prefix))+text, row[index].role))
			}
		}
	}
	return lines
}
