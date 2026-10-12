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

// The welcome box carries no inner split rules: modes stay grouped in one
// stack, and the outer frame is the only box drawing. An inner ─┬─ divider
// or a third │ inside a row would mean the content split in two again.
func TestIdentitySurfaceWideHasNoInnerSplit(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	if strings.Contains(out, "┬") {
		t.Errorf("wide surface kept an inner column divider:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if strings.Count(line, "│") > 2 {
			t.Errorf("wide surface line %d splits content in two: %q", i, line)
		}
	}
}

// The welcome frame titles the release beside the brand; an unknown release
// keeps the bare frame with no invented version.
func TestIdentitySurfaceFrameTitlesTheRelease(t *testing.T) {
	facts := identityTestFacts()
	facts.Version = "v1.0.0"
	if out := RenderIdentitySurface(facts, 120); !strings.Contains(out, "─ ohgo v1.0.0 ─") {
		t.Errorf("wide surface lacks the release title:\n%s", out)
	}
	if out := RenderIdentitySurface(identityTestFacts(), 120); !strings.Contains(out, "┌─ ohgo ─") {
		t.Errorf("bare surface lost its frame title:\n%s", out)
	}
}

// FactsFromStatus normalizes the release once, so the header and the frame
// cannot spell it differently.
func TestFactsFromStatusCarriesVersion(t *testing.T) {
	s := New()
	s.Status.Version = "1.0.0"
	if got := FactsFromStatus(s).Version; got != "v1.0.0" {
		t.Errorf("facts version = %q, want %q", got, "v1.0.0")
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
