package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Prose must resolve to the calibrated body role rather than inheriting the
// terminal's own foreground. A flat terminal default is the failure this
// contract names.
func TestMarkdownBodyUsesPrimaryRole(t *testing.T) {
	pal := forcedPalette()
	out := RenderMarkdownWrapped("The retry helper keeps the cause.", pal, 40)
	if !strings.Contains(out, pal.Assistant.Render("retry")) {
		t.Fatalf("body did not use the primary text role: %q", out)
	}
	if got := ansi.Strip(out); got != "The retry helper keeps the cause." {
		t.Fatalf("stripped body = %q, want the source text unchanged", got)
	}
}

// A bold lead-in is a structural claim, not ordinary emphasis: it uses the
// warm emphasis accent so "Project Analysis:" outranks the explanation that
// follows on the same line without becoming a second heading.
func TestMarkdownBoldPrefixUsesEmphasisRole(t *testing.T) {
	pal := forcedPalette()
	out := RenderMarkdownWrapped("**Project Analysis:** the retry path is sound.", pal, 60)
	if !strings.Contains(out, pal.Strong.Render("Project")) ||
		!strings.Contains(out, pal.Strong.Render("Analysis:")) {
		t.Fatalf("bold prefix did not use the emphasis role: %q", out)
	}
	if !strings.Contains(out, pal.Assistant.Render("explanation")) &&
		!strings.Contains(out, pal.Assistant.Render("path")) {
		t.Fatalf("follow text did not use the body role: %q", out)
	}
	if pal.Strong.Render("Project") == pal.Title.Render("Project") {
		t.Fatalf("emphasis collapsed into the chrome title role")
	}
}

// Inline code is a technical artifact, not a fenced block: it takes the pill
// role, and it must not borrow the full block surface.
func TestMarkdownInlineCodeUsesPillRole(t *testing.T) {
	pal := forcedPalette()
	out := RenderMarkdownWrapped("edit `src/main.go` now", pal, 40)
	if !strings.Contains(out, pal.InlineCode.Render("src/main.go")) {
		t.Fatalf("inline code did not use the pill role: %q", out)
	}
	if strings.Contains(out, pal.Code.Render("src/main.go")) {
		t.Fatalf("inline code borrowed the fenced-block surface: %q", out)
	}
}

// The bullet glyph carries the secondary brand accent while the item text
// stays body copy, so a list reads as structure rather than a coloured block.
func TestMarkdownBulletUsesAccentAndBodyText(t *testing.T) {
	pal := forcedPalette()
	out := RenderMarkdownWrapped("- inspect the retry path", pal, 40)
	if !strings.Contains(out, pal.Bullet.Render("•")) {
		t.Fatalf("bullet glyph did not use its accent role: %q", out)
	}
	if !strings.Contains(out, pal.Assistant.Render("inspect")) {
		t.Fatalf("list item did not use the body role: %q", out)
	}
}

// The quickstart card's legend is its category header: the word takes the
// structure accent while the surrounding rule stays a separator.
func TestQuickstartLegendUsesStructureAccent(t *testing.T) {
	pal := forcedPalette()
	chrome := ChromeFor(ThemeDark)
	line := "┌─ Quickstart " + strings.Repeat("─", 8) + "┐"
	styled := styleIdentityLine(line, pal, chrome)
	if !strings.Contains(styled, pal.Heading.Render(identityFrameLegend)) {
		t.Fatalf("legend did not use the structure accent: %q", styled)
	}
	if !strings.Contains(styled, pal.Separator.Render("┌─ ")) {
		t.Fatalf("legend rule did not stay in the separator role: %q", styled)
	}
	if got := ansi.Strip(styled); got != line {
		t.Fatalf("stripped legend = %q, want the cells unchanged", got)
	}
}
