package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Mouse wheel scrolling folds into the same ScrollMoved contract as pgup/pgdn:
// scrolling up must clear sticky-follow, scrolling back to the bottom must
// restore it, and the wheel must never need a viewport component to work.

func wheelUp() tea.MouseMsg {
	return tea.MouseMsg{Button: tea.MouseButtonWheelUp}
}

func wheelDown() tea.MouseMsg {
	return tea.MouseMsg{Button: tea.MouseButtonWheelDown}
}

func TestMouseWheelUpScrollsTranscriptAndClearsFollow(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	p.Update(wheelUp())
	if p.state.Scroll != mouseScrollLines || p.state.Follow {
		t.Errorf("Scroll = %d Follow = %v, want %d and unfollowed", p.state.Scroll, p.state.Follow, mouseScrollLines)
	}
}

func TestMouseWheelDownReengagesFollowAtBottom(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	p.Update(wheelUp())
	p.Update(wheelDown())
	if p.state.Scroll != 0 || !p.state.Follow {
		t.Errorf("Scroll = %d Follow = %v, want the live edge back", p.state.Scroll, p.state.Follow)
	}
}

func TestMouseWheelMovesPickerWhenOneIsOpen(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetModelLister(func() []string { return []string{"a/1", "b/2", "c/3"} })
	Command{Name: "model"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("no picker opened")
	}
	before := p.state.Picker.Cursor
	p.Update(wheelDown())
	if p.state.Picker.Cursor == before {
		t.Error("wheel down over a picker left the highlight unmoved")
	}
	if p.state.Scroll != 0 {
		t.Errorf("Scroll = %d, want the transcript untouched while picking", p.state.Scroll)
	}
}

func TestMouseOtherButtonsLeaveStateAlone(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft})
	if p.state.Scroll != 0 || !p.state.Follow {
		t.Errorf("Scroll = %d Follow = %v, want clicks ignored", p.state.Scroll, p.state.Follow)
	}
}

func TestEndKeyReengagesFollowOnlyWhenScrolledUp(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	p.state.Apply(ComposerChanged{Text: "ab", Cursor: 1})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnd})
	if p.state.Input != "ab" || p.state.Cursor != 2 {
		t.Errorf("input = %q cursor = %d, want end-of-line when following", p.state.Input, p.state.Cursor)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyPgUp})
	if p.state.Follow {
		t.Fatal("pgup left follow engaged: the end-key case cannot be reached")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnd})
	if p.state.Scroll != 0 || !p.state.Follow {
		t.Errorf("Scroll = %d Follow = %v, want the live edge back", p.state.Scroll, p.state.Follow)
	}
}

// Header hierarchy is a colour contract: each level must resolve to its own
// forest step in both themes so depth reads without parsing markers.

func TestHeadingLevelsUseDistinctThemeColors(t *testing.T) {
	for _, theme := range []string{ThemeDark, ThemeLight} {
		pal := PaletteFor(theme)
		got := map[string]string{
			"H1": colorName(pal.Heading.GetForeground()),
			"H2": colorName(pal.Heading2.GetForeground()),
			"H3": colorName(pal.Heading3.GetForeground()),
		}
		for a, ca := range got {
			for b, cb := range got {
				if a < b && ca == cb {
					t.Errorf("theme %s: %s and %s share foreground %q", theme, a, b, ca)
				}
			}
		}
	}
}

// colorName renders a lipgloss colour for comparison: TerminalColor is an
// interface, and %v of the dynamic value names the token behind it.
func colorName(c lipgloss.TerminalColor) string {
	return fmt.Sprintf("%v", c)
}

// Lexical highlighting must colour without changing cells: stripping the
// output returns the input, and no highlight ever introduces a newline that
// would corrupt the wrapping done on the plain text.

func TestCodeHighlightKeepsTextAndAddsColor(t *testing.T) {
	// Colour assertions need the forced ANSI256 palette: test terminals
	// degrade lipgloss to plain text by design, which would hide any
	// collapse behind a colorless renderer.
	pal := forcedPalette()
	line := `func main() { // serve`
	out := highlightCode(line, "go", pal)
	if ansi.Strip(out) != line {
		t.Errorf("stripped = %q, want the input cells unchanged", ansi.Strip(out))
	}
	if out == pal.Code.Render(line) {
		t.Error("known language rendered as plain code: no token was highlighted")
	}
	if strings.Contains(out, "\n") {
		t.Errorf("highlight introduced a newline: %q", out)
	}
}

func TestCodeHighlightFallsBackForUnknownLanguage(t *testing.T) {
	pal := forcedPalette()
	line := "some $exotic { syntax"
	if out := highlightCode(line, "klingon", pal); out != pal.Code.Render(line) {
		t.Errorf("unknown language = %q, want the plain code block", out)
	}
	if out := highlightCode(line, "", pal); out != pal.Code.Render(line) {
		t.Errorf("missing language = %q, want the plain code block", out)
	}
}

func TestCodeHighlightCommentAndStringSurviveStripped(t *testing.T) {
	pal := DarkPalette()
	for lang, line := range map[string]string{
		"go":     `x := "hi" // done`,
		"python": `x = 'hi' # done`,
		"sh":     `echo "hi" # done`,
	} {
		if stripped := ansi.Strip(highlightCode(line, lang, pal)); stripped != line {
			t.Errorf("%s: stripped = %q, want %q", lang, stripped, line)
		}
	}
}

func TestFencedCodeWithLanguageRendersBodyWithoutLabel(t *testing.T) {
	out := RenderMarkdown("```go\nfunc main() {}\n```", DarkPalette())
	if strings.Contains(out, "```") || strings.Contains(ansi.Strip(out), "go\n") {
		t.Errorf("fence = %q, want no delimiter or language label row", out)
	}
	if !strings.Contains(ansi.Strip(out), "func main() {}") {
		t.Errorf("fence = %q, want the body kept", out)
	}
}

// Sequential reveal splits a turn into bounded, ordered chunks whose
// concatenation is the turn itself: progressive disclosure with no timers
// and no blocked loop.

func TestSplitAssistantChunksRoundTrip(t *testing.T) {
	if chunks := splitAssistantChunks(""); chunks != nil {
		t.Errorf("empty = %v, want nil so nothing publishes", chunks)
	}
	short := "hello"
	chunks := splitAssistantChunks(short)
	if len(chunks) != 1 || chunks[0] != short {
		t.Errorf("short = %v, want one event, not pacing overhead", chunks)
	}
	long := strings.Repeat("a quick brown fox jumps over the lazy dog. ", 200)
	chunks = splitAssistantChunks(long)
	if strings.Join(chunks, "") != long {
		t.Error("chunks do not reassemble to the turn text")
	}
	for i, chunk := range chunks {
		if len([]rune(chunk)) > assistantChunkRunes {
			t.Errorf("chunk %d holds %d runes, want at most %d", i, len([]rune(chunk)), assistantChunkRunes)
		}
	}
	if len(chunks) < 2 {
		t.Errorf("long text produced %d chunk(s), want progressive reveal", len(chunks))
	}
}

func TestLongTurnReachesTranscriptInOrder(t *testing.T) {
	want := strings.Repeat("streaming chunk reveal keeps long answers readable. ", 100)
	p := NewProgram(scriptedLoop(t, want), "test", 0)
	p.runTurn("go")
	var got strings.Builder
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			switch e := event.(type) {
			case AssistantDelta:
				got.WriteString(e.Text)
			case AssistantDone:
				if got.String() != want {
					t.Fatalf("transcript holds %d runes, want the %d-rune turn in order", len([]rune(got.String())), len([]rune(want)))
				}
				return
			}
		case <-timeout:
			t.Fatal("the chunked turn never completed")
		}
	}
}

// A fenced block is one uniform surface: every body line — including an
// empty one — fills the wrap width and carries the code background, so the
// block never reads as patchy per-token backgrounds ending mid-row.
func TestFencedCodeBodyFillsTheBlockWidth(t *testing.T) {
	pal := forcedPalette()
	out := RenderMarkdownWrapped("```go\nx := 1\n\nlonger line here\n```", pal, 20)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("fence = %q, want exactly the 3 body rows", out)
	}
	for _, line := range lines {
		if CellWidth(line) != 20 {
			t.Errorf("code line = %d cells, want the full block width 20: %q", CellWidth(line), line)
		}
		if !strings.Contains(line, "236") {
			t.Errorf("code line lost the code background: %q", line)
		}
	}
}

// Live text deltas reveal the answer while the stream is still open, and the
// completed turn publishes only what was not already revealed: the
// transcript holds the turn exactly once, in order, across several frames
// rather than one dump.
func TestLiveTextDeltasRevealWithoutDuplication(t *testing.T) {
	want := "live streaming answer that reveals as it is generated"
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			return ai.NewTextStream("live ", "streaming ", "answer that reveals as it is generated"), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.runTurn("go")
	var got strings.Builder
	deltas := 0
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			switch e := event.(type) {
			case AssistantDelta:
				deltas++
				got.WriteString(e.Text)
			case AssistantDone:
				if got.String() != want {
					t.Fatalf("transcript = %q, want the turn exactly once, in order", got.String())
				}
				if deltas < 3 {
					t.Errorf("deltas = %d, want the live reveals, not one dump", deltas)
				}
				return
			}
		case <-timeout:
			t.Fatal("the live turn never completed")
		}
	}
}

// A dedup skip that lands inside a multi-byte rune corrupts the tail: the
// transcript would show a replacement character where CJK/emoji text belongs.
// The skip must back off to the rune boundary so the republished tail stays
// valid UTF-8.
func TestCommonPrefixLenLandsOnRuneBoundary(t *testing.T) {
	final := "a\u4e2dx"
	diverged := "a\xe4\xb8y"
	skip := commonPrefixLen(final, diverged)
	rest := final[skip:]
	if !utf8.ValidString(rest) {
		t.Fatalf("tail = %q, want valid UTF-8 after a rune-boundary skip", rest)
	}
	if rest != "a\u4e2dx"[skip:] || skip != 1 {
		t.Errorf("skip = %d tail = %q, want 1 and the whole final rune intact", skip, rest)
	}
}
