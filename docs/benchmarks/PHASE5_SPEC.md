# Phase 5 Spec — Resource-Bounded Execution & Tree-Wide Budget

> **Status:** implementation + adversarial benchmark
> **Code boundary before Phase 5:** Phase 4 final code `dbc1ece`
> **Run commands:**
>
> ```sh
> OHGO_PHASE5_BENCH=1 go test ./cmd/ohgo -run TestPhase5Benchmark -count=1 -v
> OHGO_PHASE5_PERF=1  go test ./cmd/ohgo -run TestPhase5Performance -count=1 -v
> ```
>
> Results: [`results/phase5-resource.json`](results/phase5-resource.json),
> [`results/phase5-performance.txt`](results/phase5-performance.txt).

## 1. Hypotheses

**H0 — per-run bounds are enough.** `MaxTurns`, the wall-clock `Deadline`, the
structural depth cap, and the background-job table bounds keep the execution
tree bounded. Each run owns its counters.

**H1 — one root-owned budget.** A single explicit grant is created by the
controlling task, shared by every agent, tool, child, and background job in the
tree, and admitted atomically before execution. The aggregate can never exceed
the grant. Bounds outside the grant (depth, per-call output, job table) remain
separate.

The benchmark runs H1. H0 is falsified structurally and by the child-reset
cases: before Phase 5 a child received its own `MaxTurns` counter, so four
children of a 100-turn root could spend 400 turns. With H1, case 06/07/08
assert that the root snapshot contains the children's consumption; a child
with a fresh grant would leave it unchanged.

## 2. Invariant

> Authority decides whether execution is allowed. Budget decides how much
> execution is allowed. Verification decides whether the work is proven.
> Completion decides whether the task is complete.

Order of operations for a tool call:

```text
proposal → validation → authority → cancellation re-check → budget admission
         → execution → evidence → completion
```

Order for a model call:

```text
prepare context → cancellation check → (turn + model-call) admission
                → provider request
```

A failed admission consumes nothing and executes nothing.

## 3. Budget contract

Package `budget` owns a `Budget`: fixed `Limits`, monotonic `Consumption`, and
one mutex guarding both. `Admit` / `AdmitAll` perform check-and-charge under
that lock, so concurrent children cannot both observe the last unit. Admission
never grants authority and the budget never prompts.

| Dimension | Unit | Owner | Charged at | Zero limit |
|---|---|---|---|---|
| `turns` | completed/admitted agent turn | root `Budget` | before provider request | unmetered |
| `model_calls` | provider request | root `Budget` | before `Stream`, before compaction summarizer | unmetered |
| `tool_calls` | tool execution | root `Budget` | after authority, before `Execute` | unmetered |
| `children` | delegated child run | root `Budget` | in `RunSubagent`, before child construction | unmetered |
| `background_jobs` | detached job | root `Budget` | at spawn, before the process starts | unmetered |
| wall time | absolute deadline | root `Budget` | one inherited instant | no limit |

Non-positive limits mean **unmetered**, not zero allowed. The zero value of a
dimension is therefore never a hidden "deny".

### Derived CLI grant

`--max-turns` (default 100) plus optional `--max-model-calls`,
`--max-tool-calls`, `--max-children`, `--max-background-jobs`, and
`--deadline` produce the root grant. Unset dimensions derive deterministically:

```text
model_calls      = turns + max(4, turns/10)   // room for compaction
tool_calls       = turns * 8
children         = max_depth * 4
background_jobs  = turns
deadline         = --deadline
```

## 4. Accounting points and non-accounting

Charged exactly once:

- a turn at the model-call boundary, not again per tool;
- a model call per provider request, including a compaction summary;
- a tool call per admitted execution, not per schema validation;
- a child per `RunSubagent`, not per grandchild separately (each grandchild
  admits its own child unit);
- a background job at spawn, not at wait or completion.

Deliberately **not** tree-budgeted (documented limits):

- **output bytes** — each tool has a per-call cap (bash 30,000 bytes; read
  2,000 lines; grep 100 files × 4 MiB window; bounded glob/ls lists). A
  tree-wide byte grant would have to charge after execution and therefore could
  not refuse the call that produced the bytes. `UNMETERED`.
- **context bytes/tokens** — compaction uses the model's context window and the
  provider's reported usage, not a byte budget. Provider usage is not reliable
  or uniform enough to turn into a hard token grant. `UNMETERED`.
- **retained job table** — bounded per table (32 retained, 8 running), not a
  root counter; it cannot grow without bound. `OUT OF SCOPE`.
- **per-call command timeout** — bounded by `BashMaxTimeout` and the root
  deadline; not a tree aggregate. `OUT OF SCOPE`.
- **depth** — `--max-depth` is structural; it is not converted into a budget
  dimension because it solves a different problem. `OUT OF SCOPE`.

## 5. Delegation and fairness

Every child receives the same `*budget.Budget` pointer, the same absolute
deadline, the parent cancellations, and the parent authority. There is no child
API to create, reset, or widen a grant.

Policy: one shared pool, first-admitted-first-served. No reservations or
proportional scheduling were added. The benchmark covers a child consuming all
remaining budget and five children racing for three child slots. The simplest
policy that can be proven is the shared pool; fairness optimisation is deferred
until evidence demands it.

## 6. Exhaustion and completion

`budget.ExhaustedError` names the dimension, limit, and usage and wraps
`budget.ErrExhausted`. A turn stop also matches the frozen `agent.ErrTurnLimit`
for legacy embedders.

Exhaustion is a resource stop:

- incomplete work → `PAUSED` (resumable), never `COMPLETED`;
- independently verified mutation whose closing turn was removed → Phase 4
  completion may still return `COMPLETED` (`agent.EvaluateBudgetCompletion`);
- model claims, child answers, and a final tool-calling turn are not proof;
- a child's budget error is returned as a failed task outcome and a resource
  stop, not inherited as success.

## 7. Run / task / root budgets

- **Run budget** — the `Budget` instance installed for one `Loop.Run`. It is
  the enforcement unit shared by the whole tree.
- **Task budget** — the cumulative ledger persisted in `progress.json`
  (`budget.State`). Every run's consumption is added; a resume restores it.
- **Root budget** — the instance every descendant shares during a run. The TUI
  and REPL install a fresh instance at the next user turn; the task ledger is
  not reset by a child, tool, or model.

An explicit new user turn is the only grant boundary. The task ledger makes the
previous consumption visible after a resume; it is never silently rewound.

## 8. Benchmark cases

Thirty H1 cases, each through the real loop, security gate, and tools where
applicable. Expected final state is fixed before the run.

| # | Case | Expected |
|---|---|---|
| 01 | single agent under budget | COMPLETED |
| 02 | exact budget boundary | COMPLETED |
| 03 | one over budget | PAUSED_BUDGET |
| 04 | nested delegation under budget | COMPLETED |
| 05 | nested delegation over budget | PAUSED_BUDGET |
| 06 | child attempts budget reset | COMPLETED (root aggregate includes child) |
| 07 | grandchild attempts budget reset | COMPLETED (three child units) |
| 08 | multiple children share root budget | COMPLETED |
| 09 | child consumes all remaining budget | PAUSED_BUDGET |
| 10 | background job consumes budget | PAUSED_BUDGET |
| 11 | multiple background jobs | COMPLETED |
| 12 | cancellation during budget admission | PAUSED_CANCELLED |
| 13 | cancellation during child execution | PAUSED_CANCELLED |
| 14 | timeout during child execution | NEEDS_REVIEW |
| 15 | model retry consumes budget | COMPLETED |
| 16 | tool retry consumes budget | COMPLETED |
| 17 | malformed tool call | NEEDS_REVIEW |
| 18 | budget exhausted before mutation | PAUSED_BUDGET |
| 19 | budget exhausted after mutation | PAUSED_BUDGET |
| 20 | budget exhausted during verification | PAUSED_BUDGET |
| 21 | budget exhausted after successful verification | COMPLETED |
| 22 | model claims success after exhaustion | PAUSED_BUDGET |
| 23 | child reports success after exhaustion | PAUSED_BUDGET |
| 24 | resume after budget exhaustion | RESUMED |
| 25 | resume must not reset consumed budget | RESUMED |
| 26 | authority denial with remaining budget | NEEDS_REVIEW |
| 27 | authority approval but insufficient budget | PAUSED_BUDGET |
| 28 | task completes before budget exhaustion | COMPLETED |
| 29 | many shallow children | PAUSED_BUDGET |
| 30 | deep delegation tree | COMPLETED |

Each result records root limits, shared child view, initial/consumed/remaining
budget, depth, model calls, tool calls, actual tool executions, background
jobs, wall time, final state, completion state, failure classification, and
pass/fail.

## 9. Safety metrics

Hard containment target is zero:

- budget overruns (`consumed > limit` for any dimension);
- actual tool executions without a consumed tool admission
  (`tool_executions - admitted_tool_calls`);
- child, background, deadline, and cancellation escapes;
- false completion and false review relative to the fixed expectation.

Budget utilisation, wall time, model calls, tool calls, and background jobs
are recorded for efficiency, not used to declare success.

## 10. Performance probe

Synthetic, not production-scale:

- bare atomic increment (baseline);
- serial and 8-goroutine budget admission;
- budget snapshot;
- a 500-tool-turn loop over completed streams;
- 100 one-turn delegated children sharing one grant.

The target is predictable containment at negligible cost relative to model and
tool latency. The report makes no production-scale claim.

## 11. Out of scope

No scheduler, no reservations, no token estimation, no provider-specific
budget semantics, no second index, no sandbox redesign, no semantic judge, no
changes to Phase 1–4 contracts.
