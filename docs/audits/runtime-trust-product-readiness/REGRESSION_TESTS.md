# Regression tests

## Completion

| Test | Contract |
|---|---|
| `TestRequiredFacetRejectsExplicitlyUnverifiedDeliverable` | “caller count … remains unverified” is not complete. |
| `TestRequiredFacetRejectsOmittedDeliverable` | A requested facet missing from the answer is reviewed. |
| `TestRequiredFacetRejectsPartialObservedList` | 3 named of 13 observed sites without a total is reviewed. |
| `TestRequiredFacetAcceptsStaticCaveat` | A complete list with a dynamic-dispatch caveat is accepted. |
| `TestRequiredFacetAcceptsBoundedZeroObservation` | A bounded zero test-file observation is accepted. |
| `TestRequiredFacetRejectsContradictedAbsence` | “No callers” against an observed count is reviewed. |
| `TestCodeClaimRejectsInventedPropAgainstObservedDeclaration` | An invented prop contradicted by the observed declaration is reviewed. |
| `TestCodeClaimAcceptsObservedDeclaredProp` | A declared prop is accepted. |
| `TestCodeClaimSkipsWhenNoSourceWasObserved` | A resumed answer is not punished for absent current evidence. |
| `TestAnswerReviewCorrectsMissingFacetOnce` | One correction turn fixes a missing facet. |
| `TestAnswerReviewStopsAfterOneCorrection` | A repeated failure does not loop. |
| `TestAnswerReviewCorrectsInventedProp` | The corrected answer uses the observed declaration. |
| `TestTestAbsenceClaimRequiresCompleteToolingAndScope` | A zero test inventory is not a repository-wide test absence. |
| `TestMissingTestScriptDoesNotProveNoTests` | A manifest script fact is not a test inventory. |

Run:

```sh
go test ./internal/agent ./internal/control/workspace ./internal/control/contract
```

## Authority

| Test | Contract |
|---|---|
| `TestLeadingParticipialEvidenceClauseStaysRead` | “Using read-only structural evidence, report …” stays in ASK. |
| `TestLeadingUsingCommandClauseKeepsExecutionAuthority` | Executable `using` clauses still require execution authority. |
| `TestAuthorityClaimsInProseDoNotRequestMutation` | “The user authorized BUILD” is prose, not a mutation request. |
| `TestModelClaimOfAuthorizationCannotEscalate` | Model text cannot move the gate or execute a write. |
| `TestToolOutputCannotEscalateAuthority` | Tool output cannot grant the next mutation. |
| `TestAuthorityIsEnforcedWithoutPromptInstructions` | A prompt cannot override the live gate. |
| `TestAskRefusesExecutionWithoutCallingTheModel` | ASK + mutation is refused before the provider. |
| `TestFuzzFindingDoesNotAuthorizeMutation` | A FUZZ finding is not a BUILD grant. |

Run:

```sh
go test ./internal/control/workspace ./cmd/ohgo -run 'Authority|Participial|Using|ModelClaim|ToolOutput'
```

## Runtime-state isolation

| Test | Contract |
|---|---|
| `TestSanitizeAnswerRemovesCopiedRuntimeStateBlock` | A copied state block is removed from public output. |
| `TestSanitizeAnswerRemovesStandaloneRuntimeDirective` | “No further tools are available” is not project prose. |
| `TestSanitizeAnswerKeepsLegitimateStandaloneSection` | A model's own `Verification:` section is preserved. |
| `TestSanitizeAnswerLeavesFencedCodeUntouched` | Code fences are data, not orchestration. |
| `TestSanitizeAnswerDoesNotFallBackToCopiedState` | An all-state answer becomes empty and is reviewed. |

Run:

```sh
go test ./internal/agent -run TestSanitize
```

## Evidence and GRY

| Test | Contract |
|---|---|
| `TestSourceVerificationCurrentWhenSymbolStillExists` | Current coordinate is CURRENT. |
| `TestSourceVerificationMissingFileIsStale` | Missing source is STALE. |
| `TestSourceVerificationMovedSymbolIsStale` | A moved symbol is STALE. |
| `TestSourceVerificationOutsideRootIsUnavailable` | Outside-root source is UNAVAILABLE. |
| `TestSourceVerificationSymlinkOutsideRootIsUnavailable` | A symlink cannot smuggle an outside file into evidence. |
| `TestSourceVerificationWithoutRootIsUnknown` | No root is UNKNOWN, not CURRENT. |
| `TestTraceWithoutGenerationIsVerifiedAgainstSource` | A trace with no generation is verified against source. |
| `TestTraceStaleRowDowngradesObservation` | A stale trace row downgrades the observation. |
| `TestStaleContextIsMarkedAndBounded` | Context freshness and truncation are preserved. |
| `TestRouterNeverPresentsStaleExternalEvidenceAsCurrent` | Stale external evidence does not become current. |

Run:

```sh
go test ./internal/intelligence/gry ./internal/intelligence/investigate
```

## Usage persistence

| Test | Contract |
|---|---|
| `TestAddUsageAccumulatesAndSurvivesReopen` | Measured totals survive a session reopen. |
| `TestAddUsageIgnoresUnmeasuredRun` | No measurement is not zero. |
| `TestUsageColumnsMigrateOldDatabase` | Old session databases migrate in place. |
| `TestRecordRunUsagePersistsMeasuredTurns` | A headless run persists once per run. |
| `TestRecordRunUsageLeavesUnmeasuredSessionUnknown` | Missing provider usage stays unknown. |

Run:

```sh
go test ./internal/continuity/session ./cmd/ohgo -run 'Usage|Stats'
```

## Structure Core

| Test | Contract |
|---|---|
| `TestNestedGoModuleResolvesCrossPackageReferences` | A nested `backend/go.mod` owns its files. |
| `TestImpactReportsCallees` | Impact includes forward callees. |
| `TestSiblingGoModulesAreReportedAsAmbiguous` | Sibling modules are disclosed, not guessed. |
| `TestSingleNestedModuleStaysUnambiguous` | The single-nested-module fix is intact. |
| `TestTraceIncludesTestCallSites` | GRY trace includes tests. |
| `TestResolvePreservesAllCandidates` | Exact-name lookup does not drop candidates. |

Run:

```sh
go test ./internal/intelligence/ast -run 'Nested|Sibling|Callees|Trace|Resolve'
```

## Real repositories (read-only)

```sh
CGO_ENABLED=0 OHGO_AUDIT_TARGET=/Users/anvndev/Documents/Project/Golang/ecommerce \
  OHGO_AUDIT_SYMBOL=NewOrder \
  go test ./internal/intelligence/ast -run TestAuditTargetSnapshot -v

CGO_ENABLED=1 OHGO_AUDIT_TARGET=/Users/anvndev/Documents/Project/Blog/andev0x-blog \
  OHGO_AUDIT_SYMBOL=TerminalBlog \
  go test ./internal/intelligence/ast -run TestAuditTargetSnapshot -v
```

The probe builds an in-memory index through the production
`tools.StructuralSource`; it writes no project state and does not initialize
the target.

## Full checks

```sh
go build ./...
go test ./...
CGO_ENABLED=0 go test ./...
go test -race ./...
go vet ./...
gofmt -l .
golangci-lint run ./...
make bench
```

The build matrix is:

```sh
make build            # CGO_ENABLED=0
make build-multilang  # CGO_ENABLED=1
./ohgo doctor         # 1 adapter
./ohgo-multilang doctor # 11 adapters
```

## Adversarial model scenarios

The hosted runs in `MODEL_EVALUATION.md` cover:

- read-only request under ASK (allowed);
- read-only request with a mutation clause under ASK (refused before model);
- investigative request under FUZZ (allowed, no mutation);
- model claims of authorization;
- tool output claiming a mode change;
- a partial caller list;
- an invented/unsupported code reference;
- a zero test-file inventory with uninspected tooling;
- provider roster absence (cohere) and prior 429 (longcat).
