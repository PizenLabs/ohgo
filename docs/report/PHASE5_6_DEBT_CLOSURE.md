# Phase 5/6 Debt Closure

- **Date:** 2026-10-22
- **Baseline:** `a4252dfc82c82fc85d267cd0401b7c5d7fb990bf`
- **Final tree:** working tree; no commit was created
- **Scope:** close the remaining Phase 5 resource-model debt and the material
  Phase 6 repository-relationship debt without adding external knowledge
- **Decision frame:** *Dynamic Path, Static Authority, Truthful State Transition*

## 1. Executive Verdict

**PASS — PHASE 5/6 DEBT CLOSURE.**

Phase 5's production budget model already satisfied the declared contract. This
task adds the missing adversarial coverage for retries, sibling children,
cancellation, deadlines, failed children, and concurrent multi-dimension
admission. No budget semantics were changed.

Phase 6's package/import graph is preserved. The debt closure adds an **exact
static subset** of symbol references and direct-call edges over the same
`ast.Index` and `StructuralSource`:

- package-qualified and same-package unqualified references to package-scope
  declarations, with stable `path:line:column` coordinates;
- direct calls to package functions, including generic instantiation and
  method-expression calls whose receiver type resolves to a local declaration with a direct method declaration;
- bounded callers/callees traversal with depth, node, edge, work, cycle, and
  cancellation bounds;
- explicit `ok`, `ambiguous`, `not_found`, `truncated`, and `unsupported`
  states;
- explicit unknowns for method dispatch, function values, dot imports, and
  unresolved targets.

It does **not** implement interface dispatch, reflection, function-value
call resolution, method reference queries, or a type-checking semantic index.
Those are classified, not guessed.

No external-knowledge capability was added. Phase 8 has not begun.

## 2. Original Phase 5 Objectives

The original Phase 5 contract was:

```text
shared budget
child allocation
retry budget
resource accounting
```

The closure question is not "does a theoretical scheduler exist?" It is:

- is there one root-owned grant;
- is admission atomic;
- can a child mint, reset, or exceed it;
- does every retry consume the same authoritative resource;
- are the accounted resources explicit;
- can resource state and completion state disagree into a false completion?

## 3. Objective Matrix

| Original objective | Current implementation | Evidence | Gap | Required? | Action |
|---|---|---|---|---|---|
| Shared budget | One root-owned `budget.Budget`; `Admit`/`AdmitAll` check-and-charge under one mutex; loop, tools, children, and background jobs share the pointer | `budget/budget.go`; `agent/loop.go`; `agent/subagent.go`; `tools/bash.go`; `budget/budget_test.go`; `agent/budget_test.go`; Phase 5 cases 01–30 | None for the declared dimensions | Yes | Preserve; add concurrent `AdmitAll` regression |
| Child allocation | Shared first-admitted pool; one `children` unit per `RunSubagent`; no per-child reservation or proportional split | `agent/subagent.go`; `TestChildCannotResetTheRootGrant`; Phase 5 cases 04–09, 29–30; new sibling/failed-child tests | No reservations/fairness; documented by design | Shared-pool subset is the declared design; reservations are deferred | Preserve; add contention/retry tests |
| Retry budget | No runtime retry loop; every model/tool retry is a new admission; child retry is a new child unit | Phase 5 cases 15–16; new `agent/budget_debt_test.go` retry matrix | None | Yes | Add model/tool/verification/cancellation/deadline/exhaustion coverage |
| Resource accounting | Turns, model calls, tool calls, children, background admissions, absolute deadline; durable cumulative task ledger | `budget.Limits`/`Consumption`/`State`; `task.Progress`; Phase 5 report §4, §12 | Byte/token/monetary/lifetime budgets are not metered | Not required by the declared Phase 5 contract | Preserve; classify unsupported resources explicitly |
| Background execution | Charged at spawn; bounded by per-job timeout, root deadline, and table caps; survives parent turn by design | `tools/bash.go`, `tools/jobs.go`, `tools/budget_test.go`, Phase 7 background tests | Detached lifetime is not a tree budget; no kill on turn cancellation | Accepted Phase 5 limitation | Preserve; no new architecture |
| Import graph | Package nodes and `imports`/`imported_by` edges from the same parse; deterministic, bounded, read-only | `ast/graph.go`; `ast/graph_test.go`; Phase 6 benchmark H1 8/8, 0 false edges | None in its declared package/import scope | Yes | Preserve; regression-tested |
| References | **Not implemented in the original Phase 6 tree.** Now exact static package-scope references over the same index | `ast/relationships.go`; `tools/refs.go`; `ast/relationships_test.go`; `tools/refs_test.go` | Methods unsupported; dot imports and dynamic uses explicit; no type checking | Material Phase 6 debt | Implement exact subset |
| Callers/callees | **Not implemented in the original Phase 6 tree.** Now static direct-call edges and bounded traversal | `ast/relationships.go`; `tools/refs.go`; integration tests | Interface/reflection/function-value dispatch unknown; method expressions only for static calls | Material Phase 6 debt | Implement exact subset |
| Dependency traversal | Package import BFS plus new call-graph BFS; depth/node/edge/work bounds; cycle-safe; cancellation-aware | `ast/graph.go`; `ast/relationships.go`; bounds/cancellation/cycle tests | No whole-program semantic graph | Package + static-call subset is the provable model | Preserve and extend |
| Freshness | Size+mtime Go-file snapshot; graph/refs additionally fingerprint `go.mod`; stale snapshots are not served silently | `ast/index.go`; `TestGraphFreshness`; `TestReferencesFreshness` | Same-size + same-mtime rewrite undetected; full rebuild, no incremental index | Inherited Phase 1/6 limitation | Preserve; document |
| Single repository source | `tools.StructuralSource` owns one lazy `ast.Index`; `symbols`, `graph`, and `refs` share it | `tools/source.go`; `cmd/ohgo/run.go`; source-identity tests | None | Yes | Preserve; add refs to the identity test |
| Security boundary | `refs` is `TierRead`; path containment; no mutation/execution/verification flags; child allowlist updated | `tools/refs.go`; `agent/subagent.go`; `cmd/ohgo/phase6_debt_integration_test.go` | None | Yes | Preserve; test ASK/FUZZ/BUILD |
| Verification boundary | Graph/refs evidence cannot mark a task verified or complete a mutation | `TestRefsEvidenceCannotCompleteAMutation`; Phase 6 graph tests | None | Yes | Preserve |

## 4. Phase 5 Current State

**PASS for the declared dimensions.** The production budget model is unchanged:

- one root-owned `Budget` per run, shared by the loop, every child, every
  tool, and every background admission;
- `Admit`/`AdmitAll` check and charge under one lock; a failed admission
  charges nothing;
- turns and model calls are charged atomically before a provider request;
- tool calls are charged after authority and cancellation re-check, before
  `Execute`;
- children are charged in `RunSubagent` before child construction;
- background jobs are charged at spawn before `cmd.Start`;
- the root deadline is one absolute instant inherited by the tree;
- exhaustion is explicit, resumable, and cannot become completion without
  Phase 4 evidence;
- the durable task ledger records cumulative consumption across runs.

The declared allocation model is a **shared first-admitted pool**, not
per-child reservations. A child can consume the remaining grant; that stops
the tree rather than overrunning it. This is intentional, tested, and not
reopened by this task.

## 5. Phase 5 Fixes

No production budget code was changed. The fixes are coverage and
classification:

1. Added `agent/budget_debt_test.go`:
   - model failure → retry on the same grant consumes another turn/model call;
   - tool failure → retry needs a new tool admission;
   - verification failure → retry needs a new admission and the failed check
     is not verification evidence;
   - cancellation → consumed units are not refunded; a later run continues
     the same grant;
   - deadline → an expired absolute instant is not resurrected by retrying;
   - budget exhaustion → retrying opens no provider stream;
   - sibling children → exactly one admits the last child unit;
   - failed child → its child unit is not refunded; a retry consumes another.
2. Added `budget.TestConcurrentAdmitAllIsAllOrNothing` for concurrent
   multi-dimension admission.
3. Documented the exact accounting semantics and the unsupported aggregate
   resources in §3 and §6 below.

No retry mechanism was invented. The runtime has no automatic retry loop;
retries are model/host actions and therefore naturally new admissions. That is
the correct contract: `retry != free execution`.

## 6. Phase 5 Accepted Limitations

These are explicit and bounded; none is a hidden budget escape:

- **No per-child reservations or fairness.** One shared pool; first admitted
  wins. A child can starve siblings. This is the declared Phase 5 policy.
- **No tree-wide output-byte, context/token, or monetary budget.** Tools have
  per-call caps and compaction; provider usage is recorded, not converted into
  a hard grant.
- **No lifetime task ceiling.** A user turn is a grant boundary; the durable
  ledger records cumulative use but does not automatically reduce the next
  grant.
- **Detached background jobs survive the parent turn.** They are bounded by
  admission, timeout, root deadline, and table caps. Turn cancellation does
  not kill them. The model must `wait` for terminal evidence.
- **An in-flight provider request is not interrupted at the deadline.** New
  requests stop; the current request may finish.
- **A direct embedder that supplies no parent budget gets a normalized child
  grant.** Production `buildLoop` always installs a root grant; this is a
  library seam, not a production child escape.
- **Manual `/compact` outside an execution tree is not charged.**
- **Accounting overhead is one mutex per admission.** No lock-free path.

## 7. Phase 5 Test Evidence

### 7.1 Targeted tests

| Contract | Test | Result |
|---|---|---|
| exact boundary / one over | `budget.TestAdmissionIsExactAndMonotonic` | PASS |
| all-or-nothing multi-dimension | `budget.TestAdmitAllChargesNothingOnFailure` | PASS |
| concurrent single dimension | `budget.TestConcurrentAdmissionNeverExceedsTheGrant` | PASS |
| concurrent multi-dimension | `budget.TestConcurrentAdmitAllIsAllOrNothing` | PASS |
| root turn stop before provider | `agent.TestRootTurnBudgetStopsBeforeAnUnadmittedModelCall` | PASS |
| model-call stop before stream | `agent.TestModelCallBudgetStopsBeforeOpeningAStream` | PASS |
| tool batch admits exactly the remainder | `agent.TestToolBudgetAdmitsExactlyTheRemainingCalls` | PASS |
| model retry on same grant | `agent.TestRetryAfterModelFailureStaysInsideTheGrant` | PASS |
| tool retry needs admission | `agent.TestRetryAfterToolFailureNeedsANewAdmission` | PASS |
| verification retry needs admission | `agent.TestRetryAfterVerificationFailureNeedsANewAdmission` | PASS |
| cancellation does not refund | `agent.TestRetryAfterCancellationDoesNotResetTheGrant` | PASS |
| deadline does not resurrect | `agent.TestRetryAfterDeadlineDoesNotResurrectBudget` | PASS |
| exhaustion retry opens no stream | `agent.TestRetryAfterBudgetExhaustionMakesNoProviderCall` | PASS |
| sibling child contention | `agent.TestConcurrentChildrenCannotBothAdmitTheLastUnit` | PASS |
| failed child retry | `agent.TestFailedChildRetryConsumesAnotherChildUnit` | PASS |
| child cannot reset root | `agent.TestChildCannotResetTheRootGrant` | PASS |
| background refusal before start | `tools.TestBackgroundBudgetRefusesWithoutStartingAJob` | PASS |
| background deadline cap | `tools.TestBackgroundJobIsCappedByTheRootDeadline` | PASS |
| expired deadline refuses spawn | `tools.TestExpiredRootDeadlineRefusesBackgroundWork` | PASS |

### 7.2 Phase 5 benchmark

Command:

```sh
OHGO_PHASE5_BENCH=1 go test ./cmd/ohgo -run TestPhase5Benchmark -count=1
```

Result: **30/30 cases pass**, 0 budget overruns, 0 execution after failed
admission, 0 child escapes, 0 background escapes, 0 deadline escapes, 0
cancellation escapes, 0 false completions, 0 false reviews. Totals: 74 model
calls, 41 tool calls, 23 tool executions, 3 background jobs, 1,506 ms wall time
(wall time varies by run).
Artifact: [`../benchmarks/results/phase5-resource.json`](../benchmarks/results/phase5-resource.json).

### 7.3 Resource/accounting classification

| Resource | Metered? | Unit | Owner / charge point |
|---|---|---|---|
| turns | yes | admitted agent turn | root budget; before provider request |
| model calls | yes | provider request | root budget; before `Stream` and compaction summary |
| tool calls | yes | admitted execution | root budget; after authority, before `Execute` |
| children | yes | delegated run | root budget; in `RunSubagent` before construction |
| background jobs | yes | detached job admission | root budget; at spawn before `cmd.Start` |
| wall time | yes | absolute deadline | root budget; one inherited instant |
| output bytes | no | per-call cap | tool-specific caps |
| context/token usage | no | provider window / compaction | provider usage is recorded, not a hard grant |
| monetary cost | no | provider pricing | not observed as an enforceable resource |
| retained job table | no | per-table cap | 32 retained / 8 running |
| delegation depth | no | structural levels | `--max-depth` |

## 8. Original Phase 6 Objectives

The original Phase 6 target was:

```text
references
callers/callees
import graph
dependency traversal
```

The delivered Phase 6 tree implemented only the package/import graph and
explicitly deferred references, calls, implementations, and symbol/file nodes.
The audit and closure reports classified them as out of scope because the
original parser/index model had no retained relationship data and because
exactness was not proven.

## 9. Phase 6 Current State

### 9.1 Preserved

- One `ast.Index` and one `StructuralSource`.
- One `GoBackend.Parse` pass produces symbols, package clauses, imports,
  references, and call sites.
- Package/import edges are deterministic `ImportSpec` evidence with
  source/test flags, external counts, unresolved paths, and `file:line`
  witnesses.
- Forward (`imports`) and reverse (`imported_by`) traversal over the same
  stored edges.
- Depth, node, edge, and work bounds; deterministic ordering; cycle-safe BFS.
- Cancellation before the query, during freshness, during rebuild, during
  forward traversal, and during reverse-adjacency construction.
- Read-only authority, tool-call budget, and shared child source.
- Freshness including the `go.mod` fingerprint for graph/refs.

### 9.2 Added exact subset

`ast/relationships.go` resolves the raw syntactic projection against the
complete declaration table:

- **References** — a package-qualified or same-package unqualified use of a
  package-scope function, type, var, or const. The result carries
  `path:line:column`, whether it is qualified, whether it is a static call,
  and whether it is in a test file.
- **Callers/callees** — a direct call whose callee resolves to an indexed
  function or a method expression whose receiver type resolves to a local
  declaration. Every edge carries caller coordinate, callee coordinate, call
  site `path:line:column`, `certainty` (`static` or `lexical` for a closure),
  and test/closure flags.
- **Traversal** — callers/callees BFS with depth 1–4, node limit, edge limit,
  work limit, cycle detection, cancellation checks, and explicit truncation
  reasons.
- **Unknowns** — method dispatch, function values, dot imports, and unresolved
  targets are counted and labelled. Package-initializer and anonymous-closure
  callers are materialised with explicit caller kinds. None is turned into a
  false edge.

### 9.3 Query surface

The wire tool is `refs`, `TierRead`, with a closed schema:

| Argument | Contract |
|---|---|
| `symbol` | exact symbol or qualified name; required |
| `relation` | `references` (default), `callers`, `callees` |
| `path` | file or directory inside the workspace; scopes the declaration lookup |
| `depth` | 1–4 for callers/callees; references are one-hop |
| `limit` | 1–200 references, direct-call edges, or traversal nodes |
| `edges` | 1–400 transitive call edges; defaults to `2 * limit`, capped; not valid for a direct call query |
| `skip` | non-negative rows to skip (references or call edges) |

Statuses:

- `ok` — target resolved and the requested projection completed within bounds;
- `ambiguous` — the symbol matched more than one declaration; candidates are
  listed and no winner is chosen;
- `not_found` — no declaration matched;
- `truncated` — a limit, node/edge, depth, or work bound stopped the answer;
- `unsupported` — the relation is not provable for that declaration kind
  (for example, a method reference query).

## 10. Phase 6 Fixes

Production changes:

- `ast/ast.go` — `Options.Relationships`; one `Parse` pass now returns the
  syntactic reference/call projection alongside symbols and imports; imports
  retain their alias.
- `ast/index.go` — `fileRecord` retains raw relationships only until the
  snapshot is built; `Index` stores the resolved `symbolGraph` and rebuilds it
  with the snapshot.
- `ast/relationships.go` — raw extraction, declaration table, import-qualifier
  resolution, exact reference resolution, static call resolution, call-graph
  traversal, bounds, cancellation, and diagnostics.
- `tools/refs.go` — the read-only `refs` tool and compact output/details.
- `cmd/ohgo/run.go` — registers `refs` over the same `StructuralSource`.
- `agent/subagent.go` — adds `refs` to `ReadOnlyToolNames`.
- `cmd/ohgo/phase6_perf_test.go` — measures the symbols-only parse, the
  relationship parse, the cold index, graph queries, reference queries, and
  retained heap.

No second parser, index, daemon, database, or graph service was added.

## 11. Graph Correctness Evidence

| Invariant | Evidence | Result |
|---|---|---|
| Package/import edges unchanged | `TestGraphFollowsImportsAndReverseDependents`, `TestGraphReportsExternalAndUnresolvedImports`, `TestGraphPreservesSelectorAmbiguity`, `TestGraphPreservesPackageIdentityAmbiguity` | PASS |
| Deterministic traversal | `TestGraphOutputIsDeterministic`, repeated-query tests | PASS |
| Bounded traversal | `TestGraphBoundsTraversal`; depth/node/edge/work reasons | PASS |
| Cycle safety | `TestGraphHandlesCyclesAndIsolatedPackages` | PASS |
| Reverse adjacency | `TestGraphReverseAdjacencyHonoursCancellation` | PASS |
| One parse | Phase 6 perf `index/rel_parse` 1.00–1.07; no second parse | PASS |
| Stable witnesses | every import edge carries `source_evidence` / `test_evidence` | PASS |
| Reference coordinates | `TestReferencesResolveStaticSymbolUses` exact `path:line:column` | PASS |
| Local shadowing | `TestReferencesRespectLocalShadowing` | PASS |
| Import qualifier shadowing | `TestReferencesDoNotTreatAnImportQualifierAsASymbol` | PASS |
| Field-key false positives | `TestReferencesResolveStaticSymbolUses` (`b.Field` has zero references) | PASS |
| Ambiguity | `TestReferencesPreserveAmbiguity` | PASS |
| Truncation/pagination | `TestReferencesBoundsAndPagination` | PASS |
| Static call edges only | `TestCallGraphKeepsDynamicDispatchUnknown` | PASS |
| Interface/function-value unknown | `TestCallGraphCallerCompletenessIsConservative` | PASS |
| Package init/closure labels | `TestCallGraphLabelsPackageInitAndClosureCallers` | PASS |
| Cycle/bounds | `TestCallGraphTraversalIsBoundedAndCycleSafe` | PASS |
| H0/H1 package benchmark preserved | H0 4/8, H1 8/8, 0 false edges, 10,780 vs 7,140 context bytes | PASS |

## 12. Freshness Evidence

- `TestReferencesFreshness`:
  - a changed file rebuilds the snapshot and replaces old references;
  - a `go.mod` module-path change invalidates qualified references that are
    now external;
  - a parse failure is counted (`unparsed`) and marks the answer incomplete.
- `TestGraphFreshness` (existing) covers changed, created, deleted, same-size
  rewrite with changed mtime, module change, and parse failure.
- The inherited limitation remains: a same-size rewrite that preserves both
  size and mtime is not detected. The index does not claim otherwise.
- The first lazy `ast.NewIndex` build is not context-cancellable; a graph/refs
  query arriving after the index exists observes the cancellation checks.
- A cancelled rebuild never publishes partial records; the previous snapshot
  remains in place and the query returns the cancellation error.

## 13. Cancellation Evidence

- `TestReferencesHonourCancellation`:
  - cancellation before the query returns `context.Canceled`;
  - cancellation during a 20,000-reference scan stops at the scan check
    (asserts no more than 20 checks), not only at the final check.
- `TestCallGraphTraversalHonoursCancellation`:
  - cancellation at the 20th BFS queue node stops there; a traversal that
    ignored the queue check returns no error.
- `TestGraphCancellation`, `TestGraphReverseAdjacencyHonoursCancellation`, and
  `TestGraphCancellationDuringBuildDoesNotPublishPartialState` remain green.
- Cancellation is never reported as a partial `ok` result. It is an error
  result.

## 14. Performance Evidence

### 14.1 Phase 5 accounting cost

Artifact: [`../benchmarks/results/phase5-performance.txt`](../benchmarks/results/phase5-performance.txt).

macOS / Apple M1 Pro / go1.27.2:

| Probe | Result |
|---|---:|
| bare atomic increment | 9.88 ns/op |
| budget admission, serial | 16.92 ns/op |
| budget admission, 8 goroutines | 101.02 ns/op |
| budget snapshot | 20.69 ns/op |
| 500-tool-turn loop | 21.138 ms total (~42 µs/turn) |
| 100 delegated one-turn children | 645 µs (~6.4 µs each) |

No budget code changed. The serial and snapshot numbers are stable against the
Phase 5 baseline (16.33 ns serial, 100.89 ns at 8 goroutines). The 100-child
probe varies with goroutine scheduling and load (352–645 µs across runs); it is
a synthetic scheduling measurement, not a production claim.

### 14.2 Phase 6 index/query cost, before and after

Artifact: [`../benchmarks/results/phase6-performance.txt`](../benchmarks/results/phase6-performance.txt).

Before (pre-change, same host/session):

| Files | Packages | Cold ms | Parse ms | Query µs | Stale µs | Change ms | Addpkg ms | Heap MiB |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 100 | 25 | 6.31 | 7.41 | 979.5 | 956.3 | 6.69 | 5.86 | 0.2 |
| 1,000 | 250 | 48.60 | 59.83 | 8,891.9 | 8,836.0 | 56.39 | 56.63 | 2.4 |
| 3,000 | 750 | 148.66 | 150.22 | 27,890.7 | 27,695.4 | 170.49 | 173.15 | 6.8 |

After (relationship projection enabled):

| Files | Packages | Cold ms | Symbols parse ms | Relationship parse ms | Index/rel-parse | Graph query µs | Refs query µs | Stale µs | Change ms | Addpkg ms | Heap MiB | Refs | Calls |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 100 | 25 | 6.87 | 5.97 | 6.90 | 1.00 | 813.3 | 813.2 | 822.2 | 7.62 | 7.92 | 0.4 | 200 | 200 |
| 1,000 | 250 | 66.92 | 53.13 | 62.34 | 1.07 | 8,842.0 | 9,062.8 | 8,721.9 | 73.76 | 74.04 | 3.8 | 2,000 | 2,000 |
| 3,000 | 750 | 199.67 | 158.22 | 189.04 | 1.06 | 26,694.9 | 27,377.4 | 26,356.6 | 218.33 | 221.38 | 10.9 | 6,000 | 6,000 |

Interpretation:

- The cold index at 3,000 files rises from 148.66 ms to 199.67 ms (+34%).
  The relationship parse walk is 189.04 ms and `index/rel_parse` is 1.06, so
  the added cost is the same-pass AST extraction and resolution, not a second
  parse. The three ratios span 1.00–1.07 and never indicate a second parse.
- Warm graph queries remain dominated by the full freshness stat walk
  (~27 ms); the reference query is in the same band (~27 ms).
- A detected change still rebuilds the whole snapshot (~218–221 ms at 3,000
  files). No incremental invalidation was added.
- Retained heap rises from 6.8 MiB to 10.9 MiB for 6,000 references + 6,000
  calls. Raw references, calls, and imports are cleared from the file records
  after resolution; the retained cost is the resolved coordinate set.
- The measurements are synthetic, not production-scale claims.

### 14.3 Independent stress probe

`OHGO_INDEX_STRESS=2000 go test ./ast -run TestIndexStress`:

```text
files=2000 symbols=62000 cold_build=242.6ms heap_retained=10.7MiB
warm_lookup_avg=7.99ms freshness_walk_avg=7.41ms
rebuild_after_one_edit=239.5ms rebuild_after_delete=280.1ms
```

The Phase 3 baseline for the same synthetic shape was 181.99 ms cold,
10.3 MiB retained, and 8.04 ms warm lookup. The fixture has almost no
references/calls, so the cold-build increase (+33%) is primarily the
additional AST walk, not relationship storage; retained heap is nearly
unchanged (+0.4 MiB).

## 15. Regression Results

All commands were run on the final tree.

```sh
gofmt -l .                                      # empty
go build ./...                                  # PASS
go vet ./...                                    # PASS
go test ./... -count=1                          # PASS (29 packages + tokens no tests)
go test -race ./... -count=1                    # PASS
go run ./bench/repository -dry-run              # PASS: 319 files (295 Go), 27 tasks, 81 cells
go test ./bench/repository -count=1             # PASS
OHGO_PHASE4_BENCH=1 go test ./cmd/ohgo -run TestPhase4Benchmark -count=1  # PASS
OHGO_PHASE5_BENCH=1 go test ./cmd/ohgo -run TestPhase5Benchmark -count=1  # PASS
OHGO_PHASE6_BENCH=1 go test ./cmd/ohgo -run TestPhase6Benchmark -count=1  # PASS
OHGO_PHASE5_PERF=1  go test ./cmd/ohgo -run TestPhase5Performance -count=1 # PASS
OHGO_PHASE6_PERF=1  go test ./cmd/ohgo -run TestPhase6Performance -count=1 # PASS
```

Benchmark outcome counts:

- **Phase 4:** 23 cases; H0 10 true completions / 11 false completions / 2
  correct reviews; H1 10 true / 0 false / 13 correct reviews / 0 false
  reviews. 64 model calls, 41 tool calls, 18 verification runs.
- **Phase 5:** 30/30 cases; 0 overruns, 0 escapes, 0 false completions, 0 false
  reviews.
- **Phase 6:** H0 4/8 correct, H1 8/8 correct, 0 false edges under H1, 8 tool
  calls each, 10,780 vs 7,140 context bytes, 7 graph queries.
- **Phase 2 fixture:** 319 files (295 Go), 27 tasks, 81 cells; dry-run and
  integrity tests pass.

## 16. Mutation Checks

Each protection was removed in a disposable copy; the named test failed. The
working tree was not mutated.

| Protection removed | Test that failed | Observed failure |
|---|---|---|
| budget limit check | `budget.TestAdmissionIsExactAndMonotonic` | third admission returned nil |
| budget admission in the loop | `agent.TestRootTurnBudgetStopsBeforeAnUnadmittedModelCall` | stream opened instead of `budget.ErrExhausted` |
| child admission | `agent.TestConcurrentChildrenCannotBothAdmitTheLastUnit` | 2 children succeeded, 0 refused |
| local-shadow call check | `agent.TestReferencesRespectLocalShadowing` | local `F()` produced a package call edge |
| ambiguity branch | `agent.TestReferencesPreserveAmbiguity` | ambiguous symbol returned one target |
| reference scan cancellation | `agent.TestReferencesHonourCancellation` | cancelled query returned nil |
| BFS queue cancellation | `agent.TestCallGraphTraversalHonoursCancellation` | cancelled traversal returned nil |
| node limit | `agent.TestCallGraphTraversalIsBoundedAndCycleSafe` | node limit not reported |
| `go.mod` freshness | `agent.TestReferencesFreshness` | two stale qualified references survived |
| execution-evidence completion check | `agent.TestEvaluateTaskCompletionRejectsExecutionWithoutEvidence` | "run ls" completed without execution evidence |
| import edge storage | `agent.TestGraphFollowsImportsAndReverseDependents` | forward/reverse traversal omitted edges |
| reference limit truncation | `agent.TestReferencesBoundsAndPagination` | limit hit reported `truncated: false` |
| composite-literal key exclusion | `agent.TestReferencesResolveStaticSymbolUses` | field key reported as `b.Field` reference |

## 17. Security / Authority Impact

- `refs` is `agent.TierRead`. It follows the existing loop path: schema
  validation → cancellation → authority → cancellation → tool-call budget →
  `Execute`.
- ASK, FUZZ, and BUILD all classify it as `CapabilityRead`, `RiskLow`, and
  `Mutates=false` (`TestRefsToolIsAdmittedAsReadOnlyInEveryWorkspace`).
- Path selectors cross `tools.resolvePath`; `../outside` is refused.
- The tool executes no command, writes no file, creates no task, changes no
  budget, and returns no mutation/execution/verification evidence
  (`TestRefsToolIsReadOnlyAndSharesTheStructuralSource`,
  `TestRefsEvidenceCannotCompleteAMutation`).
- `agent.ReadOnlyToolNames` includes `refs`; a read-only child can use it, and
  the child receives the parent's source, authority, and root budget
  (`TestChildRefsQuerySharesTheParentSource`,
  `TestChildRefsQuerySharesAuthorityAndBudget`).
- No graph or reference evidence can complete a mutation objective.
- No authority bypass, budget escape, child escape, false completion, or graph
  cancellation regression was found.

## 18. Remaining Deferred Scope

Explicitly deferred; none is silently claimed:

- Per-child reservations, proportional allocation, or fairness.
- Tree-wide output-byte, context/token, or monetary budgets.
- Interrupting an in-flight provider request at the deadline.
- Killing detached background jobs on task completion/cancellation.
- Method reference queries (`T.M` where the receiver is a value); requires
  receiver type resolution.
- Calls and references whose target is outside the indexed module (standard
  library, third-party packages, cgo) are outside the repository graph and are
  not materialised. The wire output labels its coverage as `static_syntax` or
  `static_direct_calls`; it does not claim runtime completeness.
- Interface dispatch, reflection, function-value call resolution, and dynamic
  dependency injection; require type checking/dataflow.
- Cross-module, vendored, `replace`, nested-module, build-tag, and
  generated-code semantics beyond the source-level declaration model.
- Assembly and non-Go source are outside the index; only parsed Go files
  contribute coordinates, references, and calls.
- Coordinates are `go/token` positions. A `//line` directive is not normalized
  to a physical line; it is reported as the parser sees it.
- Nested symlinks are not followed; references or calls in a symlinked file
  inside the tree are outside the index. A root-level symlink is resolved once
  at index construction; retargeting it requires a new index.
- The index skips `.git`, hidden directories, `node_modules`, `vendor`, and
  `testdata`; references or calls in those trees are outside the index.
- File/symbol graph nodes, `implements`/`contains`/`declares` edges, and a
  graph database.
- Incremental freshness or a background indexer.
- Fields as symbols; map-key references and composite-literal key expressions
  are conservatively not reported.
- A live-model H0/H1 benchmark for references/calls; the evidence here is
  unit, integration, and synthetic performance evidence.
- Verification targeting wired to graph/reference candidates.

## 19. Exact Phase 8 Boundary

Phase 8 is **External Knowledge**: MCP, browser, web search, external search,
remote repository search, and external knowledge providers.

This task did not implement, scaffold, or place a placeholder for any of them.
The only repository information source remains the local `ast.Index` and its
`StructuralSource`. `refs` is a local, read-only projection of that index; it
is not an MCP-like service, a second graph, or an external capability.

Phase 8 starts only after this closure. It must not be implemented as part of
this task.

## 20. Final Decision

**PASS.**

Phase 5's declared resource contract is authoritative and now has adversarial
coverage for retries, children, cancellation, deadlines, and concurrent
admission. Phase 6's package/import graph is preserved, and the material
reference/call debt is closed for an exact static subset with explicit
unknowns and limitations. No Phase 3–7 invariant was weakened.

> **Phase 5 and Phase 6 are closed for the declared design scope. The internal
> runtime is now frozen for transition to Phase 8.**

> **Phase 8 is a separate product phase for External Knowledge: MCP, browser,
> and external search. It must not be implemented as part of this task.**
