# Final report — runtime trust and product readiness

Date: 2026-11-20
OHGO base: `b7e65399e8fede38717de561263b8a809a6c2614`
Targets: ecommerce `00173f5f…`, andev0x-blog `fdb0b009…`

## 1. Readiness status

**CONDITIONALLY READY**

No critical authority, data-integrity, or trust defect found in the exercised
paths remains unfixed. Completion, authority, evidence freshness, runtime-state
isolation, usage persistence, and documentation now have tested behavior. The
remaining limits are explicit capability and provider-coverage gaps:

- multiple sibling Go modules are disclosed but not cross-resolved;
- non-Go intelligence is syntax-level;
- GRY endpoints are source-verified but edges cannot be proven current;
- the completion claim vocabulary is finite;
- cohere/north-mini was absent from the live free roster, and the LongCat
  sample is one successful run after prior 429s.

## 2. What was fixed

### Completion integrity

- Added a formal `RequiredFacet` contract extracted from the user's request.
- Rejects missing, explicitly unverified, partial, truncated, and contradicted
  deliverables.
- Rejects a partial observed list (for example 3 named of 13 observed sites)
  unless the answer states the total.
- Rejects invented code identifiers when the observed declaration contradicts
  them.
- Added one bounded correction turn with a one-turn reopened action surface.
- The correction cannot complete the task, widen authority, or bypass grounding.

### Authority integrity

- Fixed the leading participial “Using read-only structural evidence, report …”
  false escalation to FUZZ.
- Kept command/server/package-runner `using` clauses as execution evidence.
- Verified model text and tool output cannot move the live gate.
- Verified ASK refuses mutation/investigation before the provider; FUZZ cannot
  mutate; BUILD still mutates and verifies.

### Runtime-state isolation

- Final answers are sanitized before storage/publication.
- Copied `# Runtime state …` blocks, `Sufficiency:`/`Available actions:` field
  lines, and fixed “no tool” directives are removed.
- Fenced code and model-authored limitations are preserved.
- An answer consisting only of copied state becomes empty and is reviewed.

### GRY evidence freshness

- Added `FreshnessUnavailable`.
- Trace, search, and context coordinates are verified against current source.
- Missing/moved source is `STALE`; unreadable/outside-root is `UNAVAILABLE`;
  unverifiable is `UNKNOWN`.
- The router downgrades stale external evidence; it is never presented as
  current.

### Headless token usage

- Added measured session columns and additive `Store.AddUsage`.
- Headless, REPL, and TUI runs persist one run's measured usage.
- Session listings prefer measured totals over text estimates.
- A provider that reports nothing stays unknown; a pre-provider refusal has
  zero measured turns.

### Documentation and build truthfulness

- README now describes the portable vs multi-language build, bounded `gopls`,
  DAP observation-only status, and the multi-language update refusal.
- `doctor` reports 1 adapter for portable and 11 for multi-language.
- `ohgo update` refuses to replace a multi-language install with a portable
  release.

### Sibling Go modules

- The snapshot now reports `module_ambiguous` and `module_candidates`.
- The structure header states that cross-module imports are not resolved.
- Full sibling-module resolution remains a documented release limitation.

## 3. What was newly discovered

- Partial observed lists were accepted even after the facet contract existed;
  a stated total is now required for a bounded summary.
- Invented code identifiers were not checked against the observed declaration.
- Completion had no bounded correction path.
- Runtime-state text could be published verbatim.
- GRY trace/search had no source verification.
- Headless usage was not persisted at all.
- Sibling Go modules were silently ambiguous.
- README described an older LSP/DAP and static-build posture.

## 4. Root causes

- Completion was a run-shape policy, not a deliverable contract.
- The authority classifier derived `using` from the head verb `use` without
  distinguishing a participial evidence clause.
- Runtime state and public answer shared one text channel with no output
  boundary.
- The GRY adapter trusted payload absence of metadata and did not consult the
  current workspace.
- Headless execution never attached the usage ledger that the interactive path
  already had.
- The snapshot's module discovery returned one identity or empty, with no way
  to say “several owners, no selection.”
- README was not updated when the bounded semantic/runtime providers landed.

## 5. Verification actually executed

| Check | Result |
|---|---|
| `go build ./...` | pass |
| `go test ./...` (CGO=1) | pass |
| `CGO_ENABLED=0 go test ./...` | pass |
| `go test -race ./...` | pass |
| `go vet ./...` | pass |
| `gofmt -l .` | empty |
| `golangci-lint run ./...` | 0 issues |
| `make bench` | pass |
| `make test-portable` | pass |
| `make test-multilang` | pass |
| `make build` | pass; portable hash `0dec6d53…` |
| `make build-multilang` | pass; multi-language hash `7885757a…` |
| `./ohgo doctor` | 1 adapter (`go`) |
| `./ohgo-multilang doctor` | 11 adapters |

## 6. Hosted free-model results

Only hosted zero-priced models were used. No local model and no paid fallback.

| Model | Result |
|---|---|
| `opencode-go/beam-free` | Blog overview: one overclaim correctly reviewed; a second run completed with a correct overview. Ecommerce 13-caller analysis completed after correction. |
| `openrouter/baidu/ernie-5.5-flash-preview:free` | Blog props answer correct (`posts?: Post[]`). Ecommerce caller list complete; a citation to an unopened file was correctly reviewed. |
| `opencode-go/longcat-2.5-free` | Blog investigation: a careful run completed; a later run that claimed “no tests” and “no ESLint config” was correctly reviewed. One successful run after prior 429s. |
| `openrouter/cohere/north-mini-code:free` | Not in the live roster at test time; no sample. Recorded as provider availability, not an OHGO defect. |

Measured usage (provider-reported, persisted):

- beam: 3–6 turns, 12,436–36,498 input, 594–2,379 output per run.
- ERNIE: 5–8 turns, 25,126–48,811 input, 682–1,876 output per run.
- LongCat: 3 turns, 18,513 input, 1,389 output.

## 7. Portable and multi-language builds

- **Portable (`CGO_ENABLED=0`)**: native Go Structure Core; 1 adapter; `.ts`,
  `.tsx`, `.mjs`, etc. are `UNSUPPORTED`. Blog: 111 files / 77 source / 0
  parsed.
- **Multi-language (`CGO_ENABLED=1`)**: 11 adapters; blog 77/77 parsed, 529
  symbols, 817 references, 1,035 calls, 2 entrypoints.
- Ecommerce: module `…/backend`, 305/305 Go parsed, 80 packages, 3,440
  symbols, 9,090 references, 2,412 calls, `NewOrder` 13 refs / 13 callers / 7
  importers / 4 tests.
- These match the prior audit's post-fix baseline; no regression was found.

## 8. Remaining limitations

1. Completion is deterministic but not exhaustive: an unsupported claim outside
   the audited vocabulary can pass.
2. Multiple sibling Go modules are disclosed but not cross-resolved.
3. Non-Go languages remain syntax-level Tree-sitter facts.
4. GRY endpoints are source-verified; edges cannot be proven current from the
   payload.
5. Live streaming can briefly show runtime text before the completed answer is
   sanitized; the persisted answer and completion input are sanitized.
6. The authority classifier can under-authorize ambiguous prose; it does not
   over-authorize.
7. The free-model evaluation is a behavior sample, not a benchmark, and has a
   provider availability gap.

## 9. Protected repository integrity

- Ecommerce: `00173f5f…`, working tree clean, untouched.
- Blog: `fdb0b009…`, pre-existing `M .gitignore` and `M package-lock.json`
  preserved, untouched.
- No commit, push, tag, or release was performed in OHGO or either target.
- No target was initialized, formatted, or used as a durable session store.

## 10. Changed files

Source changes (25 tracked files) are grouped by layer:

- Control/contract: `internal/control/contract/contract.go`,
  `internal/control/workspace/{contract,intent}.go`.
- Agent: `internal/agent/{claims,grounding,loop}.go` plus new
  `facets.go`, `code_claims.go`, `review.go`, `presentation.go`.
- Session/usage: `internal/continuity/session/{session,usage}.go`,
  `cmd/ohgo/{session,run,repl,outcome}.go`,
  `internal/tui/{app,picker,sessionview}.go`.
- Evidence/GRY: `internal/intelligence/evidence/evidence.go`,
  `internal/intelligence/gry/{observe,parse,source_verify}.go`.
- Structure: `internal/intelligence/ast/{index,model}.go`,
  `internal/execution/tools/structure.go`.
- Prompt/build: `internal/prompt/prompt.go`, `cmd/ohgo/build.go`.
- Docs: `README.md`, `.gitignore`.

New tests: `internal/agent/{facets,code_claims,review,presentation,test_absence_contract}_test.go`,
`internal/control/workspace/{facets,authority_regression}_test.go`,
`internal/intelligence/gry/source_verify_test.go`,
`internal/intelligence/ast/{sibling_module,target_audit}_test.go`,
`internal/continuity/session/usage_test.go`,
`cmd/ohgo/{authority_injection,session_usage}_test.go`.

## 11. Audit artifacts

`docs/audits/runtime-trust-product-readiness/`:

- `README.md`
- `FINDINGS.md`
- `BUG_LEDGER.md`
- `ARCHITECTURE_DECISIONS.md`
- `COMPLETION_POLICY.md`
- `AUTHORITY_MODEL.md`
- `EVIDENCE_PROVENANCE.md`
- `MODEL_EVALUATION.md`
- `MEASUREMENTS.md`
- `PRODUCT_READINESS.md`
- `REGRESSION_TESTS.md`
- `FINAL_REPORT.md`

## 12. Gates left unverified

None of the mandatory engineering gates is unverified. The only intentionally
incomplete verification is the free-model matrix: cohere/north-mini was absent
from the live roster, and LongCat has one successful sample after prior 429s.
That is recorded under Gate G as a provider-dependent gap, not hidden as a
pass.
