package semantic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Manager errors.
var (
	// ErrUnknownServer reports a name not in the configured roster.
	ErrUnknownServer = errors.New("semantic: unknown language server")
	// ErrServerLimit reports that the bounded server count is exhausted.
	ErrServerLimit = errors.New("semantic: language server limit reached")
	// ErrNoServer reports that no configured server covers a language.
	ErrNoServer = errors.New("semantic: no language server configured for the language")
)

// Spec is one configured language server.
//
// Specs come from composition or user configuration, never from model output:
// the manager launches only these commands, so semantic intelligence cannot
// become an arbitrary process-execution path.
type Spec struct {
	// Name is the stable identity, used for deduplication.
	Name string
	// Languages are the language ids this server serves, e.g. "go".
	Languages []string
	// Command is the executable and arguments.
	Command []string
	// Env is appended to the process environment.
	Env []string
	// InitializationOptions is passed through to the server.
	InitializationOptions json.RawMessage
	// StartTimeout bounds the initialize handshake.
	StartTimeout time.Duration
}

// ServerStatus is the truthful lifecycle projection for the UI.
type ServerStatus struct {
	Name     string
	Language string
	Status   string
	Detail   string
	Error    string
}

// Manager owns language server lifecycles for one workspace.
//
// Servers start lazily on the first request for their language, are
// deduplicated by name and workspace, and are bounded by MaxServers. A crashed
// server is restarted at most maxRestarts times; after that the failure is
// reported instead of looping.
type Manager struct {
	root       string
	specs      []Spec
	maxServers int

	mu       sync.Mutex
	entries  map[string]*managed
	failures map[string]int
	closed   bool
}

type managed struct {
	client   *Client
	restarts int
	lastErr  string
}

// maxRestarts bounds crash recovery per server.
const maxRestarts = 2

// NewManager returns a manager over root. maxServers <= 0 uses 4.
func NewManager(root string, specs []Spec, maxServers int) *Manager {
	if maxServers <= 0 {
		maxServers = 4
	}
	return &Manager{
		root:       root,
		specs:      append([]Spec(nil), specs...),
		maxServers: maxServers,
		entries:    make(map[string]*managed),
		failures:   make(map[string]int),
	}
}

// Servers returns the configured server names in order.
func (m *Manager) Servers() []string {
	names := make([]string, 0, len(m.specs))
	for _, spec := range m.specs {
		names = append(names, spec.Name)
	}
	return names
}

// Client returns a live client for name, starting it if needed.
func (m *Manager) Client(ctx context.Context, name string) (*Client, error) {
	spec, ok := m.spec(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownServer, name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	restarts := m.failures[name]
	if entry, ok := m.entries[name]; ok {
		if entry.client.Alive() {
			return entry.client, nil
		}
		if entry.restarts >= maxRestarts {
			return nil, fmt.Errorf("semantic: server %q crashed %d times: %s", name, entry.restarts, entry.lastErr)
		}
		restarts = entry.restarts + 1
		delete(m.entries, name)
	}
	if len(m.entries) >= m.maxServers {
		return nil, fmt.Errorf("%w: %d already running", ErrServerLimit, len(m.entries))
	}
	client, err := StartClient(ctx, m.root, spec)
	if err != nil {
		restarts++
		m.failures[name] = restarts
		if restarts >= maxRestarts {
			return nil, fmt.Errorf("semantic: server %q crashed %d times: %w", name, restarts, err)
		}
		return nil, err
	}
	delete(m.failures, name)
	m.entries[name] = &managed{client: client, restarts: restarts}
	return client, nil
}

// ForLanguage returns the first server configured for language.
func (m *Manager) ForLanguage(ctx context.Context, language string) (*Client, error) {
	for _, spec := range m.specs {
		for _, served := range spec.Languages {
			if served == language {
				return m.Client(ctx, spec.Name)
			}
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrNoServer, language)
}

// Statuses reports the current lifecycle state without starting anything.
func (m *Manager) Statuses() []ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	statuses := make([]ServerStatus, 0, len(m.specs))
	for _, spec := range m.specs {
		row := ServerStatus{Name: spec.Name, Language: joinLanguages(spec.Languages), Status: "stopped"}
		if entry, ok := m.entries[spec.Name]; ok {
			if entry.client != nil && entry.client.Alive() {
				caps := entry.client.Capabilities()
				row.Status = "ready"
				row.Detail = capabilitySummary(caps)
			} else {
				row.Status = "error"
				row.Error = entry.lastErr
			}
		}
		statuses = append(statuses, row)
	}
	sort.SliceStable(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

// Close stops every started server.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	clients := make([]*Client, 0, len(m.entries))
	for _, entry := range m.entries {
		if entry.client != nil {
			clients = append(clients, entry.client)
		}
	}
	m.entries = make(map[string]*managed)
	m.mu.Unlock()
	var first error
	for _, client := range clients {
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *Manager) spec(name string) (Spec, bool) {
	for _, spec := range m.specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}

func joinLanguages(languages []string) string {
	result := ""
	for index, language := range languages {
		if index > 0 {
			result += ","
		}
		result += language
	}
	return result
}

func capabilitySummary(caps Capabilities) string {
	parts := make([]string, 0, 6)
	if caps.Definition {
		parts = append(parts, "definition")
	}
	if caps.References {
		parts = append(parts, "references")
	}
	if caps.DocumentSymbols {
		parts = append(parts, "documentSymbol")
	}
	if caps.CallHierarchy {
		parts = append(parts, "callHierarchy")
	}
	if caps.Hover {
		parts = append(parts, "hover")
	}
	if caps.PullDiagnostics {
		parts = append(parts, "diagnostics")
	}
	if len(parts) == 0 {
		return "no negotiated capabilities"
	}
	return "capabilities: " + joinComma(parts)
}

func joinComma(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ", "
		}
		result += value
	}
	return result
}
