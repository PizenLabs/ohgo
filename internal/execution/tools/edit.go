package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/edit"
)

// EditTool replaces an exact string in a file.
type EditTool struct {
	Base
	// Root bounds every edit.
	Root string
}

// NewEditTool returns an edit tool rooted at root.
func NewEditTool(root string) *EditTool {
	tool := &EditTool{Root: root}
	tool.Base = NewBase("edit",
		"Replace an exact string in a file with another. The string to replace "+
			"must appear exactly once, so include enough surrounding context to "+
			"make it unique.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file, absolute or relative to the working directory.",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "The exact text to replace. It must appear exactly once in the file.",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "The replacement text.",
				},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
		agent.Approval{Tier: agent.TierWrite, Reason: "edits file contents"},
	)
	tool.SetMutationOperations(contract.ChangeModify, contract.ChangeAny)
	tool.SetActionClasses(agent.ActionMutate, agent.ActionRequiresExisting)
	return tool
}

// Execute implements agent.Tool.
func (t *EditTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	raw, err := pathArg(args)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	old, err := strArg(args, "old_string", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	replacement, err := strArg(args, "new_string", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if _, present := args["old_string"]; !present {
		return agent.ErrorResult("missing required argument %q", "old_string")
	}
	if _, present := args["new_string"]; !present {
		return agent.ErrorResult("missing required argument %q", "new_string")
	}
	if old == "" {
		return agent.ErrorResult("old_string must not be empty; use write to create a file")
	}
	if old == replacement {
		return agent.ErrorResult("old_string and new_string are identical")
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

	// Locate the span with the fuzzy matcher rather than a plain
	// strings.Replace. The exact-first, ambiguity-refusing behaviour is
	// preserved; what it adds is a near match, so a model that mis-transcribes
	// one character of a long line gets a correction instead of a failure.
	found := edit.Find(original, old, edit.Options{AllowFuzzy: true})
	switch {
	case found.Matched != nil:
		// Fall through to the replace below.
	case found.Occurrences > 1:
		// Refusing an ambiguous match is safer than guessing, because a wrong
		// guess silently corrupts a file.
		return agent.ErrorResult("old_string appears %d times in %q; include more context to make it unique",
			found.Occurrences, raw)
	case found.Closest != nil:
		return agent.ErrorResult(
			"old_string not found in %q; closest match was line %d (confidence %.2f): %q",
			raw, found.Closest.StartLine, found.Closest.Confidence,
			firstLine(found.Closest.ActualText))
	default:
		return agent.ErrorResult("old_string not found in %q", raw)
	}

	match := found.Matched
	updated, _, err := edit.Apply(original, *match, replacement)
	if err != nil {
		return agent.ErrorResult("apply edit: %v", err)
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return agent.ErrorResult("write %q: %v", raw, err)
	}

	line := match.StartLine
	if line == 0 {
		line = strings.Count(original[:match.StartIndex], "\n") + 1
	}
	text := fmt.Sprintf("Replaced 1 occurrence in %s (line %d).", raw, line)
	if match.Confidence < 1.0 {
		// A fuzzy correction must be visible: silently editing slightly
		// different text than was asked for is the kind of thing that erodes
		// trust in every other edit.
		text += fmt.Sprintf(" Matched approximately (confidence %.2f).", match.Confidence)
	}

	op := agent.MutationModify
	binary := !isTextContent(original) || !isTextContent(updated)
	p := makePreview(raw, op, original, updated, binary)
	ev := evidenceFromPreview(p, "success")
	return agent.Result{
		Text:    text,
		Mutates: true,
		Details: map[string]any{
			"path":              raw,
			"line":              line,
			"confidence":        match.Confidence,
			"operation":         string(op),
			"mutation":          string(op),
			"mutation_type":     string(op),
			"type":              string(op),
			"op":                string(op),
			"diff":              p.Diff,
			"delta":             p.Delta,
			"content_delta":     p.Delta,
			"binary":            p.Binary,
			"unsupported":       p.Unsupported,
			"result":            "success",
			"status":            "success",
			"mutation_evidence": ev,
		},
	}
}

// firstLine returns the first line of a text, for a one-line error message.
func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[:idx]
	}
	return text
}
