# Phase 6 Final Synthesis

- **Date:** 2026-10-22
- **Tree audited:** `6a9765f` (`feat(graph): add bounded import graph`), clean working tree
- **Scope:** Phase 1–6 source, tests, benchmark artifacts, reports, and Git state
- **Audit mode:** source-first, evidence-classified, no implementation, no commit
- **Decision frame:** *Dynamic Path, Static Authority, Truthful State Transition*
- **Companion:** [`PHASE7_TERMINAL_BOUNDARY.md`](PHASE7_TERMINAL_BOUNDARY.md)

## 0. Audit Method and Evidence Standard

The audit read the production source before accepting any prior report:

- `agent/`, `ast/`, `budget/`, `contract/`, `security/`, `task/`, `tools/`,
  `workspace/`, `tui/`, and `cmd/ohgo/`;
- the Phase 1–6 closure and audit reports;
- the Phase 4, 5, and 6 benchmark artifacts;
- Git history and the Phase 6 diff from `30aa541`.

Executed checks on the audited tree:

```text
gofmt -l .                                  empty
go build ./...                              PASS
go vet ./...                                PASS
go test ./... -count=1                      PASS
go test -race ./... -count=1                PASS
go run ./bench/repository -dry-run          PASS (319 files, 295 Go, 27 tasks, 81 cells)
```

The Phase 2 live-model, Phase 4 scripted, Phase 5 scripted, and Phase 6 static
benchmarks were **not rerun** in this audit. Their committed artifacts were read
and checked against the source and against the claims made about them. Three
small throw-away completion probes were run outside the repository to settle
questions the existing tests do not cover; they are labelled **AUDIT PROBE** and
are not committed evidence.

Evidence labels used below:

| Label | Meaning |
|---|---|
| **SOURCE** | The production code path was read directly. |
| **TEST** | A committed test exercises the exact claim. |
| **BENCH** | A committed benchmark artifact supports the claim. |
| **INFERRED** | A conclusion follows from the source but was not directly run. |
| **DOC ONLY** | A report or comment says it; the source does not establish it. |
| **UNKNOWN** | No evidence was found. |

A unit test is not evidence of a broader property unless the test establishes
that property. A synthetic benchmark is not production scale. A deterministic
benchmark is not semantic correctness. A live model result from one model and
one fixture is not a general model claim.

Key conclusions and their tier:

| Conclusion | Tier |
|---|---|
| One production structural source and one parser pass | **SOURCE + TEST** |
| Phase 2 agent-level discovery comparison | **BENCH** (historical live model, one fixture) |
| Phase 4 23-case completion result | **BENCH** (scripted, deterministic) |
| Phase 5 30-case budget result | **BENCH** (scripted, deterministic) |
| Phase 6 H0/H1 comparison | **BENCH** (static tool-level, no model) |
| Production authority path | **SOURCE + TEST** |
| Execution/empty-answer false completion | **SOURCE + AUDIT PROBE** |
| Resumed-task false completion | **SOURCE + AUDIT PROBE** |
| Background mutation/failure evidence | **SOURCE + AUDIT PROBE** |
| Cancellation-to-transition race | **SOURCE / INFERRED** (not reproduced deterministically) |
| Graph reverse cancellation gap | **SOURCE** |
| Production-scale performance | **UNKNOWN** |
| Live-model graph value | **UNKNOWN** |
| Documentation/tool-list drift | **SOURCE** |

---

## 1. Verdict

**PASS WITH LIMITATIONS — PHASE 6 GRAPH CONTRACT.**

**HOLD — TERMINAL-COMPLETENESS CLAIM.**

The Phase 6 graph itself satisfies its declared contract: it is one bounded,
read-only package/import projection over the existing `ast.Index` parse; it
does not add a second index, authority path, budget, or completion path; its
static H0/H1 benchmark is reproducible from the committed artifact and shows a
real precision improvement on the measured tasks.

The system as a whole is **not yet terminal-complete**. The audit found
reproducible false-completion or false-state paths in the Phase 3/4 completion
layer, outside the graph:

1. an execution objective with no named check completes after a clean stop even
   when nothing executed;
2. an informational objective completes on an empty final assistant message;
3. the one-shot `ohgo run` and plain REPL paths never apply the Phase 4
   completion evaluator, and a one-shot run can exit `0` after unverified work;
4. a resumed `NEEDS_REVIEW` task can be marked `COMPLETED` by a later turn whose
   evidence belongs to a different contract;
5. a background job can claim `Mutates` before it has run, and a failed or
   timed-out background job is reported by `wait` as a successful observation;
6. the TUI cancellation-to-task-transition sequence has a check-then-act race
   that can commit `COMPLETED` after the user cancelled.

Those are Phase 7 closure defects, not reasons to add a new capability. The
Phase 6 graph is not responsible for them, and none of them requires a second
repository index, a call graph, a semantic judge, or a new authority layer.

---

## 2. System Status

OHGO after Phase 6 is a coherent single-index, single-production-admission
coding agent for its CLI/TUI scope. Authority, budget admission, structural
coordinates, bounded package relationships, and the TUI task lifecycle are
structurally sound and well tested.

It is not yet a system that can honestly claim **completion** for every
production execution path. Completion is authoritative only in the TUI task
path and in writable delegated children. The stateless paths do not evaluate it,
a resumed task does not retain its original contract, and background execution
does not yet produce truthful completion evidence. Phase 7 must close those
paths before "OHGO — FINAL / COMPLETE" is defensible.

---

## 3. Phase 1–6 Evidence Matrix

| Phase | Objective | Proven | Remaining Limitation | Status |
|---|---|---|---|---|
| **1 — Repository Coordinates** | Symbol-first coordinates from one Go structural backend | One `ast.Index`; one `GoBackend.Parse` pass; deterministic path/package/symbol/kind/line coordinates; explicit ambiguity, truncation, parse failure; freshness rebuild; cancellation before/after build; containment | The historical "exactly one structural tool" is now two surfaces (`symbols`, `graph`) over one source. Same-size + same-mtime rewrite is undetected. Index build cannot be cancelled mid-parse. Full stat walk on every query. | **FROZEN — PASS; one-surface claim superseded by Phase 6** |
| **2 — Discovery Benchmark** | Compare lexical, structural, hybrid discovery | 81/81 valid cells; 27 tasks × 3 tracks; structural cheaper (3 vs 7 turns, 1 vs 4 discovery calls, 2.1 vs 14.9 KB context); lexical still necessary on literal tasks; `.gitignore` defect fixed and replayed | One model (`cohere/north-mini-code:free`), one fixture, one sample per cell; variance comparable to some track differences; cross-file tasks unsolved; historical revision `53bc07b`; not rerun in this audit | **FROZEN — PASS WITH LIMITATIONS** |
| **3 — Authority, Execution, Completion** | One admission path; truthful failure/cancellation/state | Production `Agent.Loop` uses `securityGate`; ASK/FUZZ/BUILD enforcement; OS read-only workspace boundary; one-shot/REPL/TUI route before model; turn cap; death of process group; nested delegation inherits gate/deadline/budget/depth | Legacy `Admit`/`Approve` remains when `Authorize` is nil; headless paths do not use completion; background `wait` loses failure status; post-run cancellation race; file tools not exclusive; read-only child flag is unusable in production | **PASS WITH LIMITATIONS — Phase 7 completion work required** |
| **4 — Intent-Aligned Completion** | Deterministic contract, target evidence, verification scope, alignment | `IntentContract` from user text; target snapshot before run; SHA-256 path/symbol/package checks; create/modify/delete direction; unexpected-mutation detection; verification kind/scope coverage; one evaluator used by TUI and writable children; the committed 23-case scripted artifact reports 11 H0 false completions → 0 H1; not rerun | Completion is not universal: execution-without-check and empty-answer false positives; one-shot/REPL outside completion audit; resumed task contract not persisted; TUI post-run cancellation race | **PASS WITH LIMITATIONS — authoritative only where installed** |
| **5 — Resource-Bounded Execution** | One root-owned budget; tree accounting; atomic admission | One `budget.Budget` per run; turns/model calls/tool calls/children/background jobs; atomic `AdmitAll`; deadline inheritance; child cannot reset; background admission; exhaustion is explicit; 30/30 scripted cases; full suite/race | No token/byte budget; per-run grant is fresh at each user turn; cumulative ledger is not enforced; fairness is a shared pool; in-flight provider/tool work can outlive deadline; detached jobs survive; legacy `agent.New` creates a partial run-local budget | **FROZEN — PASS WITH LIMITATIONS** |
| **6 — Repository Graph Intelligence** | Bounded package/import relationships over the existing index | One `StructuralSource` shared by `symbols`/`graph`; one parse; deterministic `ImportSpec` edges with source/test flags and file:line witnesses; reverse query over same edges; module-local resolution; explicit external/unresolved; freshness incl. `go.mod`; ambiguity; depth/node/edge/work bounds; read-only; tool-call budget; shared child source; H1 8/8 static, 0 false edges, 33.8% context reduction | No call/reference/implementation/file/symbol graph; no incremental freshness or rebuild; source-level only (build tags/generated included); no cross-module; no live-model H0/H1 evidence; verification targeting measured but not wired; reverse-adjacency cancellation gap; first lazy index build is not cancellable | **PASS WITH LIMITATIONS — graph contract closed** |

### 3.1 What OHGO Has Actually Proven

**Proven by source and tests**

- One production structural source: `tools.NewStructuralSource` is shared by
  `SymbolsTool` and `GraphTool`; production `buildTools` constructs one source.
  `ast.NewIndex` is called by that source and by the benchmark, not by a second
  production path.
- One parser pass produces package clause, declarations, and imports; the graph
  is derived from the same `fileRecord` set. The index/parse ratio measured in
  Phase 6 is 0.99–1.00 on synthetic trees.
- Symbol coordinates are deterministic and bounded; ambiguity, truncation,
  not-found, parse failure, and containment are explicit.
- Phase 6 edges are deterministic `ImportSpec` declarations, with source/test
  distinction, deterministic witnesses, module-local resolution, external
  counts, and unresolved records.
- Freshness invalidates the graph on changed Go files and on a changed `go.mod`
  fingerprint; a graph query returns an error rather than the previous snapshot
  when freshness or rebuild fails.
- The graph tool is `TierRead`, follows the same path containment as `read`, is
  admitted by the existing security gate in ASK/FUZZ/BUILD, and consumes one
  tool-call unit.
- Production authority paths are gated by `securityGate`; production budget
  paths share a root budget; nested children inherit both.
- The TUI task path uses `agent.EvaluateTaskCompletion`, target snapshots, and
  `task.Controller`; the Phase 4 23-case scripted artifact shows 0 H1 false
  completions on those cases.
- The Phase 5 30-case scripted artifact shows no measured budget overrun,
  execution after failed admission, child/background escape, deadline escape,
  cancellation escape, or false completion on those cases.
- The Phase 6 H0/H1 artifact is internally consistent: 4/8 vs 8/8 correct,
  4 vs 0 false positives, 4 vs 0 false edges, 8 vs 8 tool calls, 7 graph
  queries, 10,780 vs 7,140 context bytes, and 47.1%/61.7% reductions on the
  transitive tasks.
- Go format, build, vet, full test, and race suite pass on the audited tree.

**Proven by benchmark, with scope**

- Phase 2: structural discovery is materially cheaper than lexical discovery on
  the measured declaration tasks; lexical remains necessary for literal/text
  tasks; hybrid is the most robust practical selection. One model, one fixture.
- Phase 4: the target/verification/alignment evaluator removed the 11 false
  completions present in H0 on the 23 scripted cases, with no false reviews in
  that matrix.
- Phase 5: root-owned admission is atomic and tree-wide for the five metered
  dimensions on the 30 scripted cases.
- Phase 6: package/import relationships remove four lexical false positives and
  reduce transitive context on the 8 static tool-level tasks.

### 3.2 What Remains Unproven

- That a real model chooses `graph` when available and improves its answer or
  turn count. The live harness exists but no valid live artifact was produced;
  the committed Phase 6 artifact is a deterministic tool-level probe.
- That graph-derived verification scopes improve production completion
  correctness. They were measured as candidate scopes and are not wired into
  Phase 4.
- That completion holds for every production entry point. It does not today.
- That a resumed task is evaluated against the contract that produced its
  `NEEDS_REVIEW` state. It is not today.
- That background jobs produce truthful failure and verification evidence. They
  do not today.
- Exact call, reference, implementation, build-tag, vendored, replaced, nested
  module, cross-module, or generated-code semantics.
- Production-scale latency, memory, contention, or provider behavior. All
  performance numbers are synthetic or scripted.
- Semantic correctness: a wrong change with a passing test can still complete.
- Absolute freshness under timestamp-preserving same-size rewrites.

### 3.3 What Remains Technically Incomplete

- Headless completion and exit semantics.
- Persisted task contract across review/resume.
- Background failure/verification evidence.
- Cancellation linearization at the task transition.
- Reverse-adjacency graph cancellation.
- Exact architecture/tool documentation for the current surface.
- File-tool serialization and read-size bounds (robustness, not core authority).

---

## 4. Architecture

### 4.1 Actual End-to-End Path

The actual production causal path is:

```text
HUMAN
  ↓
CLI entry
  ├─ TUI runTurn
  ├─ plain REPL
  └─ one-shot runOnce
  ↓
workspace.RouteTurn (all three production entry points)
  ↓
authorityGate handler (narrowed for the turn; active workspace unchanged)
  ↓
task.Begin / task budget grant                 [TUI only]
  ↓
agent.Loop.Run
  ↓
model proposal
  ↓
agent.Loop.execute
  ↓
schema validation
  ↓
BeforeToolCall (no production hook installed)
  ↓
securityGate.Evaluate → AUTO / APPROVE / DENY
  ↓
cancellation re-check
  ↓
budget.Admit(tool_calls)
  ↓
tool.Execute
  ↓
agent.Result / ToolOutcome
  ↓
completion
  ├─ TUI: EvaluateTaskCompletion + CheckTargets + alignment → task.Controller
  ├─ writable child: EvaluateTaskCompletion → TaskTool evidence
  └─ one-shot / plain REPL: no completion evaluator
  ↓
task state / process exit / transcript
```

`symbols` and `graph` are tools on the model-proposal branch. They are not a
separate authority or completion branch. The graph reads the same
`StructuralSource`/`ast.Index` that `symbols` and the Phase 4 resolver use.

### 4.2 Divergences From the Reported Architecture

1. **Task state is TUI-only.** The reported path `workspace.RouteTurn →
   task.Begin → agent.Loop.Run → … → task state` is exact for the TUI. The
   plain REPL and one-shot command share routing, the loop, tools, and budget,
   but do not create or transition a durable task.
2. **Completion is not one universal call site.** The TUI uses
   `agent.EvaluateTaskCompletion` through `tui.turnTaskOutcome`. Writable
   subagents use it through `agent.RunSubagent`; read-only children use the
   frozen `EvaluateCompletion`. One-shot and plain REPL use neither.
3. **The production authorizer is `securityGate`, not `Config.Admit`.** The
   legacy `Admit`/`Approve` pair is still a public loop path when `Authorize` is
   nil. Production `buildLoop` always installs `Authorize`, but `agent.New` does
   not require it.
4. **The budget is per run, not cumulative across user turns.** The TUI creates
   a fresh root grant per turn and records the snapshot in a durable ledger; the
   ledger is not enforced as a ceiling on the next grant.
5. **The graph is a second structural *surface*, not a second index.** Phase 1's
   "one structural tool" statement is historically true and currently
   superseded; the one-source invariant remains true.
6. **Background work is outside the completion boundary.** A spawn returns
   immediately, the model may never wait, and the task evaluator has no
   visibility into a running job.

---

## 5. Invariant Results

| Invariant | Evidence | Result |
|---|---|---|
| **I1 — Authority** | Production `buildLoop` installs `securityGate`; `agent.Loop.execute` evaluates every call; DENY is terminal; APPROVE requires a fresh confirming evaluation; path scope is normalized; `workspace.Permits` is the single table. **SOURCE + TEST.** | **PASS in production.** Caveat: `agent.New` permits a nil `Authorize` and then admits every call through legacy hooks/nil. This is a fail-open library seam, not a production CLI path. |
| **I2 — Budget** | One `budget.Budget` pointer per run; `AdmitAll` atomic; children share; background admission; 30-case artifact. **SOURCE + TEST + BENCH.** | **PASS within a root run.** Caveats: TUI creates a fresh grant per user turn; `agent.New` with nil budget derives only turns/model calls; the cumulative task ledger is not an enforced budget. |
| **I3 — Discovery** | `SymbolsTool` and `GraphTool` only read `ast.Index`; graph has no write/execute path; path containment is `tools.resolvePath`; before/after tests. **SOURCE + TEST.** | **PASS.** |
| **I4 — Verification** | `graph` and `symbols` never set `Verifies`; graph `Result` carries no verification; mutation evidence does not set verification. **SOURCE + TEST.** | **PASS for discovery.** Background spawn incorrectly claims mutation before observation (I11/I5 issue), but it does not claim verification. |
| **I5 — Completion** | TUI `turnTaskOutcome` and `task.Controller` are the only durable task transition; `task.Complete` transition is guarded; writable children use the same evaluator. **SOURCE + TEST.** | **PARTIAL.** Only task-bearing TUI/child paths. Headless paths do not transition `COMPLETED` but can exit successfully; a resumed task can be completed by an unrelated contract. |
| **I6 — Cancellation** | TUI checks `ctx.Err()` before task transition; loop returns cancellation; cancellation tests; post-run check. **SOURCE + TEST.** | **PARTIAL.** The check and `task.Complete` are not atomic; a cancel landing after the check can be lost. Graph reverse-adjacency work can return a truncated result instead of the context error. |
| **I7 — Exhaustion** | `budget.ExhaustedError`; `EvaluateBudgetCompletion` only after evidence; 30 cases; no false completion on those cases. **SOURCE + TEST + BENCH.** | **PASS within the tested budget paths.** Deadline expiry returns `nil` by design; completion evidence can still authorize `COMPLETED` after a verified objective. This is documented, not "exhaustion becomes success." |
| **I8 — Delegation** | `RunSubagent` inherits `Authorize`, deadline, turn cap, budget, tools, compaction; depth structural; child evidence propagated; tests. **SOURCE + TEST.** | **PASS for containment.** The advertised read-only child flag is unusable in production because the spawner passes the full tool set and `RunSubagent` refuses; it fails closed. |
| **I9 — Freshness** | Size+mtime snapshot; `go.mod` fingerprint for graph; rebuild errors are returned; same-size rewrite test; stale-edge tests. **SOURCE + TEST.** | **PASS within the declared fingerprint.** Same-size + same-mtime rewrite remains undetected; first index build is not context-cancellable. |
| **I10 — Ambiguity** | `symbols` returns `ambiguous` for multi-match lookup; graph returns candidates for package/symbol selectors; package identity conflicts marked; no arbitrary winner. **SOURCE + TEST.** | **PASS.** |
| **I11 — Evidence** | Completion scans `Turn.Results`; target snapshots compare hashes; graph does not contribute; failure/mutation/verification flags come from tools. **SOURCE + TEST.** | **FAIL.** Reproducible gaps: execution objective with no execution; empty informational answer; background `Mutates` before execution; failed background `wait` not an error; resumed task contract mismatch. |
| **I12 — Human Control** | Approval gate; DENY before approval; `--yes` and settings allow are explicit user decisions; workspace switch explicit; no model authority. **SOURCE + TEST.** | **PASS in production,** with the stated `--yes`/settings consequences. Default workspace is BUILD, which is a deliberate CLI default, not model-derived authority. |

---

## 6. Remaining Gaps

### A — MUST FIX BEFORE PHASE 7 CAN CLOSE

These are correctness/state-transition defects, not wishlist items.

**A1. Execution objectives can complete with no execution evidence.**

`workspace.BuildIntentContract("run ls")`, `"execute echo hello"`, and
`"run the script"` produce `ObjectiveExecution` with no verification
requirement. `agent.EvaluateTaskCompletion` only rejects a missing check when a
verification requirement exists; a clean stop with zero tool calls returns
`COMPLETED`. **AUDIT PROBE confirmed all three return `COMPLETED`.** This
violates I11 and the Phase 4 claim that completion is evidence-bound.

Acceptance: an execution objective with no successful `Executes` result and no
successful verification must return `NEEDS_REVIEW`.

**A2. An informational objective completes on an empty answer.**

A final assistant turn with no text and no tool calls returns `COMPLETED` for
`"explain this"`. **AUDIT PROBE confirmed.** A model can stop cleanly and
produce nothing; the task is still marked complete. This violates I11.

Acceptance: an informational objective requires non-empty visible assistant
text in the final answer, or `NEEDS_REVIEW`.

**A3. Headless paths bypass completion and can report success falsely.**

`cmd/ohgo/runOnce` calls `loop.Run` and returns its error; it never builds an
intent contract, captures target evidence, evaluates completion, or reports
`NEEDS_REVIEW`. The plain REPL prints turn output and errors but has no
completion projection. Phase 4 explicitly documents these paths as outside the
completion-state audit, but the process exit code is still a success claim.
**SOURCE.**

Acceptance: one-shot evaluates the contract and exits non-zero for
`NEEDS_REVIEW`, `PAUSED`, `FAILED`, or a refused route; plain REPL prints the
same verdict. No durable task is required in stateless paths.

**A4. A resumed `NEEDS_REVIEW` task can be completed by unrelated evidence.**

`task.Controller.Begin` preserves the old `Task.Objective`, but the TUI
recomputes `IntentContract` from the new user text. The task checkpoint stores
no contract, targets, or review reason. `Begin("continue")` moves a
`NEEDS_REVIEW` task to `RUNNING`; a clean informational turn then evaluates
`COMPLETED`, and `Controller.Complete` accepts. **AUDIT PROBE confirmed a task
"fix note.txt so tests pass" in `NEEDS_REVIEW` became `COMPLETED` after
`Begin("continue")` plus a clean informational turn.** The direct
`Task.Complete` guard in `task/task.go` does not prevent this because `Begin`
first resumes the task.

Acceptance: the task's authoritative contract is persisted and used for
completion; a resumed task cannot complete on evidence for a different
contract. A human has an explicit way to abandon or replace a review task.

**A5. Background job evidence is not truthful.**

`BashTool.spawnBackground` returns `Mutates: mutates` from the command
classifier **before** the process has run; `wait` never sets `IsError` for a
non-zero exit or timeout, and never sets `Verifies` for a successful recognized
check. **AUDIT PROBES:** `sleep 1; echo x > f` returned `Mutates=true` at spawn
and `wait` returned `IsError=false, Mutates=false`; `false` returned
`IsError=false`; a synthetic still-running spawn outcome (`Mutates=true`) plus
a foreground test evaluation returned `COMPLETED`. This violates I4/I11 and the
failure taxonomy.

Acceptance: a spawn is execution evidence, not definite mutation; a failed or
timed-out job is an error; a successful finished check is verification evidence;
a running job is not completion evidence.

**A6. Cancellation can be lost at the task transition.**

`tui.Program.runTurn` checks `ctx.Err()` and then calls `p.task.Complete`; the
cancel path (`cancelTurnNow`) is not synchronized with that check/transition.
A cancellation that lands between the check and the transition can still yield
`COMPLETED`. This is a source-level check-then-act race, not covered by the
existing deterministic test. Violates I6.

Acceptance: cancellation and the terminal transition have one linearization
point; a cancel that precedes the transition cannot produce `COMPLETED`.

### B — SHOULD FIX IN PHASE 7

These materially improve correctness, determinism, or production robustness but
are not by themselves proof of an invariant bypass.

- **B1. Graph reverse-adjacency cancellation.** `ast.Index.Graph` builds the
  reverse adjacency without checking `ctx` inside the loop. If the work limit
  is reached during that build, the traversal is skipped and a `truncated`
  result is returned without observing cancellation. The Phase 6 spec claims a
  context check at least every 256 edge inspections; the implementation checks
  per node, not per 256 edges. **SOURCE.**
- **B2. First lazy index build is not cancellable.** `StructuralSource.Index`
  calls `ast.NewIndex` without a context; a cancelled first graph/symbol query
  waits for the full build. Inherited Phase 1 limitation, but the Phase 6
  cancellation claim should say so explicitly. **SOURCE.**
- **B3. Concurrent file tools can lose writes.** `write`, `edit`, and `delete`
  inherit `Base.Exclusive == false`; the loop runs shared calls concurrently.
  Two calls targeting the same file in one model batch can race, and both can
  report success. **SOURCE.** This is a mutation-correctness risk, not a
  completion invariant.
- **B4. Legacy authority and budget seams are fail-open.** `agent.New` accepts
  `Authorize == nil` and `Budget == nil`; with both absent, calls execute with no
  security gate and only turns/model calls budgeted. Production never does this,
  but the library contract is weaker than the architecture claims. **SOURCE.**
- **B5. Read-only delegation is advertised but unusable in production.** The
  spawner passes the full tool set; `RunSubagent` refuses a `ReadOnly` request
  against it. `ReadOnlyToolNames` also omits `ls` and is name-based. Fails
  closed, but the feature is not delivered. **SOURCE + TEST.**
- **B6. `BeforeToolCall` rewrites are authorized against the original
  arguments.** The loop validates rewritten `args` but calls
  `Authorize.Evaluate(call, tool)` with the original `call.Arguments`, and the
  approval preview also derives from the original call. Production installs no
  hook, but the seam is unsafe if a host uses it. **SOURCE.**
- **B7. `AfterToolCall` can erase failure evidence.** The hook can set
  `IsErr=false` after execution; production installs none, but completion
  consumes the overridden result. **SOURCE.**
- **B8. `read`, `write`, `edit`, and `delete` have no file-size bound.** Each
  reads the whole file into memory; `read` displays at most 2,000 lines but
  allocates the full file first. The target verifier has a 4 MiB/16 MiB bound;
  the ordinary tools do not. **SOURCE.**
- **B9. Headless cancellation and shutdown are not wired.** One-shot and plain
  REPL use `context.Background`; Ctrl+C terminates the process without a
  cancellation path, and a detached background job can outlive the process.
  **SOURCE.**
- **B10. `--deadline` is session-absolute in interactive modes.** The TUI/REPL
  resolve one absolute instant at startup and reuse it for every turn; after it
  expires, later turns admit no work. This is consistent with "whole run" only
  if "run" means the whole session. Document or make it per-turn explicitly.
  **SOURCE.**
- **B11. Architecture documentation is stale.** `docs/architecture/
  ARCHITECTURE.md` still lists the pre-Phase-6 tool set and describes
  `Config.Admit` as the production authority path. The Phase 1 report's
  "exactly one structural tool" statement needs a historical annotation. The
  Phase 6 spec's 256-edge cancellation wording is not implemented. **SOURCE.**
- **B12. Dead/port packages.** `shell/` and `memory/` have no production
  importer; they are maintained test-bearing packages with no consumer. They
  are not repository intelligence paths, but they are scope and maintenance
  debt. **SOURCE.**
- **B13. No explicit review acceptance/abandon action.** A `NEEDS_REVIEW` task
  is non-terminal, but there is no one-key way to accept or abandon it; a new
  request is attached to the old task. This compounds A4. **SOURCE.**

### C — ACCEPTABLE LIMITATIONS

Each of these can remain in the terminal system if documented permanently and
if A1–A6 are closed.

| Limitation | Why acceptable | What is not proven | Does it invalidate core guarantees? |
|---|---|---|---|
| Package/import graph only | It is the smallest relation the Go parser proves deterministically; it answers the measured dependency/reverse/multi-hop questions | Calls, references, implementations, symbol/file nodes, exact semantic impact | No. Graph evidence is advisory, not authority or verification. |
| No cross-module, vendored, `replace`, nested-module resolution | The declared design scope is a single module; unresolved/external are explicit | Multi-module repository behavior | No. Edges outside the module are not claimed. |
| Source-level, not build-context-aware | Generated and build-tagged files are intentionally indexed and reported; edges are declaration evidence | What the compiler actually builds | No, provided the output says source-level. |
| Full freshness walk and full rebuild | Bounded; measured 0.9–25.9 ms warm walk and 6–168 ms rebuild on 100–3,000 synthetic files | Production-scale latency; incremental invalidation | No. A stale snapshot is not served silently. |
| Same-size + same-mtime rewrite undetected | Normal editor writes update mtime; the limit is explicit and inherited | Absolute freshness under timestamp preservation | No. This is a known blind spot, not a false claim of freshness. |
| No live-model H0/H1 graph evidence | Live providers were unavailable/incompatible; the static probe measures information, not model selection | That a real model uses `graph` or improves turns/answers | No. The Phase 6 claim is explicitly tool-level. |
| H1 direct small queries are larger | Structured metadata costs bytes; the measured reduction is in transitive tasks | Per-query monotonicity | No. It is reported honestly. |
| Verification is defined-check evidence | Tests/build/vet plus target hashes are observable; the runtime does not claim semantic intent | A wrong change with passing tests | No, provided the limitation is permanent. |
| No token/byte budget | Provider windows, compaction, and per-call caps bound context; no hard monetary guarantee | Hard cost containment | No, provided cost is not claimed as a hard bound. |
| Per-run grant; cumulative ledger not enforced | A user boundary is an explicit new grant; the ledger records history | A lifetime task ceiling | No, but the distinction must be stated. |
| Detached background jobs survive | They are bounded by admission, timeout, root deadline, and table caps; the model must `wait` for evidence | That no job runs after the task ends | No, if the task boundary explicitly excludes un-waited jobs. |
| Deadline does not interrupt in-flight work | New model calls stop; shell work is capped; provider latency is external | Immediate stop at the instant | No. It is a budget stop, not a failure. |
| Darwin sandbox protects workspace, not the whole filesystem | The contract is "no repository mutation"; reads/exec/network remain | Writes to user files outside the workspace | No for the declared repository boundary; document it. |
| Network is classified as a read capability | The policy has no separate egress authority; approval is still required | Data-loss/egress semantics | No, but the mapping is coarse. |
| No semantic LLM judge | Deterministic evidence is the design choice; uncertainty becomes review | Semantic correctness | No. |
| One-shot/plain REPL are stateless | They can be explicitly non-task modes; they must still report a verdict, not claim completion | Durable continuity outside TUI | No, if A3 is fixed. |
| Read-only subagent flag not delivered | It fails closed rather than widening authority | Read-only delegated work | No. |
| `--yes` / settings allow skip prompts | Explicit user configuration; authority DENY still wins | Human approval for every call | No. |

### D — DEFERRED / OUT OF SCOPE

These are **not** required to close the current design scope. They should not be
implemented in Phase 7.

- Full call graph, reference graph, implementation graph, or whole-program
  semantic graph.
- File/symbol graph nodes and `contains`/`declares` edges; coordinates already
  own those facts.
- Cross-module, vendored, `replace`, nested-module, build-tag, or generated-code
  resolution beyond the current source-level declaration model.
- Incremental freshness, a background indexer, or a persistent graph database.
- A second repository index, second parser, or external graph service.
- MCP, browser search, or a new tool protocol.
- A semantic LLM judge or probabilistic verification.
- A universal verification framework.
- Provider redesign, transport rewrite, TUI redesign, or daemon architecture.
- Arbitrary autonomous planning or unrestricted background autonomy.
- Tree-wide output-byte/token/monetary budgets or per-child reservations.
- Killing detached jobs on task completion/cancellation.
- Additional sandbox backends beyond the fail-closed platform boundary.
- New graph benchmark capabilities or live-model benchmark reruns.

The justification is not "these are hard." It is that the audit found no
invariant, production path, or measured failure that requires them. The Phase 6
scope is a package/import relation; the Phase 4/5 completion and budget layers
already provide the authorities those capabilities would need to respect.

---

## 7. Technical Debt

Only material debt is listed.

1. **Two admission seams.** `Authorize` is the production boundary; `Admit` /
   `Approve` remain as a legacy path when `Authorize` is nil. This is useful for
   embedders but weakens the claim "one path, always."
2. **Two completion evaluators.** `EvaluateCompletion` (frozen Phase 3 / child
   compatibility) and `EvaluateTaskCompletion` (Phase 4) share the evidence scan
   but differ in contract handling. A read-only child uses the former; a
   writable child uses the latter. The drift is visible in the execution and
   empty-answer gaps, which are present in both.
3. **Two budget creation paths.** `options.newBudget`/`task.Controller.RunBudget`
   create complete tree grants; `agent.New` with a nil budget creates only
   turns/model calls. Production uses the former.
4. **Per-turn grant versus cumulative ledger.** The ledger is durable and
   cumulative; enforcement is not. This is documented but easy to misread as a
   lifetime budget.
5. **TUI-only task lifecycle.** The same loop serves headless paths without the
   task contract, target snapshot, or completion evaluator. This is the largest
   structural divergence from the reported causal path.
6. **Background result semantics are split.** Spawn returns a job id; `wait`
   owns the observed result; completion only sees `ToolOutcome`s. Without
   truthful `wait` flags and a job-state contract, the split leaks into
   completion.
7. **Stale architecture docs.** `ARCHITECTURE.md` predates `delete`, `symbols`,
   and `graph`, and describes the legacy authority seam as production. Historical
   Phase 1/Phase 6 claims need scope annotations.
8. **Unused packages.** `shell/` and `memory/` have tests but no production
   consumer. Prefer deletion or an explicit "not wired" status over maintenance
   by inertia.
9. **Name-based read-only allowlist.** `ReadOnlyToolNames` trusts tool names,
   not their declared tiers or behavior; it omits `ls` and includes names not
   registered. Fail-closed for unknown names, but not a capability proof.
10. **Concurrent file mutation.** File tools are shared by default; the loop
    offers no per-path serialization. This is a real lost-update risk when the
    model emits two edits to one file in one batch.

---

## 8. Security / Authority Assessment

**Production conclusion: the authority boundary is structurally sound.**

- Every production model tool call passes through `agent.Loop.execute`, then
  `securityGate.Evaluate`/`Approve`, then a second cancellation check, then
  `budget.Admit`, then `Tool.Execute`.
- The model cannot supply capability, risk, mutation, or an approval flag.
  `securityRequest` derives them from the tool's declared tier, the per-call
  capability reporter, normalized scope, and trusted command classification.
- ASK/FUZZ/BUILD is a runtime permission table, not a prompt. A DENY is
  terminal; an APPROVE must produce a one-shot grant and a fresh confirming
  evaluation.
- Path containment resolves symlinks through the deepest existing ancestor and
  refuses outside-workspace targets. The graph path selector uses the same
  `tools.resolvePath`.
- Read-only execution is derived from authority, not command text, and is
  enforced by an OS boundary: `sandbox-exec` on darwin, `bubblewrap` on Linux,
  fail-closed elsewhere. The 22-spelling suite leaves the protected workspace
  unchanged.
- A read-only child inherits the live gate; nested delegation is structurally
  depth-bounded; children share the root budget.

**Caveats, stated exactly:**

1. **The library seam is fail-open.** `agent.New` does not require
   `Authorize` or a full `Budget`. A host that omits both executes with no
   security gate and only partial metering. Production `buildLoop` always sets
   both.
2. **`BeforeToolCall` can authorize the wrong arguments.** The loop validates
   rewritten args but authorizes the original call. No production hook is
   installed; the seam is still unsafe if used.
3. **`AfterToolCall` can erase failure evidence.** No production hook is
   installed.
4. **Read-only protection is workspace-scoped on darwin.** The OS profile
   denies writes under the root; it does not deny writes elsewhere. The stated
   contract is "no repository mutation," not "no filesystem writes anywhere."
   Linux is stricter.
5. **Network is mapped to the read capability.** There is no separate egress
   authority; a fetch is still an APPROVE-risk action.
6. **Background mutation evidence is currently untruthful.** A spawn claims
   mutation before execution; `wait` loses failure status. This is a completion
  /evidence defect, not an authority bypass: the command still passed the same
   gate and budget admission.

**No production authority bypass was found in the audited paths.** The open
security work is hardening the library seams and making background evidence
truthful, not redesigning the gate.

---

## 9. Resource / Budget Assessment

**Production conclusion: within one root run, the budget is correctly
enforced.**

- `budget.Budget` owns fixed limits, five monotonic counters, and one mutex.
- `AdmitAll` checks all requested dimensions under one lock and charges only if
  all pass; a refused model call spends no turn.
- The production loop charges turns + model calls atomically before a provider
  request; tool calls after authority and cancellation; children before child
  construction; background jobs before `cmd.Start`.
- The same pointer is shared by the root, nested children, and background
  admissions. No child-facing API can reset it.
- 30/30 scripted Phase 5 cases report zero overruns, zero execution after failed
  admission, zero child/background escapes, zero deadline escapes, zero
  cancellation escapes, and zero false completions on those cases.

**Limits:**

- No tree-wide output-byte, context-byte, token, or monetary budget. Per-call
  caps and compaction are not a hard aggregate.
- The TUI creates a fresh grant per user turn; the durable ledger accumulates
  usage but is not enforced as a ceiling. A resumed task can spend a fresh full
  grant.
- A detached background job is charged at admission, not at completion; it may
  outlive the turn/task and is not killed on task completion or cancellation.
- Deadline expiry stops new model calls and caps shell work, but does not
  interrupt an in-flight provider request or every in-flight non-shell tool.
- `agent.New` without a budget meters only turns and model calls.
- `read`/`write`/`edit`/`delete` can allocate the whole file; the target
  verifier is bounded, the ordinary file tools are not.

No budget overrun was found in production code or in the scripted artifact.
The remaining work is truthful state and documented scope, not a new budget
dimension.

---

## 10. Discovery / Repository Intelligence Assessment

**Phase 1 remains structurally true in the sense that matters: one source.**

- `tools.NewStructuralSource` is the production ownership point.
- `tools.NewSymbolsToolWithSource` and `tools.NewGraphTool` receive the same
  pointer in `cmd/ohgo/buildTools`.
- `ast.Index.buildLockedCtx` calls `GoBackend.Parse` once per file and retains
  package, symbols, and imports from that pass.
- `ast.Index` is the only production coordinate store; `bench/repository` uses
  a separate index for the benchmark fixture, not in the product.
- `agent.CaptureTargets` uses the same `SymbolsTool` index through
  `StructuralResolver`.

**The historical "one structural tool" statement is superseded.** There are two
wire surfaces, `symbols` and `graph`, because Phase 6 deliberately separated
coordinates from relationships. The one-index / one-parse invariant is intact;
the one-tool claim is not current.

**Phase 2 conclusions remain valid for their scope.** No later Phase 3–6 change
reverted `symbols`, `grep`, `glob`, `ls`, or the index contract in a way that
invalidates the Phase 2 measurements. Phase 6 adds a separate read-only graph
surface. Phase 2's cross-file conclusion remains true for call/reference
relationships; the graph provides package dependencies, not call sites, and no
live model measurement shows it closing that gap.

**Phase 6 graph conclusion.** The package/import graph is deterministic,
bounded, read-only, and useful on the measured static tasks. It is not a call,
reference, implementation, or semantic graph. It is source-level, not build
context. It does not resolve other modules. Its live-model value is unproven.
Its reverse-adjacency cancellation path does not fully honor the cancellation
contract.

---

## 11. Verification / Completion Assessment

**Phase 4 is authoritative only where it is installed.**

- The TUI creates the contract, captures targets before the run, evaluates after
  the run, and transitions the durable task. This path is real and tested.
- Writable children build their own contract, snapshot, and evaluator; their
  mutation and verification evidence is carried to the parent.
- The 23-case scripted Phase 4 artifact is a real improvement over the frozen
  evaluator: 11 H0 false completions → 0 H1 on those cases, with 10 true
  completions preserved.
- `NEEDS_REVIEW` is correctly non-terminal and is mapped to a task state that a
  later turn can resolve.
- Structural target checks cover create/modify/delete, path/symbol/package,
  ambiguity, reverts, unrelated mutations, verification scope, and cancellation
  during post-run evidence.

**But the completion claim is not universal:**

- Execution objectives with no named check can complete with no execution
  (A1).
- Informational objectives can complete with no answer (A2).
- One-shot and plain REPL do not evaluate at all (A3).
- A resumed task can complete against a different contract (A4).
- Background evidence is not truthful (A5).
- Cancellation at the final transition is not linearized (A6).

The correct statement after Phase 6 is:

> Phase 4 is the authoritative completion layer **for the TUI task path and
> writable delegated children**. It is not yet the authoritative completion
> layer for every production execution path.

That distinction matters for the terminal claim. The Phase 4 evaluator is
sound in its target/evidence rules; the gaps are in where it is called, what the
task retains across resume, and what the background tool reports.

---

## 12. Performance Assessment

### Measured

| Area | Evidence | Result |
|---|---|---|
| Index, 295 files | Phase 1 artifact/report | cold median 76.57 ms; warm lookup median 1.47 ms |
| Index stress, 2,000 files / 62,000 symbols | Phase 3 report | cold 181.99 ms; 10.3 MiB retained; warm average 8.04 ms; rebuild 172–214 ms |
| Index stress, 10,000 files / 310,000 symbols | Phase 1/FINAL_CLOSURE | cold 0.88 s; 51 MiB; warm 46 ms (42 ms freshness); rebuild 0.90–1.25 s |
| Phase 4 contract extraction | Phase 4 artifact | ~40 µs/turn, ~22–45 KB allocated |
| Phase 4 target capture/check | Phase 4 artifact | path ~68–70 µs; symbol ~193–197 µs; completion path ~122 µs |
| Phase 5 budget admission | Phase 5 artifact | ~16 ns serial; ~101 ns at 8 goroutines; snapshot ~21 ns |
| Phase 5 loop | Phase 5 artifact | 500 tool turns in ~21 ms; 100 one-turn children in ~352 µs |
| Phase 6 graph, 100/1,000/3,000 files | Phase 6 artifact | cold index 6.92/48.97/145.70 ms; index/parse 0.99/0.99/1.00; warm query 1.13/8.29/25.84 ms; stale walk 0.91/8.09/25.87 ms; rebuild 6.00/55.71/168.00 ms; retained 0.2/2.4/6.8 MiB; 8-way 6.34/65.32/186.17 ms |
| Phase 6 H0/H1 | Phase 6 artifact | 4/8 vs 8/8 correct; 4 vs 0 false positives; 8 vs 8 tool calls; 7 graph queries; 10,780 vs 7,140 bytes |

### Not Measured

- Any live-model graph benefit, turn count, token count, or answer quality.
- Production-scale repositories, memory pressure, or multi-user contention.
- Graph performance with cross-module, vendored, or huge import fan-out.
- End-to-end completion overhead on a large target set beyond the microbenchmark.
- Headless one-shot/REPL completion cost after the Phase 7 fix.
- Background job resource cost after `wait` verification.
- A hard token/byte/monetary cost bound.

### Synthetic Only

Every index, graph, Phase 4, and Phase 5 performance number above is synthetic,
scripted, or host-specific. The Phase 6 numbers are explicitly not a
production-scale claim. The Phase 2 agent-level numbers are one model, one
fixture, one sample per cell, with measured run-to-run variance comparable to
some track differences.

### Known Bottlenecks

1. The full freshness stat walk dominates warm graph/symbol query time; graph
   traversal itself is a minority.
2. Any detected change rebuilds the whole snapshot and graph; no incremental
   invalidation.
3. The index mutex serializes concurrent structural queries; the 8-way Phase 6
   number is lock/walk cost, not parallel throughput.
4. Provider latency and model turns dominate agent-level cost; budget
   admission is negligible.
5. The compaction summarizer is a provider call; it is budgeted but adds
   latency.
6. `read`/`write`/`edit`/`delete` can allocate a whole file; the target verifier
   is bounded but the ordinary tools are not.
7. Reverse graph queries build an on-demand reverse adjacency over all edges;
   bounded by the work limit, but O(E) for the query.
8. Detached background jobs can outlive the turn; wall-clock and process cost
   are bounded only by their timeout/deadline/table.

---

## 13. Terminal Readiness

**Is OHGO structurally ready to enter its final engineering phase?**

**Yes — to enter Phase 7. No — to be declared complete.**

The architecture is coherent enough to finish: one structural source, one
production authority gate, one root budget per execution tree, a deterministic
Phase 4 evaluator, and a bounded graph. The remaining defects are localized
evidence/state-transition gaps, not reasons to add a new subsystem or reopen
Phase 1–6 architecture.

The terminal claim is not yet honest because a user can receive `COMPLETED` or a
zero exit status without evidence for the requested work, and a resumed review
task can be closed by unrelated evidence. Phase 7 must close those paths, then
freeze. It must not become Phase 8.

---

## 14. Final Decision

```text
PHASE 6 FINAL SYNTHESIS
-----------------------
Verdict: PASS WITH LIMITATIONS (Phase 6 graph contract closed)
         HOLD (system-level terminal-completeness claim)

System State:
  One structural source, one production authority gate, one root budget per
  run, and a bounded package graph are proven. Completion is not yet
  authoritative for every production path, and background evidence is not yet
  truthful.

Phase 1–6 Integrity:
  No Phase 1–5 code was reverted. Full test/race suite passes. Phase 2/4/5/6
  benchmark artifacts remain internally consistent; they were not rerun. Phase 6
  intentionally supersedes the "one structural tool" wording with two surfaces
  over one index.

Critical Remaining Gaps:
  A1 execution objective with no execution evidence
  A2 informational objective with no answer
  A3 headless completion/exit bypass
  A4 resumed NEEDS_REVIEW contract mismatch
  A5 untruthful background mutation/failure/verification evidence
  A6 cancellation-to-completion race

Acceptable Limitations:
  Package/import-only graph; no call/reference/implementation graph; no
  cross-module/build-context semantics; full freshness/rebuild; same-size +
  same-mtime blind spot; static-only H1 evidence; synthetic performance; no
  token/byte budget; per-turn grant with cumulative ledger; detached background
  jobs; workspace-scoped darwin sandbox; semantic verification limits.

Terminal Phase 7 Objective:
  Make every production execution path report and persist completion only from
  the Phase 4 contract and observable evidence, preserving Phase 1–6 authority,
  budget, discovery, and graph contracts without adding a capability.

Phase 7 Required Work:
  F1 execution evidence; F2 non-empty answer; F3 headless verdict/exit;
  F4 persisted task contract and review control; F5 truthful background
  evidence; F6 cancellation linearization; F7 graph reverse cancellation;
  documentation closure.

Phase 7 Non-Goals:
  Call/reference/implementation graph; second index; graph database; daemon;
  MCP/browser; semantic judge; universal verifier; provider/TUI redesign;
  autonomous planning; unrestricted background autonomy; incremental indexing;
  cross-module resolution; token/byte budget redesign.

Terminal Closure Criteria:
  A1–A6 fixed; required tests and adversarial matrix pass; Phase 1–6 regression
  and the Phase 4/5/6 scripts pass; no new invariant violation; documentation
  matches source; only C limitations remain.

Ready to Begin Phase 7: YES
```

> **Do not implement anything beyond the approved Phase 7 boundary.**
