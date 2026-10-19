package tools

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/diff"
	"github.com/PizenLabs/ohgo/edit"
)

// Mutation preview helpers used by the execution layer and the security gate.
// They never execute; they only describe what would change.

func isTextContent(s string) bool {
	if strings.ContainsRune(s, '\x00') {
		return false
	}
	return utf8.ValidString(s)
}

func isTextBytes(b []byte) bool {
	if bytes.Contains(b, []byte{0}) {
		return false
	}
	return utf8.Valid(b)
}

func makePreview(path string, op agent.MutationOp, oldText, newText string, binary bool) agent.MutationPreview {
	p := agent.MutationPreview{
		Path:         path,
		Operation:    op,
		MutationType: op,
		Type:         op,
		Op:           op,
		Binary:       binary,
		Unsupported:  binary,
	}
	if binary {
		return p
	}
	d := diff.Unified(oldText, newText, 3)
	p.Diff = d
	p.Delta = d
	p.Content = d
	p.Text = d
	return p
}

func evidenceFromPreview(p agent.MutationPreview, result string) agent.MutationEvidence {
	e := agent.MutationEvidence{
		Path:         p.Path,
		Operation:    p.Operation,
		MutationType: p.MutationType,
		Type:         p.Type,
		Op:           p.Op,
		Result:       result,
		Status:       result,
		Binary:       p.Binary,
		Unsupported:  p.Unsupported,
	}
	e.Diff = p.Diff
	e.Delta = p.Delta
	e.Content = p.Content
	e.Text = p.Text
	return e
}

// MutationPreviewForTool is the package-level entry point used by the security
// gate, which does not hold a tool instance.
func MutationPreviewForTool(toolName string, root string, args map[string]any) (agent.MutationPreview, bool, error) {
	switch toolName {
	case "write":
		return NewWriteTool(root).MutationPreview(args)
	case "edit":
		return NewEditTool(root).MutationPreview(args)
	case "delete":
		return NewDeleteTool(root).MutationPreview(args)
	}
	return agent.MutationPreview{}, false, nil
}

// WriteTool preview.
func (t *WriteTool) MutationPreview(args map[string]any) (agent.MutationPreview, bool, error) {
	raw, err := pathArg(args)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	content, err := strArg(args, "content", "")
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	if _, present := args["content"]; !present {
		return agent.MutationPreview{}, true, fmt.Errorf("missing required argument %q", "content")
	}
	path, err := resolvePath(t.Root, raw)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	oldContent := ""
	op := agent.MutationCreate
	if data, err := os.ReadFile(path); err == nil {
		op = agent.MutationModify
		oldContent = string(data)
	} else if !os.IsNotExist(err) {
		return agent.MutationPreview{}, true, err
	}
	binary := !isTextContent(oldContent) || !isTextContent(content)
	return makePreview(raw, op, oldContent, content, binary), true, nil
}

// EditTool preview.
func (t *EditTool) MutationPreview(args map[string]any) (agent.MutationPreview, bool, error) {
	raw, err := pathArg(args)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	old, err := strArg(args, "old_string", "")
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	replacement, err := strArg(args, "new_string", "")
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	if _, present := args["old_string"]; !present {
		return agent.MutationPreview{}, true, fmt.Errorf("missing required argument %q", "old_string")
	}
	if _, present := args["new_string"]; !present {
		return agent.MutationPreview{}, true, fmt.Errorf("missing required argument %q", "new_string")
	}
	if old == "" {
		return agent.MutationPreview{}, true, fmt.Errorf("old_string must not be empty")
	}
	if old == replacement {
		return agent.MutationPreview{}, true, fmt.Errorf("old_string and new_string are identical")
	}
	path, err := resolvePath(t.Root, raw)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	original := string(data)
	found := edit.Find(original, old, edit.Options{AllowFuzzy: true})
	if found.Matched == nil {
		return agent.MutationPreview{}, true, fmt.Errorf("old_string not found in %q", raw)
	}
	updated, _, err := edit.Apply(original, *found.Matched, replacement)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	binary := !isTextContent(original) || !isTextContent(updated)
	return makePreview(raw, agent.MutationModify, original, updated, binary), true, nil
}

// DeleteTool preview.
func (t *DeleteTool) MutationPreview(args map[string]any) (agent.MutationPreview, bool, error) {
	raw, err := pathArg(args)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	path, err := resolvePath(t.Root, raw)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.MutationPreview{}, true, err
	}
	original := string(data)
	binary := !isTextContent(original)
	return makePreview(raw, agent.MutationDelete, original, "", binary), true, nil
}

// Alias methods for callers that use a different name.
func (t *WriteTool) PreviewMutation(args map[string]any) (agent.MutationPreview, bool, error) {
	return t.MutationPreview(args)
}
func (t *EditTool) PreviewMutation(args map[string]any) (agent.MutationPreview, bool, error) {
	return t.MutationPreview(args)
}
func (t *DeleteTool) PreviewMutation(args map[string]any) (agent.MutationPreview, bool, error) {
	return t.MutationPreview(args)
}

// Type aliases and constants for callers that use the tools package's own
// vocabulary.
type MutationOp = agent.MutationOp
type MutationPreview = agent.MutationPreview
type MutationEvidence = agent.MutationEvidence

const (
	MutationCreate = agent.MutationCreate
	MutationModify = agent.MutationModify
	MutationDelete = agent.MutationDelete
)

var _ = evidenceFromPreview
var _ = bytes.Contains
var _ = strings.Contains
var _ = fmt.Sprintf
