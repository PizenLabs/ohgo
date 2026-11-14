package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// State is the explicit debug session lifecycle.
type State string

// Session states. They are ordered by lifecycle, not by severity.
const (
	// StateCreated means the session exists but no handshake has happened.
	StateCreated State = "CREATED"
	// StateInitializing means initialize is in flight.
	StateInitializing State = "INITIALIZING"
	// StateConfiguring means launch/attach and breakpoints are being set.
	StateConfiguring State = "CONFIGURING"
	// StateRunning means the debuggee is executing.
	StateRunning State = "RUNNING"
	// StateStopped means execution is paused at a stop.
	StateStopped State = "STOPPED"
	// StateTerminating means shutdown is in flight.
	StateTerminating State = "TERMINATING"
	// StateTerminated means the debuggee and adapter are finished.
	StateTerminated State = "TERMINATED"
	// StateFailed means setup or the adapter failed before termination.
	StateFailed State = "FAILED"
)

// Action is an authorization-relevant debug operation.
type Action string

// Actions requiring approval. Observation-only requests are not actions.
const (
	// ActionLaunch starts a new debuggee process.
	ActionLaunch Action = "launch"
	// ActionAttach attaches to a running process.
	ActionAttach Action = "attach"
	// ActionEvaluate evaluates an expression in the debuggee.
	ActionEvaluate Action = "evaluate"
	// ActionTerminate terminates the debuggee.
	ActionTerminate Action = "terminate"
)

// Authorizer decides whether an action is allowed. It is the seam the control
// plane wires; this package never imports the authority system.
type Authorizer func(ctx context.Context, action Action, detail string) error

// Spec is one configured debug adapter.
type Spec struct {
	// Name is the adapter identity.
	Name string
	// Command is the adapter executable and arguments.
	Command []string
	// Env is appended to the environment.
	Env []string
	// Dir is the adapter working directory (usually the workspace root).
	Dir string
}

// Config configures a Manager.
type Config struct {
	// Adapters are the only adapters that may be started.
	Adapters map[string]Spec
	// MaxSessions bounds concurrent debug sessions. Zero uses 2.
	MaxSessions int
	// Authorize gates launch, attach, evaluate, and terminate. Nil denies
	// those actions, never silently allows them.
	Authorize Authorizer
	// LaunchTimeout bounds initialize plus launch. Zero uses 20s.
	LaunchTimeout time.Duration
	// Grace bounds disconnect and adapter reaping. Zero uses 3s.
	Grace time.Duration
}

// Session is one authorized debug session.
type Session struct {
	// ID is the stable session identity.
	ID string
	// Root is the workspace root used for source containment.
	Root string
	// Adapter is the configured adapter name.
	Adapter string
	// Fingerprint is caller-supplied build identity, echoed as provenance.
	Fingerprint string
	// Started is when the adapter handshake began.
	Started time.Time

	client    *client
	authorize Authorizer
	caps      Capabilities
	grace     time.Duration

	mu        sync.Mutex
	state     State
	processID int
	ended     time.Time
	events    chan Event
}

func newSession(id, root, adapter, fingerprint string, authorize Authorizer, grace time.Duration) *Session {
	return &Session{
		ID: id, Root: root, Adapter: adapter, Fingerprint: fingerprint,
		authorize: authorize, grace: grace, Started: time.Now(),
		state: StateCreated, events: make(chan Event, 256),
	}
}

// State reports the current lifecycle state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// ProcessID reports the debuggee process id when the adapter announced one.
func (s *Session) ProcessID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processID
}

// Ended reports when the session reached a terminal state, if it did.
func (s *Session) Ended() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// Capabilities reports the negotiated adapter capabilities.
func (s *Session) Capabilities() Capabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caps
}

// Events returns the observation stream. A consumer that falls behind drops
// events rather than blocking the adapter read loop; the session state remains
// authoritative.
func (s *Session) Events() <-chan Event { return s.events }

// Alive reports whether the adapter process is still connected.
func (s *Session) Alive() bool {
	if s.client == nil {
		return false
	}
	return s.client.alive()
}

// Stderr returns bounded adapter diagnostics for a failure explanation.
func (s *Session) Stderr() string {
	if s.client == nil {
		return ""
	}
	return s.client.stderr.String()
}

// WaitEvent waits for one named event, consuming unrelated events.
func (s *Session) WaitEvent(ctx context.Context, name string) (Event, error) {
	for {
		select {
		case event := <-s.events:
			if event.Event == name {
				return event, nil
			}
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Event{}, fmt.Errorf("%w: waiting for %s", ErrTimeout, name)
			}
			return Event{}, ctx.Err()
		}
	}
}

// setState applies one lifecycle transition under the session lock.
func (s *Session) setState(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateTerminated || s.state == StateFailed {
		return
	}
	s.state = state
	if state == StateTerminated || state == StateFailed {
		s.ended = time.Now()
	}
}

// onEvent applies adapter events to state and publishes them.
func (s *Session) onEvent(event Event) {
	switch event.Event {
	case "process":
		var body struct {
			SystemProcessID int `json:"systemProcessId"`
			ProcessID       int `json:"processId"`
		}
		if json.Unmarshal(event.Body, &body) == nil {
			id := body.SystemProcessID
			if id == 0 {
				id = body.ProcessID
			}
			if id > 0 {
				s.mu.Lock()
				s.processID = id
				s.mu.Unlock()
			}
		}
	case "stopped":
		s.setState(StateStopped)
	case "continued":
		s.setState(StateRunning)
	case "terminated", "exited":
		s.setState(StateTerminated)
	}
	select {
	case s.events <- event:
	default:
		// Bounded stream: observation loss must not stall execution.
	}
}

// Threads lists debuggee threads.
func (s *Session) Threads(ctx context.Context) ([]Thread, error) {
	var body struct {
		Threads []Thread `json:"threads"`
	}
	if err := s.request(ctx, "threads", nil, &body); err != nil {
		return nil, err
	}
	if len(body.Threads) > 200 {
		body.Threads = body.Threads[:200]
	}
	return body.Threads, nil
}

// StackTrace lists frames for threadID.
func (s *Session) StackTrace(ctx context.Context, threadID, levels int) ([]Frame, error) {
	if levels <= 0 {
		levels = 20
	}
	if levels > 100 {
		levels = 100
	}
	var body struct {
		StackFrames []Frame `json:"stackFrames"`
	}
	if err := s.request(ctx, "stackTrace", map[string]any{
		"threadId": threadID, "startFrame": 0, "levels": levels,
	}, &body); err != nil {
		return nil, err
	}
	return body.StackFrames, nil
}

// Scopes lists variable scopes for frameID.
func (s *Session) Scopes(ctx context.Context, frameID int) ([]Scope, error) {
	var body struct {
		Scopes []Scope `json:"scopes"`
	}
	if err := s.request(ctx, "scopes", map[string]any{"frameId": frameID}, &body); err != nil {
		return nil, err
	}
	if len(body.Scopes) > 50 {
		body.Scopes = body.Scopes[:50]
	}
	return body.Scopes, nil
}

// Variables lists variables for reference.
func (s *Session) Variables(ctx context.Context, reference int) ([]Variable, error) {
	var body struct {
		Variables []Variable `json:"variables"`
	}
	if err := s.request(ctx, "variables", map[string]any{
		"variablesReference": reference, "start": 0, "count": 200,
	}, &body); err != nil {
		return nil, err
	}
	if len(body.Variables) > 200 {
		body.Variables = body.Variables[:200]
	}
	return body.Variables, nil
}

// Continue resumes the debuggee.
func (s *Session) Continue(ctx context.Context, threadID int) error {
	return s.request(ctx, "continue", map[string]any{"threadId": threadID}, nil)
}

// Next steps over one line.
func (s *Session) Next(ctx context.Context, threadID int) error {
	return s.request(ctx, "next", map[string]any{"threadId": threadID}, nil)
}

// StepIn steps into a call.
func (s *Session) StepIn(ctx context.Context, threadID int) error {
	return s.request(ctx, "stepIn", map[string]any{"threadId": threadID}, nil)
}

// StepOut steps out of a call.
func (s *Session) StepOut(ctx context.Context, threadID int) error {
	return s.request(ctx, "stepOut", map[string]any{"threadId": threadID}, nil)
}

// Pause interrupts the debuggee.
func (s *Session) Pause(ctx context.Context, threadID int) error {
	return s.request(ctx, "pause", map[string]any{"threadId": threadID}, nil)
}

// Evaluate evaluates expression in frameID.
//
// Evaluation can call user code, so it requires explicit authorization and is
// bounded. A denial returns the authorizer's error without any adapter traffic.
func (s *Session) Evaluate(ctx context.Context, expression string, frameID int) (EvaluateResult, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return EvaluateResult{}, errors.New("runtime: evaluate needs a non-empty expression")
	}
	if len(expression) > 2048 {
		return EvaluateResult{}, errors.New("runtime: evaluate expression exceeds 2048 characters")
	}
	if s.authorize != nil {
		if err := s.authorize(ctx, ActionEvaluate, expression); err != nil {
			return EvaluateResult{}, fmt.Errorf("runtime: evaluate denied: %w", err)
		}
	}
	var body EvaluateResult
	if err := s.request(ctx, "evaluate", map[string]any{
		"expression": expression, "frameId": frameID, "context": "watch",
	}, &body); err != nil {
		return EvaluateResult{}, err
	}
	return body, nil
}

// ExceptionInfo returns the exception state for threadID.
func (s *Session) ExceptionInfo(ctx context.Context, threadID int) (json.RawMessage, error) {
	if !s.Capabilities().SupportsExceptionInfoRequest {
		return nil, ErrUnsupported
	}
	var body json.RawMessage
	if err := s.request(ctx, "exceptionInfo", map[string]any{"threadId": threadID}, &body); err != nil {
		return nil, err
	}
	return body, nil
}

// SetBreakpoints sets breakpoints in one source file.
//
// Paths outside the session root are rejected before any request: a debugger
// must not become a path-escape mechanism.
func (s *Session) SetBreakpoints(ctx context.Context, group SourceBreakpoints) ([]Breakpoint, error) {
	if len(group.Breakpoints) == 0 {
		return nil, errors.New("runtime: breakpoint group is empty")
	}
	if len(group.Breakpoints) > 100 {
		return nil, errors.New("runtime: at most 100 breakpoints per source")
	}
	path := group.Path
	if path == "" {
		path = group.Name
	}
	if !s.containsPath(path) {
		return nil, fmt.Errorf("runtime: breakpoint source %q is outside the workspace", path)
	}
	var body struct {
		Breakpoints []Breakpoint `json:"breakpoints"`
	}
	if err := s.request(ctx, "setBreakpoints", map[string]any{
		"source":      map[string]any{"name": filepath.Base(path), "path": path},
		"breakpoints": group.Breakpoints,
		"lines":       nil,
	}, &body); err != nil {
		return nil, err
	}
	return body.Breakpoints, nil
}

// Terminate asks the adapter to terminate the debuggee when supported.
func (s *Session) Terminate(ctx context.Context) error {
	if !s.Capabilities().SupportsTerminateRequest {
		return ErrUnsupported
	}
	if s.authorize != nil {
		if err := s.authorize(ctx, ActionTerminate, s.ID); err != nil {
			return fmt.Errorf("runtime: terminate denied: %w", err)
		}
	}
	s.setState(StateTerminating)
	return s.request(ctx, "terminate", map[string]any{}, nil)
}

// Close disconnects and reaps the adapter. It is the safe cleanup path and does
// not need approval: refusing cleanup would strand processes.
func (s *Session) Close() error {
	if s.client == nil {
		return nil
	}
	s.setState(StateTerminating)
	ctx, cancel := context.WithTimeout(context.Background(), s.grace)
	err := s.client.request(ctx, "disconnect", map[string]any{
		"terminateDebuggee": true,
	}, nil)
	cancel()
	if err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrTimeout) {
		// The adapter may already be gone; reaping still proceeds.
		_ = err
	}
	_ = s.client.closeAdapter(s.grace)
	if s.State() != StateFailed {
		s.setState(StateTerminated)
	}
	return nil
}

// request forwards one typed command.
func (s *Session) request(ctx context.Context, command string, arguments any, result any) error {
	if s.client == nil {
		return ErrClosed
	}
	return s.client.request(ctx, command, arguments, result)
}

// containsPath reports whether path stays inside the session root.
func (s *Session) containsPath(path string) bool {
	if path == "" || s.Root == "" {
		return false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Manager errors.
var (
	// ErrSessionLimit reports the configured concurrent-session bound.
	ErrSessionLimit = errors.New("runtime: debug session limit reached")
	// ErrUnknownAdapter reports an adapter not present in configuration.
	ErrUnknownAdapter = errors.New("runtime: unknown debug adapter")
	// ErrManagerClosed reports a closed manager.
	ErrManagerClosed = errors.New("runtime: debug manager is closed")
)

// Manager owns bounded, authorized debug sessions for one workspace.
type Manager struct {
	root   string
	config Config

	mu       sync.Mutex
	nextID   int
	starting int
	sessions map[string]*Session
	closed   bool
}

// NewManager returns a debug manager over root.
func NewManager(root string, config Config) *Manager {
	if config.MaxSessions <= 0 {
		config.MaxSessions = 2
	}
	if config.LaunchTimeout <= 0 {
		config.LaunchTimeout = 20 * time.Second
	}
	if config.Grace <= 0 {
		config.Grace = 3 * time.Second
	}
	return &Manager{root: root, config: config, sessions: make(map[string]*Session)}
}

// LaunchRequest is one launch or attach request.
type LaunchRequest struct {
	// Adapter is the configured adapter name.
	Adapter string
	// Attach selects attach instead of launch.
	Attach bool
	// Arguments is the adapter-specific launch/attach body.
	Arguments json.RawMessage
	// Breakpoints are set after the initialized event.
	Breakpoints []SourceBreakpoints
	// Fingerprint identifies the built artifact under observation.
	Fingerprint string
	// Timeout bounds initialize plus launch for this request.
	Timeout time.Duration
}

// Launch authorizes, starts, and configures one debug session.
func (m *Manager) Launch(ctx context.Context, request LaunchRequest) (*Session, error) {
	action := ActionLaunch
	if request.Attach {
		action = ActionAttach
	}
	if m.config.Authorize == nil {
		return nil, fmt.Errorf("runtime: %s denied: no authorizer is configured", action)
	}
	if err := m.config.Authorize(ctx, action, request.Adapter); err != nil {
		return nil, fmt.Errorf("runtime: %s denied: %w", action, err)
	}
	spec, ok := m.config.Adapters[request.Adapter]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAdapter, request.Adapter)
	}
	if spec.Dir == "" {
		spec.Dir = m.root
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	if len(m.sessions)+m.starting >= m.config.MaxSessions {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %d active", ErrSessionLimit, len(m.sessions)+m.starting)
	}
	m.nextID++
	id := fmt.Sprintf("dap-%d", m.nextID)
	m.starting++
	m.mu.Unlock()

	release := func() {
		m.mu.Lock()
		m.starting--
		m.mu.Unlock()
	}

	session := newSession(id, m.root, request.Adapter, request.Fingerprint, m.config.Authorize, m.config.Grace)
	client, err := startClient(spec, session.onEvent)
	if err != nil {
		release()
		session.setState(StateFailed)
		return nil, err
	}
	session.client = client
	session.setState(StateInitializing)
	session.watchExit()

	timeout := request.Timeout
	if timeout <= 0 {
		timeout = m.config.LaunchTimeout
	}
	setupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	caps, err := client.initialize(setupCtx, request.Adapter)
	if err != nil {
		session.fail()
		release()
		return nil, err
	}
	session.mu.Lock()
	session.caps = caps
	session.mu.Unlock()
	session.setState(StateConfiguring)

	command := "launch"
	if request.Attach {
		command = "attach"
	}
	arguments := request.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage("{}")
	}
	if err := client.request(setupCtx, command, arguments, nil); err != nil {
		session.fail()
		release()
		return nil, err
	}
	// Some adapters answer launch before sending initialized; others send it
	// first. The event is buffered either way.
	if _, err := session.WaitEvent(setupCtx, "initialized"); err != nil {
		session.fail()
		release()
		return nil, err
	}
	for _, group := range request.Breakpoints {
		if _, err := session.SetBreakpoints(setupCtx, group); err != nil {
			session.fail()
			release()
			return nil, err
		}
	}
	if err := client.request(setupCtx, "configurationDone", map[string]any{}, nil); err != nil {
		session.fail()
		release()
		return nil, err
	}
	session.startRunning()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = session.Close()
		release()
		return nil, ErrManagerClosed
	}
	m.sessions[id] = session
	m.starting--
	m.mu.Unlock()
	return session, nil
}

// Sessions returns the active sessions in creation order.
func (m *Manager) Sessions() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	sortStrings(ids)
	out := make([]*Session, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.sessions[id])
	}
	return out
}

// Close reaps every session.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
	return nil
}

// startRunning promotes CONFIGURING to RUNNING without overwriting a stop that
// arrived during configuration.
func (s *Session) startRunning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateConfiguring {
		s.state = StateRunning
	}
}

// watchExit marks an unexpected adapter death as FAILED. A normal termination
// already set TERMINATED, and cleanup from Close sets TERMINATING first.
func (s *Session) watchExit() {
	client := s.client
	if client == nil {
		return
	}
	go func() {
		<-client.done
		s.mu.Lock()
		if s.state != StateTerminated && s.state != StateTerminating && s.state != StateFailed {
			s.state = StateFailed
			s.ended = time.Now()
		}
		s.mu.Unlock()
	}()
}

// fail closes the adapter and records a failed setup.
func (s *Session) fail() {
	if s.client != nil {
		_ = s.client.closeAdapter(s.grace)
	}
	s.setState(StateFailed)
}

// alive reports whether the adapter stream is usable.
func (c *client) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}

// sortStrings keeps session ordering deterministic without importing sort at
// call sites.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
