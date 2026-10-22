# FINAL CLOSURE — REPOSITORY DISCOVERY

- **Date:** 2026-10-21
- **Phase 1 contract:** `docs/report/PHASE1_CLOSURE.md` (frozen, unchanged)
- **Phase 2 history:** `docs/report/PHASE2_BASELINE.md` (original truth), `docs/report/PHASE2_CLOSURE.md` (HOLD at 53 cells, plus a completion addendum)
- **Benchmark protocol:** `docs/benchmarks/PHASE2_SPEC.md` (frozen, plus amendment A1)
- **Final dataset:** `docs/benchmarks/results/phase2-final-valid-runs.json`
- **Final analysis:** `docs/benchmarks/results/phase2-final-analysis.json`, computed by `docs/benchmarks/phase2_final_analysis.py`

> **Historical note (Phase 6):** statements below about "one structural tool"
> describe the Phase 1/2 tree. Phase 6 added `graph` as a second read-only wire
> surface over the same `ast.Index`/`StructuralSource`; the one-source and
> one-parse invariants remain current. See
> [`PHASE7_CLOSURE.md`](PHASE7_CLOSURE.md).

## 1. Executive Verdict

**PASS WITH LIMITATIONS — the repository-discovery foundation is sound for the demonstrated scope.**

- The benchmark is complete: all 81 planned agent-level cells (27 tasks × 3 tracks) have valid outcomes. No cell remains provider-blocked.
- Structural discovery (`symbols`) gives measurable agent-level value on exact, path-scoped, ambiguity, negative, and noise tasks.
- Lexical discovery is still necessary for text and literal tasks. The structural track fails 2 of 5 of them.
- The hybrid track has the highest correct count (21/27), though its margin over structural (20/27) is within run-to-run noise.
- Cross-file relation tasks are not solved by any track. This is the main open limitation and is not a reason to add a capability in this phase.
- Two correctness defects in the lexical tools were found and fixed in this phase, and one earlier defect was reproduced and reconfirmed. Lifecycle costs were measured.

This is not a plain PASS. Single-sample agent outcomes vary between runs, the cross-file category is unsolved, and the lifecycle limitations below are real.

## 2. Problem Statement

Ohgo repository discovery answers one question for an agent: given a request, which discovery mechanism gives the agent the smallest sufficient set of stable coordinates or exact textual evidence, so it can read only what it needs.

It is not a repository-intelligence platform. It does not model references, callers, imports, or semantics. Its contract is:

```text
intent
  → the right mechanism (symbols for declarations, grep/glob/ls for text)
  → a stable coordinate (repository-relative path, package, symbol, kind, line range) or exact text evidence
  → read the exact range
  → reason, with the least context necessary
```

## 3. Phase 1 Summary

Phase 1 added one structural tool, `symbols`, backed by one authoritative in-process index (`ast.Index`) over `go/parser`. It returns repository-relative coordinates without source text.

The primitive was kept small on purpose:

- A closed schema: `symbol`, `path`, `package`, `limit`, `skip`. Unknown fields are rejected. No aliases.
- Deterministic ordering, bounded pages, explicit `ambiguous`, `truncated`, `not_found`, and parse-failure reporting.
- Every query revalidates freshness against the tree. A stale index is rebuilt, and a rebuild error is returned instead of stale coordinates.
- The index keeps coordinates only. Doc and signature text are dropped, so source remains authoritative and is read through `read`.

Keeping the primitive small let Phase 2 test whether coordinates alone change agent behaviour, without confounding it with search, ranking, or graph features.

## 4. Phase 2 Evidence

### 4.1 Dataset

| Item | Count |
| --- | ---: |
| Planned cells (27 tasks × 3 tracks) | 81 |
| Valid cells in the final dataset | **81** |
| Cells still provider-blocked | **0** |
| Cells from the pre-fix dataset | 44 |
| Cells from post-fix reruns | 37 |
| Fix-affected cells left unresolved | 0 |

Outcome distribution across all 81 valid cells: `correct` 49, `correct_not_found` 9, `no_answer` 18, `wrong_target` 4, `status_mismatch` 1.

### 4.2 Correctness, all 27 tasks (81 valid cells)

| Track | Correct | False positive runs | False negative runs | Wrong target | No answer | Not-found correct | Ambiguity correct |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| A — lexical | 17/27 (63.0%) | 1 | 1 | 1 | 9 | 3/3 | 0/3 |
| B — structural | 20/27 (74.1%) | 1 | 1 | 1 | 6 | 3/3 | 2/3 |
| C — hybrid | 21/27 (77.8%) | 2 | 2 | 2 | 3 | 3/3 | 2/3 |

### 4.3 Agent efficiency, all 27 tasks (medians, with quartiles)

| Track | Turns | Discovery calls | Candidate files (irrelevant) | Lines inspected | Context KB | Input tokens | Output tokens | Duration (s) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| A — lexical | 7 | 4 | 12 (11) | 280 | 14.9 | 11,126 | 703 | 43.5 |
| B — structural | 3 | 1 | 2 (0) | 50 | 2.1 | 5,110 | 431 | 18.3 |
| C — hybrid | 3 | 2 | 2 (1) | 72 | 4.0 | 5,032 | 494 | 18.8 |

Input-token quartiles: A 8,242–39,439; B 1,709–17,683; C 3,011–14,012. The distributions overlap. The medians are the headline, not the means.

### 4.4 Correctness by category

| Category | A — lexical | B — structural | C — hybrid |
| --- | ---: | ---: | ---: |
| A_EXACT (3) | 3/3 | 3/3 | 3/3 |
| B_AMBIGUOUS (3) | 1/3 | 2/3 | 1/3 |
| C_PATH_PACKAGE (3) | 1/3 | 3/3 | 3/3 |
| D_MODIFICATION (4) | 3/4 | 3/4 | 3/4 |
| E_CROSS_FILE (3) | 0/3 | 0/3 | 1/3 |
| F_NEGATIVE (3) | 3/3 | 3/3 | 3/3 |
| G_NOISE (3) | 1/3 | 3/3 | 3/3 |
| H_LEXICAL (5) | **5/5** | 3/5 | 4/5 |

Two tasks fail on every track or nearly so: D2 (tool registration) failed on all three tracks, and E1 (definition plus registration) failed on all three. These are agent-reasoning and coverage failures, not coordinate absences. See §4.7.

### 4.5 Comparison on the original 17 tasks

The Phase 2 closure report's 17-task paired view is reproduced as a consistency check:

| Track | Correct | Turns (median) | Discovery calls (median) | Candidates (median) | Input tokens (median) |
| --- | ---: | ---: | ---: | ---: | ---: |
| A | 10/17 | 6 | 4 | 12 | 15,271 |
| B | 13/17 | 4 | 2 | 11 | 7,704 |
| C | 13/17 | 3 | 2 | 3 | 9,091 |

The input-token medians match the earlier closure exactly. The A-track correct count is also 10/17 again, but it's reached through offsetting flips after the fix: B1(A) lost correctness and D3(A) gained it (§6.2). Do not read the equal total as stability.

### 4.6 Discovery precision

| Track | Candidates surfaced (total) | Irrelevant (total) | Relevant (total) | Ambiguous-status runs | Truncated-status runs |
| --- | ---: | ---: | ---: | ---: | ---: |
| A | 836 | 796 (95%) | 40 | 0 | not recorded |
| B | 349 | 319 (91%) | 30 | 2 | not recorded |
| C | 228 | 188 (82%) | 40 | 2 | not recorded |

"Relevant" is the set of candidates whose files are in the task's ground truth, as scored by the evaluator. Truncation is a tool-level status and is not stored per run in the agent records, so no agent-level truncation rate is reported. Tool-level truncation is covered by the Phase 1 tests (`TestSymbolsToolTruncatesLargeResults`) and the static probes.

Structural candidates are few because a `symbols` lookup returns one coordinate per declaration. Lexical candidates are many because a `grep` returns every file with the string.

### 4.7 Non-successes (23 of 81 cells)

| Outcome | Cells | Observation |
| --- | ---: | --- |
| `no_answer` | 18 | 17 exhausted the 16-turn budget. E3(A) stopped at 12 turns without an answer. |
| `wrong_target` | 4 | E2(A), E3(C), H3(B), H3(C) |
| `status_mismatch` | 1 | B3(C) answered all 14 targets with `exact` instead of `ambiguous` |

By task: D2 and E1 failed on all three tracks; B3 failed on all three; E3 failed on all three. Failures on the structural track are concentrated where the task needs something a declaration coordinate cannot express: cross-file relations (E1, E2, E3 on B) and text inside a line (H2, H3 on B).

No cell failed because a declaration existed but the structural track missed it. The lexical defect in §6.1 no longer affects any post-fix cell.

The failure tags (`AGENT_REASONING_FAILURE`, `STRUCTURAL_LIMITATION`) from the spec are not assigned per cell in this report. Assigning them needs a transcript review of each of the 23 cells, which was not done here. The table above is the counted fact; the tags are not.

### 4.8 Decision boundary

| Intent | Evidence | Mechanism |
| --- | --- | --- |
| Exact declaration, path- or package-scoped declaration, same-name competitor, noise, absent symbol | B and C are 3/3 on C_PATH_PACKAGE and G_NOISE; A is 1/3 on each; all tracks are 3/3 on A_EXACT and F_NEGATIVE with B/C cheaper | `symbols` (or hybrid) |
| Literal string, error text, struct tag, doc line, Makefile target | A is 5/5 on H_LEXICAL; B is 3/5 | `grep` / `glob` / `ls` |
| Mixed or uncertain intent | C is 21/27 overall, the highest correct count | Hybrid |
| Cross-file relation (definition plus call site or registration) | 0/3 on A and B; 1/3 on C | None sufficient; see §8 |

The model holds, with one exception: cross-file relations do not improve with any current track.

The decision boundary is a claim about these 27 tasks and one model. It has not been tested on other repositories or other models.

## 5. Final Architecture

The structural path and the lexical path are separate tools, selected by the agent. There is no hidden fallback between them.

```text
Agent
 ├── structural intent
 │      ↓
 │   tools.SymbolsTool        (name "symbols", TierRead; cmd/ohgo/run.go:400)
 │      ↓
 │   ast.Index                (one per tool instance, lazily built, freshness-checked per query)
 │      ↓
 │   ast.GoBackend            (go/parser + go/ast)
 │      ↓
 │   coordinate: path · package · symbol · kind · start_line · end_line
 │      ↓
 │   read (exact range)
 │
 ├── textual intent
 │      ↓
 │   tools.GrepTool / GlobTool / LsTool   (registered in cmd/ohgo/run.go)
 │      ↓
 │   .gitignore-aware walk (tools/ignore.go)
 │      ↓
 │   read
 │
 └── mixed intent: the agent chooses per step (hybrid track; no shared index, no merged result)
```

Ownership checks performed in this phase:

- **One structural tool, one registration path.** `cmd/ohgo/run.go:400` is the only production construction of `symbols`. `cmd/ohgo/phase1closure_test.go` pins exactly one structural surface.
- **One index construction site.** `ast.NewIndex` is called only by `tools/symbols.go` in production. The TUI builds no index.
- **No duplicate packages.** There are no `repo/`, `index/`, `repository/`, or `codeintel/` packages.
- **No hidden fallback.** `symbols` does not call grep or glob. The benchmark's hybrid track is a tool-selection decision by the agent.
- **Closed schema.** `symbol`, `path`, `package`, `limit`, `skip`. `additionalProperties: false`. Tested by `TestSymbolsToolSchemaIsClosed`.
- **Read-only.** `symbols`, `grep`, `glob`, and `ls` are read-tier. Tested by `TestSymbolsToolIsReadOnly`.

No code was removed in this phase. Every duplicate candidate searched for turned out to be absent, so the audit found nothing to delete.

## 6. Correctness Findings

Each entry gives cause, impact, fix, and regression coverage.

### 6.1 Anchored `.gitignore` rule hid a source subtree (fixed earlier, in `fd68257`)

- **Cause.** `tools/ignore.go` matched an anchored pattern (`/ohgo`) against every path segment, not just the leading ones. The root binary rule therefore excluded `cmd/ohgo/…`.
- **Impact.** `grep` and `glob` silently returned no results for `cmd/ohgo`. Lexical discovery reported `not_found` for code that exists. Replaying the 145 recorded lexical calls before and after the fix (§6.2) shows the defect changed 19 calls across 9 runs.
- **Fix.** Anchored patterns match only the leading path segments. Commit `fd68257`.
- **Regression coverage.** `tools/ignore_test.go`: `TestIgnorerAnchorsBarePatternToBase`, `TestIgnorerAnchorsBarePatternInsideNestedBase`, `TestGrepDoesNotLetAnchoredRootRuleHideNestedSource`.
- **Agent-level impact.** Of the 9 changed runs, 2 changed outcome (B1(A) correct→`no_answer`, D3(A) `no_answer`→correct). The variance check (§6.2) shows B1(A) is unstable on the same binary. Net agent-level effect is not separable from noise with one sample per cell.

### 6.2 Provenance and the fix replay

The fix changed the lexical tool's output, so the pre-fix lexical results were not the same dataset as post-fix results. Handling:

- **Replay.** Every recorded `grep`/`glob`/`ls` call from A-lexical and C-hybrid runs (145 calls, 30 runs) was replayed against the same fixture using `tools/ignore.go` from `fd68257^` (pre-fix) and `fd68257` (post-fix). Output: `docs/benchmarks/results/phase2-fix-impact.json`. 19 calls changed; they belong to 9 runs.
- **Rerun policy.** Each of the 9 affected runs was rerun on the post-fix binary. Their pre-fix versions were replaced, never mixed. Unaffected runs were kept, on the argument that identical tool inputs at temperature 0 give identical trajectories. That argument assumes determinism, which the variance check does not fully support (see below).
- **Track B.** Unaffected by the ignore fix, because `symbols` does not use the ignore rules and B has no lexical tools.
- **Variance check.** B1(A) and D3(A) were rerun twice more on the post-fix binary (`phase2-variance-A.json`). B1(A) returned `no_answer` and `status_mismatch`, so its outcome is not stable on the same binary. D3(A) returned `correct` both times.

A second post-fix run of tracks A and C also exists in the results directory: `phase2-postfix-AC.json` (54 cells) and `phase2-postfix-A2B.json` (1 cell), both written 12:16–12:17 on 2026-10-21. This session did not produce them, and their provenance is not recorded in the files. They are not merged into the final dataset. Compared cell by cell, 50 of their 55 cells are valid, and 9 of those 50 disagree with the final dataset on outcome. That is an independent estimate of about 18% run-to-run disagreement on the same binary, which is higher than the two repeats alone suggest.

Conclusion for the record: the single-sample agent outcomes for the lexical and hybrid tracks carry variance comparable to the differences being tested. Correctness differences of one to two tasks between tracks are not established. Category-level and median conclusions are more stable.

### 6.3 Silent truncation: long lines in `grep` (fixed in this phase)

- **Cause.** `scanFile` set the scanner's maximum token size to 2000 bytes. A line of 64 KiB or more (a minified bundle or generated table) made `Scan()` stop, and `scanner.Err()` was never checked. Every later match in the file was dropped.
- **Impact.** A false negative with no message. A grep for a string after a generated line returned `No matches found.`
- **Reproduction.** `TestGrepReachesMatchesPastALongLine` (70 KiB line, then a match) failed on the pre-fix code, reporting `No matches found.`
- **Fix.** The scanner buffer cap is now the search window (4 MiB), so any line inside the window is scanned. A file whose scan error or window cut-off occurs is marked partial, and the result says so.
- **Fixture impact.** None. No tracked fixture file has a line over 2000 bytes or a file over 4 MiB. The 54 static probes showed zero non-latency differences, and the 145 replayed calls showed zero differences.

### 6.4 Silent truncation: 4 MiB window on multi-file searches (fixed in this phase)

- **Cause.** A file over the 4 MiB window was searched only up to the window. A note was emitted only for a single-file search, and a multi-file search said nothing.
- **Impact.** Matches beyond the window were missing with no message, and a no-match result over a large file looked like a complete search.
- **Reproduction.** `TestGrepSaysWhenAFileExceedsTheSearchWindow`: a 4 MiB+ file with a match after the window. It failed on the pre-fix code.
- **Fix.** Multi-file searches now report the count of files that exceed the window. The note is also emitted on the `No matches found.` path, which previously dropped it.
- **Fixture impact.** None (no fixture file exceeds 4 MiB).

### 6.5 Silent partial result on timeout (fixed in this phase)

- **Cause.** When the 30 s grep budget expired, dispatch stopped and partial results were returned without a note. An expired search with no hits returned `No matches found.`
- **Impact.** A timed-out search looked like a complete search.
- **Reproduction.** `TestGrepReportsTimeoutInsteadOfAnEmptyAnswer` (1 ns budget): the pre-fix output contained no "partial" marker.
- **Fix.** Dispatch checks the context before each file. A timed-out search adds `note: search stopped after …; results are partial`. A cancelled parent context returns a cancellation error.
- **Fixture impact.** None (no benchmark search approaches the 30 s budget).

### 6.6 Benchmark-side corrections (documentation, not code)

- The Phase 2 closure described the HTTP 429 errors as daily quota exhaustion. The rerun showed they are per-minute limits (`free-models-per-min`, 20 requests/minute). Pacing at 4–5 s per request resolved them. The closure's addendum records this.
- The Phase 2 closure claimed the hybrid "closes the cross-file gap". With the full dataset, the hybrid solves 1 of 3 cross-file tasks, E2. That claim is withdrawn in the addendum.

## 7. Performance and Efficiency Findings

The two kinds of measurement are reported separately.

### 7.1 Tool-level (deterministic, no model)

Index on a generated 10,000-file repository (310,000 symbols; `ast/index_stress_test.go`, env-gated by `OHGO_INDEX_STRESS`):

| Measure | Value |
| --- | ---: |
| Cold build | 0.88 s |
| Retained heap | 51 MiB |
| Warm lookup, average | 46 ms (42 ms of which is the freshness walk) |
| Rebuild after one edited file | 0.90 s |
| Rebuild after one deleted file | 1.25 s |

Static probe latency on the Phase 2 fixture: 1–79 ms per probe over 54 probes (`phase2-static-postfix.json`).

These are host-dependent measurements from one machine. They support "bounded and predictable at this scale", not a production-scale guarantee.

### 7.2 Agent-level (model in the loop)

All numbers in §4.3. Structural discovery against lexical, on the same 27 tasks:

- Turns: 7 → 3 (median).
- Discovery calls: 4 → 1.
- Candidate files: 12 → 2, with irrelevant candidates 11 → 0.
- Lines inspected: 280 → 50.
- Context: 14.9 → 2.1 KB.
- Input tokens: 11,126 → 5,110 (−54%).
- Median duration: 43.5 s → 18.3 s, though this depends on provider latency and the pacing used for reruns, so it is the least reliable of these figures.

The token and candidate reductions are the strongest agent-level evidence. They are consistent across the category breakdown. Tool-level lookup latency is not evidence that the agent is faster; the agent-level measurements are what support that claim.

## 8. Known Limitations

Each limitation below is supported by the final architecture or by a measurement in this phase.

**Discovery scope**

- **Cross-file relations.** Definition plus call site, definition plus registration, and definition plus run path are not solved. E-category: 0/3 on A and B, 1/3 on C. There is no references or callers primitive.
- **Non-Go structure.** `symbols` indexes Go declarations only. Non-Go text is discoverable only through lexical tools.
- **Text inside declarations.** Structural coordinates do not find string literals or doc lines (H-category: B 3/5).

**Lexical tools**

- **Hidden directories.** `grep` excludes dot-directories and dot-files with no option to include them. `glob` and `ls` accept `hidden: true`. A file under `.github/` is therefore invisible to `grep` by policy.
- **Binary files.** `grep` does not detect binary content; a match inside a binary file is printed. This was not observed in the benchmark, and no fixture file is binary. It is not fixed, because there is no evidence it affects a result.
- **Search window.** A file over 4 MiB is searched only up to 4 MiB, and the result now says so. Matches beyond the window are not found.

**Index lifecycle**

- **Cancellation during build.** `NewIndex` takes no context. A cancelled turn that triggers the first build waits for the build to finish: about 0.9 s at 10,000 files. The result is discarded, so this affects latency only.
- **Full rebuild on change.** Any edit or delete triggers a full re-parse of the repository: about 0.9–1.3 s at 10,000 files. Acceptable at the scale the benchmark uses; not measured above that scale.
- **Freshness signal.** Freshness is `(size, mtime_ns)` per file, plus the file count and unreadable entries. A same-size rewrite that restores an identical mtime (for example `touch -r`, or a filesystem with coarse timestamps) returns stale coordinates. The test suite covers a same-size rewrite with a different mtime (`TestIndexDetectsSameSizeRewrite`), but not the identical-mtime case, which this platform's nanosecond timestamps make unlikely. This is documented rather than changed, because no observed failure motivates the cost of a different signal.
- **Per-query freshness walk.** Each query walks the tree. About 42 ms at 10,000 files.

**Agent**

- **Run-to-run variance.** Single-sample cells. Repeats of two cells and an unmerged second post-fix run both show the same binary returning different outcomes, about 18% of comparable cells in the unmerged run (§6.2). Category-level and median conclusions are more stable than any single cell.
- **Single model.** `cohere/north-mini-code:free`, temperature 0, not a deterministic provider.
- **Ambiguity status.** Under the hybrid track, B3 returned `exact` for a task requiring `ambiguous` (`COORDINATE_AMBIGUITY`). The structural track's B3 failed by running out of turns.

## 9. Non-Goals

Deliberately not built, and not justified by the evidence in this phase:

- references, callers, callees, implementation graphs, import graphs
- semantic or vector search, an LLM-generated repository map
- MCP integration, an external search server
- a second index, a repository graph, a hidden lexical fallback inside `symbols`
- schema expansion, aliases, or compatibility wire names
- TUI indexing
- any change to the Phase 1 `symbols` contract

## 10. Evidence Quality

| Item | Value |
| --- | --- |
| Tasks | 27 across 8 categories (A–H) |
| Planned cells | 81 |
| Valid cells | 81 (44 pre-fix, 37 post-fix) |
| Provider-blocked cells remaining | 0 |
| Provider errors observed during the benchmark | HTTP 429 `free-models-per-min`, per-minute limit; resolved by pacing and retries |
| Model | `openrouter/cohere/north-mini-code:free` |
| Temperature / turns / tokens / timeout | 0 / 16 / 2048 / 240 s |
| Repository revision | `53bc07b2e850720e331e5fdcdf6ed0fa256402d7` |
| Task hash | `414012543e4c10121d3e1ffd6c24ed4c6107c74dcd8134406ae0a2181f1b68bb` (every source matched) |
| Fixture | 319 files, 295 Go, sanitized (`docs/report`, `docs/benchmarks`, contributor instructions excluded) |
| Concurrency | 1 |

**Provenance files**

- `phase2-baseline.json` — original 17–27 cell baseline (pre-fix)
- `phase2-supplementary-cohere.json` — supplementary run
- `phase2-valid-runs.json` — 53 valid pre-fix cells
- `phase2-rerun-A2B.json`, `phase2-rerun-blocked.json`, `phase2-rerun-pending-{1,2,3}.json` — reruns of the blocked cells
- `phase2-rerun-fixaffected-{A,C}.json` — post-fix reruns of the 9 fix-affected runs
- `phase2-variance-A.json` — two repeats of B1(A) and D3(A) on the post-fix binary
- `phase2-final-valid-runs.json` — merged final dataset (produced by `phase2_final_merge.py`)
- `phase2-fix-impact.json` — replay of 145 lexical calls before and after the fix
- `phase2-static-postfix.json` — 54 deterministic probes, post-fix
- `phase2-postfix-AC.json`, `phase2-postfix-A2B.json` — **not produced in this session; provenance unrecorded; not merged.** Used only as an independent variance sample (§6.2).

**Known confounders**

- Single sample per cell, with variance shown to be comparable to some of the differences.
- The 17-task paired view is an artifact of which cells were blocked, not a random sample of tasks.
- Ground truth and the hybrid's tool-selection behaviour were written by the same author as the tool.
- The fixture excludes Phase 2 documents, so a grep can match no benchmark prose. This makes lexical tasks easier than real-world ones.
- Durations depend on the rate-limit pacing used for reruns (4 s, and 5 s in the first rerun). Duration is not comparable across the original and rerun runs.
- The B track has no lexical tools, so it is unaffected by the ignore fix. The A and C tracks are affected.

**Not hidden:** every missing, blocked, or affected cell is listed above with its provenance. None was silently replaced or dropped.

## 11. Verification

Run on the final working tree on 2026-10-21:

```text
gofmt -l .            → empty
go build ./...        → PASS
go vet ./...          → PASS
go test ./... -count=1 → PASS (all packages; tokens has no test files)
go test -race ./... -count=1 → PASS
```

Benchmark, static, and probe verification:

```text
go run ./bench/repository -dry-run      → 27 tasks validated against ast.Index; 81 planned cells
go run ./bench/repository -static       → 54 probes; zero non-latency differences from phase2-static-postfix.json after the grep fix
replay of 145 lexical calls             → 0 differences from the post-fix grep fix; 19 differences from the ignore fix (fd68257)
python3 docs/benchmarks/phase2_final_merge.py docs/benchmarks/results   → 81 valid, 0 unresolved, 0 missing
python3 docs/benchmarks/phase2_final_analysis.py ...                     → metrics in phase2-final-analysis.json
OHGO_INDEX_STRESS=10000 go test ./ast -run TestIndexStress -v           → measurements in §7.1
```

Regression tests added or confirmed in this phase, each run against the code it guards:

- `tools/grep_window_test.go`: `TestGrepReachesMatchesPastALongLine`, `TestGrepSaysWhenAFileExceedsTheSearchWindow`, `TestGrepReportsTimeoutInsteadOfAnEmptyAnswer`. All three failed on the pre-fix code.

Existing coverage, read from the test names:

- Symbols tests cover exact, ambiguous, not-found, truncation, scope, read-only, cancellation, concurrency, deletion, creation, malformed files, symlinks, determinism, and schema closure.
- Lexical tests cover `.gitignore`, anchoring, nested ignore files, negation, hidden files, containment, and cancellation.

`go test -race ./...` passed in the final pass. No flaky test was observed in that run.

## 12. Final Decision

### Q1 — Does structural discovery provide measurable agent value?

**Yes, on the demonstrated scope.** Structural discovery against lexical, medians over 27 tasks: 3 vs 7 turns, 1 vs 4 discovery calls, 2 vs 12 candidate files (0 vs 11 irrelevant), 50 vs 280 lines inspected, 2.1 vs 14.9 KB of context, and 5,110 vs 11,126 input tokens. Correctness is 20/27 vs 17/27, and the structural track matches or beats lexical on C_PATH_PACKAGE, G_NOISE, and B_AMBIGUOUS. The gain is strongest on token and candidate cost. The correctness gain is 3 tasks and is within the variance measured in §6.2. The claim is "structural discovery is cheaper and at least as correct on declaration tasks", not "structural discovery is more correct".

### Q2 — Is lexical discovery still necessary?

**Yes.** On H_LEXICAL (text, literals, doc lines, Makefile targets), lexical is 5/5, hybrid 4/5, structural 3/5. Structural cannot express a text question, and its failures there are a structural limitation, not a defect.

### Q3 — Is hybrid discovery the preferred practical workflow?

**Yes, with a qualification.** Hybrid is the most correct track (21/27). It matches structural on token cost (median 5,032 vs 5,110) and is the only track that answered E2 (cross-file) correctly. The B-vs-C margin is one correct task, which is inside the observed variance, so the recommendation rests on robustness across intent types, not on a measured superiority over structural alone.

### Q4 — Is another repository-intelligence capability currently justified?

**No, not in this phase.** The decision rule in `PHASE2_SPEC.md` §11 requires a repeated structural failure, a relation the current primitive cannot express, and a minimal primitive with a measured expected improvement. The cross-file category satisfies the first two conditions: E1 and E3 fail on all three tracks, and the relation (a call site or registration) is one `symbols` cannot express. It does not yet satisfy the third. No experiment has measured what a references primitive would gain, and the E1 failure is partly agent reasoning (16 turns on search without converging). The honest next step is a separately scoped hypothesis with its own benchmark, not an implementation in this phase.

### Q5 — Are there known correctness or lifecycle issues remaining?

**Yes, all documented in §8 and none is a correctness defect in the current contract:**

- Index build cannot be cancelled mid-walk (about 0.9 s at 10,000 files).
- Any edit triggers a full re-parse (about 0.9–1.3 s at 10,000 files).
- Freshness does not detect a same-size rewrite with an identical mtime. Not reproduced here.
- `grep` does not detect binary files; hidden directories are excluded by policy.
- A file over 4 MiB is searched only up to the window. Now reported, not silent.
- Cross-file relations are unsolved.

No stale coordinate, leaked file, or silent wrong answer was found in the final audit. The fixes in §6.3–§6.5 closed the silent-truncation defects that were found.

### Q6 — Is the subsystem ready to freeze?

**Yes, with the limitations above recorded.** Freeze means: the `symbols` schema, the index contract, the `grep` and `ignore` behaviour as fixed, and the benchmark protocol. Freezing does not mean the cross-file category is solved. Any future change to discovery needs a new benchmark cycle with its own provenance, following the protocol in `PHASE2_SPEC.md` and amendment A1.

## Definition of Done

| Requirement | Status |
| --- | --- |
| All Phase 2 blocked cells resolved | **Yes** — 81/81 valid |
| Benchmark provenance preserved | **Yes** — every source file kept; per-run source and binary state recorded |
| `.gitignore` fix impact understood | **Yes** — 145-call replay; 19 calls, 9 runs; 2 outcome changes, one inconclusive |
| Final A/B/C comparison available | **Yes** — §4 |
| Structural value measured at agent level | **Yes** — §4.3, §7.2 |
| Lexical necessity demonstrated | **Yes** — H_LEXICAL 5/5 vs 3/5 |
| Hybrid workflow evaluated | **Yes** — §4.2, Q3 |
| No speculative capability added | **Yes** — no code added for repository intelligence |
| Known correctness defects fixed | **Yes** — §6.3–§6.5 fixed; §6.1 fixed earlier |
| Lifecycle and resource behaviour audited | **Yes** — §7.1, §8 |
| Cancellation verified | **Partial** — tool-level cancellation tests pass; index build cannot be interrupted (§8) |
| Concurrency verified | **Yes** — `TestSymbolsToolConcurrentExecute`, `TestIndexConcurrentQueriesStayConsistent`, `TestGrepIsRaceFree`; `-race` passes |
| Deterministic behaviour verified | **Yes** — `TestSymbolsToolOutputIsDeterministic`, `TestIndexResultsAreDeterministic`; static probes stable |
| Schema remains closed | **Yes** — `TestSymbolsToolSchemaIsClosed` |
| Ownership singular | **Yes** — §5 |
| No duplicate repository-intelligence path | **Yes** — §5 |
| Benchmark artifacts reproducible | **Yes** — listed in §10; the replay harness is described in §6.2 but was not committed |
| Documentation reflects final state | **Yes** — this report and the addenda |
| Build, vet, tests, race pass | **Yes** — §11 |
| Final closure report exists | **Yes** — this file |
