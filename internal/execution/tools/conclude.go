package tools

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// ConclusionVerdict is the terminal judgement of a bounded investigation.
type ConclusionVerdict string

// Investigation verdicts.
const (
	ConclusionSafe       ConclusionVerdict = "SAFE"
	ConclusionBug        ConclusionVerdict = "BUG"
	ConclusionUnresolved ConclusionVerdict = "UNRESOLVED"
)

// ConcludeTool records the investigation conclusion after the required
// evidence has been established.
//
// It is a control observation, not proof: the evidence field states what the
// runtime observed, the uncertainty field preserves what remains open, and
// completion still belongs to the evidence evaluator. The tool exists so a
// bounded investigation has an explicit terminal action instead of leaving the
// model to decide whether "enough" means another repository survey.
type ConcludeTool struct {
	Base
}

// NewConcludeTool returns the investigation conclusion recorder.
func NewConcludeTool() *ConcludeTool {
	tool := &ConcludeTool{}
	tool.Base = NewBase("conclude",
		"Record a bounded investigation outcome. Use status FINAL only after the "+
			"production entrypoint, the reference/validation flow, persistence "+
			"ordering, and bypass paths are sufficiently established; use "+
			"IN_PROGRESS for a progress observation while evidence is still being "+
			"gathered. A progress observation never satisfies the conclusion and "+
			"never starts the answer boundary. State the verdict, the evidence that "+
			"supports it, and any remaining uncertainty. This records an observation; "+
			"it does not prove the claim and it does not authorize a change.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"verdict", "status", "evidence"},
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"FINAL", "IN_PROGRESS"},
					"description": "FINAL records a terminal verdict. IN_PROGRESS records a progress observation only and never satisfies the investigation conclusion or starts the answer boundary.",
				},
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []string{string(ConclusionSafe), string(ConclusionBug), string(ConclusionUnresolved)},
					"description": "SAFE: the production flow prevents the failure. BUG: a production-reachable path creates it. UNRESOLVED: available evidence cannot establish either.",
				},
				"evidence": map[string]any{
					"type":        "string",
					"minLength":   1,
					"maxLength":   4000,
					"description": "The observed entrypoint, identifier flow, validation boundary, persistence ordering, and bypass-path findings that support the verdict. Name files and commands; do not restate the hypothesis as proof.",
				},
				"uncertainty": map[string]any{
					"type":        "string",
					"maxLength":   2000,
					"description": "Material evidence that remains unresolved. Name concrete code coordinates (symbol, selector, or Go path) when one exists; the runtime keeps the investigation open until that coordinate is observed or the bounded attempt limit classifies UNRESOLVED. Use an empty string only when the verdict is supported without a material gap.",
				},
			},
		},
		agent.Approval{Tier: agent.TierRead, Reason: "record an investigation conclusion"},
	)
	tool.SetActionClasses(agent.ActionPlan)
	return tool
}

// Execute implements agent.Tool.
func (t *ConcludeTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	verdictArg, err := strArg(args, "verdict", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing recorded.", err)
	}
	verdict := ConclusionVerdict(strings.ToUpper(strings.TrimSpace(verdictArg)))
	switch verdict {
	case ConclusionSafe, ConclusionBug, ConclusionUnresolved:
	default:
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"verdict\" must be SAFE, BUG, or UNRESOLVED; nothing recorded.")
	}
	evidence, err := strArg(args, "evidence", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing recorded.", err)
	}
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"evidence\" is required; nothing recorded.")
	}
	uncertainty, err := strArg(args, "uncertainty", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing recorded.", err)
	}
	uncertainty = strings.TrimSpace(uncertainty)
	statusArg, err := strArg(args, "status", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing recorded.", err)
	}
	status := strings.ToUpper(strings.TrimSpace(statusArg))
	if status == "" {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"status\" is required: use FINAL for a terminal verdict or IN_PROGRESS while evidence is still being gathered; nothing recorded.")
	}
	if status != "FINAL" && status != "IN_PROGRESS" {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"status\" must be FINAL or IN_PROGRESS; nothing recorded.")
	}
	// A model sometimes puts its progress marker in uncertainty while still
	// selecting FINAL. The marker is an explicit statement that the verdict is
	// not terminal; honor it instead of closing the investigation.
	if status == "FINAL" && contract.IsProvisionalConclusion(evidence, uncertainty) {
		status = "IN_PROGRESS"
	}
	text := fmt.Sprintf("conclusion: %s\n%s", verdict, evidence)
	if status == "IN_PROGRESS" {
		text = "progress: investigation remains open; this observation alone does not close the action surface or satisfy the conclusion"
	}
	return agent.Result{
		Text: text,
		Details: map[string]any{
			"verdict":     string(verdict),
			"status":      status,
			"evidence":    evidence,
			"uncertainty": uncertainty,
		},
	}
}
