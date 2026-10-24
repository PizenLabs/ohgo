package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// An uncertain request does not reach the model until the human chooses an
// authority. Choosing BUILD is an explicit authority decision, not a model
// inference from the classifier's conservative READ fallback.
func TestAmbiguousWorkspaceChoiceRunsOnlyAfterHumanSelection(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	current := "BUILD"
	prepared := 0
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return current },
		Switch: func(name string) error {
			current = strings.ToUpper(name)
			p.state.Apply(WorkspaceUpdated{Workspace: current, HasWorkspace: true})
			return nil
		},
		PrepareTurn: func(string) TurnAuthority {
			prepared++
			return TurnAuthority{Active: current, Blocked: true, Reason: "workspace choice was not applied"}
		},
		Predict: func(string) (TurnTransition, bool) {
			return TurnTransition{Kind: TransitionKindAmbiguous, Reason: "ambiguous intent"}, true
		},
	})
	p.state.Apply(WorkspaceUpdated{Workspace: current, HasWorkspace: true})

	cmd := typeAndEnter(p, "check this project")
	if cmd != nil || p.state.Permissions == nil {
		t.Fatal("ambiguous request did not wait in the centered authority selector")
	}
	if p.state.Permissions.Request != "check this project" {
		t.Fatalf("pending request = %q, want the original request", p.state.Permissions.Request)
	}
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	view := stripANSI(Render(p.state))
	for _, want := range []string{"Choose Workspace For This Request", "ambiguous intent", "Request: check this project"} {
		if !strings.Contains(view, want) {
			t.Errorf("authority picker missing %q:\n%s", want, view)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("provider requests before selection = %d, want 0", got)
	}

	cmd = press(p, tea.KeyMsg{Type: tea.KeyEnter}) // explicitly reaffirm BUILD
	if cmd == nil {
		t.Fatal("explicit BUILD selection did not release the request")
	}
	cmd()
	collectTurn(t, p)
	if current != "BUILD" || p.state.Status.Workspace != "BUILD" {
		t.Fatalf("workspace owner/UI = %s/%s, want BUILD/BUILD", current, p.state.Status.Workspace)
	}
	if prepared != 0 {
		t.Errorf("explicit workspace decision was reclassified %d times", prepared)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("provider requests after selection = %d, want exactly 1", got)
	}
}

func TestCancellingAmbiguousWorkspaceChoicePreservesRequestWithoutExecution(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	p.state.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	p.openAuthorityChoice("check this project", "ambiguous intent")
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Permissions != nil {
		t.Fatal("Esc left the authority selector open")
	}
	if p.state.Input != "check this project" {
		t.Errorf("input = %q, want the cancelled request restored", p.state.Input)
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Errorf("provider requests = %d, want 0 after cancellation", got)
	}
}

func TestAmbiguousWorkspaceChoiceSynchronizesAuthorityAndUI(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	current := "BUILD"
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return current },
		Switch: func(name string) error {
			current = strings.ToUpper(name)
			p.state.Apply(WorkspaceUpdated{Workspace: current, HasWorkspace: true})
			return nil
		},
	})
	p.state.Apply(WorkspaceUpdated{Workspace: current, HasWorkspace: true})
	p.openAuthorityChoice("check this project", "ambiguous intent")
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp}) // select ASK, not the current BUILD
	cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("explicit ASK selection did not release the request")
	}
	cmd()
	collectTurn(t, p)
	if current != "ASK" || p.state.Status.Workspace != "ASK" {
		t.Fatalf("workspace owner/UI = %s/%s, want ASK/ASK", current, p.state.Status.Workspace)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("provider requests = %d, want exactly 1", got)
	}
}

// The selector must mark exactly the highlighted row and exactly the
// authority in force: a badge on the wrong row would tell the user they are
// somewhere they are not.
func TestPermissionsModalMarksSelectionAndCurrent(t *testing.T) {
	out := stripANSI(RenderPermissionsModal(1, ModeBuild, 72))
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "ASK (Read-Only)"):
			rows["ASK"] = line
		case strings.Contains(line, "BUILD (Workspace Authority)"):
			rows["BUILD"] = line
		case strings.Contains(line, "FUZZ (Autonomous Assistance)"):
			rows["FUZZ"] = line
		}
	}
	build, ok := rows["BUILD"]
	if !ok {
		t.Fatalf("BUILD row missing:\n%s", out)
	}
	if !strings.Contains(build, "›") || !strings.Contains(build, "2.") {
		t.Errorf("selected row = %q, want the › indicator on 2. BUILD", build)
	}
	if !strings.Contains(build, "(current)") {
		t.Errorf("BUILD row = %q, want the (current) badge on the authority in force", build)
	}
	for _, name := range []string{"ASK", "FUZZ"} {
		row, ok := rows[name]
		if !ok {
			t.Fatalf("%s row missing:\n%s", name, out)
		}
		if strings.Contains(row, "›") {
			t.Errorf("%s row = %q, want no indicator on an unselected row", name, row)
		}
		if strings.Contains(row, "(current)") {
			t.Errorf("%s row = %q, want no (current) badge off BUILD", name, row)
		}
	}
}

// No (current) badge belongs on the dialog when the authority is unknown: the
// selector must not invent one.
func TestPermissionsModalOmitsCurrentWhenUnknown(t *testing.T) {
	out := stripANSI(RenderPermissionsModal(0, "", 72))
	if strings.Contains(out, "(current)") {
		t.Errorf("modal invented a current authority:\n%s", out)
	}
}

// Wrapping is a cycle: overshooting either end lands on the other authority
// rather than stranding the cursor.
func TestPermissionsSelectionWraps(t *testing.T) {
	last := len(AuthorityOptions) - 1
	state := &PermissionsState{Cursor: last}
	state.Move(1)
	if state.Cursor != 0 {
		t.Errorf("Move(+1) from last = %d, want wrap to 0", state.Cursor)
	}
	state.Move(-1)
	if state.Cursor != last {
		t.Errorf("Move(-1) from first = %d, want wrap to %d", state.Cursor, last)
	}
}

// Keys drive the highlight and the modal's open/close contract through Update.
func TestPermissionsKeysMoveAndWrap(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(WorkspaceUpdated{Workspace: "ASK", HasWorkspace: true})
	Command{Name: "permissions"}.runOn(p)
	if p.state.Permissions == nil {
		t.Fatal("permissions did not open the selector")
	}
	if p.state.Permissions.Cursor != 0 {
		t.Fatalf("cursor = %d, want it parked on the current ASK row", p.state.Permissions.Cursor)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if got, want := p.state.Permissions.Cursor, len(AuthorityOptions)-1; got != want {
		t.Errorf("up from ASK = %d, want wrap to %d", got, want)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	if p.state.Permissions.Cursor != 0 {
		t.Errorf("down from last = %d, want wrap to 0", p.state.Permissions.Cursor)
	}
	keyPress(p, runes("j"))
	if p.state.Permissions.Cursor != 1 {
		t.Errorf("j = %d, want 1 (vim down)", p.state.Permissions.Cursor)
	}
	keyPress(p, runes("k"))
	if p.state.Permissions.Cursor != 0 {
		t.Errorf("k = %d, want 0 (vim up)", p.state.Permissions.Cursor)
	}
}

// /mode is an alias: it parses, resolves, and opens the same selector.
func TestModeAliasOpensSelector(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	command, ok := ParseCommand("/mode")
	if !ok {
		t.Fatal("/mode did not parse")
	}
	if _, known := LookupCommand(command.Name); !known {
		t.Fatal("/mode is not a known command")
	}
	p.runCommand(command)
	if p.state.Permissions == nil {
		t.Error("/mode did not open the authority selector")
	}
}

// Escape cancels without changing authority and returns focus to the composer.
func TestPermissionsEscapeCancelsWithoutSwitching(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	called := ""
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return "ask" },
		Switch:  func(name string) error { called = name; return nil },
	})
	p.state.Apply(WorkspaceUpdated{Workspace: "ASK", HasWorkspace: true})
	Command{Name: "permissions"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Permissions != nil {
		t.Error("escape must close the selector")
	}
	if called != "" {
		t.Errorf("escape switched authority to %q, want no change", called)
	}
}

// Enter applies the highlighted authority through the owner seam, using the
// lowercase wire name workspace.Parse accepts, and closes the modal.
func TestPermissionsEnterAppliesSelectedMode(t *testing.T) {
	cases := []struct{ current, want string }{
		{"ASK", "build"},
		{"BUILD", "fuzz"},
		{"FUZZ", "ask"},
	}
	for _, tc := range cases {
		p := NewProgram(nil, "test", 0)
		var switched string
		p.SetWorkspaceSwitcher(WorkspaceHooks{
			Current: func() string { return strings.ToLower(tc.current) },
			Switch:  func(name string) error { switched = name; return nil },
		})
		p.state.Apply(WorkspaceUpdated{Workspace: tc.current, HasWorkspace: true})
		Command{Name: "permissions"}.runOn(p)
		keyPress(p, tea.KeyMsg{Type: tea.KeyDown}) // one step off the current row
		keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
		if switched != tc.want {
			t.Errorf("from %s entered %q, want %q", tc.current, switched, tc.want)
		}
		if p.state.Permissions != nil {
			t.Errorf("from %s the selector stayed open after confirming", tc.current)
		}
	}
}

// Space confirms the same way Enter does.
func TestPermissionsSpaceConfirms(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var switched string
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Switch: func(name string) error { switched = name; return nil },
	})
	p.state.Apply(WorkspaceUpdated{Workspace: "ASK", HasWorkspace: true})
	Command{Name: "permissions"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeySpace})
	if switched != "ask" {
		t.Errorf("space confirmed %q, want ask (the parked row)", switched)
	}
	if p.state.Permissions != nil {
		t.Error("space must close the selector")
	}
}

// A modal owns the screen: the composer and its hint bar must not render
// underneath, or a workspace turn could be submitted behind the dialog.
func TestPermissionsOverlayOwnsTheScreen(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	p.state.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	Command{Name: "permissions"}.runOn(p)
	out := stripANSI(Render(p.state))
	if !strings.Contains(out, "Update Model Execution Authority") {
		t.Fatalf("overlay missing the title:\n%s", out)
	}
	if strings.Contains(out, "enter send") {
		t.Errorf("composer hints rendered under the modal:\n%s", out)
	}
}

// The selector refuses to open while a turn runs: one turn must never span two
// authorities.
func TestPermissionsRefusedWhileBusy(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "work"})
	Command{Name: "permissions"}.runOn(p)
	if p.state.Permissions != nil {
		t.Error("the selector opened mid-turn")
	}
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "wait for the turn") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the turn-boundary refusal", p.state.Entries)
	}
}

// Every emitted row fits the box width at any readable size: an overflowing
// row soft-wraps the terminal and breaks the border.
func TestPermissionsModalLinesFitWidth(t *testing.T) {
	for _, width := range []int{120, 80, 60, 44, 30} {
		out := RenderPermissionsModal(2, ModeFuzz, width)
		for _, line := range strings.Split(out, "\n") {
			if got := CellWidth(line); got > width {
				t.Errorf("width %d: line %q is %d cells", width, stripANSI(line), got)
			}
		}
	}
}

// Too-narrow input grows to the readable floor instead of degenerating.
func TestPermissionsModalGrowsToReadableFloor(t *testing.T) {
	out := RenderPermissionsModal(0, ModeAsk, 8)
	plain := stripANSI(out)
	floor := permissionsMinInterior + 2
	for _, line := range strings.Split(out, "\n") {
		if got := CellWidth(line); got > floor {
			t.Errorf("line %q is %d cells, want at most the %d-cell floor", stripANSI(line), got, floor)
		}
	}
	// Every authority still names itself, even if the label yields to the
	// narrow axis.
	for _, opt := range AuthorityOptions {
		if !strings.Contains(plain, string(opt.Mode)) {
			t.Errorf("narrow box lost the %s label:\n%s", opt.Mode, plain)
		}
	}
}

// Wrapping preserves every description word: a description cut by the width
// would hide the authority's contract.
func TestPermissionsModalPreservesDescriptions(t *testing.T) {
	// The box border would otherwise glue itself to the wrapped words and
	// break the contiguous-phrase check.
	border := strings.NewReplacer("│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ")
	out := stripANSI(RenderPermissionsModal(0, ModeAsk, 44))
	hay := strings.Join(strings.Fields(border.Replace(out)), " ")
	for _, opt := range AuthorityOptions {
		want := strings.Join(strings.Fields(opt.Description), " ")
		if !strings.Contains(hay, want) {
			t.Errorf("modal is missing the %s description:\n%s", opt.Mode, out)
		}
	}
}

// Styled rows must close their SGR state on the line that opened it: an
// unterminated escape would bleed the mode accent into the next row.
func TestPermissionsModalLinesCloseTheirANSI(t *testing.T) {
	out := RenderPermissionsModal(1, ModeBuild, 72)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "\x1b") {
			continue
		}
		if !strings.Contains(line, "\x1b[0m") {
			t.Errorf("line opened ANSI without closing it: %q", stripANSI(line))
		}
	}
}

// Advertised commands must be dispatchable, and the /mode alias must stay out
// of discovery: the palette can never show a name the parser rejects, nor hide
// one it accepts.
func TestPermissionsCommandsAreRegistered(t *testing.T) {
	command, ok := ParseCommand("/permissions")
	if !ok {
		t.Fatal("/permissions did not parse")
	}
	if _, known := LookupCommand(command.Name); !known {
		t.Error("/permissions is not in the command registry")
	}
	found := false
	for _, spec := range SlashSuggestions("/permissions") {
		if spec.Name == "permissions" {
			found = true
		}
	}
	if !found {
		t.Error("/permissions missing from the slash suggestions")
	}
	if !strings.Contains(HelpText(), "/permissions") {
		t.Errorf("help omits /permissions:\n%s", HelpText())
	}

	alias, ok := ParseCommand("/mode")
	if !ok {
		t.Fatal("/mode did not parse")
	}
	if _, known := LookupCommand(alias.Name); !known {
		t.Error("/mode is not in the command registry")
	}
	for _, spec := range SlashSuggestions("/mode") {
		if spec.Name == "mode" {
			t.Error("hidden alias /mode leaked into discovery")
		}
	}
	if strings.Contains(HelpText(), "/mode ") {
		t.Errorf("hidden alias /mode leaked into help:\n%s", HelpText())
	}
}
