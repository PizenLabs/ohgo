package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
)

func TestBenchmarkGateKeepsWorkspaceAuthority(t *testing.T) {
	root := t.TempDir()
	write := tools.NewWriteTool(root)
	bash := tools.NewBashTool(root)
	read := tools.NewReadTool(root)

	ask := newBenchmarkGate(root, workspace.Ask)
	if got := ask.Evaluate(ai.ToolCall{ID: "w1", Name: "write", Arguments: map[string]any{"path": "x", "content": "y"}}, write); got.Outcome != security.OutcomeDeny {
		t.Fatalf("ASK write decision = %s, want DENY", got.Outcome)
	}
	if got := ask.Evaluate(ai.ToolCall{ID: "r1", Name: "read", Arguments: map[string]any{"path": "x"}}, read); got.Outcome != security.OutcomeAuto {
		t.Fatalf("ASK read decision = %s, want AUTO", got.Outcome)
	}

	build := newBenchmarkGate(root, workspace.Build)
	call := ai.ToolCall{ID: "w2", Name: "write", Arguments: map[string]any{"path": "x", "content": "y"}}
	decision := build.Evaluate(call, write)
	if decision.Outcome != security.OutcomeApprove {
		t.Fatalf("BUILD write decision = %s, want APPROVE", decision.Outcome)
	}
	if _, ok := build.Approve(call, decision); !ok {
		t.Fatal("benchmark auto-approval refused a permitted write")
	}
	if confirmed := build.Evaluate(call, write); confirmed.Outcome != security.OutcomeAuto {
		t.Fatalf("confirmed decision = %s, want AUTO", confirmed.Outcome)
	}

	fuzz := newBenchmarkGate(root, workspace.Fuzz)
	execCall := ai.ToolCall{ID: "e1", Name: "bash", Arguments: map[string]any{"command": "echo hi"}}
	execDecision := fuzz.Evaluate(execCall, bash)
	if execDecision.Outcome != security.OutcomeApprove || !execDecision.ReadOnly {
		t.Fatalf("FUZZ exec decision = %+v, want APPROVE/read-only", execDecision)
	}
	writeCall := ai.ToolCall{ID: "e2", Name: "bash", Arguments: map[string]any{"command": "echo hi > x"}}
	if got := fuzz.Evaluate(writeCall, bash); got.Outcome != security.OutcomeDeny {
		t.Fatalf("FUZZ mutating command = %s, want DENY", got.Outcome)
	}
}
