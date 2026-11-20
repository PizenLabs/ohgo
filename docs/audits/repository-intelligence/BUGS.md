# Confirmed defects

Status values: **Fixed** means the implementation changed and a regression test
was added and observed to fail on the old behavior. **Confirmed** means the
defect is reproducible but is not fixed in this change. **Limitation** is an
architectural boundary, not a contradiction of the current contract.

The environment and exact commits are in `ENVIRONMENT.md`. All counts are from
the current source at those commits.

## OHGO-STR-001 — Go module in a subdirectory is ignored

**Severity:** Critical
**Status:** Fixed
**Component:** Structure Core (`internal/intelligence/ast`)

### Summary

`readModule` looked only for `<index root>/go.mod`. When the module lives in a
subdirectory (`ecommerce/backend/go.mod`), `SnapshotInfo.Module` was empty.
Every module-local import was then treated as external, so cross-package
references, callers, package graph edges, dependency edges, and architecture
were wrong. The same files indexed at `backend/` produced a different graph.

### Reproduction

```sh
go run ./bench/structural -repo /Users/anvndev/Documents/Project/Golang/ecommerce
```

Before the fix: module empty; `graph nodes / edges 109 / 0`; architecture
`independent modules`; `NewOrder` had 0 references even though the source has
13 call sites. Indexing `…/ecommerce/backend` directly produced 9,090
references, 2,412 calls, and 234 dependency edges.

### Root cause

`readModule(idx.walkRoot)` checked one path only. `nearestModuleRoot` was
metadata-only and searched upward, never downward, so the common
`project/backend/go.mod` layout had no module identity. Import targets were
derived by string arithmetic from the walk root, which cannot represent a
nested module.

### Fix

- `discoverModule` now checks the walk root, one unambiguous nested `go.mod`,
  then a parent module for a subdirectory walk. Ambiguous sibling modules
  deliberately produce no module rather than a wrong one.
- `importTargetTable` is derived from the directories that actually contain
  indexed Go files and the absolute module root, not from path arithmetic.
- `packageGraph`, `symbolGraph`, and `qualifierTable` consume that table.
- `Dependencies.Complete` is false with reason `go_module_unresolved` when Go
  files exist and no module was resolved.

### Evidence

| Measurement | Before (project root) | After (project root) | Module-root control (`backend/`) |
|---|---:|---:|---:|
| module | — | `…/backend` | `…/backend` |
| references | 4,801 | 9,090 | 9,090 |
| calls | 1,035 | 2,412 | 2,412 |
| dependency edges | 0 | 234 | 234 |
| `NewOrder` references | 0 | 13 | 13 |
| architecture | `independent modules` | `layered` | `layered` |

### Regression

`TestNestedGoModuleResolvesCrossPackageReferences` (portable and cgo),
`TestSingleNestedGoModuleProducesArchitectureShape`.

---

## OHGO-STR-002 — Target-shaped analysis skipped the focus strategy

**Severity:** High
**Status:** Fixed
**Component:** Structure Core / investigate composition

### Summary

`investigate.StructuralProvider` and `structure view=analysis` left
`structural.Request.Level` at `LevelPackage`. The mandatory
`symbol-neighborhood` strategy has `level: LevelFile`, so it was ineligible.
Impact/flow answers had no `FocusReport` at all, even with a valid focus.

### Root cause

`Level` is the deepest authorized resolution, not a requested resolution. The
default was treated as a forced ceiling. `investigate` emitted
`547 files, 0 modules, architecture not-analyzed` for an impact question.

### Fix

- `investigate.StructuralProvider` requests `LevelRelationship`.
- `StructureTool` defaults `analysisLevel` to `LevelRelationship`; the planner
  still chooses the cheapest strategy for project questions.

### Regression

`TestStructuralProviderRunsFocusStrategyForImpact`,
`TestStructureAnalysisRunsFocusAtDefaultLevel`.

---

## OHGO-STR-003 — Focus `references` counted callers, importers, and tests

**Severity:** Medium
**Status:** Fixed
**Component:** Structure Core

### Summary

`runSymbolNeighborhood` assigned `report.References = result.Total`.
`ImpactResult.Total` is `references + callers + callees + importers + tests`.
A symbol with 2 reference sites, 1 caller, and 1 importer reported 4
references.

### Fix

`report.References = len(result.References)`. Added `Callees` to
`FocusReport`; `visual` output names all counts.

### Regression

`TestFocusReferencesDoesNotIncludeCallersOrImporters`.

---

## OHGO-STR-004 — No forward call edges in impact/focus

**Severity:** High
**Status:** Fixed
**Component:** Structure Core

### Summary

`Impact` exposed only inbound callers. A flow question asks what the focus
reaches; `investigation: flow` therefore returned no forward path.

### Fix

Added `ImpactResult.Callees`, `impactCalleesLocked`, generic callee merging, and
`FocusReport.Callees`.

### Regression

`TestImpactReportsCallees`, `TestFocusReportsForwardCallees`.

---

## OHGO-ML-001 — TypeScript path aliases were not resolved

**Severity:** Critical
**Status:** Fixed
**Component:** Structure Core / Tree-sitter TS adapter

### Summary

`resolveImportTarget` accepted only relative TypeScript imports. The blog uses
`@/*` from `tsconfig.json`; `app/page.tsx` imported
`@/components/blog/TerminalBlog` and `@/lib/post-loader`, but both were
external. The page→component and page→loader edges were absent, and the
project appeared to have no dependency edges.

### Evidence

Before: 25 dependency nodes, 0 edges, 1 unresolved generated import.
After: 25 nodes, 6 edges; `app -> components/blog`, `app -> lib`,
`components/blog -> lib`, `components/ui -> hooks/lib`, `hooks -> components/ui`.
Architecture changed from `independent modules`/`not-established` to `mixed`
(the hook/UI cycle is real).

### Fix

`jsPathConfig` reads `tsconfig.json`/`jsconfig.json` (JSONC comments and
trailing commas accepted), resolves `compilerOptions.paths` and `baseUrl`, and
the shared import resolver uses it. Alias targets are tried with the canonical
source extensions and `index` files, and a target that escapes the repository
is rejected.

### Regression

`TestTypeScriptAliasImportsResolveDependencyEdges`,
`TestTypeScriptAliasUnresolvedIsDisclosed`.

---

## OHGO-ML-002 — JSX component usage was not a reference

**Severity:** High
**Status:** Fixed
**Component:** Tree-sitter TSX/JSX adapter

### Summary

`<TerminalBlog />` in `app/page.tsx` was parsed but produced no reference.
`refs TerminalBlog` returned 0, `impact` returned 0 references, and the
page→component relationship was visible only as a raw import edge.

### Fix

`collectTSReferences` records JSX opening/self-closing element names when they
look like components (uppercase first rune). Intrinsic elements (`div`,
`span`) are skipped; declaration names and type parameters are skipped.

### Evidence

After: `refs TerminalBlog` returns `app/page.tsx:7:11`; `impact TerminalBlog`
reports 1 reference and 1 importer. `relations TerminalBlog` now has 14 nodes /
17 edges, including `app/page.tsx -> TerminalBlog`.

### Regression

`TestJSXComponentUsageBecomesReference`.

---

## OHGO-ML-003 — TypeScript type usage was not a reference

**Severity:** High
**Status:** Fixed
**Component:** Tree-sitter TS/TSX adapter

### Summary

`Post` is declared once and used as a type annotation across the blog.
`refs Post` returned 0. The same class of loss affects interfaces, type
aliases, and generic arguments.

### Fix

`collectTSReferences` records `type_identifier` nodes outside type-parameter
declarations and heritage clauses, with the same uppercase convention used for
component names. Unresolved library types remain unresolved and are counted as
a disclosed static blind spot.

### Evidence

After: `refs Post` returns 27 references across the component tree.

### Regression

`TestTypeScriptTypeUsageBecomesReference`.

---

## OHGO-ML-004 — Non-Go package importers were always zero

**Severity:** Medium
**Status:** Fixed
**Component:** Structure Core package projection

### Summary

`languagePackagesLocked` computed `imports` but never `importers`. Every
TypeScript package printed `importers: 0`, which reads as "nothing imports this
package".

### Fix

Importers are counted from the resolved `languageGraph.importEdges`, per target
directory, excluding self-edges.

### Regression

`TestNonGoPackageImportersAreComputed`.

---

## OHGO-ML-005 — Generated build output was an unresolved local import

**Severity:** Medium
**Status:** Fixed
**Component:** Structure Core dependency honesty

### Summary

`next-env.d.ts` imports `./.next/dev/types/routes.d.ts`. `.next` is generated
and intentionally not indexed, but the import was counted as an unresolved
local gap, making the blog's dependency projection incomplete.

### Fix

Added `ResolutionExcluded`; a relative TypeScript import into a known generated
directory (`.next`, `dist`, `build`, `out`, `coverage`, `target`, `bin`,
`obj`, …) is a known exclusion, not an unresolved authoring error. Excluded
imports do not block completeness.

### Regression

`TestGeneratedImportIsExcludedNotUnresolved`.

---

## OHGO-STR-005 — Single nested module erased the architecture shape

**Severity:** High
**Status:** Fixed
**Component:** `internal/intelligence/structural`

### Summary

After OHGO-STR-001 the graph had 234 edges, but architecture still reported
`not-established`. `ProjectGraphs` collapsed every directory under the single
`backend` module to that module node, so all edges became self-edges. The
module projection was empty even though the code inside is layered.

### Fix

When the snapshot has exactly one Go module and the module graph has no edges,
`runArchitecture` builds a graph of the module's direct children from the
directory graph. This is the same resolution a developer uses when reading the
`backend/` tree. If there is no Go module, the directory projection is used.
Evidence states which resolution was used.

### Evidence

Project root: architecture `layered` (0.50, PARTIALLY_OBSERVED) from 234 edges;
before: `independent modules` with 0 edges. Backend root control: `layered`
(0.75, OBSERVED).

### Regression

`TestSingleNestedGoModuleProducesArchitectureShape`.

---

## OHGO-STR-006 — Zero-edge architecture could be claimed from an incomplete graph

**Severity:** High
**Status:** Fixed
**Component:** `internal/intelligence/structural`

### Summary

`runArchitecture` assigned `independent modules` whenever the module graph had
no edges, even when the dependency projection was incomplete. A missing module
or unresolved imports therefore became a positive architecture claim.

### Fix

Zero edges + incomplete dependencies now yields `not-established` with
`basis: incomplete-graph`. Independence is claimed only when the dependency
projection is complete and there are no unresolved imports.

### Regression

Covered by `TestSingleNestedGoModuleProducesArchitectureShape` and the
dependency completeness assertions in OHGO-STR-001.

---

## OHGO-EVID-001 — Unparsed overview printed zero packages and entrypoints

**Severity:** Medium
**Status:** Fixed
**Component:** `tools.StructureTool`

### Summary

`structure view=overview` on the unparsed ecommerce snapshot printed
`packages: 0` and `entrypoints: 0` while structural state was `UNPARSED`. A
zero package count is not an observation before parsing; Go entrypoints also
require a parsed `main` function. The model could read both as absence.

### Fix

Package count uses the same declaration-count qualification as symbols.
Entrypoints add `not established: source not parsed` when the total is zero
and structural state is not COMPLETE. Convention-discovered web routes are
still shown, with `verified: 0`.

### Verification

Ecommerce overview now prints `packages: 0 (source not parsed)` and
`entrypoints: 0 (...; not established: source not parsed)`.

### Regression

`TestOverviewQualifiesUnparsedCounts`.

---

## OHGO-EVID-002 — Tests view double-counted returned rows

**Severity:** Low
**Status:** Fixed
**Component:** `tools.StructureTool`

### Summary

The tests view printed `returned = len(tests) + len(associations)` while
`total` counted associations. A symbol with 4 unique tests and 4 associations
printed `returned: 8`, which exceeds `total`.

### Fix

`returned`/`total` now count unique tests; separate
`associations_returned`/`associations_total` name the second list.

### Regression

`TestTestsViewSeparatesUniqueTestsFromAssociations`.

---

## OHGO-EVID-003 — Executable templates were not reported as unsupported source

**Severity:** Medium
**Status:** Fixed
**Component:** Structure Core inventory

### Summary

The ecommerce repository has 57 `.gohtml` and 16 `.tmpl` executable templates.
They were not in the unsupported-source extension list, so the only reported
unsupported source was SQL. If SQL were supported, `structural_complete` could
become true despite 73 unparsed templates that shape HTTP output.

### Fix

`.gohtml` and `.tmpl` are reported in `unsupported_languages`, so
`StructuralComplete` stays false and the model sees the gap.

### Verification

Ecommerce now reports `.gohtml(57)`, `.sql(77)`, `.tmpl(16)`.

---

## OHGO-INT-001 — Semantic provider sent the question instead of the focus

**Severity:** Medium
**Status:** Fixed
**Component:** `intelligence/investigate` semantic adapter

### Summary

`SemanticProvider.Observe` called `workspace/symbol` with
`"who calls NewOrder and what depends on it?"`. gopls returned no declaration
for a symbol that exists. The provider then reported semantic UNKNOWN.

### Fix

`semanticQuery` prefers `Request.Focus`; the question is used only for
open-ended investigation.

### Regression

`TestSemanticQueryPrefersFocus`.

---

## OHGO-INT-002 — Semantic reference count included the declaration

**Severity:** Low
**Status:** Fixed
**Component:** `intelligence/investigate` semantic adapter

### Summary

`textDocument/references` was called with `includeDeclaration=true`, then the
same declaration was also recorded as a separate "declared at" fact. `NewOrder`
reported 14 references where the source has 13 call sites.

### Fix

Pass `false`; the declaration remains its own fact.

### Verification

Ecommerce `NewOrder` semantic fact now reports 13 references.

---

## OHGO-INT-003 — gopls started at a root with no Go module

**Severity:** Medium
**Status:** Fixed
**Component:** `cmd/ohgo` semantic composition

### Summary

The semantic manager was started at the project root. For
`ecommerce/backend/go.mod`, gopls had no module at that root and returned no
workspace symbols.

### Fix

`semanticWorkspaceRoot` selects the sole nested module or keeps the project
root when the choice is ambiguous. `SemanticProvider.Root` remains the project
root so paths stay workspace-relative.

### Regression

`TestSemanticWorkspaceRootFindsSingleNestedModule`,
`TestSemanticWorkspaceRootKeepsAmbiguousRoot`.

---

## OHGO-GRY-001 — GRY trace excluded test call sites

**Severity:** High
**Status:** Fixed
**Component:** `intelligence/gry`

### Summary

`gry trace` defaults to excluding tests. The adapter never passed
`--include-tests`, so an impact query for `NewOrder` returned zero inbound
relationships although 13 test functions call it. The adapter reported
`UNRESOLVED`, which is truthful about its own query but useless for impact.

### Fix

The trace command includes `--include-tests`; test rows are evidence for impact
and verification.

### Regression

`TestTraceIncludesTestCallSites`.

---

## OHGO-GRY-002 — GRY locate used ranked lexical search

**Severity:** Medium
**Status:** Fixed
**Component:** `intelligence/gry`

### Summary

`gry search ecommerce NewOrder` returns `NewOrderView`, `newOrderID`, and
`NewOrderSummary` before `NewOrder`. A locate question could not resolve the
exact symbol.

### Fix

`searchSymbols` first asks for `--name-pattern '^NewOrder$'`, then falls back to
ranked search only when the exact query has no results.

### Regression

`TestResolvePrefersExactNamePattern`.

---

## OHGO-GRY-003 — External evidence replaced native evidence

**Severity:** High
**Status:** Fixed
**Component:** `intelligence/investigate` router

### Summary

For target-shaped steps the router returned the GRY observation directly and
never ran native analysis. For impact, GRY carried callers only; native
references, importers, and test associations were lost. A useful stale GRY
answer was also returned without running the current native analysis.

### Fix

Native runs first. A `Sufficient` native observation (intent-specific: found
focus with relationships for impact/flow, established architecture with
edges, etc.) is returned without spending an external request. Otherwise the
external result is merged with native facts/records, sources stay attributed,
and stale external evidence downgrades the combined outcome to `STALE` with an
explicit issue.

### Regression

`TestRouterKeepsSufficientNativeWithoutEscalation`,
`TestRouterKeepsSufficientNativeEvenWhenGlobalCoverageIsPartial`,
`TestRouterEscalatesToExternalWhenNativeIsInsufficient`,
`TestRouterNeverPresentsStaleExternalEvidenceAsCurrent`.

---

## OHGO-BUILD-001 — Doctor could not report structural capability

**Severity:** Medium
**Status:** Fixed
**Component:** Build / diagnostics

### Summary

`ohgo doctor` printed the same nine checks for the portable and multi-language
binaries; neither reported the compiled adapter set. A user could not tell why
TypeScript symbols were missing.

### Fix

Added a `structural` check: `1 adapter (go); portable build...` or
`11 adapters (...)`. Verified on both binaries.

### Regression

`TestDoctorReportsStructuralAdapters`, updated `TestDoctorReportsAllChecks`.

---

## OHGO-BUILD-002 — Self-update would silently remove multi-language support

**Severity:** Medium
**Status:** Fixed
**Component:** Build / update

### Summary

Published release artifacts are `CGO_ENABLED=0`. `ohgo update` on a
multi-language binary would replace it with the portable binary and report a
successful version update, silently removing ten adapters.

### Fix

A non-development multi-language build refuses the install and explains the
choice; `--check` remains safe.

### Regression

`TestPortableUpdateRefusalProtectsMultiLanguageBuilds`.

---

## OHGO-BUILD-003 — `make bench` pointed at directories that do not exist

**Severity:** Low
**Status:** Fixed
**Component:** Makefile

### Summary

`make bench` ran `./tools/ ./agent/ ./ai/`, all removed by the
`internal/` restructure. It failed before running a benchmark.

### Fix

Targets the current intelligence and tools packages. Verified with
`make bench`.

---

## OHGO-BUILD-004 — Test targets did not distinguish build variants

**Severity:** Low
**Status:** Fixed
**Component:** Makefile

### Summary

Only `go test ./...` existed, so the portable and multi-language code paths
were not independently runnable.

### Fix

Added `make test-portable` (`CGO_ENABLED=0`) and `make test-multilang`
(`CGO_ENABLED=1`). Both pass.

---

## OHGO-BUILD-005 — `make clean` left `ohgo-multilang`

**Severity:** Low
**Status:** Fixed
**Component:** Makefile

### Fix

`clean` removes both binaries.

---

## OHGO-PROV-001 — ERNIE tool-call XML was published as an answer

**Severity:** High
**Status:** Fixed
**Component:** `internal/provider/ai` protocol normalizer

### Summary

Baidu ERNIE on OpenRouter returned its final turn as:

```text
<ernie:tool_call>
<invoke name="read">...</invoke>
<invoke name="retrieve">...</invoke>
</ernie:tool_call>
```

The provider adapter delivered that as ordinary assistant text. The runtime
published it verbatim and marked the run `COMPLETED`. The model had asked for
two reads and a retrieval; none ran.

### Root cause

The serialized-tool normalizer recognized dots, generic `<tool_call>`, and
LongCat envelopes, but not the ERNIE namespace. `ProtocolResidue` therefore
stayed false, and completion had nothing to veto.

### Fix

Registered `<ernie:tool_call>` with the existing XML-invoke parser. Valid
envelopes normalize to canonical calls; malformed or unterminated envelopes are
consumed and reported as protocol residue rather than published as prose.

### Evidence

Before: 5 tools ran, the 6th serialized call was printed as the answer, exit 0
`COMPLETED`. After: the normalized read/retrieve calls execute or the run stops
with `NEEDS_REVIEW`; the rerun produced a source-grounded Markdown-loader
answer and did not publish raw XML.

### Regression

`TestNormalizeMessageRecognizesErnieToolEnvelope`.

---

## OHGO-CTRL-001 — Leading “Using read-only …” phrase can force FUZZ

**Severity:** Medium
**Status:** Confirmed (not fixed; conservative authority behavior)
**Component:** `internal/control/workspace`

### Summary

A read-only request phrased as “Using read-only structural evidence, report …”
was refused under ASK with “this request needs FUZZ authority”. Replacing the
opening with “Describe …” kept the same evidence request in ASK and produced
the correct answer.

### Root cause

The intent classifier reads the first clause head. `using` is not a read head,
and the later clause head (`report`/`locate`) is not in the read vocabulary.
Later phrases (`callers`, `tests`, `affected`) are investigative, so the
prediction escalates to FUZZ. This is conservative: FUZZ has more authority,
and the refusal is explicit, not silent.

### Impact

The model is forced to switch authority or the one-shot run fails before any
evidence is gathered. A user who writes a read-only request in a participial
formulation cannot run it under ASK. The workaround is to start with an
explicit read verb (`Describe`, `List`, `Explain`).

### Why it is not fixed here

The classifier is an authority boundary. Adding a general “ignore the first
clause if it starts with using” rule would also change mutation and execution
requests. A narrow rule for `using … read-only …` is possible, but it needs
its own adversarial tests; it is safer to document the conservative behavior
than to widen authority in this audit.

---

## OHGO-COMP-001 — COMPLETED accepts an explicitly incomplete or unverified answer

**Severity:** High
**Status:** Confirmed (not fixed; evaluation-policy work)
**Component:** `internal/agent` completion evaluation

### Summary

Two final-run examples:

- Baidu ERNIE answered a NewOrder impact request with “caller count, test
  count, and two call sites remain unverified”. OHGO printed `COMPLETED` and
  exited 0.
- Cohere invented a `showDetails` prop for `TerminalBlog` that does not exist
  in `TerminalBlogProps`. An earlier run exited 0 and printed `COMPLETED`.

### Root cause

`EvaluateTaskCompletion` checks run shape, failed-call recovery, mutation
verification, protocol residue, grounding/provenance, and explicit evidence
review. It does not check whether every field the user requested was actually
established. A model can therefore honestly report “not established” and be
completed, or assert a plausible field that no tool observed and pass the
available checks.

### Impact

A script that trusts the exit code can treat an incomplete or hallucinated
answer as a satisfied objective. This is separate from parser correctness: the
structure tools returned correct evidence; the completion policy did not
require it.

### Why it is not fixed here

A correct fix needs a task-contract representation of required fields/entities
and a deterministic check that the answer established them, not a string
search for “not established”. That is a broader completion-policy change and
is listed as unresolved in `FINAL_REPORT.md`.

---

## OHGO-DOC-001 — README says LSP/DAP is outside the build

**Severity:** Medium (documentation)
**Status:** Confirmed
**Component:** README

### Summary

`README.md` says "LSP/DAP ... are also outside this build". The repository
contains a bounded LSP client (`intelligence/semantic`), a semantic
`investigate` provider, and `cmd/ohgo` starts `gopls` when installed. The
semantic path now resolves `NewOrder` against `backend/` after the nested
module fix. DAP is implemented as a runtime package but is not wired as a
general-purpose debugging tool.

### Why it is not a runtime bug

The presence of an optional, bounded semantic provider is not a promise that
every LSP feature exists. The documentation is stale relative to the code; the
capability table should say what is wired (workspace/symbol and references via
gopls when installed) and what is not (general LSP/DAP surface).

### Follow-up

Documentation correction, not a code change in this audit. The final report
calls this out rather than silently treating the README as the contract.
