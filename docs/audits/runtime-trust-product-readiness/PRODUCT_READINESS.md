# Product readiness

Verdict: **CONDITIONALLY READY**

Closure result: the P0 public-stream boundary and the P1 edge-representation
and entity-association items are closed. No critical authority, data-integrity,
or trust defect found in the exercised paths remains unfixed. The remaining
items are documented capability limits and provider-dependent verification
gaps, not silent false-success paths.

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
  bounded overview completed, and a later “no tests” overclaim was correctly
  returned as `NEEDS_REVIEW`.
- A general entity-to-evidence association now reviews an explicit code span
  that no observation contains, even when the sentence uses a predicate outside
  the vocabulary.
- A negative existential claim about an observed entity is reviewed as a
  contradiction.
- Real-model false positives were removed: a prose adjective is no longer an
  invented member, and an explicit evidence gap is no longer a repository
  absence.

Residual limit: the association is general for explicit code entities, but the
claim audit is still not a semantic parser. A plausible unsupported claim that
names no code entity, or names an observed entity while asserting a behavior the
observation does not establish, can still pass.

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

**Status: PASS. Node and edge freshness are separate.**

Evidence:

- Native snapshots expose generation, snapshot identity, scope/inventory/
  structural completeness, exclusions, and unsupported source independently.
- GRY project identity is resolved by root, not name.
- Native-first routing avoids external calls when native is sufficient.
- Trace/search coordinates are verified against current source; missing/moved
  source is `STALE`, unreadable/outside-root is `UNAVAILABLE`.
- A current endpoint set makes a relationship `UNVERIFIED`, never `CURRENT`.
  The state is carried on the edge fact and stated once in the issues.
- Stale external evidence downgrades the combined observation.
- The structure header discloses sibling-module ambiguity.

Residual limit: per-edge verification is not implemented. The representation
is honest, not a proof of every current relationship.

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
- Closure-phase samples added LongCat (blog review, ecommerce completion),
  Nemotron (blog and ecommerce), and Ling 3.2 Flash (429).
- Cohere north-mini was absent from the live OpenRouter roster; recorded, not
  treated as an OHGO defect.
- Ling 3.2 Flash returned an upstream 429 with zero provider turns and zero
  measured tokens.
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
- `make build` (portable hash `c1e6ef06…`)
- `make build-multilang` (multi-language hash `a1d30334…`)

Exact counts and per-gate evidence are in `VERIFICATION_EVIDENCE.md`; the
file-by-file review is in `DIFF_REVIEW.md`.

## Why not PRODUCT READY

The product is not declared fully ready because:

1. Claim grounding is general for explicit code entities but still not a
   semantic parser; an unsupported claim outside the audited shapes can pass.
2. Multi-module Go cross-module resolution remains unimplemented.
3. No non-Go semantic resolution exists beyond syntax-level Tree-sitter facts.
4. GRY relationships are honestly `UNVERIFIED`, but per-edge verification is
   not implemented.
5. The free-model matrix has a provider availability gap (cohere absent, Ling
   429) and a small sample per model.

None of these is a silent authority or data-integrity failure. They are explicit
limits that a user can see and reason about. The public stream no longer leaks
runtime directives, and an unverified edge is no longer presented as current.

## Protected repository integrity

- Ecommerce: `00173f5f751ce1abda6fe952f63e2dacd417e718`; no changes made by
  this audit.
- Blog: `fdb0b009a972a5ee0aa866d4de50149cc102eec8`; pre-existing
  `M .gitignore`, `M package-lock.json` preserved; no changes made by this
  audit.
- No commit, tag, push, or release was performed in OHGO or either target.
