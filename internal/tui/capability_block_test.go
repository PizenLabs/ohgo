package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A BUILD turn whose objective needs a repository change is refused before the
// model is asked to act when the runtime has no capability that can make one.
//
// The failure this defends against is the whole reason the preflight exists: the
// model is asked to implement something, proposes an action the task surface
// withholds, and then answers an execution request with a manual plan. The turn
// is spent, nothing was changed, and the transcript reads as progress.
func TestBuildMutationIsBlockedBeforeTheModelWithoutMutationCapability(t *testing.T) {
	root := t.TempDir()
	role := workspace.Build
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	var requests int32
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			atomic.AddInt32(&requests, 1)
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "Here is the implementation you can paste."}},
			}), nil
		},
		// Execution and reading only: nothing can change the repository, and
		// nothing can verify a change.
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	program := NewProgram(loop, "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{Current: func() string { return string(role) }})
	program.SetTaskHooks(runtimeHooks(controller))

	done := make(chan struct{})
	go func() { defer close(done); program.runTurn("convert this project to Next.js") }()
	events := collectTurn(t, program)
	<-done

	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("provider requests = %d, want 0: the capability is absent", got)
	}
	var blocked bool
	for _, event := range events {
		notice, ok := event.(Notice)
		if !ok {
			continue
		}
		if strings.Contains(notice.Text, "BLOCKED") &&
			strings.Contains(notice.Text, "repository mutation") &&
			strings.Contains(notice.Text, "No files were changed") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("the block was not reported in control-plane terms: %+v", events)
	}
	current, ok := controller.Current()
	if !ok {
		t.Fatal("the blocked turn recorded no task")
	}
	if got := current.ObjectiveState(); got != task.ObjectiveBlocked {
		t.Fatalf("objective state = %s, want %s", got, task.ObjectiveBlocked)
	}
	// The durable record carries the classification, not just the status: the
	// failed timeline event is what a diagnostic or a later recovery reads.
	var recorded bool
	for _, event := range current.Timeline {
		if event.Kind == task.TimelineFailed && strings.Contains(event.Summary, "CAPABILITY_UNAVAILABLE") {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("the block was not recorded with its class: %+v", current.Timeline)
	}
}

// The same objective on a runtime that can perform it does reach the model.
// Without this half, the preflight could silently refuse everything.
func TestBuildMutationReachesTheModelWhenTheCapabilityExists(t *testing.T) {
	root := t.TempDir()
	role := workspace.Build
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	var requests int32
	program := NewProgram(countingLoop(t, &requests), "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{Current: func() string { return string(role) }})
	program.SetTaskHooks(runtimeHooks(controller))

	done := make(chan struct{})
	go func() { defer close(done); program.runTurn("convert this project to Next.js") }()
	collectTurn(t, program)
	<-done

	if got := atomic.LoadInt32(&requests); got == 0 {
		t.Fatal("the provider was never called for a performable mutation objective")
	}
	current, ok := controller.Current()
	if !ok {
		t.Fatal("the turn recorded no task")
	}
	// A model that answers without changing anything is a review, never a
	// completion and never a capability block.
	if state := current.ObjectiveState(); state == task.ObjectiveBlocked || state == task.ObjectiveProven {
		t.Fatalf("objective state = %s, want a reviewable state", state)
	}
}
