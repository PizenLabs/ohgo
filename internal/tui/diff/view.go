package diff

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// tabSpaces is the fixed tab expansion the diff body commits to.
//
// It is a fixed four spaces rather than aligned tab stops so a character span
// computed by the word diff maps to the same cells after expansion: three
// extra bytes per tab, regardless of column. Tab-stop alignment would make the
// mapping column-dependent and the rendered row could then disagree with the
// measured width.
const tabSpaces = "    "

// Horizontal chrome.
const (
	// paneDivider is the one-cell rule between the two panes.
	paneDivider = "│"
	// barCells is the width of the +/- proportion bar in the stats header.
	barCells = 5
	// minRightPane is the narrowest diff pane worth rendering. Below it the
	// sidebar takes the whole width and the viewer opens on selection.
	minRightPane = 14
)

// styles carries every resolved style the renderer uses, built once per theme
// so the hot loop never constructs a style.
type styles struct {
	title    lipgloss.Style
	meta     lipgloss.Style
	path     lipgloss.Style
	statAdd  lipgloss.Style
	statDel  lipgloss.Style
	barAdd   lipgloss.Style
	barDel   lipgloss.Style
	barEmpty lipgloss.Style
	divider  lipgloss.Style
	gutter   lipgloss.Style
	header   lipgloss.Style
	dim      lipgloss.Style
	marker   lipgloss.Style
	selected lipgloss.Style
	filter   lipgloss.Style
	cursor   lipgloss.Style
	badgeA   lipgloss.Style
	badgeM   lipgloss.Style
	badgeD   lipgloss.Style
	addText  lipgloss.Style
	delText  lipgloss.Style
	ctxText  lipgloss.Style
	addHL    lipgloss.Style
	delHL    lipgloss.Style
	addSign  lipgloss.Style
	delSign  lipgloss.Style
}

// colorSet is the hex palette a styles bundle resolves from.
type colorSet struct {
	title, meta, path, statAdd, statDel                        string
	barAdd, barDel, barEmpty, divider, gutter, header          string
	dim, marker, filter                                        string
	addBg, delBg, addHLBg, delHLBg                             string
	ctxText                                                    string
	badgeA, badgeM, badgeD, addText, delText, addSign, delSign string
}

// darkColors is the default inspector palette: muted surfaces, a mint accent,
// and enough background difference for an added/deleted row to read without
// the text losing contrast.
var darkColors = colorSet{
	title: "#E6E6E6", meta: "#6B6B6B", path: "#7DCFFF",
	statAdd: "#4EA96F", statDel: "#E06C75",
	barAdd: "#4EA96F", barDel: "#E06C75", barEmpty: "#303033",
	divider: "#303033", gutter: "#5A5A5A", header: "#7DCFFF",
	dim: "#6B6B6B", marker: "#4EA96F", filter: "#7DCFFF",
	addBg: "#12261A", delBg: "#2A1719", addHLBg: "#1F5C37", delHLBg: "#6B2B30",
	ctxText: "#8A8A8A",
	badgeA:  "#4EA96F", badgeM: "#E5C07B", badgeD: "#E06C75",
	addText: "#D7E8DC", delText: "#C7A6AB", addSign: "#4EA96F", delSign: "#E06C75",
}

// lightColors keeps the same roles on a bright screen: dark foregrounds and
// barely-there row tints instead of the dark theme's saturated backgrounds.
var lightColors = colorSet{
	title: "#1F2933", meta: "#6B7280", path: "#0B7285",
	statAdd: "#1B7F4B", statDel: "#C0392B",
	barAdd: "#1B7F4B", barDel: "#C0392B", barEmpty: "#D0D5DA",
	divider: "#D0D5DA", gutter: "#9AA0A6", header: "#0B7285",
	dim: "#6B7280", marker: "#1B7F4B", filter: "#0B7285",
	addBg: "#E7F4EC", delBg: "#FCE9E7", addHLBg: "#B7E2C8", delHLBg: "#F3C4BD",
	ctxText: "#4B5563",
	badgeA:  "#1B7F4B", badgeM: "#A16207", badgeD: "#C0392B",
	addText: "#14532D", delText: "#7F1D1D", addSign: "#1B7F4B", delSign: "#C0392B",
}

// colorSetFor resolves the hex palette for a theme.
func colorSetFor(light bool) colorSet {
	if light {
		return lightColors
	}
	return darkColors
}

// newStyles resolves a styles bundle for the palette.
func newStyles(c colorSet) styles {
	fg := func(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)) }
	row := func(fgHex, bgHex string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(fgHex)).Background(lipgloss.Color(bgHex))
	}
	rowHL := func(bgHex string) lipgloss.Style {
		return lipgloss.NewStyle().Background(lipgloss.Color(bgHex)).Bold(true)
	}
	return styles{
		title:    fg(c.title).Bold(true),
		meta:     fg(c.meta),
		path:     fg(c.path).Bold(true),
		statAdd:  fg(c.statAdd).Bold(true),
		statDel:  fg(c.statDel).Bold(true),
		barAdd:   fg(c.barAdd),
		barDel:   fg(c.barDel),
		barEmpty: fg(c.barEmpty),
		divider:  fg(c.divider),
		gutter:   fg(c.gutter),
		header:   fg(c.header).Bold(true),
		dim:      fg(c.dim),
		marker:   fg(c.marker).Bold(true),
		selected: fg(c.marker).Bold(true),
		filter:   fg(c.filter),
		cursor:   fg(c.filter).Reverse(true),
		badgeA:   fg(c.badgeA).Bold(true),
		badgeM:   fg(c.badgeM).Bold(true),
		badgeD:   fg(c.badgeD).Bold(true),
		addText:  row(c.addText, c.addBg),
		delText:  row(c.delText, c.delBg),
		ctxText:  fg(c.ctxText),
		addHL:    rowHL(c.addHLBg),
		delHL:    rowHL(c.delHLBg),
		addSign:  fg(c.addSign).Bold(true),
		delSign:  fg(c.delSign).Bold(true),
	}
}

// SetTheme switches the palette between the dark (default) and light designs.
func (m *Model) SetTheme(name string) {
	m.light = strings.EqualFold(strings.TrimSpace(name), "light")
	m.styleSet = newStyles(colorSetFor(m.light))
}

// styles resolves the active palette.
func (m *Model) styles() styles { return m.styleSet }

// View implements tea.Model.
//
// It renders only the rows inside the current terminal budget: the tree and the
// hunk list are sliced to their windows before a single cell is styled. A
// 100,000-line diff is one struct per line and a few hundred styled rows.
func (m *Model) View() string {
	s := m.styles()
	width := m.width
	if width <= 0 {
		width = 100
	}
	leftW, rightW := m.panes(width)
	bodyH := m.bodyHeight()

	var sb strings.Builder
	sb.WriteString(m.headerLine(s, width))
	sb.WriteByte('\n')

	for b := 0; b < bodyH; b++ {
		left := m.leftCell(s, b, leftW)
		if rightW <= 0 {
			sb.WriteString(padCell(left, width))
			sb.WriteByte('\n')
			continue
		}
		right := m.rightCell(s, b, rightW)
		sb.WriteString(padCell(left, leftW))
		sb.WriteString(s.divider.Render(paneDivider))
		sb.WriteString(padCell(right, rightW))
		sb.WriteByte('\n')
	}

	sb.WriteString(s.meta.Render(truncate(m.footerLine(), width)))
	return sb.String()
}

// panes splits the width into the sidebar and the diff viewport. A width too
// narrow for both gives the sidebar the whole row; the diff still opens on
// selection, it just is not previewed alongside.
func (m *Model) panes(width int) (leftW, rightW int) {
	leftW = width / 3
	if leftW < 20 {
		leftW = 20
	}
	if leftW > 44 {
		leftW = 44
	}
	rightW = width - leftW - 1
	if rightW < minRightPane {
		return width, 0
	}
	return leftW, rightW
}

// headerLine names the revision pair and the change totals.
func (m *Model) headerLine(s styles, width int) string {
	title := sanitize(m.title)
	if title == "" {
		title = "diff"
	}
	var sb strings.Builder
	sb.WriteString(s.title.Render("diff"))
	sb.WriteString(s.meta.Render(" · "))
	sb.WriteString(s.path.Render(title))
	if m.totalFiles > 0 {
		noun := "files"
		if m.totalFiles == 1 {
			noun = "file"
		}
		sb.WriteString(s.meta.Render(fmt.Sprintf("  %d %s", m.totalFiles, noun)))
	}
	if m.totalAdd > 0 || m.totalDel > 0 {
		sb.WriteString("  ")
		sb.WriteString(s.statAdd.Render("+" + strconv.Itoa(m.totalAdd)))
		sb.WriteString(" ")
		sb.WriteString(s.statDel.Render("-" + strconv.Itoa(m.totalDel)))
	}
	return truncate(sb.String(), width)
}

// footerLine lists the keys that act in the inspector.
func (m *Model) footerLine() string {
	if m.filterFocus {
		return "type to filter · enter apply · esc clear · ctrl+u clear all"
	}
	return "tab focus · j/k move · enter open · / filter · esc close"
}

// countFiles counts file (non-directory) leaves in the tree.
func countFiles(nodes []*FileNode) int {
	n := 0
	for _, node := range nodes {
		if node.IsDir {
			n += countFiles(node.Children)
			continue
		}
		n++
	}
	return n
}

// totalStats sums the added/removed line counts across the tree.
func totalStats(nodes []*FileNode) (added, removed int) {
	for _, node := range nodes {
		added += node.StatAdd
		removed += node.StatDel
	}
	return added, removed
}

// leftCell renders one row of the sidebar: the filter box on the first body
// row, then the visible tree window.
func (m *Model) leftCell(s styles, bodyRow, width int) string {
	if width <= 0 {
		return ""
	}
	if bodyRow == 0 {
		return m.filterCell(s, width)
	}
	if m.loadingFiles {
		if bodyRow == 1 {
			return s.dim.Render(truncate("loading changed files…", width))
		}
		return ""
	}
	if len(m.rows) == 0 {
		if bodyRow == 1 {
			return s.dim.Render(truncate("no changes", width))
		}
		return ""
	}

	visible := m.treeWindowSize()
	start := m.treeWindowStart(visible)
	index := start + bodyRow - 1
	if index < 0 || index >= len(m.rows) {
		return ""
	}
	return m.treeRow(s, m.rows[index], index == m.cursor, width)
}

// treeWindowSize is the rows the sidebar may show below its filter box.
func (m *Model) treeWindowSize() int {
	size := m.bodyHeight() - 1
	if size < 1 {
		size = 1
	}
	return size
}

// treeWindowStart follows the cursor so the selection is always on screen.
func (m *Model) treeWindowStart(size int) int {
	if len(m.rows) <= size {
		return 0
	}
	start := m.cursor - size + 1
	if start < 0 {
		start = 0
	}
	if start+size > len(m.rows) {
		start = len(m.rows) - size
	}
	if start < 0 {
		start = 0
	}
	return start
}

// treeRow renders one sidebar entry: selection marker, indentation, glyph or
// status badge, name, and the file's own +/- counts.
func (m *Model) treeRow(s styles, row Row, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = s.marker.Render("▸ ")
	}

	var sb strings.Builder
	sb.WriteString(marker)
	indent := strings.Repeat("  ", row.Depth)
	// The marker and indentation are fixed; the name and stats share what is
	// left so the numbers never push the name off the pane.
	lead := 2 + 2*row.Depth
	badge := ""
	badgeWidth := 2
	if row.Node.IsDir {
		if row.Node.Expanded {
			badge = "▾ "
		} else {
			badge = "▸ "
		}
	} else {
		badge = string(row.Node.Status) + " "
	}
	stats := ""
	if row.Node.StatAdd > 0 || row.Node.StatDel > 0 {
		stats = fmt.Sprintf("+%d -%d", row.Node.StatAdd, row.Node.StatDel)
	}

	name := sanitize(row.Node.Name)
	statsWidth := 0
	if stats != "" {
		statsWidth = len(stats) + 1
	}
	nameBudget := width - lead - badgeWidth - statsWidth
	if nameBudget < 1 {
		nameBudget = 1
	}

	sb.WriteString(indent)
	sb.WriteString(m.badgeStyle(s, row.Node).Render(badge))
	nameStyle := s.ctxText
	if selected {
		nameStyle = s.selected
	} else if row.Node.IsDir {
		nameStyle = s.meta
	}
	sb.WriteString(nameStyle.Render(truncate(name, nameBudget)))
	if stats != "" {
		pad := width - lead - badgeWidth - lipgloss.Width(truncate(name, nameBudget)) - statsWidth
		if pad < 1 {
			pad = 1
		}
		sb.WriteString(strings.Repeat(" ", pad))
		sb.WriteString(s.statAdd.Render("+" + strconv.Itoa(row.Node.StatAdd)))
		sb.WriteString(" ")
		sb.WriteString(s.statDel.Render("-" + strconv.Itoa(row.Node.StatDel)))
	}
	return sb.String()
}

// badgeStyle resolves the status colour: green added, amber modified, red
// deleted, and mint for a directory's expand glyph.
func (m *Model) badgeStyle(s styles, node *FileNode) lipgloss.Style {
	if node.IsDir {
		return s.marker
	}
	switch node.Status {
	case StatusAdded:
		return s.badgeA
	case StatusDeleted:
		return s.badgeD
	default:
		return s.badgeM
	}
}

// filterCell renders the sidebar's filter box.
func (m *Model) filterCell(s styles, width int) string {
	label := s.dim.Render("⌕ ")
	if m.filterFocus || m.filter != "" {
		text := sanitize(m.filter)
		caret := ""
		if m.filterFocus {
			caret = s.cursor.Render(" ")
		}
		line := label + s.filter.Render(text) + caret
		return truncate(line, width)
	}
	return truncate(label+s.dim.Render("filter (/)"), width)
}

// rightCell renders one row of the hunk pane: the selected file's stats header
// on the first body row, then the visible hunk window.
func (m *Model) rightCell(s styles, bodyRow, width int) string {
	if width <= 0 {
		return ""
	}
	if bodyRow == 0 {
		return m.statsHeader(s, width)
	}
	if len(m.renderRows) == 0 {
		if bodyRow == 1 {
			return s.dim.Render(truncate(m.diffPlaceholder(), width))
		}
		return ""
	}
	index := m.diffScroll + bodyRow - 1
	if index < 0 || index >= len(m.renderRows) {
		return ""
	}
	return m.renderViewportCell(s, m.renderRows[index], width)
}

// statsHeader shows the current file, its +/-, and the proportion bar.
func (m *Model) statsHeader(s styles, width int) string {
	if m.currentPath == "" {
		return s.meta.Render(truncate("no file selected", width))
	}
	var sb strings.Builder
	sb.WriteString(s.path.Render(sanitize(m.currentPath)))
	add, del, binary := m.currentStats()
	if binary {
		sb.WriteString(s.meta.Render("  binary"))
		return truncate(sb.String(), width)
	}
	sb.WriteString("  ")
	sb.WriteString(s.statAdd.Render("+" + strconv.Itoa(add)))
	sb.WriteString(" ")
	sb.WriteString(s.statDel.Render("-" + strconv.Itoa(del)))
	sb.WriteString("  ")
	sb.WriteString(m.statBar(s, add, del))
	return truncate(sb.String(), width)
}

// currentStats returns the displayed file's counts from the tree node, so the
// header agrees with the sidebar badge even before the hunks arrive.
func (m *Model) currentStats() (added, removed int, binary bool) {
	node := findFile(m.roots, m.currentPath)
	if node == nil {
		return 0, 0, false
	}
	return node.StatAdd, node.StatDel, node.Binary
}

// statBar draws the proportion bar: green cells for additions, red for
// deletions, dim for the remainder.
func (m *Model) statBar(s styles, added, removed int) string {
	total := added + removed
	if total == 0 {
		return s.barEmpty.Render(strings.Repeat("🟩", barCells))
	}
	add := added * barCells / total
	del := barCells - add
	if added > 0 && add == 0 {
		add = 1
		del = barCells - 1
	}
	if removed > 0 && del == 0 {
		del = 1
		add = barCells - 1
	}
	return strings.Repeat("🟩", add) + strings.Repeat("🟥", del)
}

// findFile locates a file node by path.
func findFile(nodes []*FileNode, path string) *FileNode {
	for _, node := range nodes {
		if node.Path == path && !node.IsDir {
			return node
		}
		if node.IsDir {
			if found := findFile(node.Children, path); found != nil {
				return found
			}
		}
	}
	return nil
}

// diffPlaceholder is the empty-state message for the hunk pane.
func (m *Model) diffPlaceholder() string {
	switch {
	case m.loadingFiles:
		return "loading…"
	case m.errText != "":
		return m.errText
	case len(m.roots) == 0:
		return "no changes"
	case m.focus == FocusTree && m.currentPath == "":
		return "move to a file to preview its diff"
	case m.currentPath == "":
		return "select a file"
	default:
		return "no changes in this file"
	}
}

// renderViewportCell styles one hunk-viewport row. The name is deliberately
// distinct from the renderRow type: one is a method, the other a struct, and
// they are read in the same breath.
func (m *Model) renderViewportCell(s styles, row renderRow, width int) string {
	switch row.kind {
	case rowGap:
		return s.dim.Render(truncate(fmt.Sprintf("↕ %d unchanged", row.gap), width))
	case rowChunk:
		return s.header.Render(truncate(sanitize(m.chunks[row.chunk].Header), width))
	case rowLine:
		return m.renderLine(s, row, width)
	}
	return ""
}

// renderLine renders one hunk body line: gutter numbers, sign, and content
// with the intra-line highlight spans applied.
func (m *Model) renderLine(s styles, row renderRow, width int) string {
	line := m.chunks[row.chunk].Lines[row.line]
	if line.Type == LineMeta {
		return s.dim.Render(truncate("\\ "+sanitize(line.Content), width))
	}

	gutterW := m.gutterWidth()
	var oldCol, newCol string
	switch line.Type {
	case LineContext:
		oldCol, newCol = strconv.Itoa(line.OldLineNum), strconv.Itoa(line.NewLineNum)
	case LineDeletion:
		oldCol = strconv.Itoa(line.OldLineNum)
	case LineAddition:
		newCol = strconv.Itoa(line.NewLineNum)
	}

	content := expandTabs(sanitize(line.Content))
	spans := mapSpans(line.Content, line.Highlights)

	var body string
	sign := " "
	switch line.Type {
	case LineAddition:
		body = renderSpans(content, spans, s.addText, s.addHL)
		sign = s.addSign.Render("+")
	case LineDeletion:
		body = renderSpans(content, spans, s.delText, s.delHL)
		sign = s.delSign.Render("-")
	default:
		body = s.ctxText.Render(content)
	}

	lead := fmt.Sprintf("%s %s %s ", padLeft(oldCol, gutterW), padLeft(newCol, gutterW), sign)
	budget := width - lipgloss.Width(lead)
	if budget < 1 {
		budget = 1
	}
	return lead + truncate(body, budget)
}

// gutterWidth is the line-number column width for the current file: the digits
// of the largest number, floored at three so the gutter stays aligned across
// files.
func (m *Model) gutterWidth() int {
	width := 3
	for _, chunk := range m.chunks {
		for _, line := range chunk.Lines {
			if digits(line.OldLineNum) > width {
				width = digits(line.OldLineNum)
			}
			if digits(line.NewLineNum) > width {
				width = digits(line.NewLineNum)
			}
		}
	}
	return width
}

// renderSpans applies the changed spans, styling each segment with the row
// background and the changed segments with the stronger highlight.
func renderSpans(content string, spans []HighlightSpan, base, highlight lipgloss.Style) string {
	if len(spans) == 0 {
		return base.Render(content)
	}
	var sb strings.Builder
	pos := 0
	for _, span := range spans {
		start, end := clampSpan(content, span)
		if start > pos {
			sb.WriteString(base.Render(content[pos:start]))
		}
		if end > start {
			sb.WriteString(highlight.Render(content[start:end]))
		}
		pos = end
	}
	if pos < len(content) {
		sb.WriteString(base.Render(content[pos:]))
	}
	return sb.String()
}

// clampSpan keeps a span inside the content and on rune boundaries.
func clampSpan(content string, span HighlightSpan) (int, int) {
	start, end := span.Start, span.End
	if start < 0 {
		start = 0
	}
	if end > len(content) {
		end = len(content)
	}
	if start > len(content) {
		start = len(content)
	}
	if end < start {
		end = start
	}
	return start, end
}

// mapSpans shifts spans from raw-content offsets to the expanded content's, so
// the fixed tab expansion does not misplace a highlight.
func mapSpans(raw string, spans []HighlightSpan) []HighlightSpan {
	if len(spans) == 0 || !strings.Contains(raw, "\t") {
		return spans
	}
	out := make([]HighlightSpan, len(spans))
	for i, span := range spans {
		out[i] = HighlightSpan{
			Start: span.Start + (len(tabSpaces)-1)*strings.Count(raw[:clampIndex(raw, span.Start)], "\t"),
			End:   span.End + (len(tabSpaces)-1)*strings.Count(raw[:clampIndex(raw, span.End)], "\t"),
		}
	}
	return out
}

// clampIndex bounds a byte index to the string.
func clampIndex(s string, i int) int {
	if i < 0 {
		return 0
	}
	if i > len(s) {
		return len(s)
	}
	return i
}

// expandTabs replaces each tab with a fixed four spaces.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	return strings.ReplaceAll(s, "\t", tabSpaces)
}

// sanitize drops control characters from untrusted text (paths, file content)
// so a crafted diff cannot move the cursor, recolour the screen, or clear the
// line. Tabs survive: expandTabs owns their width.
func sanitize(s string) string {
	if s == "" {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			sb.WriteRune(r)
		case r < 0x20, r == 0x7f:
			// C0 controls and DEL.
		case r >= 0x80 && r <= 0x9f:
			// C1 controls.
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// truncate cuts styled or plain text to width cells, appending an ellipsis.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// padCell pads a styled cell to exactly width cells, truncating when over.
func padCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w > width {
		return ansi.Truncate(s, width, "…")
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// padLeft right-aligns a line number in its gutter column; an empty or zero
// number renders blank, keeping the gutter stable across added/deleted rows.
func padLeft(number string, width int) string {
	if number == "" || number == "0" {
		return strings.Repeat(" ", width)
	}
	if len(number) >= width {
		return number
	}
	return strings.Repeat(" ", width-len(number)) + number
}

// digits counts the decimal digits in a non-negative number.
func digits(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		n /= 10
		d++
	}
	return d
}
