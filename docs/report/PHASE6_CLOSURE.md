# Phase 6 Closure

> **Post-Phase-7 note:** this report describes the Phase 6 tree. Phase 7 closed
> the reverse-adjacency cancellation gap, the truthful-background-evidence gap,
> and the completion/exit gaps outside the graph. See
> [`PHASE7_CLOSURE.md`](PHASE7_CLOSURE.md).

## 1. Verdict

**PASS WITH LIMITATIONS — PHASE 6 CLOSED**

The bounded package/import graph is derived from the same `ast.Index` parse as
symbol coordinates. It is read-only, bounded, cancellation-aware, and it cannot
grant authority, spend a second budget, mutate the repository, or complete a
task.

H1 beats H0 on the controlled discovery benchmark: **8/8 correct tasks versus
4/8**, with **0 false edges versus 4**, and **33.8% less total tool-output
context**. The improvement is deterministic tool-level evidence, not a live
model run; that is the principal limitation.

The graph does not include references, calls, implementations, or symbol/file
edges. Verification targeting was measured but is not wired into the Phase 4
completion contract. Performance is synthetic.

## 2. Final Code State

The final state is the Phase 6 working tree on top of `30aa541`
(`feat(semantic): intent-aligned completion`). No previous phase file was
reverted.

Production changes:

- `ast/ast.go` — one `GoBackend.Parse` pass now returns the package clause,
  declarations, and imports. `Search` delegates to it.
- `ast/index.go` — retains per-file imports, fingerprints `go.mod`, builds the
  package graph from the same file records, and adds context-aware freshness
  and build paths.
- `ast/graph.go` — package nodes, `imports` edges, `imported_by` traversal,
  bounded BFS, ambiguity and unresolved classification.
- `tools/source.go` — one lazy `StructuralSource` shared by structural tools.
- `tools/symbols.go` — reads the shared source; symbol behaviour is otherwise
  unchanged.
- `tools/graph.go` — the read-only `graph` tool, closed schema, compact output.
- `cmd/ohgo/run.go` — registers `symbols` and `graph` over one source.
- `agent/subagent.go` — adds `symbols` and `graph` to the read-only tool
  allowlist, so a read-only child can use the verified read-tier structural
  tools instead of being refused.
- `README.md` — tool list includes `graph`.

Verification code and artifacts:

- `ast/graph_test.go`
- `tools/graph_test.go`
- `cmd/ohgo/phase6_integration_test.go`
- `cmd/ohgo/phase6_bench_test.go`
- `cmd/ohgo/phase6_perf_test.go`
- `cmd/ohgo/phase6_live_test.go` (gated; see §17)
- `docs/benchmarks/PHASE6_SPEC.md`
- `docs/benchmarks/results/phase6-graph.json`
- `docs/benchmarks/results/phase6-performance.txt`
- `docs/report/PHASE6_AUDIT.md`

Final tree checks:

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./... -count=1` | PASS |
| `go test -race ./... -count=1` | PASS |

## 3. Hypothesis

**H0 — symbol coordinates are sufficient.** Lexical discovery, `symbols`,
package/file coordinates, and targeted reads answer relationship questions
without retained edges.

**H1 — bounded package/import relationships materially improve repository
work.** A graph derived from the existing parse improves dependency discovery,
reverse dependency discovery, change-impact candidates, verification targeting,
and multi-hop navigation without a second index, a second parse, authority
escape, budget escape, or stale edges.

**Result:**

- H0 is sufficient for direct symbol lookup, a scoped direct-import grep,
  multi-hop navigation when the lexical scan is complete, and package-name
  ambiguity. It is **not** sufficient for proving a negative or a reverse
  relationship when a string literal or comment mentions the import path.
- H1 is sufficient for all eight benchmark tasks and produced no false edges.
  It is now a first-class read-only relationship surface over the same
  structural source.

H1 does not replace lexical search: text literals, documentation, JSON tags,
and Makefile targets remain the lexical tools' evidence.

## 4. Existing Repository Intelligence

The audit (`docs/report/PHASE6_AUDIT.md`) confirmed:

- one structural source: `SymbolsTool` → `ast.Index` → `GoBackend`;
- imports were parsed and discarded because `indexedKinds` excluded imports;
- freshness was size+mtime with a full repository rebuild;
- Phase 4 used the same index through `StructuralResolver`;
- Phase 5 metered tool calls at the loop boundary;
- Phase 2 showed direct symbol strength but weak dependency/multi-hop evidence.

No Phase 1–5 defect was found that reopened an earlier phase. The audit
recorded eight discrepancies/drift items; the relevant ones were documentation
revision drift (Phase 2 line targets, Phase 5 commit naming) and the intentional
Phase 6 extension of one structural tool into two surfaces over one index.

The concrete information gap was: **no API returned an importer set, a
dependency set, or an explicit unresolved/external classification.** A caller
had to scan every Go file's import block and could not distinguish an import
declaration from a string literal or comment that mentioned the same path.

## 5. Graph Contract

The graph is a bounded projection of the existing index:

```text
                 ┌── symbol coordinates (unchanged)
ast.Index ───────┤
                 └── package/import relationships (new)
```

- It is built in the same `GoBackend.Parse` pass that produces symbols. The
  performance probe measured index/parse ratios of **0.99–1.00** on synthetic
  repositories, so there is no second parse.
- It is rebuilt with the same snapshot. There is no independent graph store,
  daemon, background scanner, or persistent graph database.
- Every edge is derived from an `ast.ImportSpec`; no edge is inferred from
  names, proximity, textual mentions, or model output.
- Edges are **advisory evidence**. They never grant read, write, execute,
  delegation, authority, budget, or completion.

## 6. Node Types

Implemented:

- **package** — a repository-relative directory with at least one parsed Go
  file. Fields: path, canonical package name, conflicting package names,
  file/test counts, ambiguity, external-import count, module-local import
  count.

Not materialised:

- **file** and **symbol**. Their coordinates remain owned by `ast.Index` and
  `symbols`. A graph query may use a file path or symbol as a selector and
  resolve it to its package node; this does not create a second file or symbol
  graph.

## 7. Edge Types

Implemented:

- **`imports`** — package A → package B, proven by an import declaration in a
  file in A. The edge records whether a non-test file (`source`) and/or a
  `_test.go` file (`test`) declared it. The target is marked
  `target_ambiguous` when B's package identity is conflicting.
- **`imported_by`** — the inverse query direction over the same stored
  `imports` edges. It is not a second stored edge type.

Explicitly classified:

- imports outside the module are counted as `external_imports`, not
  materialised as nodes;
- module-local-looking imports with no indexed target are `unresolved` with
  the source package, import path, and reason; they are never traversed;
- relative or escaping import paths are unresolved, not resolved.

Not implemented (OUT OF SCOPE):

- `references` — textual reference is not semantic reference;
- `calls` — method expressions, function values, interface dispatch,
  reflection, generics, and build tags make exactness unprovable;
- `implements` — requires type checking and a build context;
- `contains` / `declares` — duplicate existing coordinates;
- cross-module, vendored, `replace`, and nested-module resolution;
- build-tag evaluation.

## 8. Freshness

**PASS within the inherited Phase 1 limits.**

- The Go-file fingerprint remains size + mtime. Any changed, added, deleted,
  resized, or re-timestamped file rebuilds the snapshot and graph.
- Graph queries additionally fingerprint `go.mod`. A module-path change
  invalidates every module-local edge even though `symbols` deliberately
  ignores non-Go changes.
- Root symlinks are resolved once; nested symlinks are not followed.
- A parse failure removes that file's imports from the snapshot and increments
  `unparsed`. The graph reports the count; absence is not presented as proof.
- A freshness-walk or rebuild error is returned; the previous snapshot is not
  served as current.
- The same-size + same-mtime rewrite limitation remains (normal editor writes
  update mtime).

Tests cover initial build, modification, same-size rewrite, creation,
deletion, import change, module change, parse failure, symlinked root, and a
stale-edge probe. See `ast/graph_test.go` and the `stale_probe` field in
`phase6-graph.json`.

## 9. Ambiguity

**PASS.**

- A package-name selector matching more than one directory returns
  `ambiguous` with every candidate and chooses none.
- A symbol selector whose declarations span more than one package directory
  returns `ambiguous`; multiple declarations in one directory resolve to one
  package node.
- A directory whose files disagree on package name is a node with
  `ambiguous: true` and all package names; its edges carry
  `target_ambiguous: true`.
- An unresolved module-local import is reported, not dropped or guessed.
- A missing symbol or package is `not_found`, distinct from `ambiguous` and
  from a node with zero edges.

## 10. Traversal Bounds

**PASS.**

| Bound | Default | Maximum |
|---|---:|---:|
| depth | 1 | 4 |
| nodes | 50 | 200 |
| edges | `2 * limit` | 400 |
| work (edge inspections) | 100,000 | 100,000 |

- Reaching node, edge, or work capacity returns `truncated: true` with
  `truncation_reason` = `node_limit`, `edge_limit`, or `work_limit`.
- A node at the requested depth with relationships beyond that depth also
  returns `truncated: true` with reason `depth`; depth is a traversal scope,
  not a claim of completeness.
- The focal node is always included when it resolves.
- The work bound is deterministic and also applies to the on-demand reverse
  adjacency scan.
- Output is deduplicated, path-ordered, and bounded by the node/edge caps.

## 11. Graph Query Contract

The wire tool is `graph`, `TierRead`, with a closed schema.

| Argument | Contract |
|---|---|
| `package` | exact package name; one selector |
| `path` | file or directory inside the workspace; one selector |
| `symbol` | exact symbol/qualified name; one selector |
| `relation` | `imports` (default) or `imported_by` |
| `depth` | integer 1..4 |
| `limit` | integer 1..200 |
| `edges` | integer 1..400 |

- Exactly one selector is required. Zero or multiple selectors are errors.
- Empty selectors, unknown arguments, unknown relations, and out-of-range
  bounds are errors, never silent clamps.
- A missing `go.mod` module path is an error; the tool will not serve an empty
  edge set as authoritative.
- Paths cross `tools.resolvePath`, the same containment authority as `read`.
- The tool executes no command, writes no file, creates no task, changes no
  budget or authority, and returns no completion evidence.
- Output carries `status`, module, query, relation, depth, package/edge totals,
  returned counts, truncation, candidates, nodes, edges, and unresolved
  imports.

## 12. Phase 3 Authority Interaction

**PASS.**

- The tool is `agent.TierRead`. `securityGate` classifies it as
  `CapabilityRead`, `RiskLow`, and `Mutates=false` under ASK, FUZZ, and BUILD.
- It follows the existing loop path: schema validation → cancellation →
  `Authorize` → cancellation → tool-call budget admission → `Execute`.
- It is added to `agent.ReadOnlyToolNames` (together with `symbols`), so a
  read-only child is admitted rather than refused by the fail-closed allowlist.
- Path containment and symlink-escape rejection are tested. A graph result
  cannot be used to read or write outside the workspace.
- No graph operation mutates the repository. A before/after tree snapshot is
  unchanged.

## 13. Phase 4 Verification Interaction

**PASS for non-interference; candidate targeting is measured but not wired.**

- `Result.Mutates`, `Executes`, and `Verifies` are false for every graph query.
- A run that only queried the graph remains `NEEDS_REVIEW` for a mutation
  objective; `CompletionEvidence` reports no mutation and no verification.
- The benchmark's category E shows the graph identifies candidate verification
  scopes (`cmd/app`, `internal/api`, `internal/legacy`, `internal/store`,
  `internal/worker`) with no lexical false positive. These remain **candidate
  evidence**, not proof of behavioral impact.
- The Phase 4 evaluator, target checks, verification scope rules, and
  completion transitions are unchanged. Graph evidence is not consumed by the
  verifier.
- Production verification targeting was not rewritten to use graph edges.
  That is a deferred integration, not a hidden behavior.

## 14. Phase 5 Budget Interaction

**PASS.**

- A graph query is a normal tool call and consumes one `tool_calls` unit after
  authority and before `Execute`.
- A grant with one tool call admits exactly one graph query; a second query in
  the same batch is refused and the loop returns `budget.ErrExhausted` without
  a second execution.
- Construction happens inside the admitted call. There is no background
  indexer, daemon, or unmetered graph process.
- No new budget dimension was invented. Graph result bytes are capped by the
  traversal/node/edge bounds; tree-wide output-byte accounting remains the
  existing Phase 5 limitation.
- A child shares the root grant; the delegated-child test observes one child
  unit and one tool-call unit in the root snapshot.

## 15. Delegation

**PASS.**

- `childToolSet` passes the parent's `symbols` and `graph` instances to a
  child; both read the same `StructuralSource`.
- A delegated read-only child executed a graph query under ASK authority and
  charged the shared root budget.
- No child can create a second index or graph service; no child API can reset
  the source, authority, deadline, or budget.
- Multiple concurrent callers on the shared source return identical results
  (8-goroutine test), and the index's own lock serialises snapshots.

## 16. Cancellation

**PASS.**

- `Graph` checks the context before the query, during the freshness stat walk,
  between file parses in a rebuild, and at every BFS node.
- A cancelled build does not publish partial records; a fresh query rebuilds
  from the authoritative tree. This is tested directly with a context that
  cancels after parsing has begun.
- A cancelled query returns `context.Canceled` as an error result; it never
  returns a partial `ok` answer.
- Cancellation during traversal is deterministic in the test: a counting
  context cancels after the freshness walk and during queue expansion.
- The existing `symbols` path keeps its frozen Phase 1 cancellation behavior.

## 17. Discovery Benchmark

**Method.** `OHGO_PHASE6_BENCH=1 go test ./cmd/ohgo -run TestPhase6Benchmark`
runs a deterministic, warm, tool-level capability probe on a synthetic
single-module fixture:

- 53 files, 52 Go files;
- 40 filler packages to make a full import scan materially large;
- a known dedicated graph (`cmd/app`, `internal/api`, `internal/store`,
  `internal/model`, `internal/worker`, `internal/legacy`, `internal/isolated`);
- test imports, an unresolved module-local import, and two lexical decoys
  (a string literal and a comment mentioning `internal/model`);
- 8 tasks across categories A–H.

H0 uses `read`, `glob`, `grep`, `ls`, `symbols`; H1 uses the same tools plus
`graph`. Both use the minimal correct query plan for the task, warmed indexes,
and the same repository/task ordering. The static benchmark is the reproducible
capability measurement; it makes no model-call, token, or turn claim.

The live harness `TestPhase6LiveBenchmark` is present and gated by
`OHGO_PHASE6_LIVE=1`. It was exercised, but no valid live H0/H1 artifact was
produced: the available local qwen models emitted tool-call JSON as text or
ended turns without calls; OpenRouter returned 402; the available OpenAI key
was an OpenRouter key; Gemini's OpenAI-compatible endpoint rejected replayed
tool calls for a missing `thought_signature`. The live result is therefore
**not evidence** and is not shipped as an artifact. This is the main limitation
of the phase.

## 18. H0 vs H1 Results

Source: `docs/benchmarks/results/phase6-graph.json`.

| Metric | H0 — existing | H1 — graph |
|---|---:|---:|
| Tasks correct | 4 / 8 | **8 / 8** |
| False positives | 4 | **0** |
| False edges | 4 | **0** |
| Missing packages | 0 | 0 |
| Missing edges | 0 | 0 |
| Tool calls | 8 | 8 |
| Graph queries | 0 | 7 |
| Total context bytes | 10,780 | **7,140** |
| Median context bytes | 384.5 | 948.0 |
| Direct-symbol context (A) | 136 | 136 |
| Direct-import context (B) | 195 | 885 |
| Reverse context (C) | 309 | 1,011 |
| Change-impact context (D) | 3,194 | **1,691** |
| Verification context (E) | 3,194 | **1,691** |
| Multi-hop context (F) | 3,194 | **1,222** |
| Ambiguity context (H) | 460 | **214** |
| Tool-only duration | 21 ms | 8 ms |
| Stale probe | — | **PASS** |

Per-task:

| Task | H0 | H1 |
|---|---|---|
| A direct symbol | correct | correct |
| B direct imports | correct | correct |
| C reverse imports | **false positive** (`internal/isolated` string literal) | correct |
| D change impact | **false positive** (`internal/isolated`) | correct |
| E verification targets | **false positive** (`internal/isolated`) | correct |
| F multi-hop | correct | correct |
| G negative relationship | **false positive** (string literal) | correct |
| H ambiguous package | correct | correct |

Interpretation:

- The graph's advantage is **precision on reverse/negative relationships** and
  **context reduction on transitive work**, not raw call count. Both plans use
  one call per task.
- H1's output is larger for small direct queries (B, C) because it carries
  module metadata and node detail; the median H1 context is therefore higher
  even though the total is lower. The phase does not claim every query is
  smaller.
- H0's false positives are not a grep bug: a lexical scan genuinely cannot
  distinguish an import declaration from a string literal or comment. The graph
  can because it is derived from `ImportSpec` nodes.
- The graph was 47.1% smaller on change impact/verification and 61.7% smaller
  on multi-hop, while being correct where H0 was not on C/D/E/G.

## 19. Safety Metrics

| Hard safety metric | Result |
|---|---:|
| stale edges served as current | 0 |
| unbounded traversals | 0 |
| budget escapes | 0 |
| authority bypasses | 0 |
| mutations through the graph tool | 0 |
| false edges from H1 | 0 |
| unknown arguments accepted | 0 |
| path-containment escapes | 0 |
| cancellation reported as success | 0 |
| graph evidence causing completion | 0 |

The adversarial suite covers missing package/symbol, ambiguous selectors,
package-identity conflict, cycles, deep chains, wide fan-out, isolated
packages, created/deleted/changed packages, same-size rewrites, parse
failures, generated and build-tagged source, cross-package symbols, depth/node/
edge limits, cancellation before and during traversal, cancellation during
rebuild, concurrent queries, malformed arguments, unknown relations,
post-mutation queries, exhausted budget, a delegated child, and multiple
concurrent callers.

Not applicable by construction:

- interface implementation and call relationships (not implemented);
- build-context evaluation (source-level graph, documented);
- graph-driven mutation or authority (the tool is TierRead and has no such
  path).

## 20. Efficiency Metrics

Static benchmark (tool-level, warm indexes, no model):

- H1: 8 tool calls, 7 graph queries, 7,140 context bytes, 8 ms tool time.
- H0: 8 tool calls, 0 graph queries, 10,780 context bytes, 21 ms tool time.
- H1 removes four false positives with the same number of calls.
- H1 reduces total context by 33.8%; the reduction is concentrated in the
  transitive tasks (D/E −47.1%, F −61.7%).
- Direct symbol lookup is at parity (136 bytes); direct scoped import lookup is
  larger under H1 (885 vs 195 bytes), which is an honest cost of structured
  metadata and declaration evidence.

No model-call, turn, token, or wall-clock claim is made from the static
benchmark. The live harness is available but did not produce valid evidence
(§17).

## 21. Performance

Source: `docs/benchmarks/results/phase6-performance.txt`.
macOS / Apple M1 Pro / go1.27.2, synthetic single-module repositories:

| Files | Packages | Cold index | Parse-only | Index/parse | Warm graph query | Stale walk | One import change | Add package | Retained heap | 8-way concurrent avg |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 100 | 25 | 6.92 ms | 6.97 ms | 0.99 | 1,128 µs | 910 µs | 6.00 ms | 6.11 ms | 0.2 MiB | 6.34 ms |
| 1,000 | 250 | 48.97 ms | 49.45 ms | 0.99 | 8.29 ms | 8.09 ms | 55.71 ms | 56.35 ms | 2.4 MiB | 65.32 ms |
| 3,000 | 750 | 145.70 ms | 146.26 ms | 1.00 | 25.84 ms | 25.87 ms | 168.00 ms | 168.66 ms | 6.8 MiB | 186.17 ms |

Interpretation:

- The index/parse ratio (0.99–1.00) proves imports and the graph are derived
  from the same parse; there is no duplicate repository parse.
- The graph build adds no measurable parse multiplier at these scales. Memory
  is bounded by the index and the unique module-local edge set.
- Warm query latency is dominated by the existing **full freshness stat walk**,
  not graph traversal: `query_us` and `stale_us` are nearly identical at every
  size. No incremental freshness was added. This is the same Phase 1 limitation
  at graph scale.
- A single import change or package creation rebuilds the whole snapshot
  (~168 ms at 3,000 files). Incremental invalidation remains deferred.
- Eight concurrent callers serialise on the index and each pays a freshness
  walk; the 186.2 ms average at 3,000 files reflects that lock and walk, not a
  production concurrency claim.
- The measurements are synthetic and must not be read as production-scale
  latency.

## 22. Proven

- One structural source; `symbols` and `graph` share one `ast.Index` in
  production and in tests.
- One parse pass produces coordinates, package clauses, and imports; the
  measured index/parse ratio is 0.99–1.00.
- Package/import edges are deterministic `ImportSpec` evidence, with explicit
  source/test flags, external counts, unresolved paths, and no inference from
  text.
- Package-name and symbol selectors preserve ambiguity; package-identity
  conflicts are marked on nodes and edges.
- Freshness covers Go-file changes and `go.mod` module-path changes; stale edges
  are not served.
- All traversal bounds are explicit and reported; depth, node, edge, and work
  truncation are distinguishable from `not_found` and `ambiguous`.
- Cancellation works before the query, during freshness, during rebuild, and
  during traversal; a cancelled build does not publish partial state.
- The graph is TierRead, passes ASK/FUZZ/BUILD read admission, cannot mutate,
  and cannot contribute mutation or verification evidence.
- Graph queries consume the existing tool-call budget exactly once; an
  exhausted grant refuses the second query.
- A delegated read-only child uses the same source, authority, and root budget.
- H1 passes 8/8 benchmark tasks with 0 false edges; H0 passes 4/8 with 4 false
  positives. H1 reduces total context by 33.8% and transitive context by
  47.1–61.7%.
- All Phase 1–5 frozen suites and benchmarks pass, including the full race
  suite.

## 23. Implemented but Limited

- **Tool-level, not model-level evidence.** The H0/H1 result is a deterministic
  capability probe. The live harness exists but no valid live artifact was
  produced; see §17 and §24.
- **Package/import only.** No references, calls, implementations, file nodes,
  symbol nodes, cross-module resolution, vendoring, `replace`, or nested
  modules.
- **Source-level, not build-context-aware.** Generated files and files excluded
  by build tags are indexed and their imports are reported. This is explicit
  and tested, not a semantic claim.
- **No incremental freshness.** Every query performs a full stat walk; every
  detected change rebuilds the whole snapshot. At 3,000 files a warm query is
  ~26 ms and a rebuild ~168 ms.
- **Same-size + same-mtime rewrite** is still undetected, inherited from Phase
  1.
- **Query context is not monotonic.** Direct small queries are larger under H1;
  the reduction is in transitive tasks and total context. Edge evidence adds a
  small, bounded cost per edge.
- **Verification targeting is not wired.** The graph can identify candidate
  scopes, but Phase 4's verifier and completion evaluator do not consume graph
  output. Wiring it would require a separate change and evidence.
- **Read-only allowlist was incomplete.** `symbols` and `graph` were not in
  `agent.ReadOnlyToolNames`; both were added. This is a Phase 3 integration
  fix, not a new authority.
- **No per-query byte budget.** Result size is bounded structurally; tree-wide
  output-byte accounting remains the Phase 5 limitation.

## 24. Not Proven

- That a real model chooses the graph when it is available and improves its
  answer quality or turn count. The live harness did not produce valid data.
- That graph-derived verification scopes improve completion correctness in
  production; only the candidate-scope information was measured.
- Exact call/reference/implementation relationships.
- Build-tag, generated-code, vendored, replaced, or nested-module semantics.
- Production-scale latency, memory, or contention.
- Incremental invalidation or a background indexing lifecycle.
- That H1 is better for every query; direct small queries cost more context.

## 25. Deferred

- Symbol references and call edges, under an exactness contract.
- Interface implementation edges and type checking.
- File/symbol graph nodes and `contains`/`declares` edges.
- Wiring graph candidates into Phase 4 verification targeting.
- Incremental freshness or a bounded background indexer, if a later hypothesis
  demonstrates the need.
- Provider-level live benchmarking with a reliable tool-using model.
- Cross-module, vendored, `replace`, and nested-module resolution.
- Build-tag/build-context evaluation.
- Tree-wide output-byte or context-byte budgeting.
- Fairness or reservations for many concurrent graph queries.

## 26. Final Decision

**FROZEN — PASS WITH LIMITATIONS.**

The Phase 6 contract is satisfied for the package/import relationship it
claims:

- one structural source, one parse, no second index;
- deterministic, bounded, read-only relationships;
- explicit freshness, ambiguity, unresolved, truncation, and cancellation;
- authority, budget, delegation, and completion non-interference;
- a measurable H1 improvement in precision and transitive context on a
  controlled benchmark;
- Phase 1–5 regression suites and benchmarks green, including race.

The phase does not claim a live-model improvement, call-graph capability, or
production-scale performance. Those are recorded above as limitations and
deferred work, not hidden assumptions.

The next phase must start from a separate hypothesis and benchmark. Do not add
semantic edges to this frozen tree without evidence that the package/import
layer is insufficient.
