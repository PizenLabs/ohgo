package tools

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
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
		name          string
		tool          agent.Tool
		classes       []agent.ActionClass
		operation     contract.ChangeOp
		language      string
		languages     []string
		allStructural bool
	}{
		{name: "write", tool: NewWriteTool(""), classes: []agent.ActionClass{agent.ActionMutate}, operation: contract.ChangeCreate},
		{name: "edit", tool: NewEditTool(""), classes: []agent.ActionClass{agent.ActionMutate, agent.ActionRequiresExisting}, operation: contract.ChangeModify},
		{name: "delete", tool: NewDeleteTool(""), classes: []agent.ActionClass{agent.ActionMutate, agent.ActionRequiresExisting}, operation: contract.ChangeDelete},
		{name: "bash", tool: NewBashTool(""), classes: []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}, operation: contract.ChangeAny},
		{name: "web_fetch", tool: NewWebFetchTool(), classes: []agent.ActionClass{agent.ActionExternal}},
		{name: "symbols", tool: NewSymbolsToolWithSource(source), classes: []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}, allStructural: true},
		{name: "graph", tool: NewGraphTool(source), classes: []agent.ActionClass{agent.ActionObserve, agent.ActionRelationship, agent.ActionStructure}, language: "go"},
		{name: "refs", tool: NewRefsTool(source), classes: []agent.ActionClass{agent.ActionObserve, agent.ActionRelationship, agent.ActionStructure}, allStructural: true},
		{name: "structure", tool: NewStructureTool(source), classes: []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}},
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
			if tc.allStructural {
				got := agent.ToolRequiredLanguages(tc.tool)
				want := structuralLanguageNames()
				if len(got) != len(want) {
					t.Errorf("%s languages = %v, want %v", tc.name, got, want)
				}
				for i := range want {
					if i >= len(got) || got[i] != want[i] {
						t.Errorf("%s languages = %v, want %v", tc.name, got, want)
						break
					}
				}
			}
			for _, want := range tc.languages {
				found := false
				for _, got := range agent.ToolRequiredLanguages(tc.tool) {
					if got == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("%s languages = %v, want %q", tc.name, agent.ToolRequiredLanguages(tc.tool), want)
				}
			}
		})
	}
}
