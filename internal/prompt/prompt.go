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
  files, grep for exact or regex text lookup, glob for path discovery, symbols
  for locating declarations and their repository coordinates across registered
  languages, graph for Go package imports, refs for exact symbol references
  and statically resolved callers/callees, and structure for bounded project
  inventory (languages, files, packages, tests, configs, entrypoints,
  explicit relations, and static impact). Use retrieve when you need
  relevance-ranked candidate files rather than an exact match. Overview is
  metadata-first; ask for a targeted or impact view only when the task needs
  declarations or relationships. Do not run grep, rg, find, or ls through
  bash: the dedicated tools apply the shared repository ignore scope and name
  excluded paths, while a raw shell command does not.
- Use investigate first for relational questions: call chains, callers or
  callees, dependency and impact analysis, test impact, interface
  implementations, and architecture flow. It returns bounded facts with
  provenance and uncertainty. Do not follow a sufficient investigation with
  broad graph dumps or repeated symbol searches; use symbols, refs, graph, or
  read for one exact coordinate or source excerpt.
- An investigation is evidence, not proof. When it identifies a concrete
  declaration or call site, read that exact coordinate before claiming what
  the implementation does. Do not stop at a candidate list.
- A broad project analysis is a model of the project, not an inventory. Read
  the entry point and the implementation files it leads to (the data loader,
  the main component or service, the persistence boundary) before answering.
  Explain how the pieces connect: where data comes from, how it is loaded and
  transformed, what renders or serves it, and what the main interaction is. A
  list of files, components, or dependencies is not an analysis; name the
  relationships you actually read.
- Separate observed facts from documentation and inference. Attribute a
  README, manifest, or metadata claim ("according to package.json"), label an
  inference as inferred, and say plainly when a relationship or runtime
  behavior was not observed. When the runtime reports partial structural
  coverage, name the part of the repository the answer did not inspect.
- When the runtime state asks for an investigation conclusion, call conclude
  with the vocabulary that state names. A failure investigation records SAFE,
  BUG, or UNRESOLVED. A general answer about the project records ANSWERED,
  PARTIAL, or UNRESOLVED; SAFE and BUG are not answers to "what is this
  project?". Include the evidence that supports the verdict and status FINAL
  for a terminal verdict or IN_PROGRESS for a progress observation. Call FINAL
  only after the evidence the runtime asked for is observed. Do not use
  UNRESOLVED as a progress report. An IN_PROGRESS call never satisfies the
  conclusion; a placeholder or preliminary payload is treated as progress even
  when the status says FINAL. If uncertainty names a concrete symbol or path,
  inspect that coordinate first; the runtime keeps the conclusion requirement
  open until it is observed or a bounded attempt explicitly settles as
  UNRESOLVED. Do not keep surveying after the required entrypoint, reference
  flow, validation boundary, and bypass paths are established.
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
const AnswerRequest = `Runtime: no tool is available for this response. Provide the final answer to the user's request now, using the observations already gathered where they apply. Do not propose or describe another tool call. Keep the answer substantive. Name only a specific limitation that materially bounds it; do not add a generic coverage disclaimer.`

// AnswerPlanRequest is the directive carried when the control plane has
// reached its answer boundary but the pre-prose claim preflight has not
// passed. It asks for claims, not prose: the runtime audits each one against
// the observations and reopens the evidence surface when a read is missing.
const AnswerPlanRequest = `Runtime answer boundary: the evidence phase is complete. Before writing the final answer, call prepare_answer with every factual claim the answer will make, one complete declarative sentence per entry, and list honest scope limits separately under limitations. The runtime audits those claims against the observations and returns any missing reads. If a previous plan was refused, do not resubmit a refused claim unchanged: support it with the missing observation or move it to limitations. Do not write the answer in this request; the answer request follows an accepted plan.`

// AnswerPlanEvidenceRequest is the directive carried on the evidence request
// after a refused plan. The plan tool is withheld on that request so the model
// cannot repeat the same proposal; the refusal already names the missing reads.
const AnswerPlanEvidenceRequest = `Runtime answer plan refused: prepare_answer named claims the observations do not support. Use the available evidence tools now to read the subjects the refusal named, or narrow the claim to what was actually observed. The plan tool is withheld until a successful observation is recorded. Do not repeat the plan or write the final answer in this request.`

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

// ToolArgumentRecoveryRequest is the one bounded correction the loop appends
// when a streamed tool call closed with incomplete argument JSON. Nothing from
// that call executed. The directive is runtime-authored and never persisted.
const ToolArgumentRecoveryRequest = `Runtime protocol recovery: your previous tool call was not executed because its argument JSON was incomplete or could not be decoded. Re-emit the action with complete, valid arguments. If the payload is large, do not repeat the same oversized call: create a complete file first and make the remaining changes with smaller edits, or reduce the scope of this call. Nothing from the incomplete call changed the workspace.`

// OutputRecoveryRequest is the one bounded correction the loop appends when a
// mid-run turn produced neither a tool call nor visible answer text — either it
// contained only hidden reasoning or the provider cut it off at the completion
// ceiling. The turn could not act or answer, so the directive asks for the next
// concrete output without spending another response on
// planning. It is runtime-authored and never persisted.
const OutputRecoveryRequest = `Runtime recovery: your previous response produced no tool call and no answer, so nothing was executed and the user received no result. Produce the next concrete output immediately with minimal planning: either emit the actual tool call with complete arguments, or, if the evidence is sufficient, write the final answer. Do not restate the plan or repeat the analysis.`

// CompletionReviewRequest is the one bounded correction the loop appends when a
// candidate answer fails the answer-level completion review. The placeholder is
// replaced with the runtime's evidence reason. The model may use an available
// tool to acquire the missing observation; a generic disclaimer is not a
// correction. It is runtime-authored, never persisted as a user turn, and never
// shown in the transcript.
const CompletionReviewRequest = `Runtime completion review: the previous answer cannot be accepted because {{reason}}. The previous candidate was withheld and has not been shown to the user; the corrected answer must be complete. The action surface is open for this repair. First obtain the missing observation with the available tools; the runtime reopened them for this purpose. Keep every supported sentence and the answer's structure. Do not add a general disclaimer about coverage or uncertainty; a disclaimer does not resolve an unrelated claim. If the allowed tools cannot establish a named fact, qualify or remove only the sentence that carries it and say what specifically remains unverified.`

// ClaimRepairRequest is the bounded, claim-level correction. It is used in
// place of CompletionReviewRequest when the review can name the rejected
// claims. The model must repair only those claims, keep the rest of the
// answer, and stay inside the correction's small tool allowance. It is
// runtime-authored, never persisted as a user turn, and never shown in the
// transcript.
const ClaimRepairRequest = `Runtime completion review: {{count}} claim(s) in the previous answer are not supported by the evidence observed so far. The previous candidate was withheld and has not been shown to the user; the repaired answer must be complete. This is a repair turn, not a rewrite: keep every supported sentence, section, and structural detail exactly as it is. The action surface is open for this repair.

For each claim below, first obtain the missing observation its requirement/action names. Use the reopened surface only for those observations; the runtime enforces a small allowance and refuses a call past it. Read the exact coordinate the claim is about when the prior observation did not cover it; do not repeat an observation that already contains the required evidence.

Only if those calls cannot establish the claim: remove that claim or qualify that sentence specifically. Do not add a general coverage or uncertainty disclaimer; it does not repair a different claim and will be rejected.

Allowed repair actions: {{actions}}.

{{preserved}}

{{repairs}}

Audit reason: {{reason}}`

// CorrectionAnswerRequest is the final-answer boundary after the correction
// tool allowance is spent. The model still sees its earlier draft in the live
// conversation so it can preserve supported claims, but that draft was
// withheld and never delivered. Without this statement a model treats the
// draft as a previous answer and returns a completion confirmation instead of
// the full answer.
const CorrectionAnswerRequest = `Runtime answer correction: your earlier candidate answer was withheld and was not shown to the user. It is an internal draft, not a delivered answer. The repair tool allowance is spent. Write the complete final answer now from the evidence already observed. Keep every supported section, finding, and structural detail; qualify or remove only the claims the review named. Do not write a completion confirmation, do not refer to "the analysis above" or to a previous answer, and do not assume the user has seen any draft.`

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
a missing plan means derive one internally, never stop to ask the user. If you
created a plan, reconcile each open item (done, or removed if you will not do
it) before your final answer so the task boundary and your plan agree. A blocked
item is still open and keeps the task active.
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
