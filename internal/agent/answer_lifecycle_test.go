package agent_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// An answer the publication gate refuses is not a final answer. The turn stays
// in the completion input so the host can name the exact claims, but no host
// may publish it and the response transaction must report REVIEW rather than
// COMMITTED.
func TestUnapprovedAnswerIsNotCommitted(t *testing.T) {
	const answer = "The project is built with HTMX."
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: answer}},
			}), nil
		},
		Tools: []agent.Tool{evidenceReadTool{}},
		FinalizeAnswer: func([]agent.Turn, string, string) agent.FinalizedAnswer {
			return agent.FinalizedAnswer{
				Text: answer,
				Audit: agent.AnswerAudit{
					Finalized:         true,
					UnsupportedClaims: 1,
					Issues: []contract.GroundingIssue{{
						Code: "unsupported_documentation_claim", Claim: "htmx",
						Category: "attribution",
					}},
				},
			}
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	var reported []agent.Turn
	loop.SetTurnHook(func(turn agent.Turn) { reported = append(reported, turn) })
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "analyze this project"}})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want one candidate", len(turns))
	}
	if !turns[0].Suppressed {
		t.Fatal("the refused candidate was not suppressed")
	}
	if got := agent.TranscriptTurn(turns[0]).Assistant.Text(); got != "" {
		t.Fatalf("the refused candidate remained publishable: %q", got)
	}
	if len(reported) != 1 || !reported[0].Suppressed {
		t.Fatalf("reported turns = %+v, want one suppressed candidate", reported)
	}
	audit := loop.LastResponseAudit()
	if audit.State != agent.ResponseReview {
		t.Fatalf("response state = %s, want REVIEW", audit.State)
	}
	if audit.Answer.UnsupportedClaims != 1 {
		t.Fatalf("answer audit = %+v, want the refusal recorded", audit.Answer)
	}
	if audit.Drafts != 1 || audit.Rejected != 1 {
		t.Fatalf("draft audit = %+v, want one candidate, one rejection", audit)
	}
}

// The corrected answer is reviewed again. A model that repeats the rejected
// claim cannot get it published by spending the one correction window.
func TestCorrectedAnswerIsReviewedBeforeCommit(t *testing.T) {
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "The project is built with HTMX."}}},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "The project is built with HTMX."}}},
	}
	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:                   []agent.Tool{evidenceReadTool{}},
		MaxTurns:                6,
		CompletionReviewRequest: "Repair the answer: {{reason}}",
		AnswerReview: func(_ []agent.Turn, answer string) string {
			if strings.Contains(answer, "HTMX") {
				return "the answer presents a documentation-only term as an observed fact"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "analyze this project"}})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want the candidate plus one correction", len(turns))
	}
	if !turns[0].Superseded || turns[0].Suppressed {
		t.Fatalf("turn 0 = %+v, want an internal candidate superseded by the correction", turns[0])
	}
	if !turns[1].Suppressed || turns[1].Superseded {
		t.Fatalf("turn 1 = %+v, want the terminal review refusal", turns[1])
	}
	audit := loop.LastResponseAudit()
	if audit.State != agent.ResponseReview {
		t.Fatalf("response state = %s, want REVIEW", audit.State)
	}
	if audit.Rejected != 2 || audit.Drafts != 2 {
		t.Fatalf("draft audit = %+v, want two candidates, both rejected", audit)
	}
}

// A correction that actually clears the review commits exactly once and is
// the only turn a host may publish.
func TestAcceptedCorrectionCommitsOnce(t *testing.T) {
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "Unsupported draft."}}},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "Supported final answer."}}},
	}
	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:                   []agent.Tool{evidenceReadTool{}},
		MaxTurns:                6,
		CompletionReviewRequest: "Repair the answer: {{reason}}",
		AnswerReview: func(_ []agent.Turn, answer string) string {
			if strings.Contains(answer, "Unsupported") {
				return "the draft is not supported"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "analyze this project"}})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(turns) != 2 || !turns[0].Superseded || turns[1].Suppressed || turns[1].Superseded {
		t.Fatalf("turns = %+v, want one internal draft and one accepted answer", turns)
	}
	if got := turns[1].Assistant.Text(); got != "Supported final answer." {
		t.Fatalf("final answer = %q", got)
	}
	audit := loop.LastResponseAudit()
	if audit.State != agent.ResponseCommitted {
		t.Fatalf("response state = %s, want COMMITTED", audit.State)
	}
	if audit.Rejected != 1 {
		t.Fatalf("rejected candidates = %d, want 1", audit.Rejected)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d, want the candidate and one correction", requests.Load())
	}
}

// A repair can introduce a new unsupported claim. The second review must be
// allowed to repair that new claim set; otherwise the first correction is
// spent on an issue the final answer no longer has, and a repairable answer is
// silently withheld. The same claim set still terminates at review.
func TestCorrectionCompletesWhenTheClaimSetChanges(t *testing.T) {
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "Draft with the first unsupported claim."}}},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "Draft with a different unsupported claim."}}},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "Supported final answer."}}},
	}
	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:                   []agent.Tool{evidenceReadTool{}},
		MaxTurns:                8,
		CompletionReviewRequest: "Repair the answer: {{reason}}",
		AnswerReview: func(_ []agent.Turn, answer string) string {
			switch {
			case strings.Contains(answer, "first unsupported"):
				return "the first claim is not supported"
			case strings.Contains(answer, "different unsupported"):
				return "the replacement claim is not supported"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "analyze this project"}})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want two corrections and the accepted answer", len(turns))
	}
	if !turns[0].Superseded || !turns[1].Superseded || turns[2].Superseded || turns[2].Suppressed {
		t.Fatalf("turns = %+v, want two internal drafts and one accepted answer", turns)
	}
	audit := loop.LastResponseAudit()
	if audit.State != agent.ResponseCommitted {
		t.Fatalf("response state = %s, want COMMITTED", audit.State)
	}
	if audit.Rejected != 2 || audit.Drafts != 3 {
		t.Fatalf("draft audit = %+v, want three candidates, two rejected", audit)
	}
	if requests.Load() != 3 {
		t.Fatalf("model requests = %d, want three", requests.Load())
	}
}

// A facet gap is a completion gap, not a false claim. Once correction cannot
// close it, the answer may honestly state the missing deliverable; the
// finalizer may publish it while completion reports NEEDS_REVIEW. Suppressing
// it would leave the user with no answer for a gap the model already named.
func TestFacetOnlyRejectionStillPublishesTheHonestAnswer(t *testing.T) {
	const answer = "The project is a Go service. Tests: not inspected."
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	}
	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:                   []agent.Tool{evidenceReadTool{}},
		MaxTurns:                6,
		CompletionReviewRequest: "Repair the answer: {{reason}}",
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	loop.SetAnswerReview(func([]agent.Turn, string) string {
		return "the answer leaves the requested test inventory unverified"
	})
	loop.SetAnswerRepairs(func([]agent.Turn, string) []contract.GroundingIssue {
		return []contract.GroundingIssue{{
			Code: "required_facet_tests", Category: "facet", Claim: "tests",
		}}
	})
	loop.SetAnswerFinalizer(func(turns []agent.Turn, text, gap string) agent.FinalizedAnswer {
		return agent.FinalizedAnswer{
			Text:  text,
			Audit: agent.AnswerAudit{Finalized: true, UnresolvedFacets: 1},
		}
	})
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "analyze this project"}})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(turns) != 2 || !turns[0].Superseded || turns[1].Suppressed {
		t.Fatalf("turns = %+v, want the internal candidate and one published honest answer", turns)
	}
	if got := turns[1].Assistant.Text(); got != answer {
		t.Fatalf("published answer = %q", got)
	}
	audit := loop.LastResponseAudit()
	if audit.State != agent.ResponseCommitted {
		t.Fatalf("response state = %s, want COMMITTED", audit.State)
	}
	if audit.Answer.UnsupportedClaims != 0 || audit.Answer.UnresolvedFacets != 1 {
		t.Fatalf("answer audit = %+v, want a facet gap only", audit.Answer)
	}
}
