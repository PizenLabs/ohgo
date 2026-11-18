// Package gry adapts the external gry repository-intelligence CLI to Ohgo's
// structural-intelligence provider contract.
//
// It is an adapter, not an architectural center. Ohgo's planner decides that a
// structural step is required; investigate.StructuralRouter decides whether the
// native engine or this adapter is the better source; this package only turns
// bounded gry observations into the shared evidence vocabulary.
//
// The adapter is optional and read-only. It never indexes, mutates, approves,
// completes, or bypasses the shared budget. It launches only the read-only
// query subcommands listed in operationFor, with a hard timeout and output cap,
// and it treats every failure as an explicit outcome rather than as absence.
package gry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// Operation bounds.
const (
	maxObservationFacts   = 16
	maxObservationRecords = 24
	maxObservationIssues  = 12
	maxFailureMessage     = 400
)

// Errors that classify a process failure. They are wrapped so the outcome
// mapping is explicit rather than a string match on the process output.
var (
	errTimeout        = errors.New("gry command timed out")
	errOutputTooLarge = errors.New("gry output exceeded the adapter limit")
)

// runner executes one read-only gry command. It exists as a field so tests can
// substitute a deterministic runner without starting a process.
type runner func(ctx context.Context, cfg Config, args []string) (stdout, stderr []byte, err error)

// Provider implements investigate.Provider over gry.
//
// It owns no graph, index, task state, or completion state. The only retained
// state is the resolved project identity (gry's decision, cached briefly) and
// the resolved executable path.
type Provider struct {
	cfg Config
	run runner

	mu        sync.Mutex
	binary    string
	project   string
	projectAt time.Time
}

var _ investigate.Provider = (*Provider)(nil)

// New returns a provider for cfg. It performs no I/O: construction is safe on
// every Ohgo startup, including a machine without gry.
func New(cfg Config) *Provider {
	return &Provider{cfg: cfg.normalized(), run: execCommand}
}

// newWithRunner is the test seam for deterministic command results.
func newWithRunner(cfg Config, run runner) *Provider {
	provider := New(cfg)
	if run != nil {
		provider.run = run
	}
	return provider
}

// Name implements investigate.Provider. gry answers the structural capability;
// the mechanism is recorded in Observation.Source and evidence.SourceGRY.
func (p *Provider) Name() planner.Provider { return planner.ProviderStructural }

// Available implements investigate.Provider.
//
// It reports whether a gry executable can be started. It does not require an
// index: a missing index is an explicit NOT_INDEXED outcome when a query is
// requested, not a startup failure. It starts no process.
func (p *Provider) Available(context.Context) bool {
	if p == nil || p.cfg.Disabled || p.cfg.Root == "" {
		return false
	}
	_, err := p.binaryPath()
	return err == nil
}

// Observe implements investigate.Provider.
//
// The router charges the external-request budget before calling this method, so
// the adapter does not charge it twice. Every classified failure is returned as
// an Observation with an explicit Outcome; the error return is reserved for a
// future transport bug, never for "gry said no".
func (p *Provider) Observe(ctx context.Context, req investigate.Request, _ planner.Step, _ *investigate.Meter) (investigate.Observation, error) {
	if p == nil || p.cfg.Disabled || p.cfg.Root == "" {
		return failureObservation(&failure{
			outcome: investigate.OutcomeUnavailable,
			code:    "disabled",
			message: "gry structural intelligence is not configured for this workspace",
		}), nil
	}
	project, projectFailure := p.resolveProject(ctx)
	if projectFailure != nil {
		return failureObservation(projectFailure), nil
	}
	var observation investigate.Observation
	switch operationFor(req) {
	case opContext:
		observation = p.observeContext(ctx, project, req)
	case opResolve:
		observation = p.observeResolve(ctx, project, req)
	case opTrace:
		observation = p.observeTrace(ctx, project, req, "both")
	case opTraceInbound:
		observation = p.observeTrace(ctx, project, req, "inbound")
	case opArchitecture:
		observation = p.observeArchitecture(ctx, project, req)
	case opChangeImpact:
		observation = p.observeChanges(ctx, project, "impact")
	case opTestImpact:
		observation = p.observeChanges(ctx, project, "tests")
	default:
		observation = p.observeContext(ctx, project, req)
	}
	return boundObservation(observation), nil
}

// operation is the structural information requirement, not a gry tool name.
type operation int

const (
	opContext operation = iota
	opResolve
	opTrace
	opTraceInbound
	opArchitecture
	opChangeImpact
	opTestImpact
)

// operationFor maps the task's information need to the cheapest gry question
// that answers it. Role is deliberately absent.
func operationFor(req investigate.Request) operation {
	switch req.Intent {
	case planner.IntentArchitecture:
		return opArchitecture
	case planner.IntentLocate:
		return opResolve
	case planner.IntentFlow:
		return opTrace
	case planner.IntentImpact:
		if strings.TrimSpace(req.Focus) == "" {
			return opChangeImpact
		}
		return opTraceInbound
	case planner.IntentDebug:
		return opContext
	case planner.IntentVerify:
		return opTestImpact
	default:
		return opContext
	}
}

// failure is one classified adapter failure. It is not an evidence state.
type failure struct {
	outcome investigate.Outcome
	code    string
	message string
}

func (f *failure) Error() string {
	if f == nil {
		return "gry: unknown failure"
	}
	return "gry " + f.code + ": " + f.message
}

func failureObservation(f *failure) investigate.Observation {
	if f == nil {
		f = &failure{outcome: investigate.OutcomeFailed, code: "failed", message: "unknown failure"}
	}
	return investigate.Observation{
		Outcome:    f.outcome,
		State:      evidence.Unknown,
		Confidence: 0.05,
		Source:     "gry",
		Issues:     []string{"gry " + f.code + ": " + f.message},
	}
}

// failureFromCommand classifies a process error. The classification is explicit
// so a timeout is never reported as an empty successful result.
func failureFromCommand(op string, err error, stderr []byte) *failure {
	switch {
	case errors.Is(err, context.Canceled):
		return &failure{investigate.OutcomeUnavailable, "cancelled", op + " was cancelled"}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, errTimeout):
		return &failure{investigate.OutcomeUnavailable, "timeout", op + " exceeded its time budget"}
	case errors.Is(err, errOutputTooLarge):
		return &failure{investigate.OutcomeFailed, "output_too_large", op + " output exceeded the adapter limit"}
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return &failure{investigate.OutcomeUnavailable, "unavailable", "gry executable was not found"}
	}
	message := strings.TrimSpace(string(stderr))
	if message == "" {
		message = err.Error()
	}
	return &failure{investigate.OutcomeFailed, "command_failed", op + ": " + truncate(singleLine(message), maxFailureMessage)}
}

// resolveProject asks gry which indexed project owns this workspace. It never
// guesses an identity from a directory name and never triggers indexing.
func (p *Provider) resolveProject(ctx context.Context) (string, *failure) {
	if p.cfg.Project != "" {
		return p.cfg.Project, nil
	}
	p.mu.Lock()
	if p.project != "" && time.Since(p.projectAt) < projectCacheTTL {
		project := p.project
		p.mu.Unlock()
		return project, nil
	}
	p.mu.Unlock()

	stdout, stderr, err := p.run(ctx, p.cfg, []string{"projects", "--json"})
	if err != nil {
		return "", failureFromCommand("projects", err, stderr)
	}
	var payload projectsResponse
	if err := json.Unmarshal(stdout, &payload); err != nil {
		return "", &failure{investigate.OutcomeFailed, "invalid_output", "projects returned invalid JSON"}
	}
	root, err := canonicalPath(p.cfg.Root)
	if err != nil {
		return "", &failure{investigate.OutcomeUnavailable, "unavailable", "workspace root cannot be resolved"}
	}
	var matches []string
	for _, project := range payload.Projects {
		candidate, err := canonicalPath(project.RootPath)
		if err != nil {
			continue
		}
		if candidate == root {
			matches = append(matches, project.Name)
		}
	}
	switch len(matches) {
	case 0:
		return "", &failure{
			investigate.OutcomeNotIndexed,
			"not_indexed",
			"no gry project is indexed for this workspace; this is not evidence that the target is absent",
		}
	case 1:
		p.mu.Lock()
		p.project, p.projectAt = matches[0], time.Now()
		p.mu.Unlock()
		return matches[0], nil
	default:
		return "", &failure{
			investigate.OutcomeAmbiguous,
			"ambiguous_project",
			"more than one gry project is indexed for this workspace; no project was selected",
		}
	}
}

func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}

func (p *Provider) binaryPath() (string, error) {
	p.mu.Lock()
	cached := p.binary
	p.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	binary, err := resolveBinary(p.cfg)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.binary = binary
	p.mu.Unlock()
	return binary, nil
}

// resolveBinary accepts a PATH name, an absolute path, or a workspace-relative
// path. A relative path containing a separator is resolved against the indexed
// workspace rather than the process working directory.
func resolveBinary(cfg Config) (string, error) {
	binary := cfg.Binary
	if !filepath.IsAbs(binary) && strings.ContainsRune(binary, filepath.Separator) {
		binary = filepath.Join(cfg.Root, binary)
	}
	if !strings.ContainsRune(binary, filepath.Separator) {
		return exec.LookPath(binary)
	}
	info, err := os.Stat(binary)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("gry binary %q is a directory", binary)
	}
	return binary, nil
}

// execCommand is the production runner. It launches only the args supplied by
// the adapter, from the workspace root, with a filtered environment.
func execCommand(parent context.Context, cfg Config, args []string) ([]byte, []byte, error) {
	path, err := resolveBinary(cfg)
	if err != nil {
		return nil, nil, err
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, nil, err
		}
	}
	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	command.Dir = cfg.Root
	command.Env = filteredEnv(cfg.CacheDir)
	stdout := &limitedBuffer{remaining: cfg.MaxOutputBytes}
	stderr := &limitedBuffer{remaining: maxStderrBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, stderr.Bytes(), errTimeout
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, stderr.Bytes(), context.Canceled
	}
	if stdout.truncated {
		return nil, stderr.Bytes(), errOutputTooLarge
	}
	return stdout.Bytes(), stderr.Bytes(), runErr
}

// filteredEnv drops credentials and unrelated secrets. gry needs its own
// configuration, PATH, and the user's locale; it does not need model keys.
func filteredEnv(cacheDir string) []string {
	keep := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "LC_ALL": true,
	}
	env := make([]string, 0, 16)
	for _, pair := range os.Environ() {
		name, _, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if name == "GRY_CACHE_DIR" && cacheDir != "" {
			continue
		}
		if keep[name] || strings.HasPrefix(name, "GRY_") {
			env = append(env, pair)
		}
	}
	if cacheDir != "" {
		env = append(env, "GRY_CACHE_DIR="+cacheDir)
	}
	return env
}

type limitedBuffer struct {
	data      []byte
	remaining int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	n := len(p)
	if n > b.remaining {
		n = b.remaining
		b.truncated = true
	}
	b.data = append(b.data, p[:n]...)
	b.remaining -= n
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte { return b.data }

// boundObservation enforces the context contract after a parser has produced
// facts. The parser may see a large raw response; the model never does.
func boundObservation(observation investigate.Observation) investigate.Observation {
	factsOmitted := 0
	if len(observation.Facts) > maxObservationFacts {
		factsOmitted = len(observation.Facts) - maxObservationFacts
		observation.Facts = observation.Facts[:maxObservationFacts]
	}
	recordsOmitted := 0
	if len(observation.Records) > maxObservationRecords {
		recordsOmitted = len(observation.Records) - maxObservationRecords
		observation.Records = observation.Records[:maxObservationRecords]
	}
	if len(observation.Issues) > maxObservationIssues {
		observation.Issues = observation.Issues[:maxObservationIssues]
		observation.Issues = append(observation.Issues, "additional issues were omitted by the adapter bound")
	}
	if factsOmitted > 0 || recordsOmitted > 0 {
		observation.Issues = append(observation.Issues, fmt.Sprintf(
			"bounded output: %d fact(s) and %d evidence record(s) were omitted",
			factsOmitted, recordsOmitted))
		if observation.Outcome == investigate.OutcomeSuccess {
			observation.Outcome = investigate.OutcomePartial
		}
	}
	if len(observation.Summary) > 400 {
		observation.Summary = observation.Summary[:400] + "…"
	}
	return observation
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func singleLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
