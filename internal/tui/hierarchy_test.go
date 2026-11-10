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
		"OHGO   Good evening, Jack.",
		identitySubtitle,
		"ASK      Understand, inspect, reason",
		"/model  choose model    /help  commands",
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
	if !strings.Contains(title, pal.Title.Render("   Good evening, Jack.")) {
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
	if !strings.Contains(mode, pal.Secondary.Render("      Understand, inspect, reason")) {
		t.Errorf("mode row = %q, want the description in the Medium Focus role", mode)
	}
	if strings.Contains(mode, pal.Faint.Render("      Understand, inspect, reason")) {
		t.Errorf("mode row = %q, want the description above the Low Focus role", mode)
	}

	triggers := styleIdentityLine(lines[3], pal, chrome)
	for _, token := range []string{"/model", "/help"} {
		if !strings.Contains(triggers, pal.Title.Render(token)) {
			t.Errorf("trigger row = %q, want %q in the High Focus role", triggers, token)
		}
	}
	if !strings.Contains(triggers, pal.Faint.Render("  choose model    ")) {
		t.Errorf("trigger row = %q, want its hint in the Low Focus role", triggers)
	}
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
