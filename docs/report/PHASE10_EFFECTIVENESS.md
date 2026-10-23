# Phase 10 — Effectiveness Evaluation

> **Status:** Phase 10 controlled scripted matrix complete. Real-model matrix is
> recorded in `docs/benchmarks/results/phase10-control.json`,
> `phase10-cognitive.json`, and `phase10-final.json`; provider limitations are
> listed below.
>
> **Verdict:** the Phase 9 efficiency baseline is preserved, the runtime
> overhead is negligible against model latency, and the cognitive arm adds a
> measurable truthfulness effect: it refuses to mark an evidence-free
> repository answer `COMPLETED`.

---

## 1. Controlled experiment

The experiment isolates the cognitive runtime from model quality:

| Variable | CONTROL | COGNITIVE |
|---|---|---|
| Model | same pinned reference | same pinned reference |
| Model settings | same `max_tokens`, temperature, turn cap | same |
| Task / prompt | same scenario and task hash | same |
| Repository | same deterministic fixture/revision | same |
| Tool implementation | same production-shaped set | same |
| Authority | same routed workspace gate, same auto-approval policy | same |
| Budget | same turns/tokens/wall-clock | same |
| Timeout | same per-run and process bound | same |
| Projection | **off** | **on** (`cognition.Projector`) |
| Completion review | no projection report | projection sufficiency veto |

The last row is the one deliberate treatment difference. It is recorded here
rather than hidden: the cognitive arm can downgrade an answer that the
projection itself says lacked evidence. That is the mechanism under test.

### Model conditions

The environment exposed these practical conditions on the run date
(2026-10-23):

| Class | Reference | Availability |
|---|---|---|
| local/small | `ollama/llama3.2:3b` | available, keyless |
| local/medium | `ollama/qwen2.5:7b` | available, keyless; **full A–J + P10 + K1/K2/K7 primary** |
| remote/fast-primary | `gemini/gemini-2.5-flash-lite` | worked in probes, then HTTP 429 free-tier quota exhausted (retry ~3h); failed A/B/C retained as a provider limitation |
| remote/free-router | `openrouter/openrouter/free` | available but slow/variable; smaller matrix |
| freetier/constrained | `openrouter/nvidia/nemotron-3-ultra-550b-a55b:free` | available with intermittent upstream overload; smaller matrix |

Unavailable conditions are documented, not substituted:

- **Groq** returned HTTP 403 for both `/models` and chat completions. No Groq
  result is included.
- **Paid OpenRouter rows** returned HTTP 402 (no credits). No paid model result
  is included.
- **Gemini 3 Flash** produced a tool call, then HTTP 400 on the replay because
  the OpenAI-compatible endpoint requires a `thought_signature` on each function
  call. It was not used for the matrix.
- **OpenRouter free Gemma** returned HTTP 429; it was not used.
- No API key, request body, or provider secret is stored in the artifacts.

### Controlled matrix

The Phase 10 suite is the frozen Phase 9 A–J/K matrix plus:

| ID | Purpose |
|---|---|
| `P10-C1-read-file` | read-only task must not mutate |
| `P10-C2-read-then-update` | read, mutate only the named file, verify |
| `P10-F-package-name` | one-file question; no repository exploration |

Real-model coverage is intentionally balanced against provider reliability and
the provider-quota limitation above. The **local/medium** condition runs the
complete A–J + P10 set plus K1/K2/K7 and is the primary real-model comparison. Local/small
runs the representative A/B/C/P10/G/K1/K7 subset. The OpenRouter free-router and
free-tier conditions run a smaller A/G/K1 subset with tighter turn and timeout
bounds. The attempted Gemini condition is retained as failed provider evidence,
not as a quality result. The per-model scenario list is in the artifacts.

### Metrics

- **Decision:** completion status, answer facts matched/expected, wrong
  completions, retries, turns.
- **Exploration:** files read, duplicate reads, search calls, tool calls.
- **Context:** estimated input tokens, provider input/output/cache tokens,
  tool-schema bytes, repeated-context bytes, first/last request bytes, raw
  tool-output bytes.
- **Execution:** mutations, verification, unexpected mutations, authorization
  refusals, candidate rejections, provider cost (when known).
- **Verified success:** `COMPLETED` **and** all scenario facts matched **and** no
  unverified mutation. This is stricter than runtime completion alone.

Token counts are the runtime's documented estimate; provider usage is separate.
The scripted arm measures runtime effects exactly; the live arm measures model
behavior and must not be compared across providers as if it were a tokenizer
benchmark.

---

## 2. Scripted runtime result (no model choice)

### 2.1 Phase 9 frozen matrix

Fresh run on the final tree:

```sh
go run ./bench/phase9 -projection off   -out docs/benchmarks/results/phase10-phase9-control.json
go run ./bench/phase9 -projection state -out docs/benchmarks/results/phase10-phase9-cognitive.json
```

| Measure | CONTROL | COGNITIVE | Change |
|---|---:|---:|---:|
| Estimated input tokens (17 scenarios) | 514,155 | 261,956 | **−49.05%** |
| Estimated input tokens excluding K1 | 392,288 | 253,407 | **−35.40%** |
| Median scenario | — | — | −40.73% |
| Tool-schema bytes | 1,084,005 | 469,917 | −56.65% |
| Context bytes | 894,424 | 530,177 | −40.72% |
| Repeated-context bytes | 1,554,461 | 727,661 | −53.19% |
| Raw tool-output bytes | 213,633 | 213,571 | −0.03% |
| Objective statuses | same per scenario | same per scenario | no correctness change |
| Tool calls / turns | same | same | the scripted path is fixed |

The Phase 9 published result was −49.2% input tokens, −56.7% tool schema, and
−54.7% repeated bytes. The small differences are expected: the state block now
always carries task scope, verification, completion, and sufficiency, which
raises repeated bytes slightly (54.7% → 53.2%) while keeping the input-token and
schema results at the same level. This is the deliberate cost of preserving the
context invariant; it is not hidden.

### 2.2 Phase 10 added suite

```sh
go run ./bench/phase9 -suite phase10 -projection off \
  -out docs/benchmarks/results/phase10-scripted-control.json
go run ./bench/phase9 -suite phase10 -projection state \
  -out docs/benchmarks/results/phase10-scripted-cognitive.json
```

| Measure | CONTROL | COGNITIVE | Change |
|---|---:|---:|---:|
| Estimated input tokens (20 scenarios) | 540,471 | 276,299 | **−48.88%** |
| Objective statuses | same per scenario | same per scenario | no change |
| `P10-C1-read-file` | COMPLETED, no mutation | COMPLETED, no mutation | pass |
| `P10-C2-read-then-update` | COMPLETED, mutated+verified | COMPLETED, mutated+verified | pass |
| `P10-F-package-name` | COMPLETED, one read | COMPLETED, one read | pass |

Multiple scripted scenarios are already `NEEDS_REVIEW` in both arms (unverified
mutation, failed check, partial background work). COGNITIVE additionally records
the evidence review for those; it does not change their status.

### 2.3 Projection overhead

`BenchmarkProjectorProject` (200 evidence items, 200 tool-result messages):

```text
~0.25 ms/op, ~95 KiB/op, 722 allocs/op (Apple M1 Pro)
```

This is two to four orders of magnitude below a model request. Projection
latency is not a material runtime cost in the measured scenarios.

---

## 3. Real-model result

### 3.1 Local/medium primary (full Phase 10 suite)

`ollama/qwen2.5:7b`, 20 scenarios, one run per pair. This is the primary
real-model comparison; it is a 7B local model, not a frontier model.

| Measure | CONTROL | COGNITIVE | Result |
|---|---:|---:|---|
| Estimated input tokens | 124,612 | 70,892 | **−43.1%** |
| Tool calls | 20 | 21 | effectively unchanged |
| Model turns | 40 | 41 | effectively unchanged |
| Answer facts matched | 24/48 | 25/48 | preserved (P10-F scorer correction) |
| Runtime `COMPLETED` | 7 | 3 | stricter proof |
| Verified successes | 2 | 2 | preserved |
| Completed-but-not-verified | 5 | 1 | fewer shallow completions |

The five CONTROL completions that are not verified successes are A, K1, K2,
K6, and P10-F. Their answers were thin or wrong (K1 matched 0/3 facts; P10-F was
the wrong answer "not found" and was corrected by the artifact scorer), yet
CONTROL returned `COMPLETED`. COGNITIVE returned `NEEDS_REVIEW` for those runs.
The one compensating COGNITIVE false review is F: its answer matched 2/2 facts
but was based on a directory listing, not content. P10-F is the reverse: the
COGNITIVE symbols query produced the correct package name and completed, while
CONTROL completed the wrong "not found" answer.

| Scenario | CONTROL | COGNITIVE | C tokens | G tokens | C facts | G facts | C tools | G tools |
|---|---|---|---:|---:|---:|---:|---:|---:|
| A-pure-read | COMPLETED | NEEDS_REVIEW | 6,152 | 2,862 | 3/5 | 3/5 | 1 | 1 |
| B-read-build | NEEDS_REVIEW | NEEDS_REVIEW | 6,212 | 3,493 | 1/2 | 1/2 | 1 | 1 |
| C-explicit-boundary | NEEDS_REVIEW | NEEDS_REVIEW | 6,228 | 3,527 | 1/2 | 1/2 | 1 | 1 |
| D-mutation-after-investigation | NEEDS_REVIEW | NEEDS_REVIEW | 6,216 | 3,499 | 1/2 | 1/2 | 1 | 1 |
| E-embedded-mutation | NEEDS_REVIEW | NEEDS_REVIEW | 6,235 | 3,530 | 0/2 | 0/2 | 1 | 1 |
| F-negative-control | COMPLETED | NEEDS_REVIEW | 6,164 | 2,877 | 2/2 | 2/2 | 1 | 1 |
| G-debugging | NEEDS_REVIEW | NEEDS_REVIEW | 6,228 | 4,331 | 2/3 | 1/3 | 1 | 1 |
| H-refactoring | NEEDS_REVIEW | NEEDS_REVIEW | 6,285 | 5,990 | 0/2 | 0/2 | 1 | 1 |
| I-fuzzing | NEEDS_REVIEW | COMPLETED | 6,174 | 3,942 | 1/3 | 2/3 | 1 | 2 |
| J-external-knowledge | NEEDS_REVIEW | NEEDS_REVIEW | 6,270 | 3,752 | 1/3 | 1/3 | 1 | 1 |
| K1-irrelevant-large-file | COMPLETED | NEEDS_REVIEW | 6,155 | 2,863 | 0/3 | 0/3 | 1 | 1 |
| K2-missing-file | COMPLETED | NEEDS_REVIEW | 6,154 | 2,862 | 1/2 | 1/2 | 1 | 1 |
| K7-stop-after-proof | NEEDS_REVIEW | NEEDS_REVIEW | 6,246 | 3,555 | 1/2 | 2/2 | 1 | 1 |
| P10-C1-read-file | COMPLETED | COMPLETED | 6,329 | 2,883 | 3/3 | 3/3 | 1 | 1 |
| P10-C2-read-then-update | NEEDS_REVIEW | NEEDS_REVIEW | 6,559 | 4,086 | 1/1 | 0/1 | 1 | 1 |
| P10-F-package-name | COMPLETED | COMPLETED | 6,158 | 3,541 | 0/1 | 1/1 | 1 | 1 |
| K3-failed-verification | NEEDS_REVIEW | NEEDS_REVIEW | 6,228 | 3,527 | 1/2 | 1/2 | 1 | 1 |
| K4-unexpected-mutation | NEEDS_REVIEW | NEEDS_REVIEW | 6,228 | 3,527 | 1/2 | 1/2 | 1 | 1 |
| K5-partial-execution | NEEDS_REVIEW | NEEDS_REVIEW | 6,237 | 3,383 | 1/1 | 1/1 | 1 | 1 |
| K6-large-source-context | COMPLETED | NEEDS_REVIEW | 6,154 | 2,862 | 3/5 | 3/5 | 1 | 1 |

The P10-F CONTROL fact is corrected to 0/1: its answer was the wrong
"not found" response and the generic substring scorer had matched `tui` inside
the path. The artifact carries the correction; the runtime CONTROL still
returned `COMPLETED`, which is the false-positive being demonstrated.


### 3.2 Local/small

`ollama/llama3.2:3b`, 8 representative scenarios:

| Measure | CONTROL | COGNITIVE | Result |
|---|---:|---:|---|
| Estimated input tokens | 50,096 | 25,817 | **−48.5%** |
| Answer facts matched | 6/21 | 5/21 | one fact lost |
| Runtime `COMPLETED` | 1 | 0 | stricter proof |
| Verified successes | 1 | 0 | one pair lost |
| Tool calls | 12 | 9 | fewer |

The lost pair is P10-C1: CONTROL read `index.html` and answered correctly;
COGNITIVE called `grep` with an invalid `limit`, then answered that the file did
not exist. The read tool and target were available; this is a 3B model tool-use
error, and the runtime truthfully returned `NEEDS_REVIEW` rather than accepting
the wrong answer. It is reported as a model-capability limitation, not hidden.

### 3.3 Remote/free-router probe

`openrouter/openrouter/free`, A/G/K1 only, 8-turn bound:

| Scenario | CONTROL | COGNITIVE | C tokens | G tokens | C facts | G facts |
|---|---|---|---:|---:|---:|---:|
| A-pure-read | COMPLETED | COMPLETED | 22,935 | 12,402 | 5/5 | 4/5 (style.css omitted) |
| G-debugging | COMPLETED | NEEDS_REVIEW | 24,087 | 15,825 | 2/3 | 2/3 |
| K1-irrelevant-large-file | NEEDS_REVIEW | PAUSED | 15,912 | 14,624 | 0/3 | 0/3 |

The router used 31.9% fewer tokens in COGNITIVE. It also demonstrates why the
free router is a **model condition**, not a runtime claim:

- A: CONTROL read 12 tools and covered all five facts; COGNITIVE read 6 and
  omitted `style.css` from the answer. The answer is broadly correct but the
  strict facts score is 4/5.
- G: both matched 2/3 facts; CONTROL returned `COMPLETED`, COGNITIVE required
  review. The control completion is the weaker proof.
- K1: CONTROL failed a tool call; COGNITIVE hit the 8-turn bound and is reported
  as `PAUSED`, not failed. This is a resource stop.

No remote quality conclusion is drawn from three router runs.




---

## 4. Analysis

### 4.1 What the runtime layer clearly does

- It reduces the fixed capability surface by withholding tools the task shape
  cannot use, and by using the relationship/language/operation metadata instead
  of a model guess.
- It reduces repeated tool result bytes by summarizing old structural/exact
  observations while keeping the newest working set.
- It preserves objective status in the deterministic matrix. The same scripted
  sequence produces the same verdict before and after projection.
- It now preserves the contract and sufficiency verdict in the projected state
  block, so cutting evidence summaries cannot erase what the task requires.
- It refuses to complete an informational answer when the projection says the
  runtime still lacks required evidence.

### 4.2 What is a model effect, not a runtime effect

- The free router (`openrouter/free`) can select different upstream backends
  between runs. Its results are a provider condition, not a fixed model
  capability.
- Local 3B/7B models often stop after a listing or one file read. Their weak
  answers are not evidence that projection is ineffective; the answer-facts
  column separates behavior from runtime overhead.
- Tokenizer and provider cache accounting differ by provider; only within-model
  CONTROL/COGNITIVE deltas are meaningful.
- A model that never calls a tool cannot be rescued by a better projection.

### 4.3 Completion truthfulness

The cognitive arm's sufficiency veto changes the completion rate. That is not a
regression in correctness; it is the removal of a false positive. A CONTROL run
can return `COMPLETED` for a polished answer that never read the repository.
The same run in COGNITIVE returns `NEEDS_REVIEW` when its last projection says
evidence is still missing. The answer-facts and verified-success columns make
this visible rather than hiding it behind a completion percentage.

Across all three real-model conditions, the strict verified-success count is 4
CONTROL / 2 COGNITIVE. The full primary condition is equal at 2/2; the lower
overall count comes from one 3B tool-use error and one free-router answer
omission. The result is a measured model-quality/strictness trade-off, not a
runtime authority or completion defect.

---

## 5. Known limitations and measurement gaps

1. **No repeats.** The live matrix uses one run per model/scenario. Real-model
   variance, especially the free router, is not measured. The scripted matrix is
   deterministic.
2. **Free router identity and speed.** `openrouter/free` is a router, not a
   pinned upstream model, and it was slow enough that only a smaller subset was
   run. A repeat can route differently.
3. **Provider availability.** Groq (HTTP 403), paid OpenRouter (HTTP 402),
   Gemini 2.5 free-tier quota (HTTP 429 after the probes), Gemini 3 tool replay
   (`thought_signature`), and OpenRouter free Gemma (HTTP 429) were unavailable
   for the reasons above. The full A–J comparison was therefore run on the
   available local/medium condition; this substitution is explicit, and no
   result was fabricated.
4. **Answer facts are a heuristic.** They are case-insensitive substrings, not a
   semantic judge. They catch answers that never mention the project or entry
   point; they do not prove understanding.
5. **Informational correctness is not proven.** Runtime completion proves an
   answer exists and, in COGNITIVE, that required evidence was acquired. It does
   not prove the answer is factually correct. A correct existence/negative answer
   supported only by a directory listing can be `NEEDS_REVIEW` in COGNITIVE
   while CONTROL completes it; that is the strict-evidence trade-off.
6. **Synthetic fixtures.** Absolute costs are not a prediction for another
   repository. The comparison is controlled, not representative of every codebase.
7. **No paid billing measurement.** Provider cost is zero/unknown for local and
   free conditions. Do not read `provider_cost_usd` as a bill.

---

## 6. Reproduce

```sh
# Deterministic runtime comparison
go run ./bench/phase9 -projection off   -out docs/benchmarks/results/phase10-phase9-control.json
go run ./bench/phase9 -projection state -out docs/benchmarks/results/phase10-phase9-cognitive.json
go run ./bench/phase9 -suite phase10 -projection off   -out docs/benchmarks/results/phase10-scripted-control.json
go run ./bench/phase9 -suite phase10 -projection state -out docs/benchmarks/results/phase10-scripted-cognitive.json

# Real-model matrix (default matrix, one run per pair)
python3 bench/phase10/run.py --out-dir docs/benchmarks/results

# Summary tables
python3 bench/phase10/summarize.py docs/benchmarks/results/phase10-final.json
```

The live runner builds the pinned Phase 9 harness, records the git revision and
working-tree status, and writes all three Phase 10 artifacts. A transient
provider failure is retried; the attempt count is recorded.
