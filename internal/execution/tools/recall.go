package tools

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// RecallTool retrieves one archived observation by its stable id.
//
// Compaction replaces older tool output with an id so the active context stays
// small while the evidence stays addressable. The tool is read-only: it reads
// the run's own archive, never the workspace, and it grants nothing.
type RecallTool struct {
	Base
	store *agent.RecallStore
}

// NewRecallTool returns a recall tool over store.
func NewRecallTool(store *agent.RecallStore) *RecallTool {
	tool := &RecallTool{store: store}
	tool.Base = NewBase("recall",
		"Retrieve a historical observation by its evidence id (for example E12). "+
			"Compaction lists retained ids when it replaces older tool output. "+
			"Use this instead of repeating the original call.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Evidence id shown by compaction, e.g. E12.",
				},
			},
			"required": []string{"id"},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
}

// Execute implements agent.Tool.
func (t *RecallTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return agent.ErrorResult("recall: id is required")
	}
	item, ok := t.store.Get(id)
	if !ok {
		return agent.ErrorResult("recall: no archived observation %q; it may have been evicted or never existed", id)
	}
	return agent.Result{
		Text: item.Text,
		Details: map[string]any{
			"id":   item.ID,
			"tool": item.Tool,
		},
	}
}
