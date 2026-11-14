# Adaptive Structural Intelligence

> **Scope:** the analysis layer behind the `structure` capability: the typed
> multi-resolution model, evidence rules, the strategy portfolio, the planner,
> bounded graph analysis, the result cache, and the failure contract.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](OHGO_ARCHITECTURE_MAP.md).
> Language and adapter coverage: [`STRUCTURAL_LANGUAGES.md`](STRUCTURAL_LANGUAGES.md).
> Runtime behaviour: [`ARCHITECTURE.md`](ARCHITECTURE.md).
> Unified providers, evidence, and planner:
> [`UNIFIED_INTELLIGENCE.md`](UNIFIED_INTELLIGENCE.md).

Evidence states are shared across structural, semantic, runtime, and retrieval
observations: `structural.EvidenceState` is an alias of `evidence.State`, so the
vocabulary cannot drift between providers.

`structure` is a capability, not an engine. The engine is
`internal/intelligence/structural`; it consumes the one `ast.Index` snapshot and
returns a typed analysis. Nothing here mutates repository source, decides
authority, or writes model-facing prose.

## 1. Position and boundaries

```
user / model intent
        │
        ▼
execution/tools.structure          capability, arguments, bounded rendering
        │  Request{Intent, Focus, Level}
        ▼
intelligence/structural            planner → strategies → evidence → snapshot
        │  Inventory, Dependencies, Impact
        ▼
intelligence/ast                   one incremental snapshot (files, packages,
        │                          symbols, imports, references, calls)
        ▼
filesystem (read-only)
```

Ownership:

- `intelligence/ast` owns observation: what files exist, what they declare, and
  which import/reference/call facts a syntax adapter proved.
- `intelligence/structural` owns interpretation: resolution levels, source
  classes, graph algorithms, strategy selection, confidence, caching, and the
  explicit-gap vocabulary.
- `execution/tools` owns presentation and the argument contract. It never
  decides what to analyse.
- `control/*` owns authority. The engine is read-only and has no second gate.

The engine imports only `intelligence/ast` and the standard library. It cannot
reach the agent, the control plane, a provider, or a UI; the architecture test
enforces that direction.

## 2. Data model

The model is one `Snapshot` per analysis, at one published resolution.

```go
type Snapshot struct {
    SchemaVersion string              // structural/v1
    Version       string              // analyzer semantics
    ProjectID     string
    Source        ast.SnapshotInfo    // generation, snapshot id, freshness
    Level         Level               // deepest materialised resolution
    Nodes         []Node              // ordered by level, then id
    Coverage      Coverage
    Issues        []Issue
}

type Node struct {
    ID         string
    Kind       ast.NodeKind           // project, workspace, module, directory, file
    Level      Level
    Name, Path string
    Parent     string                 // containment, not duplicated as an edge
    Class      SourceClass             // files only
    Languages  []ast.Language
    Files, TestFiles, Symbols int
    Packages   []string               // multiple identities in one directory
    Classes    map[SourceClass]int
    Classification Evidence           // evidence for Class
    Evidence   Evidence               // evidence for existence/counts
}
```

Resolutions (`Level`): `project` → `workspace` → `module` → `package`
(directory) → `file` → `symbol` → `relationship`. The numbering starts at 1 so
the zero value means "unset" and defaults to `package`.

A `Snapshot` publishes the deepest level it **materialised**, not the level the
caller asked for. Hierarchy nodes cover levels 0–4; symbol and relationship
resolution (levels 5–6) is answered by the bounded focal projection
(`FocusReport`) through the existing `refs`/`impact` resolver, which already
owns declaration coordinates and call edges. There are no invented symbol nodes
in the hierarchy, and `Snapshot.Level` never claims a resolution that was not
built.

Containment is `Parent`; dependency relationships are edges in the graph
projection, not duplicated node fields. Edge kinds reuse `ast.EdgeKind`;
`BELONGS_TO`, `CONFIGURED_BY`, `GENERATED_FROM`, `EXPOSES`, `CONSUMES`, and
`CROSSES` extend that one vocabulary rather than creating a second one.

A source class is path-derived except for the adapter's test flag, so its
evidence is `INFERRED` by construction. `ClassProduction` means "no vendor,
generated, test, documentation, fixture, example, or tooling rule matched", not
"proved authored".

## 3. Evidence semantics

Every fact carries a state and a confidence derived from the mechanism, never
from model intuition.

| Mechanism | State | Confidence |
|---|---|---|
| `adapter_fact` (parsed source) | `OBSERVED` | 0.95 |
| `manifest_fact` | `OBSERVED` | 0.90 |
| `import_edge` (aggregated dependency) | `OBSERVED` | 0.85 |
| `inventory` (filesystem scan) | `OBSERVED` | 0.80 |
| `path_convention` | `INFERRED` | 0.45 |
| no observation possible | `UNKNOWN` | 0.00 |

Rules enforced by `CombineEvidence` / `AggregateContainment` and covered by
tests:

- A conjunction is its weakest link; `UNKNOWN` anywhere makes the claim
  `UNKNOWN`.
- `VERIFIED` is only produced by an explicit proof constructor. Combining
  observations can never manufacture it.
- A container with an unparsed or inferred member is `PARTIALLY_OBSERVED`, not
  `OBSERVED`. A truncated row set demotes every container's count evidence.
- Multi-witness agreement is capped below the strongest single mechanism: two
  observations do not become one proof.

Coverage is independent of evidence: `Coverage.State` describes what was
observed (files, parse, edges, truncation), and `Coverage.Complete()` is the
only place a consumer may read "repository-wide claim supported".

## 4. Strategy portfolio

A strategy is a named, versioned, costed analysis step with an explicit
applicability guard. The portfolio is a table of these entries rather than an
exported interface: the registry in `plan.go` is the extension point, and an
interface with no implementer outside the package would be an API promise
without a consumer. Each entry is independently benchmarkable, and the planner
can select any subset.

| Strategy | Level | Needs graph | Cost driver | Mandatory intents |
|---|---|:---:|---|---|
| `filesystem-hierarchy` | workspace | — | files | all |
| `manifest-topology` | module | — | constant | project, architecture |
| `dependency-graph` | package | yes | source files, nodes | architecture, locate, flow, impact, isolation |
| `scc-cycles` | module | yes | nodes | architecture, isolation |
| `boundary-analysis` | module | yes | nodes | architecture |
| `architecture-inference` | module | yes | constant | architecture |
| `degree-centrality` | package | yes | nodes | — |
| `betweenness-centrality` | package | yes | nodes² | — (bounded to 400 nodes / 4000 edges) |
| `modularity-label-propagation` | package | yes | edges | architecture (≤800 estimated nodes) |
| `representative-sampling` | package | yes | nodes | — |
| `symbol-neighborhood` | file | yes | files | locate, flow, impact, isolation |

Costs are relative units, not durations; the band (`low`/`medium`/`high`) is the
only user-facing form. Every selected and skipped strategy is recorded in the
`Plan` with a reason, so the decision is auditable after the fact.

## 5. Planner

Input: `Request{Intent, Focus, Level, MaxFiles, MaxSteps, MinConfidence}`.

1. Read a metadata-only inventory (no adapter runs). This also yields the
   snapshot identity used for caching.
2. Select the intent's mandatory strategies, honouring the level budget and
   each strategy's applicability guard.
3. Add optional strategies while the estimated confidence is below the intent
   threshold and the step budget allows, ordered by relevance ÷ cost.
4. Drop strategies that need the dependency graph when the graph provider was
   itself excluded, and record the reason.
5. Fetch the dependency projection only if a selected strategy needs it, then
   re-project the inventory so the hierarchy describes the parsed state.
6. Build the hierarchy, connect the graphs, run strategies in plan order.
7. Stop when: coverage confidence meets the threshold, the step budget is
   exhausted, the portfolio is exhausted, or a strategy degraded the analysis.
   The chosen reason is `Plan.StopReason`.

Default thresholds are policy, not measurement: project `0.55`, architecture
`0.70`, selector intents `0.65`. A caller may raise the threshold to buy more
analysis; the planner never silently upgrades the request.

`intent` also determines what must not be parsed: a project orientation keeps
the graph out entirely, so describing a 50,000-file repository does not parse
it. "Metadata-first" is about what the analysis triggers, not a guarantee that
nothing was parsed earlier: the index is shared, so a previous targeted query
may already have published declarations in the same process. The result reports
the snapshot it actually observed (`parsed_files`, `pending_files`,
`coverage.state`), never the intent's assumption.

## 6. Modularity label propagation

> **Correction.** Earlier revisions called this strategy "MB25". That was a
> documentation error: it is not Okapi BM25, and BM25 is a retrieval ranking
> function, not a graph-partitioning algorithm. The strategy is now registered
> as `modularity-label-propagation`, and Okapi BM25 lives in
> `internal/intelligence/retrieval` where it belongs.

This is an explicit local definition, not a claim about a published algorithm:

> Bounded modularity-gain label propagation over the dependency graph: nodes
> are visited in ID order, the move maximising modularity gain is chosen, ties
> break to the lower community label, at most **25 sweeps** run, and a sweep
> that moves nothing stops early. Communities are renumbered by their smallest
> member.

It is one strategy behind the same `strategy` abstraction as SCC condensation,
path-prefix grouping, and flat partitioning, with its own version
(`ModularityStrategyVersion`) in the plan and the cache key. It is mandatory
only for architecture analysis and only when the estimated graph is ≤800 nodes;
above that the plan records the bound instead of running it. It is not used for
project orientation, where the graph is not built at all.

The benchmark harness (`go run ./bench/structural`) measures partition quality
on the same graph:

| Partition (medium synthetic repo, 72 graph nodes) | Communities | Modularity |
|---|---:|---:|
| flat (no grouping) | 72 | −0.016 |
| path prefix | 8 | **+0.764** |
| SCC condensation | 72 | −0.016 |
| modularity label propagation | 40 | +0.477 |

The honest conclusion is that modularity label propagation is **not** automatically superior. On a
repository whose directory layout matches its true modules, path-prefix
grouping scores higher. Its value is the case the path hierarchy cannot see:
graph communities that cross directory boundaries. The planner therefore keeps
it bounded and optional-by-intent instead of universal, and the comparison is
reproducible rather than asserted.

## 7. Graph intelligence

`ProjectGraphs` maps the dependency projection onto materialised nodes, producing
a directory graph and a module graph from the same edges.

- **SCC / cycles**: iterative Tarjan, so a deep chain cannot exhaust the stack.
  Cyclic components are exact; representative cycles are one bounded witness per
  component, not an enumeration.
- **Condensation**: SCCs collapse to a DAG, which is the only graph a layering
  claim may use.
- **Layering**: longest-path depth from sources. It refuses cyclic graphs.
- Bridge/articulation analysis is deliberately absent: the portfolio asks
  "which region depends on which" and "what cycles exist", and an undirected
  bridge claim on a directed dependency graph would need a separate
  interpretation contract. SCC condensation is the supported answer to
  "critical coupling"; adding bridges later means adding a strategy, not
  changing this rule.
- **Centrality**: degree centrality is always available; exact Brandes
  betweenness is bounded to 400 nodes / 4,000 edges and recorded as skipped
  above that.
- **Boundaries**: derived only from edges — module-to-module crossings, observed
  source-class crossings, and external dependency counts. An unresolved import
  is classified by shape: a relative/C-shaped spec (`./x`, `"x.h"`,
  `crate::x`) is a structural gap, while a package-shaped spec (`react`,
  `com.google.guava`) is an external dependency. The heuristic is disclosed in
  this document because no manifest-based symbol resolution exists to decide it
  exactly. A boundary with zero
  crossings is not emitted. Directory names are collected as `name_hints` and
  never used as evidence.
- **Architecture inference**: `layered` (acyclic, ≥3 layers), `modular`
  (acyclic but shallow), `independent modules` (no edges), `mixed` (cycles), or
  `not-established`. Fewer than three modules is always `not-established`.
  Incomplete graph evidence caps confidence at 0.5 and demotes the state to
  `PARTIALLY_OBSERVED`.

## 8. Cache and incremental behaviour

Two caches exist, with different jobs.

1. **Snapshot freshness** is owned by `ast.Index`. It stats the tree, parses
   added/changed files, reuses retained syntax for unchanged files, and
   publishes a new snapshot identity. This is process-local and incremental.
2. **Analysis reuse** is owned by `structural`. Entries are keyed by
   `SHA-256(schema, analyzer version, adapter fingerprint, root, snapshot id,
   normalized request)`. A changed tree, schema, analyzer, adapter set, Go
   runtime, workspace root, intent, focus, or level is a different key. The
   adapter fingerprint covers the compiled adapter names plus the Go version;
   grammar revisions are pinned by `go.mod`.

Envelope validation on load repeats the comparison. A corrupt, truncated, or
foreign entry is reported as `cache_invalid` and recomputed; it is never
partially trusted. Writes are atomic (temp file + rename) and the directory is
pruned to the newest 64 entries. A cache read or write failure is an `Issue`,
not an analysis failure. A cache hit still performs one inventory scan: serving
a cached answer without checking that the snapshot identity is current would be
stale evidence.

`<state>/cache/structural` is wired by `cmd/ohgo`; without a resolvable state
directory the cache is disabled and analysis still runs.

## 9. Failure semantics

| Code | Meaning |
|---|---|
| `unsupported_language` | inventoried source with no adapter in this build |
| `parse_failure` | a supported source file failed to parse |
| `unparsed_source` | declarations were deliberately not parsed for this analysis |
| `scope_incomplete` | part of the repository was not inspected |
| `truncated_analysis` | the hierarchy row set was bounded |
| `empty_inventory` | no indexed files under the root |
| `dependency_graph_failed` | the dependency projection could not be built |
| `inventory_refresh_failed` | re-projection after parsing failed |
| `strategy_failed_<name>` | one strategy failed; the rest of the analysis survives |
| `cache_invalid` / `cache_read_failed` / `cache_write_failed` | cache is not evidence |
| `focus_not_found` | not emitted: an unmatched selector is `FocusReport.Evidence = UNKNOWN` |

Unknown stays unknown. A missing dependency is never emitted as an edge, an
unresolved module-local import is listed rather than dropped, and an incomplete
graph cannot support an `OBSERVED` architecture claim.

## 10. Token cost and model-facing output

The model never receives the node list or the raw graph.

- The tool renders a bounded plan, at most 20 hierarchy rows, 8 boundaries, 4
  communities, 6 samples, and 8 issues; details are typed for evidence
  consumers.
- Expansion is explicit: `view=expand` returns only the direct children of one
  node.
- Measured on the synthetic medium repository (384 files): the full node JSON is
  ~29.9 KB while the module-level summary JSON is ~2.9 KB, a ~10× reduction
  before the tool's row bounds apply.
- The cache prevents a repeated intent from recomputing or re-emitting the same
  structural work.

## 11. Benchmarks

```sh
go test ./internal/intelligence/structural/ -run '^$' -bench . -benchmem
go run ./bench/structural
```

The harness generates deterministic synthetic Go repositories, measures cold
project analysis, cold architecture analysis, a warm cache hit, and an
incremental run after one file changes; then scores four partitions of the same
dependency graph. Example run (Apple M1 Pro, Go 1.27.4, 2026-11-14):

| Scale | Cold project | Cold architecture | Warm (hit) | Incremental |
|---|---:|---:|---:|---:|
| 12 files | 2.6 ms | 3.6 ms | 0.35 ms | 0.33 ms |
| 80 files | 1.9 ms | 7.8 ms | 1.26 ms | 1.17 ms |
| 384 files | 5.1 ms | 26.8 ms | 4.33 ms | 3.57 ms |

Interpretation limits: these are synthetic Go repositories on one machine. The
numbers demonstrate the shape of the cost model (architecture ≫ project, warm ≪
cold, incremental ≪ cold), not a promise about any real repository. The
hierarchy microbenchmark shows 15 ms / 17 MB for 10,000 materialised file nodes;
above `MaxFiles` (4,000) the model clamps to directory resolution instead.

## 12. Deliberate limits and remaining risks

- Syntax-level resolution only: no type checker, no method dispatch, no macro
  expansion. Dynamic, ambiguous, and unsupported sites are disclosed, not
  guessed.
- The language-neutral dependency graph is directory-aggregated. A cross-file
  import inside one directory is not an edge and does not contribute to
  coupling. External-vs-local classification of an unresolved import is
  language-aware but shape-based, not manifest-resolved.
- Path-derived source classes can misclassify an unusual layout; the class is
  `INFERRED` and named with its rule for that reason.
- Architecture inference recognises graph shape, not intent. A layered DAG is
  evidence of a layered dependency structure, not proof the team designed one.
- The analysis cache trusts the snapshot identity, which is a stat-based
  fingerprint. A rewritten file that preserves size and mtime is outside the
  existing index contract; `ForceRefresh` exists for that case.
- Snapshot freshness is process-local. A new process rebuilds the in-process
  index; only the analysis result is persisted.
- Clustering quality is measured only on synthetic layouts. Real-repository
  tuning of the modularity sweep bound and the 800-node threshold is future work, and
  the benchmark exists so that work can be evidence-based.

## 13. Migration notes

- `structure` gained two views (`analysis`, `expand`) and three arguments
  (`intent`, `level`, `node`). Existing views, arguments, and details are
  unchanged; the claim-grounding keys (`graph_state`, `scope_complete`,
  `inventory_complete`, `structural_complete`, `test_files`, `truncated`,
  `excluded_roots`, `nested_projects`) remain the shared vocabulary.
- `StructuralSource` gained `NewStructuralSourceWithOptions` and
  `AnalysisCacheDir`; `NewStructuralSource` keeps its signature and behaviour.
- `ast.Index` gained `Inventory` and `Dependencies`. Both are whole-snapshot,
  in-process projections for analysis; the existing paginated queries remain the
  model-facing contract and are unchanged.
- Nothing in the authority model changed. The engine is read-only; the tool is
  still `TierRead` and listed in `ReadOnlyToolNames`.
- To add a strategy: implement the `strategy` entry (name, version, level,
  graph/parse requirements, cost function, relevance table, applicability,
  `run`), register it in `portfolio`, and add its intent to the mandatory table
  only if the intent cannot be answered without it. Add a benchmark row and a
  test that the planner does not select it when its evidence is unavailable.
