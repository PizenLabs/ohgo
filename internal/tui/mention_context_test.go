package tui

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// mentionTestStyle is the distinct highlight a valid reference receives. It
// runs through a private ANSI256 renderer so the style always emits escapes:
// on a colorless test terminal the styling would otherwise collapse to the
// plain text and the assertions could not fail.
func mentionTestStyle() lipgloss.Style {
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.ANSI256)
	return renderer.NewStyle().Foreground(lipgloss.Color(ColorPrimary)).Bold(true)
}

// TestHighlightMentionsStylesOnlyReferenceTokens proves the composer highlight
// marks path references — including folders — while leaving an "@" inside a word
// or a bare "@" as prose.
func TestHighlightMentionsStylesOnlyReferenceTokens(t *testing.T) {
	style := mentionTestStyle()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "leading", in: "@tui/app.go check", want: style.Render("@tui/app.go") + " check"},
		{name: "middle", in: "see @tui/app.go now", want: "see " + style.Render("@tui/app.go") + " now"},
		{name: "trailing punctuation", in: "@main.go, then", want: style.Render("@main.go") + ", then"},
		{name: "folder", in: "@pkg/ util", want: style.Render("@pkg/") + " util"},
		{name: "email stays prose", in: "mail foo@bar.com", want: "mail foo@bar.com"},
		{name: "bare at stays prose", in: "@ + x", want: "@ + x"},
	}
	for _, tt := range tests {
		got, _ := highlightMentions(style, tt.in, -1)
		if got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestHighlightMentionsRemapsCursorPastEscapes proves the cursor still lands
// where the caret is drawn: styling inserts escape runes, and a cursor that did
// not account for them would drift from the character the user is editing.
func TestHighlightMentionsRemapsCursorPastEscapes(t *testing.T) {
	style := mentionTestStyle()
	text, cursor := highlightMentions(style, "see @a.go now", 9)
	marked := insertAt(text, cursor, "|")
	if !strings.Contains(marked, style.Render("@a.go")+"| now") {
		t.Fatalf("cursor = %d rendered %q, want it just after the styled reference", cursor, marked)
	}
}

// TestComposerStylesMentionReferences proves the composer render path actually
// applies the mention style, so the highlight is not a dead helper.
func TestComposerStylesMentionReferences(t *testing.T) {
	pal := forcedPalette()
	pal.Mention = mentionTestStyle()
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	text := "read @tui/app.go now"
	s.Apply(ComposerChanged{Text: text, Cursor: len([]rune(text))})
	out := renderComposer(s, pal, ComputeLayout(s))
	if !strings.Contains(out, pal.Mention.Render("@tui/app.go")) {
		t.Fatalf("composer output did not style the reference:\n%q", out)
	}
}

// TestParseMentionsDedupesAndSkipsFolders proves the turn pipeline reads each
// referenced file at most once and never tries to attach a directory.
func TestParseMentionsDedupesAndSkipsFolders(t *testing.T) {
	got := parseMentions("see @tui/app.go and @pkg/ then @tui/app.go, plus @main.go")
	want := []string{"tui/app.go", "main.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMentions = %v, want %v", got, want)
	}
}

// TestMentionContextReadsFilesAndWarnsOnFailures proves a resolvable reference
// contributes its bytes while an unreadable one warns explicitly instead of
// silently contributing empty context.
func TestMentionContextReadsFilesAndWarnsOnFailures(t *testing.T) {
	read := func(path string) (string, error) {
		if path == "tui/app.go" {
			return "package tui", nil
		}
		return "", errors.New("no such file")
	}
	text, warnings := mentionContext("see @tui/app.go and @missing.go", read)
	if !strings.Contains(text, "tui/app.go") || !strings.Contains(text, "package tui") {
		t.Fatalf("context = %q, want the path and its content", text)
	}
	if strings.Contains(text, "missing.go") {
		t.Errorf("context = %q, want no block for an unreadable reference", text)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "missing.go") {
		t.Fatalf("warnings = %v, want one naming the missing reference", warnings)
	}
}

// TestMentionContextNilReaderResolvesNothing proves the feature is off when the
// owner installs no reader: "@" stays a completion affordance and no context is
// invented.
func TestMentionContextNilReaderResolvesNothing(t *testing.T) {
	text, warnings := mentionContext("see @a.go", nil)
	if text != "" || len(warnings) != 0 {
		t.Fatalf("got (%q, %v), want nothing resolved with a nil reader", text, warnings)
	}
}

// TestRunTurnInjectsMentionedFileContent is the end-to-end contract: a prompt
// naming a file delivers that file's real bytes to the model through the system
// context, alongside the loop's own system prompt.
func TestRunTurnInjectsMentionedFileContent(t *testing.T) {
	var captured ai.Request
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		System: []ai.SystemPromptBlock{{Text: "base system prompt"}},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			captured = req
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetMentionReader(func(path string) (string, error) {
		if path != "tui/app.go" {
			return "", errors.New("no such file")
		}
		return "package tui\nconst sentinel = 1", nil
	})
	p.runTurn("explain @tui/app.go")

	if !systemContains(captured.System, "sentinel = 1") {
		t.Fatalf("system = %+v, want the referenced file content attached", captured.System)
	}
	if !systemContains(captured.System, "base system prompt") {
		t.Fatalf("system = %+v, want the base system prompt preserved", captured.System)
	}
}

// TestRunTurnWarnsOnUnreadableMention proves a bad reference becomes a visible
// warning rather than a silent omission.
func TestRunTurnWarnsOnUnreadableMention(t *testing.T) {
	p := NewProgram(scriptedLoop(t, "ok"), "test", 0)
	p.SetMentionReader(func(string) (string, error) { return "", errors.New("no such file") })
	p.runTurn("see @gone.go")

	var notices []string
	done := false
	timeout := time.After(2 * time.Second)
	for !done {
		select {
		case event := <-p.events:
			switch e := event.(type) {
			case Notice:
				notices = append(notices, e.Text)
			case AssistantDone:
				done = true
			}
		case <-timeout:
			t.Fatal("turn never completed")
		}
	}
	if !strings.Contains(strings.Join(notices, "\n"), "@gone.go") {
		t.Fatalf("notices = %v, want one naming the missing reference", notices)
	}
}

// TestRunTurnClearsMentionContextBetweenTurns proves per-turn context does not
// leak: a prompt with no reference must not inherit the previous turn's files.
func TestRunTurnClearsMentionContextBetweenTurns(t *testing.T) {
	var systems [][]ai.SystemPromptBlock
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			systems = append(systems, req.System)
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetMentionReader(func(string) (string, error) { return "SENTINEL CONTENT", nil })
	p.runTurn("read @a.go")
	p.runTurn("just chatting")

	if len(systems) != 2 {
		t.Fatalf("captured %d requests, want 2", len(systems))
	}
	if !systemContains(systems[0], "SENTINEL CONTENT") {
		t.Fatalf("first system = %+v, want the reference attached", systems[0])
	}
	if systemContains(systems[1], "SENTINEL CONTENT") {
		t.Fatalf("second system = %+v, want the previous turn's context cleared", systems[1])
	}
}

// systemContains reports whether any system block holds needle.
func systemContains(blocks []ai.SystemPromptBlock, needle string) bool {
	for _, block := range blocks {
		if strings.Contains(block.Text, needle) {
			return true
		}
	}
	return false
}
