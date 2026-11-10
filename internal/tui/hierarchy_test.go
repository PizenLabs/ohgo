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
	lines := []string{
		identityIndent + "OHGO  ·  Good evening, Jack.",
		identityIndent + identitySubtitle,
		identityIndent + identityModeGlyph + " " + padCells("ASK", identityModeLabelWidth) + "Understand, inspect, reason",
		identityIndent + identityShortcutLine(triggerShortcuts[0]),
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
	if !strings.Contains(mode, pal.Faint.Render(identityIndent+identityModeGlyph+" ")) {
		t.Errorf("mode row = %q, want the indent and alignment glyph in the Low Focus role", mode)
	}
	if !strings.Contains(mode, pal.Secondary.Render("    Understand, inspect, reason")) {
		t.Errorf("mode row = %q, want the description in the Medium Focus role", mode)
	}
	if strings.Contains(mode, pal.Faint.Render("    Understand, inspect, reason")) {
		t.Errorf("mode row = %q, want the description above the Low Focus role", mode)
	}

	triggers := styleIdentityLine(lines[3], pal, chrome)
	for _, token := range []string{"/model", "/help"} {
		if !strings.Contains(triggers, pal.Title.Render(token)) {
			t.Errorf("trigger row = %q, want %q in the High Focus role", triggers, token)
		}
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
	line := identityIndent + identityModeGlyph + " " + padCells("FUZZ", identityModeLabelWidth) + "$sup · fast assistance"
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

// The quickstart is a composed card, not a dense list: mode rows carry an
// alignment anchor, sections are separated by breathing rows, and a low-focus
// rule introduces the shortcut grid. This pins the structural polish the card
// promises without asserting a single terminal width.
func TestQuickstartGroupsSectionsWithBreathingAndRule(t *testing.T) {
	const width = 120
	out := RenderIdentitySurface(identityTestFacts(), width)
	lines := strings.Split(out, "\n")

	for _, want := range []string{"▸ ASK", "▸ BUILD", "▸ FUZZ"} {
		if !strings.Contains(out, want) {
			t.Errorf("quickstart lacks the mode anchor %q:\n%s", want, out)
		}
	}

	rule := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "│") && strings.Contains(trimmed, "───") &&
			!strings.ContainsAny(trimmed, "┌└┐┘") {
			rule = i
			break
		}
	}
	if rule < 0 {
		t.Fatalf("quickstart has no section rule:\n%s", out)
	}
	if !blankBoxRow(lines[rule-1]) || !blankBoxRow(lines[rule+1]) {
		t.Errorf("section rule is not surrounded by breathing rows:\n%s", out)
	}

	modelRow, filesRow := -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "/model"):
			modelRow = i
		case strings.Contains(line, "@files"):
			filesRow = i
		}
	}
	if modelRow < 0 || filesRow < 0 {
		t.Fatalf("quickstart lacks the shortcut rows:\n%s", out)
	}
	if got, want := strings.Index(lines[modelRow], "/help"), strings.Index(lines[filesRow], "$sup"); got != want {
		t.Errorf("shortcut columns are not aligned: /help at %d, $sup at %d", got, want)
	}
}

// blankBoxRow reports a frame row whose interior is empty.
func blankBoxRow(line string) bool {
	segs := strings.Split(line, "│")
	return len(segs) == 3 && strings.TrimSpace(segs[1]) == ""
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
