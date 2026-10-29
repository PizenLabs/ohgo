package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
func updateRestartCard(view *UpdateRestartView, _ *Palette, cardW int) string {
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
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardTeal)).Bold(true).
		Render(TruncateToWidth(titleText, cardW))
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("Restart ohgo to use the new version.")
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardTeal)).Bold(true).
		Render("Press Enter to exit")
	inner := lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", footer)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(wizardTeal)).
		Background(lipgloss.Color(wizardBG)).
		Padding(1, 2).
		Width(cardW).
		Render(inner)
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
// a bounded list of local release notes, and the single key that dismisses it.
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
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardTeal)).Bold(true).
		Render(TruncateToWidth(titleText, cardW))

	heading := strings.TrimSpace(view.Title)
	if heading == "" {
		heading = "What's new"
	}
	rows := []string{title, ""}
	rows = append(rows, lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).Bold(true).
		Render(TruncateToWidth(heading, cardW)))

	changes := view.Changes
	if len(changes) > postUpdateMaxChanges {
		changes = changes[:postUpdateMaxChanges]
	}
	if len(changes) == 0 {
		rows = append(rows, lipgloss.NewStyle().
			Foreground(lipgloss.Color(wizardGrey)).
			Render("No release notes are available for this release."))
	} else {
		marker := lipgloss.NewStyle().Foreground(lipgloss.Color(wizardTeal)).Bold(true)
		label := lipgloss.NewStyle().Foreground(lipgloss.Color(wizardGrey))
		for _, change := range changes {
			text := strings.TrimSpace(change.Title)
			if text == "" {
				continue
			}
			line := marker.Render(changeMarker(change.Type)) + " " +
				label.Render(TruncateToWidth(text, cardW-2))
			rows = append(rows, line)
		}
		if hidden := len(view.Changes) - len(changes); hidden > 0 {
			rows = append(rows, lipgloss.NewStyle().
				Foreground(lipgloss.Color(wizardGrey)).
				Render(fmt.Sprintf("… and %d more", hidden)))
		}
	}
	if strings.TrimSpace(view.Err) != "" {
		rows = append(rows, "", pal.ToolError.Render("Could not record the acknowledgement."))
	}
	rows = append(rows, "")
	rows = append(rows, lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardTeal)).Bold(true).
		Render("Press Enter"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(wizardTeal)).
		Background(lipgloss.Color(wizardBG)).
		Padding(1, 2).
		Width(cardW).
		Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
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
