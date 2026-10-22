# Phase 7 — Final System Closure

- **Date:** 2026-10-22
- **Phase:** 7 — Final System Closure
- **Tree:** working tree based on `a045782` (Phase 6/7 reports, itself on `6a9765f`) plus the Phase 7 changes; no commit was made
- **Predecessors:** [`PHASE6_FINAL_SYNTHESIS.md`](PHASE6_FINAL_SYNTHESIS.md), [`PHASE7_TERMINAL_BOUNDARY.md`](PHASE7_TERMINAL_BOUNDARY.md)
- **Mode:** source-first, evidence-classified, targeted regression, mutation-checked
- **Decision frame:** *Dynamic Path, Static Authority, Truthful State Transition*

## 1. Verdict

**PASS WITH LIMITATIONS — `OHGO — COMPLETE FOR DECLARED DESIGN SCOPE (LIMITED)`.**

All six Phase 6 completion/evidence gaps (A1–A6), the graph-cancellation hardening
(F7), and the documentation closure (F8) are implemented and proven by source
inspection plus targeted tests. The full Go suite, the race suite, the Phase 2
fixture dry-run, and the Phase 4/5/6 scripted benchmarks are green. No new
authority, budget, discovery, graph, or completion capability was added.

The limitations that remain are explicitly accepted below. None permits a
production false completion, an authority bypass, a budget escape, a child
escape, or cancellation-as-completion on the tested paths.

## 2. Final System State

OHGO is a single-index, single-production-admission coding agent with one
authoritative completion model. Every production execution surface now reports
completion only from the Phase 4 contract and observable evidence: the TUI task
path, writable delegated children, `ohgo run`, and the plain REPL. A task that
cannot prove its objective is `NEEDS_REVIEW` or `PAUSED`, never `COMPLETED`, and
the headless process status distinguishes those states from success.

## 3. A1–A6 Results

| ID | Problem | Fix | Evidence | Tests | Result |
|---|---|---|---|---|---|
| **A1** | A checkless execution objective (`run ls`, `execute echo hello`) completed after a clean model stop with nothing executed. | The shared evidence scan now records `executed` only for a process that reached a successful terminal state (`Executes` + `Finished`) or a successful verification. `ObjectiveExecution` with no verification requirement is `NEEDS_REVIEW` without it. A started background spawn is not finished execution. | Source: `agent/completion.go`; `agent/tool.go` (`Finished`); `tools/bash.go`, `tools/wait.go`. Tests fail when the check is removed (mutation-checked). | `TestEvaluateTaskCompletionRejectsExecutionWithoutEvidence`, `TestEvaluateTaskCompletionAcceptsExecutedObjective`, `TestEvaluateTaskCompletionRejectsNonExecutionEvidence`, `TestHeadlessExecutionNeedsExecutionEvidence` | **PASS** |
| **A2** | An informational objective completed on an empty or whitespace-only final assistant message. | `ObjectiveInformational` now requires non-empty visible assistant text in the final turn. No minimum prose length, relevance score, or model judgement is used; refusal text is still text. A final tool-only turn remains review. | Source: `agent/completion.go`. Tests fail when the check is removed. | `TestEvaluateTaskCompletionRejectsEmptyInformationalAnswer`, `TestEvaluateTaskCompletionRejectsToolOnlyInformationalAnswer`, `TestEvaluateTaskCompletionAcceptsInformationalAnswer`, `TestEvaluateTaskCompletionTreatsRefusalTextAsAnswer`, `TestEvaluateTaskCompletionRejectsModelError`, `TestHeadlessRejectsEmptyInformationalAnswer` | **PASS** |
| **A3** | `ohgo run` and the plain REPL never invoked completion; `ohgo run` could exit `0` after unverified work, a refusal, a budget stop, or a deadline stop. | One shared stateless path (`runHeadlessTurn` → `projectRun`) builds the contract, captures the pre-run snapshot, invokes `agent.EvaluateTaskCompletion` / `EvaluateBudgetCompletion`, prints one verdict, and returns an honest exit status. `ohgo run` exits 0/2/3/1/130 for completed/review/paused/failed/cancelled; the REPL prints the same verdict and continues. SIGINT/SIGTERM are wired into the run context. | Source: `cmd/ohgo/outcome.go`, `cmd/ohgo/run.go`, `cmd/ohgo/main.go`. | `TestOneShotReturnsZeroOnCompleted`, `TestOneShotReturnsNonZeroOnNeedsReview`, `TestHeadlessBudgetExhaustionIsPaused`, `TestHeadlessCancellationIsNotSuccess`, `TestOneShotReturnsNonZeroOnRefusedRoute`, `TestHeadlessModelFailureIsFailed`, `TestHeadlessDeadlineIsNotSuccess`, `TestReplReportsNeedsReviewAndContinues`, `TestHeadlessHistoryIsNotCompletionEvidence`, `TestHeadlessExitCodes` | **PASS** |
| **A4** | A resumed `NEEDS_REVIEW` task could be completed by a later turn whose evidence belonged to a different contract. The checkpoint stored no contract, target snapshot, or review reason. | The checkpoint schema (version 2) persists the `IntentContract`, the pre-run `TargetSnapshot`, and the review reason. A non-terminal task's contract is immutable; a different incoming contract returns `ErrObjectiveConflict`. The TUI refuses a materially different prompt and exposes `/task status|cancel|accept`; cancel/accept close the task as `CANCELLED`, never `COMPLETED`, and the next prompt starts a new identity. A version-1 checkpoint loads but cannot complete until explicitly replaced. | Source: `task/task.go`, `task/progress.go`, `task/controller.go`, `tui/task.go`, `tui/app.go`. Tests fail when the persisted-contract use or conflict check is removed. | `TestControllerPersistsContractSnapshotAndReviewReason`, `TestControllerRefusesDifferentContractOnResume`, `TestControllerRefusesLegacyReviewWithoutContract`, `TestControllerReviewCanBeCancelledAndReplaced`, `TestVersion1CheckpointLoadsWithoutContract`, `TestResumedReviewUsesPersistedContract`, `TestResumedReviewRefusesDifferentObjective`, `TestResumedReviewCompletesWithPersistedContractEvidence`, `TestTaskCommandCancelDoesNotComplete` | **PASS** |
| **A5** | A background spawn claimed `Mutates` before the process ran; `wait` reported a failed or timed-out job as success and never reported a successful check as verification. | Spawn reports `Executes` only (not `Finished`, not `Mutates`). The job record stores its command, root, and mutating classification. `wait` reports a finished non-zero or timed-out job as `IsError`, reports a mutating job's mutation, and reports `Verifies`/`Verification` only for a successful recognised check via the existing `VerificationForIn` semantics. A still-running job contributes no terminal evidence. | Source: `tools/bash.go`, `tools/jobs.go`, `tools/wait.go`. Tests fail when the spawn mutation claim or the wait failure mapping is removed. | `TestBackgroundSpawnReportsExecutesNotMutates`, `TestWaitReportsFailedJobAsError`, `TestBackgroundTimeoutKillsARunaway`, `TestWaitReportsSuccessfulVerification`, `TestWaitOnRunningJobIsNotCompletionEvidence`, `TestCompletionEvidenceBackgroundSpawnIsNotMutation`, `TestEvaluateTaskCompletionRejectsMutationWithOnlySpawn`, `TestEvaluateTaskCompletionRejectsBackgroundMutationWithFailedVerification`, `TestEvaluateTaskCompletionRejectsRunningJobPlusForegroundCheck` | **PASS** |
| **A6** | A cancellation could land between the TUI's `ctx.Err()` check and `task.Complete`, persisting `COMPLETED` after the user cancelled. | The turn now owns one atomic terminal claim shared by Ctrl+C and the post-run transition. Cancellation stores `CANCELLED`; completion claims `OPEN → COMPLETED` with a compare-and-swap. A cancel that wins turns the action into `PAUSE` (including a review transition); a completion claim that wins first is not rewritten. A deadline stop is mapped to a resource stop and pauses unless already-complete evidence justifies completion. A deterministic test injects cancellation in the exact old window. | Source: `tui/app.go`. Test fails when the atomic claim is removed. | `TestCancelDuringPostRunTransitionPauses`, `TestCancelDuringReviewTransitionPauses`, existing `TestRunTurnPausesWhenCancelledDuringPostRunVerification`, `TestRunTurnPausesTaskOnInterruption` | **PASS** |

### 3.1 Source audit summary

| Fix | Production path | State owner | Evidence producer | Completion consumer | Persistence boundary | Cancellation boundary |
|---|---|---|---|---|---|---|
| A1 | `agent.Loop.execute` → `Turn.Results` | `agent.Loop` | `tools.BashTool` / `tools.WaitTool` (`Executes`, `Finished`, `IsError`) | `agent.EvaluateTaskCompletion` | none (per-run) | loop/tool checks before and during execution |
| A2 | `agent.Loop.Run` final `Turn.Assistant` | `agent.Loop` | provider stream | `agent.EvaluateTaskCompletion` | none (per-run) | context at loop entry and drain |
| A3 | `runOnce` / `repl` → `runHeadlessTurn` | `cmd/ohgo` | same loop/tool evidence as TUI | `projectRun` → Phase 4 evaluator | session transcript only; no durable task | `signal.NotifyContext` + loop checks |
| A4 | `tui.runTurn` → `task.Controller` | `task.Controller` | persisted contract/snapshot + current turn evidence | `turnTaskOutcome` → Phase 4 evaluator | `.ohgo/progress.json` v2 (contract, snapshot, review reason) | TUI atomic terminal claim + task transition |
| A5 | `BashTool.spawnBackground` / `WaitTool.Execute` | `tools.BackgroundJobs` | process reaper and exit record | `agent.EvaluateTaskCompletion` via `ToolOutcome` | job table (in-memory) | per-job timeout, root deadline, wait checks |
| A6 | `tui.Program.runTurn` | `tui.Program` | `turnTerminal` atomic state | `task.Complete` / `task.Pause` / `task.NeedsReview` | `task.Controller` checkpoint | single atomic claim |

## 4. F7–F8 Results

| Item | Result | Evidence |
|---|---|---|
| **F7 — Graph cancellation** | **PASS.** Forward and reverse-adjacency loops check the context at every queue node and at least every 256 edge inspections, and re-check before returning a result. A cancelled reverse query returns `context.Canceled`; no truncated result is published as success. Depth, node, edge, work, ambiguity, freshness, authority, and budget semantics are unchanged. | Source: `ast/graph.go` (`graphCancelCheckInterval`, `checkCancelled`). Tests: `TestGraphReverseAdjacencyHonoursCancellation`, `TestGraphCancellation`, `TestGraphCancellationDuringBuildDoesNotPublishPartialState`, `TestGraphBoundsTraversal`. Mutation check: disabling `checkCancelled` makes the reverse test fail. |
| **F8 — Documentation closure** | **PASS.** Current-state docs now describe the Phase 7 completion/evidence model, headless verdict/exit codes, persisted task contract, truthful background evidence, cancellation linearization, and graph cancellation. Historical Phase 1/2/6 statements are annotated as historical rather than silently rewritten. | `README.md`, `CHANGELOG.md`, `docs/architecture/ARCHITECTURE.md`, `docs/architecture/WORKSPACE_AUTHORITY.md`, `docs/architecture/ARCHITECTURE_CONTRACT_V1.md` (current-state note), `docs/report/PHASE1_CLOSURE.md`, `docs/report/PHASE2_CLOSURE.md`, `docs/report/FINAL_CLOSURE.md`, `docs/report/PHASE6_FINAL_SYNTHESIS.md` (post-Phase-7 note), `docs/benchmarks/PHASE6_SPEC.md` §9. |

## 5. Final Architecture

```text
HUMAN
  ↓
Intent
  ↓
Workspace / Task
  ├─ TUI: task.Controller (durable contract + snapshot + review reason)
  └─ headless: per-run contract (explicitly non-durable)
  ↓
Agent Loop
  ↓
Model Proposal
  ↓
Discovery / Coordinates / Graph
  └─ one ast.Index / one StructuralSource / one parse
  ↓
Authority
  └─ securityGate (Config.Authorize): DENY / APPROVE / AUTO
  ↓
Budget
  └─ one root-owned atomic grant shared by children/background jobs
  ↓
Execution
  └─ tools; background spawn ≠ finished mutation
  ↓
Observable Evidence
  └─ target hashes, execution terminal state, verification scopes
  ↓
Verification / Alignment
  └─ Phase 4 target/scope/alignment rules, deterministic
  ↓
Authoritative Completion
  └─ agent.EvaluateTaskCompletion / EvaluateBudgetCompletion
  ↓
Task State
  ├─ TUI: COMPLETED / NEEDS_REVIEW / PAUSED / FAILED / CANCELLED
  └─ headless: COMPLETED / NEEDS_REVIEW / PAUSED / FAILED / CANCELLED
  ↓
Honest User / Process Result
  └─ verdict line + exit 0 / 2 / 3 / 1 / 130
```

The TUI terminal transition and cancellation share one atomic claim
(`turnTerminal`). Graph traversal cancels at bounded work intervals. Background
jobs are evidence only after `wait` observes their terminal state.

## 6. Invariant Matrix

| Invariant | Status | Evidence |
|---|---|---|
| Authority | **PASS** | Production `buildLoop` installs `Config.Authorize` (`securityGate`); `agent.Loop.execute` evaluates every call; DENY is terminal; APPROVE requires a fresh confirming evaluation; path containment is the existing `tools.resolvePath`; workspace table unchanged. Tests: `agent/authorize_test.go`, `cmd/ohgo/authority_route_test.go`, `security/security_test.go`, `tools/*sandbox*`. |
| Budget | **PASS** | One root-owned `budget.Budget` per run; `AdmitAll` atomic; children and background jobs share the pointer; Phase 5 30/30 scripted cases: 0 overruns, 0 escapes, 0 execution after failed admission. No Phase 7 change to admission semantics. |
| Discovery | **PASS** | `tools.NewStructuralSource` is the one production source; `symbols` and `graph` share it; one `ast.Index`/one parse. Phase 1/6 tests and Phase 2 dry-run (319 files, 295 Go, 27 tasks, 81 cells) pass. |
| Graph | **PASS** | Package/import-only, read-only, bounded; deterministic edges with file:line witnesses; no verification/mutation flags; reverse cancellation now bounded. Phase 6 H0/H1: 4/8 vs 8/8 correct, 4 vs 0 false positives, 0 false edges, 10,780 vs 7,140 context bytes. |
| Verification | **PASS** | Verification remains defined-check evidence (tests/build/vet) with scope matching; background checks use the same `VerificationForIn` semantics; a failed/timed-out background check is never verification. Semantic correctness (a wrong change with passing tests) remains an accepted limitation, not a Phase 7 defect. |
| Completion | **PASS** | One authoritative evaluator (`EvaluateTaskCompletion`) used by TUI, writable children, and both headless paths. F1/F2 evidence rules; F4 persisted contract; F3 honest exit status. Phase 4 scripted benchmark: 23 cases, H0 false completion 11 → H1 0, 0 false reviews. |
| Cancellation | **PASS** | TUI terminal transition linearized with an atomic claim; headless paths use signal contexts; loop/tool checks remain; graph reverse traversal checks every 256 edges. Deterministic F6 test and race suite pass. |
| Delegation | **PASS** | Children inherit authority, deadline, turn cap, budget, tools, compaction; depth is structural; child evidence propagates (`Mutated`, `Executed`, `Verified`, `Verification`); an incomplete child is an error to the parent. Read-only children still fail closed (see limitations). |
| Freshness | **PASS / LIMITATION** | Size+mtime snapshot and `go.mod` fingerprint; stale rebuild errors are returned, never served silently. Same-size + same-mtime rewrites and the first lazy index build remain documented limits. |
| Human Control | **PASS** | Approval gate unchanged; DENY before approval; `--yes`/settings are explicit user decisions; `/task cancel` and `/task accept` are explicit human choices; no model authority. |

## 7. Known Limitations

### 7.1 Accepted terminal-scope limitations (category C)

| Limitation | Why it is acceptable | What is not proven |
|---|---|---|
| Package/import graph only | It is the smallest relation the Go parser proves deterministically; graph evidence is advisory, not authority or verification. | Calls, references, implementations, symbol/file nodes, exact semantic impact. |
| No cross-module, vendored, `replace`, nested-module resolution | Declared scope is a single module; external/unresolved are explicit. | Multi-module repository behavior. |
| Source-level, not build-context-aware | Generated/build-tagged files are reported as source declarations. | What the compiler actually builds. |
| Full freshness walk and full rebuild | Bounded and measured on synthetic trees; stale snapshots are not served silently. | Production-scale latency; incremental invalidation. |
| Same-size + same-mtime rewrite undetected | Normal editor writes update mtime; the blind spot is explicit. | Absolute freshness under timestamp preservation. |
| First lazy `ast.Index` build is not cancellable | Frozen Phase 1 build; Phase 7 did not redesign it. | Immediate cancellation during the first cold index build. |
| No live-model H0/H1 graph evidence | Providers were unavailable/incompatible; the benchmark is explicitly tool-level. | That a real model chooses `graph` or improves answers/turns. |
| Verification is defined-check evidence | Tests/build/vet plus target hashes are observable. | Semantic correctness: a wrong change with passing tests can still complete. |
| No token/byte/monetary budget | Per-call caps and compaction bound context; no hard aggregate. | Hard cost containment. |
| Per-run grant; cumulative ledger not enforced | A user boundary is an explicit new grant; the ledger records history. | A lifetime task ceiling. |
| Detached background jobs survive | They are bounded by admission, timeout, root deadline, and table caps; the model must `wait` for evidence. | That no job runs after the task ends; un-awaited jobs are outside the completion boundary. |
| Deadline does not interrupt in-flight provider/tool work | New model calls stop; shell work is capped. | Immediate stop at the deadline instant. |
| Darwin sandbox protects the workspace, not the whole filesystem | The contract is "no repository mutation." | Writes to user files outside the workspace. |
| Network is classified as a read capability | Approval is still required; there is no separate egress authority. | Data-loss/egress semantics. |
| `--yes` / settings allow skip prompts | Explicit user configuration; authority DENY still wins. | Human approval for every call. |

### 7.2 Accepted category B items (explicit, outside the Phase 7 closure gate)

These are the Phase 6 B items that remain. Each is either a library seam not
installed in production, a robustness concern that cannot authorize or complete
work by itself, or a declared non-goal. None reproduces A1–A6.

| Item | Status | Why it does not reopen A1–A6 |
|---|---|---|
| B3 — concurrent file tools can lose writes | Not fixed | A lost update can produce a wrong result, which is covered by the semantic-verification limitation; it cannot create a completion claim without target/verification evidence. |
| B4 — `agent.New` accepts nil `Authorize`/`Budget` | Not fixed | Production `buildLoop` always installs both; the library seam is documented and fails open only for an embedder that supplies neither. |
| B5 — read-only delegation advertised but unusable | Not fixed | It fails closed; no writable child is created from a read-only request. |
| B6/B7 — `BeforeToolCall`/`AfterToolCall` seams | Not fixed | No production hook is installed; authority and completion consume the normal path. |
| B8 — file tools have no file-size bound | Not fixed | Robustness/DoS risk, not a completion or authority invariant. |
| B10 — `--deadline` is session-absolute in interactive modes | Documented | It is a consistent whole-session bound; per-turn deadlines are not claimed. |
| B12 — `shell/` and `memory/` have no production importer | Maintained test-bearing packages | Scope/maintenance debt, not a repository-intelligence or completion path. |
| B13 — review acceptance/abandon | **Fixed by Phase 7** | `/task cancel` and `/task accept` close without completion and start a new identity. |

### 7.3 Phase 7-specific limitations

1. **Stateless paths are non-durable.** `ohgo run` and the plain REPL do not
   create a task checkpoint. `--resume` restores session history, not a
   `task.Controller` identity. A resumed session is judged on the current
   turn's evidence.
2. **Review resolution is current-turn evidence.** Prior turns are not replayed
   into the evaluator. A resumed mutation task must carry a mutation and a
   verification in the resumed turn; verification alone leaves it in review,
   even when the persisted baseline shows the earlier change.
3. **Version-1 checkpoints cannot complete.** They load for recovery but have no
   contract or snapshot; the human must `/task cancel`/`/task accept` and start
   a new task. This is deliberate: silently binding a legacy review to a new
   prompt would reintroduce A4.
4. **`EvaluateCompletion` remains frozen.** It is retained for the historical
   Phase 4 H0 measurement and for the read-only-child compatibility path. A
   direct embedder that calls `RunSubagent` with no contract gets the frozen
   evidence rules; production writable children always receive a contract.
5. **Headless `PAUSED` on deadline without evidence.** A deadline is not a
   failure, but it is also not success; only already-complete evidence can
   complete a deadline-stopped run.

## 8. Deferred Scope

The following remain explicitly deferred and were not implemented in Phase 7:

- Call, reference, implementation, or whole-program semantic graphs.
- File/symbol graph nodes, `contains`/`declares` edges, or a graph database.
- A second repository index, second parser, external graph service, daemon, or
  incremental indexing.
- Cross-module, vendored, `replace`, nested-module, build-tag, or generated-code
  resolution beyond the current source-level declaration model.
- MCP, browser search, or a new tool protocol.
- A semantic LLM judge or probabilistic verification.
- A universal verification engine.
- Provider/transport/TUI redesign or a daemon architecture.
- Autonomous planning or unrestricted background autonomy.
- Tree-wide token/byte/monetary budgets, per-child reservations, or fairness.
- Killing detached jobs on task completion/cancellation.
- Additional sandbox backends beyond the fail-closed platform behavior.
- Live-model graph benchmarking as a closure requirement.
- A Phase 8. Per the terminal boundary, a future feature request is a new
  product decision, not an automatic continuation of this engineering process.

## 9. Regression Results

All commands were run on the final tree. Exact commands and outcomes:

```sh
gofmt -l .                                      # empty
go build ./...                                  # PASS
go vet ./...                                    # PASS
go test ./... -count=1                          # PASS (29 packages + tokens no tests)
go test -race ./... -count=1                    # PASS (race)
```

Historical suites:

```sh
go run ./bench/repository -dry-run              # PASS: 319 files (295 Go), 27 tasks, 81 cells
go test ./bench/repository -count=1             # PASS
OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -count=1  # PASS
OHGO_PHASE5_BENCH=1 go test ./cmd/ohgo -run TestPhase5Benchmark -count=1  # PASS
OHGO_PHASE6_BENCH=1 go test ./cmd/ohgo -run TestPhase6Benchmark -count=1  # PASS
```

### 9.1 Phase 4 completion benchmark

- Cases: **23**.
- H0: 10 true completions, **11 false completions**, 2 correct reviews, 0 false reviews.
- H1: 10 true completions, **0 false completions**, 13 correct reviews, **0 false reviews**.
- 64 model calls, 41 tool calls, 18 verification runs.
- The H1 expectation is unchanged. The artifact's timestamps and per-case
  latencies were regenerated; contract bytes and outcome counts are unchanged.

### 9.2 Phase 5 budget benchmark

- Cases: **30/30 passed**, 0 failures.
- 0 budget overruns, 0 execution after failed admission, 0 child budget
  escapes, 0 background budget escapes, 0 deadline escapes, 0 cancellation
  escapes, 0 false completions, 0 false reviews.
- 74 model calls, 41 tool calls, 23 tool executions, 3 background jobs.
- Phase 5 admission/limit behavior is unchanged by Phase 7.

### 9.3 Phase 6 graph benchmark

- H0: 4/8 correct, 4 false positives, 0 false edges, 8 tool calls,
  10,780 context bytes.
- H1: 8/8 correct, 0 false positives, 0 false edges, 8 tool calls,
  7 graph queries, 7,140 context bytes.
- Phase 6 graph semantics, bounds, ambiguity, freshness, and read-only behavior
  are unchanged; F7 adds bounded cancellation checks.

### 9.4 Phase 2 fixture integrity

- Dry-run: fixture and ground truth validated; 319 files (295 Go), 27 tasks,
  81 cells; no model calls.
- `go test ./bench/repository`: PASS.
- The Phase 2 live-model matrix was **not** rerun. It is historical,
  single-model, single-fixture evidence and cannot prove a completion/evidence
  fix. Its static fixture integrity remains covered.

### 9.5 Historical expectation changes

| Old expectation | Why it was wrong | New expectation |
|---|---|---|
| `TestBackgroundTimeoutKillsARunaway` expected a timed-out background job to be a non-error result. | A timed-out process did not succeed; reporting it as a successful observation was the A5 failure. | The same test now requires `IsError == true` while still asserting the timeout notice. |
| `TestEvaluateTaskCompletionAcceptsInformationalAnswer` used `cleanStop()` with no visible text. | An empty final message is not an answer; this was the A2 failure. | The test uses `answerTurn("the package parses Go files")`; separate tests assert empty/whitespace review. |
| `task.Controller.Begin(objective)` could resume a task without a persisted contract. | That API could not bind the evidence boundary A4 requires. | `Begin(intent, snapshot)` persists the contract/snapshot for new tasks and refuses a different contract on resume. Tests were updated to pass a contract. |
| Version-1 checkpoints could resume into completion. | They carry no contract or snapshot, so completion would be evidence-free. | Version-1 checkpoints load for recovery but cannot complete; explicit `/task cancel`/`accept` replacement is required. |

No Phase 4/5/6 benchmark expectation was weakened. The artifacts were rerun
because F1–F6 change completion/evidence semantics; their outcome counts are
unchanged.

## 10. Phase 7 Adversarial Matrix

The matrix is exercised through the production seams where they exist. Each
row names the test that proves it.

### Completion

| Scenario | Test | Result |
|---|---|---|
| empty informational answer | `TestEvaluateTaskCompletionRejectsEmptyInformationalAnswer`, `TestHeadlessRejectsEmptyInformationalAnswer` | review |
| whitespace-only answer | same | review |
| successful informational answer | `TestEvaluateTaskCompletionAcceptsInformationalAnswer`, `TestOneShotReturnsZeroOnCompleted` | complete |
| execution without execution | `TestEvaluateTaskCompletionRejectsExecutionWithoutEvidence`, `TestHeadlessExecutionNeedsExecutionEvidence/missing` | review |
| execution success | `TestEvaluateTaskCompletionAcceptsExecutedObjective`, `TestHeadlessExecutionNeedsExecutionEvidence/successful` | complete |
| execution failure | `TestEvaluateTaskCompletionRejectsNonExecutionEvidence` (`failed`) | review |
| verification missing | `TestHeadlessMutationRequiresVerification/missing` | review |
| verification failure | `TestHeadlessMutationRequiresVerification/failed`, `TestEvaluateTaskCompletionRejectsBackgroundMutationWithFailedVerification` | review |
| unrelated mutation | `TestEvaluateTaskCompletionRejectsUnexpectedMutation`, `TestEvaluateTaskCompletionRejectsUnrelatedCreate` | review |
| alignment mismatch | `TestEvaluateTaskCompletionRejectsWrongTarget`, `TestSemanticIntegrationRejectsWrongTarget` | review |
| model refusal | `TestEvaluateTaskCompletionTreatsRefusalTextAsAnswer` | complete (non-empty text) |
| model error | `TestEvaluateTaskCompletionRejectsModelError`, `TestHeadlessModelFailureIsFailed` | review / failed |

### Headless

| Scenario | Test | Result |
|---|---|---|
| `ohgo run` success | `TestOneShotReturnsZeroOnCompleted` | COMPLETED, exit 0 |
| `ohgo run` review | `TestOneShotReturnsNonZeroOnNeedsReview` | NEEDS_REVIEW, exit 2 |
| `ohgo run` failure | `TestHeadlessModelFailureIsFailed` | FAILED, exit 1 |
| `ohgo run` cancellation | `TestHeadlessCancellationIsNotSuccess` | CANCELLED, exit 130 |
| `ohgo run` budget exhaustion | `TestHeadlessBudgetExhaustionIsPaused` | PAUSED, exit 3 |
| `ohgo run` deadline | `TestHeadlessDeadlineIsNotSuccess` | PAUSED, exit 3 |
| `ohgo run` authority refusal | `TestOneShotReturnsNonZeroOnRefusedRoute` | FAILED, exit 1 |
| plain REPL review + continue | `TestReplReportsNeedsReviewAndContinues` | NEEDS_REVIEW then COMPLETED |

### Resume

| Scenario | Test | Result |
|---|---|---|
| review → unrelated evidence | `TestResumedReviewUsesPersistedContract`, `TestEvaluateTaskCompletionRejectsMutationDuringInformationalRequest` | non-terminal |
| review → correct evidence | `TestResumedReviewCompletesWithPersistedContractEvidence` | COMPLETED |
| review → different objective | `TestResumedReviewRefusesDifferentObjective`, `TestControllerRefusesDifferentContractOnResume` | refused |
| review → cancellation | `TestCancelDuringReviewTransitionPauses` | PAUSED |
| review → restart | `TestControllerPersistsContractSnapshotAndReviewReason` | contract/snapshot/reason restored |
| persisted contract mismatch | `TestControllerRefusesDifferentContractOnResume` | `ErrObjectiveConflict` |
| legacy checkpoint | `TestControllerRefusesLegacyReviewWithoutContract`, `TestVersion1CheckpointLoadsWithoutContract` | cannot complete |

### Background

| Scenario | Test | Result |
|---|---|---|
| success | `TestBackgroundOutputCollectedViaWait` | finished success |
| failure | `TestWaitReportsFailedJobAsError` | `IsError` |
| timeout | `TestBackgroundTimeoutKillsARunaway` | `IsError` |
| cancellation | `TestWaitHonoursCancellation` | cancelled |
| mutation | `TestWaitReportsSuccessfulVerification` context + `TestEvaluateTaskCompletionRejectsMutationWithOnlySpawn` | spawn-only review; waited mutation requires verification |
| verification | `TestWaitReportsSuccessfulVerification` | `Verifies` only after observed exit |
| budget | `TestBackgroundBudgetRefusesWithoutStartingAJob` | no process |
| deadline | `TestBackgroundJobIsCappedByTheRootDeadline` | capped |
| child-created job | shared `BackgroundJobs` table (Phase 5 tests) | shared admission/table |
| running job | `TestWaitOnRunningJobIsNotCompletionEvidence` | no terminal evidence |

### Cancellation

| Scenario | Test | Result |
|---|---|---|
| before model | `TestHeadlessCancellationIsNotSuccess` | CANCELLED |
| during model | loop cancellation tests (`agent/cancellation_test.go`) | context error |
| before tool / during tool | loop cancellation tests | no execution/completion |
| during verification | `TestRunTurnPausesWhenCancelledDuringPostRunVerification` | PAUSED |
| before terminal transition | `TestCancelDuringPostRunTransitionPauses` | PAUSED |
| review transition | `TestCancelDuringReviewTransitionPauses` | PAUSED |
| deadline | `TestHeadlessDeadlineIsNotSuccess` | PAUSED |
| budget race | `TestHeadlessBudgetExhaustionIsPaused`, Phase 5 cases | PAUSED/no false completion |

### Graph

| Scenario | Test | Result |
|---|---|---|
| reverse traversal cancellation | `TestGraphReverseAdjacencyHonoursCancellation` | `context.Canceled` |
| freshness cancellation | `TestGraphCancellation`, `TestGraphCancellationDuringBuildDoesNotPublishPartialState` | `context.Canceled` / no partial state |
| bounded traversal after cancellation | `TestGraphBoundsTraversal`, `TestGraphCancellation` | bounds unchanged; no truncated success after cancel |

## 11. Mutation Checks

The targeted tests were verified to fail when their fix is removed:

| Fix removed | Test that failed |
|---|---|
| F1 execution-evidence check | `TestEvaluateTaskCompletionRejectsExecutionWithoutEvidence` |
| F2 non-empty-answer check | `TestEvaluateTaskCompletionRejectsEmptyInformationalAnswer` |
| F5 spawn `Mutates` removal | `TestBackgroundSpawnReportsExecutesNotMutates` |
| F5 wait failure mapping | `TestWaitReportsFailedJobAsError`, `TestBackgroundTimeoutKillsARunaway` |
| F6 atomic terminal claim | `TestCancelDuringPostRunTransitionPauses` |
| F7 reverse cancellation checks | `TestGraphReverseAdjacencyHonoursCancellation` |
| A4 persisted-contract conflict check | `TestControllerRefusesDifferentContractOnResume` |
| A4 TUI persisted-contract use | `TestResumedReviewUsesPersistedContract` |

## 12. Terminal Decision

> **Is OHGO complete for its declared design scope?**
>
> **Yes, for the declared scope, with the explicitly accepted limitations in
> §7.** All terminal correctness criteria A1–A6 are proven; F7 and F8 are
> complete; all Phase 1–6 regression suites and the Phase 4/5/6 scripted
> benchmarks are green; no authority, budget, child, graph, verification, or
> cancellation invariant was weakened.

**OHGO — COMPLETE FOR DECLARED DESIGN SCOPE (LIMITED).**

The development process represented by Phases 1–7 is closed. A future feature
request is a new product decision, not an automatic Phase 8.
