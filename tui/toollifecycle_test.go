package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/jsonschema"
)

// execTool runs through the real agent execute path and returns canned text,
// so the TUI tests prove the bridge maps real progress to real blocks.
type execTool struct {
	name    string
	text    string
	isErr   bool
	args    []map[string]any
	updates []string
}

func (t *execTool) Name() string        { return t.name }
func (t *execTool) Description() string { return "lifecycle test tool" }
func (t *execTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"},"command":{"type":"string"}}}`))
}
func (t *execTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (t *execTool) Exclusive(map[string]any) bool { return false }
func (t *execTool) Execute(ctx agent.Context, args map[string]any) ResultAlias {
	t.args = append(t.args, args)
	for _, u := range t.updates {
		if ctx.OnProgress != nil {
			ctx.OnProgress(agent.Result{Text: u})
		}
	}
	return agent.Result{Text: t.text, IsError: t.isErr}
}

// ResultAlias keeps the tool signature obvious without importing agent twice.
type ResultAlias = agent.Result

// runTurnEvents drives one turn and applies every published event, returning
// the transcript. It mirrors what Update does with the event channel.
func runTurnEvents(t *testing.T, p *Program, prompt string) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); p.runTurn(prompt) }()
	timeout := time.After(10 * time.Second)
	pending := true
	for pending {
		select {
		case <-done:
			pending = false
		case event := <-p.events:
			p.state.Apply(event)
		case <-timeout:
			t.Fatal("runTurn hung: no completion and no events")
		}
	}
	for {
		select {
		case event := <-p.events:
			p.state.Apply(event)
		default:
			return
		}
	}
}

func toolLoop(t *testing.T, stream ai.StreamFn, tools ...agent.Tool) *Program {
	t.Helper()
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  tools,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return NewProgram(loop, "test", 0)
}

func completedCalls(calls ...ai.ToolCall) ai.StreamFn {
	blocks := make([]ai.ContentBlock, 0, len(calls))
	for _, c := range calls {
		blocks = append(blocks, c)
	}
	first := ai.AssistantMessage{Content: blocks, StopReason: ai.StopToolUse}
	second := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "all done"}},
		StopReason: ai.StopEndTurn,
	}
	var n int
	return func(context.Context, ai.Request) (*ai.Stream, error) {
		n++
		if n == 1 {
			return ai.NewCompletedStream(first), nil
		}
		return ai.NewCompletedStream(second), nil
	}
}

// A real tool start must open a running block carrying what the call does.
func TestRunTurnShowsToolStart(t *testing.T) {
	tool := &execTool{name: "read", text: "package auth\nline2\nline3\n"}
	p := toolLoop(t,
		completedCalls(ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "internal/auth/auth.go"}}),
		tool)
	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("inspect auth") }()
	var started *ToolStarted
	timeout := time.After(10 * time.Second)
loop:
	for {
		select {
		case <-done:
			break loop
		case event := <-p.events:
			if s, ok := event.(ToolStarted); ok && s.ID == "c1" {
				cpy := s
				started = &cpy
			}
			p.state.Apply(event)
		case <-timeout:
			t.Fatal("runTurn hung: no completion and no events")
		}
	}
	// The loop above can observe `done` before it has read every buffered
	// event, so the start may still be waiting in the channel. Keep looking
	// while draining: the turn has returned, so nothing new can arrive.
	for {
		select {
		case event := <-p.events:
			if s, ok := event.(ToolStarted); ok && s.ID == "c1" {
				cpy := s
				started = &cpy
			}
			p.state.Apply(event)
		default:
			goto drained
		}
	}
drained:
	if started == nil {
		t.Fatal("no ToolStarted for c1: a real execution left no visible start")
	}
	if started.Name != "read" {
		t.Errorf("start name = %q, want read", started.Name)
	}
	if !strings.Contains(started.Summary, "internal/auth/auth.go") {
		t.Errorf("start summary = %q, want the call's path", started.Summary)
	}
	block := p.state.tool("c1")
	if block == nil {
		t.Fatal("no tool block for c1 after the turn")
	}
	// The finish replaces the args summary with the compact outcome.
	if block.Summary != "3 lines" {
		t.Errorf("final summary = %q, want the compact outcome", block.Summary)
	}
}

// Start and finish correlate by call id, and the finish keeps the failure
// bit so an error never renders as a success.
func TestRunTurnCorrelatesFinishAndFailure(t *testing.T) {
	tool := &execTool{name: "bash", text: "boom\n", isErr: true}
	p := toolLoop(t,
		completedCalls(ai.ToolCall{ID: "c7", Name: "bash", Arguments: map[string]any{"command": "exit 1"}}),
		tool)
	runTurnEvents(t, p, "run it")

	block := p.state.tool("c7")
	if block == nil {
		t.Fatal("no tool block for c7")
	}
	if block.Status != ToolError {
		t.Errorf("status = %q, want error: a failure rendered as a success", block.Status)
	}
	if block.Status == ToolDone {
		t.Error("an error result rendered as a success")
	}
}

// Two sequential calls keep separate identities: the second finish must not
// touch the first block.
func TestRunTurnKeepsSequentialToolsSeparate(t *testing.T) {
	tool := &execTool{name: "read", text: "x"}
	p := toolLoop(t,
		completedCalls(
			ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.go"}},
			ai.ToolCall{ID: "c2", Name: "read", Arguments: map[string]any{"path": "b.go"}},
		),
		tool)
	runTurnEvents(t, p, "read both")

	first, second := p.state.tool("c1"), p.state.tool("c2")
	if first == nil || second == nil {
		t.Fatalf("blocks = %v, %v, want one per call id", first, second)
	}
	if first == second {
		t.Fatal("both call ids resolve to the same block: a finish overwrote its sibling")
	}
	if first.Status != ToolDone || second.Status != ToolDone {
		t.Errorf("statuses = %q, %q, want done, done", first.Status, second.Status)
	}
}

// A duplicate start announcement must not open a second block.
func TestApplyDedupesDuplicateStarts(t *testing.T) {
	s := New()
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: now})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: now})
	count := 0
	for _, e := range s.Entries {
		if e.Kind == EntryTool {
			count++
		}
	}
	if count != 1 {
		t.Errorf("tool entries = %d, want 1: an empty partial opened a duplicate block", count)
	}
}

// Assistant prose about a tool must never open a block. Only the executor
// can do that.
func TestAssistantProseCreatesNoToolBlock(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "run the tests"})
	s.Apply(AssistantDelta{Text: "I'll run go test ./... and inspect auth.go next"})
	s.Apply(AssistantDone{})
	for _, e := range s.Entries {
		if e.Kind == EntryTool {
			t.Errorf("assistant prose opened a tool block for %+v: events must originate at execution", e.Tool)
		}
	}
}

// Compact grammar: running shows the spinner state, success a check with
// its summary, failure a cross that stays a failure.
func TestToolBlockRenderStates(t *testing.T) {
	now := time.Now()
	running := New()
	running.Apply(TerminalResized{Width: 80, Height: 24})
	running.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: now})
	out := stripForTest(t, Render(running))
	if !strings.Contains(out, "bash") || !strings.Contains(out, "running") {
		t.Errorf("running block = %q, want the tool name and its running state", out)
	}
	if !strings.Contains(out, "go test") {
		t.Errorf("running block = %q, want the call's summary", out)
	}

	ok := New()
	ok.Apply(TerminalResized{Width: 80, Height: 24})
	ok.Apply(ToolStarted{ID: "c1", Name: "read", At: now})
	ok.Apply(ToolFinished{ID: "c1", Text: "a\nb\nc\n", Summary: "3 lines", At: now})
	good := stripForTest(t, Render(ok))
	if !strings.Contains(good, "read") {
		t.Errorf("success block lost the tool name:\n%s", good)
	}
	if !strings.Contains(good, "3 lines") {
		t.Errorf("success block = %q, want the compact summary", good)
	}

	bad := New()
	bad.Apply(TerminalResized{Width: 80, Height: 24})
	bad.Apply(ToolStarted{ID: "c1", Name: "bash", At: now})
	bad.Apply(ToolFinished{ID: "c1", Text: "exit code 1", Summary: "error: exit code 1", IsError: true, At: now})
	failed := stripForTest(t, Render(bad))
	if strings.Contains(failed, "✓ bash") {
		t.Errorf("failure rendered as a success:\n%s", failed)
	}
	if !strings.Contains(failed, "error") {
		t.Errorf("failure block = %q, want the error summary", failed)
	}
}

// Compact summaries are deterministic: line counts for multi-line output,
// the first line for errors, nothing for trivial output.
func TestCompactResultSummary(t *testing.T) {
	if got := CompactResultSummary("a\nb\nc\n", false); got != "3 lines" {
		t.Errorf("multi-line summary = %q, want 3 lines", got)
	}
	if got := CompactResultSummary("single", false); got != "" {
		t.Errorf("single-line summary = %q, want empty (the body says it)", got)
	}
	if got := CompactResultSummary("", false); got != "" {
		t.Errorf("empty summary = %q, want empty", got)
	}
	if got := CompactResultSummary("exit code 1\nmore", true); !strings.Contains(got, "exit code 1") {
		t.Errorf("error summary = %q, want the first error line", got)
	}
	if got := CompactResultSummary("", true); got != "error" {
		t.Errorf("empty error summary = %q, want error", got)
	}
}

// Tool blocks travel the output-first path: a running tool stays live and
// a finished one seals into history, with no mouse modes and no viewport
// slicing of history.
func TestToolBlocksThroughLivePath(t *testing.T) {
	p, painter := newLiveProgram()
	var history []string
	history = append(history, liveEvent(p, UserSubmitted{Text: "check auth"})...)
	running := liveEvent(p, ToolStarted{ID: "c1", Name: "read", Summary: "read internal/auth/auth.go", At: time.Now()})
	_ = running
	// A running tool is unstable: it must be offered in the live region,
	// never silently sealed as if it were done.
	live := painter.view()
	if !strings.Contains(stripANSI(live), "read") {
		t.Errorf("live region = %q, want the running tool visible while it executes", live)
	}
	history = append(history, liveEvent(p, ToolFinished{ID: "c1", Text: "package auth\n", Summary: "", At: time.Now()})...)
	history = append(history, liveEvent(p, AssistantDelta{Text: "checked"})...)
	history = append(history, liveEvent(p, AssistantDone{})...)

	plain := stripANSI(strings.Join(history, "\n"))
	if !strings.Contains(plain, "read") {
		t.Errorf("sealed history lost the tool block:\n%s", plain)
	}
	for _, seq := range mouseModeSequences {
		if strings.HasSuffix(seq, "h") && strings.Contains(strings.Join(history, "\n"), seq) {
			t.Errorf("live output requested %q: tool visibility must not cost terminal ownership", seq)
		}
	}
}
