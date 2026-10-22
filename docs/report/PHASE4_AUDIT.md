# Phase 4 Audit — Semantic Verification

> **Date:** 2026-10-22
> **Frozen baseline:** `6974185` (tree identical to `723b35d`, the Phase 3 parent of Phase 4)
> **Method:** source read, no implementation assumptions.

## 1. Current completion paths

Only two paths can produce a `COMPLETED` state.

| Path | Entry | Decision |
|---|---|---|
| Interactive turn | `tui/app.go` `runTurn` → `turnTaskOutcome` | `agent.EvaluateCompletion(turns, workspace.RequestsMutation(text))`; on complete calls `task.Controller.Complete` |
| Delegated child | `agent.RunSubagent` | `agent.EvaluateCompletion` plus `CompletionEvidence`; `tools.TaskTool` converts a non-complete child into an error result |

The one-shot CLI path (`cmd/ohgo/run.go` `runOneShot`) and the plain REPL
(`cmd/ohgo/run.go` `runREPL`) call `loop.Run` and print the result. They do not
record a task lifecycle, so they cannot emit `COMPLETED`; they are out of the
completion-state audit but still share the loop, tools, and evidence.

## 2. Current evidence sources

| Evidence | Producer | Consumed by |
|---|---|---|
| `Result.Mutates` | write/edit/delete; bash command classifier | `scanCompletionEvidence` |
| `Result.Executes` | bash | `scanCompletionEvidence` (potential mutation) |
| `Result.Verifies` | bash + `tools.IsVerificationCommand` | `scanCompletionEvidence` |
| `Authorization.Mutates` / `Scope` | `securityGate` | completion (mutation), UI/audit |
| tool `Details.path` / operation | write/edit/delete | UI only |
| todo snapshot | todo tool | `task.Controller.Complete` open-work check |
| failed `ToolOutcome` | loop | completion (unrecovered failure) |

## 3. Where intent is lost

The chain currently carries one bit: `mutationIntent` from
`workspace.RequestsMutation(text)`.

```text
user request ──RequestsMutation──▶ bool ──▶ EvaluateCompletion
```

Lost at the boundary:

1. **Which target** the user named (path, symbol, package).
2. **What operation** was requested (create / modify / delete / any).
3. **Whether that target actually changed**; only "something changed" is known.
4. **Whether the verification covered the target**; only "a verification
   command passed" is known.
5. **Whether a change happened outside the requested scope**.
6. **Whether the request was execution-only** ("run the tests") rather than a
   mutation or a read; the old evaluator treats both as `mutationIntent=false`.
7. **Whether a change-then-revert occurred**; final state is never compared to
   the starting state.

## 4. Demonstrated false-completion shapes

The current rules accept all of these when a successful verification follows:

- wrong file changed;
- target changed, then reverted, then tests pass;
- requested target changed plus an unrelated deletion;
- unresolvable or absent target, with some other file changed;
- ambiguous mutation objective with no bounded target.

The current rules also accept an execution objective that was never executed
("run the tests" → clean stop → no verification evidence, but
`mutationIntent=false`).

## 5. Reusable runtime pieces

- `workspace.ClassifyPrediction` / `RequestsMutation` remain the intent
  authority. Phase 4 extends, not replaces, them.
- `ast.Index` and `ast.GoBackend` provide symbol/package coordinates. There is
  one index per `tools.SymbolsTool`; Phase 4 must consume that instance through
  a resolver, not build a second one.
- `tools.IsVerificationCommand` already scopes what counts as a check.
- `security.Decision.Scope` gives a canonical absolute path for file-tool
  mutations; bash is workspace-scoped because the command is its scope.
- `task.StatusNeedsReview` already persists non-terminal review state.

## 6. Hypothesis

```text
H0: observed mutation + successful verification is sufficient for completion.

H1: a task-specific, bounded intent contract plus target-scoped structural
    and behavioral evidence materially reduces false completion without
    creating unacceptable false review.
```

The benchmark in `docs/benchmarks/PHASE4_SPEC.md` distinguishes them on the
same scripted runs: H0 is the frozen `EvaluateCompletion`; H1 is the Phase 4
evaluator.

## 7. Threat model / non-goals

Phase 4 cannot prove natural-language semantics from arbitrary prose. It will:

- prove that explicitly named targets changed in the requested direction;
- prove that a verification command covers the changed Go target;
- prove that the final target state differs from the initial state;
- treat unresolved, ambiguous, or unbounded objectives as `NEEDS_REVIEW`;
- never let a semantic assessment mutate, authorize, or execute anything.

It will not add an LLM judge, a second index, call graphs, MCP, browser search,
or a new orchestration layer. Shell mutations cannot be attributed to a path by
the current security scope (the command's scope is the workspace), so
out-of-scope shell writes remain a documented limitation rather than a claimed
detection.

## 8. Implementation decisions after the audit

The audit fixed the scope of the phase; four decisions followed from it.

1. **No LLM assessor.** The semantic layer is a deterministic alignment
   assessment over the contract, target checks, and verification coverage. It
   can only downgrade to `NEEDS_REVIEW`; it never calls a provider, executes a
   tool, or mutates state. Natural-language semantic judgement is not claimed.
2. **Verification is scoped in two dimensions.** A check must cover the changed
   package directory *and* the kind of check the request named (test, build,
   vet). A passing build is not a passing test.
3. **A targetless behavioural objective is bounded by what changed.** "Fix the
   failing tests" with no path requires a check over the packages the run
   actually changed; with no attributable change it requires a repo-wide check.
4. **Unknown is review.** Unresolved symbols, ambiguous packages, unsupported
   directory targets, unattributed shell mutations, and over-bound target sets
   all produce `NEEDS_REVIEW`, never a guessed completion.
