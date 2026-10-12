package tui

import (
	"strings"
	"testing"
)

// The identity surface is a semantic composition, not hard-coded strings:
// the same facts render boxed two-column on wide terminals, boxed stacked
// on medium, and borderless on narrow — without overflow, clipping, or
// invented state.

func identityTestFacts() IdentityFacts {
	return IdentityFacts{
		User:      "Jack",
		Greeting:  "Good evening, Jack.",
		Model:     "openrouter/qwen3-coder",
		Workspace: "BUILD",
		Cwd:       "~/pizenlabs/ohgo",
		Git:       "main · clean",
		Ready:     "ready",
	}
}

func TestIdentitySurfaceKeepsSemanticsAcrossWidths(t *testing.T) {
	facts := identityTestFacts()
	for _, width := range []int{120, 85, 50} {
		out := RenderIdentitySurface(facts, width)
		for _, want := range []string{"OHGO", "Good evening, Jack.", "openrouter/qwen3-coder", "~/pizenlabs/ohgo", "main · clean", "ASK", "BUILD", "FUZZ", "/model", "/help"} {
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

func TestIdentitySurfaceNarrowDropsChromeNotContent(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 50)
	if strings.Contains(out, "┌") {
		t.Errorf("narrow surface kept a frame: %q", out)
	}
	for _, want := range []string{"ASK", "BUILD", "FUZZ"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow surface lost mode %q: %q", want, out)
		}
	}
}

func TestIdentitySurfaceShowsUnknownStateExplicitly(t *testing.T) {
	out := RenderIdentitySurface(IdentityFacts{}, 120)
	for _, want := range []string{"no model", "no workspace", "git unavailable"} {
		if !strings.Contains(out, want) {
			t.Errorf("surface hides unknown state, want %q:\n%s", want, out)
		}
	}
	// Unknown must never render as a healthy claim.
	if strings.Contains(out, "clean") {
		t.Errorf("surface claims clean with no git state: %q", out)
	}
}

func TestIdentitySurfaceNeverImpliesAPipeline(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	if strings.Contains(out, "ASK →") || strings.Contains(out, "→ BUILD") || strings.Contains(out, "ASK ->") {
		t.Errorf("surface implies a mode pipeline: %q", out)
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
	if facts.Git != "main · clean" {
		t.Errorf("git line = %q, want %q", facts.Git, "main · clean")
	}
}

func TestWelcomeRendersGreetingFromState(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(IdentityUpdated{User: "Jack", Greeting: "Good evening, Jack.", GitBranch: "main", GitStatus: "clean", Ready: "ready"})
	s.Apply(TerminalResized{Width: 100, Height: 30})
	out := Render(s)
	for _, want := range []string{"Good evening, Jack.", "groq/m", "main · clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("welcome lacks %q:\n%s", want, out)
		}
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
