# Unified Intelligence

`intelligence/*` is one subsystem with four observation providers and two
shared layers. Providers observe; the evidence engine preserves what they
observed; the planner decides which provider to use next; the control plane
authorizes; the execution plane acts. No provider owns authority, and none may
become a second mutation or process-execution path.

```
                 control plane (authority, budget, task state)
                                 |
                                 v
                        intelligence/planner
                    (selection, cost, stop reason)
                                 |
        +--------------+---------+---------+--------------+
        v              v                   v              v
   structural      retrieval           semantic        runtime
   (ast + graph)   (Okapi BM25F)       (LSP)           (DAP)
        |              |                   |              |
        +--------------+---------+---------+--------------+
                                 v
                      intelligence/evidence
                 (record, provenance, agreement,
                  contradiction, freshness)
                                 |
                                 v
                 structure / retrieve tools (model-facing)
                                 |
                                 v
                 authorized action (execution plane)
```

## 1. Ownership

| Package | Owns | Must not |
|---|---|---|
| `intelligence/ast` | source observation: snapshot, parsing, symbols, imports, references, calls | interpret architecture, own authority |
| `intelligence/structural` | filesystem/AST/graph interpretation, structural evidence, structural planning | parse source, build a second snapshot |
| `intelligence/retrieval` | lexical ranking (Okapi BM25F), incremental postings, retrieval cache | claim semantic resolution or structural facts |
| `intelligence/semantic` | LSP transport, server lifecycle, semantic requests, diagnostics | apply edits, own symbol identity, bypass authority |
| `intelligence/runtime` | DAP transport, session lifecycle, runtime observation | own process authorization, replace the execution plane |
| `intelligence/evidence` | shared state vocabulary, records, correlation, contradictions | observe, rank, or authorize |
| `intelligence/planner` | provider selection, cost/gain, stopping reasons, closed-loop reassessment | start a provider, grant authority, execute work |
| `intelligence/investigate` | adaptive execution: shared context, evidence convergence, correlation, bounded result | start a provider outside its contract, cache runtime evidence as source truth, expose raw payloads |
| `intelligence/gry` | optional external structural transport, bounded normalization, explicit outcome classification | index a repository, own or mirror a graph, authorize, mutate, complete, or bypass the shared budget |
| `intelligence/wire` | Content-Length JSON framing shared by LSP and DAP | protocol schemas or provider policy |

`internal/architecture` enforces that every package under `intelligence/` has no
dependency on `agent`, `control/*`, `execution/*`, `continuity/*`,
`provider/*`, or `tui`. LSP/DAP process launch therefore lives behind a
configuration-owned spec and an authorization callback supplied by the caller,
never behind an import of the authority system.

## 2. Provider contract

Every provider exposes the same shape, adapted to its domain rather than forced
into one interface:

- identity and capability set;
- availability (a missing binary or unadvertised LSP capability is a truthful
  "unsupported", never simulated);
- bounded work (context deadline, size caps, concurrency caps);
- an explicit operation `Outcome` (`SUCCESS`, `PARTIAL`, `STALE`, `AMBIGUOUS`,
  `UNRESOLVED`, `UNAVAILABLE`, `NOT_INDEXED`, `FAILED`), separate from the
  evidence state;
- a `Source` naming the mechanism that answered the step, separate from the
  capability identity;
- observations with provenance and freshness;
- explicit issues and degradations;
- deterministic ordering where the algorithm allows it.

The dimensions are deliberately separate:

| Dimension | Question | Never derived from |
|---|---|---|
| relevance | how well does this match the query? | confidence |
| confidence | how strongly is this observation supported? | rank score |
| coverage | how much of the system was observed? | success of one request |
| freshness | does this describe the current snapshot? | confidence |
| proof | is there an explicit verification? | a provider returning successfully |

## 3. Okapi BM25 retrieval

`intelligence/retrieval` implements the standard Okapi BM25 ranking function:

\[
BM25(q,d)=\sum_{t\in q} IDF(t)\cdot
\frac{f(t,d)(k_1+1)}{f(t,d)+k_1\left(1-b+b\frac{|d|}{avgdl}\right)}
\]

with the Lucene/BM25 positive IDF form
`ln(1 + (N - df + 0.5)/(df + 0.5))`.

Baseline parameters: `k1 = 1.2`, `b = 0.75`. They are configuration, not a
claim of universal optimality, and changing them changes the persisted schema
identity.

Field-aware scoring (BM25F) normalizes each field's term frequency by that
field's length and sums weighted contributions:

| Field | Weight | Source |
|---|---:|---|
| path | 1.5 | directory segments, stem, identifier parts |
| identifier | 1.0 | lexical identifier vocabulary of the file |
| comment | 0.4 | prose from line and block comments |

Tokenization is language-aware without being a parser: `AuthMiddleware` emits
`authmiddleware`, `auth`, `middleware`; `user_session` emits `user`,
`session`; `payment.Process` emits the segments and the dotted chain. String and
character literals are skipped; comments contribute prose. Tests pin these
rules.

Hybrid reranking adds a bounded multiplier on top of BM25:

```
score = bm25 * (1 + 0.35*exact_identifier + 0.20*path_match + 0.20*proximity)
        * (test ? 0.85 : 1) * (generated ? 0.50 : 1)
```

The exact-identifier term dominates the graph-proximity term, so a globally
central file cannot outrank the exact answer. Proximity is an optional caller
signal, not a centrality substitute.

The index stores per-document term frequencies only; source text is dropped
after tokenization. Updates remove and rebuild only the changed document's
postings. Persistence is a versioned envelope (`retrieval/v1`, root,
configuration, per-file size+mtime signatures) written atomically; a
configuration or schema mismatch is a miss, never a partially trusted index.
Corruption is surfaced and recovered by rebuilding.

Measured on this repository (923 indexed documents, 11.8 MB tokenized):

| Operation | Measured |
|---|---:|
| cold build (miss) | 565 ms |
| warm load from cache | 439 ms (decode + stat validation) |
| in-memory refresh (tool path) | 51 ms |
| query p50 (3-term) | 4–92 µs |
| index size | 37k terms, 288k postings |

A hand-labeled 12-document corpus with seven queries scores MRR 1.00 and
Recall@5 7/7; a path-substring baseline scores lower. The corpus is synthetic by
design: it is ground truth for those queries, not a claim about every
repository.

## 4. Semantic intelligence (LSP)

Lifecycle: lazy process start on first use for a language; `initialize` with
capability negotiation; `initialized`; document open/change/close; bounded
requests; `shutdown`; `exit`; reap with a kill fallback.

Negotiated capabilities: definition, references, document symbols, workspace
symbols, hover, call hierarchy, pull diagnostics, and push
`textDocument/publishDiagnostics`. The client checks the negotiated set before
every request and returns `ErrUnsupportedCapability` rather than simulating a
feature.

Safety:

- no rename, code action, `workspace/applyEdit`, or
  `workspace/executeCommand` surface exists;
- a server request for `workspace/applyEdit` is answered with an error, so the
  edit is discarded; `workspace/configuration` is answered with empty settings;
  reverse requests cannot create a terminal or process execution path;
- server commands come only from the configured roster; nothing is installed or
  downloaded automatically.

Manager guarantees: deduplication by server name and workspace, lazy start,
bounded server count, per-request timeout with `$/cancelRequest`, crash
surfacing, at most two restarts, and shutdown of every started server.

Validation: fake-server integration tests cover framing, negotiation, document
sync, diagnostics, timeout cancellation, crash bounds, dedupe, and the
edit-refusal contract. A real `gopls` test resolves `service.Serve()` to
`service/service.go` when `gopls` is installed (skipped otherwise).

## 5. Runtime intelligence (DAP)

Lifecycle: `initialize` (capability negotiation) → `launch`/`attach` →
`initialized` → `setBreakpoints` → `configurationDone` → running/stopped →
`disconnect`/`terminate` → reap.

Explicit session states:

```
CREATED → INITIALIZING → CONFIGURING → RUNNING ⇄ STOPPED
                                   ↘ TERMINATING → TERMINATED
        any failure ─────────────────────────────→ FAILED
```

Observed capabilities: breakpoints, threads, stack traces, scopes, variables,
continue/next/step-in/step-out/pause, exception info, evaluate, terminate, and
disconnect. Capabilities are negotiated; `exceptionInfo` and `terminate` return
`ErrUnsupported` when not advertised.

Authorization: `launch`, `attach`, `evaluate`, and `terminate` go through an
injected `Authorizer`; with no authorizer configured those actions are denied.
Observation-only requests after a session exists do not prompt. Evaluation is
bounded to 2048 characters and never silently enabled.

Cleanup: `disconnect` with `terminateDebuggee: true`, then a grace period, then
a process-group kill on Unix so a hung adapter cannot orphan the debuggee.
Reverse requests (`runInTerminal`, `startDebugging`) are refused, so runtime
intelligence cannot create an execution path outside the execution plane.
Breakpoint sources outside the session root are rejected before any request.

Validation: a fake adapter covers the full lifecycle, breakpoint verification,
stack/scopes/variables, stepping, evaluation authorization, session bounds,
launch timeout, and reaping an adapter that ignores disconnect. A real `dlv`
integration test is not yet wired; the DAP transport is protocol-standard but
has not been exercised against a real adapter in this change.

## 6. Evidence engine

`evidence.Record` carries: identity, kind, provider, source, subject,
relation, object, state, confidence, freshness, scope, provenance refs,
snapshot id, observation time, and a note. Identity is content-addressed, so an
identical observation from the same source deduplicates while the same fact
from a different source remains separate.

Correlation groups records by `(subject, relation)`:

- agreement keeps every record and reports the distinct sources/providers;
- disagreement lists every object and all supporting records, with a note
  naming what must be checked before choosing (snapshot freshness, build
  configuration, conditional compilation, generated code, dynamic dispatch);
- a claim's state is the weakest supporting state and its confidence is the
  minimum, so a strong observation cannot hide a weak one.

`VERIFIED` is reachable only through `evidence.Prove`. Constructing a literal
with `State: Verified` is rejected by the engine. Structural analysis now uses
the same state type (`structural.EvidenceState` is an alias), so the vocabulary
cannot drift.

What is not done: semantic, runtime, and retrieval observations are not yet fed
into one engine instance at tool-call time. The record model, correlation, and
contradiction rules are implemented and tested; the model-facing tools still
publish their own evidence detail.

## 7. Planner

`planner.Build(intent, availability, budget)` is deterministic:

1. mandatory steps in table order when available;
2. optional steps ranked by gain per cost unit, ties by provider name;
3. stop at the intent's confidence target, the step budget, or partial
   availability;
4. record every selected step (strategy, reason, cost band, expected
   confidence) and every skip (unavailable, budget, target met, or "no
   authorized runtime session").

Runtime is never planned from intent alone: availability must report an
authorized session. Provider availability is the caller's observation, not a
guess.

What is not done: the planner is not yet the orchestrator behind a model-facing
capability. `structure` still uses its structural-specific plan, `retrieve`
executes retrieval directly, and no tool executes a cross-provider plan. This is
the largest remaining integration gap.

## 8. Cache and freshness

| Cache | Identity | Invalidation |
|---|---|---|
| AST snapshot | tree stat signatures + module | change detected on next query |
| structural analysis | schema, analyzer, adapters, root, snapshot, request | any mismatch |
| retrieval index | schema, root, options; per-doc size+mtime | changed/deleted files, config mismatch, corruption |
| LSP state | process lifetime | process exit; no cross-process reuse |
| DAP state | session lifetime | session termination; never reused |

Cross-process reuse is demonstrated for structural analysis by a test that runs
two child processes sharing one cache directory: the second hits, and a changed
tree misses. Retrieval persistence is covered by save/load, configuration
mismatch, corruption recovery, and a restart test.

Runtime evidence is session-bound by construction: a stack trace or variable
value is never cached as a fact about a new execution.

## 9. Failure and degradation

| Condition | Behavior |
|---|---|
| LSP binary missing | semantic unavailable; structural + retrieval continue; no simulated capability |
| LSP capability absent | `ErrUnsupportedCapability`, no request |
| LSP crash | pending requests fail; manager restarts at most twice, then reports |
| LSP timeout | `$/cancelRequest`, `ErrTimeout` |
| DAP unavailable/denied | runtime step skipped with the authorization reason |
| DAP crash | pending requests fail; session becomes `FAILED`; watcher records exit |
| DAP ignores disconnect | grace period then process-group kill |
| retrieval cache corrupt | `CacheInvalid`, rebuild, issue reported |
| no retrieval term matches | valid empty page, marked useless for compaction |
| graph limit exceeded | structural planner records the bound instead of running |

A valid empty result is never reported as an analysis failure, and an
unavailable provider is never reported as "the fact is absent".

## 10. Authority

- ASK/FUZZ/BUILD semantics are unchanged. Intelligence is read-only.
- The `retrieve` tool is `TierRead`.
- LSP cannot edit; DAP launch/attach/evaluate/terminate require an authorizer
  and are not exposed to the model yet.
- No intelligence package imports the authority system; the execution plane
  still owns process launch and source mutation.
- `VERIFIED` remains the only proof state and cannot be manufactured by a
  provider result.

## 11. Orchestration

`intelligence/investigate` is the execution layer the planner was missing. One
run owns:

- a typed `Request` (question, intent, focus, budget) and one normalized budget;
- one shared `evidence.Engine` that every provider publishes into;
- one `Meter` charged by language-server and debug-adapter requests;
- one plan trace: selected steps with reasons, unselected providers with
  reasons, observed confidence/coverage, and the stop reason.

The loop is `PLAN → EXECUTE ONE STEP → OBSERVE → CORRELATE → REASSESS`. The
planner is re-entered after every step with the *observed* confidence and the
set of executed providers, so a provider that produced little evidence does not
count as a completed estimate. Executed providers are never selected again.

Provider adapters:

| Adapter | Source | Availability rule |
|---|---|---|
| structural | shared `ast.Index` + analysis cache | source configured |
| retrieval | shared `retrieval.Workspace` | workspace and source configured |
| semantic | LSP manager (starts lazily, closes after the step) | a server spec exists (composition checks the binary); project-root discovery is deferred to the first semantic step, never run during startup |
| runtime | already-authorized `runtime.Session` | session alive and STOPPED; never launched here |

The `investigate` tool is the model-facing entrypoint. It returns bounded
facts, provider states, contradictions, issues, confidence, coverage, and the
stopping reason. It never returns raw graphs, LSP/DAP payloads, or indexes.

## 12. External structural provider (optional)

`intelligence/investigate.StructuralRouter` is the structural capability
boundary. It composes two implementations of the same step:

- the native engine in `intelligence/structural`, which is always the default
  and the fallback;
- an optional adapter in `intelligence/gry`, which answers the target-shaped,
  architecture, impact, and verification questions where an indexed repository
  graph is materially better than re-deriving one.

The adapter is not a provider identity in the planner. It reports
`planner.ProviderStructural`, sets `Observation.Source` to `gry`, and publishes
`evidence.ProviderStructural` records with `evidence.SourceGRY`. That separation
keeps "what was observed" distinct from "which mechanism observed it".

### 12.1 Routing

Routing follows the information need, never the role:

| Information need | Native path | External path |
|---|---|---|
| project orientation, filesystem hierarchy | yes; no parsing, no subprocess | not called |
| architecture shape | fallback | `architecture` (bounded internal packages, languages, entrypoints) |
| locate with a target | fallback | `search` (all ranked candidates preserved) |
| flow with a target | fallback | `trace` (both directions) |
| impact with a target | fallback | `trace` (inbound); without a target, `changes` |
| debug with a target | fallback | `context` (task, target, tests, unknowns) |
| verification | fallback | `changes --scope tests` |

A step with no target for a locate/flow/debug need stays native. This is the
"existing or cheap local evidence first" rule: an external call is not
automatically an optimization.

The `investigate` tool exposes an opt-in `provider` parameter. `auto` (the
default) is the policy above, unchanged and silent. `external` is an explicit
request for the external structural provider: it widens the policy to every
step the provider can serve — including steps that would otherwise stay native
and even when the native answer is already sufficient — charges the shared
external budget as usual, and merges the external evidence with the native
one. Orientation and filesystem-hierarchy steps stay native-only by design.
The preference is a typed parameter the model passes; it is never guessed from
question text.

### 12.2 Outcome and fallback matrix

| Condition | Returning evidence | Ohgo behaviour |
|---|---|---|
| external success | bounded facts, `SUCCESS` | used |
| external bounded/stale | facts with omission counts / `STALE` | used; limitation preserved |
| external ambiguous | candidate facts, `AMBIGUOUS` | candidates preserved; native consulted; no arbitrary choice |
| external unresolved | `UNRESOLVED` | native consulted; "not established", never "absent" |
| external not indexed | `NOT_INDEXED` | native answers; the optional provider's absence does not qualify the native result — unless the request explicitly preferred external, in which case the native answer carries the issue `requested external structural provider reported NOT_INDEXED` |
| external unavailable/timeout | `UNAVAILABLE` | native answers; no fact is inferred from the failure and the native result is not downgraded; an explicit external preference adds the disclosure issue instead |
| external failed/invalid output | `FAILED` | native answers; the failure is an explicit issue |
| external absent or disabled | native | native answers; no gry process is started |

The native engine remains the fallback, so an external failure never turns into
an Ohgo task failure. `NOT_INDEXED` and `UNAVAILABLE` are configuration states,
not evidence limitations: the router uses the native answer without adding a
qualifying issue. `FAILED`, `AMBIGUOUS`, and `UNRESOLVED` are preserved because
they are claims about the target or a real operation failure.

An explicit `provider: "external"` request that cannot be met — the provider is
not configured, unavailable, or the step is native-only — is never silently
downgraded: the native answer carries an issue naming that the requested
external structural provider was not used, in the same channel a fallback
issue uses. An `auto` request produces no such issue.

### 12.3 Bounds and budget

One observation retains at most 12 facts and 24 evidence records; omitted counts
and the reason are explicit. Raw JSON is parsed, never forwarded: source text is
never requested, and `--include-source` is not reachable. Commands have a hard
timeout and a raw-output cap. The router charges the shared
`Meter.ChargeExternal` budget before the adapter runs; the adapter cannot bypass
it. A budget refusal falls back to native and says so.

### 12.4 Independence and authority

- Composition launches no process, reads no index, and requires no
  configuration. `ohgo start`, `ohgo run`, `ohgo models`, ASK, FUZZ, BUILD, task
  creation, and task resume are unchanged without gry.
- The adapter only exposes the read-only query subcommands listed above. It has
  no indexing, deletion, mutation, approval, or completion path.
- `NOT_INDEXED` and `UNRESOLVED` are distinct from absence; a stale or partial
  observation is marked, not promoted.
- gry provenance, including `verified`, maps to `OBSERVED`. Only
  `evidence.Prove` produces `VERIFIED`, and completion remains an
  `internal/agent` decision over mutation and observation evidence. Structural
  evidence cannot complete a mutation.
- Opt out with `OHGO_GRY=0`. `OHGO_GRY_BIN`, `OHGO_GRY_PROJECT`,
  `OHGO_GRY_TIMEOUT_MS`, `OHGO_GRY_MAX_TOKENS`, `OHGO_GRY_MAX_NODES`,
  `OHGO_GRY_MAX_ROWS`, and `OHGO_GRY_MAX_DEPTH` are the only tuning knobs.

### 12.5 Provider attribution

Attribution is presentation of provenance, not a second provider identity.
Every `investigate.Fact` carries its mechanism, and the model-facing rendering
keeps it beside the capability:

- `[structural/native]` or `[structural/gry]` on each fact;
- `investigate: sufficient · structural/gry · intent …` on the first line, so a
  collapsed transcript row is still truthful;
- `status: … · provider: native` on every `structure` result, because the
  `structure` tool is always the in-process AST index.

The step-level provider remains `structural`; `gry` is a source. This lets a
user distinguish native analysis from delegated structural evidence without
turning gry into a role, a second planner, or a separate state owner.

### 12.6 Investigation closure

An investigation that asks for a defect verdict is not allowed to substitute
another repository survey for that verdict. Its shape declares
`investigation_conclusion`; only a **FINAL** call to the `conclude` tool records it:

```text
SAFE | BUG | UNRESOLVED
evidence: entrypoint, identifier flow, validation boundary, persistence ordering
uncertainty: material gaps that remain
```

The vocabulary belongs to the objective, not to the tool. A general answer
about the project ("what is this project?", "explain the architecture") records
an answer outcome instead:

```text
ANSWERED | PARTIAL | UNRESOLVED
evidence: what the run observed
uncertainty: what remains unverified
```

`investigation_conclusion` is declared only for a failure investigation or an
explicit request for a safety verdict. A plain analysis therefore has no
manufactured verdict requirement; if the model records one anyway, the control
plane rejects `SAFE`/`BUG` and keeps the invalid record for audit. The
`ConclusionMode` installed for the turn is the authority, so the model cannot
choose a category that makes its answer look terminal.

The requirement is a convergence point, not a proof. `conclude` records an
observation; the completion evaluator still decides whether the answer is
supportable. Calling the tool is a proposal, not the transition itself:
`status: IN_PROGRESS`, a placeholder, an explicit insufficiency marker, missing
evidence, or an unrecognized status is recorded as a progress observation. It
creates no conclusion item, satisfies no conclusion requirement, and consumes no
terminal attempt. This is what prevents "placeholder, not yet sufficient" from
manufacturing a terminal state. A verdict outside the active mode is not a
progress observation either: it is recorded as `conclusion_rejected` and remains
non-terminal.

A conclusion is **provisional when its uncertainty names a concrete
code coordinate** (a selector such as `StockReserver.Reserve` or a Go path). The
runtime records it for audit but keeps `investigation_conclusion` unsatisfied
and adds `investigation_target:<coordinate>` ahead of it. Only a new observation
of that coordinate after the conclusion can clear the target; omitting the
coordinate from a later conclusion does not erase the obligation. This is the
evidence-admissibility layer: the conclusion window does not begin merely
because the investigation has some evidence.

Target matching is conservative in one direction and complete in the other.
A dotted selector may be observed through its bare method name only on code
identifier boundaries: `StockReserver.Run` is satisfied by `func (s
StockReserver) Run(...)` without also being satisfied by `runtime` or
`CheckoutRunner`, and short method names stay satisfiable without a length gate.
Matching happens against the whole tool text at observe time, not the retained
excerpt, so a target whose only mention lies past the evidence value bound is
still observed. A named source path whose complete contents the run already read is
not a reopened obligation — the read is the targeted observation — except when a
recorded mutation of that file followed the read, because the conclusion may be
naming the post-change state.

Repeated conclusions still need a hard semantic limit. After
`MaxConclusionAttempts` and at least one new observation, an explicitly
`UNRESOLVED` conclusion is accepted with the unresolved coordinate retained in
the record. A repeated `UNRESOLVED` without a new observation is not an attempt;
the projection's no-progress bound closes that shape instead.

### 12.7 Response transaction

Internal iterations are not user-visible turns. One task has one answer
transaction: `INVESTIGATING → FINALIZING → COMMITTED`, with `REVIEW` when the
publication audit refuses the candidate and `ABORTED` when no answer was
accepted. Drafts may be rejected and replaced; only the committed text reaches
the host transcript as an answer.

The runtime classifies every turn's assistant prose once, as
`agent.AnswerDisposition`:

| Disposition | Meaning | User-visible projection |
|---|---|---|
| `AnswerNarration` | A tool-round preamble or an abandoned plan. | Calls and results stay; the buffered prose is discarded. |
| `AnswerSuperseded` | A bounded correction replaced the candidate. | The buffered candidate is discarded when the correction starts; it is audit lineage only: never rendered, persisted, or replayed as the answer or a withdrawal. A committed answer of the same turn links the internal tombstone through `SupersededBy`. |
| `AnswerRejected` | The publication review terminally refused the candidate. | Exactly one labelled draft, or the plain output's `answer withheld` line. `AnswerReview` carries the control-plane reason verbatim. |
| `AnswerCommitted` | The response transaction accepted the answer. | The one answer, settled with its exact text. |

No answer prose is presented as final before `AnswerCommitted`, and unapproved
candidate prose is not presented at all. The TUI buffers streamed deltas in
state while a candidate is under review. Its telemetry line states
`verifying evidence…`; reasoning and tool activity remain live. A committed
answer becomes one transcript block with its exact text. The block is revealed
at a bounded reading pace rather than painted as one wall of text; the reveal
is a display projection over the already settled text, so copy, persistence,
and the answer contract see the full value from the first frame. The same
reveal applies to the one labelled rejected draft. A narration or
superseded disposition discards the buffer, so a refused candidate can never
appear and vanish mid-read. A terminal rejection may project the buffered text
once as the labelled display-only draft the disposition table specifies. A
superseded candidate never becomes a transcript artifact. Only a committed
answer of the same turn links the internal tombstone as replaced, and the link
is audit state rather than rendered content. The renderer does not infer a
disposition from event order, empty text, stream completion, or its own audit
read: an unclassified buffer at a turn boundary is discarded, not promoted. A
cancelled stream keeps its partial text as one display-only draft labelled with
the interruption, never as an answer.

An informational answer boundary also has a pre-prose claim gate. When the
control plane reports that the required evidence is present, the next request
offers the `prepare_answer` control tool instead of free prose. The model
declares the claims the final answer will make and, separately, the scope
limitations it will state. The runtime runs the same deterministic claim audit
used before publication. A refused plan names the missing observation and
reopens the evidence surface without the plan tool; the tool returns only after
the model records an observation. A refused plan is control feedback, not a
failed capability: after a new observation the same plan signature is
re-audited against the updated evidence instead of being suppressed by the
failure ledger. An accepted plan unlocks the answer request. The accepted
claims and limitations are carried into every later request as the composition
ledger: the final answer must use those claims at the strength and attribution
the audit accepted. The final prose is checked against the ledger again, so a documented
claim cannot be strengthened into an unattributed world claim after the plan
passes. The gate is bounded: after its attempts are spent, the ordinary
publication review still decides. The plan is a control observation, never
evidence, so its own claims cannot corroborate the answer.

The model's live context is not the host transcript. A superseded candidate
remains in that context so the model can repair its supported claims, but it
was never delivered to the user. When a correction spends its tool allowance,
the request states that the candidate was withheld and requires the complete
answer; a short answer that refers to "the analysis above" or otherwise treats
the unpublished draft as delivered is a review failure, not a committed
answer. The completion condition is rendered as a requirement, never as an
accomplished fact.

Completion reads the same publication decision. `agent.EvaluateTaskCompletion`,
the durable task's `Answered` evidence, and a delegated child's result use
`Turn.AnswerText`: prose contributes to the answer only when the turn is
committed (or is the unclassified zero value a host supplied without running
the gate). For an informational objective, a run that ends on a narrated,
superseded, or rejected candidate is `NEEDS_REVIEW` even though the raw prose
remains on the turn for audit and repair. The two authorities therefore cannot disagree about whether an answer
exists. A terminal refusal carries its draft's bounded claim issues into the
review, so the durable task records what was refused rather than only a prose
reason.

The committed disposition is also the Thought boundary. Applying it reconciles
every open reasoning trace before the answer becomes visible, so a frame can
never show `Thought: ACTIVE` beside `Answer: FINAL`. Every run terminal path
reconciles any remaining trace and terminalizes any call that never reported an
outcome; `RunTerminal` therefore implies zero live activities. This is a
lifecycle transition in the reducer, not a renderer heuristic: elapsed time,
spinner state, prose patterns, and the absence of deltas never decide that an
activity finished.

A candidate is an internal `agent.AnswerDraft`, with a version, a
`SUPERSEDES` link, and a sentence-sized claim ledger. Review is claim-scoped:
only the claims an issue names become `REPAIR_REQUIRED`; the rest keep their
`PROVEN`/`DOCUMENTED` state. A revision that drops an independently supported
claim is reported as preservation loss, and the runtime commits the strongest
draft with only the refused claims removed rather than the poorer rewrite. A
draft's lifecycle is `CANDIDATE`, `UNDER_REVIEW`, `REPAIR_REQUIRED`,
`SUPERSEDED`, `REJECTED_TERMINAL`, or `COMMITTED`. Only `COMMITTED` is a
user-visible answer; `SUPERSEDED` is internal audit lineage that the TUI keeps
only as a non-renderable tombstone, never as a candidate or a withdrawal.
`Loop.LastResponseAudit` records the drafts, lineage, preserved and removed
claims, rejections, tool calls, reused observations, retries, redundant
identical calls, and the final claim audit; a second commit for the same run is
a no-op.
`UnsupportedClaims` counts material claims the deterministic audit could not
certify, while `UnresolvedFacets` counts requested deliverables the answer
honestly reports as unverified. The second is still a completion gap; it is not
counted as an uncertified factual claim.

Before commit, `FinalizeAnswer` runs the same deterministic grounding and
provenance audits completion uses. It sanitizes copied runtime state but does
not delete substantive sentences and does not append a runtime-authored
limitation report. `AnswerAudit.Approved` decides publication: a candidate with
uncertified claims is recorded as `REVIEW`, suppressed from the host transcript,
and reused by completion for the exact `NEEDS_REVIEW` claim list. The model's
raw candidate remains in the turn, so the review can name what was not
established without showing it as a final answer. A sentence-level editor could
mangle tables, lists, and fenced examples, and the appended report was a second,
competing conclusion. An answer may still be published while the task is
`NEEDS_REVIEW` for a non-claim gap (a requested facet or a bounded evidence
gap); the published answer and the completion verdict then describe the same
text.

Once the conclusion is the only missing requirement, a bounded ten-turn
conclusion window still allows concrete closing reads, but further surveying
does not reset the requirement; after the window the run reports instead of
consuming the model-turn grant. A deferred mutation phase keeps its executable
surface for the later probe; an ordinary investigation closes at its required
evidence.
A targetless mutation gets a separate eight-turn act window: it may read the
implementation once, then must act rather than survey indefinitely. At the end
of the conclusion window the projection forces the `conclude` tool for the next
request; the tool was already offered, so this is a transition, not a new
capability.

The contract also treats a negated fix (`Do not create a fix`, `investigation
only`) as informational rather than mutation, and treats Gry as optional
structural intelligence. Only an explicitly requested MCP capability, URL, or
external documentation creates `external_observation`.

MCP remains a transport option, not the architecture. Configuring gry as a
generic MCP server exposes its tool surface to the model as a user-chosen
capability; the canonical structural integration is this provider, which keeps
routing, bounds, provenance, and fallback inside Ohgo.

The pre-existing `internal/gryhook` adapter remains an opt-in compatibility
observation path for headless `ohgo run`: it injects bounded evidence around
tool calls and cannot authorize, block, or complete. It is not the structural
provider above, it is disabled by default, and this change adds no lifecycle
hook to the Control Plane. The capability provider is the canonical integration;
the hook adapter is retained only for users who explicitly enabled it.

Validation: adapter unit tests cover availability, project resolution, every
outcome, bounds, provenance, the read-only command surface, real subprocess
timeout/output caps, and environment filtering; router tests cover the routing
matrix, fallback, ambiguity, stale evidence, and budget; an end-to-end
`cmd/ohgo` test drives the real tool set and scripted model against a fake gry
process; `TestLiveCompatibility` (opt-in `OHGO_GRY_LIVE=1`) exercises a real gry
installation and index.

## 13. Status

| Capability | Implemented | Tested | Integrated into a model-facing tool |
|---|---|---|---|
| structural intelligence | yes | yes | yes (`structure`, `investigate`) |
| external structural provider (optional) | yes | yes (adapter, router, end-to-end, opt-in live) | yes (`investigate`, when configured and indexed) |
| Okapi BM25 retrieval | yes | yes (labeled corpus + invariants) | yes (`retrieve`, `investigate`) |
| semantic intelligence | yes | yes (fake + real gopls, including the live provider) | yes (`investigate`, gopls when installed) |
| runtime intelligence | yes | yes (fake adapter + real `dlv` fixture through the orchestrator) | no (authority decision, not a capability gap) |
| unified evidence engine | yes | yes, including orchestrator fixtures | yes, inside `investigate` |
| closed-loop planner | yes | yes (selection, reassessment, budget) | yes (`investigate`) |
| investigation conclusion | yes | yes (verdict, evidence, stall bound) | yes (`conclude`) |
| shared wire framing | yes | via LSP/DAP suites | — |

Known gaps, stated rather than implied:

1. Runtime is validated against a real debugger but is not exposed to the
   model. `TestRealDlvRuntimeEvidence` builds a fixture with `-gcflags=all=-N
   -l` (optimized builds can merge the known lines and leave the breakpoint
   unverified), launches it through `dlv dap --client-addr={addr}`, stops at a
   verified breakpoint, and drives threads/stack/scopes/variables through the
   orchestrator's runtime provider into the shared evidence engine and meter.
   The test skips only when dlv is absent or macOS developer mode is disabled.
   Exposing launch/attach/evaluate as a model tool remains a separate authority
   decision; the execution plane has no runtime tool.
2. Retrieval relevance is validated on a synthetic labeled corpus, not on a
   large human-labeled repository corpus.
3. Retrieval medians (5 rounds, 15 queries each) after the integer-slot
   postings change: 5,000 files cold 280 ms, warm 67 ms, refresh 48 ms,
   query-only 0.8 ms; 20,000 files cold 1.11 s, warm 286 ms, refresh 189 ms,
   query-only 5 ms; 50,000 files (20k cap) query-only 5 ms. The earlier
   single-run 5k "112/126 ms" was run-to-run variance, not a regression. The
   AST inventory exposes its observed mtime so retrieval does not stat every
   file twice.
4. Semantic manager instances are per investigation step: a warm `gopls` is not
   reused across separate `investigate` calls.
5. Adaptive-vs-full benchmarking covers structural + retrieval only; semantic
   and runtime rows are unavailable to the benchmark harness. Measured at 5,000
   files: project 30 ms adaptive vs 222 ms full; locate 124 ms vs 136 ms.
6. Memory is decomposed by phase in fresh processes (heap alloc):
   5,000 files: AST inventory 23.6 MiB, retrieval index +10.8 MiB,
   investigation +0.1 MiB. 20,000: 94.5 / +43.3 / +0.1 MiB. 50,000 (20k index
   cap): 227.6 / +43.2 / +0.1 MiB. The dominant retained structure is the AST
   source snapshot, not retrieval; the investigation layer adds almost nothing
   after its transient analysis is collected. LSP/DAP subprocess memory is still
   unmeasured.
