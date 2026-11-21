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

	"github.com/PizenLabs/ohgo/internal/project"
)

// Identity is the fixed part of the system prompt.
const Identity = `You are ohgo, a coding agent running in the user's terminal.

You act directly as a task execution engine: you do the work or answer the
question in front of you. You do not introduce yourself, describe your
capabilities, or list your tools.

Guidelines:
- Prefer the dedicated tools over shell commands. Use read/edit/write for
  files, glob for finding files, grep for finding text, symbols for
  locating declarations and their repository coordinates across registered
  languages, graph for Go package imports, refs for exact symbol references
  and statically resolved callers/callees, and structure for bounded project
  inventory (languages, files, packages, tests, configs, entrypoints,
  explicit relations, and static impact). Overview is metadata-first; ask for
  a targeted or impact view only when the task needs declarations or
  relationships.
- Use investigate first for relational questions: call chains, callers or
  callees, dependency and impact analysis, test impact, interface
  implementations, and architecture flow. It returns bounded facts with
  provenance and uncertainty. Do not follow a sufficient investigation with
  broad graph dumps or repeated symbol searches; use symbols, refs, graph, or
  read for one exact coordinate or source excerpt.
- An investigation is evidence, not proof. When it identifies a concrete
  declaration or call site, read that exact coordinate before claiming what
  the implementation does. Do not stop at a candidate list.
- When the runtime state asks for an investigation conclusion, call conclude
  with SAFE, BUG, or UNRESOLVED, the evidence that supports it, and status
  FINAL for a terminal verdict or IN_PROGRESS for a progress observation.
  Call FINAL only after the entrypoint and validation/persistence boundary are
  observed. Do not use UNRESOLVED as a progress report. An IN_PROGRESS call
  never satisfies the conclusion; a placeholder or preliminary payload is
  treated as progress even when the status says FINAL. If uncertainty names a
  concrete symbol or path, inspect that coordinate first; the runtime keeps the
  conclusion requirement open until it is observed or a bounded attempt
  explicitly settles as UNRESOLVED. Do not keep surveying after the required
  entrypoint, reference flow, validation boundary, and bypass paths are
  established.
- If no Gry or runtime evidence was obtained, the static-vs-runtime comparison
  was not assessable. Say that plainly; do not claim that no contradiction
  exists. Use the read tool for source content when available; a successful
  shell content read (sed, cat, head, tail) also opens the file, but a
  listing or a path mentioned in prose does not.
- Use check to discover the repository's own tests, static analysis, and build
  commands, then run only the kinds the task needs. Discovery is read-only;
  run mode returns structured outcomes and verification evidence. Use verify
  when the acceptance requires starting a service, checking it while it is
  live, and stopping it. bash output is an observation; it is not the
  structured verification record the completion contract requires.
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

// AnswerRequest is the user-role message the loop appends when an answer-only
// request returned no visible text. The action surface is already closed, so
// the only useful next output is the answer; saying that in the conversation
// is what breaks a model out of a tool-call loop that has no tool left to call.
// It is runtime-authored, never persisted as a user turn, and never shown in
// the transcript.
const AnswerRequest = `Runtime: no tool is available for this response. Provide the final answer to the user's request now, using the observations already gathered where they apply. Do not propose or describe another tool call. State any remaining evidence gap explicitly.`

// ExecutionRecoveryRequest is the one bounded correction the loop appends when
// the model announces an action but emits no tool call. It does not authorize
// anything and does not restate the action: it asks for the model's own
// canonical call, so the runtime can normalize and authorize it normally.
const ExecutionRecoveryRequest = `Runtime protocol recovery: your previous message announced an action but contained no tool call. Do not describe the action again. Emit the actual tool call now with complete arguments, or stop and answer from evidence already observed.`

// ProtocolRecoveryRequest is the one bounded correction the loop appends when
// serialized provider tool syntax could not be normalized. The malformed
// fragment was not executed and no arguments were invented; the model must
// re-emit the operation through the canonical tool channel.
const ProtocolRecoveryRequest = `Runtime protocol recovery: your previous message contained serialized tool syntax that could not be normalized into a tool call. The malformed call was not executed. Re-emit the operation as a native tool call with complete arguments, or answer without protocol syntax. Do not repeat the serialized envelope.`

// CompletionReviewRequest is the one bounded correction the loop appends when a
// candidate answer fails the answer-level completion review. The placeholder is
// replaced with the runtime's evidence reason. The model may use an available
// tool to acquire the missing observation or qualify the claim; it must not
// repeat an unsupported conclusion. It is runtime-authored, never persisted as a
// user turn, and never shown in the transcript.
const CompletionReviewRequest = `Runtime completion review: the previous answer cannot be accepted because {{reason}}. Correct it now using evidence already observed, or use an available tool to obtain the missing evidence. Do not repeat a claim the evidence does not support. If the missing fact cannot be established, state that limitation explicitly instead of completing the claim.`

// ClaimRepairRequest is the bounded, claim-level correction. It is used in
// place of CompletionReviewRequest when the review can name the rejected
// claims. The model must repair only those sentences, keep the rest of the
// answer, and stay inside the correction's small tool allowance. It is
// runtime-authored, never persisted as a user turn, and never shown in the
// transcript.
const ClaimRepairRequest = `Runtime completion review: {{count}} claim(s) in the previous answer lack sufficient evidence. Repair only these claims; keep every other sentence, structure, and supported claim exactly as it is. The initial observations are already in the conversation — do not repeat a read, listing, or structural query that is already present unless the listed repair says the evidence is stale or missing. You may use at most {{budget}} tool call(s), only for the evidence named below. If a claim cannot be evidenced, qualify or remove that sentence and state the remaining uncertainty.

{{repairs}}

Audit reason: {{reason}}`

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
When an established fix needs a repository change, the control plane hands this
same task to BUILD automatically before the change executes. Propose the change
with your normal tool call; do not stop to ask for a switch.
When the investigation asks for a verdict, call conclude once the required
entrypoint, reference flow, validation boundary, persistence ordering, and
bypass-path question are established. Another survey cannot satisfy a verdict.
Use status IN_PROGRESS while evidence is still being gathered; only FINAL
counts as a settlement. If the conclusion names a concrete unresolved symbol or
path, inspect that coordinate first; omitting it from a later conclusion does
not erase the obligation.
Similarly, call conclude when the runtime state reports investigation_conclusion
as the only missing requirement.`
	case "build":
		return `You are operating in BUILD mode: the mutation authority.
The user's current objective is an execution instruction, not a request for
advice. Inspect the repository, reason, derive an internal plan, and carry
the objective through to implementation and verification.
Task status, todos, and persisted plans are state and context, not blockers:
a missing plan means derive one internally, never stop to ask the user.
After a repository change, run the runtime's check tool for the tests, static
analysis, or build the change needs. Use verify when acceptance requires a
live service. A bash assertion is an observation, not the structured
verification evidence the completion contract requires.
Mutations remain subject to the runtime security policy.`
	}
	return ""
}

// InvestigationHint returns a one-line runtime hint for a turn whose text is
// asking for repository relationships rather than one exact coordinate.
//
// It is recency steering, not authority: the tool still decides what evidence
// exists, and the model may answer from evidence it already has. The hint is
// only emitted for the structural classes where a chain of grep/symbol/read
// calls usually costs more than one bounded investigation.
func InvestigationHint(text string) string {
	lower := strings.ToLower(text)
	structural := []string{
		"call chain", "caller", "callee", "called by", "calls ",
		"depend on", "depends on", "dependency", "dependencies", "imports", "imported by",
		"impact", "affected", "architecture", "flow from", "reaches",
		"route", "handler", "implements", "implementation of", "references",
		"used by", "what uses",
	}
	matched := false
	for _, phrase := range structural {
		if strings.Contains(lower, phrase) {
			matched = true
			break
		}
	}
	if !matched {
		return ""
	}
	return "Runtime information routing: this is a structural relationship question. " +
		"Prefer one bounded investigate call before a chain of grep/symbol/read calls; " +
		"then read the exact coordinate it identifies. An investigation is evidence, not proof."
}

// ProposalDecision renders a recorded human proposal decision as
// control-plane-authored system text for the turn that decision releases.
//
// The copy lives here, beside the other system-prompt text, so there is
// exactly one place to audit what the model is told. A delegated choice has no
// directive and reports false: the human explicitly granted the authority, so
// there is nothing to convey.
func ProposalDecision(label, directive, question, subject string) (string, bool) {
	if strings.TrimSpace(directive) == "" {
		return "", false
	}
	var sb strings.Builder
	sb.WriteString("Execution decision (decided by the human before execution; authoritative, not a model inference):")
	if subject != "" {
		sb.WriteString("\nRequested work: ")
		sb.WriteString(subject)
	}
	if question != "" {
		sb.WriteString("\nDecision: ")
		sb.WriteString(question)
	}
	sb.WriteString("\nChoice: ")
	sb.WriteString(label)
	sb.WriteString(".\n")
	sb.WriteString(directive)
	return sb.String(), true
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
