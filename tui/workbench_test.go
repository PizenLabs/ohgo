package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Layout tiers pin the responsive contract.

func TestLayoutModesAtThresholds(t *testing.T) {
	cases := []struct {
		width int
		want  LayoutMode
	}{
		{120, LayoutWide},
		{100, LayoutWide},
		{99, LayoutMedium},
		{70, LayoutMedium},
		{69, LayoutNarrow},
		{40, LayoutNarrow},
		{39, LayoutMini},
	}
	for _, tc := range cases {
		s := New()
		s.Apply(TerminalResized{Width: tc.width, Height: 30})
		if got := ComputeLayout(s).Mode; got != tc.want {
			t.Errorf("width %d = %s, want %s", tc.width, got, tc.want)
		}
	}
}

func TestMiniModeAtSmallHeight(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 120, Height: 6})
	if got := ComputeLayout(s).Mode; got != LayoutMini {
		t.Errorf("mode = %s, want mini for a short viewport", got)
	}
	out := Render(s)
	if !strings.Contains(out, "too small") {
		t.Errorf("mini render = %q, want the size notice", out)
	}
	if strings.Contains(out, "▌") {
		t.Errorf("mini render shows a composer cursor that cannot be used: %q", out)
	}
}

func TestMiniPreservesInputText(t *testing.T) {
	s := New()
	s.Apply(ComposerChanged{Text: "keep me", Cursor: 7})
	s.Apply(TerminalResized{Width: 30, Height: 6})
	if out := Render(s); !strings.Contains(out, "keep me") {
		t.Errorf("mini render = %q, want typed text preserved in prose", out)
	}
}

func TestConversationGetsAvailableSpace(t *testing.T) {
	wide := New()
	wide.Apply(TerminalResized{Width: 120, Height: 30})
	narrow := New()
	narrow.Apply(TerminalResized{Width: 60, Height: 30})
	wl, nl := ComputeLayout(wide), ComputeLayout(narrow)
	if nl.ContentWidth >= wl.ContentWidth {
		t.Error("narrow content width must be smaller than wide")
	}
	if nl.ShowDetail {
		t.Error("narrow must collapse secondary metadata before touching the conversation")
	}
	if !wl.ShowDetail {
		t.Error("wide must keep detail")
	}
	if nl.ConversationHeight < wl.ConversationHeight-2 {
		t.Errorf("narrow conversation %d vs wide %d: chrome must shrink, not the transcript",
			nl.ConversationHeight, wl.ConversationHeight)
	}
}

func TestRepeatedResizesKeepHistoryAndInput(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "remember"})
	s.Apply(ComposerChanged{Text: "typing", Cursor: 6})
	for _, size := range [][2]int{{120, 30}, {60, 20}, {140, 40}, {50, 12}} {
		s.Apply(TerminalResized{Width: size[0], Height: size[1]})
	}
	if len(s.Entries) != 1 || s.Entries[0].UserText != "remember" {
		t.Errorf("entries = %+v, want history intact across resizes", s.Entries)
	}
	if s.Input != "typing" {
		t.Errorf("input = %q, want it preserved across resizes", s.Input)
	}
	out := Render(s)
	if !strings.Contains(out, "remember") || !strings.Contains(out, "typing") {
		t.Errorf("render = %q, want both history and input visible", out)
	}
}

func TestResizeNeverStartsWork(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	p.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if p.state.Status.Busy {
		t.Error("a resize started work: sizing must never change runtime state")
	}
	select {
	case event := <-p.events:
		t.Errorf("event = %#v, want no events from a resize", event)
	default:
	}
}

func TestNarrowCollapsesDetailBeforeConversation(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Status.ContextUsed = 50
	s.Status.ContextWindow = 100
	s.Apply(TerminalResized{Width: 60, Height: 24})
	if out := Render(s); strings.Contains(strings.Split(out, "\n")[len(strings.Split(out, "\n"))-2], "context") {
		t.Errorf("narrow footer keeps the budget prefix: %q", out)
	}
	s.Apply(UserSubmitted{Text: "readable"})
	if out := Render(s); !strings.Contains(out, "readable") {
		t.Errorf("narrow render = %q, want the conversation kept over metadata", out)
	}
}

func TestLongMessageWrapsWithoutOverflow(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 60, Height: 30})
	s.Apply(UserSubmitted{Text: strings.Repeat("word ", 100)})
	for i, line := range strings.Split(Render(s), "\n") {
		if CellWidth(line) > 62 {
			t.Errorf("line %d = %d cells, want no horizontal overflow", i, CellWidth(line))
		}
	}
}

func TestCJKMessageWrapsByCells(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 40, Height: 30})
	s.Apply(UserSubmitted{Text: strings.Repeat("日本語テスト", 20)})
	for i, line := range strings.Split(Render(s), "\n") {
		if CellWidth(line) > 42 {
			t.Errorf("line %d = %d cells, want CJK folded by cells", i, CellWidth(line))
		}
	}
}

func TestEmptyMessageRendersWithoutPanic(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: ""})
	s.Apply(AssistantDelta{Text: ""})
	if out := Render(s); !strings.Contains(out, "ohgo") {
		t.Errorf("render = %q, want chrome intact for empty content", out)
	}
}

func TestToolOutputBoundedAndWrapped(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 70, Height: 40})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "bash", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: strings.Repeat("x", 20000), At: now})
	// Collapsed stays compact: a 20k result must not flood the transcript.
	collapsed := Render(s)
	if strings.Contains(collapsed, strings.Repeat("x", 1000)) {
		t.Errorf("collapsed render dumped the raw output instead of bounding it")
	}
	// Expanded still bounds: the truncation marker survives the toggle.
	s.Apply(ToolToggled{ID: "c1"})
	out := Render(s)
	if !strings.Contains(out, "truncated") {
		t.Errorf("render lacks the truncation marker for a 20k result")
	}
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 72 {
			t.Errorf("line %d overflows: %d cells", i, CellWidth(line))
		}
	}
}

func TestStreamingWhileScrolledPinsViewport(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 10})
	for i := 0; i < 10; i++ {
		s.Apply(Notice{Text: "older"})
	}
	s.Apply(ScrollMoved{Delta: 4})
	before := s.Scroll
	s.Apply(AssistantDelta{Text: "streamed\nchunk"})
	if s.Scroll <= before {
		t.Errorf("scroll = %d, want it pinned (was %d) while unfollowed", s.Scroll, before)
	}
	if out := Render(s); !strings.Contains(out, "lines above") {
		t.Errorf("render lost the scroll indicator during streaming: %q", out)
	}
}

func TestStreamingWhileFollowingTracksEdge(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 10})
	s.Apply(AssistantDelta{Text: "hello"})
	s.Apply(AssistantDelta{Text: " world"})
	if !s.Follow || s.Scroll != 0 {
		t.Errorf("scroll = %d follow = %v, want the live edge tracked", s.Scroll, s.Follow)
	}
	if out := Render(s); !strings.Contains(out, "hello world") {
		t.Errorf("render = %q, want streamed chunks concatenated in order", out)
	}
}

func TestComposerPersistsAcrossPicker(t *testing.T) {
	s := New()
	s.Apply(ComposerChanged{Text: "draft", Cursor: 5})
	s.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: []PickerItem{{Title: "a/1"}}})
	if s.Input != "draft" {
		t.Errorf("input = %q, want the draft preserved in state under the overlay", s.Input)
	}
	if out := Render(s); strings.Contains(out, "draft") {
		t.Errorf("render = %q, want the composer hidden behind the picker", out)
	}
	s.Apply(PickerClosed{})
	if s.Input != "draft" {
		t.Errorf("input = %q, want the draft restored after dismiss", s.Input)
	}
	if out := Render(s); !strings.Contains(out, "draft") {
		t.Errorf("render = %q, want the draft visible again", out)
	}
}

func TestApprovalCancelAndResizeNeverApprove(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ApprovalAsked{ToolName: "bash", Summary: "rm -rf /tmp/x", Choices: []string{"Yes", "No"}})
	if got := p.state.Pending.Selected(); got != "No" {
		t.Errorf("default = %q, want the conservative choice", got)
	}
	p.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if p.state.Pending == nil {
		t.Fatal("a resize dismissed the approval: sizing must keep the dialog")
	}
	if got := p.state.Pending.Selected(); got != "No" {
		t.Errorf("default after resize = %q, want it still conservative", got)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Pending != nil {
		t.Error("esc must dismiss the approval")
	}
}

func TestApprovalShowsPreviewAndShortcuts(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ApprovalAsked{ToolName: "bash", Summary: "run ls", Reason: "lists files", Choices: []string{"Yes", "No"}})
	out := Render(s)
	for _, want := range []string{"bash", "run ls", "default", "esc"} {
		if !strings.Contains(out, want) {
			t.Errorf("approval = %q, want %q shown", out, want)
		}
	}
}

func TestCommandPaletteDiscoversAndRuns(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "commands"}.runOn(p)
	if p.state.HelpModal == nil {
		t.Fatalf("help modal = %+v, want /commands to open the command & help center", p.state.HelpModal)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("the")})
	found := false
	for _, item := range p.state.HelpModal.ActiveItems() {
		if item.Name == "theme" {
			found = true
		}
	}
	if !found {
		t.Errorf("filtered = %+v, want filtering to find /theme", p.state.HelpModal.ActiveItems())
	}
}

func TestSlashTabAcceptsInlinePalette(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "/mod", Cursor: 4})
	keyPress(p, tea.KeyMsg{Type: tea.KeyTab})
	if p.state.Input != "/model " {
		t.Errorf("input = %q, want tab to accept the highlighted inline command", p.state.Input)
	}
	if p.state.Picker != nil {
		t.Errorf("picker = %+v, want tab to accept inline rather than open a modal", p.state.Picker)
	}
}

func TestSlashTabOpensPaletteOffTrigger(t *testing.T) {
	// With the palette dismissed (no trigger word under the cursor), tab on a
	// slash line still reaches the searchable command modal.
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "/model ", Cursor: 7})
	keyPress(p, tea.KeyMsg{Type: tea.KeyTab})
	if p.state.HelpModal == nil {
		t.Errorf("help modal = %+v, want tab off-trigger to open the command & help center", p.state.HelpModal)
	}
}

func TestEnrichedModelPickerMarksActive(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetModelSwitcher(func(id string) error { return nil }, func() string { return "b/2" })
	p.SetModelOptionLister(func() []ModelOption {
		return []ModelOption{
			{ID: "a/1", Title: "A (a/1)", Detail: "128k ctx · remote"},
			{ID: "b/2", Title: "B (b/2)", Detail: "local"},
		}
	})
	Command{Name: "model"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("no picker")
	}
	visible := p.state.Picker.Visible()
	if len(visible) != 2 {
		t.Fatalf("visible = %+v, want both options", visible)
	}
	if !visible[1].Active {
		t.Errorf("active = %+v, want the current model flagged, not title-mangled", visible[1])
	}
	if visible[0].Active {
		t.Errorf("active leaked onto %+v", visible[0])
	}
	// The marker renders in the row, not in the stored title: titles stay
	// clean ids while the workbench shows the ●.
	p.state.Apply(TerminalResized{Width: 120, Height: 30})
	if out := Render(p.state); !strings.Contains(out, "●") {
		t.Errorf("render lacks the active-model marker: %q", out)
	}
	if !strings.Contains(visible[0].Detail, "ctx") {
		t.Errorf("detail = %q, want context shown when known", visible[0].Detail)
	}
}

func TestRuntimeDerivation(t *testing.T) {
	s := New()
	if got := DeriveRuntime(s); got != RuntimeIdle {
		t.Errorf("runtime = %s, want idle", got)
	}
	s.Apply(UserSubmitted{Text: "go"})
	if got := DeriveRuntime(s); got != RuntimeWorking {
		t.Errorf("runtime = %s, want working while busy with no output", got)
	}
	s.Apply(AssistantDelta{Text: "hi"})
	if got := DeriveRuntime(s); got != RuntimeStreaming {
		t.Errorf("runtime = %s, want streaming once text lands", got)
	}
	s.Apply(ApprovalAsked{ToolName: "bash", Choices: []string{"Yes", "No"}})
	if got := DeriveRuntime(s); got != RuntimeAwaitingApproval {
		t.Errorf("runtime = %s, want awaiting approval over streaming", got)
	}
}

func TestStatusShowsWorkspaceMeta(t *testing.T) {
	// No middle status line: workspace meta lives once in the bottom hint
	// bar (session, measured cost, path), not in a third copy between the
	// transcript and the composer.
	s := New()
	s.Apply(WorkspaceUpdated{Cwd: "/home/user/project", HasCwd: true, Session: "#3", HasSession: true, UsageText: "1.2k tok", HasUsage: true})
	s.Apply(TerminalResized{Width: 120, Height: 24})
	out := Render(s)
	for _, want := range []string{"project", "#3", "1.2k"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer = %q, want %q shown", out, want)
		}
	}
}

func TestHeaderShowsPhaseInWords(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(UserSubmitted{Text: "go"})
	out := Render(s)
	if !strings.Contains(strings.Split(out, "\n")[0], "working") {
		t.Errorf("header = %q, want the phase in words, not color alone", out)
	}
}

func TestDialogBoundedToViewport(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 60, Height: 12})
	s.Apply(ApprovalAsked{ToolName: "bash", Summary: strings.Repeat("detail ", 100), Choices: []string{"Yes", "No"}})
	lines := strings.Split(Render(s), "\n")
	if len(lines) > 14 {
		t.Errorf("rendered %d lines in a 12-row viewport: the dialog must bound itself", len(lines))
	}
}
