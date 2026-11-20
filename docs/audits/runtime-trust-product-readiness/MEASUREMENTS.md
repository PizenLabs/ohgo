# Measurements

All measurements are read-only. The target repositories were not initialized,
formatted, or modified by these probes. The opt-in probe is
`internal/intelligence/ast/target_audit_test.go`; it keeps its snapshot in
memory and is skipped unless `OHGO_AUDIT_TARGET` is set.

## Go target — ecommerce

Command:

```sh
CGO_ENABLED=0 OHGO_AUDIT_TARGET=/Users/anvndev/Documents/Project/Golang/ecommerce \
  OHGO_AUDIT_SYMBOL=NewOrder \
  go test ./internal/intelligence/ast -run TestAuditTargetSnapshot -v
```

| Metric | Value |
|---|---|
| Module | `github.com/bkielbasa/go-ecommerce/backend` |
| Module root | `backend/` |
| Module ambiguous | false |
| Files / source files | 497 / 305 |
| Parsed Go files | 305 |
| Packages | 80 |
| Symbols | 3,440 |
| References | 9,090 |
| Calls | 2,412 |
| Test files / test declarations | 110 / 461 |
| Entry points | 2 |
| Scope complete | false (2 excluded roots) |
| Inventory complete | true |
| Structural complete | false (57 `.gohtml`, 77 `.sql`, 16 `.tmpl` unsupported) |
| `NewOrder` references / callers / importers / associated tests | 13 / 13 / 7 / 4 |

Prior audit baseline for comparison: 4,801 references, 1,035 calls, 0
dependency edges, `NewOrder` 0 refs/callers. The post-fix numbers are unchanged
by this closure audit, which is the required regression result.

## Non-Go target — andev0x-blog

### Portable build

```sh
CGO_ENABLED=0 OHGO_AUDIT_TARGET=/Users/anvndev/Documents/Project/Blog/andev0x-blog \
  OHGO_AUDIT_SYMBOL=TerminalBlog \
  go test ./internal/intelligence/ast -run TestAuditTargetSnapshot -v
```

| Metric | Value |
|---|---|
| Files / source files | 111 / 77 |
| Parsed files | 0 |
| Symbols / references / calls | 0 / 0 / 0 |
| Unsupported | `.mjs` 2, `.ts` 7, `.tsx` 68 |
| Inventory complete | true |
| Structural complete | false |

This is a build capability property, not a parser failure: the portable binary
has no Tree-sitter adapters.

### Multi-language build

```sh
CGO_ENABLED=1 OHGO_AUDIT_TARGET=/Users/anvndev/Documents/Project/Blog/andev0x-blog \
  OHGO_AUDIT_SYMBOL=TerminalBlog \
  go test ./internal/intelligence/ast -run TestAuditTargetSnapshot -v
```

| Metric | Value |
|---|---|
| Files / source files | 111 / 77 |
| Parsed files | 77 |
| Symbols | 529 |
| References | 817 |
| Calls | 1,035 |
| Test files / test declarations | 0 / 0 |
| Entry points | 2 |
| Scope complete | true |
| Inventory complete | true |
| Structural complete | true |
| `TerminalBlog` references / importers | 1 / 1 |

`TerminalBlog` has one observed reference and one importer. The zero test-file
count is a complete inventory observation; it is not proof that the project has
no tests, because test tooling and excluded paths were not inspected.

## Sibling Go modules

Fixture:

```text
a/go.mod  module example.com/a
a/a.go
b/go.mod  module example.com/b
b/b.go
```

`SnapshotInfo` reports `module: ""`, `module_ambiguous: true`,
`module_candidates: ["a","b"]`. The files are inventoried and parsed; imports
are not resolved across the two modules. The structure header says so.

## Build and adapter matrix

| Binary | Command | Adapters reported by `doctor` | SHA-256 |
|---|---|---|---|
| Portable | `make build` | 1 (`go`) | `0dec6d53…` |
| Multi-language | `make build-multilang` | 11 (`go` + 10 Tree-sitter) | `7885757a…` |

Final hashes are recorded in `FINAL_REPORT.md` after the last rebuild.

## Model ledger

The sandbox used a fresh `HOME` so sessions and token rows were isolated from
the user's real `~/.ohgo`. Token accounting is now durable, so these are
measured provider values, not estimates.

| Session | Model | Workspace | Turns | Input | Output | Wall time |
|---|---|---|---|---|---|---|
| 2 | `opencode-go/beam-free` | fuzz | 6 | 35,126 | 1,004 | 11.9 s |
| 3 | `opencode-go/beam-free` | ask | 3 | 12,436 | 1,095 | 11.1 s |
| 4 | `opencode-go/beam-free` | ask | 4 | 36,498 | 1,754 | 11.8 s |
| 5 | `opencode-go/beam-free` | fuzz | 3 | 14,485 | 594 | 8.1 s |
| 6 | `openrouter/baidu/ernie-5.5-flash-preview:free` | ask | 8 | 48,811 | 900 | 16.9 s |
| 7 | `opencode-go/longcat-2.5-free` | fuzz | 3 | 18,513 | 1,389 | 35.9 s |
| 8 | `openrouter/baidu/ernie-5.5-flash-preview:free` | ask | 5 | 27,965 | 682 | 11.0 s |
| 9 | `openrouter/baidu/ernie-5.5-flash-preview:free` | ask | 5 | 25,126 | 1,876 | 13.6 s |
| 11 | `opencode-go/beam-free` | ask | 4 | 19,097 | 1,527 | 9.5 s |
| 12 | `opencode-go/beam-free` | ask | 4 | 19,044 | 2,379 | 16.0 s |
| 13 | `opencode-go/longcat-2.5-free` | fuzz | 5 | 31,671 | 1,993 | — (before noun-form check) |
| 14 | `opencode-go/longcat-2.5-free` | fuzz | 5 | 41,475 | 2,173 | — (overclaim reviewed) |

Sessions 1 and 10 have `usage_turns = 0`: the request was refused before the
provider was called. That is the required distinction between “not measured”
and “measured zero”.
