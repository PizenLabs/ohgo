package tui

// The /connect Provider Manager: a centered modal that turns the flat provider
// picker into a navigable roster with a configuration pane.
//
// It is a working modal in the same family as the Session Manager: while open
// it owns the keyboard and the middle of the screen, the composer hides behind
// it, moves its own cursor, and starts the guided setup for the highlighted
// provider. Its frame reuses the shared modal frame — a border-embedded title,
// interior-padded rows, a rule above a structured footer bar — so the surface
// reads as one system with the model control plane, the status card, the usage
// inspector, and the session roster. The roster is a snapshot taken when it
// opens, so nothing already printed can go stale underneath it.
//
// The table names what a provider is, how it is reached, and whether it is
// ready; the pane below turns the highlighted row into the one instruction the
// user needs. An unmeasured fact stays absent rather than rendering as a
// fabricated "configured".

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Connect manager geometry. The dialog is responsive: it grows with the
// terminal up to a comfortable reading width and never touches the edges while
// margins fit.
const (
	// connectCardHiWidth caps the dialog so ultrawide terminals keep context
	// on both sides.
	connectCardHiWidth = 92
	// connectCardMarginX is the total horizontal margin (both sides).
	connectCardMarginX = 8
	// connectModalTitle is the border-embedded title, matching the plane's
	// "MODEL CONTROL · …", the status card's "SYSTEM STATUS", and the session
	// manager's "SESSION MANAGER" treatment.
	connectModalTitle = "PROVIDER MANAGER"
)

// Connect table column widths, in cells. The provider column absorbs whatever
// remains; the optional TYPE and DETAILS columns drop at narrow interiors so
// the table never collapses into overlapping text.
const (
	connectColCursor   = 2
	connectColStatus   = 6
	connectColDriver   = 18
	connectColDetails  = 22
	connectColGap      = 2
	connectProviderMin = 10
	// connectLabelWidth is the details pane's label column. It is wider than
	// the status card's grid column so "Credential" keeps every value on one
	// axis.
	connectLabelWidth = 12
)

// ConnectView is the open /connect Provider Manager.
//
// It is a plain value embedded in the program state so the render stays pure
// and a test can build one directly. Providers is a snapshot: the manager never
// reads a catalog itself; the program re-lists and pushes the result back.
type ConnectView struct {
	// Providers are the credential rows, in the order the producer gave them.
	Providers []ProviderRow
	// Cursor selects within Providers.
	Cursor int
	// Offset is the first visible table row.
	Offset int
	// VisibleRows bounds the rendered roster window. Zero or negative renders
	// everything (unknown terminal size).
	VisibleRows int
}

// Selected returns the highlighted provider, if any.
func (m *ConnectView) Selected() (ProviderRow, bool) {
	if m == nil || m.Cursor < 0 || m.Cursor >= len(m.Providers) {
		return ProviderRow{}, false
	}
	return m.Providers[m.Cursor], true
}

// Move shifts the highlight, wrapping at both ends, keeping it visible.
func (m *ConnectView) Move(delta int) {
	if len(m.Providers) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	m.Cursor = (m.Cursor + delta) % len(m.Providers)
	if m.Cursor < 0 {
		m.Cursor += len(m.Providers)
	}
	m.EnsureVisible()
}

// Page shifts the highlight by approximately one viewport without wrapping:
// paging past an end parks at that end.
func (m *ConnectView) Page(delta int) {
	if len(m.Providers) == 0 {
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

// Top parks the highlight on the first provider; Bottom on the last.
func (m *ConnectView) Top() {
	if len(m.Providers) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	m.Cursor = 0
	m.EnsureVisible()
}

// Bottom parks the highlight on the last provider.
func (m *ConnectView) Bottom() {
	if len(m.Providers) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	m.Cursor = len(m.Providers) - 1
	m.EnsureVisible()
}

// SetVisibleRows resizes the roster window, e.g. on WindowSizeMsg, keeping the
// highlight on screen.
func (m *ConnectView) SetVisibleRows(rows int) {
	if rows < 0 {
		rows = 0
	}
	m.VisibleRows = rows
	m.EnsureVisible()
}

// SetItems replaces the roster, preserving the highlight by id when the
// selected provider survives and clamping otherwise.
func (m *ConnectView) SetItems(items []ProviderRow) {
	selected := ""
	if cur, ok := m.Selected(); ok {
		selected = cur.ID
	}
	m.Providers = append([]ProviderRow(nil), items...)
	m.Cursor = 0
	if selected != "" {
		for i, row := range items {
			if row.ID == selected {
				m.Cursor = i
				break
			}
		}
	}
	m.clamp()
	m.EnsureVisible()
}

// clamp bounds the highlight to the roster.
func (m *ConnectView) clamp() {
	if len(m.Providers) == 0 {
		m.Cursor, m.Offset = 0, 0
		return
	}
	if m.Cursor >= len(m.Providers) {
		m.Cursor = len(m.Providers) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

// EnsureVisible adjusts the scroll offset so the highlight is on screen.
func (m *ConnectView) EnsureVisible() {
	n := len(m.Providers)
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
func (m *ConnectView) Window() []ProviderRow {
	if m.VisibleRows <= 0 || len(m.Providers) <= m.VisibleRows {
		return m.Providers
	}
	m.EnsureVisible()
	end := m.Offset + m.VisibleRows
	if end > len(m.Providers) {
		end = len(m.Providers)
	}
	return m.Providers[m.Offset:end]
}

// connectCardWidth resolves the total dialog width for a terminal width,
// including the border. Unknown sizes resolve to the cap.
func connectCardWidth(termWidth int) int {
	if termWidth <= 0 {
		return connectCardHiWidth
	}
	width := termWidth - connectCardMarginX
	if width > connectCardHiWidth {
		width = connectCardHiWidth
	}
	if width < statusMinContent+2 {
		width = statusMinContent + 2
	}
	if width > termWidth {
		width = termWidth
	}
	return width
}

// connectVisibleRows resolves the roster window from the terminal size. It
// subtracts the frame and the fixed content overhead so the highlight is never
// scrolled off below the configuration pane.
func connectVisibleRows(state State) int {
	if state.Width <= 0 || state.Height <= 0 {
		return 0
	}
	layout := ComputeLayout(state)
	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	// Frame (top/bottom border, footer rule, footer bar), the PROVIDERS
	// header, the column header, an overflow/rule row, the CONFIGURATION
	// header, and up to six detail fields.
	const overhead = 4 + 1 + 1 + 1 + 1 + 6
	rows := avail - overhead
	if rows < 1 {
		rows = 1
	}
	return rows
}

// connectTableColumns resolves the visible table columns for an interior
// width. The provider column absorbs the remainder; the optional columns are
// added from the right while the provider keeps its minimum.
type connectTableColumns struct {
	provider int
	driver   bool
	details  bool
}

// connectColumnsFor resolves the column layout for an interior width.
//
// The cursor, a gap, and the status column are fixed. Each optional column and
// the provider column cost their own leading gap plus their width, so the sum
// of the present columns always fits the interior exactly. Optional columns are
// added from the right (details, then driver) while the provider keeps its
// minimum, so a narrow modal shows the provider name and status alone rather
// than overlapping text.
func connectColumnsFor(interior int) connectTableColumns {
	var cols connectTableColumns
	remaining := interior - connectColCursor - connectColStatus - connectColGap
	titleCost := connectColGap + connectProviderMin
	if remaining < titleCost {
		cols.provider = maxInt(remaining-connectColGap, 1)
		return cols
	}
	add := func(present *bool, width int) {
		cost := connectColGap + width
		if remaining-cost >= titleCost {
			*present = true
			remaining -= cost
		}
	}
	add(&cols.details, connectColDetails)
	add(&cols.driver, connectColDriver)
	cols.provider = remaining - connectColGap
	return cols
}

// connectCell truncates and right-pads a (possibly styled) cell to width
// cells. Measurement strips ANSI, so a styled cell still aligns.
func connectCell(value string, width int) string {
	return padRight(TruncateToWidth(value, width), width)
}

// connectStatusCell renders the STATUS column: a check for a resolved
// credential, a cross otherwise. The glyph carries the state so a monochrome
// terminal loses nothing.
func connectStatusCell(row ProviderRow, pal *Palette) (string, lipgloss.Style) {
	if row.Configured {
		return "[✓]", pal.ToolDone
	}
	return "[×]", pal.ToolError
}

// connectProviderName resolves the provider column's display text, falling
// back to the id when the producer supplied no display name.
func connectProviderName(row ProviderRow) string {
	name := sanitizeInline(row.Name)
	if strings.TrimSpace(name) == "" {
		name = sanitizeInline(row.ID)
	}
	if strings.TrimSpace(name) == "" {
		name = "(unnamed)"
	}
	return name
}

// connectDriverText resolves the type/driver column: the wire protocol the
// producer resolved, or unknown rather than a guessed transport.
func connectDriverText(row ProviderRow) string {
	driver := sanitizeInline(row.Driver)
	if strings.TrimSpace(driver) == "" {
		return "unknown"
	}
	return driver
}

// connectDetailText resolves the details column: the credential source or
// missing hint, or unknown rather than a fabricated source.
func connectDetailText(row ProviderRow) string {
	detail := sanitizeInline(row.KeyDetail)
	if strings.TrimSpace(detail) == "" {
		return "unknown"
	}
	return detail
}

// connectTableHeader renders the muted column header row.
func connectTableHeader(cols connectTableColumns, pal *Palette, interior int) string {
	cell := func(text string, width int) string {
		return connectCell(pal.Muted.Render(TruncateToWidth(text, width)), width)
	}
	var b strings.Builder
	b.WriteString(cell("", connectColCursor))
	b.WriteString(strings.Repeat(" ", connectColGap))
	b.WriteString(cell("STATUS", connectColStatus))
	b.WriteString(strings.Repeat(" ", connectColGap))
	b.WriteString(cell("PROVIDER", cols.provider))
	if cols.driver {
		b.WriteString(strings.Repeat(" ", connectColGap))
		b.WriteString(cell("TYPE", connectColDriver))
	}
	if cols.details {
		b.WriteString(strings.Repeat(" ", connectColGap))
		b.WriteString(cell("DETAILS", connectColDetails))
	}
	return TruncateToWidth(b.String(), interior)
}

// renderConnectTableRow renders one roster row. The highlighted row carries a
// green cursor and a faint background so the selection is unmistakable; the
// credential state carries its own coloured glyph independently.
func renderConnectTableRow(cols connectTableColumns, view ConnectView, row ProviderRow, index int, pal *Palette, interior int) string {
	selected := index == view.Cursor
	marker := "  "
	markerStyle := pal.Muted
	if selected {
		marker = "> "
		markerStyle = pal.Selected
	}
	status, statusStyle := connectStatusCell(row, pal)
	name := connectProviderName(row)
	driver := connectDriverText(row)
	details := connectDetailText(row)

	if selected {
		// The whole row shares one background, so the cells are built plain
		// and the line is styled once.
		plain := connectRowPlain(cols, marker, status, name, driver, details)
		return pal.UserBG.Render(padRight(plain, interior))
	}

	var b strings.Builder
	b.WriteString(connectCell(markerStyle.Render(marker), connectColCursor))
	b.WriteString(strings.Repeat(" ", connectColGap))
	b.WriteString(connectCell(statusStyle.Render(status), connectColStatus))
	b.WriteString(strings.Repeat(" ", connectColGap))
	b.WriteString(connectCell(pal.Choice.Render(TruncateToWidth(name, cols.provider)), cols.provider))
	if cols.driver {
		b.WriteString(strings.Repeat(" ", connectColGap))
		b.WriteString(connectCell(pal.Muted.Render(TruncateToWidth(driver, connectColDriver)), connectColDriver))
	}
	if cols.details {
		b.WriteString(strings.Repeat(" ", connectColGap))
		b.WriteString(connectCell(pal.Muted.Render(TruncateToWidth(details, connectColDetails)), connectColDetails))
	}
	return TruncateToWidth(b.String(), interior)
}

// connectRowPlain builds the unstyled row text at exact column widths. It is
// the backing text for the selected row, whose background cannot survive
// nested style resets.
func connectRowPlain(cols connectTableColumns, marker, status, name, driver, details string) string {
	var b strings.Builder
	b.WriteString(connectCell(marker, connectColCursor))
	b.WriteString(strings.Repeat(" ", connectColGap))
	b.WriteString(connectCell(status, connectColStatus))
	b.WriteString(strings.Repeat(" ", connectColGap))
	b.WriteString(connectCell(name, cols.provider))
	if cols.driver {
		b.WriteString(strings.Repeat(" ", connectColGap))
		b.WriteString(connectCell(driver, connectColDriver))
	}
	if cols.details {
		b.WriteString(strings.Repeat(" ", connectColGap))
		b.WriteString(connectCell(details, connectColDetails))
	}
	return b.String()
}

// connectProviderPair renders a provider as "Name (id)", collapsing to one
// when the display name and key are the same.
func connectProviderPair(row ProviderRow) string {
	name := connectProviderName(row)
	id := sanitizeInline(strings.TrimSpace(row.ID))
	if id != "" && id != name {
		return name + " (" + id + ")"
	}
	return name
}

// connectStatusLine names the credential state in words so the status pane
// never depends on colour alone.
func connectStatusLine(row ProviderRow) string {
	if row.Configured {
		return "[✓] configured"
	}
	return "[×] missing credential"
}

// connectModelsLine reports the provider's catalog size, or unknown rather than
// a fabricated zero.
func connectModelsLine(row ProviderRow) string {
	switch {
	case row.Count <= 0:
		return "—"
	case row.Count == 1:
		return "1 model"
	default:
		return fmt.Sprintf("%d models", row.Count)
	}
}

// connectSetupHint turns a provider row into the one configuration instruction
// the user needs: the missing variable or the resolved source. It reads only
// the producer-owned KeyDetail and never invents a credential path.
func connectSetupHint(row ProviderRow) string {
	id := sanitizeInline(strings.TrimSpace(row.ID))
	detail := sanitizeInline(strings.TrimSpace(row.KeyDetail))
	if row.Configured {
		if detail == "" {
			return "ready — no credential required"
		}
		return "ready — credential: " + detail
	}
	need := strings.TrimSpace(strings.TrimPrefix(detail, "missing: "))
	need = strings.TrimSpace(strings.TrimPrefix(need, "set "))
	if need == "" || id == "" {
		return "missing credential — set one for " + statusValueOr(id, "this provider")
	}
	return "Missing " + need + " or config providers." + id + ".api_key"
}

// connectDetails builds the bottom configuration block for the highlighted
// provider: what it is, how it is reached, and how to make it ready.
func connectDetails(view ConnectView, pal *Palette, interior int) []string {
	rows := []string{statusSectionHeader("CONFIGURATION", interior)}
	row, ok := view.Selected()
	if !ok {
		return append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render("nothing selected"))
	}
	field := func(label, value string) []string {
		return modalField(label, value, pal, interior, 0, connectLabelWidth)
	}
	rows = append(rows, field("Provider", connectProviderPair(row))...)
	rows = append(rows, field("Driver", connectDriverText(row))...)
	rows = append(rows, field("Status", connectStatusLine(row))...)
	rows = append(rows, field("Models", connectModelsLine(row))...)
	rows = append(rows, field("Credential", connectDetailText(row))...)
	rows = append(rows, field("Setup", connectSetupHint(row))...)
	return rows
}

// connectModalContent builds the dialog body: the provider table, a full-width
// rule, then the configuration block.
func connectModalContent(view ConnectView, pal *Palette, interior int) []string {
	cols := connectColumnsFor(interior)
	rows := []string{statusSectionHeader("PROVIDERS", interior)}
	rows = append(rows, connectTableHeader(cols, pal, interior))
	window := view.Window()
	if len(window) == 0 {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render("no providers known"))
	} else {
		for i, row := range window {
			rows = append(rows, renderConnectTableRow(cols, view, row, view.Offset+i, pal, interior))
		}
		if hidden := len(view.Providers) - view.Offset - len(window); hidden > 0 {
			rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render(fmt.Sprintf("… %d more", hidden)))
		}
	}
	rows = append(rows, statusRule(pal, interior))
	rows = append(rows, connectDetails(view, pal, interior)...)
	return rows
}

// connectFooterBar is the structured action line: the connect key first, then
// the dismiss keys, matching the modal family's footer treatment.
func connectFooterBar(pal *Palette, interior int) string {
	line := strings.Repeat(" ", statusFieldIndent) +
		pal.Bold.Render("↵") + pal.Muted.Render(" connect  ·  ") +
		pal.Bold.Render("Esc") + pal.Muted.Render(" / ") + pal.Bold.Render("q") +
		pal.Muted.Render("  close modal")
	return TruncateToWidth(line, interior)
}

// RenderConnectModal composes the /connect Provider Manager as an ANSI string.
//
// width is the total rendered width, border included; height, when positive,
// bounds the total rendered height so the dialog never overflows a short
// viewport. The caller centers the returned block in the terminal.
func RenderConnectModal(view ConnectView, pal *Palette, width, height int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	if width <= 0 {
		width = connectCardHiWidth
	}
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}
	content := connectModalContent(view, pal, interior)
	// The title border, the footer rule, the footer bar, and the bottom
	// border claim four rows; everything else is body.
	if height > 0 {
		content = fitStatusRows(content, height-4)
	}
	footer := connectFooterBar(pal, interior)
	return renderModalFrameWithFooter(connectModalTitle, content, footer, pal, interior)
}

// renderConnectArea draws the Provider Manager centered over the workbench. The
// header and status chrome keep rendering around it and the conversation is
// untouched: dismissing returns to the previous surface with nothing lost.
func renderConnectArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	view := state.ConnectModal
	if view == nil {
		return
	}
	renderCardAreaWidth(sb, state, layout, connectCardWidth, func(width, height int) string {
		return RenderConnectModal(*view, pal, width, height)
	})
}

// openConnectManager opens the Provider Manager over the credential roster.
// The cursor parks on the provider in force so the manager opens oriented on
// where the user already is.
func (p *Program) openConnectManager() {
	if p.connect.Providers == nil {
		p.state.Apply(Notice{Text: "guided setup is unavailable"})
		return
	}
	providers := p.connect.Providers()
	if len(providers) == 0 {
		p.state.Apply(Notice{Text: "no providers known"})
		return
	}
	p.flow = nil
	view := ConnectView{Providers: providers}
	active := currentProvider(p.state.Status.Model)
	for i, row := range providers {
		if row.ID == active {
			view.Cursor = i
			break
		}
	}
	// Size the roster window against the modal layout (composer and hints
	// hidden) rather than the surface being replaced, so the highlight stays
	// on screen without a resize.
	probe := p.state
	probe.ConnectModal = &view
	view.VisibleRows = connectVisibleRows(probe)
	view.EnsureVisible()
	p.state.Apply(ConnectModalOpened{View: view})
}

// handleConnectKey routes keys to the open Provider Manager.
//
// Precedence is fixed: esc/q close; enter starts the guided setup for the
// highlighted provider; arrows/j/k move; pgup/pgdn page; home/end jump. Nothing
// else reaches the composer hidden underneath.
func (p *Program) handleConnectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		p.state.Apply(ConnectModalClosed{})
	case "up", "k", "ctrl+p", "shift+tab":
		p.state.Apply(ConnectModalMoved{Delta: -1})
	case "down", "j", "ctrl+n", "tab":
		p.state.Apply(ConnectModalMoved{Delta: 1})
	case "pgup":
		p.state.Apply(ConnectModalPaged{Delta: -1})
	case "pgdown":
		p.state.Apply(ConnectModalPaged{Delta: 1})
	case "home", "g":
		p.state.Apply(ConnectModalTop{})
	case "end", "G":
		p.state.Apply(ConnectModalBottom{})
	case "enter":
		return p.connectSelectedProvider()
	}
	return p, nil
}

// connectSelectedProvider closes the manager and opens the API-key modal for
// the highlighted provider.
func (p *Program) connectSelectedProvider() (tea.Model, tea.Cmd) {
	view := p.state.ConnectModal
	if view == nil {
		return p, nil
	}
	row, ok := view.Selected()
	if !ok {
		return p, nil
	}
	p.state.Apply(ConnectModalClosed{})
	p.openConnectKey(row)
	return p, nil
}
