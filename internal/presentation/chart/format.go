package chart

import (
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// CellWidth reports the terminal cells s occupies. The chart package must
// measure exactly as the renderer paints: a wide CJK label that is counted as
// one cell would silently break every column after it.
func CellWidth(s string) int { return runewidth.StringWidth(ansi.Strip(s)) }

// FormatValue renders one measured value in its declared unit.
//
// Formatting is a deterministic function of the number and the unit. It never
// changes the unit family: milliseconds may print as milliseconds or seconds,
// but a duration never becomes a token count.
func FormatValue(value Value, unit Unit) string {
	if !value.Present {
		return "—"
	}
	return formatNumber(value.Number, unit)
}

// FormatSigned renders a delta with an explicit positive sign. It is used for
// change tables, where the sign is the point of the row.
func FormatSigned(number float64, unit Unit) string {
	text := formatNumber(number, unit)
	if number > 0 {
		return "+" + text
	}
	return text
}

// formatNumber applies the unit's presentation rules.
func formatNumber(number float64, unit Unit) string {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "—"
	}
	switch unit {
	case UnitMilliseconds:
		return formatDuration(number, "ms", "s")
	case UnitMicroseconds:
		return formatDuration(number, "µs", "ms")
	case UnitSeconds:
		return trimFloat(number, 3) + " s"
	case UnitBytes:
		return formatBytes(number)
	case UnitPercent:
		return trimFloat(number, 1) + "%"
	case UnitRatio:
		return trimFloat(number, 3)
	case UnitUSD:
		return formatUSD(number)
	case UnitCount, UnitFiles, UnitTokens:
		if isIntegral(number) {
			return groupThousands(strconv.FormatFloat(number, 'f', 0, 64))
		}
		return groupThousands(trimFloat(number, 2))
	default:
		return groupThousands(trimFloat(number, 2))
	}
}

// formatDuration converts within one duration family. A 1290 ms measurement
// prints as "1.29 s"; a 0.43 ms measurement stays in milliseconds so its
// precision is not lost to a unit that is too coarse.
func formatDuration(number float64, base, next string) string {
	abs := math.Abs(number)
	if abs >= 1000 {
		return trimFloat(number/1000, 3) + " " + next
	}
	// Keep sub-10 values at three decimals: a benchmark row such as 3.944 ms
	// must not be rounded into 3.94 ms when the measurement carried more.
	decimals := 2
	if abs < 10 && abs > 0 {
		decimals = 3
	}
	if abs > 0 && abs < 0.01 {
		decimals = 5
	}
	return trimFloat(number, decimals) + " " + base
}

// formatBytes converts binary magnitudes. It is exact about the chosen
// family but intentionally does not print four representations at once.
func formatBytes(number float64) string {
	abs := math.Abs(number)
	switch {
	case abs >= 1<<40:
		return trimFloat(number/(1<<40), 2) + " TiB"
	case abs >= 1<<30:
		return trimFloat(number/(1<<30), 2) + " GiB"
	case abs >= 1<<20:
		return trimFloat(number/(1<<20), 2) + " MiB"
	case abs >= 1<<10:
		return trimFloat(number/(1<<10), 2) + " KiB"
	default:
		return trimFloat(number, 0) + " B"
	}
}

// formatUSD keeps sub-dollar values precise enough to be useful without
// turning every row into a ledger dump.
func formatUSD(number float64) string {
	abs := math.Abs(number)
	if abs < 1 {
		return "$" + trimFloat(number, 4)
	}
	return "$" + groupThousands(trimFloat(number, 2))
}

// trimFloat formats with at most decimals, removing trailing zeros. A value
// that is integral keeps no decimal point.
func trimFloat(number float64, decimals int) string {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "—"
	}
	if number == 0 {
		return "0"
	}
	text := strconv.FormatFloat(number, 'f', decimals, 64)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(text, "0")
		text = strings.TrimRight(text, ".")
	}
	if text == "-0" {
		return "0"
	}
	return text
}

func isIntegral(number float64) bool {
	return math.Abs(number) < 1e15 && number == math.Trunc(number)
}

// groupThousands inserts comma separators into the integer part of a decimal
// string. It handles a leading sign and a fractional part without parsing the
// number again, so the printed digits are exactly the formatted digits.
func groupThousands(text string) string {
	if text == "" || text == "—" {
		return text
	}
	sign := ""
	if text[0] == '-' || text[0] == '+' {
		sign, text = text[:1], text[1:]
	}
	whole, fraction, hasFraction := strings.Cut(text, ".")
	if len(whole) <= 3 {
		if hasFraction {
			return sign + whole + "." + fraction
		}
		return sign + whole
	}
	var b strings.Builder
	b.Grow(len(sign) + len(whole) + len(whole)/3 + len(fraction) + 1)
	b.WriteString(sign)
	first := len(whole) % 3
	if first == 0 {
		first = 3
	}
	b.WriteString(whole[:first])
	for i := first; i < len(whole); i += 3 {
		b.WriteByte(',')
		b.WriteString(whole[i : i+3])
	}
	if hasFraction {
		b.WriteByte('.')
		b.WriteString(fraction)
	}
	return b.String()
}

// TurnsNote renders the measured-turn caveat shared by runtime telemetry
// charts. It is a presentation phrase, not a telemetry count: the caller
// supplies the measured value.
func TurnsNote(turns int) string {
	if turns == 1 {
		return "1 measured turn"
	}
	return strconv.Itoa(turns) + " measured turns"
}

// UnitLabel is the human-readable unit name used in headers and legends.
func UnitLabel(unit Unit) string {
	switch unit {
	case UnitCount:
		return "count"
	case UnitFiles:
		return "files"
	case UnitTokens:
		return "tokens"
	case UnitBytes:
		return "bytes"
	case UnitMilliseconds:
		return "ms"
	case UnitMicroseconds:
		return "µs"
	case UnitSeconds:
		return "s"
	case UnitPercent:
		return "%"
	case UnitRatio:
		return "ratio"
	case UnitUSD:
		return "USD"
	default:
		return ""
	}
}

// seriesName is the display name of one series. The aggregation is preserved
// even when the series name already mentions it: two different aggregations
// of the same metric must not collapse into one label.
func seriesName(series Series) string {
	name := sanitizeLabel(series.Name)
	if name == "" {
		name = "value"
	}
	if series.Aggregation != AggNone && series.Aggregation != AggRaw {
		agg := string(series.Aggregation)
		if !strings.HasSuffix(strings.ToLower(name), agg) {
			name += " " + agg
		}
	}
	return name
}

// seriesTitle is the legend/panel form: name plus unit.
func seriesTitle(series Series) string {
	name := seriesName(series)
	if label := UnitLabel(series.Unit); label != "" && !mentionsUnit(name, label) {
		name += " (" + label + ")"
	}
	return name
}

// tableHeader is the table header form. Count-like units are named in the
// header because their cells are bare numbers; duration/percent/currency
// cells carry their own suffix so the header stays quiet.
func tableHeader(series Series) string {
	name := seriesName(series)
	switch series.Unit {
	case UnitCount, UnitFiles, UnitTokens, UnitRatio:
		if label := UnitLabel(series.Unit); label != "" && !mentionsUnit(name, label) {
			name += " (" + label + ")"
		}
	}
	return name
}

// mentionsUnit reports whether a series name already names its unit as a
// token. It prevents "tokens (tokens)" without the false match a naive
// substring test would produce for a one-letter unit such as "s".
func mentionsUnit(name, label string) bool {
	label = strings.ToLower(label)
	if label == "" {
		return true
	}
	tokens := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		switch r {
		case ' ', '(', ')', '·', '/', ',', ':', ';':
			return true
		}
		return false
	})
	for _, token := range tokens {
		if token == label {
			return true
		}
	}
	return false
}

// sanitizeLabel strips terminal control sequences and control characters from
// text that may have crossed a tool, benchmark, or provider boundary. The
// chart renderer never emits caller-supplied escape bytes.
func sanitizeLabel(text string) string {
	text = ansi.Strip(text)
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// fit truncates text to width cells, appending one ellipsis cell when cut.
func fit(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = sanitizeLabel(text)
	if CellWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return runewidth.Truncate(text, width-1, "…")
}

// fitExact truncates without an ellipsis. It is used for padding cells where
// an ellipsis would be mistaken for data.
func fitExact(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = sanitizeLabel(text)
	if CellWidth(text) <= width {
		return text
	}
	return runewidth.Truncate(text, width, "")
}

func padRight(text string, width int) string {
	text = fitExact(text, width)
	if gap := width - CellWidth(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

func padLeft(text string, width int) string {
	text = fitExact(text, width)
	if gap := width - CellWidth(text); gap > 0 {
		return strings.Repeat(" ", gap) + text
	}
	return text
}

// wrap folds text at word boundaries and hard-slices an unbroken run that is
// wider than the budget. Empty input yields one empty line so callers can keep
// their row accounting simple.
func wrap(text string, width int) []string {
	text = sanitizeLabel(text)
	if width <= 0 {
		return []string{""}
	}
	if text == "" {
		return []string{""}
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := ""
		curWidth := 0
		flush := func() {
			if cur != "" {
				out = append(out, cur)
				cur, curWidth = "", 0
			}
		}
		for _, word := range words {
			wordWidth := CellWidth(word)
			if wordWidth > width {
				flush()
				out = append(out, hardSlice(word, width)...)
				continue
			}
			if cur == "" {
				cur, curWidth = word, wordWidth
				continue
			}
			if curWidth+1+wordWidth <= width {
				cur += " " + word
				curWidth += 1 + wordWidth
				continue
			}
			flush()
			cur, curWidth = word, wordWidth
		}
		flush()
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// hardSlice cuts a string into width-cell chunks without inserting content.
func hardSlice(text string, width int) []string {
	var out []string
	runes := []rune(text)
	start := 0
	for start < len(runes) {
		used := 0
		end := start
		for end < len(runes) {
			rw := runewidth.RuneWidth(runes[end])
			if rw > 0 && used+rw > width {
				break
			}
			used += rw
			end++
		}
		if end == start {
			end++
		}
		out = append(out, string(runes[start:end]))
		start = end
	}
	return out
}

// axisText formats a value for an axis tick. It uses the same formatter as a
// table cell so the two representations can never disagree.
func axisText(number float64, unit Unit) string {
	return formatNumber(number, unit)
}

// provenanceText renders a bounded provenance line. Evidence coordinates are
// copied, never upgraded: a stale observation stays visibly stale.
func provenanceText(p Provenance) string {
	var parts []string
	switch p.Kind {
	case ProvenanceRuntime:
		parts = append(parts, "source: runtime telemetry")
	case ProvenanceBenchmark:
		parts = append(parts, "source: benchmark")
	case ProvenanceEvidence:
		parts = append(parts, "source: evidence")
	case ProvenanceFile:
		parts = append(parts, "source: file")
	default:
		if p.Kind != "" {
			parts = append(parts, "source: "+string(p.Kind))
		}
	}
	if p.ID != "" {
		parts = append(parts, p.ID)
	}
	if p.State != "" {
		state := p.State
		if p.Freshness != "" {
			state += ", " + p.Freshness
		}
		parts = append(parts, state)
	} else if p.Freshness != "" {
		parts = append(parts, p.Freshness)
	}
	if p.Ref != "" {
		parts = append(parts, p.Ref)
	}
	if p.Note != "" {
		parts = append(parts, p.Note)
	}
	return strings.Join(parts, " · ")
}
