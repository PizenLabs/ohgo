package tui

import (
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Terminal lifecycle shutdown tests: quitting must never strand a tool that
// is blocked in its approval prompt, and bridge sends must never block after
// the UI is gone. All of these drive the headless seams (Update, runCommand,
// publish); live terminal restoration belongs to Bubble Tea and is covered in
// the audit report, not here.

// A quit issued while a tool waits for approval must answer it. Otherwise the
// tool blocks on its answer channel forever: no UI remains to read it after
// shutdown, so its goroutine (and the turn holding it) leaks until the
// process exits.
func TestQuitDuringApprovalDeniesBlockedTool(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	answer := make(chan approvalAnswer, 1)
	p.Update(approvalPrompt{request: approvalRequest{
		toolName: "bash",
		summary:  "rm -rf /",
		answer:   answer,
	}})
	if p.state.Pending == nil {
		t.Fatal("no approval pending: the quit case cannot be reached")
	}

	Command{Name: "quit"}.runOn(p)

	if p.state.Pending != nil {
		t.Error("Pending still set after quit: the dialog outlives the session")
	}
	if !p.state.Quit {
		t.Error("Quit not requested by /quit")
	}
	select {
	case reply := <-answer:
		if reply.approved {
			t.Error("quit approved a blocked tool: shutdown must deny, never allow")
		}
	default:
		t.Error("blocked tool got no answer: its goroutine waits on a UI that is gone")
	}
}

// A tool reaching approval after the UI has shut down must not park forever
// on an answer nobody will send. It gets a denial it can complete, whether
// it arrives before or after the shutdown release.
func TestApprovalAfterShutdownIsDenied(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	approve := p.ApprovalFunc()
	p.closeDone()

	done := make(chan approvalAnswer, 1)
	go func() {
		ok, reason := approve(ai.ToolCall{Name: "bash"}, agent.Approval{})
		done <- approvalAnswer{approved: ok, reason: reason}
	}()
	select {
	case reply := <-done:
		if reply.approved {
			t.Error("post-shutdown approval returned true: shutdown must deny, never allow")
		}
		if reply.reason == "" {
			t.Error("post-shutdown denial carries no reason for the transcript")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval blocked past shutdown: the tool goroutine never returns")
	}
}

// Closing the shutdown release twice must be safe: Run defers it and tests
// drive it by hand, and the two must never meet as a close of a closed
// channel.
func TestShutdownReleaseIsIdempotent(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second shutdown release panicked: %v", r)
		}
	}()
	p := NewProgram(nil, "test", 0)
	p.closeDone()
	p.closeDone()
}

// Competing shutdown requests must be stable: quitting twice (or /quit then
// /exit) must not panic and must leave the session ended.
func TestRepeatedQuitIsStable(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second quit panicked: %v", r)
		}
	}()
	p := NewProgram(nil, "test", 0)
	Command{Name: "quit"}.runOn(p)
	Command{Name: "exit"}.runOn(p)
	if !p.state.Quit {
		t.Error("Quit not requested after repeated quits")
	}
	if p.state.Pending != nil {
		t.Error("Pending set after idle quits with nothing ever asked")
	}
}

// The event bridge drops on a full buffer by construction. A detached turn
// publishing after shutdown must therefore never block, even when nothing
// will ever drain the channel again.
func TestPublishOnFullBufferNeverBlocks(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	for i := 0; i < cap(p.events)+16; i++ {
		p.publish(Notice{Text: "fill"})
	}
	done := make(chan struct{})
	go func() {
		p.publish(Notice{Text: "after shutdown"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a full buffer with no reader")
	}
}

// An unrecognised message must leave the session alone, never panic and never
// quit: the update loop outlives any single message source.
func TestUnknownMessageLeavesStateAlone(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	model, cmd := p.Update(struct{ X int }{X: 1})
	if model == nil {
		t.Fatal("Update returned a nil model for an unknown message")
	}
	if cmd != nil {
		t.Errorf("unknown message produced a command: only known input may act")
	}
	if p.state.Quit {
		t.Error("unknown message requested quit")
	}
	if p.state.Pending != nil {
		t.Error("unknown message opened an approval")
	}
}
