package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A transcript prompt is attributed, not highlighted: the configured worker
// name and a dim glyph introduce the body on the terminal background. A
// background SGR sequence here would recreate the full-width strip the
// minimalist contract removes.
func TestUserPromptAttributionHasNoBackgroundFill(t *testing.T) {
	lines := userPromptLines("check this project", "andev0x", forcedPalette(), Layout{ContentWidth: 60})
	if len(lines) != 1 {
		t.Fatalf("prompt rows = %d, want one", len(lines))
	}
	if got, want := ansi.Strip(lines[0]), "andev0x › check this project"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	for _, sequence := range []string{"\x1b[48;", "\x1b[49m"} {
		if strings.Contains(lines[0], sequence) {
			t.Fatalf("prompt paints a background (%q): %q", sequence, lines[0])
		}
	}
}

// An unset project worker must still attribute the prompt without inventing an
// identity. The neutral label is the fallback, never a blank prefix.
func TestUserPromptAttributionFallsBackToUser(t *testing.T) {
	lines := userPromptLines("explain the parser", "", DarkPalette(), Layout{ContentWidth: 60})
	if len(lines) != 1 {
		t.Fatalf("prompt rows = %d, want one", len(lines))
	}
	if got := ansi.Strip(lines[0]); !strings.HasPrefix(got, "user › ") {
		t.Fatalf("prompt = %q, want the neutral user attribution", got)
	}
}

// The project worker name is metadata, not authored prose. Embedded styling
// must not override the semantic attribution accent.
func TestUserPromptAttributionStripsInjectedStyling(t *testing.T) {
	lines := userPromptLines("body", "\x1b[31mred\x1b[0m", forcedPalette(), Layout{ContentWidth: 60})
	if strings.Contains(lines[0], "\x1b[31m") {
		t.Fatalf("prompt kept injected styling: %q", lines[0])
	}
	if got := ansi.Strip(lines[0]); got != "red › body" {
		t.Fatalf("prompt = %q, want the plain worker name", got)
	}
}

// The attribution prefix owns only the first row. Continuation rows hang under
// the body, and every row stays inside the content budget so the layout's
// height and the terminal's soft-wrap agree.
func TestUserPromptWrapHangsUnderAttribution(t *testing.T) {
	const width = 32
	text := strings.Repeat("alpha beta gamma ", 6)
	lines := userPromptLines(text, "andev0x", forcedPalette(), Layout{ContentWidth: width})
	if len(lines) < 2 {
		t.Fatalf("prompt rows = %d, want a wrapped prompt", len(lines))
	}
	prefix := CellWidth("andev0x › ")
	indent := strings.Repeat(" ", prefix)
	for i, line := range lines {
		if got := CellWidth(line); got > width {
			t.Fatalf("row %d is %d cells, want at most %d: %q", i, got, width, ansi.Strip(line))
		}
		plain := ansi.Strip(line)
		if i == 0 {
			if !strings.HasPrefix(plain, "andev0x › ") {
				t.Fatalf("first row = %q, want the attribution", plain)
			}
			continue
		}
		if !strings.HasPrefix(plain, indent) {
			t.Fatalf("row %d = %q, want %q hanging under the body", i, plain, indent)
		}
	}
}

// The rendered transcript reads the attribution from runtime identity, so the
// configured project worker appears without any per-entry copy of the name.
func TestTranscriptPromptRendersConfiguredIdentity(t *testing.T) {
	state := New()
	state.Apply(TerminalResized{Width: 80, Height: 24})
	state.Apply(IdentityUpdated{User: "andev0x"})
	state.Apply(UserSubmitted{Text: "check this project"})

	out := ansi.Strip(Render(state))
	if !strings.Contains(out, "andev0x › check this project") {
		t.Fatalf("transcript lacks the configured prompt attribution:\n%s", out)
	}
}

// The live scrollback projection is the fullscreen projection. If the two
// paths ever diverge, a resumed prompt looks different from a fresh one.
func TestLiveUserPromptUsesSameAttribution(t *testing.T) {
	state := New()
	state.Status.User = "andev0x"
	state.Apply(TerminalResized{Width: 60, Height: 20})
	entry := Entry{Kind: EntryUser, UserText: "same projection"}
	pal := DarkPalette()
	layout := ComputeLayout(state)

	live := liveEntryLinesRaw(state, 0, entry, pal, layout)
	full := renderUserPromptBlock(entry.UserText, state.Status.User, pal, layout)
	if len(live) != len(full) {
		t.Fatalf("live rows = %d, fullscreen rows = %d", len(live), len(full))
	}
	for i := range live {
		if live[i] != full[i] {
			t.Fatalf("row %d: live %q != fullscreen %q", i, live[i], full[i])
		}
	}
}

func renderUserPromptBlock(text, user string, pal *Palette, layout Layout) []string {
	var sb strings.Builder
	renderUserBlock(&sb, text, user, pal, layout)
	return splitLines(sb.String())
}
