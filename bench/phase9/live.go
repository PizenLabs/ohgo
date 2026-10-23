package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/cognition"
	"github.com/PizenLabs/ohgo/prompt"
	"github.com/PizenLabs/ohgo/workspace"
)

// liveModel is the provider transport plus the model reference a live run
// uses. It is a seam so a test can substitute a deterministic transport while
// exercising the same live measurements.
type liveModel struct {
	model  ai.Model
	stream ai.StreamFn
	ref    string
}

// resolveLive resolves a provider/model reference from the static catalog.
//
// Dynamic discovery is deliberately not used here: a benchmark must run
// against a pinned model identity, not whatever the provider listed today.
func resolveLive(ref string) (liveModel, error) {
	model, err := catalog.Resolve(ref)
	if err != nil {
		return liveModel{}, err
	}
	registry := ai.NewRegistry()
	return liveModel{model: model, stream: registry.StreamFn(), ref: ref}, nil
}

// runLiveScenario executes one matrix prompt with a real model.
//
// This is the model-behaviour half of the benchmark. The scripted path answers
// "what does the runtime cost for a fixed action sequence?"; this path answers
// "what does this model actually do under the same runtime?". Both use the same
// fixture, contract, authority, tools, and budget, so a difference is
// attributable to the model or the runtime configuration, never to the task.
func runLiveScenario(ctx context.Context, sc scenario, live liveModel, projection string) (runMetrics, error) {
	metrics := runMetrics{Scenario: sc.ID + "@" + live.ref, Prompt: sc.Prompt}
	root, err := createFixture(sc.Fixture)
	if err != nil {
		return metrics, err
	}
	defer func() { _ = os.RemoveAll(root) }()

	var server *httptest.Server
	promptText := sc.Prompt
	if sc.Expect.External {
		server = externalServer()
		defer server.Close()
		promptText = strings.ReplaceAll(promptText, "{{EXTERNAL_URL}}", server.URL)
	}
	metrics.Prompt = promptText

	contract := workspace.BuildIntentContract(promptText)
	toolSet := builtTools(root)
	resolver := structuralResolver(toolSet)
	var snapshot *agent.TargetSnapshot
	if contract.Kind == workspace.ObjectiveMutation {
		snapshot = agent.CaptureTargetsContext(ctx, root, contract, resolver)
	}

	system := aiSystemBlock(prompt.Build(root,
		prompt.WorkspaceContract("build")+"\n\n"+prompt.ProjectContext(root)))

	var requests []ai.Request
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		requests = append(requests, req)
		return live.stream(ctx, req)
	}
	loop, err := agent.New(agent.Config{
		Model:    live.model,
		Stream:   stream,
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
	turns, runErr := loop.Run(ctx, []agent.Message{agent.UserMessage{Text: promptText}})
	metrics.WallMillis = time.Since(started).Milliseconds()
	perRequest, totalBytes, repeated, newBytes, toolSchema, totalTokens := measureRequests(requests)
	aggregateRequestMetrics(&metrics, perRequest, totalBytes, repeated, newBytes, toolSchema, totalTokens)
	summarizeTurns(&metrics, turns)
	for _, turn := range turns {
		metrics.ProviderInputTokens += turn.Assistant.Usage.Input + turn.Assistant.Usage.CacheRead
		metrics.ProviderOutputTokens += turn.Assistant.Usage.Output
		metrics.ProviderCacheRead += turn.Assistant.Usage.CacheRead
		metrics.ProviderCacheWrite += turn.Assistant.Usage.CacheWrite
	}
	if runErr != nil {
		metrics.ObjectiveStatus = "FAILED"
		metrics.CompletionReason = runErr.Error()
		metrics.VerificationStatus = "not_applicable"
		return metrics, nil
	}
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    turns,
		Contract: contract,
		Snapshot: snapshot,
		Resolver: resolver,
		Root:     root,
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
	if contract.Kind == workspace.ObjectiveMutation {
		if verified {
			metrics.VerificationStatus = "verified"
		} else {
			metrics.VerificationStatus = "not_verified"
		}
	} else {
		metrics.VerificationStatus = "not_applicable"
	}
	if evidence != nil {
		encoded, _ := json.Marshal(evidence)
		metrics.EvidenceBytes = len(encoded)
		metrics.EvidenceItems = len(evidence.Items)
	}
	return metrics, nil
}

// liveSummary renders one line for a live run. It is used by the CLI so a
// provider comparison does not require reading a JSON file.
func liveSummary(metrics runMetrics) string {
	return fmt.Sprintf("%-36s %-13s turns=%d tools=%d in=%d out=%d cache_read=%d wall=%dms",
		metrics.Scenario, metrics.ObjectiveStatus, metrics.TurnCount, metrics.ToolCallCount,
		metrics.ProviderInputTokens, metrics.ProviderOutputTokens, metrics.ProviderCacheRead,
		metrics.WallMillis)
}
