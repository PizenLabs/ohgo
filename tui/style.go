package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Design-system spacing. One place for padding so regions align.
const (
	// PadX is the horizontal inset for conversation content.
	PadX = 2
	// GapY is the blank rows between transcript entries.
	GapY = 1
)

// Semantic accent roles. Hues stay restrained: color signals state, never
// decorates. Every state also carries a text marker (✓ ✗ ● ○) so nothing
// depends on color alone.
const (
	accentBlue   = "39"
	accentViolet = "141"
	accentGreen  = "42"
	accentAmber  = "214"
	accentRed    = "196"
	mutedDark    = "244"
	mutedLight   = "242"
)

// ExtraStyles carries semantic styles beyond the base palette.
//
// The base Palette stays the source of truth for message colors; these cover
// chrome introduced by the workbench layout: separators, headers, focus,
// and severity signals.
type ExtraStyles struct {
	Separator lipgloss.Style
	Header    lipgloss.Style
	Focus     lipgloss.Style
	Success   lipgloss.Style
	Warning   lipgloss.Style
	Danger    lipgloss.Style
	Hint      lipgloss.Style
	Key       lipgloss.Style
}

// ExtrasFor resolves semantic chrome styles for a theme.
func ExtrasFor(theme string) *ExtraStyles {
	if NormalizeTheme(theme) == ThemeLight {
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color(mutedLight))
		return &ExtraStyles{
			Separator: muted,
			Header:    lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Bold(true),
			Focus:     lipgloss.NewStyle().Foreground(lipgloss.Color("27")).Bold(true),
			Success:   lipgloss.NewStyle().Foreground(lipgloss.Color("28")),
			Warning:   lipgloss.NewStyle().Foreground(lipgloss.Color("166")),
			Danger:    lipgloss.NewStyle().Foreground(lipgloss.Color("160")),
			Hint:      muted,
			Key:       lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Bold(true),
		}
	}
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(mutedDark))
	return &ExtraStyles{
		Separator: muted,
		Header:    lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true),
		Focus:     lipgloss.NewStyle().Foreground(lipgloss.Color(accentBlue)).Bold(true),
		Success:   lipgloss.NewStyle().Foreground(lipgloss.Color(accentGreen)),
		Warning:   lipgloss.NewStyle().Foreground(lipgloss.Color(accentAmber)),
		Danger:    lipgloss.NewStyle().Foreground(lipgloss.Color(accentRed)),
		Hint:      muted,
		Key:       lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true),
	}
}

// SeparatorLine draws a subtle rule across width cells.
func SeparatorLine(width int, pal *Palette) string {
	if width < 1 {
		return ""
	}
	return pal.Muted.Render(strings.Repeat("─", width))
}

// KeyHint renders one keyboard hint as "key action".
func KeyHint(key, action string, pal *Palette) string {
	ex := ExtrasFor(ThemeDark)
	_ = ex
	return pal.Muted.Render(key + " " + action)
}

// ComposerHints returns the contextual hint row for the composer.
//
// Every hint shown must work: enter submits, alt+enter / ctrl+j inserts a
// break, esc cancels a turn, pgup/pgdn scroll. No decorative shortcuts.
func ComposerHints(busy bool) string {
	if busy {
		return "enter queue · esc cancel · alt+enter break · pgup/pgdn scroll"
	}
	return "enter send · alt+enter break · ↑ history · pgup/pgdn scroll"
}

// RuntimeState names the agent/UI phase for header and status.
//
// It is derived from State on every render, never stored: stored phases drift
// from the loop that owns the truth.
type RuntimeState string

// Runtime phases.
const (
	// RuntimeIdle means ready for input.
	RuntimeIdle RuntimeState = "idle"
	// RuntimeStreaming means the model is producing text.
	RuntimeStreaming RuntimeState = "streaming"
	// RuntimeRunningTool means a tool is executing.
	RuntimeRunningTool RuntimeState = "running tool"
	// RuntimeAwaitingApproval means a tool is blocked on the user.
	RuntimeAwaitingApproval RuntimeState = "awaiting approval"
	// RuntimeWorking covers a busy turn with no finer signal.
	RuntimeWorking RuntimeState = "working"
	// RuntimeShuttingDown means the session is exiting.
	RuntimeShuttingDown RuntimeState = "shutting down"
)

// DeriveRuntime maps state to its display phase.
func DeriveRuntime(s State) RuntimeState {
	if s.Quit {
		return RuntimeShuttingDown
	}
	if s.Pending != nil {
		return RuntimeAwaitingApproval
	}
	if hasRunningTool(s) {
		return RuntimeRunningTool
	}
	if s.Status.Busy {
		if hasAssistantText(s) {
			return RuntimeStreaming
		}
		return RuntimeWorking
	}
	return RuntimeIdle
}

// hasRunningTool reports any tool still executing.
func hasRunningTool(s State) bool {
	for _, e := range s.Entries {
		if e.Kind == EntryTool && e.Tool != nil && e.Tool.Status == ToolRunning {
			return true
		}
	}
	return false
}

// hasAssistantText reports the trailing assistant entry holds streamed text.
func hasAssistantText(s State) bool {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == EntryAssistant {
			return s.Entries[i].AssistantText != ""
		}
	}
	return false
}

// RuntimeMarker pairs each phase with a text marker so state never depends on
// color alone.
func RuntimeMarker(r RuntimeState) string {
	switch r {
	case RuntimeIdle:
		return "○"
	case RuntimeStreaming, RuntimeWorking, RuntimeRunningTool:
		return "●"
	case RuntimeAwaitingApproval:
		return "◐"
	case RuntimeShuttingDown:
		return "■"
	default:
		return "○"
	}
}
