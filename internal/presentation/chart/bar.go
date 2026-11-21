package chart

import (
	"math"
	"strings"
)

// renderBarPanel draws one horizontal comparison. The validated panel owns
// exactly one series, so there is no ambiguous grouped-bar projection.
func renderBarPanel(spec ChartSpec, panel Panel) []Line {
	lines := chartPrelude(spec, panel)
	if len(panel.Series) == 0 {
		return lines
	}
	return append(lines, renderBarSeries(spec, spec.Series[panel.Series[0]])...)
}

func renderBarSeries(spec ChartSpec, series Series) []Line {
	width := spec.Width
	if width <= 0 {
		return nil
	}
	labels := make([]string, len(series.Points))
	values := make([]string, len(series.Points))
	labelWidth := 0
	valueWidth := 0
	maxAbs := 0.0
	hasNegative := false
	for index, point := range series.Points {
		label := point.Label
		if label == "" {
			label = "item"
		}
		labels[index] = label
		values[index] = FormatValue(point.Value, series.Unit)
		if point.Note != "" && point.Value.Present {
			values[index] += " (" + point.Note + ")"
		}
		if w := CellWidth(label); w > labelWidth {
			labelWidth = w
		}
		if w := CellWidth(values[index]); w > valueWidth {
			valueWidth = w
		}
		if point.Value.Present {
			abs := math.Abs(point.Value.Number)
			if abs > maxAbs {
				maxAbs = abs
			}
			if point.Value.Number < 0 {
				hasNegative = true
			}
		}
	}
	if len(labels) == 0 {
		return nil
	}
	// Labels shrink first; values never do. If even the label minimum cannot
	// leave a bar, use the exact stacked form instead of dropping the bar
	// silently.
	maxLabel := width / 3
	if maxLabel < 4 {
		maxLabel = 4
	}
	if labelWidth > maxLabel {
		labelWidth = maxLabel
	}
	if width <= labelWidth+valueWidth+3 {
		return renderBarStack(labels, values, width)
	}
	barWidth := width - labelWidth - valueWidth - 2
	if barWidth < 1 {
		return renderBarStack(labels, values, width)
	}
	lines := make([]Line, 0, len(labels))
	for index, point := range series.Points {
		label := padRight(fit(labels[index], labelWidth), labelWidth)
		value := padLeft(values[index], valueWidth)
		var bar string
		if !point.Value.Present || maxAbs == 0 {
			bar = strings.Repeat(" ", barWidth)
		} else if hasNegative {
			bar = negativeBar(point.Value.Number, maxAbs, barWidth)
		} else {
			bar = positiveBar(point.Value.Number, maxAbs, barWidth)
		}
		role := RoleSeries1
		if !point.Value.Present {
			role = RoleMuted
		}
		lines = append(lines, Line{Spans: []Span{
			{Text: label, Role: RoleText},
			{Text: " "},
			{Text: bar, Role: role},
			{Text: " "},
			{Text: value, Role: valueRole(point.Value)},
		}})
	}
	return lines
}

// renderBarStack is the exact fallback for a width that cannot hold a label, a
// bar, and a value. The value survives; only the bar is omitted.
func renderBarStack(labels, values []string, width int) []Line {
	var lines []Line
	for index := range labels {
		if index > 0 {
			lines = append(lines, Line{})
		}
		for _, wrapped := range wrap(labels[index], width) {
			lines = append(lines, textLine(wrapped, RoleText))
		}
		for _, wrapped := range wrap(values[index], width-2) {
			lines = append(lines, textLine("  "+wrapped, RoleText))
		}
	}
	return lines
}

// positiveBar fills from the left. Fractional cells use the Unicode block
// fractions so a short bar still encodes its measured value.
func positiveBar(value, max float64, width int) string {
	if width <= 0 || max <= 0 || value <= 0 {
		return strings.Repeat(" ", maxInt(width, 0))
	}
	return blockBar(value/max, width)
}

// negativeBar places a zero baseline and extends bars in both directions.
// One scale governs both sides so a large positive value cannot make a
// negative value look smaller than it is.
func negativeBar(value, max float64, width int) string {
	if width <= 0 {
		return ""
	}
	left := width / 2
	right := width - left - 1
	if right < 0 {
		right = 0
	}
	cells := make([]rune, width)
	for i := range cells {
		cells[i] = ' '
	}
	cells[left] = '│'
	// One scale governs both signs. The shorter side caps the scale so no
	// magnitude is silently clipped; the longer side keeps at most one empty
	// cell, which is a visible absence rather than a falsified length.
	span := minInt(left, right)
	if max > 0 && value != 0 && span > 0 {
		scale := max / float64(span)
		if value > 0 {
			bar := blockBar(value/scale/float64(span), span)
			for i, r := range []rune(bar) {
				if left+1+i < width {
					cells[left+1+i] = r
				}
			}
		} else {
			bar := blockBar(-value/scale/float64(span), span)
			runes := []rune(bar)
			for i := range runes {
				position := left - 1 - i
				if position >= 0 {
					cells[position] = runes[len(runes)-1-i]
				}
			}
		}
	}
	return string(cells)
}

// blockBar renders a 0..1 fraction in eighths. Rounding is deterministic and
// never produces a negative or overlong bar.
func blockBar(fraction float64, width int) string {
	if width <= 0 {
		return ""
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	eighths := int(math.Round(fraction * float64(width) * 8))
	full := eighths / 8
	partial := eighths % 8
	if full > width {
		full = width
		partial = 0
	}
	if full == width {
		partial = 0
	}
	blocks := []rune("▏▎▍▌▋▊▉")
	var b strings.Builder
	b.Grow(width * 3)
	b.WriteString(strings.Repeat("█", full))
	if partial > 0 && full < width {
		b.WriteRune(blocks[partial-1])
	}
	used := full
	if partial > 0 {
		used++
	}
	if used < width {
		b.WriteString(strings.Repeat(" ", width-used))
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
