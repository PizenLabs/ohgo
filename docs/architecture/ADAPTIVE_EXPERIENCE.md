# Adaptive Experience (AES)

> **Status:** implemented as an advisory layer.
> **Owner:** `internal/experience`.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](OHGO_ARCHITECTURE_MAP.md).
> **Permanent rules:** [`OHGO_ARCHITECTURE_RULES.md`](../migration/OHGO_ARCHITECTURE_RULES.md).
> **Plan:** [`docs/plans/adaptive-experience-system.md`](../plans/adaptive-experience-system.md).

## 1. What it is

AES is OHGO's verified execution-experience layer. It learns **how to execute
better**, not **what the project is** and not **who may do what**.

```text
Execution
    ↓
Event / Outcome
    ↓
Incident Detection
    ↓
Lesson Extraction
    ↓
Candidate Experience
    ↓
Validation / Regression
    ↓
Promotion
    ↓
Experience Store
    ↓
Future Path Selection
```

It is not a memory database, a task lifecycle, an evidence store, a completion
evaluator, or an authority path.

## 2. Ownership

| Concern | Owner |
| --- | --- |
| Turn execution, tool outcomes, usage | `internal/agent` |
| Task shape, evidence, sufficiency, projection | `internal/control/cognition` |
| Authority and workspace policy | `internal/control/workspace` + `cmd/ohgo` gate |
| Durable task, task timeline, completion proof | `internal/continuity/task` |
| Project knowledge / memory trust boundary | `internal/memory` |
| Verified execution experience | `internal/experience` |
| Advisory application to a projection | `cognition.ExperienceAdvisor` |
| Host wiring | `cmd/ohgo` |
| Inspection UI | `internal/tui` through `ExperienceHooks` |

AES consumes the runtime's own structured state: `agent.Turn`, `agent.Result`,
`agent.Usage`, `cognition.TaskShape`, `cognition.EvidenceState`,
`cognition.ExecutionState`, `cognition.Sufficiency`, and
`agent.ProjectionReport`. It does not replay a transcript to discover facts.

## 3. Typed record model

- **Event** — one observed runtime signal (`repeated_probe`,
  `recoverable_failure`, `material_failure`, `successful_shortcut`,
  `expensive_path`, `evidence_gap`, `stale_review`, `handoff_redundancy`,
  `authority_refusal`, `context_growth`).
- **Incident** — the structured pattern extracted from one observation, with
  context, tool, target, failure class, cost, and occurrence bounds.
- **Lesson** — a bounded, deterministic statement describing what was learned.
- **Candidate** — a proposed path preference derived from a lesson. It is not
  applied.
- **Validation** — recurrence, regression, contradiction, or architecture
  evidence.
- **Promoted experience** — a validated candidate that may influence path
  selection. It carries the full incident, lesson, validation, transition,
  regression, and scope trail.

The distinction is structural, not a comment: only a `Promoted` or `Monitored`
record reaches `Advise`.

## 4. Scopes

| Scope | Storage | Use |
| --- | --- | --- |
| `task` | in-memory engine | only the current task |
| `project` | `<root>/.ohgo/experience.json` | reusable in one checkout |
| `runtime` | optional explicit path | local harness patterns |
| `global` | optional `<state>/experience/global.json` | cross-repository, after explicit validated promotion |

Production TUI turns record `project` scope. A project record never becomes
runtime or global automatically. `PromoteToGlobal` is the only global writer; it
requires project scope, at least three independent observations, zero
contradictions, and a passed regression validation. Direct `Observe` into
global scope is refused.

## 5. Promotion lifecycle

```text
Candidate ──second independent matching success──> Validated
Validated ──recurrence + regression link─────────> Promoted
Promoted  ──continued matching success───────────> Monitored
Candidate/Validated ──contradiction──────────────> Rejected (when material)
Promoted/Monitored ──contradiction/retirement────> Retired
```

- A single observation never changes behavior.
- `AutoPromote` is opt-in at the host. The TUI enables it only after recurrence
  validation and a regression link; a `Validated` record still does not apply.
- `Validate` can add a regression check; a passed regression can justify
  promotion of a single high-value incident.
- `Retire` and `Reject` are explicit, audited transitions.
- Contradictory observations block promotion and retire an active record when
  they meet or exceed its successful observations.

## 6. Application boundary

`cognition.ExperienceAdvisor` may return:

- `PreferredTools` — names of tools **already offered** by the candidate
  surface; used only for stable reordering;
- `Hints` — at most three bounded advisory lines appended to the runtime state
  block;
- `Matched` — record IDs for audit.

The advisor cannot add a tool, remove a tool, change authority, bypass a
security decision, alter evidence, change sufficiency, or decide completion.
The candidate surface still applies task-shape filtering, and the security gate
still authorizes every call. A hint is labelled "advisory, not authority" in the
state block.

## 7. Safety invariants

1. AES never grants BUILD or FUZZ authority.
2. AES never bypasses human approval.
3. AES never downgrades evidence requirements or marks a claim verified.
4. AES never declares a task complete.
5. AES never changes a budget ceiling.
6. AES never writes task, evidence, completion, or session state.
7. Project experience cannot become global without explicit validated promotion.
8. A promoted record can be retired; retirement is traceable.
9. A malformed, stale, or newer-version experience file is ignored safely.
10. Cold start with an empty store is a no-op and preserves existing behavior.
11. Experience is not evidence: a lesson cannot satisfy a requirement.
12. No model-facing tool creates, promotes, retires, or edits experience; those
    are host APIs and human commands.

## 8. Persistence

`experience.json` is a versioned document:

```json
{
  "version": 1,
  "records": [ ... ],
  "updatedAt": "..."
}
```

- One writer: `experience.Store`.
- Temp-then-rename in the destination directory.
- Read-old/refuse-newer: a newer version is ignored with a diagnostic.
- Malformed records are dropped; valid records are retained.
- The store is bounded (`MaxRecords`), but active records are retained before
  historical candidates. No tool payload or transcript is stored.
- Deleting the file is always safe: the runtime starts with no experience.

The task checkpoint remains `progress.json`; the experience file is not a
second task record and cannot restore or complete a task.

## 9. Inspection

`/experience list` shows active and historical records. `/experience show <id>`
shows the incident, lesson, hint, context, scope, observations,
contradictions, regression, last validation, and transition trail.
`/experience retire|reject <id>` are human actions.

The same information is available through `Engine.List` and `Engine.Explain`.
No transcript reconstruction is required.

## 10. Known limits

- Extraction is deterministic and heuristic; it recognizes the initial bounded
  signal set, not every possible execution pattern.
- There is no LLM semantic generalization in this layer. A new lesson class
  needs a deterministic extractor, a regression, and a placement decision.
- Global experience is supported by the engine and tests but is not written by
  a normal runtime path; promotion is an explicit host operation.
- The runtime records project-scoped experience in the TUI path. `ohgo run`
  remains stateless and does not create a durable task or experience store.
- A regression reference is linked and test-checked, but the runtime does not
  execute Go tests during a turn; the repository test suite is the executor.
- Recurrence validation is not a probability. `Observations` and
  `Contradictions` are counts, never a calibrated confidence score.
