package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
)

// anthropicTextStream is a completed assistant text turn in Anthropic SSE form.
func anthropicTextStream(text string) string {
	return `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + jsonString(text) + `}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
}

// anthropicToolCallStream is one tool call in Anthropic SSE form.
func anthropicToolCallStream(id, name, input string) string {
	return `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":` + jsonString(id) + `,"name":"` + name + `","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + jsonString(input) + `}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
}

// jsonString quotes a string for embedding in JSON.
func jsonString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// contractServer answers based on which agent is asking.
//
// It keys on the system prompt, because that is the one thing guaranteed to
// differ between a parent and a subagent: the parent has the product prompt, the
// subagent has the isolation prompt agent.RunSubagent requires. Responding by
// request count instead would couple the test to how many requests each agent
// happens to make.
type contractServer struct {
	*httptest.Server
	mu       sync.Mutex
	parents  int
	children int
	// parentBodies records every parent request, so a test can check the child's
	// answer was carried back into the parent's next turn.
	parentBodies []string
}

// subagentPromptMarker identifies a request from a subagent.
const subagentPromptMarker = "You are a subagent working on one piece"

func newContractServer(t *testing.T) *contractServer {
	t.Helper()
	srv := &contractServer{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		body := string(buf)

		srv.mu.Lock()
		isChild := strings.Contains(body, subagentPromptMarker)
		if isChild {
			srv.children++
		} else {
			srv.parents++
			srv.parentBodies = append(srv.parentBodies, body)
		}
		// The parent delegates on its first request and answers from the result
		// on its second. Answering every request with a tool call would make the
		// test loop forever, which is exactly the runaway the deadline now bounds.
		parentCount := srv.parents
		srv.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case isChild:
			_, _ = io.WriteString(w, anthropicTextStream("I read the file and it is correct."))
		case parentCount == 1:
			_, _ = io.WriteString(w, anthropicToolCallStream("call_1", "task", `{"prompt":"check src/auth"}`))
		default:
			_, _ = io.WriteString(w, anthropicTextStream("Done, based on the subagent's report."))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *contractServer) counts() (parents, children int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parents, s.children
}

func (s *contractServer) parentBodyAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.parentBodies) {
		return ""
	}
	return s.parentBodies[i]
}

func testOptions(t *testing.T, root string, maxDepth int) options {
	t.Helper()
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	return options{
		root:        root,
		model:       model,
		maxTokens:   resolveMaxTokens(config.Settings{}, model),
		maxDepth:    maxDepth,
		settings:    config.Default(),
		autoApprove: true,
	}
}

func hasTool(toolSet []agent.Tool, name string) bool {
	for _, tool := range toolSet {
		if tool.Name() == name {
			return true
		}
	}
	return false
}

// TestBuildToolsOmitsDelegationAtDepthOne proves delegation is opt-in rather
// than silently available, so the token cost of a subagent tree is a choice.
func TestBuildToolsOmitsDelegationAtDepthOne(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 1)
	toolSet := buildTools(opts, ai.NewRegistry())

	if hasTool(toolSet, "task") {
		t.Fatal("a depth of 1 still offered delegation")
	}
	// The other tools must be untouched, or the depth flag would be disabling the
	// agent rather than bounding it.
	for _, name := range []string{"read", "grep", "glob", "edit", "bash", "todo"} {
		if !hasTool(toolSet, name) {
			t.Errorf("tool %q is missing at depth 1", name)
		}
	}
}

// TestBuildToolsOffersDelegationAtDepthTwo is the other half: the tool appears
// when asked for.
func TestBuildToolsOffersDelegationAtDepthTwo(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 2)
	if !hasTool(buildTools(opts, ai.NewRegistry()), "task") {
		t.Fatal("a depth of 2 did not offer delegation")
	}
}

// TestChildToolSetBoundsTheTreeStructurally is the security contract of the
// depth cap.
//
// A subagent spawned at the cap must be built *without* a task tool, so it cannot
// delegate however it is prompted. Checking the bound at call time instead would
// make it a rule someone can bypass, rather than a property of the construction.
func TestChildToolSetBoundsTheTreeStructurally(t *testing.T) {
	parent := []agent.Tool{}

	cases := []struct {
		name     string
		maxDepth int
		depth    int
		want     bool
	}{
		{"a child below the cap may delegate", 3, 1, true},
		{"a child at the cap may not", 2, 1, false},
		{"a child past the cap may not", 3, 2, false},
		{"a zero cap binds immediately", 0, 0, false},
		{"a negative cap binds immediately", -1, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t, t.TempDir(), tc.maxDepth)
			got := hasTool(childToolSet(opts, ai.NewRegistry(), parent, tc.depth), "task")
			if got != tc.want {
				t.Errorf("child at depth %d of %d has a task tool: %v, want %v",
					tc.depth, tc.maxDepth, got, tc.want)
			}
		})
	}
}

// TestChildToolSetDoesNotMutateTheParent guards a slice-aliasing bug that would
// silently give the parent a task tool it was not built with.
func TestChildToolSetDoesNotMutateTheParent(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 3)
	parent := buildTools(opts, ai.NewRegistry())
	before := len(parent)

	_ = childToolSet(opts, ai.NewRegistry(), parent, 1)
	if len(parent) != before {
		t.Fatalf("the parent tool set grew from %d to %d", before, len(parent))
	}
}

// A child below the cap must be able to delegate. Before the fix it was
// offered a task tool with no spawner, so every nested delegation failed with
// "no subagent spawner configured" while the bound looked correct.
func TestChildTaskToolBelowCapHasASpawner(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 3)
	set := childToolSet(opts, ai.NewRegistry(), nil, 1)

	var task *tools.TaskTool
	for _, tool := range set {
		if tt, ok := tool.(*tools.TaskTool); ok {
			task = tt
		}
	}
	if task == nil {
		t.Fatal("a child below the cap has no task tool")
	}
	result := task.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"prompt": "inspect src"})
	if strings.Contains(result.Text, "no subagent spawner configured") {
		t.Fatalf("the nested delegation was refused without a spawner: %q", result.Text)
	}
}

// TestDelegationRunsEndToEndThroughAScriptedProvider is the capability this
// increment bought, proved at the boundary the user observes.
//
// The parent must call task, the subagent must answer from its own isolated
// prompt, and the parent must carry that answer into its next request. A
// delegation that returned nothing, or a subagent that never ran, shows up here
// as a request count.
func TestDelegationRunsEndToEndThroughAScriptedProvider(t *testing.T) {
	server := newContractServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	opts := testOptions(t, t.TempDir(), 2)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	if !hasTool(buildTools(opts, registry), "task") {
		t.Fatal("delegation was not wired into the tool set")
	}

	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	_, err = loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "check the auth module"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	parents, children := server.counts()
	if parents != 2 {
		t.Errorf("parent requests = %d, want 2 (one to delegate, one to answer)", parents)
	}
	if children != 1 {
		t.Errorf("subagent requests = %d, want 1: the child never ran, or ran more than once", children)
	}

	// The child's answer must reach the parent's second request as a tool
	// result, or the parent would answer without any information from it.
	second := server.parentBodyAt(1)
	if !strings.Contains(second, "check src/auth") {
		t.Errorf("the second parent request did not carry the delegated task: %s", second)
	}
	if !strings.Contains(second, "I read the file and it is correct.") {
		t.Errorf("the second parent request did not carry the subagent's answer: %s", second)
	}
}

// TestAbsentDelegationIsAConfigurationError keeps a half-wired session from
// silently behaving as though delegation worked.
func TestAbsentDelegationIsAConfigurationError(t *testing.T) {
	// With no task tool in the set, a model that tries to delegate gets a
	// "no such tool" answer rather than a hang or a silent success.
	toolSet := buildTools(testOptions(t, t.TempDir(), 1), ai.NewRegistry())
	if hasTool(toolSet, "task") {
		t.Fatal("delegation appeared without being enabled")
	}
}

// TestDefaultMaxDepthDisablesDelegation proves the safe default: an agent that
// cannot delegate costs at most its own turns, so a user who never asked for
// subagents cannot be surprised by a token bill from a tree of them.
func TestDefaultMaxDepthDisablesDelegation(t *testing.T) {
	// A depth of 1 is what resolveOptions sets when no flag is given.
	if hasTool(buildTools(testOptions(t, t.TempDir(), 1), ai.NewRegistry()), "task") {
		t.Error("the default depth offered delegation")
	}
	// A zero, negative or nonsense flag value must not enable it either.
	for _, depth := range []int{0, -1, -100} {
		if hasTool(buildTools(testOptions(t, t.TempDir(), depth), ai.NewRegistry()), "task") {
			t.Errorf("a depth of %d offered delegation", depth)
		}
	}
}

// nestedServer answers a three-level delegation by reading the assignment in
// each child's system prompt. It is the real provider transport (Anthropic SSE
// over HTTP) with a scripted model, not a live external API call.
type nestedServer struct {
	*httptest.Server
	mu     sync.Mutex
	counts map[string]int
	bodies map[string][]string
}

func newNestedServer(t *testing.T) *nestedServer {
	t.Helper()
	srv := &nestedServer{counts: map[string]int{}, bodies: map[string][]string{}}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		body := string(buf)

		level := "parent"
		switch {
		case strings.Contains(body, `Your assignment:\nlevel-two`):
			level = "two"
		case strings.Contains(body, `Your assignment:\nlevel-one`):
			level = "one"
		}

		srv.mu.Lock()
		srv.counts[level]++
		count := srv.counts[level]
		srv.bodies[level] = append(srv.bodies[level], body)
		srv.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case level == "parent" && count == 1:
			_, _ = io.WriteString(w, anthropicToolCallStream("parent-call", "task",
				`{"prompt":"level-one: inspect the module"}`))
		case level == "one" && count == 1:
			_, _ = io.WriteString(w, anthropicToolCallStream("one-call", "task",
				`{"prompt":"level-two: inspect deeper"}`))
		case level == "two":
			_, _ = io.WriteString(w, anthropicTextStream("level two done"))
		case level == "one":
			_, _ = io.WriteString(w, anthropicTextStream("level one done"))
		default:
			_, _ = io.WriteString(w, anthropicTextStream("parent done"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *nestedServer) count(level string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[level]
}

func (s *nestedServer) bodyAt(level string, i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.bodies[level]) {
		return ""
	}
	return s.bodies[level][i]
}

// Nested delegation is exercised through the provider transport at three
// levels. The grandchild must run, its answer must reach the child, and the
// child's answer must reach the parent.
func TestNestedDelegationRunsThreeLevels(t *testing.T) {
	server := newNestedServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	opts := testOptions(t, t.TempDir(), 3)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)
	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	if _, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "delegate deeply"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := server.count("parent"); got != 2 {
		t.Errorf("parent requests = %d, want 2", got)
	}
	if got := server.count("one"); got != 2 {
		t.Errorf("level-one requests = %d, want 2", got)
	}
	if got := server.count("two"); got != 1 {
		t.Errorf("level-two requests = %d, want 1 (the grandchild never ran)", got)
	}
	if body := server.bodyAt("one", 1); !strings.Contains(body, "level two done") {
		t.Errorf("the grandchild's answer did not reach the child: %s", body)
	}
	if body := server.bodyAt("parent", 1); !strings.Contains(body, "level one done") {
		t.Errorf("the child's answer did not reach the parent: %s", body)
	}
}

// The production spawner must copy the parent's deadline. The D-2 fix in
// RunSubagent is vacuous if the spawner's own parent config has a zero deadline.
func TestSpawnerPropagatesTheParentDeadline(t *testing.T) {
	server := newContractServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	opts := testOptions(t, t.TempDir(), 2)
	opts.deadline = time.Now().Add(-time.Second)
	opts.security = opts.newSecurityGate(nil)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)
	spawner := newSubagentSpawner(opts, registry, nil)

	_, err := spawner.Spawn(context.Background(), agent.SubagentSpec{
		Task:   "inspect src",
		System: []ai.SystemPromptBlock{{Text: "you are a researcher"}},
	})
	if !errors.Is(err, agent.ErrNoSubagentResult) {
		t.Fatalf("spawn err = %v, want ErrNoSubagentResult (the child produced no output)", err)
	}
	_, children := server.counts()
	if children != 0 {
		t.Fatalf("the child made %d provider calls after the deadline expired, want 0", children)
	}
}

// A child inherits the parent's FUZZ authority. A write attempted by the child
// is refused by the same security gate, and the child's reviewable result does
// not let the parent claim success.
func TestChildInheritsFuzzAuthority(t *testing.T) {
	var (
		mu      sync.Mutex
		parents int
		child   int
		bodies  []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		body := string(buf)
		isChild := strings.Contains(body, subagentPromptMarker)
		mu.Lock()
		if isChild {
			child++
		} else {
			parents++
		}
		bodies = append(bodies, body)
		childCount := child
		parentCount := parents
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case isChild && childCount == 1:
			_, _ = io.WriteString(w, anthropicToolCallStream("write-call", "write",
				`{"path":"created.txt","content":"child wrote this"}`))
		case isChild:
			_, _ = io.WriteString(w, anthropicTextStream("I could not write the file."))
		case parentCount == 1:
			_, _ = io.WriteString(w, anthropicToolCallStream("parent-call", "task",
				`{"prompt":"write created.txt with the child content"}`))
		default:
			_, _ = io.WriteString(w, anthropicTextStream("parent done"))
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	root := t.TempDir()
	opts := testOptions(t, root, 2)
	opts.setAuthority(workspace.Fuzz)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)
	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if _, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "delegate the write"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "created.txt")); err == nil {
		t.Fatal("a child in FUZZ wrote a file through the inherited authority")
	}
	mu.Lock()
	defer mu.Unlock()
	if child < 2 {
		t.Fatalf("child requests = %d, want at least 2", child)
	}
	joined := strings.Join(bodies, "\n")
	if !strings.Contains(joined, "not permitted") {
		t.Errorf("the child's write refusal was not observable: %s", joined)
	}
	if !strings.Contains(joined, "subagent needs review") {
		t.Errorf("the parent did not receive a reviewable child result: %s", joined)
	}
}
