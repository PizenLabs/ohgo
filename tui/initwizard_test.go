package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// errTestWizardCommit stands in for a failed project commit.
var errTestWizardCommit = errors.New("commit project: disk is read-only")

// Onboarding must ask Git, then identity, then project: the startup gate's
// order is the contract, and the wizard replays it in-canvas.

func TestWizardStepOrderGitIdentityProject(t *testing.T) {
	w := NewInitWizard(true, "Alex", "payments-api")
	if w.Step() != WizardStepGit {
		t.Fatalf("step = %v, want git first", w.Step())
	}
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if w.Step() != WizardStepIdentity {
		t.Fatalf("step = %v, want identity after git", w.Step())
	}
	if w.Decision().WantGit {
		t.Error("WantGit = true after answering n")
	}
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if w.Step() != WizardStepProject {
		t.Fatalf("step = %v, want project after identity", w.Step())
	}
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !w.Done() || w.Step() != WizardStepDone {
		t.Fatalf("done = %v step = %v, want the flow to finish at project", w.Done(), w.Step())
	}
	decision := w.Decision()
	if decision.Worker != "Alex" || decision.Project != "payments-api" {
		t.Errorf("decision = %+v, want the confirmed defaults", decision)
	}
}

func TestWizardSkipsGitInsideARepository(t *testing.T) {
	w := NewInitWizard(false, "Alex", "payments-api")
	if w.Step() != WizardStepIdentity {
		t.Fatalf("step = %v, want identity first when git exists", w.Step())
	}
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !w.Done() {
		t.Fatal("a two-step flow must finish after identity and project")
	}
}

// The Git choice answers directly and stays reversible until confirmed.

func TestWizardGitShortcutsAnswerDirectly(t *testing.T) {
	for key, want := range map[string]bool{"y": true, "Y": true, "n": false, "N": false} {
		w := NewInitWizard(true, "Alex", "proj")
		w, _ = w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if w.Step() != WizardStepIdentity {
			t.Errorf("key %q: step = %v, want identity", key, w.Step())
		}
		if w.Decision().WantGit != want {
			t.Errorf("key %q: WantGit = %v, want %v", key, w.Decision().WantGit, want)
		}
	}
}

func TestWizardGitQuitCancelsWithoutCommitting(t *testing.T) {
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
	} {
		w := NewInitWizard(true, "Alex", "proj")
		w, _ = w.Update(msg)
		if !w.Canceled() {
			t.Errorf("key %q: canceled = false, want a clean abort", msg.String())
		}
		if w.Done() {
			t.Errorf("key %q: a cancelled wizard must never read as done", msg.String())
		}
	}
}

func TestWizardGitCursorStopsAtTheEnds(t *testing.T) {
	w := NewInitWizard(true, "Alex", "proj")
	w.MoveGit(-1)
	if w.GitCursor() != 0 {
		t.Errorf("cursor = %d, want it clamped at Yes", w.GitCursor())
	}
	w.MoveGit(10)
	w.MoveGit(1)
	if w.GitCursor() != 2 {
		t.Errorf("cursor = %d, want it clamped at Quit without wrapping", w.GitCursor())
	}
}

// Identity and project inputs confirm their defaults untouched, accept
// edits, and refuse an empty name rather than committing one.

func TestWizardEmptyInputConfirmsTheDefault(t *testing.T) {
	w := NewInitWizard(false, "Alex", "payments-api")
	clearInput(w)
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	clearInput(w)
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !w.Done() {
		t.Fatal("empty inputs must confirm the offered defaults")
	}
	if decision := w.Decision(); decision.Worker != "Alex" || decision.Project != "payments-api" {
		t.Errorf("decision = %+v, want the defaults", decision)
	}
}

func TestWizardTypedInputOverridesTheDefault(t *testing.T) {
	w := NewInitWizard(false, "Alex", "payments-api")
	clearInput(w)
	w, _ = w.Update(runes("Bob"))
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if w.Step() != WizardStepProject {
		t.Fatalf("step = %v, want project after confirming identity", w.Step())
	}
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if decision := w.Decision(); decision.Worker != "Bob" {
		t.Errorf("worker = %q, want the typed name", decision.Worker)
	}
}

func TestWizardEmptyNameWithEmptyDefaultStaysOpen(t *testing.T) {
	w := NewInitWizard(false, "", "proj")
	clearInput(w)
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if w.Done() || w.Step() != WizardStepIdentity {
		t.Fatal("an empty worker name must hold the step open, not commit")
	}
	if w.errMsg == "" {
		t.Error("the held step must say why it refused the empty name")
	}
}

func TestWizardEscapeStepsBackKeepingTypedValues(t *testing.T) {
	w := NewInitWizard(true, "Alex", "proj")
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	clearInput(w)
	w, _ = w.Update(runes("Bob"))
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if w.Step() != WizardStepIdentity {
		t.Fatalf("step = %v, want esc to return to identity", w.Step())
	}
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if w.Step() != WizardStepGit {
		t.Fatalf("step = %v, want esc to return to git", w.Step())
	}
	w, _ = w.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !w.Canceled() {
		t.Error("esc on the first step must cancel: there is nowhere earlier")
	}
}

// The modal owns its keys: nothing typed reaches the composer behind it,
// and the frame keeps the header and footer chrome.

func TestWizardKeysNeverReachTheComposer(t *testing.T) {
	p := NewProgram(nil, "groq/llama", 0)
	p.SetInitWizard(NewInitWizard(true, "Alex", "proj"), InitWizardHooks{})
	keyPress(p, runes("n"))
	keyPress(p, runes("Bob"))
	if p.state.Input != "" {
		t.Errorf("composer = %q, want onboarding keys kept out of it", p.state.Input)
	}
	if len(p.state.Entries) != 0 {
		t.Errorf("entries = %d, want no transcript while onboarding", len(p.state.Entries))
	}
}

func TestWizardFrameKeepsHeaderAndFooter(t *testing.T) {
	p := NewProgram(nil, "groq/llama-3.1-8b-instant", 128000)
	p.state.Status.Workspace = "BUILD"
	p.state.Status.Cwd = "/tmp/demo"
	p.SetInitWizard(NewInitWizard(true, "Alex", "proj"), InitWizardHooks{})
	view := p.View()
	if !strings.Contains(view, "ohgo") || !strings.Contains(view, "groq/llama-3.1-8b-instant") {
		t.Errorf("frame %q: the header must keep product and model", firstLines(view, 2))
	}
	if !strings.Contains(view, "BUILD") {
		t.Errorf("frame %q: the footer must keep the workspace badge", lastLines(view, 1))
	}
	if !strings.Contains(view, "Project setup") {
		t.Errorf("frame %q: the dialog card must render between the chrome", firstLines(view, 12))
	}
}

func TestWizardCompletionHandsDecisionsToTheOwner(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var got InitWizardDecision
	p.SetInitWizard(NewInitWizard(false, "Alex", "proj"), InitWizardHooks{
		Complete: func(d InitWizardDecision) (IdentityUpdated, string, error) {
			got = d
			return IdentityUpdated{User: d.Worker, Greeting: "Good evening, Alex.", Ready: "ready"}, "runtime ready", nil
		},
	})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.wizard != nil {
		t.Fatal("the modal must close once the owner commits")
	}
	if got.Worker != "Alex" || got.Project != "proj" {
		t.Errorf("decision = %+v, want the confirmed answers", got)
	}
	if p.state.Status.User != "Alex" || p.state.Status.Greeting != "Good evening, Alex." {
		t.Errorf("status = %+v, want the committed identity published", p.state.Status)
	}
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && entry.Notice == "runtime ready" {
			found = true
		}
	}
	if !found {
		t.Error("the completion notice must land on the transcript")
	}
}

func TestWizardCancelQuitsWithoutPublishing(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetInitWizard(NewInitWizard(true, "Alex", "proj"), InitWizardHooks{})
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if p.wizard != nil {
		t.Fatal("a cancelled wizard must leave the screen")
	}
	if p.state.Status.User != "" {
		t.Errorf("user = %q, want no identity from an abandoned flow", p.state.Status.User)
	}
}

// A failed commit reopens the final step instead of stranding the user.
func TestWizardFailedCommitReopensForRetry(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetInitWizard(NewInitWizard(false, "Alex", "proj"), InitWizardHooks{
		Complete: func(InitWizardDecision) (IdentityUpdated, string, error) {
			return IdentityUpdated{}, "", errTestWizardCommit
		},
	})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.wizard == nil {
		t.Fatal("a failed commit must keep the wizard open")
	}
	if p.wizard.Done() {
		t.Error("a failed commit must leave the done state for retry")
	}
	if p.wizard.Step() != WizardStepProject {
		t.Errorf("step = %v, want the final step reopened", p.wizard.Step())
	}
}

// clearInput empties the focused wizard field, simulating a user who
// deletes the offered default before confirming.
func clearInput(w *InitWizardModel) {
	input := w.focusedInput()
	input.SetValue("")
	w.storeFocusedInput(*input)
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
