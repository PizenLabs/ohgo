package tui

// The Command & Help Center: the single discovery surface behind /help,
// /commands, the "?" shortcut, and ^p on a fresh session.
//
// It replaces the old plain-text /help notice, which printed the registry into
// the transcript and left a stale listing behind, and the separate command
// palette. Every entry point opens this exact modal, so the surfaces can never
// disagree and there is one frame to learn. The frame follows the modal family
// — a border-embedded title, section headers, a rule above a structured footer
// bar — so it reads as one system with the Session Manager and the model plane.
//
// The modal has two tabs sharing one layout, filter, and navigation path:
//
//   - Commands: the authoritative slash-command registry. Choosing a row runs
//     the command exactly as typing it would.
//   - Shortcuts: how to drive ohgo from the keyboard. It is read-only; there is
//     nothing to run.
//
// Each tab keeps its own query and highlight, so switching views never loses
// the other tab's place. Rows normalize to one helpItem shape, so the two
// rosters cannot drift in how they filter, window, or align.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Help Center geometry. The dialog is responsive: it grows with the terminal up
// to a comfortable reading width and never touches the edges while margins fit.
const (
	// helpCardHiWidth caps the dialog so ultrawide terminals keep context on
	// both sides.
	helpCardHiWidth = 96
	// helpCardMarginX is the total horizontal margin (both sides).
	helpCardMarginX = 8
	// helpModalTitle is the border-embedded title, matching the Session
	// Manager's "SESSION MANAGER" treatment.
	helpModalTitle = "COMMAND & HELP CENTER"

	// helpColCursor is the fixed selection gutter. It keeps the green cursor
	// visible without colour alone and keeps the first column aligned across
	// active and inactive rows.
	helpColCursor = 2
	// helpColName is the preferred first column. It is padded to a fixed width
	// so the category and description columns never jitter. It carries command
	// names for the Commands tab and key bindings for the Shortcuts tab.
	helpColName = 22
	// helpColCategory is the minimum category-badge column. A view whose longest
	// category label is wider grows the column to fit it.
	helpColCategory = 11
	// helpColGap is the spacing between columns.
	helpColGap = 2
	// helpNameMin is the narrowest usable first column before the category
	// column is shed.
	helpNameMin = 8
	// helpDescMin is the narrowest usable description column.
	helpDescMin = 10
	// helpOverhead is the fixed content the roster window does not own: the
	// frame (top border, footer rule, footer bar, bottom border), the tab strip,
	// the search line, two rules, the column header, and the three-row action
	// hint.
	helpOverhead = 12
)

// HelpView names the active tab.
type HelpView int

const (
	// HelpViewCommands is the slash-command roster.
	HelpViewCommands HelpView = iota
	// HelpViewShortcuts is the keybinding roster.
	HelpViewShortcuts
)

// helpViewCount is the number of tabs, so cycling stays correct when a third
// view is added.
const helpViewCount = int(HelpViewShortcuts) + 1

// Shortcut categories. They group the Shortcuts tab into the surfaces a binding
// belongs to: moving around the interface, driving the session, and acting on
// the workspace.
const (
	// ShortcutGroupNavigation holds movement and view keys.
	ShortcutGroupNavigation = "NAVIGATION"
	// ShortcutGroupSession holds prompt and session-lifecycle keys.
	ShortcutGroupSession = "SESSION"
	// ShortcutGroupWorkspace holds workspace triggers and editing keys.
	ShortcutGroupWorkspace = "WORKSPACE / GENERAL"
)

// ShortcutSpec describes one keybinding for the Shortcuts tab.
//
// It is data, not behavior: the registry teaches the binding, and the handlers
// remain the single authority on what a key does. A binding listed here must
// actually work, so the roster is reviewed against the handlers rather than
// invented.
type ShortcutSpec struct {
	// Keys is the display binding, e.g. "Ctrl+S", "Esc / q", "↑ / ↓".
	Keys string
	// Description is one line of help.
	Description string
	// Category groups the roster: NAVIGATION, SESSION, or WORKSPACE / GENERAL.
	Category string
}

// Keybindings is the Shortcuts tab registry.
//
// It lives beside the modal rather than in the key handlers so the two cannot
// silently drift: when a handler's binding changes, this list is the one place
// the help text has to change too. Order defines the rendered groups.
var Keybindings = []ShortcutSpec{
	{Keys: "↑ / ↓", Description: "Move the selection, or recall prompt history.", Category: ShortcutGroupNavigation},
	{Keys: "PgUp / PgDn", Description: "Scroll the transcript or page a list.", Category: ShortcutGroupNavigation},
	{Keys: "Home / End", Description: "Jump to the first or last row, or the line edge.", Category: ShortcutGroupNavigation},
	{Keys: "Ctrl+O", Description: "Expand or collapse the latest activity block.", Category: ShortcutGroupNavigation},
	{Keys: "Tab / Shift+Tab", Description: "Switch between the Commands and Shortcuts views.", Category: ShortcutGroupNavigation},
	{Keys: "Esc / q", Description: "Close the current overlay (q is literal while filtering).", Category: ShortcutGroupNavigation},
	{Keys: "Wheel", Description: "Scroll the transcript or the list under the cursor.", Category: ShortcutGroupNavigation},

	{Keys: "Enter", Description: "Send the prompt; queue it while a turn is running.", Category: ShortcutGroupSession},
	{Keys: "Alt+Enter / Ctrl+J", Description: "Insert a line break in the prompt.", Category: ShortcutGroupSession},
	{Keys: "Ctrl+P / Ctrl+N", Description: "Recall older / newer prompt history.", Category: ShortcutGroupSession},
	{Keys: "Ctrl+C", Description: "Interrupt the turn, clear the prompt, or close an overlay.", Category: ShortcutGroupSession},
	{Keys: "Ctrl+D", Description: "Exit when the prompt is empty and idle; else delete forward.", Category: ShortcutGroupSession},
	{Keys: "Ctrl+S", Description: "Open the $sup support surface.", Category: ShortcutGroupSession},

	{Keys: "?", Description: "Open this Command & Help Center (empty prompt).", Category: ShortcutGroupWorkspace},
	{Keys: "!", Description: "Run a shell command directly in the workspace.", Category: ShortcutGroupWorkspace},
	{Keys: "$sup", Description: "Ask the ephemeral support surface.", Category: ShortcutGroupWorkspace},
	{Keys: "/", Description: "Open inline command discovery in the composer.", Category: ShortcutGroupWorkspace},
	{Keys: "@", Description: "Reference a workspace file.", Category: ShortcutGroupWorkspace},
	{Keys: "Ctrl+U", Description: "Delete from the cursor to the start of the prompt.", Category: ShortcutGroupWorkspace},
	{Keys: "Ctrl+K", Description: "Delete from the cursor to the end of the prompt.", Category: ShortcutGroupWorkspace},
	{Keys: "Ctrl+W", Description: "Delete the word before the cursor.", Category: ShortcutGroupWorkspace},
}

// HelpRoster is one tab's live query and highlight.
//
// The two tabs each own a roster, so switching views preserves the other tab's
// filter and selection instead of resetting them.
type HelpRoster struct {
	// Filter narrows the visible rows.
	Filter string
	// FilterCursor is the rune offset in Filter where typing lands.
	FilterCursor int
	// Cursor selects within the filtered rows.
	Cursor int
}

// clamp bounds the highlight to n rows.
func (r *HelpRoster) clamp(n int) {
	if n <= 0 {
		r.Cursor = 0
		return
	}
	if r.Cursor >= n {
		r.Cursor = n - 1
	}
	if r.Cursor < 0 {
		r.Cursor = 0
	}
}

// Clear empties the query and restores the highlight to the top.
func (r *HelpRoster) Clear() {
	r.Filter = ""
	r.FilterCursor = 0
	r.Cursor = 0
}

// SetText replaces the query, parking the text cursor at the end.
func (r *HelpRoster) SetText(text string) {
	r.Filter = text
	r.FilterCursor = len([]rune(text))
}

// InsertText inserts s at the query cursor.
func (r *HelpRoster) InsertText(s string) {
	if s == "" {
		return
	}
	runes := []rune(r.Filter)
	cur := clampRune(r.FilterCursor, len(runes))
	out := make([]rune, 0, len(runes)+len([]rune(s)))
	out = append(out, runes[:cur]...)
	out = append(out, []rune(s)...)
	out = append(out, runes[cur:]...)
	r.Filter = string(out)
	r.FilterCursor = cur + len([]rune(s))
}

// DeleteBefore removes the rune ahead of the query cursor's left.
func (r *HelpRoster) DeleteBefore() {
	runes := []rune(r.Filter)
	cur := clampRune(r.FilterCursor, len(runes))
	if cur == 0 {
		return
	}
	r.Filter = string(append(runes[:cur-1], runes[cur:]...))
	r.FilterCursor = cur - 1
}

// DeleteAt removes the rune under the query cursor.
func (r *HelpRoster) DeleteAt() {
	runes := []rune(r.Filter)
	cur := clampRune(r.FilterCursor, len(runes))
	if cur >= len(runes) {
		return
	}
	r.Filter = string(append(runes[:cur], runes[cur+1:]...))
	r.FilterCursor = cur
}

// MoveCursor shifts the query cursor, clamped to the text.
func (r *HelpRoster) MoveCursor(delta int) {
	r.FilterCursor = clampRune(r.FilterCursor+delta, len([]rune(r.Filter)))
}

// HelpCenterState is the open Command & Help Center.
//
// It is a plain value embedded in the program state so the render stays pure
// and a test can build one directly. Commands and Shortcuts are snapshots taken
// when the modal opens, so a later registry change cannot make a printed row
// stale underneath it. Each tab's Cursor indexes that tab's filtered rows and
// never a group header, so a selection is always a real row.
type HelpCenterState struct {
	// Commands is the slash-command roster snapshot.
	Commands []CommandSpec
	// Shortcuts is the keybinding roster snapshot.
	Shortcuts []ShortcutSpec
	// View is the active tab.
	View HelpView
	// Command is the Commands tab's query and highlight.
	Command HelpRoster
	// Shortcut is the Shortcuts tab's query and highlight.
	Shortcut HelpRoster
	// VisibleRows bounds the rendered roster window, counting group headers.
	// Zero or negative renders everything (unknown terminal size).
	VisibleRows int
}

// NewHelpCenter opens the modal with a command snapshot and the keybinding
// registry, on the Commands tab.
func NewHelpCenter(commands []CommandSpec) HelpCenterState {
	return HelpCenterState{
		Commands:  append([]CommandSpec(nil), commands...),
		Shortcuts: append([]ShortcutSpec(nil), Keybindings...),
		View:      HelpViewCommands,
	}
}

// ActiveRoster returns the active tab's query and highlight.
func (v *HelpCenterState) ActiveRoster() *HelpRoster {
	if v == nil {
		return nil
	}
	if v.View == HelpViewShortcuts {
		return &v.Shortcut
	}
	return &v.Command
}

// ActiveCursor reports the highlight of the active tab.
func (v *HelpCenterState) ActiveCursor() int {
	if v == nil {
		return 0
	}
	return v.ActiveRoster().Cursor
}

// ActiveFilter reports the query of the active tab.
func (v *HelpCenterState) ActiveFilter() string {
	if v == nil {
		return ""
	}
	return v.ActiveRoster().Filter
}

// SwitchView cycles to another tab by delta, wrapping both ways, and re-clamps
// the target tab's highlight to its own filtered rows. Each tab keeps its own
// query, so the switch is non-destructive.
func (v *HelpCenterState) SwitchView(delta int) {
	if v == nil || delta == 0 {
		return
	}
	next := (int(v.View) + delta) % helpViewCount
	if next < 0 {
		next += helpViewCount
	}
	v.View = HelpView(next)
	v.ActiveRoster().clamp(len(v.ActiveItems()))
}

// ActiveItems returns the active tab's visible rows: every query token matched
// case-insensitively against the row's name, usage, description, and category.
// An empty query matches everything. Tokens are ANDed, so "workspace build"
// narrows to the one row that carries both.
func (v *HelpCenterState) ActiveItems() []helpItem {
	if v == nil {
		return nil
	}
	if v.View == HelpViewShortcuts {
		return filterHelpItems(shortcutItems(v.Shortcuts), v.Shortcut.Filter)
	}
	return filterHelpItems(commandItems(v.Commands), v.Command.Filter)
}

// Selected returns the highlighted row, if any.
func (v *HelpCenterState) Selected() (helpItem, bool) {
	if v == nil {
		return helpItem{}, false
	}
	visible := v.ActiveItems()
	cur := v.ActiveCursor()
	if cur < 0 || cur >= len(visible) {
		return helpItem{}, false
	}
	return visible[cur], true
}

// Move shifts the highlight, wrapping at both ends.
func (v *HelpCenterState) Move(delta int) {
	if v == nil {
		return
	}
	r := v.ActiveRoster()
	n := len(v.ActiveItems())
	if n == 0 {
		r.Cursor = 0
		return
	}
	r.Cursor = (r.Cursor + delta) % n
	if r.Cursor < 0 {
		r.Cursor += n
	}
}

// Page shifts the highlight by approximately one viewport without wrapping:
// paging past an end parks at that end, which is what "one screenful" means.
func (v *HelpCenterState) Page(delta int) {
	if v == nil {
		return
	}
	r := v.ActiveRoster()
	n := len(v.ActiveItems())
	if n == 0 {
		r.Cursor = 0
		return
	}
	step := v.VisibleRows
	if step <= 0 {
		step = 10
	}
	r.Cursor += delta * step
	r.clamp(n)
}

// Top parks the highlight on the first row; Bottom on the last.
func (v *HelpCenterState) Top() {
	if v == nil {
		return
	}
	v.ActiveRoster().Cursor = 0
}

// Bottom parks the highlight on the last row.
func (v *HelpCenterState) Bottom() {
	if v == nil {
		return
	}
	r := v.ActiveRoster()
	if n := len(v.ActiveItems()); n > 0 {
		r.Cursor = n - 1
	} else {
		r.Cursor = 0
	}
}

// SetFilter replaces the active query, parking the text cursor at the end and
// re-clamping the highlight into the narrowed list.
func (v *HelpCenterState) SetFilter(text string) {
	if v == nil {
		return
	}
	r := v.ActiveRoster()
	r.SetText(text)
	r.clamp(len(v.ActiveItems()))
}

// InsertFilterText inserts s at the active query cursor.
func (v *HelpCenterState) InsertFilterText(s string) {
	if v == nil {
		return
	}
	r := v.ActiveRoster()
	r.InsertText(s)
	r.clamp(len(v.ActiveItems()))
}

// DeleteFilterBefore removes the rune ahead of the active query cursor's left.
func (v *HelpCenterState) DeleteFilterBefore() {
	if v == nil {
		return
	}
	r := v.ActiveRoster()
	r.DeleteBefore()
	r.clamp(len(v.ActiveItems()))
}

// DeleteFilterAt removes the rune under the active query cursor.
func (v *HelpCenterState) DeleteFilterAt() {
	if v == nil {
		return
	}
	r := v.ActiveRoster()
	r.DeleteAt()
	r.clamp(len(v.ActiveItems()))
}

// MoveFilterCursor shifts the active query cursor, clamped to the text.
func (v *HelpCenterState) MoveFilterCursor(delta int) {
	if v == nil {
		return
	}
	v.ActiveRoster().MoveCursor(delta)
}

// ClearFilter empties the active query and restores the highlight to the top.
func (v *HelpCenterState) ClearFilter() {
	if v == nil {
		return
	}
	v.ActiveRoster().Clear()
}

// helpItem is one selectable roster row, normalized from a command or a
// shortcut so the two tabs share one filter, window, and layout path.
type helpItem struct {
	// Name is the display token without its prefix: a command word ("model")
	// or a key binding ("Ctrl+S").
	Name string
	// Prefix renders before the name: "/" for a command, "" for a shortcut.
	Prefix string
	// Category is the group header and the badge value.
	Category string
	// Description is the one-line explanation.
	Description string
	// Usage is the argument shape for a command, empty otherwise.
	Usage string
	// Command is the dispatch word for a command row, empty for a shortcut.
	Command string
}

// commandItems normalizes command specs into roster rows.
func commandItems(specs []CommandSpec) []helpItem {
	out := make([]helpItem, 0, len(specs))
	for _, spec := range specs {
		out = append(out, helpItem{
			Name:        spec.Name,
			Prefix:      "/",
			Category:    groupOf(spec),
			Description: spec.Description,
			Usage:       spec.Usage,
			Command:     spec.Name,
		})
		for _, sub := range spec.Subcommands {
			// A subcommand row names one argument form. Choosing it runs the
			// parent command: there is no second dispatch table, and opening
			// the manager is the safe action for every MCP form.
			out = append(out, helpItem{
				Name:        spec.Name,
				Prefix:      "/",
				Category:    groupOf(spec),
				Description: sub.Description,
				Usage:       sub.Usage,
				Command:     spec.Name,
			})
		}
	}
	return out
}

// shortcutItems normalizes shortcut specs into roster rows. A shortcut has no
// runnable command, so choosing one is inert.
func shortcutItems(specs []ShortcutSpec) []helpItem {
	out := make([]helpItem, 0, len(specs))
	for _, spec := range specs {
		out = append(out, helpItem{
			Name:        spec.Keys,
			Category:    spec.Category,
			Description: spec.Description,
		})
	}
	return out
}

// filterHelpItems returns the rows whose searchable text carries every token.
func filterHelpItems(items []helpItem, filter string) []helpItem {
	tokens := queryTokens(filter)
	if len(tokens) == 0 {
		return items
	}
	var out []helpItem
	for _, item := range items {
		if matchHelpItem(item, tokens) {
			out = append(out, item)
		}
	}
	return out
}

// matchHelpItem reports whether every token appears in the row's searchable
// text.
func matchHelpItem(item helpItem, tokens []string) bool {
	hay := strings.ToLower(strings.Join([]string{
		item.Name, item.Usage, item.Description, item.Category,
	}, "\n"))
	for _, tok := range tokens {
		if !strings.Contains(hay, tok) {
			return false
		}
	}
	return true
}

// helpRow is one rendered line of the roster: a group header when Header is
// set, otherwise a selectable row. Index is the row's position in the filtered
// list, or -1 for a header, so selection never lands on a header.
type helpRow struct {
	Header string
	Item   helpItem
	Index  int
}

// rows expands the filtered rows into headers and items, ordered by category. A
// header is emitted whenever the category changes.
func (v *HelpCenterState) rows(items []helpItem) []helpRow {
	out := make([]helpRow, 0, len(items)+3)
	last := ""
	for i, item := range items {
		if item.Category != last {
			out = append(out, helpRow{Header: item.Category, Index: -1})
			last = item.Category
		}
		out = append(out, helpRow{Item: item, Index: i})
	}
	return out
}

// cursorLine returns the rendered line the highlight sits on, or zero when the
// list is empty. It walks headers too, so a group header above the highlight
// cannot push it out of the window.
func (v *HelpCenterState) cursorLine(all []helpRow) int {
	cur := v.ActiveCursor()
	for i, row := range all {
		if row.Header == "" && row.Index == cur {
			return i
		}
	}
	return 0
}

// window returns the roster rows to render. The window follows the highlight so
// it is always on a rendered row, never scrolled off.
func (v *HelpCenterState) window() []helpRow {
	all := v.rows(v.ActiveItems())
	if len(all) == 0 {
		return nil
	}
	limit := v.VisibleRows
	if limit <= 0 || limit >= len(all) {
		return all
	}
	cur := v.cursorLine(all)
	start := 0
	if cur >= limit {
		start = cur - limit + 1
	}
	if start > len(all)-limit {
		start = len(all) - limit
	}
	if start < 0 {
		start = 0
	}
	return all[start : start+limit]
}

// helpCenterVisibleRows resolves the roster window from the terminal size. It
// subtracts the frame and fixed content so the highlight is never scrolled off
// below the action hint. Zero or negative terminal size renders everything.
func helpCenterVisibleRows(s State) int {
	if s.Height <= 0 {
		return 0
	}
	layout := ComputeLayout(s)
	avail := layout.ConversationHeight
	if s.Pending != nil {
		avail -= approvalLines(s.Pending, layout)
	}
	rows := avail - helpOverhead
	if rows < 1 {
		rows = 1
	}
	return rows
}

// helpCardWidth resolves the total dialog width for a terminal width,
// including the border. Unknown sizes resolve to the cap.
func helpCardWidth(termWidth int) int {
	if termWidth <= 0 {
		return helpCardHiWidth
	}
	width := termWidth - helpCardMarginX
	if width > helpCardHiWidth {
		width = helpCardHiWidth
	}
	if width < statusMinContent+2 {
		width = statusMinContent + 2
	}
	if width > termWidth {
		width = termWidth
	}
	return width
}

// helpCategoryAccent is the badge colour for a category. Each category reuses a
// semantic token, so the rosters read apart without a second literal.
func helpCategoryAccent(category string) string {
	switch category {
	case CommandGroupWorkspace, ShortcutGroupNavigation:
		return ColorAccent
	case CommandGroupControl, ShortcutGroupSession:
		return ColorPrimary
	default:
		return ColorModel
	}
}

// helpCategoryWidth resolves the category column for a roster: wide enough for
// its longest label, never narrower than the shared minimum. Computing it from
// the full roster keeps the column stable while the filter narrows the list.
func helpCategoryWidth(items []helpItem) int {
	w := helpColCategory
	for _, item := range items {
		if l := CellWidth(item.Category); l > w {
			w = l
		}
	}
	return w
}

// helpColumnsFor resolves the first column width and whether the category badge
// fits. The category column is shed first so a narrow dialog keeps the name and
// a legible description rather than overlapping text.
func helpColumnsFor(width, catW int) (nameW int, showCategory bool) {
	full := helpColName + helpColGap + catW + helpColGap + helpDescMin
	if width >= full {
		return helpColName, true
	}
	avail := width - helpColGap - helpDescMin
	if avail > helpColName {
		avail = helpColName
	}
	if avail < helpNameMin {
		avail = helpNameMin
	}
	return avail, false
}

// helpCell truncates and right-pads a (possibly styled) cell to width cells.
// Measurement strips ANSI, so a styled cell still aligns.
func helpCell(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return padRight(TruncateToWidth(value, width), width)
}

// helpRowText lays a roster row out at exact column widths. The plain and
// background-tinted variants share this layout through the caller's styles, so
// they cannot drift.
func helpRowText(width int, name, category, desc string, catW int, nameStyle, catStyle, descStyle lipgloss.Style) string {
	nameW, showCategory := helpColumnsFor(width, catW)
	used := nameW + helpColGap
	if showCategory {
		used += catW + helpColGap
	}
	descW := width - used
	if descW < 0 {
		descW = 0
	}
	var b strings.Builder
	b.WriteString(nameStyle.Render(helpCell(name, nameW)))
	if showCategory {
		b.WriteString(strings.Repeat(" ", helpColGap))
		b.WriteString(catStyle.Render(helpCell(category, catW)))
	}
	b.WriteString(strings.Repeat(" ", helpColGap))
	b.WriteString(descStyle.Render(helpCell(desc, descW)))
	return b.String()
}

// renderHelpRow renders one roster row. A group header is a section label; a
// row carries the green cursor and a faint background when highlighted, so the
// selection is unmistakable even without colour.
func renderHelpRow(row helpRow, view HelpCenterState, pal *Palette, interior, catW int) string {
	if row.Header != "" {
		return statusSectionHeader(row.Header, interior)
	}
	item := row.Item
	category := item.Category
	lineW := interior - helpColCursor - helpColGap
	if lineW < 1 {
		lineW = 1
	}

	marker := "  "
	markerStyle := pal.Muted
	nameStyle := pal.Choice
	catStyle := fg(helpCategoryAccent(category)).Bold(true)
	descStyle := pal.Muted
	if row.Index == view.ActiveCursor() {
		// The tint and the green cursor share one background, derived from the
		// UserBG token rather than a second literal, so the row reads as one
		// selected line instead of two adjacent fragments.
		marker = "> "
		bg := pal.UserBG.GetBackground()
		markerStyle = pal.Selected.Background(bg)
		nameStyle = pal.Choice.Background(bg)
		catStyle = fg(helpCategoryAccent(category)).Bold(true).Background(bg)
		descStyle = pal.Muted.Background(bg)
	}
	body := helpRowText(lineW, helpCommandForm(item), category, item.Description, catW, nameStyle, catStyle, descStyle)
	return markerStyle.Render(marker) + strings.Repeat(" ", helpColGap) + body
}

// helpCommandForm renders one command row's full invocation: "/new [name]" for
// a parent command, "/mcp inspect <server>" for a subcommand. A parent's Usage
// repeats the command word, so it is not appended a second time.
func helpCommandForm(item helpItem) string {
	form := item.Prefix + item.Name
	if item.Command == "" || item.Usage == "" || item.Usage == item.Name {
		return form
	}
	if strings.HasPrefix(item.Usage, item.Name+" ") {
		return item.Prefix + item.Usage
	}
	return form + " " + item.Usage
}

// helpTabStrip renders the tab indicators: a filled marker and the accent on
// the active tab, a hollow marker and muted ink on the other. The label itself
// is the signal, so the strip survives a monochrome terminal.
func helpTabStrip(view HelpCenterState, pal *Palette, interior int) string {
	tabs := []struct {
		label  string
		active bool
	}{
		{"Commands", view.View == HelpViewCommands},
		{"Shortcuts", view.View == HelpViewShortcuts},
	}
	var parts []string
	for _, tab := range tabs {
		if tab.active {
			parts = append(parts, pal.Selected.Render("● "+tab.label))
			continue
		}
		parts = append(parts, pal.Muted.Render("○ "+tab.label))
	}
	line := strings.Repeat(" ", statusFieldIndent) + strings.Join(parts, pal.Muted.Render("   "))
	return TruncateToWidth(line, interior)
}

// helpSearchLine draws the live query with a text cursor and the match count.
// The label follows the active tab, so the box never claims to search one view
// while listing the other. When the roster is windowed the count becomes the
// visible range ("2–3/19") so scrolling past the fold is discoverable without a
// separate marker row — the range counts rows, never the group-header lines
// between them.
func helpSearchLine(view HelpCenterState, pal *Palette, interior int) string {
	runes := []rune(sanitizeInline(view.ActiveFilter()))
	cur := clampRune(view.ActiveRoster().FilterCursor, len(runes))
	query := string(runes[:cur]) + pal.Cursor.Render(" ") + string(runes[cur:])
	shown := view.window()
	total := len(view.ActiveItems())
	first, last := -1, -1
	for _, row := range shown {
		if row.Header != "" {
			continue
		}
		if first < 0 {
			first = row.Index
		}
		last = row.Index
	}
	count := fmt.Sprintf("  [%d]", total)
	if first > 0 || last < total-1 {
		count = fmt.Sprintf("  [%d–%d/%d]", first+1, last+1, total)
	}
	return TruncateToWidth(helpSearchLabel(view.View)+query+pal.Muted.Render(count), interior)
}

// helpSearchLabel names the active tab in the search box.
func helpSearchLabel(view HelpView) string {
	if view == HelpViewShortcuts {
		return "Search shortcuts… "
	}
	return "Search commands… "
}

// helpEmptyLabel names what a filter matched nothing in, for the active tab's
// empty state.
func helpEmptyLabel(view HelpView) string {
	if view == HelpViewShortcuts {
		return "no matching shortcuts"
	}
	return "no matching commands"
}

// helpFirstColumn names the first column for the active tab.
func helpFirstColumn(view HelpView) string {
	if view == HelpViewShortcuts {
		return "KEY / BINDING"
	}
	return "COMMAND"
}

// helpTableHeader renders the muted column header, aligned with the rows by the
// shared column resolver.
func helpTableHeader(view HelpCenterState, pal *Palette, interior, catW int) string {
	lineW := interior - helpColCursor - helpColGap
	if lineW < 1 {
		lineW = 1
	}
	nameW, showCategory := helpColumnsFor(lineW, catW)
	used := nameW + helpColGap
	if showCategory {
		used += catW + helpColGap
	}
	descW := lineW - used
	if descW < 0 {
		descW = 0
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", helpColCursor+helpColGap))
	b.WriteString(pal.Muted.Render(helpCell(helpFirstColumn(view.View), nameW)))
	if showCategory {
		b.WriteString(strings.Repeat(" ", helpColGap))
		b.WriteString(pal.Muted.Render(helpCell("CATEGORY", catW)))
	}
	b.WriteString(strings.Repeat(" ", helpColGap))
	b.WriteString(pal.Muted.Render(helpCell("DESCRIPTION", descW)))
	return TruncateToWidth(b.String(), interior)
}

// helpSelectedBlock renders the detail for the highlighted row: its binding or
// usage, its description, and what choosing it does. A command runs; a shortcut
// is inert and says so instead of promising an action.
func helpSelectedBlock(view HelpCenterState, pal *Palette, interior int) []string {
	rows := []string{statusSectionHeader("SELECTED", interior)}
	item, ok := view.Selected()
	if !ok {
		return append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render(helpEmptyLabel(view.View)))
	}
	line := strings.Repeat(" ", statusFieldIndent) + pal.Choice.Render(sanitizeInline(helpCommandForm(item)))
	if item.Description != "" {
		line += "  " + pal.Muted.Render("· "+sanitizeInline(item.Description))
	}
	rows = append(rows, TruncateToWidth(line, interior))
	hint := "↵ runs the command · type the command to pass arguments"
	if item.Command == "" {
		hint = "shortcuts work in the surface they belong to"
	}
	rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render(hint))
	return rows
}

// helpCenterContent builds the dialog body: the tab strip, the search bar, the
// column header, the categorized roster, and the highlighted row's detail.
func helpCenterContent(view HelpCenterState, pal *Palette, interior int) []string {
	catW := helpCategoryWidth(allActiveItems(view))
	rows := []string{helpTabStrip(view, pal, interior)}
	rows = append(rows, helpSearchLine(view, pal, interior))
	rows = append(rows, statusRule(pal, interior))
	rows = append(rows, helpTableHeader(view, pal, interior, catW))
	shown := view.window()
	if len(shown) == 0 {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render(helpEmptyLabel(view.View)))
	} else {
		for _, row := range shown {
			rows = append(rows, renderHelpRow(row, view, pal, interior, catW))
		}
	}
	rows = append(rows, statusRule(pal, interior))
	rows = append(rows, helpSelectedBlock(view, pal, interior)...)
	return rows
}

// allActiveItems returns the active tab's full roster, unfiltered, so the
// category column is sized from every label the roster can show.
func allActiveItems(view HelpCenterState) []helpItem {
	if view.View == HelpViewShortcuts {
		return shortcutItems(view.Shortcuts)
	}
	return commandItems(view.Commands)
}

// helpFooterBar is the structured action line: the keys in bold, their meaning
// muted, so it names exactly what works right now. It advertises the tab
// switch, since that is the one binding unique to this surface.
func helpFooterBar(pal *Palette, interior int) string {
	sep := pal.Muted.Render("  ·  ")
	line := strings.Repeat(" ", statusFieldIndent) +
		pal.Bold.Render("⇥") + pal.Muted.Render(" tab view") + sep +
		pal.Bold.Render("↑↓") + pal.Muted.Render(" navigate") + sep +
		pal.Muted.Render("type to filter") + sep +
		pal.Bold.Render("Esc/q") + pal.Muted.Render(" close")
	return TruncateToWidth(line, interior)
}

// RenderHelpCenter composes the Command & Help Center as an ANSI string.
//
// width is the total rendered width, border included; height, when positive,
// bounds the total rendered height so the dialog never overflows a short
// viewport. The caller centers the returned block in the terminal.
func RenderHelpCenter(view HelpCenterState, pal *Palette, width, height int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	if width <= 0 {
		width = helpCardHiWidth
	}
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}
	content := helpCenterContent(view, pal, interior)
	// The title border, the footer rule, the footer bar, and the bottom
	// border claim four rows; everything else is body.
	if height > 0 {
		content = fitStatusRows(content, height-4)
	}
	footer := helpFooterBar(pal, interior)
	return renderModalFrameWithFooter(helpModalTitle, content, footer, pal, interior)
}

// renderHelpArea draws the Command & Help Center centered over the workbench.
// The header and status chrome keep rendering around it and the conversation is
// untouched: dismissing returns to the previous surface with nothing lost.
func renderHelpArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	view := state.HelpModal
	if view == nil {
		return
	}
	renderCardAreaWidth(sb, state, layout, helpCardWidth, func(width, height int) string {
		return RenderHelpCenter(*view, pal, width, height)
	})
}

// openHelpCenter opens the Command & Help Center on the Commands tab with the
// authoritative registry. A non-empty filter seeds the search box, which is how
// the "/" tab trigger hands the typed prefix over. The roster window is sized by
// the HelpCenterOpened transition, so every opener gets the same fit.
func (p *Program) openHelpCenter(filter string) {
	view := NewHelpCenter(filterCommands(""))
	if filter != "" {
		view.SetFilter(filter)
	}
	p.state.Apply(HelpCenterOpened{View: view})
}

// closeHelpCenter dismisses the modal, restoring composer text saved when it
// opened over typing. Closing never selects.
func (p *Program) closeHelpCenter() {
	p.state.Apply(HelpCenterClosed{})
	if p.pickerRestoreSet {
		p.state.Apply(InputChanged{Text: p.pickerRestore})
		p.pickerRestore = ""
		p.pickerRestoreSet = false
	}
}

// handleHelpCenterKey routes keys to the open Command & Help Center.
//
// Precedence is fixed: esc clears a non-empty query first and closes only when
// already empty; q closes from an empty query (with text it is a query
// character); ctrl+c always closes; tab/shift+tab switch views; enter runs a
// highlighted command; up/down/j/k and pgup/pgdn/home/end navigate;
// left/right edit the query; printable input filters. Nothing else reaches the
// composer hidden underneath.
func (p *Program) handleHelpCenterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	view := p.state.HelpModal
	if view == nil {
		return p, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		if msg.String() == "esc" && view.ActiveFilter() != "" {
			p.state.Apply(HelpCenterFilterCleared{})
			return p, nil
		}
		p.closeHelpCenter()
	case "q":
		// q closes the modal, but only while the query is empty: once the user
		// is filtering, a letter must stay a letter.
		if view.ActiveFilter() == "" {
			p.closeHelpCenter()
			return p, nil
		}
		p.state.Apply(HelpCenterTyped{Text: "q"})
	case "tab":
		p.state.Apply(HelpCenterViewSwitched{Delta: 1})
	case "shift+tab":
		p.state.Apply(HelpCenterViewSwitched{Delta: -1})
	case "enter":
		return p.chooseHelpRow()
	case "up", "k", "ctrl+p":
		p.state.Apply(HelpCenterMoved{Delta: -1})
	case "down", "j", "ctrl+n":
		p.state.Apply(HelpCenterMoved{Delta: 1})
	case "pgup":
		p.state.Apply(HelpCenterPaged{Delta: -1})
	case "pgdown":
		p.state.Apply(HelpCenterPaged{Delta: 1})
	case "home", "ctrl+home":
		p.state.Apply(HelpCenterTop{})
	case "end", "ctrl+end":
		p.state.Apply(HelpCenterBottom{})
	case "left", "ctrl+b":
		p.state.Apply(HelpCenterFilterCursorMoved{Delta: -1})
	case "right", "ctrl+f":
		p.state.Apply(HelpCenterFilterCursorMoved{Delta: 1})
	case "ctrl+a":
		p.state.Apply(HelpCenterFilterCursorMoved{Delta: -len([]rune(view.ActiveFilter()))})
	case "ctrl+e":
		p.state.Apply(HelpCenterFilterCursorMoved{Delta: len([]rune(view.ActiveFilter()))})
	case "backspace", "ctrl+h":
		p.state.Apply(HelpCenterBackspaced{})
	case "delete", "ctrl+d":
		p.state.Apply(HelpCenterDeleted{})
	case "ctrl+u":
		p.state.Apply(HelpCenterFilterCleared{})
	default:
		// Space arrives as KeySpace, not KeyRunes: accept both, or a multiword
		// search silently loses its spaces.
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			text := string(msg.Runes)
			if msg.Type == tea.KeySpace {
				text = " "
			}
			if text != "" {
				p.state.Apply(HelpCenterTyped{Text: text})
			}
		}
	}
	return p, nil
}

// chooseHelpRow runs the highlighted command, then closes the modal.
//
// A shortcut row is inert: it teaches a binding, so there is nothing to run and
// the modal stays open for browsing. The two listing commands close the modal
// but run nothing — they would only reopen the surface the user just left.
func (p *Program) chooseHelpRow() (tea.Model, tea.Cmd) {
	view := p.state.HelpModal
	if view == nil {
		return p, nil
	}
	item, ok := view.Selected()
	if !ok || item.Command == "" {
		return p, nil
	}
	p.state.Apply(HelpCenterClosed{})
	p.pickerRestore = ""
	p.pickerRestoreSet = false
	switch item.Command {
	case "help", "commands":
		return p, nil
	}
	p.state.Apply(InputChanged{Text: ""})
	return p, p.runCommand(Command{Name: item.Command, Raw: "/" + item.Command})
}
