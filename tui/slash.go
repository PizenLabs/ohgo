package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// Inline palette geometry. The name column is fixed so a description always
// starts at the same cell: filtering, cycling the cursor, or switching the
// active row never shifts the column, which is what makes the overlay read as
// a table instead of jittering.
const (
	// PaletteNameCols is the fixed width of the entry-name column, including
	// the trigger symbol. Names are padded with spaces to this width so the
	// description column cannot move between rows.
	PaletteNameCols = 18
	// PaletteMaxRows bounds the selectable rows. Discovery shows the top
	// matches only, so the palette never evicts the conversation behind it.
	PaletteMaxRows = 6
	// maxPaletteRows is the inline palette's own row cap. It equals
	// PaletteMaxRows so the inline overlay and the exported
	// RenderSlashPalette never disagree.
	maxPaletteRows = PaletteMaxRows
	// paletteFileMatchCap bounds the file match list before windowing. The
	// visible window is smaller; the cap keeps ranking work bounded on a
	// large workspace without changing what the window can show.
	paletteFileMatchCap = 200
	// paletteBorderRows is the rounded box's top and bottom border. The layout
	// reservation and the renderer share it so reserved and painted rows agree.
	paletteBorderRows = 2
	// paletteMarkerCols is the fixed width of the selection gutter. It keeps
	// the cursor visible without colour (nothing depends on hue alone) and
	// keeps the name column aligned across active and inactive rows.
	paletteMarkerCols = 2
	// paletteMinInner is the smallest usable content width before the box
	// itself stops being readable.
	paletteMinInner = 8
)

// paletteTrigger names the character that opens an inline palette.
type paletteTrigger byte

const (
	// paletteCommand is "/": the slash-command registry.
	paletteCommand paletteTrigger = '/'
	// paletteFile is "@": workspace files and folders.
	paletteFile paletteTrigger = '@'
	// paletteSupport is "$": the ephemeral support surface.
	paletteSupport paletteTrigger = '$'
	// paletteEscape is "!": the direct shell escape. It is not a completion
	// registry — its single row teaches the syntax — but it shares the
	// palette so the hint appears in the same place as every other trigger.
	paletteEscape paletteTrigger = '!'
)

// paletteEntry is one inline suggestion row.
//
// Every trigger renders through this one shape, so the layout, the fixed name
// column, the marker, and the section headers are identical whether the row is
// a command, a file, or a support shortcut.
type paletteEntry struct {
	// Name is the display token including its trigger: "/model", "@src/",
	// "$sup".
	Name string
	// Detail is the second column: a command's description, a path's type, or
	// a shortcut's blurb. A command may carry a "· current" suffix.
	Detail string
	// Insert is the exact text spliced into the composer on accept.
	Insert string
	// Group is the section header. An empty group suppresses the header, so a
	// single-group palette can omit it.
	Group string
}

// paletteToken finds the trigger word that contains cursor.
//
// A trigger word is a whitespace-delimited token whose first rune is one of
// "/", "@", or "$", with the cursor somewhere after the trigger. It returns the
// trigger, the query between the trigger and the cursor, the rune span
// [start,end) of the whole token, and whether a trigger is active.
//
// "/" and "$" are line-level directives: the parser only accepts them as the
// first token of the line, so the palette stays off for a mid-sentence token
// that could never dispatch. "@" references may appear anywhere in a prompt.
func paletteToken(input string, cursor int) (trigger paletteTrigger, query string, start, end int, ok bool) {
	runes := []rune(input)
	cursor = clampRune(cursor, len(runes))
	start = cursor
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	if start >= len(runes) || cursor <= start {
		return 0, "", 0, 0, false
	}
	switch runes[start] {
	case '/':
		trigger = paletteCommand
	case '@':
		trigger = paletteFile
	case '$':
		trigger = paletteSupport
	case '!':
		// The escape is line-level: it must lead the line, and its "token"
		// is only the marker, so accepting the hint can never erase a
		// command already being typed.
		if start != 0 {
			return 0, "", 0, 0, false
		}
		return paletteEscape, string(runes[1:cursor]), 0, 1, true
	default:
		return 0, "", 0, 0, false
	}
	if trigger != paletteFile && strings.TrimSpace(string(runes[:start])) != "" {
		return 0, "", 0, 0, false
	}
	end = cursor
	for end < len(runes) && !unicode.IsSpace(runes[end]) {
		end++
	}
	return trigger, string(runes[start+1 : cursor]), start, end, true
}

// SpliceToken replaces the token span [start,end) with text and returns the new
// input plus the cursor placed just after the insertion, so the next typed
// character continues the prompt rather than the completed token.
func SpliceToken(input string, start, end int, text string) (string, int) {
	runes := []rune(input)
	start = clampRune(start, len(runes))
	end = clampRune(end, len(runes))
	if end < start {
		end = start
	}
	out := string(runes[:start]) + text + string(runes[end:])
	return out, start + len([]rune(text))
}

// supportShortcuts is the "$" trigger's registry. It is a slice rather than a
// single constant so a future shortcut is one row, not a new branch.
var supportShortcuts = []paletteEntry{
	{Name: "$sup", Detail: "fast assistance", Insert: "$sup ", Group: "SUPPORT"},
}

// paletteStyles names the semantic styles the palette uses, resolved once per
// theme so the row builder never reaches for a raw colour.
type paletteStyles struct {
	border       lipgloss.Style
	header       lipgloss.Style
	marker       lipgloss.Style
	activeName   lipgloss.Style
	activeDesc   lipgloss.Style
	inactiveName lipgloss.Style
	inactiveDesc lipgloss.Style
}

// paletteStylesFor resolves the palette styles from the shared chrome tokens.
//
// Dark follows the design tokens exactly: active name in ColorPrimary and
// bold, active description in ColorText, inactive name in ColorMuted, inactive
// description in ColorDim, border in ColorDim. Light keeps the same roles with
// the darkened counterparts so a bright screen stays legible.
func paletteStylesFor(theme string) paletteStyles {
	if NormalizeTheme(theme) == ThemeLight {
		return paletteStyles{
			border:       fg(chromeLightDim),
			header:       fg(lightMuted).Bold(true),
			marker:       fg(chromeLightPrimary),
			activeName:   fg(chromeLightPrimary).Bold(true),
			activeDesc:   fg(lightText),
			inactiveName: fg(lightMuted),
			inactiveDesc: fg(chromeLightDim),
		}
	}
	return paletteStyles{
		border:       fg(ColorDim),
		header:       fg(ColorMuted).Bold(true),
		marker:       fg(ColorPrimary),
		activeName:   fg(ColorPrimary).Bold(true),
		activeDesc:   fg(ColorText),
		inactiveName: fg(ColorMuted),
		inactiveDesc: fg(ColorDim),
	}
}

// filterCommands returns the visible registry entries whose name starts with
// query, case-insensitively. Hidden aliases stay out, matching /help, so the
// palette can never show a command the listing hides. The registry is the
// single source of truth, so a shown command is always one dispatch accepts.
func filterCommands(query string) []CommandSpec {
	// The registry names never carry the leading slash, and both the stateful
	// caller (which strips it) and the exported palette (which may not) feed
	// this, so trim one defensively rather than forcing every caller to agree.
	filter := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(query)), "/")
	var out []CommandSpec
	for _, spec := range Commands {
		if spec.Hidden {
			continue
		}
		if filter == "" || strings.HasPrefix(strings.ToLower(spec.Name), filter) {
			out = append(out, spec)
		}
	}
	return out
}

// commandEntries builds the "/" palette rows for a query. current names the
// workspace whose command carries the "· current" marker.
func commandEntries(query, current string) []paletteEntry {
	var out []paletteEntry
	for _, spec := range filterCommands(query) {
		desc := spec.Description
		if current != "" && strings.EqualFold(spec.Name, current) {
			if desc != "" {
				desc += " · current"
			} else {
				desc = "current"
			}
		}
		out = append(out, paletteEntry{
			Name:   "/" + spec.Name,
			Detail: desc,
			// A trailing space leaves room for an argument, an "@" reference,
			// or a "$sup" request without backspacing over the command.
			Insert: "/" + spec.Name + " ",
			Group:  groupOf(spec),
		})
	}
	return out
}

// fileEntries builds the "@" palette rows for a query. Prefix matches rank
// above substring matches so typing a path segment surfaces that segment's
// entries first; order is otherwise the roster's own (already sorted).
func fileEntries(files []string, query string) []paletteEntry {
	q := strings.ToLower(query)
	var prefix, substring []paletteEntry
	for _, path := range files {
		lower := strings.ToLower(path)
		switch {
		case strings.HasPrefix(lower, q):
			prefix = append(prefix, fileEntry(path))
		case q != "" && strings.Contains(lower, q):
			substring = append(substring, fileEntry(path))
		}
	}
	out := append(prefix, substring...)
	if len(out) > paletteFileMatchCap {
		out = out[:paletteFileMatchCap]
	}
	return out
}

// fileEntry renders one workspace path row. A trailing slash marks a folder.
//
// A completed file gets a trailing space so the next word starts a new token
// instead of gluing to the path ("@index.html check this"). A folder gets no
// space: the user is still composing a path, so the next typed segment must
// continue it ("@pkg/" + "util.go").
func fileEntry(path string) paletteEntry {
	detail := "file"
	insert := "@" + path + " "
	if strings.HasSuffix(path, "/") {
		detail = "folder"
		insert = "@" + path
	}
	return paletteEntry{Name: "@" + path, Detail: detail, Insert: insert, Group: "FILES"}
}

// filterSupport builds the "$" palette rows for a query.
func filterSupport(query string) []paletteEntry {
	q := strings.ToLower(strings.TrimPrefix(query, "$"))
	var out []paletteEntry
	for _, entry := range supportShortcuts {
		name := strings.ToLower(strings.TrimPrefix(entry.Name, "$"))
		if q == "" || strings.HasPrefix(name, q) {
			out = append(out, entry)
		}
	}
	return out
}

// paletteEntriesFor is the single source of the inline palette's rows.
//
// It reads the trigger word at the cursor and dispatches to the trigger's
// registry. It returns nil when the palette is dismissed, when no trigger word
// holds the cursor, or when nothing matches, which the caller treats as "no
// overlay". Rendering and key handling both call it, so what is shown is
// exactly what accept will splice.
func paletteEntriesFor(s State) []paletteEntry {
	if s.PaletteHide {
		return nil
	}
	trigger, query, _, _, ok := paletteToken(s.Input, s.Cursor)
	if !ok {
		return nil
	}
	switch trigger {
	case paletteCommand:
		return commandEntries(query, strings.ToUpper(strings.TrimSpace(s.Status.Workspace)))
	case paletteFile:
		return fileEntries(s.PaletteFiles, query)
	case paletteSupport:
		return filterSupport(query)
	case paletteEscape:
		return escapeEntries(query)
	}
	return nil
}

// shellEscapeHint is the palette row's description for the "!" trigger: it
// teaches what the escape does before the user commits to a command.
const shellEscapeHint = "Run shell command directly in workspace"

// escapeEntries builds the "!" palette row for a query.
//
// The hint is shown only while the command is still empty: it teaches the
// syntax, and once a real command is being typed it has nothing left to say.
// The single row carries no group header, so the hint reads as one subtle line
// rather than a section.
func escapeEntries(query string) []paletteEntry {
	if strings.TrimSpace(query) != "" {
		return nil
	}
	return []paletteEntry{{
		Name:   "! <command>",
		Detail: shellEscapeHint,
		// Accepting the hint leaves "! " so the cursor lands ready for the
		// command, rather than quoting the placeholder literally.
		Insert: "! ",
	}}
}

// paletteOwnsKeys reports whether the visible palette should capture
// navigation and accept keys.
//
// The "!" escape hint is display-only: it teaches the syntax and must not
// swallow Enter, which belongs to submitting the typed line. Every completion
// trigger owns its keys; the escape trigger does not.
func paletteOwnsKeys(s State) bool {
	if len(paletteEntriesFor(s)) == 0 {
		return false
	}
	trigger, _, _, _, ok := paletteToken(s.Input, s.Cursor)
	return ok && trigger != paletteEscape
}

// ClampPaletteCursor keeps a selection inside the filtered match list. An
// empty list parks at zero; an overshooting index lands on the last match
// rather than rendering no highlight at all.
func ClampPaletteCursor(cursor, matches int) int {
	if matches <= 0 || cursor < 0 {
		return 0
	}
	if cursor >= matches {
		return matches - 1
	}
	return cursor
}

// paletteWindow returns the visible suggestion window with its start offset
// and clamped cursor, plus whether rows hide above or below. The window follows
// the cursor so the highlight is always on a rendered row, never scrolled off.
// Render and layout share it so reserved and rendered rows always agree.
func paletteWindow(s State) (shown []paletteEntry, start, cursor int, above, below bool) {
	matches := paletteEntriesFor(s)
	if len(matches) == 0 {
		return nil, 0, 0, false, false
	}
	cursor = s.PaletteCursor
	if cursor < 0 || cursor >= len(matches) {
		cursor = 0
	}
	shown, start, above, below = windowMatches(matches, cursor, maxPaletteRows)
	return shown, start, cursor, above, below
}

// windowMatches slices the visible window of a match list for an in-range
// cursor: it follows the cursor so the highlight is always on a rendered row,
// then clamps to keep the window inside the list. It returns the slice plus
// whether rows hide above or below. The stateful renderer and the exported
// palette share it, so "top matches" means the same thing everywhere.
func windowMatches(matches []paletteEntry, cursor, max int) (shown []paletteEntry, start int, above, below bool) {
	if len(matches) == 0 || max <= 0 {
		return nil, 0, false, false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(matches) {
		cursor = len(matches) - 1
	}
	start = 0
	if len(matches) > max {
		start = cursor - max + 1
		if start < 0 {
			start = 0
		}
		if start+max > len(matches) {
			start = len(matches) - max
		}
	}
	end := start + max
	if end > len(matches) {
		end = len(matches)
	}
	return matches[start:end], start, start > 0, end < len(matches)
}

// paletteRows counts the suggestion rows the composer will render, so the
// layout reserves exactly what the view draws.
func paletteRows(s State) int {
	shown, _, _, above, below := paletteWindow(s)
	if len(shown) == 0 {
		return 0
	}
	n := len(shown) + paletteGroupHeaders(shown)
	if above {
		n++
	}
	if below {
		n++
	}
	// The overlay renders inside a rounded box: the border claims one row on
	// each side, so the reservation must carry it or the frame shorts.
	return n + paletteBorderRows
}

// paletteGroupHeaders counts the group titles the suggestion list will render:
// one per distinct group present in the visible window. The headers are
// presentation only; selection still indexes the flat matches.
func paletteGroupHeaders(shown []paletteEntry) int {
	seen := map[string]bool{}
	n := 0
	for _, entry := range shown {
		if entry.Group == "" || seen[entry.Group] {
			continue
		}
		seen[entry.Group] = true
		n++
	}
	return n
}

// paletteRequest is the renderer's input. It carries an already-windowed slice
// plus the absolute indices needed for the overflow markers, so the stateful
// renderer and the exported RenderSlashPalette share one row builder and
// cannot drift.
type paletteRequest struct {
	// shown is the visible match slice, in registry order.
	shown []paletteEntry
	// cursor is the absolute index into the full match list.
	cursor int
	// startIndex is the absolute index of shown[0].
	startIndex int
	// total is the full match count, for the "more" markers.
	total int
	// above and below report rows hidden on either side of the window.
	above, below bool
	// headers renders the group titles.
	headers bool
	// width is the cells the whole box may occupy.
	width int
	// exactRows, when positive, is the total number of lines the box must
	// emit (borders included). The stateful caller reserves rows before it
	// trims the match list, so it pads the box to the reservation: a frame
	// that emits fewer rows than it reserved would float the composer.
	exactRows int
	// styles resolves the semantic palette colours.
	styles paletteStyles
}

// RenderSlashPalette renders the slash-command overlay for the text typed
// after the leading "/".
//
// It is the "/" trigger's pure entry point: query, cursor, and width in, a
// string out. The filter is a case-insensitive prefix match on the command
// name, the cursor is clamped into range, and only the top PaletteMaxRows
// matches show. Every row is padded to one content width, so cycling the
// cursor or typing filter letters can never change the layout. It returns ""
// when nothing matches, which the caller treats as "no overlay".
func RenderSlashPalette(query string, cursor, width int) string {
	entries := commandEntries(query, "")
	if len(entries) == 0 {
		return ""
	}
	cursor = ClampPaletteCursor(cursor, len(entries))
	shown, start, above, below := windowMatches(entries, cursor, PaletteMaxRows)
	return renderPaletteBox(paletteRequest{
		shown:      shown,
		cursor:     cursor,
		startIndex: start,
		total:      len(entries),
		above:      above,
		below:      below,
		headers:    true,
		width:      width,
		styles:     paletteStylesFor(ThemeDark),
	})
}

// renderPaletteBox builds the rounded, fixed-width overlay.
//
// Rows are laid out plain first and padded to one content width, then each
// field is styled: measuring after styling would let ANSI codes inflate the
// column. The box owns the only decoration, so a group title, a match, and an
// overflow marker all share the same width and the overlay never shifts.
func renderPaletteBox(req paletteRequest) string {
	if len(req.shown) == 0 && !req.above && !req.below {
		return ""
	}
	inner := paletteInnerWidth(req.width)
	nameW := PaletteNameCols
	if avail := inner - paletteMarkerCols - 1; nameW > avail {
		nameW = avail
	}
	if nameW < 4 {
		nameW = 4
	}
	// Description budget: the fixed gutter, name column, and one separator.
	descW := inner - paletteMarkerCols - nameW - 1
	if descW < 0 {
		descW = 0
	}

	var lines []string
	if req.above {
		lines = append(lines, req.styles.inactiveDesc.Render(padPaletteRow(fmt.Sprintf("… %d above", req.startIndex), inner)))
	}
	lastGroup := ""
	for i, entry := range req.shown {
		if req.headers && entry.Group != "" && entry.Group != lastGroup {
			lines = append(lines, req.styles.header.Render(padPaletteRow(entry.Group, inner)))
			lastGroup = entry.Group
		}
		lines = append(lines, paletteItemRow(entry, req.startIndex+i == req.cursor, nameW, descW, inner, req.styles))
	}
	if req.below {
		hidden := req.total - req.startIndex - len(req.shown)
		lines = append(lines, req.styles.inactiveDesc.Render(padPaletteRow(fmt.Sprintf("… %d more", hidden), inner)))
	}
	// Fill the reserved height exactly. Trimming the match list can shed a
	// group header along with the last row of that group, so the content may
	// come in under the reservation; padding the interior keeps the frame
	// exactly as tall as the layout promised.
	if req.exactRows >= paletteBorderRows+1 {
		for len(lines)+paletteBorderRows < req.exactRows {
			lines = append(lines, strings.Repeat(" ", inner))
		}
		for len(lines)+paletteBorderRows > req.exactRows && len(lines) > 0 {
			lines = lines[:len(lines)-1]
		}
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(req.styles.border.GetForeground()).
		Padding(0, 1)
	return box.Render(strings.Join(lines, "\n"))
}

// paletteItemRow renders one suggestion row. The selection is carried by the
// marker glyph and by colour together: a monochrome terminal still shows which
// row Enter would accept.
func paletteItemRow(entry paletteEntry, selected bool, nameW, descW, inner int, st paletteStyles) string {
	marker := "  "
	nameStyle := st.inactiveName
	descStyle := st.inactiveDesc
	if selected {
		marker = st.marker.Render("▸ ")
		nameStyle = st.activeName
		descStyle = st.activeDesc
	}
	name := padCells(entry.Name, nameW)
	if CellWidth(name) > nameW {
		name = TruncateToWidth(name, nameW)
	}
	desc := TruncateToWidth(entry.Detail, descW)
	// Pad the description so the styled row is exactly `inner` cells wide.
	gap := inner - paletteMarkerCols - nameW - 1 - CellWidth(desc)
	if gap > 0 {
		desc += strings.Repeat(" ", gap)
	}
	return marker + nameStyle.Render(name) + " " + descStyle.Render(desc)
}

// paletteInnerWidth is the content width inside the border and padding. It
// never collapses below paletteMinInner: a caller that passed an unusable
// width gets a readable box rather than a degenerate one.
func paletteInnerWidth(width int) int {
	if width <= 0 {
		width = 64
	}
	inner := width - paletteBorderRows - 2 // box sides + one cell of horizontal padding each
	if inner < paletteMinInner {
		inner = paletteMinInner
	}
	return inner
}

// padPaletteRow pads a plain row to exactly width cells. It is used for rows
// without a styled interior (headers and overflow markers), whose trailing
// space is safe to style as one span.
func padPaletteRow(text string, width int) string {
	text = TruncateToWidth(text, width)
	if gap := width - CellWidth(text); gap > 0 {
		text += strings.Repeat(" ", gap)
	}
	return text
}
