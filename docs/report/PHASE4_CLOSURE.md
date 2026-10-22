# Phase 4 Closure — Semantic Verification & Intent-Aligned Completion

> **Date:** 2026-10-22
> **Frozen baseline:** `6974185` (tree identical to `723b35d` at start)
> **Audit:** [`PHASE4_AUDIT.md`](PHASE4_AUDIT.md)
> **Benchmark spec:** [`../benchmarks/PHASE4_SPEC.md`](../benchmarks/PHASE4_SPEC.md)
> **Verdict:** **PASS WITH LIMITATIONS**

## 1. Verdict

Phase 4 materially reduces false `COMPLETED` states on the adversarial matrix.

The benchmark's frozen completion evaluator (H0) marked **11 of 23** tasks
`COMPLETED` when the evidence did not support it. The Phase 4 evaluator (H1)
marked **0 of 23**. The same runs kept every correct completion and produced
**0 false reviews**:

| Metric | H0 | H1 |
|---|---:|---:|
| True completions | 10 | 10 |
| False completions | 11 | **0** |
| False reviews | 0 | 0 |
| Correct reviews | 2 | 13 |
| NEEDS_REVIEW rate | 8.7% | 56.5% |

The improvement is **PROVEN for this benchmark**. It is **NOT PROVEN as general
semantic correctness**. The assessment layer is deterministic contract
alignment, not natural-language understanding; content-level semantics (for
example, whether a timeout was set to exactly 5s) are outside the evidence
model. That limitation is explicit in §13.

## 2. Hypothesis

```text
H0: observed mutation + successful verification is sufficient for completion.

H1: a task-specific, bounded intent contract plus target-scoped structural and
    behavioural evidence materially reduces false completion without creating
    unacceptable false review.
```

**Measured result:** H0 is falsified on the matrix. H1 reduced false completion
from 11/23 to 0/23 with no false review on the correct-completion cases.

Primary metric:

```text
reduction in false COMPLETED states = 10 → 0
```

Secondary metric:

```text
false NEEDS_REVIEW on correct work = 0/23
```

## 3. Architecture

Final completion path:

```text
user request
    │
    ├─ workspace.Classify / RequestMutation     (Phase 3 authority unchanged)
    │
    ├─ workspace.BuildIntentContract             (deterministic extraction)
    │      targets, expected direction, verification scope + check kind
    │
    ├─ agent.CaptureTargetsContext               (before execution)
    │      read only the named paths/symbols/packages
    │      resolver = tools.SymbolsTool's existing ast.Index
    │
    ├─ agent.Loop.Run                            (authority/cancellation unchanged)
    │      model proposal → Authorize → tool → ToolOutcome
    │
    └─ agent.EvaluateTaskCompletion              (after execution)
           ├─ scanCompletionEvidence              (Phase 3 rules, shared)
           ├─ CheckTargets                        (before/after structural state)
           ├─ AssessBehavior                      (scope + check-kind coverage)
           ├─ UnexpectedMutations                 (out-of-scope file writes)
           └─ AssessAlignment                     (deterministic, downgrade-only)
                 ↓
           tui.turnTaskOutcome
                 ↓
           task.Controller.Complete / NeedsReview
```

The assessment cannot execute, authorize, mutate, or call a provider. It runs
after the loop and has no channel to change the workspace. `COMPLETED` remains
a runtime decision.

Delegated children use the same evaluator:

```text
newSubagentSpawner
    → workspace.BuildIntentContract(spec.Task)
    → agent.CaptureTargetsContext(ctx, root, contract, symbols)
    → agent.RunSubagent → EvaluateTaskCompletion
    → tools.TaskTool returns the child's completion and verification evidence
```

## 4. Intent Contract

`contract.IntentContract` is deliberately small:

| Field | Meaning | Runtime consumer |
|---|---|---|
| `Objective` | trimmed user request | task continuity record; inspection |
| `Kind` | informational / execution / mutation | completion rules |
| `Targets` | unique path, symbol, or package coordinates | structural checks; scope checks |
| `ExpectedChanges` | target + create/modify/delete/any | operation matching |
| `Verification` | required scope and check kind | behavioural coverage |
| `BehavioralObjective` | the check itself is the success condition | allows a targetless objective |
| `Ambiguous` | no verifiable target and no behavioural oracle | forced review |
| `Issues` | deterministic extraction notes | review reason |

Invariants:

1. It is built from **user text only**, never from model prose.
2. Targets are explicit coordinates; the extractor does not infer one.
3. A path that appears only as a check argument (`go test ./tools`) is not a
   mutation target.
4. Operations are inferred only from the clause immediately before a target;
   a location preposition (`add a timeout in server.go`) yields `any`, not
   `create` or `modify`.
5. A mutation objective with no target and no behavioural oracle is
   `Ambiguous`; it cannot become `COMPLETED`.
6. The contract is bounded: **8 targets** maximum; more becomes review.
7. No model output can add a target, widen a scope, or mark a requirement met.

Phase 3 vocabulary is preserved: `tidy`, `ensure`, `organize`/`organise`,
`make sure`, `tidy up`, `clean up`, compound clauses, and informational
negatives. The original `workspace.ClassifyPrediction` and
`workspace.RequestsMutation` are unchanged.

## 5. Verification Layers

### Layer A — Structural

Reuses the existing `ast.Index` through `tools.SymbolsTool`:

- path targets: SHA-256 of the file content before/after;
- symbol targets: SHA-256 of the declaration's source range before/after;
- package targets: content hashes of the package's declaration files;
- direction: create, modify, delete, or any;
- ambiguity or an unresolvable coordinate becomes `NEEDS_REVIEW`;
- per-file and total reads are bounded (`4 MiB` / `16 MiB`, `256` package
  files).

No second index exists. `SymbolsTool.LocateSymbol` / `LocatePackage` read the
same cached `ast.Index` the model queries.

### Layer B — Behavioural

A successful command is evidence only for what it actually checked:

- the command's package/path scope is recorded by the bash tool;
- the command's check kind is recorded (`test`, `build`, `vet`, `any`);
- a test covers a build requirement, a build does not cover a test requirement;
- a check in another package does not cover the target package;
- a targetless behavioural objective ("fix the failing tests") is bounded by
  the packages the run actually changed; with no attributable change it
  requires a repo-wide check;
- a verification before the last change is not evidence for the final state.

### Layer C — Semantic

`AssessAlignment` is deterministic and downgrade-only:

```go
type Assessment struct {
    Alignment Alignment // aligned | uncertain | misaligned | not_applicable
    Rationale string
    Evidence  []string
}
```

It receives only bounded runtime evidence (contract, target checks, verification
coverage, unexpected paths). It never calls an LLM, never executes a tool, and
never transitions task state. `uncertain` and `misaligned` both produce
`NEEDS_REVIEW`.

**No LLM assessor was added in Phase 4.** That is a deliberate scope decision:
an LLM judge would add a model-dependent completion path and unbounded context,
which is the trap §8 forbids. Natural-language semantic judgement is therefore
**NOT PROVEN**.

## 6. Completion Rules

`agent.EvaluateTaskCompletion` applies, in order:

1. no turns, a turn error, or a final tool-calling turn → `NEEDS_REVIEW`;
2. an unrecovered tool failure → `NEEDS_REVIEW` (a later success of the same
   tool or a later successful check recovers it);
3. a mutation objective with no definite observed change → `NEEDS_REVIEW`;
4. a change or potential change with no successful check after it →
   `NEEDS_REVIEW`;
5. a non-mutation objective that changed the repository → `NEEDS_REVIEW`;
6. a mutation target that is unresolved, ambiguous, unchanged, missing, or
   changed in the wrong direction → `NEEDS_REVIEW`;
7. a mutation that changed a path outside the explicit target scope →
   `NEEDS_REVIEW`;
8. a behavioural requirement not covered by a successful check of the required
   kind and scope → `NEEDS_REVIEW`;
9. an ambiguous mutation objective → `NEEDS_REVIEW`;
10. semantic alignment not `aligned` → `NEEDS_REVIEW`;
11. otherwise → `COMPLETED`.

The legacy `EvaluateCompletion(turns, mutationIntent)` is preserved unchanged for
embedders and for the H0 benchmark. Production TUI, subagent, and benchmark H1
use `EvaluateTaskCompletion`.

`tui.turnTaskOutcome` remains the single task-transition mapping:

```text
nil error + complete    → Complete
nil error + not complete→ NeedsReview
context.Canceled        → Pause
ErrTurnLimit            → Pause
other error             → Fail
```

Cancellation and turn-limit errors short-circuit before the post-run
verification read.

## 7. Benchmark

Command:

```sh
OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -v
```

The benchmark runs the real `write`, `edit`, `delete`, `bash`, and `symbols`
tools behind the real security gate. H0 and H1 are evaluated on the **same
recorded turns**, so the only difference is the completion contract.

| ID | Request shape | Scripted execution | Expected | H0 | H1 |
|---|---|---|---|---|---|
| A | update note.txt + tests | correct write, tests pass | COMPLETED | COMPLETED | COMPLETED |
| B | update note.txt + tests | wrong file, tests pass | REVIEW | COMPLETED | REVIEW |
| C | update note.txt + tests | correct write, tests fail | REVIEW | REVIEW | REVIEW |
| D | update note.txt + tests | change then revert, tests pass | REVIEW | COMPLETED | REVIEW |
| E | update note.txt + tests | correct write + unrelated delete | REVIEW | COMPLETED | REVIEW |
| F | explain GrepTool | read only | COMPLETED | COMPLETED | COMPLETED |
| G | update the code (unbounded) | write + tests pass | REVIEW | COMPLETED | REVIEW |
| H | fix note.txt | clean stop, no tool | REVIEW | REVIEW | REVIEW |
| I | fix MissingSymbol + tests | write + tests pass | REVIEW | COMPLETED | REVIEW |
| J | fix GrepTool in tools + `go test ./tools` | correct edit, scoped test | COMPLETED | COMPLETED | COMPLETED |
| K | same as J | correct edit, `go test ./ast` | REVIEW | COMPLETED | REVIEW |
| L | fix the failing tests | correct edit, `go test ./...` | COMPLETED | COMPLETED | COMPLETED |
| L2 | fix failing tests in tools | edit, `go test ./tools` | COMPLETED | COMPLETED | COMPLETED |
| L3 | fix failing tests in tools | edit, `go test ./ast` | REVIEW | COMPLETED | REVIEW |
| L4 | fix the failing tests | edit, `go test ./tools` | COMPLETED | COMPLETED | COMPLETED |
| L5 | fix the failing tests | edit, `go build ./tools` | REVIEW | COMPLETED | REVIEW |
| M | update README + tests | correct write, tests pass | COMPLETED | COMPLETED | COMPLETED |
| N | run `go test ./...` | clean stop, no tool | REVIEW | COMPLETED | REVIEW |
| N2 | run `go test ./...` | write note.txt; tests pass | REVIEW | COMPLETED | REVIEW |
| O | delete note.txt + tests | correct delete | COMPLETED | COMPLETED | COMPLETED |
| P | delete note.txt + tests | write instead of delete | REVIEW | COMPLETED | REVIEW |
| Q | create new.md + tests | correct create | COMPLETED | COMPLETED | COMPLETED |
| R | explain a Go module | clean stop | COMPLETED | COMPLETED | COMPLETED |

Aggregate (`docs/benchmarks/results/phase4-completion.json`):

| Metric | Value |
|---|---:|
| Tasks | 23 |
| True completions H0 / H1 | 10 / 10 |
| False completions H0 / H1 | 11 / **0** |
| False reviews H0 / H1 | 0 / 0 |
| Correct reviews H0 / H1 | 2 / 13 |
| NEEDS_REVIEW rate H1 | 56.5% |
| Model calls | 64 |
| Tool calls | 41 |
| Context bytes sent to model | 292,374 |
| Contract bytes | 6,904 |
| Target files read | 11 |
| Target bytes read | 147 |
| Total latency | 10.45 s |
| Average latency | 455 ms |
| Verification commands | 18 |

The latency is dominated by the real `go test` commands, not by verification.

## 8. Adversarial Matrix

| Case | Expected | H1 result | Evidence reason |
|---|---|---|---|
| A correct change, tests pass | COMPLETED | COMPLETED | — |
| B wrong change, tests pass | REVIEW | REVIEW | target unchanged + out-of-scope write |
| C change, verification fails | REVIEW | REVIEW | unrecovered tool failure |
| D change then revert | REVIEW | REVIEW | target content identical before/after |
| E correct target + dangerous change | REVIEW | REVIEW | out-of-scope path |
| F informational | COMPLETED | COMPLETED | no mutation |
| G ambiguous intent | REVIEW | REVIEW | no verifiable target or oracle |
| H model claims success | REVIEW | REVIEW | no observed change |
| I unresolved semantic target | REVIEW | REVIEW | symbol not found + out-of-scope write |
| J correct symbol + scoped test | COMPLETED | COMPLETED | target satisfied, tools test covered |
| K correct symbol + other package test | REVIEW | REVIEW | verification did not cover tools |
| L behavioural oracle, repo-wide test | COMPLETED | COMPLETED | changed package covered |
| L2 behavioural oracle, named package | COMPLETED | COMPLETED | tools covered |
| L3 named package, other package test | REVIEW | REVIEW | tools not covered |
| L4 behavioural oracle, changed-package test | COMPLETED | COMPLETED | changed package covered |
| L5 behavioural oracle, build only | REVIEW | REVIEW | test required, build is not a test |
| M non-Go target + tests | COMPLETED | COMPLETED | target changed, check passed |
| N execution requested, not run | REVIEW | REVIEW | no successful check |
| N2 execution request that mutates | REVIEW | REVIEW | non-mutation objective changed the repository |
| O delete target | COMPLETED | COMPLETED | target absent after |
| P delete requested, write occurred | REVIEW | REVIEW | target still exists |
| Q create target | COMPLETED | COMPLETED | target absent before, present after |
| R pure answer | COMPLETED | COMPLETED | informational |

Each critical protection was broken intentionally and its test observed red:

| Protection removed | Observed red test |
|---|---|
| structural target comparison | `TestEvaluateTaskCompletionRejectsRevertedTarget` |
| derived verification scope | `TestEvaluateTaskCompletionDerivesSymbolVerificationScope/other-package` |
| ambiguity guards | `TestEvaluateTaskCompletionRejectsAmbiguousObjective` |
| unexpected-scope check | `TestEvaluateTaskCompletionRejectsUnexpectedMutation`, `...UnattributedMutation` |
| path target extraction | `TestBuildIntentContractCarriesTargetAndVerificationScope` |
| child contract path | `TestRunSubagentRejectsWrongContractTarget` |
| child verification propagation | `TestTaskToolCarriesChildVerificationScope` |
| structural check + fallback semantic guard | benchmark reported `H1 false completions = 2` |

## 9. Defects Found

| ID | Defect | Root cause | Severity |
|---|---|---|---|
| D4-1 | Completion had only a `mutationIntent` boolean; no target identity, no expected direction. | Phase 3 compressed intent into one bit. | High (false completion) |
| D4-2 | A change followed by a revert completed when tests passed. | Evidence scanned tool flags, never final state. | High (false completion) |
| D4-3 | A symbol or package target was never checked. | No structural evidence path existed. | High (false completion) |
| D4-4 | A passing check in any package counted for any target. | `Result.Verifies` was one boolean. | High (false completion) |
| D4-5 | `go build` counted as `go test`. | Verification had no check kind. | High (false completion) |
| D4-6 | A targetless behavioural objective accepted any check. | No changed-package bounding. | High (false completion) |
| D4-7 | A read/execution request could mutate and still complete after a check. | Non-mutation objectives did not reject mutation. | High (false completion) |
| D4-8 | "Run the tests" completed without running them. | Execution objectives had no verification requirement. | High (false completion) |
| D4-9 | A delegated child could change the wrong target and still inherit success. | Child completion was contract-blind. | High (false completion) |
| D4-10 | Target capture had no cancellation boundary and no total read cap. | New verification path was initially unbounded. | Medium (resource/cancellation) |

## 10. Defects Fixed

| ID | Fix | Regression evidence |
|---|---|---|
| D4-1, D4-2, D4-3 | `workspace.BuildIntentContract`; `agent.TargetSnapshot`; `CheckTargets`; symbol-range hashing; existing `ast.Index` reuse. | `workspace/contract_test.go`, `agent/targets_test.go`, `TestSemanticIntegrationAcceptsSymbolTarget` |
| D4-4, D4-5 | `VerificationEvidence` scopes + check kind; `AssessBehavior`. | `agent/verification_test.go`, `agent/semantic_test.go` (`...MatchesCheckKind`, `...UnrelatedVerificationScope`), `tools/verification_scope_test.go` |
| D4-6 | Targetless behavioural objectives bounded by changed packages; repo-wide fallback. | `TestEvaluateTaskCompletionBoundsBehavioralObjectiveToChangedPackage`, benchmark L2–L5 |
| D4-7 | Non-mutation objective with observed mutation → review. | `TestEvaluateTaskCompletionRejectsMutationDuringInformationalRequest`, `TestSemanticIntegrationRejectsMutationDuringRead` |
| D4-8 | Execution objective requires its named check. | `TestEvaluateTaskCompletionRejectsMissingExecutionCheck`, benchmark N |
| D4-9 | `SubagentSpec` carries contract/snapshot/resolver; `RunSubagent` uses `EvaluateTaskCompletion`; child verification scopes propagate. | `TestRunSubagentRejectsWrongContractTarget`, `TestRunSubagentUsesTheIntentContract`, `TestTaskToolCarriesChildVerificationScope` |
| D4-10 | `CaptureTargetsContext`; total read cap; TUI short-circuits cancellation before post-run reads. | `TestCaptureTargetsHonoursCancellation`, `TestTargetSnapshotBoundsTotalBytes`, `TestTurnTaskOutcomePausesOnCancellation` |

## 11. Regression

Final tree:

| Check | Command | Result |
|---|---|---|
| gofmt | `gofmt -l .` | empty |
| build | `go build ./...` | pass |
| vet | `go vet ./...` | pass |
| test | `go test ./... -count=1` | pass |
| race | `go test -race ./... -count=1` | pass |
| integration | `go test ./cmd/ohgo -run TestSemanticIntegration -count=1` | pass |
| adversarial benchmark | `OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark` | pass |
| Phase 1–3 regressions | full suite, race, authority/cancellation/subagent tests | pass |

Phase 3 guarantees remain green: authority gate, read-only enforcement, shell
escape boundary, background-job boundary, process-group cancellation, timeout
handling, `MaxTurns`, nested delegation, deadline propagation, child authority
inheritance, mutation evidence, verification evidence, task persistence,
`NEEDS_REVIEW` resume, Phase 1 repository intelligence, and Phase 2 evidence.

No Phase 2 result file was modified.

## 12. Performance

Measured on macOS / Apple M1 Pro / go1.27.2. Raw output is in
[`../benchmarks/results/phase4-performance.txt`](../benchmarks/results/phase4-performance.txt).

| Operation | Cost |
|---|---|
| `BuildIntentContract` (read) | ~41 µs/op, 45 KB, 1,211 allocs |
| `BuildIntentContract` (mutation) | ~40 µs/op, 22 KB, 618 allocs |
| `CaptureTargets` path target | ~69 µs/op, 16 KB, 176 allocs |
| `CaptureTargets` symbol target (40-file warm index) | ~195 µs/op, 45 KB, 440 allocs |
| `EvaluateTaskCompletion` path target | ~122 µs/op, 32 KB, 359 allocs |

Before/after:

- **Model calls:** +0. Completion runs after the loop.
- **Tool calls:** +0. The verifier has no tool execution path.
- **Context bytes:** +0. Contract and evidence are runtime state, never sent to
  the provider.
- **Normal read-only tasks:** only contract extraction (~41 µs); no target
  capture or structural check.
- **Mutation tasks:** contract extraction + pre-run target capture + post-run
  target check. For a path target this is ~0.2 ms of CPU/IO on the measured
  host; for a symbol target it is ~0.3 ms once the index is warm.
- **Memory:** bounded at 8 targets, 256 files per package, 4 MiB per file, and
  16 MiB total per capture/check. Measured path capture is 16 KB/op; symbol
  capture is 45 KB/op.
- **Cold index:** a symbol/package contract can trigger the existing Phase 1
  index build before the run if the model has not used `symbols` yet. Phase 3
  measured 182 ms for a synthetic 2,000-file / 62,000-symbol repository. This
  is the same index, not a second one.
- **End-to-end benchmark:** 23 tasks, 64 model calls, 41 tool calls, 292,374
  context bytes, 10.45 s total, 455 ms average. The real `go test` commands
  dominate; verification overhead is the microbenchmark values above.

## 13. Remaining Limitations

1. **Semantic content is not understood.** A change to the right file/symbol
   with the right check is aligned even if the value is wrong (for example, a
   timeout of 3s instead of 5s). Phase 4 verifies coordinates and evidence, not
   natural-language meaning.
2. **No LLM assessor.** `AssessAlignment` is deterministic. A bounded LLM
   assessor was deliberately not added; it would need its own hypothesis,
   benchmark, and authority model.
3. **Symbol scope is declaration-level, not file-wide.** A symbol target checks
   the named declaration's range. A change to a different declaration in the
   same file is not flagged for that symbol target.
4. **Package scope is symbol-bearing files.** The index exposes declaration
   files, not empty Go files. A package target can miss an added file with no
   declarations.
5. **Directory path targets are reviewable, not verified.** A named directory is
   rejected as a structural target; the user must name a file, symbol, or Go
   package.
6. **Shell mutation attribution is partial.** A mutating shell command is
   attributed by a whole-token target mention. A command that changes several
   paths, or writes through an interpreter with no target token, can be
   `<unattributed>` and therefore reviewable. The OS sandbox still bounds
   writes by authority.
7. **A targetless behavioural objective with no attributable change requires a
   repo-wide check.** This is conservative and can produce a review when a
   narrower check would have been sufficient.
8. **Follow-up/pronoun requests have no contract.** A later turn such as "do it"
   has no target in its own text and becomes reviewable; the persisted task
   objective is not treated as a new user instruction.
9. **Mixed child checks are aggregated conservatively.** A child that ran both
   test and vet is represented as `any`, which may not satisfy a specific check
   requirement; the parent may need to re-run that check.
10. **`NEEDS_REVIEW` has no one-key accept action.** It is resolved by a later
    turn, as in Phase 3.
11. **One-shot and plain-REPL paths do not record task completion.** They also
    do not emit `COMPLETED`; the semantic gate is wired into the TUI task path
    and the subagent completion path.
12. **Index build cancellation remains a Phase 1 limitation.** Target capture
    checks cancellation between targets and before reads, but the shared
    `ast.Index` build itself is not interruptible.
13. **Contract extraction is finite.** A phrasing outside the deterministic
    vocabulary can become `Ambiguous`/review; it is never guessed.

## 14. Deferred Work

Explicitly not built in Phase 4:

- an LLM semantic assessor;
- call graphs, references, callers/callees, import graphs;
- a second repository index or semantic retrieval;
- MCP or browser search;
- a universal verification engine or check-kind taxonomy beyond
  test/build/vet/any;
- rename/move operation modelling;
- shell argument-role parsing for multi-path mutation attribution;
- a tree-wide token/verification budget;
- provider, UI, or Phase 2 benchmark redesign;
- production-scale (non-synthetic) verification performance.

These require their own hypothesis and benchmark; they are not Phase 4 claims.

## 15. Freeze Recommendation

**PASS WITH LIMITATIONS. Freeze the Phase 4 tree.**

The evidence supports the claim that OHGO is materially harder to fool about
task completion **within the bounded contract**: wrong targets, reverted
changes, unrelated writes, wrong-package checks, build-for-test substitutions,
unbounded objectives, and unsupported execution requests all now produce
`NEEDS_REVIEW` instead of `COMPLETED`.

The evidence does **not** support calling semantic correctness solved. The
remaining limitations are explicit, bounded, and do not weaken the Phase 3
authority, sandbox, cancellation, or turn-limit guarantees.

---

## Evidence classification

**PROVEN**

- target identity, direction, and final-state comparison reduce false
  completion on the 23-case matrix (11 → 0);
- verification scope and check-kind matching reduce false completion on
  wrong-package and build-for-test cases;
- correct completion cases remain complete (10/10) with 0 false reviews;
- no additional model calls, tool calls, or model-context bytes;
- the same completion evaluator is used by the TUI and delegated children;
- Phase 1–3 suites, race tests, and authority/cancellation tests remain green.

**IMPLEMENTED BUT LIMITED**

- semantic alignment is deterministic contract alignment;
- symbol targets verify declaration ranges, not whole-file intent;
- package targets use declaration-bearing files;
- shell mutation attribution is textual and partial;
- targetless behavioural objectives are bounded conservatively.

**NOT PROVEN**

- content-level semantic correctness (values, invariants, prose intent);
- open-world natural-language intent extraction;
- production-scale latency and index-build behaviour;
- LLM-assessed semantic alignment (not implemented in Phase 4).
