package tui

// The /sessions modal: a centered Session Manager that turns the flat session
// picker into a navigable roster with a details pane and write actions.
//
// It is a working modal, unlike the read-only /status and /usage cards: it
// owns the keyboard, moves its own cursor, and dispatches write actions back
// to the program (resume, new, rename, archive, delete). Its frame reuses the
// shared modal frame — a border-embedded title, interior-padded rows, a rule
// above a structured footer bar — so the surface reads as one system with the
// model control plane and the status card. The roster is a snapshot taken when
// it opens; an action re-lists so the table never shows a row that no longer
// exists.
//
// Two interaction modes are layered on top of browsing: an inline rename line
// and a destructive-action confirmation. Both live inside the modal rather
// than opening a second overlay, so one surface always owns the keyboard.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Session manager geometry. The dialog is responsive: it grows with the
// terminal up to a comfortable reading width and never touches the edges while
// margins fit.
const (
	// sessionCardHiWidth caps the dialog so ultrawide terminals keep context
	// on both sides.
	sessionCardHiWidth = 96
	// sessionCardMarginX is the total horizontal margin (both sides).
	sessionCardMarginX = 8
	// sessionModalTitle is the border-embedded title, matching the plane's
	// "MODEL CONTROL · …" and the status card's "SYSTEM STATUS" treatment.
	sessionModalTitle = "SESSION MANAGER"
)

// Session table column widths, in cells. The title column absorbs whatever
// remains; the optional columns drop at narrow interiors so the table never
// collapses into overlapping text.
const (
	sessionColCursor = 2
	sessionColStatus = 6
	sessionColSlot   = 6
	sessionColDirty  = 6
	sessionColLast   = 16
	sessionColGap    = 2
	// sessionTitleMin is the narrowest usable title column before optional
	// columns are shed.
	sessionTitleMin = 10
)

// SessionManagerMode selects the interaction layered on top of browsing.
type SessionManagerMode int

const (
	// SessionBrowse is the default: the cursor navigates the roster and the
	// action keys are live.
	SessionBrowse SessionManagerMode = iota
	// SessionRename collects a new title in the modal's own line.
	SessionRename
	// SessionConfirm gates a destructive action behind a yes/no answer.
	SessionConfirm
)

// SessionManagerState is the open /sessions modal.
//
// It is a plain value embedded in the program state so the render stays pure
// and a test can build one directly. Items is a snapshot: the manager never
// reads the store itself; the program re-lists and pushes the result back.
type SessionManagerState struct {
	// Items are the stored sessions, in the order the store gave them
	// (newest first).
	Items []SessionItem
	// Cursor selects within Items.
	Cursor int
	// Offset is the first visible table row.
	Offset int
	// VisibleRows bounds the rendered roster window. Zero or negative renders
	// everything (unknown terminal size).
	VisibleRows int
	// ActiveID is the program's current session id, or zero when none. It
	// marks the loaded row so "active" is never confused with "highlighted".
	ActiveID int64
	// Mode selects the interaction: browse, inline rename, or confirm.
	Mode SessionManagerMode
	// Rename holds the edit buffer while Mode is SessionRename.
	Rename string
	// ConfirmID is the session a pending destructive action targets.
	ConfirmID int64
	// Confirm names that action in words while Mode is SessionConfirm.
	Confirm string
	// Notice reports the last action's outcome in words. Empty when nothing
	// has happened since the modal opened.
	Notice string
}

// Selected returns the highlighted session, if any.
func (m *SessionManagerState) Selected() (SessionItem, bool) {
	if m == nil || m.Cursor < 0 || m.Cursor >= len(m.Items) {
		return SessionItem{}, false
	}
	return m.Items[m.Cursor], true
}

// Move shifts the highlight, wrapping at both ends, keeping it visible.
func (m *SessionManagerState) Move(delta int) {
	if len(m.Items) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	m.Cursor = (m.Cursor + delta) % len(m.Items)
	if m.Cursor < 0 {
		m.Cursor += len(m.Items)
	}
	m.EnsureVisible()
}

// Page shifts the highlight by approximately one viewport without wrapping:
// paging past an end parks at that end.
func (m *SessionManagerState) Page(delta int) {
	if len(m.Items) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	step := m.VisibleRows
	if step <= 0 {
		step = 10
	}
	m.Cursor += delta * step
	m.clamp()
	m.EnsureVisible()
}

// Top parks the highlight on the first session; Bottom on the last.
func (m *SessionManagerState) Top() {
	if len(m.Items) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	m.Cursor = 0
	m.EnsureVisible()
}

// Bottom parks the highlight on the last session.
func (m *SessionManagerState) Bottom() {
	if len(m.Items) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	m.Cursor = len(m.Items) - 1
	m.EnsureVisible()
}

// SetVisibleRows resizes the roster window, e.g. on WindowSizeMsg, keeping the
// highlight on screen.
func (m *SessionManagerState) SetVisibleRows(rows int) {
	if rows < 0 {
		rows = 0
	}
	m.VisibleRows = rows
	m.EnsureVisible()
}

// SetItems replaces the roster, preserving the highlight by id when the
// selected session survives and clamping otherwise.
func (m *SessionManagerState) SetItems(items []SessionItem) {
	selected := int64(0)
	if cur, ok := m.Selected(); ok {
		selected = cur.ID
	}
	m.Items = append([]SessionItem(nil), items...)
	m.Cursor = 0
	if selected != 0 {
		for i, item := range items {
			if item.ID == selected {
				m.Cursor = i
				break
			}
		}
	}
	m.clamp()
	m.EnsureVisible()
}

// clamp bounds the highlight to the roster.
func (m *SessionManagerState) clamp() {
	if len(m.Items) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	if m.Cursor >= len(m.Items) {
		m.Cursor = len(m.Items) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

// EnsureVisible adjusts the scroll offset so the highlight is on screen.
func (m *SessionManagerState) EnsureVisible() {
	n := len(m.Items)
	if n == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	m.clamp()
	rows := m.VisibleRows
	if rows <= 0 || rows >= n {
		m.Offset = 0
		return
	}
	if m.Cursor < m.Offset {
		m.Offset = m.Cursor
	}
	if m.Cursor >= m.Offset+rows {
		m.Offset = m.Cursor - rows + 1
	}
	if maxOffset := n - rows; m.Offset > maxOffset {
		m.Offset = maxOffset
	}
	if m.Offset < 0 {
		m.Offset = 0
	}
}

// Window returns the rows to render: at most VisibleRows starting at Offset.
func (m *SessionManagerState) Window() []SessionItem {
	if m.VisibleRows <= 0 || len(m.Items) <= m.VisibleRows {
		return m.Items
	}
	m.EnsureVisible()
	end := m.Offset + m.VisibleRows
	if end > len(m.Items) {
		end = len(m.Items)
	}
	return m.Items[m.Offset:end]
}

// sessionCardWidth resolves the total dialog width for a terminal width,
// including the border. Unknown sizes resolve to the cap.
func sessionCardWidth(termWidth int) int {
	if termWidth <= 0 {
		return sessionCardHiWidth
	}
	width := termWidth - sessionCardMarginX
	if width > sessionCardHiWidth {
		width = sessionCardHiWidth
	}
	if width < statusMinContent+2 {
		width = statusMinContent + 2
	}
	if width > termWidth {
		width = termWidth
	}
	return width
}

// sessionManagerVisibleRows resolves the roster window from the terminal size.
// It subtracts the frame and the fixed content overhead so the highlight is
// never scrolled off below the details pane.
func sessionManagerVisibleRows(state State) int {
	if state.Width <= 0 || state.Height <= 0 {
		return 0
	}
	layout := ComputeLayout(state)
	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	// Frame (top/bottom border, footer rule, footer bar) plus the fixed
	// content: SESSIONS header, column header, an overflow/rule row, the
	// DETAILS header, and up to five detail fields.
	const overhead = 4 + 1 + 1 + 1 + 1 + 5
	rows := avail - overhead
	if rows < 1 {
		rows = 1
	}
	return rows
}

// sessionTableColumns resolves the visible table columns for an interior
// width. The title column absorbs the remainder; the optional columns are
// added from the right while the title keeps sessionTitleMin.
type sessionTableColumns struct {
	title int
	slot  bool
	dirty bool
	last  bool
}

// sessionColumnsFor resolves the column layout for an interior width.
//
// The cursor, a gap, and the status column are fixed. Each optional column and
// the title cost their own leading gap plus their width, so the sum of the
// present columns always fits the interior exactly. Optional columns are added
// from the right (last, dirty, then slot) while the title keeps its minimum.
func sessionColumnsFor(interior int) sessionTableColumns {
	var cols sessionTableColumns
	remaining := interior - sessionColCursor - sessionColStatus - sessionColGap
	titleCost := sessionColGap + sessionTitleMin
	if remaining < titleCost {
		cols.title = maxInt(remaining-sessionColGap, 1)
		return cols
	}
	add := func(present *bool, width int) {
		cost := sessionColGap + width
		if remaining-cost >= titleCost {
			*present = true
			remaining -= cost
		}
	}
	add(&cols.last, sessionColLast)
	add(&cols.dirty, sessionColDirty)
	add(&cols.slot, sessionColSlot)
	cols.title = remaining - sessionColGap
	return cols
}

// sessionCell truncates and right-pads a (possibly styled) cell to width
// cells. Measurement strips ANSI, so a styled cell still aligns.
func sessionCell(value string, width int) string {
	return padRight(TruncateToWidth(value, width), width)
}

// sessionTableHeader renders the muted column header row.
func sessionTableHeader(cols sessionTableColumns, pal *Palette, interior int) string {
	cell := func(text string, width int) string {
		return sessionCell(pal.Muted.Render(TruncateToWidth(text, width)), width)
	}
	var b strings.Builder
	b.WriteString(cell("", sessionColCursor))
	b.WriteString(strings.Repeat(" ", sessionColGap))
	b.WriteString(cell("STATUS", sessionColStatus))
	if cols.slot {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(cell("SLOT", sessionColSlot))
	}
	b.WriteString(strings.Repeat(" ", sessionColGap))
	b.WriteString(cell("TITLE / GOAL", cols.title))
	if cols.dirty {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(cell("DIRTY", sessionColDirty))
	}
	if cols.last {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(cell("LAST ACTIVE", sessionColLast))
	}
	return TruncateToWidth(b.String(), interior)
}

// sessionStatusCell renders the STATUS column: a filled marker for the loaded
// session, a hollow one otherwise. The glyph carries the state so a monochrome
// terminal loses nothing.
func sessionStatusCell(item SessionItem, pal *Palette) (string, lipgloss.Style) {
	if item.Active {
		return "●", pal.ToolDone
	}
	return "○", pal.Muted
}

// sessionDirtyCell renders the DIRTY column. A measured dirty tree is amber;
// a measured clean tree is dim; an unmeasured tree is a neutral dot so the
// table never presents an unknown as a clean result. The words themselves are
// the markers, so the state survives a monochrome terminal.
func sessionDirtyCell(item SessionItem, pal *Palette) (string, lipgloss.Style) {
	switch {
	case !item.DirtyKnown:
		return "·", pal.Muted
	case item.Dirty:
		return "dirty", pal.ToolRun
	default:
		return "clean", pal.Muted
	}
}

// renderSessionTableRow renders one roster row. The highlighted row carries a
// green cursor and a faint background so the selection is unmistakable; the
// loaded session carries the filled STATUS marker independently.
func renderSessionTableRow(cols sessionTableColumns, view SessionManagerState, item SessionItem, index int, pal *Palette, interior int) string {
	selected := index == view.Cursor
	marker := "  "
	markerStyle := pal.Muted
	if selected {
		marker = "> "
		markerStyle = pal.Selected
	}
	status, statusStyle := sessionStatusCell(item, pal)
	title := statusValueOr(sanitizeInline(item.Title), statusValueOr(sanitizeInline(item.Model), "(untitled)"))
	slot := fmt.Sprintf("#%d", item.ID)
	last := statusValueOr(sanitizeInline(item.Updated), "—")
	dirty, dirtyStyle := sessionDirtyCell(item, pal)

	if selected {
		// The whole row shares one background, so the cells are built plain
		// and the line is styled once.
		plain := sessionRowPlain(cols, marker, status, slot, item.Title, item.Model, dirty, last)
		return pal.UserBG.Render(padRight(plain, interior))
	}

	var b strings.Builder
	b.WriteString(sessionCell(markerStyle.Render(marker), sessionColCursor))
	b.WriteString(strings.Repeat(" ", sessionColGap))
	b.WriteString(sessionCell(statusStyle.Render(status), sessionColStatus))
	if cols.slot {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(sessionCell(pal.Choice.Render(TruncateToWidth(slot, sessionColSlot)), sessionColSlot))
	}
	b.WriteString(strings.Repeat(" ", sessionColGap))
	b.WriteString(sessionCell(pal.Choice.Render(TruncateToWidth(title, cols.title)), cols.title))
	if cols.dirty {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(sessionCell(dirtyStyle.Render(TruncateToWidth(dirty, sessionColDirty)), sessionColDirty))
	}
	if cols.last {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(sessionCell(pal.Muted.Render(TruncateToWidth(last, sessionColLast)), sessionColLast))
	}
	return TruncateToWidth(b.String(), interior)
}

// sessionRowPlain builds the unstyled row text at exact column widths. It is
// the backing text for the selected row, whose background cannot survive
// nested style resets.
func sessionRowPlain(cols sessionTableColumns, marker, status, slot, titleRaw, model, dirty, last string) string {
	title := statusValueOr(sanitizeInline(titleRaw), statusValueOr(sanitizeInline(model), "(untitled)"))
	var b strings.Builder
	b.WriteString(sessionCell(marker, sessionColCursor))
	b.WriteString(strings.Repeat(" ", sessionColGap))
	b.WriteString(sessionCell(status, sessionColStatus))
	if cols.slot {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(sessionCell(slot, sessionColSlot))
	}
	b.WriteString(strings.Repeat(" ", sessionColGap))
	b.WriteString(sessionCell(title, cols.title))
	if cols.dirty {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(sessionCell(dirty, sessionColDirty))
	}
	if cols.last {
		b.WriteString(strings.Repeat(" ", sessionColGap))
		b.WriteString(sessionCell(last, sessionColLast))
	}
	return b.String()
}

// sessionTokenLine renders the detail token row. An unmeasured count stays
// absent rather than rendering a fabricated zero; a measured count is
// prefixed with "~" because it is reconstructed from stored text.
func sessionTokenLine(item SessionItem) string {
	if item.Tokens <= 0 {
		return "not measured"
	}
	return "~" + formatCount(item.Tokens) + " tokens"
}

// sessionDetails builds the bottom details block for the highlighted session,
// or the inline editor when a mode owns it.
func sessionDetails(view SessionManagerState, pal *Palette, interior int) []string {
	switch view.Mode {
	case SessionRename:
		return sessionRenameBlock(view, pal, interior)
	case SessionConfirm:
		return sessionConfirmBlock(view, pal, interior)
	}
	rows := []string{statusSectionHeader("DETAILS", interior)}
	item, ok := view.Selected()
	if !ok {
		return append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render("nothing selected"))
	}
	field := func(label, value string) []string {
		return modalField(label, value, pal, interior, 0, detailFieldLabelWidth)
	}
	rows = append(rows, field("Title", statusValueOr(sanitizeInline(item.Title), "(untitled)"))...)
	rows = append(rows, field("Tokens", sessionTokenLine(item))...)
	rows = append(rows, field("Model", statusValueOr(sanitizeInline(item.Model), "no model"))...)
	rows = append(rows, field("Prompt", statusValueOr(sanitizeInline(item.Prompt), "(no prompt recorded)"))...)
	return rows
}

// sessionRenameBlock renders the inline rename editor: the live buffer with a
// block cursor and the keys that commit or abandon it.
func sessionRenameBlock(view SessionManagerState, pal *Palette, interior int) []string {
	rows := []string{statusSectionHeader("RENAME SESSION", interior)}
	buffer := sanitizeInline(view.Rename)
	line := strings.Repeat(" ", statusFieldIndent) + pal.Muted.Render(padModalLabel("Title", detailFieldLabelWidth)) +
		pal.Choice.Render(buffer) + pal.Cursor.Render(" ")
	rows = append(rows, TruncateToWidth(line, interior))
	rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render("type a new title · ↵ confirm · Esc cancel"))
	return rows
}

// sessionConfirmBlock renders the destructive-action confirmation in place of
// the details, so the target and the consequence stay on screen together.
func sessionConfirmBlock(view SessionManagerState, pal *Palette, interior int) []string {
	rows := []string{statusSectionHeader("CONFIRM", interior)}
	label := statusValueOr(sanitizeInline(view.Confirm), "this session")
	rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.ToolRun.Render(TruncateToWidth(label, interior-statusFieldIndent)))
	rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render("this cannot be undone · y confirm · n cancel"))
	return rows
}

// sessionManagerContent builds the dialog body: the roster table, a full-width
// rule, then the details (or the mode's editor).
func sessionManagerContent(view SessionManagerState, pal *Palette, interior int) []string {
	cols := sessionColumnsFor(interior)
	rows := []string{statusSectionHeader("SESSIONS", interior)}
	rows = append(rows, sessionTableHeader(cols, pal, interior))
	window := view.Window()
	if len(window) == 0 {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render("no saved sessions"))
	} else {
		for i, item := range window {
			rows = append(rows, renderSessionTableRow(cols, view, item, view.Offset+i, pal, interior))
		}
		if hidden := len(view.Items) - view.Offset - len(window); hidden > 0 {
			rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render(fmt.Sprintf("… %d more", hidden)))
		}
	}
	rows = append(rows, statusRule(pal, interior))
	rows = append(rows, sessionDetails(view, pal, interior)...)
	if view.Notice != "" {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.ToolRun.Render(TruncateToWidth(sanitizeInline(view.Notice), interior-statusFieldIndent)))
	}
	return rows
}

// sessionFooterBar is the structured action line. It follows the mode so it
// always names the keys that actually work right now.
func sessionFooterBar(view SessionManagerState, pal *Palette, interior int) string {
	line := strings.Repeat(" ", statusFieldIndent)
	switch view.Mode {
	case SessionRename:
		line += pal.Bold.Render("↵") + pal.Muted.Render(" confirm  ·  ") +
			pal.Bold.Render("Esc") + pal.Muted.Render(" cancel rename")
	case SessionConfirm:
		line += pal.Bold.Render("y") + pal.Muted.Render(" confirm  ·  ") +
			pal.Bold.Render("n") + pal.Muted.Render(" / ") + pal.Bold.Render("Esc") + pal.Muted.Render(" cancel")
	default:
		line += pal.Bold.Render("↵") + pal.Muted.Render(" resume  ·  ") +
			pal.Bold.Render("n") + pal.Muted.Render(" new  ·  ") +
			pal.Bold.Render("r") + pal.Muted.Render(" rename  ·  ") +
			pal.Bold.Render("a") + pal.Muted.Render(" archive  ·  ") +
			pal.Bold.Render("d") + pal.Muted.Render(" delete  ·  ") +
			pal.Bold.Render("Esc/q") + pal.Muted.Render(" close")
	}
	return TruncateToWidth(line, interior)
}

// RenderSessionManager composes the Session Manager as an ANSI string.
//
// width is the total rendered width, border included; height, when positive,
// bounds the total rendered height so the dialog never overflows a short
// viewport. The caller centers the returned block in the terminal.
func RenderSessionManager(view SessionManagerState, pal *Palette, width, height int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	if width <= 0 {
		width = sessionCardHiWidth
	}
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}
	content := sessionManagerContent(view, pal, interior)
	// The title border, the footer rule, the footer bar, and the bottom
	// border claim four rows; everything else is body.
	if height > 0 {
		content = fitStatusRows(content, height-4)
	}
	footer := sessionFooterBar(view, pal, interior)
	return renderModalFrameWithFooter(sessionModalTitle, content, footer, pal, interior)
}

// renderSessionArea draws the Session Manager centered over the workbench. The
// header and status chrome keep rendering around it and the conversation is
// untouched: dismissing returns to the previous surface with nothing lost.
func renderSessionArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	view := state.SessionModal
	if view == nil {
		return
	}
	renderCardAreaWidth(sb, state, layout, sessionCardWidth, func(width, height int) string {
		return RenderSessionManager(*view, pal, width, height)
	})
}

// handleSessionManagerKey routes keys to the open Session Manager.
//
// Precedence is fixed: a modal mode (rename, confirm) owns every key while it
// is active; esc/q close from browse; enter resumes; arrows/j/k move; and
// n/r/a/d start their actions. Nothing else reaches the composer hidden
// underneath.
func (p *Program) handleSessionManagerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	view := p.state.SessionModal
	if view == nil {
		return p, nil
	}
	switch view.Mode {
	case SessionRename:
		return p.handleSessionRenameKey(msg)
	case SessionConfirm:
		return p.handleSessionConfirmKey(msg)
	}

	switch msg.String() {
	case "esc", "q", "ctrl+c":
		p.closeSessionManager()
	case "enter":
		return p.resumeSelectedSession()
	case "up", "k", "ctrl+p", "shift+tab":
		p.state.Apply(SessionManagerMoved{Delta: -1})
	case "down", "j", "ctrl+n", "tab":
		p.state.Apply(SessionManagerMoved{Delta: 1})
	case "pgup":
		p.state.Apply(SessionManagerPaged{Delta: -1})
	case "pgdown":
		p.state.Apply(SessionManagerPaged{Delta: 1})
	case "home", "g":
		p.state.Apply(SessionManagerTop{})
	case "end", "G":
		p.state.Apply(SessionManagerBottom{})
	case "n":
		return p.newSessionFromManager()
	case "r":
		p.state.Apply(SessionManagerRenameStarted{})
	case "a":
		return p.archiveSelectedSession()
	case "d":
		p.state.Apply(SessionManagerConfirmAsked{ID: p.selectedSessionID(), Action: p.selectedSessionDeleteLabel()})
	}
	return p, nil
}

// handleSessionRenameKey edits the inline rename buffer.
func (p *Program) handleSessionRenameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		p.state.Apply(SessionManagerModeCancelled{})
	case "enter":
		return p.commitSessionRename()
	case "backspace", "ctrl+h":
		runes := []rune(p.state.SessionModal.Rename)
		if len(runes) > 0 {
			p.state.Apply(SessionManagerRenameChanged{Text: string(runes[:len(runes)-1])})
		}
	case "ctrl+u":
		p.state.Apply(SessionManagerRenameChanged{Text: ""})
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			chunk := string(msg.Runes)
			if msg.Type == tea.KeySpace {
				chunk = " "
			}
			if chunk != "" {
				p.state.Apply(SessionManagerRenameChanged{Text: p.state.SessionModal.Rename + chunk})
			}
		}
	}
	return p, nil
}

// handleSessionConfirmKey answers the destructive-action confirmation.
func (p *Program) handleSessionConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		return p.confirmSessionAction()
	case "n", "N", "esc", "ctrl+c", "q":
		p.state.Apply(SessionManagerModeCancelled{})
	}
	return p, nil
}

// closeSessionManager dismisses the manager without acting.
func (p *Program) closeSessionManager() {
	p.state.Apply(SessionManagerClosed{})
}

// resumeSelectedSession switches to the highlighted session and closes the
// manager.
func (p *Program) resumeSelectedSession() (tea.Model, tea.Cmd) {
	item, ok := p.state.SessionModal.Selected()
	if !ok {
		return p, nil
	}
	p.state.Apply(SessionManagerClosed{})
	return p, p.resumeSessionByID(item.ID)
}

// selectedSessionID returns the highlighted session id, or zero when none.
func (p *Program) selectedSessionID() int64 {
	if item, ok := p.state.SessionModal.Selected(); ok {
		return item.ID
	}
	return 0
}

// selectedSessionDeleteLabel names the highlighted session for the delete
// confirmation.
func (p *Program) selectedSessionDeleteLabel() string {
	item, ok := p.state.SessionModal.Selected()
	if !ok {
		return ""
	}
	title := statusValueOr(sanitizeInline(item.Title), statusValueOr(sanitizeInline(item.Model), "untitled"))
	return fmt.Sprintf("delete session #%d (%s) and its transcript?", item.ID, title)
}

// commitSessionRename persists the edited title and re-lists the roster.
func (p *Program) commitSessionRename() (tea.Model, tea.Cmd) {
	view := p.state.SessionModal
	item, ok := view.Selected()
	if !ok {
		p.state.Apply(SessionManagerModeCancelled{})
		return p, nil
	}
	title := strings.TrimSpace(view.Rename)
	if p.sessionActions.Rename == nil {
		p.state.Apply(SessionManagerNoticed{Text: "rename is unavailable"})
		return p, nil
	}
	if err := p.sessionActions.Rename(item.ID, title); err != nil {
		p.state.Apply(SessionManagerNoticed{Text: "rename: " + err.Error()})
		return p, nil
	}
	p.reloadSessionManager(fmt.Sprintf("renamed session #%d", item.ID))
	return p, nil
}

// newSessionFromManager creates and switches to a fresh session.
func (p *Program) newSessionFromManager() (tea.Model, tea.Cmd) {
	if p.sessionActions.New == nil {
		p.state.Apply(SessionManagerNoticed{Text: "new session is unavailable"})
		return p, nil
	}
	sess, opened, err := p.sessionActions.New()
	if err != nil {
		p.state.Apply(SessionManagerNoticed{Text: "new session: " + err.Error()})
		return p, nil
	}
	p.state.Apply(SessionManagerClosed{})
	p.adoptSession(sess, opened)
	return p, nil
}

// archiveSelectedSession hides the highlighted session from the default
// roster.
func (p *Program) archiveSelectedSession() (tea.Model, tea.Cmd) {
	item, ok := p.state.SessionModal.Selected()
	if !ok {
		return p, nil
	}
	if p.sessionActions.Archive == nil {
		p.state.Apply(SessionManagerNoticed{Text: "archive is unavailable"})
		return p, nil
	}
	if err := p.sessionActions.Archive(item.ID); err != nil {
		p.state.Apply(SessionManagerNoticed{Text: "archive: " + err.Error()})
		return p, nil
	}
	p.reloadSessionManager(fmt.Sprintf("archived session #%d", item.ID))
	return p, nil
}

// confirmSessionAction runs the destructive action the confirmation named.
func (p *Program) confirmSessionAction() (tea.Model, tea.Cmd) {
	view := p.state.SessionModal
	id := view.ConfirmID
	if id == 0 || p.sessionActions.Delete == nil {
		p.state.Apply(SessionManagerModeCancelled{})
		if p.sessionActions.Delete == nil {
			p.state.Apply(SessionManagerNoticed{Text: "delete is unavailable"})
		}
		return p, nil
	}
	if err := p.sessionActions.Delete(id); err != nil {
		p.state.Apply(SessionManagerNoticed{Text: "delete: " + err.Error()})
		return p, nil
	}
	p.reloadSessionManager(fmt.Sprintf("deleted session #%d", id))
	return p, nil
}

// reloadSessionManager re-lists the store after a write action so the table
// never shows a row that no longer exists. A listing failure surfaces as a
// notice rather than silently keeping a stale roster.
func (p *Program) reloadSessionManager(notice string) {
	if p.listSessions == nil {
		p.state.Apply(SessionManagerNoticed{Text: "session browsing is unavailable"})
		return
	}
	items, err := p.listSessions()
	if err != nil {
		p.state.Apply(SessionManagerNoticed{Text: "sessions: " + err.Error()})
		return
	}
	p.state.Apply(SessionManagerReloaded{Items: p.markActiveSession(items), Notice: notice})
}

// markActiveSession stamps the loaded session on the roster so the STATUS
// column distinguishes the active row from the highlighted one.
func (p *Program) markActiveSession(items []SessionItem) []SessionItem {
	active := int64(0)
	if p.session != nil {
		active = p.session.ID
	}
	for i := range items {
		items[i].Active = items[i].ID == active
	}
	return items
}
