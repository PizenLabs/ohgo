package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/cognition"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/prompt"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
)

// expectation is the externally observable contract a scenario must satisfy.
type expectation struct {
	// Mutation reports whether the prompt asks for a repository change.
	Mutation bool
	// ReadOnly reports that no repository mutation is permitted.
	ReadOnly bool
	// Completed reports the expected runtime verdict.
	Completed bool
	// Verified reports that a successful verification must follow the change.
	Verified bool
	// External reports that external knowledge is required.
	External bool
	// AllowUnexpected marks an adversarial scenario whose contract is to
	// produce an out-of-scope mutation and have the runtime report it.
	AllowUnexpected bool
}

// scenario is one controlled benchmark task.
type scenario struct {
	ID       string
	Category string
	Prompt   string
	Fixture  fixtureKind
	Steps    []step
	Expect   expectation
	// Facts are observable answer requirements. They are a bounded
	// correctness signal, not a semantic judge: a final answer that never
	// mentions the project's name or entry point is unlikely to explain it.
	Facts []string
}

// matrix returns the Phase 9 A-J matrix plus adversarial controls that are
// deterministic enough to run on every build.
func matrix() []scenario {
	readSteps := []step{
		{Tool: "read", Args: map[string]any{"path": "README.md"}},
		{Tool: "ls", Args: map[string]any{"path": "."}},
		{Tool: "read", Args: map[string]any{"path": "index.html"}},
		{Tool: "read", Args: map[string]any{"path": "style.css"}},
		{Tool: "read", Args: map[string]any{"path": "script.js"}},
		{Tool: "read", Args: map[string]any{"path": "tui/approval.go"}},
	}
	buildSteps := append(append([]step(nil), readSteps...),
		step{Tool: "write", Args: map[string]any{"path": "README.md", "content": readmeContent()}},
		step{Tool: "read", Args: map[string]any{"path": "README.md"}},
		step{Tool: "ls", Args: map[string]any{"path": "."}},
		step{Text: "Nimbus Notes is a static, offline-first notebook with a small Go terminal component. I wrote README.md and verified it by reading it back."},
	)
	readFinal := step{Text: "Nimbus Notes is a static, offline-first notebook. index.html is the entry point, style.css owns the visual system, script.js owns note persistence, and tui/approval.go is a small Go component. There is no README yet."}

	debugSteps := []step{
		{Tool: "read", Args: map[string]any{"path": "calc.go"}},
		{Tool: "read", Args: map[string]any{"path": "calc_test.go"}},
		{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go test ./..."}},
		{Tool: "edit", Args: map[string]any{"path": "calc.go", "old_string": "return a - b", "new_string": "return a + b"}},
		{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go test ./..."}},
		{Text: "The defect was an inverted subtraction in Add; I fixed it and the tests now pass."},
	}

	refactorSteps := []step{
		{Tool: "read", Args: map[string]any{"path": "shape.go"}},
		{Tool: "read", Args: map[string]any{"path": "extra.go"}},
		{Tool: "read", Args: map[string]any{"path": "shape_test.go"}},
		{Tool: "read", Args: map[string]any{"path": "extra_test.go"}},
		{Tool: "edit", Args: map[string]any{"path": "shape.go", "old_string": "func Area(width, height int) int {", "new_string": "func RectangleArea(width, height int) int {"}},
		{Tool: "edit", Args: map[string]any{"path": "extra.go", "old_string": "return 2 * Area(width, height)", "new_string": "return 2 * RectangleArea(width, height)"}},
		{Tool: "edit", Args: map[string]any{"path": "shape_test.go", "old_string": "if got := Area(3, 4); got != 12 {", "new_string": "if got := RectangleArea(3, 4); got != 12 {"}},
		{Tool: "edit", Args: map[string]any{"path": "extra_test.go", "old_string": "if got := DoubleArea(3, 4); got != 24 {", "new_string": "if got := 2 * RectangleArea(3, 4); got != 24 {"}},
		{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go test ./..."}},
		{Text: "I renamed Area to RectangleArea across the two source files and their tests, then ran the test suite successfully."},
	}

	fuzzSteps := []step{
		{Tool: "read", Args: map[string]any{"path": "parser.go"}},
		{Tool: "read", Args: map[string]any{"path": "parser_test.go"}},
		{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go test ./..."}},
		{Text: "The parser assumes at least one element and does not validate malformed empty input. I did not modify the repository."},
	}

	externalSteps := []step{
		{Tool: "web_fetch", Args: map[string]any{"url": "{{EXTERNAL_URL}}"}},
		{Tool: "read", Args: map[string]any{"path": "index.html"}},
		{Tool: "write", Args: map[string]any{"path": "README.md", "content": readmeContent() + "\n\n## External reference\n\nThe feature list was checked against the project's published documentation.\n"}},
		{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go build ./..."}},
		{Text: "I wrote README.md using repository evidence and the published documentation, and verified the repository still builds."},
	}

	largeSteps := []step{
		{Tool: "read", Args: map[string]any{"path": "logs/huge.log"}},
		{Tool: "ls", Args: map[string]any{"path": "."}},
		{Tool: "read", Args: map[string]any{"path": "index.html"}},
		{Text: "Nimbus Notes is a static notebook. The large log file is unrelated operational noise."},
	}

	missingSteps := []step{
		{Tool: "read", Args: map[string]any{"path": "MISSING.md"}},
		{Tool: "read", Args: map[string]any{"path": "index.html"}},
		{Text: "MISSING.md does not exist; index.html is the entry point."},
	}

	failedVerifySteps := []step{
		{Tool: "read", Args: map[string]any{"path": "index.html"}},
		{Tool: "write", Args: map[string]any{"path": "README.md", "content": readmeContent()}},
		{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go build ./does-not-exist"}},
		{Text: "I wrote README.md, but the build check failed."},
	}

	unexpectedSteps := []step{
		{Tool: "read", Args: map[string]any{"path": "index.html"}},
		{Tool: "write", Args: map[string]any{"path": "README.md", "content": readmeContent()}},
		{Tool: "write", Args: map[string]any{"path": "extra.txt", "content": "not requested"}},
		{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go build ./..."}},
		{Text: "I wrote README.md and also created extra.txt."},
	}

	partialSteps := []step{
		{Tool: "bash", Args: map[string]any{"command": "sleep 2", "background": true}},
		{Text: "I started the check in the background."},
	}

	postProofSteps := []step{
		{Tool: "read", Args: map[string]any{"path": "index.html"}},
		{Tool: "write", Args: map[string]any{"path": "README.md", "content": readmeContent()}},
		{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go build ./..."}},
		// After the mutation and its verification, this exploration is
		// unnecessary. The task-shaped runtime withholds it and the model must
		// answer instead of continuing to browse.
		{Tool: "ls", Args: map[string]any{"path": "."}},
		{Text: "I wrote README.md and verified the repository builds."},
	}

	return []scenario{
		{
			ID: "A-pure-read", Category: "A_READ", Fixture: fixtureStatic,
			Prompt: "Check this project and explain what it does. Do not modify anything.",
			Steps:  append(append([]step(nil), readSteps...), readFinal),
			Expect: expectation{ReadOnly: true, Completed: true},
			Facts:  []string{"Nimbus Notes", "offline", "index.html", "script.js", "style.css"},
		},
		{
			ID: "B-read-build", Category: "B_READ_BUILD", Fixture: fixtureStatic,
			Prompt: "Check this project and write README.md.",
			Steps:  buildSteps,
			Expect: expectation{Mutation: true, Completed: false},
			Facts:  []string{"README", "Nimbus"},
		},
		{
			ID: "C-explicit-boundary", Category: "C_BOUNDARY", Fixture: fixtureStatic,
			Prompt: "Check this project and write README.md. Do not modify any other file.",
			Steps:  buildSteps,
			Expect: expectation{Mutation: true, ReadOnly: false, Completed: false},
			Facts:  []string{"README", "Nimbus"},
		},
		{
			ID: "D-mutation-after-investigation", Category: "D_INVESTIGATE_THEN_BUILD", Fixture: fixtureStatic,
			Prompt: "Check this project first, then write README.md.",
			Steps:  buildSteps,
			Expect: expectation{Mutation: true, Completed: false},
			Facts:  []string{"README", "Nimbus"},
		},
		{
			ID: "E-embedded-mutation", Category: "E_EMBEDDED", Fixture: fixtureStatic,
			Prompt: "I want to understand this project, and after checking it, create a README.md.",
			Steps:  buildSteps,
			Expect: expectation{Mutation: true, Completed: false},
			Facts:  []string{"README", "Nimbus"},
		},
		{
			ID: "F-negative-control", Category: "F_NEGATIVE", Fixture: fixtureStatic,
			Prompt: "Check this project and tell me whether a README.md should be created. Do not create it.",
			Steps:  append(append([]step(nil), readSteps...), step{Text: "A README should be created, but I did not create it."}),
			Expect: expectation{ReadOnly: true, Completed: true},
			Facts:  []string{"README", "create"},
		},
		{
			ID: "G-debugging", Category: "G_DEBUG", Fixture: fixtureGoDebug,
			Prompt: "The tests fail. Fix the failing tests and verify the fix. Do not change any other file.",
			Steps:  debugSteps,
			Expect: expectation{Mutation: true, Verified: true, Completed: true},
			Facts:  []string{"Add", "subtraction", "test"},
		},
		{
			ID: "H-refactoring", Category: "H_REFACTOR", Fixture: fixtureGoRefactor,
			Prompt: "Refactor shape.go, extra.go, shape_test.go, and extra_test.go: rename the exported geometry helper to a clearer name, update every caller, and run go test ./... Do not modify any other file.",
			Steps:  refactorSteps,
			Expect: expectation{Mutation: true, Verified: true, Completed: true},
			Facts:  []string{"RectangleArea", "rename"},
		},
		{
			ID: "I-fuzzing", Category: "I_FUZZ", Fixture: fixtureGoFuzz,
			Prompt: "Investigate the parser for malformed-input failures. Do not modify anything.",
			Steps:  fuzzSteps,
			Expect: expectation{ReadOnly: true, Completed: false},
			Facts:  []string{"parser", "empty", "panic"},
		},
		{
			ID: "J-external-knowledge", Category: "J_EXTERNAL", Fixture: fixtureStatic,
			Prompt: "Check this project and write README.md. Use the official Nimbus Notes documentation at {{EXTERNAL_URL}} for the feature list. Do not modify any other file.",
			Steps:  externalSteps,
			Expect: expectation{Mutation: true, External: true, Verified: true, Completed: true},
			Facts:  []string{"Nimbus", "offline", "documentation"},
		},
		{
			ID: "K1-irrelevant-large-file", Category: "K_ADVERSARIAL", Fixture: fixtureLarge,
			Prompt: "Check this project and explain what it does. Do not modify anything.",
			Steps:  largeSteps,
			Expect: expectation{ReadOnly: true, Completed: true},
			Facts:  []string{"Nimbus", "offline", "log"},
		},
		{
			ID: "K2-missing-file", Category: "K_ADVERSARIAL", Fixture: fixtureStatic,
			Prompt: "Check this project and explain what it does. Do not modify anything.",
			Steps:  missingSteps,
			// A failed read is a tool error, not proof that the file is absent
			// repository-wide. The explanation may be correct, but the
			// unproven absence claim keeps the task reviewable.
			Expect: expectation{ReadOnly: true, Completed: false},
			Facts:  []string{"MISSING.md", "index.html"},
		},
		{
			ID: "K3-failed-verification", Category: "K_ADVERSARIAL", Fixture: fixtureStatic,
			Prompt: "Check this project and write README.md. Do not modify any other file.",
			Steps:  failedVerifySteps,
			Expect: expectation{Mutation: true, Completed: false},
			Facts:  []string{"README", "build"},
		},
		{
			ID: "K4-unexpected-mutation", Category: "K_ADVERSARIAL", Fixture: fixtureStatic,
			Prompt: "Check this project and write README.md. Do not modify any other file.",
			Steps:  unexpectedSteps,
			Expect: expectation{Mutation: true, Completed: false, AllowUnexpected: true},
			Facts:  []string{"README", "extra.txt"},
		},
		{
			ID: "K5-partial-execution", Category: "K_ADVERSARIAL", Fixture: fixtureStatic,
			Prompt: "Run the check in the background and report what you started.",
			Steps:  partialSteps,
			Expect: expectation{ReadOnly: true, Completed: false},
			Facts:  []string{"background"},
		},
		{
			ID: "K6-large-source-context", Category: "K_ADVERSARIAL", Fixture: fixtureHeavy,
			Prompt: "Check this project and explain what it does. Do not modify anything.",
			Steps:  append(append([]step(nil), readSteps...), readFinal),
			Expect: expectation{ReadOnly: true, Completed: true},
			Facts:  []string{"Nimbus Notes", "offline", "index.html", "script.js", "style.css"},
		},
		{
			ID: "K7-stop-after-proof", Category: "K_ADVERSARIAL", Fixture: fixtureStatic,
			Prompt: "Check the project, write README.md, and verify the repository still builds. Do not modify any other file.",
			Steps:  postProofSteps,
			Expect: expectation{Mutation: true, Verified: true, Completed: true},
			Facts:  []string{"README", "build"},
		},
	}
}

// phase10Matrix adds the Phase 10 mutation-boundary and minimal-exploration
// controls to the frozen Phase 9 matrix. The Phase 9 suite remains unchanged so
// its published measurements stay reproducible.
func phase10Matrix() []scenario {
	out := append([]scenario(nil), matrix()...)
	out = append(out,
		scenario{
			ID: "P10-C1-read-file", Category: "C_BOUNDARY", Fixture: fixtureStatic,
			Prompt: "Read index.html and explain what it does. Do not modify anything.",
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": "index.html"}},
				{Text: "index.html is the Nimbus Notes application shell: it loads style.css and script.js and contains the note form."},
			},
			Expect: expectation{ReadOnly: true, Completed: true},
			Facts:  []string{"index.html", "Nimbus", "note"},
		},
		scenario{
			ID: "P10-C2-read-then-update", Category: "C_BOUNDARY", Fixture: fixtureStatic,
			Prompt: "Read script.js and update the storage key to 'nimbus-notes-v2'. Do not change any other file.",
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": "script.js"}},
				{Tool: "edit", Args: map[string]any{
					"path":       "script.js",
					"old_string": "const STORAGE_KEY = 'nimbus-notes-v1';",
					"new_string": "const STORAGE_KEY = 'nimbus-notes-v2';",
				}},
				{Tool: "bash", Args: map[string]any{"command": "GOCACHE=$PWD/.gocache go build ./..."}},
				{Text: "I changed the storage key in script.js to nimbus-notes-v2 and the repository still builds."},
			},
			Expect: expectation{Mutation: true, Verified: true, Completed: true},
			Facts:  []string{"nimbus-notes-v2"},
		},
		scenario{
			ID: "P10-F-package-name", Category: "F_NEGATIVE", Fixture: fixtureStatic,
			Prompt: "What is the package declared in tui/approval.go? Answer with the package name only.",
			Steps: []step{
				{Tool: "read", Args: map[string]any{"path": "tui/approval.go"}},
				{Text: "tui"},
			},
			Expect: expectation{ReadOnly: true, Completed: true},
			Facts:  []string{"tui"},
		},
	)
	return out
}

// runScenario executes one scenario and returns its measured metrics.
// projection is "off" for the baseline or "state" for the task-shaped layer.
func runScenario(ctx context.Context, sc scenario, projection string) (metrics runMetrics, err error) {
	metrics = runMetrics{
		Scenario:           sc.ID,
		Prompt:             sc.Prompt,
		Model:              "scripted",
		Provider:           "none",
		ModelSettings:      "fixed-sequence",
		RuntimeMode:        "scripted-" + projectionMode(projection),
		Authority:          "build",
		TaskHash:           scenarioHash(sc),
		Repository:         "fixture:" + string(sc.Fixture),
		RepositoryRevision: "phase9-fixture-v1",
		StartedAt:          time.Now().UTC(),
	}
	defer func() { metrics.FinishedAt = time.Now().UTC() }()
	root, err := createFixture(sc.Fixture)
	if err != nil {
		return metrics, err
	}
	defer func() { _ = os.RemoveAll(root) }()

	steps := append([]step(nil), sc.Steps...)
	external := ""
	if sc.Expect.External {
		server := externalServer()
		defer server.Close()
		external = server.URL
	}
	for i := range steps {
		if steps[i].Args == nil {
			continue
		}
		if url, ok := steps[i].Args["url"].(string); ok && url == "{{EXTERNAL_URL}}" {
			steps[i].Args["url"] = external
		}
	}
	if strings.Contains(metrics.Prompt, "{{EXTERNAL_URL}}") {
		metrics.Prompt = strings.ReplaceAll(metrics.Prompt, "{{EXTERNAL_URL}}", external)
	}

	model := newScriptedModel(steps...)
	modelRef := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	contract := workspace.BuildIntentContract(metrics.Prompt)
	toolSet := builtTools(root)
	metrics.ToolSetHash = toolSetHash(toolSet)
	resolver := structuralResolver(toolSet)
	var snapshot *agent.TargetSnapshot
	if contract.Kind == workspace.ObjectiveMutation {
		snapshot = agent.CaptureTargetsContext(ctx, root, contract, resolver)
	}

	system := aiSystemBlock(prompt.Build(root,
		prompt.WorkspaceContract("build")+"\n\n"+prompt.ProjectContext(root)))

	loop, err := agent.New(agent.Config{
		Model:    modelRef,
		Stream:   model.stream,
		System:   system,
		Tools:    toolSet,
		MaxTurns: 30,
	})
	if err != nil {
		return metrics, err
	}

	var evidence *cognition.EvidenceState
	if projection == "state" {
		facts := cognition.ProbeFacts(root)
		authority := "build"
		if contract.Kind == workspace.ObjectiveInformational {
			authority = "ask"
		}
		shape := cognition.BuildShape(contract, cognition.ShapeParams{
			Authority: authority,
			Facts:     facts,
			Snapshot:  snapshot,
			Budget:    cognition.BudgetShapeFrom(loop.Budget().Snapshot()),
		})
		evidence = cognition.NewEvidenceState()
		projector := cognition.NewProjector(shape, evidence, facts)
		loop.SetProjectionHook(projector.Project)
		loop.SetProjectionReportHook(func(report agent.ProjectionReport) {
			metrics.ProjectionReports = append(metrics.ProjectionReports, report)
		})
	}

	started := time.Now()
	turns, runErr := loop.Run(ctx, []agent.Message{agent.UserMessage{Text: metrics.Prompt}})
	metrics.WallMillis = time.Since(started).Milliseconds()

	requests := model.snapshotRequests()
	perRequest, totalBytes, repeated, newBytes, toolSchema, totalTokens := measureRequests(requests)
	aggregateRequestMetrics(&metrics, perRequest, totalBytes, repeated, newBytes, toolSchema, totalTokens)
	summarizeTurns(&metrics, turns)
	scoreAnswer(&metrics, sc.Facts)
	review := ""
	if n := len(metrics.ProjectionReports); n > 0 {
		review = metrics.ProjectionReports[n-1].EvidenceReview()
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
	mutated, verified := agent.CompletionEvidence(turns, contract.Kind == workspace.ObjectiveMutation)
	metrics.Mutated = mutated
	metrics.Verified = verified
	metrics.MutatedPaths = agent.ObservedMutationPaths(root, turns, snapshot)
	metrics.Unexpected = agent.UnexpectedMutations(root, turns, contract, snapshot)
	if evidence != nil {
		encoded, _ := json.Marshal(evidence)
		metrics.EvidenceBytes = len(encoded)
		metrics.EvidenceItems = len(evidence.Items)
	}
	if contract.Kind == workspace.ObjectiveMutation {
		if verified {
			metrics.VerificationStatus = "verified"
		} else {
			metrics.VerificationStatus = "not_verified"
		}
	} else {
		metrics.VerificationStatus = "not_applicable"
	}
	return metrics, nil
}

// aiSystemBlock converts a plain prompt string into one system block.
func aiSystemBlock(blocks []string) []ai.SystemPromptBlock {
	out := make([]ai.SystemPromptBlock, 0, len(blocks))
	for i, text := range blocks {
		out = append(out, ai.SystemPromptBlock{Text: text, Cache: i == len(blocks)-1})
	}
	return out
}

// structuralResolver returns the symbol index the tool set already owns.
func structuralResolver(toolSet []agent.Tool) agent.StructuralResolver {
	for _, tool := range toolSet {
		if symbols, ok := tool.(*tools.SymbolsTool); ok {
			return symbols
		}
	}
	return nil
}

// builtTools is the production-shaped tool set, rooted at the fixture.
func builtTools(root string) []agent.Tool {
	bash := tools.NewBashTool(root)
	structural := tools.NewStructuralSource(root)
	return []agent.Tool{
		tools.NewReadTool(root),
		tools.NewWriteTool(root),
		tools.NewEditTool(root),
		tools.NewDeleteTool(root),
		bash,
		tools.NewWaitTool(bash.Jobs()),
		tools.NewGlobTool(root),
		tools.NewGrepTool(root),
		tools.NewLsTool(root),
		tools.NewSymbolsToolWithSource(structural),
		tools.NewGraphTool(structural),
		tools.NewRefsTool(structural),
		tools.NewWebFetchTool(),
		tools.NewTodoTool(),
	}
}

// externalServer is a local, deterministic stand-in for published
// documentation. The content is untrusted external evidence even though it is
// served from loopback: the trust class comes from the capability, not the host.
func externalServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><body><h1>Nimbus Notes documentation</h1>
<p>Features: offline note capture, browser-local persistence, a responsive reading layout.</p>
<p>No account is required and no note leaves the browser.</p></body></html>`)
	})
	return httptest.NewServer(mux)
}

// readmeContent is the README the scripted model writes. It is long enough to
// make the write result realistic without dominating the fixture.
func readmeContent() string {
	return `# Nimbus Notes

Nimbus Notes is a small, offline-first notebook that runs entirely in the
browser. Notes are stored in localStorage; there is no account and no server.

## Structure

- index.html is the application shell and entry point.
- style.css defines the visual system.
- script.js owns note persistence and rendering.
- tui/approval.go is the small Go component used by the terminal tooling.
- docs/ARCHITECTURE.md records the design in prose.

## Running it

Open index.html in a browser. The Makefile contains the Go checks used by the
terminal tooling.

## Design notes

The application deliberately avoids a backend. The browser is the data store,
so the project has no build step and no runtime dependencies.
`
}
