package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
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

// submittedTurn returns the route projection carried by the turn's
// UserSubmitted event, or false when the turn never started.
func submittedTurn(events []Event) (TurnSemantics, bool) {
	for _, event := range events {
		if submitted, ok := event.(UserSubmitted); ok {
			return submitted.Turn, true
		}
	}
	return TurnSemantics{}, false
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
			return TurnAuthority{Active: "ASK", Handler: "ASK", Intent: "EXECUTE", Blocked: true, Reason: "this request needs BUILD authority"}
		},
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("fix the bug") }()
	events := collectTurn(t, p)
	<-done

	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Errorf("provider requests = %d, want 0", got)
	}
	if turn, ok := submittedTurn(events); ok && turn.Active {
		t.Errorf("a refused turn projected a delegated route: %+v", turn)
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
	if state.Status.Turn.Active {
		t.Error("a refused turn painted a turn projection")
	}
}

// TestRunTurnProjectsDelegatedRouteTruthfully proves the UI stores the router's
// own handler and intent for the turn instead of re-deriving them (§5).
func TestRunTurnProjectsDelegatedRouteTruthfully(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return "BUILD" },
		PrepareTurn: func(string) TurnAuthority {
			return TurnAuthority{
				Active:    "BUILD",
				Handler:   "ASK",
				Intent:    "READ",
				Effective: "ASK",
				Delegated: true,
			}
		},
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("why does this work?") }()
	events := collectTurn(t, p)
	<-done

	if got := atomic.LoadInt32(&requests); got == 0 {
		t.Error("a delegated turn did not reach the model")
	}
	turn, ok := submittedTurn(events)
	if !ok {
		t.Fatal("the turn never started")
	}
	if !turn.Active || !turn.Delegated {
		t.Errorf("turn = %+v, want an active delegated projection", turn)
	}
	if turn.Workspace != "BUILD" || turn.Handler != "ASK" || turn.Intent != "READ" || turn.Effective != "ASK" {
		t.Errorf("turn = %+v, want BUILD/ASK/READ/ASK", turn)
	}
}

// TestDelegatedTurnNeverSwitchesWorkspace is the negative contract: routing a
// BUILD informational turn must not call the workspace owner (§7, §16).
func TestDelegatedTurnNeverSwitchesWorkspace(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	switched := ""
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return "BUILD" },
		Switch:  func(name string) error { switched = name; return nil },
		PrepareTurn: func(string) TurnAuthority {
			return TurnAuthority{Active: "BUILD", Handler: "ASK", Intent: "READ", Effective: "ASK", Delegated: true}
		},
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("why does this work?") }()
	collectTurn(t, p)
	<-done

	if switched != "" {
		t.Errorf("a delegated turn switched the workspace to %q", switched)
	}
	if p.state.Status.Workspace == "ASK" {
		t.Error("a delegated turn moved the active workspace to ASK")
	}
}

// TestTurnSemanticsAreClearedOnCompletion proves the delegated indicator
// outlives neither the turn nor the transcript: after AssistantDone the
// composer is back to the persistent workspace (§8).
func TestTurnSemanticsAreClearedOnCompletion(t *testing.T) {
	s := New()
	s.Status.Workspace = "BUILD"
	s.Apply(UserSubmitted{Text: "why?", Seq: 1, Turn: TurnSemantics{
		Active: true, Workspace: "BUILD", Handler: "ASK", Intent: "READ", Effective: "ASK", Delegated: true,
	}})

	if got := composerPrefix(s); !strings.Contains(got, "ask/read") {
		t.Fatalf("composer prefix = %q, want the delegated turn indicator", got)
	}

	s.Apply(AssistantDone{Seq: 1})

	if got := composerPrefix(s); got != "build > " {
		t.Errorf("composer prefix after the turn = %q, want the persistent workspace", got)
	}
	if s.Status.Turn.Active {
		t.Error("turn semantics survived completion")
	}
	if got := s.Status.Workspace; got != "BUILD" {
		t.Errorf("active workspace after the turn = %q, want BUILD", got)
	}
}

// TestComposerPrefixShowsOnlyNarrowedTurns pins the visual contract: the
// ephemeral indicator appears only when the handler differs from the active
// workspace, so BUILD+EXECUTE and ASK+READ add no redundant second label.
func TestComposerPrefixShowsOnlyNarrowedTurns(t *testing.T) {
	cases := []struct {
		name string
		ws   string
		turn TurnSemantics
		want string
	}{
		{"BUILD+READ", "BUILD", TurnSemantics{Active: true, Handler: "ASK", Intent: "READ", Delegated: true}, "build · ask/read > "},
		{"BUILD+INVESTIGATE", "BUILD", TurnSemantics{Active: true, Handler: "FUZZ", Intent: "INVESTIGATE", Delegated: true}, "build · fuzz/investigate > "},
		{"FUZZ+READ", "FUZZ", TurnSemantics{Active: true, Handler: "ASK", Intent: "READ", Delegated: true}, "fuzz · ask/read > "},
		{"BUILD+EXECUTE", "BUILD", TurnSemantics{Active: true, Handler: "BUILD", Intent: "EXECUTE"}, "build > "},
		{"ASK+READ", "ASK", TurnSemantics{Active: true, Handler: "ASK", Intent: "READ"}, "ask > "},
		{"idle", "BUILD", TurnSemantics{}, "build > "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			s.Status.Workspace = tc.ws
			s.Status.Turn = tc.turn
			if got := composerPrefix(s); got != tc.want {
				t.Errorf("composer prefix = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderShowsTurnSemanticsWithoutMovingTheWorkspace proves the semantic
// projection reaches the rendered frame and still names the persistent
// workspace as the authority (§12, §15).
func TestRenderShowsTurnSemanticsWithoutMovingTheWorkspace(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", WorkspaceAccent: "33", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "why does this work?", Seq: 1, Turn: TurnSemantics{
		Active: true, Workspace: "BUILD", Handler: "ASK", Intent: "READ", Effective: "ASK", Delegated: true,
	}})

	out := stripANSI(Render(s))
	if !strings.Contains(out, "build · ask/read") {
		t.Fatalf("frame does not show the delegated turn:\n%s", out)
	}
	if strings.Contains(out, "ask > ") {
		t.Errorf("frame presented the session as ASK:\n%s", out)
	}
}
