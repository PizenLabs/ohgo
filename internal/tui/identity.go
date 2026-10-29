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

// triggerShortcuts are the affordances the empty screen teaches. They are a
// pure function of the product, never of state: the same three triggers on
// every fresh session, so the surface never advertises a shortcut that does
// not exist.
var triggerShortcuts = []string{
	"/model  choose model    /help  commands",
	"@files  attach files    $sup   quick answers (Ctrl+S)",
}

// RenderIdentitySurface renders the runtime identity surface for facts at
// width. It is pure: semantic sections first, then a density policy per
// tier, then truncation so no line exceeds width and no border breaks.
//
// Tiers mirror LayoutForMode: wide (>=100) and medium (70-99) box the same
// clean vertical stack — identity, capability modes grouped together, then
// the trigger shortcuts — narrow (<70) drops the border and keeps content
// over decoration. Non-positive widths render the stack unboxed. There are
// no inner divider rules: the outer frame is the only box drawing, so the
// modes read as one group rather than two split panes.
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
	lines := identityContentLines(f)
	return fitLines(lines, width)
}

// renderIdentityBoxed frames the semantic content in a terminal-native box.
func renderIdentityBoxed(f IdentityFacts, width int) string {
	inner := width - 4
	if inner < 10 {
		// Too small for a box: content beats decoration.
		return renderIdentityStack(f, width)
	}
	content := identityContentLines(f)
	// The frame labels the surface, not the release: the top bar already
	// brands the version, so the box legend stays the same on every build.
	title := "─ Quickstart ─"
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
// capability modes grouped together, then the trigger shortcuts. One vertical
// stack at every width: modes are never split across columns, the top bar's
// runtime metadata is never repeated here, and there are no inner divider
// rules — the outer frame carries the only box drawing.
func identityContentLines(f IdentityFacts) []string {
	var lines []string
	title := "OHGO"
	if f.Greeting != "" {
		// Greeting carries the name; title stays product identity.
		lines = append(lines, title+"   "+f.Greeting)
	} else if f.User != "" {
		lines = append(lines, title+"   "+f.User)
	} else {
		lines = append(lines, title)
	}
	lines = append(lines, "agent runtime for real codebases")
	lines = append(lines, "")
	for _, m := range capabilityModes {
		lines = append(lines, padCells(m.name, 9)+m.desc)
	}
	lines = append(lines, "")
	lines = append(lines, triggerShortcuts...)
	return lines
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
