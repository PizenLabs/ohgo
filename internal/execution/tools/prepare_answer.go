package tools

import (
	"github.com/PizenLabs/ohgo/internal/agent"
)

// PrepareAnswerTool records the claims a model intends to make before it
// writes the final answer.
//
// The runtime audits those claims against the evidence already observed. A
// refused plan names the missing reads while the action surface is still open;
// an accepted plan unlocks the answer request. The tool is a control
// observation, never evidence: the plan's own text must not corroborate a
// claim.
type PrepareAnswerTool struct {
	Base
}

// NewPrepareAnswerTool returns the pre-prose answer plan tool.
func NewPrepareAnswerTool() *PrepareAnswerTool {
	tool := &PrepareAnswerTool{}
	tool.Base = NewBase(agent.AnswerPlanToolName,
		"Preflight the final answer before writing it. List every factual claim "+
			"the answer will make as a complete declarative sentence. The runtime "+
			"audits each claim against the evidence it has observed and returns the "+
			"missing reads; an accepted plan unlocks the final answer. If a previous "+
			"plan was refused, support each named claim with the missing observation "+
			"or move it to limitations; do not resubmit it unchanged. List honest "+
			"scope limits separately under limitations: they are not claims. Call "+
			"this only when the evidence phase is complete; do not write the answer "+
			"in the same request.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"claims": map[string]any{
					"type":        "array",
					"description": "Complete declarative sentences the final answer will assert, one claim per entry.",
					"minItems":    1,
					"maxItems":    agent.MaxAnswerPlanClaims,
					"items": map[string]any{
						"type":      "string",
						"minLength": 1,
						"maxLength": 600,
					},
				},
				"limitations": map[string]any{
					"type":        "array",
					"description": "Honest scope limits the answer will state, such as a source the run could not inspect. A limitation is not a claim.",
					"maxItems":    agent.MaxAnswerPlanLimitations,
					"items": map[string]any{
						"type":      "string",
						"minLength": 1,
						"maxLength": 600,
					},
				},
			},
			"required": []string{"claims"},
		},
		agent.Approval{Tier: agent.TierRead, Reason: "preflight the final answer's claims against observed evidence"},
	)
	tool.SetActionClasses(agent.ActionPlan)
	tool.SetExclusive(true)
	return tool
}

// Execute implements agent.Tool.
func (t *PrepareAnswerTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	plan, err := agent.ParseAnswerPlan(args)
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing recorded.", err)
	}
	result := agent.AnswerPlanResult{Claims: len(plan.Claims)}
	if ctx.AnswerPlanReview == nil {
		// A host without the preflight seam still gets a truthful record; the
		// plan must not claim it was audited.
		return agent.Result{
			Text:    "answer plan recorded; no preflight audit is installed. Write the final answer from the evidence already observed.",
			Details: result,
		}
	}
	result.Reviewed = true
	issues := ctx.AnswerPlanReview(plan)
	if len(issues) > 0 {
		result.Issues = issues
		return agent.Result{
			Text:         agent.AnswerPlanRefusal(issues),
			IsError:      true,
			FailureClass: agent.ToolFailureAnswerPlan,
			Details:      result,
		}
	}
	result.Accepted = true
	return agent.Result{
		Text:    "answer plan accepted: every declared claim is supported by the observed evidence. Write the complete final answer now; do not call another tool.",
		Details: result,
	}
}

var _ agent.Tool = (*PrepareAnswerTool)(nil)
