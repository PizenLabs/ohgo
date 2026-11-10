package tui

import (
	"strings"
)

// IdentityFacts is the truthful runtime state the quickstart surface shows.
//
// It is deliberately narrow. The top bar owns the runtime facts — model,
// workspace, working directory, git state, and readiness — so this surface
// carries only what is ephemeral and unowned: who is being greeted, and by
// name. Empty means unknown and renders as product identity rather than
// invented state.
type IdentityFacts struct {
	// User is the project worker display name, "" when unset.
	User string
	// Greeting is the time-of-day greeting, e.g. "Good evening, Jack."
	Greeting string
}

// FactsFromStatus derives the identity facts from rendered state.
func FactsFromStatus(s State) IdentityFacts {
	return IdentityFacts{
		User:     s.Status.User,
		Greeting: s.Status.Greeting,
	}
}

// capabilityModes are OHGO's three interaction/authority modes. They are
// modes, not a pipeline: rendering keeps them parallel, never joined by an
// arrow.
var capabilityModes = []struct{ name, desc string }{
	{"ASK", "Understand, inspect, reason"},
	{"BUILD", "Authorized changes"},
	{"FUZZ", "$sup · fast assistance"},
}

// identitySubtitle is the product line under the title. It is a constant so
// the layout and the styling layer agree on the exact content; a drifted copy
// would silently lose its Medium Focus role.
const identitySubtitle = "agent runtime for real codebases"

// identityFrameLegend is the quickstart card's category label. It is shared
// with the styling layer so the legend can take the document-structure accent
// while the border stays a separator.
const identityFrameLegend = "Quickstart"

// triggerShortcuts are the affordances the empty screen teaches, in two
// aligned columns. They are a pure function of the product, never of state:
// the same triggers on every fresh session, so the surface never advertises a
// shortcut that does not exist.
var triggerShortcuts = [][]string{
	{"/model", "choose model", "/help", "commands"},
	{"@files", "attach files", "$sup", "quick answers (Ctrl+S)"},
}

// Quickstart composition constants. The indent is the inset inside the frame,
// the glyph anchors the mode column, and the trigger widths align the two
// shortcut columns without hardcoding a second layout in the view layer.
const (
	identityIndent            = "  "
	identityModeGlyph         = "▸"
	identityModeLabelWidth    = 7
	identityTriggerTokenWidth = 9
	identityTriggerHintWidth  = 22
	identityFallbackRuleWidth = 44
	identityMinRuleWidth      = 8
)

// RenderIdentitySurface renders the runtime identity surface for facts at
// width. It is pure: semantic sections first, then a density policy per
// tier, then truncation so no line exceeds width and no border breaks.
//
// Tiers mirror LayoutForMode: wide (>=100) and medium (70-99) box the same
// clean vertical stack — identity, capability modes grouped together, then
// the trigger shortcuts — narrow (<70) drops the border and keeps content
// over decoration. Non-positive widths render the stack unboxed. Sections are
// separated by breathing rows, and a single low-focus rule marks the shortcut
// group; there is no column split, so the modes still read as one authority
// family rather than two split panes.
func RenderIdentitySurface(f IdentityFacts, width int) string {
	if width <= 0 {
		return renderIdentityStack(f, 0)
	}
	switch {
	case width >= WideMinWidth:
		return renderIdentityBoxed(f, width)
	case width >= MediumMinWidth:
		return renderIdentityBoxed(f, width)
	default:
		return renderIdentityStack(f, width)
	}
}

// renderIdentityStack is the borderless semantic stack shared by narrow
// terminals and unbounded renders.
func renderIdentityStack(f IdentityFacts, width int) string {
	lines := identityContentLines(f, width)
	return fitLines(lines, width)
}

// renderIdentityBoxed frames the semantic content in a terminal-native box.
func renderIdentityBoxed(f IdentityFacts, width int) string {
	inner := width - 4
	if inner < 10 {
		// Too small for a box: content beats decoration.
		return renderIdentityStack(f, width)
	}
	content := identityContentLines(f, inner-2)
	// The frame labels the surface, not the release: the top bar already
	// brands the version, so the box legend stays the same on every build.
	title := "─ " + identityFrameLegend + " ─"
	var sb strings.Builder
	sb.WriteString(fitOne("┌"+title+strings.Repeat("─", max(0, inner-CellWidth(title)))+"┐", width) + "\n")
	sb.WriteString(fitOne("│"+strings.Repeat(" ", inner)+"│", width) + "\n")
	for _, line := range content {
		sb.WriteString(fitOne("│ "+truncateCells(line, inner-2)+strings.Repeat(" ", max(0, inner-2-CellWidth(truncateCells(line, inner-2))))+" │", width) + "\n")
	}
	sb.WriteString(fitOne("│"+strings.Repeat(" ", inner)+"│", width) + "\n")
	sb.WriteString(fitOne("└"+strings.Repeat("─", inner)+"┘", width))
	return sb.String()
}

// identityContentLines builds the semantic sections: identity header,
// capability modes grouped together, then the trigger shortcuts. Every section
// is inset and separated by a blank row, so the card reads as framed groups
// rather than a dense list. A single low-focus rule introduces the shortcuts;
// it separates sections without splitting the mode column.
func identityContentLines(f IdentityFacts, contentWidth int) []string {
	var lines []string
	lines = append(lines, identityIndent+identityGreetingLine(f))
	lines = append(lines, identityIndent+identitySubtitle)
	lines = append(lines, "")
	for _, m := range capabilityModes {
		lines = append(lines, identityIndent+identityModeGlyph+" "+padCells(m.name, identityModeLabelWidth)+m.desc)
	}
	lines = append(lines, "")
	lines = append(lines, identityIndent+identityDividerRule(contentWidth))
	lines = append(lines, "")
	for _, row := range triggerShortcuts {
		lines = append(lines, identityIndent+identityShortcutLine(row))
	}
	return lines
}

// identityGreetingLine joins product identity and the personal address with a
// low-focus separator. An unknown identity falls back to the product name
// alone rather than inventing a name or an empty address.
func identityGreetingLine(f IdentityFacts) string {
	address := f.Greeting
	if address == "" {
		address = f.User
	}
	if address == "" {
		return "OHGO"
	}
	return "OHGO  ·  " + address
}

// identityDividerRule sizes the section rule to the available content field.
// The two-cell inset on both sides keeps the stroke inside the frame's
// padding; an unbounded render keeps a readable fixed length.
func identityDividerRule(contentWidth int) string {
	width := identityFallbackRuleWidth
	if contentWidth > 0 {
		width = contentWidth - 2*CellWidth(identityIndent)
	}
	if width < identityMinRuleWidth {
		width = identityMinRuleWidth
	}
	return strings.Repeat("─", width)
}

// identityShortcutLine lays out one row of token/hint pairs on the shared
// column grid. Tokens are taught affordances; hints are their low-focus
// explanations, so the grid, not punctuation, carries the grouping.
func identityShortcutLine(cells []string) string {
	var sb strings.Builder
	for i := 0; i+1 < len(cells); i += 2 {
		sb.WriteString(padCells(cells[i], identityTriggerTokenWidth))
		sb.WriteString(padCells(cells[i+1], identityTriggerHintWidth))
	}
	return strings.TrimRight(sb.String(), " ")
}

func fitLines(lines []string, width int) string {
	if width <= 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, fitOne(line, width))
	}
	return strings.Join(out, "\n")
}

func fitOne(line string, width int) string {
	if width <= 0 {
		return line
	}
	if CellWidth(line) > width {
		return truncateCells(line, width)
	}
	return line
}

func padCells(s string, width int) string {
	if w := CellWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return truncateCells(s, width)
}

func truncateCells(s string, width int) string {
	return TruncateToWidth(s, width)
}
