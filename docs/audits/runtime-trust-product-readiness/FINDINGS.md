# Findings

## A. Confirmed defects fixed in this audit

### OHGO-COMP-001 — Explicitly incomplete answers were completed

The previous audit confirmed that `EvaluateTaskCompletion` checked run shape and
generic claims but not the answer fields the user requested. The LongCat
evaluation exposed the same class: a detailed answer could say a required field
was unverified and still print `COMPLETED`.

Current implementation before the fix:

- No representation of “the user asked for callers/tests/call sites”.
- No comparison of an answer's partial list to the observed count.
- No check that a claimed code identifier was observed.

After the fix:

- `contract.RequiredFacet` and `workspace.BuildIntentContract` record explicit
  deliverables.
- `agent.AssessRequiredFacets` rejects missing, explicitly unverified, partial,
  or contradicted deliverable claims.
- `agent.codeClaimIssues` rejects an invented prop/field when the observed
  declaration contradicts it.
- A bounded correction turn asks the model to fix a rejection before the host
  reports `NEEDS_REVIEW`.

Evidence: `internal/agent/facets_test.go`, `code_claims_test.go`,
`review_test.go`; model runs R1, R4, R5 in `MODEL_EVALUATION.md`.

### OHGO-COMP-002 — Partial observed lists were accepted

Before the omission check, an answer that named 3 of 13 call sites read as a
complete caller answer. The new check compares distinct answer coordinates to
the trusted observed count. A stated total permits a bounded example list; a
silent subset does not.

### OHGO-COMP-003 — Invented code identifiers were accepted

A model could claim a component prop that the observed props declaration did
not contain. The new audit checks code-marked identifiers against source
observations and against the observed declaration body.

### OHGO-COMP-004 — No bounded completion correction

A review boundary was terminal for the run. The loop now has one bounded
correction request and one reopened action surface; a second failure returns to
the host.

### OHGO-CTRL-001 — Leading “Using read-only …” false-escalated to FUZZ

`headForms("use")` includes `using`; the leading participial clause was read as
an execution objective. The fix narrows only evidence/analysis modifiers and
keeps command/server/package-runner forms as execution.

### OHGO-ISO-001 — Runtime-state leakage into the final answer

A model can copy the request-side `# Runtime state (authoritative, not a user
request)` block or its `Sufficiency:` / `Available actions:` lines. The final
answer is sanitized before storage/publication. The sanitizer preserves
fenced code and model-authored limitations; an all-state answer becomes empty
and is reviewed.

### OHGO-ISO-002 — Runtime-state leakage into the live stream

The final-only sanitizer left the live delta channel raw. The new online
sanitizer is shared by `Loop.drain` and the completed answer, and
`TranscriptTurn` sanitizes every turn a host may publish or persist. It holds
only a line-start prefix that could still become a runtime line, so an
arbitrary delta split cannot leak a partial directive. See
`STREAMING_ISOLATION.md`.

### OHGO-GRY-005 — Current nodes made unverified edges look current

Endpoint verification was applied to the whole observation, so every
relationship record inherited `CURRENT`. The adapter now keeps node and edge
freshness separate and uses `UNVERIFIED` for a relationship whose endpoints are
current but whose edge was not checked. See `EDGE_FRESHNESS.md`.

### OHGO-COMP-005/006/007 — General claim grounding and real-model false positives

The closure phase added an entity-to-evidence association that is independent
of the predicate vocabulary, an absence contradiction for observed entities,
and two calibration fixes found by real models: a prose adjective is no longer
an invented member, and an explicit evidence gap is no longer a repository
absence. See `CLAIM_GROUNDING.md`.

### OHGO-GRY-004 — GRY trace/search freshness was unknown and unverified

The payloads have no generation/freshness fields. The adapter now verifies the
file/line/symbol coordinates against the current workspace, adds a distinct
`UNAVAILABLE` state, and reports `STALE` when the symbol no longer exists. The
router downgrades the combined observation. Endpoints are verified; edges are
not overstated as proven.

### OHGO-USE-001 — Headless token usage was not persisted

The session store now has measured usage columns and an additive `AddUsage`.
Headless, REPL, and TUI runs persist one run's measured turns. An unmeasured run
stays unknown; a pre-provider refusal has zero measured turns, which is
different from “zero tokens spent” only when a provider actually reported zero.

### OHGO-DOC-002 — README described a build that did not exist

README now distinguishes the portable and multi-language builds, describes the
bounded `gopls` path, states that DAP is observation-only, and documents the
multi-language update refusal.

### OHGO-STR-007 — Sibling Go modules were silently ambiguous

The snapshot now reports `module_ambiguous` and `module_candidates`, and the
structure header says cross-module imports are not resolved. Full sibling
module resolution remains a documented limitation.

## B. Re-validated prior fixes

| Prior fix | Current evidence |
|---|---|
| Nested Go module discovery | Ecommerce module is `…/backend`; 305/305 Go parsed. |
| `NewOrder` references/callers/importers/tests | 13 / 13 / 7 / 4, source-confirmed. |
| Focus strategy and reference counting | Full suite; `internal/intelligence/structural` tests pass. |
| Forward callees | `TestImpactReportsCallees` passes. |
| TS path aliases | Full multi-language suite passes. |
| JSX component references | Full multi-language suite passes. |
| Type references | Full multi-language suite passes. |
| Non-Go importers | Blog `TerminalBlog` importer observed. |
| Generated `.next` exclusion | Full suite; blog inventory is 111 files, no `.next` flood. |
| GRY trace includes tests | `TestTraceIncludesTestCallSites` passes. |
| Exact-name locate | `TestResolvePreservesAllCandidates` and live evaluation pass. |
| Native-first merge | Router tests pass; no external request for a sufficient native step. |
| Portable/multilang doctor | 1 adapter vs 11 adapters. |
| Update downgrade refusal | `TestUpdateRefusesPortableOverMultilang` passes. |
| `make bench` package path | Target exists and runs. |

## C. Remaining limitations

1. **Multiple sibling Go modules**: disclosed ambiguity; cross-module import
   resolution is not implemented.
2. **Non-Go semantics**: Tree-sitter is syntax-level; no TypeScript/Java/etc.
   language server.
3. **GRY edge verification**: relationships are honestly `UNVERIFIED`; per-edge
   verification is not implemented.
4. **Claim grounding**: general for explicit code entities but not a semantic
   parser. A plausible claim outside the audited shapes can pass.
5. **Authority classifier conservatism**: ambiguous prose can under-authorize
   (READ instead of FUZZ); it does not over-authorize.
6. **Model coverage**: cohere/north-mini unavailable; Ling 3.2 Flash 429;
   LongCat/Nemotron samples are small.
7. **Provider-dependent token accounting**: a provider that reports no usage is
   represented as unknown, not estimated.
