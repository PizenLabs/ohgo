# Phase 10 — Final Synthesis

## 1. Executive Verdict

**PASS**

The final Ohgo runtime passes the Phase 10 contract: authority remains
deterministic, evidence remains bounded and truthful, completion remains
evidence-owned, context reduction is reversible, the Phase 9 efficiency baseline
is preserved, the adversarial matrix is green, and the final tree passes the full
verification suite.

The verdict is based on the deterministic matrix, the new Phase 10 hardening
tests, the real-model CONTROL/COGNITIVE runs, and the final verification
commands — not on architecture aesthetics or token reduction alone.

The primary full real-model condition preserves answer facts and verified
successes while cutting input tokens by 43%. The overall strict verified count
is lower on two secondary model conditions due to model tool-use/answer
coverage, not a runtime defect; that trade-off is quantified in §4 and §6.

---

## 2. Transformation Summary

```text
repository intelligence
        ↓
execution safety
        ↓
external knowledge boundaries
        ↓
cognitive runtime
        ↓
final validation + freeze
```

The final architecture is the same one Ohgo has been building across the cycle,
not a new phase:

```text
USER → INTENT → TASK SHAPE → EVIDENCE → SUFFICIENT CONTEXT
     → VALID ACTION SPACE → LLM REASONING → AUTHORITY
     → EXECUTION → EVIDENCE UPDATE → SUFFICIENCY
     → VERIFICATION → PROVEN RESULT
```

Phase 10 did not add another planning layer, repository index, authority model,
or orchestration path. It validated the existing one, found concrete boundary
defects, fixed them, and froze the contracts.

---

## 3. Before / After

### 3.1 Controlled scripted matrix (runtime-only)

Same fixtures, same scripted action sequence, same tools, same budget. Only the
projection differs.

| Measure | Pre-Phase-9 CONTROL | Final COGNITIVE | Result |
|---|---:|---:|---|
| Estimated input tokens, Phase 9 matrix | 514,157 | 261,961 | **−49.05%** |
| Estimated input tokens excluding K1 | 392,290 | 253,412 | −35.40% |
| Median scenario | — | — | −40.73% |
| Tool-schema bytes | 1,084,005 | 469,917 | −56.65% |
| Context bytes | 894,436 | 530,224 | −40.72% |
| Repeated-context bytes | 1,554,471 | 727,693 | −53.19% |
| Raw tool output | 213,633 | 213,575 | unchanged |
| Objective statuses | — | — | identical per scenario |
| Tool calls / turns | — | — | identical per scenario |

The Phase 9 published result was −49.2% input tokens, −56.7% tool schema, and
−54.7% repeated bytes. The final tree keeps the input-token and schema results;
repeated bytes are 1.5 points higher because the state block now always carries
task scope, verification, completion, and sufficiency. That is the cost of the
Phase 10 context invariant, and it is reported rather than hidden.

### 3.2 Phase 10 added suite

| Measure | CONTROL | COGNITIVE | Result |
|---|---:|---:|---|
| Estimated input tokens, 20 scenarios | 540,471 | 276,305 | **−48.88%** |
| `P10-C1-read-file` | COMPLETED, no mutation | COMPLETED, no mutation | pass |
| `P10-C2-read-then-update` | COMPLETED, mutated+verified | COMPLETED, mutated+verified | pass |
| `P10-F-package-name` | COMPLETED, one read | COMPLETED, one read | pass |

### 3.3 Correctness, security, completion, observability

| Dimension | Before Phase 10 | Final |
|---|---|---|
| Projected tool identity | name-only intersection could substitute a fake tool | loop-owned instance always executes |
| Evidence conflicts | different read windows could conflict | only same-window observations conflict |
| Exact context budget | old small results could bypass the 16 KiB working set | budget enforced |
| Denied/refused calls | counted as pending failures | control decision, not execution failure |
| Read-only completion | could accept an observed mutation | rejected |
| Informational Go-path questions | could require an unrequested build/test | no verification required |
| Evidence-free repository answers | could be `COMPLETED` | projection veto → `NEEDS_REVIEW` |
| Headless authority label | could say BUILD while the gate was ASK/FUZZ | routed handler used |
| Read-only delegation | refused instead of restricted | child receives observation tools only |
| State-block truncation | could drop contract/sufficiency | critical contract first, evidence last |
| Explicit no-mutation/no-external | model-facing only | shape narrows authority/external need |
| Repository overview | a directory listing was enough | content-bearing evidence required |
| Named-file content questions | `target_state` could be satisfied by a listing | `target_content` requires a read or exact structural coordinate |
| Question prose | “package declared/name” invented package targets | generic-word stop list |
| Projection overhead | unmeasured | ~0.25 ms, ~95 KiB/op on 200 items |

The observable net effect is not fewer tokens alone. It is a narrower action
surface, a smaller and more provable context, a clearer state briefing, and a
completion gate that refuses answers the runtime cannot support.

---

## 4. Real Model Results

The full A–J real-model condition is the available local/medium model. Remote
primary conditions could not be used: Gemini 2.5 exhausted its free-tier quota
(HTTP 429), Groq returned 403, paid OpenRouter had no credits (402), and Gemini 3
tool replay failed on `thought_signature`. The failed conditions and the
explicit substitution are recorded in `PHASE10_EFFECTIVENESS.md`; no result was
fabricated.

| Condition | Pairs | CONTROL statuses | COGNITIVE statuses | Tokens C/G | Delta | Facts C/G | Verified C/G |
|---|---|---:|---|---|---:|---:|---:|---:|
| local/medium (full A–J + P10 + K1/K2/K7/K3–K6) | 20 | 7 COMPLETED / 13 REVIEW | 3 COMPLETED / 17 REVIEW | 124,612 / 70,892 | **−43.1%** | 24/25 of 48 | 2/2 |
| local/small (representative) | 8 | 1 COMPLETED / 7 REVIEW | 0 COMPLETED / 8 REVIEW | 50,096 / 25,817 | **−48.5%** | 6/5 of 21 | 1/0 |
| free router (A/G/K1) | 3 | 2 COMPLETED / 1 REVIEW | 1 COMPLETED / 1 REVIEW / 1 PAUSED | 62,934 / 42,851 | −31.9% | 7/6 of 11 | 1/0 |

Primary findings:

- On the full local/medium condition, **verified successes are preserved (2 in
  both arms)** and answer facts are 24/48 CONTROL vs 25/48 COGNITIVE after the
  wrong P10-F CONTROL answer is corrected, while estimated input tokens fall
  43.1% (124,612 → 70,892). The cognitive arm returns fewer
  `COMPLETED` verdicts (3 vs 7) because it removes shallow completions (A, K1,
  K2, K6) and the wrong P10-F control answer. One correct negative control (F) is
  downgraded to review; a correct P10-F answer is completed by COGNITIVE, so the
  verified-success count is equal.
- On the weak 3B local condition, tokens fall 48.5% but one verified success is
  lost because the model called `grep` with an invalid `limit` and then guessed
  that the file did not exist. The read tool and target were offered. This is a
  model tool-use failure, and the runtime truthfully returned `NEEDS_REVIEW`.
- On the free router, A is broadly correct but omits `style.css` (strict facts
  4/5); G is a correct review in COGNITIVE while CONTROL completes a 2/3 answer;
  K1 hits the 8-turn bound and is reported `PAUSED`, not failed. The router is
  variable and is not used for a remote quality claim.
- The overall strict verified-success count (4 control / 2 cognitive) is lower
  because the two secondary conditions each lose one pair (a 3B tool-use error
  and a router answer omission). This is a measured strictness/model-behavior
  trade-off, not a runtime authority or completion defect. The primary full
  condition preserves correctness and improves verified work per token.



---

## 5. Adversarial Results

Full matrix: `docs/report/PHASE10_ADVERSARIAL.md`.

- No authority bypass: projection cannot add tools, substitute a tool, or grant
  permission; every remaining call crosses the security gate.
- No mutation-scope acceptance: an out-of-scope or unpermitted change is
  detected, blocks further pure mutation, and returns `NEEDS_REVIEW`.
- No evidence trust escalation: denials and candidate refusals are not evidence;
  external content is untrusted; model text is not evidence.
- No completion false positive after the hardening: evidence-free answers are
  vetoed; unverified mutations, failed checks, partial background work,
  provider failures, and cancellations remain distinct and truthful.
- Failure recovery is tested for tool failure, malformed arguments, provider
  failure, timeout, cancellation, budget stop, incomplete evidence, rejected
  candidates, verification failure, and conflicting evidence.
- Context reduction is reversible: the live transcript and durable evidence are
  retained; old results become provenance-bearing summaries; the model can
  re-read.

All deterministic adversarial tests pass under `go test` and `go test -race`.

---

## 6. Known Limitations

### Known limitation

1. **Informational quality is not semantically proven.** Completion proves an
   answer exists and (in COGNITIVE) that required evidence was acquired. It does
   not prove factual correctness. The benchmark's answer-facts column is a
   heuristic, not a judge.
2. **Per-path mutation scope is a completion boundary.** BUILD authority permits
   a workspace write; an out-of-scope write is detected and reviewed, not
   prevented before execution. The same applies to an explicit `no_mutation`
   constraint: it withholds pure mutation tools but does not make an
   execution-capable BUILD shell read-only; a shell mutation is reviewed.
   Pre-execution path scope would be a new authority model and is not part of the
   frozen contract.
3. **External evidence is untrusted, not fact-checked.** It is bounded and
   provenance-bearing and cannot authorize or complete.
4. **A trusted host projector can replace messages.** The loop protects tool
   identity and authority, not a hostile host process. A host that omits
   `Config.Project` also opts out of the evidence review; production TUI, plain
   REPL, headless, and subagent paths install it.
5. **The evidence review is stricter than answer correctness.** A correct
   negative/existence answer supported only by a directory listing can be
   `NEEDS_REVIEW` in COGNITIVE while CONTROL marks it `COMPLETED`. This is the
   proof-over-convenience trade-off, not a false success; it is visible in the
   per-scenario real-model table.
6. **Verified success is not uniformly higher across every model.** The primary
   full local/medium condition preserves facts and verified successes; the weak
   3B and variable free-router conditions each lose one pair to model tool-use
   or answer-coverage behavior. This is reported, not hidden; it is a model
   quality limit, not a runtime authority or completion defect.
7. **Exec attribution is bounded.** A shell command that mutates an unnamed path
   is unattributed and forces review; it cannot produce a false completion.

### Measurement gap

1. **One live run per model/scenario.** Real-model variance, especially the free
   router, is not measured. Repeats are future measurement work.
2. **Provider availability.** Groq was HTTP 403, paid OpenRouter was HTTP 402,
   Gemini 2.5 free-tier quota was HTTP 429 after the probes, Gemini 3 Flash tool
   replay failed on `thought_signature`, and OpenRouter free Gemma was HTTP 429.
   The full A–J real-model comparison is therefore the available local/medium
   condition; the failed Gemini attempts are retained as provider evidence. No
   result was fabricated.
3. **Free router identity.** `openrouter/free` is a router; the upstream model
   can change between runs.
4. **Synthetic fixtures.** Absolute costs are not a prediction for another
   repository; the controlled comparison is the evidence.
5. **No paid billing measurement.** Provider cost is zero/unknown for free/local
   conditions; it is not a bill.

### Future work

1. A semantic answer-quality judge (human or model) with calibrated agreement.
2. Repeats and confidence intervals for each model condition.
3. A provider-specific `thought_signature` replay path for Gemini 3 tool calls.
4. Optional pre-execution path-scope policy, if the product wants it as a new
   authority contract rather than a completion review.
5. Compressing or staging tool schemas for multi-file refactors (H remained the
   weakest Phase 9 scenario at −6.4%).
6. A durable `BLOCKED` task status if the UI needs it as a first-class badge;
   today it is a sufficiency verdict surfaced as `NEEDS_REVIEW` with a reason.

None of these is a disguised Phase 10 defect.

---

## 7. Frozen Contracts

These must not change casually after Phase 10. A future feature may add
capability; it may not silently weaken these:

1. **One authority plane.** `security.Policy` plus the workspace permission
   table is the only admission decision. No projection, candidate set, evidence
   item, external source, or model text grants authority.
2. **One execution path.** `agent.Loop.execute` is the only model-call-to-tool
   path. A projection may narrow the tool set; it may not substitute a tool
   instance or widen the constructed set.
3. **Evidence is observation, not narration.** Candidate refusals and
   authorization denials are not evidence. External evidence is untrusted.
   `UNKNOWN`/`CONFLICTING` may not become `KNOWN` without an observation.
   `target_state` proves existence/coordinates; `target_content` proves content
   or an exact-path structural coordinate. A listing alone is not content.
4. **Completion is evidence-owned.** `agent.EvaluateTaskCompletion` is the sole
   normal completion authority. `EvidenceReview` can only downgrade; it cannot
   authorize. A budget stop preserves already-proven mutation evidence.
5. **Context reduction is a view.** The live transcript and full tool set are
   retained. Only tool results may be elided in the built-in projector. The
   contract, mutation scope, verification, completion conditions, and
   sufficiency survive truncation.
6. **Task state has one owner.** `task.Controller` writes durable task state.
   The model cannot transition a task.
7. **External knowledge has no authority.** Search/browser/MCP content is
   bounded, provenance-bearing, untrusted data.
8. **Read-only delegation is real.** A read-only child receives only
   `agent.ReadOnlyToolNames`; it is not merely labelled read-only.
9. **Benchmark control.** CONTROL and COGNITIVE use the same model, settings,
   task, fixture, tools, authority, budget, timeout, and temperature. Only the
   projection and its completion review differ, and that difference is recorded.
10. **No model-identity policy.** Capabilities come from `ai.Model` and catalog
    facts, never a hardcoded model/provider table at a call site.

---

## 8. Final Verification

Run from the repository root on the final tree:

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./...
go test -race ./...
golangci-lint run ./...
```

Results:

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass |
| `golangci-lint run ./...` | 0 issues |
| Phase 9 scripted matrix | pass, statuses identical |
| Phase 10 scripted matrix | pass, statuses identical |
| Real-model matrix | see §4; failures recorded, no fabrication |

The exact run logs and artifacts are in `docs/benchmarks/results/` and the
Phase 10 reports.

---

## 9. Git State

```text
final code commit:        7d004c8 (content evidence hardening)
final documentation tag:  phase10-freeze
working tree:             clean after the final commit
```

The benchmark artifacts record the revision and working-tree status at the time
of the run. The final documentation/artifact commit is created after validation;
the working tree must be clean when Phase 10 is declared frozen.

---

## 10. Final Statement

Ohgo's final runtime makes the model's work smaller, clearer, better grounded,
reversible, and observable while keeping execution bounded, authorized,
verifiable, and truthful.

The frozen contract is:

> **LLMs reason. Ohgo decides. Capabilities execute. Evidence proves. Humans
> remain in control.**
