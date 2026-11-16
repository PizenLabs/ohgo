# AUTO Orchestration: Origin, Authority, and Reasoning Demand

> **Status:** implemented, deterministically verified, and benchmarked. Live
> acceptance is partially validated: routing and protected boundaries passed on
> a real repository, while a continuous live BUILD↔FUZZ master loop was blocked
> by the available provider quota and local-model tool reliability (see
> *Live Acceptance*). Certification is therefore **not** claimed.
> **Scope:** persistent task execution authority, capability-based role
> orchestration, protected authority transitions, and demand-driven
> provider-native reasoning selection.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](../architecture/OHGO_ARCHITECTURE_MAP.md).
> **Authority behaviour:** [`WORKSPACE_AUTHORITY.md`](../architecture/WORKSPACE_AUTHORITY.md).
> **Task continuity:** [`TASK_CONTINUITY.md`](../architecture/TASK_CONTINUITY.md).
> **Non-goals:** a second authority system, a second task store, a parallel
> orchestration loop, a role router beside `workspace.DecideTransition`, a
> universal reasoning enum, or per-model special cases in the engine.

## Objective

Make the existing runtime determine, for each request or internal phase:

1. the capability the work needs (`read`, `execute`, `investigate`, `mutate`);
2. the role that can perform it (`ASK`, `FUZZ`, `BUILD`);
3. whether that move is allowed by the master task's persistent execution
   contract, or needs a human decision;
4. the reasoning demand of the work, independently of the role;
5. the provider-native effort value, or omission when the model publishes no
   safe one.

The master task remains one identity. Role changes are execution phases of that
task, not new authority grants.

## Architecture Survey

```text
User request
    ↓
cmd/ohgo (composition)             CLI flags, session, model, gate
    ↓
task continuity (continuity/task)  durable objective, contract, role context
    ↓
control plane
    ├── workspace.AnalyzeRequest        capability + evidence level
    ├── workspace.DecideTransition      stay | automatic | confirm
    ├── workspace.PreflightTurn         required vs available vs permitted
    ├── workspace.RouteTurn             admission guard
    ├── contract.RequiredCapabilities   completion-contract needs
    └── cognition                       task shape, evidence, completion input
    ↓
harness.SwitchWorkspace (cmd)       the single authority move
    ↓
agent loop (runtime)                grounding → handoff → authorization → tool
    ↓
security gate + workspace.Admit     per-call enforcement
    ↓
execution/tools                     read / execute / mutate
    ↓
completion + evidence               agent evaluators; task proof
```

The discovered owners are:

| Responsibility | Owner | Existing type / call path |
| --- | --- | --- |
| Capability vocabulary | `control/contract` | `Capability`, `CapabilityNeed`, `RequiredCapabilities` |
| Request capability and evidence level | `control/workspace` | `AnalyzeRequest`, `ClassifyPrediction` |
| Transition policy | `control/workspace` | `DecideTransition`, `TransitionDecision` |
| Capability preflight | `control/workspace` | `PreflightTurn`, `RouteTurn` |
| Per-call admission | `security` + `workspace` | `securityGate.Evaluate` → `workspace.Authority.Admit` |
| The one authority move | `cmd/ohgo` | `harness.SwitchWorkspace` → `authorityGate.Set` |
| Task identity, contract, role, handoff | `continuity/task` | `Task.Contract`, `Task.ActiveRole`, `Task.Handoff` |
| Durable checkpoint | `continuity/task` | `progress.json`, `ProgressVersion` |
| Task shape / evidence | `control/cognition` | `BuildShape`, `EvidenceState`, `Projector` |
| Model capabilities | `provider/ai` + `provider/catalog` | `ai.Model.Reasoning*`, `catalog.Thinking` |
| Turn execution | `agent` | `Loop.Run`, `SetWorkspaceHandoff`, `SetReasoning` |
| Completion verdict | `agent` | `ClassifyRun`, `EvaluateTaskCompletion` |

## Existing Owners

- **Role routing already exists.** `workspace.DecideTransition` is the one
  policy. `AnalyzeRequest` reads capability, not keyword equality. BUILD ↔ FUZZ
  is automatic; ASK is confirmed.
- **Task continuity already exists.** `task.Task` is the durable objective and
  `task.Handoff` is the durable transition record. Neither is authority.
- **Capability preflight already exists.** `PreflightTurn` separates
  `AUTHORITY_DENIED` from `CAPABILITY_UNAVAILABLE`.
- **Provider-native reasoning already exists.** `ai.Model` carries
  `Reasoning`, `ReasoningEfforts`, `ReasoningDisable`, and
  `ReasoningMandatory`; `catalog.ThinkingFor` is the authoritative ladder.
- **The authority move already exists.** `harness.SwitchWorkspace` is the only
  writer of the live gate, session authority, and effective role model.

## Genuine Gaps

The current policy uses the **live role** as the whole authority fact. It cannot
answer whether a FUZZ-origin task has *ever* been authorized to mutate, and it
cannot distinguish "a BUILD task currently answering a question" from "an ASK
task that was never allowed to execute". The task record keeps the completion
contract and the last role, but not the authority the task was originally
authorized under or the human approvals that widened it.

Reasoning is currently a role setting or a global default. It is not derived
from the work, so:

- a hard analysis under ASK and a one-line question under ASK receive the same
  effort;
- an explicit role model silently ties reasoning depth to role;
- an empty effort can be turned into an invented `medium` by a transport.

## Required Behavior

### 1. Persistent execution contract

A task records:

```text
ExecutionContract
  Origin   : ask | fuzz | build   (immutable after creation)
  Execute  : bool                 (may run/investigate)
  Mutate   : bool                 (may change the repository)
```

`Origin` is the authority under which the master task was created. It survives
role changes, handoffs, checkpoint recovery, and session resume. Capability
flags only widen through an explicit human authorization (an accepted
transition or an explicit workspace command). They never widen because the model
proposed work.

The contract is:
- created from the live role when a new task is created;
- persisted in `progress.json` with the task;
- cloned on read and on `Task.Clone`;
- derived conservatively from the restored role when loading a checkpoint that
  predates the field (legacy provenance only, reported in the task doc);
- never used to bypass the per-call gate.

### 2. Role selection is subordinate to the contract

`workspace.DecideTransition` gains the task's `ExecutionContract` as an input.
The capability mapping stays:

```text
read           → ASK preferred; any role may read
execute        → FUZZ preferred; FUZZ/BUILD may execute
investigate    → FUZZ preferred; FUZZ/BUILD may investigate
mutate         → BUILD only
```

Disposition:

| Active | Capability | Contract | Decision |
| --- | --- | --- | --- |
| BUILD | investigate (strong/staged) | any | automatic FUZZ |
| FUZZ | mutate | `Mutate` | automatic BUILD |
| FUZZ | mutate | `!Mutate` | confirm BUILD |
| BUILD/FUZZ | read (strong) | any | confirm ASK |
| ASK | execute/investigate/mutate | any | confirm target |
| ASK | execute/investigate/mutate | any | confirm target (even if widened) |
| any | capability already performed | any | stay |

The ASK boundary is protected in both directions. A widened ASK-origin task is
still origin ASK; leaving ASK is still a human decision.

### 3. Human authority updates the contract

- An accepted widening transition widens the task contract to the approved
  target when `Begin` applies the approved handoff.
- An explicit `/ask`, `/fuzz`, or `/build` records an applied switch; a switch
  to a stronger role widens the contract because the human chose it.
- A narrowing move never removes authority from the contract. It changes the
  live role only, which is what makes `origin=BUILD, current=ASK` meaningful.

### 4. Master-task orchestration

The existing loop already supports internal phase changes through
`workspace.ToolCallTransition` before each call. That is the master-task
orchestration path: a BUILD task can file a finding (auto FUZZ), reproduce,
then mutate (auto BUILD) and verify inside one task. No second loop is added.
A subtask is a role phase / todo of the master task, inherits the live gate, and
cannot exceed the parent contract.

### 5. Reasoning demand

`control/cognition` owns a deterministic demand assessment:

```text
MINIMAL | MODERATE | HIGH
```

Signals are structural, not role-based:

- multiple targets or expected changes;
- verification requirements;
- conditional/staged objectives;
- investigation or execution capability;
- grounding breadth (actions/entities);
- ambiguity and assumptions;
- open task work (todos, hypotheses, questions);
- long multi-clause objectives.

Budget and context clamp demand down:

- one remaining turn or model call → minimal;
- at most three remaining → at most moderate;
- context pressure near the window → one level lower, or minimal at the limit.

Provider mapping uses only `ai.Model` facts:

```text
!Reasoning                 → omit
MINIMAL + ReasoningDisable → send the native disable value
MINIMAL + ladder           → lowest rung
MODERATE                   → middle rung
HIGH                       → highest rung
ladder empty / unknown     → omit reasoning configuration
```

No universal `off/low/medium/high/xhigh` is assumed. Unknown capability never
causes an invented value.

### 6. Reasoning is independent of role

The selected reasoning setting is applied per turn at the composition boundary
(`cmd/ohgo`) through the existing `agent.Loop.SetReasoning`. It is not changed by
an automatic role handoff, because that would make effort a function of role.

An explicit per-role effort or `--effort` remains a human override. Explicit
`off` remains a disable.

## Authority Model

```text
Origin        immutable: where the master task was authorized
Contract      persisted: origin + execute + mutate
Live role     ephemeral authority boundary for this phase
Gate          enforcement: per-call operation admission
```

- The model may propose an operation; it cannot grant authority.
- The UI renders decisions; it cannot grant authority.
- The task records authority; it cannot enforce it.
- Only `harness.SwitchWorkspace` moves the live gate.
- Only the security boundary admits a call.

## Role Transition Rules

```text
BUILD ↔ FUZZ    automatic when the contract holds the target capability
FUZZ → BUILD    confirm when the contract does not hold mutation
BUILD/FUZZ→ASK  confirm
ASK → any       confirm
```

A transition is committed before the model runs under the target role. The
existing TUI decision card is the confirmation surface; no second permission
framework is introduced.

## AUTO Routing

Routing consumes, in order:

1. the persisted task contract (when a task is open);
2. structured request capability (`AnalyzeRequest`);
3. evidence strength from the classifier;
4. the live role's existing capability;
5. tool-call phase evidence (`ToolCallTransition`) during execution.

No keyword table maps words to roles. Ambiguity resolves downward (READ).

## AUTO Reasoning

```text
intent contract + task state + capability
        ↓
cognition.AssessReasoningDemand
        ↓
budget/context clamp
        ↓
cognition.ReasoningSetting(model, demand)
        ↓
valid native level or omission
        ↓
agent.Loop.SetReasoning before the turn
```

The selected effort is applied once per turn. Role handoffs keep it.

## Task Orchestration

A master objective is one task with execution phases, not a sequence of new
tasks. The existing `agent.Loop.Run` is the only orchestration loop:

```text
request capability + task contract
        ↓
workspace.DecideTransition        choose the phase (or stay)
        ↓
harness.SwitchWorkspace           move the one live gate
        ↓
agent loop                        ground → call tool → observe → repeat
        ↓
workspace.ToolCallTransition      change phase when the call itself does
        ↓
existing completion/evidence      decide the verdict
```

A BUILD-authorized task may enter FUZZ on a strong investigation and return to
BUILD on a mutating call without human confirmation. The contract does not
change. A subtask inherits the live gate and cannot exceed the parent contract;
the existing subagent budget and gate tests remain the evidence. There is no
second planner, router, or work queue.

## Provider Capability Constraints

- `ai.Model.Reasoning` false ⇒ send nothing.
- No published ladder ⇒ send nothing; do not invent `medium`.
- A model with a mandatory reasoning policy is never claimed disabled.
- The catalog remains the only source of model facts; no second registry.

## Integration Points

| Change | File / owner |
| --- | --- |
| `ExecutionContract` | `internal/control/workspace/contract.go` |
| Transition policy input | `internal/control/workspace/decision.go` |
| Durable task field | `internal/continuity/task/task.go` |
| Checkpoint field / migration | `internal/continuity/task/progress.go` |
| New task / approval widening | `internal/continuity/task/controller.go`, `handoff.go` |
| Reasoning demand and mapping | `internal/control/cognition/reasoning.go` |
| Host composition | `cmd/ohgo/reasoning.go`, `options.go`, `harness.go`, `run.go`, `repl.go`, `turn.go`, `workspacehooks.go`, `wizard.go` |
| Turn application in the full-screen UI | `internal/tui/app.go` (`SetReasoningSelector`) |
| Transport omission | `internal/provider/ai/openai.go`, `anthropic.go` |
| Docs | architecture map, workspace authority, task continuity, runtime architecture |

## Implementation Plan

1. Add `workspace.ExecutionContract` and make `DecideTransition` consume it.
2. Persist and clone the contract on `task.Task`; capture origin on creation;
   widen only on human-authorized apply.
3. Thread the contract through TUI prediction, approval re-derivation, REPL,
   one-shot, and tests.
4. Add `cognition` reasoning demand and native selection with unit tests.
5. Apply AUTO reasoning per turn in the composition root; preserve it across
   role handoffs; keep explicit effort as override.
6. Stop transports inventing an empty effort.
7. Update canonical documentation and this task file.

## Invariants

1. AUTO never grants authority.
2. The current role never defines the task's authority.
3. Task origin is persistent and immutable.
4. Capability flags are persistent and only widen through human authorization.
5. A BUILD-authorized task may use BUILD ↔ FUZZ freely.
6. An ASK-origin task cannot silently acquire execution authority.
7. BUILD ↔ ASK is a protected boundary in both directions.
8. FUZZ cannot self-grant mutation authority.
9. Human confirmation precedes protected execution.
10. Reasoning effort is provider/model-native.
11. Unsupported reasoning configuration is omitted.
12. Unknown capability never invents a provider value.
13. One responsibility has one owner.
14. No parallel orchestration loop or authority path.
15. No dead abstraction remains.
16. Completion requires evidence, not model confidence.
17. Subtasks cannot exceed parent authority.
18. Authority state survives checkpoint and resume.
19. AUTO chooses the minimum sufficient authority.
20. AUTO chooses the minimum sufficient reasoning.

## Benchmark Plan

- A deterministic matrix at the host seam (routing, boundaries, master loop,
  session grant, reasoning demand, budget clamp, provider mapping), repeated at
  least three times.
- Adversarial cases for each protected boundary, asserting zero provider calls
  and zero tool executions on refusal.
- Live acceptance on the external project with a read-only ASK matrix and an
  isolated mutation copy, recording model calls, tool calls, wall time,
  authority lines and the terminal verdict.
- Root-cause analysis for every miss, at the owning subsystem, followed by a
  regression test and a rerun.

## Acceptance Tests

The real-project matrix is: understanding, recommendation, architecture
analysis, ASK execution boundary, BUILD mutation, failure recovery, and resume.
Tests 1–4 run against the checkout read-only; mutation runs only in an isolated
copy. The acceptance checkout's Git history is never touched. Results are in
*Real Project Acceptance Results*.

## Failure Scenarios

The deterministic suite exercises: ASK attempting execute/mutate, FUZZ
attempting mutation without a grant, BUILD leaving for ASK without confirmation,
a new objective inheriting an unrelated grant, an unsupported provider
reasoning value, an unknown reasoning ladder, budget/context exhaustion, a
transient provider failure, a rejected request, and a model claiming completion
without evidence. Live runs additionally exercised provider rate limits,
provider-side tool-call rejection, a weak model's repeated wrong-directory
commands, and a prose-only verification claim.

## Verification Plan

- `workspace`: policy matrix with each origin/contract combination; ASK and
  FUZZ safety; BUILD↔FUZZ automatic only when authorized.
- `cognition`: demand independence from role; budget/context clamping; native
  ladder mapping; omission for unsupported/unknown; mandatory reasoning.
- `task`: origin capture, clone, persistence, load fallback, widening on
  approval and explicit switch, subtask inheritance (live gate).
- `cmd/ohgo`: widening from ASK is a handoff; FUZZ-origin mutation confirms;
  BUILD task keeps one identity across automatic phases; AUTO reasoning applied
  per turn and preserved across a role switch; explicit effort overrides.
- `tui`: protected transition re-derivation uses the task contract; an ASK
  continuation still cannot execute.
- `provider/ai`: empty effort sends no reasoning parameter.
- Full repository gates from `AGENTS.md`.

## Documentation Updates

- `docs/architecture/OHGO_ARCHITECTURE_MAP.md` — state table and placement.
- `docs/architecture/WORKSPACE_AUTHORITY.md` — origin/contract/role model and
  the updated decision matrix.
- `docs/architecture/TASK_CONTINUITY.md` — durable authority ownership.
- `docs/architecture/ARCHITECTURE.md` — per-turn reasoning selection.
- This task file — implementation result and evidence.

## Completion Criteria

- Existing architecture is extended, not duplicated.
- No second authority path, task state, role router, or orchestration loop.
- All invariants above are enforced by tests at the owning boundary.
- Full gates pass: build, vet, gofmt, test, race, golangci-lint.
- Canonical docs and this task file match the implementation.

## Known Non-Goals

- An LLM intent judge.
- Per-model role rules.
- A universal reasoning enum.
- A new subtask engine or planner.
- Automatic escalation across ASK.
- Replacing the existing TUI approval surface.
- Persisting a second snapshot of the session transcript.

---

## Implementation Result

The capability was implemented by extending the existing owners, not by adding
a parallel subsystem.

- `workspace.ExecutionContract` is the persistent authority evidence:
  immutable origin plus execution/mutation grants. `DecideTransition` now takes
  it as an input; `ToolCallTransition` does the same for a concrete call.
- `task.Task.ExecutionContract` is captured when a task is created, cloned,
  persisted in `progress.json` (schema version 4), projected in `task.md`, and
  restored on resume. An approved handoff or an explicit human workspace
  command widens it; narrowing changes only the live role.
- The TUI prediction seam carries whether the open task owns the prompt, so a
  new objective is routed from the live role instead of inheriting another
  task's grant.
- `cognition.AssessReasoningDemand`, `ClampReasoningDemand`, and
  `ReasoningSetting` own demand and native mapping. The composition root applies
  the result per turn; a role handoff preserves it. `provider/ai` no longer
  invents `medium` when no effort is published.
- No new package, task store, policy engine, role router, permission surface, or
  orchestration loop was added.

## Architecture Changes

```text
task origin + human grants        task.ExecutionContract (progress.json)
        ↓                         read-only evidence
workspace.DecideTransition        stay | automatic | confirm
        ↓                         only BUILD <-> FUZZ automatic, and only when
        ↓                         the contract holds the target capability
harness.SwitchWorkspace           the one authority move
        ↓
workspace.Admit / security gate   the one per-call admission path

work structure + task state       cognition.AssessReasoningDemand
        ↓                         MINIMAL | MODERATE | HIGH (internal)
budget + context clamp            cognition.ClampReasoningDemand
        ↓
model's native ladder             cognition.ReasoningSetting
        ↓                         valid value or omission
agent.Loop.SetReasoning           applied per turn, preserved across role moves
```

## Files Changed

Core authority:

- `internal/control/workspace/execution_contract.go` (new)
- `internal/control/workspace/decision.go`
- `internal/continuity/task/task.go`
- `internal/continuity/task/progress.go`
- `internal/continuity/task/controller.go`
- `internal/continuity/task/handoff.go`
- `internal/continuity/task/archive.go`
- `internal/continuity/task/markdown.go`

Reasoning:

- `internal/control/cognition/reasoning.go` (new)
- `cmd/ohgo/reasoning.go` (new)
- `cmd/ohgo/options.go`, `harness.go`, `run.go`, `repl.go`, `turn.go`,
  `workspacehooks.go`, `wizard.go`
- `internal/tui/reasoning.go` (new), `internal/tui/app.go`
- `internal/provider/ai/openai.go`, `internal/provider/ai/anthropic.go`

Benchmark and hardening:

- `internal/agent/retry.go` (new), `internal/agent/retry_test.go` (new),
  `internal/agent/loop.go`
- `cmd/ohgo/harness.go`, `cmd/ohgo/repl.go`, `cmd/ohgo/run.go`,
  `cmd/ohgo/workspacehooks.go` (stateless session authority)
- `cmd/ohgo/auto_benchmark_test.go` (new deterministic benchmark)
- `internal/execution/tools/bash.go`, `structure.go`, `symbols.go`, `refs.go`
- `internal/execution/tools/path_selector_schema_test.go` (new)

Documentation:

- `docs/architecture/OHGO_ARCHITECTURE_MAP.md`
- `docs/architecture/WORKSPACE_AUTHORITY.md`
- `docs/architecture/ARCHITECTURE.md`
- `docs/architecture/TASK_CONTINUITY.md`
- `docs/architecture/STATE_AND_PERSISTENCE.md`
- `docs/architecture/TASK_PHASE_CONTINUITY.md`
- this task file

## Tests

New invariant tests:

- `workspace`: origin derivation, authorize-only-widens, mutation implies
  execution, widened ASK still confirms, FUZZ mutation requires the task grant,
  tool-boundary contract behavior.
- `cognition`: demand independence from role, budget/context clamping, native
  ladder mapping, mandatory reasoning, disable value, unknown ladder/demand
  omission.
- `task`: origin survives a role change and resume; approved widening keeps the
  ASK origin; explicit narrowing preserves the grant; an explicit FUZZ choice
  does not grant mutation; a legacy checkpoint derives a conservative contract.
- `cmd/ohgo`: AUTO selects low/high by work, respects the run budget, keeps
  explicit overrides, omits unknown capability; a standalone FUZZ mutation asks
  while a BUILD-authorized task hands off automatically; a new objective does
  not inherit the open task's grant.
- `tui`: the reasoning selector is applied before the turn and preserves manual
  configuration when absent.
- `provider/ai`: an unspecified effort is omitted on OpenAI and Anthropic; a
  known native level still travels.
- `agent`: a transient status/timeout is retried without extra turn spend; a
  request rejection is not; a backoff that would cross the deadline is not
  started; cancellation ends the retry.
- `execution/tools`: the empty `path` selector is valid for `structure`,
  `symbols`, and `refs`; `bash` runs in a contained `cwd` and refuses an escape.
- `cmd/ohgo/auto_benchmark_test.go`: the 66-row routing matrix, ASK/FUZZ
  boundary admission, the BUILD→FUZZ→BUILD master loop, the stateless session
  grant, the reasoning demand/budget/provider matrix.

Existing behavior that intentionally changed:

- `cmd/ohgo.TestFuzzMutationNeedsBuildAuthority` replaces the former automatic
  FUZZ → BUILD expectation for a standalone FUZZ session. The automatic path is
  now covered by `TestTurnTransitionUsesTaskMutationAuthority` with a
  BUILD-authorized task contract.

Diagnostic observation during implementation: the new tests failed against the
old behavior (the standalone FUZZ handoff, the implicit `medium` fallback, and a
simple rename scoring as moderate), which is the regression evidence that they
are not vacuous. The benchmark-driven fixes were verified the same way: the
session-grant test fails without `harness.activeContract`, and the schema test
fails while `path` carries `minLength` despite the implementation accepting the
empty form.

## Real-Model Validation

Environment: Groq `openai/gpt-oss-120b` through the bundled transport, isolated
temporary `HOME` and project, no repository state touched.

| Flow | Command intent | Observed |
| --- | --- | --- |
| ASK informational | `--workspace ask "What is Go?..."` | completed with a one-sentence answer; no authority move |
| ASK mutation | `--workspace ask "Fix the bug..."` | refused before the model: `this request needs BUILD authority` |
| BUILD informational | `--workspace build "What is Go?..."` | refused before the model: `this request needs ASK authority` |
| BUILD master task | `--workspace build "Fix add.go and run the tests"` | reached tool execution (`glob`) but the provider's 8000 TPM limit interrupted the run; the run reported `FAILED` rather than false completion and left the file unchanged |

The Groq `openai/gpt-oss-120b` row in this environment exposed no bundled
reasoning-effort ladder. AUTO therefore omitted reasoning configuration rather
than inventing `medium`, which is the required behavior for unknown capability.

The BUILD master-task full loop (read → investigate → mutate → verify) could
not complete on this Groq tier because each request exceeds the remaining
tokens-per-minute budget. The deterministic runtime tests cover the same path
with scripted providers, including the tool-boundary BUILD ↔ FUZZ round trip.
No failure was converted into a successful completion.

## Evidence

Repository gates run in `/Users/anvndev/Documents/Project/OpenSource/PizenLabs/ohgo`:

```text
go build ./...                 pass
go vet ./...                   pass
gofmt -l .                     empty
go test ./...                  pass
go test -count=1 ./...         pass (58 packages, fresh)
go test -race ./...            pass (no data races)
golangci-lint run ./...        0 issues
```

The architecture manifest test (`internal/architecture`) passes with no new
package. The existing subagent authority tests (`internal/agent` and
`cmd/ohgo`) continue to prove that a delegated child inherits the parent's gate
and cannot mutate through inherited FUZZ authority.

## Benchmark Suite

The deterministic benchmark lives in `cmd/ohgo/auto_benchmark_test.go` and runs
as an ordinary repository test:

```text
go test ./cmd/ohgo -run TestAuto -count=3
```

It covers:

| Benchmark | What it executes | Invariant |
| --- | --- | --- |
| `TestAutoRoutingBenchmarkMatrix` | 66 role/prompt rows (information, deep analysis, recommendation, execution, mutation, investigation, ambiguous wording, the master objective) through the host prediction seam | the capability decides the disposition; ASK is protected in both directions; BUILD↔FUZZ is automatic only when the contract covers the target |
| `TestAutoAskExecutionBoundaryNeverReachesTheModel` | an ASK session asking for a benchmark | refusal before the provider and before any command |
| `TestAutoFuzzMutationBoundaryNeedsTheTaskGrant` | a standalone FUZZ write and an authorized FUZZ write | no self-granted mutation; automatic BUILD only with the grant |
| `TestAutoMasterTaskOrchestratesBuildFuzzBuild` | one task: finding → reproduction → write → verification → answer | phases move BUILD→FUZZ→BUILD; one task contract; every tool runs under the phase authority |
| `TestAutoSessionKeepsItsGrantAcrossAutomaticPhases` | a BUILD REPL turn that investigates, then a turn that fixes | leaving BUILD for FUZZ does not lose the mutation grant |
| `TestAutoReasoningBenchmarkDemandLevels` | rename, request trace, diagnosis with competing evidence | demand follows structure, not role, and rises with evidence |
| `TestAutoReasoningBenchmarkBudgetClamp` | one call, one turn, three calls, 95% and 80% context | demand is lowered, never maximized |
| `TestAutoReasoningBenchmarkProviderCapability` | full ladder, limited ladder, disable value, no capability, unknown ladder, mandatory reasoning | a published native value or omission; never an invented level |

## Benchmark Results

Run on 2026-11-17 in the repository:

```text
go test ./cmd/ohgo -run TestAuto -count=3        pass (3 repetitions)
go test ./internal/control/workspace              pass
go test ./internal/control/cognition              pass
go test ./internal/continuity/task                pass
go test ./internal/agent                          pass
go test ./internal/execution/tools                pass
```

Observed:

| Scenario | Expected | Actual | Result |
| --- | --- | --- | --- |
| ASK information | ASK, no execution | stay ASK, 0 provider calls on a refused boundary | pass |
| ASK deep analysis | ASK | stay ASK | pass |
| recommendation | ASK | stay ASK | pass |
| ASK → execution | confirmation, no silent run | confirmation; 0 model calls, 0 commands | pass |
| ASK → BUILD | confirmation | confirmation (protected handoff) | pass |
| BUILD → ASK | confirmation | confirmation in both ASK directions | pass |
| BUILD → FUZZ | automatic | automatic on a strong investigation | pass |
| FUZZ → BUILD | automatic when authorized | automatic with a BUILD contract; confirmation without it | pass |
| FUZZ mutation without authority | blocked | blocked at the tool boundary | pass |
| master task | BUILD→FUZZ→BUILD→verify | `[FUZZ BUILD]`, 5 model turns, 4 tool executions, one contract | pass |
| new objective | no authority inheritance | live role contract only | pass |
| resume | origin and grant preserved | checkpoint/reload tests preserve both | pass |
| reasoning minimal/high | native or omitted | lowest/highest published rung; unknown omitted | pass |
| unsupported reasoning | omitted | omitted | pass |
| provider failure | truthful failure | retried only when transient; otherwise FAILED | pass |

The matrix was also run against the pre-fix behavior during development: the
session-grant test failed before `harness.activeContract`, and the live provider
rejected the empty `structure` path before the schema alignment. Both are
regression evidence, not vacuous assertions.

## Real Project Acceptance Results

Environment: the real acceptance project
`/Users/anvndev/Documents/Project/Golang/ecommerce`, read-only for ASK tests and
an isolated copy for mutation. Providers: Groq `openai/gpt-oss-120b` and
`gpt-oss-20b` (8,000 TPM tier), and local Ollama `qwen3:8b`. Sessions used an
isolated `HOME`.

| Test | Prompt / intent | Observed | Verdict |
| --- | --- | --- | --- |
| 1. Understanding | "Check this project and analyze what this project is about." | ASK stayed ASK; 10 model calls, 9 tool calls, 237 s; answer grounded in read source | `COMPLETED` |
| 2. Recommendation | "Suggest technologies that could complete this project." | ASK stayed ASK; no mutation; 6 model calls, 5 tool calls; answer overclaimed two dependency behaviours | `NEEDS_REVIEW` (truthful) |
| 3. Architecture | "Analyze the architecture and identify the most important areas to improve." | ASK stayed ASK; no mutation; provider rejected the 7th request at 8,967 tokens against the 8,000 TPM tier | `FAILED` (truthful) |
| 4. ASK execution boundary | "Run a benchmark and compare the performance." | refused before the model: `this request needs BUILD authority` | pass |
| 4. FUZZ mutation boundary | `--workspace fuzz "Fix the failing test."` | refused before the model: `this request needs BUILD authority` | pass |
| 4. BUILD informational boundary | `--workspace build "What is dependency injection?"` | refused before the model: `this request needs ASK authority` | pass |
| 5. BUILD mutation | isolated copy with a seeded `Complete` guard defect; "Investigate why TestComplete_HappyPath fails…" | automatic `BUILD -> FUZZ`; the local investigation model produced no evidence and did not mutate | `NEEDS_REVIEW` |
| 5. BUILD mutation (direct) | seeded defect; edit + test instruction | the `edit` succeeded, `cd backend && go test ./repricing/...` passed all packages, 8 model calls / 13 tool calls | `NEEDS_REVIEW` (scope label mismatch: required "repricing", verified "backend/repricing") |
| 6. Failure recovery | a local model claimed "all tests passed" in prose after an edit, without calling a command | `NEEDS_REVIEW: the repository change or execution was not followed by a successful verification`; the claim was not promoted to evidence | truthful, false completion prevented |
| 6. Failure recovery | repeated provider generation faults | invalid tool arguments and an unparsable generation ended as `FAILED`; nothing was changed | truthful, not a pass |
| 7. Resume | durable task checkpoint/reload | deterministic checkpoint, reload, role-change and grant-preservation tests | pass (deterministic) |

The ASK runs did not execute a command or change a file. The BUILD copy was the
only mutated target; the acceptance checkout and its Git history were untouched.

## Fixes Applied

Four root-cause fixes came out of the benchmark and live acceptance:

1. transient transport retry at the agent's model boundary;
2. tool schemas aligned with the empty-selector behaviour they already accept;
3. a contained `cwd` for shell commands;
4. one session contract for stateless hosts, preserved across automatic phases.

The detailed mapping is in *Failure Analysis*.

## Failure Analysis

| Observed failure | Root-cause category | Fix |
| --- | --- | --- |
| Groq returned HTTP 429 after a few requests | provider / transport | `agent.RetryPolicy`: bounded deadline-aware retry for transient statuses and timeouts; request errors are not retried |
| Groq rejected `structure` because `path` was `""` | tool contract | the schema forbade a value the implementation already treats as "whole project"; `structure`, `symbols`, and `refs` now accept the empty selector |
| The model passed `cwd` to `bash`, which silently ignored it and re-ran from the repository root | tool contract | `bash` now supports a root-contained `cwd`; invalid directories and escapes are refused |
| A BUILD session that moved to FUZZ lost its mutation grant on the next turn | authority / continuity | stateless hosts keep an in-memory session contract captured from the starting role; automatic phases never narrow it |
| A provider-side generated tool call that failed the provider's schema ended the run | provider / generation | documented; retrying a deterministic generation fault would spend a request without new evidence, and the run reports `FAILED` |

The first four were fixed at their owner, not at the benchmark prompt. No
prompt-specific branch was added.

## Performance / Efficiency Findings

- ASK understanding used 9 tool calls, including one duplicate `ls`; the loop's
  read-only replay guard caught the final duplicate and told the model to reuse
  the earlier payload.
- A weak local model repeated a failing `go test` from the wrong directory
  before `cwd` existed; after the fix, an explicit subdirectory ran in place.
- No unnecessary ASK↔BUILD or BUILD→FUZZ transition was observed in the live
  runs; an ASK request never escalated just because analysis was difficult.
- A refused boundary spends zero provider requests and zero tool calls.
- Token usage is not persisted in the session; live metrics therefore record
  model calls, tool calls, wall time, authority lines, and the terminal verdict.
  The deterministic benchmark asserts the exact turn and tool counts.

## Final Regression Results

After all fixes, on the tested working tree:

```text
go build ./...                     pass
go vet ./...                       pass
gofmt -l .                         empty
go test -count=1 ./...             pass (58 packages)
go test -race ./...                pass (no data races)
golangci-lint run ./...            0 issues
go test ./cmd/ohgo -run TestAuto -count=3   pass
```

## Remaining Limitations

- A continuous live BUILD → FUZZ → BUILD master loop was not completed. The
  deterministic loop test exercises the exact authority path, but the available
  live providers could not: Groq's 8,000 TPM tier rejects realistic
  single-request contexts with HTTP 413, and the installed 8B local models do
  not follow the repository tool protocol reliably.
- Local models with 8B parameters produced invalid provider-side tool calls and
  prose descriptions of tools (`qwen2.5-coder`, `llama3.1`, `command-r7b`); those
  runs correctly ended `FAILED`/`NEEDS_REVIEW`.
- Completion scope matching is literal. A request whose requirement names
  "repricing" while the verified command covers "backend/repricing" reports
  review rather than success. The conservative direction is intentional; a
  unique-suffix resolver would be a separate target-resolution feature.
- A provider generation fault is not retried. This is deliberate: the request
  itself is not the failure, but a blind retry cannot be distinguished from a
  deterministic incompatibility without an invented heuristic.
- The reasoning demand is structural. A terse but architecturally broad request
  can start at minimal demand and rise only as the task gains targets,
  hypotheses, or verification requirements.
- Legacy checkpoints (schema 1–3) derive an origin from the restored role
  because the original grant was never stored. It is documented as legacy
  provenance, never as an original origin.
- The plain REPL is stateless: its session contract survives authority moves
  inside the process, but not a process restart. Durable resume belongs to the
  full-screen task controller.
- `--no-tools` still reports `agent: no tools registered`; this was observed
  while preparing a minimal live run and is outside the AUTO authority path.

## Certification Status

```text
IMPLEMENTED_AND_VERIFIED   yes
BENCHMARK_VERIFIED         yes (deterministic matrix, adversarial boundaries, repetitions)
ACCEPTANCE_VALIDATED       no  (partially validated on the real project)
CERTIFIED_FOR_REAL_PROJECT_USE   no
```

Evidence supports the first two. Live ASK routing, protected boundaries, and
BUILD mutation/verification are validated, but a full live master-task loop and
repeated real-provider stability runs are not. Claiming certification would
exceed the evidence.
