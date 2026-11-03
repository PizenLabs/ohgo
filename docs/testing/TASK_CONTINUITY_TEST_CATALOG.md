# Task Continuity — Product Readiness Test Catalog

> **Scope:** Phase 1–3 Task Continuity in `internal/continuity/task` and its
> runtime integration in `internal/agent`, `internal/tui`, and `cmd/ohgo`.
> **Suite entrypoints:** `internal/continuity/task/readiness_component_test.go`,
> `internal/tui/readiness_runtime_test.go`,
> `internal/tui/readiness_process_test.go`,
> `cmd/ohgo/readiness_process_test.go`.
> **Report:** [`TASK_CONTINUITY_PRODUCT_READINESS.md`](TASK_CONTINUITY_PRODUCT_READINESS.md).

## How to read this catalog

| Field | Meaning |
| --- | --- |
| ID | Stable test identifier used by the report and findings. |
| Level | `A` component/contract, `B` runtime integration, `C` process boundary. |
| Entrypoint | The Go test that implements the case, or the existing regression it relies on. |
| Status | `PASS`, `FAIL`, `NOT TESTED`, or `INCONCLUSIVE`. Only executed outcomes are PASS/FAIL. |
| Severity | Set when the case fails. P0–P3 per the workstream contract. |

This catalog now records the **corrected** contract. The defects found by the
product-readiness workstream were repaired under the BUILD authority and every
row that previously failed is now a passing regression. A regression that can no
longer fail is not evidence, so each repaired row cites the production change it
defends and the failure mode it would catch again.

## Commands

```sh
# All Task Continuity readiness tests (expected: exit 0)
go test -count=1 -run '^TestReadiness' ./internal/continuity/task/ ./internal/tui/ ./cmd/ohgo/

# One domain (replace X with A..I)
go test -count=1 -run 'TestReadinessX' ./internal/continuity/task/ ./internal/tui/ ./cmd/ohgo/

# End-to-end scenarios only
go test -count=1 -run 'TestReadinessI' ./internal/tui/ ./cmd/ohgo/

# Process/restart/crash boundary only
go test -count=1 -run 'TestReadiness(D0[23]|E0[56]|I01b|I02c|A05)' ./internal/tui/ ./cmd/ohgo/

# Race execution
go test -race -count=1 -run '^TestReadiness' ./internal/continuity/task/ ./internal/tui/ ./cmd/ohgo/
```

## Summary

| Result | Top-level tests | Including subtests |
| --- | ---: | ---: |
| PASS | 46 | 79 |
| FAIL | 0 | 0 |
| SKIP (inert subprocess helpers) | 2 | 2 |
| NOT TESTED / INCONCLUSIVE | see §Untested | — |

Top-level counts are the 48 `TestReadiness*` functions found by
`go test -list`; two of them are inert subprocess entrypoints that skip when no
child mode is requested. Subtests include the table-driven cases below.

## A. Task identity and lifecycle

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RA-A01 | A | Six turns with one contract across ASK/FUZZ/BUILD preserve one identity and one creation event. | `TestReadinessA01TaskIdentityAcrossManyTurns` | Same ID; all role contexts retained. | Same ID, one creation event, three role contexts. | PASS | — |
| RA-A02 | B | Two same-role turns with the same objective text preserve the task identity. | `TestReadinessA02SameObjectiveSameRoleTurnsPreserveIdentity` | Same ID; one creation event. | Same ID, one creation event. Defends the `objectiveContinuation` rule in `internal/tui/task.go`. | PASS | — |
| RA-A02b | B | Control: an explicit continuation prompt attaches the next turn. | `TestReadinessA02bExplicitContinuationPreservesIdentity` | Same ID. | Same ID. | PASS | — |
| RA-A03 | A | An incompatible objective is refused; a genuinely different objective starts clean. | `TestReadinessA03DifferentObjectiveDoesNotInheritContext` | `ErrObjectiveConflict`; B has no A facts/evidence; A resumable. | As expected. | PASS | — |
| RA-A04 | A | Invalid lifecycle moves are rejected; terminal states absorb; repeated pause is timeline-idempotent. | `TestReadinessA04LifecycleTransitionMatrix` | Errors for invalid moves; one pause event. | As expected. | PASS | — |
| RA-A05 | B | Task identity is independent of session identity. | `TestReadinessA05TaskIdentityOutlivesSessionIdentity` | A second session does not change the task ID. | Same task ID. | PASS | — |
| RA-A06 | B | A role change preserves identity and role contexts. | Existing `TestTaskIdentitySurvivesRoleChangeAndRoleContextsStayIsolated`, `TestHandoffThroughTurnEntrypointPreservesTaskAndAuthority` | Same ID. | Existing suite green. | PASS | — |
| RA-A07 | A | Incompatible objectives do not silently reuse the old task. | RA-A03 | Refused. | As expected. | PASS | — |
| RA-A08 | B | Starting a new objective while a terminal task is live archives the terminal task first. | `TestReadinessA08CompletedTaskIsPreservedBeforeANewObjective` | Completed task present in `.ohgo/tasks` with objective, proof, and timeline. | Archived with the full continuity record. Defends the terminal `Detach` branch in `runTurn`. | PASS | — |
| RA-A09 | A | Terminal lifecycle states remain terminal. | RA-A04 | Every different move rejected. | As expected. | PASS | — |
| RA-A10 | A | Repeated lifecycle operations are idempotent where specified. | RA-A04, existing `TestRepeatedCheckpointIsIdempotent` | No duplicate transition event. | As expected. | PASS | — |
| RA-A11 | A | Invalid lifecycle transitions are rejected. | RA-A04 | `ErrInvalidTransition`. | As expected. | PASS | — |
| RA-A12 | B | Task identity is not session identity. | RA-A05 | Independent. | As expected. | PASS | — |

## B. Task context and evidence projection

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RB-B01 | A | Facts require current usable evidence; inconclusive/contradicting evidence cannot promote; duplicates collapse. | `TestReadinessB01EvidencePromotionRules` | Rejections; one deduplicated record. | As expected. | PASS | — |
| RB-B02 | A | Invalidating one support reevaluates the fact, order-independently. | `TestReadinessB02InvalidationIsOrderIndependent` | Active until all support gone; then invalidated. | As expected. | PASS | — |
| RB-B03 | A | Role briefings contain shared facts plus their own context only; bounded. | `TestReadinessB03RoleContextIsolationAndBounds` | No cross-role private plan; ≤ 6000 chars. | As expected. | PASS | — |
| RB-B04 | A | Under budget pressure an older role-referenced fact is retained and omissions are reported. | `TestReadinessB04ContextKeepsRoleRelevantOlderFacts` | Older fact retained; `(+…)` marker. | As expected. | PASS | — |
| RB-B05 | A | Facts, hypotheses, questions, artifacts, invalidations, and dedup survive restart; role isolation holds. | `TestReadinessB05ContextContinuityAcrossRestart` | Restored state plus role-scoped briefings. | As expected. | PASS | — |
| RB-B06 | B | Completed runtime turns are observed through the production path. | `TestReadinessI01…`, `TestReadinessI02…`; existing `TestObserveTurnsPopulatesSharedAndRoleContext` | Evidence projected from real tool calls. | Change and check evidence present. | PASS | — |
| RB-B07 | B | Evidence references preserve provenance. | `TestReadinessI01…` | Proof evidence has TaskID, role, source, artifact. | As expected. | PASS | — |
| RB-B08 | A | Transient/diagnostic observations do not become facts. | RB-B01; existing `TestProjectionSkipsTransientAndKeepsProvenance` | No promotion. | As expected. | PASS | — |
| RB-B09 | A | Successful verification creates evidence only for the observed check and scope. | `TestReadinessF01…` (scope mismatch covered by existing `TestVerificationMustCoverContractScope`) | Scope/check must match. | As expected. | PASS | — |
| RB-B10 | A | Contradictory evidence does not rewrite a fact. | RB-B01; existing `TestContradictoryEvidenceDoesNotOverwriteFacts` | Fact retained. | As expected. | PASS | — |
| RB-B11 | A | Superseded/invalid evidence is not current support. | Existing `TestCurrentEvidenceAssemblyExcludesStaleRecords`; RB-B05 | Excluded. | Existing suite green. | PASS | — |
| RB-B12 | A | Duplicate evidence does not accumulate indefinitely. | RB-B01 | One record per dedup key. | As expected. | PASS | — |
| RB-B13 | A | Context selection prioritises the active role. | RB-B04 | Older referenced fact kept. | As expected. | PASS | — |
| RB-B14 | A | Old facts do not disappear merely because newer turns exist. | RB-B04 | Retained. | As expected. | PASS | — |
| RB-B15 | A | Role-specific context is isolated. | RB-B03, RB-B05 | No leakage. | As expected. | PASS | — |

## C. Role transition and authority

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RC-C01 | A | Authority policy matrix: ASK read-only, FUZZ exec-only, BUILD mutation. | `TestReadinessC01AuthorityPolicyMatrix` | Exact allow/deny table. | As expected. | PASS | — |
| RC-C02 | A | A task metadata claim cannot move the recorded role past the live gate. | `TestReadinessC02TaskMetadataCannotGrantAuthority` | Claim refused; role stays ASK; live gate still denies write. | Refused; role stayed ASK; write denied. Defends the live-role verification in `RecordWorkspaceSwitch`. | PASS | — |
| RC-C03 | A | Handoff state machine: propose/approve/reject/fail/apply/replay/terminal. | `TestReadinessC03HandoffDispositionMatrix` | No authority move before apply; replay refused. | As expected. | PASS | — |
| RC-C04 | B | ASK and FUZZ cannot mutate through the real runtime; BUILD can. | `TestReadinessC04AskAndFuzzCannotMutateThroughRuntime` | File absent for ASK/FUZZ, present for BUILD. | As expected. | PASS | — |
| RC-C05 | B | A stale task role claim cannot become runtime authority. | `TestReadinessC05StaleTaskRoleDoesNotGrantRuntimeAuthority` | Claim refused; write denied under live FUZZ. | Refused; metadata and live gate stayed FUZZ. | PASS | — |
| RC-C06 | A | Widening policy shape for the canonical scenario. | `TestReadinessC06WideningPolicyShape` | ASK+mutation → BUILD; mixed → no proposal; pure investigate → FUZZ. | As expected. Direct widening is the canonical policy; a staged ASK→FUZZ→BUILD path is available with explicit `/fuzz` and `/build` commands. | PASS | — |
| RC-C07 | B | A failed authority switch fails the handoff and never reports APPLIED. | `TestReadinessC07FailedSwitchDoesNotReportApplied` | Role unchanged; handoff FAILED. | As expected. | PASS | — |
| RC-C08 | A | Proposal does not change authority. | RC-C03 | No move. | As expected. | PASS | — |
| RC-C09 | A | Approval alone does not change authority. | RC-C03 | No move. | As expected. | PASS | — |
| RC-C10 | B | Only the workspace switch changes authority; accepted handoff applies after it. | RI-I01; existing `TestHandoffThroughTurnEntrypointPreservesTaskAndAuthority` | Role changes only after `Switch`. | As expected. | PASS | — |
| RC-C11 | B | Rejection leaves authority unchanged. | Existing `TestDeclinedHandoffLeavesAuthorityAndTaskUnchanged`; RC-C03 | No move. | Existing suite green. | PASS | — |
| RC-C12 | B | A failed switch does not report APPLIED. | RC-C07 | FAILED. | As expected. | PASS | — |
| RC-C13 | A | Handoff source role comes from live authority, not stale metadata. | Existing `TestProposeHandoffUsesLiveAuthorityAsSource` | Live role used. | Existing suite green. | PASS | — |
| RC-C14 | B | Target role receives handoff context; source context preserved. | RI-I01 | All role contexts present; BUILD has handoff note. | As expected. | PASS | — |
| RC-C15 | A | Handoff references are bounded and validated. | Existing `TestHandoffPreservesTaskAndRoleContext`, `TestHandoffCarriesCitedEvidenceBeforeNewerRecords` | Bounded. | Existing suite green. | PASS | — |
| RC-C16 | A | Replaying a consumed handoff does not apply it twice. | RC-C03 | Refused. | As expected. | PASS | — |
| RC-C17 | A | Unauthorized/terminal transitions are rejected. | RC-C03 terminal subtest | Refused. | As expected. | PASS | — |
| RC-C18 | B | Recovery never creates a new authority path. | RD-D01/D02/D03 | No switch in reconciliation. | As expected. | PASS | — |

## D. Handoff crash consistency

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RD-D01 | A | Recovery matrix: target→APPLIED consumed; source→INTERRUPTED; third role→discrepancy; proposal→INTERRUPTED. | `TestReadinessD01HandoffRecoveryMatrix` | Truthful dispositions; no switch. | As expected. | PASS | — |
| RD-D02 | C | Crash after switch before APPLIED: fresh process restores session authority and reconciles. | `TestReadinessD02CrashAfterSwitchBeforeApplied` | APPLIED, consumed, one event, no second switch. | As expected. | PASS | — |
| RD-D03 | C | Crash after approval before switch: fresh process keeps source role. | `TestReadinessD03CrashAfterApprovalBeforeSwitch` | INTERRUPTED, FUZZ preserved, resumable. | As expected. | PASS | — |
| RD-D04 | A | PROPOSED → REJECTED / APPROVED → FAILED / PROPOSED → INTERRUPTED. | RC-C03, RD-D01 | Terminal, no role change. | As expected. | PASS | — |
| RD-D05 | A | Mismatched live authority is not guessed or silently repaired. | RD-D01 unrelated-role subtest | Discrepancy notice; role untouched. | As expected. | PASS | — |
| RD-D06 | C | Recovery idempotency: a second restart does not duplicate APPLIED. | RD-D02 event count + RD-D01 repeated reconcile | One APPLIED event. | As expected. | PASS | — |

## E. Persistence and checkpoint integrity

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RE-E01 | A | A failed checkpoint commit cannot be announced by the projection. | `TestReadinessE01ProjectionCannotOutrunACommit` | Projection keeps the prior status; restart is PAUSED, no proof, resumable. | Projection did not claim COMPLETED; restart PAUSED with no proof. Defends checkpoint-first ordering in `Store.SaveCheckpoint`. | PASS | — |
| RE-E02 | A | A failed approval write does not publish an uncommitted approval in memory. | `TestReadinessE02FailedApprovalWriteLeavesNoUncommittedApproval` | In-memory remains PROPOSED. | Rolled back to PROPOSED. Defends `snapshotState`/`commitState` in `ApproveHandoff`. | PASS | — |
| RE-E03 | A | A failed APPLIED write does not publish an uncommitted application in memory. | `TestReadinessE03FailedApplyWriteLeavesNoUncommittedApplied` | In-memory remains APPROVED; role remains FUZZ. | Rolled back to APPROVED/FUZZ. | PASS | — |
| RE-E02b | A | Failed reject and fail transitions roll back, and the same transition commits once the write path recovers. | `TestReadinessE02bFailedTerminalWritesRollBack` | PROPOSED after failure; committed disposition on retry. | As expected. | PASS | — |
| RE-E02c | A | Failed recovery reconciliation leaves the loaded APPROVED record untouched. | `TestReadinessE02cFailedReconciliationRollsBack` | ERROR; handoff APPROVED; role FUZZ. | As expected. | PASS | — |
| RE-E02d | A | A failed explicit workspace-switch record rolls back; retry commits. | `TestReadinessE02dFailedExplicitSwitchRollsBack` | Prior role and no handoff after failure; BUILD/APPLIED on retry. | As expected. | PASS | — |
| RE-E04 | A | Newer checkpoint versions are refused without rewrite; malformed checkpoints recover the projection; v1 loads as needs-human. | `TestReadinessE04CheckpointClassificationAndVersionBarrier` | `ErrCheckpointVersion`/`ErrCorruptCheckpoint`; no rewrite. | As expected. | PASS | — |
| RE-E05 | C | A completed checkpoint survives a real process restart. | `TestReadinessE05CleanRestartRestoresProvenCompletion` | Same ID, COMPLETED, PROVEN. | As expected. | PASS | — |
| RE-E06 | C | A failed completion checkpoint in one process is not completed in the next. | `TestReadinessE06FailedPersistenceAcrossProcess` | Non-terminal, resumable. | As expected (chmod fault). | PASS | — |
| RE-E07 | A | Valid checkpoint survives restart; state/session restore consistently. | RE-E05, H01; existing `TestResumeRestoresDurableState` | Restored. | Existing suite green. | PASS | — |
| RE-E08 | A | Failed pause/review/cancel/failure transitions roll back. | Existing `TestFailedLifecyclePersistRollsBack`; RE-E02b | Rollback. | Existing suite green. | PASS | — |
| RE-E09 | A | A newer projection cannot override authoritative checkpoint state. | RE-E01 | Checkpoint wins. | As expected. | PASS | — |
| RE-E10 | A | Repeated checkpoint writes are safe. | Existing `TestRepeatedCheckpointIsIdempotent` | Safe. | Existing suite green. | PASS | — |
| RE-E11 | A | Truncated/malformed data is classified correctly. | RE-E04, H01 | Corrupt/RECONSTRUCTED. | As expected. | PASS | — |
| RE-E12 | A | v1 and v2 compatibility behaves as documented. | RE-E04 v1; existing `TestLegacyCheckpointWithoutContextLoads`, `TestLegacyCompletedCheckpointReportsUnproven` | Loads; unproven completion. | Existing suite green. | PASS | — |
| RE-E13 | — | v3 checkpoint refused by a real v2 binary. | Not available in this worktree. | Refusal before rewrite. | Mechanism tested (`version > current`); real old binary not executed. | NOT TESTED | — |
| RE-E14 | A | Archive restoration does not silently lose evidence/audit. | H02/H08; existing `TestCompletionAndTimelineSurviveArchive` | Preserved. | Existing suite green. | PASS | — |
| RE-E15 | A | Corrupt archives are surfaced as recovery problems. | H02; existing `TestCorruptArchiveIsReported` | `ArchiveProblem`. | As expected. | PASS | — |
| RE-E16 | A | Repeated recovery is idempotent. | H01 | No mutation/duplication. | As expected. | PASS | — |
| RE-E17 | A | Recovery diagnostics describe what was reconstructed/lost/blocked. | H01 | Named missing artifacts. | As expected. | PASS | — |
| RE-E18 | A | A committed completion appears in the projection and survives restart. | `TestReadinessE18ProjectionMatchesCommittedCheckpoint` | COMPLETED/PROVEN in both artifacts. | As expected. | PASS | — |

## F. Completion and proof validity

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RF-F01 | A | Answered, executed, observed, verified, PROVEN, COMPLETED_UNPROVEN distinction. | `TestReadinessF01CompletionProofMatrix` | Exact refusals/acceptance. | As expected. | PASS | — |
| RF-F02 | A | Proof survives restart while current; invalidation downgrades to COMPLETED_UNPROVEN. | `TestReadinessF02ProofCurrentnessAcrossRestart` | PROVEN then downgraded; lifecycle COMPLETED retained. | As expected. | PASS | — |
| RF-F03 | A | Material contradiction downgrades PROVEN; unrelated contradiction does not. | `TestReadinessF03ContradictionScopeAcrossRestart` | Downgrade only for dependent claim. | As expected. | PASS | — |
| RF-F04 | B | A real runtime mutation + verification proves the objective and survives restart. | RI-I01, RI-I01b | PROVEN with provenance. | As expected. | PASS | — |
| RF-F05 | B | Answered-only output cannot prove an execution/mutation objective. | RF-F01; RI-I02 | Refused. | As expected. | PASS | — |
| RF-F06 | B | Successful command execution is projected as execution evidence. | I01 FUZZ phase; existing `TestSuccessfulCommandProjectionIsEvidence` | Evidence recorded. | As expected. | PASS | — |
| RF-F07 | A | Verification must follow the last relevant observed change. | RF-F01 verification-before-change subtest | Refused. | As expected. | PASS | — |
| RF-F08 | A | Verification is tied to the actual check and scope. | RF-F01; existing `TestVerificationMustCoverContractScope` | Refused mismatch. | As expected. | PASS | — |
| RF-F09 | A | Superseded/invalidated evidence cannot support current proof. | RF-F02 | Downgraded. | As expected. | PASS | — |
| RF-F10 | A | Unsupported contract kinds are refused. | RF-F01 | Error. | As expected. | PASS | — |
| RF-F11 | A | Missing required evidence prevents PROVEN. | RF-F01 | Refused. | As expected. | PASS | — |
| RF-F12 | A | A failed check cannot produce PROVEN. | RF-F01 failed-check subtest | Refused. | As expected. | PASS | — |
| RF-F13 | A | Completion persistence failure cannot publish success. | RE-E01, RE-E06; existing `TestFailedCompletionPersistLeavesTaskResumable` | No COMPLETED/PROVEN. | As expected. | PASS | — |
| RF-F14 | A | A completed task without valid proof is not PROVEN. | RF-F02; existing `TestLegacyCompletedCheckpointReportsUnproven` | COMPLETED_UNPROVEN. | Existing suite green. | PASS | — |
| RF-F15 | B | Reopening/resuming does not manufacture proof. | RI-I02, RI-I02c | Non-terminal, no proof. | As expected. | PASS | — |
| RF-F16 | A | Proof remains valid after checkpoint/restart when evidence is current. | RF-F02, RE-E05 | PROVEN. | As expected. | PASS | — |
| RF-F17 | A | Proof becomes invalid when evidence is invalidated after restart. | RF-F02 | COMPLETED_UNPROVEN. | As expected. | PASS | — |
| RF-F18 | B | A durable prior change + a current verification-only resume proves the task. | `TestReadinessI02bResumeWithVerificationOnlyCompletesInterruptedMutation` | PROVEN. | PROVEN without repeating the change. Defends `CompletionInput.PriorChange` and the controller's `HasCurrentChange`. | PASS | — |

## G. Timeline and lineage

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RG-G01 | A | Timeline order, injected clock, bound, creation anchor, gap count, and authority independence. | `TestReadinessG01TimelineOrderAndAuthorityIndependence` | Monotonic, bounded, anchor retained, dropped reported. | As expected. | PASS | — |
| RG-G02 | A | Lifecycle events retain order and provenance across restart. | Existing `TestTimelineRecordsMeaningfulEventsAcrossRestart`; I01 | Ordered. | Existing suite green. | PASS | — |
| RG-G03 | A | Bounded timeline retains lifecycle over observation events. | Existing `TestTimelineBoundKeepsCreationAndReportsDroppedEvents` | Lifecycle retained. | Existing suite green. | PASS | — |
| RG-G04 | A | Duplicate pause/review do not create misleading transitions. | RA-A04; existing `TestPauseAndReviewAreTimelineIdempotent` | One event. | As expected. | PASS | — |
| RG-G05 | A | Evidence-to-hypothesis lineage traverses in the intended direction. | Existing `TestLineageTracesEvidenceToHypothesis`, `TestLineageTracesSupportContradictionAndArtifact` | Traceable. | Existing suite green. | PASS | — |
| RG-G06 | A | Invalidated/superseded relationships are represented. | RF-F02, RF-F03, G05 | Represented. | As expected. | PASS | — |
| RG-G07 | A | Findings retain role and timestamp. | Existing `TestObservationProjectionAttributesOnlyTheExecutingRole` | Retained. | Existing suite green. | PASS | — |
| RG-G08 | A | Timeline corruption is handled safely. | Existing `TestTimelineSanitizeRepairsDamageWithoutDiscardingHistory` | Repaired. | Existing suite green. | PASS | — |
| RG-G09 | A | Timeline never grants authority or changes lifecycle. | RG-G01 | No change. | As expected. | PASS | — |

## H. Recovery and archive

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RH-H01 | A | INTACT / RECONSTRUCTED / NEEDS_HUMAN / BLOCKED classification from actual restored state. | `TestReadinessH01RecoveryClassificationMatrix` | Each state with named detail. | As expected. | PASS | — |
| RH-H02 | A | Archive/resume cycles, no duplication, corrupt archive surfaced. | `TestReadinessH02ArchiveCycleAndCorruption` | Three cycles intact; one `ArchiveProblem`. | As expected. | PASS | — |
| RH-H03 | C | Recovery from a completed checkpoint in a fresh process. | RE-E05 | PROVEN. | As expected. | PASS | — |
| RH-H04 | A | Recovery after proof downgrade. | RF-F02 | COMPLETED_UNPROVEN. | As expected. | PASS | — |
| RH-H05 | A | Live authority and task metadata disagreement. | RD-D01; RC-C02/C05 | Reported or refused, never guessed. | As expected. | PASS | — |
| RH-H06 | A | Missing evidence references / partial archive. | H02; existing `TestArchiveRepairIsReportedOnResume` | Reported. | Existing suite green. | PASS | — |
| RH-H07 | A | Completion proof repair is part of classification. | Existing `TestDroppedProofForcesReconstruction`, `TestLoadRepairsTimelineAndCompletionProof` | RECONSTRUCTED. | Existing suite green. | PASS | — |
| RH-H08 | A | A completed task archived before the next objective is enumerable with its proof and timeline. | `TestReadinessH08ArchivedTerminalHistoryIsListable` | Found in `ArchivedTasks` with COMPLETED, objective, proof, timeline. | As expected. Defends the read-only archive listing. | PASS | — |
| RH-H09 | B | `/task history` lists archived terminal work and `/task show` resolves it after the live task moved on. | `TestReadinessH09TaskHistoryReachesArchivedWork` | Completed task visible and showable; resumable work is not listed as history. | As expected. Defends the UI half of archive-before-overwrite. | PASS | — |

## I. End-to-end product scenarios

| ID | Level | Scenario / contract | Entrypoint | Expected | Actual | Status | Severity |
| --- | --- | --- | --- | --- | --- | --- | --- |
| RI-I01 | B | Understand → investigate → implement → verify → restart. | `TestReadinessI01UnderstandInvestigateImplementVerifyRestart` | Same ID, actual file change, PROVEN, restart intact. | As expected. The explicit ASK selector and `/fuzz` command realize the staged path; direct ASK→BUILD widening is also canonical. | PASS | — |
| RI-I01b | C | The same full BUILD turn across a process restart. | `TestReadinessI01bFullTurnSurvivesProcessRestart` | PROVEN and file change durable. | As expected. | PASS | — |
| RI-I02 | B | Interrupted implementation, restart, continue, complete. | `TestReadinessI02InterruptedImplementationResumesAndCompletes` | Resumable, not falsely complete, then PROVEN after a real continuation. | As expected. | PASS | — |
| RI-I02b | B | A verification-only resume of an interrupted mutation proves the durable change. | `TestReadinessI02bResumeWithVerificationOnlyCompletesInterruptedMutation` | PROVEN. | PROVEN without repeating the change. | PASS | — |
| RI-I02c | C | Interrupted full turn across a process boundary. | `TestReadinessI02cInterruptedFullTurnSurvivesProcessRestart` | Non-terminal, no proof, resumable. | As expected. | PASS | — |
| RI-I03 | B | Failed persistence during a lifecycle transition. | RE-E01, RE-E02, RE-E02b/c/d, RE-E06 | No uncommitted transition presented as committed. | Completion and every handoff transition roll back. | PASS | — |
| RI-I04 | C | Crash during role handoff (switch-before-APPLIED). | RD-D02 | Recovered without a second switch. | As expected. | PASS | — |
| RI-I05 | A | Stale evidence after a successful completion. | RF-F02, RF-F03 | Downgrade to COMPLETED_UNPROVEN. | As expected. | PASS | — |
| RI-I06 | A | Context continuity across many turns and restart. | RB-B05 (plus B03/B04) | Relevant context retained, role-isolated, bounded. | As expected. | PASS | — |
| RI-I07 | B | A different objective does not inherit the prior task's facts or proof; A stays resumable. | `TestReadinessI07DifferentObjectiveIdentityIsolation` | Isolated. | As expected. | PASS | — |
| RI-I08 | A | Archive/resume cycle preserves lifecycle, evidence, timeline, lineage, recovery. | H02 | Preserved across cycles. | As expected. | PASS | — |

## Untested and inconclusive areas

| Area | Why | Status |
| --- | --- | --- |
| Real v2 binary reading a v3 checkpoint | No v2 artifact is built in this worktree; only the reader's `version > current` mechanism is exercised. | NOT TESTED |
| Semantic correctness of a requested change | `PROVEN` means the recorded change and check passed, not that the change is semantically right. This is a documented evaluator boundary. | INCONCLUSIVE (by design) |
| Filesystem artifact existence for `cognition:`/path references | Lineage preserves references; it does not prove a path still exists. | NOT TESTED |
| In-flight tool operation replay across a crash | Tool calls are not journaled; the session store owns them. | NOT TESTED (documented limitation) |
| Live provider/model integration | The suite is deterministic and uses scripted streams only. | NOT TESTED (out of scope) |
| Concurrent edits outside the task | The recovery service, not task continuity, owns reconciliation. | NOT TESTED (out of scope) |
| Third-party hosts that bypass `RecordWorkspaceSwitch` | The controller verifies the live role and the recorded source when a role source is installed; a host with no role source keeps the documented host-only contract. | PARTIAL (documented boundary) |
| Failed-persistence test under a root user | The process test uses directory permissions; a root runner can write anyway and the test skips. | ENVIRONMENT-DEPENDENT |
| Projection write failure after a committed checkpoint | The checkpoint is authoritative and the projection is refreshed by the next checkpoint; the suite asserts the checkpoint wins but does not exhaust every filesystem failure mode. | PARTIAL (documented boundary) |

## Severity legend

| Severity | Meaning |
| --- | --- |
| P0 | Data loss, authority violation, false completion/proof, unrecoverable corruption, critical security failure. |
| P1 | Core continuity, restart, handoff, evidence, or recovery behavior is unreliable. |
| P2 | Meaningful degradation, incomplete recovery, misleading diagnostics, or bounded functionality that fails its contract. |
| P3 | Non-critical usability, observability, documentation, or testability issue. |
