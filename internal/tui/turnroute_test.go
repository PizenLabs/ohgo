package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// countingLoop builds a loop that records how many provider requests it is
// asked to make.
func countingLoop(t *testing.T, requests *int32) *agent.Loop {
	t.Helper()
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			atomic.AddInt32(requests, 1)
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "answer"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop
}

// collectTurn drains the event channel through the turn's final AssistantDone.
func collectTurn(t *testing.T, p *Program) []Event {
	t.Helper()
	var events []Event
	timeout := time.After(3 * time.Second)
	for {
		select {
		case event := <-p.events:
			events = append(events, event)
			if _, ok := event.(AssistantDone); ok {
				return events
			}
		case <-timeout:
			t.Fatal("turn did not finish")
		}
	}
}

// TestRunTurnRefusesBlockedIntentBeforeTheModel proves the UI consults the
// router and never reaches the provider for a request the active workspace is
// not permitted to answer (§11, §19).
func TestRunTurnRefusesBlockedIntentBeforeTheModel(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return "ASK" },
		PrepareTurn: func(string) TurnAuthority {
			return TurnAuthority{Active: "ASK", Intent: "EXECUTE", Blocked: true, Reason: "this request needs BUILD authority"}
		},
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("fix the bug") }()
	events := collectTurn(t, p)
	<-done

	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Errorf("provider requests = %d, want 0", got)
	}
	found := false
	for _, event := range events {
		if notice, ok := event.(Notice); ok && notice.Text == "this request needs BUILD authority" {
			found = true
		}
	}
	if !found {
		t.Errorf("the refusal was not published: %+v", events)
	}

	// A refusal must not paint a turn indicator or imply the workspace moved:
	// the ASK boundary held, the user must switch explicitly.
	var state State
	state.Status.Workspace = "ASK"
	for _, event := range events {
		state.Apply(event)
	}
	if got := composerPrefix(state); got != "ask > " {
		t.Errorf("refusal prefix = %q, want the unchanged ASK workspace", got)
	}
}

// TestRunTurnKeepsTheSelectedWorkspace proves turn processing does not create
// a second per-turn workspace state.
func TestRunTurnProjectsTheSelectedWorkspace(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	p.state.Status.Workspace = "BUILD"
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current:     func() string { return "BUILD" },
		PrepareTurn: func(string) TurnAuthority { return TurnAuthority{Active: "BUILD", Intent: "READ"} },
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("why does this work?") }()
	events := collectTurn(t, p)
	<-done

	if got := atomic.LoadInt32(&requests); got == 0 {
		t.Error("the turn did not reach the model")
	}
	submitted := false
	for _, event := range events {
		if _, ok := event.(UserSubmitted); ok {
			submitted = true
		}
	}
	if !submitted {
		t.Fatal("the turn never started")
	}
	if p.state.Status.Workspace != "BUILD" {
		t.Errorf("workspace = %q, want BUILD", p.state.Status.Workspace)
	}
}

// TestRenderShowsTheSelectedWorkspaceDuringSupportingReads proves the rendered
// authority stays truthful while BUILD reads files to execute a task.
func TestRenderShowsTheSelectedWorkspaceDuringSupportingReads(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", WorkspaceAccent: "33", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "read supporting context", Seq: 1})

	out := stripANSI(Render(s))
	if strings.Contains(out, "build · ask/read") {
		t.Fatalf("frame created an ASK handler inside BUILD:\n%s", out)
	}
	if !strings.Contains(out, "build > ") {
		t.Errorf("frame lost the selected BUILD authority:\n%s", out)
	}
}
