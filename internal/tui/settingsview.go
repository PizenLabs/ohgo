package tui

// Settings: the preferences surface behind /settings.
//
// The surface is deliberately small and follows the shared modal family: a
// border-embedded title, section headers, and a ruled footer that names the
// keys which actually work. It shows only settings the runtime can apply
// immediately; configuration that belongs to a dedicated surface (/theme,
// /model, /permissions) is not repeated here.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	// settingsModalTitle is the border-embedded title, matching the other
	// framed modals.
	settingsModalTitle = "SETTINGS"

	// settingsSectionInterface groups presentation preferences that do not
	// change execution authority.
	settingsSectionInterface = "INTERFACE"

	// settingsTaskRailLabel and its descriptions are the one preference this
	// surface currently exposes. The descriptions name the trigger and the
	// fallback so the toggle never needs a second explanatory row.
	settingsTaskRailLabel = "Task rail"
	settingsTaskRailOn    = "Shown only while an execution request has open todos. Questions and finished work keep the full-width transcript."
	settingsTaskRailOff   = "Hidden for every request. Todos remain in the transcript and /task."
)

// SettingsState is the open /settings surface.
//
// Cursor indexes the preference rows in render order. The row registry is
// intentionally small; a future setting is one row and one renderer entry, not
// a second modal.
type SettingsState struct {
	// Cursor is the highlighted preference row.
	Cursor int
}

// settingsRowCount is the number of selectable preference rows.
const settingsRowCount = 1

// Move shifts the highlight, wrapping at both ends so the row list stays a
// cycle even when it grows.
func (s *SettingsState) Move(delta int) {
	if s == nil || settingsRowCount == 0 {
		return
	}
	s.Cursor = (s.Cursor + delta) % settingsRowCount
	if s.Cursor < 0 {
		s.Cursor += settingsRowCount
	}
}

// settingsCardWidth bounds the centered dialog. It is the same popup geometry
// as the status and help surfaces, so /settings reads as one more member of the
// modal family rather than a new visual language.
func settingsCardWidth(termWidth int) int {
	return centeredCardWidth(termWidth, 74, 44, statusMinContent+2)
}

// RenderSettingsModal composes the preferences surface as an ANSI string.
//
// width includes the border. height, when positive, bounds the total rendered
// height so a short viewport truncates the body rather than overflowing it.
func RenderSettingsModal(view SettingsState, taskRailEnabled bool, pal *Palette, width, height int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	if width <= 0 {
		width = settingsCardWidth(0)
	}
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}
	content := settingsContent(view, taskRailEnabled, pal, interior)
	// The title border, the footer rule, the footer bar, and the bottom
	// border claim four rows; everything else is body.
	if height > 0 {
		content = fitStatusRows(content, height-4)
	}
	return renderModalFrameWithFooter(settingsModalTitle, content, settingsFooterBar(pal, interior), pal, interior)
}

// settingsContent builds the dialog body: one interface section with the
// current value and the consequence of the opposite state.
func settingsContent(view SettingsState, taskRailEnabled bool, pal *Palette, interior int) []string {
	rows := []string{statusSectionHeader(settingsSectionInterface, interior)}
	rows = append(rows, "")
	rows = append(rows, settingsToggleRow(view.Cursor == 0, settingsTaskRailLabel, taskRailEnabled, pal, interior))
	description := settingsTaskRailOn
	if !taskRailEnabled {
		description = settingsTaskRailOff
	}
	descWidth := interior - statusFieldIndent*2
	if descWidth < 8 {
		descWidth = 8
	}
	for _, line := range WrapToWidth(description, descWidth) {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render(line))
	}
	return rows
}

// settingsToggleRow renders one preference: the selection marker, its label,
// and a text value on the right. On/off is a word, never colour alone, so a
// monochrome terminal still reads the state.
func settingsToggleRow(selected bool, label string, enabled bool, pal *Palette, interior int) string {
	marker := "  "
	if selected {
		marker = "› "
	}
	value := "off"
	valueStyle := pal.Muted
	if enabled {
		value = "on"
		valueStyle = pal.ToolDone
	}
	badge := "[" + value + "]"
	labelText := TruncateToWidth(label, maxInt(interior-CellWidth(marker)-CellWidth(badge)-2, 1))
	gap := interior - CellWidth(marker) - CellWidth(labelText) - CellWidth(badge)
	if gap < 1 {
		gap = 1
	}
	markerStyle := pal.Muted
	if selected {
		markerStyle = pal.Choice
	}
	return markerStyle.Render(marker) + pal.Bold.Render(labelText) +
		strings.Repeat(" ", gap) + valueStyle.Render(badge)
}

// settingsFooterBar names exactly the keys the surface accepts: arrow keys
// only move the highlight, enter/space flips it, and esc closes without a
// second confirmation.
func settingsFooterBar(pal *Palette, interior int) string {
	sep := pal.Muted.Render("  ·  ")
	line := strings.Repeat(" ", statusFieldIndent) +
		pal.Bold.Render("enter / space") + pal.Muted.Render(" toggle") + sep +
		pal.Bold.Render("Esc / q") + pal.Muted.Render(" close")
	return TruncateToWidth(line, interior)
}

// renderSettingsArea draws /settings centered over the workbench. The
// transcript stays untouched behind it; closing returns to the same place.
func renderSettingsArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	view := state.SettingsModal
	if view == nil {
		return
	}
	renderCardAreaWidth(sb, state, layout, settingsCardWidth, func(width, height int) string {
		return RenderSettingsModal(*view, !state.TaskRailDisabled, pal, width, height)
	})
}

// openSettings opens the preferences surface. It refuses while support owns
// the keyboard, matching the no-nested-overlays rule the other working modals
// follow.
func (p *Program) openSettings() {
	if p.state.SupportPanel != nil {
		p.state.Apply(Notice{Text: "close support first (Esc) before /settings"})
		return
	}
	p.state.Apply(SettingsOpened{})
}

// closeSettings dismisses the preferences surface.
func (p *Program) closeSettings() {
	p.state.Apply(SettingsClosed{})
}

// handleSettingsKey routes keys to the open preferences surface.
func (p *Program) handleSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		p.state.Apply(SettingsMoved{Delta: -1})
	case "down", "j":
		p.state.Apply(SettingsMoved{Delta: 1})
	case "enter", " ":
		p.toggleTaskRail()
	case "esc", "ctrl+c", "q":
		p.closeSettings()
	}
	return p, nil
}

// toggleTaskRail flips the preference and persists it through the host seam.
// The modal stays open so the new value is visible immediately.
func (p *Program) toggleTaskRail() {
	enabled := p.state.TaskRailDisabled
	p.state.Apply(TaskRailToggled{Enabled: enabled})
	if p.onTaskRailChanged != nil {
		p.onTaskRailChanged(enabled)
	}
}

// SetTaskRailEnabled applies the saved preference at startup. It does not call
// the persistence handler: loading a value must not write it back.
func (p *Program) SetTaskRailEnabled(enabled bool) {
	p.state.Apply(TaskRailToggled{Enabled: enabled})
}

// SetTaskRailChangedHandler installs the persistence behind /settings. Nil
// keeps the toggle session-only, which is useful for tests and embedded hosts.
func (p *Program) SetTaskRailChangedHandler(onChange func(enabled bool)) {
	p.onTaskRailChanged = onChange
}
