package cognition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// writeRepo creates a deterministic repository fixture.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// executeTool runs a real capability and converts the result into the same
// outcome the loop records.
func executeTool(t *testing.T, tool agent.Tool, id string, args map[string]any) agent.ToolOutcome {
	t.Helper()
	result := tool.Execute(agent.Context{Ctx: context.Background()}, args)
	return agent.ToolOutcome{
		CallID: id, ToolName: tool.Name(), Args: args, Result: result,
	}
}

// pipelineResult is the complete deterministic path from an intent to a
// completion verdict.
type pipelineResult struct {
	Intent    contract.IntentContract
	Shape     TaskShape
	State     *EvidenceState
	Projected agent.ProjectionResult
	Review    agent.Completion
	System    string
}

// runPipeline builds the contract and shape the host builds, feeds the real
// tool outcomes through the evidence projector, and evaluates a fixture answer.
func runPipeline(t *testing.T, root, prompt, authority string, facts RepositoryFacts, outcomes []agent.ToolOutcome, answer string) pipelineResult {
	t.Helper()
	intent := workspace.BuildIntentContract(prompt)
	shape := BuildShape(intent, ShapeParams{Authority: authority, Facts: facts})
	state := NewEvidenceState()
	projector := NewProjector(shape, state, facts)
	turns := []agent.Turn{{Results: outcomes}}
	projected := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: prompt}},
		Turns:    turns,
		Tools: []agent.Tool{
			testTool{name: "read", tier: agent.TierRead},
			testTool{name: "structure", tier: agent.TierRead},
		},
	})
	turns = append(turns, agent.Turn{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: answer}},
	}})
	review := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:          turns,
		Contract:       intent,
		EvidenceReview: projected.Report.EvidenceReview(),
		Provenance:     projected.Report.Provenance,
		Root:           root,
	})
	var system strings.Builder
	for _, block := range projected.System {
		system.WriteString(block.Text)
	}
	return pipelineResult{
		Intent: intent, Shape: shape, State: state,
		Projected: projected, Review: review, System: system.String(),
	}
}

// Scenario A: a project overview can use documentation and inventory, with a
// targeted source observation for the implementation sentence, and does not
// scan the whole repository.
func TestE2EScenarioAProjectOverview(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"README.md":                           "# Demo\n\nA Go backend and React frontend with a product API.\n",
		"package.json":                        `{"dependencies":{"react":"18"}}`,
		"backend/go.mod":                      "module example.com/demo\n\ngo 1.23\n",
		"backend/docker-compose.yml":          "services:\n  product-service:\n    build: .\n",
		"backend/cmd/product-service/main.go": "package main\n\nfunc main() { http.ListenAndServe(\":8081\", nil) }\n",
		"src/App.tsx":                         "export default function App() { return null }\n",
	})
	structure := tools.NewStructureTool(tools.NewStructuralSource(root))
	read := tools.NewReadTool(root)
	outcomes := []agent.ToolOutcome{
		executeTool(t, structure, "structure-1", map[string]any{"view": "overview"}),
		executeTool(t, read, "read-readme", map[string]any{"path": "README.md"}),
		executeTool(t, read, "read-package", map[string]any{"path": "package.json"}),
		executeTool(t, read, "read-main", map[string]any{"path": "backend/cmd/product-service/main.go"}),
	}
	answer := strings.Join([]string{
		"According to the README, the project is a Go backend with a React frontend.",
		"The current inventory contains backend/ and src/ with a product-service entrypoint.",
		"The inspected backend/cmd/product-service/main.go implements an HTTP handler that starts a listener.",
	}, "\n\n")
	result := runPipeline(t, root,
		"check this project and analyze what this project is about?", "ask",
		RepositoryFacts{Go: true, TypeScript: true, JavaScript: true}, outcomes, answer)
	if result.Intent.Kind != contract.ObjectiveInformational {
		t.Fatalf("intent = %s, want informational", result.Intent.Kind)
	}
	if !result.Review.Complete() {
		t.Fatalf("scenario A did not complete: %s\nstate:\n%s", result.Review.Reason, result.System)
	}
	if !strings.Contains(result.System, "implementation=OBSERVED") {
		t.Fatalf("state block did not record implementation content:\n%s", result.System)
	}
	if strings.Contains(result.System, "implementation=OBSERVED[") == false &&
		!strings.Contains(result.System, "implementation=OBSERVED") {
		t.Fatalf("state block lost implementation provenance:\n%s", result.System)
	}
}

// Scenario B: targeted source acquisition, with no component leak and no
// invented status for the uninspected component.
func TestE2EScenarioBComponentAnalysis(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"backend/internal/product/handler/http.go": "package handler\n\nfunc Create(w http.ResponseWriter, r *http.Request) {}\n",
		"backend/cmd/api-gateway/main.go":          "package main\n\nfunc main() { http.HandleFunc(\"/healthz\", health) }\n",
		"backend/internal/cart/service.go":         "package cart\n\nfunc Health() {}\n",
		"backend/internal/order/service.go":        "package order\n\nfunc Checkout() {}\n",
	})
	read := tools.NewReadTool(root)
	outcomes := []agent.ToolOutcome{
		executeTool(t, read, "read-product", map[string]any{"path": "backend/internal/product/handler/http.go"}),
		executeTool(t, read, "read-gateway", map[string]any{"path": "backend/cmd/api-gateway/main.go"}),
	}
	answer := strings.Join([]string{
		"The inspected product-service handler implements product CRUD endpoints.",
		"The inspected API gateway entrypoint exposes only health handlers.",
		"Cart service is fully implemented.",
		"I have not verified whether delivery-service is implemented.",
	}, "\n\n")
	result := runPipeline(t, root, "check this project", "ask",
		RepositoryFacts{Go: true}, outcomes, answer)
	if result.Review.Complete() {
		t.Fatal("scenario B completed with an unsupported cart claim")
	}
	reason := strings.ToLower(result.Review.Reason)
	if !strings.Contains(reason, "cart") {
		t.Fatalf("review = %q, want the uninspected component", result.Review.Reason)
	}
	if strings.Contains(reason, "delivery") {
		t.Fatalf("bounded delivery statement was reviewed: %q", result.Review.Reason)
	}
}

// Scenario C: an incomplete scope cannot prove repository-wide test absence; a
// complete current inventory can support the exact bounded claim.
func TestE2EScenarioCTestAbsence(t *testing.T) {
	partialRoot := writeRepo(t, map[string]string{
		"README.md":     "# Demo\n\nA Go service.\n",
		"main.go":       "package main\n\nfunc main() {}\n",
		".gitignore":    "legacy/\n",
		"legacy/old.go": "package legacy\n\nfunc Old() {}\n",
	})
	structure := tools.NewStructureTool(tools.NewStructuralSource(partialRoot))
	read := tools.NewReadTool(partialRoot)
	partialOutcomes := []agent.ToolOutcome{
		executeTool(t, read, "read-readme", map[string]any{"path": "README.md"}),
		executeTool(t, structure, "structure-tests", map[string]any{"view": "tests"}),
	}
	partialAnswer := "No Go test files exist in the repository."
	partial := runPipeline(t, partialRoot, "check this project", "ask",
		RepositoryFacts{Go: true}, partialOutcomes, partialAnswer)
	if partial.Review.Complete() {
		t.Fatal("scenario C accepted repository-wide absence from a partial scope")
	}
	bounded := runPipeline(t, partialRoot, "check this project", "ask",
		RepositoryFacts{Go: true}, partialOutcomes,
		"No Go test files were found in the inspected scope.")
	if !bounded.Review.Complete() {
		t.Fatalf("bounded absence statement was reviewed: %s", bounded.Review.Reason)
	}

	completeRoot := writeRepo(t, map[string]string{
		"README.md": "# Demo\n\nA Go service.\n",
		"main.go":   "package main\n\nfunc main() {}\n",
	})
	completeStructure := tools.NewStructureTool(tools.NewStructuralSource(completeRoot))
	completeRead := tools.NewReadTool(completeRoot)
	completeOutcomes := []agent.ToolOutcome{
		executeTool(t, completeRead, "read-readme", map[string]any{"path": "README.md"}),
		executeTool(t, completeStructure, "structure-tests", map[string]any{"view": "tests"}),
	}
	complete := runPipeline(t, completeRoot, "check this project", "ask",
		RepositoryFacts{Go: true}, completeOutcomes,
		"No Go test files are present.")
	if !complete.Review.Complete() {
		t.Fatalf("complete current inventory did not support absence: %s\nstate:\n%s",
			complete.Review.Reason, complete.System)
	}
}

// Scenario D: DECLARED, IMPORTED, and USED_IN_SOURCE stay distinct and are
// bound to the component that owns the evidence.
func TestE2EScenarioDDependencyUsage(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"backend/go.mod": "module example.com/demo\n\nrequire github.com/redis/go-redis/v9 v9.5.0\n",
		"backend/internal/product/cache.go": strings.Join([]string{
			"package cache",
			"import \"github.com/redis/go-redis/v9\"",
			"func New() { rdb := redis.NewClient(&redis.Options{}) }",
			"func Get(k string) { rdb.Get(ctx, k) }",
		}, "\n"),
		"backend/internal/order/service.go": "package order\n\nfunc Checkout() {}\n",
	})
	read := tools.NewReadTool(root)
	outcomes := []agent.ToolOutcome{
		executeTool(t, read, "read-module", map[string]any{"path": "backend/go.mod"}),
		executeTool(t, read, "read-product-cache", map[string]any{"path": "backend/internal/product/cache.go"}),
		executeTool(t, read, "read-order", map[string]any{"path": "backend/internal/order/service.go"}),
	}
	accepted := "The backend declares Redis as a dependency.\n\nProduct-service uses Redis for caching."
	result := runPipeline(t, root, "check this project", "ask",
		RepositoryFacts{Go: true}, outcomes, accepted)
	if !result.Review.Complete() {
		t.Fatalf("declared and used claims were reviewed: %s\nstate:\n%s", result.Review.Reason, result.System)
	}
	leaked := runPipeline(t, root, "check this project", "ask",
		RepositoryFacts{Go: true}, outcomes, "Order service uses Redis.")
	if leaked.Review.Complete() {
		t.Fatal("product dependency evidence supported an order-service claim")
	}
}

// Scenario E: configuration, source behavior, and runtime observation are
// different evidence classes and must be stated separately.
func TestE2EScenarioEConfigurationVersusRuntime(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"backend/docker-compose.yml":          "services:\n  cart-service:\n    ports:\n      - \"8082:8080\"\n",
		"backend/cmd/product-service/main.go": "package main\n\nfunc main() { http.ListenAndServe(\":8081\", nil) }\n",
	})
	read := tools.NewReadTool(root)
	runtimeOutcome := agent.ToolOutcome{
		CallID: "verify-cart", ToolName: "verify",
		Args: map[string]any{"service": map[string]any{"command": "go run ./backend/cmd/cart-service"}},
		Result: agent.Result{
			Text: "runtime ready",
			Runtime: &agent.RuntimeEvidence{
				Command: "go run ./backend/cmd/cart-service",
				URL:     "http://localhost:8082",
				Checks:  1,
			},
		},
	}
	outcomes := []agent.ToolOutcome{
		executeTool(t, read, "read-compose", map[string]any{"path": "backend/docker-compose.yml"}),
		executeTool(t, read, "read-product-main", map[string]any{"path": "backend/cmd/product-service/main.go"}),
		runtimeOutcome,
	}
	accepted := strings.Join([]string{
		"Compose maps cart-service to host port 8082.",
		"The inspected product-service source listens on :8081.",
		"Runtime observation confirmed that cart-service listens on :8082.",
	}, "\n\n")
	result := runPipeline(t, root, "check this project", "ask",
		RepositoryFacts{Go: true}, outcomes, accepted)
	if !result.Review.Complete() {
		t.Fatalf("separated configuration/source/runtime claims were reviewed: %s\nstate:\n%s",
			result.Review.Reason, result.System)
	}
	configAsRuntime := runPipeline(t, root, "check this project", "ask",
		RepositoryFacts{Go: true}, outcomes[:1], "The cart service listens on :8082.")
	if configAsRuntime.Review.Complete() {
		t.Fatal("configuration was accepted as runtime behavior")
	}
}

// Scenario F: an informational request stays read-only, no action is invented,
// and an optional recommendation does not change the objective.
func TestE2EScenarioFInformationalObjective(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"README.md":                "# Demo\n\nA small Go service.\n",
		"backend/go.mod":           "module example.com/demo\n\ngo 1.23\n",
		"backend/cmd/demo/main.go": "package main\n\nfunc main() { http.ListenAndServe(\":8080\", nil) }\n",
	})
	read := tools.NewReadTool(root)
	outcomes := []agent.ToolOutcome{
		executeTool(t, read, "read-readme", map[string]any{"path": "README.md"}),
		executeTool(t, read, "read-main", map[string]any{"path": "backend/cmd/demo/main.go"}),
	}
	answer := strings.Join([]string{
		"According to the README, this is a small Go service.",
		"The inspected backend/cmd/demo/main.go starts an HTTP listener.",
		"A useful next step would be to add tests and a CI check.",
	}, "\n\n")
	result := runPipeline(t, root,
		"check this project and analyze what this project is about?", "ask",
		RepositoryFacts{Go: true}, outcomes, answer)
	if result.Shape.Mutation.Allowed || !result.Shape.Authority.ReadOnly {
		t.Fatalf("informational shape allowed mutation: %+v", result.Shape.Authority)
	}
	if result.Shape.Authority.ExecutionAllowed {
		t.Fatal("ASK shape allowed execution")
	}
	if !result.Review.Complete() {
		t.Fatalf("informational scenario did not complete: %s\nstate:\n%s", result.Review.Reason, result.System)
	}
	for _, outcome := range outcomes {
		if outcome.Result.Mutates {
			t.Fatal("an informational scenario mutated the repository")
		}
	}
	if strings.Contains(strings.ToLower(result.Review.Reason), "objective") {
		t.Fatalf("recommendation altered objective grounding: %s", result.Review.Reason)
	}
}
