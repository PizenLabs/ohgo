package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Support modal geometry. The dialog is responsive: it grows with the
// terminal instead of holding a fixed preferred size, so the ANSWER
// viewport yields reading space on roomy terminals and still draws on
// small ones. The formulas are the contract:
//
//	modalWidth = clamp(60, viewportWidth-12, 90)
//	modalHeight = clamp(16, viewportHeight-6, 40)
const (
	// supportModalMinResponsiveWidth is the clamp floor/ceiling pair.
	supportModalLoWidth = 60
	supportModalHiWidth = 90
	// supportModalMinResponsiveHeight bounds the vertical clamp.
	supportModalLoHeight = 16
	supportModalHiHeight = 40
	// supportPadX is the single strict left-margin axis for every modal
	// row: QUERY, ANSWER, dividers, and input all start here.
	supportPadX = 2
)

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SupportModalRect returns the centered support dialog rectangle for a
// terminal size. It follows the responsive clamp, never touches the
// terminal edges while margins fit, and always stays inside the terminal.
func SupportModalRect(termWidth, termHeight int) ModalRect {
	if termWidth <= 0 || termHeight <= 0 {
		return ModalRect{}
	}
	width := clampInt(termWidth-12, supportModalLoWidth, supportModalHiWidth)
	height := clampInt(termHeight-6, supportModalLoHeight, supportModalHiHeight)
	if width > termWidth {
		width = termWidth
	}
	if height > termHeight {
		height = termHeight
	}
	if width < 10 {
		width = 10
		if width > termWidth {
			width = termWidth
		}
	}
	if height < 4 {
		height = 4
		if height > termHeight {
			height = termHeight
		}
	}
	x := (termWidth - width) / 2
	if x < 0 {
		x = 0
	}
	y := 2 + (termHeight-3-height)/2
	if y < 0 {
		y = 0
	}
	if y+height > termHeight {
		y = termHeight - height
	}
	if y < 0 {
		y = 0
	}
	return ModalRect{X: x, Y: y, Width: width, Height: height}
}

// renderSupportArea draws the support overlay as a centered modal dialog.
// The header and status chrome keep rendering around it, and the
// underlying conversation is untouched: closing the modal returns to the
// previous surface without losing anything.
func renderSupportArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	panel := state.SupportPanel
	if panel == nil {
		renderTranscriptArea(sb, state, pal, layout)
		return
	}
	if state.Width <= 0 || state.Height <= 0 {
		sb.WriteString(pal.Heading.Render("SUPPORT") + "\n")
		for _, line := range supportModalLines(panel, pal, 80, 99, state) {
			sb.WriteString(line + "\n")
		}
		return
	}
	rect := SupportModalRect(state.Width, state.Height)
	if rect.Width <= 0 || rect.Height <= 0 {
		sb.WriteString(pal.Heading.Render("SUPPORT") + "\n")
		for _, line := range supportModalLines(panel, pal, 80, 99, state) {
			sb.WriteString(line + "\n")
		}
		return
	}
	interior := rect.Width - 2
	if interior < 1 {
		interior = 1
	}
	// Top border carries the centered title, so the body gains its row:
	// borders (2) + footer (1).
	contentH := rect.Height - 3
	if contentH < 1 {
		contentH = 1
	}
	body := fitPane(supportModalLines(panel, pal, interior, contentH, state), contentH)

	footer := TruncateToWidth(strings.Repeat(" ", supportPadX)+supportFooter(panel, pal, state), interior)
	if gap := interior - CellWidth(footer); gap > 0 {
		footer += strings.Repeat(" ", gap)
	}

	out := make([]string, 0, rect.Height)
	out = append(out, supportTitleBorder(interior))
	for _, line := range body {
		if gap := interior - CellWidth(line); gap > 0 {
			line += strings.Repeat(" ", gap)
		}
		out = append(out, "│"+TruncateToWidth(line, interior)+"│")
	}
	out = append(out, "│"+TruncateToWidth(footer, interior)+"│")
	out = append(out, "└"+strings.Repeat("─", interior)+"┘")

	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	topPad := (avail - len(out)) / 2
	if topPad < 0 {
		topPad = 0
	}
	lines := make([]string, 0, avail)
	for i := 0; i < topPad && len(lines) < avail; i++ {
		lines = append(lines, "")
	}
	for _, line := range out {
		if len(lines) >= avail {
			break
		}
		lines = append(lines, line)
	}
	for len(lines) < avail {
		lines = append(lines, "")
	}
	if len(lines) > avail {
		lines = lines[:avail]
	}
	padLeft := rect.X
	termW := state.Width
	if termW <= 0 {
		termW = rect.Width
	}
	for i, line := range lines {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "┌") && !strings.HasPrefix(line, "│") && !strings.HasPrefix(line, "└") {
			continue
		}
		if padLeft > 0 {
			room := termW - CellWidth(line) - padLeft
			if room < 0 {
				room = 0
			}
			lines[i] = strings.Repeat(" ", padLeft) + line + strings.Repeat(" ", room)
		} else if CellWidth(line) < termW {
			lines[i] = line + strings.Repeat(" ", termW-CellWidth(line))
		}
	}
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
}

// supportModalLines builds the dialog body on the strict grid: an inline
// QUERY row, a divider, the ANSWER header plus scrollable viewport, a
// second divider, and one compact input/status row.
//
// Only the answer window scrolls, driven by panel.Scroll as an offset
// above the live edge, with the header naming what is hidden above and
// below. Fixed chrome (query, dividers, header, input) is pinned so a
// long answer can never evict it; the answer keeps roughly three quarters
// of the body. Every row carries the supportPadX left axis and fits width;
// callers pin the total height with fitPane.
func supportModalLines(panel *SupportPanel, pal *Palette, width, budget int, state State) []string {
	if width < 1 {
		width = 1
	}
	if budget < 1 {
		budget = 1
	}
	contentW := width - supportPadX
	if contentW < 1 {
		contentW = 1
	}
	maxQuery := 3
	if budget < 8 {
		maxQuery = 1
	}
	queryLines := capLines(supportQueryLines(panel, pal, width), maxQuery)

	respAll := supportResponseLines(panel, pal, contentW, state)
	// Pad answer rows onto the shared axis here so scroll math operates on
	// final rows and the header counts match what is drawn.
	paddedResp := make([]string, 0, len(respAll))
	for _, line := range respAll {
		if line == "" {
			paddedResp = append(paddedResp, strings.Repeat(" ", supportPadX))
			continue
		}
		paddedResp = append(paddedResp, TruncateToWidth(strings.Repeat(" ", supportPadX)+line, width))
	}

	// Fixed chrome: query + divider + answer header + divider + input.
	const fixedOverhead = 4
	respBudget := budget - (len(queryLines) + fixedOverhead)
	for respBudget < 1 && len(queryLines) > 1 {
		queryLines = queryLines[:len(queryLines)-1]
		respBudget = budget - (len(queryLines) + fixedOverhead + 1)
	}
	if respBudget < 1 {
		respBudget = 1
	}
	scroll := panel.Scroll
	if scroll < 0 {
		scroll = 0
	}
	maxScroll := len(paddedResp) - respBudget
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	start := len(paddedResp) - respBudget - scroll
	if start < 0 {
		start = 0
	}
	end := len(paddedResp) - scroll
	if end < start {
		end = start
	}
	if end > len(paddedResp) {
		end = len(paddedResp)
	}
	window := append([]string(nil), paddedResp[start:end]...)
	for len(window) < respBudget {
		window = append(window, strings.Repeat(" ", supportPadX))
	}
	above := start
	below := scroll

	var lines []string
	lines = append(lines, queryLines...)
	lines = append(lines, supportDivider(pal, width))
	lines = append(lines, supportAnswerHeader(pal, width, above, below))
	lines = append(lines, window...)
	lines = append(lines, supportDivider(pal, width))
	lines = append(lines, supportInputStatusLine(panel, pal, state, width))
	if len(lines) > budget {
		lines = lines[:budget]
	}
	return lines
}

// supportTitleBorder draws the top border with the centered SUPPORT QUERY
// title in the ASK semantic teal. The border itself is the title row, which
// returns one vertical cell to the answer viewport.
func supportTitleBorder(interior int) string {
	const title = " SUPPORT QUERY "
	titleW := CellWidth(title)
	if interior <= 0 {
		return "┌┐"
	}
	if interior <= titleW {
		t := TruncateToWidth("SUPPORT QUERY", interior)
		if gap := interior - CellWidth(t); gap > 0 {
			t += strings.Repeat(" ", gap)
		}
		return "┌" + WorkspaceBadge("37", t) + "┐"
	}
	left := (interior - titleW) / 2
	right := interior - titleW - left
	styled := WorkspaceBadge("37", title)
	return "┌" + strings.Repeat("─", left) + styled + strings.Repeat("─", right) + "┐"
}

// supportDivider is the subtle section rule on the shared axis.
func supportDivider(pal *Palette, width int) string {
	if width <= supportPadX {
		return TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)
	}
	rule := strings.Repeat("─", maxInt(width-supportPadX, 1))
	return TruncateToWidth(strings.Repeat(" ", supportPadX)+pal.Muted.Render(rule), width)
}

// capRequestLines keeps the first n request rows, replacing the tail with a
// counted marker when the question overflows its pinned block.
func capLines(lines []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if len(lines) <= n {
		return lines
	}
	out := append([]string(nil), lines[:n-1]...)
	hidden := len(lines) - (n - 1)
	out = append(out, fmt.Sprintf("… %d more lines", hidden))
	return out
}

// supportQueryLines renders the submitted question as one scannable inline
// row: QUERY › <question>. An unsubmitted panel shows a muted placeholder
// on the same axis instead of echoing the composer draft.
func supportQueryLines(panel *SupportPanel, pal *Palette, width int) []string {
	pad := strings.Repeat(" ", supportPadX)
	label := pal.Bold.Render("QUERY") + pal.Muted.Render(" › ")
	labelW := CellWidth("QUERY › ")
	contentW := width - supportPadX
	if contentW < 1 {
		contentW = 1
	}
	question := strings.TrimSpace(panel.Request)
	if question == "" {
		ph := pal.Muted.Render(TruncateToWidth("What would you like to inspect?", maxInt(contentW-labelW, 1)))
		return []string{TruncateToWidth(pad+pal.Bold.Render("QUERY")+pal.Muted.Render(" › ")+ph, width)}
	}
	var wrapped []string
	firstBudget := contentW - labelW
	if firstBudget < 1 {
		firstBudget = 1
	}
	contBudget := contentW
	first := true
	for _, raw := range strings.Split(sanitizeBlock(question), "\n") {
		budget := contBudget
		if first {
			budget = firstBudget
		}
		parts := WrapToWidth(strings.TrimSpace(sanitizeInline(raw)), budget)
		if len(parts) == 0 {
			parts = []string{""}
		}
		for i, part := range parts {
			if first && i == 0 {
				wrapped = append(wrapped, pal.User.Render(part))
			} else {
				wrapped = append(wrapped, pal.User.Render(part))
			}
		}
		first = false
	}
	if len(wrapped) == 0 {
		return []string{TruncateToWidth(pad+label, width)}
	}
	out := make([]string, 0, len(wrapped))
	for i, part := range wrapped {
		if i == 0 {
			out = append(out, TruncateToWidth(pad+label+part, width))
			continue
		}
		indent := strings.Repeat(" ", supportPadX+labelW)
		out = append(out, TruncateToWidth(indent+part, width))
	}
	return out
}

// supportAnswerHeader labels the answer block with its scroll indicator
// right-aligned: ANSWER on the shared axis, hidden counts at the right
// edge so a truncated answer reads as scrollable rather than cut off.
func supportAnswerHeader(pal *Palette, width, above, below int) string {
	pad := strings.Repeat(" ", supportPadX)
	left := pad + pal.Heading.Render("ANSWER")
	var indicator string
	switch {
	case above > 0 && below > 0:
		indicator = fmt.Sprintf("↑ %d · ↓ %d", above, below)
	case above > 0:
		indicator = fmt.Sprintf("↑ %d lines hidden", above)
	case below > 0:
		indicator = fmt.Sprintf("↓ %d lines hidden", below)
	default:
		return TruncateToWidth(left, width)
	}
	right := pal.Muted.Render(indicator)
	gap := width - CellWidth(left) - CellWidth(right)
	if gap < 1 {
		// Counts yield first; the label is short by construction.
		right = pal.Muted.Render(TruncateToWidth(indicator, maxInt(width-CellWidth(left)-1, 0)))
		gap = width - CellWidth(left) - CellWidth(right)
		if gap < 1 {
			return TruncateToWidth("ANSWER "+indicator, width)
		}
	}
	return left + strings.Repeat(" ", gap) + right
}

// supportResponseLines renders the full answer through the support markdown
// layer, so emphasis, lists, code, and entity highlights read as structure
// instead of raw markers. Status placeholders and failures stay visible as
// styled lines. Width is the content budget excluding the left axis.
func supportResponseLines(panel *SupportPanel, pal *Palette, width int, state State) []string {
	body := panel.Response
	switch panel.Status {
	case SupportRunning:
		if strings.TrimSpace(body) == "" {
			return []string{pal.Muted.Render(TruncateToWidth(spinnerFrame(state.Status.Spinner)+" working", width))}
		}
	case SupportIdle:
		if strings.TrimSpace(body) == "" {
			return []string{pal.Muted.Render(TruncateToWidth("Type a request and press Enter.", width))}
		}
	case SupportCancelled:
		if strings.TrimSpace(body) == "" {
			return []string{pal.Muted.Render(TruncateToWidth("cancelled", width))}
		}
	case SupportFailed:
		if strings.TrimSpace(body) == "" {
			body = "failed: " + panel.Error
		} else if panel.Error != "" {
			body += "\nfailed: " + panel.Error
		}
	}
	if strings.TrimSpace(body) == "" {
		return []string{pal.Muted.Render(TruncateToWidth("—", width))}
	}
	rendered := RenderSupportMarkdownWrapped(sanitizeBlock(body), pal, width)
	lines := strings.Split(rendered, "\n")
	// A failure's reason is key information: when the markdown body already
	// carried the failure line it renders as prose, so restyle a trailing
	// "failed:" row in the error accent without changing its words.
	if panel.Status == SupportFailed && strings.TrimSpace(panel.Error) != "" {
		for i, line := range lines {
			if strings.Contains(stripANSI(line), "failed:") {
				plain := TruncateToWidth(stripANSI(line), width)
				lines[i] = pal.ToolError.Render(plain)
			}
		}
	}
	if len(lines) == 0 {
		return []string{pal.Muted.Render(TruncateToWidth("—", width))}
	}
	return lines
}

// supportInputStatusLine collapses the bottom zone to one row: the composer
// on the left, the effective SUP model plus runtime status right-aligned.
// The model id truncates first so the status marker always survives.
func supportInputStatusLine(panel *SupportPanel, pal *Palette, state State, width int) string {
	pad := strings.Repeat(" ", supportPadX)
	model := strings.TrimSpace(panel.Model)
	if model == "" {
		model = "model unknown"
	}
	model = sanitizeInline(model)
	statusText, statusStyle := supportStatusMark(panel, pal, state)
	rightPlain := "[" + model + " · " + statusText + "]"
	// Reserve the axis, a space, and the status box; the composer gets the rest.
	leftBudget := width - supportPadX - CellWidth(rightPlain) - 1
	if leftBudget < 8 {
		model = TruncateToWidth(model, maxInt(4, len([]rune(model))-(8-leftBudget)))
		rightPlain = "[" + model + " · " + statusText + "]"
		leftBudget = width - supportPadX - CellWidth(rightPlain) - 1
	}
	if leftBudget < 4 {
		leftBudget = 4
	}
	var left string
	if panel.Status == SupportRunning {
		left = pal.Muted.Render(TruncateToWidth("… working — Ctrl+C cancels", leftBudget))
	} else {
		left = supportInputText(panel, pal, leftBudget)
	}
	right := pal.Muted.Render("[") + pal.Selected.Render(TruncateToWidth(model, maxInt(width-supportPadX-CellWidth(left)-CellWidth(statusText)-6, 1))) + pal.Muted.Render(" · ") + statusStyle.Render(statusText) + pal.Muted.Render("]")
	gap := width - supportPadX - CellWidth(left) - CellWidth(right)
	if gap < 1 {
		gap = 1
	}
	line := pad + left + strings.Repeat(" ", gap) + right
	return TruncateToWidth(line, width)
}

// supportInputText renders the composer draft with its cursor for the
// compact row. Long drafts show the cursor row so typing stays visible.
func supportInputText(panel *SupportPanel, pal *Palette, budget int) string {
	if budget < 1 {
		budget = 1
	}
	promptW := CellWidth("> ")
	inner := budget - promptW
	if inner < 1 {
		inner = 1
	}
	clean, cursor := sanitizeWithCursor(panel.Input, panel.Cursor)
	logical := strings.Split(clean, "\n")
	var rows []string
	for _, line := range logical {
		rows = append(rows, WrapToWidth(line, inner)...)
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	rowIdx, colIdx := supportCursorRow(logical, cursor, inner)
	if rowIdx < 0 {
		rowIdx = 0
	}
	if rowIdx >= len(rows) {
		rowIdx = len(rows) - 1
		colIdx = len([]rune(stripANSI(rows[rowIdx])))
	}
	// The compact row shows the cursor row; a pasted novel shows its tail
	// context instead of evicting the answer.
	text := rows[rowIdx]
	runes := []rune(text)
	if colIdx < 0 {
		colIdx = 0
	}
	if colIdx > len(runes) {
		colIdx = len(runes)
	}
	withCursor := string(runes[:colIdx]) + pal.Cursor.Render("▌") + string(runes[colIdx:])
	return TruncateToWidth(pal.Prompt.Render("> ")+withCursor, budget)
}

// supportCursorRow maps a rune cursor in sanitized composer text onto its
// wrapped display row and column within that row.
func supportCursorRow(logical []string, cursor, inner int) (int, int) {
	if cursor < 0 {
		cursor = 0
	}
	offset := 0
	for i, line := range logical {
		runes := len([]rune(line))
		if cursor <= offset+runes {
			col := cursor - offset
			wrapped := WrapToWidth(line, inner)
			consumed := 0
			base := 0
			for k := 0; k < i; k++ {
				base += len(WrapToWidth(logical[k], inner))
			}
			for j, wl := range wrapped {
				w := len([]rune(stripANSI(wl)))
				if col <= consumed+w {
					return base + j, col - consumed
				}
				consumed += w
			}
			return base + len(wrapped) - 1, len([]rune(stripANSI(wrapped[len(wrapped)-1])))
		}
		offset += runes + 1 // the newline
	}
	lastWrapped := WrapToWidth(logical[len(logical)-1], inner)
	if len(lastWrapped) == 0 {
		return 0, 0
	}
	last := 0
	for _, line := range logical {
		last += len(WrapToWidth(line, inner))
	}
	return last - 1, len([]rune(stripANSI(lastWrapped[len(lastWrapped)-1])))
}

// supportStatusMark pairs each lifecycle state with a text marker and its
// semantic color, matching the tool-state convention.
func supportStatusMark(panel *SupportPanel, pal *Palette, state State) (string, lipgloss.Style) {
	switch panel.Status {
	case SupportRunning:
		return spinnerFrame(state.Status.Spinner) + " working", pal.ToolRun
	case SupportCompleted:
		return "✓ done", pal.ToolDone
	case SupportCancelled:
		return "· cancelled", pal.Muted
	case SupportFailed:
		return "✗ failed", pal.ToolError
	default:
		return "○ idle", pal.Muted
	}
}

// supportFooter names the keys that actually work in the panel. There are
// no printable shortcuts: with a text input on screen, only Enter, Esc,
// Ctrl+C, PgUp, and PgDn are advertised. The row stays compact so the
// answer keeps the vertical budget.
func supportFooter(panel *SupportPanel, pal *Palette, state State) string {
	_ = pal
	_ = state
	if panel.Status == SupportRunning {
		return "Esc close  ·  PgUp/PgDn scroll  ·  Ctrl+C cancel"
	}
	return "Esc close  ·  PgUp/PgDn scroll  ·  Enter ask"
}
