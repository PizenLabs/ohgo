# Workspace Transition & Routing — Architecture Plan

> **Status:** Phase 0 investigation complete; Phase 1 implemented and validated.
> **Scope:** ASK / FUZZ / BUILD request admission, workspace transition policy,
> TUI decision surfaces, conditional objectives, task continuity, and completion.
> **Canonical topology:** [`architecture/OHGO_ARCHITECTURE_MAP.md`](architecture/OHGO_ARCHITECTURE_MAP.md).
> **Permanent rules:** [`AGENTS.md`](../AGENTS.md) and the architecture
> constraints enforced by `internal/architecture`.
> **Related:** [`architecture/WORKSPACE_AUTHORITY.md`](architecture/WORKSPACE_AUTHORITY.md),
> [`architecture/TASK_CONTINUITY.md`](architecture/TASK_CONTINUITY.md),
> [`architecture/TASK_PHASE_CONTINUITY.md`](architecture/TASK_PHASE_CONTINUITY.md),
> [`architecture/INTENT_GROUNDING.md`](architecture/INTENT_GROUNDING.md).

### Phase 1 implementation update

Implementation confirmed the plan's central diagnosis and changed two details:

- `ProposeTransition` and `ProposeHandoff` were deleted after
  `workspace.DecideTransition` replaced them. Keeping both would have left two
  transition policies; the plan's single-policy requirement takes precedence
  over preserving the old proposal helpers.
- Ambiguity does not by itself keep ASK in place. If ASK cannot perform the
  required capability, an ambiguous investigation or execution still asks the
  human for the needed workspace. Ambiguity only stays when the current
  workspace already holds the capability.

The final implementation is described in
[`architecture/WORKSPACE_AUTHORITY.md`](architecture/WORKSPACE_AUTHORITY.md).

Notation used below:

- **Observed** — read from the current tree, tests, or a executed probe.
- **Inference** — a conclusion drawn from observed code, not directly asserted
  by a test or a runtime observation.
- **Proposed design** — the change this plan asks Phase 1 to implement.

---

## 1. Current architecture

### 1.1 Workspace representation (Observed)

- `internal/control/workspace.Workspace` is a string enum with exactly three
  values: `ask`, `fuzz`, `build`.
- `Workspace.Permits(Operation)` and `Workspace.Admit(...)` own the capability
  decision. Mutation (`OpWrite`, `OpPatch`, `OpDelete`, `OpMove`) is permitted
  only under BUILD. Execution is permitted under FUZZ and BUILD. All three may
  read/search/inspect.
- `Workspace.Encompasses` orders the authorities `ASK ⊂ FUZZ ⊂ BUILD`.
- `cmd/ohgo/harness.SwitchWorkspace` is the single authority switch owner. It
  moves the runtime gate, resolves the workspace's role model, updates the
  session snapshot, and lets the caller publish chrome.
- `cmd/ohgo/authorityGate` is the only mutable live authority read by the agent
  loop and subagents at call time.
- The persistent session records `Workspace` (`internal/continuity/session`).
- `DefaultWorkspace` is BUILD for pre-workspace sessions.

### 1.2 Request admission (Observed)

Full-screen path:

```text
composer submit
  -> tui.Program.submit
  -> optional tui.Program.workspace.Predict(text)       (policy projection)
  -> TransitionProposed / ApprovalAsked / PermissionsOpened when predicted
  -> tui.Program.startTurn -> runTurnWithChoice
  -> workspace.PrepareTurn(text) (CLI projection of RouteTurn)
  -> agent.Loop.Run
  -> authorityGate / Authority.Admit on every tool call
```

The plain REPL and headless path call `harness.RouteTurn` directly; they do not
use the TUI prediction surface.

### 1.3 Workspace flow and current transition policy (Observed)

`workspace.ClassifyPrediction` maps request text to `READ`, `EXECUTE`, or
`INVESTIGATE` plus an evidence level (`ambiguous`, `moderate`, `strong`).

Two separate policy objects exist:

- `workspace.ProposeTransition` proposes only safe narrowings on `strong`
  evidence (BUILD → ASK, BUILD → FUZZ, FUZZ → ASK).
- `workspace.ProposeHandoff` proposes only widenings on `strong` evidence
  (ASK → BUILD, ASK → FUZZ, FUZZ → BUILD).

`internal/control/workspace.RouteTurn` is the authority guard. Today it refuses
whenever the intent handler is not exactly the active workspace:

```text
ASK   + READ          allowed
ASK   + EXECUTE       refused -> BUILD
ASK   + INVESTIGATE   refused -> FUZZ
BUILD + READ          refused -> ASK
BUILD + EXECUTE       allowed
BUILD + INVESTIGATE   refused -> FUZZ
FUZZ  + READ          refused -> ASK
FUZZ  + INVESTIGATE   allowed
FUZZ  + EXECUTE       refused -> BUILD
```

Two consequences are material:

1. **Every BUILD/FUZZ read request is a refusal or a prompt**, even when BUILD
   is perfectly capable of reading and the user only asked an exploratory
   question. This is the source of the repeated workspace modal.
2. **BUILD/FUZZ cooperation is presented as authority escalation**, not as
   capability complementarity. BUILD → FUZZ and FUZZ → BUILD both require the
   human through the proposal/handoff surface.

### 1.4 Picker flow (Observed)

The screenshot's modal is `PermissionsState` rendered by
`internal/tui/permissions.go`. It is opened by `Program.openAuthorityChoice`
with the title `Choose Workspace For This Request`.

The production trigger is in `cmd/ohgo/turn.go:turnTransition`:

```go
if prediction.Intent.Handler() != active &&
    (prediction.Strength == StrengthModerate ||
     prediction.Strength == StrengthAmbiguous && prediction.Competing != "" ||
     noSignalChoice) {
    return TurnTransition{Kind: TransitionKindAmbiguous, ...}, true
}
```

Therefore the picker is **request admission**, not a boundary confirmation. A
moderate READ prediction in BUILD, an ambiguous compound request, or a request
whose classifier does not match the current workspace all stop the turn and
force a workspace choice before any model call. The chosen workspace is applied
through `WorkspaceHooks.Switch` and the same request is started with
`startTurnWithChoice`.

A separate `TransitionPrompt` / `TransitionProposed` surface exists for explicit
narrowing (`ProposeTransition`) and widening (`ProposeHandoff`). That is the
correct human-boundary surface, but it is currently treated as one more
mandatory step rather than the exceptional boundary it should be.

### 1.5 Agent loop, capabilities, and models (Observed)

- `agent.Loop.execute` calls `Config.Authorize`; the production authorizer is
  the security gate backed by `workspace.Authority.Admit`.
- `admitFor(ws)` and the live `authorityGate` enforce capability at call time,
  not through prompts. FUZZ cannot write because `Workspace.Permits` refuses
  mutation.
- `agent.Config.Grounding` validates a model proposal against the user's
  request (`agent.ValidateProposal`). A mutation that the request did not ask
  for is rejected.
- `cmd/ohgo.SwitchWorkspace` resolves the target role's effective model from
  settings (role model, with global fallback) and calls `applyModel` plus
  `loop.SetReasoning`. Model specialization is therefore already a property of
  the workspace switch, not of the TUI.
- `cognition.BuildShape` derives the turn's action surface from the contract and
  the active authority. It narrows but never authorizes.

### 1.6 Existing transition/handoff abstraction (Observed)

`internal/continuity/task/handoff.go` already provides a durable, bounded
handoff record:

- `ProposeHandoff` records `PROPOSED` without moving authority.
- `ApproveHandoff` records `APPROVED`.
- `ApplyHandoff` / `applyApprovedHandoff` records `APPLIED` and changes the
  task's `ActiveRole`.
- `RecordWorkspaceSwitch` records an already-applied explicit human switch.
- `ReconcileRecoveredHandoff` reconciles approval vs. restored authority.
- Handoffs carry shared record IDs (facts, hypotheses, questions, evidence),
  artifacts, constraints, and role context by reference. They do **not** copy
  evidence or create a second task.
- `Task.Contract`, `Task.Snapshot`, `Task.Context`, `Task.Timeline`,
  `Task.Completion`, `Task.ReviewReason`, and the budget ledger live on the same
  task identity. `Controller.Begin` refuses to replace a non-terminal task's
  contract with a different one.

This is the correct continuity mechanism to extend. A second transition record
would be a bug.

### 1.7 Task continuity (Observed)

- A task is a durable checkpoint keyed by a stable ID; sessions are
  conversations that may outlive or be replaced independently.
- `taskTurnDecision` currently recognizes explicit resume, a pending handoff, a
  natural verification question about the task's own result, a role change on
  the same objective, and an exact objective repeat.
- A role change does not create a new task when the objective remains the same
  (`roleContinuation`), and an approved/applied handoff forces attachment of the
  next turn (`handoffContinuation`).
- `Controller.Begin` resumes the existing identity for a non-terminal task and
  keeps the original pre-run target snapshot.

### 1.8 Intent / objective classification (Observed)

- `ClassifyPrediction` is deterministic, clause-aware, and conservative. It
  strips politeness, splits clauses, and treats a question about an operation as
  READ rather than a request for the operation.
- `RequestsMutation` uses a narrower mutation vocabulary than routing EXECUTE:
  running tests is EXECUTE for routing but not a mutation objective.
- `BuildIntentContract` sets `ObjectiveMutation` whenever `RequestsMutation` is
  true anywhere in the request, then extracts targets, verification, and
  grounding.
- Conditional and future branches are **not** represented in the contract.
  A request beginning with investigation and later saying "If confirmed,
  implement the fix" still becomes `ObjectiveMutation`, because the conditional
  mutation clause matches `RequestsMutation`.

Probe result (Observed, temporary test against the current tree):

```text
prompt: "Check this Go e-commerce project and understand how the main
         order/checkout flow works. Start by investigating... If the issue
         is confirmed, switch... implement the fix... verify..."
prediction: investigate / ambiguous / competing execute
contract.Kind: objective mutation
contract.Targets: order/checkout
contract.Verification: test (any)
grounding.Action: mutate
grounding.Actions: read, investigate, mutate, execute
grounding.Constraints: no_mutation, no_creation
```

Two extraction bugs are visible in that probe:

- `"Do not create a new task"` becomes the `no_creation` repository constraint.
  It is a task-continuity instruction, not a repository limit.
- `"without changing anything"` / `"without modifying source code"` become a
  task-global `no_mutation` constraint. They are scoped to the investigation
  phase, but the contract cannot express that scope.

### 1.9 Completion (Observed)

`tui.turnTaskOutcome` projects the shared `agent.ClassifyRun` verdict into the
durable task lifecycle. `agent.EvaluateTaskCompletion` owns evidence rules:

- mutation objective + no observed mutation:
  `"the request asked for a repository change but no change was observed"`;
- observed mutation without a later successful verification:
  `"the repository change or execution was not followed by a successful verification"`;
- execution objective + no successful execution: review;
- informational objective + empty or deferred-plan answer: review;
- structural target and behavioural scope checks for mutation objectives;
- grounding and provenance vetoes.

The existing uncommitted work adds `workspace.PhaseContract`, which narrows a
task contract to an informational phase when ASK or FUZZ cannot perform the
persisted objective, and maps a successful phase to `taskActionPhaseComplete`
(keep the task ACTIVE without a proof). That correctly suppresses
`"no change was observed"` for FUZZ/ASK investigation phases. It does **not**
handle a conditional mutation objective that is already running under BUILD:
BUILD is allowed to perform the mutation contract, so `PhaseContract` returns it
unchanged and completion still demands a change even when the current phase is
investigation.

### 1.10 Existing tests (Observed)

Relevant suites:

- `internal/control/workspace/intent_test.go` — classifier, routing matrix,
  conditional imperative clauses, continuation/verification vocabulary.
- `internal/control/workspace/transition_test.go`, `handoff_test.go` — proposal
  policy, strong-evidence floor, no escalation.
- `internal/control/workspace/contract_test.go`, `phase_test.go` — target and
  verification extraction, phase narrowing.
- `internal/continuity/task/*_test.go` — handoff lifecycle, recovery, context,
  completion proof, timeline.
- `internal/tui/transition_test.go`, `permissions_test.go`,
  `continuation_transition_test.go`, `phase_completion_test.go`,
  `task_continuity_test.go` — TUI decision surfaces and runtime-level behavior.
- `cmd/ohgo/transition_test.go`, `turn_transition_test.go`, `handoff_test.go`,
  `readiness_process_test.go`, `phase3_continuity_test.go` — the production
  harness seam, real controller, real authority gate.
- `cmd/ohgo/integration_test.go`, `completion_integration_test.go` — scripted
  provider/tool round trips, not live model calls.

The repository has no live-provider test harness that can be run without
credentials. `docs/testing/TASK_CONTINUITY_TEST_CATALOG.md` records live
provider/model integration as `NOT TESTED (out of scope)`.

### 1.11 Uncommitted work in the tree (Observed)

The tree already contains uncommitted changes that form part of this fix:

- continuation vocabulary and verification-continuation detection
  (`workspace.IsContinuation`, `workspace.IsVerificationContinuation`);
- `workspace.PhaseContract` and `taskActionPhaseComplete`;
- TUI task-ownership resolution before prediction;
- terminal-task continuation refusal;
- new tests for continuation and phase completion.

Phase 1 must build on these changes, not discard them.

---

## 2. Current request flow

```text
user input
  -> tui.Program.submit
  -> proposalFor / terminalTaskContinuation / continuesOpenTask short-circuits
  -> workspace.Predict
       -> ClassifyPrediction
       -> ambiguous/moderate handler mismatch -> TransitionKindAmbiguous (picker)
       -> ProposeTransition (narrowing, strong) -> TransitionKindWorkspace
       -> ProposeHandoff (widening, strong) -> TransitionKindHandoff
       -> AssessProposal (commit/target decision) -> ApprovalAsked
  -> startTurnWithChoice / startTurn
  -> runTurnWithChoice
       -> taskTurnDecision (continuation / new task)
       -> PrepareTurn -> workspace.RouteTurn (exact-handler refusal)
       -> task.Begin (same identity or new)
       -> cognition.BuildShape
       -> loop.SetGrounding
       -> Loop.Run
       -> authorityGate on every tool
       -> turnTaskOutcomeForAuthority
       -> task Complete / PhaseComplete / NeedsReview / Pause / Fail
```

The exact picker trigger is the `TransitionKindAmbiguous` branch in
`cmd/ohgo/turn.go:turnTransition`, rendered by
`internal/tui/permissions.go:openAuthorityChoice`.

## 3. Current workspace flow

```text
workspace is a session property
  -> session.Workspace persisted
  -> harness.workspace label
  -> authorityGate (live)
  -> role model resolution on SwitchWorkspace
  -> task.ActiveRole (continuity metadata)

transitions:
  ASK -> BUILD   explicit / handoff approval
  ASK -> FUZZ    explicit / handoff approval
  BUILD -> FUZZ  proposal approval (currently)
  FUZZ -> BUILD  handoff approval (currently)
  BUILD -> ASK   proposal approval
  FUZZ -> ASK    proposal approval
```

## 4. Current picker flow

```text
user submits
  -> prediction has moderate evidence, or ambiguous with a competing signal,
     or no signal at all and the handler differs from the active workspace
  -> openAuthorityChoice
  -> modal blocks every turn
  -> human picks ASK / BUILD / FUZZ
  -> confirmPermissions -> WorkspaceHooks.Switch
  -> startTurnWithChoice under the chosen workspace
```

The picker is both **initial admission** and **transition authorization**. It
has no concept of a safe execution-workspace handoff, so BUILD ↔ FUZZ is
presented exactly like a human-boundary crossing.

## 5. Current intent / objective flow

```text
text
  -> normalizeRequest / stripPoliteness
  -> requestClauses
  -> ClassifyPrediction (READ / EXECUTE / INVESTIGATE + evidence)
  -> RequestsMutation (mutation objective for completion)
  -> BuildIntentContract (Kind, targets, verification, grounding)
  -> task.Begin persists the contract
  -> completion judges the entire persisted contract
```

Conditional future branches are flattened at `BuildIntentContract`:
`RequestsMutation` returns true for any non-question mutation clause, even if
that clause is the consequent of an `if` that has not been evaluated.

## 6. Current completion flow

```text
turns + contract + snapshot + resolver + evidence review + provenance
  -> agent.ClassifyRun
       -> deadline / cancelled / error / budget
       -> agent.EvaluateTaskCompletion
            -> run shape and unrecovered failures
            -> objective kind
                 mutation + no change            -> NEEDS_REVIEW
                 execution + no execution        -> NEEDS_REVIEW
                 informational + empty/deferred  -> NEEDS_REVIEW
            -> verification after last change
            -> structural target checks
            -> behavioural scope checks
            -> ambiguity
            -> grounding / provenance
  -> tui project verdict
       -> taskActionComplete / Review / Pause / Fail
       -> PhaseContract may project an informational phase (uncommitted work)
```

## 7. Exact root causes

1. **C1 — Admission picker is the default transition mechanism.** (Observed)
   `turnTransition` converts any moderate/ambiguous cross-handler prediction
   into `TransitionKindAmbiguous`, so the modal is normal request admission.
2. **C2 — The policy has no capability-based concept of "the current workspace
   can already do this."** (Observed)
   `ProposeTransition`/`ProposeHandoff` compare intent handlers, not required
   capabilities. BUILD can read and execute, but the policy treats a READ
   handler as a reason to leave BUILD.
3. **C3 — BUILD ↔ FUZZ cooperation is modelled as an authority approval.**
   (Observed)
   BUILD → FUZZ is a narrowing proposal on strong evidence; FUZZ → BUILD is a
   widening handoff. Both need a human answer, contrary to the required
   cooperative behavior.
4. **C4 — `RouteTurn` refuses capabilities the active workspace already holds.**
   (Observed)
   BUILD + READ and FUZZ + READ are refused to force a workspace move. This is
   why removing the picker alone would turn Scenario D into a hard refusal.
5. **C5 — Conditional mutation is flattened into the persisted objective.**
   (Observed)
   `BuildIntentContract` sets `Kind = ObjectiveMutation` and no phase/deferred
   marker survives. Completion therefore treats an investigation turn as a
   failed mutation.
6. **C6 — Phase narrowing only exists for lower authorities.** (Observed)
   `PhaseContract` defers a mutation under ASK/FUZZ, but not under BUILD, where
   the contract is performable. A conditional investigation under BUILD still
   demands a change.
7. **C7 — Constraints are task-global.** (Observed)
   Phase-scoped wording ("without changing anything", "without modifying source
   code") becomes a global `no_mutation`; task meta-instructions ("do not create
   a new task") become repository constraints.
8. **C8 — The control plane does not expose a first-class transition decision.**
   (Observed)
   The TUI owns the branching between ambiguous picker, transition proposal, and
   handoff. It renders policy vocabulary but also decides which policy to ask.
9. **C9 — Continuations can be reclassified as new requests.** (Observed;
   partially fixed by uncommitted work)
   `continue`, `verify the fix`, and natural verification wording are not
   capabilities; without task ownership before prediction they reach the
   classifier as READ fallback and open a workspace decision.

## 8. Why the current implementation produces the observed behavior

The screenshot prompt starts with `Check... understand...` and later contains
`investigate`, `reproduce`, and `implement the fix`. The classifier sees
investigation and execution evidence with neither dominant, returns
`investigate/ambiguous` with a competing `execute`, and `turnTransition` maps
that to `TransitionKindAmbiguous`. The modal appears before the turn. The request
never reaches the task layer, so no task identity or phase is established. If
the user selects BUILD, the completed investigation turn is judged against the
mutation contract and fails with `"the request asked for a repository change but
no change was observed"`. The runtime has no representation of "current phase =
investigation" for a contract that contains a conditional future mutation.

---

## 9. Desired architecture

The control plane owns a single **transition decision**:

```text
request text
  -> deterministic request analysis
       capability: READ | EXECUTE | INVESTIGATE | MUTATE
       staged conditional workflow?
       evidence strength
  -> transition policy
       stay
       automatic execution-workspace handoff (BUILD <-> FUZZ)
       human boundary confirmation (anything crossing ASK)
  -> the existing SwitchWorkspace owner applies an accepted/automatic move
  -> the same task continues
```

The task owns objective, contract, phase, evidence, timeline, and completion
separately from the current workspace.

```text
TASK
 |-- objective / contract / phase
 |-- shared context / evidence / hypotheses / questions
 |-- timeline / budget / completion state
 `-- current workspace (ASK | FUZZ | BUILD)
```

The policy is **capability-based**, not handler-equality-based:

- ASK can READ. It cannot EXECUTE, INVESTIGATE, or MUTATE.
- FUZZ can READ, EXECUTE, and INVESTIGATE. It cannot MUTATE.
- BUILD can READ, EXECUTE, INVESTIGATE, and MUTATE.

A preferred workspace still exists for intent specialization:
READ prefers ASK, INVESTIGATE prefers FUZZ, MUTATE prefers BUILD, and EXECUTE
prefers BUILD. Preference drives a transition only when it is safe and useful;
capability drives whether the turn may run at all.

## 10. Transition matrix

The required semantics below are the implementation contract.

| Current | Request requirement | Decision | Human confirmation |
| --- | --- | --- | --- |
| ASK | READ / explanation / analysis | stay ASK | no |
| ASK | ambiguity or no signal | stay ASK (read-only fallback) | no |
| ASK | EXECUTE (run) | confirm → BUILD | yes |
| ASK | MUTATE | confirm → BUILD | yes |
| ASK | INVESTIGATE (needs execution/probing) | confirm → FUZZ | yes |
| BUILD | MUTATE | stay BUILD | no |
| BUILD | EXECUTE | stay BUILD | no |
| BUILD | READ, ambiguous/moderate | stay BUILD (BUILD may inspect) | no |
| BUILD | READ, strong human-facing question | confirm → ASK | yes |
| BUILD | INVESTIGATE, strong / staged | auto → FUZZ | no |
| FUZZ | INVESTIGATE | stay FUZZ | no |
| FUZZ | EXECUTE that does not mutate | stay FUZZ | no |
| FUZZ | MUTATE | auto → BUILD | no |
| FUZZ | READ, ambiguous/moderate | stay FUZZ (may inspect) | no |
| FUZZ | READ, strong human-facing question | confirm → ASK | yes |
| ANY | explicit continuation of the open task | stay current; task owns the turn | no |
| ANY | conversational acknowledgement | stay current | no |

The two thresholds are deliberate:

- crossing **into ASK** requires `strong` READ evidence, so a vague "check this"
  does not interrupt a BUILD/FUZZ session;
- crossing **out of ASK** requires at least `moderate` evidence for a new
  capability, while an explicit mutation always qualifies.

## 11. Authority rules

1. `workspace.DecideTransition` is the single transition policy. It is pure,
   deterministic, and returns a decision object that names current workspace,
   target, requirement, reason, and whether human confirmation is required.
2. The TUI never decides whether a transition is allowed. It applies the
   decision through `WorkspaceHooks.Switch`/`SwitchWithReason` and renders the
   confirmation surface when the decision says confirmation is required.
3. Automatic transitions are limited to BUILD ↔ FUZZ. Anything crossing ASK
   requires confirmation.
4. Automatic transitions still go through `harness.SwitchWorkspace`, so the
   runtime gate, role model, session snapshot, and task handoff record move
   together.
5. FUZZ never gains mutation capability: the authority gate still denies every
   mutating tool call. An automatic FUZZ → BUILD switch must complete before a
   mutating turn starts.
6. The model may propose intent by its text; it cannot move authority. Only the
   control-plane decision plus the workspace owner can.
7. `RouteTurn` becomes a capability guard, not a handler-equality rule:
   - ASK: READ allowed; EXECUTE/MUTATE require BUILD; INVESTIGATE requires FUZZ.
   - FUZZ: READ/EXECUTE/INVESTIGATE allowed; mutation requires BUILD.
   - BUILD: everything allowed.
8. A rejected BUILD/FUZZ → ASK confirmation keeps the current workspace and
   runs the turn there when it is capable. A rejected ASK → BUILD/FUZZ
   confirmation cancels/refuses the request; it must never run with insufficient
   authority.
9. The picker (`Choose Workspace For This Request`) stops being request
   admission. It remains available for explicit `/permissions`/`/mode` and for
   any future genuinely unresolvable decision, but the normal path uses the
   typed confirmation surface or an automatic handoff.

## 12. Task-continuity rules

1. A transition never creates a new task. The same `Task.ID`, contract,
   snapshot, shared context, evidence, hypotheses, questions, timeline, budget,
   and completion state survive.
2. Automatic transitions are recorded on the existing handoff record as
   `APPLIED`, with `ApprovedBy` naming the control plane and a truthful reason.
   The next turn consumes the applied handoff, exactly like an explicit switch.
3. If a task does not exist yet, the automatic switch happens before `Begin`;
   the new task is created under the target workspace and from the first turn
   onward carries the target as `ActiveRole`.
4. A continuation owns the turn before prediction. `continue`, `verify the fix`,
   and natural verification wording must not be reclassified as a new READ
   request.
5. Role changes on the same objective remain continuations (`roleContinuation`),
   and an applied/approved handoff remains the explicit continuation decision
   for exactly the next turn.
6. Terminal tasks absorb no continuation; a repeated continuation reports the
   finished state and never starts a duplicate task.

## 13. Completion-contract rules

1. A contract carries an explicit `ConditionalMutation` marker when the request
   stages an investigation/read phase before a conditional future mutation.
2. `Kind` remains `ObjectiveMutation` so the mutation branch can be authorized
   under BUILD and so grounding still allows the conditional change. The marker
   prevents the *current* phase from being judged as an unconditional mutation.
3. Under ASK/FUZZ, `PhaseContract` continues to narrow a performable mutation to
   an informational phase when the authority cannot perform it.
4. Under BUILD, a conditional mutation with no observed mutation and no prior
   change is evaluated as the investigation phase: the answer must be
   substantive, but no repository change is required.
5. Once a mutation is observed (or a prior durable change exists), the normal
   mutation rules apply: targets, behavioural checks, a successful verification
   after the change, and grounding/provenance vetoes.
6. A conditional investigation phase that completes stays ACTIVE
   (`taskActionPhaseComplete`) and records no proof; the same task continues
   when BUILD performs the change. It is not `NEEDS_REVIEW` merely because the
   conditional branch was not reached.
7. A pure informational objective under any workspace still requires a
   substantive, non-deferred answer.
8. Phase-scoped constraints must not become task-global:
   - `"do not create a new task"` is task-continuity instruction, not
     `no_creation`;
   - `"without changing anything"` / `"without modifying source code"` in a
     staged conditional request are investigation-phase constraints, enforced by
     the phase's ASK/FUZZ authority and not carried as a global `no_mutation`
     veto on the later BUILD branch.
9. The completion evaluator remains the only owner of the verdict. The TUI only
   projects it into the task lifecycle.

## 14. Model-selection implications

- ASK, FUZZ, and BUILD keep independent role model configuration.
- Automatic and confirmed transitions both go through `SwitchWorkspace`, which
  already resolves the target role's effective model and reasoning effort.
- No generic "agent mode" is introduced. `IntentContract` does not select a
  model; it describes the objective.
- Tests must prove that a transition changes the effective role/mode selection
  rather than collapsing to one global model. The existing harness model
  resolution is the seam to assert.

## 15. TUI implications

1. `submit` asks the control plane for a `TurnTransition` decision.
2. `TransitionStay` starts the turn.
3. `TransitionAutomatic` calls the workspace switch owner, publishes a visible
   notice (`BUILD → FUZZ · investigation` / `FUZZ → BUILD · repository change`),
   and starts the turn under the target.
4. `TransitionConfirm` renders the existing typed transition/handoff dialog with
   the target and reason. Accepting calls `Switch`; rejecting either treats the
   request as cancelled (widening from ASK) or runs it under the capable current
   workspace (narrowing to ASK from BUILD/FUZZ).
5. `TransitionKindAmbiguous` is no longer produced by the production policy. The
   modal becomes an explicit `/permissions` surface, not an admission gate.
6. The transcript shows the transition; task continuity rail shows the same
   task and the new role.
7. Existing approval, proposal, and task surfaces are unchanged.

## 16. Files/packages likely to change

- `internal/control/workspace/decision.go` — the single `DecideTransition`
  policy, capability requirements, and automatic/confirm disposition.
- `internal/control/workspace/workflow.go` — staged conditional detection.
- `internal/control/workspace/intent.go` — capability-aware `RouteTurn` and
  task-follow-up vocabulary. `transition.go` and `handoff.go` were removed:
  their proposal policies were replaced by `decision.go`.
- `internal/control/workspace/contract.go` — `ConditionalMutation` and
  conditional phase handling.
- `internal/control/workspace/grounding.go` — conditional mutation grounding and
  phase-scoped constraint filtering.
- `internal/control/contract/contract.go` — the persisted contract field and
  `InformationalPhase`.
- `internal/agent/completion.go` — conditional phase completion and mutation
  branch detection.
- `internal/tui/app.go` — automatic transition application and reject behavior.
- `internal/tui/model.go`, `task.go` — decision projection and continuation
  ownership.
- `cmd/ohgo/turn.go` — project `DecideTransition` into `TurnTransition`.
- `cmd/ohgo/workspacehooks.go` — one shared explicit/automatic switch seam.
- `cmd/ohgo/run.go`, `cmd/ohgo/wizard.go`, `cmd/ohgo/repl.go` — install the
  seam, apply automatic moves, and keep ASK-boundary refusals.
- `internal/continuity/task/handoff.go` — automatic switch recording with a
  reason and `control-plane` approval.
- `docs/architecture/WORKSPACE_AUTHORITY.md`, `TASK_CONTINUITY.md`,
  `ARCHITECTURE.md`, `OHGO_ARCHITECTURE_MAP.md`,
  `docs/testing/TASK_CONTINUITY_TEST_CATALOG.md` — update in the same change.

## 17. Files/packages that should NOT change

- `internal/agent/loop.go` admission path and `agent.Config.Authorize`.
- `internal/security/*` and `workspace.Permits`/`Admit` capability semantics.
- `internal/execution/tools/*` mutation gating (except no changes expected).
- `internal/provider/*`, SSE/transport, model catalog.
- `internal/continuity/task/store.go`, markdown/checkpoint formats unless the
  new contract field requires a compatible additive encoding.
- Existing session storage and `/task` lifecycle commands.
- The TUI's generic picker/approval rendering and transcript pipeline.
- `cmd/ohgo/build.go` tool registration.
- Any prompt text or tool schema.

## 18. Test strategy

### Unit / contract

- `workspace` capability analysis: READ/EXECUTE/INVESTIGATE/MUTATE requirements,
  staged conditional detection, preference vs. capability.
- `DecideTransition` matrix:
  - ASK + read → stay;
  - ASK + investigate/mutate → confirm;
  - BUILD + mutate/execute → stay;
  - BUILD + moderate read → stay;
  - BUILD + strong read → confirm ASK;
  - BUILD + strong investigate → auto FUZZ;
  - FUZZ + execute/read → stay;
  - FUZZ + mutate → auto BUILD;
  - FUZZ + strong read → confirm ASK;
  - ambiguous/no-signal → stay;
  - conversational acknowledgement/continuation → stay.
- `RouteTurn` capability guard: FUZZ can run tests; FUZZ cannot mutate; ASK
  cannot execute/investigate; BUILD can read/investigate.
- `BuildIntentContract`:
  - conditional staged prompt is `ConditionalMutation`;
  - unconditional "fix the bug" is not;
  - "If tests fail, fix them" is still a mutation request (no staged
    investigation);
  - `no_creation` ignores "do not create a new task";
  - `no_mutation` is phase-scoped for staged conditional contracts.
- `agent.EvaluateTaskCompletion`:
  - conditional + no mutation + substantive answer → complete phase;
  - conditional + mutation + verification → complete;
  - conditional + mutation + no verification → review;
  - unconditional mutation + no change → review;
  - pure investigation + no mutation → complete;
  - empty/deferred answer still review.

### Runtime / TUI (scripted provider, real controller, real gate)

- Scenario A: ASK question stays ASK, no picker, no mutation.
- Scenario B: ASK + explicit fix shows handoff confirmation; accept → BUILD;
  reject → no build execution.
- Scenario C: ASK + staged/reproduction investigation shows FUZZ confirmation.
- Scenario D: BUILD + "check/understand" proceeds under BUILD with no picker and
  no forced mutation; strong human-facing question confirms ASK.
- Scenario E: BUILD + strong investigate auto-switches to FUZZ, same task, FUZZ
  write denied.
- Scenario F: FUZZ + mutation prompt auto-switches to BUILD, same task/evidence,
  BUILD write and verification succeed.
- Scenario G: BUILD/FUZZ + strong question shows ASK confirmation; no silent
  switch.
- Scenario H: conditional staged prompt under BUILD/FUZZ starts investigation,
  not `NEEDS_REVIEW`; mutation branch enters only after an authorized turn and
  then requires verification.
- Scenario I: ASK → FUZZ → BUILD lifecycle retains task ID, evidence,
  hypotheses, tool results, objective, budget, completion state.
- Scenario J: role model selection changes with each workspace in the harness
  seam; automatic transitions use the target role model.

### Production seam (`cmd/ohgo`)

- `turnTransition` returns automatic BUILD↔FUZZ, confirmation for ASK
  boundaries, and no picker for capability-compatible requests.
- Automatic transition leaves the gate untouched during prediction and moves it
  only through `SwitchWorkspace`.
- Accepted confirmation still uses `SwitchWorkspace`.
- Rejected narrowing runs under the capable current workspace.
- REPL applies automatic transitions and still refuses ASK escalation.
- Task switch record has the truthful reason and approval source.

### Validation commands

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./...
go test -race ./...
golangci-lint run ./...
```

Live-model validation is unavailable in this environment; any real-model claim
must be reported as unverified.

## 19. Regression risks

- **R1 — FUZZ mutation leak:** an automatic FUZZ → BUILD that mutates before the
  gate moves. Mitigation: apply the switch before `startTurn`, and keep the
  per-call gate unchanged.
- **R2 — ASK silent escalation:** an automatic path accidentally includes ASK.
  Mitigation: `DecideTransition` only marks automatic when both endpoints are
  BUILD/FUZZ; tests pin every ASK row.
- **R3 — Picker removed everywhere:** explicit `/permissions` and genuine
  boundary dialogs must remain. Mitigation: remove only the production
  `TransitionKindAmbiguous` trigger; keep the surface.
- **R4 — False completion:** conditional handling must not complete a mutation
  objective that actually changed the repo without verification. Mitigation:
  conditional phase applies only when no mutation/prior change is observed.
- **R5 — Task fragmentation:** automatic switches bypassing `task.Begin` or
  recording a new task. Mitigation: route all switches through
  `SwitchWorkspace` and the existing handoff record; assert same task ID.
- **R6 — Model collapse:** a new generic mode replaces role models. Mitigation:
  no new model abstraction; assert role-specific resolution in harness tests.
- **R7 — Constraint loss:** dropping phase-scoped `no_mutation` globally weakens
  an explicit read-only request. Mitigation: drop only for staged conditional
  contracts whose initial phase runs under ASK/FUZZ; keep it for unconditional
  read-only objectives.
- **R8 — Existing exact-handler tests:** `RouteTurn` and `turnTransition` tests
  encode the old refusal table. They must be updated to the new capability
  contract, not deleted wholesale.
- **R9 — Persisted contract compatibility:** add the new field as an optional
  JSON field so old checkpoints load unchanged.

## 20. Migration / implementation order

1. **Contract first:** add `ConditionalMutation` (additive JSON), staged
   detection, and conditional-aware grounding/constraints. Update unit tests.
2. **Completion second:** teach `EvaluateTaskCompletion` the conditional
   investigation phase; update `turnTaskOutcomeForAuthority` to map it to
   `taskActionPhaseComplete`. Keep proof rules for the mutation branch.
3. **Policy third:** add `workspace.DecideTransition` + capability analysis with
   the full matrix. Unit-test it in isolation.
4. **Routing fourth:** change `RouteTurn` to the capability guard; update the
   matrix tests.
5. **Seam fifth:** rewrite `turnTransition` to use the decision; add automatic
   result fields; keep `TransitionKindAmbiguous` only for explicit surfaces.
6. **TUI sixth:** apply automatic transitions through the workspace owner;
   change reject behavior for narrowing; keep confirmation dialogs.
7. **CLI seventh:** install `SwitchWithReason`, record automatic handoffs,
   update REPL application.
8. **Docs eighth:** update canonical docs and the test catalog.
9. **Validation:** run the six repository checks and the scenario suite; report
   any unverified boundary.

## 21. Acceptance criteria

The Phase 1 change is complete only when:

- [ ] Workspace is a current operating surface, not a task classification.
- [ ] `DecideTransition` is the single control-plane policy and the TUI only
      applies/renders it.
- [ ] ASK stays read-only and never silently escalates.
- [ ] ASK → BUILD and ASK → FUZZ require confirmation.
- [ ] BUILD/FUZZ → ASK requires confirmation when the request is clearly
      human-facing.
- [ ] BUILD → FUZZ and FUZZ → BUILD happen automatically for the required
      capabilities, without a new task or repeated dialogs.
- [ ] FUZZ remains unable to mutate source; BUILD retains mutation authority.
- [ ] Conditional mutation is not treated as an unconditional current objective.
- [ ] Pure investigation and conditional investigation phases do not fail with
      "no change was observed".
- [ ] Mutation tasks still require observed changes and successful verification.
- [ ] The workspace picker is no longer shown for every new request; it remains
      at genuine human authority boundaries and explicit selection surfaces.
- [ ] Task ID, context, evidence, hypotheses, timeline, budget, and completion
      state survive ASK → FUZZ → BUILD.
- [ ] ASK/FUZZ/BUILD keep their own model configuration.
- [ ] `gofmt`, `go build`, `go vet`, `go test`, `go test -race`, and
      `golangci-lint` pass (or any unavailable command is reported as
      unverified).
- [ ] No known regression remains in proposal/approval, evidence, session, or
      transcript behavior.
