# Phase 4 Final Closure

## 1. Verdict

**PASS WITH LIMITATIONS — PHASE 4 CLOSED**

The committed Phase 4 tree satisfies the bounded intent-aligned completion
contract. The 23-case benchmark reproduces exactly: false completions fall from
11 to 0 with no false reviews and all 10 true completions preserved. Two
closure-audit defects were found and fixed before freeze: verification scope
ignored a command's effective working directory, and a cancel during post-run
evidence could still transition a task to COMPLETED.

The remaining limitations are the documented deterministic-contract limits;
none invalidate the Phase 4 objective.

## 2. Final Commit

- Final code commit: `dbc1ece22afab0468b2543dcf80ada5f9bc70bdb`
- Closure fix commit: `c38301277c69cf9c0a093945da4645c0b0adeadf`; `dbc1ece` adds the inside-directory completion regression on top of it.
- Parent Phase 4 commit: `c3f2fdf` (`fix(lint): use append for package directories`)
- Phase 4 origin: `5f563b0` / `c113014` (identical trees); `c3f2fdf` differs only by the lint fix in `agent/targets.go`
- This report is committed immediately after the final code commit; it adds documentation only.
- Working tree requirement: clean after the report commit.

## 3. Objective

Phase 4 was intended to make OHGO harder to falsely declare a task complete by
aligning the user's request, a bounded intent contract, the expected target and
operation, the observed mutation, deterministic structural evidence, the
verification scope and kind, deterministic alignment, and the final completion
decision.

The phase does **not** claim natural-language semantic understanding. It claims
that within a bounded, inspectable contract, missing or contradictory evidence
produces `NEEDS_REVIEW` instead of `COMPLETED`.

## 4. Architecture Verified

The production causal path is:

```text
TUI runTurn
  workspace.BuildIntentContract(text)              // user text only
  task.Controller.Begin(objective)                 // bookkeeping
  agent.CaptureTargetsContext(ctx, root, intent, resolver)
        resolver = tools.SymbolsTool's existing ast.Index
  agent.Loop.Run(ctx, request)
        authority gate → tool → ToolOutcome
  tui.turnTaskOutcome
        agent.EvaluateTaskCompletion
           scanCompletionEvidence
           CheckTargets
           UnexpectedMutations / ObservedMutationPaths
           AssessBehavior
           AssessAlignment
        COMPLETED → task.Controller.Complete
        other     → task.Controller.NeedsReview
        cancel    → task.Controller.Pause
```

Delegated children follow the same evaluator:

```text
tools.TaskTool
  → newSubagentSpawner
  → workspace.BuildIntentContract(spec.Task)
  → agent.CaptureTargetsContext
  → agent.RunSubagent
  → agent.EvaluateTaskCompletion
  → tools.TaskTool carries completion, mutation, and verification evidence
```

The one-shot CLI and plain REPL do not record a durable task lifecycle, so they
cannot emit `COMPLETED`; they share the loop, tools, and evidence but are outside
the completion-state audit. This is unchanged Phase 3 behavior, not a Phase 4
bypass.

## 5. Contract Verified

`contract.IntentContract` carries:

- the trimmed objective;
- informational / execution / mutation kind;
- unique path, symbol, and package targets;
- a per-target create / modify / delete / any operation;
- verification requirements with scope and check kind;
- a behavioural-objective flag;
- an ambiguity state and deterministic extraction issues.

Verified invariants:

1. The contract is built from user text only, never model output.
2. Unknown targets are not invented; an unbounded mutation is `Ambiguous`.
3. Command arguments (`go test ./tools`) are not mutation targets.
4. Operation extraction is clause-local; a location preposition yields `any`
   rather than a guessed direction.
5. At most 8 targets are accepted; more becomes review.
6. No model output can add a target, widen a scope, or mark a requirement met.
7. Empty or unresolved requests remain reviewable.

## 6. Evidence Verified

`agent.CaptureTargetsContext` captures pre-run state and `CheckTargets`
compares it with the final tree:

- **Path targets:** existence plus SHA-256 of the file content.
- **Symbol targets:** declaration range plus SHA-256 of that range, located
  through the same `ast.Index` the symbols tool serves.
- **Package targets:** the package's declaration-bearing file set, with content
  hashes.
- **Direction:** create requires absent→present; modify requires present→present
  with a changed hash; delete requires present→absent; any accepts a change.
- **Scope:** unrequested path mutations, unattributed shell mutations, unresolved
  or ambiguous coordinates, and read/directory limits all become review.
- **Reverts:** a change that returns the target to its pre-run hash is not a
  completion.
- **Bounds:** 256 files per package, 4 MiB per file, 16 MiB total per capture or
  check; target count is bounded at 8 by the contract.

One structural index exists in production: `tools.SymbolsTool` owns a single
`ast.Index`; `agent.StructuralResolver` only projects its locations. No second
repository index, MCP dependency, or grep-as-structural-truth path was added.

## 7. Verification Verified

`tools.VerificationForIn` records a successful command's check kind and package
scopes from the command's own arguments.

Verified behavior:

- a `test` check covers a `test` requirement;
- a `build` check does not cover a `test` requirement;
- a `test` check covers a `build` requirement;
- a check in another package does not cover the target package;
- a verification before the last change is not evidence for the final state;
- a failed or cancelled check is not a pass;
- a targetless behavioural objective is bounded by packages the run actually
  changed, with a repo-wide requirement only when no package can be attributed;
- a check that changes directory is rebased to that directory; a check outside
  the workspace is not evidence for a workspace target (closure fix D4-11);
- no verification remains review for a mutation.

## 8. Alignment Verified

`agent.AssessAlignment` is a pure deterministic assessment over the bounded
evidence set:

```go
type Assessment struct {
    Alignment Alignment // aligned | uncertain | misaligned | not_applicable
    Rationale string
    Evidence  []string
}
```

It receives the contract, structural checks, behavioural coverage, and
unexpected paths. It cannot execute a tool, call a provider, authorize an
action, mutate state, or transition a task. `uncertain` and `misaligned` both
downgrade to `NEEDS_REVIEW`; there is no path from uncertainty to completion.
No LLM semantic judge was added.

## 9. Completion Authority

`agent.EvaluateTaskCompletion` is the single task-completion evaluator. The TUI
uses it through `tui.turnTaskOutcome`; writable subagents use it in
`agent.RunSubagent`; the benchmark's H1 applies the same function to the same
recorded turns.

The following are not sufficient by themselves:

- the model saying it is done;
- the model saying tests passed;
- an empty todo list;
- a successful tool call with no evidence;
- a successful build when a test was required;
- a final natural-language answer;
- a cancelled or turn-limited run.

The frozen Phase 3 `agent.EvaluateCompletion` remains unchanged for embedders
and H0 measurement. `EvaluateTaskCompletion` shares the Phase 3 run-shape,
failure, mutation, and verification-order rules, then adds target and
behavioural evidence.

## 10. Adversarial Results

Every row is a real scripted run through the real write/edit/delete/bash/symbols
tools and the real security gate. H0 is the frozen evaluator; H1 is
`EvaluateTaskCompletion`.

| ID | Intent contract | Observed mutation | Structural evidence | Verification | Alignment | H1 | Expected | Pass |
|---|---|---|---|---|---|---|---|---|
| A | mutation, path `note.txt` modify, all test | `note.txt` changed | target satisfied | `go test ./...` pass (test) | aligned | COMPLETED | COMPLETED | yes |
| B | mutation, `note.txt` modify, all test | `other.txt` created; `note.txt` unchanged | target unchanged; unexpected `other.txt` | tests pass | misaligned | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| C | mutation, `note.txt` modify, all test | `note.txt` changed; broken test | failure gate before structural | `go test ./...` failed | not reached | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| D | mutation, `note.txt` modify, all test | written then reverted; final content old | target unchanged | tests pass | misaligned | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| E | mutation, `note.txt` modify, all test | `note.txt` changed; `important.txt` deleted | target satisfied; unexpected `important.txt` | tests pass | misaligned | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| F | informational | read only | not applicable | none | not_applicable | COMPLETED | COMPLETED | yes |
| G | mutation, no target, ambiguous | `note.txt` changed | no target evidence | tests pass | uncertain (ambiguous) | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| H | mutation, `note.txt` modify | none | no change | none | not reached | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| I | mutation, symbol `MissingSymbol` modify, all test | `note.txt` changed; symbol unresolved | unresolved symbol; unexpected `note.txt` | tests pass | misaligned | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| J | mutation, `tools/grep.go` + symbol `GrepTool`, tools test | `GrepTool` edited | path and symbol satisfied | `go test ./tools` pass | aligned | COMPLETED | COMPLETED | yes |
| K | same as J | `GrepTool` edited | path and symbol satisfied | `go test ./ast` pass (wrong scope) | uncertain | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| L | behavioural, no target, all test | `tools/grep.go` edited | no target; changed package `tools` | `go test ./...` pass | aligned | COMPLETED | COMPLETED | yes |
| L2 | behavioural, scope `tools`, tools test | `tools/grep.go` edited | no target; `tools` covered | `go test ./tools` pass | aligned | COMPLETED | COMPLETED | yes |
| L3 | behavioural, scope `tools`, tools test | `tools/grep.go` edited | no target; `tools` not covered | `go test ./ast` pass | uncertain | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| L4 | behavioural, no target, test | `tools/grep.go` edited | changed package `tools` | `go test ./tools` pass | aligned | COMPLETED | COMPLETED | yes |
| L5 | behavioural, no target, test | `tools/grep.go` edited | changed package `tools` | `go build ./tools` pass (build) | uncertain | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| M | mutation, `docs/README.md` modify, all test | README changed | target satisfied | `go test ./...` pass | aligned | COMPLETED | COMPLETED | yes |
| N | execution, all test | none | none | no check ran | uncertain | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| N2 | execution, all test | `note.txt` changed | non-mutation objective changed repository | `go test ./...` pass | not reached | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| O | mutation, `note.txt` delete, all test | `note.txt` deleted | delete satisfied | `go test ./...` pass | aligned | COMPLETED | COMPLETED | yes |
| P | mutation, `note.txt` delete, all test | `note.txt` written; still exists | wrong operation | tests pass | misaligned | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| Q | mutation, `new.md` create, all test | `new.md` created | create satisfied | `go test ./...` pass | aligned | COMPLETED | COMPLETED | yes |
| R | informational | none | not applicable | none | not_applicable | COMPLETED | COMPLETED | yes |

Closure-audit cases outside the 23-case matrix:

| Case | Contract | Observed mutation | Structural evidence | Verification | Alignment | Final | Expected | Pass |
|---|---|---|---|---|---|---|---|---|
| Foreign module via `cd` | `tools/grep.go` + `GrepTool`, tools test | local target changed to wrong value | target satisfied | `cd <other> && go test ./tools` pass but outside workspace | uncertain | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| Inside `cd` | `tools/grep.go` + `GrepTool`, tools test | target changed | target satisfied | `cd tools && go test ./...` pass, scopes rebased to `tools` | aligned | COMPLETED | COMPLETED | yes |
| Cancel during post-run read | `tools/grep.go` + `GrepTool`, all test | target changed | target satisfied | pass | aligned | PAUSE | PAUSE | yes |
| Cancelled verification | `note.txt` modify, any check | target changed | target satisfied | cancelled check | uncertain | NEEDS_REVIEW | NEEDS_REVIEW | yes |
| Create unrelated file | `new.md` create, any check | `other.md` created | target missing; unexpected `other.md` | pass | misaligned | NEEDS_REVIEW | NEEDS_REVIEW | yes |

No benchmark case was added, removed, reordered, or re-expected. The closure
tests are separate regressions.

## 11. H0 / H1 Results

Reproduced from the committed tree with:

```sh
OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -v -count=1
```

| Metric | H0 | H1 |
|---|---:|---:|
| True completions | 10 | 10 |
| False completions | 11 | **0** |
| False reviews | 0 | 0 |
| Correct reviews | 2 | 13 |
| NEEDS_REVIEW rate | 8.7% | 56.5% |
| Cases | 23 | 23 |
| Model calls | 64 | 64 |
| Tool calls | 41 | 41 |
| Context bytes | 292,374 | 292,374 |
| Contract bytes | 6,904 | 6,904 |
| Verification commands | 18 | 18 |

The reproduced case states, reasons, model/tool counts, context bytes,
contract bytes, snapshot counts, and summary metrics are identical to
`docs/benchmarks/results/phase4-completion.json` after removing only
wall-clock and generated-at fields. Closure reruns measured 10.09–11.04 s
total / 439–480 ms average versus 10.45 s / 455 ms in the committed baseline.
This is environment variance, not benchmark drift.

Benchmark revision: `cmd/ohgo/phase4_bench_test.go` at `dbc1ece`; the case table
is unchanged from `5f563b0`/`c113014`. Task hash: not applicable; the fixture is
a local temporary Go module with scripted model streams.

## 12. Regression Results

All commands below pass on the final tree. The full suite, race suite, and
benchmark were rerun at code commit `dbc1ece`; the earlier targeted runs used
the same production code, and `dbc1ece` only adds the inside-directory test:

| Check | Command | Result |
|---|---|---|
| gofmt | `gofmt -l .` | PASS (empty) |
| build | `go build ./...` | PASS |
| vet | `go vet ./...` | PASS |
| test | `go test ./... -count=1` | PASS |
| race | `go test -race ./... -count=1` | PASS |
| Phase 4 benchmark | `OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -count=1` | PASS |
| Phase 4 integration | `go test ./cmd/ohgo -run TestSemanticIntegration -count=1` | PASS |
| Phase 1 intelligence | `go test ./ast ./tools -count=1` | PASS |
| Phase 2 integrity | `go test ./bench/repository -count=1` | PASS |
| Phase 3 authority/sandbox | `go test ./agent ./security ./sandbox -run 'Test.*(Authorize|Security|Sandbox|Admit|ReadOnly)' -count=1` | PASS |
| cancellation/turn limit | `go test ./agent ./tui -run 'Test.*(Cancel|TurnLimit|Interrupt)' -count=1` | PASS |
| task persistence | `go test ./task ./cmd/ohgo -run 'Test.*Task' -count=1` | PASS |
| delegation | `go test ./agent ./tools -run 'Test.*Subagent|TestTaskTool' -count=1` | PASS |
| Phase 4 completion/adversarial | `go test ./agent ./tools ./tui ./cmd/ohgo -run 'Test.*(TargetSnapshot|EvaluateTaskCompletion|Verification|Semantic|Phase4)' -count=1` | PASS |

The full suite includes the Phase 1–3 tests, so an earlier frozen phase did not
regress. The race suite passed with the new blocking-resolver cancellation test
and the new shell-directory verification paths.

## 13. Cancellation / State Safety

| Cancellation point | Observed behavior | Evidence |
|---|---|---|
| Before mutation | tool not dispatched; run returns cancellation; review/pause | `TestCancellationBeforeToolExecutionStopsTheCall`, `TestRunTurnPausesTaskOnInterruption` |
| During mutation | cancellation observable; no completion | `TestCancellationDuringToolExecutionIsObservable` |
| After mutation, before verification | review; no successful check | `TestEvaluateTaskCompletionRejectsCancelledVerification` |
| During verification | cancelled check is an error, not a pass | `TestEvaluateTaskCompletionRejectsCancelledVerification`; tool-level cancellation tests |
| After verification, before task transition | Pause, not Complete | `TestRunTurnPausesWhenCancelledDuringPostRunVerification` (closure fix D4-12) |
| Turn limit | Pause, no completion | `TestTurnTaskOutcomePausesOnTurnLimit`, `TestTurnLimitBoundsARunawayModel` |
| Delegated child | child error/cancellation; parent does not inherit success | `TestRunSubagentPropagatesContextCancellation`, `TestRunSubagentHonoursCancellation`, `TestTaskToolHonoursCancellation` |
| Target capture | cancellation check before and between reads | `TestCaptureTargetsHonoursCancellation` |

`cancelled`, `incomplete`, and `unverifiable` do not map to `COMPLETED`. The
cancel/transition guard is checked immediately before the task hook; a cancel
that arrives inside the hook itself is not separately serialized, but the hook
is the transition boundary, not the evidence read.

## 14. Security / Authority Preservation

Verified by source and tests:

- The security gate is unchanged; Phase 4 adds no authority path.
- Verification, alignment, contract extraction, and completion evaluation are
  pure read-only consumers. They cannot execute a tool, grant approval, or
  mutate the workspace.
- `TargetSnapshot` reads only the named coordinates through
  `security.NormalizeScope`; out-of-root paths are refused.
- Read-only requests still cannot mutate: the shell sandbox tests pass, and a
  mutating read is review (`TestSemanticIntegrationRejectsMutationDuringRead`).
- Writable subagents inherit the parent's authorizer, admission, approval,
  deadline, and turn limit; child tools cross the same gate. The task tool is
  read-tier only because delegation itself spends tokens.
- A child that needs review is returned to the parent as an error result, not
  as a clean success.
- `tools.TaskTool` cannot bypass the central gate; the spawner inherits it.
- No completion path bypasses `agent.EvaluateTaskCompletion` in the TUI or
  writable subagent paths.

## 15. Performance Sanity

Microbenchmarks on the final tree (macOS / Apple M1 Pro / go1.27.2):

| Operation | Final run | Committed baseline |
|---|---:|---:|
| `BuildIntentContract` (read) | ~57 µs/op, 44.8 KB, 1,211 allocs | ~41 µs/op, 45 KB, 1,211 allocs |
| `BuildIntentContract` (mutation) | ~50 µs/op, 22.1 KB, 618 allocs | ~40 µs/op, 22 KB, 618 allocs |
| `CaptureTargets` path | ~72 µs/op, 16 KB, 176 allocs | ~69 µs/op, 16 KB, 176 allocs |
| `CaptureTargets` symbol | ~209 µs/op, 45 KB, 440 allocs | ~196 µs/op, 45 KB, 440 allocs |
| `EvaluateTaskCompletion` path | ~130 µs/op, 32 KB, 359 allocs | ~122 µs/op, 32 KB, 359 allocs |

End-to-end benchmark: 23 tasks, 64 model calls, 41 tool calls, 292,374 context
bytes. Closure reruns took 10.09–11.04 s total (439–480 ms average). The real
`go test` commands dominate.
The closure fix adds one path resolution and a small segment walk per bash
command; it adds no model calls, tool calls, or model-context bytes. Memory
remains bounded by the existing caps. This is a sanity check, not a
production-scale performance claim.

## 16. Proven

- Bounded, inspectable intent contract built from user text only.
- Deterministic path, symbol, and package target evidence.
- Expected-operation matching, including create/modify/delete direction and
  reverted-change rejection.
- Out-of-scope file and unattributed shell mutation detection.
- Scoped verification with check-kind enforcement.
- Verification scope relative to the command's effective working directory;
  foreign-module checks are not evidence (D4-11).
- Downgrade-only deterministic alignment.
- Centralized completion through `agent.EvaluateTaskCompletion` for the TUI and
  writable subagents.
- Elimination of the benchmark's false completions (11 → 0) with preservation
  of true completions (10/10) and no false reviews (0).
- Cancellation during post-run evidence pauses rather than completes (D4-12).
- Compatibility with the frozen Phase 1–3 suites, authority, sandbox, turn
  limit, and task persistence.

## 17. Implemented but Limited

- Semantic alignment is deterministic contract alignment, not natural-language
  understanding.
- Symbol targets verify declaration ranges, not whole-file intent.
- Package targets compare declaration-bearing files; an added/removed file with
  no declarations can be missed.
- Shell mutation attribution is textual and partial; an interpreter write with
  no target token is unattributed/reviewable.
- Targetless behavioural objectives are bounded conservatively.
- Test flags (`-run '^$'`, `-exec`, environment filters) and environment
  variables are not interpreted; a successful test-kind command of the right
  scope is accepted even if it ran no relevant tests.
- Directory resolution is lexical: `popd`, dynamic paths, and shell expansions
  are unscoped; symlink escapes are not resolved.
- Directory path targets are reviewable, not verified.
- Non-Go checks carry no Go package scope.
- `NEEDS_REVIEW` has no one-key accept action.

## 18. Not Proven

- Content-level semantic correctness (values, invariants, prose intent).
- That a test command actually exercised the relevant tests when flags or
  environment filtered them.
- Open-world natural-language intent extraction.
- Production-scale latency, index-build behavior, or memory.
- LLM-assessed semantic alignment (deliberately not implemented).
- A universal semantic verification framework.

## 19. Deferred

Explicitly outside Phase 4 and not implemented:

- an LLM semantic assessor or judge;
- call graphs, references, callers/callees, import graphs;
- a second repository index, semantic/vector retrieval, or MCP;
- browser/realtime search;
- a universal verification engine or broader check-kind taxonomy;
- rename/move operation modelling;
- shell argument-role parsing for multi-path attribution;
- test-flag/environment semantics;
- a tree-wide resource budget;
- provider, TUI, or Phase 2 benchmark redesign;
- production-scale performance characterization.

These require their own hypothesis, contract, authority model, and benchmark;
they are not Phase 4 claims.

## 20. Final Decision

**FROZEN — PASS WITH LIMITATIONS.**

The Phase 4 contract is satisfied within its bounded deterministic scope. The
false-completion benchmark is reproducible, the two closure defects are fixed
and regression-tested, phases 1–3 remain green, completion authority is
centralized, cancellation cannot produce a completed task after the user has
cancelled, and the remaining limitations are explicit.

The next phase must start from a separate hypothesis and benchmark. Do not add
semantic features to this frozen tree.
