# Architecture decisions

## D1 — Fix the owning layer, not the symptom

- Completion defects were fixed in `internal/agent` and the contract vocabulary,
  not by changing a prompt or hardcoding one model's answer.
- The `using` defect was fixed in the classifier's clause model, not by
  disabling FUZZ routing.
- Token usage was persisted in the session store, not inferred from text.
- GRY freshness was added at the adapter boundary, not by making GRY mandatory
  or by hiding its limitations.

## D2 — Required facets are contract data

`contract.RequiredFacet` is extracted from the user's request in
`workspace.BuildIntentContract` and consumed by `agent.AssessRequiredFacets`.

Rejected alternative: a free-form "did the answer cover everything?" LLM judge.
That would add a second model, nondeterminism, and a new authority surface.

Rejected alternative: string-searching for “not verified”. That would accept a
partial list that never uses the word, and reject honest limitations.

## D3 — Bounded correction, not unbounded retry

The loop gets one `AnswerReview` correction. It reopens the constructed tool
set for that request only. Authority and grounding are unchanged.

Rejected alternative: automatically converting every review into another full
agent run. That would spend unbounded turns and could loop on the same claim.

Rejected alternative: rejecting every uncertain answer. The facet and
uncertainty checks are scoped to fields the user explicitly requested; a
documented static-analysis limitation is accepted when the requested list is
complete.

## D4 — Source truth over provider freshness claims

A provider's “current” label is not proof. The GRY adapter reads the current
workspace and verifies the coordinates it can. A missing/moved symbol is
`STALE`; an unreadable source is `UNAVAILABLE`; an unverifiable row stays
`UNKNOWN`.

Rejected alternative: trusting GRY's index timestamp. It does not prove every
indexed source file is current.

## D5 — Measured usage is run-scoped and additive

`session.MeasuredUsage(turns)` folds one run's provider-reported usage.
`Store.AddUsage` adds it atomically. The process-cumulative `UsageLog` is for
display, not for persistence, so a resumed session cannot double-count.

Rejected alternative: reconstructing tokens from transcript text for billing.
An estimate is not a measurement; it remains separately labelled.

## D6 — Runtime state is not project evidence

The request-side state block remains model-visible, because it is the runtime's
briefing. The final answer is sanitized before storage/publication. The
sanitizer removes only the state header, its field lines, and fixed directives;
it leaves fenced code and model-authored limitations untouched.

Rejected alternative: hiding the state block from the model entirely. The model
needs the evidence boundary and action surface to decide what to do next.

## D7 — Sibling Go modules: disclose, do not guess

A single nested module is fully resolved. Multiple sibling modules have no
single owner; choosing one by directory order would mis-resolve every import in
the others. The snapshot now reports `module_ambiguous` and
`module_candidates`, and the structure header states that cross-module imports
are not resolved.

Rejected alternative: merging sibling modules into one graph. That would invent
edges across unrelated module boundaries.

Full multi-module resolution (per-file module ownership, per-module import
maps, cross-module `replace` handling) is a release limitation, not a silently
claimed capability.

## D8 — README describes the build that exists

The README now states:

- the portable build is native Go only;
- the multi-language build adds pinned Tree-sitter adapters and requires cgo;
- `gopls` is a bounded Go semantic aid when installed;
- DAP is observation-only for an already-authorized stopped session;
- `ohgo update` refuses to replace a multi-language install with a portable
  release.

## D9 — One online boundary for live and completed text

The runtime state block is request-side orchestration. The public answer and the
live delta channel are the same publication viewed at different times, so they
share one sanitizer. The sanitizer is incremental: it holds only a line-start
prefix that could still become a runtime line.

Rejected alternative: sanitize only the completed answer. That leaves the live
stream as a second, weaker publication path and makes the two outputs drift.

Rejected alternative: buffer whole lines before publishing. That would delay
ordinary prose at every line boundary and weaken the streaming contract.

## D10 — Node freshness and edge freshness are separate facts

A node can be current while the relationship between two nodes is stale. The
adapter verifies endpoints cheaply and reports edges as `UNVERIFIED` unless a
current structural observation or source read binds both endpoints.

Rejected alternative: apply one observation-level freshness to every record.
That is the defect this decision closes.

Rejected alternative: verify every edge against source on every trace. That
turns a bounded query into a repository scan and still does not prove semantic
dispatch.

## D11 — Entity association before predicate enumeration

The claim audit may know only a finite predicate vocabulary, but it can always
ask whether an explicit code name was observed. That question is independent of
the verb and catches invented identifiers in novel sentence shapes.

Rejected alternative: keep adding behavior verbs. The vocabulary grows without
bound and still misses the next phrasing.

Rejected alternative: use an LLM judge for every claim. That adds a second
model, nondeterminism, cost, and a new authority surface to the completion
boundary.
