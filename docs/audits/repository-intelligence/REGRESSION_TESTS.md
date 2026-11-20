# Regression tests

Each test defends one externally observable contract. The "before" column is
the observed baseline behavior or the assertion that would have failed; the
fix was exercised against the real repositories as well as the fixture.

| Bug | Test | Contract defended | Before |
|---|---|---|---|
| STR-001 | `TestNestedGoModuleResolvesCrossPackageReferences` | A nested `go.mod` owns the indexed Go files; refs, graph, and dependencies resolve | module empty, 0 refs, 0 edges |
| STR-001 | `TestSingleNestedGoModuleProducesArchitectureShape` | One nested module still yields a layered architecture from its children | not-established / independent |
| STR-002 | `TestStructuralProviderRunsFocusStrategyForImpact` | Native investigate runs symbol-neighborhood for impact | no focus report |
| STR-002 | `TestStructureAnalysisRunsFocusAtDefaultLevel` | `structure view=analysis` default level admits the focus strategy | no focus report |
| STR-003 | `TestFocusReferencesDoesNotIncludeCallersOrImporters` | Focus references is the reference list, not the neighbourhood total | 4 reported for 2 refs |
| STR-004 | `TestImpactReportsCallees`, `TestFocusReportsForwardCallees` | Impact/flow expose forward call edges | only callers |
| ML-001 | `TestTypeScriptAliasImportsResolveDependencyEdges` | `@/*` alias resolves to a file edge | 0 dependency edges |
| ML-001 | `TestTypeScriptAliasUnresolvedIsDisclosed` | A missing alias is a local gap | alias treated external |
| ML-002 | `TestJSXComponentUsageBecomesReference` | `<Widget/>` is a reference to Widget | 0 references |
| ML-003 | `TestTypeScriptTypeUsageBecomesReference` | `Post` annotation is a reference to Post | 0 references |
| ML-004 | `TestNonGoPackageImportersAreComputed` | Non-Go package importers are counted | always 0 |
| ML-005 | `TestGeneratedImportIsExcludedNotUnresolved` | `.next/...` import is a known exclusion | dependency incomplete |
| STR-005 | `TestSingleNestedGoModuleProducesArchitectureShape` | module children, not one collapsed module node | 0 module edges |
| STR-006 | same, plus nested-module dependency assertion | incomplete graph cannot claim independence | positive false claim |
| INT-001 | `TestSemanticQueryPrefersFocus` | language server gets the focus symbol | whole question sent |
| GRY-001 | `TestTraceIncludesTestCallSites` | impact trace includes test callers | 13 callers invisible |
| GRY-002 | `TestResolvePrefersExactNamePattern` | exact locate before ranked neighbours | exact symbol not top 3 |
| GRY-003 | `TestRouterKeepsSufficientNativeWithoutEscalation` | sufficient native does not spend external | external replaced native |
| GRY-003 | `TestRouterKeepsSufficientNativeEvenWhenGlobalCoverageIsPartial` | global coverage limit does not force escalation | external always used |
| GRY-003 | `TestRouterEscalatesToExternalWhenNativeIsInsufficient` | external gap is merged with native evidence | native facts lost |
| GRY-003 | `TestRouterNeverPresentsStaleExternalEvidenceAsCurrent` | stale external downgrades outcome | stale returned alone |
| PROV-001 | `TestNormalizeMessageRecognizesErnieToolEnvelope` | ERNIE XML invoke is a call, not answer text | raw XML published |
| BUILD-001 | `TestDoctorReportsStructuralAdapters`, `TestDoctorReportsAllChecks` | doctor names the active adapter set | no structural row |
| BUILD-002 | `TestPortableUpdateRefusalProtectsMultiLanguageBuilds` | multilang update refuses portable replacement | silent downgrade |
| EVID-001 | `TestOverviewQualifiesUnparsedCounts` | unparsed zero is qualified | bare `packages: 0` |
| EVID-002 | `TestTestsViewSeparatesUniqueTestsFromAssociations` | returned/total counts one list | 8 > 4 possible |
| EVID-003 | inventory completeness assertions | template source blocks completeness | templates invisible |
| Adapter coverage | `TestLanguageAdaptersEmitCanonicalFacts` (extended) | every adapter emits a call/reference fact | parse-only could pass |

## Full regression evidence

Final matrix:

```text
go build ./...                     PASS
go test ./...                      PASS   (CGO_ENABLED=1)
CGO_ENABLED=0 go test ./...        PASS
make test-portable                 PASS
make test-multilang                PASS
go test -race ./...                PASS
go vet ./...                       PASS
gofmt -l .                         empty
golangci-lint run ./...            0 issues
make bench                         PASS
```

Focused failures observed during development were fixed rather than skipped:
the initial graph test caught the lost unresolved-import classification, and
the cgo=0 test run caught TypeScript fixtures compiled without a Tree-sitter
adapter (the tests are now correctly cgo-gated; the nested-module tests run in
both modes).

## Test design notes

- The nested-module fixture does not inject a root `go.mod`; it reproduces the
  actual ecommerce layout.
- The alias fixture is JSONC with a comment and a trailing comma.
- The JSX/type fixtures assert no intrinsic or declaration self-reference.
- The router tests use fake providers to control sufficiency, staleness, and
  ambiguity without a real GRY process.
- The ERNIE fixture uses the exact envelope observed from the provider.
