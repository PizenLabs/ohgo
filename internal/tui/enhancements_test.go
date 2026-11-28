package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
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

// Space is the reader's one-key return to the live edge. After scrolling to
// the transcript top the viewport is detached; with an idle composer the key
// is not text, so it must jump rather than insert a leading blank into the
// next prompt.
func TestSpaceReturnsToLiveEdgeWhenScrolledUp(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	p.state.Apply(ScrollMoved{Delta: 10000})
	if p.state.Follow {
		t.Fatal("scroll did not detach the transcript")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeySpace})
	if p.state.Scroll != 0 || !p.state.Follow {
		t.Errorf("Scroll = %d Follow = %v, want the live edge back", p.state.Scroll, p.state.Follow)
	}
	if p.state.Input != "" {
		t.Errorf("space jumped to the bottom but also typed %q", p.state.Input)
	}
}

// A draft keeps space literal even while the reader had scrolled up: the
// viewport must never eat a character the user is composing.
func TestSpaceStaysLiteralWhileComposing(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	p.state.Apply(ComposerChanged{Text: "ab", Cursor: 2})
	p.state.Apply(ScrollMoved{Delta: 10})
	keyPress(p, tea.KeyMsg{Type: tea.KeySpace})
	if p.state.Input != "ab " {
		t.Errorf("input = %q, want the space inserted", p.state.Input)
	}
	if p.state.Follow {
		t.Error("typing must not reattach the transcript")
	}
}

// The scrolled-up marker is the discovery surface for the jump binding: a
// reader who scrolled away from the live edge must be told which key returns,
// with the key itself standing out from the caption.
func TestScrolledUpMarkerAdvertisesSpaceJump(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 10})
	for i := 0; i < 10; i++ {
		s.Apply(Notice{Text: "older"})
	}
	s.Apply(ScrollMoved{Delta: 4})
	out := Render(s)
	if plain := ansi.Strip(out); !strings.Contains(plain, "space: jump to latest") {
		t.Errorf("scrolled-up marker does not advertise the space jump:\n%s", plain)
	}
	if !strings.Contains(out, DarkPalette().Bold.Render("space")) {
		t.Errorf("scrolled-up marker does not highlight the space key:\n%q", out)
	}
}

// Header hierarchy is a document-structure contract, not a colour ladder:
// every heading level resolves to the Heading accent and bold weight, never to
// the body role and never to the chrome Title role. Depth comes from document
// spacing, not from a second palette.
func TestHeadingLevelsUseStructureAccent(t *testing.T) {
	for _, theme := range []string{ThemeDark, ThemeLight} {
		pal := PaletteFor(theme)
		heading := colorName(pal.Heading.GetForeground())
		body := colorName(pal.Assistant.GetForeground())
		title := colorName(pal.Title.GetForeground())
		if heading == body || heading == title {
			t.Errorf("theme %s: heading shares a role (heading %q, body %q, title %q)", theme, heading, body, title)
		}
		if !pal.Heading.GetBold() {
			t.Errorf("theme %s: heading role is not bold", theme)
		}
		for _, tc := range []struct{ src, text string }{
			{"# H1", "H1"}, {"## H2", "H2"}, {"### H3", "H3"},
		} {
			rendered := RenderMarkdownWrapped(tc.src, pal, 40)
			if !strings.Contains(rendered, pal.Heading.Render(tc.text)) {
				t.Errorf("theme %s: %q did not use the structure accent", theme, tc.src)
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
	want := strings.Repeat("streaming chunk reveal keeps long answers readable. ", 99) +
		"streaming chunk reveal keeps long answers readable."
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewTextStream(want), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.runTurn("go")
	var preview, committed string
	deltas := 0
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			p.state.Apply(event)
			switch e := event.(type) {
			case AssistantDelta:
				preview += e.Text
				deltas++
				if index := p.state.lastProvisionalAssistant(); index < 0 || !p.state.Entries[index].Provisional {
					t.Fatal("streamed text was not buffered as a provisional entry")
				}
				if got := p.state.AssistantText(); got != "" {
					t.Fatalf("uncommitted candidate entered the answer projection: %q", got)
				}
				if !strings.Contains(ansi.Strip(Render(p.state)), "verifying evidence…") {
					t.Fatal("streamed candidate was not marked as under review")
				}
			case AssistantReconcile:
				if e.Answer != agent.AnswerCommitted {
					continue
				}
				committed = e.Text
				if committed != want {
					t.Fatalf("committed text holds %d runes, want the %d-rune turn in order", len([]rune(committed)), len([]rune(want)))
				}
				if got := p.state.AssistantText(); got != committed {
					t.Fatalf("settled transcript = %q, want the committed text", got)
				}
			case AssistantDone:
				if deltas < 2 || preview != committed || committed != want {
					t.Fatalf("preview chunks=%d preview=%d runes committed=%d runes; want multiple ordered chunks settling to %d runes",
						deltas, len([]rune(preview)), len([]rune(committed)), len([]rune(want)))
				}
				return
			}
		case <-timeout:
			t.Fatal("the chunked turn never completed")
		}
	}
}

// Streamed prose is buffered until the answer gate, then the exact committed
// value appears once without duplication.
func TestRunTurnBuffersCandidateUntilCommit(t *testing.T) {
	deltas := []string{"# Project\n\n", "The project is a ", "Go agent runtime.\n"}
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewTextStream(deltas...), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.runTurn("what is this project")
	var preview, committed string
	artifactCount := 0
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			p.state.Apply(event)
			switch e := event.(type) {
			case AssistantDelta:
				preview += e.Text
				index := p.state.lastProvisionalAssistant()
				if index < 0 || !p.state.Entries[index].Provisional {
					t.Fatal("candidate delta was not retained as a buffered entry")
				}
				if got := p.state.AssistantText(); got != "" {
					t.Fatalf("uncommitted candidate entered the answer projection: %q", got)
				}
			case AssistantReconcile:
				if e.Answer == agent.AnswerCommitted {
					artifactCount++
					committed = e.Text
					if index := p.state.lastProvisionalAssistant(); index >= 0 {
						t.Fatal("committed answer still has a provisional entry")
					}
				}
			case AssistantDone:
				want := strings.TrimSpace(strings.Join(deltas, ""))
				if strings.TrimSpace(preview) != want || committed != want || artifactCount != 1 {
					t.Fatalf("preview=%q committed=%q count=%d want=%q count=1", preview, committed, artifactCount, want)
				}
				return
			}
		case <-timeout:
			t.Fatal("turn never completed")
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

// The completed turn can preview streamed text, but only the exact committed
// answer survives reconciliation.
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
	// The stream is visible as a provisional preview; the committed text
	// replaces it exactly once after the publication gate.
	p.runTurn("fix the failing test")
	var preview, got string
	deltas := 0
	committed := 0
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			switch e := event.(type) {
			case AssistantDelta:
				preview += e.Text
				deltas++
			case AssistantReconcile:
				if e.Answer == agent.AnswerCommitted {
					got = e.Text
					committed++
				}
			case AssistantDone:
				if strings.TrimSpace(preview) != want || got != want || committed != 1 || deltas == 0 {
					t.Fatalf("preview=%q transcript=%q committed=%d deltas=%d, want the turn once", preview, got, committed, deltas)
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

type terminalTestReadTool struct{}

func (terminalTestReadTool) Name() string        { return "read" }
func (terminalTestReadTool) Description() string { return "read fixture" }
func (terminalTestReadTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (terminalTestReadTool) Approval() agent.Approval { return agent.Approval{Tier: agent.TierRead} }
func (terminalTestReadTool) Exclusive(map[string]any) bool {
	return false
}
func (terminalTestReadTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "package fixture"}
}

// A full answer followed by a tool call is investigation, not a conclusion.
// The TUI publishes the text only when the turn completes and is publishable,
// so the abandoned answer never reaches the transcript.
func TestAbandonedAnswerBeforeToolIsNotPublished(t *testing.T) {
	responses := []ai.AssistantMessage{
		{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "abandoned answer that should not appear"},
				ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"path": "a.go"}},
			},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "final answer"}},
			StopReason: ai.StopEndTurn,
		},
	}
	index := 0
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			if index >= len(responses) {
				return ai.NewCompletedStream(responses[len(responses)-1]), nil
			}
			response := responses[index]
			index++
			return ai.NewCompletedStream(response), nil
		},
		Tools: runtimeCaps(terminalTestReadTool{}),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.runTurn("fix the failing test")
	var got strings.Builder
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			// Deltas are a provisional preview. A discard is the runtime
			// withdrawing tool narration; an accept replaces the preview with
			// the exact committed answer.
			if delta, ok := event.(AssistantDelta); ok {
				got.WriteString(delta.Text)
			}
			if reconcile, ok := event.(AssistantReconcile); ok {
				if reconcile.Answer != agent.AnswerCommitted {
					got.Reset()
				} else {
					got.Reset()
					got.WriteString(reconcile.Text)
				}
			}
			if _, ok := event.(AssistantDone); ok {
				if got.String() != "final answer" {
					t.Fatalf("published %q, want only the final answer", got.String())
				}
				return
			}
		case <-timeout:
			t.Fatal("turn did not finish")
		}
	}
}
