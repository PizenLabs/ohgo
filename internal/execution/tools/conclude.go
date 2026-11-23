package tools

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// ConcludeTool records the terminal outcome of a bounded investigation after
// the required evidence has been established.
//
// It is a control observation, not proof: the evidence field states what the
// runtime observed, the uncertainty field preserves what remains open, and
// completion still belongs to the evidence evaluator. The tool exists so a
// bounded investigation has an explicit terminal action instead of leaving the
// model to decide whether "enough" means another repository survey.
//
// The verdict vocabulary is objective-dependent. A general project analysis
// records ANSWERED, PARTIAL, or UNRESOLVED; only a failure investigation may
// record SAFE or BUG. Context.ConclusionMode is the authority for that split,
// so the model cannot turn "what is this project?" into a safety verdict.
type ConcludeTool struct {
	Base
}

// NewConcludeTool returns the investigation conclusion recorder.
func NewConcludeTool() *ConcludeTool {
	tool := &ConcludeTool{}
	tool.Base = NewBase("conclude",
		"Record a bounded investigation outcome. Use status FINAL only after the "+
			"evidence the runtime asked for is established; use IN_PROGRESS for a "+
			"progress observation while evidence is still being gathered. A progress "+
			"observation never satisfies the conclusion and never starts the answer "+
			"boundary. A general answer uses ANSWERED, PARTIAL, or UNRESOLVED; SAFE "+
			"and BUG are reserved for a request that asks whether a failure is "+
			"production-reachable. State the verdict, the evidence that supports it, "+
			"and any remaining uncertainty. This records an observation; it does not "+
			"prove the claim and it does not authorize a change.",
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
					"type": "string",
					"enum": []string{
						string(contract.VerdictAnswered), string(contract.VerdictPartial),
						string(contract.VerdictUnresolved), string(contract.VerdictSafe), string(contract.VerdictBug),
					},
					"description": "ANSWERED: the request was answered by observed evidence. PARTIAL: a usable answer exists and names what remains unverified. SAFE: the production flow prevents the failure. BUG: a production-reachable path creates it. UNRESOLVED: available evidence cannot establish the answer or the defect verdict.",
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
	verdict := contract.ConclusionVerdict(strings.ToUpper(strings.TrimSpace(verdictArg)))
	mode := ctx.ConclusionMode
	if !verdict.Known() {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"verdict\" must be %s; nothing recorded.", mode.VerdictList())
	}
	if !mode.Accepts(verdict) {
		// A general answer is not a safety verdict. The request's vocabulary is
		// installed by the control plane, not chosen by the model, so the
		// correction cannot be argued away in prose.
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"verdict\" must be %s for this request; %s is only valid for a defect investigation; nothing recorded.",
			mode.VerdictList(), verdict)
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
	// ANSWERED is a verdict about the evidence, not the user's answer. The
	// result must not be labelled "answer": a model that reads its own
	// conclusion as the deliverable then produces a one-line acknowledgement
	// instead of the synthesis the request asked for.
	text := fmt.Sprintf("conclusion: %s\nstatus: %s\n%s", verdict, status, evidence)
	if status == "IN_PROGRESS" {
		text = "progress: investigation remains open; this observation alone does not close the action surface or satisfy the conclusion"
	} else {
		text += "\n\nThe terminal observation is recorded. Answer synthesis is a separate step: " +
			"produce the final answer from the evidence unless the runtime reports an unresolved investigation target."
	}
	return agent.Result{
		Text: text,
		Details: map[string]any{
			"verdict":     string(verdict),
			"status":      status,
			"mode":        string(mode),
			"evidence":    evidence,
			"uncertainty": uncertainty,
		},
	}
}
