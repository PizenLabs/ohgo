# PHASE 2 — REPOSITORY DISCOVERY CLOSURE

- **Date:** 2026-10-20
- **Tree:** Phase 1 commit `53bc07b` + benchmark-only working-tree additions
- **Phase 1 contract:** `docs/report/PHASE1_CLOSURE.md` (frozen)
- **Baseline:** `docs/report/PHASE2_BASELINE.md`
- **Specification:** `docs/benchmarks/PHASE2_SPEC.md`
- **Harness:** `bench/repository`

## 1. Verdict

**HOLD — benchmark exposed unresolved measurement limitations.**

The benchmark was built and run. On 53 valid agent-level cells — 17 tasks
with all three tracks plus the two valid `A2` cells — structural and hybrid
discovery reduced turns, discovery calls, candidate files, context, and tokens
relative to lexical discovery, with better correctness on the measured set.
Lexical discovery remained necessary for text tasks, and the hybrid was the best
practical shape.

The closure is not a PASS because 28 of 81 planned agent-level cells could not
be executed after the primary provider exhausted its free quota. The missing
cells are concentrated in the negative, noise, and lexical categories. The
available evidence does not justify a production capability expansion, but it
is not complete enough to claim that the full Phase 2 question has been
answered.

The benchmark did surface one concrete lexical defect: an anchored
`.gitignore` rule (`/ohgo`) matched any path segment and silently hid the whole
`cmd/ohgo` subtree from `grep`/`glob`. That defect was fixed after the baseline,
with a regression test. The `symbols` path is unchanged.

## 2. Architecture

The Phase 1 causal path is unchanged:

```text
agent
  ↓
tools.SymbolsTool              (wire name "symbols", TierRead)
  ↓
ast.Index                       (one cached index per tool, disposable)
  ↓
ast.GoBackend                   (go/parser + go/ast)
  ↓
repository-relative coordinate
  path · package · symbol · kind · start_line · end_line
  ↓
read                            (source evidence, exact range)
```

Verified properties that remain intact:

- Exactly one structural tool and one authoritative index ownership path.
- The closed schema is unchanged: `symbol`, `path`, `package`, `limit`, `skip`.
- No alias package, no second repository-intelligence path, no hidden fallback
  search, no semantic search, no graph database.
- The index retains coordinates only; source remains authoritative.
- Every query revalidates freshness; parse failures are explicit.
- The model reads source through the existing `read` tool.
- `symbols` is registered in `cmd/ohgo/run.go` alongside the lexical tools; the
  hybrid is a tool-selection decision, not a new subsystem.

The benchmark harness is outside this path. It uses the production
`agent.Loop`, production tools, and production stream transport, with only a
recording wrapper, a fixed benchmark prompt, and a benchmark-only retry/pacing
layer.

## 3. Benchmark

### Tracks

| Track | Tools | Mechanism |
| --- | --- | --- |
| A — lexical | `read`, `glob`, `grep`, `ls` | text → candidates → inspect |
| B — structural | `read`, `symbols`, `ls` | structure → coordinate → inspect |
| C — hybrid | all five | choose structural or lexical per intent |

The target tree was a Git archive of the Phase 1 commit, with reports, prior
benchmark target lists, agent instructions, and README removed. All 27 tasks
were validated against `ast.Index` before any run. The model was
`cohere/north-mini-code:free`, temperature 0, 16 turns, 2048 output tokens, and
a 240-second timeout.

### Coverage

| Item | Count |
| --- | ---: |
| Planned cells (27 tasks × 3 tracks) | 81 |
| Valid baseline cells | 43 |
| Additional valid supplementary cells | 10 |
| **Valid merged cells** | **53** |
| Provider-blocked cells | 28 |
| Tasks with all three tracks | 17 / 27 |

The 28 unavailable cells are `A2(B)`, `E3(A,B,C)`, `F1(A,B,C)`, `F2(A,B,C)`,
`G1(A,B,C)`, `G2(A,B,C)`, `G3(A,B,C)`, `H2(A,B,C)`, `H3(A,B,C)`, and
`H5(A,B,C)`. They failed on provider HTTP 429/402 errors and are excluded from
capability conclusions. A supplementary run with the same model and settings
recovered 10 cells (`E2(C)`, `F3`, `H1`, `H4`); it did not replace any valid
baseline cell.

## 4. Evidence

### Paired 17-task comparison

This is the fairest available comparison: every median is over the same 17 task
IDs.

| Track | Correct | Complete | Turns | Tool calls | Discovery calls | Candidate files | Irrelevant candidates | Lines inspected | Context KB | Input tokens | Output tokens |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| A — lexical | 10/17 | 10/17 | 6 | 5 | 4 | 12 | 12 | 280 | 15.3 | 15,271 | 630 |
| B — structural | 13/17 | 13/17 | 4 | 3 | 2 | 11 | 11 | 210 | 9.4 | 7,704 | 524 |
| C — hybrid | 13/17 | 14/17 | 4 | 3 | 2 | 3 | 1 | 50 | 8.5 | 9,091 | 493 |

The measured deltas:

- B and C each solve 3 more tasks than A.
- B and C use two fewer turns and two fewer discovery calls at the median.
- C reduces median candidate files from 12 to 3, irrelevant candidates from 12
  to 1, lines inspected from 280 to 50, and context from 15.3 KB to 8.5 KB.
- B has the lowest median input tokens: 7,704 versus 15,271 for A and 9,091 for
  C.

Caveat: A and C were measured before the anchored-ignore fix, so their
`D2`/`E1` failures may be partly lexical-tool defects. Track B's relative
correctness advantage on those two tasks may therefore be overstated. The
efficiency direction is measured across all 17 tasks and is unaffected by that
specific defect.

### Representative tasks

| Task | Observation |
| --- | --- |
| A1 `NewSymbolsTool` | All correct. A: 4 turns / 3 calls / 6.1 KB; B: 3 / 2 / 2.1 KB; C: 3 / 2 / 2.1 KB. |
| B1 all `Classify` | A: 10 turns / 9 calls / 58.5 KB; B and C: 2 turns / 1 call / 278 B. All three preserve ambiguity. |
| B3 all 14 `tools.Execute` methods | A and B run out of turns; C finds all 14 but answers `exact` instead of `ambiguous`. The coordinates exist; the failure is status/enumeration overhead. |
| C1 `Settings` in `config` | A: 16 turns / 15.3 KB context (40,347 input tokens), no answer; B and C: 1 call / 140 B, correct. |
| E2 `Index.Lookup` definition + caller | A wrong target; B no answer; C correct with 7 turns and 23.0 KB context. The hybrid, not a new primitive, closes the cross-file gap. |
| F3 exact `Grep` | All correct; B proves absence in 1 call / 36 B. A spends 5 calls / 17.5 KB / 556 lines. |
| H4 documentation line | All correct. A: 4 turns / 25.6 KB; B: 5 turns / 25.7 KB; C: 2 turns / 141 B. Lexical search is the right tool. |

### Deterministic tool-level probes

`phase2-static.json` records 54 probes. Representative results:

- Exact symbol lookup: one file, 140–149 bytes.
- Ambiguous lookup: explicit `ambiguous`, all matches.
- Package-wide enumeration: explicit `truncated` with the true total (459
  declarations in package `tools`).
- Exact-name absence: `not_found` with 36 bytes.
- Non-Go text: the structural tool cannot represent it; lexical probes find
  the documentation line and Makefile target.
- Anchored-ignore defect: `grep buildTools` returned `not_found` before the fix
  and 5 files / 1,033 bytes after it, including `cmd/ohgo/run.go`. The
  before/after files are `phase2-static-before-fix.json` and
  `phase2-static.json`.

## 5. Failure Classification

Valid-cell failures (15 non-successes out of 53) were classified as:

| Classification | Evidence |
| --- | --- |
| `AGENT_REASONING_FAILURE` | Repeated identical reads/greps after finding the target; guessed conceptual queries; ran out of turns. |
| `TOOLING_OVERHEAD` | B3 paginated 459 declarations and read method bodies until the turn budget. |
| `COORDINATE_AMBIGUITY` | B3/C answered all 14 targets but used `exact` instead of `ambiguous`. |
| `INSUFFICIENT_CONTEXT` | E1 found the definition but not the registration path. |
| `STRUCTURAL_LIMITATION` | B lacks references for E1/E2 and lacks lexical search for text tasks; this is expected from the track definition, not a missing coordinate. |
| `DISCOVERY_MISSING` (lexical) | The anchored `/ohgo` ignore rule hid `cmd/ohgo` from `grep`/`glob` on D2/E1; fixed after the baseline with a regression test. |
| `HARNESS_FAILURE` | 28 provider-blocked cells (HTTP 429/402), excluded. |

No failure was classified `DISCOVERY_MISSING` for `symbols`. The one actual
discovery miss was lexical and has been fixed. The structural primitive
produced the coordinates in the cases where the agent reached the right query.

## 6. Changes

Benchmark and documentation artifacts were added, plus one evidence-driven
lexical defect fix:

- `bench/repository/` — harness, task suite, evaluator tests, static probes,
  rescoring, and result writers.
- `docs/benchmarks/PHASE2_SPEC.md` — frozen protocol.
- `docs/benchmarks/phase2_summary.py` — result renderer.
- `docs/benchmarks/phase2_merged.py` — conservative valid-cell merge.
- `docs/benchmarks/phase2_inspect.py` — failure evidence viewer.
- `docs/benchmarks/results/` — raw attempts, supplementary run, valid merged
  cells, deterministic probes before/after the ignore fix, and the secondary
  local-model run.
- `tools/ignore.go` — anchored-pattern matching fix, justified by the
  deterministic probe and the D2/E1 agent-level failures.
- `tools/ignore_test.go` — regression tests for anchored bare patterns at the
  root and inside a nested ignore file.
- `docs/report/PHASE2_BASELINE.md` — measurements and analysis.
- `docs/report/PHASE2_CLOSURE.md` — this report.

No file in `agent/`, `ai/`, `ast/`, `cmd/`, `prompt/`, or the rest of the
repository outside `tools/ignore.go` and its test was changed. The only
production change is the lexical ignore-matcher defect fix; the `symbols`
schema and implementation are untouched.

## 7. Non-Changes

The following were considered and intentionally not implemented:

- **References / callers / callees.** E2 is solved by the hybrid with one
  lexical search after a structural lookup. The benchmark does not establish a
  repeated failure that requires a call graph. Implementing one would add a
  second structural subsystem and a new traversal contract without sufficient
  evidence.
- **Implementations / interface satisfaction.** No measured task required it;
  the ambiguous interface cases are handled by exact type lookup plus reads.
- **Import or package graphs.** No task exposed a failure that coordinates plus
  `read` could not answer. Package enumeration is already bounded and paginated.
- **Semantic search / embeddings.** D2 failed across all tracks because the
  model formulated conceptual queries badly. That is not evidence for a
  semantic index.
- **MCP or external repository search.** No MCP server was connected. No claim
  of superiority is made.
- **A second index or fallback search.** Track C already demonstrates the
  hybrid at the tool-selection layer. A hidden fallback inside `symbols` would
  violate the Phase 1 contract.
- **Schema broadening or aliases.** The closed `symbol`/`path`/`package`/
  `limit`/`skip` vocabulary remains.
- **TUI indexing.** No new index producer was added.
- **A new repository-intelligence capability.** The only production change is
  the anchored-ignore defect fix; it does not add a discovery primitive.

The ignore fix is deliberately narrow: it changes only how an anchored pattern
is matched against the leading path segments. It does not change the `symbols`
tool, the index, the prompt, or the agent loop.

## 8. Verification

Run on the final working tree:

```text
gofmt -l .          → empty
go build ./...      → PASS
go vet ./...        → PASS
go test ./...       → PASS (26 packages)
go test -race ./... → PASS (26 packages)
```

Benchmark verification:

```text
go run ./bench/repository -dry-run
  → 319 fixture files, 295 Go files
  → all 27 tasks validated against ast.Index
  → 27 tasks × 3 tracks = 81 planned cells

go run ./bench/repository -static
  → 54 deterministic probes written
```

Focused regression tests:

```text
go test ./tools -run 'TestIgnorer|TestGrepHonoursGitignore'
  → PASS
```

The anchored-pattern tests pin both halves of the contract: `/ohgo` excludes the
root binary and its subtree, and it does not match `cmd/ohgo` or its files.

Focused harness tests cover final-answer parsing (explicit, bare, fenced, and
lenient), negative false positives, ambiguity-status enforcement, missing
cross-file targets, qualified-symbol acceptance, similar-name rejection,
candidate-file extraction, line counting, path normalization, line tolerance,
task-ID uniqueness, category coverage, and prompt tool-name leakage.

## 9. Definition of Done

| Requirement | Status |
| --- | --- |
| Phase 1 remains architecturally intact | **Yes** — `symbols`/`ast` are unchanged; only lexical ignore matching was fixed. |
| Reproducible benchmark exists | **Yes** — `bench/repository`, spec, result JSON. |
| Conventional lexical discovery baseline measured | **Partial** — 18 of 27 cells; the rest are provider-blocked. |
| Ohgo structural discovery baseline measured | **Partial** — 17 of 27 cells; the rest are provider-blocked. |
| Relevant task categories represented | **Partial** — A–E well covered; F/G/H under-covered at agent level, covered by static probes. |
| Ambiguous and negative cases represented | **Yes** — B1/B3/G3 and F1–F3 exist; agent-level F3 measured, F1/F2 blocked. |
| Adversarial lexical tasks represented | **Yes** — H1–H5 exist; H1/H4 measured, H2/H3/H5 blocked. |
| Correctness measured | **Yes** — success, target completeness, coordinate overlap, status. |
| Discovery cost measured | **Yes** — calls, candidates, files, lines, context. |
| Agent/tool/model cost measured | **Yes** — turns, input/output tokens, duration. |
| Failure modes classified | **Yes** — 15 valid failures, 28 provider failures, one lexical discovery defect fixed. |
| No capability added without evidence | **Yes** — no production capability added; the only production change is an evidence-backed lexical defect fix. |
| Any justified capability has a minimal design | **N/A** — none justified. |
| Build/vet/test/race pass | **Yes**. |
| Closure report exists | **Yes**. |
| Claims do not exceed evidence | **Yes** — HOLD, not PASS. |

## 10. Next Action

Re-run the 28 unavailable cells when the OpenRouter free quota resets, using the
same frozen task hash (`414012543e4c10121d3e1ffd6c24ed4c6107c74dcd8134406ae0a2181f1b68bb`),
fixture revision, model, temperature, max turns, and max tokens. The rerun must
include the anchored-ignore fix; it can only improve Track A and Track C on
`D2`/`E1`. Merge the results with the existing 53 valid cells and re-evaluate.

If the direction holds, Phase 2 can close as:

> **PASS — benchmark proves sufficient value; no capability expansion required.**

If those cells instead expose a repeated structural failure that exact-name
coordinates plus `read` cannot express, the failure must be documented first,
then the smallest capability that closes it can be considered. The current
evidence does not justify that step.

The Phase 1 primitive remains frozen, small, and inspectable:

```text
intent → the right mechanism → the right coordinate → minimum evidence
```

not a general-purpose repository-intelligence platform.
