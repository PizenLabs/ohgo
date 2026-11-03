# Task Continuity — Product Readiness Report

**Workstream:** automated product-readiness evaluation and correction of Phases
1–3 Task Continuity.
**Repository:** `github.com/PizenLabs/ohgo`
**Branch:** `feature/task-continuity`
**Baseline commit at evaluation:** `624aef05c136d198bb832150b65f78c0a87fffdf`
**Environment:** `go1.27.3 darwin/arm64`, Darwin 27.0.0.
**Catalog:** [`TASK_CONTINUITY_TEST_CATALOG.md`](TASK_CONTINUITY_TEST_CATALOG.md)
**Raw results:** [`results/`](results/)

---

## 1. Executive verdict

**READY FOR PRODUCT RELEASE.**

The Phase 1–3 contract is implemented, the defects found by the readiness
workstream are repaired, and the full readiness suite plus the pre-existing
repository suite are green:

```text
go test -count=1 ./...                     exit 0
go test -race -count=1 -run '^TestReadiness'
    ./internal/continuity/task/ ./internal/tui/ ./cmd/ohgo/
                                           exit 0, no race report
go build ./...  go vet ./...  gofmt -l .   exit 0
golangci-lint run ./...                    0 issues
```

All three release blockers from the previous evaluation are resolved:
completed tasks are archived before a new objective can overwrite them, a
repeated objective continues the same identity, and every handoff transition
rolls back when its checkpoint write fails. The two P2 findings (verification-
only resume, widening-policy shape) are resolved as a behavior fix and a
documented canonical path respectively. Both P3 findings (projection
truthfulness, caller role claims) are fixed and covered by regressions.

This verdict is scoped to the deterministic runtime contract. It does not claim
semantic correctness of a model-proposed change, and it does not certify a live
provider/model integration; those boundaries are stated in §12.

## 2. What changed

| Finding | Severity | Production change | Regression that now protects it |
| --- | --- | --- | --- |
| F-01 completed task overwritten | P0 | `internal/tui/app.go` archives a terminal live task through the existing `Detach` path before the new objective calls `Begin`; `cmd/ohgo/taskcontinuity.go` projects the archived task even though it is no longer resumable. | `TestReadinessA08…`, `TestReadinessH08…`, `TestReadinessH09…`, `TestTerminalTaskIsNeverResumable` |
| F-02 same-role repeat fragmented identity | P1 | `internal/tui/task.go` + `app.go`: `objectiveContinuation` treats an exact repeat of the persisted objective and contract kind as the same task. | `TestReadinessA02…` (plus A02b control) |
| F-03 failed handoff writes published uncommitted state | P1 | `internal/continuity/task/handoff.go`: `ApproveHandoff`, `ApplyHandoff`, `RejectHandoff`, `FailHandoff`, `ReconcileRecoveredHandoff`, and `RecordWorkspaceSwitch` now snapshot and restore on a failed checkpoint write. | `TestReadinessE02…`, `E03…`, `E02b…`, `E02c…`, `E02d…` |
| F-04 verification-only resume could not prove | P2 | `internal/agent/completion.go` + `internal/tui/completion.go` + `internal/continuity/task/completion.go`: a durable current change (`HasCurrentChange`) may satisfy the per-run mutation gate; the durable boundary still requires a later covering check. | `TestReadinessI02b…` |
| F-05 staged scenario shape | P2 | No authority-policy change. Direct ASK→BUILD widening is the canonical policy in `WORKSPACE_AUTHORITY.md`; the staged path is executable with explicit `/fuzz` and `/build` commands and is covered by `RI-I01`. | `TestReadinessC06…`, `TestReadinessI01…` |
| F-06 projection could outrun a commit | P3 | `internal/continuity/task/progress.go`: the checkpoint is written first and the projection second; a failed checkpoint never rewrites the human artifact, and the checkpoint remains authoritative on load. | `TestReadinessE01…`, `TestReadinessE18…` |
| F-07 caller role claim trusted | P3 | `internal/continuity/task/handoff.go`: `RecordWorkspaceSwitch` verifies the live role and the task's recorded source before writing metadata. | `TestReadinessC02…`, `TestReadinessC05…` |

One additional UX completion was made so the F-01 archive is reachable:
`Controller.ArchivedTasks`, the `TaskHooks.ListArchived` seam, `/task history`,
and `/task show` falling back to archived terminal work. It is read-only and
grants no authority.

## 3. Scope and exclusions

In scope and verified:

- durable task identity, lifecycle, context, evidence, handoff, timeline,
  lineage, recovery, persistence, archive, and completion proof in
  `internal/continuity/task`;
- their real runtime integration in `internal/tui` and `cmd/ohgo`;
- workspace authority policy in `internal/control/workspace`;
- process-boundary restart, crash-window, and failed-persistence behavior;
- the UI surfaces that make durable state visible and reachable.

Excluded:

- live model/provider behavior and the semantic quality of model output;
- general agent-loop correctness unrelated to task continuity;
- concurrent external edits and the separate recovery service;
- a real previous-release binary for the v2→v3 downgrade barrier;
- semantic correctness of a requested code change: `PROVEN` is a runtime
  evidence claim, not an independent proof of correctness.

## 4. Test methodology

Three levels, matching the required architecture:

- **Level A — deterministic component/contract tests**
  (`internal/continuity/task/readiness_component_test.go`).
  Injected clock, temporary project roots, the existing `Store.write` test seam
  for checkpoint faults, and the real controller. No controller or lifecycle
  logic is mocked.
- **Level B — runtime integration tests**
  (`internal/tui/readiness_runtime_test.go`, `internal/tui/task_test.go`).
  The real `Program.runTurn`/`submit`/`resolveTransition`/`taskCommand`
  entrypoints, real `workspace.Authority` admission, real
  `internal/execution/tools` read/bash/write tools against temporary Go modules,
  and scripted offline model streams.
- **Level C — process-boundary tests**
  (`internal/tui/readiness_process_test.go`,
  `cmd/ohgo/readiness_process_test.go`).
  Re-executed test binaries run the production turn or composition in one
  process and exit; a fresh process restores durable state. The handoff crash
  test uses the real `harness.SwitchWorkspace`, the real authority gate, and the
  real SQLite session store.

Fault injection uses only existing seams: `Store.write` (already a testable
function field) and directory permissions. No new production injection
mechanism was introduced.

## 5. Test environment

- `go version go1.27.3 darwin/arm64`
- Branch `feature/task-continuity`, baseline commit
  `624aef05c136d198bb832150b65f78c0a87fffdf`
- Working tree after this workstream: production and test changes are
  **uncommitted**, as required. See §13.
- No network, no API keys, no live provider, no real user data.

## 6. Test results

| Result | Top-level | Including subtests |
| --- | ---: | ---: |
| PASS | 46 | 79 |
| FAIL | 0 | 0 |
| SKIP (inert subprocess helpers) | 2 | 2 |
| NOT TESTED / INCONCLUSIVE | see §12 | — |

The pre-existing suite (excluding the readiness tests) is also green:

```text
go test -count=1 -skip '^TestReadiness' ./...   exit 0
```

The five previously failing tests now pass:

- `TestReadinessA02SameObjectiveSameRoleTurnsPreserveIdentity`
- `TestReadinessA08CompletedTaskIsPreservedBeforeANewObjective`
- `TestReadinessI02bResumeWithVerificationOnlyCompletesInterruptedMutation`
- `TestReadinessE02FailedApprovalWriteLeavesNoUncommittedApproval`
- `TestReadinessE03FailedApplyWriteLeavesNoUncommittedApplied`

New regressions added with the fixes:

- `TestReadinessE02bFailedTerminalWritesRollBack` (reject/fail + retry)
- `TestReadinessE02cFailedReconciliationRollsBack`
- `TestReadinessE02dFailedExplicitSwitchRollsBack`
- `TestReadinessE18ProjectionMatchesCommittedCheckpoint`
- `TestReadinessH08ArchivedTerminalHistoryIsListable`
- `TestReadinessH09TaskHistoryReachesArchivedWork`

## 7. Results by domain

### A. Identity and lifecycle

Strong. RA-A01 preserves one identity across six turns and three roles with
exactly one creation event. RA-A02 now preserves identity for an exact repeated
objective in the same role; RA-A02b is the explicit-continuation control;
RA-A03 refuses an incompatible contract and starts a different objective clean.
RA-A08 archives a completed task with its objective, proof, and timeline before
the next objective starts. RA-A04/A05/A09–A12 hold.

### B. Context and evidence

RB-B01–B15 pass: facts require current evidence, inconclusive or contradicting
records cannot promote, duplicate evidence collapses, invalidation is
order-independent, briefings are bounded and role-isolated, and the state
survives restart. RI-I01 confirms proof provenance.

### C. Role transition and authority

RC-C01’s matrix is exact. RC-C02 and RC-C05 now show that a task cannot even
record a role the live gate does not hold: the claim is refused and the metadata
stays truthful. RC-C04 shows the real runtime denies ASK/FUZZ writes and permits
BUILD. RC-C03/C07/C08–C18 show that only the host switch moves authority and a
failed switch is recorded as FAILED. RC-C06 characterizes the canonical widening
policy: ASK+mutation proposes BUILD, a mixed request proposes nothing, and a pure
investigate request proposes FUZZ.

### D. Handoff crash consistency

RD-D02 passes in a real second process: authority switch and session authority
are restored, APPROVED reconciles to APPLIED, the record is consumed once,
exactly one APPLIED event exists, and no second switch occurs. RD-D03 preserves
the source role when the crash happened before the switch. RD-D01 covers the
recovery matrix and repeated reconciliation.

### E. Persistence and checkpoint integrity

The central ordering invariant holds and is now stricter: the checkpoint is
written first, so a failed commit cannot be announced by `task.md` (RE-E01,
RE-E18). Handoff approval, application, rejection, failure, explicit switch
recording, and recovery reconciliation all roll back on a failed write
(RE-E02/E03/E02b/E02c/E02d). RE-E04 refuses a newer checkpoint without rewriting
it, recovers a readable projection from a corrupt checkpoint, and classifies a
v1 checkpoint as NEEDS_HUMAN. RE-E05/RE-E06 validate completion and failed
completion across real processes.

### F. Completion and proof

RF-F01 passes the full answered/executed/observed/verified matrix. RF-F02/RE-E05
show proof survives restart while current and downgrades to
COMPLETED_UNPROVEN after invalidation. RF-F03 shows material contradictions
downgrade and unrelated ones do not. RI-I02b now completes a verification-only
resume by crediting the durable current change while still requiring a current
covering check.

### G. Timeline and lineage

RG-G01 confirms monotonic sequence/time, the bounded log, creation anchor
retention, visible sequence gaps, and no lifecycle/authority change from
timeline truncation. Existing lineage tests pass.

### H. Recovery and archive

RH-H01 exercises INTACT, RECONSTRUCTED, NEEDS_HUMAN, and BLOCKED from the actual
restored state, including named missing artifacts. RH-H02 runs three
archive/resume cycles without duplication and surfaces a corrupt archive as an
`ArchiveProblem`. RH-H08 proves the completed archive is enumerable with its
proof and timeline, and RH-H09 proves `/task history` and `/task show` reach it
from the UI.

### I. End-to-end scenarios

| Scenario | Result | Evidence |
| --- | --- | --- |
| 1 — Understand, investigate, implement, verify, restart | PASS | RI-I01, RI-I01b |
| 2 — Interrupted implementation, including verification-only resume | PASS | RI-I02, RI-I02b, RI-I02c |
| 3 — Failed persistence (completion and every handoff transition) | PASS | RE-E01, RE-E02, E02b–E02d, RE-E06 |
| 4 — Crash during role handoff | PASS | RD-D02, RD-D03 |
| 5 — Stale evidence after successful completion | PASS | RF-F02, RF-F03 |
| 6 — Context continuity across many turns | PASS | RB-B05, B03, B04 |
| 7 — Different objective and identity isolation | PASS | RI-I07, RA-A03 |
| 8 — Archive and recovery cycle, plus history access | PASS | RH-H02, RH-H08, RH-H09 |

## 8. Persistence and crash consistency

- The checkpoint is the commit point and is written before the projection. A
  failed checkpoint returns an error and leaves both the checkpoint and the
  projection at their prior state; a projection written after a successful
  checkpoint is best-effort and refreshed by the next checkpoint.
- `Load` takes status, contract, objective, timeline, handoff, and proof from the
  checkpoint. The contract now also supplies the objective even when a stale
  projection carries a different one.
- Every handoff disposition and the explicit-switch recorder use the same
  snapshot/commit/restore shape as `Begin` and `CompleteWithEvidence`.
- The switch-before-APPLIED window is genuinely handled across process
  boundaries with the real session authority.
- Recovery reconciliation rolls back if its write fails, so the next boundary
  reconciles the still-APPROVED record again instead of trusting an uncommitted
  APPLIED claim.

## 9. Authority and role-transition results

- The authority matrix is exact: ASK read/search/inspect; FUZZ adds execution;
  BUILD is the sole mutation authority; unresolved authority denies everything.
- Runtime denial is real, not cosmetic: ASK/FUZZ write calls fail before the
  tool executes and the file is not created.
- Task metadata, role context, a BUILD plan, or a stale recorded role does not
  grant authority. A host that claims a role the live gate does not hold is
  refused at the recording boundary, so the metadata cannot lie to recovery.
- A proposal and an approval do not move authority; only the host switch does.
- Recovery reconciliation never switches authority and marks a recovered APPLIED
  record consumed.

## 10. Evidence and completion-proof results

- Answered-only output cannot prove an execution or mutation objective.
- A mutation proof requires an observed change and a current verification that
  followed it, with the contract’s check and scope.
- Execution proof requires a projected successful execution.
- A failed or inconclusive check cannot produce PROVEN.
- Proof survives restart while its evidence is current and downgrades to
  COMPLETED_UNPROVEN when the proof’s evidence is invalidated.
- A resumed verification-only turn may prove a durable change a prior run
  observed, but only when the new check follows the change and covers the
  contract. A read-only resume or an unrelated check still cannot complete.

The boundary is explicit: `PROVEN` means the recorded change and check passed
under recorded conditions; it does not establish semantic correctness.

## 11. Performance and resource observations

No benchmarks were added. Observed wall-clock ranges on this machine:

- Component readiness suite: ≈ 0.4 s.
- Runtime readiness suite: ≈ 3 s (includes two real `go test ./tools` runs).
- Process readiness suite: ≈ 0.3 s plus subprocess startup.
- Full readiness suite, race: ≈ 16 s.
- Full repository suite: ≈ 30 s.

Context and timeline bounds were exercised repeatedly (6000-char briefing,
`MaxTimelineEvents`, evidence dedup, bounded archive listing) without unbounded
growth. These are functional observations, not a performance benchmark.

## 12. Risks, accepted limitations, and untested areas

Accepted product boundaries (not defects):

- `PROVEN` reports the recorded change and verification, not semantic
  correctness.
- The task contract is derived from the user’s text; the model cannot expand it.
- Direct ASK→BUILD widening is canonical. The staged ASK→FUZZ→BUILD path uses
  explicit `/fuzz` and `/build` commands; an approval prompt is not required to
  take the intermediate step.
- Lineage preserves references; it does not prove an external path still
  exists.
- In-flight tool calls are not journaled; recovery reconstructs the durable
  task, not the session’s in-progress call.
- The timeline is bounded and elides old observations first; sequence gaps are
  reported.
- The workspace summary in a handoff is diagnostic, not a merge/rollback.
- A recovery result must be judged by the restored state, not only its label.

Environment-dependent or untested:

| Area | Reason | Status |
| --- | --- | --- |
| Real v2 binary reading a v3 checkpoint | No old binary was built; only the reader’s version refusal mechanism was exercised. | NOT TESTED |
| Semantic correctness of a change | The evaluator proves recorded change + check, not intent correctness. | INCONCLUSIVE (documented boundary) |
| Filesystem existence of `cognition:`/path artifacts after restart | Lineage is a reference index, not a filesystem proof. | NOT TESTED |
| In-flight tool operation replay | Tool calls are not journaled; the session store owns them. | NOT TESTED |
| Live provider/model integration | The suite uses scripted streams only. | NOT TESTED |
| Concurrent edits outside the task | The recovery service owns them. | NOT TESTED |
| Failed-persistence test on a root runner | Directory-permission fault is bypassed by root; the process test skips. | ENVIRONMENT-DEPENDENT |
| Projection write failure after a committed checkpoint | The next checkpoint refreshes the projection; every filesystem failure mode was not enumerated. | PARTIAL (documented boundary) |

## 13. Git status

No commit was created. The worktree contains the production fixes, the updated
existing tests, the readiness suite, and this documentation:

```text
 M cmd/ohgo/handoff_recovery_test.go
 M cmd/ohgo/taskcontinuity.go
 M cmd/ohgo/taskcontinuity_test.go
 M docs/architecture/TASK_CONTINUITY.md
 M internal/agent/completion.go
 M internal/continuity/task/completion.go
 M internal/continuity/task/controller.go
 M internal/continuity/task/handoff.go
 M internal/continuity/task/handoff_test.go
 M internal/continuity/task/postfreeze_test.go
 M internal/continuity/task/progress.go
 M internal/tui/app.go
 M internal/tui/completion.go
 M internal/tui/completion_test.go
 M internal/tui/postfreeze_test.go
 M internal/tui/task.go
 M internal/tui/task_continuity_test.go
 M internal/tui/task_test.go
?? cmd/ohgo/readiness_process_test.go
?? docs/testing/
?? internal/continuity/task/readiness_component_test.go
?? internal/tui/readiness_process_test.go
?? internal/tui/readiness_runtime_test.go
```

## 14. Exact commands and verification output

All commands were run from the repository root.

```text
$ go version
go version go1.27.3 darwin/arm64

$ git rev-parse HEAD
624aef05c136d198bb832150b65f78c0a87fffdf

$ gofmt -l .
(empty)                                                     exit 0
$ go build ./...
(no output)                                                 exit 0
$ go vet ./...
(no output)                                                 exit 0
$ golangci-lint run ./...
0 issues.                                                   exit 0

$ go test -count=1 -skip '^TestReadiness' ./...
(all packages ok)                                           exit 0

$ go test -count=1 -run '^TestReadiness' \
    ./internal/continuity/task/ ./internal/tui/ ./cmd/ohgo/
ok  internal/continuity/task    0.393s
ok  internal/tui                2.995s
ok  cmd/ohgo                    0.342s                      exit 0

$ go test -race -count=1 -run '^TestReadiness' \
    ./internal/continuity/task/ ./internal/tui/ ./cmd/ohgo/
ok  internal/continuity/task    3.363s
ok  internal/tui                6.800s
ok  cmd/ohgo                    5.711s                      exit 0

$ go test -count=1 ./...
(all packages ok)                                           exit 0
```

Result files:

- `results/environment.txt`
- `results/readiness-go-test.json`
- `results/readiness-verbose.txt`
- `results/readiness-race.txt`
- `results/existing-suite.txt`
- `results/full-suite-with-readiness.txt`
- `results/build-vet-fmt-lint.txt`

## 15. Follow-up opportunities (not release blockers)

1. Build a real previous-release binary to execute the v2→v3 downgrade barrier.
2. Consider surfacing a failed post-commit projection write as a bounded
   diagnostic instead of silently deferring to the next checkpoint.
3. Extend the archive history view with retention or export once product
   requirements exist; the current surface is deliberately read-only.
4. Keep the readiness suite in CI as the regression gate for task continuity.

## 16. Final release decision

**READY FOR PRODUCT RELEASE.**

The P0 data-loss path is closed, task identity no longer fragments on an
ordinary repeated turn, every persistence boundary now commits or rolls back as
one unit, completion proof can consume a durable prior change without weakening
the evidence rule, and the human projection cannot announce a state the
checkpoint never committed. The full repository suite and the readiness suite,
including race execution, are green, and no production package, ownership
boundary, store, or authority path was added.
