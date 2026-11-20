# Test matrix

## Baselines

| ID | Build | GRY | Repository | Result |
|---|---|---|---|---|
| A | portable (CGO=0) | disabled | ecommerce | Go complete; SQL/templates unsupported |
| A | portable (CGO=0) | disabled | blog | 0/77 parsed; all TS/TSX/JS `UNSUPPORTED`; 2 route discoveries |
| B | multi-language (CGO=1) | disabled | ecommerce | 305/305 Go parsed; 9,090 refs; 2,412 calls; 234 dependency edges |
| B | multi-language (CGO=1) | disabled | blog | 77/77 parsed; 529 symbols; 817 refs; 6 dependency edges |
| C | multi-language | unavailable/not indexed | blog | native-only; GRY not called; no false GRY claim |
| D | multi-language | enabled, pre-fix native gap | ecommerce | GRY 15 inbound rows for NewOrder; 13 direct source-confirmed |
| E | multi-language | native-first, external only on gap | ecommerce | native sufficient; external_requests=0 |

## Structural scenarios

| Scenario | Evidence |
|---|---|
| Repo purpose and boundaries | native overview + source reads; model answers in `MODEL_EVALUATION.md` |
| Entrypoints | ecommerce: 2 verified `main.main`; blog: 2 convention routes, 0 verified |
| Go declaration | `NewOrder` at `backend/checkout/domain/order.go:99` |
| Go direct callers | 13, all test-side; 4 associated tests |
| Go package graph | `NewOrder` package has 7 importers; 234 total edges |
| Go architecture | layered (module-child resolution), 234 edges, partial coverage |
| TSX declaration | `TerminalBlog` at `components/blog/TerminalBlog.tsx:24` |
| TSX page relationship | `app/page.tsx:7` references `TerminalBlog`; 1 importer |
| TSX type usage | `Post` has 27 references |
| TSX dependency graph | 6 directory edges; real hook/UI cycle → mixed |
| Tests | ecommerce 461 declarations / 110 files / 4 associated with NewOrder; blog 0 |
| Unsupported coverage | `.gohtml(57)`, `.sql(77)`, `.tmpl(16)`; blog CSS/Markdown/assets are not source |
| Test/coverage distinction | tools and model answers explicitly call associations static, not coverage |
| Negative claims | `scope: PARTIAL`, `structural: PARTIAL/UNPARSED`, `result: TRUNCATED` qualify absence |

## Adapter matrix

All 12 languages/extensions parsed and emitted facts; see `BUILD_MATRIX.md`.
Portable A is the negative control: the same blog files are inventoried but
not parsed, and the extension list is reported as unsupported.

## Agent scenarios

The model suite covered overview, impact, coverage, and docs-vs-implementation
on both repositories for three eligible models. Tool traces, answers, and
verdicts are summarized in `MODEL_EVALUATION.md`; raw traces are retained in
the benchmark directory.

Observed failure classes:

| Class | Example | Attribution |
|---|---|---|
| Provider rate limit | LongCat 429; inclusionAI 429 | provider |
| Provider empty completion | Cohere 5/8 final tasks with no answer | model/provider |
| Wrong tool selection | beam/ernie stopped after `symbols` on an early impact prompt | model |
| Invalid tool arguments | Cohere overview read paths failed twice | model |
| Protocol envelope | ERNIE XML published as answer | OHGO (fixed) |
| Incomplete answer marked COMPLETED | ERNIE earlier impact; Cohere invented prop | OHGO completion policy (open) |
| Prompt escalation | “Using read-only …” required FUZZ | OHGO classifier (documented, conservative) |
| Runtime evidence | native focus, tests, importers missing before fixes | OHGO (fixed) |
| Context leakage | Cohere copied runtime state block into answer | model; projection boundary open |

## GRY matrix

| Query | Native | GRY | Source-confirmed | Verdict |
|---|---|---|---|---|
| NewOrder impact, pre-fix | 0 refs/callers | 15 rows (13 direct, 2 indirect) | yes, every row | GRY gain 15 |
| NewOrder impact, post-fix | 13 refs, 13 callers, 7 importers, 4 tests | skipped (native sufficient) | n/a | GRY gain 0 |
| NewOrder locate | 1 exact declaration | exact name-pattern gives 1 | yes | both correct |
| Blog | 6 edges, 817 refs | no project | n/a | NOT_INDEXED, native only |
| Stale ecommerce index | current native | stale/unknown freshness | rows checked | not treated as current |

## Commands

```sh
make build
make build-multilang
go test ./...
CGO_ENABLED=0 go test ./...
make test-portable
make test-multilang
go test -race ./...
go vet ./...
gofmt -l .
golangci-lint run ./...
make bench
```

All pass after the fixes. The model runner and raw traces are not part of the
OHGO repository; they live under the session temp directory documented in
`MODEL_EVALUATION.md`.
