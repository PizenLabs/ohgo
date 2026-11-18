package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// TestGryStructuralEvidenceReachesTheModelThroughTheRealToolSet proves the
// composition, not just the adapter: the real investigate tool routes a
// target-shaped flow question to the optional external provider, the adapter
// resolves the project through gry, and the bounded fact reaches the model on
// the next request. The fake gry process is the only substitute; the tool set,
// planner, router, budget, evidence engine, and provider wire are production
// code.
func TestGryStructuralEvidenceReachesTheModelThroughTheRealToolSet(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(t.TempDir(), "gry")
	body := fmt.Sprintf(`#!/bin/sh
case "$1" in
projects)
  echo '{"projects":[{"name":"fixture","root_path":%q}]}'
  ;;
trace)
  echo '{"found":true,"total":1,"returned":1,"truncated":false,"start":{"qn":"pkg.A"},"rows":[{"hop":1,"direction":"inbound","edge":"CALLS","qn":"pkg.B.Caller","name":"Caller","label":"Function","file":"b.go","lines":[10,12],"observed":true}]}'
  ;;
*)
  echo '{}'
  ;;
esac
`, root)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OHGO_GRY_BIN", script)

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		requests = append(requests, string(buf))
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = fmt.Fprint(w, anthropicTurnSSE("investigate",
				`{"question":"what calls pkg.A?","intent":"flow","focus":"pkg.A"}`))
			return
		}
		_, _ = fmt.Fprint(w, anthropicFinalTurn)
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	opts := options{
		root:        root,
		model:       catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		settings:    config.Default(),
		autoApprove: true,
		effort:      "medium",
	}
	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "what calls pkg.A?"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	second := requests[1]
	for _, want := range []string{"structural/gry", "pkg.B.Caller", "CALLS"} {
		if !strings.Contains(second, want) {
			t.Fatalf("the model did not receive %q from gry evidence:\n%s", want, second)
		}
	}
	for _, outcome := range turns[0].Results {
		if outcome.Result.Mutates {
			t.Fatalf("a read-only investigation reported a mutation: %+v", outcome)
		}
	}
}

// TestBuildToolsDoesNotStartGry proves the optional provider is lazy: composing
// the full tool set must not launch gry, inspect an index, or require one.
func TestBuildToolsDoesNotStartGry(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	script := filepath.Join(t.TempDir(), "gry")
	body := "#!/bin/sh\necho started > " + marker + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OHGO_GRY_BIN", script)
	tools := buildTools(testOptions(t, root, 1), ai.NewRegistry())
	if len(tools) == 0 {
		t.Fatal("buildTools returned no tools")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("buildTools launched gry during composition")
	}
}

// TestLiveGryThroughRealToolSet is the opt-in real-task probe: the production
// tool set, planner, router, budget, evidence engine, and gry adapter run
// against this repository's real index. Only the model is absent, so this is
// the structural half of a real task; the scripted end-to-end test above covers
// the model wire.
func TestLiveGryThroughRealToolSet(t *testing.T) {
	if os.Getenv("OHGO_GRY_LIVE") != "1" {
		t.Skip("set OHGO_GRY_LIVE=1 to run against a real gry installation")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	toolSet := buildTools(testOptions(t, root, 1), ai.NewRegistry())
	tool := phase8FindTool(t, toolSet, "investigate")
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"question": "who calls investigate.Engine.Run",
		"intent":   "flow",
		"focus":    "ohgo.internal.intelligence.investigate.investigate.Engine.Run",
	})
	if result.IsError {
		t.Fatalf("investigate failed: %s", result.Text)
	}
	if result.Mutates {
		t.Fatal("a read-only investigation reported a mutation")
	}
	if !strings.Contains(result.Text, "structural/gry") {
		t.Fatalf("the real tool set did not route through gry:\n%s", result.Text)
	}
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T", result.Details)
	}
	requests, _ := details["external_requests"].(int)
	if requests < 1 {
		t.Fatalf("external request was not accounted: %v", details["external_requests"])
	}
	if len(result.Text) > tools.InvestigateMaxOutput {
		t.Fatalf("output is not bounded: %d bytes", len(result.Text))
	}
	t.Logf("external_requests=%d output_bytes=%d\n%s", requests, len(result.Text), result.Text)
}

// TestLiveGryStructuralPrecision is the opt-in efficiency probe. It executes
// the same flow question twice through the real tool set — once with the
// external provider disabled and once with the real gry index — and reports the
// bounded output size, elapsed time, and external request count. It asserts the
// contract (native is not charged, external is charged, both outputs are
// bounded), not a brittle speed claim.
func TestLiveGryStructuralPrecision(t *testing.T) {
	if os.Getenv("OHGO_GRY_LIVE") != "1" {
		t.Skip("set OHGO_GRY_LIVE=1 to run against a real gry installation")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	run := func(grySetting string) (string, int, time.Duration) {
		t.Setenv("OHGO_GRY", grySetting)
		toolSet := buildTools(testOptions(t, root, 1), ai.NewRegistry())
		tool := phase8FindTool(t, toolSet, "investigate")
		started := time.Now()
		result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
			"question": "who calls investigate.Engine.Run",
			"intent":   "flow",
			"focus":    "ohgo.internal.intelligence.investigate.investigate.Engine.Run",
		})
		if result.IsError {
			t.Fatalf("investigate failed: %s", result.Text)
		}
		if len(result.Text) > tools.InvestigateMaxOutput {
			t.Fatalf("output is not bounded: %d bytes", len(result.Text))
		}
		details, ok := result.Details.(map[string]any)
		if !ok {
			t.Fatalf("details = %T", result.Details)
		}
		external, _ := details["external_requests"].(int)
		return result.Text, external, time.Since(started)
	}
	nativeText, nativeExternal, nativeDuration := run("off")
	gryText, gryExternal, gryDuration := run("")
	if nativeExternal != 0 {
		t.Fatalf("native path charged an external request: %d", nativeExternal)
	}
	if gryExternal < 1 {
		t.Fatalf("gry path was not charged: %d", gryExternal)
	}
	t.Logf("native: bytes=%d duration=%s external=%d", len(nativeText), nativeDuration, nativeExternal)
	t.Logf("gry:    bytes=%d duration=%s external=%d", len(gryText), gryDuration, gryExternal)
}

// TestInvestigateWorksWithoutGry is the independence probe through the real
// tool set: a bogus binary makes the optional provider unavailable, and the
// native structural path still answers. Ohgo does not require gry.
func TestInvestigateWorksWithoutGry(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OHGO_GRY_BIN", filepath.Join(t.TempDir(), "does-not-exist"))
	toolSet := buildTools(testOptions(t, root, 1), ai.NewRegistry())
	tool := phase8FindTool(t, toolSet, "investigate")
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"question": "what is this project?",
	})
	if result.IsError {
		t.Fatalf("investigate failed without gry: %s", result.Text)
	}
	if !strings.Contains(result.Text, "structural/native") {
		t.Fatalf("the native structural path did not answer:\n%s", result.Text)
	}
	if len(result.Text) > tools.InvestigateMaxOutput {
		t.Fatalf("output is not bounded: %d bytes", len(result.Text))
	}
}
