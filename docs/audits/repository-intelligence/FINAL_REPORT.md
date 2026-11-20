# Final report — OHGO repository intelligence audit

Date: 2026-11-20
OHGO: `ade60d9c9d37c6ad7abbeb6679063a71aa04b304` (`v0.1.2-9-gade60d9`)
Targets: ecommerce `00173f5f…`, andev0x-blog `fdb0b009…`

## A. What was actually wrong?

### Build and packaging

| ID | Severity | Status | Root cause |
|---|---|---|---|
| OHGO-BUILD-001 | Medium | Fixed | Doctor did not report compiled adapters, so portable and multi-language builds were indistinguishable |
| OHGO-BUILD-002 | Medium | Fixed | `ohgo update` would replace a multi-language binary with the portable release |
| OHGO-BUILD-003 | Low | Fixed | `make bench` pointed at deleted top-level packages |
| OHGO-BUILD-004 | Low | Fixed | No portable/multi-language test targets |
| OHGO-BUILD-005 | Low | Fixed | `make clean` left `ohgo-multilang` |

### Structure Core

| ID | Severity | Status | Root cause |
|---|---|---|---|
| OHGO-STR-001 | Critical | Fixed | `readModule` checked only the walk root; a nested `go.mod` left module/refs/calls/graph empty |
| OHGO-STR-002 | High | Fixed | `LevelPackage` default made mandatory symbol-neighborhood ineligible in investigate/analysis |
| OHGO-STR-003 | Medium | Fixed | `FocusReport.References` was assigned `ImpactResult.Total` |
| OHGO-STR-004 | High | Fixed | `Impact` had no forward callees |
| OHGO-STR-005 | High | Fixed | One module collapsed all directory edges to self-edges; architecture lost its shape |
| OHGO-STR-006 | High | Fixed | Zero-edge architecture was claimed from an incomplete graph |
| OHGO-EVID-001 | Medium | Fixed | Overview printed bare zero `packages`/`entrypoints` before parsing |
| OHGO-EVID-002 | Low | Fixed | Tests view double-counted tests + associations as `returned` |
| OHGO-EVID-003 | Medium | Fixed | `.gohtml`/`.tmpl` executable templates were not reported unsupported |

### Multi-language adapters

| ID | Severity | Status | Root cause |
|---|---|---|---|
| OHGO-ML-001 | Critical | Fixed | TS/JS resolution accepted only relative imports; `@/*` aliases were external |
| OHGO-ML-002 | High | Fixed | JSX element names were never extracted as references |
| OHGO-ML-003 | High | Fixed | `type_identifier` usage was never extracted as references |
| OHGO-ML-004 | Medium | Fixed | `languagePackagesLocked` never computed importers |
| OHGO-ML-005 | Medium | Fixed | Imports into generated `.next` were counted as unresolved local gaps |

### Semantic resolution, agent orchestration, evidence

| ID | Severity | Status | Root cause |
|---|---|---|---|
| OHGO-INT-001 | Medium | Fixed | Semantic provider sent the natural-language question, not Focus |
| OHGO-INT-002 | Low | Fixed | Semantic references included the declaration |
| OHGO-INT-003 | Medium | Fixed | gopls started at a project root with no module |
| OHGO-GRY-001 | High | Fixed | GRY trace omitted tests |
| OHGO-GRY-002 | Medium | Fixed | GRY locate used ranked lexical search |
| OHGO-GRY-003 | High | Fixed | GRY replaced native evidence; stale external could be returned as current |
| OHGO-PROV-001 | High | Fixed | ERNIE XML tool envelope was published as answer text |
| OHGO-COMP-001 | High | **Confirmed** | Completion accepts explicitly incomplete or unverified answers |
| OHGO-CTRL-001 | Medium | **Confirmed** | Leading “Using read-only …” can force FUZZ |

### Documentation

- `OHGO-DOC-001` (Confirmed): README says LSP/DAP is outside the build while
  a bounded gopls semantic provider is wired. DAP is not exposed as a general
  tool. The README is stale; this was documented, not silently trusted.

## B. Where was OHGO bottlenecked?

| Observed failure | Layer |
|---|---|
| Go cross-package refs/calls = 0 | Structure Core → module discovery → import target mapping |
| Go architecture = independent modules | Structure Core → module projection |
| Impact/flow focus missing | Structural planner → level gate → strategy eligibility |
| `packages: 0` on unparsed overview | Agent tool → evidence rendering |
| TS page→component missing | Language adapter → import resolution → alias table |
| `Post` refs = 0 | Language adapter → reference extraction |
| `importers: 0` for TS packages | Structure Core → package projection |
| Model stopped after `symbols` | Model tool selection; tool description was not a routing guarantee |
| Model invented a prop | Model reasoning; completion policy did not require evidence |
| ERNIE XML as answer | Provider protocol normalization |
| GRY lost native tests/importers | Investigation router → external-first policy |
| Stale GRY answer alone | Investigation router → freshness handling |
| Read-only request needed FUZZ | Workspace intent classifier (conservative) |

The dominant bottlenecks were in the Structure Core and adapter layers, not in
the model. After those fixes, the free-model runs that actually invoked the
relationship tools received correct coordinates and relationships; runs that
stopped after `symbols` did not, which is a model tool-selection failure rather
than a structural one.

## C. How well does OHGO handle Go?

Current ecommerce result:

- 547 indexed files, 305/305 Go parsed, 80 packages, 3,440 symbols,
  9,090 references, 2,412 calls, 461 test declarations, 110 test files,
  2 verified entrypoints.
- The module is correctly identified as `…/backend` even though it is not at
  the project root.
- `NewOrder` has 13 references, 13 direct callers, 7 package importers, and
  4 associated tests; all 13 call sites are source-confirmed.
- Dependency graph: 234 edges. Architecture: layered (0.50 confidence,
  partially observed because `.sql`/`.gohtml`/`.tmpl` are unsupported).
- Scope is partial due two ignored roots, and the tools qualify absence claims.

Remaining Go limits: method references (use callers), interface satisfaction
(no `go/types`), dynamic dispatch (10,986 disclosed static gaps), dot imports,
and multiple sibling Go modules.

This is materially better than the baseline, where the same repository had
4,801 references, 1,035 calls, zero dependency edges, and a false
“independent modules” architecture.

## D. How well does OHGO handle non-Go projects?

**Portable build:** the blog is inventoried (113 files, 77 source), but 0/77
parsed, 0 symbols, 0 references, 0 calls. All TS/TSX/JS are `UNSUPPORTED`;
the two Next.js route modules are convention discoveries with
`verified: false`. This is correctly reported, not a parser failure.

**Multi-language build:** 77/77 parsed, 529 symbols, 817 references,
1,035 calls, 6 dependency edges, 2 discovered entrypoints, structural
completeness true. `.mjs` is parsed by the JavaScript adapter; `.ts`/`.tsx`
by their adapters.

**Tree-sitter syntax coverage:** declarations, imports, calls, JSX references,
type references, and explicit inheritance/implements clauses are extracted for
the tested languages. All ten Tree-sitter adapters parse their fixture.

**Cross-file structural coverage:** aliases now resolve; `app -> components`
and `app -> lib` are observed; `impact TerminalBlog` reports the page reference
and importer.

**Semantic resolution:** none for TypeScript in this build. The graph is
name-based and unique-match only; ambiguous names are `AMBIGUOUS` and produce
no edge. `Post` receives 27 references, but an unresolved external type stays
unresolved and is counted as a blind spot.

Content: the blog is Markdown, not MDX. `lib/post-loader.ts` reads
`content/posts/*/README.md`, parses frontmatter, and yields posts; the
ReaderPanel renders Markdown. No MDX dependency or file exists.

## E. How well do free hosted models work with OHGO?

Four zero-priced hosted models were verified; three completed the matrix.
Local models and paid fallbacks were not used.

| Model | Tool-use reliability | Understanding | Evidence interpretation | Main weakness |
|---|---|---|---|---|
| `opencode-go/beam-free` | Best overall: used refs/structure/read in final runs | Strong Go and TSX overview | Correctly separated discovered entrypoints and static association | Occasional inference from directory names; one docs review |
| `openrouter/baidu/ernie-5.5-flash-preview:free` | Good, but sometimes stopped early | Strongest cited explanations (overview, outbox) | Correct source-grounded distinctions | Coverage answer was a meta statement; protocol envelope (fixed); rate limits |
| `openrouter/cohere/north-mini-code:free` | Weak: 5/8 final tasks no answer; 2 failed reads | Correct when it read source | Correct page/impact when it answered, but invented a prop in one run | Empty completions, short tool chains, hallucination |
| `opencode-go/longcat-2.5-free` | Not established | Not established | Not established | Repeated HTTP 429 |

Provider restrictions are not OHGO defects: shared-pool 429s, empty
completions, and upstream unavailability were reported as failures. The shared
runtime issues are model tool-selection (stopping after `symbols`) and the
completion policy accepting incomplete answers.

## F. Does GRY provide measurable value?

- **Pre-fix native:** NewOrder 0 references/callers. **GRY:** 15 inbound rows
  at depth 2, 13 direct; all 13 match current source. Marginal gain 15.
- **Post-fix native:** 13 references, 13 callers, 7 importers, 4 tests; the
  native answer is sufficient. The router spends 0 external requests and GRY
  is not used. Marginal gain 0.
- **Exact locate:** GRY exact name-pattern returns the declaration that ranked
  search missed; useful as a fallback, not needed when native symbols work.
- **Freshness:** the ecommerce GRY index predates the commit; trace/search
  payloads lack generation, so the adapter marks freshness unknown. Stale
  evidence now downgrades the combined result rather than being returned as
  current.
- **Blog:** no GRY project; `NOT_INDEXED`, native answer preserved. No GRY
  claim was made.

Hybrid routing is justified only when native is insufficient. The router now
implements native-first and merges only on a gap; on these repositories the
fixed native path is sufficient.

## G. What was fixed?

All fixed defects are listed in `BUGS.md` with evidence and in `FIXES.md` with
files. The key before/after:

| Surface | Before | After |
|---|---|---|
| Ecommerce module | empty | `…/backend` |
| Ecommerce refs / calls | 4,801 / 1,035 | 9,090 / 2,412 |
| Ecommerce dependency edges | 0 | 234 |
| Ecommerce architecture | independent modules | layered |
| NewOrder refs / callers / tests | 0 / 0 / 0 | 13 / 13 / 4 |
| Blog refs | 0 | 817 |
| Blog dependency edges | 0 | 6 |
| Blog TerminalBlog impact | 0 refs | 1 ref, 1 importer |
| Doctor structural row | absent | portable: Go only; multilang: 11 adapters |
| `make bench` | failed | passes |
| ERNIE docs answer | raw XML, COMPLETED | normalized or NEEDS_REVIEW |

Verification: `go build`, `go test` in both CGO modes, `go test -race`,
`go vet`, `gofmt -l`, `golangci-lint`, `make bench` all pass.

## H. What remains unresolved?

1. **Completion policy** (`OHGO-COMP-001`) accepts an answer that explicitly
   leaves requested fields unverified, and can accept a plausible invented
   field. A deterministic task-field evidence contract is needed.
2. **Go semantics**: interface implementation, method-value references, and
   dynamic dispatch are not resolved. This is documented, not silently filled.
3. **Multi-module Go workspaces**: one unambiguous module is supported; sibling
   modules are not. Imports across them remain unresolved.
4. **Non-Go semantic resolution**: no TypeScript/Java/etc. language server.
   Unique-name syntax resolution is the ceiling.
5. **Multi-language releases**: GitHub/Docker artifacts are portable; the
   multi-language build is local. `ohgo update` now refuses the downgrade but
   does not provide an alternative artifact.
6. **GRY freshness**: trace/search payloads do not expose generation, so the
   adapter can only mark freshness unknown. A status probe would add cost.
7. **Generated output classification** is currently TS/JS-specific.
8. **Read-only phrasing** can still false-escalate to FUZZ (`OHGO-CTRL-001`).
9. **Projection leakage**: a model can copy the runtime-state block into its
   public answer; the boundary is not enforced.
10. **Token accounting**: headless session persistence does not record provider
    token usage, so the model matrix reports latency/tool counts, not tokens.
11. **Java/C#/Kotlin/Ruby/C/C++ import resolution** is convention-based; a
    Maven/Gradle layout or namespace/file mismatch remains unresolved.
12. **README LSP/DAP statement** is stale relative to the bounded gopls path.

## I. Final product readiness

The native Structure Core is now materially more trustworthy on the tested
surface. It resolves the real module, returns the correct cross-package
relationships, discloses unsupported templates/SQL, and no longer turns a
single nested module into a false architecture claim. The TSX adapter now
answers the page→component question that a Next.js user actually asks.

The product is **not** ready to treat `COMPLETED` as proof that a user's
information need was satisfied. The completion policy, multi-module support,
and non-Go semantic resolution remain real limits. The documented
multi-language distinction is now visible in `doctor` and protected in
`update`, but release artifacts are still portable.

Documented capabilities and tested behavior now match for the core structural
path. They do not yet match for "completion means the request is satisfied",
for multi-module Go, or for semantic understanding beyond Go.

## Acceptance checklist

- [x] Both targets inspected at recorded commits; working trees preserved.
- [x] Portable and multi-language build behavior verified.
- [x] Actual binary adapter sets established and now reported by doctor.
- [x] Native Go AST benchmarked on ecommerce.
- [x] TS/TSX Tree-sitter benchmarked on andev0x-blog.
- [x] Every documented adapter checked with a suitable fixture.
- [x] Structural and semantic capabilities evaluated separately.
- [x] Three eligible hosted free models were exercised across the full suite;
      their empty and failed runs are recorded. A fourth was rate-limited and
      recorded.
- [x] No local model and no paid fallback used.
- [x] Same task suite used on both repositories.
- [x] Tool traces retained per session.
- [x] Model failures separated from OHGO defects.
- [x] GRY was not reported as used when the router skipped it.
- [x] GRY value measured against the native baseline; candidates
      source-confirmed.
- [x] Bugs documented with root cause and evidence.
- [x] Confirmed fixes implemented with tests.
- [x] Full relevant tests pass in both CGO modes.
- [x] Both repositories re-measured after fixes.
- [x] Before/after results recorded.
- [x] Unresolved issues explicitly listed.
- [x] No commit, push, tag, or release performed.
