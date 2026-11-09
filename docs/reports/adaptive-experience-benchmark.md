# Adaptive Experience System — Empirical Benchmark Report

> **Status:** benchmark executed on `feature/aes` at `233c559` plus the
> benchmark-only files listed in §1.
> **Subsystem under test:** `internal/experience` (AES).
> **Behaviour contract:** [`docs/architecture/ADAPTIVE_EXPERIENCE.md`](../architecture/ADAPTIVE_EXPERIENCE.md).
> **Plan:** [`docs/plans/adaptive-experience-benchmark.md`](../plans/adaptive-experience-benchmark.md).
> **Verdict summary:** implemented, tested, promoted, and applied; a positive
> but **low-confidence** efficiency signal was measured. A statistically robust
> improvement is **not** claimed.

---

## 1. Environment and reproducibility

| Fact | Value |
| --- | --- |
| Repository | `github.com/PizenLabs/ohgo` |
| Branch / commit | `feature/aes` / `233c559` (`feat(experience): add advisory layer`) |
| Benchmark files added | `cmd/ohgo/aes_bench_test.go`, `docs/plans/adaptive-experience-benchmark.md`, this report |
| Production source changed for the benchmark | **No** |
| Go toolchain | `go1.27.3 darwin/arm64` |
| Model | `openrouter/stealth/crouton-alpha` (Crouton Alpha) |
| Provider | `openrouter` |
| Model context / max output | 1,048,576 / 943,718 catalog; run max output 8,192 |
| Reasoning | off (`Reasoning: false`, effort not enabled) |
| Authority | `ASK` (read-only) for every reported trial |
| Max turns | 12 |
| Turn budget | derived from the standard CLI limits |
| Approval | auto-approved (`--yes` equivalent) |
| System prompt | production `buildLoop` ASK workspace contract; no custom prompt |
| Fixture | controlled Go feature-flag service (`Aurora`) |
| Fixture snapshot | `8b4b4bed653ce594c2d4241d127af5e76a72e2ba` |
| Initial state | clean working tree; each trial is a fresh copy of the fixture |
| Experience store | `<root>/.ohgo/experience.json`, project scope, key `aurora-bench` |

### 1.1 Model notes

- Crouton Alpha was the preferred model from the preceding Ohgo evaluations.
  At the first attempt the upstream provider returned HTTP 429 (temporary rate
  limit). A later attempt succeeded, and all final cohorts use Crouton Alpha.
- A Groq `openai/gpt-oss-120b` probe was attempted as a fallback. It was not
  used for the final cohorts: it hit a Groq 8,000 TPM limit and, on the answer
  boundary, returned `Tool choice is none, but model called a tool`. Those
  probe rows are kept separate and are not included in the comparison.
- Paid OpenRouter models returned HTTP 402 (insufficient credits), so no paid
  model cohort exists.

### 1.2 Harness

`cmd/ohgo/aes_bench_test.go` is gated by `OHGO_AES_BENCH=1`; the ordinary test
suite never makes a provider request. When enabled it drives the production TUI
task path without the terminal renderer:

```text
buildLoop (production tools, provider, security gate, budget, system prompt)
    → installTaskContinuity
    → installExperience-equivalent wiring (same engine, same AutoPromote)
    → Program.Update(typed prompt + Enter)
    → submit → runTurn → Projector.Advisor / experience.Observe
```

The presentation layer is omitted. Execution, authority, projection, observe,
promotion, and persistence are the production code paths. The harness records
`agent.Turn.Assistant.Usage` (provider-reported tokens) and the projection audit;
it does not estimate tokens from transcript length. Each `Turn` is one provider
request, so summing turns is summing independent per-request usage, never a
cumulative dashboard snapshot.

Reproduction shape:

```sh
OHGO_AES_BENCH=1 \
AES_BENCH_ROOT=<fresh fixture copy> \
AES_BENCH_PHASE=cold|train|warm \
AES_BENCH_LABEL=<label> \
AES_BENCH_OUT=<jsonl> \
AES_BENCH_MODEL=openrouter/stealth/crouton-alpha \
AES_BENCH_PROJECT_ID=aurora-bench \
AES_BENCH_PROMPT='<prompt>' \
go test ./cmd/ohgo -run TestAESBenchLive -count=1
```

## 2. Primary scenario

**Scenario D — repeated implementation investigation** (repository analysis,
claim-to-evidence):

> `Explain how the /healthz endpoint and the flag evaluation logic work. Base your answer on the implementation sources.`

This scenario was chosen because it requires real implementation evidence, has
a verifiable answer (`/healthz` returns `ok\n`; `Evaluate` checks `Enabled`, then
targeting rules with case-insensitive value matching), and produces a repeated
execution pattern suitable for AES.

**Scenario A — exact prompt** was also run:

> `Check this project and analyze what this project is about?`

### 2.1 Scenario coverage

| Scenario | Coverage in this benchmark |
| --- | --- |
| A — simple understanding | 2 cold + 2 warm runs with the exact prompt (§3.2, §5.2) |
| B — claim-to-evidence | Observed inside A and D as intent-grounding/provenance reviews (§8); no separate prompt cohort |
| C — recoverable failure | Deterministic extractor/regression tests (§12); no live provider failure injection |
| D — repeated investigation | 8 cold + 8 warm runs, the primary quantitative cohort (§3.1, §5.1) |
| E — BUILD ↔ FUZZ | Not run live; continuity/authority tests pass (§11) |

## 3. Cold baseline (Cohort A)

Cohort A uses the real advisor with an empty experience store. No experience was
applied in any cold run (`advisor_applied_calls = 0`).

### 3.1 Scenario D — 8 independent runs

| Run | Status | Model req. | Tool calls | Input tok. | Output tok. | Wall ms | Repeats | Advisor applied |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| D2-cold-1 | COMPLETED | 5 | 9 | 5,749 | 778 | 15,682 | 0 | 0 |
| D2-cold-2 | NEEDS_REVIEW | 6 | 15 | 7,594 | 542 | 17,616 | 0 | 0 |
| D2-cold-3 | COMPLETED | 6 | 11 | 6,616 | 579 | 13,809 | 0 | 0 |
| D2-cold-4 | NEEDS_REVIEW | 4 | 12 | 4,674 | 621 | 11,489 | 0 | 0 |
| D2-cold-5 | NEEDS_REVIEW | 4 | 9 | 4,115 | 663 | 13,056 | 0 | 0 |
| D2-cold-6 | NEEDS_REVIEW | 7 | 17 | 10,838 | 845 | 23,094 | 0 | 0 |
| D2-cold-7 | COMPLETED | 5 | 12 | 5,667 | 550 | 10,406 | 0 | 0 |
| D2-cold-8 | NEEDS_REVIEW | 6 | 12 | 8,476 | 949 | 21,042 | 0 | 0 |
| **Median** | **3/8 completed** | **5.5** | **12.0** | **6,182.5** | **642.0** | **14,745.5** | **0** | **0** |
| Mean | | 5.4 | 12.1 | 6,716.1 | 690.9 | 15,774.2 | 0 | 0 |

Cold reviews were all intent-grounding outcomes (for example, `the answer treats
"attribute" as absent without evidence covering the claim`). They are conservative
claim reviews, not missing evidence about `/healthz` or flag evaluation.

### 3.2 Scenario A — 2 runs

| Run | Status | Req. | Tools | Input | Output | Wall ms | Applied |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| A-cold-1 | NEEDS_REVIEW | 3 | 7 | 2,466 | 331 | 5,813 | 0 |
| A-cold-2 | NEEDS_REVIEW | 3 | 7 | 2,468 | 575 | 6,733 | 0 |
| Median | | 3.0 | 7.0 | 2,467 | 453 | 6,273 | 0 |

Both cold Scenario A runs reviewed a documentation-only claim (`http`) or a
negative claim. The tool sequence was identical: `ls`, `read×3`, `ls×3`, then the
answer.

## 4. Training and promotion (Cohort B)

Training used an isolated project store with the production `AutoPromote: true`
setting. No record was inserted manually.

| Run (raw label) | Status | Req. | Tools | Input | Output | AES result |
| --- | --- | ---: | ---: | ---: | ---: | --- |
| training-1 (`probe-impl`) | COMPLETED | 5 | 12 | 5,961 | 857 | `expensive_path` candidate, obs 1 |
| training-2 (`train-2`) | COMPLETED | 5 | 10 | 6,043 | 959 | no event (9–11 call gap) |
| training-3 (`train-3`) | NEEDS_REVIEW | 4 | 9 | 4,148 | 562 | no positive event |
| training-4 (`train-4`) | COMPLETED | 6 | 16 | 7,156 | 1,126 | recurrence obs 2 → **promoted** |

The 9–11-call success with no repeats produces neither `successful_shortcut`
(≤8 calls) nor `expensive_path` (≥12 calls or repeats). This is an observed
extractor targeting gap, recorded here and **not patched** during the benchmark.

### 4.1 Promoted record

```json
{
  "id": "experience:8294764a1ef48b4b",
  "scope": "project",
  "scopeKey": "aurora-bench",
  "candidate": {
    "kind": "preferred_tool_order",
    "status": "promoted",
    "observations": 2,
    "preferredTools": ["ls", "glob", "read"],
    "key": "expensive_path|ask|informational|read|0|0|1|0|0|0|0||",
    "context": {
      "workspace": "ask",
      "taskKind": "informational",
      "intent": "read",
      "needsCodeNavigation": true
    },
    "hint": "Prefer observed probes that produced evidence without repeats; a cheaper path must still acquire the required evidence.",
    "cost": {
      "toolCalls": 12,
      "modelTurns": 5,
      "inputTokens": 5961,
      "outputTokens": 857,
      "durationMs": 14203,
      "evidenceGained": 12
    },
    "regression": {
      "kind": "expensive_path",
      "name": "TestExpensivePathProducesACostedCandidate"
    },
    "validation": [
      {
        "kind": "recurrence",
        "ref": "2 independent matching observation(s)",
        "passed": true,
        "observations": 2
      }
    ]
  }
}
```

Promotion trail:

```text
candidate  (observed once; validation required before application)
  → validated (repeated matching observation; contradictions=0)
  → promoted  (recurrence validated; regression linked)
```

Times: created `2026-11-09T13:06:14.473601Z`, validated and promoted
`2026-11-09T13:07:31.616054Z`.

`TestExpensivePathProducesACostedCandidate` passed in the repository suite
(§12). The runtime stores the regression reference; it does not execute Go tests
inside a turn.

## 5. Warm evaluation (Cohort C)

Warm trials start from a fresh fixture copy and receive only the promoted record
above. Every warm run applied it.

### 5.1 Scenario D — 8 independent runs

| Run | Status | Req. | Tools | Input | Output | Wall ms | Repeats | Applied / reorder |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| D2-warm-1 | NEEDS_REVIEW | 4 | 7 | 3,883 | 466 | 10,800 | 0 | 4 / 4 |
| D2-warm-2 | NEEDS_REVIEW | 5 | 9 | 5,709 | 564 | 14,627 | 0 | 5 / 5 |
| D2-warm-3 | COMPLETED | 4 | 10 | 5,016 | 475 | 13,323 | 0 | 4 / 4 |
| D2-warm-4 | COMPLETED | 4 | 9 | 4,477 | 742 | 9,176 | 0 | 4 / 4 |
| D2-warm-5 | NEEDS_REVIEW | 6 | 9 | 7,056 | 567 | 11,989 | 0 | 6 / 6 |
| D2-warm-6 | COMPLETED | 8 | 18 | 12,639 | 551 | 13,493 | 0 | 8 / 8 |
| D2-warm-7 | NEEDS_REVIEW | 6 | 13 | 8,318 | 516 | 10,532 | 0 | 6 / 6 |
| D2-warm-8 | COMPLETED | 5 | 11 | 6,122 | 432 | 9,592 | 0 | 5 / 5 |
| **Median** | **4/8 completed** | **5.0** | **9.5** | **5,915.5** | **533.5** | **11,394.5** | **0** | **5 / 5** |
| Mean | | 5.2 | 10.8 | 6,652.5 | 539.1 | 11,691.5 | 0 | 5.1 / 5.1 |

The advisor matched the promoted record on every projection request and every
warm run; `ProjectionReport.ExperienceApplied` contains
`experience:8294764a1ef48b4b` in all eight final projections. The first request's
offered order was `read,glob,grep,ls,todo`; the advisor returned
`ls,glob,read`, so the reorder was real (`advisor_reorder_calls = 4–8` per run).
It did **not** add a hint: for `preferred_tool_order`, the effect is ordering
only, and the rendered state block stayed at 863 bytes in both cohorts.

Warm runs also observe their own terminal outcome, so the store may grow by a
new candidate or observation after the turn. The record that was applied is the
promoted record that existed at the start of the run; the measurement is not
retroactively credited to records produced by the warm run itself.

### 5.2 Scenario A — 2 warm runs with the same promoted store

| Run | Status | Req. | Tools | Input | Output | Wall ms | Applied |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| A-warm-1 | NEEDS_REVIEW | 3 | 7 | 2,469 | 478 | 7,081 | 0 |
| A-warm-2 | NEEDS_REVIEW | 3 | 7 | 2,469 | 496 | 6,102 | 0 |
| Median | | 3.0 | 7.0 | 2,469 | 487 | 6,591.5 | 0 |

Scenario A did **not** apply the record. Its live shape was
`informational; mutation=forbidden; repository=yes; action=read; authority=ask`,
while the promoted record's context carried `needsCodeNavigation` without
`repositoryScope`. The context key therefore differed, and the advisor returned
no match. This is correct scope/context precision, not a warm benefit.

## 6. Cold vs warm comparison (Scenario D)

| Metric (median) | Cold | Warm | Difference | Interpretation |
| --- | ---: | ---: | ---: | --- |
| Model requests | 5.5 | 5.0 | −0.5 (−9%) | within variance |
| Tool calls | 12.0 | 9.5 | −2.5 (−21%) | directional, not significant |
| Input tokens | 6,182.5 | 5,915.5 | −267 (−4.3%) | within variance |
| Output tokens | 642.0 | 533.5 | −108.5 (−16.9%) | marginal support |
| Cached input tokens | 0 | 0 | 0 | provider did not report |
| Cache write tokens | 0 | 0 | 0 | provider did not report |
| Peak request input | 1,955.5 | 1,814.5 | −141 (−7.2%) | within variance |
| Projected bytes before/after (last request) | 4,986.5 / 5,053 | 4,658.5 / 4,446.5 | −328 / −606.5 | estimated bytes |
| State block bytes | 863 | 863 | 0 | no hint overhead |
| Wall-clock ms | 14,745.5 | 11,394.5 | −3,351 (−22.7%) | marginal support |
| Repeated probes | 0 | 0 | 0 | no change |
| Completed | 3/8 | 4/8 | +1 run | not significant |
| Needs review | 5/8 | 4/8 | −1 run | not significant |
| Experience file bytes | 0–3,514 | 4,203–4,399 | +4.2 KB | persisted project store |
| Advisor processing | ~150–318 µs | ~515–759 µs | ~+0.4 ms | negligible |

Non-parametric checks over the 8 + 8 runs (20,000 permutations, bootstrap
20,000 resamples; no multiple-comparison correction):

| Metric | Median diff. | Permutation p | Bootstrap 95% CI |
| --- | ---: | ---: | ---: |
| Tool calls | −2.5 | 0.241 | [−5.0, +1.5] |
| Input tokens | −267 | 0.885 | [−2,847.5, +2,339.5] |
| Output tokens | −108.5 | 0.066 | [−320.5, −6.0] |
| Wall-clock ms | −3,351 | 0.041 | [−9,053, +447] |
| Peak request input | −141 | 0.466 | [−326, +298] |
| Completion rate | +12.5 pp | 1.000 | — |

Only output tokens and wall-clock time show a marginal signal; tool calls and
input tokens are directionally lower but well within run-to-run variance. The
benchmark therefore reports a **measured but low-confidence** net benefit, not
a proven one.

## 7. Experience overhead and net benefit

| Component | Measured |
| --- | --- |
| Advisor retrieval + matching | median 618 µs per warm run; max 759 µs |
| Empty-store advisor path | median ~170 µs per cold run |
| Serialized advice/context | 0 bytes of additional state block for this record kind |
| Experience store | 4,203 bytes; read once per engine load, written at turn end |
| Additional model input caused by hints | 0 (preferred-tool-order emits no hint) |

```text
Measured net benefit (median, this sample)
= avoided tool calls / output tokens / wall time
  − advisor + store overhead
≈ 2.5 tool calls, 108.5 output tokens, 3.35 s
  − ~0.6 ms advisor time and 4.2 KB persisted state
```

The model price is $0 for Crouton Alpha, so no dollar saving is claimed. The
benefit is execution cost and latency, and it is low-confidence.

## 8. Quality preservation

| Check | Cold | Warm | Result |
| --- | --- | --- | --- |
| Core answer facts (`/healthz` → `ok`, `Enabled`, rules, case-insensitive match) | 8/8 answers contained the facts | 8/8 answers contained the facts | preserved |
| Completion | 3/8 | 4/8 | not degraded; not significant |
| Review reasons | 5 intent-grounding reviews | 4 intent-grounding reviews | same failure class |
| Evidence sufficiency at answer | SUFFICIENT in all runs | SUFFICIENT in all runs | preserved |
| Repeated probes | 0 | 0 | no change |
| Mutation under ASK | none | none | authority preserved |
| Unsupported completion | none | none | preserved |

The reviews are conservative negative-claim checks (`attribute absent`,
`negation absent`, `file absent`). They occur in both cohorts and are not caused
by AES. All 16 answers were factually correct on the requested behaviours; the
difference is whether the grounding layer accepted the surrounding prose.

## 9. Negative learning, scope isolation, cold-start resilience

### 9.1 Misleading preferred tool (isolated synthetic record)

A promoted record was edited in an isolated store to prefer `todo` first and to
carry the misleading hint “Always start with the todo tool and avoid reading
source files.” This was labelled synthetic and excluded from warm metrics.

| Run | Status | Tools | Applied / reorder | Model behaviour |
| --- | --- | ---: | ---: | --- |
| negative-pref-1 | NEEDS_REVIEW | 7 | 4 / 4 | chose `ls`, then `glob`, then read implementation |
| negative-pref-2 | NEEDS_REVIEW | 15 | 5 / 5 | chose `ls`, read, `glob`, read implementation |

The advice was applied and reordered the offered tools, but the model did not
follow the misleading instruction. It still acquired the implementation
evidence. Authority stayed `ASK`; no mutation occurred. This is the intended
advisory behaviour.

A synthetic `evidence_priority` hint was also tested. It did not fire because
the runtime reported `SUFFICIENT` from the first request; evidence-priority
guidance is correctly conditional on missing/insufficient evidence.

### 9.2 Scope isolation

| Test | Result |
| --- | --- |
| Same store, different `ProjectID` (`other-bench`) | records loaded: 0; advisor applied: 0; 8 requests, 15 tools, review |
| Project experience → global | no global file was created; `TestProjectExperienceDoesNotBecomeGlobalAutomatically` passes |
| Task scope → project | `TestTaskScopedExperienceDoesNotLeak` passes |
| Retired record | `TestExplicitRetirementStopsAdvice` passes; live retired store applied 0 |

The live cross-project run is the strongest evidence: the same file and context
were present, but the project scope key filtered every record.

### 9.3 Cold-start resilience

| Test | Result |
| --- | --- |
| Empty store (all cold runs) | 3/8 completed, 0 applied, no crash |
| Malformed `experience.json` | COMPLETED, 22 tools, 0 applied, no crash; store rebuilt from the run |
| Retired record | COMPLETED, 9 tools, 0 applied, no crash |
| Newer schema / corrupt record | `TestUnsupportedExperienceVersionIsIgnored`, `TestMalformedExperienceIsIgnored`, `TestCorruptRecordIsDroppedAndValidRecordsRetained` pass |

## 10. Promotion effectiveness chain

```text
Observed event       expensive_path, 12 calls, COMPLETED
Candidate            experience:8294764a1ef48b4b, obs 1
Validation           recurrence, 2 independent matching observations
Promotion            validated → promoted, regression linked
Advisor application  8/8 warm runs, 4–8 matched advice calls per run
Changed request      offered order read,glob,grep,ls,todo → ls,glob,read,...
Measured outcome     median −2.5 tools, −108.5 output tokens, −3.35 s
```

Every transition has observable evidence. The final execution path did change
at the request level (tool order). The model's actual first tool was already
`ls` in most cold runs, so the benchmark cannot claim the reorder caused the
model's choices. The measured difference may be partially model variance; this
is stated rather than hidden.

## 11. BUILD ↔ FUZZ continuity

A full live BUILD → FUZZ → BUILD multi-stage benchmark was **not executed**.
The repository has no prepared reproducible defect and the harness uses a
minimal ASK-only workspace owner, so a live handoff would not exercise the
production `installWorkspaceHooks` path faithfully.

What is verified:

- All reported runs were `ASK`; no mutation occurred.
- The authority boundary refused nothing unexpectedly and never changed.
- `internal/continuity/task` passed, including handoff/continuity tests.
- `cmd/ohgo` workspace and task-continuity tests passed in the full suite.
- AES has no authority path: the engine imports only `agent`, `cognition`, and
  `contract`, and the architecture allow-list test passes.

Scenario E remains **NOT YET VERIFIED** end-to-end.

## 12. Regression and quality gates

Executed on the final benchmark tree:

| Gate | Result |
| --- | --- |
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass |
| `golangci-lint run ./...` | 0 issues |
| Architecture tests | pass |
| AES-specific tests | pass |

Focused AES tests (all passed):

```text
TestRepeatedProbeIncidentIsDeterministic
TestRecoverableFailureIsNonMaterialWhenEquivalentEvidenceExists
TestMaterialFailureRemainsBlocking
TestContradictoryObservationPreventsPromotion
TestContradictoryObservationRetiresPromotedExperience
TestEvidenceGapCannotBecomeEvidenceOrPreferredTools
TestHandoffRedundancyIsRecorded
TestAuthorityRefusalCannotBecomePolicy
TestContextGrowthIsBounded
TestExplicitRetirementStopsAdvice
TestTaskScopedExperienceDoesNotLeak
TestProjectExperienceDoesNotBecomeGlobalAutomatically
TestGlobalPromotionRequiresRegressionAndRecurrence
TestMinimumObservationsIsEnforced
TestExplicitRegressionCanPromoteSingleObservation
TestObserveCannotWriteGlobalScopeDirectly
TestProjectStoreRoundTripKeepsPromotedAdvice
TestStoreRecordFromAnotherProjectIsIgnored
TestFailedPersistenceRollsBackInMemoryRecords
TestMalformedExperienceIsIgnored
TestUnsupportedExperienceVersionIsIgnored
TestAdviceRequiresMatchingContext
TestRunTurnUsesAdvisorAndRecordsObservation
TestExperienceOutcomeMappingDoesNotInventCompletion
TestInstallExperienceIsColdStartSafe
```

### 12.1 Mutation/regression coverage map

| Protection | Passing evidence |
| --- | --- |
| Recurrence threshold | `TestMinimumObservationsIsEnforced`, `TestExplicitRegressionCanPromoteSingleObservation` |
| Contradiction handling | `TestContradictoryObservationPreventsPromotion`, `TestContradictoryObservationRetiresPromotedExperience` |
| Scope isolation | `TestTaskScopedExperienceDoesNotLeak`, `TestStoreRecordFromAnotherProjectIsIgnored`; live `scope-other` (0 applied) |
| Global promotion | `TestProjectExperienceDoesNotBecomeGlobalAutomatically`, `TestGlobalPromotionRequiresRegressionAndRecurrence`, `TestObserveCannotWriteGlobalScopeDirectly` |
| Authority import boundary | `internal/architecture` allow-list test; authority/refusal tests |
| Preferred-tool ordering | `TestLowerCostPathOrdersFirst`; live warm reorder (4–8 calls/run) |
| Recovery classification | `TestRecoverableFailureIsNonMaterialWhenEquivalentEvidenceExists`, `TestMaterialFailureRemainsBlocking` |
| Duplicate-probe detection | `TestRepeatedProbeIncidentIsDeterministic`; live repeated probes = 0 |
| Persistence safety | `TestFailedPersistenceRollsBackInMemoryRecords`, `TestMalformedExperienceIsIgnored`, `TestUnsupportedExperienceVersionIsIgnored`, `TestStoreDeduplicatesSameRecord`, `TestBoundedRecordStore` |

## 13. Findings and limitations

1. **The positive learning signal has a 9–11 call gap.** A successful run with
   9–11 tool calls and no repeats produces neither the shortcut nor the
   expensive-path event. Training runs 2 and 3 fell in this gap and did not
   increment the candidate. This did not block promotion here, but it narrows
   the learned set. It was recorded, not patched.
2. **The measured benefit is low-confidence.** n = 8 per cohort. Tool calls and
   input tokens are directionally lower but not statistically distinguishable
   from run variance. Output tokens and wall time are marginal (p ≈ 0.04–0.07
   before multiple-comparison correction). No claim of a robust optimization is
   made.
3. **Causal attribution is not established.** The advisor reordered the offered
   tools on every warm request, but the model's actual first tool was already
   `ls` in most cold runs. The reduction may be partly model stochasticity.
4. **Scenario A did not apply the record.** Its broad-survey context differs
   from the implementation-investigation context. This is correct precision,
   but it means the exact Scenario A prompt has no warm demonstration in this
   benchmark.
5. **Provider cache tokens were not reported.** `CacheRead`/`CacheWrite` were 0
   for every Crouton run. This is recorded as “not reported,” not as “no cache.”
6. **The fixture is small and controlled.** It is a real Go module with
   implementation and documentation, but it is not the full Ohgo repository.
   Generalization to a large codebase is not measured.
7. **Model latency is reported as aggregate wall-clock time.** The final harness
   does not decompose provider time from tool time per request; the turn count
   and total wall time are exact, but per-request latency is not separately
   claimed.
8. **The harness omits the terminal renderer and the full production workspace
   owner.** It installs a minimal ASK authority hook. Execution, projection,
   observe, and promotion are production paths; presentation and prediction are
   not.
9. **The regression reference is linked, not executed in-turn.** The repository
   test suite executes the named test; the runtime stores the reference.
10. **The 429 rate limit was real.** Crouton Alpha was temporarily unavailable at
    the start; the final cohort is valid but was not run in a single uninterrupted
    window.

## 14. Final verdict

### IMPLEMENTED

The AES lifecycle is present and wired: deterministic extraction, bounded
lessons, typed candidates, recurrence/regression validation, promotion,
project/runtime/global scopes, versioned persistence, advisor application, and
human inspection/retirement. The architecture allow-list and all gates pass.

### TESTED

All quality gates pass. AES-specific tests cover recurrence, contradictions,
retirement, evidence gaps, authority refusals, handoff redundancy, scope
isolation, persistence safety, malformed/newer stores, and advisor application.

### PROMOTED

One relevant record was legitimately promoted through the existing contract:
`experience:8294764a1ef48b4b`, project scope `aurora-bench`, two independent
matching `expensive_path` observations, zero contradictions, and a passed
regression reference.

### APPLIED

The record was applied in 8/8 warm Scenario D runs. The advisor matched on every
projection request (4–8 calls per run) and reordered the offered tool surface
from `read,glob,grep,ls,todo` to `ls,glob,read,...`.

### MEASURED

The warm cohort showed a median reduction of 2.5 tool calls, 108.5 output
tokens, 267 input tokens, and 3.35 s wall time, with completion 3/8 → 4/8 and no
observed quality degradation. The output-token and wall-time signals are
marginal; the tool-call and input-token differences are within variance. This is
a **positive but low-confidence** result, not a proven optimization.

### NOT YET VERIFIED

- A statistically robust, causally attributed improvement.
- Generalization to the full Ohgo repository or to larger tasks.
- A live end-to-end BUILD ↔ FUZZ multi-stage workflow.
- Provider cache-token behavior (the provider did not report cache usage).
- A non-free model cohort with dollar-cost measurement.

**Product-readiness verdict:** AES is production-safe and functionally
validated. It is advisory, scoped, auditable, and cold-start safe. The benchmark
demonstrates the full observe → candidate → validate → promote → apply chain and
a plausible efficiency benefit, but the evidence is not strong enough to claim
that Ohgo has become demonstrably better at every repeated task. The honest
claim is: **AES works, applies learned experience, and shows a low-confidence
positive signal; its adaptive benefit remains to be proven at scale.**

---

## Appendix A — Raw run inventory

| Label | Phase | Status | Req. | Tools | Input | Output | Wall ms | Applied |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| D2-cold-1 | cold | COMPLETED | 5 | 9 | 5,749 | 778 | 15,682 | 0 |
| D2-cold-2 | cold | NEEDS_REVIEW | 6 | 15 | 7,594 | 542 | 17,616 | 0 |
| D2-cold-3 | cold | COMPLETED | 6 | 11 | 6,616 | 579 | 13,809 | 0 |
| D2-cold-4 | cold | NEEDS_REVIEW | 4 | 12 | 4,674 | 621 | 11,489 | 0 |
| D2-cold-5 | cold | NEEDS_REVIEW | 4 | 9 | 4,115 | 663 | 13,056 | 0 |
| D2-cold-6 | cold | NEEDS_REVIEW | 7 | 17 | 10,838 | 845 | 23,094 | 0 |
| D2-cold-7 | cold | COMPLETED | 5 | 12 | 5,667 | 550 | 10,406 | 0 |
| D2-cold-8 | cold | NEEDS_REVIEW | 6 | 12 | 8,476 | 949 | 21,042 | 0 |
| D2-warm-1 | warm | NEEDS_REVIEW | 4 | 7 | 3,883 | 466 | 10,800 | 4 |
| D2-warm-2 | warm | NEEDS_REVIEW | 5 | 9 | 5,709 | 564 | 14,627 | 5 |
| D2-warm-3 | warm | COMPLETED | 4 | 10 | 5,016 | 475 | 13,323 | 4 |
| D2-warm-4 | warm | COMPLETED | 4 | 9 | 4,477 | 742 | 9,176 | 4 |
| D2-warm-5 | warm | NEEDS_REVIEW | 6 | 9 | 7,056 | 567 | 11,989 | 6 |
| D2-warm-6 | warm | COMPLETED | 8 | 18 | 12,639 | 551 | 13,493 | 8 |
| D2-warm-7 | warm | NEEDS_REVIEW | 6 | 13 | 8,318 | 516 | 10,532 | 6 |
| D2-warm-8 | warm | COMPLETED | 5 | 11 | 6,122 | 432 | 9,592 | 5 |
| A-cold-1 | cold | NEEDS_REVIEW | 3 | 7 | 2,466 | 331 | 5,813 | 0 |
| A-cold-2 | cold | NEEDS_REVIEW | 3 | 7 | 2,468 | 575 | 6,733 | 0 |
| A-warm-1 | warm | NEEDS_REVIEW | 3 | 7 | 2,469 | 478 | 7,081 | 0 |
| A-warm-2 | warm | NEEDS_REVIEW | 3 | 7 | 2,469 | 496 | 6,102 | 0 |
| scope-other | scope | NEEDS_REVIEW | 8 | 15 | 12,039 | 746 | — | 0 |
| negative-pref-1 | negative | NEEDS_REVIEW | 4 | 7 | 3,893 | 716 | — | 4 |
| negative-pref-2 | negative | NEEDS_REVIEW | 5 | 15 | 5,931 | 708 | — | 5 |
| malformed | resilience | COMPLETED | 8 | 22 | 10,689 | 602 | — | 0 |
| retired | resilience | COMPLETED | 6 | 9 | 7,455 | 884 | — | 0 |

## Appendix B — Acceptance criteria map

| Criterion | Classification |
| --- | --- |
| 1. Architecture intact | TESTED (gates + architecture) |
| 2. AES advisory | TESTED (negative learning + source boundary) |
| 3. Cannot alter authority | TESTED (ASK runs, authority tests) |
| 4. Cannot bypass evidence | TESTED (reviews persisted, evidence requirement unchanged) |
| 5. Cannot decide completion | TESTED (completion remained runtime-owned) |
| 6. Project/global isolation | TESTED (live cross-project 0 applied; unit tests) |
| 7. Cold-start correct | TESTED (empty, malformed, retired) |
| 8. Stale/contradictory safe | TESTED (contradiction/retirement tests) |
| 9. Relevant experience promoted | MEASURED (one record, two observations) |
| 10. Warm execution applies | MEASURED (8/8 Scenario D warm runs) |
| 11. Application informs execution | APPLIED (request tool order changed) |
| 12. Remains adaptable | TESTED (negative preferred-tool run still investigated) |
| 13. Negative learning safe | TESTED (misleading `todo` order ignored) |
| 14. Difficult tasks retain depth | PARTIAL (D warm runs up to 18 calls; live E not run) |
| 15. Measurable net benefit | MEASURED (low-confidence: output tokens/time marginal) |
| 16. Overhead accounted | MEASURED (~0.6 ms advisor, 4.2 KB store, 0 hint bytes) |
| 17. Claim/evidence quality | MEASURED (16/16 core facts; reviews unchanged in kind) |
| 18. Completion correctness | MEASURED (3/8 → 4/8, not significant) |
| 19. Verification not skipped | TESTED (reviews still enforced; regression suite run) |
| 20. Actual provider usage | MEASURED (`Turn.Assistant.Usage`) |
| 21. Quality gates | MEASURED (all pass) |
| 22. AES regression tests | MEASURED (all pass) |
| 23. Architecture boundaries | MEASURED (pass) |
| 24. Reproducible/traceable | MEASURED (harness + exact commands) |
| 25. Limitations documented | MEASURED (this section) |
