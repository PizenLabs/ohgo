# Structure Engineering Audit — Final Report

Date: 2026-11-22
OHGO: `da0750e` + the audit changes on `fix/structural`
Targets: OHGO (source, mutable) and `andev0x-blog` (real repository, read-only)
Authority: ASK/FUZZ for investigation, BUILD for the OHGO source changes.

## Executive summary

The native Structure implementation was exercised through its production tool
constructors over the real blog repository, the OHGO repository itself, a
mutable copy for incremental tests, malformed-source fixtures, and adversarial
resolution fixtures.

**No P0 defect was found.** Twelve defects were reproduced or source-verified
and fixed. All relate to correctness of what Structure claims:

- duplicate non-Go names were not resolved through the referring file’s
  imports, suppressing real references;
- a qualified reference could fall back to an unrelated global declaration;
- `typeof X` was not a reference;
- non-Go package selectors contradicted the packages view;
- Next.js route handlers, Pages Router modules, and middleware were omitted
  from entrypoints;
- zero-reference and analysis answers claimed completeness over disclosed
  static gaps;
- documentation drift reported machine-local paths;
- an in-place filter could corrupt the shared name index;
- the cached analysis version and the tool’s level/package descriptions were
  stale.

The blog’s `GenericAmbiguous` count fell from 61 to 0, `Button` references rose
from 5 to 11 (matching source), `useToast` gained its real consumer, package
selectors returned the files/symbols the packages view advertised, and drift
fell from 3 false findings to 0. Graph integrity, incremental convergence, and
downstream evidence semantics were verified.

**Production-readiness assessment: CONDITIONAL PASS for the native,
syntax-level Structure capability. Not a claim of semantic/type-level
completeness.** The capability is usable and honest about its remaining limits;
it is not a compiler oracle.

## 1. Original structural problems

The baseline at `da0750e` already passed `go build`, `go test`, `go vet`,
`gofmt`, and lint. The problems were therefore not build failures:

| Observed at baseline | Consequence |
|---|---|
| `refs useToast` returned `AMBIGUOUS` with 0 references | a real consumer was invisible |
| `typeof Button` was not a reference | `Button` reported 5 of 11 sites |
| `files package=components/blog` returned 0 | advertised package selector unusable |
| `symbols package=components/blog` returned not-found | same |
| a four-module Next.js route fixture reported 1 entrypoint | 3 executable modules silently omitted |
| `refs Card` returned `complete: true` with 1,340 unresolved sites | false completeness |
| `analysis impact Post` returned `complete: true` with `PARTIALLY_OBSERVED` focus | downstream could overclaim |
| drift reported `~/.tmux.conf` etc. as missing repo paths | 3 false findings |
| ambiguous focus printed `useToast, useToast` | no way to choose a declaration |
| schema said level default `package`, code used `relationship` | a model could suppress its own focus |

## 2. Confirmed root causes

1. **Resolution was global-name only.** `languageGraph` had `byName` and
   `byQualified`, no file-scoped import evidence. Repeated names became
   `AMBIGUOUS` and emitted no edge.
2. **A qualifier could be ignored.** `matchName` fell back to `byName` after a
   failed `byQualified` lookup, manufacturing edges for `console.log` and
   `React.ComponentProps`.
3. **Reference extraction was incomplete.** `collectTSReferences` handled
   `type_identifier` and JSX, but not `type_query` (`typeof X`).
4. **Package identity was split.** The projection used directory names for
   non-Go packages; queries used empty `packageName`/`Match.Package` fields.
5. **Route conventions were duplicated and narrow.** Metadata and the parsed
   adapter each owned a page/layout/index list; route handlers, Pages Router,
   and middleware were absent.
6. **Empty results skipped completeness demotion.** `mergeGenericReferencesLocked`
   returned before applying the unresolved/ambiguous check.
7. **Analysis completeness was coverage-only.** `details["complete"]` ignored
   `FocusReport.Evidence`.
8. **Drift accepted unknown machine paths.** Any source-extension code span with
   an unknown first segment was checked document-relative.
9. **In-place filtering aliased the name index.** `filtered := matches[:0]`
   mutated the `byName` backing array.
10. **Cache/contract text was stale** after earlier semantic changes.

## 3. Changes implemented

### Resolution and references

- `languageGraph.byFile` indexes declarations by file and name.
- `disambiguate` applies: local member/qualified declaration → unique local
  declaration → unique resolved-import declaration → global unique rule.
- Qualified references no longer fall back to an unrelated bare declaration.
- Call filtering allocates instead of mutating the shared index.
- `typeof X` identifiers are references.
- Zero-reference non-Go answers inherit the unknown demotion.

### Identity and selectors

- `recordInPackage` / `MatchInPackage` / `targetInPackage` share one package
  identity rule: Go clause, or the directory the `packages` view advertises.
- `files`, `symbols`, `packages`, and `tests` accept that rule.
- `Packages`/`Files`/`Tests` no longer return empty results for a valid
  non-Go package name.

### Entrypoints

- `ast.WebRouteModule` is the single convention owner.
- App Router: page, layout, route, template, default, loading, error,
  global-error, not-found.
- Pages Router: every source file under `pages/`.
- Root or `src` middleware.
- All remain `Verified=false`; discovery is not execution.

### Evidence and downstream

- `Evidence.Complete()` exists and is `OBSERVED`/`VERIFIED` only.
- Analysis `complete` is false when the focus report is partial.
- Ambiguous focus candidates carry `path:line`.
- Drift rejects home-relative and unknown machine-shaped paths while retaining
  conventional roots, explicit relative paths, single file names, and links.
- Analysis cache version bumped to `2026-11-22.1`.

### Contract text

- `structure` level description now says the default is `relationship`.
- The package selector describes both Go clauses and non-Go directory names.

## 4. Files and components modified

```
internal/intelligence/ast/generic.go
internal/intelligence/ast/lang_web.go
internal/intelligence/ast/language.go
internal/intelligence/ast/snapshot.go
internal/intelligence/ast/model.go
internal/intelligence/ast/query.go
internal/intelligence/ast/index.go
internal/intelligence/ast/relationships.go
internal/intelligence/ast/audit_regression_test.go
internal/intelligence/structural/drift.go
internal/intelligence/structural/drift_test.go
internal/intelligence/structural/evidence.go
internal/intelligence/structural/strategies.go
internal/intelligence/structural/model.go
internal/execution/tools/structure.go
internal/execution/tools/structure_analysis.go
internal/execution/tools/structure_analysis_test.go
internal/execution/tools/structure_entrypoint_test.go
internal/execution/tools/symbols.go
docs/architecture/STRUCTURAL_LANGUAGES.md
docs/architecture/STRUCTURAL_INTELLIGENCE.md
CHANGELOG.md
docs/audits/structure-engineering/ (this audit)
```

No new dependency. No new package. No second index or graph.

## 5. Structural accuracy results

### andev0x-blog (final)

| Metric | Final |
|---|---:|
| Indexed files | 111 |
| Supported source | 77 |
| Parsed source | 77 |
| Languages | TSX 68, TS 7, JS 2 |
| Symbols | 529 |
| References | 871 |
| Calls | 1,035 |
| Packages | 9 |
| Dependency nodes / edges | 25 / 6 |
| Unresolved dependencies | 0 |
| Entrypoints | 2 discovered, 0 verified |
| Test files / tests | 0 / 0 |
| Architecture | `mixed` (0.55, OBSERVED), 1 real cycle |
| Structural coverage | COMPLETE |
| `GenericAmbiguous` | 0 |
| `GenericUnresolved` | 1,340 (disclosed) |

The 1,340 unresolved sites are library/external names and constructs outside
the syntax-level projection. They are not reported as edges and now prevent a
false `complete` claim.

### Ground-truth checks

| Symbol/probe | Final | Source truth |
|---|---|---|
| `TerminalBlog` refs | 1 | 1 JSX use |
| `Post` refs | 27 | 27 non-import/non-declaration uses |
| `Button` refs | 11 | 11 semantic sites (5 JSX + 6 `typeof`) |
| `useToast` scoped refs | 1 | 1 consumer |
| `useToast` unscoped | ambiguous, 2 coordinates | two declarations |
| `Card` refs | 0, `complete: false` | no source use; static gaps disclosed |
| `files package=components/blog` | 9 | 9 files |
| `symbols package=components/blog` | 92 | 92 declarations |

### Go — OHGO

- 1,008 Go files, 61 packages, 215 package edges in the final tree.
- `NewIndexWithOptions` references: exactly 17, matching all source call sites.
- `structural.Build` scoped: 12 references, 11 direct callers, 6 static test
  associations.
- Method references are `UNSUPPORTED` with a reason; dynamic dispatch is
  counted (46k+ sites) and never guessed.
- Test discovery and entrypoints (`main.main`) matched source.

## 6. Connectivity results

- Import path aliases resolve (`@/…`).
- JSX components produce references.
- Type annotations produce references.
- `typeof X` produces references.
- Namespace/default imports disambiguate qualified members.
- Unknown qualifiers stay unresolved; no false global edge.
- Go cross-package references and direct calls resolve.
- Package importers are computed from the same resolved import edges.
- Duplicate names remain ambiguous at target selection, but consumer sites are
  attributed through the referring import when it proves the module.
- Interface implementation and runtime dispatch remain outside the model and
  are disclosed.

## 7. Graph integrity results

| Check | Result |
|---|---|
| Dependency nodes/edges (blog) | 25 / 6 |
| Duplicate edges | 0 |
| Dangling endpoints | 0 |
| Self edges | 0 |
| Orphan package nodes | 20 (legitimate isolated UI modules) |
| Strongly connected components | 24, one real 2-node cycle |
| Snapshot nodes / roots / dangling parents | 140 / 1 / 0 |
| OHGO package graph | 96 directory nodes / 215 edges after cleanup |
| Non-Go graph request | explicit error, not empty success |

## 8. Incremental indexing results

On a mutable copy of the blog:

| Step | Files | Symbols | Result |
|---|---:|---:|---|
| Initial | 77 parsed | 529 | baseline |
| Add file | 78 | 532 | new symbols present |
| Modify symbol | 78 | 532 | old name gone, new present |
| Rename file | 78 | 532 | new path present, old absent |
| Delete file | 77 | 529 | counts return to baseline |
| Delete imported file | 77 | — | 0 stale edges; unresolved import disclosed |
| Clean rebuild | 77 | 529 | same logical counts |

The only difference from a clean rebuild was the `generation` counter, which
counts refreshes and is not logical state. No stale edge survived deletion.

## 9. Performance comparison

| Metric | Baseline | Current | Delta |
|---|---:|---:|---:|
| Blog project analysis, median | 1.476 ms | 1.498 ms | +1.5% |
| Blog architecture analysis, median | 71.854 ms | 72.221 ms | +0.5% |
| Synthetic medium cold architecture, paired median | 25.894 ms | 25.360 ms | −2.1% |
| Peak RSS, compiled benchmark | 21.30 MB | 21.71 MB | +1.9% |
| Parsed files / snapshot nodes | 77 / 27 | 77 / 27 | 0 |
| Node / module JSON bytes | 10,763 / 2,425 | 10,763 / 2,425 | 0 |

No measurable regression beyond noise. Warm architecture is about 7× cheaper
than cold; one-file incremental is about 7.3× cheaper. See `PERFORMANCE.md` for
methodology.

## 10. Downstream integration results

| Contract | Result |
|---|---|
| `analysis complete` agrees with focus evidence | PASS (fixed) |
| Zero-reference `complete` agrees with unknown summary | PASS (fixed) |
| Ambiguous focus candidates are actionable | PASS (fixed) |
| Package selector agrees with packages view | PASS (fixed) |
| Structural limits are visible (`PARTIAL`, `UNSUPPORTED`, `UnknownSummary`) | PASS |
| Phase-4 structure → survey → drift → investigate → check pipeline | PASS |
| Live ASK/FUZZ/BUILD model runs | NOT VERIFIED |
| GRY/semantic external providers | NOT VERIFIED (out of scope) |

The structural outputs are consumable by the evidence layer: snapshot ID,
generation, freshness, parse counts, scope completeness, result truncation, and
unknown summaries are machine fields, not prose.

## 11. Test suite results

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go test ./...` | **PASS** (final tree, after removing temporary harnesses) |
| `go vet ./...` | PASS |
| `gofmt -l .` | empty |
| `golangci-lint run ./internal/intelligence/... ./internal/execution/tools/...` | 0 issues |
| `go test -race ./internal/intelligence/ast ./internal/intelligence/structural ./internal/execution/tools` | PASS |
| `go test ./cmd/ohgo -run TestPhase4UnifiedTrustPipeline` | PASS |

The full suite passed on the final tree. No test was skipped, weakened, or
re-pinned to obtain a pass. Regression tests were observed to fail on the
old behavior where the defect was reproducible (see `REGRESSION.md`).

## 12. Remaining limitations

- **Go method references** are `UNSUPPORTED`; use direct callers. Receiver type
  resolution and interface satisfaction would need `go/types`.
- **Dynamic dispatch** is counted (tens of thousands of sites), not resolved.
- **Non-Go resolution is syntax-level**: bare value reads in expressions, some
  type-parameter positions, shadowing, overloads, and runtime dispatch are
  outside the reference projection. `typeof X`, JSX, and type annotations are
  covered.
- **Ambiguous target selection** still requires a `path` scope when two files
  declare the same name. Consumer sites are now attributed where imports prove
  them, but the target query itself does not guess.
- **Drift heuristics**: a single file-like inline code span or any Markdown link
  is still treated as a claim; a tutorial linking a machine-local file can
  appear as a missing target. Conventional-root names are a disclosed
  allowlist, not a fact.
- **Benchmark harness scope**: `bench/structural` constructs `ast.Index`
  directly, without the production ignore scope, so it can inventory hidden
  files the application excludes.
- **Repository documentation drift** remains: several docs link
  `OHGO_ARCHITECTURE_MAP.md` at the old root location. This is a documentation
  issue outside the Structure implementation and was not silently “fixed” by
  changing the drift detector.

## 13. Unverified areas

- Live ASK/FUZZ/BUILD model-driven runs against the blog. The tool contracts,
  evidence extraction, and phase-4 pipeline were tested, but not a model’s
  tool selection or answer quality.
- Semantic LSP and DAP observations (different subsystems; not exposed as the
  native Structure path).
- Very large repositories (millions of files), multi-root workspaces, and
  hostile filesystem races. The 384-file synthetic medium repo and the 1,000+
  file OHGO repo were exercised; no claim is made above that.
- Cross-platform path behavior beyond the audit host (darwin/arm64). Symlink and
  containment behavior is covered by platform-neutral tests.

## 14. Final production-readiness assessment

**PASS for the native syntax-level Structure capability as audited here.**

- No P0 defect.
- All reproduced P1 defects are fixed with regression tests.
- Structural accuracy, connectivity, graph integrity, incremental consistency,
  and evidence honesty improved and were verified on the real repository.
- The capability is no longer silently incomplete in the cases found: missing
  route entrypoints, package selectors, duplicate-name consumers, `typeof`
  references, zero-reference completeness, analysis completeness, and drift
  false positives all now report the truth.
- It is not a semantic compiler replacement. The remaining limitations are
  explicit and are the correct boundary for a syntax-only index.

The requested work is complete to the extent the environment allows. The only
criteria marked NOT VERIFIED are the live model orchestration and external
provider paths, which are outside the native Structure implementation audited
here.
