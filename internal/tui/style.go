package tui

import (
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/tui/design"
	"github.com/charmbracelet/lipgloss"
)

// Design-system spacing. One place for padding so regions align.
const (
	// PadX is the horizontal inset for conversation content.
	PadX = 2
	// GapY is the blank rows between transcript entries.
	GapY = 1
)

// fg is the single place a token becomes a style foreground.
func fg(token string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(token))
}

// DarkTokens and LightTokens are the resolved design foundation. Constructors
// in this package consume these roles; no component below should reach for a
// raw colour literal.
func DarkTokens() design.Tokens  { return design.Dark() }
func LightTokens() design.Tokens { return design.Light() }

// Palette and chrome caches: a render asks for a theme's style bundle many
// times per frame, and the tokens never change at runtime. Caching keeps a
// theme switch a lookup rather than a rebuild.
var (
	darkPaletteOnce   sync.Once
	darkPaletteCache  *Palette
	lightPaletteOnce  sync.Once
	lightPaletteCache *Palette

	darkChromeOnce   sync.Once
	darkChromeCache  *Chrome
	lightChromeOnce  sync.Once
	lightChromeCache *Chrome

	darkExtrasOnce   sync.Once
	darkExtrasCache  *ExtraStyles
	lightExtrasOnce  sync.Once
	lightExtrasCache *ExtraStyles
)

// Semantic mode accents are the workspace's identity colors, re-exported here
// so presentation code has one name to reach for. The dark and light forms are
// the same hue at different depths: the prompt stays soft on a dark terminal
// and readable on a bright one, without turning the mode into a different
// color.
const (
	// ModeAccentAsk is ASK's teal identity on a dark screen.
	ModeAccentAsk = workspace.AccentAsk
	// ModeAccentAskLight is ASK's deep teal identity on a bright screen.
	ModeAccentAskLight = workspace.AccentAskLight
	// ModeAccentBuild is BUILD's blue identity on a dark screen.
	ModeAccentBuild = workspace.AccentBuild
	// ModeAccentBuildLight is BUILD's deep blue identity on a bright screen.
	ModeAccentBuildLight = workspace.AccentBuildLight
	// ModeAccentFuzz is FUZZ's magenta/coral identity on a dark screen.
	ModeAccentFuzz = workspace.AccentFuzz
	// ModeAccentFuzzLight is FUZZ's deep magenta identity on a bright screen.
	ModeAccentFuzzLight = workspace.AccentFuzzLight
)

// ModeAccentFor resolves an authority label to its semantic accent for a
// theme. An unknown or empty label returns "", so callers fall back to neutral
// chrome instead of inventing an authority color.
func ModeAccentFor(theme, label string) string {
	parsed, err := workspace.Parse(label)
	if err != nil {
		return ""
	}
	style := parsed.Style()
	if NormalizeTheme(theme) == ThemeLight && style.LightAccent != "" {
		return style.LightAccent
	}
	return style.Accent
}

// ModeAccent resolves an authority label to its dark-screen accent. It is the
// compatibility form for surfaces that do not carry a theme.
func ModeAccent(label string) string {
	return ModeAccentFor(ThemeDark, label)
}

// themeOf reports the appearance behind a palette, defaulting to dark for a
// nil or hand-built style bundle.
func themeOf(pal *Palette) string {
	if pal != nil && pal.Theme != "" {
		return NormalizeTheme(pal.Theme)
	}
	return ThemeDark
}

// themeSummaryFor names the stored preference, the effective palette, and how
// it was resolved. One formatter serves /theme, the picker card, and notices,
// so the three-part contract cannot drift between surfaces.
func themeSummaryFor(state State) string {
	return formatThemeSummary(state.ThemePreference, state.Theme, state.ThemeSource)
}

func formatThemeSummary(preference, effective, source string) string {
	preference = NormalizeThemePreference(preference)
	effective = NormalizeTheme(effective)
	if preference != ThemeAuto {
		return "Theme: " + titleTheme(preference) + " (explicit)"
	}
	switch source {
	case ThemeSourceSystem:
		return "Theme: Auto → " + titleTheme(effective) + " (system)"
	case ThemeSourceFallback:
		return "Theme: Auto → " + titleTheme(effective) + " (fallback: system unavailable)"
	default:
		return "Theme: Auto → " + titleTheme(effective)
	}
}

// titleTheme formats an effective or explicit appearance for user-facing
// chrome. Auto is not a palette and must be resolved before calling this.
func titleTheme(theme string) string {
	if NormalizeTheme(theme) == ThemeLight {
		return "Light"
	}
	return "Dark"
}

// modeStyle renders an authority label in its semantic accent for a theme.
// The fallback is used only for an unknown label: a malformed mode must not
// borrow a real authority's identity.
func modeStyle(theme, label string, fallback lipgloss.Style) lipgloss.Style {
	if accent := ModeAccentFor(theme, label); accent != "" {
		return fg(accent).Bold(true)
	}
	return fallback
}

// Chrome carries the header/footer/composer styles resolved from the tokens
// above: one named style per segment so render code never reaches for a raw
// colour.
type Chrome struct {
	// Theme is the normalized palette these styles were resolved for. Mode
	// accents use it so a caller holding only Chrome still resolves the right
	// light/dark form.
	Theme string
	// Brand renders the app name in the High Focus title role: the product
	// identity is the strongest text on the chrome, not an accent-coloured
	// label competing with the authority modes.
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
		lightChromeOnce.Do(func() {
			t := LightTokens()
			lightChromeCache = &Chrome{
				Theme:     ThemeLight,
				Brand:     fg(t.TextTitle).Bold(true),
				Version:   fg(t.TextMuted),
				Model:     fg(t.Model),
				Active:    fg(t.Active).Bold(true),
				Idle:      fg(t.Idle),
				Check:     fg(t.Check),
				Separator: fg(t.BorderDefault),
				Prompt:    fg(t.Prompt),
				Context:   fg(t.TextMuted),
			}
		})
		return lightChromeCache
	}
	darkChromeOnce.Do(func() {
		t := DarkTokens()
		darkChromeCache = &Chrome{
			Theme:     ThemeDark,
			Brand:     fg(t.TextTitle).Bold(true),
			Version:   fg(t.TextMuted),
			Model:     fg(t.Model),
			Active:    fg(t.Active).Bold(true),
			Idle:      fg(t.Idle),
			Check:     fg(t.Check),
			Separator: fg(t.BorderDefault),
			Prompt:    fg(t.Prompt),
			Context:   fg(t.TextMuted),
		}
	})
	return darkChromeCache
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
		lightExtrasOnce.Do(func() {
			t := LightTokens()
			lightExtrasCache = &ExtraStyles{
				Separator: fg(t.BorderDefault),
				Header:    fg(t.TextTitle).Bold(true),
				Focus:     fg(t.BorderFocus).Bold(true),
				Success:   fg(t.Success),
				Warning:   fg(t.Warning),
				Danger:    fg(t.Error),
				Hint:      fg(t.TextMuted),
				Key:       fg(t.TextPrimary).Bold(true),
			}
		})
		return lightExtrasCache
	}
	darkExtrasOnce.Do(func() {
		t := DarkTokens()
		darkExtrasCache = &ExtraStyles{
			Separator: fg(t.BorderDefault),
			Header:    fg(t.TextTitle).Bold(true),
			Focus:     fg(t.BorderFocus).Bold(true),
			Success:   fg(t.Success),
			Warning:   fg(t.Warning),
			Danger:    fg(t.Error),
			Hint:      fg(t.TextMuted),
			Key:       fg(t.TextPrimary).Bold(true),
		}
	})
	return darkExtrasCache
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
	// RuntimeCommitting means the dedicated /commit workflow is generating a
	// proposal or executing one approved proposal.
	RuntimeCommitting RuntimeState = "committing"
	// RuntimeShuttingDown means the session is exiting.
	RuntimeShuttingDown RuntimeState = "shutting down"
)

// DeriveRuntime maps state to its display phase.
func DeriveRuntime(s State) RuntimeState {
	if s.Quit {
		return RuntimeShuttingDown
	}
	// Commit work is an explicit lifecycle, not an inference from Busy: the
	// agent turn is idle while the commit model is answering.
	if s.CommitActive() {
		return RuntimeCommitting
	}
	if s.Pending != nil || s.QuitConfirm != nil || s.Transition != nil || s.UpdateRestart != nil || s.PostUpdate != nil {
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
	case RuntimeStreaming, RuntimeWorking, RuntimeRunningTool, RuntimeCommitting:
		return "●"
	case RuntimeAwaitingApproval:
		return "◐"
	case RuntimeShuttingDown:
		return "■"
	default:
		return "○"
	}
}
