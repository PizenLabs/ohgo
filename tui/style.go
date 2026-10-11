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

// Semantic colour tokens. Every style in the package resolves through one of
// these, so a theme change lands here and nowhere else.
//
// The indexes are 256-colour, deliberately not truecolor hex: they resolve on
// every terminal that reports colour and degrade predictably on the rest.
// Colour signals state or voice; it never decorates ordinary metadata, and
// every state that uses colour also carries a text marker (✓ ✗ ● ○ ■ ◐).
const (
	// Dark theme: muted surfaces, a single restrained mint accent.
	darkText    = "252" // body text
	darkMuted   = "244" // metadata, separators, hints
	darkAccent  = "121" // mint: focus, the user's voice, selection
	darkTool    = "73"  // soft teal: tool identity, distinct from the accent
	darkSuccess = "42"
	darkWarning = "214"
	darkDanger  = "196"
	darkCodeFg  = "228"
	darkCodeBg  = "236"
	// Deep-forest header scale: bright emerald H1, sage H2, muted sage H3.
	darkH1 = "85"
	darkH2 = "114"
	darkH3 = "144"
	// Warm amber strings read against the dark code background.
	darkCodeStr = "214"

	// Light theme: dark foregrounds on a bright screen, same semantics.
	lightText    = "235"
	lightMuted   = "242"
	lightAccent  = "28"
	lightTool    = "30"
	lightSuccess = "28"
	lightWarning = "166"
	lightDanger  = "160"
	lightCodeFg  = "235"
	lightCodeBg  = "254"
	// Same forest scale for bright terminals: deep emerald H1, dark sage
	// H2, muted grey H3.
	lightH1      = "28"
	lightH2      = "65"
	lightH3      = "242"
	lightCodeStr = "166"
)

// fg is the single place a token becomes a style foreground.
func fg(token string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(token))
}

// ExtraStyles carries the semantic chrome styles that sit above the base
// message palette: separators, the header, severity signals, and key hints.
//
// They exist so header and hint rendering never reach for a raw colour: the
// role is named, and the theme layer decides what it looks like.
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
		return &ExtraStyles{
			Separator: fg(lightMuted),
			Header:    fg(lightText).Bold(true),
			Focus:     fg(lightAccent).Bold(true),
			Success:   fg(lightSuccess),
			Warning:   fg(lightWarning),
			Danger:    fg(lightDanger),
			Hint:      fg(lightMuted),
			Key:       fg(lightText).Bold(true),
		}
	}
	return &ExtraStyles{
		Separator: fg(darkMuted),
		Header:    fg(darkText).Bold(true),
		Focus:     fg(darkAccent).Bold(true),
		Success:   fg(darkSuccess),
		Warning:   fg(darkWarning),
		Danger:    fg(darkDanger),
		Hint:      fg(darkMuted),
		Key:       fg(darkText).Bold(true),
	}
}

// SeparatorLine draws a subtle rule across width cells.
func SeparatorLine(width int, pal *Palette) string {
	if width < 1 {
		return ""
	}
	return pal.Muted.Render(strings.Repeat("─", width))
}

// ComposerHints returns the contextual hint row for the composer.
//
// Every hint shown must work: enter submits, alt+enter / ctrl+j inserts a
// break, esc cancels a turn, pgup/pgdn scroll. The idle set teaches the two
// discovery paths (/ commands, ? help) so nobody must memorise them first.
func ComposerHints(busy bool) string {
	if busy {
		return "enter queue · esc cancel · alt+enter break · pgup/pgdn scroll"
	}
	return "enter send · alt+enter break · / commands · ? help"
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
