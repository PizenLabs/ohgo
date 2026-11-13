package tui

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The original investigation line names a conceptual order/checkout flow. It
// must not become a literal target_state requirement: a real implementation
// read is enough evidence for the investigation phase. This exercises the
// production phase + evidence-review boundary, not only the shape struct.
func TestPhaseCompletionDoesNotDemandAConceptualSlashPath(t *testing.T) {
	prompt := ecommerceInvestigationPrompt
	intent := workspace.BuildIntentContract(prompt)
	if intent.Kind != workspace.ObjectiveMutation || !intent.ConditionalMutation {
		t.Fatalf("contract = %s conditional=%v, want the staged mutation", intent.Kind, intent.ConditionalMutation)
	}

	facts := cognition.RepositoryFacts{Go: true}
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "fuzz", Facts: facts})
	evidence := cognition.NewEvidenceState()
	projector := cognition.NewProjector(shape, evidence, facts)

	observed := []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "read-1", ToolName: "read",
		Args: map[string]any{"path": "tools/grep.go"},
		Result: agent.Result{
			Text: "1\tpackage tools",
			Details: map[string]any{
				"path": "tools/grep.go", "totalLines": 1, "offset": 1, "limit": 2000,
			},
		},
	}}}}
	projected := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: prompt}},
		Turns:    observed,
	})
	if review := projected.Report.EvidenceReview(); review != "" {
		t.Fatalf("realistic implementation read was still insufficient: %s", review)
	}

	answer := agent.Turn{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{
			Text: "The order/checkout flow is represented by tools/grep.go in this snapshot.",
		}},
		StopReason: ai.StopEndTurn,
	}}
	turns := append(append([]agent.Turn(nil), observed...), answer)
	action, reason := turnTaskOutcomeForAuthority(nil, turns, intent, nil, nil,
		projected.Report.EvidenceReview(), false, agent.EvidenceProvenance{}, agent.MutationProvenance{}, "fuzz")
	if action != taskActionPhaseComplete {
		t.Fatalf("phase outcome = %v (%s), want phase-complete", action, reason)
	}

	// The same evidence must not make the literal target acceptable when the
	// user actually named a path. This keeps the concept fix from weakening
	// strict coordinates.
	literal := workspace.BuildIntentContract("Read order/checkout.go")
	literalShape := cognition.BuildShape(literal, cognition.ShapeParams{Authority: "ask", Facts: facts})
	literalEvidence := cognition.NewEvidenceState()
	literalProjector := cognition.NewProjector(literalShape, literalEvidence, facts)
	literalProjected := literalProjector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: literal.Objective}},
		Turns:    observed,
	})
	if literalProjected.Report.EvidenceReview() == "" {
		t.Fatal("a literal order/checkout.go request accepted a tools/grep.go read")
	}
}
