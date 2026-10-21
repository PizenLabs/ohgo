# Phase 2 — Repository Discovery Benchmark Specification

- **Status:** frozen before the baseline run
- **Benchmark revision:** `phase2-v1`
- **Target tree:** `53bc07b2e850720e331e5fdcdf6ed0fa256402d7` (Phase 1 frozen)
- **Harness:** `bench/repository`
- **Result file:** `docs/benchmarks/results/phase2-baseline.json`
- **Summary renderer:** `docs/benchmarks/phase2_summary.py`

## 1. Question

Does stable structural coordinates let an agent reach the correct code with
less noise, fewer searches, fewer candidate files, fewer inspection steps, or
less context than conventional lexical discovery, while preserving correctness?

This is an agent-level question. Tool-level hit counts alone do not answer it.

## 2. Controlled variables

| Variable | Value |
| --- | --- |
| Target repository | this repository, pinned to the Phase 1 commit |
| Fixture | `git archive` of the pinned commit, sanitized |
| System prompt | identical for every track |
| User prompt | identical for every track |
| Model | `openrouter/cohere/north-mini-code:free` |
| Temperature | `0` |
| Max completion | 2048 tokens |
| Max turns | 16 |
| Wall clock per run | 240 s |
| Workspace | read-only; only `read`, `ls`, `glob`, `grep`, `symbols` are registered |
| Repetitions | 1 baseline run per task × track; unavailable cells replicated separately with the same model and settings |

The model is deliberately not the model that implemented Phase 1. It is a
general tool-using model with no knowledge of the benchmark suite.

## 3. Tracks

| Track | Tools | Mechanism |
| --- | --- | --- |
| `A-lexical` | `read`, `glob`, `grep`, `ls` | text → candidate files → inspect → narrow |
| `B-structural` | `read`, `symbols`, `ls` | structure → coordinate → inspect |
| `C-hybrid` | `read`, `symbols`, `grep`, `glob`, `ls` | choose structural or lexical per intent |

Track B intentionally omits `grep` and `glob` so the measured effect is the
coordinate primitive rather than a larger tool set. Track A and Track C keep
the normal lexical tools. The hybrid track is the practical deployment shape,
not an attempt to replace grep.

No MCP or external repository-search server is connected. The optional
external/MCP track is **not measured**, so no claim about MCP is made.

## 4. Fixture sanitization

The fixture is the tracked tree at the pinned commit minus:

- `docs/report` — contains the Phase 1 closure report and benchmark answers
- `docs/benchmarks` — contains earlier benchmark target lists
- `AGENTS.md`, `MEMORY.md`, `PROGRESS.md`, `PORT.md`, `README.md` — contain
  contributor instructions or target names

`docs/architecture` is retained because one task deliberately asks for a line
in it. `.git`, `.ohgo`, build artifacts, and the local `ohgo` binary are not in
the archive.

## 5. Task suite

The suite contains 27 tasks across eight categories. Every target is validated
against the fixture with `ast.Index` before any run; negative names are
validated to have zero exact declarations. The task hash is recorded in the
result metadata.

| Category | Count | What it tests |
| --- | ---: | --- |
| `A_EXACT` | 3 | exact declaration lookup with stable coordinates |
| `B_AMBIGUOUS` | 3 | duplicate names, receiver-qualified methods, package-wide enumeration |
| `C_PATH_PACKAGE` | 3 | path/package scoping with same-name competitors |
| `D_MODIFICATION` | 4 | identifying the actual edit surface |
| `E_CROSS_FILE` | 3 | definition plus registration/caller/use |
| `F_NEGATIVE` | 3 | exact-name absence despite plausible lexical noise |
| `G_NOISE` | 3 | similarly named declarations and prefix collisions |
| `H_LEXICAL` | 5 | string literals, struct tags, documentation, and Makefile text |

Each task has a stable ID, prompt, expected status, and evaluator-only ground
truth in `bench/repository/tasks.go`.

### Task inventory

| ID | Category | Expected |
| --- | --- | --- |
| `A1-exact-new-symbols-tool` | A | `tools/symbols.go`: `NewSymbolsTool` |
| `A2-exact-index-lookup` | A | `ast/index.go`: `Index.Lookup` |
| `A3-exact-grep-cap` | A | `tools/grep.go`: `GrepSingleFileMatches` |
| `B1-ambiguous-classify` | B | two `Classify` declarations, status `ambiguous` |
| `B2-ambiguous-agent-config` | B | `agent/loop.go`: `Config` (not `shell.Config`) |
| `B3-ambiguous-execute-methods` | B | all 14 `Execute` methods in package `tools`, status `ambiguous` |
| `C1-path-settings-config` | C | `config/config.go`: `Settings` |
| `C2-path-project-load` | C | `project/project.go`: `Load` (not `config.Load`) |
| `C3-path-workspace-prediction` | C | `workspace/intent.go`: `Prediction` |
| `D1-mod-provider-env-table` | D | `creds/creds.go`: `providerEnvVars` |
| `D2-mod-tool-registration` | D | `cmd/ohgo/run.go`: `buildTools` |
| `D3-mod-grep-single-file-cap` | D | `tools/grep.go`: `GrepSingleFileMatches` |
| `D4-mod-prompt-identity` | D | `prompt/prompt.go`: `Identity` |
| `E1-cross-grep-def-and-registration` | E | `tools/grep.go` + `cmd/ohgo/run.go` |
| `E2-cross-lookup-def-and-caller` | E | `ast/index.go` + `tools/symbols.go` |
| `E3-cross-close-def-and-run-path` | E | `session/session.go` + `cmd/ohgo/run.go` |
| `F1-negative-find-references` | F | `not_found` |
| `F2-negative-call-graph` | F | `not_found` |
| `F3-negative-grep-symbol` | F | `not_found` (`GrepTool` and friends exist) |
| `G1-noise-classify-risk` | G | `security/scope.go`: `ClassifyRisk` |
| `G2-noise-prompt-identity` | G | `prompt/prompt.go`: `Identity` |
| `G3-noise-all-load` | G | two `Load` declarations, status `ambiguous` |
| `H1-lexical-error-text` | H | `agent/loop.go`: `ErrNoTools` line |
| `H2-lexical-todo-literal` | H | `task/view.go`: `renderTodoContext` line |
| `H3-lexical-json-field` | H | `ai/openai.go`: `openAIRequest` line |
| `H4-lexical-doc-line` | H | `docs/architecture/ARCHITECTURE.md:23` |
| `H5-lexical-makefile-target` | H | `Makefile:49` |

## 6. Answer contract

The model must end with one machine-readable line:

```text
FINAL: {"status":"exact|ambiguous|not_found","targets":[{"path":"...","symbol":"...","start_line":N,"end_line":N}]}
```

- `exact` means every requested target was found without ambiguity.
- `ambiguous` is required when the task asks for all declarations and more than
  one exists.
- `not_found` requires an empty target list.

The evaluator also recovers prose answers, but records `answer_format` as
`lenient`; a formatting failure is never silently reported as a discovery
success.

## 7. Measurements

Per run:

- **Correctness:** success, outcome, reported status, matched/missing/extra
  targets, line-range accuracy.
- **Discovery:** tool calls, discovery calls, distinct candidate files,
  reported candidate total, files opened, read lines, context bytes.
- **Agent cost:** model turns, input tokens, output tokens, cache tokens,
  wall-clock duration.
- **Time to target:** call index and elapsed tool time at which the first
  expected target became visible, and when all targets were visible.
- **Answer:** final text, reported targets, answer format.

Discovery calls are all non-`read` tool calls. Candidate files are distinct
file paths surfaced by `symbols`, `grep`, `glob`, or `ls`. A `read` is an
inspection, not discovery.

## 8. Outcome taxonomy

| Outcome | Meaning |
| --- | --- |
| `correct` | every expected target present, no extra target, required status |
| `correct_not_found` | negative task answered with empty `not_found` |
| `false_negative` | a requested target is missing |
| `false_positive` | an extra target was reported, or a negative task named a target |
| `wrong_target` | missing expected targets and reporting extras |
| `status_mismatch` | targets are correct but ambiguity/exactness was not preserved |
| `wrong_status` | status contradicts the task without an additional target |
| `harness_error` | the loop could not be constructed |
| no answer / provider error | retained as an explicit failure with its error text |

## 9. Failure classification

Every non-success is classified in the baseline report as one or more of:

```text
DISCOVERY_MISSING
COORDINATE_AMBIGUITY
INSUFFICIENT_CONTEXT
LEXICAL_NOISE
STRUCTURAL_LIMITATION
AGENT_REASONING_FAILURE
TOOLING_OVERHEAD
MODEL_FAILURE
HARNESS_FAILURE
```

A model that finds the coordinate but answers with the wrong caller is not a
`symbols` failure. A task that requires a string literal is not a structural
failure; it is evidence for the division of labour.

## 10. Reproducibility

```sh
go run ./bench/repository -dry-run
go run ./bench/repository \
  -model cohere/north-mini-code:free \
  -tracks A,B,C \
  -concurrency 1 \
  -max-turns 16 \
  -max-tokens 2048 \
  -timeout 240s \
  -out docs/benchmarks/results/phase2-baseline.json
python3 docs/benchmarks/phase2_summary.py docs/benchmarks/results/phase2-baseline.json
```

For a free provider tier, add `-min-interval 5s` to stay within the published
requests-per-minute limit. Daily quota exhaustion is an unavailable cell, never
a track failure; the result file records the provider error explicitly.

The result metadata records the repository revision, benchmark revision, task
hash, model, provider, endpoint, temperature, budgets, fixture size, and the
excluded paths. The target tree is reproduced from Git, not from the working
directory, so later implementation changes cannot silently move the ground
truth.

## 11. Decision rule

A capability expansion is justified only if the baseline shows:

1. a repeated failure on a structural task,
2. the current `symbols` primitive cannot express the required relation, and
3. a minimal additional primitive has a stated measurable improvement.

A lexical task that structural discovery cannot answer is **not** evidence for
expanding `symbols`. It is evidence that the agent should choose the cheaper
mechanism, which is exactly what the hybrid track tests.

## Amendment A1 — completion protocol (2026-10-21)

The sections above are unchanged. This amendment records how the blocked cells
were completed and does not alter the frozen protocol.

- **Reruns.** Blocked cells were rerun with the same task hash, revision,
  model, temperature, turn, token, and timeout settings. Pacing used
  `-min-interval 4s` or `5s` and `-retries 10`. Provider errors (HTTP 429) were
  never admitted to the dataset.
- **Fix-affected cells.** Cells whose recorded lexical calls change output under
  the anchored `.gitignore` fix (`fd68257`) were replaced only by valid post-fix
  reruns, as identified by replaying every recorded lexical call. See
  `docs/benchmarks/results/phase2-fix-impact.json`.
- **Variance.** Two fix-affected cells were repeated twice on the same binary to
  estimate run-to-run variance (`phase2-variance-A.json`).
- **Merge.** `docs/benchmarks/phase2_final_merge.py` builds
  `phase2-final-valid-runs.json`. It refuses any source whose task hash,
  revision, model, provider, or temperature differs.
- **Analysis.** `docs/benchmarks/phase2_final_analysis.py` computes the final
  metrics. Results are in `phase2-final-analysis.json` and discussed in
  `docs/report/FINAL_CLOSURE.md`.
