# Structure Engineering Audit — Test Matrix

Status vocabulary:

- **PASS** — exercised against the real application/index on the stated target.
- **PARTIAL** — some behavior verified, a stated limitation remains.
- **FAIL** — observed contract violation (see `DEFECTS.md`).
- **NOT VERIFIED** — not exercised in this audit; no claim is made.

Targets: OHGO source (mutable) and `andev0x-blog` (read-only). Runtime probes
used the production tool constructors over `StructuralSource`; the temporary
probe programs were removed after the audit.

## A. Repository discovery

| ID | Scenario | Expected | Actual | Status |
|---|---|---|---|---|
| A1 | Root detection | Index the requested root only | `lib/…`, `app/…`, `components/…` resolved from the root | PASS |
| A2 | Inventory vs filesystem | Indexed files equal non-ignored tracked files | 111 indexed; `git ls-files` 111 | PASS |
| A3 | `.gitignore` | Ignore `.next`, `node_modules`, `.ohgo`, `.DS_Store` | 111 inventory; production scope excludes them | PASS |
| A4 | Hidden configuration | Keep meaningful hidden config (`.github`) | `.github` workflows found in OHGO | PASS |
| A5 | Excluded roots | Name the rule and bound scope | OHGO: `dist`, `scripts/__pycache__`, `ScopeComplete=false` | PASS |
| A6 | Nested repository boundary | Include with its own ignore rules | Covered by `ast` tests (`nested_module_regression_test.go`); no nested repo in the target | PASS (automated) |
| A7 | Symlink escape | Do not follow a symlink outside root | Covered by `ast` tests (`index_test.go`) | PASS (automated) |
| A8 | Unsupported extensions | Report as unsupported source | OHGO: `.py(5)`, `.sh(3)`, `structural: PARTIAL` | PASS |

## B. Language and parser coverage

| ID | Scenario | Expected | Actual | Status |
|---|---|---|---|---|
| B1 | TypeScript/TSX declarations | Symbols with kinds/locations | 529 symbols; `Post` interface, `TerminalBlog` function | PASS |
| B2 | JavaScript | Parse `.mjs`/`.js` | `next.config.mjs`, JS helpers parsed | PASS |
| B3 | Go | Native AST symbols/imports/calls/tests | 1,008 Go files, 61 packages, exact 17 `NewIndexWithOptions` refs | PASS |
| B4 | Malformed TypeScript | Surface `PARTIAL`, not empty | `state: PARTIAL`, `reason: syntax error`, `failed_files: 1` | PASS |
| B5 | Parser failure isolation | Other files remain queryable | Tested by `ast` failure-path tests | PASS (automated) |
| B6 | Language without adapter | `UNSUPPORTED`, not zero-fact success | `.py`/`.sh` reported unsupported; snapshot PARTIAL | PASS |
| B7 | Tree-sitter error tolerance | Partial facts with issues | `ParseIssue` + `State=PARTIAL` path | PASS |

## C. Symbol integrity

| ID | Scenario | Expected | Actual | Status |
|---|---|---|---|---|
| C1 | Exact names/kinds | Name, kind, language, path, line | `TerminalBlog` function `components/blog/TerminalBlog.tsx:24` | PASS |
| C2 | Qualified names | Container-qualified declaration | Interface fields `ArticleListPanelProps.posts` | PASS |
| C3 | Duplicate names | Ambiguous, with candidates | `useToast` → 2 candidates with path/line | PASS (after fix) |
| C4 | File-scoped duplicate | Resolve the selected declaration | `useToast` scoped to `hooks/use-toast.ts` → 1 ref | PASS |
| C5 | Import-aware duplicate | Consumer attributed to imported module | `useToast` → `components/ui/toaster.tsx:14` | PASS (fixed) |
| C6 | Type query reference | `typeof Button` is a reference | `Button` refs 5 → 11 | PASS (fixed) |
| C7 | Declaration identity | Stable within snapshot | Snapshot-scoped IDs; line changes change ID | PASS |
| C8 | Go method reference | Explicit `UNSUPPORTED` | `Index.Lookup` returns unsupported reason | PASS |

## D. Cross-file connectivity

| ID | Scenario | Expected | Actual | Status |
|---|---|---|---|---|
| D1 | TS path aliases | `@/…` resolves to file | `app/page.tsx` → `components/blog/TerminalBlog.tsx` | PASS |
| D2 | JSX component usage | Reference edge | 1 reference at `app/page.tsx:7` | PASS |
| D3 | Type annotation usage | Reference edge | `Post` 27 refs (matches source count) | PASS |
| D4 | Value type query | Reference edge | `Button` 11 refs (5 JSX + 6 `typeof`) | PASS (fixed) |
| D5 | Namespace import | Qualifier resolves through import | 2 competing `format` symbols; namespace call resolves to the imported file | PASS (fixed) |
| D6 | Unknown qualifier | No false global fallback | `console.log()` does not resolve to a local `log` | PASS (fixed) |
| D7 | Importers | Directory-level importers | `components/blog` → 1 importer | PASS |
| D8 | Go cross-package calls | Direct static calls | `structural.Build` 11 callers, 12 references | PASS |
| D9 | Interface implementation | Not resolved | Disclosed as unsupported/unknown | LIMITATION |
| D10 | Bare TS value reads | Not collected | `const C = Button` outside projection | LIMITATION (documented) |

## E. Graph integrity

| ID | Scenario | Expected | Actual | Status |
|---|---|---|---|---|
| E1 | Dependency graph | Consistent nodes/edges | blog: 25 nodes / 6 edges / 0 unresolved | PASS |
| E2 | Duplicate edges | None | package graph 0 duplicates | PASS |
| E3 | Dangling endpoints | None | 0 dangling in package/module graphs | PASS |
| E4 | Self edges | None emitted | 0 self edges | PASS |
| E5 | Orphans | Legitimate nodes reported, not invented | 20 package orphans (isolated UI modules) | PASS |
| E6 | Cycles | Exact SCCs | 24 SCCs, one real 2-node cycle (`components`↔`hooks`) | PASS |
| E7 | Snapshot parents | Every node has a parent except project | 140 nodes, 1 root, 0 dangling parents | PASS |
| E8 | Go package graph | Module-local edges only | OHGO 99 directory nodes / 220 edges | PASS |
| E9 | Missing module | Explicit error, not empty graph | blog `graph` returns “requires a module path in go.mod” | PASS |

## F. Incremental indexing

| ID | Scenario | Expected | Actual (temp copy) | Status |
|---|---|---|---|---|
| F1 | Initial parse | 77 files, 529 symbols | 77 / 529 | PASS |
| F2 | Add file | New symbols present | 114 files, 532 symbols, marker found | PASS |
| F3 | Modify file | Old symbol gone, new present | `auditLifecycle=false`, `Renamed=true` | PASS |
| F4 | Delete file | Symbols gone | file count returns to 113, symbol absent | PASS |
| F5 | Rename file | New path only | new path true, old path false | PASS |
| F6 | Delete imported file | No stale edge | 0 stale import edges, unresolved=1 | PASS |
| F7 | Clean rebuild convergence | Same logical counts | after-delete counts equal clean rebuild | PASS |
| F8 | Repeated index | No duplicates | no duplicate symbols/edges observed | PASS |
| F9 | Interrupted parse | No corrupted publish | Covered by `ast` cancellation tests | PASS (automated) |
| F10 | Analysis cache invalidation | New analyzer code invalidates | `AnalyzerVersion` bumped to `2026-11-22.1` | PASS |

The incremental probe’s only “mismatch” against a clean rebuild was the
`generation` counter, which tracks how many refreshes occurred and is not part
of the logical graph.

## G. Query and retrieval

| ID | Scenario | Expected | Actual | Status |
|---|---|---|---|---|
| G1 | Overview | Bounded, metadata-first | 111 files, 0 pending parsed, clear `UNPARSED` | PASS |
| G2 | Languages/packages/files | Honest coverage | TSX 68, TS 7, JS 2; 9 packages | PASS |
| G3 | Package selector | Match advertised package | `files package=components/blog` → 9; `symbols` → 92 | PASS (fixed) |
| G4 | Missing path | Observed absence, no crash | `stat … no such file` | PASS |
| G5 | Outside-root path | Reject | “escapes the working directory” | PASS |
| G6 | Missing project root | Explicit execution error | “build repository index: … no such file” | PASS |
| G7 | Missing symbol | `not_found`, complete only if complete | `NoSuchSymbol` → not_found | PASS |
| G8 | Ambiguous symbol | Candidates, no fabricated answer | `useToast` → two paths | PASS |
| G9 | Relations/impact | Bounded, truncated reason | path targets limited, reason `target_limit` | PASS |
| G10 | Expand | Direct children only | node list + children | PASS |
| G11 | Semantic activation | Explicit unsupported | `structure` returns unsupported | PASS |

## H. Downstream integration

| ID | Scenario | Expected | Actual | Status |
|---|---|---|---|---|
| H1 | Evidence `complete` semantics | No completeness with unresolved focus | `analysis impact Post` → `complete=false`, focus `PARTIALLY_OBSERVED` | PASS (fixed) |
| H2 | Zero-reference completeness | No completeness with unresolved sites | `refs Card` → `complete=false` | PASS (fixed) |
| H3 | Ambiguous focus actionability | Candidate coordinates | `useToast (path:line)` for both | PASS (fixed) |
| H4 | Package selector continuity | Tool result matches `packages` row | files/symbols/tests now agree | PASS (fixed) |
| H5 | Structural limitations visible | `UNSUPPORTED`/`PARTIAL`/unknowns | headers and `UnknownSummary` carry them | PASS |
| H6 | ASK/FUZZ/BUILD live model runs | End-to-end model behavior | Not run in this audit; tool/evidence contracts verified | NOT VERIFIED |
| H7 | GRY/semantic external providers | Provider behavior | Out of scope; untouched | NOT VERIFIED |
| H8 | Documentation drift through investigate | Drift facts current | `TestPhase4UnifiedTrustPipeline` passes | PASS |

## I. Quality gates

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l .` | empty |
| `golangci-lint run ./internal/intelligence/... ./internal/execution/tools/...` | 0 issues |
| `go test -race ./internal/intelligence/ast ./internal/intelligence/structural ./internal/execution/tools` | PASS |
