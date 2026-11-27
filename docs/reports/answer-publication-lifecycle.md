# Answer Publication Lifecycle — Root-Cause Audit

> **Scope.** The investigation/evidence/answer lifecycle observed in a
> project-overview run: provider-local confidence, structural coverage,
> answer candidates, claim review, correction, `conclude`, completion, and the
> TUI projection.
> **Status of this document.** Phase 0 audit and implementation contract. The
> verification section records the checks actually executed after the change.
> **Canonical behaviour.** `docs/architecture/UNIFIED_INTELLIGENCE.md` §12.6–12.7
> owns investigation closure and the response transaction. This report records
> the defect and the evidence for the fix; it does not create a second
> behavioural contract.
> **Follow-up.** The provisional-preview presentation decision in §10/§13 was
> later reversed: §12.7 now buffers candidate text and never paints it before
> the publication decision. The completion-authority finding and its checks
> remain valid; the preview discussion is historical.

## 1. Executive summary

The runtime already has one publication authority: `agent.AnswerDisposition`.
The TUI, the plain-output surface, and the session store consume it through
`agent.TranscriptTurn` and may not present a narrated, superseded, or rejected
candidate as an answer.

Before this change, completion did not consume it. `agent.EvaluateTaskCompletion`
read `turns[len(turns)-1].Assistant.Text()` directly. The same raw-text pattern
existed in the durable task's `Answered` evidence and in the delegated-child
result. A run that ended after a candidate was withheld could therefore be
reported `COMPLETED` / `PROVEN` even though no host was allowed to publish an
answer.

That was the systemic defect. It is not a renderer bug and not a model-quality
judgement: the completion authority re-derives "an answer exists" from prose
the publication gate already refused.

The streamed provisional preview, the withdrawal on supersession, and the
bounded repair reads are separate facts. The preview is intentional and
documented; it is not the root cause. The repair reads are bounded by the
existing correction allowance and do not by themselves make a candidate
authoritative.

## 2. User-visible symptoms

These are observations, not causes:

- a project-overview run reports `structural: UNPARSED` and many pending files
  while the `investigate` provider reports `provider_sufficient` at its own
  confidence target;
- a streamed answer appears as a draft, is withdrawn or superseded, and a
  later answer becomes the visible final answer;
- the model performs further reads/greps after a `conclude` call with
  `status: FINAL`;
- the transcript, the terminal footer, and the durable task can describe the
  same run differently when a candidate is withheld;
- a persisted or logged final answer can look correct while the live surface
  temporarily showed non-authoritative drafts.

## 3. Reproduction

The smallest deterministic reproduction does not need a model, a network, or a
benchmark repository:

```text
loop := agent.New(Config{
    Stream: one text response,
    AnswerReview: always returns "the answer is not supported",
    Tools: one read tool,
})
turns, _ := loop.Run(...)

turns[0].Answer == AnswerRejected          // the publication gate refused it
turns[0].Assistant.Text() != ""            // the raw candidate is retained

agent.ClassifyRun(nil, CompletionInput{
    Turns: turns,
    Contract: informational("analyze this project"),
}, false)
```

**Before the fix:** `ClassifyRun` returns `COMPLETED`.
**After the fix:** `ClassifyRun` returns `NEEDS_REVIEW` and the completion
reason names the unpublished answer; the rejected draft's claim issues remain
available to the durable task.

The same path is reachable at a budget stop after a correction superseded a
candidate (`EvaluateBudgetCompletion`), and at a terminal surface where the
model's prose was withheld as narration.

Reproduction at the lowest layer:

```text
EvaluateTaskCompletion(CompletionInput{
    Turns: []Turn{{Answer: AnswerRejected, Assistant: "The project is built with HTMX."}},
    Contract: informational("Explain this project."),
})
```

**Before the fix:** `COMPLETED` with no issues.
**After the fix:** `NEEDS_REVIEW` with the control-plane review reason and the
draft's bounded issues.

## 4. Relevant architecture

The only concepts needed to understand the defect:

| Responsibility | Current owner | Entry point | State mutated | Consumers |
|---|---|---|---|---|
| Classify one turn's assistant prose | `internal/agent` | `Loop.acceptAnswer` | `Turn.Answer`, `Turn.AnswerReview`, `Turn.Draft` | `OnTurn`, TUI, plain output, session persistence, completion |
| Withhold non-published prose from hosts | `internal/agent` | `TranscriptTurn`, `RejectedAnswer`, `AnswerText` (new) | projected `Turn` | TUI, plain output, session store |
| Decide task completion | `internal/agent` | `EvaluateTaskCompletion` / `ClassifyRun` | `Completion` | TUI task boundary, headless runner, subagent |
| Project the authoritative answer | `internal/tui` | `AssistantDelta` → `AssistantReconcile` | transcript entries | the user |
| Record durable answer evidence | `internal/tui` / `internal/continuity/task` | `CompletionEvidence.Answered` | `CompletionProof` | task status, `/task`, replay |

The critical type is already correct. Its documentation says:

```text
Only AnswerCommitted may be presented as the task's answer,
and only AnswerRejected may be presented as a terminal refusal.
```

`AnswerNarration` and `AnswerSuperseded` are explicitly internal. The defect was
that three consumers of "the task's answer" did not read this field.

## 5. Actual control/data flow

Normal committed path (correct):

```text
provider stream
  → Loop.drain
  → Turn.Assistant
  → acceptAnswer
      → AnswerReview / FinalizeAnswer
      → AnswerCommitted
  → OnTurn
  → TUI AssistantReconcile(AnswerCommitted)
  → EvaluateTaskCompletion (raw text happens to equal the committed text)
  → COMPLETED
```

Withheld path (broken before the fix):

```text
provider stream
  → Loop.drain
  → Turn.Assistant (raw candidate)
  → acceptAnswer
      → AnswerReview refuses / correction supersedes
      → AnswerRejected or AnswerSuperseded
  → OnTurn
  → TUI AssistantReconcile(AnswerRejected/Superseded)
      → transcript withholds the prose
  → EvaluateTaskCompletion
      → last.Assistant.Text()  ← bypasses the disposition
      → COMPLETED
  → task boundary records Answered=true and PROVEN
```

The two consumers of the same run disagree at the same boundary:

```text
presentation: no answer              completion: answer exists
```

`internal/agent/subagent.go` had the same shape: it scanned raw assistant text
and returned the last non-empty value as the child's result. A child whose
answer was withheld could still hand its refused prose to the parent. That
consumer now uses the same publication projection.

## 6. Structural and confidence audit

The reported `structural: UNPARSED` / `provider_sufficient` combination is
**not** a completion predicate and was not the cause of the false completion.

| Question | Answer from source |
|---|---|
| Who decides investigation should continue? | The control plane's projection. `EvaluateSufficiency` returns the stage and missing requirements; the loop narrows the offered tools from that report. |
| What does `confidence` measure? | The `investigate` planner's provider-local correlation estimate. `investigate.StatusSufficient` is documented as "not task sufficiency"; the tool prints `provider_sufficient` and says the control plane's facets still gate completion. |
| What does `coverage` measure? | The provider's own observed coverage, not repository completeness. It is not read by completion. |
| What does "confidence target reached" mean? | The provider plan has no further step worth its cost. It is a stop reason inside one capability call. |
| Can confidence reach its target while required evidence is missing? | Yes, by design. The control plane's `RequiredKnowledge` still reports the missing requirement, and the projection stays open. |
| What is the evidence contract for a broad overview? | `repository_overview` plus, for a broad account, `repository_survey`: source content, and for a measured multi-file source project the entry point plus at least one implementation file. Structural inventory alone is not a substitute. |
| Is architecture a required facet? | Only for an explicitly relationship-shaped request. The facet policy is narrow: a broad "explain the architecture" answer is an overview, not a mandatory edge traversal. |
| What happens when structural analysis is incomplete? | The structural result discloses `UNPARSED`/`PARTIAL`; source access remains available. The runtime does not treat a zero parsed count as absence. |
| Does the planner retry/activate a fallback? | The `investigate` engine's structural router and planner choose providers; the control plane can still require source content through `repository_survey`. It does not force a full parse for an overview. |
| When is the investigation considered sufficient? | When every declared `RequiredKnowledge` key is satisfied, required facets are satisfied, no mandatory requirement is unresolved, and no capability block exists. |
| Is "sufficient to answer" distinct from "structurally complete"? | Yes. Structural completeness is one provenance axis; sufficiency is the requirement verdict. |
| Can the system answer safely with incomplete structural coverage? | Yes, when the intent's content requirements are satisfied and the answer's claims are attributed to the evidence actually read. |
| If not, where should completion be prevented? | In `EvaluateSufficiency` / `Candidates`, by a missing requirement. Not by a global parse-complete condition. |

The observed run is consistent with this contract: the `investigate` result was
provider-local, the control plane still required source content, and the final
answer attributed its architecture statements to documentation and to the
source files it read. Forcing a full parse would add cost without changing the
answer's evidence contract.

**Conclusion:** the UNPARSED/confidence symptom is a diagnostic of two
different confidence vocabularies, not a control-plane failure. The fix must
not make structural completeness a global completion gate.

## 7. Evidence ledger

| Finding | Classification | Evidence | Location |
|---|---|---|---|
| `AnswerDisposition` is documented as the single authority for presenting an answer | OBSERVED | Type and field comments | `internal/agent/loop.go:60-96`, `:121-130` |
| The publication gate sets `AnswerRejected`/`AnswerSuperseded`/`AnswerCommitted` before a turn reaches a host | OBSERVED | `acceptAnswer` closures | `internal/agent/loop.go:1295-1487` |
| Host projection withholds narration, superseded, and unclassified prose | OBSERVED | `TranscriptTurn` strips text blocks | `internal/agent/transcript.go:95-125` |
| Completion read raw final-turn text for the answer-presence rule (pre-fix) | OBSERVED | `strings.TrimSpace(last.Assistant.Text())`; now `last.AnswerText()` | `internal/agent/completion.go` (pre-fix `:620`, after `:665`) |
| Completion passed the same raw text to grounding, verification-claim, provenance, and inventory audits (pre-fix) | OBSERVED | `last.Assistant.Text()`; now the single `answer` projection | `internal/agent/completion.go` (pre-fix `:779-801`, after `:832-854`) |
| A rejected turn retains its raw candidate text | OBSERVED | `withhold` sets the disposition but does not clear `Assistant` | `internal/agent/loop.go:1306-1315` |
| A scripted loop with an always-refusing review produces `AnswerRejected` and `ClassifyRun(nil, ...)` returns `COMPLETED` before the fix | OBSERVED | deterministic reproduction | `internal/agent/answer_lifecycle_test.go` (new regression) |
| Completion can therefore certify text no host may publish | INFERRED | raw-text consumer plus withheld disposition | `completion.go`, `transcript.go` |
| The TUI task boundary derived `Answered` from the same raw text (pre-fix) | OBSERVED | `strings.TrimSpace(turns[len(turns)-1].Assistant.Text())`; now `AnswerText()` | `internal/tui/app.go` (pre-fix `:6848`, after `:6852`) |
| A delegated child returned the last raw non-empty assistant text as its result (pre-fix) | OBSERVED | `turn.Assistant.Text()` scan; now `AnswerText()` | `internal/agent/subagent.go:307-315` |
| The TUI streams a labelled provisional preview before commit | OBSERVED | `appendAssistant` sets `Provisional`; `assistantDisplay` labels it | `internal/tui/model.go:3627-3655`, `view.go:2120-2145` |
| The preview is intentional and tested, not an accidental leak | OBSERVED | `AssistantReconcile` contract; runtime lifecycle test; architecture doc | `internal/tui/model.go:4014-4047`, `internal/tui/answer_candidate_runtime_test.go`, `docs/architecture/UNIFIED_INTELLIGENCE.md` §12.7 |
| `conclude` with `status: FINAL` records a control observation, not the task's answer transaction | OBSERVED | tool result text says synthesis is a separate step; `Run` does not terminate on the call | `internal/execution/tools/conclude.go:130-150` |
| The correction window can span tool turns and suppresses `AnswerEligible` while `Correcting` | OBSERVED | projection branch | `internal/control/cognition/project.go:470-495` |
| The observed run performed grep/read after `conclude FINAL` because a claim repair window was active | INFERRED | session turns and thinking show the review directive; code shows tools reopened while correcting | `.ohgo` session record, `project.go:470-495` |
| The shorthand claims `Rejected`/`Narration` are not declared identifiers; the audit correctly refused them | OBSERVED | `codeTokenObserved` requires a code boundary; the declarations are `AnswerRejected`/`AnswerNarration` | `internal/agent/entity_claims.go:185-230` |
| The repair action "read the declaration" cannot repair a spelling mismatch, so repeated reads were bounded but unproductive | INFERRED | model read the same region; the repair allowance was spent; no code path could change the token match | `internal/agent/entity_claims.go:96-110`, `internal/agent/review.go:405-480` |
| The exact TUI frames of the benchmark session are available | UNVERIFIED | session DB stores turns, not rendered frames | — |

## 8. Root cause

**The publication disposition was not the single input to answer-presence
decisions.**

`Turn.Answer` decides whether prose may be presented as an answer. Three
downstream authorities re-derived the same decision from `Turn.Assistant.Text()`:

1. task completion (`EvaluateTaskCompletion`),
2. the durable task's `Answered` evidence (TUI `CompletionEvidence`),
3. a delegated child's result (`RunSubagent`).

The existing safeguard — `TranscriptTurn` — protects the transcript, not the
completion verdict. A run that ends with a withheld candidate therefore has a
truthful transcript and an untruthful completion verdict. The behavior repeats
on every path that ends after a rejection, a supersession, or narration,
including budget stops and terminal surfaces.

## 9. Contributing causes

- **The zero-value ambiguity.** `AnswerUnclassified` means "no publication
  decision touched this turn"; completion inputs built by hosts and tests rely
  on that. The ambiguity made it easy for new consumers to keep reading raw
  text instead of adding a projection. The fix keeps `AnswerUnclassified`
  admissible for legacy/host-built turns and excludes only the dispositions
  that represent a withheld candidate.
- **No shared answer-text accessor.** Every consumer called
  `Assistant.Text()` independently. There was no small function that encoded
  "which disposition carries the answer".
- **Correction across tool turns.** An answer correction reopens the tool
  surface and remains active across tool-call turns. This is legitimate for
  evidence repair, but it lets a `conclude` call appear terminal to the model
  while the answer correction is still unresolved. The runtime bounds the
  reads; it does not make the candidate authoritative.
- **Claim repair instructions can name an unreachable action.** A claim that
  is a shorthand for an observed declaration is refused correctly, but the
  action "read the declaration" does not tell the model to use the declared
  spelling. This causes bounded but unproductive reads. It is a contributing
  efficiency issue, not the completion invariant.

## 10. Why previous fixes were insufficient

- #125 introduced `AnswerDisposition` and migrated the transcript and plain
  output. It did not migrate the completion evaluator or the durable task's
  answer evidence.
- #126 reintroduced the labelled provisional preview. That was an intentional
  presentation decision; the tests and architecture doc now define it. It did
  not address the completion authority.
- Claim-level repair, bounded correction budgets, and the TUI tombstone all
  made the *presentation* truthful. A truthful withdrawal still left the
  completion evaluator reading the refused prose.
- The structural evidence work made provider confidence explicitly
  provider-local. That part is correct; the answer-lifecycle defect is
  independent of structural coverage.

## 11. Existing abstraction ownership

`internal/agent.Turn.Answer` owns the publication decision. The completion
authority is `EvaluateTaskCompletion`; the durable task consumes its verdict
through `ClassifyRun`. The fix belongs in that existing boundary:

- add one `Turn.AnswerText` projection that encodes the disposition rule;
- make completion, the TUI's `Answered` evidence, and the subagent result use
  it;
- keep `AnswerDraft` and `AnswerDisposition` as they are.

No new state machine, draft manager, event bus, or answer ledger is introduced.

## 12. Minimal architectural fix

1. Add `Turn.AnswerText()`: returns `Assistant.Text()` only for
   `AnswerCommitted` and `AnswerUnclassified` (the host-built/legacy zero
   value). Returns empty for `AnswerNarration`, `AnswerSuperseded`, and
   `AnswerRejected`.
2. `EvaluateTaskCompletion` reads that projection once. The answer-presence,
   deferred-plan, grounding, verification-claim, provenance, and inventory
   audits use the same text. A rejected turn's completion verdict carries the
   draft's existing review issues instead of losing them.
3. The TUI's `CompletionEvidence.Answered` uses the same projection.
4. `RunSubagent` uses the same projection for the child result.
5. Add regression tests at each seam and update the canonical response-
   transaction section and the changelog.

The fix does not change the normal committed path, the preview, the repair
budget, structural activation, or the completion evidence rules for mutation
and execution.

## 13. Alternatives rejected

| Alternative | Why rejected |
|---|---|
| Remove the streamed preview | Symptom patch. The preview is a labelled, documented presentation contract; the completion bug exists even with no preview. Removing it would not stop a rejected turn from completing. |
| Return immediately on `conclude FINAL` | `conclude` records an investigation observation, not the answer transaction. An early return would drop answer synthesis, claim review, persistence, and the correction the host authorized. |
| Raise `DefaultCorrectionToolCalls` | Does not change the completion invariant and turns a bounded repair into a second investigation. |
| Make `UNPARSED` block every overview | Forces full parsing for an intent whose evidence contract is satisfied by source content. Cost without a correctness gain. |
| Filter non-committed turns out of `CompletionInput` | Loses the exact review reasons and claim issues the durable task needs. The completion authority must still see the turn, just not treat its prose as the answer. |
| Add an `AnswerManager`/`DraftManager` | Duplicates `AnswerDraft`, `AnswerDisposition`, and `AnswerLedger`; the existing owner only needed a projection accessor. |
| Re-run completion on the TUI's committed text | Creates a second completion source of truth and can disagree with the headless runner. The shared evaluator must own the decision. |

## 14. Regression risks

- **Legacy/host-built turns:** they carry `AnswerUnclassified`; the accessor
  must keep their text admissible or completion tests and embedders break.
- **Mutation/execution completion:** the final turn is often a tool turn with
  narration. Excluding its prose from claim audits is correct, but the
  mutation/verification rules must still complete on observed evidence.
- **Rejected-answer issue preservation:** the durable task must keep the exact
  claims; the completion verdict must carry the draft's issues rather than a
  bare reason.
- **Subagent contract:** a child that produced no committed answer must return
  `ErrNoSubagentResult`, not refused prose.
- **TUI `Answered`:** a completion with a committed answer remains `true`; a
  completion reached through a withheld candidate is impossible after the fix.

## 15. Verification plan

- Add `TestCompletionRequiresPublishedAnswerText` (narration, superseded,
  rejected → not complete; committed and unclassified → complete).
- Add the loop + `ClassifyRun` regression to the existing refused-answer
  lifecycle test.
- Add a TUI task-boundary regression for a rejected turn.
- The delegated-child consumer uses the same accessor. Its current wiring does
  not install an `AnswerReview` on the child, so there is no reachable
  withheld-child fixture to add; the change is a consistency guard, not a new
  lifecycle.
- Run `go test ./internal/agent ./internal/tui ./cmd/ohgo` first, then the
  repository gates: `go build ./...`, `go test ./...`, `go vet ./...`,
  `go test -race ./...`, `gofmt -l .`, `golangci-lint run ./...`.
- Do not treat a green suite as proof of the benchmark. The deterministic
  reproduction is the proof of the invariant; a real benchmark run is
  corroboration only.

### Verification results

Executed after the final source edit:

| Check | Result |
|---|---|
| Pre-fix reproduction (`ClassifyRun` after an always-refusing review) | **OBSERVED** `COMPLETED` before the change; the new regression asserts `NEEDS_REVIEW` |
| `go test ./internal/agent ./internal/tui ./cmd/ohgo` | pass |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `gofmt -l .` | empty |
| `go test -p 2 ./...` | pass (all packages) |
| `go test -race -p 2 ./...` | pass (all packages) |
| `golangci-lint run ./...` | 0 issues |

The first full-suite attempt failed before compiling the changed packages
because the machine's disk was full (`ld: write() failed, errno=28`). After
`go clean -cache` freed 3.6 GB, the same suite and the race suite passed. That
was an environment failure, not a code failure.

The subagent consumer is updated to the same accessor but has no end-to-end
withheld-child fixture: `RunSubagent` does not propagate the parent's
`AnswerReview` to the child, so the child cannot currently reach
`AnswerRejected` through that path. The accessor keeps the result consistent if
that wiring changes.

## 16. Remaining uncertainty

- The real benchmark session did commit a final answer, so its recorded
  `COMPLETED` is not itself an instance of the false-completion path. The path
  is proven deterministically for a run that ends after a withheld candidate;
  whether that exact run ever stopped there is not established.
- The TUI frames of the session were not captured; the transcript evidence
  shows the withheld turns, not what was painted on each frame.
- The repair-action mismatch (`Rejected` vs `AnswerRejected`) is observed and
  bounded, but this change does not attempt to make the model's shorthand
  pass. The correct repair remains to use the declared spelling or remove the
  claim.
