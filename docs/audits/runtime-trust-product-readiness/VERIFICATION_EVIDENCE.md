# Verification evidence

Date: 2026-11-20
OHGO HEAD: `4848ed9a532f643d91282bdf8da941a283a92d5e`
Closure work: uncommitted working-tree changes on that HEAD

This file is the acceptance record for the closure phase. It records the exact
commands, counts, hashes, and per-gate evidence; it does not replace the bug
ledger or the final report.

## 1. Test volume

CGO=1 full suite (`go test ./... -count=1 -json`):

| Measure | Value |
|---|---|
| Packages with test events | 59 |
| Unique test/subtest events | 6,010 |
| Passed | 5,986 |
| Failed | 0 |
| Skipped | 24 |

CGO=0 full suite (`CGO_ENABLED=0 go test ./... -count=1 -json`):

| Measure | Value |
|---|---|
| Packages with test events | 59 |
| Passed | 5,961 |
| Failed | 0 |
| Skipped | 25 |

The difference is the cgo-only multi-language adapter tests; the portable
suite skips them and does not fail. `go test -race ./...` passed with no race
report. The counts include subtests, so they are larger than the number of
top-level `func Test…` declarations.

## 2. Engineering commands

| Command | Result |
|---|---|
| `go build ./...` | pass |
| `go test ./...` (CGO=1) | pass, 0 failed |
| `CGO_ENABLED=0 go test ./...` | pass, 0 failed |
| `go test -race ./...` | pass |
| `go vet ./...` | pass |
| `gofmt -l .` | empty |
| `golangci-lint run ./...` | 0 issues |
| `make bench` | pass |

## 3. Build matrix

| Artifact | Command | SHA-256 | Adapters |
|---|---|---|---|
| `ohgo` | `make build` | `4d5dec5236f4476a3af492522ad376d1aff6cf637c068c437d8c5bb8f1078b29` | 1 (`go`) |
| `ohgo-multilang` | `make build-multilang` | `1a9b4cd7b71a148fec9dadd24a4de782d8167a73b3b90622d698c156aba22683` | 11 (`go` + 10 Tree-sitter) |

`./ohgo doctor` and `./ohgo-multilang doctor` both passed the structural check
with the adapter counts above. The portable build reports other-language
adapters as unsupported; the multi-language build parses the blog's TypeScript,
TSX, and JavaScript.

## 4. Gate evidence

### Gate A — Completion integrity

- `internal/agent/facets_test.go` — required facet missing, unverified, partial,
  contradicted absence, bounded zero, static caveat.
- `internal/agent/code_claims_test.go` — invented prop, observed prop, no-source
  baseline, unobserved identifier, prose adjective.
- `internal/agent/entity_claims_test.go` — novel predicate, observed entity,
  boundary miss, no baseline, imperative advice, absence contradiction.
- `internal/agent/review_test.go` — one correction, stop after one, no directive,
  corrected prop.
- `internal/agent/test_absence_contract_test.go`,
  `internal/agent/claim_scope_test.go`,
  `internal/agent/negative_claim_test.go`,
  `internal/agent/implementation_claim_test.go`,
  `internal/agent/claim_coverage_contract_test.go`.

### Gate B — Authority integrity

- `internal/control/workspace/authority_regression_test.go`
- `cmd/ohgo/authority_injection_test.go`
- `cmd/ohgo/authority_route_test.go`
- `internal/agent/grounding_test.go`
- Real run: ASK + mutation clause refused before the provider; zero provider
  turns and zero tokens (prior audit R6; unchanged).

### Gate C — Evidence integrity

- `internal/intelligence/gry/source_verify_test.go` — endpoint current/stale/
  unavailable/symlink/unknown; edge `UNVERIFIED`; node vs edge separation.
- `internal/intelligence/gry/gry_test.go` — ambiguity, stale context, bounded
  results, exact-name lookup.
- `internal/intelligence/investigate/router_test.go` — stale external evidence
  downgrade, optional provider absence, external failure disclosure.
- `internal/intelligence/ast/sibling_module_test.go`,
  `target_audit_test.go`.
- Real probes: ecommerce and blog measurements unchanged from the prior
  baseline (section 5).

### Gate D — Operational integrity

- `internal/continuity/session/usage_test.go`
- `cmd/ohgo/session_usage_test.go`
- `internal/agent/completion_test.go` — resource stop vs failure vs
  cancellation.
- Sandbox ledger: seven sessions, measured provider totals; Ling 429 has
  `usage_turns = 0`, not a fabricated zero.

### Gate E — Regression integrity

- `internal/intelligence/ast` nested module, references/callers, forward
  callees, TS aliases, JSX/type references, generated-import exclusion.
- `internal/intelligence/gry` trace tests and exact-name lookup.
- `internal/control/cognition` native-first merge and projection.
- `make bench` passed.

### Gate F — Product and documentation integrity

- `README.md` portable/multi-language split, bounded `gopls`, DAP
  observation-only, multi-language update refusal.
- `./ohgo doctor` and `./ohgo-multilang doctor` agree with the adapter table.
- `./ohgo update` refusal is covered by the update tests.

### Gate G — Hosted free-model verification

See `MODEL_EVALUATION.md` and `MEASUREMENTS.md`. The closure-phase samples were:

| Sample | Result |
|---|---|
| LongCat blog investigation | `NEEDS_REVIEW` for “no tests” overclaim |
| LongCat ecommerce callers, before calibration | `NEEDS_REVIEW` for an evidence-gap false positive |
| LongCat ecommerce callers, after calibration | `COMPLETED`, all 13 call sites |
| Nemotron blog props, before adjective fix | `NEEDS_REVIEW` for false `single`/`Based` identifier |
| Nemotron blog props, after adjective fix | no entity false positive; `NEEDS_REVIEW` for insufficient runtime evidence |
| Nemotron ecommerce callers | correct list; `NEEDS_REVIEW` for insufficient runtime evidence |
| Ling 3.2 Flash | upstream HTTP 429, zero provider turns |

### Gate H — Engineering quality

The commands in section 2 are the gate. All passed on the final tree.

## 5. Real-repository measurements

Ecommerce (`00173f5f…`, clean):

| Metric | Value |
|---|---|
| Files / source files | 497 / 305 |
| Parsed Go files | 305 |
| Packages | 80 |
| Symbols | 3,440 |
| References | 9,090 |
| Calls | 2,412 |
| Test files / test declarations | 110 / 461 |
| Entry points | 2 |
| Scope / inventory / structural complete | false / true / false |
| `NewOrder` references / callers / importers / tests | 13 / 13 / 7 / 4 |
| Unsupported source | 57 `.gohtml`, 77 `.sql`, 16 `.tmpl` |

Blog (`fdb0b009…`, pre-existing `M .gitignore`, `M package-lock.json` preserved):

| Build | Files / source | Parsed | Symbols | References | Calls | Tests | Entry points |
|---|---|---|---|---|---|---|---|
| Portable | 111 / 77 | 0 | 0 | 0 | 0 | 0 | 2 |
| Multi-language | 111 / 77 | 77 | 529 | 817 | 1,035 | 0 files | 2 |

The zero test-file count is a complete inventory observation, not a claim that
the project has no tests.

## 6. Target integrity

| Repository | Commit | Working tree |
|---|---|---|
| ecommerce | `00173f5f751ce1abda6fe952f63e2dacd417e718` | clean |
| andev0x-blog | `fdb0b009a972a5ee0aa866d4de50149cc102eec8` | pre-existing `M .gitignore`, `M package-lock.json` |

No target was initialized, formatted, indexed into a durable store, or
modified by this closure. No commit, tag, push, or release was performed in
OHGO or either target.
