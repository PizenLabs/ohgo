package cognition

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/contract"
)

// CandidateSet is the tool surface offered for one model request.
//
// It is a narrowing, never a grant. Every tool that remains still passes the
// loop's security boundary, and every removal is explained so the decision can
// be audited. The loop enforces the set at dispatch, so a model cannot call a
// withheld tool by naming it.
type CandidateSet struct {
	// Tools is the offered surface, in the loop's original order.
	Tools []agent.Tool
	// Excluded names every withheld tool and why.
	Excluded []agent.CandidateExclusion
	// Notices are bounded summaries for the audit and the runtime state block.
	Notices []string
}

// Candidates narrows a tool surface to what the task shape can use.
//
// The policy is deliberately conservative. It removes a tool only when its
// declared class, language, operation, or the active authority makes it
// provably irrelevant:
//
//   - a mutating tool when the shape does not permit mutation;
//   - an external-knowledge tool when the task does not require external
//     knowledge;
//   - an execution tool when the task needs no process;
//   - a structural Go tool when the repository has no Go sources or the task
//     needs no code navigation;
//   - a create-only task's edit/delete tools;
//   - a background-wait tool before any background job exists;
//   - every tool once the evidence lifecycle is complete.
//
// It never removes a tool because a model "probably would not need it", and it
// never adds one. A tool that declares no metadata falls back to its approval
// tier, so an unrecognised tool remains available.
func Candidates(shape TaskShape, evidence *EvidenceState, execution ExecutionState, tools []agent.Tool, facts RepositoryFacts, suff Sufficiency) CandidateSet {
	var set CandidateSet
	set.Tools = make([]agent.Tool, 0, len(tools))
	counts := map[string]int{}
	addNotice := func(reason string) {
		counts[reason]++
	}
	exclude := func(tool agent.Tool, reason string) {
		set.Excluded = append(set.Excluded, agent.CandidateExclusion{Tool: tool.Name(), Reason: reason})
		addNotice(reason)
	}

	for _, tool := range tools {
		if reason, excluded := candidateExclusion(shape, execution, tool, facts, suff); excluded {
			exclude(tool, reason)
			continue
		}
		set.Tools = append(set.Tools, tool)
	}

	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		set.Notices = append(set.Notices, fmt.Sprintf("%s (%d tool(s))", reason, counts[reason]))
	}
	return set
}

// candidateExclusion returns the reason a tool is not offered, if any.
func candidateExclusion(shape TaskShape, execution ExecutionState, tool agent.Tool, facts RepositoryFacts, suff Sufficiency) (string, bool) {
	// A completed evidence lifecycle has nothing left for a tool to do. This is
	// the deterministic stop for "exploration continued because the model could
	// think of one more file": the proof is complete, so the next turn is for
	// the model's report, not another call.
	if suff.Stage == StageComplete && shape.Kind != contract.ObjectiveInformational {
		return "the objective's evidence is complete; answer without further tools", true
	}
	if suff.Stage == StageBlocked && agent.HasActionClass(tool, agent.ActionMutate) &&
		!agent.HasActionClass(tool, agent.ActionExecute) {
		// Do not allow more mutation after a scope violation, but keep the
		// observation and check surface so the model can verify what happened
		// and report it truthfully.
		return "the evidence boundary is blocked; do not mutate further", true
	}
	if shape.Budget.Exhausted() {
		return "the root grant is exhausted", true
	}

	if language := agent.ToolRequiredLanguage(tool); language != "" {
		if !facts.HasLanguage(language) {
			return "the repository has no " + language + " sources", true
		}
		if !shape.NeedsCodeNavigation {
			return "the task does not need " + language + " structural navigation", true
		}
	}
	if agent.HasActionClass(tool, agent.ActionExternal) &&
		!agent.HasActionClass(tool, agent.ActionConfigured) &&
		!shape.External.Required {
		// Configured capabilities (MCP servers) are exempt: a human already
		// decided they belong in this agent's action space. The security gate
		// still decides whether any particular call is authorized.
		return "the task does not require external knowledge", true
	}
	if agent.HasActionClass(tool, agent.ActionRelationship) && !shape.NeedsRelationshipNavigation {
		return "the task does not ask a relationship question", true
	}
	if agent.HasActionClass(tool, agent.ActionMutate) {
		// A dual-use tool (a shell) must remain available for an execution
		// task even when the repository shape forbids mutation. The security
		// boundary enforces read-only execution; candidate selection must not
		// make an execution objective impossible by hiding its only capability.
		executionCapable := agent.HasActionClass(tool, agent.ActionExecute) &&
			shape.NeedsExecution && shape.Authority.ExecutionAllowed
		if !shape.Mutation.Allowed && !executionCapable {
			return "the task shape does not permit repository mutation", true
		}
		operations := agent.ToolMutationOperations(tool)
		if shape.Mutation.Explicit && !operationsIntersect(shape.Mutation.Operations, operations) {
			return "the requested change operations do not include this tool's operation", true
		}
		if onlyOperation(operations, contract.ChangeDelete) && !shape.Mutation.DeleteRequested {
			return "the request did not ask to delete a path", true
		}
		if (modifyOnly(operations) || agent.HasActionClass(tool, agent.ActionRequiresExisting)) &&
			allExplicitPathsAbsent(shape.Mutation) && !allExplicitPathsMutated(shape.Mutation, execution) {
			return "the requested target is absent; this tool requires an existing file", true
		}
	}
	if agent.HasActionClass(tool, agent.ActionExecute) && !shape.NeedsExecution {
		return "the task does not require execution", true
	}
	if agent.HasActionClass(tool, agent.ActionBackground) && !execution.BackgroundStarted {
		return "no background job has been started", true
	}
	// Delegation is never hidden by task kind. A subagent can be the cheapest
	// way to explore a large repository, and the Phase 5 depth/budget bounds
	// already make it safe. Hiding it would be a workflow choice the shape
	// cannot justify.
	return "", false
}

// onlyOperation reports whether a tool declares exactly one operation.
func onlyOperation(operations []contract.ChangeOp, want contract.ChangeOp) bool {
	return len(operations) == 1 && operations[0] == want
}

// modifyOnly reports whether a tool can only change an existing file.
func modifyOnly(operations []contract.ChangeOp) bool {
	if len(operations) == 0 {
		return false
	}
	for _, operation := range operations {
		switch operation {
		case contract.ChangeCreate, contract.ChangeDelete, contract.ChangeAny:
			return false
		}
	}
	for _, operation := range operations {
		if operation == contract.ChangeModify {
			return true
		}
	}
	return false
}

// allExplicitPathsAbsent reports whether every named path target was observed
// absent before the run. Unknown existence is not absence.
func allExplicitPathsAbsent(scope MutationScope) bool {
	if len(scope.Paths) == 0 || len(scope.AbsentPaths) != len(scope.Paths) {
		return false
	}
	for _, path := range scope.Paths {
		found := false
		for _, absent := range scope.AbsentPaths {
			if absent == path {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// allExplicitPathsMutated reports whether the run has already created or
// changed every named path, which makes an existing-file tool relevant even
// when the pre-run state said the path was absent.
func allExplicitPathsMutated(scope MutationScope, execution ExecutionState) bool {
	if len(scope.Paths) == 0 {
		return false
	}
	for _, path := range scope.Paths {
		if !containsString(execution.MutatedPaths, path) {
			return false
		}
	}
	return true
}

// operationsIntersect reports whether a requested operation set can be served
// by a tool. An empty tool set means the tool did not declare its surface, so
// it is not excluded on an operation mismatch.
func operationsIntersect(requested, provided []contract.ChangeOp) bool {
	if len(provided) == 0 {
		return true
	}
	for _, want := range requested {
		for _, have := range provided {
			if want == contract.ChangeAny || have == contract.ChangeAny || want == have {
				return true
			}
		}
	}
	return false
}

// CandidateSummary renders the offered set for a state block.
func (s CandidateSet) CandidateSummary(limit int) string {
	names := make([]string, 0, len(s.Tools))
	for _, tool := range s.Tools {
		names = append(names, tool.Name())
	}
	text := strings.Join(names, ", ")
	if limit > 0 && len(text) > limit {
		text = text[:limit] + "…"
	}
	if text == "" {
		return "none"
	}
	return text
}
