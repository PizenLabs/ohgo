# Adaptive Structural Runtime — Benchmark Record

Status: measured on macOS arm64 (Apple M1 Pro), Go 1.27.3, cgo enabled.
These are local observations, not service-level guarantees.

## Synthetic mixed-language fixture

Fixture: two TypeScript files, one Java file, one Rust file, one C# file, one
Kotlin file, one Ruby file, and one C++ file. Eight assertions are relevant:
the overview must not parse, a targeted lookup must parse the mix, and one
TypeScript edit must reparse exactly that file.

Command:

```sh
go test ./internal/intelligence/ast -run '^$' \
  -bench 'BenchmarkPhase9' -benchmem -count=1
```

| Benchmark | Time / op | Alloc / op | Allocs / op |
|---|---:|---:|---:|
| Adaptive overview (cold inventory) | 523 µs | 77 KB | 746 |
| Targeted multi-language lookup (cold parse) | 1.02 ms | 204 KB | 1,741 |
| Incremental one-file update (warm) | 417 µs | 101 KB | 709 |

Observed behavior:

- The overview reports `parsed_files: 0`, `pending_files: N`, per-language
  `UNPARSED` coverage, and `structural_complete: false`.
- The targeted lookup parses all pending supported files and publishes
  `COMPLETE` per-language coverage in the fixture; cross-language call sites
  resolve only where the target name is unique.
- The incremental update parses one changed TypeScript file and reuses the
  unchanged files; `LastParsedFiles` is 1 and `LastReusedFiles` is non-zero.

## OHGO self-repository

Command (gated benchmark; one iteration for a cold measurement):

```sh
go test ./cmd/ohgo -run '^$' \
  -bench 'BenchmarkPhase8Structural(ColdIndex|ColdParsed)' \
  -benchtime=1x -benchmem -count=1
```

| Path | Time / op | Alloc / op |
|---|---:|---:|
| Metadata-first cold inventory | 26 ms | 3.5 MB |
| Full parsed cold snapshot | 485 ms | 411 MB allocated (cumulative) |

The full parsed path includes native Go parsing plus relationship resolution.
The adaptive runtime does not pay the 485 ms cost for a broad overview; it pays
the inventory cost, and the parsed cost only when a targeted or impact query
requests declarations or relationships.

## Real-project validation

```sh
OHGO_PHASE9=1 go test ./cmd/ohgo -run TestSelfAnalysisStructuralRuntime
OHGO_PHASE8=1 go test ./cmd/ohgo -run TestPhase8StructuralSnapshot
go test ./cmd/ohgo -run 'TestNextJSAnalysis|TestGoProjectAnalysis'
```

The Next.js fixture verifies ASK-style overview without source parsing,
TypeScript/TSX detection, Next.js framework discovery, route entrypoint
convention, targeted symbol lookup, reference call sites, and an explicit
`IMPLEMENTS` relation. The Go fixture verifies that the native parser, tests,
entrypoint, reference, and evidence paths are unchanged. The self-analysis
baseline verifies the real repository's own Go snapshot and impact lookup;
unsupported Python/shell utilities remain disclosed instead of being folded
into the Go claim.

## Known measurement limitations

- The parser dependency is `github.com/smacker/go-tree-sitter` pinned at
  `v0.0.0-20240827094217-dd81d9e9be82`; grammar versions are fixed by that
  module. No grammar is downloaded or loaded at runtime.
- The synthetic fixture is deliberately small. It measures activation and
  incremental behavior, not parser throughput at repository scale.
- Memory figures are Go benchmark allocation totals, not resident-set
  measurements. The syntax tree and parser are closed after each file.
- The benchmark machine is not dedicated; compare relative paths, not absolute
  numbers, across runs.
