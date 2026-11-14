// Package investigate orchestrates one bounded intelligence investigation.
//
// The planner stays a pure decision layer: this package asks it for the next
// cheapest sufficient step, executes exactly that step through the provider
// that owns the capability, publishes every observation into one shared
// evidence engine, and then asks the planner again with the observed
// confidence. A provider never decides to run; availability is reported before
// planning, and an authorized runtime session is never inferred from intent.
package investigate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// Budget bounds one investigation. Zero fields use the defaults.
type Budget struct {
	// MaxSteps bounds executed providers, including re-planned steps.
	MaxSteps int
	// MaxDuration bounds wall-clock work end to end.
	MaxDuration time.Duration
	// MaxRecords bounds records retained from all providers.
	MaxRecords int
	// MaxLSPRequests bounds language-server calls in the run.
	MaxLSPRequests int
	// MaxDAPRequests bounds debug-adapter calls in the run.
	MaxDAPRequests int
	// MaxOutputBytes is advisory to the renderer; the engine does not truncate.
	MaxOutputBytes int
}

// Defaults.
const (
	DefaultMaxSteps    = 5
	DefaultMaxRecords  = 120
	DefaultMaxLSP      = 24
	DefaultMaxDAP      = 24
	DefaultMaxDuration = 20 * time.Second
	DefaultMaxOutput   = 8000
	maxQuestionsChars  = 512
	maxSummaryChars    = 240
)

// Budget normalized returns d with unset fields defaulted.
func (b Budget) normalized() Budget {
	if b.MaxSteps <= 0 {
		b.MaxSteps = DefaultMaxSteps
	}
	if b.MaxSteps > 8 {
		b.MaxSteps = 8
	}
	if b.MaxDuration <= 0 {
		b.MaxDuration = DefaultMaxDuration
	}
	if b.MaxRecords <= 0 {
		b.MaxRecords = DefaultMaxRecords
	}
	if b.MaxRecords > 500 {
		b.MaxRecords = 500
	}
	if b.MaxLSPRequests <= 0 {
		b.MaxLSPRequests = DefaultMaxLSP
	}
	if b.MaxDAPRequests <= 0 {
		b.MaxDAPRequests = DefaultMaxDAP
	}
	if b.MaxOutputBytes <= 0 {
		b.MaxOutputBytes = DefaultMaxOutput
	}
	return b
}

// Request is one bounded investigation request.
type Request struct {
	// Question is the task text. It is also the retrieval query.
	Question string
	// Intent selects the planner row. Empty is inferred from the question.
	Intent planner.Intent
	// Focus is an optional path or symbol the question is about.
	Focus string
	// Budget bounds the run.
	Budget Budget
}

// Observation is what one provider observed. It never replaces prior
// observations: the engine appends every Record and correlates them.
type Observation struct {
	State      evidence.State
	Confidence float64
	Coverage   float64
	Summary    string
	Facts      []Fact
	Records    []evidence.Record
	Issues     []string
}

// Fact is one bounded, model-facing statement from a provider.
type Fact struct {
	Provider string
	Subject  string
	Detail   string
}

// Provider is one intelligence capability.
//
// Observe must do the work for exactly one planner step. Returning an error
// means the step did not establish a fact; the caller records that as an
// explicit failure, never as absence.
type Provider interface {
	Name() planner.Provider
	Available(ctx context.Context) bool
	Observe(ctx context.Context, req Request, step planner.Step, meter *Meter) (Observation, error)
}

// Meter is the one shared budget all providers charge.
type Meter struct {
	mu             sync.Mutex
	lsp, dap       int
	maxLSP, maxDAP int
}

// ErrBudget reports that a provider hit its share of the shared budget.
var ErrBudget = errors.New("investigate: budget exhausted")

func newMeter(b Budget) *Meter {
	return &Meter{maxLSP: b.MaxLSPRequests, maxDAP: b.MaxDAPRequests}
}

// ChargeLSP accounts one language-server request.
func (m *Meter) ChargeLSP() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lsp >= m.maxLSP {
		return fmt.Errorf("%w: language-server request limit %d", ErrBudget, m.maxLSP)
	}
	m.lsp++
	return nil
}

// ChargeDAP accounts one debug-adapter request.
func (m *Meter) ChargeDAP() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dap >= m.maxDAP {
		return fmt.Errorf("%w: debug-adapter request limit %d", ErrBudget, m.maxDAP)
	}
	m.dap++
	return nil
}

// LSP and DAP report consumption.
func (m *Meter) LSP() int { m.mu.Lock(); defer m.mu.Unlock(); return m.lsp }
func (m *Meter) DAP() int { m.mu.Lock(); defer m.mu.Unlock(); return m.dap }

// Status is the terminal state of one investigation.
type Status string

// Statuses.
const (
	// StatusSufficient means the plan reached its confidence target.
	StatusSufficient Status = "sufficient"
	// StatusPartial means work stopped before the target, with explicit issues.
	StatusPartial Status = "partial"
	// StatusCancelled means the caller's context ended.
	StatusCancelled Status = "cancelled"
	// StatusFailed means no provider could run.
	StatusFailed Status = "failed"
)

// StepResult is the auditable outcome of one executed step.
type StepResult struct {
	Provider   planner.Provider
	Strategy   string
	Reason     string
	State      evidence.State
	Confidence float64
	Coverage   float64
	Summary    string
	Issues     []string
	Duration   time.Duration
	Failed     bool
}

// Result is one bounded investigation result. It contains no raw graph, raw
// protocol payload, or full index.
type Result struct {
	Status         Status
	Intent         planner.Intent
	Question       string
	Focus          string
	Steps          []StepResult
	Skips          []planner.Skip
	Facts          []Fact
	Claims         []evidence.Claim
	Contradictions []evidence.Contradiction
	Records        int
	Confidence     float64
	Coverage       float64
	StopReason     string
	Issues         []string
	Duration       time.Duration
	LSPRequests    int
	DAPRequests    int
}

// Engine runs adaptive investigations over a fixed provider set.
type Engine struct {
	providers []Provider
}

// New returns an engine over providers. Provider order is irrelevant: the
// planner decides selection and the result is sorted.
func New(providers ...Provider) *Engine {
	return &Engine{providers: providers}
}

// Errors.
var (
	// ErrEmptyQuestion reports a request without a question.
	ErrEmptyQuestion = errors.New("investigate: question is required")
	// ErrUnknownIntent reports an intent no planner row owns.
	ErrUnknownIntent = errors.New("investigate: unknown intent")
)

// Run executes the adaptive loop and returns a bounded result.
//
// The loop is PLAN -> EXECUTE ONE STEP -> OBSERVE -> CORRELATE -> REASSESS.
// Executing one step at a time is deliberate: the next decision must see the
// evidence this step produced.
func (e *Engine) Run(ctx context.Context, req Request) (Result, error) {
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return Result{}, ErrEmptyQuestion
	}
	if len(question) > maxQuestionsChars {
		question = question[:maxQuestionsChars]
	}
	budget := req.Budget.normalized()
	intent := req.Intent
	if intent == "" {
		intent = InferIntent(question)
	}
	if _, err := planner.Build(intent, planner.Availability{Structural: true}, planner.Budget{MaxSteps: 1}); err != nil {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownIntent, intent)
	}
	start := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, budget.MaxDuration)
	defer cancel()

	result := Result{Intent: intent, Question: question, Focus: req.Focus}
	available := e.availability(runCtx)
	engine := evidence.New()
	meter := newMeter(budget)
	executed := make(map[planner.Provider]bool)
	skips := make(map[planner.Provider]planner.Skip)
	stop := ""
	remainingBudget := budget

	for len(result.Steps) < budget.MaxSteps {
		if err := runCtx.Err(); err != nil {
			stop = "cancelled: " + err.Error()
			result.Status = StatusCancelled
			break
		}
		var avail planner.Availability
		available.apply(&avail)
		plan, err := planner.Reassess(intent, avail, planner.Budget{MaxSteps: remainingBudget.MaxSteps},
			planner.Progress{Executed: executed, Confidence: result.Confidence, Coverage: result.Coverage})
		if err != nil {
			if len(result.Steps) == 0 {
				return Result{}, err
			}
			result.Issues = append(result.Issues, "planner: "+err.Error())
			result.Status = StatusPartial
			stop = "planner could not select another provider: " + err.Error()
			break
		}
		for _, skip := range plan.Skips {
			skips[skip.Provider] = skip
		}
		if len(plan.Steps) == 0 {
			if result.Confidence >= plan.Target {
				result.Status = StatusSufficient
				stop = "confidence target reached"
			} else {
				result.Status = StatusPartial
				stop = "no provider could add evidence: " + string(plan.Stop)
			}
			break
		}
		step := plan.Steps[0]
		provider := e.provider(step.Provider)
		if provider == nil {
			available.set(step.Provider, false)
			remainingBudget.MaxSteps--
			continue
		}
		stepStart := time.Now()
		observation, err := provider.Observe(runCtx, req, step, meter)
		elapsed := time.Since(stepStart)
		executed[step.Provider] = true
		remainingBudget.MaxSteps--
		outcome := StepResult{
			Provider: step.Provider, Strategy: step.Strategy, Reason: step.Reason,
			State: evidence.Unknown, Confidence: result.Confidence,
			Coverage: result.Coverage, Duration: elapsed,
			Issues: append([]string(nil), observation.Issues...),
		}
		if err != nil {
			outcome.Failed = true
			available.set(step.Provider, false)
			issue := fmt.Sprintf("%s failed: %v; no fact was inferred from the failure", step.Provider, err)
			outcome.Issues = append(outcome.Issues, issue)
			result.Issues = append(result.Issues, issue)
		} else {
			state := observation.State
			if state == "" {
				state = evidence.Observed
			}
			outcome.State = state
			outcome.Summary = capText(observation.Summary, maxSummaryChars)
			result.Facts = append(result.Facts, observation.Facts...)
			result.Issues = append(result.Issues, observation.Issues...)
			for _, record := range observation.Records {
				if result.Records >= budget.MaxRecords {
					result.Issues = append(result.Issues, "evidence record cap reached; later records were not retained")
					break
				}
				if _, addErr := engine.Add(record); addErr != nil {
					result.Issues = append(result.Issues, "evidence: "+addErr.Error())
					continue
				}
				result.Records++
			}
			result.Confidence = combine(result.Confidence, clamp01(observation.Confidence))
			result.Coverage = max(result.Coverage, clamp01(observation.Coverage))
			outcome.Confidence, outcome.Coverage = result.Confidence, result.Coverage
		}
		result.Steps = append(result.Steps, outcome)
	}

	if result.Status == "" {
		result.Status = StatusPartial
		if stop == "" {
			stop = "step budget exhausted"
		}
	}
	result.Skips = result.Skips[:0]
	for _, skip := range skips {
		result.Skips = append(result.Skips, skip)
	}
	sort.SliceStable(result.Skips, func(i, j int) bool {
		return result.Skips[i].Provider < result.Skips[j].Provider
	})
	correlation := engine.Correlate()
	result.Claims = correlation.Claims
	result.Contradictions = correlation.Contradictions
	result.StopReason = stop
	result.Duration = time.Since(start)
	result.LSPRequests, result.DAPRequests = meter.LSP(), meter.DAP()
	sort.SliceStable(result.Facts, func(i, j int) bool {
		if result.Facts[i].Provider != result.Facts[j].Provider {
			return result.Facts[i].Provider < result.Facts[j].Provider
		}
		if result.Facts[i].Subject != result.Facts[j].Subject {
			return result.Facts[i].Subject < result.Facts[j].Subject
		}
		return result.Facts[i].Detail < result.Facts[j].Detail
	})
	return result, nil
}

func (e *Engine) provider(name planner.Provider) Provider {
	for _, provider := range e.providers {
		if provider.Name() == name {
			return provider
		}
	}
	return nil
}

func (e *Engine) availability(ctx context.Context) *availability {
	set := &availability{}
	for _, provider := range e.providers {
		set.set(provider.Name(), provider.Available(ctx))
	}
	return set
}

// availability is the typed planner input.
type availability struct {
	structural, retrieval, semantic, runtime bool
}

func (a *availability) set(provider planner.Provider, value bool) {
	switch provider {
	case planner.ProviderStructural:
		a.structural = value
	case planner.ProviderRetrieval:
		a.retrieval = value
	case planner.ProviderSemantic:
		a.semantic = value
	case planner.ProviderRuntime:
		a.runtime = value
	}
}

func (a *availability) apply(dst *planner.Availability) {
	dst.Structural, dst.Retrieval, dst.Semantic, dst.Runtime = a.structural, a.retrieval, a.semantic, a.runtime
}

// InferIntent classifies a question into a planner row.
//
// It is a deterministic keyword heuristic, not a semantic judgement: its job is
// to avoid running an expensive row when the question plainly asks for an
// overview or a location. The caller can always pass an explicit intent.
func InferIntent(question string) planner.Intent {
	text := strings.ToLower(question)
	switch {
	case containsAny(text, "architect", "layering", "coupling", "boundar", "monorepo structure"):
		return planner.IntentArchitecture
	case containsAny(text, "impact", "depend", "affect", "safe to change", "reverse"):
		return planner.IntentImpact
	case containsAny(text, "fail", "bug", "panic", "error", "crash", "runtime", "debug"):
		return planner.IntentDebug
	case containsAny(text, "flow", "call path", "how does", "trace", "sequence"):
		return planner.IntentFlow
	case containsAny(text, "where", "locate", "find", "implemented", "which file"):
		return planner.IntentLocate
	case containsAny(text, "verify", "prove", "confirm", "evidence"):
		return planner.IntentVerify
	default:
		return planner.IntentProject
	}
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func combine(current, observed float64) float64 {
	return clamp01(current + (1-current)*observed)
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func max(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func capText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
