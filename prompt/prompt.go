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

	"github.com/PizenLabs/ohgo/project"
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

// ProjectContext renders the project instruction files for root.
//
// The root AGENTS.md is the primary project instruction source and is read
// first; the nearest one wins, because a nested file usually documents the
// package the user is actually working in. The optional
// <project>/.ohgo/AGENTS.md is supplementary OHGO-local instruction: it is
// appended after the primary file and can never displace it. Both files are
// human-authored and rendered verbatim; OHGO never writes either.
//
// The project root is resolved through project.Root, so the supplementary file
// is the one belonging to the project the working directory sits in, not to
// whatever directory the process happened to start from.
func ProjectContext(root string) string {
	var blocks []string
	if primary := nearestAgents(root); primary != "" {
		blocks = append(blocks, "# Project instructions\n\n"+primary)
	}
	local := filepath.Join(project.Root(root), project.DirName, "AGENTS.md")
	if data, err := os.ReadFile(local); err == nil {
		if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			blocks = append(blocks, "# OHGO project instructions\n\n"+trimmed)
		}
	}
	return strings.Join(blocks, "\n\n")
}

// nearestAgents returns the trimmed content of the nearest AGENTS.md at or
// above dir, or "" when none exists. It never walks above the filesystem root.
func nearestAgents(dir string) string {
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")); err == nil {
			return strings.TrimSpace(string(data))
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
