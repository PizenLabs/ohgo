# Phase 6 Audit — Repository Graph Intelligence

- **Date:** 2026-10-22
- **Tree audited:** `30aa541` (clean working tree at start)
- **Frozen boundary:** Phase 1–5 reports and code
- **Scope:** read the prior reports, read the actual implementation, compare the
  two, and identify whether any relationship information already exists.

## 1. Method

The audit read the Phase 1, 2, 3, 4, and 5 closure documents and then the
production source:

- `ast/ast.go`, `ast/index.go`
- `tools/symbols.go`, `tools/base.go`, `tools/grep.go`, `tools/glob.go`
- `agent/loop.go`, `agent/tool.go`, `agent/targets.go`
- `budget/budget.go`
- `cmd/ohgo/run.go`, `cmd/ohgo/taskcontinuity.go`
- `bench/repository/harness.go`, `bench/repository/tasks.go`
- the tests that pin the Phase 1 and Phase 5 contracts

A claim was accepted only when the source or a test demonstrated it.

## 2. Reported architecture

The reports describe one structural source:

```text
model
  ↓
tools.SymbolsTool
  ↓
ast.Index
  ↓
ast.GoBackend
  ↓
Go parser
  ↓
repository-relative coordinates
  path · package · symbol · kind · start_line · end_line
```

The reported boundaries are:

- Phase 1: one structural coordinate store, exact-name lookup, explicit
  ambiguity/truncation/parse-failure states.
- Phase 2: lexical, structural, and hybrid discovery were benchmarked; the
  hybrid was the best practical shape. Dependency and multi-hop questions
  remained weak.
- Phase 3: one authority gate, one security gate, OS-enforced read-only FUZZ.
- Phase 4: intent contract, deterministic target evidence, verification
  alignment, one completion evaluator.
- Phase 5: one root-owned budget shared by the whole execution tree.

## 3. Actual source

### 3.1 One structural source (confirmed)

`cmd/ohgo/run.go:buildTools` registers `tools.NewSymbolsTool(opts.root)`.
`SymbolsTool` lazily creates one `*ast.Index` and caches it behind a mutex
(`tools/symbols.go:91-103`). `ast.Index` walks the repository once, calls
`ast.GoBackend.Search` per file, drops doc and signature text, and stores
coordinates (`ast/index.go:95-157`). `tools.SymbolsTool.LocateSymbol` and
`LocatePackage` project that same index into `agent.StructuralResolver` for
Phase 4 completion (`tools/symbols.go:105-134`).

There is no second index, no persistent symbol store, and no background
indexer. This part of the reports matches the source.

### 3.2 Import data is parsed and discarded (confirmed)

`GoBackend.Search` has an `ast.ImportSpec` branch
(`ast/ast.go:220-224`), but `ast.Index` passes `indexedKinds`
(`ast/index.go:30-37`), which deliberately excludes `KindImport`. Therefore:

- `ast.Index` retains no imports;
- no API exposes package relationships;
- `symbols` can answer "where is this declaration" but not "what imports it"
  or "what does it depend on".

Phase 1 explicitly listed dependency graphs as out of scope
(`docs/report/PHASE1_CLOSURE.md` §10). The source confirms that this remained
true through Phase 5.

### 3.3 Freshness (confirmed, with inherited limits)

`Index.buildLocked` records size and mtime per `.go` file and compares the tree
on every query (`ast/index.go:176-274`). A changed, added, deleted, resized, or
re-timestamped file triggers a full rebuild. Root-level symlinks are resolved;
nested symlinks are not followed. Parse failures are isolated per file and
counted as `unparsed`.

Inherited limits from Phase 1 remain:

- a same-size rewrite that also preserves mtime is not detected;
- rebuild is a full reparse, not incremental;
- the build itself has no context cancellation; cancellation is observed before
  and after a query.

Phase 6 must not weaken these. A graph must inherit the same fingerprint and
the same failure behavior.

### 3.4 Index cost (Phase 1/3 measurements)

The Phase 1 report measured a 76.57 ms median cold build on a 295-file tree.
The Phase 3 stress probe measured a 2,000-file / 62,000-symbol tree at
181.99 ms cold, 10.3 MiB retained, and 8.04 ms warm lookup. These are the
baseline costs a graph projection must not multiply.

### 3.5 Tool admission (confirmed)

Every tool call crosses the same path in `agent/loop.go`:

```text
schema validation → BeforeToolCall → cancellation → Authorize
→ cancellation → budget.Admit(tool_calls) → Execute
```

`securityGate` derives capability, scope, and risk from the tool tier and its
arguments. `symbols` is `TierRead`. A graph tool declared `TierRead` inherits
this path without a new authority seam.

### 3.6 Budget (confirmed)

`budget.Budget` owns limits, usage, and one mutex. Tool calls are charged once
before `Execute`. There is no byte or context dimension. A graph query that is
a tool call is therefore metered by the existing `tool_calls` dimension; graph
construction inside that call is not a second execution path.

### 3.7 Delegation (confirmed)

`childToolSet` gives a child the parent's tool set (plus `task` below the
depth cap). The spawner shares the root budget holder, the live authority gate,
the deadline, and the turn cap. A graph tool in the parent tool set is
therefore visible to children with the same contracts.

### 3.8 Verification (confirmed)

`agent.CaptureTargetsContext` resolves path, symbol, and package targets through
the `SymbolsTool` index. `CheckTargets` compares content hashes. Verification is
a command-scoped check recorded by `tools.VerificationForIn`. There is no
import/dependency input to verification targeting; package impact is derived
only from packages the run actually changed.

## 4. Discrepancies and drift

These are recorded, not silently corrected.

| # | Reported | Actual | Classification |
|---|---|---|---|
| D1 | Phase 1: "exactly one structural tool" and a test pins it | Still true at the audit point. Phase 6 will add a relationship surface. | Intentional Phase 6 extension; must preserve one index and one symbol surface. |
| D2 | Phase 1: index retains "coordinates only" | True: imports are not retained because they are excluded. Phase 6 will retain import declarations as relationship data. | Contract extension; not source text, not a second index. |
| D3 | Phase 2 tasks are stable benchmark ground truth | The task table is pinned to `53bc07b` line ranges. The current tree has different line numbers (`buildTools` is at 464, not 382). The harness defaults to `-rev 53bc07b`, so integrity tests do not validate the current tree. | Documentation/revision drift, not a Phase 1 code regression. Phase 6 must not reuse stale line targets. |
| D4 | Phase 5 closure names a final code commit; the current HEAD is later | The Phase 5 budget code and tests are present at `30aa541`; HEAD also contains later semantic/report commits. | Revision drift, not a behavioural discrepancy. |
| D5 | `tools.SymbolsTool` is described as the single structural-navigation tool | The comment and the Phase 1 test enforce one surface. Phase 6 adds a second relationship surface, so the test is now `TestBuildToolsExposesOneStructuralSource` and pins one shared index rather than one wire name. | Intentional Phase 6 extension; one-source ownership is preserved. |
| D6 | Phase 1: cancellation before and after the build, not mid-parse | True. `buildLocked` has no context checks. | Graph queries must add between-file cancellation for their own build path, or document the remaining limit. |
| D7 | README tool list | The list at `README.md:87` omits any relationship tool. | Documentation update required if graph is added. |
| D8 | Phase 2 found dependency/cross-file tasks weak | Confirmed by the reported E2 failure and by the absence of a relationship API. | This is the concrete information gap Phase 6 must test, not assume. |

No Phase 1–5 defect was found that reopens an earlier phase. The only
behavioural gap relevant to Phase 6 is the absence of any retained relationship
data.

## 5. Baseline probes (before implementation)

### P1 — import data is not exposed

Source probe: `indexedKinds` excludes `KindImport`; `fileRecord` stores only
`symbols` and `err`. A lookup of an import path through `Index.Lookup` returns
no match; a package listing contains declarations, not dependencies.

### P2 — reverse dependency requires a lexical scan

A three-package fixture was created:

```text
a/a.go  imports example.com/probe/b
b/b.go  imports example.com/probe/c
c/c.go  declares C
```

`grep -rn "example.com/probe/c"` finds `b/b.go`. To find every consumer of `c`
transitively, a caller must scan every Go file's import block and repeat. No
existing API returns the importer set, the transitive set, or an explicit
unresolved/external classification. This is the gap the Phase 6 benchmark will
measure.

### P3 — structural discovery does not create edges

`Index.Lookup("C")` returns a coordinate; it cannot return that `A` reaches
`C` through `B`. Phase 2's E2 result (definition found, caller missed by one
track) is consistent with this limitation.

### P4 — index cost is the baseline to protect

Any graph construction must reuse the existing parse pass. Calling
`parser.ParseFile` a second time would roughly double the cold cost and would
create a second source of truth. The Phase 6 probe will compare the existing
index build against the index-plus-graph build on the same fixture.

## 6. Decision

The audit supports the Phase 6 hypothesis only as a question, not a result:

- There is exactly one structural source today.
- It retains no relationships.
- Existing tools can answer relationship questions only by lexical scanning and
  repeated inspection.
- The smallest candidate relationship is the package import edge, which the Go
  parser already exposes in the same file parse.

Phase 6 will therefore define a package/import graph derived from the existing
`ast.Index` parse, expose it read-only through one bounded tool, and benchmark
whether it improves discovery. Symbol references, call edges, interface
implementations, and file/symbol materialisation are not justified by this
audit and remain deferred.
