package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/execution/sandbox"
)

// Verification defaults and hard bounds.
const (
	DefaultVerifyReadinessTimeout = 15 * time.Second
	MaxVerifyReadinessTimeout     = 60 * time.Second
	DefaultVerifyCheckTimeout     = 30 * time.Second
	MaxVerifyCheckTimeout         = 5 * time.Minute
	DefaultVerifyBodyBytes        = 1 << 20
	MaxVerifyBodyBytes            = 4 << 20
	DefaultVerifyOutputBytes      = 1 << 20
	MaxVerifyChecks               = 8
	MaxVerifyConcurrency          = 4
	verifyStopGrace               = 2 * time.Second
	verifyPollInterval            = 25 * time.Millisecond
	verifyEvidenceTailBytes       = 2000
)

// VerifyTool is the structured execution primitive for a verification plan.
//
// It owns the mechanical lifecycle the model should not orchestrate by hand:
// start a service in its own process group, wait for a declared readiness
// condition, run independent checks (HTTP, optional headless browser, command),
// collect bounded evidence, and stop the service on every exit path. It never
// marks an objective complete by itself: the result carries the same
// Executes/Finished/Verifies evidence every other capability uses, and the
// completion evaluator still decides.
type VerifyTool struct {
	Base
	// Root is the workspace the plan runs in.
	Root string
	// Now is the clock seam. Nil uses time.Now.
	Now func() time.Time
	// BrowserPath optionally overrides the headless browser binary. Empty
	// means discover one at call time; absence is an unsupported optional
	// capability, not a failure of the rest of the plan.
	BrowserPath string
}

// NewVerifyTool returns the verification capability for one workspace.
func NewVerifyTool(root string) *VerifyTool {
	tool := &VerifyTool{Root: root, Now: time.Now}
	tool.Base = NewBase("verify",
		"Execute a verification plan with deterministic lifecycle ownership: "+
			"start a long-running service, wait for HTTP/TCP/output readiness, run "+
			"HTTP, command, and optional headless-browser checks, then stop the "+
			"service and return compact structured evidence. Use this instead of "+
			"starting a server with bash and manually polling, finding a port, "+
			"opening pages, and killing the process. Browser checks are optional "+
			"and report unsupported when no headless browser is available.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"service": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"command"},
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Long-running service command, for example \"npx live-server\".",
						},
						"cwd": map[string]any{
							"type":        "string",
							"description": "Workspace-relative working directory.",
						},
						"readiness": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"properties": map[string]any{
								"kind": map[string]any{
									"type":        "string",
									"enum":        []any{"http", "tcp", "output", "process"},
									"description": "Readiness signal. Defaults to http; a missing URL is discovered from service output.",
								},
								"url": map[string]any{
									"type":        "string",
									"description": "Explicit loopback readiness URL. Omit to discover the advertised URL.",
								},
								"expect": map[string]any{
									"type":        "string",
									"description": "Literal output text for output readiness.",
								},
								"timeout_ms": map[string]any{
									"type":    "integer",
									"minimum": 1.0,
									"maximum": float64(MaxVerifyReadinessTimeout.Milliseconds()),
								},
							},
						},
					},
				},
				"checks": map[string]any{
					"type":        "array",
					"maxItems":    float64(MaxVerifyChecks),
					"description": "Independent checks that run only after readiness. At least one check or a service is required.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []string{"kind"},
						"properties": map[string]any{
							"kind": map[string]any{
								"type": "string",
								"enum": []any{"http", "browser", "command"},
							},
							"name": map[string]any{
								"type": "string", "description": "Short label for this check.",
							},
							"url": map[string]any{
								"type":        "string",
								"description": "Loopback URL. Supports {url} and {port} from the discovered service URL; defaults to the service URL.",
							},
							"status": map[string]any{
								"type":        "integer",
								"description": "Exact HTTP status. Omit to accept any 2xx response.",
							},
							"contains": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "string"},
								"maxItems":    float64(16),
								"description": "Literal strings the response body or rendered DOM must contain.",
							},
							"command": map[string]any{
								"type":        "string",
								"description": "Command for a command check.",
							},
							"timeout_ms": map[string]any{
								"type":    "integer",
								"minimum": 1.0,
								"maximum": float64(MaxVerifyCheckTimeout.Milliseconds()),
							},
						},
					},
				},
				"parallel": map[string]any{
					"type":        "boolean",
					"description": "Run independent checks concurrently after readiness (default true, capped).",
				},
			},
		},
		agent.Approval{Tier: agent.TierExec, Reason: "starts a bounded service and runs verification checks"},
	)
	tool.SetActionClasses(agent.ActionExecute)
	// A service owns a port and a process group. Running two plans together
	// would make port readiness and cleanup nondeterministic, so verification
	// drains the batch like a foreground shell.
	tool.SetExclusive(true)
	return tool
}

// Capability refines the invocation tier from the commands it will run. A
// mutating service or check is a write so FUZZ cannot use verification as a
// back door; an HTTP-only plan remains execution.
func (t *VerifyTool) Capability(args map[string]any) agent.Tier {
	for _, command := range verifyCommands(args) {
		if commandCapabilityInRoot(command, t.Root) == agent.TierWrite {
			return agent.TierWrite
		}
	}
	return agent.TierExec
}

// verifyCommands collects every command the plan could execute.
func verifyCommands(args map[string]any) []string {
	var out []string
	if service, ok := args["service"].(map[string]any); ok {
		if command, ok := service["command"].(string); ok && strings.TrimSpace(command) != "" {
			out = append(out, command)
		}
	}
	if checks, ok := args["checks"].([]any); ok {
		for _, raw := range checks {
			check, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if command, ok := check["command"].(string); ok && strings.TrimSpace(command) != "" {
				out = append(out, command)
			}
		}
	}
	return out
}

// Execute implements agent.Tool.
func (t *VerifyTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	return t.execute(ctx, args)
}

func (t *VerifyTool) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// verifyPlan is the validated internal form of one plan.
type verifyPlan struct {
	Service  *verifyServicePlan
	Checks   []verifyCheckPlan
	Parallel bool
}

type verifyServicePlan struct {
	Command   string
	Dir       string
	Readiness verifyReadiness
}

type verifyReadiness struct {
	Kind    string
	URL     string
	Expect  string
	Timeout time.Duration
}

type verifyCheckPlan struct {
	Kind     string
	Name     string
	URL      string
	Status   int
	Contains []string
	Command  string
	Timeout  time.Duration
}

type verifyServiceState string

const (
	verifyStarting verifyServiceState = "STARTING"
	verifyRunning  verifyServiceState = "RUNNING"
	verifyReady    verifyServiceState = "READY"
	verifyFailed   verifyServiceState = "FAILED"
	verifyStopping verifyServiceState = "STOPPING"
	verifyStopped  verifyServiceState = "STOPPED"
	verifyTimedOut verifyServiceState = "TIMED_OUT"
)

// verifyService is one owned process and its bounded output.
type verifyService struct {
	command string
	dir     string
	cmd     *exec.Cmd
	output  *boundedOutput
	done    chan struct{}
	waitErr error

	mu          sync.Mutex
	state       verifyServiceState
	baseURL     string
	startedAt   time.Time
	readyAt     time.Time
	stoppedAt   time.Time
	termination string
}

// boundedOutput is a thread-safe, byte-bounded combined output buffer.
type boundedOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedOutput(limit int) *boundedOutput {
	if limit <= 0 {
		limit = DefaultVerifyOutputBytes
	}
	return &boundedOutput{limit: limit}
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len()+len(p) > b.limit {
		overflow := b.buf.Len() + len(p) - b.limit
		kept := b.buf.Bytes()
		if overflow > len(kept) {
			overflow = len(kept)
		}
		rest := append([]byte(nil), kept[overflow:]...)
		b.buf.Reset()
		b.buf.Write(rest)
		b.truncated = true
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *boundedOutput) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// Tail returns the last n bytes without mutating the retained evidence.
func (b *boundedOutput) Tail(n int) string {
	text := b.String()
	if n <= 0 || len(text) <= n {
		return text
	}
	return text[len(text)-n:]
}

func (t *VerifyTool) execute(ctx agent.Context, args map[string]any) agent.Result {
	if ctx.Ctx == nil {
		ctx.Ctx = context.Background()
	}
	if err := checkCancelled(ctx.Ctx); err != nil {
		return verifyFailure(agent.ToolFailureCancelled, "verification cancelled", false, false, nil)
	}
	plan, err := parseVerifyPlan(args)
	if err != nil {
		return agent.Result{
			Text:         "invalid verification plan: " + err.Error(),
			IsError:      true,
			FailureClass: agent.ToolFailureInvalidArgument,
		}
	}
	started := t.now()
	details := map[string]any{
		"verification": map[string]any{
			"status": "STARTING",
		},
	}
	var service *verifyService
	if plan.Service != nil {
		running, startErr := t.startService(ctx, *plan.Service)
		if startErr != nil {
			details["verification"] = map[string]any{
				"status":  "FAILED",
				"service": startErr.details,
			}
			return agent.Result{
				Text:         startErr.text,
				IsError:      true,
				Executes:     startErr.executed,
				Finished:     startErr.executed,
				FailureClass: agent.ToolFailureStart,
				Details:      details,
			}
		}
		service = running
		defer service.stop(verifyStopped)
	}

	results := t.runChecks(ctx, plan, service, details)
	verdict := verificationVerdict(results)
	if service != nil && service.exited() && verdict.Status == "PASS" {
		// A declared long-running service that exited while checks were running
		// did not provide a stable observation, even when its exit code was 0
		// and a fast check happened to pass first. Cleanup had not started yet,
		// so this is a runtime failure, not a normal stop.
		verdict = verificationOutcome{
			Status:       "FAIL",
			FailureClass: agent.ToolFailureRuntime,
			Detail:       fmt.Sprintf("service exited during verification with code %d", service.exitCode()),
		}
	}
	cleanup := "no service"
	if service != nil {
		before := service.stateValue()
		service.stop(verifyStopped)
		cleanup = fmt.Sprintf("stopped pid %d", service.pid())
		if before == verifyTimedOut {
			cleanup = "timed out and killed"
		}
	}
	elapsed := t.now().Sub(started)
	text := renderVerification(plan, service, results, verdict, cleanup, elapsed)
	verification := verificationEvidence(plan, results)

	base := agent.Result{
		Text:         text,
		Details:      details,
		Executes:     service != nil || anyCommandCheck(results),
		Finished:     true,
		Verifies:     verdict.Status == "PASS",
		Verification: verification,
		Mutates:      anyCheckMutated(results),
	}
	if verdict.Status == "PASS" {
		details["verification"] = verificationDetails(plan, service, results, verdict, cleanup, elapsed)
		return base
	}
	base.IsError = true
	base.FailureClass = verdict.FailureClass
	details["verification"] = verificationDetails(plan, service, results, verdict, cleanup, elapsed)
	return base
}

// verifyStartError carries the truth of a failed start without pretending a
// process executed when it never did.
type verifyStartError struct {
	text     string
	details  map[string]any
	executed bool
}

func (t *VerifyTool) startService(ctx agent.Context, plan verifyServicePlan) (*verifyService, *verifyStartError) {
	dir, err := t.resolveDir(plan.Dir)
	if err != nil {
		return nil, &verifyStartError{text: "invalid service directory: " + err.Error(), details: map[string]any{"state": string(verifyFailed)}}
	}
	cmd, err := sandbox.Command(ctx.Ctx, sandbox.Spec{
		Argv:     []string{"sh", "-c", plan.Command},
		Dir:      dir,
		Root:     t.Root,
		ReadOnly: ctx.ReadOnly,
	})
	if err != nil {
		return nil, &verifyStartError{
			text:    "service refused: " + err.Error(),
			details: map[string]any{"state": string(verifyFailed), "command": plan.Command},
		}
	}
	sandbox.Configure(cmd)
	output := newBoundedOutput(DefaultVerifyOutputBytes)
	cmd.Stdout = output
	cmd.Stderr = output
	service := &verifyService{
		command: plan.Command,
		dir:     dir,
		cmd:     cmd,
		output:  output,
		done:    make(chan struct{}),
		state:   verifyStarting,
	}
	if err := cmd.Start(); err != nil {
		return nil, &verifyStartError{
			text:    fmt.Sprintf("service failed to start: %v\n$ %s", err, plan.Command),
			details: map[string]any{"state": string(verifyFailed), "command": plan.Command, "error": err.Error()},
		}
	}
	service.startedAt = t.now()
	t.setServiceState(service, verifyRunning)
	go func() {
		service.waitErr = cmd.Wait()
		close(service.done)
	}()

	if err := t.waitReady(ctx, service, plan.Readiness); err != nil {
		executed := true
		stderr := service.output.Tail(verifyEvidenceTailBytes)
		state := verifyFailed
		if service.stateValue() == verifyTimedOut {
			state = verifyTimedOut
		}
		detail := map[string]any{
			"state":     string(state),
			"command":   plan.Command,
			"pid":       service.pid(),
			"stderr":    stderr,
			"truncated": service.output.Truncated(),
		}
		service.stop(state)
		text := fmt.Sprintf("service did not become ready: %v\n$ %s", err, plan.Command)
		if service.exited() {
			text = fmt.Sprintf("service exited before readiness: %v\n$ %s", err, plan.Command)
			detail["exitCode"] = service.exitCode()
		}
		if stderr != "" {
			text += "\n\ncaptured output:\n" + stderr
		}
		return nil, &verifyStartError{text: text, details: detail, executed: executed}
	}
	service.mu.Lock()
	service.state = verifyReady
	service.readyAt = t.now()
	service.mu.Unlock()
	return service, nil
}

func (t *VerifyTool) resolveDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return t.Root, nil
	}
	if filepath.IsAbs(raw) {
		return "", fmt.Errorf("cwd must be workspace-relative")
	}
	path, err := resolvePath(t.Root, raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", raw)
	}
	return path, nil
}

func (t *VerifyTool) setServiceState(service *verifyService, state verifyServiceState) {
	service.mu.Lock()
	service.state = state
	service.mu.Unlock()
}

func (s *verifyService) stateValue() verifyServiceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *verifyService) setBaseURL(raw string) {
	s.mu.Lock()
	s.baseURL = raw
	s.mu.Unlock()
}

func (s *verifyService) base() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

func (s *verifyService) pid() int {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

func (s *verifyService) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *verifyService) exitCode() int {
	if !s.exited() || s.waitErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(s.waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// stop terminates the owned process group. It is idempotent and safe after a
// natural exit; the deferred call in execute is the cleanup guarantee.
func (s *verifyService) stop(state verifyServiceState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.state == verifyStopped {
		s.mu.Unlock()
		return
	}
	previous := s.state
	s.state = verifyStopping
	s.mu.Unlock()

	if s.cmd != nil && s.cmd.Process != nil {
		_ = sandbox.Kill(s.cmd)
	}
	select {
	case <-s.done:
	case <-time.After(verifyStopGrace):
	}
	s.mu.Lock()
	s.stoppedAt = time.Now()
	s.state = state
	switch {
	case previous == verifyTimedOut:
		s.termination = "readiness timeout"
	case s.exited():
		s.termination = "process exited before runtime cleanup"
	default:
		s.termination = "stopped by runtime"
	}
	s.mu.Unlock()
}

func (t *VerifyTool) waitReady(ctx agent.Context, service *verifyService, readiness verifyReadiness) error {
	timeout := readiness.Timeout
	if timeout <= 0 {
		timeout = DefaultVerifyReadinessTimeout
	}
	deadline := time.Now().Add(timeout)
	kind := strings.ToLower(strings.TrimSpace(readiness.Kind))
	if kind == "" {
		kind = "http"
	}
	var lastErr error
	for {
		if err := ctx.Ctx.Err(); err != nil {
			return err
		}
		if service.exited() {
			return fmt.Errorf("process exited with code %d", service.exitCode())
		}
		switch kind {
		case "process":
			if time.Since(service.startedAt) >= 100*time.Millisecond {
				return nil
			}
		case "output":
			if strings.TrimSpace(readiness.Expect) == "" {
				return fmt.Errorf("output readiness requires expect")
			}
			if strings.Contains(strings.ToLower(service.output.String()), strings.ToLower(readiness.Expect)) {
				return nil
			}
		case "tcp", "http":
			target := strings.TrimSpace(readiness.URL)
			if target == "" {
				if discovered, ok := discoverServiceURL(service.output.String()); ok {
					target = discovered
				}
			}
			if target != "" {
				if kind == "tcp" {
					parsed, err := url.Parse(target)
					if err != nil {
						return fmt.Errorf("readiness URL: %w", err)
					}
					service.setBaseURL(target)
					if err := probeTCP(ctx.Ctx, parsed.Host); err == nil {
						return nil
					} else {
						lastErr = err
					}
				} else if normalized, err := loopbackURL(target); err != nil {
					return err
				} else {
					service.setBaseURL(normalized.String())
					if _, err := probeHTTP(ctx.Ctx, target, DefaultVerifyBodyBytes, time.Second); err == nil {
						return nil
					} else {
						lastErr = err
					}
				}
			}
		default:
			return fmt.Errorf("unknown readiness kind %q", kind)
		}
		if time.Now().After(deadline) {
			detail := "readiness timed out"
			if lastErr != nil {
				detail += ": " + lastErr.Error()
			}
			if kind == "http" || kind == "tcp" {
				detail += " (no loopback service URL was advertised; set readiness.url explicitly)"
			}
			t.setServiceState(service, verifyTimedOut)
			return errors.New(detail)
		}
		select {
		case <-ctx.Ctx.Done():
			return ctx.Ctx.Err()
		case <-service.done:
			return fmt.Errorf("process exited with code %d", service.exitCode())
		case <-time.After(verifyPollInterval):
		}
	}
}

// discoverServiceURL finds the first loopback URL a service advertised.
var advertisedURL = regexp.MustCompile(`(?i)https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):[0-9]{2,5}(?:/[^\s"']*)?`)
var advertisedPort = regexp.MustCompile(`(?i)(?:port|listening(?:\s+on)?|serving(?:\s+at)?)\D{0,24}([0-9]{2,5})`)

func discoverServiceURL(output string) (string, bool) {
	if match := advertisedURL.FindString(output); match != "" {
		return match, true
	}
	if match := advertisedPort.FindStringSubmatch(output); len(match) == 2 {
		return "http://127.0.0.1:" + match[1], true
	}
	return "", false
}

// probeHTTP performs one bounded loopback request.
func probeHTTP(ctx context.Context, rawURL string, maxBytes int, timeout time.Duration) (*verifyHTTPObservation, error) {
	target, err := loopbackURL(rawURL)
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ohgo-verifier/1")
	response, err := verificationClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if maxBytes <= 0 || maxBytes > MaxVerifyBodyBytes {
		maxBytes = DefaultVerifyBodyBytes
	}
	body, readErr := readBounded(response.Body, maxBytes)
	if readErr != nil {
		return nil, readErr
	}
	return &verifyHTTPObservation{
		URL:         target.String(),
		Status:      response.StatusCode,
		ContentType: response.Header.Get("Content-Type"),
		Body:        body,
		Bytes:       len(body),
	}, nil
}

type verifyHTTPObservation struct {
	URL         string
	Status      int
	ContentType string
	Body        string
	Bytes       int
}

// verificationClient has no proxy and dials only loopback addresses. A
// verification plan is a local observation, never a way to reach the network.
var verificationHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if err := requireLoopbackHost(host); err != nil {
				return nil, err
			}
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, network, address)
		},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if _, err := loopbackURL(req.URL.String()); err != nil {
			return err
		}
		return nil
	},
	Timeout: 0,
}

func verificationClient() *http.Client { return verificationHTTPClient }

// loopbackURL validates and normalizes a local verification target.
func loopbackURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("URL scheme must be http or https")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("URL must not carry credentials")
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	switch strings.ToLower(host) {
	case "localhost":
		host = "127.0.0.1"
	case "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if err := requireLoopbackHost(host); err != nil {
		return nil, err
	}
	parsed.Host = net.JoinHostPort(host, port)
	parsed.User = nil
	return parsed, nil
}

func requireLoopbackHost(host string) error {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("verification target %q is not loopback", host)
	}
	return nil
}

func probeTCP(ctx context.Context, address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// A URL without a port is not a TCP readiness target.
		return err
	}
	if err := requireLoopbackHost(host); err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	return conn.Close()
}

func (t *VerifyTool) runChecks(ctx agent.Context, plan verifyPlan, service *verifyService, details map[string]any) []verifyCheckResult {
	results := make([]verifyCheckResult, len(plan.Checks))
	if len(plan.Checks) == 0 {
		return results
	}
	baseURL := ""
	port := ""
	if service != nil {
		baseURL = service.base()
		if discovered, ok := discoverServiceURL(service.output.String()); ok {
			if baseURL == "" {
				baseURL = discovered
			}
		}
		if parsed, err := loopbackURL(baseURL); err == nil {
			baseURL = parsed.String()
			port = parsed.Port()
		}
	}
	run := func(index int) {
		check := plan.Checks[index]
		check.URL = expandVerifyURL(check.URL, baseURL, port)
		if check.Kind != "command" && check.URL == "" && baseURL != "" {
			check.URL = baseURL
		}
		results[index] = t.runCheck(ctx, check)
	}
	if !plan.Parallel {
		for index := range plan.Checks {
			run(index)
		}
		return results
	}
	sem := make(chan struct{}, MaxVerifyConcurrency)
	var wg sync.WaitGroup
	for index := range plan.Checks {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Ctx.Done():
				results[index] = verifyCheckResult{
					Kind: plan.Checks[index].Kind, Name: plan.Checks[index].Name,
					Status: "FAIL", FailureClass: agent.ToolFailureCancelled,
					Detail: "verification cancelled before the check started",
				}
				return
			}
			run(index)
		}()
	}
	wg.Wait()
	return results
}

func expandVerifyURL(raw, baseURL, port string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.ReplaceAll(raw, "{url}", strings.TrimSuffix(baseURL, "/"))
	raw = strings.ReplaceAll(raw, "{port}", port)
	return raw
}

type verifyCheckResult struct {
	Kind         string
	Name         string
	Target       string
	Status       string
	FailureClass string
	Detail       string
	Duration     time.Duration
	HTTPStatus   int
	Bytes        int
	Command      string
	ExitCode     int
	Output       string
	Verification *agent.VerificationEvidence
	Mutated      bool
}

func (t *VerifyTool) runCheck(ctx agent.Context, check verifyCheckPlan) verifyCheckResult {
	timeout := check.Timeout
	if timeout <= 0 {
		timeout = DefaultVerifyCheckTimeout
	}
	started := time.Now()
	result := verifyCheckResult{Kind: check.Kind, Name: check.Name, Command: check.Command}
	switch check.Kind {
	case "http":
		if strings.TrimSpace(check.URL) == "" {
			result.Status = "FAIL"
			result.FailureClass = agent.ToolFailureVerification
			result.Detail = "HTTP check requires a URL or a service with a discovered URL"
			return result
		}
		observation, err := probeHTTP(ctx.Ctx, check.URL, DefaultVerifyBodyBytes, timeout)
		result.Duration = time.Since(started)
		if err != nil {
			result.Status = "FAIL"
			result.FailureClass = agent.ToolFailureVerification
			result.Detail = err.Error()
			result.Target = check.URL
			return result
		}
		result.Target = observation.URL
		result.HTTPStatus = observation.Status
		result.Bytes = observation.Bytes
		if check.Status != 0 {
			if observation.Status != check.Status {
				result.Status = "FAIL"
				result.FailureClass = agent.ToolFailureVerification
				result.Detail = fmt.Sprintf("status %d, want %d", observation.Status, check.Status)
				return result
			}
		} else if observation.Status < 200 || observation.Status >= 300 {
			result.Status = "FAIL"
			result.FailureClass = agent.ToolFailureVerification
			result.Detail = fmt.Sprintf("status %d is not 2xx", observation.Status)
			return result
		}
		for _, want := range check.Contains {
			if !strings.Contains(observation.Body, want) {
				result.Status = "FAIL"
				result.FailureClass = agent.ToolFailureVerification
				result.Detail = fmt.Sprintf("response does not contain %q", want)
				return result
			}
		}
		result.Status = "PASS"
		result.Detail = fmt.Sprintf("status %d, %d bytes", observation.Status, observation.Bytes)
		return result
	case "browser":
		if strings.TrimSpace(check.URL) == "" {
			result.Status = "UNSUPPORTED"
			result.FailureClass = agent.ToolFailureInsufficient
			result.Detail = "browser check requires a URL or a service with a discovered URL"
			return result
		}
		dom, err := t.runBrowser(ctx.Ctx, check.URL, timeout)
		result.Duration = time.Since(started)
		result.Target = check.URL
		if err != nil {
			result.Status = "UNSUPPORTED"
			result.FailureClass = agent.ToolFailureInsufficient
			result.Detail = err.Error()
			return result
		}
		result.Bytes = len(dom)
		for _, want := range check.Contains {
			if !strings.Contains(dom, want) {
				result.Status = "FAIL"
				result.FailureClass = agent.ToolFailureVerification
				result.Detail = fmt.Sprintf("rendered DOM does not contain %q", want)
				return result
			}
		}
		result.Status = "PASS"
		result.Detail = fmt.Sprintf("rendered DOM %d bytes", len(dom))
		return result
	case "command":
		if strings.TrimSpace(check.Command) == "" {
			result.Status = "FAIL"
			result.FailureClass = agent.ToolFailureInvalidArgument
			result.Detail = "command check requires command"
			return result
		}
		result.Target = check.Command
		bash := NewBashTool(t.Root)
		outcome := bash.Execute(agent.Context{
			Ctx:      ctx.Ctx,
			ToolName: "verify",
			ReadOnly: ctx.ReadOnly,
			Budget:   ctx.Budget,
			Deadline: ctx.Deadline,
		}, map[string]any{
			"command": check.Command,
			"timeout": float64(timeout.Milliseconds()),
		})
		result.Duration = time.Since(started)
		result.ExitCode = verificationExitCode(outcome)
		result.Output = outcome.Text
		result.Mutated = outcome.Mutates
		if outcome.IsError {
			result.Status = "FAIL"
			result.FailureClass = outcome.FailureClass
			if result.FailureClass == "" {
				var exitErr *exec.ExitError
				_ = exitErr
				result.FailureClass = agent.ToolFailureVerification
			}
			result.Detail = "command failed"
			return result
		}
		result.Status = "PASS"
		result.Detail = "command exited 0"
		result.Verification = VerificationForIn(t.Root, check.Command)
		return result
	default:
		result.Status = "UNSUPPORTED"
		result.FailureClass = agent.ToolFailureUnsupported
		result.Detail = fmt.Sprintf("unknown check kind %q", check.Kind)
		return result
	}
}

func (t *VerifyTool) runBrowser(ctx context.Context, rawURL string, timeout time.Duration) (string, error) {
	target, err := loopbackURL(rawURL)
	if err != nil {
		return "", err
	}
	binary, err := t.browserBinary()
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "ohgo-browser-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	args := []string{
		"--headless",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--user-data-dir=" + dir,
		"--virtual-time-budget=5000",
		"--dump-dom",
		target.String(),
	}
	if os.Getenv("OHGO_BROWSER_NO_SANDBOX") == "1" {
		args = append([]string{"--no-sandbox"}, args...)
	}
	cmd := exec.CommandContext(runCtx, binary, args...)
	sandbox.Configure(cmd)
	stdout := newBoundedOutput(MaxVerifyBodyBytes)
	stderr := newBoundedOutput(verifyEvidenceTailBytes * 2)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return "", fmt.Errorf("browser timed out: %w", runCtx.Err())
		}
		return "", fmt.Errorf("browser failed: %v: %s", err, stderr.Tail(verifyEvidenceTailBytes))
	}
	dom := stdout.String()
	if strings.TrimSpace(dom) == "" {
		return "", errors.New("browser produced no DOM")
	}
	return dom, nil
}

func (t *VerifyTool) browserBinary() (string, error) {
	if override := strings.TrimSpace(t.BrowserPath); override != "" {
		return override, nil
	}
	for _, env := range []string{"OHGO_CHROMIUM", "OHGO_BROWSER"} {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			return value, nil
		}
	}
	candidates := []string{
		"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome", "msedge",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no headless browser is available; browser verification is optional and was not run")
}

// verificationExitCode reads the exit code BashTool attached to a command
// result.
func verificationExitCode(result agent.Result) int {
	details, ok := result.Details.(map[string]any)
	if !ok {
		return 0
	}
	switch value := details["exitCode"].(type) {
	case int:
		return value
	case float64:
		return int(value)
	}
	return 0
}

type verificationOutcome struct {
	Status       string
	FailureClass string
	Detail       string
}

func verificationVerdict(results []verifyCheckResult) verificationOutcome {
	outcome := verificationOutcome{Status: "PASS"}
	for _, result := range results {
		switch result.Status {
		case "FAIL":
			outcome.Status = "FAIL"
			outcome.FailureClass = result.FailureClass
			if outcome.FailureClass == "" {
				outcome.FailureClass = agent.ToolFailureVerification
			}
			outcome.Detail = fmt.Sprintf("%s check failed: %s", result.Kind, result.Detail)
			return outcome
		case "UNSUPPORTED":
			if outcome.Status == "PASS" {
				outcome.Status = "INSUFFICIENT"
				outcome.FailureClass = result.FailureClass
				if outcome.FailureClass == "" {
					outcome.FailureClass = agent.ToolFailureInsufficient
				}
				outcome.Detail = fmt.Sprintf("%s check was not run: %s", result.Kind, result.Detail)
			}
		}
	}
	return outcome
}

func anyCommandCheck(results []verifyCheckResult) bool {
	for _, result := range results {
		if result.Kind == "command" {
			return true
		}
	}
	return false
}

func anyCheckMutated(results []verifyCheckResult) bool {
	for _, result := range results {
		if result.Mutated {
			return true
		}
	}
	return false
}

func verificationEvidence(plan verifyPlan, results []verifyCheckResult) *agent.VerificationEvidence {
	evidence := &agent.VerificationEvidence{Check: contract.CheckAny}
	if plan.Service != nil {
		evidence.Command = plan.Service.Command
	}
	bestRank := -1
	for _, result := range results {
		if result.Verification != nil {
			rank := verificationCheckRank(result.Verification.EffectiveCheck())
			if rank > bestRank {
				bestRank = rank
				evidence = result.Verification
			}
			continue
		}
		if result.Kind == "command" && result.Command != "" && bestRank < 0 {
			evidence.Command = result.Command
		}
	}
	return evidence
}

// verificationCheckRank prefers the strongest check a plan produced. A test is
// stronger than a build, which is stronger than an untyped behavioural check.
func verificationCheckRank(check contract.VerificationCheck) int {
	switch check {
	case contract.CheckTest:
		return 3
	case contract.CheckBuild:
		return 2
	case contract.CheckVet:
		return 1
	default:
		return 0
	}
}

func renderVerification(plan verifyPlan, service *verifyService, results []verifyCheckResult, verdict verificationOutcome, cleanup string, elapsed time.Duration) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "verification: %s (%s)\n", verdict.Status, elapsed.Round(time.Millisecond))
	if service != nil {
		state := service.stateValue()
		fmt.Fprintf(&sb, "service: %s (pid %d, state %s", plan.Service.Command, service.pid(), state)
		if discovered, ok := discoverServiceURL(service.output.String()); ok {
			fmt.Fprintf(&sb, ", advertised %s", discovered)
		}
		sb.WriteString(")\n")
	}
	for _, result := range results {
		label := result.Name
		if strings.TrimSpace(label) == "" {
			label = result.Kind
		}
		fmt.Fprintf(&sb, "- %s: %s", label, result.Status)
		if result.Target != "" && result.Target != result.Kind {
			fmt.Fprintf(&sb, " %s", result.Target)
		}
		if result.Detail != "" {
			fmt.Fprintf(&sb, " — %s", result.Detail)
		}
		if result.Duration > 0 {
			fmt.Fprintf(&sb, " (%s)", result.Duration.Round(time.Millisecond))
		}
		sb.WriteByte('\n')
		if result.Status == "FAIL" && result.Output != "" {
			fmt.Fprintf(&sb, "  output: %s\n", firstNonEmptyLine(result.Output, 400))
		}
	}
	fmt.Fprintf(&sb, "cleanup: %s\n", cleanup)
	if verdict.Status != "PASS" && verdict.Detail != "" {
		fmt.Fprintf(&sb, "review: %s\n", verdict.Detail)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func verificationDetails(plan verifyPlan, service *verifyService, results []verifyCheckResult, verdict verificationOutcome, cleanup string, elapsed time.Duration) map[string]any {
	out := map[string]any{
		"status":      verdict.Status,
		"cleanup":     cleanup,
		"duration":    elapsed.String(),
		"checks":      len(results),
		"passed":      0,
		"failed":      0,
		"unsupported": 0,
	}
	for _, result := range results {
		switch result.Status {
		case "PASS":
			out["passed"] = out["passed"].(int) + 1
		case "FAIL":
			out["failed"] = out["failed"].(int) + 1
		case "UNSUPPORTED":
			out["unsupported"] = out["unsupported"].(int) + 1
		}
	}
	if verdict.Detail != "" {
		out["reason"] = verdict.Detail
	}
	if service != nil {
		service.mu.Lock()
		serviceDetails := map[string]any{
			"command":     plan.Service.Command,
			"workspace":   service.dir,
			"pid":         service.pid(),
			"state":       string(service.state),
			"startedAt":   service.startedAt.Format(time.RFC3339Nano),
			"termination": service.termination,
		}
		if !service.readyAt.IsZero() {
			serviceDetails["readyAt"] = service.readyAt.Format(time.RFC3339Nano)
		}
		if !service.stoppedAt.IsZero() {
			serviceDetails["stoppedAt"] = service.stoppedAt.Format(time.RFC3339Nano)
		}
		if service.exited() {
			serviceDetails["exitCode"] = service.exitCode()
		}
		if tail := service.output.Tail(500); tail != "" {
			serviceDetails["outputTail"] = tail
		}
		service.mu.Unlock()
		if discovered, ok := discoverServiceURL(service.output.String()); ok {
			serviceDetails["url"] = discovered
		}
		out["service"] = serviceDetails
	}
	checkDetails := make([]map[string]any, 0, len(results))
	for _, result := range results {
		item := map[string]any{
			"kind":   result.Kind,
			"status": result.Status,
			"detail": result.Detail,
		}
		if result.Name != "" {
			item["name"] = result.Name
		}
		if result.Target != "" {
			item["target"] = result.Target
		}
		if result.HTTPStatus != 0 {
			item["httpStatus"] = result.HTTPStatus
		}
		if result.Bytes != 0 {
			item["bytes"] = result.Bytes
		}
		if result.Duration > 0 {
			item["duration"] = result.Duration.String()
		}
		if result.FailureClass != "" {
			item["failureClass"] = result.FailureClass
		}
		if result.ExitCode != 0 {
			item["exitCode"] = result.ExitCode
		}
		if result.Command != "" {
			item["command"] = result.Command
		}
		if result.Output != "" && result.Status == "FAIL" {
			item["output"] = result.Output
		}
		checkDetails = append(checkDetails, item)
	}
	out["evidence"] = checkDetails
	return out
}

// verifyFailure builds an early cancellation/failure result with truthful
// execution flags.
func verifyFailure(class, text string, executed, finished bool, details map[string]any) agent.Result {
	return agent.Result{
		Text:         text,
		IsError:      true,
		Executes:     executed,
		Finished:     finished,
		FailureClass: class,
		Details:      details,
	}
}

func parseVerifyPlan(args map[string]any) (verifyPlan, error) {
	var plan verifyPlan
	plan.Parallel = true
	if raw, ok := args["parallel"]; ok {
		value, ok := raw.(bool)
		if !ok {
			return plan, fmt.Errorf("parallel must be a boolean")
		}
		plan.Parallel = value
	}
	if raw, ok := args["service"]; ok && raw != nil {
		service, ok := raw.(map[string]any)
		if !ok {
			return plan, fmt.Errorf("service must be an object")
		}
		parsed, err := parseServicePlan(service)
		if err != nil {
			return plan, err
		}
		plan.Service = &parsed
	}
	if raw, ok := args["checks"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return plan, fmt.Errorf("checks must be an array")
		}
		if len(list) > MaxVerifyChecks {
			return plan, fmt.Errorf("at most %d checks are allowed", MaxVerifyChecks)
		}
		for index, item := range list {
			check, ok := item.(map[string]any)
			if !ok {
				return plan, fmt.Errorf("check %d must be an object", index+1)
			}
			parsed, err := parseCheckPlan(check)
			if err != nil {
				return plan, fmt.Errorf("check %d: %w", index+1, err)
			}
			plan.Checks = append(plan.Checks, parsed)
		}
	}
	if plan.Service == nil && len(plan.Checks) == 0 {
		return plan, fmt.Errorf("a verification plan needs a service or at least one check")
	}
	if len(plan.Checks) == 0 {
		// A service-only plan observes readiness and immediately stops the
		// process. That is not verification evidence, and treating it as one
		// would let "start the server" complete without checking anything.
		return plan, fmt.Errorf("a service plan needs at least one check")
	}
	if plan.Service == nil {
		for _, check := range plan.Checks {
			if check.Kind == "command" {
				continue
			}
			if strings.TrimSpace(check.URL) == "" {
				return plan, fmt.Errorf("%s check requires a URL when no service is declared", check.Kind)
			}
		}
	}
	return plan, nil
}

func parseServicePlan(raw map[string]any) (verifyServicePlan, error) {
	var plan verifyServicePlan
	command, _ := raw["command"].(string)
	command = strings.TrimSpace(command)
	if command == "" {
		return plan, fmt.Errorf("service.command is required")
	}
	if len(command) > 8192 {
		return plan, fmt.Errorf("service.command exceeds 8192 characters")
	}
	plan.Command = command
	if cwd, ok := raw["cwd"].(string); ok {
		plan.Dir = cwd
	}
	plan.Readiness = verifyReadiness{Kind: "http", Timeout: DefaultVerifyReadinessTimeout}
	if rawReadiness, ok := raw["readiness"].(map[string]any); ok {
		if kind, ok := rawReadiness["kind"].(string); ok && strings.TrimSpace(kind) != "" {
			plan.Readiness.Kind = strings.ToLower(strings.TrimSpace(kind))
		}
		if u, ok := rawReadiness["url"].(string); ok {
			plan.Readiness.URL = strings.TrimSpace(u)
		}
		if expect, ok := rawReadiness["expect"].(string); ok {
			plan.Readiness.Expect = expect
		}
		if timeout, ok := rawReadiness["timeout_ms"]; ok {
			parsed, err := milliseconds(timeout)
			if err != nil {
				return plan, fmt.Errorf("service.readiness.timeout_ms: %w", err)
			}
			plan.Readiness.Timeout = parsed
		}
	}
	if plan.Readiness.Timeout > MaxVerifyReadinessTimeout {
		plan.Readiness.Timeout = MaxVerifyReadinessTimeout
	}
	switch plan.Readiness.Kind {
	case "http", "tcp", "output", "process":
	default:
		return plan, fmt.Errorf("unknown readiness kind %q", plan.Readiness.Kind)
	}
	if plan.Readiness.Kind == "output" && strings.TrimSpace(plan.Readiness.Expect) == "" {
		return plan, fmt.Errorf("output readiness requires expect")
	}
	return plan, nil
}

func parseCheckPlan(raw map[string]any) (verifyCheckPlan, error) {
	var plan verifyCheckPlan
	kind, _ := raw["kind"].(string)
	plan.Kind = strings.ToLower(strings.TrimSpace(kind))
	switch plan.Kind {
	case "http", "browser", "command":
	default:
		return plan, fmt.Errorf("unknown kind %q", plan.Kind)
	}
	if name, ok := raw["name"].(string); ok {
		plan.Name = name
	}
	if u, ok := raw["url"].(string); ok {
		plan.URL = strings.TrimSpace(u)
	}
	if status, ok := raw["status"]; ok {
		value, err := integer(status)
		if err != nil || value < 100 || value > 599 {
			return plan, fmt.Errorf("status must be a valid HTTP status")
		}
		plan.Status = value
	}
	if contains, ok := raw["contains"].([]any); ok {
		if len(contains) > 16 {
			return plan, fmt.Errorf("contains has more than 16 entries")
		}
		for _, entry := range contains {
			value, ok := entry.(string)
			if !ok {
				return plan, fmt.Errorf("contains entries must be strings")
			}
			plan.Contains = append(plan.Contains, value)
		}
	}
	if command, ok := raw["command"].(string); ok {
		plan.Command = command
	}
	if timeout, ok := raw["timeout_ms"]; ok {
		parsed, err := milliseconds(timeout)
		if err != nil {
			return plan, fmt.Errorf("timeout_ms: %w", err)
		}
		plan.Timeout = parsed
	}
	if plan.Timeout <= 0 {
		plan.Timeout = DefaultVerifyCheckTimeout
	}
	if plan.Timeout > MaxVerifyCheckTimeout {
		plan.Timeout = MaxVerifyCheckTimeout
	}
	if plan.Kind == "command" && strings.TrimSpace(plan.Command) == "" {
		return plan, fmt.Errorf("command check requires command")
	}
	return plan, nil
}

func milliseconds(raw any) (time.Duration, error) {
	value, err := integer(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return time.Duration(value) * time.Millisecond, nil
}

func integer(raw any) (int, error) {
	switch value := raw.(type) {
	case float64:
		if value != float64(int(value)) {
			return 0, fmt.Errorf("must be an integer")
		}
		return int(value), nil
	case int:
		return value, nil
	case int64:
		return int(value), nil
	default:
		return 0, fmt.Errorf("must be a number")
	}
}

// readBounded reads at most limit bytes. The caller only needs bounded
// evidence, so a larger response is truncated rather than rejected.
func readBounded(r interface{ Read([]byte) (int, error) }, limit int) (string, error) {
	var buf bytes.Buffer
	chunk := make([]byte, 32*1024)
	remaining := limit
	for remaining > 0 {
		n, err := r.Read(chunk)
		if n > 0 {
			take := n
			if take > remaining {
				take = remaining
			}
			buf.Write(chunk[:take])
			remaining -= take
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return buf.String(), err
		}
	}
	return buf.String(), nil
}

func firstNonEmptyLine(text string, max int) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "$ ") {
			continue
		}
		if len(line) > max {
			return line[:max] + "…"
		}
		return line
	}
	return ""
}
