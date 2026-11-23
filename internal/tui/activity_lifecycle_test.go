package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// liveActivitiesAt counts transient activity that still presents as running at
// now. A terminal run owns none of it: a reasoning trace must have closed, and
// every tool must have reached a terminal status.
func liveActivitiesAt(s State, now time.Time) int {
	live := 0
	for i := range s.Entries {
		entry := &s.Entries[i]
		switch entry.Kind {
		case EntryReasoning:
			if entry.Reasoning != nil && entry.Reasoning.reasoningPresentationOpen(now) {
				live++
			}
		case EntryTool:
			if entry.Tool == nil {
				continue
			}
			switch entry.Tool.presentationStatus(now) {
			case ToolRunning, ToolPending:
				live++
			}
		}
	}
	return live
}

// visibleAnswers returns the assistant prose that is allowed to be an answer.
// A withdrawn candidate keeps its text only in WithdrawnText, so it can never
// be counted here.
func visibleAnswers(s State) []string {
	var answers []string
	for i := range s.Entries {
		entry := &s.Entries[i]
		if entry.Kind == EntryAssistant && !entry.Discarded && strings.TrimSpace(entry.AssistantText) != "" {
			answers = append(answers, entry.AssistantText)
		}
	}
	return answers
}

// The answer boundary is a control-plane transition, not a rendering cue. An
// open reasoning trace must reconcile when the runtime commits the answer, and
// it must do so before the next frame can show Thought running beside a final
// answer.
func TestAnswerBoundaryReconcilesThoughtBeforeCommit(t *testing.T) {
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 30})
	s.Apply(UserSubmitted{Text: "Investigate this project", At: start, Seq: 1})
	s.Apply(ReasoningDelta{Text: "I need to inspect the repository. ", At: start.Add(30 * time.Second), Seq: 1})
	s.Apply(ReasoningDelta{Text: "Available actions: none\nNext action: answer from the evidence above.\nNo tool is available.", At: start.Add(time.Minute), Seq: 1})

	if len(s.Entries) < 2 || s.Entries[1].Kind != EntryReasoning || s.Entries[1].Reasoning == nil {
		t.Fatalf("entries = %+v, want the live Thought trace", s.Entries)
	}
	if s.Entries[1].Reasoning.Closed {
		t.Fatal("fixture Thought is already closed; the regression would not reproduce")
	}
	if got := liveActivitiesAt(s, start.Add(time.Minute)); got != 1 {
		t.Fatalf("live activities before the answer = %d, want the one Thought", got)
	}

	const answer = "This project is a Go agent runtime with a claim-preserving answer lifecycle."
	commitAt := start.Add(6*time.Minute + 2*time.Second)
	s.Apply(AssistantReconcile{Text: answer, Answer: agent.AnswerCommitted, At: commitAt, Seq: 1})

	reasoning := s.Entries[1].Reasoning
	if !reasoning.Closed {
		t.Fatal("committing the answer left the Thought trace ACTIVE")
	}
	if !reasoning.DwellUntil.IsZero() {
		t.Fatalf("reconciled Thought kept a live dwell until %v", reasoning.DwellUntil)
	}
	if !reasoning.EndedAt.Equal(commitAt) {
		t.Fatalf("Thought ended at %v, want the answer boundary %v", reasoning.EndedAt, commitAt)
	}
	if got := liveActivitiesAt(s, commitAt.Add(time.Minute)); got != 0 {
		t.Fatalf("live activities after the answer boundary = %d, want 0", got)
	}
	if answers := visibleAnswers(s); len(answers) != 1 || answers[0] != answer {
		t.Fatalf("visible answers = %q, want exactly the committed answer", answers)
	}
	// The reasoning body may still be expanded, but its header must no longer
	// claim to be producing thought.
	if rendered := Render(s); strings.Contains(rendered, spinnerFrames[0]+" Thought") {
		t.Fatalf("committed answer left a live Thought spinner:\n%s", rendered)
	}

	terminalAt := commitAt.Add(time.Second)
	s.Apply(AssistantDone{Seq: 1, At: terminalAt, Footer: &TurnFooter{Kind: FooterVerified, Detail: "read-only analysis"}})
	if s.Status.Busy {
		t.Fatal("run terminal left the turn busy")
	}
	if got := liveActivitiesAt(s, terminalAt.Add(time.Hour)); got != 0 {
		t.Fatalf("live activities after run terminal = %d, want 0", got)
	}
	if activity := ProjectActivity(s); activity.Surface != ActivityNone {
		t.Fatalf("run terminal still projects an %s activity surface", activity.Surface)
	}
	if answers := visibleAnswers(s); len(answers) != 1 || answers[0] != answer {
		t.Fatalf("terminal run published answers %q, want exactly one committed answer", answers)
	}
}

// Replay is history, not live activity. A stored thinking block must appear as
// a closed Thought row, never as something that starts spinning again.
func TestReplayNeverResurrectsThoughtAsActive(t *testing.T) {
	history := []agent.Message{
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ThinkingContent{Thinking: "Available actions: none\nNext action: answer from the evidence above."},
			ai.TextContent{Text: "The stored answer."},
		}}},
	}
	s := New()
	s.Apply(HistoryReplaced{Entries: EntriesFromMessages(history)})

	var reasoning *ReasoningEntry
	for i := range s.Entries {
		if s.Entries[i].Kind == EntryReasoning {
			reasoning = s.Entries[i].Reasoning
		}
	}
	if reasoning == nil {
		t.Fatal("replay dropped the stored Thought block")
	}
	if !reasoning.Closed {
		t.Fatal("replay resurrected the stored Thought as ACTIVE")
	}
	if got := liveActivitiesAt(s, time.Now().Add(time.Hour)); got != 0 {
		t.Fatalf("replay has %d live activities, want 0", got)
	}
}

// The production ordering is part of the invariant. The run publishes the
// final reasoning trace before the committed answer, so the reconcile must
// close that trace inside the answer event: waiting for AssistantDone would
// leave one frame where Thought is ACTIVE and the answer is FINAL.
func TestRunTurnReconcilesPostRunThoughtBeforeCommittedAnswer(t *testing.T) {
	const trace = "Available actions: none\nNext action: answer from the evidence above.\nNo tool is available."
	const answer = "This project is a Go agent runtime with an explicit answer boundary."
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{
					ai.ThinkingContent{Thinking: trace},
					ai.TextContent{Text: answer},
				},
			}), nil
		},
		Tools: []agent.Tool{&orderReadTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurn("Investigate this project and analyze what this project is about?")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn hung")
	}

	var events []Event
	boundaryLive := -1
	for {
		select {
		case event := <-p.events:
			events = append(events, event)
			p.state.Apply(event)
			if reconcile, ok := event.(AssistantReconcile); ok && reconcile.Answer == agent.AnswerCommitted {
				// This is the frame the user sees when the answer first
				// appears. It must already be past the Thought boundary;
				// AssistantDone has not arrived yet.
				boundaryLive = liveActivitiesAt(p.state, time.Now().Add(time.Hour))
			}
		default:
			goto drained
		}
	}

drained:
	reasoningAt, reconcileAt, terminalAt := -1, -1, -1
	committed := false
	for i, event := range events {
		switch typed := event.(type) {
		case ReasoningDelta:
			if reasoningAt < 0 && strings.Contains(typed.Text, "Next action: answer") {
				reasoningAt = i
			}
		case AssistantReconcile:
			if typed.Answer == agent.AnswerCommitted {
				committed = true
				reconcileAt = i
			}
		case AssistantDone:
			terminalAt = i
		}
	}
	if reasoningAt < 0 {
		t.Fatalf("the run never published its final Thought: %+v", events)
	}
	if !committed || reconcileAt < 0 {
		t.Fatalf("the run never published its committed answer: %+v", events)
	}
	if reasoningAt > reconcileAt {
		t.Fatalf("Thought published at %d after the committed answer at %d", reasoningAt, reconcileAt)
	}
	if terminalAt != len(events)-1 {
		t.Fatalf("terminal event = %d of %d, want the last event", terminalAt, len(events))
	}
	if boundaryLive != 0 {
		t.Fatalf("committed answer became visible with %d live activities, want 0 at the answer boundary", boundaryLive)
	}

	if got := liveActivitiesAt(p.state, time.Now().Add(time.Hour)); got != 0 {
		t.Fatalf("run terminal left %d live activities:\n%s", got, Render(p.state))
	}
	if answers := visibleAnswers(p.state); len(answers) != 1 || answers[0] != answer {
		t.Fatalf("visible answers = %q, want exactly the committed answer", answers)
	}
	if rendered := Render(p.state); strings.Contains(rendered, spinnerFrames[0]+" Thought") {
		t.Fatalf("run terminal left a live Thought spinner:\n%s", rendered)
	}
}

// The repair window is a bounded correction phase. A call refused after it
// closes is a terminal outcome, not an error that leaves the tool or the
// Thought trace pending, and the run still converges on one answer.
func TestRepairWindowClosureLeavesNoOrphanedActivity(t *testing.T) {
	start := time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 30})
	s.Apply(UserSubmitted{Text: "check this project", At: start, Seq: 1})
	s.Apply(ReasoningDelta{Text: "The answer needs one more citation.", At: start.Add(time.Second), Seq: 1})

	for i, id := range []string{"r1", "r2", "r3", "r4"} {
		at := start.Add(time.Duration(i+1) * time.Second)
		s.Apply(ToolStarted{ID: id, Name: "read", Summary: "read evidence-" + id, At: at, Seq: 1})
		s.Apply(ToolFinished{ID: id, Text: "evidence", Summary: "evidence", At: at.Add(10 * time.Millisecond), Seq: 1})
	}
	closedAt := start.Add(6 * time.Second)
	s.Apply(ReasoningDelta{Text: "The repair window has closed.", At: closedAt, Seq: 1})
	s.Apply(ToolStarted{ID: "r5", Name: "read", Summary: "read evidence-r5", At: closedAt.Add(time.Millisecond), Seq: 1})
	s.Apply(ToolFinished{
		ID:      "r5",
		Text:    "the repair window is closed. Answer with the evidence already observed.",
		Summary: "the repair window is closed",
		IsError: true,
		At:      closedAt.Add(2 * time.Millisecond),
		Seq:     1,
	})

	refused := s.tool("r5")
	if refused == nil {
		t.Fatal("the refused repair call never reached the transcript")
	}
	if refused.Status != ToolError {
		t.Fatalf("refused repair call status = %q, want a terminal error outcome", refused.Status)
	}
	if !strings.Contains(refused.Text, "repair window is closed") {
		t.Fatalf("refused repair outcome = %q, want the runtime's REPAIR_WINDOW_CLOSED reason", refused.Text)
	}

	const answer = "The project is a Go agent runtime. The remaining claim is qualified by the observed evidence."
	s.Apply(AssistantReconcile{Text: answer, Answer: agent.AnswerCommitted, At: start.Add(7 * time.Second), Seq: 1})
	s.Apply(AssistantDone{Seq: 1, At: start.Add(8 * time.Second), Footer: &TurnFooter{Kind: FooterReview, Detail: "repair window closed"}})

	terminalAt := start.Add(9 * time.Second)
	if got := liveActivitiesAt(s, terminalAt); got != 0 {
		t.Fatalf("live activities after the repair run = %d, want 0", got)
	}
	if s.runningTools != 0 {
		t.Fatalf("running tools after terminal = %d, want 0", s.runningTools)
	}
	if answers := visibleAnswers(s); len(answers) != 1 || answers[0] != answer {
		t.Fatalf("visible answers = %q, want exactly one committed answer", answers)
	}
	for i := range s.Entries {
		entry := &s.Entries[i]
		if entry.Kind == EntryAssistant && entry.Discarded && strings.TrimSpace(entry.WithdrawnText) != "" {
			t.Fatalf("repair run resurrected a withdrawn answer: %q", entry.WithdrawnText)
		}
	}

	rendered := Render(s)
	if strings.Contains(rendered, spinnerFrames[0]+" Thought") {
		t.Fatalf("repair run left a live Thought spinner:\n%s", rendered)
	}
}

// Every terminal outcome converges transient activity. This is the global
// invariant behind the two prompt-specific regressions above: no run may end
// while a Thought or tool it owns is still ACTIVE.
func TestEveryTerminalRunPathLeavesNoLiveActivity(t *testing.T) {
	start := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	verify := &TurnFooter{Kind: FooterVerified, Detail: "complete"}
	review := &TurnFooter{Kind: FooterReview, Detail: "needs review"}
	budget := &TurnFooter{Kind: FooterReview, Detail: "budget exhausted"}

	cases := []struct {
		name   string
		events []Event
	}{
		{name: "completed", events: []Event{AssistantDone{Seq: 1, At: start.Add(time.Second), Footer: verify}}},
		{name: "needs_review", events: []Event{AssistantDone{Seq: 1, At: start.Add(time.Second), Footer: review}}},
		{name: "rejected", events: []Event{
			AssistantReconcile{Text: "rejected prose", Answer: agent.AnswerRejected, Reason: "answer review requested changes", At: start.Add(time.Second), Seq: 1},
			AssistantDone{Seq: 1, At: start.Add(2 * time.Second), Footer: review},
		}},
		{name: "failed", events: []Event{AssistantDone{Seq: 1, At: start.Add(time.Second)}}},
		{name: "cancelled", events: []Event{TurnCancelled{Seq: 1, At: start.Add(time.Second)}}},
		{name: "budget_exhausted", events: []Event{AssistantDone{Seq: 1, At: start.Add(time.Second), Footer: budget}}},
		{name: "repair_window_closed", events: []Event{
			ToolFinished{ID: "repair", Text: "the repair window is closed", Summary: "repair window closed", IsError: true, At: start.Add(time.Second), Seq: 1},
			AssistantReconcile{Text: "final answer", Answer: agent.AnswerCommitted, At: start.Add(2 * time.Second), Seq: 1},
			AssistantDone{Seq: 1, At: start.Add(3 * time.Second), Footer: review},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			s.Apply(TerminalResized{Width: 96, Height: 30})
			s.Apply(UserSubmitted{Text: "go", At: start, Seq: 1})
			s.Apply(ReasoningDelta{Text: "think about the answer", At: start.Add(time.Millisecond), Seq: 1})
			s.Apply(ToolStarted{ID: "repair", Name: "read", Summary: "read evidence", At: start.Add(2 * time.Millisecond), Seq: 1})

			for _, event := range tc.events {
				s.Apply(event)
			}

			if got := liveActivitiesAt(s, start.Add(time.Hour)); got != 0 {
				t.Fatalf("terminal %s left %d live activities:\n%s", tc.name, got, Render(s))
			}
			if s.runningTools != 0 {
				t.Fatalf("terminal %s left %d running tools", tc.name, s.runningTools)
			}
			if s.Status.Busy {
				t.Fatalf("terminal %s left the turn busy", tc.name)
			}
			if activity := ProjectActivity(s); activity.Surface != ActivityNone {
				t.Fatalf("terminal %s still projects an %s activity surface", tc.name, activity.Surface)
			}
			for i := range s.Entries {
				entry := &s.Entries[i]
				if entry.Kind == EntryReasoning && entry.Reasoning != nil && !entry.Reasoning.Closed {
					t.Fatalf("terminal %s left reasoning open", tc.name)
				}
			}
		})
	}
}
