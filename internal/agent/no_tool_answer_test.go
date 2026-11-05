package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
)

// evidenceReadTool returns the same attributable observation the real read
// tool returns for a complete file window.
type evidenceReadTool struct{}

func (evidenceReadTool) Name() string        { return "read" }
func (evidenceReadTool) Description() string { return "read a file" }
func (evidenceReadTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"path":{"type":"string"}},
		"required":["path"]
	}`))
}
func (evidenceReadTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (evidenceReadTool) Exclusive(map[string]any) bool { return false }
func (evidenceReadTool) Execute(_ agent.Context, args map[string]any) agent.Result {
	path, _ := args["path"].(string)
	if path == "" {
		path = "README.md"
	}
	return agent.Result{
		Text: "1\t# ohgo\n2\tA coding agent with a controlled runtime.",
		Details: map[string]any{
			"path": path, "offset": 1, "limit": 2000, "totalLines": 2,
		},
	}
}

// evidenceListTool is a second real observation tool. Adversarial scripts use
// it to alternate tools while proving the action surface still bounds them.
type evidenceListTool struct{}

func (evidenceListTool) Name() string        { return "ls" }
func (evidenceListTool) Description() string { return "list a directory" }
func (evidenceListTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"path":{"type":"string"}}
	}`))
}
func (evidenceListTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (evidenceListTool) Exclusive(map[string]any) bool { return false }
func (evidenceListTool) Execute(_ agent.Context, args map[string]any) agent.Result {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	return agent.Result{
		Text: "README.md\ngo.mod\ncmd",
		Details: map[string]any{
			"path": path, "entries": 3,
		},
	}
}

// allowRead authorizes a read-only test capability.
type allowRead struct{}

func (allowRead) Evaluate(ai.ToolCall, agent.Tool) security.Decision {
	return security.Decision{Outcome: security.OutcomeAuto, Rule: "test_allow"}
}
func (allowRead) Approve(ai.ToolCall, security.Decision) (security.Grant, bool) {
	return security.Grant{}, true
}

type informationalResult struct {
	turns    []agent.Turn
	intent   contract.IntentContract
	report   agent.ProjectionReport
	requests int64
	err      error
}

// runInformational executes one informational turn against a scripted model.
// The script is consumed one message per provider request; running past it is a
// failure, so the test observes exactly how many requests the runtime made.
func runInformational(t *testing.T, script []ai.AssistantMessage) informationalResult {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/ohgo\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# ohgo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := "check this project and analyze what this project is about?"
	intent := workspace.BuildIntentContract(prompt)
	if intent.Kind != contract.ObjectiveInformational {
		t.Fatalf("intent kind = %s, want informational", intent.Kind)
	}
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "ask", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	var requests atomic.Int64
	var lastReport agent.ProjectionReport
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script; the runtime continued after the answer", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:     []agent.Tool{evidenceReadTool{}, evidenceListTool{}},
		Project:   projector.Project,
		Authorize: allowRead{},
		MaxTurns:  6,
		OnProjection: func(report agent.ProjectionReport) {
			lastReport = report
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})
	return informationalResult{turns: turns, intent: intent, report: lastReport, requests: requests.Load(), err: err}
}

func readCall(id string) []ai.ContentBlock {
	return []ai.ContentBlock{ai.ToolCall{
		ID: id, Name: "read", Arguments: map[string]any{"path": "README.md"},
	}}
}

// overviewCall supplies the two content observations a repository overview
// needs: a document and the module manifest.
func overviewCall(id string) []ai.ContentBlock {
	return []ai.ContentBlock{
		ai.ToolCall{ID: id + "-readme", Name: "read", Arguments: map[string]any{"path": "README.md"}},
		ai.ToolCall{ID: id + "-mod", Name: "read", Arguments: map[string]any{"path": "go.mod"}},
		ai.ToolCall{ID: id + "-main", Name: "read", Arguments: map[string]any{"path": "main.go"}},
	}
}

// Once the required repository evidence is known, the next request is for the
// answer. The runtime must withhold every tool instead of letting the model
// observe more files merely because it can.
func TestSufficientInformationalEvidenceMakesTheNextTurnAnswerOnly(t *testing.T) {
	answer := "ohgo is a coding agent with a controlled runtime: explicit authority, bounded budgets, and evidence-based completion."
	result := runInformational(t, []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want 2: the runtime kept offering tools after sufficiency", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	if result.turns[1].Assistant.Text() != answer {
		t.Fatalf("final answer = %q, want the model's answer", result.turns[1].Assistant.Text())
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A model that answers and still asks for another action after the runtime
// moved to answer-only must not keep the run alive. The answer is preserved,
// the continuation call is dropped without dispatch (so no red refusal card
// appears after the answer), and the loop stops.
func TestAnswerWithUnnecessaryCallStopsAndStaysComplete(t *testing.T) {
	answer := "ohgo is a coding agent with a controlled runtime."
	result := runInformational(t, []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
		{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: answer},
				ai.ToolCall{ID: "read-after-answer", Name: "read", Arguments: map[string]any{"path": "README.md"}},
			},
			StopReason: ai.StopToolUse,
		},
	})

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want 2", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	last := result.turns[1]
	if last.Assistant.Text() != answer {
		t.Fatalf("answer lost: %q", last.Assistant.Text())
	}
	if len(last.Results) != 0 {
		t.Fatalf("last results = %+v, want no dispatched continuation", last.Results)
	}
	if calls := last.Assistant.ToolCalls(); len(calls) != 0 {
		t.Fatalf("stored tool calls = %+v, want none", calls)
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED from the preserved answer", got.State, got.Reason)
	}
}

// A model that needs the empty surface repeated once before answering does so
// without executing anything after sufficiency.
func TestAnswerlessContinuationRecoversAfterARefusal(t *testing.T) {
	answer := "ohgo is a coding agent with a controlled runtime."
	result := runInformational(t, []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
		{Content: readCall("read-2"), StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 3 {
		t.Fatalf("model requests = %d, want 3 (the refusal, then the answer)", result.requests)
	}
	if len(result.turns) != 3 || result.turns[2].Assistant.Text() != answer {
		t.Fatalf("final answer = %q, want the answer after the refusal", result.turns[2].Assistant.Text())
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A probe that establishes absence is an observation, not an unrecovered
// failure. It remains visible to the model and the user, but it cannot veto an
// otherwise correct answer.
func TestObservedAbsenceDoesNotBlockCompletion(t *testing.T) {
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{{
			ToolName: "read",
			Result: agent.Result{
				Text:            "stat \"README.md\": no such file or directory",
				IsError:         true,
				ObservedAbsence: true,
			},
		}}},
		{Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "There is no README yet; index.html is the entry point."}},
		}},
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "check this project",
		},
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A final message that still contains serialized tool syntax is not an
// ordinary answer, even when its text is non-empty. Completion must report
// review rather than success.
func TestProtocolResidueVetoesInformationalCompletion(t *testing.T) {
	turn := agent.Turn{Assistant: ai.AssistantMessage{
		Content:         []ai.ContentBlock{ai.TextContent{Text: `<dots_function_call><invoke name="ls"><parameter name="path">x`}},
		ProtocolResidue: true,
	}}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{turn},
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "check this project",
		},
	})
	if got.Complete() {
		t.Fatal("serialized protocol residue was reported as a completed answer")
	}
	if !strings.Contains(got.Reason, "serialized tool syntax") {
		t.Fatalf("reason = %q, want the protocol-residue reason", got.Reason)
	}
}

// A model that never answers is bounded by the turn grant. Its refusals execute
// nothing, and the run is never reported as a successful analysis.
func listCall(id string) []ai.ContentBlock {
	return []ai.ContentBlock{ai.ToolCall{
		ID: id, Name: "ls", Arguments: map[string]any{"path": "."},
	}}
}

func invalidReadCall(id string) []ai.ContentBlock {
	return []ai.ContentBlock{ai.ToolCall{
		ID: id, Name: "read", Arguments: map[string]any{},
	}}
}

// A model that alternates between different tools after the action surface is
// empty still terminates. Every call is refused, nothing executes, and the run
// cannot be reported complete.
func TestNonAnsweringModelAlternatingToolsIsBounded(t *testing.T) {
	script := []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
	}
	for i := 0; i < 5; i++ {
		if i%2 == 0 {
			script = append(script, ai.AssistantMessage{
				Content: readCall("read-" + string(rune('a'+i))), StopReason: ai.StopToolUse,
			})
			continue
		}
		script = append(script, ai.AssistantMessage{
			Content: listCall("ls-" + string(rune('a'+i))), StopReason: ai.StopToolUse,
		})
	}
	result := runInformational(t, script)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if result.requests != 3 {
		t.Fatalf("model requests = %d, want the bounded answer-only surface (3)", result.requests)
	}
	last := result.turns[len(result.turns)-1]
	for _, outcome := range last.Results {
		if !outcome.Result.AnswerRequired {
			t.Fatalf("post-answer call was not refused: %+v", outcome)
		}
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if got.Complete() {
		t.Fatal("an alternating non-answering model was reported complete")
	}
	if strings.TrimSpace(got.Reason) == "" {
		t.Fatal("a non-complete run must explain its verdict")
	}
}

// Malformed tool calls (schema violations) never become evidence and never
// produce an answer. The runtime records the failure class, refuses the
// identical repeat, and closes the task instead of spending the turn grant.
func TestNonAnsweringModelWithMalformedCallsIsBounded(t *testing.T) {
	script := make([]ai.AssistantMessage, 0, 6)
	for i := 0; i < 6; i++ {
		script = append(script, ai.AssistantMessage{
			Content: invalidReadCall("bad-" + string(rune('a'+i))), StopReason: ai.StopToolUse,
		})
	}
	result := runInformational(t, script)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if result.requests > 3 {
		t.Fatalf("model requests = %d, want a bounded decision", result.requests)
	}
	failureRejected := 0
	for _, turn := range result.turns {
		for _, outcome := range turn.Results {
			if outcome.Result.FailureRejected {
				failureRejected++
			}
		}
	}
	if failureRejected == 0 {
		t.Fatal("the identical failed proposal was executed again instead of refused")
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if got.Complete() {
		t.Fatal("a run of malformed calls was reported complete")
	}
}

// An empty or whitespace-only final message is not an answer, even after the
// evidence was acquired.
func TestEmptyAndWhitespaceAnswersAreNotCompletion(t *testing.T) {
	for name, answer := range map[string]string{
		"empty":      "",
		"whitespace": "  \n\t ",
	} {
		t.Run(name, func(t *testing.T) {
			result := runInformational(t, []ai.AssistantMessage{
				{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
				{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
			})
			if result.err != nil {
				t.Fatalf("Run: %v", result.err)
			}
			got := agent.EvaluateTaskCompletion(agent.CompletionInput{
				Turns: result.turns, Contract: result.intent,
				EvidenceReview: result.report.EvidenceReview(),
			})
			if got.Complete() {
				t.Fatalf("%s answer was reported complete", name)
			}
			if !strings.Contains(got.Reason, "empty") {
				t.Fatalf("reason = %q, want an empty-answer reason", got.Reason)
			}
		})
	}
}

// An answer-shaped message emitted before any observation is not evidence. The
// projector's review vetoes it even though the text looks like a result.
func TestAnswerShapedMessageWithoutEvidenceIsNotCompletion(t *testing.T) {
	result := runInformational(t, []ai.AssistantMessage{{
		Content: []ai.ContentBlock{ai.TextContent{
			Text: "This project is a coding agent with a controlled runtime and explicit authority.",
		}},
	}})
	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 1 {
		t.Fatalf("model requests = %d, want 1", result.requests)
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if got.Complete() {
		t.Fatal("an answer with no repository evidence was reported complete")
	}
	if !strings.Contains(got.Reason, "not sufficient") {
		t.Fatalf("reason = %q, want an evidence-sufficiency reason", got.Reason)
	}
}

// Quoted protocol documentation is assistant content. It must not set the
// residue flag or veto completion; unquoted provider bytes still do.
func TestQuotedProtocolContentDoesNotVetoCompletion(t *testing.T) {
	quoted := ai.NormalizeMessage(ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{
			Text: "Use `<dotsfunctioncall>` and `</dotsfunctioncall>` in the docs.",
		}},
	}, ai.NormalizeOptions{})
	if quoted.ProtocolResidue {
		t.Fatal("quoted documentation was marked as protocol residue")
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{{Assistant: quoted}},
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "What is a cron expression?",
		},
	})
	if !got.Complete() {
		t.Fatalf("quoted documentation vetoed completion: %s", got.Reason)
	}

	unquoted := ai.NormalizeMessage(ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{
			Text: `<dots_protocol_call><invoke name="ls"><parameter name="path">x</parameter></invoke></dots_protocol_call>`,
		}},
	}, ai.NormalizeOptions{})
	got = agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{{Assistant: unquoted}},
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "What is a cron expression?",
		},
	})
	if got.Complete() {
		t.Fatal("unquoted provider protocol was reported complete")
	}
}

func TestAnswerlessContinuationIsBoundedAndNotCompleted(t *testing.T) {
	script := []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
	}
	for i := 0; i < 5; i++ {
		script = append(script, ai.AssistantMessage{
			Content: readCall("read-" + string(rune('a'+i))), StopReason: ai.StopToolUse,
		})
	}
	result := runInformational(t, script)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if result.requests != 3 {
		t.Fatalf("model requests = %d, want the bounded answer-only surface (3)", result.requests)
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
	})
	if got.Complete() {
		t.Fatal("a run with no answer was reported complete")
	}
}

// A call to a capability the loop was never built with is continuation after
// the answer once the action surface is empty. Nothing ran, and no refusal is
// recorded as outstanding work or shown after a finished answer.
func TestUnknownCallAfterAnswerIsNotRecordedAsWork(t *testing.T) {
	answer := "ohgo is a coding agent with a controlled runtime."
	result := runInformational(t, []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
		{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: answer},
				ai.ToolCall{ID: "unknown-1", Name: "gry:index_status"},
			},
			StopReason: ai.StopToolUse,
		},
	})
	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want 2", result.requests)
	}
	last := result.turns[len(result.turns)-1]
	if len(last.Results) != 0 {
		t.Fatalf("last results = %+v, want no dispatched continuation", last.Results)
	}
	if calls := last.Assistant.ToolCalls(); len(calls) != 0 {
		t.Fatalf("stored tool calls = %+v, want none", calls)
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if strings.Contains(got.Reason, "outstanding") {
		t.Fatalf("a dropped continuation was counted as outstanding work: %s", got.Reason)
	}
}

// A model that narrates an impending action instead of answering has not
// produced the answer the informational objective requires. Once the runtime
// closes the action surface, that narration cannot be executed either; the run
// must end in review, not in a completion the user can observe as false.
func TestPreparatoryPlanIsNotAnAnswerOnAClosedSurface(t *testing.T) {
	result := runInformational(t, []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
		{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "Let me explore the backend directory structure more thoroughly to find Go files."},
				ai.ToolCall{ID: "read-after-answer", Name: "read", Arguments: map[string]any{"path": "README.md"}},
			},
			StopReason: ai.StopToolUse,
		},
	})

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want 2", result.requests)
	}
	last := result.turns[len(result.turns)-1]
	if len(last.Results) != 0 {
		t.Fatalf("last results = %+v, want no dispatched continuation", last.Results)
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if got.Complete() {
		t.Fatal("a promise to keep exploring was reported as the requested answer")
	}
	if !strings.Contains(got.Reason, "answer") {
		t.Fatalf("completion reason = %q, want an answer-substance reason", got.Reason)
	}
}
