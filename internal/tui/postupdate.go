package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/ohgo/internal/tui/design"
)

// postUpdateMaxChanges bounds the acknowledgement card. What's New is a short
// confirmation, not a release-notes screen: the full changelog stays in the
// repository and the binary, and the card names only the head of the list.
const postUpdateMaxChanges = 6

// ShowPostUpdateNotice presents the pending post-update acknowledgement.
//
// The host calls this only after it has verified a persisted update transition
// for the running version. acknowledge persists the acknowledgement; it runs
// only after the user presses Enter, so opening the card never consumes the
// notice.
func (p *Program) ShowPostUpdateNotice(notice PostUpdateView, acknowledge func() error) {
	if strings.TrimSpace(notice.Version) == "" {
		return
	}
	p.ackPostUpdate = acknowledge
	p.state.Apply(PostUpdateOpened{Notice: notice})
}

// acknowledgePostUpdate consumes the notice only after a successful Enter.
// A persistence failure keeps the card open: the user has not acknowledged a
// notice the next launch would still owe them.
func (p *Program) acknowledgePostUpdate() (tea.Model, tea.Cmd) {
	if p.ackPostUpdate != nil {
		if err := p.ackPostUpdate(); err != nil {
			p.state.Apply(PostUpdateAckFailed{Err: err.Error()})
			return p, nil
		}
	}
	p.ackPostUpdate = nil
	p.state.Apply(PostUpdateAcknowledged{})
	return p, nil
}

// SetURLOpener installs the external browser seam used by the What's New
// release link. Nil reports the action unavailable instead of spawning a
// process from the TUI.
func (p *Program) SetURLOpener(fn func(string) error) {
	p.openURL = fn
}

// openPostUpdateLink opens the selected release page and then acknowledges
// the notice. A missing or failed opener keeps the card open: the notice is
// only consumed once the user actually reached the release.
func (p *Program) openPostUpdateLink() (tea.Model, tea.Cmd) {
	view := p.state.PostUpdate
	if view == nil {
		return p, nil
	}
	if p.openURL == nil {
		p.state.Apply(PostUpdateLinkFailed{Err: "opening a browser is unavailable in this session"})
		return p, nil
	}
	if err := p.openURL(strings.TrimSpace(view.URL)); err != nil {
		p.state.Apply(PostUpdateLinkFailed{Err: err.Error()})
		return p, nil
	}
	return p.acknowledgePostUpdate()
}

// postUpdateVisibleChanges is the rendered note list. Navigation and rendering
// share it so the highlight can never land on an empty or hidden row.
func postUpdateVisibleChanges(view *PostUpdateView) []PostUpdateChange {
	if view == nil {
		return nil
	}
	rows := make([]PostUpdateChange, 0, postUpdateMaxChanges)
	for _, change := range view.Changes {
		title := strings.TrimSpace(change.Title)
		if title == "" {
			continue
		}
		rows = append(rows, PostUpdateChange{Type: change.Type, Title: title})
		if len(rows) == postUpdateMaxChanges {
			break
		}
	}
	return rows
}

// postUpdateHiddenChanges counts the notes beyond the card's bound, ignoring
// blank titles that are never rendered.
func postUpdateHiddenChanges(view *PostUpdateView) int {
	if view == nil {
		return 0
	}
	total := 0
	for _, change := range view.Changes {
		if strings.TrimSpace(change.Title) != "" {
			total++
		}
	}
	hidden := total - postUpdateMaxChanges
	if hidden < 0 {
		hidden = 0
	}
	return hidden
}

// postUpdateLinkRow is the index of the release link, or -1 when the host
// supplied no destination.
func postUpdateLinkRow(view *PostUpdateView) int {
	if view == nil || strings.TrimSpace(view.URL) == "" {
		return -1
	}
	return len(postUpdateVisibleChanges(view))
}

// postUpdateRowCount is the selectable row count: the visible notes plus the
// release link when one exists.
func postUpdateRowCount(view *PostUpdateView) int {
	count := len(postUpdateVisibleChanges(view))
	if postUpdateLinkRow(view) >= 0 {
		count++
	}
	return count
}

// normalizePostUpdateCursor keeps the highlight inside the selectable rows.
// A card with no notes may open without a highlight (-1): the link is still
// reachable with "m" or Down, and a plain Enter keeps its old dismiss meaning.
func normalizePostUpdateCursor(view *PostUpdateView) {
	if view == nil {
		return
	}
	count := postUpdateRowCount(view)
	switch {
	case count == 0:
		view.Cursor = -1
	case view.Cursor >= count:
		view.Cursor = count - 1
	case view.Cursor < -1:
		view.Cursor = -1
	}
}

// resetPostUpdateCursor opens the card on the first note when one exists. An
// empty note list rests on no row so Enter alone still acknowledges instead of
// opening the release page.
func resetPostUpdateCursor(view *PostUpdateView) {
	if view == nil {
		return
	}
	if len(postUpdateVisibleChanges(view)) == 0 {
		view.Cursor = -1
		return
	}
	view.Cursor = 0
}

// movePostUpdateCursor shifts the highlight, clamping at both ends. The link
// row is the last row, so a repeated Down parks on it instead of wrapping
// back to the notes.
func movePostUpdateCursor(view *PostUpdateView, delta int) {
	if view == nil {
		return
	}
	view.LinkErr = ""
	normalizePostUpdateCursor(view)
	count := postUpdateRowCount(view)
	if count == 0 || delta == 0 {
		return
	}
	if view.Cursor < 0 {
		// The first move enters the list at the nearest row.
		view.Cursor = 0
		return
	}
	next := view.Cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= count {
		next = count - 1
	}
	view.Cursor = next
}

// focusPostUpdateLink parks the highlight on the release link so the "m"
// shortcut reaches it directly.
func focusPostUpdateLink(view *PostUpdateView) {
	if view == nil {
		return
	}
	view.LinkErr = ""
	if row := postUpdateLinkRow(view); row >= 0 {
		view.Cursor = row
	}
}

// postUpdateLinkSelected reports whether Enter would open the release page.
func postUpdateLinkSelected(view *PostUpdateView) bool {
	if view == nil {
		return false
	}
	row := postUpdateLinkRow(view)
	return row >= 0 && view.Cursor == row
}

// updateRestartCardWidth bounds the restart-required card.
func updateRestartCardWidth(termWidth int) int {
	return centeredCardWidth(termWidth, 56, 34, 12)
}

// postUpdateCardWidth bounds the What's New card.
func postUpdateCardWidth(termWidth int) int {
	return centeredCardWidth(termWidth, 58, 34, 12)
}

// updateRestartCard builds the truthful completion state: the new binary is
// on disk, while the running process is still the old one.
func updateRestartCard(view *UpdateRestartView, pal *Palette, cardW int) string {
	if view == nil {
		return ""
	}
	if cardW < 12 {
		cardW = 12
	}
	version := strings.TrimSpace(view.Version)
	titleText := "Update installed"
	if version != "" {
		titleText = "Update installed: " + version
	}
	theme := themeOf(pal)
	t := design.For(theme)
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Brand)).Bold(true).
		Render(TruncateToWidth(titleText, cardW))
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.TextSecondary)).
		Render("Restart ohgo to use the new version.")
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Brand)).Bold(true).
		Render("Press Enter to exit")
	inner := lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", footer)
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Brand)).
		Padding(1, 2).
		Width(cardW).
		Render(inner)
	return paintSurface(card, t.SurfaceElevated)
}

// renderUpdateRestartArea draws the restart-required card as a centered modal.
func renderUpdateRestartArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	if state.UpdateRestart == nil {
		return
	}
	renderCardAreaWidth(sb, state, layout, updateRestartCardWidth, func(width, _ int) string {
		return updateRestartCard(state.UpdateRestart, pal, width)
	})
}

// changeMarker is the plain glyph that classifies one note. It uses ASCII or
// typographic bullets only: no emoji and no icon font.
func changeMarker(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "added", "improved":
		return "+"
	case "fixed":
		return "•"
	default:
		return "~"
	}
}

// postUpdateCard builds the one-shot acknowledgement: the installed release,
// a navigable list of local release notes, the release link, and the keys that
// operate them. Enter remains the only key that consumes the notice.
func postUpdateCard(view *PostUpdateView, pal *Palette, cardW int) string {
	if view == nil {
		return ""
	}
	if cardW < 12 {
		cardW = 12
	}
	version := strings.TrimSpace(view.Version)
	titleText := "Updated"
	if version != "" {
		titleText = "Updated to " + version
	}
	theme := themeOf(pal)
	t := design.For(theme)
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Brand)).Bold(true).
		Render(TruncateToWidth(titleText, cardW))

	heading := strings.TrimSpace(view.Title)
	if heading == "" {
		heading = "What's new"
	}
	rows := []string{title, ""}
	rows = append(rows, lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.TextSecondary)).Bold(true).
		Render(TruncateToWidth(heading, cardW)))

	accent := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Brand)).Bold(true)
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextSecondary))

	changes := postUpdateVisibleChanges(view)
	if len(changes) == 0 {
		rows = append(rows, lipgloss.NewStyle().
			Foreground(lipgloss.Color(t.TextSecondary)).
			Render("No release notes are available for this release."))
	} else {
		for i, change := range changes {
			text := label.Render(TruncateToWidth(change.Title, cardW-4))
			prefix := "  "
			if view.Cursor == i {
				prefix = "› "
				text = accent.Render(TruncateToWidth(change.Title, cardW-4))
			}
			rows = append(rows, accent.Render(prefix)+accent.Render(changeMarker(change.Type))+" "+text)
		}
		if hidden := postUpdateHiddenChanges(view); hidden > 0 {
			rows = append(rows, lipgloss.NewStyle().
				Foreground(lipgloss.Color(t.TextSecondary)).
				Render(fmt.Sprintf("… and %d more", hidden)))
		}
	}

	if linkRow := postUpdateLinkRow(view); linkRow >= 0 {
		link := label
		prefix := "  "
		if view.Cursor == linkRow {
			prefix = "› "
			link = accent
		}
		rows = append(rows, accent.Render(prefix)+link.Render("↗ ")+
			link.Render(TruncateToWidth(strings.TrimSpace(view.URL), cardW-4)))
		if strings.TrimSpace(view.LinkErr) != "" {
			rows = append(rows, pal.ToolError.Render("Could not open the release page."))
		}
	}

	if strings.TrimSpace(view.Err) != "" {
		rows = append(rows, "", pal.ToolError.Render("Could not record the acknowledgement."))
	}
	rows = append(rows, "")
	hint := "↑↓ navigate · Enter dismiss"
	if postUpdateLinkRow(view) >= 0 {
		hint = "↑↓ navigate · m release page · Enter dismiss"
	}
	rows = append(rows, lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Brand)).Bold(true).
		Render(TruncateToWidth(hint, cardW)))
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Brand)).
		Padding(1, 2).
		Width(cardW).
		Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	return paintSurface(card, t.SurfaceElevated)
}

// renderPostUpdateArea draws the What's New card as a centered modal.
func renderPostUpdateArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	if state.PostUpdate == nil {
		return
	}
	renderCardAreaWidth(sb, state, layout, postUpdateCardWidth, func(width, _ int) string {
		return postUpdateCard(state.PostUpdate, pal, width)
	})
}

// liveUpdateRestartLines renders the restart-required card in output-first
// mode with the same centered, bounded treatment as the other cards.
func liveUpdateRestartLines(s State, pal *Palette, layout Layout) []string {
	if s.UpdateRestart == nil {
		return nil
	}
	return liveModalCardLinesWidth(s, pal, layout, updateRestartCardWidth, func(width, _ int) string {
		return updateRestartCard(s.UpdateRestart, pal, width)
	})
}

// livePostUpdateLines renders the What's New card in output-first mode.
func livePostUpdateLines(s State, pal *Palette, layout Layout) []string {
	if s.PostUpdate == nil {
		return nil
	}
	return liveModalCardLinesWidth(s, pal, layout, postUpdateCardWidth, func(width, _ int) string {
		return postUpdateCard(s.PostUpdate, pal, width)
	})
}
