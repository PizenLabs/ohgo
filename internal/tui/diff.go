package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Inline file-edit diff. A completed edit is chrome, not conversation prose:
// the user needs to see what changed at a glance without the model
// re-narrating it. This file renders that panel — a header naming the file, an
// optional ⎿ summary, and the changed rows in a framed container.
//
// The palette is truecolor hex, like the chrome tokens in style.go, because
// the panel carries its own dark identity rather than the transcript's
// 256-colour body tokens. Every role is a named token resolved once per
// render, so the render loop never reaches for a raw colour.

// Inline diff design tokens.
const (
	// diffMint is the header glyph and the added-row mark.
	diffMint = "#4EA96F"
	// diffRemove is the deleted-row mark.
	diffRemove = "#E06C75"
	// diffPath is the edited file path.
	diffPath = "#7DCFFF"
	// diffLabel is the bold "Update" label.
	diffLabel = "#E6E6E6"
	// diffPanelBG is the hunk container background.
	diffPanelBG = "#101010"
	// diffPanelEdge is the hunk container border.
	diffPanelEdge = "#202022"
	// diffAddBG is the added-row tint.
	diffAddBG = "#16281E"
	// diffDelBG is the deleted-row tint.
	diffDelBG = "#2A191D"
	// diffAddFg is the added-row text: bright against the tint.
	diffAddFg = "#D7E8DC"
	// diffDelFg is the deleted-row text: muted against the tint.
	diffDelFg = "#C7A6AB"
	// diffCtxFg is the context-row text: dimmed so changes read first.
	diffCtxFg = "#6B6B6B"
	// diffGutterFg is the line-number gutter.
	diffGutterFg = "#5A5A5A"
)

// DiffLineKind names the role of one row in an inline file-edit diff.
type DiffLineKind string

// Diff row roles, the values a DiffLine carries in its Type field.
const (
	// DiffAdd is a line the edit introduced.
	DiffAdd DiffLineKind = "add"
	// DiffDel is a line the edit removed.
	DiffDel DiffLineKind = "del"
	// DiffCtx is an unchanged line kept for orientation.
	DiffCtx DiffLineKind = "ctx"
)

// DiffLine is one row of an inline file-edit diff.
type DiffLine struct {
	// Type is the row's role: add, del, or ctx.
	Type DiffLineKind
	// N is the 1-based line number shown in the gutter. A non-positive N
	// renders a blank gutter, for rows that annotate rather than belong to a
	// numbered side of the file.
	N int
	// Text is the line content, without any diff mark; the renderer owns the
	// +, -, and space marks so the stored line stays the file's own bytes.
	Text string
}

// DiffHunk is one file edit rendered inline in the transcript.
type DiffHunk struct {
	// File is the edited path shown in the header.
	File string
	// Summary, when non-empty, is the one-line outcome shown under the header
	// behind a ⎿ glyph.
	Summary string
	// Lines are the changed rows, in file order.
	Lines []DiffLine
}

// diffBorderRounded is the corner set for the hunk container.
var diffBorderRounded = lipgloss.RoundedBorder()

// diffRowStyles carries the styles for one row kind, all sharing one
// background so the tint reads as a single stripe across the row.
type diffRowStyles struct {
	mark   lipgloss.Style
	text   lipgloss.Style
	gutter lipgloss.Style
	space  lipgloss.Style
}

// diffStyles resolves every style the panel uses for one render.
type diffStyles struct {
	glyph lipgloss.Style
	label lipgloss.Style
	path  lipgloss.Style
	dim   lipgloss.Style
	edge  lipgloss.Style
	add   diffRowStyles
	del   diffRowStyles
	ctx   diffRowStyles
}

// newDiffStyles builds the panel styles. A nil renderer uses the default one,
// so a caller renders through the ambient colour profile; tests pass an
// explicit forced-profile renderer to pin the design tokens.
func newDiffStyles(r *lipgloss.Renderer) diffStyles {
	mk := func() lipgloss.Style {
		if r == nil {
			return lipgloss.NewStyle()
		}
		return r.NewStyle()
	}
	row := func(bg, markFg, textFg string, boldMark bool) diffRowStyles {
		b := lipgloss.Color(bg)
		mark := mk().Foreground(lipgloss.Color(markFg)).Background(b)
		if boldMark {
			mark = mark.Bold(true)
		}
		return diffRowStyles{
			mark:   mark,
			text:   mk().Foreground(lipgloss.Color(textFg)).Background(b),
			gutter: mk().Foreground(lipgloss.Color(diffGutterFg)).Background(b),
			space:  mk().Background(b),
		}
	}
	return diffStyles{
		glyph: mk().Foreground(lipgloss.Color(diffMint)).Bold(true),
		label: mk().Foreground(lipgloss.Color(diffLabel)).Bold(true),
		path:  mk().Foreground(lipgloss.Color(diffPath)),
		dim:   mk().Foreground(lipgloss.Color(ColorMuted)),
		edge:  mk().Foreground(lipgloss.Color(diffPanelEdge)).Background(lipgloss.Color(diffPanelBG)),
		// Context rows resolve to the panel background: "transparent" against
		// the container, so only changed rows carry a tint.
		add: row(diffAddBG, diffMint, diffAddFg, true),
		del: row(diffDelBG, diffRemove, diffDelFg, true),
		ctx: row(diffPanelBG, diffGutterFg, diffCtxFg, false),
	}
}

// RenderDiffHunk renders an inline file-edit diff to a string.
//
// width bounds the panel in terminal cells, border included; a non-positive
// width renders at the natural content width, for tests and callers without a
// known viewport. The file path, summary, and every row arrive from the edit
// engine and are untrusted: they are sanitized inline and their tabs expanded
// before measuring, so the terminal lays out exactly the cells the box was
// sized for. An overlong row truncates rather than wraps — a wrapped row would
// break the line-number gutter alignment and the box edges with it.
func RenderDiffHunk(h DiffHunk, width int) string {
	return renderDiffHunk(newDiffStyles(nil), h, width)
}

// renderDiffHunk is the renderer seam: styles are injected so a test can pin
// the tokens against a forced-profile renderer.
func renderDiffHunk(s diffStyles, h DiffHunk, width int) string {
	var sb strings.Builder

	// Header: ⏺ Update (<file>). The path is budgeted against the fixed
	// chrome around it, so a long path truncates instead of pushing the row
	// past the panel width.
	file := sanitizeInline(h.File)
	if width > 0 {
		budget := width - CellWidth("⏺ Update ()")
		if budget < 1 {
			budget = 1
		}
		file = TruncateToWidth(file, budget)
	}
	sb.WriteString(s.glyph.Render("⏺"))
	sb.WriteString(" ")
	sb.WriteString(s.label.Render("Update"))
	sb.WriteString(" ")
	sb.WriteString(s.dim.Render("("))
	sb.WriteString(s.path.Render(file))
	sb.WriteString(s.dim.Render(")"))
	sb.WriteString("\n")

	// Summary: the outcome line, indented and marked with ⎿.
	if summary := strings.TrimSpace(sanitizeInline(h.Summary)); summary != "" {
		if width > 0 {
			budget := width - CellWidth("  ⎿ ")
			if budget < 1 {
				budget = 1
			}
			summary = TruncateToWidth(summary, budget)
		}
		sb.WriteString("  ")
		sb.WriteString(s.dim.Render("⎿"))
		sb.WriteString(" ")
		sb.WriteString(s.dim.Render(summary))
		sb.WriteString("\n")
	}

	if len(h.Lines) == 0 {
		return sb.String()
	}

	// The gutter is as wide as the largest line number, floored at two so a
	// single-digit file still leaves the number off the border.
	gutterWidth := diffGutterWidth(h.Lines)
	// leadWidth is every cell before the row text: " " + gutter + " " + mark + " ".
	leadWidth := gutterWidth + 4

	texts := make([]string, len(h.Lines))
	maxText := 0
	for i, line := range h.Lines {
		t := expandTabs(sanitizeInline(line.Text))
		texts[i] = t
		if w := CellWidth(t); w > maxText {
			maxText = w
		}
	}

	// innerWidth is the interior between the two border columns: the lead
	// cells, the text, and a one-cell trailing gap before the right border.
	// With a known viewport it is the budget minus the borders; without one it
	// is the natural content width. Never narrower than the lead plus one text
	// cell and the gap, so a row always has somewhere to put its content.
	innerWidth := leadWidth + maxText + 1
	if width > 0 {
		innerWidth = width - 2
		if innerWidth < leadWidth+2 {
			innerWidth = leadWidth + 2
		}
	}
	textBudget := innerWidth - leadWidth - 1

	edge := s.edge
	sb.WriteString(edge.Render(diffBorderRounded.TopLeft + strings.Repeat(diffBorderRounded.Top, innerWidth) + diffBorderRounded.TopRight))
	sb.WriteString("\n")
	for i, line := range h.Lines {
		rows, mark := s.ctx, " "
		switch line.Type {
		case DiffAdd:
			rows, mark = s.add, "+"
		case DiffDel:
			rows, mark = s.del, "-"
		}
		shown := TruncateToWidth(texts[i], textBudget)
		// The pad carries the reserved trailing gap too: textBudget already
		// leaves one cell free, so pad is always at least one and the row
		// fills the interior exactly.
		pad := innerWidth - leadWidth - CellWidth(shown)
		if pad < 0 {
			pad = 0
		}
		sb.WriteString(edge.Render(diffBorderRounded.Left))
		sb.WriteString(rows.space.Render(" "))
		sb.WriteString(rows.gutter.Render(diffGutter(gutterWidth, line.N)))
		sb.WriteString(rows.space.Render(" "))
		sb.WriteString(rows.mark.Render(mark))
		sb.WriteString(rows.space.Render(" "))
		sb.WriteString(rows.text.Render(shown))
		sb.WriteString(rows.space.Render(strings.Repeat(" ", pad)))
		sb.WriteString(edge.Render(diffBorderRounded.Right))
		sb.WriteString("\n")
	}
	sb.WriteString(edge.Render(diffBorderRounded.BottomLeft + strings.Repeat(diffBorderRounded.Bottom, innerWidth) + diffBorderRounded.BottomRight))
	sb.WriteString("\n")
	return sb.String()
}

// diffGutterWidth is the line-number column width: the largest number's digit
// count, floored at two. ASCII digits are one cell each, so byte length is the
// column width.
func diffGutterWidth(lines []DiffLine) int {
	width := 2
	for _, line := range lines {
		if line.N <= 0 {
			continue
		}
		if n := len(strconv.Itoa(line.N)); n > width {
			width = n
		}
	}
	return width
}

// diffGutter right-aligns a line number in its column. A non-positive number
// renders blank, keeping the column width stable across annotating rows.
func diffGutter(width, n int) string {
	if n <= 0 {
		return strings.Repeat(" ", width)
	}
	number := strconv.Itoa(n)
	if len(number) >= width {
		return number
	}
	return strings.Repeat(" ", width-len(number)) + number
}
