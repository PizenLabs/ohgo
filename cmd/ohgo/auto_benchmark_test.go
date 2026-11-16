package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// This file is the deterministic AUTO orchestration benchmark. It exercises the
// host seam rather than the policy internals: what authority a real session
// would move to, which capability a tool-boundary phase change needs, and
// whether a refused boundary can reach the model or a tool.
//
// The live acceptance matrix is separate because a provider cannot be part of a
// deterministic gate.

// routingCase is one prompt observed from one live role.
type routingCase struct {
	prompt string
	active workspace.Workspace
	// want is "stay", "auto:<target>", or "confirm:<target>".
	want string
}

func (c routingCase) name() string {
	return string(c.active) + "/" + c.prompt
}

// autoRoutingMatrix covers information, analysis, recommendation, execution,
// mutation, ambiguous wording, and the master objective. Every row is evaluated
// as a whole request, not by matching one keyword.
func autoRoutingMatrix() []routingCase {
	readStrong := []string{
		"What is this project?",
		"Explain what this repository does.",
		"What technologies does this project use?",
		"Analyze the architecture of this project.",
		"Explain how the services communicate.",
		"What would you improve first?",
		"What database would fit this architecture?",
		"How should this project evolve?",
		"What is dependency injection?",
		"What is Go generics?",
		"Analyze this project and suggest technologies to complete it.",
		"Explain how I would fix this bug.",
		"Review this code and tell me whether it needs fixing.",
		"What would you change to fix the architecture?",
	}
	readAmbiguous := []string{
		"Identify the main architectural weaknesses.",
		"Suggest technologies that could complete this project.",
	}
	var cases []routingCase
	for _, prompt := range readStrong {
		cases = append(cases,
			routingCase{prompt: prompt, active: workspace.Ask, want: "stay"},
			routingCase{prompt: prompt, active: workspace.Build, want: "confirm:ASK"},
			routingCase{prompt: prompt, active: workspace.Fuzz, want: "confirm:ASK"},
		)
	}
	for _, prompt := range readAmbiguous {
		cases = append(cases,
			routingCase{prompt: prompt, active: workspace.Ask, want: "stay"},
			routingCase{prompt: prompt, active: workspace.Build, want: "stay"},
			routingCase{prompt: prompt, active: workspace.Fuzz, want: "stay"},
		)
	}
	for _, prompt := range []string{
		"Run a benchmark and compare the performance.",
	} {
		cases = append(cases,
			routingCase{prompt: prompt, active: workspace.Ask, want: "confirm:BUILD"},
			routingCase{prompt: prompt, active: workspace.Build, want: "stay"},
			routingCase{prompt: prompt, active: workspace.Fuzz, want: "stay"},
		)
	}
	for _, prompt := range []string{
		"Fix the failing test.",
		"Fix this bug and make the tests pass.",
		"Analyze and fix this wording.",
		"Inspect this project, identify the most important backend issue, fix it, and verify the result with tests.",
	} {
		cases = append(cases,
			routingCase{prompt: prompt, active: workspace.Ask, want: "confirm:BUILD"},
			routingCase{prompt: prompt, active: workspace.Build, want: "stay"},
			// A standalone FUZZ task has never been authorized to mutate, so
			// the move to BUILD is the human's decision.
			routingCase{prompt: prompt, active: workspace.Fuzz, want: "confirm:BUILD"},
		)
	}
	for _, prompt := range []string{
		"Investigate this project and analyze what this project is about?",
	} {
		cases = append(cases,
			routingCase{prompt: prompt, active: workspace.Ask, want: "confirm:FUZZ"},
			routingCase{prompt: prompt, active: workspace.Build, want: "auto:FUZZ"},
			routingCase{prompt: prompt, active: workspace.Fuzz, want: "stay"},
		)
	}
	return cases
}

// TestAutoRoutingBenchmarkMatrix is the routing regression matrix: expected
// authority disposition for each category and live role.
func TestAutoRoutingBenchmarkMatrix(t *testing.T) {
	for _, tc := range autoRoutingMatrix() {
		t.Run(tc.name(), func(t *testing.T) {
			held := &harness{workspace: tc.active.Label(), root: t.TempDir()}
			transition, ok := turnTransition(held, workspace.ContractFor(tc.active), tc.prompt)
			got := "stay"
			if ok && transition.Kind != tui.TransitionKindProposal {
				switch {
				case transition.Automatic:
					got = "auto:" + transition.Proposed
				case transition.Proposed != "":
					got = "confirm:" + transition.Proposed
				}
			}
			if got != tc.want {
				t.Fatalf("%s under %s = %s (intent %s/%s, %q); want %s",
					tc.prompt, tc.active, got, transition.Intent, transition.Strength,
					transition.Reason, tc.want)
			}
			if held.activeWorkspace() != tc.active {
				t.Fatalf("prediction moved the live gate to %s before approval", held.activeWorkspace())
			}
		})
	}
}

// TestAutoAskExecutionBoundaryNeverReachesTheModel proves the pre-execution
// admission rule for a request that needs a command: the ASK session refuses,
// and neither the provider nor a tool is touched.
func TestAutoAskExecutionBoundaryNeverReachesTheModel(t *testing.T) {
	gate := newAuthorityGate(workspace.Ask)
	bash := &spyTool{name: "bash", tier: agent.TierExec}
	stream := &routeStream{call: ai.ToolCall{ID: "call_1", Name: "bash", Arguments: map[string]any{"command": "go test ./..."}}}
	held := routedHarness(gate, newRoutedLoop(t, stream.fn(), gate, bash), "ASK")

	var out strings.Builder
	if err := repl(held, &out, strings.NewReader("Run a benchmark and compare the performance.\n/quit\n"), nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	if got := stream.requests(); got != 0 {
		t.Fatalf("provider requests = %d, want 0: the boundary must be admitted before the model", got)
	}
	if got := bash.executions(); got != 0 {
		t.Fatalf("command executions = %d, want 0", got)
	}
	if !strings.Contains(out.String(), "BUILD") {
		t.Fatalf("refusal did not name the authority the request needs: %q", out.String())
	}
}

// TestAutoFuzzMutationBoundaryNeedsTheTaskGrant proves a standalone FUZZ
// session cannot widen itself into mutation at the tool boundary either.
func TestAutoFuzzMutationBoundaryNeedsTheTaskGrant(t *testing.T) {
	contract := workspace.ContractFor(workspace.Fuzz)
	write := &spyTool{name: "write", tier: agent.TierWrite}
	if decision, ok := automaticToolHandoff(workspace.Fuzz, contract, write, map[string]any{"path": "x.go"}); ok {
		t.Fatalf("unauthorized FUZZ write moved authority automatically: %+v", decision)
	}

	authorized := workspace.ContractFor(workspace.Build)
	decision, ok := automaticToolHandoff(workspace.Fuzz, authorized, write, map[string]any{"path": "x.go"})
	if !ok || !decision.Automatic() || decision.Target != workspace.Build {
		t.Fatalf("authorized FUZZ write = %+v ok=%v, want an automatic BUILD handoff", decision, ok)
	}
}

// TestAutoMasterTaskOrchestratesBuildFuzzBuild is the master-objective loop:
// one BUILD-authorized task files a finding (automatic FUZZ), reproduces there,
// changes source (automatic BUILD), and verifies under the restored role. The
// task identity and contract never move; only the phase does.
func TestAutoMasterTaskOrchestratesBuildFuzzBuild(t *testing.T) {
	artifact := &spyTool{name: "artifact", tier: agent.TierRead}
	bash := &spyTool{name: "bash", tier: agent.TierExec}
	write := &spyTool{name: "write", tier: agent.TierWrite}
	gate := newAuthorityGate(workspace.Build)
	contract := workspace.ContractFor(workspace.Build)

	var phases []string
	loop := newRoutedLoop(t, stagedMasterStream().fn(), gate, artifact, bash, write)
	loop.SetWorkspaceHandoff(func(_ ai.ToolCall, tool agent.Tool, args map[string]any) bool {
		decision, ok := automaticToolHandoff(gate.Workspace(), contract, tool, args)
		if !ok {
			return false
		}
		gate.Set(decision.Target)
		phases = append(phases, decision.Target.Label())
		return true
	})

	turns, err := loop.Run(t.Context(), []agent.Message{agent.UserMessage{
		Text: "Inspect this project, identify the most important backend issue, fix it, and verify the result with tests.",
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(phases) != 2 || phases[0] != "FUZZ" || phases[1] != "BUILD" {
		t.Fatalf("authority phases = %v, want [FUZZ BUILD]", phases)
	}
	if gate.Workspace() != workspace.Build {
		t.Fatalf("final authority = %s, want the task's BUILD contract", gate.Workspace())
	}
	if artifact.executions() != 1 || bash.executions() != 2 || write.executions() != 1 {
		t.Fatalf("tool executions artifact/bash/write = %d/%d/%d, want 1/2/1",
			artifact.executions(), bash.executions(), write.executions())
	}
	if len(turns) != 5 {
		t.Fatalf("model turns = %d, want 5 (finding, repro, change, verify, answer)", len(turns))
	}
	if got := turns[len(turns)-1].Assistant.Text(); got != "verified" {
		t.Fatalf("final answer = %q, want the verification result", got)
	}
}

// TestAutoAskNeverEscalatesAtTheToolBoundary proves even a legitimate phase
// signal (an investigation finding) is refused when the session has ASK
// authority: crossing ASK stays a human decision.
func TestAutoAskNeverEscalatesAtTheToolBoundary(t *testing.T) {
	artifact := &spyTool{name: "artifact", tier: agent.TierRead}
	if decision, ok := automaticToolHandoff(workspace.Ask, workspace.ContractFor(workspace.Ask), artifact, map[string]any{"kind": "finding"}); ok {
		t.Fatalf("ASK tool boundary escalated automatically: %+v", decision)
	}
}

// messageStream answers each provider request with the next scripted message.
type messageStream struct {
	mu       sync.Mutex
	index    int
	messages []ai.AssistantMessage
}

func (s *messageStream) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index
}

func (s *messageStream) fn() ai.StreamFn {
	return func(context.Context, ai.Request) (*ai.Stream, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.index >= len(s.messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{StopReason: ai.StopEndTurn}), nil
		}
		message := s.messages[s.index]
		s.index++
		return ai.NewCompletedStream(message), nil
	}
}

// stagedMasterStream is one authorized master task observed as four concrete
// actions plus the final report.
func stagedMasterStream() *messageStream {
	call := func(id, name string, args map[string]any) ai.AssistantMessage {
		return ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: id, Name: name, Arguments: args},
			},
			StopReason: ai.StopToolUse,
		}
	}
	text := func(value string) ai.AssistantMessage {
		return ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: value}},
			StopReason: ai.StopEndTurn,
		}
	}
	return &messageStream{messages: []ai.AssistantMessage{
		call("c1", "artifact", map[string]any{"kind": "finding"}),
		call("c2", "bash", map[string]any{"command": "go test ./..."}),
		call("c3", "write", map[string]any{"path": "x.go"}),
		call("c4", "bash", map[string]any{"command": "go test ./..."}),
		text("verified"),
	}}
}

// TestAutoReasoningBenchmarkDemandLevels checks the demand is a function of the
// work's structure, not of the role, and that later task evidence raises it.
func TestAutoReasoningBenchmarkDemandLevels(t *testing.T) {
	demand := func(prompt string, investigate bool) cognition.ReasoningDemand {
		intent := workspace.BuildIntentContract(prompt)
		return cognition.AssessReasoningDemand(cognition.ReasoningInput{
			Intent: &intent, Investigate: investigate,
			RemainingTurns: -1, RemainingModelCalls: -1,
		})
	}

	if got := demand("Rename this variable.", false); got != cognition.ReasoningMinimal {
		t.Errorf("mechanical rename demand = %s, want minimal", got.Label())
	}
	if got := demand("Trace this request flow.", true); got != cognition.ReasoningModerate {
		t.Errorf("request trace demand = %s, want moderate", got.Label())
	}

	// A diagnosis with no evidence yet is moderate; once the task holds two
	// competing hypotheses it is high. The same task gains demand as evidence
	// arrives, without the role changing.
	diagnose := workspace.BuildIntentContract("Diagnose a cross-package concurrency issue.")
	// The runtime learns the work spans several packages once it has inspected
	// them: three capabilities and four observed entities are the structural
	// evidence a diagnosis contract accumulates, not a keyword.
	diagnose.Grounding = &contract.IntentGrounding{
		Actions: []contract.GroundingAction{
			contract.GroundingRead, contract.GroundingInvestigate, contract.GroundingExecute,
		},
		Entities: []contract.Entity{
			{Value: "concurrency"}, {Value: "packages"}, {Value: "races"}, {Value: "tests"},
		},
	}
	base := cognition.ReasoningInput{
		Intent: &diagnose, Investigate: true,
		RemainingTurns: -1, RemainingModelCalls: -1,
	}
	if got := cognition.AssessReasoningDemand(base); got == cognition.ReasoningMinimal {
		t.Errorf("diagnosis with no evidence = %s, want at least moderate", got.Label())
	}
	withEvidence := base
	withEvidence.OpenHypotheses = 2
	withEvidence.UnresolvedQuestions = 2
	if got := cognition.AssessReasoningDemand(withEvidence); got != cognition.ReasoningHigh {
		t.Errorf("diagnosis with competing hypotheses and open questions = %s, want high", got.Label())
	}
}

// TestAutoReasoningBenchmarkBudgetClamp checks AUTO lowers demand under a
// constrained run rather than spending the last request thinking.
func TestAutoReasoningBenchmarkBudgetClamp(t *testing.T) {
	cases := []struct {
		name string
		in   cognition.ReasoningInput
		want cognition.ReasoningDemand
	}{
		{"one remaining call", cognition.ReasoningInput{RemainingTurns: 10, RemainingModelCalls: 1}, cognition.ReasoningMinimal},
		{"one remaining turn", cognition.ReasoningInput{RemainingTurns: 1, RemainingModelCalls: 10}, cognition.ReasoningMinimal},
		{"three remaining calls", cognition.ReasoningInput{RemainingTurns: 3, RemainingModelCalls: 3}, cognition.ReasoningModerate},
		{"context nearly full", cognition.ReasoningInput{RemainingTurns: -1, RemainingModelCalls: -1, ContextWindow: 1000, ContextUsed: 950}, cognition.ReasoningMinimal},
		{"context under pressure", cognition.ReasoningInput{RemainingTurns: -1, RemainingModelCalls: -1, ContextWindow: 1000, ContextUsed: 800}, cognition.ReasoningModerate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cognition.ClampReasoningDemand(cognition.ReasoningHigh, tc.in); got != tc.want {
				t.Fatalf("clamped demand = %s, want %s", got.Label(), tc.want.Label())
			}
		})
	}
}

// TestAutoReasoningBenchmarkProviderCapability checks the native mapping never
// invents a value: an advertised rung is chosen, everything else is omitted.
func TestAutoReasoningBenchmarkProviderCapability(t *testing.T) {
	full := ai.Model{Provider: "test", ID: "full", Reasoning: true, ReasoningEfforts: []string{"minimal", "low", "medium", "high"}}
	limited := ai.Model{Provider: "test", ID: "limited", Reasoning: true, ReasoningEfforts: []string{"low", "high"}}
	disabled := ai.Model{Provider: "test", ID: "off", Reasoning: true, ReasoningDisable: "none"}
	plain := ai.Model{Provider: "test", ID: "plain"}
	unknown := ai.Model{Provider: "test", ID: "unknown", Reasoning: true}
	mandatory := ai.Model{Provider: "test", ID: "mandatory", Reasoning: true, ReasoningMandatory: true}

	cases := []struct {
		name    string
		model   ai.Model
		demand  cognition.ReasoningDemand
		enabled bool
		level   string
	}{
		{"full minimal", full, cognition.ReasoningMinimal, true, "minimal"},
		{"full moderate", full, cognition.ReasoningModerate, true, "medium"},
		{"full high", full, cognition.ReasoningHigh, true, "high"},
		{"limited high", limited, cognition.ReasoningHigh, true, "high"},
		{"disable value", disabled, cognition.ReasoningMinimal, true, "none"},
		{"no capability", plain, cognition.ReasoningHigh, false, ""},
		{"unknown ladder", unknown, cognition.ReasoningHigh, false, ""},
		{"mandatory minimal", mandatory, cognition.ReasoningMinimal, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled, level := cognition.ReasoningSetting(tc.model, tc.demand)
			if enabled != tc.enabled || level != tc.level {
				t.Fatalf("setting = %v/%q, want %v/%q", enabled, level, tc.enabled, tc.level)
			}
			if level != "" {
				for _, native := range tc.model.ReasoningEfforts {
					if level == native {
						return
					}
				}
				if level != tc.model.ReasoningDisable {
					t.Fatalf("selected %q is not a value the model publishes", level)
				}
			}
		})
	}
}

// TestAutoSessionKeepsItsGrantAcrossAutomaticPhases is the stateless-host
// regression for authority loss: a BUILD session that moves to FUZZ to
// investigate must return to BUILD for the fix without losing the mutation
// grant it started with. Current role is not the task's authority.
func TestAutoSessionKeepsItsGrantAcrossAutomaticPhases(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	stream := &messageStream{messages: []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "investigated"}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "fixed"}}, StopReason: ai.StopEndTurn},
	}}
	held := routedHarness(gate, newRoutedLoop(t, stream.fn(), gate,
		&spyTool{name: "read", tier: agent.TierRead},
		&spyTool{name: "write", tier: agent.TierWrite},
		&spyTool{name: "bash", tier: agent.TierExec}), "BUILD")
	held.contract = workspace.ContractFor(workspace.Build)

	var out strings.Builder
	if err := repl(held, &out,
		strings.NewReader("Investigate this project and analyze what this project is about?\n"+
			"Fix the failing test.\n/quit\n"), nil); err != nil {
		t.Fatalf("repl: %v", err)
	}

	if !strings.Contains(out.String(), "workspace BUILD -> FUZZ") {
		t.Errorf("investigation did not hand off to FUZZ:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "workspace FUZZ -> BUILD") {
		t.Errorf("the authorized fix did not return to BUILD:\n%s", out.String())
	}
	if got := gate.Workspace(); got != workspace.Build {
		t.Fatalf("final authority = %s, want BUILD", got)
	}
	if span := stream.requests(); span != 2 {
		t.Fatalf("provider requests = %d, want 2\n%s", span, out.String())
	}
}
