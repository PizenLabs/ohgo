package tools

import (
	"fmt"
	"os"

	"github.com/PizenLabs/ohgo/agent"
)

// DeleteTool removes a file.
type DeleteTool struct {
	Base
	// Root bounds every delete.
	Root string
}

// NewDeleteTool returns a delete tool rooted at root.
func NewDeleteTool(root string) *DeleteTool {
	tool := &DeleteTool{Root: root}
	tool.Base = NewBase("delete",
		"Delete a file from the filesystem.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file, absolute or relative to the working directory.",
				},
			},
			"required": []string{"path"},
		},
		agent.Approval{Tier: agent.TierWrite, Reason: "deletes a file"},
	)
	return tool
}

// Execute implements agent.Tool.
func (t *DeleteTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	raw, err := pathArg(args)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	path, err := resolvePath(t.Root, raw)
	if err != nil {
		return agent.ErrorResult("resolve path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.ErrorResult("read %q: %v", raw, err)
	}
	original := string(data)
	if err := os.Remove(path); err != nil {
		return agent.ErrorResult("delete %q: %v", raw, err)
	}
	binary := !isTextContent(original)
	p := makePreview(raw, agent.MutationDelete, original, "", binary)
	ev := evidenceFromPreview(p, "success")
	details := map[string]any{
		"path":              raw,
		"operation":         string(agent.MutationDelete),
		"mutation":          string(agent.MutationDelete),
		"mutation_type":     string(agent.MutationDelete),
		"type":              string(agent.MutationDelete),
		"op":                string(agent.MutationDelete),
		"diff":              p.Diff,
		"delta":             p.Delta,
		"content_delta":     p.Delta,
		"binary":            p.Binary,
		"unsupported":       p.Unsupported,
		"result":            "success",
		"status":            "success",
		"mutation_evidence": ev,
	}
	return agent.Result{
		Text:    fmt.Sprintf("Deleted %s.", raw),
		Mutates: true,
		Details: details,
	}
}
