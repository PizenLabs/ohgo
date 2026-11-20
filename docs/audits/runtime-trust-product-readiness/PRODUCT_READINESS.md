# Product readiness

Verdict: **CONDITIONALLY READY**

No critical authority, data-integrity, or trust defect found in the exercised
paths remains unfixed. The remaining items are documented capability limits and
provider-dependent verification gaps, not silent false-success paths.

## Gate A — Completion integrity

**Status: PASS for the tested contract; bounded vocabulary.**

Evidence:

- Required facets are extracted from the user's own request and enforced.
- Missing, explicitly unverified, partial, and contradicted deliverables are
  reviewed.
- Invented code identifiers are checked against observed declarations.
- One bounded correction turn is available; a second failure is returned to the
  host as `NEEDS_REVIEW`.
- The LongCat blog regression is satisfied in both directions: a careful
  bounded overview completed, and a later “no tests”/“no ESLint config”
  overclaim was correctly returned as `NEEDS_REVIEW`.

Residual limit: the claim audit is a finite deterministic vocabulary. A
plausible unsupported claim outside the audited shapes can still pass.

## Gate B — Authority integrity

**Status: PASS.**

Evidence:

- Model text claiming authorization cannot move the live gate.
- Tool output cannot grant the next mutation.
- ASK refuses mutation and investigation before the provider is called.
- FUZZ can execute/probe but cannot mutate.
- BUILD executes and verifies explicit mutations.
- The leading “Using read-only …” read request now stays in ASK; command forms
  still require execution authority.

Residual limit: ambiguous prose may under-authorize (READ instead of FUZZ).
That narrows capability and is documented.

## Gate C — Evidence integrity

**Status: PASS with a documented edge-freshness limit.**

Evidence:

- Native snapshots expose generation, snapshot identity, scope/inventory/
  structural completeness, exclusions, and unsupported source independently.
- GRY project identity is resolved by root, not name.
- Native-first routing avoids external calls when native is sufficient.
- Trace/search coordinates are verified against current source; missing/moved
  source is `STALE`, unreadable/outside-root is `UNAVAILABLE`.
- Stale external evidence downgrades the combined observation.
- The structure header discloses sibling-module ambiguity.

Residual limit: endpoint verification does not prove every edge is current.

## Gate D — Operational integrity

**Status: PASS.**

Evidence:

- Headless, REPL, and TUI runs persist measured token usage.
- Missing provider usage stays unknown; a pre-provider refusal has zero
  measured turns.
- Retry/stream/cancellation paths retain their existing bounded recovery.
- A resource stop is `PAUSED`; a failure is `FAILED`; cancellation is
  `CANCELLED`.
- A provider 429 is reported as a failure, not completion.

## Gate E — Regression integrity

**Status: PASS.**

- Nested Go module, references/callers, forward callees, TS aliases, JSX/type
  references, non-Go importers, generated-import exclusion, GRY trace tests,
  exact-name lookup, native-first merge, doctor reporting, update refusal, and
  `make bench` all pass in the full suite.
- Portable: 1 adapter (`go`). Multi-language: 11 adapters.
- Ecommerce and blog measurements match the prior audit's post-fix baseline.

## Gate F — Product and documentation integrity

**Status: PASS.**

- README documents the portable/multi-language split, bounded `gopls`, DAP
  observation-only status, and the multi-language update refusal.
- Installation, update, and doctor behavior agree with the implementation.
- Sibling-module and non-Go semantic limits are documented.

## Gate G — Real-world model verification

**Status: PASS with provider availability gaps.**

- `beam-free`, ERNIE, and LongCat were exercised on both repositories.
- Cohere north-mini was absent from the live OpenRouter roster; recorded, not
  treated as an OHGO defect.
- Prior LongCat 429s are recorded; one successful run is not a reliability
  verdict.
- No local model and no paid fallback was used.
- Measured token usage is recorded for every completed run.

## Gate H — Engineering quality

**Status: PASS.**

Executed:

- `go build ./...`
- `go test ./...` (CGO=1)
- `CGO_ENABLED=0 go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `gofmt -l .` (empty)
- `golangci-lint run ./...` (0 issues)
- `make bench`
- `make build`
- `make build-multilang`

## Why not PRODUCT READY

The product is not declared fully ready because:

1. Completion is deterministic but not complete: a plausible unsupported claim
   outside the audited vocabulary can still pass.
2. Multi-module Go cross-module resolution remains unimplemented.
3. No non-Go semantic resolution exists beyond syntax-level Tree-sitter facts.
4. GRY edge freshness cannot be proven from the interface payload.
5. The free-model matrix has a provider availability gap (cohere absent) and a
   small LongCat sample.

None of these is a silent authority or data-integrity failure. They are explicit
limits that a user can see and reason about.

## Protected repository integrity

- Ecommerce: `00173f5f751ce1abda6fe952f63e2dacd417e718`; no changes made by
  this audit.
- Blog: `fdb0b009a972a5ee0aa866d4de50149cc102eec8`; pre-existing
  `M .gitignore`, `M package-lock.json` preserved; no changes made by this
  audit.
- No commit, tag, push, or release was performed in OHGO or either target.
