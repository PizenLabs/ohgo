package tui

import (
	"strings"
	"testing"
)

// The identity surface is a semantic composition, not hard-coded strings:
// the same facts render boxed on wide and medium terminals and borderless on
// narrow — without overflow, clipping, or invented state.
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
	for _, width := range []int{120, 85, 50} {
		out := RenderIdentitySurface(facts, width)
		for _, want := range []string{"OHGO", "Good evening, Jack.", "ASK", "BUILD", "FUZZ", "/model", "/help", "@files", "$sup"} {
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

// The surface must not duplicate the top bar: model, workspace, git state,
// and readiness have one home, and it is the header. A duplicated fact is
// worse than a missing one because the two can disagree.
func TestIdentitySurfaceLeavesRuntimeMetadataToTheHeader(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	for _, duplicated := range []string{"model      ", "git        ", "workspace  ", "status     ", "no workspace", "git unavailable", "no model"} {
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

// The frame labels the surface as a quickstart on every build; the release
// is the top bar's fact, so the welcome box never brands a version.
func TestIdentitySurfaceFrameTitlesTheQuickstart(t *testing.T) {
	if out := RenderIdentitySurface(identityTestFacts(), 120); !strings.Contains(out, "─ Quickstart ─") {
		t.Errorf("wide surface lacks the quickstart title:\n%s", out)
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
