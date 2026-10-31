# Workspace Authority: Intent-Aware Transitions

> **Status:** implemented.
> **Scope:** ASK / BUILD / FUZZ authority, turn routing, delegation, escalation.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](../../OHGO_ARCHITECTURE_MAP.md).
> **Permanent rules:** [`OHGO_ARCHITECTURE_RULES.md`](../../OHGO_ARCHITECTURE_RULES.md) §13–§14.
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
| `EXECUTE`     | implement, fix, modify, refactor, create, delete, apply, run, change… | BUILD |
| `INVESTIGATE` | reproduce, investigate, diagnose, troubleshoot, stress, probe, try to break, find edge cases, benchmark, test failure modes… | FUZZ |

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

**Ambiguity resolves downward.** A request the classifier cannot place becomes
`READ`, the lowest-authority interpretation, so a misclassification can only
reduce authority. When a request asks to both probe and change ("reproduce and
fix"), the investigative signal wins, because FUZZ is the lower authority.

The list of low-confidence examples the spec calls ambiguous — "Take a look at
this.", "Can you handle this?", "Check this.", "Make this better." — all
classify as `READ`.

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

## 4. Transition matrix

ASK is execution-closed: it cannot execute and it cannot mutate. BUILD and FUZZ
may delegate an informational READ turn to ASK semantics and then return.
Delegation does not transfer authority.

```text
ACTIVE       INTENT          RESULT
──────────────────────────────────────────────────────────────
ASK          READ            ASK                    (read-only)
ASK          EXECUTE         REFUSED — requires an explicit BUILD switch
ASK          INVESTIGATE     REFUSED — requires an explicit FUZZ switch

BUILD        READ            ASK semantics, BUILD remains active
BUILD        EXECUTE         BUILD
BUILD        INVESTIGATE     FUZZ semantics, BUILD remains active

FUZZ         READ            ASK semantics, FUZZ remains active
FUZZ         INVESTIGATE     FUZZ
FUZZ         EXECUTE         REFUSED — requires an explicit BUILD switch
```

The last row is deliberately conservative: a FUZZ finding never grants BUILD
mutation authority, so an executable-sounding request is refused until the user
switches. The rule behind every row is that a route is never a superset of the
active workspace's authority.

---

## 5. Delegation and return

An informational turn delegated from BUILD or FUZZ is temporary. The system
distinguishes:

```text
active workspace   — the session's authority; only an explicit switch moves it
turn handler       — the semantics answering the current turn
turn authority     — the capability actually admitted for the turn
```

A BUILD informational turn is:

```text
activeWorkspace = BUILD
currentTurn.handler = ASK
admitted authority for the turn = ASK
```

not:

```text
activeWorkspace = ASK
authority = ASK
```

After the turn the handler is recomputed from scratch, so the next turn is
routed from the unchanged active workspace — no state is carried back. This is
why "implement the fix" after an informational turn still mutates: BUILD was
never left.

Delegation only ever **narrows**. BUILD → ASK drops execution; FUZZ → ASK drops
execution. Nothing delegates to a stronger authority.

---

## 6. Escalation

There is no implicit escalation path:

- ASK never becomes BUILD or FUZZ because a request was phrased executably or
  investigatively. The request is refused and the user is asked to switch.
- FUZZ never becomes BUILD because a defect was found. A finding is reported;
  mutation requires an explicit BUILD transition.
- BUILD's authority is preserved but not exercised for an informational
  request.

The only ways authority changes are:

1. an explicit user switch — `/ask`, `/fuzz`, `/build`, the authority selector
   (`/permissions`, `/mode`), the `--workspace` flag, a resumed session's
   snapshot, or accepting a transition proposal (§10);
2. delegation to a **lower** authority for one turn.

The agent can suggest ("this request needs BUILD authority", or a transition
proposal) but cannot perform an escalation itself.

---

## 7. Where authority is enforced

Enforcement is structural, below the UI and above execution:

```text
cmd/ohgo
  authorityGate            the session's single live authority
    ▲                       read at call time by the loop and by subagents
    │ Set()
    ├── options.setAuthority / SwitchWorkspace   explicit transitions
    └── harness.RouteTurn                         delegated turns

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
  hook and the subagent spawner both read the gate at call time, a delegated
  turn's narrowing applies to anything the turn delegates.
- A refused route never reaches the model: `harness.RouteTurn` is consulted in
  `repl`, in `tui.Program.runTurn`, and in `runOnce` before `Loop.Run`.

Prompts are not the boundary. `prompt.WorkspaceContract` tells the model what
its mode means, but deleting it cannot grant a capability: the gate denies the
call regardless.

---

## 8. Session state

The session keeps the active workspace durably; the turn handler is transient
and derived.

```text
session
├── workspace            active authority, persisted (session.SetAuthority)
├── effective model      resolved per workspace role, persisted
└── turn
    ├── intent           classified per request
    ├── handler          derived from intent
    └── admitted gate    active gate value for the turn
```

`SwitchWorkspace` persists the active workspace, the effective model, and moves
the gate together. `RouteTurn` never persists anything.

---

## 9. Truthful state

The UI is a projection, never a router. It consumes the routing result and
renders two facts without conflating them:

```text
activeWorkspace     the session's authority boundary  (persistent)
turn.handler        the semantics answering the turn  (ephemeral)
turn.intent         the classified request            (ephemeral)
effectiveAuthority  the authority available this turn (ephemeral)
```

The ephemeral projection lives only for the turn. The composer prefix carries
it directly after the persistent workspace:

```text
BUILD + READ          build · ask/read >       (BUILD stays active)
BUILD + INVESTIGATE   build · fuzz/investigate >
FUZZ + READ           fuzz · ask/read >
BUILD + EXECUTE       build >                  (workspace already answers)
ASK + READ            ask >                    (workspace already answers)
```

The TUI receives the route through `WorkspaceHooks.PrepareTurn` (which wraps
`harness.RouteTurn`), stores it as `State.Status.Turn`, and clears it when the
turn reaches `AssistantDone` or `TurnCancelled`. `Status.Workspace` is never
written by a route, so the active workspace badge cannot move because a turn
was delegated. The plain REPL prints the same indicator in the canonical
`BUILD · ASK / READ` form via `workspace.Route.Indicator`.

A refused turn publishes no indicator and no workspace change: it reports the
authority it needs and the command that grants it (`this request needs BUILD
authority; switch with /build and resend it`). The refusal happens before the
model is called, so no artificial turn is simulated.

### Canonical lifecycle

```text
idle
  ↓  user: "What is Go?"          (activeWorkspace = BUILD)
turn routed: READ intent → ASK handler → ASK effective authority
  ↓
turn active:  build · ask/read
  ↓  answer streams under READ authority
turn completed
  ↓
turn state cleared
  ↓
idle: BUILD, unchanged — the next turn is routed independently
```

> The UI must never represent turn delegation as an implicit workspace
> transition.

---

## 10. Transition proposals

Delegation answers an informational request *inside* the active workspace. A
strong prediction can additionally *recommend* a real workspace move, without
ever moving authority by itself:

```text
ClassifyPrediction → Prediction (intent + evidence)
        ↓
ProposeTransition  → TransitionProposal?      (policy: safe and strong enough?)
        ↓
TransitionPrompt                              (TUI decision surface)
        ↓
human: accept / reject / cancel
        ↓
SwitchWorkspace                               (the one authority owner)
        ↓
RouteTurn → Loop.Run → authorityGate           (unchanged enforcement)
```

Every arrow above the human decision is pure. `workspace.ClassifyPrediction` and
`workspace.ProposeTransition` change nothing; `WorkspaceHooks.Predict` is
side-effect-free by contract, unlike `PrepareTurn`, which installs the turn's
handler. Only an accepted proposal reaches `harness.SwitchWorkspace` — the same
owner `/ask`, `/fuzz`, and `/build` use — so authority moves through exactly one
seam and is still enforced by the runtime gate.

### What may be proposed

Only safe narrowings, and only on strong evidence:

```text
ACTIVE   INTENT        PROPOSAL
────────────────────────────────
ASK      READ          none
ASK      INVESTIGATE   none   (would widen)
ASK      EXECUTE       none   (would widen)

BUILD    READ          switch to ASK
BUILD    INVESTIGATE   switch to FUZZ
BUILD    EXECUTE       none   (already the workspace)

FUZZ     READ          switch to ASK
FUZZ     INVESTIGATE   none   (already the workspace)
FUZZ     EXECUTE       none   (would widen)
```

Three guards make the table safe even if it were edited wrongly: the policy
refuses an unresolved workspace, refuses any target `active.Encompasses` does
not cover, and refuses evidence below `proposalThreshold` (currently `strong`).
A proposal is a request, never a grant.

### What the decision surface does

The TUI stores the proposal as `State.Transition` and renders it as a compact
centered dialog:

```text
Switch to ASK?

This request appears informational.

[ Keep BUILD ]   [ ASK ]
Enter to confirm · Esc keeps BUILD
```

The highlight opens on **keep current**, and left/right move it without wrapping,
so a stray Enter never changes authority. **Enter** confirms the highlighted
choice; **Esc** declines the transition and runs the turn under the current
workspace (whose own delegation still applies); **Ctrl+C** abandons the pending
turn and keeps the typed prompt. Each choice carries its workspace's own semantic
accent; no percentage is ever shown.

While the dialog is open the composer is hidden and key handling is captured, so
no turn — and therefore no provider request, tool, or mutation — can start. The
proposal is cleared on accept, reject, cancel, turn completion, cancellation,
history replacement, an explicit workspace change, or a clear, so it can never
survive into another turn. Because an accepted proposal goes through
`SwitchWorkspace`, the session is the same one: only its workspace changed.

The plain REPL keeps its existing behaviour — delegation and refusal — and does
not raise the interactive proposal. The proposal is a full-screen decision
surface; a line-oriented equivalent would need its own y/N contract and is
deliberately out of scope for this slice.

## 11. Tests

The invariants are behavioural: they assert that a call never executes, not
that a classifier returned a string.

| Invariant | Test |
| --------- | ---- |
| A — ASK cannot escalate to BUILD | `cmd/ohgo.TestAskRefusesExecutionWithoutCallingTheModel` |
| B — ASK cannot escalate to FUZZ | `cmd/ohgo.TestAskRefusesInvestigation` |
| C — BUILD informational delegation preserves BUILD | `cmd/ohgo.TestBuildDelegatesReadTurnsToAsk`, `TestBuildExecutesExplicitMutation` |
| D — FUZZ informational delegation preserves FUZZ | `cmd/ohgo.TestFuzzDelegatesReadTurnsAndStaysFuzz` |
| E — a FUZZ finding does not authorize mutation | `cmd/ohgo.TestFuzzFindingDoesNotAuthorizeMutation` |
| F — mutation requires explicit BUILD intent | `cmd/ohgo.TestFuzzRefusesExecutionWithoutTheModel`, `workspace.TestRouteTurnTransitionMatrix` |
| G — workspace and turn handler are distinct | `workspace.TestRouteTurnNeverChangesTheActiveWorkspace`, `cmd/ohgo.TestRouteTurnInstallsTheHandlerForDelegatedTurns` |
| H — authority is runtime-enforced, not prompt-enforced | `cmd/ohgo.TestAuthorityIsEnforcedWithoutPromptInstructions` |
| Classifier contract | `workspace.TestClassifyReadsCapabilityNotGrammar`, `TestClassifyFallsBackToTheLowerAuthority` |
| UI consults the router | `tui.TestRunTurnRefusesBlockedIntentBeforeTheModel`, `tui.TestRunTurnProjectsDelegatedRouteTruthfully` |
| UI never moves the workspace | `tui.TestDelegatedTurnNeverSwitchesWorkspace`, `tui.TestRenderShowsTurnSemanticsWithoutMovingTheWorkspace` |
| Turn state is ephemeral | `tui.TestTurnSemanticsAreClearedOnCompletion`, `cmd/ohgo.TestDelegatedTurnDoesNotLeakIntoTheNextRoute` |
| Effective authority is bounded | `workspace.TestRouteEffectiveAuthorityNeverExceedsTheWorkspace`, `cmd/ohgo.TestRefusedRouteLeavesAuthorityUntouched` |
| Cross-workspace scenario | `cmd/ohgo.TestCrossWorkspaceSessionKeepsAuthority` |
| Classifier evidence levels | `workspace.TestClassifyPredictionReportsStrength`, `TestClassifyPredictionMarksContestedPredictionsAmbiguous` |
| Policy matrix and floor | `workspace.TestProposeTransitionMatrix`, `TestProposalThresholdRequiresStrongEvidence`, `TestProposeTransitionRefusesWeakAndAmbiguousEvidence` |
| No proposed escalation | `workspace.TestProposeNeverEscalates`, `cmd/ohgo.TestTurnTransitionNeverProposesEscalation` |
| Prediction moves nothing | `cmd/ohgo.TestTurnTransitionPredictsWithoutMovingAuthority` |
| Acceptance uses the owner | `tui.TestAcceptedProposalSwitchesThroughTheOwner`, `cmd/ohgo.TestAcceptedTransitionUsesTheAuthoritativeSwitch` |
| Rejection cannot escalate | `tui.TestRejectedProposalKeepsAuthorityAndRunsTheTurn`, `TestEscRejectsWithoutSwitching`, `cmd/ohgo.TestRejectedTransitionLeavesAuthorityUntouched` |
| No execution while pending | `tui.TestProposalBlocksTheTurnUntilDecided`, `TestAcceptedSwitchFailureDoesNotRunTheTurn` |
| Same session across a switch | `cmd/ohgo.TestTransitionKeepsTheSameSession` |
| Ephemeral proposal state | `tui.TestStaleProposalCannotSurviveALifecycleEvent`, `TestProposalNavigationClampsAtTheEnds` |
| No fabricated confidence | `workspace.TestTransitionProposalNeverCarriesAProbability`, `cmd/ohgo.TestTurnTransitionCarriesNoProbability`, `tui.TestTransitionPromptRendersTheDecision` |

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
- automatic authority escalation of any kind;
- a calibrated confidence score (evidence levels only);
- an interactive transition prompt in the plain REPL;
- a new session, task file, or persistence layer for a workspace move.

The design is a small classifier, an explicit transition table, and a runtime
gate. It optimises for predictable behaviour, not maximum autonomy.
