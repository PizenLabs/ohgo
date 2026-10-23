# Phase 9 — Benchmark

## 1. Controlled experiment

The benchmark isolates **runtime** cost from **model** behaviour.

- The same fixture, contract, authority, tool set, budget, and maximum turns are
  used in both modes.
- A scripted model emits an identical action sequence in baseline and
  projected runs. No model chooses differently between the two arms.
- Tools execute for real (`read`, `ls`, `write`, `edit`, `bash`, `web_fetch`).
- Every request is measured before transport. Tokens are the runtime's
  documented estimate, not provider billing.
- The projected arm installs the Phase 9 `cognition.Projector`; the baseline
  arm leaves `agent.Config.Project` nil and preserves the pre-Phase-9 path.

Modes:

```sh
go run ./bench/phase9 -projection off   -out docs/benchmarks/results/phase9-baseline.json
go run ./bench/phase9 -projection state -out docs/benchmarks/results/phase9-state.json
```

Live-provider mode is implemented but not exercised in this environment:

```sh
go run ./bench/phase9 -live-model anthropic/claude-sonnet-4-5 -projection state
```

It runs the same prompts with a real model and records provider input/output
and cache usage. Model behaviour is unmeasured in this report.

## 2. Matrix

A–J are the required task shapes; K are adversarial controls.

| ID | Shape | Expected outcome |
|---|---|---|
| A | Pure read | `READ`, no mutation |
| B | Read → build README | mutation, README changed |
| C | Explicit boundary | only README may change |
| D | Mutation after investigation | investigate, then build |
| E | Embedded mutation | build |
| F | Negative control | `READ`, no mutation |
| G | Debugging | evidence → diagnosis → bounded edit → tests |
| H | Refactoring | multi-file dependency-aware edit → tests |
| I | Fuzzing | read-only exploration → bounded execution → no mutation |
| J | External knowledge | external evidence with provenance, no authority |
| K1 | Irrelevant large file | no mutation, bounded retention |
| K2 | Missing file | truthful error, no false mutation |
| K3 | Failed verification | review, not completion |
| K4 | Unexpected mutation | review, scope violation reported |
| K5 | Partial execution | review, a started job is not finished execution |
| K6 | Large source context | read-only, retention stress |
| K7 | Post-proof exploration | proof complete, extra `ls` withheld |

## 3. Results

Full data: `docs/benchmarks/results/phase9-baseline.json` and
`docs/benchmarks/results/phase9-state.json`. Rows with `NEEDS_REVIEW` did so in
**both** arms; the projected arm did not change the objective status in any
scenario.

| Scenario | Baseline tokens | State tokens | Delta | Baseline schema bytes | State schema bytes | Baseline last request | State last request | Baseline replay bytes | State replay bytes |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A-pure-read | 24,604 | 13,911 | −43.5% | 69,615 | 22,939 | 15,693 | 10,263 | 77,760 | 37,571 |
| B-read-build | 38,004 | 25,972 | −31.7% | 99,450 | 43,602 | 17,459 | 13,316 | 127,041 | 76,158 |
| C-explicit-boundary | 38,084 | 26,027 | −31.7% | 99,450 | 43,602 | 17,489 | 13,376 | 127,311 | 76,428 |
| D-mutation-after-investigation | 38,014 | 26,010 | −31.6% | 99,450 | 43,602 | 17,463 | 13,332 | 127,079 | 76,230 |
| E-embedded-mutation | 38,104 | 26,090 | −31.5% | 99,450 | 43,602 | 17,497 | 13,392 | 127,383 | 76,500 |
| F-negative-control | 24,639 | 13,976 | −43.3% | 69,615 | 22,939 | 15,712 | 10,301 | 77,874 | 37,685 |
| G-debugging | 19,587 | 12,752 | −34.9% | 59,670 | 29,000 | 12,847 | 3,899 | 61,345 | 34,822 |
| H-refactoring | 34,031 | 31,758 | −6.7% | 99,450 | 81,027 | 13,731 | 5,053 | 114,884 | 97,403 |
| I-fuzzing | 12,952 | 7,244 | −44.1% | 39,780 | 15,388 | 12,823 | 7,715 | 36,317 | 18,023 |
| J-external-knowledge | 16,880 | 9,721 | −42.4% | 49,725 | 18,328 | 13,832 | 4,942 | 50,266 | 23,839 |
| K1-irrelevant-large-file | 121,867 | 8,505 | −93.0% | 39,780 | 13,108 | 158,282 | 7,191 | 326,525 | 19,678 |
| K2-missing-file | 9,593 | 4,808 | −49.9% | 29,835 | 9,831 | 12,744 | 7,033 | 23,670 | 10,334 |
| K3-failed-verification | 13,517 | 8,593 | −36.4% | 39,780 | 17,860 | 13,660 | 9,486 | 37,725 | 21,026 |
| K4-unexpected-mutation | 17,063 | 10,570 | −38.1% | 49,725 | 20,827 | 13,622 | 8,683 | 51,220 | 27,540 |
| K5-partial-execution | 6,234 | 3,377 | −45.8% | 19,890 | 8,190 | 11,917 | 7,180 | 11,742 | 5,645 |
| K6-large-source-context | 43,834 | 23,413 | −46.6% | 69,615 | 22,939 | 45,472 | 24,737 | 124,897 | 44,526 |
| K7-stop-after-proof | 17,135 | 8,507 | −50.4% | 49,725 | 13,133 | 13,722 | 4,588 | 51,402 | 20,898 |

### 3.1 Aggregate

| Measure | Baseline | Projected | Change |
|---|---:|---:|---:|
| Estimated input tokens, full matrix | 514,142 | 261,234 | **−49.2%** |
| Estimated input tokens, excluding K1 outlier | 392,275 | 252,729 | **−35.6%** |
| Median scenario change | — | — | **−42.4%** |
| Mean scenario change | — | — | −41.3% |
| Tool-schema bytes, full matrix | 1,084,005 | 469,917 | −56.7% |
| Context bytes, full matrix | 894,401 | 527,320 | −41.0% |
| Repeated-context bytes, full matrix | 1,554,441 | 704,306 | −54.7% |
| Raw tool output | 213,631 | 213,576 | effectively unchanged |

The raw tool output is essentially unchanged: projection did not make the
tools produce less. It changed what the model was asked to re-read.

### 3.2 The README case

Baseline:

- 10 requests, 9 tool calls, 38,004 estimated input tokens.
- First request 11,722 bytes, of which 9,945 were tool definitions.
- Last request 17,459 bytes.
- 127,041 repeated bytes.

Projected:

- Same 10 requests and 9 tool calls; the scripted path is unchanged.
- First request 5,980 bytes, with roughly 4.2 KiB of tool schema; the offered
  surface totals 43,602 schema bytes across the ten requests.
- Last request 13,316 bytes.
- 76,158 repeated bytes.
- 23.7% fewer bytes on the final request; 31.7% fewer estimated input tokens.

The fixed first-request cost fell because the task shape withheld delete,
edit (the target was absent), wait (no background job), Go structural tools
(no code-navigation need), and external tools (no external requirement).

## 4. Why some scenarios improved less

H (refactoring) improved only 6.7%. This is not a failure of the mechanism; it
is an honest boundary.

- The task names four files, asks to update callers, and requires a test run.
- Structural code navigation and relationship tools are genuinely relevant, so
  most of their schema bytes remain.
- The final request still fell from 13,731 to 5,051 bytes because old file
  reads were summarized; the total is dominated by the fixed tool surface on
  the first several requests.
- Further improvement would require compressing or staging tool schemas, which
  Phase 9 did not attempt.

K1 improved 93.0% because the scenario reads a 146 KB irrelevant log once.
On the next request the log is outside the exact working set and is represented
by a bounded excerpt plus provenance. This is the intended behavior for
irrelevant evidence, but it is also the clearest retention trade-off: a
critical large file would need to be re-read around the relevant line.

## 5. Safety and adversarial results

All adversarial controls preserved their verdict:

| Control | Result |
|---|---|
| K1 irrelevant large file | `COMPLETED`, read-only, no mutation |
| K2 missing file | `COMPLETED`, missing file reported, no mutation |
| K3 failed verification | `NEEDS_REVIEW`; failed check remains failure evidence |
| K4 unexpected mutation | `NEEDS_REVIEW`; `extra.txt` reported as outside scope; further mutation blocked, verification still allowed |
| K5 partial execution | `NEEDS_REVIEW`; a started background job is not finished execution |
| K6 large source | `COMPLETED`, read-only, final request reduced 45.6% |
| K7 post-proof exploration | `COMPLETED`; extra `ls` withheld as a candidate rejection (1), completion unchanged |

K7 is the deterministic sufficiency stop. The model attempted an unnecessary
listing after the write and successful build check. The runtime withheld the
tool; the result was `CandidateRejected` (an error for the model, not an
execution failure); completion remained `COMPLETED`.

Authority tests (unit-level):

- a projected tool still passes the security boundary; a DENY decision never
  executes;
- a projection that proposes a tool outside the loop's constructed set is
  ignored and the report says so;
- an empty message projection falls back to the live conversation;
- a provider failure with projection installed never claims completion;
- a candidate rejection is not counted as a pending tool failure.

## 6. Baseline cost decomposition

Across the baseline matrix:

- tool definitions: **1,084,005 bytes (54.8% of request bytes)**;
- system/context: **894,401 bytes (45.2%)**;
- raw tool output: 213,631 bytes.

For the short README task alone, tool definitions were **84.8% of the first
request** and **~69% of the total request bytes** across the run. For the
long-running refactor, the fixed surface still cost 99,450 bytes across 10
requests.

This is the central measured result: for short coding tasks the runtime was
spending most of its request budget describing capabilities the task could not
use, and then re-sending tool results. The Phase 9 changes attack both, in that
order.

## 7. Model limitation vs runtime inefficiency

The scripted arm fixes model behaviour. Therefore:

- differences between baseline and projected arms are **runtime effects**;
- the remaining absolute cost is a mix of runtime and model policy
  (the model chose how many files to read);
- live provider tokens include model planning, provider tokenizer, and cache
  accounting and are not directly comparable to the estimates here.

To distinguish the two in a real deployment, use the live mode. The measured
columns are the model-behaviour columns the prompt requires: success,
correctness, turns, tool calls, input/output tokens, files inspected, failed
actions, time, unexpected mutations, verification quality.

## 8. Reproduce

```sh
go run ./bench/phase9 -projection off   -json
go run ./bench/phase9 -projection state -json
go test ./bench/phase9 -run TestPhase9 -v
```

Shell tool results include elapsed milliseconds, so a fresh run can differ by a
few bytes from the representative numbers above. `docs/benchmarks/results/`
is the reference artifact for the reported run.

The test matrix asserts, for every scenario:

1. the baseline meets its observable expectation;
2. the projected arm meets the same expectation;
3. projected input tokens do not exceed baseline;
4. projected tool-schema bytes do not exceed baseline;
5. a mutation scenario produced evidence.

It also asserts the proof-stop behavior and the large-source retention floor.

## 9. Remaining uncertainty

- No live provider comparison was run. Model differences, provider cache
  discounts, and exact tokenizer behavior are unmeasured.
- The fixtures are synthetic. The absolute numbers are not a prediction for
  another repository.
- The scripted sequence is one trajectory. A model that reads fewer files or
  stops earlier may see a smaller benefit; a model that explores more may see a
  larger one.
- K1's 93% is an outlier driven by one irrelevant 146 KB read. Excluding it,
  the matrix total falls 35.8%, which is the more conservative summary.
- Retention of very large exact source is a bounded-window trade-off, not a
  proof that the model will always re-read the right section.
