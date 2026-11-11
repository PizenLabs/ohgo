# Task Continuity

> **Scope:** the durable, role-independent task context introduced by Phase 1,
> the controlled role handoff introduced by Phase 2, and the progression,
> lineage, recovery, and evidence-backed completion introduced by Phase 3.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](OHGO_ARCHITECTURE_MAP.md).
> **Permanent rules:** [`OHGO_ARCHITECTURE_RULES.md`](../migration/OHGO_ARCHITECTURE_RULES.md).
> **State ownership:** [`STATE_AND_PERSISTENCE.md`](STATE_AND_PERSISTENCE.md).

A task is what the user is trying to accomplish. It is not a conversation, a
turn, a tool call, or the active role. `internal/continuity/task` owns task
identity, lifecycle, shared context, evidence, facts, hypotheses, questions, and
the per-role execution contexts associated with one task.

```text
One Objective. Continuous Context. Multiple Execution Authorities.
One Verifiable Outcome.
```

Task continuity records state. It never executes, grants authority, changes a
workspace, completes a task, or promotes model output into verified fact.

---

## 1. Ownership

| Fact | Source of truth | Persisted by | Projected by |
| --- | --- | --- | --- |
| Task identity and lifecycle | `task.Task` | `progress.json` | `task.md`, TUI task block |
| Completion contract and pre-run snapshot | `task.Task.Contract` / `.Snapshot` | `progress.json` | `task.md`, TUI task block |
| Verified facts, hypotheses, evidence, questions | `task.Task.Context` | `progress.json` (`context`) | `task.ContextFor` |
| Role-specific execution context | `task.Task.Context.Roles` | `progress.json` (`context.roles`) | `task.ContextFor` |
| Role handoff and its disposition | `task.Task.Handoff` | `progress.json` (`handoff`) | `task.ContextFor`, TUI transition card |
| Task progression timeline | `task.Task.Timeline` | `progress.json` (`timeline`) | `task.ContextFor`, `/task timeline` |
| Completion proof | `task.Task.Completion` | `progress.json` (`completion`) | `task.ObjectiveState`, `/task status` |
| Recovery classification | `task.Store.Load` | never (derived) | task block, `/task status` |
| Transition policy | `workspace.DecideTransition` | never (derived) | TUI decision card |
| Detached tasks | `task.Store` archive | `.ohgo/tasks/<id>.json` | `/task list`, `/task history`, `/task show`, `/task resume` |
| Completion verdict | `agent.Evaluate*Completion` | `task.Status` + proof | TUI verdict |

The task checkpoint is **project-local**, not session-local:

```text
<project>/.ohgo/progress.json   authoritative task checkpoint
<project>/.ohgo/task.md         lossy human/model projection (never authority)
<project>/.ohgo/tasks/<id>.json detached task archive
```

A session is one conversation (`sessions.db`). A task is one objective that may
survive a session, an interruption, a restart, and a role change. A role change
never by itself recreates the task: the same objective continues. An automatic
BUILD ↔ FUZZ handoff is recorded as an applied control-plane transition, so the
same task and evidence continue. A new session keeps the task durably
resumable, an explicit continuation or a natural verification request about the
task's own result re-attaches it, and a genuinely new objective starts a new
task. The task layer resolves that ownership before workspace prediction or
routing. A terminal task owns no continuation: a repeated verification prompt
reports the finished state instead of replacing the proof with a duplicate
task. A terminal task is archived through
the same detach path before the new objective starts, so its objective,
evidence, proof, and timeline remain inspectable through `/task history` rather
than being overwritten by the replacement checkpoint.

**One writer:** `task.Store` writes the live checkpoint and the archive. The
controller derives the checkpoint's context copy from the in-memory task, so a
caller cannot persist a second, divergent representation.

---

## 2. Canonical task state

`Task` carries the stable identity, original objective, lifecycle status,
constraints, coarse phase, execution guidance (todos), next action, creation and
update times, the completion contract, and `SharedContext`. It also carries
`ActiveRole`, the execution role it last ran under; that is continuity metadata,
never authority, and it is additive to the checkpoint.

`SharedContext` is additive to the checkpoint and carries its own schema
version. It contains:

- **Facts** — statements supported by current evidence.
- **Hypotheses** — proposals and their dispositions.
- **Evidence** — task-linked references to observations.
- **Roles** — ASK, FUZZ, and BUILD execution contexts.
- **Questions** — outstanding and answered questions.

The lifecycle vocabulary (`PENDING`, `RUNNING`, `PAUSED`, `NEEDS_REVIEW`,
`COMPLETED`, `FAILED`, `CANCELLED`) is unchanged. Context never sets a status:
a passing test, a recorded fact, or a confirmed hypothesis does not complete the
objective. Completion remains the `agent` evaluator's evidence-backed verdict.

`Task.Timeline` is a bounded, additive index of meaningful events. Each event
carries a sequence number, time, kind, role, resulting status, a bounded
summary, and references. It never stores a tool payload, a conversation
message, or a second copy of a fact. When the bound is reached, the creation
event and the newest events are retained and the sequence gap reports how many
were dropped. `Task.Completion` is the durable proof for a COMPLETED task:
the evaluator verdict, the observed check summaries, the evidence references,
and whether an informational answer was produced.

---

## 3. Epistemic invariants

### Facts

A fact is created only by `RecordFact` or by confirming a hypothesis. Both
require at least one **current**, usable evidence record:

- `validity = current`
- `outcome = observed` or `supporting`
- `kind` is one of tool, command, test, benchmark, source, artifact,
  reproduction, external, or human

A model assertion, a repeated assertion in another role, a hypothesis, and an
inconclusive result cannot satisfy that requirement. A fact whose only support
is invalidated becomes `invalidated`; it is retained, not deleted.

Recording is a host/control-plane API. Phase 1 exposes no model tool that
creates evidence, promotes a fact, confirms a hypothesis, or edits role context.
A capability could propose a hypothesis in a later phase, but it cannot certify
its own claim.

### Hypotheses

A hypothesis starts `active`. It may become `confirmed`, `rejected`,
`superseded`, or `invalidated`. Confirmation requires evidence and records the
confirming role and time. It also creates a fact that names its source
hypothesis, so confirmed knowledge is structurally distinguishable from a
direct observation.

Rejection keeps the reason and the resolving role. Supersession keeps both
records. Contradictory evidence never rewrites a fact: the evidence names the
claim it contradicts, and assembly marks the fact `DISPUTED`.

### Evidence

Evidence stores an identity, owning task, kind, originating role, source,
optional artifact reference, bounded summary, outcome, validity, optional
digest, contradiction targets, and timestamp. It is a reference, not a copied
tool payload. `DedupKey` collapses a repeated observation of the same result.

Outcomes:

| Outcome | Meaning | Can support a fact? |
| --- | --- | --- |
| `observed` | direct observation | yes |
| `supporting` | evidence offered for a claim | yes |
| `contradicting` | evidence against a named claim | no |
| `inconclusive` | incomplete or unusable result | no |

Validity is separate: a current result can later be `superseded` or `invalid`
without rewriting what was observed.

A passing test proves that the recorded test passed under its recorded
conditions. It does not prove the user's objective was achieved.

`cognition.EvidenceState` remains the observation owner; task evidence is the
durable, task-linked reference index. Phase 1 does not copy the payload from one
into the other. At a checkpoint boundary the controller projects each material
observation into task evidence with a bounded summary, provenance, and a
`cognition:<item>` artifact reference; the value itself stays in the evidence
state. Transient output is skipped.

A successful verification observation also creates an evidence-backed fact that
states the recorded check passed under its recorded conditions. It never says
the objective is achieved. A diagnostic, a derived inference, and a conflicting
observation are recorded as `inconclusive` and can never support a fact.
Mutations and command runs are evidence and role progress, not facts.

There is deliberately no runtime signal for a model-authored hypothesis, an open
question, or a role strategy: Phase 1 has no model-facing context tool, and
deriving them from prose would manufacture context. The host may record them
through the controller API when it has a trustworthy source; the gap is
documented rather than filled with an inference.

---

## 4. Role execution context

Each role owns one `RoleContext` inside the shared task:

| Role | Context fields |
| --- | --- |
| ASK | reasoning continuity, references to read-only facts |
| FUZZ | investigation strategy, references to exploratory hypotheses, reproduction progress, experiment/benchmark references |
| BUILD | implementation plan, mutation scope, verification progress, patch references |

Role contexts are isolated: updating one role never overwrites another. They
reference shared facts and hypotheses by ID instead of duplicating them. Only the
active role's context is assembled into the model briefing.

At each checkpoint the host populates the executing role's context from material
observations: ASK gets inspected read references and read-only artifact
references; FUZZ gets command, reproduction, verification, external, and
diagnostic progress plus experiment references; BUILD gets mutation and
verification progress, observed mutation scope, patch references, and its open
todo labels as its plan. A todo is a model proposal, so it is plan context, never
a fact. Semantic reasoning, strategy, and hypotheses still require a host write;
they are not inferred from model prose.

A new task starts with an empty session plan. The todo tool is a session-scoped
owner, so the controller asks it to clear at the new-task boundary before the
new checkpoint can snapshot it; the previous task keeps its own todo snapshot in
its archive. Resuming a different task clears the plan that belonged to the
task being left behind; the resumed task keeps its own checkpoint snapshot. This
keeps a finished plan from being read as, or imported into, the next objective's
progress.

**A role context is not authority.** A BUILD plan or mutation scope in the
record does not permit a write; a FUZZ strategy does not grant execution; an ASK
fact does not grant a read. The workspace gate and the security gate remain the
only authority paths. Storing role context never moves authority; Phase 2 adds a
separate, host-approved handoff record for that (section 8).

---

## 5. Context assembly

`task.ContextFor(task, role, lastAction)` is deterministic for the same
authoritative task state, role, and last action. It assembles, in priority
order:

1. objective and lifecycle status;
2. a continuity warning when context is missing, repaired, or unavailable;
3. the active authority label;
4. the current handoff (roles, disposition, carried counts) when one exists;
5. constraints, todos, current step, next action, and last action;
6. active verified facts (with evidence references and disputes);
7. hypotheses and dispositions;
8. open questions;
9. the active role's execution context;
10. current evidence references;
11. legacy unclassified notes, explicitly labelled as unverified.

Bounds are fixed: 6000 characters and 1500 estimated tokens, reusing
`internal/tokens`. Selection is by relevance under the budget, not by a fixed
recency window: disputed facts and facts the active role references come first,
then the remaining active facts in record order, so an older verified fact is
not dropped merely because a newer one exists. Evidence is current-only;
invalid and superseded records cannot displace the records that still apply.
The active role's section has a reserved share, so a full shared-knowledge
briefing cannot push it out. A section that exceeds the budget reports the
number of omitted items instead of pretending to be complete.

Role context never includes another role's plan, scope, strategy, or notes.

Assembly exposes incomplete continuity rather than fabricating it:

- no resolved role → `Authority: unresolved`;
- no checkpoint while `task.md` exists → a continuity warning;
- unreadable checkpoint → a continuity warning and no structured context;
- repaired records → a warning naming the repair count;
- unsupported context version → the task loads, the context is refused, and the
  reason is reported.

The model cannot change authoritative state by generating a context
representation. It receives text; only an explicit controller write changes the
checkpoint.

---

## 6. Persistence and compatibility

`ProgressVersion` is **3**. Version 2 added the persisted completion contract,
pre-run target snapshot, and review reason; version 3 is a **downgrade
barrier**, not a shape change. Structured context is an additive,
self-versioned subdocument (`ContextVersion = 1`), and `activeRole`, `handoff`,
`timeline`, and `completion` are additive optional fields.

The exact read/write boundary:

| Writer / reader | Result |
| --- | --- |
| New binary reads a version-1 or version-2 checkpoint | Supported. Missing fields are zero values; a completed task with no proof reports `COMPLETED_UNPROVEN`. |
| New binary reads a version-2 checkpoint that carries timeline and proof | Supported; both are restored. |
| Version-2 binary reads a version-3 checkpoint | Refused with `ErrCheckpointVersion`. It never reaches a rewrite. |
| Version-2 binary writes a checkpoint | Only possible for a version-2 file; it cannot load a version-3 file, so it cannot erase the Phase 3 fields. |
| New binary reads a checkpoint written by a version-2 binary | Supported. If that write happened before Phase 3, the timeline and proof are absent and the completion reports `COMPLETED_UNPROVEN`. |

Compatibility is therefore **read-old, refuse-newer**: an older reader may read
older checkpoints, but it must refuse a newer checkpoint rather than silently
rewriting it. `encoding/json` does not preserve unknown fields, so a version
bump is the only reliable protection available in this architecture; there is
no sidecar and no preservation framework.

Further persistence rules:

- a newer reader refuses a `context.version` it does not understand, keeps the
  task, and reports the problem;
- missing context means "not recorded", not "corrupt";
- a malformed context is sanitized on load: invalid records are dropped or
  marked invalidated, valid records are retained, and the repair count is
  surfaced;
- duplicate role contexts are detected across the whole collection, not just
  against the previous record;
- a dangling supersession pointer is cleared, and a repaired confirmed
  hypothesis whose support vanished receives the same invalidation resolution
  as the live path;
- the sanitized context is validated before it is returned; a context that
  still cannot validate is dropped with a warning rather than left in a state
  that would make every later write fail;
- an invalid context is rejected before write, and writes remain atomic
  (temp-then-rename);
- the checkpoint is written first and the projection second. The checkpoint is
  the commit point, so a returned write error means the authoritative transition
  did not land and the controller restores the prior in-memory task. A failed
  checkpoint also leaves `task.md` at its prior state, so the human artifact
  cannot announce a status or proof the checkpoint never committed. A projection
  failure after a committed checkpoint is not a rollback signal; the next
  checkpoint rewrites the projection from the authoritative task.
  `Controller.CompleteWithEvidence` uses the same rollback, which is what makes
  a failed completion resumable instead of falsely published.

`task.md` remains a projection. It does not carry the structured context,
timeline, or proof, so `progress.json` outranks it always. Detached tasks
archive the full task and progress, including context, evidence state, audit,
timeline, and completion proof.

---

## 7. Phase boundary

Implemented in Phase 1:

- stable task identity independent of session and role, including a role change
  on the same objective;
- shared, authoritative task context;
- verified facts and hypotheses with explicit lifecycles;
- task-linked evidence with contradictions and invalidation;
- host projection of material runtime observations into task evidence,
  verification facts, and role-specific progress;
- isolated ASK/FUZZ/BUILD execution contexts;
- persistence and recovery;
- deterministic, bounded, role-scoped context assembly.

Implemented in Phase 2 (handoff):

- a controlled widening transition from ASK or FUZZ to the role the request
  needs, approved by the human through the existing decision surface;
- a durable, bounded handoff record in `progress.json` that preserves identity,
  shared references, source-role progress, and measured workspace state;
- role restoration in both directions, including BUILD → FUZZ and repeated
  transitions;
- interrupted and failed transitions that leave the last valid role and task
  state recoverable and report the outcome truthfully.

Implemented in Phase 3 (progression and outcome):

- a bounded durable timeline of creation, checkpoints, findings,
  hypothesis/evidence dispositions, role transitions and approvals, detach and
  resume, verification, completion, and failure;
- evidence and artifact lineage derived from the authoritative records, with
  proposed, executed, observed, verified, uncertain, invalidated, and
  superseded states kept distinct;
- recovery classification that distinguishes an intact checkpoint, a
  reconstruction with named missing information, a state requiring a human
  decision, and an unrecoverable load;
- an evidence-backed completion proof and an objective-state projection
  (`ACTIVE`, `INTERRUPTED`, `NEEDS_REVIEW`, `BLOCKED`, `PROVEN`,
  `COMPLETED_UNPROVEN`, `CANCELLED`);
- read-only `/task status`, `/task timeline`, and `/task lineage` views and a
  bounded progression section in the task briefing.

Not implemented:

- automatic role switching, parallel role instances, or agent cloning;
- a general workflow orchestration framework or a second approval system;
- cross-role execution orchestration;
- model-facing tools that create evidence, promote a claim, edit role context,
  or request a handoff; the host classifies the request and the human decides;
- a new task database, knowledge graph, memory subsystem, or completion rule;
- a general project-management dashboard, event-sourcing framework, or second
  verification engine.

### Workstream status

| Phase | Status | Acceptance evidence |
| --- | --- | --- |
| Phase 1 — shared task context | Complete | identity, role-scoped context, projection, restart tests |
| Phase 2 — bidirectional role transition | Complete | handoff proposal/approval/application, crash reconciliation, recovery tests |
| Phase 3 — timeline, recovery, verifiable outcome | Complete | bounded timeline, derived lineage, recovery classification, atomic completion proof, production mutation test |

The Phase 3 correction pass resolved the final audit's blocker and high
findings: completion is atomic with its checkpoint, execution objectives require
a projected execution observation, downgrade writes are refused by the version
barrier, and the real production mutation path is covered end to end.

### Final domain distinctions

| Concept | Owner and responsibility |
| --- | --- |
| Task | `internal/continuity/task`; durable project-scoped unit of work |
| Session | `internal/continuity/session`; conversation and interaction continuity |
| Role | `internal/control/workspace`; current execution authority |
| Contract | `internal/control/contract`; objective, target, and completion requirements |
| Context | `task.SharedContext`; role-scoped working information |
| Evidence | `cognition` observations projected as `task.EvidenceRecord`; what supports or challenges a claim |
| Timeline | `task.Timeline`; bounded lifecycle and audit index |
| Lineage | derived by `task.Lineage`; relationships between claims, evidence, and artifacts |
| Recovery | derived by `task.recoveryReport`; classification of restored integrity |
| Completion proof | `task.CompletionProof`; durable evidence that the contract's deterministic requirements were satisfied |

### Verification and acceptance evidence

The final pass executed, in this repository:

```text
gofmt -l .        # empty
go build ./...
go vet ./...
golangci-lint run ./...   # 0 issues
go test -count=1 ./...
go test -race ./internal/continuity/task/ ./internal/tui/
```

The production acceptance test is
`internal/tui/phase3_acceptance_test.go::TestPhase3ProductionMutationCompletionThroughRuntime`.
It drives the real turn entrypoint with the real write and bash tools against a
temporary Go module, observes the mutation and verification through the
projection, asserts `PROVEN` only after the whole path succeeds, and restores
the task with the same identity and a valid proof. The controller-level
regressions live in `internal/continuity/task/phase3_corrections_test.go`.

---

## 8. Handoff (Phase 2)

### 8.1 Ownership

A role transition spans four existing owners and adds no package:

| Concern | Owner |
| --- | --- |
| Whether authority may move and where | `workspace.DecideTransition` (policy) |
| The durable transition record | `task.Handoff` in `progress.json` |
| The human decision and its presentation | `internal/tui` decision surface |
| The actual authority move | `harness.SwitchWorkspace` (the existing seam) |

`workspace.DecideTransition` classifies each request as `stay`, an automatic
BUILD ↔ FUZZ handoff, or a human confirmation across ASK. It never changes
authority by being computed; the TUI applies the automatic target or presents
the confirmation. The plain REPL applies automatic BUILD ↔ FUZZ moves and asks
for an explicit command before crossing ASK.

### 8.2 State model

```text
PROPOSED ──approve──> APPROVED ──turn Begin──> APPLIED
    │                    │
    │                    └──switch refused / live role mismatch──> FAILED
    └──decline──> REJECTED

PROPOSED found on load after a restart ──> INTERRUPTED (never approved)

APPROVED found on load after a restart ──> decided by the live workspace:
    restored role == target ──> APPLIED (no switch, no turn, consumed)
    restored role == source ──> INTERRUPTED (the switch did not happen)
    restored role == neither ──> INTERRUPTED with a recovery discrepancy
```

- `PROPOSED` is recorded before the decision. Authority has not moved. It can
  never be applied by recovery: only an explicit human approval can.
- `APPROVED` records the human's authorization. It proves neither that authority
  moved nor that it did not. The data layer therefore leaves an approved record
  undecided; the application boundary reconciles it against the authority that
  was actually restored.
- `APPLIED` is written after authority moved. It is the only status that changes
  `Task.ActiveRole`. An applied record is consumed by the next turn that attaches
  through it, so it continues that turn once and then stops acting as a
  continuation signal. A reconciled applied record is marked consumed at
  recovery, so recovery never resumes a turn by itself.
- `REJECTED`, `FAILED`, and `INTERRUPTED` are terminal for that record and never
  change the recorded role.
- `APPLIED` is terminal for that record; a later transition creates a new one.

`Begin` remains a fallback application boundary: it applies an approved handoff
only when the handoff objective matches the task and the approved target equals
the live role. Otherwise it records `FAILED` and continues under the live role.

Recovery ownership is deliberately split. `Load` knows the record but not the
restored authority, so it decides only what the durable status alone proves
(`PROPOSED`). The startup boundary knows both and calls
`Controller.ReconcileRecoveredHandoff`, which records the disposition and never
performs a switch. A restored role that matches neither endpoint is reported as
a discrepancy and the task's recorded role is left untouched.

### 8.3 What a handoff carries

The record snapshots references, never payloads:

- active verified facts;
- active and confirmed hypotheses;
- open questions;
- current evidence records;
- task constraints;
- artifact references from every role (patch, reproduction, benchmark);
- the source role, target role, objective, reason, and a concise summary;
- the measured workspace state (branch and status) when the host has it.

The source role is the live authority at the moment the transition is recorded,
not the task's stored role. Continuity metadata can lag a recovered switch, and
using it as the source would refuse or mislabel a valid transition.

Each list is bounded (`maxHandoffRefs`, the role-note bound), and repair prunes a
reference whose record no longer exists. The target role receives its own role
context plus a `handoff from <source>` progress note. The source role's context
is never rewritten, so returning to it restores its plan, scope, strategy, and
progress.

A structured FUZZ finding is projected into the shared context at the turn
boundary: the artifact remains in the session store, and the task records an
**active hypothesis** with the bounded target, status, observed result,
reproduction, and evidence summary. BUILD therefore consumes the finding as a
claim to verify, never as a fact; confirmation still requires current supporting
evidence. The artifact reference is also recorded in the producing role's
context. The artifact tool itself only buffers the record — the task has one
writer at the turn boundary.

### 8.4 Authority and workspace

- A handoff is a request, never a grant. No fact, reference, or record enables a
  tool; `workspace.Admit` and the security gate are unchanged.
- The accepted target is re-derived from policy at decision time and must match
  the pending proposal, so a stale or corrupted prompt cannot move authority
  somewhere the policy never allowed.
- The only authority move is `SwitchWorkspace`, which parses the target, updates
  the live gate, the effective role model, and the session snapshot together.
- Recovery never switches authority. It compares an approved record with the
  authority that was restored: a matching target is recorded as applied, a
  matching source stays unapplied, and any third role is reported as a
  discrepancy without rewriting the task role.
- A source that is missing from `task.md` is reconstructed from the persisted
  contract, so a recovered task and its handoff satisfy the same validation and
  persistence rules as a normally loaded task; an objective conflict drops the
  handoff with a diagnostic and leaves the task writable.
- Switching authority does not read, rewrite, stage, or discard project files.
  Dirty work is preserved by construction; the handoff records the measured
  workspace summary so the user can see what was there.
- Experimental artifacts stay distinguishable by their reference kind
  (`cognition:command:*` for FUZZ, `cognition:mutation:*` for BUILD); the handoff
  never reclassifies one as the other.
- FUZZ cannot mutate through a handoff, and ASK remains read-only.

### 8.5 Presentation and recovery

The handoff reuses the existing in-chat workspace-transition decision card, the
same permission surface that already asks a human to confirm a transition; no
new popup, picker, or workflow surface is introduced. It shows the current and
target roles, the objective, the counts of carried records, the measured
workspace state, and that approval is required. The conservative choice keeps
the current role; Enter confirms only after a deliberate move.

On restart, recovery reconciles an approved record with the authority that was
actually restored. The record is applied only when the live role is the approved
target; it stops being a continuation signal at that point, so recovery cannot
resume a turn by itself. When the live role is the source, the transition is
reported as not applied and the source role is preserved. When the live role is
neither endpoint, the record is reported as a recovery discrepancy and the task's
recorded role is left untouched. A proposal that was never approved is always
interrupted, never applied. The task remains writable and resumable in every
case, and notices name only facts the restored state establishes.

### 8.6 Explicit limits

- `Task.Handoff` retains only the most recent transition record. The durable
  timeline retains the disposition of every transition, so history is
  traceable without a second handoff store.
- Explicit `/ask`, `/fuzz`, and `/build` commands are direct human authority
  changes. They need no proposal or second approval, but they are recorded as an
  applied handoff so the next turn stays attached to the same task. The record
  is consumed by that turn; later turns use the ordinary continuation policy.
- The handoff request is host-classified from the user's request. There is no
  model-facing handoff tool, and the model cannot approve or apply one.
- The workspace summary is diagnostic, not a rollback or merge mechanism; the
  existing recovery service remains the only edit-history authority. A dirty
  tree is preserved by the switch, and the handoff reports the measured state;
  it does not attempt to reconcile unrelated concurrent edits.

---

## 9. Progression, Lineage, Recovery, and Proven Completion (Phase 3)

### 9.1 Ownership

Phase 3 adds no package, store, manager, or orchestration layer. It extends the
existing task record and derives every view from state that already has an
owner.

| Concern | Owner |
| --- | --- |
| Meaningful event history | `task.Task.Timeline` in `progress.json` |
| Conclusion/observation lineage | derived by `task.Lineage` / `task.TraceLineage` from `Task.Context` |
| Recovery classification | derived by `task.Store.Load`, exposed as `Controller.Recovery` |
| Completion proof | `task.Task.Completion`, produced by `Controller.CompleteWithEvidence` |
| Completion verdict | `agent.ClassifyRun` / `agent.EvaluateTaskCompletion` (unchanged) |
| Presentation | existing task block and `/task` command surface |

The timeline is an index, never a second source of truth. A fact, hypothesis,
or evidence record remains authoritative for its own statement, status, and
references; the timeline only names the transition and the record id. Historical
events are append-only and are never rewritten by a later state change.

### 9.2 Timeline

Bounded by `MaxTimelineEvents`; each event carries `Seq`, `At`, `Kind`, `Role`,
`Status`, a summary of at most 320 bytes, and at most 16 references. Events are
recorded at the existing mutation boundaries:

- task creation, resume, checkpoints, pause, review, completion, failure,
  cancellation, and detach;
- evidence and verification observations, facts, hypotheses, questions, and
  their confirmation, rejection, supersession, invalidation, and attached
  evidence;
- handoff proposal, approval, rejection, failure, application, explicit
  workspace commands, and recovery dispositions.

A repeated checkpoint of the same action, a repeated pause, or a repeated review
does not append a duplicate event. Task-context mutations use the caller's
clock, so the log is deterministic under an injected clock.

When the bound is reached, the first retained event is the anchor and is never
dropped. Within the rest, observation events (evidence, verification, finding,
fact, hypothesis, question) are elided before lifecycle boundaries, so a long
investigation cannot push pause, review, completion, or handoff out of the
record. `TimelineDropped` counts the sequence gaps: it derives the number from
the retained sequence numbers themselves, so it stays accurate even when the
creation event was repaired away or an earlier truncation already removed it.

### 9.3 Lineage

`Lineage` returns facts, hypotheses, and evidence in record order, each with a
stage: `PROPOSED`, `EXECUTED`, `OBSERVED`, `VERIFIED`, `CONTRADICTED`,
`UNCERTAIN`, `INVALIDATED`, or `SUPERSEDED`. `TraceLineage` follows a record in
both directions: a fact or hypothesis to its supporting evidence and the fact a
hypothesis produced, an evidence record to the facts and hypotheses that cite it
and the claims it contradicts. Artifacts remain references; a `cognition:`
reference is checked against the durable observation state and reported as
missing when it no longer resolves. A path or external artifact is preserved
verbatim because this package cannot prove a filesystem path still exists.

### 9.4 Recovery

`Store.Load` classifies what it actually restored:

- `INTACT` — the checkpoint and projection were readable, no record needed
  repair, the completion proof (when present) is valid, and a non-terminal task
  has a contract;
- `RECONSTRUCTED` — a projection or checkpoint was missing/unreadable, or a
  record (context, handoff, timeline, or completion proof) was repaired or
  dropped; the report names what was absent;
- `NEEDS_HUMAN` — the task is valid but cannot progress without an explicit
  decision (for example, a non-terminal task with no completion contract);
- `BLOCKED` — nothing consistent could be recovered.

`INTACT` guarantees only structural recovery: both files parsed, the schema is
supported, the optional records sanitized with zero repairs, and a non-terminal
task has a contract. It does **not** claim that a `cognition:` artifact still
exists, that a filesystem path exists, that the workspace still matches the
checkpoint, or that an in-flight tool operation can be replayed. Tool calls are
not journaled; the session store remains their owner.

Archives derive the same vocabulary. `LoadArchived` and `ListArchived` sanitize
through the same rules and attach a `RecoveryReport` to each snapshot;
`ResumeTask` preserves that classification instead of overwriting it with
`INTACT`. A corrupt archive file is skipped from the resumable list and recorded
as an `ArchiveProblem` with its path and reason, so a lost task is observable
rather than silently indistinguishable from one that never existed.

The classification is derived on every load and never persisted. The report also
counts outstanding todos. Recovery never invents progress, never marks an
interrupted task complete, and never changes authority.

### 9.5 Objective state and proven completion

`Task.ObjectiveState` projects the lifecycle and proof into `ACTIVE`,
`INTERRUPTED`, `NEEDS_REVIEW`, `BLOCKED`, `PROVEN`, `COMPLETED_UNPROVEN`, or
`CANCELLED`. It is a projection, not a second lifecycle.

`Controller.CompleteWithEvidence` accepts the host evaluator's verdict
(`agent.ClassifyRun` / `agent.EvaluateTaskCompletion` remains the semantic
authority) and records a proof only when the durable task carries the
observations the objective requires. Each contract kind has an explicit
requirement:

| Kind | Durable requirement | What `PROVEN` means |
| --- | --- | --- |
| `informational` | a non-empty final answer | an answer was produced; its correctness is not verified |
| `execution` | a current, attributable execution observation, or the contract's required verification | a process or check reached a successful terminal state |
| `mutation` | an observed definite change **and** a current verification that followed it and covers the contract's declared check kind and scope | the named change was made and checked afterwards |
| unsupported | refused | no claim |

The four evidence notions stay distinct: `Answered` is answer production,
`Executed` is a successful terminal execution, `Observed` is a definite change,
and `Verified` is a check that passed under recorded conditions. `PROVEN`
requires the contract's combination of them; it never means independent
semantic correctness.

Specific rules:

- an answer alone never proves an execution objective;
- a successful command exit establishes execution only when its observation was
  projected into the task and associated with it;
- a verification that predates the last observed change is not accepted; the
  runtime's observation order is authoritative, with the durable record
  timestamp as the fallback for host-recorded evidence;
- a resumed turn may prove a change a prior run already recorded without
  repeating it, but only when the new check follows that change and covers the
  contract; a read-only resume or an unrelated check still cannot complete;
- the proof's `Verified` list contains only checks that followed the last
  change, not every current check;
- open todo work refuses completion and leaves the task active; a fully
  completed todo list is not evidence for a mutation or execution objective,
  because the todo tool records a model plan and not an observed result;
- a non-complete evaluation is refused;
- a task must be `RUNNING`: `PAUSED` and `NEEDS_REVIEW` resume through `Begin`
  first, so the boundary that reconciles the live workspace is not skipped.

#### Phase completion under a lower authority

A turn may run under an authority that cannot perform the persisted contract
(for example, a FUZZ investigation of a mutation objective). A conditional
mutation under BUILD follows the same rule before its branch is entered:
`IntentContract.ConditionalMutation` records that the change is deferred, and
no observed change means the turn is judged as the investigation answer rather
than a failed mutation. Completion then evaluates the informational phase the
authority can perform. A completed phase keeps the task `RUNNING` (`ACTIVE`)
and records no proof: it is neither `NEEDS_REVIEW` nor `COMPLETED`. The
checkpoint still records the phase evidence, and the same task continues when
the human authorizes the required workspace or the branch is entered. A phase
answer that is empty, only announces further work, or fails grounding still
enters review.

Phase completion narrows the completion requirement; an explicit constraint
that survives the phase still applies. The projection shape is also built from
the phase contract, and a staged conditional mutation defers its literal
target-state requirement to the change branch. For a staged conditional
objective, wording such as "without modifying source code" applies to the
investigation phase, which runs under ASK or FUZZ authority. It is not carried
as a global `no_mutation` veto on the later BUILD branch. An unconditional "do
not modify anything" remains global. A task-continuity instruction such as "do
not create a new task" is not a repository `no_creation` constraint.

The proof stores the verdict, the `Answered`/`Executed`/`Observed` flags, the
bounded check summaries, the evidence ids, and the timestamp. It never stores
the assistant's answer or a tool payload.

Completion also resolves the current review boundary: the durable
`ReviewReason` is cleared, and an older completed checkpoint is normalized on
load. The historical reason remains in the bounded timeline and in the review
event, so it can still be audited without being projected as current
uncertainty. While a task is `RUNNING`, `PAUSED`, or terminal, the reason is not
rendered as a live warning; only `NEEDS_REVIEW` owns that projection.

A later invalidation, supersession, or eviction of the proof's evidence does not
rewrite the lifecycle record; it downgrades the projection to
`COMPLETED_UNPROVEN`. A contradiction downgrades the proof only when it targets
a claim the proof depends on (a fact or confirmed hypothesis backed by the
proof's evidence, or an evidence id the proof names); an unrelated
contradiction does not. The current objective state always reflects the current
proof validity, while the historical timeline events remain unchanged.

The legacy `Controller.Complete` remains for compatibility and records
`COMPLETED_UNPROVEN`: it may not claim a proof it never saw.

If the completion checkpoint cannot be written, the in-memory task is restored
to its prior state: no proof, no completion event, and no `COMPLETED` status is
published, and the same task remains resumable. The TUI repeats that guard, so a
host hook cannot surface an uncommitted completion either.

### 9.6 Observability

The existing task block shows a proven/unproven completion, a non-intact
recovery, and a continuity warning that did not block recovery. `/task status` and `/task show` add the objective state, the completion
summary, the recovery classification, continuity warnings, and outstanding work. `/task timeline [n]`
shows the bounded event log and `/task lineage [id]` traces a record to its
evidence. All three are read-only; no new dashboard or interaction model is
introduced.

### 9.7 Limits

- The timeline is bounded; old observations are dropped first, and the visible
  sequence gap reports the loss. It is an index, not an event-sourcing archive.
- Lineage is derived per call. It does not verify external artifacts or prove
  that a filesystem path still exists.
- `PROVEN` depends on the evaluator's verdict plus the task's recorded
  observation. It is not an independent semantic proof, and an informational
  proof does not establish answer correctness. A proof written before the
  explicit `Answered`/`Executed`/`Observed` flags existed projects as
  `COMPLETED_UNPROVEN`; it is not silently upgraded.
- The durable boundary re-checks ordering, currentness, the contract kind, and
  the declared check kind and scope. Full target and behavioural-scope
  analysis remains the evaluator's job; a check that covers the declared scope
  can still be weaker than the change it is said to cover.
- In-flight tool operations are not journaled. Recovery classifies the durable
  checkpoint and the session store remains the owner of an interrupted call.
- Concurrent edits outside the task are still the recovery service's concern;
  the timeline reports the measured workspace state but does not reconcile it.
- There is no model-facing tool for completion, evidence, or handoff. The host
  evaluates and the human decides.
