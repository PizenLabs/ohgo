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

// A review failure is claim-scoped. When the model's next revision drops
// independently supported sentences, the runtime must commit the strongest
// draft with only the refused claim removed, not the poorer rewrite.
func TestAnswerReviewPreservesValidClaimsAcrossRevision(t *testing.T) {
	valid := []string{
		"The repository module is github.com/PizenLabs/ohgo.",
		"The entry point is cmd/ohgo/main.go.",
		"The CLI root is built with cobra.",
		"The runtime keeps a structured graph of nodes and edges.",
		"The graph model includes CALLS relationships.",
		"The graph model includes IMPLEMENTS relationships.",
		"The graph model includes IMPORTS relationships.",
		"The structural intelligence layer parses Go and TypeScript.",
		"The MCP integration exposes repository capabilities to the model.",
		"The watcher tracks repository changes.",
		"The analysis commands include structure and check.",
		"The package layout separates control, execution, and continuity.",
	}
	invalid := "The project uses HTMX for its user interface."
	candidate := strings.Join(append(append([]string(nil), valid...), invalid), " ")
	weakRevision := valid[0] + " " + valid[1] + " " + valid[2]

	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: candidate}}},
		{Content: []ai.ContentBlock{ai.TextContent{Text: weakRevision}}},
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
		AnswerRepairs: func(_ []agent.Turn, answer string) []contract.GroundingIssue {
			if !strings.Contains(answer, "HTMX") {
				return nil
			}
			return []contract.GroundingIssue{{
				Code:        "unsupported_documentation_claim",
				Detail:      "the answer presents documentation-only term htmx as an observed repository fact",
				Claim:       "HTMX",
				Location:    invalid,
				Category:    "attribution",
				Requirement: "a source observation or an explicit documentation attribution",
				Reference:   "HTMX",
				Action:      "attribute the term to the documentation or verify it in the observed source",
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
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want the rich candidate and the committed revision", len(turns))
	}
	if turns[0].Answer != agent.AnswerSuperseded {
		t.Fatalf("first draft = %s, want SUPERSEDED", turns[0].Answer)
	}
	if turns[1].Answer != agent.AnswerCommitted {
		t.Fatalf("second turn = %s, want COMMITTED", turns[1].Answer)
	}
	finalText := turns[1].Assistant.Text()
	for _, claim := range valid {
		if !strings.Contains(finalText, claim) {
			t.Fatalf("committed answer lost a valid claim:\nclaim: %s\nanswer:\n%s", claim, finalText)
		}
	}
	if strings.Contains(finalText, "HTMX") {
		t.Fatalf("committed answer retained the unsupported claim:\n%s", finalText)
	}
	if turns[0].Draft == nil || turns[1].Draft == nil {
		t.Fatal("draft lineage was not attached to the turns")
	}
	if turns[0].Draft.State != agent.AnswerDraftSuperseded {
		t.Fatalf("first draft state = %s, want SUPERSEDED", turns[0].Draft.State)
	}
	if turns[1].Draft.State != agent.AnswerDraftCommitted {
		t.Fatalf("committed draft state = %s, want COMMITTED", turns[1].Draft.State)
	}
	if turns[0].Draft.Version == turns[1].Draft.Version {
		t.Fatalf("revision reused version %d", turns[0].Draft.Version)
	}
	if turns[1].Draft.Supersedes == 0 {
		t.Fatalf("committed draft does not record its lineage: %+v", turns[1].Draft)
	}
	audit := loop.LastResponseAudit()
	if audit.State != agent.ResponseCommitted {
		t.Fatalf("response state = %s, want COMMITTED", audit.State)
	}
	if audit.Drafts != 2 || audit.Rejected != 1 {
		t.Fatalf("draft audit = %+v, want two candidates and one rejection", audit)
	}
	if audit.PreservedClaims != len(valid) {
		t.Fatalf("preserved claims = %d, want %d", audit.PreservedClaims, len(valid))
	}
	if audit.RemovedClaims != 1 {
		t.Fatalf("removed claims = %d, want 1", audit.RemovedClaims)
	}
	if len(audit.Lineage) < 2 {
		t.Fatalf("draft lineage = %+v, want the candidate and its revision", audit.Lineage)
	}
	if audit.Lineage[0].State != agent.AnswerDraftSuperseded {
		t.Fatalf("first lineage state = %s, want SUPERSEDED", audit.Lineage[0].State)
	}
	if audit.Lineage[len(audit.Lineage)-1].State != agent.AnswerDraftCommitted {
		t.Fatalf("last lineage state = %s, want COMMITTED", audit.Lineage[len(audit.Lineage)-1].State)
	}
	if !audit.Lineage[1].PreservationLost {
		t.Fatalf("the weak revision was not recorded as preservation loss: %+v", audit.Lineage[1])
	}
	visible := exportedVisibleTurns(turns)
	if len(visible) != 1 || visible[0] != finalText {
		t.Fatalf("visible answers = %q, want exactly the committed answer", visible)
	}
}

// The correction request carries the failed claim, the valid claims the model
// must preserve, and the permitted repair actions. It never exposes scheduler
// allowance counters: the model reasons about evidence, not the harness.
func TestRepairContextCarriesClaimsAndPreservationNotSchedulerCounters(t *testing.T) {
	const valid = "The entry point is cmd/ohgo/main.go."
	const invalid = "The project uses HTMX for its user interface."
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: valid + " " + invalid}}},
		{Content: []ai.ContentBlock{ai.TextContent{Text: valid}}},
	}
	var requests atomic.Int64
	var repairContext string
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			if index == 1 {
				if tail, ok := req.Messages[len(req.Messages)-1].(ai.RuntimeStateMessage); ok {
					repairContext = tail.Text
				}
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:                   []agent.Tool{evidenceReadTool{}},
		MaxTurns:                6,
		RepairRequest:           "Repair only these claims:\n{{repairs}}\n\n{{preserved}}\n\nAllowed actions: {{actions}}.\nReason: {{reason}}",
		CompletionReviewRequest: "Repair the answer: {{reason}}",
		AnswerReview: func(_ []agent.Turn, answer string) string {
			if strings.Contains(answer, "HTMX") {
				return "the answer presents a documentation-only term as an observed fact"
			}
			return ""
		},
		AnswerRepairs: func(_ []agent.Turn, answer string) []contract.GroundingIssue {
			if !strings.Contains(answer, "HTMX") {
				return nil
			}
			return []contract.GroundingIssue{{
				Code: "unsupported_documentation_claim", Claim: "HTMX", Location: invalid,
				Category: "attribution", Requirement: "source observation or attribution",
				Detail: "the answer presents documentation-only term htmx as an observed repository fact",
				Action: "attribute the term to the documentation or verify it in the source",
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
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d, want the candidate and one correction", requests.Load())
	}
	if len(turns) != 2 || len(turns[0].Results) != 0 || len(turns[1].Results) != 0 {
		t.Fatalf("correction restarted investigation: turns=%+v", turns)
	}
	for _, want := range []string{
		"1. [attribution]",
		"HTMX",
		"Already supported claims that must survive this repair",
		valid,
		"Allowed actions: VERIFY, ATTRIBUTE, REMOVE",
	} {
		if !strings.Contains(repairContext, want) {
			t.Fatalf("repair context missing %q:\n%s", want, repairContext)
		}
	}
	for _, forbidden := range []string{"tool calls remaining", "calls remaining", "CorrectionToolCalls", "correction budget:", "candidate number"} {
		if strings.Contains(strings.ToLower(repairContext), strings.ToLower(forbidden)) {
			t.Fatalf("repair context exposed scheduler state %q:\n%s", forbidden, repairContext)
		}
	}
}

// exportedVisibleTurns is the host-visible answer projection for a run. A
// superseded candidate must not appear; only committed prose does.
func exportedVisibleTurns(turns []agent.Turn) []string {
	var out []string
	for _, turn := range turns {
		projected := agent.TranscriptTurn(turn)
		if turn.Answer == agent.AnswerCommitted {
			if text := projected.Assistant.Text(); strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
			continue
		}
		if text := projected.Assistant.Text(); strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}
