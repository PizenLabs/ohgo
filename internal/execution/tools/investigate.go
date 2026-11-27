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
			"Call this before a chain of grep/symbol/read calls when the question is "+
			"relational: call chains, dependencies, architecture, impact, test impact, "+
			"or interface implementations. Use symbols, grep, and read for one exact "+
			"coordinate or source excerpt. "+
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
					"enum":        []string{"auto", "project", "architecture", "locate", "flow", "impact", "debug", "verify", "drift"},
					"description": "Optional task shape. Default auto infers it from the question.",
				},
				"focus": map[string]any{
					"type": "string",
					"description": "Optional repository-relative path or symbol the question is about. " +
						"For flow, callers/callees, and impact, prefer the exact symbol name " +
						"returned by symbols; a file path is better for package or file-level context.",
				},
				"provider": map[string]any{
					"type":        "string",
					"enum":        []string{"auto", "external"},
					"description": "Optional structural evidence preference. auto (default) chooses the cheapest sufficient path. external answers structural steps with the external structural provider (GRY) when it is available, and reports an issue when it cannot be used.",
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
	tool.SetGroup(ToolGroupCodeIntelligence)
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
	providerName, err := strArg(args, "provider", "auto")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	if providerName != "auto" && providerName != "external" {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"provider\" must be \"auto\" or \"external\"; nothing executed.")
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
		Question: question, Intent: intent, Focus: focus,
		PreferExternal: providerName == "external", Budget: budget,
	})
	if err != nil {
		if err == investigate.ErrEmptyQuestion {
			return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
		}
		return requestError(agent.ToolFailureRuntime, "investigation did not run: %v", err)
	}
	details := map[string]any{
		"status":            string(result.Status),
		"status_scope":      "provider-local; control-plane evidence facets gate task completion",
		"intent":            string(result.Intent),
		"confidence":        result.Confidence,
		"coverage":          result.Coverage,
		"stop_reason":       result.StopReason,
		"providers":         stepDetails(result.Steps),
		"skips":             result.Skips,
		"facts":             factDetails(result.Facts),
		"contradictions":    contradictionDetails(result),
		"issues":            result.Issues,
		"records":           result.Records,
		"lsp_requests":      result.LSPRequests,
		"dap_requests":      result.DAPRequests,
		"external_requests": result.ExternalRequests,
		"duration_ms":       time.Since(started).Milliseconds(),
	}
	text := formatInvestigation(result)
	if len(result.Facts) == 0 {
		return agent.Result{Text: text, Details: details, Useless: true}
	}
	return agent.Result{Text: text, Details: details}
}

func formatInvestigation(result investigate.Result) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "investigation: %s", investigationStatus(result.Status))
	if sources := stepSources(result.Steps); sources != "" {
		fmt.Fprintf(&builder, " · %s", sources)
	}
	fmt.Fprintf(&builder, " · intent %s · confidence %.2f · coverage %.2f · %s\n",
		result.Intent, result.Confidence, result.Coverage, result.StopReason)
	if result.Status == investigate.StatusSufficient {
		builder.WriteString("scope: provider-local; the control plane's evidence facets still gate task completion\n")
	}
	if len(result.Steps) > 0 {
		parts := make([]string, 0, len(result.Steps))
		for _, step := range result.Steps {
			if step.Failed {
				parts = append(parts, fmt.Sprintf("%s(failed)", stepLabel(step)))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s(%s)", stepLabel(step), step.State))
		}
		builder.WriteString("providers: ")
		builder.WriteString(strings.Join(parts, ", "))
		builder.WriteString("\n")
	}
	for _, fact := range result.Facts {
		provider := fact.Provider
		if fact.Source != "" {
			provider += "/" + fact.Source
		}
		if fact.Freshness != "" {
			// Freshness belongs on the fact, not only in the step summary: an
			// UNVERIFIED edge must not read as a current relationship merely
			// because the nodes around it are current.
			provider += " " + string(fact.Freshness)
		}
		fmt.Fprintf(&builder, "- [%s] %s — %s\n", provider, fact.Subject, fact.Detail)
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

// investigationStatus distinguishes a provider-local confidence target from a
// task-level evidence verdict. The control plane owns the latter.
func investigationStatus(status investigate.Status) string {
	if status == investigate.StatusSufficient {
		return "provider_sufficient"
	}
	return string(status)
}

// stepSources renders the mechanisms that answered the steps, deduplicated
// and bounded. It is the compact attribution carried on the first line; the
// per-fact lines carry the same source at their own resolution.
func stepSources(steps []investigate.StepResult) string {
	seen := map[string]bool{}
	var out []string
	for _, step := range steps {
		name := string(step.Provider)
		if step.Source != "" {
			name += "/" + step.Source
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

func stepLabel(step investigate.StepResult) string {
	if step.Source != "" {
		return string(step.Provider) + "/" + step.Source
	}
	return string(step.Provider)
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
			"outcome":     string(step.Outcome),
			"source":      step.Source,
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
			"provider": fact.Provider, "source": fact.Source,
			"subject": fact.Subject, "detail": fact.Detail,
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
