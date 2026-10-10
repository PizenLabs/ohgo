// Package prompt builds the system prompt.
//
// Prompts are assembled from this package rather than inlined at call sites so
// there is exactly one place to audit what the model is told.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Identity is the fixed part of the system prompt.
const Identity = `You are ohgo, a coding agent running in the user's terminal.

You help with software engineering tasks: reading and editing code, running
commands, debugging failures, and explaining how code works.

Guidelines:
- Prefer the dedicated tools over shell commands. Use read/edit/write for
  files, glob for finding files, and grep for finding text.
- Make the smallest change that solves the problem. Do not refactor code the
  task did not ask you to touch.
- Never guess a file's contents. Read it before editing it.
- When a tool fails, read the error and adjust. Do not retry the identical
  call.
- Report what you did concisely, without restating the code back to the user.`

// Build assembles the system prompt blocks for one session.
func Build(root string, extra string) []string {
	var blocks []string
	blocks = append(blocks, Identity)

	context := BuildEnvironment(root)
	if context != "" {
		blocks = append(blocks, context)
	}
	if trimmed := strings.TrimSpace(extra); trimmed != "" {
		blocks = append(blocks, trimmed)
	}
	return blocks
}

// BuildEnvironment renders the environment section.
func BuildEnvironment(root string) string {
	var sb strings.Builder
	sb.WriteString("# Environment\n\n")
	fmt.Fprintf(&sb, "Working directory: %s\n", root)
	fmt.Fprintf(&sb, "Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "Date: %s\n", currentDate())
	return sb.String()
}

// ToolCatalog renders the tool roster for the model.
func ToolCatalog(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return "Available tools: " + strings.Join(sorted, ", ") + "."
}

// currentDate formats today's date. It is a variable so tests can pin it.
var currentDate = func() string {
	return time.Now().UTC().Format("2006-01-02")
}

// ProjectContext renders AGENTS.md-style project instructions found in root.
//
// The nearest AGENTS.md wins: a nested file usually documents the package the
// user is actually working in.
func ProjectContext(root string) string {
	dir := root
	for {
		candidate := filepath.Join(dir, "AGENTS.md")
		if data, err := os.ReadFile(candidate); err == nil {
			return fmt.Sprintf("# Project instructions\n\n%s", strings.TrimSpace(string(data)))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// SummarizeInstruction is fed to the model when compacting a conversation.
//
// It asks for a working handover rather than an abstract: the resumed session
// needs the decisions made and the state on disk, not a description of the
// discussion. A vague summary is worse than none because it looks like context.
const SummarizeInstruction = `Summarise the conversation so far so it can
continue without it. Cover, in order:

1. What the user asked for, in their words.
2. Decisions that were made and why, including rejected options.
3. Files that were read, created, or edited, with what changed.
4. Commands that were run and what they showed.
5. What remains to be done, and any known failure still open.

Write in plain prose. Do not include tool syntax, code blocks, or content that
only matters within a single turn. Be specific: name the files, functions, and
error messages, because a later turn will act on this summary.`
