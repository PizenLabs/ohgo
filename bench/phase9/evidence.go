package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/prompt"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// The evidence suite measures one property: a correct, evidence-grounded
// outcome for the minimum sufficient work.
//
// Each scenario fixes the authority the request's *effect* requires, the
// actions a competent model takes, and the answer facts the evidence supports.
// The scripted arm then measures what the runtime does with that sequence —
// which tools it offers, whether it withholds a call whose evidence gain is
// zero, and what verdict it reaches. The same tasks run live so the runtime
// property can be separated from one model's behaviour.
//
// The prompts are the contract examples from the runtime requirements, kept
// verbatim so a regression is recognisable without reading this file.

// evidenceMatrix returns the benchmark tasks.
func evidenceMatrix() []scenario {
	workflowQuestion := "I want to ask if this automatically runs and updates on GitHub, or if I have to run it manually."
	workflowAnswer := "The workflow runs automatically: .github/workflows/telemetry.yml schedules it with cron `17 3 * * *` and also allows a manual start through workflow_dispatch. " +
		"The job runs scripts/telemetry.py and finishes with `git push`, so the regenerated telemetry.svg is committed back. " +
		"That is the local configuration; I cannot establish from here whether GitHub actually ran it today."

	return []scenario{
		{
			ID: "A1-github-question", Category: "authority", Fixture: fixtureWorkflow,
			Prompt: workflowQuestion,
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": ".github/workflows/telemetry.yml"}},
				{Tool: "read", Args: map[string]any{"path": "scripts/telemetry.py"}},
				{Text: workflowAnswer},
			},
			Expect: expectation{ReadOnly: true, Completed: true, Authority: workspace.Ask},
			Facts:  []string{"17 3 * * *", "workflow_dispatch", "push"},
		},
		{
			// The same question asked in one clause. Both forms must reach ASK:
			// "automatically" plus "run" is the vocabulary that used to force
			// BUILD even though the user is asking about the behaviour.
			ID: "A2-github-question-compact", Category: "authority", Fixture: fixtureWorkflow,
			Prompt: "Does this automatically update GitHub, or do I have to run it manually?",
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": ".github/workflows/telemetry.yml"}},
				{Text: workflowAnswer},
			},
			Expect: expectation{ReadOnly: true, Completed: true, Authority: workspace.Ask},
			Facts:  []string{"17 3 * * *", "workflow_dispatch"},
		},
		{
			// A model that re-observes a file it already read gains nothing.
			// The runtime must withhold the next exploration instead of paying
			// for it, and the answer must still be complete.
			ID: "A3-wasteful-explorer", Category: "efficiency", Fixture: fixtureWorkflow,
			Prompt: workflowQuestion,
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": ".github/workflows/telemetry.yml"}},
				{Tool: "read", Args: map[string]any{"path": ".github/workflows/telemetry.yml"}},
				{Tool: "read", Args: map[string]any{"path": "scripts/telemetry.py"}},
				{Text: workflowAnswer},
			},
			Expect: expectation{ReadOnly: true, Completed: true, Authority: workspace.Ask, Withheld: true},
			Facts:  []string{"17 3 * * *", "workflow_dispatch"},
		},
		{
			ID: "B-update-repository", Category: "authority", Fixture: fixtureWorkflow,
			Prompt: "Update this repository on GitHub.",
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": "README.md"}},
				{Tool: "write", Args: map[string]any{"path": "README.md", "content": updatedReadme()}},
				{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go build ./..."}},
				{Text: "I updated README.md and verified the module still builds."},
			},
			Expect: expectation{Mutation: true, Verified: true, Authority: workspace.Build},
			Facts:  []string{"README", "build"},
		},
		{
			ID: "C-run-tests", Category: "authority", Fixture: fixtureWorkflow,
			Prompt: "Run the tests and tell me whether they pass.",
			Steps: []step{
				{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go test ./..."}},
				{Text: "The tests pass: `go test ./...` exited successfully for the module."},
			},
			Expect: expectation{ReadOnly: true, Completed: true, Authority: workspace.Build},
			Facts:  []string{"pass"},
		},
		{
			ID: "D-investigate-failure", Category: "authority", Fixture: fixtureWorkflowBug,
			Prompt: "Investigate why this test is failing.",
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": "calc.go"}},
				{Tool: "read", Args: map[string]any{"path": "calc_test.go"}},
				{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go test ./..."}},
				{Text: "TestAdd fails because Add returns a - b instead of a + b in calc.go. I did not change the repository."},
			},
			Expect: expectation{ReadOnly: true, Authority: workspace.Fuzz},
			Facts:  []string{"Add", "a - b", "TestAdd"},
		},
		{
			ID: "E-implement-fix", Category: "authority", Fixture: fixtureWorkflowBug,
			Prompt: "Implement the fix for this failing test.",
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": "calc.go"}},
				{Tool: "read", Args: map[string]any{"path": "calc_test.go"}},
				{Tool: "edit", Args: map[string]any{"path": "calc.go", "old_string": "return a - b", "new_string": "return a + b"}},
				{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go test ./..."}},
				{Text: "Add now returns a + b; the failing test passes and the suite is green."},
			},
			Expect: expectation{Mutation: true, Verified: true, Completed: true, Authority: workspace.Build},
			Facts:  []string{"a + b", "test"},
		},
	}
}

// updatedReadme is the content the mutation scenario writes.
func updatedReadme() string {
	return `# Telemetry

A small Go module with a scheduled telemetry refresh.

The chart is regenerated by ` + "`scripts/telemetry.py`" + ` and committed by
` + "`.github/workflows/telemetry.yml`" + `.

Last reviewed by hand.
`
}

// runEvidenceScenario executes one evidence task and records what the runtime
// did. The state projection is always on: the evidence lifecycle is the subject
// of the measurement, so it must not be an optional arm.
func runEvidenceScenario(ctx context.Context, sc scenario) (metrics runMetrics, err error) {
	routed := workspace.Classify(sc.Prompt).Handler()
	route := workspace.RouteTurn(routed, sc.Prompt)
	askRoute := workspace.RouteTurn(workspace.Ask, sc.Prompt)

	metrics = runMetrics{
		Scenario:           sc.ID,
		Prompt:             sc.Prompt,
		Model:              "scripted",
		Provider:           "none",
		ModelSettings:      "fixed-sequence",
		RuntimeMode:        "evidence-state",
		Authority:          routed.String(),
		RoutedAuthority:    routed.String(),
		AskRoute:           routeOutcome(askRoute),
		TaskHash:           scenarioHash(sc),
		Repository:         "fixture:" + string(sc.Fixture),
		RepositoryRevision: "evidence-fixture-v1",
		StartedAt:          time.Now().UTC(),
	}
	defer func() { metrics.FinishedAt = time.Now().UTC() }()

	if !route.Allowed() {
		metrics.ObjectiveStatus = "FAILED"
		metrics.CompletionReason = route.Message()
		metrics.VerificationStatus = "not_applicable"
		return metrics, nil
	}

	root, err := createFixture(sc.Fixture)
	if err != nil {
		return metrics, err
	}
	defer func() { _ = os.RemoveAll(root) }()

	model := newScriptedModel(sc.Steps...)
	contract := workspace.BuildIntentContract(metrics.Prompt)
	toolSet := builtTools(root)
	metrics.ToolSetHash = toolSetHash(toolSet)
	resolver := structuralResolver(toolSet)
	var snapshot *agent.TargetSnapshot
	if contract.Kind == workspace.ObjectiveMutation {
		snapshot = agent.CaptureTargetsContext(ctx, root, contract, resolver)
	}

	facts := cognition.ProbeFacts(root)

	// The gate enforces the routed authority. A read-only question that reaches
	// for a write is refused here, exactly as in a production session.
	gate := newBenchmarkGate(root, routed)
	loop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    model.stream,
		System:    aiSystemBlock(prompt.Build(root, prompt.WorkspaceContract(routed.String())+"\n\n"+prompt.ProjectContext(root))),
		Tools:     toolSet,
		MaxTurns:  30,
		Authorize: gate,
	})
	if err != nil {
		return metrics, err
	}
	shape := cognition.BuildShape(contract, cognition.ShapeParams{
		Authority: routed.String(),
		Facts:     facts,
		Snapshot:  snapshot,
		Budget:    cognition.BudgetShapeFrom(loop.Budget().Snapshot()),
	})
	// The request's semantic contract is installed for this run so the runtime's
	// own admission rules are exercised, not just the security gate.
	loop.SetGrounding(contract.Grounding)

	evidence := cognition.NewEvidenceState()
	projector := cognition.NewProjector(shape, evidence, facts)
	loop.SetProjectionHook(projector.Project)
	var reports []agent.ProjectionReport
	loop.SetProjectionReportHook(func(report agent.ProjectionReport) {
		reports = append(reports, report)
	})

	started := time.Now()
	turns, runErr := loop.Run(ctx, []agent.Message{agent.UserMessage{Text: metrics.Prompt}})
	metrics.WallMillis = time.Since(started).Milliseconds()
	metrics.ProjectionReports = reports

	requests := model.snapshotRequests()
	perRequest, totalBytes, repeated, newBytes, toolSchema, totalTokens := measureRequests(requests)
	aggregateRequestMetrics(&metrics, perRequest, totalBytes, repeated, newBytes, toolSchema, totalTokens)
	summarizeTurns(&metrics, turns)
	metrics.Budget = fmt.Sprintf("maxTurns=%d", loop.Budget().Snapshot().Limits.Turns)
	scoreAnswer(&metrics, sc.Facts)

	review := ""
	if n := len(reports); n > 0 {
		review = reports[n-1].EvidenceReview()
	}
	metrics.EvidenceReview = review

	if runErr != nil {
		metrics.ObjectiveStatus = "FAILED"
		metrics.CompletionReason = runErr.Error()
		metrics.VerificationStatus = "not_applicable"
		return metrics, nil
	}
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:          turns,
		Contract:       contract,
		Snapshot:       snapshot,
		Resolver:       resolver,
		Root:           root,
		EvidenceReview: review,
	})
	metrics.CompletionReason = completion.Reason
	if completion.Complete() {
		metrics.ObjectiveStatus = "COMPLETED"
	} else {
		metrics.ObjectiveStatus = "NEEDS_REVIEW"
	}
	metrics.Mutated, metrics.Verified = agent.CompletionEvidence(turns, contract.Kind == workspace.ObjectiveMutation)
	metrics.MutatedPaths = agent.ObservedMutationPaths(root, turns, snapshot)
	metrics.Unexpected = agent.UnexpectedMutations(root, turns, contract, snapshot)
	if encoded, err := json.Marshal(evidence); err == nil {
		metrics.EvidenceBytes = len(encoded)
		metrics.EvidenceItems = len(evidence.Items)
	}
	metrics.VerificationStatus = "not_applicable"
	if contract.Kind == workspace.ObjectiveMutation {
		if metrics.Verified {
			metrics.VerificationStatus = "verified"
		} else {
			metrics.VerificationStatus = "not_verified"
		}
	}
	return metrics, nil
}

// routeOutcome renders a route as a stable, comparable token.
func routeOutcome(route workspace.Route) string {
	if route.Allowed() {
		return "allow"
	}
	return "require:" + route.Required.String()
}

// printEvidenceTable renders the evidence suite in a form a reader can scan
// without the JSON artifact.
func printEvidenceTable(results []runMetrics) {
	fmt.Printf("%-26s %-7s %-14s %-14s %5s %4s %8s %7s %7s\n",
		"TASK", "AUTH", "ASK-ROUTE", "STATUS", "CALLS", "DUP", "WITHHELD", "TOKENS", "FACTS")
	for _, result := range results {
		facts := fmt.Sprintf("%d/%d", result.FactsMatched, result.FactsExpected)
		fmt.Printf("%-26s %-7s %-14s %-14s %5d %4d %8d %7d %7s\n",
			result.Scenario,
			result.RoutedAuthority,
			result.AskRoute,
			result.ObjectiveStatus,
			result.ToolCallCount,
			result.DuplicateReads,
			result.CandidateRejections,
			result.InputTokens,
			facts,
		)
		if result.CompletionReason != "" {
			fmt.Printf("  reason: %s\n", truncateLine(result.CompletionReason, 160))
		}
		if len(result.FactsMissing) > 0 {
			fmt.Printf("  answer facts missing: %s\n", strings.Join(result.FactsMissing, ", "))
		}
	}
}

// evidenceMet reports whether one result satisfies the evidence contract.
func evidenceMet(expect expectation, metrics runMetrics) bool {
	if expect.Authority != "" && metrics.RoutedAuthority != expect.Authority.String() {
		return false
	}
	if expect.Authority == workspace.Ask && metrics.AskRoute != "allow" {
		return false
	}
	if expect.Withheld && metrics.CandidateRejections == 0 {
		return false
	}
	if metrics.FactsMatched < metrics.FactsExpected {
		return false
	}
	return true
}
