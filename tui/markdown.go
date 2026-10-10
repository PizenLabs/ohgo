package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// RenderMarkdown renders assistant text for the transcript.
//
// It covers chat prose — fences, inline code, headers, emphasis, links,
// lists, quotes, rules, tables — without a dependency: a chat transcript
// needs readable structure, not a document engine. Anything unrecognised
// passes through untouched, so exotic markup degrades to plain text rather
// than disappearing.
//
// Width handling lives here, not in the caller: plain (unstyled) text is
// measured and wrapped first, styles applied afterwards, so ANSI sequences
// never inflate the budget and wrapped lines never lose their styling.
// Pass width <= 0 to skip wrapping (tests, unknown terminal size).
func RenderMarkdown(text string, pal *Palette) string {
	return RenderMarkdownWrapped(text, pal, 0)
}

// RenderMarkdownWrapped renders assistant text wrapped to width cells.
//
// Raw model output, parsed content, and rendered output stay distinct: the
// stored Entry keeps the raw text untouched, parsing happens here on every
// render, and only this function's return carries ANSI styling. Persisted
// conversation content is never mutated to fit the terminal.
//
// A trailing incomplete construct (unclosed fence, unclosed emphasis, a
// table cut off mid-stream) renders provisionally — fenced text stays
// code-styled, stray markers stay literal — without corrupting neighbouring
// blocks. Because rendering is a pure function of the accumulated text,
// the final content converges to the same presentation as rendering the
// complete text from scratch regardless of chunk boundaries.
func RenderMarkdownWrapped(text string, pal *Palette, width int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	// Raw ANSI is consumed at the boundary: the renderer owns all styling,
	// so escape sequences in model output are stripped before measurement.
	// This keeps cell widths truthful and guarantees wrapping never splits
	// an escape sequence mid-line. Fenced code keeps its text and
	// whitespace; only the foreign styling goes.
	text = stripANSI(text)
	r := &blockRenderer{pal: pal, width: width}
	return r.render(text)
}

// blockRenderer owns content interpretation for one render call.
type blockRenderer struct {
	pal   *Palette
	width int
}

func (r *blockRenderer) render(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			out = append(out, "")
			i++
		case strings.HasPrefix(trimmed, "```"):
			block, next := r.renderFence(lines, i)
			out = append(out, block...)
			i = next
		case isTableHeader(line, lines, i):
			block, next := r.renderTable(lines, i)
			out = append(out, block...)
			i = next
		case isHR(trimmed):
			out = append(out, r.pal.Muted.Render("─ ─ ─"))
			i++
		case isHeading(trimmed):
			out = append(out, r.renderHeading(trimmed))
			i++
		case isQuote(line):
			out = append(out, r.renderQuote(line))
			i++
		case isListItem(line):
			var items []string
			for i < len(lines) && (isListItem(lines[i]) || isListContinuation(lines[i])) {
				items = append(items, lines[i])
				i++
			}
			out = append(out, r.renderList(items)...)
		default:
			// Group consecutive plain lines into one paragraph so a
			// soft-wrapped model paragraph reflows as a unit; blank lines
			// and block starts bound it.
			var para []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if t == "" || strings.HasPrefix(t, "```") || isTableHeader(lines[i], lines, i) ||
					isHR(t) || isHeading(t) || isQuote(lines[i]) || isListItem(lines[i]) {
					break
				}
				para = append(para, lines[i])
				i++
			}
			out = append(out, r.renderParagraph(para)...)
		}
	}
	// Collapse 3+ blank lines to at most one: models emit ragged spacing.
	out = collapseBlanks(out)
	// No trailing blank: the caller (transcript entry, table cell) owns
	// inter-block spacing, so a trailing newline here would double it and
	// push the viewport off its budget.
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func collapseBlanks(lines []string) []string {
	var out []string
	blanks := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blanks++
			if blanks > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blanks = 0
		out = append(out, l)
	}
	return out
}

// wrapWidth reports the usable budget, or 0 for "do not wrap".
func (r *blockRenderer) wrapWidth() int {
	if r.width <= 0 {
		return 0
	}
	return r.width
}

// ---- headings ----

func isHeading(trimmed string) bool {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	return level >= 1 && level <= 6 && len(trimmed) > level && trimmed[level] == ' '
}

func (r *blockRenderer) renderHeading(trimmed string) string {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	text := strings.TrimSpace(trimmed[level:])
	chunks := parseInline(text, r.pal)
	var styled string
	switch level {
	case 1:
		styled = applyChunks(chunks, "", r.pal.Heading)
	case 2:
		styled = applyChunks(chunks, "", r.pal.Prompt)
	default:
		styled = applyChunks(chunks, "", r.pal.Bold)
	}
	// Headings are short by construction; wrap only on overflow.
	if w := r.wrapWidth(); w > 0 && CellWidth(styled) > w {
		wrapped := wrapChunks(chunks, w, "", "", nil)
		for i, wl := range wrapped {
			switch level {
			case 1:
				wrapped[i] = r.pal.Heading.Render(wl)
			case 2:
				wrapped[i] = r.pal.Prompt.Render(wl)
			default:
				wrapped[i] = r.pal.Bold.Render(wl)
			}
		}
		return strings.Join(wrapped, "\n")
	}
	return styled
}

// ---- fences ----

func (r *blockRenderer) renderFence(lines []string, start int) ([]string, int) {
	trimmed := strings.TrimSpace(lines[start])
	lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
	head := r.pal.CodeFence.Render("```" + lang)
	var out []string
	out = append(out, head)
	i := start + 1
	closed := false
	for ; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			closed = true
			i++
			break
		}
		out = append(out, r.renderCodeLine(lines[i]))
	}
	// An unclosed fence renders to the end as code: provisional during
	// streaming, exact when the model never closed it.
	if closed {
		out = append(out, r.pal.CodeFence.Render("```"))
	}
	return out, i
}

func (r *blockRenderer) renderCodeLine(line string) string {
	if w := r.wrapWidth(); w > 0 && CellWidth(line) > w {
		// Preserve whitespace: hard-fold, never reindent.
		return strings.Join(sliceCells(line, w), "\n")
	}
	if line == "" {
		return r.pal.Code.Render(" ")
	}
	return r.pal.Code.Render(line)
}

// ---- quotes ----

func isQuote(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), ">")
}

func (r *blockRenderer) renderQuote(line string) string {
	trimmed := strings.TrimSpace(line)
	inner := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
	// A second ">" nests one level deeper; the prefix grows, text unchanged.
	for strings.HasPrefix(inner, ">") {
		inner = strings.TrimSpace(strings.TrimPrefix(inner, ">"))
	}
	chunks := parseInline(inner, r.pal)
	if w := r.wrapWidth(); w > 0 {
		wrapped := wrapChunks(chunks, w, "│ ", "│ ", nil)
		for i, wl := range wrapped {
			wrapped[i] = r.pal.Quote.Render(wl)
		}
		return strings.Join(wrapped, "\n")
	}
	return r.pal.Quote.Render("│ " + applyChunksPlain(chunks))
}

// ---- lists ----

// listMarker splits a line into its indent, marker, and body. It reports
// ok=false for non-list lines.
func listMarker(line string) (indent, marker, body string, ordered bool, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	lead := line[:len(line)-len(trimmed)]
	// Tabs count as two cells for nesting depth.
	indent = strings.ReplaceAll(lead, "\t", "  ")
	switch {
	case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ "):
		return indent, strings.TrimSpace(trimmed[:1]), strings.TrimSpace(trimmed[2:]), false, true
	default:
		dot := strings.Index(trimmed, ".")
		paren := strings.Index(trimmed, ")")
		sep := dot
		if sep < 0 || (paren >= 0 && paren < sep) {
			sep = paren
		}
		if sep <= 0 || !strings.HasPrefix(trimmed[sep:], string(trimmed[sep])+" ") {
			return "", "", "", false, false
		}
		for _, ch := range trimmed[:sep] {
			if ch < '0' || ch > '9' {
				return "", "", "", false, false
			}
		}
		return indent, strings.TrimSpace(trimmed[:sep+1]), strings.TrimSpace(trimmed[sep+2:]), true, true
	}
}

func isListItem(line string) bool {
	_, _, _, _, ok := listMarker(line)
	return ok
}

// isListContinuation keeps indented plain lines with their list item so a
// wrapped model bullet ("  continued text") does not become a paragraph.
func isListContinuation(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	_, _, _, _, ok := listMarker(line)
	if ok {
		return false
	}
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

func (r *blockRenderer) renderList(items []string) []string {
	var out []string
	for _, item := range items {
		indent, marker, body, ordered, ok := listMarker(item)
		if !ok {
			// Continuation of the previous bullet: fold into its
			// hanging indent when there is one.
			if len(out) > 0 {
				out[len(out)-1] += " " + strings.TrimSpace(item)
			} else {
				out = append(out, r.renderParagraph([]string{item})...)
			}
			continue
		}
		var bullet string
		if ordered {
			bullet = r.pal.Bullet.Render(marker)
		} else {
			bullet = r.pal.Bullet.Render("•")
		}
		prefix := indent + "  " // placeholder replaced below
		_ = prefix
		first := indent + "• "
		chunks := parseInline(body, r.pal)
		if w := r.wrapWidth(); w > 0 {
			cont := indent + "  "
			if ordered {
				cont = indent + strings.Repeat(" ", CellWidth(marker)+1)
			}
			// First line carries the bullet; continuations hang-align.
			wrapped := wrapChunks(chunks, w, indent+"X ", cont, nil)
			for i, wl := range wrapped {
				if i == 0 {
					// Replace the placeholder indent with bullet + space.
					inner := strings.TrimPrefix(wl, indent+"X ")
					wrapped[i] = indent + bullet + " " + inner
				} else {
					wrapped[i] = wl
				}
			}
			_ = first
			out = append(out, wrapped...)
			continue
		}
		styled := applyChunksPlain(chunks)
		_ = first
		out = append(out, indent+bullet+" "+styled)
	}
	return out
}

// ---- paragraphs ----

func (r *blockRenderer) renderParagraph(src []string) []string {
	text := strings.Join(src, " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	chunks := parseInline(text, r.pal)
	if w := r.wrapWidth(); w > 0 {
		return wrapChunks(chunks, w, "", "", nil)
	}
	return []string{applyChunksPlain(chunks)}
}

// isHR reports a horizontal rule line.
func isHR(trimmed string) bool {
	return trimmed == "---" || trimmed == "***" || trimmed == "___"
}

// ---- tables ----

// isTableHeader reports a pipe header line followed by a delimiter row.
func isTableHeader(line string, lines []string, i int) bool {
	if !strings.Contains(line, "|") {
		return false
	}
	if i+1 >= len(lines) {
		return false
	}
	return isDelimiterRow(lines[i+1])
}

func isDelimiterRow(line string) bool {
	t := strings.TrimSpace(line)
	t = strings.Trim(t, "|")
	if t == "" {
		return false
	}
	for _, cell := range strings.Split(t, "|") {
		c := strings.TrimSpace(cell)
		if len(c) < 3 && strings.Trim(c, ":-") != "" {
			return false
		}
		if c == "" {
			return false
		}
		for _, ch := range c {
			if ch != '-' && ch != ':' {
				return false
			}
		}
		if !strings.Contains(c, "-") {
			return false
		}
	}
	return true
}

func splitRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func (r *blockRenderer) renderTable(lines []string, start int) ([]string, int) {
	header := splitRow(lines[start])
	ncols := len(header)
	var rows [][]string
	i := start + 2 // skip delimiter
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" || !strings.Contains(lines[i], "|") {
			break
		}
		row := splitRow(lines[i])
		// Ragged rows pad/truncate to the header width rather than
		// corrupting the grid.
		for len(row) < ncols {
			row = append(row, "")
		}
		rows = append(rows, row[:ncols])
	}
	width := r.wrapWidth()
	if width <= 0 {
		width = 80
	}
	// Natural widths from display (unstyled) text.
	natural := make([]int, ncols)
	all := append([][]string{header}, rows...)
	for _, row := range all {
		for c, cell := range row {
			if w := CellWidth(stripInline(cell, r.pal)); w > natural[c] {
				natural[c] = w
			}
		}
	}
	overhead := 3*ncols + 1 // "| a | b |"
	avail := width - overhead
	const minCol = 4
	if avail < ncols*minCol {
		return r.renderTableStacked(header, rows), i
	}
	colW := append([]int(nil), natural...)
	for sum(colW) > avail {
		// Shrink the widest column above the minimum.
		best, bestW := -1, minCol
		for c, w := range colW {
			if w > bestW {
				best, bestW = c, w
			}
		}
		if best < 0 {
			break
		}
		colW[best]--
	}
	var out []string
	emit := func(cells []string, isHeader bool) {
		wrapped := make([][]string, ncols)
		height := 1
		for c, cell := range cells {
			chunks := parseInline(cell, r.pal)
			lines := wrapChunks(chunks, colW[c], "", "", nil)
			wrapped[c] = lines
			if len(lines) > height {
				height = len(lines)
			}
		}
		for row := 0; row < height; row++ {
			var sb strings.Builder
			sb.WriteString("| ")
			for c := 0; c < ncols; c++ {
				text := ""
				if row < len(wrapped[c]) {
					text = wrapped[c][row]
				}
				sb.WriteString(padCell(text, colW[c]))
				if c < ncols-1 {
					sb.WriteString(" | ")
				}
			}
			sb.WriteString(" |")
			line := sb.String()
			if isHeader {
				line = r.pal.Bold.Render(line)
			}
			out = append(out, line)
		}
	}
	emit(header, true)
	sep := "|"
	for _, w := range colW {
		sep += strings.Repeat("-", w+2) + "|"
	}
	out = append(out, r.pal.Muted.Render(sep))
	for _, row := range rows {
		emit(row, false)
	}
	return out, i
}

// renderTableStacked is the narrow-terminal fallback: each row becomes
// "header: value" lines so nothing overflows and no grid corrupts.
func (r *blockRenderer) renderTableStacked(header []string, rows [][]string) []string {
	var out []string
	for _, row := range rows {
		for c, cell := range row {
			label := ""
			if c < len(header) {
				label = strings.TrimSpace(stripInline(header[c], r.pal))
			}
			chunks := parseInline(strings.TrimSpace(cell), r.pal)
			text := applyChunksPlain(chunks)
			var line string
			if label != "" {
				line = r.pal.Bold.Render(label+": ") + text
			} else {
				line = r.pal.Bullet.Render("•") + " " + text
			}
			if w := r.wrapWidth(); w > 0 {
				out = append(out, WrapToWidth(stripANSI(line), w)...)
			} else {
				out = append(out, line)
			}
		}
		out = append(out, "")
	}
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// stripANSI removes escape sequences for measurement and the narrow-table
// fallback, where styled text must reflow as plain cells.
func stripANSI(s string) string {
	return ansi.Strip(s)
}

func sum(vals []int) int {
	total := 0
	for _, v := range vals {
		total += v
	}
	return total
}

func padCell(s string, width int) string {
	w := CellWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// ---- inline ----

// chunk is one display run with a single style. A nil style is plain text.
// Chunks let wrapping measure display cells first and apply ANSI after,
// so styling never inflates the budget and links never split mid-markup.
type chunk struct {
	text  string
	style *lipgloss.Style
}

func styledChunk(text string, style lipgloss.Style) chunk {
	s := style
	return chunk{text: text, style: &s}
}

// parseInline scans one line rune-wise: code spans, bold, italics, links.
// An unclosed marker renders literally, so stray asterisks and partial
// streaming delimiters survive intact and converge when closed.
func parseInline(text string, pal *Palette) []chunk {
	runes := []rune(text)
	var out []chunk
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, chunk{text: plain.String()})
			plain.Reset()
		}
	}
	i := 0
	for i < len(runes) {
		switch {
		case runes[i] == '`':
			end := indexRune(runes[i+1:], '`')
			if end < 0 {
				plain.WriteRune(runes[i])
				i++
				continue
			}
			flush()
			out = append(out, styledChunk(string(runes[i+1:i+1+end]), pal.Code))
			i += end + 2
		case runes[i] == '[':
			if link, rest, ok := parseLink(runes[i:], pal); ok {
				flush()
				out = append(out, link...)
				i = len(runes) - len(rest)
				continue
			}
			plain.WriteRune(runes[i])
			i++
		case i+1 < len(runes) && runes[i] == '*' && runes[i+1] == '*':
			end := indexDouble(runes[i+2:])
			if end < 0 {
				plain.WriteString(string(runes[i:]))
				i = len(runes)
				continue
			}
			flush()
			out = append(out, styledChunk(string(runes[i+2:i+2+end]), pal.Bold))
			i += end + 4
		case runes[i] == '*' || runes[i] == '_':
			marker := runes[i]
			end := indexRune(runes[i+1:], marker)
			if end < 0 {
				plain.WriteRune(runes[i])
				i++
				continue
			}
			// A single "*" inside a word (2*3) is math, not emphasis.
			if marker == '*' && i > 0 && i+1+end < len(runes) &&
				isWordRune(runes[i-1]) && isWordRune(runes[i+1+end]) {
				plain.WriteRune(runes[i])
				i++
				continue
			}
			flush()
			out = append(out, styledChunk(string(runes[i+1:i+1+end]), pal.Italic))
			i += end + 2
		default:
			plain.WriteRune(runes[i])
			i++
		}
	}
	flush()
	return out
}

// parseLink parses "[text](url)" at the head of runes, returning the display
// chunks and the unconsumed tail. The text keeps emphasis; the URL follows
// in muted parens so no bracket or paren leaks into completed output.
func parseLink(runes []rune, pal *Palette) ([]chunk, []rune, bool) {
	close := indexRune(runes[1:], ']')
	if close < 0 {
		return nil, runes, false
	}
	after := iPlus(runes, 1+close+1)
	if len(after) < 2 || after[0] != '(' {
		return nil, runes, false
	}
	end := indexRune(after[1:], ')')
	if end < 0 {
		return nil, runes, false
	}
	label := string(runes[1 : 1+close])
	url := strings.TrimSpace(string(after[1 : 1+end]))
	// A URL never contains a raw space: one means the brackets were prose
	// (e.g. "[see] (notes)"), not a link, so the markup stays literal.
	if strings.TrimSpace(label) == "" || url == "" || strings.Contains(url, " ") || strings.Contains(url, "\n") {
		return nil, runes, false
	}
	inner := parseInline(label, pal)
	// Re-style inner emphasis as-is; plain label text stays plain.
	out := append([]chunk(nil), inner...)
	out = append(out, styledChunk(" ("+url+")", pal.Muted))
	return out, after[1+end+1:], true
}

func iPlus(runes []rune, i int) []rune {
	if i >= len(runes) {
		return nil
	}
	return runes[i:]
}

func isWordRune(r rune) bool {
	return r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
}

func indexRune(hay []rune, needle rune) int {
	for i, r := range hay {
		if r == needle {
			return i
		}
	}
	return -1
}

func indexDouble(hay []rune) int {
	for i := 0; i+1 < len(hay); i++ {
		if hay[i] == '*' && hay[i+1] == '*' {
			return i
		}
	}
	return -1
}

// applyChunksPlain renders chunks with ANSI styling.
func applyChunksPlain(chunks []chunk) string {
	var sb strings.Builder
	for _, c := range chunks {
		if c.style != nil {
			sb.WriteString(c.style.Render(c.text))
		} else {
			sb.WriteString(c.text)
		}
	}
	return sb.String()
}

// applyChunks renders chunks inside an outer block style (headings): plain
// runs take the outer style, inner emphasis keeps its own.
func applyChunks(chunks []chunk, prefix string, outer lipgloss.Style) string {
	var sb strings.Builder
	sb.WriteString(prefix)
	for _, c := range chunks {
		if c.style != nil {
			sb.WriteString(c.style.Render(c.text))
		} else {
			sb.WriteString(outer.Render(c.text))
		}
	}
	return sb.String()
}

// stripInline returns the display text of a cell without ANSI styling.
func stripInline(text string, pal *Palette) string {
	var sb strings.Builder
	for _, c := range parseInline(text, pal) {
		sb.WriteString(c.text)
	}
	return sb.String()
}

// styledRune is one display rune tagged with its chunk index (-1 plain).
// Integer tags group runs without comparing lipgloss styles, which hold
// funcs and do not compare.
type styledRune struct {
	r     rune
	chunk int
}

// wrapChunks folds display text to width cells, preserving hanging indent.
//
// first is the indent of the first line, cont of continuations. Styles are
// applied per wrapped line, so measurement stays on plain cells and ANSI
// never crosses a line boundary split mid-sequence.
func wrapChunks(chunks []chunk, width int, first, cont string, _ *Palette) []string {
	var flat []styledRune
	for ci, c := range chunks {
		tag := ci
		if c.style == nil {
			tag = -1
		}
		for _, r := range c.text {
			flat = append(flat, styledRune{r: r, chunk: tag})
		}
	}
	styles := map[int]*lipgloss.Style{}
	for ci, c := range chunks {
		if c.style != nil {
			s := c.style
			styles[ci] = s
		}
	}
	paint := func(rs []styledRune) string {
		var sb strings.Builder
		i := 0
		for i < len(rs) {
			j := i + 1
			for j < len(rs) && rs[j].chunk == rs[i].chunk {
				j++
			}
			var text strings.Builder
			for _, v := range rs[i:j] {
				text.WriteRune(v.r)
			}
			if st, ok := styles[rs[i].chunk]; ok {
				sb.WriteString(st.Render(text.String()))
			} else {
				sb.WriteString(text.String())
			}
			i = j
		}
		return sb.String()
	}
	if width <= 0 {
		return []string{paint(flat)}
	}
	firstW := CellWidth(first)
	contW := CellWidth(cont)
	// Split into words on spaces; spaces become break opportunities and
	// are re-emitted singly (list/paragraph spacing is single-space by
	// construction after TrimSpace/Join).
	var words [][]styledRune
	var cur []styledRune
	for _, rs := range flat {
		if rs.r == ' ' || rs.r == '\t' {
			if len(cur) > 0 {
				words = append(words, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, rs)
	}
	if len(cur) > 0 {
		words = append(words, cur)
	}
	if len(words) == 0 {
		return []string{first}
	}
	var out []string
	var line []styledRune
	lineW := 0
	indent := first
	indentW := firstW
	flush := func() {
		out = append(out, indent+paint(line))
		line = nil
		lineW = 0
		indent = cont
		indentW = contW
	}
	for _, word := range words {
		ww := styledWidth(word)
		avail := width - indentW
		if avail < 1 {
			avail = 1
		}
		if ww > avail {
			// Hard-fold an overlong run (URL, hash, CJK without spaces).
			if len(line) > 0 {
				flush()
				avail = width - indentW
			}
			for _, piece := range foldStyled(word, avail) {
				if styledWidth(piece)+lineW > avail && len(line) > 0 {
					flush()
					avail = width - indentW
				}
				if len(line) > 0 {
					line = append(line, styledRune{r: ' ', chunk: -1})
					lineW++
				}
				line = append(line, piece...)
				lineW += styledWidth(piece)
				if lineW >= avail {
					flush()
				}
			}
			continue
		}
		need := ww
		if len(line) > 0 {
			need++ // the space
		}
		if lineW+need > avail {
			flush()
		}
		if len(line) > 0 {
			line = append(line, styledRune{r: ' ', chunk: -1})
			lineW++
		}
		line = append(line, word...)
		lineW += ww
	}
	if len(line) > 0 || len(out) == 0 {
		out = append(out, indent+paint(line))
	}
	return out
}

func styledWidth(rs []styledRune) int {
	w := 0
	for _, v := range rs {
		if v.r == '\n' {
			continue
		}
		rw := runewidth.RuneWidth(v.r)
		if rw < 0 {
			rw = 0
		}
		w += rw
	}
	return w
}

// foldStyled hard-folds a word into width-cell pieces, keeping combining
// marks (zero-width) attached to their base.
func foldStyled(word []styledRune, width int) [][]styledRune {
	if width < 1 {
		width = 1
	}
	var pieces [][]styledRune
	var cur []styledRune
	w := 0
	for _, v := range word {
		rw := runewidth.RuneWidth(v.r)
		if rw == 0 {
			cur = append(cur, v)
			continue
		}
		if w+rw > width && len(cur) > 0 {
			pieces = append(pieces, cur)
			cur = nil
			w = 0
		}
		cur = append(cur, v)
		w += rw
	}
	if len(cur) > 0 {
		pieces = append(pieces, cur)
	}
	return pieces
}
