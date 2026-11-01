package tui

// /copy contracts.
//
// Each test defends one externally visible choice: which content a copy
// selects, that the whole-answer and context flags select what they promise,
// and that a missing transport or an empty transcript is reported instead of
// looking like a successful copy.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/charmbracelet/x/ansi"
)

func TestCopySmartSelectsLargestCodeBlock(t *testing.T) {
	response := "Here are two examples.\n\n" +
		"```go\nsmall\n```\n\n" +
		"```go\nline one\nline two\nline three\n```\n"

	got, kind, ok := CopyPayload(CopyModeSmart, response)
	if !ok {
		t.Fatal("smart copy found nothing")
	}
	if kind != CopyKindCode {
		t.Fatalf("kind = %q, want %q", kind, CopyKindCode)
	}
	want := "line one\nline two\nline three"
	if got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

// Equal-sized blocks are ambiguous, so the latest one wins: it is the block the
// user just watched the model finish.
func TestCopySmartPrefersLastCodeBlockOnTie(t *testing.T) {
	response := "```go\nfirst\n```\n\n```go\nsecond\n```"
	got, _, ok := CopyPayload(CopyModeSmart, response)
	if !ok {
		t.Fatal("smart copy found nothing")
	}
	if got != "second" {
		t.Fatalf("payload = %q, want the later block", got)
	}
}

// A unified diff that was not fenced is still a diff, not prose: copying the
// explanation with it would paste a sentence into a patch.
func TestCopySmartExtractsUnfencedUnifiedDiff(t *testing.T) {
	response := "Here is the change.\n\n" +
		"--- a/main.go\n" +
		"+++ b/main.go\n" +
		"@@ -1,2 +1,2 @@\n" +
		"-old\n" +
		"+new\n" +
		" context\n\n" +
		"That is all."

	got, kind, ok := CopyPayload(CopyModeSmart, response)
	if !ok {
		t.Fatal("smart copy found nothing")
	}
	if kind != CopyKindDiff {
		t.Fatalf("kind = %q, want %q", kind, CopyKindDiff)
	}
	want := "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n-old\n+new\n context"
	if got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestCopySmartFallsBackToWholeResponse(t *testing.T) {
	response := "A plain explanation with no code."
	got, kind, ok := CopyPayload(CopyModeSmart, response)
	if !ok {
		t.Fatal("smart copy found nothing")
	}
	if kind != CopyKindResponse || got != response {
		t.Fatalf("payload = %q (kind %q), want the whole response", got, kind)
	}
}

// An edit turn whose prose does not repeat the change still has a copyable
// diff: the tool reported it, and copying "Done." would be useless.
func TestCopySmartFallsBackToTurnDiff(t *testing.T) {
	const diff = "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new"
	got, kind, ok := CopyPayload(CopyModeSmart, "Done.", diff)
	if !ok {
		t.Fatal("smart copy found nothing")
	}
	if kind != CopyKindDiff || got != diff {
		t.Fatalf("payload = %q (kind %q), want the turn diff", got, kind)
	}
}

// A completed mutation call records its diff on the block without putting it
// into the rendered outcome, so /copy can use it after an edit turn that only
// says "Done."
func TestToolFinishedRecordsDiffForCopy(t *testing.T) {
	const diff = "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b"
	state := New()
	state.Apply(UserSubmitted{Text: "fix it"})
	state.Apply(ToolStarted{ID: "1", Name: "edit", Summary: "edit x"})
	state.Apply(ToolFinished{
		ID: "1", Text: "Replaced 1 occurrence.", Diff: diff, At: time.Now(),
	})

	tool := state.tool("1")
	if tool == nil || tool.Diff != diff {
		t.Fatalf("tool diff = %+v, want %q", tool, diff)
	}
	response, editDiff, ok := latestResponseEntries(state.Entries)
	if !ok {
		t.Fatal("a tool-only turn produced nothing to copy")
	}
	if response != "" || editDiff != diff {
		t.Fatalf("response = %q, diff = %q; want only the diff", response, editDiff)
	}
}

func TestMutationDiffReadsToolDetails(t *testing.T) {
	const diff = "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b"
	if got := MutationDiff(map[string]any{"diff": diff}); got != diff {
		t.Fatalf("map details = %q, want %q", got, diff)
	}
	if got := MutationDiff(agent.MutationEvidence{Diff: diff}); got != diff {
		t.Fatalf("evidence details = %q, want %q", got, diff)
	}
	if got := MutationDiff(nil); got != "" {
		t.Fatalf("nil details = %q, want empty", got)
	}
}

func TestCopyAllKeepsExplanationAndEveryFence(t *testing.T) {
	response := "Explanation.\n\n```go\nfunc main() {}\n```\n\nDone."
	got, kind, ok := CopyPayload(CopyModeAll, response)
	if !ok {
		t.Fatal("all copy found nothing")
	}
	if kind != CopyKindResponse || got != response {
		t.Fatalf("payload = %q (kind %q), want the exact response", got, kind)
	}
}

func TestParseCopyMode(t *testing.T) {
	tests := []struct {
		args []string
		want CopyMode
		ok   bool
	}{
		{nil, CopyModeSmart, true},
		{[]string{"-a"}, CopyModeAll, true},
		{[]string{"--all"}, CopyModeAll, true},
		{[]string{"-all"}, CopyModeAll, true},
		{[]string{"-c"}, CopyModeContext, true},
		{[]string{"--context"}, CopyModeContext, true},
		{[]string{"-context"}, CopyModeContext, true},
		{[]string{"-a", "-c"}, CopyModeSmart, false},
		{[]string{"-c", "-a"}, CopyModeSmart, false},
		{[]string{"--everything"}, CopyModeSmart, false},
	}
	for _, tt := range tests {
		got, err := ParseCopyMode(tt.args)
		if tt.ok && err != nil {
			t.Errorf("ParseCopyMode(%v) error = %v", tt.args, err)
			continue
		}
		if !tt.ok && err == nil {
			t.Errorf("ParseCopyMode(%v) accepted an invalid combination", tt.args)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseCopyMode(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// The latest answer is everything the assistant produced after the last user
// turn; an older answer must never leak into it.
// Typing the command reveals its flags with their descriptions: a user who
// only saw "/copy" had no way to discover -a or -c.
func TestCopyHelpRowRunsItsFlag(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.termOut = &bytes.Buffer{}
	var copied string
	p.clipboard = func(text string) string {
		copied = text
		return OSC52Sequence(text)
	}
	const response = "Explanation.\n\n```go\nfunc main() {}\n```\n"
	p.state.Entries = []Entry{
		{Kind: EntryUser, UserText: "show me"},
		{Kind: EntryAssistant, AssistantText: response},
	}

	view := NewHelpCenter(Commands)
	items := view.ActiveItems()
	index := -1
	for i, item := range items {
		if item.Name == "copy" && item.Usage == "-a" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("help center does not list /copy -a")
	}
	view.Command.Cursor = index
	p.state.HelpModal = &view

	_, _ = p.chooseHelpRow()
	if copied != response {
		t.Fatalf("copied = %q, want the -a form", copied)
	}
}

func TestCopyPaletteShowsFlagSuggestions(t *testing.T) {
	out := ansi.Strip(RenderSlashPalette("/copy", 0, 100))
	for _, want := range []string{
		"/copy",
		"/copy -a",
		"/copy -c",
		"entire latest answer",
		"active session context",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("palette missing %q:\n%s", want, out)
		}
	}
}

// Choosing a flag row completes the line instead of dispatching the bare
// command, which would silently perform the wrong copy.
func TestCopyPaletteSubcommandCompletes(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "/copy", Cursor: len("/copy")})
	p.state.PaletteCursor = 1 // "/copy -a"
	_, _ = p.acceptPalette()
	if p.state.Input != "/copy -a " {
		t.Fatalf("input = %q, want the -a form completed", p.state.Input)
	}
}

// Enter on the parent row still runs the smart default, so /copy + Enter keeps
// its five-character efficiency.
func TestCopyPaletteParentStillRuns(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.termOut = &bytes.Buffer{}
	var copied string
	p.clipboard = func(text string) string {
		copied = text
		return OSC52Sequence(text)
	}
	p.state.Entries = []Entry{
		{Kind: EntryUser, UserText: "show me"},
		{Kind: EntryAssistant, AssistantText: "```go\nfunc main() {}\n```"},
	}
	p.state.Apply(ComposerChanged{Text: "/copy", Cursor: len("/copy")})
	p.state.PaletteCursor = 0
	_, _ = p.acceptPalette()
	if copied != "func main() {}" {
		t.Fatalf("copied = %q, want the smart default", copied)
	}
}

func TestLatestResponseUsesOnlyTheLastTurn(t *testing.T) {
	messages := []agent.Message{
		agent.UserMessage{Text: "first question"},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "first answer"}}}},
		agent.UserMessage{Text: "second question"},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "look at this"}}}},
		agent.ToolResultMessage{ToolCallID: "1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "file"}}},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "second answer"}}}},
	}
	got, ok := LatestResponse(messages)
	if !ok {
		t.Fatal("LatestResponse found nothing")
	}
	if strings.Contains(got, "first answer") {
		t.Fatalf("payload leaked an older answer: %q", got)
	}
	if !strings.Contains(got, "look at this") || !strings.Contains(got, "second answer") {
		t.Fatalf("payload = %q, want both assistant texts from the last turn", got)
	}
}

func TestLatestResponseWithoutUserTurnUsesLastAssistant(t *testing.T) {
	messages := []agent.Message{
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "older"}}}},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "latest"}}}},
	}
	got, ok := LatestResponse(messages)
	if !ok || got != "latest" {
		t.Fatalf("LatestResponse = %q, %v; want latest", got, ok)
	}
}

func TestContextTranscriptCarriesPromptCallsAndResults(t *testing.T) {
	messages := []agent.Message{
		agent.UserMessage{Text: "please fix @main.go"},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "I will edit it."},
			ai.ToolCall{ID: "1", Name: "edit", Arguments: map[string]any{"path": "main.go"}},
		}}},
		agent.ToolResultMessage{
			ToolCallID: "1",
			ToolName:   "edit",
			Content:    []ai.ContentBlock{ai.TextContent{Text: "Replaced 1 occurrence."}},
		},
	}
	got := ContextTranscript(messages)
	for _, want := range []string{
		"## User", "please fix @main.go",
		"## Assistant", "I will edit it.",
		"### Tool call: edit", `"path": "main.go"`,
		"## Tool result: edit", "Replaced 1 occurrence.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context missing %q:\n%s", want, got)
		}
	}
}

// The command path selects the smart payload from the live transcript and
// reports success through the existing transient confirmation, never by adding
// a transcript block.
func TestCopyCommandCopiesSmartPayload(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var out bytes.Buffer
	p.termOut = &out
	var copied string
	p.clipboard = func(text string) string {
		copied = text
		return OSC52Sequence(text)
	}
	p.state.Entries = []Entry{
		{Kind: EntryUser, UserText: "show me"},
		{Kind: EntryAssistant, AssistantText: "Here:\n\n```go\nfunc main() {}\n```\n"},
	}

	if cmd := p.runCommand(Command{Name: "copy"}); cmd == nil {
		t.Fatal("/copy returned no expiry command")
	}
	if copied != "func main() {}" {
		t.Fatalf("copied = %q, want the code body", copied)
	}
	if p.state.CopyNotice != copyNoticeText {
		t.Fatalf("notice = %q, want %q", p.state.CopyNotice, copyNoticeText)
	}
	if out.Len() == 0 {
		t.Fatal("/copy wrote no clipboard bytes")
	}
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			t.Fatal("a successful copy entered the transcript")
		}
	}
}

func TestCopyCommandUsesTurnDiffWhenProseHasNone(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.termOut = &bytes.Buffer{}
	var copied string
	p.clipboard = func(text string) string {
		copied = text
		return OSC52Sequence(text)
	}
	const diff = "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new"
	p.state.Entries = []Entry{
		{Kind: EntryUser, UserText: "fix it"},
		{Kind: EntryTool, Tool: &ToolEntry{ID: "1", Name: "edit", Diff: diff}},
		{Kind: EntryAssistant, AssistantText: "Done."},
	}

	p.runCommand(Command{Name: "copy"})
	if copied != diff {
		t.Fatalf("copied = %q, want the turn diff", copied)
	}
}

func TestCopyCommandAllKeepsFences(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.termOut = &bytes.Buffer{}
	var copied string
	p.clipboard = func(text string) string {
		copied = text
		return OSC52Sequence(text)
	}
	const response = "Explanation.\n\n```go\nfunc main() {}\n```\n"
	p.state.Entries = []Entry{
		{Kind: EntryUser, UserText: "show me"},
		{Kind: EntryAssistant, AssistantText: response},
	}

	p.runCommand(Command{Name: "copy", Args: []string{"-a"}})
	if copied != response {
		t.Fatalf("copied = %q, want the whole response", copied)
	}
}

func TestCopyCommandContextCopiesHistory(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.termOut = &bytes.Buffer{}
	var copied string
	p.clipboard = func(text string) string {
		copied = text
		return OSC52Sequence(text)
	}
	p.history = []agent.Message{
		agent.UserMessage{Text: "the prompt"},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "the answer"}}}},
	}

	p.runCommand(Command{Name: "copy", Args: []string{"-c"}})
	if !strings.Contains(copied, "the prompt") || !strings.Contains(copied, "the answer") {
		t.Fatalf("context payload = %q, want the conversation", copied)
	}
}

func TestCopyCommandUnavailableClipboardIsReported(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.clipboard = nil
	p.state.Entries = []Entry{
		{Kind: EntryUser, UserText: "show me"},
		{Kind: EntryAssistant, AssistantText: "answer"},
	}

	p.runCommand(Command{Name: "copy"})
	if p.state.CopyNotice != "" {
		t.Fatal("a disabled clipboard reported success")
	}
	last := p.state.Entries[len(p.state.Entries)-1]
	if last.Kind != EntryNotice || !strings.Contains(last.Notice, "clipboard") {
		t.Fatalf("last entry = %+v, want a clipboard notice", last)
	}
}

func TestCopyCommandWithoutResponseIsReported(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.termOut = &bytes.Buffer{}
	var called bool
	p.clipboard = func(text string) string {
		called = true
		return OSC52Sequence(text)
	}

	p.runCommand(Command{Name: "copy"})
	if called {
		t.Fatal("an empty transcript reached the clipboard")
	}
	last := p.state.Entries[len(p.state.Entries)-1]
	if last.Kind != EntryNotice || !strings.Contains(last.Notice, "no response") {
		t.Fatalf("last entry = %+v, want a no-response notice", last)
	}
}
