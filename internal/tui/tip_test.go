package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The bottom grid ends in a Tip slot whose copy is chosen from the curated
// pool. A provided tip must reach the rendered panel, and the FUZZ signature
// must keep its exact original description beside it.
func TestQuickstartRendersProvidedTip(t *testing.T) {
	facts := identityTestFacts()
	facts.Tip = "Ctrl+S opens support"
	out := RenderIdentitySurface(facts, 120)
	if !strings.Contains(out, identityTipLabel) {
		t.Fatalf("quickstart lacks the Tip label:\n%s", out)
	}
	if !strings.Contains(out, facts.Tip) {
		t.Fatalf("quickstart lacks the provided tip %q:\n%s", facts.Tip, out)
	}
	if got := strings.Count(out, "$sup · fast assistance"); got != 1 {
		t.Fatalf("FUZZ description appears %d times, want its exact original form once:\n%s", got, out)
	}
}

// The Tip is a warm micro-annotation, not another command: its label takes the
// amber thought accent while its body and the command hints stay low focus.
func TestQuickstartTipLabelUsesWarmAccentAndQuietBody(t *testing.T) {
	forceTrueColor(t)
	pal := forcedPalette()
	chrome := ChromeFor(ThemeDark)
	const tip = "Ctrl+O expands activity"
	line := padCells(triggerShortcuts[2][0], identityTriggerTokenWidth) + triggerShortcuts[2][1] +
		strings.Repeat(" ", identityCommandGap) +
		padCells(identityTipLabel, identityTriggerTokenWidth) + tip

	styled := styleIdentityLine(line, pal, chrome)
	if !strings.Contains(styled, pal.Thought.Render(identityTipLabel)) {
		t.Fatalf("Tip label did not take the warm reasoning accent: %q", styled)
	}
	if !strings.Contains(styled, pal.Title.Render(triggerShortcuts[2][0])) {
		t.Fatalf("command token did not take the High Focus role: %q", styled)
	}
	if !strings.Contains(styled, pal.Faint.Render(strings.Repeat(" ", identityTriggerTokenWidth-len(identityTipLabel))+tip)) {
		t.Fatalf("tip body did not stay in the low-focus role: %q", styled)
	}
	if strings.Contains(styled, pal.Title.Render(identityTipLabel)) {
		t.Fatalf("Tip label borrowed a command token's role: %q", styled)
	}
	if got := ansi.Strip(styled); got != line {
		t.Fatalf("stripped = %q, want the measured cells unchanged %q", got, line)
	}
}

// Every tip must be short, unique, and selectable by GetRandomTip. A duplicate
// would silently overweight one binding; a long entry would fold the grid into
// a paragraph on a split pane.
func TestQuickstartTipPoolIsCurated(t *testing.T) {
	if len(quickstartTips) == 0 {
		t.Fatal("tip pool is empty")
	}
	seen := make(map[string]bool, len(quickstartTips))
	for _, tip := range quickstartTips {
		if strings.TrimSpace(tip) == "" {
			t.Error("tip pool contains an empty entry")
		}
		if strings.ContainsAny(tip, "\n\r\t") {
			t.Errorf("tip %q contains a control character", tip)
		}
		if got := CellWidth(tip); got > 30 {
			t.Errorf("tip %q is %d cells, want <= 30 so the grid stays balanced", tip, got)
		}
		if seen[tip] {
			t.Errorf("duplicate tip %q", tip)
		}
		seen[tip] = true
	}
	for i := 0; i < 32; i++ {
		if got := GetRandomTip(); !seen[got] {
			t.Fatalf("GetRandomTip returned %q, not a pool entry", got)
		}
	}
}

// The tip is a project fact, not a per-frame roll. Resizing, redrawing, an
// authority switch, or clearing the transcript must not shuffle it.
func TestQuickstartTipStaysPinnedToProject(t *testing.T) {
	s := New()
	s.Apply(WorkspaceUpdated{Cwd: "/work/alpha", HasCwd: true})
	pinned := s.Tip
	if pinned == "" {
		t.Fatal("entering a project left the tip empty")
	}
	if got := FactsFromStatus(s).Tip; got != pinned {
		t.Fatalf("first render tip = %q, want the project tip %q", got, pinned)
	}
	for i := 0; i < 8; i++ {
		s.Apply(TerminalResized{Width: 90 + i, Height: 24})
		if got := FactsFromStatus(s).Tip; got != pinned {
			t.Fatalf("resize %d changed the tip to %q, want %q", i, got, pinned)
		}
	}
	s.Apply(WorkspaceUpdated{Cwd: "/work/alpha", HasCwd: true, Workspace: "BUILD", HasWorkspace: true})
	s.Apply(Cleared{})
	if got := FactsFromStatus(s).Tip; got != pinned {
		t.Fatalf("same-project update changed the tip to %q, want %q", got, pinned)
	}
}

// A different directory is a new project: the tip re-rolls exactly once when
// the project is entered, never again while that project stays open.
func TestEnteringProjectRerollsTipOnce(t *testing.T) {
	s := New()
	picks := 0
	choose := func() string {
		picks++
		return fmt.Sprintf("tip-%d", picks)
	}
	s.enterProjectWith("/work/alpha", choose)
	if picks != 0 {
		t.Fatalf("first project re-rolled the tip %d times, want the tip chosen at state creation", picks)
	}
	s.enterProjectWith("/work/beta", choose)
	if picks != 1 || s.Tip != "tip-1" {
		t.Fatalf("new project picks = %d, tip = %q, want one pick of tip-1", picks, s.Tip)
	}
	s.enterProjectWith("/work/beta", choose)
	if picks != 1 {
		t.Fatalf("staying in a project re-rolled the tip %d times", picks)
	}
	s.enterProjectWith("/work/gamma", choose)
	if picks != 2 || s.Tip != "tip-2" {
		t.Fatalf("second new project picks = %d, tip = %q, want tip-2", picks, s.Tip)
	}
}
