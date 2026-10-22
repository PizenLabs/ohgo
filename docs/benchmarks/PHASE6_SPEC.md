# Phase 6 Spec — Bounded Repository Graph Intelligence

> **Status:** implementation + deterministic tool-level benchmark; live harness
> gated but no valid live artifact
> **Code boundary before Phase 6:** Phase 1–5 frozen at `30aa541`
> **Production surface:** one read-only `graph` tool sharing the existing
> `ast.Index`
>
> **Run commands:**
>
> ```sh
> go test ./ast ./tools -run 'Test(Graph|PackageGraph)' -count=1 -v
> OHGO_PHASE6_BENCH=1 go test ./cmd/ohgo -run TestPhase6Benchmark -count=1 -v
> OHGO_PHASE6_PERF=1  go test ./cmd/ohgo -run TestPhase6Performance -count=1 -v
> ```
>
> Results: [`results/phase6-graph.json`](results/phase6-graph.json),
> [`results/phase6-performance.txt`](results/phase6-performance.txt).
> The live harness is `OHGO_PHASE6_LIVE=1`; see the closure report for why it
> did not produce valid evidence.

## 1. Hypotheses

**H0 — symbol coordinates are sufficient.** Lexical discovery, `symbols`,
package/file coordinates, and targeted reads answer the benchmark's relationship
questions without retained edges.

**H1 — bounded package/import relationships materially improve repository
work.** A graph derived from the same parse pass improves dependency discovery,
reverse dependency discovery, change-impact candidate selection, verification
targeting, and multi-hop navigation, without a second index, a second parse,
authority escape, budget escape, or stale edges.

The benchmark must be able to falsify H1. A graph that exists but does not
improve a measured task is not a Phase 6 success.

## 2. Invariant

> Coordinates identify where. Graphs identify relationships. Authority controls
> what may execute. Verification proves what happened. Budget controls how much
> may be consumed.

Graph edges are **informational and advisory**. They never grant read, write,
execute, delegation, or completion authority. They may narrow a candidate
verification scope; they never produce verification evidence.

## 3. What is in scope

The smallest relationship that the audit showed is missing and that the Go
parser can prove deterministically:

```text
package A --imports--> package B
```

where a package is a repository directory containing parsed Go files and the
edge is backed by an import declaration in one of A's files.

A query may also traverse the inverse direction:

```text
package B <--imported_by-- package A
```

The inverse is a query direction over the same stored `imports` edges, not a
second stored edge type.

## 4. Node contract

### 4.1 Package node (implemented)

| Property | Meaning |
|---|---|
| `path` | Repository-relative directory, slash-separated. The module root is `.`. |
| `package` | Canonical Go package name, or empty when the directory's files disagree. |
| `packages` | Sorted set of package names declared in the directory, when more than one. |
| `files` | Number of parsed Go files in the directory. |
| `test_files` | Number of parsed `_test.go` files. |
| `ambiguous` | True when the directory has more than one non-test package name, or a test package name not matching `<name>` / `<name>_test`. |
| `external_imports` | Count of imports that resolve outside the module. They are counted, not materialised as nodes. |
| `unresolved` | Module-local-looking imports that do not resolve to an indexed package directory. |

Package identity is the directory, not the package name. Two directories may
declare the same package name; a package-name query that matches both returns
`ambiguous` and does not choose one.

### 4.2 Deferred node types

`file` and `symbol` are **not** graph nodes in Phase 6. Their coordinates remain
owned by `ast.Index` and `symbols`. A graph query can accept a symbol or path as
a *selector* and resolve it to its package node; it does not materialise a
second file or symbol graph.

## 5. Edge contract

### 5.1 `imports` (implemented)

| Property | Contract |
|---|---|
| Source | package directory containing the importing file |
| Destination | package directory that the module-relative import path resolves to |
| Construction | `ast.ImportSpec` path from a file parsed in the same pass as the symbol index; each edge keeps a deterministic file:line witness for its source and/or test declaration |
| Confidence | **deterministic declaration evidence** for source and import path; package-target resolution is deterministic only when the target directory is indexed exactly once |
| Strength | `source` when any non-test file declares the import; `test` when only `_test.go` files do; both flags may be present |
| Freshness | the containing `ast.Index` snapshot; any size/mtime change rebuilds the graph with the index |
| Ambiguity | a target directory with conflicting package names is returned with `target_ambiguous: true`; a module-local import path with no indexed directory is `unresolved`, never guessed |
| Traversal | both directions, bounded by depth, node, edge, and work limits |
| Memory | one aggregated outgoing set per package; external imports are counters, not nodes |
| Invalidation | whole-snapshot rebuild on a detected tree change; no incremental edge cache |
| Authoritative? | **no** — advisory evidence; never permission and never completion |

### 5.2 Edge construction rules

For each indexed file `f` in directory `D`:

1. Read the import path `P` from the parsed `ImportSpec`.
2. If `P` equals the module path, the target is `.`.
3. If `P` has the module path as a slash-delimited prefix, the target is the
   remaining relative directory.
4. If the target directory has at least one parsed Go file, add or merge an
   edge `D -> target`, with `source` / `test` flags from `f`.
5. If the target directory is not indexed, record `P` as unresolved for `D`.
6. Otherwise record `P` as external and increment the counter.

No edge is inferred from a similar name, a nearby file, a textual mention, or
a model assertion.

### 5.3 Out of scope

| Relationship | Decision |
|---|---|
| `file` nodes / `contains` edges | OUT OF SCOPE — the coordinate source already returns file paths. |
| `symbol` nodes / `declares` edges | OUT OF SCOPE — the coordinate source already returns declarations. |
| `references` | OUT OF SCOPE — textual references are not semantic references. |
| `calls` | OUT OF SCOPE — method expressions, function values, interface dispatch, reflection, generics, and build tags make exactness unprovable. |
| `implements` | OUT OF SCOPE — requires type checking and a build context. |
| cross-module / vendored / `replace` resolution | OUT OF SCOPE — treated as external or unresolved, explicitly. |
| build-tag evaluation | OUT OF SCOPE — the index is source-level; edges from files excluded by a build tag are still declared edges and are documented as such. |

## 6. Graph query contract

The wire tool is named `graph`. Its schema is closed.

| Argument | Type | Required | Contract |
|---|---|---|---|
| `package` | string | one selector | Exact package name; must resolve to exactly one directory or return `ambiguous`. |
| `path` | string | one selector | File or directory inside the workspace. A file maps to its directory. |
| `symbol` | string | one selector | Exact symbol or qualified name; resolves to its package directory. Multiple directories return `ambiguous`. |
| `relation` | enum | no | `imports` (default) or `imported_by`. |
| `depth` | integer 1..4 | no | Traversal depth from the focal package. Default 1. |
| `limit` | integer 1..200 | no | Maximum package nodes returned. Default 50. |
| `edges` | integer 1..400 | no | Maximum edges returned. Default `2 * limit`, capped at 400. |

Rules:

- exactly one selector is required; zero or more than one is an error;
- unknown arguments are rejected by the schema and by the tool;
- empty selector strings are errors;
- `depth`, `limit`, and `edges` outside their ranges are errors, never silently
  clamped;
- an unknown relation is an error listing the supported relations;
- a missing `go.mod` module path is an error, because no module-local edge can
  be proven without it;
- the tool never executes a command, writes a file, changes authority, changes
  budget, or changes task state.

### 6.1 Status values

| Status | Meaning |
|---|---|
| `ok` | The focal node was found and the requested traversal completed within every bound. Zero edges is a valid `ok` result. |
| `not_found` | No package, path, or symbol resolved to a node. A partial index is reported with `unparsed`. |
| `ambiguous` | A selector matched more than one package directory. Candidates are listed; no winner is chosen. |
| `truncated` | A depth/node/edge/work bound stopped the traversal. `truncated: true` and the reason are explicit. |
| error | Malformed arguments, cancellation, index/rebuild failure, missing module path, or path containment refusal. No partial result is returned as success. |

Ambiguity and truncation are not interchangeable with `not_found`. A node with
zero outgoing edges is `ok`, not `not_found`.

### 6.2 Bounds

| Bound | Default | Maximum |
|---|---:|---:|
| depth | 1 | 4 |
| nodes | 50 | 200 |
| edges | `2 * limit` | 400 |
| work (edge inspections) | 100,000 | 100,000 |

The work bound is a deterministic traversal guard. Reaching any bound returns
`truncated` with `reason` = `depth`, `node_limit`, `edge_limit`, or
`work_limit`. The focal node is always included when it resolves.

### 6.3 Output shape

The text is compact, ordered, and attributable:

```text
status: ok
module: example.com/app
query: package app
relation: imports
depth: 1
packages_total: 4
packages: 3
packages_returned: 1
edges: 2
truncated: false

nodes:
  - path: .
    package: app
    depth: 0
    files: 2
    external_imports: 1
    imports:
      - path: internal/store
        package: store
        kinds: source
        source_evidence: a/a.go:3
      - path: internal/api
        package: api
        kinds: source,test
        source_evidence: a/a.go:4
        test_evidence: a/a_test.go:3

unresolved:
  - from: .
    import: example.com/app/missing
    reason: no indexed package at that path
```

Every node and edge carries a coordinate. Every edge also carries a deterministic
`file:line` witness for each declaration kind that proves it. Truncation is
never hidden.

## 7. Freshness

- The graph is built in the same `ast.Index` build pass as the symbols. No
  second parse is introduced.
- The existing size+mtime fingerprint and full stat-walk decide freshness.
- A graph query calls the same freshness check. If the check or rebuild fails,
  the tool returns an error; it never serves the previous snapshot.
- A parse failure removes that file's imports from the snapshot and increments
  `unparsed`. The output marks the result partial; absence caused by a parse
  failure is not presented as a proven negative.
- Symlink rules are inherited: the root may be a symlink; nested symlinks are
  not followed.
- The inherited limits (same-size+mtime rewrite, no incremental rebuild) remain
  explicit.

## 8. Ambiguity and unresolved relationships

- A package-name selector matching two directories returns `ambiguous` with all
  candidates; no arbitrary winner.
- A symbol selector matching declarations in two directories returns
  `ambiguous`; declarations within one directory are one package node.
- A directory whose files disagree on package name is a node with
  `ambiguous: true`; its edges are still reported as declarations, but the
  package identity is not claimed to be unique.
- A module-local import path with no indexed target is `unresolved` with the
  source package and import path. It is not silently dropped.
- External imports are counted, not listed as nodes, and are never traversed.

## 9. Cancellation

`ast.Index` gains a context-aware freshness/build path used only by graph
queries. It checks the context between files and before publishing a rebuilt
snapshot. A cancelled build leaves the previous snapshot intact but the query
returns the cancellation error; it does not answer from stale data.

Traversal checks the context at every node and at least every 256 edge
inspections. Cancellation is an error result, never a partial `ok` result.

The existing `symbols` path keeps its frozen Phase 1 cancellation behaviour.

## 10. Authority, budget, verification, delegation

- The tool is `agent.TierRead`. Its path selector goes through the same
  `tools.resolvePath` containment authority as every other path argument.
- The tool is a normal tool call: the loop validates the schema, authorizes it,
  re-checks cancellation, admits one `tool_calls` unit, and then executes it.
- Graph construction happens inside that admitted call. There is no background
  worker, daemon, or unmetered index process.
- A graph result is candidate evidence. It cannot mark a task verified, satisfy
  a verification requirement, or transition completion.
- Children receive the same tool instance from the parent tool set, so they
  share the index, authority, cancellation, deadline, and root budget.

## 11. Non-goals

- full call graph, whole-program semantic graph, cross-language graph;
- a graph database, external graph service, or second repository index;
- incremental or background indexing;
- model-generated edges;
- graph-driven mutation, authority, budget, or completion;
- interface implementation or call resolution;
- build-tag or generated-code awareness beyond the source declaration.

## 12. Benchmark categories

| ID | Category | Question |
|---|---|---|
| A | direct symbol | Find a known declaration. Graph should not be required. |
| B | dependency | What does package X import? |
| C | reverse dependency | Which packages import X? |
| D | change impact | If X changes, which packages are candidate affected areas? |
| E | verification targeting | Which package scopes should be considered for a check? |
| F | multi-hop navigation | Reach related packages through bounded relationships. |
| G | negative relationship | A relationship that does not exist must not be invented. |
| H | ambiguous relationship | A selector or package identity with more than one candidate stays ambiguous. |
| I | large repository | Graph construction/query cost on a synthetic tree. |
| J | stale repository | A changed import must not serve the old edge. |

## 13. Metrics

- discovery: correct target, relevant packages/files, irrelevant candidates,
  missed relationships, ambiguous results, false edges;
- efficiency: turns/model calls (agent-level run), tool calls, graph queries,
  context bytes, wall time;
- execution impact: mutations (must be 0), verification calls, unnecessary
  exploration;
- graph cost: build time, query latency, retained memory, rebuild cost;
- safety: stale edge served, unbounded traversal, budget escape, authority
  bypass, mutation through the graph tool. Target: 0 for every hard violation.

## 14. Verdict rule

The phase can close only if:

- the package/import contract is deterministic and tested;
- freshness, ambiguity, bounds, cancellation, authority, budget, and delegation
  hold;
- Phase 1–5 tests remain green;
- H1 shows a measurable improvement on at least one meaningful task without
  losing correctness on direct-symbol or negative tasks;
- every limitation is explicit.

If H1 does not beat H0, the correct outcome is `HOLD` and the graph surface is
removed or left unreferenced. A graph that merely exists is not a success.
