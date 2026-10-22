package tui

// The /info page: a centered, read-only modal that collects the platform's
// documentation and changelog in one place.
//
// It is a modal in the same family as the /status card: while open it owns the
// keyboard and the middle of the screen, the composer hides behind it, and any
// key dismisses it without changing anything. The frame reuses the shared modal
// primitives so it reads as one system with the status and usage cards.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// infoModalTitle is the border-embedded title.
const infoModalTitle = "INFORMATION"

// infoModalHiWidth caps the card so ultrawide terminals keep context on either
// side.
const infoModalHiWidth = 84

// InfoView is the snapshot behind the /info page.
//
// It is a plain value so the render stays pure and a test can build one
// directly. The documentation and changelog text are producer-owned; when a
// field is empty the render falls back to the bundled copy so the page is never
// blank.
type InfoView struct {
	// Documentation is the guidance text shown in the Documentation section.
	Documentation string
	// Changelog is the update log shown in the Changelog section.
	Changelog string
	// Docs is an alias for Documentation, accepted for compatibility.
	Docs string
	// Changes is an alias for Changelog, accepted for compatibility.
	Changes string
	// Version is the platform version reported on the page, when known.
	Version string
}

// InformationView is an alias for InfoView.
type InformationView = InfoView

// infoDocumentationText returns the documentation text to render.
func (v InfoView) infoDocumentationText() string {
	for _, c := range []string{v.Documentation, v.Docs} {
		if strings.TrimSpace(c) != "" {
			return c
		}
	}
	return defaultDocumentation
}

// infoChangelogText returns the changelog text to render.
func (v InfoView) infoChangelogText() string {
	for _, c := range []string{v.Changelog, v.Changes} {
		if strings.TrimSpace(c) != "" {
			return c
		}
	}
	return defaultChangelog
}

// defaultDocumentation is the bundled usage guidance.
var defaultDocumentation = strings.Join([]string{
	"Ohgo is a coding agent that reads, edits, and runs code in your terminal.",
	"",
	"Quickstart:",
	"  1. ohgo doctor            check setup; every failure names its fix",
	"  2. ohgo                   start an interactive session",
	"  3. ohgo run \"task\"        one-shot run with a verdict",
	"",
	"In-session commands:",
	"  /help /commands   open the command and help center",
	"  /model            browse or switch the model",
	"  /status           workspace, session, and authority status",
	"  /usage            token usage, cost, and providers",
	"  /doctor           diagnose setup, credentials, and tools",
	"  /diff             inspect the working-tree diff",
	"  /compact          summarise context now",
	"  /quit             exit the session",
	"",
	"Authority modes: /ask (read-only), /fuzz (explore), /build (modify).",
	"Use @file to attach context and $sup for a quick support answer.",
}, "\n")

// defaultChangelog is the bundled change log summary.
var defaultChangelog = strings.Join([]string{
	"v0.1.0-dev",
	"  - Additional information page with Documentation and Changelog sections.",
	"  - TUI modals for status, usage, sessions, help, and connect.",
	"  - Symbol, graph, and refs tools sharing one repository index.",
	"  - Context compaction and subagent delegation.",
	"See CHANGELOG.md for the full history.",
}, "\n")

// RenderInfoModal composes the /info page as an ANSI string.
func RenderInfoModal(view InfoView, pal *Palette, width, height int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	if width <= 0 {
		width = infoModalHiWidth
	}
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}
	content := infoModalContent(view, pal, interior)
	if height > 0 {
		content = fitStatusRows(content, height-4)
	}
	return renderModalFrame(infoModalTitle, content, pal, interior)
}

// RenderInformationModal is an alias for RenderInfoModal.
func RenderInformationModal(view InformationView, pal *Palette, width, height int) string {
	return RenderInfoModal(view, pal, width, height)
}

// infoModalContent builds the card body: Documentation then Changelog.
func infoModalContent(view InfoView, pal *Palette, interior int) []string {
	var rows []string
	rows = append(rows, statusSectionHeader("Documentation", interior))
	for _, line := range strings.Split(view.infoDocumentationText(), "\n") {
		rows = append(rows, padInfoLine(line, interior))
	}
	rows = append(rows, statusRule(pal, interior))
	rows = append(rows, statusSectionHeader("Changelog", interior))
	for _, line := range strings.Split(view.infoChangelogText(), "\n") {
		rows = append(rows, padInfoLine(line, interior))
	}
	if strings.TrimSpace(view.Version) != "" {
		rows = append(rows, statusRule(pal, interior))
		rows = append(rows, statusField("Version", view.Version, pal, interior)...)
	}
	return rows
}

func padInfoLine(line string, interior int) string {
	if line == "" {
		return ""
	}
	return TruncateToWidth(line, interior)
}

// renderInfoArea draws the /info page centered over the workbench.
func renderInfoArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	view := state.InfoModal
	if view == nil {
		view = state.InformationModal
	}
	if view == nil {
		return
	}
	renderCardArea(sb, state, layout, func(width, height int) string {
		return RenderInfoModal(*view, pal, width, height)
	})
}

// openInfo opens the /info page with a snapshot of the bundled copy.
func (p *Program) openInfo() {
	view := InfoView{}
	p.state.Apply(InfoModalOpened{View: view})
	p.state.Apply(InformationModalOpened{View: view})
}

// Event types.
type InfoModalOpened struct{ View InfoView }

func (InfoModalOpened) eventKind() string { return "info_modal_opened" }

type InfoModalClosed struct{}

func (InfoModalClosed) eventKind() string { return "info_modal_closed" }

type InformationModalOpened struct{ View InformationView }

func (InformationModalOpened) eventKind() string { return "information_modal_opened" }

type InformationModalClosed struct{}

func (InformationModalClosed) eventKind() string { return "information_modal_closed" }

// handleInfoKey dismisses the page on any key.
func (p *Program) handleInfoKey(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	p.state.Apply(InfoModalClosed{})
	p.state.Apply(InformationModalClosed{})
	return p, nil
}

// Keep imports used.
var _ = tea.Cmd(nil)
var _ = lipgloss.NewStyle
var _ = fmt.Sprintf
