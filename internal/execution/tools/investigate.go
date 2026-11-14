package tools

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// Investigation bounds. They are runtime constants, not model-controlled.
const (
	InvestigateDefaultSteps = 4
	InvestigateMaxSteps     = 8
	InvestigateMaxOutput    = 8000
)

// InvestigateTool runs one adaptive intelligence investigation.
//
// It is the single model-facing entrypoint for the orchestrator: the tool does
// not know which provider answers the question, and it never dumps raw graphs,
// protocol payloads, or full indexes. It returns bounded facts, uncertainty,
// contradictions, and the reason the run stopped.
type InvestigateTool struct {
	Base

	// Root bounds the workspace.
	Root string
	// Engine is the pre-wired orchestrator.
	Engine *investigate.Engine
	// Budget is the per-call bound applied to the request.
	Budget investigate.Budget
}

// NewInvestigateTool returns an investigation tool over engine.
func NewInvestigateTool(root string, engine *investigate.Engine, budget investigate.Budget) *InvestigateTool {
	tool := &InvestigateTool{Root: root, Engine: engine, Budget: budget}
	tool.Base = NewBase("investigate",
		"Investigate a repository question by coordinating structural, lexical "+
			"(BM25), semantic (language server), and authorized runtime evidence. "+
			"Chooses the cheapest sufficient path, correlates observations from "+
			"multiple providers, preserves contradictions, and stops when more "+
			"analysis is not worth its cost. Returns bounded facts with provider, "+
			"confidence, coverage, issues, and the stopping reason. Absence of a "+
			"fact is never proof the fact is false.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"question"},
			"properties": map[string]any{
				"question": map[string]any{
					"type":      "string",
					"minLength": 1,
					"maxLength": 512,
					"description": "The engineering question. Examples: \"where is " +
						"authentication implemented?\", \"what depends on internal/agent?\", " +
						"\"explain the architecture\".",
				},
				"intent": map[string]any{
					"type":        "string",
					"enum":        []string{"auto", "project", "architecture", "locate", "flow", "impact", "debug", "verify"},
					"description": "Optional task shape. Default auto infers it from the question.",
				},
				"focus": map[string]any{
					"type":        "string",
					"description": "Optional repository-relative path or symbol the question is about.",
				},
				"max_steps": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     float64(InvestigateMaxSteps),
					"description": "Maximum providers to execute. Default 4.",
				},
			},
		},
		agent.Approval{Tier: agent.TierRead, Reason: "coordinate read-only repository intelligence"},
	)
	tool.SetActionClasses(agent.ActionObserve, agent.ActionStructure, agent.ActionPlan)
	return tool
}

// Execute implements agent.Tool.
func (t *InvestigateTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	question, err := strArg(args, "question", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"question\" is required; nothing executed.")
	}
	focus, err := strArg(args, "focus", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	focus = strings.TrimSpace(focus)
	if !validRetrievalScope(focus) {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"focus\" must be a repository-relative path or symbol, not an absolute or escaping path; nothing executed.")
	}
	intentName, err := strArg(args, "intent", "auto")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	intent := planner.Intent("")
	if intentName != "" && intentName != "auto" {
		intent = planner.Intent(intentName)
	}
	steps, err := intArg(args, "max_steps", InvestigateDefaultSteps)
	if err != nil || steps < 1 || steps > InvestigateMaxSteps {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"max_steps\" must be between 1 and %d; nothing executed.", InvestigateMaxSteps)
	}
	if t.Engine == nil {
		return agent.ErrorResult("investigation is not configured with any intelligence provider; nothing executed.")
	}
	budget := t.Budget
	budget.MaxSteps = steps
	started := time.Now()
	result, err := t.Engine.Run(ctx.Ctx, investigate.Request{
		Question: question, Intent: intent, Focus: focus, Budget: budget,
	})
	if err != nil {
		if err == investigate.ErrEmptyQuestion {
			return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
		}
		return requestError(agent.ToolFailureRuntime, "investigation did not run: %v", err)
	}
	details := map[string]any{
		"status":         string(result.Status),
		"intent":         string(result.Intent),
		"confidence":     result.Confidence,
		"coverage":       result.Coverage,
		"stop_reason":    result.StopReason,
		"providers":      stepDetails(result.Steps),
		"skips":          result.Skips,
		"facts":          factDetails(result.Facts),
		"contradictions": contradictionDetails(result),
		"issues":         result.Issues,
		"records":        result.Records,
		"lsp_requests":   result.LSPRequests,
		"dap_requests":   result.DAPRequests,
		"duration_ms":    time.Since(started).Milliseconds(),
	}
	text := formatInvestigation(result)
	if len(result.Facts) == 0 {
		return agent.Result{Text: text, Details: details, Useless: true}
	}
	return agent.Result{Text: text, Details: details}
}

func formatInvestigation(result investigate.Result) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "investigation: %s · intent %s · confidence %.2f · coverage %.2f · %s\n",
		result.Status, result.Intent, result.Confidence, result.Coverage, result.StopReason)
	if len(result.Steps) > 0 {
		parts := make([]string, 0, len(result.Steps))
		for _, step := range result.Steps {
			if step.Failed {
				parts = append(parts, fmt.Sprintf("%s(failed)", step.Provider))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s(%s)", step.Provider, step.State))
		}
		builder.WriteString("providers: ")
		builder.WriteString(strings.Join(parts, ", "))
		builder.WriteString("\n")
	}
	for _, fact := range result.Facts {
		fmt.Fprintf(&builder, "- [%s] %s — %s\n", fact.Provider, fact.Subject, fact.Detail)
	}
	if len(result.Contradictions) > 0 {
		builder.WriteString("contradictions (preserved, not resolved silently):\n")
		for _, contradiction := range result.Contradictions {
			fmt.Fprintf(&builder, "- %s %s %s — %s\n", contradiction.Subject, contradiction.Relation,
				strings.Join(contradiction.Objects, " | "), contradiction.Note)
		}
	}
	if len(result.Issues) > 0 {
		builder.WriteString("issues:\n")
		for _, issue := range result.Issues {
			builder.WriteString("- ")
			builder.WriteString(issue)
			builder.WriteString("\n")
		}
	}
	if len(result.Skips) > 0 {
		parts := make([]string, 0, len(result.Skips))
		for _, skip := range result.Skips {
			parts = append(parts, fmt.Sprintf("%s: %s", skip.Provider, skip.Reason))
		}
		builder.WriteString("skipped: ")
		builder.WriteString(strings.Join(parts, "; "))
		builder.WriteString("\n")
	}
	if result.Records > 0 {
		fmt.Fprintf(&builder, "evidence: %d records retained; confidence is correlation, not proof of implementation.\n", result.Records)
	}
	return capInvestigationOutput(builder.String())
}

func capInvestigationOutput(text string) string {
	if len(text) <= InvestigateMaxOutput {
		return text
	}
	cut := InvestigateMaxOutput
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n… investigation output truncated; narrow the question or lower max_steps.\n"
}

func stepDetails(steps []investigate.StepResult) []map[string]any {
	out := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		out = append(out, map[string]any{
			"provider":    string(step.Provider),
			"strategy":    step.Strategy,
			"reason":      step.Reason,
			"state":       string(step.State),
			"confidence":  step.Confidence,
			"coverage":    step.Coverage,
			"summary":     step.Summary,
			"failed":      step.Failed,
			"duration_ms": step.Duration.Milliseconds(),
		})
	}
	return out
}

func factDetails(facts []investigate.Fact) []map[string]any {
	out := make([]map[string]any, 0, len(facts))
	for _, fact := range facts {
		out = append(out, map[string]any{
			"provider": fact.Provider, "subject": fact.Subject, "detail": fact.Detail,
		})
	}
	return out
}

func contradictionDetails(result investigate.Result) []map[string]any {
	out := make([]map[string]any, 0, len(result.Contradictions))
	for _, contradiction := range result.Contradictions {
		providers := make(map[string]int, len(contradiction.Records))
		for _, record := range contradiction.Records {
			providers[string(record.Provider)]++
		}
		out = append(out, map[string]any{
			"subject": contradiction.Subject, "relation": contradiction.Relation,
			"objects": contradiction.Objects, "note": contradiction.Note, "records": providers,
		})
	}
	return out
}
