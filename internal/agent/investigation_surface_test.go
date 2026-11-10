package agent_test

import (
	"context"
	"errors"
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

// investigationShell models the one capability that made the observed failure
// reproducible: a shell that declares both execute and mutate because a command
// may do either.
type investigationShell struct{}

func (investigationShell) Name() string        { return "bash" }
func (investigationShell) Description() string { return "run a shell command" }
func (investigationShell) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"command":{"type":"string"}},
		"required":["command"]
	}`))
}
func (investigationShell) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierExec} }
func (investigationShell) Exclusive(map[string]any) bool { return true }
func (investigationShell) ActionClasses() []agent.ActionClass {
	return []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}
}
func (investigationShell) Execute(_ agent.Context, args map[string]any) agent.Result {
	command, _ := args["command"].(string)
	return agent.Result{
		Text:     "command output",
		Details:  map[string]any{"command": command},
		Executes: true,
		Finished: true,
	}
}

// investigationCall is one scripted shell call.
func investigationCall(id, command string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID: id, Name: "bash", Arguments: map[string]any{"command": command},
		}},
		StopReason: ai.StopToolUse,
	}
}

// capabilityShell reports the tier of the concrete command, like the real
// BashTool: a redirect is a write even when the command only reads. This is
// what makes grounding refuse a scratch probe while allowing `git log`.
type capabilityShell struct{ investigationShell }

func (capabilityShell) Capability(args map[string]any) agent.Tier {
	command, _ := args["command"].(string)
	if strings.Contains(command, ">") {
		return agent.TierWrite
	}
	return agent.TierExec
}

// A FUZZ investigation must be able to run more than the first command. The
// observed failure treated a successful `git log` as completion, withdrew the
// tool surface, and refused the model's next inspection until the task blocked.
func TestInvestigationKeepsExecutingAfterAnObservation(t *testing.T) {
	prompt := "investigate the bottleneck and evaluate"
	intent := workspace.BuildIntentContract(prompt)
	if intent.Kind != contract.ObjectiveInformational {
		t.Fatalf("contract kind = %s, want informational", intent.Kind)
	}
	facts := cognition.ProbeFacts(t.TempDir())
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "fuzz", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	script := []ai.AssistantMessage{
		investigationCall("1", "git log --oneline -10"),
		investigationCall("2", "cat index.html"),
		{Content: []ai.ContentBlock{ai.TextContent{
			Text: "The bottleneck is in evaluate: it rescans every row, so the work grows quadratically.",
		}}, StopReason: ai.StopEndTurn},
	}

	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script; the investigation did not reach its answer", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:     []agent.Tool{investigationShell{}},
		Project:   projector.Project,
		Authorize: allowRead{},
		MaxTurns:  6,
	})
	if err != nil {
		t.Fatal(err)
	}

	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})
	if runErr != nil {
		t.Fatalf("Run error = %v, want the investigation to answer", runErr)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("model requests = %d, want observation, inspection, answer (3)", got)
	}
	executed := 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Refused() {
				t.Fatalf("the investigation refused %q: %s", outcome.ToolName, outcome.Result.Text)
			}
			executed++
		}
	}
	if executed != 2 {
		t.Fatalf("executed calls = %d, want both shell observations", executed)
	}
	if verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{Turns: turns, Contract: intent}); !verdict.Complete() {
		t.Fatalf("investigation completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

// The reported answer never says "evaluate" and ends by reporting that the run
// left the workspace untouched. Both are faithful: the request names an
// evaluation deliverable, not an entity called "evaluate", and a no-op change
// report is not a claim that the repository contains no files. Completion must
// accept it instead of returning "needs review".
func TestInvestigationAnswerWithoutRequestVerbOrFileClaimCompletes(t *testing.T) {
	prompt := "investigate the bottleneck and evaluate"
	intent := workspace.BuildIntentContract(prompt)
	if intent.Kind != contract.ObjectiveInformational {
		t.Fatalf("contract kind = %s, want informational", intent.Kind)
	}

	probe := func(command string) agent.Turn {
		return agent.Turn{Results: []agent.ToolOutcome{{
			ToolName: "bash",
			Args:     map[string]any{"command": command},
			Result:   agent.Result{Text: "command output", Executes: true, Finished: true},
		}}}
	}
	answer := "Bottleneck: external Google Fonts, not the code.\n\n" +
		"Ranked findings, all measured: 4 woff2 files are 170.8 KB on a second origin; " +
		"the local document and CSS total 25.7 KB. The static server sends no " +
		"Content-Encoding and no Cache-Control, so every load re-downloads uncompressed bytes.\n\n" +
		"Not verified: paint timings and CLS were not observed in a real browser; " +
		"launching one was refused as a mutating action.\n\n" +
		"Workspace unchanged — git status is clean; no files were modified."
	turns := []agent.Turn{
		probe("git log --oneline -10"),
		probe("git status --short"),
		{Assistant: ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: answer}},
			StopReason: ai.StopEndTurn,
		}},
	}
	verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{Turns: turns, Contract: intent})
	if !verdict.Complete() {
		t.Fatalf("investigation completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

// refusedProbeRun is one scripted investigation whose last proposals may be
// refused. toolCounts records the offered surface of every provider request.
type refusedProbeRun struct {
	turns        []agent.Turn
	toolCounts   []int
	answerNudged bool
	intent       contract.IntentContract
	err          error
}

func runRefusedProbeScript(t *testing.T, script []ai.AssistantMessage) refusedProbeRun {
	t.Helper()
	const prompt = "investigate the bottleneck and evaluate in this project"
	intent := workspace.BuildIntentContract(prompt)
	facts := cognition.ProbeFacts(t.TempDir())
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "fuzz", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	var toolCounts []int
	answerNudged := false
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			toolCounts = append(toolCounts, len(req.Tools))
			if len(req.Messages) > 0 {
				if user, ok := req.Messages[len(req.Messages)-1].(ai.UserMessage); ok &&
					user.Text() == "runtime answer request" {
					answerNudged = true
				}
			}
			index := len(toolCounts) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:         []agent.Tool{capabilityShell{}},
		Project:       projector.Project,
		Grounding:     intent.Grounding,
		Authorize:     allowRead{},
		MaxTurns:      8,
		AnswerRequest: "runtime answer request",
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})
	return refusedProbeRun{
		turns: turns, toolCounts: toolCounts, answerNudged: answerNudged,
		intent: intent, err: runErr,
	}
}

// Two consecutive turns of refused probes must not end the run with a hard
// block. Nothing the model proposes can execute, so the runtime closes the
// action surface and asks for the answer from the evidence it already has.
// The observed failure blocked with "the runtime refused every proposed action
// and the model did not answer" after exactly this sequence.
func TestInvestigationAnswersAfterRepeatedRefusedProbes(t *testing.T) {
	answer := "Bottleneck: external Google Fonts, not the code. " +
		"Measured 170.8 KB of woff2 files on a second origin. Not verified: " +
		"paint timings were not observed. Workspace unchanged; no files were modified."
	result := runRefusedProbeScript(t, []ai.AssistantMessage{
		investigationCall("1", "git log --oneline -10"),
		investigationCall("2", "cd /tmp && curl -s https://fonts.googleapis.com/css2?family=Inter > inter.css"),
		investigationCall("3", "cd /tmp && curl -s https://fonts.googleapis.com/css2?family=Space+Grotesk > grotesk.css"),
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}, StopReason: ai.StopEndTurn},
	})
	if result.err != nil {
		t.Fatalf("Run error = %v, want the refused probes to end in an answer", result.err)
	}
	if len(result.toolCounts) != 4 {
		t.Fatalf("model requests = %d, want observation, two refusals, forced answer (4)", len(result.toolCounts))
	}
	if got := result.toolCounts[3]; got != 0 {
		t.Fatalf("forced-answer request offered %d tools, want 0", got)
	}
	refused := 0
	for _, turn := range result.turns {
		for _, outcome := range turn.Results {
			if outcome.Refused() {
				refused++
			}
		}
	}
	if refused != 2 {
		t.Fatalf("refused outcomes = %d, want both scratch writes refused", refused)
	}
	if verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
	}); !verdict.Complete() {
		t.Fatalf("investigation completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

// A model can answer the refusal with silence instead of another proposal. The
// run must not treat that empty stop as the final answer: it has seen the
// refusal and has not produced the answer the request asked for. The loop
// closes the surface once and asks for the answer from the evidence already
// gathered.
func TestInvestigationAnswersAfterRefusedProbeThenEmptyStop(t *testing.T) {
	answer := "Bottleneck: the page loads four external font files before it " +
		"renders. Not verified: browser paint timings were not observed. " +
		"Workspace unchanged; no files were modified."
	result := runRefusedProbeScript(t, []ai.AssistantMessage{
		investigationCall("1", "git log --oneline -10"),
		investigationCall("2", "cd /tmp && curl -s https://fonts.googleapis.com/css2?family=Inter > inter.css"),
		{StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}, StopReason: ai.StopEndTurn},
	})
	if result.err != nil {
		t.Fatalf("Run error = %v, want an answer after the empty stop", result.err)
	}
	if len(result.toolCounts) != 4 {
		t.Fatalf("model requests = %d, want observation, refusal, empty stop, forced answer (4)", len(result.toolCounts))
	}
	if got := result.toolCounts[3]; got != 0 {
		t.Fatalf("forced-answer request offered %d tools, want 0", got)
	}
	last := result.turns[len(result.turns)-1]
	if last.Assistant.Text() != answer {
		t.Fatalf("final answer = %q, want the forced answer", last.Assistant.Text())
	}
	if verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
	}); !verdict.Complete() {
		t.Fatalf("investigation completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

// The forced answer request itself can return no visible text. The surface was
// already closed, so the model has no action left; the lifecycle failure is the
// same one request later. The observed session ended here and recorded an empty
// final assistant message even though the investigation had gathered enough
// evidence to answer. One further answer-only request must convert that silence
// into the user-visible result.
func TestInvestigationAnswersAfterAnswerOnlyEmptyStop(t *testing.T) {
	answer := "Bottleneck: the page blocks rendering on four external font files. " +
		"Measured 170.8 KB of woff2 on a second origin. Not verified: browser " +
		"paint timings were not observed. Workspace unchanged; no files were modified."
	result := runRefusedProbeScript(t, []ai.AssistantMessage{
		investigationCall("1", "git log --oneline -10"),
		investigationCall("2", "cd /tmp && curl -s https://fonts.googleapis.com/css2?family=Inter > inter.css"),
		investigationCall("3", "cd /tmp && curl -s https://fonts.googleapis.com/css2?family=Space+Grotesk > grotesk.css"),
		{StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}, StopReason: ai.StopEndTurn},
	})
	if result.err != nil {
		t.Fatalf("Run error = %v, want an answer after the empty answer-only stop", result.err)
	}
	if len(result.toolCounts) != 5 {
		t.Fatalf("model requests = %d, want observation, two refusals, empty answer, answered retry (5)", len(result.toolCounts))
	}
	if got := result.toolCounts[3]; got != 0 {
		t.Fatalf("forced-answer request offered %d tools, want 0", got)
	}
	if got := result.toolCounts[4]; got != 0 {
		t.Fatalf("answer retry offered %d tools, want 0", got)
	}
	if !result.answerNudged {
		t.Fatal("the answer retry did not append the runtime answer request")
	}
	last := result.turns[len(result.turns)-1]
	if last.Assistant.Text() != answer {
		t.Fatalf("final answer = %q, want the retried answer", last.Assistant.Text())
	}
	if verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
	}); !verdict.Complete() {
		t.Fatalf("investigation completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

// The forced answer is one bounded request, not an unbounded recovery loop. A
// model that ignores the closed surface emits one more refused call and the
// run stops with a surfaced error.
func TestRepeatedRefusedProbesStopAfterOneAnswerOnlyRequest(t *testing.T) {
	result := runRefusedProbeScript(t, []ai.AssistantMessage{
		investigationCall("1", "git log --oneline -10"),
		investigationCall("2", "cd /tmp && curl -s https://fonts.googleapis.com/css2?family=Inter > a.css"),
		investigationCall("3", "cd /tmp && curl -s https://fonts.googleapis.com/css2?family=Inter:ital > b.css"),
		investigationCall("4", "cd /tmp && curl -s https://fonts.googleapis.com/css2?family=Inter:wght > c.css"),
	})
	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if len(result.toolCounts) != 4 {
		t.Fatalf("model requests = %d, want observation, two refusals, one answer-only request (4)", len(result.toolCounts))
	}
	if got := result.toolCounts[3]; got != 0 {
		t.Fatalf("answer-only request offered %d tools, want 0", got)
	}
}
