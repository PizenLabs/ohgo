# Structure Engineering Audit — Baseline

Date: 2026-11-22
OHGO revision: `da0750e` (`fix/structural`, clean working tree at start)
Branch: `fix/structural`
Go: `go version` as installed on the audit machine (darwin/arm64)
Test repository (read-only): `/Users/anvndev/Documents/Project/Blog/andev0x-blog` at `fdb0b00`

## 1. What was audited

The audit covers the Structure capability end to end:

| Layer | Package | Responsibility |
|---|---|---|
| Scope | `internal/intelligence/ignore`, `internal/execution/tools/source.go` | `.gitignore`, nested repositories, containment |
| Observation | `internal/intelligence/ast` | files, languages, parsers, symbols, imports, references, calls, tests, entrypoints |
| Interpretation | `internal/intelligence/structural` | hierarchy, dependency graph, planner, strategies, evidence, drift, cache |
| Capability | `internal/execution/tools` (`structure`, `symbols`, `refs`, `graph`) | argument contract, bounded rendering, provenance |
| Consumption | `internal/control/cognition`, `internal/agent` | evidence extraction, completion, claim grounding |
| External | `internal/intelligence/{investigate,gry,semantic,runtime}` | optional providers; not the subject of this audit |

The canonical architecture documents are `docs/architecture/STRUCTURAL_LANGUAGES.md`,
`docs/architecture/STRUCTURAL_INTELLIGENCE.md`, and
`docs/architecture/OHGO_ARCHITECTURE_MAP.md`. The prior audit is
`docs/audits/repository-intelligence/`.

## 2. Baseline quality gates

All commands were run from the OHGO repository root before any change:

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go test ./...` | PASS (full suite) |
| `go vet ./...` | PASS |
| `gofmt -l .` | empty |
| `golangci-lint run` | PASS on the audited tree; not separately measured at baseline |

The baseline is green. A green suite did not establish structural
correctness; the runtime probes below did.

## 3. Baseline runtime behavior — andev0x-blog

The production structural source (`.gitignore` scope injected) reports:

| Metric | Baseline |
|---|---:|
| Indexed files | 111 |
| Supported source files | 77 |
| Parsed source files | 77 (after a targeted/impact activation) |
| Languages | TypeScript JSX 68, TypeScript 7, JavaScript 2 |
| Symbols | 529 |
| References | 817 |
| Calls | 1,035 |
| Packages | 9 |
| Dependency nodes / edges | 25 / 6 |
| Unresolved dependencies | 0 (one generated `.next` import is `EXCLUDED`) |
| Entrypoints | 2 discovered, 0 verified |
| Test files / tests | 0 / 0 |
| Structural coverage | `COMPLETE` after parse |
| Architecture | `mixed` (0.55, OBSERVED), one real components↔hooks cycle |
| Documentation drift | 4 claims, 1 resolved, 3 findings |
| `GenericUnresolved` | 1,297 |
| `GenericAmbiguous` | 61 |

The same tree read through `bench/structural` without the injected ignore scope
reports 113 files because it also inventories hidden files the production
scope excludes. That difference is a benchmark-harness artifact, not a
production inconsistency.

### Baseline relationship probes

| Probe | Baseline result | Source truth |
|---|---|---|
| `refs TerminalBlog` | 1 reference (`app/page.tsx:7`) | 1 JSX use |
| `refs Post` | 27 references | 27 non-import, non-declaration uses |
| `refs Button` | 5 references | 11 semantic sites (5 JSX, 6 `typeof Button`) |
| `refs useToast` | `AMBIGUOUS`, 0 references | 1 consumer (`components/ui/toaster.tsx:14`) |
| `files package=components/blog` | 0 | 9 files |
| `symbols package=components/blog` | not-found | 92 declarations |
| `analysis intent=impact symbol=Post` | `complete: true` while the focus evidence was `PARTIALLY_OBSERVED` | contradiction |

### Baseline entrypoint probe

A Next.js fixture with `app/page.tsx`, `app/api/health/route.ts`,
`pages/api/hello.ts`, and `middleware.ts` reported **1** entrypoint
(`app/page.tsx`). Three executable route modules were silently omitted while
the snapshot reported `structural: COMPLETE`.

### Baseline drift probe

Tutorial documentation in the blog names machine paths such as `~/.tmux.conf`
and `home/hypr/hyprland.conf`. The baseline extractor reported all three as
missing repository paths (high severity). They are not repository claims.

## 4. Baseline Go behavior — OHGO itself

| Metric | Baseline |
|---|---:|
| Inventory files | 1,122 (opening probe) |
| Go source files | about 1,000 (opening probe) |
| Parsed Go files | 1,007 in the final tree |
| Go packages / graph nodes | 61 § |
| Dependency edges | 215 § |
| Symbols | about 18k across `internal`, `cmd`, `bench` |
| Tests / test files | 583 test files |
| Unsupported source | `.py` (5), `.sh` (3) |
| Scope | PARTIAL: `dist/`, `scripts/__pycache__/` excluded by `.gitignore` |
| Structural coverage | PARTIAL because unsupported source exists |
| `NewIndexWithOptions` references | 17, exactly matching source call sites |
| `structural.Build` scoped impact | 12 references, 11 callers, 6 test associations |

§ Final production tree. Probes made while temporary audit harness packages
existed reported 63 packages / 220 edges; those harnesses were removed before
the final gates.

Go module discovery, package graph, references, calls, tests, and entrypoints
matched source. The known Go limits (method references, interface satisfaction,
dynamic dispatch) are disclosed by the tool and remain limitations.

## 5. Existing defects carried into the audit

- Prior-audit fixes for nested modules, path aliases, JSX/type references, and
  focus level were already present and verified again.
- Known limits: syntax-only TypeScript resolution, no `go/types`, no interface
  implementation, method references are `UNSUPPORTED`, dynamic dispatch is
  counted as unknown.
- Documentation in the repository itself contains stale links
  (`OHGO_ARCHITECTURE_MAP.md` moved to `docs/architecture/`). This is a
  documentation defect, not a Structure implementation defect; it is recorded
  as residual drift.
