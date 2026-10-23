package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/budget"
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

// liveOptions bounds one live benchmark run. They are identical for CONTROL
// and COGNITIVE; only the projection differs.
type liveOptions struct {
	MaxTurns    int
	MaxTokens   int
	Temperature float64
	Timeout     time.Duration
	Retries     int
	MinInterval time.Duration
}

// defaultLiveOptions is the benchmark default. The timeout is a resource
// bound, not a model setting: a slow local model must not hang a matrix run.
func defaultLiveOptions() liveOptions {
	return liveOptions{
		MaxTurns:  30,
		MaxTokens: 8192,
		Timeout:   15 * time.Minute,
		Retries:   3,
	}
}

// resolveLive resolves a provider/model reference from the static catalog.
//
// Dynamic discovery is deliberately not used here: a benchmark must run
// against a pinned model identity, not whatever the provider listed today.
// A local Ollama reference is constructed explicitly because the bundled
// catalog intentionally does not pin local tags.
func resolveLive(ref string) (liveModel, error) {
	if strings.HasPrefix(ref, "ollama/") {
		id := strings.TrimSpace(strings.TrimPrefix(ref, "ollama/"))
		if id == "" {
			return liveModel{}, fmt.Errorf("ollama reference %q has no model id", ref)
		}
		base := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
		if base == "" {
			base = ai.OllamaBaseURL
		}
		return liveModel{
			model: ai.Model{
				ID:              id,
				Name:            id,
				Provider:        "ollama",
				API:             ai.APIOllamaChat,
				BaseURL:         base,
				ContextWindow:   32768,
				MaxOutputTokens: 8192,
			},
			stream: ai.NewRegistry().StreamFn(),
			ref:    ref,
		}, nil
	}
	if strings.HasPrefix(ref, "gemini/") {
		id := strings.TrimSpace(strings.TrimPrefix(ref, "gemini/"))
		key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
		if id == "" || key == "" {
			return liveModel{}, fmt.Errorf("gemini reference %q requires GEMINI_API_KEY and a model id", ref)
		}
		registry := ai.NewRegistry()
		registry.SetAPIKey("gemini", key)
		return liveModel{
			model: ai.Model{
				ID:              id,
				Name:            id,
				Provider:        "gemini",
				API:             ai.APIOpenAIChatCompletions,
				BaseURL:         "https://generativelanguage.googleapis.com/v1beta/openai",
				ContextWindow:   1_000_000,
				MaxOutputTokens: 8192,
			},
			stream: registry.StreamFn(),
			ref:    ref,
		}, nil
	}
	if strings.HasPrefix(ref, "openrouter/") {
		// The bundled catalog pins a small OpenRouter subset. A benchmark may
		// name an explicitly pinned free row that is not in that subset; the
		// identity is still fixed in the command line, never discovered.
		model, err := catalog.Resolve(ref)
		if err == nil {
			registry := ai.NewRegistry()
			return liveModel{model: model, stream: registry.StreamFn(), ref: ref}, nil
		}
		id := strings.TrimSpace(strings.TrimPrefix(ref, "openrouter/"))
		key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
		if id == "" || key == "" {
			return liveModel{}, fmt.Errorf("openrouter reference %q requires OPENROUTER_API_KEY and a model id", ref)
		}
		registry := ai.NewRegistry()
		return liveModel{
			model: ai.Model{
				ID:              id,
				Name:            id,
				Provider:        "openrouter",
				API:             ai.APIOpenAIChatCompletions,
				BaseURL:         "https://openrouter.ai/api/v1",
				ContextWindow:   262144,
				MaxOutputTokens: 8192,
			},
			stream: registry.StreamFn(),
			ref:    ref,
		}, nil
	}
	model, err := catalog.Resolve(ref)
	if err != nil {
		return liveModel{}, err
	}
	registry := ai.NewRegistry()
	return liveModel{model: model, stream: registry.StreamFn(), ref: ref}, nil
}

// runLiveScenario executes one matrix prompt with a real model.
func runLiveScenario(ctx context.Context, sc scenario, live liveModel, projection string) (runMetrics, error) {
	return runLiveScenarioWithOptions(ctx, sc, live, projection, defaultLiveOptions())
}

// runLiveScenarioWithOptions executes one matrix prompt with a real model and
// explicit run bounds.
//
// This is the model-behaviour half of the benchmark. The scripted path answers
// "what does the runtime cost for a fixed action sequence?"; this path answers
// "what does this model actually do under the same runtime?". Both use the same
// fixture, contract, authority, tools, and budget within a model condition, so
// a difference is attributable to the model or the runtime configuration,
// never to the task.
func runLiveScenarioWithOptions(ctx context.Context, sc scenario, live liveModel, projection string, options liveOptions) (metrics runMetrics, err error) {
	metrics = runMetrics{
		Scenario:           sc.ID + "@" + live.ref,
		Prompt:             sc.Prompt,
		Model:              live.model.ID,
		Provider:           live.model.Provider,
		ModelSettings:      fmt.Sprintf("maxTokens=%d maxTurns=%d temperature=%g", options.MaxTokens, options.MaxTurns, options.Temperature),
		RuntimeMode:        projectionMode(projection),
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

	var server *httptest.Server
	promptText := sc.Prompt
	if sc.Expect.External {
		server = externalServer()
		defer server.Close()
		promptText = strings.ReplaceAll(promptText, "{{EXTERNAL_URL}}", server.URL)
	}
	metrics.Prompt = promptText

	contract := workspace.BuildIntentContract(promptText)
	route := workspace.RouteTurn(workspace.Build, promptText)
	if !route.Allowed() {
		metrics.ObjectiveStatus = "FAILED"
		metrics.CompletionReason = route.Message()
		metrics.VerificationStatus = "not_applicable"
		return metrics, nil
	}
	metrics.Authority = route.Handler.String()
	toolSet := builtTools(root)
	metrics.ToolSetHash = toolSetHash(toolSet)
	resolver := structuralResolver(toolSet)
	var snapshot *agent.TargetSnapshot
	if contract.Kind == workspace.ObjectiveMutation {
		snapshot = agent.CaptureTargetsContext(ctx, root, contract, resolver)
	}

	system := aiSystemBlock(prompt.Build(root,
		prompt.WorkspaceContract(route.Handler.String())+"\n\n"+prompt.ProjectContext(root)))

	var requests []ai.Request
	retries := 0
	retry := retryLiveStream(live.stream, options.Retries, options.MinInterval, &retries)
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		requests = append(requests, req)
		temperature := options.Temperature
		req.Temperature = &temperature
		return retry(ctx, req)
	}
	deadline := time.Time{}
	if options.Timeout > 0 {
		deadline = time.Now().Add(options.Timeout)
	}
	loop, err := agent.New(agent.Config{
		Model:     live.model,
		Stream:    stream,
		System:    system,
		Tools:     toolSet,
		MaxTokens: options.MaxTokens,
		MaxTurns:  options.MaxTurns,
		Deadline:  deadline,
		Authorize: newBenchmarkGate(root, route.Handler),
	})
	if err != nil {
		return metrics, err
	}
	metrics.Budget = fmt.Sprintf("maxTurns=%d maxTokens=%d timeout=%s", options.MaxTurns, options.MaxTokens, options.Timeout)
	var evidence *cognition.EvidenceState
	if projection != "off" {
		facts := cognition.ProbeFacts(root)
		shape := cognition.BuildShape(contract, cognition.ShapeParams{
			Authority: route.Handler.String(),
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
	metrics.Retries = retries
	perRequest, totalBytes, repeated, newBytes, toolSchema, totalTokens := measureRequests(requests)
	aggregateRequestMetrics(&metrics, perRequest, totalBytes, repeated, newBytes, toolSchema, totalTokens)
	summarizeTurns(&metrics, turns)
	scoreAnswer(&metrics, sc.Facts)
	review := ""
	if n := len(metrics.ProjectionReports); n > 0 {
		review = metrics.ProjectionReports[n-1].EvidenceReview()
	}
	metrics.EvidenceReview = review
	for _, turn := range turns {
		metrics.ProviderInputTokens += turn.Assistant.Usage.Input + turn.Assistant.Usage.CacheRead
		metrics.ProviderOutputTokens += turn.Assistant.Usage.Output
		metrics.ProviderCacheRead += turn.Assistant.Usage.CacheRead
		metrics.ProviderCacheWrite += turn.Assistant.Usage.CacheWrite
		metrics.ProviderCostUSD += live.model.Cost.Total(turn.Assistant.Usage)
	}
	deadlineExpired := !deadline.IsZero() && !time.Now().Before(deadline)
	if runErr != nil {
		resourceStop := deadlineExpired || errors.Is(runErr, agent.ErrTurnLimit) ||
			errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, budget.ErrExhausted)
		if resourceStop {
			// A turn/deadline/budget stop is a resource pause, not a model
			// failure. Proven evidence can still complete; otherwise the run is
			// resumable.
			completion := agent.EvaluateBudgetCompletion(agent.CompletionInput{
				Turns: turns, Contract: contract, Snapshot: snapshot, Resolver: resolver, Root: root,
			}, true)
			if completion.Complete() {
				metrics.ObjectiveStatus = "COMPLETED"
				metrics.CompletionReason = ""
			} else {
				metrics.ObjectiveStatus = "PAUSED"
				metrics.CompletionReason = runErr.Error()
			}
			metrics.VerificationStatus = "not_applicable"
			return metrics, nil
		}
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
	if deadlineExpired {
		completion = agent.EvaluateBudgetCompletion(agent.CompletionInput{
			Turns: turns, Contract: contract, Snapshot: snapshot, Resolver: resolver, Root: root,
			EvidenceReview: review,
		}, true)
		if !completion.Complete() {
			metrics.CompletionReason = "deadline reached"
		}
	}
	metrics.CompletionReason = completion.Reason
	if completion.Complete() {
		metrics.ObjectiveStatus = "COMPLETED"
	} else if deadlineExpired {
		metrics.ObjectiveStatus = "PAUSED"
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

// projectionMode names the arm in the artifact without inferring it from the
// implementation detail later.
func projectionMode(projection string) string {
	if projection == "off" {
		return "control"
	}
	return "cognitive"
}

// scenarioHash identifies the exact task definition and fixture.
func scenarioHash(sc scenario) string {
	encoded, _ := json.Marshal(sc)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// toolSetHash identifies the offered capability surface, including schemas.
func toolSetHash(tools []agent.Tool) string {
	var texts []string
	for _, tool := range tools {
		encoded, _ := json.Marshal(agent.Spec(tool).Parameters)
		texts = append(texts, tool.Name(), tool.Description(), string(encoded))
	}
	sum := sha256.Sum256([]byte(strings.Join(texts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// retryLiveStream absorbs transient provider throttling. A benchmark that
// records a quota 429 as a model failure would be dishonest; a non-retryable
// error is surfaced unchanged.
func retryLiveStream(base ai.StreamFn, attempts int, minInterval time.Duration, retries *int) ai.StreamFn {
	if attempts < 1 {
		attempts = 1
	}
	var last time.Time
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		if minInterval > 0 && !last.IsZero() {
			if wait := minInterval - time.Since(last); wait > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
			}
		}
		last = time.Now()
		var lastErr error
		for attempt := 0; attempt < attempts; attempt++ {
			stream, err := base(ctx, req)
			if err == nil {
				return stream, nil
			}
			lastErr = err
			if !retryableLiveError(err) {
				return nil, err
			}
			if attempt+1 < attempts && retries != nil {
				(*retries)++
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
			}
		}
		return nil, lastErr
	}
}

func retryableLiveError(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"http 408", "http 409", "http 425", "http 429",
		"http 500", "http 502", "http 503", "http 504",
		"rate limit", "temporarily", "timeout", "connection reset",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// liveSummary renders one line for a live run. It is used by the CLI so a
// provider comparison does not require reading a JSON file.
func liveSummary(metrics runMetrics) string {
	return fmt.Sprintf("%-36s %-13s turns=%d tools=%d in=%d out=%d cache_read=%d wall=%dms",
		metrics.Scenario, metrics.ObjectiveStatus, metrics.TurnCount, metrics.ToolCallCount,
		metrics.ProviderInputTokens, metrics.ProviderOutputTokens, metrics.ProviderCacheRead,
		metrics.WallMillis)
}
