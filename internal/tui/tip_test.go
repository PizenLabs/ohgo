package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The composer's placeholder hint trails the caret: the caret stays at the
// input origin where typing lands, and the hint reads as an embedded prompt
// suggestion rather than a block of text that pushes the caret to the edge.
func TestComposerShowsHintAfterCaret(t *testing.T) {
	s := New()
	s.Tip = firstHint(hintContextWelcome)
	s.Apply(TerminalResized{Width: 120, Height: 24})

	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "▌ "+s.Tip) {
		t.Fatalf("empty composer does not place the hint after the caret %q:\n%s", s.Tip, out)
	}
}

// The hint is placeholder chrome, not content: typing one rune must remove it
// without changing the buffer.
func TestComposerHintDisappearsOnFirstKeystroke(t *testing.T) {
	s := New()
	s.Tip = firstHint(hintContextWelcome)
	s.Apply(TerminalResized{Width: 120, Height: 24})

	s.Apply(ComposerChanged{Text: "e", Cursor: 1})
	out := ansi.Strip(Render(s))
	if strings.Contains(out, s.Tip) {
		t.Fatalf("hint survived the first keystroke:\n%s", out)
	}
	if s.Input != "e" {
		t.Fatalf("render changed the input buffer to %q", s.Input)
	}
}

// The hint is contextual, not a mechanical passthrough: entering an authority
// selects a hint that authority can actually use.
func TestComposerHintFollowsAuthority(t *testing.T) {
	for _, tc := range []struct {
		workspace string
		context   string
	}{
		{"ASK", hintContextAsk},
		{"BUILD", hintContextBuild},
		{"FUZZ", hintContextFuzz},
	} {
		s := New()
		s.SessionActive = true
		s.Apply(WorkspaceUpdated{Workspace: tc.workspace, HasWorkspace: true})
		if !hintInPool(s.Tip, promptHintPools[tc.context]) {
			t.Errorf("%s hint %q is not in the %s pool", tc.workspace, s.Tip, tc.context)
		}
	}
}

// Each submitted prompt advances the hint within the active authority's pool,
// so the next empty composer teaches a different, relevant binding.
func TestComposerHintRotatesOnEachSubmit(t *testing.T) {
	s := New()
	s.Status.Workspace = "BUILD"
	s.SessionActive = true
	s.Tip = firstHint(hintContextBuild)
	s.Apply(TerminalResized{Width: 120, Height: 24})

	want := nextHint(hintContextBuild, s.Tip)
	s.Apply(UserSubmitted{Text: "first"})
	if s.Tip != want {
		t.Fatalf("after first submit hint = %q, want %q", s.Tip, want)
	}

	s.Apply(AssistantDone{})
	out := ansi.Strip(Render(s))
	if !strings.Contains(out, want) {
		t.Fatalf("the next empty composer does not show the rotated hint %q:\n%s", want, out)
	}

	next := nextHint(hintContextBuild, want)
	s.Apply(UserSubmitted{Text: "second"})
	if s.Tip != next {
		t.Fatalf("after second submit hint = %q, want %q", s.Tip, next)
	}
}

// Every context has a curated, non-empty pool: duplicates within a pool would
// overweight one binding, and a long entry would truncate against the caret.
func TestHintPoolsAreCurated(t *testing.T) {
	for _, ctx := range []string{hintContextWelcome, hintContextAsk, hintContextBuild, hintContextFuzz} {
		pool := promptHintPools[ctx]
		if len(pool) < 3 {
			t.Errorf("%s pool has %d hints, want at least 3 for variety", ctx, len(pool))
		}
		seen := make(map[string]bool, len(pool))
		for _, hint := range pool {
			if strings.TrimSpace(hint) == "" {
				t.Errorf("%s pool contains an empty entry", ctx)
			}
			if strings.ContainsAny(hint, "\n\r\t") {
				t.Errorf("%s hint %q contains a control character", ctx, hint)
			}
			if got := CellWidth(hint); got > 30 {
				t.Errorf("%s hint %q is %d cells, want <= 30 so the prompt stays on one row", ctx, hint, got)
			}
			if seen[hint] {
				t.Errorf("%s pool contains duplicate %q", ctx, hint)
			}
			seen[hint] = true
		}
	}
}

// The quickstart card is deliberately reduced to identity and authority; the
// composer is the only home for trigger hints.
func TestQuickstartLeavesHintsToTheComposer(t *testing.T) {
	out := RenderIdentitySurface(identityTestFacts(), 120)
	for _, duplicate := range []string{"/model", "/help", "@files"} {
		if strings.Contains(out, duplicate) {
			t.Fatalf("quickstart duplicated the composer shortcut %q:\n%s", duplicate, out)
		}
	}
	for _, pool := range promptHintPools {
		for _, hint := range pool {
			if strings.Contains(out, hint) {
				t.Fatalf("quickstart repeated the composer hint %q:\n%s", hint, out)
			}
		}
	}
}

func hintInPool(hint string, pool []string) bool {
	for _, entry := range pool {
		if entry == hint {
			return true
		}
	}
	return false
}
