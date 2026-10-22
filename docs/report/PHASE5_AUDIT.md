# Phase 5 Audit — Resource-Bounded Execution

> **Date:** 2026-10-22
> **Frozen boundary:** Phase 1–4 reports read before any change.
> **Code boundary:** Phase 4 final code `dbc1ece`.
> **Spec:** [`../benchmarks/PHASE5_SPEC.md`](../benchmarks/PHASE5_SPEC.md)

## 1. What was inspected

Read directly from source, not from prior summaries:

- `agent/loop.go` — `Run`, turn accounting, `MaxTurns`, `Deadline`,
  `dispatch`, `execute`, authorization ordering, compaction.
- `agent/subagent.go` — child config construction, inherited authority,
  deadline, turn cap, completion, evidence.
- `agent/tool.go` — tool execution context and result shape.
- `tools/bash.go` — foreground/background execution, process groups, timeout,
  output cap, detached lifetime.
- `tools/jobs.go` — running/retained job caps, truncation, reaper, kill.
- `tools/task.go` — delegation, depth, child result shaping.
- `task/controller.go`, `task/progress.go`, `task/task.go` — durable task
  lifecycle, checkpoints, recovery.
- `tui/app.go`, `tui/completion.go`, `tui/task.go` — turn execution, task
  transitions, completion mapping.
- `cmd/ohgo/run.go`, `cmd/ohgo/main.go`, `cmd/ohgo/taskcontinuity.go` —
  production loop/spawner construction, CLI bounds, task wiring.
- `ai/*` — provider calls, usage, retry behavior.
- Existing Phase 1–4 regression suites.

## 2. Resource inventory before Phase 5

| Resource | Existing bound | Owner | Scope | Shared? | Failure |
|---|---|---|---|---|---|
| turns | `Config.MaxTurns` (default 100) | each `Loop` | one `Run` | **No** — each child got its own counter | `ErrTurnLimit`, mapped to pause |
| model calls | none explicit; one per loop iteration; compaction summary unmetered | loop / host | per run | No | provider error |
| tool calls | none explicit; bounded only by turns × batch size | loop | per run | No | tool error result |
| wall time | `Config.Deadline`, inherited by children | root config | whole tree (absolute) | Yes (copied value) | nil error on expiry; resume |
| background jobs | `MaxRunningBackgroundJobs=8`, `MaxRetainedBackgroundJobs=32` per table; per-job timeout ≤10m; 30,000-byte output cap | `BackgroundJobs` table | one table instance | Yes within a tool set; not root-counted | tool error / timeout kill |
| retained jobs | `MaxRetainedBackgroundJobs=32` | job table | one table | Yes | table-full refusal |
| output bytes | bash 30,000 B; read 2,000 lines; grep 100 files × 4 MiB window; bounded glob/ls | each tool | per call | No | truncation report |
| context bytes | compaction threshold; model context window | agent compaction | per loop | No hard byte grant | compaction or provider refusal |
| delegation depth | `MaxDepth`, structural tool-set construction | `TaskTool` / tool builder | tree | yes (depth value) | tool absent at cap |
| child executions | none tree-wide; depth only | `TaskTool` | tree | No | none besides depth |
| provider token usage | measured when provider reports it | `agent.UsageLog` | session | No budget | no hard failure |

### Concrete gap

A root with `MaxTurns=100` and three children could spend 100 turns in the
root, 100 in each child, and more in grandchildren: the counters were per
`Run`, not per tree. Depth capped the shape, not the aggregate. Background jobs
were bounded per table and per job, but were not attributable to a root grant.
Compaction issued a provider request outside every counter.

This is the H0 failure Phase 5 addresses.

## 3. Authority, budget, completion ordering

Before the change, authority was already correctly ordered ahead of execution:
`execute` validates arguments, checks cancellation, calls `Authorize`, then
runs the tool. Completion was already centralized in Phase 4
(`agent.EvaluateTaskCompletion`, `tui.turnTaskOutcome`, `RunSubagent`).

No Phase 5 audit finding required changing the authority model, the security
gate, the sandbox, intent extraction, target evidence, alignment, or the Phase
4 evaluator. Phase 5 inserts budget admission after authority and before
execution, and adds one completion adapter for the resource-stop shape.

## 4. Existing failure taxonomy

Reused:

- `agent.ErrTurnLimit` — legacy turn-stop, resumable.
- tool `Result.IsError` — ordinary execution failure.
- `context.Canceled` — interruption.
- task statuses `PAUSED`, `NEEDS_REVIEW`, `FAILED`, `CANCELLED`, `COMPLETED`.
- Phase 4 completion states.

Added:

- `budget.ErrExhausted` / `*budget.ExhaustedError` with dimension, limit, used.
- `agent.Result.Exhausted` so a tool that refuses a dimension (background job,
  child) can surface the same resource stop to the loop.
- `agent.EvaluateBudgetCompletion` as the only Phase 5 completion adapter; it
  delegates semantic decisions to Phase 4.

## 5. Durable state

`task.Progress` gains an optional `budget` object: limits, cumulative used,
timestamp. It is optional on read so old checkpoints resume with a fresh
ledger rather than failing. `Controller.Begin` resets the ledger only for a new
task; resuming keeps it. `SetBudgetLimits` may raise the limits but never
rewrites consumed usage.

## 6. Test-first findings

Tests written before or alongside the implementation:

- `budget/budget_test.go` — exact boundary, all-or-nothing `AdmitAll`,
  concurrency, unmetered semantics, snapshot isolation, holder, state.
- `agent/budget_test.go` — turn/model/tool stops, no execution after a refused
  admission, legacy vs tree sentinel, child cannot reset, deadline.
- `agent/budget_completion_test.go` — tool refusal surfaces a resource stop;
  completion adapter only closes verified mutations.
- `tools/budget_test.go` — background admission, no job on refusal, root
  deadline caps background and foreground, expired deadline refusal.
- `task/budget_test.go` — ledger survives pause/resume, new task resets,
  run-budget freshness.
- `tui/budget_test.go`, `tui/budget_run_test.go` — Case A–E mapping, per-turn
  grant installation/recording.
- `cmd/ohgo/phase5_wiring_test.go` — production loop and spawner share one
  root; derived limits are finite.
- `cmd/ohgo/phase5_bench_test.go` — the 30-case adversarial benchmark.

## 7. Limitations found

- Provider token usage is not a hard budget: providers differ and some report
  none. Bytes are measured; tokens are not fabricated.
- Tool output bytes are bounded per call, not per tree, because a post-hoc
  charge cannot refuse the execution that produced them.
- Context bytes/tokens are bounded by compaction and the model window, not a
  byte grant.
- The wall-clock deadline is checked between model calls; an in-flight provider
  request is not interrupted by the budget deadline (pre-existing Phase 3
  behavior). Foreground and background shell commands are capped by it.
- A detached background job survives its parent turn by design; it is bounded
  by the per-job timeout, the root deadline, and the job table, not killed by a
  turn cancellation.
- Fairness is a shared first-admitted pool. No reservation or proportional
  policy is implemented.
- A resumed task receives a new run grant; the task ledger preserves consumed
  usage. A budget-exhausted task therefore needs an explicit new user turn and
  a grant that allows more work.
- Compaction summarisation is charged as a model call, but the summary request
  is not independently scoped to the task contract.

None of these invalidates hard containment for the metered dimensions. They are
recorded in the closure report rather than hidden behind a PASS claim.
