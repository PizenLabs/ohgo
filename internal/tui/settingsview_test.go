package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// /settings is the discoverable control for the task rail. Toggling it takes
// effect immediately and persists through the host seam without closing the
// surface, so the user can see the consequence of the choice.
func TestSettingsCommandTogglesTaskRailAndPersists(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 140, Height: 40})
	p.state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "ship the parser",
		Items: []TaskItem{{State: taskStateActive, Label: "run the suite"}},
	}})
	if !ComputeWorkspaceLayout(p.state).Sidebar {
		t.Fatal("the rail did not appear for an open task with a todo plan")
	}

	var persisted []bool
	p.SetTaskRailChangedHandler(func(enabled bool) { persisted = append(persisted, enabled) })
	p.runCommand(Command{Name: "settings", Raw: "/settings"})
	if p.state.SettingsModal == nil {
		t.Fatal("/settings did not open the preferences surface")
	}
	if ComputeWorkspaceLayout(p.state).Sidebar {
		t.Fatal("the rail stayed beside the modal it configures")
	}

	out := ansi.Strip(Render(p.state))
	for _, want := range []string{"SETTINGS", "INTERFACE", "Task rail", "[on]"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings surface missing %q:\n%s", want, out)
		}
	}

	keyPress(p, tea.KeyMsg{Type: tea.KeySpace})
	if !p.state.TaskRailDisabled {
		t.Fatal("space did not disable the task rail")
	}
	if len(persisted) != 1 || persisted[0] {
		t.Fatalf("persisted = %v, want one disabled value", persisted)
	}
	if out := ansi.Strip(Render(p.state)); !strings.Contains(out, "[off]") {
		t.Fatalf("settings surface did not reflect the disabled state:\n%s", out)
	}

	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.SettingsModal != nil {
		t.Fatal("esc did not close the settings surface")
	}
	if ComputeWorkspaceLayout(p.state).Sidebar {
		t.Fatal("the rail reappeared after being disabled")
	}
	if out := ansi.Strip(Render(p.state)); !strings.Contains(out, "ship the parser") {
		t.Fatalf("the task vanished from the transcript after disabling the rail:\n%s", out)
	}
}

// The settings dialog keeps the shared modal geometry: every rendered line
// stays inside the requested width, and the row states its value as a word.
func TestSettingsModalFitsWidthAndStatesValue(t *testing.T) {
	for _, width := range []int{44, 74} {
		out := RenderSettingsModal(SettingsState{}, true, DarkPalette(), width, 16)
		plain := ansi.Strip(out)
		for _, line := range strings.Split(plain, "\n") {
			if got := CellWidth(line); got > width {
				t.Fatalf("width %d: line %q = %d cells", width, line, got)
			}
		}
		for _, want := range []string{"SETTINGS", "INTERFACE", "Task rail", "[on]"} {
			if !strings.Contains(plain, want) {
				t.Errorf("width %d: settings surface missing %q:\n%s", width, want, plain)
			}
		}
	}
}

// The settings row is a truthful projection of the preference: an explicit
// startup value is applied without calling the persistence handler, and the
// toggle can re-enable a previously disabled rail.
func TestTaskRailPreferenceAppliesWithoutWritingBack(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 140, Height: 40})
	var writes int
	p.SetTaskRailChangedHandler(func(bool) { writes++ })
	p.SetTaskRailEnabled(false)
	if !p.state.TaskRailDisabled {
		t.Fatal("startup preference was not applied")
	}
	if writes != 0 {
		t.Fatalf("startup preference wrote back %d times", writes)
	}
	p.state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "ship the parser",
		Items: []TaskItem{{State: taskStateActive, Label: "run the suite"}},
	}})
	if ComputeWorkspaceLayout(p.state).Sidebar {
		t.Fatal("a disabled preference still opened the rail")
	}

	p.state.Apply(SettingsOpened{})
	keyPress(p, tea.KeyMsg{Type: tea.KeySpace})
	if p.state.TaskRailDisabled {
		t.Fatal("space did not re-enable the task rail")
	}
	if writes != 1 {
		t.Fatalf("toggle wrote %d times, want once", writes)
	}
	p.state.Apply(SettingsClosed{})
	if !ComputeWorkspaceLayout(p.state).Sidebar {
		t.Fatal("the rail did not return after re-enabling the preference")
	}
}

// The command registry is the discovery source, so /settings must be there for
// the slash palette and help center to teach it.
func TestSettingsCommandIsRegistered(t *testing.T) {
	for _, spec := range Commands {
		if spec.Name == "settings" {
			if spec.Category != CommandGroupControl {
				t.Fatalf("settings category = %q, want control", spec.Category)
			}
			return
		}
	}
	t.Fatal("/settings is missing from the command registry")
}
