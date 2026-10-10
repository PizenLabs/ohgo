package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
)

// scriptedSpawner records what the task tool asked for and returns a fixed
// result, so tests assert on the tool's contract rather than on a real model.
type scriptedSpawner struct {
	spec   agent.SubagentSpec
	result agent.SubagentResult
	err    error
	calls  int
}

func (s *scriptedSpawner) Spawn(ctx context.Context, spec agent.SubagentSpec) (agent.SubagentResult, error) {
	s.spec = spec
	s.calls++
	return s.result, s.err
}

func testTaskTool(spawn Spawner) *TaskTool {
	return NewTaskTool(spawn, 0, 1, func(task string) []ai.SystemPromptBlock {
		return []ai.SystemPromptBlock{{Text: "You are a subagent. Assignment: " + task}}
	})
}

func taskArgs(prompt string) map[string]any {
	return map[string]any{"prompt": prompt}
}

// TestTaskToolDelegatesTheAssignment proves the tool passes exactly what the
// model asked for, shaped as the child's only user message.
func TestTaskToolDelegatesTheAssignment(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{
		Text:      "the auth module is fine",
		ToolCalls: 3,
		ReadOnly:  true,
	}}
	tool := testTaskTool(spawner)

	result := tool.Execute(agent.Context{Ctx: context.Background()}, taskArgs("review src/auth"))
	if result.IsError {
		t.Fatalf("Execute: %s", result.Text)
	}
	if !strings.Contains(result.Text, "the auth module is fine") {
		t.Errorf("result = %q, want the subagent's answer", result.Text)
	}
	if spawner.calls != 1 {
		t.Errorf("spawn calls = %d, want 1", spawner.calls)
	}
	if !strings.Contains(spawner.spec.Task, "review src/auth") {
		t.Errorf("task = %q, want the assignment", spawner.spec.Task)
	}
	// Depth advances, so a nested task tool can tell how deep it is.
	if spawner.spec.Depth != 1 {
		t.Errorf("depth = %d, want 1", spawner.spec.Depth)
	}
}

// TestTaskToolReportsTheCostOfTheRun lets a parent tell a cheap success from one
// that spent its whole budget reading the repository.
func TestTaskToolReportsTheCostOfTheRun(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{
		Text:      "done",
		ToolCalls: 4,
		ReadOnly:  true,
	}}
	result := testTaskTool(spawner).Execute(
		agent.Context{Ctx: context.Background()}, taskArgs("look"))

	if !strings.Contains(result.Text, "[subagent") {
		t.Errorf("result = %q, want the run summary", result.Text)
	}
	if !strings.Contains(result.Text, "read-only") {
		t.Errorf("result = %q, want the read-only marker", result.Text)
	}
	if !strings.Contains(result.Text, "4 tool calls") {
		t.Errorf("result = %q, want the tool-call count", result.Text)
	}
}

// TestTaskToolReturnsTheChildsErrorUnaltered is what makes a retry possible.
//
// Wrapping the child's error into a generic failure would leave the parent
// unable to tell a timeout from a malformed task from a provider outage.
func TestTaskToolReturnsTheChildsErrorUnaltered(t *testing.T) {
	boom := errors.New("context deadline exceeded")
	spawner := &scriptedSpawner{err: boom}

	result := testTaskTool(spawner).Execute(
		agent.Context{Ctx: context.Background()}, taskArgs("look"))
	if !result.IsError {
		t.Fatal("a failing subagent was reported as a success")
	}
	if !strings.Contains(result.Text, "context deadline exceeded") {
		t.Errorf("result = %q, want the child's reason", result.Text)
	}
}

// TestTaskToolRefusesAnEmptyPrompt stops a child being launched with nothing to
// do, which would spend tokens to produce an answer nobody asked for.
func TestTaskToolRefusesAnEmptyPrompt(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{Text: "x"}}

	for _, prompt := range []string{"", "   ", "\t\n"} {
		result := testTaskTool(spawner).Execute(
			agent.Context{Ctx: context.Background()}, taskArgs(prompt))
		if !result.IsError {
			t.Errorf("prompt %q was accepted", prompt)
		}
	}
	if spawner.calls != 0 {
		t.Errorf("spawn calls = %d, want 0 for a rejected prompt", spawner.calls)
	}
}

// TestTaskToolRequiresAPrompt keeps a missing argument from reading as an empty
// one, which would be a confusing message about a key the model never sent.
func TestTaskToolRequiresAPrompt(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{Text: "x"}}
	result := testTaskTool(spawner).Execute(agent.Context{Ctx: context.Background()}, map[string]any{})
	if !result.IsError {
		t.Fatal("a missing prompt was accepted")
	}
	if !strings.Contains(result.Text, "prompt") {
		t.Errorf("result = %q, want it to name the missing argument", result.Text)
	}
}

// TestTaskToolReportsEmptyOutputAsAnError is a silent-wrong-answer guard: an
// empty child result must not become an empty tool result the parent reads as a
// valid answer.
func TestTaskToolReportsEmptyOutputAsAnError(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{Text: "  "}}
	result := testTaskTool(spawner).Execute(
		agent.Context{Ctx: context.Background()}, taskArgs("look"))
	if !result.IsError {
		t.Fatal("an empty subagent answer was reported as a success")
	}
}

// TestTaskToolPassesBackgroundSeparated keeps a subagent from mistaking the
// parent's background note for its own assignment.
func TestTaskToolPassesBackgroundSeparated(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{Text: "ok", ReadOnly: true}}

	_, err := runTaskWithArgs(t, spawner, map[string]any{
		"prompt":  "review src/auth",
		"context": "the repo uses snake_case",
	})
	if err != nil {
		t.Fatal(err)
	}

	system := spawner.spec.System
	if len(system) < 2 {
		t.Fatalf("system = %d blocks, want the prompt plus a background block", len(system))
	}
	background := system[len(system)-1].Text
	if !strings.Contains(background, "snake_case") {
		t.Errorf("background block = %q, want the supplied context", background)
	}
	if !strings.Contains(background, "Background") {
		t.Errorf("background block = %q, want it labelled so it is not read as the assignment", background)
	}
}

func runTaskWithArgs(t *testing.T, spawner Spawner, args map[string]any) (agent.Result, error) {
	t.Helper()
	result := testTaskTool(spawner).Execute(agent.Context{Ctx: context.Background()}, args)
	if result.IsError {
		return result, errors.New(result.Text)
	}
	return result, nil
}

// TestTaskToolForwardsTheReadOnlyRequest proves the model's restriction reaches
// the engine, where it is enforced against the actual tool set.
func TestTaskToolForwardsTheReadOnlyRequest(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{Text: "ok", ReadOnly: true}}

	_, err := runTaskWithArgs(t, spawner, map[string]any{
		"prompt":   "look around",
		"readOnly": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !spawner.spec.ReadOnly {
		t.Error("the read-only request did not reach the subagent spec")
	}
}

// TestTaskToolDefaultsToWritable keeps the restriction opt-in, so a subagent
// asked to make a change is not silently confined to reading.
func TestTaskToolDefaultsToWritable(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{Text: "ok"}}
	_, err := runTaskWithArgs(t, spawner, taskArgs("fix the typo"))
	if err != nil {
		t.Fatal(err)
	}
	if spawner.spec.ReadOnly {
		t.Error("a task without readOnly was restricted anyway")
	}
}

// TestTaskToolRequiresBothDependencies refuses to run half-configured, because
// a silent no-op subagent would look like a working one.
func TestTaskToolRequiresBothDependencies(t *testing.T) {
	tool := NewTaskTool(nil, 0, 1, nil)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, taskArgs("x"))
	if !result.IsError || !strings.Contains(result.Text, "spawner") {
		t.Errorf("result = %q, want it to name the missing spawner", result.Text)
	}

	tool = NewTaskTool(&scriptedSpawner{result: agent.SubagentResult{Text: "ok"}}, 0, 1, nil)
	result = tool.Execute(agent.Context{Ctx: context.Background()}, taskArgs("x"))
	if !result.IsError || !strings.Contains(result.Text, "system prompt") {
		t.Errorf("result = %q, want it to name the missing system prompt", result.Text)
	}
}

// TestTaskToolRejectsAnEmptySystemPrompt catches a builder that returned nothing.
func TestTaskToolRejectsAnEmptySystemPrompt(t *testing.T) {
	tool := NewTaskTool(&scriptedSpawner{result: agent.SubagentResult{Text: "ok"}},
		0, 1, func(string) []ai.SystemPromptBlock { return nil })

	result := tool.Execute(agent.Context{Ctx: context.Background()}, taskArgs("x"))
	if !result.IsError || !strings.Contains(result.Text, "system prompt") {
		t.Errorf("result = %q, want it to name the empty system prompt", result.Text)
	}
}

// TestTaskToolHonoursCancellation stops a child from being spawned into a
// context that is already gone, which would spend tokens on work nobody waits
// for.
func TestTaskToolHonoursCancellation(t *testing.T) {
	spawner := &scriptedSpawner{result: agent.SubagentResult{Text: "ok"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := testTaskTool(spawner).Execute(agent.Context{Ctx: ctx}, taskArgs("x"))
	if !result.IsError {
		t.Fatal("a cancelled context did not stop the tool")
	}
	if spawner.calls != 0 {
		t.Errorf("spawn calls = %d, want 0 when cancelled", spawner.calls)
	}
}

// TestTaskToolCanNestBoundsRecursion guards against an unbounded tree of agents.
func TestTaskToolCanNestBoundsRecursion(t *testing.T) {
	cases := []struct {
		name     string
		depth    int
		maxDepth int
		want     bool
	}{
		{"no nesting at all", 0, 1, false},
		{"one level allowed", 0, 2, true},
		{"at the cap", 1, 2, false},
		{"past the cap", 2, 2, false},
		{"a zero max is safe", 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewTaskTool(nil, tc.depth, tc.maxDepth, nil)
			if got := tool.canNest(); got != tc.want {
				t.Errorf("canNest() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTaskToolIsReadTier proves delegation is classified as observation, so it
// does not trigger a write approval the parent would then have to explain.
func TestTaskToolIsReadTier(t *testing.T) {
	if got := testTaskTool(&scriptedSpawner{}).Approval().Tier; got != agent.TierRead {
		t.Errorf("tier = %q, want read (a subagent's own tools carry their own tiers)", got)
	}
}

// TestTaskToolSchemaRequiresThePrompt keeps the wire contract honest, since a
// model that omits the prompt would otherwise get a confusing type error.
func TestTaskToolSchemaRequiresThePrompt(t *testing.T) {
	schema := testTaskTool(&scriptedSpawner{}).Parameters()
	if schema == nil {
		t.Fatal("no schema")
	}
	raw := string(schema.RawJSON())
	for _, want := range []string{`"prompt"`, `"required"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("schema = %s, want it to contain %s", raw, want)
		}
	}
}
