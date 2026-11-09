# Minimum Sufficient Path: Adaptive Runtime Repair

> **Status:** implemented and verified.
> **Scope:** investigation depth, evidence truthfulness, phase completion,
> BUILD/FUZZ cooperation, and the runtime facts already owned by the engine.
> **Non-goals:** a second orchestrator, an LLM judge, a planner, model-specific
> heuristics, or a fixed exploration recipe.

## 1. Problem statement

Ohgo already has a deterministic request contract, a task shape, an evidence
ledger, a projector, and one completion evaluator. The remaining defects are
not missing intelligence; they are places where the runtime's current decision
does not match the evidence it already holds.

The repair must not make Ohgo explore more. It must make the runtime stop,
escalate, complete, or hand off at the point the current evidence justifies.

## 2. Verified findings

### A. Runtime problems

**A1. The workspace probe is not visible to the model.**

`cognition.ProbeFacts` already deterministically reads root markers, language
presence, and root entry count. Those facts filter the tool surface but are not
part of the runtime state block. The model can spend a tool call rediscovering
language and manifest facts the engine already knows.

- Root cause: `RenderState` renders the shape but not `RepositoryFacts`.
- Desired invariant: deterministic, trusted workspace facts are surfaced
  before the model spends a capability on them.
- Fix: add a bounded `Workspace probe` line to the state block.

### B. Orchestration problems

**B1. A repeated observation closes the action surface even when required
evidence is still missing.**

`Projector.Project` sets `stopped` whenever an informational turn observed the
repository but added no novel fact, and `withholdExploration` removes every
tool. Sufficiency may still be `INSUFFICIENT` with declared requirements
missing. The next request is forced into an answer the completion boundary must
then reject, instead of allowing targeted escalation.

- Root cause: the stop is derived from novelty alone, not from sufficiency.
- Desired invariant: the surface closes when the contract's evidence is
  sufficient; a no-gain turn with missing evidence escalates with the exact
  missing requirement and keeps the capability needed to close it.
- Fix: remove the novelty-only stop; use the existing stage-based closure;
  append a bounded escalation reason and the existing `Next evidence` guidance
  when a turn gains nothing while requirements remain.

**B2. A lower-authority phase is projected through the full objective.**

The TUI and headless paths build the task shape from the persisted contract,
even when `workspace.PhaseContract` says the current authority can only perform
an informational phase. A deferred execution under ASK therefore asks the
projector for `execution_result`; a deferred mutation under FUZZ can ask for
change-shaped evidence. The model cannot perform those actions, the projector
reports insufficient evidence, and `EvidenceReview` turns a legitimate phase
answer into `NEEDS_REVIEW`. A staged conditional mutation also carries the
future change target into the investigation phase, so a target that does not
exist yet is reported as a missing observation.

- Root cause: the shape is built from the full contract, not the contract the
  active authority can satisfy; conditional phase targets are not distinguished
  from current evidence.
- Desired invariant: a turn is projected against the evidence the current
  authority can actually acquire; the persisted objective and its future
  targets remain unchanged for the later branch.
- Fix: build the projection shape from `workspace.PhaseContract` when deferred;
  mark the shape as a phase; defer literal target-state requirements for a
  conditional mutation until the change branch. Exclude execute-class tools
  when the shape's authority forbids execution, so the model is not offered a
  capability the gate will deny.

### C. Evidence/provenance problems

**C1. File absence counts as repository content.**

`EvidenceState.hasRepositoryOverview` accepts any `file:` item. A failed read
of a missing path records such an item with no value (`addError`), so an
unobserved repository question can become answer-eligible.

- Root cause: absence (target state) and content (repository overview) are
  conflated by the prefix check.
- Desired invariant: overview evidence must carry content; absence proves only
  target state.
- Fix: require an exact file observation with a non-empty value, or a
  content-bearing search/structural result, with a usable status.

**C2. A resolved review warning survives completion and resume.**

`Task.ReviewReason` is durable, but `Task.Complete` never clears it, and the
task view/TUI project it whenever non-empty. A task that was reviewed, resumed,
and later completed still reports "needs review", and a RUNNING resumed task
shows the previous review as current.

- Root cause: the warning is treated as a permanent task property rather than
  the current review state.
- Desired invariant: a warning describes the current evidence state. Completion
  clears the resolved warning; outside `NEEDS_REVIEW` it is history, not a
  current warning.
- Fix: clear on completion; normalize completed checkpoints on load; project it
  in task context and TUI surfaces only while the task is in review.
- Existing provenance recomputation (`EvidenceState.Provenance`) already
  subtracts terms corroborated by implementation evidence; add an end-to-end
  regression so a documentation-only warning cannot survive stronger evidence.

### D. Verification problems

**D1. FUZZ findings are stored but not consumable by BUILD.**

The `artifact` tool saves a finding to the session store, but no task or model
context reads it. The handoff records only artifact counts; BUILD sees shared
facts and evidence, not the structured finding. Cross-workspace cooperation is
therefore weaker than the architecture claims.

- Root cause: the artifact sink has one consumer (the session store) and no
  task-context projection.
- Desired invariant: a finding is a proposal, never a fact. BUILD can consume
  it as an active hypothesis; confirmation still requires observed evidence.
- Fix: project recorded findings into the durable task as active hypotheses at
  the turn boundary. Buffer them during the turn (the artifact tool runs on the
  loop goroutine) and drain them through the task layer before completion.
  Preserve the session artifact and all authority boundaries.

### E. Test coverage gaps

- No regression for a no-gain turn that still has missing requirements.
- No regression for absence being mistaken for repository content.
- No regression for review reason clearing after completion or hiding it while
  running.
- No regression for an execution objective under ASK completing its
  informational phase without a false review.
- No regression for a FUZZ finding becoming a BUILD-visible hypothesis.
- No end-to-end provenance regression where stronger implementation evidence
  removes a documentation-only warning.
- No regression that deterministic workspace probe facts are in the state block.

## 3. Implementation

### 3.1 Cognition

- `internal/control/cognition/project.go`
  - remove the novelty-only `stopped` field and `withholdExploration` call;
  - when the last turn added no fact while `suff.State != SUFFICIENT` and
    `suff.Missing` is non-empty, append one bounded escalation reason and use
    the existing `Next evidence` guidance. Keep tools.
- `internal/control/cognition/candidates.go`
  - close an informational surface at `StageAnswer` when the shape declares
    required knowledge, not only when `RepositoryScope` is true;
  - withhold execute-class tools when `shape.Authority.ExecutionAllowed` is
    false.
- `internal/control/cognition/evidence.go`
  - make `hasRepositoryOverview` require content-bearing observations.
- `internal/control/cognition/render.go`
  - render a bounded `Workspace probe` line from `RepositoryFacts`.
- `internal/control/cognition/shape.go`
  - add the explicit phase marker and defer a conditional mutation's literal
    target-state requirements to the change branch;
  - a path-scoped mutation does not also demand a separate symbol-coordinate
    lookup: the path is the edit boundary and completion re-resolves the
    symbol after the change.
- `internal/control/cognition/project.go`
  - align `AnswerEligible` with the same declared-requirement boundary through
    the shared `informationalEvidenceBoundary` predicate.

### 3.2 Task continuity

- `internal/continuity/task/task.go`
  - clear `ReviewReason` on a successful completion transition.
- `internal/continuity/task/progress.go` / `archive.go`
  - normalize completed checkpoints so a stale reason cannot be restored.
- `internal/continuity/task/view.go`
  - include `Review reason` only while the task is `NEEDS_REVIEW`.
- `internal/continuity/task/artifact.go` (new file in the existing owner)
  - `Controller.RecordArtifact`: a finding becomes an active hypothesis with a
    bounded description; it never becomes a fact or evidence by itself.
  - record the artifact reference in the origin role's context so a handoff and
    `/task lineage` can trace it.
  - deduplicate by hypothesis text; a terminal/absent task is a no-op.
- `internal/execution/tools/artifact.go`
  - add `AddSink` so the existing session sink and the task projection can both
    observe a recorded artifact; `SetSink` keeps its replacement semantics.
- `cmd/ohgo/taskcontinuity.go`
  - attach a mutex-guarded artifact buffer to the live artifact tool;
  - add `TaskHooks.RecordArtifacts` and drain it at the turn boundary before
    completion.

### 3.3 Host projection

- `internal/tui/app.go`
  - build the projector shape from `workspace.PhaseContract(intent, active)`
    when the authority cannot perform the full objective;
  - drain recorded artifacts after `ObserveTurns` and before the task
    completion transition.
- `cmd/ohgo/outcome.go`
  - apply the same phase selection on the stateless path for consistency.
- `internal/tui/task.go`
  - add the nil-safe `RecordArtifacts` hook.

### 3.4 TUI projection

- `internal/tui/task.go` and `internal/tui/sidebar.go`
  - show `ReviewReason` only while the task status is `NEEDS_REVIEW`.

## 4. Regression matrix

| Requirement | Test |
| --- | --- |
| Simple task stays cheap | existing `TestRepositoryOverviewAtStageAnswerWithholdsAllTools`; new declared-requirement closure test |
| Normal repository analysis stops at enough evidence | `TestProjectorStopsOnceRequiredEvidenceIsKnown` |
| Ambiguous/no-gain task escalates, not closes | rewritten stop test with a duplicate observation and a missing requirement |
| Difficult task can still deepen | broad survey: duplicate read keeps tools, second distinct read reaches answer |
| Deterministic runtime facts | `TestRenderStateCarriesWorkspaceProbe` |
| Provenance reconciliation | `TestAnswerProvenanceClearsAfterImplementationCorroboration` |
| Absence is not content | `TestRepositoryOverviewRequiresContentEvidence` absent-file case |
| BUILD/FUZZ cooperation | controller artifact projection + `attachArtifactSink` drain test |
| Completion truthfulness | review reason cleared on completion; hidden outside review; completed checkpoint normalization |
| Phase completion under lower authority | execution objective under ASK stays ACTIVE, not NEEDS_REVIEW |
| Path-scoped mutation is not over-gated | real runtime mutation/verification completes without a redundant symbol lookup; `TestPhase3ProductionMutationCompletionThroughRuntime` and the verification continuation routes |
| Existing historical regressions | focused package suites plus full gates |

## 5. Invariants that must remain true

1. ASK remains read-only; FUZZ may read/execute but never mutate; BUILD is the
   only mutation authority.
2. A finding is a proposal. It cannot become a fact without a current,
   supporting runtime observation.
3. The completion evaluator remains the only completion decision; the projector
   and task layer may veto, never grant.
4. No second repository index, task store, or workflow engine is introduced.
5. Simple tasks do not pay for a broad survey; difficult tasks are not forced
   through a shallow stop.
6. A warning is current state, not an immutable historical label.
7. The persisted objective and authority are unchanged by projection choices.

## 6. Verification

Focused first:

```sh
go test ./internal/control/cognition ./internal/continuity/task ./internal/execution/tools ./internal/tui ./cmd/ohgo
go test -race ./internal/control/cognition ./internal/continuity/task ./internal/tui
```

Then the repository gates:

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./...
go test -race ./...
golangci-lint run ./...
```

## 7. Risks and rejected alternatives

- **Rejected:** a fixed minimum/maximum exploration budget. Depth follows
  missing evidence, not a constant.
- **Rejected:** refusing every repeated successful call. A re-read after an
  external change is legitimate; the repair escalates instead of assuming the
  model is wrong.
- **Rejected:** recording a finding as evidence or a fact. That would promote a
  model claim; an active hypothesis is the truthful epistemic state.
- **Risk:** another host may call `Candidates` with a shape it built from the
  full contract. The phase selection lives at the host boundary because
  `cognition` does not import `workspace`; tests cover the production hosts.
- **Risk:** an artifact sink failure after the session write could leave the
  session artifact and a missing task hypothesis. The turn-boundary drain
  reports the failure rather than silently claiming continuity.

## 8. Implementation record

The repair landed in the existing owners:

- `internal/control/cognition`: evidence-aware escalation instead of the
  novelty-only stop; content-bearing repository overview; phase marker and
  phase target deferral; path-scoped coordinate efficiency; execution-tool
  withholding under a read-only authority; workspace probe in the state block.
- `internal/continuity/task`: resolved review reason cleared on completion and
  normalized on load/archive; `RecordArtifact` projects a finding as an active
  hypothesis and a role reference.
- `internal/execution/tools`: additive artifact observers.
- `internal/tui` / `cmd/ohgo`: phase-contract projection on both hosts; artifact
  buffer drained at the turn boundary; review reason only rendered while
  `NEEDS_REVIEW`.

Regression coverage added or strengthened: stop/escalation, declared-target
closure, absence-vs-content overview, execution withholding under ASK, phase
completion under ASK, conditional phase target deferral through the existing
runtime scenario, FUZZ finding to BUILD hypothesis, review clearing and
surface hiding, provenance corroboration, workspace probe, and artifact
observer delivery.

Verification executed from the repository root:

```text
gofmt -l .                                      empty
go build ./...                                  pass
go vet ./...                                    pass
go test -count=1 ./...                          pass
go test -race -count=1 ./...                    pass
golangci-lint run ./...                         0 issues
```

Mutation checks (temporarily breaking the mechanism and observing the intended
failure) covered: absence accepted as content, stale review retention, finding
not projected as a hypothesis, full-objective shape under ASK, and the
novelty-only tool stop. Each restored mechanism is green again.
