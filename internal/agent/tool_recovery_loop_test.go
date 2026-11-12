package agent_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// recoveryRepo is a deterministic project with documentation, a manifest, and
// two components so a failed targeted structural query has real alternatives.
func recoveryRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":    "module example.com/recovery\n\ngo 1.23\n",
		"README.md": "# Recovery\n\nA Go service with an API gateway and an order backend.\n",
		"backend/cmd/api/main.go": "package main\n\nimport \"net/http\"\n\n" +
			"func main() { http.ListenAndServe(\":8080\", nil) }\n",
		"backend/internal/order/service.go": "package order\n\nfunc Checkout() {}\n",
		"web/src/App.tsx":                   "export default function App() { return null }\n",
	}
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func toolCall(id, name string, args map[string]any) ai.ContentBlock {
	return ai.ToolCall{ID: id, Name: name, Arguments: args}
}

// Session #224: a successful structural overview, an invalid targeted
// selector, a repeated identical request, then a corrected invocation and
// independent reads. The overview must survive, the invalid request must not
// become a capability outage, the repeat must not execute, and the task must
// not block while the authorized providers remain available.
func TestSession224InvalidSelectorRecovers(t *testing.T) {
	root := recoveryRepo(t)
	source := tools.NewStructuralSource(root)
	structure := tools.NewStructureTool(source)
	read := tools.NewReadTool(root)
	list := tools.NewLsTool(root)

	prompt := "check this project and analyze what this project is about?"
	answer := "According to the README, the project is a Go service with an API gateway " +
		"and an order backend. The structural inventory contains backend/cmd/api and " +
		"backend/internal/order. The inspected backend/cmd/api/main.go starts an HTTP " +
		"listener."

	badArgs := map[string]any{"path": "backend"}
	result := runFailureLoop(t, prompt, []agent.Tool{structure, read, list}, []ai.AssistantMessage{
		{
			Content:    []ai.ContentBlock{toolCall("overview", "structure", map[string]any{"view": "overview"})},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{toolCall("bad-1", "structure", badArgs)},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{toolCall("bad-2", "structure", badArgs)},
			StopReason: ai.StopToolUse,
		},
		{
			Content: []ai.ContentBlock{
				toolCall("corrected", "structure", map[string]any{"view": "files", "path": "backend"}),
				toolCall("readme", "read", map[string]any{"path": "README.md"}),
				toolCall("main", "read", map[string]any{"path": "backend/cmd/api/main.go"}),
			},
			StopReason: ai.StopToolUse,
		},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("session #224 blocked despite available providers: %v (requests=%d report=%+v)",
			result.err, result.requests, result.report)
	}
	if result.err != nil {
		t.Fatalf("run: %v", result.err)
	}
	if result.report.Terminal || result.report.Stage == "blocked" {
		t.Fatalf("projection closed the task: %+v", result.report)
	}

	overview, ok := result.evidence.Find("structure:overview")
	if !ok {
		t.Fatal("the successful overview disappeared from the evidence ledger")
	}
	if overview.Status == "conflicting" || overview.Source.SnapshotID == "" {
		t.Fatalf("overview lost its original provenance: %+v", overview)
	}

	failures := result.evidence.Failures
	var rejected *cognitionFailure
	for i := range failures {
		record := failures[i]
		if record.Signature == agent.CallSignature("structure", badArgs) {
			rejected = &cognitionFailure{Class: record.Class, Failures: record.Failures,
				Refusals: record.Refusals, Blocks: record.Blocks()}
		}
	}
	if rejected == nil {
		for i, turn := range result.turns {
			for _, outcome := range turn.Results {
				t.Logf("turn %d: tool=%s err=%t class=%s refused=%t text=%.180q",
					i, outcome.ToolName, outcome.Result.IsError,
					outcome.Result.FailureClass, outcome.Refused(), outcome.Result.Text)
			}
		}
		t.Fatalf("the invalid selector was not recorded: %+v", failures)
	}
	if rejected.Class != knowledge.FailureInvalidSelector {
		t.Fatalf("class = %q, want invalid_selector (not a remote outage)", rejected.Class)
	}
	if rejected.Class == knowledge.FailureToolError {
		t.Fatal("the selector error was misclassified as remote_tool_error")
	}
	if rejected.Failures != 1 {
		t.Fatalf("invalid invocation executed %d times, want exactly 1", rejected.Failures)
	}
	if rejected.Refusals < 1 {
		t.Fatal("the identical invalid invocation was not refused")
	}
	if rejected.Blocks {
		t.Fatal("the request-level rejection was recorded as blocking capability health")
	}

	if _, ok := result.evidence.Find("structure:backend"); !ok {
		t.Fatal("the corrected targeted inventory produced no evidence")
	}
	if _, ok := result.evidence.Find("file:README.md"); !ok {
		t.Fatal("the documentation fallback produced no evidence")
	}
	if _, ok := result.evidence.Find("file:backend/cmd/api/main.go"); !ok {
		t.Fatal("the source fallback produced no evidence")
	}

	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:          result.turns,
		Contract:       result.intent,
		EvidenceReview: result.report.EvidenceReview(),
		Provenance:     result.report.Provenance,
		Root:           root,
	})
	if !completion.Complete() {
		t.Fatalf("grounded recovery answer was not complete: %s", completion.Reason)
	}
}

// Scenario E: a successful fallback acquires documentation and inventory but
// no implementation source. The task can produce a qualified answer, but an
// implementation claim is not admissible and the run is not complete.
func TestFallbackWithoutSourceCannotClaimImplementation(t *testing.T) {
	root := recoveryRepo(t)
	var executions atomic.Int64
	structure := outageStructure{executions: &executions}
	read := tools.NewReadTool(root)
	list := tools.NewLsTool(root)

	prompt := "check this project and analyze what this project is about?"
	answer := "The backend is fully implemented and the API is production-ready."

	result := runFailureLoop(t, prompt, []agent.Tool{structure, read, list}, []ai.AssistantMessage{
		{Content: []ai.ContentBlock{toolCall("s1", "structure", map[string]any{"view": "overview"})}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{toolCall("s2", "structure", map[string]any{"view": "overview"})}, StopReason: ai.StopToolUse},
		{
			Content: []ai.ContentBlock{
				toolCall("readme", "read", map[string]any{"path": "README.md"}),
				toolCall("root", "ls", map[string]any{"path": "."}),
			},
			StopReason: ai.StopToolUse,
		},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("documentation fallback was treated as a blocked task: %v", result.err)
	}
	if result.err != nil {
		t.Fatalf("run: %v", result.err)
	}
	if _, ok := result.evidence.Find("file:README.md"); !ok {
		t.Fatal("documentation fallback was not recorded")
	}
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:          result.turns,
		Contract:       result.intent,
		EvidenceReview: result.report.EvidenceReview(),
		Provenance:     result.report.Provenance,
		Root:           root,
	})
	if completion.Complete() {
		t.Fatal("an implementation claim from documentation and inventory was accepted")
	}
	if completion.Reason == "" {
		t.Fatal("a non-complete fallback did not explain the evidence gap")
	}
}

// selectiveReadTool delegates to the real read capability but fails one
// coordinate, modelling a mixed-health provider surface without inventing a
// second observation model.
type selectiveReadTool struct {
	read       *tools.ReadTool
	failPath   string
	executions *atomic.Int64
}

func (t selectiveReadTool) Name() string        { return "read" }
func (t selectiveReadTool) Description() string { return "read a file" }
func (t selectiveReadTool) Parameters() *jsonschema.Schema {
	return t.read.Parameters()
}
func (t selectiveReadTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (t selectiveReadTool) Exclusive(map[string]any) bool { return false }
func (t selectiveReadTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	t.executions.Add(1)
	path, _ := args["path"].(string)
	if path == t.failPath {
		return agent.Result{
			Text:         "read failed for " + path,
			IsError:      true,
			FailureClass: agent.ToolFailureExecutionFailed,
		}
	}
	return t.read.Execute(ctx, args)
}

// Scenario D: structure succeeds, one source read fails, and independent reads
// still succeed. The failed coordinate produces no file evidence; every
// successful observation keeps its provenance.
func TestMixedProviderHealthPreservesSuccessfulObservations(t *testing.T) {
	root := recoveryRepo(t)
	source := tools.NewStructuralSource(root)
	structure := tools.NewStructureTool(source)
	read := selectiveReadTool{
		read: tools.NewReadTool(root), failPath: "backend/internal/order/service.go",
		executions: &atomic.Int64{},
	}
	list := tools.NewLsTool(root)

	prompt := "check this project and analyze what this project is about?"
	answer := "According to the README and the inspected API entrypoint, this project is " +
		"a Go service with an API gateway. The order service source could not be read, " +
		"so its implementation is not verified."

	result := runFailureLoop(t, prompt, []agent.Tool{structure, read, list}, []ai.AssistantMessage{
		{Content: []ai.ContentBlock{toolCall("inventory", "structure", map[string]any{"view": "overview"})}, StopReason: ai.StopToolUse},
		{
			Content: []ai.ContentBlock{
				toolCall("readme", "read", map[string]any{"path": "README.md"}),
				toolCall("main", "read", map[string]any{"path": "backend/cmd/api/main.go"}),
				toolCall("order", "read", map[string]any{"path": "backend/internal/order/service.go"}),
			},
			StopReason: ai.StopToolUse,
		},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("one failed read blocked independent providers: %v", result.err)
	}
	if result.err != nil {
		t.Fatalf("run: %v", result.err)
	}
	if _, ok := result.evidence.Find("structure:overview"); !ok {
		t.Fatal("the successful structure observation disappeared")
	}
	if _, ok := result.evidence.Find("file:README.md"); !ok {
		t.Fatal("the successful README read disappeared")
	}
	if _, ok := result.evidence.Find("file:backend/cmd/api/main.go"); !ok {
		t.Fatal("the successful source read disappeared")
	}
	if _, ok := result.evidence.Find("file:backend/internal/order/service.go"); ok {
		t.Fatal("a failed read was recorded as source content")
	}
}

// Scenario C: the model repeats only an invalid selector. The runtime gives one
// correction window, refuses every identical repeat, and closes with a bounded
// review verdict rather than a false remote-capability failure.
func TestRepeatedInvalidSelectorIsBoundedWithoutFalseOutage(t *testing.T) {
	root := recoveryRepo(t)
	structure := tools.NewStructureTool(tools.NewStructuralSource(root))
	read := tools.NewReadTool(root)
	list := tools.NewLsTool(root)

	badArgs := map[string]any{"path": "backend"}
	script := make([]ai.AssistantMessage, 0, 8)
	for i := 0; i < 8; i++ {
		script = append(script, ai.AssistantMessage{
			Content:    []ai.ContentBlock{toolCall("bad-"+string(rune('a'+i)), "structure", badArgs)},
			StopReason: ai.StopToolUse,
		})
	}
	result := runFailureLoop(t, "check this project and analyze what this project is about?",
		[]agent.Tool{structure, read, list}, script)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("repeated invalid selectors did not close: %v", result.err)
	}
	if result.requests > 5 {
		t.Fatalf("model requests = %d, want a bounded correction window", result.requests)
	}
	if strings.Contains(strings.ToLower(result.err.Error()), "capability failed") ||
		strings.Contains(result.err.Error(), "remote_tool_error") {
		t.Fatalf("invalid selector was reported as a capability outage: %v", result.err)
	}
	var rejected bool
	for _, record := range result.evidence.Failures {
		if record.Signature == agent.CallSignature("structure", badArgs) {
			rejected = true
			if record.Class != knowledge.FailureInvalidSelector {
				t.Fatalf("class = %q, want invalid_selector", record.Class)
			}
			if record.Failures != 1 {
				t.Fatalf("invalid selector executed %d times, want exactly 1", record.Failures)
			}
			if record.Refusals == 0 {
				t.Fatal("identical invalid selectors were not deduplicated")
			}
		}
	}
	if !rejected {
		t.Fatal("the invalid selector was not recorded")
	}
}

// flakyStructure succeeds for an overview and is unavailable for targeted
// views. It models a provider whose health changes by operation, which the
// failure ledger must not widen into a global outage.
type flakyStructure struct {
	real     *tools.StructureTool
	failures *atomic.Int64
}

func (t flakyStructure) Name() string        { return "structure" }
func (t flakyStructure) Description() string { return "structural inventory" }
func (t flakyStructure) Parameters() *jsonschema.Schema {
	return t.real.Parameters()
}
func (t flakyStructure) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (t flakyStructure) Exclusive(map[string]any) bool { return false }
func (t flakyStructure) ActionClasses() []agent.ActionClass {
	return []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}
}
func (t flakyStructure) Execute(ctx agent.Context, args map[string]any) agent.Result {
	view, _ := args["view"].(string)
	if view == "" || view == "overview" {
		return t.real.Execute(ctx, args)
	}
	t.failures.Add(1)
	return agent.Result{
		Text:         "targeted structural view unavailable",
		IsError:      true,
		FailureClass: string(knowledge.FailureUnavailable),
	}
}

// A successful inventory survives a later targeted-provider outage. The failed
// operation is recorded as a provider failure, not by rewriting the prior
// snapshot, and independent reads close the evidence gap.
func TestSuccessfulInventorySurvivesTargetedOutage(t *testing.T) {
	root := recoveryRepo(t)
	source := tools.NewStructuralSource(root)
	structure := flakyStructure{
		real: tools.NewStructureTool(source), failures: &atomic.Int64{},
	}
	read := tools.NewReadTool(root)
	list := tools.NewLsTool(root)

	answer := "According to the README and the inspected API entrypoint, this project is " +
		"a Go service with an API gateway."
	args := map[string]any{"view": "files", "path": "backend"}
	result := runFailureLoop(t, "check this project and analyze what this project is about?",
		[]agent.Tool{structure, read, list}, []ai.AssistantMessage{
			{Content: []ai.ContentBlock{toolCall("overview", "structure", map[string]any{"view": "overview"})}, StopReason: ai.StopToolUse},
			{Content: []ai.ContentBlock{toolCall("files-1", "structure", args)}, StopReason: ai.StopToolUse},
			{Content: []ai.ContentBlock{toolCall("files-2", "structure", args)}, StopReason: ai.StopToolUse},
			{
				Content: []ai.ContentBlock{
					toolCall("readme", "read", map[string]any{"path": "README.md"}),
					toolCall("main", "read", map[string]any{"path": "backend/cmd/api/main.go"}),
				},
				StopReason: ai.StopToolUse,
			},
			{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
		})

	if errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("a targeted view outage blocked independent providers: %v", result.err)
	}
	if result.err != nil {
		t.Fatalf("run: %v", result.err)
	}
	overview, ok := result.evidence.Find("structure:overview")
	if !ok || overview.Status == "conflicting" || overview.Source.SnapshotID == "" {
		t.Fatalf("the successful inventory was rewritten by the later failure: %+v", overview)
	}
	if _, ok := result.evidence.Find("file:README.md"); !ok {
		t.Fatal("documentation fallback was not recorded")
	}
	if _, ok := result.evidence.Find("file:backend/cmd/api/main.go"); !ok {
		t.Fatal("source fallback was not recorded")
	}
}

// cognitionFailure is the subset of the durable failure record this black-box
// loop test needs without importing control-plane internals into the assertion.
type cognitionFailure struct {
	Class    knowledge.FailureClass
	Failures int
	Refusals int
	Blocks   bool
}

// outageStructure is a structural provider that is genuinely unavailable. It is
// independent of the read/list providers, so the task must continue through
// them rather than treating one failing provider as a failed investigation.
type outageStructure struct {
	executions *atomic.Int64
}

func (t outageStructure) Name() string        { return "structure" }
func (t outageStructure) Description() string { return "structural inventory" }
func (t outageStructure) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"view":{"type":"string"},"path":{"type":"string"}}
	}`))
}
func (t outageStructure) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (t outageStructure) Exclusive(map[string]any) bool { return false }
func (t outageStructure) ActionClasses() []agent.ActionClass {
	return []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}
}
func (t outageStructure) Execute(agent.Context, map[string]any) agent.Result {
	t.executions.Add(1)
	return agent.Result{
		Text:         "structural provider unavailable",
		IsError:      true,
		FailureClass: string(knowledge.FailureUnavailable),
	}
}

// Scenario B: the structural provider is actually down. The typed failure is
// preserved, the repeated call is refused, and independent documentation and
// source providers still satisfy the project question.
func TestStructureOutageRecoversThroughIndependentProviders(t *testing.T) {
	root := recoveryRepo(t)
	var executions atomic.Int64
	structure := outageStructure{executions: &executions}
	read := tools.NewReadTool(root)
	list := tools.NewLsTool(root)

	prompt := "check this project and analyze what this project is about?"
	answer := "According to the README and the inspected source, this is a Go service " +
		"with an API gateway and an order backend. The structural provider was " +
		"unavailable, so this account is based on documentation, inventory, and " +
		"source observations rather than structural analysis."

	args := map[string]any{"view": "overview"}
	result := runFailureLoop(t, prompt, []agent.Tool{structure, read, list}, []ai.AssistantMessage{
		{Content: []ai.ContentBlock{toolCall("s1", "structure", args)}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{toolCall("s2", "structure", args)}, StopReason: ai.StopToolUse},
		{
			Content: []ai.ContentBlock{
				toolCall("readme", "read", map[string]any{"path": "README.md"}),
				toolCall("main", "read", map[string]any{"path": "backend/cmd/api/main.go"}),
			},
			StopReason: ai.StopToolUse,
		},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("a single provider outage blocked the whole task: %v (report=%+v)",
			result.err, result.report)
	}
	if result.err != nil {
		t.Fatalf("run: %v", result.err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("failed provider executed %d times, want exactly 1", got)
	}
	if _, ok := result.evidence.Find("file:README.md"); !ok {
		t.Fatal("documentation fallback was not recorded")
	}
	if _, ok := result.evidence.Find("file:backend/cmd/api/main.go"); !ok {
		t.Fatal("source fallback was not recorded")
	}
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:          result.turns,
		Contract:       result.intent,
		EvidenceReview: result.report.EvidenceReview(),
		Provenance:     result.report.Provenance,
		Root:           root,
	})
	if !completion.Complete() {
		t.Fatalf("qualified fallback answer was not complete: %s", completion.Reason)
	}
}
