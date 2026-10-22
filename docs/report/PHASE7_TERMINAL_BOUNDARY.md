# Phase 7 Terminal Boundary

- **Date:** 2026-10-22
- **Status:** proposed terminal contract; **not implemented**
- **Predecessor:** [`PHASE6_FINAL_SYNTHESIS.md`](PHASE6_FINAL_SYNTHESIS.md)
- **Purpose:** finish the current system, not continue the roadmap

## 1. Phase 7 Name

**Phase 7 — Final System Closure**

This is a closure phase, not a capability phase. The name is deliberately not
"Phase 7 — Advanced Repository Intelligence" or "Phase 7 — Verification
Platform." Either of those would be another expansion, and the audit found no
evidence that expansion is required to make the current design honest.

## 2. Objective

> Make every production execution path report and persist completion only when
> the existing Phase 4 contract is satisfied by observable evidence, while
> preserving the Phase 1–6 authority, budget, discovery, and graph contracts and
> adding no new repository-intelligence capability.

## 3. Hypothesis

The hypothesis is falsifiable by any one of the following counterexamples:

> After the Phase 7 fixes, no scripted or live-model run can produce
> `COMPLETED`, or a successful process exit, for:
>
> 1. a mutation objective with no observed change;
> 2. an execution objective with no executed check;
> 3. an informational objective with no answer;
> 4. a completed turn followed by a failed or timed-out background job treated
>    as a success;
> 5. a cancellation that occurs before the terminal state transition;
> 6. a resumed `NEEDS_REVIEW` task whose evidence belongs to a different
>    contract;
>
> and all Phase 1–6 suites, the Phase 4/5/6 scripted benchmarks, the API
> authority/budget invariants, and the bounded graph contract remain green.

If a counterexample survives, Phase 7 is **HOLD**, not PASS.

## 4. Required Fixes

The list is finite. Each fix uses existing seams; none requires a new subsystem.

### F1 — Execution objectives require execution evidence

**Problem.** `workspace.BuildIntentContract("run ls")` is an
`ObjectiveExecution` with no verification requirement. A clean stop with no
tool calls is `COMPLETED` (audit probe confirmed).

**Fix.** Extend the shared completion evidence scan with an `executed` fact set
by a successful result with `Result.Executes` (or a successful verification,
which is itself execution evidence). For `ObjectiveExecution`:

- if the contract has a verification requirement, existing behavior applies;
- if it has no verification requirement, at least one successful executed
  result is required;
- a read-only tool call is not execution evidence;
- a failed execution is not evidence.

Do not change the `ObjectiveMutation` or `ObjectiveInformational` rules except
as required by F2.

These rules are authoritative through `EvaluateTaskCompletion`, which is the
production completion path (TUI and writable children). The frozen
`EvaluateCompletion` is retained for the historical H0 measurement; do not
leave a production execution path using it for execution/informational
objectives. F3 routes the stateless paths through the Task evaluator, and
read-only children already fail closed on empty output.

**Acceptance.** A clean stop with no calls is `NEEDS_REVIEW`; a successful
`bash`/execution result can complete when the contract otherwise warrants it.

### F2 — Informational objectives require an answer

**Problem.** An empty final assistant message with no tool calls returns
`COMPLETED` for `"explain this"` (audit probe confirmed).

**Fix.** For `ObjectiveInformational`, require non-empty visible assistant text
in the final turn. Whitespace is not an answer. A final turn with only tool
calls is already review.

Do not require a particular wording or semantic relevance; that would be an LLM
judge and is out of scope. The deterministic rule is "the model produced an
answer at all."

**Acceptance.** Empty/whitespace final text is `NEEDS_REVIEW`; a non-empty
answer remains `COMPLETED` under the existing informational rules.

### F3 — Stateless paths report the same verdict and honest exit status

**Problem.** `cmd/ohgo/runOnce` and the plain REPL call `loop.Run` and stop
there. A one-shot run can exit `0` after unverified work, a refusal, a budget
stop, or a deadline stop.

**Fix.** Add one small outcome helper used by both stateless paths:

1. Build the `IntentContract` from the user text before the run.
2. Capture target evidence before a mutation objective, using the existing
   `StructuralResolver` and the existing `SymbolsTool` index.
3. After the run, evaluate `agent.EvaluateTaskCompletion` with the same inputs
   the TUI uses.
4. Print one terminal line:
   - `COMPLETED`
   - `NEEDS_REVIEW: <reason>`
   - `PAUSED: <reason>` (cancellation, budget, deadline, turn limit)
   - `FAILED: <reason>`
5. `ohgo run` returns non-zero for every outcome except `COMPLETED`, including
   a refused route. The plain REPL continues to the next prompt but never
   presents `NEEDS_REVIEW` or `PAUSED` as success.

This does **not** add a durable task lifecycle to stateless paths. The TUI
remains the task-bearing path; the stateless modes are explicitly non-durable
and must say so in their output/documentation.

Wire a `signal.NotifyContext` into the one-shot and plain-REPL run contexts so
the same cancellation semantics apply when the user interrupts the process.
This is part of making the exit status honest, not a new orchestration layer.

**Acceptance.** One-shot exits non-zero for unverified mutation, execution
without execution, empty answer, refusal, cancellation, budget exhaustion, and
deadline stop. Plain REPL prints the same verdict and continues.

### F4 — Persist the task contract and make review resumption truthful

**Problem.** `task.Progress` persists objective, status, todos, and budget, but
not the `IntentContract` or target evidence. The TUI recomputes the contract
from the new prompt. `Begin("continue")` can resume a `NEEDS_REVIEW` task and a
clean informational turn can mark it `COMPLETED` (audit probe confirmed).

**Fix.**

1. Add the authoritative contract to the task checkpoint (version bump):
   objective kind, targets, expected changes, verification requirements,
   behavioural flag, ambiguity/issues, and the review reason at the time of
   `NEEDS_REVIEW`.
2. On resume, completion for that task uses the **persisted contract**, not a
   newly invented contract from the continuation prompt.
3. If the new request differs materially from the persisted objective, do not
   silently attach it to the old task. Require an explicit human choice:
   continue the task, or abandon it and start a new one.
4. Expose that choice through the existing human-control layer: a minimal
   `/task cancel` (uses `Controller.Cancel`, already implemented) and a
   `NEEDS_REVIEW` acceptance path that starts a new task without asserting the
   old objective completed. Do not add a new workspace, tool, or automatic
   planner.
5. A later turn can complete the task only by supplying evidence for the
   persisted contract. Prior turns are not replayed into the evaluator; if the
   current turn does not carry the required mutation/verification, the task
   remains non-terminal.
6. `Task.Complete` remains guarded; `Begin` must not create a path that bypasses
   the review state's evidence requirement.

**Acceptance.** A `NEEDS_REVIEW` task resumed with `"continue"` and a clean
informational turn remains `NEEDS_REVIEW`/non-terminal. A turn that supplies
the persisted contract's mutation and verification can complete it. Cancelling
or explicitly replacing the task starts a new task identity/ledger.

### F5 — Truthful background evidence

**Problem.** `BashTool.spawnBackground` returns `Mutates=true` before the
process has run. A failed or timed-out job is returned by `wait` with
`IsError=false`. A successful background verification is never verification
evidence. A running mutating job can be counted as a completed mutation.

**Fix.**

1. Store the job's mutating classification and command/root in the background
   job record; the spawn result is `Executes=true` and **not**
   `Mutates=true`. Starting a process is not a completed mutation.
2. On `wait`:
   - a finished non-zero exit is `IsError=true`;
   - a timed-out/killed job is `IsError=true` and reports partial output;
   - a finished mutating job may set `Mutates=true` because the process ran and
     exited, but it is still a mutation requiring later verification;
   - a finished successful recognized check sets `Verifies=true` and the
     existing `VerificationEvidence` (command, kind, rebased scopes);
   - a still-running job contributes no mutation or verification evidence.
3. Completion must not treat a still-running job as evidence. A mutation
   objective whose only mutation was a background spawn and which never waited
   is `NEEDS_REVIEW`.
4. Keep detached jobs detached. Do not add a universal job killer on task
   completion; that is a separate lifecycle capability. Document that an
   un-awaited job is outside the completion boundary.

**Acceptance.** Spawn-only mutation is review; a failed/timed-out job is an
error; a waited successful check can verify; a still-running mutating job plus a
foreground check cannot complete a mutation objective without observed
mutation evidence.

### F6 — Linearize cancellation and the terminal transition

**Problem.** `runTurn` checks `ctx.Err()` and then calls `task.Complete`; the
cancel path is not synchronized with that check/transition.

**Fix.** Give the task transition one linearization point. A small mutex/atomic
state in the turn owner is sufficient:

- `cancelTurnNow` marks the turn cancelled under the same synchronization;
- `runTurn` checks the cancellation state and performs the terminal task
  transition under that synchronization;
- if cancellation wins, the action is `PAUSE`; if completion wins first, the
  cancellation is after the linearization point and does not need to rewrite a
  committed state.

Do not solve this by re-checking `ctx.Err()` after `Complete`; that would still
leave a window and would report a completion after the user saw a cancel.

**Acceptance.** A deterministic test that injects cancellation around the
post-run transition yields `PAUSED`, never `COMPLETED`.

### F7 — Honor graph cancellation in reverse adjacency; document first build

**Problem.** `ast.Index.Graph` builds reverse adjacency without checking the
context inside the edge loop; if the work limit is hit there, it returns a
truncated result without observing cancellation. The Phase 6 spec also claims a
check at least every 256 edge inspections, which the source does not implement.
The first lazy `ast.NewIndex` build is not context-cancellable.

**Fix.**

- Check `ctx.Err()` inside the reverse adjacency loop (at least every 256 edge
  inspections, matching the spec) and before returning a truncated result.
  Return the context error; never a successful partial result after
  cancellation.
- Add the same check to the forward traversal loop where it is missing.
- Do **not** redesign the Phase 1 symbols build. The first lazy `ast.NewIndex`
  build remains uncancellable; document that exact boundary in the closure
  report and remove any broader claim that every graph build can be
  interrupted. This is a C limitation, not a Phase 7 fix.
- Update `PHASE6_SPEC.md` §9 to match the implementation exactly.

**Acceptance.** A cancelled reverse query returns `context.Canceled`; a
cancelled first build is documented as the one explicitly uncancellable phase.

### F8 — Documentation closure

Update only what is factually wrong or historically misleading:

- `docs/architecture/ARCHITECTURE.md`: current tool list (`delete`, `symbols`,
  `graph`), and the production authority sequence (`Authorize`, not `Admit`).
- `docs/report/PHASE1_CLOSURE.md`: annotate that the "exactly one structural
  tool" statement is historical; Phase 6 added a second surface over the same
  source.
- `docs/benchmarks/PHASE6_SPEC.md`: cancellation wording, and the reverse
  adjacency behavior.
- `README.md` / `CHANGELOG.md`: only if the tool or behavior list is still
  stale after the fixes.
- `docs/report/PHASE7_CLOSURE.md`: the closure evidence and the final verdict.

Do not create a new benchmark specification unless F5/F1/F2 require a new
measured claim. They are correctness fixes, not capability claims; existing
tests and the Phase 4/5/6 regressions are the evidence.

## 5. Required Tests

Every test must fail if its fix is removed, and must assert an externally
observable contract.

### Completion evidence

- `TestEvaluateTaskCompletionRejectsExecutionWithoutEvidence` — a clean stop
  for `"run ls"`/`"execute echo hello"` is `NEEDS_REVIEW`.
- `TestEvaluateTaskCompletionAcceptsExecutedObjective` — a successful
  `Result.Executes` with no verification satisfies a checkless execution
  objective.
- `TestEvaluateTaskCompletionRejectsReadOnlyCallAsExecution` — a `read` result
  is not execution evidence.
- `TestEvaluateTaskCompletionRejectsEmptyInformationalAnswer` — empty and
  whitespace final text are `NEEDS_REVIEW`.
- `TestEvaluateTaskCompletionAcceptsInformationalAnswer` — non-empty text
  remains complete.
- `TestCompletionEvidenceBackgroundSpawnIsNotMutation` — a spawn alone does not
  set `mutated`.

### Headless outcome

- `TestOneShotReturnsNonZeroOnNeedsReview` — unverified mutation exits non-zero.
- `TestOneShotReturnsNonZeroOnRefusedRoute` — an ASK refusal is not exit 0.
- `TestOneShotReturnsZeroOnCompleted` — a verified scripted mutation exits 0.
- `TestReplReportsNeedsReviewAndContinues` — status is printed, next prompt
  still works.
- `TestOneShotHonoursCancellation` — signal/context cancellation yields
  `PAUSED`/non-zero.
- `TestOneShotDeadlineIsNotSuccess` — deadline without evidence yields
  `NEEDS_REVIEW`/non-zero.

### Task continuity

- `TestResumedReviewUsesPersistedContract` — resumed `"continue"` with no
  evidence stays `NEEDS_REVIEW`.
- `TestResumedReviewCompletesWithOriginalContractEvidence` — mutation +
  verification for the original target completes.
- `TestResumedReviewCannotCompleteWithDifferentContract` — an unrelated
  informational/mutation contract does not complete the old task.
- `TestReviewTaskCanBeCancelledOrReplaced` — the explicit human action starts a
  new task/ledger without claiming the old objective complete.
- `TestCheckpointVersionRejectsOrMigratesOldContract` — schema behavior is
  explicit; no silent loss of a review contract.

### Background evidence

- `TestBackgroundSpawnReportsExecutesNotMutates`.
- `TestWaitReportsFailedJobAsError`.
- `TestWaitReportsTimedOutJobAsError`.
- `TestWaitReportsSuccessfulVerification`.
- `TestWaitOnRunningJobIsNotCompletionEvidence`.
- `TestMutationObjectiveWithOnlyBackgroundSpawnIsReview`.
- `TestBackgroundMutationAfterWaitRequiresVerification`.

### Cancellation

- `TestCancelDuringPostRunTransitionPauses` — deterministic sync, no
  `COMPLETED`.
- `TestCancelDuringGraphReverseTraversal` — `context.Canceled`.
- `TestGraphReverseWorkLimitDoesNotMaskCancellation`.

### Authority / budget regression

- `TestProductionEntryPointsInstallAuthorizer` — one-shot, REPL, TUI, and
  spawner all reach the same gate.
- `TestProductionEntryPointsInstallFullBudget` — one-shot, REPL, TUI, and
  spawner reach a grant with turns/model calls/tool calls/children/background
  jobs.
- `TestBudgetExhaustionCannotCompleteWithoutEvidence` — existing Phase 5
  behavior preserved.
- `TestChildCannotResetParentGrant` — existing.
- `TestGraphCannotVerifyOrMutate` — existing.

## 6. Required Adversarial Matrix

Each row must be exercised through a production seam, not a direct call to a
helper, where the seam exists.

| Area | Scenario | Required result |
|---|---|---|
| Authority | ASK write/exec attempts | DENY; no execution; no state change |
| Authority | FUZZ mutating shell spellings, nested shell/interpreter | read-only workspace boundary; no protected write |
| Authority | BUILD read turn | handler ASK; no mutation; active BUILD remains |
| Authority | graph in ASK/FUZZ/BUILD | admitted read; no mutation/verification flag |
| Budget | exact boundary | admitted work completes; no overrun |
| Budget | one unit over on each dimension | explicit pause/exhaustion; no execution |
| Budget | child/grandchild attempts reset | shared pointer; refusal |
| Budget | background admission refused | no process start; no table slot |
| Cancellation | before model call | no model call; pause |
| Cancellation | during tool | tool stops; run returns cancellation; no completion |
| Cancellation | after mutation, before verification | review/pause; no completion |
| Cancellation | after verification, during post-run transition | pause; no completion (F6) |
| Cancellation | reverse graph traversal | context error, no truncated success (F7) |
| Delegation | nested write child inherits gate/deadline/budget | allowed only within authority; evidence propagated |
| Delegation | child unverified mutation | parent sees review/error, not success |
| Delegation | `readOnly` request | either works with a genuinely read-only set or fails closed; no writable child |
| Stale state | changed import | new edge; no stale edge |
| Stale state | same-size + same-mtime rewrite | documented blind spot; never claimed current |
| Stale state | module-path change | old edges invalidated |
| Ambiguity | package/symbol/target ambiguity | candidates; no arbitrary winner |
| Verification | failed check | review; not completion |
| Verification | wrong scope/kind | review; not completion |
| Verification | background check via `wait` | verification only after observed successful finish |
| Completion | no mutation for mutation objective | review |
| Completion | empty answer | review |
| Completion | execution with no execution | review |
| Completion | resumed review with unrelated evidence | review; task non-terminal |
| Completion | revert to pre-run hash | review |
| Completion | unrelated mutation | review |
| Completion | applied change + passing check for wrong intent | review under the contract |
| Malformed model | invalid arguments | observable error result; no execution |
| Malformed model | empty/whitespace answer | review |
| Malformed model | repeated identical calls | turn cap; pause |
| Background | failed job | `wait` IsError; no completion |
| Background | timed-out job | `wait` IsError; no completion |
| Background | still-running job | no evidence; review if objective depends on it |
| Repository mutation | direct write/delete outside target | unexpected mutation; review |
| Repository mutation | symlink escape | denied before execution |
| Resource exhaustion | all dimensions | explicit pause; no completion without evidence |
| Headless | one-shot unverified mutation/refusal/deadline | non-zero; explicit verdict |
| Headless | plain REPL unverified mutation | prints review/needs review; no success claim |

## 7. Required Benchmarks

No new benchmark is required for Phase 7. The fixes are correctness and
state-transition work, not a new capability or a new performance claim.
Creating a new benchmark artifact would add maintenance without proving a
terminal claim.

The existing benchmarks that materially prove the terminal contract are rerun:

| Benchmark | Why it is required | Artifact |
|---|---|---|
| Phase 4 23-case scripted completion | Proves F1/F2/F3/F4 do not regress target evidence, verification scope, alignment, or false-review behavior | `docs/benchmarks/results/phase4-completion.json` |
| Phase 5 30-case scripted budget | Proves F5/F6 and headless outcome do not break tree accounting, delegation, background admission, exhaustion, or cancellation | `docs/benchmarks/results/phase5-resource.json` |
| Phase 6 8-task static graph | Proves the graph contract and read-only non-interference remain intact after F7 | `docs/benchmarks/results/phase6-graph.json` |

The Phase 6 performance probe is **not** required: F7 adds bounded context
checks but makes no new latency, memory, or scale claim. The Phase 2 live-model
matrix is **not** rerun: it is historical, single-model, and cannot prove a
completion/evidence fix. Its fixture integrity remains covered by
`go run ./bench/repository -dry-run` and `go test ./bench/repository`.

No live-model H0/H1 graph benchmark is a closure requirement. That remains an
explicit unproven limitation, not a gate.

## 8. Required Regression

All commands must pass on the final Phase 7 tree.

```sh
gofmt -l .                                      # empty
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -count=1

go run ./bench/repository -dry-run              # 319 files, 27 tasks, 81 cells
go test ./bench/repository -count=1             # Phase 2 integrity, no model calls

OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -count=1
OHGO_PHASE5_BENCH=1 go test ./cmd/ohgo -run TestPhase5Benchmark -count=1
OHGO_PHASE6_BENCH=1 go test ./cmd/ohgo -run TestPhase6Benchmark -count=1
```

The Phase 4/5/6 benchmarks are rerun because F1–F6 change completion/evidence
semantics. The Phase 2 live-model matrix is **not** rerun: it is historical,
single-model, and not required to prove a correctness fix. Its dry-run
integrity and static probes remain the regression.

Each fix must be mutation-checked: break the fix, confirm the named test fails,
restore the fix, and confirm the suite is green. No source-grep test and no
`mock.module`-style global patch.

## 9. Required Documentation

Finite list:

1. `docs/report/PHASE7_CLOSURE.md` — objective, fixes, tests, results,
   evidence tiers, remaining limitations, final verdict.
2. `docs/architecture/ARCHITECTURE.md` — current tool list and authority
   sequence.
3. `docs/report/PHASE1_CLOSURE.md` — historical annotation for the two-surface
   Phase 6 extension.
4. `docs/benchmarks/PHASE6_SPEC.md` — cancellation wording and actual reverse
   adjacency behavior.
5. `docs/report/PHASE6_FINAL_SYNTHESIS.md` — this audit remains unchanged except
   for a post-Phase-7 note if closure succeeds.
6. `README.md` / `CHANGELOG.md` — only if the final tool/behavior list is still
   stale.

No new benchmark spec is required for Phase 7. The fixes are correctness and
state-transition work, not a new measured capability. If a benchmark file is
created anyway, it must have a hypothesis, a falsifiable task set, provenance,
and a result artifact; do not create one for documentation appearance.

## 10. Non-Goals — Frozen

Phase 7 must **not** implement:

- a call graph, reference graph, implementation graph, or whole-program
  semantic graph;
- file/symbol graph nodes, `contains`/`declares` edges, or a graph database;
- a second repository index, second parser, or external graph service;
- MCP, browser search, or a new tool protocol;
- a semantic LLM judge or probabilistic verification;
- a universal verification engine;
- provider redesign, transport rewrite, TUI redesign, or daemon architecture;
- autonomous planning or unrestricted background autonomy;
- incremental freshness, background indexing, or cross-module/vendored/
  `replace` resolution;
- tree-wide token/byte/monetary budgeting, per-child reservations, or fairness;
- killing detached jobs on task completion/cancellation;
- additional sandbox backends beyond fail-closed platform behavior;
- live-model graph benchmarking as a closure requirement;
- a Phase 8.

If a genuinely new architectural invariant is discovered, Phase 7 must stop and
report it. It must not silently become Phase 8.

## 11. Closure Gate

### PASS — `OHGO — FINAL / COMPLETE`

All of the following are true:

- F1–F8 implemented.
- Every required test in §5 passes and fails when its fix is removed.
- Every row in the §6 adversarial matrix has a reproducible result.
- The §8 regression is green, including race and the Phase 4/5/6 scripts.
- A1–A6 are closed; no new authority, budget, completion, or freshness
  invariant is violated.
- Only category C limitations remain, each documented in the closure report.
- `gofmt`, build, vet, test, and race are clean.
- The Phase 7 closure report states exactly what remains limited.

This is the only state that permits the phrase **OHGO — FINAL / COMPLETE**.

### PASS WITH LIMITATIONS

All A fixes are done, but one or more category B items remain, with:

- an explicit written acceptance of the limitation;
- a test showing it cannot produce false completion, false authority, or budget
  escape;
- a statement that it is outside the declared terminal scope.

The label is `OHGO — COMPLETE FOR DECLARED SCOPE (LIMITED)`, not
`OHGO — FINAL / COMPLETE`. A B item that affects completion, authority, budget,
or state truthfulness cannot be accepted under this gate; it is A.

### HOLD

Any of:

- any A1–A6 defect still reproduces;
- a required test is missing, flaky, or passes without the fix;
- the Phase 4/5/6 scripted benchmarks regress;
- a documentation claim contradicts the source on authority, budget,
  completion, freshness, or graph scope;
- the closure report cannot distinguish proven from inferred;
- a B item that affects a core invariant remains.

A HOLD is resumable work, not a failure of the architecture. It means do not
freeze.

### FAIL

Any of:

- a production authority bypass, budget escape, or false completion remains;
- a cancellation can become `COMPLETED`;
- a child can escape parent authority, budget, deadline, or completion;
- the full suite or race suite cannot pass;
- closing the gap requires a new architecture, not a local fix.

FAIL stops the phase and reopens the design question. It does not authorize a
new roadmap by default.

## 12. Terminal Design Rule

The default assumption is:

> **Phase 7 is the final closure boundary.**

A proposal for Phase 8 must prove that a Phase 7 closure criterion cannot
technically contain the required work. Wanting a new graph type, a new
benchmark, a new UI, or a new provider is not proof.

The goal is:

> **finish the system, not continue the roadmap.**

## 13. Final Boundary Statement

Phase 7 is allowed to change:

- completion evidence rules for execution and informational objectives;
- the stateless CLI outcome projection and exit status;
- the task checkpoint schema and review/continuation control;
- background result evidence and `wait` failure taxonomy;
- cancellation synchronization in the TUI task transition;
- graph reverse-adjacency cancellation;
- documentation that is factually stale.

Phase 7 is not allowed to change:

- the one structural source or one parse;
- the authority gate or workspace permission table;
- the root-owned budget or atomic admission;
- the Phase 4 target/verification/alignment rules;
- the package/import graph's declared scope;
- the human approval boundary;
- any Phase 1–6 benchmark expectation merely to make a test pass.

> **Do not implement anything beyond this boundary.**
