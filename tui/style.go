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
// every terminal that reports colour and degrade predictably on the rest. The
// code surface is the sole exception (see darkCodeBg): its tint must sit close
// to the deep-forest transcript, which no 256-colour index can express.
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
	// The code surface is the one truecolor body token: no 256-colour index
	// sits close enough to the deep-forest transcript (#0b1210) to read as
	// part of it. This faint emerald stays a surface, never a box.
	darkCodeBg = "#121d19"
	// Deep-forest header scale: bright emerald H1, sage H2, muted sage H3.
	darkH1 = "85"
	darkH2 = "114"
	darkH3 = "144"
	// Warm amber strings read against the dark code background.
	darkCodeStr = "214"
	// Faint user background: a subtle surface behind the question so it
	// reads as a block distinct from the answer, without a label prefix.
	darkUserBg = "237"

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
	// Faint user background for bright terminals: slightly darker than
	// the screen, distinct from the code surface.
	lightUserBg = "253"
)

// fg is the single place a token becomes a style foreground.
func fg(token string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(token))
}

// Semantic chrome tokens: a cohesive low-contrast dark palette
// (Catppuccin / Forest Minimal inspired) for the header, footer, and composer
// chrome.
//
// These are truecolor hex, unlike the 256-colour body tokens above: the
// chrome targets modern terminals where the brand, model, and status segments
// must read as distinct hues, while the transcript body keeps 256-colour
// indexes for broad degrade. Every state that uses colour also carries a text
// marker (● ○ ◐), so nothing depends on hue alone.
const (
	// ColorPrimary is subtle mint teal: the app name, the active cursor,
	// and focused borders.
	ColorPrimary = "#5FB3A1"
	// ColorModel is muted sage green: the model name / active engine.
	ColorModel = "#8FB08D"
	// ColorStatus is muted amber: warnings and check-status indicators.
	ColorStatus = "#C9A86A"
	// ColorAccent is soft steel blue: mode badges and the active runtime.
	ColorAccent = "#7AA8C9"
	// ColorFuzz is FUZZ's magenta workspace identity: semantic authority
	// accent, not theme chrome, so it reads the same under every palette.
	ColorFuzz = "#C678DD"
	// ColorDim is slate gray: dot separators, dividers, and borders.
	ColorDim = "#4C566A"
	// ColorText is warm off-white: primary body text.
	ColorText = "#C9C5BE"
	// ColorMuted is muted gray: labels such as model, workspace, and git.
	ColorMuted = "#6C7A89"
	// ColorMutedAmber is warm muted amber, the approval frame accent for a
	// read or inspect call: the action is side-effect free, so the boundary
	// reads as a checkpoint rather than a hazard. It shares the status amber,
	// so the two amber roles stay one value.
	ColorMutedAmber = ColorStatus
	// ColorRose is warm burnt rose, the approval frame accent for a call that
	// mutates or destroys: the boundary reads as a hazard before the text is.
	ColorRose = "#CD694A"
	// ColorRecap is the dim sage-teal for the post-task summary line. It is
	// deliberately below muted text in contrast: closure is metadata, not a
	// second answer, and it must recede into the transcript.
	ColorRecap = "#5C7E70"
)

// Light-theme counterparts, darkened to read on a bright screen. The hues
// stay in the same families so the chrome keeps one identity per role.
const (
	chromeLightPrimary = "#1F6F61"
	chromeLightModel   = "#4E7A4C"
	chromeLightStatus  = "#8A6D2B"
	chromeLightAccent  = "#3E6E8E"
	chromeLightDim     = "#8A93A3"
	// chromeLightRecap is the same dim sage role darkened for a bright
	// screen: still quieter than body text, still readable.
	chromeLightRecap = "#4F6E61"
)

// Chrome carries the header/footer/composer styles resolved from the tokens
// above: one named style per segment so render code never reaches for a raw
// colour.
type Chrome struct {
	// Brand renders the app name.
	Brand lipgloss.Style
	// Version renders the release label beside the brand, e.g. "v1.0.0".
	// It stays muted: identity first, release metadata second.
	Version lipgloss.Style
	// Model renders the model name / active engine.
	Model lipgloss.Style
	// Active renders the runtime phase and mode badges when active.
	Active lipgloss.Style
	// Idle renders the runtime phase when idle.
	Idle lipgloss.Style
	// Check renders the connection check status.
	Check lipgloss.Style
	// Separator renders dots, dividers, and border lines.
	Separator lipgloss.Style
	// Prompt renders the input cursor.
	Prompt lipgloss.Style
	// Context renders session and workspace context info.
	Context lipgloss.Style
}

// ChromeFor resolves the chrome styles for a theme.
func ChromeFor(theme string) *Chrome {
	if NormalizeTheme(theme) == ThemeLight {
		return &Chrome{
			Brand:     fg(chromeLightPrimary).Bold(true),
			Version:   fg(lightMuted),
			Model:     fg(chromeLightModel),
			Active:    fg(chromeLightAccent).Bold(true),
			Idle:      fg(lightMuted),
			Check:     fg(chromeLightStatus),
			Separator: fg(chromeLightDim),
			Prompt:    fg(chromeLightPrimary),
			Context:   fg(lightMuted),
		}
	}
	return &Chrome{
		Brand:     fg(ColorPrimary).Bold(true),
		Version:   fg(ColorMuted),
		Model:     fg(ColorModel),
		Active:    fg(ColorAccent).Bold(true),
		Idle:      fg(ColorMuted),
		Check:     fg(ColorStatus),
		Separator: fg(ColorDim),
		Prompt:    fg(ColorPrimary),
		Context:   fg(ColorMuted),
	}
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
		Focus:     fg(ColorPrimary).Bold(true),
		Success:   fg(darkSuccess),
		Warning:   fg(darkWarning),
		Danger:    fg(darkDanger),
		Hint:      fg(darkMuted),
		Key:       fg(darkText).Bold(true),
	}
}

// SeparatorLine draws a subtle rule across width cells in the dim chrome
// style, so the header divider and section borders share one slate line.
func SeparatorLine(width int, pal *Palette) string {
	if width < 1 {
		return ""
	}
	return pal.Separator.Render(strings.Repeat("─", width))
}

// badgeNeutral is the fallback accent for an unlabelled workspace.
const badgeNeutral = "244"

// WorkspaceBadge renders the execution-authority label with its semantic
// accent.
//
// The accent is a workspace-owned identity colour (ASK teal, FUZZ magenta,
// BUILD blue), not a theme token: it must read the same under every palette
// (§9). It is passed in by the producer, which reads it from
// workspace.Style(), so this package never hardcodes a workspace colour. An
// empty accent degrades to neutral rather than borrowing an authority's.
func WorkspaceBadge(accent, label string) string {
	if accent == "" {
		accent = badgeNeutral
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Bold(true).Render(label)
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
	if s.Pending != nil || s.QuitConfirm != nil || s.Transition != nil {
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

// hasRunningTool reports any tool still executing. Apply maintains the count,
// so idle frames do not scan a long transcript; hand-built fixtures fall back
// to the scan.
func hasRunningTool(s State) bool {
	if s.runtimeTracked {
		return s.runningTools > 0
	}
	return latestRunningTool(s) != nil
}

// hasAssistantText reports the trailing assistant entry holds streamed text.
func hasAssistantText(s State) bool {
	if s.runtimeTracked {
		return s.assistantText
	}
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
