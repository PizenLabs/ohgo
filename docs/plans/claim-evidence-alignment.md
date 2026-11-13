# Claim-to-Evidence Alignment

> **Status:** implemented and verified; the finalized claim classes and coverage
> contract are canonical in
> [`docs/architecture/INTENT_GROUNDING.md`](../architecture/INTENT_GROUNDING.md) §4.6.
> **Scope:** the evidence class an informational answer may promote into a
> claim, the negative-claim boundary, and the claim orientation in the runtime
> state block.
> **Non-goals:** an LLM judge on the answer path, a second orchestrator, an
> automatic probing loop, model-specific rules, or any change to ASK/FUZZ/BUILD
> authority.

## 1. Problem statement

The practical repository-analysis run on the `go-ecommerce` project improved
its investigation selection: it moved from the README to `docker-compose.yml`,
`go.mod`, the service entry points, and the source directories without reading
the whole tree. It stayed cheap.

The remaining failure was not evidence selection. It was **claim scope**:

- Compose topology was presented as proof of runtime behavior.
- Separate service declarations were presented as independent deployment.
- A directory listing's byte count was presented as a line count.
- A path's likely content was presented as an observation.
- The bounded report "no test files or CI configuration were surfaced in the
  probes" was treated as a repository-wide absence conclusion.

The runtime needed to distinguish three claim states rather than one:

```text
SUPPORTED              declared configuration / observed source / measured metric
REQUIRES VERIFICATION  behavior inferred from topology or documentation
NOT ESTABLISHED        property of a path or repository the run never inspected
```

## 2. Verified findings

### A. A bounded probe report was read as an absence claim

`negativeClaims` matched "No test files or CI configuration were surfaced in
the probes". The sentence describes what this run's observations contained and
says the property is worth verifying; it does not assert that the repository has
no tests or CI. The detector had no notion of a bounded report.

- Root cause: every "no X" phrase was treated as a repository-wide conclusion.
- Desired invariant: a report scoped to the probes is an observation about the
  run; a repository-wide conclusion still needs coverage.
- Fix: skip a negative claim when the same sentence bounds it to the probes,
  the pass, the run, or an explicit "not evaluated / not checked / worth
  verifying" limitation — unless the same sentence also makes a global scope
  claim.

### B. Configuration and manifests were promoted to runtime behavior

`AssessGrounding` already checked external state and negative conclusions. It
did not distinguish:

```text
go.mod / package.json     declared dependency
docker-compose.yml        declared topology
README / docs             what the project says
source read               what the implementation does
successful execution      what actually ran
```

The answer used `go.mod`, Compose, and the README to support "cart validates
against product, order consumes cart and publishes events, delivery fulfills".
Those terms can appear in dependency and documentation text without the
behavior being implemented.

- Root cause: no answer-level behavior claim was matched to a source
  observation.
- Desired invariant: a runtime-behavior claim needs a source observation (or an
  explicit limitation/attribution), not a dependency or topology declaration.
- Fix: match a narrow behavior vocabulary, derive its source stem, and require
  a successful source `read` or source-scoped `grep` containing that stem.
  Documentation attribution and explicit "not verified" phrasing are accepted
  as lower-assertion claims. Compose's PostgreSQL init hook is the one
  configuration mechanism that can support an auto-apply claim, because the
  mount itself is the mechanism.

### C. A listing byte size became a line count

`ls` reports a file size. The answer turned `1202 App.tsx` (bytes) into
`~1200 LOC`; the file is 40 lines. The run never read the file.

- Root cause: no line-count claim was matched to a read's `totalLines`.
- Desired invariant: a concrete metric requires the observation that measured
  it; a listing is not a measurement.
- Fix: match a line-count claim, locate the nearest path, require a read
  `totalLines` for that path (or a line-count command when no path is named),
  and reject a gross contradiction outside a fifth/50-line tolerance.

### D. Compose topology was promoted to independent deployment

Separate Compose service declarations prove separate containers and builds.
They do not prove independent deployment boundaries, release coupling, or
deployability.

- Root cause: no deployment claim was checked against a deployment artifact.
- Desired invariant: independent deployment needs a Dockerfile, Kubernetes,
  Helm, or deploy manifest observation, not a service entry alone.
- Fix: match independent-deployment claims and require a read deployment
  artifact path or content.

### E. Path-scoped speculation was presented as observation

`supabase/ — ... (likely schema/edge functions ...)` and `App.tsx (... likely
containing most routes/pages)` inferred content for paths the run only listed.

- Root cause: no path-scoped speculation check.
- Desired invariant: a speculative content claim about a named path needs the
  category term to appear in an observation of that path; a general inference
  is left to the evidence/provenance gates.
- Fix: match an explicit inference marker, choose the nearest file/directory
  reference, and require the content noun in the observation scoped to that
  path. An unscoped inference such as "the backend likely uses PostgreSQL" is
  not forced to repeat a manifest observation.

### F. A complete Go scan could not prove a Go test-file absence

A complete `glob "**/*.go"` that returned every Go file and no `_test.go` was
still reviewed as an unproven "no Go test" conclusion. Go mandates the
`_test.go` suffix, so that scan is proof for the narrowed claim.

- Root cause: absence support only accepted concrete file claims, not a
  language-qualified test-file claim.
- Desired invariant: a complete recursive scan of every Go file without a
  `_test.go` file proves no Go test files. A bare "no tests" claim and frontend
  test naming remain concept absences because neither has a mandatory suffix.
- Fix: retain complete scan results and recognize the Go test-file case.

### G. The claim boundary was not visible to the model

The model learned the configuration/behavior distinction only after a
completion review. The runtime state block carried the objective, probe,
evidence, and next action, but not the claim boundary.

- Root cause: no claim-scope orientation in `RenderState`.
- Desired invariant: an informational turn sees the distinction before it
  writes the answer, without spending a model or tool call.
- Fix: add a bounded `Claim boundary` line to informational state blocks.

## 3. Design

The repair stays inside the existing answer-validation owner:

```text
tool observations + answer
        │
        ▼
agent.AssessGrounding
  ├─ existing entity / action / scope / ambiguity
  ├─ existing external-state coverage
  ├─ negative-claim coverage (bounded reports excluded)
  ├─ Go test-file scan exception
  └─ claim scope audit (new, informational only)
        ├─ behavior  → source observation
        ├─ deployment → deployment artifact
        ├─ line count → measured line count
        └─ path-scoped speculation → observed path content
```

The audit can only downgrade completion. It never authorizes an action, never
grants authority, and never replaces target, mutation, verification, or
evidence-sufficiency checks.

Claim scope is ordered by consequence before the reason is bounded: behavior,
deployment, line count, speculation. Each detail names the missing observation
and the narrower claim the model can make instead.

## 4. Implementation

- `internal/agent/path.go` (new file in the existing `agent` owner)
  - one path classifier (`ClassifyPath`) and manifest-language map shared with
    the cognition provenance layer, so answer claim scope and evidence
    provenance cannot drift apart.
- `internal/agent/claims.go` (new file in the existing `agent` owner)
  - evidence-class predicates for documentation, manifests, configuration,
    source, deployment artifacts, and measured line counts;
  - behavior/deployment/line-count/speculation claim matchers;
  - bounded negative-report recognition and Go test-file scan support;
  - bounded, consequence-ordered review reasons.
- `internal/control/cognition/evidence.go` / `provenance.go`
  - delegate path provenance and manifest-language lookup to the shared
    `agent` classifier.
- `internal/agent/grounding.go`
  - `negativeClaims` skips bounded probe reports unless the same sentence makes
    a global scope claim;
  - `groundingCoverage` retains complete glob results and recognizes a complete
    Go scan without `_test.go`;
  - `AssessGrounding` runs the claim audit for informational objectives and
    improves the negative-claim reason wording.
- `internal/control/cognition/render.go`
  - informational state blocks carry the claim boundary.
- Tests
  - `internal/agent/claim_scope_test.go`: behavior vs configuration, source
    support, documentation attribution, explicit limitation, deployment,
    line-count observation/contradiction, init-hook mechanism, path-scoped
    speculation, bounded probe reports, global absence, Go test scans, frontend
    convention limits, fenced code, and the practical answer.
  - `internal/control/cognition/grounding_test.go`: the state block carries the
    claim boundary.

## 5. Regression matrix

| Requirement | Test |
| --- | --- |
| Bounded probe report is not an absence claim | `TestBoundedProbeReportIsNotAnAbsenceClaim` |
| A global scope is not excused by a probe phrase | `TestBoundedProbePhraseDoesNotExcuseGlobalAbsence` |
| Compose topology is not runtime behavior | `TestConfigurationTopologyDoesNotProveRuntimeBehavior` |
| Source observation can support behavior | `TestSourceObservationCanSupportBehaviorClaim` |
| Documentation attribution is not an observed fact | `TestDocumentationAttributionIsNotAnObservedBehaviorClaim` |
| Explicit "not verified" is a lower assertion | `TestExplicitlyUnverifiedBehaviorIsNotAnObservation` |
| Compose topology is not independent deployment | `TestComposeTopologyDoesNotProveIndependentDeployment` |
| A deployment artifact supports the claim | `TestDeploymentArtifactSupportsIndependentDeploymentClaim` |
| A listing byte size is not a line count | `TestUnobservedLineCountIsReviewed` |
| A contradicted line count is reviewed | `TestContradictedLineCountIsReviewed` |
| A measured line count completes | `TestObservedLineCountCompletes` |
| Auto-apply needs the observed init hook | `TestAutoAppliedMigrationNeedsTheObservedMechanism` |
| Path-scoped speculation is reviewed | `TestSpeculationAboutUninspectedPathIsReviewed` |
| Unscoped inference is not path-scoped speculation | `TestUnscopedInferenceIsNotClaimScopeReview` |
| A quoted code block is not a prose claim | `TestClaimAuditIgnoresFencedCode` |
| The practical answer reviews the unsupported claims, not the bounded report | `TestPracticalRepositoryAnalysisReviewsTheUnsupportedClaims` |
| Path classification is shared and complete | `TestClassifyPath`, `TestManifestLanguage` |
| A complete Go scan proves no Go test files | `TestCompleteGoScanProvesNoGoTestFiles` |
| A bare test absence still needs coverage | `TestBareTestAbsenceStillNeedsCoverage` |
| Frontend test absence stays concept-level | `TestFrontendTestAbsenceStaysReview` |
| The state block carries the claim boundary | `TestRenderStateCarriesClaimBoundary` |

## 6. Invariants that must remain true

1. ASK is read-only, FUZZ may read/execute but never mutate, and BUILD is the
   only mutation authority.
2. Completion remains the only completion decision; the claim audit can only
   downgrade.
3. No second model call, planner, orchestrator, repository index, or task store.
4. A bounded probe report is accepted; a repository-wide absence conclusion
   still needs coverage.
5. A manifest, dependency, or topology declaration does not prove behavior.
6. A concrete metric, deployment conclusion, or path-scoped inference needs the
   observation that supports it.
7. A complete Go file scan without `_test.go` may prove no Go test files; a bare
   "no tests" claim and convention-based frontend test absence do not.
8. The review reason stays bounded and names the missing observation.

## 7. Verification

Focused packages passed:

```sh
go test ./internal/agent ./internal/control/cognition ./internal/continuity/task ./internal/tui ./cmd/ohgo
```

Repository gates passed from the repository root:

```text
gofmt -l .                                      empty
go build ./...                                  pass
go vet ./...                                    pass
go test -count=1 ./...                          pass (all packages)
go test -race -count=1 ./...                    pass (all packages)
golangci-lint run ./...                         0 issues
```

Mutation checks (temporarily break the mechanism, observe the intended test
failure, restore) were run and confirmed for:

- bounded negative recognition (`TestBoundedProbeReportIsNotAnAbsenceClaim`);
- behavior evidence (`TestConfigurationTopologyDoesNotProveRuntimeBehavior`);
- deployment evidence (`TestComposeTopologyDoesNotProveIndependentDeployment`);
- line-count measurement (`TestUnobservedLineCountIsReviewed`,
  `TestContradictedLineCountIsReviewed`);
- path-scoped speculation (`TestSpeculationAboutUninspectedPathIsReviewed`);
- the Go test-scan exception (`TestCompleteGoScanProvesNoGoTestFiles`);
- the informational state-block claim boundary
  (`TestRenderStateCarriesClaimBoundary`).

Each restored mechanism was green again.

## 8. Risks and rejected alternatives

- **Rejected:** an LLM judge for claim scope. It adds a model call and
  non-determinism to the common path; the runtime already owns the observations
  needed for the high-signal claim classes.
- **Rejected:** rejecting every behavior sentence. Source-backed behavior is the
  point of a repository analysis; the check requires only a source observation.
- **Rejected:** treating Compose as independent deployment. It is topology.
- **Rejected:** reading a listing byte size as a line count. The metric has one
  owner: the read's `totalLines` or a line-count command.
- **Rejected:** accepting a complete `.tsx` scan as proof that no frontend tests
  exist. TypeScript test naming is convention-based; a targeted file claim can
  still be proven.
- **Risk:** the checks are English-pattern based. A paraphrase outside the
  patterns escapes the claim audit; structural, verification, grounding, and
  provenance checks still apply.
- **Risk:** an answer about a file supplied in the user's `@` context may be
  reviewed because that content is not a tool `Turn`. The review is
  conservative and the model can mark the claim as user-provided.
- **Risk:** the informational state-block line consumes part of the state
  budget. Evidence summaries are the first section sacrificed by the existing
  bounded renderer; the contract, claim boundary, sufficiency, and next action
  remain.
