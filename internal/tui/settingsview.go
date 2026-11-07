package tui

// Settings: the preferences surface behind /settings.
//
// The surface is deliberately small and follows the shared modal family: a
// border-embedded title, section headers, and a ruled footer that names the
// keys which actually work. It shows only settings the runtime can apply
// immediately; configuration that belongs to a dedicated surface (/theme,
// /model, /permissions) is not repeated here.

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/continuity/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	// settingsModalTitle is the border-embedded title, matching the other
	// framed modals.
	settingsModalTitle = "SETTINGS"

	// settingsSectionInterface groups presentation preferences that do not
	// change execution authority.
	settingsSectionInterface = "INTERFACE"

	// settingsSectionCleanup groups transcript retention preferences.
	settingsSectionCleanup = "CLEANUP"

	// settingsTaskRailLabel and its descriptions name the trigger and the
	// fallback so the toggle never needs a second explanatory row.
	settingsTaskRailLabel = "Task rail"
	settingsTaskRailOn    = "Shown only while an execution request has open todos. Questions and finished work keep the full-width transcript."
	settingsTaskRailOff   = "Hidden for every request. Todos remain in the transcript and /task."

	// Cleanup rows and their values. Labels are words, never bare glyphs.
	settingsAutoPruneLabel   = "Auto-Prune Sessions"
	settingsKeepRecentLabel  = "Keep Recent Max"
	settingsManualCleanLabel = "Run Manual Clean"
	settingsManualCleanValue = "press Enter"
	settingsCleanupNote      = "Protected sessions are never pruned. Explicit delete still works."
)

// Settings row indexes, in render order.
const (
	settingsRowTaskRail = iota
	settingsRowAutoPrune
	settingsRowKeepRecent
	settingsRowManualClean
	settingsRowCount
)

// DefaultKeepRecentSessions is the store-owned default when a host supplies no
// positive value.
const DefaultKeepRecentSessions = session.DefaultKeepRecent

// SettingsState is the open /settings surface.
//
// Cursor indexes the preference rows in render order. The row registry is
// intentionally small; a future setting is one row and one renderer entry, not
// a second modal.
type SettingsState struct {
	// Cursor is the highlighted preference row.
	Cursor int
	// AutoPrune and KeepRecent mirror the durable cleanup values while the
	// surface is open. They are a snapshot of host-owned settings, refreshed
	// through state events when a row changes.
	AutoPrune  bool
	KeepRecent int
	// Notice reports the last manual-clean outcome or a persistence failure.
	Notice string
}

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

// settingsContent builds the dialog body: an interface section with the
// current task-rail value, then the cleanup section with retention and an
// explicit manual action.
func settingsContent(view SettingsState, taskRailEnabled bool, pal *Palette, interior int) []string {
	rows := []string{statusSectionHeader(settingsSectionInterface, interior)}
	rows = append(rows, "")
	rows = append(rows, settingsToggleRow(view.Cursor == settingsRowTaskRail, settingsTaskRailLabel, taskRailEnabled, pal, interior))
	description := settingsTaskRailOn
	if !taskRailEnabled {
		description = settingsTaskRailOff
	}
	rows = append(rows, settingsDescription(description, pal, interior)...)

	rows = append(rows, "")
	rows = append(rows, statusSectionHeader(settingsSectionCleanup, interior))
	rows = append(rows, "")
	rows = append(rows, settingsToggleRow(view.Cursor == settingsRowAutoPrune, settingsAutoPruneLabel, view.AutoPrune, pal, interior))
	rows = append(rows, settingsValueRow(view.Cursor == settingsRowKeepRecent, settingsKeepRecentLabel,
		fmt.Sprintf("%d", normalizeKeepRecent(view.KeepRecent)), pal.ToolDone, pal, interior))
	rows = append(rows, settingsValueRow(view.Cursor == settingsRowManualClean, settingsManualCleanLabel,
		settingsManualCleanValue, pal.Choice, pal, interior))
	rows = append(rows, settingsDescription(settingsCleanupNote, pal, interior)...)
	if view.Notice != "" {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+
			pal.ToolRun.Render(TruncateToWidth(sanitizeInline(view.Notice), maxInt(interior-statusFieldIndent, 1))))
	}
	return rows
}

// settingsDescription renders a muted explanatory paragraph at the dialog's
// standard interior indent.
func settingsDescription(text string, pal *Palette, interior int) []string {
	descWidth := interior - statusFieldIndent*2
	if descWidth < 8 {
		descWidth = 8
	}
	out := make([]string, 0, 2)
	for _, line := range WrapToWidth(text, descWidth) {
		out = append(out, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render(line))
	}
	return out
}

// settingsToggleRow renders one boolean preference: the selection marker, its
// label, and a text value on the right. On/off is a word, never colour alone,
// so a monochrome terminal still reads the state.
func settingsToggleRow(selected bool, label string, enabled bool, pal *Palette, interior int) string {
	value := "off"
	valueStyle := pal.Muted
	if enabled {
		value = "on"
		valueStyle = pal.ToolDone
	}
	return settingsValueRow(selected, label, value, valueStyle, pal, interior)
}

// settingsValueRow renders one preference row with an arbitrary text value.
func settingsValueRow(selected bool, label, value string, valueStyle lipgloss.Style, pal *Palette, interior int) string {
	marker := "  "
	if selected {
		marker = "› "
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

// normalizeKeepRecent bounds a host-supplied retention count to a usable
// positive value. Zero and negatives are "unset", not "delete everything".
func normalizeKeepRecent(value int) int {
	if value <= 0 {
		return DefaultKeepRecentSessions
	}
	return value
}

// settingsFooterBar names exactly the keys the surface accepts: arrows move,
// enter/space toggles the highlighted row or runs the manual clean, and the
// left/right pair adjusts the numeric retention value.
func settingsFooterBar(pal *Palette, interior int) string {
	sep := pal.Muted.Render("  ·  ")
	line := strings.Repeat(" ", statusFieldIndent) +
		pal.Bold.Render("↑↓") + pal.Muted.Render(" move") + sep +
		pal.Bold.Render("enter / space") + pal.Muted.Render(" toggle / run") + sep +
		pal.Bold.Render("←→") + pal.Muted.Render(" adjust") + sep +
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
	case "left", "h", "-":
		p.adjustKeepRecent(-1)
	case "right", "l", "+", "=":
		p.adjustKeepRecent(1)
	case "enter", " ":
		p.activateSettingsRow()
	case "esc", "ctrl+c", "q":
		p.closeSettings()
	}
	return p, nil
}

// activateSettingsRow performs the action the highlighted row names.
func (p *Program) activateSettingsRow() {
	view := p.state.SettingsModal
	if view == nil {
		return
	}
	switch view.Cursor {
	case settingsRowTaskRail:
		p.toggleTaskRail()
	case settingsRowAutoPrune:
		p.setCleanupSettings(!p.state.AutoPruneEnabled, p.state.KeepRecentMax)
	case settingsRowManualClean:
		p.runManualClean()
	}
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

// adjustKeepRecent changes the retention count by one, clamped so a manual
// clean can never be configured to retain nothing.
func (p *Program) adjustKeepRecent(delta int) {
	if p.state.SettingsModal == nil || p.state.SettingsModal.Cursor != settingsRowKeepRecent {
		return
	}
	keep := normalizeKeepRecent(p.state.KeepRecentMax)
	keep += delta
	if keep < 1 {
		keep = 1
	}
	if keep == p.state.KeepRecentMax {
		return
	}
	p.setCleanupSettings(p.state.AutoPruneEnabled, keep)
}

// setCleanupSettings applies and persists the cleanup preferences through the
// host seams. A missing persistence handler keeps the change session-only.
func (p *Program) setCleanupSettings(autoPrune bool, keepRecent int) {
	keepRecent = normalizeKeepRecent(keepRecent)
	p.state.Apply(CleanupSettingsChanged{AutoPrune: autoPrune, KeepRecent: keepRecent})
	if p.onCleanupChanged != nil {
		p.onCleanupChanged(autoPrune, keepRecent)
	}
}

// runManualClean asks the host to prune immediately with the configured
// retention count and reports the measured outcome in the modal.
func (p *Program) runManualClean() {
	if p.onCleanupRun == nil {
		p.state.Apply(SettingsNoticed{Text: "cleanup is unavailable"})
		return
	}
	keep := normalizeKeepRecent(p.state.KeepRecentMax)
	// The live conversation is exempt from the count as well as deletion:
	// manually pruning from /settings must never remove the session the user
	// is working in.
	deleted, err := p.onCleanupRun(keep, p.activeSessionID())
	if err != nil {
		p.state.Apply(SettingsNoticed{Text: "clean: " + err.Error()})
		return
	}
	p.state.Apply(SettingsNoticed{Text: fmt.Sprintf("pruned %d session(s); kept the %d most recent", deleted, keep)})
}

// SetCleanupSettings applies saved retention values at startup. It does not
// save them back: loading a preference is not a write.
func (p *Program) SetCleanupSettings(autoPrune bool, keepRecent int) {
	p.state.Apply(CleanupSettingsChanged{AutoPrune: autoPrune, KeepRecent: normalizeKeepRecent(keepRecent)})
}

// SetCleanupChangedHandler installs persistence behind the cleanup controls.
// Nil keeps changes session-only for tests and embedded hosts.
func (p *Program) SetCleanupChangedHandler(onChange func(autoPrune bool, keepRecent int)) {
	p.onCleanupChanged = onChange
}

// SetCleanupRunner installs the immediate pruning pass behind "Run Manual
// Clean". The returned count is the number of deleted sessions. activeSessionID
// is passed so the host can exempt the live conversation.
func (p *Program) SetCleanupRunner(run func(keepRecent int, activeSessionID int64) (int, error)) {
	p.onCleanupRun = run
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
