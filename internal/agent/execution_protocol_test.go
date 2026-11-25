package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// normalizedTextStreamFn serves one normalized text response per request. It
// drives the loop through the same provider boundary production uses, so a
// serialized envelope is recognized (or rejected) before the loop sees it.
func normalizedTextStreamFn(messages ...string) ai.StreamFn {
	var (
		mu    sync.Mutex
		index int
	)
	return func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
		mu.Lock()
		position := index
		index++
		mu.Unlock()
		if position >= len(messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{}), nil
		}
		return ai.NormalizeStream(ctx, ai.NewTextStream(messages[position]), ai.NormalizeOptions{
			Provider: "test",
		}), nil
	}
}

func protocolLoop(t *testing.T, stream ai.StreamFn, tool Tool) *Loop {
	t.Helper()
	loop, err := New(Config{
		Model:                    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:                   stream,
		Tools:                    []Tool{tool},
		ExecutionRecoveryRequest: "emit the actual tool call",
		ProtocolRecoveryRequest:  "re-emit the call canonically",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

// Test 4: prose that announces an action is not an action. The loop spends one
// bounded recovery request and then reports the protocol failure; the tool is
// never invoked.
func TestExecutionIntentWithoutToolIsClassifiedNotExecuted(t *testing.T) {
	tool := newScriptedTool("bash", "started")
	stream := normalizedTextStreamFn(
		"I'll use bash to start the server.",
		"I'll start the server with npx live-server.",
	)
	loop := protocolLoop(t, stream, tool)

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "use npx live-server test this project"}})
	if !errors.Is(err, ErrExecutionIntentWithoutTool) {
		t.Fatalf("Run error = %v, want ErrExecutionIntentWithoutTool", err)
	}
	if len(tool.invoked()) != 0 {
		t.Fatal("model prose was converted into an executed tool call")
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want intent + one bounded recovery", len(turns))
	}
}

// Test 5: an empty response with no tool is distinct from execution intent.
func TestNoModelOutputIsClassified(t *testing.T) {
	tool := newScriptedTool("bash", "started")
	loop := protocolLoop(t, normalizedTextStreamFn(""), tool)

	_, err := loop.Run(context.Background(), []Message{UserMessage{Text: "start the server"}})
	if !errors.Is(err, ErrNoModelOutput) {
		t.Fatalf("Run error = %v, want ErrNoModelOutput", err)
	}
	if len(tool.invoked()) != 0 {
		t.Fatal("empty output executed a tool")
	}
}

// Test 3: malformed serialized syntax never executes. When the bounded
// recovery produces nothing, the run reports the normalization failure rather
// than treating the diagnostic text as an answer.
func TestMalformedSerializedToolCallIsClassifiedAndNotExecuted(t *testing.T) {
	tool := newScriptedTool("bash", "started")
	malformed := `<longcat_tool_call>{"name":"bash","arguments":{"command":` +
		`"npx live-server"}</longcat_tool_call>`
	loop := protocolLoop(t, normalizedTextStreamFn(malformed), tool)

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "start the server"}})
	if !errors.Is(err, ErrToolNormalizationFailed) {
		t.Fatalf("Run error = %v, want ErrToolNormalizationFailed", err)
	}
	if len(tool.invoked()) != 0 {
		t.Fatal("a malformed serialized call reached execution")
	}
	if len(turns) == 0 || len(turns[0].Assistant.NormalizationFailures) == 0 {
		t.Fatal("the malformed envelope left no structured normalization evidence")
	}
	failure := turns[0].Assistant.NormalizationFailures[0]
	if failure.Status != ai.NormalizationInvalid {
		t.Fatalf("status = %q, want INVALID", failure.Status)
	}
	if failure.RawFragment == "" {
		t.Fatal("the raw provider fragment was not preserved for audit")
	}
	if strings.Contains(turns[0].Assistant.Text(), "<longcat_tool_call>") {
		t.Fatal("raw protocol syntax was published as assistant prose")
	}
}

// Test 13: the recovery itself is bounded. Two malformed responses stop after
// one recovery request, never an infinite retry.
func TestMalformedToolSyntaxRecoveryIsBounded(t *testing.T) {
	tool := newScriptedTool("bash", "started")
	malformed := `<longcat_tool_call>{"name":"bash","arguments":{"command":` +
		`"npx live-server"}</longcat_tool_call>`
	// A complete envelope with no call is still a normalization failure; the
	// recovery must not turn an empty envelope into an action.
	broken := `<dotsfunctioncall></dotsfunctioncall>`
	stream := normalizedTextStreamFn(malformed, broken)
	loop := protocolLoop(t, stream, tool)

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "start the server"}})
	if !errors.Is(err, ErrToolNormalizationFailed) {
		t.Fatalf("Run error = %v, want ErrToolNormalizationFailed", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want exactly one recovery attempt", len(turns))
	}
	if len(tool.invoked()) != 0 {
		t.Fatal("a malformed call executed after recovery")
	}
}

// Test 1: the same loop still executes a valid native call.
func TestValidNativeToolCallStillExecutes(t *testing.T) {
	tool := newScriptedTool("bash", "started")
	streamFn, _ := scriptedStream(
		ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID: "c1", Name: "bash", Arguments: map[string]any{"command": "npx live-server"},
			}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "started"}}},
	)
	loop := protocolLoop(t, streamFn, tool)
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "start the server"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tool.invoked()) != 1 {
		t.Fatalf("tool invocations = %d, want 1", len(tool.invoked()))
	}
}

// A capability that is static-execution but refines to write for this call
// must be inside the edit journal's conservative boundary. Verify runs service
// and check commands, some of which can mutate.
type perCallCapabilityTool struct {
	scriptedTool
	tier Tier
}

func (t *perCallCapabilityTool) ActionClasses() []ActionClass   { return []ActionClass{ActionExecute} }
func (t *perCallCapabilityTool) Capability(map[string]any) Tier { return t.tier }

func TestBatchMayMutateUsesPerCallCapability(t *testing.T) {
	tool := &perCallCapabilityTool{scriptedTool: scriptedTool{name: "verify"}, tier: TierWrite}
	if !batchMayMutate(map[string]Tool{"verify": tool}, []ai.ToolCall{{Name: "verify"}}) {
		t.Fatal("a per-call write capability was left outside the edit journal")
	}
	tool.tier = TierExec
	if batchMayMutate(map[string]Tool{"verify": tool}, []ai.ToolCall{{Name: "verify"}}) {
		t.Fatal("an execution-only invocation was treated as a mutation")
	}
}

// normalizedRequestStream serves normalized responses and records every request
// the loop sent, so a test can assert the surface the recovery actually saw.
func normalizedRequestStream(messages ...string) (ai.StreamFn, func() []ai.Request) {
	var (
		mu       sync.Mutex
		index    int
		requests []ai.Request
	)
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		position := index
		index++
		requests = append(requests, req)
		mu.Unlock()
		if position >= len(messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{}), nil
		}
		return ai.NormalizeStream(ctx, ai.NewTextStream(messages[position]), ai.NormalizeOptions{
			Provider: "test",
		}), nil
	}
	return stream, func() []ai.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]ai.Request(nil), requests...)
	}
}

// decoratedEnvelope is the registered <tool_call> tag plus a fabricated call
// id, the spelling that reached a persisted answer and a verified footer.
func decoratedEnvelope(id, path string) string {
	return "<tool_call_" + id + ">\n<function=read>\n<parameter=limit>\n240\n" +
		"</parameter>\n<parameter=path>\n" + path + "\n</parameter>\n</function>\n</tool_call_" + id + ">"
}

// closedAnswerProjection is the answer boundary: the runtime holds enough
// evidence, so the loop withdraws every tool before the model answers.
func closedAnswerProjection(ProjectionRequest) ProjectionResult {
	return ProjectionResult{Report: ProjectionReport{AnswerEligible: true}}
}

// A decorated tool-call envelope emitted at the answer boundary is not an
// answer. The surface is closed, so the one recovery asks for prose from the
// evidence already observed; that answer commits and the diagnostic never
// becomes the result.
func TestSerializedSyntaxAtAnswerBoundaryIsNotTheAnswer(t *testing.T) {
	envelope := decoratedEnvelope("24b82c79", "crates/gry-mcp/src/tools.rs")
	const answer = "The project is a Rust workspace with a CLI, a store, and an MCP server."
	stream, requests := normalizedRequestStream(envelope, answer)
	tool := newScriptedTool("read", "result")
	loop, err := New(Config{
		Model:         catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:        stream,
		Tools:         []Tool{tool},
		Project:       closedAnswerProjection,
		AnswerRequest: "no tool is available; answer from the observed evidence now",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "what is this project?"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want the residue turn and the prose answer", len(turns))
	}
	if !turns[0].Assistant.ProtocolResidue {
		t.Fatal("the decorated envelope was not recorded as protocol residue")
	}
	if turns[0].Answer == AnswerCommitted {
		t.Fatal("the residue diagnostic was committed as the answer")
	}
	if turns[1].Answer != AnswerCommitted || turns[1].Assistant.Text() != answer {
		t.Fatalf("final answer = %q (%s), want the prose answer committed", turns[1].Assistant.Text(), turns[1].Answer)
	}
	if len(tool.invoked()) != 0 {
		t.Fatal("a decorated envelope reached execution")
	}
	if got := requests(); len(got) != 2 {
		t.Fatalf("requests = %d, want residue + one recovery", len(got))
	} else {
		if len(got[1].Tools) != 0 {
			t.Fatalf("recovery offered tools %v after the answer boundary closed", got[1].Tools)
		}
		if state := lastRuntimeState(t, got[1]); !strings.Contains(state, "no tool is available") {
			t.Fatalf("recovery did not ask for a prose answer: %q", state)
		}
	}
}

// The recovery is bounded. A second decorated envelope at the answer boundary
// ends the run as a protocol failure, and no turn in the run is committed as
// the answer.
func TestSerializedSyntaxRecoveryAtAnswerBoundaryIsBounded(t *testing.T) {
	first := decoratedEnvelope("24b82c79", "crates/gry-mcp/src/tools.rs")
	second := decoratedEnvelope("24b82c80", "crates/gry-query/src/search.rs")
	stream, requests := normalizedRequestStream(first, second)
	loop, err := New(Config{
		Model:         catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:        stream,
		Tools:         []Tool{newScriptedTool("read", "result")},
		Project:       closedAnswerProjection,
		AnswerRequest: "no tool is available; answer from the observed evidence now",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "what is this project?"}})
	if !errors.Is(err, ErrToolNormalizationFailed) {
		t.Fatalf("Run error = %v, want ErrToolNormalizationFailed", err)
	}
	if len(requests()) != 2 {
		t.Fatalf("requests = %d, want exactly one recovery attempt", len(requests()))
	}
	for _, turn := range turns {
		if turn.Answer == AnswerCommitted {
			t.Fatalf("a protocol failure was committed as the answer: %q", turn.Assistant.Text())
		}
	}
	if completion := EvaluateCompletion(turns, false); completion.Complete() {
		t.Fatalf("completion = %#v, want a review verdict for protocol residue", completion)
	}
}
