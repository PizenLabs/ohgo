# OHGO Project Structural Runtime — Phase 1 Implementation Plan

> **Status:** Phase 1 deliverable. This document is the architecture decision and
> implementation boundary for Phase 2.
>
> **Phase 2 status:** implemented. The runtime lives in the extended
> `internal/intelligence/ast` snapshot, the shared `tools.StructuralSource`,
> the `structure` tool, and the extended structural evidence path. The measured
> baseline is in `docs/benchmarks/results/phase8-structural.md`; the current
> behavioural contract is in `docs/architecture/ARCHITECTURE.md`.
>
> **Stop condition:** no production code or repository source is changed in
> Phase 1. Phase 2 begins only after this plan is reviewed.
>
> **Canonical sources:** `OHGO_ARCHITECTURE_MAP.md` owns topology,
> `OHGO_ARCHITECTURE_RULES.md` owns permanent constraints, and source is the
> final authority where documentation and implementation disagree.

---

## 0. Executive decision

OHGO already has the foundation of the requested runtime. `internal/intelligence/ast`
owns one in-process Go index that produces symbol coordinates, package/import
edges, resolved references, and statically provable direct calls from a single
parse pass. `internal/execution/tools.StructuralSource` owns the one lazy index
handle shared by `symbols`, `graph`, and `refs`. The completion verifier reaches
the same index through `SymbolsTool`.

The correct Phase 2 is therefore **not** a second AST index, a second parser, or
a new graph database. It is an extension of the existing index into a project
structural snapshot:

```text
existing ast.Index
  + file/directory/config/entrypoint/test facts
  + canonical node/edge query projections
  + incremental snapshot updates
  + explicit freshness and generation
  + bounded query surfaces
  + evidence and tool integration
```

### Decisions at a glance

| Decision | Choice |
|---|---|
| Single source of truth | Extend `internal/intelligence/ast`; do not create a second index |
| Canonical graph storage | Query projection over existing index records, not a duplicate graph store |
| Persistence | In-memory for MVP; `<project>/.ohgo/graph.db` stays reserved and unimplemented |
| Incremental parsing | Parse only added/changed Go files; never re-parse unchanged files for a normal update |
| Incremental resolution | Re-resolve relationship edges from retained raw syntax without re-parsing; optimize with reverse indexes only if benchmarks require it |
| Repository scoping | Consume `intelligence/ignore` through an injected scope seam; do not implement a second `.gitignore` matcher |
| Freshness | Explicit `CURRENT` / `UPDATING` / `STALE` / `INVALID` state plus generation and completeness |
| Evidence | Extend `cognition.EvidenceState`; no parallel evidence model |
| Authority | New query surfaces are read-only; they never grant mutation or execution authority |
| External semantic graph | GRY remains an optional complementary provider; the native runtime is the deterministic baseline |
| Git | `vcs` remains the changed-file/branch owner; the runtime uses filesystem freshness and may consume Git hints, never own Git state |

### Architectural contradictions surfaced

1. **Documentation drift.** `STATE_AND_PERSISTENCE.md` still describes `ast` as a
   stateless per-file searcher with no graph. The source now has a package graph,
   references, and direct calls. Phase 2 must update that document in the same
   change that makes the index a structural snapshot.
2. **Dependency-free leaf vs repository scoping.** `intelligence/ast` is declared
   dependency-free, while repository scoping is owned by `intelligence/ignore`.
   The plan resolves this with an injected `Scope`/`Exclude` seam rather than
   importing `ignore` into `ast` or writing a second matcher.
3. **Ownership naming.** The package is called `ast`, but the target responsibility
   is broader: project structure. The plan keeps the existing package for the MVP
   to avoid a second index. If a second language adapter or non-Go project
   discovery makes the package genuinely language-neutral, extract
   `internal/intelligence/structural` in a dedicated migration with `ast` as the
   Go adapter. Do not split the graph now.
4. **Incremental correctness.** A changed declaration can affect references in
   arbitrary files, so a fully correct update cannot always be file-local.
   "Incremental" in this plan means **incremental parsing** plus bounded
   re-resolution, not a claim that every update is O(1).
5. **Parser vs type information.** `go/parser` cannot prove interface
   implementations. `IMPLEMENTS` is therefore deferred and must be reported as
   `UNKNOWN`/unsupported until a type-aware phase exists.

### Phase 1K deliverable map

| Required deliverable | Section |
|---|---|
| 1. Existing architecture map | §1 |
| 2. Existing reusable components | §2 |
| 3. Overlapping logic | §3 |
| 4. Components to extend | §3 |
| 5. Components to replace | §3 (none wholesale) |
| 6. New components required | §3, §17 |
| 7. Canonical graph model | §5 |
| 8. Language adapter boundary | §6 |
| 9. Incremental update design | §7, §8 |
| 10. Query API | §9 |
| 11. Persistence strategy | §10 |
| 12. Integration points | §11 |
| 13. Test strategy | §12 |
| 14. Benchmark strategy | §13 |
| 15. Migration strategy | §14 |
| 16. Risks | §15 |
| 17. Explicit non-goals | §16 |
| 18. Implementation order | §17, §18 |

---

## 1. Existing architecture map (source-confirmed)

### 1.1 Data flow today

```text
user request
    ↓
workspace routing / intent contract
    ↓
agent loop (internal/agent)
    ↓
tool proposal
    ↓
security + workspace admission
    ↓
execution/tools
    ├── read/glob/grep/ls          lexical discovery
    ├── symbols/graph/refs         structural discovery
    └── mcp_* / search / browser   external capabilities
    ↓
agent.ToolOutcome
    ↓
cognition.EvidenceState            observation/evidence projection
    ↓
task.Controller                    durable task evidence at checkpoints
    ↓
completion evaluator               verdict; never a graph claim
```

Structural discovery does not sit above or beside authority. It is a read-only
capability inside the existing gate.

### 1.2 Relevant components

| Component | Source-confirmed responsibility | Current consumers |
|---|---|---|
| `internal/intelligence/ast` | One Go parse pass per file; symbols, package imports, resolved references, direct calls; stat-based freshness; bounds; cancellation | `execution/tools`, completion verifier, tests/benchmarks |
| `ast.Index` | Rooted, in-memory, rebuildable snapshot; `Symbols`, `Lookup`, `File`, `Package`, `Graph`, `References`, `ParseFailures`, `Stale` | Structural tools |
| `ast.GoBackend` | `go/parser` declaration extraction plus relationship collection in the same parse | `Index` |
| `ast.Registry` / `Searcher` | Path-to-backend registry; current built-in is Go only | `GoBackend` callers |
| `internal/intelligence/ignore` | `.gitignore` matching with nested rules, negation, anchoring, `**` | `glob`, `grep`, mention discovery, recovery |
| `internal/execution/tools.StructuralSource` | Lazily builds and caches one `*ast.Index` per tool set | `symbols`, `graph`, `refs`; subagents via shared tool set |
| `tools.SymbolsTool` | Exact symbol/file/directory/package coordinates; also implements `agent.StructuralResolver` | Agent, completion verifier |
| `tools.GraphTool` | Bounded package import/imported-by traversal with file:line witnesses | Agent |
| `tools.RefsTool` | References, callers, callees; explicit unknown/unsupported states | Agent |
| `internal/agent.StructuralResolver` | Narrow definition/package lookup seam for target snapshots and completion | `SymbolsTool` via `cmd/ohgo` wiring |
| `internal/control/cognition.ProbeFacts` | Cheap marker-file language probe for shape/projection | Request shape, tool filtering |
| `cognition.EvidenceState` | Observation, retention, provenance, completeness, conflicts, failure ledger | Projection, task checkpoint |
| `internal/continuity/task` | Durable task facts, hypotheses, evidence references, role contexts | TUI, continuity, completion proof |
| `internal/control/workspace` | ASK/FUZZ/BUILD authority and read/search/inspect policy | Security gate and agent admission |
| `internal/project` / `internal/startup` | Project root/identity, discovery, initialization | CLI/TUI startup |
| `internal/vcs` | Git status, changed files, branch/HEAD, diffs, hashes, commits | Commit workflow, recovery, TUI |
| `internal/gryhook` | Opt-in GRY lifecycle hook adapter; read-only external evidence | Headless `ohgo run` only |
| `internal/execution/mcp` | External capability protocol; discovery/classification/transport | `mcp_*` tools |
| `internal/state` | Machine-level `~/.ohgo` path owner | Catalog, sessions, credentials, history |

### 1.3 What the existing index already answers

| Existing query | API |
|---|---|
| Where is symbol X defined? | `Index.Lookup` |
| What symbols are in this file? | `Index.File` |
| What symbols belong to this package? | `Index.Package` |
| What are all symbols? | `Index.Symbols` |
| What imports this package / what does it import? | `Index.Graph` with `imports` / `imported_by` |
| Where is symbol X referenced? | `Index.References` with `references` |
| Who calls X / what does X call? | `Index.References` with `callers` / `callees` |

Each result carries path/line/column coordinates, deterministic ordering, parse
failure counts, ambiguity, truncation, and static-unknown disclosures. This is
already a provenance-aware structural runtime for Go symbols and relationships.

### 1.4 What is missing relative to the target runtime

| Missing responsibility | Current state | Required Phase 2 work |
|---|---|---|
| Files and directories as facts | Only `.go` files are indexed; directories exist only as package aggregates | Add file/directory snapshot records and bounded queries |
| Modules and configs | Only the module path in `go.mod` is read | Add module/config facts from a small allowlist (`go.mod`, `go.work`, `Makefile`, `Dockerfile`, relevant CI config) |
| Tests as graph facts | `_test.go` is a flag on references/calls; no test nodes or test queries | Add test declarations and static test associations |
| Entrypoints | Not represented | Add Go `main` function/package entrypoints |
| Incremental update | Any change triggers a full snapshot rebuild | Parse only changed/added files; remove deleted; update derived graphs |
| Explicit freshness state | `Stale()` bool and parse-failure count | Add state, generation, completeness, and update diagnostics |
| Persistence/recovery | No persistence; cold start always parses | Keep in-memory for MVP; define recovery and optional future `graph.db` |
| Project inventory queries | No files/packages/entrypoints/tests query surface | Add bounded query API and one inventory tool |
| Structural evidence for graph/refs | Only `symbols` is classified; `graph`/`refs` fall through to generic diagnostic evidence | Add evidence mapping for all structural tools |
| Impact composition | `refs` + `graph` exist separately | Add a bounded impact composition with explicit unknowns |
| Git-driven invalidation | Stat walk only | Treat Git as an optional hint; do not own changed-file truth |
| Multi-language adapter | Go only behind a narrow `Searcher` interface | Formalize the adapter boundary; do not add languages in MVP |

### 1.5 Root-scope finding

The tool set is rooted at the effective working directory (`opts.root`), while
`project.Root` resolves the Git/project root. The existing structural tools
therefore index the working directory, not necessarily the whole checkout.

**Decision for MVP:** preserve the working-directory root for containment and
tool consistency. Record the nearest module/project root as metadata when it can
be discovered above the working directory, but do not silently widen the index
or the model's filesystem authority. Local imports that resolve outside the
indexed root remain explicit `unresolved` facts. Running OHGO at the project
root gives the complete graph. A future explicit "index root" setting can change
this without changing workspace authority. The same scope limit applies to
`.gitignore` files above the working directory: they are not loaded by the
existing tools, and the structural runtime must not silently invent a wider
scope.

### 1.6 Current mechanism map (Phase 1C)

| Mechanism | Owner | Inputs | Outputs | Consumers | Limitation |
|---|---|---|---|---|---|
| Project root and identity | `project`, `startup` | working directory, Git discovery, `.ohgo/project.yml` | project root, stable id, name, worker | CLI/TUI startup, task store | Tools still root at the working directory, not the project root |
| Filesystem discovery | `execution/tools` (`glob`, `grep`, `ls`, `read`) | root, pattern, path | paths, file content, text matches | Agent | Lexical/content-only; no structural facts |
| Ignore scoping | `intelligence/ignore` | `.gitignore` files | matcher | glob, grep, mention, recovery | Not currently used by `ast`; index uses blanket exclusions |
| Go AST and symbols | `intelligence/ast` | root, `.go` files, `go.mod` | symbols, packages, imports, references, calls | `symbols`/`graph`/`refs`, completion resolver | Go only; full rebuild; no files/configs/entrypoints/tests as first-class facts |
| Git state | `vcs` | repository | status, diff, changed files, branch, HEAD, hashes | commit, recovery, TUI | Separate from structural freshness; not consumed by `ast` |
| External capabilities | `execution/mcp`, `external/search`, `execution/tools` | configured servers/providers | capabilities, results, external evidence envelope | Agent | External/trusted-by-provenance; not local structural facts |
| GRY hooks | `gryhook` | environment, GRY CLI | bounded lifecycle evidence | Opt-in headless `ohgo run` | Disabled by default; not a runtime dependency |
| Evidence | `control/cognition` | tool outcomes | evidence items, retention, provenance, completeness | Projection, task checkpoint | Only `symbols` is classified structurally today; graph/refs are generic diagnostics |
| Task context | `continuity/task` | evidence references, controller writes | facts, hypotheses, evidence, role context | TUI, continuity, completion proof | Durable knowledge; not a structural index |
| Path provenance | `agent.ClassifyPath` | repository-relative path | source/documentation/manifest/configuration/generated | Evidence provenance, answer claims | Lives in the kernel; `intelligence` cannot import it |

The last row is load-bearing: the structural runtime may identify a file as
`go_source`, `go_test`, `config`, or `other` for graph purposes, but **evidence
provenance** must continue through `agent.ClassifyPath` in `control/cognition`.
There must not be a second docs/manifest/generated classifier.

---

## 2. Existing reusable components

| Reusable component | Reuse |
|---|---|
| `ast.GoBackend.Parse` | The only Go parser; extend the returned facts rather than adding another parser |
| `ast.Index` file records/snapshot | The only repository snapshot; add file metadata and incremental updates here |
| `ast.packageGraph` | Existing package identity/import aggregation; extend for incremental per-directory updates |
| `ast.symbolGraph` | Existing reference/call resolution; extend for incremental re-resolution |
| Existing bounds (`GraphMax*`, `RefMax*`, graph/ref work limits) | Reuse the vocabulary and hard ceilings |
| `StructuralSource` | The one lazy handle; keep it as the ownership point |
| Existing tool output contracts (`status`, `truncated`, `parse_failures`, coordinates) | Extend additively; do not invent a second result vocabulary |
| `agent.StructuralResolver` | Keep completion verification on the same snapshot |
| `ignore.Matcher` | Inject into the index scope; do not copy matching logic |
| `cognition.EvidenceState` | Add structural item classification; do not create a graph evidence model |
| `agent.ClassifyPath` | Reuse for evidence provenance; do not duplicate docs/manifest/generated rules in `ast` |
| `task.SharedContext` | Structural observations become task evidence references, not a parallel durable store |
| `workspace` authority + `securityGate` | No new authority path; structural tools remain read-tier |
| `vcs` | Changed files/branch/HEAD remain Git facts; optional update hints only |
| `gryhook` | Keep GRY as an optional evidence provider, not a runtime dependency |
| Existing test/benchmark fixtures | Reuse `writeTree`, phase6 integration patterns, and repository benchmark tracks |

---

## 3. Overlapping logic and ownership decisions (Phase 1D)

| Existing component | Responsibility | Current consumers | Potential overlap | Decision | Reason |
|---|---|---|---|---|---|
| `ast.Index` | Go symbol/package/relationship index | Structural tools, completion | The new runtime | **Extend** | It is already the single index; a new graph would duplicate parsing and freshness |
| `ignore.Matcher` | Repository scoping | Glob, grep, mention, recovery | File membership | **Reuse through a seam** | Two matchers would answer "is this file in the repository?" differently |
| `tools.StructuralSource` | One lazy shared index | Symbols, graph, refs | Query ownership | **Extend** | One handle preserves one snapshot for coordinate and relationship queries |
| `agent.StructuralResolver` | Completion target lookup | Target snapshots, completion | Definitions/packages | **Reuse** | It already projects the shared index; no second resolver |
| `cognition.ProbeFacts` | Cheap marker probe | Shape/projection | Project language facts | **Leave separate** | It is a projection hint, not an index; starting an index at startup would regress startup |
| `cognition.EvidenceState` | Observation/evidence model | Projection, task checkpoint | Structural facts | **Extend** | Provenance and retention already exist; a parallel model would violate the trust boundary |
| `agent.ClassifyPath` | Path provenance (source/docs/manifest/config/generated) | Evidence, answer claims | File kind | **Reuse at evidence layer** | Two path classifiers would disagree about what an observation proves; `intelligence` cannot import the kernel, so classification stays in `cognition` |
| `task.SharedContext` | Durable facts/evidence/roles | Continuity | Structural evidence | **Reuse** | Structural results are evidence, not a second durable store |
| `vcs` | Git status/diff/branch/HEAD | Commit, recovery, TUI | Changed files | **Leave separate** | Git is not the structural source of truth; the runtime must work without Git |
| `glob` / `grep` / `ls` / `read` | Lexical/filesystem discovery | Agent | File discovery | **Leave separate** | Non-Go files and exact text remain lexical problems; structure must not replace them |
| `gryhook` / GRY MCP | Optional rich semantic graph and hooks | Opt-in headless runs | Whole graph | **Leave separate/complementary** | Native runtime is deterministic and always available; GRY is heavier and optional |
| `mcp` | External capability protocol | Agent | External discovery | **Leave separate** | Structural runtime is local and source-derived |
| `project` / `startup` | Root/identity | CLI/TUI startup | Project/module identity | **Reuse at wiring** | One root/identity owner; do not mint a second project identity |
| `state` | Machine path owner | Caches | Future persistence | **Reuse only if needed** | No cache in MVP; if added later, it must use the state owner |

### Decision summary

- **Extend:** `ast.Index`, `StructuralSource`, structural tools, cognition structural evidence.
- **Reuse:** `ignore`, `agent.StructuralResolver`, `task` evidence model, workspace authority, `vcs`, project/root ownership.
- **Leave separate:** lexical tools, MCP, GRY, Git state, startup probe.
- **Replace:** nothing wholesale. No existing responsibility is deleted.
- **New:** project inventory query surface, incremental update engine, freshness/generation model, structural tool evidence mapping, one read-only inventory tool.

---

## 4. Architectural boundary (Phase 1E)

```text
Structural Runtime
    = what the project structurally is

LLM
    = what the model proposes or reasons

Control Plane
    = what OHGO authorizes

Execution Plane
    = what actually runs

Verifier
    = what can be proven
```

### The runtime owns

- Derived structural facts: project, directories, files, modules, packages,
  symbols, tests, configs, entrypoints.
- Provenance for those facts: path, line range, package, symbol, snapshot
  generation.
- Explicit freshness/completeness: `CURRENT`, `UPDATING`, `STALE`, `INVALID`,
  parse failures, truncation.
- Bounded deterministic queries over the snapshot.
- Incremental parsing and bounded re-resolution.
- The fact "this file is in the structural snapshot", including the scope
  fingerprint that produced it.

### The runtime does not own

- Whether a tool may run (workspace/security).
- Execution, mutation, process launch, or network access.
- LLM reasoning or prompt construction.
- Completion verdicts, task lifecycle, or fact promotion.
- Git state, branch policy, or commit policy.
- Project identity (that is `project`/`startup`).
- External semantic knowledge (MCP, GRY, web).
- Durable project knowledge or human decisions.

### Authority invariant

```text
structural query → agent.ToolOutcome → evidence → LLM proposal
                                              ↓
                                      control plane decides
                                              ↓
                                      execution plane acts
```

Structural knowledge never short-circuits admission. A read-tier structural
tool is admitted by the same workspace policy as `read` and `grep`; it cannot
mutate, execute, or complete a task by itself.

---

## 5. Canonical graph model (Phase 1F)

### 5.1 Representation decision

The canonical model is a **query projection** over the index's file records,
package graph, and relationship graph. It is not stored a second time. This
avoids two competing graphs while giving every query one node/edge vocabulary,
stable identity, and provenance.

### 5.2 Node kinds

| Node | MVP | Identity sketch | Provenance |
|---|---|---|---|
| Project | Yes | `project:<module-or-root>` | root path, module path |
| Directory | Yes | `dir:<rel>` | repository-relative directory |
| File | Yes | `file:<rel>` | path, language, kind, test flag |
| Module | Yes (Go) | `module:<module-path>` | `go.mod`/`go.work` path |
| Package | Yes | `pkg:<dir>#<name>` | package clause + directory |
| Symbol | Yes | `sym:<rel>:<line>:<qualified>` | path, package, line range, kind |
| Test | Yes | `test:<rel>:<line>:<qualified>` | `_test.go` function (`Test*`, `Benchmark*`, `Fuzz*`, `Example*`, `TestMain`) |
| Config | Yes (allowlist) | `config:<rel>` | config path + parsed fact |
| Entrypoint | Yes (Go) | `entry:<rel>:<line>:<qualified>` | non-test `main` function in package `main` |

Identity is **snapshot-scoped**, not durable across edits: a line change changes
the symbol coordinate. Edges and query results always carry the snapshot
generation so a consumer can tell when a coordinate was observed.

### 5.3 Edge kinds

| Edge | MVP | Meaning | Evidence |
|---|---|---|---|
| `CONTAINS` | Yes | Project/directory contains file/directory; package contains file | path/dir |
| `DEFINES` | Yes | File/package defines symbol | declaration coordinate |
| `IMPORTS` | Yes | Package imports package | import declaration file:line; source/test flag |
| `REFERENCES` | Yes | File/package-scope reference to symbol | reference file:line:column |
| `CALLS` | Yes | Statically provable direct call | call site file:line:column; certainty |
| `TESTS` | Yes | Test function statically references/calls a symbol | reference/call site; explicitly not coverage |
| `ENTRYPOINT` | Yes | Project/package exposes entrypoint | `main` coordinate |
| `DEPENDS_ON` | Phase 2 | External module/package dependency aggregate | manifest + resolution; not a runtime claim |
| `IMPLEMENTS` | Future | Type implements interface | requires `go/types`; must stay `UNKNOWN` until then |
| `CONFIGURES` | Future | Config file configures module/package/entrypoint | requires config parsing policy |

### 5.4 Provenance contract

Every node and edge must be able to produce a `SourceRef`:

```go
type SourceRef struct {
    Path      string // repository-relative, slash-separated
    Package   string
    Symbol    string
    StartLine int
    EndLine   int
    StartCol  int
    EndCol    int
    Test      bool
}
```

Aggregate edges (`IMPORTS`) carry a deterministic source witness and, when
present, a test witness. If an edge has no reliable witness, it is not emitted;
the query reports `UNKNOWN`/`unsupported` instead. This is the rule that keeps
the graph from fabricating relationships.

### 5.5 Classification

- **MVP:** Project, Directory, File, Module, Package, Symbol, Test, Config,
  Entrypoint; `CONTAINS`, `DEFINES`, `IMPORTS`, `REFERENCES`, `CALLS`, `TESTS`,
  `ENTRYPOINT`.
- **Phase 2:** `DEPENDS_ON` for external modules; `TESTS` refinements; `CONFIGURES`;
  impact composition; optional `graph.db` only if benchmarks justify it.
- **Future:** `IMPLEMENTS` and type-aware method sets; additional languages;
  runtime-observed edges (clearly separated from static facts).

---

## 6. Language adapter boundary (Phase 1G)

### 6.1 Current boundary

`ast.Searcher` currently exposes `Supported(path)` and `Search(path, Options)`.
`GoBackend.Parse` additionally returns package clause, imports, raw references,
and raw calls. That extra surface is what the graph uses; it is not expressed in
the interface.

### 6.2 Target boundary

```text
Source Language
      ↓
Language Adapter
      ↓
Canonical Structural Model
      ↓
Project Graph / Index
```

Proposed direction, without adding a second parser:

```go
type Adapter interface {
    Name() string
    Extensions() []string
    Supports(path string) bool
    // Parse performs one parser pass and returns canonical file facts.
    Parse(path string, opts ParseOptions) (FileFacts, error)
    // ProjectFacts is optional: module/workspace/config facts for this adapter.
    ProjectFacts(root string, files []FileInfo) ([]ProjectFact, error)
}
```

`FileFacts` contains language, package/module name, symbols with canonical
`SymbolKind`, imports, references, calls, test declarations, and entrypoint
candidates. The Go adapter maps `go/ast` nodes into those facts. Existing
`Kind` values remain the Go adapter's compatibility projection; new canonical
queries use `SymbolKind` so the graph is not forced to expose `func`/`var`/`const`
as the universal vocabulary.

### 6.3 MVP language scope

- Go only. No TypeScript/JavaScript/Python/Rust adapter in this phase.
- Go parsing remains `go/parser`; no cgo, no tree-sitter, no new dependency.
- Build constraints are not evaluated in MVP. Files are parsed as source; a
  duplicate declaration caused by OS/build-tag variants is reported as
  ambiguous, never silently chosen. If a later phase evaluates build tags, the
  active build context must be part of the snapshot metadata.
- `go/types` is not used in MVP. `IMPLEMENTS` and type-sensitive method-set
  claims stay `UNKNOWN`.

---

## 7. Incremental update design (Phase 1H)

### 7.1 Lifecycle

```text
initial indexing (cold)
    ↓
snapshot published (generation N, CURRENT)
    ↓
change detected
    ↓
diff file set against snapshot
    ↓
parse only added/changed Go files
    ↓
remove deleted contributions
    ↓
update derived package/directory aggregates
    ↓
re-resolve affected relationships
    ↓
publish new snapshot (generation N+1, CURRENT)
```

### 7.2 Change detection

- Walk the indexed root and compare size+mtime per file to the stored snapshot.
- Track the `go.mod`/`go.work` fingerprint separately, as the current graph does.
- Track a **scope fingerprint** supplied by the injected `Scope` implementation
  (the `ignore.Matcher` rule state). A `.gitignore` change can add or remove
  files without any `.go` file changing. Add a deterministic `Fingerprint()` to
  `ignore.Matcher` (a hash of the loaded rule text) rather than re-reading or
  re-implementing the rules in `ast`.
- Detect unreadable entries and keep them explicit; never treat an unreadable
  subtree as empty.
- Use stat data for cheap freshness. A `ForceRefresh`/explicit invalidation path
  exists for callers that know mtime is unreliable (e.g. restored timestamps).
  Do not hash every file on every query.
- Git is an optional hint, not a source of truth. `vcs.ChangedFiles` may trigger
  or validate a refresh; a branch switch is handled as a batch file-set change.

### 7.3 Update algorithm

1. Diff current files against `idx.snapshot`; classify `added`, `changed`,
   `deleted`, `unreadable`.
2. For each added/changed `.go` file, parse exactly once with the Go adapter.
   For changed non-Go files, update file/config facts without parsing.
3. Replace/remove `fileRecord`s. Keep the old record if a file became unreadable
   and report the scope as partial; do not present the unreadable subtree as
   deleted source.
4. Update package/directory aggregates only for affected directories. A module
   path change or scope-fingerprint change may require rebuilding the package
   graph; this is derived metadata, not a re-parse.
5. Re-resolve relationships:
   - Re-resolve the changed files and files whose recorded raw references target
     a changed/removed declaration or a changed package/import.
   - If a reverse-dependency index is not yet implemented, re-resolve the retained
     raw relationship facts globally **without re-parsing**. Report the resolution
     scope in diagnostics.
6. Publish the new snapshot atomically under the index lock. Never publish a
   partial update after cancellation or a source-changed-mid-parse condition.
7. Increment `generation`; update `builtAt`, counts, and completeness.

### 7.4 Event matrix

| Event | Parse work | Derived work |
|---|---|---|
| Create Go file | Parse one file | Add package/file contributions; re-resolve affected refs |
| Modify Go file | Parse one file | Replace contributions; re-resolve affected refs |
| Delete Go file | None | Remove contributions; remove empty package/dir nodes; re-resolve |
| Rename Go file | Parse new path | Remove old path; add new path; re-resolve |
| Create/modify/delete non-Go file | None | Update file metadata; if config/scope, update module/scope facts |
| `go.mod` module path change | None if imports unchanged | Recompute module-local target mapping; re-resolve all module-local edges |
| `.gitignore` change | Parse newly included Go files only | Refresh membership; remove excluded contributions; re-resolve |
| Multiple files / batch | Parse each changed Go file once | One published generation |
| Git checkout/branch switch | Parse changed Go files only | Batch update; no Git state stored |
| Parse failure | One failed parse | Keep file metadata; mark unparsed; no symbols/edges from it |
| Unreadable directory | None | Keep last known records, mark partial; recover on next refresh |

### 7.5 Cross-file resolution and the honest claim

The runtime must not claim that a single-file change always costs O(1). A changed
declaration can invalidate references in other files. The MVP guarantee is:

- **One parse per changed Go file.**
- **No re-parse of unchanged files for a normal update.**
- **Relationship re-resolution is bounded and reported.**
- **If global re-resolution is required, it is a proven consequence of the
  change, not a silent full rebuild.**

Phase 2 optimization, only if benchmarks show it is needed: maintain reverse
indexes from declaration/package identity to raw reference sites and re-resolve
only affected files. Do not add this complexity speculatively.

### 7.6 Consistency and cancellation

- Build into local records/graphs, then assign atomically. A cancelled build
  must leave the previous snapshot untouched and return the context error.
- Stat before parse and again after parse. If the file changed during the parse,
  retry once or report the update as failed; never publish a mixed snapshot.
- Queries that observe a failed update return the error, not the previous
  snapshot as if it were current.
- Per-file parse failures are not a failed snapshot: they produce `Partial=true`
  with `ParseFailures > 0`, and every dependent claim is qualified.

---

## 8. Freshness, state, and recovery (Phase 2E preview)

### 8.1 State model

```go
type Freshness string

const (
    Current  Freshness = "CURRENT"  // last successful snapshot matches the observed tree
    Updating Freshness = "UPDATING" // an update is in progress
    Stale    Freshness = "STALE"    // changes detected but not yet applied
    Invalid  Freshness = "INVALID"  // no usable snapshot; queries must error
)
```

This is runtime index state, not a task lifecycle. It does not belong in
`progress.json` and does not use task statuses.

### 8.2 Snapshot metadata

```go
type SnapshotInfo struct {
    Root             string
    Module           string
    Generation       uint64
    SnapshotID       string // deterministic identity for this published snapshot
    SourceFingerprint string // cheap hash of the path/size/mtime signature set
    State            Freshness
    BuiltAt          time.Time
    Files            int
    GoFiles          int
    Packages         int
    Symbols          int
    References       int
    Calls            int
    Tests            int
    Entrypoints      int
    ParseFailures    int
    Partial          bool
    LastParsedFiles  int
    LastReusedFiles  int
}
```

Every query result carries at least `SnapshotID`, `Generation`, `State`,
`Partial`, and `ParseFailures` (directly or through an embedded snapshot view).
`Generation` is process-local and monotonic; `SnapshotID` and
`SourceFingerprint` are what make a structural claim meaningful after a restart.
Evidence records the same values so a structural claim is never detached from
the snapshot it came from.

### 8.3 Recovery

| Condition | Behavior |
|---|---|
| Index missing | Build lazily on first structural query; never at startup |
| Process restart | Cold rebuild; no persisted index in MVP |
| Malformed Go file | Keep file metadata; symbols/edges absent; `Partial=true`; parse failure counted |
| Unreadable directory | Keep last known records; mark partial; retry on next refresh |
| Root unreadable/missing | `INVALID`; queries error; no stale answer |
| Build failure | Previous snapshot is not served as current; error returned; next query retries |
| Cancellation | No partial publish; context error returned |
| Source changes mid-build | Retry once or fail the refresh; do not publish mixed facts |
| Corrupt persisted cache | Not applicable in MVP (no cache); if `graph.db` is added, treat invalid as absent and rebuild |

---

## 9. Query contract (Phase 1I)

### 9.1 Existing API preserved

| Query | Existing API | Notes |
|---|---|---|
| `definitions(name)` | `Index.Lookup` | Exact name/qualified name; ambiguity preserved |
| `symbols(file)` | `Index.File` | File must be indexed |
| `packageSymbols(name)` | `Index.Package` | Package name, not directory |
| `allSymbols()` | `Index.Symbols` | Deterministic order |
| `imports(package)` | `Index.Graph` relation `imports` | Proven module-local edges |
| `importers(package)` | `Index.Graph` relation `imported_by` | Reverse traversal |
| `references(symbol)` | `Index.References` relation `references` | Static package-scope references |
| `callers/callees` | `Index.References` relations `callers`/`callees` | Static direct calls; unknown disclosed |

These signatures and semantics remain stable. New fields are additive.

### 9.2 New API

| Query | Proposed API | Output |
|---|---|---|
| `snapshot()` | `Index.Snapshot(ctx)` | Root, module, generation, state, counts, parse failures |
| `files(...)` | `Index.Files(ctx, FileQuery)` | Path, dir, package, language, kind, test flag; bounded/paged |
| `packages(...)` | `Index.Packages(ctx, PackageQuery)` | Package dir/name, files, test files, imports/importers counts |
| `packageTree()` | `Index.Packages` with directory filter | No separate API; a tree is a projection |
| `entrypoints()` | `Index.Entrypoints(ctx)` | Go `main` coordinates and package context |
| `tests(symbol)` | `Index.Tests(ctx, TestQuery)` | Static test associations + explicit non-coverage label |
| `impact(symbol|path)` | `Index.Impact(ctx, ImpactQuery)` | References, callers, importers, related tests; explicit unknown |
| `changedFiles()` | Not in the index | `vcs`/CLI composition; do not duplicate Git |

### 9.3 Query rules

- **Deterministic:** every result is sorted by path, line, column, symbol, kind.
- **Bounded:** reuse existing default/max limits (50/200 rows; 400 edges;
  depth 1–4; work limit 100k–200k). New inventory queries get comparable limits.
- **Paged:** `skip` for stable pagination where a result can exceed the page.
- **Source-aware:** every node/edge has a `SourceRef` or is explicitly unknown,
  and every result names its `SnapshotID`/generation.
- **Sliced:** no query returns the whole graph by default. A project summary
  returns counts and a bounded package/file slice, not every symbol.
- **No storage leakage:** results expose coordinates, not internal map keys.
- **No semantic claims:** `tests` is static association, not coverage; `impact`
  is static reachability, not behavioral impact.

### 9.4 Proposed tool surface

Keep the existing three tools and add one project-inventory tool:

| Tool | Responsibility |
|---|---|
| `symbols` | Exact symbol/file/directory/package coordinates (unchanged) |
| `graph` | Package imports/imported-by (unchanged) |
| `refs` | References/callers/callees; optionally `tests` and `impact` relations |
| `structure` | Project/file/package/entrypoint inventory; bounded slices |

`structure` is read-tier, shares `StructuralSource`, is added to
`agent.ReadOnlyToolNames`, and is registered in `cmd/ohgo/build.go`. It is
filtered by the existing Go language requirement until a second adapter exists.
`changedFiles` is not added as a structural tool: Git remains the owner and
`bash`/`/diff` remain the interfaces.

All structural tools add `graph_state`, `generation`, `partial`, and
`complete` to their details so the evidence layer can record provenance without
parsing prose.

---

## 10. Persistence strategy (Phase 1J)

### Decision: in-memory only for MVP

Reasons:

- The source tree is authoritative and the index is derived; a cold rebuild is
  deterministic.
- The project already reserves `<project>/.ohgo/graph.db` for a future derived
  graph and explicitly requires it to be disposable, project-local, and never
  required for startup. Implementing it now would add schema, corruption,
  migration, and locking work before measurements justify it.
- The first structural query is lazy; startup is not on the index path.
- The current Phase 6 measurements show one parse pass is the dominant cost; the
  incremental update removes the repeated full-parse cost without a database.

### What persists, rebuilds, caches

| Item | MVP behavior |
|---|---|
| Snapshot records | In memory only |
| Package/relationship graphs | In memory only, derived from records |
| File signatures | In memory only |
| Module/scope fingerprints | In memory only |
| `graph.db` | Not created, not read, not required |
| Cache size | 0 bytes |

### Future persistence gate

If benchmarks show cold-start or memory costs that justify persistence, use the
already-defined `graph.db` lifecycle in `STATE_AND_PERSISTENCE.md`:

```text
No graph → initial index → graph.db
graph.db → validate metadata
             ├── fresh   → reuse
             ├── stale   → incremental update
             └── invalid → rebuild
```

Metadata: project id, source root, schema version, indexer version, source
fingerprint, generated-at. Corruption is treated as absent. The store remains
project-local, derived, and lazily created. Do not add it in MVP.

---

## 11. Integration points

### 11.1 Evidence (`cognition`)

Extend `EvidenceState.apply` (or the existing switch) for `graph`, `refs`, and
`structure`:

- Aggregate item per query, bounded summary, `ProvenanceObservation`,
  `RetentionStructural`, `Complete` only when the result is exact and complete.
  Record `graph_state`, `snapshot_id`, and generation in the item/source so a
  restart cannot make an old coordinate look current.
- Individual coordinate items up to the existing small cap (24), with `Source`
  path/line and IDs such as `graph_edge:`, `ref:`, `call:`, `entrypoint:`,
  `test:`, `package:`.
- Preserve ambiguity/truncation/parse failures in the summary and completeness.
- Graph/refs results must not be classified as arbitrary diagnostics; they are
  structural observations.
- Evidence provenance still comes from `agent.ClassifyPath`; the structural
  runtime's file kinds are for graph queries, not a second provenance classifier.
- Add optional `graph_state`/`snapshot_id` fields to `cognition.Source` (or the
  evidence item) with an additive evidence schema version bump; old records stay
  readable. This is a field addition, not a new evidence model.
- No new evidence model, no new durable store. Task projection continues through
  `continuity/task` as evidence references.

### 11.2 Discovery and tools

- `symbols`/`graph`/`refs` remain the structural surfaces; add `structure` for
  project inventory.
- Update `internal/prompt/prompt.go` and any tool guidance to mention
  `structure` only where it reduces exploration.
- Keep `read`/`glob`/`grep`/`ls` for non-Go, exact text, and content observation.
- Do not route every discovery task through the graph.
- Add structural tool fields additively so existing consumers and benchmarks do
  not break.

### 11.3 Roles and authority

| Role | Structural access |
|---|---|
| ASK | Read-only structural queries; no execution or mutation |
| FUZZ | Same read-only structural queries; existing investigation execution unchanged |
| BUILD | Same queries for implementation/impact; mutation still requires BUILD authority |
| COMMIT | May consume structural evidence; graph access adds no commit authority |

No new tool tier, security capability, or workspace operation. `structure` is
`TierRead`; `symbols`/`graph`/`refs` remain `TierRead`.

### 11.4 Completion and task continuity

- Keep `agent.StructuralResolver` backed by `SymbolsTool` and the same index.
- Target snapshots and completion re-checks use current structural coordinates;
  a stale snapshot still returns an error rather than a false "unchanged".
- Structural observations are evidence; they do not by themselves create facts
  or complete a task. Existing verification rules remain.
- Impact/test associations are advisory evidence, never proof of behavior.

### 11.5 Git

- `vcs` remains the changed-file/branch/HEAD/diff owner.
- The structural runtime may consume changed paths as an optional refresh hint,
  but its own freshness check is filesystem-based and must work without Git.
- Branch switches/checkouts are batch updates; no Git state is stored in the
  graph.
- `changedFiles()` stays a `vcs`/CLI concern.

### 11.6 Grep, MCP, and GRY

- Grep/glob remain lexical discovery; they are not replaced.
- MCP remains an external capability boundary. Codebase-memory/Gry MCP surfaces
  available to the outer agent environment are not part of the OHGO binary's
  native index and are not a required dependency.
- GRY remains an opt-in external semantic/evidence provider; it is not required,
  not started by the structural runtime, and not merged into structural facts.
- If both GRY and the native runtime answer, evidence must keep provenance
  distinct (`gry:*` vs `structure:*`); neither becomes authority.

### 11.7 Startup and shape

- `ProbeFacts` remains a cheap marker probe; it must not build the index.
- The first structural query builds lazily.
- Tool filtering by language remains in the shape/projection layer; no index
  construction at startup.
- A future explicit index-root setting must not silently widen workspace
  containment.

### 11.8 Subagents

- Add `structure` to `agent.ReadOnlyToolNames` if introduced.
- A child receives the same `StructuralSource` when it shares the parent tool
  set, so it sees the same snapshot generation.
- A read-only child remains read-only; structural access adds no capability.

---

## 12. Test strategy

### 12.1 Structural unit tests (`internal/intelligence/ast`)

- Snapshot identity/provenance: deterministic node IDs, sorted results, line
  ranges, source witnesses.
- File/directory/config/module/entrypoint extraction; Go `main` only, no
  fabricated entrypoints.
- Test associations: static references/calls only; same-package test files
  labeled separately; no coverage claim.
- Incremental:
  - one modified file parses exactly one file;
  - unaffected symbols/edges remain;
  - deleted symbols/edges disappear;
  - create/rename/multiple changes;
  - `go.mod` path change re-resolves without re-parsing;
  - `.gitignore` scope change adds/removes membership;
  - non-Go change updates file facts without parsing Go.
- Freshness:
  - `CURRENT` after build; `STALE` after change;
  - `INVALID` on root failure; queries error;
  - parse failure produces `Partial`, not a false complete;
  - cancellation publishes nothing;
  - source changed mid-parse is detected/retried;
  - concurrent readers see a consistent generation.
- Query bounds: depth/node/edge/work limits, pagination, empty results,
  ambiguity, unknown, no-go.mod behavior.
- Determinism: repeated identical queries produce byte-identical structured
  output.
- No second index: the tool set's structural surfaces share one
  `StructuralSource`/`*ast.Index`.

### 12.2 Tool and evidence tests (`internal/execution/tools`, `internal/control/cognition`)

- `structure` is read-tier and admitted in ASK, FUZZ, and BUILD.
- Path containment; no path escapes.
- Subagent read-only set includes `structure`.
- Tool details carry generation/state/completeness.
- `graph`/`refs`/`structure` create structural evidence items, bounded, with
  provenance and correct completeness.
- Truncated/ambiguous/parse-failure results are not marked complete.
- No mutation capability is reported.

### 12.3 Integration tests

- Structural vs lexical discovery (existing test extended): a Go declaration is
  found structurally; a text occurrence in a non-Go file is found lexically.
- Shared snapshot: `symbols`, `graph`, `refs`, `structure` see the same
  generation.
- ASK: structural package/file/symbol query only; no mutation.
- FUZZ: structural investigation plus approved read-only execution; no mutation.
- BUILD: modify a symbol with dependencies; query callers/tests; run the
  prescribed check; completion evidence is structural, not graph wording.
- File modification → generation increments and the changed fact is visible.
- Git branch/checkout in a temporary fixture → batch update; no Git dependency in
  the index.
- Stale/corrupt-like state (unreadable root, malformed file) → explicit
  `INVALID`/`Partial` and recovery.

### 12.4 Test rules

- One test per invariant; assert observable contracts, not source text.
- After adding a test, break the covered behavior and confirm the test fails.
- No global mutable state, `os.Chdir`, or un-restored `t.Setenv`.
- Race tests for concurrent query/update.
- Benchmarks and fixtures must not require network or model calls.

---

## 13. Benchmark strategy

### 13.1 Metrics

| Metric | Measurement |
|---|---|
| Initial indexing | Cold `NewIndex`/first query wall time and parse count |
| Incremental single-file update | Wall time and parsed-file count after one Go change |
| Incremental multi-file update | Wall time and parsed-file count after N changes |
| Query latency | Warm p50/p95 for symbol/file/package/graph/refs/entrypoint queries |
| Memory | Retained heap after GC; graph node/edge counts |
| Graph size | Files, dirs, packages, symbols, references, calls, tests, entrypoints |
| Cache size | 0 bytes in MVP (no cache) |
| Startup | No index work at startup; measure `ProbeFacts` only |
| Context cost | Bytes/lines/calls for structural vs lexical benchmark tracks |

### 13.2 Scenarios

- Real OHGO repository from its root (726 `.go` files at the time of this plan).
- Clean tree, small edit, large refactor, multiple-file change, cold start, warm
  start.
- Branch switch/checkout in a temporary clone or worktree, not the user's tree.
- Synthetic fixtures from the existing Phase 6 performance probe for scale
  (100/1000/3000 files) and a large-file graph.
- Negative cases: parse failures, unreadable directory, no `go.mod`, package
  ambiguity.

### 13.3 Guardrails

- A single-file update must parse exactly one Go file (instrumented), not the
  repository.
- Incremental update must be materially cheaper than a cold rebuild on OHGO.
- Warm structural queries must stay within the existing bounded work limits and
  show no TUI-visible latency regression.
- No startup indexing.
- `bench/repository` tracks A (lexical), B (symbols), C (hybrid) get a D track
  (structural inventory/impact) so reduced exploration is measured, not assumed.
- Results are written under `docs/benchmarks/results/` (create the directory in
  the benchmark change) with host, revision, fixture size, and method notes.

---

## 14. Migration strategy

- **No persisted data migration.** The MVP writes no graph/index files.
- **API compatibility:** keep `NewIndex`, `Symbols`, `Lookup`, `File`, `Package`,
  `Graph`, `References`, and existing tool result semantics. New fields are
  additive. `NewIndex` remains the default/self-contained constructor for tests
  and fixtures; production uses `NewIndexWithOptions` through
  `StructuralSource` with the injected scope. Benchmarks that claim production
  behavior must use the production wiring, not the test constructor.
- **Behavior change:** production `StructuralSource` will apply the injected
  `.gitignore` scope. Existing temp fixtures without ignore files are unaffected;
  a gitignored Go file will no longer be a structural fact. Tests must cover the
  change explicitly.
- **Tool registration:** add `structure` to `buildTools`, prompt guidance,
  `ReadOnlyToolNames`, and structural integration tests. Existing tools keep
  their schemas.
- **Docs:** update `OHGO_ARCHITECTURE_MAP.md`, `ARCHITECTURE.md`,
  `STATE_AND_PERSISTENCE.md`, `README.md`, and the package manifest if a new
  package is introduced. `STATE_AND_PERSISTENCE.md` must stop claiming `ast` has
  no graph.
- **Rollback:** `structure` can be unregistered; the extended index remains
  functional; no persisted state to clean up.
- **Future extraction:** if a second language is added, move the language-neutral
  snapshot/query owner to `internal/intelligence/structural` and keep `ast` as
  the Go adapter. That migration is not part of MVP.

---

## 15. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Global re-resolution after a small change | Incremental update cost grows with relationship count | Retain raw syntax; measure resolution work; add reverse indexes only if benchmarks require |
| Retained raw references/calls increase memory | Resident heap growth | Measure on OHGO; store compact forms; drop raw data after resolution only when no incremental update is needed |
| `go/parser` sees build-tag variants | Duplicate/ambiguous declarations | Never pick a winner; report ambiguity; record build context if tags are evaluated later |
| `.gitignore` scope change alters existing results | Symbols in ignored files disappear | Make the change explicit in tests/docs; keep `ignore` as the one matcher; provide scope fingerprint |
| Cold start blocks the first query | TUI/agent latency | Lazy build already; benchmark; if needed add single-flight/async update with explicit `UPDATING` state |
| Concurrent readers block behind a full build | Query latency | Benchmark; move build outside read lock with atomic snapshot swap if necessary |
| mtime/size can miss a content change | Stale structural facts | Keep stat as the fast path; provide force refresh/invalidation; document residual risk |
| Source changes during parse | Mixed snapshot | Stat-before/parse/stat-after; retry or fail; never publish mixed facts |
| Graph/refs evidence misclassified | Wrong provenance/completeness | Add explicit evidence tests; only mark complete when exact |
| Adding `structure` bloats tool surface/context | Model confusion, token cost | One inventory tool, bounded slices; keep lexical tools; measure context cost |
| GRY overlap | Two graph answers with different provenance | Keep GRY opt-in and labelled; native runtime is baseline; no merge into structural facts |
| Root scope (cwd vs project root) | Incomplete graph in a subdirectory | Document; record nearest module root metadata; keep containment; run at project root for full graph |
| Generated/vendored source | Misleading symbols/edges | Respect ignore rules; classify generated/vendored paths; never claim implementation |
| Future `graph.db` temptation | Schema/corruption/migration work | Keep MVP in-memory; gate persistence on benchmarks and the existing lifecycle |
| Doc drift | Reviewers/consumers trust stale contracts | Update canonical docs in the same change; treat source as truth |

---

## 16. Explicit non-goals

- General-purpose IDE or full code-intelligence platform.
- Replacement for Git, grep, glob, MCP, GRY, or lexical search.
- Semantic/AI database, remote indexing service, model training, or embedding
  store.
- `go/types`/interface implementation analysis in MVP.
- TypeScript/JavaScript/Python/Rust adapters in MVP.
- `graph.db` or any persistent structural cache in MVP.
- A file watcher/daemon; filesystem freshness plus optional Git hints is enough.
- New authority, approval, or mutation path.
- Model-authored edges or model inference promoted to structural fact.
- Uncontrolled graph dumps into prompts.
- Behavioral impact claims from static references.
- Startup indexing.

---

## 17. Implementation order (dependency-ordered)

Each step must leave the repository green (`gofmt`, `go build`, `go vet`,
`go test`, `go test -race`, `golangci-lint`) before the next begins.

### Step 0 — Baseline and contract tests

- Add tests pinning the one-index/one-snapshot contract and current
  symbols/graph/refs behavior.
- Record a baseline benchmark on the real OHGO tree.
- No production behavior change.

### Step 1 — Canonical model and snapshot metadata

- Add `SnapshotInfo`, `Freshness`, `SourceRef`, node/edge vocabularies.
- Keep the existing index maps as storage; canonical types are query views.
- No behavior change; compile/tests.

### Step 2 — Repository snapshot and scope seam

- Add `IndexOptions` with an injected `Scope`/`Exclude` function and scope
  fingerprint.
- Walk all files, not only `.go`; store file metadata/signatures; classify file
  kinds; parse Go only.
- Wire `StructuralSource` to `ignore.Matcher`.
- Tests: non-Go inventory, ignored file exclusion/inclusion, stale on non-Go
  change, no extra parse.

### Step 3 — Go adapter boundary

- Formalize `Adapter`/`FileFacts`; implement `GoBackend` as the Go adapter.
- Add canonical `SymbolKind` while preserving existing `Kind` wire values.
- No behavior change; parser-pass tests.

### Step 4 — File/directory/module/config/package/test/entrypoint facts

- Build directory/file records and aggregates.
- Add module/config allowlist facts.
- Add Go test declarations and `main` entrypoints.
- Tests for determinism, ambiguity, and no fabricated facts.

### Step 5 — Incremental update engine

- Add changed-set diff and per-file parse.
- Add deletion/rename/multi-file handling.
- Update package/directory aggregates for affected dirs.
- Retain raw relationship facts and re-resolve without re-parsing.
- Add parse/resolution counters for tests and benchmarks.
- Tests: one-file parse, delete/create/rename, module/scope changes,
  cancellation.

### Step 6 — Freshness, state, recovery, concurrency

- Add `CURRENT`/`UPDATING`/`STALE`/`INVALID`, generation, completeness.
- Atomic publish; stat-before/after parse; failure/retry behavior.
- Race tests for concurrent readers and updates.

### Step 7 — Query runtime

- Add `Snapshot`, `Files`, `Packages`, `Entrypoints`, `Tests`, `Impact`.
- Reuse bounds/pagination/determinism.
- Tests for truncation, ambiguity, unknown, empty/no-module cases.

### Step 8 — Evidence integration

- Extend `cognition.EvidenceState` for `graph`, `refs`, `structure`.
- Add generation/state/completeness fields to tool details.
- Tests for structural items, provenance, and no context bloat.

### Step 9 — Discovery and role integration

- Add `structure` tool; register; prompt guidance; `ReadOnlyToolNames`.
- Add `tests`/`impact` support to `refs` if included in MVP scope.
- Tests: ASK/FUZZ/BUILD admission, no mutation, shared snapshot, subagent.

### Step 10 — Impact composition

- Compose references, callers, importers, and related tests.
- Report dynamic dispatch/dot imports/parse failures as unknown; no behavioral
  claim.
- Tests for proven edges and explicit unknowns.

### Step 11 — Benchmarks and real-runtime validation

- Measure real OHGO repository and synthetic scale fixtures.
- Add structural track to `bench/repository`.
- Exercise ASK/FUZZ/BUILD scenarios, file modification, branch switch, and
  recovery.
- Record results and compare against lexical/hybrid tracks.

### Step 12 — Documentation and full gates

- Update architecture map, rules/manifest if needed, architecture/state docs,
  README, CHANGELOG.
- Run all required gates and the project-specific architecture tests.
- Confirm no duplicate parsing, no startup index, no authority bypass, no silent
  stale answer.

---

## 18. Phase 2 acceptance criteria

Phase 2 is complete only when all of the following are demonstrably true:

1. One `StructuralSource` and one `ast.Index` serve `symbols`, `graph`, `refs`,
   and `structure`; tests prove no second parser or index.
2. The runtime answers, with provenance and snapshot state:
   definitions, file/package symbols, imports/importers, references, callers,
   callees, files, packages, entrypoints, and static test associations.
3. A single-file change parses only that file; the graph updates without a full
   repository re-parse; a multi-file/branch change is a batch update.
4. Freshness is explicit; a stale or invalid snapshot is never served as current;
   parse failures and truncation are visible; recovery is deterministic.
5. Structural results become bounded evidence items with generation/state and
   completeness; no parallel evidence model exists.
6. ASK, FUZZ, and BUILD can use structural queries, and no structural query adds
   mutation or execution authority.
7. Actual OHGO repository benchmarks demonstrate lower exploration cost and
   acceptable cold/incremental/query/memory behavior.
8. All required repository gates pass, including architecture rules and race
   tests.

---

## Appendix A — Proposed file-level change map

| Area | Files |
|---|---|
| Canonical model/snapshot | `internal/intelligence/ast/model.go`, `snapshot.go` |
| Adapter boundary | `internal/intelligence/ast/adapter.go`, `ast.go` |
| Incremental update | `internal/intelligence/ast/update.go`, `index.go` |
| Queries | `internal/intelligence/ast/query.go`, `graph.go`, `relationships.go` |
| Scope seam | `internal/intelligence/ignore/ignore.go` (add `Fingerprint()`); `internal/execution/tools/source.go` (constructs `ignore.Matcher`); `internal/intelligence/ast/index.go` receives the function |
| Inventory tool | `internal/execution/tools/structure.go`; `cmd/ohgo/build.go` registration |
| Existing tools | `internal/execution/tools/symbols.go`, `graph.go`, `refs.go` (additive state/completeness/details) |
| Evidence | `internal/control/cognition/evidence.go` |
| Read-only set | `internal/agent/subagent.go` |
| Prompt | `internal/prompt/prompt.go` |
| Docs | `docs/architecture/OHGO_ARCHITECTURE_MAP.md`, `docs/architecture/ARCHITECTURE.md`, `docs/architecture/STATE_AND_PERSISTENCE.md`, `README.md` |
| Benchmarks/tests | `cmd/ohgo/phase7_*`, `bench/repository/*`, `internal/intelligence/ast/*_test.go` |

## Appendix B — Phase 2 start gate

Phase 2 may start only after a reviewer confirms:

- extending `ast.Index` is the intended ownership choice for this phase;
- the injected scope seam is acceptable under the dependency-free rule;
- MVP scope (files/directories/packages/symbols/tests/configs/entrypoints +
  imports/references/calls) is the intended first slice;
- `graph.db` remains deferred;
- impact composition is scheduled after the incremental graph is stable;
- the working-directory root behavior is acceptable, or an explicit index root
  decision is made first.
