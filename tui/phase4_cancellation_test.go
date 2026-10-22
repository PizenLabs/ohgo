package tui

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/jsonschema"
	"github.com/PizenLabs/ohgo/workspace"
)

// blockingSymbolResolver makes the post-run structural read observable. The
// first call is the pre-run snapshot; the second is the completion check.
type blockingSymbolResolver struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (r *blockingSymbolResolver) LocateSymbol(string) ([]agent.SymbolLocation, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == 2 {
		close(r.entered)
		<-r.release
	}
	return []agent.SymbolLocation{{
		Path: "tools/grep.go", Symbol: "GrepTool", QualifiedName: "tools.GrepTool", Line: 3, EndLine: 3,
	}}, nil
}

func (r *blockingSymbolResolver) LocatePackage(string) ([]agent.SymbolLocation, error) {
	return nil, nil
}

// postRunWriteTool and postRunVerifyTool are the smallest tools that produce
// the evidence the completion evaluator needs. Using them keeps the test on
// the cancellation seam instead of on a provider or a real test command.
type postRunWriteTool struct{ root string }

func (t *postRunWriteTool) Name() string                   { return "write" }
func (t *postRunWriteTool) Description() string            { return "write a file" }
func (t *postRunWriteTool) Parameters() *jsonschema.Schema { return nil }
func (t *postRunWriteTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierWrite} }
func (t *postRunWriteTool) Exclusive(map[string]any) bool  { return true }
func (t *postRunWriteTool) Execute(agent.Context, map[string]any) agent.Result {
	path := filepath.Join(t.root, "tools", "grep.go")
	if err := os.WriteFile(path, []byte("package tools\n\nfunc GrepTool() string { return \"new\" }\n"), 0o644); err != nil {
		return agent.ErrorResult("%v", err)
	}
	return agent.Result{Text: "wrote", Mutates: true, Details: map[string]any{"path": "tools/grep.go"}}
}

type postRunVerifyTool struct{}

func (t *postRunVerifyTool) Name() string                   { return "bash" }
func (t *postRunVerifyTool) Description() string            { return "run a check" }
func (t *postRunVerifyTool) Parameters() *jsonschema.Schema { return nil }
func (t *postRunVerifyTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierExec} }
func (t *postRunVerifyTool) Exclusive(map[string]any) bool  { return true }
func (t *postRunVerifyTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{
		Text: "ok", Executes: true, Verifies: true,
		Verification: &agent.VerificationEvidence{
			Command: "go test ./...", Check: contract.CheckTest,
			Scopes: []agent.VerificationScope{{All: true}},
		},
	}
}

// fixedResolver resolves the one symbol the cancellation fixture changes.
type fixedResolver struct{}

func (fixedResolver) LocateSymbol(string) ([]agent.SymbolLocation, error) {
	return []agent.SymbolLocation{{
		Path: "tools/grep.go", Symbol: "GrepTool", QualifiedName: "tools.GrepTool", Line: 3, EndLine: 3,
	}}, nil
}

func (fixedResolver) LocatePackage(string) ([]agent.SymbolLocation, error) { return nil, nil }

// A resumed review task completes only when the new turn carries evidence for
// the persisted contract: a change to the original target plus a check that
// covers it.
func TestResumedReviewCompletesWithPersistedContractEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "grep.go"),
		[]byte("package tools\n\nfunc GrepTool() string { return \"old\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := workspace.BuildIntentContract("Fix GrepTool in tools/grep.go and run go test ./tools")
	snapshot := agent.CaptureTargets(root, intent, fixedResolver{})

	call := func(id, name string) ai.AssistantMessage {
		return ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: map[string]any{}}},
			StopReason: ai.StopToolUse,
		}
	}
	index := 0
	messages := []ai.AssistantMessage{
		call("write-1", "write"),
		call("check-1", "bash"),
		{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	}
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		message := messages[index]
		index++
		return ai.NewCompletedStream(message), nil
	}
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   stream,
		Tools:    []agent.Tool{&postRunWriteTool{root: root}, &postRunVerifyTool{}},
		MaxTurns: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetCompletionContext(root, fixedResolver{})

	var (
		mu        sync.Mutex
		completed bool
		reviewed  bool
	)
	p.SetTaskHooks(TaskHooks{
		Current: func() (TaskResume, bool) {
			return TaskResume{
				ID: "t1", Status: "NEEDS_REVIEW", Objective: intent.Objective,
				Contract: intent, HasContract: true, Snapshot: snapshot,
			}, true
		},
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Complete: func() TaskProjection {
			mu.Lock()
			completed = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
		NeedsReview: func(string) TaskProjection {
			mu.Lock()
			reviewed = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "NEEDS_REVIEW"}
		},
	})

	p.runTurn("continue")

	mu.Lock()
	defer mu.Unlock()
	if reviewed {
		t.Fatal("a turn carrying the persisted contract's evidence was left in review")
	}
	if !completed {
		t.Fatal("the resumed task did not complete on the persisted contract's evidence")
	}
}

// A cancel that lands in the exact window between "not cancelled" and the
// terminal task transition must win. The hook runs after evidence evaluation
// and before the atomic claim, reproducing the old ctx.Err-then-Complete
// check-then-act window deterministically.
func TestCancelDuringPostRunTransitionPauses(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "grep.go"),
		[]byte("package tools\n\nfunc GrepTool() string { return \"old\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := func(id, name string) ai.AssistantMessage {
		return ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: map[string]any{}}},
			StopReason: ai.StopToolUse,
		}
	}
	index := 0
	messages := []ai.AssistantMessage{
		call("write-1", "write"),
		call("check-1", "bash"),
		{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	}
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		message := messages[index]
		index++
		return ai.NewCompletedStream(message), nil
	}

	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   stream,
		Tools:    []agent.Tool{&postRunWriteTool{root: root}, &postRunVerifyTool{}},
		MaxTurns: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetCompletionContext(root, fixedResolver{})

	var (
		mu        sync.Mutex
		completed bool
		paused    bool
	)
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
		Pause: func(string) TaskProjection {
			mu.Lock()
			paused = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "PAUSED"}
		},
	})
	p.beforeTaskTransition = func() {
		// The completion verdict is already complete here; this is the exact
		// pre-transition window the old check-then-act code exposed.
		p.cancelTurnNow("cancelled")
	}

	p.runTurn("Fix GrepTool in tools/grep.go and run go test ./tools")

	mu.Lock()
	defer mu.Unlock()
	if completed {
		t.Fatal("the task was completed after cancellation won the terminal claim")
	}
	if !paused {
		t.Fatal("cancellation in the transition window did not pause the task")
	}
}

// Cancellation that lands before a review transition pauses the task. Both
// states are non-terminal, but the human's stop is the truthful one.
func TestCancelDuringReviewTransitionPauses(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	var (
		mu     sync.Mutex
		paused bool
		review bool
	)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Pause: func(string) TaskProjection {
			mu.Lock()
			paused = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "PAUSED"}
		},
		NeedsReview: func(string) TaskProjection {
			mu.Lock()
			review = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "NEEDS_REVIEW"}
		},
	})
	p.beforeTaskTransition = func() { p.cancelTurnNow("cancelled") }

	p.runTurn("explain this")

	mu.Lock()
	defer mu.Unlock()
	if review {
		t.Fatal("a cancelled review transition was persisted as NEEDS_REVIEW")
	}
	if !paused {
		t.Fatal("cancellation before the review transition did not pause the task")
	}
}

// A cancel that lands after the last tool but while post-run evidence is being
// read must pause the task. The work was verified, but the user cancelled
// before the transition; completing it would make the durable task contradict
// the terminal UI state.
func TestRunTurnPausesWhenCancelledDuringPostRunVerification(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "grep.go"),
		[]byte("package tools\n\nfunc GrepTool() string { return \"old\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := func(id, name string) ai.AssistantMessage {
		return ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: map[string]any{}}},
			StopReason: ai.StopToolUse,
		}
	}
	index := 0
	messages := []ai.AssistantMessage{
		call("write-1", "write"),
		call("check-1", "bash"),
		{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	}
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		if index >= len(messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{}), nil
		}
		message := messages[index]
		index++
		return ai.NewCompletedStream(message), nil
	}

	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   stream,
		Tools:    []agent.Tool{&postRunWriteTool{root: root}, &postRunVerifyTool{}},
		MaxTurns: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &blockingSymbolResolver{entered: make(chan struct{}), release: make(chan struct{})}
	p := NewProgram(loop, "test", 0)
	p.SetCompletionContext(root, resolver)

	var (
		mu        sync.Mutex
		completed bool
		paused    bool
	)
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
		Pause: func(string) TaskProjection {
			mu.Lock()
			paused = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "PAUSED"}
		},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurn("Fix GrepTool in tools/grep.go and run go test ./...")
	}()

	select {
	case <-resolver.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("post-run verification never started")
	}
	p.cancelTurnNow("cancelled")
	close(resolver.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn did not return")
	}

	mu.Lock()
	defer mu.Unlock()
	if !p.state.Cancelled {
		t.Fatal("the turn was not recorded as cancelled")
	}
	if completed {
		t.Fatal("a task was completed after the user cancelled the turn")
	}
	if !paused {
		t.Fatal("a cancelled turn did not pause the task")
	}
}
