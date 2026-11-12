# Phase 8 — Structural runtime baseline

> **Method:** deterministic Go benchmarks, not model calls. The cold benchmark
> builds a snapshot of the real repository `cmd/ohgo` test working directory.
> The incremental benchmark copies only Go source, `go.mod`, and `go.work` to a
> temporary tree, indexes once, then appends one declaration and runs a lookup
> per iteration. The synthetic benchmark measures 200 generated Go files.

| Field | Value |
|---|---|
| Date | 2026-11-12 |
| Host | Apple M1 Pro, darwin/arm64 |
| Revision | `b8e7057` plus the Phase 2 working tree |
| Toolchain | Go 1.27 |
| Fixture | current OHGO tree, 737 Go files (plus manifests/configs) |
| Persistence | none (`graph.db` not created) |

## Commands

```sh
go test ./cmd/ohgo -run '^$' -bench 'BenchmarkPhase8' -benchtime=3x
go test ./internal/intelligence/ast -run '^$' -bench 'BenchmarkColdIndex|BenchmarkIncrementalSingleFileUpdate|BenchmarkWarmStructuralQueries' -benchtime=3x
OHGO_PHASE8=1 go test ./cmd/ohgo -run TestPhase8StructuralSnapshot -count=1
```

## Results

| Measurement | Result |
|---|---|
| Real repository cold index | ~411 ms/op, ~332 MB allocated/op |
| Real repository one-file incremental lookup | ~99 ms/op, ~87 MB allocated/op, `parsed_files=1` |
| Real repository warm structural query mix | ~70 ms/op, ~29 MB allocated/op for symbols + files + packages + impact |
| Synthetic 200-file cold index | ~5.5 ms/op, ~1.3 MB allocated/op |
| Synthetic 200-file one-file update | ~1.3 ms/op, ~0.37 MB allocated/op, `parsed_files=1` |
| Synthetic warm query mix | ~3.0 ms/op, ~0.67 MB allocated/op |

The incremental path is materially cheaper than a cold rebuild on the real
repository, and the parse counter proves the one-file guarantee rather than
inferring it from wall time: a one-file edit parses 1 of 737 Go files. Warm
queries include the bounded file and package inventory pages and a symbol impact
composition.

## Interpretation limits

- The first structural query is still lazy; these numbers do not claim startup
  work. `ProbeFacts` remains a cheap marker probe and does not build the index.
- Stat-based freshness can miss a content change that preserves size and mtime
  exactly. `ForceRefresh` exists for that case; the ordinary path avoids hashing
  every file on every query.
- Relationship re-resolution is global over retained raw syntax. It does not
  re-parse unchanged files, but it is not claimed to be O(1) after a declaration
  change.
- Memory numbers are total allocations per operation, not retained heap. Retained
  heap was not profiled as a separate number in this baseline. Raw reference and
  call syntax is retained to make incremental re-resolution possible; the cost
  is proportional to the relationship set, not source text.
