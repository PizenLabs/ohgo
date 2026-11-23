package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

type reviewReadTool struct {
	text string
	n    atomic.Int64
}

func (t *reviewReadTool) Name() string        { return "read" }
func (t *reviewReadTool) Description() string { return "read fixture" }
func (t *reviewReadTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (t *reviewReadTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (t *reviewReadTool) Exclusive(map[string]any) bool { return false }
func (t *reviewReadTool) Execute(Context, map[string]any) Result {
	t.n.Add(1)
	return Result{Text: t.text, Details: map[string]any{"path": "components/TerminalBlog.tsx"}}
}

type reviewStream struct {
	responses []ai.AssistantMessage
	index     int
	requests  []ai.Request
}

func (s *reviewStream) fn() ai.StreamFn {
	return func(_ context.Context, req ai.Request) (*ai.Stream, error) {
		s.requests = append(s.requests, req)
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
	var reported []Turn
	loop.SetTurnHook(func(turn Turn) { reported = append(reported, turn) })
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "report the tests"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want the rejected answer plus one correction", len(turns))
	}
	if len(reported) != 2 || !reported[0].Suppressed || reported[1].Suppressed {
		t.Fatalf("reported turns = %+v, want the candidate suppressed before publication", reported)
	}
	if got := turns[len(turns)-1].Assistant.Text(); !strings.Contains(got, "Tests: 4") {
		t.Fatalf("final answer = %q, want the corrected facet", got)
	}
	if !turns[0].Suppressed {
		t.Fatal("the rejected candidate was not suppressed")
	}
	if turns[len(turns)-1].Suppressed {
		t.Fatal("the accepted answer was suppressed")
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
	if read.n.Load() != 1 {
		t.Fatalf("read executions = %d, want 1", read.n.Load())
	}
	if got := turns[len(turns)-1].Assistant.Text(); !strings.Contains(got, "publishedAt") {
		t.Fatalf("final answer = %q, want the corrected prop", got)
	}
}

// The correction request names only the rejected claims, not a rewritten
// answer. Each line carries the evidence class and the permitted action, and
// the list is capped so a broad rejection stays a bounded repair.
func TestClaimRepairDirectiveListsOnlyRejectedClaims(t *testing.T) {
	repairs := []contract.GroundingIssue{
		{Code: "doc", Detail: "documentation-only term htmx", Claim: "htmx", Category: "attribution", Requirement: "source observation or attribution", Action: "attribute or verify"},
		{Code: "abs", Detail: "tests treated as absent", Claim: "tests", Category: "absence", Requirement: "complete inventory", Action: "inspect or bound"},
	}
	got := completionRepairDirective(
		"count={{count}} budget={{budget}}\n{{repairs}}\nreason={{reason}}",
		"fallback {{reason}}", "the audit reason", repairs, 2)
	for _, want := range []string{
		"count=2", "budget=2", `"htmx"`, `"tests"`, "required: complete inventory",
		"action: attribute or verify", "reason=the audit reason",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("directive missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fallback") {
		t.Fatalf("the prose fallback replaced the claim list:\n%s", got)
	}

	many := make([]contract.GroundingIssue, 0, 8)
	for index := 0; index < 8; index++ {
		many = append(many, contract.GroundingIssue{Code: "x", Detail: "d", Claim: "claim", Category: "absence"})
	}
	if got := completionRepairDirective("{{count}}/{{repairs}}", "fb", "r", many, 1); !strings.HasPrefix(got, "5/") {
		t.Fatalf("repair list was not capped: %s", got)
	}
	if got := completionRepairDirective("{{count}}", "fallback {{reason}}", "r", nil, 1); got != "fallback r" {
		t.Fatalf("no repairs did not fall back to prose: %q", got)
	}
}

// The correction window has its own small call bound. A model that tries to
// reopen the whole investigation after one rejected claim executes only the
// allowance; the remaining calls are refused before the tool runs.
func TestCorrectionBudgetBoundsToolCalls(t *testing.T) {
	intent := reviewIntent(contract.FacetTests)
	read := &reviewReadTool{text: "source body"}
	var calls []ai.ContentBlock
	for index := 0; index < 5; index++ {
		calls = append(calls, ai.ToolCall{
			ID: fmt.Sprintf("call-%d", index), Name: "read",
			Arguments: map[string]any{"path": fmt.Sprintf("src/%d.go", index)},
		})
	}
	stream := &reviewStream{responses: []ai.AssistantMessage{
		reviewAnswer("This is a Next.js blog."),
		{Content: calls, StopReason: ai.StopToolUse},
		reviewAnswer("Tests: 4 associated test files."),
	}}
	loop := newReviewLoop(t, stream.fn(), read, intent, testReviewDirective)
	loop.cfg.RepairRequest = "Repairs: {{count}}; budget: {{budget}}\n{{repairs}}\nreason: {{reason}}"
	loop.cfg.CorrectionToolCalls = 2
	loop.SetInformationalRepairs(intent)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "report the tests"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if read.n.Load() != 2 {
		t.Fatalf("correction executed %d tool calls, want 2", read.n.Load())
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want answer, bounded correction, answer", len(turns))
	}
	if got := turns[len(turns)-1].Assistant.Text(); !strings.Contains(got, "Tests: 4") {
		t.Fatalf("final answer = %q, want the repaired facet", got)
	}
	// The correction request carries the claim list and the budget, not just
	// the prose reason.
	found := false
	for _, request := range stream.requests {
		if strings.Contains(strings.Join(messageTexts(request.Messages), "\n"), "Repairs: 1; budget: 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the correction request did not carry the claim-level directive: %+v", stream.requests)
	}
}

// A correction may need more than one model turn: read one thing, then
// another. The reopened surface lasts for the whole correction window, not
// only the first request, and the budget still bounds it.
func TestCorrectionSurfaceSpansMultipleRequests(t *testing.T) {
	intent := reviewIntent(contract.FacetTests)
	read := &reviewReadTool{text: "source body"}
	stream := &reviewStream{responses: []ai.AssistantMessage{
		reviewAnswer("This is a Next.js blog."),
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "read-a", Name: "read", Arguments: map[string]any{"path": "src/a.go"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "read-b", Name: "read", Arguments: map[string]any{"path": "src/b.go"}}}, StopReason: ai.StopToolUse},
		reviewAnswer("Tests: 4 associated test files."),
	}}
	loop := newReviewLoop(t, stream.fn(), read, intent, testReviewDirective)
	loop.cfg.CorrectionToolCalls = 3
	loop.SetInformationalRepairs(intent)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "report the tests"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if read.n.Load() != 2 {
		t.Fatalf("correction reads = %d, want two across the correction window", read.n.Load())
	}
	if len(turns) != 4 {
		t.Fatalf("turns = %d, want answer, read, read, answer", len(turns))
	}
	if got := turns[len(turns)-1].Assistant.Text(); !strings.Contains(got, "Tests: 4") {
		t.Fatalf("final answer = %q", got)
	}
	// The repair instruction must follow every request in the correction
	// window. Withholding it after the first read leaves the next turn with
	// only the generic state block, so it follows an unrelated frontier.
	directives := 0
	for _, request := range stream.requests {
		if strings.Contains(strings.Join(messageTexts(request.Messages), "\n"), "Runtime completion review:") {
			directives++
		}
	}
	if directives != 3 {
		t.Fatalf("correction directive carried by %d request(s), want every request in the correction window", directives)
	}
}

// Three model texts — a rejected candidate, an answer abandoned for another
// tool call, and the final answer — must publish exactly one conclusion.
func TestOnlyTheFinalAnswerIsPublished(t *testing.T) {
	intent := reviewIntent(contract.FacetTests)
	stream := &reviewStream{responses: []ai.AssistantMessage{
		reviewAnswer("This is a Next.js blog."),
		{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "I will replace this answer after one more read."},
				ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"path": "components/TerminalBlog.tsx"}},
			},
			StopReason: ai.StopToolUse,
		},
		reviewAnswer("Tests: 4 associated test files."),
	}}
	loop := newReviewLoop(t, stream.fn(), &reviewReadTool{}, intent, testReviewDirective)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "report the tests"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var published []string
	for _, turn := range turns {
		if text := strings.TrimSpace(TranscriptTurn(turn).Assistant.Text()); text != "" {
			published = append(published, text)
		}
	}
	if len(published) != 1 {
		t.Fatalf("published %d answers: %q", len(published), published)
	}
	if !strings.Contains(published[0], "Tests: 4") {
		t.Fatalf("published %q, want only the final answer", published[0])
	}
}

// A plan-only answer is rejected before it can consume the completion
// verdict. The loop withholds the text from the transcript and spends its one
// correction window asking for the answer the user requested.
func TestReviewInformationalAnswerRejectsAPlanOnlyMessage(t *testing.T) {
	intent := contract.IntentContract{Objective: "analyze this project", Kind: contract.ObjectiveInformational}
	reason := ReviewInformationalAnswer(intent, nil, "Let me read the main module and then explain the project.")
	if strings.TrimSpace(reason) == "" {
		t.Fatal("a plan-only message was accepted as an answer")
	}
	if !strings.Contains(strings.ToLower(reason), "answer") {
		t.Fatalf("reason = %q, want a request for the actual answer", reason)
	}
	if got := ReviewInformationalAnswer(intent, nil, "This project is a Go coding agent."); got != "" {
		t.Fatalf("a substantive answer was rejected: %q", got)
	}
}
