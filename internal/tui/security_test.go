package tui

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/internal/execution/exec"
	tea "github.com/charmbracelet/bubbletea"
)

// authorizingShell wires an escape whose security decision is supplied by the
// test, and records whether the command ran.
func authorizingShell(authorize func(string) (bool, bool, string)) (ShellHooks, *bool) {
	ran := false
	return ShellHooks{
		Run: func(ctx context.Context, command string, onChunk func(string)) (exec.EscapeResult, error) {
			ran = true
			return exec.EscapeResult{ExitCode: 0}, nil
		},
		// Mutates is set to the opposite of a naive answer on purpose: the
		// Authorize hook must win.
		Mutates:   func(string) bool { return false },
		Authorize: authorize,
	}, &ran
}

func lastNotice(p *Program) string {
	for i := len(p.state.Entries) - 1; i >= 0; i-- {
		if p.state.Entries[i].Kind == EntryNotice {
			return p.state.Entries[i].Notice
		}
	}
	return ""
}

func TestShellEscapeAuthorizeDenyDoesNotRun(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	hooks, ran := authorizingShell(func(string) (bool, bool, string) {
		return false, false, "mutation is not permitted in the fuzz authority"
	})
	p.SetShellEscape(hooks)
	p.state.Apply(WorkspaceUpdated{Workspace: "FUZZ", HasWorkspace: true})
	p.state.Apply(InputChanged{Text: "!rm -rf internal"})

	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("a denied escape returned a command")
	}
	if p.state.Pending != nil {
		t.Error("a denied escape opened a confirmation")
	}
	if p.state.shell(0) != nil {
		t.Error("a denied escape opened a shell block")
	}
	if *ran {
		t.Error("a denied escape ran")
	}
	if got := lastNotice(p); got != "mutation is not permitted in the fuzz authority" {
		t.Errorf("notice = %q, want the policy reason", got)
	}
}

func TestShellEscapeAuthorizeConfirmAsksThenRuns(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	hooks, ran := authorizingShell(func(string) (bool, bool, string) {
		return true, true, ""
	})
	p.SetShellEscape(hooks)
	p.state.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	p.state.Apply(InputChanged{Text: "!rm -rf build"})

	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("opening a confirmation must not start the command")
	}
	if p.state.Pending == nil || p.state.Pending.Prompt != "Run shell command" {
		t.Fatalf("pending = %+v, want the shell confirmation", p.state.Pending)
	}
	if *ran {
		t.Fatal("the command ran before confirmation")
	}

	_, cmd = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("approving must start the command")
	}
	cmd()
	drainShellEvents(p)
	if !*ran {
		t.Error("the approved command did not run")
	}
}

func TestShellEscapeAuthorizeAllowRunsDirectly(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	hooks, ran := authorizingShell(func(string) (bool, bool, string) {
		return true, false, ""
	})
	p.SetShellEscape(hooks)
	p.state.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	p.state.Apply(InputChanged{Text: "!go test ./..."})

	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || p.state.Pending != nil {
		t.Fatal("an auto-authorized escape must run without confirmation")
	}
	cmd()
	drainShellEvents(p)
	if !*ran {
		t.Error("the auto-authorized command did not run")
	}
}
