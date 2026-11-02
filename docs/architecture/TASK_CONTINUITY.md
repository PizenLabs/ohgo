# Task Continuity

> **Scope:** the durable, role-independent task context introduced by Phase 1
> and the controlled role handoff introduced by Phase 2.
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
| Handoff authorization policy | `workspace.ProposeHandoff` | never (derived) | TUI transition card |
| Detached tasks | `task.Store` archive | `.ohgo/tasks/<id>.json` | `/task list`, `/task resume` |
| Completion verdict | `agent.Evaluate*Completion` | `task.Status` | TUI verdict |

The task checkpoint is **project-local**, not session-local:

```text
<project>/.ohgo/progress.json   authoritative task checkpoint
<project>/.ohgo/task.md         lossy human/model projection (never authority)
<project>/.ohgo/tasks/<id>.json detached task archive
```

A session is one conversation (`sessions.db`). A task is one objective that may
survive a session, an interruption, a restart, and a role change. A role change
never by itself recreates the task: the same objective continues. A new session
keeps the task durably resumable, an explicit continuation re-attaches it, and a
genuinely new objective starts a new task.

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

`ProgressVersion` remains **2**. Structured context is an additive,
self-versioned subdocument (`ContextVersion = 1`), and `activeRole` is an
additive optional field:

- an older reader ignores the unknown `context` and `activeRole` fields;
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
  (temp-then-rename). A failed write returns an error and restores the prior
  in-memory task.

`task.md` remains a projection. It does not carry the structured context, so
`progress.json` outranks it always. Detached tasks archive the full task and
progress, including context.

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

Not implemented:

- automatic role switching, parallel role instances, or agent cloning;
- a general workflow orchestration framework or a second approval system;
- unified timeline, transition history, checkpoints, or cross-role execution
  orchestration (Phase 3);
- model-facing tools that create evidence, promote a claim, edit role context,
  or request a handoff; the host classifies the request and the human decides;
- a new task database, knowledge graph, memory subsystem, or completion rule.

---

## 8. Handoff (Phase 2)

### 8.1 Ownership

A role transition spans four existing owners and adds no package:

| Concern | Owner |
| --- | --- |
| Whether authority may widen | `workspace.ProposeHandoff` (policy) |
| The durable transition record | `task.Handoff` in `progress.json` |
| The human decision and its presentation | `internal/tui` transition surface |
| The actual authority move | `harness.SwitchWorkspace` (the existing seam) |

`workspace.ProposeHandoff` is the exact complement of
`workspace.ProposeTransition`: the transition policy narrows, the handoff
policy widens, and neither changes authority by being computed. A widening
proposal therefore always requires an explicit human decision, while a
narrowing request keeps its existing prompt.

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

- Only the most recent handoff is retained; full transition history is Phase 3.
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
