package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// flowProgram opens the plane on the fixture with a recording Assign hook, so
// a test can assert exactly when (and whether) the authority seam is reached.
func flowProgram(t *testing.T) (*Program, *[]RoleAssignment) {
	t.Helper()
	p := openPlaneProgram(t)
	var calls []RoleAssignment
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return planeFixture().Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			calls = append(calls, update)
			update.Name = "Applied"
			return update, planeFixture().Roles, nil
		},
	})
	return p, &calls
}

// TestFlowModelFirstIsTransactional is the core picker contract: MODEL mode
// runs model -> role -> effort -> apply, and only the explicit Apply reaches
// the authority seam.
func TestFlowModelFirstIsTransactional(t *testing.T) {
	p, calls := flowProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	enter := tea.KeyMsg{Type: tea.KeyEnter}

	p.Update(enter) // choose the model -> role step
	if p.state.Plane.Stage != PlaneStageRole {
		t.Fatalf("stage after model pick = %v, want role selection", p.state.Plane.Stage)
	}
	if len(*calls) != 0 {
		t.Fatal("the model pick persisted a selection")
	}

	p.Update(enter) // choose the role -> effort step
	if p.state.Plane.Stage != PlaneStageEffort {
		t.Fatalf("stage after role pick = %v, want effort selection", p.state.Plane.Stage)
	}
	if len(*calls) != 0 {
		t.Fatal("the role pick persisted a selection")
	}

	p.Update(enter) // apply
	if len(*calls) != 1 {
		t.Fatalf("apply calls = %d, want exactly one", len(*calls))
	}
	got := (*calls)[0]
	if got.Role != "build" {
		t.Errorf("applied role = %q, want the workspace default build", got.Role)
	}
	if got.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("applied model = %q, want the picked model", got.Model)
	}
	if p.state.Plane.Stage != PlaneStageBrowse {
		t.Errorf("stage after apply = %v, want browse", p.state.Plane.Stage)
	}
}

// TestFlowWorkspaceSuppliesTheDefaultRoleOnly proves the workspace influences
// navigation, never assignment: the role cursor opens on the workspace's role,
// but staging a model does not change the target or any role.
func TestFlowWorkspaceSuppliesTheDefaultRoleOnly(t *testing.T) {
	for _, active := range []string{"ask", "build", "commit"} {
		opened := planeFixture()
		opened.ActiveRole = active
		plane := NewModelPlane(opened)
		plane.FocusModels()
		model, ok := plane.ChosenModel()
		if !ok {
			t.Fatal("no model to stage")
		}
		if !plane.beginModelFirst(model) {
			t.Fatal("beginModelFirst reported no model")
		}
		if plane.Roles[plane.RoleCursor].Role != active {
			t.Errorf("default role focus = %q, want the workspace role %q", plane.Roles[plane.RoleCursor].Role, active)
		}
		if plane.ActiveRole != active {
			t.Errorf("active role = %q, want it unchanged until the role is chosen", plane.ActiveRole)
		}
	}
}

// TestFlowEscBeforeApplyDiscardsTheTransaction proves Esc backs out one step
// at a time and leaves configuration untouched.
func TestFlowEscBeforeApplyDiscardsTheTransaction(t *testing.T) {
	p, calls := flowProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	esc := tea.KeyMsg{Type: tea.KeyEsc}

	p.Update(enter) // model -> role
	p.Update(enter) // role -> effort
	p.Update(esc)   // effort -> role
	if p.state.Plane.Stage != PlaneStageRole {
		t.Fatalf("stage after first Esc = %v, want role selection", p.state.Plane.Stage)
	}
	p.Update(esc) // role -> browse
	if p.state.Plane.Stage != PlaneStageBrowse {
		t.Fatalf("stage after second Esc = %v, want browse", p.state.Plane.Stage)
	}
	if p.state.Plane.PendingModel.ID != "" {
		t.Errorf("pending model = %q, want the transaction discarded", p.state.Plane.PendingModel.ID)
	}
	if p.state.Plane == nil {
		t.Error("Esc closed the dialog instead of backing out the transaction")
	}
	if len(*calls) != 0 {
		t.Fatal("Esc persisted a pending assignment")
	}
}

// TestFlowModelWithoutALadderSkipsEffort proves no fake effort step is created
// for a model that exposes none; the flow goes straight to an explicit apply.
func TestFlowModelWithoutALadderSkipsEffort(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	plane.SetFilter("Plain")
	model, ok := plane.ChosenModel()
	if !ok {
		t.Fatal("no plain model")
	}
	plane.beginModelFirst(model)
	if !plane.chooseRole() {
		t.Fatal("chooseRole reported no role")
	}
	if plane.Stage != PlaneStageApply {
		t.Fatalf("stage = %v, want apply for a model with no ladder", plane.Stage)
	}
	if plane.Effort != "" {
		t.Errorf("effort = %q, want none", plane.Effort)
	}
	update, ok := plane.PendingAssignment()
	if !ok {
		t.Fatal("no pending assignment at the apply step")
	}
	if update.Model != "openrouter/vendor/plain" || update.Effort != "" {
		t.Errorf("pending = %+v, want the plain model with no effort", update)
	}
}

// TestFlowEffortChoicesComeOnlyFromTheModel proves the effort step is derived
// from the staged model's ladder and refuses values outside it.
func TestFlowEffortChoicesComeOnlyFromTheModel(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	model, _ := plane.ChosenModel() // sonnet
	plane.beginModelFirst(model)
	plane.chooseRole()
	if plane.Stage != PlaneStageEffort {
		t.Fatalf("stage = %v, want effort", plane.Stage)
	}
	got := plane.EffortOptions()
	want := []string{EffortOff, "low", "medium", "high"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("options = %v, want exactly the model's ladder %v", got, want)
	}
	plane.SetEffort("xhigh")
	for _, option := range plane.EffortOptions() {
		if option == "xhigh" {
			t.Fatal("an effort outside the model's ladder was staged")
		}
	}
}

// TestFlowUnknownCapabilityStaysUnknown proves an unpublished ladder is not
// fabricated into either supported or unsupported.
func TestFlowUnknownCapabilityStaysUnknown(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	plane.SetFilter("Silent")
	model, ok := plane.ChosenModel()
	if !ok {
		t.Fatal("no silent model")
	}
	if got := planeThinking(model); got != "unknown" {
		t.Errorf("thinking = %q, want unknown", got)
	}
	if got := effortOptions(model); len(got) != 0 {
		t.Errorf("efforts = %v, want none for an unknown capability", got)
	}
}

// TestFlowRolesBackNavigation pins the roles-first flow and its Esc ladder:
// role -> model -> effort -> apply, backing out one step at a time.
func TestFlowRolesBackNavigation(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusRoles()
	if !plane.StageRole() {
		t.Fatal("StageRole reported no role")
	}
	if plane.Stage != PlaneStageModel || plane.Mode != PlaneRoles {
		t.Fatalf("after role pick: stage=%v mode=%q", plane.Stage, plane.Mode)
	}
	if _, ok := plane.ChosenModel(); !ok {
		t.Fatal("no model to choose")
	}
	if !plane.chooseModel() {
		t.Fatal("chooseModel reported no model")
	}
	if plane.Stage != PlaneStageEffort {
		t.Fatalf("stage = %v, want effort", plane.Stage)
	}
	plane.CancelStage()
	if plane.Stage != PlaneStageModel {
		t.Fatalf("stage after Esc = %v, want model selection", plane.Stage)
	}
	plane.CancelStage()
	if plane.Stage != PlaneStageBrowse || plane.Mode != PlaneRoles {
		t.Fatalf("stage after second Esc = %v mode=%q, want roles browse", plane.Stage, plane.Mode)
	}
	if plane.Focus != PlaneFocusRoles {
		t.Errorf("focus = %v, want the roles pane", plane.Focus)
	}
}

// TestFlowEscFromRolesReturnsToModel keeps the top-level navigation: Esc in
// ROLES backs out to MODEL, and a further Esc closes.
func TestFlowEscFromRolesReturnsToModel(t *testing.T) {
	p, _ := flowProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusRoles})
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	p.Update(esc)
	if p.state.Plane == nil {
		t.Fatal("Esc closed the dialog instead of returning to MODEL")
	}
	if p.state.Plane.Mode != PlaneModels {
		t.Fatalf("mode = %q, want MODEL", p.state.Plane.Mode)
	}
	p.Update(esc)
	if p.state.Plane != nil {
		t.Error("a second Esc from MODEL did not close the dialog")
	}
}

// TestFlowStagesIgnorePrintableKeys is the input-safety regression for the
// flow: a printable never advances a step, moves a cursor, or leaks into
// search while a selection or effort step owns the keyboard.
func TestFlowStagesIgnorePrintableKeys(t *testing.T) {
	p, _ := flowProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // model -> role
	before := p.state.Plane.RoleCursor
	p.Update(planeKey("e"))
	if p.state.Plane.RoleCursor != before {
		t.Error("a printable moved the role cursor")
	}
	if p.state.Plane.Filter != "" {
		t.Errorf("filter = %q, want no search input outside the model step", p.state.Plane.Filter)
	}
}

// TestFlowRendersWithinEveryRequiredSize keeps the bounded modal contract for
// the flow steps: no line overflows the terminal, the stage title names the
// current step, and the control stays visible at every supported size.
func TestFlowRendersWithinEveryRequiredSize(t *testing.T) {
	sizes := [][2]int{{40, 10}, {60, 20}, {80, 24}, {120, 40}, {160, 50}}
	for _, size := range sizes {
		w, h := size[0], size[1]

		s := New()
		s.Apply(TerminalResized{Width: w, Height: h})
		s.Apply(ModelPlaneOpened(planeFixture()))
		s.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
		s.Apply(PlaneModelStaged{Model: s.Plane.Models[0]}) // sonnet, laddered
		out := Render(s)
		if !strings.Contains(out, "MODEL CONTROL · role") {
			t.Errorf("%dx%d: role stage title missing:\n%s", w, h, out)
		}
		assertNoFlowOverflow(t, w, h, out)

		s.Apply(PlaneRoleChosen{}) // -> effort step
		if s.Plane.Stage != PlaneStageEffort {
			t.Fatalf("%dx%d: stage = %v, want effort", w, h, s.Plane.Stage)
		}
		out = Render(s)
		if !strings.Contains(out, "MODEL CONTROL · effort") {
			t.Errorf("%dx%d: effort stage title missing:\n%s", w, h, out)
		}
		assertNoFlowOverflow(t, w, h, out)
		// Roomy dialogs can also show the step heading; tiny ones keep the
		// control row instead.
		if w >= 60 && !strings.Contains(out, "Thinking effort") {
			t.Errorf("%dx%d: effort heading missing:\n%s", w, h, out)
		}
	}
}

// assertNoFlowOverflow fails if any rendered line is wider than the terminal.
func assertNoFlowOverflow(t *testing.T, w, h int, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if CellWidth(line) > w {
			t.Errorf("%dx%d: line overflows (%d cells): %q", w, h, CellWidth(line), line)
		}
	}
}

// TestFlowChangingTheModelRefreshesTheEffortChoices walks the reassignment
// scenario: SUP is bound to a laddered model at a chosen effort, then replaced
// by a model with no ladder. The effort choices follow the new model and the
// old level is not carried into Apply.
func TestFlowChangingTheModelRefreshesTheEffortChoices(t *testing.T) {
	p, calls := flowProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	down := tea.KeyMsg{Type: tea.KeyDown}

	// Assign sonnet @ low to SUP.
	p.Update(enter) // model -> role (cursor on the workspace role, build)
	p.Update(down)  // role cursor -> sup
	if got := p.state.Plane.Roles[p.state.Plane.RoleCursor].Role; got != "sup" {
		t.Fatalf("role cursor = %q, want sup", got)
	}
	p.Update(enter) // role -> effort
	if p.state.Plane.Stage != PlaneStageEffort {
		t.Fatalf("stage = %v, want effort", p.state.Plane.Stage)
	}
	p.Update(down) // off -> low
	if p.state.Plane.Effort != "low" {
		t.Fatalf("effort = %q, want low", p.state.Plane.Effort)
	}
	p.Update(enter) // apply
	if len(*calls) != 1 {
		t.Fatalf("calls = %d, want one", len(*calls))
	}
	if first := (*calls)[0]; first.Role != "sup" || first.Effort != "low" {
		t.Fatalf("first assignment = %+v, want sup/low", first)
	}

	// Replace SUP with a model that exposes no ladder.
	p.state.Plane.SetFilter("Plain")
	p.Update(enter) // model -> role; the target is still SUP
	if got := p.state.Plane.Roles[p.state.Plane.RoleCursor].Role; got != "sup" {
		t.Fatalf("role cursor = %q, want the previously assigned sup", got)
	}
	p.Update(enter) // role -> apply (no ladder, so the effort step is skipped)
	if p.state.Plane.Stage != PlaneStageApply {
		t.Fatalf("stage = %v, want apply for a model with no ladder", p.state.Plane.Stage)
	}
	if p.state.Plane.Effort != "" {
		t.Errorf("effort = %q, want the old level dropped", p.state.Plane.Effort)
	}
	p.Update(enter) // apply
	if len(*calls) != 2 {
		t.Fatalf("calls = %d, want two", len(*calls))
	}
	second := (*calls)[1]
	if second.Model != "openrouter/vendor/plain" || second.Effort != "" {
		t.Errorf("second assignment = %+v, want the new model with no carried effort", second)
	}
}
