# Diff review — closure phase

Date: 2026-11-20
Base: OHGO `4848ed9a532f643d91282bdf8da941a283a92d5e`
State: uncommitted working-tree changes

This review covers the closure-phase diff only. The prior audit's changes are
already in the base commit. No target repository was changed.

## 1. Summary

| Area | Files | Effect |
|---|---|---|
| Streaming isolation | `internal/agent/presentation.go`, `loop.go`, `transcript.go` | One online sanitizer shared by live deltas and completed answers; every published/persisted turn is projected. |
| Claim grounding | `internal/agent/entity_claims.go` (new), `claims.go`, `code_claims.go`, `grounding.go` | General entity-to-evidence association, absence contradiction, prose-adjective filter, evidence-gap calibration. |
| Edge freshness | `internal/intelligence/evidence/evidence.go`, `internal/intelligence/gry/observe.go`, `internal/intelligence/investigate/investigate.go`, `internal/execution/tools/investigate.go` | New `UNVERIFIED` state; node and edge freshness are separate; edge facts render their state. |
| Regression tests | `internal/agent/stream_isolation_test.go` (new), `entity_claims_test.go` (new), `code_claims_test.go`, `negative_claim_test.go`, `internal/intelligence/gry/source_verify_test.go` | Contract pinned at chunk boundaries, entity evidence, false-positive shapes, node/edge separation. |
| Audit evidence | `docs/audits/runtime-trust-product-readiness/*` | Closure reports, verification record, this review. |

## 2. File-by-file

### `internal/agent/presentation.go`

**Change.** Replaced the two-pass final-answer sanitizer with one state machine,
`answerStreamSanitizer`. `SanitizeAnswerText` runs it over the whole answer;
`Write` runs it over live deltas. The state machine holds only a line-start
prefix that could still become a runtime line, recognizes the state header and
internal field prefixes, preserves fenced code, and collapses trailing blank
runs.

**Why.** The previous final sanitizer and the live stream had different
implementations. A copied state block could be published live even though the
stored answer was clean.

**Risk.** A legitimate line beginning with an internal field label is withheld.
The labels are runtime-specific; ambiguous headings such as `Verification:` and
`Completion:` remain publishable. A fenced block is transparent. The
chunk-boundary test covers every split of the copied block.

**Verification.** `TestSanitizeAnswer*`,
`TestAnswerStream*`, `TestLoopStreamsOnlyPublicAnswerText`.

### `internal/agent/loop.go`

**Change.** `drain` wraps `OnTextDelta` in the sanitizer and flushes it at end
of stream. Every completed assistant turn is sanitized before it enters the
returned `turns` slice, not only the terminal answer.

**Why.** The returned turns are what hosts persist, publish, and replay. A
turn with a tool call can carry copied runtime text even though it is not the
final answer.

**Risk.** The loop's internal `live` conversation still carries raw model text;
that is the provider conversation, not the publication. The returned turns and
the delta channel are sanitized. No authority or tool path changes.

**Verification.** `TestPseudoToolCallsEnterTheCanonicalExecutionPath` (live and
completed text converge), `TestLoopStreamsOnlyPublicAnswerText`.

### `internal/agent/transcript.go`

**Change.** `TranscriptTurn` applies `SanitizeAssistantMessage` before the
answer-required refusal projection.

**Why.** `TranscriptTurn` is the documented host projection for display,
persistence, and `ConversationMessages`. Sanitizing there makes the boundary
hold for every host, including a future one that bypasses the loop's final
answer path.

**Risk.** A model-authored block that exactly matches a runtime field is
removed from replayed history. That is the intended contract.

**Verification.** `TestTranscriptTurn*`, session replay tests.

### `internal/agent/entity_claims.go` (new)

**Change.** Adds `entityClaimIssues`, which checks explicit code spans against
the run's successful observations, plus a negative-existence contradiction
check. It uses boundary matching, skips generic lowercase words, imperative
advice, bounded statements, documentation attribution, and negative
observations, and is inert without an observation baseline.

**Why.** The existing claim audit knew a finite set of predicates. A model could
invent a plausible identifier with a verb outside that set.

**Risk.** A project identifier that is only mentioned in an unobserved external
API can be reviewed. The imperative and generic-word filters cover the common
advice shapes; the check only runs when the answer marks the name as code.

**Verification.** `TestEntityClaim*`.

### `internal/agent/claims.go`

**Change.** Calls `entityClaimIssues` for each non-fenced sentence and ranks the
new issue with the other code/completeness findings. It builds the observation
corpus once per answer instead of once per sentence.

**Why.** The new association must reach the same completion boundary as the
existing claim checks, without turning the audit into an O(sentences × turns)
scan.

**Risk.** No new authority. The issue is a review boundary, never a mutation
grant.

**Verification.** `TestEntityClaimReachesGroundingAssessment`.

### `internal/agent/code_claims.go`

**Change.** Added `codeClaimAdjectives` and a code-shape filter for the
non-backticked `X prop` / `prop X` form.

**Why.** A real Nemotron answer said “TerminalBlog accepts a single prop”; the
old adjacent-identifier form read `single` as a member on a component named
`Based`.

**Risk.** A lowercase non-backticked member name is no longer audited by this
form. The backticked form still covers it, and lowercase bare words are too
ambiguous to treat as identifiers.

**Verification.** `TestCodeClaimIgnoresProseAdjectiveBeforeProp`, plus the
existing code-claim tests.

### `internal/agent/grounding.go`

**Change.** Excludes evidence-gap subjects from `negativeClaims`:
`claimSubjectIsEvidenceGap` and `claimIsEvidenceGap`.

**Why.** “No runtime or configuration evidence” is a limitation of this run,
not a claim that the repository has no runtime or configuration. A real
LongCat answer was reviewed for that honest limitation.

**Risk.** A sentence that genuinely claims “no evidence of tests” is treated as
an epistemic gap rather than a repository absence. That is the intended
distinction; the stricter test-absence gate still owns “no tests” claims.

**Verification.** `TestEvidenceGapIsNotANegativeRepositoryClaim`.

### `internal/intelligence/evidence/evidence.go`

**Change.** Added `FreshnessUnverified`.

**Why.** `CURRENT`, `STALE`, `UNKNOWN`, and `UNAVAILABLE` could not express
“the endpoints are current, but the relationship was not checked”.

**Risk.** Consumers that switch exhaustively on freshness must add the value.
The repository has no such exhaustive switch outside tests; the zero value
remains `UNKNOWN`.

**Verification.** `internal/intelligence/evidence` tests and the GRY tests.

### `internal/intelligence/gry/observe.go`

**Change.** Added `edgeFreshness`; `verifyFreshness` maps a current endpoint set
to `UNVERIFIED` edges; `edgeRecord`/`edgeFact` use it; trace and context emit
one edge-boundary issue and include both states in the summary.

**Why.** A current node is not a current edge. The previous code applied the
endpoint result to relationship records, which read as current facts.

**Risk.** Trace/context answers appear less certain. That is the honest state.
Stale and unavailable observations still downgrade both nodes and edges.

**Verification.** `TestTraceWithoutGenerationVerifiesEndpointsButNotEdges`,
`TestContextKeepsNodeAndEdgeFreshnessSeparate`,
`TestTraceStaleRowDowngradesObservation`,
`TestStaleContextIsMarkedAndBounded`.

### `internal/intelligence/investigate/investigate.go`

**Change.** `Fact` carries `Freshness`.

**Why.** The model-facing fact line is where a relationship is presented. A
step summary alone is too easy to miss.

**Risk.** No behavior change for providers that leave the field empty.

**Verification.** `internal/intelligence/investigate` tests.

### `internal/execution/tools/investigate.go`

**Change.** `formatInvestigation` renders a fact's freshness in its bracket
(`[structural/gry UNVERIFIED]`).

**Why.** The edge state must be visible on the fact, not only in the issues
list.

**Risk.** The label changes when freshness is present; existing providers with
empty freshness keep the old format.

**Verification.** `internal/execution/tools/investigate_test.go`.

### Tests

- `internal/agent/stream_isolation_test.go` — every chunk boundary, stand-alone
  directive, stand-alone fields, legitimate prose/fences, loop-level live
  channel.
- `internal/tui/stream_isolation_test.go` — the TUI's public `AssistantDelta`
  channel and stored transcript withhold runtime state.
- `internal/agent/entity_claims_test.go` — novel predicate, observed entity,
  boundary miss, no baseline, imperative advice, absence contradiction,
  grounding path.
- `internal/agent/code_claims_test.go` — prose adjective regression.
- `internal/agent/negative_claim_test.go` — evidence-gap regression.
- `internal/intelligence/gry/source_verify_test.go` — node/edge separation and
  updated current-endpoint contract.

## 3. What did not change

- No authority, workspace, security, or mutation path.
- No native Structure Core parser or graph algorithm.
- No GRY dependency: it remains optional and non-authoritative.
- No target repository.
- No commit, tag, push, or release.
- No change to the portable/multi-language build boundary.

## 4. Residual risks

1. A model can evade entity association by not marking a name as code, or by
   naming an observed entity while asserting a behavior the observation does not
   establish. This is the documented claim-grounding limit.
2. A model can invent prose that resembles runtime state without matching a
   fixed runtime line. That is model-authored text, not a leaked directive.
3. `UNVERIFIED` edges are less useful than verified edges. The state is honest,
   not a substitute for edge verification.
4. The model sample is a behavior sample, not a benchmark; provider rosters and
   rate limits change.
