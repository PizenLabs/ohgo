package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Palette holds every named style the view uses.
//
// Colours are referenced through the palette rather than inline escape codes
// so a theme change lands in one place. The two constructors below are the
// only literals.
type Palette struct {
	User      lipgloss.Style
	Assistant lipgloss.Style
	ToolName  lipgloss.Style
	ToolDone  lipgloss.Style
	ToolError lipgloss.Style
	ToolRun   lipgloss.Style
	Notice    lipgloss.Style
	Status    lipgloss.Style
	Prompt    lipgloss.Style
	Choice    lipgloss.Style
	Selected  lipgloss.Style
	Truncated lipgloss.Style
	Cursor    lipgloss.Style
	Queued    lipgloss.Style
	// Markdown styles.
	Code      lipgloss.Style
	CodeFence lipgloss.Style
	Heading   lipgloss.Style
	Bold      lipgloss.Style
	Italic    lipgloss.Style
	Quote     lipgloss.Style
	Bullet    lipgloss.Style
	Muted     lipgloss.Style
}

// DarkPalette is the default: bright foregrounds for dark terminals.
func DarkPalette() *Palette {
	return &Palette{
		User:      lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),
		Assistant: lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		ToolName:  lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true),
		ToolDone:  lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		ToolError: lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		ToolRun:   lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		Notice:    lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true),
		Status:    lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		Prompt:    lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
		Choice:    lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		Selected:  lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),
		Truncated: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		Cursor:    lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Reverse(true),
		Queued:    lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true),
		Code:      lipgloss.NewStyle().Foreground(lipgloss.Color("228")).Background(lipgloss.Color("236")),
		CodeFence: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		Heading:   lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),
		Bold:      lipgloss.NewStyle().Bold(true),
		Italic:    lipgloss.NewStyle().Italic(true),
		Quote:     lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true),
		Bullet:    lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
		Muted:     lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
}

// LightPalette reads on bright terminals: dark foregrounds, no heavy
// backgrounds that would clash with a light screen.
func LightPalette() *Palette {
	return &Palette{
		User:      lipgloss.NewStyle().Foreground(lipgloss.Color("27")).Bold(true),
		Assistant: lipgloss.NewStyle().Foreground(lipgloss.Color("235")),
		ToolName:  lipgloss.NewStyle().Foreground(lipgloss.Color("127")).Bold(true),
		ToolDone:  lipgloss.NewStyle().Foreground(lipgloss.Color("28")),
		ToolError: lipgloss.NewStyle().Foreground(lipgloss.Color("160")),
		ToolRun:   lipgloss.NewStyle().Foreground(lipgloss.Color("166")),
		Notice:    lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Italic(true),
		Status:    lipgloss.NewStyle().Foreground(lipgloss.Color("242")),
		Prompt:    lipgloss.NewStyle().Foreground(lipgloss.Color("27")),
		Choice:    lipgloss.NewStyle().Foreground(lipgloss.Color("235")),
		Selected:  lipgloss.NewStyle().Foreground(lipgloss.Color("27")).Bold(true),
		Truncated: lipgloss.NewStyle().Foreground(lipgloss.Color("242")),
		Cursor:    lipgloss.NewStyle().Foreground(lipgloss.Color("27")).Reverse(true),
		Queued:    lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Italic(true),
		Code:      lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("254")),
		CodeFence: lipgloss.NewStyle().Foreground(lipgloss.Color("242")),
		Heading:   lipgloss.NewStyle().Foreground(lipgloss.Color("27")).Bold(true),
		Bold:      lipgloss.NewStyle().Bold(true),
		Italic:    lipgloss.NewStyle().Italic(true),
		Quote:     lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Italic(true),
		Bullet:    lipgloss.NewStyle().Foreground(lipgloss.Color("27")),
		Muted:     lipgloss.NewStyle().Foreground(lipgloss.Color("242")),
	}
}

// PaletteFor resolves a theme name to its palette. Unknown names degrade to
// dark, matching NormalizeTheme: the view never fails to render.
func PaletteFor(theme string) *Palette {
	if NormalizeTheme(theme) == ThemeLight {
		return LightPalette()
	}
	return DarkPalette()
}

// spinnerFrames is the animation used for running tools.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Render produces the full view for a state.
//
// It is pure: the same state always renders the same string, which is what
// makes it testable even though the output is not asserted on by a test — the
// point is that nothing here touches a terminal, performs I/O, or mutates
// runtime state. Layout derives from WindowSizeMsg dimensions recorded in
// state; a resize re-renders without issuing requests.
func Render(state State) string {
	pal := PaletteFor(state.Theme)
	layout := ComputeLayout(state)
	if layout.Mode == LayoutMini && state.Width > 0 && state.Height > 0 {
		return renderMini(state, pal, layout)
	}

	var sb strings.Builder
	sb.WriteString(renderHeader(state, pal, layout) + "\n")
	sb.WriteString(SeparatorLine(separatorWidth(state, layout), pal) + "\n")

	if state.Picker != nil {
		renderPickerArea(&sb, state, pal, layout)
	} else {
		renderTranscriptArea(&sb, state, pal, layout)
	}

	if state.Pending != nil {
		renderApproval(&sb, state.Pending, pal, layout)
	}
	for _, queued := range state.Queued {
		sb.WriteString(pal.Queued.Render("queued: "+TruncateToWidth(queued, displayWidth(state, layout))) + "\n")
	}

	sb.WriteString(renderStatus(state, pal, layout))
	sb.WriteString("\n")
	if state.Picker == nil {
		sb.WriteString(renderComposer(state, pal, layout))
		if layout.ShowHints {
			sb.WriteString("\n" + pal.Muted.Render(ComposerHints(state.Status.Busy)))
		}
	}
	return sb.String()
}

// renderMini concedes a viewport too small for the workbench. It keeps the
// composer text and the model visible in prose so nothing is silently lost,
// and tells the user the useful action: enlarge the terminal.
func renderMini(state State, pal *Palette, layout Layout) string {
	var sb strings.Builder
	sb.WriteString(pal.Heading.Render("ohgo — terminal too small") + "\n")
	sb.WriteString(pal.Muted.Render(fmt.Sprintf("%dx%d: enlarge to at least %dx%d for the workbench.",
		state.Width, state.Height, MiniMinWidth, MiniMinHeight)) + "\n")
	if state.Status.Model != "" {
		sb.WriteString(pal.Status.Render(state.Status.Model) + "\n")
	}
	if strings.TrimSpace(state.Input) != "" {
		sb.WriteString(pal.Prompt.Render("> ") + TruncateToWidth(state.Input, layout.Width) + "\n")
	}
	sb.WriteString(pal.Muted.Render("your input and history are preserved.") + "\n")
	return sb.String()
}

// renderHeader draws product identity, runtime phase, model, and connection.
//
// Wide shows everything; medium drops the connection detail to its state
// word; narrow keeps identity plus phase only. Connection is words, not color
// alone: a dot nobody can decode is decoration, not information.
func renderHeader(state State, pal *Palette, layout Layout) string {
	runtime := DeriveRuntime(state)
	marker := RuntimeMarker(runtime)
	model := state.Status.Model
	if model == "" {
		model = "no model"
	}
	switch layout.Mode {
	case LayoutNarrow:
		return pal.Status.Render(fmt.Sprintf("ohgo %s %s · %s", marker, model, string(runtime)))
	case LayoutMedium:
		conn := "not checked"
		if state.ConnDetail != "" {
			if state.ConnOK {
				conn = "connected"
			} else {
				conn = "not connected"
			}
		}
		return pal.Status.Render(fmt.Sprintf("ohgo %s %s · %s · %s", marker, model, string(runtime), conn))
	default:
		conn := "○ not checked"
		if state.ConnDetail != "" {
			if state.ConnOK {
				conn = "● " + state.ConnDetail
			} else {
				conn = "○ " + state.ConnDetail
			}
		}
		return pal.Status.Render(fmt.Sprintf("ohgo %s %s · %s · %s", marker, model, string(runtime), conn))
	}
}

// renderTranscriptArea writes the transcript, wrapped to the content width
// and sliced to the conversation height when the size is known.
func renderTranscriptArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	body := renderTranscriptBody(state, pal, layout)
	lines := splitLines(body)
	if state.Height <= 0 || layout.Mode == LayoutWide && state.Width <= 0 {
		sb.WriteString(body)
		if body != "" && !strings.HasSuffix(body, "\n") {
			sb.WriteString("\n")
		}
		return
	}
	avail := layout.ConversationHeight
	// Overlays (approval, queue) claim rows inside the conversation budget so
	// regions never overlap.
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	avail -= len(state.Queued)
	if avail < 1 {
		avail = 1
	}
	if len(lines) <= avail {
		sb.WriteString(body)
		return
	}
	if state.Scroll > 0 {
		sb.WriteString(pal.Muted.Render(fmt.Sprintf("↑ %d lines above — end to follow", state.Scroll)) + "\n")
		avail--
		if avail < 1 {
			avail = 1
		}
	}
	start := len(lines) - avail - state.Scroll
	if start < 0 {
		start = 0
	}
	end := len(lines) - state.Scroll
	if end < start {
		end = start
	}
	if end > len(lines) {
		end = len(lines)
	}
	for _, line := range lines[start:end] {
		sb.WriteString(line + "\n")
	}
}

// renderTranscriptBody renders every entry wrapped to the content width.
func renderTranscriptBody(state State, pal *Palette, layout Layout) string {
	var body strings.Builder
	for _, entry := range state.Entries {
		renderEntry(&body, entry, pal, layout)
	}
	return body.String()
}

// chromeLines counts the non-transcript lines so the viewport knows what is
// left for history. It mirrors Render's own layout: header, separator,
// approval, queued, status, composer, and hints.
func chromeLines(state State) int {
	layout := ComputeLayout(state)
	if state.Width <= 0 || state.Height <= 0 {
		lines := 1 + 1 + 1 // header + blank + status
		if state.Pending != nil {
			lines += approvalLines(state.Pending, layout)
		}
		lines += len(state.Queued)
		lines += composerLines(state)
		return lines
	}
	lines := layout.HeaderLines + layout.StatusLines
	if layout.ShowHints && state.Picker == nil {
		lines++
	}
	if state.Pending != nil {
		lines += approvalLines(state.Pending, layout)
	}
	lines += len(state.Queued)
	lines += layout.ComposerLines
	return lines
}

// approvalLines counts the rendered approval block, bounded to the viewport.
func approvalLines(pending *PendingApproval, layout Layout) int {
	detail := len(boundedPreview(pending, layout))
	return 1 + detail + len(pending.Choices) + 1 // prompt + preview + choices + hints
}

// boundedPreview wraps summary+reason and caps rows so the dialog never
// exceeds the viewport. Excess collapses into a counted marker line rather
// than pushing chrome off-screen or truncating the choices.
func boundedPreview(pending *PendingApproval, layout Layout) []string {
	var lines []string
	width := dialogWidth(layout)
	for _, text := range []string{pending.Summary, pending.Reason} {
		if text == "" {
			continue
		}
		lines = append(lines, WrapToWidth(text, width)...)
	}
	max := len(lines)
	if layout.Height > 0 {
		max = layout.Height - len(pending.Choices) - 8
		if max < 2 {
			max = 2
		}
		if max > 10 {
			max = 10
		}
	} else if max > 10 {
		max = 10
	}
	if len(lines) <= max {
		return lines
	}
	kept := append([]string(nil), lines[:max]...)
	kept = append(kept, fmt.Sprintf("… %d more lines", len(lines)-max))
	return kept
}

// composerLines counts the rendered composer block.
func composerLines(state State) int {
	layout := ComputeLayout(state)
	if state.Width > 0 && layout.ContentWidth > 0 {
		return composerHeight(state, layout.ContentWidth, layout.Mode)
	}
	return countLines(state.Input)
}

// splitLines splits rendered output into lines without a trailing empty.
func splitLines(body string) []string {
	trimmed := strings.TrimRight(body, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// renderPickerArea shows the selection overlay in place of the transcript.
//
// The transcript hides while picking: an overlay that shared the screen with
// a streaming transcript would jump under the cursor with every event. The
// search row stays fixed while only the result window scrolls; the highlight
// is clamped into the window by EnsureVisible, so the marker is always
// rendered on a visible row, never outside the viewport.
func renderPickerArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	picker := state.Picker
	width := displayWidth(state, layout)
	sb.WriteString(pal.Heading.Render(TruncateToWidth(pickerHeader(picker), width)) + "\n")
	sb.WriteString(renderPickerSearch(picker, pal, width) + "\n")
	filtered := picker.Filtered()
	window, start := picker.windowOf(filtered)
	if picker.Loading && len(window) == 0 {
		sb.WriteString(pal.Muted.Render("  loading catalog…") + "\n")
	} else if len(window) == 0 {
		sb.WriteString(pal.Muted.Render("  (no matches — esc clears the search)") + "\n")
	} else {
		for i, item := range window {
			row := renderPickerRow(start+i, picker, item, pal, layout, width)
			for _, wl := range WrapToWidth(row, width) {
				sb.WriteString(wl + "\n")
			}
		}
		if hidden := len(filtered) - start - len(window); hidden > 0 {
			sb.WriteString(pal.Muted.Render(fmt.Sprintf("  … %d more", hidden)) + "\n")
		}
	}
	if picker.Kind == PickerModels {
		if status := pickerCatalogStatus(picker); status != "" {
			sb.WriteString(pal.Muted.Render(TruncateToWidth(status, width)) + "\n")
		}
	}
	sb.WriteString(pal.Muted.Render(TruncateToWidth(pickerFooter(picker, layout), width)) + "\n")
}

// pickerHeader names the overlay, the provider scope, the result count, and
// any loading state: "Models · All providers · 215 results".
func pickerHeader(picker *PickerState) string {
	scope := "All providers"
	if provider := picker.ProviderScope(); provider != "" {
		scope = "provider:" + provider
	}
	count := len(picker.Filtered())
	results := "results"
	if count == 1 {
		results = "result"
	}
	title := picker.Title
	if title == "" {
		title = "select"
	}
	if picker.Kind == PickerModels {
		return fmt.Sprintf("%s · %s · %d %s", title, scope, count, results)
	}
	return fmt.Sprintf("%s · %d %s", title, count, results)
}

// renderPickerSearch draws the query with a text cursor. The cursor renders
// inside the query so spaces and mid-text edits stay visible; active scope
// filters show after the query without covering it.
func renderPickerSearch(picker *PickerState, pal *Palette, width int) string {
	label := "Search models… "
	if picker.Kind != PickerModels {
		label = "> "
	}
	runes := []rune(picker.Filter)
	cur := picker.FilterCursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(runes) {
		cur = len(runes)
	}
	query := string(runes[:cur]) + pal.Cursor.Render(" ") + string(runes[cur:])
	line := label + query
	if scope := picker.ProviderScope(); scope != "" && picker.Kind == PickerModels {
		line += "  " + pal.Muted.Render("[provider:"+TruncateToWidth(scope, 24)+"]")
	}
	return TruncateToWidth(line, width)
}

// renderPickerRow draws one result row: marker first so long ids truncate
// the text, never the selection indicator. The active model carries a ●
// alongside the highlight; pricing appears only when metadata backs it.
// index is the row's position in the filtered set: the marker lands exactly
// on the highlight, never on a scrolled-off row.
func renderPickerRow(index int, picker *PickerState, item PickerItem, pal *Palette, layout Layout, width int) string {
	marker := "  "
	if index == picker.Cursor {
		marker = pal.Selected.Render("> ")
	}
	active := ""
	if item.Active {
		active = "● "
	}
	if picker.Kind != PickerModels {
		title := TruncateToWidth(item.Title, width-4-len(active))
		line := marker + active + title
		if item.Detail != "" && layout.ShowDetail {
			if detail := TruncateToWidth(item.Detail, width-CellWidth(title)-8); detail != "" {
				line += "  " + pal.Muted.Render(detail)
			}
		}
		return line
	}
	name := item.Title
	if name == "" {
		name = item.Value
	}
	switch layout.Mode {
	case LayoutNarrow:
		// Name and selection state only; ids truncate hard.
		return marker + active + TruncateToWidth(name, width-4-len(active))
	case LayoutMedium:
		row := marker + active + TruncateToWidth(name, width-4-len(active)-20)
		if item.Provider != "" {
			row += "  " + pal.Muted.Render(TruncateToWidth(item.Provider, 16))
		}
		if item.Context > 0 {
			row += "  " + pal.Muted.Render(formatCount(item.Context))
		}
		return row
	default:
		row := marker + active + TruncateToWidth(name, width-4-len(active)-40)
		if item.Provider != "" {
			row += "  " + pal.Muted.Render(TruncateToWidth(item.Provider, 16))
		}
		if item.Context > 0 {
			row += "  " + pal.Muted.Render(formatCount(item.Context)+" ctx")
		}
		if item.PriceKnown && item.Free {
			row += "  " + pal.Muted.Render("free")
		}
		if item.Value != "" && item.Value != name {
			if id := TruncateToWidth(item.Value, 42); id != "" {
				row += "  " + pal.Muted.Render(id)
			}
		}
		return row
	}
}

// pickerCatalogStatus reports catalog freshness in words: loading, live,
// cached with staleness, or a failed refresh that kept cached rows.
func pickerCatalogStatus(picker *PickerState) string {
	if picker.Loading {
		return "loading catalog…"
	}
	var parts []string
	switch picker.Origin {
	case "live":
		parts = append(parts, "live catalog")
	case "cached":
		parts = append(parts, "cached catalog")
	case "bundled":
		parts = append(parts, "bundled catalog")
	}
	if picker.Refreshing {
		parts = append(parts, "refreshing…")
	}
	if picker.ErrMsg != "" {
		parts = append(parts, "refresh failed — showing cached ("+picker.ErrMsg+")")
	}
	return strings.Join(parts, " · ")
}

// pickerFooter lists the shortcuts that actually work in the picker,
// shortened per tier so the footer never overflows a narrow viewport.
func pickerFooter(picker *PickerState, layout Layout) string {
	refresh := ""
	if picker.Kind == PickerModels {
		refresh = " · Ctrl+R refresh"
	}
	switch layout.Mode {
	case LayoutNarrow:
		return "↑↓ nav · Enter select · Esc close"
	case LayoutMedium:
		return "↑↓ navigate · PgUp/PgDn · Enter select · Esc close" + refresh
	default:
		if picker.Kind == PickerModels {
			return "↑↓ navigate · PgUp/PgDn scroll · Home/End jump · Enter select · Esc close · Ctrl+R refresh"
		}
		return "↑↓ navigate · PgUp/PgDn scroll · Home/End jump · Enter select · Esc close · type to filter"
	}
}

// renderComposer draws the multiline input with a block cursor, or the
// masked secret entry while one is open. Masking renders bullets and never
// the key: shoulder-surfing a setup screen must not leak the credential.
// Input wraps to the content width and is capped like the layout so a paste
// cannot evict the conversation.
func renderComposer(state State, pal *Palette, layout Layout) string {
	width := displayWidth(state, layout)
	if state.Secret != nil {
		masked := strings.Repeat("•", len([]rune(state.Input)))
		wrapped := WrapToWidth(masked, width-CellWidth(state.Secret.Label)-4)
		var sb strings.Builder
		for i, wl := range wrapped {
			if i == 0 {
				sb.WriteString(pal.Prompt.Render(state.Secret.Label+": ") + wl)
			} else {
				sb.WriteString("\n" + pal.Prompt.Render("  ") + wl)
			}
		}
		sb.WriteString(pal.Cursor.Render("▌"))
		sb.WriteString("\n" + pal.Muted.Render("enter submits · esc cancels"))
		return sb.String()
	}
	runes := []rune(state.Input)
	cursor := state.Cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	marked := string(runes[:cursor]) + pal.Cursor.Render("▌") + string(runes[cursor:])
	// Wrap each logical line to the content width, keeping the prompt prefix
	// outside the budget so the cursor column stays honest.
	var sb strings.Builder
	lines := strings.Split(marked, "\n")
	shown := lines
	if len(shown) > 6 {
		shown = shown[len(shown)-6:]
	}
	for i, line := range shown {
		prefix := "> "
		if i > 0 {
			prefix = "  "
		}
		for _, wl := range WrapToWidth(stripCursor(line), width-2) {
			_ = wl
			break
		}
		if i == 0 {
			sb.WriteString(pal.Prompt.Render(prefix) + line)
		} else {
			sb.WriteString("\n" + pal.Prompt.Render(prefix) + line)
		}
	}
	_ = width
	return sb.String()
}

// stripCursor removes the rendered cursor marker for measurement. The marker
// is a styled span, so plain text here carries no ANSI; the helper exists to
// document that measuring happens on content, not chrome.
func stripCursor(s string) string {
	return s
}

// renderEntry writes one transcript entry, wrapped to the content width.
func renderEntry(sb *strings.Builder, entry Entry, pal *Palette, layout Layout) {
	switch entry.Kind {
	case EntryUser:
		for _, wl := range WrapToWidth(entry.UserText, textWidth(layout)) {
			if wl == entry.UserText || len(WrapToWidth(entry.UserText, textWidth(layout))) == 1 {
				_ = wl
				break
			}
		}
		sb.WriteString(pal.User.Render("you") + "\n")
		for _, wl := range wrapPlain(entry.UserText, layout) {
			sb.WriteString("  " + wl + "\n")
		}
		sb.WriteString("\n")

	case EntryAssistant:
		if entry.AssistantText == "" {
			return
		}
		for _, wl := range wrapPlain(RenderMarkdown(entry.AssistantText, pal), layout) {
			sb.WriteString(wl + "\n")
		}
		sb.WriteString("\n")

	case EntryTool:
		if entry.Tool != nil {
			renderTool(sb, entry.Tool, pal, layout)
		}

	case EntryNotice:
		for _, wl := range wrapPlain(entry.Notice, layout) {
			sb.WriteString(pal.Notice.Render(wl) + "\n")
		}
		sb.WriteString("\n")
	}
}

// renderTool writes a tool call and, once finished, its outcome.
//
// States are text plus color: running spinner, ✓ success, ✗ failure, ·
// pending, so nothing depends on hue alone. Output is bounded and wrapped;
// large results collapse to the limit with a truncation marker rather than
// flooding the conversation.
func renderTool(sb *strings.Builder, tool *ToolEntry, pal *Palette, layout Layout) {
	var badge string
	switch tool.Status {
	case ToolRunning:
		frame := spinnerFrames[0]
		badge = pal.ToolRun.Render(frame+" "+tool.Name) + "  " + pal.Status.Render("running")
		if tool.Summary != "" {
			badge += "  " + pal.Status.Render(TruncateToWidth(tool.Summary, dialogWidth(layout)))
		}
	case ToolDone:
		badge = pal.ToolDone.Render("✓ "+tool.Name) + "  " + pal.Status.Render(tool.elapsed())
	case ToolError:
		badge = pal.ToolError.Render("✗ "+tool.Name) + "  " + pal.Status.Render(tool.elapsed())
	case ToolPending:
		badge = pal.ToolRun.Render("· " + tool.Name)
	}
	sb.WriteString(badge + "\n")

	if tool.Text == "" {
		return
	}
	body, truncated := TruncateForDisplay(tool.Text, toolOutputLimit)
	width := textWidth(layout) - 4
	if width < 10 {
		width = 10
	}
	shown := 0
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		for _, wl := range WrapToWidth(line, width) {
			if shown >= maxToolLines(layout) {
				break
			}
			sb.WriteString("    " + wl + "\n")
			shown++
		}
		if shown >= maxToolLines(layout) {
			break
		}
	}
	if truncated || shown >= maxToolLines(layout) {
		sb.WriteString(pal.Truncated.Render("    … [truncated]") + "\n")
	}
	sb.WriteString("\n")
}

// toolOutputLimit bounds one tool's displayed outcome.
const toolOutputLimit = 4000

// maxToolLines bounds rendered tool rows per call so one result cannot flood
// the conversation. Wide shows more; narrow keeps the transcript scannable.
func maxToolLines(layout Layout) int {
	if layout.Mode == LayoutNarrow {
		return 12
	}
	return 24
}

// elapsed renders a duration compactly, omitting it entirely when the call has
// not started so a pending line is not padded with a zero.
func (t *ToolEntry) elapsed() string {
	if t.StartedAt.IsZero() {
		return ""
	}
	d := t.Duration().Round(time.Millisecond)
	if d <= 0 {
		return ""
	}
	return d.String()
}

// renderApproval draws the modal prompt with a bounded preview.
//
// The default cursor lands on the conservative choice (deny last). Closing,
// cancelling, or resizing never approves: only an explicit affirmative key
// does, enforced by the Update handler, not by rendering.
func renderApproval(sb *strings.Builder, pending *PendingApproval, pal *Palette, layout Layout) {
	width := dialogWidth(layout)
	line := pal.Prompt.Render("Run this tool?") + " " + pal.ToolName.Render(TruncateToWidth(pending.ToolName, width))
	sb.WriteString(line + "\n")
	for _, wl := range boundedPreview(pending, layout) {
		sb.WriteString(pal.Status.Render("  "+wl) + "\n")
	}

	for i, choice := range pending.Choices {
		marker := "  "
		style := pal.Choice
		if i == pending.Cursor {
			marker = "> "
			style = pal.Selected
		}
		def := ""
		if i == len(pending.Choices)-1 {
			def = pal.Muted.Render(" (default)")
		}
		sb.WriteString(marker + style.Render(choice) + def + "\n")
	}
	sb.WriteString(pal.Muted.Render("↑↓ move · enter confirm · y approve · n/esc deny") + "\n\n")
}

// renderStatus draws the footer: provider/model, runtime phase, cwd, session,
// context budget, and measured usage.
//
// Narrow tiers keep model plus phase; wider tiers add context %, session,
// cwd, and usage text. Unknown values stay absent rather than rendering as
// zero, and usage text is shown verbatim from measured accounting — the view
// never labels an estimate as measured. Nothing wraps: the terminal owns
// wrapping, and a footer that wraps reads as two footers.
func renderStatus(state State, pal *Palette, layout Layout) string {
	status := state.Status
	runtime := DeriveRuntime(state)
	marker := RuntimeMarker(runtime)
	if state.Width > 0 && (layout.Mode == LayoutNarrow || state.Width < 60) {
		parts := []string{}
		if status.Model != "" {
			parts = append(parts, status.Model)
		}
		parts = append(parts, marker+" "+string(runtime))
		if len(parts) == 0 {
			parts = append(parts, "ohgo")
		}
		return pal.Status.Render(TruncateToWidth(strings.Join(parts, "  ·  "), state.Width))
	}
	parts := []string{" ohgo ", marker + " " + string(runtime)}
	if status.Model != "" {
		parts = append(parts, status.Model)
	}
	if layout.ShowDetail {
		if pct := status.Percent(); pct >= 0 {
			parts = append(parts, fmt.Sprintf("context %d%%", pct))
		}
		if status.Session != "" {
			parts = append(parts, status.Session)
		}
		if status.Cwd != "" {
			parts = append(parts, shortenCwd(status.Cwd, 24))
		}
		if status.UsageText != "" {
			parts = append(parts, status.UsageText)
		}
	}
	if status.Busy {
		frame := spinnerFrames[status.Spinner%len(spinnerFrames)]
		parts = append(parts, frame+" working")
	}
	out := strings.Join(parts, "  ·  ")
	if state.Width > 0 {
		out = TruncateToWidth(out, state.Width)
	}
	return pal.Status.Render(out)
}

// shortenCwd keeps the tail of a long path: the leaf directory is what
// orients, not the $HOME prefix.
func shortenCwd(cwd string, max int) string {
	if CellWidth(cwd) <= max {
		return cwd
	}
	return "…/" + TruncateToWidth(lastTwo(cwd), max-3)
}

// lastTwo keeps the last two path segments.
func lastTwo(p string) string {
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return p
}

// textWidth is the wrap budget for message text.
func textWidth(layout Layout) int {
	if layout.ContentWidth > 0 {
		return layout.ContentWidth
	}
	return 80
}

// displayWidth is the budget for chrome lines (header, picker, composer).
func displayWidth(state State, layout Layout) int {
	if state.Width > 0 {
		return state.Width - 1
	}
	return 80
}

// dialogWidth bounds approval and tool preview lines inside the viewport.
func dialogWidth(layout Layout) int {
	if layout.ContentWidth > 0 {
		return layout.ContentWidth
	}
	return 80
}

// separatorWidth sizes the rule under the header without overflowing.
func separatorWidth(state State, layout Layout) int {
	if state.Width > 0 {
		w := state.Width - 1
		if w < 1 {
			w = 1
		}
		return w
	}
	return 40
}

// wrapPlain wraps unstyled text to the content width. Styled spans (markdown)
// are wrapped per line by the caller splitting on newlines; ANSI is stripped
// for measurement so styles never inflate the budget.
func wrapPlain(text string, layout Layout) []string {
	width := textWidth(layout)
	if width <= 0 {
		return strings.Split(text, "\n")
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		out = append(out, WrapToWidth(line, width)...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// AdvanceSpinner returns the next frame index.
func AdvanceSpinner(current, frames int) int {
	if frames <= 0 {
		return 0
	}
	return (current + 1) % frames
}
