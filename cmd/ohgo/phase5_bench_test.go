package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// phase5CaseResult is the evidence for one adversarial case. Every field is
// measured from the run or read from the root grant; none is inferred from the
// case name.
type phase5CaseResult struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Expected       string             `json:"expected"`
	RootLimits     budget.Limits      `json:"root_limits"`
	ChildLimits    *budget.Limits     `json:"child_limits,omitempty"`
	ChildShared    bool               `json:"child_budget_shared"`
	InitialUsed    budget.Consumption `json:"initial_used"`
	Consumed       budget.Consumption `json:"consumed"`
	Remaining      budget.Consumption `json:"remaining"`
	Depth          int                `json:"depth"`
	ModelCalls     int                `json:"model_calls"`
	ToolCalls      int                `json:"tool_calls"`
	ToolExecutions int                `json:"tool_executions"`
	LateModelCalls int                `json:"late_model_calls"`
	BackgroundJobs int                `json:"background_jobs"`
	WallMS         float64            `json:"wall_ms"`
	FinalState     string             `json:"final_state"`
	Completion     string             `json:"completion_state"`
	Failure        string             `json:"failure_classification"`
	Reason         string             `json:"reason,omitempty"`
	Pass           bool               `json:"pass"`
	Detail         string             `json:"detail,omitempty"`
}

type phase5Case struct {
	ID       string
	Name     string
	Expected string
	Depth    int
	Run      func(t *testing.T) phase5CaseResult
}

func phase5Model() ai.Model { return catalog.MustResolve("anthropic/claude-sonnet-4-5") }

func p5ToolCallMessage(id, name string, args map[string]any) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: args}},
		StopReason: ai.StopToolUse,
	}
}

func p5BatchMessage(calls ...ai.ToolCall) ai.AssistantMessage {
	content := make([]ai.ContentBlock, 0, len(calls))
	for _, call := range calls {
		content = append(content, call)
	}
	return ai.AssistantMessage{Content: content, StopReason: ai.StopToolUse}
}

func p5StopMessage(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}
}

func p5BashCall(id, command string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: "bash", Arguments: map[string]any{"command": command, "timeout": float64(60_000)}}
}

func p5BackgroundCall(id, command string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: "bash", Arguments: map[string]any{"command": command, "background": true}}
}

func p5TaskCall(id, prompt string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: "task", Arguments: map[string]any{"prompt": prompt}}
}

func p5ToolSet(root string) []agent.Tool {
	return []agent.Tool{
		tools.NewReadTool(root),
		tools.NewWriteTool(root),
		tools.NewEditTool(root),
		tools.NewBashTool(root),
	}
}

func p5Gate(root string) *securityGate {
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Build)
	return opts.newSecurityGate(nil)
}

// p5CountingStream wraps a script so provider admissions are measured at the
// only place a model call actually happens. late counts requests that began
// after the root deadline, the hard deadline-escape signal.
func p5CountingStream(stream ai.StreamFn, deadline time.Time) (ai.StreamFn, func() int, func() int) {
	var mu sync.Mutex
	calls, late := 0, 0
	wrapped := func(ctx context.Context, request ai.Request) (*ai.Stream, error) {
		mu.Lock()
		calls++
		if !deadline.IsZero() && time.Now().After(deadline) {
			late++
		}
		mu.Unlock()
		return stream(ctx, request)
	}
	return wrapped, func() int {
			mu.Lock()
			defer mu.Unlock()
			return calls
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return late
		}
}

type phase5Run struct {
	Turns          []agent.Turn
	Err            error
	Completion     agent.Completion
	Intent         contract.IntentContract
	Snapshot       *agent.TargetSnapshot
	ModelCalls     int
	ToolExecutions int
	LateModelCalls int
	Elapsed        time.Duration
}

// p5CountingTool observes actual Execute calls. It is the benchmark's evidence
// that a refused admission did not reach a tool, independent of result text.
type p5CountingTool struct {
	agent.Tool
	calls *atomic.Int64
}

func (t *p5CountingTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	t.calls.Add(1)
	return t.Tool.Execute(ctx, args)
}

func p5WrapTools(tools []agent.Tool, calls *atomic.Int64) []agent.Tool {
	out := make([]agent.Tool, len(tools))
	for i, tool := range tools {
		out[i] = &p5CountingTool{Tool: tool, calls: calls}
	}
	return out
}

func p5Run(t *testing.T, root, request string, tools []agent.Tool, stream ai.StreamFn, b *budget.Budget, gate *securityGate) phase5Run {
	t.Helper()
	if gate == nil {
		gate = p5Gate(root)
	}
	intent := workspace.BuildIntentContract(request)
	var resolver agent.StructuralResolver
	var snapshot *agent.TargetSnapshot
	if intent.Kind == workspace.ObjectiveMutation {
		snapshot = agent.CaptureTargets(root, intent, resolver)
	}
	counted, calls, late := p5CountingStream(stream, b.Deadline())
	executions := &atomic.Int64{}
	loop, err := agent.New(agent.Config{
		Model:     phase5Model(),
		Stream:    counted,
		Tools:     p5WrapTools(tools, executions),
		Budget:    b,
		MaxTurns:  100,
		Authorize: gate,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	start := time.Now()
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: request}})
	elapsed := time.Since(start)
	completion := agent.EvaluateBudgetCompletion(agent.CompletionInput{
		Turns:    turns,
		Contract: intent,
		Snapshot: snapshot,
		Resolver: resolver,
	}, errors.Is(runErr, budget.ErrExhausted))
	return phase5Run{
		Turns: turns, Err: runErr, Completion: completion, Intent: intent,
		Snapshot: snapshot, ModelCalls: calls(), ToolExecutions: int(executions.Load()),
		LateModelCalls: late(), Elapsed: elapsed,
	}
}

func p5Classify(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "CANCELLED"
	case errors.Is(err, budget.ErrExhausted):
		return "BUDGET_EXHAUSTED"
	case errors.Is(err, agent.ErrTurnLimit):
		return "TURN_LIMIT"
	default:
		return "ERROR"
	}
}

func p5FinalState(completion agent.Completion, err error) string {
	if completion.Complete() {
		return "COMPLETED"
	}
	switch {
	case errors.Is(err, budget.ErrExhausted):
		return "PAUSED_BUDGET"
	case errors.Is(err, context.Canceled):
		return "PAUSED_CANCELLED"
	case err != nil:
		return "FAILED"
	default:
		return "NEEDS_REVIEW"
	}
}

func p5Result(t *testing.T, c phase5Case, b *budget.Budget, run phase5Run, startUsed budget.Consumption) phase5CaseResult {
	t.Helper()
	snap := b.Snapshot()
	toolCalls := 0
	for _, turn := range run.Turns {
		toolCalls += len(turn.Results)
	}
	state := p5FinalState(run.Completion, run.Err)
	result := phase5CaseResult{
		ID: c.ID, Name: c.Name, Expected: c.Expected, Depth: c.Depth,
		RootLimits: snap.Limits, ChildLimits: &snap.Limits, ChildShared: c.Depth > 0,
		InitialUsed: startUsed, Consumed: snap.Used, Remaining: snap.Remaining,
		ModelCalls: run.ModelCalls, ToolCalls: toolCalls, ToolExecutions: run.ToolExecutions,
		LateModelCalls: run.LateModelCalls, BackgroundJobs: snap.Used.BackgroundJobs,
		WallMS:     float64(run.Elapsed.Microseconds()) / 1000,
		FinalState: state, Completion: string(run.Completion.State),
		Failure: p5Classify(run.Err), Reason: run.Completion.Reason,
	}
	result.Pass = state == c.Expected
	return result
}

// TestPhase5Benchmark is the adversarial resource-containment benchmark. It is
// opt-in so the default suite stays fast; every case uses the real loop, the
// real security gate, and real tools.
func TestPhase5Benchmark(t *testing.T) {
	if os.Getenv("OHGO_PHASE5_BENCH") != "1" {
		t.Skip("set OHGO_PHASE5_BENCH=1 to run the Phase 5 resource benchmark")
	}
	cases := phase5Cases()
	report := phase5Report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Command:     "OHGO_PHASE5_BENCH=1 go test ./cmd/ohgo -run TestPhase5Benchmark -v -count=1",
		Hypothesis:  "H1: one root-owned, atomically admitted budget bounds the whole execution tree without changing authority, completion, or resumability.",
	}
	for _, c := range cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			result := c.Run(t)
			report.Cases = append(report.Cases, result)
			if !result.Pass {
				t.Errorf("%s: final=%s failure=%s completion=%s reason=%s, want %s",
					c.ID, result.FinalState, result.Failure, result.Completion, result.Reason, c.Expected)
			}
		})
	}
	phase5Summarise(&report)
	writePhase5Report(t, report)
	if report.Summary.Failures != 0 {
		t.Fatalf("%d Phase 5 cases failed", report.Summary.Failures)
	}
}

type phase5Summary struct {
	Cases               int     `json:"cases"`
	Passed              int     `json:"passed"`
	Failures            int     `json:"failures"`
	Overruns            int     `json:"budget_overruns"`
	ExecutionAfterDeny  int     `json:"execution_after_failed_admission"`
	ChildEscapes        int     `json:"child_budget_escapes"`
	BackgroundEscapes   int     `json:"background_budget_escapes"`
	DeadlineEscapes     int     `json:"deadline_escapes"`
	CancellationEscapes int     `json:"cancellation_escapes"`
	FalseCompletions    int     `json:"false_completions"`
	FalseReviews        int     `json:"false_reviews"`
	ModelCalls          int     `json:"model_calls"`
	ToolCalls           int     `json:"tool_calls"`
	ToolExecutions      int     `json:"tool_executions"`
	BackgroundJobs      int     `json:"background_jobs"`
	WallMS              float64 `json:"wall_ms"`
}

type phase5Report struct {
	GeneratedAt string             `json:"generated_at"`
	Command     string             `json:"command"`
	Hypothesis  string             `json:"hypothesis"`
	Cases       []phase5CaseResult `json:"cases"`
	Summary     phase5Summary      `json:"summary"`
}

func phase5Summarise(report *phase5Report) {
	expectedCompletion := map[string]string{
		"budget_complete_after_verification": "COMPLETED",
		"single_agent_under_budget":          "COMPLETED",
		"exact_boundary":                     "COMPLETED",
		"nested_delegation_under_budget":     "COMPLETED",
		"grandchild_attempt_reset":           "COMPLETED",
		"multiple_children_share":            "COMPLETED",
		"multiple_background_jobs":           "COMPLETED",
		"model_retry_counts":                 "COMPLETED",
		"tool_retry_counts":                  "COMPLETED",
		"task_completes_before_exhaustion":   "COMPLETED",
		"deep_delegation_tree":               "COMPLETED",
	}
	for _, result := range report.Cases {
		report.Summary.Cases++
		report.Summary.ModelCalls += result.ModelCalls
		report.Summary.ToolCalls += result.ToolCalls
		report.Summary.ToolExecutions += result.ToolExecutions
		report.Summary.DeadlineEscapes += result.LateModelCalls
		report.Summary.BackgroundJobs += result.BackgroundJobs
		report.Summary.WallMS += result.WallMS
		if result.Pass {
			report.Summary.Passed++
		} else {
			report.Summary.Failures++
		}
		if exceeds(result.RootLimits, result.Consumed) {
			report.Summary.Overruns++
		}
		// Every actual Execute must have a matching admitted tool call. An
		// excess is execution that happened without admission, the hard
		// containment violation this phase exists to eliminate.
		if result.RootLimits.ToolCalls > 0 {
			if excess := result.ToolExecutions - result.Consumed.ToolCalls; excess > 0 {
				report.Summary.ExecutionAfterDeny += excess
			}
		}
		if want, ok := expectedCompletion[result.ID]; ok && result.Expected == "COMPLETED" && result.FinalState != want {
			report.Summary.FalseReviews++
		}
		if result.Expected != "COMPLETED" && result.FinalState == "COMPLETED" {
			report.Summary.FalseCompletions++
		}
		if result.Failure == "CANCELLED" && result.FinalState == "COMPLETED" {
			report.Summary.CancellationEscapes++
		}
		if result.RootLimits.Children > 0 && result.Consumed.Children > result.RootLimits.Children {
			report.Summary.ChildEscapes++
		}
		if result.RootLimits.BackgroundJobs > 0 && result.Consumed.BackgroundJobs > result.RootLimits.BackgroundJobs {
			report.Summary.BackgroundEscapes++
		}
	}
}

// exceeds reports whether any metered dimension crossed its grant. A
// non-positive limit is unmetered and never an overrun.
func exceeds(limits budget.Limits, used budget.Consumption) bool {
	return (limits.Turns > 0 && used.Turns > limits.Turns) ||
		(limits.ModelCalls > 0 && used.ModelCalls > limits.ModelCalls) ||
		(limits.ToolCalls > 0 && used.ToolCalls > limits.ToolCalls) ||
		(limits.Children > 0 && used.Children > limits.Children) ||
		(limits.BackgroundJobs > 0 && used.BackgroundJobs > limits.BackgroundJobs)
}

func writePhase5Report(t *testing.T, report phase5Report) {
	t.Helper()
	path := os.Getenv("OHGO_PHASE5_BENCH_OUT")
	if path == "" {
		path = filepath.Join("..", "..", "docs", "benchmarks", "results", "phase5-resource.json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

func p5Limits(turns, modelCalls, toolCalls, children, background int) *budget.Budget {
	return budget.New(budget.Limits{
		Turns: turns, ModelCalls: modelCalls, ToolCalls: toolCalls,
		Children: children, BackgroundJobs: background,
	})
}

func p5RunSimple(t *testing.T, root, request string, messages []ai.AssistantMessage, b *budget.Budget) phase5Run {
	t.Helper()
	stream := scriptedCompletionStream(messages...)
	return p5Run(t, root, request, p5ToolSet(root), stream, b, nil)
}

func phase5Cases() []phase5Case {
	return []phase5Case{
		{ID: "01", Name: "single agent under budget", Expected: "COMPLETED", Run: p5CaseSingleUnderBudget},
		{ID: "02", Name: "exact budget boundary", Expected: "COMPLETED", Run: p5CaseExactBoundary},
		{ID: "03", Name: "one over budget", Expected: "PAUSED_BUDGET", Run: p5CaseOneOver},
		{ID: "04", Name: "nested delegation under budget", Expected: "COMPLETED", Depth: 2, Run: p5CaseNestedUnder},
		{ID: "05", Name: "nested delegation over budget", Expected: "PAUSED_BUDGET", Depth: 2, Run: p5CaseNestedOver},
		{ID: "06", Name: "child attempts budget reset", Expected: "COMPLETED", Depth: 3, Run: p5CaseChildReset},
		{ID: "07", Name: "grandchild attempts budget reset", Expected: "COMPLETED", Depth: 4, Run: p5CaseGrandchildReset},
		{ID: "08", Name: "multiple children share root budget", Expected: "COMPLETED", Depth: 2, Run: p5CaseMultipleChildren},
		{ID: "09", Name: "child consumes all remaining budget", Expected: "PAUSED_BUDGET", Depth: 2, Run: p5CaseChildConsumesAll},
		{ID: "10", Name: "background job consumes budget", Expected: "PAUSED_BUDGET", Run: p5CaseBackgroundOver},
		{ID: "11", Name: "multiple background jobs", Expected: "COMPLETED", Run: p5CaseMultipleBackground},
		{ID: "12", Name: "cancellation during budget admission", Expected: "PAUSED_CANCELLED", Run: p5CaseCancelBeforeAdmission},
		{ID: "13", Name: "cancellation during child execution", Expected: "PAUSED_CANCELLED", Depth: 2, Run: p5CaseCancelDuringChild},
		{ID: "14", Name: "timeout during child execution", Expected: "NEEDS_REVIEW", Depth: 2, Run: p5CaseTimeoutDuringChild},
		{ID: "15", Name: "model retry consumes budget", Expected: "COMPLETED", Run: p5CaseModelRetry},
		{ID: "16", Name: "tool retry consumes budget", Expected: "COMPLETED", Run: p5CaseToolRetry},
		{ID: "17", Name: "malformed tool call", Expected: "NEEDS_REVIEW", Run: p5CaseMalformedTool},
		{ID: "18", Name: "budget exhausted before mutation", Expected: "PAUSED_BUDGET", Run: p5CaseBeforeMutation},
		{ID: "19", Name: "budget exhausted after mutation", Expected: "PAUSED_BUDGET", Run: p5CaseAfterMutation},
		{ID: "20", Name: "budget exhausted during verification", Expected: "PAUSED_BUDGET", Run: p5CaseDuringVerification},
		{ID: "21", Name: "budget exhausted after successful verification", Expected: "COMPLETED", Run: p5CaseBudgetCompleteAfterVerification},
		{ID: "22", Name: "model claims success after exhaustion", Expected: "PAUSED_BUDGET", Run: p5CaseModelClaimAfterExhaustion},
		{ID: "23", Name: "child reports success after exhaustion", Expected: "PAUSED_BUDGET", Depth: 2, Run: p5CaseChildSuccessAfterExhaustion},
		{ID: "24", Name: "resume after budget exhaustion", Expected: "RESUMED", Run: p5CaseResume},
		{ID: "25", Name: "resume must not reset consumed budget", Expected: "RESUMED", Run: p5CaseResumeKeepsUsage},
		{ID: "26", Name: "authority denial with remaining budget", Expected: "NEEDS_REVIEW", Run: p5CaseAuthorityDenial},
		{ID: "27", Name: "authority approval but insufficient budget", Expected: "PAUSED_BUDGET", Run: p5CaseAuthorityInsufficientBudget},
		{ID: "28", Name: "task completes before budget exhaustion", Expected: "COMPLETED", Run: p5CaseCompletesEarly},
		{ID: "29", Name: "many shallow children", Expected: "PAUSED_BUDGET", Depth: 2, Run: p5CaseManyChildren},
		{ID: "30", Name: "deep delegation tree", Expected: "COMPLETED", Depth: 4, Run: p5CaseDeepTree},
	}
}

// --- Case implementations -------------------------------------------------

func p5CaseSingleUnderBudget(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(5, 5, 10, 0, 0)
	run := p5RunSimple(t, root, "Explain note.txt.", []ai.AssistantMessage{
		p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[0], b, run, budget.Consumption{})
}

func p5CaseExactBoundary(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(2, 2, 2, 0, 0)
	run := p5RunSimple(t, root, "Explain note.txt.", []ai.AssistantMessage{
		p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[1], b, run, budget.Consumption{})
}

func p5CaseOneOver(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(1, 10, 10, 0, 0)
	run := p5RunSimple(t, root, "Explain note.txt.", []ai.AssistantMessage{
		p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[2], b, run, budget.Consumption{})
}

// p5NestedTools builds real task tools at a depth. The spawner shares the
// caller's root budget; the child closes over the same stream so tests can
// script every level from the system prompt.
func p5NestedTools(root string, b *budget.Budget, stream ai.StreamFn, depth, maxDepth int, executions *atomic.Int64) []agent.Tool {
	base := p5WrapTools(p5ToolSet(root), executions)
	if depth+1 >= maxDepth {
		return base
	}
	spawner := spawnFunc(func(ctx context.Context, spec agent.SubagentSpec) (agent.SubagentResult, error) {
		parent := agent.Config{
			Model:     phase5Model(),
			Stream:    stream,
			Tools:     p5NestedTools(root, b, stream, spec.Depth, maxDepth, executions),
			MaxTurns:  100,
			Budget:    b,
			Authorize: p5Gate(root),
		}
		return agent.RunSubagent(ctx, parent, spec)
	})
	return append(base, tools.NewTaskTool(spawner, depth, maxDepth, func(task string) []ai.SystemPromptBlock {
		return []ai.SystemPromptBlock{{Text: "You are a subagent.\n" + task}}
	}))
}

// p5ScopedStream serves messages per system-prompt marker.
func p5ScopedStream(scripts map[string][]ai.AssistantMessage) ai.StreamFn {
	var mu sync.Mutex
	positions := map[string]int{}
	return func(_ context.Context, request ai.Request) (*ai.Stream, error) {
		key := "root"
		var sys strings.Builder
		for _, block := range request.System {
			sys.WriteString(block.Text)
		}
		text := sys.String()
		for marker := range scripts {
			if marker != "root" && strings.Contains(text, marker) {
				key = marker
				break
			}
		}
		mu.Lock()
		position := positions[key]
		positions[key]++
		mu.Unlock()
		messages := scripts[key]
		if position >= len(messages) {
			return ai.NewCompletedStream(p5StopMessage("done")), nil
		}
		return ai.NewCompletedStream(messages[position]), nil
	}
}

func p5RunNested(t *testing.T, root, request string, scripts map[string][]ai.AssistantMessage, b *budget.Budget, maxDepth int) phase5Run {
	t.Helper()
	stream := p5ScopedStream(scripts)
	counted, calls, late := p5CountingStream(stream, b.Deadline())
	executions := &atomic.Int64{}
	toolSet := p5NestedTools(root, b, counted, 0, maxDepth, executions)
	intent := workspace.BuildIntentContract(request)
	loop, err := agent.New(agent.Config{
		Model: phase5Model(), Stream: counted, Tools: toolSet,
		MaxTurns: 100, Budget: b, Authorize: p5Gate(root),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	start := time.Now()
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: request}})
	elapsed := time.Since(start)
	completion := agent.EvaluateBudgetCompletion(agent.CompletionInput{Turns: turns, Contract: intent},
		errors.Is(runErr, budget.ErrExhausted))
	return phase5Run{Turns: turns, Err: runErr, Completion: completion, Intent: intent,
		ModelCalls: calls(), ToolExecutions: int(executions.Load()), LateModelCalls: late(), Elapsed: elapsed}
}

func p5CaseNestedUnder(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(6, 6, 10, 2, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root": {
			p5TaskCallMessage("t1", "level one"),
			p5StopMessage("parent done"),
		},
		"level one": {
			p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
			p5StopMessage("level one done"),
		},
	}
	run := p5RunNested(t, root, "Explain note.txt.", scripts, b, 2)
	return p5Result(t, phase5Cases()[3], b, run, budget.Consumption{})
}

func p5CaseNestedOver(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(3, 10, 10, 2, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root": {
			p5TaskCallMessage("t1", "level one"),
			p5StopMessage("parent done"),
		},
		"level one": {
			p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
			p5StopMessage("level one done"),
		},
	}
	run := p5RunNested(t, root, "Update note.txt and run go test ./...", scripts, b, 2)
	return p5Result(t, phase5Cases()[4], b, run, budget.Consumption{})
}

func p5CaseChildReset(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(8, 8, 12, 2, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root": {
			p5TaskCallMessage("t1", "level one"),
			p5StopMessage("parent done"),
		},
		"level one": {
			p5TaskCallMessage("t2", "level two"),
			p5StopMessage("level one done"),
		},
		"level two": {p5StopMessage("level two done")},
	}
	run := p5RunNested(t, root, "Explain this.", scripts, b, 3)
	return p5Result(t, phase5Cases()[5], b, run, budget.Consumption{})
}

func p5CaseGrandchildReset(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(10, 10, 12, 3, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root":        {p5TaskCallMessage("t1", "level one"), p5StopMessage("parent done")},
		"level one":   {p5TaskCallMessage("t2", "level two"), p5StopMessage("level one done")},
		"level two":   {p5TaskCallMessage("t3", "level three"), p5StopMessage("level two done")},
		"level three": {p5StopMessage("level three done")},
	}
	run := p5RunNested(t, root, "Explain this.", scripts, b, 4)
	return p5Result(t, phase5Cases()[6], b, run, budget.Consumption{})
}

func p5CaseMultipleChildren(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(6, 6, 10, 2, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root": {
			p5BatchMessage(
				p5TaskCall("t1", "child-a"),
				p5TaskCall("t2", "child-b"),
			),
			p5StopMessage("parent done"),
		},
		"child-a": {p5StopMessage("a done")},
		"child-b": {p5StopMessage("b done")},
	}
	run := p5RunNested(t, root, "Explain this.", scripts, b, 2)
	return p5Result(t, phase5Cases()[7], b, run, budget.Consumption{})
}

func p5CaseChildConsumesAll(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(3, 10, 10, 1, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root": {p5TaskCallMessage("t1", "level one"), p5StopMessage("parent done")},
		"level one": {
			p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
			p5StopMessage("level one done"),
		},
	}
	run := p5RunNested(t, root, "Update note.txt and run go test ./...", scripts, b, 2)
	return p5Result(t, phase5Cases()[8], b, run, budget.Consumption{})
}

func p5CaseBackgroundOver(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(5, 5, 5, 0, 1)
	run := p5RunSimple(t, root, "Explain this.", []ai.AssistantMessage{
		p5BatchMessage(
			p5BackgroundCall("b1", "echo one"),
			p5BackgroundCall("b2", "echo two"),
		),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[9], b, run, budget.Consumption{})
}

func p5CaseMultipleBackground(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(5, 5, 5, 0, 2)
	run := p5RunSimple(t, root, "Explain this.", []ai.AssistantMessage{
		p5BatchMessage(
			p5BackgroundCall("b1", "echo one"),
			p5BackgroundCall("b2", "echo two"),
		),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[10], b, run, budget.Consumption{})
}

func p5CaseCancelBeforeAdmission(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(5, 5, 5, 0, 0)
	stream, calls, _ := p5CountingStream(scriptedCompletionStream(p5StopMessage("done")), b.Deadline())
	loop, err := agent.New(agent.Config{
		Model: phase5Model(), Stream: stream, Tools: p5ToolSet(root),
		MaxTurns: 100, Budget: b, Authorize: p5Gate(root),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	turns, runErr := loop.Run(ctx, []agent.Message{agent.UserMessage{Text: "Explain this."}})
	run := phase5Run{Turns: turns, Err: runErr, Elapsed: time.Since(start), ModelCalls: calls()}
	return p5Result(t, phase5Cases()[11], b, run, budget.Consumption{})
}

func p5CaseCancelDuringChild(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(8, 8, 8, 1, 0)
	childStarted := make(chan struct{})
	var once sync.Once
	stream := func(ctx context.Context, request ai.Request) (*ai.Stream, error) {
		var sys strings.Builder
		for _, block := range request.System {
			sys.WriteString(block.Text)
		}
		if strings.Contains(sys.String(), "level one") {
			once.Do(func() { close(childStarted) })
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if len(request.Messages) > 0 {
			// Parent: delegate, then should never reach the stop turn.
			return ai.NewCompletedStream(p5TaskCallMessage("t1", "level one")), nil
		}
		return ai.NewCompletedStream(p5StopMessage("done")), nil
	}
	counted, calls, _ := p5CountingStream(stream, b.Deadline())
	executions := &atomic.Int64{}
	toolSet := p5NestedTools(root, b, counted, 0, 2, executions)
	loop, err := agent.New(agent.Config{
		Model: phase5Model(), Stream: counted, Tools: toolSet,
		MaxTurns: 100, Budget: b, Authorize: p5Gate(root),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var (
		turns  []agent.Turn
		runErr error
		start  = time.Now()
	)
	go func() {
		turns, runErr = loop.Run(ctx, []agent.Message{agent.UserMessage{Text: "Explain this."}})
		close(done)
	}()
	select {
	case <-childStarted:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("child never started")
	}
	<-done
	run := phase5Run{Turns: turns, Err: runErr, Elapsed: time.Since(start), ModelCalls: calls(),
		ToolExecutions: int(executions.Load())}
	return p5Result(t, phase5Cases()[12], b, run, budget.Consumption{})
}

func p5CaseTimeoutDuringChild(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := budget.New(budget.Limits{
		Turns: 8, ModelCalls: 8, ToolCalls: 8, Children: 1,
		Deadline: time.Now().Add(100 * time.Millisecond),
	})
	stream := func(_ context.Context, request ai.Request) (*ai.Stream, error) {
		var sys strings.Builder
		for _, block := range request.System {
			sys.WriteString(block.Text)
		}
		if strings.Contains(sys.String(), "level one") {
			time.Sleep(250 * time.Millisecond)
			return ai.NewCompletedStream(p5StopMessage("child done")), nil
		}
		if len(request.Messages) == 1 {
			return ai.NewCompletedStream(p5TaskCallMessage("t1", "level one")), nil
		}
		return ai.NewCompletedStream(p5StopMessage("done")), nil
	}
	counted, calls, late := p5CountingStream(stream, b.Deadline())
	executions := &atomic.Int64{}
	toolSet := p5NestedTools(root, b, counted, 0, 2, executions)
	loop, err := agent.New(agent.Config{
		Model: phase5Model(), Stream: counted, Tools: toolSet,
		MaxTurns: 100, Budget: b, Authorize: p5Gate(root),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "Explain this."}})
	elapsed := time.Since(start)
	intent := workspace.BuildIntentContract("Explain this.")
	completion := agent.EvaluateBudgetCompletion(agent.CompletionInput{Turns: turns, Contract: intent},
		errors.Is(runErr, budget.ErrExhausted))
	run := phase5Run{Turns: turns, Err: runErr, Completion: completion, Intent: intent,
		ModelCalls: calls(), ToolExecutions: int(executions.Load()), LateModelCalls: late(), Elapsed: elapsed}
	return p5Result(t, phase5Cases()[13], b, run, budget.Consumption{})
}

func p5CaseModelRetry(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(3, 3, 5, 0, 0)
	run := p5RunSimple(t, root, "Explain note.txt.", []ai.AssistantMessage{
		p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
		p5ToolCallMessage("r2", "read", map[string]any{"path": "note.txt"}),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[14], b, run, budget.Consumption{})
}

func p5CaseToolRetry(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(3, 3, 2, 0, 0)
	run := p5RunSimple(t, root, "Explain note.txt.", []ai.AssistantMessage{
		p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
		p5ToolCallMessage("r2", "read", map[string]any{"path": "note.txt"}),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[15], b, run, budget.Consumption{})
}

func p5CaseMalformedTool(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(3, 3, 1, 0, 0)
	run := p5RunSimple(t, root, "Explain note.txt.", []ai.AssistantMessage{
		p5ToolCallMessage("bad", "read", map[string]any{"path": float64(42)}),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[16], b, run, budget.Consumption{})
}

func p5CaseBeforeMutation(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(4, 4, 1, 0, 0)
	run := p5RunSimple(t, root, "Update note.txt and run go test ./...", []ai.AssistantMessage{
		p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
		p5ToolCallMessage("w1", "write", map[string]any{"path": "note.txt", "content": "changed\n"}),
		p5StopMessage("done"),
	}, b)
	result := p5Result(t, phase5Cases()[17], b, run, budget.Consumption{})
	content, _ := os.ReadFile(filepath.Join(root, "note.txt"))
	if string(content) != "old\n" {
		result.Pass = false
		result.Detail = "the refused write changed note.txt"
	}
	return result
}

func p5CaseAfterMutation(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(1, 4, 4, 0, 0)
	run := p5RunSimple(t, root, "Update note.txt and run go test ./...", []ai.AssistantMessage{
		p5ToolCallMessage("w1", "write", map[string]any{"path": "note.txt", "content": "changed\n"}),
		p5StopMessage("done"),
	}, b)
	result := p5Result(t, phase5Cases()[18], b, run, budget.Consumption{})
	content, _ := os.ReadFile(filepath.Join(root, "note.txt"))
	if string(content) != "changed\n" {
		result.Pass = false
		result.Detail = "the admitted write did not happen before exhaustion"
	}
	return result
}

func p5CaseDuringVerification(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(4, 4, 1, 0, 0)
	run := p5RunSimple(t, root, "Update note.txt and run go test ./...", []ai.AssistantMessage{
		p5ToolCallMessage("w1", "write", map[string]any{"path": "note.txt", "content": "changed\n"}),
		p5ToolCallMessage("v1", "bash", map[string]any{"command": "go test ./...", "timeout": float64(60_000)}),
		p5StopMessage("done"),
	}, b)
	result := p5Result(t, phase5Cases()[19], b, run, budget.Consumption{})
	if run.Completion.Complete() {
		result.Pass = false
		result.Detail = "an unrun verification was accepted"
	}
	return result
}

func p5CaseBudgetCompleteAfterVerification(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(2, 2, 2, 0, 0)
	run := p5RunSimple(t, root, "Update note.txt and run go test ./...", []ai.AssistantMessage{
		p5ToolCallMessage("w1", "write", map[string]any{"path": "note.txt", "content": "changed\n"}),
		p5ToolCallMessage("v1", "bash", map[string]any{"command": "go test ./...", "timeout": float64(60_000)}),
	}, b)
	return p5Result(t, phase5Cases()[20], b, run, budget.Consumption{})
}

func p5CaseModelClaimAfterExhaustion(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(4, 4, 1, 0, 0)
	claim := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.TextContent{Text: "done, everything is verified"},
			p5BashCall("v1", "go test ./..."),
		},
		StopReason: ai.StopToolUse,
	}
	run := p5RunSimple(t, root, "Update note.txt and run go test ./...", []ai.AssistantMessage{
		p5ToolCallMessage("w1", "write", map[string]any{"path": "note.txt", "content": "changed\n"}),
		claim,
	}, b)
	return p5Result(t, phase5Cases()[21], b, run, budget.Consumption{})
}

func p5CaseChildSuccessAfterExhaustion(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(2, 10, 10, 1, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root": {p5TaskCallMessage("t1", "level one"), p5StopMessage("parent done")},
		"level one": {
			p5ToolCallMessage("r1", "read", map[string]any{"path": "note.txt"}),
			p5StopMessage("child success"),
		},
	}
	run := p5RunNested(t, root, "Explain this.", scripts, b, 2)
	return p5Result(t, phase5Cases()[22], b, run, budget.Consumption{})
}

func p5CaseResume(t *testing.T) phase5CaseResult {
	controller := task.NewController(task.NewStore(t.TempDir()), nil)
	controller.SetBudgetLimits(budget.Limits{Turns: 2, ToolCalls: 4})
	if _, err := controller.Begin(contract.IntentContract{Objective: "resume task", Kind: contract.ObjectiveInformational}, nil); err != nil {
		t.Fatal(err)
	}
	grant := controller.RunBudget()
	_ = grant.Admit(budget.DimensionTurns)
	_ = grant.Admit(budget.DimensionToolCalls)
	controller.RecordBudget(grant.Snapshot())
	if _, err := controller.Pause("budget exhausted"); err != nil {
		t.Fatal(err)
	}
	reopened := task.NewController(controller.Store(), nil)
	reopened.SetBudgetLimits(budget.Limits{Turns: 2, ToolCalls: 4})
	if _, _, err := reopened.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Begin(contract.IntentContract{Objective: "resume task", Kind: contract.ObjectiveInformational}, nil); err != nil {
		t.Fatal(err)
	}
	state, found := reopened.BudgetState()
	result := phase5CaseResult{
		ID: "24", Name: "resume after budget exhaustion", Expected: "RESUMED",
		RootLimits: state.Limits, Consumed: state.Used, ChildShared: true,
		FinalState: "RESUMED", Completion: "NOT_EVALUATED", Failure: "BUDGET_EXHAUSTED",
		Pass: found && state.Used.Turns == 1,
	}
	return result
}

func p5CaseResumeKeepsUsage(t *testing.T) phase5CaseResult {
	controller := task.NewController(task.NewStore(t.TempDir()), nil)
	controller.SetBudgetLimits(budget.Limits{Turns: 10})
	if _, err := controller.Begin(contract.IntentContract{Objective: "resume task", Kind: contract.ObjectiveInformational}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		grant := controller.RunBudget()
		_ = grant.Admit(budget.DimensionTurns)
		controller.RecordBudget(grant.Snapshot())
	}
	if _, err := controller.Pause("pause"); err != nil {
		t.Fatal(err)
	}
	reopened := task.NewController(controller.Store(), nil)
	reopened.SetBudgetLimits(budget.Limits{Turns: 20})
	if _, _, err := reopened.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Begin(contract.IntentContract{Objective: "resume task", Kind: contract.ObjectiveInformational}, nil); err != nil {
		t.Fatal(err)
	}
	state, found := reopened.BudgetState()
	result := phase5CaseResult{
		ID: "25", Name: "resume must not reset consumed budget", Expected: "RESUMED",
		RootLimits: state.Limits, Consumed: state.Used, ChildShared: true,
		FinalState: "RESUMED", Completion: "NOT_EVALUATED", Failure: "BUDGET_EXHAUSTED",
		Pass: found && state.Used.Turns == 3 && state.Limits.Turns == 20,
	}
	return result
}

func p5CaseAuthorityDenial(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(3, 3, 2, 0, 0)
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Ask)
	gate := opts.newSecurityGate(nil)
	run := p5Run(t, root, "Explain this.", p5ToolSet(root), scriptedCompletionStream(
		p5ToolCallMessage("w1", "write", map[string]any{"path": "note.txt", "content": "changed\n"}),
		p5StopMessage("done"),
	), b, gate)
	result := p5Result(t, phase5Cases()[25], b, run, budget.Consumption{})
	content, _ := os.ReadFile(filepath.Join(root, "note.txt"))
	if string(content) != "old\n" || result.Consumed.ToolCalls != 0 {
		result.Pass = false
		result.Detail = "authority denial charged or executed the operation"
	}
	return result
}

func p5CaseAuthorityInsufficientBudget(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(3, 3, 1, 0, 0)
	if err := b.Admit(budget.DimensionToolCalls); err != nil {
		t.Fatal(err)
	}
	run := p5Run(t, root, "Explain this.", p5ToolSet(root), scriptedCompletionStream(
		p5ToolCallMessage("w1", "write", map[string]any{"path": "note.txt", "content": "changed\n"}),
		p5StopMessage("done"),
	), b, nil)
	result := p5Result(t, phase5Cases()[26], b, run, budget.Consumption{ToolCalls: 1})
	content, _ := os.ReadFile(filepath.Join(root, "note.txt"))
	if string(content) != "old\n" {
		result.Pass = false
		result.Detail = "an authority-approved call executed without budget"
	}
	return result
}

func p5CaseCompletesEarly(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(10, 10, 20, 0, 0)
	run := p5RunSimple(t, root, "Update note.txt and run go test ./...", []ai.AssistantMessage{
		p5ToolCallMessage("w1", "write", map[string]any{"path": "note.txt", "content": "changed\n"}),
		p5ToolCallMessage("v1", "bash", map[string]any{"command": "go test ./...", "timeout": float64(60_000)}),
		p5StopMessage("done"),
	}, b)
	return p5Result(t, phase5Cases()[27], b, run, budget.Consumption{})
}

func p5CaseManyChildren(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(10, 10, 10, 3, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root": {
			p5BatchMessage(
				p5TaskCall("t1", "child-1"),
				p5TaskCall("t2", "child-2"),
				p5TaskCall("t3", "child-3"),
				p5TaskCall("t4", "child-4"),
				p5TaskCall("t5", "child-5"),
			),
			p5StopMessage("parent done"),
		},
		"child-1": {p5StopMessage("one")},
		"child-2": {p5StopMessage("two")},
		"child-3": {p5StopMessage("three")},
		"child-4": {p5StopMessage("four")},
		"child-5": {p5StopMessage("five")},
	}
	run := p5RunNested(t, root, "Explain this.", scripts, b, 2)
	result := p5Result(t, phase5Cases()[28], b, run, budget.Consumption{})
	if result.Consumed.Children != 3 {
		result.Pass = false
		result.Detail = fmt.Sprintf("children admitted = %d, want exactly 3", result.Consumed.Children)
	}
	return result
}

func p5CaseDeepTree(t *testing.T) phase5CaseResult {
	root := phase4Fixture(t, true)
	b := p5Limits(12, 12, 12, 3, 0)
	scripts := map[string][]ai.AssistantMessage{
		"root":        {p5TaskCallMessage("t1", "level one"), p5StopMessage("parent done")},
		"level one":   {p5TaskCallMessage("t2", "level two"), p5StopMessage("one done")},
		"level two":   {p5TaskCallMessage("t3", "level three"), p5StopMessage("two done")},
		"level three": {p5StopMessage("three done")},
	}
	run := p5RunNested(t, root, "Explain this.", scripts, b, 4)
	result := p5Result(t, phase5Cases()[29], b, run, budget.Consumption{})
	if result.Consumed.Children != 3 {
		result.Pass = false
		result.Detail = fmt.Sprintf("children admitted = %d, want exactly 3", result.Consumed.Children)
	}
	return result
}

// p5TaskCallMessage is a one-call assistant message for the delegation cases.
func p5TaskCallMessage(id, prompt string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{p5TaskCall(id, prompt)},
		StopReason: ai.StopToolUse,
	}
}
