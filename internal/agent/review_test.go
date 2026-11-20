package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

type reviewReadTool struct {
	text string
	n    int
}

func (t *reviewReadTool) Name() string        { return "read" }
func (t *reviewReadTool) Description() string { return "read fixture" }
func (t *reviewReadTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (t *reviewReadTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (t *reviewReadTool) Exclusive(map[string]any) bool { return false }
func (t *reviewReadTool) Execute(Context, map[string]any) Result {
	t.n++
	return Result{Text: t.text, Details: map[string]any{"path": "components/TerminalBlog.tsx"}}
}

type reviewStream struct {
	responses []ai.AssistantMessage
	index     int
}

func (s *reviewStream) fn() ai.StreamFn {
	return func(_ context.Context, _ ai.Request) (*ai.Stream, error) {
		if s.index >= len(s.responses) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
		response := s.responses[s.index]
		s.index++
		return ai.NewCompletedStream(response), nil
	}
}

func reviewAnswer(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}
}

func reviewIntent(facets ...contract.Facet) contract.IntentContract {
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	for _, facet := range facets {
		intent.RequiredFacets = append(intent.RequiredFacets, contract.RequiredFacet{Facet: facet})
	}
	return intent
}

const testReviewDirective = "Runtime completion review: {{reason}}. Correct the answer or state the limitation."

func newReviewLoop(t *testing.T, stream ai.StreamFn, tool Tool, intent contract.IntentContract, directive string) *Loop {
	t.Helper()
	loop, err := New(Config{
		Model:                   catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:                  stream,
		Tools:                   []Tool{tool},
		Grounding:               intent.Grounding,
		CompletionReviewRequest: directive,
		AnswerReview: func(turns []Turn, answer string) string {
			return ReviewInformationalAnswer(intent, turns, answer)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

// A missing required facet can be corrected before the run ends. The loop gets
// exactly one correction request, and the corrected answer becomes the final
// turn the completion evaluator sees.
func TestAnswerReviewCorrectsMissingFacetOnce(t *testing.T) {
	intent := reviewIntent(contract.FacetTests)
	stream := &reviewStream{responses: []ai.AssistantMessage{
		reviewAnswer("This is a Next.js blog."),
		reviewAnswer("Tests: 4 associated test files."),
	}}
	loop := newReviewLoop(t, stream.fn(), &reviewReadTool{}, intent, testReviewDirective)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "report the tests"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want the rejected answer plus one correction", len(turns))
	}
	if got := turns[len(turns)-1].Assistant.Text(); !strings.Contains(got, "Tests: 4") {
		t.Fatalf("final answer = %q, want the corrected facet", got)
	}
}

// The correction is bounded. A model that repeats the unsupported answer is
// returned to the host, which reports NEEDS_REVIEW; the loop never spins.
func TestAnswerReviewStopsAfterOneCorrection(t *testing.T) {
	intent := reviewIntent(contract.FacetTests)
	stream := &reviewStream{responses: []ai.AssistantMessage{
		reviewAnswer("This is a Next.js blog."),
		reviewAnswer("This is a Next.js blog."),
		reviewAnswer("This is a Next.js blog."),
	}}
	loop := newReviewLoop(t, stream.fn(), &reviewReadTool{}, intent, testReviewDirective)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "report the tests"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want exactly one correction attempt", len(turns))
	}
}

// Without a host-authorized directive the loop must not spend a turn on an
// empty correction request.
func TestAnswerReviewWithoutDirectiveDoesNotRetry(t *testing.T) {
	intent := reviewIntent(contract.FacetTests)
	stream := &reviewStream{responses: []ai.AssistantMessage{
		reviewAnswer("This is a Next.js blog."),
		reviewAnswer("Tests: 4 associated test files."),
	}}
	loop := newReviewLoop(t, stream.fn(), &reviewReadTool{}, intent, "")
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "report the tests"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want no correction without a directive", len(turns))
	}
}

// The corrected answer may use evidence the first answer ignored. This is the
// code-claim failure: read the component, invent a prop, then correct against
// the declaration the run already observed.
func TestAnswerReviewCorrectsInventedProp(t *testing.T) {
	intent := reviewIntent()
	read := &reviewReadTool{text: terminalBlogSource}
	stream := &reviewStream{responses: []ai.AssistantMessage{
		{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "call_read", Name: "read", Arguments: map[string]any{"path": "components/TerminalBlog.tsx"}},
			},
			StopReason: ai.StopToolUse,
		},
		reviewAnswer("`TerminalBlog` accepts a `showDetails` prop."),
		reviewAnswer("`TerminalBlog` accepts a `publishedAt` prop."),
	}}
	loop := newReviewLoop(t, stream.fn(), read, intent, testReviewDirective)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "what props does TerminalBlog accept?"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if read.n != 1 {
		t.Fatalf("read executions = %d, want 1", read.n)
	}
	if got := turns[len(turns)-1].Assistant.Text(); !strings.Contains(got, "publishedAt") {
		t.Fatalf("final answer = %q, want the corrected prop", got)
	}
}
