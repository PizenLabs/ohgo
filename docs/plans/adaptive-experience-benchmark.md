# Adaptive Experience System — Empirical Benchmark Plan

> **Status:** benchmark plan, grounded in `feature/aes` at `233c559`.
> **Owner of the measured subsystem:** `internal/experience`.
> **Behaviour contract:** [`docs/architecture/ADAPTIVE_EXPERIENCE.md`](../architecture/ADAPTIVE_EXPERIENCE.md).
> **Final report:** [`docs/reports/adaptive-experience-benchmark.md`](../reports/adaptive-experience-benchmark.md).
> **Non-goal:** changing AES decision logic, promotion thresholds, authority,
> evidence requirements, completion, or budget in order to produce a warm win.

## 1. Question

Does Ohgo become measurably better at executing a repeated task after it
observes, extracts, validates, promotes, and applies execution experience?

The benchmark separates three facts that are often conflated:

1. **Implemented** — the lifecycle exists in source.
2. **Promoted/Applied** — a validated record influenced a projection.
3. **Measured** — the applied experience changed cost or reliability without
   degrading evidence or correctness.

Only the third is an improvement claim. A record that is applied but changes no
observable outcome is reported as applied, not as a measured benefit.

## 2. Current baseline (Phase 0 findings)

The benchmark runs the **durable TUI task path** because that is the only path
that installs experience in production:

```text
cmd/ohgo/run.go (interactive UI branch)
    └── installTaskContinuity(program, root, tools, authority)
    └── installExperience(program, root, projectID)
            ├── Advisor: *experience.Engine
            └── Observe: engine.Observe at turn terminal transition
```

- `ohgo run` is intentionally stateless and does **not** install an experience
  store. It is not a failure of AES.
- The advisor is consulted only when the durable task evidence hook is installed
  (`internal/tui/app.go` → `cognition.NewProjector` → `projector.Advisor`).
- `agent.ProjectionReport.ExperienceApplied` and `ExperienceHints` are the
  audit surface.
- Only `promoted` / `monitored` records reach `Advise`.
- Project writes go to `<root>/.ohgo/experience.json`; global writes require
  `PromoteToGlobal` (project scope, ≥3 observations, zero contradictions, passed
  regression) and are never written by a normal turn.

### 2.1 Promotion contract (verified in source)

```text
observation 1 ──> candidate
observation 2 ──> validated   (recurrence ≥ MinObservations, default 2)
validated + regression link + no contradictions ──> promoted (AutoPromote)
promoted ──continued matching observation──> monitored
candidate/validated ──contradiction──> rejected when material
promoted/monitored ──contradiction or explicit retire──> retired
```

`RegressionFor` maps every extractor event kind to a named repository test. The
runtime stores the reference; `go test` is the executor. The benchmark does not
fabricate regression evidence.

### 2.2 Measurement seams

| Metric | Source | Kind |
| --- | --- | --- |
| Model requests | number of `agent.Turn` | actual |
| Input/output tokens | `Turn.Assistant.Usage.Input/Output` | provider-reported |
| Cache read/write | `Turn.Assistant.Usage.CacheRead/CacheWrite` | provider-reported when supplied |
| Tool calls | `len(Turn.Results)` | actual |
| Repeated probes | same `agent.CallSignature` with no intervening mutation | derived from actual calls |
| Peak request input | max per-turn `Usage.Input` | provider-reported |
| Wall clock | harness monotonic start/end | actual |
| Model latency | per-request interval | derived |
| Completion | TUI terminal action + task projection | actual |
| Evidence/sufficiency | `ProjectionReport.Sufficiency`, `EvidenceReview`, `Provenance` | actual |
| Experience applied | `ProjectionReport.ExperienceApplied` | actual |
| Experience hints | `ProjectionReport.ExperienceHints` | actual |
| Preferred ordering | `ProjectionReport.ToolNames` | actual |
| Context size | `ProjectionReport.BytesBefore/BytesAfter/StateBlockBytes` | estimated bytes |
| Experience overhead | hint bytes + state-block delta + experience file bytes | measured/estimated as labelled |
| Storage | `.ohgo/experience.json` size and record count | actual |

Provider token counts are never inferred from transcript length. A provider that
reports no usage is recorded as missing, not zero.

## 3. Benchmark environment

Recorded at execution time:

- Repository snapshot: disposable copy of a controlled fixture repository and,
  for the primary live scenario, a disposable copy of a real repository tree.
- Initial Git state: commit id and clean/dirty state, recorded per trial.
- Model: the same model and provider for every cold and warm run in one cohort.
  Crouton Alpha (`openrouter/stealth/crouton-alpha`) is the preferred model from
  the preceding Ohgo evaluations. If it is rate-limited or unavailable, the
  benchmark uses a stable Groq-hosted tool-capable model and records the
  substitution. Cohorts with different models are never aggregated.
- Model configuration: max output tokens, reasoning setting, effort, max turns,
  budget, tools, system prompt, authority (`ASK` for informational scenarios).
- Prompt: byte-identical across cold and warm trials.
- Repository state: reset to the recorded initial state between mutation trials.
- Authority: `ASK` for repository understanding and claim investigation; BUILD/
  FUZZ only for Scenario E.

Isolation:

- Cohort A (cold) uses a store directory with no prior project experience.
- Cohort B (training) uses a separate project copy and store.
- Cohort C (warm) starts from a fresh task session on the same snapshot and
  loads only records promoted in Cohort B.
- Global experience is never populated. No unrelated repository record is
  loaded; the engine filters by scope key (`filterScopeLocked`).

## 4. Cohorts

### Cohort A — Cold baseline

Real advisor integration, empty experience store. Three independent runs for
Scenario A; two runs for more expensive scenarios. No experience is applied.

### Cohort B — Training

The same task conditions through an isolated store. Real runtime outcomes
produce candidates. Promotion happens only through `Observe`/`Validate`/
`Promote` under the existing contract. Training runs are excluded from warm
results.

### Cohort C — Warm evaluation

Fresh task sessions with the same snapshot, model, configuration, and prompt.
Only validated/promoted project records are preserved. The benchmark verifies
`ExperienceApplied` is non-empty; a stored-but-unapplied record is not a warm
benefit.

### Control (optional)

AES disabled is represented by a program built without experience hooks. Cold
start is not disabled AES: cold start keeps the real advisor and has no prior
record to apply.

## 5. Scenarios

### Scenario A — Simple repository understanding

Prompt (byte-exact): `Check this project and analyze what this project is about?`

Expected: correct project identity, bounded evidence, no unnecessary
implementation audit, no unsupported claims, no avoidable repeated reads.

### Scenario B — Claim-to-evidence investigation

Informational distinction such as configuration vs implementation, or
documentation vs source. Expected: experience may prioritize a probe; it cannot
satisfy a claim requirement; claims remain grounded.

### Scenario C — Recoverable tool failure

Controlled fixture: `ls(src)` succeeds, `read(src)` fails. The structural
requirement is already satisfied. Mirror case: a required implementation read
fails with no equivalent evidence and remains material. Fixture failures are
labelled as deterministic injected observations, never as provider observations.

### Scenario D — Repeated investigation

A task with a known opportunity for redundant exploration. Evaluate whether
promoted guidance avoids an equivalent repeat, reuses evidence, or selects a
more informative next probe. The advisor is advisory: a legitimate repeat after
new information is not waste.

### Scenario E — BUILD ↔ FUZZ workflow

A reproducible issue in an isolated copy, or a clearly labelled controlled
fixture when the repository has none. FUZZ must not mutate source; BUILD retains
mutation authority; findings remain hypotheses until verified; task identity,
evidence, and budget survive the handoff.

## 6. Execution sequence

1. Record `git rev-parse HEAD`, `git status --porcelain`, fixture tree hash.
2. Reset the project copy and delete `.ohgo/experience.json` for Cohort A.
3. Run cold trials; capture per-turn usage, projection report, tool sequence,
   outcome, and wall clock.
4. Run training trials in a separate copy until the existing contract yields a
   promoted record, or report the actual blocker.
5. Inspect the promoted records (`List`/`Explain`): identity, scope, incident,
   lesson, candidate, validation, regression, promotion reason, state.
6. Preserve only those records for Cohort C.
7. Run warm trials with identical conditions; require non-empty
   `ExperienceApplied` for a warm claim.
8. Run negative learning, scope isolation, and cold-start resilience probes.
9. Re-run all quality gates on the final tree.

## 7. Invariants under test

1. AES remains advisory; it cannot add, remove, or authorize a tool.
2. AES cannot change authority, evidence, completion, or budget.
3. A single observation never changes behavior.
4. Contradictory evidence blocks promotion and can retire an active record.
5. Project experience never becomes global automatically.
6. Task experience never silently becomes project experience.
7. A record from another repository is ignored.
8. Missing, malformed, or newer-version experience degrades to cold start.
9. Cold start with an empty store preserves existing behavior.
10. Experience is not evidence; a lesson cannot satisfy a requirement.

## 8. Aggregate statistics

- Report every individual run and the median for each cohort.
- Primary comparison is cold median vs warm median for the same scenario,
  model, prompt, and snapshot.
- Two-trial advanced scenarios are labelled lower-confidence; no significance
  claim is made.
- Net benefit is reported only where provider usage is available:

```text
Net benefit = avoided execution waste - experience overhead
```

Waste includes repeated probes, avoidable tool calls, and avoidable input
tokens that did not change the answer. Overhead includes hint bytes, advisor
retrieval/state growth, and experience store I/O where measurable.

## 9. Acceptance mapping

The final report uses the acceptance criteria from the task brief and classifies
each as IMPLEMENTED / TESTED / PROMOTED / APPLIED / MEASURED / NOT YET VERIFIED.
A quality decrease is never reported as an efficiency win. If no live measured
improvement is observed, the report says so and separates the implemented
architecture from the unproven adaptive benefit.

## 10. Known constraints at plan time

- `openrouter/stealth/crouton-alpha` is present in the cached catalog. At plan
  time the upstream provider returned HTTP 429 (temporary rate limit) and paid
  OpenRouter models returned HTTP 402 (insufficient credits). A Groq-hosted
  model was tried as a fallback; it was unsuitable for the final cohort because
  of an 8,000 TPM limit and a `tool_choice: none` incompatibility. Crouton
  Alpha later became available and is the model for every reported cohort. If a
  cohort used a different model it would be reported separately; no cohorts were
  aggregated across models.
- The live harness drives the real TUI task path (`Program.Update` → `submit` →
  `runTurn`) with the production `buildLoop`, `installTaskContinuity`, and
  `installExperience` seams, but without the terminal renderer. This changes
  presentation only; execution, authority, projection, observe, and promotion
  are the production code paths.
- Scenario E may use a controlled fixture because the current repository may
  not contain a reproducible defect that justifies a BUILD/FUZZ handoff. Any
  fixture is labelled.
