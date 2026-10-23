package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/charmbracelet/x/ansi"
)

// The projection is the TUI's whole task surface: it must read as one compact
// block with a single status column, or the user cannot tell done from active.
func TestTaskProjectionRendersCompactTodoBlock(t *testing.T) {
	projection := &TaskProjection{
		ID:        "t1",
		Status:    "RUNNING",
		Objective: "ship phase 1",
		Items: []TaskItem{
			{State: taskStateDone, Label: "wrote the domain"},
			{State: taskStateActive, Label: "wiring the loop"},
			{State: taskStatePending, Label: "validate"},
		},
	}
	lines := taskLines(projection, DarkPalette(), Layout{ContentWidth: 80})
	joined := ansi.Strip(strings.Join(lines, "\n"))

	if !strings.Contains(joined, "ship phase 1") || !strings.Contains(joined, "running") {
		t.Fatalf("header is missing objective or status:\n%s", joined)
	}
	for _, want := range []string{"✔ wrote the domain", "◼ wiring the loop", "◻ validate"} {
		if !strings.Contains(joined, want) {
			t.Errorf("projection is missing %q:\n%s", want, joined)
		}
	}
	// One continuation column: the first todo hangs off ⎿, the rest align.
	if !strings.Contains(joined, "⎿ ✔") {
		t.Errorf("the first todo is not on the continuation column:\n%s", joined)
	}
}

// The recap belongs to the tail of the stream, not the task header. A completed
// projection must leave the header block unchanged, or a long answer pushes the
// closure line off the top of the screen.
func TestTaskHeaderNeverRendersRecap(t *testing.T) {
	projection := &TaskProjection{
		ID:        "t1",
		Status:    "COMPLETED",
		Objective: "Implemented concurrent thread execution",
		Items:     []TaskItem{{State: taskStateDone, Label: "wired the scheduler"}},
		Recap:     "Updated the scheduler and verified the change.",
	}
	joined := ansi.Strip(strings.Join(taskLines(projection, DarkPalette(), Layout{ContentWidth: 80}), "\n"))
	if strings.Contains(joined, recapPrefix) || strings.Contains(joined, "Updated the scheduler") {
		t.Fatalf("the task header still renders completion metadata:\n%s", joined)
	}
	if !strings.Contains(joined, "completed") {
		t.Fatalf("the task header lost its lifecycle status:\n%s", joined)
	}
}

// The footer is emitted only after the task completed and the stream settled:
// a completed projection arriving mid-answer must wait for AssistantDone.
func TestTaskRecapWaitsForSettledStream(t *testing.T) {
	state := New()
	state.Apply(UserSubmitted{Text: "what is golang and for example code"})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "explain Go",
	}})
	state.Apply(AssistantDelta{Text: "a long answer"})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "COMPLETED", Objective: "explain Go",
		Recap: "Explained the core idea: goroutines are lightweight threads.",
	}})
	if countRecapEntries(state) != 0 {
		t.Fatal("the recap emitted while the answer was still streaming")
	}

	state.Apply(AssistantDone{})
	if got := countRecapEntries(state); got != 1 {
		t.Fatalf("recap entries after settle = %d, want 1", got)
	}
	last := state.Entries[len(state.Entries)-1]
	if last.Kind != EntryRecap || last.Recap != "Explained the core idea: goroutines are lightweight threads." {
		t.Fatalf("last entry = %+v, want the trailing recap", last)
	}
}

// A task that has not completed must never emit a footer, even if it carries a
// stale recap string.
func TestTaskRecapRequiresCompletion(t *testing.T) {
	state := New()
	state.Apply(UserSubmitted{Text: "ship it"})
	state.Apply(AssistantDelta{Text: "working"})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "ship it",
		Recap: "Updated the release config and verified the build.",
	}})
	state.Apply(AssistantDone{})
	if got := countRecapEntries(state); got != 0 {
		t.Fatalf("a running task emitted %d recap entries", got)
	}

	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "COMPLETED", Objective: "ship it",
		Recap: "Updated the release config and verified the build.",
	}})
	if got := countRecapEntries(state); got != 1 {
		t.Fatalf("a settled completed task emitted %d recap entries, want 1", got)
	}
}

// Repeated projections and duplicate turn boundaries must not stack footers.
func TestTaskRecapEmitsOncePerTask(t *testing.T) {
	completed := TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "COMPLETED", Objective: "ship it",
		Recap: "Updated the release config and verified the build.",
	}}
	state := New()
	state.Apply(UserSubmitted{Text: "ship it"})
	state.Apply(completed)
	state.Apply(AssistantDone{})
	state.Apply(completed)
	state.Apply(AssistantDone{})
	if got := countRecapEntries(state); got != 1 {
		t.Fatalf("recap entries = %d, want exactly one", got)
	}
}

// Back-to-back tasks each close with one footer, and each footer follows its
// own answer rather than the next task's header.
func TestBackToBackTasksEmitOneFooterEach(t *testing.T) {
	state := New()
	run := func(id, answer, recap string) {
		state.Apply(UserSubmitted{Text: "task " + id})
		state.Apply(TaskProjected{Projection: TaskProjection{
			ID: id, Status: "RUNNING", Objective: id,
		}})
		state.Apply(AssistantDelta{Text: answer})
		state.Apply(TaskProjected{Projection: TaskProjection{
			ID: id, Status: "COMPLETED", Objective: id, Recap: recap,
		}})
		state.Apply(AssistantDone{})
	}
	run("t1", "first answer", "Explained the first result.")
	run("t2", "second answer", "Explained the second result.")

	if got := countRecapEntries(state); got != 2 {
		t.Fatalf("recap entries = %d, want one per task", got)
	}
	joined := ansi.Strip(strings.Join(entryTexts(state), "\n"))
	firstFooter := strings.Index(joined, "Explained the first result")
	secondAnswer := strings.Index(joined, "second answer")
	secondFooter := strings.Index(joined, "Explained the second result")
	if firstFooter < 0 || secondFooter < 0 {
		t.Fatalf("a task footer is missing:\n%s", joined)
	}
	if firstFooter > secondAnswer {
		t.Fatalf("the first footer is not at the end of its own output:\n%s", joined)
	}
	if secondFooter < secondAnswer {
		t.Fatalf("the second footer is not below its answer:\n%s", joined)
	}
}

// entryTexts projects the document to plain text for order assertions.
func entryTexts(state State) []string {
	out := make([]string, 0, len(state.Entries))
	for _, entry := range state.Entries {
		switch entry.Kind {
		case EntryUser:
			out = append(out, entry.UserText)
		case EntryAssistant:
			out = append(out, entry.AssistantText)
		case EntryRecap:
			out = append(out, entry.Recap)
		}
	}
	return out
}

// The footer is display metadata: the task block copy and the footer block copy
// both stay out of the clipboard payload.
func TestTaskRecapIsNotPartOfBlockCopy(t *testing.T) {
	entry := Entry{Kind: EntryTask, Task: &TaskProjection{
		ID:        "t1",
		Status:    "COMPLETED",
		Objective: "Implemented concurrent thread execution",
		Items:     []TaskItem{{State: taskStateDone, Label: "wired the scheduler"}},
		Recap:     "Updated the scheduler and verified the change.",
	}}
	got := BlockCopyText(entry)
	if strings.Contains(got, recapPrefix) || strings.Contains(got, "Updated the scheduler") {
		t.Fatalf("block copy leaked the recap metadata:\n%s", got)
	}
	if !strings.Contains(got, "Implemented concurrent thread execution") {
		t.Fatalf("block copy lost the task objective:\n%s", got)
	}
	if got := BlockCopyText(Entry{Kind: EntryRecap, Recap: "Updated the release config."}); got != "" {
		t.Fatalf("footer copy = %q, want empty metadata", got)
	}
}

// On a long answer the footer must remain visible under the answer: the viewport
// follows the tail, so scrolling up reveals content above the recap, never below.
func TestLongAnswerKeepsRecapAtTheBottom(t *testing.T) {
	state := New()
	state.Apply(TerminalResized{Width: 80, Height: 30})
	state.Apply(UserSubmitted{Text: "what is golang and for example code"})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "explain Go",
	}})
	var answer strings.Builder
	for i := 0; i < 40; i++ {
		answer.WriteString("paragraph " + strings.Repeat("x", 20) + "\n\n")
	}
	answer.WriteString("ANSWER_END\n")
	state.Apply(AssistantDelta{Text: answer.String()})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "COMPLETED", Objective: "explain Go",
		Recap: "Explained the core idea: goroutines are lightweight threads.",
	}})
	state.Apply(AssistantDone{})

	frame := ansi.Strip(Render(state))
	answerAt := strings.LastIndex(frame, "ANSWER_END")
	recapAt := strings.LastIndex(frame, recapPrefix+" Explained the core idea: goroutines are lightweight threads.")
	if recapAt < 0 {
		t.Fatalf("the footer is missing from the rendered frame:\n%s", frame)
	}
	if answerAt < 0 || recapAt < answerAt {
		t.Fatalf("the footer is not below the answer (answer=%d, recap=%d):\n%s", answerAt, recapAt, frame)
	}
	if got := colorName(DarkPalette().Recap.GetForeground()); got != "#5C7E70" {
		t.Errorf("recap foreground = %q, want the dim token #5C7E70", got)
	}
}

// countRecapEntries reports how many trailing lifecycle footers the document
// holds, without depending on a particular rendering.
func countRecapEntries(state State) int {
	n := 0
	for _, entry := range state.Entries {
		if entry.Kind == EntryRecap {
			n++
		}
	}
	return n
}

// A projection updates the one task block in place, so a long turn cannot stack
// a fresh panel on every todo change.
func TestTaskProjectedReplacesInPlace(t *testing.T) {
	state := New()
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "first",
		Items: []TaskItem{{State: taskStatePending, Label: "a"}},
	}})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "PAUSED", Objective: "first",
		Items: []TaskItem{{State: taskStateDone, Label: "a"}},
	}})

	tasks := 0
	var block *TaskProjection
	for i := range state.Entries {
		if state.Entries[i].Kind == EntryTask {
			tasks++
			block = state.Entries[i].Task
		}
	}
	if tasks != 1 {
		t.Fatalf("task entries = %d, want 1 (the projection replaces in place)", tasks)
	}
	if block == nil || block.Status != "PAUSED" || block.Items[0].State != taskStateDone {
		t.Fatalf("stale projection survived: %+v", block)
	}
}

// An interruption must pause the task and must never report completion: the
// runtime observed a cancellation, not a finished objective.
func TestRunTurnPausesTaskOnInterruption(t *testing.T) {
	begun := make(chan struct{})
	var (
		mu        sync.Mutex
		paused    bool
		completed bool
	)
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			close(begun)
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Pause: func(string) TaskProjection {
			mu.Lock()
			paused = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "PAUSED"}
		},
		Complete: func() TaskProjection {
			mu.Lock()
			completed = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("do a long thing") }()

	select {
	case <-begun:
	case <-time.After(2 * time.Second):
		t.Fatal("the task was never begun")
	}
	p.cancelTurnNow("cancelled")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn did not return after cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	if !paused {
		t.Error("interruption did not pause the task")
	}
	if completed {
		t.Error("interruption was reported as completion")
	}
}

// A run whose final turn stopped calling tools completes the task.
func TestRunTurnCompletesTaskOnFinalTurn(t *testing.T) {
	var (
		mu        sync.Mutex
		completed bool
	)
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "all done"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Complete: func() TaskProjection {
			mu.Lock()
			completed = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("finish it") }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn hung")
	}

	mu.Lock()
	defer mu.Unlock()
	if !completed {
		t.Error("a run whose final turn stopped calling tools did not complete the task")
	}
}

// The completion transition is the only place the recap is attached, and it
// carries the run's own duration and measured token usage.
func TestRunTurnAttachesRecapToCompletedTask(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "one goroutine owns the scheduler"}},
				Usage:   ai.Usage{Input: 10, Output: 5, Total: 15},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING", Objective: "explain the scheduler"}
		},
		Complete: func() TaskProjection {
			return TaskProjection{ID: "t1", Status: "COMPLETED", Objective: "explain the scheduler"}
		},
	})

	p.runTurn("explain the scheduler")

	// runTurn publishes events for the update goroutine; with no running
	// program the test folds them itself, exactly as Update would.
drain:
	for {
		select {
		case event := <-p.events:
			p.state.Apply(event)
		default:
			break drain
		}
	}

	var task *TaskProjection
	taskAt := -1
	for i := range p.state.Entries {
		if p.state.Entries[i].Kind == EntryTask {
			task = p.state.Entries[i].Task
			taskAt = i
		}
	}
	if task == nil {
		t.Fatal("the completed turn published no task block")
	}
	if task.Status != "COMPLETED" {
		t.Fatalf("task status = %q, want COMPLETED", task.Status)
	}
	if !strings.Contains(task.Recap, "goroutine owns the scheduler") {
		t.Fatalf("recap = %q, want the answer's own subject", task.Recap)
	}
	if strings.Contains(task.Recap, "explain the scheduler") {
		t.Fatalf("recap = %q, echoed the objective instead of narrating the answer", task.Recap)
	}
	if strings.Contains(task.Recap, "tokens") || strings.Contains(task.Recap, "0.8s") || strings.Contains(task.Recap, "•") {
		t.Fatalf("recap = %q, leaked telemetry instead of a narrative", task.Recap)
	}

	footerAt := -1
	for i := range p.state.Entries {
		if p.state.Entries[i].Kind == EntryRecap {
			footerAt = i
		}
	}
	if footerAt < 0 {
		t.Fatalf("the completed turn appended no recap footer: %+v", p.state.Entries)
	}
	if footerAt < taskAt || footerAt != len(p.state.Entries)-1 {
		t.Fatalf("recap footer at %d is not the tail (task at %d, entries %d)", footerAt, taskAt, len(p.state.Entries))
	}
	footer := p.state.Entries[footerAt].Recap
	if !strings.HasPrefix(recapLine(footer), recapPrefix+" ") {
		t.Fatalf("footer = %q, want the stable recap prefix", recapLine(footer))
	}
	header := ansi.Strip(strings.Join(taskLines(task, DarkPalette(), Layout{ContentWidth: 100}), "\n"))
	if strings.Contains(header, recapPrefix) {
		t.Fatalf("the task header still rendered the recap:\n%s", header)
	}
}

// A continuation of a review task is judged against the persisted contract, not
// the continuation prompt. A clean informational answer cannot complete a
// mutation objective that still has no observed change.
func TestResumedReviewUsesPersistedContract(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "explained"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	persisted := contract.IntentContract{
		Objective: "fix note.txt",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "note.txt"}},
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyAny,
		}},
	}
	var (
		mu        sync.Mutex
		begunWith contract.IntentContract
		completed bool
		reviewed  string
		began     bool
	)
	p.SetTaskHooks(TaskHooks{
		Current: func() (TaskResume, bool) {
			return TaskResume{
				ID:          "t1",
				Status:      "NEEDS_REVIEW",
				Objective:   persisted.Objective,
				Contract:    persisted,
				HasContract: true,
				Snapshot:    &agent.TargetSnapshot{Root: t.TempDir()},
			}, true
		},
		Begin: func(intent contract.IntentContract, _ *agent.TargetSnapshot) TaskProjection {
			mu.Lock()
			began = true
			begunWith = intent
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Complete: func() TaskProjection {
			mu.Lock()
			completed = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
		NeedsReview: func(reason string) TaskProjection {
			mu.Lock()
			reviewed = reason
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "NEEDS_REVIEW"}
		},
	})

	p.runTurn("continue")

	mu.Lock()
	defer mu.Unlock()
	if !began {
		t.Fatal("the continuation did not begin the task")
	}
	if begunWith.Objective != persisted.Objective || begunWith.Kind != persisted.Kind {
		t.Fatalf("continuation used %+v, want the persisted contract", begunWith)
	}
	if completed {
		t.Fatal("a mutation review task completed on an informational answer")
	}
	if reviewed == "" {
		t.Fatal("the unresolved task was not left in review")
	}
}

// A prompt with a different objective starts a new task; it is never blocked
// by the durable task and never silently attached to it. The old task is
// detached (preserved) and the new intent runs with its own contract.
func TestResumedReviewRefusesDifferentObjective(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	var (
		detached bool
		began    bool
	)
	p.SetTaskHooks(TaskHooks{
		Current: func() (TaskResume, bool) {
			return TaskResume{
				ID: "t1", Status: "NEEDS_REVIEW", Objective: "fix note.txt", HasContract: true,
				Contract: contract.IntentContract{
					Objective: "fix note.txt",
					Kind:      contract.ObjectiveMutation,
					Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "note.txt"}},
				},
			}, true
		},
		Detach: func() TaskProjection {
			detached = true
			return TaskProjection{ID: "t1", Status: "PAUSED", Objective: "fix note.txt"}
		},
		Begin: func(intent contract.IntentContract, _ *agent.TargetSnapshot) TaskProjection {
			began = true
			if intent.Objective != "what does this project do?" {
				t.Errorf("new task began with %q, want the new intent", intent.Objective)
			}
			return TaskProjection{ID: "t2", Status: "RUNNING", Objective: intent.Objective}
		},
		Pause: func(string) TaskProjection {
			return TaskProjection{ID: "t1", Status: "PAUSED"}
		},
		NeedsReview: func(string) TaskProjection {
			return TaskProjection{ID: "t2", Status: "NEEDS_REVIEW"}
		},
	})

	p.runTurn("what does this project do?")

	if !detached {
		t.Error("a new intent did not detach the durable task")
	}
	if !began {
		t.Error("a new intent did not begin a new task")
	}
}

// /task cancel is the explicit abandonment path: it never reports completion.
func TestTaskCommandCancelDoesNotComplete(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var completed, cancelled bool
	p.SetTaskHooks(TaskHooks{
		Current: func() (TaskResume, bool) {
			return TaskResume{ID: "t1", Status: "NEEDS_REVIEW", Objective: "fix note.txt"}, true
		},
		Cancel: func(string) TaskProjection {
			cancelled = true
			return TaskProjection{ID: "t1", Status: "CANCELLED"}
		},
		Complete: func() TaskProjection {
			completed = true
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
	})
	p.taskCommand([]string{"cancel"})
	if !cancelled {
		t.Fatal("/task cancel did not close the task")
	}
	if completed {
		t.Fatal("/task cancel claimed completion")
	}
}

// A task projection is display-only: it must never flip execution state. This
// is the negative half of "TUI and task.md are projections, not authorities".
func TestTaskProjectionDoesNotChangeExecutionState(t *testing.T) {
	state := New()
	state.Apply(UserSubmitted{Text: "go", Seq: 1})
	if !state.Status.Busy {
		t.Fatal("the turn did not start busy")
	}
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "PAUSED", Objective: "x",
		Items: []TaskItem{{State: taskStatePending, Label: "pending"}},
	}})
	if !state.Status.Busy {
		t.Error("a paused task projection stopped the running turn")
	}
	if state.Cancelled {
		t.Error("a task projection cancelled the turn")
	}
}

// Context reconstruction must reach the model: the task orientation is injected
// as system context on the turn, not kept as a UI decoration.
func TestRunTurnInjectsTaskContext(t *testing.T) {
	var (
		mu     sync.Mutex
		system []ai.SystemPromptBlock
	)
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			mu.Lock()
			system = req.System
			mu.Unlock()
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Context: func() []string { return []string{"Objective: ship phase 1", "Next action: run tests"} },
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("continue") }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn hung")
	}

	mu.Lock()
	defer mu.Unlock()
	var joined strings.Builder
	for _, block := range system {
		joined.WriteString(block.Text)
		joined.WriteString("\n")
	}
	got := joined.String()
	for _, want := range []string{"Task continuity", "Objective: ship phase 1", "Next action: run tests"} {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt is missing %q:\n%s", want, got)
		}
	}
}
