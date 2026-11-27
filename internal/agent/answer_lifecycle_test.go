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
	if turns[0].Answer != agent.AnswerRejected {
		t.Fatal("the refused candidate was not classified as a terminal rejection")
	}
	if strings.TrimSpace(turns[0].AnswerReview) == "" {
		t.Fatal("the terminal rejection carries no control-plane reason")
	}
	if !strings.Contains(turns[0].AnswerReview, "htmx") {
		t.Fatalf("the rejection reason does not name the refused claim: %q", turns[0].AnswerReview)
	}
	if got := agent.TranscriptTurn(turns[0]).Assistant.Text(); got != "" {
		t.Fatalf("the refused candidate remained publishable: %q", got)
	}
	if _, ok := turns[0].RejectedAnswer(); !ok {
		t.Fatal("the terminal rejection has no accountable artifact")
	}
	if len(reported) != 1 || reported[0].Answer != agent.AnswerRejected {
		t.Fatalf("reported turns = %+v, want one terminal rejection", reported)
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
	// The completion authority must agree with the transcript: a refused
	// candidate is not an answer, even though the turn retains its prose for
	// the review. Before this rule the same run was reported COMPLETED.
	verdict, reason, _ := agent.ClassifyRun(nil, agent.CompletionInput{
		Turns:    turns,
		Contract: contract.IntentContract{Objective: "analyze this project", Kind: contract.ObjectiveInformational},
	}, false)
	if verdict == agent.RunCompleted || verdict == agent.RunCompletedLimited {
		t.Fatalf("a refused answer was reported complete: %s (%s)", verdict, reason)
	}
	if !strings.Contains(reason, "not published") {
		t.Fatalf("completion reason = %q, want the publication boundary", reason)
	}
}

// A correction can spend its tool allowance and still need the answer. The
// earlier candidate was withheld, so the final request must say so; otherwise
// the model treats its own draft as delivered and returns a confirmation.
func TestSpentCorrectionRequiresTheCompleteAnswer(t *testing.T) {
	const rich = "The repository has a Go backend. The repository has a React frontend. The project uses UNSUPPORTED."
	const terse = "The investigation is complete. The analysis above summarizes the project."
	const full = "The repository has a Go backend. The repository has a React frontend."
	intent := contract.IntentContract{
		Objective: "analyze this project",
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: rich}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.ToolCall{
			ID: "call-1", Name: "read", Arguments: map[string]any{"path": "README.md"},
		}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: terse}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.TextContent{Text: full}}, StopReason: ai.StopEndTurn},
	}
	var requests []ai.Request
	loop, err := agent.New(agent.Config{
		Model:                   ai.Model{ID: "m", Provider: "p"},
		MaxTurns:                8,
		Tools:                   []agent.Tool{evidenceReadTool{}},
		CorrectionToolCalls:     1,
		CorrectionAnswerRequest: "The earlier candidate was withheld; write the complete final answer, not a confirmation.",
		CompletionReviewRequest: "Repair the answer: {{reason}}",
		RepairRequest:           "Repair these claims:\n{{repairs}}\n\n{{preserved}}\n\nReason: {{reason}}",
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			requests = append(requests, req)
			if len(requests) > len(script) {
				t.Fatalf("provider request %d exceeded the script", len(requests))
			}
			return ai.NewCompletedStream(script[len(requests)-1]), nil
		},
		AnswerReview: func(turns []agent.Turn, answer string) string {
			if strings.Contains(answer, "UNSUPPORTED") {
				return "the answer presents an unsupported term as an observed fact"
			}
			return agent.ReviewInformationalAnswer(intent, turns, answer)
		},
		AnswerRepairs: func(_ []agent.Turn, answer string) []contract.GroundingIssue {
			if !strings.Contains(answer, "UNSUPPORTED") && !strings.Contains(answer, "analysis above") {
				return nil
			}
			return []contract.GroundingIssue{{
				Code: "unsupported_claim", Claim: "unlocated finding",
				Location: "a sentence the draft does not contain",
				Category: "attribution", Requirement: "a source observation or an attribution",
				Detail: "the answer presents an unsupported term as an observed fact",
				Action: "verify the term or remove the claim",
			}}
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "analyze this project"}})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(turns) != 4 {
		t.Fatalf("turns = %d, want the draft, the repair, the confirmation, and the complete answer", len(turns))
	}
	if turns[0].Answer != agent.AnswerSuperseded {
		t.Fatalf("first draft = %s, want SUPERSEDED", turns[0].Answer)
	}
	if turns[2].Answer != agent.AnswerSuperseded {
		t.Fatalf("completion confirmation = %s, want SUPERSEDED", turns[2].Answer)
	}
	if turns[3].Answer != agent.AnswerCommitted {
		t.Fatalf("final answer = %s, want COMMITTED", turns[3].Answer)
	}
	if got := turns[3].Assistant.Text(); got != full {
		t.Fatalf("committed answer = %q, want the complete answer", got)
	}
	if len(requests) != 4 {
		t.Fatalf("provider requests = %d, want 4", len(requests))
	}
	state := ""
	for index := len(requests[3].Messages) - 1; index >= 0; index-- {
		if runtime, ok := requests[3].Messages[index].(ai.RuntimeStateMessage); ok {
			state = runtime.Text
			break
		}
	}
	if !strings.Contains(state, "The earlier candidate was withheld") {
		t.Fatalf("the spent correction did not tell the model its draft was withheld:\n%s", state)
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
	if turns[0].Answer != agent.AnswerSuperseded {
		t.Fatalf("turn 0 = %+v, want an internal candidate superseded by the correction", turns[0])
	}
	if turns[1].Answer != agent.AnswerRejected {
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
	if len(turns) != 2 || turns[0].Answer != agent.AnswerSuperseded || turns[1].Answer != agent.AnswerCommitted {
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
	if turns[0].Answer != agent.AnswerSuperseded || turns[1].Answer != agent.AnswerSuperseded || turns[2].Answer != agent.AnswerCommitted {
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
	if len(turns) != 2 || turns[0].Answer != agent.AnswerSuperseded || turns[1].Answer != agent.AnswerCommitted {
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
