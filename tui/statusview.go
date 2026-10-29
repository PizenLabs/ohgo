package tui

// The /status card: a centered, read-only modal that reports the session's
// local facts in labelled sections.
//
// It is a modal in the same family as the model control plane and the
// authority selector: while open it owns the keyboard and the middle of the
// screen, the composer hides behind it, and any key dismisses it without
// changing anything. The frame follows the plane's design language — a
// border-embedded title, section headers, full-width rules between blocks, and
// a structured footer bar — so the two surfaces read as one system. The facts
// are a snapshot taken when the card opens, so nothing already printed can go
// stale underneath it; a fact the producer did not measure stays absent rather
// than rendering as a fabricated zero.

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Card geometry. The dialog is responsive: it grows with the terminal up to a
// comfortable reading width and never touches the terminal edges while margins
// fit.
const (
	// statusCardHiWidth caps the card so ultrawide terminals keep context on
	// either side.
	statusCardHiWidth = 84
	// statusCardMarginX is the total horizontal margin (both sides).
	statusCardMarginX = 8
	// statusMinContent is the narrowest readable content column, inside the
	// border. Below it the card grows to this floor rather than rendering a
	// degenerate box.
	statusMinContent = 24
	// statusFieldIndent is the left axis every field row starts on.
	statusFieldIndent = 2
	// statusModalTitle is the border-embedded title, matching the plane's
	// "MODEL CONTROL · …" treatment.
	statusModalTitle = "SYSTEM STATUS"
)

// StatusEngine is the optional code-index status reported by the SYMBOL ENGINE
// section. A nil Engine on the view hides the section: the card never invents
// an engine the runtime did not report.
type StatusEngine struct {
	// Indexed reports the symbol index is built.
	Indexed bool
	// Symbols is the indexed declaration count.
	Symbols int
	// ASTReady reports the AST layer is available.
	ASTReady bool
}

// StatusView is the snapshot behind the /status card.
//
// It is a plain value so the render stays pure and a test can build one
// directly. Every field is producer-owned: the card reports what it is given
// and hides what is empty rather than substituting a guess.
type StatusView struct {
	// Path is the working directory the session runs in.
	Path string
	// GitBranch is the short branch name or short SHA when detached.
	GitBranch string
	// GitStatus is the cleanliness summary when the counts are unknown.
	GitStatus string
	// GitStaged, GitModified, and GitUntracked are the status counts, valid
	// only when GitKnown is set. Unknown counts render the summary line
	// alone rather than a fabricated clean tree.
	GitStaged    int
	GitModified  int
	GitUntracked int
	GitKnown     bool
	// Engine is the optional symbol-index status. Nil hides the section.
	Engine *StatusEngine
	// Slot names the session, e.g. "#12".
	Slot string
	// Prompt is the latest user prompt, summarized by the card.
	Prompt string
	// TokensIn and TokensOut are the cumulative session token counts.
	TokensIn  int
	TokensOut int
	// Turns is the number of messages in the conversation.
	Turns int
	// ContextUsed and ContextWindow size the budget line. A non-positive
	// window means unknown and renders as such rather than "0%".
	ContextUsed   int
	ContextWindow int
	// Model is the active "provider/id".
	Model string
	// Workspace is the authority label, e.g. "BUILD".
	Workspace string
	// Policy is the plain-language mode, e.g. "Read-Only".
	Policy string
}

// statusCardWidth resolves the total card width for a terminal width,
// including the border. Unknown sizes resolve to the cap.
func statusCardWidth(termWidth int) int {
	if termWidth <= 0 {
		return statusCardHiWidth
	}
	width := termWidth - statusCardMarginX
	if width > statusCardHiWidth {
		width = statusCardHiWidth
	}
	if width < statusMinContent+2 {
		width = statusMinContent + 2
	}
	if width > termWidth {
		width = termWidth
	}
	return width
}

// detailFieldLabelWidth is the label column shared by the field rows and the
// model detail grid.
const detailFieldLabelWidth = 8

// modalValueCol is the cell a value column opens at for a padded label of the
// given text width: the field indent, the label itself, and the " · "
// separator. It lets a wider inspector (the usage modal) reuse the field
// wrapping while keeping every label on one axis.
func modalValueCol(labelWidth int) int {
	return statusFieldIndent + labelWidth + 2
}

// RenderStatusModal composes the /status card as an ANSI string.
//
// width is the total rendered width, border included; height, when positive,
// bounds the total rendered height so the card never overflows a short
// viewport. The caller centers the returned block in the terminal. The frame
// mirrors renderPlaneModal: a border-embedded title, body rows padded to the
// interior, a rule above the footer bar, and the closing border.
func RenderStatusModal(view StatusView, pal *Palette, width, height int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	if width <= 0 {
		width = statusCardHiWidth
	}
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}
	content := statusModalContent(view, pal, interior)
	// The title border, the footer rule, the footer bar, and the bottom
	// border claim four rows; everything else is body.
	if height > 0 {
		content = fitStatusRows(content, height-4)
	}
	return renderModalFrame(statusModalTitle, content, pal, interior)
}

// renderModalFrame wraps a modal body in the shared card frame: a
// border-embedded title, interior-padded body rows, an inset rule above the
// footer bar, the footer key line, and the closing border. The /status and
// /usage cards share it so their frames read as one family.
func renderModalFrame(title string, content []string, pal *Palette, interior int) string {
	return renderModalFrameWithFooter(title, content, statusFooterBar(pal, interior), pal, interior)
}

// renderModalFrameWithFooter is renderModalFrame with an explicit footer bar,
// so a working modal (the Session Manager) can name its own action keys while
// keeping the identical frame. The footer is truncated to the interior before
// padding, so an over-long line can never break the border alignment.
func renderModalFrameWithFooter(title string, content []string, footer string, pal *Palette, interior int) string {
	out := make([]string, 0, len(content)+4)
	out = append(out, modalTitleBorder(interior, title))
	for _, line := range content {
		if gap := interior - CellWidth(line); gap > 0 {
			line += strings.Repeat(" ", gap)
		}
		out = append(out, "│"+TruncateToWidth(line, interior)+"│")
	}
	out = append(out, "│"+statusFooterRule(pal, interior)+"│")
	out = append(out, "│"+padRight(TruncateToWidth(footer, interior), interior)+"│")
	out = append(out, "└"+strings.Repeat("─", interior)+"┘")
	return strings.Join(out, "\n")
}

// statusModalContent builds the card body: each labelled block heads with a
// cyan section header and is separated from the next by a full-width rule.
func statusModalContent(view StatusView, pal *Palette, interior int) []string {
	field := func(label, value string) []string {
		return statusField(label, value, pal, interior)
	}
	var blocks [][]string
	blocks = append(blocks, statusBlock("WORKSPACE STATUS", pal, interior,
		field("Path", statusValueOr(view.Path, "unknown")),
		field("Git", statusGitLine(view)),
	))
	if view.Engine != nil {
		blocks = append(blocks, statusBlock("SYMBOL ENGINE", pal, interior,
			field("Engine", statusEngineLine(*view.Engine)),
		))
	}
	blocks = append(blocks,
		statusBlock("SESSION CONTEXT", pal, interior,
			field("Slot", statusValueOr(view.Slot, "—")),
			statusFieldCapped("Prompt", statusValueOr(view.Prompt, "(no prompt yet)"), pal, interior, 2),
			field("Tokens", statusTokensLine(view)),
			field("Context", statusContextLine(view)),
		),
		statusBlock("ENGINE & AUTHORITY", pal, interior,
			field("Model", statusValueOr(view.Model, "no model")),
			field("Mode", statusModeLine(view)),
		),
	)
	var rows []string
	for i, block := range blocks {
		if i > 0 {
			rows = append(rows, statusRule(pal, interior))
		}
		rows = append(rows, block...)
	}
	return rows
}

// statusBlock renders one section: its header followed by its field rows.
func statusBlock(header string, pal *Palette, interior int, fields ...[]string) []string {
	rows := []string{statusSectionHeader(header, interior)}
	for _, field := range fields {
		rows = append(rows, field...)
	}
	return rows
}

// statusSectionHeader renders one category label in the cyan modal accent,
// flush with the interior's left edge like the plane's pane headings.
func statusSectionHeader(title string, interior int) string {
	return WorkspaceBadge("37", TruncateToWidth(title, interior))
}

// statusRule is the full-width rule that separates two sections.
func statusRule(pal *Palette, interior int) string {
	return pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(interior, 1)), interior))
}

// statusFooterRule is the inset rule above the footer bar, matching the
// plane's divider that sets the key line apart from the body.
func statusFooterRule(pal *Palette, interior int) string {
	return pal.Muted.Render(TruncateToWidth(" "+strings.Repeat("─", maxInt(interior-2, 1))+" ", interior))
}

// statusFooterBar is the structured key line: the dismiss keys in bold, their
// meaning muted, padded to span the interior width.
func statusFooterBar(pal *Palette, interior int) string {
	line := strings.Repeat(" ", statusFieldIndent) +
		pal.Bold.Render("Esc") + pal.Muted.Render(" / ") + pal.Bold.Render("q") +
		pal.Muted.Render("  close modal")
	return TruncateToWidth(line, interior)
}

// statusField renders one label/value row, wrapping the value onto the value
// column. The label column is fixed, so values align across every section.
func statusField(label, value string, pal *Palette, interior int) []string {
	return statusFieldCapped(label, value, pal, interior, 0)
}

// statusFieldCapped is statusField with an optional row cap. maxRows of zero
// renders every wrapped row; a positive cap keeps the head and appends an
// ellipsis marker so a truncated value reads as elided rather than ended.
func statusFieldCapped(label, value string, pal *Palette, interior, maxRows int) []string {
	return modalField(label, value, pal, interior, maxRows, detailFieldLabelWidth)
}

// modalField renders one label/value row for a modal card, wrapping the value
// onto the value column. labelWidth fixes the label column so values align
// across every section; the /status card uses the detail-grid width and the
// usage inspector a wider column for its longer metric names.
func modalField(label, value string, pal *Palette, interior, maxRows, labelWidth int) []string {
	col := modalValueCol(labelWidth)
	valueW := interior - col
	if valueW < 4 {
		valueW = 4
	}
	parts := WrapToWidth(sanitizeInline(value), valueW)
	if len(parts) == 0 {
		parts = []string{""}
	}
	if maxRows > 0 && len(parts) > maxRows {
		parts = append(append([]string(nil), parts[:maxRows]...), "…")
	}
	out := make([]string, 0, len(parts))
	for i, part := range parts {
		prefix := strings.Repeat(" ", col)
		if i == 0 {
			prefix = strings.Repeat(" ", statusFieldIndent) + pal.Muted.Render(padModalLabel(label, labelWidth))
		}
		out = append(out, prefix+pal.Choice.Render(part))
	}
	return out
}

// fitStatusRows bounds the card body to max rows: content yields from the
// bottom so the earliest and most orientation-critical sections survive.
func fitStatusRows(rows []string, max int) []string {
	if max < 0 {
		max = 0
	}
	if len(rows) <= max {
		return rows
	}
	return rows[:max]
}

// statusValueOr returns value, or fallback when value is blank.
func statusValueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// statusGitLine composes the repository row: the branch plus either the
// measured status counts or the preformatted summary. Unknown counts render
// the summary rather than inventing zeros.
func statusGitLine(view StatusView) string {
	branch := strings.TrimSpace(view.GitBranch)
	if view.GitKnown {
		if branch == "" {
			branch = "detached"
		}
		if view.GitStaged == 0 && view.GitModified == 0 && view.GitUntracked == 0 {
			return branch + " · clean"
		}
		parts := []string{branch, "dirty"}
		if view.GitStaged > 0 {
			parts = append(parts, fmt.Sprintf("%d staged", view.GitStaged))
		}
		parts = append(parts,
			fmt.Sprintf("%d modified", view.GitModified),
			fmt.Sprintf("%d untracked", view.GitUntracked))
		return strings.Join(parts, " · ")
	}
	status := strings.TrimSpace(view.GitStatus)
	switch {
	case branch != "" && status != "":
		return branch + " · " + status
	case branch != "":
		return branch
	case status != "":
		return status
	default:
		return "unknown"
	}
}

// statusEngineLine composes the symbol-engine row: index state, declaration
// count, and AST readiness. Every marker carries a glyph so the state never
// depends on colour alone.
func statusEngineLine(engine StatusEngine) string {
	indexed := "○ not indexed"
	if engine.Indexed {
		indexed = "✓ indexed"
	}
	ast := "○ pending"
	if engine.ASTReady {
		ast = "✓ ready"
	}
	return fmt.Sprintf("%s · %s symbols · AST %s", indexed, formatCount(engine.Symbols), ast)
}

// statusTokensLine composes the token row: measured input and output, their
// total, and the turn count.
func statusTokensLine(view StatusView) string {
	total := view.TokensIn + view.TokensOut
	return fmt.Sprintf("↑%s · ↓%s (%s total) · %s",
		formatCount(view.TokensIn), formatCount(view.TokensOut), formatCount(total), statusTurns(view.Turns))
}

// statusContextLine composes the context budget row. An unknown window renders
// as such rather than a fabricated percentage.
func statusContextLine(view StatusView) string {
	if view.ContextWindow <= 0 {
		return "unknown window"
	}
	pct := view.ContextUsed * 100 / view.ContextWindow
	if pct < 0 {
		pct = 0
	}
	return fmt.Sprintf("%s / %s (%d%%)", formatCount(view.ContextUsed), formatCount(view.ContextWindow), pct)
}

// statusTurns pluralizes the turn count.
func statusTurns(n int) string {
	if n == 1 {
		return "1 turn"
	}
	return fmt.Sprintf("%d turns", n)
}

// statusModeLine composes the authority row: the mode label plus its
// plain-language policy. Either half survives on its own.
func statusModeLine(view StatusView) string {
	mode := strings.ToUpper(strings.TrimSpace(view.Workspace))
	policy := strings.TrimSpace(view.Policy)
	switch {
	case mode != "" && policy != "":
		return mode + " · " + policy
	case mode != "":
		return mode
	case policy != "":
		return policy
	default:
		return "unknown"
	}
}

// statusPolicy maps an authority label to its plain-language mode. An unknown
// or empty label reports nothing rather than guessing an authority.
func statusPolicy(workspace string) string {
	switch strings.ToUpper(strings.TrimSpace(workspace)) {
	case "ASK":
		return "Read-Only"
	case "BUILD":
		return "Interactive"
	case "FUZZ":
		return "Auto"
	default:
		return ""
	}
}

// renderStatusArea draws the /status card centered over the workbench. The
// header and status chrome keep rendering around it and the underlying
// conversation is untouched: dismissing returns to the previous surface with
// nothing lost.
func renderStatusArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	view := state.StatusModal
	if view == nil {
		return
	}
	renderCardArea(sb, state, layout, func(width, height int) string {
		return RenderStatusModal(*view, pal, width, height)
	})
}

// renderCardArea centers a modal card over the workbench. The header and
// status chrome keep rendering around it and the conversation underneath is
// untouched: dismissing returns to the previous surface with nothing lost.
// build receives the card width and the available height so each card keeps
// its own bounded frame.
func renderCardArea(sb *strings.Builder, state State, layout Layout, build func(width, height int) string) {
	renderCardAreaWidth(sb, state, layout, statusCardWidth, build)
}

// renderCardAreaWidth is renderCardArea with an explicit width resolver, so a
// card with a wider preferred frame (the Session Manager's roster) can keep
// its own responsive clamp while sharing the centering contract.
func renderCardAreaWidth(sb *strings.Builder, state State, layout Layout, widthFn func(int) int, build func(width, height int) string) {
	if state.Width <= 0 || state.Height <= 0 {
		card := build(widthFn(state.Width), 0)
		sb.WriteString(card)
		if !strings.HasSuffix(card, "\n") {
			sb.WriteString("\n")
		}
		return
	}
	avail := layout.ConversationHeight
	// The update lifecycle cards own the surface outright; a tool approval
	// underneath stays blocked and does not reserve rows in the card.
	if state.Pending != nil && state.UpdateRestart == nil && state.PostUpdate == nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	width := state.Width
	card := build(widthFn(width), avail)
	placed := lipgloss.Place(width, avail, lipgloss.Center, lipgloss.Center, card)
	lines := strings.Split(placed, "\n")
	if len(lines) > avail {
		lines = lines[:avail]
	}
	for len(lines) < avail {
		lines = append(lines, strings.Repeat(" ", width))
	}
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
}

// openStatus opens the /status card with a snapshot of the current facts.
//
// It is read-only and safe mid-turn: the card reports the in-flight state
// instead of blocking on a turn boundary the way an authority move must.
func (p *Program) openStatus() {
	p.state.Apply(StatusModalOpened{View: p.statusView()})
}

// statusView captures the facts the card reports. History is read under the
// mutex because the turn goroutine appends to it while the UI renders.
func (p *Program) statusView() StatusView {
	p.mu.Lock()
	turns := len(p.history)
	prompt, _ := agent.LastUserText(p.history)
	p.mu.Unlock()

	slot := p.state.Status.Session
	if slot == "" && p.session != nil {
		slot = fmt.Sprintf("#%d", p.session.ID)
	}
	var engine *StatusEngine
	if p.symbolStatus != nil {
		status := p.symbolStatus()
		engine = &status
	}
	return StatusView{
		Path:          p.state.Status.Cwd,
		GitBranch:     p.state.Status.GitBranch,
		GitStatus:     p.state.Status.GitStatus,
		GitStaged:     p.state.Status.GitStaged,
		GitModified:   p.state.Status.GitModified,
		GitUntracked:  p.state.Status.GitUntracked,
		GitKnown:      p.state.Status.GitKnown,
		Engine:        engine,
		Slot:          slot,
		Prompt:        prompt,
		TokensIn:      p.state.SessionIn,
		TokensOut:     liveSessionOut(p.state),
		Turns:         turns,
		ContextUsed:   liveContextUsed(p.state),
		ContextWindow: p.state.Status.ContextWindow,
		Model:         p.state.Status.Model,
		Workspace:     p.state.Status.Workspace,
		Policy:        statusPolicy(p.state.Status.Workspace),
	}
}

// handleStatusKey dismisses the card on any key.
//
// The card is read-only, so there is nothing to navigate and nothing to
// confirm: every key means "close". Swallowing the key here keeps a stray
// space or Enter from reaching the composer hidden underneath.
func (p *Program) handleStatusKey(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	p.state.Apply(StatusModalClosed{})
	return p, nil
}
