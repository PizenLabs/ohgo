package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/contract"
)

// Preview limits shared by the read tool and its tests.
const (
	// ReadMaxLines caps how many lines one call returns.
	ReadMaxLines = 2000
	// ReadMaxLineLength caps the width of one rendered line.
	ReadMaxLineLength = 2000
	// ReadDefaultLimit is the line budget when the model does not pick one.
	ReadDefaultLimit = 2000
)

// ReadTool reads a file with line numbers.
type ReadTool struct {
	Base
	// Root bounds every read.
	Root string
}

// NewReadTool returns a read tool rooted at root.
func NewReadTool(root string) *ReadTool {
	tool := &ReadTool{Root: root}
	tool.Base = NewBase("read",
		"Read a file and return its contents with 1-based line numbers. "+
			"Use offset to page through a large file.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file, absolute or relative to the working directory.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based line to start from.",
					"minimum":     1.0,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to return.",
					"minimum":     1.0,
					"maximum":     float64(ReadMaxLines),
				},
			},
			"required": []string{"path"},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
}

// Execute implements agent.Tool.
func (t *ReadTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
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

	info, err := os.Stat(path)
	if err != nil {
		return agent.ErrorResult("stat %q: %v", raw, err)
	}
	if info.IsDir() {
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			return agent.ErrorResult("%q is a directory, not a file", raw)
		}
		if len(entries) == 0 {
			return agent.ErrorResult("%q is an empty directory, not a file", raw)
		}
		names := make([]string, 0, len(entries))
		for i, entry := range entries {
			if i >= 50 {
				break
			}
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		suffix := ""
		if len(entries) > len(names) {
			suffix = fmt.Sprintf(" … (%d entries total)", len(entries))
		}
		return agent.ErrorResult("%q is a directory, not a file; entries: %s%s",
			raw, strings.Join(names, ", "), suffix)
	}

	offset, err := intArg(args, "offset", 1)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	limit, err := intArg(args, "limit", ReadDefaultLimit)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if offset < 1 {
		return agent.ErrorResult("offset must be >= 1, got %d", offset)
	}
	if limit < 1 {
		return agent.ErrorResult("limit must be >= 1, got %d", limit)
	}
	if limit > ReadMaxLines {
		limit = ReadMaxLines
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return agent.ErrorResult("read %q: %v", raw, err)
	}
	if len(data) == 0 {
		return agent.Result{Text: fmt.Sprintf("%s is empty.", raw), Useless: true}
	}

	lines := strings.Split(string(data), "\n")
	// A trailing newline produces a final empty element that is not a line.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	total := len(lines)
	start := offset - 1
	if start >= total {
		return agent.ErrorResult("offset %d is past the end of %q (%d lines)", offset, raw, total)
	}
	end := min(start+limit, total)

	var sb strings.Builder
	for i := start; i < end; i++ {
		if err := checkCancelled(ctx.Ctx); err != nil {
			return agent.ErrorResult("%v", err)
		}
		line := lines[i]
		if len([]rune(line)) > ReadMaxLineLength {
			runes := []rune(line)
			line = string(runes[:ReadMaxLineLength]) + "… [truncated]"
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "%d\t%s", i+1, line)
	}

	if end < total {
		fmt.Fprintf(&sb, "\n\n[showing lines %d-%d of %d]", start+1, end, total)
	}

	return agent.Result{
		Text: sb.String(),
		Details: map[string]any{
			"path":       raw,
			"offset":     offset,
			"limit":      limit,
			"totalLines": total,
		},
	}
}

// WriteTool writes a file, replacing its contents.
type WriteTool struct {
	Base
	// Root bounds every write.
	Root string
}

// NewWriteTool returns a write tool rooted at root.
func NewWriteTool(root string) *WriteTool {
	tool := &WriteTool{Root: root}
	tool.Base = NewBase("write",
		"Write content to a file, creating it and any missing parent "+
			"directories. Replaces the file when it already exists.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to write, absolute or relative to the working directory.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The complete file contents.",
				},
			},
			"required": []string{"path", "content"},
		},
		agent.Approval{Tier: agent.TierWrite, Reason: "overwrites file contents"},
	)
	tool.SetMutationOperations(contract.ChangeCreate, contract.ChangeModify, contract.ChangeAny)
	return tool
}

// Execute implements agent.Tool.
func (t *WriteTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	raw, err := pathArg(args)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	content, err := strArg(args, "content", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if _, present := args["content"]; !present {
		return agent.ErrorResult("missing required argument %q", "content")
	}

	path, err := resolvePath(t.Root, raw)
	if err != nil {
		return agent.ErrorResult("resolve path: %v", err)
	}

	oldContent := ""
	op := agent.MutationCreate
	if data, err := os.ReadFile(path); err == nil {
		op = agent.MutationModify
		oldContent = string(data)
	} else if !os.IsNotExist(err) {
		return agent.ErrorResult("read %q: %v", raw, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return agent.ErrorResult("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return agent.ErrorResult("write %q: %v", raw, err)
	}

	binary := !isTextContent(oldContent) || !isTextContent(content)
	p := makePreview(raw, op, oldContent, content, binary)
	ev := evidenceFromPreview(p, "success")
	return agent.Result{
		Text:    fmt.Sprintf("Wrote %d bytes to %s.", len(content), raw),
		Mutates: true,
		Details: map[string]any{
			"path":              raw,
			"bytes":             len(content),
			"lines":             strings.Count(content, "\n") + 1,
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
