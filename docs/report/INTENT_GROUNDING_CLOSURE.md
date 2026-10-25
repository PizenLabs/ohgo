# Intent Grounding Closure

> **Date:** 2026-10-26
> **Verdict:** implemented; deterministic semantic grounding is on the request,
> action, projection, persistence, and completion seams.
> **No second agent. No per-request model call. No new authority path.**

## 1. Architecture summary

The runtime now distinguishes three things it previously collapsed:

```text
user request
    → grounded intent contract        (what the user actually said)
    → model action proposal           (untrusted tool call)
    → execution evidence              (what the runtime observed)
    → completion verdict              (what the evidence proves)
```

The model still interprets natural language. The runtime validates the
interpretation against the user's own words before that interpretation can
mutate state or complete a task.

Integration points:

| Seam | Change |
| --- | --- |
| Request extraction | `workspace.BuildIntentContract` attaches `contract.IntentGrounding` |
| Semantic admission | `agent.Loop` refuses a mutation the request did not ground, before `Authorize` |
| Context | `cognition.TaskShape` and `RenderState` carry entities, action, ambiguity |
| Completion | `agent.EvaluateTaskCompletion` calls `agent.AssessGrounding` before `COMPLETED` |
| Persistence | `task.cloneContract` deep-copies grounding; the existing `IntentContract` checkpoint stores it |
| Subagents | `RunSubagent` gives a child its own assignment grounding, never the parent's |
| `$sup` | unchanged, human-invoked, not on the request path |

## 2. Files changed

New:

- `contract/grounding.go` — grounding/proposal/assessment data types.
- `workspace/grounding.go` — deterministic extraction.
- `agent/grounding.go` — proposal validation and answer assessment.
- Tests: `workspace/grounding_test.go`,
  `workspace/grounding_integration_test.go`, `agent/grounding_test.go`,
  `task/grounding_test.go`, `cognition/grounding_test.go`.
- Benchmark: `bench/intent/main.go`.
- Docs: `docs/architecture/INTENT_GROUNDING.md`, this report.

Modified:

- `contract/contract.go` — `IntentContract.Grounding`.
- `workspace/contract.go` — attach grounding; package stop-words no longer
  invent numeric package targets.
- `agent/loop.go` — `Config.Grounding`, `SetGrounding`, semantic admission.
- `agent/completion.go` — grounding review before completion.
- `agent/subagent.go` — child grounding from its own contract.
- `task/task.go` — deep copy of the persisted grounding.
- `cognition/shape.go`, `cognition/render.go` — state projection.
- `cmd/ohgo/outcome.go`, `tui/app.go` — install/clear grounding at the turn
  boundary.
- `bench/phase9/scenarios.go` — K2 now expects `NEEDS_REVIEW`: a failed read is
  not proof of repository-wide absence.
- `docs/architecture/ARCHITECTURE.md` — pointer to this layer.

## 3. Semantic contract

`contract.IntentGrounding`:

```text
objective, action, actions[]
entities[]        (project | path | symbol | package; optional flag)
scope             (unknown | named | repository; conditional flag)
expectedOutcome, constraints[]
assumptions[]     (grounded | inferred | unsupported)
ambiguities[]     (unknown_action | unresolved_reference |
                   unresolved_alternative; material flag)
mutationIntent, confidence
```

A model proposal is represented independently as
`contract.ActionProposal{action, targets, mutates, executes, external}`. It is
derived from trusted tool metadata and call arguments, never from narration.

## 4. Validation rules

- **Entity preservation.** Required project entities must appear in an
  informational answer. Conditional entities are preserved but not forced.
- **Action fidelity.** A mutation under `mutationIntent=false` or
  `action=unknown` is `REJECT` at semantic admission. Reads remain allowed as
  bounded observation. An answer that narrates an unrequested action is
  `REVIEW`. Delegation under a non-mutation request is forced read-only.
- **Scope fidelity.** A named scope cannot be narrowed to “only the current
  repository”; an unresolved reference cannot be completed by inventing names.
- **Ambiguity preservation.** `unknown` stays `unknown`; an unresolved
  alternative cannot be chosen by the model for the user.
- **Evidence integrity.** Tool error and failed lookup remain tool error and
  failed lookup. A complete recursive file scan can support a concrete file's
  absence; concept absence (`documentation`, `tests`) is never proven by a file
  scan. “I could not find …” is an unverified negative result.
- **Completion integrity.** Grounding can only downgrade completion. Every
  structural target/verification/evidence rule still runs first.
- **Authority integrity.** Semantic admission narrows action space before the
  security gate. The gate remains the only authorizer.

## 5. Ambiguity and escalation

| Situation | Behaviour |
| --- | --- |
| Actionless context, observation only | Continue; no interruption |
| Actionless request proposes a mutation | `REJECT` before the security gate |
| Unresolved scope/alternative | Observation may proceed; completion is `NEEDS_REVIEW` |
| Answer drops a named entity or concludes unsupported absence | `NEEDS_REVIEW` |
| Authority violation | Existing `DENY` path, unchanged |

This avoids a clarification dialog for ordinary conversation while making the
observed failure impossible to mark `COMPLETED`.

## 6. `$sup` decision

Not integrated automatically. `$sup` is context-light by contract: no tools,
no repository evidence, no main history. It can compare a paraphrase, but it
cannot judge whether a repository-wide negative conclusion is supported by the
evidence it never sees. Automatic use would also add a provider call to the
common path without removing the need for deterministic validation.

`$sup` remains available as a human-invoked semantic second opinion. The
runtime itself stays deterministic and local-first.

## 7. Benchmark

Run:

```sh
go run ./bench/intent
go run ./bench/intent -json
```

The benchmark is deterministic and provider-free. It compares grounding
disabled (baseline completion) with grounding enabled on the same scripted
answers and turns. The JSON artifact is
`docs/benchmarks/results/intent-grounding.json`.

### Extraction (A–K)

```text
entity recall                 7/7   (100.0%)
action fidelity              11/11  (100.0%)
scope fidelity                4/4   (100.0%)
ambiguity preservation        3/3   (100.0%)
unsupported assumption rate   2/11  (18.2%, explicitly marked unsupported)
```

### Behaviour

```text
unsupported action admitted, baseline    6/6 (100.0%)
unsupported action admitted, grounded    0/6 (0.0%)
false completion, baseline               5/7 (71.4%)
false completion, grounded               0/7 (0.0%)
false negative conclusion, baseline      2/3 (66.7%)
false negative conclusion, grounded      0/3 (0.0%)
review boundaries on adversarial suite   7/10
estimated grounding tokens/request      116
extra model calls                        0
extra tool calls                         0
```

Case-level result:

```text
C-negative         COMPLETED → NEEDS_REVIEW
C-missing-entity   COMPLETED → NEEDS_REVIEW
C-grounded-ack     COMPLETED → COMPLETED
K-conditional      COMPLETED → COMPLETED
B-missing-entity   COMPLETED → NEEDS_REVIEW
G-unproven         COMPLETED → NEEDS_REVIEW
G-proven           COMPLETED → COMPLETED
H-tool-failure     NEEDS_REVIEW → NEEDS_REVIEW
E-unresolved       COMPLETED → NEEDS_REVIEW
J-benign           NEEDS_REVIEW → NEEDS_REVIEW
```

The 7/10 review rate is on an adversarial suite where most cases are bad
answers. It is not a predicted clarification rate for ordinary use.

## 8. Regression verification

```text
go build ./...                 pass
go vet ./...                   pass
gofmt -l .                     empty
go test ./... -count=1         pass
go test -race ./...            pass
```

`bench/phase9` was rerun in full after the K2 expectation change: pass. The
change is a semantic correction, not a weakened test: K2 answers “MISSING.md
does not exist” after a failed read, which the evidence contract now reviews.

`BuildIntentContract` extraction cost is roughly 0.06–0.09 ms/op on the
benchmark host, with no provider call and no additional tool call. The common
path remains model-latency dominated.

## 9. Known limitations

- Extraction is finite and pattern-backed. Explicit lists and coordinates are
  preserved; arbitrary paraphrase is not.
- Concept absence is never proven automatically, so some truthful “there is no
  …” answers remain `NEEDS_REVIEW`. This is the deliberate direction.
- The answer validator is English-pattern based; an unusual paraphrase of a
  global negative claim may not match. Structural completion rules remain in
  force.
- The layer cannot make a verified but semantically wrong change correct. It
  narrows the objective; it does not judge code quality.
- No live model comparison was run. The benchmark isolates runtime semantics
  with scripted model behaviour, so it does not claim a model-quality delta
  across providers. A live A/B would need the same provider, quota, and
  repository fixtures the Phase 10 arm documents.
- `$sup` is not an automatic escalator. A future benchmark could test a narrow
  opt-in audit, but the common path does not pay for it.
- Existing scripts that expected K2 to complete will now see `NEEDS_REVIEW`,
  which is the truthful verdict for an unproven absence claim.
