# PHASE 1 — SYMBOL-FIRST REPOSITORY COORDINATES
CLOSURE REPORT

- **Date:** 2026-10-20
- **Tree:** `c6bbd02` + Phase 1 working tree
- **Scope:** audit, defect repair, bounded micro-benchmark, freeze

## 1. Verdict

**PASS** — Phase 1 is CLOSED.

The final tree builds, passes the full suite under `-race`, exposes exactly one
structural tool to the model, and returns stable, human-readable coordinates
with explicit ambiguity, not-found, truncation, and partial-index states.

## 2. Architecture

Final ownership chain, verified by reading the code:

```
model
  ↓
tools.SymbolsTool (wire name "symbols", TierRead)
  ↓
ast.Index (one cached instance per tool, in-process, rebuildable)
  ↓
ast.GoBackend (go/parser + go/ast, no cgo, no tree-sitter)
  ↓
repository-relative coordinate
  path · package · symbol · kind · start_line · end_line
```

- `cmd/ohgo/buildTools` registers exactly one structural tool: `symbols`.
- `SymbolsTool` lazily creates and owns one `*ast.Index`; the handle is
  mutex-guarded because the agent can issue tool calls concurrently.
- `ast.Index` owns all freshness policy. Query entry points revalidate a
  size+mtime snapshot and rebuild only when the tree changed.
- The index retains coordinates only; doc comments and signatures are
  discarded during build, so it does not become a second source cache.
- The model reads source through the existing `read` tool at the returned
  line range. The structural layer never becomes an execution or mutation
  authority.

## 3. Implemented Capability

- Exact lookup by symbol name (`GrepTool.Execute`) or qualified name
  (`tools.GrepTool.Execute`), case-sensitive.
- File enumeration: `path` naming a file.
- Directory enumeration: `path` naming a directory, scoped strictly to files
  beneath it.
- Package enumeration: `package` naming a Go package; `path` can scope it
  when the same package name exists in more than one directory.
- Symbol lookup scoping: `symbol` + `path` and/or `package`.
- Kinds: `package`, `func`, `method`, `type`, `var`, `const`. Methods are
  receiver-qualified; interfaces remain `type` (no taxonomy expansion).
- Coordinates: repository-relative slash path, package, symbol, kind,
  `start_line`, `end_line`.
- Deterministic order: path, then line, then qualified name.
- Bounded output: default limit 50, maximum 200, `skip` pagination.
- Explicit states:
  - `status: ok` — exactly one lookup result, or a complete listing page.
  - `status: ambiguous` — more than one lookup match; no arbitrary winner.
  - `status: not_found` — no match; `Useless`-marked when the index is
    complete, so compaction can elide it.
  - `status: truncated` — the page is not the full set, with `returned`,
    `total`, and `skip` where applicable.
  - Ambiguity survives truncation: status stays `ambiguous` and the text adds
    `truncated: true` plus the totals.
  - `unparsed: N` — one or more indexed files failed to parse, so the result
    is explicitly partial rather than a silent false negative.

Examples from the final tree (actual tool output):

```
GrepTool.Execute
  path: tools/grep.go
  package: tools
  kind: method
  start_line: 126
  end_line: 288
```

```
Tool
  path: agent/tool.go
  package: agent
  kind: type
  start_line: 113
  end_line: 131
```

`Classify` is `ambiguous` with two coordinates:
`startup/startup.go:106-111` and `workspace/intent.go:118`.

## 4. Freshness

Policy:

- The source tree is authoritative; the index is derived and disposable.
- Each `.go` file is fingerprinted by size and mtime in nanoseconds.
- Every query performs a bounded tree stat-walk, then rebuilds the whole
  index only when a file was added, removed, resized, or re-timestamped.
- Root-level symlinks are resolved once so a symlinked working directory is
  indexed under the same relative coordinates; nested symlinks are never
  followed.
- A parse failure is isolated to its file. Neighbouring files stay searchable;
  `File` on the malformed file returns the parse error; every listing/lookup
  reports the `unparsed` count.
- No background worker, timer, persistent cache, or daemon exists.

Test evidence (`ast/index_test.go`, `tools/symbols_test.go`):

| Case | Test |
| --- | --- |
| Initial indexing → lookup succeeds | `TestIndexLookupReturnsStableCoordinate` |
| Modified file → old coordinate gone, new visible | `TestIndexDetectsModifiedFile`, `TestSymbolsToolRefreshesStaleIndex` |
| Same-size rewrite → detected | `TestIndexDetectsSameSizeRewrite` |
| Deleted file → stale symbols gone | `TestIndexDetectsDeletedFile`, `TestSymbolsToolSeesDeletionAndCreation` |
| Created file → discoverable | `TestIndexDetectsCreatedFile`, `TestSymbolsToolSeesDeletionAndCreation` |
| Non-Go changes → no effect | `TestIndexIgnoresNonGoFiles` |
| Malformed file → isolated, explicit | `TestIndexSurvivesMalformedSource`, `TestSymbolsToolReportsParseFailures` |
| Symlinked root → indexed; nested escape → rejected | `TestIndexFollowsRootSymlink`, `TestIndexDoesNotFollowSymlinkOutOfRoot` |
| Concurrent queries → stable, race-free | `TestIndexConcurrentQueriesStayConsistent`, `TestSymbolsToolConcurrentExecute` |

Known freshness limit: a rewrite that preserves both byte size and mtime
(for example, an explicit timestamp restore) is not detected. Normal editor
and compiler writes update mtime and are detected.

## 5. Boundary / Authority

- Every model-supplied path goes through the existing `tools.resolvePath`
  containment authority (symlink resolution + deepest-existing-ancestor
  handling). There is no second path-security implementation in the tool.
- `ast.Index.File` adds a lexical containment guard at the index boundary;
  it is defense in depth, not the model-facing authority.
- Verified rejections: `../outside`, `../../etc/passwd`, nonexistent paths,
  symlinks targeting outside the root. An in-root absolute path resolves.
- Approval is `TierRead`. A before/after content snapshot proves execution
  does not change the repository tree.
- No shell execution, no file writes, no new authorization path, no retries,
  no hidden autonomous search loop, no workspace escalation.
- ASK / FUZZ / BUILD authority, the security gate, provider/model control,
  session persistence, and execution semantics are unchanged.
- If the index cannot be built, the tool returns an error and the lexical
  tools remain available. Repository intelligence is evidence, not a
  mandatory execution dependency.

## 6. Micro-Benchmark

### Method

No live model run was executed; all metrics are deterministic tool-level
measurements. This keeps the closure independent of model availability and
avoids introducing benchmark-only tool gating. Turns and token usage are
therefore **N/A**. The benchmark ran the real `SymbolsTool` and `GrepTool`
against a clean copy of this repository:

- 295 Go files.
- `.git`, `docs/`, `*.md`, and `.ohgo/` excluded so reports could not leak the
  answers into lexical search.
- Track A: `grep` / `glob` (the existing lexical tools).
- Track B: the same tree plus `symbols`; lexical tools were not removed and
  no benchmark-specific shortcut was added.
- Latencies are single tool executions; the warm structural figure is the
  median of 20 repeated T7 lookups after one cold build.

### Results

| Task | Track | Operation | Result | Candidate files to inspect | Output bytes | Latency |
| --- | --- | --- | --- | ---: | ---: | ---: |
| T7 `GrepTool.Execute` | B | `symbols {symbol}` | `ok`; `tools/grep.go`, method, 126–288 | 0 | 147 | 1.53 ms |
| T7, name-only | A | `grep "func .*Execute"` | target present among 34 hits | 27 | 3,493 | 13.21 ms |
| T7, qualified | A | `grep "GrepTool.*Execute"` | target present among 7 hits | 4 | 812 | 12.74 ms |
| T15 `agent.Tool` | B | `symbols {symbol:"Tool"}` | `ok`; `agent/tool.go`, type, 113–131 | 0 | 133 | 1.44 ms |
| T15 | A | `grep "type Tool interface"` | 1 hit | 1 | 62 | 11.73 ms |
| T11 `GrepTimeout` | B | `symbols {symbol}` | `ok`; `tools/grep.go`, const, line 39 | 0 | 139 | 1.47 ms |
| T11 `timeout` | B | `symbols {symbol:"timeout"}` | `not_found` (no substring guess) | 0 | 36 | 1.42 ms |
| T11 | A | `grep "(?i)timeout"` | 154 hits across 40 files; no single authoritative path | 40 | 10,329 | 28.46 ms |
| T16 `GrepTool` | B | `symbols {symbol}` | `ok`; `tools/grep.go`, type, 45–53 | 0 | 135 | 1.89 ms |
| T16 `Grep` | B | `symbols {symbol:"Grep"}` | `not_found` (type is `GrepTool`) | 0 | 36 | 1.56 ms |
| T16 | A | `grep "GrepTool"` | 30 hits | 6 | 2,777 | 11.71 ms |
| T16 `ast` | A | `grep "\bast\b"` | 40 hits | 7 | 2,505 | 32.96 ms |
| Ambiguous `Classify` | B | `symbols {symbol}` | `ambiguous`; exactly 2 coordinates | 0 | 278 | 1.74 ms |
| Ambiguous `Classify` | A | `grep "func Classify"` | 4 hits including substring names | 3 | 351 | 11.08 ms |
| File `tools/grep.go` | B | `symbols {path}` | `ok`; 25 declarations with kinds/ranges | 0 | 3,091 | 1.44 ms |
| File `tools/grep.go` | A | declaration-start grep | 18 starts; no end lines or kinds | 1 | 1,026 | 0.17 ms |
| Package `tools` | B | `symbols {package}` | `truncated`; 50 of 459, explicit total | 0 | 6,679 | 1.49 ms |
| Package `tools` | A | declaration grep + `glob tools/*.go` | 397 starts across 31 files; no total/page | 31 | 23,115 + 547 | 2.53 + 3.43 ms |

Track B's `0` means no candidate-file narrowing before the coordinate; reading
the declaration body is still one `read` call. Track A's count is distinct
files containing matches, not files a model actually opened.

Index cost on the same tree:

- Cold build (3 runs): 102.21 ms, 76.57 ms, 75.98 ms; **median 76.57 ms**.
- Warm T7 lookup (20 runs): **median 1.47 ms**, range 1.37–2.09 ms.

### Interpretation

- The measured delta is **repository reconstruction cost**, not raw speed:
  Track B resolves a known symbol to one coordinate with zero candidate files;
  Track A must narrow 27 (name-only) or 4 (qualified) files for T7, 40 for the
  T11 timeout keyword, and 6–7 for T16.
- T15 is close to lexical parity: `type Tool interface` is already a precise
  lexical query (1 file). The structural tool still returns kind and line
  range without opening the file, but Phase 1 does not claim a large T15
  advantage.
- T11/T16 are not solved by Phase 1. Exact lookup removes substring false
  positives only when the model knows the symbol name; it does not reason
  about "timeout" semantics or choose an implementation. `timeout` and `Grep`
  correctly return `not_found` rather than inventing a coordinate.
- Package enumeration is the clearest reconstruction win: one bounded,
  paginated call with an explicit total versus a 31-file glob plus a 397-hit
  grep with no total.
- No claim is made about model turns, tokens, or final answer quality; those
  were not measured.

## 7. Compatibility Surface

The final tree contains **no** `repo/`, `index/`, `repository/`, or
`codeintel/` package. No alias package has a consumer because no alias package
exists. No compatibility constructor aliases remain.

| Candidate | Present in final tree | Production consumer | Decision |
| --- | --- | --- | --- |
| `repo/` | no | none | do not retain |
| `index/` | no | none | do not retain |
| `repository/` | no | none | do not retain |
| `codeintel/` | no | none | do not retain |

- One wire name: `symbols`.
- No `repo` tool is registered.
- No `NewRepoTool` / `NewSymbolTool` aliases.
- No `wireName` indirection.
- No `FileSymbols` / `PackageSymbols` / `LookupSymbol` aliases on `ast.Index`.
- No `ast.Symbol` type alias; `ast.Match` is the single coordinate record.
- `TestBuildToolsExposesSingleStructuralTool` pins the one-surface decision.

The pre-existing `ast.GoBackend` / `ast.Registry` search API remains because it
is the parser seam used by the index, not a second repository-intelligence
path.

## 8. Defects Found and Fixed During Closure

1. **Tree did not compile.** `ast.Index` already returned errors while the tool
   and tests still used the single-value API. All callers now propagate the
   error; `go build ./...` is green.
2. **Duplicate full index path.** The TUI `/status` wiring constructed a fresh
   `ast.Index` on every render. Removed; the only producer is `SymbolsTool`.
3. **Speculative input aliases.** `operation`, `name`, `query`, `file`,
   `mode`, and `action` were removed. The schema is closed
   (`additionalProperties: false`) and the single vocabulary is `symbol`,
   `path`, `package`, `limit`, `skip`.
4. **Duplicate ordering rule.** The tool sorted again after filters even
   though every index query is already ordered. Removed.
5. **Source text retained in the index.** Doc comments and signatures were
   stored per symbol but never returned. The index now drops them.
6. **Root symlink produced an empty index.** `WalkDir` does not follow a root
   symlink. `NewIndex` now resolves the root once for walking while keeping
   repository-relative keys; nested symlinks are still not followed.
7. **Partial index could look complete.** A malformed file silently vanished
   from lookups. The tool now returns `parse_failures` in details and
   `unparsed: N` in text; `File` still returns the parse error.
8. **Ambiguity could be hidden by truncation.** A multi-match lookup over the
   page limit now stays `ambiguous` and adds `truncated: true` plus totals.
9. **Per-query map churn.** Freshness detection allocated a full path map on
   every query. It now counts entries and compares against the snapshot.
10. **README tool list was stale.** Updated to the actual tool set, including
    `delete` and `symbols`.

## 9. Verification

Run on the final tree (`CGO_ENABLED=0`, macOS arm64):

```
gofmt -l .          → empty
go build ./...      → PASS
go vet ./...        → PASS
go test ./...       → PASS (26 packages)
go test -race ./... → PASS (26 packages)
```

Focused Phase 1 tests: 22 index tests + 22 tool tests + 1 registration test.

## 10. Known Limitations

Phase 1 does **not** provide:

- references
- callers
- implementations
- interface satisfaction / implementation edges
- test relationships
- dependency graphs or package relationship graphs
- cross-file structural traversal
- persistent graph databases, vector search, embeddings, tree-sitter, MCP,
  external indexing services
- substring, fuzzy, or semantic symbol search; lookup is exact-name only
- bare method-name lookup (`Execute` must be asked as `Receiver.Execute`)
- autonomous search loops or repository-wide context injection
- build-tag/compiled-package awareness: every `.go` file outside pruned
  directories is indexed, including files a build constraint would exclude
- incremental reindexing: any tree change reparses the repository (bounded,
  in-process, disposable; ~77 ms cold on this 295-file tree)
- freshness is a full stat-walk of Go files on every query, so warm lookup
  cost grows with repository size even when nothing changed
- index construction is synchronous; cancellation is observed before and
  after the build, not in the middle of parsing
- timestamp-preserving same-size rewrites (size+mtime is the freshness
  fingerprint)

## 11. Phase 2 Boundary

> Phase 2 begins only after Phase 1 is frozen and focuses on structural
> relationships: references, callers, implementations, tests, package
> relationships, and controlled cross-file traversal.

Phase 1 remains a small, trustworthy primitive: it tells the model exactly
where a Go declaration is, deterministically and safely, without requiring the
model to reconstruct that coordinate through grep.
