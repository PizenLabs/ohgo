# Phase 5 Closure

## 1. Verdict

**PASS WITH LIMITATIONS — PHASE 5 CLOSED**

Hard containment is proven for every metered dimension: turns, model calls,
tool calls, delegated children, and background-job admissions. A root-owned
grant is shared by the entire tree, admission is atomic, and no child, tool, or
background job can reset it. Budget exhaustion cannot become completion without
Phase 4 evidence.

The limitations are explicit: tool output bytes and context/token usage are not
tree-budgeted, the wall-clock deadline stops new work but does not interrupt an
in-flight provider request, fairness is a shared first-admitted pool, and a
resumed task receives a new run grant while preserving its cumulative ledger.

## 2. Final Code Commit

- Final code commit:
  `c3ec17d0dcc33668f67718d9288ea2ea8c6c9c1a`
  (`feat(budget): phase 5 tree-wide resource budget`)
- Frozen Phase 4 boundary: `dbc1ece` / closure `d50ee24`
- This closure report is committed after the code commit and changes
  documentation only.

## 3. Root Budget Contract

`budget.Budget` owns fixed `Limits`, monotonic `Consumption`, and one mutex.

```go
type Limits struct {
    Turns, ModelCalls, ToolCalls, Children, BackgroundJobs int
    Deadline time.Time
}
```

`Admit` and `AdmitAll` check and charge under the same lock. A failed admission
charges nothing. `AdmitAll` is all-or-nothing, so a refused model call cannot
spend a turn. `ExhaustedError` carries the dimension, limit, and used count and
wraps `budget.ErrExhausted`.

Non-positive limits mean unmetered; they are never a hidden deny. The production
CLI derives finite values from `--max-turns`, `--max-depth`, and optional
per-dimension flags (spec §3).

Ownership:

- root task / command invocation creates the grant;
- the loop, tools, and every `RunSubagent` child receive the same pointer;
- the TUI task controller creates one fresh grant per user turn and records its
  consumption in the durable task ledger;
- no tool, model, child, UI, or background job can create or reset a grant.

## 4. Resource Dimensions

| Dimension | Metered | Unit | Limit source |
|---|---|---|---|
| turns | yes | admitted agent turn | `--max-turns` / root budget |
| model calls | yes | provider request, including compaction summary | `--max-model-calls` or derived |
| tool calls | yes | admitted tool execution | `--max-tool-calls` or derived |
| children | yes | `RunSubagent` execution | `--max-children` or derived |
| background jobs | yes | admitted detached job | `--max-background-jobs` or derived |
| wall time | yes | absolute inherited deadline | `--deadline` / root budget |
| output bytes | no (per-call cap) | bytes | bash 30,000; read 2,000 lines; grep 100 × 4 MiB |
| context bytes/tokens | no | provider window / compaction | model and compaction settings |
| retained jobs | no (per-table cap) | jobs | 32 retained, 8 running per table |
| delegation depth | no (structural cap) | levels | `--max-depth` |

## 5. Accounting Points

Charged once:

- **turn** — immediately before a provider request, together with the model
  call, after context preparation and cancellation check;
- **model call** — same atomic admission; the compaction summarizer charges the
  same dimension;
- **tool call** — after authority and cancellation re-check, before
  `Tool.Execute`;
- **child** — in `RunSubagent`, before child construction;
- **background job** — in `BashTool.spawnBackground`, after the table
  reservation and before `cmd.Start`; a refusal discards the reservation.

Not double-charged:

- a tool batch charges one unit per call, not per turn;
- a child charges one child unit and its own turns/model/tool calls, which are
  distinct resources;
- a background job charges one background unit and the bash call that requested
  it;
- retries are new calls and therefore new admissions;
- schema-invalid calls fail before tool admission and consume only the model
  call that proposed them.

## 6. Tree-Wide Enforcement

`agent.Config.Budget` is inherited by `RunSubagent`. The production spawner
reads one `budget.Holder`, so the top loop and every nested spawner resolve the
same instance. The holder is replaced only at a user-turn boundary, never by a
child.

Verified by:

- `agent.TestRootTurnBudgetStopsBeforeAnUnadmittedModelCall`
- `agent.TestModelCallBudgetStopsBeforeOpeningAStream`
- `agent.TestToolBudgetAdmitsExactlyTheRemainingCalls`
- `agent.TestChildCannotResetTheRootGrant`
- `cmd/ohgo.TestProductionLoopAndDelegationShareOneRootBudget`
- benchmark cases 04–09, 29, 30

The run also keeps the frozen per-run `MaxTurns` cap. With an explicit tree
budget, reaching that cap returns an error that matches both `ErrTurnLimit`
(for Phase 3 embedders) and `budget.ErrExhausted` (for Phase 5 classification).

## 7. Delegation

**PASS.**

Every child inherits cancellation, the absolute deadline, the same authorizer,
the same budget pointer, and the parent's turn cap. Depth remains structural.
`RunSubagent` admits a `children` unit before constructing the child; a refused
child produces no provider call. A child budget error is surfaced as a failed
`task` outcome with `Result.Exhausted`, and the parent loop turns it into a
resource stop instead of a clean delegation.

No child-facing API can reset a counter.

## 8. Background Jobs

**PASS (bounded, not root-killed on turn cancellation).**

- Running 8 and retained 32 per table remain.
- Each spawn admits one tree-wide background unit; refusal discards the table
  reservation and starts no process.
- The job timeout is capped by the root deadline; an expired deadline refuses
  before reservation.
- Foreground shell work is also capped by the root deadline.
- Child-created jobs use the shared tool set and the shared budget in
  production.
- Process groups and per-job timeout kills are unchanged.

A detached job survives its parent turn by design. It is bounded by its
10-minute maximum, the root deadline, and the table caps; a turn cancellation
does not kill it. That is a documented Phase 5 limitation, not an accounting
escape.

## 9. Cancellation / Deadline

**PASS within the documented boundary.**

- Cancellation before admission: no provider call, no charge
  (`TestPhase5Benchmark/12`).
- Cancellation during child execution: the child and parent stop with
  `context.Canceled` (`TestPhase5Benchmark/13`).
- Cancellation after authorization but before tool admission: no tool charge
  and no execution (loop re-check).
- Cancellation during background preparation: the reservation is discarded and
  the process is not started.
- Deadline: one absolute instant from the root budget, inherited by every
  child, consulted before each model call and by bash for foreground and
  background work. An expired deadline admits no new model call.
- A provider request already in flight is not interrupted by the budget
  deadline; the loop stops before the next request. This is the pre-existing
  Phase 3 behavior and is recorded as a limitation.

## 10. Budget Exhaustion

**PASS.**

`budget.ExhaustedError` is an explicit resource state. The loop returns the
turns completed before the stop; evidence is never discarded. A tool-level
refusal (background slot or child) surfaces the same error through
`Result.Exhausted`.

`tui.turnTaskOutcome` maps:

- budget stop + incomplete evidence → `PAUSED` with the exhausted dimension;
- budget stop + independently verified mutation → completion evaluation
  (possibly `COMPLETED`);
- cancellation → `PAUSED`;
- legacy `ErrTurnLimit` without a tree budget → `PAUSED("turn limit reached")`;
- real execution error → `FAILED`.

## 11. Completion Interaction

**PASS.** Phase 4 remains the semantic authority.

`agent.EvaluateBudgetCompletion` is the only adapter. It appends a synthetic
clean stop **only** when all of the following hold:

1. the run stopped on a budget error;
2. the contract is a mutation objective;
3. a definite mutation was observed;
4. a successful verification followed the last change.

Otherwise it evaluates the unchanged turns. It cannot add evidence, widen a
target, or override alignment.

| Case | Result |
|---|---|
| A. exhaustion before objective | `PAUSED`, not complete |
| B. exhaustion after verified objective | `COMPLETED` |
| C. exhaustion during verification | `NEEDS_REVIEW`/pause, not complete |
| D. child exhaustion | parent gets a failed task outcome and a resource stop |
| E. model claims success after exhaustion | not complete without evidence |

Benchmark cases 18–23 and `tui/budget_test.go` cover these.

## 12. Resumability

**PASS WITH A DEFINED GRANT BOUNDARY.**

- `task.Progress` persists `budget.State` (limits, cumulative used, timestamp).
- A new task starts a zero ledger.
- Resuming a paused/review task restores cumulative usage; an explicit new
  limit may raise the ceiling but never rewrites consumed usage.
- The current run grant is fresh at each user turn. A task that exhausted its
  grant can resume on a later user turn; it cannot reset itself, and the
  previous consumption remains in the checkpoint.
- Child, tool, and model actions cannot trigger a resume or a grant.

`task/budget_test.go` and benchmark cases 24–25 verify this.

## 13. Adversarial Benchmark

Command:

```sh
OHGO_PHASE5_BENCH=1 go test ./cmd/ohgo -run TestPhase5Benchmark -count=1 -v
```

30 cases, all pass. Cases run through the real loop, the real security gate,
and real read/write/bash/task tools. Delegation cases use a real recursive
`TaskTool`/`RunSubagent` tree; background cases start real processes.

Artifact: [`../benchmarks/results/phase5-resource.json`](../benchmarks/results/phase5-resource.json).

## 14. Safety Metrics

| Metric | Result |
|---|---:|
| budget overruns | 0 |
| executions without a tool admission | 0 |
| child budget escapes | 0 |
| background budget escapes | 0 |
| model calls after the root deadline | 0 |
| cancellation reported as completion | 0 |
| false completions | 0 |
| false reviews | 0 |
| cases | 30/30 pass |

Measured totals: 74 model calls, 41 tool-call outcomes, 23 actual tool
executions, 3 background jobs, 1,390 ms wall time (generated 2026-10-22).
The benchmark is deterministic except wall time.

## 15. Efficiency Metrics

- model calls: 74
- tool-call outcomes: 41
- actual tool executions: 23
- background jobs admitted: 3
- budget utilisation is recorded per case in the JSON (consumed vs limits)
- wall time: 1,390 ms total across 30 cases
- no case was stopped while a lower-budget useful execution was still
  available except where the case intentionally injects exhaustion

No claim is made that H1 is more efficient than H0. H1 is a containment
contract, not an optimisation.

## 16. Phase 1–4 Regression

All frozen suites pass:

| Check | Command | Result |
|---|---|---|
| gofmt | `gofmt -l .` | empty |
| build | `go build ./...` | PASS |
| vet | `go vet ./...` | PASS |
| full test | `go test ./... -count=1` | PASS |
| race | `go test -race ./... -count=1` | PASS |
| Phase 2 integrity | `go test ./bench/repository -count=1` | PASS |
| Phase 3 authority/sandbox | `go test ./agent ./security ./sandbox -run 'Test.*(Authorize|Security|Sandbox|Admit|ReadOnly)' -count=1` | PASS |
| Phase 3 cancellation/delegation | `go test ./agent ./tui ./tools -run 'Test.*(Cancel|TurnLimit|Subagent)' -count=1` | PASS |
| Phase 4 benchmark | `OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -count=1` | PASS |
| Phase 4 completion | `go test ./agent ./tools ./tui ./cmd/ohgo -run 'Test.*(EvaluateTaskCompletion|Verification|Semantic|TargetSnapshot)' -count=1` | PASS |

The Phase 4 benchmark artifact was restored to its committed contents after
the verification run; Phase 5 did not change Phase 4 expectations.

## 17. Performance

Artifact: [`../benchmarks/results/phase5-performance.txt`](../benchmarks/results/phase5-performance.txt).
macOS / Apple M1 Pro / go1.27.2, synthetic:

| Probe | Result |
|---|---:|
| bare atomic increment | 11.05 ns/op |
| budget admission, serial | 18.17 ns/op |
| budget admission, 8 goroutines | 103.97 ns/op |
| budget snapshot | 21.46 ns/op |
| 500-tool-turn agent loop, completed streams | 20.6 ms total (~41 µs/turn) |
| 100 delegated one-turn children, shared grant | 449 µs (~4.5 µs each) |

Accounting adds roughly 7 ns over a bare atomic counter in the serial path.
The 8-goroutine number reflects lock contention, not a production claim. Model
and tool latency remain orders of magnitude larger. This is a synthetic probe,
not production-scale measurement.

## 18. Proven

- One root-owned `Budget`, shared by every descendant through an explicit
  pointer/holder.
- Atomic all-or-nothing admission; a failed admission executes nothing.
- Tree-wide aggregate enforcement for turns, model calls, tool calls, children,
  and background-job admissions.
- Child inheritance of cancellation, deadline, authority, depth, and budget;
  no child reset.
- Background job admission, deadline capping, and table bounds.
- Explicit exhaustion state and dimension, with no false completion.
- Phase 4 completion remains authoritative; verified work can complete after
  exhaustion, unverified work cannot.
- Durable cumulative task budget ledger across pause/resume.
- All 30 adversarial cases and all Phase 1–4 suites pass, including race.
- Measured synthetic accounting overhead is small relative to model/tool cost.

## 19. Implemented but Limited

- **Output bytes** are capped per call, not tree-wide. A post-hoc byte charge
  cannot refuse the call that produced the bytes.
- **Context/token usage** is not a hard budget. Provider usage is recorded when
  available; no token grant is fabricated.
- **Deadline granularity**: an in-flight provider request can finish after the
  deadline; no new request is admitted. Foreground/background shell commands
  are capped by the deadline.
- **Background cancellation**: a detached job survives its parent turn and is
  bounded by its timeout, the root deadline, and the table; turn cancellation
  does not kill it.
- **Fairness**: one shared first-admitted pool. A child can consume all
  remaining budget; cases 09 and 29 test that this stops the tree rather than
  overrunning it.
- **Resume grant**: a resumed task gets a fresh run grant; cumulative
  consumption is preserved in the ledger, but the default grant is not
  automatically reduced by prior use.
- **Manual `/compact`** runs outside an execution tree and is not charged.
- **Highly concurrent admission** costs one mutex serialisation per admission;
  no lock-free optimisation was added.

## 20. Not Proven

- A hard provider-token or monetary budget.
- Exact context-byte accounting across providers, caching, and tokenisers.
- Production-scale contention or latency.
- Fair allocation among many children beyond the shared-pool stop.
- That a background job is always killed on process exit by the OS; only its
  timeout, deadline, and process-group kill are proven.
- Semantic correctness of the work that consumes the budget (Phase 4 scope).
- That a resumed task with the same default grant can make progress after a
  complete grant exhaustion; it needs a new user turn, and progress may require
  a larger explicit grant.

## 21. Deferred

- Per-child reservations or proportional allocation.
- A tree-wide output-byte or context-byte budget with pre-execution admission.
- Interrupting an in-flight provider request at the budget deadline.
- Provider-token-based budgeting and cost accounting.
- Killing detached background jobs on task completion/cancellation.
- Lock-free admission for very high concurrency.
- A UI budget inspector beyond the persisted ledger and CLI flags.

These require separate hypotheses and evidence; they are not Phase 5 claims.

## 22. Final Decision

**FROZEN — PASS WITH LIMITATIONS.**

The Phase 5 contract is satisfied for the dimensions it claims to meter. A root
grant is causally enforced across the tree; admission is atomic; children cannot
reset or escape it; background jobs are charged and deadline-capped; exhaustion
is explicit and resumable; completion still requires Phase 4 evidence; the
durable ledger survives resume; and Phase 1–4 remain green, including race and
the Phase 4 benchmark.

The limitations are explicit, bounded, and do not reopen the authority,
completion, or resumability contracts.
