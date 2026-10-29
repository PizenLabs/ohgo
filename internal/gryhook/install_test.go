package gryhook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

type writeTool struct{}

func (writeTool) Name() string        { return "edit" }
func (writeTool) Description() string { return "edit a file" }
func (writeTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`))
}
func (writeTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierWrite} }
func (writeTool) Exclusive(map[string]any) bool { return false }
func (writeTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	path, _ := args["path"].(string)
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		return agent.ErrorResult("write: %v", err)
	}
	return agent.Result{Text: "edited " + path, Mutates: true}
}

func scripted(messages ...ai.AssistantMessage) ai.StreamFn {
	var (
		mu    sync.Mutex
		index int
	)
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		defer mu.Unlock()
		if index >= len(messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
		msg := messages[index]
		index++
		return ai.NewCompletedStream(msg), nil
	}
}

func TestInstallTracksTheRealToolLifecycle(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls.log")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "@LOG@"
stage="$2"
cat <<EOF
{"hook":{"stage":"$stage","status":"ok","cached":false},"context":{"status":"ok","evidence":{"freshness":"fresh","complete_for_scope":true,"repository_scan":false},"files":[{"path":"main.go"}],"nodes":[{"qn":"pkg.main","file":"main.go"}],"unknowns":[],"evidence":{"reasons":[]}},"failure":null}
EOF`
	bin := filepath.Join(t.TempDir(), "gry")
	script = strings.ReplaceAll(script, "@LOG@", log)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Config{
		Enabled: true, Binary: bin, Project: "proj", Root: root,
		Timeout: 2_000_000_000, MaxTokens: 1200,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(runner, "run-1", "edit main.go")
	beforePlan := session.BeforePlan(context.Background())
	if !beforePlan.OK() {
		t.Fatalf("before_plan = %+v", beforePlan)
	}
	if blocks := session.PlanEvidence(); len(blocks) == 0 || !strings.Contains(blocks[0].Text, "before_plan") {
		t.Fatalf("plan evidence was not produced: %+v", blocks)
	}

	loop, err := agent.New(agent.Config{
		Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: scripted(
			ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "c1", Name: "edit", Arguments: map[string]any{"path": target},
				}},
				StopReason: ai.StopToolUse,
			},
		),
		Tools: []agent.Tool{writeTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	Install(loop, session)
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "edit"}})
	if err != nil {
		t.Fatal(err)
	}
	finish := session.BeforeFinish(context.Background())
	if !finish.OK() {
		t.Fatalf("before_finish = %+v", finish)
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	order := strings.Fields(string(raw))
	var stages []string
	for _, field := range order {
		switch field {
		case "before_plan", "before_edit", "after_edit", "before_finish":
			stages = append(stages, field)
		}
	}
	want := []string{"before_plan", "before_edit", "after_edit", "before_finish"}
	if strings.Join(stages, ",") != strings.Join(want, ",") {
		t.Fatalf("stage order = %v, want %v\nlog:\n%s", stages, want, raw)
	}
	if len(turns) == 0 {
		t.Fatal("no turns")
	}
	result := turns[0].Results[0]
	if !strings.Contains(result.Result.Text, "GRY before_edit") ||
		!strings.Contains(result.Result.Text, "GRY after_edit") {
		t.Fatalf("edit result did not carry hook evidence:\n%s", result.Result.Text)
	}
	if !strings.Contains(result.Result.Text, "not as permission") {
		t.Fatalf("evidence did not state the authority boundary:\n%s", result.Result.Text)
	}
}
