# Phase 4 Benchmark — Semantic Completion

> **Status:** defined before implementation.
> **Primary metric:** false `COMPLETED` states.
> **Secondary metric:** false `NEEDS_REVIEW` states on correct work.

## 1. Hypothesis

```text
H0: observed mutation + successful verification is sufficient for completion.
H1: a bounded intent contract plus target-scoped structural and behavioral
    evidence reduces false completion without unacceptable false review.
```

Both hypotheses are evaluated on the same recorded runs. H0 is the frozen
`agent.EvaluateCompletion(turns, workspace.RequestsMutation(text))`. H1 is the
Phase 4 `agent.EvaluateTaskCompletion`.

## 2. Fixture

A temporary Go module (`example.com/phase4`) containing:

```text
go.mod
note.txt                  "old\n"
important.txt             "keep\n"
tools/grep.go             GrepTool() returns "bug"
tools/grep_test.go        fails until GrepTool() returns "fixed"
ast/ast.go                a passing package
ast/ast_test.go           passes
docs/README.md            documentation
```

Every case runs the real `write`, `edit`, `delete`, and `bash` tools through the
real loop and the real security gate. The model is scripted (completed streams),
so a run is deterministic and the only variable is the completion contract.

## 3. Matrix

| ID | Request shape | Scripted execution | Expected H1 |
|---|---|---|---|
| A | update `note.txt`, run `go test ./...` | write note.txt; tests pass | COMPLETED |
| B | update `note.txt`, run tests | write other.txt; tests pass | NEEDS_REVIEW |
| C | update `note.txt`, run tests | write note.txt; tests fail | NEEDS_REVIEW |
| D | update `note.txt`, run tests | write note.txt, revert it; tests pass | NEEDS_REVIEW |
| E | update `note.txt`, run tests | write note.txt, delete important.txt; tests pass | NEEDS_REVIEW |
| F | read `tools/grep.go` | read file | COMPLETED (no mutation) |
| G | update the code (no target) | write note.txt; tests pass | NEEDS_REVIEW |
| H | fix `note.txt` | clean stop, no tool call | NEEDS_REVIEW |
| I | fix `MissingSymbol`, run tests | write note.txt; tests pass | NEEDS_REVIEW |
| J | fix `GrepTool` in `tools/grep.go`, run `go test ./tools` | edit GrepTool; tests pass | COMPLETED |
| K | same as J | edit GrepTool; `go test ./ast` passes | NEEDS_REVIEW |
| L | fix the failing tests | edit GrepTool; `go test ./...` passes | COMPLETED |
| L2 | fix the failing tests in `tools` | edit GrepTool; `go test ./tools` passes | COMPLETED |
| L3 | fix the failing tests in `tools` | edit GrepTool; `go test ./ast` passes | NEEDS_REVIEW |
| L4 | fix the failing tests | edit GrepTool; `go test ./tools` passes | COMPLETED |
| L5 | fix the failing tests | edit GrepTool; `go build ./tools` passes | NEEDS_REVIEW |
| M | update `docs/README.md`, run tests | write README; tests pass | COMPLETED |
| N | run `go test ./...` | clean stop, no tool call | NEEDS_REVIEW |
| N2 | run `go test ./...` | write note.txt; tests pass | NEEDS_REVIEW |
| O | delete `note.txt`, run tests | delete note.txt; tests pass | COMPLETED |
| P | delete `note.txt`, run tests | write note.txt; tests pass | NEEDS_REVIEW |
| Q | create `new.md`, run tests | write new.md; tests pass | COMPLETED |
| R | read explanation only | answer from model, no tool call | COMPLETED |

Case F is the informational no-mutation path. Case R is the pure-answer path:
no target contract is needed because no mutation is requested or observed.
Cases L2–L5 distinguish a check that covers the changed package from a check in
another package, and a test from a build.

## 4. Measurements

Per case and per hypothesis:

- final state (`COMPLETED` / `NEEDS_REVIEW`);
- whether that state is true / false;
- model calls (stream invocations);
- tool calls (outcome count);
- wall-clock latency;
- request context bytes (JSON size of the provider request);
- verification files read and bytes hashed;
- contract/evidence bytes (bounded assessment input).

Aggregate:

```text
true completion
false completion
false review (correct work marked NEEDS_REVIEW)
missed completion
NEEDS_REVIEW rate
verification coverage
```

The benchmark passes only if:

1. H1 has zero false completions on the matrix;
2. correct-completion cases A, F, J, L, M, O, Q, R remain `COMPLETED`;
3. H1 completes cases that H0 completed correctly (no regression);
4. every H1 `NEEDS_REVIEW` has a non-empty evidence reason.

## 5. Reproduction

```sh
OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -v
```

The benchmark uses the real `write`, `edit`, `delete`, `bash`, and `symbols`
tools behind the real security gate. Results are written to
`docs/benchmarks/results/phase4-completion.json`.

H0 is the frozen `EvaluateCompletion` applied to the same recorded turns; H1 is
`EvaluateTaskCompletion`. No run is repeated between the two evaluations, so the
only difference is the completion contract, not model or tool variance.

## 6. Closure note

The 23 cases above are unchanged by the closure audit. Two additional
contracts discovered during closure — verification scope relative to the
command's effective working directory, and cancellation during post-run
evidence — are covered by separate regression tests in `tools`, `agent`, and
`tui`; they are not silently added to this matrix or used to change its
expected states.
