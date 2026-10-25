// Command intent benchmarks the deterministic intent-grounding layer.
//
// It is a behavioural benchmark, not a model benchmark: every case runs
// without a provider. It measures extraction fidelity, semantic admission,
// and completion against the same scripted model behaviour with grounding
// enabled and disabled. The disabled arm is the runtime before this layer.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/jsonschema"
	"github.com/PizenLabs/ohgo/workspace"
)

type extractionCase struct {
	ID          string
	Request     string
	Action      contract.GroundingAction
	Actions     []contract.GroundingAction
	Entities    []string
	Scope       contract.GroundingScopeKind
	Ambiguity   string
	Description string
}

type behaviorCase struct {
	ID          string
	Request     string
	Turns       []agent.Turn
	Answer      string
	Negative    bool
	Description string
	// WantGrounded is the expected grounded verdict.
	WantGrounded bool
}

func main() {
	jsonOut := flag.Bool("json", false, "emit JSON instead of a markdown report")
	flag.Parse()

	extraction := extractionCases()
	behaviors := behaviorCases()

	var entityExpected, entityFound, actionOK, scopeOK, ambiguityOK int
	var groundingBytes int
	var assumptionTotal, unsupportedAssumptions int
	for _, tc := range extraction {
		intent := workspace.BuildIntentContract(tc.Request)
		g := intent.Grounding
		if g == nil {
			continue
		}
		for _, assumption := range g.Assumptions {
			assumptionTotal++
			if assumption.Kind == contract.AssumptionUnsupported {
				unsupportedAssumptions++
			}
		}
		if len(tc.Entities) > 0 {
			entityExpected += len(tc.Entities)
			values := g.EntityValues()
			for _, want := range tc.Entities {
				for _, got := range values {
					if strings.EqualFold(got, want) {
						entityFound++
						break
					}
				}
			}
		}
		if g.Action == tc.Action {
			actionOK++
		}
		if tc.Scope != "" && g.Scope.Kind == tc.Scope {
			scopeOK++
		}
		if tc.Ambiguity != "" {
			for _, ambiguity := range g.Ambiguities {
				if ambiguity.Kind == tc.Ambiguity {
					ambiguityOK++
					break
				}
			}
		}
		if data, err := json.Marshal(g); err == nil {
			groundingBytes += len(data)
		}
	}

	var baselineComplete, groundedComplete, falseCompletionBase, falseCompletionGrounded int
	var negativeBase, negativeGrounded, negativeTotal int
	var unsupportedBase, unsupportedGrounded, unsupportedTotal int
	var groundedReview int
	badCases := 0
	for _, tc := range behaviors {
		intent := workspace.BuildIntentContract(tc.Request)
		baseline := intent
		baseline.Grounding = nil
		base := agent.EvaluateTaskCompletion(agent.CompletionInput{
			Turns: tc.Turns, Contract: baseline,
		})
		grounded := agent.EvaluateTaskCompletion(agent.CompletionInput{
			Turns: tc.Turns, Contract: intent,
		})
		if base.Complete() {
			baselineComplete++
		}
		if grounded.Complete() {
			groundedComplete++
		}
		if !tc.WantGrounded {
			badCases++
			if base.Complete() {
				falseCompletionBase++
			}
			if grounded.Complete() {
				falseCompletionGrounded++
			}
			if !grounded.Complete() {
				groundedReview++
			}
		}
		if tc.Negative && !tc.WantGrounded {
			negativeTotal++
			if base.Complete() {
				negativeBase++
			}
			if grounded.Complete() {
				negativeGrounded++
			}
		}
	}

	// Semantic admission: an invented mutation must be refused when grounding
	// is installed, and admitted when it is not. This is measured separately
	// because it happens before completion.
	writeTool := benchTool{name: "write", tier: agent.TierWrite}
	for _, tc := range extraction {
		if tc.Action == contract.GroundingMutate {
			continue
		}
		intent := workspace.BuildIntentContract(tc.Request)
		call := ai.ToolCall{ID: "bench", Name: "write", Arguments: map[string]any{"path": "README.md"}}
		proposal := agent.ProposalForCall(writeTool, call.Arguments)
		unsupportedTotal++
		if agent.ValidateProposal(nil, proposal).Verdict != contract.GroundingReject {
			unsupportedBase++
		}
		if agent.ValidateProposal(intent.Grounding, proposal).Verdict != contract.GroundingReject {
			unsupportedGrounded++
		}
	}

	// Delegation under an actionless request is bounded observation: the loop
	// forces the child read-only. The loop test covers that path; the
	// deterministic validator does not need a separate metric here.

	report := struct {
		Extraction  []extractionCase
		Behaviors   []behaviorCase
		Metrics     map[string]string
		TokenBytes  int
		ModelCalls  int
		ToolCalls   int
		GroundedRev int
	}{
		Extraction: extraction, Behaviors: behaviors, TokenBytes: groundingBytes, ModelCalls: 0, ToolCalls: 0,
		GroundedRev: groundedReview,
		Metrics:     map[string]string{},
	}
	report.Metrics["entity_recall"] = percent(entityFound, entityExpected)
	report.Metrics["action_fidelity"] = percent(actionOK, len(extraction))
	report.Metrics["scope_fidelity"] = percent(scopeOK, countScopeCases(extraction))
	report.Metrics["ambiguity_preservation"] = percent(ambiguityOK, countAmbiguityCases(extraction))
	report.Metrics["unsupported_assumption_rate"] = percent(unsupportedAssumptions, assumptionTotal)
	report.Metrics["unsupported_action_rate_baseline"] = percent(unsupportedBase, unsupportedTotal)
	report.Metrics["unsupported_action_rate_grounded"] = percent(unsupportedGrounded, unsupportedTotal)
	report.Metrics["false_completion_rate_baseline"] = percent(falseCompletionBase, badCases)
	report.Metrics["false_completion_rate_grounded"] = percent(falseCompletionGrounded, badCases)
	report.Metrics["false_negative_conclusion_rate_baseline"] = percent(negativeBase, negativeTotal)
	report.Metrics["false_negative_conclusion_rate_grounded"] = percent(negativeGrounded, negativeTotal)
	report.Metrics["clarification_rate_grounded"] = percent(groundedReview, len(behaviors))
	report.Metrics["grounding_tokens_per_request_estimate"] = fmt.Sprintf("%d", estimateTokens(groundingBytes, len(extraction)))
	report.Metrics["tool_call_overhead"] = "0"
	report.Metrics["extra_model_calls"] = "0"

	if *jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(report)
		return
	}
	printReport(report.Extraction, report.Behaviors, report.Metrics, report.TokenBytes, len(extraction), report.GroundedRev)
}

func extractionCases() []extractionCase {
	return []extractionCase{
		{"A", "Fix the authentication bug.", contract.GroundingMutate, []contract.GroundingAction{contract.GroundingMutate}, nil, "", "", "explicit objective"},
		{"B", "Check k8s and opencode as additional projects.", contract.GroundingRead, []contract.GroundingAction{contract.GroundingRead}, []string{"k8s", "opencode"}, contract.ScopeNamed, "", "multiple entities"},
		{"C", "Additional projects are k8s and opencode.", contract.GroundingUnknown, nil, []string{"k8s", "opencode"}, contract.ScopeNamed, "unknown_action", "missing action"},
		{"D", "Investigate the API issue and update the documentation.", contract.GroundingMutate, []contract.GroundingAction{contract.GroundingInvestigate, contract.GroundingMutate}, nil, "", "", "compound request"},
		{"E", "Check the other two repositories too.", contract.GroundingRead, []contract.GroundingAction{contract.GroundingRead}, nil, "", "unresolved_reference", "scope ambiguity"},
		{"F", "Find why auth is failing and fix it.", contract.GroundingMutate, []contract.GroundingAction{contract.GroundingInvestigate, contract.GroundingMutate}, nil, "", "", "mutation after investigation"},
		{"G", "Check whether README.md exists.", contract.GroundingRead, []contract.GroundingAction{contract.GroundingRead}, []string{"README.md"}, contract.ScopeNamed, "", "negative evidence"},
		{"H", "Read the configuration file.", contract.GroundingRead, []contract.GroundingAction{contract.GroundingRead}, nil, "", "", "tool failure"},
		{"I", "Fix the failing tests.", contract.GroundingMutate, []contract.GroundingAction{contract.GroundingMutate}, nil, "", "", "false completion"},
		{"J", "Clean up the login flow.", contract.GroundingMutate, []contract.GroundingAction{contract.GroundingMutate}, nil, "", "", "benign inference"},
		{"K", "The additional projects are k8s and opencode; use them if relevant.", contract.GroundingUnknown, nil, []string{"k8s", "opencode"}, contract.ScopeNamed, "unknown_action", "conditional named context"},
	}
}

func behaviorCases() []behaviorCase {
	rootListing := agent.Turn{Results: []agent.ToolOutcome{{
		ToolName: "ls", Args: map[string]any{"path": "."}, Result: agent.Result{Text: "cmd\ninternal\nmain.go\n"},
	}}}
	docsListing := agent.Turn{Results: []agent.ToolOutcome{{
		ToolName: "ls", Args: map[string]any{"path": "docs"}, Result: agent.Result{Text: "design.md\n"},
	}}}
	fileScan := agent.Turn{Results: []agent.ToolOutcome{{
		ToolName: "glob", Args: map[string]any{"pattern": "**/*"}, Result: agent.Result{Text: "main.go\nnotes.txt\n"},
	}}}
	return []behaviorCase{
		{
			ID: "C-negative", Request: "Additional projects are k8s and opencode.",
			Turns:  []agent.Turn{rootListing, answer("The repository contains no README documentation, so I cannot describe the test-web2 project.")},
			Answer: "negative conclusion", Negative: true, WantGrounded: false,
			Description: "global absence conclusion from a root listing; entities dropped",
		},
		{
			ID: "C-missing-entity", Request: "Additional projects are k8s and opencode.",
			Turns:  []agent.Turn{answer("opencode looks fine.")},
			Answer: "partial entity answer", WantGrounded: false,
			Description: "one named project disappears",
		},
		{
			ID: "C-grounded-ack", Request: "Additional projects are k8s and opencode.",
			Turns:  []agent.Turn{answer("Noted: k8s and opencode are additional projects.")},
			Answer: "grounded acknowledgement", WantGrounded: true,
			Description: "both entities preserved without an invented action",
		},
		{
			ID: "K-conditional", Request: "The additional projects are k8s and opencode; use them if relevant.",
			Turns:  []agent.Turn{answer("Understood; I will use them only if relevant.")},
			Answer: "conditional acknowledgement", WantGrounded: true,
			Description: "relevance remains conditional; no forced mention",
		},
		{
			ID: "B-missing-entity", Request: "Check k8s and opencode as additional projects.",
			Turns:  []agent.Turn{answer("opencode is healthy.")},
			Answer: "partial entity answer", WantGrounded: false,
			Description: "explicit multi-entity check drops one entity",
		},
		{
			ID: "G-unproven", Request: "Check whether README.md exists.",
			Turns:  []agent.Turn{docsListing, answer("README.md does not exist in this repository.")},
			Answer: "global absence from a local listing", Negative: true, WantGrounded: false,
			Description: "a docs listing cannot prove repository-wide absence",
		},
		{
			ID: "G-proven", Request: "Check whether README.md exists.",
			Turns:  []agent.Turn{fileScan, answer("README.md does not exist in this repository.")},
			Answer: "absence from a complete file scan", Negative: true, WantGrounded: true,
			Description: "a complete recursive file scan supports file absence",
		},
		{
			ID: "H-tool-failure", Request: "Check whether config.yaml exists.",
			Turns: []agent.Turn{
				{Results: []agent.ToolOutcome{{
					ToolName: "read", Args: map[string]any{"path": "config.yaml"},
					Result: agent.Result{Text: "no such file", IsError: true},
				}}},
				answer("config.yaml does not exist."),
			},
			Answer: "tool error as absence", Negative: true, WantGrounded: false,
			Description: "a failed read remains a tool error, not proof",
		},
		{
			ID: "E-unresolved", Request: "Check the other two repositories too.",
			Turns:  []agent.Turn{answer("I checked repo-a and repo-b; both are fine.")},
			Answer: "invented scope", WantGrounded: false,
			Description: "the request never named which repositories",
		},
		{
			ID: "J-benign", Request: "Clean up the login flow.",
			Turns:  []agent.Turn{answer("Done.")},
			Answer: "claim without mutation evidence", WantGrounded: false,
			Description: "a reasonable inference still needs observed evidence",
		},
	}
}

func answer(text string) agent.Turn {
	return agent.Turn{Assistant: ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}}
}

func countScopeCases(cases []extractionCase) int {
	total := 0
	for _, tc := range cases {
		if tc.Scope != "" {
			total++
		}
	}
	return total
}

func countAmbiguityCases(cases []extractionCase) int {
	total := 0
	for _, tc := range cases {
		if tc.Ambiguity != "" {
			total++
		}
	}
	return total
}

func percent(found, total int) string {
	if total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d (%.1f%%)", found, total, 100*float64(found)/float64(total))
}

func estimateTokens(bytes, cases int) int {
	if cases == 0 {
		return 0
	}
	return (bytes/cases + 3) / 4
}

func printReport(extraction []extractionCase, behavior []behaviorCase, metrics map[string]string, bytes, cases, reviews int) {
	fmt.Println("# Intent grounding benchmark")
	fmt.Println()
	fmt.Println("Deterministic runtime only: no provider call, no extra model turn.")
	fmt.Println()
	fmt.Println("## Extraction")
	fmt.Println()
	fmt.Println("| ID | Request | Action | Entities | Ambiguity |")
	fmt.Println("|---|---|---|---|---|")
	for _, tc := range extraction {
		intent := workspace.BuildIntentContract(tc.Request)
		g := intent.Grounding
		fmt.Printf("| %s | %s | %s | %s | %s |\n", tc.ID, tc.Request, g.Action,
			strings.Join(g.EntityValues(), ", "), ambiguityNames(g))
	}
	fmt.Println()
	fmt.Println("## Behaviour")
	fmt.Println()
	fmt.Println("| ID | Baseline | Grounded | Meaning |")
	fmt.Println("|---|---|---|---|")
	for _, tc := range behavior {
		intent := workspace.BuildIntentContract(tc.Request)
		baseline := intent
		baseline.Grounding = nil
		base := agent.EvaluateTaskCompletion(agent.CompletionInput{Turns: tc.Turns, Contract: baseline})
		grounded := agent.EvaluateTaskCompletion(agent.CompletionInput{Turns: tc.Turns, Contract: intent})
		fmt.Printf("| %s | %s | %s | %s |\n", tc.ID, state(base), state(grounded), tc.Description)
	}
	fmt.Println()
	fmt.Println("## Metrics")
	fmt.Println()
	for _, key := range []string{
		"entity_recall", "action_fidelity", "scope_fidelity", "ambiguity_preservation",
		"unsupported_assumption_rate",
		"unsupported_action_rate_baseline", "unsupported_action_rate_grounded",
		"false_completion_rate_baseline", "false_completion_rate_grounded",
		"false_negative_conclusion_rate_baseline", "false_negative_conclusion_rate_grounded",
		"clarification_rate_grounded", "grounding_tokens_per_request_estimate",
		"tool_call_overhead", "extra_model_calls",
	} {
		fmt.Printf("- %s: %s\n", key, metrics[key])
	}
	fmt.Println()
	fmt.Printf("Grounding bytes per request: %d (estimated %d tokens); extra model calls: 0; extra tool calls: 0.\n",
		bytes/cases, estimateTokens(bytes, cases))
	fmt.Printf("Grounded review boundaries on the behaviour suite: %d/%d.\n", reviews, len(behavior))
}

func ambiguityNames(g *contract.IntentGrounding) string {
	var out []string
	for _, ambiguity := range g.Ambiguities {
		out = append(out, ambiguity.Kind)
	}
	return strings.Join(out, ", ")
}

func state(completion agent.Completion) string {
	if completion.Complete() {
		return "COMPLETED"
	}
	return "NEEDS_REVIEW"
}

type benchTool struct {
	name    string
	tier    agent.Tier
	classes []agent.ActionClass
}

func (t benchTool) Name() string                   { return t.name }
func (t benchTool) Description() string            { return "benchmark tool" }
func (t benchTool) Parameters() *jsonschema.Schema { return nil }
func (t benchTool) Approval() agent.Approval       { return agent.Approval{Tier: t.tier} }
func (t benchTool) Exclusive(map[string]any) bool  { return false }
func (t benchTool) ActionClasses() []agent.ActionClass {
	return t.classes
}
func (t benchTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "ok"}
}
