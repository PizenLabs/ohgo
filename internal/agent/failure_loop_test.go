package agent_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// failingCapability is a configured external capability that returns a
// classified failure. It is provider-agnostic on purpose: the runtime must
// reason about the failure class, not the tool's identity.
type failingCapability struct {
	name       string
	class      knowledge.FailureClass
	retryable  bool
	executions *atomic.Int64
}

func (t failingCapability) Name() string        { return t.name }
func (t failingCapability) Description() string { return "a configured external capability" }
func (t failingCapability) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"capability":{"type":"string"}},
		"required":["capability"]
	}`))
}
func (t failingCapability) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (t failingCapability) Exclusive(map[string]any) bool { return false }
func (t failingCapability) Execute(_ agent.Context, args map[string]any) agent.Result {
	t.executions.Add(1)
	return agent.Result{
		Text:             "external capability failed: " + string(t.class),
		IsError:          true,
		FailureClass:     string(t.class),
		FailureRetryable: t.retryable,
		Details: map[string]any{
			"failure_classification": string(t.class),
			"retryable":              t.retryable,
		},
	}
}

type failureRun struct {
	turns    []agent.Turn
	intent   contract.IntentContract
	report   agent.ProjectionReport
	evidence *cognition.EvidenceState
	requests int64
	err      error
}

// runFailureLoop drives one scripted run with a failing capability and the
// real task-shaped projector. The script supplies one assistant message per
// request and fails if the runtime asks past it.
func runFailureLoop(t *testing.T, prompt string, tools []agent.Tool, script []ai.AssistantMessage) failureRun {
	t.Helper()
	intent := workspace.BuildIntentContract(prompt)
	facts := cognition.RepositoryFacts{Go: true}
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "ask", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	var requests atomic.Int64
	var lastReport agent.ProjectionReport
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Tools:    tools,
		MaxTurns: 8,
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script; the runtime did not close the task", index+1)
			}
			if script[index].StopReason == "" {
				script[index].StopReason = ai.StopEndTurn
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Project:   projector.Project,
		Authorize: allowRead{},
		OnProjection: func(report agent.ProjectionReport) {
			lastReport = report
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})
	return failureRun{turns: turns, intent: intent, report: lastReport,
		evidence: projector.Evidence, requests: requests.Load(), err: runErr}
}

func repeatCapability(id, tool, capability string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID: id, Name: tool, Arguments: map[string]any{"capability": capability},
		}},
		StopReason: ai.StopToolUse,
	}
}

// The reported Dots failure: the same non-retryable capability was proposed
// again and again, executing every time. The runtime must execute it once,
// refuse the repeat without execution, and close the task.
func TestRepeatedNonRetryableCapabilityExecutesOnceAndCloses(t *testing.T) {
	var executions atomic.Int64
	tool := failingCapability{
		name: "mcp_read", class: knowledge.FailureUnknownCapability,
		executions: &executions,
	}
	prompt := "check this project and analyze what this project is about? use gry mcp"
	script := make([]ai.AssistantMessage, 0, 8)
	for i := 0; i < 8; i++ {
		script = append(script, repeatCapability(fmt.Sprintf("call-%d", i), "mcp_read", "gry:nope"))
	}
	result := runFailureLoop(t, prompt, []agent.Tool{tool}, script)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("capability executions = %d, want exactly 1", got)
	}
	if result.requests > 3 {
		t.Fatalf("model requests = %d, want a bounded terminal decision", result.requests)
	}
	last := result.turns[len(result.turns)-1]
	if len(last.Results) != 1 || !last.Results[0].Result.FailureRejected {
		t.Fatalf("last outcome = %+v, want a failure refusal", last.Results)
	}
	if !last.Results[0].Refused() {
		t.Fatal("the repeated failure refusal was counted as an execution")
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
		Provenance:     result.report.Provenance,
	})
	if got.Complete() {
		t.Fatalf("a blocked run was reported complete: %s", got.Reason)
	}
}

// A fatal authentication failure cannot recover without an external change. It
// closes the task at the first failure, after one answer-only request.
func TestFatalCapabilityFailureClosesAtFirstFailure(t *testing.T) {
	var executions atomic.Int64
	tool := failingCapability{
		name: "mcp_read", class: knowledge.FailureAuthentication,
		executions: &executions,
	}
	prompt := "check this project and analyze what this project is about? use gry mcp"
	result := runFailureLoop(t, prompt, []agent.Tool{tool}, []ai.AssistantMessage{
		repeatCapability("call-1", "mcp_read", "gry:search"),
		repeatCapability("call-2", "mcp_read", "gry:search"),
	})

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("capability executions = %d, want 1", got)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want the first failure plus one answer-only request", result.requests)
	}
}

// A retryable class gets exactly one retry, then the identical proposal is
// refused and the task closes. The bound is the failure class, not the vendor.
func TestRetryableCapabilityGetsOneRetryThenCloses(t *testing.T) {
	var executions atomic.Int64
	tool := failingCapability{
		name: "mcp_read", class: knowledge.FailureTimeout, retryable: true,
		executions: &executions,
	}
	prompt := "check this project and analyze what this project is about? use gry mcp"
	script := make([]ai.AssistantMessage, 0, 8)
	for i := 0; i < 8; i++ {
		script = append(script, repeatCapability(fmt.Sprintf("call-%d", i), "mcp_read", "gry:search"))
	}
	result := runFailureLoop(t, prompt, []agent.Tool{tool}, script)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if got := executions.Load(); got != 2 {
		t.Fatalf("capability executions = %d, want one failure plus one retry", got)
	}
	if result.requests > 4 {
		t.Fatalf("model requests = %d, want a bounded decision", result.requests)
	}
}

// A corrected proposal is allowed to proceed. The ledger refuses only the
// identical failed invocation; it does not close the capability or the task.
func TestCorrectedRetryAfterFailureIsAllowed(t *testing.T) {
	var executions, failures, successes atomic.Int64
	tool := conditionalCapability{
		executions: &executions,
		failures:   &failures,
		successes:  &successes,
	}
	prompt := "check this project and analyze what this project is about"
	answer := "The README describes the project; the source read confirms a Go entry point."
	result := runFailureLoop(t, prompt, []agent.Tool{tool, evidenceReadTool{}, evidenceListTool{}}, []ai.AssistantMessage{
		repeatCapability("bad", "mcp_read", "gry:nope"),
		{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "good", Name: "mcp_read", Arguments: map[string]any{"capability": "gry:search"}},
				ai.ToolCall{ID: "readme", Name: "read", Arguments: map[string]any{"path": "README.md"}},
				ai.ToolCall{ID: "main", Name: "read", Arguments: map[string]any{"path": "main.go"}},
			},
			StopReason: ai.StopToolUse,
		},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if got := failures.Load(); got != 1 {
		t.Fatalf("failing executions = %d, want 1", got)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("corrected executions = %d, want 1", got)
	}
	if result.requests != 3 {
		t.Fatalf("model requests = %d, want failure, correction, answer", result.requests)
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
		Provenance:     result.report.Provenance,
	})
	if !got.Complete() {
		t.Fatalf("a corrected run was not complete: %s", got.Reason)
	}
}

// conditionalCapability fails for one capability identity and succeeds for
// another. One tool instance serves both proposals, which is how a real MCP
// boundary behaves.
type conditionalCapability struct {
	executions *atomic.Int64
	failures   *atomic.Int64
	successes  *atomic.Int64
}

func (t conditionalCapability) Name() string        { return "mcp_read" }
func (t conditionalCapability) Description() string { return "a configured external capability" }
func (t conditionalCapability) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"capability":{"type":"string"}},
		"required":["capability"]
	}`))
}
func (t conditionalCapability) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (t conditionalCapability) Exclusive(map[string]any) bool { return false }
func (t conditionalCapability) Execute(_ agent.Context, args map[string]any) agent.Result {
	t.executions.Add(1)
	capability, _ := args["capability"].(string)
	if capability == "gry:nope" {
		t.failures.Add(1)
		return agent.Result{
			Text:             "external capability failed: unknown_capability",
			IsError:          true,
			FailureClass:     string(knowledge.FailureUnknownCapability),
			FailureRetryable: false,
		}
	}
	t.successes.Add(1)
	return agent.Result{
		Text: "MCP tool result: package main",
		Details: map[string]any{
			"status": "ok",
			"evidence": []knowledge.Evidence{{
				Kind: knowledge.EvidenceMCPToolResult,
				Provenance: knowledge.Provenance{
					Capability: knowledge.CapabilityMCPReadTool,
					Source:     "mcp:gry",
					Provider:   "gry",
				},
			}},
		},
	}
}
