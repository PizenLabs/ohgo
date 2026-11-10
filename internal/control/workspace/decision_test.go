package workspace

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

const stagedWorkflowPrompt = `Check this Go e-commerce project and understand how the main order/checkout flow works.

Start by investigating the project without changing anything. If you identify a concrete issue that needs reproduction or deeper investigation, investigate it and reproduce it without modifying source code.

If the issue is confirmed, switch to the appropriate workspace, implement the fix, run the relevant tests, and verify that the fix actually works.

Keep the same task context throughout the process. Do not create a new task for continuation or verification. Do not claim completion without evidence.`

func TestAnalyzeRequestUsesCapabilityAndStagedStructure(t *testing.T) {
	cases := []struct {
		text       string
		capability Capability
		intent     Intent
	}{
		{"What is this project about?", CapabilityRead, IntentRead},
		{"Why is this repository structured this way?", CapabilityRead, IntentRead},
		{"Run the tests.", CapabilityExecute, IntentExecute},
		{"Fix the checkout validation bug.", CapabilityMutate, IntentExecute},
		{"Reproduce the checkout failure.", CapabilityInvestigate, IntentInvestigate},
		{"Investigate the project first. If you confirm a concrete issue, fix it.", CapabilityInvestigate, IntentInvestigate},
		{stagedWorkflowPrompt, CapabilityInvestigate, IntentInvestigate},
	}
	for _, tc := range cases {
		requirement := AnalyzeRequest(tc.text)
		if requirement.Capability != tc.capability || requirement.Intent != tc.intent {
			t.Errorf("AnalyzeRequest(%.40q) = %s/%s, want %s/%s",
				tc.text, requirement.Capability, requirement.Intent, tc.capability, tc.intent)
		}
	}
}

func TestDecideTransitionMatrix(t *testing.T) {
	cases := []struct {
		name         string
		active       Workspace
		text         string
		kind         DecisionKind
		target       Workspace
		automatic    bool
		confirmation bool
	}{
		{"ask read", Ask, "Explain the checkout flow.", DecisionStay, Ask, false, false},
		{"ask greeting", Ask, "hello", DecisionStay, Ask, false, false},
		{"ask execute", Ask, "Run the tests.", DecisionConfirm, Build, false, true},
		{"ask mutation", Ask, "Fix the checkout bug.", DecisionConfirm, Build, false, true},
		{"ask investigate", Ask, "Reproduce the checkout failure.", DecisionConfirm, Fuzz, false, true},
		{"ask staged", Ask, stagedWorkflowPrompt, DecisionConfirm, Fuzz, false, true},

		{"build mutation", Build, "Fix the checkout bug.", DecisionStay, Build, false, false},
		{"build execute", Build, "Run the tests.", DecisionStay, Build, false, false},
		{"build moderate read", Build, "Check this project.", DecisionStay, Build, false, false},
		{"build strong question", Build, "Why does this work?", DecisionConfirm, Ask, false, true},
		{"build investigate", Build, "Reproduce the failure under load.", DecisionAutomatic, Fuzz, true, false},
		{"build staged", Build, stagedWorkflowPrompt, DecisionAutomatic, Fuzz, true, false},

		{"fuzz investigate", Fuzz, "Reproduce the failure.", DecisionStay, Fuzz, false, false},
		{"fuzz execute", Fuzz, "Run the tests.", DecisionStay, Fuzz, false, false},
		{"fuzz moderate read", Fuzz, "Check this project.", DecisionStay, Fuzz, false, false},
		{"fuzz strong question", Fuzz, "Why does this work?", DecisionConfirm, Ask, false, true},
		{"fuzz staged", Fuzz, stagedWorkflowPrompt, DecisionStay, Fuzz, false, false},
		{"fuzz mutation", Fuzz, "Fix the checkout bug.", DecisionAutomatic, Build, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := DecideTransition(tc.active, AnalyzeRequest(tc.text))
			if decision.Kind != tc.kind || decision.Target != tc.target {
				t.Fatalf("decision = %s %s -> %s, want %s %s -> %s",
					decision.Kind, decision.Current, decision.Target, tc.kind, tc.active, tc.target)
			}
			if decision.Automatic() != tc.automatic || decision.NeedsConfirmation() != tc.confirmation {
				t.Fatalf("automatic/confirm = %v/%v, want %v/%v",
					decision.Automatic(), decision.NeedsConfirmation(), tc.automatic, tc.confirmation)
			}
			if tc.kind != DecisionStay && decision.Reason == "" {
				t.Error("a moving decision has no user-facing reason")
			}
			if decision.Current != tc.active {
				t.Errorf("decision changed the current workspace: %s -> %s", tc.active, decision.Current)
			}
			// Automatic movement is limited to the execution side. No automatic
			// decision may name ASK at either endpoint.
			if decision.Automatic() && (decision.Current == Ask || decision.Target == Ask) {
				t.Errorf("automatic decision crossed ASK: %s -> %s", decision.Current, decision.Target)
			}
		})
	}
}

// classifiedTool declares a broad action surface but reports the capability of
// the specific invocation, the way the real bash tool does.
type classifiedTool struct {
	fakeTool
	classes []agent.ActionClass
	cap     agent.Tier
}

func (t classifiedTool) ActionClasses() []agent.ActionClass   { return t.classes }
func (t classifiedTool) Capability(map[string]any) agent.Tier { return t.cap }

// A staged task moves phase through the model's own actions, not only through
// new user text. The capability a call needs is read from the call itself, so
// the control plane can apply the ordinary automatic policy before the call
// reaches the workspace that cannot perform it.
func TestToolCallTransitionRoutesCapabilityBoundary(t *testing.T) {
	finding := map[string]any{"kind": "finding", "target": "checkout", "reproduction": "submit", "evidence": "404"}
	change := map[string]any{"kind": "change", "target": "checkout"}
	plan := map[string]any{"kind": "plan", "target": "checkout"}
	write := map[string]any{"path": "checkout.go", "content": "package checkout\n"}

	artifact := fakeTool{name: "artifact", tier: agent.TierRead}
	writeTool := fakeTool{name: "write", tier: agent.TierWrite}
	readShell := classifiedTool{
		fakeTool: fakeTool{name: "bash", tier: agent.TierExec},
		classes:  []agent.ActionClass{agent.ActionExecute, agent.ActionMutate},
		cap:      agent.TierExec,
	}
	writeShell := classifiedTool{
		fakeTool: fakeTool{name: "bash", tier: agent.TierExec},
		classes:  []agent.ActionClass{agent.ActionExecute, agent.ActionMutate},
		cap:      agent.TierWrite,
	}

	cases := []struct {
		name      string
		active    Workspace
		tool      agent.Tool
		args      map[string]any
		automatic bool
		target    Workspace
	}{
		{"build finding", Build, artifact, finding, true, Fuzz},
		{"build change", Build, artifact, change, false, ""},
		{"fuzz finding", Fuzz, artifact, finding, false, ""},
		{"fuzz change", Fuzz, artifact, change, true, Build},
		{"fuzz write", Fuzz, writeTool, write, true, Build},
		{"ask write", Ask, writeTool, write, false, ""},
		{"fuzz plan", Fuzz, artifact, plan, false, ""},
		{"build write", Build, writeTool, write, false, ""},
		// A shell declares mutation as a possible class. The invocation, not
		// the declaration, decides whether this call needs BUILD.
		{"fuzz read-only shell", Fuzz, readShell, map[string]any{"command": "ls -la"}, false, ""},
		{"fuzz mutating shell", Fuzz, writeShell, map[string]any{"command": "touch x"}, true, Build},
		{"build read-only shell", Build, readShell, map[string]any{"command": "go test ./..."}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, automatic := ToolCallTransition(tc.active, tc.tool, tc.args)
			if automatic != tc.automatic {
				t.Fatalf("automatic = %v, want %v (decision=%+v)", automatic, tc.automatic, decision)
			}
			if automatic && decision.Target != tc.target {
				t.Fatalf("target = %s, want %s", decision.Target, tc.target)
			}
			if automatic && (decision.Current == Ask || decision.Target == Ask) {
				t.Fatalf("a tool handoff crossed ASK: %s -> %s", decision.Current, decision.Target)
			}
		})
	}
}

// The exact failure that exposed the missing tool-boundary handoff: a BUILD
// session files a finding, and the control plane moves to FUZZ rather than
// letting the artifact tool refuse the call.
func TestToolCallTransitionMovesBuildToFuzzForAFindingArtifact(t *testing.T) {
	artifact := fakeTool{name: "artifact", tier: agent.TierRead}
	decision, ok := ToolCallTransition(Build, artifact, map[string]any{
		"kind": "finding", "target": "order/checkout",
		"reproduction": "POST /api/v1/orders", "evidence": "404",
	})
	if !ok || !decision.Automatic() || decision.Target != Fuzz {
		t.Fatalf("finding under BUILD = %+v automatic=%v, want an automatic FUZZ handoff", decision, ok)
	}
}

// A shell that declares ActionMutate is not mutating just because the tool can
// mutate. Treating the declared class as the invocation moved every read-only
// inspection from FUZZ to BUILD and reported a repository change the request
// never asked for.
func TestToolCallTransitionKeepsReadOnlyShellInFuzz(t *testing.T) {
	shell := classifiedTool{
		fakeTool: fakeTool{name: "bash", tier: agent.TierExec},
		classes:  []agent.ActionClass{agent.ActionExecute, agent.ActionMutate},
		cap:      agent.TierExec,
	}
	decision, automatic := ToolCallTransition(Fuzz, shell, map[string]any{"command": "ls /Applications"})
	if automatic {
		t.Fatalf("read-only shell under FUZZ moved to %s: %+v", decision.Target, decision)
	}
}

// Analysis and policy are pure: inspecting them never moves authority.
func TestTransitionDecisionIsDataOnly(t *testing.T) {
	for _, active := range All() {
		before := active
		decision := DecideTransition(active, AnalyzeRequest("reproduce the failure"))
		if active != before {
			t.Fatal("the decision mutated its input workspace")
		}
		if decision.Current != active {
			t.Fatalf("decision reported current %s, want %s", decision.Current, active)
		}
	}
}
