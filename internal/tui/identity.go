package tui

import "strings"

// IdentityFacts is the truthful runtime state the quickstart surface shows.
//
// It is deliberately narrow. The top bar owns the runtime facts — model,
// workspace, working directory, git state, and readiness — so this surface
// carries only what is ephemeral and unowned: who is being greeted, by name.
// Empty means unknown and renders as product identity rather than invented
// state.
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
// arrow. Each row is label + description on one shared column, so the
// signature accents read as one authority family.
var capabilityModes = []struct{ name, desc string }{
	{"ASK", "Understand, inspect, reason"},
	{"BUILD", "Authorized changes"},
	{"FUZZ", "$sup · fast assistance"},
}

// identitySubtitle is the product line under the greeting. It is a constant so
// the layout and the styling layer agree on the exact content; a drifted copy
// would silently lose its Muted role.
const identitySubtitle = "agent runtime for real codebases"

// identityFrameLegend is the quickstart panel's quiet category label. It is
// deliberately lowercase and shares the border's role, so it reads as part of
// the frame rather than competing with the greeting inside.
const identityFrameLegend = "quickstart"

// identityBrandText is the single-line product mark. The header carries it in
// the High Focus title role.
const identityBrandText = "OHGO"

// The composer's placeholder rotation. Each pool groups hints by the
// interaction they belong to, so the hint a user sees is relevant to the
// authority they are in rather than a mechanical passthrough of one list.
//
// A hint is only worth teaching if the runtime actually implements it, so
// every entry names a binding or command from Keybindings or Commands. Keep
// entries short: the hint shares one row with the prompt and the caret.
const (
	hintContextWelcome = "welcome"
	hintContextAsk     = "ASK"
	hintContextBuild   = "BUILD"
	hintContextFuzz    = "FUZZ"
)

var promptHintPools = map[string][]string{
	hintContextWelcome: {
		"Type @ to attach a file",
		"? opens command help",
		"Alt+Enter adds a line break",
		"Ctrl+P recalls prompt history",
	},
	hintContextAsk: {
		"Type @ to attach a file",
		"/diff shows working tree",
		"/status shows runtime facts",
		"PgUp scrolls the transcript",
		"? opens command help",
	},
	hintContextBuild: {
		"/permissions sets authority",
		"Alt+Enter adds a line break",
		"Type @ to attach a file",
		"/diff shows working tree",
		"Ctrl+P recalls prompt history",
		"Ctrl+O expands activity",
	},
	hintContextFuzz: {
		"/permissions sets authority",
		"? opens command help",
		"Type @ to attach a file",
		"Ctrl+O expands activity",
		"Ctrl+D exits when idle",
	},
}

// hintContext classifies the composer's current interaction so the placeholder
// teaches a binding that applies now. A fresh conversation gets onboarding
// hints; an active one follows the authority that owns the next prompt.
func hintContext(s State) string {
	if !s.SessionActive && len(s.Entries) == 0 {
		return hintContextWelcome
	}
	switch strings.ToUpper(strings.TrimSpace(s.Status.Workspace)) {
	case hintContextAsk:
		return hintContextAsk
	case hintContextBuild:
		return hintContextBuild
	case hintContextFuzz:
		return hintContextFuzz
	}
	return hintContextWelcome
}

// hintPool returns the hints for one interaction. An unknown context degrades
// to the onboarding pool instead of rendering nothing.
func hintPool(ctx string) []string {
	if pool := promptHintPools[ctx]; len(pool) > 0 {
		return pool
	}
	return promptHintPools[hintContextWelcome]
}

// firstHint is the deterministic starting hint for a context. Randomising here
// would make two independently constructed states render differently, which
// breaks frame comparisons; variety comes from advancing the pool.
func firstHint(ctx string) string {
	pool := hintPool(ctx)
	if len(pool) == 0 {
		return ""
	}
	return pool[0]
}

// nextHint advances the rotation deterministically within one context. A hint
// that is not in the pool starts at the top, so a context change or a
// hand-authored fixture can never stall the rotation.
func nextHint(ctx, current string) string {
	pool := hintPool(ctx)
	if len(pool) == 0 {
		return ""
	}
	for i, tip := range pool {
		if tip == current {
			return pool[(i+1)%len(pool)]
		}
	}
	return pool[0]
}

// nextHintFor advances the placeholder to the next hint for the current
// interaction. A changed authority starts at that pool's first hint unless the
// current hint already belongs to it, in which case the rotation continues.
func (s State) nextHintFor() string {
	return nextHint(hintContext(s), s.Tip)
}

// Panel geometry.
//
// The panel is deliberately full-width: it is the empty conversation surface,
// not a floating card, so it should occupy the same field as the transcript it
// replaces. Padding is the only inset; there is no width ceiling.
const (
	// identityPadX is the horizontal inset inside the frame. It is generous
	// enough that the border reads as a quiet margin rather than a box around
	// the copy.
	identityPadX = 3
	// identityModeLabelWidth aligns every mode description on one column. The
	// gap is intentionally wider than the longest label so the authority names
	// and their explanations read as two distinct columns.
	identityModeLabelWidth = 12
	// identityMinFrameWidth is the narrowest panel that can still draw a
	// frame around readable content. Below it the content wins.
	identityMinFrameWidth = 20
	// identityFallbackWidth is the composition width when no viewport has been
	// measured.
	identityFallbackWidth = 72
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
// blank rows inside the frame so a short viewport can still show the whole
// panel, including its modes, instead of losing the top of the card.
func renderIdentitySurface(f IdentityFacts, width int, compact bool) string {
	if width <= 0 {
		return strings.Join(identityBodyRows(f, identityFallbackWidth, compact), "\n")
	}
	if width < identityMinFrameWidth {
		return strings.Join(wrapIdentityLines(identityBodyRows(f, width, compact), width), "\n")
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
	lines := make([]string, 0, len(capabilityModes)+10)
	lines = append(lines, identityTopBorder(inner))
	if !compact {
		lines = append(lines, identityBlankRow(inner))
	}
	for _, row := range identityBodyRows(f, contentW, compact) {
		for _, wrapped := range wrapIdentityLine(row, contentW) {
			lines = append(lines, "│"+
				strings.Repeat(" ", identityPadX)+
				padCells(wrapped, contentW)+
				strings.Repeat(" ", identityPadX)+
				"│")
		}
	}
	if !compact {
		lines = append(lines, identityBlankRow(inner))
	}
	lines = append(lines, identityBottomBorder(inner))
	return strings.Join(lines, "\n")
}

// identityBodyRows composes the stack in reading order: greeting, subtitle,
// then the authority modes. Blank rows carry the vertical rhythm; compact omits
// only the panel's outer padding, so the stack itself never loses a section.
func identityBodyRows(f IdentityFacts, contentW int, compact bool) []string {
	rows := make([]string, 0, len(capabilityModes)+8)
	rows = append(rows, wrapIdentityText(identityGreetingLine(f), contentW, 0)...)
	rows = append(rows, wrapIdentityText(identitySubtitle, contentW, 0)...)
	if !compact {
		rows = append(rows, "")
	}
	modeW := identityModeWidth(contentW, compact)
	for _, m := range capabilityModes {
		row := padCells(m.name, modeW) + m.desc
		rows = append(rows, wrapIdentityText(row, contentW, modeW)...)
	}
	return rows
}

// identityModeWidth keeps the label column aligned while shrinking it enough
// to keep a description readable. The normal form only protects the first
// word; the compact form fits the whole description on one row, because a
// short viewport cannot spend rows on wraps.
func identityModeWidth(contentW int, compact bool) int {
	width := identityModeLabelWidth
	if contentW <= 0 {
		return width
	}
	for _, m := range capabilityModes {
		text := m.desc
		if !compact {
			if space := strings.IndexByte(text, ' '); space >= 0 {
				text = text[:space]
			}
		}
		if need := contentW - CellWidth(text); need < width {
			width = need
		}
	}
	if minimum := len("BUILD") + 1; width < minimum {
		width = minimum
	}
	if width < 1 {
		width = 1
	}
	return width
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

// identityTopBorder embeds the category legend in the panel's top rule. The
// legend starts on the same column as the content below, so the frame and the
// greeting share one left edge instead of the label sitting in its own groove.
func identityTopBorder(inner int) string {
	inset := identityPadX - 1
	if inset < 0 {
		inset = 0
	}
	title := strings.Repeat("─", inset) + " " + identityFrameLegend + " "
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
		switch {
		case len(rest) > len(prefix) && rest[len(prefix)] == ' ':
			// The width boundary already falls on a word boundary; keep the
			// whole prefix instead of backing up into its indentation.
		case strings.LastIndex(prefix, " ") > indent:
			cut = strings.LastIndex(prefix, " ")
		case indent > 0 && strings.LastIndex(prefix, " ") > 0:
			// The first content word does not fit after the hanging indent.
			// Break after the label instead of splitting the word.
			cut = strings.LastIndex(prefix, " ")
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

// identityHangingIndent is the description column for a wrapped mode row, zero
// for prose.
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
