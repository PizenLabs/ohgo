package tui

import (
	"math/rand/v2"
	"strings"
)

// IdentityFacts is the truthful runtime state the quickstart surface shows.
//
// It is deliberately narrow. The top bar owns the runtime facts — model,
// workspace, working directory, git state, and readiness — so this surface
// carries only what is ephemeral and unowned: who is being greeted, by name,
// and the rotating tip. Empty means unknown and renders as product identity
// rather than invented state.
type IdentityFacts struct {
	// User is the project worker display name, "" when unset.
	User string
	// Greeting is the time-of-day greeting, e.g. "Good evening, Jack."
	Greeting string
	// Tip is the micro-annotation shown in the quickstart's bottom grid. It is
	// chosen when a project is entered and stays pinned for that project.
	Tip string
}

// FactsFromStatus derives the identity facts from rendered state. The tip is a
// stored project fact, not a fresh roll: rendering must not shuffle it.
func FactsFromStatus(s State) IdentityFacts {
	return IdentityFacts{
		User:     s.Status.User,
		Greeting: s.Status.Greeting,
		Tip:      s.Tip,
	}
}

// capabilityModes are OHGO's three interaction/authority modes. They are
// modes, not a pipeline: rendering keeps them parallel, never joined by an
// arrow. Each row is label + description on one shared column, so the
// signature accents read as one authority family.
var capabilityModes = []struct{ name, desc string }{
	{"ASK", "Understand, inspect, reason"},
	{"BUILD", "Authorized changes"},
	{"FUZZ", "$sup · fast assistance"},
}

// identitySubtitle is the product line under the greeting. It is a constant so
// the layout and the styling layer agree on the exact content; a drifted copy
// would silently lose its Medium Focus role.
const identitySubtitle = "agent runtime for real codebases"

// identityFrameLegend is the quickstart panel's category label. It is shared
// with the styling layer so the legend can take the document-structure accent
// while the border stays a separator.
const identityFrameLegend = "Quickstart"

// identityBrandText is the single-line product mark. The header carries it in
// the High Focus title role; no block glyph or icon competes with it.
const identityBrandText = "OHGO"

// triggerShortcuts are the affordances the empty screen teaches. They are a
// pure function of the product, never of state: the same triggers on every
// fresh session, so the surface never advertises a shortcut that does not
// exist. Each pair is a token and its low-focus explanation. The grid pairs
// them two per row; the last cell of the second row is the dynamic Tip slot.
var triggerShortcuts = [][2]string{
	{"/model", "choose model"},
	{"/help", "all commands"},
	{"@files", "attach context files"},
}

// identityTipLabel introduces the dynamic tip in the bottom grid. It takes the
// warm reasoning accent so the annotation reads as a helpful aside rather than
// another command token.
const identityTipLabel = "Tip"

// quickstartTips is the curated pool behind the dynamic Tip slot.
//
// A tip is only worth teaching if the runtime actually implements it, so every
// entry names a binding or command from Keybindings or Commands. Keep entries
// short: the grid gives the tip one column beside a command, and a long line
// would fold into a paragraph and break the panel's rhythm.
var quickstartTips = []string{
	"Ctrl+O expands activity",
	"Ctrl+S opens support",
	"PgUp scrolls the transcript",
	"Ctrl+P recalls prompt history",
	"Alt+Enter adds a line break",
	"Type @ to attach a file",
	"/compact trims the context",
	"/permissions sets authority",
	"/status shows runtime facts",
	"/diff shows working tree",
	"Ctrl+D exits when idle",
	"? opens command help",
}

// GetRandomTip returns one tip from the curated pool. It is called when a
// project is entered, not per frame, so the annotation changes at human
// navigation boundaries rather than while the surface is being viewed.
func GetRandomTip() string {
	if len(quickstartTips) == 0 {
		return ""
	}
	return quickstartTips[rand.IntN(len(quickstartTips))]
}

// identityTipText keeps the slot populated for callers that compose the
// surface without a state snapshot. The first pool entry is the fallback, so
// there is only one authored copy of it.
func identityTipText(tip string) string {
	if tip = strings.TrimSpace(tip); tip != "" {
		return tip
	}
	if len(quickstartTips) > 0 {
		return quickstartTips[0]
	}
	return ""
}

// Panel geometry.
//
// The panel is deliberately full-width: it is the empty conversation surface,
// not a floating card, so it should occupy the same field as the transcript it
// replaces. Padding is the only inset; there is no width ceiling.
const (
	// identityPadX is the horizontal inset inside the frame.
	identityPadX = 2
	// identityModeLabelWidth aligns every mode description on one column.
	identityModeLabelWidth = 7
	// identityTriggerTokenWidth aligns the shortcut hints on one column.
	identityTriggerTokenWidth = 9
	// identityCommandGap separates the two quick-command columns.
	identityCommandGap = 4
	// identityCommandMinColumn is the narrowest command column worth a grid:
	// the token column plus room for a short hint. Below two of these the
	// grid folds to one column instead of crushing both.
	identityCommandMinColumn = identityTriggerTokenWidth + 11
	// identityMinFrameWidth is the narrowest panel that can still draw a
	// frame around readable content. Below it the content wins.
	identityMinFrameWidth = 20
	// identityFallbackRuleWidth is the divider length when no viewport has
	// been measured.
	identityFallbackRuleWidth = 56
)

// RenderIdentitySurface renders the runtime identity surface for facts at
// width. It is pure: compose semantic rows once, fold them to the content
// field, then frame the result at the viewport width.
//
// The panel spans its viewport with only the inner padding as an inset. On a
// split pane it keeps the same hierarchy and wraps the copy; below
// identityMinFrameWidth it drops the frame rather than break it. Non-positive
// widths render the unbounded composition for callers that do not know their
// viewport.
func RenderIdentitySurface(f IdentityFacts, width int) string {
	return renderIdentitySurface(f, width, false)
}

// renderIdentitySurface is the composition seam. Compact drops the decorative
// blank rows around the section rule so a short viewport can still show the
// whole panel, including its modes and the tip slot, instead of losing the top
// of the card.
func renderIdentitySurface(f IdentityFacts, width int, compact bool) string {
	if width <= 0 {
		return strings.Join(identityLogicalRows(f, identityFallbackRuleWidth, compact), "\n")
	}
	if width < identityMinFrameWidth {
		return strings.Join(wrapIdentityLines(identityLogicalRows(f, width, compact), width), "\n")
	}
	return renderIdentityPanel(f, width, compact)
}

// renderIdentityPanel draws the full-width panel. Content is wrapped to the
// inner field before padding, so every row fills the same width and no border
// can be pushed off-screen by a long greeting or description.
func renderIdentityPanel(f IdentityFacts, width int, compact bool) string {
	inner := width - 2
	contentW := inner - 2*identityPadX
	if contentW < 1 {
		contentW = 1
	}
	lines := make([]string, 0, len(capabilityModes)+12)
	lines = append(lines, identityTopBorder(inner), identityBlankRow(inner))
	for _, row := range identityLogicalRows(f, contentW, compact) {
		for _, wrapped := range wrapIdentityLine(row, contentW) {
			lines = append(lines, "│"+
				strings.Repeat(" ", identityPadX)+
				padCells(wrapped, contentW)+
				strings.Repeat(" ", identityPadX)+
				"│")
		}
	}
	lines = append(lines, identityBottomBorder(inner))
	return strings.Join(lines, "\n")
}

// identityLogicalRows composes the semantic sections in reading order: header,
// authority modes, a low-focus divider, then the quick commands and tip. Blank
// rows carry the vertical rhythm; compact omits the two around the divider when
// a short viewport must show the whole card.
func identityLogicalRows(f IdentityFacts, contentW int, compact bool) []string {
	rows := make([]string, 0, len(capabilityModes)+10)
	rows = append(rows, identityGreetingLine(f), identitySubtitle, "")
	for _, m := range capabilityModes {
		rows = append(rows, padCells(m.name, identityModeLabelWidth)+m.desc)
	}
	if !compact {
		rows = append(rows, "")
	}
	ruleW := contentW
	if ruleW <= 0 {
		ruleW = identityFallbackRuleWidth
	}
	rows = append(rows, strings.Repeat("─", ruleW))
	if !compact {
		rows = append(rows, "")
	}
	rows = append(rows, identityCommandRows(contentW, f.Tip)...)
	return rows
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
		return identityBrandText
	}
	return identityBrandText + "  ·  " + address
}

// identityCommandRows lays the quick commands and the dynamic tip on the
// panel's bottom grid.
//
// Two balanced columns are the normal form: commands on the left and right,
// with the tip as the last cell. A cell folds its hint under the token column
// when the pane cannot carry it on one line, and the grid falls back to one
// column only when two would crush both. Every line is padded to the content
// field, so the columns stay aligned at every width.
func identityCommandRows(contentW int, tip string) []string {
	cells := identityCommandCells(tip)
	if contentW >= identityCommandGridWidth() {
		colW := (contentW - identityCommandGap) / 2
		if colW > 0 {
			rows := make([]string, 0, (len(cells)+1)/2)
			for i := 0; i < len(cells); i += 2 {
				left := wrapIdentityCommandCell(cells[i], colW)
				var right []string
				if j := i + 1; j < len(cells) {
					right = wrapIdentityCommandCell(cells[j], colW)
				}
				height := len(left)
				if len(right) > height {
					height = len(right)
				}
				for line := 0; line < height; line++ {
					row := strings.Repeat(" ", colW) + strings.Repeat(" ", identityCommandGap)
					if line < len(left) {
						row = padCells(left[line], colW) + strings.Repeat(" ", identityCommandGap)
					}
					if line < len(right) {
						row += padCells(right[line], colW)
					} else {
						row += strings.Repeat(" ", colW)
					}
					rows = append(rows, padCells(row, contentW))
				}
			}
			return rows
		}
	}
	rows := make([]string, 0, len(cells))
	for _, cell := range cells {
		rows = append(rows, wrapIdentityCommandCell(cell, contentW)...)
	}
	return rows
}

// identityCommandCells is the bottom grid's reading order. The first two cells
// fill the left column, the next two the right; the final Tip cell is the
// dynamic annotation that replaces the old static fourth shortcut.
func identityCommandCells(tip string) [][2]string {
	cells := make([][2]string, 0, len(triggerShortcuts)+1)
	cells = append(cells, triggerShortcuts...)
	return append(cells, [2]string{identityTipLabel, identityTipText(tip)})
}

// wrapIdentityCommandCell renders one token/hint pair on the token column and
// folds its hint under that column when the pane is narrow.
func wrapIdentityCommandCell(cell [2]string, width int) []string {
	line := identityCommandPair(cell)
	if width <= 0 || CellWidth(line) <= width {
		return []string{line}
	}
	indent := identityTriggerTokenWidth
	if indent >= width {
		indent = 0
	}
	return wrapIdentityText(line, width, indent)
}

// identityCommandPair renders one token/hint pair on the shared columns.
func identityCommandPair(pair [2]string) string {
	return padCells(pair[0], identityTriggerTokenWidth) + pair[1]
}

// identityCommandGridWidth is the content width the two-column command grid
// needs before two readable columns exist. It is a geometry floor, not the
// width of the current copy: a long tip folds inside its cell instead of
// collapsing the whole grid into a list.
func identityCommandGridWidth() int {
	return 2*identityCommandMinColumn + identityCommandGap
}

// identityTopBorder embeds the category legend in the panel's top rule.
func identityTopBorder(inner int) string {
	title := "─ " + identityFrameLegend + " "
	if CellWidth(title) >= inner {
		return "┌" + strings.Repeat("─", inner) + "┐"
	}
	return "┌" + title + strings.Repeat("─", inner-CellWidth(title)) + "┐"
}

// identityBlankRow is one empty padded row inside the frame.
func identityBlankRow(inner int) string {
	return "│" + strings.Repeat(" ", inner) + "│"
}

// identityBottomBorder closes the frame.
func identityBottomBorder(inner int) string {
	return "└" + strings.Repeat("─", inner) + "┘"
}

// wrapIdentityLines folds a borderless composition to the pane width. The
// continuation indent follows a mode's description column so a wrapped
// authority row stays readable instead of turning into a hanging fragment.
func wrapIdentityLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapIdentityLine(line, width)...)
	}
	return out
}

func wrapIdentityLine(line string, width int) []string {
	return wrapIdentityText(line, width, identityHangingIndent(line))
}

// wrapIdentityText folds one line to width with a fixed continuation indent.
// It returns the line unchanged when it already fits, so a padded grid row is
// never re-folded by the caller.
func wrapIdentityText(line string, width, indent int) []string {
	if width <= 0 || CellWidth(line) <= width {
		return []string{line}
	}
	if indent >= width {
		indent = 0
	}
	var rows []string
	rest := line
	for CellWidth(rest) > width && len(rows) < 64 {
		prefix := CellPrefix(rest, width)
		if prefix == "" {
			break
		}
		cut := len(prefix)
		if space := strings.LastIndex(prefix, " "); space > indent {
			cut = space
		}
		head := strings.TrimRight(rest[:cut], " ")
		if head == "" {
			head = strings.TrimRight(prefix, " ")
			cut = len(prefix)
		}
		rows = append(rows, head)
		rest = strings.Repeat(" ", indent) + strings.TrimLeft(rest[cut:], " ")
	}
	if rest != "" {
		rows = append(rows, truncateCells(rest, width))
	}
	return rows
}

// identityHangingIndent is the description column for a mode row, zero for
// prose.
func identityHangingIndent(line string) int {
	if _, ok := leadingMode(line); ok {
		return identityModeLabelWidth
	}
	return 0
}

// padCells right-pads s to width cells, truncating when it is already wider.
// Padding is measured, never assumed, so wide glyphs keep the grid.
func padCells(s string, width int) string {
	if w := CellWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return truncateCells(s, width)
}

// truncateCells clips s to width terminal cells.
func truncateCells(s string, width int) string {
	return TruncateToWidth(s, width)
}
