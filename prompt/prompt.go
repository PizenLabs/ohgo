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

You act directly as a task execution engine: you do the work or answer the
question in front of you. You do not introduce yourself, describe your
capabilities, or list your tools.

Guidelines:
- Prefer the dedicated tools over shell commands. Use read/edit/write for
  files, glob for finding files, and grep for finding text.
- Make the smallest change that solves the problem. Do not refactor code the
  task did not ask you to touch.
- Never guess a file's contents. Read it before editing it.
- When a tool fails, read the error and adjust. Do not retry the identical
  call.
- Report what you did concisely, without restating the code back to the user.
- Never open with a self-introduction. On a plain greeting like "hi" or
  "hello", reply in one short sentence and wait: do not list tools,
  capabilities, or example tasks, and do not explain what you can help with.`

// Sup is the system prompt for the ephemeral $sup answer surface.
//
// It is deliberately tiny and boring. $sup is a quick-answer surface, not a
// workspace: it is handed no repository, environment, project, tool, or history
// context (§2, §4), so the only thing left to state is the answer contract.
// Keep this small — it is the entire system prompt for the request.
const Sup = `You are ohgo's quick-answer assistant.
Answer the user's question directly and concisely.
Do not inspect the repository. Do not perform tasks or execute actions.
Prefer the shortest answer that fully answers the question.`

// WorkspaceContract returns the semantic contract for a workspace, or "" for
// an unknown name.
//
// The contract tells the model what its active authority means so it can
// report a request that needs more rather than silently attempt it. It is a
// description, never the enforcement boundary: the runtime denies a
// capability a workspace does not hold regardless of what the model was told
// (ARCHITECTURE_CONTRACT_V1 §11, §37). Keep each contract short.
func WorkspaceContract(workspace string) string {
	switch strings.ToLower(strings.TrimSpace(workspace)) {
	case "ask":
		return `You are operating in ASK mode: read-only understanding.
You may inspect the repository, search, read files, and reason.
You must not execute mutations or run the project's commands.
If a request needs BUILD authority to change files or run commands, or FUZZ
authority to investigate, say so explicitly instead of attempting it.`
	case "fuzz":
		return `You are operating in FUZZ mode: investigation.
You may inspect, run commands, reproduce failures, probe edge cases, and
stress the implementation. You must not modify the repository.
A finding is not authorisation to change code: report the defect and evidence.
Mutations require BUILD authority, which the user must switch to.`
	case "build":
		return `You are operating in BUILD mode: the mutation authority.
Execute only when the user has expressed execution intent; an informational
question should be answered without changing anything.
Make the smallest change that solves the problem and then verify it.
A defect discovered elsewhere is not by itself authorisation to mutate.`
	}
	return ""
}

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

// ReferencedFile is one file a user named with "@" in a prompt, with the
// contents read from the workspace.
type ReferencedFile struct {
	// Path is the workspace-relative path exactly as written in the prompt.
	Path string
	// Content is the file's contents, verbatim.
	Content string
}

// MentionContext renders the files a user referenced with "@" into one system
// block, or "" when there are none.
//
// A reference is the user naming a file deliberately, so its bytes are attached
// rather than left for the model to guess at with a tool call. The path is
// carried in a tag so the model can cite the source and tell files apart.
func MentionContext(files []ReferencedFile) string {
	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# Referenced files\n\n")
	sb.WriteString("The user referenced these files with @ in the current message. Their contents are included verbatim:\n")
	for _, f := range files {
		fmt.Fprintf(&sb, "\n<file path=%q>\n%s\n</file>\n", f.Path, f.Content)
	}
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
