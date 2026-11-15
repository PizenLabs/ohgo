package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/prompt"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A session is a sequence of provider requests that repeat each other. The
// provider caches a request prefix, so the only cache property Ohgo controls is
// whether the next request starts with the bytes the previous one sent. These
// tests drive the production loop, prompt builder, and cognitive projector to
// measure exactly that.

// requestWire is the order-sensitive shape of one outbound request, rendered in
// the order a chat transport consumes it: tool definitions, then the system
// prompt, then the conversation. Tool definitions travel first on every
// supported protocol, so a divergence inside the conversation still leaves them
// cached — and a changed tool block invalidates everything behind it.
type requestWire struct {
	Tools    []string `json:"tools"`
	System   []string `json:"system"`
	Messages []string `json:"messages"`
}

func wireOf(req ai.Request) requestWire {
	wire := requestWire{}
	for _, block := range req.System {
		wire.System = append(wire.System, block.Text)
	}
	for _, message := range req.Messages {
		switch typed := message.(type) {
		case ai.UserMessage:
			wire.Messages = append(wire.Messages, "user\x00"+typed.Text())
		case ai.AssistantMessage:
			wire.Messages = append(wire.Messages, "assistant\x00"+assistantWire(typed))
		case ai.ToolResultMessage:
			wire.Messages = append(wire.Messages, "tool\x00"+typed.ToolCallID+"\x00"+typed.Text())
		case ai.RuntimeStateMessage:
			wire.Messages = append(wire.Messages, "runtime\x00"+typed.Text)
		}
	}
	for _, spec := range req.Tools {
		wire.Tools = append(wire.Tools, spec.Name+"\x00"+spec.Description+"\x00"+string(spec.Parameters))
	}
	return wire
}

func assistantWire(msg ai.AssistantMessage) string {
	var sb strings.Builder
	for _, block := range msg.Content {
		switch typed := block.(type) {
		case ai.TextContent:
			sb.WriteString(typed.Text)
		case ai.ToolCall:
			args, _ := json.Marshal(typed.Arguments)
			sb.WriteString(typed.Name + "(" + string(args) + ")")
		}
	}
	return sb.String()
}

func (w requestWire) bytes() []byte {
	data, err := json.Marshal(w)
	if err != nil {
		panic(err)
	}
	return data
}

// reusedPrefix returns how many leading bytes two wires share.
func reusedPrefix(previous, current []byte) int {
	limit := min(len(previous), len(current))
	for i := range limit {
		if previous[i] != current[i] {
			return i
		}
	}
	return limit
}

// sessionCost is one request's measured cache locality.
type sessionCost struct {
	// Bytes is the whole request.
	Bytes int
	// Reused is the leading byte count shared with the previous request.
	Reused int
	// New is the bytes no cache can serve: the appended content plus the
	// volatile runtime tail.
	New int
}

// coverage is the share of this request the provider could have served from
// the previous one.
func (c sessionCost) coverage() float64 {
	if c.Bytes == 0 {
		return 0
	}
	return float64(c.Reused) / float64(c.Bytes)
}

const cacheFixture = `package store

import "errors"

var ErrMissing = errors.New("store: missing")

type Store struct {
	data map[string]string
}

func New() *Store { return &Store{data: map[string]string{}} }

func (s *Store) Get(key string) (string, error) {
	value, ok := s.data[key]
	if !ok {
		return "", ErrMissing
	}
	return value, nil
}
`

// cacheFixtureWorkspace writes a small Go project with one large file, so the
// session has both cheap and expensive observations.
func cacheFixtureWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("store.go", cacheFixture)
	write("README.md", "# cache fixture\n\nA key/value store.\n")
	write("main.go", strings.Repeat(cacheFixture, 40))
	return root
}

// maxSessionRequests bounds one harness session so a runaway loop fails loudly
// instead of spinning.
const maxSessionRequests = 16

// sessionHarness drives the production request path and records the wire of
// every request.
type sessionHarness struct {
	loop     *agent.Loop
	script   []ai.AssistantMessage
	index    int
	requests []ai.Request
	wires    []requestWire
	tools    []agent.Tool
}

func newSessionHarness(tb testing.TB, root string, script []ai.AssistantMessage) *sessionHarness {
	tb.Helper()
	h := &sessionHarness{script: script}
	h.tools = []agent.Tool{
		tools.NewReadTool(root),
		tools.NewGlobTool(root),
		tools.NewGrepTool(root),
		tools.NewLsTool(root),
	}
	shape := cognition.BuildShape(contract.IntentContract{
		Objective: "Explain how the store reports a missing key.",
		Kind:      contract.ObjectiveInformational,
	}, cognition.ShapeParams{
		Authority: "ask",
		Facts:     cognition.RepositoryFacts{Go: true, Markers: []string{"go.mod"}},
	})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), cognition.RepositoryFacts{
		Go: true, Markers: []string{"go.mod"},
	})
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "test", Provider: "test", ContextWindow: 200000},
		Stream:   h.stream,
		Tools:    h.tools,
		MaxTurns: 32,
		SystemFn: func() []ai.SystemPromptBlock {
			return promptBlocks(root, prompt.WorkspaceContract("ask"))
		},
		Project: projector.Project,
	})
	if err != nil {
		tb.Fatalf("agent.New: %v", err)
	}
	h.loop = loop
	return h
}

// promptBlocks renders the session's stable system prefix.
func promptBlocks(root, contractText string) []ai.SystemPromptBlock {
	blocks := prompt.Build(root, contractText)
	out := make([]ai.SystemPromptBlock, 0, len(blocks))
	for i, text := range blocks {
		out = append(out, ai.SystemPromptBlock{Text: text, Cache: i == len(blocks)-1})
	}
	return out
}

func (h *sessionHarness) stream(_ context.Context, req ai.Request) (*ai.Stream, error) {
	h.requests = append(h.requests, req)
	h.wires = append(h.wires, wireOf(req))
	if h.index >= len(h.script) {
		// A follow-up turn can need one more request than the scripted
		// sequence; answer it instead of failing, and let the caller bound the
		// total so a runaway loop is still visible.
		h.index++
		return ai.NewCompletedStream(sessionText("Nothing further to observe.")), nil
	}
	message := h.script[h.index]
	h.index++
	if h.index > maxSessionRequests {
		return nil, fmt.Errorf("provider request %d exceeded the session bound", h.index)
	}
	return ai.NewCompletedStream(message), nil
}

// costs returns the per-request locality measurements.
func (h *sessionHarness) costs() []sessionCost {
	out := make([]sessionCost, len(h.wires))
	for i, wire := range h.wires {
		data := wire.bytes()
		out[i].Bytes = len(data)
		if i > 0 {
			out[i].Reused = reusedPrefix(h.wires[i-1].bytes(), data)
		}
		out[i].New = out[i].Bytes - out[i].Reused
	}
	return out
}

// history appends the conversation the session produced, as the TUI does
// between turns: the user request, then every completed turn in order.
func (h *sessionHarness) history(history []agent.Message, user string, turns []agent.Turn) []agent.Message {
	history = append(history, agent.UserMessage{Text: user})
	for _, turn := range turns {
		if turn.Conversational() {
			history = append(history, agent.AssistantMessage{Message: turn.Assistant})
		}
		for _, outcome := range turn.Results {
			history = append(history, agent.ToolResultMessage{
				ToolCallID: outcome.CallID,
				ToolName:   outcome.ToolName,
				Content:    ai.TextBlocks(outcome.Result.Text),
				IsError:    outcome.Result.IsError,
			})
		}
	}
	return history
}

// realSessionScript is the nine-turn shape a real coding session has: an
// initial request, tool observations, an analysis, a follow-up, a role change,
// a continuation, and a compaction in the middle of the work.
func realSessionScript() []ai.AssistantMessage {
	return []ai.AssistantMessage{
		sessionToolCall("c1", "read", map[string]any{"path": "store.go"}),
		sessionToolCall("c2", "read", map[string]any{"path": "README.md"}),
		sessionText("The store returns ErrMissing. I will check the rest of the package."),
		sessionToolCall("c3", "glob", map[string]any{"pattern": "*.go"}),
		sessionToolCall("c4", "grep", map[string]any{"pattern": "ErrMissing"}),
		sessionText("ErrMissing is declared in store.go and returned from Get."),
		sessionToolCall("c5", "read", map[string]any{"path": "main.go"}),
		sessionText("The main package wires Store.Get through the same error path."),
		sessionText("Everything needed to answer is observed."),
	}
}

func sessionToolCall(id, name string, args map[string]any) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: args}},
		StopReason: ai.StopToolUse,
	}
}

func sessionText(value string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: value}},
		StopReason: ai.StopEndTurn,
	}
}

// TestSessionRequestsAreAppendOnly is the cache-locality contract.
//
// Every request must repeat the previous one except for the bytes it genuinely
// adds plus its volatile runtime tail. The failure a consumer sees otherwise is
// the provider re-reading the whole conversation on each turn — the exact loss
// this guards against.
func TestSessionRequestsAreAppendOnly(t *testing.T) {
	root := cacheFixtureWorkspace(t)
	h := newSessionHarness(t, root, realSessionScript())
	ctx := context.Background()
	var history []agent.Message
	for turn := range 9 {
		h.loop.SetRuntimeContext([]string{
			fmt.Sprintf("# Runtime state (authoritative, not a user request)\n\nObjective: explain the store\nTurn: %d\n", turn),
		})
		turns, err := h.loop.Run(ctx, append(append([]agent.Message(nil), history...), agent.UserMessage{Text: "Explain how the store reports a missing key."}))
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		history = h.history(history, "Explain how the store reports a missing key.", turns)
	}

	if len(h.wires) < 6 {
		t.Fatalf("requests = %d, want a multi-request session", len(h.wires))
	}
	costs := h.costs()
	for i := 1; i < len(costs); i++ {
		previous, current := h.wires[i-1], h.wires[i]
		if strings.Join(previous.System, "\x00") != strings.Join(current.System, "\x00") {
			t.Fatalf("request %d changed the system prefix:\n previous %q\n current  %q",
				i, previous.System, current.System)
		}
		if strings.Join(previous.Tools, "\x00") != strings.Join(current.Tools, "\x00") {
			t.Fatal("request changed the tool definitions")
		}
		// The previous request's last message is its runtime tail: it is
		// rebuilt per request by design and is the only region allowed to
		// differ. Every message before it must repeat byte for byte.
		stable := previous.Messages[:len(previous.Messages)-1]
		if len(current.Messages) < len(stable) {
			t.Fatalf("request %d rewound the conversation: %d < %d messages",
				i, len(current.Messages), len(stable))
		}
		for j := range stable {
			if current.Messages[j] != stable[j] {
				t.Fatalf("request %d rewrote already-sent message %d:\n previous %q\n current  %q",
					i, j, stable[j], current.Messages[j])
			}
		}
		t.Logf("request %d: %6d bytes, %6d reused (%.1f%%), %5d new", i, costs[i].Bytes, costs[i].Reused, costs[i].coverage()*100, costs[i].New)
	}

	// The stable prefix itself never changes across the session: the same
	// system prompt and the same tool definitions, in the same order.
	first := h.wires[0]
	last := h.wires[len(h.wires)-1]
	if strings.Join(first.System, "\x00") != strings.Join(last.System, "\x00") {
		t.Fatal("the system prefix changed during a session with one stable semantic state")
	}
	if strings.Join(first.Tools, "\x00") != strings.Join(last.Tools, "\x00") {
		t.Fatal("the tool definitions changed during a session with one stable semantic state")
	}
}

// BenchmarkSessionCacheLocality measures the request cost and the reusable share
// of a realistic session. It reports both so a raw token reduction cannot be
// mistaken for better cache reuse.
func BenchmarkSessionCacheLocality(b *testing.B) {
	for b.Loop() {
		root := b.TempDir()
		if err := os.WriteFile(filepath.Join(root, "store.go"), []byte(cacheFixture), 0o644); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Repeat(cacheFixture, 40)), 0o644); err != nil {
			b.Fatal(err)
		}
		h := newSessionHarness(b, root, realSessionScript())
		ctx := context.Background()
		var history []agent.Message
		var total, reused int
		for turn := range 9 {
			h.loop.SetRuntimeContext([]string{
				fmt.Sprintf("# Runtime state (authoritative, not a user request)\n\nObjective: explain the store\nTurn: %d\n", turn),
			})
			turns, err := h.loop.Run(ctx, append(append([]agent.Message(nil), history...), agent.UserMessage{Text: "Explain how the store reports a missing key."}))
			if err != nil {
				b.Fatalf("turn %d: %v", turn, err)
			}
			history = h.history(history, "Explain how the store reports a missing key.", turns)
		}
		for _, cost := range h.costs() {
			total += cost.Bytes
			reused += cost.Reused
		}
		b.ReportMetric(float64(total)/float64(len(h.costs())), "bytes/request")
		b.ReportMetric(float64(reused)*100/float64(total), "reused%")
	}
}

// Compaction is the one deliberate prefix break. It must reduce the transient
// history while leaving the stable prefix — system prompt and tool definitions —
// and the durable runtime orientation untouched, and it must be reported as an
// expected regression rather than blamed on the provider.
func TestCompactionPreservesTheStablePrefixAndIsExpected(t *testing.T) {
	root := cacheFixtureWorkspace(t)
	usage := &agent.UsageLog{}
	summary := "SUMMARY: the store returns ErrMissing from Get."
	var requests []ai.Request
	script := []ai.AssistantMessage{
		{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "main.go"}}},
			StopReason: ai.StopToolUse,
			Usage:      ai.Usage{Input: 1700, CacheRead: 1500, Output: 40, Total: 3240},
		},
		{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "compacted answer"}},
			StopReason: ai.StopEndTurn,
			Usage:      ai.Usage{Input: 2400, CacheRead: 600, Output: 20, Total: 3020},
		},
	}
	index := 0
	compacted := false
	loop, err := agent.New(agent.Config{
		Model:      ai.Model{ID: "test", Provider: "test", ContextWindow: 12000},
		MaxTokens:  256,
		MaxTurns:   8,
		Usage:      usage,
		Tools:      []agent.Tool{tools.NewReadTool(root)},
		SystemFn:   func() []ai.SystemPromptBlock { return promptBlocks(root, prompt.WorkspaceContract("ask")) },
		Compaction: agent.Compaction{Enabled: true, Strategy: agent.StrategySummarize, ThresholdTokens: 2500, KeepRecent: 1},
		Summarize: func(context.Context, []agent.Message) (string, error) {
			compacted = true
			return summary, nil
		},
		Project: func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{Runtime: []string{"DURABLE TASK ORIENTATION"}}
		},
		OnCompactionMetrics: func(agent.CompactionMetrics) {},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			requests = append(requests, req)
			if index >= len(script) {
				return ai.NewCompletedStream(sessionText("done")), nil
			}
			message := script[index]
			index++
			return ai.NewCompletedStream(message), nil
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "explain the store"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !compacted {
		t.Fatalf("the session did not compact; requests = %d", len(requests))
	}
	if len(requests) < 2 {
		t.Fatalf("requests = %d, want a request before and after compaction", len(requests))
	}
	first, last := requests[0], requests[len(requests)-1]
	if strings.Join(toolWire(first.Tools), "\x00") != strings.Join(toolWire(last.Tools), "\x00") {
		t.Fatal("compaction rewrote the tool definitions")
	}
	if strings.Join(systemWire(first.System), "\x00") != strings.Join(systemWire(last.System), "\x00") {
		t.Fatal("compaction rewrote the system prefix")
	}
	post := wireOf(last)
	if !strings.Contains(strings.Join(post.Messages, "\n"), summary) {
		t.Fatal("the compacted context did not carry the durable summary")
	}
	if tail := post.Messages[len(post.Messages)-1]; !strings.Contains(tail, "runtime\x00DURABLE TASK ORIENTATION") {
		t.Fatalf("the durable runtime orientation was lost: %q", tail)
	}
	// The regression verdict must name the rewrite instead of reporting a
	// provider fault: the request changed on purpose.
	regression, current, ok := usage.CacheRegression()
	if !ok {
		t.Fatal("the loop did not report cache samples")
	}
	if current.Shape.Rewrites != 0 {
		t.Fatalf("compaction was recorded as %d suppressed rewrites, want a deliberate rewind", current.Shape.Rewrites)
	}
	if regression.Class != agent.CacheExpected || !strings.Contains(regression.Detail, "rewind") {
		t.Fatalf("regression = %+v, want an EXPECTED rewind", regression)
	}
}

// toolWire renders tool definitions for comparison.
func toolWire(specs []ai.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name+spec.Description+string(spec.Parameters))
	}
	return out
}

// systemWire renders system blocks for comparison.
func systemWire(blocks []ai.SystemPromptBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, block.Text)
	}
	return out
}

// A broad repository request reaches the source-acquisition stage during the
// first session: the inventory is known and the answer still needs component
// content. That verdict used to withdraw the inventory tools, which rewrote the
// request's tool block and invalidated every cached token behind it — the
// observed 99.7% reuse dropping to roughly 2% on the turn the tools changed.
// The verdict must travel in the runtime briefing instead, so the tool block
// stays byte-identical for the whole exploration.
func TestSourceAcquisitionKeepsTheToolBlockStable(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("go.mod", "module example.com/app\n\ngo 1.24\n")
	writeFixture("README.md", "# app\n\nA gateway and an order service.\n")
	writeFixture("backend/cmd/api-gateway/main.go", "package main\n\nfunc main() {}\n")
	writeFixture("backend/cmd/order-service/main.go", "package main\n\nfunc main() {}\n")

	script := []ai.AssistantMessage{
		sessionToolCall("1", "ls", map[string]any{"path": "backend/cmd"}),
		sessionToolCall("2", "read", map[string]any{"path": "README.md"}),
		sessionToolCall("3", "ls", map[string]any{"path": "backend/cmd/api-gateway"}),
		sessionToolCall("4", "read", map[string]any{"path": "backend/cmd/api-gateway/main.go"}),
		sessionText("The project is a gateway plus an order service."),
	}
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, cognition.ShapeParams{Authority: "ask", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	var wires []requestWire
	var reports []agent.ProjectionReport
	h := &sessionHarness{script: script}
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "test", Provider: "test", ContextWindow: 200000},
		Stream:   h.stream,
		MaxTurns: 12,
		Tools: []agent.Tool{
			tools.NewReadTool(root),
			tools.NewGlobTool(root),
			tools.NewLsTool(root),
			tools.NewGrepTool(root),
		},
		SystemFn: func() []ai.SystemPromptBlock {
			return promptBlocks(root, prompt.WorkspaceContract("ask"))
		},
		Project:      projector.Project,
		OnProjection: func(report agent.ProjectionReport) { reports = append(reports, report) },
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []agent.Message{
		agent.UserMessage{Text: "check this project and analyze what this project is about?"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, request := range h.requests {
		wires = append(wires, wireOf(request))
	}
	if len(wires) < 3 {
		t.Fatalf("requests = %d, want the exploration requests plus the answer", len(wires))
	}

	// The verdict must actually fire: otherwise this test would pass on a
	// session that never reached the source-acquisition stage.
	redundant := false
	for _, report := range reports {
		for _, entry := range report.Redundant {
			if entry.Tool == "ls" || entry.Tool == "glob" {
				redundant = true
			}
		}
	}
	if !redundant {
		t.Fatalf("no projection reported an inventory tool as redundant: %+v", reports)
	}

	stable := strings.Join(wires[0].Tools, "\x00")
	for i, wire := range wires {
		if strings.Join(wire.Tools, "\x00") != stable {
			// Closure is the one deliberate surface change: the runtime closes
			// the action space when the answer is the only remaining output.
			// Anything else rewrites the cached prefix mid-exploration.
			if len(wire.Tools) != 0 || i != len(wires)-1 {
				t.Fatalf("request %d changed the tool block:\n first %q\n this  %q",
					i, wire.Tools, wire.Tools)
			}
		}
		if i == 0 {
			continue
		}
		previous := wires[i-1]
		// The previous request's last message is its runtime tail: it is
		// rebuilt per request by design and is the only region allowed to
		// differ. Every message before it must repeat byte for byte.
		sent := previous.Messages[:len(previous.Messages)-1]
		if len(wire.Messages) < len(sent) {
			t.Fatalf("request %d rewound the conversation: %d < %d messages",
				i, len(wire.Messages), len(sent))
		}
		for j := range sent {
			if wire.Messages[j] != sent[j] {
				t.Fatalf("request %d rewrote already-sent message %d:\n previous %q\n current  %q",
					i, j, sent[j], wire.Messages[j])
			}
		}
	}
}
