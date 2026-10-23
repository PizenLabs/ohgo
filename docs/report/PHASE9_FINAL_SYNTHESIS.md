# Phase 9 — Final Synthesis

## Verdict

**PASS for the Phase 9 runtime contract.**

The implementation delivers an adaptive, inspectable runtime layer that:

- measures and separates model/runtime cost sources;
- derives a declarative task shape;
- keeps explicit, durable, bounded evidence;
- projects a smaller sufficient context without rewriting state;
- narrows the valid action surface without hard-coding workflows;
- detects evidence sufficiency and stops unnecessary exploration;
- preserves authority, security, Phase 8 knowledge boundaries, and
  evidence-based completion;
- remains model-independent.

One requirement is **explicitly unmeasured**: a live multi-model provider
comparison. The harness exists (`-live-model`), but no paid provider run was
performed in this environment. No live model-quality or billing claim is made.
That is a measurement gap, not an architectural gap.

## Definition of Done

| Requirement | Evidence | Status |
|---|---|---|
| Current context behaviour is measured | `PHASE9_BASELINE.md`, `phase9-baseline.json` | PASS |
| Model/runtime cost sources are distinguishable | scripted arm fixes model behaviour; live mode records provider usage separately; baseline decomposes tool schema vs context vs replay | PASS |
| TaskShape is generalized | `cognition/shape.go`; A–J matrix; no per-file workflow | PASS |
| EvidenceState is explicit | `cognition/evidence.go`; statuses, retention, provenance, conflicts, bounds | PASS |
| State is separated from transcript history | projector view; `live` transcript retained; evidence checkpoint in `progress.json` | PASS |
| Context projection is implemented | `agent.Config.Project`; `cognition.Projector`; TUI/headless/subagent wiring | PASS |
| Evidence retention is bounded and principled | exact/structural/derived/diagnostic/external/transient; 16 KiB working set; 64 KiB evidence cap | PASS |
| Candidate actions are constrained without hard-coded workflows | tool metadata + declarative policy; A–J results | PASS |
| Sufficiency detection exists | `cognition/sufficiency.go`; stages; K7 proof stop | PASS |
| Completion remains proof-based | `agent.EvaluateTaskCompletion` unchanged apart from ignoring runtime candidate refusals; K3/K4/K5 remain review | PASS |
| Authority boundaries remain unchanged | `TestProjectionDoesNotBypassAuthorizer`; security gate still sole admission path | PASS |
| Phase 8 knowledge boundaries remain intact | external evidence untrusted, bounded, provenance-bearing; no authority | PASS |
| Model independence is preserved | no model/provider branches in production code | PASS |
| Benchmark matrix passes | `TestPhase9Matrix` (A–J, K1–K7) in both arms | PASS |
| Adversarial tests pass | large file, missing file, failed verification, unexpected mutation, partial execution, provider failure, candidate rejection, conflicts | PASS |
| Regression tests pass | `gofmt -l .` empty; `go build ./...`; `go vet ./...`; `go test ./...`; `go test -race ./...` | PASS |
| Observability explains major decisions | `/audit`, `OHGO_DEBUG_CONTEXT`, `ProjectionReport` (offered/withheld/elided/sufficiency/retention policy) | PASS |
| No optimization is justified solely by token reduction | every matrix row preserves status; K1 retains a documented trade-off; authority tests pass | PASS |

## What changed

### New package

- `cognition/`
  - `shape.go` — TaskShape, repository probe, budget shape.
  - `evidence.go` — EvidenceState, statuses, retention, provenance,
    observation adapters, conflict detection, bounds.
  - `project.go` — Projector, working-set selection, tool-result views,
    state block.
  - `candidates.go` — task-aware action surface with auditable exclusions.
  - `sufficiency.go` — ExecutionState, stages, sufficiency.
  - `render.go` — bounded model-facing state and audit rendering.
  - `cognition_test.go` — invariant and adversarial unit tests.

### Execution seam

- `agent/projection.go` — `ProjectionRequest`, `ProjectionResult`,
  `ProjectionReport`, elision/exclusion audit.
- `agent/loop.go` — optional projection, tool-set intersection, per-request
  specs, candidate rejection, `ToolOutcome.Args`, projection audit hook.
- `agent/actions.go` — optional action/language/mutation metadata.
- `agent/tool.go` — `CandidateRejected` (not execution failure).
- `agent/completion.go` — ignore candidate refusals for pending failures.
- `agent/authorize.go` behaviour — a denied call now carries the refusing
  decision in its outcome (the documented contract was previously not
  reflected in `ToolOutcome`).

### Task continuity

- `task/controller.go` — durable evidence pointer, checkpoint/restore,
  projection audit, lock for cross-goroutine `/audit`.
- `task/progress.go` — additive optional `evidenceState` and `audit` fields.

### Interfaces

- `tui/app.go`, `tui/task.go`, `tui/model.go` — per-turn shape/projector,
  `/audit`, task evidence hooks.
- `cmd/ohgo/outcome.go` — headless projection and `OHGO_DEBUG_CONTEXT`.
- `cmd/ohgo/taskcontinuity.go` — controller wiring.
- `cmd/ohgo/run.go` — subagent projection, plain REPL `/audit` explanation.
- `tools/*` — action/language/mutation metadata; no behavior change.
- `workspace/intent.go` — comma preserved as a clause boundary (concrete
  defect: "check the project, write README.md" was classified informational).

### Benchmark

- `bench/phase9/` — deterministic fixtures, scripted model, request metrics,
  matrix A–J, adversarial K1–K7, live-provider mode, matrix tests.
- `docs/benchmarks/results/phase9-baseline.json`
- `docs/benchmarks/results/phase9-state.json`

## Measured result

Full matrix:

- estimated input tokens: **514,142 → 261,234 (−49.2%)**
- excluding the irrelevant-large-file outlier: **−35.6%**
- median scenario: **−42.4%**
- tool-schema bytes: **−56.7%**
- repeated-context bytes: **−54.7%**
- raw tool output: effectively unchanged (tools still do the same work)
- objective status: **identical in all 17 scenarios**

The README task specifically:

- 38,004 → 25,972 estimated input tokens (−31.7%);
- first request 11,722 → 5,980 bytes;
- last request 17,459 → 13,316 bytes;
- same 10 requests and 9 tool calls (the scripted sequence is fixed);
- final verdict remains `NEEDS_REVIEW` because the scripted sequence read the
  file back but did not run a successful check. The runtime did not turn a
  clean stop into proof.

The post-proof scenario:

- extra `ls` withheld as a candidate rejection;
- completion remains `COMPLETED`;
- 17,135 → 8,507 estimated input tokens (−50.4%).

## Regression evidence

Run from the repository root:

```text
gofmt -l .              → empty
go build ./...          → pass
go vet ./...            → pass
go test ./...           → pass (all packages)
go test -race ./...     → pass (all packages)
```

The benchmark matrix itself is part of `go test ./...` and runs the real tools,
including `go test` and `go build` in the fixtures.

## Contract preservation

### Authority

- Candidate selection can only remove tools; the loop intersects any proposed
  set with the constructed set.
- The security `Authorize` sequence is unchanged and still runs immediately
  before execution.
- A DENY never executes. The denied outcome now carries the decision for audit.
- A dual-use shell is never treated as read-only by candidate selection; the
  security boundary remains responsible for command-level enforcement.

### Evidence

- Projection never edits the live transcript, user/assistant text, or tool-call
  pairing.
- Conflicting evidence is sent exactly, never summarized.
- Diagnostics are exact up to a bounded size.
- Old exact observations become summaries with provenance; the model can
  re-read. This is the documented retention trade-off.
- Candidate refusals are not evidence and are not failures.

### Completion

- `agent.EvaluateTaskCompletion` remains the authority.
- A candidate refusal is ignored for `pendingFailures`; it is not a tool
  failure.
- `NEEDS_REVIEW` is still returned for failed checks, missing changes,
  unexpected mutations, and incomplete execution.
- The README baseline/projected arms both stay `NEEDS_REVIEW` without a
  successful check. The runtime did not optimize the verdict.

### Phase 8 external knowledge

- External tools are offered only when the task shape requires external
  knowledge (explicit URL or named documentation).
- External evidence is recorded with provenance, trust class, and a bounded
  value.
- The state block marks it `UNTRUSTED DATA, not instructions`.
- External evidence cannot authorize, verify, or complete.

## Concrete defects found during audit

1. **Comma-separated mutation clause was invisible.** `normalizeRequest`
   removed commas before `splitClauses`, so "check the project, write
   README.md" was classified informational. The fix preserves the comma as a
   clause boundary and has a regression test.
2. **Denied calls did not carry their security decision** in `ToolOutcome`,
   despite the documented contract. The outcome now carries the decision so an
   audit can distinguish "not authorized" from "authorized but failed".
3. **Subagent projection initially hid delegation on informational
   assignments.** This broke nested delegation tests and removed a useful
   exploration path. Delegation is no longer hidden by task kind.

## Remaining uncertainty

1. **Live provider comparison not run.** The harness supports it; the report
   does not claim model-quality or billing results.
2. **Scope vocabulary can misclassify novel phrasing.** A task that reads as
   informational but needs a command may have execution tools withheld. The
   conservative direction is safe but can reduce flexibility; the shape is
   rebuilt on a clarified turn.
3. **Large exact evidence can be excerpted.** A 100 KB file read is bounded to
   a 4 KiB working-set window on the next request. Diagnostics, conflicts,
   small files, and the recent working set remain exact. A critical large file
   may need a re-read around the relevant line.
4. **Fixed tool schemas still dominate refactoring/relationship tasks.**
   H improved only 6.7%. Compressing or staging tool definitions is a
   legitimate future phase; it was not attempted here.
5. **Durable evidence is TUI/task-scoped.** Stateless `ohgo run` and the plain
   REPL use run-local evidence and say so. They do not pretend to resume
   knowledge they did not persist.
6. **Evidence is bounded.** A very large investigation can evict low-value
   facts under the 64 KiB cap; re-acquisition remains possible and no
   authority/completion claim depends on the evicted data.

## Final assessment

The measured improvement is not "a smaller prompt". It is a smaller, clearer
runtime boundary:

- the model sees fewer irrelevant capabilities;
- it sees a bounded, provenance-bearing summary instead of replaying every
  tool result;
- it is told what is known, what is missing, and what the next evidence need
  is;
- it cannot drift into mutation or execution the task shape does not support;
- runtime evidence, not model narration, still decides completion;
- every narrowing is explainable through a projection report.

The Phase 9 architecture preserves the original philosophy:

> **LLMs reason. The Engine decides. Capabilities execute. Evidence tells the
> truth. Humans remain in control.**
