package tui

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// strictRuntimeVerifyTool is the verify capability with the real argument
// contract: a schema the loop validates before execution. The fixture needs it
// to tell an offered call (which reaches schema validation) from a call the
// runtime already withdrew (which must be refused as a closed surface).
type strictRuntimeVerifyTool struct {
	schema *jsonschema.Schema
}

func newStrictRuntimeVerifyTool(t *testing.T) *strictRuntimeVerifyTool {
	t.Helper()
	schema, err := jsonschema.CompileMap(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"checks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"kind"},
					"properties": map[string]any{
						"kind": map[string]any{"type": "string"},
						"contains": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return &strictRuntimeVerifyTool{schema: schema}
}

func (t *strictRuntimeVerifyTool) Name() string                   { return "verify" }
func (t *strictRuntimeVerifyTool) Description() string            { return "observe the project runtime" }
func (t *strictRuntimeVerifyTool) Parameters() *jsonschema.Schema { return t.schema }
func (t *strictRuntimeVerifyTool) Approval() agent.Approval {
	return agent.Approval{Tier: agent.TierExec}
}
func (t *strictRuntimeVerifyTool) Exclusive(map[string]any) bool { return true }
func (t *strictRuntimeVerifyTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{
		Text:         "verification: PASS\nservice: npm run dev\n- http: PASS status 200",
		Details:      map[string]any{"verification": map[string]any{"status": "PASS"}},
		Executes:     true,
		Finished:     true,
		Verifies:     true,
		Verification: &agent.VerificationEvidence{Command: "npm run dev"},
		Runtime: &agent.RuntimeEvidence{
			Command: "npm run dev",
			URL:     "http://127.0.0.1:3000",
			PID:     4242,
			Checks:  1,
		},
	}
}

// answerWithCall is one assistant message that closes with prose and also asks
// for a tool. Models emit this shape routinely; the loop must judge the call
// against the surface it was offered, not assume the prose ended the run.
func answerWithCall(text string, call ai.ToolCall) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}, call},
		StopReason: ai.StopToolUse,
	}
}

// Once the runtime observation proves the objective, the action surface is
// closed. A late verify call in the same message as the closing answer is a
// continuation attempt on a finished task: it must be refused as unavailable,
// not dispatched into schema validation. The observed failure showed a second
// verify error printed after "completed · proven", which reads as work that is
// still happening after the task was declared done.
func TestRuntimeCompletionClosesSurfaceAgainstLateCall(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	badCall := ai.ToolCall{ID: "late-1", Name: "verify", Arguments: map[string]any{
		"checks": []any{map[string]any{"kind": "http", "contains": "ready"}},
	}}
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewBashTool(root), newStrictRuntimeVerifyTool(t)},
		[]ai.AssistantMessage{
			readinessToolCall("probe-1", "bash", map[string]any{"command": "echo environment ok"}),
			readinessToolCall("verify-1", "verify", map[string]any{}),
			answerWithCall("The project is serving at http://127.0.0.1:3000.", badCall),
			readinessAnswer("done"),
		})

	r.turn("run this project help me")

	proven := r.currentTask()
	if proven.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("objective = %s, want PROVEN: %s", proven.ObjectiveState(), proven.CompletionSummary())
	}
	// The proof is the runtime observation, not the incidental probe.
	if proven.Completion == nil || !proven.Completion.Executed {
		t.Fatalf("proof = %+v, want the executed runtime observation", proven.Completion)
	}

	var late *agent.ToolResultMessage
	for _, result := range r.toolResults() {
		if result.ToolCallID == "late-1" {
			copied := result
			late = &copied
		}
	}
	if late != nil {
		t.Fatalf("a call emitted after the proving observation was dispatched: %q", late.Text())
	}
	// The loop reports the proving request as the answer boundary; that is the
	// closure the late call must meet.
	if report, ok := r.controller.LastProjection(); ok {
		if report.Stage != string(cognition.StageComplete) {
			t.Fatalf("last projection stage = %q, want complete", report.Stage)
		}
		if len(report.ToolNames) != 0 {
			t.Fatalf("the completed objective still offered tools: %v", report.ToolNames)
		}
	}
}
