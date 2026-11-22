# Structure Engineering Audit — Regression

## 1. Production changes

| Area | File | Change |
|---|---|---|
| Non-Go resolution | `internal/intelligence/ast/generic.go` | `byFile` index; import-aware `disambiguate`; strict qualified resolution; non-mutating call filter |
| Web references | `internal/intelligence/ast/lang_web.go` | `typeof X` reference; shared route convention |
| Route conventions | `internal/intelligence/ast/language.go` | `WebRouteModule`, the single owner of Next.js App/Pages/middleware conventions |
| Entrypoint discovery | `internal/intelligence/ast/snapshot.go` | metadata discovery calls `WebRouteModule` |
| Package identity | `internal/intelligence/ast/model.go`, `query.go`, `index.go` | one rule for Go clauses and non-Go directory packages |
| Reference completeness | `internal/intelligence/ast/relationships.go` | empty generic result keeps the unknown demotion |
| Drift accuracy | `internal/intelligence/structural/drift.go` | reject home/unknown machine paths; conventional-root allowlist |
| Evidence semantics | `internal/intelligence/structural/evidence.go` | `Evidence.Complete()` |
| Focus candidates | `internal/intelligence/structural/strategies.go` | candidate labels include path/line |
| Analysis completeness | `internal/execution/tools/structure_analysis.go` | focus evidence participates in `complete` |
| Package selector | `internal/execution/tools/symbols.go` | second filter uses `ast.MatchInPackage` |
| Tool contract text | `internal/execution/tools/structure.go` | level default and package-selector descriptions match behavior |
| Cache invalidation | `internal/intelligence/structural/model.go` | `AnalyzerVersion = 2026-11-22.1` |
| Documentation | `docs/architecture/STRUCTURAL_LANGUAGES.md`, `STRUCTURAL_INTELLIGENCE.md`, `CHANGELOG.md` | canonical behavior updated |

No public tool name, view, argument, or result field was removed. The JSON
`candidates` field remains a string list; only the string content now carries a
coordinate.

## 2. Regression tests added

| Test | Contract |
|---|---|
| `TestImportedDuplicateNameResolvesThroughImportPath` | a repeated name resolves through the referring file’s import |
| `TestTwoImportedDuplicateNamesStayAmbiguous` | two reachable candidates still emit no edge |
| `TestCallResolutionDoesNotCorruptNameIndex` | call filtering cannot rewrite the name index |
| `TestTypeQueryCountsAsValueReference` | `typeof X` is a reference |
| `TestUnknownQualifierDoesNotResolveToBareName` | unknown qualifier cannot become a global edge |
| `TestNamespaceImportDisambiguatesQualifiedCall` | namespace import proves the member’s module |
| `TestZeroReferencesDoesNotClaimCompletenessWithUnresolvedSites` | zero results do not hide static gaps |
| `TestNonGoPackageSelectorMatchesAdvertisedName` | files/symbols/package selectors agree with the packages view |
| `TestStructureEntrypointsIncludeRouteHandlersAndMiddleware` | route handlers, Pages Router, middleware are discovered |
| `TestStructureAnalysisDoesNotClaimCompletenessForPartialFocus` | analysis `complete` agrees with focus evidence |
| `TestStructureAnalysisNamesAmbiguousCandidateCoordinates` | ambiguous focus is actionable |
| Drift fixture extension | `~/.tmux.conf` and `home/hypr/…` are not repository claims |
| `TestPhase4UnifiedTrustPipeline` (existing) | `docs/missing.yaml` is still a real drift finding |

Defect regression tests were observed against the old behavior where the
defect was reproducible; guard tests pin boundaries that must stay ambiguous
or unsupported.

- Import-aware resolution: failed with `total: 0` before the change.
- In-place filtering / qualifier: the allocation path was reviewed and pinned;
  the combined resolution test failed before the change.
- Two-candidate ambiguity and unsupported-method guards: behavior boundaries,
  not repaired defects.
- `typeof`: failed with `total: 0`.
- Package selector: failed with `files.Total = 0`.
- Route entrypoints: the runtime probe reported 1 of 4 before the change.
- Analysis completeness: failed with `complete: true`.
- Zero-reference completeness: failed with `complete: true`.
- Drift: failed with a finding for `~/.config/hypr/hyprland.conf`; the tightened
  rule was then re-run against the existing phase-4 drift test to keep a real
  missing path reportable.

## 3. Compatibility checks

| Check | Result |
|---|---|
| Snapshot IDs / generations preserved as fields | PASS |
| Existing views (`overview`, `scope`, `languages`, `files`, `packages`, `entrypoints`, `configs`, `tests`, `relations`, `impact`, `analysis`, `expand`) | PASS |
| Existing arguments (`path`, `package`, `symbol`, `limit`, `skip`, `depth`, `activation`, `intent`, `level`, `node`) | PASS |
| Structural evidence vocabulary (`OBSERVED`, `PARTIALLY_OBSERVED`, `UNKNOWN`, …) | unchanged |
| `graph` remains Go-only with an explicit non-Go error | PASS |
| Semantic activation remains explicitly unsupported | PASS |
| Analysis cache keys include analyzer version | PASS (`cache_test.go`) |

## 4. Final gates

| Gate | Command | Result |
|---|---|---|
| Build | `go build ./...` | PASS |
| Full tests | `go test ./...` | PASS |
| Vet | `go vet ./...` | PASS |
| Formatting | `gofmt -l .` | empty |
| Lint | `golangci-lint run ./internal/intelligence/... ./internal/execution/tools/...` | 0 issues |
| Race | `go test -race ./internal/intelligence/ast ./internal/intelligence/structural ./internal/execution/tools` | PASS |
| Phase-4 integration | `go test ./cmd/ohgo -run TestPhase4UnifiedTrustPipeline` | PASS |

The full-suite result is recorded in the final report with the final tree.

## 5. End-to-end regression — andev0x-blog

| Probe | Baseline | Final |
|---|---|---|
| Indexed files | 111 | 111 |
| Parsed source | 77 | 77 |
| Symbols | 529 | 529 |
| References | 817 | 871 |
| Calls | 1,035 | 1,035 |
| Dependency nodes / edges | 25 / 6 | 25 / 6 |
| `TerminalBlog` references | 1 | 1 |
| `Post` references | 27 | 27 |
| `Button` references | 5 | 11 |
| `useToast` scoped references | 0 (ambiguous) | 1 |
| `GenericAmbiguous` | 61 | 0 |
| Drift claims / findings | 4 / 3 | 1 / 0 |
| Entrypoints | 2 discovered | 2 discovered |
| Incremental clean-rebuild convergence | not measured | logical counts equal |

The reference total rose because `typeof` sites are now observed; this is an
accuracy gain, not duplicate counting.

## 6. Non-regression boundaries

- The Go adapter and `go/types`-free contract are unchanged.
- No new dependency was added.
- `internal/intelligence/structural` still imports only `ast` and the standard
  library.
- No repository source mutation was added to any structural path.
- The blog repository was never written to; all mutation tests ran on copies
  under `/tmp`.
