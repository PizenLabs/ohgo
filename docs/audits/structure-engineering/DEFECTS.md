# Structure Engineering Audit — Defects

Status: **Fixed** means the implementation changed and a regression test was
observed to fail before the change and pass after it. **Limitation** is an
architectural boundary, not a contradiction of the current contract.

Severity reflects consumer impact: P1 = a false answer or unusable selector that
a downstream component can act on; P2 = missing or misleading evidence with a
narrower blast radius; P3 = contract/documentation wording.

## OHGO-STR-101 — Duplicate names were not disambiguated by imports

**Severity:** P1
**Status:** Fixed
**Component:** `internal/intelligence/ast` (`languageGraph`, non-Go resolution)

### Summary

A non-Go reference resolved only by global name. When two modules declared the
same name, every consumer reference became `AMBIGUOUS` and no edge was emitted,
even when the referring file imported exactly one of them. The blog contains
`hooks/use-toast.ts` and a duplicated `components/ui/use-toast.ts`;
`components/ui/toaster.tsx` imports the hook from `@/hooks/use-toast`, but
`refs useToast` returned 0 references.

### Reproduction

```sh
# two modules export useToast; toaster imports hooks/use-toast and calls it
structure view=impact symbol=useToast
```

Before: `ambiguous: true`, 0 references, 61 repository-wide ambiguous sites.
After: scoped to `hooks/use-toast.ts` → 1 reference at
`components/ui/toaster.tsx:14`; repository-wide `GenericAmbiguous` 61 → 0.
The target remains ambiguous when queried without a path, because two
declarations really do share the name.

### Root cause

`languageGraph` indexed only global name/qualified maps. `resolveName` had no
file context and never consulted the referring record's resolved imports.

### Fix

- Index declarations by file and name (`byFile`).
- Resolve a repeated bare name against: a unique local declaration, then a
  unique declaration in a resolved import that actually binds the name.
- Keep the global rule for a unique name; return `AMBIGUOUS` when imports do
  not prove a single target.

### Regression

`TestImportedDuplicateNameResolvesThroughImportPath`,
`TestTwoImportedDuplicateNamesStayAmbiguous`.

## OHGO-STR-102 — In-place call filtering corrupted the name index

**Severity:** P1
**Status:** Fixed
**Component:** `languageGraph.resolveCall`

### Summary

`resolveCall` filtered the match slice in place (`filtered := matches[:0]`).
That slice aliases the `byName` index, so filtering overwrote entries and left
duplicate tail elements. A later reference with the same name could become
ambiguous or resolve to the wrong declaration depending on query order.

### Root cause

Slice aliasing: filtering for callable kinds mutated the shared index backing
array because the length of the original map entry was not changed.

### Fix

Filter into a new slice (`make([]Match, 0, len(matches))`), never the index
backing array.

### Regression

`TestCallResolutionDoesNotCorruptNameIndex` (a call resolves first, then a type
reference with the same name must still resolve).

## OHGO-STR-103 — Unknown qualifiers fell back to a bare global name

**Severity:** P2
**Status:** Fixed
**Component:** Non-Go qualified resolution

### Summary

`matches` looked up `qualifier.Name`, then fell back to `byName[Name]` even
when the qualifier was unknown. `console.log()` could therefore become a call
to a repository-level `log`, and `React.ComponentProps` could become a
reference to an unrelated `ComponentProps`. That is a fabricated relationship.

### Fix

A qualified reference now requires a declared member (`Class.method`), a local
qualified declaration, or an imported namespace/default whose target declares
the name. Otherwise it is `UNRESOLVED`.

### Regression

`TestUnknownQualifierDoesNotResolveToBareName`,
`TestNamespaceImportDisambiguatesQualifiedCall`.

## OHGO-STR-104 — TypeScript `typeof X` was not a reference

**Severity:** P2
**Status:** Fixed
**Component:** `lang_web.go` reference collection

### Summary

Only JSX element names and `type_identifier` nodes were collected. A wrapper
that derives props from a component (`React.ComponentProps<typeof Button>`)
did not count as a `Button` reference. The blog’s `Button` had 5 references
instead of the 11 semantic sites in source.

### Fix

Collect the identifier inside a `type_query` node as a value reference. The
blog now returns 11 (`5` JSX + `6` `typeof`).

### Regression

`TestTypeQueryCountsAsValueReference`.

## OHGO-STR-105 — Non-Go package selectors contradicted the packages view

**Severity:** P1
**Status:** Fixed
**Component:** `Files`, `Package`, `Tests`, `symbols` tool

### Summary

The `packages` view reports the containing directory as the package name for
languages without a package clause (`components/blog`). But
`files package=components/blog` returned 0, `symbols package=components/blog`
returned `not_found`, and `tests package=` used an empty symbol package. A
consumer could read a valid package row and be unable to query it.

### Root cause

Files filtered `rec.packageName`, symbols filtered `Match.Package`, and tests
filtered `TestInfo.Symbol.Package`. Those fields are empty for TypeScript.

### Fix

One package-identity rule (`recordInPackage`, `MatchInPackage`,
`targetInPackage`, `declarationInPackage`): a Go package clause wins; a non-Go
source file belongs to the directory the packages view advertises. The symbols
tool’s second filter uses the same rule.

### Evidence

`files package=components/blog` → 9; `symbols package=components/blog` → 92;
`Packages(Name="components/blog")` → one row.

### Regression

`TestNonGoPackageSelectorMatchesAdvertisedName`.

## OHGO-STR-106 — Next.js route entrypoints were silently omitted

**Severity:** P2
**Status:** Fixed
**Component:** `WebRouteModule` / entrypoint discovery

### Summary

Only `page`, `layout`, and `index` files were route modules. `route.ts` (App
Router handlers), the Pages Router surface, and root middleware were not
entrypoints at all. A fixture with four executable route modules reported one
and still called the snapshot structurally `COMPLETE`.

### Fix

One convention owner, `ast.WebRouteModule`, is used by both the metadata
inventory and the parsed adapter:

- App Router: `page`, `layout`, `route`, `template`, `default`, `loading`,
  `error`, `global-error`, `not-found`.
- Pages Router: every source file under `pages/` (including `_app`,
  `_document`, `pages/api/*`).
- Root or `src` `middleware`.

All remain `Verified=false`: a path convention proves discovery, not execution.

### Regression

`TestStructureEntrypointsIncludeRouteHandlersAndMiddleware`,
`TestStructureEntrypointsAgreeAcrossViews`.

## OHGO-STR-107 — Zero-reference answers claimed completeness with unknowns

**Severity:** P1
**Status:** Fixed
**Component:** `mergeGenericReferencesLocked`

### Summary

When the generic projection had no references to a target, the early return
skipped the completeness demotion. `refs Card` returned `complete: true` while
the same result disclosed 1,340 unresolved non-Go sites. The next target with
one reference correctly returned `complete: false`.

### Fix

Apply the `unresolved > 0 || ambiguous > 0` demotion on the empty path too.

### Regression

`TestZeroReferencesDoesNotClaimCompletenessWithUnresolvedSites`.

## OHGO-STR-108 — Analysis claimed completeness over a partial focus

**Severity:** P1
**Status:** Fixed
**Component:** `structure` analysis view

### Summary

`analysis complete` was `Coverage.Complete() && status=="ok"`. For
`intent=impact symbol=Post` it was `true` while `focus_report.evidence` was
`PARTIALLY_OBSERVED` because 1,340 static resolution gaps affect the
neighbourhood. Downstream evidence reads `complete` as authority for an
absence/count.

### Fix

`Evidence.Complete()` was added (`OBSERVED`/`VERIFIED` only). An analysis with a
focus report that is not complete is not complete, even when its coverage is.

### Regression

`TestStructureAnalysisDoesNotClaimCompletenessForPartialFocus`.

## OHGO-STR-109 — Ambiguous focus candidates were not actionable

**Severity:** P3
**Status:** Fixed
**Component:** `structural.runSymbolNeighborhood`

### Summary

`FocusReport.Candidates` stored only `QualifiedName`. A duplicate name printed
`candidates: useToast, useToast`; a consumer had no coordinate to choose the
declaration it meant.

### Fix

Append `qualified (path:line)` when a path exists, while keeping the JSON field
a string list.

### Regression

`TestStructureAnalysisNamesAmbiguousCandidateCoordinates`.

## OHGO-STR-110 — Documentation drift reported machine-local paths

**Severity:** P2
**Status:** Fixed
**Component:** `structural` documentation drift

### Summary

The blog’s tutorial documentation names `~/.tmux.conf`,
`~/.config/hypr/hyprland.conf`, and `home/hypr/hyprland.conf`. All three were
reported as missing repository paths (high severity), although they describe
the reader’s machine. Baseline: 4 claims, 1 resolved, 3 findings. After:
1 claim, 1 resolved, 0 findings.

### Root cause

Inline code spans with a source-like extension were accepted as claims even
when the first segment was unknown, and home-relative paths were not rejected.

### Fix

- Reject `~`, `~/`, and `~\` claims.
- For an unknown first segment, accept only an explicitly relative spelling,
  a single file-like name, or a conventional repository root (`src`, `cmd`,
  `internal`, `docs`, `pkg`, `app`, …). Conventional roots are an explicit,
  documented heuristic.

### Regression

`TestDocumentationDriftResolvesModulePathsAndRejectsNonPaths` now includes
home-relative and machine-shaped claims. `TestPhase4UnifiedTrustPipeline` and
`TestStructureDriftComparesDocumentationWithoutParsingSource` pin that
`docs/missing.yaml` is still reported.

## OHGO-STR-111 — Stale analysis-cache version

**Severity:** P2
**Status:** Fixed
**Component:** `structural` result cache

### Summary

The audit changed resolution and focus completeness semantics. Persisted
analyses keyed only by schema, adapter, root, snapshot, and request would have
been reused across the change.

### Fix

`AnalyzerVersion` bumped to `2026-11-22.1`. Cache entries from earlier semantics
now miss and recompute.

## OHGO-STR-112 — Tool schema contradicted the level default

**Severity:** P3
**Status:** Fixed
**Component:** `structure` tool schema

### Summary

The schema said `level` defaults to `package`. The implementation defaults to
`relationship` so target-shaped intents can run `symbol-neighborhood`; a model
following the schema could pass `package` and lose its focus report. The
`package` argument description also said “exact Go package name” after non-Go
directory names became valid.

### Fix

Both descriptions now match the implementation.

## Residual limitations (not defects)

- Go method references are `UNSUPPORTED`; use callers, which are direct static
  edges. Dynamic dispatch is counted, never guessed.
- No `go/types`: interface implementation and overload resolution are not
  claimed.
- Non-Go resolution is syntax-level. Bare value reads in expressions, type
  parameters in some positions, shadowing, and runtime dispatch remain outside
  the reference projection.
- A single file-like inline code span and every Markdown link are still drift
  claims; a tutorial linking a machine-local file can be reported.
- `bench/structural` builds `ast.Index` directly without the production ignore
  scope; it can report hidden files the tool excludes. This is a harness
  difference, not a production path.
- The repository’s own docs still contain links to `OHGO_ARCHITECTURE_MAP.md`
  at the old root location. That is documentation drift, not a Structure
  implementation defect; it was left untouched.
