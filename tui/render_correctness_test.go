package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/jsonschema"
	"github.com/PizenLabs/ohgo/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// RC1 rendering and interaction regressions: each test pins one externally
// observable contract — what the user sees on the terminal — so a break in
// the pipeline fails loudly at the layer the user would notice it.

func stripForTest(t *testing.T, s string) string {
	t.Helper()
	return ansi.Strip(s)
}

func renderAssistant(t *testing.T, text string, width, height int) string {
	t.Helper()
	s := New()
	s.Apply(TerminalResized{Width: width, Height: height})
	s.Apply(AssistantDelta{Text: text})
	s.Apply(AssistantDone{})
	return Render(s)
}

func TestMarkdownListsPreserveIndentation(t *testing.T) {
	out := stripForTest(t, RenderMarkdownWrapped("A paragraph.\n\n- one\n- two\n- three\n- four\n- five", DarkPalette(), 40))
	lines := strings.Split(out, "\n")
	var bullets []string
	for _, line := range lines {
		if strings.Contains(line, "one") || strings.Contains(line, "two") ||
			strings.Contains(line, "three") || strings.Contains(line, "four") ||
			strings.Contains(line, "five") {
			bullets = append(bullets, line)
		}
	}
	if len(bullets) != 5 {
		t.Fatalf("bullets = %q, want five list rows", bullets)
	}
	for _, b := range bullets {
		if !strings.HasPrefix(b, "• ") {
			t.Errorf("bullet = %q, want the marker flush at column zero", b)
		}
	}
}

func TestMarkdownNestedListsKeepHierarchy(t *testing.T) {
	out := stripForTest(t, RenderMarkdownWrapped("- a\n  - b\n    - c", DarkPalette(), 40))
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			got = append(got, line)
		}
	}
	want := []string{"• a", "  • b", "    • c"}
	if len(got) != len(want) {
		t.Fatalf("nested = %q, want %q", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("nested[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestMarkdownOrderedContinuationHangs(t *testing.T) {
	out := stripForTest(t, RenderMarkdownWrapped("1. first item with a long body that must wrap somewhere\n2. second", DarkPalette(), 30))
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "1. ") {
		t.Fatalf("line0 = %q, want the ordered marker kept", lines[0])
	}
	// Continuation rows of the first item hang under its body; the second
	// item starts its own marker.
	i := 1
	for ; i < len(lines) && !strings.HasPrefix(lines[i], "2. "); i++ {
		if lines[i] == "" {
			continue
		}
		if !strings.HasPrefix(lines[i], "   ") {
			t.Errorf("continuation = %q, want hanging alignment under the body", lines[i])
		}
	}
	if i >= len(lines) {
		t.Errorf("output lost the second item:\n%s", out)
	}
}

func TestMarkdownBlockStylesStayDistinct(t *testing.T) {
	// Hierarchy is a styling contract, and test terminals are colorless
	// (lipgloss degrades everything to plain text by design). Verify the
	// wiring with a forced-profile palette that cannot hide a collapse:
	// each level and block must resolve through a different style.
	fp := forcedPalette()
	if f1, f2, f3 := RenderMarkdownWrapped("# Title", fp, 40),
		RenderMarkdownWrapped("## Title", fp, 40),
		RenderMarkdownWrapped("### Title", fp, 40); f1 == f2 || f2 == f3 || f1 == f3 {
		t.Error("heading levels share one style: hierarchy is unreadable")
	}
	blocks := map[string]string{
		"quote": RenderMarkdownWrapped("> quoted", fp, 40),
		"code":  RenderMarkdownWrapped("```go\nx()\n```", fp, 40),
		"rule":  RenderMarkdownWrapped("---", fp, 40),
		"list":  RenderMarkdownWrapped("- item", fp, 40),
		"head":  RenderMarkdownWrapped("# Title", fp, 40),
	}
	seen := map[string]string{}
	for name, rendered := range blocks {
		for other, or := range seen {
			if rendered == or {
				t.Errorf("%s renders identically to %s: styles collapsed", name, other)
			}
		}
		seen[name] = rendered
	}
}

// forcedPalette wires distinct styles through a fixed ANSI256 profile so
// style-collapse mutations fail even on a colorless test terminal.
func forcedPalette() *Palette {
	// An explicit profile on a private renderer: no global state, no
	// environment dependence, and styles always emit escapes so a
	// collapse mutation cannot hide behind a colorless terminal.
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.ANSI256)
	fg := func(token string) lipgloss.Style {
		return renderer.NewStyle().Foreground(lipgloss.Color(token))
	}
	return &Palette{
		User:        fg("121").Bold(true),
		UserBG:      renderer.NewStyle().Background(lipgloss.Color("237")),
		Assistant:   fg("252"),
		ToolName:    fg("73").Bold(true),
		ToolDone:    fg("42"),
		ToolError:   fg("196"),
		ToolRun:     fg("214"),
		Notice:      fg("244").Italic(true),
		Status:      fg("244"),
		Prompt:      fg("121"),
		Choice:      fg("252"),
		Selected:    fg("121").Bold(true),
		Truncated:   fg("244"),
		Cursor:      fg("121").Reverse(true),
		Queued:      fg("244").Italic(true),
		Code:        fg("228").Background(lipgloss.Color("236")),
		CodeFence:   fg("244"),
		Heading:     fg("121").Bold(true),
		Heading2:    fg("114").Bold(true),
		Heading3:    fg("144").Bold(true),
		CodeKeyword: fg("85").Background(lipgloss.Color("236")).Bold(true),
		CodeString:  fg("214").Background(lipgloss.Color("236")),
		CodeComment: fg("240").Background(lipgloss.Color("236")).Italic(true),
		CodeNumber:  fg("73").Background(lipgloss.Color("236")),
		Bold:        renderer.NewStyle().Bold(true),
		Italic:      renderer.NewStyle().Italic(true),
		Quote:       fg("244").Italic(true),
		Bullet:      fg("121"),
		Muted:       fg("240"),
	}
}

func TestMarkdownDelimitersDoNotLeak(t *testing.T) {
	in := "# Title\n\nRun **bold** and *italic* with `code` and [docs](https://example.com).\n\n- item\n\n1. first\n\n> quoted\n\n---\n\n```go\nfmt.Println()\n```"
	out := stripForTest(t, RenderMarkdownWrapped(in, DarkPalette(), 60))
	// Completed inline syntax leaves no raw markers: no asterisks,
	// backticks, brackets, heading hashes, or quote prefixes. Fence
	// boundaries (```go / ```) and the link URL in parens are deliberate
	// readable structure, not leakage, and are asserted present below.
	for _, leak := range []string{"**", "[docs]", "# Title", "> quoted", "*italic*"} {
		if strings.Contains(out, leak) {
			t.Errorf("output contains raw delimiter %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"Title", "bold", "italic", "code", "docs", "https://example.com", "item", "first", "quoted", "```go", "fmt.Println()"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lost content %q:\n%s", want, out)
		}
	}
}

func TestMarkdownIncompleteStreamingRendersSafely(t *testing.T) {
	pal := DarkPalette()
	// Unclosed fence streams as code to the end without dropping text.
	if out := RenderMarkdownWrapped("```py\nx = 1", pal, 40); !strings.Contains(stripForTest(t, out), "x = 1") {
		t.Errorf("unclosed fence dropped its body: %q", out)
	}
	// Unclosed emphasis stays literal and converges when closed.
	partial := stripForTest(t, RenderMarkdownWrapped("a **bold move", pal, 40))
	if !strings.Contains(partial, "**bold move") {
		t.Errorf("partial emphasis was eaten: %q", partial)
	}
	full := stripForTest(t, RenderMarkdownWrapped("a **bold move** now", pal, 40))
	if strings.Contains(full, "**") {
		t.Errorf("completed emphasis leaked delimiters: %q", full)
	}
	if !strings.Contains(full, "bold move") {
		t.Errorf("completed emphasis lost text: %q", full)
	}
	// A table cut off before its delimiter row never corrupts neighbours.
	cut := stripForTest(t, RenderMarkdownWrapped("before\n\n| a | b |", pal, 40))
	if !strings.Contains(cut, "before") || !strings.Contains(cut, "a") {
		t.Errorf("cut table corrupted neighbours: %q", cut)
	}
}

func TestMarkdownConvergesRegardlessOfChunkSplits(t *testing.T) {
	pal := DarkPalette()
	full := "Intro with **bold** and a [link](https://example.com).\n\n- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfmt.Println()\n```\n"
	splits := [][]string{
		{full},
		{full[:10], full[10:40], full[40:]},
		{full[:1], full[1:2], full[2:]},
	}
	var baseline string
	for i, chunks := range splits {
		s := New()
		for _, c := range chunks {
			s.Apply(AssistantDelta{Text: c})
		}
		s.Apply(AssistantDone{})
		got := RenderMarkdownWrapped(s.Entries[0].AssistantText, pal, 50)
		if i == 0 {
			baseline = got
			continue
		}
		if got != baseline {
			t.Errorf("chunk split %d diverged:\n%q\nwant:\n%q", i, got, baseline)
		}
	}
	// Rendering the whole text at once matches the accumulated render.
	if want := RenderMarkdownWrapped(full, pal, 50); baseline != want {
		t.Error("accumulated render differs from rendering the complete text from scratch")
	}
}

func TestMarkdownWidthsAcrossTerminals(t *testing.T) {
	in := "A paragraph followed by five items.\n\n- one\n- two\n- three\n- four\n- five\n\n| name | description |\n|---|---|\n| alpha | a much longer description that must wrap inside its column |\n\nSome **bold** text with a verylongunbrokenrunthatmustfoldhardwithoutoverflow."
	for _, width := range []int{40, 60, 80, 120} {
		out := renderAssistant(t, in, width, 40)
		for i, line := range strings.Split(out, "\n") {
			if CellWidth(line) > width {
				t.Errorf("width %d line %d = %d cells, want no horizontal overflow: %q", width, i, CellWidth(line), line)
			}
		}
	}
}

func TestMarkdownWideCombiningAndANSIStaySafe(t *testing.T) {
	in := "日本語テストの折り返し 🎉 emoji é combining **bold** and a verylongunbrokenrunthatmustfoldhard."
	out := renderAssistant(t, in, 50, 30)
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 50 {
			t.Errorf("line %d = %d cells, want CJK/emoji folded by cells: %q", i, CellWidth(line), line)
		}
		if strings.Contains(line, "\x1b[31m") {
			t.Errorf("line %d carries raw model ANSI into the viewport: %q", i, line)
		}
	}
	stripped := stripForTest(t, out)
	for _, want := range []string{"日本語", "🎉", "bold", "verylong"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("output lost %q:\n%s", want, stripped)
		}
	}
}

func TestMarkdownTableDegradesOnNarrowTerminal(t *testing.T) {
	in := "| name | description |\n|---|---|\n| alpha | short |\n| beta | a much longer description that must still fit |"
	out := stripForTest(t, RenderMarkdownWrapped(in, DarkPalette(), 24))
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 24 {
			t.Errorf("narrow line %d = %d cells, want graceful fallback, not overflow: %q", i, CellWidth(line), line)
		}
	}
	for _, want := range []string{"alpha", "beta", "description"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow table lost %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "|---") {
		t.Errorf("narrow table kept a raw markdown delimiter row:\n%s", out)
	}
}

func TestComposerCursorSurvivesMultilineAndResize(t *testing.T) {
	text := "first line\nsecond 日本語 line with emoji 🎉\nthird"
	cursor := len([]rune("first line\nsecond "))
	for _, width := range []int{80, 40, 20} {
		rows, cursorRow, cursorRune, have := layoutComposer(text, cursor, width-2)
		if !have {
			t.Fatalf("width %d lost the cursor", width)
		}
		if cursorRow < 0 || cursorRow >= len(rows) {
			t.Errorf("width %d cursor row %d outside %d rows", width, cursorRow, len(rows))
		}
		// Reassembling the rows must reproduce the input exactly.
		if strings.Join(rows, "\n") != text && !multilineWrapped(rows, text) {
			t.Errorf("width %d rows do not cover the input: %q", width, rows)
		}
		_ = cursorRune
	}
	s := New()
	s.Apply(ComposerChanged{Text: text, Cursor: cursor})
	s.Apply(TerminalResized{Width: 80, Height: 24})
	before := Render(s)
	s.Apply(TerminalResized{Width: 40, Height: 24})
	after := Render(s)
	for _, out := range []string{before, after} {
		if strings.Count(out, "▌") != 1 {
			t.Errorf("want exactly one cursor marker, got %d in %q", strings.Count(out, "▌"), out)
		}
	}
	if !strings.Contains(after, "second") {
		t.Errorf("resize lost composer text: %q", after)
	}
}

func multilineWrapped(rows []string, text string) bool {
	// layoutComposer hard-folds logical lines; joining display rows with ""
	// per logical line is covered by composerRows tests — here only check
	// every rune survived in order.
	var sb strings.Builder
	for _, r := range rows {
		sb.WriteString(r)
	}
	a, b := sb.String(), strings.ReplaceAll(text, "\n", "")
	return strings.ReplaceAll(a, "\n", "") == b
}

func TestSlashSuggestionsMatchRegistry(t *testing.T) {
	all := SlashSuggestions("/")
	var names []string
	for _, spec := range all {
		names = append(names, spec.Name)
	}
	for _, spec := range Commands {
		if spec.Hidden {
			for _, n := range names {
				if n == spec.Name {
					t.Errorf("hidden alias /%s leaked into discovery", n)
				}
			}
			continue
		}
		found := false
		for _, n := range names {
			if n == spec.Name {
				found = true
			}
		}
		if !found {
			t.Errorf("registry command /%s missing from bare-/ suggestions", spec.Name)
		}
	}
	filtered := SlashSuggestions("/mod")
	if len(filtered) != 1 || filtered[0].Name != "model" {
		t.Errorf("/mod suggestions = %+v, want exactly [/model]", filtered)
	}
	if got := SlashSuggestions("/zzz"); len(got) != 0 {
		t.Errorf("/zzz suggestions = %+v, want none", got)
	}
	if got := SlashSuggestions("not a command"); len(got) != 0 {
		t.Errorf("prose suggestions = %+v, want none", got)
	}
}

func TestSlashTypingShowsInlinePicker(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(ComposerChanged{Text: "/", Cursor: 1})
	out := Render(p.state)
	if !strings.Contains(out, "/help") || !strings.Contains(out, "/model") {
		t.Errorf("bare / shows no discovery:\n%s", out)
	}
	// The transcript stays visible behind inline discovery.
	p.state.Apply(UserSubmitted{Text: "earlier"})
	p.state.Apply(ComposerChanged{Text: "/mod", Cursor: 4})
	if out := Render(p.state); !strings.Contains(out, "earlier") || !strings.Contains(out, "/model") {
		t.Errorf("inline discovery covered the transcript:\n%s", out)
	}
}

func TestSlashEnterCompletesThenRuns(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var switched string
	p.SetModelSwitcher(func(id string) error { switched = id; return nil }, func() string { return switched })
	p.SetModelLister(func() []string { return []string{"openai/gpt", "model/m1"} })
	p.state.Apply(ComposerChanged{Text: "/mod", Cursor: 4})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Input != "/model" {
		t.Fatalf("input = %q, want Enter to complete the partial command first", p.state.Input)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Picker == nil {
		t.Errorf("second Enter on /model ran nothing: no model picker opened")
	}
}

func TestSlashNavigationAndDismissKeepDraft(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "/", Cursor: 1})
	first := SlashSuggestions(p.state.Input)[p.state.SlashCursor]
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	second := SlashSuggestions(p.state.Input)[p.state.SlashCursor]
	if first.Name == second.Name && len(SlashSuggestions("/")) > 1 {
		t.Error("down did not move the inline highlight")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Input != "/" {
		t.Errorf("input = %q, want esc to keep the draft while dismissing", p.state.Input)
	}
	if out := Render(p.state); strings.Contains(out, "/help") && strings.Contains(out, "list the available commands") {
		t.Error("dismissed suggestions still render")
	}
	// Unknown slash still reports instead of reaching the model.
	p.state.Apply(ComposerChanged{Text: "/zzz", Cursor: 4})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	found := false
	for _, e := range p.state.Entries {
		if e.Kind == EntryNotice && strings.Contains(e.Notice, "unknown command") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the unknown-command notice", p.state.Entries)
	}
}

func TestQuestionMarkOpensHelpWhenEmpty(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, runes("?"))
	if p.state.Picker == nil || p.state.Picker.Kind != PickerCommands {
		t.Errorf("picker = %+v, want ? on an empty composer to open command help", p.state.Picker)
	}
	if p.state.Input != "" {
		t.Errorf("input = %q, want ? consumed as help, not typed", p.state.Input)
	}
}

func TestQuestionMarkStaysLiteralWhileComposing(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "really", Cursor: 6})
	keyPress(p, runes("?"))
	if p.state.Picker != nil {
		t.Error("? stole input from an active composer to open help")
	}
	if p.state.Input != "really?" {
		t.Errorf("input = %q, want the literal question mark kept", p.state.Input)
	}
}

func TestComposerHasTwoRulesAndTruthfulState(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	idle := Render(s)
	if strings.Count(idle, strings.Repeat("─", 8)) < 2 {
		t.Errorf("idle composer lacks the two framing rules:\n%s", idle)
	}
	if strings.Contains(idle, "working") || strings.Contains(idle, "queued") {
		t.Errorf("idle composer claims work that is not happening:\n%s", idle)
	}
	// Busy is truthful in the header phase, not restated under the input.
	s.Apply(UserSubmitted{Text: "go"})
	busy := Render(s)
	if !strings.Contains(strings.Split(busy, "\n")[0], "working") {
		t.Errorf("busy header lost the runtime phase:\n%s", busy)
	}
	if strings.Contains(busy, "queued") {
		t.Errorf("busy composer invents queued prompts:\n%s", busy)
	}
	s.Apply(MessageQueued{Text: "next"})
	queued := Render(s)
	if !strings.Contains(queued, "1 queued") {
		t.Errorf("queued prompt not shown truthfully:\n%s", queued)
	}
}

// TestAttachSessionReplaysTranscriptIntoState guards the CLI resume path:
// attaching with history must populate the visible transcript, or the UI
// opens on an empty welcome screen while the loop holds full context.
func TestAttachSessionReplaysTranscriptIntoState(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	history := []agent.Message{
		agent.UserMessage{Text: "earlier"},
		agent.AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "answer"}},
		}},
	}
	p.AttachSession(nil, &session.Session{ID: 8}, history)
	if len(p.state.Entries) != 3 {
		t.Fatalf("entries = %+v, want replayed user + assistant + resume notice", p.state.Entries)
	}
	if p.state.Entries[0].UserText != "earlier" {
		t.Errorf("first entry = %+v, want the replayed prompt", p.state.Entries[0])
	}
	found := false
	for _, e := range p.state.Entries {
		if e.Kind == EntryNotice && strings.Contains(e.Notice, "resumed session 8") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the resume confirmation notice", p.state.Entries)
	}
	if out := Render(p.state); strings.Contains(out, "Describe a task to begin") {
		t.Errorf("resumed render shows the empty welcome:\n%s", out)
	}
}

// TestAttachSessionEmptyHistoryStaysEmpty pins the fresh-session case:
// attaching without history must not fabricate entries or notices.
func TestAttachSessionEmptyHistoryStaysEmpty(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.AttachSession(nil, &session.Session{ID: 9}, nil)
	if len(p.state.Entries) != 0 {
		t.Errorf("entries = %+v, want none for a fresh session", p.state.Entries)
	}
}

// Repeated approval cycles must never stack chrome: denying one tool and
// asking for the next leaves exactly one prompt, one status, and the pinned
// composer — the triplicated-dialog class from the field stays impossible.
func TestRepeatedApprovalsNeverDuplicateChrome(t *testing.T) {
	p := NewProgram(scriptedLoop(t, "x"), "m", 0)
	p.PublishWorkspace(WorkspaceUpdated{Session: "#9", HasSession: true})
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	feed := func(event Event) {
		p.publish(event)
		select {
		case got := <-p.events:
			p.Update(teaEvent{event: got})
		default:
			t.Fatal("event channel empty")
		}
	}
	for i := 0; i < 3; i++ {
		feed(UserSubmitted{Text: "go"})
		feed(ApprovalAsked{ToolName: "ls", Summary: "run ls", Choices: []string{"Yes", "No"}})
		out := p.View()
		if got := len(strings.Split(out, "\n")); got != 24 {
			t.Fatalf("cycle %d: %d lines, want 24", i, got)
		}
		if got := strings.Count(out, "Run this tool?"); got != 1 {
			t.Fatalf("cycle %d: approval prompt %d times, want exactly once", i, got)
		}
		if got := strings.Count(out, "#9"); got != 1 {
			t.Fatalf("cycle %d: status marker %d times, want exactly once", i, got)
		}
		if got := fullRules(out, 79); got != 3 {
			t.Fatalf("cycle %d: %d full rules, want header + dual composer", i, got)
		}
		feed(ApprovalAnswered{})
		feed(AssistantDone{})
	}
}

// echoTool runs inline and succeeds, modelling a read-only tool whose
// outcome must reach both the transcript and the store.
type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "test echo tool" }
func (echoTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"value":{"type":"string"}}}`))
}
func (echoTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (echoTool) Exclusive(map[string]any) bool { return false }
func (echoTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "echo-out"}
}

// TestRunTurnPersistsEveryTurnOnce guards the program's half of the
// persistence contract: the loop's turn hook stays silent in UI mode, so
// runTurn must persist the user, every assistant, and every turn's tool
// results — exactly once each. The old code kept only the last turn's
// results (earlier evidence vanished from resume) while the hook doubled
// everything it did keep.
func TestRunTurnPersistsEveryTurnOnce(t *testing.T) {
	call := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "echo", Arguments: map[string]any{"value": "x"}}},
		StopReason: ai.StopToolUse,
	}
	index := 0
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		index++
		if index == 1 {
			return ai.NewCompletedStream(call), nil
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "done"}},
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []agent.Tool{echoTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create(t.TempDir(), "test/m")
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	p.AttachSession(store, &sess, nil)
	p.runTurn("go")

	stored, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	results := 0
	for _, msg := range stored {
		roles = append(roles, msg.Role)
		if msg.Role == "toolResult" {
			results++
			if !strings.Contains(msg.Text, "echo-out") {
				t.Errorf("tool result text = %q, want the echo outcome", msg.Text)
			}
		}
	}
	want := []string{"user", "assistant", "toolResult", "assistant"}
	if len(roles) != len(want) {
		t.Fatalf("stored roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("stored roles = %v, want %v", roles, want)
		}
	}
	if results != 1 {
		t.Errorf("tool results persisted = %d, want exactly 1", results)
	}
}

func TestOverlayCleanupOnCancelAndQuit(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "work"})
	p.state.Apply(MessageQueued{Text: "stale"})
	p.cancelTurnNow("cancelled")
	if len(p.state.Queued) != 0 {
		t.Errorf("Queued = %v, want cancellation to drop prompts typed against the abandoned turn", p.state.Queued)
	}
	if !p.state.Cancelled {
		t.Error("no terminal cancellation recorded: want Cancelled, not a second notice")
	}
	if p.state.Status.Busy {
		t.Error("spinner still claims working after cancellation")
	}
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "cancelled") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the single cancellation line", p.state.Entries)
	}
	// A second cancel is idempotent: no duplicate transition, no duplicate line.
	before := len(p.state.Entries)
	p.cancelTurnNow("cancelled")
	if len(p.state.Entries) != before {
		t.Errorf("entries grew on repeated cancel: want one semantic transition")
	}
	idle := NewProgram(nil, "test", 0)
	_, cmd := idle.cancelOrQuit()
	if !idle.state.Quit {
		t.Error("idle ctrl+c must set the quit flag for alt-screen teardown")
	}
	if cmd == nil {
		t.Error("idle ctrl+c must return tea.Quit so the runtime restores the terminal")
	}
}

func TestRunCommandQuitRestoresTerminal(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	if cmd := p.runCommand(Command{Name: "quit"}); cmd == nil {
		t.Error("/quit must return tea.Quit so alt-screen exits and the cursor returns")
	}
	if !p.state.Quit {
		t.Error("/quit must set the quit flag")
	}
}

func TestFailedTurnReportsError(t *testing.T) {
	loop, err := testFailingLoop()
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurn("go")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the failed turn never returned: cleanup missing on the error path")
	}
	found := false
	timeout := time.After(2 * time.Second)
drain:
	for {
		select {
		case event := <-p.events:
			if notice, ok := event.(Notice); ok && strings.HasPrefix(notice.Text, "error: ") {
				found = true
			}
		case <-timeout:
			break drain
		}
	}
	if !found {
		t.Error("no error notice published: the UI would look idle after a failure")
	}
}

// testFailingLoop builds a loop whose stream fails, exercising the error
// cleanup path: the turn must return and the UI must report, not idle.
func testFailingLoop() (*agent.Loop, error) {
	return agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			return nil, errors.New("boom")
		},
		Tools: []agent.Tool{&idleTool{}},
	})
}

func TestResizeDuringStreamKeepsViewportClean(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	chunks := []string{"# Report\n\n", "Intro **bold** text.\n\n", "- one\n- two\n", "| a | b |\n|---|---|\n", "| 1 | 2 |\n\n", "> quoted\n\n```go\nx()\n```\n"}
	for i, c := range chunks {
		s.Apply(AssistantDelta{Text: c})
		if i == 2 {
			s.Apply(TerminalResized{Width: 50, Height: 16})
		}
		if i == 4 {
			s.Apply(TerminalResized{Width: 100, Height: 30})
		}
		out := Render(s)
		width, height := s.Width, s.Height
		lines := strings.Split(out, "\n")
		if len(lines) != height {
			t.Errorf("chunk %d rendered %d lines in a %d-row viewport, want exactly filled", i, len(lines), height)
		}
		for j, line := range lines {
			if CellWidth(line) > width {
				t.Errorf("chunk %d line %d = %d cells, want no overflow or overlap: %q", i, j, CellWidth(line), line)
			}
			if strings.Contains(line, "\x1b") && strings.Count(line, "\x1b") != strings.Count(line, "m") {
				t.Errorf("chunk %d line %d splits an escape sequence: %q", i, j, line)
			}
		}
	}
	s.Apply(AssistantDone{})
	if !strings.Contains(Render(s), "Report") || !strings.Contains(Render(s), "quoted") {
		t.Error("resized streaming lost content")
	}
	// Persisted content is never mutated to fit the width: the stored
	// entry still holds the raw model output verbatim.
	var raw strings.Builder
	for _, c := range chunks {
		raw.WriteString(c)
	}
	if s.Entries[0].AssistantText != raw.String() {
		t.Error("rendering mutated the stored assistant text to fit the terminal")
	}
}

// Viewport budgeting: every known-size render fills exactly its height —
// idle, busy, streaming, approval, picker, slash, queued, and secret.
// Each case pins the row contract its region owns.
func TestViewportExactAcrossModesAndStates(t *testing.T) {
	sizes := [][2]int{{100, 24}, {80, 24}, {60, 24}, {100, 40}, {40, 24}, {200, 50}}
	for _, size := range sizes {
		w, h := size[0], size[1]
		mk := func() State {
			s := New()
			s.Status.Model = "m"
			s.Apply(TerminalResized{Width: w, Height: h})
			return s
		}
		cases := map[string]func(*State){}
		cases["idle"] = func(s *State) {}
		cases["busy"] = func(s *State) { s.Apply(UserSubmitted{Text: "go"}) }
		cases["stream"] = func(s *State) {
			s.Apply(UserSubmitted{Text: "go"})
			s.Apply(AssistantDelta{Text: "# T\n\n- a\n- b\n| x | y |\n|---|---|\n| 1 | 2 |\n"})
		}
		cases["approval"] = func(s *State) {
			s.Apply(UserSubmitted{Text: "go"})
			s.Apply(ApprovalAsked{ToolName: "b", Summary: "s", Choices: []string{"Yes", "No"}})
		}
		cases["queued"] = func(s *State) {
			s.Apply(UserSubmitted{Text: "go"})
			s.Apply(MessageQueued{Text: "x"})
			s.Apply(ComposerChanged{Text: "typing", Cursor: 6})
		}
		cases["secret"] = func(s *State) { s.Apply(SecretOpened{Label: "key"}) }
		cases["secret-long"] = func(s *State) {
			s.Apply(SecretOpened{Label: "key"})
			long := strings.Repeat("k", 200)
			s.Apply(ComposerChanged{Text: long, Cursor: len([]rune(long))})
		}
		for name, setup := range cases {
			s := mk()
			setup(&s)
			if got := len(strings.Split(Render(s), "\n")); got != h {
				t.Errorf("%s %dx%d rendered %d lines, want exactly %d", name, w, h, got, h)
			}
		}
		// Pickers fill exactly too, with few or many results.
		for _, n := range []int{1, 30} {
			s := mk()
			var items []PickerItem
			for i := 0; i < n; i++ {
				items = append(items, PickerItem{Title: "item", Value: "v"})
			}
			s.Apply(PickerOpened{Kind: PickerCommands, Title: "c", Items: items})
			if got := len(strings.Split(Render(s), "\n")); got != h {
				t.Errorf("picker n=%d %dx%d rendered %d lines, want exactly %d", n, w, h, got, h)
			}
		}
		// Slash discovery with a queue behind it, full window and none.
		for _, input := range []string{"/", "/zzz-no-match"} {
			s := mk()
			s.Apply(UserSubmitted{Text: "go"})
			s.Apply(MessageQueued{Text: "x"})
			s.Apply(ComposerChanged{Text: input, Cursor: len([]rune(input))})
			if got := len(strings.Split(Render(s), "\n")); got != h {
				t.Errorf("slash %q %dx%d rendered %d lines, want exactly %d", input, w, h, got, h)
			}
		}
	}
}

// Tiny viewports degrade within bounds: chrome plus a modal minimum cannot
// fit exactly under ~12 rows, but the transcript yields first and the total
// stays within two rows of the viewport instead of flooding it.
func TestTinyViewportStaysBounded(t *testing.T) {
	for _, size := range [][2]int{{60, 12}, {80, 10}, {50, 9}} {
		w, h := size[0], size[1]
		s := New()
		s.Status.Model = "m"
		s.Apply(TerminalResized{Width: w, Height: h})
		s.Apply(UserSubmitted{Text: "go"})
		s.Apply(ApprovalAsked{ToolName: "b", Summary: strings.Repeat("detail ", 50), Choices: []string{"Yes", "No"}})
		if got := len(strings.Split(Render(s), "\n")); got > h+2 {
			t.Errorf("approval %dx%d rendered %d lines, want at most %d", w, h, got, h+2)
		}
		q := New()
		q.Status.Model = "m"
		q.Apply(TerminalResized{Width: w, Height: h})
		q.Apply(UserSubmitted{Text: "go"})
		q.Apply(MessageQueued{Text: "x"})
		q.Apply(ComposerChanged{Text: "/", Cursor: 1})
		if got := len(strings.Split(Render(q), "\n")); got > h+2 {
			t.Errorf("slash+queue %dx%d rendered %d lines, want at most %d", w, h, got, h+2)
		}
	}
}

// Queued prompts live only in the composer: the transcript holds
// conversation history, never interaction state, so no "queued:" placeholder
// may float mid-screen while the composer restates the count.
func TestQueuedPromptLivesOnlyInComposer(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(MessageQueued{Text: "first prompt"})
	s.Apply(MessageQueued{Text: "second prompt"})
	out := Render(s)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(stripForTest(t, line)), "queued:") {
			t.Errorf("transcript placeholder floats mid-screen: %q", line)
		}
	}
	if got := strings.Count(out, "first prompt"); got != 1 {
		t.Errorf("queue head appears %d times, want exactly once (in the composer)", got)
	}
	if !strings.Contains(out, "2 queued") {
		t.Errorf("composer lost the queue count:\n%s", out)
	}
	if strings.Contains(stripForTest(t, out), "second prompt") {
		t.Error("queue beyond the head must wait its turn, not crowd the chrome")
	}
}

func TestLinkWithSpaceStaysLiteral(t *testing.T) {
	// A URL never contains a raw space: "[t](http://x y)" is prose with
	// brackets, not a link, and must not gain parens or lose brackets.
	out := stripForTest(t, RenderMarkdownWrapped("[t](http://x y)", DarkPalette(), 40))
	if !strings.Contains(out, "[t](http://x y)") {
		t.Errorf("output mangled bracket prose: %q", out)
	}
}

// The bottom chrome is pinned, never floating: driving the real Program
// through submits, streaming deltas, queueing, approval, and resizes, every
// frame must fill the viewport exactly, carry the session marker exactly
// once outside busy turns (while busy the hint row belongs to cancellation
// keys, and the marker must never duplicate mid-screen), and show exactly
// the header separator plus the two composer rules — no ghost chrome
// anywhere else.
func TestLiveProgramFramesHoldBottomChrome(t *testing.T) {
	p := NewProgram(scriptedLoop(t, "answer"), "test-model", 100)
	p.PublishWorkspace(WorkspaceUpdated{Cwd: "/work/project", HasCwd: true, Session: "#9", HasSession: true})
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	feed := func(event Event) {
		p.publish(event)
		select {
		case got := <-p.events:
			p.Update(teaEvent{event: got})
		default:
			t.Fatal("event channel empty: the frame was never driven")
		}
	}
	check := func(label string, height, width, ruleWidth, wantMarker int) {
		t.Helper()
		out := p.View()
		lines := strings.Split(out, "\n")
		if len(lines) != height {
			t.Errorf("%s: %d lines, want exactly %d: chrome floats", label, len(lines), height)
		}
		for i, line := range lines {
			if CellWidth(line) > width {
				t.Errorf("%s line %d overflows: %q", label, i, line)
			}
		}
		if !strings.Contains(lines[0], "ohgo") {
			t.Errorf("%s: header left the top: %q", label, lines[0])
		}
		if got := strings.Count(out, "#9"); got != wantMarker {
			t.Errorf("%s: session marker %d times, want exactly %d", label, got, wantMarker)
		}
		if got := fullRules(out, ruleWidth); got != 3 {
			t.Errorf("%s: %d full-width rules, want 3 (header + dual composer)", label, got)
		}
	}

	check("idle", 24, 80, 79, 1)
	keyPress(p, runes("do it"))
	check("typing", 24, 80, 79, 1)
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter}) // starts the scripted turn
	feed(UserSubmitted{Text: "do it"})
	// While busy the hint row belongs to cancellation keys, so the session
	// marker yields until the turn ends; it must never duplicate, either.
	check("submitted-busy", 24, 80, 79, 0)
	feed(AssistantDelta{Text: "# Title\n\nSome **bold** text.\n"})
	check("streaming", 24, 80, 79, 0)
	feed(MessageQueued{Text: "later"})
	keyPress(p, runes("x"))
	check("queued-typing", 24, 80, 79, 0)
	feed(AssistantDelta{Text: "More.\n\n- one\n- two\n"})
	check("streaming-list", 24, 80, 79, 0)
	p.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	check("resized", 20, 60, 59, 0)
	feed(AssistantDone{})
	check("done", 20, 60, 59, 1)
	feed(ApprovalAsked{ToolName: "bash", Summary: "run it", Choices: []string{"Yes", "No"}})
	check("approval", 20, 60, 59, 1)
}

// fullRules counts lines that are nothing but a full-width rule: the header
// separator and the dual composer borders. Transcript rules ("─ ─ ─") and
// table borders carry other characters and never match.
func fullRules(out string, width int) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		plain := ansi.Strip(line)
		if CellWidth(plain) != width || plain == "" {
			continue
		}
		if strings.Trim(plain, "─") == "" {
			n++
		}
	}
	return n
}

// Scrolling must not unpin the chrome: a scrolled-up frame still fills the
// viewport exactly, keeps the header on top, and keeps the hint bar pinned
// last with the dual composer rules directly above it — the prompt never
// drifts up into the transcript region.
func TestScrolledFrameKeepsFooterPinned(t *testing.T) {
	s := New()
	s.Status.Model = "m"
	s.Apply(TerminalResized{Width: 80, Height: 24})
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&sb, "line %d with some words to fill the transcript\n", i)
	}
	s.Apply(AssistantDelta{Text: sb.String()})
	s.Apply(AssistantDone{})
	s.Apply(WorkspaceUpdated{Cwd: "/work/project", HasCwd: true, Session: "#9", HasSession: true})
	s.Apply(ScrollMoved{Delta: 10})
	if s.Follow {
		t.Fatal("scroll left follow engaged: the scrolled case cannot be reached")
	}
	out := Render(s)
	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("%d lines, want exactly 24: the footer floats", len(lines))
	}
	if !strings.Contains(lines[0], "ohgo") {
		t.Errorf("header left the top: %q", lines[0])
	}
	if got := fullRules(out, 79); got != 3 {
		t.Errorf("%d full-width rules, want 3 (header + dual composer)", got)
	}
	if !strings.Contains(out, "lines above") {
		t.Error("scrolled viewport lost its position marker")
	}
	// Bottom block is composer rule, composer, composer rule, hint bar — in
	// that order, with the session footer pinned last.
	if last := stripForTest(t, lines[20]); strings.Trim(last, "─") != "" || CellWidth(last) != 79 {
		t.Errorf("line 20 = %q, want the composer's top rule above the prompt", last)
	}
	if last := stripForTest(t, lines[23]); !strings.Contains(last, "#9") {
		t.Errorf("last line = %q, want the session footer pinned last", last)
	}
}

func TestRenderLongTranscriptBounded(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 30})
	var sb strings.Builder
	sb.WriteString("# Long report\n\n")
	for i := 0; i < 300; i++ {
		sb.WriteString("- item with **bold** and `code` and a [link](https://example.com/x)\n")
	}
	sb.WriteString("\n| a | b |\n|---|---|\n| 1 | 2 |\n")
	s.Apply(AssistantDelta{Text: sb.String()})
	s.Apply(AssistantDone{})
	start := time.Now()
	out := Render(s)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("render took %v for a long response, want bounded work", elapsed)
	}
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 80 {
			t.Errorf("line %d overflows: %d cells", i, CellWidth(line))
			break
		}
	}
}

// A tab-indented fence is the ordinary shape of a Go or Rust snippet, and it
// broke the whole frame: the code line was measured with its tab counted as a
// zero-width cell, then the styling pass expanded that tab to four spaces
// while the block was padded, so the row landed four cells past the budget.
//
// A row wider than the terminal is not merely ugly — the terminal soft-wraps
// it, so the frame becomes taller than the viewport. The terminal scrolls,
// pinned chrome drifts out of place, and stale rows stay on screen. That is
// the ghosted, misaligned screen, so the contract pinned here is the one that
// matters to the user: no rendered row is ever wider than it was measured for.
func TestCodeFenceTabsNeverWidenTheFrame(t *testing.T) {
	// Tabs at two depths, so both a leading and an interior tab are covered.
	const src = "Here is the code:\n\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n\tif ok {\n\t\tfmt.Println(\"deep\")\n\t}\n}\n```\n\nDone.\n"

	for _, width := range []int{40, 44, 60, 80, 120} {
		out := RenderMarkdownWrapped(src, forcedPalette(), width)
		for i, line := range strings.Split(out, "\n") {
			if got := CellWidth(line); got > width {
				t.Errorf("content width %d: row %d is %d cells, over budget: %q",
					width, i, got, stripForTest(t, line))
			}
		}
	}

	for _, size := range [][2]int{{44, 14}, {60, 20}, {80, 24}, {120, 30}} {
		width, height := size[0], size[1]
		for i, line := range strings.Split(renderAssistant(t, src, width, height), "\n") {
			if got := CellWidth(line); got > width {
				t.Errorf("terminal %dx%d: row %d is %d cells, wider than the terminal: %q",
					width, height, i, got, stripForTest(t, line))
			}
		}
	}
}

// The pipeline-wide form of the same contract: whatever the content shape, the
// frame the terminal receives is never wider than the terminal, and is exactly
// as tall as it. This is the guard for the whole render path, not one block —
// a table column, a CJK run, or a tab in tool output can each mis-measure, and
// each one soft-wraps into an extra physical row that scrolls the frame and
// unpins the chrome.
//
// Mini concedes the viewport on purpose (it prints "terminal too small"), so it
// is excluded rather than counted as a failure.
func TestRenderedFrameFitsTerminalExactly(t *testing.T) {
	cases := map[string]string{
		"prose and lists":  "Para.\n\n**Key:**\n* **One**: a bullet long enough to need wrapping at narrow widths\n* **Two**: another\n",
		"fenced code":      "```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n",
		"table":            "| Name | Description | Cost |\n| --- | --- | --- |\n| alpha | the first one | 1 |\n| beta | the second | 22 |\n",
		"wide glyphs":      strings.Repeat("这是一个很长的中文句子。", 20) + "\n",
		"unbroken run":     "See " + strings.Repeat("https://example.com/a/long/path", 8) + " end.\n",
		"nested list":      "0. a\n   1. b\n      2. c\n         3. d\n",
		"tabs in prose":    "col1\tcol2\tcol3\nvalue\tvalue\tvalue\n",
		"deeply indented":  strings.Repeat("    ", 6) + "indented prose that has to wrap somewhere\n",
		"empty and blank":  "\n\n\nafter blanks\n",
		"only a delimiter": "```\n```\n",
	}
	for name, text := range cases {
		for _, size := range [][2]int{{40, 12}, {44, 14}, {60, 20}, {80, 24}, {120, 30}, {200, 50}} {
			width, height := size[0], size[1]
			s := New()
			s.Apply(TerminalResized{Width: width, Height: height})
			s.Apply(UserSubmitted{Text: "show me"})
			s.Apply(AssistantDelta{Text: text})
			s.Apply(AssistantDone{})
			if ComputeLayout(s).Mode == LayoutMini {
				continue
			}
			rows := strings.Split(Render(s), "\n")
			if n := len(rows); n > 0 && rows[n-1] == "" {
				rows = rows[:n-1]
			}
			if len(rows) != height {
				t.Errorf("%s at %dx%d: frame is %d rows, want exactly %d",
					name, width, height, len(rows), height)
			}
			for i, row := range rows {
				if got := CellWidth(row); got > width {
					t.Errorf("%s at %dx%d: row %d is %d cells, wider than the terminal: %q",
						name, width, height, i, got, stripForTest(t, row))
				}
			}
		}
	}
}

// BenchmarkAssistantStreaming measures the full-transcript render a tick or
// a delta pays. Expectation: linear in transcript size — one render pass,
// no reparsing per entry beyond its own lines.
func BenchmarkAssistantStreaming(b *testing.B) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 30})
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("Paragraph with **bold** and a [link](https://example.com).\n\n- one\n- two\n\n")
	}
	s.Apply(AssistantDelta{Text: sb.String()})
	s.Apply(AssistantDone{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Render(s)
	}
}
