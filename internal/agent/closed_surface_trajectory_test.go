package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// trajectoryRead reproduces the observed read failures for directory targets
// and the content observations that satisfy the repository survey.
type trajectoryRead struct{}

func (trajectoryRead) Name() string        { return "read" }
func (trajectoryRead) Description() string { return "read a file" }
func (trajectoryRead) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`))
}
func (trajectoryRead) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (trajectoryRead) Exclusive(map[string]any) bool { return false }
func (trajectoryRead) Execute(_ agent.Context, args map[string]any) agent.Result {
	path, _ := args["path"].(string)
	switch path {
	case ".", "backend":
		return agent.Result{Text: path + " is a directory", IsError: true, Details: map[string]any{"path": path}}
	case "README.md":
		return agent.Result{Text: "1\t# ohgo\n2\tA coding agent.", Details: map[string]any{"path": path, "offset": 1, "limit": 2000, "totalLines": 2}}
	case "package.json":
		return agent.Result{Text: "1\t{\"name\":\"ohgo\"}", Details: map[string]any{"path": path, "offset": 1, "limit": 2000, "totalLines": 1}}
	case "backend/main.go":
		return agent.Result{Text: "1\tpackage main", Details: map[string]any{"path": path, "offset": 1, "limit": 2000, "totalLines": 1}}
	}
	return agent.Result{Text: "missing", IsError: true, Details: map[string]any{"path": path}}
}

type trajectoryList struct{}

func (trajectoryList) Name() string        { return "ls" }
func (trajectoryList) Description() string { return "list a directory" }
func (trajectoryList) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"}}}`))
}
func (trajectoryList) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (trajectoryList) Exclusive(map[string]any) bool { return false }
func (trajectoryList) Execute(_ agent.Context, args map[string]any) agent.Result {
	path, _ := args["path"].(string)
	return agent.Result{Text: "README.md\npackage.json\nbackend\n.ohgo", Details: map[string]any{"path": path, "entries": 4}}
}

// trajectoryGry is the external capability the request names. It is offered
// while external knowledge is missing, even though this model never calls it.
type trajectoryGry struct{}

func (trajectoryGry) Name() string        { return "gry:get_architecture" }
func (trajectoryGry) Description() string { return "gry architecture" }
func (trajectoryGry) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{}}`))
}
func (trajectoryGry) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (trajectoryGry) Exclusive(map[string]any) bool { return false }
func (trajectoryGry) ActionClasses() []agent.ActionClass {
	return []agent.ActionClass{agent.ActionExternal, agent.ActionConfigured}
}
func (trajectoryGry) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "architecture observed"}
}

type trajectoryGlob struct{}

func (trajectoryGlob) Name() string        { return "glob" }
func (trajectoryGlob) Description() string { return "find files" }
func (trajectoryGlob) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"pattern":{"type":"string"}}}`))
}
func (trajectoryGlob) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (trajectoryGlob) Exclusive(map[string]any) bool { return false }
func (trajectoryGlob) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "backend/cmd/product-service/main.go"}
}

// A repeated README read after a substantive answer is the smallest version of
// the observed #237 trajectory. The read is still offered, but it is an exact
// observation already in hand, so the answer is terminal and the call is
// continuation.
func TestRepeatedReadAfterAnswerIsDropped(t *testing.T) {
	facts := cognition.RepositoryFacts{Go: true}
	shape := cognition.BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, cognition.ShapeParams{Authority: "ask", Intent: "read", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	const answer = "The project is a Go service with an observed product-service entrypoint."
	result := runSurface(t,
		[]agent.Tool{trajectoryRead{}, trajectoryList{}},
		[]ai.AssistantMessage{
			surfaceCall("read-1", "read", map[string]any{"path": "README.md"}),
			{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: answer},
					ai.ToolCall{ID: "read-2", Name: "read", Arguments: map[string]any{"path": "README.md"}},
				},
				StopReason: ai.StopToolUse,
			},
			surfaceAnswer("should never be requested"),
		}, projector.Project, 6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want the read plus the answer (2)", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	last := result.turns[1]
	if len(last.Results) != 0 {
		t.Fatalf("outcomes = %+v, want no dispatched continuation", last.Results)
	}
	if calls := last.Assistant.ToolCalls(); len(calls) != 0 {
		t.Fatalf("stored tool calls = %+v, want none", calls)
	}
}

// The practical ASK trajectory: documentation and an inventory listing are
// observed, then the model answers while repeating the README read and
// proposing another inventory scan. The source-acquisition stage withholds
// glob, and the read is an exact observation already in hand; neither call can
// add evidence. The answer is the terminal transition, so the loop must
// dispatch neither.
func TestWithheldInventoryCallAfterAnswerIsNotDispatched(t *testing.T) {
	facts := cognition.RepositoryFacts{Go: true}
	shape := cognition.BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, cognition.ShapeParams{Authority: "ask", Intent: "read", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	const answer = "The project is a Go service with an observed product-service entrypoint."
	result := runSurface(t,
		[]agent.Tool{trajectoryRead{}, trajectoryList{}, trajectoryGlob{}},
		[]ai.AssistantMessage{
			{
				Content: []ai.ContentBlock{
					ai.ToolCall{ID: "read", Name: "read", Arguments: map[string]any{"path": "README.md"}},
					ai.ToolCall{ID: "ls", Name: "ls", Arguments: map[string]any{"path": "backend/cmd"}},
				},
				StopReason: ai.StopToolUse,
			},
			{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: answer},
					ai.ToolCall{ID: "glob", Name: "glob", Arguments: map[string]any{"pattern": "backend/**/*.go"}},
					ai.ToolCall{ID: "read", Name: "read", Arguments: map[string]any{"path": "README.md"}},
				},
				StopReason: ai.StopToolUse,
			},
			surfaceAnswer("should never be requested"),
		}, projector.Project, 6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want the observations plus the answer (2)", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	if len(result.reports) != 2 {
		t.Fatalf("projections = %d, want 2", len(result.reports))
	}
	withheld := false
	for _, exclusion := range result.reports[1].Excluded {
		if exclusion.Tool == "glob" && strings.Contains(exclusion.Reason, "source content") {
			withheld = true
		}
	}
	if !withheld {
		t.Fatalf("second projection exclusions = %+v, want glob withheld for source acquisition",
			result.reports[1].Excluded)
	}
	offeredRead := false
	for _, name := range result.reports[1].ToolNames {
		if name == "read" {
			offeredRead = true
		}
	}
	if !offeredRead {
		t.Fatalf("second projection tools = %v, want read still offered", result.reports[1].ToolNames)
	}
	last := result.turns[len(result.turns)-1]
	if last.Assistant.Text() != answer {
		t.Fatalf("answer = %q, want the model's text", last.Assistant.Text())
	}
	if len(last.Results) != 0 {
		t.Fatalf("outcomes = %+v, want no dispatched continuation", last.Results)
	}
	if calls := last.Assistant.ToolCalls(); len(calls) != 0 {
		t.Fatalf("stored tool calls = %+v, want none", calls)
	}
}

// The observed North transcript: seven observations satisfy the repository
// survey, then the model proposes the same withdrawn local tool twice.
// The runtime must reach a semantic terminal decision on the ninth request, not
// walk the turn budget and not reopen the action surface.
func TestObservedClosedSurfaceTrajectoryIsBounded(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/repro\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := "check this project and analyze what this project is about? use gry mcp"
	intent := workspace.BuildIntentContract(prompt)
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "ask", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	call := func(id, name string, args map[string]any) ai.AssistantMessage {
		return ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: args}},
			StopReason: ai.StopToolUse,
		}
	}
	script := []ai.AssistantMessage{
		call("1", "read", map[string]any{"path": "."}),
		call("2", "read", map[string]any{"path": "README.md"}),
		call("3", "ls", map[string]any{"path": "."}),
		call("4", "read", map[string]any{"path": "package.json"}),
		call("5", "read", map[string]any{"path": "backend"}),
		call("6", "ls", map[string]any{"path": "backend"}),
		call("7", "read", map[string]any{"path": "backend/main.go"}),
		call("8", "ls", map[string]any{"path": ".ohgo"}),
		call("9", "ls", map[string]any{"path": ".ohgo"}),
		call("10", "ls", map[string]any{"path": ".ohgo"}),
	}

	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Tools:    []agent.Tool{trajectoryRead{}, trajectoryList{}, trajectoryGry{}},
		MaxTurns: 20,
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script; stale proposals reopened the loop", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Project:   projector.Project,
		Authorize: allowRead{},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})

	if !errors.Is(runErr, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", runErr)
	}
	if errors.Is(runErr, agent.ErrTurnLimit) {
		t.Fatal("the run ended at the turn limit instead of a semantic verdict")
	}
	if got := requests.Load(); got != 9 {
		t.Fatalf("model requests = %d, want 9 (seven observations plus two refusals)", got)
	}
	executed, refused := 0, 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Refused() {
				refused++
				continue
			}
			executed++
		}
	}
	if executed != 7 || refused != 2 {
		t.Fatalf("trajectory = %d executed, %d refused; want 7 and 2", executed, refused)
	}
	last := turns[len(turns)-1]
	if len(last.Results) != 1 || !last.Results[0].Refused() {
		t.Fatalf("final outcomes = %+v, want one refusal", last.Results)
	}
	if !strings.Contains(last.Results[0].Result.Text, "not offered") {
		t.Fatalf("final refusal = %q, want the withheld-capability reason", last.Results[0].Result.Text)
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    turns,
		Contract: intent,
	})
	if got.Complete() {
		t.Fatal("a trajectory with no answer was reported complete")
	}
}
