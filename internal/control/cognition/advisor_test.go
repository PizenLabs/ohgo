package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
)

type adviceTool struct{ name string }

func (f adviceTool) Name() string                   { return f.name }
func (f adviceTool) Description() string            { return "test tool" }
func (f adviceTool) Parameters() *jsonschema.Schema { return nil }
func (f adviceTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierRead} }
func (f adviceTool) Exclusive(map[string]any) bool  { return false }
func (f adviceTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{}
}

type fakeAdvisor struct {
	advice Advice
	req    AdviceRequest
}

func (f *fakeAdvisor) Advise(req AdviceRequest) Advice {
	f.req = req
	return f.advice
}

// A preference is guidance, not a tool-list rewrite: tool definitions precede
// the conversation on every wire protocol, so reordering them between turns
// would invalidate the provider's cached prefix to convey something the
// advisory line says outright.
func TestPreferredToolsBecomeAdviceWithoutReordering(t *testing.T) {
	tools := []agent.Tool{adviceTool{"read"}, adviceTool{"grep"}, adviceTool{"write"}}
	line := preferredToolHint([]string{"write", "missing", "read"}, tools)
	if !strings.Contains(line, "write") || !strings.Contains(line, "read") {
		t.Fatalf("hint = %q, want the preferred names", line)
	}
	if strings.Contains(line, "missing") {
		t.Fatalf("hint = %q, want only offered names", line)
	}
	preferred := BuildShape(contract.IntentContract{Objective: "inspect", Kind: contract.ObjectiveInformational}, ShapeParams{Authority: "ask", Intent: "read"})
	projector := NewProjector(preferred, NewEvidenceState(), RepositoryFacts{})
	projector.Advisor = &fakeAdvisor{advice: Advice{PreferredTools: []string{"write", "read"}}}
	result := projector.Project(agent.ProjectionRequest{
		Tools: tools,
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{{
			CallID:   "1",
			ToolName: "read",
			Args:     map[string]any{"path": "src/main.go"},
			Result:   agent.Result{Text: "package parser", Details: map[string]any{"path": "src/main.go"}},
		}}}},
	})
	if names := toolNameList(result.Tools); strings.Join(names, ",") != "read,grep,write" {
		t.Fatalf("tools = %v, want the loop's own order", names)
	}
	state := ""
	if len(result.Runtime) > 0 {
		state = result.Runtime[0]
	}
	if !strings.Contains(state, "prefer these offered tools") {
		t.Fatalf("runtime state = %q, want the advisory preference line", state)
	}
}

func TestProjectorAppliesExperienceWithoutWideningTheSurface(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "inspect the parser",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read"})
	evidence := NewEvidenceState()
	projector := NewProjector(shape, evidence, RepositoryFacts{})
	advisor := &fakeAdvisor{advice: Advice{
		PreferredTools: []string{"grep", "not-offered"},
		Hints:          []string{"prefer the bounded observation"},
		Matched:        []string{"experience:1"},
	}}
	projector.Advisor = advisor
	tools := []agent.Tool{adviceTool{"read"}, adviceTool{"grep"}, adviceTool{"write"}}
	req := agent.ProjectionRequest{
		Tools: tools,
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{{
			CallID:   "1",
			ToolName: "read",
			Args:     map[string]any{"path": "src/main.go"},
			Result:   agent.Result{Text: "package parser", Details: map[string]any{"path": "src/main.go"}},
		}}}},
	}
	result := projector.Project(req)
	if len(advisor.req.Tools) != 3 {
		t.Fatalf("advisor saw %d tools, want the pre-existing 3", len(advisor.req.Tools))
	}
	if names := toolNameList(result.Tools); strings.Join(names, ",") != "read,grep,write" {
		t.Fatalf("tools = %v, want the offered order preserved", names)
	}
	for _, tool := range result.Tools {
		if tool.Name() == "not-offered" {
			t.Fatal("the advisor introduced a tool that was not offered")
		}
	}
	state := ""
	if len(result.Runtime) > 0 {
		state = result.Runtime[0]
	}
	if !strings.Contains(state, "Experience guidance (advisory, not authority)") {
		t.Fatalf("state = %q, want advisory guidance", state)
	}
	if !strings.Contains(state, "prefer the bounded observation") {
		t.Fatalf("state = %q, want the advisor hint", state)
	}
	if len(result.Report.ExperienceApplied) != 1 || result.Report.ExperienceApplied[0] != "experience:1" {
		t.Fatalf("report experience = %v, want the matched record", result.Report.ExperienceApplied)
	}
}

type panicAdvisor struct{}

func (panicAdvisor) Advise(AdviceRequest) Advice { panic("advisor failure") }

func TestExperienceAdviceIsBounded(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "inspect the parser",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read"})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{})
	hints := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		hints = append(hints, "hint with enough text to consume the state budget "+string(rune('a'+i)))
	}
	tools := []agent.Tool{adviceTool{"read"}, adviceTool{"grep"}, adviceTool{"ls"}}
	projector.Advisor = &fakeAdvisor{advice: Advice{
		PreferredTools: []string{"read", "grep", "ls"},
		Hints:          hints,
	}}
	result := projector.Project(agent.ProjectionRequest{
		Tools: tools,
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{{
			CallID:   "1",
			ToolName: "read",
			Args:     map[string]any{"path": "src/main.go"},
			Result:   agent.Result{Text: "package parser", Details: map[string]any{"path": "src/main.go"}},
		}}}},
	})
	// The advisor's preference is rendered as one further advisory line, and
	// the rendered block stays inside its byte budget either way.
	if len(result.Report.ExperienceHints) > 4 {
		t.Fatalf("hints = %d, want at most 4", len(result.Report.ExperienceHints))
	}
	state := ""
	if len(result.Runtime) > 0 {
		state = result.Runtime[0]
	}
	if len(state) > DefaultStateBlockBytes+8 {
		t.Fatalf("state bytes = %d, want bounded by %d", len(state), DefaultStateBlockBytes)
	}
}

func TestPanickingAdvisorDegradesToColdStart(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "inspect the parser",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read"})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{})
	projector.Advisor = panicAdvisor{}
	result := projector.Project(agent.ProjectionRequest{
		Tools: []agent.Tool{adviceTool{"read"}, adviceTool{"grep"}},
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{{
			CallID:   "1",
			ToolName: "read",
			Args:     map[string]any{"path": "src/main.go"},
			Result:   agent.Result{Text: "package parser", Details: map[string]any{"path": "src/main.go"}},
		}}}},
	})
	if names := toolNameList(result.Tools); strings.Join(names, ",") != "read,grep" {
		t.Fatalf("tools = %v, want the original order after an advisor panic", names)
	}
	if len(result.Report.ExperienceApplied) != 0 || len(result.Report.ExperienceHints) != 0 {
		t.Fatalf("report = %+v, want no applied experience after a panic", result.Report)
	}
}

func TestProjectorWithoutAdvisorIsUnchanged(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "inspect the parser",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read"})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{})
	tools := []agent.Tool{adviceTool{"read"}, adviceTool{"grep"}}
	result := projector.Project(agent.ProjectionRequest{
		Tools: tools,
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{{
			CallID:   "1",
			ToolName: "read",
			Args:     map[string]any{"path": "src/main.go"},
			Result:   agent.Result{Text: "package parser", Details: map[string]any{"path": "src/main.go"}},
		}}}},
	})
	if names := toolNameList(result.Tools); strings.Join(names, ",") != "read,grep" {
		t.Fatalf("tools = %v, want the original order", names)
	}
	for _, block := range result.Runtime {
		if strings.Contains(block, "Experience guidance") {
			t.Fatal("nil advisor produced experience guidance")
		}
	}
}

func toolNameList(tools []agent.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}
