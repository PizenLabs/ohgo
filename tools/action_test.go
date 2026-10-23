package tools

import (
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/contract"
)

func hasClass(tool agent.Tool, want agent.ActionClass) bool {
	return agent.HasActionClass(tool, want)
}

func hasOperation(tool agent.Tool, want contract.ChangeOp) bool {
	for _, operation := range agent.ToolMutationOperations(tool) {
		if operation == want {
			return true
		}
	}
	return false
}

// The task-shaped candidate policy reads this metadata. A tool that changes
// its declared surface must change it here, so a policy cannot silently hide a
// capability or offer a mutation the tool cannot perform.
func TestBuiltinToolActionMetadata(t *testing.T) {
	source := NewStructuralSource("")
	cases := []struct {
		name      string
		tool      agent.Tool
		classes   []agent.ActionClass
		operation contract.ChangeOp
		language  string
	}{
		{name: "write", tool: NewWriteTool(""), classes: []agent.ActionClass{agent.ActionMutate}, operation: contract.ChangeCreate},
		{name: "edit", tool: NewEditTool(""), classes: []agent.ActionClass{agent.ActionMutate, agent.ActionRequiresExisting}, operation: contract.ChangeModify},
		{name: "delete", tool: NewDeleteTool(""), classes: []agent.ActionClass{agent.ActionMutate, agent.ActionRequiresExisting}, operation: contract.ChangeDelete},
		{name: "bash", tool: NewBashTool(""), classes: []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}, operation: contract.ChangeAny},
		{name: "web_fetch", tool: NewWebFetchTool(), classes: []agent.ActionClass{agent.ActionExternal}},
		{name: "symbols", tool: NewSymbolsToolWithSource(source), classes: []agent.ActionClass{agent.ActionObserve}, language: "go"},
		{name: "graph", tool: NewGraphTool(source), classes: []agent.ActionClass{agent.ActionObserve, agent.ActionRelationship}, language: "go"},
		{name: "refs", tool: NewRefsTool(source), classes: []agent.ActionClass{agent.ActionObserve, agent.ActionRelationship}, language: "go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, class := range tc.classes {
				if !hasClass(tc.tool, class) {
					t.Errorf("%s is missing action class %q", tc.name, class)
				}
			}
			if tc.operation != "" && !hasOperation(tc.tool, tc.operation) {
				t.Errorf("%s is missing mutation operation %q", tc.name, tc.operation)
			}
			if tc.language != "" && agent.ToolRequiredLanguage(tc.tool) != tc.language {
				t.Errorf("%s language = %q, want %q", tc.name, agent.ToolRequiredLanguage(tc.tool), tc.language)
			}
		})
	}
}
