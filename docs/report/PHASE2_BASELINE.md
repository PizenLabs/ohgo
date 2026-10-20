# PHASE 2 — REPOSITORY DISCOVERY BASELINE

- **Status:** measured baseline, **HOLD** (partial agent-level coverage)
- **Date:** 2026-10-20
- **Target tree:** `53bc07b2e850720e331e5fdcdf6ed0fa256402d7`
- **Phase 1 contract:** `docs/report/PHASE1_CLOSURE.md` (frozen)
- **Specification:** `docs/benchmarks/PHASE2_SPEC.md`
- **Harness:** `bench/repository`
- **Raw attempts:** `docs/benchmarks/results/phase2-baseline.json` (81 cells)
- **Supplementary run:** `docs/benchmarks/results/phase2-supplementary-cohere.json`
- **Valid merged cells:** `docs/benchmarks/results/phase2-valid-runs.json` (53 cells)
- **Deterministic probes:** `docs/benchmarks/results/phase2-static.json` (54 probes, after the ignore fix)
- **Pre-fix probes:** `docs/benchmarks/results/phase2-static-before-fix.json`
- **Secondary local model:** `docs/benchmarks/results/phase2-secondary-local.json` (not merged)

## 1. Status

The benchmark was designed and run, but the controlled agent-level baseline is
**incomplete**. The primary model provider exhausted its free daily quota and
then rejected requests with HTTP 429/402. Of 81 planned cells, 53 are valid;
28 cells are unavailable, concentrated in categories F, G, and H.

The valid cells show a consistent direction: structural discovery and the
hybrid reduce turns, discovery calls, candidate files, lines inspected, context
bytes, and tokens relative to lexical discovery on the measured task set.
However, the missing cells include the negative, noise, and several lexical
tasks that are most important for a balanced conclusion. Reporting a full PASS
would exceed the evidence.

**Verdict: HOLD — measurement coverage is incomplete, not a demonstrated
capability failure.** No production capability was added or changed. The
benchmark did surface one concrete lexical-defect finding (an anchored
`.gitignore` rule hid `cmd/ohgo` from `grep`/`glob`); that defect was fixed
after the baseline, with a regression test. The `symbols` path is unchanged.

## 2. Question

Does Ohgo's symbol-first discovery give an agent a measurable advantage over
conventional lexical discovery on realistic repository tasks, without losing
correctness where the intent is textual rather than structural?

Phase 1 established that `symbols` produces deterministic coordinates. This
baseline measures what the agent does with them.

## 3. Method

The frozen protocol is in `docs/benchmarks/PHASE2_SPEC.md`. The essential
controls:

- The target tree is a Git archive of the Phase 1 commit. The working tree and
  the benchmark harness are not part of the target repository.
- Benchmark reports, earlier target lists, agent instructions, and README were
  removed from the fixture so answers could not leak into search results.
  `docs/architecture` was retained because one task asks for a line in it.
- All 27 tasks were validated before the run: every positive target was checked
  against `ast.Index` for path and line range, and every negative name was
  checked to have zero exact declarations.
- The same system prompt and user prompt were used for every track. Only the
  tool set changed.
- Track A: `read`, `glob`, `grep`, `ls`.
- Track B: `read`, `symbols`, `ls`; `grep` and `glob` were deliberately absent
  so the measured effect is the structural primitive.
- Track C: all five tools, the practical hybrid.
- Runs were read-only, capped at 16 turns, 2048 completion tokens, and 240
  seconds, at temperature 0.
- The primary model is `openrouter/cohere/north-mini-code:free`. A
  benchmark-only retry and pacing layer absorbed provider throttling; those
  retries are infrastructure handling, not results.

The harness uses the production `agent.Loop`, the production tool
implementations, and the production stream transport. It adds only a recording
wrapper around each tool and a fixed benchmark system prompt.

### Coverage

| Item | Count |
| --- | ---: |
| Planned agent-level cells | 81 |
| Valid baseline cells | 43 |
| Additional valid cells from the supplementary run | 10 |
| **Valid merged cells** | **53** |
| Provider-blocked cells | 28 |
| Tasks with all three tracks measured | 17 / 27 |

The supplementary run used the same model, fixture, prompt, temperature, max
turns, and max tokens as the baseline. It was used only for cells that the
baseline could not execute; it did not replace a valid baseline cell.

The 28 unavailable cells are:

```text
A2 (B)                         E3 (A, B, C)
F1 (A, B, C)                   F2 (A, B, C)
G1 (A, B, C)                   G2 (A, B, C)       G3 (A, B, C)
H2 (A, B, C)                   H3 (A, B, C)       H5 (A, B, C)
```

They are provider failures (HTTP 429/402), not track failures. They are
excluded from every correctness and efficiency aggregate below.

## 4. Environment

| Field | Value |
| --- | --- |
| Repository | `github.com/PizenLabs/ohgo` |
| Revision | `53bc07b2e850720e331e5fdcdf6ed0fa256402d7` |
| Fixture | 319 files, 295 Go files |
| OS / runtime | macOS (Darwin, arm64), Go 1.27 |
| Provider | OpenRouter, OpenAI Chat Completions protocol |
| Model | `cohere/north-mini-code:free` |
| Temperature | 0 |
| Max turns | 16 |
| Max completion | 2048 tokens |
| Run timeout | 240 s |
| Concurrency | 1 |
| Compaction | disabled in the harness |

The model is not the model used to design Phase 1. It is a free coding model;
absolute success is therefore a property of this model as well as of the tools.
The comparison is within-model.

## 5. Task Inventory

The suite has 27 tasks in eight categories. Full prompts and ground truth are
in `bench/repository/tasks.go`.

| Category | Count | Purpose |
| --- | ---: | --- |
| A — exact | 3 | Unique declarations with stable coordinates. |
| B — ambiguous | 3 | Duplicate names, context-dependent choice, package-wide enumeration. |
| C — path/package | 3 | Same-name competitors resolved by scope. |
| D — modification | 4 | The actual edit surface for a change. |
| E — cross-file | 3 | Definition plus registration, caller, or use. |
| F — negative | 3 | Exact-name absence despite plausible lexical matches. |
| G — noise | 3 | Prefix collisions and multiple declarations of one name. |
| H — lexical | 5 | String literals, struct tags, documentation, and Makefile text. |

## 6. Agent-Level Results

These are the 53 valid cells. Values are medians per track. The tracks do not
have exactly the same valid task set, so the **paired 17-task table in §7** is
the fair comparison.

| Track | Valid | Correct | Complete targets | Coordinate exact | Status preserved | Turns | Discovery calls | Candidate files | Files opened | Lines inspected | Context KB | Input tokens | Output tokens | Duration ms |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| A — lexical | 18 | 11 (61.1%) | 11 | 11 | 12 | 6 | 4 | 13 | 1 | 292.5 | 15.1 | 13,158 | 609 | 21,320 |
| B — structural | 17 | 13 (76.5%) | 13 | 13 | 13 | 4 | 2 | 11 | 1 | 210 | 9.4 | 7,704 | 524 | 16,546 |
| C — hybrid | 18 | 14 (77.8%) | 15 | 14 | 14 | 4 | 2 | 2.5 | 1 | 75 | 9.2 | 8,464 | 490 | 15,708 |

`Complete targets` counts cells where every requested target was present and no
extra target was reported. `Coordinate exact` additionally requires the
reported line range to overlap the indexed range. `Status preserved` requires
the requested `exact`/`ambiguous`/`not_found` status. Across all 53 valid cells,
at most one extra target was reported in a single run (Track A); the coordinate
false-positive rate is therefore low even where the final status was wrong.

### Correctness by category (valid cells)

| Category | A — lexical | B — structural | C — hybrid |
| --- | ---: | ---: | ---: |
| A_EXACT | 3/3 | 2/2 | 3/3 |
| B_AMBIGUOUS | 2/3 | 2/3 | 1/3 |
| C_PATH_PACKAGE | 1/3 | 3/3 | 3/3 |
| D_MODIFICATION | 2/4 | 3/4 | 3/4 |
| E_CROSS_FILE | 0/2 | 0/2 | 1/2 |
| F_NEGATIVE | 1/1 | 1/1 | 1/1 |
| H_LEXICAL | 2/2 | 2/2 | 2/2 |

The category cells have very small counts, and F/H are represented by only one
and two tasks respectively. They are evidence, not a population estimate.

## 7. Paired Comparison — 17 Tasks With All Three Tracks

This is the fairest agent-level comparison: every median is over the same 17
task IDs.

| Track | Correct | Complete | Turns | Tool calls | Discovery calls | Candidate files | Irrelevant candidates | Files opened | Lines inspected | Context KB | Input tokens | Output tokens | Duration ms |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| A — lexical | 10/17 | 10/17 | 6 | 5 | 4 | 12 | 12 | 1 | 280 | 15.3 | 15,271 | 630 | 21,421 |
| B — structural | 13/17 | 13/17 | 4 | 3 | 2 | 11 | 11 | 1 | 210 | 9.4 | 7,704 | 524 | 16,546 |
| C — hybrid | 13/17 | 14/17 | 4 | 3 | 2 | 3 | 1 | 1 | 50 | 8.5 | 9,091 | 493 | 15,590 |

The 17 tasks are A1, A3, B1, B2, B3, C1, C2, C3, D1, D2, D3, D4, E1, E2, F3,
H1, and H4. A2 is excluded because Track B's cell is unavailable; E3, F1, F2,
G1–G3, H2, H3, and H5 are excluded entirely because none of their agent-level
cells executed.

The important deltas on this paired set:

- B and C each solve **3 more tasks** than A (13 vs 10).
- B and C use **2 fewer discovery calls** and **2 fewer turns** at the median.
- C reduces median candidate files from 12 (A) to 3, irrelevant candidates from
  12 to 1, lines inspected from 280 to 50, and context from 15.3 KB to 8.5 KB.
- B has the lowest median input tokens (7,704 vs 15,271 for A; 9,091 for C).
- C is the only track to solve the cross-file caller task E2.
- Lexical search remains necessary: H1/H4 require text matching, and C uses
  grep to solve H4 in two turns and 141 bytes. Track A is not the best executor
  of that mechanism once structural coordinates are also available.

## 8. Representative Evidence

### Exact discovery — A1 `NewSymbolsTool`

| Track | Outcome | Turns | Calls | Candidate files | Lines read | Context | Input tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| A | correct | 4 | 3 | 2 | 120 | 6.1 KB | 5,825 |
| B | correct | 3 | 2 | 1 | 50 | 2.1 KB | 2,506 |
| C | correct | 3 | 2 | 1 | 50 | 2.1 KB | 3,538 |

### Ambiguity — B1 `Classify`

| Track | Outcome | Turns | Calls | Candidate files | Lines read | Context | Input tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| A | correct (ambiguous) | 10 | 9 | 10 | 1,368 | 58.5 KB | 85,385 |
| B | correct (ambiguous) | 2 | 1 | 2 | 0 | 278 B | 1,349 |
| C | correct (ambiguous) | 2 | 1 | 2 | 0 | 278 B | 2,037 |

The structural result names both declarations and keeps the ambiguity explicit.
The lexical run also found both, but spent ten turns and 60 KB of context doing
it.

### Package-wide enumeration — B3 all `Execute` methods in `tools`

| Track | Outcome | Turns | Calls | Candidate files | Context | Input tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| A | no answer | 16 | 16 | 62 | 19.3 KB | 65,187 |
| B | no answer | 16 | 16 | 31 | 68.6 KB | 204,276 |
| C | all 14 targets, status `exact` | 10 | 9 | 23 | 37.9 KB | 99,326 |

This is the clearest limitation of the result, and it is not a missing
coordinate. C found all 14 methods and their ranges. It failed only the
ambiguity-status contract, because it answered `exact` instead of
`ambiguous`. B paginated all 459 declarations in package `tools` (11 discovery
calls) and then ran out of turns while reading the method bodies. A found the
14 methods with one grep but also ran out of turns. The failure is
`TOOLING_OVERHEAD` / `AGENT_REASONING_FAILURE`, not `DISCOVERY_MISSING`.

### Path/package scope — C1 `Settings` in package `config`

| Track | Outcome | Turns | Calls | Candidate files | Context | Input tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| A | no answer | 16 | 16 | 14 | 15.3 KB | 40,347 |
| B | correct | 2 | 1 | 1 | 140 B | 1,284 |
| C | correct | 2 | 1 | 1 | 140 B | 1,972 |

A found the correct grep and then repeated reads/greps until the turn budget.
This is a model degeneracy, not a lexical-tool failure; it is classified as
`AGENT_REASONING_FAILURE`. B/C got the exact coordinate in one call.

### Cross-file — E2 `Index.Lookup` definition and caller

| Track | Outcome | Turns | Calls | Candidate files | Context | Input tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| A | wrong target | 5 | 4 | 3 | 2.9 KB | 5,687 |
| B | no answer | 16 | 16 | 1 | 1.2 KB | 13,481 |
| C | correct | 7 | 6 | 3 | 23.0 KB | 27,388 |

A reported the call-site line but named the callee (`Lookup`) rather than the
enclosing method (`SymbolsTool.Execute`). B has no reference/call-graph
primitive and no grep, so it could not find the caller. C combined a
structural lookup with a lexical search and solved it. This is evidence for
the hybrid, not yet for adding a call-graph primitive: the same task was
solved with one grep once the mechanism was chosen.

### Negative discovery — F3 exact `Grep`

| Track | Outcome | Turns | Calls | Candidate files | Lines read | Context | Input tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| A | correct (`not_found`) | 6 | 5 | 10 | 556 | 17.5 KB | 15,271 |
| B | correct (`not_found`) | 2 | 1 | 0 | 0 | 36 B | 1,297 |
| C | correct (`not_found`) | 5 | 4 | 6 | 556 | 24.2 KB | 16,302 |

The exact-name structural query proves absence in one call. The lexical run
must rule out many plausible `Grep*` matches and reads 556 lines to do so.
This is the strongest measured advantage for exact-name structural discovery.

### Lexical — H4 architecture documentation line

| Track | Outcome | Turns | Calls | Candidate files | Lines read | Context | Input tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| A | correct | 4 | 3 | 9 | 416 | 25.6 KB | 9,468 |
| B | correct | 5 | 4 | 14 | 416 | 25.7 KB | 9,761 |
| C | correct | 2 | 1 | 1 | 0 | 141 B | 1,966 |

B can brute-force a non-Go file with `ls` and `read`, but it pays the same
whole-file cost as A. C uses one lexical grep and wins. This is the required
counter-example to “symbols replaces grep.”

## 9. Deterministic Tool-Level Probes

`phase2-static.json` executes one canonical query per task per track. It is
not an agent-level measurement. Several probes are evaluator-informed (they
start from the expected symbol), so they measure the precision of the tool,
not whether an agent would formulate the query.

Representative probes:

| Task | A — lexical | B — structural |
| --- | --- | --- |
| A1 `NewSymbolsTool` | 2 files / 1,693 B | 1 file / 144 B |
| A2 `Index.Lookup` | 3 files / 1,348 B | 1 file / 140 B |
| B1 all `Classify` | 7 files / 1,601 B | `ambiguous`, 2 files / 278 B |
| B3 all `tools.Execute` | 40 files / 14,991 B | `truncated`, 459 declarations / 16 files / 26,029 B |
| C1 `Settings` | 7 files / 3,793 B | 1 file / 140 B |
| G2 `Identity` in `prompt` | 12 files / 3,540 B | `ambiguous`, 3 files / 389 B |
| G3 all `Load` | 14 files / 2,860 B | `ambiguous`, 2 files / 264 B |
| F3 exact `Grep` | 1 file / 110 B | `not_found`, 0 results / 36 B |
| H4 documentation line | 1 file / 141 B | no textual result (70 B) |
| H5 Makefile target | 1 file / 40 B | no textual result (45 B) |

Tool-level conclusions:

1. Exact structural lookup is consistently one file and 140–149 bytes.
2. Ambiguous structural lookup returns all matches and says so; lexical search
   returns candidate files without an ambiguity state.
3. Package-wide enumeration is bounded: `symbols` reports `truncated` with the
   true total (459) rather than pretending the page is complete.
4. Exact-name absence is a first-class `not_found` result for `symbols`.
   Lexical search can return plausible `Grep*` matches for a query that names
   `Grep` exactly.
5. Non-Go text is not representable by the structural index. The structural
   probe returns declarations in the requested file or nothing, never the
   requested string/line.

### Benchmark finding: anchored ignore rule hid `cmd/ohgo`

The deterministic probe exposed a lexical-discovery defect unrelated to
`symbols`. The repository `.gitignore` contains `/ohgo` for the root binary.
The ignore matcher treated an anchored bare pattern as a match against any path
segment, so `cmd/ohgo` was silently pruned from every `grep`/`glob` scan.

The canonical probe for the tool-registration task, `grep buildTools`, shows
the before/after difference:

| Fixture state | Result | Files | Bytes |
| --- | --- | ---: | ---: |
| Before fix (`phase2-static-before-fix.json`) | `not_found` | 0 | 17 |
| After fix (`phase2-static.json`) | `ok` | 5 | 1,033 |

The after-fix result includes `cmd/ohgo/run.go:371`, `382`, and `751`. The fix
matches anchored patterns against the leading path segments, so `/ohgo` matches
the root binary but not `cmd/ohgo`. Regression tests:
`TestIgnorerAnchorsBarePatternToBase` and
`TestIgnorerAnchorsBarePatternInsideNestedBase`.

This finding matters for interpretation: Track A and Track C baseline runs were
executed **before** the fix. Their failures on `D2` and `E1` therefore reflect a
pre-fix lexical-tool effect, not only a mechanism limit. As a result, the paired
correctness comparison may overstate Track B's relative advantage on those two
tasks. The efficiency direction (fewer calls, candidate files, context, and
tokens for B/C) is less affected because it is measured across all 17 paired
tasks. The agent-level effect of the fix was not re-measured because the
provider quota was exhausted.

## 10. Failure Classification — Valid Agent-Level Cells

The 53 valid cells contain 15 non-successes. The 28 provider-blocked cells are
excluded from this table.

| Task | Track | Outcome | Classification | Evidence |
| --- | --- | --- | --- | --- |
| B2 | C | no answer | `AGENT_REASONING_FAILURE` | Read the right `Config` region, then explored unrelated types for 16 turns. |
| B3 | A | no answer | `AGENT_REASONING_FAILURE`, `TOOLING_OVERHEAD` | Found all 14 methods with one grep; read files until the turn budget. |
| B3 | B | no answer | `TOOLING_OVERHEAD`, `AGENT_REASONING_FAILURE` | Paginated all 459 package declarations (11 calls), then read until the turn budget. |
| B3 | C | status mismatch | `COORDINATE_AMBIGUITY` | All 14 targets correct; answered `exact` instead of `ambiguous`. |
| C1 | A | no answer | `AGENT_REASONING_FAILURE` | Repeated the same reads/greps after finding the target; no final answer. |
| C3 | A | no answer | `AGENT_REASONING_FAILURE` | Repeated `grep "type Prediction"` ten times after finding it. |
| D2 | A | no answer | `AGENT_REASONING_FAILURE`, `DISCOVERY_MISSING` (lexical) | Guessed conceptual grep patterns; the anchored-ignore defect also hid `cmd/ohgo` before the fix. |
| D2 | B | no answer | `STRUCTURAL_LIMITATION`, `AGENT_REASONING_FAILURE` | Exact-name lookup cannot search by concept; read package bodies until budget. |
| D2 | C | no answer | `AGENT_REASONING_FAILURE`, `DISCOVERY_MISSING` (lexical) | Broad “tool set” patterns; the same pre-fix ignore defect hid the registration file. |
| D3 | A | no answer | `AGENT_REASONING_FAILURE` | Repeated `grep "match cap"` and `grep "limit"` without narrowing. |
| E1 | A | no answer | `AGENT_REASONING_FAILURE`, `DISCOVERY_MISSING` (lexical) | Found the definition, never found the registration; the pre-fix ignore defect hid `cmd/ohgo`. |
| E1 | B | no answer | `STRUCTURAL_LIMITATION`, `AGENT_REASONING_FAILURE` | No reference traversal and no grep; could not connect definition to registration. |
| E1 | C | no answer | `AGENT_REASONING_FAILURE`, `DISCOVERY_MISSING` (lexical) | Had both mechanisms, but the registration file was outside the pre-fix scan and the query never narrowed. |
| E2 | A | wrong target | `COORDINATE_AMBIGUITY` | Reported the call-site line, named the callee instead of the enclosing method. |
| E2 | B | no answer | `STRUCTURAL_LIMITATION`, `AGENT_REASONING_FAILURE` | No caller/reference relation; spent the budget on the definition. |

No valid failure is classified `DISCOVERY_MISSING` for `symbols`. The failures
are dominated by agent termination/query-formulation problems, plus one
ambiguity-status error and one answer-granularity error. The one actual
discovery miss was lexical, and it was fixed after the baseline: the anchored
`.gitignore` rule hid `cmd/ohgo` from `grep`/`glob`.

## 11. Provider Failures

The primary run produced 38 HTTP 429/402 failures after the free-tier quota was
exhausted. A supplementary run with the same model and settings recovered 10 of
those cells. The remaining 28 are reported as `unavailable`, not as failures of
a track. This is a **HARNESS / provider limitation**:

```text
HARNESS_FAILURE (provider quota)
```

No conclusion about lexical, structural, or hybrid capability is drawn from
those cells.

A secondary local 3B model (`llama3.2:3b`) was attempted on the missing cells.
It was too weak to be a meaningful agent-level replication: 0/10 correct on A,
3/10 on B, and 2/10 on C. It is retained as evidence of a failed fallback, not
merged into the primary results. The structural track did outperform the
lexical track on that secondary subset, but the sample is too small and the
model too weak to support a claim.

## 12. What the Evidence Supports

On the 17 tasks with all three tracks measured:

1. **Structural coordinates reduce agent work.** B uses 2 discovery calls and 4
   turns at the median versus 4 and 6 for A. It uses 7,704 input tokens versus
   15,271.
2. **Hybrid is the best practical shape.** C matches B's 13/17 correctness,
   reduces median candidate files to 3 and lines inspected to 50, and is the
   only track to solve the cross-file caller task E2.
3. **Exact negative discovery is a real structural advantage.** F3 is one
   call and 36 bytes for B; A spends 5 calls and 17.5 KB ruling out lexical
   noise.
4. **Ambiguity is explicit in the structural result.** B1 is 2 turns and 278
   bytes for B/C; A spends 10 turns and 58.5 KB.
5. **Scope constraints are handled well structurally.** C1/C2/C3 are one to
   three calls for B; A degenerates on two of the three.
6. **The division of labour is real.** H1 is roughly tied between A and C; H4
   is cheapest for C, which uses one grep. B can brute-force both but pays
   whole-file costs. H4/H5 are not representable by the structural index at
   all. Lexical search remains necessary; Track A is not.
7. **Lexical discovery had a real defect, and the benchmark found it.** An
   anchored `.gitignore` pattern hid `cmd/ohgo` from `grep`/`glob`. The fix and
   its regression tests are listed in the closure report. This is a defect
   repair, not a repository-intelligence capability.

## 13. What the Evidence Does Not Support

- **Do not replace grep.** The lexical cases and the cross-file registration
  case require it. Track C wins by combining mechanisms, not by deleting one.
- **Do not add references/callers yet.** E2 is solved by C with one lexical
  search after a structural lookup. The B failure is explained by the
  deliberate absence of grep plus the absence of a reference relation; the
  hybrid closes it without a new primitive. The measured cost is 7 turns and
  23.0 KB context (27,388 input tokens), not obviously better than a modest
  call-graph addition would be, but the benchmark does not establish that a
  call graph is necessary.
- **Do not add semantic search.** D2 fails in all three tracks because the
  model formulates conceptual queries badly. That is not evidence that a
  semantic index would help; it is evidence that the task is hard for this
  model.
- **Do not claim MCP superiority.** No MCP server was connected. The optional
  external track was not measured.
- **Do not claim a full Phase 2 PASS.** Twenty-eight cells are unavailable and
  the missing categories are disproportionately negative/noise/lexical.

## 14. Limitations

- **Incomplete coverage.** 17/27 tasks have all three tracks; 28 cells are
  provider-blocked. F, G, and part of H are represented by only one or two
  agent-level tasks and by deterministic probes.
- **One model, one repetition.** Temperature 0 reduces but does not remove
  model variance. Absolute success rates are model-specific.
- **One repository.** The target is a 295-file Go repository with tests and
  docs. Multi-language monorepos, generated code, and very large trees may
  behave differently.
- **The benchmark prompt is minimal.** It states the answer contract but not
  Ohgo's production identity prompt. Tool descriptions are the only discovery
  guidance. Results may differ under the production prompt.
- **Track B is deliberately narrower.** It has no grep or glob, so its failures
  on text/cross-file tasks are expected and are not evidence that `symbols`
  should absorb lexical search.
- **Static probes are not agent-level.** Some use evaluator-known symbols to
  start the query. They measure tool precision, not query formulation.
- **Time-to-target fields are derived from tool-call completion.** They exclude
  model thinking time and are not used for the main conclusions.
- **The anchored-ignore fix landed after the baseline.** Track A and Track C
  were measured before the fix, so their `D2`/`E1` failures may be partly a
  lexical-tool defect rather than a mechanism limit. The paired correctness
  comparison may overstate Track B's relative advantage on those two tasks.
  The static before/after probe and the regression test prove the defect and
  the fix; the agent-level effect is not re-measured.
- **The secondary local model is too weak.** It is not merged and does not
  establish a second model replication.

## 15. Decision

**HOLD.**

The measured cells support the Phase 2 hypothesis directionally: structural
and hybrid discovery reduce agent work and improve correctness on the measured
structural/path/ambiguous/negative tasks, while lexical discovery remains the
right tool for text. No capability expansion is justified by this baseline.
The benchmark is not complete enough to close Phase 2 as a PASS because the
negative/noise/lexical categories are under-measured.

The next action is to re-run the 28 unavailable cells when the provider quota
resets, using the same frozen task hash, fixture revision, model, and settings.
The anchored-ignore fix should be included in that rerun; it can only improve
the lexical and hybrid tracks on `D2`/`E1`. If those cells reproduce the same
direction, Phase 2 can close as PASS with no capability expansion. If they
expose a repeated structural failure that the current exact-name primitive
cannot express, that becomes the evidence for a targeted Phase 2 capability.

No production capability was added, removed, or changed before this baseline.
After the baseline, one lexical discovery defect was fixed with a regression
test; the `symbols` primitive is untouched.
