package tui

import (
	"strings"
	"testing"
)

// The identity surface is a semantic composition, not hard-coded strings:
// the same facts render as a full-width panel, folding their copy on narrow
// viewports without overflow, clipping, or invented state.
//
// It is intentionally a *quickstart*, not a status board: the top bar owns
// the runtime facts (model, workspace, cwd, git, readiness), so the surface
// carries only the greeting, the authority modes, and the trigger shortcuts.

func identityTestFacts() IdentityFacts {
	return IdentityFacts{
		User:     "Jack",
		Greeting: "Good evening, Jack.",
	}
}

func TestIdentitySurfaceKeepsSemanticsAcrossWidths(t *testing.T) {
	facts := identityTestFacts()
	for _, width := range []int{120, 85, 50, 30} {
		out := RenderIdentitySurface(facts, width)
		for _, want := range []string{"OHGO", "Good evening,", "Jack.", "ASK", "BUILD", "FUZZ", "/model", "/help", "@files", "$sup"} {
			if !strings.Contains(out, want) {
				t.Errorf("width %d: surface lacks %q:\n%s", width, want, out)
			}
		}
		for i, line := range strings.Split(out, "\n") {
			if CellWidth(line) > width {
				t.Errorf("width %d line %d = %d cells, want <= %d: %q", width, i, CellWidth(line), width, line)
			}
		}
		if strings.Contains(out, "\x1b[") {
			t.Errorf("width %d: raw escape sequences in output", width)
		}
	}
}

func TestIdentitySurfaceUsesBoxDrawingWithoutEmoji(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	for _, want := range []string{"┌", "┐", "└", "┘", "│", "─"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide surface lacks box character %q", want)
		}
	}
	if strings.ContainsAny(out, "💡🚀✨🤖") {
		t.Errorf("surface uses emoji: %q", out)
	}
}

func TestIdentitySurfaceNarrowKeepsFrameAndContent(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 43)
	if !strings.Contains(out, "┌") {
		t.Errorf("narrow surface lost its frame: %q", out)
	}
	for _, want := range []string{"ASK", "BUILD", "FUZZ"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow surface lost mode %q: %q", want, out)
		}
	}
}

// The surface must not duplicate the top bar: model, workspace, git state,
// and readiness have one home, and it is the header. A duplicated fact is
// worse than a missing one because the two can disagree.
func TestIdentitySurfaceLeavesRuntimeMetadataToTheHeader(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	for _, duplicated := range []string{"git        ", "workspace  ", "status     ", "no workspace", "git unavailable", "no model"} {
		if strings.Contains(out, duplicated) {
			t.Errorf("surface duplicated top-bar metadata %q:\n%s", duplicated, out)
		}
	}
}

func TestIdentitySurfaceNeverImpliesAPipeline(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	if strings.Contains(out, "ASK →") || strings.Contains(out, "→ BUILD") || strings.Contains(out, "ASK ->") {
		t.Errorf("surface implies a mode pipeline: %q", out)
	}
}

// The wide composition groups every capability mode in one stack. A prior
// 2x2-grid layout duplicated BUILD (badge and description), so the welcome
// box read as if there were two BUILD authorities.
func TestIdentitySurfaceWideShowsEachModeOnce(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	for mode, desc := range map[string]string{
		"ASK":   "Understand, inspect, reason",
		"BUILD": "Authorized changes",
		"FUZZ":  "$sup · fast assistance",
	} {
		if got := strings.Count(out, mode); got != 1 {
			t.Errorf("wide surface shows %q %d times, want once:\n%s", mode, got, out)
		}
		if got := strings.Count(out, desc); got != 1 {
			t.Errorf("wide surface shows %q %d times, want once:\n%s", desc, got, out)
		}
	}
}

// The quickstart is a full-width panel, not a floating micro-box: the frame
// spans the viewport, the modes keep their own left-aligned column, and no
// inner vertical divider or block mark fragments the surface.
func TestIdentitySurfaceSpansTheFullWidth(t *testing.T) {
	const width = 120
	out := RenderIdentitySurface(identityTestFacts(), width)
	lines := strings.Split(out, "\n")
	for _, want := range []string{"┌", "└", "Quickstart", "OHGO"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel lacks %q:\n%s", want, out)
		}
	}
	for i, line := range lines {
		if got := CellWidth(line); got != width {
			t.Errorf("line %d = %d cells, want the full %d-cell panel: %q", i, got, width, line)
		}
		if strings.Count(line, "│") > 2 {
			t.Errorf("line %d kept an inner vertical divider: %q", i, line)
		}
	}
	if strings.ContainsAny(out, "▄▀█") {
		t.Errorf("panel kept a block-glyph wordmark:\n%s", out)
	}
	for _, line := range lines {
		if strings.Contains(line, "ASK") && strings.Contains(line, "/model") {
			t.Fatalf("mode and command share one row: %q", line)
		}
	}
}

// The panel must fit every panel width without an ellipsis or a broken border.
// Wrapping is the fallback; clipping and overflow are not.
func TestIdentitySurfaceWidthNeverOverflows(t *testing.T) {
	for _, width := range []int{160, 120, 100, 80, 60, 50, 44, 40, 30, 20} {
		out := RenderIdentitySurface(identityTestFacts(), width)
		for i, line := range strings.Split(out, "\n") {
			if got := CellWidth(line); got > width {
				t.Fatalf("width %d line %d = %d cells, want <= %d:\n%s", width, i, got, width, out)
			}
		}
		if strings.Contains(out, "…") {
			t.Fatalf("width %d clipped content instead of wrapping:\n%s", width, out)
		}
	}
}

// The frame labels the surface as a quickstart on every build; the release
// is the top bar's fact, so the welcome box never brands a version.
func TestIdentitySurfaceFrameTitlesTheQuickstart(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	for _, want := range []string{"OHGO", identityFrameLegend} {
		if !strings.Contains(out, want) {
			t.Errorf("wide surface lacks %q in its frame:\n%s", want, out)
		}
	}
}

// FactsFromStatus carries the ephemeral greeting facts and nothing the top
// bar already owns.
func TestFactsFromStatusCarriesGreeting(t *testing.T) {
	s := New()
	s.Status.User = "Jack"
	s.Status.Greeting = "Good evening, Jack."
	facts := FactsFromStatus(s)
	if facts.User != "Jack" || facts.Greeting != "Good evening, Jack." {
		t.Errorf("facts = %+v, want the user and greeting", facts)
	}
}

// The state machine owns the facts; rendering only reads them.

func TestIdentityUpdatedAppliesWithoutTouchingTranscript(t *testing.T) {
	s := New()
	s.Apply(IdentityUpdated{User: "Jack", Greeting: "Good evening, Jack.", GitBranch: "main", GitStatus: "clean", Ready: "ready"})
	if s.Status.User != "Jack" || s.Status.Greeting != "Good evening, Jack." {
		t.Errorf("identity not applied: %+v", s.Status)
	}
	if s.Status.GitBranch != "main" || s.Status.GitStatus != "clean" {
		t.Errorf("git not applied: %+v", s.Status)
	}
	if len(s.Entries) != 0 {
		t.Errorf("identity leaked %d entries into the transcript", len(s.Entries))
	}
	facts := FactsFromStatus(s)
	if facts.Greeting != "Good evening, Jack." {
		t.Errorf("greeting = %q, want %q", facts.Greeting, "Good evening, Jack.")
	}
}

func TestWelcomeRendersGreetingFromState(t *testing.T) {
	s := New()
	s.Apply(IdentityUpdated{User: "Jack", Greeting: "Good evening, Jack.", GitBranch: "main", GitStatus: "clean", Ready: "ready"})
	s.Apply(TerminalResized{Width: 100, Height: 30})
	out := renderWelcome(s, DarkPalette(), ComputeLayout(s))
	if !strings.Contains(out, "Good evening, Jack.") {
		t.Errorf("welcome lost the greeting:\n%s", out)
	}
	if strings.Contains(out, "main · clean") {
		t.Errorf("welcome duplicated the top-bar git state:\n%s", out)
	}
}

func TestWelcomeCollapsesOnceConversationStarts(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(IdentityUpdated{User: "Jack", Greeting: "Good evening, Jack."})
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "hello"})
	if out := Render(s); strings.Contains(out, "agent runtime for real codebases") {
		t.Errorf("identity surface survived a real prompt")
	}
}

// A split pane narrower than the card must fold the copy, not clip it: every
// word survives and no row grows past the pane.
func TestIdentitySurfaceNarrowWrapsInsteadOfClipping(t *testing.T) {
	const width = 30
	out := RenderIdentitySurface(identityTestFacts(), width)
	for _, want := range []string{"Understand,", "reason", "Authorized", "codebases"} {
		if !strings.Contains(out, want) {
			t.Errorf("wrapped surface lost %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "…") {
		t.Errorf("wrapped surface clipped content:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := CellWidth(line); got > width {
			t.Errorf("line %d = %d cells, want <= %d: %q", i, got, width, line)
		}
	}
}
