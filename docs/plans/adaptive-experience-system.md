# Adaptive Experience System (AES) — Implementation Plan

> **Status:** implementation plan, grounded in the current tree.
> **Owner:** `internal/experience` (new domain, declared in the architecture
> manifest).
> **Canonical behaviour document (to be added):**
> `docs/architecture/ADAPTIVE_EXPERIENCE.md`.
> **Non-goal:** a second task/evidence/timeline system, a second authority
> path, or a self-modifying agent.

## 0. What the current runtime already owns

The plan is built on the actual implementation, not an assumed architecture.

| Concern | Current owner | What AES may consume |
| --- | --- | --- |
| Turn execution, tool outcomes, usage | `internal/agent` (`Turn`, `ToolOutcome`, `Result`, `Usage`) | completed turns, call signatures, tool targets, failures, usage, verification flags |
| Task shape, evidence, sufficiency, projection, candidate tools | `internal/control/cognition` | `TaskShape`, `EvidenceState`, `ExecutionState`, `Sufficiency`, `CandidateSet`, `ProjectionReport` |
| Authority and phase transitions | `internal/control/workspace`, `cmd/ohgo` gate | workspace label only; AES never reads or changes policy |
| Durable task, shared/role context, timeline, proof | `internal/continuity/task` | read-only snapshots (prior status, review reason, role, evidence pointer) |
| Session transcript and artifacts | `internal/continuity/session` | no transcript replay; AES consumes structured outcomes |
| Budget | `internal/control/budget` | usage counters and limits, read-only |
| Completion and run verdict | `internal/agent` (`EvaluateTaskCompletion`, `ClassifyRun`) | outcome label; AES never computes or changes completion |
| Project memory trust boundary | `internal/memory` | distinct from AES: memory is durable project *knowledge*; AES is durable *execution strategy* |

The loop is a closed kernel and may not import AES. AES therefore plugs into
existing seams only:

- **Observe:** a host hook at the turn boundary receives `[]agent.Turn`, the
  completion contract, the last `agent.ProjectionReport`, the durable
  `cognition.EvidenceState`, the workspace, and the evaluator outcome.
- **Advise:** `cognition.Projector` gets an optional
  `cognition.ExperienceAdvisor`. The advisor may reorder tools already offered
  and append bounded advisory hints. It cannot add, remove, or authorize a
  tool.
- **Trace:** a read-only projection (TUI `/experience` and package API) over the
  store. No event is reconstructed from raw prose.

No new event model is introduced. AES events are derived from the authoritative
runtime outcome, and the durable task timeline/evidence remain the sources for
what happened.

## 1. AES architecture

```text
Execution (agent.Loop)
    ↓  completed turns + projection report + task outcome
Observation (host turn boundary)
    ↓
Incident Detection (deterministic extractor)
    ↓
Lesson Extraction (deterministic templates)
    ↓
Candidate Experience (typed, scoped, traceable)
    ↓
Validation (recurrence / contradiction / explicit regression)
    ↓
Promotion (status transition, audit trail)
    ↓
Experience Store (versioned, bounded, project/runtime/global)
    ↓
Future Path Selection (cognition.ExperienceAdvisor: ordering + hints)
```

Placement:

| Stage | Owner |
| --- | --- |
| Execution | `internal/agent` (unchanged) |
| Incident extraction / lessons / candidates / promotion / store | `internal/experience` |
| Projection application | `internal/control/cognition` (`Projector.Advisor`, advisory only) |
| Host wiring | `cmd/ohgo` (constructs the engine, passes project/runtime scope, installs hooks) |
| UI inspection | `internal/tui` through projection hooks (no domain import) |

The engine is a consumer, not a controller. It does not run, authorize, or
complete anything.

## 2. Experience model

All types are explicit and distinct.

```go
// Event: one observed runtime signal. Not persisted as a transcript.
type Event struct {
    ID, Kind, Key, Summary string
    Tool, Target, FailureClass string
    Workspace, TaskKind string
    Outcome Outcome
    Cost Cost
    Refs []string
    At time.Time
}

// Incident: a structured pattern recognised in one observation.
type Incident struct {
    ID, Key, Kind string
    Summary string
    Context Context
    Evidence []string
    Cost Cost
    Occurrences int
    At time.Time
}

// Lesson: a deterministic, bounded generalized statement.
type Lesson struct {
    ID, Key, Kind string
    Statement string
    Context Context
    Regression RegressionCase
    At time.Time
}

// Candidate: a proposed future path preference derived from a lesson.
// CandidateStatus is the promotion lifecycle.
type Candidate struct {
    ID, LessonID, Key string
    Kind CandidateKind
    Statement string
    Hint string
    PreferredTools []string
    Context Context
    Scope Scope
    ScopeKey string
    Status CandidateStatus
    Observations int
    Contradictions int
    Cost Cost
    Validation []Validation
    Regression RegressionCase
    PromotedAt *time.Time
    RetiredAt *time.Time
    RetirementReason string
    Transitions []Transition
}
```

`Validation` records recurrence, contradictory-case, regression, or
architecture guard evidence with a bounded reference and timestamp.
`Transition` records `candidate → validated → promoted → monitored → retired`
with the reason and time. Every record answers: what happened, what was learned,
which evidence supported it, which regression covers it, why it was promoted,
where it applies, its scope, when it was last validated, and what could retire
it.

The distinction is enforced by construction: `Event` is ephemeral input,
`Incident` and `Lesson` are explanatory records, `Candidate` is not applied, and
only `Promoted`/`Monitored` records reach `Advise`.

## 3. Experience scopes

| Scope | Key | Persistence | Use |
| --- | --- | --- | --- |
| `task` | task ID | in-memory engine, optional | help the current task only |
| `project` | project ID (default) | `<root>/.ohgo/experience.json` | reusable in one repository |
| `runtime` | runtime key | optional `<state>/experience/runtime.json` | general to the local harness |
| `global` | `global` | optional `<state>/experience/global.json` | cross-project, explicit promotion only |

Rules:

- `Advise` consults task → project → runtime → global, and deduplicates.
- A project record never becomes runtime/global automatically.
- `PromoteToGlobal` requires explicit project scope, at least three independent
  observations, zero unresolved contradictions, and regression validation.
- A global record never overwrites project-local evidence; project advice is
  ordered first.
- Scopes are part of the record key, so the same statement in two scopes is two
  records, not one record with an ambiguous authority.

## 4. Learning includes successes

Events are extracted from both positive and negative outcomes:

- repeated probe (same `agent.CallSignature`, no mutation in between);
- recoverable / non-material failure (a later success or equivalent observation
  covers it);
- material failure (unrecovered at the run boundary, or a failed required read
  with no equivalent evidence);
- successful shortcut (completed, low tool count, no repeats, sufficient
  evidence);
- expensive path (completed but high tool count or repeated probes);
- evidence gap / unsupported claim (projection review, conflicting evidence, or
  documentation-only claim with no implementation observation);
- stale review (a repeated unresolved review boundary);
- handoff redundancy (a BUILD ↔ FUZZ transition followed by re-probing evidence
  already carried);
- authority refusal (a call the runtime refused; recorded as a path lesson, not
  an authority rule);
- context growth (a large projected request);
- successful verification after a failure (positive recovery evidence).

Successes produce `preferred_tool_order` candidates. Failures produce recovery
or duplicate guidance. A successful minimal path is therefore reusable even
when nothing failed.

## 5. Cost

`Cost` records tool calls, repeated tool calls, model turns, input/output
tokens, duration, evidence gained, verifications, and recovery attempts. A
candidate retains the best observed cost and the observations that produced it.
`LowerCost` prefers successful, fewer repeated calls, fewer tools, fewer tokens,
then shorter duration. Efficiency is only a tie-breaker for path selection; it
never removes a required evidence step. A difficult task may remain expensive.

## 6. Promotion rules

```text
Candidate ──second independent matching success──> Validated
Validated ──regression link + no contradiction────> Promoted
Promoted  ──next matching success─────────────────> Monitored
Promoted/Monitored ──contradiction or retirement──> Retired
Candidate ──contradictory evidence────────────────> Rejected (or remains
                                                      ineligible)
```

- One observation never changes behavior. The default `MinObservations` is 2.
- A `Candidate` is not applied.
- Auto-promotion (when enabled by the host) is allowed only for advisory
  candidate kinds, only after validation, and only when a regression case is
  attached. Explicit `Validate` + `Promote` is always available.
- Contradictory observations block promotion. Once contradictions reach the
  success count, a promoted record is retired.
- `Retire` is explicit and auditable. `Reinstate` is not provided; a retired
  lesson can be learned again as a new record.
- Global promotion is a separate explicit operation with a higher evidence bar.

## 7. Regression generation

Each lesson kind maps deterministically to a `RegressionCase` in
`internal/experience/regression.go`:

| Incident | Regression intent |
| --- | --- |
| repeated probe | duplicate-path suppression/ordering regression |
| recoverable failure | non-material failure stays non-blocking |
| material failure | required failure still escalates |
| successful shortcut | preferred path is selected under matching conditions |
| evidence gap | experience cannot replace evidence |
| stale review | repeated review boundary is not promoted |
| handoff redundancy | continuity preserves evidence across roles |
| context growth | bounded projection / no transcript retention |
| authority refusal | experience cannot grant authority |

The mapping is static and test-checked. The package tests are the actual
regression execution; the runtime stores only the reference. No LLM is needed
to generate a regression. If a future host adds LLM-assisted formulation, the
resulting candidate still requires the same runtime validation and a reviewed
test before promotion.

## 8. Application

`cognition.ExperienceAdvisor` may return:

- `PreferredTools`: names of tools **already in the offered set**, used only for
  stable reordering;
- `Hints`: bounded advisory text appended to the runtime state block;
- `Matched`: promoted record IDs for audit.

It cannot return a new tool, a permission, an approval, a budget change, an
evidence verdict, or a completion verdict. The candidate layer still enforces
the same shape/authority filtering; the security gate still authorizes every
call. The model still chooses the action.

Application is deliberately advisory: the engine can make a low-cost probe
earlier in the offered order and can remind the model that an equivalent
observation already exists. It cannot force a fixed workflow.

## 9. Contradiction, supersession, retirement

- A later adverse outcome for the same context (e.g. a preferred path ends in
  `NEEDS_REVIEW`, or a recoverable failure becomes material for the same tool)
  records a contradiction.
- A contradiction above the success count retires the record.
- `Retire` records the reason and timestamp; `Advise` ignores retired records.
- Scope narrowing is modeled by recording the narrower context; a retired or
  rejected record is never silently reinterpreted.
- Malformed, unknown-version, or out-of-scope records are ignored with a
  diagnostic, never applied.

## 10. Traceability

`Engine.List` and `Engine.Explain` expose the record, incident, lesson,
candidate, validation, regression, transitions, scope, last validation time, and
contradictions. `/experience` renders a read-only projection. The model never
needs to reconstruct it from a transcript.

## 11. Non-goals

AES explicitly rejects:

- self-modifying authority;
- autonomous policy rewriting without validation;
- unrestricted global memory;
- transcript-as-memory;
- provider-specific or model-specific hacks;
- replacing evidence with experience;
- replacing deterministic runtime logic with LLM reasoning;
- forcing every task through one learned path;
- optimizing tokens by reducing necessary evidence;
- a second task, evidence, timeline, completion, or authority system.

## 12. Success criteria

- A repeated known failure becomes less likely in matching conditions.
- A recoverable/non-material failure no longer produces an unnecessary review
  when equivalent evidence exists.
- A successful low-cost path is reusable in matching conditions.
- Duplicate exploration produces a record and, after validation, a hint or
  ordering preference.
- Context growth is measured and bounded.
- Cold start (empty store, nil advisor) behaves exactly as before.
- BUILD/FUZZ authority, evidence requirements, completion, and budget ceilings
  are unchanged.
- Every promoted record is traceable and regression-linked.
- Project experience never silently becomes global.

## 13. Implementation sequence

1. Add `internal/experience` with model, deterministic extractor, lesson and
   candidate lifecycle, bounded store, and engine.
2. Add the optional advisor seam to `cognition.Projector`; prove it can only
   reorder already-offered tools and append bounded hints.
3. Wire the engine at the TUI turn boundary and add `/experience` inspection;
   wire headless paths as a no-op/cold-start-safe observer where practical.
4. Add package, integration, architecture, cold-start, corruption, scope, and
   mutation tests.
5. Update the architecture map, rules, state document, and behaviour document.
6. Run `gofmt`, build, vet, tests, race tests, and lint.

## 14. Implementation status and deliberate adjustments

Implemented:

- `internal/experience`: typed events, incidents, lessons, candidates, scopes,
  validation, promotion, contradiction/retirement, bounded versioned store,
  cost capture, task/project/runtime/global APIs, and traceability.
- `cognition.ExperienceAdvisor`: optional projector seam that only reorders
  already-offered tools and appends at most three bounded hints. The projection
  report records the matched experience IDs and hints.
- TUI turn-boundary observation and `/experience` inspection/retirement
  command. The experience engine is created per session with the stable project
  id; a learning or persistence failure is only a notice.
- Architecture manifest and rules for the new `experience` domain; state,
  behaviour, map, and changelog documentation.

Deliberate adjustments:

- Direct `Observe` into global scope is refused. Global experience has exactly
  one writer, `PromoteToGlobal`, behind the higher bar.
- `BroadRepositoryAnalysis` is part of the matching context so a whole-project
  survey does not reuse a targeted read path.
- Production wiring is the TUI path. `ohgo run` and the plain stateless REPL
  remain non-durable by design; they do not create an experience store.
- Regression links are deterministic and test-checked. The runtime stores the
  reference; it does not execute Go tests during a turn.
- Auto-promotion requires two independent matching observations and a
  regression link. An explicit regression validation can promote a single
  high-value incident. `Validated` alone still does not apply.

Verification recorded in the final report:

- full `go test ./...`
- focused `go test -race` for experience, cognition, TUI, and command wiring
- architecture tests
- mutation checks for recurrence, contradiction, scope isolation, global
  guard, ordering, regression barrier, recovery classification, duplicate
  detection, and the authority import boundary
- cold start, malformed, newer-version, duplicate, and cross-project tests.

