# Structure Engineering Audit — Architecture

This document describes the Structure implementation as it exists after this
audit. Topology and ownership remain canonical in
`docs/architecture/OHGO_ARCHITECTURE_MAP.md`; this file is the execution-flow
view used by the audit.

## 1. Composition

```
cmd/ohgo/build.go
  └─ tools.NewStructuralSourceWithOptions(root, AnalysisCacheDir)
       └─ internal/execution/tools.StructuralSource
            ├─ ignoreScope            (.gitignore + nested-repository boundaries)
            └─ ast.NewIndexWithOptions(root, Scope, Adaptive)
                 └─ internal/intelligence/ast.Index

tools.NewSymbolsToolWithSource(source)
tools.NewGraphTool(source)
tools.NewRefsTool(source)
tools.NewStructureTool(source)
```

All four tools share **one** lazily built `ast.Index`. A second handle would be
a second snapshot with independent freshness; production code never constructs
one per query.

## 2. Observation path

### 2.1 Inventory

`Index.scanLockedCtx` walks the tree with `filepath.WalkDir`:

- `.git`, `node_modules`, `vendor`, `testdata`, and hidden directories (except
  `.github`) are never indexed.
- `ignoreScope.Ignored` applies the repository `.gitignore`, including nested
  repository boundaries; the rule that excluded a path is retained.
- Each file gets a size/mtime signature, a language from the single extension
  registry, a `FileKind`, and a test classification.
- Directories the scope excluded are reported as `excluded_roots`; a source
  file excluded by a rule is counted as `excluded_source_files`.

The resulting snapshot is metadata-first: an overview does not parse source.

### 2.2 Parse

`Index.EnsureParsed` / `EnsureParsedScope` parse pending supported files:

```
rel + signature
  → adapterSet.AdapterFor(rel)       (extension → one adapter)
  → adapter.ParseFileContext         (Go native; pinned tree-sitter otherwise)
  → FileFacts{ symbols, imports, tests, entrypoints,
               raw references, raw calls, raw relations, issues }
  → fileRecord
```

A file over `maxStructuralFileBytes` is a recorded failure, not a crash. A
grammar panic is recovered as a file failure. Tree-sitter syntax errors become
`ParseIssue` entries and `FileState=PARTIAL`; the file is not silently treated
as empty.

## 3. Projections

The index derives four projections from the same records; none is a second
repository index.

| Projection | Builder | Contents |
|---|---|---|
| Go package graph | `buildPackageGraph` | package nodes, module-local import edges, unresolved imports, external counts |
| Go symbol graph | `buildSymbolGraph` | resolved Go references, direct calls, dispatch unknowns |
| Language-neutral graph | `buildLanguageGraph` | non-Go imports, calls, references, explicit relations |
| Inventory | `Inventory` | files, packages, configs, entrypoints, language coverage |

`resolveRecordImports` runs before `buildLanguageGraph` and records the
repository-relative target of every non-Go import (`RESOLVED`, `UNRESOLVED`,
`EXCLUDED`, `UNSUPPORTED`).

### 3.1 Identity

- `file:<rel>`
- `dir:<rel>` / `workspace:<rel>` / `project:<module>`
- `sym:<rel>:<line>:<qualified>`
- `test:<rel>:<line>:<qualified>`
- `entry:<rel>:<line>:<qualified>`
- `config:<rel>`
- `pkg:<dir>#<name>`

Symbol IDs are snapshot-scoped: a line change changes the ID. The snapshot ID,
generation, and source fingerprint are carried by every result so a consumer
never mistakes an old coordinate for a current one.

### 3.2 Edges

`CONTAINS`, `DEFINES`, `IMPORTS`, `EXPORTS`, `REFERENCES`, `CALLS`,
`EXTENDS`, `IMPLEMENTS`, `OVERRIDES`, `TESTS`, `ENTRYPOINT`, `DEPENDS_ON`.

An edge is materialised only with a source witness and `RESOLVED` state.
`UNRESOLVED`, `AMBIGUOUS`, and `UNSUPPORTED` sites are counted in
`UnknownSummary`; they are never emitted as edges.

## 4. Adaptive analysis path

```
structure view=analysis
  → structural.Request{Intent, Focus, Level, MaxFiles}
  → Planner: choose the cheapest sufficient strategy portfolio
  → Strategies:
      filesystem-hierarchy, manifest-topology, dependency-graph,
      scc-cycles, boundary-analysis, architecture-inference,
      modularity-label-propagation, symbol-neighborhood,
      documentation-drift, module-survey, degree/betweenness, sampling
  → Inventory + Dependencies + Impact + hierarchy Snapshot
  → Evidence{State, Confidence, Reason, Refs}
  → Analysis{Plan, Snapshot, Architecture, Focus, Drift, Coverage, Issues}
  → result cache (optional, content-addressed)
```

`Level` is a ceiling, not a forced resolution. The default capability level is
`relationship` so `symbol-neighborhood` can run for locate/flow/impact/
isolation. A caller may pass a shallower level explicitly and the plan reports
the strategy it skipped and why.

Evidence grades are ordered: `UNKNOWN < INFERRED < PARTIALLY_OBSERVED <
OBSERVED < VERIFIED`. An aggregate is never upgraded above its weakest required
witness. `Evidence.Complete()` is true only for `OBSERVED`/`VERIFIED`.

## 5. Freshness and incremental behavior

- The index keeps the last published snapshot and a per-file signature.
- Every query entry point calls `ensureFresh`/`ensureFreshGraph`, which scans
  the tree and rebuilds only when a signature, module file, scope fingerprint,
  or ignore rule changed.
- Deleted files are removed; stale records are not served as current.
- A parse failure keeps its record with an explicit error and a failure count.
- A scope/rule change changes `ScopeFingerprint`, therefore the snapshot ID and
  the analysis-cache key.
- Incremental and clean rebuilds converge on the same logical file, symbol,
  reference, call, and edge sets. The generation counter differs; that is not a
  logical difference.

The analysis cache stores only an `Analysis` keyed by schema version, analyzer
version, adapter fingerprint, root, snapshot ID, and normalized request. The
graph itself is not persisted; it is reconstructed from source on process
start.

## 6. Downstream consumption

- `symbols` returns declaration coordinates.
- `refs` returns references/callers/callees, with `UnknownSummary`.
- `graph` returns the bounded Go package graph; it fails explicitly when no Go
  module exists instead of returning an empty graph.
- `structure` renders inventory, relationships, and analysis with four
  independent completeness axes: scope, inventory, structural, result.
- `control/cognition` reads `complete`, `truncated`, `partial`, snapshot, and
  coordinate fields as evidence; it does not re-resolve symbols.
- Completion/claim grounding can therefore distinguish an observed absence from
  an unparsed or unresolved one.

## 7. Ownership boundaries

- `ast` owns observation and identity; it has no authority and no model text.
- `structural` owns interpretation and evidence; it imports only `ast` and the
  standard library.
- `execution/tools` owns the argument contract and bounded rendering.
- `control` owns authority, evidence admission, and completion.
- `ignore` owns `.gitignore` semantics; `ast` accepts a `Scope` interface rather
  than implementing a second matcher.
