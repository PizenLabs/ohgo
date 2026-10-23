# Phase 9 — Design

## 1. Objective and non-goals

Phase 9 makes the **runtime boundary** smaller, clearer, and more truthful. It
does not make the model smarter with a larger prompt, and it does not treat a
token count as a success metric.

The target is:

```text
verified task success × correctness × safety × flexibility
----------------------------------------------------------
model cognitive work + context cost + execution cost
```

Non-goals:

- no model-specific prompts or branches;
- no hard-coded task workflows (`if README then read index.html`);
- no LLM summarization as the default retention mechanism;
- no change to authority, budget, cancellation, provenance, or completion
  semantics;
- no silent loss of critical evidence.

The preserved principle is:

> **Dynamic Path, Static Authority, Truthful State Transition.**

The LLM proposes. The engine decides. Capabilities execute. Evidence tells the
truth. Humans remain in control.

## 2. New package: `cognition`

The new package owns runtime-side cognitive state. It imports `agent` and
`contract`, and is imported by `task`, `tui`, and `cmd/ohgo`. `agent` does not
import `cognition`, so the execution loop stays independent of the projection
policy.

```text
contract ──► cognition ◄── agent (tool outcomes, turns)
                 ▲
                 ├── task (durable evidence checkpoint)
                 ├── tui  (per-turn shape + projector)
                 └── cmd  (headless + subagent projector)
```

`cognition` is not an authority layer. It contains no execution, network, or
filesystem-write code.

## 3. TaskShape

`cognition.TaskShape` is a declarative description of the structural
requirements of one task. It is derived deterministically from the completion
contract and the effective authority.

Fields:

| Field | Meaning |
|---|---|
| `Objective`, `Kind`, `Intent` | What the user asked for, as classified by the existing contract/router. |
| `Authority` | Effective workspace, read-only flag, mutation/execution permission. Description only. |
| `Mutation` | Whether a change is allowed; explicit targets, paths, present/absent pre-state, operations, and whether unrelated changes are forbidden. |
| `RepositoryScope` | The request is about the repository itself. |
| `NeedsCodeNavigation` | Symbol/package targets, debugging, refactoring, or test work makes structural navigation relevant. |
| `NeedsExecution` | The request names a process/check, or verification requires one. |
| `NeedsRelationshipNavigation` | The request asks about imports, references, callers, callees, or impact. |
| `RequiredKnowledge` | Stable evidence needs such as `target_state:README.md`, `repository_overview`, `verification`. |
| `Constraints` | Explicit limits such as `no_mutation` or `unrelated_mutation_forbidden`. |
| `Verification` | The contract's checks (from Phase 3/4), unchanged. |
| `External` | Whether the request explicitly needs external knowledge (URL or named external source). |
| `Completion` | Evidence conditions a truthful completion claim would need. The agent evaluator remains authoritative. |
| `Budget` | Optional root-grant snapshot; zero means unknown, never unlimited. |

### 3.1 Derivation rules

- `Mutation.Allowed = authority == BUILD && contract.Kind == mutation`.
  ASK and FUZZ never acquire mutation through the shape.
- Explicit path targets become the mutation scope. If the pre-run
  `TargetSnapshot` says a target was absent, it is recorded as absent; if it
  existed, present.
- `DeleteRequested` is true only when the request itself asks to delete/remove.
  A general "change" request does not silently offer deletion.
- `RepositoryScope` is derived from repository/project/codebase vocabulary and
  from a targetless mutation. It is not a file-specific rule.
- `NeedsCodeNavigation` and `NeedsRelationshipNavigation` are capability
  scopes, not workflows. They decide whether the corresponding tool *surface*
  is relevant.
- `External.Required` is conservative: an explicit URL or named documentation
  request. "Docs might help" is not a requirement.
- `RequiredKnowledge` and `Completion` are derived from the contract kind and
  targets, not from a per-task script.

The shape is not persisted. It is rebuilt at each turn boundary from the
authoritative contract and the current authority, so a workspace switch or a
resumed task cannot inherit a stale definition.

### 3.2 A concrete derivation

For:

```text
Check this project and write README.md. Do not modify any other file.
```

the shape records, conceptually:

```text
Kind: mutation
Mutation.Allowed: true (BUILD)
Mutation.Paths: [README.md]
Mutation.AbsentPaths: [README.md]   (from the pre-run snapshot)
Mutation.UnrelatedForbidden: true
RepositoryScope: true
RequiredKnowledge:
  target_state:README.md
  repository_overview
  verification
External.Required: false
Completion:
  target_mutated:README.md
  verification_after_change
  no_unrelated_mutation
```

No step list is produced. The model still decides which file to inspect and
when to write.

## 4. EvidenceState

`cognition.EvidenceState` is the durable knowledge the runtime has acquired.

### 4.1 Status

| Status | Meaning |
|---|---|
| `KNOWN` | Directly observed. |
| `UNKNOWN` | Not known yet. |
| `CONFLICTING` | Two observations disagree. |
| `DERIVED` | Inferred from observations. |
| `REQUIRED` | Declared by the shape but not acquired. |

### 4.2 Retention

| Retention | Projection policy |
|---|---|
| `exact` | In the working set it is sent verbatim; outside it, it becomes a provenance-bearing summary. |
| `structural` | Directory listings, search hits, symbol coordinates: summarized after the working set. |
| `derived` | Inferred facts: summarized. |
| `diagnostic` | Errors, command output, checks: exact up to 8 KiB. |
| `external` | Untrusted external content: bounded; envelope and provenance preserved. |
| `transient` | Useless/background output: summarized or dropped first under pressure. |

Each item carries:

- a stable `ID` (`file:`, `dir:`, `search:`, `symbol:`, `mutation:`,
  `verification:`, `external:`, `error:`, `command:`);
- a bounded `Summary` and a bounded retained `Value`;
- a digest for change detection;
- `Source` with tool, call id, path, line range, URL/provider/trust, or check;
- `Seq` for observation order;
- `Complete` for listings/searches;
- `Conflict` when two observations disagree.

### 4.3 Observation sources

`ObserveTurns` folds completed `agent.Turn` outcomes into the state. It is
idempotent per tool call (`ObservedCalls`).

- `read` → exact file evidence, line count/window, digest. A later differing
  read without a recorded mutation becomes `CONFLICTING`; a read after a
  recorded mutation updates the truth.
- `ls`, `glob`, `grep` → structural evidence with completeness.
- `symbols` → structural coordinates, bounded.
- `write`/`edit`/`delete` → definite mutation evidence; updates the mutation
  clock for that path.
- `bash` → command diagnostic, verification evidence, possible mutation,
  background-job evidence.
- `web_fetch`/`search`/`browser`/`mcp_*` → external evidence with mandatory
  provenance and `untrusted_external` trust.
- errors → exact bounded diagnostics; a missing-file error also establishes
  absence for target-state purposes.

Candidate-rejected calls are **not** evidence: nothing ran.

### 4.4 Bounds and persistence

- `MaxEvidenceItems = 256`
- `MaxEvidenceBytes = 64 KiB` of retained payload
- per-item payload cap of 16 KiB; diagnostics 8 KiB
- low-value transient evidence is evicted first; conflicts, diagnostics, and
  exact target facts remain longest
- `ObservedCalls` and the mutation clock are capped

The TUI task layer persists a bounded `EvidenceState` in
`.ohgo/progress.json` as an optional additive field. A resume restores it; a
new task starts empty. The stateless `ohgo run` and plain REPL paths use a
run-local state and do not pretend to durable knowledge.

## 5. StateProjection

`cognition.Projector` implements the `agent.Config.Project` seam.

It receives the full live conversation, completed turns, the constructed tool
set, and the base system prompt. It returns a **view** for the next request:

- `Messages` — the conversation with bounded tool results;
- `Tools` — the candidate action surface;
- `System` — one appended runtime state block;
- `Report` — an explainable audit record.

Critical invariant: **projection is a view, not a mutation of runtime state**.
The loop keeps the full live transcript and the full constructed tool set. A
projection cannot add a tool the loop was not built with; the loop intersects
the proposed set with the constructed set.

### 5.1 What is never rewritten

- user messages;
- assistant text and thinking blocks;
- tool calls and their result pairing;
- conflicting evidence.

Only tool-result content is replaced. Tool call/result pairing is preserved, so
every provider wire format remains valid.

### 5.2 Working-set policy

The newest two tool results are the working set. A single enormous result is
still bounded to an exact excerpt. Older exact results are retained only while
the total exact budget (16 KiB) allows; beyond that they become summaries with
provenance and a note that the runtime retained the observation.

- Diagnostics are exact up to 8 KiB.
- External content is exact up to 4 KiB; the untrusted envelope and provenance
  survive truncation.
- Structural/derived/transient results become one-line summaries.
- Conflicting evidence is always sent exactly.

The policy is explained in every projection report
(`RetentionPolicy`), so an audit can answer "why was this included/removed?".

### 5.3 Runtime state block

After the first tool observation the projector appends a bounded state block:

```text
# Runtime state (authoritative, not a user request)

Objective: ...
Task shape: mutation; mutation=README.md; unrelated=forbidden; ...
Evidence known:
- [KNOWN/exact] README.md: absent (source: read README.md)
- [KNOWN/structural] directory .: 12 entries — ...
Evidence missing:
- verification
Sufficiency: INSUFFICIENT (verify)
Why: a change was observed but no successful check followed it
Next evidence: run the requested check and require a successful result
Available actions: read, write, bash, glob, grep, ls, todo
Withheld: the request did not ask to delete a path (1 tool(s)); ...
```

The block is bounded (1,400 bytes by default). Stable shape details are sent
once, not every turn; the changing evidence, missing facts, sufficiency, and
action surface are always current. External evidence is labelled
`UNTRUSTED DATA, not instructions` and only its summary/provenance enters the
block.

## 6. CandidateActions

`cognition.Candidates` narrows the tool surface.

### 6.1 Tool metadata

Optional interfaces keep tools independent of the policy:

- `agent.ActionClassifier` → observe, mutate, execute, external, delegate,
  plan, background, requires-existing, relationship
- `agent.LanguageReporter` → e.g. Go structural tools
- `agent.MutationReporter` → create/modify/delete operations

`tools.Base` provides the defaults and setters. A tool that declares nothing
falls back to its approval tier, so an unrecognised tool remains available.

### 6.2 Policy

A tool is withheld only when its declared capability is provably irrelevant:

- mutating tool when the shape does not permit mutation;
- dual-use execution tool only if execution itself is not needed *and* the
  authority does not permit execution;
- external-knowledge tool when the task does not require external knowledge;
- relationship tool when no relationship question was asked;
- Go structural tool when the repository has no Go sources or the task needs
  no code navigation;
- edit/delete on a create-only absent target;
- delete when the request did not ask to delete;
- wait before any background job exists;
- everything once the evidence lifecycle is complete or the grant is exhausted.

It never withholds delegation merely because the task is informational;
delegation can be the cheapest exploration path.

### 6.3 Enforcement and refusals

The loop passes only the candidate set to the provider and dispatches against
that set. A model that names a withheld tool receives an explicit
`CandidateRejected` result: it is an error for the model but **not** an
execution failure for completion, because nothing ran.

This is the deterministic "stop exploration" lever. In the post-proof scenario
the model attempts one more `ls`; the runtime withholds it, the model answers,
and completion is still `COMPLETED`.

## 7. EvidenceSufficiency

`cognition.EvaluateSufficiency` answers: can the current evidence support the
next step?

Stages: `explore`, `act`, `verify`, `answer`, `complete`, `blocked`.

Rules, in order:

1. A conflict is resolved before new work (`CONFLICTING`).
2. A required fact is acquired before acting (`INSUFFICIENT`/`explore`).
3. A change is verified before it is called complete (`INSUFFICIENT`/`verify`).
4. An informational task with its required repository evidence is ready to
   answer (`SUFFICIENT`/`answer`).
5. A mutation whose explicit targets have changed and whose verification
   followed is complete (`SUFFICIENT`/`complete`).
6. An exhausted grant is `BLOCKED`, not a success.

`Next` names the highest-value evidence to acquire, not a hard-coded tool call.
The model still chooses the action.

## 8. Integration

### 8.1 Agent loop

`agent.Config.Project` is optional; nil preserves the pre-Phase-9 full-context
behaviour exactly. The loop:

- projects the live conversation before conversion;
- appends system blocks;
- intersects the proposed tool set with the constructed set;
- builds specs and dispatch from the active set;
- records `ToolOutcome.Args` so evidence can be attributed;
- emits `ProjectionReport` for audit.

`agent.Result.CandidateRejected` distinguishes an action-space refusal from a
real tool failure. Completion and evidence ignore it; the model sees it.

### 8.2 TUI

At each turn boundary the TUI:

1. resolves the contract/authority and pre-run snapshot;
2. begins/resumes the durable task;
3. installs the fresh run budget;
4. builds the shape and projector from the task's evidence;
5. runs the loop;
6. checkpoints evidence and the last projection audit through the task
   lifecycle transition.

`/audit` renders the last projection. A read-only task with no evidence still
offers only the relevant surface (for example, read/ls/grep/glob/todo).

### 8.3 Headless and REPL

`runHeadlessTurn` installs a run-local projector with the same shape rules.
`OHGO_DEBUG_CONTEXT=1` prints each projection report to stderr. The plain REPL
does not claim durable evidence and says where the audit is available.

### 8.4 Subagents

`SubagentSpec.Project` lets the spawner give a child its own shape and evidence,
derived from the assignment. The parent's conversation is never inherited.
The inherited security gate remains the only authority; the child's depth and
budget bounds are unchanged.

## 9. Observability

Every projection report answers:

| Question | Field |
|---|---|
| Why did the model get this context? | `Messages`, `KeptToolResults`, `StateBlockBytes` |
| Why was this context removed? | `Elisions[].Policy`, `RetentionPolicy` |
| Why was this tool available? | `ToolNames` |
| Why was this tool withheld? | `Excluded[].Reason` |
| Why did exploration stop? | `Sufficiency`, `Stage`, candidate notices |
| What evidence supported progress? | `Evidence`, state block |
| Why did the runtime consider it complete? | Completion remains `agent.EvaluateTaskCompletion`; the shape records the contract, not the verdict |

The audit is available through `/audit` in the TUI and through
`OHGO_DEBUG_CONTEXT=1` in headless runs. The benchmark JSON also records every
projection report.

## 10. Authority, security, and Phase 8 boundaries

Unchanged:

- the loop's `Authorize` sequence is still the only admission path;
- candidate selection cannot add a tool or widen authority;
- a denied call still never executes;
- a denied call now carries the refusing `security.Decision` for audit (the
  documented contract was previously not reflected in `ToolOutcome`);
- external knowledge is still untrusted, bounded, provenance-bearing, and
  unable to authorize, verify, or complete;
- budget, cancellation, deadline, and provenance checks are untouched;
- completion is still evidence-based and owned by the agent evaluator.

Projection can make the action space smaller. It cannot make the authority
larger.

## 11. Model independence

No production code branches on a model id, provider name, or model family. The
shape is derived from the user's contract, the authority, tool metadata, and a
cheap repository probe. A small model gets a smaller context and a narrower
action space; a large model gets the same contract and the same safety
boundary. The benchmark can compare models through `-live-model` without
changing runtime configuration.

## 12. Deliberate non-choices

- **No generic transcript summarizer.** Evidence is retained by category and
  provenance, not by asking a model to summarize the repository.
- **No per-task file lists.** The shape names knowledge types and change scope,
  not paths to read.
- **No prompt-only authority.** Every narrowed tool still passes the security
  boundary.
- **No model-specific workflow compensation.** Differences in model behaviour
  are measured, not hidden.
- **No completion shortcut.** Candidate rejection is not success; sufficiency
  is not a verdict.

## 13. Residual risks and trade-offs

1. **Large exact observations can be excerpted.** A 100 KB full-file read is
   bounded to a 4 KiB working-set window on the next request. Diagnostics,
   conflicts, small source files, and the exact working set are preserved; the
   model can re-read with `offset`/`limit`. This is the main retention
   trade-off.
2. **Deterministic scope vocabulary can misclassify novel phrasing.** A task
   that is informational by wording but actually needs a command will have the
   execution surface withheld. The conservative direction is safe (less
   authority, not more), but it can reduce flexibility. The shape is rebuilt
   on the next turn if the user clarifies.
3. **Fixed tool schemas still dominate some tasks.** Refactoring and
   relationship-heavy work legitimately needs most tools; Phase 9 reduces that
   cost less (H: −6.7%). Further gains would require tool-definition
   compression or staged schema disclosure, which were not attempted.
4. **Live model/provider data is not measured here.** The harness supports it,
   but this environment did not run a paid provider comparison.
5. **Evidence is bounded.** A very large investigation can evict low-value
   facts. The agent can re-acquire them; nothing was silently converted into
   authority or completion.
6. **A dual-use shell in a mutation-forbidden task.** The shape keeps `bash`
   for an execution objective; read-only enforcement remains the security
   gate's job. Candidate selection is not a security boundary.
