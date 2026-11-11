package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The quickstart is where the hierarchy is most visible: title, mode identity,
// supporting descriptions, and taught affordances must each resolve to a
// different focus tier. A flat surface is the failure this test defends.
func TestQuickstartUsesThreeFocusTiers(t *testing.T) {
	forceTrueColor(t)
	pal := forcedPalette()
	chrome := ChromeFor(ThemeDark)
	modeLine := padCells("ASK", identityModeLabelWidth) + "Understand, inspect, reason"
	shortcutLine := padCells(triggerShortcuts[0][0], identityTriggerTokenWidth) + triggerShortcuts[0][1]
	lines := []string{
		identityGreetingLine(identityTestFacts()),
		identitySubtitle,
		modeLine,
		shortcutLine,
	}
	for _, line := range lines {
		if got := ansi.Strip(styleIdentityLine(line, pal, chrome)); got != line {
			t.Fatalf("stripped = %q, want the input cells unchanged %q", got, line)
		}
	}

	title := styleIdentityLine(lines[0], pal, chrome)
	if !strings.Contains(title, pal.Title.Render("OHGO")) {
		t.Errorf("title row = %q, want the brand in the High Focus title role", title)
	}
	if !strings.Contains(title, pal.Faint.Render("·")) {
		t.Errorf("title row = %q, want the brand/address separator in the Low Focus role", title)
	}
	if !strings.Contains(title, pal.Title.Render("  Good evening, Jack.")) {
		t.Errorf("title row = %q, want the greeting in the High Focus title role", title)
	}

	subtitle := styleIdentityLine(lines[1], pal, chrome)
	if !strings.Contains(subtitle, pal.Muted.Render(lines[1])) {
		t.Errorf("subtitle = %q, want the dimmed muted role", subtitle)
	}

	mode := styleIdentityLine(lines[2], pal, chrome)
	accent := ModeAccentFor(ThemeDark, "ASK")
	if !strings.Contains(mode, fg(accent).Bold(true).Render("ASK")) {
		t.Errorf("mode row = %q, want ASK in its bold signature accent %q", mode, accent)
	}
	if !strings.Contains(mode, pal.Secondary.Render("    Understand, inspect, reason")) {
		t.Errorf("mode row = %q, want the description in the Medium Focus role", mode)
	}
	if strings.Contains(mode, pal.Faint.Render("    Understand, inspect, reason")) {
		t.Errorf("mode row = %q, want the description above the Low Focus role", mode)
	}

	triggers := styleIdentityLine(lines[3], pal, chrome)
	if !strings.Contains(triggers, pal.Title.Render("/model")) {
		t.Errorf("trigger row = %q, want /model in the High Focus role", triggers)
	}
	if strings.Contains(triggers, pal.Title.Render("choose model")) {
		t.Errorf("trigger row = %q, want the hint below the High Focus role", triggers)
	}
}

// FUZZ must render in its soft-violet identity in both appearances. The
// quickstart is the first place a user meets the authority, so a magenta or
// unaccented row would teach the wrong signature.
func TestQuickstartRendersFuzzInSoftViolet(t *testing.T) {
	forceTrueColor(t)
	line := padCells("FUZZ", identityModeLabelWidth) + capabilityModes[2].desc
	for _, theme := range []string{ThemeDark, ThemeLight} {
		styled := styleIdentityLine(line, PaletteFor(theme), ChromeFor(theme))
		accent := ModeAccentFor(theme, "FUZZ")
		if accent != ModeAccentFuzz && accent != ModeAccentFuzzLight {
			t.Errorf("%s FUZZ accent = %q, want one of the signature violet pair", theme, accent)
		}
		if !strings.Contains(styled, fg(accent).Bold(true).Render("FUZZ")) {
			t.Errorf("%s FUZZ row did not use its violet accent:\n%q", theme, styled)
		}
		if strings.Contains(styled, fg(ModeAccentBuild).Bold(true).Render("FUZZ")) {
			t.Errorf("%s FUZZ row borrowed the BUILD blue", theme)
		}
	}
}

// The quickstart is one full-width stack: modes share a strict description
// column, the quick commands sit on a balanced two-column grid, and no inner
// divider fragments the surface.
func TestQuickstartAlignsFullWidthSections(t *testing.T) {
	const width = 120
	out := RenderIdentitySurface(identityTestFacts(), width)
	lines := strings.Split(out, "\n")

	if strings.Contains(out, "▸") {
		t.Errorf("quickstart kept a redundant arrow anchor:\n%s", out)
	}
	if strings.ContainsAny(out, "▄▀█") {
		t.Errorf("quickstart kept a block-glyph wordmark:\n%s", out)
	}
	for _, line := range lines {
		if got := CellWidth(line); got != width {
			t.Errorf("line = %d cells, want the full %d-cell panel: %q", got, width, line)
		}
		if strings.Count(line, "│") > 2 {
			t.Errorf("line kept an inner vertical divider: %q", line)
		}
	}

	modeStart := -1
	for _, m := range capabilityModes {
		row := -1
		for i, line := range lines {
			if strings.Contains(line, m.name+" ") && strings.Contains(line, m.desc) {
				row = i
				break
			}
		}
		if row < 0 {
			t.Fatalf("quickstart lacks the %s row:\n%s", m.name, out)
		}
		start := cellIndex(lines[row], m.name)
		desc := cellIndex(lines[row], m.desc)
		if got := desc - start; got != identityModeLabelWidth {
			t.Errorf("%s description starts %d cells after the label, want %d:\n%s",
				m.name, got, identityModeLabelWidth, lines[row])
		}
		if modeStart < 0 {
			modeStart = start
		}
		if start != modeStart {
			t.Errorf("%s label starts at %d, want the shared mode column %d", m.name, start, modeStart)
		}
	}

	commandRow := func(pair [2]string) int {
		for i, line := range lines {
			if strings.Contains(line, pair[0]) && strings.Contains(line, pair[1]) {
				return i
			}
		}
		return -1
	}
	modelRow, helpRow := commandRow(triggerShortcuts[0]), commandRow(triggerShortcuts[1])
	filesRow := commandRow(triggerShortcuts[2])
	tipRow := -1
	for i, line := range lines {
		if strings.Contains(line, identityTipLabel+" ") {
			tipRow = i
			break
		}
	}
	for name, row := range map[string]int{"/model": modelRow, "@files": filesRow, "/help": helpRow, identityTipLabel: tipRow} {
		if row < 0 {
			t.Fatalf("quickstart lacks command %q:\n%s", name, out)
		}
	}
	if modelRow != helpRow {
		t.Errorf("/model row %d and /help row %d, want one balanced command row", modelRow, helpRow)
	}
	if filesRow != tipRow {
		t.Errorf("@files row %d and Tip row %d, want one balanced tip row", filesRow, tipRow)
	}
	left := cellIndex(lines[modelRow], "/model")
	if got := cellIndex(lines[filesRow], "@files"); got != left {
		t.Errorf("@files starts at %d, want the shared left command column %d", got, left)
	}
	right := cellIndex(lines[helpRow], "/help")
	if got := cellIndex(lines[tipRow], identityTipLabel); got != right {
		t.Errorf("Tip starts at %d, want the shared right command column %d", got, right)
	}
	if right <= left {
		t.Errorf("command columns overlap: left %d, right %d", left, right)
	}

	foundRule := false
	for _, line := range lines {
		segs := strings.Split(line, "│")
		if len(segs) != 3 {
			continue
		}
		interior := strings.TrimSpace(segs[1])
		if interior == "" {
			continue
		}
		rule := true
		for _, r := range interior {
			if r != '─' {
				rule = false
				break
			}
		}
		if rule {
			foundRule = true
		}
	}
	if !foundRule {
		t.Errorf("quickstart has no full-width section rule:\n%s", out)
	}
}

// cellIndex is the terminal column of sub in line, not the byte offset: content
// may mix wide glyphs, and columns must compare in cells.
func cellIndex(line, sub string) int {
	idx := strings.Index(line, sub)
	if idx < 0 {
		return -1
	}
	return CellWidth(line[:idx])
}

// The palette itself must expose the three tiers, the amber thought pair, and
// the signature accents without a component reaching for a literal.
func TestPaletteExposesFocusTiersAndThoughtPair(t *testing.T) {
	for _, theme := range []string{ThemeDark, ThemeLight} {
		tok := DarkTokens()
		pal := DarkPalette()
		if theme == ThemeLight {
			tok = LightTokens()
			pal = LightPalette()
		}
		if got := colorName(pal.Title.GetForeground()); got != tok.TextTitle {
			t.Errorf("%s title = %q, want %q", theme, got, tok.TextTitle)
		}
		if !pal.Title.GetBold() {
			t.Errorf("%s title is not bold", theme)
		}
		if got := colorName(pal.Secondary.GetForeground()); got != tok.TextSecondary {
			t.Errorf("%s secondary = %q, want %q", theme, got, tok.TextSecondary)
		}
		if got := colorName(pal.Faint.GetForeground()); got != tok.TextFaint {
			t.Errorf("%s faint = %q, want %q", theme, got, tok.TextFaint)
		}
		if got := colorName(pal.Thought.GetForeground()); got != tok.ThoughtHeader {
			t.Errorf("%s thought header = %q, want %q", theme, got, tok.ThoughtHeader)
		}
		if got := colorName(pal.ThoughtBody.GetForeground()); got != tok.ThoughtBody {
			t.Errorf("%s thought body = %q, want %q", theme, got, tok.ThoughtBody)
		}
		if !pal.ThoughtBody.GetItalic() {
			t.Errorf("%s thought body is not italic", theme)
		}
	}
}

// The header brand is a single-line title, not a graphic: it must take the
// High Focus role and keep its measured cells so the rows below stay on the
// same left edge.
func TestQuickstartBrandIsSingleLineTitle(t *testing.T) {
	forceTrueColor(t)
	pal := forcedPalette()
	chrome := ChromeFor(ThemeDark)
	line := identityGreetingLine(identityTestFacts())
	styled := styleIdentityLine(line, pal, chrome)
	if !strings.Contains(styled, pal.Title.Render("OHGO")) {
		t.Fatalf("brand did not take the title role: %q", styled)
	}
	if got := ansi.Strip(styled); got != line {
		t.Fatalf("stripped brand = %q, want the cells unchanged %q", got, line)
	}
}
