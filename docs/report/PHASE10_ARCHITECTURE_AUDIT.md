# Phase 10 — Final Architecture Audit

> **Status:** Phase 10 audit, based on commit `7d004c8` plus the Phase 10
> documentation/tests in this tree.
>
> **Verdict of this document:** the runtime has one authority plane, one
> completion plane, one evidence owner, and one projection seam. A set of
> concrete defects was found and fixed with regression tests. No duplicate
> control plane or hidden execution path was found.
>
> **HOLD conditions:** none remain in the audited code. One deliberate
> asymmetry is recorded: CONTROL (projection off) has no sufficiency report and
> therefore no evidence veto; COGNITIVE does. This is a measured treatment
> effect, not a hidden authority difference.

---

## 1. Runtime flow

```text
user text
  │
  ├─ workspace.Classify / RouteTurn ────────────── authority + handler
  ├─ workspace.BuildIntentContract ─────────────── completion contract
  │
  ├─ task.Controller.Begin ─────────────────────── durable task, contract, snapshot
  │
  ├─ cognition.BuildShape ──────────────────────── TaskShape
  │     authority, mutation scope, required evidence,
  │     verification, completion conditions, external need
  │
  ├─ agent.Loop.Run
  │     ├─ cognition.Projector.Project (COGNITIVE only)
  │     │     ├─ EvidenceState.ObserveTurns
  │     │     ├─ SummarizeExecution
  │     │     ├─ EvaluateSufficiency
  │     │     ├─ Candidates
  │     │     ├─ projectMessages (tool results only)
  │     │     └─ RenderState (bounded briefing)
  │     │
  │     ├─ provider request (projected messages + narrowed tools)
  │     ├─ model proposal (text and/or tool calls)
  │     │
  │     ├─ loop.dispatch
  │     │     ├─ tool lookup (loop-owned instance)
  │     │     ├─ schema validation
  │     │     ├─ BeforeToolCall (host hook)
  │     │     ├─ Authorize.Evaluate ────────────── security.Decision
  │     │     │     AUTO  ──────────────────────── execute
  │     │     │     APPROVE ─→ Approve ─→ confirm ─ execute / deny
  │     │     │     DENY  ──────────────────────── no execution
  │     │     ├─ budget admission
  │     │     ├─ tool.Execute (Context.ReadOnly, deadline, budget)
  │     │     └─ result normalization
  │     │
  │     ├─ transcript append (full, never projected in place)
  │     └─ repeat until clean stop, budget/deadline, cancellation, or failure
  │
  ├─ cognition.SummarizeExecution / EvaluateSufficiency
  ├─ agent.EvaluateTaskCompletion
  │     ├─ run-shape and pending-failure rules
  │     ├─ target/structural evidence
  │     ├─ behavioral verification scopes
  │     ├─ bounded semantic alignment (can only downgrade)
  │     └─ optional cognitive EvidenceReview veto
  │
  └─ task/run verdict: COMPLETED / NEEDS_REVIEW / PAUSED / FAILED / CANCELLED
```

The important direction is unchanged:

```text
LLM proposes → Ohgo authorizes → tools execute → evidence updates →
verification proves → runtime completes
```

The model never owns a state transition. A projector never authorizes. A tool
never authorizes itself. Completion never reads model prose as proof.

---

## 2. Boundary ownership

| Boundary | State owner | May mutate | Information crossing | Deliberately excluded |
|---|---|---|---|---|
| User text → intent | `workspace` | nobody (pure) | text, active workspace | model state |
| Intent → contract | `workspace.BuildIntentContract` | nobody (pure) | objective, targets, verification | model output |
| Contract → task | `task.Controller` | controller | contract, pre-run snapshot, budget ledger | transcript replay |
| Contract → shape | `cognition.BuildShape` | nobody (value) | objective, authority, facts, snapshot, budget | tool results, model text |
| Turns → evidence | `cognition.EvidenceState` | projector goroutine | tool outcomes, details, args | model narration |
| Evidence + shape → projection | `cognition.Projector` | projector (working view only) | messages view, tool set, state block | authority decisions, security policy, budget internals |
| Projection → request | `agent.Loop` | loop request only | messages, system blocks, tool specs | live transcript mutation |
| Proposal → authorization | `securityGate` / `security.Policy` | nobody (decision value) | capability, scope, risk, mutation flag, operation id | model-provided capability/risk |
| Authorization → execution | `agent.Loop.execute` | loop | decision, args, budget, deadline, read-only flag | a second execution path |
| Execution → result | tool | tool | text, details, `Mutates`/`Executes`/`Verifies` | model claims |
| Result → evidence | projector | evidence state | normalized outcome | candidate refusals, denials |
| Evidence → completion | `agent.EvaluateTaskCompletion` | nobody (value) | turns, contract, snapshot, resolver, optional review | projection bloat |
| Completion → task/run | task controller / headless outcome | one transition | state + reason | model stop reason |

### 2.1 Who may mutate what

- **Task state:** `task.Controller` only. The model cannot call a task
  transition; the UI/host maps evidence to a transition.
- **Authority:** `securityGate.Evaluate`/`Approve` decisions and the workspace
  owner (`authorityGate` / explicit user switch). A projection and a task shape
  can only describe/narrow, never grant.
- **Evidence:** `EvidenceState.ObserveOutcome` via the projector. Tools do not
  write evidence; the runtime observes their normalized outcomes.
- **Projection:** the loop holds the live transcript and full tool set; only the
  per-request view is replaced.
- **Completion:** `agent.EvaluateTaskCompletion` (and its budget variant). A
  model stop is only a turn boundary.

### 2.2 What is intentionally excluded

- The projector never sees `security.Decision`, the policy, or budget
  internals. It cannot authorize and cannot turn a refusal into permission.
- The model never sees authority labels beyond the bounded state block and
  workspace prompt; the actual admission decision is made after the proposal.
- Completion never uses model confidence, narration, or a "done" sentence as
  evidence. Informational completion still proves only that an answer exists;
  quality is a separate measurement (see §8).
- External knowledge is recorded with provenance and an untrusted marker; it is
  never target-state evidence, authority, or completion proof.

---

## 3. Authority audit

### 3.1 The one admission path

`agent.Loop.execute` is the only path from a model tool call to
`Tool.Execute`:

1. resolve the tool from the **loop-owned** constructed set;
2. validate the JSON schema;
3. run `BeforeToolCall` (a host hook; can block/rewrite, never grant);
4. re-check cancellation;
5. `Authorize.Evaluate` (production path);
6. on `APPROVE`, `Authorize.Approve` and a fresh `Evaluate` confirmation;
7. re-check cancellation;
8. budget admission;
9. execute with `Context.ReadOnly` derived from the decision;
10. normalize the result.

The legacy `Admit`/`Approve` path exists only when no authorizer is installed.
Production installs `securityGate`. The Phase 10 live benchmark installs
`benchmarkGate`, a benchmark-only adapter over the same `security.Policy` and
workspace permission table; it is not a second production control plane.

### 3.2 Projection cannot grant authority

- A projector may only return a subset of the loop's tools.
- `intersectTools` now resolves every accepted name to the **loop's own tool
  instance**, not the projector's value. Before this fix, a projector could
  return a different tool under a known name; the loop executed the fake.
  Regression: `TestProjectionCannotSubstituteAToolWithTheSameName`.
- The report's offered-tool names are now always filled from the actual
  post-intersection set, not the projector's claim.
- A withheld tool called by name is a `CandidateRejected` result: no execution,
  no evidence, no pending failure.

### 3.3 Candidate selection cannot bypass authorization

A candidate is only visible. Every call still crosses `Authorize`. A candidate
selection that accidentally exposes a tool cannot widen workspace authority; a
projection that hides a tool cannot make a task impossible if the tool was the
only capability (the loop reports the refusal and the model can adapt).

### 3.4 Evidence and external knowledge cannot grant authority

- A known file, target, search result, or web page never changes the decision
  inputs. `securityRequest` derives capability/scope/risk from the tool and
  trusted arguments only.
- External evidence is `RetentionExternal`, marked `UNTRUSTED`, and never
  satisfies a target-state or verification requirement.
- A model's text is not parsed into authority; only a structured tool call is
  proposed, and the gate decides.

### 3.5 Denials are control decisions, not failures

Phase 10 fixed a completion-truthfulness bug: a denied or human-rejected call
was counted as a pending tool failure. It never executed, so it is not a failed
action. `ToolOutcome.Refused` now covers both `CandidateRejected` and
`Authorization.Outcome == DENY`; completion and sufficiency ignore it for
pending failures, while evidence ignores it entirely. Regression:
`TestAuthorizationDenialIsNotACompletionFailure`,
`TestApprovalRejectionIsNotACompletionFailure`,
`TestEvidenceIgnoresRefusedCalls`, `TestSummarizeExecutionIgnoresRefusedCalls`.

---

## 4. Evidence audit

### 4.1 Evidence classes

| Retention | Produced by | Projection policy |
|---|---|---|
| exact | file reads, mutations | newest working set exact; older exact summarized when the exact budget is full |
| structural | ls, glob, symbols | bounded summary, provenance retained |
| derived | grep, structural queries | bounded summary |
| diagnostic | errors, command/check output | exact up to 8 KiB, then excerpt |
| external | web/search/MCP/browser | bounded, provenance and untrusted envelope retained |
| transient | background/unknown | bounded summary |

Evidence is capped at 256 items / 64 KiB; deterministic access and eviction.
Required, conflicting, diagnostic, and external evidence is retained longest.
Eviction can lose a fact; re-acquisition is possible and no authority or
completion claim depends on an evicted item.

### 4.2 Integrity rules

- `KNOWN` = observed directly by a tool.
- `DERIVED` = structural/query conclusion.
- `CONFLICTING` = same source range observed differently without a recorded
  runtime mutation.
- `UNKNOWN`/`REQUIRED` are requirement vocabulary; unmet requirements are
  rendered through sufficiency, not silently converted to `KNOWN`.
- A missing-file error records absence; a denial does not.
- `target_state:<path>` proves existence/state; `target_content:<path>` proves content or
  an exact-path structural coordinate. A directory listing alone cannot satisfy a
  content question.
- A model assertion is never evidence.

Phase 10 fixed a false-conflict defect: reading different windows of one large
file was treated as two disagreeing observations. Conflict is now limited to
identical windows (both complete, or the same start/end range). Regression:
`TestEvidenceDifferentReadWindowsAreNotAConflict`.

### 4.3 Reversibility

Projection never deletes the live transcript, the exact tool output, or the
durable evidence. It replaces a tool result only in the request view. The
summary names the source and retained size; the model can re-read. The exact
working-set budget is now actually enforced for older small results; before the
fix, a sequence of small old reads bypassed the 16 KiB budget. Regression:
`TestProjectionEnforcesExactWorkingSetBudget`.

---

## 5. Context reduction invariant

Formally:

```text
Projection(Messages) ⊆ Messages
Projection(Tools)    ⊆ Tools
```

and the runtime retains:

```text
Authority(projected request) = Authority(full run)
CompletionRequirements(projected request) = CompletionRequirements(full contract)
```

The state block was reordered so **contract and sufficiency come first, evidence
summaries last**. It now always carries constraints, mutation scope, verification,
completion conditions, sufficiency, available actions, and withheld reasons, even
after many tool calls. Evidence is the first section sacrificed when the block is
truncated. Regression:
`TestRenderStateKeepsContractWhenEvidenceIsTruncated`.

The model still sees the user objective in the live user message; the state block
is a briefing, not the only source of task identity.

---

## 6. Completion audit

### 6.1 States and mapping

| Runtime surface | Values | Notes |
|---|---|---|
| `agent.CompletionState` | `COMPLETED`, `NEEDS_REVIEW` | evidence-based; no "completed but unproven" value |
| `runState` (headless) | `COMPLETED`, `NEEDS_REVIEW`, `PAUSED`, `FAILED`, `CANCELLED` | terminal verdict + exit code |
| `task.Status` | `PENDING`, `RUNNING`, `PAUSED`, `NEEDS_REVIEW`, `COMPLETED`, `FAILED`, `CANCELLED` | durable lifecycle |
| `cognition.Sufficiency` | `SUFFICIENT`, `INSUFFICIENT`, `CONFLICTING`, `BLOCKED` | projection/evidence state |

`PROVEN` is not a separate completion enum because the only path to `COMPLETED`
is the evidence evaluator. A completion is either proven or not returned.
`BLOCKED` is a sufficiency verdict; it becomes `NEEDS_REVIEW` at the durable
surface with the blocking reason, and is visible in `/audit`. This mapping is a
documented contract, not a UX collapse of two runtime states.

### 6.2 Completion rules

A normal completion requires:

- at least one completed turn;
- no turn error;
- no outstanding tool-call batch;
- no unrecovered non-refusal tool failure;
- a requested mutation observed (if mutation objective);
- a requested execution observed (if execution objective and no verification);
- a non-empty final answer (informational);
- verification after the last potential mutation/execution;
- no mutation for a non-mutation objective;
- target/structural checks and unexpected-mutation checks;
- behavioral scope coverage;
- no downgrading semantic assessment;
- no `EvidenceReview` veto.

Resource stops (`EvaluateBudgetCompletion`) keep Phase 5's rule: a proven
mutation + verification remains authoritative even when the closing turn was
removed; the cognitive review is deliberately ignored on that path.

### 6.3 Phase 10 corrections

1. **Read-only/execution completion accepted a mutation.** Legacy
   `EvaluateCompletion` now rejects an observed mutation when the objective did
   not request one. Regression:
   `TestEvaluateCompletionRejectsUnexpectedMutation`.
2. **Informational completion required a build/test when the question named a
   Go file.** `AssessBehavior` now returns early for informational objectives.
   Regression: `TestInformationalPathTargetDoesNotRequireVerification`.
3. **A denied call shadowed completion as a tool failure.** Fixed via
   `ToolOutcome.Refused`; tests above.
4. **A model could answer while the projection still reported missing
   evidence.** `ProjectionReport.EvidenceReview` now supplies a completion veto;
   TUI, headless, subagent, and benchmark paths pass it. Regressions:
   `TestEvidenceReviewVetoesAnAnswerWithoutEvidence`,
   `TestHeadlessEvidenceReviewPreventsGuessedAnswer`,
   `TestTurnTaskOutcomeHonorsEvidenceReview`.
5. **A package-name question invented package targets** from "package declared"
   and "package name", which made an informational answer require structural
   evidence. Fixed with a generic-word stop list. Regression:
   `TestBuildIntentContractDoesNotInventPackagesFromQuestions`.

---

## 7. Failure and cancellation

| Condition | Runtime behavior | Truthful state |
|---|---|---|
| tool error | error result, pending failure unless recovered | `NEEDS_REVIEW` |
| malformed arguments | schema error result, model may retry | `NEEDS_REVIEW` if unresolved |
| provider open/drain error | loop returns turns + error | `FAILED` |
| cancellation | context error; no completion claim | `CANCELLED` / `PAUSED` in TUI |
| deadline | turns returned with nil error; caller checks deadline | `PAUSED` unless proven |
| budget exhaustion | admission error or tool `Exhausted` | `PAUSED` unless proven |
| denied/refused call | error result carrying decision; no execution | not a pending failure |
| candidate rejection | `CandidateRejected`; no execution | not a failure |
| conflicting evidence | `CONFLICTING`, `StageExplore` | completion review |
| out-of-scope mutation | `BLOCKED`/`StageBlocked`; further pure mutation withheld | `NEEDS_REVIEW` |
| failed verification | failed check remains evidence | `NEEDS_REVIEW` |
| partial/background execution | started is not finished | `NEEDS_REVIEW` |

Cancellation is re-checked before authorization, after authorization, and before
tool execution; a cancelled call does not consume a tool-call budget. The TUI's
terminal CAS prevents a late "completed" transition after cancellation.

---

## 8. Observability

- `/audit` in the TUI renders the last `ProjectionReport`: turn, shape,
  bytes before/after, elisions, offered/withheld tools, sufficiency, stage,
  evidence summary, retention policy.
- `OHGO_DEBUG_CONTEXT=1` prints the same audit for headless/stateless runs.
- `task.progress.json` stores a bounded audit string and durable evidence.
- The Phase 10 benchmark artifacts carry per-run requests, projection reports,
  outcomes, provider usage, completion status, evidence review, and answer facts.
- The runtime does not claim to explain the model's private reasoning; it
  explains what the model was offered, what it called, what the gate decided,
  what executed, what evidence changed, and why completion did or did not hold.

---

## 9. UX validation

The Phase 10 UI contract is unchanged: output first, truthful status, no
decorative state.

- The full-screen UI writes the transcript once in live mode and leaves scroll,
  selection, and copy to the terminal (`docs/report/ui/OUTPUT_FIRST.md`).
- `/audit` is a bounded explanation, not a dashboard; it reports the last
  projection's offered/withheld tools, elisions, sufficiency, and stage.
- The status surface distinguishes `COMPLETED`, `NEEDS_REVIEW`, `PAUSED`,
  `FAILED`, and `CANCELLED`; a reviewable turn is never painted as success.
- The approval prompt shows capability, normalized scope, risk, the rule/reason,
  and a trusted mutation preview.
- The runtime does not render confidence percentages, emoji state, or fake
  progress. Streaming text and tool progress are real events from the provider
  and the running tool.
- A withheld candidate is explained to the model in the result text; the user
  sees the same decision in `/audit`.

The one visible Phase 10 behavioral change is intentional: an evidence-free
repository answer now ends in `NEEDS_REVIEW` instead of a polished
`COMPLETED`. That is more honest, not less finished.

---

## 10. Concrete defects found and fixed in Phase 10

| # | Defect | Severity | Fix | Regression |
|---|---|---|---|---|
| 1 | Projector could substitute a fabricated tool under a known name | security/authority | resolve to loop-owned instance; always assign filtered set | `TestProjectionCannotSubstituteAToolWithTheSameName` |
| 2 | Exact working-set budget bypassed by old small results | context integrity | summarize any old exact result not in the budgeted set | `TestProjectionEnforcesExactWorkingSetBudget` |
| 3 | Different read windows marked CONFLICTING | evidence integrity | same-window comparison | `TestEvidenceDifferentReadWindowsAreNotAConflict` |
| 4 | Denied/rejected calls counted as pending tool failures | completion integrity | `ToolOutcome.Refused`; ignore for failures/evidence | denial/rejection tests |
| 5 | Legacy completion accepted a mutation in a non-mutation objective | completion integrity | explicit unexpected-mutation rule | `TestEvaluateCompletionRejectsUnexpectedMutation` |
| 6 | Informational Go-path question required a build/test | completion integrity | informational early return in `AssessBehavior` | `TestInformationalPathTargetDoesNotRequireVerification` |
| 7 | Headless shape named BUILD while the routed gate was ASK/FUZZ | observability/authority semantics | pass `route.Handler` into shape construction | `TestHeadlessShapeUsesRoutedAuthority` |
| 8 | `readOnly` subagent was refused instead of restricted | delegation correctness | filter child tool set through `agent.ReadOnlyToolNames` | `TestReadOnlyToolSetKeepsOnlyObservationTools` |
| 9 | State block dropped contract/sufficiency under truncation | context invariant | critical-first rendering; evidence last | `TestRenderStateKeepsContractWhenEvidenceIsTruncated` |
| 10 | No-mutation/no-external constraints were model-facing only | authority/task scope | shape narrows mutation and external need; execution stays available for read-only checks | `TestBuildShapeHonorsExplicitNoMutationAndNoExternal` |
| 11 | Sufficiency did not block a mutation the shape forbade | evidence/task integrity | `!Mutation.Allowed && Mutated` → BLOCKED | `TestSufficiencyBlocksMutationTheShapeDidNotPermit` |
| 12 | Informational answer could ignore missing evidence | completion truthfulness | `ProjectionReport.EvidenceReview` veto across TUI/headless/subagent/bench | evidence-review tests |
| 13 | Question prose invented package targets | evidence sufficiency | package stop-word set | `TestBuildIntentContractDoesNotInventPackagesFromQuestions` |
| 14 | A directory listing satisfied a question about file content | evidence sufficiency | `target_content` requirement; a read or an exact-path structural query satisfies it | `TestInformationalContentQuestionNeedsAFileRead`, `TestStructuralQuerySatisfiesCoordinatesForANamedPath` |

Items 1–14 are fixed. There is no unresolved critical defect in the audited
code.

---

## 11. Deliberate limits and non-goals

These are **not** hidden defects and were not implemented in Phase 10:

1. **Informational quality is not semantically proven.** Completion proves an
   answer exists and, in COGNITIVE mode, that the runtime's evidence was
   sufficient. It does not prove the answer is correct. The benchmark records
   answer facts separately; a human/LLM judge would be a new measurement layer.
2. **Per-path mutation scope is a completion boundary, not a pre-execution
   authority gate.** BUILD authority permits a workspace write; an out-of-scope
   write is detected, blocks further pure mutation, and returns NEEDS_REVIEW.
   Moving path scope into the security gate would be a new authority model.
   The same applies to an explicit `no_mutation` constraint: it withholds pure
   mutation tools but does not make an execution-capable shell read-only in
   BUILD; a shell mutation is completion-reviewed. Hiding execution entirely
   would make a read-only check (for example a FUZZ test run) impossible.
3. **External evidence is not verified for truth.** It is bounded, provenance-
   bearing, and untrusted. It cannot authorize or complete.
4. **A trusted projector can replace messages.** The built-in projector only
   rewrites tool results. The loop protects tool identity and the level of
   authority, not a hostile host process. A host that installs an arbitrary
   projector already has the process.
5. **No free remote tier was available for every class.** Provider failures are
   recorded in the effectiveness report; no result was fabricated.

---

## 12. Audit conclusion

The architecture satisfies the Phase 10 control contract:

- one authority plane (`security.Policy` + workspace gate);
- one execution path (`agent.Loop.execute` → tool);
- one evidence owner (`cognition.EvidenceState`, updated by observation);
- one completion owner (`agent.EvaluateTaskCompletion`);
- one projection seam, with loop-owned narrowing and no authority widening;
- truthful failure, cancellation, and resource-stop states;
- bounded, inspectable, reversible context reduction.

No Phase 10 HOLD condition was found after the fixes above.
