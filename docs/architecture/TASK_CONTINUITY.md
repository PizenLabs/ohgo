# Task Continuity

> **Scope:** the durable, role-independent task context introduced by Phase 1.
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
only authority paths. Storing role context does not implement a role handoff:
authority transfer is outside Phase 1.

---

## 5. Context assembly

`task.ContextFor(task, role, lastAction)` is deterministic for the same
authoritative task state, role, and last action. It assembles, in priority
order:

1. objective and lifecycle status;
2. a continuity warning when context is missing, repaired, or unavailable;
3. the active authority label;
4. constraints, todos, current step, next action, and last action;
5. active verified facts (with evidence references and disputes);
6. hypotheses and dispositions;
7. open questions;
8. the active role's execution context;
9. current evidence references;
10. legacy unclassified notes, explicitly labelled as unverified.

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

Not implemented:

- automatic role switching or handoff orchestration (Phase 2);
- handoff approval UI or workspace transfer (Phase 2);
- unified timeline, transition history, checkpoints, or cross-role execution
  orchestration (Phase 3);
- model-facing tools that create evidence, promote a claim, or edit role
  context; semantic hypotheses, questions, reasoning, and strategy remain host
  writes because the runtime has no trustworthy signal for them;
- a new task database, knowledge graph, memory subsystem, or completion rule.
