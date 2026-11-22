# Structure Engineering Audit — Performance

## 1. Method

- Machine: Apple Silicon (darwin/arm64), same host for every run.
- Baseline: detached worktree at `da0750e`, built with the same Go toolchain.
- Current: the audited working tree.
- Both binaries were built once (`go build -o … ./bench/structural`) and run
  directly, so process startup and compilation are outside the measurement.
- The blog benchmark used the real repository. The synthetic benchmark
  generates deterministic Go repositories (tiny 12 files, small 80, medium
  384) and reports the engine’s own cold/warm/incremental paths.
- Timings are single-process wall-clock measurements from the existing
  benchmark; the host is shared, so medians and interleaved runs are reported
  rather than one “best” number.
- No optimization was applied by weakening correctness. The only hot-path
  changes are one file map in the language graph and import-aware narrowing for
  names that have more than one candidate.

## 2. Real repository — andev0x-blog

| Metric | Baseline | Current | Delta |
|---|---:|---:|---:|
| `intent=project` analysis, median of 9 | 1.476 ms | 1.498 ms | +1.5% |
| `intent=architecture` analysis, median of 7 | 71.854 ms | 72.221 ms | +0.5% |
| Peak RSS, compiled benchmark (3 runs) | 21.30 MB | 21.71 MB | +1.9% |
| Parsed files | 77 | 77 | 0 |
| Snapshot nodes (architecture) | 27 | 27 | 0 |
| Dependency nodes / edges | 25 / 6 | 25 / 6 | 0 |
| Node JSON | 10,763 B | 10,763 B | 0 |
| Module-summary JSON | 2,425 B | 2,425 B | 0 |

The deltas are within run-to-run noise on this host. The current engine does
more work per non-Go reference (import-aware narrowing), but only when a name
has more than one candidate, and the aggregate cost is below the measurement
noise for this 77-file project.

## 3. Synthetic medium (384 Go files, 8 modules)

One representative run of the compiled benchmark; the cold-architecture result
was also measured 7 times interleaved.

| Metric | Baseline | Current |
|---|---:|---:|
| Cold project analysis | 5.296 ms | 5.165 ms |
| Cold architecture analysis | 27.068 ms | 24.280 ms |
| Warm architecture (cache hit) | 3.627 ms | 3.370 ms |
| Incremental architecture (1 changed file) | 3.420 ms | 3.521 ms |
| Snapshot nodes | 74 | 74 |
| Node JSON | 29,752 B | 29,752 B |
| Module-summary JSON | 2,889 B | 2,889 B |

Interleaved cold-architecture medians (7 paired runs): baseline **25.894 ms**,
current **25.360 ms** (~2% faster, noise-level). The earlier single-run 47 ms
current reading was host contention; the paired run is the reliable one.

Output sizes are byte-identical because the synthetic project is Go-only and
the new non-Go paths are not exercised.

## 4. What was not optimized

- No concurrency setting was changed. Parsing is already bounded by the
  adapter pass and the index lock; raising workers would trade correctness for
  unmeasured throughput.
- No repository-wide scan was added. The incremental path still reparses only
  changed files and re-derives projections from retained syntax.
- No second index or graph was introduced. The new `byFile` map is inside the
  existing language graph and dies with the snapshot.
- No large-file or network-path benchmark was run. The 64 KiB adapter bound and
  cancellation tests are the existing safeguards.

## 5. Conclusion

The audit fixes are correctness-first and did not introduce a measurable
performance regression on the real target. Peak memory rose by less than 2% in
the compiled benchmark. Warm and incremental analyses remain an order of
magnitude cheaper than cold analysis, and the result cache still avoids
recomputation only when the snapshot and analyzer semantics match.
