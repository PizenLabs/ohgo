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
	UserBG    lipgloss.Style
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
	// Heading2 and Heading3 stratify the header hierarchy below Heading
	// (H1): H2 is sage, H3 muted sage, so depth reads without parsing.
	Heading2 lipgloss.Style
	Heading3 lipgloss.Style
	// Code token styles highlight fenced code lexically. Every token
	// carries the same background as Code so a highlighted line keeps one
	// uniform block; the foreground alone signals the token kind.
	CodeKeyword lipgloss.Style
	CodeString  lipgloss.Style
	CodeComment lipgloss.Style
	CodeNumber  lipgloss.Style
	Bold        lipgloss.Style
	Italic      lipgloss.Style
	Quote       lipgloss.Style
	Bullet      lipgloss.Style
	Muted       lipgloss.Style
	// SupportHi highlights key entities inside the $sup answer viewport:
	// bright cyan for concepts/technologies, amber for dates/specs.
	// Main-chat markdown never uses them, so the overlay hierarchy stays
	// distinct without changing the transcript.
	SupportHi    lipgloss.Style
	SupportHiAlt lipgloss.Style
}

// DarkPalette is the default: muted surfaces with a single mint accent for
// the user's voice and focus, primary text for the assistant. Headers step
// down a deep-forest scale — bright emerald H1, sage H2, muted sage H3 —
// and code tokens reuse the same scale so prose and code read as one theme.
// The question keeps a faint background block instead of a label prefix so
// it reads apart from the answer.
func DarkPalette() *Palette {
	return &Palette{
		User:         fg(darkAccent).Bold(true),
		UserBG:       fg(darkText).Background(lipgloss.Color(darkUserBg)),
		Assistant:    fg(darkText),
		ToolName:     fg(darkTool).Bold(true),
		ToolDone:     fg(darkSuccess),
		ToolError:    fg(darkDanger),
		ToolRun:      fg(darkWarning),
		Notice:       fg(darkMuted).Italic(true),
		Status:       fg(darkMuted),
		Prompt:       fg(darkAccent),
		Choice:       fg(darkText),
		Selected:     fg(darkAccent).Bold(true),
		Truncated:    fg(darkMuted),
		Cursor:       fg(darkAccent).Reverse(true),
		Queued:       fg(darkMuted).Italic(true),
		Code:         fg(darkCodeFg).Background(lipgloss.Color(darkCodeBg)),
		CodeFence:    fg(darkMuted),
		Heading:      fg(darkH1).Bold(true),
		Heading2:     fg(darkH2).Bold(true),
		Heading3:     fg(darkH3).Bold(true),
		CodeKeyword:  fg(darkH1).Background(lipgloss.Color(darkCodeBg)).Bold(true),
		CodeString:   fg(darkCodeStr).Background(lipgloss.Color(darkCodeBg)),
		CodeComment:  fg(darkMuted).Background(lipgloss.Color(darkCodeBg)).Italic(true),
		CodeNumber:   fg(darkTool).Background(lipgloss.Color(darkCodeBg)),
		Bold:         lipgloss.NewStyle().Bold(true),
		Italic:       lipgloss.NewStyle().Italic(true),
		Quote:        fg(darkMuted).Italic(true),
		Bullet:       fg(darkAccent),
		Muted:        fg(darkMuted),
		SupportHi:    fg("87").Bold(true),
		SupportHiAlt: fg("220").Bold(true),
	}
}

// LightPalette reads on bright terminals: dark foregrounds, no heavy
// backgrounds that would clash with a light screen, the same semantics.
// The question keeps its faint background block here as well.
func LightPalette() *Palette {
	return &Palette{
		User:         fg(lightAccent).Bold(true),
		UserBG:       fg(lightText).Background(lipgloss.Color(lightUserBg)),
		Assistant:    fg(lightText),
		ToolName:     fg(lightTool).Bold(true),
		ToolDone:     fg(lightSuccess),
		ToolError:    fg(lightDanger),
		ToolRun:      fg(lightWarning),
		Notice:       fg(lightMuted).Italic(true),
		Status:       fg(lightMuted),
		Prompt:       fg(lightAccent),
		Choice:       fg(lightText),
		Selected:     fg(lightAccent).Bold(true),
		Truncated:    fg(lightMuted),
		Cursor:       fg(lightAccent).Reverse(true),
		Queued:       fg(lightMuted).Italic(true),
		Code:         fg(lightCodeFg).Background(lipgloss.Color(lightCodeBg)),
		CodeFence:    fg(lightMuted),
		Heading:      fg(lightH1).Bold(true),
		Heading2:     fg(lightH2).Bold(true),
		Heading3:     fg(lightH3).Bold(true),
		CodeKeyword:  fg(lightH1).Background(lipgloss.Color(lightCodeBg)).Bold(true),
		CodeString:   fg(lightCodeStr).Background(lipgloss.Color(lightCodeBg)),
		CodeComment:  fg(lightMuted).Background(lipgloss.Color(lightCodeBg)).Italic(true),
		CodeNumber:   fg(lightTool).Background(lipgloss.Color(lightCodeBg)),
		Bold:         lipgloss.NewStyle().Bold(true),
		Italic:       lipgloss.NewStyle().Italic(true),
		Quote:        fg(lightMuted).Italic(true),
		Bullet:       fg(lightAccent),
		Muted:        fg(lightMuted),
		SupportHi:    fg("30").Bold(true),
		SupportHiAlt: fg("166").Bold(true),
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

	if state.Plane != nil {
		renderPlaneArea(&sb, state, pal, layout)
	} else if state.SupportPanel != nil {
		renderSupportArea(&sb, state, pal, layout)
	} else if state.Picker != nil {
		renderPickerArea(&sb, state, pal, layout)
	} else {
		renderTranscriptArea(&sb, state, pal, layout)
	}

	if state.Pending != nil {
		renderApproval(&sb, state.Pending, pal, layout)
	}

	sb.WriteString(renderStatus(state, pal, layout))
	if state.Picker == nil && state.Plane == nil && state.SupportPanel == nil {
		sb.WriteString("\n")
		sb.WriteString(renderComposer(state, pal, layout))
		if layout.ShowHints {
			sb.WriteString("\n" + pal.Muted.Render(composerHintsFor(state)))
		}
	} else if state.SupportPanel != nil && state.Plane == nil && state.Picker == nil {
		// The support overlay owns interaction while open: the main
		// composer hides so a normal workspace turn cannot be submitted
		// underneath it.
		if layout.ShowHints {
			sb.WriteString("\n" + pal.Muted.Render("support owns input · Enter submit · Esc close"))
		}
	}
	return sb.String()
}

// composerHintsFor picks the hint row for the current interaction: slash
// navigation while discovering commands, the busy set while a turn runs,
// the idle set otherwise. Every hint shown must work.
//
// An open approval owns the interaction, so no composer hint is produced: the
// dialog has captured enter, esc, and the scroll keys, and advertising them
// here would contradict the key line it prints instead. ComputeLayout drops
// the row in the same case, so the frame keeps its budget either way.
func composerHintsFor(state State) string {
	if state.Pending != nil {
		return ""
	}
	if !state.SlashHide && len(SlashSuggestions(state.Input)) > 0 && state.Secret == nil {
		return "↑↓ navigate · enter accept · esc dismiss · tab palette"
	}
	return ComposerHints(state.Status.Busy)
}

// renderMini concedes a viewport too small for the workbench. It keeps the
// composer text and the model visible in prose so nothing is silently lost,
// and tells the user the useful action: enlarge the terminal.
func renderMini(state State, pal *Palette, layout Layout) string {
	// Every line fits the viewport: mini screens are where an overlong row
	// hurts most, so each line is truncated plain and styled after.
	fit := func(s string) string {
		if state.Width > 0 && CellWidth(s) > state.Width {
			return TruncateToWidth(s, state.Width)
		}
		return s
	}
	var sb strings.Builder
	sb.WriteString(pal.Heading.Render(fit("ohgo — terminal too small")) + "\n")
	sb.WriteString(pal.Muted.Render(fit(fmt.Sprintf("%dx%d: enlarge to at least %dx%d for the workbench.",
		state.Width, state.Height, MiniMinWidth, MiniMinHeight))) + "\n")
	if state.Status.Model != "" {
		sb.WriteString(pal.Status.Render(fit(sanitizeInline(state.Status.Model))) + "\n")
	}
	if strings.TrimSpace(state.Input) != "" {
		budget := state.Width - CellWidth("> ")
		if budget < 1 {
			budget = 1
		}
		sb.WriteString(pal.Prompt.Render("> ") + TruncateToWidth(sanitizeInline(strings.TrimSpace(state.Input)), budget) + "\n")
	}
	sb.WriteString(pal.Muted.Render(fit("your input and history are preserved.")) + "\n")
	return sb.String()
}

// renderHeader draws product identity, the current model, the runtime phase,
// and the connection state. It is the only place identity and phase appear:
// the status line below carries workspace context, never a second copy.
//
// Wide shows everything; medium drops the connection detail to its state
// word; narrow keeps identity, model, and phase only. Connection is words, not
// color alone: a dot nobody can decode is decoration, not information.
func renderHeader(state State, pal *Palette, layout Layout) string {
	ex := ExtrasFor(state.Theme)
	runtime := DeriveRuntime(state)
	marker := RuntimeMarker(runtime)
	// Model ids and probe details arrive from catalogs and networks:
	// untrusted, sanitized inline. The header is one row by contract, so
	// the composed line is truncated to the viewport — an overlong model
	// name must clip, never soft-wrap the terminal into an extra row.
	model := sanitizeInline(state.Status.Model)
	if model == "" {
		model = "no model"
	}
	var text string
	switch layout.Mode {
	case LayoutNarrow:
		text = fmt.Sprintf("ohgo %s %s · %s", marker, model, string(runtime))
	case LayoutMedium:
		conn := "not checked"
		if state.ConnDetail != "" {
			if state.ConnOK {
				conn = "connected"
			} else {
				conn = "not connected"
			}
		}
		text = fmt.Sprintf("ohgo %s %s · %s · %s", marker, model, string(runtime), conn)
	default:
		conn := "○ not checked"
		if state.ConnDetail != "" {
			detail := sanitizeInline(state.ConnDetail)
			if state.ConnOK {
				conn = "● " + detail
			} else {
				conn = "○ " + detail
			}
		}
		text = fmt.Sprintf("ohgo %s %s · %s · %s", marker, model, string(runtime), conn)
	}
	if state.Width > 0 {
		text = TruncateToWidth(text, displayWidth(state, layout))
	}
	return ex.Header.Render(text)
}

// renderTranscriptArea writes the transcript, wrapped to the content width
// and sliced to the conversation height when the size is known.
//
// The region always fills its budget: short content sits at the top with the
// remaining rows left blank below it, so the composer is pinned to the bottom
// of the screen instead of floating up under a short conversation. Overflow is
// trimmed from the top, keeping the live edge visible.
func renderTranscriptArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	body := renderTranscriptBody(state, pal, layout)
	if body == "" && showWelcome(state) {
		body = renderWelcome(state, pal, layout)
	}
	lines := splitLines(body)
	if state.Height <= 0 || layout.Mode == LayoutWide && state.Width <= 0 {
		sb.WriteString(body)
		if body != "" && !strings.HasSuffix(body, "\n") {
			sb.WriteString("\n")
		}
		return
	}
	avail := layout.ConversationHeight
	// An approval dialog claims rows inside the conversation budget so
	// regions never overlap. Queued prompts live in the composer, never
	// here: the transcript holds conversation history, interaction state
	// belongs to the pinned chrome.
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	// The transcript yields entirely to modal pressure on tiny viewports:
	// a zero-row transcript plus a visible dialog beats an overflowing one.
	if avail < 0 {
		avail = 0
	}
	// The region emits exactly avail rows: entry separators are blank lines
	// inside the row budget, not extra rows past it. Writing the raw body
	// plus padding would double-count them and push chrome off-screen.
	if len(lines) <= avail {
		for _, line := range lines {
			sb.WriteString(line + "\n")
		}
		// Pad below so the composer stays at the bottom of the viewport.
		for i := len(lines); i < avail; i++ {
			sb.WriteString("\n")
		}
		return
	}
	// The scroll offset is clamped to the content: an offset past the top
	// would emit fewer than avail rows and unpin the composer from the
	// bottom of the viewport, and the marker must count lines actually
	// above rather than the raw offset. Render stays pure — the stored
	// offset is untouched, so scrolling back down still moves.
	scroll := state.Scroll
	if max := len(lines) - avail; scroll > max {
		scroll = max
	}
	if scroll > 0 && avail > 0 {
		sb.WriteString(pal.Muted.Render(fmt.Sprintf("↑ %d lines above — end to follow", scroll)) + "\n")
		avail--
	}
	start := len(lines) - avail - scroll
	if start < 0 {
		start = 0
	}
	end := len(lines) - scroll
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

// showWelcome reports whether the empty-conversation introduction belongs.
//
// It is derived, never stored: the welcome is chrome for "nothing has happened
// yet", so it appears exactly when the transcript is empty and vanishes the
// moment real content lands or an overlay owns the screen. Because it is only
// ever rendered — never applied as an Entry — it cannot leak into the
// persisted session or reappear over a resumed transcript.
func showWelcome(state State) bool {
	return len(state.Entries) == 0 && state.Picker == nil && state.Secret == nil && state.Pending == nil
}

// renderTranscriptBody renders every entry wrapped to the content width.
func renderTranscriptBody(state State, pal *Palette, layout Layout) string {
	var body strings.Builder
	frame := spinnerFrame(state.Status.Spinner)
	for _, entry := range state.Entries {
		renderEntry(&body, entry, pal, layout, frame)
	}
	return body.String()
}

// approvalLines counts the rendered approval block, bounded to the viewport.
func approvalLines(pending *PendingApproval, layout Layout) int {
	detail := len(boundedPreview(pending, layout))
	// prompt + preview + choices + hints, plus the blank separator when it
	// fits (see approvalBlank). Every row must be claimed here or the total
	// exceeds the viewport.
	return 1 + detail + len(pending.Choices) + 1 + approvalBlankRows(pending, layout)
}

// approvalBlankRows is 1 when the separator blank after the dialog fits and
// 0 when yielding it keeps the choices on screen. Render and accounting
// share it so the reserved rows and the rendered rows always agree.
func approvalBlankRows(pending *PendingApproval, layout Layout) int {
	if approvalBlank(pending, layout) {
		return 1
	}
	return 0
}

// approvalBlank reports whether the separator blank after the dialog fits.
// On viewports too short for the chrome plus the dialog minimum it yields,
// so the choices stay on screen instead of the spacing.
func approvalBlank(pending *PendingApproval, layout Layout) bool {
	if layout.Height <= 0 {
		return true
	}
	hints := 0
	if layout.ShowHints {
		hints = 1
	}
	// header + prompt + one preview row + choices + dialog hints + blank +
	// status + composer + layout hints + one transcript row.
	return layout.Height >= len(pending.Choices)+layout.ComposerLines+hints+8
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
		// Approval text echoes tool arguments: untrusted, sanitized before
		// wrapping so measurement matches terminal layout.
		lines = append(lines, WrapToWidth(sanitizeInline(text), width)...)
	}
	max := len(lines)
	if layout.Height > 0 {
		// The preview gets whatever the chrome does not claim: header,
		// status, the composer block, the prompt/choices/hint rows, and
		// the blank separator. Deriving it from the computed layout
		// keeps the dialog bounded on short viewports instead of
		// overflowing them.
		max = layout.Height - len(pending.Choices) - layout.HeaderLines - layout.StatusLines - layout.ComposerLines - 3
		if max < 1 {
			max = 1
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
	// A one-row budget shows the first row alone: the counted marker would
	// cost the very row it complains about. Choices stay fully visible,
	// which is the safety-critical part of the dialog.
	if max <= 1 {
		return lines[:1]
	}
	kept := append([]string(nil), lines[:max]...)
	kept = append(kept, fmt.Sprintf("… %d more lines", len(lines)-max))
	return kept
}

// renderWelcome draws the first-use introduction for an empty conversation.
//
// It is deliberately short and never persisted: a few lines that name the
// product, invite a task, and offer at most three examples of real work. It
// collapses to a two-line form on narrow terminals so it cannot crowd out the
// composer, and it teaches nothing that /help and /commands do not already
// cover — command discovery stays in one place.
func renderWelcome(state State, pal *Palette, layout Layout) string {
	ex := ExtrasFor(state.Theme)
	var sb strings.Builder
	sb.WriteString(pal.Heading.Render("ohgo") + " " + ex.Hint.Render("· a coding agent for real codebases") + "\n")
	sb.WriteString(ex.Hint.Render("Describe a task to begin.") + "\n")
	if layout.Mode == LayoutNarrow || layout.Mode == LayoutMini {
		sb.WriteString(ex.Hint.Render("Type /help for commands.") + "\n")
		return sb.String()
	}
	sb.WriteString("\n")
	sb.WriteString(ex.Hint.Render("Try:") + "\n")
	for _, example := range welcomeExamples {
		sb.WriteString("  " + pal.Bullet.Render("•") + " " + ex.Hint.Render(example) + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString(ex.Hint.Render("Type /help for commands · /model to switch models.") + "\n")
	return sb.String()
}

// welcomeExamples are the at-most-three concrete tasks the first-use screen
// offers. They are kept short so even a medium terminal fits them without
// pushing the composer off screen.
var welcomeExamples = []string{
	"understand how this codebase is organised",
	"implement a change and run the tests",
	"investigate why a command or test is failing",
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
//
// Like the transcript, the picker fills its budget exactly: short result
// lists pad below so the status and composer stay pinned, and overflow
// trims item rows while keeping the header, search, and footer visible.
func renderPickerArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	picker := state.Picker
	width := displayWidth(state, layout)
	header := pal.Heading.Render(TruncateToWidth(pickerHeader(picker), width))
	search := renderPickerSearch(picker, pal, width)
	footer := pal.Muted.Render(TruncateToWidth(pickerFooter(picker, layout), width))

	var middle []string
	filtered := picker.Filtered()
	window, start := picker.windowOf(filtered)
	if picker.Loading && len(window) == 0 {
		middle = append(middle, pal.Muted.Render("  loading catalog…"))
	} else if len(window) == 0 {
		middle = append(middle, pal.Muted.Render("  (no matches — esc clears the search)"))
	} else {
		for i, item := range window {
			row := renderPickerRow(start+i, picker, item, pal, layout, width)
			middle = append(middle, WrapToWidth(row, width)...)
		}
		if hidden := len(filtered) - start - len(window); hidden > 0 {
			middle = append(middle, pal.Muted.Render(fmt.Sprintf("  … %d more", hidden)))
		}
	}
	if picker.Kind == PickerModels {
		if status := pickerCatalogStatus(picker); status != "" {
			middle = append(middle, pal.Muted.Render(TruncateToWidth(status, width)))
		}
	}

	// Unknown size renders everything: only a known viewport may slice.
	if state.Height <= 0 || layout.Mode == LayoutWide && state.Width <= 0 {
		sb.WriteString(header + "\n")
		sb.WriteString(search + "\n")
		for _, line := range middle {
			sb.WriteString(line + "\n")
		}
		sb.WriteString(footer + "\n")
		return
	}
	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	// Header, search, and footer are never trimmed: overflow yields item
	// rows from the bottom, keeping navigation context on screen. Below a
	// zero budget even those overflow, but that corner needs a modal pileup
	// on a sub-10-row screen to exist at all.
	budget := avail - 3
	if budget < 0 {
		budget = 0
	}
	if len(middle) > budget {
		middle = middle[:budget]
	}
	sb.WriteString(header + "\n")
	sb.WriteString(search + "\n")
	for _, line := range middle {
		sb.WriteString(line + "\n")
	}
	for i := 3 + len(middle); i < avail; i++ {
		sb.WriteString("\n")
	}
	sb.WriteString(footer + "\n")
}

// pickerHeader names the overlay, the provider scope, the result count, and
// any loading state: "Models · All providers · 215 results".
func pickerHeader(picker *PickerState) string {
	scope := "All providers"
	if provider := picker.ProviderScope(); provider != "" {
		scope = "provider:" + sanitizeInline(provider)
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
	runes := []rune(sanitizeInline(picker.Filter))
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
	// Picker rows replay catalogs, session titles, and job commands: all
	// external text, sanitized inline before measuring so a control
	// sequence can never split a row or move the cursor.
	item.Title = sanitizeInline(item.Title)
	item.Detail = sanitizeInline(item.Detail)
	item.Value = sanitizeInline(item.Value)
	item.Name = sanitizeInline(item.Name)
	item.Provider = sanitizeInline(item.Provider)
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
		parts = append(parts, "refresh failed — showing cached ("+sanitizeInline(picker.ErrMsg)+")")
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

// renderComposer draws the multiline input framed by two restrained
// horizontal rules, or the masked secret entry while one is open. Masking
// renders bullets and never the key: shoulder-surfing a setup screen must
// not leak the credential.
//
// The visual hierarchy is deliberate: editable text and cursor first, the
// interaction state second, contextual suggestions when relevant, hints
// only when space permits. The rules stay quiet when idle (muted) and turn
// to the focus accent while the composer owns the keyboard, so focus is
// unmistakable without permanent decoration.
//
// Input is hard-wrapped to the content budget cell-by-cell (never
// byte-sliced, and never folded on whitespace) so a long line stays within
// the viewport and the cursor lands exactly where typing goes. It is capped
// at the layout's own row budget from the tail, so a paste cannot evict the
// conversation.
func renderComposer(state State, pal *Palette, layout Layout) string {
	rule := composerRule(state, pal, layout)
	if state.Secret != nil {
		wrapped := secretMaskedRows(state.Secret.Label, state.Input, composerWrapWidth(layout))
		// A pasted credential can be arbitrarily long: show the tail so the
		// masked block can never push chrome off-screen. Only bullets ever
		// render — content stays masked on every row.
		if len(wrapped) > maxComposerRows {
			wrapped = wrapped[len(wrapped)-maxComposerRows:]
		}
		var sb strings.Builder
		sb.WriteString(rule + "\n")
		for i, wl := range wrapped {
			if i == 0 {
				sb.WriteString(pal.Prompt.Render(state.Secret.Label+": ") + wl)
			} else {
				sb.WriteString("\n" + pal.Prompt.Render("  ") + wl)
			}
		}
		sb.WriteString(pal.Cursor.Render("▌"))
		sb.WriteString("\n" + rule)
		sb.WriteString("\n" + pal.Muted.Render("enter submits · esc cancels"))
		return sb.String()
	}

	// Pasted input can carry terminal escapes: sanitize for display (cursor
	// mapping preserved) while the stored input stays raw for editing.
	// Layout reserves from the same sanitized text — see composerHeight.
	clean, cleanCursor := sanitizeWithCursor(state.Input, state.Cursor)
	rows, cursorRow, cursorRune, haveCursor := layoutComposer(clean, cleanCursor, composerWrapWidth(layout))

	start := 0
	if len(rows) > maxComposerRows {
		start = len(rows) - maxComposerRows
	}
	shown := rows[start:]

	var sb strings.Builder
	sb.WriteString(rule + "\n")
	for i, row := range shown {
		text := row
		if haveCursor && start+i == cursorRow {
			text = insertAt(row, cursorRune, pal.Cursor.Render("▌"))
		}
		prefix := "  "
		if i == 0 {
			prefix = "> "
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(pal.Prompt.Render(prefix) + text)
	}
	if suggest := renderSlashSuggestions(state, pal, layout); suggest != "" {
		sb.WriteString("\n" + suggest)
	}
	if status := composerStateLine(state, pal, layout); status != "" {
		sb.WriteString("\n" + status)
	}
	sb.WriteString("\n" + rule)
	return sb.String()
}

// composerRule draws one restrained horizontal rule framing the composer.
// The accent marks that the composer owns the keyboard; any overlay,
// approval, or secret entry quiets it so attention goes to the modal state.
func composerRule(state State, pal *Palette, layout Layout) string {
	width := separatorWidth(state, layout)
	if width < 1 {
		width = 1
	}
	line := strings.Repeat("─", width)
	if state.Picker != nil || state.Secret != nil || state.Pending != nil {
		return pal.Muted.Render(line)
	}
	return ExtrasFor(state.Theme).Focus.Render(line)
}

// renderSlashSuggestions draws the compact inline command picker for a
// leading "/". It is built from the authoritative registry on every render,
// shows short descriptions, filters as the user types, caps its rows, and
// collapses descriptions on narrow terminals — never a second catalog.
func renderSlashSuggestions(state State, pal *Palette, layout Layout) string {
	shown, start, cursor, above, below := slashWindow(state)
	if len(shown) == 0 {
		return ""
	}
	width := displayWidth(state, layout)
	matches := SlashSuggestions(state.Input)
	// Fit the reserved budget: the window trims item rows from the bottom
	// while the "more" marker absorbs the trimmed count, staying truthful
	// about what is hidden. Negative budget means uncapped (unknown size).
	items := append([]CommandSpec(nil), shown...)
	hideAbove := above
	hideBelow := below
	if layout.SlashLines >= 0 {
		budget := layout.SlashLines
		if budget == 0 {
			return ""
		}
		for {
			hideBelow = len(matches)-start-len(items) > 0
			if len(items)+slashGroupHeaders(items)+one(hideAbove)+one(hideBelow) <= budget {
				break
			}
			if len(items) > 0 {
				items = items[:len(items)-1]
				continue
			}
			// Markers alone overflow the budget: keep the counted
			// below-marker over the uncounted above-marker.
			hideAbove = false
		}
	}
	var rows []string
	if hideAbove {
		rows = append(rows, pal.Muted.Render("  … more"))
	}
	current := strings.ToUpper(strings.TrimSpace(state.Status.Workspace))
	lastGroup := ""
	for i, spec := range items {
		group := slashGroupOf(spec)
		if group != lastGroup {
			rows = append(rows, pal.Muted.Render(group))
			lastGroup = group
		}
		name := "/" + spec.Name
		line := name
		desc := spec.Description
		if strings.EqualFold(spec.Name, current) && current != "" {
			if desc != "" {
				desc += " · current"
			} else {
				desc = "current"
			}
		}
		if layout.ShowDetail && desc != "" {
			detail := TruncateToWidth(desc, width-CellWidth(name)-6)
			if detail != "" {
				line += "  " + pal.Muted.Render(detail)
			}
		} else {
			line = TruncateToWidth(line, width-2)
		}
		if start+i == cursor {
			rows = append(rows, pal.Selected.Render("> "+stripANSI(line)))
		} else {
			rows = append(rows, "  "+line)
		}
	}
	if hideBelow {
		hidden := len(matches) - start - len(items)
		rows = append(rows, pal.Muted.Render(fmt.Sprintf("  … %d more", hidden)))
	}
	return strings.Join(rows, "\n")
}

// one counts a marker row when its section shows.
func one(show bool) int {
	if show {
		return 1
	}
	return 0
}

// composerStateLine names queued prompts under the input: the count plus the
// head of the queue, truncated to one row. This is the queue's only live
// home — the transcript holds conversation history, never interaction
// state — so nothing appears twice and nothing floats mid-screen.
// Idleness stays quiet, and an approval modal owns its dialog, so both
// yield no line at all.
func composerStateLine(state State, pal *Palette, layout Layout) string {
	if state.Pending != nil {
		return ""
	}
	n := len(state.Queued)
	if n == 0 {
		return ""
	}
	label := "1 queued"
	if n > 1 {
		label = fmt.Sprintf("%d queued", n)
	}
	first := TruncateToWidth(strings.TrimSpace(sanitizeInline(state.Queued[0])), displayWidth(state, layout)-CellWidth(label)-4)
	if first == "" {
		return pal.Queued.Render(label)
	}
	return pal.Queued.Render(label + " · " + first)
}

// secretMaskedRows folds the masked credential block identically for render
// and layout: wrapWidth is the composer text width (after the prompt
// prefix), and the label takes its share. Sharing it keeps reserved and
// rendered rows in agreement no matter how long the pasted input is.
func secretMaskedRows(label, input string, wrapWidth int) []string {
	if w := wrapWidth - CellWidth(label) - 2; w > 0 {
		wrapWidth = w
	} else {
		wrapWidth = 1
	}
	masked := strings.Repeat("•", len([]rune(input)))
	return WrapToWidth(masked, wrapWidth)
}

// composerWrapWidth is the cells available for composer text after the two-cell
// prompt/continuation prefix. It matches the budget the layout reserves so the
// rendered rows and the reserved rows agree.
func composerWrapWidth(layout Layout) int {
	width := layout.ContentWidth
	if width <= 0 {
		width = 80
	}
	if width < 3 {
		width = 3
	}
	return width - 2
}

// renderEntry writes one transcript entry, wrapped to the content width.
// frame is the current spinner frame, shared with the status line so a running
// tool and the footer animate in step.
//
// The question renders as a faint background block across the content width,
// with no label prefix: the surface alone separates it from the answer.
func renderEntry(sb *strings.Builder, entry Entry, pal *Palette, layout Layout, frame string) {
	switch entry.Kind {
	case EntryUser:
		renderUserBlock(sb, entry.UserText, pal, layout)
		sb.WriteString("\n")

	case EntryAssistant:
		if entry.AssistantText == "" {
			return
		}
		// The markdown layer owns wrapping: it measures plain cells first
		// and styles after, so one width calculation governs and ANSI
		// never inflates the budget. Never re-wrap styled output here.
		rendered := RenderMarkdownWrapped(entry.AssistantText, pal, textWidth(layout))
		for _, wl := range strings.Split(rendered, "\n") {
			sb.WriteString(wl + "\n")
		}
		sb.WriteString("\n")

	case EntryTool:
		if entry.Tool != nil {
			renderTool(sb, entry.Tool, pal, layout, frame)
		}

	case EntryNotice:
		for _, wl := range wrapPlain(entry.Notice, layout) {
			sb.WriteString(pal.Notice.Render(wl) + "\n")
		}
		sb.WriteString("\n")

	case EntrySupport:
		renderSupportEntry(sb, entry, pal, layout)
	}
}

// renderSupportEntry renders one $sup exchange.
//
// The question carries a "$sup" marker and the answer renders through the
// markdown layer, so the ephemeral surface reads as distinct from the main
// conversation without depending on colour alone.
func renderSupportEntry(sb *strings.Builder, entry Entry, pal *Palette, layout Layout) {
	if entry.SupportIsQuestion {
		for _, line := range wrapPlain("$sup "+entry.SupportText, layout) {
			sb.WriteString(pal.Notice.Render(line) + "\n")
		}
		sb.WriteString("\n")
		return
	}
	rendered := RenderMarkdownWrapped(entry.SupportText, pal, textWidth(layout))
	for _, wl := range strings.Split(rendered, "\n") {
		sb.WriteString(wl + "\n")
	}
	sb.WriteString("\n")
}

// renderTool writes a tool call and, once finished, its outcome.
//
// The default projection is collapsed and compact: one badge line naming
// the invocation plus one ⎿ outcome line. The bounded body appears only
// when the block is expanded via Ctrl+O, so a long agent run stays
// scannable without losing the deeper content.
//
// States are text plus color: running spinner, ✓ success, ✗ failure, ·
// pending, so nothing depends on hue alone. Output is bounded and wrapped;
// large results collapse to the limit with a truncation marker rather than
// flooding the conversation.
func renderTool(sb *strings.Builder, tool *ToolEntry, pal *Palette, layout Layout, frame string) {
	// The badge names the invocation (tool plus its bounded arguments),
	// falling back to the bare tool name for blocks that predate it.
	// The invocation is truncated against the viewport minus the badge
	// chrome around it, so a long path can never soft-wrap the terminal.
	elapsed := tool.elapsed()
	invBudget := dialogWidth(layout)
	switch tool.Status {
	case ToolRunning:
		invBudget -= CellWidth("⠋  running")
	case ToolDone, ToolError:
		invBudget -= 4 + CellWidth(elapsed)
	default:
		invBudget -= 2
	}
	if invBudget < 8 {
		invBudget = 8
	}
	invocation := TruncateToWidth(sanitizeInline(toolInvocation(tool)), invBudget)
	var badge string
	switch tool.Status {
	case ToolRunning:
		badge = pal.ToolRun.Render(frame+" "+invocation) + "  " + pal.Status.Render("running")
	case ToolDone:
		badge = pal.ToolDone.Render("✓ "+invocation) + "  " + pal.Status.Render(elapsed)
	case ToolError:
		badge = pal.ToolError.Render("✗ "+invocation) + "  " + pal.Status.Render(elapsed)
	case ToolPending:
		badge = pal.ToolRun.Render("· " + invocation)
	}
	sb.WriteString(badge + "\n")

	// The compact outcome lands on one ⎿ line: the finished summary when
	// the executor provided one, else a bounded first-line fallback so a
	// single-line result stays visible even with its body collapsed.
	// The hint width is reserved up front so the whole row stays bounded.
	if tool.Status != ToolRunning {
		if outcome := collapsedOutcome(tool); outcome != "" {
			hint := ""
			if toolExpandable(tool) && !tool.Expanded {
				hint = "  (ctrl+o to expand)"
			}
			budget := dialogWidth(layout) - 4 - CellWidth(hint)
			if budget < 8 {
				budget = 8
			}
			sb.WriteString("  " + pal.Status.Render("⎿ "+TruncateToWidth(outcome, budget)))
			if hint != "" {
				sb.WriteString(pal.Muted.Render(hint))
			}
			sb.WriteString("\n")
		}
	}

	// Collapsed is the default: the body below only renders expanded.
	// Expansion is presentation-only — the same semantic Text, bounded by
	// the same truncation, wrapping, and sanitization as before.
	if !tool.Expanded || tool.Text == "" {
		// A finished block keeps its blank separator so sequential calls
		// stay distinct; a running one stays tight while it streams.
		if tool.Status == ToolDone || tool.Status == ToolError {
			sb.WriteString("\n")
		}
		return
	}
	body, truncated := TruncateForDisplay(tool.Text, toolOutputLimit)
	// Tool results carry progress bars, colors, and carriage returns from
	// the child process: keep the colors, neutralize the cursor control,
	// then wrap what the terminal will actually lay out.
	body = sanitizeBlock(body)
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

// toolInvocation names what the call does: the captured invocation when the
// block opened with one, else the bare tool name.
func toolInvocation(tool *ToolEntry) string {
	if tool.Invocation != "" {
		return tool.Invocation
	}
	if tool.Name != "" {
		return tool.Name
	}
	return "tool"
}

// collapsedOutcome is the one-line result shown with or without expansion.
// The executor's compact summary wins; without one, the first line of the
// outcome stands in so trivial results stay visible.
func collapsedOutcome(tool *ToolEntry) string {
	if tool.Summary != "" {
		return sanitizeInline(tool.Summary)
	}
	if strings.TrimSpace(tool.Text) == "" {
		return ""
	}
	first := tool.Text
	if i := strings.Index(first, "\n"); i >= 0 {
		first = first[:i]
	}
	return sanitizeInline(oneLine(first))
}

// toolExpandable reports whether expanding reveals more than the collapsed
// line already says: multi-line or truncated outcomes only. Trivial
// single-line results stay hint-free so sequential runs stay scannable.
func toolExpandable(tool *ToolEntry) bool {
	if tool.Text == "" {
		return false
	}
	if tool.Truncated {
		return true
	}
	return strings.Contains(strings.TrimRight(tool.Text, "\n"), "\n")
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
	sb.WriteString(pal.Muted.Render("↑↓ move · enter confirm · y approve · n/esc deny") + "\n")
	if approvalBlank(pending, layout) {
		sb.WriteString("\n")
	}
}

// renderStatus draws the contextual footer: working directory, session,
// context budget, and measured usage.
//
// It deliberately does not repeat the identity, model, or runtime phase shown
// in the header — one fact, one home. Wide and medium tiers show the full
// context/session/cwd/usage set; narrow keeps cwd and session so the composer
// and conversation keep their rows, dropping the budget prefix and usage.
// Unknown values stay absent rather than rendering as zero, and usage text is
// shown verbatim from measured accounting — the view never labels an estimate
// as measured. Nothing wraps: the terminal owns wrapping, and a footer that
// wraps reads as two footers.
func renderStatus(state State, pal *Palette, layout Layout) string {
	status := state.Status
	parts := []string{}
	// Cwd and usage text arrive from the environment and measured
	// accounting: untrusted for layout purposes, sanitized inline so the
	// footer stays exactly one row (it is truncated, never wrapped).
	if layout.ShowDetail {
		if pct := status.Percent(); pct >= 0 {
			parts = append(parts, fmt.Sprintf("context %d%%", pct))
		}
		if status.Session != "" {
			parts = append(parts, status.Session)
		}
		if status.Cwd != "" {
			parts = append(parts, shortenCwd(sanitizeInline(status.Cwd), 24))
		}
		if status.UsageText != "" {
			parts = append(parts, sanitizeInline(status.UsageText))
		}
	} else {
		if status.Cwd != "" {
			parts = append(parts, shortenCwd(sanitizeInline(status.Cwd), 16))
		}
		if status.Session != "" {
			parts = append(parts, status.Session)
		}
	}
	if status.Busy {
		parts = append(parts, spinnerFrame(status.Spinner)+" working")
	}
	// SUP is a model role, not a workspace: the footer keeps naming the
	// workspace authority while support is open. Support runtime state
	// lives inside the overlay itself.
	out := strings.Join(parts, "  ·  ")
	if state.Width > 0 {
		out = TruncateToWidth(out, state.Width-1)
	}
	if status.Workspace == "" {
		return pal.Status.Render(out)
	}
	// The authority badge leads the footer, in its own semantic accent. Plain
	// cells are measured and truncated before styling, so ANSI never inflates
	// the one-row budget.
	badge := sanitizeInline(status.Workspace)
	const sep = "  ·  "
	if state.Width > 0 {
		restBudget := state.Width - 1 - CellWidth(badge) - CellWidth(sep)
		if restBudget < 0 {
			// Too narrow for both: the authority identity is the part that
			// must survive, so the context detail yields.
			return WorkspaceBadge(status.WorkspaceAccent, TruncateToWidth(badge, state.Width-1))
		}
		out = TruncateToWidth(out, restBudget)
	}
	styled := WorkspaceBadge(status.WorkspaceAccent, badge)
	if out == "" {
		return styled
	}
	return styled + pal.Status.Render(sep+out)
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

// renderUserBlock writes the question as a faint background layer across
// the content width. Each wrapped line is padded to the full budget before
// styling so the background reads as one solid block rather than patchy
// per-word highlights. The stored text stays untouched; only this render
// carries the background.
func renderUserBlock(sb *strings.Builder, text string, pal *Palette, layout Layout) {
	width := textWidth(layout)
	for _, wl := range wrapPlain(text, layout) {
		padded := wl
		if width > 0 {
			if gap := width - CellWidth(wl); gap > 0 {
				padded += strings.Repeat(" ", gap)
			}
		}
		sb.WriteString(pal.UserBG.Render(padded) + "\n")
	}
}

// wrapPlain wraps unstyled transcript prose to the content width. Input is
// untrusted (prompts, notices, tool echoes), so it is sanitized first:
// control sequences that could move the terminal cursor must never reach
// measurement or output. Styled spans (markdown) are wrapped per line by
// the caller splitting on newlines; ANSI is stripped for measurement so
// styles never inflate the budget.
func wrapPlain(text string, layout Layout) []string {
	text = sanitizeBlock(text)
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

// spinnerFrame returns the animation glyph for an index, normalised so a
// negative or oversized value can never index out of range: Render must not
// panic on any state a caller can build.
func spinnerFrame(index int) string {
	n := len(spinnerFrames)
	if n == 0 {
		return ""
	}
	index %= n
	if index < 0 {
		index += n
	}
	return spinnerFrames[index]
}

// AdvanceSpinner returns the next frame index.
func AdvanceSpinner(current, frames int) int {
	if frames <= 0 {
		return 0
	}
	return (current + 1) % frames
}
