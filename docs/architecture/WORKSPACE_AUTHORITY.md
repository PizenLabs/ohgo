# Workspace Authority: Intent-Aware Transitions

> **Status:** implemented.
> **Scope:** ASK / BUILD / FUZZ authority, turn routing, cooperation, boundaries.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](../../OHGO_ARCHITECTURE_MAP.md).
> **Permanent rules:** [`AGENTS.md`](../../AGENTS.md) and the architecture
> constraints enforced by `internal/architecture`.
> **Runtime behaviour:** [`ARCHITECTURE.md`](ARCHITECTURE.md).

This document describes the authority model that is actually implemented. It
replaces the earlier target-only statements about cross-workspace behaviour
with the behaviour the runtime enforces today.

---

## 1. Three authorities over one runtime

ASK, BUILD, and FUZZ are not three agents and not three execution engines.
They are three authority modes over one shared runtime:

```text
                    OHGO RUNTIME
                         │
             ┌───────────┼───────────┐
             │           │           │
            ASK        BUILD       FUZZ
          READ ONLY   MUTATION    INVESTIGATION
                      AUTHORITY    AUTHORITY
```

Shared by all three: the provider transport, streaming, the agent loop, the
tool primitives, session infrastructure, model resolution, and the lifecycle.
Separate per workspace: the capability policy, allowed operations, mutation
permission, the mode contract shown to the model, and the transition rules.

| Workspace | Purpose     | Authority               |
| --------- | ----------- | ----------------------- |
| ASK       | Understand  | read / search / inspect |
| FUZZ      | Discover    | read + execute, no mutation |
| BUILD     | Change      | read + execute + mutation |

There are exactly three. Plan, review, debug, test, and research are activities
performed inside one of them, never a fourth workspace.

---

## 2. The invariant

> **Information may flow between workspaces. Authority may not escalate
> implicitly.**

The deepest form of the rule:

> The agent may choose how to answer a request; it may never choose to grant
> itself a stronger authority.

Writes through the loop are exactly where this is enforced, never in a prompt.

---

## 3. Intent

Intent is the semantic capability a request asks for. It is not grammar: a
question can carry execution intent and an imperative can be informational.

| Intent        | Examples                                                         | Handler semantics |
| ------------- | ---------------------------------------------------------------- | ----------------- |
| `READ`        | what is…, why does…, how does…, explain, describe, summarize, compare, what changed… | ASK |
| `EXECUTE`     | implement, fix, modify, refactor, redesign, rework, create, delete, apply, run, change… | BUILD |
| `INVESTIGATE` | reproduce, investigate, diagnose, troubleshoot, stress, probe, try to break, find edge cases, benchmark, test failure modes… | FUZZ |

Intent and completion are different axes. A standalone `INVESTIGATE` request
routes to FUZZ because the work may execute probes, but its completion contract
is informational: the deliverable is a grounded answer, and a successful
command is evidence rather than task completion. Classifying the same request
as an execution objective would let the first successful process satisfy the
contract and withdraw the tool surface while the model is still investigating.
The projector keeps an investigation's executable surface open after a
repository overview for the same reason: one observed fact is not the probe the
user asked for. A staged conditional mutation remains `ObjectiveMutation` under
this rule; only its current phase is informational.

Classification is deterministic and small (`workspace.Classify`): no model, no
embeddings, no intent model. It strips politeness wrappers, splits the request
into its clauses, reads each clause's head, and matches a fixed phrase set.

**A question about an operation is not a request for it.** Before a clause can
supply execution or mutation evidence, it must be a clause the user *asked for*.
A clause is *asked* when it continues a question: the request opens with an
interrogative head (`does`, `is`, `why`, `how`, `do`, `whether`, `if`, …), or a
non-imperative clause carries a complement opener ("I want to ask if …"). A
question continues through coordinated and alternative clauses until a new main
clause begins at a comma boundary, so "if the tests fail, fix them" keeps its
imperative while "does this run, or do I run it myself?" stays a question.

The distinction is the difference between vocabulary and effect:

```text
"Does this automatically update GitHub, or do I have to run it manually?"  → READ
"Update this repository on GitHub."                                        → EXECUTE
```

`update`, `run`, `build`, `commit`, and `GitHub` are not authority signals on
their own. A verb only requests an effect in a clause that is not a question.
The same rule governs `workspace.RequestsMutation` (the completion contract) and
the grounding action extractor, so a question is never given a mutation
objective that completion would then demand evidence for.

Rework verbs (`redesign`, `rework`, `revamp`, `restyle`, `redo`, `overhaul`,
`modernize`, `redraw`, `reimplement`, `rearchitect`) head a repository change,
not a description. A follow-up such as “redesign it” after an informational
design turn is therefore a new mutation objective, not another read of the
design. It is a new task under the ordinary new-intent rule; the finished design
task is archived rather than reinterpreted, and completion requires an observed
change and a later check. The classifier never turns a question about a rework
(“should the site be redesigned?”) into the change itself.

**Ambiguity resolves downward.** A request the classifier cannot place becomes
`READ`, the lowest-authority interpretation, so a misclassification can only
reduce authority. When a request asks to both probe and change ("reproduce and
fix"), the investigative signal wins, because FUZZ is the lower authority.

The list of low-confidence examples the spec calls ambiguous — "Take a look at
this.", "Can you handle this?", "Check this.", "Make this better." — all
classify as `READ`.

A later clause with an explicit, unambiguous read head strengthens the READ
signal. "Check this project and analyze what this project is about?" opens with
the ambiguous verb `check`, so its first clause alone is only a moderate READ;
the later `analyze` clause is the objective's verb and makes the prediction
strong. Strength is still a level, not a score, and a later noun phrase is
deliberately not enough. A later mutation clause still wins as the objective,
so "check this project and create the missing files" remains EXECUTE.

`Classify` returns the intent alone. `ClassifyPrediction` returns it with an
**evidence level**, because routing may want to know how well the request was
placed without inventing a probability:

| Strength    | Meaning                                                              |
| ----------- | -------------------------------------------------------------------- |
| `strong`    | the leading verb matched the winning intent, and no rival is as strong |
| `moderate`  | only a corroborating phrase matched, uncontested                     |
| `ambiguous` | nothing matched (the `READ` fallback), or a rival signal is at least as strong |

A level is not a score. The classifier has no calibrated probability, so
nothing may render one (§9).

---

## 4. Transition policy

Capability, not intent-handler equality, decides whether the current workspace
can serve a request. `workspace.AnalyzeRequest` maps the request to one of four
capabilities:

| Capability | Meaning | ASK | FUZZ | BUILD |
| --- | --- | --- | --- | --- |
| `READ` | inspect, search, reason, explain | yes | yes | yes |
| `EXECUTE` | run a process or check | no | yes | yes |
| `INVESTIGATE` | reproduce, probe, experiment | no | yes | yes |
| `MUTATE` | change repository source | no | no | yes |

A staged request (`investigate first; if confirmed, implement the fix`) starts
in `INVESTIGATE`, even when a later clause names a mutation. The mutation
remains in the contract for the branch, but it does not classify the current
turn as a mutation objective.

`workspace.DecideTransition` owns the policy. It is pure and returns one of:

- `stay` — the current workspace can serve the request;
- `automatic` — a cooperative BUILD ↔ FUZZ handoff;
- `confirm` — a human authority-boundary decision.

```text
ACTIVE   REQUEST                         DECISION
────────────────────────────────────────────────────────────────────
ASK      READ / analysis                 stay ASK
ASK      greeting / continuation         stay ASK
ASK      EXECUTE / MUTATE                confirm BUILD
ASK      INVESTIGATE                     confirm FUZZ

BUILD    READ (ambiguous/moderate)       stay BUILD
BUILD    READ (strong question)          confirm ASK
BUILD    EXECUTE / MUTATE                stay BUILD
BUILD    INVESTIGATE (strong/staged)     automatic FUZZ
BUILD    INVESTIGATE (weak/moderate)     stay BUILD

FUZZ     READ (ambiguous/moderate)       stay FUZZ
FUZZ     READ (strong question)          confirm ASK
FUZZ     EXECUTE (non-mutating)          stay FUZZ
FUZZ     INVESTIGATE                     stay FUZZ
FUZZ     MUTATE                          automatic BUILD
```

The thresholds are deliberate:

- crossing **into ASK** requires strong READ evidence, so a vague "check this"
  does not interrupt a BUILD/FUZZ session;
- crossing **out of ASK** requires at least moderate evidence for a new
  capability; an explicit mutation always qualifies;
- BUILD → FUZZ requires a strong investigation signal;
- FUZZ → BUILD is automatic whenever the request actually needs mutation;
- every automatic decision has BUILD and FUZZ as both endpoints. ASK is never
  crossed automatically in either direction.

An automatic or confirmed target is applied only through
`harness.SwitchWorkspace`, which moves the live gate, resolves the target role's
model, and records the transition on the same task. A rejected BUILD/FUZZ → ASK
decision keeps the capable current workspace and runs the turn there. A rejected
ASK → BUILD/FUZZ decision never runs the request with insufficient authority.

`workspace.RouteTurn` is the capability guard, not the transition policy:

```text
ASK   READ                           allowed
ASK   EXECUTE / MUTATE               refused -> BUILD
ASK   INVESTIGATE                    refused -> FUZZ
FUZZ  READ / EXECUTE / INVESTIGATE   allowed
FUZZ  MUTATE                         refused -> BUILD
BUILD READ / EXECUTE / INVESTIGATE   allowed
BUILD MUTATE                         allowed
```

The guard prevents an authority from performing work outside its contract. It
does not force a workspace move when the authority already holds the
capability.

A **task-local continuation** (`continue`, `verify the fix`, `run the
verification`, …) is not a new capability request. It runs under the active
authority so the open task can finish its persisted contract; the task layer
resolves ownership before workspace prediction or routing. This grants nothing:
the gate still admits each tool, so a continuation under ASK cannot execute
merely because verification was named.

Ownership and routing are different facts. An exact repeat of the task's
objective, or a role change on that objective, is still a continuation of the
same task identity, but the prompt states a capability requirement. It therefore
goes through `workspace.DecideTransition` like a new request: a resumed BUILD
task whose objective is a strong investigation hands off to FUZZ automatically,
and a FUZZ mutation objective hands off to BUILD. Only continuation wording that
carries no capability signal — `continue`, a verification of the task's own
result, or an already-decided handoff — bypasses prediction. A task follow-up
(`fix it`) is predicted for the same reason. This keeps a resumed session from
silently staying in BUILD merely because the task was already open.

Natural verification wording (`check whether the fix worked`, `did it work?`,
`are the tests passing?`) follows the same rule when an execution or mutation
task owns the result. The predicate needs both a check cue and a reference to
existing work; `check the authentication flow` remains a new request. A short
follow-up that refers to the task's own object (`fix it`, `implement the fix and
verify it`) also attaches to the open task. A prompt that names a new subject
(`fix the checkout bug and run tests`) remains a new objective. A terminal task
owns no continuation: repeating `verify the fix` after `PROVEN` reports the
finished state instead of archiving the proof and starting a duplicate task.

### Implementation refinement

An open BUILD mutation objective is an evolving unit of work. Once a turn has
applied a change but has not proven it, the next user message often names only
the delta ("I want to use green and black", "make it green", "make it wider")
and carries no verb the classifier can place. That is a continuation of the
open implementation, not a new informational objective: the delta inherits the
persisted contract and the mutation authority the contract requires. Without
this, the READ fallback rebuilt an informational contract, and the projection
withheld the only tools that could apply the refinement even though the live
workspace was BUILD.

The inheritance is bounded. It applies only when the open contract is a
mutation objective under BUILD and the delta carries no capability signal of
its own. An explicit mutation prompt is a new mutation objective: it stays
under BUILD and keeps the write surface, but it does not impersonate the open
objective. A request that states a read or investigation capability, or an
explicit whole-repository no-mutation limit, remains a new turn and is routed
by the ordinary policy. A whole-change prohibition followed by a request to
understand is a strong READ signal: it can narrow BUILD to the ASK
confirmation boundary, and it never continues the mutation. The task still owns
its evidence and completion requirements; the delta does not create a new
proof or relax a verification requirement.

> **Continuation authority preservation.** A continuation that refines an
> active implementation inherits the current authority. Only a new objective
> that explicitly requires a different capability class moves it.

A rework request such as `redesign it` is still a refinement when the open
contract already owns the object; it remains a new mutation objective when the
open task is informational. The task layer decides attachment from the open
contract and the delta, never from similarity alone.

### Conditional objectives and phase completion

A contract records `ConditionalMutation` when the request stages an
investigation/understanding phase before a conditional future change. `Kind`
remains `ObjectiveMutation` so the change is authorized and grounded when the
branch is reached; the marker says the current phase is not yet that change.

A task contract can require a change while the current turn runs under ASK or
FUZZ. The turn cannot perform the change, but it can still produce the
understanding or investigation the task needs first. The projection shape is
built from the phase contract as well, so the model is never asked for evidence
the active authority cannot acquire; an execute-class tool is withheld when the
authority forbids execution. A staged conditional mutation defers its literal
target-state requirement to the change branch, because the investigation phase
must not be reported as a failed read of a target it has not been authorized to
change. Completion therefore judges the phase the active authority can actually
perform:

- a mutation objective under ASK or FUZZ, or an execution objective under ASK,
  is evaluated as an informational phase;
- a substantive, grounded answer completes the phase but **does not** complete
  the task. The task remains `RUNNING` (`ACTIVE`), keeps its identity,
  contract, and evidence, and records no completion proof;
- the next turn under the required authority continues the same task and may
  complete it;
- the phase boundary is not a pass. An empty or deferred answer, an
  unrecovered tool failure, a grounding failure, or an observed mutation
  outside authority still enters review.

A conditional mutation under BUILD follows the same phase rule before the
branch is entered. With no observed change and no prior durable change, the
turn is judged as the investigation answer, not as a failed mutation. Once a
change is observed (or a prior change exists), the full mutation contract
applies: targets, behavioural checks, and a successful verification after the
change.

An execution during the investigation phase runs behind a read-only
authorization, so it is investigation evidence, not a potential mutation that
owes a later verification. Only a definite change, or an execution under
write-capable authority, creates that verification debt.

Phase completion narrows the completion requirement; it does not erase an
explicit request constraint. For a staged conditional objective, `without
changing anything` and `without modifying source code` are scoped to the
investigation phase. That phase runs under ASK or FUZZ, whose gate already
prevented mutation; the wording is not carried as a global `no_mutation` veto
on the later BUILD branch. An unconditional `do not modify anything` remains a
task-global constraint. `do not create a new task` is a continuity instruction,
not a repository `no_creation` limit.

This is why a FUZZ or BUILD investigation of a request that also says "if
confirmed, implement the fix" is not reported as "the request asked for a
repository change but no change was observed": the investigation phase is
complete, and the mutation remains a later, explicitly authorized phase.

---

## 5. Capability guard and turn state

There is no second "handler authority" behind the active workspace. The turn
runs under the live workspace, and `RouteTurn` either admits the request because
the workspace holds the capability or refuses it:

```text
active workspace   — the session's authority; only a switch moves it
turn intent        — the classified request (ephemeral)
turn capability    — READ / EXECUTE / INVESTIGATE / MUTATE
admitted authority — the active workspace for this turn
```

BUILD answering a read request remains BUILD; it does not impersonate ASK. If
the transition policy prefers ASK for a clear human-facing question, it asks
first, and the accepted switch changes the live workspace. If the user declines,
BUILD answers the question because reading is within BUILD's authority.

A turn is a boundary: the next turn is routed from the workspace in force at
that point. No transient handler state is carried back.

---

## 6. Escalation and cooperation

The human boundary is asymmetric:

- **ASK never escalates implicitly.** An executable or investigative request
  under ASK is a confirmation, never a silent move. Declining leaves the
  request unrun because ASK cannot perform it.
- **BUILD ↔ FUZZ cooperate automatically.** BUILD may hand an investigative
  request to FUZZ; FUZZ may hand a repository-change request to BUILD. The
  control plane selects the target, and the owner applies it before the turn.
- **FUZZ still cannot mutate.** The FUZZ → BUILD handoff happens first; the
  per-call gate remains the safety boundary. A finding from FUZZ never grants
  mutation authority inside FUZZ.
- **BUILD retains mutation authority**, but it is not obliged to mutate an
  investigation it can answer.

The only ways authority changes are:

1. an explicit user switch — `/ask`, `/fuzz`, `/build`, the authority selector
   (`/permissions`, `/mode`), the `--workspace` flag, or a resumed session's
   snapshot;
2. an accepted human-boundary decision;
3. an automatic BUILD ↔ FUZZ decision from `workspace.DecideTransition`,
   whether the request text or a grounded tool call produced it.

The agent may propose intent through its request text or through a tool it
calls, but it cannot move authority. The control plane owns the decision, and
only `harness.SwitchWorkspace` performs it.

### Tool-boundary handoff

A staged task changes phase while it runs, and the model's own action is the
signal: a finding artifact is investigation, a change artifact or a call that
actually mutates is mutation. `workspace.ToolCallTransition` classifies the
invocation through `OperationForTool` and applies the ordinary
`DecideTransition` policy before the call executes:

- a finding artifact under BUILD → automatic FUZZ;
- a change or verification artifact, or a call that mutates this invocation
  under FUZZ → automatic BUILD;
- a plan artifact under FUZZ would cross ASK, so it is not automatic and the
  artifact tool refuses it with the allowed kind.

A tool's declared action class is a capability, not a property of the current
call. A shell can mutate, but `ls` does not; the per-invocation classification
is what keeps an inspection in FUZZ instead of escalating it to BUILD.

The loop consults the host hook after semantic grounding, so an action the
user's request did not authorize cannot use a handoff to reach a stronger
authority. The host applies the target through `harness.SwitchWorkspace`,
records it on the same task, and the loop re-authorizes the same call under the
new live gate. The trigger differs from a turn-boundary handoff; the decision,
the owner, and the continuity record are the same.

---

## 7. Where authority is enforced

Enforcement is structural, below the UI and above execution:

```text
cmd/ohgo
  workspace.DecideTransition  pure policy: stay | automatic | confirm
    └── TUI/REPL applies the decision
          └── harness.SwitchWorkspace          the single authority move
                └── authorityGate.Set()        live authority for the turn
  task.Controller.RecordAutomaticSwitch /
  RecordWorkspaceSwitch                        continuity record, same task

agent.Loop.execute
  Config.Authorize(call, tool)  the production admission path
    └── securityGate.Evaluate → workspace.Authority.Admit
          └── Workspace.Permits(Operation)
```

- `workspace.Permits` / `workspace.Admit` is the capability decision. An
  unrecognised operation is denied. The loop's production authorizer is
  `Config.Authorize` (`securityGate`); `Config.Admit`/`Config.Approve` remain
  only as the legacy fallback when no authorizer is installed.
- `authorityGate` is the only mutable authority. Because the loop's admission
  hook and the subagent spawner both read the gate at call time, a switch
  applies to anything the turn delegates.
- The `artifact` tool resolves its producer from the live gate on every call,
  not from the workspace active when the tool set was built. A BUILD ↔ FUZZ
  switch therefore changes which kind the existing tool can author; a captured
  origin would attribute a FUZZ finding to BUILD and refuse it.
- A `verification` artifact is a reference to a check the runtime observed, not
  a claim the model may author. The host wires the tool to
  `Loop.VerificationObserved`, which is true only after a successful check that
  follows the last definite change. The tool refuses an unbacked verification
  with the check to run, so a durable "verified" record cannot contradict the
  completion evidence. Recording a `change` before the check is still allowed;
  only the verification claim is gated.
- A refused route never reaches the model: `harness.RouteTurn` is consulted in
  `repl`, in `tui.Program.runTurn`, and in `runOnce` before `Loop.Run`.

Prompts are not the boundary. `prompt.WorkspaceContract` tells the model what
its mode means, but deleting it cannot grant a capability: the gate denies the
call regardless.

---

## 8. Session state

The session keeps the active workspace durably; the turn intent is transient
and derived.

```text
session
├── workspace            active authority, persisted (session.SetAuthority)
├── effective model      resolved per workspace role, persisted
└── turn
    ├── intent           classified per request
    ├── capability       READ / EXECUTE / INVESTIGATE / MUTATE
    └── admitted gate    active gate value for the turn
```

`SwitchWorkspace` persists the active workspace, the effective model, and moves
the gate together. `RouteTurn` never persists anything.

---

## 9. Truthful state

The UI is a projection, never a router. It consumes the control-plane decision
and renders the facts without conflating them:

```text
activeWorkspace     the session's authority boundary  (persistent)
turn.intent         the classified request            (ephemeral)
turn.capability     what the request needs            (ephemeral)
transitionDecision  stay / automatic / confirm        (ephemeral)
```

A transition is visible because the owner publishes the new workspace and a
notice. A turn refused by the capability guard publishes no workspace change:
it reports the authority it needs and the command that grants it (`this request
needs BUILD authority; switch with /build and resend it`). The refusal happens
before the model is called, so no artificial turn is simulated.

The plain REPL applies automatic BUILD ↔ FUZZ decisions and prints the move. It
has no confirmation surface, so crossing ASK prints the required target and
waits for an explicit command.

### Canonical lifecycle

```text
idle
  ↓  user: "What is Go?"          (activeWorkspace = BUILD)
request analysed: READ, strong
decision: confirm ASK
  ↓
human accepts
  ↓
SwitchWorkspace -> ASK          (same session, same task if one is open)
  ↓
turn runs under ASK             (read-only)
  ↓
idle: ASK
```

```text
idle
  ↓  user: "reproduce the failure" (activeWorkspace = BUILD)
request analysed: INVESTIGATE, strong
decision: automatic FUZZ
  ↓
SwitchWorkspace -> FUZZ          (same session/task, recorded handoff)
  ↓
turn runs under FUZZ             (execute, no mutation)
  ↓
idle: FUZZ
```

> The UI must never represent an automatic handoff as an implicit silent
> authority change: the target, reason, and new workspace are published.

A staged task that changes phase mid-turn follows the same shape, triggered by
its tool call instead of a new request:

```text
BUILD, investigating
  ↓  model: artifact(kind=finding)
ToolCallTransition: INVESTIGATE / FUZZ
decision: automatic FUZZ
  ↓
SwitchWorkspace -> FUZZ          (same session/task, recorded handoff)
  ↓
artifact executes under FUZZ
  ↓
model: write checkout.go
ToolCallTransition: MUTATE / BUILD
decision: automatic BUILD
  ↓
SwitchWorkspace -> BUILD
  ↓
write executes under BUILD
```

The model sees the same kind of target/reason notice, and the task timeline
records both moves as APPLIED handoffs on one task identity.

---

## 10. Decisions and the decision surface

The control plane produces one `workspace.TransitionDecision` per request:

```text
request text
  -> workspace.AnalyzeRequest      capability + evidence + staged phase
  -> workspace.DecideTransition    stay | automatic | confirm
  -> TUI apply / render             owner seam or confirmation dialog
  -> SwitchWorkspace                the one authority move
  -> RouteTurn -> Loop.Run          unchanged enforcement
```

```text
tool call
  -> semantic grounding            never widen an unrequested action
  -> workspace.ToolCallTransition  capability the call itself needs
  -> workspace.DecideTransition    automatic only on BUILD <-> FUZZ
  -> SwitchWorkspace               the same owner, same task record
  -> re-authorize the same call    under the new live gate
```

`AnalyzeRequest` and `DecideTransition` are pure. `WorkspaceHooks.Predict` is
side-effect-free by contract, unlike `PrepareTurn`, which installs the turn's
handler. Only an automatic decision or an accepted confirmation reaches
`harness.SwitchWorkspace` — the same owner `/ask`, `/fuzz`, and `/build` use.

### Automatic: BUILD ↔ FUZZ

The TUI applies an automatic decision before starting the turn:

```text
BUILD -> FUZZ   "investigation / reproduction"
FUZZ  -> BUILD  "repository change"
```

The switch is recorded on the existing task handoff as `APPLIED`, with
`ApprovedBy = control-plane` and the measured workspace state. The next turn
consumes that record and continues the same task identity. An unrelated new
objective does not inherit the old contract merely because the switch was
automatic; the ordinary new-intent boundary detaches the old task.

### Confirm: the ASK boundary

A confirmation uses the existing typed transition dialog:

```text
Switch to ASK?

This request asks for a human-facing explanation; ASK keeps the session read-only.

[ Keep BUILD ]   [ ASK ]
Enter to confirm · Esc keeps BUILD
```

The highlight opens on **keep current**, and left/right move it without wrapping,
so a stray Enter never changes authority. **Enter** confirms; **Esc** declines.
Declining a narrowing decision (BUILD/FUZZ → ASK) keeps the capable current
workspace and runs the turn there. Declining a widening decision (ASK → BUILD or
FUZZ) restores the request without running it, because the current authority
cannot perform it. **Ctrl+C** abandons the pending turn and keeps the typed
prompt. Each choice carries its workspace's own semantic accent; no percentage
is ever shown.

While the dialog is open the composer is hidden and key handling is captured, so
no turn — and therefore no provider request, tool, or mutation — can start. The
decision is cleared on accept, reject, cancel, turn completion, cancellation,
history replacement, an explicit workspace change, or a clear.

The plain REPL has no confirmation surface. BUILD ↔ FUZZ moves automatically;
crossing ASK prints the required target and asks for an explicit `/ask`,
`/fuzz`, or `/build`, then the user resends the request. It never runs a
human-boundary request under the wrong authority.

## 11. Tests

The invariants are behavioural: they assert that a call never executes, not
that a classifier returned a string.

| Invariant | Test |
| --------- | ---- |
| A — ASK cannot escalate to BUILD | `cmd/ohgo.TestAskRefusesExecutionWithoutCallingTheModel` |
| B — ASK cannot escalate to FUZZ | `cmd/ohgo.TestAskRefusesInvestigation` |
| C — BUILD may read and investigate | `cmd/ohgo.TestBuildExecutesExplicitMutation`, `workspace.TestRouteTurnTransitionMatrix` |
| D — BUILD analysis does not force mutation | `tui.TestBuildAnalysisConfirmsAskBoundary` |
| E — a FUZZ finding does not authorize mutation | `cmd/ohgo.TestFuzzFindingDoesNotAuthorizeMutation` |
| F — mutation requires BUILD, automatically from FUZZ | `cmd/ohgo.TestFuzzMutationHandsOffToBuild`, `workspace.TestRouteTurnTransitionMatrix` |
| G — routing never changes authority | `workspace.TestRouteTurnNeverChangesTheActiveWorkspace`, `cmd/ohgo.TestRouteTurnKeepsBuildAuthority` |
| H — authority is runtime-enforced, not prompt-enforced | `cmd/ohgo.TestAuthorityIsEnforcedWithoutPromptInstructions` |
| Classifier contract | `workspace.TestClassifyReadsCapabilityNotGrammar`, `TestClassifyFallsBackToTheLowerAuthority` |
| UI consults the router | `tui.TestRunTurnRefusesBlockedIntentBeforeTheModel`, `tui.TestRunTurnProjectsDelegatedRouteTruthfully` |
| UI never moves the workspace | `tui.TestDelegatedTurnNeverSwitchesWorkspace`, `tui.TestRenderShowsTurnSemanticsWithoutMovingTheWorkspace` |
| Turn state is ephemeral | `tui.TestTurnSemanticsAreClearedOnCompletion`, `cmd/ohgo.TestDelegatedTurnDoesNotLeakIntoTheNextRoute` |
| A resumed task's repeated objective is routed | `tui.TestRepeatedInvestigationObjectiveRoutesOpenBuildTaskToFuzz` |
| Task continuation keeps the active authority | `workspace.TestRouteTurnKeepsTaskContinuationInTheActiveWorkspace`, `workspace.TestVerificationContinuationIsAboutExistingWork`, `cmd/ohgo.TestTurnTransitionDoesNotAskWorkspaceForTaskContinuation`, `tui.TestVerificationContinuationRunsUnderBuildAndProves`, `tui.TestVerificationQuestionContinuationKeepsTaskIdentityAndProves`, `tui.TestOpenFuzzTaskContinueStaysInFuzz`, `tui.TestLifecycleUnderstandInvestigateImplementThenVerify` |
| An implementation refinement keeps BUILD mutation | `tui.TestContinuationAuthorityPreservationKeepsBuildMutationAuthority`, `tui.TestTaskTurnDecisionNamesImplementationRefinement` |
| An explicit mutation remains BUILD without inheriting the task | `tui.TestExplicitMutationRefinementStaysBuildWithANewContract` |
| An explicit no-mutation request does not inherit the implementation | `tui.TestImplementationRefinementStopsAtAnExplicitNoMutationRequest`, `workspace.TestWholeChangeProhibitionWithReadRequestNarrowsToAsk`, `workspace.TestBuildIntentContractRecognizesAContractedNoMutationLimit`, `contract.TestNoMutationRequestedRecognizesWholeChangeLimits`, `cognition.TestBuildShapeHonorsAContractedNoMutationLimit` |
| Investigation yields from an open implementation | `tui.TestImplementationRefinementYieldsToInvestigation` |
| Lower authority completes a phase, not the task | `workspace.TestPhaseContractDefersObjectivesTheAuthorityCannotPerform`, `tui.TestFuzzInvestigationPhaseOfMutationTaskStaysActive`, `tui.TestFuzzInvestigationPhaseContinuesIntoBuildSameTask`, `tui.TestFuzzInvestigationPhaseRejectsADeferredPlan`, `tui.TestLifecycleUnderstandInvestigateImplementThenVerify` |
| Continuation grants no authority | `tui.TestVerificationContinuationUnderAskCannotExecute` |
| Failed verification stays unproven | `tui.TestFailingVerificationContinuationDoesNotProve` |
| Proven objectives terminate | `tui.TestProvenTaskRejectsRedundantContinuation` |
| Effective authority is bounded | `workspace.TestRouteEffectiveAuthorityNeverExceedsTheWorkspace`, `cmd/ohgo.TestRefusedRouteLeavesAuthorityUntouched` |
| Cross-workspace scenario | `cmd/ohgo.TestCrossWorkspaceSessionKeepsAuthority` |
| Classifier evidence levels | `workspace.TestClassifyPredictionReportsStrength`, `TestClassifyPredictionMarksContestedPredictionsAmbiguous` |
| Policy matrix | `workspace.TestDecideTransitionMatrix`, `workspace.TestTransitionDecisionIsDataOnly` |
| Automatic only on the execution side | `workspace.TestDecideTransitionMatrix`, `cmd/ohgo.TestWideningFromAskIsOnlyAHandoff` |
| Prediction moves nothing | `cmd/ohgo.TestTurnTransitionPredictsWithoutMovingAuthority` |
| Acceptance uses the owner | `tui.TestAcceptedProposalSwitchesThroughTheOwner`, `cmd/ohgo.TestAcceptedTransitionUsesTheAuthoritativeSwitch` |
| Rejection cannot escalate | `tui.TestRejectedProposalKeepsAuthorityAndRunsTheTurn`, `TestEscRejectsWithoutSwitching`, `cmd/ohgo.TestRejectedTransitionLeavesAuthorityUntouched` |
| No execution while pending | `tui.TestProposalBlocksTheTurnUntilDecided`, `TestAcceptedSwitchFailureDoesNotRunTheTurn` |
| Same session across a switch | `cmd/ohgo.TestTransitionKeepsTheSameSession` |
| Ephemeral proposal state | `tui.TestStaleProposalCannotSurviveALifecycleEvent`, `TestProposalNavigationClampsAtTheEnds` |
| No fabricated confidence | `cmd/ohgo.TestTurnTransitionCarriesNoProbability`, `tui.TestTransitionPromptRendersTheDecision` |
| Automatic BUILD <-> FUZZ cooperation | `tui.TestHandoffThroughTurnEntrypointPreservesTaskAndAuthority`, `TestBuildToFuzzTransitionPreservesImplementationContext`, `TestRepeatedHandoffsPreserveContextsAndWorkspace` |
| Tool-boundary phase handoff | `workspace.TestToolCallTransitionRoutesCapabilityBoundary`, `workspace.TestToolCallTransitionKeepsReadOnlyShellInFuzz`, `agent.TestWorkspaceHandoffRunsBeforeAdmission`, `agent.TestWorkspaceHandoffCannotGroundAnUngroundedMutation`, `tui.TestToolBoundaryHandoffRoundTripKeepsOneTask` |
| ASK boundary confirmation | `tui.TestBuildStrongQuestionConfirmsAskBoundary`, `TestBuildAnalysisConfirmsAskBoundary`, `TestAskMutationRequiresBuildConfirmation`, `cmd/ohgo.TestWideningFromAskIsOnlyAHandoff` |
| Later read clause strengthens READ | `workspace.TestProjectAnalysisPromptRoutesToAsk`, `workspace.TestAmbiguousReadVerbStaysInACapableWorkspace` |
| Staged conditional workflow | `workspace.TestConditionalWorkflowRequiresAStagedInvestigation`, `tui.TestConditionalWorkflowStartsInvestigatingThenContinuesIntoBuild` |
| Conditional completion is phase-aware | `agent.TestConditionalMutationCompletesInvestigationWithoutAChange`, `tui.TestBuildConditionalInvestigationStaysActiveWithoutMutation` |
| A failed check resolves after correction | `agent.TestEvaluateCompletionResolvesAFailedVerificationAfterCorrection` |
| A verification artifact needs an observed check | `agent.TestLoopTracksVerificationAfterTheLastDefiniteChange`, `tools.TestArtifactVerificationRequiresAnObservedCheck`, `cmd/ohgo.TestArtifactToolFollowsTheLiveWorkspace` |
| Model specialization survives a switch | `cmd/ohgo.TestSwitchWorkspaceRebindsTheRoleModel` |
| A rework follow-up is a mutation objective | `workspace.TestClassifyTreatsReworkAsRepositoryChange`, `cognition.TestBuildShapeAllowsMutationForAReworkObjectiveInBuild`, `tui.TestRedesignFollowUpStartsANewMutationTaskAndCannotProveFromDesignWork` |
| A question about a rework stays informational | `workspace.TestClassifyKeepsReworkQuestionsInformational` |
| A rework under ASK is refused before the model | `tui.TestRedesignUnderAskIsRefusedBeforeTheModel` |
| A change artifact is not an observed mutation | `agent.TestChangeArtifactIsNotAnObservedMutation` |
| Completed todos are not objective proof | `task.TestCompletedTodosDoNotProveAMutationObjective` |
| A new task starts with an empty plan | `task.TestNewTaskDoesNotInheritThePreviousTodoPlan`, `tools.TestTodoResetClearsTheSessionPlan` |
| Resuming a task clears the previous plan | `task.TestResumeTaskClearsThePreviousSessionPlan` |

Run the full suite:

```sh
go build ./...
go vet ./...
gofmt -l .
go test ./...
go test -race ./...
```

---

## 12. Non-goals

This model deliberately does **not** introduce:

- a general intent engine, planner, or multi-agent router;
- a second execution engine or an agent clone;
- per-request model switching beyond the existing workspace role binding;
- automatic escalation across ASK (only BUILD <-> FUZZ cooperate automatically);
- a calibrated confidence score (evidence levels only);
- an interactive transition prompt in the plain REPL;
- a new session, task file, or persistence layer for a workspace move.

The design is a small classifier, an explicit transition policy, and a runtime
gate. It optimises for predictable behaviour, not maximum autonomy.
